package agent

import (
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/modelrouter"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestAgentLoopSelectCandidatesUsesBuiltModelRouter(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName: "account-a",
				Provider:  "openai",
				Model:     "gpt-4o",
				APIKeys:   config.SimpleSecureStrings("sk-account-a"),
			},
			{
				ModelName: "account-b",
				Provider:  "openai",
				Model:     "gpt-4o-mini",
				APIKeys:   config.SimpleSecureStrings("sk-account-b"),
			},
			{
				ModelName: "router-main",
				Provider:  config.ModelRouterProvider,
				Model:     "router-main",
				Router: &config.ModelRouterConfig{
					Enabled: true,
					Entry:   "pool",
					Blocks: []config.ModelRouterBlock{{
						ID:       "pool",
						Type:     config.ModelRouterBlockTypeLoadBalance,
						Accounts: []string{"account-a", "account-b"},
						Strategy: config.ModelRouterStrategyTokensSpent,
					}},
				},
			},
		},
	}
	workspace := t.TempDir()
	candidateProviders := map[string]providers.LLMProvider{}
	router := buildModelRouter(cfg, "openai", "router-main", workspace, candidateProviders)
	if router == nil {
		t.Fatal("buildModelRouter() = nil")
	}
	if got := router.StatePath; got != filepath.Join(workspace, "model_router_state.json") {
		t.Fatalf("state path = %q, want workspace model_router_state.json", got)
	}

	loop := &AgentLoop{}
	agent := &AgentInstance{
		ID:    "main",
		Model: "router-main",
		Candidates: []providers.FallbackCandidate{{
			Provider:    "openai",
			Model:       "fallback",
			IdentityKey: "model_name:fallback",
		}},
		ModelRouter: router,
	}

	candidates, model, usedLight, selection := loop.selectCandidates(
		agent,
		"hello",
		nil,
		"session-1",
		modelrouter.SelectReasonInitial,
	)
	if usedLight {
		t.Fatal("usedLight = true, want false")
	}
	if selection.RouterName != "router-main" || selection.SessionKey != "session-1" {
		t.Fatalf("router selection = %#v, want router-main/session-1", selection)
	}
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(candidates))
	}
	if got := candidates[0].IdentityKey; got != "model_name:account-a" {
		t.Fatalf("candidate identity = %q, want model_name:account-a", got)
	}
	if model != "gpt-4o" {
		t.Fatalf("resolved model = %q, want gpt-4o", model)
	}
	if candidateProviders[providers.ModelKey("openai", "gpt-4o")] == nil {
		t.Fatal("account-a provider was not registered")
	}
}

func TestBuildModelRouterIgnoresRouterModelField(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName: "account-a",
				Provider:  "openai",
				Model:     "gpt-4o",
				APIKeys:   config.SimpleSecureStrings("sk-account-a"),
			},
			{
				ModelName: "account-b",
				Provider:  "openai",
				Model:     "gpt-4o-mini",
				APIKeys:   config.SimpleSecureStrings("sk-account-b"),
			},
			{
				ModelName: "joint-account",
				Provider:  config.ModelRouterProvider,
				Model:     "ignored-legacy-model",
				Router: &config.ModelRouterConfig{
					Enabled: true,
					Entry:   "pool",
					Blocks: []config.ModelRouterBlock{{
						ID:       "pool",
						Type:     config.ModelRouterBlockTypeLoadBalance,
						Accounts: []string{"account-a", "account-b"},
						Strategy: config.ModelRouterStrategyTokensSpent,
					}},
				},
			},
		},
	}
	candidateProviders := map[string]providers.LLMProvider{}
	router := buildModelRouter(cfg, "openai", "joint-account", t.TempDir(), candidateProviders)
	if router == nil {
		t.Fatal("buildModelRouter() = nil")
	}

	selection := router.Select("session-1", modelrouter.SelectReasonInitial)
	if len(selection.Candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(selection.Candidates))
	}
	candidate := selection.Candidates[0]
	if candidate.Model != "gpt-4o" {
		t.Fatalf("candidate model = %q, want account model", candidate.Model)
	}
	if candidate.IdentityKey != "model_name:account-a" {
		t.Fatalf("candidate identity = %q, want account identity", candidate.IdentityKey)
	}
	if candidateProviders["model_name:account-a"] == nil {
		t.Fatal("account-a identity provider was not registered")
	}
	if candidateProviders["model_name:account-b"] == nil {
		t.Fatal("account-b identity provider was not registered")
	}
	if candidateProviders["model_name:account-a"] == candidateProviders["model_name:account-b"] {
		t.Fatal("account providers share one provider instance")
	}
}

func TestBuildModelRouterDoesNotCreateCredentialAccountCandidatesWithoutModelListEntry(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName: "joint-account",
				Provider:  config.ModelRouterProvider,
				Router: &config.ModelRouterConfig{
					Enabled: true,
					Entry:   "primary",
					Blocks: []config.ModelRouterBlock{{
						ID:      "primary",
						Type:    config.ModelRouterBlockTypeAccount,
						Account: "credential:openai:work",
					}},
				},
			},
		},
	}
	if err := cfg.ValidateModelList(); err != nil {
		t.Fatalf("ValidateModelList() error = %v", err)
	}

	candidateProviders := map[string]providers.LLMProvider{}
	router := buildModelRouter(cfg, "openai", "joint-account", t.TempDir(), candidateProviders)
	if router == nil {
		t.Fatal("buildModelRouter() = nil")
	}

	selection := router.Select("session-1", modelrouter.SelectReasonInitial)
	if len(selection.Candidates) != 0 {
		t.Fatalf("len(candidates) = %d, want 0", len(selection.Candidates))
	}
}

func TestBuildModelRouterSupportsGitHubCopilotCredentialLoadBalanceRefs(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName: "copilot-router",
				Provider:  config.ModelRouterProvider,
				Router: &config.ModelRouterConfig{
					Enabled: true,
					Entry:   "pool",
					Blocks: []config.ModelRouterBlock{{
						ID:       "pool",
						Type:     config.ModelRouterBlockTypeLoadBalance,
						Accounts: []string{"credential:github-copilot:gh-copilot", "credential:github-copilot:backup"},
						Strategy: config.ModelRouterStrategyTokensSpent,
					}},
				},
			},
		},
	}
	if err := cfg.ValidateModelList(); err != nil {
		t.Fatalf("ValidateModelList() error = %v", err)
	}

	router := buildModelRouter(cfg, "openai", "copilot-router", t.TempDir(), map[string]providers.LLMProvider{})
	if router == nil {
		t.Fatal("buildModelRouter() = nil")
	}

	selection := router.Select("session-1", modelrouter.SelectReasonInitial)
	if len(selection.Candidates) != 0 {
		t.Fatalf("len(candidates) = %d, want 0", len(selection.Candidates))
	}
}
