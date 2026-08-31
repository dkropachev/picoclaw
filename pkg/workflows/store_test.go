package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileRunStoreUpdatesRemainReadableDuringAtomicReplacement(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	run := &Run{
		ID:          "wr_atomic_updates",
		WorkflowRef: "workflows/test.yml",
		Status:      RunStatusRunning,
		CreatedAt:   time.Now().UTC(),
		Outputs: map[string]any{
			"payload": strings.Repeat("a", 256*1024),
		},
	}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	stopReaders := make(chan struct{})
	readerErr := make(chan error, 1)
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stopReaders:
					return
				default:
					observed, err := store.GetRun(ctx, run.ID)
					if err != nil {
						select {
						case readerErr <- fmt.Errorf("read run during update: %w", err):
						default:
						}
						return
					}
					if observed.ID != run.ID {
						select {
						case readerErr <- fmt.Errorf("observed run id %q, want %q", observed.ID, run.ID):
						default:
						}
						return
					}
				}
			}
		}()
	}

	for i := range 96 {
		run.Outputs = map[string]any{
			"iteration": i,
			"payload":   strings.Repeat(string(rune('a'+i%26)), 256*1024),
		}
		if err := store.UpdateRun(ctx, run); err != nil {
			close(stopReaders)
			readers.Wait()
			t.Fatalf("UpdateRun(%d) error = %v", i, err)
		}
	}
	close(stopReaders)
	readers.Wait()
	select {
	case err := <-readerErr:
		t.Fatal(err)
	default:
	}

	persisted, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if got := persisted.Outputs["iteration"]; got != float64(95) {
		t.Fatalf("final iteration = %#v, want 95", got)
	}
}

func TestFileRunStorePreservesJSONNumbersAcrossReadsAndUpdates(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	const exact = "9007199254740993"
	event := map[string]any{
		"id":        "ev_0123456789abcdef0123456789abcdef",
		"source":    "webhook",
		"connector": "primary",
		"type":      "test.number",
		"payload":   map[string]any{"count": json.Number(exact)},
	}
	run := &Run{
		ID:          "wr_exact_number",
		WorkflowRef: "workflows/test.yml",
		Status:      RunStatusRunning,
		Event:       event,
		Inputs: map[string]any{
			"event_id":       event["id"],
			"dispatch_id":    "dsp_0123456789abcdef0123456789abcdef",
			"event":          event,
			"ordinary_count": float64(7),
		},
		Session:   EventWorkflowSession("workflows/test.yml", event["id"].(string)),
		CreatedAt: time.Now().UTC(),
	}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	assertExact := func(label string, value any) {
		t.Helper()
		number, ok := value.(json.Number)
		if !ok || number.String() != exact {
			t.Fatalf("%s = %#v (%T), want json.Number(%s)", label, value, value, exact)
		}
	}
	got, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	assertExact("GetRun event count", got.Event["payload"].(map[string]any)["count"])
	assertExact(
		"GetRun input event count",
		got.Inputs["event"].(map[string]any)["payload"].(map[string]any)["count"],
	)
	if ordinary, ok := got.Inputs["ordinary_count"].(float64); !ok || ordinary != 7 {
		t.Fatalf(
			"GetRun ordinary_count = %#v (%T), want existing float64 behavior",
			got.Inputs["ordinary_count"],
			got.Inputs["ordinary_count"],
		)
	}

	runs, err := store.ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns() length = %d, want 1", len(runs))
	}
	assertExact("ListRuns event count", runs[0].Event["payload"].(map[string]any)["count"])

	if _, cancelErr := store.CancelRun(ctx, run.ID, "test"); cancelErr != nil {
		t.Fatalf("CancelRun() error = %v", cancelErr)
	}
	canceled, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun(canceled) error = %v", err)
	}
	assertExact("canceled event count", canceled.Event["payload"].(map[string]any)["count"])
	assertExact(
		"canceled input event count",
		canceled.Inputs["event"].(map[string]any)["payload"].(map[string]any)["count"],
	)
}

func TestFileRunStoreDoesNotPromoteManualEventShapedNumbers(t *testing.T) {
	t.Parallel()

	store := NewFileRunStore(t.TempDir())
	run := &Run{
		ID:          "wr_manual_event_shape",
		WorkflowRef: "workflows/manual.yml",
		Status:      RunStatusRunning,
		Event: map[string]any{
			"id":        "ev_0123456789abcdef0123456789abcdef",
			"source":    "manual",
			"connector": "user",
			"type":      "manual.lookalike",
			"payload":   map[string]any{"count": json.Number("9007199254740993")},
		},
		Inputs: map[string]any{
			"event": map[string]any{
				"id":        "ev_0123456789abcdef0123456789abcdef",
				"source":    "manual",
				"connector": "user",
				"type":      "manual.lookalike",
				"payload":   map[string]any{"count": json.Number("9007199254740993")},
			},
		},
		CreatedAt: time.Now().UTC(),
	}
	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	got, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	eventCount := got.Event["payload"].(map[string]any)["count"]
	if _, promoted := eventCount.(json.Number); promoted {
		t.Fatalf("manual event count = %#v, unexpectedly promoted to json.Number", eventCount)
	}
	inputCount := got.Inputs["event"].(map[string]any)["payload"].(map[string]any)["count"]
	if _, promoted := inputCount.(json.Number); promoted {
		t.Fatalf("manual input event count = %#v, unexpectedly promoted to json.Number", inputCount)
	}
}

func TestFileRunStoreCreateRunReturnsTypedDuplicateError(t *testing.T) {
	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	run := &Run{
		ID:          "wr_duplicate",
		WorkflowRef: "workflows/test.yml",
		Status:      RunStatusSucceeded,
		CreatedAt:   time.Now().UTC(),
	}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatalf("first CreateRun() error = %v", err)
	}

	err := store.CreateRun(ctx, run)
	if !errors.Is(err, ErrRunAlreadyExists) {
		t.Fatalf("second CreateRun() error = %v, want ErrRunAlreadyExists", err)
	}
	if !strings.Contains(err.Error(), run.ID) {
		t.Fatalf("second CreateRun() error = %q, want run ID %q", err, run.ID)
	}
}

func TestFileRunStoreCreateRunIsAtomicWithoutAdvisoryLock(t *testing.T) {
	const contenders = 32

	ctx := context.Background()
	workspace := t.TempDir()
	start := make(chan struct{})
	errs := make([]error, contenders)
	refs := make([]string, contenders)
	var wg sync.WaitGroup
	for i := range contenders {
		refs[i] = "workflows/contender-" + string(rune('a'+i)) + ".yml"
		store := NewFileRunStore(workspace)
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			// Calling the locked helper directly models platforms where the
			// process-wide advisory file lock is unavailable. The filesystem
			// create boundary must still select exactly one winner.
			errs[index] = store.createRunLocked(&Run{
				ID:          "wr_atomic",
				WorkflowRef: refs[index],
				Status:      RunStatusRunning,
				CreatedAt:   time.Now().UTC(),
			})
		}(i)
	}
	close(start)
	wg.Wait()

	successes := 0
	winnerRef := ""
	for i, err := range errs {
		switch {
		case err == nil:
			successes++
			winnerRef = refs[i]
		case errors.Is(err, ErrRunAlreadyExists):
		default:
			t.Fatalf("createRunLocked() contender %d error = %v", i, err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful creates = %d, want 1", successes)
	}

	persisted, err := NewFileRunStore(workspace).GetRun(ctx, "wr_atomic")
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if persisted.WorkflowRef != winnerRef {
		t.Fatalf("persisted workflow ref = %q, want winner %q", persisted.WorkflowRef, winnerRef)
	}
}

func TestFileRunStoreCreateRunMarshalFailureLeavesNoPartialFile(t *testing.T) {
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	run := &Run{
		ID:          "wr_bad_json",
		WorkflowRef: "workflows/bad.yml",
		Status:      RunStatusRunning,
		Event:       map[string]any{"unsupported": make(chan struct{})},
		CreatedAt:   time.Now().UTC(),
	}
	if err := store.CreateRun(context.Background(), run); err == nil {
		t.Fatal("CreateRun() error = nil, want JSON marshal error")
	}
	if runs, err := store.ListRuns(context.Background()); err != nil || len(runs) != 0 {
		t.Fatalf("runs after failed create = %#v, %v", runs, err)
	}

	run.Event = nil
	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("CreateRun() retry error = %v", err)
	}
	if persisted, err := store.GetRun(context.Background(), run.ID); err != nil || persisted.ID != run.ID {
		t.Fatalf("persisted run = %#v, %v", persisted, err)
	}
}

func TestFileRunStoreCreateRunIfUnderLimitReturnsTypedLimitError(t *testing.T) {
	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	if err := store.CreateRun(ctx, &Run{
		ID:          "wr_running",
		WorkflowRef: "workflows/test.yml",
		Status:      RunStatusRunning,
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	err := store.CreateRunIfUnderLimit(ctx, &Run{
		ID:          "wr_blocked",
		WorkflowRef: "workflows/test.yml",
		Status:      RunStatusRunning,
		CreatedAt:   time.Now().UTC(),
	}, 1)
	if !errors.Is(err, ErrRunConcurrencyLimit) {
		t.Fatalf("CreateRunIfUnderLimit() error = %v, want ErrRunConcurrencyLimit", err)
	}
	if !strings.Contains(err.Error(), "1 running, max 1") {
		t.Fatalf("CreateRunIfUnderLimit() error = %q, want running/max detail", err)
	}
}

func TestFileRunStoreCancelRunPreservesTerminalStatus(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{RunStatusSucceeded, RunStatusFailed, RunStatusCanceled} {
		t.Run(status, func(t *testing.T) {
			store := NewFileRunStore(t.TempDir())
			completedAt := time.Now().UTC().Add(-time.Minute)
			run := &Run{
				ID:          "wr_terminal",
				WorkflowRef: "workflows/test.yml",
				Status:      status,
				Error:       "original error",
				CreatedAt:   completedAt,
				UpdatedAt:   completedAt,
				CompletedAt: &completedAt,
			}
			if status == RunStatusCanceled {
				run.CancelReason = "already canceled"
				run.CancelRequestedAt = &completedAt
			}
			if err := store.CreateRun(ctx, run); err != nil {
				t.Fatalf("CreateRun() error = %v", err)
			}

			got, err := store.CancelRun(ctx, run.ID, "late cancel")
			if err != nil {
				t.Fatalf("CancelRun() error = %v", err)
			}
			if got.Status != status {
				t.Fatalf("returned status = %q, want %q", got.Status, status)
			}
			if status != RunStatusCanceled && got.CancelReason != "" {
				t.Fatalf("returned cancel reason = %q, want empty", got.CancelReason)
			}
			if status == RunStatusCanceled && got.CancelReason != "already canceled" {
				t.Fatalf("returned cancel reason = %q, want existing reason", got.CancelReason)
			}

			persisted, err := store.GetRun(ctx, run.ID)
			if err != nil {
				t.Fatalf("GetRun() error = %v", err)
			}
			if persisted.Status != status {
				t.Fatalf("persisted status = %q, want %q", persisted.Status, status)
			}
			events, err := store.Events(ctx, run.ID)
			if err != nil {
				t.Fatalf("Events() error = %v", err)
			}
			if len(events) != 0 {
				t.Fatalf("events = %#v, want none for terminal cancel no-op", events)
			}
		})
	}
}

func TestFileRunStoreUpdateRunDoesNotOverwriteTerminalStatus(t *testing.T) {
	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	now := time.Now().UTC()
	canceledAt := now.Add(time.Second)
	run := &Run{
		ID:                "wr_late_cancel",
		WorkflowRef:       "workflows/test.yml",
		Status:            RunStatusCanceled,
		CancelReason:      "operator cancel",
		CancelRequestedAt: &canceledAt,
		CompletedAt:       &canceledAt,
		CreatedAt:         now,
		UpdatedAt:         canceledAt,
	}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	incoming := &Run{
		ID:          run.ID,
		WorkflowRef: run.WorkflowRef,
		Status:      RunStatusSucceeded,
		Outputs:     map[string]any{"result": "late success"},
		CreatedAt:   now,
		CompletedAt: &canceledAt,
	}
	if err := store.UpdateRun(ctx, incoming); err != nil {
		t.Fatalf("UpdateRun() error = %v", err)
	}
	if incoming.Status != RunStatusCanceled {
		t.Fatalf("incoming status = %q, want canceled", incoming.Status)
	}
	persisted, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if persisted.Status != RunStatusCanceled || persisted.CancelReason != "operator cancel" {
		t.Fatalf("persisted run = %#v, want original canceled state", persisted)
	}
	if got := persisted.Outputs["result"]; got != nil {
		t.Fatalf("persisted output result = %#v, want no late success output", got)
	}
}

func TestFileRunStoreCreateRunIfUnderLimitIsAtomicAcrossInstances(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	stores := []*FileRunStore{
		NewFileRunStore(workspace),
		NewFileRunStore(workspace),
	}
	now := time.Now().UTC()
	var wg sync.WaitGroup
	errs := make([]error, len(stores))
	for i, store := range stores {
		wg.Add(1)
		go func(i int, store *FileRunStore) {
			defer wg.Done()
			errs[i] = store.CreateRunIfUnderLimit(ctx, &Run{
				ID:          "wr_limit_" + string(rune('a'+i)),
				WorkflowRef: "workflows/test.yml",
				Status:      RunStatusRunning,
				CreatedAt:   now,
			}, 1)
		}(i, store)
	}
	wg.Wait()

	successes := 0
	limitErrors := 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case strings.Contains(err.Error(), "concurrency limit"):
			limitErrors++
		default:
			t.Fatalf("unexpected CreateRunIfUnderLimit() error: %v", err)
		}
	}
	if successes != 1 || limitErrors != 1 {
		t.Fatalf("successes=%d limitErrors=%d, want one of each", successes, limitErrors)
	}
	runs, err := stores[0].ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 || runs[0].Status != RunStatusRunning {
		t.Fatalf("runs = %#v, want one running run", runs)
	}
}

func TestFileRunStoreCreateRunIfUnderLimitIgnoresChildRuns(t *testing.T) {
	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	now := time.Now().UTC()
	if err := store.CreateRun(ctx, &Run{
		ID:          "wr_child",
		WorkflowRef: "workflows/child.yml",
		Status:      RunStatusRunning,
		ParentRunID: "wr_parent",
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("CreateRun(child) error = %v", err)
	}
	err := store.CreateRunIfUnderLimit(ctx, &Run{
		ID:          "wr_top",
		WorkflowRef: "workflows/top.yml",
		Status:      RunStatusRunning,
		CreatedAt:   now.Add(time.Second),
	}, 1)
	if err != nil {
		t.Fatalf("CreateRunIfUnderLimit() error = %v, want child run ignored", err)
	}
}
