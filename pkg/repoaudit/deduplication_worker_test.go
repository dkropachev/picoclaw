package repoaudit

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeduplicationWorkerSerializesBucketAndPromotesOnlyDeduplicatedFinding(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "dedup-run", ReviewableFiles: fixture.files,
	}); err != nil {
		t.Fatal(err)
	}
	firstLine, secondLine := 12, 91
	first := repositoryReviewCampaignFinding(fixture.files[0], "first admitted diagnosis")
	first.Line = &firstLine
	second := repositoryReviewCampaignFinding(fixture.files[0], "equivalent later wording")
	second.Line = &secondLine
	checkpoint := assignmentCoverageCheckpoint(fixture, "dedup-run", 0, fixture.files)
	checkpoint.Observation.Findings = []FindingCandidate{first, second}
	result, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AcceptedFindingIDs) != 2 || len(result.State.RawFindings) != 2 ||
		len(result.State.DeduplicationJobs) != 2 || len(result.State.MappingJobs) != 0 {
		t.Fatalf("atomic checkpoint result = %#v", result)
	}
	var scoringCalls, judgingCalls atomic.Int32
	processed, err := fixture.store.ProcessPendingDeduplicationJobs(
		t.Context(), fixture.repository, DeduplicationProcessOptions{
			Score: func(
				_ context.Context,
				_ RepositoryReviewDeduplicationSnapshot,
				_ string,
				request DeduplicationScoringRequest,
			) (DeduplicationScoringResponse, error) {
				scoringCalls.Add(1)
				if _, _, loadErr := fixture.store.Get(fixture.repository); loadErr != nil {
					return DeduplicationScoringResponse{}, loadErr
				}
				scores := make([]DeduplicationCandidateScore, 0, len(request.Candidates))
				for _, candidate := range request.Candidates {
					scores = append(scores, DeduplicationCandidateScore{
						CandidateID: candidate.ID, Score: 97,
						Explanation: "Same mechanism, trigger, invariant, and outcome.",
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
				judgingCalls.Add(1)
				return DeduplicationJudgment{
					Decision: "duplicate", CandidateID: request.Candidates[0].OpaqueID,
				}, nil
			},
		},
	)
	if err != nil || processed.Completed != 2 || processed.Created != 1 ||
		processed.Duplicates != 1 || scoringCalls.Load() != 1 || judgingCalls.Load() != 1 {
		t.Fatalf("processed=%#v scoring=%d judging=%d err=%v", processed, scoringCalls.Load(), judgingCalls.Load(), err)
	}
	state, _, err := fixture.store.Get(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.DeduplicatedFindings) != 1 || len(state.MappingJobs) != 1 ||
		state.MappingJobs[0].ReviewFindingID != state.DeduplicatedFindings[0].ID ||
		len(state.DeduplicatedFindings[0].RawSourceIDs) != 2 ||
		state.DeduplicatedFindings[0].Title != first.Title {
		t.Fatalf("deduplicated state = %#v", state)
	}
	for _, raw := range state.RawFindings {
		if raw.State != RawFindingDeduplicationCompleted ||
			raw.DeduplicatedFindingID != state.DeduplicatedFindings[0].ID {
			t.Fatalf("raw finding was not retained: %#v", raw)
		}
	}
}

func TestDeduplicationWorkerRunsFourBucketsConcurrently(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 4, 4)
	if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "parallel-run", ReviewableFiles: fixture.files,
	}); err != nil {
		t.Fatal(err)
	}
	findings := make([]FindingCandidate, 0, len(fixture.files))
	for index, file := range fixture.files {
		findings = append(findings, repositoryReviewCampaignFinding(
			file, "bucket "+string(rune('a'+index)),
		))
	}
	seed := assignmentCoverageCheckpoint(fixture, "parallel-run", 0, fixture.files)
	seed.Observation.Findings = findings
	if _, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	if processed, err := fixture.store.ProcessPendingDeduplicationJobs(
		t.Context(), fixture.repository, DeduplicationProcessOptions{},
	); err != nil || processed.Created != 4 {
		t.Fatalf("seed processing=%#v err=%v", processed, err)
	}
	duplicates := assignmentCoverageCheckpoint(fixture, "parallel-run", 1, fixture.files)
	duplicates.Observation.Findings = append([]FindingCandidate(nil), findings...)
	if _, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), duplicates); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var current, maximum atomic.Int32
	options := DeduplicationProcessOptions{
		Score: func(
			_ context.Context,
			_ RepositoryReviewDeduplicationSnapshot,
			_ string,
			request DeduplicationScoringRequest,
		) (DeduplicationScoringResponse, error) {
			now := current.Add(1)
			for {
				seen := maximum.Load()
				if now <= seen || maximum.CompareAndSwap(seen, now) {
					break
				}
			}
			started <- struct{}{}
			<-release
			current.Add(-1)
			return DeduplicationScoringResponse{Scores: []DeduplicationCandidateScore{{
				CandidateID: request.Candidates[0].ID, Score: 100,
				Explanation: "Equivalent diagnosis.",
			}}}, nil
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
	}
	done := make(chan error, 1)
	go func() {
		_, err := fixture.store.ProcessPendingDeduplicationJobs(
			t.Context(), fixture.repository, options,
		)
		done <- err
	}()
	for range 4 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("four deduplication buckets did not run concurrently")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if maximum.Load() != 4 {
		t.Fatalf("maximum concurrent buckets = %d, want 4", maximum.Load())
	}
}

func TestDeduplicationWorkerCandidateLimitZeroSkipsModels(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	state, _, err := fixture.store.Get(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := RepositoryReviewDeduplicationSnapshot{
		ReviewerModel: "review-a", DeduplicationModel: "review-a",
		SimilarityThreshold: 90, CandidateLimit: 0,
	}
	if _, beginErr := fixture.store.BeginCampaign(t.Context(), BeginCampaignRequest{
		Repository: fixture.repository, CampaignID: fixture.campaignID,
		CommitSHA: fixture.plan.CommitSHA, ExpectedReviewVersion: state.ReviewVersion,
		DeduplicationSnapshot: &snapshot,
	}); beginErr != nil {
		t.Fatal(beginErr)
	}
	// Binding the legacy campaign snapshot advances its review CAS, so produce
	// a fresh plan before beginning the run.
	fixture.plan, err = fixture.store.PlanAssignmentsForCampaign(
		t.Context(), fixture.repository, fixture.plan.CommitSHA, fixture.plan.InventoryHash,
		fixture.plan.ProfileHash, fixture.campaignID, fixture.catalog, fixture.files,
		false, 1, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, beginErr := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "no-model-run", ReviewableFiles: fixture.files,
	}); beginErr != nil {
		t.Fatal(beginErr)
	}
	finding := repositoryReviewCampaignFinding(fixture.files[0], "same diagnosis")
	checkpoint := assignmentCoverageCheckpoint(fixture, "no-model-run", 0, fixture.files)
	checkpoint.Observation.Findings = []FindingCandidate{finding, finding}
	if _, checkpointErr := fixture.store.CheckpointRepositoryReviewAssignment(
		t.Context(),
		checkpoint,
	); checkpointErr != nil {
		t.Fatal(checkpointErr)
	}
	processed, err := fixture.store.ProcessPendingDeduplicationJobs(
		t.Context(), fixture.repository, DeduplicationProcessOptions{
			Score: func(context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationScoringRequest) (DeduplicationScoringResponse, error) {
				return DeduplicationScoringResponse{}, errors.New("scorer must not be called")
			},
			Judge: func(context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationJudgeRequest) (DeduplicationJudgment, error) {
				return DeduplicationJudgment{}, errors.New("judge must not be called")
			},
		},
	)
	if err != nil || processed.Created != 2 || processed.Duplicates != 0 {
		t.Fatalf("candidate-limit-zero processed=%#v err=%v", processed, err)
	}
}

func TestDeduplicationFailureRetainsReadableRawAndRetryMovesOnlyJobToTail(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "retry-run", ReviewableFiles: fixture.files,
	}); err != nil {
		t.Fatal(err)
	}
	checkpoint := assignmentCoverageCheckpoint(fixture, "retry-run", 0, fixture.files)
	checkpoint.Observation.Findings = []FindingCandidate{
		repositoryReviewCampaignFinding(fixture.files[0], "seed"),
		repositoryReviewCampaignFinding(fixture.files[0], "provider failure"),
	}
	if _, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), checkpoint); err != nil {
		t.Fatal(err)
	}
	options := DeduplicationProcessOptions{
		Score: func(context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationScoringRequest) (DeduplicationScoringResponse, error) {
			return DeduplicationScoringResponse{}, errors.New("provider unavailable: secret details")
		},
		Judge: func(context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationJudgeRequest) (DeduplicationJudgment, error) {
			return DeduplicationJudgment{}, errors.New("unexpected judge")
		},
	}
	for attempt := 0; attempt < DeduplicationAttemptLimit; attempt++ {
		_, _ = fixture.store.ProcessPendingDeduplicationJobs(t.Context(), fixture.repository, options)
	}
	state, _, err := fixture.store.Get(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	failed := state.RawFindings[1]
	originalOrdinal := failed.InsertionOrdinal
	if failed.State != RawFindingDeduplicationFailed || failed.Failure == nil ||
		strings.Contains(failed.Failure.Message, "secret") {
		t.Fatalf("unsafe or missing retained failure: %#v", failed)
	}
	state, retried, err := fixture.store.RetryDeduplication(fixture.repository, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	job := state.DeduplicationJobs[deduplicationJobIndexByRawID(state.DeduplicationJobs, failed.ID)]
	if retried.InsertionOrdinal != originalOrdinal || job.InsertionOrdinal <= originalOrdinal ||
		retried.State != RawFindingDeduplicationPending || job.State != DeduplicationJobPending {
		t.Fatalf("retry raw=%#v job=%#v", retried, job)
	}
}

func TestDeduplicationCompletionRejectsStaleUniverse(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "stale-run", ReviewableFiles: fixture.files,
	}); err != nil {
		t.Fatal(err)
	}
	seedRequest := assignmentCoverageCheckpoint(fixture, "stale-run", 0, fixture.files)
	seedRequest.Observation.Findings = []FindingCandidate{
		repositoryReviewCampaignFinding(fixture.files[0], "seed"),
	}
	if _, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), seedRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ProcessPendingDeduplicationJobs(
		t.Context(), fixture.repository, DeduplicationProcessOptions{},
	); err != nil {
		t.Fatal(err)
	}
	duplicateRequest := assignmentCoverageCheckpoint(fixture, "stale-run", 1, fixture.files)
	duplicateRequest.Observation.Findings = []FindingCandidate{
		repositoryReviewCampaignFinding(fixture.files[0], "duplicate"),
	}
	checkpoint, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), duplicateRequest)
	if err != nil {
		t.Fatal(err)
	}
	jobID := checkpoint.State.DeduplicationJobs[len(checkpoint.State.DeduplicationJobs)-1].ID
	_, claim, claimed, err := fixture.store.ClaimDeduplicationJob(
		fixture.repository, jobID, time.Minute,
	)
	if err != nil || !claimed || len(claim.Candidates) != 1 {
		t.Fatalf("claim=%#v claimed=%v err=%v", claim, claimed, err)
	}
	state, _, err := fixture.store.Get(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	state.DeduplicatedFindings[0].Version++
	state.Version++
	if saveErr := fixture.store.save(&state); saveErr != nil {
		t.Fatal(saveErr)
	}
	completedState, completedFinding, created, completionErr := fixture.store.CompleteDeduplicationJob(
		fixture.repository,
		DeduplicationCompletion{
			JobID: claim.Job.ID, LeaseID: claim.Job.LeaseID,
			CandidateUniverseDigest: claim.UniverseDigest,
			ShortlistedScores: []DeduplicationCandidateScore{{
				CandidateID: claim.Candidates[0].ID, Score: 100,
				Explanation: "Equivalent diagnosis.",
			}},
			Decision: DeduplicationJudgment{
				Decision: "duplicate", CandidateID: claim.Candidates[0].ID,
			},
		},
	)
	if !errors.Is(completionErr, ErrDeduplicationUniverseChanged) {
		t.Fatalf("stale completion error = %v", completionErr)
	}
	if completedState.Version != 0 || completedFinding.ID != "" || created {
		t.Fatalf(
			"stale completion returned state=%#v finding=%#v created=%v",
			completedState,
			completedFinding,
			created,
		)
	}
}

func TestDeduplicationAttemptLimitDoesNotBlockLaterBucketJob(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "attempt-limit-run", ReviewableFiles: fixture.files,
	}); err != nil {
		t.Fatal(err)
	}
	checkpoint := assignmentCoverageCheckpoint(fixture, "attempt-limit-run", 0, fixture.files)
	finding := repositoryReviewCampaignFinding(fixture.files[0], "same bucket")
	checkpoint.Observation.Findings = []FindingCandidate{finding, finding}
	result, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	state := result.State
	state.DeduplicationJobs[0].Attempts = DeduplicationAttemptLimit
	state.Version++
	if saveErr := fixture.store.save(&state); saveErr != nil {
		t.Fatal(saveErr)
	}
	processed, err := fixture.store.ProcessPendingDeduplicationJobs(
		t.Context(), fixture.repository, DeduplicationProcessOptions{},
	)
	if err != nil || processed.Failed != 1 || processed.Created != 1 {
		t.Fatalf("attempt-limit processing=%#v err=%v", processed, err)
	}
}
