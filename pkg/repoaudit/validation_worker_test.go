package repoaudit

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestValidationWorkerConfirmsOnlySuppliedReachableCommit(t *testing.T) {
	store := NewStore(t.TempDir())
	state, occurrence := recordLifecycleFinding(
		t, store, strings.Repeat("1", 40), strings.Repeat("2", 40), "worker-run",
		"main", "main", true, "worker validation defect",
	)
	mapping := lifecycleJobForFinding(t, state, occurrence.ID)
	_, mapping, _, claimed, err := store.ClaimMappingJob(
		state.Repository, mapping.ID, RepositoryMappingModelSnapshot{Model: "reviewer"},
	)
	if err != nil || !claimed {
		t.Fatalf("mapping claim=%v err=%v", claimed, err)
	}
	state, aggregate, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: mapping.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.ReserveValidationJobs(
		state.Repository, []string{aggregate.ID}, RepositoryMappingModelSnapshot{Model: "reviewer"},
	)
	if err != nil {
		t.Fatal(err)
	}
	fix := strings.Repeat("3", 40)
	fixTime := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	result, err := store.ProcessPendingValidationJobs(t.Context(), state.Repository, RepositoryValidationProcessOptions{
		Evidence: func(context.Context, RepositoryFinding, []string) ([]RepositoryValidationEvidence, error) {
			return []RepositoryValidationEvidence{{
				CommitSHA: fix, CommitTime: fixTime, Summary: "restore current owner invariant",
			}}, nil
		},
		Adjudicate: func(
			context.Context,
			RepositoryMappingModelSnapshot,
			RepositoryFinding,
			[]RepositoryValidationEvidence,
		) (RepositoryValidationDecision, error) {
			return RepositoryValidationDecision{
				Outcome: RepositoryValidationConfirmed, SelectedCommitSHA: fix,
				Summary: "The supplied commit prevents stale-owner requeue.",
			}, nil
		},
		VerifyAncestry:   func(context.Context, string) (bool, error) { return true, nil },
		FirstSemanticTag: func(context.Context, string) (string, error) { return "v1.2.3", nil },
	})
	if err != nil || result.Confirmed != 1 {
		t.Fatalf("validation result=%#v err=%v", result, err)
	}
	current, _, _ := store.Get(state.Repository)
	resolved := current.RepositoryFindings[repositoryFindingIndexByID(current.RepositoryFindings, aggregate.ID)]
	if resolved.Lifecycle != RepositoryFindingResolved || resolved.FixCommitSHA != fix ||
		resolved.FirstContainingTag != "v1.2.3" {
		t.Fatalf("resolved finding=%#v", resolved)
	}
}

func TestValidationWorkerRequeuesStaleEvidenceAfterNewOccurrence(t *testing.T) {
	store := NewStore(t.TempDir())
	state, occurrence := recordLifecycleFinding(
		t, store, strings.Repeat("4", 40), strings.Repeat("5", 40), "stale-validation-base",
		"main", "main", true, "stale validation defect",
	)
	mapping := lifecycleJobForFinding(t, state, occurrence.ID)
	_, mapping, _, _, err := store.ClaimMappingJob(
		state.Repository, mapping.ID, RepositoryMappingModelSnapshot{},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, aggregate, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: mapping.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, later := recordLifecycleFinding(
		t, store, strings.Repeat("6", 40), strings.Repeat("7", 40), "stale-validation-later",
		"main", "main", true, "stale validation defect returns",
	)
	laterJob := lifecycleJobForFinding(t, state, later.ID)
	_, laterJob, _, _, err = store.ClaimMappingJob(
		state.Repository, laterJob.ID, RepositoryMappingModelSnapshot{},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.ReserveValidationJobs(
		state.Repository, []string{aggregate.ID}, RepositoryMappingModelSnapshot{Model: "reviewer"},
	)
	if err != nil {
		t.Fatal(err)
	}
	fix := strings.Repeat("8", 40)
	result, err := store.ProcessPendingValidationJobs(t.Context(), state.Repository, RepositoryValidationProcessOptions{
		Evidence: func(context.Context, RepositoryFinding, []string) ([]RepositoryValidationEvidence, error) {
			return []RepositoryValidationEvidence{{CommitSHA: fix, CommitTime: time.Now().UTC()}}, nil
		},
		Adjudicate: func(
			context.Context,
			RepositoryMappingModelSnapshot,
			RepositoryFinding,
			[]RepositoryValidationEvidence,
		) (RepositoryValidationDecision, error) {
			if _, _, completeErr := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
				JobID: laterJob.ID, RepositoryFindingID: aggregate.ID, DefaultBranchVerified: true,
			}); completeErr != nil {
				return RepositoryValidationDecision{}, completeErr
			}
			return RepositoryValidationDecision{
				Outcome: RepositoryValidationConfirmed, SelectedCommitSHA: fix, Summary: "stale result",
			}, nil
		},
		VerifyAncestry: func(context.Context, string) (bool, error) { return true, nil },
	})
	if err != nil || result.Completed != 0 || result.Confirmed != 0 {
		t.Fatalf("stale validation result=%#v err=%v", result, err)
	}
	current, _, _ := store.Get(state.Repository)
	job := lifecycleValidationJobByID(t, current, current.ValidationJobs[0].ID)
	aggregateIndex := repositoryFindingIndexByID(current.RepositoryFindings, aggregate.ID)
	currentAggregate := current.RepositoryFindings[aggregateIndex]
	if job.State != RepositoryValidationPending || job.CandidateCommits != nil ||
		currentAggregate.ValidationState != RepositoryValidationPending ||
		currentAggregate.Lifecycle == RepositoryFindingResolved {
		t.Fatalf("requeued job=%#v aggregate=%#v", job, currentAggregate)
	}
}
