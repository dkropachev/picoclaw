package repoaudit

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

//nolint:govet // Table callbacks intentionally use operation-local error bindings.
func TestHistoricalDependencyPlanRejectsRecoveryAndIdentityDrift(t *testing.T) {
	_, state, snapshot := seedHistoricalCheckpointSources(t, "Coverage.Closeout")
	dependencies, err := HistoricalDeduplicationDependencies(state, "", nil)
	if err != nil || len(dependencies) == 0 {
		t.Fatalf("seed dependencies = %#v, %v", dependencies, err)
	}
	legacyID := dependencies[0].LegacyFindingID
	campaignID := NewRepositoryReviewCampaignID()

	for name, call := range map[string]func() error{
		"campaign without selection": func() error {
			_, err := HistoricalDeduplicationDependencies(state, campaignID, nil)
			return err
		},
		"selection without campaign": func() error {
			_, err := HistoricalDeduplicationDependencies(state, "", []string{legacyID})
			return err
		},
		"invalid campaign": func() error {
			_, err := HistoricalDeduplicationDependencies(state, "bad", []string{legacyID})
			return err
		},
		"invalid finding": func() error {
			_, err := HistoricalDeduplicationDependencies(state, campaignID, []string{" "})
			return err
		},
		"oversized finding": func() error {
			_, err := HistoricalDeduplicationDependencies(
				state, campaignID, []string{strings.Repeat("x", 257)},
			)
			return err
		},
		"duplicate finding": func() error {
			_, err := HistoricalDeduplicationDependencies(
				state, campaignID, []string{legacyID, legacyID},
			)
			return err
		},
		"missing recovery finding": func() error {
			_, err := HistoricalDeduplicationDependencies(
				state, campaignID, []string{"missing-finding"},
			)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("invalid dependency recovery was accepted")
			}
		})
	}

	if _, err := normalizeHistoricalDeduplicationDependencies(state, nil); err == nil {
		t.Fatal("incomplete dependency plan was accepted")
	}
	invalidIdentity := append([]HistoricalDeduplicationDependency(nil), dependencies...)
	invalidIdentity[0].LegacyFindingID = "missing"
	if _, err := normalizeHistoricalDeduplicationDependencies(state, invalidIdentity); err == nil {
		t.Fatal("unknown dependency identity was accepted")
	}
	invalidRaw := append([]HistoricalDeduplicationDependency(nil), dependencies...)
	invalidRaw[0].RawFindingID = "rrw_wrong"
	if _, err := normalizeHistoricalDeduplicationDependencies(state, invalidRaw); err == nil {
		t.Fatal("wrong raw identity was accepted")
	}
	invalidCampaign := append([]HistoricalDeduplicationDependency(nil), dependencies...)
	invalidCampaign[0].CampaignID = "bad"
	if _, err := normalizeHistoricalDeduplicationDependencies(state, invalidCampaign); err == nil {
		t.Fatal("invalid dependency campaign was accepted")
	}
	invalidBucket := append([]HistoricalDeduplicationDependency(nil), dependencies...)
	invalidBucket[0].AdmissionBucket = "different"
	if _, err := normalizeHistoricalDeduplicationDependencies(state, invalidBucket); err == nil {
		t.Fatal("wrong dependency bucket was accepted")
	}

	unadmitted := state
	unadmitted.RawFindings = nil
	unadmitted.DeduplicationJobs = nil
	future := append([]HistoricalDeduplicationDependency(nil), dependencies...)
	future[0].CampaignID = campaignID
	findingIndex := findingIndexByID(unadmitted.Findings, future[0].LegacyFindingID)
	if findingIndex < 0 {
		t.Fatal("seed finding is missing")
	}
	future[0].AdmissionBucket, err = DeduplicationAdmissionBucket(
		campaignID, unadmitted.Findings[findingIndex].File, unadmitted.Findings[findingIndex].Symbol,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeHistoricalDeduplicationDependencies(
		unadmitted,
		future,
	); !errors.Is(
		err,
		ErrHistoricalDeduplicationRestartRequired,
	) {
		t.Fatalf("future dependency identity = %v", err)
	}

	compatible, err := historicalDeduplicationDependenciesCompatible(state, snapshot, dependencies)
	if err != nil || !compatible {
		t.Fatalf("compatible dependencies = %v, %v", compatible, err)
	}
	differentSnapshot := snapshot
	differentSnapshot.CandidateLimit--
	if compatible, err := historicalDeduplicationDependenciesCompatible(
		state,
		differentSnapshot,
		dependencies,
	); err != nil ||
		compatible {
		t.Fatalf("profile drift compatibility = %v, %v", compatible, err)
	}
	wrongDependency := append([]HistoricalDeduplicationDependency(nil), dependencies...)
	wrongDependency[0].AdmissionBucket = "wrong"
	if compatible, err := historicalDeduplicationDependenciesCompatible(
		state,
		snapshot,
		wrongDependency,
	); err != nil ||
		compatible {
		t.Fatalf("dependency drift compatibility = %v, %v", compatible, err)
	}

	stray := state
	strayRaw := stray.RawFindings[0]
	strayRaw.ID = "rrw_stray_dependency"
	strayRaw.LegacyFindingID = "missing-dependency"
	stray.RawFindings = append(stray.RawFindings, strayRaw)
	if _, err := currentHistoricalDeduplicationDependencies(stray); !errors.Is(err, ErrConflict) {
		t.Fatalf("stray admitted dependency = %v", err)
	}
	if compatible, err := historicalDeduplicationDependenciesCompatible(
		stray,
		snapshot,
		dependencies,
	); !errors.Is(err, ErrConflict) ||
		compatible {
		t.Fatalf("stray compatibility = %v, %v", compatible, err)
	}
	stray.HistoricalDeduplication.ProfileSnapshot = HistoricalDeduplicationProfileSnapshot{}
	if compatible, err := historicalDeduplicationDependenciesCompatible(
		stray,
		snapshot,
		dependencies,
	); err != nil ||
		compatible {
		t.Fatalf("missing frozen profile with admitted raw = %v, %v", compatible, err)
	}

	withNative := state
	withNative.RawFindings = append(withNative.RawFindings, RawReviewFinding{ID: "native"})
	if _, err := currentHistoricalDeduplicationDependencies(withNative); err != nil {
		t.Fatalf("native raw dependency projection = %v", err)
	}

	_, twoSourceState, _ := seedHistoricalCheckpointSources(t, "Duplicate.One", "Duplicate.Two")
	twoDependencies, err := HistoricalDeduplicationDependencies(twoSourceState, "", nil)
	if err != nil || len(twoDependencies) != 2 {
		t.Fatalf("two-source dependencies = %#v, %v", twoDependencies, err)
	}
	duplicatePlan := append([]HistoricalDeduplicationDependency(nil), twoDependencies...)
	duplicatePlan[1] = duplicatePlan[0]
	if _, err := normalizeHistoricalDeduplicationDependencies(twoSourceState, duplicatePlan); err == nil {
		t.Fatal("duplicate dependency plan was accepted")
	}
}

func TestHistoricalFailureEvidenceDistinguishesProcessingAndSetup(t *testing.T) {
	failed := RawReviewFinding{
		ID: "rrw_failed", LegacyFindingID: "legacy-failed",
		AssignmentID: historicalReplayAssignmentID, State: RawFindingDeduplicationFailed,
	}
	if phase, proven := historicalDeduplicationFailureEvidence(
		RepositoryState{RawFindings: []RawReviewFinding{failed}},
	); phase != HistoricalDeduplicationFailureProcessing || !proven {
		t.Fatalf("failed evidence = %q, %v", phase, proven)
	}
	pending := failed
	pending.ID = "rrw_pending"
	pending.LegacyFindingID = "legacy-pending"
	pending.State = RawFindingDeduplicationPending
	if phase, proven := historicalDeduplicationFailureEvidence(
		RepositoryState{RawFindings: []RawReviewFinding{pending}},
	); phase != HistoricalDeduplicationFailureSetup || !proven {
		t.Fatalf("pending evidence = %q, %v", phase, proven)
	}
}

func TestHistoricalResumeAndRestartPropagateTypedPreflightFailures(t *testing.T) {
	snapshot := historicalReplayCoverageSnapshot()
	invalidSnapshot := snapshot
	invalidSnapshot.DeduplicationModel = ""
	store := newSQLiteStoreLocal(t.TempDir())
	if _, _, err := store.ResumeHistoricalDeduplicationReplay("owner/repo", invalidSnapshot, nil); err == nil {
		t.Fatal("resume accepted invalid snapshot")
	}
	if _, _, err := store.RestartHistoricalDeduplicationReplay(
		"owner/repo", HistoricalDeduplicationRestartRequest{ProfileSnapshot: invalidSnapshot},
	); err == nil {
		t.Fatal("restart accepted invalid snapshot")
	}

	sentinel := database.NewError(database.CodeUnavailable, "provider unavailable")
	failed := Store{brokerErr: sentinel}
	if _, _, err := failed.ResumeHistoricalDeduplicationReplay("owner/repo", snapshot, nil); !errors.Is(err, sentinel) {
		t.Fatalf("resume lock failure = %v", err)
	}
	if _, _, err := failed.RestartHistoricalDeduplicationReplay(
		"owner/repo", HistoricalDeduplicationRestartRequest{ProfileSnapshot: snapshot},
	); !errors.Is(err, sentinel) {
		t.Fatalf("restart lock failure = %v", err)
	}

	loadFailed := newSQLiteStoreLocal(t.TempDir())
	loadFailed.openForTest = func(context.Context) (*sql.DB, error) { return nil, sentinel }
	if _, _, err := loadFailed.ResumeHistoricalDeduplicationReplay(
		"owner/repo",
		snapshot,
		nil,
	); !errors.Is(
		err,
		sentinel,
	) {
		t.Fatalf("resume load failure = %v", err)
	}
	if _, _, err := loadFailed.RestartHistoricalDeduplicationReplay(
		"owner/repo", HistoricalDeduplicationRestartRequest{ProfileSnapshot: snapshot},
	); !errors.Is(err, sentinel) {
		t.Fatalf("restart load failure = %v", err)
	}

	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	required := newSQLiteStoreLocal(t.TempDir())
	required.now = func() time.Time { return now }
	state, err := required.load("owner/required")
	if err != nil {
		t.Fatal(err)
	}
	state.HistoricalDeduplication = HistoricalDeduplicationReplay{
		Required: true, Status: HistoricalDeduplicationPending,
		ProfileSnapshot: snapshot, UpdatedAt: now,
	}
	state.UpdatedAt = now
	if err := required.save(&state); err != nil {
		t.Fatal(err)
	}
	bogus := []HistoricalDeduplicationDependency{{LegacyFindingID: "bogus"}}
	if _, _, err := required.ResumeHistoricalDeduplicationReplay(state.Repository, snapshot, bogus); err == nil {
		t.Fatal("resume accepted incomplete dependencies")
	}
	if _, _, err := required.RestartHistoricalDeduplicationReplay(
		state.Repository,
		HistoricalDeduplicationRestartRequest{ProfileSnapshot: snapshot, Dependencies: bogus},
	); err == nil {
		t.Fatal("restart accepted incomplete dependencies")
	}
}

func TestHistoricalResumeAndRestartAdvanceSetupCheckpoint(t *testing.T) {
	snapshot := historicalReplayCoverageSnapshot()
	now := time.Date(2026, 9, 2, 13, 0, 0, 0, time.UTC)
	seedFailed := func(t *testing.T, repository string) (Store, RepositoryState) {
		t.Helper()
		store := newSQLiteStoreLocal(t.TempDir())
		store.now = func() time.Time { return now }
		state, err := store.load(repository)
		if err != nil {
			t.Fatal(err)
		}
		state.HistoricalDeduplication = HistoricalDeduplicationReplay{
			Required: true, Status: HistoricalDeduplicationFailed,
			FailurePhase: HistoricalDeduplicationFailureSetup,
			Error:        "Historical deduplication failed.", UpdatedAt: now,
		}
		state.UpdatedAt = now
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		return store, state
	}

	resumeStore, resumeState := seedFailed(t, "owner/resume-closeout")
	resumed, replay, err := resumeStore.ResumeHistoricalDeduplicationReplay(
		resumeState.Repository, snapshot, nil,
	)
	if err != nil || replay.Status != HistoricalDeduplicationPending ||
		replay.Attempts != 1 || resumed.Version != resumeState.Version+1 {
		t.Fatalf("setup resume = %#v, %#v, %v", resumed, replay, err)
	}
	repeated, replay, err := resumeStore.ResumeHistoricalDeduplicationReplay(
		resumeState.Repository, snapshot, nil,
	)
	if err != nil || repeated.Version != resumed.Version || replay.Attempts != 1 {
		t.Fatalf("idempotent setup resume = %#v, %#v, %v", repeated, replay, err)
	}
	unchanged, replay, err := resumeStore.RestartHistoricalDeduplicationReplay(
		resumeState.Repository,
		HistoricalDeduplicationRestartRequest{ProfileSnapshot: snapshot},
	)
	if err != nil || unchanged.Version != resumed.Version || replay.Status != HistoricalDeduplicationPending {
		t.Fatalf("compatible pending restart = %#v, %#v, %v", unchanged, replay, err)
	}

	restartStore, restartState := seedFailed(t, "owner/restart-closeout")
	restarted, replay, err := restartStore.RestartHistoricalDeduplicationReplay(
		restartState.Repository,
		HistoricalDeduplicationRestartRequest{ProfileSnapshot: snapshot},
	)
	if err != nil || replay.Status != HistoricalDeduplicationPending ||
		replay.Attempts != 1 || restarted.Version != restartState.Version+1 {
		t.Fatalf("compatible failed restart = %#v, %#v, %v", restarted, replay, err)
	}

	empty := newSQLiteStoreLocal(t.TempDir())
	if _, _, err := empty.RestartHistoricalDeduplicationReplay(
		"owner/not-required",
		HistoricalDeduplicationRestartRequest{ProfileSnapshot: snapshot},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("not-required restart = %v", err)
	}
}
