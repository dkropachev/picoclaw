package database

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

const registryClosePanicSecret = "registry-close-panic-secret-7f31"

type registryValueHandler struct {
	result any
	err    error
}

type registryValueCloser struct{}

func (registryValueCloser) Handle(context.Context, Request) (any, error) { return EmptyPayload{}, nil }
func (registryValueCloser) Close() error                                 { return nil }

func (handler registryValueHandler) Handle(context.Context, Request) (any, error) {
	return handler.result, handler.err
}

type registryMapHandler map[string]string

func (registryMapHandler) Handle(context.Context, Request) (any, error) {
	return EmptyPayload{}, nil
}

type registrySliceHandler []string

func (registrySliceHandler) Handle(context.Context, Request) (any, error) {
	return EmptyPayload{}, nil
}

type registryChanHandler chan struct{}

func (registryChanHandler) Handle(context.Context, Request) (any, error) {
	return EmptyPayload{}, nil
}

type registryZeroSizedCloser struct{}

func (*registryZeroSizedCloser) Handle(context.Context, Request) (any, error) {
	return EmptyPayload{}, nil
}

func (*registryZeroSizedCloser) Close() error { return nil }

type registryCloseHandler struct {
	name   string
	err    error
	mu     *sync.Mutex
	order  *[]string
	calls  atomic.Int32
	panic  bool
	active *atomic.Int32
}

func (*registryCloseHandler) Handle(context.Context, Request) (any, error) {
	return EmptyPayload{}, nil
}

func (handler *registryCloseHandler) Close() error {
	handler.calls.Add(1)
	if handler.active != nil && handler.active.Load() != 0 {
		return errors.New("handler closed during active dispatch")
	}
	handler.mu.Lock()
	*handler.order = append(*handler.order, handler.name)
	handler.mu.Unlock()
	if handler.panic {
		panic(registryClosePanicSecret)
	}
	return handler.err
}

type registryBlockingCloser struct {
	entered chan struct{}
	release chan struct{}
	err     error
	calls   atomic.Int32
}

func (*registryBlockingCloser) Handle(context.Context, Request) (any, error) {
	return EmptyPayload{}, nil
}

func (handler *registryBlockingCloser) Close() error {
	handler.calls.Add(1)
	close(handler.entered)
	<-handler.release
	return handler.err
}

func TestHandlerRegistryRegisterAndDispatch(t *testing.T) {
	registry := NewHandlerRegistry()
	type contextKey struct{}
	ctx := context.WithValue(t.Context(), contextKey{}, "context-value")
	request := Request{Domain: "alpha", Version: 1, Operation: "read"}
	wantResult := &struct{ Value string }{Value: "result"}
	var gotRequest Request
	if err := registry.Register("alpha", HandlerFunc(func(
		handlerCtx context.Context,
		handlerRequest Request,
	) (any, error) {
		if got := handlerCtx.Value(contextKey{}); got != "context-value" {
			t.Errorf("handler context value = %v, want context-value", got)
		}
		gotRequest = handlerRequest
		return wantResult, nil
	})); err != nil {
		t.Fatalf("Register(alpha) error = %v", err)
	}

	gotResult, err := registry.Handle(ctx, request)
	if err != nil {
		t.Fatalf("Handle(alpha) error = %v", err)
	}
	if gotResult != wantResult {
		t.Fatalf("Handle(alpha) result = %#v, want %#v", gotResult, wantResult)
	}
	if !reflect.DeepEqual(gotRequest, request) {
		t.Fatalf("Handle(alpha) request = %#v, want %#v", gotRequest, request)
	}

	duplicate := registryValueHandler{result: "duplicate"}
	if registerErr := registry.Register("alpha", duplicate); CodeOf(registerErr) != CodeAlreadyExists {
		t.Fatalf("Register(duplicate) error = %v, want AlreadyExists", registerErr)
	}
	gotResult, err = registry.Handle(ctx, request)
	if err != nil || gotResult != wantResult {
		t.Fatalf("Handle(alpha) after duplicate = %#v, %v", gotResult, err)
	}

	canary := errors.New("handler canary")
	if err := registry.Register("failure", registryValueHandler{err: canary}); err != nil {
		t.Fatalf("Register(failure) error = %v", err)
	}
	if _, err := registry.Handle(ctx, Request{Domain: "failure"}); !errors.Is(err, canary) {
		t.Fatalf("Handle(failure) error = %v, want canary", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		_, err := registry.Handle(ctx, Request{Domain: "missing"})
		if CodeOf(err) != CodeUnsupported ||
			err.Error() != "Unsupported: database domain is unsupported" {
			t.Fatalf("Handle(missing) error = %v, want deterministic Unsupported", err)
		}
	}

	var nilRegistry *HandlerRegistry
	if _, err := nilRegistry.Handle(ctx, request); CodeOf(err) != CodeUnsupported {
		t.Fatalf("nil registry Handle() error = %v, want Unsupported", err)
	}
	if err := registry.Register("value-closer", registryValueCloser{}); CodeOf(err) != CodeInvalid {
		t.Errorf("Register(value closer) error = %v, want Invalid", err)
	}
}

func TestHandlerRegistryRejectsInvalidRegistrations(t *testing.T) {
	registry := &HandlerRegistry{}
	validHandler := registryValueHandler{}
	invalidDomains := []string{"", " ", "Uppercase", ".prefix", ControlDomain}
	for _, domain := range invalidDomains {
		if err := registry.Register(domain, validHandler); CodeOf(err) != CodeInvalid {
			t.Errorf("Register(%q) error = %v, want Invalid", domain, err)
		}
	}
	if err := registry.Register("nil-interface", nil); CodeOf(err) != CodeInvalid {
		t.Errorf("Register(nil interface) error = %v, want Invalid", err)
	}
	var nilFunc HandlerFunc
	if err := registry.Register("nil-function", nilFunc); CodeOf(err) != CodeInvalid {
		t.Errorf("Register(nil HandlerFunc) error = %v, want Invalid", err)
	}
	var nilPointer *registryCloseHandler
	if err := registry.Register("nil-pointer", nilPointer); CodeOf(err) != CodeInvalid {
		t.Errorf("Register(nil pointer) error = %v, want Invalid", err)
	}
	for _, test := range []struct {
		domain  string
		handler Handler
	}{
		{domain: "nil-map", handler: registryMapHandler(nil)},
		{domain: "nil-slice", handler: registrySliceHandler(nil)},
		{domain: "nil-channel", handler: registryChanHandler(nil)},
	} {
		if err := registry.Register(test.domain, test.handler); CodeOf(err) != CodeInvalid {
			t.Errorf("Register(%s) error = %v, want Invalid", test.domain, err)
		}
	}
	if err := registry.Register("zero-sized-closer", &registryZeroSizedCloser{}); CodeOf(err) != CodeInvalid {
		t.Errorf("Register(zero-sized closer) error = %v, want Invalid", err)
	}
	var nilRegistry *HandlerRegistry
	if err := nilRegistry.Register("domain", validHandler); CodeOf(err) != CodeInvalid {
		t.Errorf("nil registry Register() error = %v, want Invalid", err)
	}
}

func TestHandlerRegistryClosesInReverseOrderOnce(t *testing.T) {
	registry := &HandlerRegistry{}
	var orderMu sync.Mutex
	order := make([]string, 0, 3)
	firstErr := errors.New("first close canary")
	lastErr := errors.New("last close canary")
	first := &registryCloseHandler{name: "first", err: firstErr, mu: &orderMu, order: &order}
	panicking := &registryCloseHandler{name: "panicking", panic: true, mu: &orderMu, order: &order}
	last := &registryCloseHandler{name: "last", err: lastErr, mu: &orderMu, order: &order}
	registrations := []struct {
		domain  string
		handler Handler
	}{
		{domain: "first", handler: first},
		{domain: "no-close", handler: HandlerFunc(func(context.Context, Request) (any, error) {
			return EmptyPayload{}, nil
		})},
		{domain: "panicking", handler: panicking},
		{domain: "last", handler: last},
	}
	for _, registration := range registrations {
		if err := registry.Register(registration.domain, registration.handler); err != nil {
			t.Fatalf("Register(%s) error = %v", registration.domain, err)
		}
	}

	closeErr := registry.Close()
	if !errors.Is(closeErr, firstErr) || !errors.Is(closeErr, lastErr) ||
		CodeOf(closeErr) != CodeInternal {
		t.Errorf("Close() error = %v, want both canaries and panic Internal", closeErr)
	}
	if strings.Contains(closeErr.Error(), registryClosePanicSecret) {
		t.Fatalf("Close() exposed panic secret: %v", closeErr)
	}
	if repeated := registry.Close(); repeated != closeErr {
		t.Fatalf("repeated Close() error = %v, want same result %v", repeated, closeErr)
	}
	if want := []string{"last", "panicking", "first"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("close order = %v, want %v", order, want)
	}
	for name, handler := range map[string]*registryCloseHandler{
		"first": first, "panicking": panicking, "last": last,
	} {
		if calls := handler.calls.Load(); calls != 1 {
			t.Errorf("%s Close() calls = %d, want 1", name, calls)
		}
	}
	if err := registry.Register("after-close", registryValueHandler{}); CodeOf(err) != CodeConflict {
		t.Errorf("Register() after Close() error = %v, want Conflict", err)
	}
	if _, err := registry.Handle(t.Context(), Request{Domain: "first"}); CodeOf(err) != CodeUnavailable {
		t.Errorf("Handle() after Close() error = %v, want Unavailable", err)
	}

	var nilRegistry *HandlerRegistry
	if err := nilRegistry.Close(); err != nil {
		t.Fatalf("nil registry Close() error = %v", err)
	}
	empty := &HandlerRegistry{}
	if err := empty.Close(); err != nil {
		t.Fatalf("empty registry Close() error = %v", err)
	}
	if err := empty.Close(); err != nil {
		t.Fatalf("second empty registry Close() error = %v", err)
	}
}

func TestHandlerRegistryCloseWaitsForActiveDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := &HandlerRegistry{}
		started := make(chan struct{})
		release := make(chan struct{})
		requestDone := make(chan error, 1)
		closeDone := make(chan error, 1)
		var active atomic.Int32
		var orderMu sync.Mutex
		order := make([]string, 0, 1)
		handler := &registryCloseHandler{
			name: "blocking", mu: &orderMu, order: &order, active: &active,
		}
		if err := registry.Register("blocking", HandlerFunc(func(context.Context, Request) (any, error) {
			active.Store(1)
			close(started)
			<-release
			active.Store(0)
			return EmptyPayload{}, nil
		})); err != nil {
			t.Fatalf("Register(blocking) error = %v", err)
		}
		// Register the closer after the blocking handler so it is the first cleanup.
		if err := registry.Register("closer", handler); err != nil {
			t.Fatalf("Register(closer) error = %v", err)
		}

		go func() {
			_, err := registry.Handle(context.Background(), Request{Domain: "blocking"})
			requestDone <- err
		}()
		<-started
		go func() { closeDone <- registry.Close() }()
		synctest.Wait()
		select {
		case err := <-closeDone:
			t.Fatalf("Close() completed during active dispatch: %v", err)
		default:
		}

		close(release)
		synctest.Wait()
		if err := <-requestDone; err != nil {
			t.Fatalf("Handle(blocking) error = %v", err)
		}
		if err := <-closeDone; err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if calls := handler.calls.Load(); calls != 1 {
			t.Fatalf("closer calls = %d, want 1", calls)
		}
	})
}

func TestHandlerRegistryConcurrentCloseWaitsAndStopsAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := NewHandlerRegistry()
		canary := errors.New("blocking close canary")
		closer := &registryBlockingCloser{
			entered: make(chan struct{}), release: make(chan struct{}), err: canary,
		}
		if err := registry.Register("blocking-close", closer); err != nil {
			t.Fatalf("Register(blocking-close) error = %v", err)
		}

		firstDone := make(chan error, 1)
		secondDone := make(chan error, 1)
		go func() { firstDone <- registry.Close() }()
		<-closer.entered
		if _, err := registry.Handle(
			context.Background(), Request{Domain: "blocking-close"},
		); CodeOf(err) != CodeUnavailable {
			t.Fatalf("Handle() during Close() error = %v, want Unavailable", err)
		}
		if err := registry.Register("late", registryValueHandler{}); CodeOf(err) != CodeConflict {
			t.Fatalf("Register() during Close() error = %v, want Conflict", err)
		}

		go func() { secondDone <- registry.Close() }()
		synctest.Wait()
		select {
		case err := <-secondDone:
			t.Fatalf("concurrent Close() completed before owner close: %v", err)
		default:
		}

		close(closer.release)
		synctest.Wait()
		firstErr := <-firstDone
		secondErr := <-secondDone
		if !errors.Is(firstErr, canary) || secondErr != firstErr {
			t.Fatalf("concurrent Close() errors = %v, %v, want same canary result", firstErr, secondErr)
		}
		if repeated := registry.Close(); repeated != firstErr {
			t.Fatalf("repeated Close() error = %v, want same result %v", repeated, firstErr)
		}
		if calls := closer.calls.Load(); calls != 1 {
			t.Fatalf("closer calls = %d, want 1", calls)
		}
	})
}

func TestHandlerRegistryContainsHandlerPanicAndDrainsBeforeClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const panicSecret = "registry-handler-panic-secret-3c82"
		registry := NewHandlerRegistry()
		started := make(chan struct{})
		release := make(chan struct{})
		if err := registry.Register("panicking", HandlerFunc(func(
			context.Context,
			Request,
		) (any, error) {
			close(started)
			<-release
			panic(panicSecret)
		})); err != nil {
			t.Fatalf("Register(panicking) error = %v", err)
		}

		type handleResult struct {
			value any
			err   error
		}
		handleDone := make(chan handleResult, 1)
		closeDone := make(chan error, 1)
		go func() {
			value, err := registry.Handle(context.Background(), Request{Domain: "panicking"})
			handleDone <- handleResult{value: value, err: err}
		}()
		<-started
		go func() { closeDone <- registry.Close() }()
		synctest.Wait()
		select {
		case err := <-closeDone:
			t.Fatalf("Close() completed before panicking handler drained: %v", err)
		default:
		}

		close(release)
		synctest.Wait()
		result := <-handleDone
		if result.value != nil || CodeOf(result.err) != CodeInternal {
			t.Fatalf("panicking Handle() = %#v, %v, want nil, Internal", result.value, result.err)
		}
		if strings.Contains(result.err.Error(), panicSecret) {
			t.Fatalf("panicking Handle() exposed panic secret: %v", result.err)
		}
		if err := <-closeDone; err != nil {
			t.Fatalf("Close() after handler panic error = %v", err)
		}
	})
}

func TestHandlerRegistryDeduplicatesSharedCloser(t *testing.T) {
	registry := NewHandlerRegistry()
	var orderMu sync.Mutex
	order := make([]string, 0, 1)
	shared := &registryCloseHandler{name: "shared", mu: &orderMu, order: &order}
	if err := registry.Register("first-domain", shared); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("second-domain", shared); err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	if shared.calls.Load() != 1 {
		t.Fatalf("shared closer calls = %d, want 1", shared.calls.Load())
	}
	if registry.handlers != nil || registry.closers != nil || registry.closerOwners != nil {
		t.Fatal("closed registry retained handlers or closer identities")
	}
}
