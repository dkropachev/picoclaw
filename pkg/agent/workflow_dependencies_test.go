package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type workflowDependencyTestTool struct {
	name string
}

func (t workflowDependencyTestTool) Name() string {
	return t.name
}

func (workflowDependencyTestTool) Description() string {
	return "workflow dependency test tool"
}

func (workflowDependencyTestTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (workflowDependencyTestTool) Execute(
	context.Context,
	map[string]any,
) *tools.ToolResult {
	return tools.SilentResult("")
}

func TestResolveWorkflowDependencyAgentsFunctionsAndTools(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	loop := workflowDependencyTestLoop(cfg)
	defaultAgent := loop.registry.GetDefaultAgent()
	defaultAgent.Tools.Register(workflowDependencyTestTool{name: "ready_tool"})
	defaultAgent.Tools.RegisterHidden(workflowDependencyTestTool{name: "hidden_tool"})
	loop.cfg.Tools.MCP.Enabled = true

	tests := []struct {
		name       string
		dependency workflows.WorkflowDependencyOccurrence
		want       workflows.WorkflowDependencyReadinessCode
	}{
		{
			name: "implicit main agent",
			dependency: workflows.WorkflowDependencyOccurrence{
				Kind: workflows.WorkflowDependencyKindAgent,
				Name: "MAIN",
			},
			want: workflows.WorkflowDependencyReadinessReady,
		},
		{
			name: "missing agent",
			dependency: workflows.WorkflowDependencyOccurrence{
				Kind: workflows.WorkflowDependencyKindAgent,
				Name: "missing",
			},
			want: workflows.WorkflowDependencyReadinessNotFound,
		},
		{
			name: "native function",
			dependency: workflows.WorkflowDependencyOccurrence{
				Kind: workflows.WorkflowDependencyKindFunction,
				Name: "workflow.artifact",
			},
			want: workflows.WorkflowDependencyReadinessReady,
		},
		{
			name: "custom function without runner",
			dependency: workflows.WorkflowDependencyOccurrence{
				Kind: workflows.WorkflowDependencyKindFunction,
				Name: "custom",
			},
			want: workflows.WorkflowDependencyReadinessNotFound,
		},
		{
			name: "callable tool",
			dependency: workflows.WorkflowDependencyOccurrence{
				Kind: workflows.WorkflowDependencyKindTool,
				Name: "ready_tool",
			},
			want: workflows.WorkflowDependencyReadinessReady,
		},
		{
			name: "hidden tool is not publish ready",
			dependency: workflows.WorkflowDependencyOccurrence{
				Kind: workflows.WorkflowDependencyKindTool,
				Name: "hidden_tool",
			},
			want: workflows.WorkflowDependencyReadinessDisabled,
		},
		{
			name: "workflow tool recursion is blocked",
			dependency: workflows.WorkflowDependencyOccurrence{
				Kind: workflows.WorkflowDependencyKindTool,
				Name: "workflow",
			},
			want: workflows.WorkflowDependencyReadinessNotAllowed,
		},
		{
			name: "reusable readiness is structural",
			dependency: workflows.WorkflowDependencyOccurrence{
				Kind: workflows.WorkflowDependencyKindReusable,
				Name: "workflows/shared.yml",
			},
			want: workflows.WorkflowDependencyReadinessReady,
		},
		{
			name: "human task primitive",
			dependency: workflows.WorkflowDependencyOccurrence{
				Kind: workflows.WorkflowDependencyKindHuman,
				Name: "task",
			},
			want: workflows.WorkflowDependencyReadinessReady,
		},
		{
			name: "unknown human primitive",
			dependency: workflows.WorkflowDependencyOccurrence{
				Kind: workflows.WorkflowDependencyKindHuman,
				Name: "unknown",
			},
			want: workflows.WorkflowDependencyReadinessNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := loop.ResolveWorkflowDependency(
				context.Background(),
				test.dependency,
			); got != test.want {
				t.Fatalf("ResolveWorkflowDependency() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolveWorkflowDependencyDetectsNormalizedAgentCollision(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "Review Agent", Workspace: t.TempDir()},
		{ID: "review-agent", Workspace: t.TempDir()},
	}
	loop := workflowDependencyTestLoop(cfg)
	got := loop.ResolveWorkflowDependency(
		context.Background(),
		workflows.WorkflowDependencyOccurrence{
			Kind: workflows.WorkflowDependencyKindAgent,
			Name: "review-agent",
		},
	)
	if got != workflows.WorkflowDependencyReadinessNameCollision {
		t.Fatalf("readiness = %q, want name_collision", got)
	}
}

func TestResolveWorkflowDependencyMCPConfigurationStates(t *testing.T) {
	eager := false
	tests := []struct {
		name      string
		configure func(*config.Config, *AgentLoop)
		want      workflows.WorkflowDependencyReadinessCode
	}{
		{
			name: "global disabled",
			configure: func(cfg *config.Config, _ *AgentLoop) {
				cfg.Tools.MCP.Enabled = false
			},
			want: workflows.WorkflowDependencyReadinessDisabled,
		},
		{
			name: "server disabled",
			configure: func(cfg *config.Config, _ *AgentLoop) {
				cfg.Tools.MCP.Enabled = true
				cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
					"github": {Enabled: false},
				}
			},
			want: workflows.WorkflowDependencyReadinessDisabled,
		},
		{
			name: "server not configured",
			configure: func(cfg *config.Config, _ *AgentLoop) {
				cfg.Tools.MCP.Enabled = true
				cfg.Tools.MCP.Servers = nil
			},
			want: workflows.WorkflowDependencyReadinessNotConfigured,
		},
		{
			name: "server deferred",
			configure: func(cfg *config.Config, _ *AgentLoop) {
				cfg.Tools.MCP.Enabled = true
				cfg.Tools.MCP.Discovery.Enabled = true
				cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
					"github": {Enabled: true},
				}
			},
			want: workflows.WorkflowDependencyReadinessDisabled,
		},
		{
			name: "server disallowed",
			configure: func(cfg *config.Config, loop *AgentLoop) {
				cfg.Tools.MCP.Enabled = true
				cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
					"github": {Enabled: true, Deferred: &eager},
				}
				loop.registry.GetDefaultAgent().MCPServerAllowlist = map[string]struct{}{}
			},
			want: workflows.WorkflowDependencyReadinessNotAllowed,
		},
		{
			name: "eager server cannot connect",
			configure: func(cfg *config.Config, _ *AgentLoop) {
				cfg.Tools.MCP.Enabled = true
				cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
					"github": {Enabled: true, Deferred: &eager},
				}
			},
			want: workflows.WorkflowDependencyReadinessNotConnected,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = t.TempDir()
			loop := workflowDependencyTestLoop(cfg)
			test.configure(cfg, loop)
			got := loop.ResolveWorkflowDependency(
				context.Background(),
				workflows.WorkflowDependencyOccurrence{
					Kind: workflows.WorkflowDependencyKindMCP,
					Name: "github/comment",
				},
			)
			if got != test.want {
				t.Fatalf("ResolveWorkflowDependency() = %q, want %q", got, test.want)
			}
			loop.Close()
		})
	}
}

func TestResolveWorkflowDependencyMCPRequiresExactConnectedIdentity(t *testing.T) {
	t.Run("boundary-canonical alternate is not ready", func(t *testing.T) {
		loop := workflowDependencyConnectedMCPTestLoop(t, map[string][]string{
			"a_b": {"other"},
			"a":   {"b_c"},
		})

		got := loop.ResolveWorkflowDependency(
			context.Background(),
			workflows.WorkflowDependencyOccurrence{
				Kind: workflows.WorkflowDependencyKindMCP,
				Name: "a_b/c",
			},
		)
		if got != workflows.WorkflowDependencyReadinessNotFound {
			t.Fatalf("readiness = %q, want not_found", got)
		}
	})

	t.Run("exact identity is ready", func(t *testing.T) {
		loop := workflowDependencyConnectedMCPTestLoop(t, map[string][]string{
			"a_b": {"c"},
		})

		got := loop.ResolveWorkflowDependency(
			context.Background(),
			workflows.WorkflowDependencyOccurrence{
				Kind: workflows.WorkflowDependencyKindMCP,
				Name: "a_b/c",
			},
		)
		if got != workflows.WorkflowDependencyReadinessReady {
			t.Fatalf("readiness = %q, want ready", got)
		}
	})

	t.Run("global boundary collision still fails", func(t *testing.T) {
		loop := workflowDependencyConnectedMCPTestLoop(t, map[string][]string{
			"a_b": {"c"},
			"a":   {"b_c"},
		})

		got := loop.ResolveWorkflowDependency(
			context.Background(),
			workflows.WorkflowDependencyOccurrence{
				Kind: workflows.WorkflowDependencyKindMCP,
				Name: "a_b/c",
			},
		)
		if got != workflows.WorkflowDependencyReadinessNameCollision {
			t.Fatalf("readiness = %q, want name_collision", got)
		}
	})
}

func workflowDependencyConnectedMCPTestLoop(
	t *testing.T,
	serverTools map[string][]string,
) *AgentLoop {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Servers = make(map[string]config.MCPServerConfig, len(serverTools))

	manager := picomcp.NewManager()
	for serverName, toolNames := range serverTools {
		httpServer := workflowDependencyMCPTestServer(t, toolNames)
		serverCfg := config.MCPServerConfig{
			Enabled: true,
			Type:    "http",
			URL:     httpServer.URL,
		}
		cfg.Tools.MCP.Servers[serverName] = serverCfg
		if err := manager.ConnectServer(context.Background(), serverName, serverCfg); err != nil {
			t.Fatalf("ConnectServer(%q) error = %v", serverName, err)
		}
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close MCP manager: %v", err)
		}
	})

	loop := workflowDependencyTestLoop(cfg)
	loop.mcp.setManager(manager)
	loop.mcp.initOnce.Do(func() {})
	defaultAgent := loop.registry.GetDefaultAgent()
	for serverName, connection := range manager.GetServers() {
		for _, tool := range connection.Tools {
			defaultAgent.Tools.Register(tools.NewMCPTool(manager, serverName, tool))
		}
	}
	return loop
}

func workflowDependencyMCPTestServer(
	t *testing.T,
	toolNames []string,
) *httptest.Server {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "workflow-dependency-test",
		Version: "1.0.0",
	}, nil)
	for _, toolName := range toolNames {
		sdkmcp.AddTool(
			server,
			&sdkmcp.Tool{Name: toolName},
			func(
				context.Context,
				*sdkmcp.CallToolRequest,
				map[string]any,
			) (*sdkmcp.CallToolResult, any, error) {
				return &sdkmcp.CallToolResult{
					Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}},
				}, nil, nil
			},
		)
	}
	handler := sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return server },
		nil,
	)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer
}

func workflowDependencyTestLoop(cfg *config.Config) *AgentLoop {
	return &AgentLoop{
		cfg:      cfg,
		registry: NewAgentRegistry(cfg, nil),
	}
}
