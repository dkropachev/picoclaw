package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type agentInstanceCloseTool struct {
	closeErr   error
	closePanic any
	closeCalls atomic.Int64
}

func (*agentInstanceCloseTool) Name() string        { return "agent_instance_close" }
func (*agentInstanceCloseTool) Description() string { return "agent instance close probe" }
func (*agentInstanceCloseTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (*agentInstanceCloseTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	return tools.SilentResult("closed")
}

func (tool *agentInstanceCloseTool) Close() error {
	tool.closeCalls.Add(1)
	if tool.closePanic != nil {
		panic(tool.closePanic)
	}
	return tool.closeErr
}

type agentInstanceCloseSessionStore struct {
	session.SessionStore
	closeErr   error
	closePanic any
	closeCalls atomic.Int64
}

func (store *agentInstanceCloseSessionStore) Close() error {
	store.closeCalls.Add(1)
	if store.closePanic != nil {
		panic(store.closePanic)
	}
	return store.closeErr
}

func TestResolveAgentWorkspaceMatchesRuntimeResolution(t *testing.T) {
	defaults := &config.AgentDefaults{Workspace: filepath.Join(t.TempDir(), "workspace")}
	for name, agentConfig := range map[string]*config.AgentConfig{
		"implicit main": nil,
		"configured main": {
			ID: "main",
		},
		"named default": {
			ID:      "primary",
			Default: true,
		},
		"named derived": {
			ID: "worker",
		},
		"explicit": {
			ID:        "worker",
			Workspace: filepath.Join(t.TempDir(), "explicit"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			want := resolveAgentWorkspace(agentConfig, defaults)
			if got := ResolveAgentWorkspace(agentConfig, defaults); got != want {
				t.Fatalf("ResolveAgentWorkspace() = %q, want %q", got, want)
			}
		})
	}
}

func TestNewAgentInstance_UsesDefaultsTemperatureAndMaxTokens(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         1234,
				MaxToolIterations: 5,
			},
		},
	}

	configuredTemp := 1.0
	cfg.Agents.Defaults.Temperature = &configuredTemp

	provider := &mockProvider{}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)

	if agent.MaxTokens != 1234 {
		t.Fatalf("MaxTokens = %d, want %d", agent.MaxTokens, 1234)
	}
	if agent.Temperature != 1.0 {
		t.Fatalf("Temperature = %f, want %f", agent.Temperature, 1.0)
	}
}

func TestNewAgentInstance_DefaultsTemperatureWhenZero(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         1234,
				MaxToolIterations: 5,
			},
		},
	}

	configuredTemp := 0.0
	cfg.Agents.Defaults.Temperature = &configuredTemp

	provider := &mockProvider{}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)

	if agent.Temperature != 0.0 {
		t.Fatalf("Temperature = %f, want %f", agent.Temperature, 0.0)
	}
}

func TestNewAgentInstance_DefaultsTemperatureWhenUnset(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         1234,
				MaxToolIterations: 5,
			},
		},
	}

	provider := &mockProvider{}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)

	if agent.Temperature != 0.7 {
		t.Fatalf("Temperature = %f, want %f", agent.Temperature, 0.7)
	}
}

func TestCandidateProviderHelpersCoverEmptyAndDuplicateBranches(t *testing.T) {
	provider := &mockProvider{}
	agent := &AgentInstance{}

	if got := (*AgentInstance)(nil).candidateProvider("model_name:primary"); got != nil {
		t.Fatalf("nil agent candidateProvider() = %#v, want nil", got)
	}
	if got := agent.candidateProvider(" "); got != nil {
		t.Fatalf("blank candidateProvider() = %#v, want nil", got)
	}
	if agent.setCandidateProviderIfAbsent("", provider) {
		t.Fatal("setCandidateProviderIfAbsent(blank key) = true, want false")
	}
	if agent.setCandidateProviderIfAbsent("model_name:primary", nil) {
		t.Fatal("setCandidateProviderIfAbsent(nil provider) = true, want false")
	}
	if !agent.setCandidateProviderIfAbsent("model_name:primary", provider) {
		t.Fatal("setCandidateProviderIfAbsent(first insert) = false, want true")
	}
	if agent.setCandidateProviderIfAbsent("model_name:primary", provider) {
		t.Fatal("setCandidateProviderIfAbsent(duplicate) = true, want false")
	}

	candidate := providers.FallbackCandidate{
		Provider:    "openai",
		Model:       "gpt-4o",
		IdentityKey: "model_name:primary",
	}
	if got := agent.candidateProviderForCandidate(candidate); got != provider {
		t.Fatalf("candidateProviderForCandidate() = %#v, want inserted provider", got)
	}
	keys := candidateProviderKeys(providers.FallbackCandidate{
		Provider:    "openai",
		Model:       "gpt-4o",
		IdentityKey: providers.ModelKey("openai", "gpt-4o"),
	})
	if len(keys) != 1 || keys[0] != providers.ModelKey("openai", "gpt-4o") {
		t.Fatalf("candidateProviderKeys(deduped) = %#v, want single model key", keys)
	}

	out := map[string]providers.LLMProvider{"model_name:primary": provider}
	if registerCandidateProvider(nil, candidate, provider) {
		t.Fatal("registerCandidateProvider(nil map) = true, want false")
	}
	if registerCandidateProvider(out, candidate, nil) {
		t.Fatal("registerCandidateProvider(nil provider) = true, want false")
	}
	if !registerCandidateProvider(out, candidate, provider) {
		t.Fatal("registerCandidateProvider(model-key insert) = false, want true")
	}
	if out[providers.ModelKey("openai", "gpt-4o")] != provider {
		t.Fatal("registerCandidateProvider did not add provider/model key")
	}
}

func TestCandidateProviderUsesAccountAndModelSpecificInstance(t *testing.T) {
	nativeProvider := &mockProvider{}
	overrideProvider := &mockProvider{}
	nativeCandidate := providers.FallbackCandidate{
		Provider:    "github-copilot",
		Model:       "auto",
		IdentityKey: "model_name:copilot-account",
	}
	overrideCandidate := nativeCandidate
	overrideCandidate.Model = "gpt-5.4"

	registered := map[string]providers.LLMProvider{}
	if !registerCandidateProvider(registered, nativeCandidate, nativeProvider) {
		t.Fatal("register native provider = false, want true")
	}
	if !registerCandidateProvider(registered, overrideCandidate, overrideProvider) {
		t.Fatal("register override provider = false, want true")
	}
	agent := &AgentInstance{CandidateProviders: registered}
	if got := agent.candidateProviderForCandidate(nativeCandidate); got != nativeProvider {
		t.Fatalf("native provider = %#v, want native instance", got)
	}
	if got := agent.candidateProviderForCandidate(overrideCandidate); got != overrideProvider {
		t.Fatalf("override provider = %#v, want model-specific instance", got)
	}
}

func TestRegisterCandidateProviderIsAtomicWithConcurrentReaders(t *testing.T) {
	candidate := providers.FallbackCandidate{
		Provider:    "openai",
		Model:       "gpt-5.4",
		IdentityKey: "model_name:concurrent-account",
	}
	keys := candidateProviderKeys(candidate)
	registered := map[string]providers.LLMProvider{}

	const writerCount = 32
	start := make(chan struct{})
	stopReaders := make(chan struct{})
	var (
		writers         sync.WaitGroup
		readers         sync.WaitGroup
		insertions      atomic.Int32
		partialObserved atomic.Bool
	)

	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-start
			for {
				select {
				case <-stopReaders:
					return
				default:
				}

				agentCandidateProvidersMu.RLock()
				var (
					found    int
					selected providers.LLMProvider
				)
				for _, key := range keys {
					provider := registered[key]
					if provider == nil {
						continue
					}
					found++
					if selected == nil {
						selected = provider
					} else if selected != provider {
						partialObserved.Store(true)
					}
				}
				if found != 0 && found != len(keys) {
					partialObserved.Store(true)
				}
				agentCandidateProvidersMu.RUnlock()
			}
		}()
	}

	for i := range writerCount {
		writers.Add(1)
		go func(index int) {
			defer writers.Done()
			<-start
			provider := &sequenceProvider{callCount: index}
			if registerCandidateProvider(registered, candidate, provider) {
				insertions.Add(1)
			}
		}(i)
	}

	close(start)
	writers.Wait()
	close(stopReaders)
	readers.Wait()

	if got := insertions.Load(); got != 1 {
		t.Fatalf("successful registrations = %d, want 1", got)
	}
	if partialObserved.Load() {
		t.Fatal("concurrent reader observed a partial or inconsistent provider key set")
	}

	agentCandidateProvidersMu.RLock()
	defer agentCandidateProvidersMu.RUnlock()
	selected := registered[keys[0]]
	if selected == nil {
		t.Fatal("registered provider = nil")
	}
	for _, key := range keys[1:] {
		if registered[key] != selected {
			t.Fatalf("provider key %q does not reference the atomically registered provider", key)
		}
	}
}

func TestAccountRouterAccountNamesTrimsDedupesAndSkipsUnsupportedBlocks(t *testing.T) {
	if got := accountRouterAccountNames(nil); got != nil {
		t.Fatalf("accountRouterAccountNames(nil) = %#v, want nil", got)
	}

	got := accountRouterAccountNames(&config.AccountRouterConfig{
		Blocks: []config.AccountRouterBlock{
			{Type: config.AccountRouterBlockTypeAccount, Account: " account-a "},
			{Type: config.AccountRouterBlockTypeAccount, Account: "account-a"},
			{Type: config.AccountRouterBlockTypeAccount, Account: " "},
			{
				Type:     config.AccountRouterBlockTypeLoadBalance,
				Accounts: []string{"account-b", "account-a", " account-c "},
			},
			{Type: "unknown", Account: "ignored"},
		},
	})
	want := []string{"account-a", "account-b", "account-c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("accountRouterAccountNames() = %#v, want %#v", got, want)
	}
}

func TestInstanceUtilityHelpersCoverFallbackBranches(t *testing.T) {
	patterns := compilePatterns([]string{"[", "^/tmp/workspace$"})
	if len(patterns) != 1 {
		t.Fatalf("compilePatterns() len = %d, want 1 valid pattern", len(patterns))
	}
	if !patterns[0].MatchString("/tmp/workspace") {
		t.Fatal("compiled pattern does not match expected path")
	}

	mediaPattern := mediaTempDirPattern()
	allowRead := buildAllowReadPatterns(&config.Config{
		Tools: config.ToolsConfig{
			AllowReadPaths: []string{mediaPattern},
		},
	})
	if len(allowRead) != 1 {
		t.Fatalf("buildAllowReadPatterns(existing media pattern) len = %d, want 1", len(allowRead))
	}
	defaultAllowRead := buildAllowReadPatterns(nil)
	if len(defaultAllowRead) != 1 {
		t.Fatalf("buildAllowReadPatterns(nil) len = %d, want 1", len(defaultAllowRead))
	}

	if err := (&AgentInstance{}).Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}
	if err := (*AgentInstance)(nil).Close(); err != nil {
		t.Fatalf("nil Close() error = %v, want nil", err)
	}
	if !(*AgentInstance)(nil).AllowsMCPServer("github") {
		t.Fatal("nil agent AllowsMCPServer() = false, want true")
	}
	agent := &AgentInstance{MCPServerAllowlist: map[string]struct{}{"github": {}}}
	if !agent.AllowsMCPServer(" GitHub ") {
		t.Fatal("allowlisted MCP server was denied")
	}
	if agent.AllowsMCPServer("slack") {
		t.Fatal("non-allowlisted MCP server was allowed")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	if got := expandHome(""); got != "" {
		t.Fatalf("expandHome(empty) = %q, want empty", got)
	}
	if got := expandHome("~"); got != home {
		t.Fatalf("expandHome(~) = %q, want %q", got, home)
	}
	if got := expandHome("~/workspace"); got != filepath.Join(home, "workspace") {
		t.Fatalf("expandHome(~/workspace) = %q, want %q", got, filepath.Join(home, "workspace"))
	}
	if got := expandHome("/tmp/workspace"); got != "/tmp/workspace" {
		t.Fatalf("expandHome(abs) = %q, want /tmp/workspace", got)
	}
}

func TestAgentInstanceCloseJoinsToolAndSessionCleanup(t *testing.T) {
	toolCloseErr := errors.New("tool close failed")
	sessionCloseErr := errors.New("session close failed")
	var product *agentInstanceCloseTool
	factory, err := tools.NewToolFactory(
		tools.ToolDescriptor{
			Name: "agent_instance_close", Description: "agent instance close probe",
			Parameters: map[string]any{"type": "object"},
		},
		tools.ToolTraits{},
		func(tools.ToolBuildContext) (tools.Tool, error) {
			product = &agentInstanceCloseTool{closeErr: toolCloseErr}
			return product, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewOwnedToolRegistry(tools.ToolOwner{Scope: tools.ToolOwnerScopeRegistry})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterFactory(factory); err != nil {
		t.Fatal(err)
	}
	if product == nil {
		t.Fatal("factory did not publish its close probe")
	}
	store := &agentInstanceCloseSessionStore{
		SessionStore: session.NewSessionManager(t.TempDir()),
		closeErr:     sessionCloseErr,
	}
	agent := &AgentInstance{Tools: registry, Sessions: store}
	closeErr := agent.Close()
	if !errors.Is(closeErr, toolCloseErr) || !errors.Is(closeErr, sessionCloseErr) {
		t.Fatalf("Close() error = %v, want both cleanup errors", closeErr)
	}
	if product.closeCalls.Load() != 1 || store.closeCalls.Load() != 1 {
		t.Fatalf("cleanup calls = tool:%d session:%d",
			product.closeCalls.Load(), store.closeCalls.Load())
	}
}

func TestAgentInstanceCloseContainsBothCleanupPanics(t *testing.T) {
	tool := &agentInstanceCloseTool{closePanic: "tool panic"}
	factory, err := tools.NewToolFactory(
		tools.ToolDescriptor{
			Name: "agent_instance_close", Description: "agent instance close probe",
			Parameters: map[string]any{"type": "object"},
		},
		tools.ToolTraits{},
		func(tools.ToolBuildContext) (tools.Tool, error) { return tool, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewOwnedToolRegistry(tools.ToolOwner{Scope: tools.ToolOwnerScopeRegistry})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterFactory(factory); err != nil {
		t.Fatal(err)
	}
	store := &agentInstanceCloseSessionStore{
		SessionStore: session.NewSessionManager(t.TempDir()),
		closePanic:   "session panic",
	}
	closeErr := (&AgentInstance{Tools: registry, Sessions: store}).Close()
	if closeErr == nil || !strings.Contains(closeErr.Error(), "tool panic") ||
		!strings.Contains(closeErr.Error(), "session panic") {
		t.Fatalf("Close() panic error = %v", closeErr)
	}
	if tool.closeCalls.Load() != 1 || store.closeCalls.Load() != 1 {
		t.Fatalf("panic cleanup calls = tool:%d session:%d",
			tool.closeCalls.Load(), store.closeCalls.Load())
	}
	if err := closeAgentResource("none", nil); err != nil {
		t.Fatalf("nil close resource error = %v", err)
	}
}

func TestAgentInstanceConstructionGuardCleansPartialResourcesAndPreservesPanic(t *testing.T) {
	live := tools.NewUpdatePlanTool()
	registry := tools.NewToolRegistry()
	if err := registry.RegisterFactoryBacked(live, tools.NewUpdatePlanToolFactory()); err != nil {
		t.Fatal(err)
	}
	store := &agentInstanceCloseSessionStore{
		SessionStore: session.NewSessionManager(t.TempDir()),
		closeErr:     errors.New("partial session close failed"),
	}
	sentinel := errors.New("construction panic")
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		guard := &agentInstanceConstructionGuard{partial: AgentInstance{
			Tools: registry, Sessions: store,
		}}
		defer guard.cleanupPanic()
		panic(sentinel)
	}()
	if recovered != sentinel {
		t.Fatalf("recovered panic = %v, want %v", recovered, sentinel)
	}
	if registry.Count() != 0 || store.closeCalls.Load() != 1 {
		t.Fatalf("partial cleanup = tools:%d sessions:%d", registry.Count(), store.closeCalls.Load())
	}
}

func TestAgentInstanceCloseReleasesCompatibilitySourceLease(t *testing.T) {
	live := tools.NewUpdatePlanTool()
	factory := tools.NewUpdatePlanToolFactory()
	source := tools.NewToolRegistry()
	if err := source.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatal(err)
	}
	competitor := tools.NewToolRegistry()
	if err := competitor.RegisterFactoryBacked(live, factory); err == nil {
		t.Fatal("live source pointer was not leased before agent close")
	}
	if err := (&AgentInstance{Tools: source}).Close(); err != nil {
		t.Fatal(err)
	}
	if source.Count() != 0 {
		t.Fatal("agent close retained compatibility source tools")
	}
	if err := competitor.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatalf("agent close did not release compatibility source lease: %v", err)
	}
	if err := competitor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNewAgentInstance_ResolveCandidatesFromModelListAlias(t *testing.T) {
	tests := []struct {
		name         string
		aliasName    string
		modelName    string
		apiBase      string
		wantProvider string
		wantModel    string
	}{
		{
			name:         "alias with provider prefix",
			aliasName:    "step-3.5-flash",
			modelName:    "openrouter/stepfun/step-3.5-flash:free",
			apiBase:      "https://openrouter.ai/api/v1",
			wantProvider: "openrouter",
			wantModel:    "stepfun/step-3.5-flash:free",
		},
		{
			name:         "alias without provider prefix",
			aliasName:    "glm-5",
			modelName:    "glm-5",
			apiBase:      "https://api.z.ai/api/coding/paas/v4",
			wantProvider: "openai",
			wantModel:    "glm-5",
		},
		{
			name:         "unknown namespace remains part of model ID",
			aliasName:    "nvidia-gpt",
			modelName:    "vendor/glm-5.1",
			apiBase:      "https://integrate.api.nvidia.com/v1",
			wantProvider: "nvidia",
			wantModel:    "vendor/glm-5.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			cfg := &config.Config{
				Agents: config.AgentsConfig{
					Defaults: config.AgentDefaults{
						Workspace:  tmpDir,
						AccountRef: "account",
						ModelName:  tt.aliasName,
					},
				},
				ModelAliases: []config.ModelAliasConfig{{
					Name:  tt.aliasName,
					Model: tt.modelName,
				}},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "account",
						Provider:  tt.wantProvider,
						APIBase:   tt.apiBase,
						APIKeys:   config.SimpleSecureStrings("test-key"),
						Enabled:   true,
					},
				},
			}

			provider := &mockProvider{}
			agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)

			if len(agent.Candidates) != 1 {
				t.Fatalf(
					"len(Candidates) = %d, want 1 (configuration error: %v)",
					len(agent.Candidates),
					agent.ConfigurationError,
				)
			}
			if agent.Candidates[0].Provider != tt.wantProvider {
				t.Fatalf("candidate provider = %q, want %q", agent.Candidates[0].Provider, tt.wantProvider)
			}
			if agent.Candidates[0].Model != tt.wantModel {
				t.Fatalf("candidate model = %q, want %q", agent.Candidates[0].Model, tt.wantModel)
			}
		})
	}
}

func TestNewAgentInstance_PreservesDistinctLimiterIdentityForSharedResolvedModel(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:      tmpDir,
				AccountRef:     "zhipu-account",
				ModelName:      "primary",
				ModelFallbacks: []string{"backup"},
			},
		},
		ModelAliases: []config.ModelAliasConfig{
			{Name: "primary", Model: "glm-4.7"},
			{Name: "backup", Model: "glm-4.7"},
		},
		ModelList: []*config.ModelConfig{{
			ModelName: "zhipu-account",
			Provider:  "zhipu",
			APIBase:   "http://example.invalid/v1",
			APIKeys:   config.SimpleSecureStrings("test-key"),
			Enabled:   true,
			RPM:       3,
		}},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if len(agent.Candidates) != 2 {
		t.Fatalf("len(Candidates) = %d, want 2", len(agent.Candidates))
	}

	first := agent.Candidates[0]
	second := agent.Candidates[1]
	if first.Provider != "zhipu" || first.Model != "glm-4.7" {
		t.Fatalf("first candidate = %s/%s, want zhipu/glm-4.7", first.Provider, first.Model)
	}
	if second.Provider != "zhipu" || second.Model != "glm-4.7" {
		t.Fatalf("second candidate = %s/%s, want zhipu/glm-4.7", second.Provider, second.Model)
	}
	if first.IdentityKey != accountAliasIdentityKey("zhipu-account", "primary") {
		t.Fatalf("first identity key = %q, want account/primary", first.IdentityKey)
	}
	if second.IdentityKey != accountAliasIdentityKey("zhipu-account", "backup") {
		t.Fatalf("second identity key = %q, want account/backup", second.IdentityKey)
	}
	if first.RPM != 3 {
		t.Fatalf("first RPM = %d, want 3", first.RPM)
	}
	if second.RPM != 3 {
		t.Fatalf("second RPM = %d, want 3", second.RPM)
	}
}

func TestNewAgentInstance_RejectsRawProviderModelAsAlias(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:  tmpDir,
				AccountRef: "nvidia-account",
				ModelName:  "nvidia/z-ai/glm-5.1",
			},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "nvidia-account",
				Provider:  "nvidia",
				Model:     "z-ai/glm-5.1",
				RPM:       7,
			},
		},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if len(agent.Candidates) != 0 {
		t.Fatalf("len(Candidates) = %d, want 0", len(agent.Candidates))
	}
	if agent.ConfigurationError == nil ||
		!strings.Contains(agent.ConfigurationError.Error(), `model alias "nvidia/z-ai/glm-5.1" is not configured`) {
		t.Fatalf("ConfigurationError = %v, want strict unknown alias error", agent.ConfigurationError)
	}
}

func TestNewAgentInstance_AllowsMediaTempDirForReadListAndExec(t *testing.T) {
	workspace := t.TempDir()
	mediaDir := media.TempDir()
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(mediaDir) error = %v", err)
	}

	mediaFile, createErr := os.CreateTemp(mediaDir, "instance-tool-*.txt")
	if createErr != nil {
		t.Fatalf("CreateTemp(mediaDir) error = %v", createErr)
	}
	mediaPath := mediaFile.Name()
	if _, err := mediaFile.WriteString("attachment content"); err != nil {
		mediaFile.Close()
		t.Fatalf("WriteString(mediaFile) error = %v", err)
	}
	if err := mediaFile.Close(); err != nil {
		t.Fatalf("Close(mediaFile) error = %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(mediaPath) })

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:           workspace,
				ModelName:           "test-model",
				RestrictToWorkspace: true,
			},
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileToolConfig{Enabled: true},
			ListDir:  config.ToolConfig{Enabled: true},
			Exec: config.ExecConfig{
				ToolConfig:         config.ToolConfig{Enabled: true},
				EnableDenyPatterns: true,
				AllowRemote:        true,
			},
		},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})

	readTool, readToolOK := agent.Tools.Get("read_file")
	if !readToolOK {
		t.Fatal("read_file tool not registered")
	}
	readResult := readTool.Execute(context.Background(), map[string]any{"path": mediaPath})
	if readResult.IsError {
		t.Fatalf("read_file should allow media temp dir, got: %s", readResult.ForLLM)
	}
	if !strings.Contains(readResult.ForLLM, "attachment content") {
		t.Fatalf("read_file output missing media content: %s", readResult.ForLLM)
	}

	listTool, ok := agent.Tools.Get("list_dir")
	if !ok {
		t.Fatal("list_dir tool not registered")
	}
	listResult := listTool.Execute(context.Background(), map[string]any{"path": mediaDir})
	if listResult.IsError {
		t.Fatalf("list_dir should allow media temp dir, got: %s", listResult.ForLLM)
	}
	if !strings.Contains(listResult.ForLLM, filepath.Base(mediaPath)) {
		t.Fatalf("list_dir output missing media file: %s", listResult.ForLLM)
	}
	child, constructionErr := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "media-reader",
	}, []string{"list_dir", "read_file"})
	if constructionErr != nil {
		t.Fatal(constructionErr)
	}
	defer child.Close()
	childRead, ok := child.Get("read_file")
	if !ok {
		t.Fatal("owner-constructed read_file tool not registered")
	}
	childReadResult := childRead.Execute(context.Background(), map[string]any{"path": mediaPath})
	if childReadResult.IsError || !strings.Contains(childReadResult.ForLLM, "attachment content") {
		t.Fatalf("owner-constructed read_file lost media path policy: %#v", childReadResult)
	}
	childList, ok := child.Get("list_dir")
	if !ok {
		t.Fatal("owner-constructed list_dir tool not registered")
	}
	childListResult := childList.Execute(context.Background(), map[string]any{"path": mediaDir})
	if childListResult.IsError ||
		!strings.Contains(childListResult.ForLLM, filepath.Base(mediaPath)) {
		t.Fatalf("owner-constructed list_dir lost media path policy: %#v", childListResult)
	}

	execTool, ok := agent.Tools.Get("exec")
	if !ok {
		t.Fatal("exec tool not registered")
	}
	execResult := execTool.Execute(context.Background(), map[string]any{
		"action":  "run",
		"command": "cat " + filepath.Base(mediaPath),
		"cwd":     mediaDir,
	})
	if execResult.IsError {
		t.Fatalf("exec should allow media temp dir, got: %s", execResult.ForLLM)
	}
	if !strings.Contains(execResult.ForLLM, "attachment content") {
		t.Fatalf("exec output missing media content: %s", execResult.ForLLM)
	}
}

func TestNewAgentInstanceOwnerFactoriesFreezeOutsideWorkspaceWritePolicy(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	allowWritePaths := []string{
		"^" + regexp.QuoteMeta(filepath.Clean(outside)) + "(?:" +
			regexp.QuoteMeta(string(os.PathSeparator)) + "|$)",
	}
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
			Workspace: workspace, ModelName: "test-model", RestrictToWorkspace: true,
		}},
		Tools: config.ToolsConfig{
			AllowWritePaths: allowWritePaths,
			WriteFile:       config.ToolConfig{Enabled: true},
			AppendFile:      config.ToolConfig{Enabled: true},
			EditFile:        config.ToolConfig{Enabled: true},
		},
	}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	defer agent.Close()
	allowWritePaths[0] = "^" + regexp.QuoteMeta(filepath.Join(workspace, "never")) + "$"
	child, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "outside-writer",
	}, []string{"append_file", "edit_file", "write_file"})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	target := filepath.Join(outside, "owner.txt")
	writeTool, _ := child.Get("write_file")
	if result := writeTool.Execute(context.Background(), map[string]any{
		"path": target, "content": "one", "overwrite": false,
	}); result == nil || result.IsError {
		t.Fatalf("owner write_file lost frozen allow path: %#v", result)
	}
	appendTool, _ := child.Get("append_file")
	if result := appendTool.Execute(context.Background(), map[string]any{
		"path": target, "content": " two",
	}); result == nil || result.IsError {
		t.Fatalf("owner append_file lost frozen allow path: %#v", result)
	}
	editTool, _ := child.Get("edit_file")
	if result := editTool.Execute(context.Background(), map[string]any{
		"path": target, "old_text": "one two", "new_text": "final",
	}); result == nil || result.IsError {
		t.Fatalf("owner edit_file lost frozen allow path: %#v", result)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "final" {
		t.Fatalf("owner write policy result = %q, %v", content, err)
	}
}

func TestNewAgentInstance_RejectsCrossProviderFallbackAliasesForSingleAccount(t *testing.T) {
	workspace := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:      workspace,
				AccountRef:     "openrouter-account",
				ModelName:      "primary",
				ModelFallbacks: []string{"gemini-fallback"},
			},
		},
		ModelAliases: []config.ModelAliasConfig{
			{Name: "primary", Model: "openrouter/mistralai/mistral-small-3.1"},
			{Name: "gemini-fallback", Model: "gemini/gemma-3-27b-it"},
		},
		ModelList: []*config.ModelConfig{{
			ModelName: "openrouter-account",
			Provider:  "openrouter",
			APIBase:   "https://openrouter.ai/api/v1",
			APIKeys:   config.SimpleSecureStrings("sk-or-test"),
			Enabled:   true,
			Workspace: workspace,
		}},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if agent.ConfigurationError == nil ||
		!strings.Contains(agent.ConfigurationError.Error(), "does not match account provider") {
		t.Fatalf(
			"ConfigurationError = %v, want cross-provider alias rejection",
			agent.ConfigurationError,
		)
	}
}

func TestNewAgentInstance_ReadFileModeSelectsSchema(t *testing.T) {
	workspace := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "test-model",
			},
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileToolConfig{
				Enabled:         true,
				Mode:            config.ReadFileModeLines,
				MaxReadFileSize: 4096,
			},
		},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	readTool, ok := agent.Tools.Get("read_file")
	if !ok {
		t.Fatal("read_file tool not registered")
	}

	params := readTool.Parameters()
	props, _ := params["properties"].(map[string]any)
	if _, ok := props["start_line"]; !ok {
		t.Fatalf("expected line-mode schema to expose start_line, got %#v", props)
	}
	if _, ok := props["max_lines"]; !ok {
		t.Fatalf("expected line-mode schema to expose max_lines, got %#v", props)
	}
	if _, ok := props["offset"]; ok {
		t.Fatalf("did not expect line-mode schema to expose offset, got %#v", props)
	}
	if _, ok := props["length"]; ok {
		t.Fatalf("did not expect line-mode schema to expose length, got %#v", props)
	}
	child, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "line-reader",
	}, []string{"read_file"})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	childRead, childReadOK := child.Get("read_file")
	if !childReadOK || reflect.TypeOf(childRead) != reflect.TypeOf(readTool) ||
		!reflect.DeepEqual(childRead.Parameters(), params) {
		t.Fatalf("line-mode child = %T %#v, want %T %#v", childRead, childRead.Parameters(), readTool, params)
	}
}

// write_file copy names append_file/edit_file only when they are registered.
func TestNewAgentInstance_WriteFileCopyReflectsAvailableAltTools(t *testing.T) {
	newCfg := func(editEnabled, appendEnabled bool) *config.Config {
		return &config.Config{
			Agents: config.AgentsConfig{
				Defaults: config.AgentDefaults{
					Workspace: t.TempDir(),
					ModelName: "test-model",
				},
			},
			Tools: config.ToolsConfig{
				WriteFile:  config.ToolConfig{Enabled: true},
				EditFile:   config.ToolConfig{Enabled: editEnabled},
				AppendFile: config.ToolConfig{Enabled: appendEnabled},
			},
		}
	}

	writeToolDesc := func(cfg *config.Config) string {
		agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
		defer agent.Close()
		writeTool, ok := agent.Tools.Get("write_file")
		if !ok {
			t.Fatal("write_file tool not registered")
		}
		child, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
			Scope: tools.ToolOwnerScopeAgent, AgentID: "write-copy",
		}, []string{"write_file"})
		if err != nil {
			t.Fatal(err)
		}
		defer child.Close()
		childWrite, ok := child.Get("write_file")
		if !ok || childWrite.Description() != writeTool.Description() ||
			!reflect.DeepEqual(childWrite.Parameters(), writeTool.Parameters()) {
			t.Fatal("write_file owner construction changed allowlist-derived copy")
		}
		return writeTool.Description()
	}

	t.Run("only write_file exposed", func(t *testing.T) {
		desc := writeToolDesc(newCfg(false, false))
		if strings.Contains(desc, "append_file") || strings.Contains(desc, "edit_file") {
			t.Fatalf("write_file must not reference unavailable tools, got: %q", desc)
		}
	})

	t.Run("only append_file exposed", func(t *testing.T) {
		desc := writeToolDesc(newCfg(false, true))
		if !strings.Contains(desc, "append_file") {
			t.Fatalf("expected write_file to reference append_file, got: %q", desc)
		}
		if strings.Contains(desc, "edit_file") {
			t.Fatalf("write_file must not reference disabled edit_file, got: %q", desc)
		}
	})

	t.Run("both exposed", func(t *testing.T) {
		desc := writeToolDesc(newCfg(true, true))
		if !strings.Contains(desc, "append_file") || !strings.Contains(desc, "edit_file") {
			t.Fatalf("expected write_file to reference both alternatives, got: %q", desc)
		}
	})
}

// Availability follows the per-agent allowlist, not just the enable flag:
// editors enabled globally but hidden by frontmatter must not be named.
func TestNewAgentInstance_WriteFileCopyExcludesAllowlistHiddenAltTools(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENT.md": "---\ntools: [write_file]\n---\n# Agent\n",
	})
	defer cleanupWorkspace(t, workspace)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "test-model",
			},
		},
		Tools: config.ToolsConfig{
			WriteFile:  config.ToolConfig{Enabled: true},
			EditFile:   config.ToolConfig{Enabled: true},
			AppendFile: config.ToolConfig{Enabled: true},
		},
	}

	agent := NewAgentInstance(&config.AgentConfig{
		ID:        "restricted",
		Workspace: workspace,
	}, &cfg.Agents.Defaults, cfg, &mockProvider{})

	if _, ok := agent.Tools.Get("edit_file"); ok {
		t.Fatal("edit_file should be blocked by the allowlist")
	}
	if _, ok := agent.Tools.Get("append_file"); ok {
		t.Fatal("append_file should be blocked by the allowlist")
	}

	writeTool, ok := agent.Tools.Get("write_file")
	if !ok {
		t.Fatal("write_file tool not registered")
	}
	if desc := writeTool.Description(); strings.Contains(desc, "append_file") ||
		strings.Contains(desc, "edit_file") {
		t.Fatalf("write_file must not name allowlist-hidden tools, got: %q", desc)
	}
	child, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "allowlisted-write",
	}, []string{"write_file"})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	childWrite, ok := child.Get("write_file")
	if !ok || childWrite.Description() != writeTool.Description() {
		t.Fatal("owner construction restored allowlist-hidden write alternatives")
	}
}

func TestNewAgentInstance_InvalidExecConfigDoesNotExit(t *testing.T) {
	workspace := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "test-model",
			},
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileToolConfig{Enabled: true},
			Exec: config.ExecConfig{
				ToolConfig:         config.ToolConfig{Enabled: true},
				EnableDenyPatterns: true,
				CustomDenyPatterns: []string{"[invalid-regex"},
			},
		},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if agent == nil {
		t.Fatal("expected agent instance, got nil")
	}

	if _, ok := agent.Tools.Get("exec"); ok {
		t.Fatal("exec tool should not be registered when exec config is invalid")
	}

	if _, ok := agent.Tools.Get("read_file"); !ok {
		t.Fatal("read_file tool should still be registered")
	}
}

func TestNewAgentInstance_RegistersCodexCompatToolsForCodexSurface(t *testing.T) {
	workspace := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "gpt-5",
				Provider:  "openai",
			},
		},
		Tools: config.ToolsConfig{
			Adaptation: config.DefaultToolAdaptationConfig(),
			EditFile:   config.ToolConfig{Enabled: true},
			Exec: config.ExecConfig{
				ToolConfig:         config.ToolConfig{Enabled: true},
				EnableDenyPatterns: true,
				AllowRemote:        true,
			},
		},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})

	for _, name := range []string{"apply_patch", "exec", "exec_command", "write_stdin", "update_plan"} {
		if _, ok := agent.Tools.Get(name); !ok {
			t.Fatalf("expected %q to be registered; tools=%v", name, agent.Tools.List())
		}
	}
}

func TestNewAgentInstance_ResolvesToolAdaptationFromModelListAlias(t *testing.T) {
	workspace := t.TempDir()
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{{
			ModelName: "claude-alias",
			Provider:  "anthropic",
			Model:     "claude-3-5-sonnet",
		}},
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "claude-alias",
				Provider:  "openai",
			},
		},
		Tools: config.ToolsConfig{
			Adaptation: config.DefaultToolAdaptationConfig(),
			EditFile:   config.ToolConfig{Enabled: true},
			Exec: config.ExecConfig{
				ToolConfig:         config.ToolConfig{Enabled: true},
				EnableDenyPatterns: true,
				AllowRemote:        true,
			},
		},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if agent.ToolAdaptation.VisibleToolSurface != config.ToolSurfaceSimple {
		t.Fatalf(
			"VisibleToolSurface = %q, want %q",
			agent.ToolAdaptation.VisibleToolSurface,
			config.ToolSurfaceSimple,
		)
	}
	if _, ok := agent.Tools.Get("exec_command"); ok {
		t.Fatalf("exec_command registered for Anthropic alias; tools=%v", agent.Tools.List())
	}
}

func TestNewAgentInstance_ResolvesToolAdaptationFromAccountRouterOverride(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	if err := auth.SetCredential("openai:work", &auth.AuthCredential{
		AccessToken: "work-token",
		Provider:    "openai",
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}
	workspace := t.TempDir()
	adaptation := config.DefaultToolAdaptationConfig()
	adaptation.ProfileOverrides = []config.ToolAdaptationProfileOverride{{
		Provider:           "openai",
		Model:              "gpt-5.4",
		VisibleToolSurface: config.ToolSurfaceSimple,
	}}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:  workspace,
				AccountRef: "router-1",
				ModelName:  "coding",
			},
		},
		ModelAliases: []config.ModelAliasConfig{{
			Name:  "coding",
			Model: "gpt-5.4",
		}},
		AccountRouters: []config.AccountRouterConfig{{
			Name:    "router-1",
			Enabled: true,
			Entry:   "account",
			Blocks: []config.AccountRouterBlock{{
				ID:      "account",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "credential:openai:work",
			}},
		}},
		Tools: config.ToolsConfig{
			Adaptation: adaptation,
			EditFile:   config.ToolConfig{Enabled: true},
			Exec: config.ExecConfig{
				ToolConfig:         config.ToolConfig{Enabled: true},
				EnableDenyPatterns: true,
				AllowRemote:        true,
			},
		},
	}
	cfg.MaterializeAccountRouterModels()

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if agent.ToolAdaptation.VisibleToolSurface != config.ToolSurfaceSimple {
		t.Fatalf(
			"VisibleToolSurface = %q, want router profile override %q",
			agent.ToolAdaptation.VisibleToolSurface,
			config.ToolSurfaceSimple,
		)
	}
	if _, ok := agent.Tools.Get("exec_command"); ok {
		t.Fatalf("exec_command registered for simple router override; tools=%v", agent.Tools.List())
	}
}

func TestNewAgentInstance_RegistersCodexCompatToolsForAlternateProfileOverride(t *testing.T) {
	workspace := t.TempDir()
	adaptation := config.DefaultToolAdaptationConfig()
	adaptation.ProfileOverrides = []config.ToolAdaptationProfileOverride{{
		Provider:           "gpt",
		Model:              "gpt-5",
		VisibleToolSurface: config.ToolSurfaceCodex,
	}}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "claude-sonnet",
				Provider:  "anthropic",
			},
		},
		Tools: config.ToolsConfig{
			Adaptation: adaptation,
			EditFile:   config.ToolConfig{Enabled: true},
			Exec: config.ExecConfig{
				ToolConfig:         config.ToolConfig{Enabled: true},
				EnableDenyPatterns: true,
				AllowRemote:        true,
			},
		},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if agent.ToolAdaptation.VisibleToolSurface != config.ToolSurfaceSimple {
		t.Fatalf(
			"initial VisibleToolSurface = %q, want %q",
			agent.ToolAdaptation.VisibleToolSurface,
			config.ToolSurfaceSimple,
		)
	}
	for _, name := range []string{"apply_patch", "exec_command", "write_stdin", "update_plan"} {
		if _, ok := agent.Tools.Get(name); !ok {
			t.Fatalf("expected %q for alternate Codex profile; tools=%v", name, agent.Tools.List())
		}
	}
}

func TestNewAgentInstance_CodexApplyPatchPreservesFileToolPermissions(t *testing.T) {
	workspace := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "gpt-5",
				Provider:  "openai",
			},
		},
		Tools: config.ToolsConfig{
			Adaptation: config.DefaultToolAdaptationConfig(),
			EditFile:   config.ToolConfig{Enabled: true},
		},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	patchTool, ok := agent.Tools.Get("apply_patch")
	if !ok {
		t.Fatalf("expected apply_patch to be registered; tools=%v", agent.Tools.List())
	}
	child, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "patch-permissions",
	}, []string{"apply_patch"})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	childPatch, ok := child.Get("apply_patch")
	if !ok {
		t.Fatal("owner-constructed apply_patch is unavailable")
	}
	for label, candidate := range map[string]tools.Tool{"root": patchTool, "child": childPatch} {
		result := candidate.Execute(context.Background(), map[string]any{
			"patch": "*** Begin Patch\n*** Add File: note.txt\n+nope\n*** End Patch",
		})
		if !result.IsError {
			t.Fatalf("%s apply_patch add succeeded even though write_file is disabled", label)
		}
		if !strings.Contains(result.ForLLM, "write_file is disabled") {
			t.Fatalf("%s error = %q, want write_file disabled", label, result.ForLLM)
		}
	}
}

func TestNewAgentInstance_DoesNotRegisterCodexCompatToolsForPicoClawSurface(t *testing.T) {
	workspace := t.TempDir()
	adaptation := config.DefaultToolAdaptationConfig()
	adaptation.VisibleToolSurface = config.ToolSurfacePicoClaw

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "gpt-5",
				Provider:  "openai",
			},
		},
		Tools: config.ToolsConfig{
			Adaptation: adaptation,
			EditFile:   config.ToolConfig{Enabled: true},
			Exec: config.ExecConfig{
				ToolConfig:         config.ToolConfig{Enabled: true},
				EnableDenyPatterns: true,
				AllowRemote:        true,
			},
		},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})

	if _, ok := agent.Tools.Get("exec"); !ok {
		t.Fatal("expected native exec to be registered")
	}
	for _, name := range []string{"apply_patch", "exec_command", "write_stdin", "update_plan"} {
		if _, ok := agent.Tools.Get(name); ok {
			t.Fatalf("did not expect %q to be registered; tools=%v", name, agent.Tools.List())
		}
	}
}

func TestNewAgentInstance_RegistersCodexCompatToolsForRuntimePromotion(t *testing.T) {
	workspace := t.TempDir()
	adaptation := config.DefaultToolAdaptationConfig()
	adaptation.CacheSensitiveAPIs = config.ToolCacheSensitivityNever
	adaptation.ApplyVisibleChanges = config.ToolVisibleChangeImmediate

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "unknown-local-model",
				Provider:  "local",
			},
		},
		Tools: config.ToolsConfig{
			Adaptation: adaptation,
			EditFile:   config.ToolConfig{Enabled: true},
			Exec: config.ExecConfig{
				ToolConfig:         config.ToolConfig{Enabled: true},
				EnableDenyPatterns: true,
				AllowRemote:        true,
			},
		},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if agent.ToolAdaptation.VisibleToolSurface != config.ToolSurfacePicoClaw {
		t.Fatalf(
			"VisibleToolSurface = %q, want %q",
			agent.ToolAdaptation.VisibleToolSurface,
			config.ToolSurfacePicoClaw,
		)
	}
	if !agent.ToolAdaptation.RuntimePromotion {
		t.Fatal("RuntimePromotion = false, want true")
	}

	for _, name := range []string{"apply_patch", "exec_command", "write_stdin", "update_plan"} {
		if _, ok := agent.Tools.Get(name); !ok {
			t.Fatalf("expected %q to be registered for runtime promotion; tools=%v", name, agent.Tools.List())
		}
	}
}

func TestNewAgentInstance_UsesFrontmatterModelAndSkills(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENT.md": `---
model: frontmatter-model
skills: [frontmatter-skill]
mcpServers: [GitHub, filesystem]
---
# Agent

Use frontmatter identity.
`,
	})
	defer cleanupWorkspace(t, workspace)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "default-model",
			},
		},
	}

	agent := NewAgentInstance(&config.AgentConfig{
		ID:        "research",
		Workspace: workspace,
		Model: &config.AgentModelConfig{
			Primary: "config-model",
		},
		Skills: []string{"config-skill"},
	}, &cfg.Agents.Defaults, cfg, &mockProvider{})

	if agent.Model != "frontmatter-model" {
		t.Fatalf("agent.Model = %q, want frontmatter-model", agent.Model)
	}
	if len(agent.SkillsFilter) != 1 || agent.SkillsFilter[0] != "frontmatter-skill" {
		t.Fatalf("agent.SkillsFilter = %v, want [frontmatter-skill]", agent.SkillsFilter)
	}
	if !agent.AllowsMCPServer("github") {
		t.Fatal("expected github MCP server to be allowed from frontmatter")
	}
	if !agent.AllowsMCPServer("FILESYSTEM") {
		t.Fatal("expected filesystem MCP server matching to be case-insensitive")
	}
	if agent.AllowsMCPServer("slack") {
		t.Fatal("expected slack MCP server to be blocked by frontmatter allowlist")
	}
}

func TestNewAgentInstance_UsesConfiguredAccountForFrontmatterAlias(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENT.md": `---
model: claude-frontmatter
---
# Agent
`,
	})
	defer cleanupWorkspace(t, workspace)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:  workspace,
				AccountRef: "openai-account",
				ModelName:  "default-model",
			},
		},
		ModelAliases: []config.ModelAliasConfig{
			{Name: "default-model", Model: "gpt-5.4"},
			{Name: "claude-frontmatter", Model: "claude-3-7-sonnet"},
		},
		ModelList: []*config.ModelConfig{{
			ModelName: "openai-account",
			Provider:  "openai",
			APIBase:   "http://example.invalid/v1",
			APIKeys:   config.SimpleSecureStrings("test-key"),
			Enabled:   true,
			Workspace: workspace,
		}},
	}

	defaultProvider := &mockProvider{}
	agent := NewAgentInstance(&config.AgentConfig{
		ID:        "research",
		Workspace: workspace,
	}, &cfg.Agents.Defaults, cfg, defaultProvider)

	if agent.Model != "claude-frontmatter" {
		t.Fatalf("agent.Model = %q, want %q", agent.Model, "claude-frontmatter")
	}
	if len(agent.Candidates) != 1 {
		t.Fatalf("len(agent.Candidates) = %d, want 1", len(agent.Candidates))
	}
	if got := agent.Candidates[0].Provider; got != "openai" {
		t.Fatalf("primary candidate provider = %q, want %q", got, "openai")
	}
	if got := agent.Candidates[0].Model; got != "claude-3-7-sonnet" {
		t.Fatalf("primary candidate model = %q, want %q", got, "claude-3-7-sonnet")
	}
	if agent.Provider == defaultProvider {
		t.Fatal("frontmatter alias incorrectly became bound to the default-model provider")
	}
	if got := agent.candidateProviderForCandidate(agent.Candidates[0]); got == defaultProvider {
		t.Fatal("frontmatter alias incorrectly reused a provider bound to the default model")
	} else if got == nil {
		t.Fatal("frontmatter alias has no concrete provider")
	}
}

func TestNewAgentInstance_SuppressesToolDiscoveryPromptWhenNoMCPServersSelected(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENT.md": `---
mcpServers: []
---
# Agent
`,
	})
	defer cleanupWorkspace(t, workspace)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "default-model",
			},
		},
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				ToolConfig: config.ToolConfig{Enabled: true},
				Discovery: config.ToolDiscoveryConfig{
					Enabled:  true,
					UseBM25:  true,
					UseRegex: false,
				},
				Servers: map[string]config.MCPServerConfig{
					"github": {Enabled: true},
				},
			},
		},
	}

	agent := NewAgentInstance(&config.AgentConfig{
		ID:        "research",
		Workspace: workspace,
	}, &cfg.Agents.Defaults, cfg, &mockProvider{})

	if agent.AllowsMCPServer("github") {
		t.Fatal("expected empty mcpServers allowlist to deny all servers")
	}
	messages := agent.ContextBuilder.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "hello"})
	if prompt := messages[0].Content; strings.Contains(prompt, tools.BM25SearchToolName) {
		t.Fatalf("expected no tool discovery prompt when no MCP servers are selected, got %q", prompt)
	}
}

func TestNewAgentInstance_DoesNotAdvertiseDiscoveryBeforeMCPAdmission(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENT.md": `---
mcpServers: [github]
---
# Agent
`,
	})
	defer cleanupWorkspace(t, workspace)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "default-model",
			},
		},
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				ToolConfig: config.ToolConfig{Enabled: true},
				Discovery: config.ToolDiscoveryConfig{
					Enabled:  true,
					UseBM25:  true,
					UseRegex: false,
				},
				Servers: map[string]config.MCPServerConfig{
					"github": {Enabled: true},
				},
			},
		},
	}

	agent := NewAgentInstance(&config.AgentConfig{
		ID:        "research",
		Workspace: workspace,
	}, &cfg.Agents.Defaults, cfg, &mockProvider{})

	messages := agent.ContextBuilder.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "hello"})
	if prompt := messages[0].Content; strings.Contains(prompt, tools.BM25SearchToolName) {
		t.Fatalf("discovery prompt appeared before successful MCP admission: %q", prompt)
	}
}

func TestNewAgentInstance_InvalidFrontmatterFailsClosedForToolsAndMCPServers(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENT.md": `---
tools: [read_file
mcpServers: [github]
---
# Agent
`,
	})
	defer cleanupWorkspace(t, workspace)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "default-model",
			},
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileToolConfig{Enabled: true},
		},
	}

	agent := NewAgentInstance(&config.AgentConfig{
		ID:        "research",
		Workspace: workspace,
	}, &cfg.Agents.Defaults, cfg, &mockProvider{})

	if _, ok := agent.Tools.Get("read_file"); ok {
		t.Fatal("expected malformed frontmatter to fail closed and block read_file")
	}
	if agent.AllowsMCPServer("github") {
		t.Fatal("expected malformed frontmatter to fail closed for MCP servers")
	}
}

func TestNewAgentInstance_ExplicitEmptyToolsFieldBlocksAllTools(t *testing.T) {
	tests := []struct {
		name         string
		toolsSnippet string
	}{
		{
			name:         "empty list",
			toolsSnippet: "tools: []",
		},
		{
			name:         "blank field",
			toolsSnippet: "tools:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := setupWorkspace(t, map[string]string{
				"AGENT.md": `---
` + tt.toolsSnippet + `
---
# Agent
`,
			})
			defer cleanupWorkspace(t, workspace)

			cfg := &config.Config{
				Agents: config.AgentsConfig{
					Defaults: config.AgentDefaults{
						Workspace: workspace,
						ModelName: "default-model",
					},
				},
				Tools: config.ToolsConfig{
					ReadFile: config.ReadFileToolConfig{Enabled: true},
					ListDir:  config.ToolConfig{Enabled: true},
				},
			}

			agent := NewAgentInstance(&config.AgentConfig{
				ID:        "research",
				Workspace: workspace,
			}, &cfg.Agents.Defaults, cfg, &mockProvider{})

			if got := agent.Tools.List(); len(got) != 0 {
				t.Fatalf("agent tools = %v, want no registered tools", got)
			}
			if _, ok := agent.Tools.Get("read_file"); ok {
				t.Fatal("expected read_file to be blocked by explicit empty tools field")
			}
			if _, ok := agent.Tools.Get("list_dir"); ok {
				t.Fatal("expected list_dir to be blocked by explicit empty tools field")
			}
		})
	}
}
