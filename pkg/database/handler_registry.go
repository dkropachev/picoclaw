package database

import (
	"context"
	"errors"
	"reflect"
	"sync"
)

type closeableHandler interface {
	Close() error
}

type handlerCloseIdentity struct {
	typeOf  reflect.Type
	pointer uintptr
}

// HandlerRegistry dispatches requests to explicitly registered domain
// handlers. Its zero value is ready for use.
type HandlerRegistry struct {
	mu            sync.Mutex
	handlers      map[string]Handler
	closers       []func() error
	closerOwners  map[handlerCloseIdentity]struct{}
	active        sync.WaitGroup
	closed        bool
	closing       bool
	closeComplete bool
	closeDone     chan struct{}
	closeErr      error
}

// NewHandlerRegistry returns an empty domain handler registry.
func NewHandlerRegistry() *HandlerRegistry {
	return &HandlerRegistry{}
}

// Register binds domain to handler. A closeable handler must have stable,
// unique pointer identity; one pointer registered for several domains is owned
// and closed once.
func (registry *HandlerRegistry) Register(domain string, handler Handler) error {
	if registry == nil || domain == ControlDomain ||
		!validProtocolName(domain, maxDomainBytes) || isNilHandler(handler) {
		return NewError(CodeInvalid, "database domain handler registration is invalid")
	}

	closeHandler, closeIdentity, ownsClose, err := handlerCloser(handler)
	if err != nil {
		return err
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closed {
		return NewError(CodeConflict, "database domain handler registry is closed")
	}
	if _, exists := registry.handlers[domain]; exists {
		return NewError(CodeAlreadyExists, "database domain handler is already registered")
	}
	if registry.handlers == nil {
		registry.handlers = make(map[string]Handler)
	}

	registry.handlers[domain] = handler
	if ownsClose {
		if registry.closerOwners == nil {
			registry.closerOwners = make(map[handlerCloseIdentity]struct{})
		}
		if _, found := registry.closerOwners[closeIdentity]; !found {
			registry.closerOwners[closeIdentity] = struct{}{}
			registry.closers = append(registry.closers, closeHandler)
		}
	}
	return nil
}

// Handle dispatches request to its registered domain handler. Admission and
// shutdown are linearized under the same mutex, so every admitted callback is
// counted before Close can begin waiting.
func (registry *HandlerRegistry) Handle(ctx context.Context, request Request) (any, error) {
	if registry == nil {
		return nil, NewError(CodeUnsupported, "database domain is unsupported")
	}

	registry.mu.Lock()
	if registry.closed {
		registry.mu.Unlock()
		return nil, NewError(CodeUnavailable, "database domain handler registry is closed")
	}
	handler, ok := registry.handlers[request.Domain]
	if ok {
		registry.active.Add(1)
	}
	registry.mu.Unlock()
	if !ok {
		return nil, NewError(CodeUnsupported, "database domain is unsupported")
	}

	defer registry.active.Done()
	return callRegisteredHandler(ctx, request, handler)
}

// Close stops admission, waits for admitted dispatch, and closes uniquely owned
// handlers in reverse registration order. Concurrent and repeated callers
// receive the same result. A handler must not synchronously call its owning
// registry's Close from inside Handle, and an owned closer must not call it
// during Close, because owner shutdown waits for those callbacks.
func (registry *HandlerRegistry) Close() error {
	if registry == nil {
		return nil
	}
	registry.mu.Lock()
	registry.closed = true
	if registry.closeComplete {
		result := registry.closeErr
		registry.mu.Unlock()
		return result
	}
	if registry.closing {
		done := registry.closeDone
		registry.mu.Unlock()
		<-done
		registry.mu.Lock()
		result := registry.closeErr
		registry.mu.Unlock()
		return result
	}
	registry.closing = true
	registry.closeDone = make(chan struct{})
	done := registry.closeDone
	closers := append([]func() error(nil), registry.closers...)
	registry.handlers = nil
	registry.closers = nil
	registry.closerOwners = nil
	registry.mu.Unlock()

	registry.active.Wait()
	var closeErr error
	for index := len(closers) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, callCloseHandler(closers[index]))
	}
	registry.mu.Lock()
	registry.closeErr = closeErr
	registry.closeComplete = true
	registry.closing = false
	close(done)
	registry.mu.Unlock()
	return closeErr
}

func callRegisteredHandler(
	ctx context.Context,
	request Request,
	handler Handler,
) (result any, returnErr error) {
	defer func() {
		if recover() != nil {
			result = nil
			returnErr = NewError(CodeInternal, "database broker domain handler failed")
		}
	}()
	return handler.Handle(ctx, request)
}

func handlerCloser(handler Handler) (func() error, handlerCloseIdentity, bool, error) {
	closer, ok := handler.(closeableHandler)
	if !ok {
		return nil, handlerCloseIdentity{}, false, nil
	}
	value := reflect.ValueOf(handler)
	if value.Kind() != reflect.Pointer || value.IsNil() || value.Type().Elem().Size() == 0 {
		return nil, handlerCloseIdentity{}, false, NewError(
			CodeInvalid,
			"closeable database domain handler must have stable unique pointer identity",
		)
	}
	return closer.Close, handlerCloseIdentity{
		typeOf: value.Type(), pointer: value.Pointer(),
	}, true, nil
}

func isNilHandler(handler Handler) bool {
	if handler == nil {
		return true
	}
	value := reflect.ValueOf(handler)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
