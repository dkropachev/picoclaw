package prdevelopment

import (
	"context"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

type publicationPushHeartbeatStore interface {
	RenewPRDevelopmentPublicationPush(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationRenew,
	) error
}

// publicationPushHeartbeat owns only the durable push-start lease. It is
// deliberately separate from the pre-effect queue heartbeat so the authority
// handoff is visible and a replayed push start can never regain Git authority.
type publicationPushHeartbeat struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	errs   chan error
	once   sync.Once
}

func startPublicationPushHeartbeat(
	ctx context.Context,
	store publicationPushHeartbeatStore,
	publication eventing.PRDevelopmentPublication,
	claim eventing.PRDevelopmentPublication,
	lease time.Duration,
) (*publicationPushHeartbeat, error) {
	workCtx, cancel := context.WithCancel(ctxOrBackground(ctx))
	heartbeat := &publicationPushHeartbeat{
		ctx: workCtx, cancel: cancel, done: make(chan struct{}), errs: make(chan error, 1),
	}
	renewCtx, renewCancel := publicationGateFinishContext(ctx)
	err := renewPublicationPushClaim(renewCtx, store, publication, claim, lease)
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
				if renewErr := renewPublicationPushClaim(
					workCtx,
					store,
					publication,
					claim,
					lease,
				); renewErr != nil {
					if workCtx.Err() != nil {
						return
					}
					select {
					case heartbeat.errs <- renewErr:
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

func renewPublicationPushClaim(
	ctx context.Context,
	store publicationPushHeartbeatStore,
	publication eventing.PRDevelopmentPublication,
	claim eventing.PRDevelopmentPublication,
	lease time.Duration,
) error {
	if publication.ID != claim.ID || publication.ClaimEpoch != claim.ClaimEpoch {
		return ErrInvalidRequest
	}
	return store.RenewPRDevelopmentPublicationPush(
		ctx,
		eventing.PRDevelopmentPublicationRenew{
			PublicationID: publication.ID,
			ClaimToken:    claim.ClaimToken,
			ClaimEpoch:    claim.ClaimEpoch,
			Lease:         lease,
		},
	)
}

func (heartbeat *publicationPushHeartbeat) Context() context.Context {
	if heartbeat == nil || heartbeat.ctx == nil {
		return context.Background()
	}
	return heartbeat.ctx
}

func (heartbeat *publicationPushHeartbeat) Stop() error {
	if heartbeat == nil {
		return nil
	}
	heartbeat.once.Do(func() {
		heartbeat.cancel()
		<-heartbeat.done
	})
	select {
	case err := <-heartbeat.errs:
		return err
	default:
		return nil
	}
}
