package prdevelopment

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

// PublicationPendingClaimHandler handles one publication already claimed from
// pending. Claim acquisition and lease lifecycle remain outside this contract.
type PublicationPendingClaimHandler interface {
	HandlePendingClaim(
		ctx context.Context,
		claim eventing.PRDevelopmentPublication,
	) error
}

// PublicationGateWaitingClaimHandler handles one publication already claimed
// from gate_waiting. It receives no authority to claim another publication.
type PublicationGateWaitingClaimHandler interface {
	HandleGateWaitingClaim(
		ctx context.Context,
		claim eventing.PRDevelopmentPublication,
	) error
}

// PublicationPushReadyClaimHandler handles one publication already claimed
// from push_ready. It is the only dispatcher dependency allowed to cross the
// external-effect boundary.
type PublicationPushReadyClaimHandler interface {
	HandlePushReadyClaim(
		ctx context.Context,
		claim eventing.PRDevelopmentPublication,
	) error
}

// PublicationPendingClaimHandlerFunc adapts a function to a pending handler.
type PublicationPendingClaimHandlerFunc func(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) error

func (handler PublicationPendingClaimHandlerFunc) HandlePendingClaim(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) error {
	if handler == nil {
		return ErrUnavailable
	}
	return handler(ctx, claim)
}

// PublicationGateWaitingClaimHandlerFunc adapts a function to a gate-waiting
// handler.
type PublicationGateWaitingClaimHandlerFunc func(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) error

func (handler PublicationGateWaitingClaimHandlerFunc) HandleGateWaitingClaim(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) error {
	if handler == nil {
		return ErrUnavailable
	}
	return handler(ctx, claim)
}

// PublicationPushReadyClaimHandlerFunc adapts a function to a push-ready
// handler.
type PublicationPushReadyClaimHandlerFunc func(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) error

func (handler PublicationPushReadyClaimHandlerFunc) HandlePushReadyClaim(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) error {
	if handler == nil {
		return ErrUnavailable
	}
	return handler(ctx, claim)
}

// PublicationDispatcherConfig requires one least-authority handler for every
// reclaimable pre-effect publication phase.
type PublicationDispatcherConfig struct {
	Pending     PublicationPendingClaimHandler     `json:"-"`
	GateWaiting PublicationGateWaitingClaimHandler `json:"-"`
	PushReady   PublicationPushReadyClaimHandler   `json:"-"`
}

// PublicationDispatcher routes one already-claimed publication by ClaimFrom.
// It never claims, renews, requeues, or otherwise changes publication state.
type PublicationDispatcher struct {
	pending     PublicationPendingClaimHandler
	gateWaiting PublicationGateWaitingClaimHandler
	pushReady   PublicationPushReadyClaimHandler
}

func NewPublicationDispatcher(
	config PublicationDispatcherConfig,
) (*PublicationDispatcher, error) {
	if config.Pending == nil || isNilServiceValue(config.Pending) {
		return nil, fmt.Errorf("%w: pending publication claim handler is required", ErrUnavailable)
	}
	if config.GateWaiting == nil || isNilServiceValue(config.GateWaiting) {
		return nil, fmt.Errorf(
			"%w: gate-waiting publication claim handler is required",
			ErrUnavailable,
		)
	}
	if config.PushReady == nil || isNilServiceValue(config.PushReady) {
		return nil, fmt.Errorf(
			"%w: push-ready publication claim handler is required",
			ErrUnavailable,
		)
	}
	return &PublicationDispatcher{
		pending:     config.Pending,
		gateWaiting: config.GateWaiting,
		pushReady:   config.PushReady,
	}, nil
}

// DispatchClaim validates and routes exactly one claim. Handler errors are
// returned unchanged so the lifecycle owner can apply phase-specific policy.
func (dispatcher *PublicationDispatcher) DispatchClaim(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) error {
	if dispatcher == nil ||
		dispatcher.pending == nil || isNilServiceValue(dispatcher.pending) ||
		dispatcher.gateWaiting == nil || isNilServiceValue(dispatcher.gateWaiting) ||
		dispatcher.pushReady == nil || isNilServiceValue(dispatcher.pushReady) {
		return ErrUnavailable
	}
	if err := validatePublicationDispatchClaim(claim); err != nil {
		return err
	}
	ctx = ctxOrBackground(ctx)
	switch claim.ClaimFrom {
	case eventing.PRDevelopmentPublicationPending:
		return dispatcher.pending.HandlePendingClaim(ctx, claim)
	case eventing.PRDevelopmentPublicationGateWaiting:
		return dispatcher.gateWaiting.HandleGateWaitingClaim(ctx, claim)
	case eventing.PRDevelopmentPublicationPushReady:
		return dispatcher.pushReady.HandlePushReadyClaim(ctx, claim)
	default:
		return ErrInvalidRequest
	}
}

func validatePublicationDispatchClaim(
	claim eventing.PRDevelopmentPublication,
) error {
	if !validDevelopmentID(claim.ID, "pdpub_") ||
		claim.Status != eventing.PRDevelopmentPublicationClaimed ||
		!validPublicationDispatchOrigin(claim.ClaimFrom) ||
		claim.ClaimOwner == "" || claim.ClaimOwner != strings.TrimSpace(claim.ClaimOwner) ||
		claim.ClaimToken == "" || claim.ClaimToken != strings.TrimSpace(claim.ClaimToken) ||
		claim.ClaimEpoch < 1 || claim.Claims < 1 || int64(claim.Claims) != claim.ClaimEpoch ||
		claim.ClaimedAt == nil || claim.ClaimUntil == nil ||
		!claim.ClaimUntil.After(*claim.ClaimedAt) ||
		claim.EffectStartedAt != nil || claim.CompletedAt != nil {
		return ErrInvalidRequest
	}
	return nil
}

func validPublicationDispatchOrigin(
	status eventing.PRDevelopmentPublicationStatus,
) bool {
	switch status {
	case eventing.PRDevelopmentPublicationPending,
		eventing.PRDevelopmentPublicationGateWaiting,
		eventing.PRDevelopmentPublicationPushReady:
		return true
	default:
		return false
	}
}
