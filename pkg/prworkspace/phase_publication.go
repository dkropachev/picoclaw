package prworkspace

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type ReviewPublicationRequest struct {
	Provider ProviderSnapshot
	Summary  string
	Findings []Finding
	Marker   string
}

type ReviewPublicationResult struct {
	ExternalID  string
	ExternalURL string
	Ambiguous   bool
}

type ReviewPublisher interface {
	PublishReview(ctx context.Context, request ReviewPublicationRequest) (ReviewPublicationResult, error)
	ReconcileReview(ctx context.Context, request ReviewPublicationRequest) (ReviewPublicationResult, bool, error)
}

type reviewPublicationPayload struct {
	Provider ProviderSnapshot `json:"provider"`
	Summary  string           `json:"summary"`
	Findings []Finding        `json:"findings"`
}

type BranchPublicationRequest struct {
	Provider   ProviderSnapshot
	Repair     RepairAttempt
	Validation ValidationRun
}

type BranchPublicationResult struct {
	ExternalID    string
	ExternalURL   string
	Ambiguous     bool
	PullRequestID string
	PullNumber    int64
	HeadRef       string
	HeadSHA       string
}

type BranchPublisher interface {
	PublishBranch(ctx context.Context, request BranchPublicationRequest) (BranchPublicationResult, error)
	ReconcileBranch(ctx context.Context, request BranchPublicationRequest) (BranchPublicationResult, bool, error)
}

type branchPublicationPayload struct {
	Provider   ProviderSnapshot                `json:"provider"`
	Repair     RepairAttempt                   `json:"repair"`
	Validation ValidationRun                   `json:"validation"`
	Fence      *ImplementationPublicationFence `json:"publication_fence"`
}

type QueueReviewPublicationRequest struct {
	WorkspaceID     string
	ExpectedVersion int64
	RequestID       string
	ExpectedHeadSHA string
	FindingIDs      []string
}

func (service *Service) QueueReviewPublication(
	ctx context.Context,
	request QueueReviewPublicationRequest,
) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		request.ExpectedHeadSHA == "" {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	publicationID := stableID("ppb_", aggregate.Workspace.ID, string(PublicationGitHubReview), request.RequestID)
	if existing, found := findPublication(aggregate.Publications, publicationID); found {
		if existing.Kind == PublicationGitHubReview && existing.ExpectedHeadSHA == request.ExpectedHeadSHA &&
			equalStringSets(existing.FindingIDs, request.FindingIDs) {
			return aggregate, nil
		}
		return aggregate, ErrConflict
	}
	if aggregate.Workspace.Version != request.ExpectedVersion ||
		aggregate.ProviderSnapshot.HeadSHA != request.ExpectedHeadSHA ||
		aggregate.ProviderSnapshot.State != "open" || !aggregate.ProviderSnapshot.CanReview {
		return aggregate, ErrConflict
	}
	stage, ok := latestSucceededStage(aggregate.StageRuns, "review", request.ExpectedHeadSHA)
	if !ok {
		return aggregate, ErrConflict
	}
	findings, err := publicationFindings(aggregate.Findings, request.FindingIDs)
	if err != nil {
		return aggregate, err
	}
	authorizationRequest := reviewPublicationPayload{
		Provider: aggregate.ProviderSnapshot,
		Summary:  stage.Summary,
		Findings: findings,
	}
	pinnedPayload, payload, err := encodePublicationPayload(authorizationRequest)
	if err != nil {
		return aggregate, err
	}
	now := service.now().UTC()
	publication := Publication{
		ID:   publicationID,
		Kind: PublicationGitHubReview, State: ExecutionQueued, TargetID: stage.ID,
		FindingIDs: findingIDs(findings), ExpectedHeadSHA: request.ExpectedHeadSHA,
		PayloadDigest: payload, CreatedAt: now, UpdatedAt: now, payload: pinnedPayload,
	}
	if publicationLocksHead(aggregate.Publications, PublicationGitHubReview, request.ExpectedHeadSHA) {
		return aggregate, ErrConflict
	}
	gate, gateNew, err := service.ensureGate(ctx, aggregate, "pr.review.publish", map[string]any{
		"publication": publication, "request": authorizationRequest,
		"provider_revision": aggregate.ProviderSnapshot.ProviderRevision,
	})
	if err != nil {
		return aggregate, err
	}
	gate.TargetID = publication.ID
	patch := AggregatePatch{AppendPublications: []Publication{publication}}
	if gateNew {
		patch.AppendGates = []GateRun{gate}
	}
	if !gateCompletedWith(gate, "publish") {
		publication.State = ExecutionWaitingGate
		patch.AppendPublications[0] = publication
		state := ExecutionWaitingGate
		patch.ExecutionState = &state
	}
	patch.Activity = []Activity{
		{
			Kind:      "review.publication_requested",
			Actor:     "user",
			EntityID:  publication.ID,
			Summary:   "GitHub review publication requested",
			CreatedAt: now,
		},
	}
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID, Patch: patch,
	})
	return result.Aggregate, err
}

type QueueBranchPublicationRequest struct {
	WorkspaceID     string
	ExpectedVersion int64
	RequestID       string
	ExpectedHeadSHA string
}

func (service *Service) QueueBranchPublication(
	ctx context.Context,
	request QueueBranchPublicationRequest,
) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		request.ExpectedHeadSHA == "" {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	publicationID := stableID("ppb_", aggregate.Workspace.ID, string(PublicationBranchPush), request.RequestID)
	if existing, found := findPublication(aggregate.Publications, publicationID); found {
		if existing.Kind == PublicationBranchPush && existing.ExpectedHeadSHA == request.ExpectedHeadSHA {
			return aggregate, nil
		}
		return aggregate, ErrConflict
	}
	if aggregate.Workspace.Version != request.ExpectedVersion || aggregate.Workspace.Phase != PhasePublication ||
		aggregate.ProviderSnapshot.HeadSHA != request.ExpectedHeadSHA || aggregate.ProviderSnapshot.State != "open" ||
		!aggregate.ProviderSnapshot.HeadWritable {
		return aggregate, ErrConflict
	}
	repair, ok := latestPublishableRepair(aggregate, request.ExpectedHeadSHA)
	if !ok {
		return aggregate, ErrConflict
	}
	completionGate, ok := passedImplementationCompletionGate(aggregate.Gates, repair.ID)
	if !ok {
		return aggregate, ErrConflict
	}
	fresh, freshnessErr := implementationCompletionGateFresh(aggregate, completionGate)
	if freshnessErr != nil {
		return aggregate, freshnessErr
	}
	if !fresh {
		return service.invalidateImplementationCompletionGate(ctx, RespondGateRequest{
			WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion, RequestID: request.RequestID,
		}, aggregate, completionGate)
	}
	validation, ok := latestValidationForRepair(aggregate.ValidationRuns, repair)
	if !ok || validation.State != ExecutionSucceeded {
		return aggregate, ErrConflict
	}
	authorizationRequest := branchPublicationPayload{
		Provider:   aggregate.ProviderSnapshot,
		Repair:     repair,
		Validation: validation,
		Fence:      repair.PublicationFence,
	}
	pinnedPayload, payload, err := encodePublicationPayload(authorizationRequest)
	if err != nil {
		return aggregate, err
	}
	now := service.now().UTC()
	publication := Publication{
		ID:   publicationID,
		Kind: PublicationBranchPush, State: ExecutionQueued, TargetID: repair.ID,
		ExpectedHeadSHA: request.ExpectedHeadSHA, PayloadDigest: payload,
		CreatedAt: now, UpdatedAt: now, payload: pinnedPayload,
	}
	if publicationLocksHead(aggregate.Publications, PublicationBranchPush, request.ExpectedHeadSHA) {
		return aggregate, ErrConflict
	}
	gateSubject := map[string]any{
		"publication": publication, "request": authorizationRequest,
		"provider_revision": aggregate.ProviderSnapshot.ProviderRevision,
	}
	gateSubject["validation"] = validation
	gate, gateNew, err := service.ensureGate(ctx, aggregate, "pr.implementation.publish", gateSubject)
	if err != nil {
		return aggregate, err
	}
	gate.TargetID = publication.ID
	patch := AggregatePatch{AppendPublications: []Publication{publication}}
	if gateNew {
		patch.AppendGates = []GateRun{gate}
	}
	if !gateCompletedWith(gate, "publish") {
		publication.State = ExecutionWaitingGate
		patch.AppendPublications[0] = publication
		state := ExecutionWaitingGate
		patch.ExecutionState = &state
	}
	patch.Activity = []Activity{
		{
			Kind:      "branch.publication_requested",
			Actor:     "user",
			EntityID:  publication.ID,
			Summary:   "PR branch publication requested",
			CreatedAt: now,
		},
	}
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID, Patch: patch,
	})
	return result.Aggregate, err
}

type DispatchPhasePublicationRequest struct {
	WorkspaceID     string
	PublicationID   string
	ExpectedVersion int64
	RequestID       string
}

func (service *Service) DispatchReviewPublication(
	ctx context.Context,
	publisher ReviewPublisher,
	request DispatchPhasePublicationRequest,
) (Aggregate, error) {
	if publisher == nil {
		return Aggregate{}, ErrInvalid
	}
	return service.dispatchPhasePublication(
		ctx,
		request,
		func(aggregate Aggregate, publication Publication) (phasePublicationResult, error) {
			var payload reviewPublicationPayload
			if err := decodePinnedPublicationPayload(publication, &payload); err != nil {
				return phasePublicationResult{}, err
			}
			if payload.Provider.HeadSHA != publication.ExpectedHeadSHA {
				return phasePublicationResult{}, ErrConflict
			}
			result, err := publisher.PublishReview(ctx, ReviewPublicationRequest{
				Provider: payload.Provider, Summary: payload.Summary,
				Findings: payload.Findings, Marker: phasePublicationMarker(publication),
			})
			return phasePublicationResult{
				externalID:  result.ExternalID,
				externalURL: result.ExternalURL,
				ambiguous:   result.Ambiguous,
			}, err
		},
	)
}

func (service *Service) DispatchBranchPublication(
	ctx context.Context,
	publisher BranchPublisher,
	request DispatchPhasePublicationRequest,
) (Aggregate, error) {
	if publisher == nil {
		return Aggregate{}, ErrInvalid
	}
	return service.dispatchPhasePublication(
		ctx,
		request,
		func(aggregate Aggregate, publication Publication) (phasePublicationResult, error) {
			var payload branchPublicationPayload
			if err := decodePinnedPublicationPayload(publication, &payload); err != nil ||
				payload.Fence == nil || payload.Provider.HeadSHA != publication.ExpectedHeadSHA {
				return phasePublicationResult{}, ErrConflict
			}
			payload.Repair.PublicationFence = payload.Fence
			result, err := publisher.PublishBranch(
				ctx,
				BranchPublicationRequest{
					Provider: payload.Provider, Repair: payload.Repair, Validation: payload.Validation,
				},
			)
			phaseResult := phasePublicationResult{
				externalID:  result.ExternalID,
				externalURL: result.ExternalURL,
				ambiguous:   result.Ambiguous,
			}
			if err == nil && payload.Provider.Intent == IntentImplementFeature && result.PullNumber > 0 &&
				result.PullRequestID != "" && result.HeadRef != "" && result.HeadSHA != "" {
				updated := payload.Provider
				updated.PullRequestID, updated.PullNumber = result.PullRequestID, result.PullNumber
				updated.HeadRef, updated.HeadSHA = result.HeadRef, result.HeadSHA
				updated.ProviderRevision = "published:" + result.ExternalID
				updated.ObservedAt = service.now().UTC()
				phaseResult.provider = &updated
			}
			return phaseResult, err
		},
	)
}

type phasePublicationResult struct {
	externalID  string
	externalURL string
	ambiguous   bool
	provider    *ProviderSnapshot
}

func (service *Service) dispatchPhasePublication(
	ctx context.Context,
	request DispatchPhasePublicationRequest,
	publish func(Aggregate, Publication) (phasePublicationResult, error),
) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		!validOpaqueID(request.PublicationID, "ppb_") {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	publication, ok := findPublication(aggregate.Publications, request.PublicationID)
	if !ok || publication.State != ExecutionQueued || aggregate.Workspace.Version != request.ExpectedVersion ||
		publication.ExpectedHeadSHA == "" {
		return aggregate, ErrConflict
	}
	if publication.ExpectedHeadSHA != aggregate.ProviderSnapshot.HeadSHA {
		now := service.now().UTC()
		publication.State, publication.PublicErrorCode, publication.UpdatedAt = ExecutionStale, "provider_revision_changed", now
		staled, staleErr := service.store.Mutate(ctx, Mutation{
			WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
			RequestID: request.RequestID, Patch: AggregatePatch{
				ReplacePublications: []Publication{publication},
				Activity: []Activity{
					{
						Kind:      "publication.stale",
						Actor:     "system",
						EntityID:  publication.ID,
						Summary:   "Publication head no longer matches the provider",
						CreatedAt: now,
					},
				},
			},
		})
		return staled.Aggregate, staleErr
	}
	if publication.Kind == PublicationBranchPush {
		completionGate, found := passedImplementationCompletionGate(aggregate.Gates, publication.TargetID)
		if !found {
			return aggregate, ErrConflict
		}
		fresh, freshnessErr := implementationCompletionGateFresh(aggregate, completionGate)
		if freshnessErr != nil {
			return aggregate, freshnessErr
		}
		if !fresh {
			return service.invalidateImplementationCompletionGate(ctx, RespondGateRequest{
				WorkspaceID:     request.WorkspaceID,
				ExpectedVersion: request.ExpectedVersion,
				RequestID:       request.RequestID,
			}, aggregate, completionGate)
		}
	}
	publication.State, publication.Attempts, publication.UpdatedAt = ExecutionRunning, publication.Attempts+1, service.now().
		UTC()
	claimed, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID + ":claim", Patch: AggregatePatch{ReplacePublications: []Publication{publication}},
	})
	if err != nil {
		return claimed.Aggregate, err
	}
	result, publishErr := publish(claimed.Aggregate, publication)
	finished := service.now().UTC()
	publication.UpdatedAt = finished
	success := publishErr == nil && !result.ambiguous && strings.TrimSpace(result.externalID) != "" &&
		validPhasePublicationURL(publication, result.externalURL)
	switch {
	case success:
		publication.State, publication.ExternalID, publication.ExternalURL = ExecutionSucceeded, result.externalID, result.externalURL
		publication.PublicErrorCode, publication.PublishedAt = "", &finished
	case result.ambiguous || strings.TrimSpace(result.externalID) != "" || strings.TrimSpace(result.externalURL) != "":
		publication.State, publication.PublicErrorCode = ExecutionUnknown, "provider_outcome_unknown"
	default:
		publication.State = ExecutionFailed
		if publication.Kind == PublicationGitHubReview {
			publication.PublicErrorCode = "provider_review_publish_failed"
		} else {
			publication.PublicErrorCode = "provider_branch_publish_failed"
		}
	}
	patch := AggregatePatch{
		ReplacePublications: []Publication{publication},
		Activity: []Activity{
			{
				Kind:      "publication.finished",
				Actor:     "system",
				EntityID:  publication.ID,
				Summary:   "PR publication finished",
				CreatedAt: finished,
			},
		},
	}
	if success && result.provider != nil {
		patch.Provider = result.provider
	}
	if success && publication.Kind == PublicationBranchPush {
		phase, state := PhaseComplete, ExecutionSucceeded
		patch.Phase, patch.ExecutionState = &phase, &state
	}
	finalized, finalizeErr := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: claimed.Aggregate.Workspace.Version,
		RequestID: request.RequestID + ":finalize", Patch: patch,
		branchPublicationLeaseID: branchPublicationLeaseID(publication),
	})
	if finalizeErr != nil {
		return finalized.Aggregate, finalizeErr
	}
	if publishErr != nil {
		return finalized.Aggregate, publishErr
	}
	return finalized.Aggregate, nil
}

type ReconcilePhasePublicationRequest struct {
	WorkspaceID     string
	PublicationID   string
	ExpectedVersion int64
	RequestID       string
}

func (service *Service) ReconcilePhasePublication(
	ctx context.Context,
	review ReviewPublisher,
	branch BranchPublisher,
	request ReconcilePhasePublicationRequest,
) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		!validOpaqueID(request.PublicationID, "ppb_") {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	publication, ok := findPublication(aggregate.Publications, request.PublicationID)
	if !ok || aggregate.Workspace.Version != request.ExpectedVersion ||
		(publication.State != ExecutionUnknown && publication.State != ExecutionRunning) {
		return aggregate, ErrConflict
	}
	if publication.State == ExecutionUnknown {
		authorizationRequest, requestErr := publicationAuthorizationRequest(publication)
		if requestErr != nil {
			return aggregate, requestErr
		}
		authorized, proceed, authorizationErr := service.authorizePublicationReconciliation(
			ctx, aggregate, publication, authorizationRequest, request.ExpectedVersion, request.RequestID,
			"PR publication reconciliation authorized",
		)
		if authorizationErr != nil || !proceed {
			return authorized, authorizationErr
		}
		aggregate = authorized
		request.ExpectedVersion = aggregate.Workspace.Version
	}
	var result phasePublicationResult
	var exists bool
	switch publication.Kind {
	case PublicationGitHubReview:
		if review == nil {
			return aggregate, errors.New("GitHub review reconciler is unavailable")
		}
		var payload reviewPublicationPayload
		if decodeErr := decodePinnedPublicationPayload(publication, &payload); decodeErr != nil ||
			payload.Provider.HeadSHA != publication.ExpectedHeadSHA {
			return aggregate, ErrConflict
		}
		observed, found, observeErr := review.ReconcileReview(ctx, ReviewPublicationRequest{
			Provider: payload.Provider, Summary: payload.Summary,
			Findings: payload.Findings, Marker: phasePublicationMarker(publication),
		})
		if observeErr != nil {
			return service.recordUnknownPhasePublication(ctx, aggregate, publication, request, observeErr)
		}
		result, exists = phasePublicationResult{
			externalID:  observed.ExternalID,
			externalURL: observed.ExternalURL,
			ambiguous:   observed.Ambiguous,
		}, found
	case PublicationBranchPush:
		if branch == nil {
			return aggregate, errors.New("branch reconciler is unavailable")
		}
		var payload branchPublicationPayload
		if decodeErr := decodePinnedPublicationPayload(publication, &payload); decodeErr != nil ||
			payload.Fence == nil || payload.Provider.HeadSHA != publication.ExpectedHeadSHA {
			return aggregate, ErrConflict
		}
		payload.Repair.PublicationFence = payload.Fence
		observed, found, observeErr := branch.ReconcileBranch(
			ctx,
			BranchPublicationRequest{
				Provider: payload.Provider, Repair: payload.Repair, Validation: payload.Validation,
			},
		)
		if observeErr != nil {
			return service.recordUnknownPhasePublication(ctx, aggregate, publication, request, observeErr)
		}
		result, exists = phasePublicationResult{
			externalID:  observed.ExternalID,
			externalURL: observed.ExternalURL,
			ambiguous:   observed.Ambiguous,
		}, found
		if found && payload.Provider.Intent == IntentImplementFeature && observed.PullNumber > 0 &&
			observed.PullRequestID != "" && observed.HeadRef != "" && observed.HeadSHA != "" {
			updated := payload.Provider
			updated.PullRequestID, updated.PullNumber = observed.PullRequestID, observed.PullNumber
			updated.HeadRef, updated.HeadSHA = observed.HeadRef, observed.HeadSHA
			updated.ProviderRevision = "published:" + observed.ExternalID
			updated.ObservedAt = service.now().UTC()
			result.provider = &updated
		}
	default:
		return aggregate, ErrInvalid
	}
	if !exists || strings.TrimSpace(result.externalID) == "" || result.ambiguous ||
		!validPhasePublicationURL(publication, result.externalURL) {
		if publication.Kind == PublicationBranchPush && publication.State == ExecutionRunning &&
			service.publicationRecoveryReady(publication) && !exists && !result.ambiguous {
			publication.State = ExecutionQueued
			publication.PublicErrorCode = ""
			publication.UpdatedAt = service.now().UTC()
			requeued, requeueErr := service.store.Mutate(ctx, Mutation{
				WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
				RequestID: request.RequestID, Patch: AggregatePatch{
					ReplacePublications: []Publication{publication},
					Activity: []Activity{
						{
							Kind:      "publication.recovered",
							Actor:     "system",
							EntityID:  publication.ID,
							Summary:   "Requeued an interrupted PR publication",
							CreatedAt: publication.UpdatedAt,
						},
					},
				},
				branchPublicationLeaseID: branchPublicationLeaseID(publication),
			})
			return requeued.Aggregate, requeueErr
		}
		return service.recordUnknownPhasePublication(
			ctx, aggregate, publication, request,
			errors.New("publication outcome remains unknown"),
		)
	}
	now := service.now().UTC()
	publication.State, publication.ExternalID, publication.ExternalURL = ExecutionSucceeded, result.externalID, result.externalURL
	publication.PublicErrorCode, publication.UpdatedAt, publication.PublishedAt = "", now, &now
	patch := AggregatePatch{ReplacePublications: []Publication{publication}}
	if result.provider != nil {
		patch.Provider = result.provider
	}
	if publication.Kind == PublicationBranchPush {
		phase, state := PhaseComplete, ExecutionSucceeded
		patch.Phase, patch.ExecutionState = &phase, &state
	}
	mutated, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID, Patch: patch,
		branchPublicationLeaseID: branchPublicationLeaseID(publication),
	})
	return mutated.Aggregate, err
}

func (service *Service) recordUnknownPhasePublication(
	ctx context.Context,
	aggregate Aggregate,
	publication Publication,
	request ReconcilePhasePublicationRequest,
	cause error,
) (Aggregate, error) {
	now := service.now().UTC()
	publication.State = ExecutionUnknown
	publication.PublicErrorCode = "provider_outcome_unknown"
	publication.UpdatedAt = now
	mutated, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID, Patch: AggregatePatch{
			ReplacePublications: []Publication{publication},
			Activity: []Activity{
				{
					Kind:      "publication.unknown",
					Actor:     "system",
					EntityID:  publication.ID,
					Summary:   "PR publication outcome remains unknown",
					CreatedAt: now,
				},
			},
		},
		branchPublicationLeaseID: branchPublicationLeaseID(publication),
	})
	if err != nil {
		return mutated.Aggregate, err
	}
	return mutated.Aggregate, cause
}

func branchPublicationLeaseID(publication Publication) string {
	if publication.Kind == PublicationBranchPush {
		return publication.ID
	}
	return ""
}

func publicationAuthorizationRequest(publication Publication) (any, error) {
	switch publication.Kind {
	case PublicationGitHubReview:
		var value reviewPublicationPayload
		if err := decodePinnedPublicationPayload(publication, &value); err != nil {
			return nil, err
		}
		return value, nil
	case PublicationBranchPush:
		var value branchPublicationPayload
		if err := decodePinnedPublicationPayload(publication, &value); err != nil {
			return nil, err
		}
		return value, nil
	default:
		return nil, ErrInvalid
	}
}

func validPhasePublicationURL(publication Publication, raw string) bool {
	if raw == "" {
		return true
	}
	var provider ProviderSnapshot
	switch publication.Kind {
	case PublicationGitHubReview:
		var payload reviewPublicationPayload
		if decodePinnedPublicationPayload(publication, &payload) != nil {
			return false
		}
		provider = payload.Provider
	case PublicationBranchPush:
		var payload branchPublicationPayload
		if decodePinnedPublicationPayload(publication, &payload) != nil {
			return false
		}
		provider = payload.Provider
	default:
		return false
	}
	external, err := url.Parse(raw)
	if err != nil || external.Scheme != "https" || external.Host == "" || external.User != nil ||
		external.RawQuery != "" {
		return false
	}
	origin, err := url.ParseRequestURI(provider.ProviderOrigin)
	if err != nil || !strings.EqualFold(external.Scheme, origin.Scheme) ||
		!strings.EqualFold(external.Host, origin.Host) {
		return false
	}
	wantedPath := strings.TrimSuffix(
		origin.Path,
		"/",
	) + "/" + provider.Repository + "/pull/"
	if provider.Intent == IntentImplementFeature && provider.PullNumber == 0 {
		if !strings.HasPrefix(external.Path, wantedPath) {
			return false
		}
		number, parseErr := strconv.ParseInt(strings.TrimPrefix(external.Path, wantedPath), 10, 64)
		return parseErr == nil && number > 0
	}
	wantedPath += strconv.FormatInt(
		provider.PullNumber,
		10,
	)
	return external.Path == wantedPath
}

func latestSucceededStage(values []StageRun, stage, head string) (StageRun, bool) {
	for index := len(values) - 1; index >= 0; index-- {
		if values[index].Stage == stage && values[index].State == ExecutionSucceeded && values[index].HeadSHA == head {
			return values[index], true
		}
	}
	return StageRun{}, false
}

func publicationFindings(values []Finding, ids []string) ([]Finding, error) {
	seen := make(map[string]struct{}, len(ids))
	result := make([]Finding, 0, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			return nil, ErrInvalid
		}
		seen[id] = struct{}{}
		finding, index := findFinding(values, id)
		if index < 0 || finding.Disposition != FindingInScope {
			return nil, fmt.Errorf("%w: finding %s is not publishable", ErrInvalid, id)
		}
		result = append(result, finding)
	}
	return result, nil
}

func equalStringSets(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy, rightCopy := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] || index > 0 && leftCopy[index] == leftCopy[index-1] {
			return false
		}
	}
	return true
}

func publicationLocksHead(values []Publication, kind PublicationKind, head string) bool {
	for _, publication := range values {
		if publication.Kind != kind || publication.ExpectedHeadSHA != head {
			continue
		}
		switch publication.State {
		case ExecutionQueued,
			ExecutionRunning,
			ExecutionWaitingGate,
			ExecutionWaitingUser,
			ExecutionUnknown,
			ExecutionSucceeded:
			return true
		}
	}
	return false
}

func latestPublishableRepair(aggregate Aggregate, head string) (RepairAttempt, bool) {
	charter, hasCharter := aggregate.ActiveCharter()
	for _, finding := range currentContextFindings(aggregate, charter, hasCharter) {
		if finding.Disposition != FindingFixed && HardCandidateScopeBlocker(finding.Scope) {
			return RepairAttempt{}, false
		}
	}
	for index := len(aggregate.RepairAttempts) - 1; index >= 0; index-- {
		repair := aggregate.RepairAttempts[index]
		if repair.State != ExecutionSucceeded || repair.CandidateSHA == "" || repair.PublicationFence == nil ||
			repair.PublicationFence.BaseCommit != head {
			continue
		}
		if HardCandidateScopeBlocker(repair.Scope) {
			continue
		}
		if hardScopeGateTargetsRepair(aggregate.Gates, repair.ID) {
			continue
		}
		stage, ok := findStageRun(aggregate.StageRuns, repair.StageRunID)
		if ok && stage.State == ExecutionSucceeded && stage.HeadSHA == head {
			return repair, true
		}
	}
	return RepairAttempt{}, false
}

func hardScopeGateTargetsRepair(gates []GateRun, repairID string) bool {
	for _, gate := range gates {
		if gate.DecisionPoint == "pr.implementation.hard-scope" && gate.TargetID == repairID &&
			gate.Evidence.HardScope {
			return true
		}
	}
	return false
}

func latestValidationForRepair(values []ValidationRun, repair RepairAttempt) (ValidationRun, bool) {
	validatedCandidate := repair.CandidateSHA
	if repair.PublicationFence != nil {
		// Validation runs before finalization, so it attests the candidate tree.
		// Finalization then replaces CandidateSHA with the retained commit and
		// records the immutable commit/tree tuple in the publication fence.
		// Require that tuple to be internally consistent and match validation
		// against its tree instead of comparing two different Git object types.
		if repair.PublicationFence.Tip != repair.CandidateSHA || repair.PublicationFence.Tree == "" {
			return ValidationRun{}, false
		}
		validatedCandidate = repair.PublicationFence.Tree
	}
	if validatedCandidate == "" {
		return ValidationRun{}, false
	}
	for index := len(values) - 1; index >= 0; index-- {
		if values[index].StageRunID == repair.StageRunID && values[index].CandidateSHA == validatedCandidate {
			return values[index], true
		}
	}
	return ValidationRun{}, false
}

func phasePublicationMarker(publication Publication) string {
	return fmt.Sprintf("picoclaw-pr-publication:%s:%s", publication.ID, publication.PayloadDigest)
}
