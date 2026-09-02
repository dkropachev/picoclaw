//go:build !mipsle && !netbsd && !(freebsd && arm)

//nolint:govet // Independent store assertions intentionally reuse err.
package eventing

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestEventingStoreMetadataDispatchAndRoutingResiduals(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, newMutableClock(now))
	inserted, err := store.Insert(t.Context(), testEnvelope("residual-store"))
	if err != nil {
		t.Fatal(err)
	}
	eventID := inserted.Event.Envelope.ID
	page, err := store.ListEventMetadata(t.Context(), EventFilter{Limit: 10})
	if err != nil || len(page.Events) != 1 || page.Events[0].Envelope.ID != eventID {
		t.Fatalf("metadata page = %#v, %v", page, err)
	}
	if _, err := store.GetEventMetadata(t.Context(), "bad"); err == nil {
		t.Fatal("invalid metadata ID accepted")
	}
	if _, err := store.GetEventPayload(t.Context(), "bad"); err == nil {
		t.Fatal("invalid payload ID accepted")
	}
	dispatch, created, err := store.CreateDispatch(t.Context(), eventID, "workflows/residual.yml")
	if err != nil || !created {
		t.Fatalf("create dispatch = %#v/%v/%v", dispatch, created, err)
	}
	if value, err := store.GetDispatch(t.Context(), dispatch.ID); err != nil || value.ID != dispatch.ID {
		t.Fatalf("get dispatch = %#v, %v", value, err)
	}
	if value, err := store.GetDispatchMetadata(t.Context(), dispatch.ID); err != nil || value.ID != dispatch.ID {
		t.Fatalf("get dispatch metadata = %#v, %v", value, err)
	}
	if page, err := store.ListDispatches(t.Context(), DispatchFilter{Limit: 10}); err != nil ||
		len(page.Dispatches) != 1 {
		t.Fatalf("dispatch page = %#v, %v", page, err)
	}
	for _, id := range []string{"bad", "dsp_00000000000000000000000000000000"} {
		if _, err := store.GetDispatch(t.Context(), id); err == nil {
			t.Errorf("GetDispatch(%q) succeeded", id)
		}
		if _, err := store.GetDispatchMetadata(t.Context(), id); err == nil {
			t.Errorf("GetDispatchMetadata(%q) succeeded", id)
		}
	}
	if err := store.NackDispatch(
		t.Context(), dispatch.ID, "wrong-token", now.Add(time.Minute), "retry",
	); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("stale dispatch nack error = %v", err)
	}
	claimed, err := store.ClaimRouting(t.Context(), "worker", 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("routing claim = %#v, %v", claimed, err)
	}
	if err := store.NackRouting(
		t.Context(), eventID, "wrong-token", now.Add(time.Minute), "retry",
	); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("stale routing nack error = %v", err)
	}
	if err := store.DeadRouting(t.Context(), eventID, "wrong-token", "dead"); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("stale routing dead error = %v", err)
	}
	for _, status := range []RoutingStatus{
		RoutingPending, RoutingClaimed, RoutingSucceeded, RoutingDead,
	} {
		if !validRoutingStatus(status) {
			t.Errorf("valid routing status rejected: %q", status)
		}
	}
	if validRoutingStatus("bad") {
		t.Fatal("invalid routing status accepted")
	}
}

func TestEventingOfflineMigrationResidualSuccess(t *testing.T) {
	home := t.TempDir()
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fence.Close() })
	path := filepath.Join(home, "eventing", "events.db")
	if err := RunOfflineDatabaseMigration(t.Context(), path); err != nil {
		t.Fatalf("offline eventing migration = %v", err)
	}
}
