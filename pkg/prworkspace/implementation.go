package prworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type RepairRequest struct {
	Context              PRContextBundle
	AuthorizedFindingIDs []string
	Instruction          string
	Attempt              int
}

type RepairResult struct {
	Summary          string
	WorkspaceID      string
	ChangedFiles     []string
	SemanticLines    int
	Modules          int
	CandidateSHA     string
	CandidateDiff    string
	PromptDigest     string
	PublicationFence *ImplementationPublicationFence
}

type RepairExecutor interface {
	Repair(ctx context.Context, request RepairRequest) (RepairResult, error)
}

// RepairFinalizer turns one validated, completion-audited candidate into a
// retained local commit. It runs before the completion/publication gate, so a
// human may safely delay or reject publication without losing exact evidence.
type RepairFinalizer interface {
	FinalizeRepair(ctx context.Context, workspaceID string, result RepairResult) (RepairResult, error)
}

// RepairFinalizationAcknowledger releases runtime recovery state only after
// the aggregate mutation containing the publication fence has committed.
type RepairFinalizationAcknowledger interface {
	AcknowledgeFinalizedRepair(ctx context.Context, workspaceID string, result RepairResult) error
}

type ValidationRequest struct {
	ID            string
	WorkspaceID   string
	Charter       Charter
	Provider      ProviderSnapshot
	CandidateSHA  string
	ChangedFiles  []string
	CandidateDiff string
}

type ValidationExecutor interface {
	Validate(ctx context.Context, request ValidationRequest) (ValidationRun, error)
}

type ImplementationConfig struct {
	Repair     RepairExecutor
	Validation ValidationExecutor
	MaxCycles  int
}

type RunImplementationRequest struct {
	WorkspaceID     string
	ExpectedVersion int64
	RequestID       string
	FindingIDs      []string
	NudgePolicy     NudgePolicy
	SizePolicy      SizePolicy
	MaxCycles       int
}

func (service *Service) RunImplementation(
	ctx context.Context,
	config ImplementationConfig,
	request RunImplementationRequest,
) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		config.Repair == nil || config.Validation == nil {
		return Aggregate{}, ErrInvalid
	}
	maxCycles := request.MaxCycles
	if maxCycles == 0 {
		maxCycles = config.MaxCycles
	}
	if maxCycles == 0 {
		maxCycles = 3
	}
	if maxCycles < 1 || maxCycles > 10 {
		return Aggregate{}, ErrInvalid
	}
	if request.NudgePolicy == (NudgePolicy{}) {
		request.NudgePolicy = DefaultNudgePolicy()
	}
	if request.SizePolicy == (SizePolicy{}) {
		request.SizePolicy = DefaultSizePolicy()
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	charter, ready := aggregate.ActiveCharter()
	if !ready || !charter.Confirmed || aggregate.Workspace.Version != request.ExpectedVersion ||
		charter.HeadSHA != aggregate.ProviderSnapshot.HeadSHA ||
		!implementationStartReady(aggregate, charter) {
		return aggregate, ErrConflict
	}
	selected, err := selectImplementationFindings(aggregate.Findings, aggregate.Gates, request.FindingIDs)
	if err != nil {
		return aggregate, err
	}
	if !aggregate.ProviderSnapshot.HeadWritable {
		return aggregate, errors.New("PR head is not writable")
	}
	if !service.claimImplementation(request.WorkspaceID) {
		return aggregate, ErrConflict
	}
	defer service.releaseImplementation(request.WorkspaceID)
	var authorizationGates []GateRun
	if !aggregate.ProviderSnapshot.Owned {
		eligibility, eligibilityNew, gateErr := service.ensureGate(
			ctx,
			aggregate,
			"pr.implementation.eligibility",
			map[string]any{
				"owned": false, "head_writable": true, "provider": aggregate.ProviderSnapshot,
			},
		)
		if gateErr != nil {
			return Aggregate{}, gateErr
		}
		eligibility.TargetID = charter.ID
		if eligibilityNew {
			authorizationGates = append(authorizationGates, eligibility)
		}
		if !gateCompletedWith(eligibility, "authorize") {
			state := ExecutionWaitingGate
			result, mutateErr := service.store.Mutate(ctx, Mutation{
				WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
				RequestID: request.RequestID,
				Patch:     AggregatePatch{ExecutionState: &state, AppendGates: authorizationGates},
			})
			if mutateErr != nil {
				return result.Aggregate, mutateErr
			}
			return result.Aggregate, nil
		}
	}
	startGate, startGateNew, err := service.ensureGate(ctx, aggregate, "pr.implementation.start", map[string]any{
		"charter": charter, "findings": selected, "owned": aggregate.ProviderSnapshot.Owned,
	})
	if err != nil {
		return Aggregate{}, err
	}
	startGate.TargetID = charter.ID
	if startGateNew {
		authorizationGates = append(authorizationGates, startGate)
	}
	if !gateCompletedWith(startGate, "continue") {
		state := ExecutionWaitingGate
		result, mutateErr := service.store.Mutate(ctx, Mutation{
			WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
			RequestID: request.RequestID,
			Patch:     AggregatePatch{ExecutionState: &state, AppendGates: authorizationGates},
		})
		if mutateErr != nil {
			return result.Aggregate, mutateErr
		}
		return result.Aggregate, nil
	}

	now := service.now().UTC()
	runID := stableID("psr_", aggregate.Workspace.ID, request.RequestID)
	stage := StageRun{
		ID: runID, Stage: "implementation", State: ExecutionRunning,
		CharterID: charter.ID, HeadSHA: charter.HeadSHA,
		Attempt: countStageRuns(aggregate.StageRuns, "implementation") + 1, StartedAt: now,
	}
	patch := AggregatePatch{AppendGates: authorizationGates, AppendStageRuns: []StageRun{stage}}
	workingFindings := append([]Finding(nil), selected...)
	addressedFindings := make(map[string]Finding, len(selected))
	completed := false
	var validationForNextRepair map[string]any
	var finalizedRepair *RepairResult
	var delayedNudgeErr error
	for cycle := 1; cycle <= maxCycles; cycle++ {
		for _, finding := range workingFindings {
			addressedFindings[finding.ID] = finding
		}
		instruction := implementationInstruction(charter, workingFindings, cycle)
		repairContext := implementationContextBundle(aggregate)
		repairContext.Findings = upsertByID(
			repairContext.Findings,
			patch.UpsertFindings,
			func(value Finding) string { return value.ID },
		)
		repairContext.Validation = validationForNextRepair
		repairStart := service.now().UTC()
		repair, repairErr := config.Repair.Repair(ctx, RepairRequest{
			Context: repairContext, AuthorizedFindingIDs: findingIDs(workingFindings),
			Instruction: instruction, Attempt: cycle,
		})
		if repairErr != nil {
			stage.State = ExecutionFailed
			stage.PublicError = "repair_failed"
			finished := service.now().UTC()
			stage.FinishedAt = &finished
			setStageInPatch(&patch, stage)
			break
		}
		scopeBundle := implementationContextBundle(aggregate)
		scopeBundle.Charter, scopeBundle.Findings, scopeBundle.CandidateDiff = charter, workingFindings, repair.CandidateDiff
		scopeBundle.CandidateMetrics = CandidateMetrics{
			Files: len(repair.ChangedFiles), SemanticLines: repair.SemanticLines,
			Modules: repair.Modules, ChangedFiles: append([]string(nil), repair.ChangedFiles...),
		}
		scopeAudit, scopePromptDigest, scopeErr := service.ai.RunScopeAudit(ctx, scopeBundle)
		if scopeErr != nil {
			stage.State, stage.PublicError = ExecutionFailed, "scope_audit_ai_failed"
			finished := service.now().UTC()
			stage.FinishedAt = &finished
			setStageInPatch(&patch, stage)
			break
		}
		scopeAudit, mismatch := bindScopeAuditCandidateEvidence(scopeAudit, repair)
		if mismatch != "" {
			stage.State, stage.PublicError = ExecutionFailed, "scope_audit_evidence_mismatch"
			stage.Summary = "Scope audit evidence mismatch: " + mismatch
			finished := service.now().UTC()
			stage.FinishedAt = &finished
			setStageInPatch(&patch, stage)
			break
		}
		deterministicSize := ClassifyChangeSize(
			len(repair.ChangedFiles),
			repair.SemanticLines,
			repair.Modules,
			request.SizePolicy,
		)
		actualScope := ScopeAssessment{
			Distance:       scopeAudit.WorstDistance,
			Size:           worstChangeSize(scopeAudit.WorstSize, deterministicSize),
			Presence:       WorkCandidatePresent,
			Files:          len(repair.ChangedFiles),
			SemanticLines:  repair.SemanticLines,
			Modules:        repair.Modules,
			TypeCompatible: filesTypeCompatible(charter.Type, repair.ChangedFiles) && scopeAudit.TypeCompatible,
			Confidence:     scopeAudit.Confidence,
			CharterClauses: append([]string(nil), scopeAudit.CharterClauses...),
			Explanation:    scopeAudit.Explanation,
			ChangeEvidence: append([]ScopeChange(nil), scopeAudit.Changes...),
		}
		repairFinish := service.now().UTC()
		scopeBlockers := materializeCandidateScopeBlockers(
			aggregate,
			runID,
			charter.Type,
			scopeAudit,
			repairFinish,
			fmt.Sprint(cycle),
		)
		patch.UpsertFindings = append(patch.UpsertFindings, scopeBlockers...)
		attempt := RepairAttempt{
			ID:         stableID("pra_", aggregate.Workspace.ID, request.RequestID, fmt.Sprint(cycle)),
			StageRunID: runID, Number: cycle, State: ExecutionSucceeded,
			Instruction: instruction, WorkspaceID: repair.WorkspaceID, ResultSummary: repair.Summary,
			ChangedFiles: append([]string(nil), repair.ChangedFiles...),
			FindingIDs:   findingIDsFromMap(addressedFindings), CandidateSHA: repair.CandidateSHA,
			Scope: actualScope, PromptDigest: repair.PromptDigest, ScopePromptDigest: scopePromptDigest,
			StartedAt: repairStart, FinishedAt: &repairFinish,
		}
		patch.AppendRepairs = append(patch.AppendRepairs, attempt)
		// Candidate-present S2/S3 or PR-type-incompatible code is never an
		// approvable implementation result. Stop before validation, completion,
		// and finalization so those later signals cannot launder the candidate
		// into a publishable state. The dedicated human gate offers only removal
		// plus follow-up, charter revision, or stop.
		if hasHardCandidateScope(actualScope, scopeBlockers) {
			scopeGate, gateErr := service.startImplementationScopeGate(ctx, aggregate, map[string]any{
				"charter": charter, "repair": attempt, "scope": actualScope, "candidate_drift": scopeBlockers,
			}, actualScope, scopeBlockers)
			if gateErr != nil {
				return Aggregate{}, gateErr
			}
			scopeGate.TargetID = attempt.ID
			patch.AppendGates = append(patch.AppendGates, scopeGate)
			stage.State = ExecutionWaitingGate
			stage.PublicError = "candidate_scope_requires_resolution"
			stage.FinishedAt = &repairFinish
			setStageInPatch(&patch, stage)
			break
		}
		validationID := stableID("pvr_", aggregate.Workspace.ID, request.RequestID, fmt.Sprint(cycle))
		validation, validationErr := config.Validation.Validate(ctx, ValidationRequest{
			ID:          validationID,
			WorkspaceID: aggregate.Workspace.ID, Charter: charter, Provider: aggregate.ProviderSnapshot,
			CandidateSHA: repair.CandidateSHA, ChangedFiles: repair.ChangedFiles, CandidateDiff: repair.CandidateDiff,
		})
		validationIdentityChanged := validation.ID != "" && validation.ID != validationID ||
			validation.CandidateSHA != "" && validation.CandidateSHA != repair.CandidateSHA
		validation.ID = validationID
		validation.StageRunID = runID
		validation.CandidateSHA = repair.CandidateSHA
		patch.AppendValidations = append(patch.AppendValidations, validation)
		if validationIdentityChanged {
			stage.State, stage.PublicError = ExecutionFailed, "validation_evidence_mismatch"
			stage.Summary = "Local validation returned evidence for a different candidate or attempt"
			finished := service.now().UTC()
			stage.FinishedAt = &finished
			setStageInPatch(&patch, stage)
			break
		}
		if blocker := validationDefinitionBlocker(validation); blocker != "" {
			stage.State, stage.PublicError = ExecutionBlocked, blocker
			stage.Summary = validationDefinitionBlockerSummary(blocker)
			finished := service.now().UTC()
			stage.FinishedAt = &finished
			setStageInPatch(&patch, stage)
			break
		}
		if validationErr != nil || validationInfrastructureFailed(validation) {
			stage.State, stage.PublicError = ExecutionFailed, "validation_infrastructure_failed"
			stage.Summary = "Local validation infrastructure stopped before it could produce reliable code evidence"
			finished := service.now().UTC()
			stage.FinishedAt = &finished
			setStageInPatch(&patch, stage)
			break
		}
		if !validationGreen(validation) {
			validationForNextRepair = validationRepairEvidence(validation)
			workingFindings = []Finding{
				{
					ID: stableID(
						"pfn_",
						aggregate.Workspace.ID,
						request.RequestID,
						"validation",
						fmt.Sprint(cycle),
					),
					Fingerprint: stableID(
						"sha256:",
						"validation",
						repair.CandidateSHA,
					),
					Origin:      FindingOriginImplementation,
					OriginRunID: runID,
					Severity:    "high",
					Title:       "Validation remains non-green",
					Message:     "Repair the failed validation checks before completion.",
					Validation:  validationRepairSummary(validation),
					Scope: ScopeAssessment{
						Distance:       ScopeExact,
						Size:           ChangeSizeXS,
						TypeCompatible: true,
						Confidence:     1,
					},
					Disposition: FindingInScope,
					Version:     1,
					CreatedAt:   repairFinish,
					UpdatedAt:   repairFinish,
				},
			}
			patch.UpsertFindings = append(patch.UpsertFindings, workingFindings...)
			continue
		}
		completionBundle := implementationContextBundle(aggregate)
		completionBundle.Charter = charter
		completionBundle.Findings = upsertByID(
			completionBundle.Findings,
			patch.UpsertFindings,
			func(value Finding) string { return value.ID },
		)
		completionBundle.CandidateDiff = repair.CandidateDiff
		completionBundle.CandidateMetrics = CandidateMetrics{
			Files: len(repair.ChangedFiles), SemanticLines: repair.SemanticLines,
			Modules: repair.Modules, ChangedFiles: append([]string(nil), repair.ChangedFiles...),
		}
		completionBundle.Validation = map[string]any{"run": validation}
		durableRounds := append(append([]NudgeRoundRecord(nil), aggregate.NudgeRounds...), patch.AppendNudgeRounds...)
		rounds, completionErr := service.ai.RunCompletionAudit(
			ctx,
			completionBundle,
			request.NudgePolicy,
			NudgeStrategyStats(durableRounds, NudgeImplementationDone),
		)
		if completionErr != nil && len(rounds) == 0 {
			delayedNudgeErr = completionErr
			stage.State = ExecutionFailed
			stage.PublicError = "completion_audit_failed"
			stage.FinishedAt = &repairFinish
			setStageInPatch(&patch, stage)
			break
		}
		if !completionRoundsMatchCandidateScope(rounds, actualScope) {
			stage.State = ExecutionFailed
			stage.PublicError = "completion_candidate_evidence_mismatch"
			stage.FinishedAt = &repairFinish
			setStageInPatch(&patch, stage)
			break
		}
		materializationAggregate := aggregate
		materializationAggregate.StageRuns = append(materializationAggregate.StageRuns, stage)
		materializationAggregate.Findings = upsertByID(
			aggregate.Findings,
			patch.UpsertFindings,
			func(value Finding) string { return value.ID },
		)
		missing, deferred, candidateDrift, nudges := materializeCompletionRounds(
			materializationAggregate,
			runID,
			rounds,
			request.NudgePolicy,
			repairFinish,
			fmt.Sprint(cycle),
		)
		stage.Evidence = completionStageEvidence(
			runID, "implementation_completion", rounds[len(rounds)-1].Result.Summary, rounds[0].PromptDigest,
			rounds, append(append(append([]Finding(nil), missing...), deferred...), candidateDrift...),
			map[string]any{"run": validation}, repairFinish,
		)
		patch.UpsertFindings = append(patch.UpsertFindings, missing...)
		patch.UpsertFindings = append(patch.UpsertFindings, deferred...)
		patch.UpsertFindings = append(patch.UpsertFindings, candidateDrift...)
		patch.AppendNudgeRounds = append(patch.AppendNudgeRounds, nudges...)
		if completionErr != nil {
			delayedNudgeErr = completionErr
			stage.State = ExecutionFailed
			stage.PublicError = "completion_nudge_failed"
			stage.FinishedAt = &repairFinish
			setStageInPatch(&patch, stage)
			break
		}
		if len(missing) > 0 {
			workingFindings = missing
			continue
		}
		if finalizer, ok := config.Repair.(RepairFinalizer); ok {
			finalized, finalizeErr := finalizer.FinalizeRepair(ctx, request.WorkspaceID, repair)
			if finalizeErr != nil {
				stage.State = ExecutionFailed
				stage.PublicError = "candidate_finalize_failed"
				stage.FinishedAt = &repairFinish
				setStageInPatch(&patch, stage)
				break
			}
			repair = finalized
			finalizedCopy := finalized
			finalizedRepair = &finalizedCopy
			attempt.CandidateSHA = finalized.CandidateSHA
			attempt.ResultSummary = finalized.Summary
			attempt.PublicationFence = finalized.PublicationFence
			patch.AppendRepairs[len(patch.AppendRepairs)-1] = attempt
		}
		var completionGates []GateRun
		allCandidateDrift := append(append([]Finding(nil), scopeBlockers...), candidateDrift...)
		if DecideScope(actualScope) != ScopeActionProceed || len(allCandidateDrift) > 0 {
			scopeGate, gateErr := service.startImplementationScopeGate(ctx, aggregate, map[string]any{
				"charter": charter, "repair": attempt, "scope": actualScope, "candidate_drift": allCandidateDrift,
			}, actualScope, allCandidateDrift)
			if gateErr != nil {
				return Aggregate{}, gateErr
			}
			scopeGate.TargetID = attempt.ID
			completionGates = append(completionGates, scopeGate)
		}
		projected := aggregate
		projected.StageRuns = append(append([]StageRun(nil), aggregate.StageRuns...), stage)
		projected.Findings = upsertByID(
			append([]Finding(nil), aggregate.Findings...),
			patch.UpsertFindings,
			func(value Finding) string { return value.ID },
		)
		contextRevision, revisionErr := implementationCompletionContextRevision(projected)
		if revisionErr != nil {
			return Aggregate{}, revisionErr
		}
		completeSubject := map[string]any{
			"charter": charter, "repair": attempt, "validation": validation, "completion_rounds": rounds,
			"implementation_context_revision": contextRevision,
		}
		completeGate, gateErr := service.startGate(ctx, aggregate, "pr.implementation.complete", completeSubject)
		if gateErr != nil {
			return Aggregate{}, gateErr
		}
		completeGate, gateErr = pinGateSubject(completeGate, completeSubject)
		if gateErr != nil {
			return Aggregate{}, gateErr
		}
		completeGate.TargetID = attempt.ID
		completionGates = append(completionGates, completeGate)
		patch.AppendGates = append(patch.AppendGates, completionGates...)
		if !allGatesPassed(completionGates) {
			stage.State = ExecutionWaitingGate
			stage.FinishedAt = &repairFinish
			setStageInPatch(&patch, stage)
			break
		}
		stage.State = ExecutionSucceeded
		stage.Summary = repair.Summary
		stage.FinishedAt = &repairFinish
		setStageInPatch(&patch, stage)
		for _, finding := range addressedFindings {
			finding.Disposition = FindingFixed
			finding = setFindingNudgeReward(finding, NudgeReward(RewardConfirmedFixed), "green_validation")
			finding.Version++
			finding.UpdatedAt = repairFinish
			patch.UpsertFindings = append(patch.UpsertFindings, finding)
		}
		completed = true
		break
	}
	if !completed && stage.State == ExecutionRunning {
		finished := service.now().UTC()
		stage.State, stage.PublicError, stage.FinishedAt = ExecutionBlocked, "completion_incomplete", &finished
		setStageInPatch(&patch, stage)
	}
	phase, state := PhaseImplementation, ExecutionBlocked
	if completed {
		phase, state = PhasePublication, ExecutionWaitingGate
	} else if stage.State == ExecutionWaitingGate {
		state = ExecutionWaitingGate
	} else if stage.State == ExecutionFailed {
		state = ExecutionFailed
	}
	patch.Phase, patch.ExecutionState = &phase, &state
	activitySummary := fmt.Sprintf("Implementation finished after %d repair attempts", len(patch.AppendRepairs))
	if stage.State == ExecutionFailed {
		activitySummary = "Implementation stopped before a publishable candidate was ready"
	} else if stage.State == ExecutionBlocked {
		activitySummary = "Implementation needs more work before publication"
	} else if stage.State == ExecutionWaitingGate {
		activitySummary = "Implementation candidate is waiting for authorization"
	}
	patch.Activity = append(patch.Activity, Activity{
		Kind: "implementation.finished", Actor: "ai", EntityID: runID,
		Summary: activitySummary, CreatedAt: service.now().UTC(),
	})
	refreshNudgeRewardsForPatch(aggregate, &patch)
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID, Patch: patch,
	})
	if err != nil {
		return result.Aggregate, err
	}
	if finalizedRepair != nil {
		if acknowledger, ok := config.Repair.(RepairFinalizationAcknowledger); ok {
			if acknowledgeErr := acknowledger.AcknowledgeFinalizedRepair(
				ctx, request.WorkspaceID, *finalizedRepair,
			); acknowledgeErr != nil {
				return result.Aggregate, acknowledgeErr
			}
		}
	}
	if delayedNudgeErr != nil {
		return result.Aggregate, delayedNudgeErr
	}
	return result.Aggregate, nil
}

// A first implementation run starts only after review and triage have
// produced a fully classified set of findings. Once an implementation has
// actually run, failed, blocked, or completion-audit-queued candidates may be
// retried without walking the lifecycle backwards through review.
func implementationStartReady(aggregate Aggregate, charter Charter) bool {
	if aggregate.Workspace.Phase != PhaseTriage && aggregate.Workspace.Phase != PhaseImplementation {
		return false
	}
	if !hasSuccessfulStageAtHead(aggregate.StageRuns, "review", charter.ID, charter.HeadSHA) {
		return false
	}
	for _, finding := range currentContextFindings(aggregate, charter, true) {
		switch finding.Disposition {
		case FindingInScope, FindingFixed, FindingDeferred, FindingDismissed:
		default:
			return false
		}
	}
	if aggregate.Workspace.Phase == PhaseTriage {
		return true
	}
	switch aggregate.Workspace.ExecutionState {
	case ExecutionFailed, ExecutionBlocked, ExecutionQueued:
	default:
		return false
	}
	for index := len(aggregate.StageRuns) - 1; index >= 0; index-- {
		stage := aggregate.StageRuns[index]
		if stage.Stage == "implementation" && stage.CharterID == charter.ID &&
			stage.HeadSHA == charter.HeadSHA && stage.State != ExecutionCanceled && stage.State != ExecutionStale {
			return true
		}
	}
	return false
}

// The completion gate is deliberately bound only to implementation-relevant
// shared context. Reward bookkeeping and UI-only state may change without
// invalidating authorization; guidance, corrections, findings, lessons, and
// durable stage evidence may not.
func implementationCompletionContextRevision(aggregate Aggregate) (string, error) {
	bundle := implementationContextBundle(aggregate)
	findings := append([]Finding(nil), bundle.Findings...)
	for index := range findings {
		// Passing the completion gate is itself allowed to mark addressed
		// findings fixed and update reward bookkeeping. Exclude exactly those
		// outcome fields so the same authorization remains fresh for the branch
		// publication command; newly added findings and stage evidence still
		// change the snapshot.
		findings[index].Disposition = ""
		findings[index].NudgeReward = nil
		findings[index].RewardSource = ""
		findings[index].Version = 0
		findings[index].UpdatedAt = time.Time{}
	}
	snapshot := struct {
		Messages          []Message          `json:"messages,omitempty"`
		Findings          []Finding          `json:"findings,omitempty"`
		Corrections       []Correction       `json:"corrections,omitempty"`
		RepositoryLessons []RepositoryLesson `json:"repository_lessons,omitempty"`
		PriorEvidence     []StageEvidence    `json:"prior_evidence,omitempty"`
	}{
		Messages: bundle.Messages, Findings: findings,
		Corrections: bundle.Corrections, RepositoryLessons: bundle.RepositoryLessons,
		PriorEvidence: bundle.PriorEvidence,
	}
	// Stage evidence is restored from persistence with generic JSON objects in
	// Validation, while an in-flight run still contains concrete Go structs.
	// Normalize both representations before hashing so semantically identical
	// evidence produces the same authorization revision across a store roundtrip.
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return "", err
	}
	return fingerprintValue(normalized)
}

func allGatesPassed(gates []GateRun) bool {
	for _, gate := range gates {
		if !gateAllowsProgress(gate) {
			return false
		}
	}
	return true
}

func setStageInPatch(patch *AggregatePatch, stage StageRun) {
	for index := range patch.AppendStageRuns {
		if patch.AppendStageRuns[index].ID == stage.ID {
			patch.AppendStageRuns[index] = stage
			return
		}
	}
	patch.ReplaceStageRuns = []StageRun{stage}
}

func selectImplementationFindings(all []Finding, gates []GateRun, ids []string) ([]Finding, error) {
	if len(ids) == 0 {
		for _, finding := range all {
			if finding.Disposition == FindingInScope {
				ids = append(ids, finding.ID)
			}
		}
	}
	seen := make(map[string]struct{}, len(ids))
	selected := make([]Finding, 0, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			return nil, ErrInvalid
		}
		seen[id] = struct{}{}
		finding, index := findFinding(all, id)
		if index < 0 || finding.Disposition != FindingInScope ||
			(DecideScope(finding.Scope) != ScopeActionProceed &&
				!findingClassificationPassed(gates, finding.ID) && !findingRemovalAuthorized(gates, finding)) {
			return nil, fmt.Errorf("%w: finding %s is not eligible for implementation", ErrInvalid, id)
		}
		selected = append(selected, finding)
	}
	return selected, nil
}

func findingClassificationPassed(gates []GateRun, findingID string) bool {
	for index := len(gates) - 1; index >= 0; index-- {
		gate := gates[index]
		if gate.DecisionPoint == "pr.finding.classify" && gate.TargetID == findingID {
			return gateCompletedWith(gate, "keep-in-pr")
		}
	}
	return false
}

func findingRemovalAuthorized(gates []GateRun, finding Finding) bool {
	if !HardCandidateScopeBlocker(finding.Scope) {
		return false
	}
	for index := len(gates) - 1; index >= 0; index-- {
		gate := gates[index]
		if gate.DecisionPoint != "pr.implementation.hard-scope" || !gateCompletedWith(gate, "defer-follow-up") {
			continue
		}
		for _, id := range gate.Evidence.HardScopeFindingIDs {
			if id == finding.ID {
				return true
			}
		}
	}
	return false
}

func implementationInstruction(charter Charter, findings []Finding, attempt int) string {
	sort.Slice(findings, func(i, j int) bool { return findings[i].ID < findings[j].ID })
	var builder strings.Builder
	fmt.Fprintf(
		&builder,
		"Implement confirmed charter %s (type %s), attempt %d.\nGoal: %s\n",
		charter.ID,
		charter.Type,
		attempt,
		charter.Goal,
	)
	builder.WriteString("Satisfy every acceptance criterion in the confirmed charter:\n")
	for _, criterion := range charter.AcceptanceCriteria {
		fmt.Fprintf(&builder, "- %s\n", criterion)
	}
	builder.WriteString("Also address these confirmed in-scope findings:\n")
	for _, finding := range findings {
		if finding.Scope.Presence == WorkCandidatePresent && DecideScope(finding.Scope) != ScopeActionProceed {
			fmt.Fprintf(
				&builder,
				"- Remove candidate-present scope drift %s: %s — %s. Do not implement or expand it.\n",
				finding.ID,
				finding.Title,
				finding.Message,
			)
			continue
		}
		fmt.Fprintf(&builder, "- %s: %s — %s\n", finding.ID, finding.Title, finding.Message)
	}
	builder.WriteString("Do not add adjacent cleanup. Report blockers instead.")
	return builder.String()
}

func findingIDs(findings []Finding) []string {
	ids := make([]string, 0, len(findings))
	for _, finding := range findings {
		ids = append(ids, finding.ID)
	}
	sort.Strings(ids)
	return ids
}

func findingIDsFromMap(findings map[string]Finding) []string {
	ids := make([]string, 0, len(findings))
	for id := range findings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func filesTypeCompatible(prType PRType, files []string) bool {
	for _, path := range files {
		if !DeterministicTypeCompatible(prType, ClassifyFile(path)) {
			return false
		}
	}
	return true
}

func validationGreen(run ValidationRun) bool {
	if run.State != ExecutionSucceeded || len(run.Checks) == 0 {
		return false
	}
	for _, check := range run.Checks {
		if check.Status != "passed" && check.Status != "skipped" {
			return false
		}
	}
	return true
}

func validationInfrastructureFailed(run ValidationRun) bool {
	for _, check := range run.Checks {
		switch check.Status {
		case "environment_unavailable", "infrastructure_error", "canceled":
			return true
		}
	}
	return false
}

func validationDefinitionBlocker(run ValidationRun) string {
	for _, check := range run.Checks {
		if check.Status == "plan_changed" {
			return "validation_plan_changed"
		}
	}
	for _, check := range run.Checks {
		if check.Status == "incomplete" {
			return "validation_plan_incomplete"
		}
	}
	return ""
}

func validationDefinitionBlockerSummary(blocker string) string {
	switch blocker {
	case "validation_plan_changed":
		return "Local validation definitions changed in the candidate and require explicit scope resolution"
	case "validation_plan_incomplete":
		return "Local validation could not derive a complete executable plan and requires explicit configuration"
	default:
		return "Local validation requires explicit resolution before code repair can continue"
	}
}

const (
	maxRepairValidationChecks       = 32
	maxRepairValidationSummaryBytes = 32 << 10
	maxRepairValidationCheckBytes   = 2 << 10
)

// validationRepairEvidence is the bounded, public check projection supplied
// to the next repair attempt. It excludes raw runner structures while
// retaining enough check status/output to make a code repair evidence-driven.
func validationRepairEvidence(run ValidationRun) map[string]any {
	type checkEvidence struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Status  string `json:"status"`
		Summary string `json:"summary,omitempty"`
	}
	limit := len(run.Checks)
	if limit > maxRepairValidationChecks {
		limit = maxRepairValidationChecks
	}
	checks := make([]checkEvidence, 0, limit)
	remaining := maxRepairValidationSummaryBytes
	for _, check := range run.Checks[:limit] {
		if remaining <= 0 {
			break
		}
		summaryLimit := maxRepairValidationCheckBytes
		if summaryLimit > remaining {
			summaryLimit = remaining
		}
		summary := boundedUTF8(strings.TrimSpace(check.Summary), summaryLimit)
		remaining -= len(summary)
		checks = append(checks, checkEvidence{
			ID:      boundedUTF8(strings.TrimSpace(check.ID), 256),
			Name:    boundedUTF8(strings.TrimSpace(check.Name), 256),
			Status:  boundedUTF8(strings.TrimSpace(check.Status), 128),
			Summary: summary,
		})
	}
	return map[string]any{
		"attempt_id": run.ID, "candidate_sha": run.CandidateSHA,
		"state": run.State, "checks": checks,
		"checks_omitted": len(run.Checks) - len(checks),
	}
}

func validationRepairSummary(run ValidationRun) string {
	encoded, err := json.Marshal(validationRepairEvidence(run))
	if err != nil {
		return "Validation failed; bounded check evidence was unavailable."
	}
	return boundedUTF8(string(encoded), maxRepairValidationSummaryBytes)
}

func boundedUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func materializeCandidateScopeBlockers(
	aggregate Aggregate,
	runID string,
	prType PRType,
	audit ScopeAuditPass,
	now time.Time,
	namespace string,
) []Finding {
	blockers := make([]Finding, 0)
	for index, change := range audit.Changes {
		change.TypeCompatible = change.TypeCompatible && DeterministicTypeCompatible(prType, ClassifyFile(change.Path))
		scope := ScopeAssessment{
			Distance: change.Distance, Size: change.Size, Presence: WorkCandidatePresent,
			Files: 1, SemanticLines: change.SemanticLines, Modules: 1,
			TypeCompatible: change.TypeCompatible, Confidence: change.Confidence,
			CharterClauses: append([]string(nil), change.CharterClauses...),
			Explanation:    change.Explanation,
			ChangeEvidence: []ScopeChange{change},
		}
		if !HardCandidateScopeBlocker(scope) {
			continue
		}
		fingerprint := stableID(
			"sha256:", aggregate.Workspace.ID, runID, namespace, change.Path,
			change.Hunk, string(change.Distance), fmt.Sprint(change.TypeCompatible),
		)
		title := "Candidate change is outside the confirmed PR scope"
		if !change.TypeCompatible {
			title = "Candidate change violates the confirmed PR type"
		}
		blockers = append(blockers, Finding{
			ID:          stableID("pfn_", aggregate.Workspace.ID, runID, namespace, fmt.Sprint(index), fingerprint),
			Fingerprint: fingerprint, Origin: FindingOriginImplementation, OriginRunID: runID,
			Severity: "high", Title: title, File: change.Path,
			Message: change.Explanation, Evidence: change.Path + "\n" + change.Hunk,
			Impact:         "This candidate cannot be published under the confirmed charter and PR type.",
			Recommendation: "Remove this candidate code, or revise and reconfirm the charter before implementing it.",
			Validation:     "Re-run the exact candidate scope audit after the code or charter changes.",
			Scope:          scope, Disposition: FindingOpen, Version: 1, CreatedAt: now, UpdatedAt: now,
		})
	}
	return blockers
}

func hasHardCandidateScope(scope ScopeAssessment, findings []Finding) bool {
	if HardCandidateScopeBlocker(scope) {
		return true
	}
	for _, finding := range findings {
		if HardCandidateScopeBlocker(finding.Scope) {
			return true
		}
	}
	return false
}

func (service *Service) startImplementationScopeGate(
	ctx context.Context,
	aggregate Aggregate,
	subject map[string]any,
	scope ScopeAssessment,
	candidateDrift []Finding,
) (GateRun, error) {
	if !hasHardCandidateScope(scope, candidateDrift) {
		return service.startGate(ctx, aggregate, "pr.implementation.scope", subject)
	}
	return service.startGate(
		ctx,
		aggregate,
		"pr.implementation.hard-scope",
		subject,
	)
}

func materializeCompletionRounds(
	aggregate Aggregate,
	runID string,
	rounds []CompletionRound,
	policy NudgePolicy,
	now time.Time,
	namespaceParts ...string,
) ([]Finding, []Finding, []Finding, []NudgeRoundRecord) {
	namespace := runID
	for _, part := range namespaceParts {
		namespace += "\x00" + part
	}
	charter, hasCharter := aggregate.ActiveCharter()
	current := currentContextFindings(aggregate, charter, hasCharter)
	known := make(map[string]Finding, len(current))
	for _, finding := range current {
		known[finding.Fingerprint] = finding
	}
	var missing, deferred, candidateDrift []Finding
	materializedMissing := make(map[string]int)
	materializedDeferred := make(map[string]int)
	materializedDrift := make(map[string]int)
	var nudges []NudgeRoundRecord
	for roundIndex, round := range rounds {
		roundID := ""
		if !round.Initial {
			roundID = stableID("pnr_", aggregate.Workspace.ID, namespace, "completion", fmt.Sprint(round.Round))
		}
		var roundFindingIDs []string
		appendCandidate := func(candidate CompletionFinding, kind string) {
			fingerprint := completionFindingFingerprint(candidate)
			existing, exists := known[fingerprint]
			// Resolved history remains immutable evidence. If an audit reports the
			// same defect again, create a fresh occurrence instead of erasing the
			// prior resolution or dropping the current blocker as a duplicate.
			if exists && (existing.Disposition == FindingFixed || existing.Disposition == FindingDismissed) {
				exists = false
			}
			id := stableID("pfn_", aggregate.Workspace.ID, namespace, fmt.Sprint(roundIndex), fingerprint)
			if exists {
				id = existing.ID
			} else {
				roundFindingIDs = appendUniqueString(roundFindingIDs, id)
			}
			disposition := FindingDeferred
			target := &deferred
			indexes := materializedDeferred
			switch kind {
			case "missing":
				disposition, target, indexes = reviewFindingDisposition(
					completionFindingScope(candidate),
				), &missing, materializedMissing
				if disposition == FindingDeferred {
					disposition = FindingOpen
				}
			case "candidate_drift":
				disposition, target, indexes = FindingOpen, &candidateDrift, materializedDrift
			}
			finding := completionFindingRecord(
				existing,
				exists,
				id,
				fingerprint,
				runID,
				roundID,
				candidate,
				disposition,
				round.Source,
				now,
			)
			if index, duplicate := indexes[fingerprint]; duplicate {
				(*target)[index] = finding
			} else {
				indexes[fingerprint] = len(*target)
				*target = append(*target, finding)
			}
			known[fingerprint] = finding
		}
		for _, finding := range round.Result.Missing {
			appendCandidate(finding, "missing")
		}
		for _, finding := range round.Result.OutOfScope {
			if finding.Presence == WorkCandidatePresent {
				appendCandidate(finding, "candidate_drift")
			} else {
				appendCandidate(finding, "deferred")
			}
		}
		if !round.Initial {
			nudges = append(nudges, NudgeRoundRecord{
				ID:         roundID,
				StageRunID: runID, Stage: NudgeImplementationDone, Round: round.Round,
				MinimumRounds: policy.MinimumAdditionalRounds, HardCap: policy.MaximumAdditionalRounds,
				Strategy: round.Strategy, Challenge: round.Challenge,
				VariantDigest: round.VariantDigest, PromptDigest: round.PromptDigest,
				State: round.State, PublicError: round.PublicError,
				NovelFindings: round.NovelFindings, DuplicateCount: round.DuplicateCount,
				FindingIDs: roundFindingIDs,
				CreatedAt:  now,
			})
		}
	}
	return missing, deferred, candidateDrift, nudges
}

func completionRoundsMatchCandidateScope(rounds []CompletionRound, scope ScopeAssessment) bool {
	exact := make(map[string]ScopeChange, len(scope.ChangeEvidence))
	for _, change := range scope.ChangeEvidence {
		if change.Presence != WorkCandidatePresent {
			continue
		}
		hunk, ok := candidateHunkCoordinate(change.Hunk)
		if !ok {
			return false
		}
		exact[change.Path+"\x00"+hunk] = change
	}
	for _, round := range rounds {
		if round.State != ExecutionSucceeded {
			continue
		}
		findings := append(append([]CompletionFinding(nil), round.Result.Missing...), round.Result.OutOfScope...)
		for _, finding := range findings {
			if finding.Presence != WorkCandidatePresent {
				continue
			}
			hunk, validHunk := candidateHunkCoordinate(finding.Hunk)
			if !validHunk {
				return false
			}
			change, ok := exact[finding.File+"\x00"+hunk]
			if !ok || finding.Module != change.Module || finding.SemanticLines != change.SemanticLines ||
				finding.ChangeSize != change.Size ||
				scopeDistanceRank(finding.ScopeDistance) < scopeDistanceRank(change.Distance) ||
				(!change.TypeCompatible && finding.TypeCompatible) {
				return false
			}
		}
	}
	return true
}

func completionFindingRecord(
	existing Finding,
	reuse bool,
	id, fingerprint, runID, roundID string,
	candidate CompletionFinding,
	disposition FindingDisposition,
	source *AIExecutionSource,
	now time.Time,
) Finding {
	createdAt, version := now, int64(1)
	origin, originRunID, nudgeRoundID := FindingOriginNudge, runID, roundID
	var nudgeReward *float64
	rewardSource := ""
	if reuse {
		createdAt, version = existing.CreatedAt, existing.Version+1
		origin, originRunID, nudgeRoundID = existing.Origin, existing.OriginRunID, existing.NudgeRoundID
		nudgeReward, rewardSource = existing.NudgeReward, existing.RewardSource
		// Finding provenance is immutable. Reusing an older finding preserves
		// either its exact source or its deliberate lack of one; a later AI round
		// cannot retarget the same durable finding to a different session.
		source = existing.source
	}
	return Finding{
		ID: id, Fingerprint: fingerprint, Origin: origin, OriginRunID: originRunID,
		NudgeRoundID: nudgeRoundID, Severity: candidate.Severity, Title: candidate.Title,
		File: candidate.File, Line: candidate.Line, Message: candidate.Message,
		Evidence: candidate.Evidence, Impact: candidate.Impact,
		Validation: candidate.Validation,
		Scope:      completionFindingScope(candidate), Disposition: disposition,
		NudgeReward: nudgeReward, RewardSource: rewardSource,
		SourceAvailable: source != nil, source: cloneAIExecutionSource(source),
		Version: version, CreatedAt: createdAt, UpdatedAt: now,
	}
}

func cloneAIExecutionSource(source *AIExecutionSource) *AIExecutionSource {
	if source == nil {
		return nil
	}
	cloned := *source
	return &cloned
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func worstChangeSize(values ...ChangeSize) ChangeSize {
	worst := ChangeSizeXS
	for _, value := range values {
		if changeSizeRank(value) > changeSizeRank(worst) {
			worst = value
		}
	}
	return worst
}

func scopeAuditMatchesCandidate(audit ScopeAuditPass, repair RepairResult) bool {
	_, mismatch := bindScopeAuditCandidateEvidence(audit, repair)
	return mismatch == ""
}

// bindScopeAuditCandidateEvidence treats the model's path and hunk coordinate
// as an untrusted classification key, not as measurement authority. Once each
// reported key has a one-to-one match in the exact candidate diff, module
// identity, additions-plus-deletions, and aggregate rollups are derived from
// the trusted diff. This avoids turning fallible hunk arithmetic into a retry
// loop while preserving fail-closed coverage against fabricated, duplicate,
// or omitted hunks.
func bindScopeAuditCandidateEvidence(audit ScopeAuditPass, repair RepairResult) (ScopeAuditPass, string) {
	if !validScopeAuditPassShape(audit) {
		return ScopeAuditPass{}, "audit classification"
	}
	wanted := make(map[string]struct{}, len(repair.ChangedFiles))
	modulesByPath := make(map[string]string, len(repair.ChangedFiles))
	deterministicModules := make(map[string]struct{})
	for _, path := range repair.ChangedFiles {
		if strings.TrimSpace(path) == "" {
			return ScopeAuditPass{}, "changed file identity"
		}
		if _, duplicate := wanted[path]; duplicate {
			return ScopeAuditPass{}, "changed file identity"
		}
		module := candidatePathModule(path)
		if module == "" {
			return ScopeAuditPass{}, "changed file identity"
		}
		wanted[path] = struct{}{}
		modulesByPath[path] = module
		deterministicModules[module] = struct{}{}
	}
	if len(wanted) != len(repair.ChangedFiles) || len(deterministicModules) != repair.Modules {
		return ScopeAuditPass{}, "candidate metrics"
	}
	actualHunks, validDiff := exactCandidateDiffHunks(repair.CandidateDiff, wanted)
	if !validDiff {
		return ScopeAuditPass{}, "candidate diff"
	}
	if len(actualHunks) != len(audit.Changes) {
		return ScopeAuditPass{}, "hunk count"
	}
	canonical := audit
	canonical.Changes = append([]ScopeChange(nil), audit.Changes...)
	seen := make(map[string]struct{}, len(audit.Changes))
	semanticLines := 0
	worstDistance, worstSize, typeCompatible := ScopeExact, ChangeSizeXS, true
	confidence := 1.0
	for index, change := range canonical.Changes {
		if _, ok := wanted[change.Path]; !ok {
			return ScopeAuditPass{}, "hunk path"
		}
		hunk, validHunk := candidateHunkCoordinate(change.Hunk)
		if !validHunk {
			return ScopeAuditPass{}, "hunk coordinate"
		}
		key := change.Path + "\x00" + hunk
		hunkLines, ok := actualHunks[key]
		if !ok {
			return ScopeAuditPass{}, "hunk coordinate"
		}
		canonical.Changes[index].Hunk = hunk
		canonical.Changes[index].Module = modulesByPath[change.Path]
		canonical.Changes[index].SemanticLines = hunkLines
		semanticLines += hunkLines
		if scopeDistanceRank(change.Distance) > scopeDistanceRank(worstDistance) {
			worstDistance = change.Distance
		}
		if changeSizeRank(change.Size) > changeSizeRank(worstSize) {
			worstSize = change.Size
		}
		typeCompatible = typeCompatible && change.TypeCompatible
		if change.Confidence < confidence {
			confidence = change.Confidence
		}
		delete(actualHunks, key)
		seen[change.Path] = struct{}{}
	}
	if len(seen) != len(wanted) {
		return ScopeAuditPass{}, "file coverage"
	}
	if len(actualHunks) != 0 {
		return ScopeAuditPass{}, "hunk coverage"
	}
	if semanticLines != repair.SemanticLines {
		return ScopeAuditPass{}, "candidate metrics"
	}
	canonical.Files = len(wanted)
	canonical.SemanticLines = semanticLines
	canonical.Modules = len(deterministicModules)
	canonical.WorstDistance = worstDistance
	canonical.WorstSize = worstSize
	canonical.TypeCompatible = typeCompatible
	if len(canonical.Changes) > 0 {
		canonical.Confidence = confidence
	}
	if !validScopeAuditPass(canonical) {
		return ScopeAuditPass{}, "canonical evidence"
	}
	return canonical, ""
}

func candidatePathModule(path string) string {
	module, _, _ := strings.Cut(strings.TrimPrefix(path, "./"), "/")
	return module
}

// exactCandidateDiffHunks binds every model-reported path/hunk to the trusted
// canonical git diff. Hunk identity is the exact old/new coordinate portion;
// optional trailing function context is descriptive text and is deliberately
// excluded. One ScopeChange must still exist for every real unified-diff hunk,
// with the exact path and additions-plus-deletions count. This prevents
// internally consistent but fabricated evidence from authorizing a candidate.
func exactCandidateDiffHunks(diff string, wanted map[string]struct{}) (map[string]int, bool) {
	if len(wanted) == 0 {
		return map[string]int{}, strings.TrimSpace(diff) == ""
	}
	result := make(map[string]int)
	sections := make(map[string]struct{}, len(wanted))
	currentPath, currentKey := "", ""
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			left, right, ok := parseCandidateDiffPaths(strings.TrimPrefix(line, "diff --git "))
			if !ok || left != right {
				return nil, false
			}
			if _, ok = wanted[right]; !ok {
				return nil, false
			}
			if _, duplicate := sections[right]; duplicate {
				return nil, false
			}
			sections[right] = struct{}{}
			currentPath, currentKey = right, ""
			continue
		}
		if strings.HasPrefix(line, "@@") {
			if currentPath == "" {
				return nil, false
			}
			hunk, ok := candidateHunkCoordinate(line)
			if !ok {
				return nil, false
			}
			currentKey = currentPath + "\x00" + hunk
			if _, duplicate := result[currentKey]; duplicate {
				return nil, false
			}
			result[currentKey] = 0
			continue
		}
		if currentKey != "" && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) {
			result[currentKey]++
		}
	}
	if len(sections) != len(wanted) || len(result) == 0 {
		return nil, false
	}
	for path := range wanted {
		if _, ok := sections[path]; !ok {
			return nil, false
		}
	}
	return result, true
}

var candidateHunkCoordinatePattern = regexp.MustCompile(
	`^(@@ -[0-9]+(?:,[0-9]+)? \+[0-9]+(?:,[0-9]+)? @@)(?:[^\r\n]*)$`,
)

// candidateHunkCoordinate returns the security-relevant identity of a unified
// diff hunk. Git may append a best-effort function or section label after the
// second @@; that label is not part of the old/new line-range identity.
func candidateHunkCoordinate(value string) (string, bool) {
	match := candidateHunkCoordinatePattern.FindStringSubmatch(value)
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

func parseCandidateDiffPaths(value string) (string, string, bool) {
	left, rest, ok := parseCandidateDiffToken(value)
	if !ok {
		return "", "", false
	}
	right, rest, ok := parseCandidateDiffToken(rest)
	if !ok || strings.TrimSpace(rest) != "" || !strings.HasPrefix(left, "a/") || !strings.HasPrefix(right, "b/") {
		return "", "", false
	}
	return strings.TrimPrefix(left, "a/"), strings.TrimPrefix(right, "b/"), true
}

func parseCandidateDiffToken(value string) (string, string, bool) {
	value = strings.TrimLeft(value, " \t")
	if value == "" {
		return "", "", false
	}
	if value[0] != '"' {
		if index := strings.IndexAny(value, " \t"); index >= 0 {
			return value[:index], value[index:], value[:index] != ""
		}
		return value, "", true
	}
	escaped := false
	for index := 1; index < len(value); index++ {
		switch {
		case escaped:
			escaped = false
		case value[index] == '\\':
			escaped = true
		case value[index] == '"':
			decoded, err := strconv.Unquote(value[:index+1])
			return decoded, value[index+1:], err == nil && decoded != ""
		}
	}
	return "", "", false
}
