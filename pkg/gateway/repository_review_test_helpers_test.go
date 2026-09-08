package gateway

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

const gatewayRepositoryReviewRunID = "gateway-publication-run"

func seedGatewayRepositoryReviewState(
	t *testing.T,
	workspace string,
	repository string,
) (repoaudit.Store, repoaudit.RepositoryState) {
	t.Helper()
	store := repoaudit.NewSQLiteStore(workspace)
	file := repoaudit.FileRef{
		Path: "service.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 120,
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
	if _, err := store.BeginCampaign(t.Context(), repoaudit.BeginCampaignRequest{
		Repository: repository, CampaignID: campaignID, CommitSHA: commit,
		DeduplicationSnapshot: &repoaudit.RepositoryReviewDeduplicationSnapshot{
			ReviewerModel: "review-model", DeduplicationModel: "review-model",
			SimilarityThreshold: repoaudit.DeduplicationDefaultThreshold,
			CandidateLimit:      repoaudit.DeduplicationDefaultCandidateLimit,
		},
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanAssignmentsForCampaign(
		t.Context(), repository, commit, "inventory-gateway", profileHash,
		campaignID, catalog, []repoaudit.FileRef{file}, false, 1, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, beginErr := store.BeginRepositoryReviewRun(
		t.Context(), repoaudit.BeginRepositoryReviewRunRequest{
			Plan: plan, RunID: gatewayRepositoryReviewRunID,
			ReviewableFiles: []repoaudit.FileRef{file},
		},
	); beginErr != nil {
		t.Fatal(beginErr)
	}
	line := 12
	for index, assignment := range plan.AssignmentPlans {
		observation := repoaudit.Observation{
			Model: "provider/review-model", ModelAlias: "review-model", Account: "gateway",
			Reviewer: assignment.FocusID, ScopeFiles: assignment.Files,
			RawDigest: "sha256:" + strings.Repeat("e", 64),
		}
		if index == 0 {
			observation.Findings = []repoaudit.FindingCandidate{{
				Severity: "high", Title: "Lost update", Symbol: "updateState",
				File: file.Path, Line: &line, Message: "The update is not fenced.",
				Evidence: "Two writers overwrite each other.", Impact: "Data is lost.",
				Validation: repoaudit.Validation{Status: "confirmed", Summary: "Reproduced."},
				MatchHints: repoaudit.MatchHints{
					Component: "state", Operation: "update", FailureMode: "lost update",
					Trigger: "concurrent writers", ViolatedInvariant: "updates are serialized",
					ObservableOutcome: "data is lost", RelatedSymbols: []string{"updateState"},
					SourceAnchors: []string{"updateState"}, DistinguishingFacts: []string{"concurrent write"},
				},
				FixEffort: gatewayRepositoryReviewFixEffort(),
			}}
		}
		if _, checkpointErr := store.CheckpointRepositoryReviewAssignment(
			t.Context(), repoaudit.CheckpointRepositoryReviewAssignmentRequest{
				Plan: plan, RunID: gatewayRepositoryReviewRunID,
				AssignmentID: assignment.AssignmentID,
				AutomationID: "rra_gateway_seed", AgentID: "main", ChildIndex: index + 1,
				Digest:            "sha256:" + strings.Repeat(fmt.Sprintf("%x", index+4), 64),
				AcknowledgedFiles: []repoaudit.FileRef{file}, Observation: observation,
			},
		); checkpointErr != nil {
			t.Fatal(checkpointErr)
		}
	}
	if _, finalizeErr := store.FinalizeRepositoryReviewRun(
		t.Context(),
		repoaudit.FinalizeRepositoryReviewRunRequest{
			Plan: plan, RunID: gatewayRepositoryReviewRunID,
		},
	); finalizeErr != nil {
		t.Fatal(finalizeErr)
	}
	if _, processErr := store.ProcessPendingDeduplicationJobs(
		t.Context(), repository, repoaudit.DeduplicationProcessOptions{},
	); processErr != nil {
		t.Fatal(processErr)
	}
	state, found, err := store.Get(repository)
	if err != nil || !found || len(state.Findings) != 1 {
		t.Fatalf("load gateway repository review: findings=%d found=%v err=%v", len(state.Findings), found, err)
	}
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
	return store, state
}

func gatewayRepositoryReviewFixEffort() repoaudit.FixEffort {
	return repoaudit.FixEffort{
		Quick: repoaudit.FixEffortEstimate{
			LOCMin: 1, LOCMax: 5, Class: "tiny", Rationale: "Localized.",
		},
		Quality: repoaudit.FixEffortEstimate{
			LOCMin: 6, LOCMax: 20, Class: "small", Rationale: "Bounded.",
		},
	}
}
