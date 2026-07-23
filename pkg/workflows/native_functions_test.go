package workflows

import (
	"context"
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
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

func TestNativeGitInventoryLinksSelectedFilesWithoutEmbeddingContent(t *testing.T) {
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
			"workspace": map[string]any{
				"id":   "gw-inventory",
				"path": repo,
			},
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
	if _, exists := selected[0]["content"]; exists {
		t.Fatalf("content unexpectedly embedded in %#v", selected[0])
	}
	if _, exists := selected[0]["contentTruncated"]; exists {
		t.Fatalf("contentTruncated unexpectedly embedded in %#v", selected[0])
	}
	source, ok := selected[0]["source"].(map[string]any)
	if !ok {
		t.Fatalf("source = %#v, want workspace file source", selected[0]["source"])
	}
	if source["workspaceId"] != "gw-inventory" {
		t.Fatalf("source.workspaceId = %#v, want gw-inventory", source["workspaceId"])
	}
	if source["workspacePath"] != repo {
		t.Fatalf("source.workspacePath = %#v, want %q", source["workspacePath"], repo)
	}
	if source["filePath"] != "main.go" {
		t.Fatalf("source.filePath = %#v, want main.go", source["filePath"])
	}
	if source["path"] != filepath.Join(repo, "main.go") {
		t.Fatalf("source.path = %#v, want linked file path", source["path"])
	}
}

func TestNativeGitFilterAppliesGlobPolicyAndLinksWorkspaceFiles(t *testing.T) {
	requireGit(t)
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repo")
	for _, dir := range []string{
		filepath.Join(repo, "src", "main"),
		filepath.Join(repo, "src", "fixtures"),
		filepath.Join(repo, "src", "testdata"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.email", "test@example.com")
	gitCmd(t, repo, "config", "user.name", "Test User")
	writeTestFile(t, filepath.Join(repo, "src", "main", "App.java"), "class App {}\n")
	writeTestFile(t, filepath.Join(repo, "src", "fixtures", "Fixture.java"), "class Fixture {}\n")
	writeTestFile(t, filepath.Join(repo, "src", "testdata", "payload.json"), "{}\n")
	gitCmd(t, repo, "add", ".")
	gitCmd(t, repo, "commit", "-m", "initial")

	inventory, handled, err := RunNativeFunction(
		context.Background(),
		"git.inventory",
		map[string]any{
			"workspace": map[string]any{
				"id":   "gw-filter",
				"path": repo,
			},
			"target": "all",
		},
		ExecutionContext{WorkspaceDir: workspace},
	)
	if err != nil {
		t.Fatalf("git.inventory error = %v", err)
	}
	if !handled {
		t.Fatal("git.inventory was not handled")
	}
	filtered, handled, err := RunNativeFunction(
		context.Background(),
		"git.filter",
		map[string]any{
			"workspace": map[string]any{
				"id":   "gw-filter",
				"path": repo,
			},
			"files":  inventory["files"],
			"target": "code",
			"filter": map[string]any{
				"includeGlobs": []any{"src/**"},
				"excludeGlobs": []any{"**/fixtures/**", "**/testdata/**"},
				"rationale":    "Skip fixtures and test data.",
			},
			"include_content":   true,
			"max_content_bytes": 1024,
		},
		ExecutionContext{WorkspaceDir: workspace},
	)
	if err != nil {
		t.Fatalf("git.filter error = %v", err)
	}
	if !handled {
		t.Fatal("git.filter was not handled")
	}
	selected, ok := filtered["selectedFiles"].([]map[string]any)
	if !ok {
		t.Fatalf("selectedFiles = %#v, want []map[string]any", filtered["selectedFiles"])
	}
	if len(selected) != 1 {
		t.Fatalf("selectedFiles = %#v, want one production file", selected)
	}
	if selected[0]["path"] != "src/main/App.java" {
		t.Fatalf("selected path = %#v, want src/main/App.java", selected[0]["path"])
	}
	if _, exists := selected[0]["content"]; exists {
		t.Fatalf("selected content unexpectedly embedded in %#v", selected[0])
	}
	source, ok := selected[0]["source"].(map[string]any)
	if !ok {
		t.Fatalf("source = %#v, want workspace file source", selected[0]["source"])
	}
	if source["workspaceId"] != "gw-filter" {
		t.Fatalf("source.workspaceId = %#v, want gw-filter", source["workspaceId"])
	}
	if source["filePath"] != "src/main/App.java" {
		t.Fatalf("source.filePath = %#v, want src/main/App.java", source["filePath"])
	}
	sourcePath, ok := source["path"].(string)
	if !ok || sourcePath != filepath.Join(repo, "src", "main", "App.java") {
		t.Fatalf("source.path = %#v, want linked App.java path", source["path"])
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil || !strings.Contains(string(data), "class App") {
		t.Fatalf("linked source read = %q, %v; want App.java content", string(data), err)
	}
	counts, ok := filtered["counts"].(map[string]any)
	if !ok || counts["totalSelectedFiles"] != 1 {
		t.Fatalf("counts = %#v, want one selected file", filtered["counts"])
	}
}

func TestNativeGitWorkspaceFileSourceHelpers(t *testing.T) {
	empty := nativeGitWorkspaceRefFromMap(nil)
	if empty != (nativeGitWorkspaceRef{}) {
		t.Fatalf("nativeGitWorkspaceRefFromMap(nil) = %#v, want empty ref", empty)
	}
	if got := empty.Map(); len(got) != 0 {
		t.Fatalf("empty workspace Map() = %#v, want empty map", got)
	}

	ref := nativeGitWorkspaceRefFromMap(map[string]any{
		"id":         "gw-1",
		"repo_id":    "repo-1",
		"remote_url": "git@example.com:org/repo.git",
		"ref":        "main",
		"path":       "/tmp/repo",
	})
	mapped := ref.Map()
	for key, want := range map[string]any{
		"id":         "gw-1",
		"repo_id":    "repo-1",
		"remote_url": "git@example.com:org/repo.git",
		"ref":        "main",
		"path":       "/tmp/repo",
	} {
		if mapped[key] != want {
			t.Fatalf("workspace Map()[%q] = %#v, want %#v", key, mapped[key], want)
		}
	}

	source, err := nativeGitFileSource(ref, "./src/../main.go")
	if err != nil {
		t.Fatalf("nativeGitFileSource() error = %v", err)
	}
	if source["type"] != "workspace_file" || source["workspaceId"] != "gw-1" ||
		source["workspacePath"] != "/tmp/repo" || source["filePath"] != "main.go" ||
		source["path"] != filepath.Join("/tmp/repo", "main.go") {
		t.Fatalf("source = %#v, want normalized workspace file source", source)
	}

	for _, badPath := range []string{"", ".", "../secret.go", "/tmp/secret.go"} {
		if _, cleanErr := nativeCleanRepoFilePath(badPath); cleanErr == nil {
			t.Fatalf("nativeCleanRepoFilePath(%q) error = nil, want rejection", badPath)
		}
	}

	if _, outputErr := nativeGitInventoryOutputFiles(
		nativeGitWorkspaceRef{},
		[]nativeGitFile{{Path: "../escape.go", BlobHash: "abc"}},
		"all",
		false,
	); outputErr == nil {
		t.Fatal("nativeGitInventoryOutputFiles() error = nil, want escaped path rejection")
	}

	files, err := nativeGitInventoryOutputFiles(
		nativeGitWorkspaceRef{Path: "/tmp/repo"},
		[]nativeGitFile{{Path: "src/app.go", BlobHash: "abc", SizeBytes: 7}},
		"all",
		false,
	)
	if err != nil {
		t.Fatalf("nativeGitInventoryOutputFiles() error = %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %#v, want one file", files)
	}
	if _, exists := files[0]["mode"]; exists {
		t.Fatalf("mode unexpectedly present when includeModes=false: %#v", files[0])
	}
	fileSource, ok := files[0]["source"].(map[string]any)
	if !ok || fileSource["path"] != filepath.Join("/tmp/repo", "src", "app.go") {
		t.Fatalf("file source = %#v, want linked app.go source", files[0]["source"])
	}
}

func TestNativeResolveGitWorkspaceAcceptsStringAndRejectsMissingPath(t *testing.T) {
	requireGit(t)
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "init")

	resolved, ref, err := nativeResolveGitWorkspace(
		ExecutionContext{WorkspaceDir: workspace},
		map[string]any{"workspace": "repo"},
	)
	if err != nil {
		t.Fatalf("nativeResolveGitWorkspace(string) error = %v", err)
	}
	if resolved != repo || ref.Path != repo {
		t.Fatalf("resolved = %q, ref = %#v, want repo path %q", resolved, ref, repo)
	}

	_, _, err = nativeResolveGitWorkspace(
		ExecutionContext{WorkspaceDir: workspace},
		map[string]any{"workspace": map[string]any{"id": "gw-missing-path"}},
	)
	if err == nil || !strings.Contains(err.Error(), "workspace.path is required") {
		t.Fatalf("nativeResolveGitWorkspace(missing path) error = %v, want path requirement", err)
	}
}

func TestNativeGitFilterRejectsEscapedInventoryPath(t *testing.T) {
	requireGit(t)
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "init")

	_, _, err := RunNativeFunction(
		context.Background(),
		"git.filter",
		map[string]any{
			"workspace": map[string]any{"path": repo},
			"files": []map[string]any{
				{"path": "../escape.go", "category": "code", "fileHash": "abc"},
			},
			"target": "all",
			"filter": map[string]any{},
		},
		ExecutionContext{WorkspaceDir: workspace},
	)
	if err == nil || !strings.Contains(err.Error(), "must stay inside repository") {
		t.Fatalf("git.filter error = %v, want escaped path rejection", err)
	}
}

func TestNativeFunctionInputParsingHelpers(t *testing.T) {
	if got := nativeMapValue(map[string]string{"include": "src/**"}); got["include"] != "src/**" {
		t.Fatalf("nativeMapValue(map[string]string) = %#v", got)
	}
	if got := nativeMapValue(`{"exclude":"vendor/**"}`); got["exclude"] != "vendor/**" {
		t.Fatalf("nativeMapValue(JSON string) = %#v", got)
	}
	if got := nativeMapValue(`not-json`); got != nil {
		t.Fatalf("nativeMapValue(invalid JSON) = %#v, want nil", got)
	}

	direct, err := nativeMapSlice([]map[string]any{{"path": "direct.go"}})
	if err != nil || len(direct) != 1 || direct[0]["path"] != "direct.go" {
		t.Fatalf("nativeMapSlice([]map) = %#v, %v", direct, err)
	}
	fromAny, err := nativeMapSlice([]any{map[string]any{"path": "any.go"}})
	if err != nil || len(fromAny) != 1 || fromAny[0]["path"] != "any.go" {
		t.Fatalf("nativeMapSlice([]any) = %#v, %v", fromAny, err)
	}
	fromJSON, err := nativeMapSlice(`[{"path":"json.go"}]`)
	if err != nil || len(fromJSON) != 1 || fromJSON[0]["path"] != "json.go" {
		t.Fatalf("nativeMapSlice(JSON string) = %#v, %v", fromJSON, err)
	}
	for _, value := range []any{nil, []any{"bad"}, `not-json`, 123} {
		if _, err := nativeMapSlice(value); err == nil {
			t.Fatalf("nativeMapSlice(%#v) error = nil, want rejection", value)
		}
	}

	if got := nativeStringSlice([]string{" src/** ", "", "tests/**"}); !reflect.DeepEqual(
		got,
		[]string{"src/**", "tests/**"},
	) {
		t.Fatalf("nativeStringSlice([]string) = %#v", got)
	}
	if got := nativeStringSlice([]any{" src/** ", 42, ""}); !reflect.DeepEqual(
		got,
		[]string{"src/**", "42"},
	) {
		t.Fatalf("nativeStringSlice([]any) = %#v", got)
	}
	if got := nativeStringSlice(`["src/**"," tests/** "]`); !reflect.DeepEqual(
		got,
		[]string{"src/**", "tests/**"},
	) {
		t.Fatalf("nativeStringSlice(JSON string) = %#v", got)
	}
	if got := nativeStringSlice("src/**, tests/**"); !reflect.DeepEqual(got, []string{"src/**", "tests/**"}) {
		t.Fatalf("nativeStringSlice(comma string) = %#v", got)
	}
	if got := nativeStringSlice(""); got != nil {
		t.Fatalf("nativeStringSlice(empty) = %#v, want nil", got)
	}
	if got := nativeStringSlice(123); got != nil {
		t.Fatalf("nativeStringSlice(number) = %#v, want nil", got)
	}

	boolArgs := map[string]any{
		"enabled":  true,
		"yes":      "yes",
		"disabled": "off",
		"other":    1,
	}
	if !nativeBool(boolArgs, "enabled") || !nativeBool(boolArgs, "yes") {
		t.Fatalf("nativeBool true forms failed")
	}
	if nativeBool(boolArgs, "disabled") || nativeBool(boolArgs, "other") || nativeBool(nil, "enabled") {
		t.Fatalf("nativeBool false forms failed")
	}

	intArgs := map[string]any{
		"int":    7,
		"int64":  int64(8),
		"float":  9.9,
		"string": "10",
		"bad":    "nan",
	}
	for key, want := range map[string]int{"int": 7, "int64": 8, "float": 9, "string": 10} {
		if got := nativeInt(intArgs, key, -1); got != want {
			t.Fatalf("nativeInt(%q) = %d, want %d", key, got, want)
		}
	}
	if got := nativeInt(intArgs, "bad", 11); got != 11 {
		t.Fatalf("nativeInt(bad) = %d, want fallback", got)
	}
	if got := nativeInt(nil, "missing", 12); got != 12 {
		t.Fatalf("nativeInt(nil) = %d, want fallback", got)
	}
}

func TestNativeWorkflowStateDeleteAndArtifactReadList(t *testing.T) {
	workspace := t.TempDir()
	execCtx := ExecutionContext{
		WorkspaceDir: workspace,
		WorkflowRef:  "workflows/native.yml",
		RunID:        "run-native",
	}

	_, handled, err := RunNativeFunction(
		context.Background(),
		"workflow.state",
		map[string]any{
			"action": "set",
			"key":    "scratch",
			"value":  map[string]any{"status": "stored"},
		},
		execCtx,
	)
	if err != nil || !handled {
		t.Fatalf("workflow.state set handled=%v error=%v", handled, err)
	}
	deleted, handled, err := RunNativeFunction(
		context.Background(),
		"workflow.state",
		map[string]any{
			"action": "delete",
			"key":    "scratch",
		},
		execCtx,
	)
	if err != nil || !handled {
		t.Fatalf("workflow.state delete handled=%v error=%v", handled, err)
	}
	if deleted["deleted"] != true {
		t.Fatalf("deleted = %#v, want true", deleted["deleted"])
	}
	deletedAgain, _, err := RunNativeFunction(
		context.Background(),
		"workflow.state",
		map[string]any{
			"action": "delete",
			"key":    "scratch",
		},
		execCtx,
	)
	if err != nil {
		t.Fatalf("workflow.state delete missing error = %v", err)
	}
	if deletedAgain["deleted"] != false {
		t.Fatalf("deleted again = %#v, want false", deletedAgain["deleted"])
	}

	defaultArtifact, handled, err := RunNativeFunction(
		context.Background(),
		"workflow.artifact",
		map[string]any{
			"action": "write",
			"format": "json",
			"value":  map[string]any{"kind": "default-name"},
		},
		execCtx,
	)
	if err != nil || !handled {
		t.Fatalf("workflow.artifact default write handled=%v error=%v", handled, err)
	}
	if name, _ := defaultArtifact["name"].(string); !strings.HasPrefix(name, "artifact-") ||
		!strings.HasSuffix(name, ".json") {
		t.Fatalf("default artifact name = %#v, want generated json artifact name", defaultArtifact["name"])
	}

	written, _, err := RunNativeFunction(
		context.Background(),
		"workflow.artifact",
		map[string]any{
			"action": "write",
			"name":   "reports/result.json",
			"value":  map[string]any{"status": "ok"},
		},
		execCtx,
	)
	if err != nil {
		t.Fatalf("workflow.artifact write error = %v", err)
	}
	read, _, err := RunNativeFunction(
		context.Background(),
		"workflow.artifact",
		map[string]any{
			"action": "read",
			"name":   "reports/result.json",
		},
		execCtx,
	)
	if err != nil {
		t.Fatalf("workflow.artifact read error = %v", err)
	}
	if read["relativePath"] != written["relativePath"] {
		t.Fatalf("read relativePath = %#v, want %#v", read["relativePath"], written["relativePath"])
	}
	value, ok := read["value"].(map[string]any)
	if !ok || value["status"] != "ok" {
		t.Fatalf("read value = %#v, want status ok", read["value"])
	}
	listed, _, err := RunNativeFunction(
		context.Background(),
		"workflow.artifact",
		map[string]any{"action": "list"},
		execCtx,
	)
	if err != nil {
		t.Fatalf("workflow.artifact list error = %v", err)
	}
	artifacts, ok := listed["artifacts"].([]map[string]any)
	if !ok || len(artifacts) != 2 {
		t.Fatalf("listed artifacts = %#v, want two artifacts", listed["artifacts"])
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
