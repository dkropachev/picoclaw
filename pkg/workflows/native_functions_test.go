package workflows

import (
	"context"
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutorRunsNativeWorkflowStateAndArtifactFunctions(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Native State
on:
  workflow_call:
    outputs:
      stored:
        value: ${{ jobs.main.outputs.stored }}
      artifact:
        value: ${{ jobs.main.outputs.artifact }}
jobs:
  main:
    runs-on: picoclaw
    outputs:
      stored: ${{ steps.get.outputs.value.answer }}
      artifact: ${{ steps.write.outputs.relativePath }}
    steps:
      - id: set
        uses: function/workflow.state
        with:
          action: set
          key: review_state
          value:
            answer: ok
      - id: get
        uses: function/workflow.state
        with:
          action: get
          key: review_state
      - id: write
        uses: function/workflow.artifact
        with:
          action: write
          name: reports/result.json
          value:
            status: ${{ steps.get.outputs.value.answer }}
`)
	workspace := t.TempDir()
	result, err := (&Executor{WorkspaceDir: workspace}).Run(context.Background(), RunRequest{
		Workflow:    workflow,
		WorkflowRef: "workflows/native.yml",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := result.Outputs["stored"]; got != "ok" {
		t.Fatalf("stored output = %#v, want ok", got)
	}
	artifact, ok := result.Outputs["artifact"].(string)
	if !ok || !strings.Contains(artifact, "workflow_artifacts/") {
		t.Fatalf("artifact output = %#v, want workflow_artifacts path", result.Outputs["artifact"])
	}
	if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(artifact))); err != nil {
		t.Fatalf("artifact stat error = %v", err)
	}
}

func TestExecutorComposesNativeGitInventoryWithStateAndArtifact(t *testing.T) {
	requireGit(t)
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(
		t,
		filepath.Join(repo, "src", "auth.js"),
		"export function canAdmin(user) { return user.role === 'admin' }\n",
	)
	writeTestFile(t, filepath.Join(repo, "tests", "auth.test.js"), "import { canAdmin } from '../src/auth.js'\n")
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.email", "test@example.com")
	gitCmd(t, repo, "config", "user.name", "Test User")
	gitCmd(t, repo, "add", ".")
	gitCmd(t, repo, "commit", "-m", "initial")

	workflow := parseWorkflow(t, `
name: Native Git Inventory
on:
  workflow_call:
    outputs:
      selected:
        value: ${{ jobs.inventory.outputs.selected }}
      artifact:
        value: ${{ jobs.inventory.outputs.artifact }}
      stateHash:
        value: ${{ jobs.inventory.outputs.stateHash }}
jobs:
  inventory:
    runs-on: picoclaw
    outputs:
      selected: ${{ steps.inventory.outputs.counts.totalSelectedFiles }}
      artifact: ${{ steps.store.outputs.relativePath }}
      stateHash: ${{ steps.get.outputs.value.inventoryHash }}
    steps:
      - id: inventory
        uses: function/git.inventory
        with:
          working_directory: repo
          target: all
      - id: save
        uses: function/workflow.state
        with:
          action: set
          key: git_inventory
          value: ${{ steps.inventory.outputs }}
      - id: get
        uses: function/workflow.state
        with:
          action: get
          key: git_inventory
      - id: store
        uses: function/workflow.artifact
        with:
          action: write
          name: inventories/result.json
          value: ${{ steps.inventory.outputs }}
`)
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	result, err := executor.Run(context.Background(), RunRequest{
		Workflow:    workflow,
		WorkflowRef: "workflows/native-git-inventory.yml",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := result.Outputs["selected"]; got != 2 {
		t.Fatalf("selected = %#v, want 2", got)
	}
	if got, ok := result.Outputs["stateHash"].(string); !ok || got == "" {
		t.Fatalf("stateHash = %#v, want non-empty string", result.Outputs["stateHash"])
	}
	artifactPath, ok := result.Outputs["artifact"].(string)
	if !ok || artifactPath == "" {
		t.Fatalf("artifact = %#v, want artifact path", result.Outputs["artifact"])
	}
	artifactData, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(artifactPath)))
	if err != nil {
		t.Fatalf("read inventory artifact: %v", err)
	}
	var artifact struct {
		SelectedFiles []map[string]any `json:"selectedFiles"`
		Counts        struct {
			TotalSelectedFiles float64 `json:"totalSelectedFiles"`
		} `json:"counts"`
	}
	if err := json.Unmarshal(artifactData, &artifact); err != nil {
		t.Fatalf("unmarshal inventory artifact: %v\n%s", err, artifactData)
	}
	if len(artifact.SelectedFiles) != 2 || artifact.Counts.TotalSelectedFiles != 2 {
		t.Fatalf("inventory artifact = %#v, want two selected files", artifact)
	}
}

func TestNativeGitInventoryDefaultsToHead(t *testing.T) {
	requireGit(t)
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.email", "test@example.com")
	gitCmd(t, repo, "config", "user.name", "Test User")
	writeTestFile(t, filepath.Join(repo, "base.go"), "package main\n")
	gitCmd(t, repo, "add", ".")
	gitCmd(t, repo, "commit", "-m", "base")
	gitCmd(t, repo, "branch", "-M", "main")
	gitCmd(t, repo, "checkout", "-b", "feature")
	writeTestFile(t, filepath.Join(repo, "feature.go"), "package main\n")
	gitCmd(t, repo, "add", ".")
	gitCmd(t, repo, "commit", "-m", "feature")

	outputs, handled, err := RunNativeFunction(
		context.Background(),
		"git.inventory",
		map[string]any{"working_directory": "repo", "target": "all"},
		ExecutionContext{WorkspaceDir: workspace},
	)
	if err != nil {
		t.Fatalf("RunNativeFunction() error = %v", err)
	}
	if !handled {
		t.Fatal("git.inventory was not handled")
	}
	selected, ok := outputs["selectedFiles"].([]map[string]any)
	if !ok {
		t.Fatalf("selectedFiles = %#v, want []map[string]any", outputs["selectedFiles"])
	}
	var sawFeature bool
	for _, file := range selected {
		if file["path"] == "feature.go" {
			sawFeature = true
		}
	}
	if !sawFeature {
		t.Fatalf("selected files = %#v, want feature.go from HEAD", selected)
	}
}

func TestNativeGitInventoryCanIncludeSelectedFileContent(t *testing.T) {
	requireGit(t)
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.email", "test@example.com")
	gitCmd(t, repo, "config", "user.name", "Test User")
	writeTestFile(t, filepath.Join(repo, "main.go"), "package main\n\nfunc main() {}\n")
	writeTestFile(t, filepath.Join(repo, "README.md"), "docs\n")
	gitCmd(t, repo, "add", ".")
	gitCmd(t, repo, "commit", "-m", "initial")

	outputs, handled, err := RunNativeFunction(
		context.Background(),
		"git.inventory",
		map[string]any{
			"working_directory": "repo",
			"target":            "code",
			"include_content":   true,
			"max_content_bytes": 12,
		},
		ExecutionContext{WorkspaceDir: workspace},
	)
	if err != nil {
		t.Fatalf("RunNativeFunction() error = %v", err)
	}
	if !handled {
		t.Fatal("git.inventory was not handled")
	}
	selected, ok := outputs["selectedFiles"].([]map[string]any)
	if !ok || len(selected) != 1 {
		t.Fatalf("selectedFiles = %#v, want one selected code file", outputs["selectedFiles"])
	}
	if selected[0]["path"] != "main.go" {
		t.Fatalf("selected path = %#v, want main.go", selected[0]["path"])
	}
	if selected[0]["content"] != "package main" {
		t.Fatalf("content = %#v, want truncated file content", selected[0]["content"])
	}
	if selected[0]["contentTruncated"] != true {
		t.Fatalf("contentTruncated = %#v, want true", selected[0]["contentTruncated"])
	}
	if _, exists := selected[0]["contentEncoding"]; !exists {
		t.Fatalf("contentEncoding missing from %#v", selected[0])
	}
}

func TestNativeGitInventoryRejectsWorkspaceEscape(t *testing.T) {
	requireGit(t)
	workspace := t.TempDir()
	other := t.TempDir()
	gitCmd(t, other, "init")
	workflow := parseWorkflow(t, `
name: Escape
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: function/git.inventory
        with:
          working_directory: ../outside
`)
	_, err := (&Executor{WorkspaceDir: workspace}).Run(context.Background(), RunRequest{
		Workflow:    workflow,
		WorkflowRef: "workflows/escape.yml",
	})
	if err == nil || !strings.Contains(err.Error(), "working_directory must stay inside") {
		t.Fatalf("Run() error = %v, want workspace escape rejection", err)
	}
}

func TestNativeWorkflowStateRejectsSymlinkedNamespaceEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	namespace := "escape"
	stateRoot := filepath.Join(workspace, workflowStateDir)
	if err := os.MkdirAll(stateRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(stateRoot, safeStorageSegment(namespace))); err != nil {
		t.Fatal(err)
	}

	_, _, err := RunNativeFunction(
		context.Background(),
		"workflow.state",
		map[string]any{
			"action":    "set",
			"namespace": namespace,
			"key":       "secret",
			"value":     "outside",
		},
		ExecutionContext{WorkspaceDir: workspace},
	)
	if err == nil || !strings.Contains(err.Error(), "inside workflow workspace") {
		t.Fatalf("RunNativeFunction() error = %v, want symlink escape rejection", err)
	}
	if entries, err := os.ReadDir(outside); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("outside entries = %#v, want none", entries)
	}
}

func TestNativeWorkflowStateRejectsSymlinkedNamespaceInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "unrelated")
	namespace := "escape"
	stateRoot := filepath.Join(workspace, workflowStateDir)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(stateRoot, safeStorageSegment(namespace))); err != nil {
		t.Fatal(err)
	}

	_, _, err := RunNativeFunction(
		context.Background(),
		"workflow.state",
		map[string]any{
			"action":    "set",
			"namespace": namespace,
			"key":       "secret",
			"value":     "inside-workspace",
		},
		ExecutionContext{WorkspaceDir: workspace},
	)
	if err == nil || !strings.Contains(err.Error(), "storage root") {
		t.Fatalf("RunNativeFunction() error = %v, want storage root symlink rejection", err)
	}
	if entries, err := os.ReadDir(target); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("target entries = %#v, want none", entries)
	}
}

func TestNativeWorkflowStateSetDoesNotFollowTempSymlink(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	namespace := "safe"
	key := "plan"
	stateDir := filepath.Join(workspace, workflowStateDir, safeStorageSegment(namespace))
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(stateDir, safeStorageSegment(key)+".json.tmp")); err != nil {
		t.Fatal(err)
	}

	_, _, err := RunNativeFunction(
		context.Background(),
		"workflow.state",
		map[string]any{
			"action":    "set",
			"namespace": namespace,
			"key":       key,
			"value":     "inside",
		},
		ExecutionContext{WorkspaceDir: workspace},
	)
	if err != nil {
		t.Fatalf("RunNativeFunction() error = %v", err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("outside stat error = %v, want not exist", err)
	}
	if info, err := os.Lstat(filepath.Join(stateDir, safeStorageSegment(key)+".json")); err != nil {
		t.Fatalf("state file lstat error = %v", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("state file is symlink, want regular file")
	}
}

func TestNativeWorkflowStateListRejectsSymlinkedStateFile(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	namespace := "safe"
	stateDir := filepath.Join(workspace, workflowStateDir, safeStorageSegment(namespace))
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte(`{"key":"outside","value":"leaked"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(stateDir, "outside.json")); err != nil {
		t.Fatal(err)
	}

	_, _, err := RunNativeFunction(
		context.Background(),
		"workflow.state",
		map[string]any{
			"action":    "list",
			"namespace": namespace,
		},
		ExecutionContext{WorkspaceDir: workspace},
	)
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("RunNativeFunction() error = %v, want symlink rejection", err)
	}
}

func TestNativeWorkflowArtifactRejectsSymlinkedNamespaceEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	namespace := "escape"
	artifactRoot := filepath.Join(workspace, workflowArtifactsDir)
	if err := os.MkdirAll(artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(artifactRoot, safeStorageSegment(namespace))); err != nil {
		t.Fatal(err)
	}

	_, _, err := RunNativeFunction(
		context.Background(),
		"workflow.artifact",
		map[string]any{
			"action":    "write",
			"namespace": namespace,
			"run_id":    "run-1",
			"name":      "report.txt",
			"content":   "outside",
		},
		ExecutionContext{WorkspaceDir: workspace},
	)
	if err == nil || !strings.Contains(err.Error(), "inside workflow workspace") {
		t.Fatalf("RunNativeFunction() error = %v, want symlink escape rejection", err)
	}
	if entries, err := os.ReadDir(outside); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("outside entries = %#v, want none", entries)
	}
}

func TestNativeWorkflowArtifactRejectsSymlinkedNamespaceInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "unrelated-artifacts")
	namespace := "escape"
	artifactRoot := filepath.Join(workspace, workflowArtifactsDir)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(artifactRoot, safeStorageSegment(namespace))); err != nil {
		t.Fatal(err)
	}

	_, _, err := RunNativeFunction(
		context.Background(),
		"workflow.artifact",
		map[string]any{
			"action":    "write",
			"namespace": namespace,
			"run_id":    "run-1",
			"name":      "report.txt",
			"content":   "inside-workspace",
		},
		ExecutionContext{WorkspaceDir: workspace},
	)
	if err == nil || !strings.Contains(err.Error(), "storage root") {
		t.Fatalf("RunNativeFunction() error = %v, want storage root symlink rejection", err)
	}
	if entries, err := os.ReadDir(target); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("target entries = %#v, want none", entries)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if err := osexec.Command("git", "--version").Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}
}

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := osexec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
