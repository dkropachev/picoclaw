package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type trackedSpawnTestSpawner struct {
	prepareErr error
	result     *ToolResult
	err        error

	prepared   atomic.Int64
	finalized  atomic.Int64
	runCalls   atomic.Int64
	runEntered chan struct{}
	releaseRun chan struct{}

	mu     sync.Mutex
	config SubTurnConfig
}

func (spawner *trackedSpawnTestSpawner) PrepareAsyncSubTurn(
	ctx context.Context,
) (context.Context, func(), error) {
	spawner.prepared.Add(1)
	if spawner.prepareErr != nil {
		return ctx, func() {}, spawner.prepareErr
	}
	var once sync.Once
	return ctx, func() {
		once.Do(func() { spawner.finalized.Add(1) })
	}, nil
}

func (spawner *trackedSpawnTestSpawner) SpawnSubTurn(
	ctx context.Context,
	cfg SubTurnConfig,
) (*ToolResult, error) {
	spawner.runCalls.Add(1)
	spawner.mu.Lock()
	spawner.config = cfg
	spawner.mu.Unlock()
	if spawner.runEntered != nil {
		close(spawner.runEntered)
	}
	if spawner.releaseRun != nil {
		select {
		case <-spawner.releaseRun:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return spawner.result, spawner.err
}

func (spawner *trackedSpawnTestSpawner) configSnapshot() SubTurnConfig {
	spawner.mu.Lock()
	defer spawner.mu.Unlock()
	return spawner.config
}

func waitForTrackedTaskStatus(
	t *testing.T,
	manager *SubagentManager,
	taskID, status string,
) SubagentTask {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if task, ok := manager.GetTaskCopy(taskID); ok && task.Status == status {
			return task
		}
		time.Sleep(time.Millisecond)
	}
	task, _ := manager.GetTaskCopy(taskID)
	t.Fatalf("task %q status = %q, want %q", taskID, task.Status, status)
	return SubagentTask{}
}

func TestSubagentCompletionFromContextReturnsDetachedValue(t *testing.T) {
	if _, ok := SubagentCompletionFromContext(nil); ok {
		t.Fatal("nil context unexpectedly carried a subagent completion")
	}
	if _, ok := SubagentCompletionFromContext(context.Background()); ok {
		t.Fatal("background context unexpectedly carried a subagent completion")
	}

	ctx := withSubagentCompletion(context.Background(), SubagentCompletion{
		TaskID: "subagent-7",
		Status: subagentTaskStatusCompleted,
	})
	first, ok := SubagentCompletionFromContext(ctx)
	if !ok {
		t.Fatal("completion context value missing")
	}
	first.TaskID = "mutated"
	first.Status = "mutated"
	if first.TaskID != "mutated" || first.Status != "mutated" {
		t.Fatalf("local completion mutation did not apply: %#v", first)
	}
	second, ok := SubagentCompletionFromContext(ctx)
	if !ok || second.TaskID != "subagent-7" || second.Status != subagentTaskStatusCompleted {
		t.Fatalf("detached completion = %#v, %t", second, ok)
	}
}

type forgedSubagentCompletionContext struct{ context.Context }

func (forgedSubagentCompletionContext) Value(any) any {
	return SubagentCompletion{TaskID: "forged", Status: subagentTaskStatusCompleted}
}

func TestSubagentCompletionFromContextRejectsForgedExportedValue(t *testing.T) {
	ctx := forgedSubagentCompletionContext{Context: context.Background()}
	if completion, ok := SubagentCompletionFromContext(ctx); ok {
		t.Fatalf("forged completion accepted: %#v", completion)
	}
}

func TestSpawnToolTrackedLifecycleIsImmediateAndSnapshotBound(t *testing.T) {
	manager := NewSubagentManager(&MockLLMProvider{}, "model-a", t.TempDir())
	manager.SetDefaultModelFallbacks([]string{"fallback-a"})
	manager.SetLLMOptions(1234, 0.25)
	spawner := &trackedSpawnTestSpawner{
		result:     NewToolResult("tracked completion"),
		runEntered: make(chan struct{}),
		releaseRun: make(chan struct{}),
	}
	tool := NewSpawnTool(manager)
	tool.SetSpawner(spawner)
	ctx := WithToolContext(context.Background(), "telegram", "chat-a")
	ctx = WithToolSessionContext(ctx, "source-agent", "session-a", nil)
	args := map[string]any{
		"task": "tracked task", "label": "tracked-label", "agent_id": "target-agent",
	}
	callbackStatus := make(chan string, 1)
	callbackResult := make(chan *ToolResult, 1)
	ack := tool.ExecuteAsync(ctx, args, func(_ context.Context, result *ToolResult) {
		if spawner.finalized.Load() != 0 {
			t.Error("prepared ownership released before callback")
		}
		task, _ := manager.GetTaskCopy("subagent-1")
		callbackStatus <- task.Status
		callbackResult <- result
	})
	if ack == nil || ack.IsError || !ack.Async ||
		!strings.Contains(ack.ForLLM, "Spawned subagent 'tracked-label' for task: tracked task") ||
		!strings.Contains(ack.ForLLM, "task_id=subagent-1") {
		t.Fatalf("tracked acknowledgement = %#v", ack)
	}
	select {
	case <-spawner.runEntered:
	case <-time.After(time.Second):
		t.Fatal("tracked runner did not start")
	}
	running := waitForTrackedTaskStatus(t, manager, "subagent-1", subagentTaskStatusRunning)
	if running.Task != "tracked task" || running.Label != "tracked-label" ||
		running.AgentID != "target-agent" || running.OriginAgentID != "source-agent" ||
		running.OriginSessionKey != "session-a" || running.OriginChannel != "telegram" ||
		running.OriginChatID != "chat-a" || running.Created == 0 {
		t.Fatalf("running tracked record = %#v", running)
	}
	status := NewSpawnStatusTool(manager).Execute(ctx, map[string]any{"task_id": "subagent-1"})
	if status.IsError || !strings.Contains(status.ForLLM, "status=running") {
		t.Fatalf("immediate tracked status = %#v", status)
	}

	// Mutations after acknowledgement cannot rewrite the detached launch.
	args["task"] = "mutated task"
	tool.defaultModel = "model-b"
	tool.defaultModelFallbacks[0] = "fallback-b"
	tool.maxTokens = 9
	tool.temperature = 0.9
	close(spawner.releaseRun)
	select {
	case result := <-callbackResult:
		if result == nil || result.IsError || result.ForLLM != "tracked completion" {
			t.Fatalf("tracked callback result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("tracked callback did not run")
	}
	if got := <-callbackStatus; got != subagentTaskStatusCompleted {
		t.Fatalf("callback-observed status = %q", got)
	}
	completed := waitForTrackedTaskStatus(t, manager, "subagent-1", subagentTaskStatusCompleted)
	if completed.Created != running.Created || completed.Result != "tracked completion" {
		t.Fatalf("completed tracked record = %#v", completed)
	}
	deadline := time.Now().Add(time.Second)
	for spawner.finalized.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if spawner.prepared.Load() != 1 || spawner.finalized.Load() != 1 {
		t.Fatalf("prepare/finalize calls = %d/%d",
			spawner.prepared.Load(), spawner.finalized.Load())
	}
	config := spawner.configSnapshot()
	if config.Model != "model-a" || len(config.ModelFallbacks) != 1 ||
		config.ModelFallbacks[0] != "fallback-a" || config.MaxTokens != 1234 ||
		config.Temperature != 0.25 || config.Async || !config.Critical ||
		config.Tools != nil || config.TargetAgentID != "target-agent" ||
		!strings.Contains(config.SystemPrompt, "tracked task") {
		t.Fatalf("detached tracked config = %#v", config)
	}
}

func TestSpawnToolFastCompletionCallbackCarriesCommittedIdentity(t *testing.T) {
	manager := NewSubagentManager(&MockLLMProvider{}, "model", t.TempDir())
	spawner := &trackedSpawnTestSpawner{result: NewToolResult("fast completion")}
	tool := NewSpawnTool(manager)
	tool.SetSpawner(spawner)
	type callbackObservation struct {
		completion SubagentCompletion
		found      bool
		result     *ToolResult
	}
	observed := make(chan callbackObservation, 1)
	ack := tool.ExecuteAsync(
		context.Background(),
		map[string]any{"task": "finish immediately"},
		func(ctx context.Context, result *ToolResult) {
			completion, ok := TrackedSpawnCompletionFromContext(ctx)
			observed <- callbackObservation{completion: completion, found: ok, result: result}
		},
	)
	if ack == nil || ack.IsError || !ack.Async {
		t.Fatalf("fast acknowledgement = %#v", ack)
	}
	select {
	case observation := <-observed:
		if !observation.found || observation.completion.TaskID != "subagent-1" ||
			observation.completion.Status != subagentTaskStatusCompleted {
			t.Fatalf("fast callback completion = %#v, found=%t",
				observation.completion, observation.found)
		}
		if observation.result == nil || observation.result.ForLLM != "fast completion" {
			t.Fatalf("fast callback result = %#v", observation.result)
		}
	case <-time.After(time.Second):
		t.Fatal("fast completion callback did not run")
	}
}

func TestSpawnToolPreparationFailureCreatesNoTrackedRecord(t *testing.T) {
	manager := NewSubagentManager(&MockLLMProvider{}, "model", t.TempDir())
	spawner := &trackedSpawnTestSpawner{prepareErr: errors.New("prepare failed")}
	tool := NewSpawnTool(manager)
	tool.SetSpawner(spawner)
	result := tool.ExecuteAsync(
		context.Background(),
		map[string]any{"task": "never launch"},
		func(context.Context, *ToolResult) { t.Error("unexpected callback") },
	)
	if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "prepare failed") {
		t.Fatalf("preparation failure = %#v", result)
	}
	if spawner.runCalls.Load() != 0 || len(manager.ListTaskCopies()) != 0 {
		t.Fatalf("preparation effects = runs:%d tasks:%#v",
			spawner.runCalls.Load(), manager.ListTaskCopies())
	}
}

func TestSpawnToolAdmissionObserverFailureReleasesWithoutTask(t *testing.T) {
	manager := NewSubagentManager(&MockLLMProvider{}, "model", t.TempDir())
	spawner := &trackedSpawnTestSpawner{result: NewToolResult("must not run")}
	tool := NewSpawnTool(manager)
	tool.SetSpawner(spawner)
	var observed atomic.Int64
	ctx := WithTrackedSpawnAdmissionObserver(context.Background(), func() error {
		observed.Add(1)
		if len(manager.ListTaskCopies()) != 0 {
			t.Error("observer ran after manager task insertion")
		}
		return errors.New("route rejected")
	})
	result := tool.ExecuteAsync(ctx, map[string]any{"task": "reject route"}, func(
		context.Context,
		*ToolResult,
	) {
		t.Error("unexpected callback")
	})
	if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "route rejected") {
		t.Fatalf("observer rejection = %#v", result)
	}
	if observed.Load() != 1 || spawner.prepared.Load() != 1 ||
		spawner.finalized.Load() != 1 || spawner.runCalls.Load() != 0 ||
		len(manager.ListTaskCopies()) != 0 {
		t.Fatalf(
			"observer rejection effects = observed:%d prepared:%d finalized:%d runs:%d tasks:%d",
			observed.Load(),
			spawner.prepared.Load(),
			spawner.finalized.Load(),
			spawner.runCalls.Load(),
			len(manager.ListTaskCopies()),
		)
	}
}

func TestSpawnToolAdmissionObserverPrecedesManagerLaunch(t *testing.T) {
	manager := NewSubagentManager(&MockLLMProvider{}, "model", t.TempDir())
	spawner := &trackedSpawnTestSpawner{
		result:     NewToolResult("observed launch"),
		runEntered: make(chan struct{}),
	}
	tool := NewSpawnTool(manager)
	tool.SetSpawner(spawner)
	var observed atomic.Bool
	ctx := WithTrackedSpawnAdmissionObserver(context.Background(), func() error {
		if !observed.CompareAndSwap(false, true) {
			t.Error("observer ran more than once")
		}
		if len(manager.ListTaskCopies()) != 0 {
			t.Error("observer ran after manager task insertion")
		}
		return nil
	})
	callback := make(chan struct{}, 1)
	ack := tool.ExecuteAsync(ctx, map[string]any{"task": "observe route"}, func(
		ctx context.Context,
		_ *ToolResult,
	) {
		if _, ok := TrackedSpawnCompletionFromContext(ctx); !ok {
			t.Error("first-party callback lost provenance")
		}
		callback <- struct{}{}
	})
	if ack == nil || ack.IsError || !ack.Async || !observed.Load() {
		t.Fatalf("observed acknowledgement = %#v, observed=%v", ack, observed.Load())
	}
	select {
	case <-spawner.runEntered:
	case <-time.After(time.Second):
		t.Fatal("manager runner did not launch")
	}
	select {
	case <-callback:
	case <-time.After(time.Second):
		t.Fatal("manager callback did not finish")
	}
}

func TestSpawnToolAdmissionObserverPanicReleasesWithoutTask(t *testing.T) {
	manager := NewSubagentManager(&MockLLMProvider{}, "model", t.TempDir())
	spawner := &trackedSpawnTestSpawner{result: NewToolResult("must not run")}
	tool := NewSpawnTool(manager)
	tool.SetSpawner(spawner)
	ctx := WithTrackedSpawnAdmissionObserver(context.Background(), func() error {
		panic("observer panic")
	})
	result := tool.ExecuteAsync(ctx, map[string]any{"task": "panic route"}, func(
		context.Context,
		*ToolResult,
	) {
		t.Error("unexpected callback")
	})
	if result == nil || !result.IsError ||
		!strings.Contains(result.ForLLM, "admission observer panicked") {
		t.Fatalf("observer panic result = %#v", result)
	}
	if spawner.prepared.Load() != 1 || spawner.finalized.Load() != 1 ||
		spawner.runCalls.Load() != 0 || len(manager.ListTaskCopies()) != 0 {
		t.Fatalf(
			"observer panic effects = prepared:%d finalized:%d runs:%d tasks:%d",
			spawner.prepared.Load(),
			spawner.finalized.Load(),
			spawner.runCalls.Load(),
			len(manager.ListTaskCopies()),
		)
	}
}

func TestSubagentManagerTrackedTerminalOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		ctx        func() context.Context
		runner     subagentTaskRunner
		wantStatus string
		wantResult string
		wantRuns   int64
	}{
		{
			name: "completed", ctx: context.Background,
			runner: func(context.Context, SubagentTask) (*ToolResult, error) {
				return NewToolResult("done"), nil
			},
			wantStatus: subagentTaskStatusCompleted, wantResult: "done", wantRuns: 1,
		},
		{
			name: "failed", ctx: context.Background,
			runner: func(context.Context, SubagentTask) (*ToolResult, error) {
				return nil, errors.New("ordinary failure")
			},
			wantStatus: subagentTaskStatusFailed, wantResult: "ordinary failure", wantRuns: 1,
		},
		{
			name: "wrapped canceled", ctx: context.Background,
			runner: func(context.Context, SubagentTask) (*ToolResult, error) {
				return nil, fmt.Errorf("child stopped: %w", context.Canceled)
			},
			wantStatus: subagentTaskStatusCanceled, wantResult: "canceled", wantRuns: 1,
		},
		{
			name: "deadline", ctx: context.Background,
			runner: func(context.Context, SubagentTask) (*ToolResult, error) {
				return nil, context.DeadlineExceeded
			},
			wantStatus: subagentTaskStatusCanceled, wantResult: "canceled", wantRuns: 1,
		},
		{
			name: "panic", ctx: context.Background,
			runner: func(context.Context, SubagentTask) (*ToolResult, error) {
				panic("runner panic")
			},
			wantStatus: subagentTaskStatusFailed, wantResult: "panicked", wantRuns: 1,
		},
		{
			name: "nil result", ctx: context.Background,
			runner:     func(context.Context, SubagentTask) (*ToolResult, error) { return nil, nil },
			wantStatus: subagentTaskStatusFailed, wantResult: "nil result", wantRuns: 1,
		},
		{
			name: "error result", ctx: context.Background,
			runner: func(context.Context, SubagentTask) (*ToolResult, error) {
				return ErrorResult("reported failure"), nil
			},
			wantStatus: subagentTaskStatusFailed, wantResult: "reported failure", wantRuns: 1,
		},
		{
			name: "pre canceled",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			runner: func(context.Context, SubagentTask) (*ToolResult, error) {
				t.Fatal("pre-canceled task reached runner")
				return nil, nil
			},
			wantStatus: subagentTaskStatusCanceled, wantResult: "before execution",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			manager := NewSubagentManager(&MockLLMProvider{}, "model", t.TempDir())
			var runs atomic.Int64
			runner := func(ctx context.Context, task SubagentTask) (*ToolResult, error) {
				runs.Add(1)
				return testCase.runner(ctx, task)
			}
			type callbackObservation struct {
				completion SubagentCompletion
				found      bool
				result     *ToolResult
			}
			callback := make(chan callbackObservation, 1)
			finalized := make(chan struct{})
			taskID, err := manager.spawnTracked(
				testCase.ctx(), "task", "label", "", subagentTaskOrigin{}, runner,
				func(ctx context.Context, result *ToolResult) {
					task, _ := manager.GetTaskCopy("subagent-1")
					if task.Status == subagentTaskStatusRunning {
						t.Error("callback ran before terminal commit")
					}
					completion, ok := SubagentCompletionFromContext(ctx)
					if _, tracked := TrackedSpawnCompletionFromContext(ctx); tracked {
						t.Error("direct manager callback carried first-party spawn provenance")
					}
					callback <- callbackObservation{
						completion: completion,
						found:      ok,
						result:     result,
					}
				},
				func() { close(finalized) },
			)
			if err != nil || taskID != "subagent-1" {
				t.Fatalf("spawnTracked() = %q, %v", taskID, err)
			}
			select {
			case observation := <-callback:
				if !observation.found || observation.completion.TaskID != taskID ||
					observation.completion.Status != testCase.wantStatus {
					t.Fatalf("terminal callback completion = %#v, found=%t",
						observation.completion, observation.found)
				}
				if observation.result == nil ||
					(testCase.wantStatus != subagentTaskStatusCompleted && !observation.result.IsError) {
					t.Fatalf("terminal callback result = %#v", observation.result)
				}
			case <-time.After(time.Second):
				t.Fatal("terminal callback did not run")
			}
			select {
			case <-finalized:
			case <-time.After(time.Second):
				t.Fatal("terminal finalizer did not run")
			}
			task := waitForTrackedTaskStatus(t, manager, taskID, testCase.wantStatus)
			if !strings.Contains(task.Result, testCase.wantResult) || runs.Load() != testCase.wantRuns {
				t.Fatalf("terminal task = %#v, runs=%d", task, runs.Load())
			}
		})
	}
}

func TestSubagentManagerOnlyWinningTerminalCommitInvokesCallback(t *testing.T) {
	manager := NewSubagentManager(&MockLLMProvider{}, "model", t.TempDir())
	manager.mu.Lock()
	manager.tasks["subagent-1"] = &SubagentTask{
		ID:     "subagent-1",
		Task:   "competing completion",
		Status: subagentTaskStatusRunning,
	}
	manager.mu.Unlock()

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	callbacks := make(chan struct {
		completion SubagentCompletion
		result     *ToolResult
	}, 2)
	finalized := make(chan struct{}, 2)
	run := func(content string) {
		manager.runTask(
			context.Background(),
			"subagent-1",
			func(context.Context, SubagentTask) (*ToolResult, error) {
				entered <- struct{}{}
				<-release
				return NewToolResult(content), nil
			},
			func(ctx context.Context, result *ToolResult) {
				completion, ok := SubagentCompletionFromContext(ctx)
				if !ok {
					t.Error("winning callback missing completion identity")
				}
				callbacks <- struct {
					completion SubagentCompletion
					result     *ToolResult
				}{completion: completion, result: result}
			},
			func() { finalized <- struct{}{} },
		)
	}
	go run("first")
	go run("second")
	<-entered
	<-entered
	close(release)
	<-finalized
	<-finalized

	var callback struct {
		completion SubagentCompletion
		result     *ToolResult
	}
	select {
	case callback = <-callbacks:
	default:
		t.Fatal("winning terminal transition did not invoke callback")
	}
	select {
	case duplicate := <-callbacks:
		t.Fatalf("losing terminal transition invoked callback: %#v", duplicate)
	default:
	}
	task, ok := manager.GetTaskCopy("subagent-1")
	if !ok || callback.result == nil || callback.completion.TaskID != task.ID ||
		callback.completion.Status != task.Status || callback.result.ForLLM != task.Result {
		t.Fatalf("winning callback/task mismatch: callback=%#v task=%#v",
			callback, task)
	}
}

func TestSubagentManagerConcurrentIDsAndSnapshotsAreDetached(t *testing.T) {
	manager := NewSubagentManager(&MockLLMProvider{}, "model", t.TempDir())
	const count = 100
	release := make(chan struct{})
	runner := func(context.Context, SubagentTask) (*ToolResult, error) {
		<-release
		return NewToolResult("done"), nil
	}
	ids := make(chan string, count)
	errs := make(chan error, count)
	var callers sync.WaitGroup
	for index := 0; index < count; index++ {
		callers.Add(1)
		go func(index int) {
			defer callers.Done()
			id, err := manager.spawnTracked(
				context.Background(), fmt.Sprintf("task-%d", index), "", "",
				subagentTaskOrigin{}, runner, nil, nil,
			)
			ids <- id
			errs <- err
		}(index)
	}
	callers.Wait()
	close(ids)
	close(errs)
	seen := make(map[string]struct{}, count)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for id := range ids {
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate tracked ID %q", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != count || len(manager.ListTaskCopies()) != count {
		t.Fatalf("tracked IDs/tasks = %d/%d", len(seen), len(manager.ListTaskCopies()))
	}
	first, ok := manager.GetTask("subagent-1")
	if !ok {
		t.Fatal("first tracked task missing")
	}
	first.Status = "corrupted"
	list := manager.ListTasks()
	listTaskID := list[0].ID
	list[0].Task = "corrupted"
	actual, _ := manager.GetTaskCopy("subagent-1")
	listActual, _ := manager.GetTaskCopy(listTaskID)
	if actual.Status != subagentTaskStatusRunning || listActual.Task == "corrupted" {
		t.Fatalf("external task pointers mutated manager state: %#v", actual)
	}
	close(release)
	for id := range seen {
		waitForTrackedTaskStatus(t, manager, id, subagentTaskStatusCompleted)
	}
}

func TestSubagentManagerCallbackPanicStillFinalizes(t *testing.T) {
	manager := NewSubagentManager(&MockLLMProvider{}, "model", t.TempDir())
	finalized := make(chan struct{})
	_, err := manager.spawnTracked(
		context.Background(), "task", "", "", subagentTaskOrigin{},
		func(context.Context, SubagentTask) (*ToolResult, error) {
			return NewToolResult("done"), nil
		},
		func(context.Context, *ToolResult) { panic("callback panic") },
		func() { close(finalized) },
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-finalized:
	case <-time.After(time.Second):
		t.Fatal("callback panic skipped finalizer")
	}
	waitForTrackedTaskStatus(t, manager, "subagent-1", subagentTaskStatusCompleted)
}

func TestSubagentManagerSpawnCompatibilityIncludesTaskID(t *testing.T) {
	manager := NewSubagentManager(&MockLLMProvider{}, "model", t.TempDir())
	manager.SetSpawner(func(
		context.Context,
		string,
		string,
		string,
		*ToolRegistry,
		int,
		float64,
		bool,
		bool,
	) (*ToolResult, error) {
		return NewToolResult("legacy done"), nil
	})
	ctx := WithToolSessionContext(context.Background(), "legacy-agent", "legacy-session", nil)
	ack, err := manager.Spawn(ctx, "legacy task", "legacy", "", "cli", "direct", nil)
	if err != nil || !strings.Contains(ack, "task_id=subagent-1") {
		t.Fatalf("legacy manager acknowledgement = %q, %v", ack, err)
	}
	task := waitForTrackedTaskStatus(t, manager, "subagent-1", subagentTaskStatusCompleted)
	if task.OriginAgentID != "legacy-agent" || task.OriginSessionKey != "legacy-session" ||
		task.OriginChannel != "cli" || task.OriginChatID != "direct" {
		t.Fatalf("legacy manager origin = %#v", task)
	}
}

func TestSubagentManagerZeroValueTracksWithoutPanicking(t *testing.T) {
	manager := &SubagentManager{}
	taskID, err := manager.spawnTracked(
		context.Background(), "zero value", "", "", subagentTaskOrigin{},
		func(context.Context, SubagentTask) (*ToolResult, error) {
			return NewToolResult("done"), nil
		},
		nil,
		nil,
	)
	if err != nil || taskID != "subagent-1" {
		t.Fatalf("zero-value spawn = %q, %v", taskID, err)
	}
	waitForTrackedTaskStatus(t, manager, taskID, subagentTaskStatusCompleted)
}

func TestSubagentManagerSpawnSnapshotsLegacyRunnerConfiguration(t *testing.T) {
	manager := NewSubagentManager(&MockLLMProvider{}, "model", t.TempDir())
	manager.SetLLMOptions(100, 0.1)
	entered := make(chan struct{})
	release := make(chan struct{})
	var firstCalls atomic.Int64
	var secondCalls atomic.Int64
	options := make(chan string, 1)
	manager.SetSpawner(func(
		_ context.Context,
		_, _, _ string,
		_ *ToolRegistry,
		maxTokens int,
		temperature float64,
		hasMaxTokens, hasTemperature bool,
	) (*ToolResult, error) {
		firstCalls.Add(1)
		close(entered)
		<-release
		options <- fmt.Sprintf(
			"%d/%.1f/%t/%t",
			maxTokens,
			temperature,
			hasMaxTokens,
			hasTemperature,
		)
		return NewToolResult("first"), nil
	})
	if _, err := manager.Spawn(
		context.Background(), "snapshot", "", "", "", "", nil,
	); err != nil {
		t.Fatal(err)
	}
	manager.SetLLMOptions(200, 0.9)
	manager.SetSpawner(func(
		context.Context,
		string,
		string,
		string,
		*ToolRegistry,
		int,
		float64,
		bool,
		bool,
	) (*ToolResult, error) {
		secondCalls.Add(1)
		return NewToolResult("second"), nil
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("snapshotted legacy runner did not start")
	}
	close(release)
	waitForTrackedTaskStatus(t, manager, "subagent-1", subagentTaskStatusCompleted)
	if got := <-options; got != "100/0.1/true/true" ||
		firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatalf("legacy snapshot = %q calls:%d/%d",
			got, firstCalls.Load(), secondCalls.Load())
	}
}
