package workflows

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHumanTaskResumeDetachesSecretsBeforeAdmissionClaim(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	var receivedSecrets map[string]string
	registry := NewFunctionRegistry()
	if err := registry.Register(
		"after",
		func(_ context.Context, _ map[string]any, exec ExecutionContext) (map[string]any, error) {
			receivedSecrets = cloneStringMap(exec.Secrets)
			return map[string]any{"done": true}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, `
name: Human task secret detachment
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: approve
        uses: human/task
        with:
          title: Review
          questions: ["Approve?"]
      - id: after
        uses: function/after
`)
	executor := &Executor{WorkspaceDir: workspace, Store: store, Functions: registry}
	started, err := executor.Run(ctx, RunRequest{
		Workflow: workflow, WorkflowRef: "workflows/human-secret-detachment.yml",
	})
	if err != nil || started == nil || started.Status != RunStatusWaiting {
		t.Fatalf("Run() = (%#v, %v), want waiting", started, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() = (%#v, %v), want one", tasks, err)
	}
	callerSecrets := map[string]string{}
	executor.AdmittedHumanTaskClaim = func(
		_ context.Context,
		_, _ string,
		claim func() (*Run, WorkflowHumanTask, bool, error),
	) (*Run, WorkflowHumanTask, bool, error) {
		callerSecrets["late"] = "late-secret-canary-6bf903"
		return claim()
	}
	resumed, err := executor.ResumeHumanTask(ctx, started.RunID, tasks[0].ID, HumanTaskResumeRequest{
		ExpectedRevision: tasks[0].Revision,
		InputHash:        tasks[0].InputHash,
		ResponseID:       "response-1",
		Response:         "approved",
		Secrets:          callerSecrets,
	})
	if err != nil || resumed == nil || resumed.Status != RunStatusSucceeded {
		t.Fatalf("ResumeHumanTask() = (%#v, %v), want succeeded", resumed, err)
	}
	if callerSecrets["late"] == "" {
		t.Fatal("admission hook did not mutate the caller-owned map")
	}
	if len(receivedSecrets) != 0 {
		t.Fatalf("continuation observed post-entry secrets: %#v", receivedSecrets)
	}
}

func TestHumanTaskResumeValidatesCASSchemaAndIdempotency(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	executor := &Executor{WorkspaceDir: workspace, Store: NewFileRunStore(workspace)}
	workflow := parseWorkflow(t, `
name: Human task validation
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: approve
        uses: human/task
        with:
          title: Approve
          questions: [{id: approval}]
          response_schema:
            type: object
            required: [approved]
            properties:
              approved: {type: boolean}
`)
	started, err := executor.Run(ctx, RunRequest{Workflow: workflow, WorkflowRef: "workflows/validation.yml"})
	if err != nil {
		t.Fatal(err)
	}
	tasks, _ := executor.ListHumanTasks(ctx, started.RunID)
	task := tasks[0]
	base := HumanTaskResumeRequest{
		ExpectedRevision: task.Revision,
		InputHash:        task.InputHash,
		ResponseID:       "response-1",
		Response:         map[string]any{"approved": true},
	}
	staleRevision := base
	staleRevision.ExpectedRevision++
	if _, resumeErr := executor.ResumeHumanTask(
		ctx,
		started.RunID,
		task.ID,
		staleRevision,
	); !errors.Is(resumeErr, ErrHumanTaskStale) {
		t.Fatalf("stale revision error = %v", resumeErr)
	}
	staleHash := base
	staleHash.InputHash = "wrong"
	if _, resumeErr := executor.ResumeHumanTask(
		ctx,
		started.RunID,
		task.ID,
		staleHash,
	); !errors.Is(resumeErr, ErrHumanTaskStale) {
		t.Fatalf("stale hash error = %v", resumeErr)
	}
	invalid := base
	invalid.Response = map[string]any{"approved": "yes"}
	if _, resumeErr := executor.ResumeHumanTask(
		ctx,
		started.RunID,
		task.ID,
		invalid,
	); !errors.Is(resumeErr, ErrHumanTaskResponseInvalid) {
		t.Fatalf("invalid response error = %v", resumeErr)
	}
	first, err := executor.ResumeHumanTask(ctx, started.RunID, task.ID, base)
	if err != nil || first.Status != RunStatusSucceeded {
		t.Fatalf("first resume result=%#v err=%v", first, err)
	}
	duplicate, err := executor.ResumeHumanTask(ctx, started.RunID, task.ID, base)
	if err != nil || duplicate.Status != RunStatusSucceeded {
		t.Fatalf("duplicate resume result=%#v err=%v", duplicate, err)
	}
	conflict := base
	conflict.Response = map[string]any{"approved": false}
	if _, resumeErr := executor.ResumeHumanTask(
		ctx,
		started.RunID,
		task.ID,
		conflict,
	); !errors.Is(resumeErr, ErrHumanTaskConflict) {
		t.Fatalf("conflicting replay error = %v", resumeErr)
	}
}

func TestHumanTaskConcurrentResumeContinuesOnce(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	registry := NewFunctionRegistry()
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	if err := registry.Register(
		"after",
		func(_ context.Context, _ map[string]any, _ ExecutionContext) (map[string]any, error) {
			if calls.Add(1) == 1 {
				close(entered)
			}
			<-release
			return map[string]any{"ok": true}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, `
name: Concurrent resume
on: {manual: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: wait
        uses: human/task
        with: {title: Continue, questions: [{id: continue}]}
      - id: after
        uses: function/after
`)
	executor := &Executor{WorkspaceDir: workspace, Store: store, Functions: registry, DefaultTimeout: time.Minute}
	started, err := executor.Run(ctx, RunRequest{Workflow: workflow, WorkflowRef: "workflows/concurrent.yml"})
	if err != nil {
		t.Fatal(err)
	}
	tasks, _ := executor.ListHumanTasks(ctx, started.RunID)
	task := tasks[0]
	request := HumanTaskResumeRequest{
		ExpectedRevision: task.Revision,
		InputHash:        task.InputHash,
		ResponseID:       "same-response",
		Response:         map[string]any{"continue": true},
	}
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan *RunResult, 2)
	errs := make(chan error, 2)
	go func() {
		defer wg.Done()
		result, err := executor.ResumeHumanTask(ctx, started.RunID, task.ID, request)
		results <- result
		errs <- err
	}()
	<-entered
	go func() {
		defer wg.Done()
		result, err := executor.ResumeHumanTask(ctx, started.RunID, task.ID, request)
		results <- result
		errs <- err
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent resume error = %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("continuation calls = %d, want 1", calls.Load())
	}
	var sawSucceeded bool
	for result := range results {
		if result != nil && result.Status == RunStatusSucceeded {
			sawSucceeded = true
		}
	}
	if !sawSucceeded {
		t.Fatal("no concurrent caller observed successful continuation")
	}
}

func TestHumanTaskAcceptedResponseSurvivesCallerDisconnect(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	registry := NewFunctionRegistry()
	entered := make(chan struct{})
	release := make(chan struct{})
	if err := registry.Register(
		"after",
		func(stepCtx context.Context, _ map[string]any, _ ExecutionContext) (map[string]any, error) {
			close(entered)
			select {
			case <-release:
				return map[string]any{"ok": true}, nil
			case <-stepCtx.Done():
				return nil, stepCtx.Err()
			}
		},
	); err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, `
name: Detached resume
on: {manual: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: wait
        uses: human/task
        with: {title: Continue, questions: [{id: continue}]}
      - id: after
        uses: function/after
`)
	executor := &Executor{
		WorkspaceDir:   workspace,
		Store:          store,
		Functions:      registry,
		DefaultTimeout: time.Minute,
	}
	started, err := executor.Run(ctx, RunRequest{
		Workflow: workflow, WorkflowRef: "workflows/detached.yml",
	})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() tasks=%#v err=%v", tasks, err)
	}
	requestCtx, cancel := context.WithCancel(ctx)
	resultCh := make(chan *RunResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, resumeErr := executor.ResumeHumanTask(
			requestCtx,
			started.RunID,
			tasks[0].ID,
			HumanTaskResumeRequest{
				ExpectedRevision: tasks[0].Revision,
				InputHash:        tasks[0].InputHash,
				ResponseID:       "response-1",
				Response:         true,
			},
		)
		resultCh <- result
		errCh <- resumeErr
	}()
	<-entered
	cancel()
	time.Sleep(20 * time.Millisecond)
	close(release)
	result := <-resultCh
	if resumeErr := <-errCh; resumeErr != nil {
		t.Fatalf("ResumeHumanTask() after caller cancellation error = %v", resumeErr)
	}
	if result == nil || result.Status != RunStatusSucceeded {
		t.Fatalf("ResumeHumanTask() result = %#v, want succeeded", result)
	}
}

func TestHumanTaskResumeReacquiresTopLevelConcurrencyAdmission(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	executor := &Executor{
		WorkspaceDir:      workspace,
		Store:             store,
		MaxConcurrentRuns: 1,
	}
	workflow := parseWorkflow(t, `
name: Resume admission
on: {manual: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: wait
        uses: human/task
        with: {title: Continue, questions: [{id: continue}]}
`)
	started, err := executor.Run(ctx, RunRequest{
		Workflow: workflow, WorkflowRef: "workflows/admission.yml",
	})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() tasks=%#v err=%v", tasks, err)
	}
	other := &Run{
		ID:          "other-running-run",
		WorkflowRef: "workflows/other.yml",
		Status:      RunStatusRunning,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if createErr := store.CreateRun(ctx, other); createErr != nil {
		t.Fatal(createErr)
	}
	task := tasks[0]
	_, err = executor.ResumeHumanTask(ctx, started.RunID, task.ID, HumanTaskResumeRequest{
		ExpectedRevision: task.Revision,
		InputHash:        task.InputHash,
		ResponseID:       "response-1",
		Response:         true,
	})
	if !errors.Is(err, ErrRunConcurrencyLimit) {
		t.Fatalf("ResumeHumanTask() error = %v, want concurrency limit", err)
	}
	persisted, err := store.GetRun(ctx, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != RunStatusWaiting ||
		persisted.humanTasks[task.ID].Status != HumanTaskStatusWaiting {
		t.Fatalf("rejected resume mutated run: %#v task=%#v", persisted, persisted.humanTasks[task.ID])
	}
}

func TestHumanTaskExpiredClaimReacquiresTopLevelConcurrencyAdmission(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	workflow := parseWorkflow(t, `
name: Reclaim admission
on: {manual: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: wait
        uses: human/task
        with: {title: Continue, questions: [{id: continue}]}
`)
	started, err := executor.Run(ctx, RunRequest{
		Workflow: workflow, WorkflowRef: "workflows/reclaim-admission.yml",
	})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() tasks=%#v err=%v", tasks, err)
	}
	task := tasks[0]
	request := HumanTaskResumeRequest{
		ExpectedRevision: task.Revision,
		InputHash:        task.InputHash,
		ResponseID:       "response-1",
		Response:         true,
		maxConcurrent:    1,
		resumeLease:      time.Millisecond,
	}
	claimed, _, duplicate, err := store.ClaimHumanTask(ctx, started.RunID, task.ID, request)
	if err != nil || duplicate {
		t.Fatalf("first claim duplicate=%v err=%v", duplicate, err)
	}
	firstToken := claimed.execution.Resume.Token
	time.Sleep(time.Until(claimed.execution.Resume.ExpiresAt) + time.Millisecond)

	now := time.Now().UTC()
	other := &Run{
		ID:          "other-running-during-reclaim",
		WorkflowRef: "workflows/other.yml",
		Status:      RunStatusRunning,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if createErr := store.CreateRun(ctx, other); createErr != nil {
		t.Fatal(createErr)
	}
	if _, _, _, claimErr := store.ClaimHumanTask(
		ctx,
		started.RunID,
		task.ID,
		request,
	); !errors.Is(claimErr, ErrRunConcurrencyLimit) {
		t.Fatalf("expired claim error = %v, want concurrency limit", claimErr)
	}
	persisted, err := store.GetRun(ctx, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.execution.Resume.Token != firstToken {
		t.Fatal("rejected expired reclaim replaced the persisted token")
	}

	if deleteErr := store.DeleteRun(ctx, other.ID); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	reclaimed, _, duplicate, err := store.ClaimHumanTask(ctx, started.RunID, task.ID, request)
	if err != nil || duplicate {
		t.Fatalf("reclaim after admission duplicate=%v err=%v", duplicate, err)
	}
	if reclaimed.execution.Resume.Token == firstToken {
		t.Fatal("successful expired reclaim did not replace the persisted token")
	}
}

func TestHumanTaskCancelIsAtomicAndMarksTask(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	executor := &Executor{WorkspaceDir: workspace, Store: NewFileRunStore(workspace)}
	workflow := parseWorkflow(t, `
name: Cancel task
on: {manual: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: wait
        uses: human/task
        with: {title: Continue, questions: [{id: continue}]}
`)
	started, err := executor.Run(ctx, RunRequest{Workflow: workflow, WorkflowRef: "workflows/cancel.yml"})
	if err != nil {
		t.Fatal(err)
	}
	tasks, _ := executor.ListHumanTasks(ctx, started.RunID)
	task := tasks[0]
	canceled, err := executor.CancelHumanTask(ctx, started.RunID, task.ID, "operator canceled")
	if err != nil || canceled.Status != RunStatusCanceled {
		t.Fatalf("CancelHumanTask() run=%#v err=%v", canceled, err)
	}
	tasks, _ = executor.ListHumanTasks(ctx, started.RunID)
	if tasks[0].Status != HumanTaskStatusCanceled || tasks[0].CanceledAt == nil {
		t.Fatalf("canceled task = %#v", tasks[0])
	}
	if _, err := executor.ResumeHumanTask(ctx, started.RunID, task.ID, HumanTaskResumeRequest{
		ExpectedRevision: task.Revision,
		InputHash:        task.InputHash,
		ResponseID:       "late",
		Response:         true,
	}); !errors.Is(err, ErrHumanTaskConflict) {
		t.Fatalf("resume canceled task error = %v", err)
	}
}

func TestHumanTaskStaleClaimCanBeReclaimedByExactResponse(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	workflow := parseWorkflow(t, `
name: Reclaim
on: {manual: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: wait
        uses: human/task
        with: {title: Continue, questions: [{id: continue}]}
`)
	started, err := executor.Run(ctx, RunRequest{Workflow: workflow, WorkflowRef: "workflows/reclaim.yml"})
	if err != nil {
		t.Fatal(err)
	}
	tasks, _ := executor.ListHumanTasks(ctx, started.RunID)
	task := tasks[0]
	request := HumanTaskResumeRequest{
		ExpectedRevision: task.Revision,
		InputHash:        task.InputHash,
		ResponseID:       "response-1",
		Response:         true,
		resumeLease:      time.Millisecond,
	}
	claimed, _, duplicate, err := store.ClaimHumanTask(ctx, started.RunID, task.ID, request)
	if err != nil || duplicate {
		t.Fatalf("first claim duplicate=%v err=%v", duplicate, err)
	}
	firstToken := claimed.execution.Resume.Token
	time.Sleep(5 * time.Millisecond)
	reclaimed, _, duplicate, err := store.ClaimHumanTask(ctx, started.RunID, task.ID, request)
	if err != nil || duplicate {
		t.Fatalf("reclaim duplicate=%v err=%v", duplicate, err)
	}
	if reclaimed.execution.Resume.Token == firstToken {
		t.Fatal("stale claim token was not replaced")
	}
	claimed.Status = RunStatusSucceeded
	if err := store.UpdateRun(ctx, claimed); !errors.Is(err, ErrHumanTaskConflict) {
		t.Fatalf("stale claimant update error = %v", err)
	}
}

func TestHumanTaskValidationRejectsUnsafeContextsAndSecretReferences(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "workflow call",
			yaml: `name: x
on: {workflow_call: {}}
jobs: {main: {runs-on: picoclaw, steps: [{uses: human/task, with: {title: Review, questions: [one]}}]}}`,
			want: "reusable workflow definitions",
		},
		{
			name: "secret",
			yaml: `name: x
on: {manual: {}}
jobs: {main: {runs-on: picoclaw, steps: [{uses: human/task, with: {title: Review, questions: "${{ secrets.token }}"}}]}}`,
			want: "cannot reference secrets",
		},
		{
			name: "event",
			yaml: `name: x
on: {event: {types: [issue.opened]}}
jobs: {main: {runs-on: picoclaw, steps: [{uses: human/task, with: {title: Review, questions: [one]}}]}}`,
			want: "durable event workflows",
		},
		{
			name: "reusable job",
			yaml: `name: x
on: {manual: {}}
jobs:
  child: {uses: workflows/child.yml}
  main: {runs-on: picoclaw, steps: [{uses: human/task, with: {title: Review, questions: [one]}}]}`,
			want: "cannot call reusable workflows",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workflow, err := Parse([]byte(test.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(workflow); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestHumanTaskInspectionClassifiesBuiltInPrimitive(t *testing.T) {
	inspection, err := InspectWorkflowDefinitionBytes(
		workflowInspectionTestSource(),
		[]byte(`name: Human
on: {manual: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: human/task
        with: {title: Review, questions: [one]}
`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Jobs) != 1 || len(inspection.Jobs[0].Steps) != 1 ||
		inspection.Jobs[0].Steps[0].Kind != WorkflowDefinitionStepHuman ||
		inspection.Jobs[0].Steps[0].Target != "human/task" {
		t.Fatalf("inspection jobs = %#v", inspection.Jobs)
	}
	if len(inspection.Dependencies) != 1 ||
		inspection.Dependencies[0].Kind != WorkflowDependencyKindHuman ||
		inspection.Dependencies[0].Target != "task" {
		t.Fatalf("inspection dependencies = %#v", inspection.Dependencies)
	}
	if len(inspection.Effects) != 1 ||
		inspection.Effects[0].Kind != WorkflowDefinitionEffectStateChange {
		t.Fatalf("inspection effects = %#v", inspection.Effects)
	}
}
