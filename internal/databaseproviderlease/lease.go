// Package databaseproviderlease carries one deadline-bound, target-bound
// authority from physical claims into the SQLite provider without making
// either package depend on the other.
package databaseproviderlease

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	maximumLifetime = 10 * time.Minute
	maximumPathSize = 16 << 10
)

var (
	errUnavailable = database.NewError(database.CodeUnavailable, "database provider lease is unavailable")
	errRevoked     = database.NewError(database.CodeUnavailable, "database provider lease was revoked")
	errConsumed    = database.NewError(database.CodeConflict, "database provider lease was already consumed")
	errDrain       = database.NewError(database.CodeUnavailable, "database provider lease did not drain")
	errScopeEnded  = errors.New("database provider lease scope ended")
)

// Hooks bind provider operations back to the exact migration refreshing
// guard that minted a Lease. Callers must not hold their own mutex while a
// hook runs.
type Hooks struct {
	Check                func(context.Context) error
	Reconcile            func(context.Context) error
	PinReplacement       func(context.Context, string) error
	DiscardReplacement   func(context.Context) error
	ReconcileReplacement func(context.Context) error
}

// Lease is a copy-safe, opaque, one-consumer migration capability. Only the
// claims layer constructs and revokes it; only the provider consumes it.
type Lease struct {
	state *leaseState
}

// Access is valid only during the synchronous callback admitted by Consume.
// Copies share the same revocable scope.
type Access struct {
	state        *leaseState
	scope        uint64
	caller       context.Context
	scopeContext context.Context
}

type leaseState struct {
	mu sync.Mutex

	id       database.StoreID
	target   string
	hooks    Hooks
	ctx      context.Context
	consumer context.Context
	cancel   context.CancelCauseFunc
	deadline time.Time

	consumed        bool
	revoked         bool
	callbackRunning bool
	scope           uint64
	inFlight        int
	done            chan struct{}
	doneClosed      bool
	revokeCause     error
	nextCancel      uint64
	activeCancels   map[uint64]trackedCancel
}

type trackedCancel struct {
	cancel         context.CancelCauseFunc
	preferredCause func() error
}

// New constructs a one-shot child lease. The supplied context must have a
// future finite deadline; longer deadlines are clamped to ten minutes.
func New(
	parent context.Context,
	id database.StoreID,
	target string,
	hooks Hooks,
) (*Lease, error) {
	if parent == nil || !id.Valid() || !validTarget(target) || !validHooks(hooks) {
		return nil, database.NewError(
			database.CodeInvalid,
			"database provider lease input is invalid",
		)
	}
	if cause := context.Cause(parent); cause != nil {
		return nil, cause
	}
	deadline, bounded := parent.Deadline()
	if !bounded || !deadline.After(time.Now()) {
		return nil, database.NewError(
			database.CodeInvalid,
			"database provider lease requires a future deadline",
		)
	}
	maximumDeadline := time.Now().Add(maximumLifetime)
	boundedParent := parent
	cancelDeadline := func() {}
	if deadline.After(maximumDeadline) {
		deadline = maximumDeadline
		boundedParent, cancelDeadline = context.WithDeadline(parent, deadline)
	}
	leaseContext, cancelCause := context.WithCancelCause(boundedParent)
	state := &leaseState{
		id: id, target: target, hooks: hooks,
		ctx: leaseContext,
		cancel: func(cause error) {
			cancelCause(cause)
			cancelDeadline()
		},
		deadline:      deadline,
		done:          make(chan struct{}),
		activeCancels: make(map[uint64]trackedCancel),
	}
	context.AfterFunc(leaseContext, func() {
		state.revoke(context.Cause(leaseContext))
	})
	return &Lease{state: state}, nil
}

// Consume admits exactly one provider callback and revokes Access when it
// returns. After revocation, it waits unconditionally for every admitted
// operation to drain; a cancellation-ignoring provider operation deliberately
// fail-stops the caller rather than releasing unproved authority.
func Consume(
	caller context.Context,
	lease *Lease,
	use func(context.Context, Access) error,
) (resultErr error) {
	if caller == nil || lease == nil || lease.state == nil || use == nil {
		return database.NewError(
			database.CodeInvalid,
			"database provider lease consumption is invalid",
		)
	}
	if cause := context.Cause(caller); cause != nil {
		return cause
	}
	state := lease.state
	state.mu.Lock()
	if state.consumed {
		state.mu.Unlock()
		return errConsumed
	}
	if state.revoked || state.ctx.Err() != nil {
		cause := state.causeLocked()
		state.mu.Unlock()
		return cause
	}
	state.consumed = true
	state.callbackRunning = true
	state.consumer = caller
	state.scope++
	scope := state.scope
	state.mu.Unlock()

	callbackContext, cancelCallback, stopLease := linkedCauseContext(caller, state.ctx)
	stopCallbackCancel := state.trackCancel(cancelCallback, func() error {
		return context.Cause(caller)
	})
	stopCaller := context.AfterFunc(caller, func() {
		state.revoke(context.Cause(caller))
	})
	if cause := context.Cause(caller); cause != nil {
		state.revoke(cause)
	}
	defer func() {
		stopCaller()
		stopLease()
		state.finishCallback(scope, context.Cause(caller))
		stopCallbackCancel()
		cancelCallback(errScopeEnded)
		state.wait()
		state.mu.Lock()
		cause := state.revokeCause
		state.mu.Unlock()
		if cause != nil && !errors.Is(cause, errScopeEnded) {
			resultErr = errors.Join(resultErr, cause)
		}
	}()
	return use(callbackContext, Access{
		state: state, scope: scope, caller: caller, scopeContext: callbackContext,
	})
}

// Revoke synchronously closes admission and cancels the active provider
// context, but never waits for callback or operation drain.
func Revoke(lease *Lease, cause error) error {
	if lease == nil || lease.state == nil {
		return errUnavailable
	}
	if cause == nil {
		cause = errRevoked
	}
	lease.state.revoke(cause)
	return nil
}

// Wait blocks until revocation and every admitted callback/operation have
// returned, or until ctx ends. It never revokes a still-live lease itself.
func Wait(ctx context.Context, lease *Lease) error {
	if ctx == nil || lease == nil || lease.state == nil {
		return database.NewError(
			database.CodeInvalid,
			"database provider lease wait is invalid",
		)
	}
	state := lease.state
	state.mu.Lock()
	done := state.done
	state.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.Join(errDrain, context.Cause(ctx))
	}
}

// Target returns the exact logical and physical target while this Access is
// live. It exposes no mutable catalog object.
func (access Access) Target() (database.StoreID, string, error) {
	state, release, err := access.begin(nil)
	if err != nil {
		return "", "", err
	}
	defer release()
	return state.id, state.target, nil
}

// Check invokes the originating migration guard's authority check.
func (access Access) Check(ctx context.Context) error {
	return access.invoke(ctx, func(state *leaseState, operationContext context.Context) error {
		return state.hooks.Check(operationContext)
	})
}

// Reconcile invokes ordinary non-main physical reconciliation.
func (access Access) Reconcile(ctx context.Context) error {
	return access.invoke(ctx, func(state *leaseState, operationContext context.Context) error {
		return state.hooks.Reconcile(operationContext)
	})
}

// PinReplacement binds one exact provider-owned stage to this lease's target.
func (access Access) PinReplacement(ctx context.Context, path string) error {
	if !validTarget(path) {
		return database.NewError(
			database.CodeInvalid,
			"database provider replacement path is invalid",
		)
	}
	return access.invoke(ctx, func(state *leaseState, operationContext context.Context) error {
		return state.hooks.PinReplacement(operationContext, path)
	})
}

// DiscardReplacement retires an exact unused replacement pin before the
// provider removes a stage after a known pre-cutover failure.
func (access Access) DiscardReplacement(ctx context.Context) error {
	return access.invoke(ctx, func(state *leaseState, operationContext context.Context) error {
		return state.hooks.DiscardReplacement(operationContext)
	})
}

// ReconcileReplacement promotes the exact replacement previously pinned for
// this lease's immutable StoreID.
func (access Access) ReconcileReplacement(ctx context.Context) error {
	return access.invoke(ctx, func(state *leaseState, operationContext context.Context) error {
		return state.hooks.ReconcileReplacement(operationContext)
	})
}

func (access Access) invoke(
	caller context.Context,
	operation func(*leaseState, context.Context) error,
) (resultErr error) {
	if caller == nil || operation == nil {
		return database.NewError(
			database.CodeInvalid,
			"database provider lease operation is invalid",
		)
	}
	state, release, err := access.begin(caller)
	if err != nil {
		return err
	}
	defer release()
	// The consuming callback is the authority parent so its cancellation and an
	// explicit revoke synchronously reach every admitted operation. Overlay only
	// the operation caller's values; its cancellation/deadline are linked below.
	operationParent := fallbackValueContext{
		Context:  caller,
		fallback: context.WithoutCancel(access.scopeContext),
	}
	operationContext, cancelOperation, stopScope := linkedCauseContext(
		operationParent, access.scopeContext,
	)
	stopOperationCancel := state.trackCancel(cancelOperation, func() error {
		return context.Cause(caller)
	})
	defer func() {
		stopScope()
		stopOperationCancel()
		cancelOperation(errScopeEnded)
	}()
	resultErr = operation(state, operationContext)
	callerCause := context.Cause(caller)
	leaseCause := context.Cause(state.ctx)
	state.mu.Lock()
	cause := state.revokeCause
	state.mu.Unlock()
	if callerCause != nil && !errors.Is(resultErr, callerCause) {
		resultErr = errors.Join(resultErr, callerCause)
	}
	if leaseCause != nil && !errors.Is(leaseCause, errScopeEnded) &&
		!errors.Is(resultErr, leaseCause) {
		resultErr = errors.Join(resultErr, leaseCause)
	}
	if cause != nil && !errors.Is(resultErr, cause) {
		resultErr = errors.Join(resultErr, cause)
	}
	return resultErr
}

func (access Access) begin(caller context.Context) (*leaseState, func(), error) {
	if access.state == nil || access.caller == nil || access.scopeContext == nil {
		return nil, nil, errUnavailable
	}
	state := access.state
	state.mu.Lock()
	if cause := context.Cause(access.caller); cause != nil {
		state.mu.Unlock()
		return nil, nil, cause
	}
	if cause := context.Cause(access.scopeContext); cause != nil {
		state.mu.Unlock()
		return nil, nil, externalCause(cause)
	}
	if caller != nil {
		if cause := context.Cause(caller); cause != nil {
			state.mu.Unlock()
			return nil, nil, cause
		}
	}
	if !state.callbackRunning || state.scope != access.scope || state.revoked ||
		state.ctx.Err() != nil {
		cause := state.causeLocked()
		state.mu.Unlock()
		return nil, nil, cause
	}
	state.inFlight++
	state.mu.Unlock()
	var once sync.Once
	return state, func() {
		once.Do(func() { state.finishOperation() })
	}, nil
}

func (state *leaseState) finishOperation() {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.inFlight > 0 {
		state.inFlight--
	}
	state.closeDoneLocked()
}

func (state *leaseState) finishCallback(scope uint64, callerCause error) {
	state.mu.Lock()
	if state.scope == scope && state.callbackRunning {
		state.callbackRunning = false
		cause := callerCause
		if cause == nil {
			cause = context.Cause(state.ctx)
		}
		if cause == nil {
			cause = errScopeEnded
		}
		state.revokeLocked(cause)
	}
	state.closeDoneLocked()
	state.mu.Unlock()
}

func (state *leaseState) revoke(cause error) {
	state.mu.Lock()
	state.revokeLocked(cause)
	state.closeDoneLocked()
	state.mu.Unlock()
}

func (state *leaseState) revokeLocked(cause error) {
	if state.revoked {
		return
	}
	// Parent cancellation or the lease deadline may have ended the context
	// before its AfterFunc acquired the lifecycle mutex. Preserve that earlier
	// authority-ending cause rather than letting a later explicit revoke mask it.
	if contextCause := context.Cause(state.ctx); contextCause != nil {
		cause = contextCause
	} else if state.consumer != nil {
		if consumerCause := context.Cause(state.consumer); consumerCause != nil {
			cause = consumerCause
		}
	}
	if cause == nil {
		cause = errRevoked
	}
	state.revoked = true
	state.revokeCause = cause
	state.cancel(cause)
	for _, tracked := range state.activeCancels {
		trackedCause := cause
		if tracked.preferredCause != nil {
			if preferred := tracked.preferredCause(); preferred != nil {
				trackedCause = preferred
			}
		}
		tracked.cancel(trackedCause)
	}
}

func (state *leaseState) trackCancel(
	cancel context.CancelCauseFunc,
	preferredCause func() error,
) func() {
	state.mu.Lock()
	if !state.revoked {
		if cause := context.Cause(state.ctx); cause != nil {
			state.revokeLocked(cause)
			state.closeDoneLocked()
		}
	}
	if state.revoked {
		cause := state.causeLocked()
		state.mu.Unlock()
		if preferredCause != nil {
			if preferred := preferredCause(); preferred != nil {
				cause = preferred
			}
		}
		cancel(cause)
		return func() {}
	}
	state.nextCancel++
	id := state.nextCancel
	if state.activeCancels == nil {
		state.activeCancels = make(map[uint64]trackedCancel)
	}
	state.activeCancels[id] = trackedCancel{
		cancel: cancel, preferredCause: preferredCause,
	}
	state.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			state.mu.Lock()
			delete(state.activeCancels, id)
			state.mu.Unlock()
		})
	}
}

func (state *leaseState) closeDoneLocked() {
	if state.doneClosed || !state.revoked || state.callbackRunning || state.inFlight != 0 {
		return
	}
	state.doneClosed = true
	close(state.done)
}

func (state *leaseState) causeLocked() error {
	if state.revokeCause != nil {
		if errors.Is(state.revokeCause, errScopeEnded) {
			return errRevoked
		}
		return state.revokeCause
	}
	if cause := context.Cause(state.ctx); cause != nil {
		return cause
	}
	return errUnavailable
}

func externalCause(cause error) error {
	if errors.Is(cause, errScopeEnded) {
		return errRevoked
	}
	return cause
}

func (state *leaseState) wait() {
	state.mu.Lock()
	done := state.done
	state.mu.Unlock()
	<-done
}

func linkedCauseContext(
	primary context.Context,
	secondary context.Context,
) (context.Context, context.CancelCauseFunc, func() bool) {
	bounded := primary
	if secondaryDeadline, ok := secondary.Deadline(); ok {
		primaryDeadline, primaryBounded := primary.Deadline()
		if !primaryBounded || secondaryDeadline.Before(primaryDeadline) {
			bounded = deadlineOverlayContext{Context: primary, deadline: secondaryDeadline}
		}
	}
	ctx, cancel := context.WithCancelCause(bounded)
	stop := context.AfterFunc(secondary, func() {
		cancel(context.Cause(secondary))
	})
	if cause := context.Cause(secondary); cause != nil {
		cancel(cause)
	}
	return ctx, cancel, stop
}

type deadlineOverlayContext struct {
	context.Context
	deadline time.Time
}

func (ctx deadlineOverlayContext) Deadline() (time.Time, bool) {
	return ctx.deadline, true
}

type fallbackValueContext struct {
	context.Context
	fallback context.Context
}

func (ctx fallbackValueContext) Value(key any) any {
	if value := ctx.Context.Value(key); value != nil {
		return value
	}
	return ctx.fallback.Value(key)
}

func validHooks(hooks Hooks) bool {
	return hooks.Check != nil && hooks.Reconcile != nil && hooks.PinReplacement != nil &&
		hooks.DiscardReplacement != nil && hooks.ReconcileReplacement != nil
}

func validTarget(path string) bool {
	return path != "" && len(path) <= maximumPathSize && path == strings.TrimSpace(path) &&
		utf8.ValidString(path) && !strings.ContainsRune(path, 0) && filepath.IsAbs(path) &&
		filepath.Clean(path) == path
}
