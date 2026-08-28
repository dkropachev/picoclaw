package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFileRunStoreGetRunBoundedRejectsPersistenceFailures(t *testing.T) {
	t.Run("lock root", func(t *testing.T) {
		workspace := t.TempDir()
		if err := os.WriteFile(filepath.Join(workspace, "workflow_runs"), []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileRunStore(workspace).GetRunBounded(t.Context(), "run", 1024); err == nil {
			t.Fatal("invalid run-store root was accepted")
		}
	})

	t.Run("missing run", func(t *testing.T) {
		if _, err := NewFileRunStore(t.TempDir()).GetRunBounded(t.Context(), "missing", 1024); !os.IsNotExist(err) {
			t.Fatalf("missing run error = %v", err)
		}
	})

	t.Run("nonregular run", func(t *testing.T) {
		workspace := t.TempDir()
		runPath := filepath.Join(workspace, "workflow_runs", "directory", "run.json")
		if err := os.MkdirAll(runPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileRunStore(workspace).GetRunBounded(t.Context(), "directory", 1024); err == nil {
			t.Fatal("nonregular run was accepted")
		}
	})

	t.Run("corrupt run", func(t *testing.T) {
		workspace := t.TempDir()
		runPath := filepath.Join(workspace, "workflow_runs", "corrupt", "run.json")
		if err := os.MkdirAll(filepath.Dir(runPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(runPath, []byte(`{"id":`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileRunStore(workspace).GetRunBounded(t.Context(), "corrupt", 1024); err == nil {
			t.Fatal("corrupt run was accepted")
		}
	})

	t.Run("safe path alias cannot change identity", func(t *testing.T) {
		workspace := t.TempDir()
		requestedID := "alias/run"
		persisted := Run{ID: "alias\\run", WorkflowRef: "workflows/test.yml", Status: RunStatusSucceeded}
		data, err := json.Marshal(&persisted)
		if err != nil {
			t.Fatal(err)
		}
		runPath := filepath.Join(workspace, "workflow_runs", safeID(requestedID), "run.json")
		if err := os.MkdirAll(filepath.Dir(runPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(runPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileRunStore(workspace).GetRunBounded(
			t.Context(), requestedID, 1024,
		); !os.IsNotExist(err) {
			t.Fatalf("aliased run identity error = %v", err)
		}
	})
}

func TestFileRunStoreGetRunBoundedChecksCancellationAfterWaitingForLock(t *testing.T) {
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	run := &Run{ID: "blocked", WorkflowRef: "workflows/test.yml", Status: RunStatusSucceeded}
	if err := store.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}

	root, err := filepath.Abs(filepath.Clean(store.root))
	if err != nil {
		t.Fatal(err)
	}
	actual, _ := fileRunStoreLocks.LoadOrStore(root, &sync.Mutex{})
	rootMu := actual.(*sync.Mutex)
	store.mu.Lock()
	result := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, loadErr := store.GetRunBounded(ctx, run.ID, 1<<20)
		result <- loadErr
	}()

	locked := false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !rootMu.TryLock() {
			locked = true
			break
		}
		rootMu.Unlock()
		time.Sleep(time.Millisecond)
	}
	if !locked {
		store.mu.Unlock()
		cancel()
		t.Fatal("bounded load did not reach the store lock")
	}
	cancel()
	store.mu.Unlock()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("post-lock cancellation error = %v", err)
	}
}
