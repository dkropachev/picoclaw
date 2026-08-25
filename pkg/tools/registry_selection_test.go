package tools

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/media"
)

type selectionDependencyTool struct {
	*mockRegistryTool
	dependency Tool
}

type selectionCloseTool struct {
	*mockRegistryTool
	label  string
	closed *[]string
}

type selectionDescriptorGuardTool struct {
	*mockRegistryTool
	panicOnName atomic.Bool
}

func (tool *selectionDescriptorGuardTool) Name() string {
	if tool.panicOnName.Load() {
		panic("leased tool metadata was called")
	}
	return tool.mockRegistryTool.Name()
}

func (tool *selectionCloseTool) Close() error {
	*tool.closed = append(*tool.closed, tool.label)
	return nil
}

func TestToolRegistryFactoryBackedCompatibilityRegistration(t *testing.T) {
	var calls atomic.Int64
	live := NewUpdatePlanTool()
	factory := NewUpdatePlanToolFactory()
	blocked := NewToolRegistry()
	blocked.SetAllowlist([]string{})
	counting := &coverageToolFactory{
		descriptor: factory.Descriptor(),
		traits:     factory.Traits(),
		newFn: func(ToolBuildContext) (Tool, error) {
			calls.Add(1)
			return NewUpdatePlanTool(), nil
		},
	}
	if err := blocked.RegisterFactoryBacked(live, counting); err != nil ||
		blocked.Count() != 0 || calls.Load() != 0 {
		t.Fatalf("blocked registration = count:%d calls:%d error:%v", blocked.Count(), calls.Load(), err)
	}

	registry := NewToolRegistry()
	if err := registry.RegisterFactoryBacked(live, counting); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	registered, ok := registry.GetRegistered("update_plan")
	if !ok || registered != live || calls.Load() != 0 {
		t.Fatalf("live registration = %#v, %t, calls=%d", registered, ok, calls.Load())
	}
	capabilities := registry.InstantiationCapabilities()
	if !reflect.DeepEqual(capabilities, []ToolInstantiationCapability{{
		Name: "update_plan", FactoryBacked: true,
	}}) {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if result := registry.Execute(context.Background(), "update_plan", map[string]any{
		"plan": []any{map[string]any{"step": "root", "status": "completed"}},
	}); result.IsError {
		t.Fatalf("root live execution failed: %#v", result)
	}
	if calls.Load() != 0 {
		t.Fatal("root execution invoked owner factory")
	}
	if err := registry.RegisterFactoryBacked(NewUpdatePlanTool(), NewUpdatePlanToolFactory()); err == nil {
		t.Fatal("factory-backed collision was accepted")
	}

	mismatched := mustFactoryForTool(t, newMockTool("different", "different"), ToolTraits{},
		func(ToolBuildContext) (Tool, error) { return newMockTool("different", "different"), nil })
	if err := NewToolRegistry().RegisterFactoryBacked(newMockTool("live", "live"), mismatched); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("descriptor mismatch error = %v", err)
	}
	sharedFactory := &coverageToolFactory{
		descriptor: coverageFactoryDescriptor("shared_factory"),
		traits: ToolTraits{
			Parallel: ToolParallelSafe, Sharing: ToolSharingImmutableShared,
		},
		newFn: func(ToolBuildContext) (Tool, error) {
			return newMockTool("shared_factory", "shared_factory"), nil
		},
	}
	if err := NewToolRegistry().RegisterFactoryBacked(
		newMockTool("shared_factory", "shared_factory"), sharedFactory,
	); err == nil || !strings.Contains(err.Error(), "per-owner") {
		t.Fatalf("sharing error = %v", err)
	}
}

func TestToolRegistryFactoryBackedSourceLeaseAndClose(t *testing.T) {
	closed := []string{}
	live := &selectionCloseTool{
		mockRegistryTool: newMockTool("leased_live", "leased live"),
		label:            "live", closed: &closed,
	}
	factory := mustFactoryForTool(t, live, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return &selectionCloseTool{
			mockRegistryTool: newMockTool("leased_live", "leased live"),
			label:            "child", closed: &closed,
		}, nil
	})
	first := NewToolRegistry()
	if err := first.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatal(err)
	}
	clone := first.Clone()
	if err := clone.Close(); err != nil || clone.Count() != 1 {
		t.Fatalf("compatibility clone Close = count:%d error:%v", clone.Count(), err)
	}
	second := NewToolRegistry()
	if err := second.RegisterFactoryBacked(live, factory); err == nil ||
		!strings.Contains(err.Error(), "another owner") {
		t.Fatalf("live source reuse error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if len(closed) != 0 || first.Count() != 0 {
		t.Fatalf("compatibility Close closed live tool or retained entries: closed=%v count=%d", closed, first.Count())
	}
	first.Register(newMockTool("resurrect", "resurrect"))
	if first.Count() != 0 {
		t.Fatal("closed compatibility source was resurrected")
	}
	if err := second.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatalf("source lease was not released after Close: %v", err)
	}
	if err := second.Close(); err != nil || len(closed) != 0 {
		t.Fatalf("second compatibility Close = closed:%v error:%v", closed, err)
	}

	legacy := NewToolRegistry()
	legacy.Register(newMockTool("legacy", "legacy"))
	if err := legacy.Close(); err != nil || legacy.Count() != 1 {
		t.Fatalf("plain legacy Close changed behavior: count=%d error=%v", legacy.Count(), err)
	}
}

func TestToolRegistryFactoryBackedRejectedReuseDoesNotMutateForeignMedia(t *testing.T) {
	storeOne := media.NewFileMediaStore()
	storeTwo := media.NewFileMediaStore()
	live := &mockMediaStoreAwareTool{
		mockRegistryTool: *newMockTool("leased_media", "leased media"),
	}
	factory := mustFactoryForTool(t, live, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return &mockMediaStoreAwareTool{
			mockRegistryTool: *newMockTool("leased_media", "leased media"),
		}, nil
	})
	first := NewToolRegistry()
	first.SetMediaStore(storeOne)
	if err := first.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if live.store != storeOne {
		t.Fatal("first registration did not inject its media store")
	}
	second := NewToolRegistry()
	second.SetMediaStore(storeTwo)
	if err := second.RegisterFactoryBacked(live, factory); err == nil ||
		!strings.Contains(err.Error(), "another owner") {
		t.Fatalf("foreign media reuse error = %v", err)
	}
	if live.store != storeOne {
		t.Fatalf("rejected registration mutated foreign media: store=%p", live.store)
	}
}

func TestToolRegistryFactoryBackedLeaseExcludesImmutableSharing(t *testing.T) {
	live := &selectionDescriptorGuardTool{
		mockRegistryTool: newMockTool("lease_mode", "lease mode"),
	}
	factory := mustFactoryForTool(t, live, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return &selectionDescriptorGuardTool{
			mockRegistryTool: newMockTool("lease_mode", "lease mode"),
		}, nil
	})
	compatibility := NewToolRegistry()
	if err := compatibility.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatal(err)
	}
	immutableSource, err := NewOwnedToolRegistry(factoryTestOwner(
		ToolOwnerScopeRegistry,
		"immutable-after-compatibility",
	))
	if err != nil {
		t.Fatal(err)
	}
	live.panicOnName.Store(true)
	if registerErr := immutableSource.RegisterImmutableShared(live, ToolTraits{
		Parallel: ToolParallelSafe,
	}); registerErr == nil || !strings.Contains(registerErr.Error(), "exclusively leased") {
		t.Fatalf("compatibility-first immutable registration error = %v", registerErr)
	}
	live.panicOnName.Store(false)
	if closeErr := compatibility.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if registerErr := immutableSource.RegisterImmutableShared(live, ToolTraits{
		Parallel: ToolParallelSafe,
	}); registerErr != nil {
		t.Fatal(registerErr)
	}

	child, err := immutableSource.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "immutable-child"),
		[]string{"lease_mode"},
	)
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := child.InstantiateForOwner(
		factoryTestOwner(ToolOwnerScopeTurn, "immutable-grandchild"),
	)
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := immutableSource.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "immutable-sibling"),
		[]string{"lease_mode"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := immutableSource.Close(); err != nil {
		t.Fatal(err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sibling.Close(); err != nil {
		t.Fatal(err)
	}

	blocked := NewToolRegistry()
	live.panicOnName.Store(true)
	if registerErr := blocked.RegisterFactoryBacked(live, factory); registerErr == nil ||
		!strings.Contains(registerErr.Error(), "another owner") {
		t.Fatalf("immutable-first compatibility registration error = %v", registerErr)
	}
	live.panicOnName.Store(false)
	if err := grandchild.Close(); err != nil {
		t.Fatal(err)
	}
	if err := blocked.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatalf("last immutable share did not release the pointer: %v", err)
	}
	if err := blocked.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestToolRegistryExclusiveAndImmutableFirstRegistrationRace(t *testing.T) {
	for iteration := range 32 {
		name := fmt.Sprintf("lease_race_%d", iteration)
		live := newMockTool(name, name)
		factory := mustFactoryForTool(t, live, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
			return newMockTool(name, name), nil
		})
		compatibility := NewToolRegistry()
		immutable, err := NewOwnedToolRegistry(factoryTestOwner(
			ToolOwnerScopeRegistry,
			name,
		))
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			<-start
			results <- compatibility.RegisterFactoryBacked(live, factory)
		}()
		go func() {
			defer workers.Done()
			<-start
			results <- immutable.RegisterImmutableShared(live, ToolTraits{
				Parallel: ToolParallelSafe,
			})
		}()
		close(start)
		workers.Wait()
		close(results)
		successes := 0
		for result := range results {
			if result == nil {
				successes++
			}
		}
		if successes != 1 {
			t.Fatalf("iteration %d lease winners = %d, want 1", iteration, successes)
		}
		if err := compatibility.Close(); err != nil {
			t.Fatal(err)
		}
		if err := immutable.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestToolRegistryConcurrentImmutableRegistrationsRetainEveryShare(t *testing.T) {
	const owners = 16
	live := newMockTool("concurrent_shared", "concurrent shared")
	factory := mustFactoryForTool(t, live, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return newMockTool("concurrent_shared", "concurrent shared"), nil
	})
	sources := make([]*ToolRegistry, 0, owners)
	for index := range owners {
		source, err := NewOwnedToolRegistry(factoryTestOwner(
			ToolOwnerScopeRegistry,
			fmt.Sprintf("concurrent-shared-%d", index),
		))
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, source)
	}
	t.Cleanup(func() {
		for _, source := range sources {
			_ = source.Close()
		}
	})
	start := make(chan struct{})
	results := make(chan error, owners)
	var workers sync.WaitGroup
	for _, source := range sources {
		workers.Add(1)
		go func(registry *ToolRegistry) {
			defer workers.Done()
			<-start
			results <- registry.RegisterImmutableShared(live, ToolTraits{
				Parallel: ToolParallelSafe,
			})
		}(source)
	}
	close(start)
	workers.Wait()
	close(results)
	for registerErr := range results {
		if registerErr != nil {
			t.Fatalf("concurrent immutable registration failed: %v", registerErr)
		}
	}
	compatibility := NewToolRegistry()
	if registerErr := compatibility.RegisterFactoryBacked(live, factory); registerErr == nil {
		t.Fatal("exclusive registration bypassed concurrent immutable shares")
	}
	for _, source := range sources[:owners-1] {
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if registerErr := compatibility.RegisterFactoryBacked(live, factory); registerErr == nil {
		t.Fatal("exclusive registration ignored the final immutable share")
	}
	if err := sources[owners-1].Close(); err != nil {
		t.Fatal(err)
	}
	if err := compatibility.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatalf("exclusive registration remained blocked after all shares closed: %v", err)
	}
	if err := compatibility.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestToolRegistryInstantiationCapabilitiesAreSortedAndDetached(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(newMockTool("z_legacy", "legacy"))
	live := NewUpdatePlanTool()
	if err := registry.RegisterFactoryBacked(live, NewUpdatePlanToolFactory()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	got := registry.InstantiationCapabilities()
	want := []ToolInstantiationCapability{
		{Name: "update_plan", FactoryBacked: true},
		{Name: "z_legacy"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("capabilities = %#v, want %#v", got, want)
	}
	got[0].Name = "mutated"
	if registry.InstantiationCapabilities()[0].Name != "update_plan" {
		t.Fatal("capability projection aliased registry state")
	}
	var nilRegistry *ToolRegistry
	if nilRegistry.InstantiationCapabilities() != nil {
		t.Fatal("nil registry returned capabilities")
	}
}

func TestToolRegistryInstantiateForOwnerSelectionPreflightAndEmpty(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(newMockTool("legacy", "legacy"))
	var calls atomic.Int64
	prototype := newMockTool("selected", "selected")
	factory := mustFactoryForTool(t, prototype, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		calls.Add(1)
		return newMockTool("selected", "selected"), nil
	})
	if err := registry.RegisterFactoryBacked(prototype, factory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	owner := factoryTestOwner(ToolOwnerScopeAgent, "selection")
	if child, err := registry.InstantiateForOwnerSelection(owner, nil); err == nil || child != nil {
		t.Fatalf("nil selection = %#v, %v", child, err)
	}
	for _, roots := range [][]string{
		{""}, {" selected"}, {"selected", "selected"}, {"missing"}, {"legacy"},
	} {
		if child, err := registry.InstantiateForOwnerSelection(owner, roots); err == nil || child != nil {
			t.Fatalf("invalid selection %#v = %#v, %v", roots, child, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("preflight invoked factory %d time(s)", calls.Load())
	}
	empty, err := registry.InstantiateForOwnerSelection(owner, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Count() != 0 || empty.AllowsRegistration(RegexSearchToolName) || calls.Load() != 0 {
		t.Fatalf("empty selection = count:%d discovery:%t calls:%d",
			empty.Count(), empty.AllowsRegistration(RegexSearchToolName), calls.Load())
	}
	empty.SetAllowlist(nil)
	if empty.AllowsRegistration("selected") || empty.AllowsRegistration(RegexSearchToolName) {
		t.Fatal("legacy allowlist clearing widened an exact empty selection")
	}
	if closeErr := empty.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestToolRegistrySelectedExactAllowlistSurvivesOwnerInstantiation(t *testing.T) {
	source := NewToolRegistry()
	if err := source.RegisterFactoryBacked(NewUpdatePlanTool(), NewUpdatePlanToolFactory()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	selected, err := source.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "exact-source"),
		[]string{"update_plan"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if selected.AllowsRegistration(RegexSearchToolName) {
		t.Fatal("selected registry allowed an unselected discovery exception")
	}
	if !selected.AllowsRegistration("update_plan") ||
		selected.AllowsRegistration("UPDATE_PLAN") ||
		selected.AllowsRegistration(" update_plan ") {
		t.Fatal("selected registry did not preserve its exact-name registration cap")
	}
	selected.SetAllowlist(nil)
	if selected.AllowsRegistration("UPDATE_PLAN") || selected.AllowsRegistration(RegexSearchToolName) {
		t.Fatal("clearing the legacy allowlist widened the exact registration cap")
	}
	selected.SetAllowlist([]string{"UPDATE_PLAN"})
	if !selected.AllowsRegistration("update_plan") || selected.AllowsRegistration("UPDATE_PLAN") {
		t.Fatal("legacy case folding replaced the exact registration cap")
	}
	selected.Register(newMockTool("UPDATE_PLAN", "case alias"))
	selected.Register(newMockTool(" update_plan ", "whitespace alias"))
	if selected.HasRegistered("UPDATE_PLAN") || selected.HasRegistered(" update_plan ") {
		t.Fatal("selected registry admitted a non-exact future registration")
	}
	descendant, err := selected.InstantiateForOwner(
		factoryTestOwner(ToolOwnerScopeTurn, "exact-descendant"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if descendant.AllowsRegistration(RegexSearchToolName) {
		t.Fatal("owner instantiation dropped the exact selection allowlist")
	}
	if !descendant.AllowsRegistration("update_plan") ||
		descendant.AllowsRegistration("UPDATE_PLAN") {
		t.Fatal("owner instantiation dropped the exact-name registration cap")
	}
	if err := descendant.Close(); err != nil {
		t.Fatal(err)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}

	compatibilityCap := NewToolRegistry()
	compatibilityCap.exactRegistrationCap = map[string]struct{}{"exact_clone": {}}
	compatibilityCap.Register(newMockTool("exact_clone", "exact clone"))
	clone := compatibilityCap.Clone()
	if !clone.AllowsRegistration("exact_clone") || clone.AllowsRegistration("EXACT_CLONE") {
		t.Fatal("compatibility clone dropped its exact registration cap")
	}
	clone.exactRegistrationCap["other"] = struct{}{}
	if compatibilityCap.AllowsRegistration("other") {
		t.Fatal("compatibility clone aliased its exact registration cap")
	}
}

func TestToolRegistryInstantiateForOwnerRejectsGenerationChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ToolRegistry)
	}{
		{name: "media", mutate: func(source *ToolRegistry) {
			source.SetMediaStore(media.NewFileMediaStore())
		}},
		{name: "visibility_aba", mutate: func(source *ToolRegistry) {
			source.PromoteTools([]string{"generation_fence"}, 1)
			source.TickTTL()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, err := NewOwnedToolRegistry(factoryTestOwner(
				ToolOwnerScopeRegistry,
				"generation-fence-"+test.name,
			))
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			release := make(chan struct{})
			factory := mustFactoryForTool(
				t,
				newMockTool("generation_fence", "generation fence"),
				ToolTraits{},
				func(ctx ToolBuildContext) (Tool, error) {
					if ctx.Owner().Scope == ToolOwnerScopeRegistry {
						return newMockTool("generation_fence", "generation fence"), nil
					}
					close(started)
					<-release
					return newMockTool("generation_fence", "generation fence"), nil
				},
			)
			if err := source.RegisterHiddenFactory(factory); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = source.Close() })
			done := make(chan error, 1)
			go func() {
				_, instantiateErr := source.InstantiateForOwner(
					factoryTestOwner(ToolOwnerScopeAgent, "generation-fence-child-"+test.name),
				)
				done <- instantiateErr
			}()
			<-started
			test.mutate(source)
			close(release)
			if instantiateErr := <-done; instantiateErr == nil ||
				!strings.Contains(instantiateErr.Error(), "changed") {
				t.Fatalf("generation change error = %v", instantiateErr)
			}
		})
	}
}

func TestToolRegistryInstantiateForOwnerSelectionPreservesActiveHiddenParity(t *testing.T) {
	source := NewToolRegistry()
	hiddenLive := newMockTool("active_hidden", "active hidden")
	hiddenFactory := mustFactoryForTool(t, hiddenLive, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return newMockTool("active_hidden", "active hidden"), nil
	})
	if err := source.RegisterHiddenFactoryBacked(hiddenLive, hiddenFactory); err != nil {
		t.Fatal(err)
	}
	coreLive := newMockTool("selected_core", "selected core")
	coreFactory := mustFactoryForTool(t, coreLive, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return newMockTool("selected_core", "selected core"), nil
	})
	if err := source.RegisterFactoryBacked(coreLive, coreFactory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	version := source.Version()
	source.PromoteTools([]string{"active_hidden"}, 3)
	if source.Version() != version {
		t.Fatal("visibility promotion changed the registry definition version")
	}

	child, err := source.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeTurn, "active-hidden-parity"),
		[]string{"active_hidden", "selected_core"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Close() }()
	hidden := child.tools["active_hidden"]
	core := child.tools["selected_core"]
	if hidden == nil || hidden.IsCore || hidden.TTL != 3 || core == nil || !core.IsCore ||
		child.Version() != version {
		t.Fatalf("selected parity = hidden:%#v core:%#v version:%d, want %d", hidden, core, child.Version(), version)
	}
	if _, ok := child.Get("active_hidden"); !ok {
		t.Fatal("promoted selected hidden tool was not callable")
	}
	for range 3 {
		child.TickTTL()
	}
	if _, ok := child.Get("active_hidden"); ok {
		t.Fatal("selected hidden tool remained callable after its owner-local TTL expired")
	}
	if _, ok := source.Get("active_hidden"); !ok {
		t.Fatal("child TTL countdown changed the compatibility source")
	}
	if child.Version() != version || source.Version() != version {
		t.Fatal("owner-local TTL countdown changed registry definition versions")
	}
}

func TestToolRegistryInstantiateForOwnerSelectionSubsetAndPrivateDependency(t *testing.T) {
	registry := NewToolRegistry()
	closed := []string{}
	dependencyLive := &selectionCloseTool{
		mockRegistryTool: newMockTool("dependency", "dependency"),
		label:            "live-dependency", closed: &closed,
	}
	dependencyFactory := mustFactoryForTool(t, dependencyLive, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return &selectionCloseTool{
			mockRegistryTool: newMockTool("dependency", "dependency"),
			label:            "dependency", closed: &closed,
		}, nil
	})
	if err := registry.RegisterHiddenFactoryBacked(dependencyLive, dependencyFactory); err != nil {
		t.Fatal(err)
	}
	wrapperLive := &selectionDependencyTool{
		mockRegistryTool: newMockTool("wrapper", "wrapper"), dependency: dependencyLive,
	}
	wrapperFactory := mustFactoryForTool(t, wrapperLive, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		dependency, err := ctx.Resolve("dependency")
		if err != nil {
			return nil, err
		}
		return &selectionDependencyTool{
			mockRegistryTool: newMockTool("wrapper", "wrapper"), dependency: dependency,
		}, nil
	})
	if err := registry.RegisterFactoryBacked(wrapperLive, wrapperFactory); err != nil {
		t.Fatal(err)
	}
	otherLive := newMockTool("other", "other")
	var otherCalls atomic.Int64
	otherFactory := mustFactoryForTool(t, otherLive, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		otherCalls.Add(1)
		return newMockTool("other", "other"), nil
	})
	if err := registry.RegisterFactoryBacked(otherLive, otherFactory); err != nil {
		t.Fatal(err)
	}
	registry.Register(newMockTool("unselected_legacy", "legacy"))
	t.Cleanup(func() { _ = registry.Close() })

	child, err := registry.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeTurn, "private-dependency"),
		[]string{"wrapper"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(child.List(), []string{"wrapper"}) ||
		child.HasRegistered("dependency") || child.HasRegistered("other") ||
		otherCalls.Load() != 0 {
		t.Fatalf("selected child leaked roots: list=%v dependency=%t other=%t calls=%d",
			child.List(), child.HasRegistered("dependency"), child.HasRegistered("other"), otherCalls.Load())
	}
	if definitions := child.ToProviderDefs(); len(definitions) != 1 ||
		definitions[0].Function.Name != "wrapper" {
		t.Fatalf("private dependency leaked to provider definitions: %#v", definitions)
	}
	if matches, searchErr := child.SearchRegex("dependency", 10); searchErr != nil || len(matches) != 0 {
		t.Fatalf("private dependency leaked to discovery: %#v, %v", matches, searchErr)
	}
	wrapperRaw, _ := child.GetRegistered("wrapper")
	wrapper := wrapperRaw.(*selectionDependencyTool)
	if wrapper.dependency == dependencyLive || wrapper.dependency == nil {
		t.Fatal("wrapper dependency was not privately owner-constructed")
	}
	if len(child.InstantiationCapabilities()) != 1 || child.privateConstruction["dependency"] == nil {
		t.Fatal("private dependency classification was exposed or discarded")
	}
	if result := child.Execute(context.Background(), "dependency", nil); !result.IsError {
		t.Fatal("private dependency became executable")
	}
	if leaked, selectionErr := child.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeTurn, "private-dependency-selection-denied"),
		[]string{"dependency"},
	); selectionErr == nil || leaked != nil {
		t.Fatalf("private dependency became directly selectable: %#v, %v", leaked, selectionErr)
	}
	selectedDescendant, err := child.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeTurn, "private-dependency-selected-descendant"),
		[]string{"wrapper"},
	)
	if err != nil {
		t.Fatal(err)
	}
	fullDescendant, err := child.InstantiateForOwner(
		factoryTestOwner(ToolOwnerScopeTurn, "private-dependency-full-descendant"),
	)
	if err != nil {
		t.Fatal(err)
	}
	selectedWrapperRaw, _ := selectedDescendant.GetRegistered("wrapper")
	fullWrapperRaw, _ := fullDescendant.GetRegistered("wrapper")
	selectedDependency := selectedWrapperRaw.(*selectionDependencyTool).dependency
	fullDependency := fullWrapperRaw.(*selectionDependencyTool).dependency
	if selectedDescendant.HasRegistered("dependency") || fullDescendant.HasRegistered("dependency") ||
		selectedDependency == wrapper.dependency || fullDependency == wrapper.dependency ||
		selectedDependency == fullDependency {
		t.Fatal("descendant construction exposed or reused a private dependency")
	}
	if closeErr := selectedDescendant.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if closeErr := fullDescendant.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	child.Unregister("wrapper")
	if registerErr := child.RegisterFactory(wrapperFactory); registerErr != nil {
		t.Fatalf("re-register selected wrapper from private dependency: %v", registerErr)
	}
	reRegisteredRaw, _ := child.GetRegistered("wrapper")
	if reRegisteredRaw.(*selectionDependencyTool).dependency != wrapper.dependency ||
		child.HasRegistered("dependency") {
		t.Fatal("re-registering the wrapper exposed or rebuilt its retained private dependency")
	}
	if closeErr := child.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if !reflect.DeepEqual(closed, []string{"dependency", "dependency", "dependency"}) {
		t.Fatalf("private dependency cleanup = %#v", closed)
	}

	both, err := registry.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeTurn, "published-dependency"),
		[]string{"dependency", "wrapper"},
	)
	if err != nil {
		t.Fatal(err)
	}
	dependency, ok := both.GetRegistered("dependency")
	wrapperRaw, _ = both.GetRegistered("wrapper")
	if !ok || wrapperRaw.(*selectionDependencyTool).dependency != dependency {
		t.Fatal("selected dependency was not published as the same owner instance")
	}
	if _, visible := both.Get("dependency"); visible {
		t.Fatal("hidden selected dependency was callable without TTL promotion")
	}
	if err := both.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestToolRegistrySelectionRetainsOwnerDependentConstructionSpecs(t *testing.T) {
	source, sourceErr := NewOwnedToolRegistry(factoryTestOwner(
		ToolOwnerScopeRegistry,
		"owner-dependent-source",
	))
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
	var dependencyACalls atomic.Int64
	var dependencyCCalls atomic.Int64
	dependencyA := newMockTool("owner_dependency_a", "owner dependency a")
	dependencyAFactory := mustFactoryForTool(t, dependencyA, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		dependencyACalls.Add(1)
		return newMockTool("owner_dependency_a", "owner dependency a"), nil
	})
	if err := source.RegisterHiddenFactoryBacked(dependencyA, dependencyAFactory); err != nil {
		t.Fatal(err)
	}
	dependencyB := newMockTool("owner_dependency_b", "owner dependency b")
	if err := source.RegisterImmutableShared(dependencyB, ToolTraits{
		Parallel: ToolParallelSafe,
	}); err != nil {
		t.Fatal(err)
	}
	dependencyC := newMockTool("owner_dependency_c", "owner dependency c")
	dependencyCFactory := mustFactoryForTool(t, dependencyC, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		dependencyCCalls.Add(1)
		return newMockTool("owner_dependency_c", "owner dependency c"), nil
	})
	if err := source.RegisterHiddenFactoryBacked(dependencyC, dependencyCFactory); err != nil {
		t.Fatal(err)
	}
	wrapperLive := &selectionDependencyTool{
		mockRegistryTool: newMockTool("owner_wrapper", "owner wrapper"),
	}
	wrapperFactory := mustFactoryForTool(t, wrapperLive, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		dependencyName := "owner_dependency_a"
		if ctx.Owner().Scope == ToolOwnerScopeTurn {
			dependencyName = "owner_dependency_b"
		}
		dependency, resolveErr := ctx.Resolve(dependencyName)
		if resolveErr != nil {
			return nil, resolveErr
		}
		return &selectionDependencyTool{
			mockRegistryTool: newMockTool("owner_wrapper", "owner wrapper"),
			dependency:       dependency,
		}, nil
	})
	if err := source.RegisterFactoryBacked(wrapperLive, wrapperFactory); err != nil {
		t.Fatal(err)
	}
	child, err := source.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "owner-dependent-child"),
		[]string{"owner_wrapper"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if child.privateConstruction["owner_dependency_a"] == nil ||
		child.constructionCatalog["owner_dependency_b"] == nil ||
		child.constructionCatalog["owner_dependency_c"] == nil ||
		dependencyACalls.Load() != 1 || dependencyCCalls.Load() != 0 {
		t.Fatal("child did not separate built and dormant owner-dependent dependencies")
	}
	if child.HasRegistered("owner_dependency_a") || child.HasRegistered("owner_dependency_b") ||
		child.HasRegistered("owner_dependency_c") ||
		len(child.InstantiationCapabilities()) != 1 || len(child.ToProviderDefs()) != 1 {
		t.Fatal("private owner-dependent catalogs leaked into outward tool projections")
	}
	child.SetAllowlist(nil)
	child.Register(newMockTool("owner_dependency_b", "owner dependency b alias"))
	if child.HasRegistered("owner_dependency_b") {
		t.Fatal("allowlist clearing exposed a dormant private construction spec")
	}
	if closeErr := source.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	dependencyBFactory := mustFactoryForTool(t, dependencyB, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return newMockTool("owner_dependency_b", "owner dependency b"), nil
	})
	compatibility := NewToolRegistry()
	if registerErr := compatibility.RegisterFactoryBacked(
		dependencyB,
		dependencyBFactory,
	); registerErr == nil {
		t.Fatal("dormant immutable spec did not retain its descendant lease")
	}
	selectedDescendant, err := child.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeTurn, "owner-dependent-selected-descendant"),
		[]string{"owner_wrapper"},
	)
	if err != nil {
		t.Fatal(err)
	}
	fullDescendant, err := child.InstantiateForOwner(
		factoryTestOwner(ToolOwnerScopeTurn, "owner-dependent-full-descendant"),
	)
	if err != nil {
		t.Fatal(err)
	}
	selectedWrapper, _ := selectedDescendant.GetRegistered("owner_wrapper")
	fullWrapper, _ := fullDescendant.GetRegistered("owner_wrapper")
	if selectedDescendant.privateConstruction["owner_dependency_b"] == nil ||
		fullDescendant.privateConstruction["owner_dependency_b"] == nil ||
		selectedDescendant.HasRegistered("owner_dependency_b") ||
		fullDescendant.HasRegistered("owner_dependency_b") ||
		selectedWrapper.(*selectionDependencyTool).dependency != dependencyB ||
		fullWrapper.(*selectionDependencyTool).dependency != dependencyB ||
		dependencyCCalls.Load() != 0 {
		t.Fatal("descendants did not rebuild their owner-dependent private dependency")
	}
	if err := selectedDescendant.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fullDescendant.Close(); err != nil {
		t.Fatal(err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if registerErr := compatibility.RegisterFactoryBacked(dependencyB, dependencyBFactory); registerErr != nil {
		t.Fatalf("dormant immutable spec lease survived descendant close: %v", registerErr)
	}
	if closeErr := compatibility.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestToolRegistrySelectionPrivatelyRetainsImmutableDependencyLease(t *testing.T) {
	source, err := NewOwnedToolRegistry(factoryTestOwner(
		ToolOwnerScopeRegistry,
		"private-immutable-source",
	))
	if err != nil {
		t.Fatal(err)
	}
	dependency := newMockTool("private_immutable", "private immutable")
	if registerErr := source.RegisterImmutableShared(dependency, ToolTraits{
		Parallel: ToolParallelSafe,
	}); registerErr != nil {
		t.Fatal(registerErr)
	}
	wrapperPrototype := &selectionDependencyTool{
		mockRegistryTool: newMockTool("immutable_wrapper", "immutable wrapper"),
	}
	wrapperFactory := mustFactoryForTool(t, wrapperPrototype, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		resolved, resolveErr := ctx.Resolve("private_immutable")
		if resolveErr != nil {
			return nil, resolveErr
		}
		return &selectionDependencyTool{
			mockRegistryTool: newMockTool("immutable_wrapper", "immutable wrapper"),
			dependency:       resolved,
		}, nil
	})
	if registerErr := source.RegisterFactory(wrapperFactory); registerErr != nil {
		t.Fatal(registerErr)
	}
	child, err := source.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "private-immutable-child"),
		[]string{"immutable_wrapper"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if child.HasRegistered("private_immutable") {
		t.Fatal("private immutable dependency became registered")
	}
	wrapperRaw, _ := child.GetRegistered("immutable_wrapper")
	if wrapperRaw.(*selectionDependencyTool).dependency != dependency {
		t.Fatal("wrapper did not retain the explicit immutable dependency")
	}
	grandchild, err := child.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeTurn, "private-immutable-grandchild"),
		[]string{"immutable_wrapper"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if grandchild.privateConstruction["private_immutable"].Tool != dependency {
		t.Fatal("grandchild did not retain the private immutable dependency")
	}
	if closeErr := source.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	dependencyFactory := mustFactoryForTool(t, dependency, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return newMockTool("private_immutable", "private immutable"), nil
	})
	compatibility := NewToolRegistry()
	if registerErr := compatibility.RegisterFactoryBacked(
		dependency,
		dependencyFactory,
	); registerErr == nil {
		t.Fatal("private immutable dependency lease was released with its source")
	}
	if closeErr := child.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if registerErr := compatibility.RegisterFactoryBacked(
		dependency,
		dependencyFactory,
	); registerErr == nil {
		t.Fatal("grandchild private immutable dependency lease was released with its parent")
	}
	if closeErr := grandchild.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if registerErr := compatibility.RegisterFactoryBacked(dependency, dependencyFactory); registerErr != nil {
		t.Fatalf("private immutable dependency lease survived grandchild close: %v", registerErr)
	}
	if err := compatibility.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestToolRegistryPrivateConstructionMediaRemainsOwnerLocal(t *testing.T) {
	storeOne := media.NewFileMediaStore()
	storeTwo := media.NewFileMediaStore()
	source := NewToolRegistry()
	source.SetMediaStore(storeOne)
	dependencyLive := &mockMediaStoreAwareTool{
		mockRegistryTool: *newMockTool("private_media", "private media"),
	}
	dependencyFactory := mustFactoryForTool(t, dependencyLive, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return &mockMediaStoreAwareTool{
			mockRegistryTool: *newMockTool("private_media", "private media"),
		}, nil
	})
	if err := source.RegisterHiddenFactoryBacked(dependencyLive, dependencyFactory); err != nil {
		t.Fatal(err)
	}
	wrapperLive := &selectionDependencyTool{
		mockRegistryTool: newMockTool("private_media_wrapper", "private media wrapper"),
	}
	wrapperFactory := mustFactoryForTool(t, wrapperLive, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		dependency, resolveErr := ctx.Resolve("private_media")
		if resolveErr != nil {
			return nil, resolveErr
		}
		return &selectionDependencyTool{
			mockRegistryTool: newMockTool("private_media_wrapper", "private media wrapper"),
			dependency:       dependency,
		}, nil
	})
	if err := source.RegisterFactoryBacked(wrapperLive, wrapperFactory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	child, err := source.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "private-media-child"),
		[]string{"private_media_wrapper"},
	)
	if err != nil {
		t.Fatal(err)
	}
	privateMedia := child.privateConstruction["private_media"].Tool.(*mockMediaStoreAwareTool)
	if privateMedia.store != storeOne || dependencyLive.store != storeOne {
		t.Fatal("private media dependency did not inherit the source snapshot")
	}
	child.SetMediaStore(storeTwo)
	if privateMedia.store != storeTwo || dependencyLive.store != storeOne {
		t.Fatal("private media update escaped its owner")
	}
	descendant, err := child.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeTurn, "private-media-descendant"),
		[]string{"private_media_wrapper"},
	)
	if err != nil {
		t.Fatal(err)
	}
	descendantMedia := descendant.privateConstruction["private_media"].Tool.(*mockMediaStoreAwareTool)
	if descendantMedia == privateMedia || descendantMedia.store != storeTwo {
		t.Fatal("descendant private media dependency was not freshly owner-bound")
	}
	if err := descendant.Close(); err != nil {
		t.Fatal(err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestToolRegistryOwnerCleanupFailureQuarantinesImmutableShare(t *testing.T) {
	source, err := NewOwnedToolRegistry(factoryTestOwner(
		ToolOwnerScopeRegistry,
		"shared-quarantine-source",
	))
	if err != nil {
		t.Fatal(err)
	}
	shared := newMockTool("quarantined_shared", "quarantined shared")
	if registerErr := source.RegisterImmutableShared(shared, ToolTraits{
		Parallel: ToolParallelSafe,
	}); registerErr != nil {
		t.Fatal(registerErr)
	}
	closeFailure := errors.New("owner close failed")
	closed := []string{}
	prototype := &factoryCloseTool{
		mockRegistryTool: newMockTool("failing_close", "failing close"),
		label:            "prototype",
		closed:           &closed,
	}
	factory := mustFactoryForTool(t, prototype, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		tool := &factoryCloseTool{
			mockRegistryTool: newMockTool("failing_close", "failing close"),
			label:            "source",
			closed:           &closed,
		}
		if ctx.Owner().Scope != ToolOwnerScopeRegistry {
			tool.label = "child"
			tool.closeErr = closeFailure
		}
		return tool, nil
	})
	if registerErr := source.RegisterFactory(factory); registerErr != nil {
		t.Fatal(registerErr)
	}
	wrapperPrototype := &selectionDependencyTool{
		mockRegistryTool: newMockTool("quarantine_wrapper", "quarantine wrapper"),
	}
	wrapperFactory := mustFactoryForTool(t, wrapperPrototype, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		dependency, resolveErr := ctx.Resolve("quarantined_shared")
		if resolveErr != nil {
			return nil, resolveErr
		}
		return &selectionDependencyTool{
			mockRegistryTool: newMockTool("quarantine_wrapper", "quarantine wrapper"),
			dependency:       dependency,
		}, nil
	})
	if registerErr := source.RegisterFactory(wrapperFactory); registerErr != nil {
		t.Fatal(registerErr)
	}
	child, err := source.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "shared-quarantine-child"),
		[]string{"failing_close", "quarantine_wrapper"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if child.HasRegistered("quarantined_shared") ||
		child.privateConstruction["quarantined_shared"] == nil {
		t.Fatal("quarantined immutable dependency was not private")
	}
	if closeErr := child.Close(); !errors.Is(closeErr, closeFailure) {
		t.Fatalf("child close error = %v", closeErr)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	sharedFactory := mustFactoryForTool(t, shared, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return newMockTool("quarantined_shared", "quarantined shared"), nil
	})
	compatibility := NewToolRegistry()
	if registerErr := compatibility.RegisterFactoryBacked(
		shared,
		sharedFactory,
	); registerErr == nil {
		t.Fatal("failed child cleanup released a quarantined immutable share")
	}
}

func TestToolRegistryInstantiateForOwnerSelectionDiscoveryAndMediaAreOwnerLocal(t *testing.T) {
	source := NewToolRegistry()
	hiddenLive := newMockTool("selected_hidden", "selected hidden tool")
	hiddenFactory := mustFactoryForTool(t, hiddenLive, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return newMockTool("selected_hidden", "selected hidden tool"), nil
	})
	if err := source.RegisterHiddenFactoryBacked(hiddenLive, hiddenFactory); err != nil {
		t.Fatal(err)
	}
	regexLive := NewRegexSearchTool(source, 2, 5)
	if err := source.RegisterFactoryBacked(regexLive, NewRegexSearchToolFactory(2, 5)); err != nil {
		t.Fatal(err)
	}
	storeOne := media.NewFileMediaStore()
	storeTwo := media.NewFileMediaStore()
	source.SetMediaStore(storeOne)
	mediaLive := &mockMediaStoreAwareTool{
		mockRegistryTool: *newMockTool("selected_media", "selected media"),
	}
	mediaFactory := mustFactoryForTool(t, mediaLive, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return &mockMediaStoreAwareTool{
			mockRegistryTool: *newMockTool("selected_media", "selected media"),
		}, nil
	})
	if err := source.RegisterFactoryBacked(mediaLive, mediaFactory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })

	roots := []string{"selected_hidden", RegexSearchToolName, "selected_media"}
	child, err := source.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeTurn, "selected-discovery"), roots,
	)
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := source.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeTurn, "selected-discovery-sibling"), roots,
	)
	if err != nil {
		t.Fatal(err)
	}
	regexRaw, _ := child.GetRegistered(RegexSearchToolName)
	regex := regexRaw.(*RegexSearchTool)
	if regex.registry != child {
		t.Fatal("selected discovery tool retained the source registry")
	}
	if result := regex.Execute(context.Background(), map[string]any{
		"pattern": "selected_hidden",
	}); result.IsError {
		t.Fatalf("selected discovery failed: %#v", result)
	}
	if _, ok := child.Get("selected_hidden"); !ok {
		t.Fatal("selected discovery did not promote the child hidden tool")
	}
	if _, ok := source.Get("selected_hidden"); ok {
		t.Fatal("selected discovery promoted the source hidden tool")
	}
	if _, ok := sibling.Get("selected_hidden"); ok {
		t.Fatal("selected discovery promoted a sibling hidden tool")
	}
	mediaRaw, _ := child.GetRegistered("selected_media")
	childMedia := mediaRaw.(*mockMediaStoreAwareTool)
	if childMedia == mediaLive || childMedia.store != storeOne || mediaLive.store != storeOne {
		t.Fatal("selected media construction did not preserve isolated store state")
	}
	child.SetMediaStore(storeTwo)
	if childMedia.store != storeTwo || mediaLive.store != storeOne {
		t.Fatal("child media update changed the compatibility source")
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sibling.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestToolRegistryInstantiateForOwnerSelectionSupportsExplicitImmutableShared(t *testing.T) {
	source, err := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "selected-shared"))
	if err != nil {
		t.Fatal(err)
	}
	shared := newMockTool("selected_shared", "selected shared")
	if registerErr := source.RegisterImmutableShared(shared, ToolTraits{
		Risk: ToolRiskReadOnly, Parallel: ToolParallelSafe,
		Idempotency: ToolIdempotencyIdempotent,
	}); registerErr != nil {
		t.Fatal(registerErr)
	}
	t.Cleanup(func() { _ = source.Close() })
	capabilities := source.InstantiationCapabilities()
	if len(capabilities) != 1 || !capabilities[0].ImmutableShared || capabilities[0].FactoryBacked {
		t.Fatalf("immutable capability = %#v", capabilities)
	}
	child, err := source.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "selected-shared-child"),
		[]string{"selected_shared"},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := child.GetRegistered("selected_shared")
	if got != shared {
		t.Fatal("explicit immutable selection did not preserve the shared pointer")
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestToolRegistryInstantiateForOwnerSelectionDetectsSourceChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ToolRegistry)
	}{
		{name: "version", mutate: func(registry *ToolRegistry) {
			registry.Register(newMockTool("late", "late"))
		}},
		{name: "media", mutate: func(registry *ToolRegistry) {
			registry.SetMediaStore(media.NewFileMediaStore())
		}},
		{name: "visibility", mutate: func(registry *ToolRegistry) {
			registry.PromoteTools([]string{"blocking"}, 3)
		}},
		{name: "visibility_aba", mutate: func(registry *ToolRegistry) {
			registry.PromoteTools([]string{"blocking"}, 1)
			registry.TickTTL()
		}},
		{name: "close", mutate: func(registry *ToolRegistry) {
			_ = registry.Close()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := NewToolRegistry()
			started := make(chan struct{})
			release := make(chan struct{})
			closed := []string{}
			live := &selectionCloseTool{
				mockRegistryTool: newMockTool("blocking", "blocking"),
				label:            "live", closed: &closed,
			}
			factory := mustFactoryForTool(t, live, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
				close(started)
				<-release
				return &selectionCloseTool{
					mockRegistryTool: newMockTool("blocking", "blocking"),
					label:            "child", closed: &closed,
				}, nil
			})
			if err := registry.RegisterHiddenFactoryBacked(live, factory); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := registry.InstantiateForOwnerSelection(
					factoryTestOwner(ToolOwnerScopeAgent, "change-"+test.name),
					[]string{"blocking"},
				)
				done <- err
			}()
			<-started
			test.mutate(registry)
			close(release)
			if err := <-done; err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatalf("source change error = %v", err)
			}
			if !reflect.DeepEqual(closed, []string{"child"}) {
				t.Fatalf("failed selection cleanup = %#v", closed)
			}
			_ = registry.Close()
		})
	}
}

func TestToolRegistryInstantiateForOwnerSelectionIgnoresUnselectedVisibilityChanges(t *testing.T) {
	source := NewToolRegistry()
	started := make(chan struct{})
	release := make(chan struct{})
	selectedLive := newMockTool("selected_blocking", "selected blocking")
	selectedFactory := mustFactoryForTool(t, selectedLive, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		close(started)
		<-release
		return newMockTool("selected_blocking", "selected blocking"), nil
	})
	if err := source.RegisterFactoryBacked(selectedLive, selectedFactory); err != nil {
		t.Fatal(err)
	}
	unselectedLive := newMockTool("unselected_hidden", "unselected hidden")
	unselectedFactory := mustFactoryForTool(t, unselectedLive, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return newMockTool("unselected_hidden", "unselected hidden"), nil
	})
	if err := source.RegisterHiddenFactoryBacked(unselectedLive, unselectedFactory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })

	type outcome struct {
		child *ToolRegistry
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		child, err := source.InstantiateForOwnerSelection(
			factoryTestOwner(ToolOwnerScopeAgent, "unselected-visibility"),
			[]string{"selected_blocking"},
		)
		done <- outcome{child: child, err: err}
	}()
	<-started
	source.PromoteTools([]string{"unselected_hidden"}, 1)
	source.TickTTL()
	close(release)
	result := <-done
	if result.err != nil || result.child == nil {
		t.Fatalf("unselected visibility change aborted selection: %#v, %v", result.child, result.err)
	}
	if err := result.child.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestToolRegistryInstantiateForOwnerSelectionRejectsSourcePointerReuse(t *testing.T) {
	registry := NewToolRegistry()
	foreign := newMockTool("foreign", "foreign")
	registry.Register(foreign)
	live := newMockTool("selector", "selector")
	factory := mustFactoryForTool(t, live, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return foreign, nil
	})
	if err := registry.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if child, err := registry.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "source-pointer"),
		[]string{"selector"},
	); err == nil || child != nil || !strings.Contains(err.Error(), "source instance") {
		t.Fatalf("source pointer reuse = %#v, %v", child, err)
	}
}
