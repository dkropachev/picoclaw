package workflows

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSQLiteRunStoreGetRunBoundedFailureContracts(t *testing.T) {
	store := NewFileRunStore(t.TempDir())
	if _, err := store.GetRunBounded(t.Context(), "missing", 1024); !os.IsNotExist(err) {
		t.Fatalf("missing run error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.GetRunBounded(canceled, "missing", 1024); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled bounded read error = %v", err)
	}
	now := time.Now().UTC()
	run := &Run{
		ID: "alias\\run", WorkflowRef: "workflows/test.yml", Status: RunStatusSucceeded,
		CreatedAt: now, UpdatedAt: now, Outputs: map[string]any{"large": strings.Repeat("x", 4096)},
	}
	if err := store.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetRunBounded(t.Context(), "alias/run", 1<<20); !os.IsNotExist(err) {
		t.Fatalf("aliased identity error = %v", err)
	}
	if _, err := store.GetRunBounded(t.Context(), run.ID, 128); err == nil {
		t.Fatal("oversized bounded run was accepted")
	}
}
