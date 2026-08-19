package prworkspace

import (
	"context"
	"errors"
	"testing"
	"time"
)

type completionWaitingGates struct{}

type completionFinalizingRepair struct{ implementationRepair }

const (
	finalizedValidationTree  = "1111111111111111111111111111111111111111"
	finalizedCandidateCommit = "2222222222222222222222222222222222222222"
)

type finalizedCandidateRepair struct{ implementationRepair }

func (repair *finalizedCandidateRepair) Repair(ctx context.Context, request RepairRequest) (RepairResult, error) {
	result, err := repair.implementationRepair.Repair(ctx, request)
	result.CandidateSHA = finalizedValidationTree
	return result, err
}

func (repair *finalizedCandidateRepair) FinalizeRepair(
	_ context.Context,
	_ string,
	result RepairResult,
) (RepairResult, error) {
	result.CandidateSHA = finalizedCandidateCommit
	result.PublicationFence = &ImplementationPublicationFence{
		BaseCommit: "abcdef",
		Tip:        finalizedCandidateCommit,
		Tree:       finalizedValidationTree,
	}
	return result, nil
}

type candidateIdentityValidation struct{}

func (candidateIdentityValidation) Validate(_ context.Context, request ValidationRequest) (ValidationRun, error) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	return ValidationRun{
		State: ExecutionSucceeded, CandidateSHA: request.CandidateSHA,
		Checks:    []ValidationCheck{{ID: "test", Name: "tests", Status: "passed"}},
		StartedAt: now, FinishedAt: &now,
	}, nil
}

type implementationAuthorizationWaitingGates struct{}

func (implementationAuthorizationWaitingGates) Start(_ context.Context, request GateRequest) (GateRun, error) {
	gate := testSucceededGate(request)
	if request.DecisionPoint == "pr.implementation.scope" || request.DecisionPoint == "pr.implementation.hard-scope" ||
		request.DecisionPoint == "pr.implementation.complete" {
		gate = testWaitingGate(request)
	}
	return gate, nil
}

func (implementationAuthorizationWaitingGates) Respond(
	_ context.Context,
	gate GateRun,
	fieldValues map[string]any,
) (GateRun, error) {
	return answerTestGate(gate, fieldValues), nil
}

func (repair *completionFinalizingRepair) FinalizeRepair(
	_ context.Context,
	_ string,
	result RepairResult,
) (RepairResult, error) {
	result.PublicationFence = &ImplementationPublicationFence{
		BaseCommit: "abcdef",
		Tip:        result.CandidateSHA,
		Tree:       result.CandidateSHA,
	}
	return result, nil
}

type countingBranchPublisher struct{ calls int }

func (publisher *countingBranchPublisher) PublishBranch(
	_ context.Context,
	request BranchPublicationRequest,
) (BranchPublicationResult, error) {
	publisher.calls++
	return BranchPublicationResult{
		ExternalID:  request.Repair.CandidateSHA,
		ExternalURL: "https://github.com/octo/repo/pull/3",
	}, nil
}

func (*countingBranchPublisher) ReconcileBranch(
	context.Context,
	BranchPublicationRequest,
) (BranchPublicationResult, bool, error) {
	return BranchPublicationResult{}, false, nil
}

type blockingBranchPublisher struct {
	started chan struct{}
	release chan struct{}
}

func (publisher *blockingBranchPublisher) PublishBranch(
	ctx context.Context,
	request BranchPublicationRequest,
) (BranchPublicationResult, error) {
	close(publisher.started)
	select {
	case <-publisher.release:
		return BranchPublicationResult{
			ExternalID:  request.Repair.CandidateSHA,
			ExternalURL: "https://github.com/octo/repo/pull/3",
		}, nil
	case <-ctx.Done():
		return BranchPublicationResult{}, ctx.Err()
	}
}

func (*blockingBranchPublisher) ReconcileBranch(
	context.Context,
	BranchPublicationRequest,
) (BranchPublicationResult, bool, error) {
	return BranchPublicationResult{}, false, nil
}

func (completionWaitingGates) Start(_ context.Context, request GateRequest) (GateRun, error) {
	gate := testSucceededGate(request)
	if request.DecisionPoint == "pr.implementation.complete" {
		gate = testWaitingGate(request)
	}
	return gate, nil
}

func (completionWaitingGates) Respond(_ context.Context, gate GateRun, fieldValues map[string]any) (GateRun, error) {
	return answerTestGate(gate, fieldValues), nil
}

func implementationWaitingOnCompletion(t *testing.T) (*Service, Aggregate, GateRun) {
	t.Helper()
	service, aggregate := readyImplementationService(t)
	provider := aggregate.ProviderSnapshot
	provider.State = "open"
	seeded, err := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-open-provider-for-completion", Patch: AggregatePatch{Provider: &provider},
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate = seeded.Aggregate
	service.gates = completionWaitingGates{}
	result, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: &completionFinalizingRepair{}, Validation: implementationValidation{},
	}, RunImplementationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-waiting-completion", FindingIDs: []string{aggregate.Findings[0].ID},
		NudgePolicy: ConfiguredNudgePolicy(0, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, gate := range result.Gates {
		if gate.DecisionPoint == "pr.implementation.complete" {
			if gate.State != ExecutionWaitingUser || gate.runtime == nil || len(gate.runtime.PinnedSubject) == 0 {
				t.Fatalf("completion gate is not durably pinned and waiting: %#v", gate)
			}
			return service, result, gate
		}
	}
	t.Fatalf("completion gate missing: %#v", result.Gates)
	return nil, Aggregate{}, GateRun{}
}

func TestCurrentImplementationCompletionGateCanAccept(t *testing.T) {
	service, waiting, gate := implementationWaitingOnCompletion(t)
	completed, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID:     waiting.Workspace.ID,
		GateRunID:       gate.ID,
		ExpectedVersion: waiting.Workspace.Version,
		RequestID:       "request-pass-current-completion",
		FieldValues:     map[string]any{"action": "accept"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Workspace.Phase != PhasePublication || completed.Findings[0].Disposition != FindingFixed {
		t.Fatalf(
			"fresh completion was not authorized: workspace=%#v findings=%#v",
			completed.Workspace,
			completed.Findings,
		)
	}
}

func TestFinalizedImplementationScopeThenCompletionGatesCanApprove(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	provider := aggregate.ProviderSnapshot
	provider.State = "open"
	seeded, err := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-open-provider-for-finalized-completion", Patch: AggregatePatch{Provider: &provider},
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate = seeded.Aggregate
	service.gates = implementationAuthorizationWaitingGates{}
	waiting, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: &finalizedCandidateRepair{}, Validation: candidateIdentityValidation{},
	}, RunImplementationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-finalized-candidate-waiting-gates", FindingIDs: []string{aggregate.Findings[0].ID},
		NudgePolicy: ConfiguredNudgePolicy(0, 0),
		SizePolicy: SizePolicy{
			XS: SizeThreshold{Files: 1, SemanticLines: 1, Modules: 1},
			S:  SizeThreshold{Files: 1, SemanticLines: 5, Modules: 1},
			M:  SizeThreshold{Files: 1, SemanticLines: 20, Modules: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	attempt := waiting.RepairAttempts[len(waiting.RepairAttempts)-1]
	validation := waiting.ValidationRuns[len(waiting.ValidationRuns)-1]
	if attempt.CandidateSHA != finalizedCandidateCommit || attempt.PublicationFence == nil ||
		attempt.PublicationFence.Tree != finalizedValidationTree || validation.CandidateSHA != finalizedValidationTree {
		t.Fatalf("finalized candidate identities = repair %#v validation %#v", attempt, validation)
	}
	var scopeGate, completionGate GateRun
	for _, gate := range waiting.Gates {
		switch gate.DecisionPoint {
		case "pr.implementation.scope":
			scopeGate = gate
		case "pr.implementation.complete":
			completionGate = gate
		}
	}
	if scopeGate.ID == "" || completionGate.ID == "" || scopeGate.State != ExecutionWaitingUser ||
		completionGate.State != ExecutionWaitingUser {
		t.Fatalf("implementation authorization gates = %#v", waiting.Gates)
	}
	scopeApproved, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID:     waiting.Workspace.ID,
		GateRunID:       scopeGate.ID,
		ExpectedVersion: waiting.Workspace.Version,
		RequestID:       "request-pass-finalized-scope",
		FieldValues:     map[string]any{"action": "approve"},
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID:     scopeApproved.Workspace.ID,
		GateRunID:       completionGate.ID,
		ExpectedVersion: scopeApproved.Workspace.Version,
		RequestID:       "request-pass-finalized-completion",
		FieldValues:     map[string]any{"action": "accept"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Workspace.Phase != PhasePublication || completed.Findings[0].Disposition != FindingFixed {
		t.Fatalf(
			"finalized completion was not authorized: workspace=%#v findings=%#v",
			completed.Workspace,
			completed.Findings,
		)
	}
}

func TestImplementationCompletionGateRejectsLaterGuidanceAndCorrection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Service, Aggregate) Aggregate
	}{
		{
			name: "implementation guidance",
			mutate: func(t *testing.T, service *Service, aggregate Aggregate) Aggregate {
				result, err := service.AddMessage(context.Background(), AddMessageRequest{
					WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
					RequestID: "request-later-implementation-guidance", Stage: "implementation",
					Content: "Preserve callback ordering while repairing retries.",
				})
				if err != nil {
					t.Fatal(err)
				}
				return result
			},
		},
		{
			name: "implementation correction",
			mutate: func(t *testing.T, service *Service, aggregate Aggregate) Aggregate {
				result, err := service.AddCorrection(context.Background(), AddCorrectionRequest{
					WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
					RequestID: "request-later-implementation-correction",
					Correction: Correction{
						Kind:          CorrectionImplementation,
						Applicability: CorrectionImplementationOnly,
						TargetType:    "workspace",
						TargetID:      aggregate.Workspace.ID,
						OriginalClaim: "Callbacks may be reordered",
						Correction:    "Callback order is part of the contract",
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				return result
			},
		},
	}
	for testIndex, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, waiting, gate := implementationWaitingOnCompletion(t)
			changed := test.mutate(t, service, waiting)
			staled, err := service.RespondGate(context.Background(), RespondGateRequest{
				WorkspaceID:     changed.Workspace.ID,
				GateRunID:       gate.ID,
				ExpectedVersion: changed.Workspace.Version,
				RequestID:       "request-pass-stale-completion-" + string(rune('a'+testIndex)),
				FieldValues:     map[string]any{"action": "accept"},
			})
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("stale completion response error = %v", err)
			}
			assertStaleCompletionNeutralized(t, staled, gate.ID)
		})
	}
}

func TestImplementationCompletionGateRejectsLaterCompletionAuditEvidence(t *testing.T) {
	service, waiting, gate := implementationWaitingOnCompletion(t)
	service.candidateEvidence = fixedCandidateEvidenceLoader{value: CandidateEvidence{
		CandidateSHA:  waiting.RepairAttempts[len(waiting.RepairAttempts)-1].CandidateSHA,
		CandidateDiff: testCandidateDiff,
		Metrics: CandidateMetrics{
			Files: 1, SemanticLines: 10, Modules: 1, ChangedFiles: []string{"pkg/retry.go"},
		},
		EvidenceDigest: "sha256:later-completion-audit",
	}}
	service.ai.Runner = invariantImplementationAI{completion: map[string]any{
		"summary": "one requirement remains", "complete": false,
		"missing_in_scope": []any{completionFindingJSON("follow_up", "S0_exact", "XS", true)},
		"out_of_scope":     []any{}, "coverage": coverageJSON(),
	}}
	audited, err := service.RunCompletionAudit(context.Background(), RunCompletionAuditRequest{
		WorkspaceID: waiting.Workspace.ID, ExpectedVersion: waiting.Workspace.Version,
		RequestID: "request-later-completion-audit", NudgePolicy: ConfiguredNudgePolicy(0, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	staled, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID:     audited.Workspace.ID,
		GateRunID:       gate.ID,
		ExpectedVersion: audited.Workspace.Version,
		RequestID:       "request-pass-after-later-audit",
		FieldValues:     map[string]any{"action": "accept"},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale completion response error = %v", err)
	}
	assertStaleCompletionNeutralized(t, staled, gate.ID)
	foundMissing := false
	for _, finding := range staled.Findings {
		if finding.OriginRunID == audited.StageRuns[len(audited.StageRuns)-1].ID &&
			finding.Disposition == FindingInScope {
			foundMissing = true
		}
	}
	if !foundMissing {
		t.Fatalf("later completion finding was lost or marked fixed: %#v", staled.Findings)
	}
}

func TestImplementationCompletionGateRejectsSupersededRepair(t *testing.T) {
	service, waiting, gate := implementationWaitingOnCompletion(t)
	now := waiting.Workspace.UpdatedAt
	priorStage := waiting.StageRuns[len(waiting.StageRuns)-1]
	newStage := priorStage
	newStage.ID, newStage.Attempt, newStage.State = stableID(
		"psr_",
		waiting.Workspace.ID,
		"newer-repair",
	), priorStage.Attempt+1, ExecutionBlocked
	newStage.Evidence, newStage.PublicError, newStage.FinishedAt = nil, "completion_incomplete", &now
	newAttempt := waiting.RepairAttempts[len(waiting.RepairAttempts)-1]
	newAttempt.ID, newAttempt.StageRunID, newAttempt.Number = stableID(
		"pra_",
		waiting.Workspace.ID,
		"newer-repair",
	), newStage.ID, newAttempt.Number+1
	newAttempt.CandidateSHA = "newer-candidate"
	newValidation := waiting.ValidationRuns[len(waiting.ValidationRuns)-1]
	newValidation.ID, newValidation.StageRunID, newValidation.CandidateSHA = stableID(
		"pvr_",
		waiting.Workspace.ID,
		"newer-repair",
	), newStage.ID, newAttempt.CandidateSHA
	seeded, err := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID: waiting.Workspace.ID, ExpectedVersion: waiting.Workspace.Version,
		RequestID: "request-seed-newer-repair", Patch: AggregatePatch{
			AppendStageRuns: []StageRun{
				newStage,
			},
			AppendRepairs:     []RepairAttempt{newAttempt},
			AppendValidations: []ValidationRun{newValidation},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	staled, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID:     seeded.Aggregate.Workspace.ID,
		GateRunID:       gate.ID,
		ExpectedVersion: seeded.Aggregate.Workspace.Version,
		RequestID:       "request-pass-superseded-repair",
		FieldValues:     map[string]any{"action": "accept"},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("superseded completion response error = %v", err)
	}
	staleGate, _ := findGate(staled.Gates, gate.ID)
	if staleGate.State != ExecutionStale || staled.Workspace.Phase == PhasePublication {
		t.Fatalf("superseded completion entered publication: workspace=%#v gate=%#v", staled.Workspace, staleGate)
	}
}

func TestBranchPublicationRejectsGuidanceAddedAfterCompletionApproval(t *testing.T) {
	service, waiting, gate := implementationWaitingOnCompletion(t)
	completed, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID:     waiting.Workspace.ID,
		GateRunID:       gate.ID,
		ExpectedVersion: waiting.Workspace.Version,
		RequestID:       "request-approve-before-guidance",
		FieldValues:     map[string]any{"action": "accept"},
	})
	if err != nil {
		t.Fatal(err)
	}
	guided, err := service.AddMessage(context.Background(), AddMessageRequest{
		WorkspaceID: completed.Workspace.ID, ExpectedVersion: completed.Workspace.Version,
		RequestID: "request-guidance-before-publication", Stage: "implementation",
		Content: "The repaired retry must preserve cancellation ordering.",
	})
	if err != nil {
		t.Fatal(err)
	}
	staled, err := service.QueueBranchPublication(context.Background(), QueueBranchPublicationRequest{
		WorkspaceID: guided.Workspace.ID, ExpectedVersion: guided.Workspace.Version,
		RequestID: "request-queue-with-stale-completion", ExpectedHeadSHA: guided.ProviderSnapshot.HeadSHA,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale branch queue error = %v", err)
	}
	assertStaleCompletionNeutralized(t, staled, gate.ID)
	if len(staled.Publications) != 0 {
		t.Fatalf("stale completion queued branch publication: %#v", staled.Publications)
	}
}

func TestBranchDispatchRechecksCompletionAuthorization(t *testing.T) {
	service, waiting, gate := implementationWaitingOnCompletion(t)
	completed, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID:     waiting.Workspace.ID,
		GateRunID:       gate.ID,
		ExpectedVersion: waiting.Workspace.Version,
		RequestID:       "request-approve-before-queue",
		FieldValues:     map[string]any{"action": "accept"},
	})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := service.QueueBranchPublication(context.Background(), QueueBranchPublicationRequest{
		WorkspaceID: completed.Workspace.ID, ExpectedVersion: completed.Workspace.Version,
		RequestID: "request-queue-fresh-completion", ExpectedHeadSHA: completed.ProviderSnapshot.HeadSHA,
	})
	if err != nil {
		t.Fatal(err)
	}
	publication := queued.Publications[len(queued.Publications)-1]
	guided, err := service.AddMessage(context.Background(), AddMessageRequest{
		WorkspaceID: queued.Workspace.ID, ExpectedVersion: queued.Workspace.Version,
		RequestID: "request-guidance-before-dispatch", Stage: "implementation",
		Content: "Cancellation ordering is still required before publishing.",
	})
	if err != nil {
		t.Fatal(err)
	}
	publisher := &countingBranchPublisher{}
	staled, err := service.DispatchBranchPublication(context.Background(), publisher, DispatchPhasePublicationRequest{
		WorkspaceID: guided.Workspace.ID, PublicationID: publication.ID,
		ExpectedVersion: guided.Workspace.Version, RequestID: "request-dispatch-stale-completion",
	})
	if !errors.Is(err, ErrConflict) || publisher.calls != 0 {
		t.Fatalf("stale branch dispatch error=%v publisher calls=%d", err, publisher.calls)
	}
	assertStaleCompletionNeutralized(t, staled, gate.ID)
	stalePublication, found := findPublication(staled.Publications, publication.ID)
	if !found || stalePublication.State != ExecutionStale ||
		stalePublication.PublicErrorCode != "completion_authorization_stale" {
		t.Fatalf("stale queued publication = %#v", staled.Publications)
	}
}

func TestRunningBranchPublicationFencesCompletionContextMutations(t *testing.T) {
	service, waiting, gate := implementationWaitingOnCompletion(t)
	completed, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID:     waiting.Workspace.ID,
		GateRunID:       gate.ID,
		ExpectedVersion: waiting.Workspace.Version,
		RequestID:       "request-approve-before-concurrent-push",
		FieldValues:     map[string]any{"action": "accept"},
	})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := service.QueueBranchPublication(context.Background(), QueueBranchPublicationRequest{
		WorkspaceID: completed.Workspace.ID, ExpectedVersion: completed.Workspace.Version,
		RequestID: "request-queue-concurrent-push", ExpectedHeadSHA: completed.ProviderSnapshot.HeadSHA,
	})
	if err != nil {
		t.Fatal(err)
	}
	publication := queued.Publications[len(queued.Publications)-1]
	publisher := &blockingBranchPublisher{started: make(chan struct{}), release: make(chan struct{})}
	type dispatchResult struct {
		aggregate Aggregate
		err       error
	}
	dispatched := make(chan dispatchResult, 1)
	go func() {
		aggregate, dispatchErr := service.DispatchBranchPublication(
			context.Background(),
			publisher,
			DispatchPhasePublicationRequest{
				WorkspaceID: queued.Workspace.ID, PublicationID: publication.ID,
				ExpectedVersion: queued.Workspace.Version, RequestID: "request-dispatch-concurrent-push",
			},
		)
		dispatched <- dispatchResult{aggregate: aggregate, err: dispatchErr}
	}()

	select {
	case <-publisher.started:
	case <-time.After(2 * time.Second):
		t.Fatal("branch publisher was not called")
	}
	claimed, err := service.Get(context.Background(), queued.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	running, found := findPublication(claimed.Publications, publication.ID)
	if !found || running.State != ExecutionRunning {
		t.Fatalf("branch publication was not durably claimed: %#v", claimed.Publications)
	}
	rejected, err := service.AddMessage(context.Background(), AddMessageRequest{
		WorkspaceID: claimed.Workspace.ID, ExpectedVersion: claimed.Workspace.Version,
		RequestID: "request-guidance-during-concurrent-push", Stage: "implementation",
		Content: "This guidance must invalidate completion before any push.",
	})
	if !errors.Is(err, ErrConflict) || rejected.Workspace.Version != claimed.Workspace.Version ||
		len(rejected.Messages) != len(claimed.Messages) {
		t.Fatalf("in-flight context mutation was not fenced: aggregate=%#v err=%v", rejected, err)
	}

	close(publisher.release)
	result := <-dispatched
	if result.err != nil || result.aggregate.Workspace.Phase != PhaseComplete ||
		result.aggregate.Workspace.ExecutionState != ExecutionSucceeded {
		t.Fatalf(
			"branch publication did not finish after fenced mutation: workspace=%#v err=%v",
			result.aggregate.Workspace,
			result.err,
		)
	}
	if len(result.aggregate.Messages) != len(claimed.Messages) {
		t.Fatalf("rejected guidance entered the completed aggregate: %#v", result.aggregate.Messages)
	}
}

func assertStaleCompletionNeutralized(t *testing.T, aggregate Aggregate, gateID string) {
	t.Helper()
	gate, found := findGate(aggregate.Gates, gateID)
	if !found || gate.State != ExecutionStale {
		t.Fatalf("completion gate was not marked stale: %#v", aggregate.Gates)
	}
	if aggregate.Workspace.Phase != PhaseImplementation || aggregate.Workspace.ExecutionState != ExecutionBlocked {
		t.Fatalf("stale completion advanced lifecycle: %#v", aggregate.Workspace)
	}
	attempt := aggregate.RepairAttempts[0]
	stage, _ := findStageRun(aggregate.StageRuns, attempt.StageRunID)
	if stage.Stage != "implementation" || stage.State != ExecutionBlocked ||
		stage.PublicError != "completion_authorization_stale" {
		t.Fatalf("stale implementation stage = %#v", stage)
	}
	if gateAction(gate) != "accept" && aggregate.Findings[0].Disposition == FindingFixed {
		t.Fatalf("stale completion marked finding fixed: %#v", aggregate.Findings[0])
	}
}

func TestRunImplementationRequiresReviewAndCompletedTriage(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(Aggregate) AggregatePatch
	}{
		{
			name: "review phase",
			mutate: func(Aggregate) AggregatePatch {
				phase := PhaseReview
				return AggregatePatch{Phase: &phase}
			},
		},
		{
			name: "review not successful",
			mutate: func(aggregate Aggregate) AggregatePatch {
				stage := aggregate.StageRuns[0]
				stage.State = ExecutionFailed
				return AggregatePatch{ReplaceStageRuns: []StageRun{stage}}
			},
		},
		{
			name: "unclassified finding",
			mutate: func(aggregate Aggregate) AggregatePatch {
				finding := aggregate.Findings[0]
				finding.Disposition = FindingOpen
				return AggregatePatch{UpsertFindings: []Finding{finding}}
			},
		},
	}
	for testIndex, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, aggregate := readyImplementationService(t)
			seeded, err := service.store.Mutate(context.Background(), Mutation{
				WorkspaceID:     aggregate.Workspace.ID,
				ExpectedVersion: aggregate.Workspace.Version,
				RequestID: "request-not-ready-implementation-" + string(
					rune('a'+testIndex),
				),
				Patch: test.mutate(aggregate),
			})
			if err != nil {
				t.Fatal(err)
			}
			repair := &implementationRepair{}
			_, err = service.RunImplementation(context.Background(), ImplementationConfig{
				Repair: repair, Validation: implementationValidation{},
			}, RunImplementationRequest{
				WorkspaceID: seeded.Aggregate.Workspace.ID, ExpectedVersion: seeded.Aggregate.Workspace.Version,
				RequestID: "request-run-not-ready-" + string(rune('a'+testIndex)),
			})
			if !errors.Is(err, ErrConflict) || repair.calls != 0 {
				t.Fatalf("implementation readiness error=%v repair calls=%d", err, repair.calls)
			}
		})
	}
}

func TestRunImplementationAllowsFailedAndBlockedRetries(t *testing.T) {
	t.Run("failed retry", func(t *testing.T) {
		service, aggregate := readyImplementationService(t)
		failed, err := service.RunImplementation(context.Background(), ImplementationConfig{
			Repair: failedImplementationRepair{}, Validation: implementationValidation{},
		}, RunImplementationRequest{
			WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
			RequestID: "request-create-failed-implementation", FindingIDs: []string{aggregate.Findings[0].ID},
		})
		if err != nil || failed.Workspace.ExecutionState != ExecutionFailed {
			t.Fatalf("failed implementation = %#v err=%v", failed.Workspace, err)
		}
		repair := &implementationRepair{}
		_, err = service.RunImplementation(context.Background(), ImplementationConfig{
			Repair: repair, Validation: implementationValidation{},
		}, RunImplementationRequest{
			WorkspaceID: failed.Workspace.ID, ExpectedVersion: failed.Workspace.Version,
			RequestID: "request-retry-failed-implementation", FindingIDs: []string{failed.Findings[0].ID},
			NudgePolicy: ConfiguredNudgePolicy(0, 0),
		})
		if err != nil || repair.calls != 1 {
			t.Fatalf("failed retry error=%v repair calls=%d", err, repair.calls)
		}
	})

	t.Run("blocked retry", func(t *testing.T) {
		service, aggregate := readyImplementationService(t)
		charter, _ := aggregate.ActiveCharter()
		now := aggregate.Workspace.UpdatedAt
		phase, state := PhaseImplementation, ExecutionBlocked
		prior := StageRun{
			ID:          stableID("psr_", aggregate.Workspace.ID, "blocked-retry"),
			Stage:       "implementation",
			State:       ExecutionBlocked,
			PublicError: "completion_incomplete",
			CharterID:   charter.ID,
			HeadSHA:     charter.HeadSHA,
			Attempt:     1,
			StartedAt:   now,
			FinishedAt:  &now,
		}
		seeded, err := service.store.Mutate(context.Background(), Mutation{
			WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
			RequestID: "request-seed-blocked-implementation", Patch: AggregatePatch{
				Phase: &phase, ExecutionState: &state, AppendStageRuns: []StageRun{prior},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		repair := &implementationRepair{}
		_, err = service.RunImplementation(context.Background(), ImplementationConfig{
			Repair: repair, Validation: implementationValidation{},
		}, RunImplementationRequest{
			WorkspaceID: seeded.Aggregate.Workspace.ID, ExpectedVersion: seeded.Aggregate.Workspace.Version,
			RequestID: "request-retry-blocked-implementation", FindingIDs: []string{seeded.Aggregate.Findings[0].ID},
			NudgePolicy: ConfiguredNudgePolicy(0, 0),
		})
		if err != nil || repair.calls != 1 {
			t.Fatalf("blocked retry error=%v repair calls=%d", err, repair.calls)
		}
	})
}
