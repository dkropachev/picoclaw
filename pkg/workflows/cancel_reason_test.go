package workflows

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFileRunStoreBoundsAndTrimsNonemptyCancelReasons(t *testing.T) {
	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	now := time.Now().UTC()
	run := &Run{
		ID:          "wr_cancel_reason",
		WorkflowRef: "workflows/test.yml",
		Status:      RunStatusRunning,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	if _, err := store.CancelRun(
		ctx,
		run.ID,
		string([]byte{0xff}),
	); !errors.Is(err, ErrInvalidCancelReason) {
		t.Fatalf("invalid UTF-8 CancelRun() error = %v, want ErrInvalidCancelReason", err)
	}
	if _, err := store.CancelRun(
		ctx,
		run.ID,
		strings.Repeat("é", MaxWorkflowCancelReasonBytes/2+1),
	); !errors.Is(err, ErrInvalidCancelReason) {
		t.Fatalf("oversized CancelRun() error = %v, want ErrInvalidCancelReason", err)
	}
	unchanged, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun(unchanged) error = %v", err)
	}
	if unchanged.Status != RunStatusRunning {
		t.Fatalf("oversized reason changed run = %#v", unchanged)
	}

	canceled, err := store.CancelRun(
		ctx,
		run.ID,
		"  "+strings.Repeat("é", MaxWorkflowCancelReasonBytes/2)+"  ",
	)
	if err != nil {
		t.Fatalf("bounded CancelRun() error = %v", err)
	}
	if canceled.CancelReason != strings.Repeat("é", MaxWorkflowCancelReasonBytes/2) {
		t.Fatalf(
			"cancel reason byte length = %d, value prefix=%q",
			len(canceled.CancelReason),
			canceled.CancelReason[:4],
		)
	}
	if canceled.CancelRequestedAt == nil || canceled.CompletedAt == nil {
		t.Fatalf("cancellation lifecycle = %#v", canceled)
	}
}

func TestFileRunStoreRetainsEmptyCancelReasonCompatibility(t *testing.T) {
	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	now := time.Now().UTC()
	run := &Run{
		ID:          "wr_cancel_empty",
		WorkflowRef: "workflows/test.yml",
		Status:      RunStatusRunning,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	canceled, err := store.CancelRun(ctx, run.ID, "")
	if err != nil {
		t.Fatalf("CancelRun(empty) error = %v", err)
	}
	if canceled.Status != RunStatusCanceled || canceled.CancelReason != "" {
		t.Fatalf("canceled run = %#v, want empty compatibility reason", canceled)
	}
}
