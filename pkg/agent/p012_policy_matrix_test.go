package agent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type p012PolicyMatrixFallbackProvider struct {
	mu sync.Mutex

	primaryCalls  int
	fallbackCalls int
	primaryDefs   []providers.ToolDefinition
	fallbackDefs  []providers.ToolDefinition
}

func (provider *p012PolicyMatrixFallbackProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	definitions []providers.ToolDefinition,
	model string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()

	switch model {
	case "p012-primary-model":
		provider.primaryCalls++
		provider.primaryDefs = cloneToolDefinitions(definitions)
		if len(definitions) > 0 {
			definitions[0].Function.Name = "provider_mutated_tool"
			definitions[0].Function.Parameters["type"] = "array"
		}
		return nil, errors.New("status: 429 - rate limit exceeded")
	case "p012-fallback-model":
		provider.fallbackCalls++
		if provider.fallbackCalls == 1 {
			provider.fallbackDefs = cloneToolDefinitions(definitions)
			return &providers.LLMResponse{
				ToolCalls: []providers.ToolCall{{
					ID:   "call-fallback-authoritative",
					Name: "fallback_policy_tool",
					Arguments: map[string]any{
						"text": "fallback-authoritative",
					},
				}},
				FinishReason: "tool_calls",
			}, nil
		}
		return &providers.LLMResponse{
			Content:      "fallback policy turn complete",
			FinishReason: "stop",
		}, nil
	default:
		return nil, errors.New("unexpected policy-matrix model")
	}
}

func (provider *p012PolicyMatrixFallbackProvider) snapshot() (
	primaryCalls int,
	fallbackCalls int,
	primaryDefs []providers.ToolDefinition,
	fallbackDefs []providers.ToolDefinition,
) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.primaryCalls,
		provider.fallbackCalls,
		cloneToolDefinitions(provider.primaryDefs),
		cloneToolDefinitions(provider.fallbackDefs)
}

func TestToolPolicyFallbackUsesSuccessfulAttemptAuthoritativeDefinitions(t *testing.T) {
	policy := &pipelineToolPolicyProbe{decision: tools.ToolPolicyDecision{
		Kind:       tools.ToolPolicyDecisionAllow,
		ReasonCode: "fallback_allow",
	}}
	provider := &p012PolicyMatrixFallbackProvider{}
	loop, runtimeAgent, _ := newPipelineToolPolicyLoop(t, provider, policy, false)

	tool := &pipelineToolPolicyEffect{
		name:   "fallback_policy_tool",
		output: "fallback-policy-effect",
	}
	runtimeAgent.Tools.Register(tool)
	primary := providers.FallbackCandidate{
		Provider:    "openai",
		Model:       "p012-primary-model",
		DisplayName: "p012-primary",
		IdentityKey: "p012-primary",
	}
	fallback := providers.FallbackCandidate{
		Provider:    "openai",
		Model:       "p012-fallback-model",
		DisplayName: "p012-fallback",
		IdentityKey: "p012-fallback",
	}
	runtimeAgent.Candidates = []providers.FallbackCandidate{primary, fallback}
	bindBootstrapProvider(runtimeAgent.CandidateProviders, primary, provider)
	bindBootstrapProvider(runtimeAgent.CandidateProviders, fallback, provider)
	loop.fallback = providers.NewFallbackChain(providers.NewCooldownTracker(), nil)

	response, err := runPipelineToolPolicyTurn(
		context.Background(),
		loop,
		runtimeAgent,
		"fallback-policy-session",
	)
	if err != nil || response != "fallback policy turn complete" {
		t.Fatalf("fallback policy turn = (%q, %v)", response, err)
	}
	if tool.calls.Load() != 1 || policy.calls.Load() != 1 {
		t.Fatalf(
			"fallback authorization/effect calls = policy:%d effect:%d, want 1/1",
			policy.calls.Load(),
			tool.calls.Load(),
		)
	}

	requests := policy.requests()
	if len(requests) != 1 || requests[0].Tool != tool.Name() ||
		requests[0].Subject.ToolCallID != "call-fallback-authoritative" {
		t.Fatalf("fallback policy request = %#v", requests)
	}
	primaryCalls, fallbackCalls, primaryDefs, fallbackDefs := provider.snapshot()
	if primaryCalls != 1 || fallbackCalls != 2 {
		t.Fatalf(
			"fallback provider calls = primary:%d fallback:%d, want 1/2",
			primaryCalls,
			fallbackCalls,
		)
	}
	if len(primaryDefs) != 1 || len(fallbackDefs) != 1 ||
		fallbackDefs[0].Function.Name != tool.Name() {
		t.Fatalf(
			"successful fallback definitions = %#v; primary = %#v",
			fallbackDefs,
			primaryDefs,
		)
	}
	if !reflect.DeepEqual(fallbackDefs, primaryDefs) {
		t.Fatalf(
			"primary provider mutation escaped into fallback attempt:\nprimary before mutation: %#v\nfallback: %#v",
			primaryDefs,
			fallbackDefs,
		)
	}
}

func TestToolPolicyNamedAgentUsesExactAgentIDAndToolName(t *testing.T) {
	policy := &pipelineToolPolicyProbe{decision: tools.ToolPolicyDecision{
		Kind:       tools.ToolPolicyDecisionAllow,
		ReasonCode: "named_allow",
	}}
	provider := &pipelineToolPolicyProvider{toolCalls: []providers.ToolCall{{
		ID:   "call-named-policy",
		Name: "named_policy_tool",
		Arguments: map[string]any{
			"text": "named-authority",
		},
	}}}
	rootWorkspace := t.TempDir()
	namedWorkspace := t.TempDir()
	cfg := &config.Config{Agents: config.AgentsConfig{
		Defaults: config.AgentDefaults{
			Workspace:         rootWorkspace,
			ModelName:         "default-policy-model",
			MaxTokens:         4096,
			MaxToolIterations: 4,
		},
		List: []config.AgentConfig{
			{
				ID:        "main",
				Default:   true,
				Workspace: rootWorkspace,
				Model:     &config.AgentModelConfig{Primary: "default-policy-model"},
			},
			{
				ID:        "named-policy-agent",
				Name:      "Display Name Is Not Authority",
				Workspace: namedWorkspace,
				Model:     &config.AgentModelConfig{Primary: "named-policy-model"},
			},
		},
	}}
	loop := newTestAgentLoopWithStrictModels(
		cfg,
		bus.NewMessageBus(),
		provider,
		WithToolPolicy(policy),
	)
	t.Cleanup(loop.Close)
	runtimeAgent, ok := loop.registry.GetAgent("named-policy-agent")
	if !ok || runtimeAgent == nil {
		t.Fatal("named policy agent is unavailable")
	}
	if runtimeAgent.Name == runtimeAgent.ID {
		t.Fatal("named policy fixture must distinguish display name from authority ID")
	}
	for _, name := range runtimeAgent.Tools.List() {
		runtimeAgent.Tools.Unregister(name)
	}
	tool := &pipelineToolPolicyEffect{
		name:   "named_policy_tool",
		output: "named-policy-effect",
	}
	runtimeAgent.Tools.Register(tool)

	response, err := runPipelineToolPolicyTurn(
		context.Background(),
		loop,
		runtimeAgent,
		"named-policy-session",
	)
	if err != nil || response != "named-policy-effect" {
		t.Fatalf("named-agent policy turn = (%q, %v)", response, err)
	}
	requests := policy.requests()
	if len(requests) != 1 {
		t.Fatalf("named-agent policy requests = %#v, want one", requests)
	}
	request := requests[0]
	if request.Subject.AgentID != runtimeAgent.ID ||
		request.Subject.AgentID == runtimeAgent.Name ||
		request.Subject.SessionKey != "named-policy-session" ||
		request.Subject.ToolCallID != "call-named-policy" ||
		request.Subject.Source != tools.ToolPolicySourceAgentPipeline ||
		request.Tool != tool.Name() {
		t.Fatalf(
			"named-agent policy subject/name = %#v / %q; agent ID/name = %q/%q",
			request.Subject,
			request.Tool,
			runtimeAgent.ID,
			runtimeAgent.Name,
		)
	}
	if tool.calls.Load() != 1 {
		t.Fatalf("named-agent effect calls = %d, want 1", tool.calls.Load())
	}
}

func TestToolPolicyReferenceAndAuthorizationSurviveSuccessfulReload(t *testing.T) {
	policy := &pipelineToolPolicyProbe{decision: tools.ToolPolicyDecision{
		Kind:       tools.ToolPolicyDecisionAllow,
		ReasonCode: "reload_allow",
	}}
	providerA := &pipelineToolPolicyProvider{toolCalls: []providers.ToolCall{{
		ID: "call-before-reload", Name: "reload_policy_tool",
		Arguments: map[string]any{"text": "generation-a"},
	}}}
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace:         t.TempDir(),
		ModelName:         "reload-policy-model",
		MaxTokens:         4096,
		MaxToolIterations: 4,
	}}}
	loop := newTestAgentLoopWithStrictModels(
		cfg,
		bus.NewMessageBus(),
		providerA,
		WithToolPolicy(policy),
	)
	t.Cleanup(loop.Close)
	oldAgent := loop.GetRegistry().GetDefaultAgent()
	if oldAgent == nil {
		t.Fatal("generation-A default agent is unavailable")
	}
	for _, name := range oldAgent.Tools.List() {
		oldAgent.Tools.Unregister(name)
	}
	oldTool := &pipelineToolPolicyEffect{
		name:   "reload_policy_tool",
		output: "generation-a-effect",
	}
	oldAgent.Tools.Register(oldTool)

	response, err := runPipelineToolPolicyTurn(
		context.Background(), loop, oldAgent, "policy-reload-a",
	)
	if err != nil || response != "generation-a-effect" {
		t.Fatalf("generation-A policy turn = (%q, %v)", response, err)
	}

	providerB := &pipelineToolPolicyProvider{toolCalls: []providers.ToolCall{{
		ID: "call-after-reload", Name: "reload_policy_tool",
		Arguments: map[string]any{"text": "generation-b"},
	}}}
	if reloadErr := loop.ReloadProviderAndConfig(context.Background(), providerB, cfg); reloadErr != nil {
		t.Fatalf("ReloadProviderAndConfig() error = %v", reloadErr)
	}
	if loop.toolPolicy != policy {
		t.Fatalf("reload replaced tool policy reference: got %T, want %p", loop.toolPolicy, policy)
	}
	newAgent := loop.GetRegistry().GetDefaultAgent()
	if newAgent == nil || newAgent == oldAgent {
		t.Fatalf("generation-B default agent = %p; old = %p", newAgent, oldAgent)
	}
	for _, name := range newAgent.Tools.List() {
		newAgent.Tools.Unregister(name)
	}
	newTool := &pipelineToolPolicyEffect{
		name:   "reload_policy_tool",
		output: "generation-b-effect",
	}
	newAgent.Tools.Register(newTool)

	response, err = runPipelineToolPolicyTurn(
		context.Background(), loop, newAgent, "policy-reload-b",
	)
	if err != nil || response != "generation-b-effect" {
		t.Fatalf("generation-B policy turn = (%q, %v)", response, err)
	}
	requests := policy.requests()
	if len(requests) != 2 ||
		requests[0].Subject.ToolCallID != "call-before-reload" ||
		requests[0].Subject.SessionKey != "policy-reload-a" ||
		requests[1].Subject.ToolCallID != "call-after-reload" ||
		requests[1].Subject.SessionKey != "policy-reload-b" ||
		requests[0].Tool != "reload_policy_tool" ||
		requests[1].Tool != "reload_policy_tool" {
		t.Fatalf("cross-reload policy requests = %#v", requests)
	}
	if oldTool.calls.Load() != 1 || newTool.calls.Load() != 1 {
		t.Fatalf(
			"cross-reload effects = generation A:%d B:%d, want 1/1",
			oldTool.calls.Load(),
			newTool.calls.Load(),
		)
	}
}
