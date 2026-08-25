package tools

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/media"
)

type factoryDependencyProbeTool struct {
	*mockRegistryTool
	mu         sync.RWMutex
	store      media.MediaStore
	closeCalls *atomic.Int64
}

func (tool *factoryDependencyProbeTool) SetMediaStore(store media.MediaStore) {
	tool.mu.Lock()
	tool.store = store
	tool.mu.Unlock()
}

func (tool *factoryDependencyProbeTool) mediaStore() media.MediaStore {
	tool.mu.RLock()
	defer tool.mu.RUnlock()
	return tool.store
}

func (tool *factoryDependencyProbeTool) Close() error {
	if tool.closeCalls != nil {
		tool.closeCalls.Add(1)
	}
	return nil
}

func TestNewToolFactoryFromPrototypeFreezesCompleteDescriptor(t *testing.T) {
	parameters := map[string]any{
		"type":     "object",
		"required": []string{"path"},
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
	}
	metadata := PromptMetadata{
		Layer:  "policy:test",
		Slot:   ToolPromptSlotMCP,
		Source: "dependency:test",
	}
	prototype := &factoryMetadataTool{
		name: "prototype_factory", desc: "prototype description",
		params: parameters, metadata: metadata,
	}
	factory, err := NewToolFactoryFromPrototype(
		prototype,
		ToolTraits{Risk: ToolRiskReadOnly, Parallel: ToolParallelSafe},
		func(ToolBuildContext) (Tool, error) {
			return &factoryMetadataTool{
				name: "prototype_factory", desc: "prototype description",
				params: map[string]any{"type": "object"}, metadata: metadata,
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	parameters["type"] = "mutated"
	parameters["required"].([]string)[0] = "mutated"
	prototype.mu.Lock()
	prototype.name = "mutated"
	prototype.desc = "mutated"
	prototype.metadata = PromptMetadata{}
	prototype.panicNow = true
	prototype.mu.Unlock()

	descriptor := factory.Descriptor()
	if descriptor.Name != "prototype_factory" || descriptor.Description != "prototype description" ||
		descriptor.PromptMetadata != metadata || descriptor.Parameters["type"] != "object" ||
		descriptor.Parameters["required"].([]string)[0] != "path" {
		t.Fatalf("frozen prototype descriptor = %#v", descriptor)
	}
	descriptor.Parameters["type"] = "caller mutation"
	if factory.Descriptor().Parameters["type"] != "object" {
		t.Fatal("factory descriptor retained a returned schema alias")
	}

	var nilPrototype *factoryMetadataTool
	if _, err := NewToolFactoryFromPrototype(
		nilPrototype,
		ToolTraits{},
		func(ToolBuildContext) (Tool, error) { return nil, nil },
	); err == nil || !strings.Contains(err.Error(), "tool is nil") {
		t.Fatalf("typed-nil prototype error = %v", err)
	}
	panicking := &factoryMetadataTool{panicNow: true}
	if _, err := NewToolFactoryFromPrototype(
		panicking,
		ToolTraits{},
		func(ToolBuildContext) (Tool, error) { return nil, nil },
	); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("panicking prototype error = %v", err)
	}
}

func TestToolRegistryFactoryDependencyIsResolveOnlyAndOwnerLocal(t *testing.T) {
	storeOne := media.NewFileMediaStore()
	storeTwo := media.NewFileMediaStore()
	source := NewToolRegistry()
	source.SetAllowlist([]string{"dependency_wrapper"})
	source.SetMediaStore(storeOne)

	var dependencyBuilds atomic.Int64
	var dependencyCloses atomic.Int64
	dependencyPrototype := &factoryDependencyProbeTool{
		mockRegistryTool: newMockTool("private_factory_dependency", "private factory dependency"),
	}
	dependencyFactory, factoryErr := NewToolFactoryFromPrototype(
		dependencyPrototype,
		ToolTraits{Risk: ToolRiskReadOnly, Parallel: ToolParallelSafe},
		func(ToolBuildContext) (Tool, error) {
			dependencyBuilds.Add(1)
			return &factoryDependencyProbeTool{
				mockRegistryTool: newMockTool(
					"private_factory_dependency",
					"private factory dependency",
				),
				closeCalls: &dependencyCloses,
			}, nil
		},
	)
	if factoryErr != nil {
		t.Fatal(factoryErr)
	}
	if source.AllowsRegistration("private_factory_dependency") {
		t.Fatal("test allowlist unexpectedly admitted the dependency publicly")
	}
	if err := source.RegisterFactoryDependency(dependencyFactory); err != nil {
		t.Fatal(err)
	}
	dependencyVersion := source.Version()
	if dependencyVersion == 0 || dependencyBuilds.Load() != 0 {
		t.Fatalf("dependency registration = version:%d builds:%d", dependencyVersion, dependencyBuilds.Load())
	}
	if err := source.RegisterFactoryDependency(dependencyFactory); err != nil ||
		source.Version() != dependencyVersion {
		t.Fatalf("idempotent dependency registration = version:%d error:%v", source.Version(), err)
	}

	if source.Count() != 0 || len(source.List()) != 0 ||
		source.HasRegistered("private_factory_dependency") ||
		len(source.InstantiationCapabilities()) != 0 || len(source.GetDefinitions()) != 0 ||
		len(source.ToProviderDefs()) != 0 || len(source.GetSummaries()) != 0 ||
		len(source.GetAll()) != 0 || len(source.SnapshotHiddenTools().Docs) != 0 {
		t.Fatal("factory dependency leaked into an outward registry projection")
	}
	if _, ok := source.GetRegistered("private_factory_dependency"); ok {
		t.Fatal("factory dependency leaked through GetRegistered")
	}
	if _, ok := source.Get("private_factory_dependency"); ok {
		t.Fatal("factory dependency became callable")
	}
	if _, ok := source.Traits("private_factory_dependency"); ok {
		t.Fatal("factory dependency traits became outwardly observable")
	}
	if result := source.Execute(context.Background(), "private_factory_dependency", nil); !result.IsError {
		t.Fatal("factory dependency executed directly")
	}
	if matches, searchErr := source.SearchRegex(
		"private factory dependency",
		5,
	); searchErr != nil || len(matches) != 0 {
		t.Fatalf("factory dependency leaked to search: %#v, %v", matches, searchErr)
	}
	if child, selectErr := source.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "direct-private-dependency"),
		[]string{"private_factory_dependency"},
	); selectErr == nil || child != nil {
		t.Fatalf("factory dependency became directly selectable: %#v, %v", child, selectErr)
	}

	wrapperLive := &selectionDependencyTool{
		mockRegistryTool: newMockTool("dependency_wrapper", "dependency wrapper"),
	}
	wrapperFactory := mustFactoryForTool(t, wrapperLive, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		dependency, resolveErr := ctx.Resolve("private_factory_dependency")
		if resolveErr != nil {
			return nil, resolveErr
		}
		return &selectionDependencyTool{
			mockRegistryTool: newMockTool("dependency_wrapper", "dependency wrapper"),
			dependency:       dependency,
		}, nil
	})
	if err := source.RegisterFactoryBacked(wrapperLive, wrapperFactory); err != nil {
		t.Fatal(err)
	}
	child, selectionErr := source.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "resolved-private-dependency"),
		[]string{"dependency_wrapper"},
	)
	if selectionErr != nil {
		t.Fatal(selectionErr)
	}
	wrapperRaw, _ := child.GetRegistered("dependency_wrapper")
	resolved := wrapperRaw.(*selectionDependencyTool).dependency.(*factoryDependencyProbeTool)
	if dependencyBuilds.Load() != 1 || resolved == dependencyPrototype ||
		resolved.mediaStore() != storeOne || child.privateConstruction["private_factory_dependency"] == nil {
		t.Fatalf(
			"resolved dependency = %#v builds:%d store:%#v private:%#v",
			resolved,
			dependencyBuilds.Load(),
			resolved.mediaStore(),
			child.privateConstruction["private_factory_dependency"],
		)
	}
	if child.HasRegistered("private_factory_dependency") ||
		!reflect.DeepEqual(child.List(), []string{"dependency_wrapper"}) ||
		len(child.InstantiationCapabilities()) != 1 {
		t.Fatal("resolved dependency escaped its private owner product map")
	}
	child.SetMediaStore(storeTwo)
	if resolved.mediaStore() != storeTwo {
		t.Fatal("private owner product did not receive the owner's media generation")
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if dependencyCloses.Load() != 1 {
		t.Fatalf("private dependency close calls = %d", dependencyCloses.Load())
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestToolRegistryFactoryDependencyPublicPromotion(t *testing.T) {
	var builds atomic.Int64
	prototype := newMockTool("promoted_dependency", "promoted dependency")
	factory := mustFactoryForTool(
		t,
		prototype,
		ToolTraits{Risk: ToolRiskReadOnly},
		func(ToolBuildContext) (Tool, error) {
			builds.Add(1)
			return newMockTool("promoted_dependency", "promoted dependency"), nil
		},
	)
	registry := NewToolRegistry()
	registry.SetAllowlist([]string{"other"})
	if err := registry.RegisterFactoryDependency(factory); err != nil {
		t.Fatal(err)
	}
	privateVersion := registry.Version()
	live := newMockTool("promoted_dependency", "promoted dependency")
	if err := registry.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatal(err)
	}
	if registry.Version() != privateVersion || registry.constructionCatalog["promoted_dependency"] == nil ||
		registry.HasRegistered("promoted_dependency") {
		t.Fatal("allowlist-skipped publication changed the private dependency")
	}

	registry.SetAllowlist([]string{"promoted_dependency"})
	if err := registry.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatal(err)
	}
	if registry.constructionCatalog["promoted_dependency"] != nil ||
		!registry.HasRegistered("promoted_dependency") || registry.Version() != privateVersion+1 ||
		builds.Load() != 0 {
		t.Fatalf(
			"promotion = catalog:%#v registered:%t version:%d builds:%d",
			registry.constructionCatalog["promoted_dependency"],
			registry.HasRegistered("promoted_dependency"),
			registry.Version(),
			builds.Load(),
		)
	}
	if err := registry.RegisterFactoryDependency(factory); err != nil || registry.Version() != privateVersion+1 {
		t.Fatalf("public winner dependency replay = version:%d error:%v", registry.Version(), err)
	}
	if registered, ok := registry.GetRegistered("promoted_dependency"); !ok || registered != live {
		t.Fatal("promotion replaced the supplied compatibility pointer")
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	leaseProbe := NewToolRegistry()
	if err := leaseProbe.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatalf("promoted compatibility lease survived close: %v", err)
	}
	if err := leaseProbe.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestToolRegistryFactoryDependencyRejectsAmbiguousCollisionsTransactionally(t *testing.T) {
	prototype := newMockTool("collision_dependency", "collision dependency")
	factory := mustFactoryForTool(
		t,
		prototype,
		ToolTraits{Risk: ToolRiskReadOnly},
		func(ToolBuildContext) (Tool, error) {
			return newMockTool("collision_dependency", "collision dependency"), nil
		},
	)
	registry := NewToolRegistry()
	if err := registry.RegisterFactoryDependency(factory); err != nil {
		t.Fatal(err)
	}
	original := registry.constructionCatalog["collision_dependency"]
	version := registry.Version()

	cases := []struct {
		name    string
		factory ToolFactory
		want    string
	}{
		{
			name: "descriptor",
			factory: mustFactoryForTool(
				t,
				newMockTool("collision_dependency", "different description"),
				ToolTraits{Risk: ToolRiskReadOnly},
				func(ToolBuildContext) (Tool, error) {
					return newMockTool("collision_dependency", "different description"), nil
				},
			),
			want: "descriptor",
		},
		{
			name: "traits",
			factory: mustFactoryForTool(
				t,
				prototype,
				ToolTraits{Risk: ToolRiskMutation},
				func(ToolBuildContext) (Tool, error) {
					return newMockTool("collision_dependency", "collision dependency"), nil
				},
			),
			want: "traits",
		},
		{
			name: "identity",
			factory: mustFactoryForTool(
				t,
				prototype,
				ToolTraits{Risk: ToolRiskReadOnly},
				func(ToolBuildContext) (Tool, error) {
					return newMockTool("collision_dependency", "collision dependency"), nil
				},
			),
			want: "different factory",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := registry.RegisterFactoryDependency(test.factory); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("collision error = %v", err)
			}
			if registry.constructionCatalog["collision_dependency"] != original ||
				registry.Version() != version || registry.Count() != 0 {
				t.Fatal("failed dependency collision mutated the registry")
			}
		})
	}

	registry.Register(newMockTool("collision_dependency", "legacy collision"))
	if registry.Count() != 0 || registry.constructionCatalog["collision_dependency"] != original ||
		registry.Version() != version {
		t.Fatal("legacy registration overwrote a private factory dependency")
	}
	mismatchedLive := newMockTool("collision_dependency", "different description")
	if err := registry.RegisterFactoryBacked(mismatchedLive, factory); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("live descriptor mismatch error = %v", err)
	}
	if registry.constructionCatalog["collision_dependency"] != original ||
		registry.Version() != version || registry.Count() != 0 {
		t.Fatal("failed public promotion mutated the dependency")
	}

	registry.constructionCatalog["collision_dependency"] = &ToolEntry{
		Tool:       newMockTool("collision_dependency", "collision dependency"),
		descriptor: original.descriptor,
		traits:     original.traits,
		factory:    original.factory,
	}
	if err := registry.RegisterFactoryDependency(factory); err == nil ||
		!strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous catalog error = %v", err)
	}
}

func TestToolRegistryFactoryDependencyAdmissionFailures(t *testing.T) {
	valid := mustFactoryForTool(t, newMockTool("dependency_admission", "dependency admission"), ToolTraits{},
		func(ToolBuildContext) (Tool, error) {
			return newMockTool("dependency_admission", "dependency admission"), nil
		})
	var nilRegistry *ToolRegistry
	if err := nilRegistry.RegisterFactoryDependency(valid); err == nil {
		t.Fatal("nil registry accepted a dependency")
	}
	var nilFactory *coverageToolFactory
	if err := NewToolRegistry().RegisterFactoryDependency(nilFactory); err == nil {
		t.Fatal("typed-nil factory was accepted")
	}
	for _, factory := range []ToolFactory{
		&coverageToolFactory{descriptorFn: func() ToolDescriptor { panic("metadata panic") }},
		&coverageToolFactory{
			descriptor: coverageFactoryDescriptor("shared_dependency"),
			traits: ToolTraits{
				Parallel: ToolParallelSafe, Sharing: ToolSharingImmutableShared,
			},
			newFn: func(ToolBuildContext) (Tool, error) { return nil, nil },
		},
	} {
		if err := NewToolRegistry().RegisterFactoryDependency(factory); err == nil {
			t.Fatal("invalid factory metadata was accepted")
		}
	}
	closed := NewToolRegistry()
	closed.closed = true
	if err := closed.RegisterFactoryDependency(valid); err == nil {
		t.Fatal("closed registry accepted a dependency")
	}

	public := NewToolRegistry()
	public.Register(newMockTool("dependency_admission", "legacy public"))
	if err := public.RegisterFactoryDependency(valid); err == nil ||
		!strings.Contains(err.Error(), "public") {
		t.Fatalf("public collision error = %v", err)
	}

	dependencyOnly := NewToolRegistry()
	if err := dependencyOnly.RegisterFactoryDependency(valid); err != nil {
		t.Fatal(err)
	}
	if err := dependencyOnly.Close(); err != nil {
		t.Fatal(err)
	}
	if len(dependencyOnly.constructionCatalog) != 0 ||
		dependencyOnly.RegisterFactoryDependency(valid) == nil {
		t.Fatal("dependency-only source retained legacy no-op close behavior")
	}
}

func TestToolRegistryFactoryDependencyPromotionFencesCatalogMutation(t *testing.T) {
	name := "dependency_commit_fence"
	prototype := &coverageHookTool{name: name}
	factory := mustFactoryForTool(t, prototype, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return &coverageHookTool{name: name}, nil
	})
	registry := NewToolRegistry()
	if err := registry.RegisterFactoryDependency(factory); err != nil {
		t.Fatal(err)
	}
	original := registry.constructionCatalog[name]
	live := &coverageHookTool{name: name}
	live.hook = func() {
		registry.mu.Lock()
		replacement := *original
		registry.constructionCatalog[name] = &replacement
		registry.version.Add(1)
		registry.mu.Unlock()
	}
	if err := registry.RegisterFactoryBacked(live, factory); err == nil ||
		!strings.Contains(err.Error(), "changed") {
		t.Fatalf("catalog mutation promotion error = %v", err)
	}
	if registry.HasRegistered(name) || registry.constructionCatalog[name] == original {
		t.Fatal("failed promotion published a tool or rolled back a concurrent catalog mutation")
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	leaseProbe := NewToolRegistry()
	if err := leaseProbe.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatalf("failed promotion leaked the compatibility lease: %v", err)
	}
	if err := leaseProbe.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestToolRegistryFactoryDependencySourceMutationAbortsAndCleansSelection(t *testing.T) {
	source := NewToolRegistry()
	var dependencyCloses atomic.Int64
	dependency := &factoryDependencyProbeTool{
		mockRegistryTool: newMockTool("fenced_dependency", "fenced dependency"),
	}
	dependencyFactory := mustFactoryForTool(t, dependency, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return &factoryDependencyProbeTool{
			mockRegistryTool: newMockTool("fenced_dependency", "fenced dependency"),
			closeCalls:       &dependencyCloses,
		}, nil
	})
	if err := source.RegisterFactoryDependency(dependencyFactory); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	wrapperLive := newMockTool("fenced_wrapper", "fenced wrapper")
	wrapperFactory := mustFactoryForTool(t, wrapperLive, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		resolved, resolveErr := ctx.Resolve("fenced_dependency")
		if resolveErr != nil {
			return nil, resolveErr
		}
		close(started)
		<-release
		return &selectionDependencyTool{
			mockRegistryTool: newMockTool("fenced_wrapper", "fenced wrapper"),
			dependency:       resolved,
		}, nil
	})
	if err := source.RegisterFactoryBacked(wrapperLive, wrapperFactory); err != nil {
		t.Fatal(err)
	}
	deferred := make(chan error, 1)
	go func() {
		_, err := source.InstantiateForOwnerSelection(
			factoryTestOwner(ToolOwnerScopeAgent, "dependency-source-fence"),
			[]string{"fenced_wrapper"},
		)
		deferred <- err
	}()
	<-started
	lateFactory := mustFactoryForTool(t, newMockTool("late_dependency", "late dependency"), ToolTraits{},
		func(ToolBuildContext) (Tool, error) {
			return newMockTool("late_dependency", "late dependency"), nil
		})
	if err := source.RegisterFactoryDependency(lateFactory); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-deferred; err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("source mutation selection error = %v", err)
	}
	if dependencyCloses.Load() != 1 {
		t.Fatalf("aborted private dependency close calls = %d", dependencyCloses.Load())
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestToolRegistryFactoryDependencySourceCloseAbortsAndCleansSelection(t *testing.T) {
	source := NewToolRegistry()
	var dependencyCloses atomic.Int64
	dependency := newMockTool("closed_source_dependency", "closed source dependency")
	dependencyFactory := mustFactoryForTool(t, dependency, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return &factoryDependencyProbeTool{
			mockRegistryTool: newMockTool("closed_source_dependency", "closed source dependency"),
			closeCalls:       &dependencyCloses,
		}, nil
	})
	if err := source.RegisterFactoryDependency(dependencyFactory); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	wrapperLive := newMockTool("closed_source_wrapper", "closed source wrapper")
	wrapperFactory := mustFactoryForTool(t, wrapperLive, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		resolved, resolveErr := ctx.Resolve("closed_source_dependency")
		if resolveErr != nil {
			return nil, resolveErr
		}
		close(started)
		<-release
		return &selectionDependencyTool{
			mockRegistryTool: newMockTool("closed_source_wrapper", "closed source wrapper"),
			dependency:       resolved,
		}, nil
	})
	if err := source.RegisterFactoryBacked(wrapperLive, wrapperFactory); err != nil {
		t.Fatal(err)
	}
	deferred := make(chan error, 1)
	go func() {
		_, err := source.InstantiateForOwnerSelection(
			factoryTestOwner(ToolOwnerScopeAgent, "dependency-close-fence"),
			[]string{"closed_source_wrapper"},
		)
		deferred <- err
	}()
	<-started
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-deferred; err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("source close selection error = %v", err)
	}
	if dependencyCloses.Load() != 1 {
		t.Fatalf("closed-source dependency cleanup calls = %d", dependencyCloses.Load())
	}
}

func TestToolRegistryFactoryDependencyProductsKeepExclusiveOwnerLeases(t *testing.T) {
	source := NewToolRegistry()
	var closes atomic.Int64
	sharedProduct := &factoryDependencyProbeTool{
		mockRegistryTool: newMockTool("leased_factory_dependency", "leased factory dependency"),
		closeCalls:       &closes,
	}
	dependencyFactory := mustFactoryForTool(
		t,
		newMockTool("leased_factory_dependency", "leased factory dependency"),
		ToolTraits{},
		func(ToolBuildContext) (Tool, error) { return sharedProduct, nil },
	)
	if err := source.RegisterFactoryDependency(dependencyFactory); err != nil {
		t.Fatal(err)
	}
	wrapperLive := newMockTool("leased_dependency_wrapper", "leased dependency wrapper")
	wrapperFactory := mustFactoryForTool(t, wrapperLive, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		dependency, resolveErr := ctx.Resolve("leased_factory_dependency")
		if resolveErr != nil {
			return nil, resolveErr
		}
		return &selectionDependencyTool{
			mockRegistryTool: newMockTool("leased_dependency_wrapper", "leased dependency wrapper"),
			dependency:       dependency,
		}, nil
	})
	if err := source.RegisterFactoryBacked(wrapperLive, wrapperFactory); err != nil {
		t.Fatal(err)
	}
	first, err := source.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "leased-dependency-first"),
		[]string{"leased_dependency_wrapper"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := source.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "leased-dependency-second"),
		[]string{"leased_dependency_wrapper"},
	); err == nil || second != nil || !strings.Contains(err.Error(), "another owner") {
		t.Fatalf("concurrent owner lease reuse = %#v, %v", second, err)
	}
	if closes.Load() != 0 {
		t.Fatal("failed second construction closed the first owner's dependency")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatalf("first owner dependency close calls = %d", closes.Load())
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedToolRegistryPromotesExactFactoryDependency(t *testing.T) {
	registry, err := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "dependency-promotion"))
	if err != nil {
		t.Fatal(err)
	}
	var builds atomic.Int64
	prototype := newMockTool("owned_promoted_dependency", "owned promoted dependency")
	factory := mustFactoryForTool(t, prototype, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		builds.Add(1)
		return newMockTool("owned_promoted_dependency", "owned promoted dependency"), nil
	})
	if err := registry.RegisterFactoryDependency(factory); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterFactory(factory); err != nil {
		t.Fatal(err)
	}
	if builds.Load() != 1 || registry.Count() != 1 ||
		registry.constructionCatalog["owned_promoted_dependency"] != nil {
		t.Fatal("owned factory registration did not promote its dormant dependency")
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestToolRegistryFactoryDependencyCoverageEdges(t *testing.T) {
	name := "dependency_coverage_edge"
	prototype := newMockTool(name, "dependency coverage edge")
	factory := mustFactoryForTool(t, prototype, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return newMockTool(name, "dependency coverage edge"), nil
	})
	descriptor, traits, metadataErr := snapshotFactoryMetadata(factory)
	if metadataErr != nil {
		t.Fatal(metadataErr)
	}

	t.Run("zero value catalog", func(t *testing.T) {
		registry := &ToolRegistry{
			tools:               make(map[string]*ToolEntry),
			privateConstruction: make(map[string]*ToolEntry),
		}
		if err := registry.RegisterFactoryDependency(factory); err != nil ||
			registry.constructionCatalog[name] == nil {
			t.Fatalf("zero-value catalog registration = %#v, %v", registry.constructionCatalog, err)
		}
		if err := registry.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("owner-built winner", func(t *testing.T) {
		registry := NewToolRegistry()
		frozen := cloneToolDescriptor(descriptor)
		registry.privateConstruction[name] = &ToolEntry{
			Tool:       newMockTool(name, "dependency coverage edge"),
			descriptor: &frozen, traits: traits, factory: factory,
		}
		if err := registry.RegisterFactoryDependency(factory); err != nil || registry.Version() != 0 {
			t.Fatalf("matching owner-built replay = version:%d error:%v", registry.Version(), err)
		}
		different := mustFactoryForTool(t, prototype, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
			return newMockTool(name, "dependency coverage edge"), nil
		})
		if err := registry.RegisterFactoryDependency(different); err == nil ||
			!strings.Contains(err.Error(), "owner-built") {
			t.Fatalf("owner-built collision error = %v", err)
		}
		if err := registry.RegisterFactoryBacked(
			newMockTool(name, "dependency coverage edge"),
			factory,
		); err == nil || !strings.Contains(err.Error(), "owner-built") {
			t.Fatalf("owner-built promotion error = %v", err)
		}
	})

	t.Run("public factory identity mismatch", func(t *testing.T) {
		registry := NewToolRegistry()
		if err := registry.RegisterFactoryDependency(factory); err != nil {
			t.Fatal(err)
		}
		different := mustFactoryForTool(t, prototype, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
			return newMockTool(name, "dependency coverage edge"), nil
		})
		if err := registry.RegisterFactoryBacked(
			newMockTool(name, "dependency coverage edge"),
			different,
		); err == nil || !strings.Contains(err.Error(), "different factory") {
			t.Fatalf("compatibility identity mismatch = %v", err)
		}
		owned, err := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "identity-mismatch"))
		if err != nil {
			t.Fatal(err)
		}
		if err := owned.RegisterFactoryDependency(factory); err != nil {
			t.Fatal(err)
		}
		if err := owned.RegisterFactory(different); err == nil ||
			!strings.Contains(err.Error(), "different factory") {
			t.Fatalf("owned identity mismatch = %v", err)
		}
		_ = registry.Close()
		_ = owned.Close()
	})

	t.Run("owner-built commit fence", func(t *testing.T) {
		registry := NewToolRegistry()
		hookPrototype := &coverageHookTool{name: name}
		hookFactory := mustFactoryForTool(t, hookPrototype, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
			return &coverageHookTool{name: name}, nil
		})
		if err := registry.RegisterFactoryDependency(hookFactory); err != nil {
			t.Fatal(err)
		}
		live := &coverageHookTool{name: name}
		live.hook = func() {
			registry.mu.Lock()
			registry.privateConstruction[name] = &ToolEntry{}
			registry.version.Add(1)
			registry.mu.Unlock()
		}
		if err := registry.RegisterFactoryBacked(live, hookFactory); err == nil ||
			!strings.Contains(err.Error(), "owner-built dependency changed") {
			t.Fatalf("owner-built commit fence error = %v", err)
		}
		if registry.HasRegistered(name) {
			t.Fatal("owner-built commit conflict published the compatibility tool")
		}
	})

	t.Run("owned catalog commit fence", func(t *testing.T) {
		registry, err := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "catalog-commit"))
		if err != nil {
			t.Fatal(err)
		}
		var catalog *ToolEntry
		mutatingFactory := mustFactoryForTool(t, prototype, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
			registry.mu.Lock()
			replacement := *catalog
			registry.constructionCatalog[name] = &replacement
			registry.version.Add(1)
			registry.mu.Unlock()
			return newMockTool(name, "dependency coverage edge"), nil
		})
		if err := registry.RegisterFactoryDependency(mutatingFactory); err != nil {
			t.Fatal(err)
		}
		catalog = registry.constructionCatalog[name]
		if err := registry.RegisterFactory(mutatingFactory); err == nil ||
			!strings.Contains(err.Error(), "changed") {
			t.Fatalf("owned catalog commit fence error = %v", err)
		}
		if registry.HasRegistered(name) {
			t.Fatal("owned catalog commit conflict published the tool")
		}
		_ = registry.Close()
	})

	t.Run("immutable collision releases reservation", func(t *testing.T) {
		registry, registryErr := NewOwnedToolRegistry(
			factoryTestOwner(ToolOwnerScopeRegistry, "immutable-collision"),
		)
		if registryErr != nil {
			t.Fatal(registryErr)
		}
		if err := registry.RegisterFactoryDependency(factory); err != nil {
			t.Fatal(err)
		}
		shared := newMockTool(name, "dependency coverage edge")
		if err := registry.RegisterImmutableShared(shared, ToolTraits{
			Parallel: ToolParallelSafe,
		}); err == nil || !strings.Contains(err.Error(), "private factory dependency") {
			t.Fatalf("immutable collision error = %v", err)
		}
		identity, identityErr := toolInstanceIdentity(shared)
		if identityErr != nil {
			t.Fatal(identityErr)
		}
		if err := globalOwnedToolInstances.reserve(identity, shared); err != nil {
			t.Fatalf("failed immutable collision retained a reservation: %v", err)
		}
		globalOwnedToolInstances.release(identity)
		_ = registry.Close()
	})
}
