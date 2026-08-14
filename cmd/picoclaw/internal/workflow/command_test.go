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
	for _, want := range []string{"install", "compatibility", "revalidate"} {
		if !slices.Contains(names, want) {
			t.Fatalf("workflow subcommands = %v, missing %q", names, want)
		}
	}
}

func TestInstallWorkflowCommandInstallsCodeReviewWorkflow(t *testing.T) {
	testInstallWorkflowCommand(t, "code-review", "workflows/code-review.yml")
}

func TestInstallWorkflowCommandInstallsGitHubIssueTriageWorkflow(t *testing.T) {
	testInstallWorkflowCommand(
		t,
		"github-issue-triage",
		"workflows/github-issue-triage.yml",
	)
}

func testInstallWorkflowCommand(t *testing.T, template string, ref string) {
	t.Helper()
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	t.Setenv(config.EnvConfig, configPath)

	cmd := NewWorkflowCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"install", template})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("workflow install command failed: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), `"ref": "`+ref+`"`) {
		t.Fatalf("install output = %s, want ref %q", out.String(), ref)
	}
	if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(ref))); err != nil {
		t.Fatalf("installed workflow %q stat error = %v", ref, err)
	}

	validate := NewWorkflowCommand()
	var validateOut bytes.Buffer
	validate.SetOut(&validateOut)
	validate.SetArgs([]string{"validate", ref})
	if err := validate.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("workflow validate command failed: %v\n%s", err, validateOut.String())
	}
	if !strings.Contains(validateOut.String(), `"valid": true`) {
		t.Fatalf("validate output = %s, want valid", validateOut.String())
	}
}

func TestInstallWorkflowCommandHelpListsAvailableTemplates(t *testing.T) {
	cmd := NewWorkflowCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"install", "--help"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("workflow install --help failed: %v\n%s", err, out.String())
	}
	for _, template := range []string{
		"code-review",
		"github-issue-triage",
	} {
		if !strings.Contains(out.String(), template) {
			t.Fatalf("install help = %q, missing template %q", out.String(), template)
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
