package prworkspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type IssuePublicationRequest struct {
	ProviderOrigin string
	RepositoryID   string
	Repository     string
	Title          string
	Body           string
	Labels         []string
	Marker         string
}

type IssuePublicationResult struct {
	ExternalID  string
	ExternalURL string
	Ambiguous   bool
}

const publicationRecoveryDelay = 2 * time.Minute

type issuePublicationPayload struct {
	ProviderOrigin string   `json:"provider_origin"`
	RepositoryID   string   `json:"repository_id"`
	Repository     string   `json:"repository"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	Labels         []string `json:"labels"`
	FindingIDs     []string `json:"finding_ids"`
}

type IssuePublisher interface {
	CreateIssue(ctx context.Context, request IssuePublicationRequest) (IssuePublicationResult, error)
	FindIssueByMarker(ctx context.Context, providerOrigin, repositoryID, repository, marker string) (IssuePublicationResult, bool, error)
}

type QueueDeferredPublicationRequest struct {
	WorkspaceID     string
	GroupID         string
	ExpectedVersion int64
	RequestID       string
}

func (service *Service) QueueDeferredPublication(ctx context.Context, request QueueDeferredPublicationRequest) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) || !validOpaqueID(request.GroupID, "pdg_") {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	publicationID := stableID("ppb_", aggregate.Workspace.ID, request.GroupID, request.RequestID)
	if existing, found := findPublication(aggregate.Publications, publicationID); found {
		if existing.Kind == PublicationGitHubIssue && existing.TargetID == request.GroupID {
			return aggregate, nil
		}
		return aggregate, ErrConflict
	}
	if service.deferredIssueMode == DeferredIssuesOff {
		return aggregate, ErrConflict
	}
	group, ok := findDeferredGroup(aggregate.DeferredGroups, request.GroupID)
	if !ok || group.PublicationID != "" || group.ExistingIssueURL != "" || len(group.FindingIDs) == 0 ||
		aggregate.Workspace.Version != request.ExpectedVersion || !aggregate.ProviderSnapshot.CanCreateIssue {
		return aggregate, ErrConflict
	}
	if !activeDeferredGroupValid(aggregate, group) {
		return aggregate, ErrConflict
	}
	now := service.now().UTC()
	publication, err := deferredPublication(aggregate, group, request.RequestID, now)
	if err != nil {
		return aggregate, err
	}
	var authorizationRequest issuePublicationPayload
	if err := decodePinnedPublicationPayload(publication, &authorizationRequest); err != nil {
		return aggregate, err
	}
	gateSubject := map[string]any{
		"group": group, "publication": publication, "request": authorizationRequest,
		"provider": map[string]any{"origin": aggregate.ProviderSnapshot.ProviderOrigin, "repository_id": aggregate.ProviderSnapshot.RepositoryID, "can_create_issue": aggregate.ProviderSnapshot.CanCreateIssue},
	}
	gate, err := service.deferredPublicationGate(ctx, aggregate, gateSubject)
	if err != nil {
		return Aggregate{}, err
	}
	gate.TargetID = group.ID
	patch := AggregatePatch{
		AppendGates:        []GateRun{gate},
		AppendPublications: []Publication{publication},
	}
	if gateCompletedWith(gate, "publish") {
		publication.State = ExecutionQueued
	} else {
		publication.State = ExecutionWaitingGate
	}
	patch.AppendPublications[0] = publication
	group.PublicationID = publication.ID
	group.PublicationSuppressed, group.SuppressionReason = false, ""
	group.Version++
	group.UpdatedAt = now
	patch.UpsertDeferred = []DeferredGroup{group}
	patch.Activity = []Activity{{Kind: "deferred.publication_queued", Actor: "user", EntityID: group.ID, Summary: "Deferred issue publication requested", CreatedAt: now}}
	result, err := service.store.Mutate(ctx, Mutation{WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion, RequestID: request.RequestID, Patch: patch})
	if err != nil {
		return result.Aggregate, err
	}
	return result.Aggregate, nil
}

type DispatchIssuePublicationRequest struct {
	WorkspaceID     string
	PublicationID   string
	ExpectedVersion int64
	RequestID       string
}

func (service *Service) DispatchIssuePublication(ctx context.Context, publisher IssuePublisher, request DispatchIssuePublicationRequest) (Aggregate, error) {
	if publisher == nil || !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		!validOpaqueID(request.PublicationID, "ppb_") {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	publication, found := findPublication(aggregate.Publications, request.PublicationID)
	if !found || publication.Kind != PublicationGitHubIssue || publication.State != ExecutionQueued || aggregate.Workspace.Version != request.ExpectedVersion {
		return aggregate, ErrConflict
	}
	group, ok := findDeferredGroup(aggregate.DeferredGroups, publication.TargetID)
	if !ok || group.PublicationID != publication.ID {
		return aggregate, ErrConflict
	}
	if service.deferredIssueMode == DeferredIssuesOff {
		now := service.now().UTC()
		publication.State, publication.PublicErrorCode, publication.UpdatedAt = ExecutionCanceled, "deferred_issue_publication_disabled", now
		group.PublicationID = ""
		group.Version++
		group.UpdatedAt = now
		canceled, cancelErr := service.store.Mutate(ctx, Mutation{
			WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
			RequestID: request.RequestID, Patch: AggregatePatch{
				ReplacePublications: []Publication{publication}, UpsertDeferred: []DeferredGroup{group},
				Activity: []Activity{{Kind: "issue.publication_canceled", Actor: "system", EntityID: publication.ID, Summary: "Deferred issue publication is disabled", CreatedAt: now}},
			},
		})
		return canceled.Aggregate, cancelErr
	}
	var payload issuePublicationPayload
	if err := decodePinnedPublicationPayload(publication, &payload); err != nil ||
		payload.RepositoryID != aggregate.ProviderSnapshot.RepositoryID ||
		payload.ProviderOrigin != aggregate.ProviderSnapshot.ProviderOrigin {
		return aggregate, ErrConflict
	}
	publication.State = ExecutionRunning
	publication.Attempts++
	publication.UpdatedAt = service.now().UTC()
	claimed, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID + ":claim", Patch: AggregatePatch{ReplacePublications: []Publication{publication}},
	})
	if err != nil {
		return claimed.Aggregate, err
	}
	marker := publicationMarker(publication)
	result, publishErr := publisher.CreateIssue(ctx, IssuePublicationRequest{
		ProviderOrigin: payload.ProviderOrigin,
		RepositoryID:   payload.RepositoryID, Repository: payload.Repository,
		Title: payload.Title, Body: payload.Body, Labels: payload.Labels, Marker: marker,
	})
	finished := service.now().UTC()
	publication.UpdatedAt = finished
	frozenProvider := ProviderSnapshot{ProviderOrigin: payload.ProviderOrigin, RepositoryID: payload.RepositoryID, Repository: payload.Repository}
	if publishErr == nil && !result.Ambiguous && result.ExternalID != "" &&
		validExistingDeferredIssueURL(frozenProvider, result.ExternalURL) {
		publication.State = ExecutionSucceeded
		publication.ExternalID, publication.ExternalURL, publication.PublishedAt = result.ExternalID, result.ExternalURL, &finished
	} else if result.Ambiguous || strings.TrimSpace(result.ExternalID) != "" || strings.TrimSpace(result.ExternalURL) != "" {
		publication.State = ExecutionUnknown
		publication.PublicErrorCode = "provider_outcome_unknown"
	} else {
		publication.State = ExecutionFailed
		publication.PublicErrorCode = "provider_issue_create_failed"
	}
	patch := AggregatePatch{ReplacePublications: []Publication{publication}, Activity: []Activity{{Kind: "issue.publication_finished", Actor: "system", EntityID: publication.ID, Summary: "Deferred issue publication finished", CreatedAt: finished}}}
	if publication.State == ExecutionSucceeded {
		rewarded := rewardDeferredFindings(claimed.Aggregate.Findings, payload.FindingIDs, "deferred_issue_published")
		for index := range rewarded {
			rewarded[index].UpdatedAt = finished
		}
		patch.UpsertFindings = rewarded
		patch.ReplaceNudgeRounds = recomputeNudgeRoundRewards(
			claimed.Aggregate.NudgeRounds,
			upsertByID(claimed.Aggregate.Findings, rewarded, func(value Finding) string { return value.ID }),
		)
	} else if publication.State == ExecutionFailed {
		group.PublicationID = ""
		group.Version++
		group.UpdatedAt = finished
		patch.UpsertDeferred = []DeferredGroup{group}
	}
	finalized, finalizeErr := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: claimed.Aggregate.Workspace.Version,
		RequestID: request.RequestID + ":finalize",
		Patch:     patch,
	})
	if finalizeErr != nil {
		return finalized.Aggregate, finalizeErr
	}
	if publishErr != nil {
		return finalized.Aggregate, publishErr
	}
	return finalized.Aggregate, nil
}

func (service *Service) deferredPublicationGate(ctx context.Context, aggregate Aggregate, subject map[string]any) (GateRun, error) {
	if service.deferredIssueMode != DeferredIssuesAutomatic {
		return service.startGate(ctx, aggregate, "pr.deferred.publish", subject)
	}
	digest, err := fingerprintValue(subject)
	if err != nil {
		return GateRun{}, err
	}
	entry, err := prLifecycleGateCatalogEntry("pr.deferred.publish")
	if err != nil {
		return GateRun{}, err
	}
	if entry.Gate.DefaultAction == nil || entry.Gate.DefaultAction.Type != "human" {
		return GateRun{}, errors.New("automatic deferred publication requires the published deferred gate")
	}
	now := service.now().UTC()
	actionRevision := prLifecyclePolicyRevision(entry.WorkflowRevision, "automatic-deferred-issues-v3")
	return GateRun{
		ID:            stableID("pgr_", aggregate.Workspace.ID, "pr.deferred.publish", digest),
		DecisionPoint: "pr.deferred.publish", State: ExecutionSucceeded,
		PolicyRevision: actionRevision, SubjectRevision: digest,
		Turns: []GateTurn{{
			StageID: "automatic", Kind: "deterministic", ActorKind: "deterministic", Status: "answered",
			ExecutionID:    stableID("ge_", aggregate.Workspace.ID, digest, "automatic-deferred"),
			ActionRevision: actionRevision, InputHash: digest,
			GateForm:    &GateForm{GateRef: entry.GateRef, Prompt: entry.Gate.Prompt, Fields: entry.Gate.Fields},
			FieldValues: map[string]any{"action": "publish"},
		}},
		Evidence: projectGateEvidence(subject), CreatedAt: now, FinishedAt: &now,
	}, nil
}

type ReconcileIssuePublicationRequest struct {
	WorkspaceID     string
	PublicationID   string
	ExpectedVersion int64
	RequestID       string
}

func (service *Service) ReconcileIssuePublication(ctx context.Context, publisher IssuePublisher, request ReconcileIssuePublicationRequest) (Aggregate, error) {
	if publisher == nil || !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) || !validOpaqueID(request.PublicationID, "ppb_") {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	publication, found := findPublication(aggregate.Publications, request.PublicationID)
	if !found || publication.Kind != PublicationGitHubIssue ||
		(publication.State != ExecutionUnknown && publication.State != ExecutionRunning) || aggregate.Workspace.Version != request.ExpectedVersion {
		return aggregate, ErrConflict
	}
	var payload issuePublicationPayload
	if decodeErr := decodePinnedPublicationPayload(publication, &payload); decodeErr != nil {
		return aggregate, decodeErr
	}
	if publication.State == ExecutionUnknown {
		authorized, proceed, authorizationErr := service.authorizePublicationReconciliation(
			ctx, aggregate, publication, payload, request.ExpectedVersion, request.RequestID,
			"Issue publication reconciliation authorized",
		)
		if authorizationErr != nil || !proceed {
			return authorized, authorizationErr
		}
		aggregate = authorized
		request.ExpectedVersion = aggregate.Workspace.Version
	}
	observed, exists, err := publisher.FindIssueByMarker(
		ctx, payload.ProviderOrigin, payload.RepositoryID, payload.Repository, publicationMarker(publication),
	)
	if err != nil {
		return service.recordUnknownIssuePublication(ctx, aggregate, publication, request, err)
	}
	frozenProvider := ProviderSnapshot{ProviderOrigin: payload.ProviderOrigin, RepositoryID: payload.RepositoryID, Repository: payload.Repository}
	if !exists || observed.Ambiguous || strings.TrimSpace(observed.ExternalID) == "" ||
		!validExistingDeferredIssueURL(frozenProvider, observed.ExternalURL) {
		return service.recordUnknownIssuePublication(
			ctx, aggregate, publication, request,
			errors.New("publication outcome remains unknown"),
		)
	}
	now := service.now().UTC()
	publication.State, publication.ExternalID, publication.ExternalURL = ExecutionSucceeded, observed.ExternalID, observed.ExternalURL
	publication.PublicErrorCode, publication.UpdatedAt, publication.PublishedAt = "", now, &now
	rewarded := rewardDeferredFindings(aggregate.Findings, payload.FindingIDs, "deferred_issue_reconciled")
	for index := range rewarded {
		rewarded[index].UpdatedAt = now
	}
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID, Patch: AggregatePatch{
			ReplacePublications: []Publication{publication}, UpsertFindings: rewarded,
			ReplaceNudgeRounds: recomputeNudgeRoundRewards(
				aggregate.NudgeRounds,
				upsertByID(aggregate.Findings, rewarded, func(value Finding) string { return value.ID }),
			),
		},
	})
	if err != nil {
		return result.Aggregate, err
	}
	return result.Aggregate, nil
}

func (service *Service) recordUnknownIssuePublication(
	ctx context.Context,
	aggregate Aggregate,
	publication Publication,
	request ReconcileIssuePublicationRequest,
	cause error,
) (Aggregate, error) {
	now := service.now().UTC()
	publication.State, publication.PublicErrorCode, publication.UpdatedAt = ExecutionUnknown, "provider_outcome_unknown", now
	mutated, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID, Patch: AggregatePatch{
			ReplacePublications: []Publication{publication},
			Activity:            []Activity{{Kind: "issue.publication_unknown", Actor: "system", EntityID: publication.ID, Summary: "Deferred issue publication outcome remains unknown", CreatedAt: now}},
		},
	})
	if err != nil {
		return mutated.Aggregate, err
	}
	return mutated.Aggregate, cause
}

func (service *Service) publicationRecoveryReady(publication Publication) bool {
	return !publication.UpdatedAt.IsZero() && !service.now().UTC().Before(publication.UpdatedAt.Add(publicationRecoveryDelay))
}

func deferredPublication(aggregate Aggregate, group DeferredGroup, requestID string, now time.Time) (Publication, error) {
	pinnedPayload, payload, err := encodePublicationPayload(issuePublicationPayload{
		ProviderOrigin: aggregate.ProviderSnapshot.ProviderOrigin,
		RepositoryID:   aggregate.ProviderSnapshot.RepositoryID,
		Repository:     aggregate.ProviderSnapshot.Repository,
		Title:          group.Title, Body: group.Body,
		Labels:     append([]string(nil), group.Labels...),
		FindingIDs: append([]string(nil), group.FindingIDs...),
	})
	if err != nil {
		return Publication{}, err
	}
	return Publication{
		ID: stableID("ppb_", aggregate.Workspace.ID, group.ID, requestID), Kind: PublicationGitHubIssue,
		State: ExecutionQueued, TargetID: group.ID, FindingIDs: append([]string(nil), group.FindingIDs...), PayloadDigest: payload,
		CreatedAt: now, UpdatedAt: now, payload: pinnedPayload,
	}, nil
}

func publicationMarker(publication Publication) string {
	return fmt.Sprintf("picoclaw-pr-publication:%s:%s", publication.ID, publication.PayloadDigest)
}

func findPublication(values []Publication, id string) (Publication, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return Publication{}, false
}
