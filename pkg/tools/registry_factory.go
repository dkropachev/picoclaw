package tools

import (
	"errors"
	"fmt"
	"sort"

	"github.com/sipeed/picoclaw/pkg/media"
)

func (r *ToolRegistry) RegisterFactory(factory ToolFactory) error {
	return r.registerFactory(factory, true)
}

func (r *ToolRegistry) RegisterHiddenFactory(factory ToolFactory) error {
	return r.registerFactory(factory, false)
}

func (r *ToolRegistry) registerFactory(factory ToolFactory, core bool) error {
	if r == nil {
		return fmt.Errorf("tool registry is nil")
	}
	if isTypedNil(factory) {
		return fmt.Errorf("tool factory is nil")
	}
	r.mu.RLock()
	owned, closed := r.owned, r.closed
	r.mu.RUnlock()
	if !owned {
		return fmt.Errorf("factory tool requires an owned registry")
	}
	if closed {
		return fmt.Errorf("tool registry is closed")
	}
	descriptor, traits, err := snapshotFactoryMetadata(factory)
	if err != nil {
		return err
	}
	if traits.Sharing != ToolSharingPerOwner {
		return fmt.Errorf("factory tool %q must use per-owner sharing", descriptor.Name)
	}

	r.mu.Lock()
	if !r.owned {
		r.mu.Unlock()
		return fmt.Errorf("factory tool %q requires an owned registry", descriptor.Name)
	}
	if r.closed {
		r.mu.Unlock()
		return fmt.Errorf("tool registry is closed")
	}
	if !r.toolAllowedLocked(descriptor.Name) {
		r.mu.Unlock()
		return nil
	}
	owner := r.owner
	sourceEntries := make(map[string]*ToolEntry, len(r.tools))
	for name, entry := range r.tools {
		sourceEntries[name] = entry
	}
	if r.tools[descriptor.Name] != nil {
		r.mu.Unlock()
		return fmt.Errorf("tool %q is already registered", descriptor.Name)
	}
	r.mu.Unlock()

	r.mu.RLock()
	mediaStore := r.mediaStore
	mediaGen := r.mediaGen
	serviceBase := r.services.snapshot()
	r.mu.RUnlock()

	services := newToolServiceTransaction(serviceBase)
	resolved := make(map[string]*ToolEntry)
	context := ToolBuildContext{owner: owner, services: services, registry: r}
	context.resolve = func(name string) (Tool, error) {
		r.mu.RLock()
		entry := r.tools[name]
		r.mu.RUnlock()
		if entry == nil || isTypedNil(entry.Tool) {
			return nil, fmt.Errorf("tool dependency %q is missing", name)
		}
		resolved[name] = entry
		return entry.Tool, nil
	}

	tool, err := callToolFactory(factory, descriptor.Name, context)
	if err != nil {
		if !isTypedNil(tool) && registryContainsToolPointer(sourceEntries, tool) {
			return errors.Join(
				err,
				fmt.Errorf("factory tool %q returned a registered instance with an error", descriptor.Name),
				services.cleanupAndRelease(),
			)
		}
		return cleanupFactoryCallError(tool, services, err)
	}
	if isTypedNil(tool) {
		cleanupErr := services.cleanupAndRelease()
		return errors.Join(fmt.Errorf("factory tool %q returned nil", descriptor.Name), cleanupErr)
	}
	reusedSource := registryContainsToolPointer(sourceEntries, tool)
	if reusedSource {
		cleanupErr := services.cleanupAndRelease()
		return errors.Join(
			fmt.Errorf("per-owner factory for %q reused a registered instance", descriptor.Name),
			cleanupErr,
		)
	}
	identity, identityErr := toolInstanceIdentity(tool)
	if identityErr != nil {
		services.track(tool)
		cleanupErr := services.cleanupAndRelease()
		return errors.Join(fmt.Errorf("factory tool %q: %w", descriptor.Name, identityErr), cleanupErr)
	}
	if err := globalOwnedToolInstances.reserve(identity, tool); err != nil {
		// The candidate belongs to another live owner and must not be closed here.
		cleanupErr := services.cleanupAndRelease()
		return errors.Join(fmt.Errorf("factory tool %q: %w", descriptor.Name, err), cleanupErr)
	}
	reservation := toolInstanceReservation{tracker: globalOwnedToolInstances, identity: identity}
	cleanupReserved := func() error {
		return services.cleanupAndRelease(reservation)
	}
	services.track(tool)
	if err := injectFactoryMediaStore(tool, mediaStore); err != nil {
		cleanupErr := cleanupReserved()
		return errors.Join(fmt.Errorf("configure factory tool %q media store: %w", descriptor.Name, err), cleanupErr)
	}
	if err := validateFactoryToolDescriptor(tool, descriptor); err != nil {
		cleanupErr := cleanupReserved()
		return errors.Join(err, cleanupErr)
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		cleanupErr := cleanupReserved()
		return errors.Join(fmt.Errorf("tool registry closed during factory construction"), cleanupErr)
	}
	if r.mediaGen != mediaGen {
		r.mu.Unlock()
		cleanupErr := cleanupReserved()
		return errors.Join(fmt.Errorf("tool registry media store changed during factory construction"), cleanupErr)
	}
	if r.tools[descriptor.Name] != nil {
		r.mu.Unlock()
		cleanupErr := cleanupReserved()
		return errors.Join(fmt.Errorf("tool %q changed during factory construction", descriptor.Name), cleanupErr)
	}
	for name, expected := range resolved {
		if r.tools[name] != expected {
			r.mu.Unlock()
			cleanupErr := cleanupReserved()
			return errors.Join(fmt.Errorf("tool dependency %q changed during factory construction", name), cleanupErr)
		}
	}
	if err := services.commit(r.services); err != nil {
		r.mu.Unlock()
		cleanupErr := cleanupReserved()
		return errors.Join(err, cleanupErr)
	}
	created := services.detachCreated()
	serviceReservations := services.detachReservations()
	frozen := cloneToolDescriptor(descriptor)
	r.tools[descriptor.Name] = &ToolEntry{
		Tool: tool, IsCore: core, TTL: 0,
		descriptor: &frozen, traits: traits, factory: factory,
	}
	r.ownedValues = append(r.ownedValues, created...)
	r.reservations = append(r.reservations, serviceReservations...)
	r.reservations = append(r.reservations, reservation)
	r.version.Add(1)
	r.mu.Unlock()
	return nil
}

func (r *ToolRegistry) RegisterImmutableShared(tool Tool, traits ToolTraits) error {
	return r.registerImmutableShared(tool, traits, true)
}

func (r *ToolRegistry) RegisterHiddenImmutableShared(tool Tool, traits ToolTraits) error {
	return r.registerImmutableShared(tool, traits, false)
}

func (r *ToolRegistry) registerImmutableShared(tool Tool, traits ToolTraits, core bool) error {
	if r == nil {
		return fmt.Errorf("tool registry is nil")
	}
	if isTypedNil(tool) {
		return fmt.Errorf("immutable shared tool is nil")
	}
	r.mu.RLock()
	owned, closed := r.owned, r.closed
	r.mu.RUnlock()
	if !owned {
		return fmt.Errorf("immutable shared tool requires an owned registry")
	}
	if closed {
		return fmt.Errorf("tool registry is closed")
	}
	normalized, err := traits.normalized()
	if err != nil {
		return err
	}
	normalized.Sharing = ToolSharingImmutableShared
	if normalized.Parallel != ToolParallelSafe {
		return fmt.Errorf("immutable shared tool requires parallel-safe traits")
	}
	if _, mutableMedia := tool.(mediaStoreAware); mutableMedia {
		return fmt.Errorf("media-store-aware tool cannot be immutable shared")
	}
	descriptor, err := safeToolDescriptor(tool)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.owned {
		return fmt.Errorf("immutable shared tool %q requires an owned registry", descriptor.Name)
	}
	if r.closed {
		return fmt.Errorf("tool registry is closed")
	}
	if !r.toolAllowedLocked(descriptor.Name) {
		return nil
	}
	if r.tools[descriptor.Name] != nil {
		return fmt.Errorf("tool %q is already registered", descriptor.Name)
	}
	frozen := cloneToolDescriptor(descriptor)
	r.tools[descriptor.Name] = &ToolEntry{
		Tool:            tool,
		IsCore:          core,
		TTL:             0,
		descriptor:      &frozen,
		traits:          normalized,
		immutableShared: true,
	}
	r.version.Add(1)
	return nil
}

type factoryEntrySnapshot struct {
	name            string
	tool            Tool
	core            bool
	ttl             int
	descriptor      *ToolDescriptor
	traits          ToolTraits
	factory         ToolFactory
	immutableShared bool
}

func (r *ToolRegistry) InstantiateForOwner(
	owner ToolOwner,
) (result *ToolRegistry, returnErr error) {
	if r == nil {
		return nil, fmt.Errorf("tool registry is nil")
	}
	r.mu.RLock()
	if !r.owned || r.closed {
		r.mu.RUnlock()
		return nil, fmt.Errorf("strict owner instantiation requires an open owned registry")
	}
	r.mu.RUnlock()
	destination, err := NewOwnedToolRegistry(owner)
	if err != nil {
		return nil, err
	}
	r.mu.RLock()
	entries := make(map[string]factoryEntrySnapshot, len(r.tools))
	for name, entry := range r.tools {
		if entry == nil {
			continue
		}
		var descriptor *ToolDescriptor
		if entry.descriptor != nil {
			frozen := cloneToolDescriptor(*entry.descriptor)
			descriptor = &frozen
		}
		entries[name] = factoryEntrySnapshot{
			name: name, tool: entry.Tool, core: entry.IsCore, ttl: entry.TTL,
			descriptor: descriptor, traits: entry.traits, factory: entry.factory,
			immutableShared: entry.immutableShared,
		}
	}
	if r.allowlist != nil {
		destination.allowlist = make(map[string]struct{}, len(r.allowlist))
		for name := range r.allowlist {
			destination.allowlist[name] = struct{}{}
		}
	}
	destination.mediaStore = r.mediaStore
	destination.mediaGen = r.mediaGen
	version := r.version.Load()
	r.mu.RUnlock()

	services := newToolServiceTransaction(nil)
	reservations := make([]toolInstanceReservation, 0, len(entries))
	failed := true
	defer func() {
		if failed {
			returnErr = errors.Join(returnErr, services.cleanupAndRelease(reservations...))
		}
	}()
	states := make(map[string]uint8, len(entries))
	var build func(string) (Tool, error)
	build = func(name string) (Tool, error) {
		entry, ok := entries[name]
		if !ok {
			return nil, fmt.Errorf("tool dependency %q is missing", name)
		}
		switch states[name] {
		case 1:
			return nil, fmt.Errorf("tool factory dependency cycle at %q", name)
		case 2:
			return destination.tools[name].Tool, nil
		}
		states[name] = 1
		if entry.descriptor == nil {
			return nil, fmt.Errorf("legacy tool %q has no owner factory", name)
		}

		var tool Tool
		if entry.immutableShared {
			if entry.traits.Sharing != ToolSharingImmutableShared ||
				entry.traits.Parallel != ToolParallelSafe {
				return nil, fmt.Errorf("tool %q has unsafe immutable sharing metadata", name)
			}
			tool = entry.tool
		} else {
			if isTypedNil(entry.factory) {
				return nil, fmt.Errorf("tool %q has no owner factory", name)
			}
			context := ToolBuildContext{
				owner: owner, services: services, registry: destination, resolve: build,
			}
			created, err := callToolFactory(entry.factory, name, context)
			if err != nil {
				if !isTypedNil(created) {
					if factorySnapshotContainsToolPointer(entries, created) ||
						registryContainsToolPointer(destination.tools, created) {
						return nil, errors.Join(
							err,
							fmt.Errorf("factory tool %q returned a foreign instance with an error", name),
						)
					}
					identity, identityErr := toolInstanceIdentity(created)
					if identityErr != nil {
						services.track(created)
						return nil, errors.Join(err, identityErr)
					}
					if reserveErr := globalOwnedToolInstances.reserve(identity, created); reserveErr != nil {
						return nil, errors.Join(err, reserveErr)
					}
					reservations = append(reservations, toolInstanceReservation{
						tracker: globalOwnedToolInstances, identity: identity,
					})
					services.track(created)
				}
				return nil, err
			}
			if isTypedNil(created) {
				return nil, fmt.Errorf("factory tool %q returned nil", name)
			}
			if factorySnapshotContainsToolPointer(entries, created) {
				return nil, fmt.Errorf("per-owner factory for %q reused the source instance", name)
			}
			if registryContainsToolPointer(destination.tools, created) {
				return nil, fmt.Errorf("per-owner factory for %q reused a destination instance", name)
			}
			identity, identityErr := toolInstanceIdentity(created)
			if identityErr != nil {
				services.track(created)
				return nil, fmt.Errorf("per-owner factory for %q: %w", name, identityErr)
			}
			if err := globalOwnedToolInstances.reserve(identity, created); err != nil {
				return nil, fmt.Errorf("per-owner factory for %q: %w", name, err)
			}
			reservations = append(reservations, toolInstanceReservation{
				tracker: globalOwnedToolInstances, identity: identity,
			})
			services.track(created)
			tool = created
			if err := injectFactoryMediaStore(tool, destination.mediaStore); err != nil {
				return nil, fmt.Errorf("configure owner tool %q media store: %w", name, err)
			}
			if err := validateFactoryToolDescriptor(tool, *entry.descriptor); err != nil {
				return nil, err
			}
		}
		frozen := cloneToolDescriptor(*entry.descriptor)
		destination.tools[name] = &ToolEntry{
			Tool: tool, IsCore: entry.core, TTL: entry.ttl,
			descriptor: &frozen, traits: entry.traits, factory: entry.factory,
			immutableShared: entry.immutableShared,
		}
		states[name] = 2
		return tool, nil
	}

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := build(name); err != nil {
			return nil, err
		}
	}
	r.mu.RLock()
	sourceClosed := r.closed
	sourceVersion := r.version.Load()
	if sourceClosed || sourceVersion != version {
		r.mu.RUnlock()
		return nil, fmt.Errorf("source tool registry changed during owner instantiation")
	}
	if err := services.commit(destination.services); err != nil {
		r.mu.RUnlock()
		return nil, err
	}
	destination.ownedValues = services.detachCreated()
	destination.reservations = services.detachReservations()
	destination.reservations = append(destination.reservations, reservations...)
	destination.version.Store(version)
	r.mu.RUnlock()
	failed = false
	return destination, nil
}

func registryContainsToolPointer(entries map[string]*ToolEntry, candidate Tool) bool {
	for _, entry := range entries {
		if entry != nil && samePointerIdentity(entry.Tool, candidate) {
			return true
		}
	}
	return false
}

func factorySnapshotContainsToolPointer(entries map[string]factoryEntrySnapshot, candidate Tool) bool {
	for _, entry := range entries {
		if samePointerIdentity(entry.tool, candidate) {
			return true
		}
	}
	return false
}

func snapshotFactoryMetadata(factory ToolFactory) (
	descriptor ToolDescriptor,
	traits ToolTraits,
	err error,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("read tool factory metadata: panic: %v", recovered)
		}
	}()
	descriptor, err = freezeToolDescriptor(factory.Descriptor())
	if err != nil {
		return ToolDescriptor{}, ToolTraits{}, err
	}
	traits, err = factory.Traits().normalized()
	return descriptor, traits, err
}

func callToolFactory(factory ToolFactory, name string, ctx ToolBuildContext) (tool Tool, err error) {
	ctx, deactivate := activateToolBuildContext(ctx)
	defer deactivate()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("construct tool %q: panic: %v", name, recovered)
			tool = nil
		}
	}()
	return factory.New(ctx)
}

func cleanupFactoryCallError(tool Tool, services *toolServiceTransaction, constructionErr error) error {
	if isTypedNil(tool) {
		return errors.Join(constructionErr, services.cleanupAndRelease())
	}
	identity, identityErr := toolInstanceIdentity(tool)
	if identityErr != nil {
		services.track(tool)
		return errors.Join(constructionErr, identityErr, services.cleanupAndRelease())
	}
	if reserveErr := globalOwnedToolInstances.reserve(identity, tool); reserveErr != nil {
		// Candidate belongs to another live owner. Never close it here.
		return errors.Join(constructionErr, reserveErr, services.cleanupAndRelease())
	}
	services.track(tool)
	closeErr := services.cleanupAndRelease(toolInstanceReservation{
		tracker: globalOwnedToolInstances, identity: identity,
	})
	return errors.Join(constructionErr, closeErr)
}

func safeToolDescriptor(tool Tool) (descriptor ToolDescriptor, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("read tool descriptor: panic: %v", recovered)
		}
	}()
	return toolDescriptorFromTool(tool)
}

func validateFactoryToolDescriptor(tool Tool, expected ToolDescriptor) error {
	actual, err := safeToolDescriptor(tool)
	if err != nil {
		return err
	}
	if !descriptorsEqual(actual, expected) {
		return fmt.Errorf("factory tool %q does not match its frozen descriptor", expected.Name)
	}
	return nil
}

func injectFactoryMediaStore(tool Tool, store media.MediaStore) (err error) {
	aware, ok := tool.(mediaStoreAware)
	if !ok {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	aware.SetMediaStore(store)
	return nil
}
