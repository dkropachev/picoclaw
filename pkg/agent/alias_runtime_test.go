package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type aliasRuntimeCountingProvider struct {
	calls atomic.Int32
}

type subagentAliasRecordingProvider struct {
	mu     sync.Mutex
	models []string
}

func (p *subagentAliasRecordingProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	model string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.models = append(p.models, model)
	p.mu.Unlock()
	if model == "subagent-primary-upstream" {
		return nil, fmt.Errorf("status: 429 - rate limit exceeded")
	}
	return &providers.LLMResponse{Content: "subagent done"}, nil
}

func (p *subagentAliasRecordingProvider) calledModels() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.models...)
}

func (p *aliasRuntimeCountingProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	p.calls.Add(1)
	return &providers.LLMResponse{Content: "unexpected"}, nil
}

func TestMissingModelAliasFailsBeforeProviderCall(t *testing.T) {
	cfg := strictAliasTestConfig(t)
	cfg.Agents.Defaults.ModelName = ""
	provider := &aliasRuntimeCountingProvider{}
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	t.Cleanup(loop.Close)

	_, err := loop.ProcessDirect(context.Background(), "hello", "missing-alias")
	if !errors.Is(err, config.ErrNoModelConfigured) {
		t.Fatalf("ProcessDirect() error = %v, want %v", err, config.ErrNoModelConfigured)
	}
	if got := provider.calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}

func TestRawOrUnknownModelReferenceIsNotTreatedAsAlias(t *testing.T) {
	cfg := strictAliasTestConfig(t)
	for _, name := range []string{"openai/gpt-5.4", "gpt-5.4", "unknown"} {
		t.Run(name, func(t *testing.T) {
			if _, err := candidatesForAccountAliases(
				cfg,
				"account-a",
				name,
				nil,
				cfg.Agents.Defaults.Workspace,
				map[string]providers.LLMProvider{},
			); err == nil || !strings.Contains(err.Error(), "model alias") {
				t.Fatalf("resolution error = %v, want strict unknown-alias error", err)
			}
		})
	}
}

func TestConfiguredSubagentAliasPolicyOverridesParentAndUsesFallbackAlias(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "parent"
	cfg.Agents.Defaults.MaxLLMRetries = 0
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "parent", Model: "parent-upstream"},
		{Name: "subagent-primary", Model: "subagent-primary-upstream"},
		{Name: "subagent-fallback", Model: "subagent-fallback-upstream"},
	}
	cfg.Agents.List = []config.AgentConfig{{
		ID:      "main",
		Default: true,
		Subagents: &config.SubagentsConfig{Model: &config.AgentModelConfig{
			Primary:   "subagent-primary",
			Fallbacks: []string{"subagent-fallback"},
		}},
	}}

	provider := &subagentAliasRecordingProvider{}
	loop := newTestAgentLoopWithStrictModels(
		cfg,
		bus.NewMessageBus(),
		provider,
	)
	t.Cleanup(loop.Close)
	agent := loop.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is nil")
	}
	subagentTool, ok := agent.Tools.Get("subagent")
	if !ok {
		t.Fatal("subagent tool is not registered")
	}

	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	parent := &turnState{
		ctx:            parentCtx,
		cancelFunc:     cancel,
		turnID:         "parent-subagent-alias-policy",
		sessionKey:     "parent-subagent-alias-policy",
		agent:          agent,
		session:        newEphemeralSession(nil),
		pendingResults: make(chan *tools.ToolResult, 16),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
	}
	ctx := WithAgentLoop(withTurnState(parentCtx, parent), loop)
	result := subagentTool.Execute(ctx, map[string]any{"task": "check aliases"})
	if result == nil || result.IsError {
		t.Fatalf("subagent result = %#v", result)
	}

	models := provider.calledModels()
	if len(models) < 2 {
		t.Fatalf("provider models = %#v, want primary failure then fallback", models)
	}
	if models[0] != "subagent-primary-upstream" {
		t.Fatalf("first provider model = %q, want subagent primary concrete ID", models[0])
	}
	foundFallback := false
	for _, model := range models[1:] {
		if model == "subagent-fallback-upstream" {
			foundFallback = true
			break
		}
	}
	if !foundFallback {
		t.Fatalf("provider models = %#v, want configured subagent fallback", models)
	}
	for _, model := range models {
		if model == "parent-upstream" || model == "subagent-primary" ||
			model == "subagent-fallback" {
			t.Fatalf("provider received parent/raw alias instead of concrete subagent model: %#v", models)
		}
	}
}

func TestModelAliasAccountOverrideResolvesPerConcreteAccount(t *testing.T) {
	cfg := strictAliasTestConfig(t)
	accountA, err := concreteAccountModelConfig(
		cfg,
		"account-a",
		"coding",
		cfg.Agents.Defaults.Workspace,
	)
	if err != nil {
		t.Fatal(err)
	}
	accountB, err := concreteAccountModelConfig(
		cfg,
		"account-b",
		"coding",
		cfg.Agents.Defaults.Workspace,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, model := providers.ExtractProtocol(accountA); model != "gpt-5.4" {
		t.Fatalf("account-a model = %q, want gpt-5.4", model)
	}
	if _, model := providers.ExtractProtocol(accountB); model != "claude-sonnet-4-6" {
		t.Fatalf("account-b model = %q, want claude-sonnet-4-6", model)
	}
}

func TestChatSelectionIgnoresDisabledDuplicateAccountRows(t *testing.T) {
	cfg := strictAliasTestConfig(t)
	cfg.ModelList[0].APIBase = "https://enabled.example.test/v1"
	cfg.ModelList = append(
		[]*config.ModelConfig{{
			ModelName: "account-a",
			Provider:  "anthropic",
			APIBase:   "https://disabled.example.test/v1",
			Enabled:   false,
		}},
		cfg.ModelList...,
	)

	for range 8 {
		modelCfg, err := concreteAccountModelConfig(
			cfg,
			"account-a",
			"coding",
			cfg.Agents.Defaults.Workspace,
		)
		if err != nil {
			t.Fatalf("concreteAccountModelConfig() error = %v", err)
		}
		provider, model := providers.ExtractProtocol(modelCfg)
		if provider != "openai" || model != "gpt-5.4" {
			t.Fatalf(
				"chat selection = %q/%q, want openai/gpt-5.4",
				provider,
				model,
			)
		}
		if modelCfg.APIBase != "https://enabled.example.test/v1" ||
			!modelCfg.Enabled {
			t.Fatalf("chat selected disabled duplicate: %#v", modelCfg)
		}
	}
}

func TestAccountRouterSelectionCarriesSelectedAccountAliasResolution(t *testing.T) {
	cfg := strictAliasTestConfig(t)
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name:    "router-b",
		Enabled: true,
		Entry:   "selected",
		Blocks: []config.AccountRouterBlock{{
			ID:      "selected",
			Type:    config.AccountRouterBlockTypeAccount,
			Account: "account-b",
		}},
	}}
	router := buildAccountRouterWithAliases(
		cfg,
		"router-b",
		"coding",
		nil,
		cfg.Agents.Defaults.Workspace,
		map[string]providers.LLMProvider{},
	)
	if router == nil {
		t.Fatal("account router = nil")
	}
	selection := router.Select("session", accountrouter.SelectReasonInitial)
	if len(selection.Candidates) != 1 {
		t.Fatalf("candidates = %#v, want one", selection.Candidates)
	}
	candidate := selection.Candidates[0]
	if candidate.Model != "claude-sonnet-4-6" {
		t.Fatalf("selected model = %q, want account-b override", candidate.Model)
	}
	if candidate.IdentityKey != accountAliasIdentityKey("account-b", "coding") {
		t.Fatalf("selected identity = %q, want account-b/coding", candidate.IdentityKey)
	}
}

func TestModelRouterNameSelectsTerminalAlias(t *testing.T) {
	cfg := strictAliasTestConfig(t)
	cfg.ModelAliases = append(cfg.ModelAliases, config.ModelAliasConfig{
		Name:  "fast",
		Model: "gpt-5.4-mini",
	})
	cfg.Agents.Defaults.ModelName = "task-router"
	cfg.ModelRouters = []config.ModelRouterConfig{{
		Name:    "task-router",
		Enabled: true,
		Entry:   "rules",
		Blocks: []config.ModelRouterBlock{
			{
				ID:   "rules",
				Type: config.ModelRouterBlockTypeRules,
				Rules: []config.ModelRouterRule{{
					Match:  config.ModelRouterRuleContains,
					Value:  "quick",
					Target: "fast",
				}},
				Fallback: "coding",
			},
			{ID: "fast", Type: config.ModelRouterBlockTypeModel, Model: "fast"},
			{ID: "coding", Type: config.ModelRouterBlockTypeModel, Model: "coding"},
		},
	}}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &aliasRuntimeCountingProvider{})
	t.Cleanup(func() { _ = agent.Close() })
	if agent.ConfigurationError != nil {
		t.Fatalf("ConfigurationError = %v", agent.ConfigurationError)
	}
	if len(agent.Candidates) != 0 {
		t.Fatalf("router name was resolved as an alias: %#v", agent.Candidates)
	}
	loop := &AgentLoop{cfg: cfg}
	candidates, model, usedLight, _, _ := loop.selectCandidates(
		agent,
		"make this quick",
		nil,
		"session",
		accountrouter.SelectReasonInitial,
	)
	if usedLight {
		t.Fatal("model-router selection unexpectedly used the light-model route")
	}
	if len(candidates) != 1 || model != "gpt-5.4-mini" {
		t.Fatalf("selection = %#v/%q, want fast alias model", candidates, model)
	}
	if got := modelAliasFromCandidateIdentityKey(candidates[0].IdentityKey); got != "fast" {
		t.Fatalf("selected alias = %q, want fast", got)
	}
}

func TestTerminalNonRetriableFallbackKeepsTerminalAccountIdentity(t *testing.T) {
	first := providers.FallbackCandidate{
		Provider:    "openai",
		Model:       "gpt-5.4",
		IdentityKey: accountAliasIdentityKey("copilot", "coding"),
	}
	terminal := providers.FallbackCandidate{
		Provider:    "openai",
		Model:       "gpt-5.4",
		IdentityKey: accountAliasIdentityKey("openai-work", "coding"),
	}
	err := &providers.FailoverError{
		Reason:      providers.FailoverFormat,
		Provider:    terminal.Provider,
		Model:       terminal.Model,
		IdentityKey: terminal.IdentityKey,
		Status:      400,
		Wrapped:     errors.New("bad request"),
	}
	result := fallbackResultFromError(err, first, terminal)
	if result == nil || len(result.Attempts) != 1 {
		t.Fatalf("fallback result = %#v, want terminal attempt", result)
	}
	if got := result.Attempts[0].IdentityKey; got != terminal.IdentityKey {
		t.Fatalf("terminal identity = %q, want %q", got, terminal.IdentityKey)
	}
}
