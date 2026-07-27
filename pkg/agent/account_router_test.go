package agent

import (
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestAgentLoopSelectCandidatesUsesBuiltAccountRouter(t *testing.T) {
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
			{},
		},
		AccountRouters: []config.AccountRouterConfig{
			{
				Name:    "router-main",
				Model:   "router-main",
				Enabled: true,
				Entry:   "pool",
				Blocks: []config.AccountRouterBlock{{
					ID:       "pool",
					Type:     config.AccountRouterBlockTypeLoadBalance,
					Accounts: []string{"account-a", "account-b"},
					Strategy: config.AccountRouterStrategyTokensSpent,
				}},
			},
		},
	}
	cfg.MaterializeAccountRouterModels()
	workspace := t.TempDir()
	candidateProviders := map[string]providers.LLMProvider{}
	router := buildAccountRouter(cfg, "openai", "router-main", workspace, candidateProviders)
	if router == nil {
		t.Fatal("buildAccountRouter() = nil")
	}
	if got := router.StatePath; got != filepath.Join(workspace, "account_router_state.json") {
		t.Fatalf("state path = %q, want workspace account_router_state.json", got)
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
		AccountRouter: router,
	}

	candidates, model, usedLight, selection := loop.selectCandidates(
		agent,
		"hello",
		nil,
		"session-1",
		accountrouter.SelectReasonInitial,
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

func TestBuildAccountRouterUsesSharedRouterModelWithAccountIdentity(t *testing.T) {
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
			{},
		},
		AccountRouters: []config.AccountRouterConfig{
			{
				Name:    "joint-account",
				Model:   "gpt-5.4",
				Enabled: true,
				Entry:   "pool",
				Blocks: []config.AccountRouterBlock{{
					ID:       "pool",
					Type:     config.AccountRouterBlockTypeLoadBalance,
					Accounts: []string{"account-a", "account-b"},
					Strategy: config.AccountRouterStrategyTokensSpent,
				}},
			},
		},
	}
	cfg.MaterializeAccountRouterModels()
	candidateProviders := map[string]providers.LLMProvider{}
	router := buildAccountRouter(cfg, "openai", "joint-account", t.TempDir(), candidateProviders)
	if router == nil {
		t.Fatal("buildAccountRouter() = nil")
	}

	selection := router.Select("session-1", accountrouter.SelectReasonInitial)
	if len(selection.Candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(selection.Candidates))
	}
	candidate := selection.Candidates[0]
	if candidate.Model != "gpt-5.4" {
		t.Fatalf("candidate model = %q, want shared router model", candidate.Model)
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

func TestBuildAccountRouterUsesCredentialAccountRefs(t *testing.T) {
	cfg := &config.Config{
		AccountRouters: []config.AccountRouterConfig{
			{
				Name:    "joint-account",
				Model:   "gpt-5.4",
				Enabled: true,
				Entry:   "primary",
				Blocks: []config.AccountRouterBlock{{
					ID:      "primary",
					Type:    config.AccountRouterBlockTypeAccount,
					Account: "credential:openai:work",
				}},
			},
		},
	}
	cfg.MaterializeAccountRouterModels()
	if err := cfg.ValidateModelList(); err != nil {
		t.Fatalf("ValidateModelList() error = %v", err)
	}

	candidateProviders := map[string]providers.LLMProvider{}
	router := buildAccountRouter(cfg, "openai", "joint-account", t.TempDir(), candidateProviders)
	if router == nil {
		t.Fatal("buildAccountRouter() = nil")
	}

	selection := router.Select("session-1", accountrouter.SelectReasonInitial)
	if len(selection.Candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(selection.Candidates))
	}
	candidate := selection.Candidates[0]
	if candidate.Provider != "openai" {
		t.Fatalf("candidate provider = %q, want openai", candidate.Provider)
	}
	if candidate.Model != "gpt-5.4" {
		t.Fatalf("candidate model = %q, want shared router model", candidate.Model)
	}
	if candidate.IdentityKey != "model_name:credential:openai:work" {
		t.Fatalf("candidate identity = %q, want credential account identity", candidate.IdentityKey)
	}
}

func TestBuildAccountRouterDoesNotCreateCredentialAccountCandidatesWithoutSharedModel(t *testing.T) {
	cfg := &config.Config{
		AccountRouters: []config.AccountRouterConfig{
			{
				Name:    "joint-account",
				Model:   "joint-account",
				Enabled: true,
				Entry:   "primary",
				Blocks: []config.AccountRouterBlock{{
					ID:      "primary",
					Type:    config.AccountRouterBlockTypeAccount,
					Account: "credential:openai:work",
				}},
			},
		},
	}
	cfg.MaterializeAccountRouterModels()
	if err := cfg.ValidateModelList(); err != nil {
		t.Fatalf("ValidateModelList() error = %v", err)
	}

	candidateProviders := map[string]providers.LLMProvider{}
	router := buildAccountRouter(cfg, "openai", "joint-account", t.TempDir(), candidateProviders)
	if router == nil {
		t.Fatal("buildAccountRouter() = nil")
	}

	selection := router.Select("session-1", accountrouter.SelectReasonInitial)
	if len(selection.Candidates) != 0 {
		t.Fatalf("len(candidates) = %d, want 0", len(selection.Candidates))
	}
}

func TestAccountRouterCredentialAccountConfigSupportsGitHubCopilotNamedRefs(t *testing.T) {
	modelCfg, ok := accountRouterCredentialAccountConfig(
		"credential:github-copilot:gh-copilot",
		"claude-sonnet-4.5",
	)
	if !ok {
		t.Fatal("accountRouterCredentialAccountConfig() ok = false, want true")
	}
	if modelCfg == nil {
		t.Fatal("accountRouterCredentialAccountConfig() = nil, want config")
	}
	if modelCfg.Provider != "github-copilot" {
		t.Fatalf("Provider = %q, want github-copilot", modelCfg.Provider)
	}
	if modelCfg.AuthMethod != "token" {
		t.Fatalf("AuthMethod = %q, want token", modelCfg.AuthMethod)
	}
	if modelCfg.CredentialID != "github-copilot:gh-copilot" {
		t.Fatalf("CredentialID = %q, want github-copilot:gh-copilot", modelCfg.CredentialID)
	}
	if modelCfg.Model != "claude-sonnet-4.5" {
		t.Fatalf("Model = %q, want shared model", modelCfg.Model)
	}
}

func TestBuildAccountRouterSupportsGitHubCopilotCredentialLoadBalanceRefs(t *testing.T) {
	cfg := &config.Config{
		AccountRouters: []config.AccountRouterConfig{
			{
				Name:    "copilot-router",
				Model:   "gpt-5",
				Enabled: true,
				Entry:   "pool",
				Blocks: []config.AccountRouterBlock{{
					ID:       "pool",
					Type:     config.AccountRouterBlockTypeLoadBalance,
					Accounts: []string{"credential:github-copilot:gh-copilot", "credential:github-copilot:backup"},
					Strategy: config.AccountRouterStrategyTokensSpent,
				}},
			},
		},
	}
	cfg.MaterializeAccountRouterModels()
	if err := cfg.ValidateModelList(); err != nil {
		t.Fatalf("ValidateModelList() error = %v", err)
	}

	router := buildAccountRouter(cfg, "openai", "copilot-router", t.TempDir(), map[string]providers.LLMProvider{})
	if router == nil {
		t.Fatal("buildAccountRouter() = nil")
	}

	selection := router.Select("session-1", accountrouter.SelectReasonInitial)
	if len(selection.Candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(selection.Candidates))
	}
	candidate := selection.Candidates[0]
	if candidate.Provider != "github-copilot" {
		t.Fatalf("candidate provider = %q, want github-copilot", candidate.Provider)
	}
	if candidate.Model != "gpt-5" {
		t.Fatalf("candidate model = %q, want shared router model", candidate.Model)
	}
	if candidate.IdentityKey != "model_name:credential:github-copilot:gh-copilot" {
		t.Fatalf("candidate identity = %q, want full credential identity", candidate.IdentityKey)
	}
	if got := selection.BlockAccountChoices["pool"]; got != "credential:github-copilot:gh-copilot" {
		t.Fatalf("pool choice = %q, want full credential account ref", got)
	}
}
