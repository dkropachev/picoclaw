package repoaudit

import (
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func historicalReplayCoverageSnapshot() RepositoryReviewDeduplicationSnapshot {
	return RepositoryReviewDeduplicationSnapshot{
		ReviewerModel: "reviewer", DeduplicationModel: "deduplicator",
		AccountRef: "account", AccountModelRevision: "revision",
		SimilarityThreshold: 90, CandidateLimit: 4,
	}
}

func TestHistoricalReplayAdditionalPureBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	if first, second := HistoricalDeduplicationMergeLeaseID(" owner/repo ", 7, "digest"),
		HistoricalDeduplicationMergeLeaseID("owner/repo", 7, "digest"); first == "" || first != second {
		t.Fatalf("lease IDs = %q / %q", first, second)
	}
	for _, busy := range []HistoricalDeduplicationQuiescence{
		{IssueGenerations: 1}, {Publications: 1}, {Mappings: 1}, {Validations: 1},
	} {
		if busy.Ready() {
			t.Fatalf("busy quiescence reported ready: %#v", busy)
		}
	}
	if !(HistoricalDeduplicationQuiescence{}).Ready() {
		t.Fatal("empty quiescence is not ready")
	}
	state := RepositoryState{
		IssueDrafts: []IssueDraft{
			{State: IssueDraftGenerating}, {State: IssueDraftPublishing},
			{State: IssueDraftUnknown}, {State: IssueDraftEditing},
		},
		MappingJobs: []RepositoryMappingJob{
			{State: RepositoryMappingRunning}, {State: RepositoryMappingPending},
		},
		ValidationJobs: []RepositoryValidationJob{
			{State: RepositoryValidationPending}, {State: RepositoryValidationRunning},
			{State: RepositoryValidationConfirmed},
		},
	}
	if got := HistoricalDeduplicationQuiescenceForState(state); got != (HistoricalDeduplicationQuiescence{
		IssueGenerations: 1, Publications: 2, Mappings: 1, Validations: 2,
	}) {
		t.Fatalf("quiescence = %#v", got)
	}
	if historicalDeduplicationBoundaryID(" boundary ") != "boundary" ||
		historicalDeduplicationBoundaryID("") == "" ||
		historicalDeduplicationBoundaryID(strings.Repeat("x", 257)) == strings.Repeat("x", 257) ||
		historicalDeduplicationBoundaryID("bad\x00boundary") == "bad\x00boundary" {
		t.Fatal("historical boundary normalization failed")
	}

	batchState := RepositoryState{
		Contexts: []FindingContext{{ID: "ctx", RunID: "context-run"}},
		Runs: []ReviewRun{
			{ID: "later-run", FindingIDs: []string{"run-finding"}, CompletedAt: now.Add(time.Hour)},
			{ID: "earlier-run", FindingIDs: []string{"run-finding"}, CompletedAt: now},
		},
		Findings: []Finding{
			{ID: "pending", DeduplicationPending: true, CreatedAt: now},
			{ID: "projection", CreatedAt: now},
			{ID: "context-finding", ContextIDs: []string{"missing", "ctx"}, CreatedAt: now},
			{ID: "run-finding", CreatedAt: now},
			{ID: "orphan", CreatedAt: now},
		},
		DeduplicatedFindings: []DeduplicatedReviewFinding{{ID: "projection"}},
	}
	batches := HistoricalDeduplicationReplayBatches(batchState)
	gotBoundaries := make([]string, 0, len(batches))
	for _, batch := range batches {
		gotBoundaries = append(gotBoundaries, batch.BoundaryID)
	}
	if !slices.Contains(gotBoundaries, "context-run") ||
		!slices.Contains(gotBoundaries, "earlier-run") ||
		!slices.Contains(gotBoundaries, "legacy:orphan") ||
		slices.Contains(gotBoundaries, "later-run") {
		t.Fatalf("batch boundaries = %#v", gotBoundaries)
	}
}

func TestHistoricalReplayAdditionalValidationBranches(t *testing.T) {
	now := time.Date(2026, 8, 30, 11, 0, 0, 0, time.UTC)
	validSnapshot := historicalReplayCoverageSnapshot()
	if err := validateHistoricalDeduplicationProfileSnapshot(validSnapshot); err != nil {
		t.Fatal(err)
	}
	invalidSnapshots := []RepositoryReviewDeduplicationSnapshot{
		{ProfileID: "rrpf_valid", ReviewerModel: "r", DeduplicationModel: "d"},
		{ProfileID: "bad", ProfileVersion: 1, ReviewerModel: "r", DeduplicationModel: "d"},
		{ReviewerModel: "", DeduplicationModel: "d"},
		{ReviewerModel: "r", DeduplicationModel: ""},
		{ReviewerModel: "r", DeduplicationModel: "d", SimilarityThreshold: 101},
		{ReviewerModel: "r", DeduplicationModel: "d", CandidateLimit: DeduplicationMaximumShortlist + 1},
	}
	for index, snapshot := range invalidSnapshots {
		if err := validateHistoricalDeduplicationProfileSnapshot(snapshot); err == nil {
			t.Fatalf("invalid snapshot %d accepted: %#v", index, snapshot)
		}
	}

	validGroup := HistoricalDeduplicationMergeGroup{Members: []HistoricalDeduplicationFindingVersion{
		{ID: "b", Version: 2}, {ID: "a", Version: 1},
	}}
	normalized, err := normalizeHistoricalDeduplicationMergeGroups([]HistoricalDeduplicationMergeGroup{validGroup})
	if err != nil || normalized[0].Members[0].ID != "a" {
		t.Fatalf("normalized=%#v err=%v", normalized, err)
	}
	invalidGroups := [][]HistoricalDeduplicationMergeGroup{
		{{Members: []HistoricalDeduplicationFindingVersion{{ID: "one", Version: 1}}}},
		{{Members: []HistoricalDeduplicationFindingVersion{{ID: "", Version: 1}, {ID: "b", Version: 1}}}},
		{{Members: []HistoricalDeduplicationFindingVersion{{ID: " a", Version: 1}, {ID: "b", Version: 1}}}},
		{{Members: []HistoricalDeduplicationFindingVersion{{ID: "a", Version: 0}, {ID: "b", Version: 1}}}},
		{
			{Members: []HistoricalDeduplicationFindingVersion{{ID: "a", Version: 1}, {ID: "b", Version: 1}}},
			{Members: []HistoricalDeduplicationFindingVersion{{ID: "b", Version: 1}, {ID: "c", Version: 1}}},
		},
	}
	for index, groups := range invalidGroups {
		if _, err := normalizeHistoricalDeduplicationMergeGroups(groups); err == nil {
			t.Fatalf("invalid group set %d accepted", index)
		}
	}
	tooMany := make([]HistoricalDeduplicationMergeGroup, maxHistoricalDeduplicationMergeGroups+1)
	if _, err := normalizeHistoricalDeduplicationMergeGroups(tooMany); err == nil {
		t.Fatal("oversized merge set accepted")
	}

	targetState := RepositoryState{RepositoryFindings: []RepositoryFinding{{ID: "a", Version: 1}}}
	if err := validateHistoricalDeduplicationMergeTargets(targetState, []HistoricalDeduplicationMergeGroup{{
		Members: []HistoricalDeduplicationFindingVersion{{ID: "a", Version: 1}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := validateHistoricalDeduplicationMergeTargets(targetState, []HistoricalDeduplicationMergeGroup{{
		Members: []HistoricalDeduplicationFindingVersion{{ID: "missing", Version: 1}},
	}}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing target error = %v", err)
	}
	if err := validateHistoricalDeduplicationMergeTargets(targetState, []HistoricalDeduplicationMergeGroup{{
		Members: []HistoricalDeduplicationFindingVersion{{ID: "a", Version: 2}},
	}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale target error = %v", err)
	}

	validReplay := RepositoryState{HistoricalDeduplication: HistoricalDeduplicationReplay{
		Required: true, Status: HistoricalDeduplicationPending, UpdatedAt: now,
	}}
	if err := validateHistoricalDeduplicationReplay(validReplay); err != nil {
		t.Fatal(err)
	}
	validReplay.HistoricalDeduplication = HistoricalDeduplicationReplay{}
	if err := validateHistoricalDeduplicationReplay(validReplay); err != nil {
		t.Fatal(err)
	}
	invalidReplays := []HistoricalDeduplicationReplay{
		{Required: true, Status: HistoricalDeduplicationPending, Attempts: -1, UpdatedAt: now},
		{Required: true, Status: "unknown", UpdatedAt: now},
		{Required: false, Status: HistoricalDeduplicationPending, UpdatedAt: now},
		{Required: true, Status: HistoricalDeduplicationReplaying, UpdatedAt: now},
		{Required: true, Status: HistoricalDeduplicationPending, UpdatedAt: now, MergeLease: HistoricalDeduplicationMergeLease{ID: "lease"}},
		{Required: true, Status: HistoricalDeduplicationPending, UpdatedAt: now, Error: strings.Repeat("x", 1025)},
		{Required: true, Status: HistoricalDeduplicationCompleted, UpdatedAt: now},
	}
	for index, replay := range invalidReplays {
		if index == len(invalidReplays)-1 {
			replay.Required = true
		}
		if err := validateHistoricalDeduplicationReplay(RepositoryState{HistoricalDeduplication: replay}); err == nil {
			t.Fatalf("invalid replay %d accepted: %#v", index, replay)
		}
	}
	completed := RepositoryState{HistoricalDeduplication: HistoricalDeduplicationReplay{
		Status: HistoricalDeduplicationCompleted, UpdatedAt: now,
	}}
	if err := validateHistoricalDeduplicationReplay(completed); err != nil {
		t.Fatal(err)
	}
	merging := RepositoryState{HistoricalDeduplication: HistoricalDeduplicationReplay{
		Required: true, Status: HistoricalDeduplicationMerging,
		ProfileSnapshot: validSnapshot, UpdatedAt: now,
		MergeLease: HistoricalDeduplicationMergeLease{
			ID: "lease", Groups: []HistoricalDeduplicationMergeGroup{normalized[0]}, AcquiredAt: now,
		},
	}}
	if err := validateHistoricalDeduplicationReplay(merging); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalReplayAdditionalMergeHelpers(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	for _, state := range []RepositoryFindingValidationState{
		RepositoryValidationNotRequested, RepositoryValidationPending, RepositoryValidationRunning,
		RepositoryValidationFailed, RepositoryValidationInconclusive, RepositoryValidationNotFixed,
		RepositoryValidationConfirmed,
	} {
		if historicalValidationRank(state) < 0 {
			t.Fatalf("negative rank for %q", state)
		}
	}
	mergeHistoricalValidationSnapshot(nil, RepositoryFinding{})
	target := RepositoryFinding{ValidationState: RepositoryValidationNotFixed}
	mergeHistoricalValidationSnapshot(&target, RepositoryFinding{ValidationState: RepositoryValidationFailed})
	if target.ValidationState != RepositoryValidationNotFixed {
		t.Fatal("lower validation replaced target")
	}
	fixTime := now.Add(-time.Hour)
	mergeHistoricalValidationSnapshot(&target, RepositoryFinding{
		ValidationState: RepositoryValidationConfirmed,
		FixCommitSHA:    strings.Repeat("f", 40), FixCommitTime: fixTime, FirstContainingTag: "v1.2.3",
	})
	if target.ValidationState != RepositoryValidationConfirmed || target.FixCommitTime != fixTime {
		t.Fatalf("validation merge = %#v", target)
	}

	lifecycles := []RepositoryFindingLifecycle{
		RepositoryFindingResolved, RepositoryFindingDismissed, RepositoryFindingOpen,
		RepositoryFindingResolutionPending, RepositoryFindingRegressed, RepositoryFindingLifecycle("unknown"),
	}
	for _, left := range lifecycles {
		for _, right := range lifecycles {
			_ = mergeHistoricalRepositoryFindingLifecycle(left, right)
		}
	}

	if err := mergeHistoricalRepositoryFindingGroup(nil, HistoricalDeduplicationMergeGroup{}, now); err == nil {
		t.Fatal("nil merge state accepted")
	}
	missingState := RepositoryState{}
	if err := mergeHistoricalRepositoryFindingGroup(&missingState, HistoricalDeduplicationMergeGroup{
		Members: []HistoricalDeduplicationFindingVersion{{ID: "missing", Version: 1}},
	}, now); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing merge member error = %v", err)
	}

	created := now.Add(-2 * time.Hour)
	state := RepositoryState{
		Findings: []Finding{
			{ID: "occ-a", RepositoryFindingID: "a", RepositoryMatchState: RepositoryMatchKnown, Version: 1},
			{ID: "occ-b", RepositoryFindingID: "b", RepositoryMatchState: RepositoryMatchNew, PostResolutionFindingID: "b", Version: 1},
		},
		DeduplicatedFindings: []DeduplicatedReviewFinding{
			{ID: "dedup-a", RepositoryFindingID: "a", RepositoryMatchState: RepositoryMatchKnown, Version: 1},
			{ID: "dedup-b", RepositoryFindingID: "b", RepositoryMatchState: RepositoryMatchNew, Version: 1,
				History: make([]DeduplicatedFindingHistoryEntry, DeduplicationHistoryLimit)},
		},
		MappingJobs: []RepositoryMappingJob{{
			RepositoryFindingID: "b", Adjudication: RepositoryMappingAdjudication{CandidateID: "b"},
		}},
		ValidationJobs: []RepositoryValidationJob{{RepositoryFindingID: "b"}},
		RepositoryFindings: []RepositoryFinding{
			{ID: "a", Version: 1, CreatedAt: created, UpdatedAt: created,
				ReviewFindingIDs: []string{"occ-a"}, FoundCommits: []string{"one"},
				PathSymbolHistory: []RepositoryFindingPathSymbol{{ReviewFindingID: "occ-a", ObservedAt: created}},
				MatchState:        RepositoryMatchNew, Lifecycle: RepositoryFindingResolved,
				ValidationState: RepositoryValidationFailed},
			{ID: "b", Version: 2, CreatedAt: created.Add(time.Hour), UpdatedAt: created.Add(time.Hour),
				ReviewFindingIDs: []string{"occ-b"}, FoundCommits: []string{"two"},
				PathSymbolHistory: []RepositoryFindingPathSymbol{{ReviewFindingID: "occ-b", ObservedAt: created.Add(time.Hour)}},
				MatchState:        RepositoryMatchNew, Lifecycle: RepositoryFindingRegressed,
				ValidationState: RepositoryValidationConfirmed, FixCommitSHA: strings.Repeat("f", 40)},
			{ID: "c", Version: 3, CreatedAt: now, UpdatedAt: now,
				PossibleDuplicates: []RepositoryFindingPossibleDuplicate{
					{CandidateID: "b"}, {CandidateID: "c"}, {CandidateID: "a"}, {CandidateID: "b"},
				}, MatchState: RepositoryMatchProvisional},
		},
	}
	group := HistoricalDeduplicationMergeGroup{Members: []HistoricalDeduplicationFindingVersion{
		{ID: "b", Version: 2}, {ID: "a", Version: 1},
	}}
	if err := mergeHistoricalRepositoryFindingGroup(&state, group, now); err != nil {
		t.Fatal(err)
	}
	if len(state.RepositoryFindings) != 2 || state.RepositoryFindings[0].ID != "a" ||
		state.Findings[1].RepositoryFindingID != "a" || state.Findings[1].PostResolutionFindingID != "a" ||
		state.DeduplicatedFindings[1].RepositoryFindingID != "a" ||
		len(state.DeduplicatedFindings[1].History) != DeduplicationHistoryLimit ||
		state.MappingJobs[0].RepositoryFindingID != "a" || state.ValidationJobs[0].RepositoryFindingID != "a" {
		t.Fatalf("merged state = %#v", state)
	}
	if mappingJobIndexByReviewFindingID(nil, "missing") != -1 ||
		mappingJobIndexByReviewFindingID([]RepositoryMappingJob{{ReviewFindingID: "x"}}, "x") != 0 {
		t.Fatal("mapping job lookup failed")
	}
}

func TestHistoricalReplayAdditionalAssociationBranches(t *testing.T) {
	now := time.Date(2026, 8, 30, 13, 0, 0, 0, time.UTC)
	associateHistoricalDeduplicatedFindings(nil, now)
	state := RepositoryState{
		RawFindings: []RawReviewFinding{
			{ID: "live", LegacyFindingID: "legacy"},
			{ID: "rrw_one", LegacyFindingID: "legacy"},
			{ID: "rrw_two", LegacyFindingID: "legacy-two"},
		},
		Findings: []Finding{
			{ID: "legacy", RepositoryFindingID: "target"},
			{ID: "legacy-two", RepositoryFindingID: "other"},
			{ID: "dedup", Version: 1},
		},
		DeduplicatedFindings: []DeduplicatedReviewFinding{
			{ID: "already", RepositoryFindingID: "target"},
			{ID: "live-only", RawSourceIDs: []string{"live"}},
			{ID: "mixed-targets", RawSourceIDs: []string{"rrw_one", "rrw_two"}},
			{ID: "missing-target", RawSourceIDs: []string{"rrw_one"}},
			{ID: "dedup", Version: 1, RawSourceIDs: []string{"rrw_one"}},
		},
		RepositoryFindings: []RepositoryFinding{{
			ID: "target", Version: 1, MatchState: RepositoryMatchKnown,
			ReviewFindingIDs: []string{"legacy"},
		}},
		MappingJobs: []RepositoryMappingJob{{
			ID: "job", ReviewFindingID: "dedup", State: RepositoryMappingPending,
		}},
	}
	// Keep one historical source pointed at a missing aggregate to cover the
	// target/projection guards, then repair it for the successful association.
	state.Findings[0].RepositoryFindingID = "missing"
	associateHistoricalDeduplicatedFindings(&state, now)
	state.Findings[0].RepositoryFindingID = "target"
	associateHistoricalDeduplicatedFindings(&state, now)
	if state.DeduplicatedFindings[4].RepositoryFindingID != "target" ||
		state.Findings[2].RepositoryFindingID != "target" ||
		state.MappingJobs[0].State != RepositoryMappingCompleted {
		t.Fatalf("association state = %#v", state)
	}
}

func TestHistoricalReplayAdditionalStoreStateMachine(t *testing.T) {
	now := time.Date(2026, 8, 30, 14, 0, 0, 0, time.UTC)
	snapshot := historicalReplayCoverageSnapshot()
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return now }
	repository := "owner/historical-state-machine"
	state, err := store.load(repository)
	if err != nil {
		t.Fatal(err)
	}
	state.HistoricalDeduplication = HistoricalDeduplicationReplay{
		Required: true, Status: HistoricalDeduplicationPending, UpdatedAt: now,
	}
	state.UpdatedAt = now
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.FreezeHistoricalDeduplicationReplay(repository,
		RepositoryReviewDeduplicationSnapshot{}); err == nil {
		t.Fatal("invalid snapshot frozen")
	}
	state, replay, err := store.FreezeHistoricalDeduplicationReplay(repository, snapshot)
	if err != nil || replay.Status != HistoricalDeduplicationReplaying {
		t.Fatalf("freeze replay=%#v err=%v", replay, err)
	}
	if _, same, err := store.FreezeHistoricalDeduplicationReplay(repository, snapshot); err != nil ||
		!reflect.DeepEqual(same.ProfileSnapshot, snapshot) {
		t.Fatalf("idempotent freeze=%#v err=%v", same, err)
	}
	different := snapshot
	different.CandidateLimit = 3
	if _, _, err := store.FreezeHistoricalDeduplicationReplay(repository, different); !errors.Is(err, ErrConflict) {
		t.Fatalf("different frozen snapshot error=%v", err)
	}
	if _, _, _, err := store.AcquireHistoricalDeduplicationMerge(repository, "", nil); err == nil {
		t.Fatal("empty lease accepted")
	}
	if _, _, _, err := store.AcquireHistoricalDeduplicationMerge(repository, "lease", []HistoricalDeduplicationMergeGroup{{
		Members: []HistoricalDeduplicationFindingVersion{{ID: "one", Version: 1}},
	}}); err == nil {
		t.Fatal("invalid group accepted")
	}
	state, replay, acquired, err := store.AcquireHistoricalDeduplicationMerge(repository, "lease", nil)
	if err != nil || !acquired || replay.Status != HistoricalDeduplicationMerging {
		t.Fatalf("acquire=%v replay=%#v err=%v", acquired, replay, err)
	}
	if _, _, acquired, err := store.AcquireHistoricalDeduplicationMerge(repository, "lease", nil); err != nil || acquired {
		t.Fatalf("idempotent acquire=%v err=%v", acquired, err)
	}
	if _, _, _, err := store.AcquireHistoricalDeduplicationMerge(repository, "other", nil); !errors.Is(err, ErrHistoricalDeduplicationInProgress) {
		t.Fatalf("competing acquire error=%v", err)
	}
	if _, _, err := store.CompleteHistoricalDeduplicationMerge(repository, "wrong"); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong completion error=%v", err)
	}
	state, replay, err = store.CompleteHistoricalDeduplicationMerge(repository, "lease")
	if err != nil || replay.Status != HistoricalDeduplicationCompleted || replay.Required {
		t.Fatalf("complete replay=%#v err=%v", replay, err)
	}
	if _, _, err := store.CompleteHistoricalDeduplicationMerge(repository, "anything"); err != nil {
		t.Fatalf("completion replay error=%v", err)
	}
	if _, _, err := store.FailHistoricalDeduplicationReplay(repository, ""); err != nil {
		t.Fatalf("completed fail replay error=%v", err)
	}
	if _, _, err := store.RetryHistoricalDeduplicationReplay(repository); err != nil {
		t.Fatalf("completed retry replay error=%v", err)
	}
	_ = state

	second := NewStore(t.TempDir())
	second.now = func() time.Time { return now }
	secondState, _ := second.load(repository)
	secondState.HistoricalDeduplication = HistoricalDeduplicationReplay{
		Required: true, Status: HistoricalDeduplicationMerging,
		ProfileSnapshot: snapshot, UpdatedAt: now,
		MergeLease: HistoricalDeduplicationMergeLease{
			ID: "merge", Groups: []HistoricalDeduplicationMergeGroup{}, AcquiredAt: now,
		},
	}
	secondState.UpdatedAt = now
	if err := second.save(&secondState); err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.FailHistoricalDeduplicationReplay(repository, "wrong"); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong failure lease error=%v", err)
	}
	_, failed, err := second.FailHistoricalDeduplicationReplay(repository, "merge")
	if err != nil || failed.Status != HistoricalDeduplicationFailed {
		t.Fatalf("failed replay=%#v err=%v", failed, err)
	}
	if _, _, err := second.FreezeHistoricalDeduplicationReplay(repository, snapshot); !errors.Is(err, ErrConflict) {
		t.Fatalf("freeze failed replay error=%v", err)
	}
	_, pending, err := second.RetryHistoricalDeduplicationReplay(repository)
	if err != nil || pending.Status != HistoricalDeduplicationPending {
		t.Fatalf("retry replay=%#v err=%v", pending, err)
	}
	if _, _, err := second.RetryHistoricalDeduplicationReplay(repository); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry pending replay error=%v", err)
	}
}

func TestHistoricalReplayAdditionalResetErrors(t *testing.T) {
	now := time.Date(2026, 8, 30, 15, 0, 0, 0, time.UTC)
	snapshot := historicalReplayCoverageSnapshot()
	if err := resetHistoricalDeduplicationModelWork(nil, snapshot, now); err == nil {
		t.Fatal("nil reset state accepted")
	}
	raw := RawReviewFinding{ID: "rrw_raw", LegacyFindingID: "legacy"}
	for name, state := range map[string]RepositoryState{
		"missing job": {RawFindings: []RawReviewFinding{raw}},
		"running job": {
			RawFindings:       []RawReviewFinding{raw},
			DeduplicationJobs: []DeduplicationJob{{RawFindingID: raw.ID, State: DeduplicationJobRunning}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := resetHistoricalDeduplicationModelWork(&state, snapshot, now); err == nil {
				t.Fatal("invalid reset state accepted")
			}
		})
	}

	liveRaw := RawReviewFinding{
		ID: "live-raw", Repository: "owner/repo", CommitSHA: strings.Repeat("a", 40),
		File:     FileRef{Path: "live.go", BlobSHA: strings.Repeat("b", 40)},
		Severity: "high", Title: "live", Evidence: "evidence", Impact: "impact",
		Validation: Validation{Status: "confirmed", Summary: "confirmed"},
		ContextID:  "context", RunID: "run", AssignmentID: "assignment", Model: "model",
		CampaignID: "campaign", AdmissionBucket: "bucket", InsertionOrdinal: 2,
		State: RawFindingDeduplicationCompleted, Disposition: RawFindingDispositionDuplicate,
		DeduplicatedFindingID: "combined", CreatedAt: now, UpdatedAt: now,
	}
	liveRaw.DiagnosisDigest = RawReviewFindingDiagnosisDigest(liveRaw)
	historicalRaw := liveRaw
	historicalRaw.ID = "rrw_historical"
	historicalRaw.LegacyFindingID = "legacy"
	historicalRaw.InsertionOrdinal = 1
	historicalRaw.Disposition = RawFindingDispositionNew
	historicalRaw.DiagnosisDigest = RawReviewFindingDiagnosisDigest(historicalRaw)
	completedJob := func(id string, ordinal uint64) DeduplicationJob {
		return DeduplicationJob{
			ID: "job-" + id, RawFindingID: id, State: DeduplicationJobCompleted,
			AdmissionBucket: "bucket", InsertionOrdinal: ordinal, ModelSnapshot: snapshot,
			Decision: DeduplicationJudgment{Decision: "new"}, CreatedAt: now, UpdatedAt: now,
		}
	}
	combined := newDeduplicatedReviewFinding(historicalRaw, 1, nil, now)
	combined.ID = "combined"
	combined.RawSourceIDs = []string{historicalRaw.ID, liveRaw.ID}
	combined.Status = FindingPosted
	combined.IssueDraftID = "draft"
	combined.RepositoryFindingID = "repository"
	combined.RepositoryMatchState = RepositoryMatchKnown
	state := RepositoryState{
		RawFindings:          []RawReviewFinding{historicalRaw, liveRaw},
		DeduplicationJobs:    []DeduplicationJob{completedJob(historicalRaw.ID, 1), completedJob(liveRaw.ID, 2)},
		DeduplicatedFindings: []DeduplicatedReviewFinding{combined},
		Findings:             []Finding{{ID: combined.ID}},
		MappingJobs: []RepositoryMappingJob{{
			ID: mappingJobID(combined.ID), ReviewFindingID: combined.ID, State: RepositoryMappingCompleted,
			RepositoryFindingID: "repository", CreatedAt: now, UpdatedAt: now,
		}},
		IssueDrafts: []IssueDraft{{ID: "draft", FindingIDs: []string{combined.ID}, Version: 1}},
		Runs:        []ReviewRun{{FindingIDs: []string{combined.ID}}},
		RepositoryFindings: []RepositoryFinding{{
			ID: "repository", Version: 1, ReviewFindingIDs: []string{combined.ID},
			PathSymbolHistory: []RepositoryFindingPathSymbol{{ReviewFindingID: combined.ID}},
		}},
	}
	if err := resetHistoricalDeduplicationModelWork(&state, snapshot, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(state.DeduplicatedFindings) != 1 || state.DeduplicatedFindings[0].ID == combined.ID ||
		state.RawFindings[0].State != RawFindingDeduplicationPending ||
		state.RawFindings[1].DeduplicatedFindingID != state.DeduplicatedFindings[0].ID ||
		state.MappingJobs[0].ReviewFindingID != state.DeduplicatedFindings[0].ID ||
		state.IssueDrafts[0].FindingIDs[0] != state.DeduplicatedFindings[0].ID ||
		state.Runs[0].FindingIDs[0] != state.DeduplicatedFindings[0].ID {
		t.Fatalf("mixed reset state=%#v", state)
	}

	missingLive := RepositoryState{
		RawFindings:       []RawReviewFinding{historicalRaw},
		DeduplicationJobs: []DeduplicationJob{completedJob(historicalRaw.ID, 1)},
		DeduplicatedFindings: []DeduplicatedReviewFinding{{
			ID: "mixed", RawSourceIDs: []string{historicalRaw.ID, "missing-live"},
		}},
	}
	if err := resetHistoricalDeduplicationModelWork(&missingLive, snapshot, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing live source reset error=%v", err)
	}
}

func TestHistoricalReplayAdditionalAdmissionAndRawBuilderBranches(t *testing.T) {
	now := time.Date(2026, 8, 30, 16, 0, 0, 0, time.UTC)
	snapshot := historicalReplayCoverageSnapshot()
	file := FileRef{Path: "file.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 10}
	baseFinding := Finding{
		ID: "legacy", Repository: "owner/repo", CommitSHA: strings.Repeat("b", 40),
		File: file, Severity: "high", Title: "title", Evidence: "evidence", Impact: "impact",
		Validation: Validation{Status: "confirmed", Summary: "confirmed"},
	}
	if _, _, err := historicalRawFindingAndJob(
		RepositoryState{Repository: "owner/repo"}, Finding{ID: "bad"},
		HistoricalDeduplicationReplayBatch{}, nil, snapshot, 0, now,
	); err == nil {
		t.Fatal("invalid historical raw identity accepted")
	}
	contexts := map[string]FindingContext{
		"ctx": {ID: "ctx", Model: "context-model", Reviewer: "context-reviewer"},
	}
	withContext := baseFinding
	withContext.ContextIDs = []string{"missing", "ctx"}
	raw, job, err := historicalRawFindingAndJob(
		RepositoryState{Repository: "owner/repo"}, withContext,
		HistoricalDeduplicationReplayBatch{BoundaryID: " batch "}, contexts,
		snapshot, 0, now,
	)
	if err != nil || raw.ContextID != "ctx" || raw.Model != "context-model" ||
		raw.Reviewer != "context-reviewer" || raw.InsertionOrdinal != 1 || job.InsertionOrdinal != 1 {
		t.Fatalf("context raw=%#v job=%#v err=%v", raw, job, err)
	}
	withoutContext := baseFinding
	withoutContext.Models = []string{"finding-model"}
	withoutContext.CreatedAt = now.Add(time.Hour)
	raw, _, err = historicalRawFindingAndJob(
		RepositoryState{Repository: "owner/repo"}, withoutContext,
		HistoricalDeduplicationReplayBatch{BoundaryID: strings.Repeat("z", 300)}, nil,
		snapshot, 9, now,
	)
	if err != nil || raw.Model != "finding-model" || raw.Reviewer != "finding-model" ||
		raw.ContextID == "" || raw.UpdatedAt.Before(raw.CreatedAt) || raw.CampaignID == strings.Repeat("z", 300) {
		t.Fatalf("fallback raw=%#v err=%v", raw, err)
	}
	withoutContext.Models = nil
	withoutContext.CreatedAt = time.Time{}
	raw, _, err = historicalRawFindingAndJob(
		RepositoryState{Repository: "owner/repo"}, withoutContext,
		HistoricalDeduplicationReplayBatch{BoundaryID: "batch"}, nil, snapshot, 2, now,
	)
	if err != nil || raw.Model != snapshot.ReviewerModel || !raw.CreatedAt.Equal(now) {
		t.Fatalf("snapshot fallback raw=%#v err=%v", raw, err)
	}

	loaderStore := NewStore(t.TempDir())
	loaderStore.now = func() time.Time { return now }
	loaderStore.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{}, errors.New("injected load failure")
	}
	if _, _, err := loaderStore.AdmitNextHistoricalDeduplicationBatch("owner/repo"); err == nil {
		t.Fatal("admission load failure hidden")
	}
	loaderStore.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{HistoricalDeduplication: HistoricalDeduplicationReplay{
			Required: true, Status: HistoricalDeduplicationReplaying,
		}}, nil
	}
	if _, _, err := loaderStore.AdmitNextHistoricalDeduplicationBatch("owner/repo"); err == nil {
		t.Fatal("invalid replay snapshot admitted")
	}
	loaderStore.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{HistoricalDeduplication: HistoricalDeduplicationReplay{
			Required: true, Status: HistoricalDeduplicationReplaying, ProfileSnapshot: snapshot,
		}}, nil
	}
	_, admission, err := loaderStore.AdmitNextHistoricalDeduplicationBatch("owner/repo")
	if err != nil || !admission.AllComplete || !admission.Complete {
		t.Fatalf("empty admission=%#v err=%v", admission, err)
	}
}

func TestHistoricalReplayAdditionalMergeGroupErrors(t *testing.T) {
	if groups, err := HistoricalDeduplicationRepositoryMergeGroups(RepositoryState{}); err != nil || len(groups) != 0 {
		t.Fatalf("empty groups=%#v err=%v", groups, err)
	}
	pending := RepositoryState{RawFindings: []RawReviewFinding{{
		ID: "rrw_pending", LegacyFindingID: "legacy", State: RawFindingDeduplicationPending,
	}}}
	if _, err := HistoricalDeduplicationRepositoryMergeGroups(pending); !errors.Is(
		err, ErrHistoricalDeduplicationNotQuiescent,
	) {
		t.Fatalf("pending historical source error=%v", err)
	}
	completedRaw := RawReviewFinding{
		ID: "rrw_completed", LegacyFindingID: "legacy", State: RawFindingDeduplicationCompleted,
		DeduplicatedFindingID: "dedup",
	}
	missingLegacy := RepositoryState{
		RawFindings: []RawReviewFinding{completedRaw},
		DeduplicatedFindings: []DeduplicatedReviewFinding{{
			ID: "dedup", RawSourceIDs: []string{"missing", completedRaw.ID},
		}},
	}
	if _, err := HistoricalDeduplicationRepositoryMergeGroups(missingLegacy); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing legacy source error=%v", err)
	}
	missingTarget := missingLegacy
	missingTarget.Findings = []Finding{{ID: "legacy", RepositoryFindingID: "missing"}}
	if _, err := HistoricalDeduplicationRepositoryMergeGroups(missingTarget); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing repository target error=%v", err)
	}
	mappedMissing := RepositoryState{
		RawFindings: []RawReviewFinding{completedRaw},
		Findings:    []Finding{{ID: "legacy"}},
		DeduplicatedFindings: []DeduplicatedReviewFinding{{
			ID: "dedup", RawSourceIDs: []string{completedRaw.ID}, RepositoryFindingID: "missing",
		}},
	}
	if _, err := HistoricalDeduplicationRepositoryMergeGroups(mappedMissing); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing mapped target error=%v", err)
	}
}

func TestHistoricalReplayAdditionalStoreErrorBranches(t *testing.T) {
	now := time.Date(2026, 8, 30, 17, 0, 0, 0, time.UTC)
	snapshot := historicalReplayCoverageSnapshot()
	loadErrorStore := NewStore(t.TempDir())
	loadErrorStore.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{}, errors.New("injected load failure")
	}
	if _, _, err := loadErrorStore.FreezeHistoricalDeduplicationReplay("owner/repo", snapshot); err == nil {
		t.Fatal("freeze load error hidden")
	}
	if _, _, _, err := loadErrorStore.AcquireHistoricalDeduplicationMerge("owner/repo", "lease", nil); err == nil {
		t.Fatal("acquire load error hidden")
	}
	if _, _, err := loadErrorStore.CompleteHistoricalDeduplicationMerge("owner/repo", "lease"); err == nil {
		t.Fatal("complete load error hidden")
	}
	if _, _, err := loadErrorStore.FailHistoricalDeduplicationReplay("owner/repo", "lease"); err == nil {
		t.Fatal("fail load error hidden")
	}
	if _, _, err := loadErrorStore.RetryHistoricalDeduplicationReplay("owner/repo"); err == nil {
		t.Fatal("retry load error hidden")
	}
	for name, replay := range map[string]HistoricalDeduplicationReplay{
		"inactive":  {},
		"completed": {Status: HistoricalDeduplicationCompleted, UpdatedAt: now},
		"merging": {
			Required: true, Status: HistoricalDeduplicationMerging,
			ProfileSnapshot: snapshot,
			MergeLease: HistoricalDeduplicationMergeLease{
				ID: "lease", Groups: []HistoricalDeduplicationMergeGroup{}, AcquiredAt: now,
			}, UpdatedAt: now,
		},
	} {
		t.Run("freeze "+name, func(t *testing.T) {
			branchStore := NewStore(t.TempDir())
			branchStore.loadForTest = func(string) (RepositoryState, error) {
				return RepositoryState{HistoricalDeduplication: replay}, nil
			}
			_, _, err := branchStore.FreezeHistoricalDeduplicationReplay("owner/repo", snapshot)
			if name == "merging" && !errors.Is(err, ErrHistoricalDeduplicationInProgress) {
				t.Fatalf("merging freeze error=%v", err)
			}
			if name != "merging" && err != nil {
				t.Fatalf("%s freeze error=%v", name, err)
			}
		})
	}

	quiescentStore := NewStore(t.TempDir())
	quiescentStore.now = func() time.Time { return now }
	quiescentStore.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{
			HistoricalDeduplication: HistoricalDeduplicationReplay{
				Required: true, Status: HistoricalDeduplicationReplaying, ProfileSnapshot: snapshot,
			},
			MappingJobs: []RepositoryMappingJob{{State: RepositoryMappingRunning}},
		}, nil
	}
	if _, _, _, err := quiescentStore.AcquireHistoricalDeduplicationMerge(
		"owner/repo", "lease", nil,
	); !errors.Is(err, ErrHistoricalDeduplicationNotQuiescent) {
		t.Fatalf("acquire quiescence error=%v", err)
	}
	quiescentStore.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{HistoricalDeduplication: HistoricalDeduplicationReplay{
			Required: true, Status: HistoricalDeduplicationReplaying, ProfileSnapshot: snapshot,
		}, RepositoryFindings: []RepositoryFinding{{ID: "target", Version: 1}}}, nil
	}
	staleGroups := []HistoricalDeduplicationMergeGroup{{Members: []HistoricalDeduplicationFindingVersion{
		{ID: "target", Version: 2}, {ID: "other", Version: 1},
	}}}
	if _, _, _, err := quiescentStore.AcquireHistoricalDeduplicationMerge(
		"owner/repo", "lease", staleGroups,
	); err == nil {
		t.Fatal("stale acquire targets accepted")
	}
	quiescentStore.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{HistoricalDeduplication: HistoricalDeduplicationReplay{
			Required: true, Status: HistoricalDeduplicationPending,
		}}, nil
	}
	if _, _, _, err := quiescentStore.AcquireHistoricalDeduplicationMerge(
		"owner/repo", "lease", nil,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong acquire state error=%v", err)
	}
	quiescentStore.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{HistoricalDeduplication: HistoricalDeduplicationReplay{
			Required: true, Status: HistoricalDeduplicationMerging,
			MergeLease: HistoricalDeduplicationMergeLease{
				ID: "lease", Groups: []HistoricalDeduplicationMergeGroup{{
					Members: []HistoricalDeduplicationFindingVersion{
						{ID: "missing", Version: 1}, {ID: "other", Version: 1},
					},
				}},
			},
		}}, nil
	}
	if _, _, err := quiescentStore.CompleteHistoricalDeduplicationMerge(
		"owner/repo", "lease",
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing complete target error=%v", err)
	}

	freezeRetryStore := NewStore(t.TempDir())
	freezeRetryStore.now = func() time.Time { return now }
	freezeRetryStore.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{
			HistoricalDeduplication: HistoricalDeduplicationReplay{
				Required: true, Status: HistoricalDeduplicationPending,
				Attempts: 1, UpdatedAt: now,
			},
			ValidationJobs: []RepositoryValidationJob{{State: RepositoryValidationPending}},
		}, nil
	}
	if _, _, err := freezeRetryStore.FreezeHistoricalDeduplicationReplay(
		"owner/repo", snapshot,
	); !errors.Is(err, ErrHistoricalDeduplicationNotQuiescent) {
		t.Fatalf("retry freeze quiescence error=%v", err)
	}
	freezeRetryStore.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{
			HistoricalDeduplication: HistoricalDeduplicationReplay{
				Required: true, Status: HistoricalDeduplicationPending,
				Attempts: 1, UpdatedAt: now,
			},
			RawFindings: []RawReviewFinding{{ID: "rrw_missing", LegacyFindingID: "legacy"}},
		}, nil
	}
	if _, _, err := freezeRetryStore.FreezeHistoricalDeduplicationReplay(
		"owner/repo", snapshot,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry freeze reset error=%v", err)
	}
}
