package repoeval

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type stagedBulkDeleteContext struct {
	cancelOn int
	calls    int
}

func (ctx *stagedBulkDeleteContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *stagedBulkDeleteContext) Done() <-chan struct{}       { return nil }
func (ctx *stagedBulkDeleteContext) Value(any) any               { return nil }

func (ctx *stagedBulkDeleteContext) Err() error {
	ctx.calls++
	if ctx.calls >= ctx.cancelOn {
		return context.Canceled
	}
	return nil
}

func TestStoreBulkDeleteBoundaryAndDurabilityFailures(t *testing.T) {
	t.Run("entry cancellation and selection bounds", func(t *testing.T) {
		store := newEvaluationTestStore(t, 80)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := store.BulkDelete(ctx, []BulkDeleteItem{{
			ID: testEvaluationID(80), Version: 1,
		}}); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled BulkDelete() error=%v", err)
		}
		if _, err := store.BulkDelete(t.Context(), nil); !errors.Is(err, ErrInvalidEvaluation) {
			t.Fatalf("empty BulkDelete() error=%v", err)
		}
		tooMany := make([]BulkDeleteItem, 201)
		if _, err := store.BulkDelete(t.Context(), tooMany); !errors.Is(err, ErrInvalidEvaluation) {
			t.Fatalf("oversized BulkDelete() error=%v", err)
		}
	})

	t.Run("lock failure", func(t *testing.T) {
		store := newEvaluationTestStore(t, 81)
		if err := os.Mkdir(repositoryEvaluationTestLockPath(t, store.root, "store.lock"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := store.BulkDelete(t.Context(), []BulkDeleteItem{{
			ID: testEvaluationID(81), Version: 1,
		}}); err == nil || !strings.Contains(err.Error(), "lock") {
			t.Fatalf("unsafe lock BulkDelete() error=%v", err)
		}
	})

	t.Run("unsafe storage root", func(t *testing.T) {
		store := newEvaluationTestStore(t, 82)
		if err := os.WriteFile(store.root, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.BulkDelete(t.Context(), []BulkDeleteItem{{
			ID: testEvaluationID(82), Version: 1,
		}}); err == nil {
			t.Fatalf("unsafe root BulkDelete() error=%v", err)
		}
	})

	t.Run("malformed durable state", func(t *testing.T) {
		t.Skip("legacy JSON corruption is covered by SQLite payload/schema tamper tests")
		store := newEvaluationTestStore(t, 83)
		draft, err := store.Create(t.Context(), validCreateRequest())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.path(draft.ID), []byte("{not-json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.BulkDelete(t.Context(), []BulkDeleteItem{{
			ID: draft.ID, Version: draft.Version,
		}}); err == nil {
			t.Fatal("BulkDelete accepted malformed durable state")
		}
	})

	t.Run("cancellation before deletion phase", func(t *testing.T) {
		store := newEvaluationTestStore(t, 84)
		draft, err := store.Create(t.Context(), validCreateRequest())
		if err != nil {
			t.Fatal(err)
		}
		ctx := &stagedBulkDeleteContext{cancelOn: 3}
		if _, err := store.BulkDelete(ctx, []BulkDeleteItem{{
			ID: draft.ID, Version: draft.Version,
		}}); !errors.Is(err, context.Canceled) {
			t.Fatalf("staged cancellation error=%v calls=%d", err, ctx.calls)
		}
		if _, found, err := store.Get(t.Context(), draft.ID); err != nil || !found {
			t.Fatalf("draft after cancellation found=%v err=%v", found, err)
		}
	})

	t.Run("durable remove failure is item scoped", func(t *testing.T) {
		store := newEvaluationTestStore(t, 85)
		draft, err := store.Create(t.Context(), validCreateRequest())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(store.root, 0o500); err != nil {
			t.Fatal(err)
		}
		result, bulkErr := store.BulkDelete(t.Context(), []BulkDeleteItem{{
			ID: draft.ID, Version: draft.Version,
		}})
		if err := os.Chmod(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		if bulkErr != nil {
			t.Fatalf("BulkDelete() error=%v", bulkErr)
		}
		if len(result.DeletedIDs) == 1 {
			t.Skip("filesystem permits deletion from a non-writable directory")
		}
		if len(result.Failures) != 1 || result.Failures[0].ID != draft.ID ||
			result.Failures[0].Code != "delete_failed" {
			t.Fatalf("BulkDelete()=%#v", result)
		}
		if _, found, err := store.Get(t.Context(), draft.ID); err != nil || !found {
			t.Fatalf("draft after remove failure found=%v err=%v", found, err)
		}
	})
}
