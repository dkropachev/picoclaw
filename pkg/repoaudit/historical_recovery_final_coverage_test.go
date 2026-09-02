package repoaudit

import (
	"testing"
	"time"
)

//nolint:govet // Independent test assertions intentionally reuse err.
func TestRecoverHistoricalDeduplicationMergeReleasesInterruptedLease(t *testing.T) {
	now := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return now }
	repository := "owner/interrupted-historical-merge"
	state, err := store.load(repository)
	if err != nil {
		t.Fatal(err)
	}
	state.HistoricalDeduplication = HistoricalDeduplicationReplay{
		Required:        true,
		Status:          HistoricalDeduplicationMerging,
		ProfileSnapshot: historicalReplayCoverageSnapshot(),
		Attempts:        2,
		MergeLease: HistoricalDeduplicationMergeLease{
			ID:         "rhl_interrupted_merge",
			Groups:     []HistoricalDeduplicationMergeGroup{},
			AcquiredAt: now.Add(-time.Minute),
		},
		UpdatedAt: now.Add(-time.Minute),
	}
	state.Version++
	state.UpdatedAt = now.Add(-time.Minute)
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	versionBeforeRecovery := state.Version

	recovered, replay, err := store.RecoverHistoricalDeduplicationMerge(
		repository,
		" rhl_interrupted_merge ",
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Version != versionBeforeRecovery+1 ||
		replay.Status != HistoricalDeduplicationReplaying ||
		replay.Attempts != 3 ||
		replay.Error != "" ||
		replay.FailurePhase != "" ||
		replay.MergeLease.ID != "" ||
		len(replay.MergeLease.Groups) != 0 ||
		!replay.MergeLease.AcquiredAt.IsZero() ||
		!replay.UpdatedAt.Equal(now) {
		t.Fatalf("recovered state=%#v replay=%#v", recovered, replay)
	}

	// Recovery is idempotent after the durable lease release. A controller may
	// safely repeat it after losing the response without advancing the ledger.
	again, againReplay, err := store.RecoverHistoricalDeduplicationMerge(
		repository,
		"rhl_response_was_lost",
	)
	if err != nil {
		t.Fatal(err)
	}
	if again.Version != recovered.Version ||
		againReplay.Status != HistoricalDeduplicationReplaying ||
		againReplay.MergeLease.ID != "" {
		t.Fatalf("idempotent recovery state=%#v replay=%#v", again, againReplay)
	}
}
