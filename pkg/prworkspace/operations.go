package prworkspace

import (
	"context"
	"errors"
	"fmt"
)

type UpdateFindingRequest struct {
	WorkspaceID     string
	FindingID       string
	ExpectedVersion int64
	RequestID       string
	Severity        string
	Title           string
	Message         string
	Evidence        string
	Scope           ScopeAssessment
}

func (service *Service) UpdateFinding(ctx context.Context, request UpdateFindingRequest) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		!validOpaqueID(request.FindingID, "pfn_") || !validBoundedText(request.Severity, 32, false) ||
		!validBoundedText(request.Title, 1024, false) || !validBoundedText(request.Message, maxAITextBytes, false) ||
		!validBoundedText(request.Evidence, maxAITextBytes, true) {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	finding, index := findFinding(aggregate.Findings, request.FindingID)
	if index < 0 || aggregate.Workspace.Version != request.ExpectedVersion ||
		finding.Disposition == FindingFixed {
		return aggregate, ErrConflict
	}
	if hardScopeFindingPinned(aggregate.Gates, finding.ID) {
		previousScope, previousErr := fingerprintValue(finding.Scope)
		requestedScope, requestedErr := fingerprintValue(request.Scope)
		if previousErr != nil || requestedErr != nil || previousScope != requestedScope {
			return aggregate, fmt.Errorf("%w: hard candidate scope is frozen; resolve its gate or revise the charter", ErrInvalid)
		}
	}
	previous := finding
	finding.Severity, finding.Title, finding.Message, finding.Evidence = request.Severity, request.Title, request.Message, request.Evidence
	finding.Scope, finding.Version, finding.UpdatedAt = request.Scope, finding.Version+1, service.now().UTC()
	finding = setFindingNudgeReward(finding, NudgeReward(RewardRetainedOpen), "user_edited_finding")
	original, _ := fingerprintValue(previous)
	corrected, _ := fingerprintValue(finding)
	correction := Correction{
		ID:   stableID("pco_", aggregate.Workspace.ID, request.RequestID),
		Kind: CorrectionFindingQuality, Applicability: CorrectionReviewAndImpl,
		TargetType: "finding", TargetID: finding.ID, OriginalClaim: original,
		Correction: corrected, Evidence: "User edited finding content or classification.",
		CharterID: aggregate.Workspace.ActiveCharterID, HeadSHA: aggregate.ProviderSnapshot.HeadSHA,
		CreatedAt: service.now().UTC(),
	}
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID,
		Patch: AggregatePatch{
			UpsertFindings: []Finding{finding}, AppendCorrections: []Correction{correction},
			ReplaceNudgeRounds: recomputeNudgeRoundRewards(
				aggregate.NudgeRounds,
				upsertByID(aggregate.Findings, []Finding{finding}, func(value Finding) string { return value.ID }),
			),
			Activity: []Activity{{Kind: "finding.edited", Actor: "user", EntityID: finding.ID, Summary: "Finding corrected", CreatedAt: service.now().UTC()}},
		},
	})
	if err != nil {
		return result.Aggregate, err
	}
	return result.Aggregate, nil
}

func hardScopeFindingPinned(gates []GateRun, findingID string) bool {
	for _, gate := range gates {
		if gate.DecisionPoint != "pr.implementation.scope" || !gate.Evidence.HardScope {
			continue
		}
		for _, id := range gate.Evidence.HardScopeFindingIDs {
			if id == findingID {
				return true
			}
		}
	}
	return false
}

type CancelStageRequest struct {
	WorkspaceID     string
	StageRunID      string
	ExpectedVersion int64
	RequestID       string
}

func (service *Service) CancelStage(ctx context.Context, request CancelStageRequest) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		!validOpaqueID(request.StageRunID, "psr_") {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	var run StageRun
	found := false
	for _, candidate := range aggregate.StageRuns {
		if candidate.ID == request.StageRunID {
			run, found = candidate, true
			break
		}
	}
	if !found || aggregate.Workspace.Version != request.ExpectedVersion ||
		(run.State != ExecutionQueued && run.State != ExecutionRunning && run.State != ExecutionWaitingGate && run.State != ExecutionWaitingUser) {
		return aggregate, ErrConflict
	}
	now := service.now().UTC()
	run.State, run.PublicError, run.FinishedAt = ExecutionCanceled, "canceled_by_user", &now
	state := ExecutionCanceled
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID,
		Patch:     AggregatePatch{ExecutionState: &state, ReplaceStageRuns: []StageRun{run}, Activity: []Activity{{Kind: "stage.canceled", Actor: "user", EntityID: run.ID, Summary: "Stage run canceled", CreatedAt: now}}},
	})
	if err != nil {
		return result.Aggregate, err
	}
	return result.Aggregate, nil
}

type RunCompletionAuditRequest struct {
	WorkspaceID     string
	ExpectedVersion int64
	RequestID       string
	NudgePolicy     NudgePolicy
}

func (service *Service) RunCompletionAudit(ctx context.Context, request RunCompletionAuditRequest) (Aggregate, error) {
	return service.runCompletionAudit(ctx, request, false)
}

func (service *Service) runCompletionAudit(ctx context.Context, request RunCompletionAuditRequest, manualNudge bool) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) {
		return Aggregate{}, ErrInvalid
	}
	if request.NudgePolicy == (NudgePolicy{}) {
		request.NudgePolicy = DefaultNudgePolicy()
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	charter, ok := aggregate.ActiveCharter()
	if !ok || !charter.Confirmed || aggregate.Workspace.Version != request.ExpectedVersion ||
		charter.HeadSHA != aggregate.ProviderSnapshot.HeadSHA ||
		!phaseAllowsCompletionAudit(aggregate.Workspace.Phase) {
		return aggregate, ErrConflict
	}
	latestRepair, latestValidation, ok := latestValidatedCandidate(aggregate)
	if !ok {
		return aggregate, ErrConflict
	}
	implementationStage, stageOK := findStageRun(aggregate.StageRuns, latestRepair.StageRunID)
	if !stageOK || implementationStage.Stage != "implementation" ||
		implementationStage.CharterID != charter.ID || implementationStage.HeadSHA != charter.HeadSHA {
		return aggregate, ErrConflict
	}
	if service.candidateEvidence == nil {
		return aggregate, errors.New("completion audit candidate evidence is unavailable")
	}
	candidateEvidence, evidenceErr := service.candidateEvidence.LoadCandidateEvidence(ctx, latestRepair)
	if evidenceErr != nil {
		return aggregate, evidenceErr
	}
	if candidateEvidence.CandidateSHA != latestRepair.CandidateSHA ||
		candidateEvidence.EvidenceDigest == "" || candidateEvidence.CandidateDiff == "" ||
		candidateEvidence.Metrics.Files != len(candidateEvidence.Metrics.ChangedFiles) ||
		candidateEvidence.Metrics.Files < 0 || candidateEvidence.Metrics.SemanticLines < 0 || candidateEvidence.Metrics.Modules < 0 {
		return aggregate, ErrConflict
	}
	bundle := implementationContextBundle(aggregate)
	bundle.CandidateDiff = candidateEvidence.CandidateDiff
	bundle.CandidateMetrics = candidateEvidence.Metrics
	bundle.Validation = map[string]any{"run": latestValidation}
	stats := NudgeStrategyStats(aggregate.NudgeRounds, NudgeImplementationDone)
	var rounds []CompletionRound
	var runErr error
	if manualNudge {
		var round CompletionRound
		round, runErr = service.ai.RunCompletionNudge(ctx, bundle, stats)
		rounds = []CompletionRound{round}
	} else {
		rounds, runErr = service.ai.RunCompletionAudit(ctx, bundle, request.NudgePolicy, stats)
	}
	if runErr != nil && len(rounds) == 0 {
		return Aggregate{}, runErr
	}
	if !completionRoundsMatchCandidateScope(rounds, latestRepair.Scope) {
		return aggregate, errors.New("completion candidate evidence does not match the exact persisted scope audit")
	}
	now := service.now().UTC()
	runID := stableID("psr_", aggregate.Workspace.ID, request.RequestID)
	missing, deferred, candidateDrift, nudges := materializeCompletionRounds(aggregate, runID, rounds, request.NudgePolicy, now)
	state := ExecutionWaitingUser
	phase := aggregate.Workspace.Phase
	stageState, publicError := ExecutionSucceeded, ""
	if runErr != nil {
		state, stageState, publicError = ExecutionFailed, ExecutionFailed, "completion_nudge_failed"
	} else if len(missing) > 0 {
		phase, state = PhaseImplementation, ExecutionQueued
	} else if len(candidateDrift) > 0 {
		phase, state = PhaseCompletionAudit, ExecutionWaitingGate
	}
	stage := StageRun{ID: runID, Stage: "completion_audit", State: stageState, PublicError: publicError, CharterID: charter.ID, HeadSHA: charter.HeadSHA, Attempt: countStageRuns(aggregate.StageRuns, "completion_audit") + 1, PromptDigest: rounds[0].PromptDigest, Summary: rounds[len(rounds)-1].Result.Summary, StartedAt: now, FinishedAt: &now}
	stage.Evidence = completionStageEvidence(
		runID, "completion_audit", stage.Summary, stage.PromptDigest, rounds,
		append(append(append([]Finding(nil), missing...), deferred...), candidateDrift...),
		map[string]any{"run": latestValidation}, now,
	)
	patch := AggregatePatch{
		Phase: &phase, ExecutionState: &state, AppendStageRuns: []StageRun{stage},
		UpsertFindings: append(append(missing, deferred...), candidateDrift...), AppendNudgeRounds: nudges,
		Activity: []Activity{{Kind: "completion.audit_finished", Actor: "ai", EntityID: runID, Summary: fmt.Sprintf("Completion audit finished with %d rounds", len(rounds)), CreatedAt: now}},
	}
	if runErr == nil && len(candidateDrift) > 0 {
		scopeSubject := map[string]any{
			"charter": charter, "repair": latestRepair, "candidate_drift": candidateDrift,
		}
		scopeGate, gateErr := service.startImplementationScopeGate(ctx, aggregate, scopeSubject, latestRepair.Scope, candidateDrift)
		if gateErr != nil {
			return aggregate, gateErr
		}
		scopeGate.TargetID = latestRepair.ID
		if _, existing := findGate(aggregate.Gates, scopeGate.ID); !existing {
			patch.AppendGates = append(patch.AppendGates, scopeGate)
		}
		if scopeGate.State == ExecutionSucceeded && scopeGate.Outcome == GatePass &&
			!hasHardCandidateScope(latestRepair.Scope, candidateDrift) {
			phase, state = aggregate.Workspace.Phase, ExecutionWaitingUser
			patch.Phase, patch.ExecutionState = &phase, &state
		}
	}
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID,
		Patch:     patch,
	})
	if err != nil {
		return result.Aggregate, err
	}
	if runErr != nil {
		return result.Aggregate, runErr
	}
	return result.Aggregate, nil
}

type RunNudgeRequest struct {
	WorkspaceID     string
	ExpectedVersion int64
	RequestID       string
	Stage           NudgeStage
}

func (service *Service) RunNudge(ctx context.Context, request RunNudgeRequest) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	charter, ok := aggregate.ActiveCharter()
	if !ok || !charter.Confirmed || aggregate.Workspace.Version != request.ExpectedVersion ||
		charter.HeadSHA != aggregate.ProviderSnapshot.HeadSHA {
		return aggregate, ErrConflict
	}
	if request.Stage == NudgeReviewSearch {
		if aggregate.Workspace.Phase != PhaseTriage || !hasSuccessfulStageAtHead(aggregate.StageRuns, "review", charter.ID, charter.HeadSHA) {
			return aggregate, ErrConflict
		}
		return service.runReview(ctx, RunReviewRequest{WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion, RequestID: request.RequestID, NudgePolicy: NudgePolicy{MinimumAdditionalRounds: 1, MaximumAdditionalRounds: 1}}, true)
	}
	if request.Stage == NudgeImplementationDone {
		if !phaseAllowsCompletionAudit(aggregate.Workspace.Phase) ||
			!hasSuccessfulStageAtHead(aggregate.StageRuns, "completion_audit", charter.ID, charter.HeadSHA) {
			return aggregate, ErrConflict
		}
		return service.runCompletionAudit(ctx, RunCompletionAuditRequest{WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion, RequestID: request.RequestID, NudgePolicy: NudgePolicy{MinimumAdditionalRounds: 1, MaximumAdditionalRounds: 1}}, true)
	}
	return Aggregate{}, ErrInvalid
}

func phaseAllowsCompletionAudit(phase Phase) bool {
	switch phase {
	case PhaseImplementation, PhaseValidation, PhaseCompletionAudit, PhasePublication:
		return true
	default:
		return false
	}
}

func latestValidatedCandidate(aggregate Aggregate) (RepairAttempt, ValidationRun, bool) {
	if len(aggregate.RepairAttempts) == 0 || len(aggregate.ValidationRuns) == 0 {
		return RepairAttempt{}, ValidationRun{}, false
	}
	repair := aggregate.RepairAttempts[len(aggregate.RepairAttempts)-1]
	validation := aggregate.ValidationRuns[len(aggregate.ValidationRuns)-1]
	if validation.StageRunID != repair.StageRunID || !validationGreen(validation) {
		return RepairAttempt{}, ValidationRun{}, false
	}
	return repair, validation, true
}

func latestUnresolvedGate(values []GateRun) (GateRun, error) {
	for index := len(values) - 1; index >= 0; index-- {
		if values[index].State == ExecutionWaitingGate || values[index].State == ExecutionWaitingUser {
			return values[index], nil
		}
	}
	return GateRun{}, errors.New("no unresolved gate")
}
