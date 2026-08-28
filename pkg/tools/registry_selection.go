package tools

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ToolInstantiationCapability is a detached construction-classification
// projection. It exposes no Tool pointer, factory, owner, or authorization.
type ToolInstantiationCapability struct {
	Name            string
	FactoryBacked   bool
	ImmutableShared bool
}

// RegisterFactoryBacked registers live for compatibility execution and
// attaches factory metadata for later owner construction without invoking
// factory.New. The caller owns live and must quiesce its use before closing the
// source registry; ToolRegistry.Close releases the source lease but never
// closes live.
func (r *ToolRegistry) RegisterFactoryBacked(live Tool, factory ToolFactory) error {
	return r.registerFactoryBacked(live, factory, true)
}

// RegisterHiddenFactoryBacked is the hidden-entry form of
// RegisterFactoryBacked.
func (r *ToolRegistry) RegisterHiddenFactoryBacked(live Tool, factory ToolFactory) error {
	return r.registerFactoryBacked(live, factory, false)
}

func (r *ToolRegistry) registerFactoryBacked(live Tool, factory ToolFactory, core bool) error {
	if r == nil {
		return fmt.Errorf("tool registry is nil")
	}
	if isTypedNil(live) {
		return fmt.Errorf("factory-backed live tool is nil")
	}
	if isTypedNil(factory) {
		return fmt.Errorf("tool factory is nil")
	}
	descriptor, traits, err := snapshotFactoryMetadata(factory)
	if err != nil {
		return err
	}
	if traits.Sharing != ToolSharingPerOwner {
		return fmt.Errorf("factory-backed tool %q must use per-owner sharing", descriptor.Name)
	}
	identity, err := toolInstanceIdentity(live)
	if err != nil {
		return fmt.Errorf("factory-backed live tool %q: %w", descriptor.Name, err)
	}

	// Serialize with SetMediaStore so the published live tool observes the exact
	// store generation recorded by its entry.
	r.mediaApplyMu.Lock()
	defer r.mediaApplyMu.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return fmt.Errorf("tool registry is closed")
	}
	if !r.toolAllowedLocked(descriptor.Name) {
		r.mu.Unlock()
		return nil
	}
	if r.tools[descriptor.Name] != nil {
		r.mu.Unlock()
		return fmt.Errorf("tool %q is already registered", descriptor.Name)
	}
	dependency, dependencyErr := r.factoryDependencyForPromotionLocked(
		descriptor,
		traits,
		factory,
	)
	if dependencyErr != nil {
		r.mu.Unlock()
		return dependencyErr
	}
	store := r.mediaStore
	mediaGeneration := r.mediaGen
	r.mu.Unlock()

	if reserveErr := globalOwnedToolInstances.reserve(identity, live); reserveErr != nil {
		return fmt.Errorf("factory-backed live tool %q: %w", descriptor.Name, reserveErr)
	}
	reservation := toolInstanceReservation{tracker: globalOwnedToolInstances, identity: identity}
	release := true
	defer func() {
		if release {
			reservation.release()
		}
	}()
	actual, err := safeToolDescriptor(live)
	if err != nil {
		return err
	}
	if !descriptorsEqual(actual, descriptor) {
		return fmt.Errorf("factory-backed live tool %q does not match its frozen descriptor", descriptor.Name)
	}
	if err := injectFactoryMediaStore(live, store); err != nil {
		return fmt.Errorf("configure factory-backed live tool %q media store: %w", descriptor.Name, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("tool registry closed during factory-backed registration")
	}
	if r.mediaGen != mediaGeneration {
		return fmt.Errorf("tool registry media store changed during factory-backed registration")
	}
	if r.tools[descriptor.Name] != nil {
		return fmt.Errorf("tool %q changed during factory-backed registration", descriptor.Name)
	}
	if err := r.validateFactoryDependencyPromotionLocked(
		dependency,
		descriptor,
		traits,
		factory,
	); err != nil {
		return err
	}
	frozen := cloneToolDescriptor(descriptor)
	delete(r.constructionCatalog, descriptor.Name)
	r.tools[descriptor.Name] = &ToolEntry{
		Tool: live, IsCore: core, TTL: 0,
		descriptor: &frozen, traits: traits, factory: factory,
	}
	r.compatibilityReservations = append(r.compatibilityReservations, reservation)
	r.version.Add(1)
	release = false
	return nil
}

// InstantiationCapabilities returns every registered entry's detached,
// deterministic construction classification, including root-only legacy
// entries whose booleans are both false.
func (r *ToolRegistry) InstantiationCapabilities() []ToolInstantiationCapability {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name, entry := range r.tools {
		if entry != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	result := make([]ToolInstantiationCapability, 0, len(names))
	for _, name := range names {
		entry := r.tools[name]
		result = append(result, ToolInstantiationCapability{
			Name:            name,
			FactoryBacked:   !isTypedNil(entry.factory),
			ImmutableShared: entry.immutableShared,
		})
	}
	return result
}

// InstantiateForOwnerSelection constructs only exact selected roots. A
// factory may Resolve another classified entry as a private dependency; that
// dependency is owner-retained but is not published unless also selected.
// roots must be non-nil; an empty slice intentionally creates an empty owned
// registry.
func (r *ToolRegistry) InstantiateForOwnerSelection(
	owner ToolOwner,
	roots []string,
) (result *ToolRegistry, returnErr error) {
	if r == nil {
		return nil, fmt.Errorf("tool registry is nil")
	}
	if roots == nil {
		return nil, fmt.Errorf("tool owner selection must be explicit")
	}
	destination, err := NewOwnedToolRegistryWithDiagnosticPolicy(
		owner,
		r.diagnosticOwnerCap,
	)
	if err != nil {
		return nil, err
	}

	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return nil, fmt.Errorf("source tool registry is closed")
	}
	entries := snapshotFactoryEntriesLocked(
		r.tools,
		r.privateConstruction,
		r.constructionCatalog,
	)
	version := r.version.Load()
	mediaGeneration := r.mediaGen
	destination.mediaStore = r.mediaStore
	destination.mediaGen = r.mediaGen
	r.mu.RUnlock()

	selected := make(map[string]struct{}, len(roots))
	orderedRoots := make([]string, 0, len(roots))
	for _, name := range roots {
		if name == "" || name != strings.TrimSpace(name) {
			return nil, fmt.Errorf("selected tool name must be exact and non-empty")
		}
		if _, duplicate := selected[name]; duplicate {
			return nil, fmt.Errorf("selected tool %q is duplicated", name)
		}
		entry, exists := entries[name]
		if !exists || !entry.public {
			return nil, fmt.Errorf("selected tool %q is not registered", name)
		}
		if entry.descriptor == nil || isTypedNil(entry.factory) && !entry.immutableShared {
			return nil, fmt.Errorf("selected legacy tool %q has no owner factory", name)
		}
		selected[name] = struct{}{}
		orderedRoots = append(orderedRoots, name)
	}
	sort.Strings(orderedRoots)
	destination.exactRegistrationCap = make(map[string]struct{}, len(selected))
	for name := range selected {
		destination.exactRegistrationCap[name] = struct{}{}
	}

	services := newToolServiceTransaction(nil)
	reservations := make([]toolInstanceReservation, 0, len(entries))
	failed := true
	defer func() {
		if failed {
			returnErr = errors.Join(returnErr, services.cleanupAndRelease(reservations...))
		}
	}()
	states := make(map[string]uint8, len(entries))
	built := make(map[string]Tool, len(entries))
	construction := &ownerToolConstruction{
		owner: owner, entries: entries, destination: destination,
		services: services, reservations: &reservations, built: built,
	}
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
			return built[name], nil
		}
		states[name] = 1
		if entry.descriptor == nil {
			return nil, fmt.Errorf("legacy tool %q has no owner factory", name)
		}
		construction.resolve = build
		product, constructionErr := construction.buildEntry(name, entry)
		if constructionErr != nil {
			return nil, constructionErr
		}
		built[name] = product
		states[name] = 2
		return product, nil
	}

	for _, name := range orderedRoots {
		if _, err := build(name); err != nil {
			return nil, err
		}
	}
	builtNames := make([]string, 0, len(built))
	for name := range built {
		builtNames = append(builtNames, name)
	}
	sort.Strings(builtNames)
	for _, name := range builtNames {
		entry := entries[name]
		frozen := cloneToolDescriptor(*entry.descriptor)
		destinationEntry := &ToolEntry{
			Tool: built[name], IsCore: entry.core, TTL: entry.ttl,
			descriptor: &frozen, traits: entry.traits, factory: entry.factory,
			immutableShared: entry.immutableShared, visibilityRevision: entry.visibilityRevision,
		}
		if _, selectedRoot := selected[name]; selectedRoot {
			destination.tools[name] = destinationEntry
		} else {
			destination.privateConstruction[name] = destinationEntry
		}
	}
	if len(orderedRoots) > 0 {
		dormantReservations, catalogErr := retainDormantConstructionCatalog(
			destination,
			entries,
			built,
		)
		reservations = append(reservations, dormantReservations...)
		if catalogErr != nil {
			return nil, catalogErr
		}
	}

	r.mu.RLock()
	if r.closed || r.version.Load() != version || r.mediaGen != mediaGeneration {
		r.mu.RUnlock()
		return nil, fmt.Errorf("source tool registry changed during owner selection")
	}
	for name, entry := range entries {
		if !factoryEntryClassified(entry) {
			continue
		}
		if sourceFactoryEntryLocked(r, name, entry) != entry.source {
			r.mu.RUnlock()
			return nil, fmt.Errorf("source tool %q changed during owner selection", name)
		}
	}
	for _, name := range orderedRoots {
		if r.tools[name].TTL != entries[name].ttl || r.tools[name].IsCore != entries[name].core ||
			r.tools[name].visibilityRevision != entries[name].visibilityRevision {
			r.mu.RUnlock()
			return nil, fmt.Errorf("source tool %q visibility changed during owner selection", name)
		}
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

func snapshotFactoryEntriesLocked(
	publicEntries map[string]*ToolEntry,
	privateEntries map[string]*ToolEntry,
	catalogEntries map[string]*ToolEntry,
) map[string]factoryEntrySnapshot {
	result := make(
		map[string]factoryEntrySnapshot,
		len(publicEntries)+len(privateEntries)+len(catalogEntries),
	)
	appendEntries := func(entries map[string]*ToolEntry, public, catalog bool) {
		for name, entry := range entries {
			if entry == nil {
				continue
			}
			var descriptor *ToolDescriptor
			if entry.descriptor != nil {
				frozen := cloneToolDescriptor(*entry.descriptor)
				descriptor = &frozen
			}
			result[name] = factoryEntrySnapshot{
				name: name, tool: entry.Tool, core: entry.IsCore, ttl: entry.TTL,
				descriptor: descriptor, traits: entry.traits, factory: entry.factory,
				immutableShared: entry.immutableShared, source: entry,
				visibilityRevision: entry.visibilityRevision, public: public, catalog: catalog,
			}
		}
	}
	appendEntries(catalogEntries, false, true)
	appendEntries(privateEntries, false, false)
	appendEntries(publicEntries, true, false)
	return result
}

func sourceFactoryEntryLocked(
	registry *ToolRegistry,
	name string,
	entry factoryEntrySnapshot,
) *ToolEntry {
	if entry.public {
		return registry.tools[name]
	}
	if entry.catalog {
		return registry.constructionCatalog[name]
	}
	return registry.privateConstruction[name]
}

func retainDormantConstructionCatalog(
	destination *ToolRegistry,
	entries map[string]factoryEntrySnapshot,
	built map[string]Tool,
) ([]toolInstanceReservation, error) {
	reservations := make([]toolInstanceReservation, 0)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := entries[name]
		if _, constructed := built[name]; constructed || !factoryEntryClassified(entry) {
			continue
		}
		if entry.immutableShared {
			identity, identityErr := toolInstanceIdentity(entry.tool)
			if identityErr != nil {
				return reservations, fmt.Errorf("immutable-shared tool %q: %w", name, identityErr)
			}
			if reserveErr := globalOwnedToolInstances.reserveImmutableShared(
				identity,
				entry.tool,
			); reserveErr != nil {
				return reservations, fmt.Errorf("immutable-shared tool %q: %w", name, reserveErr)
			}
			reservations = append(reservations, toolInstanceReservation{
				tracker: globalOwnedToolInstances, identity: identity, immutableShared: true,
			})
		}
		frozen := cloneToolDescriptor(*entry.descriptor)
		destination.constructionCatalog[name] = &ToolEntry{
			Tool: entry.tool, IsCore: entry.core, TTL: entry.ttl,
			descriptor: &frozen, traits: entry.traits, factory: entry.factory,
			immutableShared: entry.immutableShared, visibilityRevision: entry.visibilityRevision,
		}
	}
	return reservations, nil
}

func factoryEntryClassified(entry factoryEntrySnapshot) bool {
	return entry.descriptor != nil && (entry.immutableShared || !isTypedNil(entry.factory))
}

func toolMapContainsPointer(entries map[string]Tool, candidate Tool) bool {
	for _, entry := range entries {
		if samePointerIdentity(entry, candidate) {
			return true
		}
	}
	return false
}
