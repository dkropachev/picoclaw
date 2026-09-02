package workflows

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestWorkflowRuntimeConstructorRequiresBrokerAndDoesNotOpenProvider(t *testing.T) {
	previous := database.RuntimeClient()
	database.InstallProcessClient(nil)
	restoreProviderAuthority := database.SuspendProviderTestAuthority()
	allowUnfencedWorkflowProviderForTests.Store(false)
	t.Cleanup(func() {
		allowUnfencedWorkflowProviderForTests.Store(true)
		restoreProviderAuthority()
		database.InstallProcessClient(previous)
	})
	workspace := filepath.Join(t.TempDir(), "must-not-exist")
	onlineFence, err := database.AcquireOnlineFence(filepath.Join(t.TempDir(), "launcher-home"))
	if err != nil {
		t.Fatal(err)
	}
	if local := newLocalFileRunStore(workspace); local.brokerErr == nil ||
		database.CodeOf(local.brokerErr) != database.CodeUnauthorized {
		t.Fatalf("online-fenced local store = %#v", local)
	}
	if err := onlineFence.Close(); err != nil {
		t.Fatal(err)
	}
	store := NewFileRunStore(workspace)
	if _, err := store.GetRun(t.Context(), "run"); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("GetRun() error = %v", err)
	}
	if _, err := NewSQLiteRunStore(workspace); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("NewSQLiteRunStore() error = %v", err)
	}
	if _, statErr := os.Lstat(workspace); !os.IsNotExist(statErr) {
		t.Fatalf("runtime constructor touched provider root: %v", statErr)
	}
}

type workflowBrokerFixture struct {
	home      string
	workspace string
	handler   *BrokerHandler
	server    *database.Server
	client    *database.Client
	store     *FileRunStore
}

func newWorkflowBrokerFixture(t *testing.T) *workflowBrokerFixture {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	if err := config.SaveConfig(filepath.Join(home, "config.json"), cfg); err != nil {
		t.Fatalf("save broker test config: %v", err)
	}
	handler, err := NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatalf("NewBrokerHandler() error = %v", err)
	}
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		t.Fatalf("StartServer() error = %v", err)
	}
	client, err := database.Connect(home)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	fixture := &workflowBrokerFixture{
		home: home, workspace: workspace, handler: handler, server: server, client: client,
	}
	fixture.store = fixture.storeFor(client)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("server.Close() error = %v", err)
		}
		handler.mu.RLock()
		closed := handler.closed
		handler.mu.RUnlock()
		handler.store.database.mu.Lock()
		poolOpen := handler.store.database.db != nil
		handler.store.database.mu.Unlock()
		if !closed || poolOpen {
			t.Errorf("broker handler cleanup = closed=%t pool_open=%t", closed, poolOpen)
		}
	})
	return fixture
}

func (fixture *workflowBrokerFixture) storeFor(client *database.Client) *FileRunStore {
	return &FileRunStore{
		root: filepath.Join(fixture.workspace, "workflow_runs"), workspace: fixture.workspace,
		broker: client, storeID: fixture.handler.storeID,
	}
}

func TestWorkflowBrokerRunCRUDAndEvents(t *testing.T) {
	fixture := newWorkflowBrokerFixture(t)
	store := fixture.store
	if store.StoreID() != database.StoreID(workflowDefaultStoreName) {
		t.Fatalf("StoreID() = %q", store.StoreID())
	}
	now := time.Now().UTC()
	run := &Run{
		ID: "wr_broker_crud", WorkflowRef: "workflows/broker.yml", Status: RunStatusRunning,
		CreatedAt: now, Inputs: map[string]any{"input": "value"}, Outputs: map[string]any{},
	}
	if err := store.CreateRun(t.Context(), run); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if run.storeVersion != 1 || run.UpdatedAt.IsZero() {
		t.Fatalf("CreateRun() did not round-trip mutations: %#v version=%d", run, run.storeVersion)
	}
	if err := store.CreateRun(t.Context(), cloneRun(run)); !errors.Is(err, ErrRunAlreadyExists) {
		t.Fatalf("duplicate CreateRun() error = %v", err)
	}
	got, err := store.GetRun(t.Context(), run.ID)
	if err != nil || got.Inputs["input"] != "value" {
		t.Fatalf("GetRun() = %#v, %v", got, err)
	}
	if _, boundedErr := store.GetRunBounded(t.Context(), run.ID, 1); boundedErr == nil {
		t.Fatal("GetRunBounded() accepted undersized limit")
	}
	if _, boundedErr := store.GetRunBounded(t.Context(), run.ID, 0); !errors.Is(boundedErr, os.ErrInvalid) {
		t.Fatalf("GetRunBounded(invalid) error = %v", boundedErr)
	}
	stale := cloneRun(got)
	got.Outputs = map[string]any{"updated": true}
	if updateErr := store.UpdateRun(t.Context(), got); updateErr != nil {
		t.Fatalf("UpdateRun() error = %v", updateErr)
	}
	if got.storeVersion != 2 {
		t.Fatalf("UpdateRun() version = %d, want 2", got.storeVersion)
	}
	stale.Outputs = map[string]any{"stale": true}
	if updateErr := store.UpdateRun(t.Context(), stale); !errors.Is(updateErr, ErrRunVersionConflict) {
		t.Fatalf("stale UpdateRun() error = %v", updateErr)
	}
	listed, err := store.ListRuns(t.Context())
	if err != nil || len(listed) != 1 || listed[0].ID != run.ID {
		t.Fatalf("ListRuns() = %#v, %v", listed, err)
	}
	event := RunEvent{
		Kind: "workflow.test", RunID: run.ID, Message: "broker event",
		Payload: map[string]any{"sequence": float64(1)},
	}
	if appendErr := store.AppendEvent(t.Context(), event); appendErr != nil {
		t.Fatalf("AppendEvent() error = %v", appendErr)
	}
	events, err := store.Events(t.Context(), run.ID)
	if err != nil || len(events) != 1 || events[0].Message != event.Message {
		t.Fatalf("Events() = %#v, %v", events, err)
	}
	canceled, err := store.CancelRun(t.Context(), run.ID, "operator request")
	if err != nil || canceled.Status != RunStatusCanceled || canceled.CancelReason != "operator request" {
		t.Fatalf("CancelRun() = %#v, %v", canceled, err)
	}

	second := &Run{
		ID: "wr_broker_delete", WorkflowRef: "workflows/broker.yml", Status: RunStatusSucceeded,
		CreatedAt: now.Add(-time.Hour), CompletedAt: ptrTime(now.Add(-time.Hour)),
	}
	if createErr := store.CreateRun(t.Context(), second); createErr != nil {
		t.Fatalf("CreateRun(second) error = %v", createErr)
	}
	if deleteErr := store.DeleteRun(t.Context(), second.ID); deleteErr != nil {
		t.Fatalf("DeleteRun() error = %v", deleteErr)
	}
	if _, getErr := store.GetRun(t.Context(), second.ID); !errors.Is(getErr, os.ErrNotExist) {
		t.Fatalf("GetRun(deleted) error = %v", getErr)
	}

	old := &Run{
		ID: "wr_broker_prune", WorkflowRef: "workflows/broker.yml", Status: RunStatusSucceeded,
		CreatedAt: now.Add(-2 * time.Hour), CompletedAt: ptrTime(now.Add(-time.Hour)),
	}
	if createErr := store.CreateRun(t.Context(), old); createErr != nil {
		t.Fatalf("CreateRun(old) error = %v", createErr)
	}
	deleted, err := store.PruneTerminalRuns(t.Context(), now)
	if err != nil || deleted != 1 {
		t.Fatalf("PruneTerminalRuns() = %d, %v", deleted, err)
	}
}

func TestWorkflowBrokerCreateLimitAndHumanTaskLifecycle(t *testing.T) {
	fixture := newWorkflowBrokerFixture(t)
	store := fixture.store
	now := time.Now().UTC()
	first := &Run{ID: "wr_limit_first", WorkflowRef: "workflows/limit.yml", Status: RunStatusRunning, CreatedAt: now}
	if err := store.CreateRunIfUnderLimit(t.Context(), first, 1); err != nil {
		t.Fatalf("CreateRunIfUnderLimit(first) error = %v", err)
	}
	second := &Run{ID: "wr_limit_second", WorkflowRef: "workflows/limit.yml", Status: RunStatusRunning, CreatedAt: now}
	if err := store.CreateRunIfUnderLimit(t.Context(), second, 1); !errors.Is(err, ErrRunConcurrencyLimit) {
		t.Fatalf("CreateRunIfUnderLimit(second) error = %v", err)
	}
	if _, err := store.CancelRun(t.Context(), first.ID, "free capacity"); err != nil {
		t.Fatalf("CancelRun(limit) error = %v", err)
	}

	executor := &Executor{WorkspaceDir: fixture.workspace, Store: store}
	workflow := parseWorkflow(t, `
name: Broker human task
on: {manual: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: review
        uses: human/task
        with: {title: Review, questions: [{id: approve}]}
`)
	started, err := executor.Run(t.Context(), RunRequest{
		Workflow: workflow, WorkflowRef: "workflows/broker-human.yml",
	})
	if err != nil || started.Status != RunStatusWaiting {
		t.Fatalf("Executor.Run() = %#v, %v", started, err)
	}
	tasks, err := store.ListHumanTasks(t.Context(), started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() = %#v, %v", tasks, err)
	}
	task := tasks[0]
	request := HumanTaskResumeRequest{
		ExpectedRevision: task.Revision, InputHash: task.InputHash,
		ResponseID: "broker-response", Response: true,
		resumeLease: 2 * time.Minute, maxConcurrent: 4,
	}
	claimed, answered, duplicate, err := store.ClaimHumanTask(
		t.Context(), started.RunID, task.ID, request,
	)
	if err != nil || duplicate || answered.Status != HumanTaskStatusAnswered ||
		claimed.execution == nil || claimed.execution.Resume == nil {
		t.Fatalf("ClaimHumanTask() = run=%#v task=%#v duplicate=%t err=%v", claimed, answered, duplicate, err)
	}
	claimToken := claimed.execution.Resume.Token
	if renewErr := store.RenewHumanTaskClaim(
		t.Context(), started.RunID, task.ID, claimToken, 3*time.Minute,
	); renewErr != nil {
		t.Fatalf("RenewHumanTaskClaim() error = %v", renewErr)
	}
	_, _, duplicate, err = store.ClaimHumanTask(t.Context(), started.RunID, task.ID, request)
	if err != nil || !duplicate {
		t.Fatalf("idempotent ClaimHumanTask() duplicate=%t err=%v", duplicate, err)
	}

	started2, err := executor.Run(t.Context(), RunRequest{
		Workflow: workflow, WorkflowRef: "workflows/broker-human-cancel.yml",
	})
	if err != nil || started2.Status != RunStatusWaiting {
		t.Fatalf("second Executor.Run() = %#v, %v", started2, err)
	}
	tasks2, err := store.ListHumanTasks(t.Context(), started2.RunID)
	if err != nil || len(tasks2) != 1 {
		t.Fatalf("second ListHumanTasks() = %#v, %v", tasks2, err)
	}
	canceled, err := store.CancelHumanTask(t.Context(), started2.RunID, tasks2[0].ID, "declined")
	if err != nil || canceled.Status != RunStatusCanceled {
		t.Fatalf("CancelHumanTask() = %#v, %v", canceled, err)
	}
}

func TestWorkflowBrokerPaginatesTasksAndEvents(t *testing.T) {
	fixture := newWorkflowBrokerFixture(t)
	now := time.Now().UTC()
	run := &Run{
		ID: "wr_paginated_children", WorkflowRef: "workflows/pagination.yml",
		Status: RunStatusWaiting, CreatedAt: now,
		humanTasks: make(map[string]WorkflowHumanTask, workflowRPCPageItems+7),
	}
	for index := 0; index < workflowRPCPageItems+7; index++ {
		id := fmt.Sprintf("task_%03d", index)
		run.humanTasks[id] = WorkflowHumanTask{
			ID: id, RunID: run.ID, WorkflowRef: run.WorkflowRef, JobID: "job", StepID: id,
			Status: HumanTaskStatusWaiting, Revision: 1, InputHash: "hash", Title: "Review",
			Questions: []any{}, CreatedAt: now, UpdatedAt: now,
		}
	}
	if err := fixture.store.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < workflowRPCPageItems+9; index++ {
		if err := fixture.store.AppendEvent(t.Context(), RunEvent{
			Kind: "workflow.page", RunID: run.ID, Message: fmt.Sprintf("event-%03d", index),
		}); err != nil {
			t.Fatalf("AppendEvent(%d): %v", index, err)
		}
	}
	tasks, err := fixture.store.ListHumanTasks(t.Context(), run.ID)
	if err != nil || len(tasks) != workflowRPCPageItems+7 {
		t.Fatalf("paginated tasks = %d, %v", len(tasks), err)
	}
	events, err := fixture.store.Events(t.Context(), run.ID)
	if err != nil || len(events) != workflowRPCPageItems+9 {
		t.Fatalf("paginated events = %d, %v", len(events), err)
	}
}

func TestWorkflowBrokerConcurrentClientsShareOnePool(t *testing.T) {
	fixture := newWorkflowBrokerFixture(t)
	const clients = 20
	var wait sync.WaitGroup
	errorsOut := make(chan error, clients)
	for index := range clients {
		client, err := database.Connect(fixture.home)
		if err != nil {
			t.Fatalf("Connect(%d) error = %v", index, err)
		}
		store := fixture.storeFor(client)
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			run := &Run{
				ID: fmt.Sprintf("wr_concurrent_%02d", index), WorkflowRef: "workflows/concurrent.yml",
				Status: RunStatusRunning, CreatedAt: time.Now().UTC(),
			}
			if err := store.CreateRun(context.Background(), run); err != nil {
				errorsOut <- err
				return
			}
			if _, err := store.GetRun(context.Background(), run.ID); err != nil {
				errorsOut <- err
			}
		}(index)
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Errorf("concurrent broker operation: %v", err)
	}
	runs, err := fixture.store.ListRuns(t.Context())
	if err != nil || len(runs) != clients {
		t.Fatalf("ListRuns() after concurrent clients = %d, %v", len(runs), err)
	}
	fixture.handler.store.database.mu.Lock()
	poolOpen := fixture.handler.store.database.db != nil
	fixture.handler.store.database.mu.Unlock()
	if !poolOpen {
		t.Fatal("broker did not retain its stable workflow pool")
	}
	var response workflowRunsResponse
	err = fixture.client.Call(
		t.Context(), workflowRPCDomain, workflowRPCVersion, workflowRPCOperationListRuns,
		workflowTargetRequest{StoreID: "global.auth"}, &response,
	)
	if database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("foreign store ID error = %v, want Unauthorized", err)
	}
}

func TestWorkflowBrokerConfiguredWorkspacesAreIsolated(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	primaryWorkspace := filepath.Join(home, "workspace")
	agentWorkspace := filepath.Join(home, "agents", "worker")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = primaryWorkspace
	cfg.Agents.List = []config.AgentConfig{
		{ID: "worker", Workspace: agentWorkspace},
		{ID: "shared", Workspace: agentWorkspace},
	}
	handler, err := NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(handler.workspaces) != 2 || len(handler.selectors) != 2 {
		t.Fatalf("configured maps = workspaces %d selectors %d", len(handler.workspaces), len(handler.selectors))
	}
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		_ = handler.Close()
		t.Fatal(err)
	}
	client, err := database.Connect(home)
	if err != nil {
		_ = server.Close(context.Background())
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Close(ctx)
		}
	})

	primaryID, err := resolveWorkflowBrokerStoreID(t.Context(), client, primaryWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	agentID, err := resolveWorkflowBrokerStoreID(t.Context(), client, agentWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if primaryID != database.StoreID(workflowDefaultStoreName) || !agentID.Valid() || agentID == primaryID {
		t.Fatalf("resolved IDs = primary %q agent %q", primaryID, agentID)
	}
	newStore := func(workspace string, storeID database.StoreID) *FileRunStore {
		return &FileRunStore{
			root: filepath.Join(workspace, "workflow_runs"), workspace: workspace,
			broker: client, storeID: storeID,
		}
	}
	primary := newStore(primaryWorkspace, primaryID)
	agent := newStore(agentWorkspace, agentID)
	now := time.Now().UTC()
	for label, store := range map[string]*FileRunStore{"primary": primary, "agent": agent} {
		run := &Run{
			ID: "wr_same_identity", WorkflowRef: "workflows/isolated.yml",
			Status: RunStatusRunning, CreatedAt: now, Inputs: map[string]any{"owner": label},
		}
		if createErr := store.CreateRun(t.Context(), run); createErr != nil {
			t.Fatalf("CreateRun(%s): %v", label, createErr)
		}
	}
	primaryRun, err := primary.GetRun(t.Context(), "wr_same_identity")
	if err != nil || primaryRun.Inputs["owner"] != "primary" {
		t.Fatalf("primary run = %#v, %v", primaryRun, err)
	}
	agentRun, err := agent.GetRun(t.Context(), "wr_same_identity")
	if err != nil || agentRun.Inputs["owner"] != "agent" {
		t.Fatalf("agent run = %#v, %v", agentRun, err)
	}

	const perWorkspace = 10
	var wait sync.WaitGroup
	errorsOut := make(chan error, perWorkspace*2)
	for index := range perWorkspace {
		for _, store := range []*FileRunStore{primary, agent} {
			wait.Add(1)
			go func(index int, store *FileRunStore) {
				defer wait.Done()
				run := &Run{
					ID:          fmt.Sprintf("wr_dynamic_%02d", index),
					WorkflowRef: "workflows/isolated.yml", Status: RunStatusRunning,
					CreatedAt: time.Now().UTC(),
				}
				if createErr := store.CreateRun(context.Background(), run); createErr != nil {
					errorsOut <- createErr
				}
			}(index, store)
		}
	}
	wait.Wait()
	close(errorsOut)
	for operationErr := range errorsOut {
		t.Error(operationErr)
	}
	for label, store := range map[string]*FileRunStore{"primary": primary, "agent": agent} {
		runs, listErr := store.ListRuns(t.Context())
		if listErr != nil || len(runs) != perWorkspace+1 {
			t.Fatalf("ListRuns(%s) = %d, %v", label, len(runs), listErr)
		}
	}
	if _, err := resolveWorkflowBrokerStoreID(
		t.Context(), client, filepath.Join(home, "uncataloged"),
	); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("uncataloged workspace error = %v", err)
	}

	primaryPool := handler.workspaces[primaryID].store.database
	agentPool := handler.workspaces[agentID].store.database
	if primaryPool == nil || agentPool == nil || primaryPool == agentPool {
		t.Fatalf("retained pools = primary %p agent %p", primaryPool, agentPool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatal(err)
	}
	closed = true
	for storeID, workspace := range handler.workspaces {
		workspace.store.database.mu.Lock()
		poolOpen := workspace.store.database.db != nil
		workspace.store.database.mu.Unlock()
		if poolOpen {
			t.Fatalf("workflow pool %q remained open", storeID)
		}
	}
}

func TestWorkflowBrokerRuntimeConstructorProcess(t *testing.T) {
	if os.Getenv("PICOCLAW_WORKFLOW_BROKER_CONSTRUCTOR_HELPER") == "1" {
		client, home, err := database.ConnectInherited(context.Background())
		if err != nil || client == nil || home == "" {
			t.Fatalf("ConnectInherited() = %#v, %q, %v", client, home, err)
		}
		workspace := os.Getenv("PICOCLAW_WORKFLOW_BROKER_WORKSPACE")
		store := NewFileRunStore(workspace)
		if store.StoreID() != database.StoreID(workflowDefaultStoreName) || store.database != nil {
			t.Fatalf("runtime store = id=%q local_pool=%#v", store.StoreID(), store.database)
		}
		run := &Run{
			ID: "wr_runtime_constructor", WorkflowRef: "workflows/runtime.yml",
			Status: RunStatusRunning, CreatedAt: time.Now().UTC(),
		}
		if err := store.CreateRun(context.Background(), run); err != nil {
			t.Fatalf("runtime CreateRun() error = %v", err)
		}
		wrongWorkspace := workspace + "-uncataloged"
		wrong := NewFileRunStore(wrongWorkspace)
		if wrong.database != nil || wrong.brokerErr == nil {
			t.Fatalf("uncataloged runtime store fell back locally: %#v", wrong)
		}
		if err := wrong.CreateRun(context.Background(), &Run{
			ID: "wr_must_fail_closed", WorkflowRef: "workflows/runtime.yml",
			Status: RunStatusRunning, CreatedAt: time.Now().UTC(),
		}); err == nil {
			t.Fatal("uncataloged runtime store mutation succeeded")
		}
		if _, err := os.Stat(filepath.Join(wrongWorkspace, "state", "workflows.db")); !os.IsNotExist(err) {
			t.Fatalf("uncataloged runtime store created a local database: %v", err)
		}
		return
	}

	fixture := newWorkflowBrokerFixture(t)
	authority, err := database.InheritedAuthorityEnvironment(fixture.home)
	if err != nil {
		t.Fatalf("InheritedAuthorityEnvironment() error = %v", err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestWorkflowBrokerRuntimeConstructorProcess$")
	command.Env = append(
		os.Environ(),
		"PICOCLAW_WORKFLOW_BROKER_CONSTRUCTOR_HELPER=1",
		"PICOCLAW_WORKFLOW_BROKER_WORKSPACE="+fixture.workspace,
		config.EnvHome+"="+fixture.home,
		config.EnvConfig+"="+filepath.Join(fixture.home, "config.json"),
		authority,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("runtime constructor helper: %v\n%s", err, output)
	}
	if _, err := fixture.store.GetRun(t.Context(), "wr_runtime_constructor"); err != nil {
		t.Fatalf("broker did not receive runtime constructor write: %v", err)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
