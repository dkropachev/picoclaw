package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/modelrouter"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type controllerRepairFactoryProvider struct {
	name string
}

func (*controllerRepairFactoryProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "unused"}, nil
}

func TestNewControllerLocalRepairRunnerRequiresExactCurrentAgent(t *testing.T) {
	candidate := controllerRepairFactoryCandidate("account-a", "coding", "openai", "gpt-primary")
	provider := &controllerRepairFactoryProvider{name: "primary"}
	agent := &AgentInstance{
		ID:            "repairer",
		Model:         "coding",
		Candidates:    []providers.FallbackCandidate{candidate},
		Provider:      provider,
		MaxIterations: 7,
		MaxTokens:     2048,
		Temperature:   0.4,
	}
	loop := newControllerRepairFactoryLoop(t, &config.Config{}, agent)
	if !loop.ControllerLocalRepairReady("repairer") {
		t.Fatal("ControllerLocalRepairReady() = false for exact runnable primary agent")
	}

	runner, err := loop.NewControllerLocalRepairRunner("repairer", "untrusted routing text")
	if err != nil {
		t.Fatalf("NewControllerLocalRepairRunner() error = %v", err)
	}
	if runner.provider != provider || runner.model != "gpt-primary" {
		t.Fatalf("runner target did not use the exact primary selection")
	}
	if runner.workspaces != loop.gitWorkspaces || runner.maxIterations != 7 ||
		runner.maxTokens != 2048 || runner.temperature != 0.4 {
		t.Fatalf("runner config = %#v, want exact agent settings", runner)
	}

	for _, agentID := range []string{" repairer", "repairer ", "Repairer", "repairer/child", ""} {
		t.Run("reject_"+strings.ReplaceAll(agentID, " ", "_"), func(t *testing.T) {
			if loop.ControllerLocalRepairReady(agentID) {
				t.Fatalf("ControllerLocalRepairReady(%q) = true", agentID)
			}
			if _, err := loop.NewControllerLocalRepairRunner(agentID, "route"); err == nil {
				t.Fatalf("NewControllerLocalRepairRunner(%q) error = nil", agentID)
			}
		})
	}
	if _, err := loop.NewControllerLocalRepairRunner("missing", "route"); err == nil {
		t.Fatal("missing current registry agent error = nil")
	}

	loop.registry.agents["repairer"] = &AgentInstance{ID: "different"}
	if _, err := loop.NewControllerLocalRepairRunner("repairer", "route"); err == nil {
		t.Fatal("registry entry with a different exact agent ID error = nil")
	}
	var nilLoop *AgentLoop
	if _, err := nilLoop.NewControllerLocalRepairRunner("repairer", "route"); err == nil {
		t.Fatal("nil AgentLoop error = nil")
	}
}

func TestNewControllerLocalRepairRunnerUsesModelRouterCandidateProvider(t *testing.T) {
	cfg := strictAliasTestConfig(t)
	cfg.ModelAliases = append(cfg.ModelAliases, config.ModelAliasConfig{
		Name:  "fast",
		Model: "gpt-fast",
	})
	routerConfig := config.ModelRouterConfig{
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
	}
	cfg.ModelRouters = []config.ModelRouterConfig{routerConfig}

	primary := controllerRepairFactoryCandidate("account-a", "coding", "openai", "gpt-5.4")
	selected := controllerRepairFactoryCandidate("account-a", "fast", "openai", "gpt-fast")
	primaryProvider := &controllerRepairFactoryProvider{name: "primary"}
	selectedProvider := &controllerRepairFactoryProvider{name: "selected"}
	agent := &AgentInstance{
		ID:                 "repairer",
		AccountRef:         "account-a",
		Model:              "task-router",
		Candidates:         []providers.FallbackCandidate{primary},
		Fallbacks:          []string{"coding"},
		Provider:           primaryProvider,
		CandidateProviders: map[string]providers.LLMProvider{},
		ModelRouter:        modelrouter.New(routerConfig.Name, &routerConfig),
		MaxIterations:      3,
		MaxTokens:          1024,
	}
	registerCandidateProvider(agent.CandidateProviders, selected, selectedProvider)
	registerCandidateProvider(agent.CandidateProviders, primary, primaryProvider)
	loop := newControllerRepairFactoryLoop(t, cfg, agent)
	if !loop.ControllerLocalRepairReady("repairer") {
		t.Fatal("model-router controller repair readiness = false")
	}

	runner, err := loop.NewControllerLocalRepairRunner("repairer", "make this QUICK")
	if err != nil {
		t.Fatalf("NewControllerLocalRepairRunner() error = %v", err)
	}
	if runner.provider != selectedProvider || runner.model != "gpt-fast" {
		t.Fatal("runner did not bind the first model-router candidate and its concrete provider")
	}
}

func TestNewControllerLocalRepairRunnerUsesBlankAccountAffinity(t *testing.T) {
	accountA := controllerRepairFactoryCandidate("account-a", "coding", "openai", "gpt-a")
	accountB := controllerRepairFactoryCandidate("account-b", "coding", "openai", "gpt-b")
	providerA := &controllerRepairFactoryProvider{name: "account-a"}
	providerB := &controllerRepairFactoryProvider{name: "account-b"}
	statePath := filepath.Join(t.TempDir(), "account-router-state.json")
	routerConfig := config.AccountRouterConfig{
		Name:    "accounts",
		Enabled: true,
		Entry:   "pool",
		Blocks: []config.AccountRouterBlock{{
			ID:       "pool",
			Type:     config.AccountRouterBlockTypeLoadBalance,
			Accounts: []string{"account-a", "account-b"},
			Strategy: config.AccountRouterStrategyBlind,
		}},
	}
	router := accountrouter.New(
		routerConfig.Name,
		&routerConfig,
		map[string]accountrouter.Account{
			"account-a": {Candidates: []providers.FallbackCandidate{accountA}},
			"account-b": {Candidates: []providers.FallbackCandidate{accountB}},
		},
		statePath,
	)
	agent := &AgentInstance{
		ID:                 "repairer",
		Model:              "coding",
		Candidates:         []providers.FallbackCandidate{accountA},
		AccountRouter:      router,
		CandidateProviders: map[string]providers.LLMProvider{},
		MaxIterations:      2,
		MaxTokens:          512,
	}
	registerCandidateProvider(agent.CandidateProviders, accountA, providerA)
	registerCandidateProvider(agent.CandidateProviders, accountB, providerB)
	loop := newControllerRepairFactoryLoop(t, &config.Config{}, agent)
	if !loop.ControllerLocalRepairReady("repairer") {
		t.Fatal("account-router controller repair readiness = false")
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("readiness wrote account affinity state: Stat() error = %v", err)
	}

	runner, err := loop.NewControllerLocalRepairRunner("repairer", "route")
	if err != nil {
		t.Fatalf("NewControllerLocalRepairRunner() error = %v", err)
	}
	if runner.provider != providerA || runner.model != "gpt-a" {
		t.Fatal("blank-affinity initial selection did not choose the first blind account")
	}

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile(account router state) error = %v", err)
	}
	var state struct {
		Routers map[string]struct {
			Sessions map[string]json.RawMessage `json:"sessions"`
		} `json:"routers"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("Unmarshal(account router state) error = %v", err)
	}
	if sessions := state.Routers[routerConfig.Name].Sessions; len(sessions) != 0 {
		t.Fatalf("account router persisted session affinity = %#v, want none", sessions)
	}

	agent.CandidateProviders = nil
	agent.Provider = providerB
	agent.Candidates = nil
	if _, err := loop.NewControllerLocalRepairRunner("repairer", "route"); err == nil {
		t.Fatal("account-routed selection reused a non-exact primary agent provider")
	}
}

func TestNewControllerLocalRepairRunnerRejectsUnavailableDependencies(t *testing.T) {
	newLoop := func(t *testing.T) (*AgentLoop, *AgentInstance) {
		t.Helper()
		candidate := providers.FallbackCandidate{
			Provider:    "secret-provider-should-not-leak",
			Model:       "secret-model-should-not-leak",
			IdentityKey: "secret-candidate-should-not-leak",
		}
		agent := &AgentInstance{
			ID:                 "repairer",
			Model:              "secret-alias-should-not-leak",
			Candidates:         []providers.FallbackCandidate{candidate},
			CandidateProviders: map[string]providers.LLMProvider{},
			MaxIterations:      2,
			MaxTokens:          512,
		}
		registerCandidateProvider(
			agent.CandidateProviders,
			candidate,
			&controllerRepairFactoryProvider{name: "available"},
		)
		return newControllerRepairFactoryLoop(t, &config.Config{}, agent), agent
	}

	tests := []struct {
		name   string
		mutate func(*AgentLoop, *AgentInstance)
	}{
		{name: "config", mutate: func(loop *AgentLoop, _ *AgentInstance) { loop.cfg = nil }},
		{name: "registry", mutate: func(loop *AgentLoop, _ *AgentInstance) { loop.registry = nil }},
		{name: "workspace", mutate: func(loop *AgentLoop, _ *AgentInstance) { loop.gitWorkspaces = nil }},
		{name: "agent config", mutate: func(_ *AgentLoop, agent *AgentInstance) {
			agent.ConfigurationError = errors.New("secret configuration detail")
		}},
		{name: "model", mutate: func(_ *AgentLoop, agent *AgentInstance) {
			agent.Candidates = nil
		}},
		{name: "provider", mutate: func(_ *AgentLoop, agent *AgentInstance) {
			agent.CandidateProviders = nil
			agent.Provider = nil
		}},
		{name: "runner config", mutate: func(_ *AgentLoop, agent *AgentInstance) {
			agent.MaxIterations = 129
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loop, agent := newLoop(t)
			test.mutate(loop, agent)
			if loop.ControllerLocalRepairReady("repairer") {
				t.Fatal("ControllerLocalRepairReady() = true for unavailable dependency")
			}
			_, err := loop.NewControllerLocalRepairRunner("repairer", "untrusted route")
			if err == nil {
				t.Fatal("NewControllerLocalRepairRunner() error = nil")
			}
			for _, private := range []string{
				"secret-provider-should-not-leak",
				"secret-model-should-not-leak",
				"secret-candidate-should-not-leak",
				"secret-alias-should-not-leak",
				"secret configuration detail",
			} {
				if strings.Contains(err.Error(), private) {
					t.Fatalf("error %q exposed private provider/model configuration", err)
				}
			}
		})
	}
}

func controllerRepairFactoryCandidate(
	account string,
	alias string,
	provider string,
	model string,
) providers.FallbackCandidate {
	return providers.FallbackCandidate{
		Provider:    provider,
		Model:       model,
		DisplayName: alias,
		IdentityKey: accountAliasIdentityKey(account, alias),
	}
}

func newControllerRepairFactoryLoop(
	t *testing.T,
	cfg *config.Config,
	agent *AgentInstance,
) *AgentLoop {
	t.Helper()
	manager, err := gitworkspace.NewManager(gitworkspace.Options{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("gitworkspace.NewManager() error = %v", err)
	}
	return &AgentLoop{
		cfg: cfg,
		registry: &AgentRegistry{
			cfg:    cfg,
			agents: map[string]*AgentInstance{agent.ID: agent},
		},
		gitWorkspaces: manager,
	}
}
