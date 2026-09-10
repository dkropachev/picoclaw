package databaseproviderlease

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

type hookRecorder struct {
	checks       atomic.Int32
	reconciles   atomic.Int32
	pins         atomic.Int32
	replacements atomic.Int32
	err          error
	path         string
}

func (recorder *hookRecorder) hooks() Hooks {
	return Hooks{
		Check: func(ctx context.Context) error {
			recorder.checks.Add(1)
			return errors.Join(ctx.Err(), recorder.err)
		},
		Reconcile: func(ctx context.Context) error {
			recorder.reconciles.Add(1)
			return errors.Join(ctx.Err(), recorder.err)
		},
		PinReplacement: func(ctx context.Context, path string) error {
			recorder.pins.Add(1)
			recorder.path = path
			return errors.Join(ctx.Err(), recorder.err)
		},
		ReconcileReplacement: func(ctx context.Context) error {
			recorder.replacements.Add(1)
			return errors.Join(ctx.Err(), recorder.err)
		},
	}
}

func newTestLease(t *testing.T, recorder *hookRecorder) *Lease {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	lease, err := New(ctx, "global/auth", filepath.Join(t.TempDir(), "auth.db"), recorder.hooks())
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func TestLeaseConsumesOneTargetBoundScope(t *testing.T) {
	recorder := new(hookRecorder)
	lease := newTestLease(t, recorder)
	var retained Access
	replacement := filepath.Join(t.TempDir(), "stage.db")
	err := Consume(t.Context(), lease, func(ctx context.Context, access Access) error {
		retained = access
		id, target, err := access.Target()
		if err != nil || id != "global/auth" || target != lease.state.target {
			return errors.New("provider lease target changed")
		}
		copyOfAccess := access
		if err := copyOfAccess.Check(ctx); err != nil {
			return err
		}
		if err := access.Reconcile(ctx); err != nil {
			return err
		}
		if err := access.PinReplacement(ctx, replacement); err != nil {
			return err
		}
		return access.ReconcileReplacement(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	if recorder.checks.Load() != 1 || recorder.reconciles.Load() != 1 ||
		recorder.pins.Load() != 1 || recorder.replacements.Load() != 1 ||
		recorder.path != replacement {
		t.Fatalf("hook calls = %#v", recorder)
	}
	if _, _, err := retained.Target(); !errors.Is(err, errRevoked) {
		t.Fatalf("retained target = %v", err)
	}
	if err := retained.Check(t.Context()); !errors.Is(err, errRevoked) {
		t.Fatalf("retained Check = %v", err)
	}
	if err := Consume(
		t.Context(), lease, func(context.Context, Access) error { return nil },
	); !errors.Is(err, errConsumed) {
		t.Fatalf("second Consume = %v", err)
	}
	if err := Wait(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseRevokeIsNonblockingAndWaitsForCallbackAndOperations(t *testing.T) {
	enteredHook := make(chan struct{})
	callbackContexts := make(chan context.Context, 1)
	operationContexts := make(chan context.Context, 1)
	releaseHook := make(chan struct{})
	allowCallbackReturn := make(chan struct{})
	hooks := Hooks{
		Check: func(ctx context.Context) error {
			operationContexts <- ctx
			close(enteredHook)
			<-releaseHook
			return ctx.Err()
		},
		Reconcile:            func(context.Context) error { return nil },
		PinReplacement:       func(context.Context, string) error { return nil },
		ReconcileReplacement: func(context.Context) error { return nil },
	}
	parent, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	lease, err := New(parent, "global/auth", filepath.Join(t.TempDir(), "auth.db"), hooks)
	if err != nil {
		t.Fatal(err)
	}
	consumeResult := make(chan error, 1)
	callbackReturned := make(chan struct{})
	go func() {
		consumeResult <- Consume(context.Background(), lease, func(ctx context.Context, access Access) error {
			callbackContexts <- ctx
			operationResult := make(chan error, 1)
			go func() { operationResult <- access.Check(ctx) }()
			<-enteredHook
			close(callbackReturned)
			<-allowCallbackReturn
			return nil
		})
	}()
	<-callbackReturned
	callbackContext := <-callbackContexts
	operationContext := <-operationContexts
	started := time.Now()
	copyOfLease := *lease
	revokeCause := errors.New("migration guard released")
	if err := Revoke(&copyOfLease, revokeCause); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("Revoke waited for a blocked provider operation")
	}
	for name, ctx := range map[string]context.Context{
		"callback":  callbackContext,
		"operation": operationContext,
	} {
		select {
		case <-ctx.Done():
			if !errors.Is(context.Cause(ctx), revokeCause) {
				t.Fatalf("%s revoke cause = %v", name, context.Cause(ctx))
			}
		default:
			t.Fatalf("Revoke returned before canceling the %s context", name)
		}
	}
	short, cancelShort := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancelShort()
	if err := Wait(short, lease); !errors.Is(err, context.DeadlineExceeded) ||
		!errors.Is(err, errDrain) {
		t.Fatalf("early Wait = %v", err)
	}
	close(allowCallbackReturn)
	close(releaseHook)
	if err := <-consumeResult; !errors.Is(err, revokeCause) {
		t.Fatalf("revoked Consume = %v", err)
	}
	if err := Wait(t.Context(), &copyOfLease); err != nil {
		t.Fatal(err)
	}
	if err := Revoke(lease, errors.New("later")); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseContextsExposeEarliestDeadlinesAndOperationValues(t *testing.T) {
	type contextKey string
	const key contextKey = "provider-lease-value"
	parent, cancelParent := context.WithTimeout(context.Background(), time.Minute)
	defer cancelParent()
	operationSeen := make(chan struct{})
	hooks := new(hookRecorder).hooks()
	hooks.Check = func(ctx context.Context) error {
		if got := ctx.Value(key); got != "operation" {
			t.Errorf("operation context value = %#v", got)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 30*time.Second {
			t.Errorf("operation deadline = %v, %t", deadline, ok)
		}
		close(operationSeen)
		return nil
	}
	lease, err := New(
		parent,
		"global/auth",
		filepath.Join(t.TempDir(), "auth.db"),
		hooks,
	)
	if err != nil {
		t.Fatal(err)
	}
	caller := context.WithValue(context.Background(), key, "callback")
	err = Consume(caller, lease, func(ctx context.Context, access Access) error {
		if got := ctx.Value(key); got != "callback" {
			t.Fatalf("callback context value = %#v", got)
		}
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(lease.state.deadline) {
			t.Fatalf("callback deadline = %v, %t; want %v", deadline, ok, lease.state.deadline)
		}
		operationCaller := context.WithValue(context.Background(), key, "operation")
		operationCaller, cancelOperation := context.WithTimeout(operationCaller, 30*time.Second)
		defer cancelOperation()
		return access.Check(operationCaller)
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-operationSeen:
	default:
		t.Fatal("operation hook did not run")
	}
}

func TestLeaseDrainWaitsForEveryConcurrentOperation(t *testing.T) {
	const operations = 4
	entered := make(chan int, operations)
	releases := make([]chan struct{}, operations)
	for index := range releases {
		releases[index] = make(chan struct{})
	}
	var next atomic.Int32
	hooks := new(hookRecorder).hooks()
	hooks.Check = func(ctx context.Context) error {
		index := int(next.Add(1)) - 1
		entered <- index
		<-releases[index]
		return ctx.Err()
	}
	parent, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	lease, err := New(
		parent,
		"global/auth",
		filepath.Join(t.TempDir(), "auth.db"),
		hooks,
	)
	if err != nil {
		t.Fatal(err)
	}
	consumeResult := make(chan error, 1)
	callbackReturned := make(chan struct{})
	go func() {
		consumeResult <- Consume(context.Background(), lease, func(ctx context.Context, access Access) error {
			for range operations {
				go func() { _ = access.Check(ctx) }()
			}
			for range operations {
				<-entered
			}
			close(callbackReturned)
			return nil
		})
	}()
	<-callbackReturned
	for index := range releases {
		short, cancelShort := context.WithTimeout(context.Background(), 5*time.Millisecond)
		waitErr := Wait(short, lease)
		cancelShort()
		if !errors.Is(waitErr, errDrain) || !errors.Is(waitErr, context.DeadlineExceeded) {
			t.Fatalf("Wait before operation %d release = %v", index, waitErr)
		}
		close(releases[index])
	}
	if err := <-consumeResult; err != nil && !errors.Is(err, errScopeEnded) {
		t.Fatalf("concurrent operation Consume = %v", err)
	}
	if err := Wait(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseDeadlineAndCallerCancellationFailClosed(t *testing.T) {
	recorder := new(hookRecorder)
	longParent, cancelLong := context.WithTimeout(context.Background(), time.Hour)
	defer cancelLong()
	lease, err := New(
		longParent, "global/auth", filepath.Join(t.TempDir(), "auth.db"), recorder.hooks(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(lease.state.deadline); remaining <= 0 || remaining > maximumLifetime {
		t.Fatalf("clamped lifetime = %s", remaining)
	}
	if revokeErr := Revoke(lease, nil); revokeErr != nil {
		t.Fatal(revokeErr)
	}
	if waitErr := Wait(t.Context(), lease); waitErr != nil {
		t.Fatal(waitErr)
	}
	if consumeErr := Consume(
		t.Context(), lease, func(context.Context, Access) error { return nil },
	); !errors.Is(consumeErr, errRevoked) {
		t.Fatalf("revoked-before-consume = %v", consumeErr)
	}

	deadlineParent, cancelDeadline := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelDeadline()
	expiring, err := New(
		deadlineParent, "global/auth", filepath.Join(t.TempDir(), "auth.db"), recorder.hooks(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := Wait(context.Background(), expiring); err != nil {
		t.Fatal(err)
	}
	if consumeErr := Consume(
		t.Context(), expiring, func(context.Context, Access) error { return nil },
	); !errors.Is(consumeErr, context.DeadlineExceeded) {
		t.Fatalf("expired Consume = %v", consumeErr)
	}

	caller, cancelCaller := context.WithCancelCause(context.Background())
	callerCause := errors.New("caller canceled before consume")
	cancelCaller(callerCause)
	fresh := newTestLease(t, recorder)
	if consumeErr := Consume(
		caller, fresh, func(context.Context, Access) error { return nil },
	); !errors.Is(consumeErr, callerCause) {
		t.Fatalf("canceled caller Consume = %v", consumeErr)
	}
	if err := Revoke(fresh, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLeasePreservesConstructionWaitAndRevocationCauses(t *testing.T) {
	constructionCause := errors.New("construction canceled")
	canceledParent, cancelConstruction := context.WithCancelCause(context.Background())
	cancelConstruction(constructionCause)
	deadlineParent, cancelDeadline := context.WithTimeout(canceledParent, time.Minute)
	defer cancelDeadline()
	if lease, err := New(
		deadlineParent,
		"global/auth",
		filepath.Join(t.TempDir(), "auth.db"),
		new(hookRecorder).hooks(),
	); lease != nil || !errors.Is(err, constructionCause) {
		t.Fatalf("custom-canceled New = %#v, %v", lease, err)
	}
	parentDeadlineCause := errors.New("parent custom deadline")
	deadlineCauseParent, cancelDeadlineCause := context.WithDeadlineCause(
		context.Background(), time.Now().Add(10*time.Millisecond), parentDeadlineCause,
	)
	defer cancelDeadlineCause()
	deadlineCauseLease, err := New(
		deadlineCauseParent,
		"global/auth",
		filepath.Join(t.TempDir(), "deadline-cause.db"),
		new(hookRecorder).hooks(),
	)
	if err != nil {
		t.Fatal(err)
	}
	err = Consume(context.Background(), deadlineCauseLease, func(ctx context.Context, _ Access) error {
		<-ctx.Done()
		return context.Cause(ctx)
	})
	if !errors.Is(err, parentDeadlineCause) {
		t.Fatalf("parent custom deadline Consume = %v", err)
	}

	waitLease := newTestLease(t, new(hookRecorder))
	waitCause := errors.New("wait canceled")
	waitContext, cancelWait := context.WithCancelCause(context.Background())
	cancelWait(waitCause)
	if waitErr := Wait(waitContext, waitLease); !errors.Is(waitErr, errDrain) ||
		!errors.Is(waitErr, waitCause) {
		t.Fatalf("custom-canceled Wait = %v", waitErr)
	}
	if revokeErr := Revoke(waitLease, nil); revokeErr != nil {
		t.Fatal(revokeErr)
	}

	parent, cancelParent := context.WithCancelCause(context.Background())
	bounded, cancelBounded := context.WithTimeout(parent, time.Minute)
	defer cancelBounded()
	lease, err := New(
		bounded,
		"global/auth",
		filepath.Join(t.TempDir(), "auth.db"),
		new(hookRecorder).hooks(),
	)
	if err != nil {
		t.Fatal(err)
	}
	parentCause := errors.New("parent authority ended")
	laterRevoke := errors.New("later explicit revoke")
	lease.state.mu.Lock()
	cancelParent(parentCause)
	lease.state.revokeLocked(laterRevoke)
	lease.state.closeDoneLocked()
	recorded := lease.state.revokeCause
	lease.state.mu.Unlock()
	if !errors.Is(recorded, parentCause) || errors.Is(recorded, laterRevoke) {
		t.Fatalf("recorded revocation cause = %v", recorded)
	}
	if err := Consume(
		context.Background(), lease, func(context.Context, Access) error { return nil },
	); !errors.Is(err, parentCause) || errors.Is(err, laterRevoke) {
		t.Fatalf("parent-before-revoke Consume = %v", err)
	}

	caller, cancelCaller := context.WithCancelCause(context.Background())
	callerLease := newTestLease(t, new(hookRecorder))
	callbackEntered := make(chan context.Context, 1)
	releaseCallback := make(chan struct{})
	consumeResult := make(chan error, 1)
	go func() {
		consumeResult <- Consume(caller, callerLease, func(ctx context.Context, _ Access) error {
			callbackEntered <- ctx
			<-releaseCallback
			return nil
		})
	}()
	callbackContext := <-callbackEntered
	callerCause := errors.New("consumer authority ended")
	laterRevoke = errors.New("later consumer revoke")
	callerLease.state.mu.Lock()
	cancelCaller(callerCause)
	callerLease.state.revokeLocked(laterRevoke)
	recorded = callerLease.state.revokeCause
	callerLease.state.mu.Unlock()
	if !errors.Is(recorded, callerCause) || errors.Is(recorded, laterRevoke) ||
		!errors.Is(context.Cause(callbackContext), callerCause) {
		t.Fatalf(
			"consumer-before-revoke causes = state %v, callback %v",
			recorded, context.Cause(callbackContext),
		)
	}
	close(releaseCallback)
	if err := <-consumeResult; !errors.Is(err, callerCause) || errors.Is(err, laterRevoke) {
		t.Fatalf("consumer-before-revoke Consume = %v", err)
	}
}

func TestLeasePropagatesCallbackAndHookErrors(t *testing.T) {
	callbackCanary := errors.New("callback canary")
	hookCanary := errors.New("hook canary")
	recorder := &hookRecorder{err: hookCanary}
	lease := newTestLease(t, recorder)
	err := Consume(t.Context(), lease, func(ctx context.Context, access Access) error {
		if err := access.Reconcile(ctx); !errors.Is(err, hookCanary) {
			t.Fatalf("hook error = %v", err)
		}
		return callbackCanary
	})
	if !errors.Is(err, callbackCanary) {
		t.Fatalf("callback result = %v", err)
	}
}

func TestLeaseRejectsInvalidConstructionAndOperations(t *testing.T) {
	validContext, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	recorder := new(hookRecorder)
	validPath := filepath.Join(t.TempDir(), "auth.db")
	invalidUTF8 := validPath + string([]byte{0xff})
	oversized := filepath.Join(t.TempDir(), strings.Repeat("a", maximumPathSize))
	for _, test := range []struct {
		name  string
		ctx   context.Context
		id    database.StoreID
		path  string
		hooks Hooks
	}{
		{name: "nil context", id: "global/auth", path: validPath, hooks: recorder.hooks()},
		{name: "unbounded context", ctx: context.Background(), id: "global/auth", path: validPath, hooks: recorder.hooks()},
		{name: "invalid ID", ctx: validContext, id: "bad id", path: validPath, hooks: recorder.hooks()},
		{name: "relative path", ctx: validContext, id: "global/auth", path: "auth.db", hooks: recorder.hooks()},
		{name: "unclean path", ctx: validContext, id: "global/auth", path: validPath + string(filepath.Separator) + ".." + string(filepath.Separator) + "auth.db", hooks: recorder.hooks()},
		{name: "empty path", ctx: validContext, id: "global/auth", hooks: recorder.hooks()},
		{name: "padded path", ctx: validContext, id: "global/auth", path: " " + validPath, hooks: recorder.hooks()},
		{name: "NUL path", ctx: validContext, id: "global/auth", path: validPath + "\x00", hooks: recorder.hooks()},
		{name: "invalid UTF-8 path", ctx: validContext, id: "global/auth", path: invalidUTF8, hooks: recorder.hooks()},
		{name: "oversized path", ctx: validContext, id: "global/auth", path: oversized, hooks: recorder.hooks()},
		{name: "missing hooks", ctx: validContext, id: "global/auth", path: validPath},
	} {
		t.Run(test.name, func(t *testing.T) {
			if lease, err := New(test.ctx, test.id, test.path, test.hooks); lease != nil || err == nil {
				t.Fatalf("invalid New = %#v, %v", lease, err)
			}
		})
	}
	for _, missing := range []string{"check", "reconcile", "pin", "replacement"} {
		t.Run("missing "+missing+" hook", func(t *testing.T) {
			hooks := recorder.hooks()
			switch missing {
			case "check":
				hooks.Check = nil
			case "reconcile":
				hooks.Reconcile = nil
			case "pin":
				hooks.PinReplacement = nil
			case "replacement":
				hooks.ReconcileReplacement = nil
			}
			if lease, err := New(validContext, "global/auth", validPath, hooks); lease != nil ||
				database.CodeOf(err) != database.CodeInvalid {
				t.Fatalf("New with missing %s hook = %#v, %v", missing, lease, err)
			}
		})
	}
	canceled, cancelNow := context.WithCancel(validContext)
	cancelNow()
	if lease, err := New(canceled, "global/auth", validPath, recorder.hooks()); lease != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("canceled New = %#v, %v", lease, err)
	}
	if err := Revoke(nil, nil); !errors.Is(err, errUnavailable) {
		t.Fatalf("nil Revoke = %v", err)
	}
	if err := Wait(nil, nil); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil Wait = %v", err)
	}
	if err := Consume(nil, nil, nil); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil Consume = %v", err)
	}
	if _, _, err := (Access{}).Target(); !errors.Is(err, errUnavailable) {
		t.Fatalf("empty Target = %v", err)
	}
	if err := (Access{}).Check(t.Context()); !errors.Is(err, errUnavailable) {
		t.Fatalf("empty Check = %v", err)
	}
	lease := newTestLease(t, recorder)
	if err := Consume(t.Context(), lease, func(ctx context.Context, access Access) error {
		if err := access.Check(nil); database.CodeOf(err) != database.CodeInvalid {
			t.Fatalf("nil operation context = %v", err)
		}
		if err := access.PinReplacement(ctx, "relative"); database.CodeOf(err) != database.CodeInvalid {
			t.Fatalf("relative replacement = %v", err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if err := access.Reconcile(canceled); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled operation = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseConcurrentCopiesAdmitOneConsumer(t *testing.T) {
	lease := newTestLease(t, new(hookRecorder))
	copies := []Lease{*lease, *lease, *lease, *lease}
	start := make(chan struct{})
	results := make(chan error, len(copies))
	var wait sync.WaitGroup
	for index := range copies {
		wait.Add(1)
		go func(candidate Lease) {
			defer wait.Done()
			<-start
			results <- Consume(t.Context(), &candidate, func(context.Context, Access) error {
				return nil
			})
		}(copies[index])
	}
	close(start)
	wait.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, errConsumed) && !errors.Is(err, errRevoked) {
			t.Fatalf("concurrent Consume = %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful consumers = %d, want 1", successes)
	}
}

func TestConsumeCallerCancellationRevokesRetainedAccess(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previousProcs) })
	recorder := new(hookRecorder)
	lease := newTestLease(t, recorder)
	caller, cancelCaller := context.WithCancelCause(context.Background())
	callerCause := errors.New("caller canceled migration")
	err := Consume(caller, lease, func(ctx context.Context, access Access) error {
		cancelCaller(callerCause)
		if !errors.Is(context.Cause(ctx), callerCause) {
			t.Fatalf("callback context cause = %v", context.Cause(ctx))
		}
		if _, _, err := access.Target(); !errors.Is(err, callerCause) {
			t.Fatalf("target admission immediately after caller cancel = %v", err)
		}
		if err := access.Check(context.Background()); !errors.Is(err, callerCause) {
			t.Fatalf("retained access after caller cancel = %v", err)
		}
		return nil
	})
	if !errors.Is(err, callerCause) {
		t.Fatalf("canceled Consume = %v", err)
	}
	if recorder.checks.Load() != 0 {
		t.Fatal("canceled retained access invoked its hook")
	}
}

func TestInvokeObservesCallerCauseWhenNilReturningHookCancelsIt(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previousProcs) })
	operationCause := errors.New("operation caller canceled")
	var cancelOperation context.CancelCauseFunc
	hooks := new(hookRecorder).hooks()
	hooks.Check = func(context.Context) error {
		cancelOperation(operationCause)
		return nil
	}
	parent, cancelParent := context.WithTimeout(context.Background(), time.Minute)
	defer cancelParent()
	lease, err := New(parent, "global/auth", filepath.Join(t.TempDir(), "auth.db"), hooks)
	if err != nil {
		t.Fatal(err)
	}
	err = Consume(context.Background(), lease, func(_ context.Context, access Access) error {
		operationContext, cancel := context.WithCancelCause(context.Background())
		cancelOperation = cancel
		defer cancel(nil)
		return access.Check(operationContext)
	})
	if !errors.Is(err, operationCause) {
		t.Fatalf("nil-return hook cancellation result = %v", err)
	}
}

func TestInvokeObservesLeaseCauseWhenNilReturningHookCancelsIt(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previousProcs) })
	leaseCause := errors.New("lease parent canceled")
	parent, cancelParent := context.WithCancelCause(context.Background())
	deadlineParent, cancelDeadline := context.WithTimeout(parent, time.Minute)
	defer cancelDeadline()
	hooks := new(hookRecorder).hooks()
	hooks.Check = func(context.Context) error {
		cancelParent(leaseCause)
		return nil
	}
	lease, err := New(
		deadlineParent, "global/auth", filepath.Join(t.TempDir(), "auth.db"), hooks,
	)
	if err != nil {
		t.Fatal(err)
	}
	err = Consume(context.Background(), lease, func(_ context.Context, access Access) error {
		return access.Check(context.Background())
	})
	if !errors.Is(err, leaseCause) {
		t.Fatalf("nil-return hook lease cancellation result = %v", err)
	}
}

func TestLinkedCauseContextObservesAlreadyCanceledSecondary(t *testing.T) {
	secondaryCause := errors.New("secondary canceled")
	secondary, cancelSecondary := context.WithCancelCause(context.Background())
	cancelSecondary(secondaryCause)
	ctx, cancel, stop := linkedCauseContext(context.Background(), secondary)
	defer cancel(errScopeEnded)
	defer stop()
	if !errors.Is(context.Cause(ctx), secondaryCause) {
		t.Fatalf("linked secondary cause = %v", context.Cause(ctx))
	}
}

func TestLeaseDefensiveCauseAndCancelBookkeeping(t *testing.T) {
	t.Run("nil revoke cause", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		state := &leaseState{ctx: ctx, cancel: cancel, done: make(chan struct{})}
		state.mu.Lock()
		state.revokeLocked(nil)
		state.closeDoneLocked()
		cause := state.revokeCause
		state.mu.Unlock()
		if !errors.Is(cause, errRevoked) {
			t.Fatalf("nil revoke cause = %v", cause)
		}
	})

	t.Run("context ended before cancel tracking", func(t *testing.T) {
		cause := errors.New("context ended before bookkeeping")
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(cause)
		state := &leaseState{ctx: ctx, cancel: func(error) {}, done: make(chan struct{})}
		var trackedCause error
		stop := state.trackCancel(func(err error) { trackedCause = err }, nil)
		stop()
		if !state.revoked || !errors.Is(state.revokeCause, cause) ||
			!errors.Is(trackedCause, cause) {
			t.Fatalf(
				"pre-ended tracking = revoked %t, state %v, tracked %v",
				state.revoked, state.revokeCause, trackedCause,
			)
		}
	})

	t.Run("lazy cancel ledger", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		state := &leaseState{ctx: ctx, cancel: cancel, done: make(chan struct{})}
		stop := state.trackCancel(func(error) {}, nil)
		if len(state.activeCancels) != 1 {
			t.Fatalf("tracked cancel count = %d", len(state.activeCancels))
		}
		stop()
		stop()
		if len(state.activeCancels) != 0 {
			t.Fatalf("cancel count after idempotent stop = %d", len(state.activeCancels))
		}
	})

	t.Run("cause fallbacks", func(t *testing.T) {
		if cause := (&leaseState{revokeCause: errScopeEnded}).causeLocked(); !errors.Is(cause, errRevoked) {
			t.Fatalf("scope-ended cause = %v", cause)
		}
		custom := errors.New("context cause")
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(custom)
		if cause := (&leaseState{ctx: ctx}).causeLocked(); !errors.Is(cause, custom) {
			t.Fatalf("context fallback cause = %v", cause)
		}
		state := &leaseState{ctx: context.Background()}
		if cause := state.causeLocked(); !errors.Is(cause, errUnavailable) {
			t.Fatalf("unavailable fallback cause = %v", cause)
		}
		access := Access{
			state: state, scope: 1, caller: context.Background(), scopeContext: context.Background(),
		}
		if _, _, err := access.Target(); !errors.Is(err, errUnavailable) {
			t.Fatalf("inactive access cause = %v", err)
		}
		if cause := externalCause(custom); !errors.Is(cause, custom) {
			t.Fatalf("external custom cause = %v", cause)
		}
	})
}

func TestConsumeAlwaysCleansUpAfterPanicAndGoexit(t *testing.T) {
	for _, exit := range []string{"panic", "goexit"} {
		t.Run(exit, func(t *testing.T) {
			lease := newTestLease(t, new(hookRecorder))
			var retained Access
			finished := make(chan struct{})
			panicked := make(chan any, 1)
			go func() {
				defer close(finished)
				defer func() { panicked <- recover() }()
				_ = Consume(context.Background(), lease, func(_ context.Context, access Access) error {
					retained = access
					if exit == "panic" {
						panic("callback panic")
					}
					runtime.Goexit()
					return nil
				})
			}()
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("callback cleanup did not finish")
			}
			if err := Wait(t.Context(), lease); err != nil {
				t.Fatal(err)
			}
			if _, _, err := retained.Target(); !errors.Is(err, errRevoked) {
				t.Fatalf("retained access after %s = %v", exit, err)
			}
			if recovered := <-panicked; exit == "panic" && recovered == nil {
				t.Fatal("callback panic did not propagate")
			} else if exit == "goexit" && recovered != nil {
				t.Fatalf("Goexit recovered unexpected value %#v", recovered)
			}
		})
	}
}

func TestConsumeAndInvokePreserveExternalCancellationCauses(t *testing.T) {
	t.Run("explicit revoke", func(t *testing.T) {
		lease := newTestLease(t, new(hookRecorder))
		entered := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			result <- Consume(context.Background(), lease, func(ctx context.Context, _ Access) error {
				close(entered)
				<-ctx.Done()
				return nil
			})
		}()
		<-entered
		if err := Revoke(lease, nil); err != nil {
			t.Fatal(err)
		}
		if err := <-result; !errors.Is(err, errRevoked) {
			t.Fatalf("explicitly revoked Consume = %v", err)
		}
	})

	t.Run("operation deadline", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		hooks := new(hookRecorder).hooks()
		hooks.Check = func(context.Context) error {
			close(entered)
			<-release
			return nil
		}
		parent, cancelParent := context.WithTimeout(context.Background(), time.Minute)
		defer cancelParent()
		lease, err := New(
			parent, "global/auth", filepath.Join(t.TempDir(), "auth.db"), hooks,
		)
		if err != nil {
			t.Fatal(err)
		}
		err = Consume(context.Background(), lease, func(_ context.Context, access Access) error {
			operationContext, cancelOperation := context.WithTimeout(
				context.Background(), 10*time.Millisecond,
			)
			defer cancelOperation()
			result := make(chan error, 1)
			go func() { result <- access.Check(operationContext) }()
			<-entered
			<-operationContext.Done()
			close(release)
			return <-result
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("operation deadline result = %v", err)
		}
	})

	t.Run("operation custom deadline cause", func(t *testing.T) {
		customDeadlineCause := errors.New("operation custom deadline")
		hooks := new(hookRecorder).hooks()
		hooks.Check = func(ctx context.Context) error {
			<-ctx.Done()
			return context.Cause(ctx)
		}
		parent, cancelParent := context.WithTimeout(context.Background(), time.Minute)
		defer cancelParent()
		lease, err := New(
			parent, "global/auth", filepath.Join(t.TempDir(), "auth.db"), hooks,
		)
		if err != nil {
			t.Fatal(err)
		}
		err = Consume(context.Background(), lease, func(_ context.Context, access Access) error {
			deadlineContext, cancelDeadline := context.WithDeadlineCause(
				context.Background(), time.Now().Add(10*time.Millisecond), customDeadlineCause,
			)
			defer cancelDeadline()
			return access.Check(deadlineContext)
		})
		if !errors.Is(err, customDeadlineCause) {
			t.Fatalf("operation custom deadline result = %v", err)
		}
	})
}
