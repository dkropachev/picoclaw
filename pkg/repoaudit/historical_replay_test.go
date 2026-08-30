package repoaudit

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHistoricalDeduplicationReplayBatchesAreOldestFirstAndBatchScoped(t *testing.T) {
	base := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	state := RepositoryState{
		Contexts: []FindingContext{
			{ID: "context-two", RunID: "workflow-two"},
			{ID: "context-one", RunID: "workflow-one"},
		},
		Findings: []Finding{
			{ID: "later-in-campaign", CampaignID: "rrc_campaign", CreatedAt: base.Add(4 * time.Minute)},
			{ID: "workflow-two-finding", ContextIDs: []string{"context-two"}, CreatedAt: base.Add(2 * time.Minute)},
			{ID: "first-in-campaign", CampaignID: "rrc_campaign", CreatedAt: base.Add(time.Minute)},
			{ID: "workflow-one-finding", ContextIDs: []string{"context-one"}, CreatedAt: base},
			{ID: "orphan", CreatedAt: base.Add(3 * time.Minute)},
		},
	}
	batches := HistoricalDeduplicationReplayBatches(state)
	if len(batches) != 4 {
		t.Fatalf("batches=%#v", batches)
	}
	if got := []string{
		batches[0].BoundaryID, batches[1].BoundaryID,
		batches[2].BoundaryID, batches[3].BoundaryID,
	}; !slices.Equal(got, []string{
		"workflow-one", "rrc_campaign", "workflow-two", "legacy:orphan",
	}) {
		t.Fatalf("boundary order=%#v", got)
	}
	if !slices.Equal(
		batches[1].FindingIDs,
		[]string{"first-in-campaign", "later-in-campaign"},
	) {
		t.Fatalf("campaign order=%#v", batches[1].FindingIDs)
	}
}

func TestRepositoryMappingAdmissionRequiresDeduplicatedFinding(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 30, 0, 0, time.UTC)
	state := RepositoryState{
		Findings:    []Finding{{ID: "projection"}},
		RawFindings: []RawReviewFinding{{ID: "raw-undecided"}},
	}
	if created := ensureMappingJobsForFindings(&state, []string{"projection"}, now); created != 0 ||
		len(state.MappingJobs) != 0 {
		t.Fatalf("undecided raw entered mapping: created=%d jobs=%#v", created, state.MappingJobs)
	}
	state.RawFindings = nil
	state.HistoricalDeduplication.Required = true
	if created := ensureMappingJobsForFindings(&state, []string{"projection"}, now); created != 0 ||
		len(state.MappingJobs) != 0 {
		t.Fatalf("migration marker admitted a legacy mapping: created=%d jobs=%#v", created, state.MappingJobs)
	}
	state.DeduplicatedFindings = []DeduplicatedReviewFinding{{ID: "projection"}}
	if created := ensureMappingJobsForFindings(&state, []string{"projection"}, now); created != 1 ||
		len(state.MappingJobs) != 1 || state.MappingJobs[0].ReviewFindingID != "projection" {
		t.Fatalf("deduplicated finding was not mapped: created=%d jobs=%#v", created, state.MappingJobs)
	}
}

func TestRawFindingContextRemainsReadableWhenLegacyProjectionsArePruned(t *testing.T) {
	state := RepositoryState{
		Contexts:    []FindingContext{{ID: "raw-context"}, {ID: "unused-context"}},
		RawFindings: []RawReviewFinding{{ContextID: "raw-context"}},
	}
	pruneUnreferencedFindingContexts(&state)
	if len(state.Contexts) != 1 || state.Contexts[0].ID != "raw-context" {
		t.Fatalf("raw context was pruned: %#v", state.Contexts)
	}
}

func TestHistoricalReplayAdmitsOnlyOldestIncompleteBatch(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 8, 29, 12, 45, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	state, first := recordLifecycleFinding(
		t, store, strings.Repeat("4", 40), strings.Repeat("d", 40), "replay-one",
		"main", "main", true, "oldest replay finding",
	)
	now = now.Add(time.Minute)
	state, second := recordLifecycleFinding(
		t, store, strings.Repeat("5", 40), strings.Repeat("e", 40), "replay-two",
		"main", "main", true, "later replay finding", MatchHints{
			Component: "cache", Operation: "evict", FailureMode: "stale entry",
			Trigger: "generation rollover", ViolatedInvariant: "old generations are unreachable",
			ObservableOutcome: "a stale value is returned", RelatedSymbols: []string{"Cache.Evict"},
			SourceAnchors: []string{"generation"}, DistinguishingFacts: []string{"requires rollover"},
		},
	)
	// recordLifecycleFinding uses the current gate. Strip those new resources
	// to construct the pre-v4 historical ledger this replay test exercises.
	state.RawFindings = nil
	state.DeduplicatedFindings = nil
	state.DeduplicationJobs = nil
	state.MappingJobs = nil
	state.NextDeduplicationOrdinal = 0
	state.FindingsProcessing = FindingsProcessingCounters{}
	state.HistoricalDeduplication = HistoricalDeduplicationReplay{
		Required: true, Status: HistoricalDeduplicationPending, UpdatedAt: now,
	}
	state.Version++
	state.UpdatedAt = now
	if saveErr := store.save(&state); saveErr != nil {
		t.Fatal(saveErr)
	}
	state, replay, err := store.FreezeHistoricalDeduplicationReplay(
		state.Repository,
		RepositoryReviewDeduplicationSnapshot{
			ProfileID: "rrpf_history", ProfileVersion: 1,
			ReviewerModel: "reviewer", DeduplicationModel: "reviewer",
			SimilarityThreshold: 90, CandidateLimit: 4,
		},
	)
	if err != nil || replay.Status != HistoricalDeduplicationReplaying {
		t.Fatalf("freeze=%#v err=%v", replay, err)
	}
	state, admission, err := store.AdmitNextHistoricalDeduplicationBatch(state.Repository)
	if err != nil || admission.Admitted != 1 || admission.Batch.BoundaryID != "replay-one" ||
		len(admission.RawFindings) != 1 || admission.RawFindings[0].LegacyFindingID != first.ID ||
		len(state.RawFindings) != 1 || state.RawFindings[0].DiagnosisDigest == "" {
		t.Fatalf("first admission=%#v raws=%#v err=%v", admission, state.RawFindings, err)
	}
	state, waiting, err := store.AdmitNextHistoricalDeduplicationBatch(state.Repository)
	if err != nil || waiting.Admitted != 0 || waiting.Batch.BoundaryID != "replay-one" ||
		waiting.AllComplete || len(state.RawFindings) != 1 || second.ID == first.ID {
		t.Fatalf("serialized admission=%#v raws=%#v err=%v", waiting, state.RawFindings, err)
	}
}

func TestHistoricalReplayDerivesRepositoryMergeGroupsFromRawSources(t *testing.T) {
	state := RepositoryState{
		Findings: []Finding{
			{ID: "legacy-one", RepositoryFindingID: "repository-one"},
			{ID: "legacy-two", RepositoryFindingID: "repository-two"},
		},
		RawFindings: []RawReviewFinding{
			{
				ID: "rrw_raw-one", LegacyFindingID: "legacy-one",
				State: RawFindingDeduplicationCompleted, DeduplicatedFindingID: "dedup",
			},
			{
				ID: "rrw_raw-two", LegacyFindingID: "legacy-two",
				State: RawFindingDeduplicationCompleted, DeduplicatedFindingID: "dedup",
			},
		},
		DeduplicatedFindings: []DeduplicatedReviewFinding{{
			ID: "dedup", RawSourceIDs: []string{"rrw_raw-one", "rrw_raw-two"},
		}},
		RepositoryFindings: []RepositoryFinding{
			{ID: "repository-one", Version: 3},
			{ID: "repository-two", Version: 7},
		},
	}
	groups, err := HistoricalDeduplicationRepositoryMergeGroups(state)
	if err != nil || len(groups) != 1 || !slices.Equal(
		groups[0].Members,
		[]HistoricalDeduplicationFindingVersion{
			{ID: "repository-one", Version: 3},
			{ID: "repository-two", Version: 7},
		},
	) {
		t.Fatalf("groups=%#v err=%v", groups, err)
	}
	state.RawFindings[1].State = RawFindingDeduplicationFailed
	if _, err := HistoricalDeduplicationRepositoryMergeGroups(state); !errors.Is(
		err, ErrHistoricalDeduplicationNotQuiescent,
	) {
		t.Fatalf("incomplete replay merge groups error=%v", err)
	}
}

func TestHistoricalReplayMergeGroupsIncludeMappedMixedTarget(t *testing.T) {
	state := RepositoryState{
		Findings: []Finding{{ID: "legacy", RepositoryFindingID: "historical-repository"}},
		RawFindings: []RawReviewFinding{{
			ID: "rrw_historical", LegacyFindingID: "legacy",
			State: RawFindingDeduplicationCompleted, DeduplicatedFindingID: "dedup",
		}},
		DeduplicatedFindings: []DeduplicatedReviewFinding{{
			ID: "dedup", RawSourceIDs: []string{"rrw_historical"},
			RepositoryFindingID: "live-repository",
		}},
		RepositoryFindings: []RepositoryFinding{
			{ID: "historical-repository", Version: 2},
			{ID: "live-repository", Version: 3},
		},
	}
	groups, err := HistoricalDeduplicationRepositoryMergeGroups(state)
	if err != nil || len(groups) != 1 || !slices.Equal(
		groups[0].Members,
		[]HistoricalDeduplicationFindingVersion{
			{ID: "historical-repository", Version: 2},
			{ID: "live-repository", Version: 3},
		},
	) {
		t.Fatalf("mapped mixed merge groups=%#v err=%v", groups, err)
	}
}

func TestHistoricalReplayAssociatesDeduplicatedOccurrenceWithoutAnotherModelCall(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	state := RepositoryState{
		Findings: []Finding{
			{ID: "legacy", RepositoryFindingID: "repository", RepositoryMatchState: RepositoryMatchNew},
			{
				ID: "dedup", Repository: "owner/repo", CommitSHA: strings.Repeat("a", 40),
				File: FileRef{Path: "main.go"}, Title: "defect", Severity: "high",
				Status: FindingOpen, Version: 1, CreatedAt: now, UpdatedAt: now,
			},
		},
		RawFindings: []RawReviewFinding{{
			ID: "rrw_historical", LegacyFindingID: "legacy",
			State: RawFindingDeduplicationCompleted,
		}},
		DeduplicatedFindings: []DeduplicatedReviewFinding{{
			ID: "dedup", Version: 1, RawSourceIDs: []string{"rrw_historical"},
		}},
		RepositoryFindings: []RepositoryFinding{{
			ID: "repository", Repository: "owner/repo", MatchState: RepositoryMatchNew,
			CanonicalTitle: "defect", CanonicalSeverity: "high", Version: 1,
		}},
		MappingJobs: []RepositoryMappingJob{{
			ID: mappingJobID("dedup"), ReviewFindingID: "dedup", State: RepositoryMappingPending,
		}},
	}
	associateHistoricalDeduplicatedFindings(&state, now)
	if state.Findings[1].RepositoryFindingID != "repository" ||
		state.DeduplicatedFindings[0].RepositoryFindingID != "repository" ||
		state.MappingJobs[0].State != RepositoryMappingCompleted ||
		state.MappingJobs[0].RepositoryFindingID != "repository" ||
		!containsExactString(state.RepositoryFindings[0].ReviewFindingIDs, "dedup") {
		t.Fatalf("historical direct association = %#v", state)
	}
}

func TestHistoricalLifecycleMergeKeepsActionableState(t *testing.T) {
	tests := []struct {
		left, right RepositoryFindingLifecycle
		want        RepositoryFindingLifecycle
	}{
		{RepositoryFindingResolved, RepositoryFindingOpen, RepositoryFindingOpen},
		{RepositoryFindingResolved, RepositoryFindingResolutionPending, RepositoryFindingResolutionPending},
		{RepositoryFindingResolved, RepositoryFindingRegressed, RepositoryFindingRegressed},
		{RepositoryFindingDismissed, RepositoryFindingResolved, RepositoryFindingDismissed},
	}
	for _, test := range tests {
		if got := mergeHistoricalRepositoryFindingLifecycle(test.left, test.right); got != test.want {
			t.Errorf("merge lifecycle %q + %q = %q, want %q", test.left, test.right, got, test.want)
		}
	}
}

func TestHistoricalReplayRetryResetsModelWorkToNewSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	oldSnapshot := RepositoryReviewDeduplicationSnapshot{
		ProfileID: "rrpf_old", ProfileVersion: 1, ReviewerModel: "old",
		DeduplicationModel: "old", SimilarityThreshold: 90, CandidateLimit: 4,
	}
	newSnapshot := oldSnapshot
	newSnapshot.ProfileID = "rrpf_new"
	newSnapshot.ProfileVersion = 2
	newSnapshot.ReviewerModel = "new"
	newSnapshot.DeduplicationModel = "new"
	state := RepositoryState{
		Findings: []Finding{{ID: "legacy"}, {ID: "dedup"}},
		RawFindings: []RawReviewFinding{{
			ID: "rrw_historical", LegacyFindingID: "legacy",
			State:       RawFindingDeduplicationCompleted,
			Disposition: RawFindingDispositionNew, DeduplicatedFindingID: "dedup",
		}},
		DeduplicatedFindings: []DeduplicatedReviewFinding{{
			ID: "dedup", RawSourceIDs: []string{"rrw_historical"},
		}},
		DeduplicationJobs: []DeduplicationJob{{
			ID: "job", RawFindingID: "rrw_historical", State: DeduplicationJobCompleted,
			ModelSnapshot: oldSnapshot, Decision: DeduplicationJudgment{Decision: "new"},
		}},
		MappingJobs: []RepositoryMappingJob{{ReviewFindingID: "dedup"}},
	}
	if err := resetHistoricalDeduplicationModelWork(&state, newSnapshot, now); err != nil {
		t.Fatal(err)
	}
	if len(state.DeduplicatedFindings) != 0 || len(state.MappingJobs) != 0 ||
		len(state.Findings) != 1 || state.RawFindings[0].State != RawFindingDeduplicationPending ||
		state.RawFindings[0].DeduplicatedFindingID != "" ||
		state.DeduplicationJobs[0].State != DeduplicationJobPending ||
		state.DeduplicationJobs[0].ModelSnapshot != newSnapshot {
		t.Fatalf("historical retry state = %#v", state)
	}
}

func TestHistoricalReplayRetrySeparatesMixedLiveSources(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	snapshot := RepositoryReviewDeduplicationSnapshot{
		ProfileID: "rrpf_new", ProfileVersion: 2, ReviewerModel: "new",
		DeduplicationModel: "new", SimilarityThreshold: 90, CandidateLimit: 4,
	}
	file := FileRef{Path: "main.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 1}
	historical := RawReviewFinding{
		ID: "rrw_historical", LegacyFindingID: "legacy", CampaignID: "rrc_campaign",
		AdmissionBucket: "bucket", InsertionOrdinal: 1, Repository: "owner/repo",
		CommitSHA: strings.Repeat("b", 40), File: file, Severity: "high", Title: "historical",
		Evidence: "evidence", Impact: "impact",
		Validation: Validation{Status: "confirmed", Summary: "confirmed"},
		ContextID:  "context", RunID: "run", AssignmentID: "historical-replay", Model: "old",
		State: RawFindingDeduplicationCompleted, Disposition: RawFindingDispositionNew,
		DeduplicatedFindingID: "mixed", CreatedAt: now, UpdatedAt: now,
	}
	historical.DiagnosisDigest = RawReviewFindingDiagnosisDigest(historical)
	live := historical
	live.ID = "rrf_live"
	live.LegacyFindingID = "live-projection"
	live.InsertionOrdinal = 2
	live.Title = "live"
	live.AssignmentID = "live"
	live.Model = "live-model"
	live.Disposition = RawFindingDispositionDuplicate
	live.DiagnosisDigest = RawReviewFindingDiagnosisDigest(live)
	state := RepositoryState{
		Findings: []Finding{
			{ID: "legacy", RepositoryFindingID: "historical-repository"},
			{ID: "mixed", RepositoryFindingID: "live-repository", RepositoryMatchState: RepositoryMatchNew},
		},
		RawFindings: []RawReviewFinding{historical, live},
		DeduplicatedFindings: []DeduplicatedReviewFinding{{
			ID: "mixed", RawSourceIDs: []string{historical.ID, live.ID},
			RepositoryFindingID: "live-repository", RepositoryMatchState: RepositoryMatchNew,
		}},
		RepositoryFindings: []RepositoryFinding{
			{ID: "historical-repository", Version: 1, ReviewFindingIDs: []string{"legacy"}},
			{
				ID: "live-repository", Version: 1, ReviewFindingIDs: []string{"mixed"},
				PathSymbolHistory: []RepositoryFindingPathSymbol{{ReviewFindingID: "mixed"}},
			},
		},
		DeduplicationJobs: []DeduplicationJob{
			{
				ID: "historical-job", RawFindingID: historical.ID, State: DeduplicationJobCompleted,
				InsertionOrdinal: 1, Decision: DeduplicationJudgment{Decision: "new"},
			},
			{
				ID: "live-job", RawFindingID: live.ID, State: DeduplicationJobCompleted,
				InsertionOrdinal: 2, Decision: DeduplicationJudgment{Decision: "duplicate", CandidateID: "mixed"},
			},
		},
		MappingJobs: []RepositoryMappingJob{{
			ID: mappingJobID("mixed"), ReviewFindingID: "mixed",
			State: RepositoryMappingCompleted, RepositoryFindingID: "live-repository",
		}},
		Runs: []ReviewRun{{ID: "live-run", FindingIDs: []string{"mixed"}}},
	}
	if err := resetHistoricalDeduplicationModelWork(&state, snapshot, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(state.DeduplicatedFindings) != 1 ||
		!slices.Equal(state.DeduplicatedFindings[0].RawSourceIDs, []string{live.ID}) ||
		state.DeduplicatedFindings[0].ID == "mixed" ||
		state.RawFindings[0].State != RawFindingDeduplicationPending ||
		state.RawFindings[0].DeduplicatedFindingID != "" ||
		state.RawFindings[1].DeduplicatedFindingID != state.DeduplicatedFindings[0].ID ||
		state.DeduplicationJobs[0].ModelSnapshot != snapshot ||
		state.DeduplicationJobs[1].State != DeduplicationJobCompleted ||
		len(state.MappingJobs) != 1 ||
		state.MappingJobs[0].ReviewFindingID != state.DeduplicatedFindings[0].ID ||
		state.MappingJobs[0].State != RepositoryMappingCompleted ||
		state.DeduplicatedFindings[0].RepositoryFindingID != "live-repository" ||
		state.RepositoryFindings[1].ReviewFindingIDs[0] != state.DeduplicatedFindings[0].ID ||
		state.RepositoryFindings[1].PathSymbolHistory[0].ReviewFindingID !=
			state.DeduplicatedFindings[0].ID ||
		state.Runs[0].FindingIDs[0] != state.DeduplicatedFindings[0].ID {
		t.Fatalf("mixed replay retry state = %#v", state)
	}
}

func TestHistoricalDeduplicationNarrowMergeFenceAndRetry(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	state, first := recordLifecycleFinding(
		t, store, strings.Repeat("1", 40), strings.Repeat("a", 40), "historical-one",
		"main", "main", true, "first historical defect",
	)
	firstJob := lifecycleJobForFinding(t, state, first.ID)
	claimed := claimLifecycleMappingJob(
		t, store, state.Repository, firstJob,
		RepositoryMappingModelSnapshot{Model: "reviewer", Account: "account"},
	)
	state, firstRepositoryFinding, err := store.CompleteMappingJob(
		state.Repository,
		RepositoryMappingCompletion{
			JobID: claimed.ID, CreateMatchState: RepositoryMatchNew,
			DefaultBranchVerified: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	state, second := recordLifecycleFinding(
		t, store, strings.Repeat("2", 40), strings.Repeat("b", 40), "historical-two",
		"main", "main", true, "second historical defect", MatchHints{
			Component: "allocator", Operation: "reserve slot", FailureMode: "double allocation",
			Trigger: "concurrent admission", ViolatedInvariant: "one owner per slot",
			ObservableOutcome: "two workers share a slot", RelatedSymbols: []string{"Allocator.Reserve"},
			SourceAnchors: []string{"slot owner"}, DistinguishingFacts: []string{"requires concurrency"},
		},
	)
	secondJob := lifecycleJobForFinding(t, state, second.ID)
	claimed = claimLifecycleMappingJob(
		t, store, state.Repository, secondJob,
		RepositoryMappingModelSnapshot{Model: "reviewer", Account: "account"},
	)
	state, secondRepositoryFinding, err := store.CompleteMappingJob(
		state.Repository,
		RepositoryMappingCompletion{
			JobID: claimed.ID, CreateMatchState: RepositoryMatchNew,
			DefaultBranchVerified: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for index := range state.RepositoryFindings {
		finding := &state.RepositoryFindings[index]
		switch finding.ID {
		case firstRepositoryFinding.ID:
			finding.Issue = RepositoryFindingIssueAssociation{
				ExternalID: "1", URL: "https://github.com/owner/repo/issues/1",
				State: RepositoryFindingIssueOpen,
			}
		case secondRepositoryFinding.ID:
			finding.Issue = RepositoryFindingIssueAssociation{
				ExternalID: "2", URL: "https://github.com/owner/repo/issues/2",
				State: RepositoryFindingIssueOpen,
			}
		}
	}
	state.HistoricalDeduplication = HistoricalDeduplicationReplay{
		Required: true, Status: HistoricalDeduplicationReplaying,
		ProfileSnapshot: RepositoryReviewDeduplicationSnapshot{
			ProfileID: "rrpf_history", ProfileVersion: 3,
			ReviewerModel: "reviewer", DeduplicationModel: "reviewer",
			SimilarityThreshold: 90, CandidateLimit: 4,
		},
		SnapshotVersion: state.Version, Attempts: 1, UpdatedAt: now,
	}
	state.Version++
	state.UpdatedAt = now
	if saveErr := store.save(&state); saveErr != nil {
		t.Fatal(saveErr)
	}
	groups := []HistoricalDeduplicationMergeGroup{{Members: []HistoricalDeduplicationFindingVersion{
		{ID: secondRepositoryFinding.ID, Version: secondRepositoryFinding.Version},
		{ID: firstRepositoryFinding.ID, Version: firstRepositoryFinding.Version},
	}}}
	state, replay, acquired, err := store.AcquireHistoricalDeduplicationMerge(
		state.Repository, "merge-one", groups,
	)
	if err != nil || !acquired || replay.Status != HistoricalDeduplicationMerging ||
		!HistoricalDeduplicationMergeInProgress(state) {
		t.Fatalf("acquire=%v replay=%#v err=%v", acquired, replay, err)
	}
	if _, found, readErr := store.Get(state.Repository); readErr != nil || !found {
		t.Fatalf("read during merge found=%v err=%v", found, readErr)
	}
	now = now.Add(time.Minute)
	liveState, liveFinding := recordLifecycleFinding(
		t, store, strings.Repeat("3", 40), strings.Repeat("c", 40), "live-during-merge",
		"main", "main", true, "live finding during historical merge", MatchHints{
			Component: "queue", Operation: "drain", FailureMode: "dropped item",
			Trigger: "concurrent close", ViolatedInvariant: "accepted items are drained",
			ObservableOutcome: "an accepted item is lost", RelatedSymbols: []string{"Queue.Drain"},
			SourceAnchors: []string{"drain cursor"}, DistinguishingFacts: []string{"requires close"},
		},
	)
	if liveFinding.ID == "" || !HistoricalDeduplicationMergeInProgress(liveState) {
		t.Fatalf("live review was not admitted during merge: %#v", liveState.HistoricalDeduplication)
	}
	if _, _, mutationErr := store.SetRepositoryFindingLifecycle(
		state.Repository, firstRepositoryFinding.ID, RepositoryFindingOpen,
		firstRepositoryFinding.Version,
	); !errors.Is(mutationErr, ErrHistoricalDeduplicationInProgress) {
		t.Fatalf("mutation fence=%v", mutationErr)
	}
	failed, failedReplay, err := store.FailHistoricalDeduplicationReplay(
		state.Repository, "merge-one",
	)
	if err != nil || failedReplay.Status != HistoricalDeduplicationFailed ||
		HistoricalDeduplicationMergeInProgress(failed) {
		t.Fatalf("failure release=%#v err=%v", failedReplay, err)
	}
	_, refreshed, mutationErr := store.UpdateRepositoryFindingIssueSnapshot(
		state.Repository, RepositoryIssueSnapshotUpdate{
			RepositoryFindingID: firstRepositoryFinding.ID,
			ExpectedVersion:     firstRepositoryFinding.Version,
			ExternalID:          "1",
			URL:                 "https://github.com/owner/repo/issues/1",
			State:               RepositoryFindingIssueOpen,
			SnapshotAt:          now,
		},
	)
	if mutationErr != nil {
		t.Fatalf("mutation after release=%v", mutationErr)
	}
	if refreshed.Version == firstRepositoryFinding.Version {
		t.Fatal("intervening issue snapshot did not advance the target version")
	}
	retried, pending, err := store.RetryHistoricalDeduplicationReplay(state.Repository)
	if err != nil || pending.Status != HistoricalDeduplicationPending ||
		pending.ProfileSnapshot != (RepositoryReviewDeduplicationSnapshot{}) {
		t.Fatalf("retry=%#v err=%v", pending, err)
	}
	retried, replay, err = store.FreezeHistoricalDeduplicationReplay(
		retried.Repository,
		RepositoryReviewDeduplicationSnapshot{
			ProfileID: "rrpf_history", ProfileVersion: 4,
			ReviewerModel: "reviewer-v2", DeduplicationModel: "reviewer-v2",
			SimilarityThreshold: 90, CandidateLimit: 4,
		},
	)
	if err != nil || replay.ProfileSnapshot.ProfileVersion != 4 {
		t.Fatalf("fresh snapshot=%#v err=%v", replay, err)
	}
	// The retry re-snapshotted the profile, but the first attempt's merge
	// versions are stale after the intervening issue synchronization.
	if _, _, _, acquireErr := store.AcquireHistoricalDeduplicationMerge(
		state.Repository, "merge-stale", groups,
	); !errors.Is(acquireErr, ErrConflict) {
		t.Fatalf("stale replay unexpectedly reacquired=%v", acquireErr)
	}
	freshFirst := retried.RepositoryFindings[repositoryFindingIndexByID(
		retried.RepositoryFindings, firstRepositoryFinding.ID,
	)]
	freshSecond := retried.RepositoryFindings[repositoryFindingIndexByID(
		retried.RepositoryFindings, secondRepositoryFinding.ID,
	)]
	freshGroups := []HistoricalDeduplicationMergeGroup{{Members: []HistoricalDeduplicationFindingVersion{
		{ID: freshSecond.ID, Version: freshSecond.Version},
		{ID: freshFirst.ID, Version: freshFirst.Version},
	}}}
	_, _, acquired, err = store.AcquireHistoricalDeduplicationMerge(
		retried.Repository, "merge-two", freshGroups,
	)
	if err != nil || !acquired {
		t.Fatalf("fresh acquire=%v err=%v", acquired, err)
	}
	mergedState, completed, err := store.CompleteHistoricalDeduplicationMerge(
		retried.Repository, "merge-two",
	)
	if err != nil || completed.Required || completed.Status != HistoricalDeduplicationCompleted ||
		len(mergedState.RepositoryFindings) != 1 {
		t.Fatalf("merge=%#v replay=%#v err=%v", mergedState.RepositoryFindings, completed, err)
	}
	merged := mergedState.RepositoryFindings[0]
	if merged.ID != firstRepositoryFinding.ID || !merged.Issue.Conflict ||
		!slices.Equal(merged.Issue.ConflictURLs, []string{
			"https://github.com/owner/repo/issues/1",
			"https://github.com/owner/repo/issues/2",
		}) || len(merged.ReviewFindingIDs) != 2 {
		t.Fatalf("merged identity/history=%#v", merged)
	}
}
