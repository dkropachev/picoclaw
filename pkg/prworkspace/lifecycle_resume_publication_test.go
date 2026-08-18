package prworkspace

import (
	"context"
	"strings"
	"testing"
	"time"
)

type captureReviewAI struct {
	calls int
	user  string
}

func (runner *captureReviewAI) RunIsolated(_ context.Context, request IsolatedAIRequest) (map[string]any, error) {
	if request.Operation == "review.initial" {
		runner.calls++
		runner.user = request.UserPrompt
		return map[string]any{
			"summary": "reviewed exact candidate", "findings": []any{},
			"coverage": map[string]any{
				"reviewed_areas": []any{"diff"}, "unreviewed_areas": []any{},
				"tests_considered": []any{}, "residual_risks": []any{},
			},
		}, nil
	}
	return nil, context.Canceled
}

func TestReviewHumanGatesResumeWithoutRerunningAIAndUseExactDiff(t *testing.T) {
	store := NewMemoryStore()
	created, err := store.Create(context.Background(), testCreateInput())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	charter := Charter{
		ID: "pcr_22222222222222222222222222222222", Revision: 1,
		Type: PRTypeFix, Goal: "fix retry", AcceptanceCriteria: []string{"retry succeeds"},
		HeadSHA: created.Aggregate.ProviderSnapshot.HeadSHA, Confirmed: true, CreatedAt: now,
	}
	phase, state, active := PhaseReview, ExecutionQueued, charter.ID
	ready, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: created.Aggregate.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-00000002", Patch: AggregatePatch{
			Phase: &phase, ExecutionState: &state, ActiveCharterID: &active,
			AppendCharters: []Charter{charter},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ai := &captureReviewAI{}
	service, err := NewService(ServiceConfig{
		Store: store, AI: ai, ReviewEvidence: serviceReviewEvidence{}, Gates: testAllWaitingGates{},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	waitingStart, err := service.RunReview(context.Background(), RunReviewRequest{
		WorkspaceID: ready.Aggregate.Workspace.ID, ExpectedVersion: ready.Aggregate.Workspace.Version,
		RequestID: "request-00000003", NudgePolicy: NudgePolicy{},
	})
	if err != nil || ai.calls != 0 || len(waitingStart.Gates) != 1 {
		t.Fatalf("start gate = calls %d gates %#v err %v", ai.calls, waitingStart.Gates, err)
	}
	afterStart, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID: waitingStart.Workspace.ID, GateRunID: waitingStart.Gates[0].ID,
		ExpectedVersion: waitingStart.Workspace.Version, RequestID: "request-00000004",
		FieldValues: map[string]any{"action": "continue"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitingComplete, err := service.RunReview(context.Background(), RunReviewRequest{
		WorkspaceID: afterStart.Workspace.ID, ExpectedVersion: afterStart.Workspace.Version,
		RequestID: "request-00000005", NudgePolicy: ConfiguredNudgePolicy(0, 0),
	})
	if err != nil || ai.calls != 1 || len(waitingComplete.Gates) != 2 || len(waitingComplete.StageRuns) != 1 ||
		waitingComplete.StageRuns[0].State != ExecutionWaitingGate || !strings.Contains(ai.user, "diff --git a/retry.go b/retry.go") {
		t.Fatalf("review wait = calls %d gates %d stage %#v diff=%v err=%v", ai.calls, len(waitingComplete.Gates), waitingComplete.StageRuns, strings.Contains(ai.user, "diff --git"), err)
	}
	completed, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID: waitingComplete.Workspace.ID, GateRunID: waitingComplete.Gates[1].ID,
		ExpectedVersion: waitingComplete.Workspace.Version, RequestID: "request-00000006",
		FieldValues: map[string]any{"action": "accept"},
	})
	if err != nil || completed.Workspace.Phase != PhaseTriage || completed.StageRuns[0].State != ExecutionSucceeded || ai.calls != 1 {
		t.Fatalf("completed = phase %q stage %q calls %d err %v", completed.Workspace.Phase, completed.StageRuns[0].State, ai.calls, err)
	}
}

type successfulReviewPublisher struct{}

func (successfulReviewPublisher) PublishReview(_ context.Context, request ReviewPublicationRequest) (ReviewPublicationResult, error) {
	if request.Marker == "" {
		return ReviewPublicationResult{}, ErrInvalid
	}
	return ReviewPublicationResult{ExternalID: "77", ExternalURL: "https://github.com/octo/repo/pull/3#pullrequestreview-77"}, nil
}

func (successfulReviewPublisher) ReconcileReview(context.Context, ReviewPublicationRequest) (ReviewPublicationResult, bool, error) {
	return ReviewPublicationResult{}, false, nil
}

type successfulBranchPublisher struct{}

func (successfulBranchPublisher) PublishBranch(_ context.Context, request BranchPublicationRequest) (BranchPublicationResult, error) {
	return BranchPublicationResult{ExternalID: request.Repair.CandidateSHA, ExternalURL: "https://github.com/octo/repo/pull/3"}, nil
}

func (successfulBranchPublisher) ReconcileBranch(context.Context, BranchPublicationRequest) (BranchPublicationResult, bool, error) {
	return BranchPublicationResult{}, false, nil
}

func TestReviewAndImplementationPublicationsShareOneAggregate(t *testing.T) {
	store := NewMemoryStore()
	input := testCreateInput()
	input.Provider.State, input.Provider.CanReview, input.Provider.HeadWritable = "open", true, true
	input.Provider.ProviderRevision = "sha256:provider"
	created, err := store.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	now := input.Workspace.CreatedAt
	charter := Charter{
		ID: "pcr_33333333333333333333333333333333", Type: PRTypeFix, Goal: "fix retry",
		HeadSHA: input.Provider.HeadSHA, Confirmed: true, CreatedAt: now,
	}
	reviewStage := StageRun{
		ID: "psr_33333333333333333333333333333333", Stage: "review", State: ExecutionSucceeded,
		CharterID: charter.ID, HeadSHA: input.Provider.HeadSHA, Summary: "one finding", StartedAt: now, FinishedAt: &now,
	}
	finding := Finding{
		ID: "pfn_33333333333333333333333333333333", Fingerprint: "sha256:finding",
		Origin: FindingOriginReview, OriginRunID: reviewStage.ID, Severity: "high", Title: "retry", Message: "retry fails",
		Scope:       ScopeAssessment{Distance: ScopeExact, Size: ChangeSizeXS, TypeCompatible: true, Confidence: 1},
		Disposition: FindingInScope, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	seeded, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: created.Aggregate.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-00000010", Patch: AggregatePatch{
			ActiveCharterID: &charter.ID, AppendCharters: []Charter{charter},
			AppendStageRuns: []StageRun{reviewStage}, UpsertFindings: []Finding{finding},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(ServiceConfig{Store: store, Gates: passingGates{}, Now: func() time.Time { return now }})
	queuedReview, err := service.QueueReviewPublication(context.Background(), QueueReviewPublicationRequest{
		WorkspaceID: seeded.Aggregate.Workspace.ID, ExpectedVersion: seeded.Aggregate.Workspace.Version,
		RequestID: "request-00000011", ExpectedHeadSHA: input.Provider.HeadSHA, FindingIDs: []string{finding.ID},
	})
	if err != nil || len(queuedReview.Publications) != 1 || queuedReview.Publications[0].State != ExecutionQueued {
		t.Fatalf("queued review = %#v, %v", queuedReview.Publications, err)
	}
	publishedReview, err := service.DispatchReviewPublication(context.Background(), successfulReviewPublisher{}, DispatchPhasePublicationRequest{
		WorkspaceID: queuedReview.Workspace.ID, PublicationID: queuedReview.Publications[0].ID,
		ExpectedVersion: queuedReview.Workspace.Version, RequestID: "request-00000012",
	})
	if err != nil || publishedReview.Publications[0].State != ExecutionSucceeded {
		t.Fatalf("published review = %#v, %v", publishedReview.Publications, err)
	}
	implementationStage := StageRun{
		ID: "psr_44444444444444444444444444444444", Stage: "implementation", State: ExecutionSucceeded,
		CharterID: charter.ID, HeadSHA: input.Provider.HeadSHA, StartedAt: now, FinishedAt: &now,
	}
	repair := RepairAttempt{
		ID: "pra_44444444444444444444444444444444", StageRunID: implementationStage.ID,
		Number: 1, State: ExecutionSucceeded, CandidateSHA: "0123456789012345678901234567890123456789",
		StartedAt: now, FinishedAt: &now,
		PublicationFence: &ImplementationPublicationFence{
			BaseCommit: input.Provider.HeadSHA, Tip: "0123456789012345678901234567890123456789",
			Tree: "0123456789012345678901234567890123456789",
		},
	}
	validation := ValidationRun{
		ID: "pvr_44444444444444444444444444444444", StageRunID: implementationStage.ID,
		State: ExecutionSucceeded, CandidateSHA: repair.CandidateSHA,
		Checks: []ValidationCheck{{ID: "tests", Name: "tests", Status: "passed"}}, StartedAt: now, FinishedAt: &now,
	}
	finding.Disposition, finding.Version, finding.UpdatedAt = FindingFixed, finding.Version+1, now
	publicationPhase := PhasePublication
	readyToPush, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: publishedReview.Workspace.ID, ExpectedVersion: publishedReview.Workspace.Version,
		RequestID: "request-00000013", Patch: AggregatePatch{
			Phase: &publicationPhase, AppendStageRuns: []StageRun{implementationStage}, UpsertFindings: []Finding{finding},
			AppendRepairs: []RepairAttempt{repair}, AppendValidations: []ValidationRun{validation},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	contextRevision, err := implementationCompletionContextRevision(readyToPush.Aggregate)
	if err != nil {
		t.Fatal(err)
	}
	completionSubject := map[string]any{"implementation_context_revision": contextRevision}
	completionDigest, err := fingerprintValue(completionSubject)
	if err != nil {
		t.Fatal(err)
	}
	completionGate, err := pinGateSubject(GateRun{
		ID:            stableID("pgr_", readyToPush.Aggregate.Workspace.ID, "completed-implementation"),
		DecisionPoint: "pr.implementation.complete", TargetID: repair.ID,
		State: ExecutionSucceeded, PolicyRevision: "sha256:policy",
		Turns:           []GateTurn{{Status: "answered", FieldValues: map[string]any{"action": "accept"}}},
		SubjectRevision: completionDigest, CreatedAt: now, FinishedAt: &now,
	}, completionSubject)
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: readyToPush.Aggregate.Workspace.ID, ExpectedVersion: readyToPush.Aggregate.Workspace.Version,
		RequestID: "request-00000013-auth", Patch: AggregatePatch{AppendGates: []GateRun{completionGate}},
	})
	if err != nil {
		t.Fatal(err)
	}
	queuedBranch, err := service.QueueBranchPublication(context.Background(), QueueBranchPublicationRequest{
		WorkspaceID: authorized.Aggregate.Workspace.ID, ExpectedVersion: authorized.Aggregate.Workspace.Version,
		RequestID: "request-00000014", ExpectedHeadSHA: input.Provider.HeadSHA,
	})
	if err != nil || len(queuedBranch.Publications) != 2 || queuedBranch.Publications[1].Kind != PublicationBranchPush {
		t.Fatalf("queued branch = %#v, %v", queuedBranch.Publications, err)
	}
	replayedBranch, err := service.QueueBranchPublication(context.Background(), QueueBranchPublicationRequest{
		WorkspaceID: authorized.Aggregate.Workspace.ID, ExpectedVersion: authorized.Aggregate.Workspace.Version,
		RequestID: "request-00000014", ExpectedHeadSHA: input.Provider.HeadSHA,
	})
	if err != nil || replayedBranch.Workspace.Version != queuedBranch.Workspace.Version || len(replayedBranch.Publications) != 2 {
		t.Fatalf("branch replay = %#v, %v", replayedBranch.Publications, err)
	}
	completed, err := service.DispatchBranchPublication(context.Background(), successfulBranchPublisher{}, DispatchPhasePublicationRequest{
		WorkspaceID: queuedBranch.Workspace.ID, PublicationID: queuedBranch.Publications[1].ID,
		ExpectedVersion: queuedBranch.Workspace.Version, RequestID: "request-00000015",
	})
	if err != nil || completed.Workspace.Phase != PhaseComplete || completed.Workspace.ExecutionState != ExecutionSucceeded ||
		completed.Publications[1].State != ExecutionSucceeded {
		t.Fatalf("completed publication = phase %q state %q pubs %#v err %v", completed.Workspace.Phase, completed.Workspace.ExecutionState, completed.Publications, err)
	}
}
