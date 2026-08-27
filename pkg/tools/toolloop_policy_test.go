package tools

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type p012ToolLoopPolicyFunc func(
	context.Context,
	ToolPolicyRequest,
) (ToolPolicyDecision, error)

func (policy p012ToolLoopPolicyFunc) EvaluateTool(
	ctx context.Context,
	request ToolPolicyRequest,
) (ToolPolicyDecision, error) {
	return policy(ctx, request)
}

type p012ToolLoopProvider struct {
	mu        sync.Mutex
	responses []*providers.LLMResponse
	mutate    func(int, []providers.ToolDefinition)
	calls     int
	messages  [][]providers.Message
}

func (provider *p012ToolLoopProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	definitions []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.mu.Lock()
	call := provider.calls
	provider.calls++
	provider.messages = append(
		provider.messages,
		append([]providers.Message(nil), messages...),
	)
	var response *providers.LLMResponse
	if call < len(provider.responses) {
		response = provider.responses[call]
	}
	mutate := provider.mutate
	provider.mu.Unlock()

	if mutate != nil {
		mutate(call, definitions)
	}
	return response, nil
}

func (provider *p012ToolLoopProvider) callCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.calls
}

func (provider *p012ToolLoopProvider) messagesAt(call int) []providers.Message {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if call < 0 || call >= len(provider.messages) {
		return nil
	}
	return append([]providers.Message(nil), provider.messages[call]...)
}

type p012ToolLoopPolicyTool struct {
	name       string
	parameters map[string]any
	execute    func(context.Context, map[string]any) *ToolResult
	calls      atomic.Int64

	mu        sync.Mutex
	arguments []map[string]any
}

func (tool *p012ToolLoopPolicyTool) Name() string { return tool.name }

func (tool *p012ToolLoopPolicyTool) Description() string {
	return "P012 tool-loop policy fixture for " + tool.name
}

func (tool *p012ToolLoopPolicyTool) Parameters() map[string]any {
	return tool.parameters
}

func (tool *p012ToolLoopPolicyTool) Execute(
	ctx context.Context,
	arguments map[string]any,
) *ToolResult {
	tool.calls.Add(1)
	detached, _ := DetachToolArguments(arguments)
	tool.mu.Lock()
	tool.arguments = append(tool.arguments, detached)
	tool.mu.Unlock()
	if tool.execute != nil {
		return tool.execute(ctx, arguments)
	}
	return NewToolResult(tool.name + "-result")
}

func (tool *p012ToolLoopPolicyTool) argumentsSnapshot() []map[string]any {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	result := make([]map[string]any, 0, len(tool.arguments))
	for _, arguments := range tool.arguments {
		detached, _ := DetachToolArguments(arguments)
		result = append(result, detached)
	}
	return result
}

func p012ClosedObjectSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}

func p012ToolCall(id, name string, arguments map[string]any) providers.ToolCall {
	return providers.ToolCall{ID: id, Name: name, Arguments: arguments}
}

func p012ScriptedProvider(calls []providers.ToolCall) *p012ToolLoopProvider {
	return &p012ToolLoopProvider{responses: []*providers.LLMResponse{
		{ToolCalls: calls},
		{Content: "done"},
	}}
}

func TestRunToolLoopPolicyAllowAndDeny(t *testing.T) {
	t.Run("allow", func(t *testing.T) {
		tool := &p012ToolLoopPolicyTool{
			name: "allow_tool", parameters: p012ClosedObjectSchema(),
		}
		registry := NewToolRegistry()
		registry.Register(tool)
		provider := p012ScriptedProvider([]providers.ToolCall{
			p012ToolCall("allow-call", tool.name, nil),
		})
		var observed ToolPolicyRequest
		result, err := RunToolLoop(
			context.Background(),
			ToolLoopConfig{
				Provider: provider,
				Model:    "policy-model",
				Tools:    registry,
				Policy: p012ToolLoopPolicyFunc(func(
					_ context.Context,
					request ToolPolicyRequest,
				) (ToolPolicyDecision, error) {
					observed = request
					return ToolPolicyDecision{
						Kind: ToolPolicyDecisionAllow, ReasonCode: "test_allow",
					}, nil
				}),
				MaxIterations:       2,
				SequentialToolCalls: true,
			},
			[]providers.Message{{Role: "user", Content: "run"}},
			"",
			"",
		)
		if err != nil || result == nil || result.Content != "done" || result.Iterations != 2 {
			t.Fatalf("RunToolLoop() = %#v, %v", result, err)
		}
		if got := tool.calls.Load(); got != 1 {
			t.Fatalf("allowed tool calls = %d, want 1", got)
		}
		if observed.Tool != tool.name || observed.Subject.ToolCallID != "allow-call" ||
			observed.Subject.Source != ToolPolicySourceGenericLoop ||
			observed.Fulfillment != ToolFulfillmentExecute {
			t.Fatalf("allowed policy request = %#v", observed)
		}
	})

	t.Run("deny", func(t *testing.T) {
		tool := &p012ToolLoopPolicyTool{
			name: "deny_tool", parameters: p012ClosedObjectSchema(),
		}
		registry := NewToolRegistry()
		registry.Register(tool)
		provider := p012ScriptedProvider([]providers.ToolCall{
			p012ToolCall("deny-call", tool.name, nil),
		})
		result, err := RunToolLoop(
			context.Background(),
			ToolLoopConfig{
				Provider: provider,
				Model:    "policy-model",
				Tools:    registry,
				Policy: p012ToolLoopPolicyFunc(func(
					context.Context,
					ToolPolicyRequest,
				) (ToolPolicyDecision, error) {
					return ToolPolicyDecision{
						Kind: ToolPolicyDecisionDeny, ReasonCode: "test_deny",
					}, nil
				}),
				MaxIterations:       2,
				SequentialToolCalls: true,
			},
			[]providers.Message{{Role: "user", Content: "run"}},
			"",
			"",
		)
		if err != nil || result == nil || result.Content != "done" {
			t.Fatalf("RunToolLoop() = %#v, %v", result, err)
		}
		if got := tool.calls.Load(); got != 0 {
			t.Fatalf("denied tool calls = %d, want 0", got)
		}
		followUp := provider.messagesAt(1)
		if len(followUp) != 3 || followUp[2].Role != "tool" ||
			followUp[2].ToolCallID != "deny-call" ||
			followUp[2].Content != "Tool execution denied by policy." {
			t.Fatalf("denial follow-up messages = %#v", followUp)
		}
	})
}

func TestRunToolLoopPolicyInfrastructureFailuresFailClosed(t *testing.T) {
	tests := []struct {
		name       string
		policy     func(context.CancelFunc) ToolPolicy
		want       error
		forbidden  string
		wantPolicy int64
	}{
		{
			name: "nil policy",
			policy: func(context.CancelFunc) ToolPolicy {
				return nil
			},
			want: ErrToolPolicyUnavailable,
		},
		{
			name: "policy error",
			policy: func(context.CancelFunc) ToolPolicy {
				return p012ToolLoopPolicyFunc(func(
					context.Context,
					ToolPolicyRequest,
				) (ToolPolicyDecision, error) {
					return ToolPolicyDecision{}, errors.New("secret broker failure")
				})
			},
			want: ErrToolPolicyUnavailable, forbidden: "secret broker failure", wantPolicy: 1,
		},
		{
			name: "policy panic",
			policy: func(context.CancelFunc) ToolPolicy {
				return p012ToolLoopPolicyFunc(func(
					context.Context,
					ToolPolicyRequest,
				) (ToolPolicyDecision, error) {
					panic("secret broker panic")
				})
			},
			want: ErrToolPolicyUnavailable, forbidden: "secret broker panic", wantPolicy: 1,
		},
		{
			name: "policy cancellation",
			policy: func(cancel context.CancelFunc) ToolPolicy {
				return p012ToolLoopPolicyFunc(func(
					context.Context,
					ToolPolicyRequest,
				) (ToolPolicyDecision, error) {
					cancel()
					return ToolPolicyDecision{
						Kind: ToolPolicyDecisionAllow, ReasonCode: "too_late",
					}, nil
				})
			},
			want: context.Canceled, wantPolicy: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tool := &p012ToolLoopPolicyTool{
				name: "closed_tool", parameters: p012ClosedObjectSchema(),
			}
			registry := NewToolRegistry()
			registry.Register(tool)
			provider := p012ScriptedProvider([]providers.ToolCall{
				p012ToolCall("closed-call", tool.name, nil),
			})
			var policyCalls atomic.Int64
			policy := test.policy(cancel)
			if policy != nil {
				underlying := policy
				policy = p012ToolLoopPolicyFunc(func(
					ctx context.Context,
					request ToolPolicyRequest,
				) (ToolPolicyDecision, error) {
					policyCalls.Add(1)
					return underlying.EvaluateTool(ctx, request)
				})
			}

			result, err := RunToolLoop(
				ctx,
				ToolLoopConfig{
					Provider:      provider,
					Model:         "policy-model",
					Tools:         registry,
					Policy:        policy,
					MaxIterations: 1,
				},
				[]providers.Message{{Role: "user", Content: "run"}},
				"",
				"",
			)
			if !errors.Is(err, test.want) || result != nil {
				t.Fatalf("RunToolLoop() = %#v, %v, want %v", result, err, test.want)
			}
			if test.forbidden != "" && strings.Contains(err.Error(), test.forbidden) {
				t.Fatalf("policy infrastructure detail leaked: %v", err)
			}
			if got := policyCalls.Load(); got != test.wantPolicy {
				t.Fatalf("policy calls = %d, want %d", got, test.wantPolicy)
			}
			if got := tool.calls.Load(); got != 0 {
				t.Fatalf("tool effects after policy failure = %d, want 0", got)
			}
			if got := provider.callCount(); got != 1 {
				t.Fatalf("provider calls after policy failure = %d, want 1", got)
			}
		})
	}
}

func TestRunToolLoopPolicyUnknownRiskStrictVersusCompatibility(t *testing.T) {
	t.Run("strict denies unknown", func(t *testing.T) {
		tool := &p012ToolLoopPolicyTool{
			name: "legacy_unknown", parameters: p012ClosedObjectSchema(),
		}
		registry := NewToolRegistry()
		registry.Register(tool)
		provider := p012ScriptedProvider([]providers.ToolCall{
			p012ToolCall("strict-call", tool.name, nil),
		})
		var risk ToolRiskClass
		result, err := RunToolLoop(
			context.Background(),
			ToolLoopConfig{
				Provider: provider,
				Model:    "policy-model",
				Tools:    registry,
				Policy: p012ToolLoopPolicyFunc(func(
					_ context.Context,
					request ToolPolicyRequest,
				) (ToolPolicyDecision, error) {
					risk = request.Traits.Risk
					if risk == ToolRiskUnknown {
						return ToolPolicyDecision{
							Kind: ToolPolicyDecisionDeny, ReasonCode: "unknown_risk",
						}, nil
					}
					return ToolPolicyDecision{
						Kind: ToolPolicyDecisionAllow, ReasonCode: "known_risk",
					}, nil
				}),
				MaxIterations:       2,
				SequentialToolCalls: true,
			},
			nil,
			"",
			"",
		)
		if err != nil || result == nil || result.Content != "done" {
			t.Fatalf("strict RunToolLoop() = %#v, %v", result, err)
		}
		if risk != ToolRiskUnknown || tool.calls.Load() != 0 {
			t.Fatalf("strict risk/effects = %q/%d, want unknown/0", risk, tool.calls.Load())
		}
	})

	t.Run("compatibility allows unknown", func(t *testing.T) {
		tool := &p012ToolLoopPolicyTool{
			name: "legacy_unknown", parameters: p012ClosedObjectSchema(),
		}
		registry := NewToolRegistry()
		registry.Register(tool)
		provider := p012ScriptedProvider([]providers.ToolCall{
			p012ToolCall("compat-call", tool.name, nil),
		})
		result, err := RunToolLoop(
			context.Background(),
			ToolLoopConfig{
				Provider:            provider,
				Model:               "policy-model",
				Tools:               registry,
				Policy:              CompatibilityAllowToolPolicy{},
				MaxIterations:       2,
				SequentialToolCalls: true,
			},
			nil,
			"",
			"",
		)
		if err != nil || result == nil || result.Content != "done" {
			t.Fatalf("compatibility RunToolLoop() = %#v, %v", result, err)
		}
		if got := tool.calls.Load(); got != 1 {
			t.Fatalf("compatibility effects = %d, want 1", got)
		}
	})
}

func TestLegacySubagentFallbackUsesManagerPolicy(t *testing.T) {
	tool := &p012ToolLoopPolicyTool{
		name: "legacy_fallback_tool", parameters: p012ClosedObjectSchema(),
	}
	registry := NewToolRegistry()
	registry.Register(tool)
	provider := p012ScriptedProvider([]providers.ToolCall{
		p012ToolCall("legacy-fallback-call", tool.name, nil),
	})
	manager := NewSubagentManager(provider, "policy-model", t.TempDir())
	if _, ok := manager.policy.(CompatibilityAllowToolPolicy); !ok {
		t.Fatalf("standalone manager default policy = %T, want explicit compatibility policy", manager.policy)
	}
	manager.SetTools(registry)
	var observed ToolPolicyRequest
	manager.SetToolPolicy(p012ToolLoopPolicyFunc(func(
		_ context.Context,
		request ToolPolicyRequest,
	) (ToolPolicyDecision, error) {
		observed = request
		return ToolPolicyDecision{
			Kind: ToolPolicyDecisionDeny, ReasonCode: "strict_parent_deny",
		}, nil
	}))

	runner := manager.legacyTaskRunnerSnapshot()
	result, err := runner(context.Background(), SubagentTask{
		ID:               "legacy-task-1",
		Task:             "run",
		Label:            "legacy-policy",
		AgentID:          "target-agent",
		OriginSessionKey: "source-session",
	})
	if err != nil || result == nil || result.IsError || !strings.Contains(result.ForLLM, "done") {
		t.Fatalf("legacy fallback result = %#v, %v", result, err)
	}
	if got := tool.calls.Load(); got != 0 {
		t.Fatalf("strict manager fallback effects = %d, want 0", got)
	}
	if observed.Subject.Source != ToolPolicySourceLegacySubagent ||
		observed.Subject.AgentID != "target-agent" ||
		observed.Subject.SessionKey != "source-session" ||
		observed.Subject.TurnID != "legacy-task-1" ||
		observed.Subject.ToolCallID != "legacy-fallback-call" {
		t.Fatalf("legacy fallback policy subject = %#v", observed.Subject)
	}
}

func TestPreparedInvocationValidatesNarrowerOfferedSchema(t *testing.T) {
	registrySchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"safe":  map[string]any{"type": "string"},
			"extra": map[string]any{"type": "string"},
		},
		"required":             []string{"safe"},
		"additionalProperties": false,
	}
	narrowSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"safe": map[string]any{"type": "string"},
		},
		"required":             []string{"safe"},
		"additionalProperties": false,
	}
	tool := &p012ToolLoopPolicyTool{name: "narrowed", parameters: registrySchema}
	registry := NewToolRegistry()
	registry.Register(tool)
	catalog, err := registry.SnapshotModelToolCatalog()
	if err != nil {
		t.Fatalf("SnapshotModelToolCatalog() error = %v", err)
	}
	offered := providers.ToolDefinition{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name: tool.name, Parameters: narrowSchema,
		},
	}

	invalid, err := catalog.PrepareInvocation(tool.name, map[string]any{
		"safe": "ok", "extra": "not offered",
	})
	if err != nil {
		t.Fatalf("PrepareInvocation(broad-valid) error = %v", err)
	}
	if offeredErr := invalid.ValidateOfferedDefinition(offered); !errors.Is(
		offeredErr,
		workflows.ErrToolCallNotDispatched,
	) {
		t.Fatalf("ValidateOfferedDefinition(extra) error = %v", offeredErr)
	}
	if got := tool.calls.Load(); got != 0 {
		t.Fatalf("narrow-schema rejection effects = %d, want 0", got)
	}

	valid, err := catalog.PrepareInvocation(tool.name, map[string]any{"safe": "ok"})
	if err != nil {
		t.Fatalf("PrepareInvocation(narrow-valid) error = %v", err)
	}
	if offeredErr := valid.ValidateOfferedDefinition(offered); offeredErr != nil {
		t.Fatalf("ValidateOfferedDefinition(valid) error = %v", offeredErr)
	}
	result, err := registry.DispatchPrepared(
		context.Background(), valid, "", "", nil, true,
	)
	if err != nil || result == nil || result.IsError {
		t.Fatalf("DispatchPrepared(valid) = %#v, %v", result, err)
	}
	if got := tool.calls.Load(); got != 1 {
		t.Fatalf("narrow-schema valid effects = %d, want 1", got)
	}
}

func TestRunToolLoopProviderDefinitionMutationCannotWidenAuthority(t *testing.T) {
	t.Run("name", func(t *testing.T) {
		tool := &p012ToolLoopPolicyTool{
			name: "retained_name", parameters: p012ClosedObjectSchema(),
		}
		registry := NewToolRegistry()
		registry.Register(tool)
		provider := &p012ToolLoopProvider{
			responses: []*providers.LLMResponse{{ToolCalls: []providers.ToolCall{
				p012ToolCall("injected-call", "injected_name", nil),
			}}},
			mutate: func(call int, definitions []providers.ToolDefinition) {
				if call == 0 && len(definitions) == 1 {
					definitions[0].Function.Name = "injected_name"
				}
			},
		}
		var policyCalls atomic.Int64
		result, err := RunToolLoop(
			context.Background(),
			ToolLoopConfig{
				Provider: provider,
				Model:    "policy-model",
				Tools:    registry,
				Policy: p012ToolLoopPolicyFunc(func(
					context.Context,
					ToolPolicyRequest,
				) (ToolPolicyDecision, error) {
					policyCalls.Add(1)
					return ToolPolicyDecision{
						Kind: ToolPolicyDecisionAllow, ReasonCode: "should_not_run",
					}, nil
				}),
				MaxIterations: 1,
			},
			nil,
			"",
			"",
		)
		if err == nil || !strings.Contains(err.Error(), "unoffered tool") || result != nil {
			t.Fatalf("mutated-name RunToolLoop() = %#v, %v", result, err)
		}
		if policyCalls.Load() != 0 || tool.calls.Load() != 0 {
			t.Fatalf("mutated-name policy/effects = %d/%d, want 0/0", policyCalls.Load(), tool.calls.Load())
		}
	})

	t.Run("schema", func(t *testing.T) {
		tool := &p012ToolLoopPolicyTool{
			name: "retained_schema", parameters: p012ClosedObjectSchema(),
		}
		registry := NewToolRegistry()
		registry.Register(tool)
		provider := p012ScriptedProvider([]providers.ToolCall{
			p012ToolCall("schema-call", tool.name, map[string]any{"extra": "provider-only"}),
		})
		provider.mutate = func(call int, definitions []providers.ToolDefinition) {
			if call == 0 && len(definitions) == 1 {
				definitions[0].Function.Parameters["additionalProperties"] = true
			}
		}
		var policyCalls atomic.Int64
		result, err := RunToolLoop(
			context.Background(),
			ToolLoopConfig{
				Provider: provider,
				Model:    "policy-model",
				Tools:    registry,
				Policy: p012ToolLoopPolicyFunc(func(
					context.Context,
					ToolPolicyRequest,
				) (ToolPolicyDecision, error) {
					policyCalls.Add(1)
					return ToolPolicyDecision{
						Kind: ToolPolicyDecisionAllow, ReasonCode: "should_not_run",
					}, nil
				}),
				MaxIterations:       2,
				SequentialToolCalls: true,
			},
			nil,
			"",
			"",
		)
		if err != nil || result == nil || result.Content != "done" {
			t.Fatalf("mutated-schema RunToolLoop() = %#v, %v", result, err)
		}
		if policyCalls.Load() != 0 || tool.calls.Load() != 0 {
			t.Fatalf("mutated-schema policy/effects = %d/%d, want 0/0", policyCalls.Load(), tool.calls.Load())
		}
		followUp := provider.messagesAt(1)
		if len(followUp) != 2 || followUp[1].Role != "tool" ||
			!strings.Contains(followUp[1].Content, "invalid tool call") {
			t.Fatalf("mutated-schema follow-up = %#v", followUp)
		}
	})
}

func TestRunToolLoopPolicyMutationCannotChangeExecutionArguments(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"payload": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value": map[string]any{"type": "string"},
				},
				"required":             []string{"value"},
				"additionalProperties": false,
			},
		},
		"required":             []string{"payload"},
		"additionalProperties": false,
	}
	tool := &p012ToolLoopPolicyTool{name: "detached_policy", parameters: schema}
	registry := NewToolRegistry()
	registry.Register(tool)
	provider := p012ScriptedProvider([]providers.ToolCall{
		p012ToolCall("detached-call", tool.name, map[string]any{
			"payload": map[string]any{"value": "original"},
		}),
	})
	result, err := RunToolLoop(
		context.Background(),
		ToolLoopConfig{
			Provider: provider,
			Model:    "policy-model",
			Tools:    registry,
			Policy: p012ToolLoopPolicyFunc(func(
				_ context.Context,
				request ToolPolicyRequest,
			) (ToolPolicyDecision, error) {
				request.Arguments["payload"].(map[string]any)["value"] = "policy-mutated"
				request.Arguments["policy_only"] = true
				return ToolPolicyDecision{
					Kind: ToolPolicyDecisionAllow, ReasonCode: "detached_allow",
				}, nil
			}),
			MaxIterations:       2,
			SequentialToolCalls: true,
		},
		nil,
		"",
		"",
	)
	if err != nil || result == nil || result.Content != "done" {
		t.Fatalf("RunToolLoop() = %#v, %v", result, err)
	}
	want := []map[string]any{{
		"payload": map[string]any{"value": "original"},
	}}
	if got := tool.argumentsSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("execution arguments = %#v, want %#v", got, want)
	}
}

func TestRunToolLoopParallelPolicyBarrierPreventsEveryEffectOnError(t *testing.T) {
	allowTool := &p012ToolLoopPolicyTool{
		name: "barrier_allow", parameters: p012ClosedObjectSchema(),
	}
	errorTool := &p012ToolLoopPolicyTool{
		name: "barrier_error", parameters: p012ClosedObjectSchema(),
	}
	registry := NewToolRegistry()
	registry.Register(allowTool)
	registry.Register(errorTool)
	provider := &p012ToolLoopProvider{responses: []*providers.LLMResponse{{
		ToolCalls: []providers.ToolCall{
			p012ToolCall("barrier-allow", allowTool.name, nil),
			p012ToolCall("barrier-error", errorTool.name, nil),
		},
	}}}
	var policyCalls atomic.Int64
	result, err := RunToolLoop(
		context.Background(),
		ToolLoopConfig{
			Provider: provider,
			Model:    "policy-model",
			Tools:    registry,
			Policy: p012ToolLoopPolicyFunc(func(
				_ context.Context,
				request ToolPolicyRequest,
			) (ToolPolicyDecision, error) {
				policyCalls.Add(1)
				if request.Tool == errorTool.name {
					return ToolPolicyDecision{}, errors.New("parallel broker secret")
				}
				return ToolPolicyDecision{
					Kind: ToolPolicyDecisionAllow, ReasonCode: "barrier_allow",
				}, nil
			}),
			MaxIterations: 1,
		},
		nil,
		"",
		"",
	)
	if !errors.Is(err, ErrToolPolicyUnavailable) || result != nil {
		t.Fatalf("RunToolLoop() = %#v, %v", result, err)
	}
	if strings.Contains(err.Error(), "parallel broker secret") {
		t.Fatalf("parallel policy error leaked: %v", err)
	}
	if got := policyCalls.Load(); got != 2 {
		t.Fatalf("parallel policy calls = %d, want 2", got)
	}
	if allowTool.calls.Load() != 0 || errorTool.calls.Load() != 0 {
		t.Fatalf("parallel effects = allow:%d error:%d, want 0/0", allowTool.calls.Load(), errorTool.calls.Load())
	}
}

func TestRunToolLoopParallelClaimBarrierPreventsEveryEffectOnStaleEntry(t *testing.T) {
	first := &p012ToolLoopPolicyTool{
		name: "claim_barrier_first", parameters: p012ClosedObjectSchema(),
	}
	second := &p012ToolLoopPolicyTool{
		name: "claim_barrier_second", parameters: p012ClosedObjectSchema(),
	}
	registry := NewToolRegistry()
	registry.Register(first)
	registry.Register(second)
	provider := &p012ToolLoopProvider{responses: []*providers.LLMResponse{{
		ToolCalls: []providers.ToolCall{
			p012ToolCall("claim-first", first.name, nil),
			p012ToolCall("claim-second", second.name, nil),
		},
	}}}
	policy := p012ToolLoopPolicyFunc(func(
		_ context.Context,
		request ToolPolicyRequest,
	) (ToolPolicyDecision, error) {
		if request.Tool == second.name {
			registry.Unregister(second.name)
		}
		return ToolPolicyDecision{
			Kind: ToolPolicyDecisionAllow, ReasonCode: "claim_barrier_allow",
		}, nil
	})

	result, err := RunToolLoop(
		context.Background(),
		ToolLoopConfig{
			Provider: provider, Model: "policy-model", Tools: registry,
			Policy: policy, MaxIterations: 1,
		},
		nil, "", "",
	)
	if result != nil || !errors.Is(err, ErrToolInvocationStale) {
		t.Fatalf("stale claim barrier result/error = %#v / %v", result, err)
	}
	if first.calls.Load() != 0 || second.calls.Load() != 0 {
		t.Fatalf("stale claim barrier effects = %d/%d, want 0/0", first.calls.Load(), second.calls.Load())
	}
}

type p012ToolLoopTimeline struct {
	mu     sync.Mutex
	events []string
}

func (timeline *p012ToolLoopTimeline) append(event string) {
	timeline.mu.Lock()
	timeline.events = append(timeline.events, event)
	timeline.mu.Unlock()
}

func (timeline *p012ToolLoopTimeline) snapshot() []string {
	timeline.mu.Lock()
	defer timeline.mu.Unlock()
	return append([]string(nil), timeline.events...)
}

func TestRunToolLoopSequentialPolicyAndDispatchOrder(t *testing.T) {
	timeline := &p012ToolLoopTimeline{}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"step": map[string]any{"type": "string"},
		},
		"required":             []string{"step"},
		"additionalProperties": false,
	}
	tool := &p012ToolLoopPolicyTool{
		name: "ordered_policy", parameters: schema,
		execute: func(_ context.Context, arguments map[string]any) *ToolResult {
			step := arguments["step"].(string)
			timeline.append("execute:" + step)
			return NewToolResult("result:" + step)
		},
	}
	registry := NewToolRegistry()
	registry.Register(tool)
	provider := p012ScriptedProvider([]providers.ToolCall{
		p012ToolCall("ordered-first", tool.name, map[string]any{"step": "first"}),
		p012ToolCall("ordered-second", tool.name, map[string]any{"step": "second"}),
	})
	result, err := RunToolLoop(
		context.Background(),
		ToolLoopConfig{
			Provider: provider,
			Model:    "policy-model",
			Tools:    registry,
			Policy: p012ToolLoopPolicyFunc(func(
				_ context.Context,
				request ToolPolicyRequest,
			) (ToolPolicyDecision, error) {
				step := request.Arguments["step"].(string)
				timeline.append("policy:" + step)
				return ToolPolicyDecision{
					Kind: ToolPolicyDecisionAllow, ReasonCode: "ordered_allow",
				}, nil
			}),
			MaxIterations:       2,
			SequentialToolCalls: true,
		},
		nil,
		"",
		"",
	)
	if err != nil || result == nil || result.Content != "done" {
		t.Fatalf("RunToolLoop() = %#v, %v", result, err)
	}
	want := []string{
		"policy:first", "execute:first", "policy:second", "execute:second",
	}
	if got := timeline.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sequential timeline = %#v, want %#v", got, want)
	}
}

func TestRunToolLoopParallelResultsRemainInResponseOrder(t *testing.T) {
	secondStarted := make(chan struct{})
	var secondOnce sync.Once
	first := &p012ToolLoopPolicyTool{
		name: "result_first", parameters: p012ClosedObjectSchema(),
		execute: func(ctx context.Context, _ map[string]any) *ToolResult {
			select {
			case <-secondStarted:
				return NewToolResult("first-result")
			case <-ctx.Done():
				return ErrorResult("first tool timed out")
			}
		},
	}
	second := &p012ToolLoopPolicyTool{
		name: "result_second", parameters: p012ClosedObjectSchema(),
		execute: func(context.Context, map[string]any) *ToolResult {
			secondOnce.Do(func() { close(secondStarted) })
			return NewToolResult("second-result")
		},
	}
	registry := NewToolRegistry()
	registry.Register(first)
	registry.Register(second)
	provider := p012ScriptedProvider([]providers.ToolCall{
		p012ToolCall("result-call-first", first.name, nil),
		p012ToolCall("result-call-second", second.name, nil),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := RunToolLoop(
		ctx,
		ToolLoopConfig{
			Provider:      provider,
			Model:         "policy-model",
			Tools:         registry,
			Policy:        CompatibilityAllowToolPolicy{},
			MaxIterations: 2,
		},
		nil,
		"",
		"",
	)
	if err != nil || result == nil || result.Content != "done" {
		t.Fatalf("RunToolLoop() = %#v, %v", result, err)
	}
	toolMessages := make([]providers.Message, 0, 2)
	for _, message := range provider.messagesAt(1) {
		if message.Role == "tool" {
			toolMessages = append(toolMessages, message)
		}
	}
	want := []providers.Message{
		{Role: "tool", Content: "first-result", ToolCallID: "result-call-first"},
		{Role: "tool", Content: "second-result", ToolCallID: "result-call-second"},
	}
	if !reflect.DeepEqual(toolMessages, want) {
		t.Fatalf("parallel tool messages = %#v, want %#v", toolMessages, want)
	}
}

func TestRunToolLoopPolicySubjectAndCallIDsAreExact(t *testing.T) {
	tests := []struct {
		name       string
		source     ToolPolicySource
		wantSource ToolPolicySource
	}{
		{
			name: "explicit legacy source", source: ToolPolicySourceLegacySubagent,
			wantSource: ToolPolicySourceLegacySubagent,
		},
		{
			name: "generic default", wantSource: ToolPolicySourceGenericLoop,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema := map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ordinal": map[string]any{"type": "string"},
				},
				"required":             []string{"ordinal"},
				"additionalProperties": false,
			}
			tool := &p012ToolLoopPolicyTool{name: "subject_tool", parameters: schema}
			registry := NewToolRegistry()
			registry.Register(tool)
			provider := p012ScriptedProvider([]providers.ToolCall{
				p012ToolCall("call-z", tool.name, map[string]any{"ordinal": "first"}),
				p012ToolCall("call-a", tool.name, map[string]any{"ordinal": "second"}),
			})
			var requests []ToolPolicyRequest
			result, err := RunToolLoop(
				context.Background(),
				ToolLoopConfig{
					Provider: provider,
					Model:    "policy-model",
					Tools:    registry,
					Policy: p012ToolLoopPolicyFunc(func(
						_ context.Context,
						request ToolPolicyRequest,
					) (ToolPolicyDecision, error) {
						requests = append(requests, request)
						return ToolPolicyDecision{
							Kind: ToolPolicyDecisionAllow, ReasonCode: "subject_allow",
						}, nil
					}),
					PolicySubject: ToolPolicySubject{
						AgentID:    "agent-exact",
						SessionKey: "agent:agent-exact:session-exact",
						TurnID:     "turn-exact",
						ToolCallID: "must-be-overwritten",
						Source:     test.source,
					},
					MaxIterations:       2,
					SequentialToolCalls: true,
				},
				nil,
				"",
				"",
			)
			if err != nil || result == nil || result.Content != "done" {
				t.Fatalf("RunToolLoop() = %#v, %v", result, err)
			}
			if len(requests) != 2 {
				t.Fatalf("policy requests = %#v", requests)
			}
			for index, request := range requests {
				wantID := []string{"call-z", "call-a"}[index]
				wantOrdinal := []string{"first", "second"}[index]
				if request.Subject.AgentID != "agent-exact" ||
					request.Subject.SessionKey != "agent:agent-exact:session-exact" ||
					request.Subject.TurnID != "turn-exact" ||
					request.Subject.ToolCallID != wantID ||
					request.Subject.Source != test.wantSource ||
					request.Tool != tool.name ||
					request.Fulfillment != ToolFulfillmentExecute ||
					request.Arguments["ordinal"] != wantOrdinal {
					t.Fatalf("policy request %d = %#v", index, request)
				}
			}
		})
	}
}
