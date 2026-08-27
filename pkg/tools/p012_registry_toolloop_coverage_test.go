package tools

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type p012CoverageTool struct {
	name        string
	description string
	parameters  map[string]any
	execute     func(context.Context, map[string]any) *ToolResult

	panicDescription bool
	describeEntered  chan struct{}
	describeRelease  chan struct{}
	describeOnce     sync.Once
	calls            atomic.Int64
}

func (tool *p012CoverageTool) Name() string { return tool.name }

func (tool *p012CoverageTool) Description() string {
	if tool.describeEntered != nil {
		tool.describeOnce.Do(func() { close(tool.describeEntered) })
		<-tool.describeRelease
	}
	if tool.panicDescription {
		panic("descriptor panic")
	}
	return tool.description
}

func (tool *p012CoverageTool) Parameters() map[string]any { return tool.parameters }

func (tool *p012CoverageTool) Execute(
	ctx context.Context,
	arguments map[string]any,
) *ToolResult {
	tool.calls.Add(1)
	if tool.execute != nil {
		return tool.execute(ctx, arguments)
	}
	return NewToolResult(tool.name + " result")
}

func p012NewCoverageTool(name string) *p012CoverageTool {
	return &p012CoverageTool{
		name: name, description: name + " description",
		parameters: map[string]any{"type": "object"},
	}
}

type p012CoverageMapTool map[string]int

func (p012CoverageMapTool) Name() string               { return "map_tool" }
func (p012CoverageMapTool) Description() string        { return "map tool" }
func (p012CoverageMapTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (p012CoverageMapTool) Execute(context.Context, map[string]any) *ToolResult {
	return NewToolResult("map result")
}

type p012CoverageProviderFunc func(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error)

func (provider p012CoverageProviderFunc) Chat(
	ctx context.Context,
	messages []providers.Message,
	definitions []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	return provider(ctx, messages, definitions, model, options)
}

type p012CoveragePolicyFunc func(
	context.Context,
	ToolPolicyRequest,
) (ToolPolicyDecision, error)

func (policy p012CoveragePolicyFunc) EvaluateTool(
	ctx context.Context,
	request ToolPolicyRequest,
) (ToolPolicyDecision, error) {
	return policy(ctx, request)
}

func p012CoverageAllow() ToolPolicyDecision {
	return ToolPolicyDecision{Kind: ToolPolicyDecisionAllow, ReasonCode: "coverage_allow"}
}

func p012CoverageCall(id, name string, arguments map[string]any) providers.ToolCall {
	return providers.ToolCall{ID: id, Name: name, Arguments: arguments}
}

func p012PrepareCoverageInvocation(
	t *testing.T,
	registry *ToolRegistry,
	name string,
	arguments map[string]any,
) (*ModelToolCatalog, *PreparedToolInvocation) {
	t.Helper()
	catalog, err := registry.SnapshotModelToolCatalog()
	if err != nil {
		t.Fatalf("SnapshotModelToolCatalog() error = %v", err)
	}
	invocation, err := catalog.PrepareInvocation(name, arguments)
	if err != nil {
		t.Fatalf("PrepareInvocation(%q) error = %v", name, err)
	}
	return catalog, invocation
}

func p012ClaimCoverageInvocation(
	t *testing.T,
	registry *ToolRegistry,
	name string,
) (*PreparedToolInvocation, *ClaimedToolInvocation) {
	t.Helper()
	_, invocation := p012PrepareCoverageInvocation(t, registry, name, nil)
	claim, err := registry.ClaimPrepared(context.Background(), invocation)
	if err != nil {
		t.Fatalf("ClaimPrepared(%q) error = %v", name, err)
	}
	return invocation, claim
}

func TestP012CoverageCatalogRejectsUnavailableAndMalformedState(t *testing.T) {
	t.Run("nil and closed registries", func(t *testing.T) {
		var nilRegistry *ToolRegistry
		if catalog, err := nilRegistry.SnapshotModelToolCatalog(); catalog != nil ||
			!errors.Is(err, ErrToolCatalogUnavailable) {
			t.Fatalf("nil registry snapshot = %#v, %v", catalog, err)
		}

		closed := NewToolRegistry()
		closed.mu.Lock()
		closed.closed = true
		closed.mu.Unlock()
		if catalog, err := closed.SnapshotModelToolCatalog(); catalog != nil ||
			!errors.Is(err, ErrToolCatalogUnavailable) {
			t.Fatalf("closed registry snapshot = %#v, %v", catalog, err)
		}
	})

	t.Run("non-callable entries are omitted", func(t *testing.T) {
		registry := NewToolRegistry()
		var typedNil *p012CoverageTool
		registry.mu.Lock()
		registry.tools["nil_entry"] = nil
		registry.tools["typed_nil"] = &ToolEntry{Tool: typedNil, IsCore: true}
		registry.tools["hidden"] = &ToolEntry{
			Tool: p012NewCoverageTool("hidden"), IsCore: false, TTL: 0,
		}
		registry.tools["visible"] = &ToolEntry{
			Tool: p012NewCoverageTool("visible"), IsCore: true,
			traits: conservativeLegacyToolTraits(),
		}
		registry.mu.Unlock()

		catalog, err := registry.SnapshotModelToolCatalog()
		if err != nil {
			t.Fatalf("SnapshotModelToolCatalog() error = %v", err)
		}
		if got := catalog.Names(); len(got) != 1 || got[0] != "visible" {
			t.Fatalf("catalog names = %v, want [visible]", got)
		}
	})

	tests := []struct {
		name    string
		arrange func(*ToolRegistry)
		want    string
	}{
		{
			name: "descriptor panic",
			arrange: func(registry *ToolRegistry) {
				tool := p012NewCoverageTool("panic_descriptor")
				registry.Register(tool)
				tool.panicDescription = true
			},
			want: "descriptor panic",
		},
		{
			name: "invalid descriptor schema",
			arrange: func(registry *ToolRegistry) {
				tool := p012NewCoverageTool("invalid_schema")
				registry.Register(tool)
				cycle := map[string]any{}
				cycle["cycle"] = cycle
				tool.parameters = cycle
			},
			want: "describe tool",
		},
		{
			name: "descriptor name mismatch",
			arrange: func(registry *ToolRegistry) {
				tool := p012NewCoverageTool("registered_name")
				registry.Register(tool)
				tool.name = "changed_name"
			},
			want: "does not match registry key",
		},
		{
			name: "invalid traits",
			arrange: func(registry *ToolRegistry) {
				tool := p012NewCoverageTool("bad_traits")
				registry.Register(tool)
				registry.mu.Lock()
				registry.tools[tool.name].traits.Risk = ToolRiskClass("impossible")
				registry.mu.Unlock()
			},
			want: "normalize traits",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewToolRegistry()
			test.arrange(registry)
			catalog, err := registry.SnapshotModelToolCatalog()
			if catalog != nil || !errors.Is(err, ErrToolCatalogUnavailable) ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("SnapshotModelToolCatalog() = %#v, %v", catalog, err)
			}
		})
	}
}

func TestP012CoverageCatalogDetectsConcurrentSnapshotMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ToolRegistry, *p012CoverageTool)
		want   string
	}{
		{
			name: "generation changed",
			mutate: func(registry *ToolRegistry, _ *p012CoverageTool) {
				registry.Register(p012NewCoverageTool("new_generation"))
			},
			want: "registry changed while snapshotting",
		},
		{
			name: "captured entry changed without generation",
			mutate: func(registry *ToolRegistry, tool *p012CoverageTool) {
				registry.mu.Lock()
				registry.tools[tool.name].traits.Risk = ToolRiskMutation
				registry.mu.Unlock()
			},
			want: "tool \"blocking\" changed while snapshotting",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			tool := p012NewCoverageTool("blocking")
			tool.describeEntered = entered
			tool.describeRelease = release
			registry := NewToolRegistry()
			registry.Register(tool)

			type outcome struct {
				catalog *ModelToolCatalog
				err     error
			}
			finished := make(chan outcome, 1)
			go func() {
				catalog, err := registry.SnapshotModelToolCatalog()
				finished <- outcome{catalog: catalog, err: err}
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("snapshot did not reach descriptor boundary")
			}
			test.mutate(registry, tool)
			close(release)
			select {
			case result := <-finished:
				if result.catalog != nil || !errors.Is(result.err, ErrToolCatalogUnavailable) ||
					!strings.Contains(result.err.Error(), test.want) {
					t.Fatalf("snapshot = %#v, %v", result.catalog, result.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("snapshot did not finish")
			}
		})
	}
}

func TestP012CoverageCatalogAccessorsAndPreparationFailures(t *testing.T) {
	var nilCatalog *ModelToolCatalog
	if nilCatalog.ProviderDefinitions() != nil || nilCatalog.Contains("anything") ||
		nilCatalog.Names() != nil {
		t.Fatal("nil catalog accessors returned data")
	}
	if invocation, err := nilCatalog.PrepareInvocation("anything", nil); invocation != nil ||
		!errors.Is(err, ErrToolCatalogUnavailable) {
		t.Fatalf("nil catalog PrepareInvocation() = %#v, %v", invocation, err)
	}

	var nilInvocation *PreparedToolInvocation
	if nilInvocation.Name() != "" || nilInvocation.Traits() != (ToolTraits{}) {
		t.Fatal("nil invocation accessors returned data")
	}
	if arguments, err := nilInvocation.PolicyArguments(); arguments != nil ||
		!errors.Is(err, ErrToolInvocationStale) {
		t.Fatalf("nil PolicyArguments() = %#v, %v", arguments, err)
	}
	if err := nilInvocation.ValidateOfferedDefinition(providers.ToolDefinition{}); !errors.Is(
		err,
		ErrToolInvocationStale,
	) {
		t.Fatalf("nil ValidateOfferedDefinition() error = %v", err)
	}

	registry := NewToolRegistry()
	tool := p012NewCoverageTool("prepared")
	tool.parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		},
		"required":             []any{"value"},
		"additionalProperties": false,
	}
	registry.Register(tool)
	catalog, err := registry.SnapshotModelToolCatalog()
	if err != nil {
		t.Fatalf("SnapshotModelToolCatalog() error = %v", err)
	}

	if invocation, prepareErr := catalog.PrepareInvocation("missing", nil); invocation != nil ||
		!errors.Is(prepareErr, workflows.ErrToolCallNotDispatched) {
		t.Fatalf("missing PrepareInvocation() = %#v, %v", invocation, prepareErr)
	}
	cycle := map[string]any{}
	cycle["cycle"] = cycle
	if invocation, prepareErr := catalog.PrepareInvocation("prepared", cycle); invocation != nil ||
		!errors.Is(prepareErr, workflows.ErrToolCallNotDispatched) {
		t.Fatalf("cyclic PrepareInvocation() = %#v, %v", invocation, prepareErr)
	}
	if invocation, prepareErr := catalog.PrepareInvocation(
		"prepared",
		map[string]any{"value": 7},
	); invocation != nil || !errors.Is(prepareErr, workflows.ErrToolCallNotDispatched) {
		t.Fatalf("schema-invalid PrepareInvocation() = %#v, %v", invocation, prepareErr)
	}

	invocation, err := catalog.PrepareInvocation("prepared", map[string]any{"value": "ok"})
	if err != nil {
		t.Fatalf("valid PrepareInvocation() error = %v", err)
	}
	wrongName := providers.ToolDefinition{Function: providers.ToolFunctionDefinition{
		Name: "other", Parameters: tool.parameters,
	}}
	if err := invocation.ValidateOfferedDefinition(wrongName); !errors.Is(
		err,
		workflows.ErrToolCallNotDispatched,
	) {
		t.Fatalf("wrong-name offered validation error = %v", err)
	}
	narrowed := providers.ToolDefinition{Function: providers.ToolFunctionDefinition{
		Name: "prepared",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{"type": "integer"},
			},
		},
	}}
	if err := invocation.ValidateOfferedDefinition(narrowed); !errors.Is(
		err,
		workflows.ErrToolCallNotDispatched,
	) {
		t.Fatalf("narrowed offered validation error = %v", err)
	}

	registry.Unregister("prepared")
	if stale, err := catalog.PrepareInvocation("prepared", map[string]any{"value": "ok"}); stale != nil ||
		!errors.Is(err, ErrToolInvocationStale) {
		t.Fatalf("stale PrepareInvocation() = %#v, %v", stale, err)
	}

	cyclicPolicy := map[string]any{}
	cyclicPolicy["cycle"] = cyclicPolicy
	corrupt := &PreparedToolInvocation{policyArguments: cyclicPolicy}
	if arguments, err := corrupt.PolicyArguments(); arguments != nil || err == nil {
		t.Fatalf("corrupt PolicyArguments() = %#v, %v", arguments, err)
	}
}

func TestP012CoverageSameToolBindingKinds(t *testing.T) {
	var typedNil *p012CoverageTool
	if sameToolBinding(typedNil, p012NewCoverageTool("other")) {
		t.Fatal("typed nil unexpectedly matched")
	}
	if sameToolBinding(p012NewCoverageTool("one"), p012CoverageMapTool{"one": 1}) {
		t.Fatal("different tool types unexpectedly matched")
	}
	sharedMap := p012CoverageMapTool{"shared": 1}
	if !sameToolBinding(sharedMap, sharedMap) {
		t.Fatal("same map binding did not match")
	}
	if sameToolBinding(sharedMap, p012CoverageMapTool{"shared": 1}) {
		t.Fatal("distinct map bindings unexpectedly matched")
	}
}

func TestP012CoverageClaimPreparedGuards(t *testing.T) {
	t.Run("nil foreign and stale", func(t *testing.T) {
		registry := NewToolRegistry()
		if claim, err := registry.ClaimPrepared(context.Background(), nil); claim != nil ||
			!errors.Is(err, ErrToolInvocationStale) {
			t.Fatalf("nil ClaimPrepared() = %#v, %v", claim, err)
		}

		tool := p012NewCoverageTool("foreign_claim")
		registry.Register(tool)
		_, invocation := p012PrepareCoverageInvocation(t, registry, tool.name, nil)
		foreign := NewToolRegistry()
		if claim, err := foreign.ClaimPrepared(context.Background(), invocation); claim != nil ||
			!errors.Is(err, ErrToolInvocationStale) {
			t.Fatalf("foreign ClaimPrepared() = %#v, %v", claim, err)
		}

		registry.Unregister(tool.name)
		if claim, err := registry.ClaimPrepared(context.Background(), invocation); claim != nil ||
			!errors.Is(err, ErrToolInvocationStale) {
			t.Fatalf("stale ClaimPrepared() = %#v, %v", claim, err)
		}
	})

	t.Run("nil context consumes invocation", func(t *testing.T) {
		registry := NewToolRegistry()
		tool := p012NewCoverageTool("nil_context_claim")
		registry.Register(tool)
		_, invocation := p012PrepareCoverageInvocation(t, registry, tool.name, nil)
		if claim, err := registry.ClaimPrepared(nil, invocation); claim != nil ||
			!errors.Is(err, ErrToolInvocationStale) {
			t.Fatalf("nil-context ClaimPrepared() = %#v, %v", claim, err)
		}
		if _, err := registry.ClaimPrepared(context.Background(), invocation); !errors.Is(
			err,
			ErrToolInvocationUsed,
		) {
			t.Fatalf("reused ClaimPrepared() error = %v", err)
		}
	})

	t.Run("canceled context consumes invocation", func(t *testing.T) {
		registry := NewToolRegistry()
		tool := p012NewCoverageTool("canceled_claim")
		registry.Register(tool)
		_, invocation := p012PrepareCoverageInvocation(t, registry, tool.name, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if claim, err := registry.ClaimPrepared(ctx, invocation); claim != nil ||
			!errors.Is(err, context.Canceled) {
			t.Fatalf("canceled ClaimPrepared() = %#v, %v", claim, err)
		}
	})

	t.Run("successful claim is single use", func(t *testing.T) {
		registry := NewToolRegistry()
		tool := p012NewCoverageTool("single_claim")
		registry.Register(tool)
		_, invocation := p012PrepareCoverageInvocation(t, registry, tool.name, nil)
		claim, err := registry.ClaimPrepared(context.Background(), invocation)
		if err != nil || claim == nil {
			t.Fatalf("ClaimPrepared() = %#v, %v", claim, err)
		}
		if duplicate, err := registry.ClaimPrepared(context.Background(), invocation); duplicate != nil ||
			!errors.Is(err, ErrToolInvocationUsed) {
			t.Fatalf("duplicate ClaimPrepared() = %#v, %v", duplicate, err)
		}
	})
}

func TestP012CoverageDispatchClaimedGuards(t *testing.T) {
	registry := NewToolRegistry()
	if result, err := registry.DispatchClaimed(context.Background(), nil, "", "", nil, true); result != nil ||
		!errors.Is(err, ErrToolInvocationStale) {
		t.Fatalf("nil DispatchClaimed() = %#v, %v", result, err)
	}

	t.Run("foreign then successful and reused", func(t *testing.T) {
		registry := NewToolRegistry()
		tool := p012NewCoverageTool("dispatch_once")
		registry.Register(tool)
		_, claim := p012ClaimCoverageInvocation(t, registry, tool.name)
		foreign := NewToolRegistry()
		if result, err := foreign.DispatchClaimed(
			context.Background(), claim, "", "", nil, true,
		); result != nil || !errors.Is(err, ErrToolInvocationStale) {
			t.Fatalf("foreign DispatchClaimed() = %#v, %v", result, err)
		}
		result, err := registry.DispatchClaimed(
			context.Background(), claim, "cli", "chat", nil, true,
		)
		if err != nil || result == nil || result.IsError || tool.calls.Load() != 1 {
			t.Fatalf("successful DispatchClaimed() = %#v, %v; calls=%d", result, err, tool.calls.Load())
		}
		if duplicate, err := registry.DispatchClaimed(
			context.Background(), claim, "", "", nil, true,
		); duplicate != nil || !errors.Is(err, ErrToolInvocationUsed) {
			t.Fatalf("duplicate DispatchClaimed() = %#v, %v", duplicate, err)
		}
	})

	t.Run("nil context consumes claim", func(t *testing.T) {
		registry := NewToolRegistry()
		tool := p012NewCoverageTool("nil_dispatch_context")
		registry.Register(tool)
		_, claim := p012ClaimCoverageInvocation(t, registry, tool.name)
		if result, err := registry.DispatchClaimed(nil, claim, "", "", nil, true); result != nil ||
			!errors.Is(err, ErrToolInvocationStale) {
			t.Fatalf("nil-context DispatchClaimed() = %#v, %v", result, err)
		}
		if _, err := registry.DispatchClaimed(
			context.Background(), claim, "", "", nil, true,
		); !errors.Is(err, ErrToolInvocationUsed) {
			t.Fatalf("reused nil-context claim error = %v", err)
		}
	})

	t.Run("canceled context consumes claim", func(t *testing.T) {
		registry := NewToolRegistry()
		tool := p012NewCoverageTool("canceled_dispatch_context")
		registry.Register(tool)
		_, claim := p012ClaimCoverageInvocation(t, registry, tool.name)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if result, err := registry.DispatchClaimed(ctx, claim, "", "", nil, true); result != nil ||
			!errors.Is(err, context.Canceled) {
			t.Fatalf("canceled DispatchClaimed() = %#v, %v", result, err)
		}
	})
}

func TestP012CoverageToolLoopCatalogAndProtocolFailures(t *testing.T) {
	notCalled := p012CoverageProviderFunc(func(
		context.Context,
		[]providers.Message,
		[]providers.ToolDefinition,
		string,
		map[string]any,
	) (*providers.LLMResponse, error) {
		t.Fatal("provider unexpectedly called")
		return nil, nil
	})

	t.Run("closed catalog", func(t *testing.T) {
		registry := NewToolRegistry()
		registry.mu.Lock()
		registry.closed = true
		registry.mu.Unlock()
		result, err := RunToolLoop(context.Background(), ToolLoopConfig{
			Provider: notCalled, Tools: registry, Policy: CompatibilityAllowToolPolicy{}, MaxIterations: 1,
		}, nil, "", "")
		if result != nil || !errors.Is(err, ErrToolCatalogUnavailable) {
			t.Fatalf("closed-catalog RunToolLoop() = %#v, %v", result, err)
		}
	})

	t.Run("invalid offered name", func(t *testing.T) {
		registry := NewToolRegistry()
		registry.Register(p012NewCoverageTool(strings.Repeat("n", MaxToolPolicyNameLen+1)))
		result, err := RunToolLoop(context.Background(), ToolLoopConfig{
			Provider: notCalled, Tools: registry, Policy: CompatibilityAllowToolPolicy{}, MaxIterations: 1,
		}, nil, "", "")
		if result != nil || err == nil || !strings.Contains(err.Error(), "invalid model tool definitions") {
			t.Fatalf("invalid-definition RunToolLoop() = %#v, %v", result, err)
		}
	})

	t.Run("arguments cannot detach", func(t *testing.T) {
		registry := NewToolRegistry()
		tool := p012NewCoverageTool("detach_failure")
		registry.Register(tool)
		provider := p012CoverageProviderFunc(func(
			context.Context,
			[]providers.Message,
			[]providers.ToolDefinition,
			string,
			map[string]any,
		) (*providers.LLMResponse, error) {
			return &providers.LLMResponse{ToolCalls: []providers.ToolCall{
				p012CoverageCall("detach-call", tool.name, map[string]any{"bad": make(chan int)}),
			}}, nil
		})
		result, err := RunToolLoop(context.Background(), ToolLoopConfig{
			Provider: provider, Tools: registry, Policy: CompatibilityAllowToolPolicy{}, MaxIterations: 1,
		}, nil, "", "")
		if result != nil || err == nil || !strings.Contains(err.Error(), "detach model tool arguments") {
			t.Fatalf("detach-failure RunToolLoop() = %#v, %v", result, err)
		}
	})
}

func TestP012CoverageToolLoopAuthorizationAndDispatchFailures(t *testing.T) {
	t.Run("schema denial becomes tool result", func(t *testing.T) {
		registry := NewToolRegistry()
		tool := p012NewCoverageTool("schema_denial")
		tool.parameters = map[string]any{
			"type":       "object",
			"properties": map[string]any{"value": map[string]any{"type": "string"}},
			"required":   []any{"value"}, "additionalProperties": false,
		}
		registry.Register(tool)
		calls := 0
		provider := p012CoverageProviderFunc(func(
			_ context.Context,
			messages []providers.Message,
			_ []providers.ToolDefinition,
			_ string,
			_ map[string]any,
		) (*providers.LLMResponse, error) {
			calls++
			if calls == 1 {
				return &providers.LLMResponse{ToolCalls: []providers.ToolCall{
					p012CoverageCall("schema-call", tool.name, map[string]any{"value": 9}),
				}}, nil
			}
			if len(messages) != 3 || messages[2].Role != "tool" ||
				!strings.Contains(messages[2].Content, "invalid tool call") {
				t.Fatalf("schema denial follow-up = %#v", messages)
			}
			return &providers.LLMResponse{Content: "done"}, nil
		})
		result, err := RunToolLoop(context.Background(), ToolLoopConfig{
			Provider: provider, Tools: registry, Policy: CompatibilityAllowToolPolicy{},
			MaxIterations: 2, SequentialToolCalls: true,
		}, []providers.Message{{Role: "user", Content: "run"}}, "", "")
		if err != nil || result == nil || result.Content != "done" || tool.calls.Load() != 0 {
			t.Fatalf("schema-denial RunToolLoop() = %#v, %v; calls=%d", result, err, tool.calls.Load())
		}
	})

	t.Run("stale during prepare", func(t *testing.T) {
		registry := NewToolRegistry()
		tool := p012NewCoverageTool("stale_prepare")
		registry.Register(tool)
		provider := p012CoverageProviderFunc(func(
			context.Context,
			[]providers.Message,
			[]providers.ToolDefinition,
			string,
			map[string]any,
		) (*providers.LLMResponse, error) {
			registry.Unregister(tool.name)
			return &providers.LLMResponse{ToolCalls: []providers.ToolCall{
				p012CoverageCall("stale-call", tool.name, nil),
			}}, nil
		})
		result, err := RunToolLoop(context.Background(), ToolLoopConfig{
			Provider: provider, Tools: registry, Policy: CompatibilityAllowToolPolicy{},
			MaxIterations: 1, SequentialToolCalls: true,
		}, nil, "", "")
		if result != nil || !errors.Is(err, ErrToolInvocationStale) || tool.calls.Load() != 0 {
			t.Fatalf("stale-prepare RunToolLoop() = %#v, %v; calls=%d", result, err, tool.calls.Load())
		}
	})

	t.Run("sequential policy error", func(t *testing.T) {
		registry := NewToolRegistry()
		tool := p012NewCoverageTool("policy_error")
		registry.Register(tool)
		provider := p012CoverageProviderFunc(func(
			context.Context,
			[]providers.Message,
			[]providers.ToolDefinition,
			string,
			map[string]any,
		) (*providers.LLMResponse, error) {
			return &providers.LLMResponse{ToolCalls: []providers.ToolCall{
				p012CoverageCall("policy-call", tool.name, nil),
			}}, nil
		})
		policy := p012CoveragePolicyFunc(func(
			context.Context,
			ToolPolicyRequest,
		) (ToolPolicyDecision, error) {
			return ToolPolicyDecision{}, errors.New("broker secret")
		})
		result, err := RunToolLoop(context.Background(), ToolLoopConfig{
			Provider: provider, Tools: registry, Policy: policy,
			MaxIterations: 1, SequentialToolCalls: true,
		}, nil, "", "")
		if result != nil || !errors.Is(err, ErrToolPolicyUnavailable) ||
			strings.Contains(err.Error(), "broker secret") || tool.calls.Load() != 0 {
			t.Fatalf("policy-error RunToolLoop() = %#v, %v; calls=%d", result, err, tool.calls.Load())
		}
	})

	t.Run("sequential claim becomes stale", func(t *testing.T) {
		registry := NewToolRegistry()
		tool := p012NewCoverageTool("stale_claim")
		registry.Register(tool)
		provider := p012CoverageProviderFunc(func(
			context.Context,
			[]providers.Message,
			[]providers.ToolDefinition,
			string,
			map[string]any,
		) (*providers.LLMResponse, error) {
			return &providers.LLMResponse{ToolCalls: []providers.ToolCall{
				p012CoverageCall("claim-call", tool.name, nil),
			}}, nil
		})
		policy := p012CoveragePolicyFunc(func(
			context.Context,
			ToolPolicyRequest,
		) (ToolPolicyDecision, error) {
			registry.Unregister(tool.name)
			return p012CoverageAllow(), nil
		})
		result, err := RunToolLoop(context.Background(), ToolLoopConfig{
			Provider: provider, Tools: registry, Policy: policy,
			MaxIterations: 1, SequentialToolCalls: true,
		}, nil, "", "")
		if result != nil || !errors.Is(err, ErrToolInvocationStale) || tool.calls.Load() != 0 {
			t.Fatalf("stale-claim RunToolLoop() = %#v, %v; calls=%d", result, err, tool.calls.Load())
		}
	})

	t.Run("parallel deny skips only denied effect", func(t *testing.T) {
		registry := NewToolRegistry()
		allowed := p012NewCoverageTool("parallel_allowed")
		denied := p012NewCoverageTool("parallel_denied")
		registry.Register(allowed)
		registry.Register(denied)
		calls := 0
		provider := p012CoverageProviderFunc(func(
			_ context.Context,
			_ []providers.Message,
			_ []providers.ToolDefinition,
			_ string,
			_ map[string]any,
		) (*providers.LLMResponse, error) {
			calls++
			if calls == 1 {
				return &providers.LLMResponse{ToolCalls: []providers.ToolCall{
					p012CoverageCall("allow-call", allowed.name, nil),
					p012CoverageCall("deny-call", denied.name, nil),
				}}, nil
			}
			return &providers.LLMResponse{Content: "complete"}, nil
		})
		policy := p012CoveragePolicyFunc(func(
			_ context.Context,
			request ToolPolicyRequest,
		) (ToolPolicyDecision, error) {
			if request.Tool == denied.name {
				return ToolPolicyDecision{Kind: ToolPolicyDecisionDeny, ReasonCode: "coverage_deny"}, nil
			}
			return p012CoverageAllow(), nil
		})
		result, err := RunToolLoop(context.Background(), ToolLoopConfig{
			Provider: provider, Tools: registry, Policy: policy, MaxIterations: 2,
		}, nil, "", "")
		if err != nil || result == nil || result.Content != "complete" ||
			allowed.calls.Load() != 1 || denied.calls.Load() != 0 {
			t.Fatalf(
				"parallel-deny RunToolLoop() = %#v, %v; allowed/denied=%d/%d",
				result, err, allowed.calls.Load(), denied.calls.Load(),
			)
		}
	})
}

func TestP012CoverageFindExactToolDefinitionMiss(t *testing.T) {
	definition := providers.ToolDefinition{Function: providers.ToolFunctionDefinition{Name: "present"}}
	if found, ok := findExactToolDefinition([]providers.ToolDefinition{definition}, "missing"); ok ||
		found.Function.Name != "" {
		t.Fatalf("findExactToolDefinition(missing) = %#v, %v", found, ok)
	}
}
