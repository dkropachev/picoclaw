package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
)

type runtimeLeaseContextKey struct{}

type runtimeLeaseContextValue struct {
	owner *AgentLoop
}

var errAgentRuntimeStopped = fmt.Errorf("agent runtime is stopped")

func runtimeLeaseOwner(ctx context.Context) *AgentLoop {
	if ctx == nil {
		return nil
	}
	value, _ := ctx.Value(runtimeLeaseContextKey{}).(runtimeLeaseContextValue)
	return value.owner
}

func (al *AgentLoop) acquireRuntimeUse(
	ctx context.Context,
) (context.Context, func(), error) {
	if al == nil {
		return ctx, func() {}, fmt.Errorf("agent loop not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if runtimeLeaseOwner(ctx) == al {
		return ctx, func() {}, nil
	}
	if err := ctx.Err(); err != nil {
		return ctx, func() {}, err
	}

	al.runtimeGateMu.Lock()
	al.ensureRuntimeGateChangedLocked()
	for al.runtimeGatePaused {
		if al.runtimeGateStopped {
			al.runtimeGateMu.Unlock()
			return ctx, func() {}, errAgentRuntimeStopped
		}
		changed := al.runtimeGateChanged
		al.runtimeGateMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx, func() {}, ctx.Err()
		case <-changed:
		}
		al.runtimeGateMu.Lock()
		al.ensureRuntimeGateChangedLocked()
	}
	if al.runtimeGateStopped {
		al.runtimeGateMu.Unlock()
		return ctx, func() {}, errAgentRuntimeStopped
	}
	al.runtimeGateActive++
	al.runtimeGateMu.Unlock()

	leaseCtx := context.WithValue(
		ctx,
		runtimeLeaseContextKey{},
		runtimeLeaseContextValue{owner: al},
	)
	return leaseCtx, al.newRuntimeUseRelease(), nil
}

// retainRuntimeUse extends a runtime generation already owned by ctx. It is
// used before launching asynchronous work so reload cannot observe the parent
// as drained between goroutine scheduling and child admission.
func (al *AgentLoop) retainRuntimeUse(
	ctx context.Context,
) (context.Context, func(), error) {
	if runtimeLeaseOwner(ctx) != al {
		return al.acquireRuntimeUse(ctx)
	}
	al.runtimeGateMu.Lock()
	al.ensureRuntimeGateChangedLocked()
	al.runtimeGateActive++
	al.signalRuntimeGateChangedLocked()
	al.runtimeGateMu.Unlock()
	return ctx, al.newRuntimeUseRelease(), nil
}

// AcquireRuntimeGeneration admits background work only while expected is the
// active config generation. Once admitted, provider/config reload waits for
// the returned release function, so the generation cannot change underneath
// the caller.
func (al *AgentLoop) AcquireRuntimeGeneration(
	ctx context.Context,
	expected *config.Config,
) (context.Context, func(), error) {
	if expected == nil {
		return ctx, func() {}, fmt.Errorf("expected runtime config is required")
	}
	leaseCtx, release, err := al.acquireRuntimeUse(ctx)
	if err != nil {
		return ctx, func() {}, err
	}
	if al.GetConfig() != expected {
		release()
		return ctx, func() {}, fmt.Errorf("runtime config generation changed")
	}
	return leaseCtx, release, nil
}

func (al *AgentLoop) newRuntimeUseRelease() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			al.runtimeGateMu.Lock()
			if al.runtimeGateActive > 0 {
				al.runtimeGateActive--
			}
			al.signalRuntimeGateChangedLocked()
			al.runtimeGateMu.Unlock()
		})
	}
}

func (al *AgentLoop) pauseRuntimeUses(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	al.runtimeGateMu.Lock()
	al.ensureRuntimeGateChangedLocked()
	firstPause := al.runtimeGatePauses == 0
	al.runtimeGatePauses++
	al.runtimeGatePaused = true
	al.signalRuntimeGateChangedLocked()
	for al.runtimeGateActive > 0 {
		changed := al.runtimeGateChanged
		al.runtimeGateMu.Unlock()
		select {
		case <-ctx.Done():
			al.runtimeGateMu.Lock()
			if al.runtimeGatePauses > 0 {
				al.runtimeGatePauses--
			}
			al.runtimeGatePaused = al.runtimeGatePauses > 0
			al.signalRuntimeGateChangedLocked()
			al.runtimeGateMu.Unlock()
			return nil, ctx.Err()
		case <-changed:
		}
		al.runtimeGateMu.Lock()
		al.ensureRuntimeGateChangedLocked()
	}
	al.runtimeGateMu.Unlock()

	if firstPause {
		al.runtimeGateTransitionMu.Lock()
		err := al.setWorkflowAutomationsRunning(ctx, false)
		al.runtimeGateTransitionMu.Unlock()
		if err != nil {
			al.runtimeGateMu.Lock()
			if al.runtimeGatePauses > 0 {
				al.runtimeGatePauses--
			}
			al.runtimeGatePaused = al.runtimeGatePauses > 0
			al.signalRuntimeGateChangedLocked()
			al.runtimeGateMu.Unlock()
			return nil, err
		}
	}

	var once sync.Once
	return func() {
		once.Do(al.resumeRuntimePause)
	}, nil
}

// ReleaseRuntimeStartupBarrier opens the construction-time barrier installed by
// WithRuntimeStartupBarrier. It is idempotent.
func (al *AgentLoop) ReleaseRuntimeStartupBarrier() {
	if al == nil {
		return
	}
	al.runtimeGateMu.Lock()
	if !al.runtimeStartupBarrier {
		al.runtimeGateMu.Unlock()
		return
	}
	al.runtimeStartupBarrier = false
	al.runtimeGateMu.Unlock()
	al.resumeRuntimePause()
}

func (al *AgentLoop) resumeRuntimePause() {
	al.runtimeGateMu.Lock()
	if al.runtimeGatePauses > 0 {
		al.runtimeGatePauses--
	}
	finalResume := al.runtimeGatePauses == 0
	if !finalResume {
		al.runtimeGatePaused = true
		al.signalRuntimeGateChangedLocked()
		al.runtimeGateMu.Unlock()
		return
	}
	// Keep admission paused until the automation subscription for the final
	// config generation has been recreated.
	al.runtimeGatePaused = true
	al.signalRuntimeGateChangedLocked()
	al.runtimeGateMu.Unlock()

	al.runtimeGateTransitionMu.Lock()
	al.runtimeGateMu.Lock()
	stillFinal := al.runtimeGatePauses == 0
	al.runtimeGateMu.Unlock()
	if stillFinal {
		_ = al.setWorkflowAutomationsRunning(context.Background(), true)
	}
	al.runtimeGateMu.Lock()
	al.runtimeGatePaused = al.runtimeGatePauses > 0
	al.signalRuntimeGateChangedLocked()
	al.runtimeGateMu.Unlock()
	al.runtimeGateTransitionMu.Unlock()
}

func (al *AgentLoop) ensureRuntimeGateChangedLocked() {
	if al.runtimeGateChanged == nil {
		al.runtimeGateChanged = make(chan struct{})
	}
}

func (al *AgentLoop) signalRuntimeGateChangedLocked() {
	al.ensureRuntimeGateChangedLocked()
	close(al.runtimeGateChanged)
	al.runtimeGateChanged = make(chan struct{})
}
