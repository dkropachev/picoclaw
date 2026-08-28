package workflows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileRunStoreGetRunBounded(t *testing.T) {
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	run := &Run{ID: "bounded-run", WorkflowRef: RepositoryBugFinderWorkflowRef, Status: RunStatusSucceeded}
	if err := store.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetRunBounded(t.Context(), run.ID, 1<<20)
	if err != nil || loaded.ID != run.ID {
		t.Fatalf("bounded load = %#v err=%v", loaded, err)
	}
	if _, err := store.GetRunBounded(t.Context(), run.ID, 1); err == nil {
		t.Fatal("oversized run was decoded")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.GetRunBounded(canceled, run.ID, 1<<20); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled bounded load error = %v", err)
	}
	for _, invalid := range []struct {
		id      string
		maximum int64
	}{{id: "", maximum: 1}, {id: run.ID, maximum: 0}} {
		if _, err := store.GetRunBounded(t.Context(), invalid.id, invalid.maximum); err == nil {
			t.Fatalf("invalid bounded request %#v was accepted", invalid)
		}
	}
	symlinkID := "symlink-run"
	symlinkDir := filepath.Join(workspace, "workflow_runs", safeID(symlinkID))
	if err := os.MkdirAll(symlinkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(workspace, "workflow_runs", safeID(run.ID), "run.json"),
		filepath.Join(symlinkDir, "run.json"),
	); err == nil {
		if _, err := store.GetRunBounded(t.Context(), symlinkID, 1<<20); err == nil {
			t.Fatal("symlink run was accepted")
		}
	}
}
