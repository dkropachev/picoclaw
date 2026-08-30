package prworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

// EventingStore adapts the durable eventing aggregate ledger to the
// PR-workspace domain store. The adapter is also the privacy boundary for gate
// runtime state and implementation publication fences: both are persisted,
// but neither is added to the browser-facing domain JSON.
type EventingStore struct {
	store eventing.PRWorkspaceStore
}

func NewEventingStore(store eventing.PRWorkspaceStore) *EventingStore {
	return &EventingStore{store: store}
}

var _ Store = (*EventingStore)(nil)

func (store *EventingStore) Create(ctx context.Context, input CreateInput) (MutationResult, error) {
	if store == nil || store.store == nil || !validRequestID(input.RequestID) ||
		input.Workspace.Version != 1 || input.Workspace.ID == "" ||
		input.Workspace.ProviderOrigin != input.Provider.ProviderOrigin ||
		input.Workspace.RepositoryID != input.Provider.RepositoryID ||
		input.Workspace.PullRequestID != input.Provider.PullRequestID ||
		input.Workspace.Intent != input.Provider.Intent ||
		input.Workspace.SourceKind != input.Provider.SourceKind ||
		input.Workspace.SourceID != input.Provider.SourceID {
		return MutationResult{}, ErrInvalid
	}
	_, beforeErr := store.store.GetPRWorkspace(ctx, input.Workspace.ID)
	existed := beforeErr == nil
	if beforeErr != nil && !errors.Is(beforeErr, eventing.ErrNotFound) {
		return MutationResult{}, mapEventingStoreError(beforeErr)
	}
	aggregate, created, err := store.store.CreatePRWorkspace(ctx, eventing.PRWorkspaceCreate{
		RequestID: input.RequestID, WorkspaceID: input.Workspace.ID,
		Provider:       toEventingProvider(input.Provider),
		Phase:          eventing.PRWorkspacePhase(input.Workspace.Phase),
		ExecutionState: eventing.PRExecutionState(input.Workspace.ExecutionState),
	})
	if err != nil {
		return MutationResult{}, mapEventingStoreError(err)
	}
	converted, convertErr := fromEventingAggregate(aggregate)
	if convertErr != nil {
		return MutationResult{}, convertErr
	}
	syncDevelopmentNotifications(ctx, store, converted)
	return MutationResult{Aggregate: converted, Replayed: existed || !created}, nil
}

func (store *EventingStore) Get(ctx context.Context, workspaceID string) (Aggregate, error) {
	if store == nil || store.store == nil {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := store.store.GetPRWorkspace(ctx, workspaceID)
	if err != nil {
		return Aggregate{}, mapEventingStoreError(err)
	}
	return fromEventingAggregate(aggregate)
}

func (store *EventingStore) List(ctx context.Context, filter ListFilter) (Page, error) {
	if store == nil || store.store == nil || filter.Limit < 0 || filter.Limit > 100 ||
		(filter.AfterUpdated.IsZero() != (filter.AfterID == "")) {
		return Page{}, ErrInvalid
	}
	eventFilter := eventing.PRWorkspaceFilter{
		RepositoryID: filter.RepositoryID, Repository: filter.Repository,
		Phase:          eventing.PRWorkspacePhase(filter.Phase),
		ExecutionState: eventing.PRExecutionState(filter.State),
		OwnedOnly:      filter.Owned, NeedsAction: filter.NeedsAction, Limit: filter.Limit,
	}
	if !filter.AfterUpdated.IsZero() {
		eventFilter.After = &eventing.PRWorkspaceCursor{UpdatedAt: filter.AfterUpdated, ID: filter.AfterID}
	}
	result, err := store.store.ListPRWorkspaces(ctx, eventFilter)
	if err != nil {
		return Page{}, mapEventingStoreError(err)
	}
	page := Page{Workspaces: make([]Workspace, 0, len(result.Workspaces))}
	for _, workspace := range result.Workspaces {
		page.Workspaces = append(page.Workspaces, fromEventingWorkspace(workspace))
	}
	if result.Next != nil {
		page.Next = &WorkspaceCursor{UpdatedAt: result.Next.UpdatedAt, ID: result.Next.ID}
	}
	return page, nil
}

func (store *EventingStore) Mutate(ctx context.Context, mutation Mutation) (MutationResult, error) {
	if store == nil || store.store == nil || mutation.WorkspaceID == "" ||
		mutation.ExpectedVersion < 1 || !validRequestID(mutation.RequestID) {
		return MutationResult{}, ErrInvalid
	}
	eventPatch, err := toEventingPatch(
		mutation.WorkspaceID,
		mutation.ExpectedVersion,
		mutation.RequestID,
		mutation.Patch,
	)
	if err != nil {
		return MutationResult{}, fmt.Errorf("%w: encode protected finding provenance: %v", ErrInvalid, err)
	}
	result, err := store.store.ApplyPRWorkspacePatch(ctx, eventing.PRWorkspacePatchMutation{
		WorkspaceID: mutation.WorkspaceID, ExpectedVersion: mutation.ExpectedVersion,
		RequestID: mutation.RequestID,
		Patch:     eventPatch,
	})
	if err != nil {
		mapped := mapEventingStoreError(err)
		if errors.Is(mapped, ErrConflict) {
			current, getErr := store.Get(ctx, mutation.WorkspaceID)
			if getErr == nil {
				return MutationResult{Aggregate: current}, mapped
			}
		}
		return MutationResult{}, mapped
	}
	converted, convertErr := fromEventingAggregate(result.Aggregate)
	if convertErr != nil {
		return MutationResult{}, convertErr
	}
	syncDevelopmentNotifications(ctx, store, converted)
	return MutationResult{Aggregate: converted, Replayed: result.Replayed}, nil
}

func mapEventingStoreError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, eventing.ErrInvalidPRWorkspace):
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	case errors.Is(err, eventing.ErrNotFound):
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case errors.Is(err, eventing.ErrPRWorkspaceConflict):
		if strings.Contains(err.Error(), "request ID reused") {
			return fmt.Errorf("%w: %v", ErrRequestConflict, err)
		}
		return fmt.Errorf("%w: %v", ErrConflict, err)
	default:
		return err
	}
}

func toEventingPatch(
	workspaceID string,
	version int64,
	requestID string,
	patch AggregatePatch,
) (eventing.PRWorkspacePatch, error) {
	result := eventing.PRWorkspacePatch{}
	if patch.Phase != nil {
		value := eventing.PRWorkspacePhase(*patch.Phase)
		result.Phase = &value
	}
	if patch.ExecutionState != nil {
		value := eventing.PRExecutionState(*patch.ExecutionState)
		result.ExecutionState = &value
	}
	if patch.ActiveCharterID != nil {
		value := *patch.ActiveCharterID
		result.ActiveCharterID = &value
	}
	if patch.Provider != nil {
		value := toEventingProvider(*patch.Provider)
		result.ProviderSnapshot = &value
	}
	for _, value := range patch.AppendCharters {
		result.AppendCharters = append(result.AppendCharters, toEventingCharter(value))
	}
	for _, value := range patch.ReplaceCharters {
		result.ReplaceCharters = append(result.ReplaceCharters, toEventingCharter(value))
	}
	for _, value := range patch.AppendStageRuns {
		result.AppendStageRuns = append(result.AppendStageRuns, toEventingStageRun(value, version))
	}
	for _, value := range patch.ReplaceStageRuns {
		result.ReplaceStageRuns = append(result.ReplaceStageRuns, toEventingStageRun(value, version))
	}
	for _, value := range patch.UpsertFindings {
		finding, err := toEventingFinding(value)
		if err != nil {
			return eventing.PRWorkspacePatch{}, err
		}
		result.UpsertFindings = append(result.UpsertFindings, finding)
		if value.Disposition != FindingDeferred {
			result.UpsertDeferredItems = append(result.UpsertDeferredItems, eventing.PRDeferredGroupItem{
				PRWorkspaceRecord: eventing.PRWorkspaceRecord{ID: stableID("pdi_", workspaceID, value.ID)},
				FindingID:         value.ID, OrdinalInGroup: -1, Removed: true,
			})
		}
	}
	for _, value := range patch.AppendMessages {
		result.AppendMessages = append(result.AppendMessages, toEventingMessage(value))
	}
	for _, value := range patch.AppendCorrections {
		result.AppendCorrections = append(result.AppendCorrections, toEventingCorrection(value))
	}
	for _, value := range patch.ReplaceCorrections {
		result.ReplaceCorrections = append(result.ReplaceCorrections, toEventingCorrection(value))
	}
	for _, value := range patch.AppendLessons {
		result.AppendLessons = append(result.AppendLessons, toEventingLesson(value))
	}
	for _, value := range patch.ReplaceLessons {
		result.ReplaceLessons = append(result.ReplaceLessons, toEventingLesson(value))
	}
	for _, value := range patch.AppendNudgeRounds {
		result.AppendNudgeRounds = append(result.AppendNudgeRounds, toEventingNudge(value))
	}
	for _, value := range patch.ReplaceNudgeRounds {
		result.ReplaceNudgeRounds = append(result.ReplaceNudgeRounds, toEventingNudge(value))
	}
	for _, value := range patch.UpsertDeferred {
		result.UpsertDeferredGroups = append(result.UpsertDeferredGroups, toEventingDeferredGroup(value))
		for ordinal, findingID := range value.FindingIDs {
			result.UpsertDeferredItems = append(result.UpsertDeferredItems, eventing.PRDeferredGroupItem{
				PRWorkspaceRecord: eventing.PRWorkspaceRecord{ID: stableID("pdi_", workspaceID, findingID)},
				GroupID:           value.ID, FindingID: findingID, OrdinalInGroup: ordinal,
			})
		}
	}
	for _, value := range patch.AppendRepairs {
		result.AppendRepairAttempts = append(result.AppendRepairAttempts, toEventingRepair(value))
	}
	for _, value := range patch.ReplaceRepairs {
		result.ReplaceRepairAttempts = append(result.ReplaceRepairAttempts, toEventingRepair(value))
	}
	for _, value := range patch.AppendValidations {
		result.AppendValidationRuns = append(result.AppendValidationRuns, toEventingValidation(value))
	}
	for _, value := range patch.ReplaceValidations {
		result.ReplaceValidationRuns = append(result.ReplaceValidationRuns, toEventingValidation(value))
	}
	for _, value := range patch.AppendGates {
		result.AppendGateRuns = append(result.AppendGateRuns, toEventingGate(value))
	}
	for _, value := range patch.ReplaceGates {
		result.ReplaceGateRuns = append(result.ReplaceGateRuns, toEventingGate(value))
	}
	for _, value := range patch.AppendPublications {
		result.AppendPublications = append(result.AppendPublications, toEventingPublication(value))
	}
	for _, value := range patch.ReplacePublications {
		result.ReplacePublications = append(result.ReplacePublications, toEventingPublication(value))
	}
	for index, value := range patch.Activity {
		result.AppendActivity = append(result.AppendActivity, eventing.PRActivity{
			PRWorkspaceRecord: eventing.PRWorkspaceRecord{
				ID: stableID("pac_", workspaceID, requestID, fmt.Sprint(index)),
			},
			Kind:     value.Kind,
			Actor:    value.Actor,
			Summary:  value.Summary,
			EntityID: value.EntityID,
			Metadata: value.Metadata,
		})
	}
	return result, nil
}

func toEventingProvider(value ProviderSnapshot) eventing.PRProviderSnapshot {
	return eventing.PRProviderSnapshot{
		Intent:     eventing.DevelopmentIntent(value.Intent),
		SourceKind: eventing.DevelopmentSourceKind(value.SourceKind),
		SourceID:   value.SourceID, SourceNumber: value.SourceNumber, SourceURL: value.SourceURL,
		Provider: value.Provider, ProviderOrigin: value.ProviderOrigin,
		RepositoryID: value.RepositoryID, Repository: value.Repository,
		PullRequestID: value.PullRequestID, PullNumber: value.PullNumber,
		Title: value.Title, Body: value.Body, AuthorID: value.AuthorID,
		AuthorLogin: value.AuthorLogin, AuthenticatedUserID: value.AuthenticatedUserID,
		BaseRef: value.BaseRef, BaseSHA: value.BaseSHA,
		HeadRepositoryID: value.HeadRepositoryID, HeadRepository: value.HeadRepository,
		HeadRef: value.HeadRef, HeadSHA: value.HeadSHA, State: value.State,
		Owned: value.Owned, HeadWritable: value.HeadWritable,
		CanReview: value.CanReview, CanCreateIssue: value.CanCreateIssue,
		CanCreatePullRequest: value.CanCreatePullRequest,
		ProviderRevision:     value.ProviderRevision, ObservedAt: value.ObservedAt,
	}
}

func toEventingCharter(value Charter) eventing.PRCharterRevision {
	status := eventing.PRRecordDraft
	if value.Confirmed {
		status = eventing.PRRecordConfirmed
	}
	return eventing.PRCharterRevision{
		PRWorkspaceRecord:  toEventingRecord(value.ID, value.CreatedAt, value.CreatedAt),
		Status:             status,
		Revision:           value.Revision,
		Type:               eventing.PRType(value.Type),
		Goal:               value.Goal,
		AcceptanceCriteria: append([]string(nil), value.AcceptanceCriteria...),
		IncludedAreas:      append([]string(nil), value.IncludedAreas...),
		Exclusions: append(
			[]string(nil),
			value.ExcludedAreas...),
		NonGoals:              append([]string(nil), value.NonGoals...),
		ClarificationNeeded:   value.ClarificationNeeded,
		ClarificationQuestion: value.ClarificationQuestion,
		BaseSHA:               value.BaseSHA,
		HeadSHA:               value.HeadSHA,
		CreatedBy:             "prworkspace",
		ConfirmedAt:           cloneTimePointer(value.ConfirmedAt),
	}
}

func toEventingStageRun(value StageRun, version int64) eventing.PRStageRun {
	var started *time.Time
	var evidence json.RawMessage
	if !value.StartedAt.IsZero() {
		started = cloneTimePointer(&value.StartedAt)
	}
	if value.Evidence != nil {
		evidence, _ = json.Marshal(value.Evidence)
	}
	inputVersion := value.inputWorkspaceVersion
	if inputVersion <= 0 {
		inputVersion = version
	}
	return eventing.PRStageRun{
		PRWorkspaceRecord: toEventingRecord(
			value.ID,
			value.StartedAt,
			timeFromPointer(value.FinishedAt, value.StartedAt),
		),
		Phase:            phaseForStage(value.Stage),
		Kind:             value.Stage,
		State:            eventing.PRExecutionState(value.State),
		Attempt:          value.Attempt,
		CharterID:        value.CharterID,
		WorkspaceVersion: inputVersion,
		HeadSHA:          value.HeadSHA,
		PromptDigest:     value.PromptDigest,
		Summary:          value.Summary,
		PublicErrorCode:  value.PublicError,
		Evidence:         evidence,
		StartedAt:        started,
		FinishedAt:       cloneTimePointer(value.FinishedAt),
	}
}

func toEventingFinding(value Finding) (eventing.PRFinding, error) {
	result := eventing.PRFinding{
		PRWorkspaceRecord: toEventingRecord(value.ID, value.CreatedAt, value.UpdatedAt),
		Origin:            string(value.Origin),
		StageRunID:        value.OriginRunID,
		NudgeRoundID:      value.NudgeRoundID,
		Fingerprint:       value.Fingerprint,
		Severity:          value.Severity,
		Title:             value.Title,
		Message:           value.Message,
		File:              value.File,
		Line:              cloneIntPointer(value.Line),
		Evidence:          value.Evidence,
		Impact:            value.Impact,
		Recommendation:    value.Recommendation,
		Validation:        value.Validation,
		Disposition:       eventing.PRFindingDisposition(value.Disposition),
		ScopeDistance: eventing.PRScopeDistance(
			value.Scope.Distance,
		),
		ChangeSize:         eventing.PRChangeSize(value.Scope.Size),
		TypeCompatible:     value.Scope.TypeCompatible,
		ClassificationConf: value.Scope.Confidence,
		CharterClauses:     append([]string(nil), value.Scope.CharterClauses...),
		EstimatedMetrics: eventing.PRChangeMetrics{
			Files:         value.Scope.Files,
			SemanticLines: value.Scope.SemanticLines,
			Modules:       value.Scope.Modules,
		},
		MetricsEstimated:        value.Scope.Estimated,
		ScopeExplanation:        value.Scope.Explanation,
		ScopePolicyMode:         string(value.ScopePolicyMode),
		ScopePolicyRevision:     value.ScopePolicyRevision,
		ScopePolicyPromptDigest: value.ScopePolicyPromptDigest,
		ScopePresence:           eventing.PRWorkPresence(value.Scope.Presence),
		ScopeChangeEvidence:     toEventingScopeChanges(value.Scope.ChangeEvidence),
		NudgeReward: cloneFloatPointer(
			value.NudgeReward,
		),
		RewardSource: value.RewardSource,
		Version:      value.Version,
	}
	if value.source != nil {
		source := eventing.PRFindingSourceExecution{
			ExecutionID: value.source.ExecutionID, WorkspaceID: value.source.WorkspaceID,
			Binding: value.source.Binding, AgentID: value.source.AgentID,
			Session: value.source.Session, SessionRevision: value.source.SessionRevision,
			Tools: value.source.Tools,
		}
		if err := result.SetProtectedSourceExecution(&source); err != nil {
			return eventing.PRFinding{}, err
		}
	}
	return result, nil
}

func toEventingMessage(value Message) eventing.PRMessage {
	kind := "workspace_message"
	if value.Mode != "" {
		kind = "development_chat:" + value.Mode + ":" + value.Status
	} else if value.Stage != "" {
		kind += ":" + value.Stage
	}
	return eventing.PRMessage{
		PRWorkspaceRecord: toEventingRecord(value.ID, value.CreatedAt, value.CreatedAt),
		Phase:             phaseForStage(value.Stage), Kind: kind, Role: value.Role, Content: value.Content,
		CharterID: value.CharterID, HeadSHA: value.HeadSHA,
	}
}

func toEventingCorrection(value Correction) eventing.PRCorrection {
	review, implementation := correctionApplicabilityFlags(value.Applicability)
	return eventing.PRCorrection{
		PRWorkspaceRecord: toEventingRecord(value.ID, value.CreatedAt, value.CreatedAt),
		Kind:              string(value.Kind), Status: eventing.PRRecordActive,
		TargetKind: value.TargetType, TargetID: value.TargetID,
		OriginalClaim: value.OriginalClaim, Correction: value.Correction, Evidence: value.Evidence,
		AppliesToReview: review, AppliesToImplement: implementation,
		CharterID: value.CharterID, HeadSHA: value.HeadSHA, SupersedesID: value.SupersedesID,
		Promoted: value.Promoted,
	}
}

func toEventingLesson(value RepositoryLesson) eventing.PRRepositoryLesson {
	status := eventing.PRRecordRevoked
	if value.Active {
		status = eventing.PRRecordActive
	}
	types := []eventing.PRType(nil)
	if value.PRType != "" {
		types = []eventing.PRType{eventing.PRType(value.PRType)}
	}
	return eventing.PRRepositoryLesson{
		PRWorkspaceRecord: toEventingRecord(
			value.ID,
			value.CreatedAt,
			timeFromPointer(value.RevokedAt, value.CreatedAt),
		),
		RepositoryID:       value.RepositoryID,
		Status:             status,
		Kind:               string(value.Kind),
		Content:            value.Text,
		SourceCorrectionID: value.CorrectionID,
		ApplicableTypes:    types,
		ApplicablePhases:   lessonPhases(value.Applicability),
		ConfirmedBy:        "user",
		RevokedAt:          cloneTimePointer(value.RevokedAt),
	}
}

func toEventingNudge(value NudgeRoundRecord) eventing.PRNudgeRound {
	minimum, hardCap := value.MinimumRounds, value.HardCap
	if hardCap < value.Round {
		hardCap = value.Round
	}
	if hardCap == 0 {
		hardCap = 1
	}
	if minimum > hardCap {
		minimum = hardCap
	}
	strategy := string(value.Strategy)
	if strategy == "" {
		strategy = string(NudgeCoverageGaps)
	}
	promptDigest := value.PromptDigest
	if promptDigest == "" {
		promptDigest = persistenceDigest([]byte(value.Challenge))
	}
	return eventing.PRNudgeRound{
		PRWorkspaceRecord: toEventingRecord(value.ID, value.CreatedAt, value.CreatedAt),
		StageRunID:        value.StageRunID, Phase: phaseForNudge(value.Stage), Stage: string(value.Stage),
		State: eventing.PRExecutionState(value.State), Round: value.Round,
		MinimumRounds: minimum, HardCap: hardCap, StrategyFamily: strategy, Strategy: strategy,
		CoverageTarget: strategy, Challenge: value.Challenge,
		ChallengeDigest: persistenceDigest([]byte(value.Challenge)), VariantDigest: value.VariantDigest,
		PromptDigest: promptDigest, CandidateCount: value.NovelFindings + value.DuplicateCount,
		NovelCount: value.NovelFindings, DuplicateCount: value.DuplicateCount,
		FindingIDs: append([]string(nil), value.FindingIDs...), ResolvedFindings: value.ResolvedFindings,
		Reward: cloneFloatPointer(value.Reward), RewardProvenance: value.RewardProvenance,
		PublicError: value.PublicError,
	}
}

func toEventingDeferredGroup(value DeferredGroup) eventing.PRDeferredGroup {
	status := eventing.PRRecordDraft
	if value.PublicationID != "" || value.ExistingIssueURL != "" {
		status = eventing.PRRecordActive
	}
	return eventing.PRDeferredGroup{
		PRWorkspaceRecord: toEventingRecord(value.ID, value.CreatedAt, value.UpdatedAt),
		Status:            status,
		Title:             value.Title,
		Body:              value.Body,
		ScopeDistance: eventing.PRScopeDistance(
			value.Scope.Distance,
		),
		ChangeSize:          eventing.PRChangeSize(value.Scope.Size),
		ScopeFiles:          value.Scope.Files,
		ScopeSemanticLines:  value.Scope.SemanticLines,
		ScopeModules:        value.Scope.Modules,
		ScopeEstimated:      value.Scope.Estimated,
		ScopeTypeCompatible: value.Scope.TypeCompatible,
		ScopeConfidence:     value.Scope.Confidence,
		ScopeCharterClauses: append([]string(nil), value.Scope.CharterClauses...),
		ScopeExplanation:    value.Scope.Explanation,
		ScopePresence:       eventing.PRWorkPresence(value.Scope.Presence),
		ScopeChangeEvidence: toEventingScopeChanges(
			value.Scope.ChangeEvidence,
		),
		Labels:                append([]string(nil), value.Labels...),
		DraftRevision:         value.Version,
		ExternalURL:           value.ExistingIssueURL,
		PublicationID:         value.PublicationID,
		PublicationSuppressed: value.PublicationSuppressed,
		SuppressionReason:     value.SuppressionReason,
		Version:               value.Version,
	}
}

func toEventingRepair(value RepairAttempt) eventing.PRRepairAttempt {
	baseCommit := value.CandidateSHA
	tipCommit, tree := "", ""
	var fence *eventing.PRImplementationPublicationFence
	if value.PublicationFence != nil {
		baseCommit = value.PublicationFence.BaseCommit
		tipCommit, tree = value.PublicationFence.Tip, value.PublicationFence.Tree
		fence = &eventing.PRImplementationPublicationFence{
			GitWorkspaceID: value.PublicationFence.GitWorkspaceID,
			LineID:         value.PublicationFence.LineID,
			LineVersion:    value.PublicationFence.LineVersion,
			MutationEpoch:  value.PublicationFence.MutationEpoch,
			ParkIntentID:   value.PublicationFence.ParkIntentID,
			BaseCommit:     value.PublicationFence.BaseCommit,
			Tip:            value.PublicationFence.Tip,
			Tree:           value.PublicationFence.Tree,
		}
	}
	if baseCommit == "" {
		baseCommit = persistenceDigest([]byte("missing-base:" + value.ID))
	}
	var started *time.Time
	if !value.StartedAt.IsZero() {
		started = cloneTimePointer(&value.StartedAt)
	}
	return eventing.PRRepairAttempt{
		PRWorkspaceRecord: toEventingRecord(
			value.ID,
			value.StartedAt,
			timeFromPointer(value.FinishedAt, value.StartedAt),
		),
		StageRunID:        value.StageRunID,
		State:             eventing.PRExecutionState(value.State),
		Attempt:           value.Number,
		Instruction:       value.Instruction,
		RepairWorkspaceID: value.WorkspaceID,
		ResultSummary:     value.ResultSummary,
		FindingIDs: append(
			[]string(nil),
			value.FindingIDs...),
		GoalDigest:   persistenceDigest([]byte(value.Instruction)),
		BaseCommit:   baseCommit,
		TipCommit:    tipCommit,
		CandidateSHA: value.CandidateSHA,
		Tree:         tree,
		ChangedFiles: append([]string(nil), value.ChangedFiles...),
		Metrics: eventing.PRChangeMetrics{
			Files:         value.Scope.Files,
			SemanticLines: value.Scope.SemanticLines,
			Modules:       value.Scope.Modules,
		},
		ScopeDrift: value.Scope.Distance != ScopeExact,
		TypeDrift:  !value.Scope.TypeCompatible,
		ScopeDistance: eventing.PRScopeDistance(
			value.Scope.Distance,
		),
		ScopeChangeSize:     eventing.PRChangeSize(value.Scope.Size),
		ScopeEstimated:      value.Scope.Estimated,
		ScopeTypeCompatible: value.Scope.TypeCompatible,
		ScopeConfidence:     value.Scope.Confidence,
		ScopeCharterClauses: append([]string(nil), value.Scope.CharterClauses...),
		ScopeExplanation:    value.Scope.Explanation,
		ScopePresence:       eventing.PRWorkPresence(value.Scope.Presence),
		ScopeChangeEvidence: toEventingScopeChanges(value.Scope.ChangeEvidence),
		PromptDigest:        value.PromptDigest,
		ScopePromptDigest:   value.ScopePromptDigest,
		StartedAt:           started,
		FinishedAt:          cloneTimePointer(value.FinishedAt),
		PublicationFence:    fence,
	}
}

func toEventingValidation(value ValidationRun) eventing.PRValidationRun {
	checks := make([]eventing.PRValidationCheck, 0, len(value.Checks))
	for _, check := range value.Checks {
		checks = append(checks, eventing.PRValidationCheck{
			ID: check.ID, Name: check.Name, Status: check.Status, Summary: check.Summary,
			ExitCode: cloneIntPointer(check.ExitCode), DurationMS: check.DurationMS,
		})
	}
	var started *time.Time
	if !value.StartedAt.IsZero() {
		started = cloneTimePointer(&value.StartedAt)
	}
	return eventing.PRValidationRun{
		PRWorkspaceRecord: toEventingRecord(
			value.ID,
			value.StartedAt,
			timeFromPointer(value.FinishedAt, value.StartedAt),
		),
		StageRunID:      value.StageRunID,
		RepairAttemptID: value.RepairAttemptID,
		CandidateSHA:    value.CandidateSHA,
		State:           eventing.PRExecutionState(value.State),
		Kind:            "checks",
		Checks:          checks,
		StartedAt:       started,
		FinishedAt:      cloneTimePointer(value.FinishedAt),
	}
}

func toEventingGate(value GateRun) eventing.PRGateRun {
	workflowConfigurationID := "default"
	workflowConfigurationRevision := "unversioned"
	policyRevision := value.PolicyRevision
	if policyRevision == "" {
		policyRevision = "unversioned"
	}
	var workflowRef, workflowRevision, gateRef string
	var workflowRunID string
	var policy, subject json.RawMessage
	runtimePresent := value.runtime != nil
	if value.runtime != nil {
		if value.runtime.WorkflowConfigurationID != "" {
			workflowConfigurationID = value.runtime.WorkflowConfigurationID
		}
		workflowRunID = value.runtime.WorkflowRunID
		policy = cloneOptionalGateJSON(value.runtime.PinnedPolicy)
		subject = cloneOptionalGateJSON(value.runtime.PinnedSubject)
	}
	var pinned pinnedPRLifecycleGateV4
	if len(policy) > 0 && json.Unmarshal(policy, &pinned) == nil && pinned.Version == "4" {
		workflowRef, workflowRevision, gateRef = pinned.WorkflowRef, pinned.WorkflowRevision, pinned.GateRef
		if pinned.WorkflowConfigurationID != "" {
			workflowConfigurationID = pinned.WorkflowConfigurationID
		}
		if pinned.WorkflowConfigurationRevision != "" {
			workflowConfigurationRevision = pinned.WorkflowConfigurationRevision
		}
	}
	if gateRef == "" {
		for _, turn := range value.Turns {
			if turn.GateForm != nil && turn.GateForm.GateRef != "" {
				gateRef = turn.GateForm.GateRef
				break
			}
		}
	}
	if gateRef != "" && (workflowRef == "" || workflowRevision == "") {
		if catalog, err := PRLifecycleGateCatalog(); err == nil {
			for _, entry := range catalog {
				if entry.GateRef == gateRef {
					workflowRef, workflowRevision = entry.WorkflowRef, entry.WorkflowRevision
					break
				}
			}
		}
	}
	subjectRevision := value.SubjectRevision
	if subjectRevision == "" {
		subjectRevision = persistenceDigest(subject)
	}
	turns := make([]eventing.PRGateTurn, 0, len(value.Turns))
	evidence, _ := json.Marshal(value.Evidence)
	currentStageID := ""
	for _, turn := range value.Turns {
		gateForm, _ := json.Marshal(turn.GateForm)
		if turn.GateForm == nil {
			gateForm = nil
		}
		turns = append(turns, eventing.PRGateTurn{
			StageID: turn.StageID, Kind: turn.Kind, Title: turn.Title, Status: turn.Status,
			GateForm: gateForm, FieldValues: turn.FieldValues, ActorKind: turn.ActorKind,
			ExecutionID: turn.ExecutionID, ActionRevision: turn.ActionRevision, InputHash: turn.InputHash,
		})
		if currentStageID == "" && (turn.Status == "waiting" || turn.Status == "running") {
			currentStageID = turn.StageID
		}
	}
	return eventing.PRGateRun{
		PRWorkspaceRecord: toEventingRecord(
			value.ID,
			value.CreatedAt,
			timeFromPointer(value.FinishedAt, value.CreatedAt),
		),
		DecisionPoint:                 value.DecisionPoint,
		TargetID:                      value.TargetID,
		State:                         eventing.PRExecutionState(value.State),
		PolicyRevision:                policyRevision,
		WorkflowRef:                   workflowRef,
		WorkflowRevision:              workflowRevision,
		GateRef:                       gateRef,
		WorkflowConfigurationID:       workflowConfigurationID,
		WorkflowConfigurationRevision: workflowConfigurationRevision,
		PinnedPolicy:                  policy,
		PinnedPolicyHash:              persistenceDigest(policy),
		SubjectRevision:               subjectRevision,
		PinnedSubject:                 subject,
		PinnedSubjectHash:             persistenceDigest(subject),
		WorkflowRunID:                 workflowRunID,
		RuntimePresent:                runtimePresent,
		CurrentStageID:                currentStageID,
		Turns:                         turns,
		Evidence:                      evidence,
		FinishedAt:                    cloneTimePointer(value.FinishedAt),
	}
}

func toEventingPublication(value Publication) eventing.PRPublication {
	status := publicationStatus(value.State)
	availableAt := value.CreatedAt
	if availableAt.IsZero() {
		availableAt = value.UpdatedAt
	}
	if availableAt.IsZero() {
		availableAt = time.Unix(0, 0).UTC()
	}
	digest := value.PayloadDigest
	if digest == "" {
		digest = persistenceDigest([]byte(value.ID))
	}
	request := append(json.RawMessage(nil), value.payload...)
	if len(request) == 0 {
		request = json.RawMessage("{}")
	}
	publication := eventing.PRPublication{
		PRWorkspaceRecord: toEventingRecord(value.ID, value.CreatedAt, value.UpdatedAt),
		Kind:              eventing.PRPublicationKind(value.Kind), Status: status,
		TargetID: value.TargetID, FindingIDs: append([]string(nil), value.FindingIDs...),
		ExpectedHeadSHA: value.ExpectedHeadSHA,
		Marker:          "picoclaw-pr-publication:" + value.ID + ":" + digest,
		Request:         request, RequestDigest: digest, PayloadDigest: digest,
		ExecutionState: eventing.PRExecutionState(value.State), AvailableAt: availableAt,
		Attempts: value.Attempts, ExternalID: value.ExternalID, ExternalURL: value.ExternalURL,
		PublicErrorCode: value.PublicErrorCode, PublishedAt: cloneTimePointer(value.PublishedAt),
	}
	if value.Kind == PublicationGitHubIssue {
		publication.DeferredGroupID = value.TargetID
	}
	return publication
}

func fromEventingAggregate(value eventing.PRWorkspaceAggregate) (Aggregate, error) {
	result := Aggregate{
		Workspace:        fromEventingWorkspace(value.Workspace),
		ProviderSnapshot: fromEventingProvider(value.ProviderSnapshot),
	}
	for _, item := range value.Charters {
		result.Charters = append(result.Charters, fromEventingCharter(item))
	}
	for _, item := range value.StageRuns {
		result.StageRuns = append(result.StageRuns, fromEventingStageRun(item))
	}
	for _, item := range value.Findings {
		finding, err := fromEventingFinding(item)
		if err != nil {
			return Aggregate{}, err
		}
		result.Findings = append(result.Findings, finding)
	}
	for _, item := range value.Messages {
		result.Messages = append(result.Messages, fromEventingMessage(item))
	}
	for _, item := range value.Corrections {
		result.Corrections = append(result.Corrections, fromEventingCorrection(item))
	}
	for _, item := range value.RepositoryLessons {
		result.RepositoryLessons = append(result.RepositoryLessons, fromEventingLesson(item))
	}
	for _, item := range value.NudgeRounds {
		result.NudgeRounds = append(result.NudgeRounds, fromEventingNudge(item))
	}
	for _, item := range value.DeferredGroups {
		result.DeferredGroups = append(result.DeferredGroups, fromEventingDeferredGroup(item))
	}
	for _, item := range value.RepairAttempts {
		result.RepairAttempts = append(result.RepairAttempts, fromEventingRepair(item))
	}
	for _, item := range value.ValidationRuns {
		result.ValidationRuns = append(result.ValidationRuns, fromEventingValidation(item))
	}
	for _, item := range value.GateRuns {
		result.Gates = append(result.Gates, fromEventingGate(item))
	}
	for _, item := range value.Publications {
		result.Publications = append(result.Publications, fromEventingPublication(item))
	}
	for _, item := range value.Activity {
		result.Activity = append(result.Activity, Activity{
			Ordinal: item.Ordinal, Kind: item.Kind, Actor: item.Actor, Summary: item.Summary,
			EntityID: item.EntityID, Metadata: item.Metadata, CreatedAt: item.CreatedAt,
		})
	}
	return result, nil
}

func fromEventingWorkspace(value eventing.PRWorkspace) Workspace {
	return Workspace{
		ID: value.ID, Intent: DevelopmentIntent(value.Intent), SourceKind: SourceKind(value.SourceKind),
		SourceID: value.SourceID, SourceNumber: value.SourceNumber,
		Provider: value.Provider, ProviderOrigin: value.ProviderOrigin,
		RepositoryID: value.RepositoryID, PullRequestID: value.PullRequestID,
		Repository: value.Repository, PullNumber: value.PullNumber,
		Phase: Phase(value.Phase), ExecutionState: ExecutionState(value.ExecutionState),
		ActiveCharterID: value.ActiveCharterID, ProviderHeadSHA: value.ProviderHeadSHA,
		Version: value.Version, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func fromEventingProvider(value eventing.PRProviderSnapshot) ProviderSnapshot {
	return ProviderSnapshot{
		Intent: DevelopmentIntent(value.Intent), SourceKind: SourceKind(value.SourceKind),
		SourceID: value.SourceID, SourceNumber: value.SourceNumber, SourceURL: value.SourceURL,
		Provider: value.Provider, ProviderOrigin: value.ProviderOrigin,
		RepositoryID: value.RepositoryID, Repository: value.Repository,
		PullRequestID: value.PullRequestID, PullNumber: value.PullNumber,
		Title: value.Title, Body: value.Body, AuthorID: value.AuthorID,
		AuthorLogin: value.AuthorLogin, AuthenticatedUserID: value.AuthenticatedUserID,
		BaseRef: value.BaseRef, BaseSHA: value.BaseSHA,
		HeadRepositoryID: value.HeadRepositoryID, HeadRepository: value.HeadRepository,
		HeadRef: value.HeadRef, HeadSHA: value.HeadSHA, State: value.State,
		Owned: value.Owned, HeadWritable: value.HeadWritable,
		CanReview: value.CanReview, CanCreateIssue: value.CanCreateIssue,
		CanCreatePullRequest: value.CanCreatePullRequest,
		ProviderRevision:     value.ProviderRevision, ObservedAt: value.ObservedAt,
	}
}

func fromEventingCharter(value eventing.PRCharterRevision) Charter {
	return Charter{
		ID:                 value.ID,
		Revision:           value.Revision,
		Type:               PRType(value.Type),
		Goal:               value.Goal,
		AcceptanceCriteria: append([]string(nil), value.AcceptanceCriteria...),
		IncludedAreas:      append([]string(nil), value.IncludedAreas...),
		ExcludedAreas: append(
			[]string(nil),
			value.Exclusions...),
		NonGoals:              append([]string(nil), value.NonGoals...),
		ClarificationNeeded:   value.ClarificationNeeded,
		ClarificationQuestion: value.ClarificationQuestion,
		BaseSHA:               value.BaseSHA,
		HeadSHA:               value.HeadSHA,
		Confirmed:             value.Status == eventing.PRRecordConfirmed,
		CreatedAt:             value.CreatedAt,
		ConfirmedAt:           cloneTimePointer(value.ConfirmedAt),
	}
}

func fromEventingStageRun(value eventing.PRStageRun) StageRun {
	result := StageRun{
		ID: value.ID, Stage: value.Kind, State: ExecutionState(value.State), CharterID: value.CharterID,
		HeadSHA: value.HeadSHA, Attempt: value.Attempt, PromptDigest: value.PromptDigest,
		Summary: value.Summary, PublicError: value.PublicErrorCode,
		StartedAt: timeFromPointer(value.StartedAt, value.CreatedAt), FinishedAt: cloneTimePointer(value.FinishedAt),
	}
	if len(value.Evidence) > 0 {
		var evidence StageEvidence
		if json.Unmarshal(value.Evidence, &evidence) == nil {
			result.Evidence = &evidence
		}
	}
	result.inputWorkspaceVersion = value.WorkspaceVersion
	return result
}

func fromEventingFinding(value eventing.PRFinding) (Finding, error) {
	result := Finding{
		ID: value.ID, Fingerprint: value.Fingerprint, Origin: FindingOrigin(value.Origin),
		OriginRunID: value.StageRunID, NudgeRoundID: value.NudgeRoundID,
		Severity: value.Severity, Title: value.Title, File: value.File, Line: cloneIntPointer(value.Line),
		Message: value.Message, Evidence: value.Evidence, Impact: value.Impact,
		Recommendation: value.Recommendation, Validation: value.Validation,
		Scope: ScopeAssessment{
			Distance: ScopeDistance(value.ScopeDistance), Size: ChangeSize(value.ChangeSize),
			Presence: WorkPresence(value.ScopePresence),
			Files:    value.EstimatedMetrics.Files, SemanticLines: value.EstimatedMetrics.SemanticLines,
			Modules: value.EstimatedMetrics.Modules, Estimated: value.MetricsEstimated,
			TypeCompatible: value.TypeCompatible, Confidence: value.ClassificationConf,
			CharterClauses: append([]string(nil), value.CharterClauses...), Explanation: value.ScopeExplanation,
			ChangeEvidence: fromEventingScopeChanges(value.ScopeChangeEvidence),
		},
		Disposition: FindingDisposition(value.Disposition), NudgeReward: cloneFloatPointer(value.NudgeReward),
		ScopePolicyMode:         ScopeDispositionMode(value.ScopePolicyMode),
		ScopePolicyRevision:     value.ScopePolicyRevision,
		ScopePolicyPromptDigest: value.ScopePolicyPromptDigest,
		RewardSource:            value.RewardSource, Version: value.Version,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
	if persisted, ok := value.ProtectedSourceExecution(); ok {
		source := AIExecutionSource{
			ExecutionID: persisted.ExecutionID, WorkspaceID: persisted.WorkspaceID,
			Binding: persisted.Binding, AgentID: persisted.AgentID,
			Session: persisted.Session, SessionRevision: persisted.SessionRevision,
			Tools: persisted.Tools,
		}
		if !validAIExecutionSource(&source) {
			return Finding{}, fmt.Errorf("%w: protected finding source is invalid", ErrInvalid)
		}
		result.source = &source
		result.SourceAvailable = true
	}
	return result, nil
}

func fromEventingMessage(value eventing.PRMessage) Message {
	stage := ""
	mode, status := "", ""
	if strings.HasPrefix(value.Kind, "workspace_message:") {
		stage = strings.TrimPrefix(value.Kind, "workspace_message:")
	} else if strings.HasPrefix(value.Kind, "development_chat:") {
		parts := strings.Split(value.Kind, ":")
		if len(parts) == 3 {
			mode, status, stage = parts[1], parts[2], "implementation"
		}
	}
	return Message{
		ID: value.ID, Role: value.Role, Stage: stage, Mode: mode, Status: status, Content: value.Content,
		CharterID: value.CharterID, HeadSHA: value.HeadSHA, CreatedAt: value.CreatedAt,
	}
}

func fromEventingCorrection(value eventing.PRCorrection) Correction {
	return Correction{
		ID: value.ID, Kind: CorrectionKind(value.Kind),
		Applicability: correctionApplicability(value.AppliesToReview, value.AppliesToImplement),
		TargetType:    value.TargetKind, TargetID: value.TargetID,
		OriginalClaim: value.OriginalClaim, Correction: value.Correction, Evidence: value.Evidence,
		CharterID: value.CharterID, HeadSHA: value.HeadSHA, SupersedesID: value.SupersedesID,
		Promoted: value.Promoted || value.RepositoryLessonID != "", CreatedAt: value.CreatedAt,
	}
}

func fromEventingLesson(value eventing.PRRepositoryLesson) RepositoryLesson {
	prType := PRType("")
	if len(value.ApplicableTypes) > 0 {
		prType = PRType(value.ApplicableTypes[0])
	}
	return RepositoryLesson{
		ID: value.ID, RepositoryID: value.RepositoryID, SourcePR: value.WorkspaceID,
		CorrectionID: value.SourceCorrectionID, Kind: CorrectionKind(value.Kind),
		Applicability: applicabilityFromLessonPhases(value.ApplicablePhases),
		PRType:        prType, Text: value.Content, Active: value.Status == eventing.PRRecordActive,
		CreatedAt: value.CreatedAt, RevokedAt: cloneTimePointer(value.RevokedAt),
	}
}

func fromEventingNudge(value eventing.PRNudgeRound) NudgeRoundRecord {
	strategy := value.Strategy
	if strategy == "" {
		strategy = value.StrategyFamily
	}
	return NudgeRoundRecord{
		ID: value.ID, StageRunID: value.StageRunID, Stage: NudgeStage(value.Stage), Round: value.Round,
		MinimumRounds: value.MinimumRounds, HardCap: value.HardCap, Strategy: NudgeStrategy(strategy),
		Challenge: value.Challenge, VariantDigest: value.VariantDigest, PromptDigest: value.PromptDigest,
		State: ExecutionState(value.State), PublicError: value.PublicError,
		NovelFindings: value.NovelCount, DuplicateCount: value.DuplicateCount,
		FindingIDs: append([]string(nil), value.FindingIDs...), ResolvedFindings: value.ResolvedFindings,
		Reward: cloneFloatPointer(value.Reward), RewardProvenance: value.RewardProvenance,
		CreatedAt: value.CreatedAt,
	}
}

func fromEventingDeferredGroup(value eventing.PRDeferredGroup) DeferredGroup {
	findingIDs := make([]string, 0, len(value.Items))
	for _, item := range value.Items {
		findingIDs = append(findingIDs, item.FindingID)
	}
	return DeferredGroup{
		ID: value.ID, Title: value.Title, Body: value.Body, FindingIDs: findingIDs,
		Scope: ScopeAssessment{
			Distance: ScopeDistance(value.ScopeDistance), Size: ChangeSize(value.ChangeSize),
			Presence: WorkPresence(value.ScopePresence),
			Files:    value.ScopeFiles, SemanticLines: value.ScopeSemanticLines, Modules: value.ScopeModules,
			Estimated: value.ScopeEstimated, TypeCompatible: value.ScopeTypeCompatible,
			Confidence: value.ScopeConfidence, CharterClauses: append([]string(nil), value.ScopeCharterClauses...),
			Explanation: value.ScopeExplanation, ChangeEvidence: fromEventingScopeChanges(value.ScopeChangeEvidence),
		},
		Labels: append([]string(nil), value.Labels...), ExistingIssueURL: value.ExternalURL,
		PublicationID: value.PublicationID, PublicationSuppressed: value.PublicationSuppressed,
		SuppressionReason: value.SuppressionReason, Version: value.Version,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func fromEventingRepair(value eventing.PRRepairAttempt) RepairAttempt {
	result := RepairAttempt{
		ID:            value.ID,
		StageRunID:    value.StageRunID,
		Number:        value.Attempt,
		State:         ExecutionState(value.State),
		Instruction:   value.Instruction,
		WorkspaceID:   value.RepairWorkspaceID,
		ResultSummary: value.ResultSummary,
		ChangedFiles: append(
			[]string(nil),
			value.ChangedFiles...),
		FindingIDs:   append([]string(nil), value.FindingIDs...),
		CandidateSHA: value.CandidateSHA,
		Scope: ScopeAssessment{
			Distance: ScopeDistance(value.ScopeDistance), Size: ChangeSize(value.ScopeChangeSize),
			Presence: WorkPresence(value.ScopePresence),
			Files:    value.Metrics.Files, SemanticLines: value.Metrics.SemanticLines, Modules: value.Metrics.Modules,
			Estimated: value.ScopeEstimated, TypeCompatible: value.ScopeTypeCompatible,
			Confidence: value.ScopeConfidence, CharterClauses: append([]string(nil), value.ScopeCharterClauses...),
			Explanation: value.ScopeExplanation, ChangeEvidence: fromEventingScopeChanges(value.ScopeChangeEvidence),
		},
		PromptDigest:      value.PromptDigest,
		ScopePromptDigest: value.ScopePromptDigest,
		StartedAt:         timeFromPointer(value.StartedAt, value.CreatedAt),
		FinishedAt:        cloneTimePointer(value.FinishedAt),
	}
	if value.PublicationFence != nil {
		result.PublicationFence = &ImplementationPublicationFence{
			GitWorkspaceID: value.PublicationFence.GitWorkspaceID,
			LineID:         value.PublicationFence.LineID,
			LineVersion:    value.PublicationFence.LineVersion,
			MutationEpoch:  value.PublicationFence.MutationEpoch,
			ParkIntentID:   value.PublicationFence.ParkIntentID,
			BaseCommit:     value.PublicationFence.BaseCommit,
			Tip:            value.PublicationFence.Tip,
			Tree:           value.PublicationFence.Tree,
		}
	}
	return result
}

func fromEventingValidation(value eventing.PRValidationRun) ValidationRun {
	checks := make([]ValidationCheck, 0, len(value.Checks))
	for _, check := range value.Checks {
		checks = append(checks, ValidationCheck{
			ID: check.ID, Name: check.Name, Status: check.Status, Summary: check.Summary,
			ExitCode: cloneIntPointer(check.ExitCode), DurationMS: check.DurationMS,
		})
	}
	return ValidationRun{
		ID: value.ID, StageRunID: value.StageRunID, RepairAttemptID: value.RepairAttemptID,
		State:        ExecutionState(value.State),
		CandidateSHA: value.CandidateSHA, Checks: checks,
		StartedAt: timeFromPointer(value.StartedAt, value.CreatedAt), FinishedAt: cloneTimePointer(value.FinishedAt),
	}
}

func fromEventingGate(value eventing.PRGateRun) GateRun {
	turns := make([]GateTurn, 0, len(value.Turns))
	for _, turn := range value.Turns {
		var gateForm *GateForm
		if len(turn.GateForm) > 0 {
			var decoded GateForm
			if json.Unmarshal(turn.GateForm, &decoded) == nil {
				gateForm = &decoded
			}
		}
		turns = append(turns, GateTurn{
			StageID: turn.StageID, Kind: turn.Kind, Title: turn.Title, Status: turn.Status,
			GateForm: gateForm, FieldValues: turn.FieldValues, ActorKind: turn.ActorKind,
			ExecutionID: turn.ExecutionID, ActionRevision: turn.ActionRevision, InputHash: turn.InputHash,
		})
	}
	var evidence GateEvidence
	_ = json.Unmarshal(value.Evidence, &evidence)
	result := GateRun{
		ID: value.ID, DecisionPoint: value.DecisionPoint, TargetID: value.TargetID,
		State:          ExecutionState(value.State),
		PolicyRevision: value.PolicyRevision, SubjectRevision: value.SubjectRevision,
		Turns: turns, Evidence: evidence, CreatedAt: value.CreatedAt, FinishedAt: cloneTimePointer(value.FinishedAt),
	}
	if value.RuntimePresent {
		result.runtime = &gateRuntime{
			WorkflowConfigurationID: value.WorkflowConfigurationID, WorkflowRunID: value.WorkflowRunID,
			PinnedPolicy:  cloneOptionalGateJSON(value.PinnedPolicy),
			PinnedSubject: cloneOptionalGateJSON(value.PinnedSubject),
		}
	}
	return result
}

// Optional pinned gate inputs use nil to mean absent. Encoding a nil
// json.RawMessage writes JSON null, and decoding that value produces the bytes
// "null". Treat both representations identically so a durable round trip does
// not change the immutable pinned-input hashes of configured fallback gates.
func cloneOptionalGateJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}

func fromEventingPublication(value eventing.PRPublication) Publication {
	state := ExecutionState(value.ExecutionState)
	if state == "" {
		state = executionStateForPublication(value.Status)
	}
	return Publication{
		ID: value.ID, Kind: PublicationKind(value.Kind), State: state,
		TargetID: value.TargetID, FindingIDs: append([]string(nil), value.FindingIDs...),
		ExpectedHeadSHA: value.ExpectedHeadSHA, PayloadDigest: value.PayloadDigest,
		ExternalID: value.ExternalID, ExternalURL: value.ExternalURL,
		PublicErrorCode: value.PublicErrorCode, Attempts: value.Attempts,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, PublishedAt: cloneTimePointer(value.PublishedAt),
		payload: append(json.RawMessage(nil), value.Request...),
	}
}

func toEventingScopeChanges(values []ScopeChange) []eventing.PRScopeChange {
	result := make([]eventing.PRScopeChange, 0, len(values))
	for _, value := range values {
		result = append(result, eventing.PRScopeChange{
			Path: value.Path, Hunk: value.Hunk, Module: value.Module,
			SemanticLines: value.SemanticLines, Presence: eventing.PRWorkPresence(value.Presence),
			ScopeDistance: eventing.PRScopeDistance(value.Distance), ChangeSize: eventing.PRChangeSize(value.Size),
			TypeCompatible: value.TypeCompatible, Confidence: value.Confidence,
			CharterClauses: append([]string(nil), value.CharterClauses...), Explanation: value.Explanation,
		})
	}
	return result
}

func fromEventingScopeChanges(values []eventing.PRScopeChange) []ScopeChange {
	result := make([]ScopeChange, 0, len(values))
	for _, value := range values {
		result = append(result, ScopeChange{
			Path: value.Path, Hunk: value.Hunk, Module: value.Module,
			SemanticLines: value.SemanticLines, Presence: WorkPresence(value.Presence),
			Distance: ScopeDistance(value.ScopeDistance), Size: ChangeSize(value.ChangeSize),
			TypeCompatible: value.TypeCompatible, Confidence: value.Confidence,
			CharterClauses: append([]string(nil), value.CharterClauses...), Explanation: value.Explanation,
		})
	}
	return result
}

func toEventingRecord(id string, createdAt, updatedAt time.Time) eventing.PRWorkspaceRecord {
	return eventing.PRWorkspaceRecord{ID: id, CreatedAt: createdAt, UpdatedAt: updatedAt}
}

func phaseForStage(stage string) eventing.PRWorkspacePhase {
	switch strings.TrimSpace(strings.ToLower(stage)) {
	case "charter":
		return eventing.PRWorkspaceCharter
	case "review":
		return eventing.PRWorkspaceReview
	case "triage":
		return eventing.PRWorkspaceTriage
	case "implementation", "implementation_completion":
		return eventing.PRWorkspaceImplementation
	case "validation":
		return eventing.PRWorkspaceValidation
	case "completion_audit":
		return eventing.PRWorkspaceCompletionAudit
	case "publication":
		return eventing.PRWorkspacePublication
	case "complete":
		return eventing.PRWorkspaceComplete
	default:
		return eventing.PRWorkspaceIntake
	}
}

func phaseForNudge(stage NudgeStage) eventing.PRWorkspacePhase {
	if stage == NudgeImplementationDone {
		return eventing.PRWorkspaceCompletionAudit
	}
	return eventing.PRWorkspaceReview
}

func correctionApplicabilityFlags(value CorrectionApplicability) (bool, bool) {
	switch value {
	case CorrectionReviewOnly:
		return true, false
	case CorrectionImplementationOnly:
		return false, true
	default:
		return true, true
	}
}

func correctionApplicability(review, implementation bool) CorrectionApplicability {
	if review && !implementation {
		return CorrectionReviewOnly
	}
	if implementation && !review {
		return CorrectionImplementationOnly
	}
	return CorrectionReviewAndImpl
}

func lessonPhases(value CorrectionApplicability) []eventing.PRWorkspacePhase {
	switch value {
	case CorrectionReviewOnly:
		return []eventing.PRWorkspacePhase{eventing.PRWorkspaceReview}
	case CorrectionImplementationOnly:
		return []eventing.PRWorkspacePhase{eventing.PRWorkspaceImplementation}
	default:
		return []eventing.PRWorkspacePhase{eventing.PRWorkspaceReview, eventing.PRWorkspaceImplementation}
	}
}

func applicabilityFromLessonPhases(values []eventing.PRWorkspacePhase) CorrectionApplicability {
	review, implementation := false, false
	for _, value := range values {
		review = review || value == eventing.PRWorkspaceReview
		implementation = implementation || value == eventing.PRWorkspaceImplementation
	}
	return correctionApplicability(review, implementation)
}

func publicationStatus(state ExecutionState) eventing.PRPublicationStatus {
	switch state {
	case ExecutionQueued:
		return eventing.PRPublicationPending
	case ExecutionSucceeded:
		return eventing.PRPublicationPublished
	case ExecutionUnknown, ExecutionRunning, ExecutionWaitingGate, ExecutionWaitingUser:
		return eventing.PRPublicationUnknown
	default:
		return eventing.PRPublicationFailed
	}
}

func executionStateForPublication(status eventing.PRPublicationStatus) ExecutionState {
	switch status {
	case eventing.PRPublicationPending:
		return ExecutionQueued
	case eventing.PRPublicationClaimed:
		return ExecutionRunning
	case eventing.PRPublicationPublished:
		return ExecutionSucceeded
	case eventing.PRPublicationUnknown:
		return ExecutionUnknown
	default:
		return ExecutionFailed
	}
}

func persistenceDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func timeFromPointer(value *time.Time, fallback time.Time) time.Time {
	if value != nil {
		return *value
	}
	return fallback
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneFloatPointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
