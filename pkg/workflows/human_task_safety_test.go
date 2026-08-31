package workflows

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockingHumanTaskHandoffStore struct {
	*FileRunStore

	mu            sync.Mutex
	waitingWrites int
	reached       chan int
	release       chan struct{}
}

type humanTaskRunOutcome struct {
	result *RunResult
	err    error
}

type conflictingHumanTaskHeartbeatStore struct {
	*FileRunStore
	renewCalls atomic.Int32
}

func (s *conflictingHumanTaskHeartbeatStore) RenewHumanTaskClaim(
	context.Context,
	string,
	string,
	string,
	time.Duration,
) error {
	s.renewCalls.Add(1)
	return ErrHumanTaskConflict
}

func newBlockingHumanTaskHandoffStore(workspace string) *blockingHumanTaskHandoffStore {
	return &blockingHumanTaskHandoffStore{
		FileRunStore: NewFileRunStore(workspace),
		reached:      make(chan int, 2),
		release:      make(chan struct{}),
	}
}

func (s *blockingHumanTaskHandoffStore) UpdateRun(ctx context.Context, run *Run) error {
	if run != nil && run.Status == RunStatusWaiting {
		s.mu.Lock()
		s.waitingWrites++
		ordinal := s.waitingWrites
		s.mu.Unlock()
		s.reached <- ordinal
		<-s.release
	}
	return s.FileRunStore.UpdateRun(ctx, run)
}

func TestHumanTaskWaitPublicationIsAtomicAcrossEveryHandoff(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := newBlockingHumanTaskHandoffStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	workflow := parseWorkflow(t, `
name: Atomic task handoffs
on: {manual: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: first
        uses: human/task
        with: {title: First, questions: [{id: first}]}
      - id: second
        uses: human/task
        with: {title: Second, questions: [{id: second}]}
`)
	const runID = "atomic-human-task-handoff"

	startedCh := make(chan humanTaskRunOutcome, 1)
	go func() {
		result, err := executor.Run(ctx, RunRequest{
			RunID: runID, Workflow: workflow, WorkflowRef: "workflows/atomic.yml",
		})
		startedCh <- humanTaskRunOutcome{result: result, err: err}
	}()
	waitForHumanTaskHandoff(t, store, 1)

	firstID := humanTaskID(runID, "main", "first")
	assertHumanTaskUnavailableBeforeHandoff(t, executor, runID, firstID)
	tasks, err := executor.ListHumanTasks(ctx, runID)
	if err != nil || len(tasks) != 0 {
		t.Errorf("tasks before first handoff = %#v, err=%v; want none", tasks, err)
	}
	store.release <- struct{}{}
	started := receiveHumanTaskOutcome(t, startedCh)
	if started.err != nil || started.result == nil || started.result.Status != RunStatusWaiting {
		t.Fatalf("initial Run() result=%#v err=%v, want waiting", started.result, started.err)
	}
	tasks, err = executor.ListHumanTasks(ctx, runID)
	if err != nil || len(tasks) != 1 || tasks[0].ID != firstID || tasks[0].Status != HumanTaskStatusWaiting {
		t.Fatalf("tasks after first handoff = %#v, err=%v", tasks, err)
	}
	first := tasks[0]

	resumedCh := make(chan humanTaskRunOutcome, 1)
	go func() {
		result, resumeErr := executor.ResumeHumanTask(ctx, runID, first.ID, HumanTaskResumeRequest{
			ExpectedRevision: first.Revision,
			InputHash:        first.InputHash,
			ResponseID:       "first-response",
			Response:         true,
		})
		resumedCh <- humanTaskRunOutcome{result: result, err: resumeErr}
	}()
	waitForHumanTaskHandoff(t, store, 2)

	secondID := humanTaskID(runID, "main", "second")
	assertHumanTaskUnavailableBeforeHandoff(t, executor, runID, secondID)
	tasks, err = executor.ListHumanTasks(ctx, runID)
	if err != nil || len(tasks) != 1 || tasks[0].ID != firstID || tasks[0].Status != HumanTaskStatusContinuing {
		t.Errorf("tasks during second handoff = %#v, err=%v; want only continuing first task", tasks, err)
	}
	store.release <- struct{}{}
	resumed := receiveHumanTaskOutcome(t, resumedCh)
	if resumed.err != nil || resumed.result == nil || resumed.result.Status != RunStatusWaiting {
		t.Fatalf("first resume result=%#v err=%v, want waiting", resumed.result, resumed.err)
	}
	tasks, err = executor.ListHumanTasks(ctx, runID)
	if err != nil || len(tasks) != 2 || tasks[0].Status != HumanTaskStatusAnswered ||
		tasks[1].ID != secondID || tasks[1].Status != HumanTaskStatusWaiting {
		t.Fatalf("tasks after second handoff = %#v, err=%v", tasks, err)
	}
}

func waitForHumanTaskHandoff(t *testing.T, store *blockingHumanTaskHandoffStore, want int) {
	t.Helper()
	select {
	case got := <-store.reached:
		if got != want {
			t.Fatalf("waiting handoff ordinal = %d, want %d", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for human-task handoff %d", want)
	}
}

func receiveHumanTaskOutcome(t *testing.T, outcomes <-chan humanTaskRunOutcome) humanTaskRunOutcome {
	t.Helper()
	select {
	case outcome := <-outcomes:
		return outcome
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for workflow outcome")
		return humanTaskRunOutcome{}
	}
}

func assertHumanTaskUnavailableBeforeHandoff(
	t *testing.T,
	executor *Executor,
	runID string,
	taskID string,
) {
	t.Helper()
	request := HumanTaskResumeRequest{
		ExpectedRevision: 1,
		InputHash:        "not-yet-published",
		ResponseID:       "premature-response",
		Response:         true,
	}
	if _, err := executor.ResumeHumanTask(context.Background(), runID, taskID, request); !errors.Is(
		err,
		ErrHumanTaskNotFound,
	) {
		t.Errorf("premature ResumeHumanTask(%s) error = %v, want not found", taskID, err)
	}
	if _, err := executor.CancelHumanTask(context.Background(), runID, taskID, "premature cancel"); !errors.Is(
		err,
		ErrHumanTaskNotFound,
	) {
		t.Errorf("premature CancelHumanTask(%s) error = %v, want not found", taskID, err)
	}
}

func TestHumanTaskCheckpointPreservesExactNumbersAcrossRestart(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	const (
		exactInput    = "9007199254740993"
		exactBefore   = "9007199254740995"
		exactResponse = "9007199254740997"
	)
	registry := NewFunctionRegistry()
	exactBeforeFunction := func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		return map[string]any{"exact": json.Number(exactBefore)}, nil
	}
	if err := registry.Register("exact-before", exactBeforeFunction); err != nil {
		t.Fatal(err)
	}
	var afterArgs map[string]any
	exactAfterFunction := func(_ context.Context, args map[string]any, _ ExecutionContext) (map[string]any, error) {
		afterArgs = cloneMap(args)
		return cloneMap(args), nil
	}
	if err := registry.Register("exact-after", exactAfterFunction); err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, `
name: Exact checkpoint numbers
on: {manual: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: before
        uses: function/exact-before
      - id: review
        uses: human/task
        with:
          title: Preserve exact numbers
          questions:
            - id: exact
              input: ${{ inputs.exact }}
              before: ${{ steps.before.outputs.exact }}
          response_schema:
            type: object
            required: [exact]
            properties:
              exact: {type: integer}
      - id: after
        uses: function/exact-after
        with:
          input: ${{ inputs.exact }}
          before: ${{ steps.before.outputs.exact }}
          response: ${{ steps.review.outputs.response.exact }}
`)
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store, Functions: registry}
	started, err := executor.Run(ctx, RunRequest{
		Workflow: workflow, WorkflowRef: "workflows/exact.yml",
		Inputs: map[string]any{"exact": json.Number(exactInput)},
	})
	if err != nil || started.Status != RunStatusWaiting {
		t.Fatalf("Run() result=%#v err=%v, want waiting", started, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() tasks=%#v err=%v", tasks, err)
	}
	questions, ok := tasks[0].Questions.([]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("questions = %#v", tasks[0].Questions)
	}
	question, ok := questions[0].(map[string]any)
	if !ok {
		t.Fatalf("question = %#v", questions[0])
	}
	assertHumanTaskExactJSONNumber(t, "task question input", question["input"], exactInput)
	assertHumanTaskExactJSONNumber(t, "task question before output", question["before"], exactBefore)

	// A new store and executor force every continuation input through durable JSON.
	restarted := &Executor{
		WorkspaceDir: workspace,
		Store:        NewFileRunStore(workspace),
		Functions:    registry,
	}
	resumed, err := restarted.ResumeHumanTask(ctx, started.RunID, tasks[0].ID, HumanTaskResumeRequest{
		ExpectedRevision: tasks[0].Revision,
		InputHash:        tasks[0].InputHash,
		ResponseID:       "exact-response",
		Response:         map[string]any{"exact": json.Number(exactResponse)},
	})
	if err != nil || resumed.Status != RunStatusSucceeded {
		t.Fatalf("ResumeHumanTask() result=%#v err=%v, want succeeded", resumed, err)
	}
	assertHumanTaskExactJSONNumber(t, "after input", afterArgs["input"], exactInput)
	assertHumanTaskExactJSONNumber(t, "after pre-task output", afterArgs["before"], exactBefore)
	assertHumanTaskExactJSONNumber(t, "after task response", afterArgs["response"], exactResponse)

	persisted, err := restarted.Store.GetRun(ctx, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	assertHumanTaskExactJSONNumber(t, "persisted input", persisted.Inputs["exact"], exactInput)
	assertHumanTaskExactJSONNumber(
		t,
		"persisted pre-task output",
		persisted.Steps["main/before"].Outputs["exact"],
		exactBefore,
	)
	assertHumanTaskExactJSONNumber(
		t,
		"persisted downstream response",
		persisted.Steps["main/after"].Outputs["response"],
		exactResponse,
	)
	tasks, err = restarted.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("persisted tasks=%#v err=%v", tasks, err)
	}
	response, ok := tasks[0].Response.(map[string]any)
	if !ok {
		t.Fatalf("persisted response = %#v", tasks[0].Response)
	}
	assertHumanTaskExactJSONNumber(t, "persisted task response", response["exact"], exactResponse)
}

func assertHumanTaskExactJSONNumber(t *testing.T, label string, value any, want string) {
	t.Helper()
	number, ok := value.(json.Number)
	if !ok || number.String() != want {
		t.Fatalf("%s = %#v (%T), want json.Number(%s)", label, value, value, want)
	}
}

func TestHumanTaskResumeDoesNotRestartOrReevaluateCursorJob(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	workflow := parseWorkflow(t, `
name: Resume job cursor
on: {manual: {}}
jobs:
  guarded:
    if: ${{ secrets.initial_gate }}
    runs-on: picoclaw
    steps:
      - id: review
        uses: human/task
        with: {title: Continue, questions: [{id: continue}]}
      - id: after
        uses: function/after-cursor
`)
	registry := NewFunctionRegistry()
	var afterCalls int
	afterCursorFunction := func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		afterCalls++
		return map[string]any{"ok": true}, nil
	}
	if err := registry.Register("after-cursor", afterCursorFunction); err != nil {
		t.Fatal(err)
	}
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store, Functions: registry}
	started, err := executor.Run(ctx, RunRequest{
		Workflow: workflow, WorkflowRef: "workflows/cursor.yml",
		Secrets: map[string]string{"initial_gate": "open"},
	})
	if err != nil || started.Status != RunStatusWaiting {
		t.Fatalf("Run() result=%#v err=%v, want waiting", started, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%#v err=%v", tasks, err)
	}
	// Deliberately omit initial_gate. Re-evaluating the job-level if would skip
	// the already-started job instead of continuing from its durable cursor.
	resumed, err := executor.ResumeHumanTask(ctx, started.RunID, tasks[0].ID, HumanTaskResumeRequest{
		ExpectedRevision: tasks[0].Revision,
		InputHash:        tasks[0].InputHash,
		ResponseID:       "cursor-response",
		Response:         true,
	})
	if err != nil || resumed.Status != RunStatusSucceeded {
		t.Fatalf("ResumeHumanTask() result=%#v err=%v, want succeeded", resumed, err)
	}
	if afterCalls != 1 {
		t.Fatalf("downstream calls = %d, want 1", afterCalls)
	}
	events, err := store.Events(ctx, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	jobStarts := 0
	for _, event := range events {
		if event.Kind == "workflow.job.start" && event.JobID == "guarded" {
			jobStarts++
		}
	}
	if jobStarts != 1 {
		t.Fatalf("workflow.job.start events = %d, want exactly 1", jobStarts)
	}
}

func TestHumanTaskClaimRejectsCorruptCheckpointWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Run)
	}{
		{
			name: "missing snapshot",
			mutate: func(_ *testing.T, run *Run) {
				run.execution.Workflow = nil
			},
		},
		{
			name: "snapshot digest mismatch",
			mutate: func(_ *testing.T, run *Run) {
				run.execution.Workflow.Name = "tampered after digest"
			},
		},
		{
			name: "cursor mismatch",
			mutate: func(_ *testing.T, run *Run) {
				run.execution.Cursor.StepIndex = 99
			},
		},
		{
			name: "validator upgrade rejects snapshot",
			mutate: func(t *testing.T, run *Run) {
				job := run.execution.Workflow.Jobs["main"]
				job.Steps[0].ContinueOnError = true
				run.execution.Workflow.Jobs["main"] = job
				run.execution.WorkflowRevision = workflowSnapshotDigest(t, run.execution.Workflow)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			store := NewFileRunStore(workspace)
			executor := &Executor{WorkspaceDir: workspace, Store: store}
			workflow := parseWorkflow(t, `
name: Claim validation
on: {manual: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: review
        uses: human/task
        with: {title: Review, questions: [{id: approve}]}
`)
			started, err := executor.Run(ctx, RunRequest{
				Workflow: workflow, WorkflowRef: "workflows/claim-validation.yml",
			})
			if err != nil || started.Status != RunStatusWaiting {
				t.Fatalf("Run() result=%#v err=%v", started, err)
			}
			tasks, err := executor.ListHumanTasks(ctx, started.RunID)
			if err != nil || len(tasks) != 1 {
				t.Fatalf("tasks=%#v err=%v", tasks, err)
			}
			task := tasks[0]
			run, err := store.GetRun(ctx, started.RunID)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, run)
			overwritePersistedWorkflowExecutionForTest(t, workspace, run)
			before := persistedWorkflowClaimSnapshotForTest(
				t,
				workspace,
				started.RunID,
				task.ID,
			)
			_, err = executor.ResumeHumanTask(ctx, started.RunID, task.ID, HumanTaskResumeRequest{
				ExpectedRevision: task.Revision,
				InputHash:        task.InputHash,
				ResponseID:       "must-not-be-claimed",
				Response:         true,
			})
			if !errors.Is(err, ErrHumanTaskConflict) {
				t.Fatalf("ResumeHumanTask() error = %v, want conflict", err)
			}
			after := persistedWorkflowClaimSnapshotForTest(
				t,
				workspace,
				started.RunID,
				task.ID,
			)
			if !bytes.Equal(after, before) {
				t.Fatal("rejected claim mutated the persisted waiting run")
			}
			tasks, err = executor.ListHumanTasks(ctx, started.RunID)
			if err != nil || len(tasks) != 1 || tasks[0].Status != HumanTaskStatusWaiting ||
				tasks[0].Revision != task.Revision || tasks[0].ResponseID != "" || tasks[0].Response != nil {
				t.Fatalf("task after rejected claim = %#v, err=%v", tasks, err)
			}
		})
	}
}

func workflowSnapshotDigest(t *testing.T, workflow *Workflow) string {
	t.Helper()
	data, err := json.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func TestHumanTaskValidationRejectsMalformedContractsAndBounds(t *testing.T) {
	tests := []struct {
		name string
		with map[string]any
		want string
	}{
		{
			name: "response schema is not an object",
			with: map[string]any{
				"title": "Review", "questions": []any{"one"},
				"response_schema": []any{"not", "an", "object"},
			},
			want: "response_schema must be an object",
		},
		{
			name: "unknown response schema keyword",
			with: map[string]any{
				"title": "Review", "questions": []any{"one"},
				"response_schema": map[string]any{"type": "number", "minimum": 0},
			},
			want: "unsupported response_schema keyword",
		},
		{
			name: "title bound",
			with: map[string]any{
				"title": strings.Repeat("t", MaxHumanTaskTitleBytes+1), "questions": []any{"one"},
			},
			want: fmt.Sprintf("at most %d bytes", MaxHumanTaskTitleBytes),
		},
		{
			name: "input hash bound",
			with: map[string]any{
				"title": "Review", "questions": []any{"one"},
				"input_hash": strings.Repeat("h", MaxHumanTaskInputHashBytes+1),
			},
			want: fmt.Sprintf("at most %d bytes", MaxHumanTaskInputHashBytes),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workflow := humanTaskContractWorkflow(test.with)
			if err := Validate(workflow); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}

	for _, field := range []string{"title", "input_hash"} {
		t.Run("rendered "+field+" bound", func(t *testing.T) {
			with := map[string]any{
				"title": "Review", "questions": []any{"one"}, "input_hash": "fixed",
			}
			with[field] = "${{ inputs.oversized }}"
			workflow := humanTaskContractWorkflow(with)
			result, err := (&Executor{WorkspaceDir: t.TempDir()}).Run(context.Background(), RunRequest{
				Workflow: workflow, WorkflowRef: "workflows/rendered-bound.yml",
				Inputs: map[string]any{
					"oversized": strings.Repeat("x", MaxHumanTaskTitleBytes+MaxHumanTaskInputHashBytes+1),
				},
			})
			if err == nil || result == nil || result.Status != RunStatusFailed {
				t.Fatalf("Run() result=%#v err=%v, want failed bound rejection", result, err)
			}
			if !strings.Contains(err.Error(), "human/task") && !strings.Contains(err.Error(), "exceeds") {
				t.Fatalf("Run() error = %v, want human-task bound error", err)
			}
		})
	}
}

func humanTaskContractWorkflow(with map[string]any) *Workflow {
	return &Workflow{
		Name: "Human task contract",
		On:   WorkflowTriggers{Manual: map[string]any{}},
		Jobs: map[string]Job{
			"main": {
				RunsOn: "picoclaw",
				Steps: []Step{{
					ID: "review", Uses: "human/task", With: with,
				}},
			},
		},
	}
}

func TestWorkflowDependencyClosureBlocksReachableHumanTaskAndReusableEdge(t *testing.T) {
	const (
		rootRef  = "workflows/root.yml"
		childRef = "workflows/reachable-human.yml"
	)
	root := &Workflow{
		Name: "Root",
		On:   WorkflowTriggers{Manual: map[string]any{}},
		Jobs: map[string]Job{"child": {Uses: childRef}},
	}
	child := humanTaskContractWorkflow(map[string]any{
		"title": "Reachable review", "questions": []any{"approve"},
	})
	loader := &dependencyTestLoader{
		workflows: map[string]*Workflow{childRef: child},
		calls:     make(map[string]int),
	}
	report, err := CheckWorkflowDependencyClosure(context.Background(), WorkflowDependencyCheckRequest{
		RootRef: rootRef, RootWorkflow: root, Loader: loader,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready() {
		t.Fatalf("reachable human/reusable closure unexpectedly ready: %#v", report)
	}
	foundHuman := false
	foundStructuralBlock := false
	for _, dependency := range report.Dependencies {
		if dependency.Kind == WorkflowDependencyKindHuman && dependency.WorkflowRef == childRef {
			foundHuman = true
		}
	}
	for _, issue := range report.Issues {
		if issue.Code == WorkflowDependencyIssueHumanTaskReusableUnsupported &&
			issue.WorkflowRef == rootRef && issue.DependencyName == childRef {
			foundStructuralBlock = true
		}
	}
	if !foundHuman || !foundStructuralBlock {
		t.Fatalf("closure dependencies/issues = %#v / %#v", report.Dependencies, report.Issues)
	}
}

func TestHumanTaskCrashAfterClaimProjectsRecoveryAndExactReplayResumes(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	workflow := parseWorkflow(t, `
name: Crash recovery
on: {manual: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: review
        uses: human/task
        with: {title: Continue, questions: [{id: continue}]}
`)
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	started, err := executor.Run(ctx, RunRequest{
		Workflow: workflow, WorkflowRef: "workflows/crash-recovery.yml",
	})
	if err != nil || started.Status != RunStatusWaiting {
		t.Fatalf("Run() result=%#v err=%v", started, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%#v err=%v", tasks, err)
	}
	task := tasks[0]
	request := HumanTaskResumeRequest{
		ExpectedRevision: task.Revision,
		InputHash:        task.InputHash,
		ResponseID:       "durable-exact-response",
		Response:         map[string]any{"continue": true},
		resumeLease:      time.Hour,
	}
	claimed, _, duplicate, err := store.ClaimHumanTask(ctx, started.RunID, task.ID, request)
	if err != nil || duplicate {
		t.Fatalf("ClaimHumanTask() duplicate=%v err=%v", duplicate, err)
	}
	if claimed.execution == nil || claimed.execution.Resume == nil {
		t.Fatalf("claimed run missing resume lease: %#v", claimed)
	}
	projected, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(projected) != 1 || projected[0].Status != HumanTaskStatusContinuing ||
		projected[0].RetryAt == nil {
		t.Fatalf("continuing projection = %#v, err=%v", projected, err)
	}
	if !projected[0].RetryAt.Equal(claimed.execution.Resume.ExpiresAt) {
		t.Fatalf("retry_at = %v, want %v", projected[0].RetryAt, claimed.execution.Resume.ExpiresAt)
	}

	// Simulate the continuation process crashing and its durable lease expiring.
	crashed, err := store.GetRun(ctx, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	crashed.execution.Resume.ExpiresAt = time.Now().UTC().Add(-time.Second)
	overwritePersistedWorkflowExecutionForTest(t, workspace, crashed)
	projected, err = executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(projected) != 1 || projected[0].Status != HumanTaskStatusRecoveryRequired ||
		projected[0].RetryAt == nil {
		t.Fatalf("recovery projection = %#v, err=%v", projected, err)
	}
	if projected[0].RetryAt.After(time.Now().UTC()) {
		t.Fatalf("recovery retry_at = %v, want expired", projected[0].RetryAt)
	}

	// Replay the original request, including its stale pre-claim revision. Exact
	// response identity is sufficient to reclaim only after lease expiry.
	restarted := &Executor{WorkspaceDir: workspace, Store: NewFileRunStore(workspace)}
	resumed, err := restarted.ResumeHumanTask(ctx, started.RunID, task.ID, request)
	if err != nil || resumed.Status != RunStatusSucceeded {
		t.Fatalf("exact replay result=%#v err=%v, want succeeded", resumed, err)
	}
	projected, err = restarted.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(projected) != 1 || projected[0].Status != HumanTaskStatusAnswered ||
		projected[0].RetryAt != nil {
		t.Fatalf("completed projection = %#v, err=%v", projected, err)
	}
}

func TestHumanTaskHeartbeatPreventsLiveContinuationReclaim(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	registry := NewFunctionRegistry()
	var calls int
	entered := make(chan struct{})
	release := make(chan struct{})
	heldContinuationFunction := func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		calls++
		if calls == 1 {
			close(entered)
		}
		<-release
		return map[string]any{"ok": true}, nil
	}
	if err := registry.Register("held-continuation", heldContinuationFunction); err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, `
name: Live claim heartbeat
on: {manual: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: review
        uses: human/task
        with: {title: Continue, questions: [{id: continue}]}
      - id: held
        uses: function/held-continuation
`)
	executor := &Executor{WorkspaceDir: workspace, Store: store, Functions: registry}
	started, err := executor.Run(ctx, RunRequest{
		Workflow: workflow, WorkflowRef: "workflows/live-heartbeat.yml",
	})
	if err != nil || started.Status != RunStatusWaiting {
		t.Fatalf("Run() result=%#v err=%v", started, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%#v err=%v", tasks, err)
	}
	task := tasks[0]
	request := HumanTaskResumeRequest{
		ExpectedRevision: task.Revision,
		InputHash:        task.InputHash,
		ResponseID:       "live-response",
		Response:         true,
		// Coverage runs can pause instrumented test goroutines for hundreds of
		// milliseconds. Keep enough scheduling slack while still observing the
		// claim after its original expiry below.
		resumeLease: 2 * time.Second,
	}
	claimed, claimedTask, duplicate, err := store.ClaimHumanTask(
		ctx,
		started.RunID,
		task.ID,
		request,
	)
	if err != nil || duplicate || claimed == nil || claimed.execution == nil || claimed.execution.Resume == nil {
		t.Fatalf("ClaimHumanTask() run=%#v duplicate=%v err=%v", claimed, duplicate, err)
	}
	resultCh := make(chan humanTaskRunOutcome, 1)
	go func() {
		stopHeartbeat := startHumanTaskClaimHeartbeat(
			store,
			claimed.ID,
			claimedTask.ID,
			claimed.execution.Resume.Token,
			request.resumeLease,
			claimed.execution.Resume.ExpiresAt,
			nil,
		)
		defer stopHeartbeat()
		cursor := *claimed.execution.Cursor
		outputs, continuationErr := executor.executeWorkflow(
			ctx,
			store,
			claimed,
			claimed.execution.Workflow,
			RunRequest{Inputs: cloneMap(claimed.Inputs), Event: cloneMap(claimed.Event)},
			&cursor,
		)
		if continuationErr != nil {
			resultCh <- humanTaskRunOutcome{err: continuationErr}
			return
		}
		claimed.Status = RunStatusSucceeded
		claimed.Outputs = outputs
		completedAt := time.Now().UTC()
		claimed.CompletedAt = &completedAt
		if updateErr := store.UpdateRun(ctx, claimed); updateErr != nil {
			resultCh <- humanTaskRunOutcome{err: updateErr}
			return
		}
		resultCh <- humanTaskRunOutcome{result: &RunResult{
			RunID: claimed.ID, Status: claimed.Status, Outputs: outputs,
		}}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("continuation did not start")
	}
	projected, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(projected) != 1 || projected[0].Status != HumanTaskStatusContinuing ||
		projected[0].RetryAt == nil {
		close(release)
		t.Fatalf("initial continuing projection=%#v err=%v", projected, err)
	}
	initialExpiry := *projected[0].RetryAt
	wait := time.Until(initialExpiry) + 25*time.Millisecond
	if wait > 0 {
		time.Sleep(wait)
	}
	projected, err = executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(projected) != 1 || projected[0].Status != HumanTaskStatusContinuing ||
		projected[0].RetryAt == nil || !projected[0].RetryAt.After(initialExpiry) {
		close(release)
		t.Fatalf("heartbeat projection=%#v err=%v; initial expiry=%v", projected, err, initialExpiry)
	}

	replayed, _, replayDuplicate, claimErr := store.ClaimHumanTask(ctx, started.RunID, task.ID, request)
	if claimErr != nil || !replayDuplicate || replayed == nil || replayed.Status != RunStatusRunning {
		close(release)
		t.Fatalf("live exact replay duplicate=%v run=%#v err=%v", replayDuplicate, replayed, claimErr)
	}
	if calls != 1 {
		close(release)
		t.Fatalf("continuation calls before release = %d, want 1", calls)
	}
	close(release)
	outcome := receiveHumanTaskOutcome(t, resultCh)
	if outcome.err != nil || outcome.result == nil || outcome.result.Status != RunStatusSucceeded {
		t.Fatalf("held continuation result=%#v err=%v", outcome.result, outcome.err)
	}
	if calls != 1 {
		t.Fatalf("continuation calls = %d, want exactly 1", calls)
	}
	persisted, err := store.GetRun(ctx, started.RunID)
	if err != nil || persisted.Status != RunStatusSucceeded {
		t.Fatalf("persisted run=%#v err=%v", persisted, err)
	}
}

func TestHumanTaskStaleReclaimSkipsCheckpointedLaterJobAdmissionAndEffects(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	registry := NewFunctionRegistry()
	var effectCalls int
	laterEffectFunction := func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		effectCalls++
		return map[string]any{"marker": "original"}, nil
	}
	if err := registry.Register("later-effect", laterEffectFunction); err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, `
name: Later job reclaim checkpoint
on: {manual: {}}
jobs:
  a_cursor:
    runs-on: picoclaw
    steps:
      - id: review
        uses: human/task
        with: {title: Continue, questions: [{id: continue}]}
  b_later:
    needs: [a_cursor]
    if: ${{ secrets.admit_later }}
    runs-on: picoclaw
    outputs:
      marker: ${{ steps.effect.outputs.marker }}
    steps:
      - id: effect
        uses: function/later-effect
`)
	executor := &Executor{WorkspaceDir: workspace, Store: store, Functions: registry}
	started, err := executor.Run(ctx, RunRequest{
		Workflow: workflow, WorkflowRef: "workflows/later-job-reclaim.yml",
	})
	if err != nil || started.Status != RunStatusWaiting {
		t.Fatalf("Run() result=%#v err=%v", started, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%#v err=%v", tasks, err)
	}
	task := tasks[0]
	request := HumanTaskResumeRequest{
		ExpectedRevision: task.Revision,
		InputHash:        task.InputHash,
		ResponseID:       "later-job-response",
		Response:         true,
		resumeLease:      time.Minute,
	}
	claimed, _, duplicate, err := store.ClaimHumanTask(ctx, started.RunID, task.ID, request)
	if err != nil || duplicate || claimed == nil || claimed.execution == nil || claimed.execution.Cursor == nil {
		t.Fatalf("ClaimHumanTask() run=%#v duplicate=%v err=%v", claimed, duplicate, err)
	}
	cursor := *claimed.execution.Cursor
	_, err = executor.executeWorkflow(
		ctx,
		store,
		claimed,
		claimed.execution.Workflow,
		RunRequest{
			Inputs:  cloneMap(claimed.Inputs),
			Event:   cloneMap(claimed.Event),
			Secrets: map[string]string{"admit_later": "yes"},
		},
		&cursor,
	)
	if err != nil {
		t.Fatalf("first continuation executeWorkflow() error = %v", err)
	}
	if effectCalls != 1 {
		t.Fatalf("later effect calls after first continuation = %d, want 1", effectCalls)
	}

	// Simulate a crash after job B's successful checkpoint but before the outer
	// ResumeHumanTask call can publish the terminal run update. The durable
	// resume cursor still names job A, while A and B are already complete.
	crashed, err := store.GetRun(ctx, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if crashed.Status != RunStatusRunning || crashed.execution == nil || crashed.execution.Cursor == nil ||
		crashed.execution.Cursor.JobID != "a_cursor" ||
		crashed.Jobs["a_cursor"].Status != RunStatusSucceeded ||
		crashed.Jobs["b_later"].Status != RunStatusSucceeded ||
		crashed.Jobs["b_later"].Outputs["marker"] != "original" {
		t.Fatalf("crash checkpoint = %#v execution=%#v", crashed, crashed.execution)
	}
	crashed.execution.Resume.ExpiresAt = time.Now().UTC().Add(-time.Second)
	overwritePersistedWorkflowExecutionForTest(t, workspace, crashed)

	// Do not re-supply admit_later. A stale claimant must use the complete B
	// checkpoint, not re-evaluate its job-level if or repeat its effect.
	restarted := &Executor{
		WorkspaceDir: workspace,
		Store:        NewFileRunStore(workspace),
		Functions:    registry,
	}
	resumed, err := restarted.ResumeHumanTask(ctx, started.RunID, task.ID, request)
	if err != nil || resumed == nil || resumed.Status != RunStatusSucceeded {
		t.Fatalf("stale replay result=%#v err=%v, want succeeded", resumed, err)
	}
	if effectCalls != 1 {
		t.Fatalf("later effect calls after stale replay = %d, want exactly 1", effectCalls)
	}
	persisted, err := restarted.Store.GetRun(ctx, started.RunID)
	if err != nil || persisted.Jobs["b_later"].Status != RunStatusSucceeded ||
		persisted.Jobs["b_later"].Outputs["marker"] != "original" {
		t.Fatalf("persisted later job=%#v err=%v", persisted.Jobs["b_later"], err)
	}
	events, err := store.Events(ctx, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	starts := map[string]int{}
	for _, event := range events {
		if event.Kind == "workflow.job.start" {
			starts[event.JobID]++
		}
	}
	if starts["a_cursor"] != 1 || starts["b_later"] != 1 {
		t.Fatalf("job.start counts = %#v, want one per job", starts)
	}
}

func TestHumanTaskRunIDSafeIDAliasesCannotAccessAnotherRun(t *testing.T) {
	tests := []struct {
		name    string
		runID   string
		alias   string
		attempt func(*Executor, string, WorkflowHumanTask) error
	}{
		{
			name:  "list slash alias",
			runID: "victim_list",
			alias: "victim/list",
			attempt: func(executor *Executor, alias string, _ WorkflowHumanTask) error {
				tasks, err := executor.ListHumanTasks(context.Background(), alias)
				if len(tasks) != 0 {
					return fmt.Errorf("alias disclosed tasks: %#v", tasks)
				}
				return err
			},
		},
		{
			name:  "claim backslash alias",
			runID: "victim_claim",
			alias: `victim\claim`,
			attempt: func(executor *Executor, alias string, task WorkflowHumanTask) error {
				_, err := executor.ResumeHumanTask(context.Background(), alias, task.ID, HumanTaskResumeRequest{
					ExpectedRevision: task.Revision,
					InputHash:        task.InputHash,
					ResponseID:       "alias-response",
					Response:         true,
				})
				return err
			},
		},
		{
			name:  "cancel dot alias",
			runID: "victim_cancel",
			alias: "victim..cancel",
			attempt: func(executor *Executor, alias string, task WorkflowHumanTask) error {
				_, err := executor.CancelHumanTask(context.Background(), alias, task.ID, "alias cancel")
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.alias == test.runID || safeID(test.alias) != safeID(test.runID) {
				t.Fatalf(
					"invalid collision fixture run=%q alias=%q paths=%q/%q",
					test.runID,
					test.alias,
					safeID(test.runID),
					safeID(test.alias),
				)
			}
			ctx := context.Background()
			workspace := t.TempDir()
			store := NewFileRunStore(workspace)
			executor := &Executor{WorkspaceDir: workspace, Store: store}
			workflow := parseWorkflow(t, `
name: Alias isolation
on: {manual: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: review
        uses: human/task
        with: {title: Continue, questions: [{id: continue}]}
`)
			started, err := executor.Run(ctx, RunRequest{
				RunID: test.runID, Workflow: workflow, WorkflowRef: "workflows/alias-isolation.yml",
			})
			if err != nil || started.Status != RunStatusWaiting {
				t.Fatalf("Run() result=%#v err=%v", started, err)
			}
			tasks, err := executor.ListHumanTasks(ctx, test.runID)
			if err != nil || len(tasks) != 1 {
				t.Fatalf("victim tasks=%#v err=%v", tasks, err)
			}
			original := tasks[0]
			attemptErr := test.attempt(executor, test.alias, original)
			if !errors.Is(attemptErr, ErrHumanTaskNotFound) {
				t.Fatalf("alias attempt error = %v, want not found", attemptErr)
			}
			tasks, err = executor.ListHumanTasks(ctx, test.runID)
			if err != nil || len(tasks) != 1 || tasks[0].Status != HumanTaskStatusWaiting ||
				tasks[0].Revision != original.Revision || tasks[0].ResponseID != "" {
				t.Fatalf("victim task mutated: %#v err=%v", tasks, err)
			}
			victim, err := store.GetRun(ctx, test.runID)
			if err != nil || victim.Status != RunStatusWaiting || victim.CancelRequestedAt != nil {
				t.Fatalf("victim run mutated: %#v err=%v", victim, err)
			}
		})
	}
}

func TestExecutorUniversalAdmissionRejectsReachableHumanTaskBeforeAnyRunEffect(t *testing.T) {
	const (
		parentRef = "workflows/parent.yml"
		childRef  = "workflows/child.yml"
	)
	parentSource := `
name: Parent with earlier effect
on: {manual: {}}
jobs:
  a_effect:
    runs-on: picoclaw
    steps:
      - uses: function/root-effect
  z_child:
    uses: workflows/child.yml
`
	benignChildSource := `
name: Initially validated child
on: {workflow_call: {}}
jobs:
  work:
    runs-on: picoclaw
    steps:
      - uses: function/benign-child
`
	humanChildSource := `
name: Human child
on: {workflow_call: {}}
jobs:
  review:
    runs-on: picoclaw
    steps:
      - uses: human/task
        with: {title: Review, questions: [{id: approve}]}
`
	partialRuntime := RuntimeCompatibility{PicoclawVersion: "universal-admission-test"}
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
		request func(*testing.T) RunRequest
	}{
		{
			name: "inline root and missing child compatibility stamp",
			prepare: func(t *testing.T, workspace string) {
				writeWorkflowFile(t, workspace, "child.yml", humanChildSource)
			},
			request: func(t *testing.T) RunRequest {
				return RunRequest{
					RunID:       "universal-inline-human-child",
					Workflow:    parseWorkflow(t, parentSource),
					WorkflowRef: "inline",
				}
			},
		},
		{
			name: "local root and stale child compatibility stamp",
			prepare: func(t *testing.T, workspace string) {
				writeWorkflowFile(t, workspace, "parent.yml", parentSource)
				writeWorkflowFile(t, workspace, "child.yml", benignChildSource)
				if _, err := RevalidateLocal(context.Background(), workspace, partialRuntime); err != nil {
					t.Fatalf("RevalidateLocal() error = %v", err)
				}
				writeWorkflowFile(t, workspace, "child.yml", humanChildSource)
			},
			request: func(*testing.T) RunRequest {
				return RunRequest{RunID: "universal-local-human-child", Ref: parentRef}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			test.prepare(t, workspace)
			childBytes, err := os.ReadFile(filepath.Join(workspace, childRef))
			if err != nil {
				t.Fatal(err)
			}
			if runnableErr := ensureWorkflowHashRunnable(
				workspace,
				childRef,
				NormalizeRuntimeCompatibility(partialRuntime),
				workflowHashBytes(childBytes),
			); runnableErr == nil {
				t.Fatal("child fixture unexpectedly has a current compatibility stamp")
			}
			store := NewFileRunStore(workspace)
			registry := NewFunctionRegistry()
			var effects int
			rootEffectFunction := func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
				effects++
				return map[string]any{"changed": true}, nil
			}
			if registerErr := registry.Register("root-effect", rootEffectFunction); registerErr != nil {
				t.Fatal(registerErr)
			}
			persistedCallback := false
			createdCallback := false
			request := test.request(t)
			request.OnRunPersisted = func(*Run) error {
				persistedCallback = true
				return nil
			}
			request.OnRunCreated = func(*Run) { createdCallback = true }
			result, err := (&Executor{
				WorkspaceDir:         workspace,
				Store:                store,
				Functions:            registry,
				RuntimeCompatibility: partialRuntime,
			}).Run(ctx, request)
			if result != nil || !errors.Is(err, ErrHumanTaskUnsupported) {
				t.Fatalf("Run() result=%#v err=%v, want structural human-task rejection", result, err)
			}
			if effects != 0 || persistedCallback || createdCallback {
				t.Fatalf(
					"pre-admission effects=%d persistedCallback=%v createdCallback=%v",
					effects,
					persistedCallback,
					createdCallback,
				)
			}
			runs, listErr := store.ListRuns(ctx)
			if listErr != nil || len(runs) != 0 {
				t.Fatalf("runs after rejection=%#v err=%v", runs, listErr)
			}
			events, eventErr := store.Events(ctx, request.RunID)
			if eventErr != nil || len(events) != 0 {
				t.Fatalf("events after rejection=%#v err=%v", events, eventErr)
			}
		})
	}
}

func TestHumanTaskStaleReclaimRehydratesFailedJobContinueOnError(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	registry := NewFunctionRegistry()
	boom := errors.New("durable effect failure")
	var effectCalls int
	failOnceFunction := func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		effectCalls++
		return map[string]any{"attempt": effectCalls}, boom
	}
	if err := registry.Register("fail-once", failOnceFunction); err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, `
name: Continue after durable failure
on: {manual: {}}
jobs:
  main:
    continue-on-error: true
    runs-on: picoclaw
    steps:
      - id: review
        uses: human/task
        with: {title: Continue, questions: [{id: continue}]}
      - id: effect
        uses: function/fail-once
`)
	executor := &Executor{WorkspaceDir: workspace, Store: store, Functions: registry}
	started, err := executor.Run(ctx, RunRequest{
		Workflow: workflow, WorkflowRef: "workflows/continue-error-reclaim.yml",
	})
	if err != nil || started.Status != RunStatusWaiting {
		t.Fatalf("Run() result=%#v err=%v", started, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%#v err=%v", tasks, err)
	}
	task := tasks[0]
	request := HumanTaskResumeRequest{
		ExpectedRevision: task.Revision,
		InputHash:        task.InputHash,
		ResponseID:       "continue-error-response",
		Response:         true,
		resumeLease:      time.Minute,
	}
	claimed, _, duplicate, err := store.ClaimHumanTask(ctx, started.RunID, task.ID, request)
	if err != nil || duplicate || claimed == nil || claimed.execution == nil {
		t.Fatalf("ClaimHumanTask() run=%#v duplicate=%v err=%v", claimed, duplicate, err)
	}
	job := claimed.execution.Workflow.Jobs["main"]
	stepContext := ExecutionContext{
		Inputs:       cloneMap(claimed.Inputs),
		Event:        cloneMap(claimed.Event),
		Steps:        map[string]StepExecution{"review": claimed.Steps["main/review"]},
		Needs:        map[string]JobExecution{},
		WorkspaceDir: workspace,
		WorkflowRef:  claimed.WorkflowRef,
		RunID:        claimed.ID,
	}
	failedStep, effectErr := executor.executeStep(
		ctx,
		store,
		claimed,
		"main",
		1,
		job.Steps[1],
		stepContext,
		map[string]JobExecution{},
	)
	if !errors.Is(effectErr, boom) || failedStep.Status != RunStatusFailed {
		t.Fatalf("executeStep() step=%#v err=%v", failedStep, effectErr)
	}
	claimed.Steps["main/effect"] = failedStep
	claimed.Jobs["main"] = JobExecution{
		ID: "main", Status: RunStatusRunning, Outputs: map[string]any{},
	}
	if updateErr := store.UpdateRun(ctx, claimed); updateErr != nil {
		t.Fatal(updateErr)
	}
	if effectCalls != 1 {
		t.Fatalf("effect calls before crash = %d, want 1", effectCalls)
	}
	expirePersistedHumanTaskClaim(t, workspace, store, started.RunID)

	restarted := &Executor{
		WorkspaceDir: workspace,
		Store:        NewFileRunStore(workspace),
		Functions:    registry,
	}
	resumed, err := restarted.ResumeHumanTask(ctx, started.RunID, task.ID, request)
	if err != nil || resumed == nil || resumed.Status != RunStatusSucceeded {
		t.Fatalf("stale replay result=%#v err=%v, want succeeded", resumed, err)
	}
	if effectCalls != 1 {
		t.Fatalf("effect calls after stale replay = %d, want exactly 1", effectCalls)
	}
	persisted, err := restarted.Store.GetRun(ctx, started.RunID)
	if err != nil || persisted.Status != RunStatusSucceeded ||
		persisted.Jobs["main"].Status != RunStatusSucceeded ||
		persisted.Steps["main/effect"].Status != RunStatusFailed ||
		!strings.Contains(persisted.Jobs["main"].Error, boom.Error()) {
		t.Fatalf("persisted continue-on-error run=%#v err=%v", persisted, err)
	}
}

func TestHumanTaskClaimLossCancelsContinuationAndFencesStaleWrites(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	baseStore := NewFileRunStore(workspace)
	registry := NewFunctionRegistry()
	var blockerCalls atomic.Int32
	var followingCalls atomic.Int32
	entered := make(chan struct{})
	canceled := make(chan struct{})
	claimAwareBlocker := func(
		stepCtx context.Context,
		_ map[string]any,
		_ ExecutionContext,
	) (map[string]any, error) {
		call := blockerCalls.Add(1)
		if call == 1 {
			close(entered)
			<-stepCtx.Done()
			close(canceled)
			return nil, stepCtx.Err()
		}
		return map[string]any{"recovered": true}, nil
	}
	if err := registry.Register("claim-aware-blocker", claimAwareBlocker); err != nil {
		t.Fatal(err)
	}
	afterClaimBlocker := func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		followingCalls.Add(1)
		return map[string]any{"ok": true}, nil
	}
	if err := registry.Register("after-claim-blocker", afterClaimBlocker); err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, `
name: Claim loss cancellation
on: {manual: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: review
        uses: human/task
        with: {title: Continue, questions: [{id: continue}]}
      - id: blocker
        uses: function/claim-aware-blocker
      - id: following
        uses: function/after-claim-blocker
`)
	executor := &Executor{WorkspaceDir: workspace, Store: baseStore, Functions: registry}
	started, err := executor.Run(ctx, RunRequest{
		Workflow: workflow, WorkflowRef: "workflows/claim-loss.yml",
	})
	if err != nil || started.Status != RunStatusWaiting {
		t.Fatalf("Run() result=%#v err=%v", started, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%#v err=%v", tasks, err)
	}
	task := tasks[0]
	request := HumanTaskResumeRequest{
		ExpectedRevision: task.Revision,
		InputHash:        task.InputHash,
		ResponseID:       "claim-loss-response",
		Response:         true,
		resumeLease:      time.Minute,
	}
	oldClaim, _, duplicate, err := baseStore.ClaimHumanTask(ctx, started.RunID, task.ID, request)
	if err != nil || duplicate || oldClaim == nil || oldClaim.execution == nil || oldClaim.execution.Resume == nil {
		t.Fatalf("old ClaimHumanTask() run=%#v duplicate=%v err=%v", oldClaim, duplicate, err)
	}
	oldToken := oldClaim.execution.Resume.Token
	continuationCtx, cancelContinuation := context.WithCancelCause(context.Background())
	defer cancelContinuation(nil)
	cursor := *oldClaim.execution.Cursor
	continuationDone := make(chan error, 1)
	go func() {
		_, runErr := executor.executeWorkflow(
			continuationCtx,
			baseStore,
			oldClaim,
			oldClaim.execution.Workflow,
			RunRequest{Inputs: cloneMap(oldClaim.Inputs), Event: cloneMap(oldClaim.Event)},
			&cursor,
		)
		continuationDone <- runErr
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("context-aware continuation did not start")
	}

	// Expiry itself fences the matching old token before another claimant wins.
	expirePersistedHumanTaskClaim(t, workspace, baseStore, started.RunID)
	expiredWrite := cloneRun(oldClaim)
	expiredWrite.Status = RunStatusFailed
	if updateErr := baseStore.UpdateRun(ctx, expiredWrite); !errors.Is(updateErr, ErrHumanTaskConflict) {
		t.Fatalf("matching expired-token UpdateRun() error = %v, want conflict", updateErr)
	}
	newClaim, _, duplicate, err := baseStore.ClaimHumanTask(ctx, started.RunID, task.ID, request)
	if err != nil || duplicate || newClaim == nil || newClaim.execution == nil ||
		newClaim.execution.Resume == nil || newClaim.execution.Resume.Token == oldToken {
		t.Fatalf("replacement claim=%#v duplicate=%v err=%v", newClaim, duplicate, err)
	}
	newToken := newClaim.execution.Resume.Token

	conflictingStore := &conflictingHumanTaskHeartbeatStore{FileRunStore: baseStore}
	stopHeartbeat := startHumanTaskClaimHeartbeat(
		conflictingStore,
		started.RunID,
		task.ID,
		oldToken,
		150*time.Millisecond,
		time.Now().UTC().Add(time.Second),
		cancelContinuation,
	)
	defer stopHeartbeat()
	select {
	case runErr := <-continuationDone:
		if !errors.Is(runErr, ErrHumanTaskConflict) {
			t.Fatalf("lost-claim continuation error = %v, want conflict", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lost-claim continuation was not canceled")
	}
	select {
	case <-canceled:
	default:
		t.Fatal("context-aware function did not observe cancellation")
	}
	if !errors.Is(context.Cause(continuationCtx), ErrHumanTaskConflict) ||
		conflictingStore.renewCalls.Load() == 0 {
		t.Fatalf(
			"continuation cause=%v renewCalls=%d",
			context.Cause(continuationCtx),
			conflictingStore.renewCalls.Load(),
		)
	}
	if followingCalls.Load() != 0 {
		t.Fatalf("following effect calls = %d, want 0", followingCalls.Load())
	}

	for _, status := range []string{RunStatusCanceled, RunStatusFailed, RunStatusSucceeded, RunStatusWaiting} {
		stale := cloneRun(oldClaim)
		stale.Status = status
		if updateErr := baseStore.UpdateRun(ctx, stale); !errors.Is(updateErr, ErrHumanTaskConflict) {
			t.Fatalf("stale old-token UpdateRun(%s) error = %v, want conflict", status, updateErr)
		}
	}
	owned, err := baseStore.GetRun(ctx, started.RunID)
	if err != nil || owned.Status != RunStatusRunning || owned.execution == nil ||
		owned.execution.Resume == nil || owned.execution.Resume.Token != newToken ||
		owned.Steps["main/blocker"].ID != "" {
		t.Fatalf("replacement-owned run=%#v execution=%#v err=%v", owned, owned.execution, err)
	}

	// Once the replacement claimant itself expires, an exact replay can recover
	// the untouched continuation. The blocker succeeds on its second call and
	// only that recovered owner reaches the following effect.
	expirePersistedHumanTaskClaim(t, workspace, baseStore, started.RunID)
	recoveredExecutor := &Executor{WorkspaceDir: workspace, Store: baseStore, Functions: registry}
	recovered, err := recoveredExecutor.ResumeHumanTask(ctx, started.RunID, task.ID, request)
	if err != nil || recovered == nil || recovered.Status != RunStatusSucceeded {
		t.Fatalf("recovered continuation result=%#v err=%v", recovered, err)
	}
	if blockerCalls.Load() != 2 || followingCalls.Load() != 1 {
		t.Fatalf("recovered calls blocker=%d following=%d, want 2/1", blockerCalls.Load(), followingCalls.Load())
	}
	terminalStale := cloneRun(oldClaim)
	terminalStale.Status = RunStatusSucceeded
	if updateErr := baseStore.UpdateRun(ctx, terminalStale); !errors.Is(updateErr, ErrHumanTaskConflict) {
		t.Fatalf("stale old-token UpdateRun after replacement terminal error = %v, want conflict", updateErr)
	}
	terminal, err := baseStore.GetRun(ctx, started.RunID)
	if err != nil || terminal.Status != RunStatusSucceeded ||
		terminal.execution == nil || terminal.execution.Resume == nil ||
		terminal.execution.Resume.Token == oldToken || followingCalls.Load() != 1 {
		t.Fatalf(
			"replacement terminal run=%#v execution=%#v following=%d err=%v",
			terminal,
			terminal.execution,
			followingCalls.Load(),
			err,
		)
	}
}

func expirePersistedHumanTaskClaim(
	t *testing.T,
	workspace string,
	store *FileRunStore,
	runID string,
) {
	t.Helper()
	run, err := store.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.execution == nil || run.execution.Resume == nil {
		t.Fatalf("run %s has no durable resume claim", runID)
	}
	run.execution.Resume.ExpiresAt = time.Now().UTC().Add(-time.Second)
	overwritePersistedWorkflowExecutionForTest(t, workspace, run)
}

func TestExecutorCapturedReusableSnapshotNormalizesPartialCompatibility(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "parent.yml", `
name: Captured parent
on: {manual: {}}
jobs:
  child:
    uses: workflows/child.yml
`)
	writeWorkflowFile(t, workspace, "child.yml", `
name: Captured child
on: {workflow_call: {}}
jobs:
  work:
    runs-on: picoclaw
    steps:
      - uses: function/captured-child-effect
`)
	partialRuntime := RuntimeCompatibility{PicoclawVersion: "partial-captured-runtime"}
	manifest, err := RevalidateLocal(ctx, workspace, partialRuntime)
	if err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}
	normalized := NormalizeRuntimeCompatibility(partialRuntime)
	if manifest.WorkflowEngine != normalized.WorkflowEngine ||
		manifest.WorkflowSchema != normalized.WorkflowSchema ||
		manifest.ValidatorFingerprint != normalized.ValidatorFingerprint {
		t.Fatalf("manifest runtime=%#v, want normalized %#v", manifest, normalized)
	}
	registry := NewFunctionRegistry()
	var calls int
	capturedChildEffect := func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	}
	if registerErr := registry.Register("captured-child-effect", capturedChildEffect); registerErr != nil {
		t.Fatal(registerErr)
	}
	executor := &Executor{
		WorkspaceDir:         workspace,
		Functions:            registry,
		RuntimeCompatibility: partialRuntime,
	}
	result, err := executor.Run(ctx, RunRequest{Ref: "workflows/parent.yml"})
	if err != nil || result == nil || result.Status != RunStatusSucceeded {
		t.Fatalf("Run() result=%#v err=%v, want succeeded", result, err)
	}
	if calls != 1 {
		t.Fatalf("captured child calls = %d, want 1", calls)
	}
	if len(executor.WorkflowSnapshots) != 0 {
		t.Fatal("universal admission mutated caller-owned executor snapshots")
	}
}

func TestHumanTaskProgrammaticTypedResponseSchemaIsNormalizedAndEnforced(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	typedProperties := map[string]map[string]any{
		"decision": {
			"type": "string",
			"enum": []string{"approve", "reject"},
		},
		"metadata": {
			"type":     "object",
			"required": []string{"count"},
			"properties": map[string]map[string]any{
				"count": {"type": "integer"},
			},
		},
	}
	typedSchema := map[string]any{
		"type":       "object",
		"required":   []string{"decision", "metadata"},
		"properties": typedProperties,
	}
	workflow := &Workflow{
		Name: "Typed programmatic schema",
		On:   WorkflowTriggers{Manual: map[string]any{}},
		Jobs: map[string]Job{
			"main": {
				RunsOn: "picoclaw",
				Steps: []Step{
					{
						ID: "review", Uses: "human/task",
						With: map[string]any{
							"title": "Typed review", "questions": []string{"Choose"},
							"response_schema": typedSchema,
						},
					},
					{
						ID: "capture", Uses: "function/capture-typed-response",
						With: map[string]any{"response": "${{ steps.review.outputs.response }}"},
					},
				},
			},
		},
	}
	if err := Validate(workflow); err != nil {
		t.Fatalf("Validate() typed schema error = %v", err)
	}
	registry := NewFunctionRegistry()
	var captured map[string]any
	captureTypedResponse := func(
		_ context.Context,
		args map[string]any,
		_ ExecutionContext,
	) (map[string]any, error) {
		captured = cloneMap(args)
		return cloneMap(args), nil
	}
	if err := registry.Register("capture-typed-response", captureTypedResponse); err != nil {
		t.Fatal(err)
	}
	executor := &Executor{WorkspaceDir: workspace, Store: NewFileRunStore(workspace), Functions: registry}
	started, err := executor.Run(ctx, RunRequest{
		Workflow: workflow, WorkflowRef: "workflows/typed-schema.yml",
	})
	if err != nil || started.Status != RunStatusWaiting {
		t.Fatalf("Run() result=%#v err=%v", started, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%#v err=%v", tasks, err)
	}
	task := tasks[0]
	properties, ok := task.ResponseSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf(
			"normalized properties = %#v (%T)",
			task.ResponseSchema["properties"],
			task.ResponseSchema["properties"],
		)
	}
	decision, ok := properties["decision"].(map[string]any)
	if !ok {
		t.Fatalf("normalized decision schema = %#v", properties["decision"])
	}
	if _, enumOK := decision["enum"].([]any); !enumOK {
		t.Fatalf("normalized enum = %#v (%T), want []any", decision["enum"], decision["enum"])
	}

	resume := func(responseID string, response any) error {
		_, resumeErr := executor.ResumeHumanTask(ctx, started.RunID, task.ID, HumanTaskResumeRequest{
			ExpectedRevision: task.Revision,
			InputHash:        task.InputHash,
			ResponseID:       responseID,
			Response:         response,
		})
		return resumeErr
	}
	if resumeErr := resume("invalid-enum", map[string]any{
		"decision": "later", "metadata": map[string]any{"count": json.Number("9007199254740993")},
	}); !errors.Is(resumeErr, ErrHumanTaskResponseInvalid) {
		t.Fatalf("nonmember enum response error = %v", resumeErr)
	}
	if resumeErr := resume("invalid-nested", map[string]any{
		"decision": "approve", "metadata": map[string]any{"count": "many"},
	}); !errors.Is(resumeErr, ErrHumanTaskResponseInvalid) {
		t.Fatalf("invalid nested response error = %v", resumeErr)
	}
	// Mutating the caller-owned typed schema cannot alter the durable normalized
	// task contract or the exact workflow snapshot used by continuation.
	typedProperties["decision"]["enum"] = []string{"later"}
	validResponse := map[string]any{
		"decision": "approve",
		"metadata": map[string]any{"count": json.Number("9007199254740993")},
	}
	resumed, err := executor.ResumeHumanTask(ctx, started.RunID, task.ID, HumanTaskResumeRequest{
		ExpectedRevision: task.Revision,
		InputHash:        task.InputHash,
		ResponseID:       "valid-typed-response",
		Response:         validResponse,
	})
	if err != nil || resumed == nil || resumed.Status != RunStatusSucceeded {
		t.Fatalf("valid typed response result=%#v err=%v", resumed, err)
	}
	response, ok := captured["response"].(map[string]any)
	if !ok {
		t.Fatalf("captured response = %#v", captured)
	}
	metadata, ok := response["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("captured metadata = %#v", response["metadata"])
	}
	assertHumanTaskExactJSONNumber(t, "captured typed count", metadata["count"], "9007199254740993")
}

func TestHumanTaskProgrammaticTypedSecretContainerRejectsBeforeRunEffects(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	registry := NewFunctionRegistry()
	var effects int
	typedSecretEarlierEffect := func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		effects++
		return map[string]any{"changed": true}, nil
	}
	if err := registry.Register("typed-secret-earlier-effect", typedSecretEarlierEffect); err != nil {
		t.Fatal(err)
	}
	workflow := &Workflow{
		Name: "Typed secret container",
		On:   WorkflowTriggers{Manual: map[string]any{}},
		Jobs: map[string]Job{
			"a_effect": {
				RunsOn: "picoclaw",
				Steps:  []Step{{Uses: "function/typed-secret-earlier-effect"}},
			},
			"z_review": {
				RunsOn: "picoclaw",
				Steps: []Step{{
					Uses: "human/task",
					With: map[string]any{
						"title":     "Review",
						"questions": []string{"Approve ${{ secrets.private_token }}?"},
					},
				}},
			},
		},
	}
	const runID = "typed-secret-container"
	result, err := (&Executor{
		WorkspaceDir: workspace,
		Store:        store,
		Functions:    registry,
	}).Run(ctx, RunRequest{
		RunID: runID, Workflow: workflow, WorkflowRef: "workflows/typed-secret.yml",
	})
	if result != nil || err == nil || !strings.Contains(err.Error(), "cannot reference secrets") {
		t.Fatalf("Run() result=%#v err=%v, want secret-reference rejection", result, err)
	}
	if effects != 0 {
		t.Fatalf("earlier effects = %d, want 0", effects)
	}
	runs, listErr := store.ListRuns(ctx)
	if listErr != nil || len(runs) != 0 {
		t.Fatalf("runs after typed-secret rejection=%#v err=%v", runs, listErr)
	}
	events, eventErr := store.Events(ctx, runID)
	if eventErr != nil || len(events) != 0 {
		t.Fatalf("events after typed-secret rejection=%#v err=%v", events, eventErr)
	}
}
