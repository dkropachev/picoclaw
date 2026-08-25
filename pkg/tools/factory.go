package tools

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
)

type ToolBuildFunc func(ToolBuildContext) (Tool, error)

// ToolFactory metadata and New must be safe for concurrent calls. New must
// return a fresh non-nil pointer for every per-owner construction; registry
// instantiation can construct the same factory for multiple owners in parallel.
type ToolFactory interface {
	Descriptor() ToolDescriptor
	Traits() ToolTraits
	New(ctx ToolBuildContext) (Tool, error)
}

type toolFactory struct {
	descriptor ToolDescriptor
	traits     ToolTraits
	build      ToolBuildFunc
}

func NewToolFactory(
	descriptor ToolDescriptor,
	traits ToolTraits,
	build ToolBuildFunc,
) (ToolFactory, error) {
	frozen, err := freezeToolDescriptor(descriptor)
	if err != nil {
		return nil, err
	}
	normalized, err := traits.normalized()
	if err != nil {
		return nil, err
	}
	if normalized.Sharing != ToolSharingPerOwner {
		return nil, fmt.Errorf("per-owner factory cannot declare %q sharing", normalized.Sharing)
	}
	if build == nil {
		return nil, fmt.Errorf("tool factory build function is required")
	}
	return &toolFactory{descriptor: frozen, traits: normalized, build: build}, nil
}

// NewToolFactoryFromPrototype snapshots prototype's complete provider-facing
// descriptor, including prompt metadata, and creates a per-owner factory from
// it. The snapshot is panic-safe and does not retain aliases to the
// prototype's parameter schema.
func NewToolFactoryFromPrototype(
	prototype Tool,
	traits ToolTraits,
	build ToolBuildFunc,
) (ToolFactory, error) {
	descriptor, err := safeToolDescriptor(prototype)
	if err != nil {
		return nil, fmt.Errorf("snapshot tool factory prototype: %w", err)
	}
	return NewToolFactory(descriptor, traits, build)
}

func (factory *toolFactory) Descriptor() ToolDescriptor {
	if factory == nil {
		return ToolDescriptor{}
	}
	return cloneToolDescriptor(factory.descriptor)
}

func (factory *toolFactory) Traits() ToolTraits {
	if factory == nil {
		return ToolTraits{}
	}
	return factory.traits
}

func (factory *toolFactory) New(ctx ToolBuildContext) (Tool, error) {
	if factory == nil || factory.build == nil {
		return nil, fmt.Errorf("tool factory is not configured")
	}
	return factory.build(ctx)
}

type ToolBuildContext struct {
	owner    ToolOwner
	resolve  func(string) (Tool, error)
	services *toolServiceTransaction
	registry *ToolRegistry
	lifetime *toolBuildLifetime
}

func (ctx ToolBuildContext) Owner() ToolOwner { return ctx.owner }

func (ctx ToolBuildContext) Resolve(name string) (Tool, error) {
	if ctx.lifetime == nil || !ctx.lifetime.enter() {
		return nil, fmt.Errorf("tool build context is no longer active")
	}
	defer ctx.lifetime.leave()
	ctx.lifetime.resolveMu.Lock()
	defer ctx.lifetime.resolveMu.Unlock()
	if ctx.resolve == nil {
		return nil, fmt.Errorf("tool dependency resolver is unavailable")
	}
	return ctx.resolve(name)
}

func (ctx ToolBuildContext) Service(
	key string,
	create func() (any, error),
) (any, error) {
	if ctx.lifetime == nil || !ctx.lifetime.enter() {
		return nil, fmt.Errorf("tool build context is no longer active")
	}
	defer ctx.lifetime.leave()
	if ctx.services == nil {
		return nil, fmt.Errorf("tool service cache is unavailable")
	}
	return ctx.services.service(key, create)
}

// destinationRegistryForBuild is intentionally package-private. Discovery
// factories may bind to the private destination registry without granting
// arbitrary registry mutation through the public factory API.
func destinationRegistryForBuild(ctx ToolBuildContext) *ToolRegistry {
	if ctx.lifetime == nil || !ctx.lifetime.enter() {
		return nil
	}
	defer ctx.lifetime.leave()
	return ctx.registry
}

func activateToolBuildContext(ctx ToolBuildContext) (ToolBuildContext, func()) {
	lifetime := &toolBuildLifetime{open: true}
	ctx.lifetime = lifetime
	return ctx, lifetime.closeAndWait
}

type toolBuildLifetime struct {
	mu        sync.Mutex
	resolveMu sync.Mutex
	open      bool
	wg        sync.WaitGroup
}

func (lifetime *toolBuildLifetime) enter() bool {
	lifetime.mu.Lock()
	defer lifetime.mu.Unlock()
	if !lifetime.open {
		return false
	}
	lifetime.wg.Add(1)
	return true
}

func (lifetime *toolBuildLifetime) leave() { lifetime.wg.Done() }

func (lifetime *toolBuildLifetime) closeAndWait() {
	lifetime.mu.Lock()
	lifetime.open = false
	lifetime.mu.Unlock()
	lifetime.wg.Wait()
}

type toolServiceCache struct {
	mu     sync.RWMutex
	values map[string]any
}

type toolInstanceTracker struct {
	mu     sync.Mutex
	issued map[toolInstanceKey]toolInstanceLease
}

type toolInstanceLease struct {
	value           any
	immutableShares uint64
}

var globalOwnedToolInstances = newToolInstanceTracker()

type toolInstanceKey struct {
	typeOf  reflect.Type
	pointer uintptr
}

func newToolInstanceTracker() *toolInstanceTracker {
	return &toolInstanceTracker{issued: make(map[toolInstanceKey]toolInstanceLease)}
}

func toolInstanceIdentity(tool Tool) (toolInstanceKey, error) {
	if isTypedNil(tool) {
		return toolInstanceKey{}, fmt.Errorf("per-owner factory returned nil tool")
	}
	value := reflect.ValueOf(tool)
	if value.Kind() != reflect.Pointer {
		return toolInstanceKey{}, fmt.Errorf("per-owner factory product must be a non-nil pointer")
	}
	return toolInstanceKey{typeOf: value.Type(), pointer: value.Pointer()}, nil
}

func (tracker *toolInstanceTracker) reserve(identity toolInstanceKey, value any) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if _, exists := tracker.issued[identity]; exists {
		return fmt.Errorf("factory reused an instance issued to another owner")
	}
	tracker.issued[identity] = toolInstanceLease{value: value}
	return nil
}

func (tracker *toolInstanceTracker) reserveImmutableShared(
	identity toolInstanceKey,
	value any,
) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	lease, exists := tracker.issued[identity]
	if exists && lease.immutableShares == 0 {
		return fmt.Errorf("immutable-shared instance is exclusively leased to another owner")
	}
	if exists {
		if !sameInterfaceIdentity(lease.value, value) {
			return fmt.Errorf("immutable-shared instance identity changed while leased")
		}
		if lease.immutableShares == ^uint64(0) {
			return fmt.Errorf("immutable-shared instance lease count is exhausted")
		}
		lease.immutableShares++
		tracker.issued[identity] = lease
		return nil
	}
	tracker.issued[identity] = toolInstanceLease{
		value: value, immutableShares: 1,
	}
	return nil
}

func (tracker *toolInstanceTracker) release(identity toolInstanceKey) {
	if tracker == nil || identity.pointer == 0 {
		return
	}
	tracker.mu.Lock()
	if lease, exists := tracker.issued[identity]; exists && lease.immutableShares == 0 {
		delete(tracker.issued, identity)
	}
	tracker.mu.Unlock()
}

func (tracker *toolInstanceTracker) releaseImmutableShared(identity toolInstanceKey) {
	if tracker == nil || identity.pointer == 0 {
		return
	}
	tracker.mu.Lock()
	lease, exists := tracker.issued[identity]
	if !exists || lease.immutableShares == 0 {
		tracker.mu.Unlock()
		return
	}
	lease.immutableShares--
	if lease.immutableShares == 0 {
		delete(tracker.issued, identity)
	} else {
		tracker.issued[identity] = lease
	}
	tracker.mu.Unlock()
}

type toolInstanceReservation struct {
	tracker         *toolInstanceTracker
	identity        toolInstanceKey
	immutableShared bool
}

func (reservation toolInstanceReservation) release() {
	if reservation.immutableShared {
		reservation.tracker.releaseImmutableShared(reservation.identity)
		return
	}
	reservation.tracker.release(reservation.identity)
}

func newToolServiceCache() *toolServiceCache {
	return &toolServiceCache{values: make(map[string]any)}
}

func (cache *toolServiceCache) snapshot() map[string]any {
	if cache == nil {
		return nil
	}
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	result := make(map[string]any, len(cache.values))
	for key, value := range cache.values {
		result[key] = value
	}
	return result
}

type toolServiceTransaction struct {
	mu           sync.Mutex
	base         map[string]any
	overlay      map[string]any
	creating     map[string]bool
	order        []any
	reservations []toolInstanceReservation
}

func newToolServiceTransaction(base map[string]any) *toolServiceTransaction {
	return &toolServiceTransaction{
		base:     base,
		overlay:  make(map[string]any),
		creating: make(map[string]bool),
	}
}

func (transaction *toolServiceTransaction) service(
	key string,
	create func() (any, error),
) (value any, err error) {
	if transaction == nil {
		return nil, fmt.Errorf("tool service transaction is unavailable")
	}
	if key == "" {
		return nil, fmt.Errorf("tool service key is required")
	}
	transaction.mu.Lock()
	if cached, ok := transaction.overlay[key]; ok {
		transaction.mu.Unlock()
		return cached, nil
	}
	if cached, ok := transaction.base[key]; ok {
		transaction.mu.Unlock()
		return cached, nil
	}
	if transaction.creating[key] {
		transaction.mu.Unlock()
		return nil, fmt.Errorf("tool service dependency cycle at %q", key)
	}
	if create == nil {
		transaction.mu.Unlock()
		return nil, fmt.Errorf("tool service %q is missing", key)
	}
	transaction.creating[key] = true
	transaction.mu.Unlock()

	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("create tool service %q: panic: %v", key, recovered)
			value = nil
		}
		transaction.mu.Lock()
		delete(transaction.creating, key)
		transaction.mu.Unlock()
	}()
	value, err = create()
	if err != nil {
		if !isTypedNil(value) {
			identity, tracked, identityErr := serviceInstanceIdentity(value)
			if identityErr != nil {
				return nil, errors.Join(err, identityErr)
			}
			if tracked {
				if reserveErr := globalOwnedToolInstances.reserve(identity, value); reserveErr != nil {
					return nil, errors.Join(err, reserveErr)
				}
				transaction.mu.Lock()
				transaction.reservations = append(transaction.reservations, toolInstanceReservation{
					tracker: globalOwnedToolInstances, identity: identity,
				})
				transaction.mu.Unlock()
			}
			transaction.mu.Lock()
			transaction.order = append(transaction.order, value)
			transaction.mu.Unlock()
		}
		return nil, err
	}
	if isTypedNil(value) {
		return nil, fmt.Errorf("tool service %q returned nil", key)
	}
	identity, tracked, identityErr := serviceInstanceIdentity(value)
	if identityErr != nil {
		return nil, fmt.Errorf("tool service %q: %w", key, identityErr)
	}
	if tracked {
		if reserveErr := globalOwnedToolInstances.reserve(identity, value); reserveErr != nil {
			return nil, fmt.Errorf("tool service %q: %w", key, reserveErr)
		}
		transaction.mu.Lock()
		transaction.reservations = append(transaction.reservations, toolInstanceReservation{
			tracker: globalOwnedToolInstances, identity: identity,
		})
		transaction.mu.Unlock()
	}

	transaction.mu.Lock()
	transaction.overlay[key] = value
	transaction.order = append(transaction.order, value)
	transaction.mu.Unlock()
	return value, nil
}

func (transaction *toolServiceTransaction) commit(cache *toolServiceCache) error {
	if transaction == nil || cache == nil {
		return nil
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for key, value := range transaction.overlay {
		if existing, ok := cache.values[key]; ok && !sameInterfaceIdentity(existing, value) {
			return fmt.Errorf("tool service %q changed during construction", key)
		}
	}
	for key, value := range transaction.overlay {
		cache.values[key] = value
	}
	transaction.overlay = make(map[string]any)
	return nil
}

func (transaction *toolServiceTransaction) detachCreated() []any {
	if transaction == nil {
		return nil
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	created := append([]any(nil), transaction.order...)
	transaction.order = nil
	return created
}

func (transaction *toolServiceTransaction) detachReservations() []toolInstanceReservation {
	if transaction == nil {
		return nil
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	reservations := append([]toolInstanceReservation(nil), transaction.reservations...)
	transaction.reservations = nil
	return reservations
}

func (transaction *toolServiceTransaction) cleanupAndRelease(
	extra ...toolInstanceReservation,
) error {
	closeErr := transaction.closeCreated()
	if closeErr != nil {
		return closeErr
	}
	reservations := transaction.detachReservations()
	reservations = append(reservations, extra...)
	for index := len(reservations) - 1; index >= 0; index-- {
		reservations[index].release()
	}
	return nil
}

func (transaction *toolServiceTransaction) track(value any) {
	if transaction == nil || isTypedNil(value) {
		return
	}
	transaction.mu.Lock()
	transaction.order = append(transaction.order, value)
	transaction.mu.Unlock()
}

func (transaction *toolServiceTransaction) closeCreated() error {
	if transaction == nil {
		return nil
	}
	transaction.mu.Lock()
	order := append([]any(nil), transaction.order...)
	transaction.order = nil
	transaction.overlay = make(map[string]any)
	transaction.mu.Unlock()
	return closeOwnerCreatedValues(order)
}

func closeOwnerCreatedValues(values []any) error {
	closed := make(map[uintptr]struct{})
	var closeErrors []error
	for index := len(values) - 1; index >= 0; index-- {
		if closer, ok := values[index].(io.Closer); ok && !isTypedNil(closer) {
			identity := interfacePointer(closer)
			if identity != 0 {
				if _, exists := closed[identity]; exists {
					continue
				}
				closed[identity] = struct{}{}
			}
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						closeErrors = append(closeErrors, fmt.Errorf("close tool resource: panic: %v", recovered))
					}
				}()
				if err := closer.Close(); err != nil {
					closeErrors = append(closeErrors, err)
				}
			}()
		}
	}
	return errors.Join(closeErrors...)
}

func serviceInstanceIdentity(value any) (toolInstanceKey, bool, error) {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Pointer:
		return toolInstanceKey{typeOf: reflected.Type(), pointer: reflected.Pointer()}, true, nil
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return toolInstanceKey{}, false, nil
	default:
		return toolInstanceKey{}, false,
			fmt.Errorf("owner-local service must be a pointer or immutable scalar, got %s", reflected.Kind())
	}
}
