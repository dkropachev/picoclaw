package prdevelopment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const (
	defaultPublicationWorkerLabel = "gateway-pr-development-publication"
	// The queue claim is only a handoff lease. Every phase handler immediately
	// replaces it with the longer handler-owned lease before doing work, so the
	// handoff must be strictly shorter for SQLite renewal to extend the deadline.
	defaultPublicationWorkerLease = time.Minute
)

// publicationWorkerQueue is deliberately limited to claiming bounded
// pre-effect work. Publication handlers, rather than the queue-owning worker,
// retain all renewal, requeue, completion, workflow, and push authority.
type publicationWorkerQueue interface {
	ClaimPRDevelopmentPublications(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationClaimRequest,
	) ([]eventing.PRDevelopmentPublication, error)
}

var _ publicationWorkerQueue = (*eventing.Store)(nil)

// PublicationWorkerConfig contains only process-local scheduling
// dependencies. Claims and dispatcher services are private bearer authority
// and must never appear in a JSON projection.
type PublicationWorkerConfig struct {
	Queue         publicationWorkerQueue `json:"-"`
	Dispatcher    *PublicationDispatcher `json:"-"`
	WorkerLabel   string                 `json:"-"`
	LeaseDuration time.Duration          `json:"-"`
}

// PublicationWorker owns queue enumeration only. Each ProcessOne call claims
// at most one due pre-effect publication and transfers that exact claim to the
// complete phase dispatcher. The selected handler owns the claim lifecycle.
type PublicationWorker struct {
	queue         publicationWorkerQueue
	dispatcher    *PublicationDispatcher
	workerLabel   string
	leaseDuration time.Duration
}

// NewPublicationWorker constructs the least-authority publication scheduler.
// An empty label and non-positive lease use stable gateway defaults.
func NewPublicationWorker(config PublicationWorkerConfig) (*PublicationWorker, error) {
	if config.Queue == nil || isNilServiceValue(config.Queue) {
		return nil, fmt.Errorf("%w: publication worker queue is required", ErrUnavailable)
	}
	if !publicationDispatcherComplete(config.Dispatcher) {
		return nil, fmt.Errorf(
			"%w: complete publication dispatcher is required",
			ErrUnavailable,
		)
	}

	label := strings.TrimSpace(config.WorkerLabel)
	if label == "" {
		label = defaultPublicationWorkerLabel
	}
	if config.WorkerLabel != "" && label != config.WorkerLabel {
		return nil, errors.New("publication worker label must be exact")
	}

	lease := config.LeaseDuration
	if lease <= 0 {
		lease = defaultPublicationWorkerLease
	}
	if lease < minimumPublicationGateClaimLease {
		return nil, fmt.Errorf(
			"%w: publication worker lease is below %s",
			ErrInvalidRequest,
			minimumPublicationGateClaimLease,
		)
	}
	if !publicationWorkerLeasePrecedesHandlers(config.Dispatcher, lease) {
		return nil, fmt.Errorf(
			"%w: publication worker handoff lease must be shorter than every handler lease",
			ErrInvalidRequest,
		)
	}

	return &PublicationWorker{
		queue:         config.Queue,
		dispatcher:    config.Dispatcher,
		workerLabel:   label,
		leaseDuration: lease,
	}, nil
}

// ProcessOne claims and dispatches at most one publication. It deliberately
// performs no generic error requeue: every phase handler owns its exact lease
// lifecycle and its errors are returned unchanged.
func (worker *PublicationWorker) ProcessOne(ctx context.Context) (bool, error) {
	if worker == nil || worker.queue == nil || isNilServiceValue(worker.queue) ||
		!publicationDispatcherComplete(worker.dispatcher) ||
		worker.workerLabel == "" || worker.leaseDuration < minimumPublicationGateClaimLease ||
		!publicationWorkerLeasePrecedesHandlers(worker.dispatcher, worker.leaseDuration) {
		return false, ErrUnavailable
	}
	ctx = ctxOrBackground(ctx)
	claimed, err := worker.queue.ClaimPRDevelopmentPublications(
		ctx,
		eventing.PRDevelopmentPublicationClaimRequest{
			WorkerLabel: worker.workerLabel,
			Limit:       1,
			Lease:       worker.leaseDuration,
		},
	)
	if err != nil {
		return false, err
	}
	switch len(claimed) {
	case 0:
		return false, nil
	case 1:
		return true, worker.dispatcher.DispatchClaim(ctx, claimed[0])
	default:
		return true, errors.New(
			"pull request development publication claim exceeded requested limit",
		)
	}
}

// publicationWorkerLeasePrecedesHandlers validates the concrete production
// composition while leaving narrow test or alternate dispatcher adapters free
// to own their internal lease policy. The gateway composes these three handler
// types directly, so every real handoff is covered here.
func publicationWorkerLeasePrecedesHandlers(
	dispatcher *PublicationDispatcher,
	handoff time.Duration,
) bool {
	if dispatcher == nil || handoff <= 0 {
		return false
	}
	for _, handler := range []any{
		dispatcher.pending,
		dispatcher.gateWaiting,
		dispatcher.pushReady,
	} {
		var owned time.Duration
		switch typed := handler.(type) {
		case *PublicationPendingGateHandler:
			owned = typed.leaseDuration
		case *PublicationGateWaitingHandler:
			owned = typed.leaseDuration
		case *PublicationPushReadyHandler:
			owned = typed.leaseDuration
		default:
			continue
		}
		if owned <= handoff {
			return false
		}
	}
	return true
}

func publicationDispatcherComplete(dispatcher *PublicationDispatcher) bool {
	return dispatcher != nil &&
		dispatcher.pending != nil && !isNilServiceValue(dispatcher.pending) &&
		dispatcher.gateWaiting != nil && !isNilServiceValue(dispatcher.gateWaiting) &&
		dispatcher.pushReady != nil && !isNilServiceValue(dispatcher.pushReady)
}
