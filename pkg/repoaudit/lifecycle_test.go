package repoaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRecordPersistsLifecycleProvenanceAndMappingJobAtomically(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	state, finding := recordLifecycleFinding(
		t, store, strings.Repeat("a", 40), strings.Repeat("b", 40), "run-one",
		"main", "main", true, "first defect",
	)
	if finding.MatchHints.Component != "scheduler" || finding.FixEffort.Quick.Class != "small" ||
		finding.TargetBranch != "main" || finding.AdvertisedDefaultBranch != "main" ||
		!finding.TargetIsDefault || len(state.MappingJobs) != 1 || len(state.RawFindings) != 1 ||
		len(state.DeduplicatedFindings) != 1 || len(state.DeduplicationJobs) != 1 ||
		!strings.HasPrefix(state.RawFindings[0].ID, "rrw_") ||
		!strings.HasPrefix(state.DeduplicatedFindings[0].ID, "rdf_") ||
		state.MappingJobs[0].ReviewFindingID != state.DeduplicatedFindings[0].ID ||
		state.RawFindings[0].DeduplicatedFindingID != state.DeduplicatedFindings[0].ID {
		t.Fatalf("recorded lifecycle state = %#v / %#v", finding, state.MappingJobs)
	}
	job := state.MappingJobs[0]
	if job.ID != mappingJobID(finding.ID) || job.ReviewFindingID != finding.ID ||
		job.State != RepositoryMappingPending || !job.CreatedAt.Equal(now) {
		t.Fatalf("mapping job = %#v", job)
	}
	if len(state.Runs) != 1 || state.Runs[0].TargetBranch != "main" ||
		!state.Runs[0].TargetIsDefault {
		t.Fatalf("run provenance = %#v", state.Runs)
	}
}

func TestSchemaOneListMigrationAndExplicitJobReconciliation(t *testing.T) {
	workspace := t.TempDir()
	store := NewStore(workspace)
	state, _ := recordLifecycleFinding(
		t, store, strings.Repeat("a", 40), strings.Repeat("b", 40), "legacy-run",
		"", "", false, "legacy defect",
	)
	state.SchemaVersion = 1
	state.RawFindings = nil
	state.DeduplicatedFindings = nil
	state.DeduplicationJobs = nil
	state.NextDeduplicationOrdinal = 0
	state.FindingsProcessing = FindingsProcessingCounters{}
	state.RepositoryFindings = nil
	state.MappingJobs = nil
	state.ValidationJobs = nil
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(store.path(state.Repository), data, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	legacySummary := Summarize(state)
	legacySummary.SchemaVersion = 1
	summaryData, _ := json.Marshal(legacySummary)
	summaryPath := strings.TrimSuffix(store.path(state.Repository), ".json") + ".summary.json"
	if writeErr := os.WriteFile(summaryPath, summaryData, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	summaries, err := store.ListSummaries()
	if err != nil || len(summaries) != 1 || summaries[0].SchemaVersion != SchemaVersion {
		t.Fatalf("summaries = %#v, err=%v", summaries, err)
	}
	migrated, found, err := store.Get(state.Repository)
	if err != nil || !found || migrated.SchemaVersion != SchemaVersion ||
		migrated.RepositoryFindings == nil || migrated.MappingJobs == nil || migrated.ValidationJobs == nil {
		t.Fatalf("migrated = %#v, found=%v err=%v", migrated, found, err)
	}
	if len(migrated.MappingJobs) != 0 {
		t.Fatalf("ordinary migration unexpectedly reconciled jobs: %#v", migrated.MappingJobs)
	}
	reconciled, err := store.ReconcileJobs(context.Background())
	if err != nil || reconciled.MappingJobsCreated != 0 {
		t.Fatalf("reconcile = %#v, err=%v", reconciled, err)
	}
	after, _, _ := store.Get(state.Repository)
	if len(after.MappingJobs) != 0 || !after.HistoricalDeduplication.Required {
		t.Fatalf(
			"legacy mappings bypassed replay: jobs=%#v replay=%#v",
			after.MappingJobs,
			after.HistoricalDeduplication,
		)
	}

	persisted, err := os.ReadFile(store.path(state.Repository))
	if err != nil || !strings.Contains(string(persisted), fmt.Sprintf(`"schema_version":%d`, SchemaVersion)) ||
		!strings.Contains(string(persisted), `"mapping_jobs"`) {
		t.Fatalf("persisted migration = %s, err=%v", persisted, err)
	}
}

func TestRawFindingIDMigrationRewritesEveryDurableReference(t *testing.T) {
	for _, oldID := range []string{"rrf_native", "rrl_compatibility"} {
		t.Run(oldID, func(t *testing.T) {
			state := RepositoryState{
				RawFindings:       []RawReviewFinding{{ID: oldID}},
				DeduplicationJobs: []DeduplicationJob{{RawFindingID: oldID}},
				DeduplicatedFindings: []DeduplicatedReviewFinding{{
					RawSourceIDs: []string{oldID},
					History:      []DeduplicatedFindingHistoryEntry{{RawFindingID: oldID}},
				}},
				Findings: []Finding{{RawFindingIDs: []string{oldID}}},
				Runs:     []ReviewRun{{FindingIDs: []string{oldID}}},
				ActiveReviewRun: &RepositoryReviewActiveRun{
					FindingIDs: []string{oldID},
				},
			}
			migrated, err := migrateRepositoryReviewRawFindingIDs(&state)
			want := "rrw_" + oldID[len("rrf_"):]
			if err != nil || !migrated || state.RawFindings[0].ID != want ||
				state.DeduplicationJobs[0].RawFindingID != want ||
				state.DeduplicatedFindings[0].RawSourceIDs[0] != want ||
				state.DeduplicatedFindings[0].History[0].RawFindingID != want ||
				state.Findings[0].RawFindingIDs[0] != want ||
				state.Runs[0].FindingIDs[0] != want ||
				state.ActiveReviewRun.FindingIDs[0] != want {
				t.Fatalf("migrated=%v state=%#v err=%v", migrated, state, err)
			}
			if migratedAgain, err := migrateRepositoryReviewRawFindingIDs(&state); err != nil || migratedAgain {
				t.Fatalf("second migration=%v err=%v", migratedAgain, err)
			}
		})
	}
	collision := RepositoryState{RawFindings: []RawReviewFinding{
		{ID: "rrf_same"}, {ID: "rrw_same"},
	}}
	if _, err := migrateRepositoryReviewRawFindingIDs(&collision); err == nil {
		t.Fatal("raw ID collision was accepted")
	}
}

func TestCompatibilityParentMigrationRewritesLifecycleReferences(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	oldRawID := "rrl_compatibility"
	newRawID := "rrw_compatibility"
	oldParentID := "rfn_compatibility"
	newParentID := stableID("rdf_", newRawID)
	raw := RawReviewFinding{
		ID: oldRawID, DeduplicatedFindingID: oldParentID,
		AssignmentID: "record-000-000",
		History:      []RawFindingHistoryEntry{{DeduplicatedFindingID: oldParentID}},
	}
	raw.DiagnosisDigest = RawReviewFindingDiagnosisDigest(raw)
	state := RepositoryState{
		RawFindings: []RawReviewFinding{raw},
		DeduplicationJobs: []DeduplicationJob{{
			RawFindingID:      oldRawID,
			CandidateVersions: []DeduplicationCandidateVersion{{CandidateID: oldParentID}},
			ShortlistedScores: []DeduplicationCandidateScore{{CandidateID: oldParentID}},
			Decision:          DeduplicationJudgment{CandidateID: oldParentID},
		}},
		DeduplicatedFindings: []DeduplicatedReviewFinding{{
			ID: oldParentID, DiagnosisDigest: raw.DiagnosisDigest,
			RawSourceIDs: []string{oldRawID},
			History:      []DeduplicatedFindingHistoryEntry{{RawFindingID: oldRawID}},
		}},
		Findings: []Finding{{
			ID: oldParentID, RawFindingIDs: []string{oldRawID},
			PostResolutionFindingID: oldParentID,
		}},
		MappingJobs: []RepositoryMappingJob{{
			ID: mappingJobID(oldParentID), ReviewFindingID: oldParentID,
		}},
		RepositoryFindings: []RepositoryFinding{{
			ReviewFindingIDs: []string{oldParentID},
			PathSymbolHistory: []RepositoryFindingPathSymbol{{
				ReviewFindingID: oldParentID, ObservedAt: now,
			}},
		}},
		IssueDrafts: []IssueDraft{{FindingIDs: []string{oldParentID}}},
		Runs:        []ReviewRun{{FindingIDs: []string{oldParentID}}},
		ActiveReviewRun: &RepositoryReviewActiveRun{
			FindingIDs: []string{oldRawID},
		},
	}
	migrated, err := migrateRepositoryReviewRawFindingIDs(&state)
	if err != nil || !migrated {
		t.Fatalf("migration=%v err=%v", migrated, err)
	}
	migratedRaw := state.RawFindings[0]
	if migratedRaw.ID != newRawID || migratedRaw.LegacyFindingID != oldParentID ||
		migratedRaw.DeduplicatedFindingID != newParentID ||
		migratedRaw.History[0].DeduplicatedFindingID != newParentID ||
		migratedRaw.DiagnosisDigest != RawReviewFindingDiagnosisDigest(migratedRaw) ||
		state.DeduplicatedFindings[0].ID != newParentID ||
		state.DeduplicatedFindings[0].DiagnosisDigest != migratedRaw.DiagnosisDigest ||
		state.DeduplicatedFindings[0].RawSourceIDs[0] != newRawID ||
		state.DeduplicatedFindings[0].History[0].RawFindingID != newRawID ||
		state.Findings[0].ID != newParentID ||
		state.Findings[0].PostResolutionFindingID != newParentID ||
		state.Findings[0].RawFindingIDs[0] != newRawID ||
		state.MappingJobs[0].ReviewFindingID != newParentID ||
		state.MappingJobs[0].ID != mappingJobID(newParentID) ||
		state.RepositoryFindings[0].ReviewFindingIDs[0] != newParentID ||
		state.RepositoryFindings[0].PathSymbolHistory[0].ReviewFindingID != newParentID ||
		state.IssueDrafts[0].FindingIDs[0] != newParentID ||
		state.Runs[0].FindingIDs[0] != newParentID ||
		state.ActiveReviewRun.FindingIDs[0] != newRawID ||
		state.DeduplicationJobs[0].RawFindingID != newRawID ||
		state.DeduplicationJobs[0].CandidateVersions[0].CandidateID != newParentID ||
		state.DeduplicationJobs[0].ShortlistedScores[0].CandidateID != newParentID ||
		state.DeduplicationJobs[0].Decision.CandidateID != newParentID {
		t.Fatalf("migrated state=%#v", state)
	}
	if migratedAgain, err := migrateRepositoryReviewRawFindingIDs(&state); err != nil || migratedAgain {
		t.Fatalf("second migration=%v err=%v", migratedAgain, err)
	}

	collisionRaw := raw
	collision := RepositoryState{
		RawFindings: []RawReviewFinding{collisionRaw},
		DeduplicatedFindings: []DeduplicatedReviewFinding{
			{ID: oldParentID}, {ID: newParentID},
		},
		Findings: []Finding{{ID: oldParentID}, {ID: newParentID}},
	}
	if _, err := migrateRepositoryReviewRawFindingIDs(&collision); err == nil {
		t.Fatal("compatibility parent collision was accepted")
	}
}

func TestCompatibilityParentMigrationPersistsCanonicalIdentityOnLoad(t *testing.T) {
	workspace := t.TempDir()
	store := NewStore(workspace)
	state, _ := recordLifecycleFinding(
		t, store, strings.Repeat("a", 40), strings.Repeat("b", 40), "legacy-record-run",
		"main", "main", true, "legacy compatibility parent",
	)
	oldParentID := "rfn_persisted_compatibility"
	newRawID := state.RawFindings[0].ID
	oldRawID := "rrl_" + strings.TrimPrefix(newRawID, "rrw_")
	newParentID := stableID("rdf_", newRawID)
	originalParentID := state.DeduplicatedFindings[0].ID
	raw := &state.RawFindings[0]
	raw.ID = oldRawID
	raw.LegacyFindingID = ""
	raw.DeduplicatedFindingID = oldParentID
	for index := range raw.History {
		raw.History[index].DeduplicatedFindingID = oldParentID
	}
	raw.DiagnosisDigest = RawReviewFindingDiagnosisDigest(*raw)
	deduplicated := &state.DeduplicatedFindings[0]
	deduplicated.ID = oldParentID
	deduplicated.DiagnosisDigest = raw.DiagnosisDigest
	deduplicated.RawSourceIDs[0] = oldRawID
	for index := range deduplicated.History {
		deduplicated.History[index].RawFindingID = oldRawID
	}
	for index := range state.Findings {
		if state.Findings[index].ID == originalParentID {
			state.Findings[index].ID = oldParentID
		}
	}
	for index := range state.DeduplicationJobs {
		state.DeduplicationJobs[index].RawFindingID = oldRawID
	}
	for index := range state.MappingJobs {
		state.MappingJobs[index].ID = mappingJobID(oldParentID)
		state.MappingJobs[index].ReviewFindingID = oldParentID
	}
	for runIndex := range state.Runs {
		for findingIndex := range state.Runs[runIndex].FindingIDs {
			if state.Runs[runIndex].FindingIDs[findingIndex] == originalParentID {
				state.Runs[runIndex].FindingIDs[findingIndex] = oldParentID
			}
		}
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(store.path(state.Repository), encoded, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	loaded, found, err := store.Get(state.Repository)
	if err != nil || !found {
		t.Fatalf("load found=%v err=%v", found, err)
	}
	if loaded.RawFindings[0].ID != newRawID ||
		loaded.RawFindings[0].LegacyFindingID != oldParentID ||
		loaded.RawFindings[0].DeduplicatedFindingID != newParentID ||
		loaded.RawFindings[0].DiagnosisDigest != RawReviewFindingDiagnosisDigest(loaded.RawFindings[0]) ||
		loaded.DeduplicatedFindings[0].ID != newParentID ||
		loaded.DeduplicatedFindings[0].DiagnosisDigest != loaded.RawFindings[0].DiagnosisDigest ||
		loaded.Findings[0].ID != newParentID ||
		loaded.MappingJobs[0].ID != mappingJobID(newParentID) ||
		loaded.MappingJobs[0].ReviewFindingID != newParentID ||
		loaded.Runs[0].FindingIDs[0] != newParentID {
		t.Fatalf("loaded migration=%#v", loaded)
	}
	persisted, err := os.ReadFile(store.path(state.Repository))
	if err != nil || !strings.Contains(string(persisted), `"legacy_finding_id":"`+oldParentID+`"`) ||
		!strings.Contains(string(persisted), `"id":"`+newParentID+`"`) {
		t.Fatalf("persisted migration=%s err=%v", persisted, err)
	}
	reloaded, found, err := store.Get(state.Repository)
	if err != nil || !found || !reflect.DeepEqual(loaded, reloaded) {
		t.Fatalf("idempotent reload found=%v err=%v\nfirst=%#v\nsecond=%#v", found, err, loaded, reloaded)
	}
}

func TestMappingAdjudicationAssociationDefaultBranchFenceAndRestart(t *testing.T) {
	store := NewStore(t.TempDir())
	state, first := recordLifecycleFinding(
		t, store, strings.Repeat("a", 40), strings.Repeat("b", 40), "run-first",
		"main", "main", true, "first defect",
	)
	snapshot := RepositoryMappingModelSnapshot{
		ProfileID: "rrpf_mapping", ProfileVersion: 2, Model: "reviewer", Account: "account",
	}
	_, claimedJob, _, claimed, err := store.ClaimMappingJob(
		state.Repository, state.MappingJobs[0].ID, snapshot,
	)
	if err != nil || !claimed || claimedJob.State != RepositoryMappingRunning {
		t.Fatalf("claim = %#v claimed=%v err=%v", claimedJob, claimed, err)
	}
	adjudication := RepositoryMappingAdjudication{
		Decision: "distinct", CandidateID: "opaque-candidate", Confidence: .98,
		Explanation: "Causal anchors conflict.",
	}
	if _, _, saveErr := store.SaveMappingAdjudication(
		state.Repository,
		claimedJob.ID,
		adjudication,
	); saveErr != nil {
		t.Fatal(saveErr)
	}
	if _, _, replayErr := store.SaveMappingAdjudication(
		state.Repository,
		claimedJob.ID,
		adjudication,
	); replayErr != nil {
		t.Fatalf("adjudication replay: %v", replayErr)
	}
	completed, repositoryFinding, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: claimedJob.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil || repositoryFinding.MatchState != RepositoryMatchNew ||
		completed.Findings[0].RepositoryFindingID != repositoryFinding.ID {
		t.Fatalf("completion = %#v / %#v err=%v", completed.Findings, repositoryFinding, err)
	}
	if replay, same, replayErr := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: claimedJob.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	}); replayErr != nil || same.ID != repositoryFinding.ID || len(replay.RepositoryFindings) != 1 {
		t.Fatalf("completion replay = %#v / %#v err=%v", replay, same, replayErr)
	}

	secondState, second := recordLifecycleFinding(
		t, store, strings.Repeat("c", 40), strings.Repeat("d", 40), "run-branch",
		"feature", "main", false, "branch-only defect",
	)
	secondJob := lifecycleJobForFinding(t, secondState, second.ID)
	_, secondJob, _, claimed, err = store.ClaimMappingJob(
		secondState.Repository,
		secondJob.ID,
		RepositoryMappingModelSnapshot{},
	)
	if err != nil || !claimed {
		t.Fatalf("second claim=%v err=%v", claimed, err)
	}
	if _, _, completionErr := store.CompleteMappingJob(secondState.Repository, RepositoryMappingCompletion{
		JobID: secondJob.ID, CreateMatchState: RepositoryMatchNew,
	}); completionErr == nil || !strings.Contains(completionErr.Error(), "non-default") {
		t.Fatalf("non-default create error = %v", completionErr)
	}
	joined, known, err := store.CompleteMappingJob(secondState.Repository, RepositoryMappingCompletion{
		JobID: secondJob.ID, RepositoryFindingID: repositoryFinding.ID,
	})
	if err != nil || len(known.ReviewFindingIDs) != 2 ||
		joined.Findings[findingIndexByID(joined.Findings, second.ID)].RepositoryMatchState != RepositoryMatchKnown {
		t.Fatalf("non-default association = %#v err=%v", known, err)
	}

	thirdState, third := recordLifecycleFinding(
		t, store, strings.Repeat("e", 40), strings.Repeat("f", 40), "run-restart",
		"main", "main", true, "restart defect",
	)
	thirdJob := lifecycleJobForFinding(t, thirdState, third.ID)
	claimLifecycleMappingJob(t, store, thirdState.Repository, thirdJob, snapshot)
	if _, reconcileErr := store.ReconcileJobs(context.Background()); reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	after, _, _ := store.Get(thirdState.Repository)
	thirdJob = lifecycleJobForFinding(t, after, third.ID)
	if thirdJob.State != RepositoryMappingPending || thirdJob.ModelSnapshot != snapshot ||
		thirdJob.ReservedAt != (time.Time{}) {
		t.Fatalf("reconciled mapping job = %#v", thirdJob)
	}
	_ = first
}

func TestValidationQueueCommitFenceRegressionAndIssueTTL(t *testing.T) {
	store := NewStore(t.TempDir())
	clock := time.Date(2026, 8, 26, 13, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	state, finding := recordLifecycleFinding(
		t, store, strings.Repeat("1", 40), strings.Repeat("2", 40), "resolution-run",
		"main", "main", true, "resolvable defect",
	)
	job := lifecycleJobForFinding(t, state, finding.ID)
	job = claimLifecycleMappingJob(t, store, state.Repository, job, RepositoryMappingModelSnapshot{})
	state, aggregate, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: job.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, jobs, err := store.ReserveValidationJobs(
		state.Repository, []string{aggregate.ID}, RepositoryMappingModelSnapshot{Model: "reviewer"},
	)
	if err != nil || len(jobs) != 1 || jobs[0].State != RepositoryValidationPending {
		t.Fatalf("validation reserve=%#v err=%v", jobs, err)
	}
	state, running, _, claimed, err := store.ClaimValidationJob(state.Repository, jobs[0].ID)
	if err != nil || !claimed || running.State != RepositoryValidationRunning {
		t.Fatalf("validation claim=%#v claimed=%v err=%v", running, claimed, err)
	}
	fixCommit := strings.Repeat("3", 40)
	otherCommit := strings.Repeat("4", 40)
	state, running, err = store.SetValidationJobCandidates(state.Repository, running.ID, []string{fixCommit})
	if err != nil {
		t.Fatal(err)
	}
	outsideState, outsideFinding, outsideJob, outsideErr := store.CompleteValidationJob(
		state.Repository,
		RepositoryValidationCompletion{
			JobID: running.ID, Outcome: RepositoryValidationConfirmed,
			SelectedCommitSHA: otherCommit, FixCommitTime: clock,
		})
	if outsideErr == nil || !strings.Contains(outsideErr.Error(), "outside") ||
		outsideState.Repository != "" || outsideFinding.ID != "" || outsideJob.ID != "" {
		t.Fatalf(
			"unsupplied commit result = %#v / %#v / %#v, error = %v",
			outsideState,
			outsideFinding,
			outsideJob,
			outsideErr,
		)
	}
	invalidTagState, invalidTagFinding, invalidTagJob, invalidTagErr := store.CompleteValidationJob(
		state.Repository,
		RepositoryValidationCompletion{
			JobID: running.ID, Outcome: RepositoryValidationConfirmed,
			SelectedCommitSHA: fixCommit, FixCommitTime: clock, FirstContainingTag: "release-one",
		})
	if invalidTagErr == nil || !strings.Contains(invalidTagErr.Error(), "semantic") ||
		invalidTagState.Repository != "" || invalidTagFinding.ID != "" || invalidTagJob.ID != "" {
		t.Fatalf(
			"non-semantic tag result = %#v / %#v / %#v, error = %v",
			invalidTagState,
			invalidTagFinding,
			invalidTagJob,
			invalidTagErr,
		)
	}
	clock = clock.Add(time.Minute)
	state, resolved, completedJob, err := store.CompleteValidationJob(state.Repository, RepositoryValidationCompletion{
		JobID: running.ID, Outcome: RepositoryValidationConfirmed,
		SelectedCommitSHA: fixCommit, FixCommitTime: clock.Add(-time.Hour),
		FirstContainingTag: "v1.2.3", Summary: "The supplied diff restores the invariant.",
	})
	if err != nil || completedJob.State != RepositoryValidationConfirmed ||
		resolved.Lifecycle != RepositoryFindingResolved || resolved.FixCommitSHA != fixCommit ||
		len(resolved.ResolutionHistory) != 1 {
		t.Fatalf("resolved=%#v job=%#v err=%v", resolved, completedJob, err)
	}

	clock = clock.Add(time.Minute)
	state, refreshed, err := store.UpdateRepositoryFindingIssueSnapshot(state.Repository, RepositoryIssueSnapshotUpdate{
		RepositoryFindingID: aggregate.ID, ExpectedVersion: resolved.Version,
		ExternalID: "17", URL: "https://github.com/owner/repo/issues/17",
		Origin: IssueDraftOriginLinked, State: RepositoryFindingIssueClosed, Title: "Tracked defect",
	})
	if err != nil || refreshed.Lifecycle != RepositoryFindingResolved ||
		!RepositoryFindingIssueSnapshotFresh(refreshed, clock) ||
		RepositoryFindingIssueSnapshotFresh(refreshed, clock.Add(RepositoryIssueSnapshotTTL)) {
		t.Fatalf("closed snapshot=%#v err=%v", refreshed, err)
	}
	clock = clock.Add(RepositoryIssueSnapshotTTL)
	_, reopened, err := store.UpdateRepositoryFindingIssueSnapshot(state.Repository, RepositoryIssueSnapshotUpdate{
		RepositoryFindingID: aggregate.ID, ExpectedVersion: refreshed.Version,
		ExternalID: "17", URL: "https://github.com/owner/repo/issues/17",
		Origin: IssueDraftOriginLinked, State: RepositoryFindingIssueOpen, Title: "Tracked defect",
	})
	if err != nil || reopened.Lifecycle != RepositoryFindingOpen {
		t.Fatalf("reopened snapshot=%#v err=%v", reopened, err)
	}

	clock = clock.Add(time.Minute)
	newState, newOccurrence := recordLifecycleFinding(
		t, store, strings.Repeat("5", 40), strings.Repeat("6", 40), "regression-run",
		"main", "main", true, "resolvable defect returns",
	)
	newJob := lifecycleJobForFinding(t, newState, newOccurrence.ID)
	newJob = claimLifecycleMappingJob(t, store, newState.Repository, newJob, RepositoryMappingModelSnapshot{})
	_, regressed, err := store.CompleteMappingJob(newState.Repository, RepositoryMappingCompletion{
		JobID: newJob.ID, RepositoryFindingID: aggregate.ID,
	})
	// Reopening intentionally returned lifecycle to open, so restore a confirmed
	// resolution projection to exercise the post-resolution occurrence rule.
	if err != nil || regressed.Lifecycle == RepositoryFindingRegressed {
		// The actual regression transition is covered below on an independently
		// resolved aggregate; this assertion documents reopening precedence.
		t.Fatalf("reopened association=%#v err=%v", regressed, err)
	}
}

func TestValidationRestartAndPostResolutionOccurrenceRegresses(t *testing.T) {
	store := NewStore(t.TempDir())
	clock := time.Date(2026, 8, 26, 15, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	state, finding := recordLifecycleFinding(
		t, store, strings.Repeat("7", 40), strings.Repeat("8", 40), "before-fix",
		"main", "main", true, "regression target",
	)
	job := lifecycleJobForFinding(t, state, finding.ID)
	job = claimLifecycleMappingJob(t, store, state.Repository, job, RepositoryMappingModelSnapshot{})
	state, aggregate, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: job.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, validationJobs, err := store.ReserveValidationJobs(
		state.Repository,
		[]string{aggregate.ID},
		RepositoryMappingModelSnapshot{},
	)
	if err != nil || len(validationJobs) != 1 {
		t.Fatalf("validation reservation=%#v err=%v", validationJobs, err)
	}
	state, running, validationFinding, validationClaimed, validationClaimErr := store.ClaimValidationJob(
		state.Repository,
		validationJobs[0].ID,
	)
	if validationClaimErr != nil || !validationClaimed || validationFinding.ID != aggregate.ID {
		t.Fatalf(
			"validation claim=%v finding=%#v err=%v",
			validationClaimed,
			validationFinding,
			validationClaimErr,
		)
	}
	if _, reconcileErr := store.ReconcileJobs(context.Background()); reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	state, _, _ = store.Get(state.Repository)
	running = lifecycleValidationJobByID(t, state, running.ID)
	if running.State != RepositoryValidationPending || running.ReservedAt != (time.Time{}) {
		t.Fatalf("reconciled validation job=%#v", running)
	}
	_, running, _, claimed, err := store.ClaimValidationJob(state.Repository, running.ID)
	if err != nil || !claimed {
		t.Fatalf("reclaim=%v err=%v", claimed, err)
	}
	fix := strings.Repeat("9", 40)
	_, running, _ = store.SetValidationJobCandidates(state.Repository, running.ID, []string{fix})
	clock = clock.Add(time.Minute)
	_, resolved, _, err := store.CompleteValidationJob(state.Repository, RepositoryValidationCompletion{
		JobID: running.ID, Outcome: RepositoryValidationConfirmed,
		SelectedCommitSHA: fix, FixCommitTime: clock.Add(-time.Hour), Summary: "confirmed",
	})
	if err != nil || resolved.Lifecycle != RepositoryFindingResolved {
		t.Fatalf("resolution=%#v err=%v", resolved, err)
	}
	clock = clock.Add(time.Minute)
	state, later := recordLifecycleFinding(
		t, store, strings.Repeat("a", 40), strings.Repeat("c", 40), "after-fix",
		"main", "main", true, "regression target returns",
	)
	laterJob := lifecycleJobForFinding(t, state, later.ID)
	laterJob = claimLifecycleMappingJob(t, store, state.Repository, laterJob, RepositoryMappingModelSnapshot{})
	_, regressed, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: laterJob.ID, RepositoryFindingID: aggregate.ID,
		DefaultBranchVerified: true, RegressionVerified: true, RegressionFixCommit: fix,
		RegressionFindingID: aggregate.ID,
	})
	if err != nil || regressed.Lifecycle != RepositoryFindingRegressed ||
		regressed.ValidationState != RepositoryValidationNotRequested ||
		len(regressed.ResolutionHistory) != 1 {
		t.Fatalf("regression=%#v err=%v", regressed, err)
	}
}

func TestPossibleDuplicateDistinctAndMergePreserveIssueConflicts(t *testing.T) {
	store := NewStore(t.TempDir())
	clock := time.Date(2026, 8, 26, 17, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	state, first := recordLifecycleFinding(
		t, store, strings.Repeat("a", 40), strings.Repeat("1", 40), "duplicate-target",
		"main", "main", true, "canonical defect",
	)
	job := lifecycleJobForFinding(t, state, first.ID)
	job = claimLifecycleMappingJob(t, store, state.Repository, job, RepositoryMappingModelSnapshot{})
	state, target, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: job.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, target, err = store.UpdateRepositoryFindingIssueSnapshot(state.Repository, RepositoryIssueSnapshotUpdate{
		RepositoryFindingID: target.ID, ExpectedVersion: target.Version,
		ExternalID: "11", URL: "https://github.com/owner/repo/issues/11",
		Origin: IssueDraftOriginLinked, State: RepositoryFindingIssueOpen, Title: "First issue",
	})
	if err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(time.Minute)
	state, second := recordLifecycleFinding(
		t, store, strings.Repeat("b", 40), strings.Repeat("2", 40), "duplicate-source",
		"main", "main", true, "ambiguous moved defect", MatchHints{
			Component: "scheduler", Operation: "resume migrated waiter", FailureMode: "stale waiter generation",
			Trigger: "retry after scheduler migration", ViolatedInvariant: "resumed waiters use the active generation",
			ObservableOutcome: "waiter remains blocked", RelatedSymbols: []string{"Scheduler.Run", "Scheduler.Resume"},
			SourceAnchors: []string{"generation"}, DistinguishingFacts: []string{"requires scheduler migration"},
		},
	)
	// Model an existing pre-queue issue association. Startup reconciliation must
	// restore its mapping job, and mapping must preserve the issue so a later
	// merge can surface both GitHub associations as a manual conflict.
	persistedJobs := make([]RepositoryMappingJob, 0, len(state.MappingJobs))
	for _, existingJob := range state.MappingJobs {
		if existingJob.ReviewFindingID != second.ID {
			persistedJobs = append(persistedJobs, existingJob)
		}
	}
	state.MappingJobs = persistedJobs
	state.Version++
	state.UpdatedAt = clock
	if saveErr := store.save(&state); saveErr != nil {
		t.Fatal(saveErr)
	}
	state, _, err = store.LinkExistingIssue(ExistingIssueLink{
		Repository: state.Repository, FindingID: second.ID,
		ExpectedFindingVersion: second.Version, ExternalID: "22",
		ExternalURL: "https://github.com/owner/repo/issues/22", Title: "Second issue",
		Confirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, reconcileErr := store.ReconcileJobs(context.Background()); reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	state, _, err = store.Get(state.Repository)
	if err != nil {
		t.Fatal(err)
	}
	job = lifecycleJobForFinding(t, state, second.ID)
	job = claimLifecycleMappingJob(t, store, state.Repository, job, RepositoryMappingModelSnapshot{})
	adjudication := RepositoryMappingAdjudication{
		Decision: "uncertain", CandidateID: target.ID, Confidence: .74,
		MatchingAnchors: []string{"Scheduler.Run"}, Explanation: "The code moved and one anchor conflicts.",
	}
	if _, _, saveErr := store.SaveMappingAdjudication(
		state.Repository,
		job.ID,
		adjudication,
	); saveErr != nil {
		t.Fatal(saveErr)
	}
	state, provisional, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: job.ID, CreateMatchState: RepositoryMatchProvisional, DefaultBranchVerified: true,
		PossibleDuplicates: []RepositoryFindingPossibleDuplicate{{
			CandidateID: target.ID, Relation: "uncertain", Confidence: .74,
			MatchingAnchors: []string{"Scheduler.Run"}, Explanation: "The code moved.",
		}},
	})
	if err != nil || provisional.MatchState != RepositoryMatchProvisional ||
		provisional.Issue.URL != "https://github.com/owner/repo/issues/22" {
		t.Fatalf("provisional=%#v err=%v", provisional, err)
	}
	state, merged, err := store.ResolvePossibleDuplicate(state.Repository, RepositoryDuplicateResolution{
		ProvisionalID: provisional.ID, CandidateID: target.ID, Decision: "merge",
		ExpectedProvisionalVersion: provisional.Version, ExpectedCandidateVersion: target.Version,
	})
	if err != nil || len(state.RepositoryFindings) != 1 || !merged.Issue.Conflict ||
		!containsExactString(merged.Issue.ConflictURLs, "https://github.com/owner/repo/issues/11") ||
		!containsExactString(merged.Issue.ConflictURLs, "https://github.com/owner/repo/issues/22") ||
		len(merged.ReviewFindingIDs) != 2 {
		t.Fatalf("merged=%#v repositories=%#v err=%v", merged, state.RepositoryFindings, err)
	}

	clock = clock.Add(time.Minute)
	state, third := recordLifecycleFinding(
		t, store, strings.Repeat("c", 40), strings.Repeat("3", 40), "duplicate-distinct",
		"main", "main", true, "independent nearby defect", MatchHints{
			Component:           "scheduler",
			Operation:           "discard canceled waiter",
			FailureMode:         "canceled waiter is retained",
			Trigger:             "cancellation during queue rotation",
			ViolatedInvariant:   "canceled waiters leave every queue",
			ObservableOutcome:   "queue capacity is exhausted",
			RelatedSymbols:      []string{"Scheduler.Run", "Scheduler.Cancel"},
			SourceAnchors:       []string{"canceled"},
			DistinguishingFacts: []string{"requires cancellation"},
		},
	)
	job = lifecycleJobForFinding(t, state, third.ID)
	job = claimLifecycleMappingJob(t, store, state.Repository, job, RepositoryMappingModelSnapshot{})
	if _, _, saveErr := store.SaveMappingAdjudication(state.Repository, job.ID, RepositoryMappingAdjudication{
		Decision: "uncertain", CandidateID: target.ID, Confidence: .51,
	}); saveErr != nil {
		t.Fatal(saveErr)
	}
	state, provisional, err = store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: job.ID, CreateMatchState: RepositoryMatchProvisional, DefaultBranchVerified: true,
		PossibleDuplicates: []RepositoryFindingPossibleDuplicate{{
			CandidateID: target.ID, Relation: "uncertain", Confidence: .51,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reservedState, reservedDraft, reserved, reserveErr := store.ReserveIssueGeneration(IssueGenerationRequest{
		Repository: state.Repository, FindingID: third.ID, GenerationID: "rrig_provisional",
		ResolvedInstructions: "Present the diagnosis.", InstructionsMode: IssueDraftInstructionsDefault,
		GeneratorModel: "writer", GeneratorAccount: "account",
	})
	if !errors.Is(reserveErr, ErrConflict) || reserved || reservedState.Repository != "" || reservedDraft.ID != "" {
		t.Fatalf(
			"provisional issue reservation=%#v / %#v / %v, error=%v",
			reservedState,
			reservedDraft,
			reserved,
			reserveErr,
		)
	}
	_, distinct, err := store.ResolvePossibleDuplicate(state.Repository, RepositoryDuplicateResolution{
		ProvisionalID: provisional.ID, CandidateID: target.ID, Decision: "distinct",
		ExpectedProvisionalVersion: provisional.Version,
	})
	if err != nil || distinct.MatchState != RepositoryMatchNew || len(distinct.PossibleDuplicates) != 0 {
		t.Fatalf("distinct=%#v err=%v", distinct, err)
	}
}

func TestIssueDraftStateProjectsOntoRepositoryFindingWithoutChangingPublicationFences(t *testing.T) {
	store := NewStore(t.TempDir())
	state, occurrence := recordLifecycleFinding(
		t, store, strings.Repeat("d", 40), strings.Repeat("4", 40), "issue-projection",
		"main", "main", true, "issue projection defect",
	)
	job := lifecycleJobForFinding(t, state, occurrence.ID)
	job = claimLifecycleMappingJob(t, store, state.Repository, job, RepositoryMappingModelSnapshot{})
	state, aggregate, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: job.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, draft, reserved, err := store.ReserveIssueGeneration(IssueGenerationRequest{
		Repository: state.Repository, FindingID: occurrence.ID, GenerationID: "rrig_projection_one",
		ResolvedInstructions: "Present the diagnosis.", InstructionsMode: IssueDraftInstructionsDefault,
		GeneratorModel: "writer", GeneratorAccount: "account",
	})
	if err != nil || !reserved {
		t.Fatalf("reserve=%v draft=%#v err=%v", reserved, draft, err)
	}
	projected := state.RepositoryFindings[repositoryFindingIndexByID(state.RepositoryFindings, aggregate.ID)]
	if projected.Issue.State != RepositoryFindingIssueDraft {
		t.Fatalf("generating projection=%#v", projected.Issue)
	}
	state, draft, err = store.CompleteIssueGeneration(
		state.Repository, draft.ID, draft.GenerationID, "Generated issue", "Grounded body", []string{"bug"}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.DeleteIssueDraft(state.Repository, draft.ID, draft.Version)
	if err != nil {
		t.Fatal(err)
	}
	projected = state.RepositoryFindings[repositoryFindingIndexByID(state.RepositoryFindings, aggregate.ID)]
	if projected.Issue.State != RepositoryFindingIssueNone {
		t.Fatalf("deleted projection=%#v", projected.Issue)
	}
	state, draft, reserved, err = store.ReserveIssueGeneration(IssueGenerationRequest{
		Repository: state.Repository, FindingID: occurrence.ID, GenerationID: "rrig_projection_two",
		ResolvedInstructions: "Present the diagnosis.", InstructionsMode: IssueDraftInstructionsDefault,
		GeneratorModel: "writer", GeneratorAccount: "account",
	})
	if err != nil || !reserved {
		t.Fatalf("second reserve=%v err=%v", reserved, err)
	}
	state, draft, err = store.CompleteIssueGeneration(
		state.Repository, draft.ID, draft.GenerationID, "Generated issue", "Grounded body", []string{"bug"}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	state, draft, claimed, err := store.ClaimIssueDraftPublication(state.Repository, draft.ID, draft.Version)
	if err != nil || !claimed {
		t.Fatalf("publication claim=%v draft=%#v err=%v", claimed, draft, err)
	}
	projected = state.RepositoryFindings[repositoryFindingIndexByID(state.RepositoryFindings, aggregate.ID)]
	if projected.Issue.State != RepositoryFindingIssueUnknown || projected.Issue.URL != "" {
		t.Fatalf("publishing projection=%#v", projected.Issue)
	}
	state, _, err = store.SetIssueDraftPublication(
		state.Repository, draft.ID, draft.Version, IssueDraftPosted,
		"31", "https://github.com/owner/repo/issues/31",
	)
	if err != nil {
		t.Fatal(err)
	}
	projected = state.RepositoryFindings[repositoryFindingIndexByID(state.RepositoryFindings, aggregate.ID)]
	if projected.Issue.State != RepositoryFindingIssueOpen ||
		projected.Issue.URL != "https://github.com/owner/repo/issues/31" {
		t.Fatalf("posted projection=%#v", projected.Issue)
	}
}

func TestClosedExistingIssueLinkMovesAggregateToResolutionPending(t *testing.T) {
	store := NewStore(t.TempDir())
	state, occurrence := recordLifecycleFinding(
		t, store, strings.Repeat("e", 40), strings.Repeat("5", 40), "closed-issue-link",
		"main", "main", true, "closed issue defect",
	)
	job := lifecycleJobForFinding(t, state, occurrence.ID)
	_, job, _, claimed, err := store.ClaimMappingJob(
		state.Repository, job.ID, RepositoryMappingModelSnapshot{},
	)
	if err != nil || !claimed {
		t.Fatalf("mapping claim=%v err=%v", claimed, err)
	}
	state, aggregate, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: job.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	occurrence = state.Findings[findingIndexByID(state.Findings, occurrence.ID)]
	state, _, err = store.LinkExistingIssue(ExistingIssueLink{
		Repository: state.Repository, FindingID: occurrence.ID,
		ExpectedFindingVersion: occurrence.Version,
		ExternalID:             "42", ExternalURL: "https://github.com/owner/repo/issues/42",
		State: "closed", Title: "Already closed", Origin: IssueDraftOriginLinked,
		Confirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	projected := state.RepositoryFindings[repositoryFindingIndexByID(state.RepositoryFindings, aggregate.ID)]
	if projected.Issue.State != RepositoryFindingIssueClosed ||
		projected.Lifecycle != RepositoryFindingResolutionPending {
		t.Fatalf("closed issue projection=%#v lifecycle=%q", projected.Issue, projected.Lifecycle)
	}
}

func TestMappingCreationRequeuesWhenCandidateUniverseChanges(t *testing.T) {
	store := NewStore(t.TempDir())
	_, first := recordLifecycleFinding(
		t, store, strings.Repeat("1", 40), strings.Repeat("a", 40), "universe-first",
		"main", "main", true, "first universe defect",
	)
	state, second := recordLifecycleFinding(
		t, store, strings.Repeat("2", 40), strings.Repeat("b", 40), "universe-second",
		"main", "main", true, "second universe defect", MatchHints{
			Component: "scheduler", Operation: "discard canceled waiter",
			FailureMode: "canceled waiter remains queued", Trigger: "queue rotation after cancellation",
			ViolatedInvariant: "canceled waiters leave the queue", ObservableOutcome: "queue capacity is exhausted",
			RelatedSymbols: []string{"Scheduler.Run"}, SourceAnchors: []string{"canceled"},
			DistinguishingFacts: []string{"requires cancellation"},
		},
	)
	firstJob := lifecycleJobForFinding(t, state, first.ID)
	secondJob := lifecycleJobForFinding(t, state, second.ID)
	_, firstJob, _, firstClaimed, err := store.ClaimMappingJob(
		state.Repository, firstJob.ID, RepositoryMappingModelSnapshot{},
	)
	if err != nil || !firstClaimed {
		t.Fatalf("first claim=%v err=%v", firstClaimed, err)
	}
	_, secondJob, _, secondClaimed, err := store.ClaimMappingJob(
		state.Repository, secondJob.ID, RepositoryMappingModelSnapshot{},
	)
	if err != nil || !secondClaimed {
		t.Fatalf("second claim=%v err=%v", secondClaimed, err)
	}
	emptyUniverse := repositoryMatchingUniverseFingerprint(nil)
	state, _, err = store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: firstJob.ID, CreateMatchState: RepositoryMatchNew,
		DefaultBranchVerified: true, ExpectedUniverse: emptyUniverse,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: secondJob.ID, CreateMatchState: RepositoryMatchNew,
		DefaultBranchVerified: true, ExpectedUniverse: emptyUniverse,
	})
	if !errors.Is(err, errRepositoryMappingUniverseChanged) || len(state.RepositoryFindings) != 1 {
		t.Fatalf("universe completion findings=%d err=%v", len(state.RepositoryFindings), err)
	}
	job := lifecycleJobForFinding(t, state, second.ID)
	occurrence := state.Findings[findingIndexByID(state.Findings, second.ID)]
	if job.State != RepositoryMappingPending || occurrence.RepositoryFindingID != "" {
		t.Fatalf("requeued job=%#v occurrence=%#v", job, occurrence)
	}
}

func recordLifecycleFinding(
	t *testing.T,
	store Store,
	commit string,
	blob string,
	runID string,
	targetBranch string,
	defaultBranch string,
	targetIsDefault bool,
	title string,
	matchHintsOverride ...MatchHints,
) (RepositoryState, Finding) {
	t.Helper()
	file := FileRef{Path: "service.go", BlobSHA: blob, SizeBytes: 20, Category: "code", Mode: "100644"}
	plan, err := store.Plan(context.Background(), "owner/repo", commit, "inventory-"+runID, []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = BindPlanBranch(plan, targetBranch, defaultBranch, targetIsDefault)
	if err != nil {
		t.Fatal(err)
	}
	matchHints := MatchHints{
		Component: "scheduler", Operation: "requeue waiter", FailureMode: "stale owner",
		Trigger: "failed wake", ViolatedInvariant: "waiters use current owner",
		ObservableOutcome: "waiter remains blocked",
		RelatedSymbols:    []string{"Scheduler.Run"}, SourceAnchors: []string{"waiters"},
		DistinguishingFacts: []string{"requires failed wake"},
	}
	if len(matchHintsOverride) > 0 {
		matchHints = matchHintsOverride[0]
	}
	result, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: runID, TargetBranch: targetBranch,
		AdvertisedDefaultBranch: defaultBranch, TargetIsDefault: targetIsDefault,
		Observations: []Observation{{
			Model: "reviewer", Reviewer: "reviewer", ScopeFiles: []FileRef{file},
			Findings: []FindingCandidate{
				{
					Severity: "high",
					Title:    title,
					Symbol:   "Scheduler.Run",
					File:     file.Path,
					Message:  "The waiter remains attached to the old queue.",
					Evidence: "The failed wake path uses the stale queue owner.",
					Impact:   "The waiter remains blocked.",
					Validation: Validation{
						Status:  "confirmed",
						Summary: "Traced the stale owner.",
						Checks:  []string{"followed wake path"},
					},
					MatchHints: matchHints,
					FixEffort: FixEffort{
						Quick: FixEffortEstimate{
							LOCMin:    5,
							LOCMax:    20,
							Class:     "small",
							Rationale: "Localized containment.",
						},
						Quality: FixEffortEstimate{
							LOCMin:    30,
							LOCMax:    100,
							Class:     "medium",
							Rationale: "Ownership spans related units.",
						},
					},
				},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AcceptedFindingIDs) != 1 {
		t.Fatalf("accepted=%#v", result.AcceptedFindingIDs)
	}
	index := findingIndexByID(result.State.Findings, result.AcceptedFindingIDs[0])
	if index < 0 {
		t.Fatal("recorded finding missing")
	}
	return result.State, result.State.Findings[index]
}

func lifecycleJobForFinding(t *testing.T, state RepositoryState, findingID string) RepositoryMappingJob {
	t.Helper()
	for _, job := range state.MappingJobs {
		if job.ReviewFindingID == findingID {
			return job
		}
	}
	t.Fatalf("mapping job for %s missing: %#v", findingID, state.MappingJobs)
	return RepositoryMappingJob{}
}

func claimLifecycleMappingJob(
	t *testing.T,
	store Store,
	repository string,
	pendingJob RepositoryMappingJob,
	snapshot RepositoryMappingModelSnapshot,
) RepositoryMappingJob {
	t.Helper()
	state, claimedJob, finding, claimed, err := store.ClaimMappingJob(repository, pendingJob.ID, snapshot)
	if err != nil || !claimed || state.Repository != repository || claimedJob.ID != pendingJob.ID ||
		finding.ID != pendingJob.ReviewFindingID {
		t.Fatalf(
			"mapping claim=%v state=%#v job=%#v finding=%#v err=%v",
			claimed,
			state,
			claimedJob,
			finding,
			err,
		)
	}
	return claimedJob
}

func lifecycleValidationJobByID(t *testing.T, state RepositoryState, id string) RepositoryValidationJob {
	t.Helper()
	for _, job := range state.ValidationJobs {
		if job.ID == id {
			return job
		}
	}
	t.Fatalf("validation job %s missing", id)
	return RepositoryValidationJob{}
}

func TestLifecycleValidationBatchBoundaries(t *testing.T) {
	store := NewStore(filepath.Clean(t.TempDir()))
	if _, _, err := store.ReserveValidationJobs("owner/repo", nil, RepositoryMappingModelSnapshot{}); err == nil {
		t.Fatal("empty validation batch accepted")
	}
	if _, _, err := store.ReserveValidationJobs(
		"owner/repo", make([]string, maxValidationBatch+1), RepositoryMappingModelSnapshot{},
	); err == nil {
		t.Fatal("oversized validation batch accepted")
	}
	invalidState, invalidJob, invalidFinding, claimed, claimErr := store.ClaimMappingJob(
		"owner/repo", "missing", RepositoryMappingModelSnapshot{ProfileVersion: 1},
	)
	if claimErr == nil || claimed || invalidState.Repository != "" || invalidJob.ID != "" || invalidFinding.ID != "" {
		t.Fatalf(
			"invalid model snapshot result=%#v / %#v / %#v / %v, error=%v",
			invalidState,
			invalidJob,
			invalidFinding,
			claimed,
			claimErr,
		)
	}
	if _, _, err := store.SaveMappingAdjudication(
		"owner/repo", "missing", RepositoryMappingAdjudication{Decision: "same", Confidence: math.NaN()},
	); err == nil {
		t.Fatal("invalid adjudication accepted")
	}
}

func TestValidationSlotsAreWorkspaceWideAndBoundedToFour(t *testing.T) {
	store := NewStore(t.TempDir())
	releases := make([]func(), 0, RepositoryValidationConcurrency)
	for index := 0; index < RepositoryValidationConcurrency; index++ {
		release, err := store.AcquireValidationSlot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	blocked, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.AcquireValidationSlot(blocked); !errors.Is(err, context.Canceled) {
		t.Fatalf("fifth validation slot error=%v", err)
	}
	for _, release := range releases {
		release()
	}
	release, err := store.AcquireValidationSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
}
