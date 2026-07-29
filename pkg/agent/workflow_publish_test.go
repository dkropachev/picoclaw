package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const agentWorkflowPublishRootYAML = `name: Agent publish root
on:
  manual: {}
jobs:
  shared:
    uses: workflows/shared.yml
`

const agentWorkflowPublishChildYAML = `name: Agent publish child
on:
  workflow_call: {}
jobs:
  work:
    runs-on: picoclaw
    steps:
      - uses: tool/ready_tool
`

type agentWorkflowPublishReadyTool struct{}

func (agentWorkflowPublishReadyTool) Name() string {
	return "ready_tool"
}

func (agentWorkflowPublishReadyTool) Description() string {
	return "ready workflow publish test tool"
}

func (agentWorkflowPublishReadyTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (agentWorkflowPublishReadyTool) Execute(
	context.Context,
	map[string]any,
) *tools.ToolResult {
	return tools.SilentResult("")
}

func TestNewWorkflowToolPublishesWithProductionDependencyGate(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	definitionsDir := "automation/definitions"
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Workflows.Enabled = true
	cfg.Workflows.DefinitionsDir = definitionsDir
	loop := &AgentLoop{
		cfg:      cfg,
		registry: NewAgentRegistry(cfg, nil),
	}
	defer loop.Close()

	agent := loop.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is nil")
	}
	agent.Tools.Register(agentWorkflowPublishReadyTool{})
	childPath := filepath.Join(workspace, definitionsDir, "shared.yml")
	if err := os.MkdirAll(filepath.Dir(childPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(definitions) error = %v", err)
	}
	if err := os.WriteFile(childPath, []byte(agentWorkflowPublishChildYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(child) error = %v", err)
	}
	runtime := workflowRuntimeCompatibility()
	if _, err := workflows.RevalidateLocal(
		ctx,
		workspace,
		runtime,
		workflows.WithDefinitionsDir(definitionsDir),
	); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}
	tool := newWorkflowTool(loop, agent.ID, agent)
	for _, args := range []map[string]any{
		{
			"action":     "dev_start",
			"target_ref": "workflows/published.yml",
			"prompt":     "publish through the agent tool",
		},
		{
			"action": "dev_revise",
			"yaml":   agentWorkflowPublishRootYAML,
		},
		{
			"action": "dev_test",
		},
		{
			"action": "dev_publish",
		},
	} {
		result := tool.Execute(ctx, args)
		if result == nil || result.IsError {
			t.Fatalf("%s result = %#v", args["action"], result)
		}
	}

	targetPath := filepath.Join(workspace, definitionsDir, "published.yml")
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("ReadFile(published) error = %v", err)
	}
	if string(data) != agentWorkflowPublishRootYAML {
		t.Fatalf("published bytes = %q, want exact draft", data)
	}
	if _, err := os.Stat(filepath.Join(workspace, "workflows", "published.yml")); !os.IsNotExist(err) {
		t.Fatalf("default definitions target stat error = %v, want missing", err)
	}
}
