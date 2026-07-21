package workflow

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestNewWorkflowCommandIncludesCompatibilityCommands(t *testing.T) {
	cmd := NewWorkflowCommand()
	names := make([]string, 0, len(cmd.Commands()))
	for _, subcmd := range cmd.Commands() {
		names = append(names, subcmd.Name())
	}
	for _, want := range []string{"compatibility", "revalidate"} {
		if !slices.Contains(names, want) {
			t.Fatalf("workflow subcommands = %v, missing %q", names, want)
		}
	}
}

func TestRunWorkflowCommandRunsNativeWorkflowWithoutProviderCredentials(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	workflowDir := filepath.Join(workspace, "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "native.yml"), []byte(`
name: Native CLI
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: function/workflow.state
        with:
          action: set
          key: cli_native
          value: ok
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	t.Setenv(config.EnvConfig, configPath)
	if _, err := workflows.RevalidateLocal(ctx, workspace, workflowRuntimeCompatibility()); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}

	cmd := NewWorkflowCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"run", "workflows/native.yml"})
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("workflow run command failed: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), `"status": "succeeded"`) {
		t.Fatalf("workflow run output = %s, want succeeded", out.String())
	}
}
