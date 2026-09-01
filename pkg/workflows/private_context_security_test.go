package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/session"
)

//nolint:govet // Test setup intentionally scopes independent assertion errors.
func TestCompiledGatePrivateContextIsLocalAndFailsClosedOnCorruption(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	const subjectCanary = "private-gate-subject-canary-8f6499"

	compilation, err := CompileGateWorkflow("Private deterministic gate", []GateSpec{{
		ID:        "policy",
		Kind:      GateDeterministic,
		When:      "${{ inputs.gate_subject.ask == true }}",
		Title:     "Policy approval",
		Questions: []any{"Approve?"},
	}}, map[string]any{
		"ask":   false,
		"token": subjectCanary,
	})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	if compilation.PrivateRoot == nil {
		t.Fatal("CompileGateWorkflow() private root = nil")
	}
	if len(compilation.Inputs) != 0 {
		t.Fatalf("CompileGateWorkflow() public inputs = %#v, want empty", compilation.Inputs)
	}
	assertPrivateGateTestContains(t, "compiler private values", compilation.PrivateRoot.Values, subjectCanary)
	assertPrivateGateTestOmits(t, "compiler public JSON", compilation, subjectCanary)

	result, err := (&Executor{
		WorkspaceDir: workspace,
		Store:        store,
	}).Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-deterministic-gate",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result == nil || result.Status != RunStatusSucceeded {
		t.Fatalf("Run() result = %#v, want succeeded", result)
	}
	assertPrivateGateTestOmits(t, "run result", result, subjectCanary)

	persisted, err := store.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if !IsPrivateWorkflowRun(persisted) || persisted.privateRoot == nil {
		t.Fatalf("persisted run private context = %#v", persisted)
	}
	assertPrivateGateTestOmits(t, "ordinary Run JSON", persisted, subjectCanary)
	assertPrivateGateTestOmits(
		t,
		"browser run projection",
		ProjectWorkflowRunForBrowser(persisted, false),
		subjectCanary,
	)
	events, err := store.Events(ctx, result.RunID)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	assertPrivateGateTestEventsRedacted(t, events)
	assertPrivateGateTestOmits(t, "stored run events", events, subjectCanary)
	assertPrivateGateTestOmits(
		t,
		"browser event projection",
		ProjectWorkflowRunEventsForBrowser(events, false, true),
		subjectCanary,
	)

	db, err := openWorkflowDatabase(ctx, workspace)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT private_context_json FROM workflow_run_payloads
		WHERE run_id=?`, result.RunID).Scan(&raw); err != nil {
		db.Close()
		t.Fatalf("read private continuation: %v", err)
	}
	if !bytes.Contains(raw, []byte(subjectCanary)) {
		db.Close()
		t.Fatalf("private continuation omits local context: %s", raw)
	}
	databasePath, err := workflowDatabasePath(workspace)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if info, err := os.Stat(databasePath); err != nil || info.Mode().Perm() != 0o600 {
		db.Close()
		t.Fatalf("workflow database permissions = %#v, %v", info, err)
	}

	reloaded, err := NewFileRunStore(workspace).GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("restarted GetRun() error = %v", err)
	}
	if reloaded.privateRoot == nil {
		t.Fatal("restarted GetRun() private root = nil")
	}
	assertPrivateGateTestContains(t, "reloaded private values", reloaded.privateRoot.Values, subjectCanary)
	assertPrivateGateTestOmits(t, "reloaded Run JSON", reloaded, subjectCanary)

	if count := bytes.Count(raw, []byte(subjectCanary)); count != 1 {
		db.Close()
		t.Fatalf("private payload canary occurrences = %d, want exactly one", count)
	}
	corrupted := bytes.Replace(
		raw,
		[]byte(subjectCanary),
		[]byte("tampered-private-gate-subject"),
		1,
	)
	if _, err := db.ExecContext(ctx, `UPDATE workflow_run_payloads SET private_context_json=?
		WHERE run_id=?`, corrupted, result.RunID); err != nil {
		db.Close()
		t.Fatalf("corrupt private continuation: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileRunStore(workspace).GetRun(ctx, result.RunID); !errors.Is(err, ErrPrivateWorkflowContext) {
		t.Fatalf("GetRun(corrupt private context) error = %v, want %v", err, ErrPrivateWorkflowContext)
	}
}

func TestPrivateGateCyclicMutationsFailBeforeDurableCreate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*GateCompilation, *RunRequest)
	}{
		{
			name: "private values",
			mutate: func(compilation *GateCompilation, _ *RunRequest) {
				cycle := map[string]any{}
				cycle["self"] = cycle
				compilation.PrivateRoot.Values["cycle"] = cycle
			},
		},
		{
			name: "mixed inputs",
			mutate: func(_ *GateCompilation, request *RunRequest) {
				cycle := map[string]any{}
				cycle["self"] = cycle
				request.Inputs = cycle
			},
		},
		{
			name: "mixed event",
			mutate: func(_ *GateCompilation, request *RunRequest) {
				cycle := map[string]any{}
				cycle["self"] = cycle
				request.Event = cycle
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			store := NewFileRunStore(workspace)
			compilation, err := CompileGateWorkflow("Private cyclic mutation", []GateSpec{{
				ID:        "policy",
				Kind:      GateDeterministic,
				When:      "false",
				Title:     "Policy",
				Questions: []any{"Approve?"},
			}}, map[string]any{"private": "cycle-subject-canary"})
			if err != nil {
				t.Fatalf("CompileGateWorkflow() error = %v", err)
			}
			request := RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "inline/private-cyclic-mutation",
				PrivateRoot: compilation.PrivateRoot,
			}
			test.mutate(compilation, &request)
			result, runErr := (&Executor{
				WorkspaceDir: workspace,
				Store:        store,
			}).Run(context.Background(), request)
			if result != nil || !errors.Is(runErr, ErrPrivateWorkflowContext) {
				t.Fatalf(
					"Run() = (%#v, %v), want private-context rejection",
					result,
					runErr,
				)
			}
			runs, listErr := store.ListRuns(context.Background())
			if listErr != nil || len(runs) != 0 {
				t.Fatalf("ListRuns() = (%#v, %v), want no durable run", runs, listErr)
			}
		})
	}
}

func TestPrivateGateWorkflowCaptureRejectsCustomMarshalerWithoutInvokingIt(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	compilation, err := CompileGateWorkflow("Private single capture", []GateSpec{{
		ID:        "approval",
		Kind:      GateDeterministic,
		When:      "true",
		Title:     "Approval",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "single-capture-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	stateful := &privateGateStatefulJSONMarshaler{}
	job := compilation.Workflow.Jobs[workflowGateJobID]
	job.Steps[0].With["questions"] = stateful
	compilation.Workflow.Jobs[workflowGateJobID] = job

	result, runErr := (&Executor{WorkspaceDir: workspace, Store: store}).Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-single-capture",
		PrivateRoot: compilation.PrivateRoot,
	})
	if result != nil || runErr != ErrPrivateWorkflowContext {
		t.Fatalf("Run() = (%#v, %v), want private-context rejection", result, runErr)
	}
	if stateful.calls != 0 {
		t.Fatalf("custom marshaler calls = %d, want zero", stateful.calls)
	}
	runs, listErr := store.ListRuns(ctx)
	if listErr != nil || len(runs) != 0 {
		t.Fatalf("ListRuns() = (%#v, %v), want no durable run", runs, listErr)
	}
}

func TestPrivateGateCapturedRetryCyclicMutationsFailBeforeClone(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Run)
	}{
		{
			name: "mixed inputs",
			mutate: func(source *Run) {
				cycle := map[string]any{}
				cycle["self"] = cycle
				source.Inputs = cycle
			},
		},
		{
			name: "private values",
			mutate: func(source *Run) {
				cycle := map[string]any{}
				cycle["self"] = cycle
				source.privateRoot.Values["cycle"] = cycle
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			store := NewFileRunStore(workspace)
			executor := &Executor{WorkspaceDir: workspace, Store: store}
			compilation, err := CompileGateWorkflow("Private retry cycle", []GateSpec{{
				ID:        "policy",
				Kind:      GateDeterministic,
				When:      "false",
				Title:     "Policy",
				Questions: []any{"Approve?"},
			}}, map[string]any{"private": "retry-cycle-subject-canary"})
			if err != nil {
				t.Fatalf("CompileGateWorkflow() error = %v", err)
			}
			initial, err := executor.Run(ctx, RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "inline/private-retry-cycle",
				PrivateRoot: compilation.PrivateRoot,
			})
			if err != nil || initial == nil || initial.Status != RunStatusSucceeded {
				t.Fatalf("initial Run() = (%#v, %v), want succeeded", initial, err)
			}
			source, err := store.GetRun(ctx, initial.RunID)
			if err != nil {
				t.Fatalf("GetRun(source) error = %v", err)
			}
			test.mutate(source)
			result, retryErr := executor.RetryCaptured(ctx, source, nil)
			if result != nil || retryErr != ErrPrivateWorkflowContext {
				t.Fatalf(
					"RetryCaptured() = (%#v, %v), want private-context rejection",
					result,
					retryErr,
				)
			}
			runs, listErr := store.ListRuns(ctx)
			if listErr != nil || len(runs) != 1 {
				t.Fatalf("ListRuns() = (%#v, %v), want source only", runs, listErr)
			}
		})
	}
}

func TestPrivateGateResumeRejectsClaimWrapperContextInjection(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	agents := &privateGateSecurityAgentRunner{outputs: map[string]any{
		"structured": map[string]any{
			"ask_user":  false,
			"reason":    "",
			"questions": []any{},
		},
	}}
	executor := &Executor{WorkspaceDir: workspace, Store: store, Agents: agents}
	compilation, err := CompileGateWorkflow("Private claim injection", []GateSpec{
		{
			ID:        "approval",
			Kind:      GateDeterministic,
			When:      "true",
			Title:     "Approval",
			Questions: []any{"Proceed?"},
		},
		{
			ID:       "decision",
			Kind:     GateAIIsolatedContext,
			AgentID:  "reviewer",
			Criteria: "Decide.",
			Title:    "Decision",
		},
	}, map[string]any{"private": "claim-injection-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	waiting, err := executor.Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-claim-injection",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || waiting == nil || waiting.Status != RunStatusWaiting {
		t.Fatalf("Run() = (%#v, %v), want waiting", waiting, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, waiting.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() = (%#v, %v), want one", tasks, err)
	}
	executor.AdmittedHumanTaskClaim = func(
		_ context.Context,
		_, _ string,
		claim func() (*Run, WorkflowHumanTask, bool, error),
	) (*Run, WorkflowHumanTask, bool, error) {
		run, task, duplicate, claimErr := claim()
		if run != nil {
			cycle := map[string]any{}
			cycle["self"] = cycle
			run.Inputs = cycle
		}
		return run, task, duplicate, claimErr
	}
	result, resumeErr := executor.ResumeHumanTask(ctx, waiting.RunID, tasks[0].ID, HumanTaskResumeRequest{
		ExpectedRevision: tasks[0].Revision,
		InputHash:        tasks[0].InputHash,
		ResponseID:       "response-1",
		Response:         "approved",
	})
	if result != nil || resumeErr != ErrPrivateWorkflowContext {
		t.Fatalf("ResumeHumanTask() = (%#v, %v), want private-context rejection", result, resumeErr)
	}
	if len(agents.requests) != 0 {
		t.Fatalf("agent continuation ran after injected claim: %#v", agents.requests)
	}
}

func TestPrivateGateResumeRechecksSecretsAtAuthoritativeClaim(t *testing.T) {
	for _, fakeClaim := range []bool{false, true} {
		name := "file_store_claim"
		if fakeClaim {
			name = "custom_store_return"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			base := NewFileRunStore(workspace)
			compilation, err := CompileGateWorkflow("Private secret claim", []GateSpec{{
				ID:        "approval",
				Kind:      GateDeterministic,
				When:      "true",
				Title:     "Approval",
				Questions: []any{"Proceed?"},
			}}, map[string]any{"private": "claim-secret-subject-canary"})
			if err != nil {
				t.Fatalf("CompileGateWorkflow() error = %v", err)
			}
			creator := &Executor{WorkspaceDir: workspace, Store: base}
			waiting, err := creator.Run(ctx, RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "inline/private-secret-claim",
				PrivateRoot: compilation.PrivateRoot,
			})
			if err != nil || waiting == nil || waiting.Status != RunStatusWaiting {
				t.Fatalf("Run() = (%#v, %v), want waiting", waiting, err)
			}
			tasks, err := base.ListHumanTasks(ctx, waiting.RunID)
			if err != nil || len(tasks) != 1 {
				t.Fatalf("ListHumanTasks() = (%#v, %v), want one", tasks, err)
			}
			store := &privateGateSecretClassificationStore{
				FileRunStore: base,
				fakeClaim:    fakeClaim,
			}
			result, resumeErr := (&Executor{WorkspaceDir: workspace, Store: store}).ResumeHumanTask(
				ctx,
				waiting.RunID,
				tasks[0].ID,
				HumanTaskResumeRequest{
					ExpectedRevision: tasks[0].Revision,
					InputHash:        tasks[0].InputHash,
					ResponseID:       "response-1",
					Response:         "approved",
					Secrets:          map[string]string{"new": "private-new-secret-canary"},
				},
			)
			if result != nil || resumeErr != ErrPrivateWorkflowContext {
				t.Fatalf("ResumeHumanTask() = (%#v, %v), want private-context rejection", result, resumeErr)
			}
			after, err := base.ListHumanTasks(ctx, waiting.RunID)
			if err != nil || len(after) != 1 || after[0].Status != HumanTaskStatusWaiting ||
				after[0].Revision != tasks[0].Revision {
				t.Fatalf("rejected private claim changed task: (%#v, %v)", after, err)
			}
		})
	}
}

func TestPrivateGateDuplicateResumeRedactsBeforeCloningOutputs(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	compilation, err := CompileGateWorkflow("Private duplicate projection", []GateSpec{{
		ID:        "approval",
		Kind:      GateDeterministic,
		When:      "true",
		Title:     "Approval",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "duplicate-output-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	waiting, err := executor.Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-duplicate-projection",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || waiting == nil || waiting.Status != RunStatusWaiting {
		t.Fatalf("Run() = (%#v, %v), want waiting", waiting, err)
	}
	tasks, err := store.ListHumanTasks(ctx, waiting.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() = (%#v, %v), want one", tasks, err)
	}
	executor.AdmittedHumanTaskClaim = func(
		_ context.Context,
		_, _ string,
		_ func() (*Run, WorkflowHumanTask, bool, error),
	) (*Run, WorkflowHumanTask, bool, error) {
		run, getErr := store.GetRun(ctx, waiting.RunID)
		if getErr != nil {
			return nil, WorkflowHumanTask{}, false, getErr
		}
		cycle := map[string]any{"private": "duplicate-output-canary"}
		cycle["self"] = cycle
		run.Outputs = cycle
		return run, tasks[0], true, nil
	}
	result, resumeErr := executor.ResumeHumanTask(ctx, waiting.RunID, tasks[0].ID, HumanTaskResumeRequest{
		ExpectedRevision: tasks[0].Revision,
		InputHash:        tasks[0].InputHash,
		ResponseID:       "response-1",
		Response:         "approved",
	})
	if resumeErr != nil || result == nil || result.RunID != waiting.RunID ||
		result.Outputs != nil || result.Error != "" {
		t.Fatalf("ResumeHumanTask(duplicate) = (%#v, %v), want redacted result", result, resumeErr)
	}
}

func TestPrivateGateBrowserProjectionsRedactBeforeCloning(t *testing.T) {
	const privateCanary = "private-projection-cycle-canary-9d651b"
	cycle := map[string]any{"canary": privateCanary}
	cycle["self"] = cycle
	run := &Run{
		ID:                "wr_private_projection_cycle",
		WorkflowRef:       "inline/private-projection-cycle",
		Status:            RunStatusFailed,
		ContextVisibility: WorkflowContextVisibilityPrivate,
		Origin:            &RunOrigin{Kind: RunOriginExternalEvent, EventID: privateCanary},
		ParentRunID:       privateCanary,
		ChildRunIDs:       []string{privateCanary},
		CallerJobID:       privateCanary,
		Session:           privateCanary,
		Delivery:          Delivery{Channel: privateCanary},
		Event:             cycle,
		Inputs:            cycle,
		Outputs:           cycle,
		Jobs: map[string]JobExecution{
			"main": {ID: "main", Status: RunStatusFailed, Outputs: cycle, Error: privateCanary},
		},
		Steps: map[string]StepExecution{
			"step": {ID: "step", Status: RunStatusFailed, Outputs: cycle, Error: privateCanary},
		},
		Error:        privateCanary,
		CancelReason: privateCanary,
		execution:    &workflowExecutionState{},
		humanTasks: map[string]WorkflowHumanTask{
			"task": {ID: "task", Title: privateCanary},
		},
		privateRoot: &frozenWorkflowRootContext{Values: cycle, Revision: privateCanary},
	}
	projections := map[string]*Run{
		"direct": ProjectWorkflowRunForBrowser(run, false),
		"store": ProjectWorkflowRunForBrowserWithStore(
			context.Background(),
			nil,
			run,
			false,
		),
	}
	directJSON, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("json.Marshal(private Run) error = %v", err)
	}
	if bytes.Contains(directJSON, []byte(privateCanary)) {
		t.Fatalf("default private Run JSON contains canary: %s", directJSON)
	}
	listed := ProjectEventBackedDraftRunsForBrowser([]Run{*run})
	if len(listed) != 1 {
		t.Fatalf("listed projections = %#v", listed)
	}
	projections["list"] = &listed[0]
	for label, projected := range projections {
		if projected == nil || projected.execution != nil || projected.humanTasks != nil ||
			projected.privateRoot != nil || projected.Inputs != nil || projected.Event != nil ||
			projected.Outputs != nil || projected.Origin != nil || projected.Session != "" ||
			projected.ParentRunID != "" || projected.CallerJobID != "" ||
			len(projected.ChildRunIDs) != 0 || projected.Jobs["main"].Outputs != nil ||
			projected.Steps["step"].Outputs != nil {
			t.Fatalf("%s private projection retained context: %#v", label, projected)
		}
		encoded, encodeErr := json.Marshal(projected)
		if encodeErr != nil {
			t.Fatalf("json.Marshal(%s projection) error = %v", label, encodeErr)
		}
		if bytes.Contains(encoded, []byte(privateCanary)) {
			t.Fatalf("%s projection contains private canary: %s", label, encoded)
		}
	}

	events := ProjectWorkflowRunEventsForBrowser([]RunEvent{{
		Kind:    "workflow.private",
		Message: privateCanary,
		Payload: cycle,
	}}, false, true)
	encodedEvents, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("json.Marshal(private events) error = %v", err)
	}
	if bytes.Contains(encodedEvents, []byte(privateCanary)) {
		t.Fatalf("private event projection contains canary: %s", encodedEvents)
	}
}

func TestPrivateGateFailureReturnsFixedPublicOutcome(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	const (
		subjectCanary = "private-error-subject-canary-a18b68"
		outputCanary  = "private-error-output-canary-621bc5"
		errorCanary   = "private-runner-error-canary-5bd381"
	)
	agents := &privateGateSecurityAgentRunner{
		outputs: map[string]any{"private_output": outputCanary},
		err:     fmt.Errorf("model provider exposed %s", errorCanary),
	}
	runtimeEvents := &fakeRuntimeEventPublisher{}
	store := NewFileRunStore(workspace)
	compilation, err := CompileGateWorkflow("Private failing gate", []GateSpec{{
		ID:       "finding",
		Kind:     GateAIIsolatedContext,
		AgentID:  "reviewer",
		Criteria: "Escalate incomplete evidence.",
		Title:    "Finding review",
	}}, map[string]any{"finding": subjectCanary})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}

	result, runErr := (&Executor{
		WorkspaceDir:  workspace,
		Store:         store,
		Agents:        agents,
		RuntimeEvents: runtimeEvents,
	}).Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-failing-gate",
		PrivateRoot: compilation.PrivateRoot,
	})
	if !errors.Is(runErr, ErrPrivateWorkflowFailed) ||
		runErr == nil || runErr.Error() != ErrPrivateWorkflowFailed.Error() {
		t.Fatalf("Run() error = %v, want fixed %v", runErr, ErrPrivateWorkflowFailed)
	}
	if result == nil || result.Status != RunStatusFailed ||
		result.Error != "" || result.Outputs != nil {
		t.Fatalf("Run() public failure result = %#v", result)
	}
	assertPrivateGateTestOmits(t, "public failure error", runErr.Error(), errorCanary)
	assertPrivateGateTestOmits(
		t,
		"public failure result",
		result,
		subjectCanary,
		outputCanary,
		errorCanary,
	)

	persisted, err := store.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	assertPrivateGateTestOmits(
		t,
		"failed browser run",
		ProjectWorkflowRunForBrowser(persisted, false),
		subjectCanary,
		outputCanary,
		errorCanary,
	)
	events, err := store.Events(ctx, result.RunID)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	assertPrivateGateTestEventsRedacted(t, events)
	assertPrivateGateTestOmits(
		t,
		"failed run events",
		events,
		subjectCanary,
		outputCanary,
		errorCanary,
	)
	if len(runtimeEvents.events) == 0 {
		t.Fatal("private run published no redacted runtime lifecycle events")
	}
	assertPrivateGateTestOmits(
		t,
		"runtime lifecycle events",
		runtimeEvents.events,
		subjectCanary,
		outputCanary,
		errorCanary,
	)
}

func TestPrivateGatePreservesCanonicalAdmissionErrors(t *testing.T) {
	tests := []struct {
		name     string
		sentinel error
	}{
		{name: "conflict", sentinel: ErrRunAdmissionConflict},
		{name: "unavailable", sentinel: ErrRunAdmissionUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const errorCanary = "private-admission-error-canary-4a7b90"
			compilation, err := CompileGateWorkflow("Private admission error", []GateSpec{{
				ID:        "policy",
				Kind:      GateDeterministic,
				When:      "false",
				Title:     "Policy",
				Questions: []any{"Approve?"},
			}}, map[string]any{"private": "admission-subject-canary"})
			if err != nil {
				t.Fatalf("CompileGateWorkflow() error = %v", err)
			}
			executor := &Executor{
				WorkspaceDir: t.TempDir(),
				AdmittedRunCreate: func(context.Context, *Run, func() error) error {
					return fmt.Errorf("%s: %w", errorCanary, test.sentinel)
				},
			}
			result, runErr := executor.Run(context.Background(), RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "inline/private-admission-error",
				PrivateRoot: compilation.PrivateRoot,
			})
			if result != nil || runErr != test.sentinel {
				t.Fatalf(
					"Run() = (%#v, %v), want canonical %v",
					result,
					runErr,
					test.sentinel,
				)
			}
			assertPrivateGateTestOmits(t, "admission error", runErr, errorCanary)
		})
	}
}

func TestPrivateGateCancellationEventsRedactReasons(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	compilation, err := CompileGateWorkflow("Private waiting gate", []GateSpec{{
		ID:        "approval",
		Kind:      GateDeterministic,
		When:      "true",
		Title:     "Approval",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "cancellation-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	runWaiting := func(t *testing.T) (*RunResult, WorkflowHumanTask) {
		t.Helper()
		result, runErr := executor.Run(ctx, RunRequest{
			Workflow:    compilation.Workflow,
			WorkflowRef: "inline/private-waiting-gate",
			PrivateRoot: compilation.PrivateRoot,
		})
		if runErr != nil || result == nil || result.Status != RunStatusWaiting {
			t.Fatalf("Run() = (%#v, %v), want waiting", result, runErr)
		}
		tasks, taskErr := executor.ListHumanTasks(ctx, result.RunID)
		if taskErr != nil || len(tasks) != 1 {
			t.Fatalf("ListHumanTasks() = (%#v, %v), want one", tasks, taskErr)
		}
		return result, tasks[0]
	}
	assertCanceledEvents := func(t *testing.T, runID string, canary string) {
		t.Helper()
		events, eventsErr := store.Events(ctx, runID)
		if eventsErr != nil {
			t.Fatalf("Events() error = %v", eventsErr)
		}
		assertPrivateGateTestEventsRedacted(t, events)
		assertPrivateGateTestOmits(t, "private cancellation events", events, canary)
	}
	assertCanceledResultRedacted := func(t *testing.T, run *Run, canary string) {
		t.Helper()
		if run == nil || run.CancelReason != "" || run.Error != "" ||
			run.Outputs != nil || run.Inputs != nil || run.Event != nil ||
			run.privateRoot != nil || run.execution != nil || run.humanTasks != nil {
			t.Fatalf("private cancellation returned raw run fields: %#v", run)
		}
		for key, job := range run.Jobs {
			if job.Outputs != nil || job.Error != "" {
				t.Fatalf("private cancellation job %q retained diagnostics: %#v", key, job)
			}
		}
		for key, step := range run.Steps {
			if step.Outputs != nil || step.Error != "" {
				t.Fatalf("private cancellation step %q retained diagnostics: %#v", key, step)
			}
		}
		assertPrivateGateTestOmits(t, "private cancellation direct result", run, canary)
	}

	t.Run("run cancellation", func(t *testing.T) {
		const reasonCanary = "private-run-cancel-reason-canary-638d62"
		result, _ := runWaiting(t)
		canceled, cancelErr := executor.CancelRun(ctx, result.RunID, reasonCanary)
		if cancelErr != nil || canceled == nil || canceled.Status != RunStatusCanceled {
			t.Fatalf("CancelRun() = (%#v, %v), want canceled", canceled, cancelErr)
		}
		assertCanceledResultRedacted(t, canceled, reasonCanary)
		assertCanceledEvents(t, result.RunID, reasonCanary)
	})

	t.Run("human task cancellation", func(t *testing.T) {
		const reasonCanary = "private-task-cancel-reason-canary-b37cf1"
		result, task := runWaiting(t)
		canceled, cancelErr := executor.CancelHumanTask(
			ctx,
			result.RunID,
			task.ID,
			reasonCanary,
		)
		if cancelErr != nil || canceled == nil || canceled.Status != RunStatusCanceled {
			t.Fatalf(
				"CancelHumanTask() = (%#v, %v), want canceled",
				canceled,
				cancelErr,
			)
		}
		assertCanceledResultRedacted(t, canceled, reasonCanary)
		assertCanceledEvents(t, result.RunID, reasonCanary)
	})
}

func TestPrivateGateCanonicalizesWrappedCancellationErrors(t *testing.T) {
	tests := []struct {
		name     string
		sentinel error
	}{
		{name: "canceled", sentinel: context.Canceled},
		{name: "deadline", sentinel: context.DeadlineExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const errorCanary = "private-wrapped-error-canary-e039a7"
			workspace := t.TempDir()
			compilation, err := CompileGateWorkflow("Private error gate", []GateSpec{{
				ID:       "decision",
				Kind:     GateAIIsolatedContext,
				AgentID:  "reviewer",
				Criteria: "Decide.",
				Title:    "Decision",
			}}, map[string]any{"private": "error-subject-canary"})
			if err != nil {
				t.Fatalf("CompileGateWorkflow() error = %v", err)
			}
			agents := &privateGateSecurityAgentRunner{
				err: fmt.Errorf("%s: %w", errorCanary, test.sentinel),
			}
			result, runErr := (&Executor{
				WorkspaceDir: workspace,
				Agents:       agents,
			}).Run(context.Background(), RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "inline/private-error-gate",
				PrivateRoot: compilation.PrivateRoot,
			})
			if runErr != test.sentinel {
				t.Fatalf("Run() error = %v, want canonical %v", runErr, test.sentinel)
			}
			assertPrivateGateTestOmits(t, "canonical run error", runErr, errorCanary)
			if result == nil || result.Status != RunStatusFailed ||
				result.Error != "" || result.Outputs != nil {
				t.Fatalf("Run() result = %#v, want redacted failure", result)
			}
		})
	}
}

func TestPrivateGateResumeCanonicalizesWrappedCancellationErrors(t *testing.T) {
	tests := []struct {
		name     string
		sentinel error
	}{
		{name: "canceled", sentinel: context.Canceled},
		{name: "deadline", sentinel: context.DeadlineExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const errorCanary = "private-resume-wrapped-error-canary-e67d05"
			workspace := t.TempDir()
			store := NewFileRunStore(workspace)
			agents := &privateGateSecurityAgentRunner{
				err: fmt.Errorf("%s: %w", errorCanary, test.sentinel),
			}
			executor := &Executor{WorkspaceDir: workspace, Store: store, Agents: agents}
			compilation, err := CompileGateWorkflow("Private resume error gate", []GateSpec{
				{
					ID:        "approval",
					Kind:      GateDeterministic,
					When:      "true",
					Title:     "Approval",
					Questions: []any{"Proceed?"},
				},
				{
					ID:       "decision",
					Kind:     GateAIIsolatedContext,
					AgentID:  "reviewer",
					Criteria: "Decide.",
					Title:    "Decision",
				},
			}, map[string]any{"private": "resume-error-subject-canary"})
			if err != nil {
				t.Fatalf("CompileGateWorkflow() error = %v", err)
			}
			waiting, runErr := executor.Run(context.Background(), RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "inline/private-resume-error-gate",
				PrivateRoot: compilation.PrivateRoot,
			})
			if runErr != nil || waiting == nil || waiting.Status != RunStatusWaiting {
				t.Fatalf("Run() = (%#v, %v), want waiting", waiting, runErr)
			}
			tasks, taskErr := executor.ListHumanTasks(context.Background(), waiting.RunID)
			if taskErr != nil || len(tasks) != 1 {
				t.Fatalf("ListHumanTasks() = (%#v, %v), want one", tasks, taskErr)
			}
			result, resumeErr := executor.ResumeHumanTask(
				context.Background(),
				waiting.RunID,
				tasks[0].ID,
				HumanTaskResumeRequest{
					ExpectedRevision: tasks[0].Revision,
					InputHash:        tasks[0].InputHash,
					ResponseID:       "response-1",
					Response:         "approved",
				},
			)
			if resumeErr != test.sentinel {
				t.Fatalf(
					"ResumeHumanTask() error = %v, want canonical %v",
					resumeErr,
					test.sentinel,
				)
			}
			assertPrivateGateTestOmits(t, "canonical resume error", resumeErr, errorCanary)
			if result == nil || result.Status != RunStatusFailed ||
				result.Error != "" || result.Outputs != nil {
				t.Fatalf("ResumeHumanTask() result = %#v, want redacted failure", result)
			}
		})
	}
}

func TestPrivateGateRetryAndResumeRejectNewSecrets(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store}

	completedCompilation, err := CompileGateWorkflow("Private completed gate", []GateSpec{{
		ID:        "policy",
		Kind:      GateDeterministic,
		When:      "false",
		Title:     "Policy",
		Questions: []any{"Approve?"},
	}}, map[string]any{"private": "retry-secret-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow(completed) error = %v", err)
	}
	completed, err := executor.Run(ctx, RunRequest{
		Workflow:    completedCompilation.Workflow,
		WorkflowRef: "inline/private-completed-gate",
		PrivateRoot: completedCompilation.PrivateRoot,
	})
	if err != nil || completed == nil || completed.Status != RunStatusSucceeded {
		t.Fatalf("Run(completed) = (%#v, %v), want succeeded", completed, err)
	}
	if result, retryErr := executor.Retry(
		ctx,
		completed.RunID,
		map[string]string{"new": "retry-secret-canary"},
	); result != nil || retryErr != ErrPrivateWorkflowContext {
		t.Fatalf(
			"Retry(private secrets) = (%#v, %v), want canonical rejection",
			result,
			retryErr,
		)
	}

	waitingCompilation, err := CompileGateWorkflow("Private waiting gate", []GateSpec{{
		ID:        "approval",
		Kind:      GateDeterministic,
		When:      "true",
		Title:     "Approval",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "resume-secret-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow(waiting) error = %v", err)
	}
	waiting, err := executor.Run(ctx, RunRequest{
		Workflow:    waitingCompilation.Workflow,
		WorkflowRef: "inline/private-waiting-secret-gate",
		PrivateRoot: waitingCompilation.PrivateRoot,
	})
	if err != nil || waiting == nil || waiting.Status != RunStatusWaiting {
		t.Fatalf("Run(waiting) = (%#v, %v), want waiting", waiting, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, waiting.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() = (%#v, %v), want one", tasks, err)
	}
	result, resumeErr := executor.ResumeHumanTask(
		ctx,
		waiting.RunID,
		tasks[0].ID,
		HumanTaskResumeRequest{
			ExpectedRevision: tasks[0].Revision,
			InputHash:        tasks[0].InputHash,
			ResponseID:       "response-with-secret",
			Response:         "approved",
			Secrets:          map[string]string{"new": "resume-secret-canary"},
		},
	)
	if result != nil || resumeErr != ErrPrivateWorkflowContext {
		t.Fatalf(
			"ResumeHumanTask(private secrets) = (%#v, %v), want canonical rejection",
			result,
			resumeErr,
		)
	}
	remaining, err := executor.ListHumanTasks(ctx, waiting.RunID)
	if err != nil || len(remaining) != 1 || remaining[0].Status != HumanTaskStatusWaiting {
		t.Fatalf("tasks after rejected resume = (%#v, %v), want unchanged waiting", remaining, err)
	}
}

type privateGateSecurityAgentRunner struct {
	order           *[]string
	captureKey      string
	captureSummary  string
	historyRevision string
	captureCount    int
	captureRefs     []ReadOnlySessionRef
	requests        []AgentRequest
	outputs         map[string]any
	err             error
}

type privateGateStatefulJSONMarshaler struct {
	calls int
}

type privateGateSecretClassificationStore struct {
	*FileRunStore
	fakeClaim bool
}

func (s *privateGateSecretClassificationStore) GetRun(
	ctx context.Context,
	runID string,
) (*Run, error) {
	run, err := s.FileRunStore.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	// Simulate a stale/non-authoritative pre-read that classifies the run as
	// ordinary. The claim result remains the authoritative boundary.
	run.privateRoot = nil
	run.ContextVisibility = ""
	return run, nil
}

func (s *privateGateSecretClassificationStore) ClaimHumanTask(
	ctx context.Context,
	runID string,
	taskID string,
	req HumanTaskResumeRequest,
) (*Run, WorkflowHumanTask, bool, error) {
	if !s.fakeClaim {
		return s.FileRunStore.ClaimHumanTask(ctx, runID, taskID, req)
	}
	run, err := s.FileRunStore.GetRun(ctx, runID)
	if err != nil {
		return nil, WorkflowHumanTask{}, false, err
	}
	task, ok := run.humanTasks[taskID]
	if !ok {
		return nil, WorkflowHumanTask{}, false, ErrHumanTaskNotFound
	}
	return run, task, false, nil
}

func (m *privateGateStatefulJSONMarshaler) MarshalJSON() ([]byte, error) {
	m.calls++
	return []byte(`["unexpected"]`), nil
}

func (r *privateGateSecurityAgentRunner) CaptureReadOnlySession(
	_ context.Context,
	ref ReadOnlySessionRef,
) (*FrozenReadOnlySession, error) {
	r.captureCount++
	r.captureRefs = append(r.captureRefs, ref)
	if r.order != nil {
		*r.order = append(*r.order, "capture")
	}
	return &FrozenReadOnlySession{
		AgentID: ref.AgentID,
		Snapshot: session.SessionSnapshot{
			Key:     r.captureKey,
			Summary: r.captureSummary,
		},
		HistoryRevision: r.historyRevision,
		FrozenMedia:     media.FrozenSet{Version: media.FrozenSetVersion},
	}, nil
}

func (r *privateGateSecurityAgentRunner) RunAgent(
	_ context.Context,
	req AgentRequest,
) (map[string]any, error) {
	if r.order != nil {
		*r.order = append(*r.order, "agent")
	}
	cloned := req
	cloned.Inputs = cloneMap(req.Inputs)
	cloned.FrozenReadOnlySession = cloneFrozenReadOnlySession(req.FrozenReadOnlySession)
	r.requests = append(r.requests, cloned)
	return cloneMap(r.outputs), r.err
}

func assertPrivateGateTestEventsRedacted(t *testing.T, events []RunEvent) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("private run events = empty")
	}
	for index, event := range events {
		if event.Message != "" || event.Payload != nil {
			t.Fatalf("private event %d is not redacted: %#v", index, event)
		}
	}
}

func assertPrivateGateTestContains(t *testing.T, label string, value any, canary string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%s) error = %v", label, err)
	}
	if !bytes.Contains(encoded, []byte(canary)) {
		t.Fatalf("%s does not contain %q: %s", label, canary, encoded)
	}
}

func assertPrivateGateTestOmits(t *testing.T, label string, value any, canaries ...string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%s) error = %v", label, err)
	}
	for _, canary := range canaries {
		if strings.Contains(string(encoded), canary) {
			t.Fatalf("%s contains private canary %q: %s", label, canary, encoded)
		}
	}
}
