package repoaudit

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"
)

func historicalResumeCoverageState(
	t *testing.T,
	store Store,
	repository string,
	now time.Time,
) RepositoryState {
	t.Helper()
	state, err := store.load(repository)
	if err != nil {
		t.Fatal(err)
	}
	state.HistoricalDeduplication = HistoricalDeduplicationReplay{
		Required:        true,
		Status:          HistoricalDeduplicationFailed,
		ProfileSnapshot: historicalReplayCoverageSnapshot(),
		Attempts:        2,
		Error:           "historical replay setup was interrupted",
		FailurePhase:    HistoricalDeduplicationFailureSetup,
		UpdatedAt:       now.Add(-time.Minute),
	}
	state.Version++
	state.UpdatedAt = now.Add(-time.Minute)
	return state
}

func TestResumeHistoricalDeduplicationReplayFromFrozenSetupFailure(t *testing.T) {
	now := time.Date(2026, 9, 1, 17, 0, 0, 0, time.UTC)
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return now }
	repository := "owner/resume-frozen-setup"
	state := historicalResumeCoverageState(t, store, repository, now)
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}

	resumed, replay, err := store.ResumeHistoricalDeduplicationReplay(
		repository,
		historicalReplayCoverageSnapshot(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Version != state.Version+1 ||
		replay.Status != HistoricalDeduplicationReplaying ||
		replay.Attempts != 3 ||
		replay.Error != "" ||
		replay.FailurePhase != "" ||
		!replay.UpdatedAt.Equal(now) {
		t.Fatalf("resumed state=%#v replay=%#v", resumed, replay)
	}
}

func TestResumeHistoricalDeduplicationReplayFailureBoundaries(t *testing.T) {
	now := time.Date(2026, 9, 1, 17, 30, 0, 0, time.UTC)
	snapshot := historicalReplayCoverageSnapshot()

	t.Run("invalid snapshot", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if _, _, err := store.ResumeHistoricalDeduplicationReplay(
			"owner/invalid-resume-snapshot",
			HistoricalDeduplicationProfileSnapshot{},
			nil,
		); err == nil {
			t.Fatal("resume accepted an invalid profile snapshot")
		}
	})

	t.Run("lock failure", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := os.Mkdir(store.root+".lock", 0o700); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.ResumeHistoricalDeduplicationReplay(
			"owner/resume-lock-failure",
			snapshot,
			nil,
		); err == nil {
			t.Fatal("resume ignored an unsafe store lock")
		}
	})

	t.Run("load failure", func(t *testing.T) {
		sentinel := errors.New("injected resume load failure")
		store := NewStore(t.TempDir())
		store.loadForTest = func(string) (RepositoryState, error) {
			return RepositoryState{}, sentinel
		}
		if _, _, err := store.ResumeHistoricalDeduplicationReplay(
			"owner/resume-load-failure",
			snapshot,
			nil,
		); !errors.Is(err, sentinel) {
			t.Fatalf("resume load error = %v", err)
		}
	})

	t.Run("save failure", func(t *testing.T) {
		sentinel := errors.New("injected resume save failure")
		store := NewStore(t.TempDir())
		store.now = func() time.Time { return now }
		state := historicalResumeCoverageState(
			t,
			store,
			"owner/resume-save-failure",
			now,
		)
		store.loadForTest = func(string) (RepositoryState, error) {
			return state, nil
		}
		store.openForTest = func(context.Context) (*sql.DB, error) {
			return nil, sentinel
		}
		if _, _, err := store.ResumeHistoricalDeduplicationReplay(
			state.Repository,
			snapshot,
			nil,
		); !errors.Is(err, sentinel) {
			t.Fatalf("resume save error = %v", err)
		}
	})
}
