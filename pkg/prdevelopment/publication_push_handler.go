package prdevelopment

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

var errPublicationPushCorrupt = errors.New("publication push state is inconsistent")

// PublicationPinnedLinePusher grants exactly one parked-line compare-and-swap
// operation. It exposes neither workspace inventory mutation nor generic Git.
type PublicationPinnedLinePusher interface {
	PushPinnedLine(
		ctx context.Context,
		request gitworkspace.PinnedLinePushRequest,
	) (gitworkspace.PinnedLinePushResult, error)
}

// PublicationPushReadyStore is the complete and deliberately narrow durable
// authority required by one already-claimed push_ready record. It cannot claim
// other work, change gates, admit workflow runs, or reconcile unknown effects.
type PublicationPushReadyStore interface {
	eventing.PRDevelopmentPublicationPushClaimAuthenticator
	GetPRDevelopmentPublication(
		ctx context.Context,
		publicationID string,
	) (eventing.PRDevelopmentPublication, error)
	RenewPRDevelopmentPublication(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationRenew,
	) error
	RequeuePRDevelopmentPublication(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationRequeue,
	) (eventing.PRDevelopmentPublication, bool, error)
	CompletePRDevelopmentPublicationPrestart(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationPrestartCompletion,
	) (eventing.PRDevelopmentPublication, bool, error)
	RenewPRDevelopmentPublicationPush(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationRenew,
	) error
	StartPRDevelopmentPublicationPush(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationPushStart,
	) (eventing.PRDevelopmentPublication, bool, error)
	FinalizePRDevelopmentPublicationPush(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationPushFinalize,
	) (eventing.PRDevelopmentPublication, bool, error)
}

type PublicationPushReadyHandlerConfig struct {
	Store         PublicationPushReadyStore   `json:"-"`
	Provider      PublicationProviderObserver `json:"-"`
	Pusher        PublicationPinnedLinePusher `json:"-"`
	LeaseDuration time.Duration               `json:"-"`
	Now           func() time.Time            `json:"-"`
}

// PublicationPushReadyHandler crosses the publication effect boundary only
// after an exact durable write-ahead start. Every historical start replay is
// effect-inert: newlyStarted=false can finalize uncertainty, but never call Git.
type PublicationPushReadyHandler struct {
	store         PublicationPushReadyStore
	provider      PublicationProviderObserver
	pusher        PublicationPinnedLinePusher
	leaseDuration time.Duration
	now           func() time.Time
}

func NewPublicationPushReadyHandler(
	config PublicationPushReadyHandlerConfig,
) (*PublicationPushReadyHandler, error) {
	if config.Store == nil || isNilServiceValue(config.Store) ||
		config.Provider == nil || isNilServiceValue(config.Provider) ||
		config.Pusher == nil || isNilServiceValue(config.Pusher) {
		return nil, fmt.Errorf("%w: publication push services are required", ErrUnavailable)
	}
	lease := config.LeaseDuration
	if lease <= 0 {
		lease = defaultPublicationGateClaimLease
	}
	if lease < minimumPublicationGateClaimLease {
		return nil, ErrInvalidRequest
	}
	return &PublicationPushReadyHandler{
		store: config.Store, provider: config.Provider, pusher: config.Pusher,
		leaseDuration: lease, now: config.Now,
	}, nil
}

// HandlePushReadyClaim implements the push-ready dispatcher contract.
func (handler *PublicationPushReadyHandler) HandlePushReadyClaim(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) error {
	if handler == nil || handler.store == nil || isNilServiceValue(handler.store) ||
		handler.provider == nil || isNilServiceValue(handler.provider) ||
		handler.pusher == nil || isNilServiceValue(handler.pusher) {
		return ErrUnavailable
	}
	if validatePublicationPushReadyClaim(claim) != nil {
		return ErrInvalidRequest
	}
	ctx = ctxOrBackground(ctx)
	queueHeartbeat, renewErr := startPublicationGateHeartbeat(
		ctx,
		handler.store,
		claim,
		handler.leaseDuration,
	)
	if renewErr != nil {
		return handler.finishQueueRenewFailure(ctx, claim, renewErr)
	}
	authentication, authErr := handler.store.AuthenticateClaimedPRDevelopmentPublicationPush(
		queueHeartbeat.Context(),
		claim.ID,
		claim.ClaimToken,
		claim.ClaimEpoch,
	)
	if authErr != nil {
		if stopErr := queueHeartbeat.Stop(); stopErr != nil {
			return handler.finishQueueRenewFailure(ctx, claim, stopErr)
		}
		return handler.finishPrestartFailure(ctx, claim, authErr)
	}
	if validatePublicationPushAuthentication(claim, authentication) != nil {
		if stopErr := queueHeartbeat.Stop(); stopErr != nil {
			return handler.finishQueueRenewFailure(ctx, claim, stopErr)
		}
		return handler.completePrestartRecovery(
			ctx,
			claim,
			"publication push authentication is inconsistent",
		)
	}
	authoritative := authentication.Publication
	timed, observeErr := handler.provider.ObservePublication(
		queueHeartbeat.Context(),
		authentication.Case,
		authentication.ThreadIdentity,
	)
	if observeErr == nil {
		observeErr = validatePublicationPushObservation(authoritative, timed)
	}
	if stopErr := queueHeartbeat.Stop(); stopErr != nil {
		return handler.finishQueueRenewFailure(ctx, claim, stopErr)
	}
	if observeErr != nil {
		return handler.finishPrestartFailure(ctx, claim, observeErr)
	}
	if err := ctx.Err(); err != nil {
		return handler.requeuePrestart(ctx, claim, err)
	}
	startInput := eventing.PRDevelopmentPublicationPushStart{
		PublicationID: claim.ID,
		ClaimToken:    claim.ClaimToken,
		ClaimEpoch:    claim.ClaimEpoch,
		Observation:   timed.Observation,
		ObservedAt:    timed.ObservedAt,
		Request: publicationPushRequest(
			authoritative,
			timed.Observation.HeadSHA,
		),
	}
	started, newlyStarted, startErr := handler.startPushWithExactReplay(ctx, startInput)
	if startErr != nil {
		return handler.finishPrestartFailure(ctx, claim, startErr)
	}
	if !newlyStarted {
		return handler.finishHistoricalStart(ctx, claim, started, startInput)
	}
	if validateStartedPublication(claim, authoritative, started, startInput) != nil {
		if started.PushRequestHash == "" {
			return errPublicationPushCorrupt
		}
		return handler.finalizeStarted(
			ctx,
			claim,
			started,
			publicationPushRecoveryFinalization(started, "publication push start is inconsistent"),
		)
	}
	pushHeartbeat, pushRenewErr := startPublicationPushHeartbeat(
		ctx,
		handler.store,
		started,
		claim,
		handler.leaseDuration,
	)
	if pushRenewErr != nil {
		finalizeErr := handler.finalizeStarted(
			ctx,
			claim,
			started,
			publicationPushUnknownFinalization(started, "publication push lease was not established"),
		)
		if finalizeErr == nil {
			return nil
		}
		return errors.Join(pushRenewErr, finalizeErr)
	}
	gitResult, pushErr := handler.pusher.PushPinnedLine(
		pushHeartbeat.Context(),
		gitPushRequest(started.PushRequest),
	)
	if heartbeatErr := pushHeartbeat.Stop(); heartbeatErr != nil {
		pushErr = errors.Join(pushErr, heartbeatErr, gitworkspace.ErrPinnedLinePushOutcomeUnknown)
	}
	finalization := classifyPublicationPushOutcome(started, gitResult, pushErr)
	return handler.finalizeStarted(ctx, claim, started, finalization)
}

func validatePublicationPushReadyClaim(claim eventing.PRDevelopmentPublication) error {
	if validatePublicationDispatchClaim(claim) != nil ||
		claim.ClaimFrom != eventing.PRDevelopmentPublicationPushReady ||
		!validCaseID(claim.CaseID) || !validDevelopmentID(claim.ThreadID, "pdt_") ||
		!validDevelopmentID(claim.ControllerID, "pctl_") ||
		!validRepairSessionID(claim.OwnerSessionID) ||
		!validDevelopmentID(claim.AttemptID, "pdr_") ||
		!validDevelopmentID(claim.AttemptLedgerEntryID, "pdle_") ||
		!validDevelopmentID(claim.ReviewLedgerEntryID, "pdle_") ||
		!validPublicationGateImmutableEvidence(claim) ||
		claim.PolicyRevision == "" || claim.SubjectRevision == "" ||
		validateCompletePublicationProviderPin(claim) != nil ||
		validatePublicationGatePinProgression(claim) != nil ||
		claim.ExpectedRemoteTip != "" || claim.Attempts != 0 ||
		claim.PushRequest != (eventing.PRDevelopmentPublicationPushRequest{}) ||
		len(claim.PushRequestJSON) != 0 || claim.PushRequestHash != "" ||
		claim.PushResult != (eventing.PRDevelopmentPublicationPushResult{}) ||
		len(claim.PushResultJSON) != 0 || claim.PushResultHash != "" ||
		claim.LastErrorCode != "" || claim.LastErrorDetail != "" {
		return ErrInvalidRequest
	}
	policy, found, err := decodePublicationGatePolicy(claim)
	if err != nil || !found {
		return ErrInvalidRequest
	}
	if policy.IsNoop() {
		_, found, err = decodePublicationZeroSubject(claim, policy)
		if err != nil || !found || claim.DecisionRunID != "" {
			return ErrInvalidRequest
		}
		return nil
	}
	_, _, found, err = decodePublicationActiveSubject(claim, policy)
	if err != nil || !found || !validDevelopmentID(claim.DecisionRunID, "wr_") {
		return ErrInvalidRequest
	}
	return nil
}

func validatePublicationPushAuthentication(
	claim eventing.PRDevelopmentPublication,
	authentication eventing.PRDevelopmentPublicationPushAuthentication,
) error {
	publication := authentication.Publication
	if !samePublicationGateIdentity(claim, publication) ||
		!samePublicationGatePins(claim, publication) ||
		publication.DecisionRunID != claim.DecisionRunID ||
		publication.Status != eventing.PRDevelopmentPublicationClaimed ||
		publication.ClaimFrom != eventing.PRDevelopmentPublicationPushReady ||
		publication.ClaimOwner != claim.ClaimOwner || publication.ClaimToken != "" ||
		publication.ClaimEpoch != claim.ClaimEpoch || publication.Claims != claim.Claims ||
		!timesEqual(publication.ClaimedAt, claim.ClaimedAt) ||
		publication.ClaimUntil == nil || claim.ClaimUntil == nil ||
		publication.ClaimUntil.Before(*claim.ClaimUntil) ||
		authentication.Case.ID != publication.CaseID ||
		authentication.Case.Repository != publication.ProviderObservation.Repository ||
		authentication.Case.PullNumber != publication.ProviderObservation.PullNumber ||
		authentication.Case.HeadRepository != publication.ProviderObservation.HeadRepository ||
		authentication.Case.HeadRef != publication.ProviderObservation.HeadRef ||
		authentication.Case.HeadSHA != publication.ProviderObservation.HeadSHA ||
		authentication.Case.CurrentReviewState != publication.ProviderObservation.CurrentReviewState ||
		authentication.ThreadIdentity.PullNumber != authentication.Case.PullNumber ||
		strings.TrimSpace(authentication.ThreadIdentity.Provider) == "" ||
		strings.TrimSpace(authentication.ThreadIdentity.ProviderOrigin) == "" ||
		strings.TrimSpace(authentication.ThreadIdentity.RepositoryID) == "" ||
		strings.TrimSpace(authentication.ThreadIdentity.PullRequestID) == "" ||
		validatePublicationPushReadyClaim(withPushClaimAuthority(publication, claim)) != nil {
		return errPublicationPushCorrupt
	}
	return nil
}

func withPushClaimAuthority(
	publication eventing.PRDevelopmentPublication,
	claim eventing.PRDevelopmentPublication,
) eventing.PRDevelopmentPublication {
	publication.ClaimToken = claim.ClaimToken
	return publication
}

func validatePublicationPushObservation(
	publication eventing.PRDevelopmentPublication,
	timed TimedPublicationProviderObservation,
) error {
	if timed.ObservedAt.IsZero() || timed.ObservedAt.Location() != time.UTC {
		return errPublicationPushCorrupt
	}
	if !reflect.DeepEqual(timed.Observation, publication.ProviderObservation) {
		return errPublicationGateProviderChanged
	}
	if publication.ProviderObservedAt == nil ||
		!timed.ObservedAt.After(*publication.ProviderObservedAt) {
		return errPublicationPushCorrupt
	}
	return nil
}

func publicationPushRequest(
	publication eventing.PRDevelopmentPublication,
	expectedRemoteTip string,
) eventing.PRDevelopmentPublicationPushRequest {
	return eventing.PRDevelopmentPublicationPushRequest{
		Repository: publication.SourceCloneURL, SourceRef: publication.SourceRef,
		ExpectedSourceCommit: publication.SourceCommit, WorkspaceID: publication.WorkspaceID,
		LineID: publication.LineID, ExpectedVersion: publication.LineVersion,
		ExpectedMutationEpoch: publication.MutationEpoch,
		ExpectedParkIntentID:  publication.ParkIntentID, ExpectedBase: publication.BaseCommit,
		ExpectedTip: publication.TipCommit, ExpectedTree: publication.Tree,
		ExpectedRemoteTip: expectedRemoteTip,
	}
}

func gitPushRequest(
	request eventing.PRDevelopmentPublicationPushRequest,
) gitworkspace.PinnedLinePushRequest {
	return gitworkspace.PinnedLinePushRequest{
		Repository: request.Repository, SourceRef: request.SourceRef,
		ExpectedSourceCommit: request.ExpectedSourceCommit, WorkspaceID: request.WorkspaceID,
		LineID: request.LineID, ExpectedVersion: request.ExpectedVersion,
		ExpectedMutationEpoch: request.ExpectedMutationEpoch,
		ExpectedParkIntentID:  request.ExpectedParkIntentID, ExpectedBase: request.ExpectedBase,
		ExpectedTip: request.ExpectedTip, ExpectedTree: request.ExpectedTree,
		ExpectedRemoteTip: request.ExpectedRemoteTip,
	}
}

func validateStartedPublication(
	claim eventing.PRDevelopmentPublication,
	authoritative eventing.PRDevelopmentPublication,
	started eventing.PRDevelopmentPublication,
	input eventing.PRDevelopmentPublicationPushStart,
) error {
	if !samePublicationGateIdentity(authoritative, started) ||
		!samePublicationPushPins(authoritative, started) ||
		started.DecisionRunID != authoritative.DecisionRunID ||
		started.Status != eventing.PRDevelopmentPublicationPushStarted ||
		started.ClaimFrom != eventing.PRDevelopmentPublicationPushReady ||
		started.ClaimOwner != claim.ClaimOwner || started.ClaimToken != "" ||
		started.ClaimEpoch != claim.ClaimEpoch || started.Claims != claim.Claims ||
		started.Attempts != 1 || started.EffectStartedAt == nil || started.CompletedAt != nil ||
		started.PushRequest != input.Request || len(started.PushRequestJSON) == 0 ||
		started.PushRequestHash == "" || started.ExpectedRemoteTip != input.Request.ExpectedRemoteTip ||
		!reflect.DeepEqual(started.ProviderObservation, input.Observation) ||
		started.ProviderObservedAt == nil || !started.ProviderObservedAt.Equal(input.ObservedAt) {
		return errPublicationPushCorrupt
	}
	return nil
}

func (handler *PublicationPushReadyHandler) startPushWithExactReplay(
	ctx context.Context,
	input eventing.PRDevelopmentPublicationPushStart,
) (eventing.PRDevelopmentPublication, bool, error) {
	publication, newlyStarted, err := handler.store.StartPRDevelopmentPublicationPush(ctx, input)
	if err == nil {
		return publication, newlyStarted, nil
	}
	replayCtx, cancel := publicationGateFinishContext(ctx)
	defer cancel()
	replayed, replayStarted, replayErr := handler.store.StartPRDevelopmentPublicationPush(
		replayCtx,
		input,
	)
	if replayErr == nil {
		return replayed, replayStarted, nil
	}
	return eventing.PRDevelopmentPublication{}, false, errors.Join(err, replayErr)
}

func (handler *PublicationPushReadyHandler) finishHistoricalStart(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	publication eventing.PRDevelopmentPublication,
	input eventing.PRDevelopmentPublicationPushStart,
) error {
	if !samePublicationGateIdentity(claim, publication) ||
		!samePublicationPushPins(claim, publication) ||
		publication.DecisionRunID != claim.DecisionRunID ||
		publication.PushRequest != input.Request || publication.PushRequestHash == "" ||
		len(publication.PushRequestJSON) == 0 || publication.Attempts != 1 ||
		publication.EffectStartedAt == nil || publication.ClaimEpoch != claim.ClaimEpoch ||
		publication.ExpectedRemoteTip != input.Request.ExpectedRemoteTip ||
		!reflect.DeepEqual(publication.ProviderObservation, input.Observation) ||
		publication.ProviderObservedAt == nil || !publication.ProviderObservedAt.Equal(input.ObservedAt) {
		return errPublicationPushCorrupt
	}
	switch publication.Status {
	case eventing.PRDevelopmentPublicationPublished,
		eventing.PRDevelopmentPublicationConflict,
		eventing.PRDevelopmentPublicationFailed,
		eventing.PRDevelopmentPublicationRecoveryRequired,
		eventing.PRDevelopmentPublicationOutcomeUnknown:
		if publication.ClaimFrom != "" || publication.ClaimOwner != "" ||
			publication.ClaimToken != "" || publication.ClaimUntil != nil ||
			publication.CompletedAt == nil {
			return errPublicationPushCorrupt
		}
		return nil
	case eventing.PRDevelopmentPublicationPushStarted:
		if publication.ClaimFrom != eventing.PRDevelopmentPublicationPushReady ||
			publication.ClaimOwner != claim.ClaimOwner || publication.ClaimToken != "" ||
			publication.ClaimUntil == nil || publication.CompletedAt != nil {
			return errPublicationPushCorrupt
		}
		return handler.finalizeStarted(
			ctx,
			claim,
			publication,
			publicationPushUnknownFinalization(
				publication,
				"historical publication push start cannot repeat Git",
			),
		)
	default:
		return errPublicationPushCorrupt
	}
}

func samePublicationPushPins(
	left eventing.PRDevelopmentPublication,
	right eventing.PRDevelopmentPublication,
) bool {
	return left.PolicyRevision == right.PolicyRevision &&
		bytes.Equal(left.PinnedPolicy, right.PinnedPolicy) &&
		left.PinnedPolicyHash == right.PinnedPolicyHash &&
		left.SubjectRevision == right.SubjectRevision &&
		bytes.Equal(left.PinnedSubject, right.PinnedSubject) &&
		left.PinnedSubjectHash == right.PinnedSubjectHash &&
		reflect.DeepEqual(left.ProviderObservation, right.ProviderObservation) &&
		left.ProviderObservationHash == right.ProviderObservationHash &&
		bytes.Equal(left.ProviderObservationJSON, right.ProviderObservationJSON) &&
		timesEqual(left.ProviderPinnedAt, right.ProviderPinnedAt)
}

func classifyPublicationPushOutcome(
	started eventing.PRDevelopmentPublication,
	gitResult gitworkspace.PinnedLinePushResult,
	pushErr error,
) eventing.PRDevelopmentPublicationPushFinalize {
	result, validResult := publicationPushResult(started.PushRequest, gitResult)
	emptyResult := gitResult == (gitworkspace.PinnedLinePushResult{})
	if errors.Is(pushErr, gitworkspace.ErrPinnedLinePushOutcomeUnknown) ||
		errors.Is(pushErr, context.Canceled) || errors.Is(pushErr, context.DeadlineExceeded) {
		return publicationPushUnknownFinalization(started, "publication push outcome is unknown")
	}
	if validResult && pushErr == nil && result.WorkspaceClean {
		return eventing.PRDevelopmentPublicationPushFinalize{
			RequestHash: started.PushRequestHash,
			Status:      eventing.PRDevelopmentPublicationPublished,
			Result:      result,
		}
	}
	if validResult && !result.WorkspaceClean &&
		errors.Is(pushErr, gitworkspace.ErrPinnedLinePushWorkspaceDrift) {
		return eventing.PRDevelopmentPublicationPushFinalize{
			RequestHash: started.PushRequestHash,
			Status:      eventing.PRDevelopmentPublicationPublished,
			Result:      result,
			LocalDrift:  true,
		}
	}
	if !validResult && emptyResult && errors.Is(pushErr, gitworkspace.ErrPinnedLineConflict) {
		return eventing.PRDevelopmentPublicationPushFinalize{
			RequestHash:   started.PushRequestHash,
			Status:        eventing.PRDevelopmentPublicationConflict,
			ErrorCode:     eventing.PRDevelopmentPublicationErrorPushConflict,
			InternalError: "parked publication line changed before the push effect",
		}
	}
	if !validResult && emptyResult && (errors.Is(pushErr, gitworkspace.ErrPinnedLineInvalid) ||
		errors.Is(pushErr, gitworkspace.ErrPinnedLinePushRemoteUnavailable)) {
		return eventing.PRDevelopmentPublicationPushFinalize{
			RequestHash:   started.PushRequestHash,
			Status:        eventing.PRDevelopmentPublicationFailed,
			ErrorCode:     eventing.PRDevelopmentPublicationErrorPushFailed,
			InternalError: "parked publication push failed before a remote effect was observed",
		}
	}
	return publicationPushUnknownFinalization(started, "publication push returned no trustworthy outcome")
}

func publicationPushResult(
	request eventing.PRDevelopmentPublicationPushRequest,
	result gitworkspace.PinnedLinePushResult,
) (eventing.PRDevelopmentPublicationPushResult, bool) {
	var disposition eventing.PRDevelopmentPublicationPushDisposition
	switch result.Disposition {
	case gitworkspace.PinnedLinePushApplied:
		disposition = eventing.PRDevelopmentPublicationPushApplied
	case gitworkspace.PinnedLinePushAlreadyCurrent:
		disposition = eventing.PRDevelopmentPublicationPushAlreadyCurrent
	case gitworkspace.PinnedLinePushReconciled:
		disposition = eventing.PRDevelopmentPublicationPushReconciled
	default:
		return eventing.PRDevelopmentPublicationPushResult{}, false
	}
	if result.WorkspaceID != request.WorkspaceID || result.Version != request.ExpectedVersion ||
		result.MutationEpoch != request.ExpectedMutationEpoch ||
		result.ParkIntentID != request.ExpectedParkIntentID ||
		result.BaseCommit != request.ExpectedBase || result.Tip != request.ExpectedTip ||
		result.Tree != request.ExpectedTree || result.RemoteRef != "refs/heads/"+request.SourceRef ||
		result.ExpectedRemoteTip != request.ExpectedRemoteTip ||
		result.RemoteTip != request.ExpectedTip ||
		request.ExpectedRemoteTip == request.ExpectedTip &&
			disposition != eventing.PRDevelopmentPublicationPushAlreadyCurrent {
		return eventing.PRDevelopmentPublicationPushResult{}, false
	}
	return eventing.PRDevelopmentPublicationPushResult{
		WorkspaceID: result.WorkspaceID, Version: result.Version,
		MutationEpoch: result.MutationEpoch, ParkIntentID: result.ParkIntentID,
		BaseCommit: result.BaseCommit, Tip: result.Tip, Tree: result.Tree,
		RemoteRef: result.RemoteRef, ExpectedRemoteTip: result.ExpectedRemoteTip,
		RemoteTip: result.RemoteTip, Disposition: disposition,
		WorkspaceClean: result.WorkspaceClean,
	}, true
}

func publicationPushUnknownFinalization(
	started eventing.PRDevelopmentPublication,
	detail string,
) eventing.PRDevelopmentPublicationPushFinalize {
	return eventing.PRDevelopmentPublicationPushFinalize{
		RequestHash:   started.PushRequestHash,
		Status:        eventing.PRDevelopmentPublicationOutcomeUnknown,
		ErrorCode:     eventing.PRDevelopmentPublicationErrorOutcomeUnknown,
		InternalError: detail,
	}
}

func publicationPushRecoveryFinalization(
	started eventing.PRDevelopmentPublication,
	detail string,
) eventing.PRDevelopmentPublicationPushFinalize {
	return eventing.PRDevelopmentPublicationPushFinalize{
		RequestHash:   started.PushRequestHash,
		Status:        eventing.PRDevelopmentPublicationRecoveryRequired,
		ErrorCode:     eventing.PRDevelopmentPublicationErrorRecoveryRequired,
		InternalError: detail,
	}
}

func (handler *PublicationPushReadyHandler) finalizeStarted(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	started eventing.PRDevelopmentPublication,
	input eventing.PRDevelopmentPublicationPushFinalize,
) error {
	input.PublicationID = claim.ID
	input.ClaimToken = claim.ClaimToken
	input.ClaimEpoch = claim.ClaimEpoch
	finishCtx, cancel := publicationGateFinishContext(ctx)
	publication, _, err := handler.store.FinalizePRDevelopmentPublicationPush(finishCtx, input)
	cancel()
	if err == nil && matchesPublicationPushFinalization(publication, input) {
		return nil
	}
	replayCtx, replayCancel := publicationGateFinishContext(ctx)
	replayed, _, replayErr := handler.store.FinalizePRDevelopmentPublicationPush(replayCtx, input)
	replayCancel()
	if replayErr == nil && matchesPublicationPushFinalization(replayed, input) {
		return nil
	}
	if err == nil {
		err = errPublicationPushCorrupt
	}
	if replayErr == nil {
		replayErr = errPublicationPushCorrupt
	}
	return errors.Join(err, replayErr)
}

func matchesPublicationPushFinalization(
	publication eventing.PRDevelopmentPublication,
	input eventing.PRDevelopmentPublicationPushFinalize,
) bool {
	return publication.ID == input.PublicationID && publication.Status == input.Status &&
		publication.PushRequestHash == input.RequestHash &&
		publication.LastErrorCode == input.ErrorCode &&
		publication.LastErrorDetail == input.InternalError &&
		publication.LocalDrift == input.LocalDrift &&
		reflect.DeepEqual(publication.PushResult, input.Result) &&
		publication.ClaimFrom == "" && publication.ClaimOwner == "" &&
		publication.ClaimToken == "" && publication.ClaimUntil == nil &&
		publication.CompletedAt != nil
}

func (handler *PublicationPushReadyHandler) finishPrestartFailure(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	workErr error,
) error {
	switch {
	case errors.Is(workErr, eventing.ErrStaleLease):
		return workErr
	case errors.Is(workErr, eventing.ErrPRDevelopmentPublicationSuperseded):
		return handler.completePrestart(
			ctx, claim,
			eventing.PRDevelopmentPublicationSuperseded,
			eventing.PRDevelopmentPublicationErrorSuperseded,
			"publication local candidate was superseded before push start",
		)
	case errors.Is(workErr, errPublicationGateProviderChanged),
		errors.Is(workErr, ErrGitHubCaseDrift),
		errors.Is(workErr, ErrGitHubCaseNotActionable):
		return handler.completePrestart(
			ctx, claim,
			eventing.PRDevelopmentPublicationConflict,
			eventing.PRDevelopmentPublicationErrorProviderChanged,
			"provider pull request evidence changed before push start",
		)
	case errors.Is(workErr, eventing.ErrPRDevelopmentPublicationConflict):
		return handler.completePrestart(
			ctx, claim,
			eventing.PRDevelopmentPublicationConflict,
			eventing.PRDevelopmentPublicationErrorLocalEvidence,
			"local publication evidence changed before push start",
		)
	case errors.Is(workErr, eventing.ErrPRDevelopmentPublicationRecoveryRequired):
		return handler.completePrestartRecovery(
			ctx,
			claim,
			"publication authentication evidence requires recovery",
		)
	case errors.Is(workErr, eventing.ErrInvalidPRDevelopmentPublication),
		errors.Is(workErr, errPublicationPushCorrupt):
		return handler.completePrestartRecovery(ctx, claim, "publication push state is inconsistent")
	default:
		return handler.requeuePrestart(ctx, claim, workErr)
	}
}

// A queue-renewal failure says nothing about the integrity of the durable
// publication. Before the journaled effect starts, release any still-live
// claim for retry; a definitively stale claim is already outside our authority.
func (handler *PublicationPushReadyHandler) finishQueueRenewFailure(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	renewErr error,
) error {
	if errors.Is(renewErr, eventing.ErrStaleLease) {
		return renewErr
	}
	return handler.requeuePrestart(ctx, claim, renewErr)
}

func (handler *PublicationPushReadyHandler) completePrestartRecovery(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	detail string,
) error {
	return handler.completePrestart(
		ctx, claim,
		eventing.PRDevelopmentPublicationRecoveryRequired,
		eventing.PRDevelopmentPublicationErrorRecoveryRequired,
		detail,
	)
}

func (handler *PublicationPushReadyHandler) completePrestart(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	status eventing.PRDevelopmentPublicationStatus,
	code eventing.PRDevelopmentPublicationErrorCode,
	detail string,
) error {
	input := eventing.PRDevelopmentPublicationPrestartCompletion{
		PublicationID: claim.ID,
		ClaimToken:    claim.ClaimToken, ClaimEpoch: claim.ClaimEpoch,
		Status: status, ErrorCode: code, InternalError: detail,
	}
	finishCtx, cancel := publicationGateFinishContext(ctx)
	publication, _, err := handler.store.CompletePRDevelopmentPublicationPrestart(finishCtx, input)
	cancel()
	if err == nil && matchesPublicationPrestartCompletion(publication, input) {
		return nil
	}
	replayCtx, replayCancel := publicationGateFinishContext(ctx)
	replayed, _, replayErr := handler.store.CompletePRDevelopmentPublicationPrestart(replayCtx, input)
	replayCancel()
	if replayErr == nil && matchesPublicationPrestartCompletion(replayed, input) {
		return nil
	}
	if err == nil {
		err = errPublicationPushCorrupt
	}
	if replayErr == nil {
		replayErr = errPublicationPushCorrupt
	}
	return errors.Join(err, replayErr)
}

func matchesPublicationPrestartCompletion(
	publication eventing.PRDevelopmentPublication,
	input eventing.PRDevelopmentPublicationPrestartCompletion,
) bool {
	return publication.ID == input.PublicationID && publication.Status == input.Status &&
		publication.LastErrorCode == input.ErrorCode &&
		publication.LastErrorDetail == input.InternalError && publication.PushRequestHash == "" &&
		publication.EffectStartedAt == nil && publication.CompletedAt != nil &&
		publication.ClaimFrom == "" && publication.ClaimToken == ""
}

func (handler *PublicationPushReadyHandler) requeuePrestart(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	workErr error,
) error {
	availableAt := handler.currentTime().Add(PublicationRetryDelay(claim.Claims))
	input := eventing.PRDevelopmentPublicationRequeue{
		PublicationID: claim.ID, ClaimToken: claim.ClaimToken, ClaimEpoch: claim.ClaimEpoch,
		ExpectedClaimFrom: eventing.PRDevelopmentPublicationPushReady,
		AvailableAt:       availableAt,
	}
	finishCtx, cancel := publicationGateFinishContext(ctx)
	_, _, err := handler.store.RequeuePRDevelopmentPublication(finishCtx, input)
	if err != nil {
		current, loadErr := handler.store.GetPRDevelopmentPublication(finishCtx, claim.ID)
		if loadErr != nil || current.Status != eventing.PRDevelopmentPublicationPushReady ||
			current.ClaimEpoch != claim.ClaimEpoch || current.ClaimToken != "" ||
			!current.AvailableAt.Equal(availableAt) {
			cancel()
			return errors.Join(workErr, err)
		}
	}
	cancel()
	return workErr
}

func (handler *PublicationPushReadyHandler) currentTime() time.Time {
	if handler != nil && handler.now != nil {
		return handler.now().UTC()
	}
	return time.Now().UTC()
}

var _ PublicationPushReadyClaimHandler = (*PublicationPushReadyHandler)(nil)

// Compile-time guards make accidental widening of the handler dependencies
// visible at the point where production adapters are composed.
var _ PublicationPinnedLinePusher = (*gitworkspace.Manager)(nil)
