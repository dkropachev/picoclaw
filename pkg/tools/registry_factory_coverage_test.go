package tools

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/media"
)

type coverageToolFactory struct {
	descriptor   ToolDescriptor
	traits       ToolTraits
	descriptorFn func() ToolDescriptor
	traitsFn     func() ToolTraits
	newFn        ToolBuildFunc
}

func (factory *coverageToolFactory) Descriptor() ToolDescriptor {
	if factory.descriptorFn != nil {
		return factory.descriptorFn()
	}
	return factory.descriptor
}

func (factory *coverageToolFactory) Traits() ToolTraits {
	if factory.traitsFn != nil {
		return factory.traitsFn()
	}
	return factory.traits
}

func (factory *coverageToolFactory) New(ctx ToolBuildContext) (Tool, error) {
	return factory.newFn(ctx)
}

type coverageHookTool struct {
	name string
	hook func()
}

func (tool *coverageHookTool) Name() string {
	if tool.hook != nil {
		tool.hook()
		tool.hook = nil
	}
	return tool.name
}

func (*coverageHookTool) Description() string        { return "hook tool" }
func (*coverageHookTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (*coverageHookTool) Execute(context.Context, map[string]any) *ToolResult {
	return SilentResult("hook")
}

type coveragePanicMediaTool struct {
	*mockRegistryTool
	closeCalls atomic.Int64
}

func (*coveragePanicMediaTool) SetMediaStore(media.MediaStore) { panic("media setter panic") }
func (tool *coveragePanicMediaTool) Close() error {
	tool.closeCalls.Add(1)
	return nil
}

func coverageFactoryDescriptor(name string) ToolDescriptor {
	return ToolDescriptor{
		Name: name, Description: name,
		Parameters: map[string]any{"type": "object"},
		PromptMetadata: PromptMetadata{
			Layer: ToolPromptLayerCapability, Slot: ToolPromptSlotTooling,
			Source: ToolPromptSourceRegistry,
		},
	}
}

func coverageSourceWithChildFactory(
	t *testing.T,
	name string,
	child ToolBuildFunc,
) *ToolRegistry {
	t.Helper()
	source, err := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, name+"-source"))
	if err != nil {
		t.Fatal(err)
	}
	factory := mustFactoryForTool(t, newMockTool(name, name), ToolTraits{}, func(ctx ToolBuildContext) (Tool, error) {
		if ctx.Owner().Scope == ToolOwnerScopeRegistry {
			return newMockTool(name, name), nil
		}
		return child(ctx)
	})
	if err := source.RegisterFactory(factory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	return source
}

func TestToolRegistryFactoryCoverageAdmissionAndRegistrationConflicts(t *testing.T) {
	descriptor := coverageFactoryDescriptor("coverage_factory")
	normalFactory := &coverageToolFactory{
		descriptor: descriptor,
		newFn: func(ToolBuildContext) (Tool, error) {
			return newMockTool("coverage_factory", "coverage_factory"), nil
		},
	}

	var nilRegistry *ToolRegistry
	if err := nilRegistry.RegisterFactory(normalFactory); err == nil {
		t.Fatal("nil registry accepted a factory")
	}
	owned, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "typed-nil"))
	var nilFactory *coverageToolFactory
	if err := owned.RegisterFactory(nilFactory); err == nil {
		t.Fatal("typed-nil factory was accepted")
	}

	metadataCases := []struct {
		name    string
		factory ToolFactory
	}{
		{
			name: "descriptor panic",
			factory: &coverageToolFactory{descriptorFn: func() ToolDescriptor {
				panic("descriptor panic")
			}},
		},
		{
			name: "invalid descriptor",
			factory: &coverageToolFactory{
				descriptor: ToolDescriptor{},
				newFn:      func(ToolBuildContext) (Tool, error) { return nil, nil },
			},
		},
		{
			name: "traits panic",
			factory: &coverageToolFactory{
				descriptor: descriptor,
				traitsFn:   func() ToolTraits { panic("traits panic") },
			},
		},
		{
			name: "invalid traits",
			factory: &coverageToolFactory{
				descriptor: descriptor,
				traits:     ToolTraits{Risk: ToolRiskClass("invalid")},
			},
		},
		{
			name: "immutable sharing",
			factory: &coverageToolFactory{
				descriptor: descriptor,
				traits: ToolTraits{
					Parallel: ToolParallelSafe, Sharing: ToolSharingImmutableShared,
				},
			},
		},
	}
	for _, test := range metadataCases {
		t.Run(test.name, func(t *testing.T) {
			registry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, test.name))
			if err := registry.RegisterFactory(test.factory); err == nil {
				t.Fatal("invalid factory metadata was accepted")
			}
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(*ToolRegistry)
	}{
		{name: "ownership changed", mutate: func(registry *ToolRegistry) { registry.owned = false }},
		{name: "closed changed", mutate: func(registry *ToolRegistry) { registry.closed = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, test.name))
			factory := &coverageToolFactory{
				descriptorFn: func() ToolDescriptor {
					registry.mu.Lock()
					test.mutate(registry)
					registry.mu.Unlock()
					return descriptor
				},
				newFn: normalFactory.newFn,
			}
			if err := registry.RegisterFactory(factory); err == nil {
				t.Fatal("registry state change during admission was ignored")
			}
		})
	}

	t.Run("missing dependency", func(t *testing.T) {
		registry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "missing-dependency"))
		factory := mustFactoryForTool(t, newMockTool("missing_user", "missing_user"), ToolTraits{},
			func(ctx ToolBuildContext) (Tool, error) {
				return ctx.Resolve("absent")
			})
		if err := registry.RegisterFactory(factory); err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("missing dependency error = %v", err)
		}
	})

	t.Run("source instance reuse", func(t *testing.T) {
		registry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "source-reuse-admission"))
		t.Cleanup(func() { _ = registry.Close() })
		shared := newMockTool("shared_admission", "shared_admission")
		if err := registry.RegisterImmutableShared(shared, ToolTraits{Parallel: ToolParallelSafe}); err != nil {
			t.Fatal(err)
		}
		factory := mustFactoryForTool(t, newMockTool("reuse_admission", "reuse_admission"), ToolTraits{},
			func(ToolBuildContext) (Tool, error) { return shared, nil })
		if err := registry.RegisterFactory(factory); err == nil || !strings.Contains(err.Error(), "reused") {
			t.Fatalf("source reuse error = %v", err)
		}
	})

	t.Run("globally reserved product", func(t *testing.T) {
		registry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "reserved-admission"))
		candidate := newMockTool("reserved_admission", "reserved_admission")
		identity, err := toolInstanceIdentity(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if err := globalOwnedToolInstances.reserve(identity, candidate); err != nil {
			t.Fatal(err)
		}
		defer globalOwnedToolInstances.release(identity)
		factory := mustFactoryForTool(t, candidate, ToolTraits{},
			func(ToolBuildContext) (Tool, error) { return candidate, nil })
		if err := registry.RegisterFactory(factory); err == nil || !strings.Contains(err.Error(), "another owner") {
			t.Fatalf("reserved product error = %v", err)
		}
	})

	t.Run("closed during construction", func(t *testing.T) {
		registry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "closed-construction"))
		closed := []string{}
		factory := mustFactoryForTool(t, newMockTool("closed_construction", "closed_construction"), ToolTraits{},
			func(ToolBuildContext) (Tool, error) {
				if err := registry.Close(); err != nil {
					return nil, err
				}
				return &factoryCloseTool{
					mockRegistryTool: newMockTool("closed_construction", "closed_construction"),
					label:            "candidate", closed: &closed,
				}, nil
			})
		if err := registry.RegisterFactory(factory); err == nil || !reflect.DeepEqual(closed, []string{"candidate"}) {
			t.Fatalf("closed construction = error:%v closed:%v", err, closed)
		}
	})

	t.Run("name changed during construction", func(t *testing.T) {
		registry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "name-changed"))
		closed := []string{}
		factory := mustFactoryForTool(t, newMockTool("name_changed", "name_changed"), ToolTraits{},
			func(ToolBuildContext) (Tool, error) {
				registry.Register(newMockTool("name_changed", "occupant"))
				return &factoryCloseTool{
					mockRegistryTool: newMockTool("name_changed", "name_changed"),
					label:            "candidate", closed: &closed,
				}, nil
			})
		if err := registry.RegisterFactory(factory); err == nil || !reflect.DeepEqual(closed, []string{"candidate"}) {
			t.Fatalf("name conflict = error:%v closed:%v", err, closed)
		}
	})

	t.Run("dependency changed during construction", func(t *testing.T) {
		registry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "dependency-changed"))
		dependencyFactory := mustFactoryForTool(t, newMockTool("dependency", "dependency"), ToolTraits{},
			func(ToolBuildContext) (Tool, error) { return newMockTool("dependency", "dependency"), nil })
		if err := registry.RegisterFactory(dependencyFactory); err != nil {
			t.Fatal(err)
		}
		factory := mustFactoryForTool(t, newMockTool("dependency_user", "dependency_user"), ToolTraits{},
			func(ctx ToolBuildContext) (Tool, error) {
				if _, err := ctx.Resolve("dependency"); err != nil {
					return nil, err
				}
				registry.Unregister("dependency")
				registry.Register(newMockTool("dependency", "replacement"))
				return newMockTool("dependency_user", "dependency_user"), nil
			})
		if err := registry.RegisterFactory(factory); err == nil || !strings.Contains(err.Error(), "dependency") {
			t.Fatalf("dependency conflict error = %v", err)
		}
		_ = registry.Close()
	})

	t.Run("service changed during construction", func(t *testing.T) {
		registry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "service-changed"))
		factory := mustFactoryForTool(t, newMockTool("service_changed", "service_changed"), ToolTraits{},
			func(ctx ToolBuildContext) (Tool, error) {
				if _, err := ctx.Service(
					"conflict",
					func() (any, error) { return &factoryService{id: 1}, nil },
				); err != nil {
					return nil, err
				}
				registry.services.mu.Lock()
				registry.services.values["conflict"] = &factoryService{id: 2}
				registry.services.mu.Unlock()
				return newMockTool("service_changed", "service_changed"), nil
			})
		if err := registry.RegisterFactory(factory); err == nil || !strings.Contains(err.Error(), "changed") {
			t.Fatalf("service conflict error = %v", err)
		}
	})

	t.Run("media setter panic", func(t *testing.T) {
		registry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "media-panic"))
		var candidate *coveragePanicMediaTool
		factory := mustFactoryForTool(t, newMockTool("media_panic", "media_panic"), ToolTraits{},
			func(ToolBuildContext) (Tool, error) {
				candidate = &coveragePanicMediaTool{mockRegistryTool: newMockTool("media_panic", "media_panic")}
				return candidate, nil
			})
		if err := registry.RegisterFactory(factory); err == nil || candidate.closeCalls.Load() != 1 {
			t.Fatalf("media panic = error:%v closes:%d", err, candidate.closeCalls.Load())
		}
	})
}

func TestToolRegistryFactoryCoverageImmutableAdmission(t *testing.T) {
	traits := ToolTraits{Parallel: ToolParallelSafe}
	tool := newMockTool("immutable_coverage", "immutable_coverage")
	var nilRegistry *ToolRegistry
	if err := nilRegistry.RegisterImmutableShared(tool, traits); err == nil {
		t.Fatal("nil registry accepted immutable tool")
	}
	registry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "immutable-nil"))
	t.Cleanup(func() { _ = registry.Close() })
	var nilTool *mockRegistryTool
	if err := registry.RegisterImmutableShared(nilTool, traits); err == nil {
		t.Fatal("typed-nil immutable tool was accepted")
	}
	if err := registry.RegisterImmutableShared(factoryValueTool{}, traits); err == nil ||
		!strings.Contains(err.Error(), "non-nil pointer") {
		t.Fatalf("value immutable tool error = %v", err)
	}
	if err := NewToolRegistry().RegisterImmutableShared(tool, traits); err == nil {
		t.Fatal("unowned registry accepted immutable tool")
	}
	closed, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "immutable-closed"))
	_ = closed.Close()
	if err := closed.RegisterImmutableShared(tool, traits); err == nil {
		t.Fatal("closed registry accepted immutable tool")
	}
	if err := registry.RegisterImmutableShared(tool, ToolTraits{Risk: ToolRiskClass("invalid")}); err == nil {
		t.Fatal("invalid immutable traits were accepted")
	}
	panicMetadata := &factoryMetadataTool{panicNow: true}
	if err := registry.RegisterImmutableShared(panicMetadata, traits); err == nil {
		t.Fatal("panicking immutable metadata was accepted")
	}
	panicName := &coverageHookTool{name: "panic_name", hook: func() { panic("name panic") }}
	if err := registry.RegisterImmutableShared(panicName, traits); err == nil {
		t.Fatal("panicking immutable name was accepted")
	}

	for _, test := range []struct {
		name   string
		mutate func(*ToolRegistry)
	}{
		{name: "ownership changed", mutate: func(registry *ToolRegistry) { registry.owned = false }},
		{name: "closed changed", mutate: func(registry *ToolRegistry) { registry.closed = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidateRegistry, _ := NewOwnedToolRegistry(
				factoryTestOwner(ToolOwnerScopeRegistry, "immutable-"+test.name),
			)
			candidate := &coverageHookTool{name: "immutable_hook"}
			candidate.hook = func() {
				candidateRegistry.mu.Lock()
				test.mutate(candidateRegistry)
				candidateRegistry.mu.Unlock()
			}
			if err := candidateRegistry.RegisterImmutableShared(candidate, traits); err == nil {
				t.Fatal("registry state change during immutable admission was ignored")
			}
		})
	}

	blocked, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "immutable-blocked"))
	t.Cleanup(func() { _ = blocked.Close() })
	blocked.SetAllowlist([]string{})
	if err := blocked.RegisterImmutableShared(tool, traits); err != nil || blocked.Count() != 0 {
		t.Fatalf("blocked immutable registration = error:%v count:%d", err, blocked.Count())
	}
	collision, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "immutable-collision"))
	t.Cleanup(func() { _ = collision.Close() })
	if err := collision.RegisterImmutableShared(tool, traits); err != nil {
		t.Fatal(err)
	}
	if err := collision.RegisterImmutableShared(newMockTool(tool.Name(), tool.Description()), traits); err == nil {
		t.Fatal("immutable collision was accepted")
	}
}

func TestToolRegistryFactoryCoverageInstantiationFailures(t *testing.T) {
	var nilRegistry *ToolRegistry
	if child, err := nilRegistry.InstantiateForOwner(
		factoryTestOwner(ToolOwnerScopeAgent, "nil"),
	); err == nil ||
		child != nil {
		t.Fatalf("nil source instantiation = %#v, %v", child, err)
	}
	if child, err := NewToolRegistry().InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "legacy")); err == nil ||
		child != nil {
		t.Fatalf("unowned source instantiation = %#v, %v", child, err)
	}
	invalidOwnerSource, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "invalid-owner"))
	if child, err := invalidOwnerSource.InstantiateForOwner(ToolOwner{}); err == nil || child != nil {
		t.Fatalf("invalid destination owner = %#v, %v", child, err)
	}

	t.Run("nil source entry is ignored", func(t *testing.T) {
		source, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "nil-entry"))
		source.tools["nil"] = nil
		child, err := source.InstantiateForOwner(factoryTestOwner(ToolOwnerScopeAgent, "nil-entry-child"))
		if err != nil || child.Count() != 0 {
			t.Fatalf("nil entry clone = %#v, %v", child, err)
		}
		_ = child.Close()
	})

	t.Run("unsafe immutable metadata", func(t *testing.T) {
		source, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "unsafe-immutable"))
		t.Cleanup(func() { _ = source.Close() })
		if err := source.RegisterImmutableShared(newMockTool("unsafe_immutable", "unsafe_immutable"), ToolTraits{
			Parallel: ToolParallelSafe,
		}); err != nil {
			t.Fatal(err)
		}
		source.tools["unsafe_immutable"].traits.Parallel = ToolParallelSerialized
		if child, err := source.InstantiateForOwner(
			factoryTestOwner(ToolOwnerScopeAgent, "unsafe-child"),
		); err == nil ||
			child != nil {
			t.Fatalf("unsafe immutable clone = %#v, %v", child, err)
		}
	})

	t.Run("invalid immutable product", func(t *testing.T) {
		source, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "invalid-immutable"))
		t.Cleanup(func() { _ = source.Close() })
		shared := newMockTool("invalid_immutable", "invalid immutable")
		if err := source.RegisterImmutableShared(shared, ToolTraits{
			Parallel: ToolParallelSafe,
		}); err != nil {
			t.Fatal(err)
		}
		source.tools["invalid_immutable"].Tool = factoryValueTool{}
		if child, err := source.InstantiateForOwner(
			factoryTestOwner(ToolOwnerScopeAgent, "invalid-immutable-child"),
		); err == nil || child != nil || !strings.Contains(err.Error(), "non-nil pointer") {
			t.Fatalf("invalid immutable product = %#v, %v", child, err)
		}
		_ = source.Close()
	})

	t.Run("immutable lease conflict", func(t *testing.T) {
		source, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "immutable-conflict"))
		t.Cleanup(func() { _ = source.Close() })
		shared := newMockTool("immutable_conflict", "immutable conflict")
		if err := source.RegisterImmutableShared(shared, ToolTraits{
			Parallel: ToolParallelSafe,
		}); err != nil {
			t.Fatal(err)
		}
		foreign := newMockTool("immutable_conflict", "immutable conflict")
		identity, identityErr := toolInstanceIdentity(foreign)
		if identityErr != nil {
			t.Fatal(identityErr)
		}
		if reserveErr := globalOwnedToolInstances.reserve(identity, foreign); reserveErr != nil {
			t.Fatal(reserveErr)
		}
		t.Cleanup(func() { globalOwnedToolInstances.release(identity) })
		source.tools["immutable_conflict"].Tool = foreign
		if child, err := source.InstantiateForOwner(
			factoryTestOwner(ToolOwnerScopeAgent, "immutable-conflict-child"),
		); err == nil || child != nil || !strings.Contains(err.Error(), "exclusively leased") {
			t.Fatalf("immutable lease conflict = %#v, %v", child, err)
		}
		globalOwnedToolInstances.release(identity)
		_ = source.Close()
	})

	t.Run("missing owner factory", func(t *testing.T) {
		source := coverageSourceWithChildFactory(t, "missing_factory", func(ToolBuildContext) (Tool, error) {
			return newMockTool("missing_factory", "missing_factory"), nil
		})
		source.tools["missing_factory"].factory = nil
		if child, err := source.InstantiateForOwner(
			factoryTestOwner(ToolOwnerScopeAgent, "missing-factory-child"),
		); err == nil ||
			child != nil {
			t.Fatalf("missing factory clone = %#v, %v", child, err)
		}
	})

	t.Run("fresh product returned with error is cleaned", func(t *testing.T) {
		closed := []string{}
		source := coverageSourceWithChildFactory(t, "fresh_error", func(ToolBuildContext) (Tool, error) {
			return &factoryCloseTool{
				mockRegistryTool: newMockTool("fresh_error", "fresh_error"),
				label:            "fresh", closed: &closed,
			}, errors.New("construction failed")
		})
		if child, err := source.InstantiateForOwner(
			factoryTestOwner(ToolOwnerScopeAgent, "fresh-error-child"),
		); err == nil ||
			child != nil {
			t.Fatalf("fresh error clone = %#v, %v", child, err)
		}
		if !reflect.DeepEqual(closed, []string{"fresh"}) {
			t.Fatalf("fresh product cleanup = %v", closed)
		}
	})

	t.Run("source product returned with error is not cleaned", func(t *testing.T) {
		var sourceProduct Tool
		source, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "source-error"))
		factory := mustFactoryForTool(t, newMockTool("source_error", "source_error"), ToolTraits{},
			func(ctx ToolBuildContext) (Tool, error) {
				if ctx.Owner().Scope == ToolOwnerScopeRegistry {
					sourceProduct = newMockTool("source_error", "source_error")
					return sourceProduct, nil
				}
				return sourceProduct, errors.New("foreign error")
			})
		if err := source.RegisterFactory(factory); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = source.Close() }()
		if child, err := source.InstantiateForOwner(
			factoryTestOwner(ToolOwnerScopeAgent, "source-error-child"),
		); err == nil ||
			child != nil ||
			!strings.Contains(err.Error(), "foreign") {
			t.Fatalf("source error clone = %#v, %v", child, err)
		}
	})

	t.Run("destination product returned with error", func(t *testing.T) {
		source, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "destination-error"))
		var destinationProduct Tool
		first := mustFactoryForTool(t, newMockTool("a_destination", "a_destination"), ToolTraits{},
			func(ctx ToolBuildContext) (Tool, error) {
				created := newMockTool("a_destination", "a_destination")
				if ctx.Owner().Scope != ToolOwnerScopeRegistry {
					destinationProduct = created
				}
				return created, nil
			})
		second := mustFactoryForTool(t, newMockTool("b_destination", "b_destination"), ToolTraits{},
			func(ctx ToolBuildContext) (Tool, error) {
				if ctx.Owner().Scope != ToolOwnerScopeRegistry {
					return destinationProduct, errors.New("destination foreign error")
				}
				return newMockTool("b_destination", "b_destination"), nil
			})
		if err := source.RegisterFactory(first); err != nil {
			t.Fatal(err)
		}
		if err := source.RegisterFactory(second); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = source.Close() }()
		if child, err := source.InstantiateForOwner(
			factoryTestOwner(ToolOwnerScopeAgent, "destination-error-child"),
		); err == nil ||
			child != nil {
			t.Fatalf("destination error clone = %#v, %v", child, err)
		}
	})

	for _, test := range []struct {
		name  string
		child ToolBuildFunc
	}{
		{
			name: "value product with error",
			child: func(ToolBuildContext) (Tool, error) {
				return factoryValueTool{}, errors.New("value error")
			},
		},
		{
			name:  "nil product",
			child: func(ToolBuildContext) (Tool, error) { return nil, nil },
		},
		{
			name:  "value product",
			child: func(ToolBuildContext) (Tool, error) { return factoryValueTool{}, nil },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			name := "value_tool"
			if test.name == "nil product" {
				name = "nil_product"
			}
			source := coverageSourceWithChildFactory(t, name, test.child)
			if child, err := source.InstantiateForOwner(
				factoryTestOwner(ToolOwnerScopeAgent, strings.ReplaceAll(test.name, " ", "-")),
			); err == nil ||
				child != nil {
				t.Fatalf("invalid child product = %#v, %v", child, err)
			}
		})
	}

	t.Run("reserved error product", func(t *testing.T) {
		candidate := newMockTool("reserved_error", "reserved_error")
		identity, err := toolInstanceIdentity(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if err := globalOwnedToolInstances.reserve(identity, candidate); err != nil {
			t.Fatal(err)
		}
		defer globalOwnedToolInstances.release(identity)
		source := coverageSourceWithChildFactory(t, "reserved_error", func(ToolBuildContext) (Tool, error) {
			return candidate, errors.New("reserved error")
		})
		if child, err := source.InstantiateForOwner(
			factoryTestOwner(ToolOwnerScopeAgent, "reserved-error-child"),
		); err == nil ||
			child != nil {
			t.Fatalf("reserved error clone = %#v, %v", child, err)
		}
	})

	t.Run("media injection panic", func(t *testing.T) {
		source := coverageSourceWithChildFactory(t, "child_media_panic", func(ToolBuildContext) (Tool, error) {
			return &coveragePanicMediaTool{mockRegistryTool: newMockTool("child_media_panic", "child_media_panic")}, nil
		})
		if child, err := source.InstantiateForOwner(
			factoryTestOwner(ToolOwnerScopeAgent, "media-panic-child"),
		); err == nil ||
			child != nil {
			t.Fatalf("media panic clone = %#v, %v", child, err)
		}
	})

	t.Run("descriptor panic", func(t *testing.T) {
		source := coverageSourceWithChildFactory(t, "child_descriptor_panic", func(ToolBuildContext) (Tool, error) {
			return &factoryMetadataTool{panicNow: true}, nil
		})
		if child, err := source.InstantiateForOwner(
			factoryTestOwner(ToolOwnerScopeAgent, "descriptor-panic-child"),
		); err == nil ||
			child != nil {
			t.Fatalf("descriptor panic clone = %#v, %v", child, err)
		}
	})

	t.Run("source version changes", func(t *testing.T) {
		var source *ToolRegistry
		source = coverageSourceWithChildFactory(t, "source_version", func(ToolBuildContext) (Tool, error) {
			source.RegisterHidden(newMockTool("late_tool", "late_tool"))
			return newMockTool("source_version", "source_version"), nil
		})
		if child, err := source.InstantiateForOwner(
			factoryTestOwner(ToolOwnerScopeAgent, "source-version-child"),
		); err == nil ||
			child != nil ||
			!strings.Contains(err.Error(), "changed") {
			t.Fatalf("source version clone = %#v, %v", child, err)
		}
	})

	t.Run("destination service changes", func(t *testing.T) {
		source := coverageSourceWithChildFactory(t, "destination_service", func(ctx ToolBuildContext) (Tool, error) {
			if _, err := ctx.Service("destination-conflict", func() (any, error) {
				return &factoryService{id: 1}, nil
			}); err != nil {
				return nil, err
			}
			destination := destinationRegistryForBuild(ctx)
			destination.services.mu.Lock()
			destination.services.values["destination-conflict"] = &factoryService{id: 2}
			destination.services.mu.Unlock()
			return newMockTool("destination_service", "destination_service"), nil
		})
		if child, err := source.InstantiateForOwner(
			factoryTestOwner(ToolOwnerScopeAgent, "destination-service-child"),
		); err == nil ||
			child != nil {
			t.Fatalf("destination service clone = %#v, %v", child, err)
		}
	})
}

func TestToolRegistryFactoryCoverageExactSourceEntryFence(t *testing.T) {
	source, sourceErr := NewOwnedToolRegistry(factoryTestOwner(
		ToolOwnerScopeRegistry,
		"full-source-entry-fence",
	))
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
	factory := mustFactoryForTool(t, newMockTool("full_pointer_fence", "full pointer fence"), ToolTraits{},
		func(ctx ToolBuildContext) (Tool, error) {
			if ctx.Owner().Scope != ToolOwnerScopeRegistry {
				source.mu.Lock()
				current := source.tools["full_pointer_fence"]
				replacement := *current
				source.tools["full_pointer_fence"] = &replacement
				source.mu.Unlock()
			}
			return newMockTool("full_pointer_fence", "full pointer fence"), nil
		})
	if registerErr := source.RegisterFactory(factory); registerErr != nil {
		t.Fatal(registerErr)
	}
	t.Cleanup(func() { _ = source.Close() })
	if child, instantiateErr := source.InstantiateForOwner(
		factoryTestOwner(ToolOwnerScopeAgent, "full-source-entry-fence-child"),
	); instantiateErr == nil || child != nil || !strings.Contains(instantiateErr.Error(), "source tool") {
		t.Fatalf("full source entry fence = %#v, %v", child, instantiateErr)
	}
}

func TestToolRegistryFactoryCoverageIgnoresDormantLegacyCatalogEntry(t *testing.T) {
	source, sourceErr := NewOwnedToolRegistry(factoryTestOwner(
		ToolOwnerScopeRegistry,
		"legacy-catalog-source",
	))
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
	legacy := newMockTool("legacy_catalog", "legacy catalog")
	source.constructionCatalog["legacy_catalog"] = &ToolEntry{Tool: legacy}
	child, instantiateErr := source.InstantiateForOwner(
		factoryTestOwner(ToolOwnerScopeAgent, "legacy-catalog-child"),
	)
	if instantiateErr != nil || child == nil || child.Count() != 0 ||
		len(child.constructionCatalog) != 0 {
		t.Fatalf("legacy dormant catalog = %#v, %v", child, instantiateErr)
	}
	_ = child.Close()
	_ = source.Close()
}

func TestToolRegistryFactoryCoverageConstructionFailureQuarantine(t *testing.T) {
	for _, test := range []struct {
		name       string
		closePanic bool
		closeErr   error
	}{
		{name: "close error", closeErr: errors.New("close uncertain")},
		{name: "close panic", closePanic: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			closed := []string{}
			candidate := &factoryCloseTool{
				mockRegistryTool: newMockTool("quarantine_error", "quarantine_error"),
				label:            "candidate", closed: &closed, closePanic: test.closePanic, closeErr: test.closeErr,
			}
			identity, err := toolInstanceIdentity(candidate)
			if err != nil {
				t.Fatal(err)
			}
			defer globalOwnedToolInstances.release(identity)
			registry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "quarantine-"+test.name))
			factory := mustFactoryForTool(t, candidate, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
				return candidate, errors.New("construction failed")
			})
			if err := registry.RegisterFactory(factory); err == nil {
				t.Fatal("construction failure unexpectedly succeeded")
			}
			if len(closed) != 1 {
				t.Fatalf("candidate Close calls = %v", closed)
			}
			if err := globalOwnedToolInstances.reserve(identity, candidate); err == nil {
				globalOwnedToolInstances.release(identity)
				t.Fatal("uncertain cleanup released the product reservation")
			}
		})
	}

	valueErr := cleanupFactoryCallError(factoryValueTool{}, newToolServiceTransaction(nil), errors.New("value failed"))
	if valueErr == nil || !strings.Contains(valueErr.Error(), "non-nil pointer") {
		t.Fatalf("value cleanup error = %v", valueErr)
	}
	foreign := newMockTool("foreign_cleanup", "foreign_cleanup")
	identity, _ := toolInstanceIdentity(foreign)
	if err := globalOwnedToolInstances.reserve(identity, foreign); err != nil {
		t.Fatal(err)
	}
	if err := cleanupFactoryCallError(
		foreign,
		newToolServiceTransaction(nil),
		errors.New("foreign failed"),
	); err == nil {
		t.Fatal("foreign cleanup error disappeared")
	}
	globalOwnedToolInstances.release(identity)
}

func TestToolRegistryFactoryCoverageFrozenHiddenAndCompatibilityOffsets(t *testing.T) {
	params := map[string]any{"type": "object", "properties": map[string]any{
		"value": map[string]any{"type": "string"},
	}}
	prototype := &factoryMetadataTool{
		name: "frozen_hidden", desc: "frozen hidden description", params: params,
		metadata: PromptMetadata{Slot: ToolPromptSlotMCP, Source: "coverage:hidden"},
	}
	factory := mustFactoryForTool(t, prototype, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return &factoryMetadataTool{
			name: "frozen_hidden", desc: "frozen hidden description", params: params,
			metadata: PromptMetadata{Slot: ToolPromptSlotMCP, Source: "coverage:hidden"},
		}, nil
	})
	registry, _ := NewOwnedToolRegistry(factoryTestOwner(ToolOwnerScopeRegistry, "frozen-hidden"))
	if err := registry.RegisterHiddenFactory(factory); err != nil {
		t.Fatal(err)
	}
	liveRaw, _ := registry.GetRegistered("frozen_hidden")
	live := liveRaw.(*factoryMetadataTool)
	live.mu.Lock()
	live.panicNow = true
	live.mu.Unlock()

	snapshot := registry.SnapshotHiddenTools()
	if len(snapshot.Docs) != 1 || snapshot.Docs[0].Description != "frozen hidden description" {
		t.Fatalf("frozen hidden snapshot = %#v", snapshot)
	}
	regex, err := registry.SearchRegex("frozen hidden", 1)
	if err != nil || len(regex) != 1 || regex[0].Name != "frozen_hidden" {
		t.Fatalf("frozen regex = %#v, %v", regex, err)
	}
	bm25 := registry.SearchBM25("frozen hidden", 1)
	if len(bm25) != 1 || bm25[0].Name != "frozen_hidden" {
		t.Fatalf("frozen BM25 = %#v", bm25)
	}
	registry.PromoteTools([]string{"frozen_hidden"}, 1)
	if definitions := registry.GetDefinitions(); len(definitions) != 1 {
		t.Fatalf("frozen hidden definitions = %#v", definitions)
	}
	if summaries := registry.GetSummaries(); len(summaries) != 1 || !strings.Contains(summaries[0], "frozen hidden") {
		t.Fatalf("frozen hidden summaries = %#v", summaries)
	}

	discoveryFactory := NewRegexSearchToolFactory(1, 1)
	createdTool, createErr := discoveryFactory.New(ToolBuildContext{})
	if createErr == nil || createdTool != nil {
		t.Fatalf("inactive discovery factory = %#v, %v", createdTool, createErr)
	}
	emptyRegistry := NewToolRegistry()
	if result := NewBM25SearchTool(emptyRegistry, 1, 1).Execute(context.Background(), map[string]any{
		"query": "nothing",
	}); result.IsError || !strings.Contains(result.ForLLM, "No tools") {
		t.Fatalf("empty BM25 result = %#v", result)
	}
	if results := emptyRegistry.SearchBM25("nothing", 1); len(results) != 0 {
		t.Fatalf("empty direct BM25 results = %#v", results)
	}

	execCompat := NewCodexExecCommandTool(nil)
	if execCompat.Name() != "exec_command" || execCompat.Description() == "" ||
		execCompat.Parameters()["type"] != "object" {
		t.Fatal("exec compatibility metadata is incomplete")
	}
	if result := execCompat.Execute(context.Background(), map[string]any{"cmd": "true"}); !result.IsError {
		t.Fatal("nil exec backend was accepted")
	}
	if result := (&CodexExecCommandTool{exec: &ExecTool{}}).Execute(
		context.Background(),
		map[string]any{},
	); !result.IsError {
		t.Fatal("missing command was accepted")
	}
	execBackend, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	execCompat = NewCodexExecCommandTool(execBackend)
	if result := execCompat.Execute(context.Background(), map[string]any{
		"cmd": "printf coverage", "workdir": t.TempDir(), "background": false,
		"tty": false, "timeout": "1",
	}); result.IsError {
		t.Fatalf("mapped exec command failed: %s", result.ForLLM)
	}
	if result := execCompat.Execute(context.Background(), map[string]any{
		"cmd": "printf yield", "yield_time_ms": 1,
	}); result.IsError {
		t.Fatalf("yield-mapped exec command failed: %s", result.ForLLM)
	}
	stdinCompat := NewCodexWriteStdinTool(nil)
	if stdinCompat.Name() != "write_stdin" || stdinCompat.Description() == "" ||
		stdinCompat.Parameters()["type"] != "object" {
		t.Fatal("stdin compatibility metadata is incomplete")
	}
	if result := stdinCompat.Execute(context.Background(), map[string]any{"session_id": "x"}); !result.IsError {
		t.Fatal("nil stdin backend was accepted")
	}
	if result := (&CodexWriteStdinTool{exec: &ExecTool{}}).Execute(
		context.Background(),
		map[string]any{},
	); !result.IsError {
		t.Fatal("missing session id was accepted")
	}
	imageCompat := NewCodexViewImageTool(nil)
	if imageCompat.Name() != "view_image" || imageCompat.Description() == "" ||
		imageCompat.Parameters()["type"] != "object" {
		t.Fatal("image compatibility metadata is incomplete")
	}
	if result := imageCompat.Execute(context.Background(), map[string]any{"path": "x"}); !result.IsError {
		t.Fatal("nil image backend was accepted")
	}

	plan := NewUpdatePlanTool()
	for _, args := range []map[string]any{
		{},
		{"plan": []any{"not an object"}},
		{"plan": []any{map[string]any{"status": "pending"}}},
		{"plan": []any{map[string]any{"step": "x", "status": "invalid"}}},
	} {
		if result := plan.Execute(context.Background(), args); !result.IsError {
			t.Fatalf("invalid plan was accepted: %#v", args)
		}
	}
	var stringer strings.Builder
	stringer.WriteString("text")
	if value := compatStringArg(map[string]any{"value": fmt.Stringer(&stringer)}, "value"); value == "" {
		t.Fatal("Stringer compatibility conversion failed")
	}
	for _, value := range []any{1, int64(2), float64(3), true} {
		if compatStringArg(map[string]any{"value": value}, "value") == "" {
			t.Fatalf("scalar compatibility conversion failed: %T", value)
		}
	}
	if compatStringArg(map[string]any{"value": struct{}{}}, "value") != "" {
		t.Fatal("unsupported string conversion succeeded")
	}
	if value, ok := compatBoolArg(map[string]any{"value": true}, "value"); !ok || !value {
		t.Fatal("bool compatibility conversion failed")
	}
	if value, ok := compatBoolArg(map[string]any{"value": " false "}, "value"); !ok || value {
		t.Fatal("string bool compatibility conversion failed")
	}
	if _, ok := compatBoolArg(map[string]any{"value": 1}, "value"); ok {
		t.Fatal("unsupported bool conversion succeeded")
	}
	for _, value := range []any{1, int64(2), float64(3), "4"} {
		if _, ok := compatIntArg(map[string]any{"value": value}, "value"); !ok {
			t.Fatalf("integer compatibility conversion failed: %T", value)
		}
	}
	if _, ok := compatIntArg(map[string]any{"value": true}, "value"); ok {
		t.Fatal("unsupported integer conversion succeeded")
	}
}

func TestToolRegistryFactoryCoverageRegistryCompatibilityEdges(t *testing.T) {
	var nilRegistry *ToolRegistry
	if _, ok := nilRegistry.Owner(); ok {
		t.Fatal("nil registry reported an owner")
	}
	if _, ok := nilRegistry.Traits("missing"); ok {
		t.Fatal("nil registry reported traits")
	}
	if err := nilRegistry.Close(); err != nil {
		t.Fatal(err)
	}

	registry := NewToolRegistry()
	registry.SetAllowlist(nil)
	if !registry.AllowsRegistration("anything") {
		t.Fatal("nil allowlist did not allow registration")
	}
	if _, ok := registry.GetRegistered("missing"); ok {
		t.Fatal("missing registered tool was found")
	}
	core := newMockTool("core_coverage", "core coverage")
	hidden := newMockTool("hidden_coverage", "hidden coverage")
	registry.Register(core)
	registry.RegisterHidden(hidden)
	if got, ok := registry.GetCoreTool("core_coverage"); !ok || got != core {
		t.Fatal("core lookup failed")
	}
	if _, ok := registry.GetCoreTool("hidden_coverage"); ok {
		t.Fatal("hidden tool appeared as core")
	}
	registry.PromoteTools([]string{"hidden_coverage"}, 1)
	if got := registry.GetAll(); len(got) != 2 {
		t.Fatalf("GetAll() = %#v", got)
	}
	registry.TickTTL()
	if got := registry.GetAll(); len(got) != 1 || got[0] != core {
		t.Fatalf("expired GetAll() = %#v", got)
	}

	descriptor := coverageFactoryDescriptor("descriptor_clone")
	registry.tools["descriptor_clone"] = &ToolEntry{
		Tool: newMockTool("descriptor_clone", "descriptor_clone"), IsCore: true,
		descriptor: &descriptor, traits: conservativeLegacyToolTraits(),
	}
	clone := registry.Clone()
	clone.tools["descriptor_clone"].descriptor.Parameters["type"] = "mutated"
	if registry.tools["descriptor_clone"].descriptor.Parameters["type"] != "object" {
		t.Fatal("Clone shared frozen descriptor state")
	}

	if err := registry.VisitCoreTools(nil, func(CoreToolSnapshotEntry) bool { return false }); err != nil {
		t.Fatalf("early visitor stop = %v", err)
	}
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if err := registry.VisitCoreTools(
		canceledContext,
		func(CoreToolSnapshotEntry) bool { return true },
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled visitor error = %v", err)
	}
	if err := registry.VisitCoreTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}
