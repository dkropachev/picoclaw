package agent

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type mockRegistryProvider struct{}

func TestAgentRegistryConstructionGuardClosesCompletedAgentsAndPreservesPanic(t *testing.T) {
	live := tools.NewUpdatePlanTool()
	toolRegistry := tools.NewToolRegistry()
	if err := toolRegistry.RegisterFactoryBacked(live, tools.NewUpdatePlanToolFactory()); err != nil {
		t.Fatal(err)
	}
	store := &agentInstanceCloseSessionStore{
		SessionStore: session.NewSessionManager(t.TempDir()),
	}
	partial := &AgentRegistry{agents: map[string]*AgentInstance{
		"first": {ID: "first", Tools: toolRegistry, Sessions: store},
	}}
	sentinel := errors.New("registry construction panic")
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		defer (&agentRegistryConstructionGuard{registry: partial}).cleanupPanic()
		panic(sentinel)
	}()
	if recovered != sentinel {
		t.Fatalf("recovered panic = %v, want %v", recovered, sentinel)
	}
	if toolRegistry.Count() != 0 || store.closeCalls.Load() != 1 {
		t.Fatalf("partial registry cleanup = tools:%d sessions:%d",
			toolRegistry.Count(), store.closeCalls.Load())
	}
}

func (m *mockRegistryProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "mock", FinishReason: "stop"}, nil
}

func testCfg(t *testing.T, agents []config.AgentConfig) *config.Config {
	t.Helper()
	root := t.TempDir()
	for index := range agents {
		if agents[index].Workspace == "" {
			agents[index].Workspace = filepath.Join(root, "agent-"+agents[index].ID)
		}
	}
	return &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         filepath.Join(root, "default"),
				ModelName:         "gpt-4",
				MaxTokens:         8192,
				MaxToolIterations: 10,
			},
			List: agents,
		},
	}
}

func TestNewAgentRegistry_ImplicitMain(t *testing.T) {
	cfg := testCfg(t, nil)
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	ids := registry.ListAgentIDs()
	if len(ids) != 1 || ids[0] != "main" {
		t.Errorf("expected implicit main agent, got %v", ids)
	}

	agent, ok := registry.GetAgent("main")
	if !ok || agent == nil {
		t.Fatal("expected to find 'main' agent")
	}
	if agent.ID != "main" {
		t.Errorf("agent.ID = %q, want 'main'", agent.ID)
	}
}

func TestNewAgentRegistry_ExplicitAgents(t *testing.T) {
	cfg := testCfg(t, []config.AgentConfig{
		{ID: "sales", Default: true, Name: "Sales Bot"},
		{ID: "support", Name: "Support Bot"},
	})
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	ids := registry.ListAgentIDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 agents, got %d: %v", len(ids), ids)
	}

	sales, ok := registry.GetAgent("sales")
	if !ok || sales == nil {
		t.Fatal("expected to find 'sales' agent")
	}
	if sales.Name != "Sales Bot" {
		t.Errorf("sales.Name = %q, want 'Sales Bot'", sales.Name)
	}

	support, ok := registry.GetAgent("support")
	if !ok || support == nil {
		t.Fatal("expected to find 'support' agent")
	}
}

func TestNewAgentRegistryDoesNotBindDefaultProviderToDifferentAgentSelection(t *testing.T) {
	bootstrap := &mockRegistryProvider{}
	cfg := config.DefaultConfig()
	workspace := t.TempDir()
	cfg.Agents.Defaults.Workspace = filepath.Join(workspace, "main")
	cfg.Agents.Defaults.AccountRef = "openai-default"
	cfg.Agents.Defaults.ModelName = "default"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true, Workspace: filepath.Join(workspace, "main")},
		{
			ID: "reviewer", Workspace: filepath.Join(workspace, "reviewer"),
			AccountRef: "anthropic-review",
			Model:      &config.AgentModelConfig{Primary: "review"},
		},
	}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "openai-default",
			Provider:  "openai",
			APIKeys:   config.SimpleSecureStrings("sk-openai"),
			Enabled:   true,
		},
		{
			ModelName: "anthropic-review",
			Provider:  "anthropic",
			APIKeys:   config.SimpleSecureStrings("sk-anthropic"),
			Enabled:   true,
		},
	}
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "default", Model: "openai/gpt-5.4"},
		{Name: "review", Model: "anthropic/claude-sonnet-4-6"},
	}

	registry := NewAgentRegistry(cfg, bootstrap)
	main, ok := registry.GetAgent("main")
	if !ok || len(main.Candidates) != 1 {
		t.Fatalf("main agent candidates = %#v", main)
	}
	if got := main.candidateProviderForCandidate(main.Candidates[0]); got != bootstrap {
		t.Fatalf("main provider = %T, want bootstrap provider", got)
	}

	reviewer, ok := registry.GetAgent("reviewer")
	if !ok || len(reviewer.Candidates) != 1 {
		t.Fatalf("reviewer agent candidates = %#v", reviewer)
	}
	if got := reviewer.candidateProviderForCandidate(reviewer.Candidates[0]); got == bootstrap {
		t.Fatal("reviewer selection was incorrectly bound to the default account provider")
	} else if got == nil {
		t.Fatal("reviewer selection has no provider")
	}
}

func TestAgentRegistry_GetAgent_Normalize(t *testing.T) {
	cfg := testCfg(t, []config.AgentConfig{
		{ID: "my-agent", Default: true},
	})
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	agent, ok := registry.GetAgent("My-Agent")
	if !ok || agent == nil {
		t.Fatal("expected to find agent with normalized ID")
	}
	if agent.ID != "my-agent" {
		t.Errorf("agent.ID = %q, want 'my-agent'", agent.ID)
	}
}

func TestAgentRegistry_GetDefaultAgent(t *testing.T) {
	cfg := testCfg(t, []config.AgentConfig{
		{ID: "main"},
		{ID: "beta", Default: true},
	})
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	// GetDefaultAgent first checks for "main", then returns any
	agent := registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected a default agent")
	}
	if agent.ID != "beta" {
		t.Fatalf("default agent = %q, want configured default %q", agent.ID, "beta")
	}
}

func TestAgentRegistry_GetDefaultAgentPrefersFirstConfiguredAgentOverMain(t *testing.T) {
	cfg := testCfg(t, []config.AgentConfig{
		{ID: "beta"},
		{ID: "main"},
	})
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	agent := registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected a default agent")
	}
	if agent.ID != "beta" {
		t.Fatalf("default agent = %q, want first configured agent %q", agent.ID, "beta")
	}
}

func TestAgentRegistry_CanSpawnSubagent(t *testing.T) {
	cfg := testCfg(t, []config.AgentConfig{
		{
			ID:      "parent",
			Default: true,
			Subagents: &config.SubagentsConfig{
				AllowAgents: []string{"child1", "child2"},
			},
		},
		{ID: "child1"},
		{ID: "child2"},
		{ID: "restricted"},
	})
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	if !registry.CanSpawnSubagent("parent", "child1") {
		t.Error("expected parent to be allowed to spawn child1")
	}
	if !registry.CanSpawnSubagent("parent", "child2") {
		t.Error("expected parent to be allowed to spawn child2")
	}
	if registry.CanSpawnSubagent("parent", "restricted") {
		t.Error("expected parent to NOT be allowed to spawn restricted")
	}
	if registry.CanSpawnSubagent("child1", "child2") {
		t.Error("expected child1 to NOT be allowed to spawn (no subagents config)")
	}
}

func TestAgentRegistry_CanSpawnSubagent_Wildcard(t *testing.T) {
	cfg := testCfg(t, []config.AgentConfig{
		{
			ID:      "admin",
			Default: true,
			Subagents: &config.SubagentsConfig{
				AllowAgents: []string{"*"},
			},
		},
		{ID: "any-agent"},
	})
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	if !registry.CanSpawnSubagent("admin", "any-agent") {
		t.Error("expected wildcard to allow spawning any agent")
	}
	if !registry.CanSpawnSubagent("admin", "nonexistent") {
		t.Error("expected wildcard to allow spawning even nonexistent agents")
	}
	if registry.CanSpawnSubagent("admin", "admin") {
		t.Error("expected wildcard delegation to exclude the parent agent itself")
	}
}

func TestAgentInstance_Model(t *testing.T) {
	model := &config.AgentModelConfig{Primary: "claude-opus"}
	cfg := testCfg(t, []config.AgentConfig{
		{ID: "custom", Default: true, Model: model},
	})
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	agent, _ := registry.GetAgent("custom")
	if agent.Model != "claude-opus" {
		t.Errorf("agent.Model = %q, want 'claude-opus'", agent.Model)
	}
}

func TestAgentInstance_FallbackInheritance(t *testing.T) {
	cfg := testCfg(t, []config.AgentConfig{
		{ID: "inherit", Default: true},
	})
	cfg.Agents.Defaults.ModelFallbacks = []string{"openai/gpt-4o-mini", "anthropic/haiku"}
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	agent, _ := registry.GetAgent("inherit")
	if len(agent.Fallbacks) != 2 {
		t.Errorf("expected 2 fallbacks inherited from defaults, got %d", len(agent.Fallbacks))
	}
}

func TestAgentInstance_FallbackExplicitEmpty(t *testing.T) {
	model := &config.AgentModelConfig{
		Primary:   "gpt-4",
		Fallbacks: []string{}, // explicitly empty = disable
	}
	cfg := testCfg(t, []config.AgentConfig{
		{ID: "no-fallback", Default: true, Model: model},
	})
	cfg.Agents.Defaults.ModelFallbacks = []string{"should-not-inherit"}
	registry := NewAgentRegistry(cfg, &mockRegistryProvider{})

	agent, _ := registry.GetAgent("no-fallback")
	if len(agent.Fallbacks) != 0 {
		t.Errorf(
			"expected 0 fallbacks (explicit empty), got %d: %v",
			len(agent.Fallbacks),
			agent.Fallbacks,
		)
	}
}

func TestNewAgentLoop_AgentToolAllowlistFiltersRuntimeTools(t *testing.T) {
	mainWorkspace := setupWorkspace(t, map[string]string{
		"AGENT.md": "# Agent\nMain agent.\n",
	})
	defer cleanupWorkspace(t, mainWorkspace)

	researchWorkspace := setupWorkspace(t, map[string]string{
		"AGENT.md": `---
tools: [read_file, write_file, web_search, web_fetch, message]
skills: [deep-research]
---
# Agent

Research agent.
`,
	})
	defer cleanupWorkspace(t, researchWorkspace)

	cfg := testCfg(t, []config.AgentConfig{
		{ID: "main", Default: true, Workspace: mainWorkspace},
		{
			ID:        "research",
			Workspace: researchWorkspace,
		},
	})
	cfg.Agents.Defaults.Workspace = mainWorkspace
	cfg.Tools.ReadFile.Enabled = true
	cfg.Tools.WriteFile.Enabled = true
	cfg.Tools.ListDir.Enabled = true
	cfg.Tools.Exec.Enabled = true
	cfg.Tools.Message.Enabled = true
	cfg.Tools.Web.Enabled = true
	cfg.Tools.Web.DuckDuckGo.Enabled = true
	cfg.Tools.WebFetch.Enabled = true
	cfg.Tools.Spawn.Enabled = true
	cfg.Tools.Subagent.Enabled = true

	al := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), &mockRegistryProvider{})
	defer al.Close()

	research, ok := al.GetRegistry().GetAgent("research")
	if !ok || research == nil {
		t.Fatal("expected research agent")
	}

	got := research.Tools.List()
	want := []string{"message", "read_file", "web_fetch", "web_search", "write_file"}
	if !slices.Equal(got, want) {
		t.Fatalf("research tools = %v, want %v", got, want)
	}

	for _, blocked := range []string{"exec", "list_dir", "spawn", "subagent"} {
		if _, ok := research.Tools.Get(blocked); ok {
			t.Fatalf("expected %q to be blocked by allowlist", blocked)
		}
	}
}

func TestNewAgentLoop_AgentToolAllowlistRequiresExactRuntimeToolNames(t *testing.T) {
	mainWorkspace := setupWorkspace(t, map[string]string{
		"AGENT.md": "# Agent\nMain agent.\n",
	})
	defer cleanupWorkspace(t, mainWorkspace)

	researchWorkspace := setupWorkspace(t, map[string]string{
		"AGENT.md": `---
tools: [web]
---
# Agent

Research agent.
`,
	})
	defer cleanupWorkspace(t, researchWorkspace)

	cfg := testCfg(t, []config.AgentConfig{
		{ID: "main", Default: true, Workspace: mainWorkspace},
		{
			ID:        "research",
			Workspace: researchWorkspace,
		},
	})
	cfg.Agents.Defaults.Workspace = mainWorkspace
	cfg.Tools.Web.Enabled = true
	cfg.Tools.Web.DuckDuckGo.Enabled = true

	al := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), &mockRegistryProvider{})
	defer al.Close()

	research, ok := al.GetRegistry().GetAgent("research")
	if !ok || research == nil {
		t.Fatal("expected research agent")
	}

	if _, ok := research.Tools.Get("web_search"); ok {
		t.Fatal("web_search should not be registered when allowlist contains only web")
	}
	if slices.Contains(research.Tools.List(), "web_search") {
		t.Fatalf("research tools = %v, expected web_search to be absent", research.Tools.List())
	}
}
