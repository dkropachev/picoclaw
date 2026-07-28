package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type accountRouterPrimaryProvider struct {
	calls atomic.Int32
	err   error
}

func (p *accountRouterPrimaryProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	p.calls.Add(1)
	if p.err != nil {
		return nil, p.err
	}
	return &providers.LLMResponse{Content: "primary", FinishReason: "stop"}, nil
}

func (p *accountRouterPrimaryProvider) GetDefaultModel() string {
	return "primary-model"
}

type accountRouterSideQuestionProvider struct {
	calls atomic.Int32
	model string
}

func (p *accountRouterSideQuestionProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	model string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.calls.Add(1)
	p.model = model
	return &providers.LLMResponse{Content: "side answer", FinishReason: "stop"}, nil
}

func (p *accountRouterSideQuestionProvider) GetDefaultModel() string {
	return "side-default"
}

func storeAccountRouterTestCredential(
	t *testing.T,
	credentialID string,
	credential *auth.AuthCredential,
) {
	t.Helper()
	if err := auth.SetCredential(credentialID, credential); err != nil {
		t.Fatalf("SetCredential(%q) error = %v", credentialID, err)
	}
}

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

	candidates, model, usedLight, activeRouter, selection := loop.selectCandidates(
		agent,
		"hello",
		nil,
		"session-1",
		accountrouter.SelectReasonInitial,
	)
	if usedLight {
		t.Fatal("usedLight = true, want false")
	}
	if activeRouter == nil {
		t.Fatal("activeRouter = nil, want account router")
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
	accountB := router.Accounts["account-b"]
	if len(accountB.Candidates) != 1 {
		t.Fatalf("account-b candidates = %#v, want one candidate", accountB.Candidates)
	}
	if got := accountB.Candidates[0]; got.Model != "gpt-4o-mini" ||
		got.IdentityKey != "model_name:account-b" {
		t.Fatalf(
			"account-b candidate = %#v, want native model gpt-4o-mini with stable account identity",
			got,
		)
	}
}

func TestBuildAccountRouterAppliesExplicitModelAcrossAccountsWithStableIdentities(t *testing.T) {
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
	router := buildAccountRouterWithModel(
		cfg,
		"openai",
		"joint-account",
		"gpt-5.4",
		t.TempDir(),
		candidateProviders,
	)
	if router == nil {
		t.Fatal("buildAccountRouterWithModel() = nil")
	}

	for _, accountName := range []string{"account-a", "account-b"} {
		account := router.Accounts[accountName]
		if len(account.Candidates) != 1 {
			t.Fatalf("%s candidates = %#v, want one candidate", accountName, account.Candidates)
		}
		candidate := account.Candidates[0]
		if candidate.Model != "gpt-5.4" {
			t.Fatalf("%s candidate model = %q, want explicit model gpt-5.4", accountName, candidate.Model)
		}
		if candidate.IdentityKey != "model_name:"+accountName {
			t.Fatalf(
				"%s candidate identity = %q, want stable account identity",
				accountName,
				candidate.IdentityKey,
			)
		}
	}

	selection := router.Select("session-1", accountrouter.SelectReasonInitial)
	if len(selection.Candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(selection.Candidates))
	}
	candidate := selection.Candidates[0]
	if candidate.Model != "gpt-5.4" {
		t.Fatalf("candidate model = %q, want explicit model gpt-5.4", candidate.Model)
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

func TestBuildAccountRouterExplicitModelKeepsAccountProvider(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{{
			ModelName: "anthropic-account",
			Model:     "anthropic/claude-sonnet-4-6",
			APIKeys:   config.SimpleSecureStrings("sk-ant-account"),
		}},
		AccountRouters: []config.AccountRouterConfig{{
			Name:    "joint-account",
			Enabled: true,
			Entry:   "account",
			Blocks: []config.AccountRouterBlock{{
				ID:      "account",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "anthropic-account",
			}},
		}},
	}
	cfg.MaterializeAccountRouterModels()

	candidateProviders := map[string]providers.LLMProvider{}
	router := buildAccountRouterWithModel(
		cfg,
		"openai",
		"joint-account",
		"claude-haiku-4-5",
		t.TempDir(),
		candidateProviders,
	)
	if router == nil {
		t.Fatal("buildAccountRouterWithModel() = nil")
	}

	selection := router.Select("session-1", accountrouter.SelectReasonInitial)
	if len(selection.Candidates) != 1 {
		t.Fatalf("candidates = %#v, want one candidate", selection.Candidates)
	}
	candidate := selection.Candidates[0]
	if candidate.Provider != "anthropic" || candidate.Model != "claude-haiku-4-5" {
		t.Fatalf(
			"candidate = %#v, want anthropic account with requested model",
			candidate,
		)
	}
	if candidateProviders[candidate.IdentityKey] == nil {
		t.Fatal("provider was not registered by stable account identity")
	}
}

func TestBuildAccountRouterUsesCredentialAccountRefs(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", t.TempDir())
	storeAccountRouterTestCredential(t, "openai:work", &auth.AuthCredential{
		AccessToken: "openai-work-token",
		AccountID:   "openai-work-account",
		Provider:    "openai",
		AuthMethod:  "oauth",
	})

	cfg := &config.Config{
		AccountRouters: []config.AccountRouterConfig{
			{
				Name:    "joint-account",
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
	if candidate.Model != "gpt-5.3-codex" {
		t.Fatalf(
			"candidate model = %q, want OpenAI Codex default gpt-5.3-codex",
			candidate.Model,
		)
	}
	if candidate.IdentityKey != "model_name:credential:openai:work" {
		t.Fatalf("candidate identity = %q, want credential account identity", candidate.IdentityKey)
	}
}

func TestBuildAccountRouterCreatesCredentialAccountCandidateWithExplicitModel(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", t.TempDir())
	storeAccountRouterTestCredential(t, "openai:work", &auth.AuthCredential{
		AccessToken: "openai-work-token",
		AccountID:   "openai-work-account",
		Provider:    "openai",
		AuthMethod:  "oauth",
	})

	cfg := &config.Config{
		AccountRouters: []config.AccountRouterConfig{
			{
				Name:    "joint-account",
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
	router := buildAccountRouterWithModel(
		cfg,
		"openai",
		"joint-account",
		"missing-model",
		t.TempDir(),
		candidateProviders,
	)
	if router == nil {
		t.Fatal("buildAccountRouterWithModel() = nil")
	}

	selection := router.Select("session-1", accountrouter.SelectReasonInitial)
	if len(selection.Candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(selection.Candidates))
	}
	candidate := selection.Candidates[0]
	if candidate.Model != "missing-model" {
		t.Fatalf("candidate model = %q, want missing-model", candidate.Model)
	}
	if candidate.IdentityKey != "model_name:credential:openai:work" {
		t.Fatalf("candidate identity = %q, want credential account identity", candidate.IdentityKey)
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
		t.Fatalf("Model = %q, want explicit model", modelCfg.Model)
	}
}

func TestAccountRouterCredentialAccountConfigUsesAntigravityDefaultModel(t *testing.T) {
	modelCfg, ok := accountRouterCredentialAccountConfig(
		"credential:google-antigravity:work",
		"",
	)
	if !ok || modelCfg == nil {
		t.Fatalf(
			"accountRouterCredentialAccountConfig() = (%#v, %t), want config",
			modelCfg,
			ok,
		)
	}
	if modelCfg.Provider != "antigravity" {
		t.Fatalf("Provider = %q, want antigravity", modelCfg.Provider)
	}
	if modelCfg.Model != "gemini-3-flash" {
		t.Fatalf("Model = %q, want provider default gemini-3-flash", modelCfg.Model)
	}
	if modelCfg.AuthMethod != "oauth" {
		t.Fatalf("AuthMethod = %q, want oauth", modelCfg.AuthMethod)
	}
}

func TestBuildAccountRouterSupportsGitHubCopilotCredentialLoadBalanceRefs(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", t.TempDir())
	for _, credentialID := range []string{
		"github-copilot:gh-copilot",
		"github-copilot:backup",
	} {
		storeAccountRouterTestCredential(t, credentialID, &auth.AuthCredential{
			AccessToken: "gho_test-copilot-token",
			Provider:    "github-copilot",
			AuthMethod:  "token",
		})
	}

	cfg := &config.Config{
		AccountRouters: []config.AccountRouterConfig{
			{
				Name:    "copilot-router",
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
	if candidate.Model != "auto" {
		t.Fatalf("candidate model = %q, want provider default auto", candidate.Model)
	}
	if candidate.IdentityKey != "model_name:credential:github-copilot:gh-copilot" {
		t.Fatalf("candidate identity = %q, want full credential identity", candidate.IdentityKey)
	}
	if got := selection.BlockAccountChoices["pool"]; got != "credential:github-copilot:gh-copilot" {
		t.Fatalf("pool choice = %q, want full credential account ref", got)
	}
}

func TestMissingAccountRouterCredentialNeverInvokesPrimaryProvider(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", t.TempDir())
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				Provider:          "openai",
				ModelName:         "credential-router",
				MaxTokens:         1024,
				MaxToolIterations: 1,
			},
		},
		AccountRouters: []config.AccountRouterConfig{{
			Name:    "credential-router",
			Enabled: true,
			Entry:   "account",
			Blocks: []config.AccountRouterBlock{{
				ID:      "account",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "credential:openai:missing",
			}},
		}},
	}
	cfg.MaterializeAccountRouterModels()

	primary := &accountRouterPrimaryProvider{}
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), primary)
	t.Cleanup(func() {
		loop.Close()
	})

	_, err := loop.ProcessDirect(context.Background(), "hello", "missing-router-credential")
	if err == nil || !strings.Contains(err.Error(), "no runnable account provider") {
		t.Fatalf("ProcessDirect() error = %v, want no runnable account provider", err)
	}
	if got := primary.calls.Load(); got != 0 {
		t.Fatalf("primary provider calls = %d, want 0", got)
	}
}

func TestMissingDirectCredentialOverrideNeverInvokesPrimaryProvider(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", t.TempDir())
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				Provider:          "openai",
				ModelName:         "primary-model",
				MaxTokens:         1024,
				MaxToolIterations: 1,
			},
		},
	}
	primary := &accountRouterPrimaryProvider{}
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), primary)
	t.Cleanup(func() {
		loop.Close()
	})
	agent := loop.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent = nil")
	}

	_, err := loop.runAgentLoop(context.Background(), agent, processOptions{
		Dispatch: DispatchRequest{
			SessionKey:  "missing-direct-credential",
			UserMessage: "hello",
		},
		ModelNameOverride: "credential:openai:missing",
		ModelIDOverride:   "gpt-5.4",
		NoHistory:         true,
	})
	if err == nil || !strings.Contains(err.Error(), "no runnable account provider") {
		t.Fatalf("runAgentLoop() error = %v, want no runnable account provider", err)
	}
	if got := primary.calls.Load(); got != 0 {
		t.Fatalf("primary provider calls = %d, want 0", got)
	}
}

func TestSideQuestionAccountRouterOverrideUsesSelectedModelAndNativeAccount(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				Provider:          "openai",
				ModelName:         "primary-model",
				MaxTokens:         1024,
				MaxToolIterations: 1,
			},
		},
		ModelList: []*config.ModelConfig{{
			ModelName: "native-account",
			Provider:  "openai",
			Model:     "native-default",
			APIKeys:   config.SimpleSecureStrings("sk-native"),
			Enabled:   true,
		}},
		AccountRouters: []config.AccountRouterConfig{{
			Name:    "router-main",
			Enabled: true,
			Entry:   "account",
			Blocks: []config.AccountRouterBlock{{
				ID:      "account",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "native-account",
			}},
		}},
	}
	cfg.MaterializeAccountRouterModels()

	primary := &accountRouterPrimaryProvider{}
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), primary)
	t.Cleanup(loop.Close)
	agent := loop.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent = nil")
	}

	side := &accountRouterSideQuestionProvider{}
	var gotConfig *config.ModelConfig
	loop.providerFactory = func(mc *config.ModelConfig) (providers.LLMProvider, string, error) {
		if mc != nil {
			clone := *mc
			gotConfig = &clone
		}
		return side, mc.Model, nil
	}

	response, err := loop.askSideQuestion(
		context.Background(),
		agent,
		&processOptions{
			SessionKey:        "native-btw",
			ModelNameOverride: "router-main",
			ModelIDOverride:   "gpt-selected",
			NoHistory:         true,
		},
		"explain privately",
	)
	if err != nil {
		t.Fatalf("askSideQuestion() error = %v", err)
	}
	if response != "side answer" {
		t.Fatalf("askSideQuestion() response = %q, want side answer", response)
	}
	if gotConfig == nil ||
		gotConfig.ModelName != "native-account" ||
		gotConfig.Provider != "openai" ||
		gotConfig.Model != "gpt-selected" ||
		gotConfig.APIKey() != "sk-native" {
		t.Fatalf("side question model config = %+v, want selected native account config", gotConfig)
	}
	if side.model != "gpt-selected" || side.calls.Load() != 1 {
		t.Fatalf("side provider model/calls = %q/%d, want gpt-selected/1", side.model, side.calls.Load())
	}
	if primary.calls.Load() != 0 {
		t.Fatalf("primary provider calls = %d, want 0", primary.calls.Load())
	}
}

func TestSideQuestionAccountRouterOverrideUsesNamedCredential(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", t.TempDir())
	storeAccountRouterTestCredential(t, "openai:work", &auth.AuthCredential{
		AccessToken: "work-token",
		Provider:    "openai",
		AuthMethod:  "oauth",
	})
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				Provider:          "openai",
				ModelName:         "primary-model",
				MaxTokens:         1024,
				MaxToolIterations: 1,
			},
		},
		AccountRouters: []config.AccountRouterConfig{{
			Name:    "router-main",
			Enabled: true,
			Entry:   "account",
			Blocks: []config.AccountRouterBlock{{
				ID:      "account",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "credential:openai:work",
			}},
		}},
	}
	cfg.MaterializeAccountRouterModels()

	primary := &accountRouterPrimaryProvider{}
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), primary)
	t.Cleanup(loop.Close)
	agent := loop.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent = nil")
	}

	side := &accountRouterSideQuestionProvider{}
	var gotConfig *config.ModelConfig
	loop.providerFactory = func(mc *config.ModelConfig) (providers.LLMProvider, string, error) {
		if mc != nil {
			clone := *mc
			gotConfig = &clone
		}
		return side, mc.Model, nil
	}

	response, err := loop.askSideQuestion(
		context.Background(),
		agent,
		&processOptions{
			SessionKey:        "credential-btw",
			ModelNameOverride: "router-main",
			ModelIDOverride:   "gpt-selected",
			NoHistory:         true,
		},
		"explain privately",
	)
	if err != nil {
		t.Fatalf("askSideQuestion() error = %v", err)
	}
	if response != "side answer" {
		t.Fatalf("askSideQuestion() response = %q, want side answer", response)
	}
	if gotConfig == nil ||
		gotConfig.ModelName != "credential:openai:work" ||
		gotConfig.CredentialID != "openai:work" ||
		gotConfig.AuthMethod != "oauth" ||
		gotConfig.Model != "gpt-selected" {
		t.Fatalf("side question model config = %+v, want named credential config", gotConfig)
	}
	if side.model != "gpt-selected" || side.calls.Load() != 1 {
		t.Fatalf("side provider model/calls = %q/%d, want gpt-selected/1", side.model, side.calls.Load())
	}
	if primary.calls.Load() != 0 {
		t.Fatalf("primary provider calls = %d, want 0", primary.calls.Load())
	}
}

func TestSideQuestionMissingCredentialOverrideNeverInvokesPrimaryProvider(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", t.TempDir())
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				Provider:          "openai",
				ModelName:         "primary-model",
				MaxTokens:         1024,
				MaxToolIterations: 1,
			},
		},
		AccountRouters: []config.AccountRouterConfig{{
			Name:    "router-missing",
			Enabled: true,
			Entry:   "account",
			Blocks: []config.AccountRouterBlock{{
				ID:      "account",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "credential:openai:missing",
			}},
		}},
	}
	cfg.MaterializeAccountRouterModels()

	primary := &accountRouterPrimaryProvider{}
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), primary)
	t.Cleanup(loop.Close)
	agent := loop.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent = nil")
	}

	_, err := loop.askSideQuestion(
		context.Background(),
		agent,
		&processOptions{
			SessionKey:        "missing-btw",
			ModelNameOverride: "router-missing",
			ModelIDOverride:   "gpt-selected",
			NoHistory:         true,
		},
		"explain privately",
	)
	if err == nil || !strings.Contains(err.Error(), "no runnable account provider") {
		t.Fatalf("askSideQuestion() error = %v, want no runnable account provider", err)
	}
	if primary.calls.Load() != 0 {
		t.Fatalf("primary provider calls = %d, want 0", primary.calls.Load())
	}
}

func TestCreateProviderBootstrapPreservesRouterAliasForAgentLoop(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", t.TempDir())
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				Provider:          "openai",
				ModelName:         "router-main",
				MaxTokens:         1024,
				MaxToolIterations: 1,
			},
		},
		ModelList: []*config.ModelConfig{{
			ModelName: "runnable-account",
			Provider:  "openai",
			Model:     "gpt-runnable",
			APIKeys:   config.SimpleSecureStrings("sk-runnable"),
			Enabled:   true,
		}},
		AccountRouters: []config.AccountRouterConfig{{
			Name:    "router-main",
			Enabled: true,
			Entry:   "missing",
			Blocks: []config.AccountRouterBlock{
				{
					ID:       "missing",
					Type:     config.AccountRouterBlockTypeAccount,
					Account:  "credential:openai:missing",
					Fallback: "runnable",
				},
				{
					ID:      "runnable",
					Type:    config.AccountRouterBlockTypeAccount,
					Account: "runnable-account",
				},
			},
		}},
	}
	cfg.MaterializeAccountRouterModels()

	provider, modelSelector, err := providers.CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	if modelSelector != "router-main" {
		t.Fatalf("model selector = %q, want router-main", modelSelector)
	}

	// Production gateway, CLI, and workflow startup paths retain the selector
	// returned by CreateProvider before constructing the agent loop.
	cfg.Agents.Defaults.ModelName = modelSelector
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	t.Cleanup(loop.Close)
	agent := loop.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent = nil")
	}
	if agent.AccountRouter == nil {
		t.Fatal("default agent account router = nil, want built router")
	}

	candidates, model, usedLight, router, selection := loop.selectCandidates(
		agent,
		"hello",
		nil,
		"bootstrap-session",
		accountrouter.SelectReasonInitial,
	)
	if model != "gpt-runnable" {
		t.Fatalf("resolved model = %q, want gpt-runnable", model)
	}
	if usedLight {
		t.Fatal("usedLight = true, want false")
	}
	if selection.RouterName != "router-main" ||
		selection.SessionKey != "bootstrap-session" {
		t.Fatalf(
			"router selection = %#v, want router-main/bootstrap-session",
			selection,
		)
	}
	if router == nil || len(candidates) != 1 ||
		candidates[0].IdentityKey != "model_name:runnable-account" {
		t.Fatalf("router/candidates = %#v/%#v, want runnable fallback account", router, candidates)
	}
}

func TestAccountRouterSkipsUnrunnableNativeAccountAndUsesBlockFallback(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName: "missing-account",
				Provider:  "openai",
				Model:     "gpt-missing",
				Enabled:   true,
			},
			{
				ModelName: "runnable-account",
				Provider:  "openai",
				Model:     "gpt-runnable",
				APIBase:   "http://example.invalid/v1",
				APIKeys:   config.SimpleSecureStrings("sk-runnable"),
				Enabled:   true,
			},
		},
		AccountRouters: []config.AccountRouterConfig{{
			Name:    "fallback-router",
			Enabled: true,
			Entry:   "primary",
			Blocks: []config.AccountRouterBlock{
				{
					ID:       "primary",
					Type:     config.AccountRouterBlockTypeAccount,
					Account:  "missing-account",
					Fallback: "fallback",
				},
				{
					ID:      "fallback",
					Type:    config.AccountRouterBlockTypeAccount,
					Account: "runnable-account",
				},
			},
		}},
	}
	cfg.MaterializeAccountRouterModels()

	registered := map[string]providers.LLMProvider{}
	router := buildAccountRouter(
		cfg,
		"openai",
		"fallback-router",
		t.TempDir(),
		registered,
	)
	if router == nil {
		t.Fatal("buildAccountRouter() = nil")
	}
	if _, ok := router.Accounts["missing-account"]; ok {
		t.Fatal("unrunnable native account was admitted to router")
	}

	selection := router.Select("fallback-session", accountrouter.SelectReasonInitial)
	if len(selection.Candidates) != 1 {
		t.Fatalf("selection candidates = %#v, want one runnable fallback", selection.Candidates)
	}
	if got := selection.Candidates[0].IdentityKey; got != "model_name:runnable-account" {
		t.Fatalf("selected identity = %q, want runnable-account", got)
	}
	agent := &AgentInstance{CandidateProviders: registered}
	if !candidateSelectionHasProvider(agent, selection.Candidates) {
		t.Fatal("selected block fallback has no registered provider")
	}
}

func TestAccountRouterPrimaryFailureDoesNotReusePrimaryForMissingFallbackProvider(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName: "runnable-primary",
				Provider:  "openai",
				Model:     "gpt-primary",
				APIBase:   "http://example.invalid/v1",
				APIKeys:   config.SimpleSecureStrings("sk-primary"),
				Enabled:   true,
			},
			{
				ModelName: "missing-fallback",
				Provider:  "openai",
				Model:     "gpt-fallback",
				Enabled:   true,
			},
		},
		AccountRouters: []config.AccountRouterConfig{{
			Name:    "fallback-router",
			Enabled: true,
			Entry:   "primary",
			Blocks: []config.AccountRouterBlock{
				{
					ID:       "primary",
					Type:     config.AccountRouterBlockTypeAccount,
					Account:  "runnable-primary",
					Fallback: "fallback",
				},
				{
					ID:      "fallback",
					Type:    config.AccountRouterBlockTypeAccount,
					Account: "missing-fallback",
				},
			},
		}},
	}
	cfg.MaterializeAccountRouterModels()

	registered := map[string]providers.LLMProvider{}
	router := buildAccountRouter(
		cfg,
		"openai",
		"fallback-router",
		t.TempDir(),
		registered,
	)
	if router == nil {
		t.Fatal("buildAccountRouter() = nil")
	}
	selection := router.Select("primary-session", accountrouter.SelectReasonInitial)
	if len(selection.Candidates) != 1 {
		t.Fatalf(
			"selection candidates = %#v, want only runnable primary",
			selection.Candidates,
		)
	}
	if got := selection.Candidates[0].IdentityKey; got != "model_name:runnable-primary" {
		t.Fatalf("selected identity = %q, want runnable-primary", got)
	}

	routedPrimary := &accountRouterPrimaryProvider{err: errors.New("routed primary failed")}
	agentCandidateProvidersMu.Lock()
	for _, key := range candidateProviderKeys(selection.Candidates[0]) {
		registered[key] = routedPrimary
	}
	agentCandidateProvidersMu.Unlock()

	injectedPrimary := &accountRouterPrimaryProvider{}
	agent := &AgentInstance{
		Provider:           injectedPrimary,
		CandidateProviders: registered,
	}
	active := workflowProviderForCandidates(agent, injectedPrimary, selection.Candidates)
	if active != routedPrimary {
		t.Fatalf("active provider = %#v, want routed primary provider", active)
	}
	if _, err := active.Chat(
		context.Background(),
		nil,
		nil,
		selection.Candidates[0].Model,
		nil,
	); !errors.Is(err, routedPrimary.err) {
		t.Fatalf("routed primary Chat() error = %v, want %v", err, routedPrimary.err)
	}
	if got := routedPrimary.calls.Load(); got != 1 {
		t.Fatalf("routed primary calls = %d, want 1", got)
	}
	if got := injectedPrimary.calls.Load(); got != 0 {
		t.Fatalf("injected primary calls = %d, want 0", got)
	}
}
