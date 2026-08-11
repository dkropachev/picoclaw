package prdevelopment

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	defaultPublicationGateClaimLease = 5 * time.Minute
	publicationGateFinishTimeout     = 10 * time.Second
	minimumPublicationGateClaimLease = 3 * publicationGateFinishTimeout
)

// PublicationGateLifecycleStore owns only pre-effect scheduling transitions.
// Both pending evaluation and gate-wait observation need this exact authority:
// renewal, exact-origin requeue with lost-response convergence, and terminal
// scheduling transitions. It cannot claim work, execute a run, or start a push.
type PublicationGateLifecycleStore interface {
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
	ReleasePRDevelopmentPublicationGateWait(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationGateWait,
	) (eventing.PRDevelopmentPublication, bool, error)
	MarkPRDevelopmentPublicationPushReady(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationMarkPushReady,
	) (eventing.PRDevelopmentPublication, bool, error)
	CompletePRDevelopmentPublicationPrestart(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationPrestartCompletion,
	) (eventing.PRDevelopmentPublication, bool, error)
}

// PublicationGateClaimProcessor prepares one already-claimed policy without
// acquiring work or crossing an active workflow boundary.
type PublicationGateClaimProcessor interface {
	ProcessClaim(
		ctx context.Context,
		claim eventing.PRDevelopmentPublication,
	) (PublicationGateProcessResult, error)
}

// PublicationActiveGateExecutor admits or resolves one exact private decision
// without owning its scheduling lifecycle.
type PublicationActiveGateExecutor interface {
	ExecuteClaim(
		ctx context.Context,
		claim eventing.PRDevelopmentPublication,
	) (PublicationGateExecutionResult, error)
}

// PublicationPendingGateHandlerConfig composes the already-uncomposed policy
// processor with active execution and scheduling-only lifecycle transitions.
type PublicationPendingGateHandlerConfig struct {
	Store         PublicationGateLifecycleStore `json:"-"`
	Processor     PublicationGateClaimProcessor `json:"-"`
	Executor      PublicationActiveGateExecutor `json:"-"`
	LeaseDuration time.Duration                 `json:"-"`
	Now           func() time.Time              `json:"-"`
}

// PublicationPendingGateHandler processes one existing claimed-from-pending
// publication. It owns neither queue enumeration nor push-ready handling.
type PublicationPendingGateHandler struct {
	store         PublicationGateLifecycleStore
	processor     PublicationGateClaimProcessor
	executor      PublicationActiveGateExecutor
	leaseDuration time.Duration
	now           func() time.Time
}

func NewPublicationPendingGateHandler(
	config PublicationPendingGateHandlerConfig,
) (*PublicationPendingGateHandler, error) {
	if config.Store == nil || isNilServiceValue(config.Store) {
		return nil, fmt.Errorf("%w: pending publication gate store is required", ErrUnavailable)
	}
	if config.Processor == nil || isNilServiceValue(config.Processor) ||
		config.Executor == nil || isNilServiceValue(config.Executor) {
		return nil, fmt.Errorf("%w: pending publication gate services are required", ErrUnavailable)
	}
	lease := config.LeaseDuration
	if lease <= 0 {
		lease = defaultPublicationGateClaimLease
	}
	if lease < minimumPublicationGateClaimLease {
		return nil, ErrInvalidRequest
	}
	return &PublicationPendingGateHandler{
		store: config.Store, processor: config.Processor, executor: config.Executor,
		leaseDuration: lease, now: config.Now,
	}, nil
}

func (handler *PublicationPendingGateHandler) HandleClaim(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) error {
	if handler == nil || handler.store == nil || isNilServiceValue(handler.store) ||
		handler.processor == nil || isNilServiceValue(handler.processor) ||
		handler.executor == nil || isNilServiceValue(handler.executor) {
		return ErrUnavailable
	}
	if validatePublicationDispatchClaim(claim) != nil ||
		claim.ClaimFrom != eventing.PRDevelopmentPublicationPending {
		return ErrInvalidRequest
	}
	ctx = ctxOrBackground(ctx)
	heartbeat, renewErr := startPublicationGateHeartbeat(
		ctx,
		handler.store,
		claim,
		handler.leaseDuration,
	)
	if renewErr != nil {
		return handler.finishFailure(ctx, claim, renewErr)
	}
	processed, processErr := handler.processor.ProcessClaim(heartbeat.Context(), claim)
	if processErr == nil {
		switch processed.Disposition {
		case PublicationGatePushReady, PublicationGateTerminal:
			renewErr := heartbeat.Stop()
			if renewErr != nil && !errors.Is(renewErr, eventing.ErrStaleLease) {
				return renewErr
			}
			return nil
		case PublicationGateRequiresExecution:
		default:
			processErr = errPublicationGateCorrupt
		}
	}
	if processErr != nil {
		if renewErr := heartbeat.Stop(); renewErr != nil {
			return handler.finishFailure(ctx, claim, renewErr)
		}
		return handler.finishFailure(ctx, claim, processErr)
	}

	result, executeErr := handler.executor.ExecuteClaim(heartbeat.Context(), claim)
	if renewErr := heartbeat.Stop(); renewErr != nil {
		return handler.finishFailure(ctx, claim, renewErr)
	}
	if result.RunID != "" && result.Status != "" {
		return handler.finishRun(ctx, claim, result, executeErr)
	}
	if executeErr == nil {
		executeErr = errPublicationGateCorrupt
	}
	return handler.finishFailure(ctx, claim, executeErr)
}

// HandlePendingClaim implements the pending-phase dispatcher contract.
func (handler *PublicationPendingGateHandler) HandlePendingClaim(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) error {
	return handler.HandleClaim(ctx, claim)
}

func (handler *PublicationPendingGateHandler) finishRun(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	result PublicationGateExecutionResult,
	workErr error,
) error {
	if validatePublicationGateExecutionResult(claim, result) != nil {
		return handler.completeRecovery(ctx, claim, "publication gate result identity changed")
	}
	switch result.Status {
	case workflows.RunStatusWaiting:
		return handler.releaseWait(ctx, claim, result.Publication, result.RunID)
	case workflows.RunStatusSucceeded:
		return handler.markPushReady(ctx, claim, result.RunID)
	case workflows.RunStatusFailed, workflows.RunStatusCanceled, workflows.RunStatusSkipped:
		return handler.completeGateFailed(ctx, claim)
	case workflows.RunStatusRunning:
		return handler.completeRecovery(ctx, claim, "publication gate remained running after admission")
	default:
		if workErr != nil {
			return handler.finishFailure(ctx, claim, workErr)
		}
		return handler.completeRecovery(ctx, claim, "publication gate returned an unknown state")
	}
}

func validatePublicationGateExecutionResult(
	claim eventing.PRDevelopmentPublication,
	result PublicationGateExecutionResult,
) error {
	publication := result.Publication
	if result.RunID == "" || result.Status == "" ||
		!validClaimedPublicationGateResponse(claim, publication) ||
		validateCompletePublicationProviderPin(publication) != nil ||
		claim.DecisionRunID != "" && claim.DecisionRunID != result.RunID ||
		publication.DecisionRunID != "" && publication.DecisionRunID != result.RunID {
		return errPublicationGateCorrupt
	}
	authoritative := publication
	authoritative.ClaimToken = claim.ClaimToken
	if validatePublicationGateClaim(authoritative) != nil {
		return errPublicationGateCorrupt
	}
	policy, found, err := decodePublicationGatePolicy(publication)
	if err != nil || !found || policy.IsNoop() {
		return errPublicationGateCorrupt
	}
	if _, _, found, err = decodePublicationActiveSubject(publication, policy); err != nil || !found {
		return errPublicationGateCorrupt
	}
	wantRunID, err := prDevelopmentPublicationRunID(publicationDecisionKey(publication))
	if err != nil || wantRunID != result.RunID {
		return errPublicationGateCorrupt
	}
	return nil
}

func (handler *PublicationPendingGateHandler) finishFailure(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	workErr error,
) error {
	switch {
	case errors.Is(workErr, errPublicationGateSuperseded):
		return handler.complete(
			ctx, claim,
			eventing.PRDevelopmentPublicationSuperseded,
			eventing.PRDevelopmentPublicationErrorSuperseded,
			"publication local candidate was superseded before gate completion",
		)
	case errors.Is(workErr, errPublicationGateProviderChanged):
		return handler.complete(
			ctx, claim,
			eventing.PRDevelopmentPublicationConflict,
			eventing.PRDevelopmentPublicationErrorProviderChanged,
			"provider pull request evidence changed before gate completion",
		)
	case errors.Is(workErr, errPublicationGateLocalEvidence):
		return handler.complete(
			ctx, claim,
			eventing.PRDevelopmentPublicationConflict,
			eventing.PRDevelopmentPublicationErrorLocalEvidence,
			"local publication evidence changed before gate completion",
		)
	case errors.Is(workErr, sharedattention.ErrPrivateRunAdmissionUncertain):
		return handler.completeRecovery(ctx, claim, "publication gate admission is uncertain")
	case errors.Is(workErr, errPublicationGateCorrupt):
		return handler.completeRecovery(ctx, claim, "publication gate state is inconsistent")
	case errors.Is(workErr, ErrAttentionSubjectTooLarge),
		errors.Is(workErr, ErrAIContextCompactionRequired):
		return handler.completeGateFailed(ctx, claim)
	case errors.Is(workErr, eventing.ErrStaleLease):
		return workErr
	default:
		return handler.requeue(ctx, claim, workErr)
	}
}

func (handler *PublicationPendingGateHandler) releaseWait(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	expected eventing.PRDevelopmentPublication,
	runID string,
) error {
	finishCtx, cancel := publicationGateFinishContext(ctx)
	defer cancel()
	input := eventing.PRDevelopmentPublicationGateWait{
		PublicationID: claim.ID,
		ClaimToken:    claim.ClaimToken,
		ClaimEpoch:    claim.ClaimEpoch,
		DecisionRunID: runID,
		AvailableAt: handler.currentTime().Add(
			PublicationRetryDelay(claim.Claims),
		),
	}
	_, _, err := handler.store.ReleasePRDevelopmentPublicationGateWait(finishCtx, input)
	if err == nil {
		return nil
	}
	current, loadErr := handler.store.GetPRDevelopmentPublication(finishCtx, claim.ID)
	if loadErr == nil && stablePublicationGateWaitReplay(current, claim, expected, input) {
		return nil
	}
	return handler.finishFailure(
		ctx,
		claim,
		publicationGateStoreFailure(err, errPublicationGateLocalEvidence),
	)
}

func stablePublicationGateWaitReplay(
	current eventing.PRDevelopmentPublication,
	claim eventing.PRDevelopmentPublication,
	expected eventing.PRDevelopmentPublication,
	input eventing.PRDevelopmentPublicationGateWait,
) bool {
	return current.ID == claim.ID && samePublicationGateIdentity(current, expected) &&
		samePublicationGatePins(current, expected) &&
		current.Status == eventing.PRDevelopmentPublicationGateWaiting &&
		current.ClaimFrom == "" && current.ClaimOwner == "" && current.ClaimToken == "" &&
		current.ClaimUntil == nil && current.ClaimEpoch == claim.ClaimEpoch &&
		current.DecisionRunID == input.DecisionRunID &&
		current.AvailableAt.Equal(input.AvailableAt)
}

func (handler *PublicationPendingGateHandler) markPushReady(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	runID string,
) error {
	finishCtx, cancel := publicationGateFinishContext(ctx)
	defer cancel()
	_, _, err := handler.store.MarkPRDevelopmentPublicationPushReady(
		finishCtx,
		eventing.PRDevelopmentPublicationMarkPushReady{
			PublicationID: claim.ID,
			ClaimToken:    claim.ClaimToken,
			ClaimEpoch:    claim.ClaimEpoch,
			DecisionRunID: runID,
		},
	)
	if err == nil {
		return nil
	}
	return handler.finishFailure(
		ctx,
		claim,
		publicationGateStoreFailure(err, errPublicationGateLocalEvidence),
	)
}

func (handler *PublicationPendingGateHandler) completeGateFailed(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) error {
	return handler.complete(
		ctx,
		claim,
		eventing.PRDevelopmentPublicationFailed,
		eventing.PRDevelopmentPublicationErrorGateFailed,
		"private publication gate did not approve publication",
	)
}

func (handler *PublicationPendingGateHandler) completeRecovery(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	detail string,
) error {
	return handler.complete(
		ctx,
		claim,
		eventing.PRDevelopmentPublicationRecoveryRequired,
		eventing.PRDevelopmentPublicationErrorRecoveryRequired,
		detail,
	)
}

func (handler *PublicationPendingGateHandler) complete(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	status eventing.PRDevelopmentPublicationStatus,
	code eventing.PRDevelopmentPublicationErrorCode,
	detail string,
) error {
	finishCtx, cancel := publicationGateFinishContext(ctx)
	defer cancel()
	_, _, err := handler.store.CompletePRDevelopmentPublicationPrestart(
		finishCtx,
		eventing.PRDevelopmentPublicationPrestartCompletion{
			PublicationID: claim.ID,
			ClaimToken:    claim.ClaimToken,
			ClaimEpoch:    claim.ClaimEpoch,
			Status:        status,
			ErrorCode:     code,
			InternalError: detail,
		},
	)
	return err
}

func (handler *PublicationPendingGateHandler) requeue(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	workErr error,
) error {
	availableAt := handler.currentTime().Add(PublicationRetryDelay(claim.Claims))
	finishCtx, cancel := publicationGateFinishContext(ctx)
	defer cancel()
	_, _, err := handler.store.RequeuePRDevelopmentPublication(
		finishCtx,
		eventing.PRDevelopmentPublicationRequeue{
			PublicationID:     claim.ID,
			ClaimToken:        claim.ClaimToken,
			ClaimEpoch:        claim.ClaimEpoch,
			ExpectedClaimFrom: claim.ClaimFrom,
			AvailableAt:       availableAt,
		},
	)
	if err != nil {
		current, loadErr := handler.store.GetPRDevelopmentPublication(finishCtx, claim.ID)
		if loadErr != nil || current.Status != claim.ClaimFrom ||
			current.ClaimEpoch != claim.ClaimEpoch ||
			!current.AvailableAt.Equal(availableAt) || current.ClaimToken != "" {
			return err
		}
	}
	return workErr
}

func (handler *PublicationPendingGateHandler) currentTime() time.Time {
	if handler != nil && handler.now != nil {
		return handler.now().UTC()
	}
	return time.Now().UTC()
}

type PublicationGateWaitingHandlerConfig struct {
	Store         PublicationGateLifecycleStore `json:"-"`
	Runs          workflows.RunStore            `json:"-"`
	LeaseDuration time.Duration                 `json:"-"`
	Now           func() time.Time              `json:"-"`
}

// PublicationGateWaitingHandler observes only the already-linked private run.
// A running state may be an in-flight browser continuation, so it is returned
// to gate_waiting rather than treated as admission uncertainty.
type PublicationGateWaitingHandler struct {
	store         PublicationGateLifecycleStore
	runs          workflows.RunStore
	leaseDuration time.Duration
	now           func() time.Time
}

func NewPublicationGateWaitingHandler(
	config PublicationGateWaitingHandlerConfig,
) (*PublicationGateWaitingHandler, error) {
	if config.Store == nil || isNilServiceValue(config.Store) ||
		config.Runs == nil || isNilServiceValue(config.Runs) {
		return nil, fmt.Errorf("%w: publication gate-wait services are required", ErrUnavailable)
	}
	lease := config.LeaseDuration
	if lease <= 0 {
		lease = defaultPublicationGateClaimLease
	}
	if lease < minimumPublicationGateClaimLease {
		return nil, ErrInvalidRequest
	}
	return &PublicationGateWaitingHandler{
		store: config.Store, runs: config.Runs, leaseDuration: lease, now: config.Now,
	}, nil
}

func (handler *PublicationGateWaitingHandler) HandleClaim(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) error {
	if handler == nil || handler.store == nil || isNilServiceValue(handler.store) ||
		handler.runs == nil || isNilServiceValue(handler.runs) {
		return ErrUnavailable
	}
	if validatePublicationDispatchClaim(claim) != nil ||
		claim.ClaimFrom != eventing.PRDevelopmentPublicationGateWaiting {
		return ErrInvalidRequest
	}
	ctx = ctxOrBackground(ctx)
	heartbeat, renewErr := startPublicationGateHeartbeat(
		ctx,
		handler.store,
		claim,
		handler.leaseDuration,
	)
	if renewErr != nil {
		return handler.finishRenewFailure(ctx, claim, renewErr)
	}
	if err := validatePublicationGateWaitingIdentity(claim); err != nil {
		if renewErr = heartbeat.Stop(); renewErr != nil {
			return handler.finishRenewFailure(ctx, claim, renewErr)
		}
		return handler.completeRecovery(ctx, claim, "publication gate identity is inconsistent")
	}
	key := publicationDecisionKey(claim)
	wantRunID, err := prDevelopmentPublicationRunID(key)
	if err != nil || wantRunID != claim.DecisionRunID {
		if renewErr = heartbeat.Stop(); renewErr != nil {
			return handler.finishRenewFailure(ctx, claim, renewErr)
		}
		return handler.completeRecovery(ctx, claim, "publication gate link is inconsistent")
	}
	run, loadErr := handler.runs.GetRun(heartbeat.Context(), wantRunID)
	if renewErr := heartbeat.Stop(); renewErr != nil {
		return handler.finishRenewFailure(ctx, claim, renewErr)
	}
	if errors.Is(loadErr, os.ErrNotExist) {
		return handler.completeRecovery(ctx, claim, "publication gate run is missing")
	}
	if loadErr != nil {
		return handler.requeue(ctx, claim, loadErr)
	}
	if !sharedattention.ValidPrivateRun(run, wantRunID) {
		return handler.completeRecovery(ctx, claim, "publication gate run is inconsistent")
	}
	switch run.Status {
	case workflows.RunStatusWaiting, workflows.RunStatusRunning:
		return handler.releaseWait(ctx, claim, wantRunID)
	case workflows.RunStatusSucceeded:
		return handler.markPushReady(ctx, claim, wantRunID)
	case workflows.RunStatusFailed, workflows.RunStatusCanceled, workflows.RunStatusSkipped:
		return handler.completeGateFailed(ctx, claim)
	default:
		return handler.completeRecovery(ctx, claim, "publication gate run has an unknown state")
	}
}

func validatePublicationGateWaitingIdentity(
	claim eventing.PRDevelopmentPublication,
) error {
	if claim.DecisionRunID == "" || claim.PolicyRevision == "" ||
		claim.SubjectRevision == "" || claim.ProviderObservationHash == "" {
		return errPublicationGateCorrupt
	}
	policy, found, err := decodePublicationGatePolicy(claim)
	if err != nil || !found || policy.IsNoop() {
		return errPublicationGateCorrupt
	}
	if _, _, found, err = decodePublicationActiveSubject(claim, policy); err != nil || !found ||
		validateCompletePublicationProviderPin(claim) != nil {
		return errPublicationGateCorrupt
	}
	return nil
}

// HandleGateWaitingClaim implements the gate-waiting dispatcher contract.
func (handler *PublicationGateWaitingHandler) HandleGateWaitingClaim(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) error {
	return handler.HandleClaim(ctx, claim)
}

func (handler *PublicationGateWaitingHandler) pendingAdapter() *PublicationPendingGateHandler {
	return &PublicationPendingGateHandler{
		store: handler.store,
		now:   handler.now,
	}
}

func (handler *PublicationGateWaitingHandler) releaseWait(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	runID string,
) error {
	return handler.pendingAdapter().releaseWait(ctx, claim, claim, runID)
}

func (handler *PublicationGateWaitingHandler) markPushReady(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	runID string,
) error {
	return handler.pendingAdapter().markPushReady(ctx, claim, runID)
}

func (handler *PublicationGateWaitingHandler) completeGateFailed(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) error {
	return handler.pendingAdapter().completeGateFailed(ctx, claim)
}

func (handler *PublicationGateWaitingHandler) completeRecovery(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	detail string,
) error {
	return handler.pendingAdapter().completeRecovery(ctx, claim, detail)
}

func (handler *PublicationGateWaitingHandler) requeue(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	workErr error,
) error {
	return handler.pendingAdapter().requeue(ctx, claim, workErr)
}

func (handler *PublicationGateWaitingHandler) finishRenewFailure(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	renewErr error,
) error {
	if errors.Is(renewErr, eventing.ErrStaleLease) {
		return renewErr
	}
	return handler.requeue(ctx, claim, renewErr)
}

type publicationGateHeartbeatStore interface {
	RenewPRDevelopmentPublication(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationRenew,
	) error
}

type publicationGateHeartbeat struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	errs   chan error
	stop   bool
}

func startPublicationGateHeartbeat(
	ctx context.Context,
	store publicationGateHeartbeatStore,
	claim eventing.PRDevelopmentPublication,
	lease time.Duration,
) (*publicationGateHeartbeat, error) {
	workCtx, cancel := context.WithCancel(ctxOrBackground(ctx))
	heartbeat := &publicationGateHeartbeat{
		ctx: workCtx, cancel: cancel, done: make(chan struct{}), errs: make(chan error, 1),
	}
	renewCtx, renewCancel := publicationGateFinishContext(ctx)
	err := renewPublicationGateClaim(renewCtx, store, claim, lease)
	renewCancel()
	if err != nil {
		cancel()
		close(heartbeat.done)
		return nil, err
	}
	go func() {
		defer close(heartbeat.done)
		ticker := time.NewTicker(lease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-workCtx.Done():
				return
			case <-ticker.C:
				err := renewPublicationGateClaim(workCtx, store, claim, lease)
				if err != nil {
					if workCtx.Err() != nil {
						return
					}
					select {
					case heartbeat.errs <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()
	return heartbeat, nil
}

func renewPublicationGateClaim(
	ctx context.Context,
	store publicationGateHeartbeatStore,
	claim eventing.PRDevelopmentPublication,
	lease time.Duration,
) error {
	return store.RenewPRDevelopmentPublication(
		ctx,
		eventing.PRDevelopmentPublicationRenew{
			PublicationID: claim.ID,
			ClaimToken:    claim.ClaimToken,
			ClaimEpoch:    claim.ClaimEpoch,
			Lease:         lease,
		},
	)
}

func (heartbeat *publicationGateHeartbeat) Context() context.Context {
	if heartbeat == nil || heartbeat.ctx == nil {
		return context.Background()
	}
	return heartbeat.ctx
}

func (heartbeat *publicationGateHeartbeat) Stop() error {
	if heartbeat == nil || heartbeat.stop {
		return nil
	}
	heartbeat.stop = true
	heartbeat.cancel()
	<-heartbeat.done
	select {
	case err := <-heartbeat.errs:
		return err
	default:
		return nil
	}
}

func publicationGateFinishContext(
	ctx context.Context,
) (context.Context, context.CancelFunc) {
	return context.WithTimeout(
		context.WithoutCancel(ctxOrBackground(ctx)),
		publicationGateFinishTimeout,
	)
}

var (
	_ PublicationPendingClaimHandler     = (*PublicationPendingGateHandler)(nil)
	_ PublicationGateWaitingClaimHandler = (*PublicationGateWaitingHandler)(nil)
)
