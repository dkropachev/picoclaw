package tools

import "fmt"

// RegisterFactoryDependency retains a dormant per-owner factory for use only
// by ToolBuildContext.Resolve during owner instantiation. It does not construct
// or publish a tool and is deliberately independent of the registry's public
// registration allowlist.
//
// A later public factory registration may replace the dormant dependency only
// when it supplies the exact same factory object and frozen metadata. This
// makes promotion deterministic while preventing two behaviorally different
// factories with the same provider-facing descriptor from being conflated.
func (r *ToolRegistry) RegisterFactoryDependency(factory ToolFactory) error {
	if r == nil {
		return fmt.Errorf("tool registry is nil")
	}
	if isTypedNil(factory) {
		return fmt.Errorf("tool factory is nil")
	}
	descriptor, traits, err := snapshotFactoryMetadata(factory)
	if err != nil {
		return err
	}
	if traits.Sharing != ToolSharingPerOwner {
		return fmt.Errorf("factory dependency %q must use per-owner sharing", descriptor.Name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("tool registry is closed")
	}
	if entry := r.tools[descriptor.Name]; entry != nil {
		if factoryEntryMatches(entry, descriptor, traits, factory) {
			return nil
		}
		return fmt.Errorf("factory dependency %q collides with a public tool", descriptor.Name)
	}
	if entry := r.privateConstruction[descriptor.Name]; entry != nil {
		if factoryEntryMatches(entry, descriptor, traits, factory) {
			return nil
		}
		return fmt.Errorf("factory dependency %q collides with an owner-built dependency", descriptor.Name)
	}
	if entry := r.constructionCatalog[descriptor.Name]; entry != nil {
		if err := validateDormantFactoryDependency(entry, descriptor, traits, factory); err != nil {
			return err
		}
		return nil
	}

	frozen := cloneToolDescriptor(descriptor)
	if r.constructionCatalog == nil {
		r.constructionCatalog = make(map[string]*ToolEntry)
	}
	r.constructionCatalog[descriptor.Name] = &ToolEntry{
		descriptor: &frozen,
		traits:     traits,
		factory:    factory,
	}
	r.version.Add(1)
	return nil
}

// factoryDependencyForPromotionLocked returns the exact catalog entry a
// public registration must remove at commit. r.mu must be held by the caller.
func (r *ToolRegistry) factoryDependencyForPromotionLocked(
	descriptor ToolDescriptor,
	traits ToolTraits,
	factory ToolFactory,
) (*ToolEntry, error) {
	if r.privateConstruction[descriptor.Name] != nil {
		return nil, fmt.Errorf(
			"factory tool %q collides with an owner-built dependency",
			descriptor.Name,
		)
	}
	dependency := r.constructionCatalog[descriptor.Name]
	if dependency == nil {
		return nil, nil
	}
	if err := validateDormantFactoryDependency(dependency, descriptor, traits, factory); err != nil {
		return nil, err
	}
	return dependency, nil
}

// validateFactoryDependencyPromotionLocked fences changes between admission
// and publication. r.mu must be held by the caller.
func (r *ToolRegistry) validateFactoryDependencyPromotionLocked(
	expected *ToolEntry,
	descriptor ToolDescriptor,
	traits ToolTraits,
	factory ToolFactory,
) error {
	if r.privateConstruction[descriptor.Name] != nil {
		return fmt.Errorf(
			"factory tool %q owner-built dependency changed during registration",
			descriptor.Name,
		)
	}
	current := r.constructionCatalog[descriptor.Name]
	if current != expected {
		return fmt.Errorf(
			"factory dependency %q changed during public registration",
			descriptor.Name,
		)
	}
	if current != nil {
		return validateDormantFactoryDependency(current, descriptor, traits, factory)
	}
	return nil
}

func validateDormantFactoryDependency(
	entry *ToolEntry,
	descriptor ToolDescriptor,
	traits ToolTraits,
	factory ToolFactory,
) error {
	name := descriptor.Name
	if entry == nil || !isTypedNil(entry.Tool) || entry.descriptor == nil ||
		entry.immutableShared || isTypedNil(entry.factory) {
		return fmt.Errorf("factory dependency %q has an ambiguous catalog collision", name)
	}
	if !descriptorsEqual(*entry.descriptor, descriptor) {
		return fmt.Errorf("factory dependency %q descriptor does not match public factory", name)
	}
	if entry.traits != traits {
		return fmt.Errorf("factory dependency %q traits do not match public factory", name)
	}
	if !sameInterfaceIdentity(entry.factory, factory) {
		return fmt.Errorf("factory dependency %q uses a different factory instance", name)
	}
	return nil
}

func factoryEntryMatches(
	entry *ToolEntry,
	descriptor ToolDescriptor,
	traits ToolTraits,
	factory ToolFactory,
) bool {
	return entry != nil && entry.descriptor != nil && !entry.immutableShared &&
		!isTypedNil(entry.factory) && descriptorsEqual(*entry.descriptor, descriptor) &&
		entry.traits == traits && sameInterfaceIdentity(entry.factory, factory)
}
