package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
)

type runtimeLeaseContextKey struct{}

type runtimeLeaseKind uint8

const (
	runtimeLeaseKindInvalid runtimeLeaseKind = iota
	runtimeLeaseKindTrustedRoot
	runtimeLeaseKindRetainedOrigin
	runtimeLeaseKindOriginCurrent
	runtimeLeaseKindDetached
	runtimeLeaseKindStartup
	runtimeLeaseKindPauseOwner
	runtimeLeaseKindPausedCurrent
)

// runtimeGeneration is the exact immutable owner tuple installed in one
// AgentLoop generation. The pointed-to config and registry remain live only
// while a matching runtime lease or the reload pause owns their lifetime.
type runtimeGeneration struct {
	id               uint64
	cfg              *config.Config
	registry         *AgentRegistry
	executionPolicy  isolation.ExecutionPolicy
	diagnosticPolicy logger.DiagnosticPolicy
}

// runtimeDiagnosticOrigin is the only detached diagnostic provenance carried
// across a generation lifetime. Its fields are private so arbitrary callers
// cannot assert that a policy came from an admitted runtime lease.
type runtimeDiagnosticOrigin struct {
	policy logger.DiagnosticPolicy
	valid  bool
}

type runtimeLeaseContextValue struct {
	owner      *AgentLoop
	generation *runtimeGeneration
	boundary   *runtimeLeaseBoundary
	kind       runtimeLeaseKind
}

// runtimeLeaseBoundary is always present. Paused-current boundaries are
// live-linked to their generationless pause owner and deliberately do not own
// a runtimeGateActive count.
type runtimeLeaseBoundary struct {
	active atomic.Bool
	parent *runtimeLeaseBoundary
}

var (
	errAgentRuntimeStopped      = fmt.Errorf("agent runtime is stopped")
	errAgentRuntimeLeaseRevoked = fmt.Errorf("agent runtime lease is revoked")
)

func runtimeLeaseContextFrom(ctx context.Context) (runtimeLeaseContextValue, bool) {
	if ctx == nil {
		return runtimeLeaseContextValue{}, false
	}
	value, ok := ctx.Value(runtimeLeaseContextKey{}).(runtimeLeaseContextValue)
	return value, ok && value.owner != nil && value.boundary != nil
}

func (boundary *runtimeLeaseBoundary) live() bool {
	for depth := 0; boundary != nil && depth < 64; depth++ {
		if !boundary.active.Load() {
			return false
		}
		boundary = boundary.parent
	}
	return boundary == nil
}

func (value runtimeLeaseContextValue) live() bool {
	return value.owner != nil && value.boundary != nil && value.boundary.live()
}

func runtimeLeaseOwner(ctx context.Context) *AgentLoop {
	value, ok := runtimeLeaseContextFrom(ctx)
	if !ok || !value.live() {
		return nil
	}
	return value.owner
}

func newRuntimeLeaseBoundary(parent *runtimeLeaseBoundary) *runtimeLeaseBoundary {
	boundary := &runtimeLeaseBoundary{parent: parent}
	boundary.active.Store(true)
	return boundary
}

func (al *AgentLoop) snapshotRuntimeGeneration() (*runtimeGeneration, error) {
	if al == nil {
		return nil, fmt.Errorf("agent loop not configured")
	}
	al.mu.Lock()
	// Some internal compatibility fixtures construct a complete unpublished
	// AgentLoop literal rather than using newAgentLoop. Treat its first complete
	// cfg/registry pair as generation 1. Production constructors already assign
	// 1, and every later publication increments, so this cannot create an ABA
	// identity or make an incomplete loop admissible.
	if al.runtimeGenerationID == 0 && al.cfg != nil && al.registry != nil {
		al.runtimeGenerationID = 1
	}
	generation := &runtimeGeneration{
		id:               al.runtimeGenerationID,
		cfg:              al.cfg,
		registry:         al.registry,
		executionPolicy:  al.executionPolicy,
		diagnosticPolicy: al.diagnosticPolicy,
	}
	al.mu.Unlock()
	if generation.id == 0 || generation.cfg == nil || generation.registry == nil {
		return nil, fmt.Errorf("runtime generation is not configured")
	}
	return generation, nil
}

// runtimeGenerationFromLease returns the tuple captured by one exact live
// lease. It never rereads mutable AgentLoop generation state.
func (al *AgentLoop) runtimeGenerationFromLease(
	ctx context.Context,
) (runtimeGeneration, error) {
	value, ok := runtimeLeaseContextFrom(ctx)
	if !ok || !value.live() {
		return runtimeGeneration{}, errAgentRuntimeLeaseRevoked
	}
	if value.owner != al {
		return runtimeGeneration{}, fmt.Errorf("runtime lease belongs to another agent loop")
	}
	if value.generation == nil || value.generation.id == 0 ||
		value.generation.cfg == nil || value.generation.registry == nil {
		return runtimeGeneration{}, fmt.Errorf("runtime lease is generationless")
	}
	return *value.generation, nil
}

func (al *AgentLoop) runtimeDiagnosticOriginFromLease(
	ctx context.Context,
) (runtimeDiagnosticOrigin, error) {
	if _, err := al.runtimeGenerationFromLease(ctx); err != nil {
		return runtimeDiagnosticOrigin{}, err
	}
	return runtimeDiagnosticOrigin{
		policy: logger.DiagnosticPolicyFromContext(ctx),
		valid:  true,
	}, nil
}

func (al *AgentLoop) rejectLiveForeignRuntime(ctx context.Context) error {
	value, ok := runtimeLeaseContextFrom(ctx)
	if ok && value.live() && value.owner != al {
		return fmt.Errorf("runtime lease belongs to another agent loop")
	}
	return nil
}

type runtimeDiagnosticBinder func(
	context.Context,
	*runtimeGeneration,
) (context.Context, func())

func bindTrustedRuntimeDiagnostic(
	ctx context.Context,
	generation *runtimeGeneration,
) (context.Context, func()) {
	return logger.BindRootDiagnosticPolicy(ctx, generation.diagnosticPolicy)
}

func bindOriginCurrentRuntimeDiagnostic(
	ctx context.Context,
	_ *runtimeGeneration,
) (context.Context, func()) {
	// AcquireRuntimeGeneration has no private owner-issued origin when it reaches
	// this binder. A public logger binding, including one retained after revoke,
	// is not runtime authority and must not be laundered into the current
	// generation. Legitimate late work carries runtimeDiagnosticOrigin through
	// acquireRuntimeUseFromOrigin instead.
	return logger.BindRootDiagnosticPolicy(ctx, logger.DiagnosticPolicy{})
}

func bindDetachedRuntimeDiagnostic(
	ctx context.Context,
	_ *runtimeGeneration,
) (context.Context, func()) {
	return logger.BindRootDiagnosticPolicy(ctx, logger.DiagnosticPolicy{})
}

func (al *AgentLoop) incrementRuntimeAdmission(ctx context.Context) error {
	al.runtimeGateMu.Lock()
	al.ensureRuntimeGateChangedLocked()
	for al.runtimeGatePaused {
		if al.runtimeGateStopped {
			al.runtimeGateMu.Unlock()
			return errAgentRuntimeStopped
		}
		changed := al.runtimeGateChanged
		al.runtimeGateMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
		al.runtimeGateMu.Lock()
		al.ensureRuntimeGateChangedLocked()
	}
	if al.runtimeGateStopped {
		al.runtimeGateMu.Unlock()
		return errAgentRuntimeStopped
	}
	al.runtimeGateActive++
	al.signalRuntimeGateChangedLocked()
	al.runtimeGateMu.Unlock()
	return nil
}

func (al *AgentLoop) releaseRuntimeAdmissionCount() {
	al.runtimeGateMu.Lock()
	if al.runtimeGateActive > 0 {
		al.runtimeGateActive--
	}
	al.signalRuntimeGateChangedLocked()
	al.runtimeGateMu.Unlock()
}

func (al *AgentLoop) newRuntimeLeaseRelease(
	boundary *runtimeLeaseBoundary,
	revokeDiagnostic func(),
	counted bool,
) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			al.runtimeGateMu.Lock()
			if revokeDiagnostic != nil {
				revokeDiagnostic()
			}
			if boundary != nil {
				boundary.active.Store(false)
			}
			if counted && al.runtimeGateActive > 0 {
				al.runtimeGateActive--
			}
			al.signalRuntimeGateChangedLocked()
			al.runtimeGateMu.Unlock()
		})
	}
}

func (al *AgentLoop) acquireCountedRuntimeGeneration(
	ctx context.Context,
	kind runtimeLeaseKind,
	expected *config.Config,
	binder runtimeDiagnosticBinder,
) (context.Context, func(), error) {
	if al == nil {
		return ctx, func() {}, fmt.Errorf("agent loop not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ctx, func() {}, err
	}
	if err := al.rejectLiveForeignRuntime(ctx); err != nil {
		return ctx, func() {}, err
	}
	if err := al.incrementRuntimeAdmission(ctx); err != nil {
		return ctx, func() {}, err
	}
	generation, err := al.snapshotRuntimeGeneration()
	if err != nil || expected != nil && generation.cfg != expected {
		al.releaseRuntimeAdmissionCount()
		if err != nil {
			return ctx, func() {}, err
		}
		return ctx, func() {}, fmt.Errorf("runtime config generation changed")
	}
	boundary := newRuntimeLeaseBoundary(nil)
	leaseCtx, revokeDiagnostic := binder(ctx, generation)
	leaseCtx = context.WithValue(
		leaseCtx,
		runtimeLeaseContextKey{},
		runtimeLeaseContextValue{
			owner: al, generation: generation, boundary: boundary, kind: kind,
		},
	)
	return leaseCtx, al.newRuntimeLeaseRelease(boundary, revokeDiagnostic, true), nil
}

// acquireTrustedRuntimeRoot may establish the current generation policy only
// at a reviewed root. A same-loop lease is reused without rebinding; startup
// and other already-narrowed leases therefore remain narrow.
func (al *AgentLoop) acquireTrustedRuntimeRoot(
	ctx context.Context,
) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := al.rejectLiveForeignRuntime(ctx); err != nil {
		return ctx, func() {}, err
	}
	if value, ok := runtimeLeaseContextFrom(ctx); ok && value.live() {
		if value.owner != al {
			return ctx, func() {}, fmt.Errorf("runtime lease belongs to another agent loop")
		}
		if value.kind == runtimeLeaseKindPauseOwner || value.generation == nil {
			return ctx, func() {}, fmt.Errorf("generationless pause requires paused runtime callback")
		}
		return ctx, func() {}, nil
	}
	return al.acquireCountedRuntimeGeneration(
		ctx,
		runtimeLeaseKindTrustedRoot,
		nil,
		bindTrustedRuntimeDiagnostic,
	)
}

// acquireRuntimeUse is the temporary compatibility spelling for reviewed
// trusted/nested sites. P015b3a migrates non-root sites to the closed callees
// below and freezes them in the runtime admission manifest.
func (al *AgentLoop) acquireRuntimeUse(
	ctx context.Context,
) (context.Context, func(), error) {
	return al.acquireTrustedRuntimeRoot(ctx)
}

// acquireInheritedRuntimeUse requires an already-live exact same-loop lease.
func (al *AgentLoop) acquireInheritedRuntimeUse(
	ctx context.Context,
) (context.Context, func(), error) {
	if err := al.rejectLiveForeignRuntime(ctx); err != nil {
		return ctx, func() {}, err
	}
	value, ok := runtimeLeaseContextFrom(ctx)
	if !ok || !value.live() || value.owner != al || value.generation == nil {
		return ctx, func() {}, fmt.Errorf("inherited runtime lease is required")
	}
	return ctx, func() {}, nil
}

// acquireRuntimeUseFromOrigin admits the current generation and meets it with
// one detached, owner-issued origin. Missing origin is deliberately safe-only.
func (al *AgentLoop) acquireRuntimeUseFromOrigin(
	ctx context.Context,
	origin runtimeDiagnosticOrigin,
) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := al.rejectLiveForeignRuntime(ctx); err != nil {
		return ctx, func() {}, err
	}
	if value, ok := runtimeLeaseContextFrom(ctx); ok && value.live() {
		if value.owner != al || value.generation == nil || value.kind == runtimeLeaseKindPauseOwner {
			return ctx, func() {}, fmt.Errorf("origin/current runtime lease is invalid")
		}
		policy := logger.DiagnosticPolicy{}
		if origin.valid {
			policy = origin.policy.Meet(value.generation.diagnosticPolicy)
		}
		childCtx, revoke := logger.NarrowDiagnosticPolicy(ctx, policy)
		return childCtx, revoke, nil
	}
	binder := func(
		parent context.Context,
		generation *runtimeGeneration,
	) (context.Context, func()) {
		policy := logger.DiagnosticPolicy{}
		if origin.valid {
			policy = origin.policy.Meet(generation.diagnosticPolicy)
		}
		return logger.BindRootDiagnosticPolicy(parent, policy)
	}
	return al.acquireCountedRuntimeGeneration(
		ctx,
		runtimeLeaseKindOriginCurrent,
		nil,
		binder,
	)
}

// acquireSteeringRuntimeUse is the closed steering admission boundary. A nil
// origin is an reviewed synchronous trusted/nested continuation; a non-nil
// value is detached rescue provenance and can only meet the current policy.
func (al *AgentLoop) acquireSteeringRuntimeUse(
	ctx context.Context,
	origin *runtimeDiagnosticOrigin,
) (context.Context, func(), error) {
	if origin == nil {
		return al.acquireTrustedRuntimeRoot(ctx)
	}
	return al.acquireRuntimeUseFromOrigin(ctx, *origin)
}

func (al *AgentLoop) acquireDetachedRuntimeUse(
	ctx context.Context,
) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := al.rejectLiveForeignRuntime(ctx); err != nil {
		return ctx, func() {}, err
	}
	if value, ok := runtimeLeaseContextFrom(ctx); ok && value.live() {
		if value.owner != al || value.generation == nil || value.kind == runtimeLeaseKindPauseOwner {
			return ctx, func() {}, fmt.Errorf("detached runtime lease is invalid")
		}
		childCtx, revoke := logger.BindRootDiagnosticPolicy(ctx, logger.DiagnosticPolicy{})
		return childCtx, revoke, nil
	}
	return al.acquireCountedRuntimeGeneration(
		ctx,
		runtimeLeaseKindDetached,
		nil,
		bindDetachedRuntimeDiagnostic,
	)
}

// AcquireRuntimeStartupUse exclusively admits synchronous gateway setup while
// the construction-time startup barrier is the only runtime pause. Its request
// policy is always zero and it cannot be retained.
func (al *AgentLoop) AcquireRuntimeStartupUse(
	ctx context.Context,
	expected *config.Config,
) (context.Context, func(), error) {
	if al == nil {
		return ctx, func() {}, fmt.Errorf("agent loop not configured")
	}
	if expected == nil {
		return ctx, func() {}, fmt.Errorf("expected runtime config is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ctx, func() {}, err
	}
	if value, ok := runtimeLeaseContextFrom(ctx); ok && value.live() {
		return ctx, func() {}, fmt.Errorf("startup runtime use is already owned")
	}

	al.runtimeGateMu.Lock()
	al.ensureRuntimeGateChangedLocked()
	if !al.runtimeStartupBarrier || !al.runtimeGatePaused ||
		al.runtimeGatePauses != 1 || al.runtimeGateActive != 0 ||
		al.runtimeGateStopped {
		al.runtimeGateMu.Unlock()
		return ctx, func() {}, fmt.Errorf("startup runtime generation is not exclusively paused")
	}
	al.runtimeGateActive++
	al.signalRuntimeGateChangedLocked()
	al.runtimeGateMu.Unlock()

	generation, err := al.snapshotRuntimeGeneration()
	if err != nil || generation.cfg != expected {
		al.releaseRuntimeAdmissionCount()
		if err != nil {
			return ctx, func() {}, err
		}
		return ctx, func() {}, fmt.Errorf("runtime config generation changed")
	}
	boundary := newRuntimeLeaseBoundary(nil)
	leaseCtx, revokeDiagnostic := logger.BindRootDiagnosticPolicy(
		ctx,
		logger.DiagnosticPolicy{},
	)
	leaseCtx = context.WithValue(
		leaseCtx,
		runtimeLeaseContextKey{},
		runtimeLeaseContextValue{
			owner: al, generation: generation, boundary: boundary,
			kind: runtimeLeaseKindStartup,
		},
	)
	return leaseCtx, al.newRuntimeLeaseRelease(boundary, revokeDiagnostic, true), nil
}

// retainRuntimeUse creates an independently revocable retained lease. The
// parent boundary check, diagnostic rebind, active-count increment, and child
// publication linearize against parent release under runtimeGateMu.
func (al *AgentLoop) retainRuntimeUse(
	ctx context.Context,
) (context.Context, func(), error) {
	if al == nil {
		return ctx, func() {}, fmt.Errorf("agent loop not configured")
	}
	al.runtimeGateMu.Lock()
	value, ok := runtimeLeaseContextFrom(ctx)
	if !ok || !value.live() {
		al.runtimeGateMu.Unlock()
		return ctx, func() {}, fmt.Errorf("live runtime origin is required")
	}
	if value.owner != al {
		al.runtimeGateMu.Unlock()
		return ctx, func() {}, fmt.Errorf("runtime lease belongs to another agent loop")
	}
	if value.generation == nil || value.kind == runtimeLeaseKindStartup ||
		value.kind == runtimeLeaseKindPauseOwner ||
		value.kind == runtimeLeaseKindPausedCurrent ||
		value.kind == runtimeLeaseKindDetached {
		al.runtimeGateMu.Unlock()
		return ctx, func() {}, fmt.Errorf("runtime lease kind cannot be retained")
	}
	boundary := newRuntimeLeaseBoundary(nil)
	retainedCtx, revokeDiagnostic := logger.RebindDiagnosticPolicy(
		ctx,
		ctx,
		value.generation.diagnosticPolicy,
	)
	al.runtimeGateActive++
	al.signalRuntimeGateChangedLocked()
	al.runtimeGateMu.Unlock()
	retainedCtx = context.WithValue(
		retainedCtx,
		runtimeLeaseContextKey{},
		runtimeLeaseContextValue{
			owner: al, generation: value.generation, boundary: boundary,
			kind: runtimeLeaseKindRetainedOrigin,
		},
	)
	return retainedCtx, al.newRuntimeLeaseRelease(boundary, revokeDiagnostic, true), nil
}

// AcquireRuntimeGeneration admits background work only while expected is the
// active config generation. A live same-loop lease is reused. Every detached
// acquisition is safe-only; late work must use the private owner-issued origin
// boundary instead of inferring authority from a public logger binding.
func (al *AgentLoop) AcquireRuntimeGeneration(
	ctx context.Context,
	expected *config.Config,
) (context.Context, func(), error) {
	if expected == nil {
		return ctx, func() {}, fmt.Errorf("expected runtime config is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := al.rejectLiveForeignRuntime(ctx); err != nil {
		return ctx, func() {}, err
	}
	if value, ok := runtimeLeaseContextFrom(ctx); ok && value.live() {
		if value.owner != al || value.generation == nil ||
			value.generation.cfg != expected || value.kind == runtimeLeaseKindPauseOwner {
			return ctx, func() {}, fmt.Errorf("runtime config generation changed")
		}
		return ctx, func() {}, nil
	}
	return al.acquireCountedRuntimeGeneration(
		ctx,
		runtimeLeaseKindOriginCurrent,
		expected,
		bindOriginCurrentRuntimeDiagnostic,
	)
}

// withPausedRuntimeGeneration is the sole way to borrow the current tuple from
// a generationless pause owner. The child is synchronous, non-counting, and
// live-linked to the pause boundary; its context never escapes this API.
func (al *AgentLoop) withPausedRuntimeGeneration(
	ctx context.Context,
	run func(context.Context) error,
) error {
	if al == nil || run == nil {
		return fmt.Errorf("paused runtime callback is not configured")
	}
	al.runtimeGateMu.Lock()
	parent, ok := runtimeLeaseContextFrom(ctx)
	if !ok || !parent.live() || parent.owner != al ||
		parent.kind != runtimeLeaseKindPauseOwner || parent.generation != nil {
		al.runtimeGateMu.Unlock()
		return fmt.Errorf("live generationless pause owner is required")
	}
	boundary := newRuntimeLeaseBoundary(parent.boundary)
	al.runtimeGateMu.Unlock()

	generation, err := al.snapshotRuntimeGeneration()
	if err != nil {
		boundary.active.Store(false)
		return err
	}
	childCtx, revokeDiagnostic := logger.NarrowDiagnosticPolicy(
		ctx,
		logger.DiagnosticPolicy{},
	)
	childCtx = context.WithValue(
		childCtx,
		runtimeLeaseContextKey{},
		runtimeLeaseContextValue{
			owner: al, generation: generation, boundary: boundary,
			kind: runtimeLeaseKindPausedCurrent,
		},
	)
	release := al.newRuntimeLeaseRelease(boundary, revokeDiagnostic, false)
	defer release()
	if !boundary.live() {
		return errAgentRuntimeLeaseRevoked
	}
	return run(childCtx)
}

// WithPausedRuntimeGeneration runs one synchronous safe-only callback against
// the exact generation current beneath a live generationless reload pause.
// The child context is non-counting, non-retainable, and revoked on return.
func (al *AgentLoop) WithPausedRuntimeGeneration(
	ctx context.Context,
	run func(context.Context) error,
) error {
	return al.withPausedRuntimeGeneration(ctx, run)
}

func (al *AgentLoop) pauseRuntimeUsesWithContext(
	waitCtx context.Context,
	runtimeCtx context.Context,
) (context.Context, func(), error) {
	if al == nil {
		return runtimeCtx, nil, fmt.Errorf("agent loop not configured")
	}
	if runtimeCtx == nil {
		runtimeCtx = context.Background()
	}
	if err := runtimeCtx.Err(); err != nil {
		return runtimeCtx, nil, err
	}
	if value, ok := runtimeLeaseContextFrom(runtimeCtx); ok && value.live() {
		return runtimeCtx, nil, fmt.Errorf("cannot pause from an owned runtime context")
	}
	resumeRuntime, err := al.pauseRuntimeUses(waitCtx)
	if err != nil {
		return runtimeCtx, nil, err
	}

	boundary := newRuntimeLeaseBoundary(nil)
	ownedCtx, revokeDiagnostic := logger.BindRootDiagnosticPolicy(
		runtimeCtx,
		logger.DiagnosticPolicy{},
	)
	ownedCtx = context.WithValue(
		ownedCtx,
		runtimeLeaseContextKey{},
		runtimeLeaseContextValue{
			owner: al, boundary: boundary, kind: runtimeLeaseKindPauseOwner,
		},
	)
	releaseOwner := al.newRuntimeLeaseRelease(boundary, revokeDiagnostic, false)
	var once sync.Once
	return ownedCtx, func() {
		once.Do(func() {
			releaseOwner()
			resumeRuntime()
		})
	}, nil
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

// ReleaseRuntimeStartupBarrier opens the construction-time barrier installed
// by WithRuntimeStartupBarrier. It is idempotent.
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
