package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/modelrouter"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
)

func TestRestrictedProviderClassificationIsNormalizedAndExact(t *testing.T) {
	for _, provider := range []string{
		"codex-cli", " CODEXCLI ", "claude-cli", "ClaudeCli",
	} {
		normalized, unsafe := restrictedProvider(provider)
		if !unsafe || normalized != providers.NormalizeProvider(provider) {
			t.Fatalf("restrictedProvider(%q) = %q, %v", provider, normalized, unsafe)
		}
	}
	for _, provider := range []string{
		"openai", "anthropic", "openai-codex", "claude-cli-proxy", "",
	} {
		if normalized, unsafe := restrictedProvider(provider); unsafe || normalized != "" {
			t.Fatalf("restrictedProvider(%q) = %q, %v, want safe", provider, normalized, unsafe)
		}
	}
}

func TestRestrictedProviderClosureCoversMaterializedRuntimeRoutes(t *testing.T) {
	candidate := func(provider string) providers.FallbackCandidate {
		return providers.FallbackCandidate{Provider: provider, Model: "model"}
	}
	accountRouter := func(provider string) *accountrouter.Router {
		return &accountrouter.Router{Accounts: map[string]accountrouter.Account{
			"safe":         {Candidates: []providers.FallbackCandidate{candidate("openai")}},
			"z-restricted": {Candidates: []providers.FallbackCandidate{candidate(provider)}},
		}}
	}

	for _, test := range []struct {
		name string
		edit func(*AgentInstance)
		want string
	}{
		{
			name: "primary",
			edit: func(agent *AgentInstance) {
				agent.Candidates = []providers.FallbackCandidate{candidate("codex-cli")}
			},
			want: "codex-cli",
		},
		{
			name: "fallback",
			edit: func(agent *AgentInstance) {
				agent.Candidates = []providers.FallbackCandidate{
					candidate("openai"), candidate("claudecli"),
				}
			},
			want: "claude-cli",
		},
		{
			name: "account router terminal",
			edit: func(agent *AgentInstance) {
				agent.AccountRouter = accountRouter("codexcli")
			},
			want: "codex-cli",
		},
		{
			name: "light route",
			edit: func(agent *AgentInstance) {
				agent.LightCandidates = []providers.FallbackCandidate{candidate("claude-cli")}
			},
			want: "claude-cli",
		},
		{
			name: "light account router terminal",
			edit: func(agent *AgentInstance) {
				agent.LightAccountRouter = accountRouter("codex-cli")
			},
			want: "codex-cli",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, agent := restrictedProviderClosureSafeFixture(t)
			test.edit(agent)
			audit, err := auditRestrictedProviderClosure(cfg, agent)
			if err != nil || audit.Status != RestrictedProviderClosureUnsafe ||
				audit.Provider != test.want {
				t.Fatalf("audit = %#v, %v, want unsafe %q", audit, err, test.want)
			}
		})
	}
}

func TestRestrictedProviderClosureCoversEveryLazyModelAndAccountRouterTerminal(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "must-not-be-created")
	cfg := &config.Config{
		ModelAliases: []config.ModelAliasConfig{
			{
				Name: "disabled-target", Model: "disabled-model",
				DisabledAccounts: []string{"safe-account", "restricted-account"},
			},
			{Name: "reachable-target", Model: "target-model"},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "safe-account", Provider: "openai", Model: "safe-model",
				Enabled: true,
			},
			{
				ModelName: "restricted-account", Provider: "claudecli", Model: "unsafe-model",
				Enabled: true,
			},
		},
		AccountRouters: []config.AccountRouterConfig{{
			Name: "accounts", Enabled: true, Entry: "all",
			Blocks: []config.AccountRouterBlock{{
				ID: "all", Type: config.AccountRouterBlockTypeLoadBalance,
				Accounts: []string{"safe-account", "restricted-account"},
			}},
		}},
	}
	routerCfg := config.ModelRouterConfig{
		Name: "models", Enabled: true, Entry: "rules",
		Blocks: []config.ModelRouterBlock{
			{
				ID: "rules", Type: config.ModelRouterBlockTypeRules, Fallback: "disabled",
				Rules: []config.ModelRouterRule{{
					Match: config.ModelRouterRuleHasCode, Target: "reachable",
				}},
			},
			{ID: "disabled", Type: config.ModelRouterBlockTypeModel, Model: "disabled-target"},
			{ID: "reachable", Type: config.ModelRouterBlockTypeModel, Model: "reachable-target"},
		},
	}
	agent := &AgentInstance{
		ID: "main", AccountRef: "accounts", Model: "models",
		Workspace: workspace, ModelRouter: modelrouter.New("models", &routerCfg),
	}

	audit, err := auditRestrictedProviderClosure(cfg, agent)
	if err != nil || audit.Status != RestrictedProviderClosureUnsafe ||
		audit.Provider != "claude-cli" {
		t.Fatalf("lazy routed audit = %#v, %v", audit, err)
	}
	if _, statErr := os.Stat(workspace); !os.IsNotExist(statErr) {
		t.Fatalf("closure audit touched workspace %q: %v", workspace, statErr)
	}
}

func TestRestrictedProviderClosureAllowsCredentialOAuthAccount(t *testing.T) {
	cfg := &config.Config{ModelAliases: []config.ModelAliasConfig{{
		Name: "primary", Model: "gpt-safe",
	}}}
	agent := &AgentInstance{
		ID: "main", AccountRef: "credential:openai:work", Model: "primary",
		Candidates: []providers.FallbackCandidate{{Provider: "openai", Model: "gpt-safe"}},
	}
	audit, err := auditRestrictedProviderClosure(cfg, agent)
	if err != nil || audit.Status != RestrictedProviderClosureSafe || audit.Provider != "" {
		t.Fatalf("credential OAuth audit = %#v, %v", audit, err)
	}
}

func TestRestrictedProviderClosureAuditsSafeLightRouteAndStableLease(t *testing.T) {
	cfg, runtimeAgent := restrictedProviderClosureSafeFixture(t)
	runtimeAgent.LightCandidates = []providers.FallbackCandidate{{
		Provider: "anthropic", Model: "claude-safe",
	}}
	runtimeAgent.Router = routing.New(routing.RouterConfig{
		LightModel: "light", Threshold: 0.5,
	})
	loop := &AgentLoop{
		cfg: cfg,
		registry: &AgentRegistry{
			cfg: cfg, agents: map[string]*AgentInstance{"main": runtimeAgent},
		},
	}
	leaseCtx, release, err := loop.acquireTrustedRuntimeRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	audit, err := loop.AuditRestrictedProviderClosureWithRuntimeLease(leaseCtx, "main")
	if err != nil || audit.Status != RestrictedProviderClosureSafe || audit.Provider != "" {
		release()
		t.Fatalf("safe leased audit = %#v, %v", audit, err)
	}
	release()
	if audit, err = loop.AuditRestrictedProviderClosureWithRuntimeLease(leaseCtx, "main"); err == nil ||
		audit.Status != "" {
		t.Fatalf("released leased audit = %#v, %v", audit, err)
	}
	if _, err = loop.AuditRestrictedProviderClosureWithRuntimeLease(
		context.Background(),
		"main",
	); err == nil {
		t.Fatal("lease-free closure audit succeeded")
	}
}

func TestRestrictedProviderClosureRejectsInvalidRuntimeShapes(t *testing.T) {
	t.Parallel()

	var nilLoop *AgentLoop
	if _, err := nilLoop.AuditRestrictedProviderClosureWithRuntimeLease(
		context.Background(),
		"main",
	); err == nil {
		t.Fatal("nil loop audit succeeded")
	}

	cfg, runtimeAgent := restrictedProviderClosureSafeFixture(t)
	loop := &AgentLoop{
		cfg: cfg,
		registry: &AgentRegistry{
			cfg: cfg, agents: map[string]*AgentInstance{"main": runtimeAgent},
		},
	}
	leaseCtx, release, err := loop.acquireTrustedRuntimeRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, agentID := range []string{" main", "not/canonical"} {
		if _, auditErr := loop.AuditRestrictedProviderClosureWithRuntimeLease(
			leaseCtx,
			agentID,
		); auditErr == nil {
			t.Fatalf("invalid agent ID %q succeeded", agentID)
		}
	}
	if _, err = loop.AuditRestrictedProviderClosureWithRuntimeLease(
		leaseCtx,
		"missing",
	); err == nil {
		t.Fatal("missing agent audit succeeded")
	}
	runtimeAgent.ConfigurationError = errors.New("invalid test configuration")
	if _, err = loop.AuditRestrictedProviderClosureWithRuntimeLease(
		leaseCtx,
		"main",
	); err == nil {
		t.Fatal("invalid agent configuration audit succeeded")
	}
}

func TestRestrictedProviderClosureErrorBranches(t *testing.T) {
	t.Parallel()

	cfg, agent := restrictedProviderClosureSafeFixture(t)
	if _, err := auditRestrictedProviderClosure(nil, agent); err == nil {
		t.Fatal("nil configuration audit succeeded")
	}
	if _, err := auditRestrictedProviderClosure(cfg, nil); err == nil {
		t.Fatal("nil agent audit succeeded")
	}

	missingAccount := *agent
	missingAccount.AccountRef = ""
	if _, err := auditRestrictedProviderClosure(cfg, &missingAccount); err == nil {
		t.Fatal("missing account audit succeeded")
	}

	missingModel := *agent
	missingModel.Model = ""
	if _, err := auditRestrictedProviderClosure(cfg, &missingModel); err == nil {
		t.Fatal("missing primary model audit succeeded")
	}

	noTargetsConfig := config.ModelRouterConfig{
		Name: "empty", Enabled: true, Entry: "rules",
		Blocks: []config.ModelRouterBlock{{
			ID: "rules", Type: config.ModelRouterBlockTypeRules,
		}},
	}
	noTargets := *agent
	noTargets.ModelRouter = modelrouter.New("empty", &noTargetsConfig)
	if _, err := auditRestrictedProviderClosure(cfg, &noTargets); err == nil {
		t.Fatal("target-free model router audit succeeded")
	}

	missingTargetConfig := config.ModelRouterConfig{
		Name: "missing", Enabled: true, Entry: "target",
		Blocks: []config.ModelRouterBlock{
			{ID: "ignored", Type: config.ModelRouterBlockTypeRules},
			{ID: "blank", Type: config.ModelRouterBlockTypeModel},
			{ID: "target", Type: config.ModelRouterBlockTypeModel, Model: "unknown"},
			{ID: "duplicate", Type: config.ModelRouterBlockTypeModel, Model: "unknown"},
		},
	}
	missingTarget := *agent
	missingTarget.ModelRouter = modelrouter.New("missing", &missingTargetConfig)
	if _, err := auditRestrictedProviderClosure(cfg, &missingTarget); err == nil {
		t.Fatal("unresolvable model-router target audit succeeded")
	}

	missingLight := *agent
	missingLight.Router = routing.New(routing.RouterConfig{
		LightModel: "unknown", Threshold: 0.5,
	})
	if _, err := auditRestrictedProviderClosure(cfg, &missingLight); err == nil {
		t.Fatal("unresolvable light route audit succeeded")
	}

	if audit, unsafe := auditRestrictedAccountRouter(&accountrouter.Router{
		Accounts: map[string]accountrouter.Account{
			"safe": {Candidates: []providers.FallbackCandidate{{Provider: "openai"}}},
		},
	}); unsafe || audit.Status != "" {
		t.Fatalf("safe account router audit = %#v, unsafe=%v", audit, unsafe)
	}
	if _, err := restrictedProviderClosureAccounts(cfg, " "); err == nil {
		t.Fatal("blank account reference succeeded")
	}

	emptyRouterConfig := *cfg
	emptyRouterConfig.AccountRouters = []config.AccountRouterConfig{{
		Name: "empty", Enabled: true, Entry: "missing",
	}}
	if _, err := restrictedProviderClosureAccounts(&emptyRouterConfig, "empty"); err == nil {
		t.Fatal("empty account router succeeded")
	}
	if _, _, err := auditRestrictedAliasChain(cfg, []string{"safe-account"}, " ", nil, agent.Workspace); err == nil {
		t.Fatal("blank primary alias succeeded")
	}
	if audit, unsafe, err := auditRestrictedAliasChain(
		cfg,
		[]string{"safe-account"},
		"primary",
		[]string{"", "primary", "fallback"},
		agent.Workspace,
	); err != nil || unsafe || audit.Status != "" {
		t.Fatalf("deduplicated safe alias chain = %#v, unsafe=%v, err=%v", audit, unsafe, err)
	}
	if _, _, err := auditRestrictedAliasChain(
		cfg,
		[]string{"safe-account"},
		"unknown",
		nil,
		agent.Workspace,
	); err == nil {
		t.Fatal("unresolvable alias chain succeeded")
	}
}

func TestRestrictedProviderClosureConfigurationOnlyUnsafeAndDisabledFallbacks(t *testing.T) {
	t.Parallel()

	cfg, agent := restrictedProviderClosureSafeFixture(t)
	restrictedConfig := *cfg
	restrictedConfig.ModelList = []*config.ModelConfig{{
		ModelName: "safe-account", Provider: "codex-cli", Model: "gpt-safe", Enabled: true,
	}}
	audit, err := auditRestrictedProviderClosure(&restrictedConfig, agent)
	if err != nil || audit.Status != RestrictedProviderClosureUnsafe || audit.Provider != "codex-cli" {
		t.Fatalf("configuration-only unsafe audit = %#v, err=%v", audit, err)
	}

	disabledFallbackConfig := *cfg
	disabledFallbackConfig.ModelAliases = append(
		append([]config.ModelAliasConfig(nil), cfg.ModelAliases...),
		config.ModelAliasConfig{
			Name: "disabled-fallback", Model: "disabled",
			DisabledAccounts: []string{"safe-account"},
		},
	)
	if aliasAudit, unsafe, auditErr := auditRestrictedAliasChain(
		&disabledFallbackConfig,
		[]string{"safe-account"},
		"primary",
		[]string{"disabled-fallback"},
		agent.Workspace,
	); auditErr != nil || unsafe || aliasAudit.Status != "" {
		t.Fatalf("disabled fallback audit = %#v, unsafe=%v, err=%v", aliasAudit, unsafe, auditErr)
	}

	routerConfig := config.ModelRouterConfig{
		Name: "models", Enabled: true, Entry: "target",
		Blocks: []config.ModelRouterBlock{{
			ID: "target", Type: config.ModelRouterBlockTypeModel, Model: "disabled-target",
		}},
	}
	routerAgent := &AgentInstance{
		ID: "main", AccountRef: "restricted-account", Model: "models",
		Workspace: agent.Workspace, ModelRouter: modelrouter.New("models", &routerConfig),
		Router: routing.New(routing.RouterConfig{
			LightModel: "light", Threshold: 0.5,
		}),
		Candidates: []providers.FallbackCandidate{{Provider: "openai", Model: "safe"}},
	}
	routerCfg := &config.Config{
		ModelAliases: []config.ModelAliasConfig{
			{
				Name: "disabled-target", Model: "disabled",
				DisabledAccounts: []string{"restricted-account"},
			},
			{Name: "light", Model: "enabled"},
		},
		ModelList: []*config.ModelConfig{{
			ModelName: "restricted-account", Provider: "claude-cli", Model: "enabled", Enabled: true,
		}},
	}
	audit, err = auditRestrictedProviderClosure(routerCfg, routerAgent)
	if err != nil || audit.Status != RestrictedProviderClosureUnsafe || audit.Provider != "claude-cli" {
		t.Fatalf("light-router unsafe audit = %#v, err=%v", audit, err)
	}
}

func restrictedProviderClosureSafeFixture(t *testing.T) (*config.Config, *AgentInstance) {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "workspace")
	cfg := &config.Config{
		ModelAliases: []config.ModelAliasConfig{
			{Name: "primary", Model: "gpt-safe"},
			{Name: "fallback", Model: "gpt-fallback"},
			{Name: "light", Model: "claude-safe"},
		},
		ModelList: []*config.ModelConfig{{
			ModelName: "safe-account", Provider: "openai", Model: "gpt-safe", Enabled: true,
		}},
	}
	return cfg, &AgentInstance{
		ID: "main", AccountRef: "safe-account", Model: "primary",
		Fallbacks: []string{"fallback"}, Workspace: workspace,
		Candidates: []providers.FallbackCandidate{{Provider: "openai", Model: "gpt-safe"}},
	}
}
