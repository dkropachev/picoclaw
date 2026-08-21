package workflows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
)

type fakeToolRunner struct {
	requests []ToolRequest
	outputs  map[string]any
	err      error
}

func (r *fakeToolRunner) RunTool(_ context.Context, req ToolRequest) (map[string]any, error) {
	r.requests = append(r.requests, req)
	if r.err != nil {
		return nil, r.err
	}
	if r.outputs != nil {
		return cloneMap(r.outputs), nil
	}
	return map[string]any{"text": "tool:" + req.Name}, nil
}

type fakeAgentRunner struct {
	requests []AgentRequest
	outputs  map[string]any
	err      error
}

type repositoryProfileAgentStub struct {
	profile    RepositoryReviewModelProfile
	profileErr error
	request    AgentRequest
}

func (r *repositoryProfileAgentStub) ResolveRepositoryReviewProfile(
	_ context.Context,
	_ string,
	_ []string,
) (RepositoryReviewModelProfile, error) {
	return r.profile, r.profileErr
}

func (r *repositoryProfileAgentStub) RunAgent(
	_ context.Context,
	req AgentRequest,
) (map[string]any, error) {
	r.request = req
	if req.CallAdmission != nil {
		if err := req.CallAdmission(); err != nil {
			return nil, err
		}
	}
	if req.UsageObserver != nil {
		if err := req.UsageObserver(AgentUsage{Model: "review-a", TotalTokens: 3}); err != nil {
			return nil, err
		}
	}
	return map[string]any{"text": "ok"}, nil
}

type usageReportingAgentRunner struct {
	usage []AgentUsage
}

func (r *usageReportingAgentRunner) RunAgent(
	_ context.Context,
	req AgentRequest,
) (map[string]any, error) {
	for _, usage := range r.usage {
		if req.UsageObserver == nil {
			return nil, errors.New("usage observer is nil")
		}
		if err := req.UsageObserver(usage); err != nil {
			return nil, err
		}
	}
	return map[string]any{"text": "ok", "usage": append([]AgentUsage(nil), r.usage...)}, nil
}

type retrySourceTrackingStore struct {
	RunStore
	sourceID    string
	sourceReads int
}

func (s *retrySourceTrackingStore) GetRun(
	ctx context.Context,
	runID string,
) (*Run, error) {
	if runID == s.sourceID {
		s.sourceReads++
	}
	return s.RunStore.GetRun(ctx, runID)
}

func (r *fakeAgentRunner) RunAgent(_ context.Context, req AgentRequest) (map[string]any, error) {
	r.requests = append(r.requests, req)
	if r.err != nil {
		return nil, r.err
	}
	if r.outputs != nil {
		return cloneMap(r.outputs), nil
	}
	return map[string]any{"text": req.Message}, nil
}

func TestWorkflowWorkspaceCleanupTracksFrozenScopesAndJoinsReleaseErrors(t *testing.T) {
	if cleanup := (*workflowWorkspaceCleanup)(nil); cleanup.releaseAll() != nil {
		t.Fatal("nil workspace cleanup returned an error")
	}
	var nilCleanup *workflowWorkspaceCleanup
	nilCleanup.track("")
	nilCleanup.trackFrozen("")

	runner := &fakeToolRunner{err: errors.New("release failed")}
	cleanup := &workflowWorkspaceCleanup{runner: runner}
	cleanup.track("session-a")
	cleanup.track("session-b")
	cleanup.released("session-b")
	token, err := storeNativeFrozenGitScope(
		ExecutionContext{RunID: "cleanup-run", WorkflowRef: "workflows/cleanup.yml"},
		[]map[string]any{{"path": "service.go"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	cleanup.trackFrozen(token)
	if err := cleanup.releaseAll(); err == nil || !strings.Contains(err.Error(), "release failed") {
		t.Fatalf("releaseAll error = %v", err)
	}
	if len(runner.requests) != 1 || runner.requests[0].Session != "session-a" ||
		runner.requests[0].Args["action"] != "release" {
		t.Fatalf("release requests = %#v", runner.requests)
	}
	if _, err := consumeNativeFrozenGitScope(
		ExecutionContext{RunID: "cleanup-run", WorkflowRef: "workflows/cleanup.yml"}, token,
	); err == nil {
		t.Fatal("releaseAll left a frozen scope available")
	}
}

func TestRepositoryBugFinderRepositoryInputRejectsSecretsAndMissingValues(t *testing.T) {
	for _, value := range []any{nil, "", "   "} {
		if err := validateRepositoryBugFinderRepositoryInput(value); err == nil ||
			!strings.Contains(err.Error(), "required") {
			t.Fatalf("missing repository %#v error = %v", value, err)
		}
	}
	for _, value := range []any{"owner/repo", "https://github.com/owner/repo.git"} {
		if err := validateRepositoryBugFinderRepositoryInput(value); err != nil {
			t.Fatalf("safe repository %#v error = %v", value, err)
		}
	}
	for _, value := range []any{
		"https://token@github.com/owner/repo.git",
		"https://github.com/owner/repo.git?token=secret",
		"https://github.com/owner/repo.git#fragment",
	} {
		if err := validateRepositoryBugFinderRepositoryInput(value); err == nil ||
			!strings.Contains(err.Error(), "credentialed or parameterized") {
			t.Fatalf("unsafe repository %#v error = %v", value, err)
		}
	}
}

func TestRepositoryReviewAgentContextReservesControllerDeadline(t *testing.T) {
	background, cancel := repositoryReviewAgentContext(context.Background(), false)
	cancel()
	if background != context.Background() {
		t.Fatal("ordinary agent context was replaced")
	}
	unbounded, cancel := repositoryReviewAgentContext(context.Background(), true)
	cancel()
	if _, bounded := unbounded.Deadline(); bounded {
		t.Fatal("unbounded repository review gained a deadline")
	}

	parent, parentCancel := context.WithDeadline(context.Background(), time.Now().Add(80*time.Millisecond))
	defer parentCancel()
	child, cancel := repositoryReviewAgentContext(parent, true)
	defer cancel()
	parentDeadline, _ := parent.Deadline()
	childDeadline, bounded := child.Deadline()
	if !bounded || !childDeadline.Before(parentDeadline) {
		t.Fatalf("child deadline=%v parent=%v bounded=%t", childDeadline, parentDeadline, bounded)
	}

	expired, expiredCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expiredCancel()
	canceled, cancel := repositoryReviewAgentContext(expired, true)
	defer cancel()
	if canceled.Err() == nil {
		t.Fatal("expired repository review context was not canceled")
	}
}

func TestBindRepositoryReviewModelProfileValidatesAndFreezesResolution(t *testing.T) {
	base := map[string]any{"agent": "", "profile": map[string]any{
		"schema": "repository-bug-finder-v1", "models": " review-a, review-a; review-b ",
		"max_content_bytes": 128 << 10,
	}}
	if _, err := (&Executor{Agents: &fakeAgentRunner{}}).bindRepositoryReviewModelProfile(
		context.Background(), base,
	); err == nil || !strings.Contains(err.Error(), "model-profile-aware") {
		t.Fatalf("profile-unaware runtime error = %v", err)
	}
	plain := map[string]any{"profile": map[string]any{"schema": "custom"}}
	if got, err := (&Executor{Agents: &fakeAgentRunner{}}).bindRepositoryReviewModelProfile(
		context.Background(), plain,
	); err != nil || !reflect.DeepEqual(got, plain) {
		t.Fatalf("custom profile binding = (%#v, %v)", got, err)
	}

	stub := &repositoryProfileAgentStub{profile: RepositoryReviewModelProfile{
		Revision: "sha256:models", ReviewerModels: []string{"review-a", "review-b"}, MaxContentBytes: 64 << 10,
	}}
	bound, err := (&Executor{Agents: stub}).bindRepositoryReviewModelProfile(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	profile := bound["profile"].(map[string]any)
	if profile["model_graph_revision"] != "sha256:models" || profile["max_content_bytes"] != 64<<10 ||
		bound["resolved_max_content_bytes"] != 64<<10 ||
		!reflect.DeepEqual(profile["models"], []string{"review-a", "review-b"}) {
		t.Fatalf("bound repository review profile = %#v", bound)
	}
	if base["resolved_max_content_bytes"] != nil {
		t.Fatalf("profile binding mutated input = %#v", base)
	}

	stub.profileErr = errors.New("resolver unavailable")
	if _, err := (&Executor{Agents: stub}).bindRepositoryReviewModelProfile(context.Background(), base); err == nil ||
		!strings.Contains(err.Error(), "resolver unavailable") {
		t.Fatalf("resolver error = %v", err)
	}
	for _, empty := range []RepositoryReviewModelProfile{
		{},
		{Revision: "revision", MaxContentBytes: 1},
		{Revision: "revision", ReviewerModels: []string{"review-a"}},
	} {
		stub.profileErr = nil
		stub.profile = empty
		if _, err := (&Executor{Agents: stub}).bindRepositoryReviewModelProfile(
			context.Background(),
			base,
		); err == nil ||
			!strings.Contains(err.Error(), "empty result") {
			t.Fatalf("empty profile %#v error = %v", empty, err)
		}
	}
}

func TestRepositoryReviewModelNamesNormalizesBoundsAndSupportedInputs(t *testing.T) {
	if got := repositoryReviewModelNames(" a, b; a\n c "); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("string model names = %#v", got)
	}
	if got := repositoryReviewModelNames([]string{" a ", "", "b"}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("string-slice model names = %#v", got)
	}
	values := []any{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	if got := repositoryReviewModelNames(values); len(got) != 8 || got[7] != "h" {
		t.Fatalf("bounded model names = %#v", got)
	}
	if got := repositoryReviewModelNames(42); len(got) != 0 {
		t.Fatalf("unsupported model-name input = %#v", got)
	}
}

func TestRunStepTargetRejectsUnboundReviewScopeAndReportsAdmissionUsage(t *testing.T) {
	stub := &repositoryProfileAgentStub{}
	executor := &Executor{Agents: stub}
	execCtx := ExecutionContext{RunID: "step-run", WorkflowRef: "workflows/review.yml", StepID: "review"}
	if _, err := executor.runStepTarget(context.Background(), Step{Uses: "agent/main"}, map[string]any{
		"scope_content": "immutable_git", "scope": "not-a-scope",
	}, execCtx); err == nil || !strings.Contains(err.Error(), "immutable Git scope") {
		t.Fatalf("invalid immutable scope error = %v", err)
	}
	if _, err := executor.runStepTarget(context.Background(), Step{Uses: "agent/main"}, map[string]any{
		"scope_content": "frozen_git", "scope_snapshot": "short",
	}, execCtx); err == nil || !strings.Contains(err.Error(), "token is invalid") {
		t.Fatalf("invalid frozen scope error = %v", err)
	}

	var admitted, observed int
	executor.AgentCallAdmission = func(event AgentCallAdmissionEvent) error {
		admitted++
		if event.RunID != "step-run" || event.StepID != "review" {
			t.Fatalf("admission event = %#v", event)
		}
		return nil
	}
	executor.AgentUsageObserver = func(event AgentUsageEvent) error {
		observed++
		if event.Usage.Model != "review-a" || event.Usage.TotalTokens != 3 {
			t.Fatalf("usage event = %#v", event)
		}
		return nil
	}
	if _, err := executor.runStepTarget(
		context.Background(),
		Step{Uses: "agent/main"},
		map[string]any{},
		execCtx,
	); err != nil {
		t.Fatal(err)
	}
	if admitted != 1 || observed != 1 {
		t.Fatalf("admitted=%d observed=%d", admitted, observed)
	}

	stub.profileErr = errors.New("profile blocked")
	if _, err := executor.runStepTarget(context.Background(), Step{Uses: "function/review.repository"}, map[string]any{
		"action": "plan", "profile": map[string]any{"schema": "repository-bug-finder-v1"},
	}, execCtx); err == nil || !strings.Contains(err.Error(), "profile blocked") {
		t.Fatalf("unbound plan error = %v", err)
	}
}

type fakeRuntimeEventPublisher struct {
	events []runtimeevents.Event
}

func (p *fakeRuntimeEventPublisher) PublishNonBlocking(evt runtimeevents.Event) runtimeevents.PublishResult {
	p.events = append(p.events, evt)
	return runtimeevents.PublishResult{Matched: 1, Delivered: 1}
}

type cancelOnSucceededUpdateStore struct {
	RunStore
	canceled bool
}

func (s *cancelOnSucceededUpdateStore) UpdateRun(ctx context.Context, run *Run) error {
	if run != nil && run.Status == RunStatusSucceeded && !s.canceled {
		s.canceled = true
		now := time.Now().UTC()
		canceled := cloneRun(run)
		canceled.Status = RunStatusCanceled
		canceled.CancelReason = "late cancel"
		canceled.CancelRequestedAt = &now
		canceled.CompletedAt = &now
		canceled.UpdatedAt = now
		if err := s.RunStore.UpdateRun(ctx, canceled); err != nil {
			return err
		}
		*run = *cloneRun(canceled)
		return nil
	}
	return s.RunStore.UpdateRun(ctx, run)
}

func TestExecutorRunsFunctionWorkflowWithIfAndOutputs(t *testing.T) {
	registry := NewFunctionRegistry()
	if err := registry.Register(
		"echo",
		func(_ context.Context, args map[string]any, _ ExecutionContext) (map[string]any, error) {
			return map[string]any{"text": args["text"]}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, `
name: Function
on:
  workflow_call:
    inputs:
      text:
        type: string
        required: true
    outputs:
      result:
        value: ${{ jobs.main.outputs.result }}
jobs:
  main:
    runs-on: picoclaw
    outputs:
      result: ${{ steps.echo.outputs.text }}
    steps:
      - id: skip
        if: ${{ inputs.text == 'nope' }}
        uses: function/echo
        with:
          text: should-not-run
      - id: echo
        uses: function/echo
        with:
          text: ${{ inputs.text }}
`)

	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	executor := &Executor{
		WorkspaceDir: workspace,
		Store:        store,
		Functions:    registry,
	}
	result, err := executor.Run(context.Background(), RunRequest{
		Workflow:    workflow,
		WorkflowRef: "inline",
		Inputs:      map[string]any{"text": "hello"},
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.Status != RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
	if got := result.Outputs["result"]; got != "hello" {
		t.Fatalf("output result = %#v, want hello", got)
	}

	run, err := store.GetRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if got := run.Steps["main/skip"].Status; got != RunStatusSkipped {
		t.Fatalf("skipped step status = %q, want skipped", got)
	}
}

func TestExecutorPublishesWorkflowLifecycleEvents(t *testing.T) {
	registry := NewFunctionRegistry()
	if err := registry.Register(
		"echo",
		func(_ context.Context, args map[string]any, _ ExecutionContext) (map[string]any, error) {
			return map[string]any{"text": args["text"]}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, `
name: Events
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: echo
        uses: function/echo
        with:
          text: hello
`)
	workspace := t.TempDir()
	publisher := &fakeRuntimeEventPublisher{}
	result, err := (&Executor{
		WorkspaceDir:  workspace,
		Store:         NewFileRunStore(workspace),
		Functions:     registry,
		RuntimeEvents: publisher,
	}).Run(context.Background(), RunRequest{
		Workflow:    workflow,
		WorkflowRef: "workflows/events.yml",
		Session:     "workflow:test",
		Delivery:    Delivery{Channel: "slack", ChatID: "C123", TopicID: "T1", MessageID: "m1"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
	if len(publisher.events) == 0 {
		t.Fatal("runtime events len = 0, want lifecycle events")
	}
	var sawStart, sawEnd bool
	for _, evt := range publisher.events {
		if evt.Source.Component != "workflow" || evt.Source.Name != "workflows/events.yml" {
			t.Fatalf("runtime event source = %#v, want workflow ref", evt.Source)
		}
		if evt.Scope.SessionKey != "workflow:test" ||
			evt.Scope.Channel != "slack" ||
			evt.Scope.ChatID != "C123" {
			t.Fatalf("runtime event scope = %#v, want run session and delivery", evt.Scope)
		}
		switch evt.Kind {
		case runtimeevents.KindWorkflowRunStart:
			sawStart = true
		case runtimeevents.KindWorkflowRunEnd:
			sawEnd = true
		}
	}
	if !sawStart || !sawEnd {
		t.Fatalf("runtime event kinds = %#v, want run start and end", publisher.events)
	}
}

func TestExecutorPersistenceCallbackFailurePreventsWorkflowSideEffects(t *testing.T) {
	callbackErr := errors.New("injected durable dispatch link failure")
	functionCalls := 0
	registry := NewFunctionRegistry()
	if err := registry.Register(
		"side-effect",
		func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
			functionCalls++
			return map[string]any{"called": true}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, `
name: Persistence callback
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: function/side-effect
`)
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	result, err := (&Executor{
		WorkspaceDir: workspace,
		Store:        store,
		Functions:    registry,
	}).Run(context.Background(), RunRequest{
		RunID:       "wr_persistence_callback",
		Workflow:    workflow,
		WorkflowRef: "workflows/persistence-callback.yml",
		OnRunPersisted: func(run *Run) error {
			if run == nil ||
				run.ID != "wr_persistence_callback" ||
				run.Status != RunStatusRunning {
				t.Fatalf("OnRunPersisted() run = %#v", run)
			}
			persisted, getErr := store.GetRun(context.Background(), run.ID)
			if getErr != nil || persisted.Status != RunStatusRunning {
				t.Fatalf("GetRun() during OnRunPersisted = %#v, %v", persisted, getErr)
			}
			return callbackErr
		},
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("Run() error = %v, want callback failure", err)
	}
	if result == nil || result.Status != RunStatusFailed {
		t.Fatalf("Run() result = %#v, want failed durable run", result)
	}
	if functionCalls != 0 {
		t.Fatalf("side-effect function calls = %d, want 0", functionCalls)
	}
	run, getErr := store.GetRun(context.Background(), "wr_persistence_callback")
	if getErr != nil {
		t.Fatalf("GetRun() error = %v", getErr)
	}
	if run.Status != RunStatusFailed || !strings.Contains(run.Error, callbackErr.Error()) {
		t.Fatalf("durable run = %#v, want callback failure", run)
	}
}

func TestExecutorCancelRunPublishesRuntimeEvent(t *testing.T) {
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	now := time.Now().UTC()
	run := &Run{
		ID:          "wr_cancel",
		WorkflowRef: "workflows/cancel.yml",
		Status:      RunStatusRunning,
		Session:     "workflow:cancel",
		Delivery:    Delivery{Channel: "slack", ChatID: "C123"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	publisher := &fakeRuntimeEventPublisher{}
	executor := &Executor{WorkspaceDir: workspace, Store: store, RuntimeEvents: publisher}
	canceled, err := executor.CancelRun(context.Background(), run.ID, "operator cancel")
	if err != nil {
		t.Fatalf("CancelRun() error = %v", err)
	}
	if canceled.Status != RunStatusCanceled {
		t.Fatalf("status = %q, want canceled", canceled.Status)
	}
	var sawCanceled bool
	for _, evt := range publisher.events {
		if evt.Kind == runtimeevents.KindWorkflowRunCanceled &&
			evt.Source.Name == "workflows/cancel.yml" &&
			evt.Scope.SessionKey == "workflow:cancel" {
			sawCanceled = true
		}
	}
	if !sawCanceled {
		t.Fatalf("runtime events = %#v, want canceled lifecycle event", publisher.events)
	}
}

func TestExecutorPublishesCanceledRuntimeEventWhenFinalUpdatePreservesCancel(t *testing.T) {
	registry := NewFunctionRegistry()
	if err := registry.Register(
		"noop",
		func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, `
name: Late Cancel
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	workspace := t.TempDir()
	store := &cancelOnSucceededUpdateStore{RunStore: NewFileRunStore(workspace)}
	publisher := &fakeRuntimeEventPublisher{}
	result, err := (&Executor{
		WorkspaceDir:  workspace,
		Store:         store,
		Functions:     registry,
		RuntimeEvents: publisher,
	}).Run(context.Background(), RunRequest{
		Workflow:    workflow,
		WorkflowRef: "workflows/late-cancel.yml",
	})
	if !errors.Is(err, ErrRunCanceled) || result == nil {
		t.Fatalf("Run() error = %v result=%#v, want canceled result", err, result)
	}
	if result.Status != RunStatusCanceled {
		t.Fatalf("status = %q, want canceled", result.Status)
	}
	var sawCanceled bool
	for _, evt := range publisher.events {
		if evt.Kind == runtimeevents.KindWorkflowRunCanceled &&
			evt.Source.Name == "workflows/late-cancel.yml" {
			sawCanceled = true
		}
	}
	if !sawCanceled {
		t.Fatalf("runtime events = %#v, want canceled lifecycle event", publisher.events)
	}
}

func TestExecutorPassesAgentOutputContractManagedAndScope(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Agent Output
on:
  workflow_call:
    outputs:
      result:
        value: ${{ jobs.main.outputs.result }}
jobs:
  main:
    runs-on: picoclaw
    outputs:
      result: ${{ steps.review.outputs.text }}
    steps:
      - id: review
        uses: agent/reviewer
        with:
          managed: auto
          prompt: Review the scope.
          scope:
            - id: a
              type: file
          output:
            format: json
            schema:
              type: object
              required: [summary]
              properties:
                summary:
                  type: string
`)
	agents := &fakeAgentRunner{outputs: map[string]any{"text": `{"summary":"ok"}`}}
	result, err := (&Executor{WorkspaceDir: t.TempDir(), Agents: agents}).Run(context.Background(), RunRequest{
		Workflow:    workflow,
		WorkflowRef: "inline",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
	if len(agents.requests) != 1 {
		t.Fatalf("agent requests = %d, want 1", len(agents.requests))
	}
	req := agents.requests[0]
	if req.Output == nil || req.Output.Format != "json" {
		t.Fatalf("output contract = %#v, want json contract", req.Output)
	}
	if req.Managed != "auto" {
		t.Fatalf("managed = %#v, want auto", req.Managed)
	}
	scope, ok := req.Scope.([]any)
	if !ok || len(scope) != 1 {
		t.Fatalf("scope = %#v, want one scope item", req.Scope)
	}
}

func TestExecutorWrapsAgentUsageWithRunJobStepIdentity(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Agent Usage Identity
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: review
        uses: agent/reviewer
        with:
          prompt: Review this.
`)
	reported := []AgentUsage{
		{Model: "cheap", PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, CachedTokens: 1},
		{Model: "cheap", PromptTokens: 20, CompletionTokens: 3, TotalTokens: 23, CachedTokens: 4},
	}
	var events []AgentUsageEvent
	executor := &Executor{
		WorkspaceDir: t.TempDir(),
		Agents:       &usageReportingAgentRunner{usage: reported},
		AgentUsageObserver: func(event AgentUsageEvent) error {
			events = append(events, event)
			return nil
		},
	}
	result, err := executor.Run(t.Context(), RunRequest{
		RunID:       "wr_usage_identity",
		Workflow:    workflow,
		WorkflowRef: "inline",
	})
	if err != nil || result.Status != RunStatusSucceeded {
		t.Fatalf("Run() result = %#v, error = %v", result, err)
	}
	if len(events) != len(reported) {
		t.Fatalf("usage events = %#v, want %d", events, len(reported))
	}
	for index, event := range events {
		if event.RunID != "wr_usage_identity" || event.JobID != "main" ||
			event.StepID != "review" || event.Usage != reported[index] {
			t.Fatalf("usage event %d = %#v", index, event)
		}
	}
}

func TestExecutorPassesValidatedAgentToolsMode(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Agent tool isolation
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: inherited
        uses: agent/main
        with:
          prompt: Use the configured agent policy.
      - id: isolated
        uses: agent/main
        with:
          prompt: Classify untrusted content.
          tools: none
`)
	agents := &fakeAgentRunner{outputs: map[string]any{"text": "ok"}}
	_, err := (&Executor{WorkspaceDir: t.TempDir(), Agents: agents}).Run(
		context.Background(),
		RunRequest{Workflow: workflow, WorkflowRef: "inline"},
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(agents.requests) != 2 {
		t.Fatalf("agent requests = %d, want 2", len(agents.requests))
	}
	if got := agents.requests[0].Tools; got != AgentToolsInherit {
		t.Fatalf("inherited tools mode = %q, want %q", got, AgentToolsInherit)
	}
	if got := agents.requests[1].Tools; got != AgentToolsNone {
		t.Fatalf("isolated tools mode = %q, want %q", got, AgentToolsNone)
	}
}

func TestExecutorPassesTypedEphemeralSessionIntentWithoutInheritedKey(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Agent session intent
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: ephemeral
        uses: agent/main
        with:
          session: ephemeral
          history: none
          cache: none
          tools: none
          prompt: Decide without retaining state.
      - id: named-ephemeral
        uses: agent/main
        with:
          session: key:ephemeral
          history: read_write
          cache: session
          prompt: Continue the persistent named session.
`)
	agents := &fakeAgentRunner{outputs: map[string]any{"text": "ok"}}
	_, err := (&Executor{WorkspaceDir: t.TempDir(), Agents: agents}).Run(
		context.Background(),
		RunRequest{
			Workflow:    workflow,
			WorkflowRef: "inline",
			Session:     "inherited-pr-chat",
		},
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(agents.requests) != 2 {
		t.Fatalf("agent requests = %d, want 2", len(agents.requests))
	}

	ephemeral := agents.requests[0]
	if !ephemeral.EphemeralSession {
		t.Fatal("ephemeral request intent = false, want true")
	}
	if ephemeral.Session != "" {
		t.Fatalf("ephemeral request session = %q, want no inherited key", ephemeral.Session)
	}
	if ephemeral.History != "none" || ephemeral.Cache != "none" || ephemeral.Tools != AgentToolsNone {
		t.Fatalf(
			"ephemeral request modes = history %q, cache %q, tools %q; want none/none/none",
			ephemeral.History,
			ephemeral.Cache,
			ephemeral.Tools,
		)
	}

	named := agents.requests[1]
	if named.EphemeralSession {
		t.Fatal("key:ephemeral request intent = true, want persistent session")
	}
	if named.Session != "ephemeral" {
		t.Fatalf("key:ephemeral request session = %q, want ephemeral", named.Session)
	}
}

func TestExecutorRunsReusableWorkflowJob(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "child.yml", `
name: Child
on:
  workflow_call:
    inputs:
      text:
        type: string
        required: true
    outputs:
      upper:
        value: ${{ jobs.child.outputs.upper }}
jobs:
  child:
    runs-on: picoclaw
    outputs:
      upper: ${{ steps.make.outputs.text }}
    steps:
      - id: make
        uses: function/prefix
        with:
          text: ${{ inputs.text }}
`)
	parent := parseWorkflow(t, `
name: Parent
on:
  workflow_call:
    outputs:
      result:
        value: ${{ jobs.call.outputs.upper }}
jobs:
  call:
    uses: workflows/child.yml
    with:
      text: from-parent
`)
	registry := NewFunctionRegistry()
	if err := registry.Register(
		"prefix",
		func(_ context.Context, args map[string]any, _ ExecutionContext) (map[string]any, error) {
			return map[string]any{"text": "child:" + args["text"].(string)}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	store := NewFileRunStore(workspace)
	executor := &Executor{
		WorkspaceDir: workspace,
		Store:        store,
		Functions:    registry,
	}

	result, err := executor.Run(context.Background(), RunRequest{
		Workflow:    parent,
		WorkflowRef: "workflows/parent.yml",
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if got := result.Outputs["result"]; got != "child:from-parent" {
		t.Fatalf("output result = %#v, want child:from-parent", got)
	}
	parentRun, err := store.GetRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if len(parentRun.ChildRunIDs) != 1 {
		t.Fatalf("child run ids = %#v, want one", parentRun.ChildRunIDs)
	}
}

func TestExecutorUsesAdmittedReusableSnapshotAfterDefinitionChanges(t *testing.T) {
	workspace := t.TempDir()
	childBefore := []byte(`
name: Child before
on:
  workflow_call: {}
jobs:
  child:
    runs-on: picoclaw
    steps:
      - uses: function/old-child
`)
	childAfter := `
name: Child after
on:
  workflow_call: {}
jobs:
  child:
    runs-on: picoclaw
    steps:
      - uses: function/new-child
`
	writeWorkflowFile(t, workspace, "child.yml", string(childBefore))
	childSnapshot := parseWorkflow(t, string(childBefore))
	parent := parseWorkflow(t, `
name: Parent
on:
  manual: {}
jobs:
  mutate:
    runs-on: picoclaw
    steps:
      - uses: function/mutate-child
  call:
    needs: mutate
    uses: workflows/child.yml
`)
	oldCalls := 0
	newCalls := 0
	registry := NewFunctionRegistry()
	if err := registry.Register(
		"mutate-child",
		func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
			writeWorkflowFile(t, workspace, "child.yml", childAfter)
			return map[string]any{}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(
		"old-child",
		func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
			oldCalls++
			return map[string]any{}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(
		"new-child",
		func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
			newCalls++
			return map[string]any{}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	result, err := (&Executor{
		WorkspaceDir: workspace,
		Functions:    registry,
		WorkflowSnapshots: map[string]*LocalWorkflowSnapshot{
			"workflows/child.yml": {
				Ref:      "workflows/child.yml",
				Revision: workflowHashBytes(childBefore),
				Workflow: childSnapshot,
			},
		},
	}).Run(context.Background(), RunRequest{
		Workflow:    parent,
		WorkflowRef: "workflows/parent.yml",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result == nil || result.Status != RunStatusSucceeded {
		t.Fatalf("result = %#v, want succeeded", result)
	}
	if oldCalls != 1 || newCalls != 0 {
		t.Fatalf("child calls = (old=%d, new=%d), want (1, 0)", oldCalls, newCalls)
	}
}

func TestExecutorAdmissionFenceRunsBeforeConcurrencyLimitedCreate(t *testing.T) {
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	admissionErr := errors.New("admission revision changed")
	executor := &Executor{
		WorkspaceDir:      workspace,
		Store:             store,
		MaxConcurrentRuns: 1,
		AdmittedRunCreate: func(
			context.Context,
			*Run,
			func() error,
		) error {
			return admissionErr
		},
	}
	result, err := executor.Run(context.Background(), RunRequest{
		Workflow: parseWorkflow(t, `
name: Fenced
on:
  manual: {}
jobs:
  inspect:
    runs-on: picoclaw
    steps:
      - uses: function/workflow.state
        with:
          action: list
`),
		WorkflowRef: "workflows/fenced.yml",
	})
	if !errors.Is(err, admissionErr) {
		t.Fatalf("Run() error = %v, want admission error", err)
	}
	if result != nil {
		t.Fatalf("Run() result = %#v, want nil", result)
	}
	runs, listErr := store.ListRuns(context.Background())
	if listErr != nil {
		t.Fatalf("ListRuns() error = %v", listErr)
	}
	if len(runs) != 0 {
		t.Fatalf("persisted runs = %#v, want none", runs)
	}
}

func TestExecutorRetryCapturedNeverRereadsSourceAndWrapsChildRetryCreate(
	t *testing.T,
) {
	workspace := t.TempDir()
	fileStore := NewFileRunStore(workspace)
	store := &retrySourceTrackingStore{
		RunStore: fileStore,
		sourceID: "wr_captured_source",
	}
	workflow := parseWorkflow(t, `
name: Captured retry
on:
  manual: {}
jobs:
  retry:
    runs-on: picoclaw
    steps:
      - uses: function/retry-noop
`)
	registry := NewFunctionRegistry()
	if err := registry.Register(
		"retry-noop",
		func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
			return map[string]any{}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	var admittedCandidate *Run
	executor := &Executor{
		WorkspaceDir: workspace,
		Store:        store,
		Functions:    registry,
		WorkflowSnapshots: map[string]*LocalWorkflowSnapshot{
			"workflows/captured.yml": {
				Ref:      "workflows/captured.yml",
				Revision: "captured-revision",
				Workflow: workflow,
			},
		},
		AdmittedRunCreate: func(
			_ context.Context,
			candidate *Run,
			create func() error,
		) error {
			admittedCandidate = cloneRun(candidate)
			return create()
		},
	}
	source := &Run{
		ID:          store.sourceID,
		WorkflowRef: "workflows/captured.yml",
		Status:      RunStatusFailed,
		ParentRunID: "wr_reusable_parent",
		CallerJobID: "child",
		Inputs:      map[string]any{"captured": true},
	}
	result, err := executor.RetryCaptured(context.Background(), source, nil)
	if err != nil {
		t.Fatalf("RetryCaptured() error = %v", err)
	}
	if result == nil || result.Status != RunStatusSucceeded {
		t.Fatalf("RetryCaptured() result = %#v, want succeeded", result)
	}
	if store.sourceReads != 0 {
		t.Fatalf("source GetRun calls = %d, want 0", store.sourceReads)
	}
	if admittedCandidate == nil ||
		admittedCandidate.WorkflowRef != source.WorkflowRef ||
		admittedCandidate.ParentRunID != source.ParentRunID ||
		admittedCandidate.RetryOfRunID != source.ID {
		t.Fatalf(
			"admitted child retry = %#v, want captured source context",
			admittedCandidate,
		)
	}
}

func TestExecutorAdmittedSnapshotClosureFailsClosed(t *testing.T) {
	workspace := t.TempDir()
	liveWorkflow := `
name: Live workflow
on:
  workflow_call: {}
jobs:
  live:
    runs-on: picoclaw
    steps:
      - uses: function/live
`
	writeWorkflowFile(t, workspace, "live.yml", liveWorkflow)
	admitted := parseWorkflow(t, `
name: Admitted workflow
on:
  workflow_call: {}
jobs:
  admitted:
    runs-on: picoclaw
    steps:
      - uses: function/admitted
`)
	snapshots := map[string]*LocalWorkflowSnapshot{
		"workflows/admitted.yml": {
			Ref:      "workflows/admitted.yml",
			Revision: "admitted-revision",
			Workflow: admitted,
		},
	}

	t.Run("root", func(t *testing.T) {
		result, err := (&Executor{
			WorkspaceDir:      workspace,
			WorkflowSnapshots: snapshots,
		}).Run(context.Background(), RunRequest{Ref: "workflows/live.yml"})
		if err == nil ||
			!strings.Contains(err.Error(), "outside the admitted snapshot closure") {
			t.Fatalf("Run() = (%#v, %v), want fail-closed snapshot error", result, err)
		}
	})

	t.Run("reusable child", func(t *testing.T) {
		parent := parseWorkflow(t, `
name: Parent
on:
  manual: {}
jobs:
  child:
    uses: workflows/live.yml
`)
		result, err := (&Executor{
			WorkspaceDir:      t.TempDir(),
			WorkflowSnapshots: snapshots,
		}).Run(context.Background(), RunRequest{
			Workflow:    parent,
			WorkflowRef: "workflows/parent.yml",
		})
		if err == nil ||
			!strings.Contains(err.Error(), "outside the admitted snapshot closure") {
			t.Fatalf("Run() = (%#v, %v), want fail-closed child error", result, err)
		}
	})
}

func TestExecutorReusableWorkflowRequiresCurrentValidationStamp(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "child.yml", `
name: Child
on:
  workflow_call:
    inputs:
      text:
        type: string
        required: true
    outputs:
      result:
        value: ${{ jobs.child.outputs.result }}
jobs:
  child:
    runs-on: picoclaw
    outputs:
      result: ${{ steps.make.outputs.text }}
    steps:
      - id: make
        uses: function/prefix
        with:
          text: ${{ inputs.text }}
`)
	writeWorkflowFile(t, workspace, "parent.yml", `
name: Parent
on:
  workflow_call:
    outputs:
      result:
        value: ${{ jobs.call.outputs.result }}
jobs:
  call:
    uses: workflows/child.yml
    with:
      text: from-parent
`)
	runtime := RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"}
	if _, err := RevalidateLocal(context.Background(), workspace, runtime); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}
	writeWorkflowFile(t, workspace, "child.yml", `
name: Child
on:
  workflow_call:
    inputs:
      text:
        type: string
        required: true
    outputs:
      result:
        value: ${{ jobs.child.outputs.result }}
jobs:
  child:
    runs-on: picoclaw
    outputs:
      result: ${{ steps.make.outputs.text }}
    steps:
      - id: make
        uses: function/prefix
        with:
          text: changed-${{ inputs.text }}
	`)
	registry := NewFunctionRegistry()
	_ = registry.Register(
		"prefix",
		func(_ context.Context, args map[string]any, _ ExecutionContext) (map[string]any, error) {
			return map[string]any{"text": args["text"]}, nil
		},
	)
	store := NewFileRunStore(workspace)
	result, err := (&Executor{
		WorkspaceDir:         workspace,
		Store:                store,
		Functions:            registry,
		RuntimeCompatibility: runtime,
	}).Run(context.Background(), RunRequest{Ref: "workflows/parent.yml"})
	if err == nil {
		t.Fatal("Run() error = nil, want stale child validation error")
	}
	if !strings.Contains(err.Error(), "workflows/child.yml must be revalidated") {
		t.Fatalf("Run() error = %v, want child revalidation error", err)
	}
	if result == nil || result.Status != RunStatusFailed {
		t.Fatalf("result = %#v, want failed result", result)
	}
	parentRun, getErr := store.GetRun(context.Background(), result.RunID)
	if getErr != nil {
		t.Fatalf("GetRun() error = %v", getErr)
	}
	if len(parentRun.ChildRunIDs) != 0 {
		t.Fatalf("child run ids = %#v, want none for stale child", parentRun.ChildRunIDs)
	}
}

func TestExecutorPropagatesDeliveryAndSessionToToolAndAgentSteps(t *testing.T) {
	toolRunner := &fakeToolRunner{}
	agentRunner := &fakeAgentRunner{}
	workflow := parseWorkflow(t, `
name: Chat
on:
  manual: {}
jobs:
  chat:
    runs-on: picoclaw
    steps:
      - id: search
        uses: tool/web_search
        with:
          query: ${{ event.message.text }}
      - id: answer
        uses: agent/main
        with:
          message: ${{ steps.search.outputs.text }}
          history: read_write
          cache: session
`)
	delivery := Delivery{
		Channel:          "telegram",
		ChatID:           "-1001",
		TopicID:          "42",
		MessageID:        "100",
		ReplyToMessageID: "99",
	}
	_, err := (&Executor{
		WorkspaceDir: t.TempDir(),
		Tools:        toolRunner,
		Agents:       agentRunner,
	}).Run(context.Background(), RunRequest{
		Workflow:    workflow,
		WorkflowRef: "inline",
		Event:       map[string]any{"message": map[string]any{"text": "hello"}},
		Session:     "workflow:discussion",
		Delivery:    delivery,
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if len(toolRunner.requests) != 1 {
		t.Fatalf("tool requests = %d, want 1", len(toolRunner.requests))
	}
	if got := toolRunner.requests[0].Delivery.TopicID; got != "42" {
		t.Fatalf("tool delivery topic = %q, want 42", got)
	}
	if got := toolRunner.requests[0].Session; got != "workflow:discussion" {
		t.Fatalf("tool session = %q, want workflow:discussion", got)
	}
	if len(agentRunner.requests) != 1 {
		t.Fatalf("agent requests = %d, want 1", len(agentRunner.requests))
	}
	if got := agentRunner.requests[0].Delivery.ReplyToMessageID; got != "99" {
		t.Fatalf("agent delivery reply = %q, want 99", got)
	}
	if got := agentRunner.requests[0].Session; got != "workflow:discussion" {
		t.Fatalf("agent session = %q, want workflow:discussion", got)
	}
}

func TestExecutorUsesCanonicalMCPToolNameAndMarksRequest(t *testing.T) {
	toolRunner := &fakeToolRunner{}
	executor := &Executor{Tools: toolRunner}
	uses := "mcp/GitHub Server/issues.list"

	if _, err := executor.runStepTarget(
		context.Background(),
		Step{Uses: uses},
		map[string]any{"state": "open"},
		ExecutionContext{Session: "workflow:test"},
	); err != nil {
		t.Fatalf("runStepTarget() error = %v", err)
	}
	if len(toolRunner.requests) != 1 {
		t.Fatalf("tool requests = %d, want 1", len(toolRunner.requests))
	}
	request := toolRunner.requests[0]
	if got, want := request.Name, picomcp.CanonicalToolName("GitHub Server", "issues.list"); got != want {
		t.Fatalf("tool request name = %q, want %q", got, want)
	}
	if !request.MCP {
		t.Fatal("tool request MCP = false, want true")
	}
	if request.MCPServer != "GitHub Server" || request.MCPTool != "issues.list" {
		t.Fatalf(
			"tool request MCP identity = %q/%q, want GitHub Server/issues.list",
			request.MCPServer,
			request.MCPTool,
		)
	}
	if request.Session != "workflow:test" {
		t.Fatalf("tool request session = %q, want workflow:test", request.Session)
	}
}

func TestExecutorPreservesSimpleMCPToolName(t *testing.T) {
	toolRunner := &fakeToolRunner{}
	executor := &Executor{Tools: toolRunner}

	if _, err := executor.runStepTarget(
		context.Background(),
		Step{Uses: "mcp/github/create_issue"},
		nil,
		ExecutionContext{},
	); err != nil {
		t.Fatalf("runStepTarget() error = %v", err)
	}
	if got := toolRunner.requests[0].Name; got != "mcp_github_create_issue" {
		t.Fatalf("tool request name = %q, want mcp_github_create_issue", got)
	}
}

func TestExecutorRejectsIncompleteMCPUsesTarget(t *testing.T) {
	toolRunner := &fakeToolRunner{}
	executor := &Executor{Tools: toolRunner}

	if _, err := executor.runStepTarget(
		context.Background(),
		Step{Uses: "mcp/github"},
		nil,
		ExecutionContext{},
	); err == nil || !strings.Contains(err.Error(), "expected mcp/<server>/<tool>") {
		t.Fatalf("runStepTarget() error = %v, want MCP target shape error", err)
	}
	if len(toolRunner.requests) != 0 {
		t.Fatalf("tool requests = %d, want 0", len(toolRunner.requests))
	}
}

func TestExecutorRejectsMissingWorkflowCallInputAndSecret(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Contract
on:
  workflow_call:
    inputs:
      text:
        type: string
        required: true
    secrets:
      token:
        required: true
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	executor := &Executor{WorkspaceDir: t.TempDir(), Functions: NewFunctionRegistry()}
	if _, err := executor.Run(context.Background(), RunRequest{Workflow: workflow}); err == nil {
		t.Fatal("Run succeeded, want missing input error")
	}
	if _, err := executor.Run(context.Background(), RunRequest{
		Workflow: workflow,
		Inputs:   map[string]any{"text": "ok"},
	}); err == nil {
		t.Fatal("Run succeeded, want missing secret error")
	}
}

func TestExecutorMapsReusableWorkflowSecrets(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "child-secret.yml", `
name: Child
on:
  workflow_call:
    secrets:
      child_token:
        required: true
    outputs:
      token:
        value: ${{ jobs.child.outputs.token }}
jobs:
  child:
    runs-on: picoclaw
    outputs:
      token: ${{ steps.echo.outputs.text }}
    steps:
      - id: echo
        uses: function/echo-secret
`)
	parent := parseWorkflow(t, `
name: Parent
on:
  workflow_call:
    outputs:
      token:
        value: ${{ jobs.call.outputs.token }}
jobs:
  call:
    uses: workflows/child-secret.yml
    secrets:
      child_token: ${{ secrets.parent_token }}
`)
	registry := NewFunctionRegistry()
	if err := registry.Register(
		"echo-secret",
		func(_ context.Context, _ map[string]any, exec ExecutionContext) (map[string]any, error) {
			return map[string]any{"text": exec.Secrets["child_token"]}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	result, err := (&Executor{WorkspaceDir: workspace, Functions: registry}).Run(context.Background(), RunRequest{
		Workflow:    parent,
		WorkflowRef: "workflows/parent.yml",
		Secrets:     map[string]string{"parent_token": "mapped-secret"},
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if got := result.Outputs["token"]; got != "mapped-secret" {
		t.Fatalf("token output = %#v, want mapped-secret", got)
	}
}

func TestExecutorRejectsMissingMappedReusableWorkflowSecret(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "child-secret.yml", `
name: Child
on:
  workflow_call:
    secrets:
      child_token:
        required: true
jobs:
  child:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	parent := parseWorkflow(t, `
name: Parent
on:
  manual: {}
jobs:
  call:
    uses: workflows/child-secret.yml
    secrets:
      child_token: ${{ secrets.parent_token_typo }}
`)

	result, err := (&Executor{WorkspaceDir: workspace, Functions: NewFunctionRegistry()}).Run(
		context.Background(),
		RunRequest{Workflow: parent, WorkflowRef: "workflows/parent.yml"},
	)
	if err == nil {
		t.Fatal("Run succeeded, want missing mapped secret error")
	}
	if result == nil || result.Status != RunStatusFailed {
		t.Fatalf("result = %#v, want failed result", result)
	}
	if !strings.Contains(err.Error(), `mapped workflow secret "child_token" is missing`) {
		t.Fatalf("Run error = %v, want missing mapped secret error", err)
	}
}

func TestExecutorRejectsMissingMappedReusableWorkflowSecretInMap(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "child-secret.yml", `
name: Child
on:
  workflow_call:
    secrets:
      child_token:
        required: true
jobs:
  child:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	parent := parseWorkflow(t, `
name: Parent
on:
  manual: {}
jobs:
  call:
    uses: workflows/child-secret.yml
    secrets:
      child_token:
        part: ${{ secrets.parent_token_typo }}
`)

	result, err := (&Executor{WorkspaceDir: workspace, Functions: NewFunctionRegistry()}).Run(
		context.Background(),
		RunRequest{Workflow: parent, WorkflowRef: "workflows/parent.yml"},
	)
	if err == nil {
		t.Fatal("Run succeeded, want missing mapped secret error")
	}
	if result == nil || result.Status != RunStatusFailed {
		t.Fatalf("result = %#v, want failed result", result)
	}
	if !strings.Contains(err.Error(), `mapped workflow secret "child_token" is missing`) {
		t.Fatalf("Run error = %v, want missing mapped secret error", err)
	}
}

func TestExecutorReusableWorkflowContinueOnError(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "child-fail.yml", `
name: Child
on:
  workflow_call: {}
jobs:
  fail:
    runs-on: picoclaw
    steps:
      - uses: function/fail
`)
	parent := parseWorkflow(t, `
name: Parent
on:
  manual: {}
jobs:
  call:
    uses: workflows/child-fail.yml
    continue-on-error: true
  after:
    needs: call
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	registry := NewFunctionRegistry()
	_ = registry.Register("fail", func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		return nil, errors.New("child failed")
	})
	_ = registry.Register("noop", func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		return map[string]any{"ok": true}, nil
	})
	result, err := (&Executor{WorkspaceDir: workspace, Functions: registry}).Run(context.Background(), RunRequest{
		Workflow:    parent,
		WorkflowRef: "workflows/parent.yml",
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.Status != RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
}

func TestExecutorStepJobContinueOnError(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Job Continue
on:
  workflow_call:
    outputs:
      ok:
        value: ${{ jobs.after.outputs.ok }}
jobs:
  main:
    continue-on-error: true
    runs-on: picoclaw
    steps:
      - uses: function/fail
  after:
    needs: main
    runs-on: picoclaw
    outputs:
      ok: ${{ steps.ok.outputs.text }}
    steps:
      - id: ok
        uses: function/ok
`)
	registry := NewFunctionRegistry()
	_ = registry.Register("fail", func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		return nil, errors.New("step failed")
	})
	_ = registry.Register("ok", func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		return map[string]any{"text": "after ran"}, nil
	})
	store := NewFileRunStore(t.TempDir())
	result, err := (&Executor{WorkspaceDir: t.TempDir(), Store: store, Functions: registry}).Run(
		context.Background(),
		RunRequest{Workflow: workflow, WorkflowRef: "inline"},
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
	if got := result.Outputs["ok"]; got != "after ran" {
		t.Fatalf("output ok = %#v, want after ran", got)
	}
	run, err := store.GetRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if run.Jobs["main"].Status != RunStatusSucceeded || run.Jobs["main"].Error == "" {
		t.Fatalf("main job = %#v, want succeeded with preserved error", run.Jobs["main"])
	}
}

func TestExecutorStepContinueOnErrorPreservesOutputs(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Step Continue
on:
  workflow_call:
    outputs:
      partial:
        value: ${{ jobs.main.outputs.partial }}
jobs:
  main:
    runs-on: picoclaw
    outputs:
      partial: ${{ steps.fail.outputs.partial }}
    steps:
      - id: fail
        uses: function/partial
        continue-on-error: true
`)
	registry := NewFunctionRegistry()
	_ = registry.Register("partial", func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		return map[string]any{"partial": "kept"}, errors.New("partial failure")
	})
	store := NewFileRunStore(t.TempDir())
	result, err := (&Executor{WorkspaceDir: t.TempDir(), Store: store, Functions: registry}).Run(
		context.Background(),
		RunRequest{Workflow: workflow, WorkflowRef: "inline"},
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := result.Outputs["partial"]; got != "kept" {
		t.Fatalf("partial output = %#v, want kept", got)
	}
	run, err := store.GetRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	step := run.Steps["main/fail"]
	if step.Status != RunStatusSucceeded || step.Error == "" || step.Outputs["partial"] != "kept" {
		t.Fatalf("continued step = %#v, want succeeded with error and outputs", step)
	}
}

func TestExecutorFailedStepPreservesOutputs(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Step Failure
on:
  workflow_call:
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: fail
        uses: function/partial
`)
	registry := NewFunctionRegistry()
	_ = registry.Register("partial", func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		return map[string]any{"structured_error": "invalid payload"}, errors.New("partial failure")
	})
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	result, err := (&Executor{WorkspaceDir: workspace, Store: store, Functions: registry}).Run(
		context.Background(),
		RunRequest{Workflow: workflow, WorkflowRef: "inline"},
	)
	if err == nil {
		t.Fatal("Run() error = nil, want step failure")
	}
	if result == nil || result.Status != RunStatusFailed {
		t.Fatalf("result = %#v, want failed result", result)
	}
	run, getErr := store.GetRun(context.Background(), result.RunID)
	if getErr != nil {
		t.Fatalf("GetRun() error = %v", getErr)
	}
	step := run.Steps["main/fail"]
	if step.Status != RunStatusFailed || step.Error == "" || step.Outputs["structured_error"] != "invalid payload" {
		t.Fatalf("failed step = %#v, want failed with error and outputs", step)
	}
}

func TestExecutorCancelRunBeforeNextStep(t *testing.T) {
	registry := NewFunctionRegistry()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	_ = registry.Register(
		"cancel",
		func(ctx context.Context, _ map[string]any, exec ExecutionContext) (map[string]any, error) {
			runs, err := store.ListRuns(ctx)
			if err != nil {
				return nil, err
			}
			for _, run := range runs {
				if run.Status != RunStatusRunning {
					continue
				}
				if _, err := store.CancelRun(ctx, run.ID, "test cancel"); err != nil {
					return nil, err
				}
				return map[string]any{"ok": true}, nil
			}
			return nil, errors.New("running run not found")
		},
	)
	_ = registry.Register("after", func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		t.Fatal("after step should not run after cancellation")
		return nil, nil
	})
	workflow := parseWorkflow(t, `
name: Cancel
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: function/cancel
      - uses: function/after
`)
	executor := &Executor{WorkspaceDir: workspace, Store: store, Functions: registry}
	result, err := executor.Run(context.Background(), RunRequest{
		Workflow:    workflow,
		WorkflowRef: "inline",
	})
	if !errors.Is(err, ErrRunCanceled) || result == nil {
		t.Fatalf("Run error = %v result=%#v, want cancel error with result", err, result)
	}
	if result.Status != RunStatusCanceled {
		t.Fatalf("status = %q, want canceled", result.Status)
	}
	run, err := store.GetRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.CancelReason != "test cancel" {
		t.Fatalf("cancel reason = %q, want test cancel", run.CancelReason)
	}
}

func TestExecutorRetryUsesPreviousRunInputsAndEvent(t *testing.T) {
	workspace := t.TempDir()
	registry := NewFunctionRegistry()
	_ = registry.Register(
		"echo",
		func(_ context.Context, args map[string]any, exec ExecutionContext) (map[string]any, error) {
			return map[string]any{"text": args["text"], "event": exec.Event["kind"]}, nil
		},
	)
	writeWorkflowFile(t, workspace, "retry.yml", `
name: Retry
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    outputs:
      text: ${{ steps.echo.outputs.text }}
    steps:
      - id: echo
        uses: function/echo
        with:
          text: ${{ inputs.text }}
`)
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store, Functions: registry}
	first, err := executor.Run(context.Background(), RunRequest{
		Ref:    "workflows/retry.yml",
		Inputs: map[string]any{"text": "again"},
		Event:  map[string]any{"kind": "manual"},
	})
	if err != nil {
		t.Fatalf("first Run failed: %v", err)
	}
	retry, err := executor.Retry(context.Background(), first.RunID, nil)
	if err != nil {
		t.Fatalf("Retry failed: %v", err)
	}
	retryRun, err := store.GetRun(context.Background(), retry.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if retryRun.RetryOfRunID != first.RunID {
		t.Fatalf("RetryOfRunID = %q, want %q", retryRun.RetryOfRunID, first.RunID)
	}
	if got := retryRun.Inputs["text"]; got != "again" {
		t.Fatalf("retry inputs = %#v", retryRun.Inputs)
	}
	if got := retryRun.Event["kind"]; got != "manual" {
		t.Fatalf("retry event = %#v", retryRun.Event)
	}
}

func TestExecutorEnforcesConcurrency(t *testing.T) {
	store := NewFileRunStore(t.TempDir())
	now := time.Now().UTC()
	if err := store.CreateRun(context.Background(), &Run{
		ID:          "running",
		WorkflowRef: "workflows/a.yml",
		Status:      RunStatusRunning,
		CreatedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, `
name: Limit
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	executor := &Executor{
		WorkspaceDir:      t.TempDir(),
		Store:             store,
		Functions:         NewFunctionRegistry(),
		MaxConcurrentRuns: 1,
	}
	_, err := executor.Run(context.Background(), RunRequest{Workflow: workflow, WorkflowRef: "inline"})
	if !errors.Is(err, ErrRunConcurrencyLimit) {
		t.Fatalf("Run error = %v, want ErrRunConcurrencyLimit", err)
	}
}

func TestExecutorPropagatesTypedDuplicateRunIDError(t *testing.T) {
	store := NewFileRunStore(t.TempDir())
	registry := NewFunctionRegistry()
	if err := registry.Register(
		"noop",
		func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
			return map[string]any{}, nil
		},
	); err != nil {
		t.Fatalf("Register(noop) error = %v", err)
	}
	workflow := parseWorkflow(t, `
name: Duplicate
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	executor := &Executor{
		WorkspaceDir: t.TempDir(),
		Store:        store,
		Functions:    registry,
	}
	const runID = "wr_deterministic"
	if _, err := executor.Run(context.Background(), RunRequest{
		RunID:       runID,
		Workflow:    workflow,
		WorkflowRef: "inline",
	}); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}

	_, err := executor.Run(context.Background(), RunRequest{
		RunID:       runID,
		Workflow:    workflow,
		WorkflowRef: "inline",
	})
	if !errors.Is(err, ErrRunAlreadyExists) {
		t.Fatalf("second Run() error = %v, want ErrRunAlreadyExists", err)
	}
	if !strings.Contains(err.Error(), runID) {
		t.Fatalf("second Run() error = %q, want run ID %q", err, runID)
	}
}

func TestExecutorCancelParentStopsChildBeforeNextStep(t *testing.T) {
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	writeWorkflowFile(t, workspace, "child-cancel.yml", `
name: Child
on:
  workflow_call: {}
jobs:
  child:
    runs-on: picoclaw
    steps:
      - id: cancel
        uses: function/cancel-parent
      - id: after
        uses: function/after
`)
	parent := parseWorkflow(t, `
name: Parent
on:
  manual: {}
jobs:
  call:
    uses: workflows/child-cancel.yml
`)
	registry := NewFunctionRegistry()
	if err := registry.Register(
		"cancel-parent",
		func(ctx context.Context, _ map[string]any, exec ExecutionContext) (map[string]any, error) {
			child, err := store.GetRun(ctx, exec.RunID)
			if err != nil {
				return nil, err
			}
			if child.ParentRunID == "" {
				return nil, errors.New("child parent run id is empty")
			}
			_, err = store.CancelRun(ctx, child.ParentRunID, "operator cancel")
			return map[string]any{"ok": true}, err
		},
	); err != nil {
		t.Fatal(err)
	}
	afterCalled := false
	if err := registry.Register(
		"after",
		func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
			afterCalled = true
			return map[string]any{"ok": true}, nil
		},
	); err != nil {
		t.Fatal(err)
	}

	result, err := (&Executor{
		WorkspaceDir: workspace,
		Store:        store,
		Functions:    registry,
	}).Run(context.Background(), RunRequest{Workflow: parent, WorkflowRef: "workflows/parent.yml"})
	if err == nil || !errors.Is(err, ErrRunCanceled) {
		t.Fatalf("Run error = %v, want cancellation", err)
	}
	if result == nil || result.Status != RunStatusCanceled {
		t.Fatalf("result = %#v, want canceled result", result)
	}
	if afterCalled {
		t.Fatal("child step after parent cancel ran")
	}
	runs, err := store.ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	childCanceled := false
	for _, run := range runs {
		if run.ParentRunID == result.RunID {
			childCanceled = run.Status == RunStatusCanceled
		}
	}
	if !childCanceled {
		t.Fatalf("runs = %#v, want canceled child run", runs)
	}
}

func TestExecutorAppliesDefaultTimeout(t *testing.T) {
	registry := NewFunctionRegistry()
	_ = registry.Register(
		"wait",
		func(ctx context.Context, _ map[string]any, _ ExecutionContext) (map[string]any, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
				return map[string]any{"ok": true}, nil
			}
		},
	)
	workflow := parseWorkflow(t, `
name: Timeout
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: function/wait
`)
	result, err := (&Executor{
		WorkspaceDir:   t.TempDir(),
		Functions:      registry,
		DefaultTimeout: time.Millisecond,
	}).Run(context.Background(), RunRequest{Workflow: workflow, WorkflowRef: "inline"})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want deadline exceeded", err)
	}
	if result == nil || result.Status != RunStatusFailed {
		t.Fatalf("result = %#v, want failed timeout result", result)
	}
}

func writeWorkflowFile(t *testing.T, workspace, name, content string) {
	t.Helper()
	dir := filepath.Join(workspace, "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
