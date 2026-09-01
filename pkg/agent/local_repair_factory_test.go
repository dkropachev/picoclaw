package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/modelrouter"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type controllerRepairFactoryProvider struct {
	name string
}

type controllerRepairPolicyProbeProvider struct {
	mu       sync.Mutex
	policies []logger.DiagnosticPolicy
}

func (provider *controllerRepairPolicyProbeProvider) Chat(
	ctx context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.mu.Lock()
	provider.policies = append(
		provider.policies,
		logger.DiagnosticPolicyFromContext(ctx),
	)
	provider.mu.Unlock()
	return &providers.LLMResponse{Content: "done"}, nil
}

func (provider *controllerRepairPolicyProbeProvider) snapshot() []logger.DiagnosticPolicy {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]logger.DiagnosticPolicy(nil), provider.policies...)
}

func TestP015B3ALocalRepairControllerLeaseBoundary(t *testing.T) {
	pin, workspace, _ := newLocalRepairTestWorkspace(t)
	pin.AgentID = "repairer"
	workspace.LockedBy.AgentID = pin.AgentID
	workspaces := &localRepairTestAcquirer{workspace: workspace}
	provider := &controllerRepairPolicyProbeProvider{}
	candidate := controllerRepairFactoryCandidate(
		"account-a",
		"coding",
		"openai",
		"gpt-primary",
	)
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
	protectedRoot := filepath.Join(t.TempDir(), "launcher-auth.db")
	loop.fileMutationProtectedRoots = []string{protectedRoot}
	positive := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	loop.mu.Lock()
	loop.diagnosticPolicy = positive
	loop.mu.Unlock()
	leaseCtx, release, err := loop.acquireTrustedRuntimeRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !loop.ControllerLocalRepairReadyWithRuntimeLease(leaseCtx, "repairer") {
		release()
		t.Fatal("strict repair readiness rejected a live generation")
	}
	runner, err := loop.NewControllerLocalRepairRunnerWithRuntimeLease(
		leaseCtx,
		"repairer",
		"route",
	)
	if err != nil {
		release()
		t.Fatal(err)
	}
	if !runner.strictRuntime || runner.generationID == 0 ||
		runner.runtimeLoop != loop {
		release()
		t.Fatal("strict repair runner did not retain opaque generation identity")
	}
	loop.fileMutationProtectedRoots[0] = "mutated-after-runner-construction"
	if len(runner.protectedRoots) != 1 || runner.protectedRoots[0] != protectedRoot {
		release()
		t.Fatalf("strict repair protected roots = %#v", runner.protectedRoots)
	}
	runner.workspaces = workspaces
	request := localRepairTestRunRequest(pin)
	result, err := runner.Run(leaseCtx, request)
	if err != nil || result.Content != "done" {
		release()
		t.Fatalf("live strict repair result = %#v, %v", result, err)
	}
	seen := provider.snapshot()
	if len(seen) != 1 || seen[0] != positive {
		release()
		t.Fatalf("live strict repair policies = %#v, want exact positive", seen)
	}
	baselineWorkspaceCalls := len(workspaces.Calls())
	baselineProviderCalls := len(seen)
	assertRejectedBeforeEffects := func(name string, ctx context.Context) {
		t.Helper()
		beforeWorkspace := len(workspaces.Calls())
		beforeProvider := len(provider.snapshot())
		if _, runErr := runner.Run(ctx, request); runErr == nil ||
			!strings.Contains(runErr.Error(), "runtime lease is unavailable") {
			t.Fatalf("%s strict repair Run() error = %v", name, runErr)
		}
		if got := len(workspaces.Calls()); got != beforeWorkspace {
			t.Fatalf("%s strict repair reached workspace: %d -> %d", name, beforeWorkspace, got)
		}
		if got := len(provider.snapshot()); got != beforeProvider {
			t.Fatalf("%s strict repair reached provider: %d -> %d", name, beforeProvider, got)
		}
	}
	release()
	assertRejectedBeforeEffects("released A", leaseCtx)
	assertRejectedBeforeEffects("missing", context.Background())
	if _, err := loop.NewControllerLocalRepairRunnerWithRuntimeLease(
		context.Background(),
		"repairer",
		"route",
	); err == nil || !strings.Contains(err.Error(), "runtime lease is unavailable") {
		t.Fatalf("missing-lease strict repair constructor error = %v", err)
	}

	pauseCtx, resume, pauseErr := loop.PauseRuntimeForReloadWithContext(
		context.Background(),
		context.Background(),
	)
	if pauseErr != nil {
		t.Fatal(pauseErr)
	}
	assertRejectedBeforeEffects("pause owner", pauseCtx)
	if _, pauseConstructorErr := loop.NewControllerLocalRepairRunnerWithRuntimeLease(
		pauseCtx,
		"repairer",
		"route",
	); pauseConstructorErr == nil ||
		!strings.Contains(pauseConstructorErr.Error(), "runtime lease is unavailable") {
		resume()
		t.Fatalf("pause-owner strict repair constructor error = %v", pauseConstructorErr)
	}
	resume()

	foreignAgent := &AgentInstance{
		ID:            "repairer",
		Model:         "coding",
		Candidates:    []providers.FallbackCandidate{candidate},
		Provider:      &controllerRepairFactoryProvider{name: "foreign"},
		MaxIterations: 7,
		MaxTokens:     2048,
		Temperature:   0.4,
	}
	foreignLoop := newControllerRepairFactoryLoop(t, &config.Config{}, foreignAgent)
	foreignCtx, releaseForeign, foreignErr := foreignLoop.acquireTrustedRuntimeRoot(
		context.Background(),
	)
	if foreignErr != nil {
		t.Fatal(foreignErr)
	}
	assertRejectedBeforeEffects("foreign loop", foreignCtx)
	releaseForeign()

	loop.mu.Lock()
	loop.runtimeGenerationID++
	loop.mu.Unlock()
	leaseB, releaseB, acquireBErr := loop.acquireTrustedRuntimeRoot(context.Background())
	if acquireBErr != nil {
		t.Fatal(acquireBErr)
	}
	assertRejectedBeforeEffects("generation B", leaseB)
	releaseB()
	if len(workspaces.Calls()) != baselineWorkspaceCalls ||
		len(provider.snapshot()) != baselineProviderCalls {
		t.Fatal("rejected strict repair changed effect counters")
	}

	compatibility, compatibilityErr := loop.NewControllerLocalRepairRunner(
		"repairer",
		"route",
	)
	if compatibilityErr != nil {
		t.Fatal(compatibilityErr)
	}
	compatibility.workspaces = workspaces
	positiveCtx, revokePositive := logger.BindRootDiagnosticPolicy(
		context.Background(),
		positive,
	)
	defer revokePositive()
	if _, compatibilityRunErr := compatibility.Run(
		positiveCtx,
		request,
	); compatibilityRunErr != nil {
		t.Fatal(compatibilityRunErr)
	}
	seen = provider.snapshot()
	if len(seen) != baselineProviderCalls+1 ||
		seen[len(seen)-1] != (logger.DiagnosticPolicy{}) {
		t.Fatalf("compatibility repair policies = %#v, want final zero", seen)
	}
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

	sessionKeys, err := accountrouter.SessionKeys(router.StatePath, routerConfig.Name)
	if err != nil {
		t.Fatalf("read account router sessions: %v", err)
	}
	if len(sessionKeys) != 0 {
		t.Fatalf("account router persisted session affinity = %#v, want none", sessionKeys)
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
