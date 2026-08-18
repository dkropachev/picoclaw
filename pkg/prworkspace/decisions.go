package prworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RespondGateRequest struct {
	WorkspaceID     string
	GateRunID       string
	ExpectedVersion int64
	RequestID       string
	FieldValues     map[string]any
}

func (service *Service) RespondGate(ctx context.Context, request RespondGateRequest) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		!validOpaqueID(request.GateRunID, "pgr_") || request.FieldValues == nil {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	gate, ok := findGate(aggregate.Gates, request.GateRunID)
	if !ok || (gate.State != ExecutionWaitingUser && gate.State != ExecutionWaitingGate) ||
		aggregate.Workspace.Version != request.ExpectedVersion {
		return aggregate, ErrConflict
	}
	normalizedValues, actionErr := validateSubmittedGateFieldValues(gate, request.FieldValues)
	if actionErr != nil {
		return aggregate, fmt.Errorf("%w: %v", ErrInvalid, actionErr)
	}
	originalEvidence := gate.Evidence
	if service.gates != nil {
		gate, err = service.gates.Respond(ctx, gate, normalizedValues)
		if err != nil {
			return Aggregate{}, err
		}
	} else {
		gate = answerFallbackGate(gate, normalizedValues, service.now().UTC())
	}
	// The public evidence is a frozen projection of the private subject. A gate
	// responder may advance turns and field values, but cannot rewrite the evidence
	// that determines whether hard scope is passable.
	gate.Evidence = originalEvidence
	if gate.State == ExecutionSucceeded {
		action := gateAction(gate)
		if action == "" {
			return aggregate, errors.New("completed PR lifecycle gate returned no action field")
		}
		hardScopeResolution := gate.DecisionPoint == "pr.implementation.hard-scope" && gateHasHardCandidateScope(aggregate, gate)
		if hardScopeResolution && action == "approve" {
			return aggregate, fmt.Errorf("%w: candidate code outside the charter or PR type cannot be approved without removal or charter revision", ErrInvalid)
		}
		if action == gateProgressAction(gate.DecisionPoint) {
			if completionGate, found := implementationCompletionGateForPass(aggregate.Gates, gate); found {
				fresh, freshnessErr := implementationCompletionGateFresh(aggregate, completionGate)
				if freshnessErr != nil {
					return aggregate, freshnessErr
				}
				if !fresh {
					return service.invalidateImplementationCompletionGate(ctx, request, aggregate, completionGate)
				}
			}
		}
	}
	patch, err := service.gateActionPatch(aggregate, gate)
	if err != nil {
		return aggregate, err
	}
	patch.ReplaceGates = append(patch.ReplaceGates, gate)
	patch.Activity = append(patch.Activity, Activity{
		Kind: "gate.responded", Actor: "user", EntityID: gate.ID,
		Summary: gateResponseActivitySummary(gate), CreatedAt: service.now().UTC(),
	})
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID, Patch: patch,
	})
	if err != nil {
		return result.Aggregate, err
	}
	return result.Aggregate, nil
}

func gateResponseActivitySummary(gate GateRun) string {
	for index := len(gate.Turns) - 1; index >= 0; index-- {
		if action, ok := gate.Turns[index].FieldValues["action"].(string); ok && action != "" {
			return "Gate answered: " + action
		}
	}
	return "Gate answered"
}

func pinGateSubject(gate GateRun, subject map[string]any) (GateRun, error) {
	raw, err := json.Marshal(subject)
	if err != nil {
		return GateRun{}, err
	}
	runtime := &gateRuntime{}
	if gate.runtime != nil {
		*runtime = *gate.runtime
		runtime.PinnedPolicy = append(json.RawMessage(nil), gate.runtime.PinnedPolicy...)
	}
	runtime.PinnedSubject = append(json.RawMessage(nil), raw...)
	gate.runtime = runtime
	return gate, nil
}

// A scope approval can be the final gate after the completion gate has already
// passed, so both decision points must enforce the same freshness boundary.
func implementationCompletionGateForPass(gates []GateRun, answered GateRun) (GateRun, bool) {
	if answered.DecisionPoint == "pr.implementation.complete" {
		return answered, true
	}
	if answered.DecisionPoint != "pr.implementation.scope" {
		return GateRun{}, false
	}
	for index := len(gates) - 1; index >= 0; index-- {
		gate := gates[index]
		if gate.DecisionPoint == "pr.implementation.complete" && gate.TargetID == answered.TargetID &&
			gateCompletedWith(gate, "accept") {
			return gate, true
		}
	}
	return GateRun{}, false
}

func passedImplementationCompletionGate(gates []GateRun, repairID string) (GateRun, bool) {
	for index := len(gates) - 1; index >= 0; index-- {
		gate := gates[index]
		if gate.DecisionPoint == "pr.implementation.complete" && gate.TargetID == repairID &&
			gateCompletedWith(gate, "accept") {
			return gate, true
		}
	}
	return GateRun{}, false
}

func implementationCompletionGateFresh(aggregate Aggregate, gate GateRun) (bool, error) {
	if gate.DecisionPoint != "pr.implementation.complete" || gate.TargetID == "" ||
		gate.runtime == nil || len(gate.runtime.PinnedSubject) == 0 {
		return false, nil
	}
	var subject map[string]any
	if err := json.Unmarshal(gate.runtime.PinnedSubject, &subject); err != nil {
		return false, nil
	}
	if persistenceDigest(gate.runtime.PinnedSubject) != gate.SubjectRevision {
		return false, nil
	}
	pinnedRevision, ok := subject["implementation_context_revision"].(string)
	if !ok || pinnedRevision == "" {
		return false, nil
	}
	currentRevision, err := implementationCompletionContextRevision(aggregate)
	if err != nil {
		return false, err
	}
	if pinnedRevision != currentRevision || !completionGateTargetsLatestRepair(aggregate, gate) {
		return false, nil
	}
	for index := len(aggregate.Gates) - 1; index >= 0; index-- {
		candidate := aggregate.Gates[index]
		if candidate.DecisionPoint == gate.DecisionPoint && candidate.TargetID == gate.TargetID &&
			candidate.State != ExecutionCanceled && candidate.State != ExecutionStale {
			return candidate.ID == gate.ID, nil
		}
	}
	return false, nil
}

func completionGateTargetsLatestRepair(aggregate Aggregate, gate GateRun) bool {
	charter, ok := aggregate.ActiveCharter()
	if !ok || !charter.Confirmed || charter.HeadSHA != aggregate.ProviderSnapshot.HeadSHA {
		return false
	}
	latestID := ""
	for index := len(aggregate.RepairAttempts) - 1; index >= 0; index-- {
		attempt := aggregate.RepairAttempts[index]
		stage, found := findStageRun(aggregate.StageRuns, attempt.StageRunID)
		if found && stage.Stage == "implementation" && stage.CharterID == charter.ID &&
			stage.HeadSHA == charter.HeadSHA && stage.State != ExecutionCanceled && stage.State != ExecutionStale {
			latestID = attempt.ID
			break
		}
	}
	if latestID == "" || latestID != gate.TargetID {
		return false
	}
	attempt, found := findRepairAttempt(aggregate.RepairAttempts, gate.TargetID)
	if !found || attempt.State != ExecutionSucceeded || attempt.CandidateSHA == "" ||
		attempt.PublicationFence == nil || attempt.PublicationFence.BaseCommit != aggregate.ProviderSnapshot.HeadSHA ||
		attempt.PublicationFence.Tip != attempt.CandidateSHA ||
		HardCandidateScopeBlocker(attempt.Scope) || hardScopeGateTargetsRepair(aggregate.Gates, attempt.ID) {
		return false
	}
	validation, found := latestValidationForRepair(aggregate.ValidationRuns, attempt)
	return found && validationGreen(validation)
}

func (service *Service) invalidateImplementationCompletionGate(
	ctx context.Context,
	request RespondGateRequest,
	aggregate Aggregate,
	gate GateRun,
) (Aggregate, error) {
	now := service.now().UTC()
	gate.State, gate.FinishedAt = ExecutionStale, &now
	patch := AggregatePatch{ReplaceGates: []GateRun{gate}}
	for _, sibling := range aggregate.Gates {
		if sibling.ID == gate.ID || sibling.TargetID != gate.TargetID ||
			(sibling.State != ExecutionWaitingGate && sibling.State != ExecutionWaitingUser) {
			continue
		}
		sibling.State, sibling.FinishedAt = ExecutionCanceled, &now
		patch.ReplaceGates = append(patch.ReplaceGates, sibling)
	}
	latestTarget := latestImplementationRepairID(aggregate)
	if attempt, found := findRepairAttempt(aggregate.RepairAttempts, gate.TargetID); found {
		if stage, stageFound := findStageRun(aggregate.StageRuns, attempt.StageRunID); stageFound &&
			stage.State != ExecutionCanceled && stage.State != ExecutionStale {
			if latestTarget == gate.TargetID {
				stage.State = ExecutionBlocked
				stage.PublicError = "completion_authorization_stale"
			} else {
				stage.State = ExecutionStale
				stage.PublicError = "candidate_superseded"
			}
			stage.FinishedAt = &now
			patch.ReplaceStageRuns = []StageRun{stage}
		}
	}
	if latestTarget == "" || latestTarget == gate.TargetID {
		phase, state := PhaseImplementation, ExecutionBlocked
		patch.Phase, patch.ExecutionState = &phase, &state
	}
	for _, publication := range aggregate.Publications {
		if publication.Kind != PublicationBranchPush || publication.TargetID != gate.TargetID {
			continue
		}
		switch publication.State {
		case ExecutionQueued, ExecutionWaitingGate, ExecutionWaitingUser:
			publication.State = ExecutionStale
			publication.PublicErrorCode = "completion_authorization_stale"
			publication.UpdatedAt = now
			patch.ReplacePublications = append(patch.ReplacePublications, publication)
		}
	}
	patch.Activity = []Activity{{
		Kind: "implementation.completion_authorization_stale", Actor: "system", EntityID: gate.ID,
		Summary: "Completion authorization became stale; review current guidance and findings, then rerun implementation", CreatedAt: now,
	}}
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID, Patch: patch,
	})
	if err != nil {
		return result.Aggregate, err
	}
	return result.Aggregate, fmt.Errorf("%w: completion authorization is stale; review the current aggregate and rerun implementation", ErrConflict)
}

func latestImplementationRepairID(aggregate Aggregate) string {
	charter, ok := aggregate.ActiveCharter()
	if !ok {
		return ""
	}
	for index := len(aggregate.RepairAttempts) - 1; index >= 0; index-- {
		attempt := aggregate.RepairAttempts[index]
		stage, found := findStageRun(aggregate.StageRuns, attempt.StageRunID)
		if found && stage.Stage == "implementation" && stage.CharterID == charter.ID &&
			stage.HeadSHA == charter.HeadSHA && stage.State != ExecutionCanceled && stage.State != ExecutionStale {
			return attempt.ID
		}
	}
	return ""
}

type PromoteCorrectionRequest struct {
	WorkspaceID     string
	CorrectionID    string
	ExpectedVersion int64
	RequestID       string
}

func (service *Service) PromoteCorrection(ctx context.Context, request PromoteCorrectionRequest) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		!validOpaqueID(request.CorrectionID, "pco_") {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	correction, ok := findCorrection(aggregate.Corrections, request.CorrectionID)
	if !ok || correction.Promoted || aggregate.Workspace.Version != request.ExpectedVersion {
		return aggregate, ErrConflict
	}
	gate, err := service.startGate(ctx, aggregate, "pr.correction.promote", map[string]any{
		"correction":    correction,
		"repository_id": aggregate.Workspace.RepositoryID,
	})
	if err != nil {
		return Aggregate{}, err
	}
	gate.TargetID = correction.ID
	patch := AggregatePatch{AppendGates: []GateRun{gate}}
	if gateCompletedWith(gate, "promote") {
		promotion, promotionErr := service.correctionPromotionPatch(aggregate, correction)
		if promotionErr != nil {
			return aggregate, promotionErr
		}
		patch.ReplaceCorrections = promotion.ReplaceCorrections
		patch.AppendLessons = promotion.AppendLessons
	} else {
		state := ExecutionWaitingGate
		patch.ExecutionState = &state
	}
	patch.Activity = []Activity{{Kind: "correction.promotion_requested", Actor: "user", EntityID: correction.ID, Summary: "Repository lesson promotion requested", CreatedAt: service.now().UTC()}}
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID, Patch: patch,
	})
	if err != nil {
		return result.Aggregate, err
	}
	return result.Aggregate, nil
}

func (service *Service) gateActionPatch(aggregate Aggregate, gate GateRun) (AggregatePatch, error) {
	patch := AggregatePatch{}
	if gate.State != ExecutionSucceeded {
		state := ExecutionWaitingGate
		patch.ExecutionState = &state
		return patch, nil
	}
	action := gateAction(gate)
	if action == "" {
		return AggregatePatch{}, ErrInvalid
	}
	if gate.DecisionPoint == "pr.implementation.hard-scope" {
		if !gateHasHardCandidateScope(aggregate, gate) {
			return AggregatePatch{}, ErrInvalid
		}
		return service.implementationScopeResolutionActionPatch(aggregate, gate, true)
	}
	if gate.DecisionPoint == "pr.implementation.scope" && action != "approve" {
		return service.implementationScopeResolutionActionPatch(aggregate, gate, false)
	}
	if gate.DecisionPoint == "pr.finding.classify" {
		finding, index := findFinding(aggregate.Findings, gate.TargetID)
		if index < 0 || finding.Disposition != FindingOpen {
			return AggregatePatch{}, ErrConflict
		}
		switch action {
		case "keep-in-pr":
			if !scopeCanUseClassificationGate(finding.Scope) {
				return AggregatePatch{}, ErrConflict
			}
			finding.Disposition = FindingInScope
		case "defer-follow-up":
			finding.Disposition = FindingDeferred
		case "dismiss":
			finding.Disposition = FindingDismissed
		case "revise-charter":
			finding.Disposition = FindingOpen
		default:
			return AggregatePatch{}, ErrInvalid
		}
		finding.Version++
		finding.UpdatedAt = service.now().UTC()
		patch.UpsertFindings = []Finding{finding}
		phase, state := PhaseTriage, ExecutionWaitingUser
		if action == "revise-charter" {
			phase = PhaseCharter
			patch.Activity = append(patch.Activity, Activity{
				Kind: "finding.needs_charter_revision", Actor: "gate", EntityID: finding.ID,
				Summary: "Finding requires a charter revision", CreatedAt: service.now().UTC(),
			})
		}
		if anotherUnresolvedGate(aggregate.Gates, gate) {
			state = ExecutionWaitingGate
			if unresolvedDecisionPoint(aggregate.Gates, gate, "pr.review.complete") {
				phase = PhaseReview
			}
		}
		patch.Phase, patch.ExecutionState = &phase, &state
		return patch, nil
	}
	if action != gateProgressAction(gate.DecisionPoint) {
		if gate.DecisionPoint == "pr.deferred.publish" {
			group, ok := findDeferredGroup(aggregate.DeferredGroups, gate.TargetID)
			if ok && group.PublicationID != "" {
				if publication, found := findPublication(aggregate.Publications, group.PublicationID); found {
					publication.State = ExecutionBlocked
					publication.PublicErrorCode = "publication_gate_" + action
					publication.UpdatedAt = service.now().UTC()
					patch.ReplacePublications = []Publication{publication}
				}
				group.PublicationID = ""
				group.PublicationSuppressed = true
				group.SuppressionReason = "publication_gate_" + action
				group.Version++
				group.UpdatedAt = service.now().UTC()
				patch.UpsertDeferred = []DeferredGroup{group}
			}
			return patch, nil
		}
		if gate.DecisionPoint == "pr.publication.reconcile" {
			publication, ok := findPublication(aggregate.Publications, gate.TargetID)
			if !ok || publication.State != ExecutionUnknown {
				return AggregatePatch{}, ErrConflict
			}
			state := ExecutionWaitingUser
			if action == "assume-failed" {
				publication.State = ExecutionFailed
				publication.PublicErrorCode = "publication_assumed_failed_by_user"
				publication.UpdatedAt = service.now().UTC()
				patch.ReplacePublications = []Publication{publication}
				if publication.Kind == PublicationGitHubIssue {
					if group, found := findDeferredGroup(aggregate.DeferredGroups, publication.TargetID); found && group.PublicationID == publication.ID {
						group.PublicationID = ""
						group.Version++
						group.UpdatedAt = publication.UpdatedAt
						patch.UpsertDeferred = []DeferredGroup{group}
					}
				}
			}
			patch.ExecutionState = &state
			return patch, nil
		}
		state := ExecutionBlocked
		if action == "revise" || action == "revise-charter" {
			state = ExecutionWaitingUser
		}
		patch.ExecutionState = &state
		if gate.DecisionPoint == "pr.charter.confirm" && action == "revise" {
			phase := PhaseCharter
			patch.Phase = &phase
		}
		if gate.DecisionPoint == "pr.review.publish" || gate.DecisionPoint == "pr.implementation.publish" {
			if publication, ok := findPublication(aggregate.Publications, gate.TargetID); ok {
				publication.State = ExecutionBlocked
				publication.PublicErrorCode = "publication_gate_" + action
				publication.UpdatedAt = service.now().UTC()
				patch.ReplacePublications = []Publication{publication}
			}
		}
		return patch, nil
	}
	switch gate.DecisionPoint {
	case "pr.charter.confirm", "pr.charter.reconfirm":
		charter, decisionPoint, ready := charterConfirmationReady(aggregate, gate.TargetID)
		if !ready || gate.DecisionPoint != decisionPoint {
			return AggregatePatch{}, ErrConflict
		}
		now := service.now().UTC()
		charter.Confirmed, charter.ConfirmedAt = true, &now
		phase, state, active := PhaseReview, ExecutionQueued, charter.ID
		patch.ReplaceCharters = []Charter{charter}
		patch.Phase, patch.ExecutionState, patch.ActiveCharterID = &phase, &state, &active
	case "pr.correction.promote":
		correction, ok := findCorrection(aggregate.Corrections, gate.TargetID)
		if !ok {
			return AggregatePatch{}, ErrConflict
		}
		return service.correctionPromotionPatch(aggregate, correction)
	case "pr.deferred.publish":
		if service.deferredIssueMode == DeferredIssuesOff {
			return AggregatePatch{}, ErrConflict
		}
		group, ok := findDeferredGroup(aggregate.DeferredGroups, gate.TargetID)
		if !ok || group.PublicationID == "" || group.ExistingIssueURL != "" {
			return AggregatePatch{}, ErrConflict
		}
		publication, found := findPublication(aggregate.Publications, group.PublicationID)
		if !found || publication.Kind != PublicationGitHubIssue ||
			publication.State != ExecutionWaitingGate {
			return AggregatePatch{}, ErrConflict
		}
		publication.State = ExecutionQueued
		publication.PublicErrorCode = ""
		publication.UpdatedAt = service.now().UTC()
		patch.ReplacePublications = []Publication{publication}
		group.PublicationSuppressed, group.SuppressionReason = false, ""
		patch.UpsertDeferred = []DeferredGroup{group}
	case "pr.review.complete":
		stage, ok := findStageRun(aggregate.StageRuns, gate.TargetID)
		if !ok || stage.Stage != "review" || stage.HeadSHA != aggregate.ProviderSnapshot.HeadSHA {
			return AggregatePatch{}, ErrConflict
		}
		now := service.now().UTC()
		stage.State, stage.FinishedAt = ExecutionSucceeded, &now
		phase, state := PhaseTriage, ExecutionWaitingUser
		if classificationNeedsCharterRevision(aggregate.Gates) {
			phase = PhaseCharter
		}
		if anotherUnresolvedGate(aggregate.Gates, gate) {
			state = ExecutionWaitingGate
		}
		patch.ReplaceStageRuns = []StageRun{stage}
		patch.Phase, patch.ExecutionState = &phase, &state
	case "pr.implementation.scope", "pr.implementation.complete":
		if !implementationGatesPassed(aggregate.Gates, gate) {
			state := ExecutionWaitingGate
			patch.ExecutionState = &state
			return patch, nil
		}
		attempt, ok := findRepairAttempt(aggregate.RepairAttempts, gate.TargetID)
		if !ok {
			return AggregatePatch{}, ErrConflict
		}
		stage, ok := findStageRun(aggregate.StageRuns, attempt.StageRunID)
		if !ok || stage.Stage != "implementation" || stage.HeadSHA != aggregate.ProviderSnapshot.HeadSHA {
			return AggregatePatch{}, ErrConflict
		}
		now := service.now().UTC()
		stage.State, stage.Summary, stage.FinishedAt = ExecutionSucceeded, attempt.ResultSummary, &now
		patch.ReplaceStageRuns = []StageRun{stage}
		for _, id := range attempt.FindingIDs {
			finding, index := findFinding(aggregate.Findings, id)
			if index < 0 || finding.Disposition != FindingInScope {
				continue
			}
			finding.Disposition = FindingFixed
			finding = setFindingNudgeReward(finding, NudgeReward(RewardConfirmedFixed), "green_validation")
			finding.Version++
			finding.UpdatedAt = now
			patch.UpsertFindings = append(patch.UpsertFindings, finding)
		}
		phase, state := PhasePublication, ExecutionWaitingUser
		patch.Phase, patch.ExecutionState = &phase, &state
	case "pr.review.publish", "pr.implementation.publish":
		publication, ok := findPublication(aggregate.Publications, gate.TargetID)
		if !ok || publication.State != ExecutionWaitingGate || publication.ExpectedHeadSHA != aggregate.ProviderSnapshot.HeadSHA {
			return AggregatePatch{}, ErrConflict
		}
		publication.State, publication.PublicErrorCode, publication.UpdatedAt = ExecutionQueued, "", service.now().UTC()
		patch.ReplacePublications = []Publication{publication}
		state := ExecutionQueued
		patch.ExecutionState = &state
	case "pr.publication.reconcile":
		publication, ok := findPublication(aggregate.Publications, gate.TargetID)
		if !ok || (publication.State != ExecutionUnknown && publication.State != ExecutionRunning) {
			return AggregatePatch{}, ErrConflict
		}
		state := ExecutionWaitingUser
		patch.ExecutionState = &state
	default:
		state := ExecutionQueued
		patch.ExecutionState = &state
	}
	return patch, nil
}

func anotherUnresolvedGate(values []GateRun, answered GateRun) bool {
	for _, value := range values {
		if value.ID == answered.ID {
			value = answered
		}
		if value.ID != answered.ID && (value.State == ExecutionWaitingGate || value.State == ExecutionWaitingUser) {
			return true
		}
	}
	return false
}

func unresolvedDecisionPoint(values []GateRun, answered GateRun, decisionPoint string) bool {
	for _, value := range values {
		if value.ID == answered.ID {
			value = answered
		}
		if value.ID != answered.ID && value.DecisionPoint == decisionPoint &&
			(value.State == ExecutionWaitingGate || value.State == ExecutionWaitingUser) {
			return true
		}
	}
	return false
}

func classificationNeedsCharterRevision(values []GateRun) bool {
	for _, gate := range values {
		if gate.DecisionPoint == "pr.finding.classify" && gateCompletedWith(gate, "revise-charter") {
			return true
		}
	}
	return false
}

func implementationGatesPassed(values []GateRun, answered GateRun) bool {
	required := map[string]bool{"pr.implementation.complete": false}
	for _, value := range values {
		if value.TargetID == answered.TargetID && value.DecisionPoint == "pr.implementation.scope" {
			required["pr.implementation.scope"] = false
		}
	}
	for _, value := range values {
		if value.ID == answered.ID {
			value = answered
		}
		if value.TargetID != answered.TargetID {
			continue
		}
		if _, tracked := required[value.DecisionPoint]; tracked {
			required[value.DecisionPoint] = gateAllowsProgress(value)
		}
	}
	for _, passed := range required {
		if !passed {
			return false
		}
	}
	return true
}

func gateHasHardCandidateScope(aggregate Aggregate, gate GateRun) bool {
	if gate.Evidence.HardScope {
		return true
	}
	if gate.Evidence.Scope != nil && HardCandidateScopeBlocker(*gate.Evidence.Scope) {
		return true
	}
	if attempt, ok := findRepairAttempt(aggregate.RepairAttempts, gate.TargetID); ok && HardCandidateScopeBlocker(attempt.Scope) {
		return true
	}
	wanted := make(map[string]struct{}, len(gate.Evidence.FindingIDs))
	for _, id := range gate.Evidence.FindingIDs {
		wanted[id] = struct{}{}
	}
	for _, finding := range aggregate.Findings {
		if _, ok := wanted[finding.ID]; ok && HardCandidateScopeBlocker(finding.Scope) {
			return true
		}
	}
	return false
}

func (service *Service) implementationScopeResolutionActionPatch(
	aggregate Aggregate,
	gate GateRun,
	hard bool,
) (AggregatePatch, error) {
	action := gateAction(gate)
	if hard && action == "approve" {
		return AggregatePatch{}, fmt.Errorf("%w: hard candidate scope cannot pass an authorization gate", ErrInvalid)
	}
	now := service.now().UTC()
	patch := AggregatePatch{}
	for _, sibling := range aggregate.Gates {
		if sibling.ID == gate.ID || sibling.TargetID != gate.TargetID ||
			(sibling.State != ExecutionWaitingGate && sibling.State != ExecutionWaitingUser) {
			continue
		}
		sibling.State, sibling.FinishedAt = ExecutionCanceled, &now
		patch.ReplaceGates = append(patch.ReplaceGates, sibling)
	}
	if attempt, ok := findRepairAttempt(aggregate.RepairAttempts, gate.TargetID); ok {
		if stage, found := findStageRun(aggregate.StageRuns, attempt.StageRunID); found &&
			(stage.State == ExecutionRunning || stage.State == ExecutionWaitingGate || stage.State == ExecutionWaitingUser) {
			stage.State, stage.FinishedAt = ExecutionBlocked, &now
			stage.PublicError = "candidate_scope_requires_resolution"
			patch.ReplaceStageRuns = append(patch.ReplaceStageRuns, stage)
		}
	}
	switch action {
	case "defer-follow-up":
		findingIDs := gate.Evidence.ScopeResolutionIDs
		wanted := make(map[string]struct{}, len(findingIDs))
		for _, id := range findingIDs {
			wanted[id] = struct{}{}
		}
		for _, finding := range aggregate.Findings {
			if _, ok := wanted[finding.ID]; !ok {
				continue
			}
			removal := finding
			removal.Disposition = FindingInScope
			removal.Version++
			removal.UpdatedAt = now
			patch.UpsertFindings = append(patch.UpsertFindings, removal)

			followUp := finding
			followUp.ID = stableID("pfn_", aggregate.Workspace.ID, gate.ID, finding.ID, "deferred-follow-up")
			followUp.Fingerprint = stableID("sha256:", finding.Fingerprint, gate.ID, "deferred-follow-up")
			followUp.NudgeRoundID = ""
			followUp.Scope.Presence = WorkFollowUp
			for index := range followUp.Scope.ChangeEvidence {
				followUp.Scope.ChangeEvidence[index].Presence = WorkFollowUp
			}
			followUp.Disposition = FindingDeferred
			followUp.NudgeReward, followUp.RewardSource = nil, ""
			followUp.Version, followUp.CreatedAt, followUp.UpdatedAt = 1, now, now
			patch.UpsertFindings = append(patch.UpsertFindings, followUp)
		}
		if len(patch.UpsertFindings) == 0 {
			return AggregatePatch{}, ErrConflict
		}
		phase, state := PhaseImplementation, ExecutionQueued
		patch.Phase, patch.ExecutionState = &phase, &state
		patch.Activity = append(patch.Activity, Activity{
			Kind: "implementation.scope_removal_requested", Actor: "user", EntityID: gate.TargetID,
			Summary: "Candidate scope drift must be removed; follow-up work was deferred", CreatedAt: now,
		})
	case "revise-charter":
		phase, state := PhaseCharter, ExecutionWaitingUser
		patch.Phase, patch.ExecutionState = &phase, &state
	case "stop":
		state := ExecutionBlocked
		patch.ExecutionState = &state
	default:
		return AggregatePatch{}, ErrInvalid
	}
	return patch, nil
}

func findStageRun(values []StageRun, id string) (StageRun, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return StageRun{}, false
}

func findRepairAttempt(values []RepairAttempt, id string) (RepairAttempt, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return RepairAttempt{}, false
}

func (service *Service) correctionPromotionPatch(aggregate Aggregate, correction Correction) (AggregatePatch, error) {
	if correction.Promoted || correction.Correction == "" {
		return AggregatePatch{}, ErrConflict
	}
	charter, _ := aggregate.ActiveCharter()
	correction.Promoted = true
	lesson := RepositoryLesson{
		ID:           stableID("prl_", aggregate.Workspace.RepositoryID, correction.ID),
		RepositoryID: aggregate.Workspace.RepositoryID, SourcePR: aggregate.Workspace.ID,
		CorrectionID: correction.ID, Kind: correction.Kind, Applicability: correction.Applicability,
		PRType: charter.Type, Text: correction.Correction, Active: true, CreatedAt: service.now().UTC(),
	}
	return AggregatePatch{ReplaceCorrections: []Correction{correction}, AppendLessons: []RepositoryLesson{lesson}}, nil
}

func answerFallbackGate(gate GateRun, fieldValues map[string]any, now time.Time) GateRun {
	gate.State, gate.FinishedAt = ExecutionSucceeded, &now
	for index := range gate.Turns {
		if gate.Turns[index].Status == "waiting" {
			gate.Turns[index].Status = "answered"
			gate.Turns[index].FieldValues = fieldValues
			break
		}
	}
	return gate
}

func findGate(values []GateRun, id string) (GateRun, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return GateRun{}, false
}

func findCorrection(values []Correction, id string) (Correction, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return Correction{}, false
}

type RefreshProviderRequest struct {
	WorkspaceID     string
	ExpectedVersion int64
	RequestID       string
}

func (service *Service) RefreshProvider(ctx context.Context, request RefreshProviderRequest) (Aggregate, error) {
	if service == nil || service.provider == nil || !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	provider, err := service.provider.ResolvePullRequest(ctx, ResolveRequest{
		ProviderOrigin: aggregate.Workspace.ProviderOrigin,
		Repository:     aggregate.Workspace.Repository, PullNumber: aggregate.Workspace.PullNumber,
	})
	if err != nil {
		return Aggregate{}, err
	}
	if err := validateProviderSnapshot(provider); err != nil || provider.RepositoryID != aggregate.Workspace.RepositoryID ||
		provider.PullRequestID != aggregate.Workspace.PullRequestID || provider.ProviderOrigin != aggregate.Workspace.ProviderOrigin {
		return aggregate, errors.New("provider refresh identity mismatch")
	}
	patch := AggregatePatch{Provider: &provider}
	if provider.HeadSHA != aggregate.ProviderSnapshot.HeadSHA || provider.BaseSHA != aggregate.ProviderSnapshot.BaseSHA {
		for _, stage := range aggregate.StageRuns {
			if stage.State == ExecutionQueued || stage.State == ExecutionRunning || stage.State == ExecutionWaitingGate || stage.State == ExecutionWaitingUser || stage.State == ExecutionSucceeded {
				stage.State = ExecutionStale
				stage.PublicError = "provider_revision_changed"
				patch.ReplaceStageRuns = append(patch.ReplaceStageRuns, stage)
			}
		}
		for _, gate := range aggregate.Gates {
			if gate.State == ExecutionWaitingGate || gate.State == ExecutionWaitingUser || gate.State == ExecutionRunning {
				gate.State = ExecutionStale
				patch.ReplaceGates = append(patch.ReplaceGates, gate)
			}
		}
		for _, publication := range aggregate.Publications {
			if publication.Kind != PublicationGitHubReview && publication.Kind != PublicationBranchPush {
				continue
			}
			switch publication.State {
			case ExecutionQueued, ExecutionWaitingGate, ExecutionWaitingUser:
				publication.State = ExecutionStale
				publication.PublicErrorCode = "provider_revision_changed"
				publication.UpdatedAt = service.now().UTC()
				patch.ReplacePublications = append(patch.ReplacePublications, publication)
			case ExecutionRunning:
				publication.State = ExecutionUnknown
				publication.PublicErrorCode = "provider_outcome_unknown"
				publication.UpdatedAt = service.now().UTC()
				patch.ReplacePublications = append(patch.ReplacePublications, publication)
			}
		}
		phase, state := PhaseCharter, ExecutionWaitingUser
		patch.Phase, patch.ExecutionState = &phase, &state
	}
	patch.Activity = []Activity{{Kind: "provider.refreshed", Actor: "system", Summary: "Provider snapshot refreshed", CreatedAt: service.now().UTC()}}
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID, Patch: patch,
	})
	if err != nil {
		return result.Aggregate, err
	}
	return result.Aggregate, nil
}

type AddMessageRequest struct {
	WorkspaceID      string
	ExpectedVersion  int64
	RequestID        string
	Stage            string
	Content          string
	MarkAsCorrection bool
	Applicability    CorrectionApplicability
}

func (service *Service) AddMessage(ctx context.Context, request AddMessageRequest) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		!validBoundedText(strings.TrimSpace(request.Content), 64<<10, false) ||
		!validBoundedText(strings.TrimSpace(request.Stage), 128, true) {
		return Aggregate{}, ErrInvalid
	}
	if request.MarkAsCorrection {
		if request.Applicability == "" {
			request.Applicability = CorrectionReviewAndImpl
		}
		switch request.Applicability {
		case CorrectionReviewOnly, CorrectionImplementationOnly, CorrectionReviewAndImpl:
		default:
			return Aggregate{}, ErrInvalid
		}
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	if aggregate.Workspace.Version != request.ExpectedVersion {
		return aggregate, ErrConflict
	}
	now := service.now().UTC()
	message := Message{
		ID: stableID("pms_", request.WorkspaceID, request.RequestID), Role: "user",
		Stage: strings.TrimSpace(request.Stage), Content: strings.TrimSpace(request.Content),
		CharterID: aggregate.Workspace.ActiveCharterID, HeadSHA: aggregate.ProviderSnapshot.HeadSHA,
		CreatedAt: now,
	}
	patch := AggregatePatch{
		AppendMessages: []Message{message},
		Activity:       []Activity{{Kind: "message.added", Actor: "user", EntityID: message.ID, Summary: "Workspace message added", CreatedAt: now}},
	}
	if request.MarkAsCorrection {
		correction := Correction{
			ID:            stableID("pco_", request.WorkspaceID, request.RequestID, "message-correction"),
			Kind:          CorrectionFactual,
			Applicability: request.Applicability,
			TargetType:    "workspace",
			TargetID:      request.WorkspaceID,
			OriginalClaim: "AI workspace context",
			Correction:    message.Content,
			CharterID:     message.CharterID,
			HeadSHA:       message.HeadSHA,
			CreatedAt:     now,
		}
		patch.AppendCorrections = []Correction{correction}
		patch.Activity = append(patch.Activity, Activity{
			Kind: "correction.added", Actor: "user", EntityID: correction.ID,
			Summary: "Workspace message recorded as AI correction", CreatedAt: now,
		})
	}
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID,
		Patch:     patch,
	})
	if err != nil {
		return result.Aggregate, err
	}
	return result.Aggregate, nil
}
