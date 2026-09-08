package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func seedRepositoryReviewAPIState(t *testing.T, workspace string) repoaudit.RepositoryState {
	return seedRepositoryReviewAPIStateWithProcessing(t, workspace, true)
}

func seedRepositoryReviewAPIStateWithProcessing(
	t *testing.T,
	workspace string,
	process bool,
) repoaudit.RepositoryState {
	t.Helper()
	store := repoaudit.NewSQLiteStore(workspace)
	file := repoaudit.FileRef{
		Path: "pkg/service.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 120,
		Category: "code", Mode: "100644",
	}
	commit := strings.Repeat("b", 40)
	profileHash := "sha256:" + strings.Repeat("c", 64)
	catalog := make([]repoaudit.RepositoryReviewAssignment, 0, 4)
	for _, focus := range repoaudit.RepositoryReviewFocusIDs() {
		assignment, err := repoaudit.NewRepositoryReviewAssignment(
			focus, "review-model", "prompt-v1", profileHash, true,
		)
		if err != nil {
			t.Fatal(err)
		}
		catalog = append(catalog, assignment)
	}
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	if _, err := store.BeginCampaign(context.Background(), repoaudit.BeginCampaignRequest{
		Repository: "owner/repo", CampaignID: campaignID, CommitSHA: commit,
		DeduplicationSnapshot: &repoaudit.RepositoryReviewDeduplicationSnapshot{
			ReviewerModel: "review-model", DeduplicationModel: "review-model",
			SimilarityThreshold: repoaudit.DeduplicationDefaultThreshold,
			CandidateLimit:      repoaudit.DeduplicationDefaultCandidateLimit,
		},
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanAssignmentsForCampaign(
		context.Background(), "owner/repo", commit, "inventory-a", profileHash,
		campaignID, catalog, []repoaudit.FileRef{file}, false, 1, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, beginErr := store.BeginRepositoryReviewRun(
		context.Background(), repoaudit.BeginRepositoryReviewRunRequest{
			Plan: plan, RunID: "api-run", ReviewableFiles: []repoaudit.FileRef{file},
		},
	); beginErr != nil {
		t.Fatal(beginErr)
	}
	line := 12
	for index, assignment := range plan.AssignmentPlans {
		observation := repoaudit.Observation{
			Model: "provider/review-model", ModelAlias: "review-model", Account: "api",
			Reviewer: assignment.FocusID, ScopeFiles: assignment.Files,
			RawDigest: "sha256:" + strings.Repeat("e", 64),
		}
		if index == 0 {
			observation.Findings = []repoaudit.FindingCandidate{{
				Severity: "high", Title: "Lost update", Symbol: "updateState", File: file.Path, Line: &line,
				Message: "The update is not fenced.", Evidence: "Two writers overwrite each other.",
				Impact:     "Data is lost.",
				Validation: repoaudit.Validation{Status: "confirmed", Summary: "Reproduced"},
				MatchHints: repoaudit.MatchHints{
					Component: "state", Operation: "update", FailureMode: "lost update",
					Trigger: "concurrent writers", ViolatedInvariant: "updates are serialized",
					ObservableOutcome: "data is lost", RelatedSymbols: []string{"updateState"},
					SourceAnchors: []string{"updateState"}, DistinguishingFacts: []string{"concurrent write"},
				},
				FixEffort: testRepositoryReviewFixEffort(),
			}}
		}
		if _, checkpointErr := store.CheckpointRepositoryReviewAssignment(
			context.Background(), repoaudit.CheckpointRepositoryReviewAssignmentRequest{
				Plan: plan, RunID: "api-run", AssignmentID: assignment.AssignmentID,
				AutomationID: "rra_api_seed", AgentID: "main", ChildIndex: index + 1,
				Digest:            "sha256:" + strings.Repeat(fmt.Sprintf("%x", index+4), 64),
				AcknowledgedFiles: []repoaudit.FileRef{file}, Observation: observation,
			},
		); checkpointErr != nil {
			t.Fatal(checkpointErr)
		}
	}
	if _, finalizeErr := store.FinalizeRepositoryReviewRun(
		context.Background(),
		repoaudit.FinalizeRepositoryReviewRunRequest{Plan: plan, RunID: "api-run"},
	); finalizeErr != nil {
		t.Fatal(finalizeErr)
	}
	if process {
		if _, processErr := store.ProcessPendingDeduplicationJobs(
			context.Background(), "owner/repo", repoaudit.DeduplicationProcessOptions{},
		); processErr != nil {
			t.Fatal(processErr)
		}
	}
	state, found, err := store.Get("owner/repo")
	if err != nil || !found {
		t.Fatalf("load seeded repository review: found=%v err=%v", found, err)
	}
	return state
}

func seedCanonicalRepositoryReviewGenerationFindings(
	t *testing.T,
	workspace string,
	count int,
) repoaudit.RepositoryState {
	t.Helper()
	store := repoaudit.NewSQLiteStore(workspace)
	files := make([]repoaudit.FileRef, 0, count)
	findings := make([]repoaudit.FindingCandidate, 0, count)
	for index := 0; index < count; index++ {
		file := repoaudit.FileRef{
			Path:    fmt.Sprintf("pkg/file_%d.go", index+1),
			BlobSHA: strings.Repeat(string(rune('a'+index)), 40), SizeBytes: 100,
			Category: "code", Mode: "100644",
		}
		files = append(files, file)
		findings = append(findings, repoaudit.FindingCandidate{
			Severity: "high", Title: fmt.Sprintf("Finding %d", index+1),
			Symbol: fmt.Sprintf("operation%d", index+1), File: file.Path,
			Message: "The operation loses state.", Evidence: "The immutable source shows the failure.",
			Impact:     "Data is lost.",
			Validation: repoaudit.Validation{Status: "confirmed", Summary: "Confirmed from source."},
			MatchHints: repoaudit.MatchHints{
				Component: "batch", Operation: fmt.Sprintf("operation %d", index+1),
				FailureMode: "lost state", Trigger: "bounded input",
				ViolatedInvariant: "state is retained", ObservableOutcome: "data is lost",
				RelatedSymbols: []string{fmt.Sprintf("operation%d", index+1)},
				SourceAnchors:  []string{file.Path}, DistinguishingFacts: []string{fmt.Sprintf("case %d", index+1)},
			},
			FixEffort: testRepositoryReviewFixEffort(),
		})
	}
	commit := strings.Repeat("f", 40)
	profileHash := "sha256:" + strings.Repeat("1", 64)
	catalog := make([]repoaudit.RepositoryReviewAssignment, 0, 4)
	for _, focus := range repoaudit.RepositoryReviewFocusIDs() {
		assignment, err := repoaudit.NewRepositoryReviewAssignment(
			focus, "review-model", "prompt-v1", profileHash, true,
		)
		if err != nil {
			t.Fatal(err)
		}
		catalog = append(catalog, assignment)
	}
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	if _, err := store.BeginCampaign(t.Context(), repoaudit.BeginCampaignRequest{
		Repository: "owner/repo-batch", CampaignID: campaignID, CommitSHA: commit,
		DeduplicationSnapshot: &repoaudit.RepositoryReviewDeduplicationSnapshot{
			ReviewerModel: "review-model", DeduplicationModel: "review-model",
			SimilarityThreshold: repoaudit.DeduplicationDefaultThreshold,
			CandidateLimit:      repoaudit.DeduplicationDefaultCandidateLimit,
		},
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanAssignmentsForCampaign(
		t.Context(), "owner/repo-batch", commit, "inventory-batch", profileHash,
		campaignID, catalog, files, false, max(1, count), true,
	)
	if err != nil {
		t.Fatal(err)
	}
	const runID = "batch-generation-run"
	if _, beginErr := store.BeginRepositoryReviewRun(t.Context(), repoaudit.BeginRepositoryReviewRunRequest{
		Plan: plan, RunID: runID, ReviewableFiles: files,
	}); beginErr != nil {
		t.Fatal(beginErr)
	}
	for index, assignment := range plan.AssignmentPlans {
		observation := repoaudit.Observation{
			Model: "provider/review-model", ModelAlias: "review-model", Account: "api",
			Reviewer: assignment.FocusID, ScopeFiles: assignment.Files,
			RawDigest: "sha256:" + strings.Repeat("3", 64),
		}
		if index == 0 {
			observation.Findings = findings
		}
		if _, checkpointErr := store.CheckpointRepositoryReviewAssignment(
			t.Context(), repoaudit.CheckpointRepositoryReviewAssignmentRequest{
				Plan: plan, RunID: runID, AssignmentID: assignment.AssignmentID,
				AutomationID: "rra_batch_seed", AgentID: "main", ChildIndex: index + 1,
				Digest:            "sha256:" + strings.Repeat(fmt.Sprintf("%x", index+2), 64),
				AcknowledgedFiles: files, Observation: observation,
			},
		); checkpointErr != nil {
			t.Fatal(checkpointErr)
		}
	}
	if _, finalizeErr := store.FinalizeRepositoryReviewRun(
		t.Context(), repoaudit.FinalizeRepositoryReviewRunRequest{Plan: plan, RunID: runID},
	); finalizeErr != nil {
		t.Fatal(finalizeErr)
	}
	if _, processErr := store.ProcessPendingDeduplicationJobs(
		t.Context(), "owner/repo-batch", repoaudit.DeduplicationProcessOptions{},
	); processErr != nil {
		t.Fatal(processErr)
	}
	state, found, err := store.Get("owner/repo-batch")
	if err != nil || !found {
		t.Fatalf("load batch repository review: found=%v err=%v", found, err)
	}
	return state
}

func testRepositoryReviewFixEffort() repoaudit.FixEffort {
	return repoaudit.FixEffort{
		Quick:   repoaudit.FixEffortEstimate{LOCMin: 1, LOCMax: 5, Class: "tiny", Rationale: "Localized."},
		Quality: repoaudit.FixEffortEstimate{LOCMin: 6, LOCMax: 20, Class: "small", Rationale: "Bounded."},
	}
}

func completeRepositoryReviewAPIMappingJobs(
	t *testing.T,
	workspace string,
	state repoaudit.RepositoryState,
) repoaudit.RepositoryState {
	t.Helper()
	store := repoaudit.NewSQLiteStore(workspace)
	for _, pending := range state.MappingJobs {
		if pending.State != repoaudit.RepositoryMappingPending {
			continue
		}
		_, job, _, claimed, err := store.ClaimMappingJob(
			state.Repository, pending.ID, repoaudit.RepositoryMappingModelSnapshot{},
		)
		if err != nil || !claimed {
			t.Fatalf("claim mapping job %q: claimed=%v err=%v", pending.ID, claimed, err)
		}
		state, _, err = store.CompleteMappingJob(
			state.Repository,
			repoaudit.RepositoryMappingCompletion{
				JobID: job.ID, CreateMatchState: repoaudit.RepositoryMatchNew,
				DefaultBranchVerified: true,
			},
		)
		if err != nil {
			t.Fatalf("complete mapping job %q: %v", job.ID, err)
		}
	}
	return state
}

func setRepositoryReviewMutationHeaders(request *http.Request) {
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
}
