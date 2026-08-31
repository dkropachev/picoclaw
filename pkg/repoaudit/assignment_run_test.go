package repoaudit

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestRepositoryReviewAssignmentCheckpointsAreDurableAndMissingOnly(t *testing.T) {
	ctx := context.Background()
	store := NewStore(t.TempDir())
	repository := "owner/assignment-checkpoints"
	commit := strings.Repeat("a", 40)
	profileHash := "sha256:" + strings.Repeat("b", 64)
	files := []FileRef{
		repositoryAuditTestFile("a.go", "c", 10),
		repositoryAuditTestFile("b.go", "d", 20),
	}
	catalog := repositoryReviewAssignmentCatalogForTest(t, profileHash)
	campaignID := NewRepositoryReviewCampaignID()
	if _, err := store.BeginCampaign(ctx, BeginCampaignRequest{
		Repository: repository, CampaignID: campaignID, CommitSHA: commit,
		ExpectedReviewVersion: 0,
	}); err != nil {
		t.Fatal(err)
	}
	plan := repositoryReviewAssignmentPlanForTest(
		t, store, repository, commit, profileHash, campaignID, catalog, files,
	)
	if len(plan.AssignmentPlans) != 4 {
		t.Fatalf("assignment plans = %#v", plan.AssignmentPlans)
	}
	if _, err := store.BeginRepositoryReviewRun(ctx, BeginRepositoryReviewRunRequest{
		Plan: plan, RunID: "run-one", ReviewableFiles: files,
	}); err != nil {
		t.Fatal(err)
	}
	progressState, _, loadErr := store.Get(repository)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	progress := CurrentCampaignAssignmentProgress(progressState, campaignID)
	if progress.Total != 8 || progress.Completed != 0 || progress.Active != 8 || progress.Pending != 0 {
		t.Fatalf("reserved assignment progress = %#v", progress)
	}

	first := plan.AssignmentPlans[0]
	if err := store.VerifyRepositoryReviewAssignment(ctx, VerifyRepositoryReviewAssignmentRequest{
		Repository: repository, RunID: "run-one", AssignmentID: first.AssignmentID,
		Files: first.Files,
	}); err != nil {
		t.Fatal(err)
	}
	observation := Observation{
		Model: "provider/review-a", ModelAlias: "review-a", Account: "review-account",
		Reviewer: first.FocusID, ScopeFiles: first.Files,
		RawDigest: "sha256:" + strings.Repeat("9", 64),
		Findings:  []FindingCandidate{repositoryReviewCampaignFinding(files[0], "durable finding")},
	}
	checkpoint := CheckpointRepositoryReviewAssignmentRequest{
		Plan: plan, RunID: "run-one", AssignmentID: first.AssignmentID,
		Digest:            "sha256:" + strings.Repeat("1", 64),
		AcknowledgedFiles: []FileRef{files[0]}, Observation: observation,
	}
	committed, checkpointErr := store.CheckpointRepositoryReviewAssignment(ctx, checkpoint)
	if checkpointErr != nil || len(committed.AcceptedFindingIDs) != 1 {
		t.Fatalf("checkpoint = %#v, %v", committed, checkpointErr)
	}
	replayed, replayErr := store.CheckpointRepositoryReviewAssignment(ctx, checkpoint)
	if replayErr != nil || !replayed.Idempotent {
		t.Fatalf("idempotent checkpoint = %#v, %v", replayed, replayErr)
	}
	conflict := checkpoint
	conflict.Digest = "sha256:" + strings.Repeat("2", 64)
	if _, err := store.CheckpointRepositoryReviewAssignment(ctx, conflict); !errorsIsConflict(err) {
		t.Fatalf("conflicting checkpoint error = %v", err)
	}
	if err := store.VerifyRepositoryReviewAssignment(ctx, VerifyRepositoryReviewAssignmentRequest{
		Repository: repository, RunID: "run-one", AssignmentID: first.AssignmentID,
		Files: first.Files,
	}); !errorsIsConflict(err) {
		t.Fatalf("completed dispatch error = %v", err)
	}

	second := plan.AssignmentPlans[1]
	secondCheckpoint := CheckpointRepositoryReviewAssignmentRequest{
		Plan: plan, RunID: "run-one", AssignmentID: second.AssignmentID,
		Digest: "sha256:" + strings.Repeat("3", 64), AcknowledgedFiles: files,
		Observation: Observation{
			Model: "provider/review-a", ModelAlias: "review-a", Account: "review-account",
			Reviewer: second.FocusID, ScopeFiles: second.Files,
			RawDigest: "sha256:" + strings.Repeat("8", 64),
		},
	}
	if _, err := store.CheckpointRepositoryReviewAssignment(ctx, secondCheckpoint); err != nil {
		t.Fatal(err)
	}
	third := plan.AssignmentPlans[2]
	if _, err := store.CheckpointRepositoryReviewAssignment(ctx,
		CheckpointRepositoryReviewAssignmentRequest{
			Plan: plan, RunID: "run-one", AssignmentID: third.AssignmentID,
			Digest:            "sha256:" + strings.Repeat("4", 64),
			AcknowledgedFiles: []FileRef{files[0]},
			Observation: Observation{
				Model: "provider/review-a", ModelAlias: "review-a", Account: "review-account",
				Reviewer: third.FocusID, ScopeFiles: third.Files,
				RawDigest: "sha256:" + strings.Repeat("6", 64),
				Findings: []FindingCandidate{{
					Severity: "high", Title: "unconfirmed", File: files[0].Path,
					Evidence: "claim", Impact: "impact",
					Validation: Validation{Status: "unconfirmed", Summary: "not validated"},
				}},
			},
		}); err == nil {
		t.Fatal("unconfirmed checkpoint received durable credit")
	}
	if _, err := store.FinalizeRepositoryReviewRun(ctx, FinalizeRepositoryReviewRunRequest{
		Plan: plan, RunID: "run-one",
	}); err != nil {
		t.Fatal(err)
	}

	next := repositoryReviewAssignmentPlanForTest(
		t, store, repository, commit, profileHash, campaignID, catalog, files,
	)
	wantScopes := map[string][]FileRef{
		catalog[0].ID: {files[1]},
		catalog[2].ID: files,
		catalog[3].ID: files,
	}
	if len(next.AssignmentPlans) != len(wantScopes) {
		t.Fatalf("next assignment plans = %#v", next.AssignmentPlans)
	}
	for _, assignmentPlan := range next.AssignmentPlans {
		if !reflect.DeepEqual(assignmentPlan.Files, wantScopes[assignmentPlan.AssignmentID]) {
			t.Fatalf("scope for %s = %#v, want %#v", assignmentPlan.AssignmentID,
				assignmentPlan.Files, wantScopes[assignmentPlan.AssignmentID])
		}
	}
	state, _, stateErr := store.Get(repository)
	if stateErr != nil || len(state.Findings) != 1 || state.Findings[0].Title != "durable finding" {
		t.Fatalf("durable checkpoint state = %#v, %v", state.Findings, stateErr)
	}
}

func TestRepositoryReviewConcurrentAssignmentCheckpointsMergeAndSurviveInterrupt(t *testing.T) {
	ctx := context.Background()
	store := NewStore(t.TempDir())
	repository := "owner/assignment-concurrency"
	commit := strings.Repeat("e", 40)
	profileHash := "sha256:" + strings.Repeat("f", 64)
	file := repositoryAuditTestFile("service.go", "a", 30)
	catalog := repositoryReviewAssignmentCatalogForTest(t, profileHash)
	campaignID := NewRepositoryReviewCampaignID()
	if _, err := store.BeginCampaign(ctx, BeginCampaignRequest{
		Repository: repository, CampaignID: campaignID, CommitSHA: commit,
	}); err != nil {
		t.Fatal(err)
	}
	plan := repositoryReviewAssignmentPlanForTest(
		t, store, repository, commit, profileHash, campaignID, catalog, []FileRef{file},
	)
	if _, err := store.BeginRepositoryReviewRun(ctx, BeginRepositoryReviewRunRequest{
		Plan: plan, RunID: "run-concurrent", ReviewableFiles: []FileRef{file},
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for index, assignmentPlan := range plan.AssignmentPlans[:2] {
		wg.Add(1)
		go func(index int, assignmentPlan RepositoryReviewAssignmentPlan) {
			defer wg.Done()
			_, err := store.CheckpointRepositoryReviewAssignment(ctx,
				CheckpointRepositoryReviewAssignmentRequest{
					Plan: plan, RunID: "run-concurrent",
					AssignmentID:      assignmentPlan.AssignmentID,
					Digest:            "sha256:" + strings.Repeat(string(rune('1'+index)), 64),
					AcknowledgedFiles: []FileRef{file},
					Observation: Observation{
						Model:      "provider/review-a",
						ModelAlias: "review-a",
						Account:    "review-account",
						Reviewer:   assignmentPlan.FocusID,
						ScopeFiles: assignmentPlan.Files,
						RawDigest:  "sha256:" + strings.Repeat("7", 64),
					},
				})
			errs <- err
		}(index, assignmentPlan)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	interrupted, interruptErr := store.InterruptRepositoryReviewRun(ctx, repository, "run-concurrent")
	if interruptErr != nil || interrupted.ActiveReviewRun != nil || len(interrupted.Runs) != 1 ||
		!interrupted.Runs[0].Interrupted {
		t.Fatalf("interrupt = %#v, %v", interrupted.ActiveReviewRun, interruptErr)
	}
	firstPlan := plan.AssignmentPlans[0]
	replayed, replayErr := store.CheckpointRepositoryReviewAssignment(ctx,
		CheckpointRepositoryReviewAssignmentRequest{
			Plan: plan, RunID: "run-concurrent", AssignmentID: firstPlan.AssignmentID,
			Digest:            "sha256:" + strings.Repeat("1", 64),
			AcknowledgedFiles: []FileRef{file},
			Observation: Observation{
				Model: "provider/review-a", ModelAlias: "review-a", Account: "review-account",
				Reviewer: firstPlan.FocusID, ScopeFiles: firstPlan.Files,
				RawDigest: "sha256:" + strings.Repeat("7", 64),
			},
		})
	if replayErr != nil || !replayed.Idempotent {
		t.Fatalf("interrupted checkpoint replay = %#v, %v", replayed, replayErr)
	}
	conflictingReplay := CheckpointRepositoryReviewAssignmentRequest{
		Plan: plan, RunID: "run-concurrent", AssignmentID: firstPlan.AssignmentID,
		Digest:            "sha256:" + strings.Repeat("1", 64),
		AcknowledgedFiles: []FileRef{file},
		Observation: Observation{
			Model: "provider/review-a", ModelAlias: "review-a", Account: "review-account",
			Reviewer: firstPlan.FocusID, ScopeFiles: firstPlan.Files,
			RawDigest: "sha256:" + strings.Repeat("7", 64), Summary: "different output",
		},
	}
	if _, err := store.CheckpointRepositoryReviewAssignment(ctx, conflictingReplay); !errorsIsConflict(err) {
		t.Fatalf("conflicting interrupted replay error = %v", err)
	}
	if _, err := store.FinalizeRepositoryReviewRun(ctx, FinalizeRepositoryReviewRunRequest{
		Plan: plan, RunID: "run-concurrent",
	}); !errorsIsConflict(err) {
		t.Fatalf("interrupted finalize error = %v", err)
	}
	coverage := interrupted.CurrentCampaign.Paths[file.Path]
	if coverage.Completed || !coverage.Inspected || coverage.AssignmentBits == "" {
		t.Fatalf("merged coverage = %#v", coverage)
	}
	progress := CurrentCampaignAssignmentProgress(interrupted, campaignID)
	if progress.Total != 4 || progress.Completed != 2 || progress.Pending != 2 || progress.Active != 0 {
		t.Fatalf("completed progress = %#v", progress)
	}
	next := repositoryReviewAssignmentPlanForTest(
		t, store, repository, commit, profileHash, campaignID, catalog, []FileRef{file},
	)
	if len(next.PendingFiles) != 1 || len(next.AssignmentPlans) != 2 {
		t.Fatalf("interrupted run did not schedule only missing roles: %#v", next)
	}
	restartCampaignID := NewRepositoryReviewCampaignID()
	if _, err := store.BeginCampaign(ctx, BeginCampaignRequest{
		Repository: repository, CampaignID: restartCampaignID,
		ExpectedCampaignID: campaignID, CommitSHA: commit,
		ExpectedReviewVersion: interrupted.ReviewVersion,
	}); err != nil {
		t.Fatal(err)
	}
	restarted := repositoryReviewAssignmentPlanForTest(
		t, store, repository, commit, profileHash, restartCampaignID, catalog, []FileRef{file},
	)
	if len(restarted.PendingFiles) != 1 || len(restarted.AssignmentPlans) != 4 {
		t.Fatalf("fresh campaign did not reschedule assignments: %#v", restarted)
	}
	postRestartReplay, postReplayErr := store.CheckpointRepositoryReviewAssignment(ctx,
		CheckpointRepositoryReviewAssignmentRequest{
			Plan: plan, RunID: "run-concurrent", AssignmentID: firstPlan.AssignmentID,
			Digest:            "sha256:" + strings.Repeat("1", 64),
			AcknowledgedFiles: []FileRef{file},
			Observation: Observation{
				Model: "provider/review-a", ModelAlias: "review-a", Account: "review-account",
				Reviewer: firstPlan.FocusID, ScopeFiles: firstPlan.Files,
				RawDigest: "sha256:" + strings.Repeat("7", 64),
			},
		})
	if postReplayErr != nil || !postRestartReplay.Idempotent {
		t.Fatalf("archived replay after restart = %#v, %v", postRestartReplay, postReplayErr)
	}
}

func TestRepositoryReviewAssignmentCampaignDoesNotRetryForcedUnsupportedFile(t *testing.T) {
	ctx := context.Background()
	store := NewStore(t.TempDir())
	repository := "owner/assignment-unsupported"
	commit := strings.Repeat("b", 40)
	profileHash := "sha256:" + strings.Repeat("c", 64)
	file := repositoryAuditTestFile("large.go", "d", 1<<20)
	catalog := repositoryReviewAssignmentCatalogForTest(t, profileHash)
	campaignID := NewRepositoryReviewCampaignID()
	if _, err := store.BeginCampaign(ctx, BeginCampaignRequest{
		Repository: repository, CampaignID: campaignID, CommitSHA: commit,
	}); err != nil {
		t.Fatal(err)
	}
	plan, planErr := store.PlanAssignmentsForCampaign(
		ctx, repository, commit, "inventory", profileHash, campaignID,
		catalog, []FileRef{file}, true, 1, true,
	)
	if planErr != nil {
		t.Fatal(planErr)
	}
	if _, err := store.BeginRepositoryReviewRun(ctx, BeginRepositoryReviewRunRequest{
		Plan: plan, RunID: "run-unsupported", ReviewableFiles: []FileRef{},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeRepositoryReviewRun(ctx, FinalizeRepositoryReviewRunRequest{
		Plan: plan, RunID: "run-unsupported", UnsupportedFiles: []UnsupportedFile{{
			FileRef: file, Reason: "file_too_large",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	next, nextErr := store.PlanAssignmentsForCampaign(
		ctx, repository, commit, "inventory", profileHash, campaignID,
		catalog, []FileRef{file}, true, 1, true,
	)
	if nextErr != nil {
		t.Fatal(nextErr)
	}
	if len(next.PendingFiles) != 0 || len(next.AssignmentPlans) != 0 ||
		len(next.UnsupportedFiles) != 1 || next.UnsupportedFiles[0].Reason != "file_too_large" {
		t.Fatalf("forced unsupported file was scheduled again: %#v", next)
	}
}

func TestRepositoryReviewFinalizedCheckpointReplayUsesFrozenReviewableSubset(t *testing.T) {
	ctx := context.Background()
	store := NewStore(t.TempDir())
	repository := "owner/assignment-subset"
	commit := strings.Repeat("6", 40)
	profileHash := "sha256:" + strings.Repeat("7", 64)
	files := []FileRef{
		repositoryAuditTestFile("reviewable.go", "8", 10),
		repositoryAuditTestFile("unavailable.go", "9", 20),
	}
	catalog := repositoryReviewAssignmentCatalogForTest(t, profileHash)
	campaignID := NewRepositoryReviewCampaignID()
	if _, err := store.BeginCampaign(ctx, BeginCampaignRequest{
		Repository: repository, CampaignID: campaignID, CommitSHA: commit,
	}); err != nil {
		t.Fatal(err)
	}
	plan := repositoryReviewAssignmentPlanForTest(
		t, store, repository, commit, profileHash, campaignID, catalog, files,
	)
	if _, err := store.BeginRepositoryReviewRun(ctx, BeginRepositoryReviewRunRequest{
		Plan: plan, RunID: "run-subset", ReviewableFiles: files[:1],
	}); err != nil {
		t.Fatal(err)
	}
	assignmentPlan := plan.AssignmentPlans[0]
	checkpoint := CheckpointRepositoryReviewAssignmentRequest{
		Plan: plan, RunID: "run-subset", AssignmentID: assignmentPlan.AssignmentID,
		Digest:            "sha256:" + strings.Repeat("a", 64),
		AcknowledgedFiles: files[:1],
		Observation: Observation{
			Model:      "provider/review-a",
			ModelAlias: "review-a",
			Account:    "review-account",
			Reviewer:   assignmentPlan.FocusID,
			ScopeFiles: files[:1],
			RawDigest:  "sha256:" + strings.Repeat("b", 64),
		},
	}
	if _, err := store.CheckpointRepositoryReviewAssignment(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeRepositoryReviewRun(ctx, FinalizeRepositoryReviewRunRequest{
		Plan: plan, RunID: "run-subset",
	}); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.CheckpointRepositoryReviewAssignment(ctx, checkpoint)
	if err != nil || !replayed.Idempotent {
		t.Fatalf("subset checkpoint replay = %#v, %v", replayed, err)
	}
	next := repositoryReviewAssignmentPlanForTest(
		t, store, repository, commit, profileHash, campaignID, catalog, files,
	)
	if _, err := store.BeginRepositoryReviewRun(ctx, BeginRepositoryReviewRunRequest{
		Plan: next, RunID: "run-subset", ReviewableFiles: files,
	}); !errorsIsConflict(err) {
		t.Fatalf("retained run ID reuse error = %v", err)
	}
}

func TestRepositoryReviewBeginRunRejectsRehashedCampaignScopeDrift(t *testing.T) {
	ctx := context.Background()
	store := NewStore(t.TempDir())
	repository := "owner/assignment-forged-scope"
	commit := strings.Repeat("c", 40)
	profileHash := "sha256:" + strings.Repeat("d", 64)
	file := repositoryAuditTestFile("trusted.go", "e", 10)
	catalog := repositoryReviewAssignmentCatalogForTest(t, profileHash)
	campaignID := NewRepositoryReviewCampaignID()
	if _, err := store.BeginCampaign(ctx, BeginCampaignRequest{
		Repository: repository, CampaignID: campaignID, CommitSHA: commit,
	}); err != nil {
		t.Fatal(err)
	}
	plan := repositoryReviewAssignmentPlanForTest(
		t, store, repository, commit, profileHash, campaignID, catalog, []FileRef{file},
	)
	forged := repositoryAuditTestFile("forged.go", "f", 10)
	plan.PendingFiles[0] = forged
	for index := range plan.AssignmentPlans {
		plan.AssignmentPlans[index].Files[0] = forged
	}
	plan.ID = planDigest(plan)
	if _, err := store.BeginRepositoryReviewRun(ctx, BeginRepositoryReviewRunRequest{
		Plan: plan, RunID: "run-forged", ReviewableFiles: []FileRef{forged},
	}); !errorsIsConflict(err) {
		t.Fatalf("forged campaign scope error = %v", err)
	}
}

func TestInterruptAbandonedRepositoryReviewRunReleasesUnknownWork(t *testing.T) {
	ctx := context.Background()
	store := NewStore(t.TempDir())
	repository := "owner/assignment-abandoned"
	commit := strings.Repeat("1", 40)
	profileHash := "sha256:" + strings.Repeat("2", 64)
	file := repositoryAuditTestFile("pending.go", "3", 10)
	catalog := repositoryReviewAssignmentCatalogForTest(t, profileHash)
	campaignID := NewRepositoryReviewCampaignID()
	if _, err := store.BeginCampaign(ctx, BeginCampaignRequest{
		Repository: repository, CampaignID: campaignID, CommitSHA: commit,
	}); err != nil {
		t.Fatal(err)
	}
	plan := repositoryReviewAssignmentPlanForTest(
		t, store, repository, commit, profileHash, campaignID, catalog, []FileRef{file},
	)
	if _, err := store.BeginRepositoryReviewRun(ctx, BeginRepositoryReviewRunRequest{
		Plan: plan, RunID: "run-abandoned", ReviewableFiles: []FileRef{file},
	}); err != nil {
		t.Fatal(err)
	}
	interrupted, runID, err := store.InterruptAbandonedRepositoryReviewRun(ctx, repository)
	if err != nil || runID != "run-abandoned" || interrupted.ActiveReviewRun != nil ||
		len(interrupted.Runs) != 1 || !interrupted.Runs[0].Interrupted ||
		interrupted.Runs[0].RemainingFiles != 1 {
		t.Fatalf("abandoned interruption state=%#v run=%q err=%v", interrupted, runID, err)
	}
	next := repositoryReviewAssignmentPlanForTest(
		t, store, repository, commit, profileHash, campaignID, catalog, []FileRef{file},
	)
	if len(next.AssignmentPlans) != 4 {
		t.Fatalf("abandoned unknown assignments were not retried: %#v", next.AssignmentPlans)
	}
}

func repositoryReviewAssignmentCatalogForTest(
	t *testing.T,
	profileHash string,
) []RepositoryReviewAssignment {
	t.Helper()
	catalog := make([]RepositoryReviewAssignment, 0, 4)
	for _, focusID := range RepositoryReviewFocusIDs() {
		assignment, err := NewRepositoryReviewAssignment(
			focusID, "review-a", "prompt-v1", profileHash, true,
		)
		if err != nil {
			t.Fatal(err)
		}
		catalog = append(catalog, assignment)
	}
	return catalog
}

func repositoryReviewAssignmentPlanForTest(
	t *testing.T,
	store Store,
	repository string,
	commit string,
	profileHash string,
	campaignID string,
	catalog []RepositoryReviewAssignment,
	files []FileRef,
) Plan {
	t.Helper()
	plan, err := store.PlanAssignmentsForCampaign(
		context.Background(), repository, commit, "inventory", profileHash,
		campaignID, catalog, files, false, max(1, len(files)), true,
	)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func errorsIsConflict(err error) bool {
	return err != nil && (err == ErrConflict || strings.Contains(err.Error(), ErrConflict.Error()))
}
