package repoaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func seedHistoricalCheckpointSources(
	t *testing.T,
	symbols ...string,
) (Store, RepositoryState, RepositoryReviewDeduplicationSnapshot) {
	t.Helper()
	store := NewStore(t.TempDir())
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	commitSHA := strings.Repeat("a", 40)
	blobSHA := strings.Repeat("b", 40)
	state, seed := recordLifecycleFinding(
		t, store, commitSHA, blobSHA, "wr_checkpoint_resume",
		"main", "main", true, "checkpoint source",
	)
	campaignID := NewRepositoryReviewCampaignID()
	baseContext := state.Contexts[0]
	baseContext.CampaignID = campaignID
	findings := make([]Finding, 0, len(symbols))
	contexts := make([]FindingContext, 0, len(symbols))
	for index, symbol := range symbols {
		contextRecord := baseContext
		contextRecord.ID = fmt.Sprintf("rctx_checkpoint_%d", index)
		contexts = append(contexts, contextRecord)
		finding := seed
		finding.ID = fmt.Sprintf("rfn_checkpoint_%d", index)
		finding.CampaignID = campaignID
		finding.Symbol = symbol
		finding.RepositoryFindingID = ""
		finding.RepositoryMatchState = ""
		finding.IssueDraftID = ""
		finding.ContextIDs = []string{contextRecord.ID}
		finding.Observations = append([]FindingObservation(nil), seed.Observations...)
		for observationIndex := range finding.Observations {
			finding.Observations[observationIndex].ContextID = contextRecord.ID
			finding.Observations[observationIndex].Symbol = symbol
		}
		finding.CreatedAt = now
		finding.UpdatedAt = finding.CreatedAt
		findings = append(findings, finding)
	}
	state.Findings = findings
	state.Contexts = contexts
	state.Runs[0].FindingIDs = make([]string, 0, len(findings))
	for _, finding := range findings {
		state.Runs[0].FindingIDs = append(state.Runs[0].FindingIDs, finding.ID)
	}
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
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	snapshot := historicalReplayCoverageSnapshot()
	state, _, err := store.FreezeHistoricalDeduplicationReplay(state.Repository, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	state, admission, err := store.AdmitNextHistoricalDeduplicationBatch(state.Repository)
	if err != nil || admission.Admitted != len(symbols) {
		t.Fatalf("admission=%#v err=%v", admission, err)
	}
	processingAt := now.Add(time.Duration(len(symbols)+1) * time.Minute)
	store.now = func() time.Time { return processingAt }
	return store, state, snapshot
}

func TestHistoricalCheckpointResumePreservesCompletedPrefix(t *testing.T) {
	store, state, snapshot := seedHistoricalCheckpointSources(
		t, "Checkpoint.Run", "Checkpoint.Run",
	)
	_, claim, claimed, err := store.ClaimDeduplicationJob(
		state.Repository, state.DeduplicationJobs[0].ID, time.Minute,
	)
	if err != nil || !claimed {
		t.Fatalf("claim=%#v claimed=%v err=%v", claim, claimed, err)
	}
	state, _, _, err = store.CompleteDeduplicationJob(
		state.Repository,
		DeduplicationCompletion{
			JobID: claim.Job.ID, LeaseID: claim.Job.LeaseID,
			CandidateUniverseDigest: claim.UniverseDigest,
			Decision:                DeduplicationJudgment{Decision: "new"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	failedRaw := &state.RawFindings[1]
	failedJob := &state.DeduplicationJobs[1]
	failedJob.Attempts = DeduplicationAttemptLimit
	markDeduplicationFailed(failedRaw, failedJob, "processing_failed", store.clock())
	failedJob.CandidateUniverseDigest = "checkpoint-universe"
	failedJob.CandidateVersions = []DeduplicationCandidateVersion{{
		CandidateID: state.DeduplicatedFindings[0].ID,
		Version:     state.DeduplicatedFindings[0].Version,
	}}
	failedJob.ShortlistedScores = []DeduplicationCandidateScore{{
		CandidateID: state.DeduplicatedFindings[0].ID,
		Score:       99,
		Explanation: "Same retained mechanism and outcome.",
	}}
	failedJob.Decision = DeduplicationJudgment{
		Decision: "duplicate", CandidateID: state.DeduplicatedFindings[0].ID,
	}
	state.HistoricalDeduplication.Status = HistoricalDeduplicationFailed
	state.HistoricalDeduplication.FailurePhase = HistoricalDeduplicationFailureProcessing
	state.HistoricalDeduplication.Error = "Historical deduplication failed."
	state.HistoricalDeduplication.UpdatedAt = store.clock()
	state.Version++
	state.UpdatedAt = store.clock()
	reconcileFindingsProcessingCounters(&state)
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	completedCheckpoint, _ := json.Marshal(struct {
		Raw          RawReviewFinding
		Job          DeduplicationJob
		Deduplicated []DeduplicatedReviewFinding
		Findings     []Finding
		Mappings     []RepositoryMappingJob
	}{
		Raw: state.RawFindings[0], Job: state.DeduplicationJobs[0],
		Deduplicated: state.DeduplicatedFindings,
		Findings:     state.Findings, Mappings: state.MappingJobs,
	})
	failedOrdinal := failedJob.InsertionOrdinal
	failedCampaign := failedRaw.CampaignID
	failedBucket := failedRaw.AdmissionBucket
	failedSnapshot := failedJob.ModelSnapshot

	resumed, replay, err := store.RetryHistoricalDeduplicationReplay(state.Repository)
	if err != nil || replay.Status != HistoricalDeduplicationReplaying {
		t.Fatalf("resume=%#v err=%v", replay, err)
	}
	resumedCheckpoint, _ := json.Marshal(struct {
		Raw          RawReviewFinding
		Job          DeduplicationJob
		Deduplicated []DeduplicatedReviewFinding
		Findings     []Finding
		Mappings     []RepositoryMappingJob
	}{
		Raw: resumed.RawFindings[0], Job: resumed.DeduplicationJobs[0],
		Deduplicated: resumed.DeduplicatedFindings,
		Findings:     resumed.Findings, Mappings: resumed.MappingJobs,
	})
	if string(completedCheckpoint) != string(resumedCheckpoint) {
		t.Fatalf("completed prefix changed\nbefore=%s\nafter=%s", completedCheckpoint, resumedCheckpoint)
	}
	gotRaw := resumed.RawFindings[1]
	gotJob := resumed.DeduplicationJobs[1]
	if gotRaw.State != RawFindingDeduplicationPending ||
		gotJob.State != DeduplicationJobPending || gotJob.Attempts != 0 ||
		gotJob.InsertionOrdinal != failedOrdinal || gotRaw.InsertionOrdinal != failedOrdinal ||
		gotRaw.CampaignID != failedCampaign || gotRaw.AdmissionBucket != failedBucket ||
		gotJob.AdmissionBucket != failedBucket || gotJob.ModelSnapshot != failedSnapshot ||
		gotJob.LeaseID != "" || gotRaw.Failure != nil || gotJob.Failure != nil ||
		gotJob.CandidateUniverseDigest != "" || gotJob.CandidateVersions != nil ||
		gotJob.ShortlistedScores != nil || gotJob.Decision != (DeduplicationJudgment{}) ||
		replay.ProfileSnapshot != snapshot {
		t.Fatalf("failed checkpoint identity changed raw=%#v job=%#v replay=%#v", gotRaw, gotJob, replay)
	}
}

func TestHistoricalTerminalFailureFencesOnlyItsBucketAcrossPasses(t *testing.T) {
	store, state, _ := seedHistoricalCheckpointSources(
		t, "Checkpoint.Run", "Checkpoint.Run", "Independent.Run",
	)
	failedRaw := &state.RawFindings[0]
	failedJob := &state.DeduplicationJobs[0]
	failedJob.Attempts = DeduplicationAttemptLimit
	markDeduplicationFailed(failedRaw, failedJob, "processing_failed", store.clock())
	state.HistoricalDeduplication.Status = HistoricalDeduplicationFailed
	state.HistoricalDeduplication.FailurePhase = HistoricalDeduplicationFailureProcessing
	state.HistoricalDeduplication.Error = "Historical deduplication failed."
	state.HistoricalDeduplication.UpdatedAt = store.clock()
	state.Version++
	state.UpdatedAt = store.clock()
	reconcileFindingsProcessingCounters(&state)
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	result, err := store.ProcessPendingDeduplicationJobs(
		t.Context(), state.Repository, DeduplicationProcessOptions{},
	)
	if err != nil || result.Completed != 1 || result.Created != 1 || result.Deferred != 1 {
		t.Fatalf("first pass=%#v err=%v", result, err)
	}
	afterFirst, _, err := store.Get(state.Repository)
	if err != nil {
		t.Fatal(err)
	}
	if afterFirst.RawFindings[1].State != RawFindingDeduplicationPending ||
		afterFirst.RawFindings[2].State != RawFindingDeduplicationCompleted {
		t.Fatalf("bucket progress raws=%#v", afterFirst.RawFindings)
	}
	result, err = store.ProcessPendingDeduplicationJobs(
		t.Context(), state.Repository, DeduplicationProcessOptions{},
	)
	if err != nil || result.Completed != 0 || result.Deferred != 1 ||
		result.Failed != 0 {
		t.Fatalf("second pass=%#v err=%v", result, err)
	}
}

func TestHistoricalResumeAcceptsLaterCompletedCandidate(t *testing.T) {
	store, state, _ := seedHistoricalCheckpointSources(
		t, "Checkpoint.Run", "Checkpoint.Run",
	)
	for index := range state.DeduplicationJobs {
		_, claim, claimed, err := store.ClaimDeduplicationJob(
			state.Repository, state.DeduplicationJobs[index].ID, time.Minute,
		)
		if err != nil || !claimed {
			t.Fatalf("claim %d=%#v claimed=%v err=%v", index, claim, claimed, err)
		}
		state, _, _, err = store.CompleteDeduplicationJob(
			state.Repository,
			DeduplicationCompletion{
				JobID: claim.Job.ID, LeaseID: claim.Job.LeaseID,
				CandidateUniverseDigest: claim.UniverseDigest,
				Decision:                DeduplicationJudgment{Decision: "new"},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	firstID := state.RawFindings[0].DeduplicatedFindingID
	laterID := state.RawFindings[1].DeduplicatedFindingID
	laterBefore, _ := json.Marshal(struct {
		Raw     RawReviewFinding
		Job     DeduplicationJob
		Finding DeduplicatedReviewFinding
	}{
		state.RawFindings[1], state.DeduplicationJobs[1],
		state.DeduplicatedFindings[deduplicatedFindingIndexByID(
			state.DeduplicatedFindings, laterID,
		)],
	})
	keptDeduplicated := state.DeduplicatedFindings[:0]
	for _, finding := range state.DeduplicatedFindings {
		if finding.ID != firstID {
			keptDeduplicated = append(keptDeduplicated, finding)
		}
	}
	state.DeduplicatedFindings = keptDeduplicated
	keptFindings := state.Findings[:0]
	for _, finding := range state.Findings {
		if finding.ID != firstID {
			keptFindings = append(keptFindings, finding)
		}
	}
	state.Findings = keptFindings
	keptMappings := state.MappingJobs[:0]
	for _, job := range state.MappingJobs {
		if job.ReviewFindingID != firstID {
			keptMappings = append(keptMappings, job)
		}
	}
	state.MappingJobs = keptMappings
	failedRaw := &state.RawFindings[0]
	failedJob := &state.DeduplicationJobs[0]
	failedRaw.DeduplicatedFindingID = ""
	failedRaw.Disposition = RawFindingDispositionUndecided
	failedJob.Attempts = DeduplicationAttemptLimit
	markDeduplicationFailed(failedRaw, failedJob, "processing_failed", store.clock())
	state.HistoricalDeduplication.Status = HistoricalDeduplicationFailed
	state.HistoricalDeduplication.FailurePhase = HistoricalDeduplicationFailureProcessing
	state.HistoricalDeduplication.Error = "Historical deduplication failed."
	state.HistoricalDeduplication.UpdatedAt = store.clock()
	state.Version++
	state.UpdatedAt = store.clock()
	reconcileFindingsProcessingCounters(&state)
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	resumed, _, err := store.RetryHistoricalDeduplicationReplay(state.Repository)
	if err != nil {
		t.Fatal(err)
	}
	laterAfter, _ := json.Marshal(struct {
		Raw     RawReviewFinding
		Job     DeduplicationJob
		Finding DeduplicatedReviewFinding
	}{
		resumed.RawFindings[1], resumed.DeduplicationJobs[1],
		resumed.DeduplicatedFindings[deduplicatedFindingIndexByID(
			resumed.DeduplicatedFindings, laterID,
		)],
	})
	if string(laterBefore) != string(laterAfter) {
		t.Fatalf("later completed checkpoint changed\nbefore=%s\nafter=%s", laterBefore, laterAfter)
	}
	_, claim, claimed, err := store.ClaimDeduplicationJob(
		state.Repository, resumed.DeduplicationJobs[0].ID, time.Minute,
	)
	if err != nil || !claimed || len(claim.Candidates) != 1 ||
		claim.Candidates[0].ID != laterID {
		t.Fatalf("resumed candidates=%#v claimed=%v err=%v", claim.Candidates, claimed, err)
	}
}

func TestHistoricalSelectiveRestartAlsoResumesFailedOutsideClosure(t *testing.T) {
	store, state, snapshot := seedHistoricalCheckpointSources(
		t, "Failed.Run", "Drifted.Run", "Stable.Run",
	)
	for _, index := range []int{1, 2} {
		_, claim, claimed, err := store.ClaimDeduplicationJob(
			state.Repository, state.DeduplicationJobs[index].ID, time.Minute,
		)
		if err != nil || !claimed {
			t.Fatalf("claim %d=%#v claimed=%v err=%v", index, claim, claimed, err)
		}
		state, _, _, err = store.CompleteDeduplicationJob(
			state.Repository,
			DeduplicationCompletion{
				JobID: claim.Job.ID, LeaseID: claim.Job.LeaseID,
				CandidateUniverseDigest: claim.UniverseDigest,
				Decision:                DeduplicationJudgment{Decision: "new"},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	failedRaw := &state.RawFindings[0]
	failedJob := &state.DeduplicationJobs[0]
	failedJob.Attempts = DeduplicationAttemptLimit
	markDeduplicationFailed(failedRaw, failedJob, "processing_failed", store.clock())
	failedOrdinal := failedJob.InsertionOrdinal
	failedCampaign := failedRaw.CampaignID
	failedBucket := failedRaw.AdmissionBucket
	failedSnapshot := failedJob.ModelSnapshot

	dependencies, err := HistoricalDeduplicationDependencies(state, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	state.RawFindings[1].AdmissionBucket = "rdb_stale_drifted_dependency"
	state.RawFindings[1].DiagnosisDigest = RawReviewFindingDiagnosisDigest(state.RawFindings[1])
	state.DeduplicationJobs[1].AdmissionBucket = state.RawFindings[1].AdmissionBucket
	driftedAggregateIndex := deduplicatedFindingIndexByID(
		state.DeduplicatedFindings, state.RawFindings[1].DeduplicatedFindingID,
	)
	state.DeduplicatedFindings[driftedAggregateIndex].AdmissionBucket =
		state.RawFindings[1].AdmissionBucket
	state.DeduplicatedFindings[driftedAggregateIndex].DiagnosisDigest =
		state.RawFindings[1].DiagnosisDigest
	driftedRaw := state.RawFindings[1]
	desiredByLegacy := make(map[string]HistoricalDeduplicationDependency)
	for _, dependency := range dependencies {
		desiredByLegacy[dependency.LegacyFindingID] = dependency
	}
	driftedDependency := desiredByLegacy[driftedRaw.LegacyFindingID]
	if driftedDependency.CampaignID != driftedRaw.CampaignID ||
		driftedDependency.AdmissionBucket == driftedRaw.AdmissionBucket {
		t.Fatalf("drifted dependency=%#v raw=%#v", driftedDependency, driftedRaw)
	}
	stableID := state.RawFindings[2].DeduplicatedFindingID
	stableBefore, _ := json.Marshal(struct {
		Raw     RawReviewFinding
		Job     DeduplicationJob
		Finding DeduplicatedReviewFinding
	}{
		state.RawFindings[2], state.DeduplicationJobs[2],
		state.DeduplicatedFindings[deduplicatedFindingIndexByID(
			state.DeduplicatedFindings, stableID,
		)],
	})
	state.HistoricalDeduplication.Status = HistoricalDeduplicationFailed
	state.HistoricalDeduplication.FailurePhase = HistoricalDeduplicationFailureProcessing
	state.HistoricalDeduplication.Error = "Historical deduplication failed."
	state.HistoricalDeduplication.UpdatedAt = store.clock()
	state.Version++
	state.UpdatedAt = store.clock()
	reconcileFindingsProcessingCounters(&state)
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	restarted, replay, err := store.RestartHistoricalDeduplicationReplay(
		state.Repository,
		HistoricalDeduplicationRestartRequest{
			ProfileSnapshot: snapshot, Dependencies: dependencies,
		},
	)
	if err != nil || replay.Status != HistoricalDeduplicationReplaying {
		t.Fatalf("selective restart=%#v err=%v", replay, err)
	}
	gotFailedRaw := restarted.RawFindings[0]
	gotFailedJob := restarted.DeduplicationJobs[0]
	if gotFailedRaw.State != RawFindingDeduplicationPending ||
		gotFailedJob.State != DeduplicationJobPending ||
		gotFailedJob.InsertionOrdinal != failedOrdinal ||
		gotFailedRaw.CampaignID != failedCampaign ||
		gotFailedRaw.AdmissionBucket != failedBucket ||
		gotFailedJob.ModelSnapshot != failedSnapshot {
		t.Fatalf("outside failed checkpoint=%#v job=%#v", gotFailedRaw, gotFailedJob)
	}
	if restarted.RawFindings[1].State != RawFindingDeduplicationPending ||
		restarted.RawFindings[1].CampaignID != driftedRaw.CampaignID ||
		restarted.RawFindings[1].AdmissionBucket != driftedDependency.AdmissionBucket ||
		restarted.DeduplicationJobs[1].ModelSnapshot != snapshot {
		t.Fatalf("drifted checkpoint=%#v job=%#v", restarted.RawFindings[1], restarted.DeduplicationJobs[1])
	}
	stableAfter, _ := json.Marshal(struct {
		Raw     RawReviewFinding
		Job     DeduplicationJob
		Finding DeduplicatedReviewFinding
	}{
		restarted.RawFindings[2], restarted.DeduplicationJobs[2],
		restarted.DeduplicatedFindings[deduplicatedFindingIndexByID(
			restarted.DeduplicatedFindings, stableID,
		)],
	})
	if string(stableBefore) != string(stableAfter) {
		t.Fatalf("stable checkpoint changed\nbefore=%s\nafter=%s", stableBefore, stableAfter)
	}
}

func TestHistoricalFutureDependencyRequiresRecoveredAuthority(t *testing.T) {
	store, state, snapshot := seedHistoricalCheckpointSources(t, "Future.Run")
	legacyID := state.RawFindings[0].LegacyFindingID
	state.RawFindings = nil
	state.DeduplicationJobs = nil
	state.FindingsProcessing = FindingsProcessingCounters{UpdatedAt: store.clock()}
	state.HistoricalDeduplication.Status = HistoricalDeduplicationFailed
	state.HistoricalDeduplication.FailurePhase = HistoricalDeduplicationFailureSetup
	state.HistoricalDeduplication.Error = "Historical deduplication failed."
	state.HistoricalDeduplication.UpdatedAt = store.clock()
	state.Version++
	state.UpdatedAt = store.clock()
	reconcileFindingsProcessingCounters(&state)
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	newCampaignID := NewRepositoryReviewCampaignID()
	dependencies, err := HistoricalDeduplicationDependencies(
		state, newCampaignID, []string{legacyID},
	)
	if err != nil {
		t.Fatal(err)
	}
	beforeVersion := state.Version
	_, _, err = store.ResumeHistoricalDeduplicationReplay(
		state.Repository, snapshot, dependencies,
	)
	if !errors.Is(err, ErrHistoricalDeduplicationRestartRequired) {
		t.Fatalf("unrecovered future dependency error=%v", err)
	}
	unchanged, _, err := store.Get(state.Repository)
	if err != nil || unchanged.Version != beforeVersion {
		t.Fatalf("future dependency preflight mutated state=%#v err=%v", unchanged, err)
	}
	findingIndex := findingIndexByID(state.Findings, legacyID)
	state.Findings[findingIndex].CampaignID = newCampaignID
	for _, contextID := range state.Findings[findingIndex].ContextIDs {
		for contextIndex := range state.Contexts {
			if state.Contexts[contextIndex].ID == contextID {
				state.Contexts[contextIndex].CampaignID = newCampaignID
			}
		}
	}
	state.CampaignHistory[newCampaignID] = state.Findings[findingIndex].CommitSHA
	state.Version++
	state.UpdatedAt = store.clock()
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	state, replay, err := store.RestartHistoricalDeduplicationReplay(
		state.Repository,
		HistoricalDeduplicationRestartRequest{
			ProfileSnapshot: snapshot, Dependencies: dependencies,
		},
	)
	if err != nil || replay.Status != HistoricalDeduplicationReplaying {
		t.Fatalf("recovered future restart=%#v err=%v", replay, err)
	}
	state, admission, err := store.AdmitNextHistoricalDeduplicationBatch(state.Repository)
	if err != nil || admission.Admitted != 1 ||
		state.RawFindings[0].CampaignID != newCampaignID ||
		state.RawFindings[0].AdmissionBucket != dependencies[0].AdmissionBucket {
		t.Fatalf("future admission=%#v raw=%#v err=%v", admission, state.RawFindings, err)
	}
}

func TestHistoricalMissingFrozenSnapshotRequiresProfileRestartAfterAdmission(t *testing.T) {
	store, state, snapshot := seedHistoricalCheckpointSources(t, "Missing.Snapshot")
	dependencies, err := HistoricalDeduplicationDependencies(state, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	state.DeduplicationJobs[0].Attempts = DeduplicationAttemptLimit
	markDeduplicationFailed(
		&state.RawFindings[0], &state.DeduplicationJobs[0],
		"processing_failed", store.clock(),
	)
	state.HistoricalDeduplication.Status = HistoricalDeduplicationFailed
	state.HistoricalDeduplication.ProfileSnapshot = HistoricalDeduplicationProfileSnapshot{}
	state.HistoricalDeduplication.FailurePhase = HistoricalDeduplicationFailureProcessing
	state.HistoricalDeduplication.Error = "Historical deduplication failed."
	state.HistoricalDeduplication.UpdatedAt = store.clock()
	state.Version++
	state.UpdatedAt = store.clock()
	reconcileFindingsProcessingCounters(&state)
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.ResumeHistoricalDeduplicationReplay(
		state.Repository, snapshot, dependencies,
	)
	if !errors.Is(err, ErrHistoricalDeduplicationRestartRequired) {
		t.Fatalf("missing frozen snapshot resume error=%v", err)
	}
	restarted, replay, err := store.RestartHistoricalDeduplicationReplay(
		state.Repository,
		HistoricalDeduplicationRestartRequest{
			ProfileSnapshot: snapshot, Dependencies: dependencies,
		},
	)
	if err != nil || replay.Status != HistoricalDeduplicationReplaying ||
		replay.ProfileSnapshot != snapshot ||
		restarted.RawFindings[0].State != RawFindingDeduplicationPending ||
		restarted.DeduplicationJobs[0].ModelSnapshot != snapshot {
		t.Fatalf("missing snapshot restart=%#v raw=%#v err=%v", replay, restarted.RawFindings, err)
	}
}

func TestHistoricalResumeCompletedLostResponseIsNoOp(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	state, err := store.load("owner/completed-resume")
	if err != nil {
		t.Fatal(err)
	}
	state.HistoricalDeduplication = HistoricalDeduplicationReplay{
		Status: HistoricalDeduplicationCompleted, UpdatedAt: now,
	}
	state.UpdatedAt = now
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	resumed, replay, err := store.ResumeHistoricalDeduplicationReplay(
		state.Repository, historicalReplayCoverageSnapshot(), nil,
	)
	if err != nil || replay.Status != HistoricalDeduplicationCompleted ||
		resumed.Version != state.Version {
		t.Fatalf("completed resume=%#v state=%#v err=%v", replay, resumed, err)
	}
}

func TestHistoricalSetupResumeIsIdempotentAndMarksAttempt(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 8, 31, 12, 30, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	state, err := store.load("owner/setup-resume")
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
	resumed, replay, err := store.RetryHistoricalDeduplicationReplay(state.Repository)
	if err != nil || replay.Status != HistoricalDeduplicationPending || replay.Attempts != 1 {
		t.Fatalf("setup resume=%#v err=%v", replay, err)
	}
	repeated, replay, err := store.RetryHistoricalDeduplicationReplay(state.Repository)
	if err != nil || replay.Status != HistoricalDeduplicationPending ||
		repeated.Version != resumed.Version || replay.Attempts != 1 {
		t.Fatalf("repeated setup resume=%#v state=%#v err=%v", replay, repeated, err)
	}
}

func TestHistoricalRestartClosureProfileAndSelectiveAggregateExpansion(t *testing.T) {
	current := []HistoricalDeduplicationDependency{
		{LegacyFindingID: "a", RawFindingID: "raw-a", CampaignID: "old-a", AdmissionBucket: "bucket-a"},
		{LegacyFindingID: "b", RawFindingID: "raw-b", CampaignID: "stable", AdmissionBucket: "bucket-b"},
		{LegacyFindingID: "c", RawFindingID: "raw-c", CampaignID: "stable-c", AdmissionBucket: "bucket-c"},
	}
	desired := append([]HistoricalDeduplicationDependency(nil), current...)
	desired[0].CampaignID = "new-a"
	desired[0].AdmissionBucket = "bucket-new-a"
	state := RepositoryState{
		RawFindings: []RawReviewFinding{
			{ID: "raw-a", LegacyFindingID: "a", AssignmentID: historicalReplayAssignmentID},
			{ID: "raw-b", LegacyFindingID: "b", AssignmentID: historicalReplayAssignmentID},
			{ID: "raw-c", LegacyFindingID: "c", AssignmentID: historicalReplayAssignmentID},
			{ID: "live"},
		},
		DeduplicatedFindings: []DeduplicatedReviewFinding{
			{RawSourceIDs: []string{"raw-a", "raw-b", "live"}},
		},
	}
	selected, err := historicalDeduplicationRestartClosure(state, current, desired, false)
	if err != nil || !reflect.DeepEqual(selected, map[string]struct{}{"a": {}, "b": {}}) {
		t.Fatalf("selective closure=%#v err=%v", selected, err)
	}
	selected, err = historicalDeduplicationRestartClosure(state, current, desired, true)
	if err != nil || !reflect.DeepEqual(
		selected, map[string]struct{}{"a": {}, "b": {}, "c": {}},
	) {
		t.Fatalf("profile closure=%#v err=%v", selected, err)
	}
}

func TestHistoricalProfileRestartResetsEveryCompletedBucket(t *testing.T) {
	store, state, snapshot := seedHistoricalCheckpointSources(
		t, "Profile.BucketA", "Profile.BucketB",
	)
	ordinals := make([]uint64, len(state.DeduplicationJobs))
	for index := range state.DeduplicationJobs {
		ordinals[index] = state.DeduplicationJobs[index].InsertionOrdinal
		_, claim, claimed, err := store.ClaimDeduplicationJob(
			state.Repository, state.DeduplicationJobs[index].ID, time.Minute,
		)
		if err != nil || !claimed {
			t.Fatalf("claim %d=%#v claimed=%v err=%v", index, claim, claimed, err)
		}
		state, _, _, err = store.CompleteDeduplicationJob(
			state.Repository,
			DeduplicationCompletion{
				JobID: claim.Job.ID, LeaseID: claim.Job.LeaseID,
				CandidateUniverseDigest: claim.UniverseDigest,
				Decision:                DeduplicationJudgment{Decision: "new"},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	state.HistoricalDeduplication.Status = HistoricalDeduplicationFailed
	state.HistoricalDeduplication.FailurePhase = HistoricalDeduplicationFailureMerge
	state.HistoricalDeduplication.Error = "Historical deduplication failed."
	state.HistoricalDeduplication.UpdatedAt = store.clock()
	state.Version++
	state.UpdatedAt = store.clock()
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	dependencies, err := HistoricalDeduplicationDependencies(state, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	newSnapshot := snapshot
	newSnapshot.ReviewerModel = "reviewer-v2"
	newSnapshot.DeduplicationModel = "deduplicator-v2"
	newSnapshot.AccountModelRevision = "revision-v2"
	restarted, replay, err := store.RestartHistoricalDeduplicationReplay(
		state.Repository,
		HistoricalDeduplicationRestartRequest{
			ProfileSnapshot: newSnapshot, Dependencies: dependencies,
		},
	)
	if err != nil || replay.Status != HistoricalDeduplicationReplaying ||
		replay.ProfileSnapshot != newSnapshot || len(restarted.DeduplicatedFindings) != 0 {
		t.Fatalf("profile restart=%#v deduplicated=%#v err=%v", replay, restarted.DeduplicatedFindings, err)
	}
	for index, raw := range restarted.RawFindings {
		job := restarted.DeduplicationJobs[index]
		if raw.State != RawFindingDeduplicationPending ||
			job.State != DeduplicationJobPending ||
			job.ModelSnapshot != newSnapshot ||
			job.InsertionOrdinal != ordinals[index] ||
			raw.InsertionOrdinal != ordinals[index] {
			t.Fatalf("profile-reset source %d raw=%#v job=%#v", index, raw, job)
		}
	}
}

func TestHistoricalSelectiveResetSplitsMixedAggregateAndPreservesUnrelated(t *testing.T) {
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	snapshot := historicalReplayCoverageSnapshot()
	newRaw := func(id, legacy, assignment string, ordinal uint64) RawReviewFinding {
		raw := RawReviewFinding{
			ID: id, LegacyFindingID: legacy, AssignmentID: assignment,
			Version: 1, Repository: "owner/repo", CommitSHA: strings.Repeat("a", 40),
			File:     FileRef{Path: "file.go", BlobSHA: strings.Repeat("b", 40)},
			Severity: "high", Title: id, Evidence: "evidence", Impact: "impact",
			Validation: Validation{Status: "confirmed", Summary: "confirmed"},
			ContextID:  "context", RunID: "run", Model: "model",
			CampaignID: "campaign", AdmissionBucket: "bucket", InsertionOrdinal: ordinal,
			State: RawFindingDeduplicationCompleted, Disposition: RawFindingDispositionNew,
			CreatedAt: now, UpdatedAt: now,
		}
		raw.DiagnosisDigest = RawReviewFindingDiagnosisDigest(raw)
		return raw
	}
	liveRaw := newRaw("rrw_live", "", "assignment", 1)
	selectedRaw := newRaw("rrw_selected", "legacy-selected", historicalReplayAssignmentID, 2)
	unrelatedRaw := newRaw("rrw_unrelated", "legacy-unrelated", historicalReplayAssignmentID, 3)
	job := func(raw RawReviewFinding) DeduplicationJob {
		return DeduplicationJob{
			ID: "job-" + raw.ID, RawFindingID: raw.ID,
			State: DeduplicationJobCompleted, AdmissionBucket: raw.AdmissionBucket,
			InsertionOrdinal: raw.InsertionOrdinal, ModelSnapshot: snapshot,
			Decision:  DeduplicationJudgment{Decision: "new"},
			CreatedAt: now, UpdatedAt: now,
		}
	}
	mixed := newDeduplicatedReviewFinding(liveRaw, 1, nil, now)
	mixed.RawSourceIDs = []string{liveRaw.ID, selectedRaw.ID}
	mixed.Status = FindingPosted
	mixed.IssueDraftID = "draft"
	liveRaw.DeduplicatedFindingID = mixed.ID
	selectedRaw.DeduplicatedFindingID = mixed.ID
	selectedRaw.Disposition = RawFindingDispositionDuplicate
	unrelated := newDeduplicatedReviewFinding(unrelatedRaw, 3, nil, now)
	unrelated.ID = "unrelated"
	unrelatedRaw.DeduplicatedFindingID = unrelated.ID
	selectedJob := job(selectedRaw)
	selectedJob.Decision = DeduplicationJudgment{
		Decision: "duplicate", CandidateID: mixed.ID,
	}
	state := RepositoryState{
		RawFindings: []RawReviewFinding{selectedRaw, liveRaw, unrelatedRaw},
		DeduplicationJobs: []DeduplicationJob{
			selectedJob, job(liveRaw), job(unrelatedRaw),
		},
		DeduplicatedFindings: []DeduplicatedReviewFinding{mixed, unrelated},
		Findings: []Finding{
			{ID: mixed.ID}, {ID: unrelated.ID},
		},
	}
	unrelatedBefore, _ := json.Marshal(struct {
		Raw     RawReviewFinding
		Job     DeduplicationJob
		Finding DeduplicatedReviewFinding
	}{state.RawFindings[2], state.DeduplicationJobs[2], state.DeduplicatedFindings[1]})
	liveBefore, _ := json.Marshal(struct {
		Raw RawReviewFinding
		Job DeduplicationJob
	}{state.RawFindings[1], state.DeduplicationJobs[1]})
	mixedVersion := mixed.Version
	mixedCreatedAt := mixed.CreatedAt
	dependency := HistoricalDeduplicationDependency{
		LegacyFindingID: selectedRaw.LegacyFindingID,
		RawFindingID:    selectedRaw.ID, CampaignID: selectedRaw.CampaignID,
		AdmissionBucket: selectedRaw.AdmissionBucket,
	}
	if err := resetHistoricalDeduplicationModelWorkSelection(
		&state, snapshot,
		map[string]struct{}{selectedRaw.ID: {}},
		map[string]HistoricalDeduplicationDependency{selectedRaw.ID: dependency},
		now.Add(time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	unrelatedAfter, _ := json.Marshal(struct {
		Raw     RawReviewFinding
		Job     DeduplicationJob
		Finding DeduplicatedReviewFinding
	}{
		state.RawFindings[2], state.DeduplicationJobs[2],
		state.DeduplicatedFindings[deduplicatedFindingIndexByID(
			state.DeduplicatedFindings, unrelated.ID,
		)],
	})
	if string(unrelatedBefore) != string(unrelatedAfter) {
		t.Fatalf("unrelated historical checkpoint changed\nbefore=%s\nafter=%s", unrelatedBefore, unrelatedAfter)
	}
	liveAfter, _ := json.Marshal(struct {
		Raw RawReviewFinding
		Job DeduplicationJob
	}{state.RawFindings[1], state.DeduplicationJobs[1]})
	if string(liveBefore) != string(liveAfter) {
		t.Fatalf("retained live checkpoint changed\nbefore=%s\nafter=%s", liveBefore, liveAfter)
	}
	mixedIndex := deduplicatedFindingIndexByID(state.DeduplicatedFindings, mixed.ID)
	if state.RawFindings[0].State != RawFindingDeduplicationPending ||
		state.RawFindings[1].State != RawFindingDeduplicationCompleted ||
		state.RawFindings[1].DeduplicatedFindingID != mixed.ID ||
		len(state.DeduplicatedFindings) != 2 ||
		deduplicatedFindingIndexByID(state.DeduplicatedFindings, unrelated.ID) < 0 ||
		mixedIndex < 0 || state.DeduplicatedFindings[mixedIndex].Version != mixedVersion+1 ||
		state.DeduplicatedFindings[mixedIndex].CreatedAt != mixedCreatedAt ||
		state.DeduplicatedFindings[mixedIndex].Status != FindingPosted ||
		state.DeduplicatedFindings[mixedIndex].IssueDraftID != "draft" ||
		!reflect.DeepEqual(
			state.DeduplicatedFindings[mixedIndex].RawSourceIDs,
			[]string{liveRaw.ID},
		) {
		t.Fatalf("selective mixed split=%#v", state)
	}
}

func TestHistoricalPhaseInferenceAndInterruptedMergeRecovery(t *testing.T) {
	now := time.Date(2026, 8, 31, 11, 0, 0, 0, time.UTC)
	completedRaw := RawReviewFinding{
		ID: "rrw_completed", LegacyFindingID: "legacy",
		AssignmentID: historicalReplayAssignmentID,
		State:        RawFindingDeduplicationCompleted,
	}
	state := RepositoryState{
		RawFindings: []RawReviewFinding{completedRaw},
		HistoricalDeduplication: HistoricalDeduplicationReplay{
			Required: true, Status: HistoricalDeduplicationFailed,
		},
	}
	if phase := HistoricalDeduplicationFailurePhaseForState(state); phase != HistoricalDeduplicationFailureMerge {
		t.Fatalf("merge inference=%q", phase)
	}
	state.HistoricalDeduplication.UpdatedAt = now
	state.HistoricalDeduplication.FailurePhase = HistoricalDeduplicationFailureProcessing
	if err := validateHistoricalDeduplicationReplay(state); err != nil ||
		historicalDeduplicationResumePhase(state) != HistoricalDeduplicationFailureMerge {
		t.Fatalf("stale processing phase did not resume merge: %v", err)
	}
	state.RawFindings[0].State = RawFindingDeduplicationFailed
	state.HistoricalDeduplication.FailurePhase = HistoricalDeduplicationFailureMerge
	if err := validateHistoricalDeduplicationReplay(state); err != nil ||
		historicalDeduplicationResumePhase(state) != HistoricalDeduplicationFailureProcessing {
		t.Fatalf("stale merge phase did not resume processing: %v", err)
	}
	state.RawFindings = nil
	state.HistoricalDeduplication.FailurePhase = ""
	if phase := HistoricalDeduplicationFailurePhaseForState(state); phase != HistoricalDeduplicationFailureSetup {
		t.Fatalf("setup inference=%q", phase)
	}

	store, persisted, snapshot := seedHistoricalCheckpointSources(t, "Merge.Run")
	runningState, claim, claimed, err := store.ClaimDeduplicationJob(
		persisted.Repository, persisted.DeduplicationJobs[0].ID, time.Minute,
	)
	if err != nil || !claimed {
		t.Fatalf("merge claim=%#v claimed=%v err=%v", claim, claimed, err)
	}
	if HistoricalDeduplicationModelWorkQuiescent(runningState) {
		t.Fatal("running historical model work reported quiescent")
	}
	persisted, _, _, err = store.CompleteDeduplicationJob(
		persisted.Repository,
		DeduplicationCompletion{
			JobID: claim.Job.ID, LeaseID: claim.Job.LeaseID,
			CandidateUniverseDigest: claim.UniverseDigest,
			Decision:                DeduplicationJudgment{Decision: "new"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	persisted.HistoricalDeduplication = HistoricalDeduplicationReplay{
		Required: true, Status: HistoricalDeduplicationMerging,
		ProfileSnapshot: snapshot, Attempts: 1, UpdatedAt: now,
		MergeLease: HistoricalDeduplicationMergeLease{
			ID: "lost-merge", Groups: []HistoricalDeduplicationMergeGroup{}, AcquiredAt: now,
		},
	}
	persisted.Version++
	persisted.UpdatedAt = now
	if err := store.save(&persisted); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = store.reconcileRepositoryJobs(persisted.Repository)
	if err != nil {
		t.Fatal(err)
	}
	recovered, _, err := store.Get(persisted.Repository)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.HistoricalDeduplication.Status != HistoricalDeduplicationReplaying ||
		recovered.HistoricalDeduplication.MergeLease.ID != "" ||
		recovered.HistoricalDeduplication.ProfileSnapshot != snapshot {
		t.Fatalf("merge recovery=%#v", recovered.HistoricalDeduplication)
	}
	var modelCalls atomic.Int32
	processed, err := store.ProcessPendingDeduplicationJobs(
		t.Context(), persisted.Repository,
		DeduplicationProcessOptions{
			Score: func(
				context.Context, RepositoryReviewDeduplicationSnapshot, string,
				DeduplicationScoringRequest,
			) (DeduplicationScoringResponse, error) {
				modelCalls.Add(1)
				return DeduplicationScoringResponse{}, nil
			},
			Judge: func(
				context.Context, RepositoryReviewDeduplicationSnapshot, string,
				DeduplicationJudgeRequest,
			) (DeduplicationJudgment, error) {
				modelCalls.Add(1)
				return DeduplicationJudgment{}, nil
			},
		},
	)
	if err != nil || processed != (DeduplicationProcessResult{}) || modelCalls.Load() != 0 {
		t.Fatalf("merge recovery processed=%#v model_calls=%d err=%v", processed, modelCalls.Load(), err)
	}
	if _, _, err := store.RetryHistoricalDeduplicationReplay(persisted.Repository); err != nil &&
		!errors.Is(err, ErrConflict) {
		t.Fatalf("idempotent recovered retry=%v", err)
	}
}
