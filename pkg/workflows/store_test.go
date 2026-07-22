package workflows

import (
	"context"
	"os"
	osexec "os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

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

func TestFileRunStoreLockBlocksOtherProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("workflow run store advisory file lock is not implemented on windows")
	}
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	unlock, err := store.lockRoot()
	if err != nil {
		t.Fatalf("lockRoot() error = %v", err)
	}
	released := false
	defer func() {
		if !released {
			unlock()
		}
	}()

	cmd := osexec.Command(os.Args[0], "-test.run=TestFileRunStoreLockHelper", "--", workspace)
	cmd.Env = append(os.Environ(), "PICOCLAW_WORKFLOW_LOCK_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		t.Fatalf("helper exited before lock release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	released = true
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("helper error: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("helper did not finish after lock release")
	}
	if _, err := store.GetRun(context.Background(), "wr_cross_process"); err != nil {
		t.Fatalf("GetRun(helper run) error = %v", err)
	}
}

func TestFileRunStoreLockHelper(t *testing.T) {
	if os.Getenv("PICOCLAW_WORKFLOW_LOCK_HELPER") != "1" {
		return
	}
	args := os.Args
	workspace := args[len(args)-1]
	err := NewFileRunStore(workspace).CreateRunIfUnderLimit(context.Background(), &Run{
		ID:          "wr_cross_process",
		WorkflowRef: "workflows/test.yml",
		Status:      RunStatusRunning,
		CreatedAt:   time.Now().UTC(),
	}, 10)
	if err != nil {
		t.Fatal(err)
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
