package repoaudit

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHistoricalReplayRetryMigratesProcessingIdentityAndMergesWorkflowDuplicates(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 8, 30, 19, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	commitSHA := strings.Repeat("a", 40)
	blobSHA := strings.Repeat("b", 40)
	state, first := recordLifecycleFinding(
		t, store, commitSHA, blobSHA, "wr_recovered_one",
		"main", "main", true, "same retained defect",
	)
	if len(state.Contexts) != 1 || len(state.Runs) != 1 {
		t.Fatalf("unexpected seed state contexts=%#v runs=%#v", state.Contexts, state.Runs)
	}
	campaignID := NewRepositoryReviewCampaignID()
	first.CampaignID = campaignID
	first.RepositoryFindingID = ""
	first.RepositoryMatchState = ""
	firstContext := state.Contexts[0]
	firstContext.CampaignID = campaignID
	secondContext := firstContext
	secondContext.ID = "rctx_recovered_second"
	secondContext.RunID = "wr_recovered_two"
	secondContext.CreatedAt = now.Add(time.Minute)
	second := first
	second.ID = "rfn_recovered_second"
	second.ContextIDs = []string{secondContext.ID}
	second.Observations = append([]FindingObservation(nil), first.Observations...)
	second.Observations[0].ContextID = secondContext.ID
	second.CreatedAt = now.Add(time.Minute)
	second.UpdatedAt = second.CreatedAt
	secondRun := state.Runs[0]
	secondRun.ID = secondContext.RunID
	secondRun.FindingIDs = []string{second.ID}
	secondRun.CompletedAt = now.Add(time.Minute)
	state.Findings = []Finding{first, second}
	state.Contexts = []FindingContext{firstContext, secondContext}
	state.Runs[0].FindingIDs = []string{first.ID}
	state.Runs = append(state.Runs[:1], secondRun)
	state.RawFindings = nil
	state.DeduplicatedFindings = nil
	state.DeduplicationJobs = nil
	state.MappingJobs = nil
	state.RepositoryFindings = nil
	state.NextDeduplicationOrdinal = 0
	state.FindingsProcessing = FindingsProcessingCounters{}
	state.CampaignHistory = map[string]string{campaignID: commitSHA}
	state.HistoricalDeduplication = HistoricalDeduplicationReplay{
		Required: true, Status: HistoricalDeduplicationPending, UpdatedAt: now,
	}
	state.Version++
	state.UpdatedAt = now
	if saveErr := store.save(&state); saveErr != nil {
		t.Fatal(saveErr)
	}
	snapshot := RepositoryReviewDeduplicationSnapshot{
		ReviewerModel: "reviewer", DeduplicationModel: "reviewer",
		SimilarityThreshold: 90, CandidateLimit: 4,
	}
	state, _, err := store.FreezeHistoricalDeduplicationReplay(state.Repository, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	state, admission, err := store.AdmitNextHistoricalDeduplicationBatch(state.Repository)
	if err != nil || admission.Admitted != 2 || len(state.RawFindings) != 2 {
		t.Fatalf("admission=%#v raws=%#v err=%v", admission, state.RawFindings, err)
	}
	for _, raw := range state.RawFindings {
		if raw.CampaignID != campaignID || !ValidRepositoryReviewCampaignID(raw.CampaignID) {
			t.Fatalf("recovered raw campaign=%#v", raw)
		}
	}
	provenance := make(map[string]RawReviewFinding, len(state.RawFindings))
	for _, raw := range state.RawFindings {
		provenance[raw.ID] = raw
	}
	for index := range state.RawFindings {
		raw := &state.RawFindings[index]
		raw.CampaignID = "wr_legacy_batch_" + string(rune('a'+index))
		raw.AdmissionBucket, err = DeduplicationAdmissionBucket(
			raw.CampaignID, raw.File, raw.Symbol,
		)
		if err != nil {
			t.Fatal(err)
		}
		raw.DiagnosisDigest = RawReviewFindingDiagnosisDigest(*raw)
		for jobIndex := range state.DeduplicationJobs {
			job := &state.DeduplicationJobs[jobIndex]
			if job.RawFindingID == raw.ID {
				job.AdmissionBucket = raw.AdmissionBucket
			}
		}
	}
	now = now.Add(time.Minute)
	stuckRaw := &state.RawFindings[0]
	stuckJob := &state.DeduplicationJobs[0]
	stuckJob.Attempts = DeduplicationAttemptLimit
	markDeduplicationFailed(stuckRaw, stuckJob, "processing_failed", now)
	state.DeduplicationJobs[1].Attempts = DeduplicationAttemptLimit
	markDeduplicationFailed(
		&state.RawFindings[1], &state.DeduplicationJobs[1], "processing_failed",
		state.RawFindings[1].CreatedAt,
	)
	state.HistoricalDeduplication.Status = HistoricalDeduplicationFailed
	state.HistoricalDeduplication.Error = "Historical deduplication failed."
	state.HistoricalDeduplication.FailurePhase = HistoricalDeduplicationFailureProcessing
	state.HistoricalDeduplication.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	reconcileFindingsProcessingCounters(&state)
	if saveErr := store.save(&state); saveErr != nil {
		t.Fatal(saveErr)
	}

	dependencies, err := HistoricalDeduplicationDependencies(state, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeVersion := state.Version
	_, _, err = store.ResumeHistoricalDeduplicationReplay(
		state.Repository, snapshot, dependencies,
	)
	if !errors.Is(err, ErrHistoricalDeduplicationRestartRequired) {
		t.Fatalf("incompatible resume error=%v", err)
	}
	unchanged, _, err := store.Get(state.Repository)
	if err != nil || unchanged.Version != beforeVersion {
		t.Fatalf("incompatible resume mutated state=%#v err=%v", unchanged, err)
	}
	now = now.Add(time.Minute)
	state, _, err = store.RestartHistoricalDeduplicationReplay(
		state.Repository,
		HistoricalDeduplicationRestartRequest{
			ProfileSnapshot: snapshot, Dependencies: dependencies,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for index, raw := range state.RawFindings {
		before := provenance[raw.ID]
		job := state.DeduplicationJobs[index]
		if raw.CampaignID != campaignID || !ValidRepositoryReviewCampaignID(raw.CampaignID) ||
			raw.AdmissionBucket != state.RawFindings[0].AdmissionBucket ||
			job.AdmissionBucket != raw.AdmissionBucket ||
			raw.DiagnosisDigest != RawReviewFindingDiagnosisDigest(raw) ||
			raw.State != RawFindingDeduplicationPending || job.State != DeduplicationJobPending ||
			job.Attempts != 0 || job.LeaseID != "" ||
			raw.LegacyFindingID != before.LegacyFindingID || raw.RunID != before.RunID ||
			raw.ContextID != before.ContextID || raw.Model != before.Model ||
			raw.Evidence != before.Evidence || raw.CreatedAt != before.CreatedAt {
			t.Fatalf("migrated raw=%#v job=%#v before=%#v", raw, job, before)
		}
	}
	processed, err := store.ProcessPendingDeduplicationJobs(
		t.Context(), state.Repository, DeduplicationProcessOptions{
			Score: func(
				_ context.Context,
				_ RepositoryReviewDeduplicationSnapshot,
				_ string,
				request DeduplicationScoringRequest,
			) (DeduplicationScoringResponse, error) {
				scores := make([]DeduplicationCandidateScore, 0, len(request.Candidates))
				for _, candidate := range request.Candidates {
					scores = append(scores, DeduplicationCandidateScore{
						CandidateID: candidate.ID, Score: 100,
						Explanation: "Same retained mechanism, trigger, invariant, and outcome.",
					})
				}
				return DeduplicationScoringResponse{Scores: scores}, nil
			},
			Judge: func(
				_ context.Context,
				_ RepositoryReviewDeduplicationSnapshot,
				_ string,
				request DeduplicationJudgeRequest,
			) (DeduplicationJudgment, error) {
				return DeduplicationJudgment{
					Decision: "duplicate", CandidateID: request.Candidates[0].OpaqueID,
				}, nil
			},
		},
	)
	if err != nil || processed.Completed != 2 || processed.Created != 1 ||
		processed.Duplicates != 1 {
		t.Fatalf("cross-workflow processing=%#v err=%v", processed, err)
	}
	state, _, err = store.Get(state.Repository)
	if err != nil || len(state.DeduplicatedFindings) != 1 ||
		len(state.DeduplicatedFindings[0].RawSourceIDs) != 2 {
		t.Fatalf("cross-workflow result=%#v err=%v", state.DeduplicatedFindings, err)
	}
}

func TestHistoricalReplaySyntheticCampaignPersistsProjectionWithoutRewritingLegacyProvenance(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 8, 30, 20, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	state, legacy := recordLifecycleFinding(
		t, store, strings.Repeat("c", 40), strings.Repeat("d", 40),
		"wr_orphaned_history", "main", "main", true, "orphaned retained defect",
	)
	originalContextID := legacy.ContextIDs[0]
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
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	snapshot := RepositoryReviewDeduplicationSnapshot{
		ReviewerModel: "reviewer", DeduplicationModel: "reviewer",
		SimilarityThreshold: 90, CandidateLimit: 4,
	}
	state, _, err := store.FreezeHistoricalDeduplicationReplay(state.Repository, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	state, admission, err := store.AdmitNextHistoricalDeduplicationBatch(state.Repository)
	if err != nil || admission.Admitted != 1 || len(admission.RawFindings) != 1 {
		t.Fatalf("synthetic admission=%#v err=%v", admission, err)
	}
	raw := admission.RawFindings[0]
	if !ValidRepositoryReviewCampaignID(raw.CampaignID) ||
		state.CampaignHistory[raw.CampaignID] != raw.CommitSHA ||
		raw.ContextID == originalContextID || raw.AssignmentID != historicalReplayAssignmentID {
		t.Fatalf("synthetic processing identity raw=%#v history=%#v", raw, state.CampaignHistory)
	}
	legacyIndex := findingIndexByID(state.Findings, legacy.ID)
	originalContextIndex := -1
	rawContextIndex := -1
	for index := range state.Contexts {
		switch state.Contexts[index].ID {
		case originalContextID:
			originalContextIndex = index
		case raw.ContextID:
			rawContextIndex = index
		}
	}
	if legacyIndex < 0 || originalContextIndex < 0 || rawContextIndex < 0 ||
		state.Findings[legacyIndex].CampaignID != "" ||
		state.Contexts[originalContextIndex].CampaignID != "" ||
		state.Contexts[rawContextIndex].CampaignID != raw.CampaignID ||
		state.Contexts[rawContextIndex].InventoryHash != "historical-replay" {
		t.Fatalf("legacy provenance was rewritten: finding=%#v contexts=%#v", state.Findings, state.Contexts)
	}
	processed, err := store.ProcessPendingDeduplicationJobs(
		t.Context(), state.Repository, DeduplicationProcessOptions{},
	)
	if err != nil || processed.Completed != 1 || processed.Created != 1 {
		t.Fatalf("synthetic processing=%#v err=%v", processed, err)
	}
	state, _, err = store.Get(state.Repository)
	if err != nil || len(state.DeduplicatedFindings) != 1 ||
		state.DeduplicatedFindings[0].CampaignID != raw.CampaignID ||
		len(state.DeduplicatedFindings[0].RawSourceIDs) != 1 {
		t.Fatalf("synthetic persisted projection=%#v err=%v", state.DeduplicatedFindings, err)
	}
}

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
	if err != nil || pending.Status != HistoricalDeduplicationReplaying ||
		pending.ProfileSnapshot.ProfileVersion != 3 {
		t.Fatalf("retry=%#v err=%v", pending, err)
	}
	// Merge resume preserves the frozen profile and every model result, while
	// the first attempt's merge versions are stale after issue synchronization.
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
