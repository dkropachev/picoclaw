package prdevelopment

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

type controllerHeartbeatStore interface {
	RenewPRDevelopmentRepairOrchestration(
		context.Context,
		eventing.PRDevelopmentRepairOrchestrationRenew,
	) error
	RenewPRDevelopmentControllerLease(
		context.Context,
		eventing.PRDevelopmentControllerRenew,
	) error
}

type controllerHeartbeatLease struct {
	ControllerID string
	LeaseToken   string
	LeaseEpoch   int64
}

// controllerHeartbeat keeps the scheduling claim and, once acquired, the
// exclusive mutation lease alive under one cancellation boundary. Losing
// either credential cancels model/CI/Git work immediately; it never rotates or
// replaces authority itself.
type controllerHeartbeat struct {
	store      controllerHeartbeatStore
	attemptID  string
	claimToken string
	lease      time.Duration
	cancel     context.CancelFunc
	done       chan struct{}
	errs       chan error

	mu         sync.RWMutex
	controller controllerHeartbeatLease
}

func startControllerHeartbeat(
	parent context.Context,
	store controllerHeartbeatStore,
	attemptID, claimToken string,
	lease time.Duration,
) (context.Context, *controllerHeartbeat) {
	workCtx, cancel := context.WithCancel(parent)
	heartbeat := &controllerHeartbeat{
		store:      store,
		attemptID:  attemptID,
		claimToken: claimToken,
		lease:      lease,
		cancel:     cancel,
		done:       make(chan struct{}),
		errs:       make(chan error, 1),
	}
	go heartbeat.run(workCtx)
	return workCtx, heartbeat
}

func (heartbeat *controllerHeartbeat) SetController(
	controller eventing.PRDevelopmentController,
) {
	if heartbeat == nil {
		return
	}
	heartbeat.mu.Lock()
	heartbeat.controller = controllerHeartbeatLease{
		ControllerID: controller.ID,
		LeaseToken:   controller.LeaseToken,
		LeaseEpoch:   controller.LeaseEpoch,
	}
	heartbeat.mu.Unlock()
}

func (heartbeat *controllerHeartbeat) Stop() error {
	if heartbeat == nil {
		return nil
	}
	heartbeat.cancel()
	<-heartbeat.done
	select {
	case err := <-heartbeat.errs:
		return err
	default:
		return nil
	}
}

func (heartbeat *controllerHeartbeat) run(ctx context.Context) {
	defer close(heartbeat.done)
	interval := heartbeat.lease / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := heartbeat.renew(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				select {
				case heartbeat.errs <- err:
				default:
				}
				heartbeat.cancel()
				return
			}
		}
	}
}

func (heartbeat *controllerHeartbeat) renew(ctx context.Context) error {
	if err := heartbeat.store.RenewPRDevelopmentRepairOrchestration(
		ctx,
		eventing.PRDevelopmentRepairOrchestrationRenew{
			AttemptID:  heartbeat.attemptID,
			ClaimToken: heartbeat.claimToken,
			Lease:      heartbeat.lease,
		},
	); err != nil {
		return fmt.Errorf("renew repair orchestration claim: %w", err)
	}
	heartbeat.mu.RLock()
	controller := heartbeat.controller
	heartbeat.mu.RUnlock()
	if controller.ControllerID == "" {
		return nil
	}
	if err := heartbeat.store.RenewPRDevelopmentControllerLease(
		ctx,
		eventing.PRDevelopmentControllerRenew{
			ControllerID: controller.ControllerID,
			AttemptID:    heartbeat.attemptID,
			LeaseToken:   controller.LeaseToken,
			LeaseEpoch:   controller.LeaseEpoch,
			Lease:        heartbeat.lease,
		},
	); err != nil {
		return fmt.Errorf("renew repair controller lease: %w", err)
	}
	return nil
}
