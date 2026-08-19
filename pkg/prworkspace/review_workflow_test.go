package prworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type recordingReviewWorkflow struct {
	requests []ReviewWorkflowRequest
}

func (workflow *recordingReviewWorkflow) ExecuteReviewWorkflow(
	_ context.Context,
	request ReviewWorkflowRequest,
) (ReviewWorkflowResult, error) {
	workflow.requests = append(workflow.requests, request)
	return ReviewWorkflowResult{Rounds: []ReviewRound{{
		Initial: true, State: ExecutionSucceeded,
		PromptDigest: "sha256:" + strings.Repeat("a", 64),
		Result:       ReviewPass{Summary: "Reusable review completed", Coverage: Coverage{}},
	}}}, nil
}

func TestImplementationReentryConsumesTypedReviewWorkflowHandoff(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	provider := ProviderSnapshot{
		Provider: "github", ProviderOrigin: "https://github.com", RepositoryID: "1",
		Repository: "octo/repo", PullRequestID: "2", PullNumber: 3,
		HeadRepositoryID: "1", HeadRepository: "octo/repo",
		BaseSHA: "base", HeadSHA: "head", ObservedAt: now, Owned: true, HeadWritable: true,
	}
	store := NewMemoryStore()
	reviewWorkflow := &recordingReviewWorkflow{}
	service, err := NewService(ServiceConfig{
		Store: store, Provider: serviceProvider{snapshot: provider}, AI: serviceAI{},
		ReviewEvidence: serviceReviewEvidence{}, ReviewWorkflow: reviewWorkflow,
		Gates: passingGates{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := service.Create(context.Background(), CreateWorkspaceRequest{
		RequestID: "request-review-handoff-01",
		Resolve:   ResolveRequest{PullRequestURL: "https://github.com/octo/repo/pull/3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err = service.DraftCharter(context.Background(), DraftCharterRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-review-handoff-02",
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err = service.ConfirmCharter(context.Background(), ConfirmCharterRequest{
		WorkspaceID: aggregate.Workspace.ID, CharterID: aggregate.Charters[0].ID,
		ExpectedVersion: aggregate.Workspace.Version, RequestID: "request-review-handoff-03",
	})
	if err != nil {
		t.Fatal(err)
	}
	implementationPhase, waiting := PhaseImplementation, ExecutionWaitingUser
	mutated, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-review-handoff-04",
		Patch:     AggregatePatch{Phase: &implementationPhase, ExecutionState: &waiting},
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate = mutated.Aggregate
	aggregate, err = service.ReviseCharter(context.Background(), ReviseCharterRequest{
		SaveCharterRequest: SaveCharterRequest{
			WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
			RequestID: "request-review-handoff-05",
			Draft: CharterDraftOutput{
				Type: PRTypeFix, Goal: "Fix retry handling under the revised boundary",
				AcceptanceCriteria: []string{"Retries once"}, IncludedAreas: []string{"pkg/retry"},
				ExcludedAreas: []string{"unrelated cleanup"}, NonGoals: []string{"new feature"},
			},
		},
		ExpectedCharterRevision: aggregate.Charters[0].Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	revised := aggregate.Charters[len(aggregate.Charters)-1]
	aggregate, err = service.ConfirmCharter(context.Background(), ConfirmCharterRequest{
		WorkspaceID: aggregate.Workspace.ID, CharterID: revised.ID,
		ExpectedVersion: aggregate.Workspace.Version, RequestID: "request-review-handoff-06",
	})
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Workspace.Phase != PhaseReview || aggregate.Workspace.ActiveCharterID != revised.ID {
		t.Fatalf("revised implementation handoff = %#v", aggregate.Workspace)
	}
	aggregate, err = service.RunReview(context.Background(), RunReviewRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID:   "request-review-handoff-07",
		NudgePolicy: ConfiguredNudgePolicy(0, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewWorkflow.requests) != 1 {
		t.Fatalf("review workflow calls = %d, want 1", len(reviewWorkflow.requests))
	}
	request := reviewWorkflow.requests[0]
	if request.Handoff.WorkspaceID != aggregate.Workspace.ID || request.Handoff.CharterID != revised.ID ||
		request.Handoff.HeadSHA != provider.HeadSHA || request.Context.Charter.ID != revised.ID ||
		request.Context.CandidateDiff == "" {
		t.Fatalf("typed review handoff = %#v context=%#v", request.Handoff, request.Context)
	}
	encoded, marshalErr := json.Marshal(request.Context)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, forbidden := range []string{
		"candidate-metrics", "validation", "repair", "publication-fence",
		"head-writable", "can-create-issue", "authenticated-user-id", "owned",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("implementation-specific field %q crossed review boundary: %s", forbidden, encoded)
		}
	}
}

func TestReviewWorkflowContextDropsAndRejectsImplementationOnlyInputs(t *testing.T) {
	bundle := testPromptBundle()
	bundle.Provider.HeadSHA = "head"
	bundle.Charter.HeadSHA = "head"
	bundle.CandidateDiff = "diff --git a/a.go b/a.go\n"
	bundle.CandidateMetrics = CandidateMetrics{Files: 99}
	bundle.Validation = map[string]any{"implementation": true}
	bundle.Messages = []Message{
		{Stage: "review", Content: "review guidance"},
		{Stage: "implementation", Content: "implementation guidance"},
	}
	bundle.Corrections = []Correction{
		{Applicability: CorrectionReviewOnly, Correction: "review correction"},
		{Applicability: CorrectionImplementationOnly, Correction: "implementation correction"},
	}
	bundle.PriorEvidence = []StageEvidence{
		{Stage: "review", Summary: "review evidence"},
		{Stage: "completion_audit", Summary: "implementation evidence", Validation: map[string]any{"secret": true}},
	}
	bundle.NudgeLearning = []NudgeLearningExample{
		{Stage: NudgeReviewSearch},
		{Stage: NudgeImplementationDone},
	}

	review := reviewWorkflowContext(bundle)
	if len(review.Messages) != 1 || review.Messages[0].Stage != "review" ||
		len(review.Corrections) != 1 || review.Corrections[0].Applicability != CorrectionReviewOnly ||
		len(review.PriorEvidence) != 1 || review.PriorEvidence[0].Stage != "review" ||
		len(review.NudgeLearning) != 1 || review.NudgeLearning[0].Stage != NudgeReviewSearch {
		t.Fatalf("review-only projection = %#v", review)
	}
	encoded, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "candidate-metrics") ||
		strings.Contains(string(encoded), "implementation evidence") ||
		strings.Contains(string(encoded), "implementation guidance") ||
		strings.Contains(string(encoded), "implementation correction") {
		t.Fatalf("implementation-only data crossed review projection: %s", encoded)
	}
	reviewPrompt, err := CompilePrompt(PromptReviewSearch, bundle, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"candidate_metrics", "head_writable", "can_create_issue",
		"authenticated_user_id", "\"owned\"", "implementation evidence",
		"implementation guidance", "implementation correction",
	} {
		if strings.Contains(reviewPrompt.UserPrompt, forbidden) {
			t.Fatalf(
				"implementation-specific prompt field %q crossed review boundary: %s",
				forbidden,
				reviewPrompt.UserPrompt,
			)
		}
	}

	review.Messages = append(review.Messages, Message{Stage: "implementation", Content: "not allowed"})
	runner := &scriptedIsolatedAI{}
	_, err = newIsolatedReviewWorkflow(runner).ExecuteReviewWorkflow(context.Background(), ReviewWorkflowRequest{
		Mode: ReviewWorkflowFull,
		Handoff: ReviewWorkflowHandoff{
			WorkspaceID: review.WorkspaceID, CharterID: review.Charter.ID, HeadSHA: review.Charter.HeadSHA,
		},
		Context: review, NudgePolicy: ConfiguredNudgePolicy(0, 0),
	})
	if err == nil || len(runner.operations) != 0 {
		t.Fatalf("implementation-only direct context err=%v operations=%v", err, runner.operations)
	}
}

func TestReviewWorkflowResultRejectsMalformedExecutorOutput(t *testing.T) {
	succeeded := ReviewRound{State: ExecutionSucceeded, Result: ReviewPass{Summary: "reviewed"}}
	failed := ReviewRound{State: ExecutionFailed}
	tests := []struct {
		name   string
		result ReviewWorkflowResult
		runErr error
	}{
		{name: "no rounds"},
		{name: "too many", result: ReviewWorkflowResult{Rounds: []ReviewRound{succeeded, succeeded}}},
		{name: "unbound failure", result: ReviewWorkflowResult{Rounds: []ReviewRound{failed}}},
		{
			name:   "unbound error",
			result: ReviewWorkflowResult{Rounds: []ReviewRound{succeeded}},
			runErr: errors.New("failed"),
		},
		{name: "nonterminal", result: ReviewWorkflowResult{Rounds: []ReviewRound{{State: ExecutionRunning}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateReviewWorkflowResult(
				ReviewWorkflowAdditional,
				ConfiguredNudgePolicy(0, 0),
				test.result,
				test.runErr,
			); err == nil {
				t.Fatalf("malformed result accepted: %#v", test.result)
			}
		})
	}
}
