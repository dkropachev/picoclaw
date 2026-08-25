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
	"time"

	"github.com/sipeed/picoclaw/pkg/media"
)

func factoryTestOwner(scope ToolOwnerScope, suffix string) ToolOwner {
	owner := ToolOwner{Scope: scope}
	switch scope {
	case ToolOwnerScopeAgent:
		owner.AgentID = "agent-" + suffix
	case ToolOwnerScopeTurn:
		owner.AgentID = "agent-" + suffix
		owner.SessionKey = "session-" + suffix
		owner.TurnID = "turn-" + suffix
	}
	return owner
}

func mustFactoryForTool(
	t *testing.T,
	prototype Tool,
	traits ToolTraits,
	build ToolBuildFunc,
) ToolFactory {
	t.Helper()
	descriptor, err := toolDescriptorFromTool(prototype)
	if err != nil {
		t.Fatal(err)
	}
	factory, err := NewToolFactory(descriptor, traits, build)
	if err != nil {
		t.Fatal(err)
	}
	return factory
}

type factoryDependencyTool struct {
	*mockRegistryTool
	dependency Tool
}

type factoryMetadataTool struct {
	mu       sync.RWMutex
	name     string
	desc     string
	params   map[string]any
	metadata PromptMetadata
	panicNow bool
	store    media.MediaStore
	setCalls int
}

func (tool *factoryMetadataTool) Name() string {
	tool.mu.RLock()
	defer tool.mu.RUnlock()
	if tool.panicNow {
		panic("live name called")
	}
	return tool.name
}

func (tool *factoryMetadataTool) Description() string {
	tool.mu.RLock()
	defer tool.mu.RUnlock()
	if tool.panicNow {
		panic("live description called")
	}
	return tool.desc
}

func (tool *factoryMetadataTool) Parameters() map[string]any {
	tool.mu.RLock()
	defer tool.mu.RUnlock()
	if tool.panicNow {
		panic("live parameters called")
	}
	return tool.params
}

func (tool *factoryMetadataTool) PromptMetadata() PromptMetadata {
	tool.mu.RLock()
	defer tool.mu.RUnlock()
	if tool.panicNow {
		panic("live prompt metadata called")
	}
	return tool.metadata
}

func (tool *factoryMetadataTool) SetMediaStore(store media.MediaStore) {
	tool.store = store
	tool.setCalls++
}

func (tool *factoryMetadataTool) Execute(context.Context, map[string]any) *ToolResult {
	return SilentResult("frozen execution")
}

type factoryCloseTool struct {
	*mockRegistryTool
	label      string
	closed     *[]string
	closePanic bool
	closeErr   error
}

type factoryBlockingCloseTool struct {
	*mockRegistryTool
	closeCalls atomic.Int64
	entered    chan struct{}
	release    chan struct{}
	enterOnce  sync.Once
}

func (tool *factoryBlockingCloseTool) Close() error {
	tool.closeCalls.Add(1)
	tool.enterOnce.Do(func() { close(tool.entered) })
	<-tool.release
	return nil
}

func (tool *factoryCloseTool) Close() error {
	*tool.closed = append(*tool.closed, tool.label)
	if tool.closePanic {
		panic("close panic")
	}
	return tool.closeErr
}

type factoryService struct{ id int }

type factoryServiceTool struct {
	*mockRegistryTool
	service *factoryService
}

type factoryValueTool struct{}

func (factoryValueTool) Name() string               { return "value_tool" }
func (factoryValueTool) Description() string        { return "value tool" }
func (factoryValueTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (factoryValueTool) Execute(context.Context, map[string]any) *ToolResult {
	return SilentResult("value")
}

type factoryMetadataProbe struct{ descriptorCalls atomic.Int64 }

func (probe *factoryMetadataProbe) Descriptor() ToolDescriptor {
	probe.descriptorCalls.Add(1)
	panic("descriptor must not be called")
}

func (*factoryMetadataProbe) Traits() ToolTraits { panic("traits must not be called") }
func (*factoryMetadataProbe) New(ToolBuildContext) (Tool, error) {
	panic("New must not be called")
}

func TestToolOwnerTraitsAndOwnedRegistryValidation(t *testing.T) {
	tests := []struct {
		name    string
		owner   ToolOwner
		wantErr bool
	}{
		{name: "registry", owner: factoryTestOwner(ToolOwnerScopeRegistry, "one")},
		{name: "agent", owner: factoryTestOwner(ToolOwnerScopeAgent, "one")},
		{name: "turn", owner: factoryTestOwner(ToolOwnerScopeTurn, "one")},
		{name: "unknown scope", owner: ToolOwner{}, wantErr: true},
		{name: "agent missing identity", owner: ToolOwner{Scope: ToolOwnerScopeAgent}, wantErr: true},
		{name: "turn missing identity", owner: ToolOwner{Scope: ToolOwnerScopeTurn}, wantErr: true},
		{name: "non exact", owner: ToolOwner{Scope: ToolOwnerScopeAgent, AgentID: " agent"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry, err := NewOwnedToolRegistry(test.owner)
			if (err != nil) != test.wantErr {
				t.Fatalf("NewOwnedToolRegistry() error = %v", err)
			}
			if test.wantErr {
				return
			}
			got, ok := registry.Owner()
			if !ok || got != test.owner {
				t.Fatalf("Owner() = %#v, %t", got, ok)
			}
		})
	}

	legacy := NewToolRegistry()
	if _, ok := legacy.Owner(); ok {
		t.Fatal("legacy registry unexpectedly has owner")
	}
	legacy.Register(newMockTool("legacy", "legacy"))
	traits, ok := legacy.Traits("legacy")
	if !ok || traits.Risk != ToolRiskUnknown || traits.Parallel != ToolParallelSerialized ||
		traits.Idempotency != ToolIdempotencyUnknown || traits.Sharing != ToolSharingPerOwner {
		t.Fatalf("legacy traits = %#v", traits)
	}
	ownedLegacy, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "owned-legacy"))
	ownedLegacy.Register(newMockTool("legacy", "legacy"))
	if _, err := ownedLegacy.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "strict")); err == nil ||
		!strings.Contains(err.Error(), "legacy tool") {
		t.Fatalf("strict legacy instantiation error = %v", err)
	}
}

func TestToolRegistryFactoryMutableAndImmutableOwnerIsolation(t *testing.T) {
	source, err := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "source"))
	if err != nil {
		t.Fatal(err)
	}
	if registerErr := source.RegisterFactory(NewUpdatePlanToolFactory()); registerErr != nil {
		t.Fatal(registerErr)
	}
	shared := newMockTool("shared_read", "immutable read")
	if registerErr := source.RegisterImmutableShared(shared, ToolTraits{
		Risk: ToolRiskReadOnly, Parallel: ToolParallelSafe,
		Idempotency: ToolIdempotencyIdempotent,
	}); registerErr != nil {
		t.Fatal(registerErr)
	}

	left, err := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeTurn, "left"))
	if err != nil {
		t.Fatal(err)
	}
	right, err := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeTurn, "right"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	defer func() { _ = left.Close() }()
	defer func() { _ = right.Close() }()
	sourcePlan, _ := source.GetRegistered("update_plan")
	leftPlanRaw, _ := left.GetRegistered("update_plan")
	rightPlanRaw, _ := right.GetRegistered("update_plan")
	if samePointerIdentity(sourcePlan, leftPlanRaw) || samePointerIdentity(leftPlanRaw, rightPlanRaw) {
		t.Fatal("mutable update_plan instance was shared across owners")
	}
	leftPlan := leftPlanRaw.(*UpdatePlanTool)
	rightPlan := rightPlanRaw.(*UpdatePlanTool)
	leftPlan.Execute(context.Background(), map[string]any{"plan": []any{
		map[string]any{"step": "left", "status": "in_progress"},
	}})
	rightPlan.Execute(context.Background(), map[string]any{"plan": []any{
		map[string]any{"step": "right", "status": "completed"},
	}})
	if reflect.DeepEqual(leftPlan.steps, rightPlan.steps) || leftPlan.steps[0].Step != "left" ||
		rightPlan.steps[0].Step != "right" {
		t.Fatalf("owner plans leaked: left=%#v right=%#v", leftPlan.steps, rightPlan.steps)
	}
	leftShared, _ := left.GetRegistered("shared_read")
	rightShared, _ := right.GetRegistered("shared_read")
	if leftShared != shared || rightShared != shared {
		t.Fatal("explicit immutable tool was not shared")
	}
	if ToolSchemaHash(source.ToProviderDefs()) != ToolSchemaHash(left.ToProviderDefs()) ||
		ToolSchemaHash(left.ToProviderDefs()) != ToolSchemaHash(right.ToProviderDefs()) {
		t.Fatal("owner instantiation changed provider schema identity")
	}
	compatClone := source.Clone()
	if compatClone.Count() != 0 {
		t.Fatal("owned shallow Clone did not fail closed")
	}
	if _, owned := compatClone.Owner(); owned {
		t.Fatal("owned shallow Clone duplicated lifecycle ownership")
	}
}

func TestToolRegistryFactoryAllowlistCollisionAndSharingSafety(t *testing.T) {
	prototype := newMockTool("factory", "factory")
	var calls atomic.Int64
	factory := mustFactoryForTool(t, prototype, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		calls.Add(1)
		return newMockTool("factory", "factory"), nil
	})

	unowned := NewToolRegistry()
	probe := &factoryMetadataProbe{}
	if err := unowned.RegisterFactory(probe); err == nil || probe.descriptorCalls.Load() != 0 {
		t.Fatal("unowned registry accepted factory")
	}
	blocked, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "blocked"))
	blocked.SetAllowlist([]string{})
	if err := blocked.RegisterFactory(factory); err != nil || calls.Load() != 0 || blocked.Count() != 0 {
		t.Fatalf("blocked factory = error:%v calls:%d count:%d", err, calls.Load(), blocked.Count())
	}
	linearized, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "allowlist-linearized"))
	linearized.SetAllowlist([]string{"factory"})
	started := make(chan struct{})
	release := make(chan struct{})
	linearFactory := mustFactoryForTool(t, prototype, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		close(started)
		<-release
		return newMockTool("factory", "factory"), nil
	})
	linearDone := make(chan error, 1)
	go func() { linearDone <- linearized.RegisterFactory(linearFactory) }()
	<-started
	linearized.SetAllowlist([]string{})
	close(release)
	if err := <-linearDone; err != nil || !linearized.HasRegistered("factory") {
		t.Fatalf(
			"post-admission allowlist changed outcome = error:%v registered:%t",
			err,
			linearized.HasRegistered("factory"),
		)
	}

	owned, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "owned"))
	owned.Register(newMockTool("factory", "legacy occupant"))
	if err := owned.RegisterFactory(factory); err == nil || calls.Load() != 0 {
		t.Fatalf("collision invoked/replaced factory: error=%v calls=%d", err, calls.Load())
	}
	if got, _ := owned.GetRegistered("factory"); got.Description() != "legacy occupant" {
		t.Fatal("strict collision replaced occupant")
	}

	unsafe, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "unsafe"))
	if err := unsafe.RegisterImmutableShared(newMockTool("unsafe", "unsafe"), ToolTraits{}); err == nil {
		t.Fatal("serialized immutable sharing was accepted")
	}
	mediaAware := &mockMediaStoreAwareTool{mockRegistryTool: *newMockTool("media", "media")}
	if err := unsafe.RegisterImmutableShared(mediaAware, ToolTraits{Parallel: ToolParallelSafe}); err == nil {
		t.Fatal("media-aware immutable sharing was accepted")
	}
	hiddenShared := newMockTool("hidden_shared", "hidden shared")
	if err := unsafe.RegisterHiddenImmutableShared(hiddenShared, ToolTraits{
		Risk: ToolRiskReadOnly, Parallel: ToolParallelSafe,
		Idempotency: ToolIdempotencyIdempotent,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := unsafe.Get("hidden_shared"); ok {
		t.Fatal("hidden immutable tool was visible before promotion")
	}
	unsafe.PromoteTools([]string{"hidden_shared"}, 1)
	if got, ok := unsafe.Get("hidden_shared"); !ok || got != hiddenShared {
		t.Fatal("hidden immutable tool did not preserve shared pointer")
	}
	closed, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "closed-admission"))
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	closedProbe := &factoryMetadataProbe{}
	if err := closed.RegisterFactory(closedProbe); err == nil || closedProbe.descriptorCalls.Load() != 0 {
		t.Fatalf("closed factory admission = error:%v calls:%d", err, closedProbe.descriptorCalls.Load())
	}
	panicTool := &factoryMetadataTool{panicNow: true}
	if err := closed.RegisterImmutableShared(panicTool, ToolTraits{Parallel: ToolParallelSafe}); err == nil {
		t.Fatal("closed immutable admission succeeded")
	}
}

func TestToolRegistryFactoryFrozenMetadataAndDetachedSchemas(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "string", "enum": []string{"a", "b"}},
		},
		"required": []string{"value"},
	}
	prototype := &factoryMetadataTool{
		name: "frozen", desc: "frozen metadata", params: params,
		metadata: PromptMetadata{Slot: ToolPromptSlotMCP, Source: "factory:frozen"},
	}
	factory := mustFactoryForTool(t, prototype, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		cloned, err := cloneToolSchemaMap(params)
		if err != nil {
			return nil, err
		}
		return &factoryMetadataTool{
			name: "frozen", desc: "frozen metadata", params: cloned,
			metadata: PromptMetadata{Slot: ToolPromptSlotMCP, Source: "factory:frozen"},
		}, nil
	})
	registry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "frozen"))
	if err := registry.RegisterFactory(factory); err != nil {
		t.Fatal(err)
	}
	liveRaw, _ := registry.GetRegistered("frozen")
	live := liveRaw.(*factoryMetadataTool)
	live.mu.Lock()
	live.panicNow = true
	live.mu.Unlock()

	defs := registry.ToProviderDefs()
	if len(defs) != 1 || defs[0].Function.Name != "frozen" ||
		defs[0].PromptLayer != ToolPromptLayerCapability || defs[0].PromptSlot != ToolPromptSlotMCP ||
		defs[0].PromptSource != "factory:frozen" {
		t.Fatalf("frozen provider definition = %#v", defs)
	}
	properties := defs[0].Function.Parameters["properties"].(map[string]any)
	properties["value"].(map[string]any)["type"] = "integer"
	second := registry.ToProviderDefs()[0]
	secondType := second.Function.Parameters["properties"].(map[string]any)["value"].(map[string]any)["type"]
	if secondType != "string" {
		t.Fatalf("provider schema mutation leaked: %v", secondType)
	}
	if result := registry.Execute(context.Background(), "frozen", map[string]any{"value": "a"}); result.IsError {
		t.Fatalf("frozen validation failed: %#v", result)
	}
	if summaries := registry.GetSummaries(); len(summaries) != 1 || !strings.Contains(summaries[0], "frozen metadata") {
		t.Fatalf("frozen summaries = %#v", summaries)
	}
	snapshot, ok := registry.GetCoreToolSnapshot("frozen")
	if !ok || snapshot.Descriptor == nil || snapshot.ParameterSchema()["type"] != "object" {
		t.Fatalf("core frozen snapshot = %#v, %t", snapshot, ok)
	}
	snapshot.ParameterSchema()["type"] = "mutated"
	visited := false
	if err := registry.VisitCoreTools(context.Background(), func(entry CoreToolSnapshotEntry) bool {
		visited = true
		if entry.ParameterSchema()["type"] != "object" {
			t.Fatalf("visitor schema mutation leaked: %#v", entry.ParameterSchema())
		}
		return true
	}); err != nil || !visited {
		t.Fatalf("VisitCoreTools() = visited:%t error:%v", visited, err)
	}

	descriptor := factory.Descriptor()
	descriptor.Parameters["type"] = "mutated"
	if factory.Descriptor().Parameters["type"] != "object" {
		t.Fatal("factory descriptor was not detached")
	}
	required, ok := factory.Descriptor().Parameters["required"].([]string)
	if !ok || !reflect.DeepEqual(required, []string{"value"}) {
		t.Fatalf("concrete schema slice type lost: %#v", factory.Descriptor().Parameters["required"])
	}
}

func TestToolFactoryRejectsCyclicAndUnsupportedSchema(t *testing.T) {
	cyclic := map[string]any{"type": "object"}
	cyclic["self"] = cyclic
	if _, err := NewToolFactory(ToolDescriptor{Name: "cycle", Parameters: cyclic}, ToolTraits{},
		func(ToolBuildContext) (Tool, error) { return newMockTool("cycle", ""), nil }); err == nil {
		t.Fatal("cyclic schema was accepted")
	}
	unsupported := map[string]any{"callback": func() {}}
	if _, err := NewToolFactory(ToolDescriptor{Name: "unsupported", Parameters: unsupported}, ToolTraits{},
		func(ToolBuildContext) (Tool, error) { return newMockTool("unsupported", ""), nil }); err == nil {
		t.Fatal("unsupported schema value was accepted")
	}
}

func TestToolRegistryFactoryDependenciesServicesAndExpiredContext(t *testing.T) {
	source, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "deps"))
	baseFactory := mustFactoryForTool(t, newMockTool("base", "base"), ToolTraits{},
		func(ToolBuildContext) (Tool, error) { return newMockTool("base", "base"), nil })
	if err := source.RegisterFactory(baseFactory); err != nil {
		t.Fatal(err)
	}
	var retained ToolBuildContext
	wrapperFactory := mustFactoryForTool(t, newMockTool("wrapper", "wrapper"), ToolTraits{},
		func(ctx ToolBuildContext) (Tool, error) {
			retained = ctx
			dependency, err := ctx.Resolve("base")
			if err != nil {
				return nil, err
			}
			return &factoryDependencyTool{
				mockRegistryTool: newMockTool("wrapper", "wrapper"),
				dependency:       dependency,
			}, nil
		})
	if err := source.RegisterFactory(wrapperFactory); err != nil {
		t.Fatal(err)
	}
	if _, err := retained.Resolve("base"); err == nil {
		t.Fatal("escaped build resolver remained active")
	}
	if _, err := retained.Service("late", func() (any, error) { return &factoryService{}, nil }); err == nil {
		t.Fatal("escaped service cache remained active")
	}

	var nextService atomic.Int64
	serviceFactory := func(name string) ToolFactory {
		return mustFactoryForTool(t, newMockTool(name, name), ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
			value, err := ctx.Service("bundle", func() (any, error) {
				return &factoryService{id: int(nextService.Add(1))}, nil
			})
			if err != nil {
				return nil, err
			}
			return &factoryServiceTool{mockRegistryTool: newMockTool(name, name), service: value.(*factoryService)}, nil
		})
	}
	if err := source.RegisterFactory(serviceFactory("service_a")); err != nil {
		t.Fatal(err)
	}
	if err := source.RegisterFactory(serviceFactory("service_b")); err != nil {
		t.Fatal(err)
	}
	sourceA, _ := source.GetRegistered("service_a")
	sourceB, _ := source.GetRegistered("service_b")
	if sourceA.(*factoryServiceTool).service != sourceB.(*factoryServiceTool).service {
		t.Fatal("separate source registrations did not share owner-local service")
	}

	clone, err := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "deps-clone"))
	if err != nil {
		t.Fatal(err)
	}
	cloneBase, _ := clone.GetRegistered("base")
	cloneWrapperRaw, _ := clone.GetRegistered("wrapper")
	cloneWrapper := cloneWrapperRaw.(*factoryDependencyTool)
	if cloneWrapper.dependency != cloneBase {
		t.Fatal("wrapper dependency was not rebound to destination owner")
	}
	sourceBase, _ := source.GetRegistered("base")
	if cloneWrapper.dependency == sourceBase {
		t.Fatal("wrapper retained source dependency")
	}
	cloneA, _ := clone.GetRegistered("service_a")
	cloneB, _ := clone.GetRegistered("service_b")
	if cloneA.(*factoryServiceTool).service != cloneB.(*factoryServiceTool).service ||
		cloneA.(*factoryServiceTool).service == sourceA.(*factoryServiceTool).service {
		t.Fatal("owner-local service cache leaked across owners")
	}

	source.Unregister("base")
	if partial, err := source.InstantiateForOwner(
		factoryTestOwner(ToolOwnerScopeAgent, "missing"),
	); err == nil ||
		partial != nil {
		t.Fatalf("missing dependency instantiation = %#v, %v", partial, err)
	}

	cycleSource, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "cycle"))
	cycleFactory := mustFactoryForTool(t, newMockTool("cycle", "cycle"), ToolTraits{},
		func(ctx ToolBuildContext) (Tool, error) {
			if ctx.Owner().Scope != ToolOwnerScopeRegistry {
				if _, err := ctx.Resolve("cycle"); err != nil {
					return nil, err
				}
			}
			return newMockTool("cycle", "cycle"), nil
		})
	if err := cycleSource.RegisterFactory(cycleFactory); err != nil {
		t.Fatal(err)
	}
	if clone, err := cycleSource.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "cycle")); err == nil ||
		clone != nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("dependency cycle = %#v, %v", clone, err)
	}

	serviceCycle, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "service-cycle"))
	serviceCycleFactory := mustFactoryForTool(t, newMockTool("service_cycle", "cycle"), ToolTraits{},
		func(ctx ToolBuildContext) (Tool, error) {
			_, err := ctx.Service("cycle", func() (any, error) {
				return ctx.Service("cycle", nil)
			})
			if err != nil {
				return nil, err
			}
			return newMockTool("service_cycle", "cycle"), nil
		})
	if err := serviceCycle.RegisterFactory(serviceCycleFactory); err == nil || serviceCycle.Count() != 0 {
		t.Fatalf("service cycle = error:%v count:%d", err, serviceCycle.Count())
	}
}

func TestToolRegistryFactoryAtomicFailuresAndReverseCleanup(t *testing.T) {
	tests := []struct {
		name  string
		build ToolBuildFunc
	}{
		{name: "error", build: func(ToolBuildContext) (Tool, error) { return nil, errors.New("factory failed") }},
		{name: "panic", build: func(ToolBuildContext) (Tool, error) { panic("factory panic") }},
		{name: "nil", build: func(ToolBuildContext) (Tool, error) { return nil, nil }},
		{name: "typed nil", build: func(ToolBuildContext) (Tool, error) {
			var tool *mockRegistryTool
			return tool, nil
		}},
		{name: "descriptor mismatch", build: func(ToolBuildContext) (Tool, error) {
			return newMockTool("wrong", "wrong"), nil
		}},
		{name: "description mismatch", build: func(ToolBuildContext) (Tool, error) {
			return newMockTool("atomic", "wrong"), nil
		}},
		{name: "schema mismatch", build: func(ToolBuildContext) (Tool, error) {
			tool := newMockTool("atomic", "atomic")
			tool.params = map[string]any{"type": "string"}
			return tool, nil
		}},
		{name: "prompt mismatch", build: func(ToolBuildContext) (Tool, error) {
			return &factoryMetadataTool{
				name: "atomic", desc: "atomic", params: map[string]any{"type": "object"},
				metadata: PromptMetadata{Source: "wrong"},
			}, nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, test.name))
			factory := mustFactoryForTool(t, newMockTool("atomic", "atomic"), ToolTraits{}, test.build)
			if err := registry.RegisterFactory(factory); err == nil || registry.Count() != 0 {
				t.Fatalf("atomic failure = error:%v count:%d", err, registry.Count())
			}
		})
	}
	valueRegistry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "value"))
	valueFactory := mustFactoryForTool(t, factoryValueTool{}, ToolTraits{},
		func(ToolBuildContext) (Tool, error) { return factoryValueTool{}, nil })
	if err := valueRegistry.RegisterFactory(valueFactory); err == nil ||
		!strings.Contains(err.Error(), "non-nil pointer") {
		t.Fatalf("value factory error = %v", err)
	}

	registry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "close"))
	closed := []string{}
	factory := mustFactoryForTool(t, newMockTool("close_target", "expected"), ToolTraits{},
		func(ctx ToolBuildContext) (Tool, error) {
			for _, service := range []*factoryCloseTool{
				{mockRegistryTool: newMockTool("service_1", ""), label: "service_1", closed: &closed},
				{mockRegistryTool: newMockTool("service_2", ""), label: "service_2", closed: &closed, closePanic: true},
			} {
				if _, err := ctx.Service(service.label, func() (any, error) { return service, nil }); err != nil {
					return nil, err
				}
			}
			return &factoryCloseTool{
				mockRegistryTool: newMockTool("wrong", "wrong"), label: "tool", closed: &closed,
			}, nil
		})
	if err := registry.RegisterFactory(factory); err == nil {
		t.Fatal("mismatched closer factory was accepted")
	}
	if !reflect.DeepEqual(closed, []string{"tool", "service_2", "service_1"}) {
		t.Fatalf("reverse cleanup = %#v", closed)
	}

	foreignRegistry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "foreign-error"))
	foreignClosed := []string{}
	foreign := &factoryCloseTool{
		mockRegistryTool: newMockTool("foreign", "foreign"), label: "foreign", closed: &foreignClosed,
	}
	if err := foreignRegistry.RegisterImmutableShared(foreign, ToolTraits{Parallel: ToolParallelSafe}); err != nil {
		t.Fatal(err)
	}
	foreignFactory := mustFactoryForTool(t, newMockTool("foreign_factory", "foreign factory"), ToolTraits{},
		func(ToolBuildContext) (Tool, error) { return foreign, errors.New("foreign returned with error") })
	if err := foreignRegistry.RegisterFactory(foreignFactory); err == nil {
		t.Fatal("foreign pointer plus error was accepted")
	}
	if len(foreignClosed) != 0 {
		t.Fatalf("foreign pointer was closed: %#v", foreignClosed)
	}
}

func TestToolRegistryFactoryRejectsSourceAndDestinationSingletonReuse(t *testing.T) {
	source, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "singleton"))
	var sourceSingleton Tool
	firstFactory := mustFactoryForTool(t, newMockTool("first", "first"), ToolTraits{},
		func(ToolBuildContext) (Tool, error) {
			if sourceSingleton == nil {
				sourceSingleton = newMockTool("first", "first")
			}
			return sourceSingleton, nil
		})
	if err := source.RegisterFactory(firstFactory); err != nil {
		t.Fatal(err)
	}
	if clone, err := source.InstantiateForOwner(
		factoryTestOwner(ToolOwnerScopeAgent, "source-reuse"),
	); err == nil ||
		clone != nil {
		t.Fatalf("source singleton reuse = %#v, %v", clone, err)
	}

	mediaSource, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "media-singleton"))
	mediaSingleton := &factoryMetadataTool{
		name: "media_singleton", desc: "singleton", params: map[string]any{"type": "object"},
	}
	mediaFactory := mustFactoryForTool(t, mediaSingleton, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return mediaSingleton, nil
	})
	if err := mediaSource.RegisterFactory(mediaFactory); err != nil {
		t.Fatal(err)
	}
	callsBefore := mediaSingleton.setCalls
	if clone, err := mediaSource.InstantiateForOwner(
		factoryTestOwner(ToolOwnerScopeAgent, "media-reuse"),
	); err == nil ||
		clone != nil {
		t.Fatalf("media singleton reuse = %#v, %v", clone, err)
	}
	if mediaSingleton.setCalls != callsBefore {
		t.Fatal("source singleton was media-mutated before reuse rejection")
	}

	secondSource, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "destination-reuse"))
	destinationSingleton := &mockRegistryTool{params: map[string]any{"type": "object"}, result: SilentResult("ok")}
	makeFactory := func(name string) ToolFactory {
		return mustFactoryForTool(t, newMockTool(name, name), ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
			if ctx.Owner().Scope == ToolOwnerScopeRegistry {
				return newMockTool(name, name), nil
			}
			destinationSingleton.name = name
			destinationSingleton.desc = name
			return destinationSingleton, nil
		})
	}
	if err := secondSource.RegisterFactory(makeFactory("a")); err != nil {
		t.Fatal(err)
	}
	if err := secondSource.RegisterFactory(makeFactory("b")); err != nil {
		t.Fatal(err)
	}
	if clone, err := secondSource.InstantiateForOwner(
		factoryTestOwner(ToolOwnerScopeAgent, "dest"),
	); err == nil ||
		clone != nil {
		t.Fatalf("destination singleton reuse = %#v, %v", clone, err)
	}
}

func TestToolRegistryFactoryRejectsSingletonAcrossSequentialOwners(t *testing.T) {
	source, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "cross-owner"))
	sourceInstance := newMockTool("cross_owner", "cross owner")
	ownerSingleton := newMockTool("cross_owner", "cross owner")
	var calls atomic.Int64
	factory := mustFactoryForTool(t, sourceInstance, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		if calls.Add(1) == 1 {
			return sourceInstance, nil
		}
		return ownerSingleton, nil
	})
	if err := source.RegisterFactory(factory); err != nil {
		t.Fatal(err)
	}
	first, err := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "first"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := first.GetRegistered("cross_owner"); got != ownerSingleton {
		t.Fatal("first owner did not receive singleton fixture")
	}
	second, secondErr := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "second"))
	if secondErr == nil || second != nil || !strings.Contains(secondErr.Error(), "another owner") {
		t.Fatalf("second owner singleton reuse = %#v, %v", second, secondErr)
	}
	if closeErr := first.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if closeErr := first.Close(); closeErr != nil {
		t.Fatalf("idempotent Close() error = %v", closeErr)
	}
	third, err := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "third-after-close"))
	if err != nil {
		t.Fatalf("post-Close non-overlapping reuse error = %v", err)
	}
	if got, _ := third.GetRegistered("cross_owner"); got != ownerSingleton {
		t.Fatal("post-Close owner did not receive released singleton")
	}
	if closeErr := third.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestToolRegistryFactoryRejectsSingletonAcrossConcurrentOwners(t *testing.T) {
	source, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "cross-owner-race"))
	sourceInstance := newMockTool("cross_owner_race", "cross owner race")
	ownerSingleton := newMockTool("cross_owner_race", "cross owner race")
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	factory := mustFactoryForTool(t, sourceInstance, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		if ctx.Owner().Scope == ToolOwnerScopeRegistry {
			return sourceInstance, nil
		}
		started <- struct{}{}
		<-release
		return ownerSingleton, nil
	})
	if err := source.RegisterFactory(factory); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		registry *ToolRegistry
		err      error
	}
	outcomes := make(chan outcome, 2)
	for _, suffix := range []string{"left", "right"} {
		go func() {
			registry, err := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, suffix))
			outcomes <- outcome{registry: registry, err: err}
		}()
	}
	<-started
	<-started
	close(release)
	successes, failures := 0, 0
	for range 2 {
		result := <-outcomes
		if result.err == nil && result.registry != nil {
			successes++
		} else if result.err != nil && result.registry == nil {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("concurrent singleton outcomes = successes:%d failures:%d", successes, failures)
	}
}

func TestToolRegistryFactoryGlobalTrackerSpansIndependentRootsAndFactories(t *testing.T) {
	buildRoot := func(suffix string, singleton Tool, gate <-chan struct{}, started chan<- struct{}) *ToolRegistry {
		root, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, suffix))
		sourceTool := newMockTool("global_singleton", "global singleton")
		factory := mustFactoryForTool(t, sourceTool, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
			if ctx.Owner().Scope == ToolOwnerScopeRegistry {
				return sourceTool, nil
			}
			if started != nil {
				started <- struct{}{}
			}
			if gate != nil {
				<-gate
			}
			return singleton, nil
		})
		if err := root.RegisterFactory(factory); err != nil {
			t.Fatal(err)
		}
		return root
	}

	sequentialSingleton := newMockTool("global_singleton", "global singleton")
	left := buildRoot("global-left", sequentialSingleton, nil, nil)
	right := buildRoot("global-right", sequentialSingleton, nil, nil)
	defer func() { _ = left.Close() }()
	defer func() { _ = right.Close() }()
	firstClone, err := left.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "global-first"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = firstClone.Close() }()
	if clone, err := right.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "global-second")); err == nil ||
		clone != nil {
		t.Fatalf("independent-root singleton reuse = %#v, %v", clone, err)
	}

	concurrentSingleton := newMockTool("global_singleton", "global singleton")
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	firstRoot := buildRoot("global-race-left", concurrentSingleton, release, started)
	secondRoot := buildRoot("global-race-right", concurrentSingleton, release, started)
	defer func() { _ = firstRoot.Close() }()
	defer func() { _ = secondRoot.Close() }()
	type result struct {
		registry *ToolRegistry
		err      error
	}
	results := make(chan result, 2)
	for index, root := range []*ToolRegistry{firstRoot, secondRoot} {
		go func() {
			registry, err := root.InstantiateForOwner(
				factoryTestOwner(ToolOwnerScopeAgent, fmt.Sprintf("global-race-%d", index)),
			)
			results <- result{registry: registry, err: err}
		}()
	}
	<-started
	<-started
	close(release)
	successes, failures := 0, 0
	var successfulRegistry *ToolRegistry
	for range 2 {
		result := <-results
		if result.err == nil && result.registry != nil {
			successes++
			successfulRegistry = result.registry
		} else if result.err != nil && result.registry == nil {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("global concurrent tracker = success:%d failure:%d", successes, failures)
	}
	if err := successfulRegistry.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestToolRegistryFactoryServiceSingletonIsOwnerLocal(t *testing.T) {
	singleton := &factoryService{id: 42}
	makeRoot := func(suffix string) (*ToolRegistry, error) {
		root, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, suffix))
		factory := mustFactoryForTool(t, newMockTool("service_singleton", "service singleton"), ToolTraits{},
			func(ctx ToolBuildContext) (Tool, error) {
				value, err := ctx.Service("singleton", func() (any, error) { return singleton, nil })
				if err != nil {
					return nil, err
				}
				return &factoryServiceTool{
					mockRegistryTool: newMockTool("service_singleton", "service singleton"),
					service:          value.(*factoryService),
				}, nil
			})
		return root, root.RegisterFactory(factory)
	}
	source, err := makeRoot("service-root")
	if err != nil {
		t.Fatal(err)
	}
	child, childErr := source.InstantiateForOwner(
		factoryTestOwner(ToolOwnerScopeAgent, "service-child"),
	)
	if childErr == nil || child != nil {
		t.Fatalf("source service singleton leaked to child = %#v, %v", child, childErr)
	}
	other, otherErr := makeRoot("service-other-live")
	if otherErr == nil || other == nil {
		t.Fatalf("independent live service singleton = %#v, %v", other, otherErr)
	}
	if closeErr := source.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	afterClose, err := makeRoot("service-after-close")
	if err != nil {
		t.Fatalf("service reservation not released after Close: %v", err)
	}
	if closeErr := afterClose.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestToolBuildContextSealWaitsForInflightResolveAndService(t *testing.T) {
	t.Run("resolve", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		ctx, deactivate := activateToolBuildContext(ToolBuildContext{
			resolve: func(string) (Tool, error) {
				close(entered)
				<-release
				return newMockTool("dependency", "dependency"), nil
			},
		})
		resolved := make(chan error, 1)
		go func() {
			_, err := ctx.Resolve("dependency")
			resolved <- err
		}()
		<-entered
		sealed := make(chan struct{})
		go func() {
			deactivate()
			close(sealed)
		}()
		select {
		case <-sealed:
			t.Fatal("context sealed before in-flight Resolve quiesced")
		case <-time.After(20 * time.Millisecond):
		}
		close(release)
		if err := <-resolved; err != nil {
			t.Fatal(err)
		}
		<-sealed
		if _, err := ctx.Resolve("dependency"); err == nil {
			t.Fatal("Resolve entered after context seal")
		}
	})

	t.Run("service", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		transaction := newToolServiceTransaction(nil)
		ctx, deactivate := activateToolBuildContext(ToolBuildContext{services: transaction})
		created := make(chan error, 1)
		go func() {
			_, err := ctx.Service("service", func() (any, error) {
				close(entered)
				<-release
				return &factoryService{}, nil
			})
			created <- err
		}()
		<-entered
		sealed := make(chan struct{})
		go func() {
			deactivate()
			close(sealed)
		}()
		select {
		case <-sealed:
			t.Fatal("context sealed before in-flight Service quiesced")
		case <-time.After(20 * time.Millisecond):
		}
		close(release)
		if err := <-created; err != nil {
			t.Fatal(err)
		}
		<-sealed
		if _, err := ctx.Service("late", func() (any, error) { return &factoryService{}, nil }); err == nil {
			t.Fatal("Service entered after context seal")
		}
		if err := transaction.cleanupAndRelease(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("concurrent resolve is serialized", func(t *testing.T) {
		var active atomic.Int64
		var maximum atomic.Int64
		ctx, deactivate := activateToolBuildContext(ToolBuildContext{
			resolve: func(string) (Tool, error) {
				current := active.Add(1)
				defer active.Add(-1)
				for {
					seen := maximum.Load()
					if seen >= current || maximum.CompareAndSwap(seen, current) {
						break
					}
				}
				time.Sleep(time.Millisecond)
				return newMockTool("dependency", "dependency"), nil
			},
		})
		var wait sync.WaitGroup
		for range 8 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				if _, err := ctx.Resolve("dependency"); err != nil {
					t.Error(err)
				}
			}()
		}
		wait.Wait()
		deactivate()
		if maximum.Load() != 1 {
			t.Fatalf("concurrent resolver maximum = %d", maximum.Load())
		}
	})
}

func TestToolRegistryFactoryFailureClosesBeforeReservationRelease(t *testing.T) {
	source, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "failure-close"))
	sourceTool := newMockTool("a_blocking", "blocking")
	singleton := &factoryBlockingCloseTool{
		mockRegistryTool: newMockTool("a_blocking", "blocking"),
		entered:          make(chan struct{}),
		release:          make(chan struct{}),
	}
	blockingFactory := mustFactoryForTool(t, sourceTool, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		if ctx.Owner().Scope == ToolOwnerScopeRegistry {
			return sourceTool, nil
		}
		return singleton, nil
	})
	if err := source.RegisterFactory(blockingFactory); err != nil {
		t.Fatal(err)
	}
	failingFactory := mustFactoryForTool(t, newMockTool("z_failure", "failure"), ToolTraits{},
		func(ctx ToolBuildContext) (Tool, error) {
			if ctx.Owner().Scope != ToolOwnerScopeRegistry {
				return nil, errors.New("later factory failure")
			}
			return newMockTool("z_failure", "failure"), nil
		})
	if err := source.RegisterFactory(failingFactory); err != nil {
		t.Fatal(err)
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "failing-first"))
		firstDone <- err
	}()
	<-singleton.entered
	if second, err := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "during-close")); err == nil ||
		second != nil || !strings.Contains(err.Error(), "another owner") {
		t.Fatalf("reservation escaped blocking cleanup = %#v, %v", second, err)
	}
	close(singleton.release)
	if err := <-firstDone; err == nil || !strings.Contains(err.Error(), "later factory failure") {
		t.Fatalf("first failed instantiation error = %v", err)
	}
	if singleton.closeCalls.Load() != 1 {
		t.Fatalf("blocking singleton Close calls = %d", singleton.closeCalls.Load())
	}
}

func TestToolRegistryCloseKeepsReservationUntilCleanupCompletes(t *testing.T) {
	source, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "live-close"))
	sourceTool := newMockTool("live_close", "live close")
	singleton := &factoryBlockingCloseTool{
		mockRegistryTool: newMockTool("live_close", "live close"),
		entered:          make(chan struct{}),
		release:          make(chan struct{}),
	}
	factory := mustFactoryForTool(t, sourceTool, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		if ctx.Owner().Scope == ToolOwnerScopeRegistry {
			return sourceTool, nil
		}
		return singleton, nil
	})
	if err := source.RegisterFactory(factory); err != nil {
		t.Fatal(err)
	}
	first, err := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "live-first"))
	if err != nil {
		t.Fatal(err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- first.Close() }()
	<-singleton.entered
	if second, err := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "live-second")); err == nil ||
		second != nil {
		t.Fatalf("live reservation escaped Close = %#v, %v", second, err)
	}
	close(singleton.release)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if first.Count() != 0 {
		t.Fatal("closed owned registry retained tools")
	}
	if err := first.Close(); err != nil || singleton.closeCalls.Load() != 1 {
		t.Fatalf("idempotent Close = error:%v calls:%d", err, singleton.closeCalls.Load())
	}
	if _, err := first.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "closed-source")); err == nil {
		t.Fatal("closed registry allowed owner instantiation")
	}
}

func TestToolRegistryCloseFailureQuarantinesInstance(t *testing.T) {
	source, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "close-quarantine"))
	sourceTool := newMockTool("close_quarantine", "close quarantine")
	closed := []string{}
	singleton := &factoryCloseTool{
		mockRegistryTool: newMockTool("close_quarantine", "close quarantine"),
		label:            "quarantined",
		closed:           &closed,
		closeErr:         errors.New("cleanup uncertain"),
	}
	factory := mustFactoryForTool(t, sourceTool, ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		if ctx.Owner().Scope == ToolOwnerScopeRegistry {
			return sourceTool, nil
		}
		return singleton, nil
	})
	if err := source.RegisterFactory(factory); err != nil {
		t.Fatal(err)
	}
	first, err := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "quarantine-first"))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err == nil || !strings.Contains(err.Error(), "cleanup uncertain") {
		t.Fatalf("failed Close() error = %v", err)
	}
	if !reflect.DeepEqual(closed, []string{"quarantined"}) {
		t.Fatalf("failed Close calls = %#v", closed)
	}
	if second, err := source.InstantiateForOwner(
		factoryTestOwner(ToolOwnerScopeAgent, "quarantine-second"),
	); err == nil || second != nil ||
		!strings.Contains(err.Error(), "another owner") {
		t.Fatalf("quarantined singleton reuse = %#v, %v", second, err)
	}
}

func TestToolRegistryFactoryMediaStoreAndGenerationIsolation(t *testing.T) {
	source, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "media"))
	storeOne := media.NewFileMediaStore()
	storeTwo := media.NewFileMediaStore()
	source.SetMediaStore(storeOne)
	factory := mustFactoryForTool(t, &mockMediaStoreAwareTool{
		mockRegistryTool: *newMockTool("media_factory", "media factory"),
	}, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return &mockMediaStoreAwareTool{mockRegistryTool: *newMockTool("media_factory", "media factory")}, nil
	})
	if err := source.RegisterFactory(factory); err != nil {
		t.Fatal(err)
	}
	clone, err := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "media-clone"))
	if err != nil {
		t.Fatal(err)
	}
	sourceToolRaw, _ := source.GetRegistered("media_factory")
	cloneToolRaw, _ := clone.GetRegistered("media_factory")
	sourceTool := sourceToolRaw.(*mockMediaStoreAwareTool)
	cloneTool := cloneToolRaw.(*mockMediaStoreAwareTool)
	if sourceTool == cloneTool || sourceTool.store != storeOne || cloneTool.store != storeOne {
		t.Fatal("owner media injection did not create equivalent distinct tools")
	}
	clone.SetMediaStore(storeTwo)
	if cloneTool.store != storeTwo || sourceTool.store != storeOne {
		t.Fatal("destination media injection mutated source")
	}

	conflict, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "media-conflict"))
	started := make(chan struct{})
	release := make(chan struct{})
	closed := []string{}
	blockingFactory := mustFactoryForTool(t, newMockTool("blocking_media", "blocking"), ToolTraits{},
		func(ToolBuildContext) (Tool, error) {
			close(started)
			<-release
			return &factoryCloseTool{
				mockRegistryTool: newMockTool("blocking_media", "blocking"), label: "blocking", closed: &closed,
			}, nil
		})
	done := make(chan error, 1)
	go func() { done <- conflict.RegisterFactory(blockingFactory) }()
	<-started
	conflict.SetMediaStore(storeTwo)
	close(release)
	if err := <-done; err == nil || conflict.Count() != 0 || !reflect.DeepEqual(closed, []string{"blocking"}) {
		t.Fatalf("media generation conflict = error:%v count:%d closed:%#v", err, conflict.Count(), closed)
	}
}

func TestToolRegistryFactoryHiddenTTLAndDiscoveryAreOwnerLocal(t *testing.T) {
	source, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "discovery"))
	source.SetAllowlist([]string{
		"hidden_lookup", "hidden_ttl", RegexSearchToolName, BM25SearchToolName,
	})
	hiddenFactory := mustFactoryForTool(t, newMockTool("hidden_lookup", "lookup hidden data"), ToolTraits{},
		func(ToolBuildContext) (Tool, error) { return newMockTool("hidden_lookup", "lookup hidden data"), nil })
	if err := source.RegisterHiddenFactory(hiddenFactory); err != nil {
		t.Fatal(err)
	}
	hiddenTTLFactory := mustFactoryForTool(t, newMockTool("hidden_ttl", "TTL parity"), ToolTraits{},
		func(ToolBuildContext) (Tool, error) { return newMockTool("hidden_ttl", "TTL parity"), nil })
	if err := source.RegisterHiddenFactory(hiddenTTLFactory); err != nil {
		t.Fatal(err)
	}
	source.PromoteTools([]string{"hidden_ttl"}, 3)
	if err := source.RegisterFactory(NewRegexSearchToolFactory(2, 5)); err != nil {
		t.Fatal(err)
	}
	if err := source.RegisterFactory(NewBM25SearchToolFactory(2, 5)); err != nil {
		t.Fatal(err)
	}
	child, err := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "discovery-child"))
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "discovery-sibling"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := child.Get("hidden_ttl"); !ok {
		t.Fatal("active hidden TTL was not preserved")
	}
	child.TickTTL()
	if _, ok := source.Get("hidden_ttl"); !ok {
		t.Fatal("child TTL tick changed source TTL")
	}
	if owner, ok := child.Owner(); !ok || owner.AgentID != "agent-discovery-child" {
		t.Fatalf("child owner = %#v, %t", owner, ok)
	}
	if traits, ok := child.Traits(BM25SearchToolName); !ok || traits.Risk != ToolRiskMutation ||
		traits.Sharing != ToolSharingPerOwner {
		t.Fatalf("discovery traits = %#v, %t", traits, ok)
	}
	if child.Version() != source.Version() || sibling.Version() != source.Version() {
		t.Fatalf("registry version parity = source:%d child:%d sibling:%d",
			source.Version(), child.Version(), sibling.Version())
	}
	regexRaw, _ := child.GetRegistered(RegexSearchToolName)
	result := regexRaw.Execute(context.Background(), map[string]any{"pattern": "hidden_lookup"})
	if result.IsError {
		t.Fatalf("child discovery failed: %#v", result)
	}
	if _, ok := child.Get("hidden_lookup"); !ok {
		t.Fatal("child discovery did not promote child hidden tool")
	}
	if _, ok := source.Get("hidden_lookup"); ok {
		t.Fatal("child discovery promoted source hidden tool")
	}
	if _, ok := sibling.Get("hidden_lookup"); ok {
		t.Fatal("child discovery promoted sibling hidden tool")
	}
	child.TickTTL()
	child.TickTTL()
	if _, ok := child.Get("hidden_lookup"); ok {
		t.Fatal("child TTL did not expire independently")
	}

	childBM25Raw, _ := child.GetRegistered(BM25SearchToolName)
	siblingBM25Raw, _ := sibling.GetRegistered(BM25SearchToolName)
	childBM25 := childBM25Raw.(*BM25SearchTool)
	siblingBM25 := siblingBM25Raw.(*BM25SearchTool)
	if childBM25.registry != child || siblingBM25.registry != sibling || childBM25 == siblingBM25 {
		t.Fatal("BM25 discovery factory did not bind destination owner")
	}
}

func TestBM25SearchToolConcurrentCacheRebuild(t *testing.T) {
	registry := NewToolRegistry()
	registry.RegisterHidden(newMockTool("hidden", "searchable hidden tool"))
	tool := NewBM25SearchTool(registry, 2, 5)
	if tool.getOrBuildEngine() == nil {
		t.Fatal("initial cache build failed")
	}

	const workers = 8
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(workers + 1)
	for range workers {
		go func() {
			defer wait.Done()
			<-start
			for range 100 {
				if tool.getOrBuildEngine() == nil {
					t.Error("cache disappeared during concurrent rebuild")
					return
				}
			}
		}()
	}
	go func() {
		defer wait.Done()
		<-start
		for index := range 100 {
			registry.RegisterHidden(newMockTool("hidden", fmt.Sprintf("version %d", index)))
			_ = tool.getOrBuildEngine()
		}
	}()
	close(start)
	wait.Wait()
	if tool.getOrBuildEngine() == nil || tool.cacheVersion != registry.Version() {
		t.Fatalf("final cache version = %d, registry = %d", tool.cacheVersion, registry.Version())
	}
}

func TestToolRegistryFactoryConcurrentOwnerInstantiation(t *testing.T) {
	source, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "race"))
	var active atomic.Int64
	factory := mustFactoryForTool(t, newMockTool("race_tool", "race"), ToolTraits{},
		func(ctx ToolBuildContext) (Tool, error) {
			active.Add(1)
			defer active.Add(-1)
			if ctx.Owner().Scope != ToolOwnerScopeTurn {
				return newMockTool("race_tool", "race"), nil
			}
			time.Sleep(time.Microsecond)
			return newMockTool("race_tool", "race"), nil
		})
	if err := source.RegisterFactory(factory); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for index := range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			clone, err := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeTurn, fmt.Sprint(index)))
			if err != nil || clone.Count() != 1 {
				t.Errorf("InstantiateForOwner() = %#v, %v", clone, err)
			}
		}()
	}
	wait.Wait()
	if active.Load() != 0 {
		t.Fatal("factory construction did not quiesce")
	}
}
