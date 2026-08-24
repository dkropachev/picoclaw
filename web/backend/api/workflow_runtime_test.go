package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWebWorkflowRuntimeForwardsReadOnlySessionCapture(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	msgBus := bus.NewMessageBus()
	loop := agent.NewAgentLoop(cfg, msgBus, workflowRuntimeTestProvider{})
	runner := &webWorkflowRuntimeRunner{loop: loop, msgBus: msgBus}
	t.Cleanup(func() {
		if err := runner.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	instance := loop.GetRegistry().GetDefaultAgent()
	if instance == nil {
		t.Fatal("default agent is nil")
	}
	scope := session.SessionScope{
		Version: session.ScopeVersionV1, AgentID: "main", Channel: "web",
		Account: "default", Dimensions: []string{"fixture"},
		Values: map[string]string{"fixture": "private-capture"},
	}
	key := session.BuildSessionKey(scope)
	metadata, ok := instance.Sessions.(session.MetadataAwareSessionStore)
	if !ok {
		t.Fatalf("session store %T is not metadata aware", instance.Sessions)
	}
	metadata.EnsureSessionMetadata(
		key,
		session.CloneScope(&scope),
		[]string{"agent:main:web:direct:private-capture"},
	)
	instance.Sessions.AddMessage(key, "user", "frozen web capture evidence")

	frozen, err := runner.CaptureReadOnlySession(context.Background(), workflows.ReadOnlySessionRef{
		AgentID: "main",
		Session: "agent:main:web:direct:private-capture",
	})
	if err != nil {
		t.Fatalf("CaptureReadOnlySession() error = %v", err)
	}
	if frozen == nil || frozen.AgentID != "main" || frozen.Snapshot.Key != key {
		t.Fatalf("frozen session = %#v, want exact main/%q capture", frozen, key)
	}
	if !strings.HasPrefix(frozen.HistoryRevision, "sha256:") {
		t.Fatalf("history revision = %q, want opaque sha256 revision", frozen.HistoryRevision)
	}
}

func TestWebWorkflowRuntimeForwardsRepositoryReviewAccount(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.AccountRef = "review-default"
	cfg.Agents.Defaults.ModelName = "review-model"
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name: "review-model", Model: "gpt-default",
		AccountOverrides: map[string]string{"review-alt": "gpt-alt"},
	}}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "review-default", Provider: "openai", Model: "gpt-default",
			APIBase: "http://example.invalid/v1", APIKeys: config.SimpleSecureStrings("default-key"),
			Enabled: true,
		},
		{
			ModelName: "review-alt", Provider: "openai", Model: "gpt-alt",
			APIBase: "http://example.invalid/v1", APIKeys: config.SimpleSecureStrings("alt-key"),
			Enabled: true,
		},
	}
	msgBus := bus.NewMessageBus()
	loop := agent.NewAgentLoop(cfg, msgBus, workflowRuntimeTestProvider{})
	runner := &webWorkflowRuntimeRunner{loop: loop, msgBus: msgBus}
	t.Cleanup(func() {
		if err := runner.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	profile, err := runner.ResolveRepositoryReviewProfile(
		t.Context(), "main", "review-alt", nil,
	)
	if err != nil || profile.AccountRef != "review-alt" || profile.Revision == "" {
		t.Fatalf("repository review profile=%#v err=%v", profile, err)
	}
}

func TestWebWorkflowRuntimeResolvesHumanTaskWithoutInitializingAgentLoop(t *testing.T) {
	runner := &webWorkflowRuntimeRunner{}
	if got := runner.ResolveWorkflowDependency(
		context.Background(),
		workflows.WorkflowDependencyOccurrence{
			Kind: workflows.WorkflowDependencyKindHuman,
			Name: "task",
		},
	); got != workflows.WorkflowDependencyReadinessReady {
		t.Fatalf("human/task readiness = %q, want ready", got)
	}
	if runner.loop != nil || runner.msgBus != nil {
		t.Fatal("human/task readiness initialized the agent runtime")
	}
	if got := runner.ResolveWorkflowDependency(
		context.Background(),
		workflows.WorkflowDependencyOccurrence{
			Kind: workflows.WorkflowDependencyKindHuman,
			Name: "unknown",
		},
	); got != workflows.WorkflowDependencyReadinessNotFound {
		t.Fatalf("unknown human primitive readiness = %q, want not_found", got)
	}
}

func TestWorkflowRuntimeRunnersForConfigUseCapturedConfigAfterDiskMutation(
	t *testing.T,
) {
	workspace := t.TempDir()
	newConfig := func(agentID string) *config.Config {
		cfg := config.DefaultConfig()
		cfg.Agents.Defaults.Workspace = workspace
		cfg.Agents.Defaults.AccountRef = "openai-test"
		cfg.Agents.Defaults.ModelName = "coding"
		cfg.ModelAliases = []config.ModelAliasConfig{{
			Name:  "coding",
			Model: "openai/gpt-4.1",
		}}
		cfg.Agents.List = []config.AgentConfig{{
			ID:        agentID,
			Default:   true,
			Workspace: workspace,
		}}
		cfg.ModelList = []*config.ModelConfig{{
			ModelName: "openai-test",
			Provider:  "openai",
			APIBase:   "http://127.0.0.1:1/v1",
			Enabled:   true,
		}}
		return cfg
	}

	configPath := filepath.Join(t.TempDir(), "config.json")
	captured := newConfig("captured-agent")
	if err := config.SaveConfig(configPath, captured); err != nil {
		t.Fatalf("SaveConfig(captured) error = %v", err)
	}
	runners := workflowRuntimeRunnersForConfig(configPath, captured)
	runner, ok := runners.Agents.(*webWorkflowRuntimeRunner)
	if !ok {
		t.Fatalf("captured agent runner type = %T", runners.Agents)
	}
	t.Cleanup(func() {
		if err := runner.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	current := newConfig("current-agent")
	if err := config.SaveConfig(configPath, current); err != nil {
		t.Fatalf("SaveConfig(current) error = %v", err)
	}
	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(current) error = %v", err)
	}
	if len(reloaded.Agents.List) != 1 ||
		reloaded.Agents.List[0].ID != "current-agent" {
		t.Fatalf("disk agents = %#v, want current-agent", reloaded.Agents.List)
	}

	capturedReadiness := runner.ResolveWorkflowDependency(
		context.Background(),
		workflows.WorkflowDependencyOccurrence{
			Kind: workflows.WorkflowDependencyKindAgent,
			Name: "captured-agent",
		},
	)
	if capturedReadiness != workflows.WorkflowDependencyReadinessReady {
		t.Fatalf(
			"captured agent readiness = %q, want %q",
			capturedReadiness,
			workflows.WorkflowDependencyReadinessReady,
		)
	}
	currentReadiness := runner.ResolveWorkflowDependency(
		context.Background(),
		workflows.WorkflowDependencyOccurrence{
			Kind: workflows.WorkflowDependencyKindAgent,
			Name: "current-agent",
		},
	)
	if currentReadiness != workflows.WorkflowDependencyReadinessNotFound {
		t.Fatalf(
			"current disk agent readiness = %q, want %q",
			currentReadiness,
			workflows.WorkflowDependencyReadinessNotFound,
		)
	}
}

type workflowRuntimeTestProvider struct{}

func (workflowRuntimeTestProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "unused"}, nil
}

func (workflowRuntimeTestProvider) GetDefaultModel() string {
	return "test-model"
}

type workflowRuntimeTestTool struct {
	name         string
	executeCalls *int
}

func (t workflowRuntimeTestTool) Name() string {
	return t.name
}

func (workflowRuntimeTestTool) Description() string {
	return "workflow runtime test tool"
}

func (workflowRuntimeTestTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (t workflowRuntimeTestTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	if t.executeCalls != nil {
		*t.executeCalls++
	}
	return tools.NewToolResult("initialized")
}

type workflowRuntimeTestMCPManager struct {
	calls      int
	serverName string
	toolName   string
}

func (m *workflowRuntimeTestMCPManager) CallTool(
	_ context.Context,
	serverName, toolName string,
	_ map[string]any,
) (*sdkmcp.CallToolResult, error) {
	m.calls++
	m.serverName = serverName
	m.toolName = toolName
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "initialized"}},
	}, nil
}

func TestWebWorkflowRuntimeInitializesMCPBeforeFirstExplicitStep(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	msgBus := bus.NewMessageBus()
	loop := agent.NewAgentLoop(cfg, msgBus, workflowRuntimeTestProvider{})
	runner := &webWorkflowRuntimeRunner{
		loop:   loop,
		msgBus: msgBus,
	}
	t.Cleanup(func() {
		if err := runner.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	initializeCalls := 0
	mcpManager := &workflowRuntimeTestMCPManager{}
	runner.initializeMCP = func(_ context.Context, initializedLoop *agent.AgentLoop) error {
		initializeCalls++
		if initializedLoop != loop {
			t.Fatal("initializer received a different agent loop")
		}
		initializedLoop.GetRegistry().GetDefaultAgent().Tools.Register(
			tools.NewMCPTool(
				mcpManager,
				"github",
				&sdkmcp.Tool{
					Name: "issues.list",
					InputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"state": map[string]any{"type": "string"},
						},
					},
				},
			),
		)
		return nil
	}

	workflow, err := workflows.Parse([]byte(`
name: MCP first step
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: mcp/github/issues.list
        with:
          state: open
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	result, err := (&workflows.Executor{
		WorkspaceDir: cfg.Agents.Defaults.Workspace,
		Tools:        runner,
	}).Run(context.Background(), workflows.RunRequest{
		Workflow:    workflow,
		WorkflowRef: "inline",
	})
	if err != nil {
		t.Fatalf("workflow Run() error = %v", err)
	}
	if result.Status != workflows.RunStatusSucceeded {
		t.Fatalf("workflow status = %q, want succeeded", result.Status)
	}
	if initializeCalls != 1 {
		t.Fatalf("initializer calls = %d, want 1", initializeCalls)
	}
	if mcpManager.calls != 1 {
		t.Fatalf("MCP tool execute calls = %d, want 1", mcpManager.calls)
	}
	if mcpManager.serverName != "github" || mcpManager.toolName != "issues.list" {
		t.Fatalf(
			"MCP execution identity = %q/%q, want github/issues.list",
			mcpManager.serverName,
			mcpManager.toolName,
		)
	}
}

func TestWebWorkflowRuntimeDoesNotInitializeMCPForOrdinaryToolStep(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	msgBus := bus.NewMessageBus()
	loop := agent.NewAgentLoop(cfg, msgBus, workflowRuntimeTestProvider{})
	loop.GetRegistry().GetDefaultAgent().Tools.Register(
		workflowRuntimeTestTool{name: "ordinary_test_tool"},
	)
	runner := &webWorkflowRuntimeRunner{
		loop:   loop,
		msgBus: msgBus,
		initializeMCP: func(context.Context, *agent.AgentLoop) error {
			t.Fatal("ordinary tool step initialized MCP")
			return nil
		},
	}
	t.Cleanup(func() {
		if err := runner.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	outputs, err := runner.RunTool(context.Background(), workflows.ToolRequest{
		Name: "ordinary_test_tool",
	})
	if err != nil {
		t.Fatalf("RunTool() error = %v", err)
	}
	if outputs["text"] != "initialized" {
		t.Fatalf("outputs = %#v, want ordinary tool output", outputs)
	}
}
