package tools

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestTrackedSpawnContextHelpersPreserveNilAndNoopCompatibility(t *testing.T) {
	ctx := WithTrackedSpawnAdmissionObserver(nil, nil)
	if ctx == nil {
		t.Fatal("nil context was not normalized")
	}
	if err := notifyTrackedSpawnAdmission(nil); err != nil {
		t.Fatalf("nil admission context error = %v", err)
	}
	if err := notifyTrackedSpawnAdmission(context.Background()); err != nil {
		t.Fatalf("empty admission context error = %v", err)
	}

	var observed atomic.Int64
	ctx = WithTrackedSpawnAdmissionObserver(nil, func() error {
		observed.Add(1)
		return nil
	})
	if err := notifyTrackedSpawnAdmission(ctx); err != nil || observed.Load() != 1 {
		t.Fatalf("admission notification = %v, calls = %d", err, observed.Load())
	}

	completionCtx := withSubagentCompletion(nil, SubagentCompletion{
		TaskID: "subagent-nil-context",
		Status: subagentTaskStatusCompleted,
	})
	if completion, ok := SubagentCompletionFromContext(completionCtx); !ok ||
		completion.TaskID != "subagent-nil-context" {
		t.Fatalf("nil-parent completion = %#v, found=%t", completion, ok)
	}
	trackedOnlyCtx := withTrackedSpawnCompletion(nil)
	if !IsTrackedSpawnCompletionCallback(trackedOnlyCtx) {
		t.Fatal("nil-parent tracked marker missing")
	}
	if completion, ok := TrackedSpawnCompletionFromContext(trackedOnlyCtx); ok {
		t.Fatalf("marker without manager metadata returned %#v", completion)
	}
	if IsTrackedSpawnCompletionCallback(nil) {
		t.Fatal("nil context reported tracked spawn provenance")
	}
}

func TestSpawnToolChildErrorStillCommitsIdentityAndFinalizes(t *testing.T) {
	manager := NewSubagentManager(&MockLLMProvider{}, "model", t.TempDir())
	wantErr := errors.New("child exploded")
	spawner := &trackedSpawnTestSpawner{err: wantErr}
	tool := NewSpawnTool(manager)
	tool.SetSpawner(spawner)

	type callbackObservation struct {
		completion SubagentCompletion
		found      bool
		result     *ToolResult
	}
	callback := make(chan callbackObservation, 1)
	ack := tool.ExecuteAsync(nil, map[string]any{"task": "surface the child error"}, func(
		ctx context.Context,
		result *ToolResult,
	) {
		completion, ok := TrackedSpawnCompletionFromContext(ctx)
		callback <- callbackObservation{completion: completion, found: ok, result: result}
	})
	if ack == nil || ack.IsError || !ack.Async {
		t.Fatalf("spawn acknowledgement = %#v", ack)
	}

	select {
	case observation := <-callback:
		if !observation.found || observation.completion.TaskID != "subagent-1" ||
			observation.completion.Status != subagentTaskStatusFailed {
			t.Fatalf("error completion = %#v, found=%t", observation.completion, observation.found)
		}
		if observation.result == nil || !observation.result.IsError ||
			!errors.Is(observation.result.Err, wantErr) ||
			!strings.Contains(observation.result.ForLLM, "child exploded") {
			t.Fatalf("error callback result = %#v", observation.result)
		}
	case <-time.After(time.Second):
		t.Fatal("child error callback did not run")
	}
	waitForTrackedTaskStatus(t, manager, "subagent-1", subagentTaskStatusFailed)
	deadline := time.Now().Add(time.Second)
	for spawner.finalized.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if spawner.finalized.Load() != 1 {
		t.Fatal("child error did not release prepared ownership")
	}
}

func TestSubagentManagerMissingTaskStillContainsFinalizerPanic(t *testing.T) {
	manager := NewSubagentManager(&MockLLMProvider{}, "model", t.TempDir())
	var finalized atomic.Bool
	manager.runTask(
		context.Background(),
		"missing-task",
		func(context.Context, SubagentTask) (*ToolResult, error) {
			t.Fatal("missing task reached runner")
			return nil, nil
		},
		nil,
		func() {
			finalized.Store(true)
			panic("finalizer panic")
		},
	)
	if !finalized.Load() {
		t.Fatal("missing-task finalizer did not run")
	}
}

func TestSubagentManagerLegacyFallbackPreservesDirectSpawnCompatibility(t *testing.T) {
	provider := &MockLLMProvider{}
	manager := NewSubagentManager(provider, "test-model", t.TempDir())
	manager.SetLLMOptions(321, 0.4)
	manager.SetMediaResolver(func(messages []providers.Message) []providers.Message {
		return append([]providers.Message(nil), messages...)
	})

	ack, err := manager.Spawn(
		nil,
		"legacy fallback task",
		"fallback",
		"",
		"cli",
		"direct",
		nil,
	)
	if err != nil || !strings.Contains(ack, "task_id=subagent-1") {
		t.Fatalf("legacy fallback acknowledgement = %q, %v", ack, err)
	}
	task := waitForTrackedTaskStatus(t, manager, "subagent-1", subagentTaskStatusCompleted)
	if !strings.Contains(task.Result, "Task completed: legacy fallback task") {
		t.Fatalf("legacy fallback result = %q", task.Result)
	}
	if provider.lastOptions["max_tokens"] != 321 || provider.lastOptions["temperature"] != 0.4 {
		t.Fatalf("legacy fallback options = %#v", provider.lastOptions)
	}
}
