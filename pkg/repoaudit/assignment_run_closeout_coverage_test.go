package repoaudit

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRepositoryReviewAssignmentRunCloseoutDefenses(t *testing.T) {
	t.Run("begin", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 1, 1)
		invalid := fixture.plan
		invalid.RequiredAssignments++
		invalid.ID = planDigest(invalid)
		if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
			Plan: invalid, RunID: "invalid", ReviewableFiles: fixture.files,
		}); err == nil {
			t.Fatal("invalid assignment plan began")
		}
		stale := fixture.plan
		stale.StateVersion++
		stale.ID = planDigest(stale)
		if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
			Plan: stale, RunID: "stale", ReviewableFiles: fixture.files,
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale begin error = %v", err)
		}
		if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "outside",
			ReviewableFiles: []FileRef{repositoryAuditTestFile("outside.go", "f", 1)},
		}); err == nil {
			t.Fatal("outside reviewable file began")
		}
		request := BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
		}
		first, err := fixture.store.BeginRepositoryReviewRun(nil, request)
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := fixture.store.BeginRepositoryReviewRun(t.Context(), request)
		if err != nil || replayed.Version != first.Version {
			t.Fatalf("begin replay = %#v err=%v", replayed, err)
		}
		competing := request
		competing.RunID = "competing"
		if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), competing); !errors.Is(err, ErrConflict) {
			t.Fatalf("competing begin error = %v", err)
		}
	})

	t.Run("completed reservation", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 1, 1)
		state, _, err := fixture.store.Get(fixture.repository)
		if err != nil {
			t.Fatal(err)
		}
		credited, err := CreditRepositoryReviewAssignment(
			state.CurrentCampaign.Paths[fixture.files[0].Path], fixture.catalog, fixture.catalog[0].ID,
		)
		if err != nil {
			t.Fatal(err)
		}
		state.CurrentCampaign.Paths[fixture.files[0].Path] = credited
		state.Version++
		if err := fixture.store.save(&state); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "completed", ReviewableFiles: fixture.files,
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("completed reservation error = %v", err)
		}
	})
}

func TestRepositoryReviewAssignmentVerifyCloseoutDefenses(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 2, 2)
	assignment := fixture.plan.AssignmentPlans[0]
	request := VerifyRepositoryReviewAssignmentRequest{
		Repository: fixture.repository, RunID: "run",
		AssignmentID: assignment.AssignmentID, Files: assignment.Files,
	}
	if err := fixture.store.VerifyRepositoryReviewAssignment(t.Context(), request); !errors.Is(err, ErrConflict) {
		t.Fatalf("verify without run error = %v", err)
	}
	reversed := request
	reversed.Files = []FileRef{fixture.files[1], fixture.files[0]}
	if err := fixture.store.VerifyRepositoryReviewAssignment(t.Context(), reversed); err == nil {
		t.Fatal("noncanonical dispatch scope accepted")
	}
	if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
	}); err != nil {
		t.Fatal(err)
	}
	wrongRun := request
	wrongRun.RunID = "wrong"
	if err := fixture.store.VerifyRepositoryReviewAssignment(t.Context(), wrongRun); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong-run dispatch error = %v", err)
	}
	unknown := request
	unknown.AssignmentID = "rra_unknown"
	if err := fixture.store.VerifyRepositoryReviewAssignment(t.Context(), unknown); !errors.Is(err, ErrConflict) {
		t.Fatalf("unknown dispatch error = %v", err)
	}
	subset := request
	subset.Files = fixture.files[:1]
	if err := fixture.store.VerifyRepositoryReviewAssignment(t.Context(), subset); !errors.Is(err, ErrConflict) {
		t.Fatalf("subset dispatch error = %v", err)
	}

	state, _, err := fixture.store.Get(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	credited, err := CreditRepositoryReviewAssignment(
		state.CurrentCampaign.Paths[fixture.files[0].Path], fixture.catalog, fixture.catalog[0].ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	state.CurrentCampaign.Paths[fixture.files[0].Path] = credited
	state.Version++
	if err := fixture.store.save(&state); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.VerifyRepositoryReviewAssignment(t.Context(), request); !errors.Is(err, ErrConflict) {
		t.Fatalf("completed dispatch error = %v", err)
	}
}

func TestRepositoryReviewAssignmentCheckpointCloseoutDefenses(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	checkpoint := assignmentCoverageCheckpoint(fixture, "run", 0, fixture.files)
	if _, err := fixture.store.CheckpointRepositoryReviewAssignment(
		t.Context(), checkpoint,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("checkpoint without run error = %v", err)
	}
	invalid := checkpoint
	invalid.Plan.RequiredAssignments++
	invalid.Plan.ID = planDigest(invalid.Plan)
	if _, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), invalid); err == nil {
		t.Fatal("checkpoint accepted invalid plan")
	}
	if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
	}); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*CheckpointRepositoryReviewAssignmentRequest){
		"run": func(value *CheckpointRepositoryReviewAssignmentRequest) { value.RunID = "wrong" },
		"assignment": func(value *CheckpointRepositoryReviewAssignmentRequest) {
			value.AssignmentID = "rra_unknown"
		},
		"scope": func(value *CheckpointRepositoryReviewAssignmentRequest) {
			value.Observation.ScopeFiles = nil
		},
		"acknowledgement": func(value *CheckpointRepositoryReviewAssignmentRequest) {
			value.AcknowledgedFiles = []FileRef{repositoryAuditTestFile("outside.go", "e", 1)}
		},
		"model": func(value *CheckpointRepositoryReviewAssignmentRequest) { value.Observation.Model = "" },
		"model alias": func(value *CheckpointRepositoryReviewAssignmentRequest) {
			value.Observation.ModelAlias = ""
		},
		"account": func(value *CheckpointRepositoryReviewAssignmentRequest) {
			value.Observation.Account = ""
		},
		"reviewer": func(value *CheckpointRepositoryReviewAssignmentRequest) {
			value.Observation.Reviewer = "wrong"
		},
		"raw digest": func(value *CheckpointRepositoryReviewAssignmentRequest) {
			value.Observation.RawDigest = "bad"
		},
		"summary": func(value *CheckpointRepositoryReviewAssignmentRequest) {
			value.Observation.Summary = "bad\x00summary"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := checkpoint
			candidate.Observation = checkpoint.Observation
			mutate(&candidate)
			if _, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), candidate); err == nil {
				t.Fatal("invalid checkpoint accepted")
			}
		})
	}
	unacknowledged := checkpoint
	unacknowledged.AcknowledgedFiles = nil
	unacknowledged.Observation.Findings = []FindingCandidate{
		repositoryReviewCampaignFinding(fixture.files[0], "not acknowledged"),
	}
	if _, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), unacknowledged); err == nil {
		t.Fatal("unacknowledged finding accepted")
	}
	invalidFinding := checkpoint
	invalidFinding.Observation.Findings = []FindingCandidate{{
		Severity: "high", Title: "invalid", File: fixture.files[0].Path,
		Validation: Validation{Status: "confirmed"},
	}}
	if _, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), invalidFinding); err == nil {
		t.Fatal("invalid finding checkpoint accepted")
	}
}

func TestRepositoryReviewAssignmentFinalizeCloseoutDefenses(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	if _, err := fixture.store.FinalizeRepositoryReviewRun(t.Context(), FinalizeRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "run",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("finalize without run error = %v", err)
	}
	if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
	}); err != nil {
		t.Fatal(err)
	}
	outside := FinalizeRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "run", UnsupportedFiles: []UnsupportedFile{{
			FileRef: repositoryAuditTestFile("outside.go", "f", 1), Reason: "outside",
		}},
	}
	if _, err := fixture.store.FinalizeRepositoryReviewRun(t.Context(), outside); err == nil {
		t.Fatal("outside unsupported file accepted")
	}
	blank := outside
	blank.UnsupportedFiles = []UnsupportedFile{{FileRef: fixture.files[0]}}
	if _, err := fixture.store.FinalizeRepositoryReviewRun(t.Context(), blank); err == nil {
		t.Fatal("blank unsupported reason accepted")
	}
	checkpoint := assignmentCoverageCheckpoint(fixture, "run", 0, fixture.files)
	if _, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), checkpoint); err != nil {
		t.Fatal(err)
	}
	overlap := FinalizeRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "run", UnsupportedFiles: []UnsupportedFile{{
			FileRef: fixture.files[0], Reason: "overlap",
		}},
	}
	if _, err := fixture.store.FinalizeRepositoryReviewRun(t.Context(), overlap); !errors.Is(err, ErrConflict) {
		t.Fatalf("checkpoint overlap error = %v", err)
	}
	result, err := fixture.store.FinalizeRepositoryReviewRun(t.Context(), FinalizeRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "run", ExcludedFiles: 1,
	})
	if err != nil || result.Run.ExcludedFiles != 1 {
		t.Fatalf("finalized run = %#v err=%v", result.Run, err)
	}
	different := fixture.plan
	different.TargetBranch = "other"
	different.ID = planDigest(different)
	if _, err := fixture.store.FinalizeRepositoryReviewRun(context.Background(), FinalizeRepositoryReviewRunRequest{
		Plan: different, RunID: "run",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched finalized replay error = %v", err)
	}
}

func TestRepositoryReviewAssignmentInterruptCloseoutDefenses(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.store.InterruptRepositoryReviewRun(
		canceled, fixture.repository, "run",
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled interrupt error = %v", err)
	}
	if _, err := fixture.store.InterruptRepositoryReviewRun(t.Context(), "", ""); err == nil {
		t.Fatal("invalid interrupt accepted")
	}
	if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.InterruptRepositoryReviewRun(
		t.Context(), fixture.repository, "wrong",
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong interrupt run error = %v", err)
	}
	if _, err := fixture.store.InterruptRepositoryReviewRun(
		t.Context(), fixture.repository, "run",
	); err != nil {
		t.Fatal(err)
	}
	if _, runID, err := fixture.store.InterruptAbandonedRepositoryReviewRun(
		t.Context(), fixture.repository,
	); err != nil || runID != "" {
		t.Fatalf("no-active abandoned interrupt run=%q err=%v", runID, err)
	}
	if _, _, err := fixture.store.InterruptAbandonedRepositoryReviewRun(t.Context(), ""); err == nil {
		t.Fatal("invalid abandoned interrupt accepted")
	}

	if !strings.Contains(fixture.repository, "assignment") {
		t.Fatal("unexpected fixture repository")
	}
}
