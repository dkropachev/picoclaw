package prdevelopment

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

type controllerHeartbeatStore interface {
	RenewPRDevelopmentRepairOrchestration(
		ctx context.Context,
		input eventing.PRDevelopmentRepairOrchestrationRenew,
	) error
	RenewPRDevelopmentControllerLease(
		ctx context.Context,
		input eventing.PRDevelopmentControllerRenew,
	) error
	RenewPRDevelopmentControllerSuspendedResume(
		ctx context.Context,
		input eventing.PRDevelopmentControllerSuspendedResumeRenew,
	) error
}

type controllerHeartbeatLease struct {
	ControllerID string
	LeaseToken   string
	LeaseEpoch   int64
}

type controllerHeartbeatSuspendedResume struct {
	ControllerID string
	AttemptID    string
	SuspensionID string
	ClaimID      string
	ClaimToken   string
	ClaimEpoch   int64
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
	resume     controllerHeartbeatSuspendedResume
	// resumeTransition is published before the resume-finalization barrier
	// takes the write lock. A renewal overtaken by that barrier may then
	// suppress only its now-stale resume-claim error while orchestration
	// renewal remains live.
	resumeTransition atomic.Bool
	terminal         atomic.Bool
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
	if !heartbeat.terminal.Load() {
		heartbeat.controller = controllerHeartbeatLease{
			ControllerID: controller.ID,
			LeaseToken:   controller.LeaseToken,
			LeaseEpoch:   controller.LeaseEpoch,
		}
		heartbeat.resume = controllerHeartbeatSuspendedResume{}
		heartbeat.resumeTransition.Store(false)
	}
	heartbeat.mu.Unlock()
}

// SetSuspendedResume keeps the exact short-lived resume claim alive while Git
// restores a bearer-free retained line. It never treats that claim as a
// normal mutation lease.
func (heartbeat *controllerHeartbeat) SetSuspendedResume(
	lease eventing.PRDevelopmentControllerSuspendedResumeLease,
) {
	if heartbeat == nil {
		return
	}
	resume := lease.Suspension
	heartbeat.mu.Lock()
	if !heartbeat.terminal.Load() {
		heartbeat.controller = controllerHeartbeatLease{}
		heartbeat.resume = controllerHeartbeatSuspendedResume{
			ControllerID: resume.ControllerID,
			AttemptID:    resume.ResumeAttemptID,
			SuspensionID: resume.ID,
			ClaimID:      resume.ResumeClaimID,
			ClaimToken:   resume.ResumeClaimToken,
			ClaimEpoch:   resume.ResumeClaimEpoch,
		}
		heartbeat.resumeTransition.Store(false)
	}
	heartbeat.mu.Unlock()
}

// BeginResumeTransition stops future resume-claim renewal and drains a
// renewal already in flight without stopping orchestration renewal. The
// caller can then atomically replace the consumed resume claim with the
// mutation lease returned by store finalization.
func (heartbeat *controllerHeartbeat) BeginResumeTransition() {
	if heartbeat == nil {
		return
	}
	heartbeat.resumeTransition.Store(true)
	heartbeat.mu.Lock()
	heartbeat.resume = controllerHeartbeatSuspendedResume{}
	heartbeat.mu.Unlock()
}

// BeginTerminal pauses future claim and mutation renewals and drains any
// renewal already in flight. It deliberately leaves the work context usable so
// the caller can perform the atomic terminal store transition after the
// barrier. Errors from a renewal overtaken by this barrier are stale and are
// therefore suppressed.
func (heartbeat *controllerHeartbeat) BeginTerminal() {
	if heartbeat == nil {
		return
	}
	// Publish the terminal intent before waiting for the write lock. An
	// in-flight renewal can then suppress a stale lease error while this lock
	// waits for its read-side critical section to drain.
	heartbeat.terminal.Store(true)
	heartbeat.mu.Lock()
	heartbeat.controller = controllerHeartbeatLease{}
	heartbeat.resume = controllerHeartbeatSuspendedResume{}
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
				heartbeat.mu.RLock()
				if heartbeat.terminal.Load() {
					heartbeat.mu.RUnlock()
					continue
				}
				select {
				case heartbeat.errs <- err:
				default:
				}
				heartbeat.cancel()
				heartbeat.mu.RUnlock()
				return
			}
		}
	}
}

func (heartbeat *controllerHeartbeat) renew(ctx context.Context) error {
	heartbeat.mu.RLock()
	defer heartbeat.mu.RUnlock()
	if heartbeat.terminal.Load() {
		return nil
	}
	if err := heartbeat.store.RenewPRDevelopmentRepairOrchestration(
		ctx,
		eventing.PRDevelopmentRepairOrchestrationRenew{
			AttemptID:  heartbeat.attemptID,
			ClaimToken: heartbeat.claimToken,
			Lease:      heartbeat.lease,
		},
	); err != nil {
		if heartbeat.terminal.Load() {
			return nil
		}
		return fmt.Errorf("renew repair orchestration claim: %w", err)
	}
	if heartbeat.terminal.Load() {
		return nil
	}
	resume := heartbeat.resume
	if resume.ControllerID != "" {
		if heartbeat.resumeTransition.Load() {
			return nil
		}
		if err := heartbeat.store.RenewPRDevelopmentControllerSuspendedResume(
			ctx,
			eventing.PRDevelopmentControllerSuspendedResumeRenew{
				ControllerID:            resume.ControllerID,
				AttemptID:               resume.AttemptID,
				SuspensionID:            resume.SuspensionID,
				OrchestrationClaimToken: heartbeat.claimToken,
				ClaimID:                 resume.ClaimID,
				ClaimToken:              resume.ClaimToken,
				ClaimEpoch:              resume.ClaimEpoch,
				Lease:                   heartbeat.lease,
			},
		); err != nil {
			if heartbeat.terminal.Load() || heartbeat.resumeTransition.Load() {
				return nil
			}
			return fmt.Errorf("renew suspended controller resume: %w", err)
		}
		return nil
	}
	controller := heartbeat.controller
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
		if heartbeat.terminal.Load() {
			return nil
		}
		return fmt.Errorf("renew repair controller lease: %w", err)
	}
	return nil
}
