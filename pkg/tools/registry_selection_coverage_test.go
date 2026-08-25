package tools

import (
	"errors"
	"strings"
	"testing"
)

func selectionCoverageSource(
	t *testing.T,
	name string,
	build ToolBuildFunc,
) *ToolRegistry {
	t.Helper()
	registry := NewToolRegistry()
	live := newMockTool(name, name)
	factory := mustFactoryForTool(t, live, ToolTraits{}, build)
	if err := registry.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	return registry
}

func TestToolRegistryFactoryBackedCoverageAdmissionFailures(t *testing.T) {
	validLive := newMockTool("coverage_live", "coverage live")
	validFactory := mustFactoryForTool(t, validLive, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return newMockTool("coverage_live", "coverage live"), nil
	})
	var nilRegistry *ToolRegistry
	if err := nilRegistry.RegisterFactoryBacked(validLive, validFactory); err == nil {
		t.Fatal("nil registry accepted factory-backed registration")
	}
	var nilLive *mockRegistryTool
	if err := NewToolRegistry().RegisterFactoryBacked(nilLive, validFactory); err == nil {
		t.Fatal("typed-nil live tool was accepted")
	}
	var nilFactory *coverageToolFactory
	if err := NewToolRegistry().RegisterFactoryBacked(validLive, nilFactory); err == nil {
		t.Fatal("typed-nil factory was accepted")
	}

	metadataCases := []ToolFactory{
		&coverageToolFactory{descriptorFn: func() ToolDescriptor { panic("metadata panic") }},
		&coverageToolFactory{
			descriptor: coverageFactoryDescriptor("coverage_live"),
			traits:     ToolTraits{Risk: ToolRiskClass("invalid")},
			newFn:      func(ToolBuildContext) (Tool, error) { return nil, nil },
		},
	}
	for index, factory := range metadataCases {
		if err := NewToolRegistry().RegisterFactoryBacked(validLive, factory); err == nil {
			t.Fatalf("invalid metadata case %d was accepted", index)
		}
	}
	valueLive := factoryValueTool{}
	valueFactory := mustFactoryForTool(t, valueLive, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return factoryValueTool{}, nil
	})
	if err := NewToolRegistry().RegisterFactoryBacked(valueLive, valueFactory); err == nil ||
		!strings.Contains(err.Error(), "non-nil pointer") {
		t.Fatalf("value live error = %v", err)
	}

	closed := NewToolRegistry()
	closed.closed = true
	if err := closed.RegisterFactoryBacked(validLive, validFactory); err == nil {
		t.Fatal("closed registry accepted factory-backed registration")
	}
	collision := NewToolRegistry()
	collision.Register(newMockTool("coverage_live", "occupant"))
	if err := collision.RegisterFactoryBacked(validLive, validFactory); err == nil {
		t.Fatal("occupied registry accepted factory-backed registration")
	}

	mismatchLive := newMockTool("coverage_live", "different")
	if err := NewToolRegistry().RegisterFactoryBacked(mismatchLive, validFactory); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("live descriptor mismatch error = %v", err)
	}
	panicLive := &factoryMetadataTool{panicNow: true}
	panicFactory := &coverageToolFactory{
		descriptor: coverageFactoryDescriptor("panic_live"),
		newFn:      func(ToolBuildContext) (Tool, error) { return nil, nil },
	}
	if err := NewToolRegistry().RegisterFactoryBacked(panicLive, panicFactory); err == nil {
		t.Fatal("panicking live metadata was accepted")
	}
	mediaPanic := &coveragePanicMediaTool{
		mockRegistryTool: newMockTool("media_panic_live", "media_panic_live"),
	}
	mediaFactory := mustFactoryForTool(t, mediaPanic, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return newMockTool("media_panic_live", "media_panic_live"), nil
	})
	if err := NewToolRegistry().RegisterFactoryBacked(mediaPanic, mediaFactory); err == nil {
		t.Fatal("panicking media setter was accepted")
	}
}

func TestToolRegistryFactoryBackedCoverageCommitConflicts(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ToolRegistry, string)
	}{
		{name: "closed", mutate: func(registry *ToolRegistry, _ string) {
			registry.mu.Lock()
			registry.closed = true
			registry.mu.Unlock()
		}},
		{name: "media", mutate: func(registry *ToolRegistry, _ string) {
			registry.mu.Lock()
			registry.mediaGen++
			registry.mu.Unlock()
		}},
		{name: "collision", mutate: func(registry *ToolRegistry, name string) {
			registry.Register(newMockTool(name, "collision"))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := NewToolRegistry()
			name := "commit_" + test.name
			live := &coverageHookTool{name: name}
			live.hook = func() { test.mutate(registry, name) }
			prototype := &coverageHookTool{name: name}
			factory := mustFactoryForTool(t, prototype, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
				return &coverageHookTool{name: name}, nil
			})
			if err := registry.RegisterFactoryBacked(live, factory); err == nil {
				t.Fatal("commit conflict was accepted")
			}
		})
	}
}

func TestToolRegistrySelectionCoverageDependencyFailures(t *testing.T) {
	var nilRegistry *ToolRegistry
	if child, err := nilRegistry.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "nil-source"), []string{},
	); err == nil || child != nil {
		t.Fatalf("nil source selection = %#v, %v", child, err)
	}
	invalidOwnerSource := NewToolRegistry()
	if child, err := invalidOwnerSource.InstantiateForOwnerSelection(
		ToolOwner{},
		[]string{},
	); err == nil ||
		child != nil {
		t.Fatalf("invalid owner selection = %#v, %v", child, err)
	}
	closedSource := NewToolRegistry()
	closedSource.closed = true
	if child, err := closedSource.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "closed-source"), []string{},
	); err == nil || child != nil {
		t.Fatalf("closed source selection = %#v, %v", child, err)
	}

	missing := selectionCoverageSource(t, "missing_wrapper", func(ctx ToolBuildContext) (Tool, error) {
		if _, err := ctx.Resolve("absent"); err != nil {
			return nil, err
		}
		return newMockTool("missing_wrapper", "missing_wrapper"), nil
	})
	if child, err := missing.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "missing-dependency"), []string{"missing_wrapper"},
	); err == nil || child != nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing dependency selection = %#v, %v", child, err)
	}

	legacy := NewToolRegistry()
	legacy.Register(newMockTool("legacy_dependency", "legacy dependency"))
	wrapper := newMockTool("legacy_wrapper", "legacy wrapper")
	wrapperFactory := mustFactoryForTool(t, wrapper, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		if _, err := ctx.Resolve("legacy_dependency"); err != nil {
			return nil, err
		}
		return newMockTool("legacy_wrapper", "legacy wrapper"), nil
	})
	if err := legacy.RegisterFactoryBacked(wrapper, wrapperFactory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = legacy.Close() })
	if child, err := legacy.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "legacy-dependency"), []string{"legacy_wrapper"},
	); err == nil || child != nil || !strings.Contains(err.Error(), "legacy tool") {
		t.Fatalf("legacy dependency selection = %#v, %v", child, err)
	}

	missingFactory := selectionCoverageSource(t, "missing_factory", func(ToolBuildContext) (Tool, error) {
		return newMockTool("missing_factory", "missing_factory"), nil
	})
	missingFactory.tools["missing_factory"].factory = nil
	if child, err := missingFactory.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "missing-factory"), []string{"missing_factory"},
	); err == nil || child != nil {
		t.Fatalf("missing factory selection = %#v, %v", child, err)
	}

	privateMissingFactory := NewToolRegistry()
	dependency := newMockTool("private_missing_factory", "private_missing_factory")
	privateMissingFactory.Register(dependency)
	descriptor, descriptorErr := safeToolDescriptor(dependency)
	if descriptorErr != nil {
		t.Fatal(descriptorErr)
	}
	privateMissingFactory.tools["private_missing_factory"].descriptor = &descriptor
	wrapperLive := newMockTool("private_missing_wrapper", "private_missing_wrapper")
	privateWrapperFactory := mustFactoryForTool(t, wrapperLive, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		if _, err := ctx.Resolve("private_missing_factory"); err != nil {
			return nil, err
		}
		return newMockTool("private_missing_wrapper", "private_missing_wrapper"), nil
	})
	if err := privateMissingFactory.RegisterFactoryBacked(wrapperLive, privateWrapperFactory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = privateMissingFactory.Close() })
	if child, err := privateMissingFactory.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "private-missing-factory"),
		[]string{"private_missing_wrapper"},
	); err == nil || child != nil || !strings.Contains(err.Error(), "no owner factory") {
		t.Fatalf("private missing factory selection = %#v, %v", child, err)
	}

	cycle := NewToolRegistry()
	for _, name := range []string{"cycle_a", "cycle_b"} {
		other := "cycle_a"
		if name == "cycle_a" {
			other = "cycle_b"
		}
		liveTool := newMockTool(name, name)
		factory := mustFactoryForTool(t, liveTool, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
			if _, err := ctx.Resolve(other); err != nil {
				return nil, err
			}
			return newMockTool(name, name), nil
		})
		if err := cycle.RegisterFactoryBacked(liveTool, factory); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = cycle.Close() })
	if child, err := cycle.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "selection-cycle"), []string{"cycle_a"},
	); err == nil || child != nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("dependency cycle selection = %#v, %v", child, err)
	}
}

func TestToolRegistrySelectionCoverageFactoryFailures(t *testing.T) {
	for _, test := range []struct {
		name  string
		build ToolBuildFunc
	}{
		{name: "error", build: func(ToolBuildContext) (Tool, error) {
			return nil, errors.New("selection construction failed")
		}},
		{name: "panic", build: func(ToolBuildContext) (Tool, error) {
			panic("selection construction panic")
		}},
		{name: "nil", build: func(ToolBuildContext) (Tool, error) { return nil, nil }},
		{name: "typed_nil", build: func(ToolBuildContext) (Tool, error) {
			var tool *mockRegistryTool
			return tool, nil
		}},
		{name: "value", build: func(ToolBuildContext) (Tool, error) {
			return factoryValueTool{}, nil
		}},
		{name: "value_error", build: func(ToolBuildContext) (Tool, error) {
			return factoryValueTool{}, errors.New("value construction failed")
		}},
		{name: "descriptor", build: func(ToolBuildContext) (Tool, error) {
			return newMockTool("wrong", "wrong"), nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := selectionCoverageSource(t, "selection_failure", test.build)
			if child, err := source.InstantiateForOwnerSelection(
				factoryTestOwner(ToolOwnerScopeAgent, "failure-"+test.name),
				[]string{"selection_failure"},
			); err == nil || child != nil {
				t.Fatalf("factory failure selection = %#v, %v", child, err)
			}
		})
	}

	mediaSource := selectionCoverageSource(t, "selection_media_panic", func(ToolBuildContext) (Tool, error) {
		return &coveragePanicMediaTool{
			mockRegistryTool: newMockTool("selection_media_panic", "selection_media_panic"),
		}, nil
	})
	if child, err := mediaSource.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "selection-media-panic"),
		[]string{"selection_media_panic"},
	); err == nil || child != nil {
		t.Fatalf("media panic selection = %#v, %v", child, err)
	}

	for _, withError := range []bool{false, true} {
		candidate := newMockTool("reserved_selection", "reserved_selection")
		identity, identityErr := toolInstanceIdentity(candidate)
		if identityErr != nil {
			t.Fatal(identityErr)
		}
		if reserveErr := globalOwnedToolInstances.reserve(identity, candidate); reserveErr != nil {
			t.Fatal(reserveErr)
		}
		source := selectionCoverageSource(t, "reserved_selection", func(ToolBuildContext) (Tool, error) {
			if withError {
				return candidate, errors.New("reserved construction failed")
			}
			return candidate, nil
		})
		child, err := source.InstantiateForOwnerSelection(
			factoryTestOwner(ToolOwnerScopeAgent, "reserved-selection"),
			[]string{"reserved_selection"},
		)
		if err == nil || child != nil {
			t.Fatalf("reserved selection withError=%t = %#v, %v", withError, child, err)
		}
		globalOwnedToolInstances.release(identity)
	}
}

func TestToolRegistrySelectionCoverageErrorProductsAndReuse(t *testing.T) {
	closed := []string{}
	freshError := selectionCoverageSource(t, "fresh_error_product", func(ToolBuildContext) (Tool, error) {
		return &selectionCloseTool{
			mockRegistryTool: newMockTool("fresh_error_product", "fresh_error_product"),
			label:            "fresh", closed: &closed,
		}, errors.New("fresh error")
	})
	if child, err := freshError.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "fresh-error-product"),
		[]string{"fresh_error_product"},
	); err == nil || child != nil || len(closed) != 1 {
		t.Fatalf("fresh error product = %#v, closed:%v error:%v", child, closed, err)
	}

	foreign := newMockTool("foreign_error_product", "foreign_error_product")
	foreignSource := NewToolRegistry()
	foreignSource.Register(foreign)
	live := newMockTool("foreign_error_wrapper", "foreign_error_wrapper")
	factory := mustFactoryForTool(t, live, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return foreign, errors.New("foreign error")
	})
	if err := foreignSource.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = foreignSource.Close() })
	if child, err := foreignSource.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "foreign-error-product"),
		[]string{"foreign_error_wrapper"},
	); err == nil || child != nil || !strings.Contains(err.Error(), "foreign instance") {
		t.Fatalf("foreign error product = %#v, %v", child, err)
	}

	singleton := newMockTool("owner_singleton", "owner_singleton")
	source := NewToolRegistry()
	for _, name := range []string{"a_owner", "b_owner"} {
		liveTool := newMockTool(name, name)
		ownerFactory := mustFactoryForTool(t, liveTool, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
			singleton.name = name
			singleton.desc = name
			return singleton, nil
		})
		if err := source.RegisterFactoryBacked(liveTool, ownerFactory); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = source.Close() })
	if child, err := source.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "owner-reuse"), []string{"a_owner", "b_owner"},
	); err == nil || child != nil {
		t.Fatalf("owner product reuse = %#v, %v", child, err)
	}
}

func TestToolRegistrySelectionCoverageHelpersAndDestinationConflict(t *testing.T) {
	source := NewToolRegistry()
	source.tools["nil_entry"] = nil
	if capabilities := source.InstantiationCapabilities(); len(capabilities) != 0 {
		t.Fatalf("nil entry capability = %#v", capabilities)
	}
	empty, instantiateErr := source.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "nil-entry-snapshot"), []string{},
	)
	if instantiateErr != nil {
		t.Fatal(instantiateErr)
	}
	_ = empty.Close()
	conflict := selectionCoverageSource(t, "service_conflict", func(ctx ToolBuildContext) (Tool, error) {
		if _, serviceErr := ctx.Service("conflict", func() (any, error) {
			return &factoryService{id: 1}, nil
		}); serviceErr != nil {
			return nil, serviceErr
		}
		destination := destinationRegistryForBuild(ctx)
		destination.services.mu.Lock()
		destination.services.values["conflict"] = &factoryService{id: 2}
		destination.services.mu.Unlock()
		return newMockTool("service_conflict", "service_conflict"), nil
	})
	if child, selectionErr := conflict.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "service-conflict"), []string{"service_conflict"},
	); selectionErr == nil || child != nil {
		t.Fatalf("destination service conflict = %#v, %v", child, selectionErr)
	}
	if toolMapContainsPointer(nil, newMockTool("none", "none")) {
		t.Fatal("nil built map reported a pointer")
	}

	unsafe, registryErr := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "unsafe-selection"))
	if registryErr != nil {
		t.Fatal(registryErr)
	}
	shared := newMockTool("unsafe_selected", "unsafe_selected")
	if err := unsafe.RegisterImmutableShared(shared, ToolTraits{Parallel: ToolParallelSafe}); err != nil {
		t.Fatal(err)
	}
	unsafe.tools["unsafe_selected"].traits.Parallel = ToolParallelSerialized
	if child, err := unsafe.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "unsafe-selected"), []string{"unsafe_selected"},
	); err == nil || child != nil {
		t.Fatalf("unsafe immutable selection = %#v, %v", child, err)
	}
	_ = unsafe.Close()

	invalidShared, invalidSharedErr := NewOwnedToolRegistry(factoryTestOwner(
		ToolOwnerScopeRegistry,
		"invalid-selected-shared",
	))
	if invalidSharedErr != nil {
		t.Fatal(invalidSharedErr)
	}
	t.Cleanup(func() { _ = invalidShared.Close() })
	invalidTool := newMockTool("invalid_selected_shared", "invalid selected shared")
	if registerErr := invalidShared.RegisterImmutableShared(invalidTool, ToolTraits{
		Parallel: ToolParallelSafe,
	}); registerErr != nil {
		t.Fatal(registerErr)
	}
	invalidShared.tools["invalid_selected_shared"].Tool = factoryValueTool{}
	if child, selectionErr := invalidShared.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "invalid-selected-shared-child"),
		[]string{"invalid_selected_shared"},
	); selectionErr == nil || child != nil || !strings.Contains(selectionErr.Error(), "non-nil pointer") {
		t.Fatalf("invalid selected immutable = %#v, %v", child, selectionErr)
	}
	_ = invalidShared.Close()

	conflictingShared, conflictingSharedErr := NewOwnedToolRegistry(factoryTestOwner(
		ToolOwnerScopeRegistry,
		"conflicting-selected-shared",
	))
	if conflictingSharedErr != nil {
		t.Fatal(conflictingSharedErr)
	}
	t.Cleanup(func() { _ = conflictingShared.Close() })
	original := newMockTool("conflicting_selected_shared", "conflicting selected shared")
	if registerErr := conflictingShared.RegisterImmutableShared(original, ToolTraits{
		Parallel: ToolParallelSafe,
	}); registerErr != nil {
		t.Fatal(registerErr)
	}
	foreign := newMockTool("conflicting_selected_shared", "conflicting selected shared")
	foreignIdentity, identityErr := toolInstanceIdentity(foreign)
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	if reserveErr := globalOwnedToolInstances.reserve(foreignIdentity, foreign); reserveErr != nil {
		t.Fatal(reserveErr)
	}
	t.Cleanup(func() { globalOwnedToolInstances.release(foreignIdentity) })
	conflictingShared.tools["conflicting_selected_shared"].Tool = foreign
	if child, selectionErr := conflictingShared.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "conflicting-selected-shared-child"),
		[]string{"conflicting_selected_shared"},
	); selectionErr == nil || child != nil || !strings.Contains(selectionErr.Error(), "exclusively leased") {
		t.Fatalf("conflicting selected immutable = %#v, %v", child, selectionErr)
	}
	globalOwnedToolInstances.release(foreignIdentity)
	_ = conflictingShared.Close()
}

func TestToolRegistrySelectionCoverageExactSourcePointerFence(t *testing.T) {
	registry := NewToolRegistry()
	live := newMockTool("pointer_fence", "pointer_fence")
	factory := mustFactoryForTool(t, live, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		registry.mu.Lock()
		current := registry.tools["pointer_fence"]
		replacement := *current
		registry.tools["pointer_fence"] = &replacement
		registry.mu.Unlock()
		return newMockTool("pointer_fence", "pointer_fence"), nil
	})
	if err := registry.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if child, err := registry.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "pointer-fence"), []string{"pointer_fence"},
	); err == nil || child != nil || !strings.Contains(err.Error(), "source tool") {
		t.Fatalf("source pointer fence selection = %#v, %v", child, err)
	}
}

func TestToolRegistryFactoryBackedCoverageFrozenMetadata(t *testing.T) {
	live := &factoryMetadataTool{
		name: "compat_frozen", desc: "compat frozen",
		params: map[string]any{"type": "object"},
	}
	factory := mustFactoryForTool(t, live, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return &factoryMetadataTool{
			name: "compat_frozen", desc: "compat frozen",
			params: map[string]any{"type": "object"},
		}, nil
	})
	registry := NewToolRegistry()
	if err := registry.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	live.mu.Lock()
	live.panicNow = true
	live.mu.Unlock()
	if definitions := registry.ToProviderDefs(); len(definitions) != 1 ||
		definitions[0].Function.Name != "compat_frozen" {
		t.Fatalf("frozen compatibility definition = %#v", definitions)
	}
	if child, err := registry.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "compat-frozen"), []string{"compat_frozen"},
	); err != nil || child == nil {
		t.Fatalf("frozen compatibility selection = %#v, %v", child, err)
	} else if closeErr := child.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestToolRegistryDormantImmutableCatalogCoverageFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		product  Tool
		reserve  bool
		wantText string
	}{
		{name: "invalid", product: factoryValueTool{}, wantText: "non-nil pointer"},
		{name: "reserved", product: newMockTool("dormant_reserved", "dormant reserved"), reserve: true, wantText: "exclusively leased"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := NewToolRegistry()
			wrapper := newMockTool("dormant_wrapper", "dormant wrapper")
			wrapperFactory := mustFactoryForTool(t, wrapper, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
				return newMockTool("dormant_wrapper", "dormant wrapper"), nil
			})
			if err := source.RegisterFactoryBacked(wrapper, wrapperFactory); err != nil {
				t.Fatal(err)
			}
			descriptor := coverageFactoryDescriptor("dormant_reserved")
			source.constructionCatalog["dormant_reserved"] = &ToolEntry{
				Tool: test.product, descriptor: &descriptor,
				traits: ToolTraits{
					Parallel: ToolParallelSafe, Sharing: ToolSharingImmutableShared,
				},
				immutableShared: true,
			}
			if test.reserve {
				identity, identityErr := toolInstanceIdentity(test.product)
				if identityErr != nil {
					t.Fatal(identityErr)
				}
				if reserveErr := globalOwnedToolInstances.reserve(identity, test.product); reserveErr != nil {
					t.Fatal(reserveErr)
				}
				defer globalOwnedToolInstances.release(identity)
			}
			child, selectionErr := source.InstantiateForOwnerSelection(
				factoryTestOwner(ToolOwnerScopeAgent, "dormant-failure-"+test.name),
				[]string{"dormant_wrapper"},
			)
			if selectionErr == nil || child != nil || !strings.Contains(selectionErr.Error(), test.wantText) {
				t.Fatalf("dormant failure = %#v, %v", child, selectionErr)
			}
			_ = source.Close()
		})
	}

	owned, ownedErr := NewOwnedToolRegistry(factoryTestOwner(
		ToolOwnerScopeRegistry,
		"full-dormant-failure",
	))
	if ownedErr != nil {
		t.Fatal(ownedErr)
	}
	if registerErr := owned.RegisterFactory(mustFactoryForTool(
		t,
		newMockTool("full_dormant_wrapper", "full dormant wrapper"),
		ToolTraits{},
		func(ToolBuildContext) (Tool, error) {
			return newMockTool("full_dormant_wrapper", "full dormant wrapper"), nil
		},
	)); registerErr != nil {
		t.Fatal(registerErr)
	}
	invalidDescriptor := coverageFactoryDescriptor("full_dormant_invalid")
	owned.constructionCatalog["full_dormant_invalid"] = &ToolEntry{
		Tool: factoryValueTool{}, descriptor: &invalidDescriptor,
		traits:          ToolTraits{Parallel: ToolParallelSafe, Sharing: ToolSharingImmutableShared},
		immutableShared: true,
	}
	if child, instantiateErr := owned.InstantiateForOwner(
		factoryTestOwner(ToolOwnerScopeAgent, "full-dormant-failure-child"),
	); instantiateErr == nil || child != nil || !strings.Contains(instantiateErr.Error(), "non-nil pointer") {
		t.Fatalf("full dormant failure = %#v, %v", child, instantiateErr)
	}
	_ = owned.Close()
}
