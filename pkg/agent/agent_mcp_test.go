// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/mcp"
	agenttools "github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func boolPtr(b bool) *bool { return &b }

func TestMCPRuntimeResetClearsState(t *testing.T) {
	var rt mcpRuntime
	manager := mcp.NewManager()
	rt.setManager(manager)
	rt.setInitErr(errors.New("stale init error"))
	rt.initOnce.Do(func() {})

	got := rt.reset()
	if got != manager {
		t.Fatalf("reset() manager = %p, want %p", got, manager)
	}
	if rt.hasManager() {
		t.Fatal("expected manager to be cleared after reset")
	}
	if err := rt.getInitErr(); err != nil {
		t.Fatalf("getInitErr() = %v, want nil", err)
	}

	reran := false
	rt.initOnce.Do(func() { reran = true })
	if !reran {
		t.Fatal("expected initOnce to be reset")
	}
}

func TestReloadProviderAndConfig_ResetsMCPRuntime(t *testing.T) {
	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()

	manager := mcp.NewManager()
	al.mcp.setManager(manager)
	al.mcp.setInitErr(errors.New("stale init error"))
	al.mcp.initOnce.Do(func() {})

	if !al.mcp.hasManager() {
		t.Fatal("expected MCP manager to exist before reload")
	}

	if err := al.ReloadProviderAndConfig(context.Background(), &mockProvider{}, cfg); err != nil {
		t.Fatalf("ReloadProviderAndConfig() error = %v", err)
	}

	if al.mcp.hasManager() {
		t.Fatal("expected MCP manager to be cleared when reloaded config has MCP disabled")
	}
	if err := al.mcp.getInitErr(); err != nil {
		t.Fatalf("getInitErr() = %v, want nil", err)
	}

	reran := false
	al.mcp.initOnce.Do(func() { reran = true })
	if !reran {
		t.Fatal("expected MCP initOnce to be reset after reload")
	}
}

func TestServerIsDeferred(t *testing.T) {
	tests := []struct {
		name             string
		discoveryEnabled bool
		serverDeferred   *bool
		want             bool
	}{
		// --- global false always wins: per-server deferred is ignored ---
		{
			name:             "global false: per-server deferred=true is ignored",
			discoveryEnabled: false,
			serverDeferred:   boolPtr(true),
			want:             false,
		},
		{
			name:             "global false: per-server deferred=false stays false",
			discoveryEnabled: false,
			serverDeferred:   boolPtr(false),
			want:             false,
		},
		// --- global true: per-server override applies ---
		{
			name:             "global true: per-server deferred=false opts out",
			discoveryEnabled: true,
			serverDeferred:   boolPtr(false),
			want:             false,
		},
		{
			name:             "global true: per-server deferred=true stays true",
			discoveryEnabled: true,
			serverDeferred:   boolPtr(true),
			want:             true,
		},
		// --- no per-server override: fall back to global ---
		{
			name:             "no per-server field, global discovery enabled",
			discoveryEnabled: true,
			serverDeferred:   nil,
			want:             true,
		},
		{
			name:             "no per-server field, global discovery disabled",
			discoveryEnabled: false,
			serverDeferred:   nil,
			want:             false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverCfg := config.MCPServerConfig{Deferred: tt.serverDeferred}
			got := serverIsDeferred(tt.discoveryEnabled, serverCfg)
			if got != tt.want {
				t.Errorf("serverIsDeferred(discoveryEnabled=%v, deferred=%v) = %v, want %v",
					tt.discoveryEnabled, tt.serverDeferred, got, tt.want)
			}
		})
	}
}

func TestRegisterMCPServerPromptContributorUsesActualRegisteredToolCount(t *testing.T) {
	cb := NewContextBuilder(t.TempDir())
	agent := &AgentInstance{ContextBuilder: cb}

	registerMCPServerPromptContributor("research", agent, "github", 0, false)
	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "hello"})
	if prompt := messages[0].Content; strings.Contains(prompt, "MCP server `github`") {
		t.Fatalf("expected no MCP prompt when no tools were registered, got %q", prompt)
	}

	registerMCPServerPromptContributor("research", agent, "github", 2, false)
	messages = cb.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "hello"})
	prompt := messages[0].Content
	if !strings.Contains(prompt, "MCP server `github` is connected") {
		t.Fatalf("expected MCP prompt for registered tools, got %q", prompt)
	}
	if !strings.Contains(prompt, "It contributes 2 tool(s)") {
		t.Fatalf("expected actual registered tool count in prompt, got %q", prompt)
	}
}

func TestToolRegistryIncludesReportsOnlyRegisteredTools(t *testing.T) {
	registry := agenttools.NewToolRegistry()
	registry.SetAllowlist([]string{"mcp_github_search"})

	registry.RegisterHidden(&allowlistTestTool{name: "mcp_github_search"})
	registry.RegisterHidden(&allowlistTestTool{name: "mcp_github_create_issue"})

	if !toolRegistryIncludes(registry, "mcp_github_search") {
		t.Fatal("expected hidden registered MCP tool to be included")
	}
	if toolRegistryIncludes(registry, "mcp_github_create_issue") {
		t.Fatal("blocked MCP tool should not be included")
	}
}

func TestValidateCanonicalMCPToolNamesRejectsAmbiguousCaseVariants(t *testing.T) {
	servers := map[string]*mcp.ServerConnection{
		"GitHub": {
			Tools: []*sdkmcp.Tool{{Name: "Search"}},
		},
		"github": {
			Tools: []*sdkmcp.Tool{{Name: "search"}},
		},
	}

	err := validateCanonicalMCPToolNames(servers)
	if !errors.Is(err, mcp.ErrCanonicalToolNameCollision) {
		t.Fatalf("validateCanonicalMCPToolNames() error = %v, want canonical collision", err)
	}
}

func TestValidateCanonicalMCPToolNamesAllowsDistinctLossyNames(t *testing.T) {
	servers := map[string]*mcp.ServerConnection{
		"GitHub Server": {
			Tools: []*sdkmcp.Tool{
				{Name: "issues.list"},
				{Name: "issues/list"},
			},
		},
	}

	if err := validateCanonicalMCPToolNames(servers); err != nil {
		t.Fatalf("validateCanonicalMCPToolNames() error = %v, want nil", err)
	}
}

func TestEnsureMCPInitializedRejectsRegistryCollisionBeforeExposure(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		mode := "eager"
		if deferred {
			mode = "deferred"
		}
		t.Run(mode, func(t *testing.T) {
			loop := mcpRegistryTestLoop(t, deferred, []string{"available", "search"})
			defer loop.Close()

			agent := loop.registry.GetDefaultAgent()
			collisionName := mcp.CanonicalToolName("github", "search")
			existing := &allowlistTestTool{name: collisionName}
			agent.Tools.Register(existing)

			err := loop.ensureMCPInitialized(context.Background())
			if !errors.Is(err, mcp.ErrCanonicalToolNameCollision) {
				t.Fatalf("ensureMCPInitialized() error = %v, want canonical collision", err)
			}
			if loop.mcp.getManager() != nil {
				t.Fatal("MCP manager was retained after registry collision")
			}
			got, ok := agent.Tools.GetRegistered(collisionName)
			if !ok || got != existing {
				t.Fatalf("collision occupant = %T %p, want original tool %p", got, got, existing)
			}
			availableName := mcp.CanonicalToolName("github", "available")
			if agent.Tools.HasRegistered(availableName) {
				t.Fatalf("non-conflicting wrapper %q was partially exposed", availableName)
			}

			if !deferred {
				readiness := loop.ResolveWorkflowDependency(
					context.Background(),
					workflows.WorkflowDependencyOccurrence{
						Kind: workflows.WorkflowDependencyKindMCP,
						Name: "github/search",
					},
				)
				if readiness != workflows.WorkflowDependencyReadinessNameCollision {
					t.Fatalf("readiness = %q, want name_collision", readiness)
				}
			}
		})
	}
}

func TestValidateMCPToolRegistrationsRejectsDifferentRegisteredMCPIdentity(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.MCP = config.MCPConfig{
		ToolConfig: config.ToolConfig{Enabled: true},
		Servers: map[string]config.MCPServerConfig{
			"a": {Enabled: true},
		},
	}
	registry := NewAgentRegistry(cfg, nil)
	defer registry.Close()
	previousManager := mcp.NewManager()
	defer func() {
		if err := previousManager.Close(); err != nil {
			t.Errorf("close previous MCP manager: %v", err)
		}
	}()
	registry.GetDefaultAgent().Tools.Register(agenttools.NewMCPTool(
		previousManager,
		"a_b",
		&sdkmcp.Tool{Name: "c"},
	))

	err := validateMCPToolRegistrations(
		map[string]*mcp.ServerConnection{
			"a": {Tools: []*sdkmcp.Tool{{Name: "b_c"}}},
		},
		cfg.Tools.MCP,
		registry,
	)
	if !errors.Is(err, mcp.ErrCanonicalToolNameCollision) {
		t.Fatalf("validateMCPToolRegistrations() error = %v, want canonical collision", err)
	}
	var collision *mcp.CanonicalToolNameCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("error type = %T, want canonical collision details", err)
	}
	if collision.First != (mcp.ToolIdentity{Server: "a_b", Tool: "c"}) ||
		collision.Second != (mcp.ToolIdentity{Server: "a", Tool: "b_c"}) {
		t.Fatalf("collision identities = %#v / %#v", collision.First, collision.Second)
	}
}

func TestEnsureMCPInitializedAllowsExactRegisteredMCPIdentity(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		mode := "eager"
		if deferred {
			mode = "deferred"
		}
		t.Run(mode, func(t *testing.T) {
			loop := mcpRegistryTestLoop(t, deferred, []string{"search"})
			defer loop.Close()

			agent := loop.registry.GetDefaultAgent()
			previousManager := mcp.NewManager()
			t.Cleanup(func() {
				if err := previousManager.Close(); err != nil {
					t.Errorf("close previous MCP manager: %v", err)
				}
			})
			previous := agenttools.NewMCPTool(
				previousManager,
				"github",
				&sdkmcp.Tool{Name: "search"},
			)
			if deferred {
				agent.Tools.RegisterHidden(previous)
			} else {
				agent.Tools.Register(previous)
			}

			if err := loop.ensureMCPInitialized(context.Background()); err != nil {
				t.Fatalf("ensureMCPInitialized() error = %v, want nil", err)
			}
			name := mcp.CanonicalToolName("github", "search")
			registered, ok := agent.Tools.GetRegistered(name)
			if !ok {
				t.Fatalf("exact MCP wrapper %q was not registered", name)
			}
			wrapped, ok := registered.(*agenttools.MCPTool)
			if !ok {
				t.Fatalf("registered tool = %T, want *tools.MCPTool", registered)
			}
			serverName, toolName := wrapped.MCPIdentity()
			if serverName != "github" || toolName != "search" {
				t.Fatalf(
					"MCP identity = %q/%q, want github/search",
					serverName,
					toolName,
				)
			}
			if registered == previous {
				t.Fatal("exact idempotent registration retained the stale MCP manager wrapper")
			}
			_, callable := agent.Tools.Get(name)
			if callable == deferred {
				t.Fatalf("callable = %v with deferred = %v", callable, deferred)
			}
		})
	}
}

func mcpRegistryTestLoop(
	t *testing.T,
	deferred bool,
	toolNames []string,
) *AgentLoop {
	t.Helper()
	httpServer := workflowDependencyMCPTestServer(t, toolNames)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.MCP = config.MCPConfig{
		ToolConfig: config.ToolConfig{Enabled: true},
		Discovery: config.ToolDiscoveryConfig{
			Enabled: deferred,
			UseBM25: deferred,
		},
		Servers: map[string]config.MCPServerConfig{
			"github": {
				Enabled:  true,
				Deferred: boolPtr(deferred),
				Type:     "http",
				URL:      httpServer.URL,
			},
		},
	}
	return &AgentLoop{
		cfg:      cfg,
		registry: NewAgentRegistry(cfg, nil),
	}
}

func TestFilterMCPConfigServersCaseInsensitivePreservesOriginalKeys(t *testing.T) {
	mcpCfg := config.MCPConfig{
		Servers: map[string]config.MCPServerConfig{
			"GitHub":     {Enabled: true},
			"filesystem": {Enabled: true},
			"Slack":      {Enabled: true},
		},
	}
	allowed := map[string]struct{}{
		"github":     {},
		"FILESYSTEM": {},
	}

	filtered := filterMCPConfigServers(mcpCfg, allowed)

	if len(filtered.Servers) != 2 {
		t.Fatalf("filtered.Servers = %v, want 2 entries", filtered.Servers)
	}
	if _, ok := filtered.Servers["GitHub"]; !ok {
		t.Fatal("expected original GitHub config key to be preserved")
	}
	if _, ok := filtered.Servers["filesystem"]; !ok {
		t.Fatal("expected filesystem config key to be preserved")
	}
	if _, ok := filtered.Servers["github"]; ok {
		t.Fatal("did not expect normalized github key to replace original config key")
	}
	if _, ok := filtered.Servers["Slack"]; ok {
		t.Fatal("did not expect unallowed Slack server")
	}
}

func TestAgentHasDiscoverableMCPServers(t *testing.T) {
	deferredFalse := false
	cfg := &config.Config{
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				ToolConfig: config.ToolConfig{Enabled: true},
				Discovery: config.ToolDiscoveryConfig{
					Enabled:  true,
					UseBM25:  true,
					UseRegex: false,
				},
				Servers: map[string]config.MCPServerConfig{
					"github":     {Enabled: true},
					"filesystem": {Enabled: true, Deferred: &deferredFalse},
				},
			},
		},
	}

	tests := []struct {
		name    string
		allowed map[string]struct{}
		want    bool
	}{
		{
			name: "nil allowlist includes discoverable enabled server",
			want: true,
		},
		{
			name:    "empty allowlist denies all servers",
			allowed: map[string]struct{}{},
			want:    false,
		},
		{
			name: "selected server discoverable",
			allowed: map[string]struct{}{
				"github": {},
			},
			want: true,
		},
		{
			name: "selected server opted out of discovery",
			allowed: map[string]struct{}{
				"filesystem": {},
			},
			want: false,
		},
		{
			name: "unknown allowlist server matches nothing",
			allowed: map[string]struct{}{
				"slack": {},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentHasDiscoverableMCPServers(cfg, tt.allowed); got != tt.want {
				t.Fatalf("agentHasDiscoverableMCPServers() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnsureMCPInitialized_LoadFailureSetsInitErr(t *testing.T) {
	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()

	cfg.Tools = config.ToolsConfig{
		MCP: config.MCPConfig{
			ToolConfig: config.ToolConfig{Enabled: true},
			Servers: map[string]config.MCPServerConfig{
				"broken": {
					Enabled: true,
					Command: "picoclaw-command-that-does-not-exist-for-mcp-tests",
				},
			},
		},
	}

	err := al.ensureMCPInitialized(context.Background())
	if err == nil {
		t.Fatal("ensureMCPInitialized() error = nil, want load failure")
	}
	if !strings.Contains(err.Error(), "failed to load MCP servers") {
		t.Fatalf("ensureMCPInitialized() error = %q, want wrapped load failure", err.Error())
	}

	initErr := al.mcp.getInitErr()
	if initErr == nil {
		t.Fatal("getInitErr() = nil, want cached load failure")
	}
	if !strings.Contains(initErr.Error(), "failed to load MCP servers") {
		t.Fatalf("getInitErr() = %q, want wrapped load failure", initErr.Error())
	}
	if al.mcp.getManager() != nil {
		t.Fatal("expected MCP manager to remain nil after load failure")
	}

	err = al.ensureMCPInitialized(context.Background())
	if err == nil {
		t.Fatal("second ensureMCPInitialized() error = nil, want cached load failure")
	}
	if !strings.Contains(err.Error(), "failed to load MCP servers") {
		t.Fatalf("second ensureMCPInitialized() error = %q, want wrapped load failure", err.Error())
	}
}
