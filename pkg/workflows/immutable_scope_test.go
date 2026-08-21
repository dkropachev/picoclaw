package workflows

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHydrateImmutableGitScopeReadsPinnedBlobNotMovingWorkingTree(t *testing.T) {
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(repo, "service.go")
	if err := os.WriteFile(filePath, []byte("package service\n\nconst value = \"old\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.email", "test@example.com")
	gitCmd(t, repo, "config", "user.name", "Test User")
	gitCmd(t, repo, "add", "service.go")
	gitCmd(t, repo, "commit", "-m", "old")
	blob := strings.TrimSpace(gitCmd(t, repo, "rev-parse", "HEAD:service.go"))
	old, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	scope := []map[string]any{{
		"path": "service.go", "fileHash": blob, "sizeBytes": int64(len(old)),
		"source": map[string]any{"workspacePath": repo, "filePath": "service.go"},
	}}
	if err := os.WriteFile(filePath, []byte("package service\n\nconst value = \"new\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hydrated, err := hydrateImmutableGitScope(
		context.Background(), scope, map[string]any{"max_content_bytes": 1024},
		ExecutionContext{WorkspaceDir: workspace},
	)
	if err != nil {
		t.Fatal(err)
	}
	files, ok := hydrated.([]map[string]any)
	if !ok || len(files) != 1 {
		t.Fatalf("hydrated scope=%#v", hydrated)
	}
	if content := files[0]["content"]; content != string(old) || strings.Contains(content.(string), "new") {
		t.Fatalf("hydrated content=%q, want pinned old blob", content)
	}
	if _, leaked := scope[0]["content"]; leaked {
		t.Fatal("hydration mutated the durable workflow scope")
	}
	if _, leaked := files[0]["source"]; leaked {
		t.Fatalf("hydrated reviewer scope leaked workspace source: %#v", files[0]["source"])
	}
}

func TestHydrateImmutableGitScopeRejectsUntrustedBlobMetadata(t *testing.T) {
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := hydrateImmutableGitScope(
		context.Background(),
		[]map[string]any{{
			"path": "service.go", "fileHash": "not-a-git-object", "sizeBytes": int64(1),
			"source": map[string]any{"workspacePath": repo},
		}},
		map[string]any{},
		ExecutionContext{WorkspaceDir: workspace},
	)
	if err == nil || !strings.Contains(err.Error(), "invalid blob hash") {
		t.Fatalf("hydrate error=%v, want invalid blob hash", err)
	}
}

func TestReviewTextBudgetAccountsForPromptEscapingAndControls(t *testing.T) {
	content := strings.Repeat(`"\\`, 100)
	if encoded := nativeReviewEncodedContentBytes(content); encoded <= len(content) {
		t.Fatalf("encoded prompt bytes=%d raw=%d", encoded, len(content))
	}
	if !nativeReviewText("valid\n\ttext") {
		t.Fatal("ordinary source text was rejected")
	}
	if nativeReviewText("invalid\x01text") {
		t.Fatal("control-heavy source text was accepted")
	}
}

func TestUnavailableImmutableScopeCannotProducePersistedFinding(t *testing.T) {
	file := map[string]any{
		"path": "tests/fixture.bin", "fileHash": strings.Repeat("a", 40),
		"sizeBytes": int64(4), "contentComplete": false, "contentUnavailable": "binary",
	}
	observation, err := nativeRepositoryReviewObservation(
		map[string]any{
			"summary": "unavailable", "findings": []map[string]any{{
				"severity": "high", "title": "Invented binary bug", "file": "tests/fixture.bin",
				"evidence": "not actually visible", "impact": "unknown", "recommendation": "none",
				"validation": map[string]any{"status": "confirmed", "summary": "claimed"},
			}},
		},
		[]map[string]any{file},
		"review-a",
		"binary challenge",
		"response",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Findings) != 0 {
		t.Fatalf("unavailable content produced findings: %#v", observation.Findings)
	}
	if got := nativeCategorizePath("tests/fixtures/logo.png"); got != "binary" {
		t.Fatalf("binary test fixture category=%q, want binary", got)
	}
}

func TestOnlyIntrinsicUnavailableFilesBecomeTerminalUnsupported(t *testing.T) {
	file := func(pathValue, reason string) map[string]any {
		return map[string]any{
			"path": pathValue, "fileHash": strings.Repeat("a", 40), "sizeBytes": int64(4),
			"contentComplete": false, "contentUnavailable": reason,
		}
	}
	unsupported := nativeRepositoryReviewUnsupportedFiles([]map[string]any{{
		"scope": []map[string]any{
			file("binary.dat", "binary"),
			file("large.go", "file_too_large"),
			file("later.go", "aggregate_limit"),
		},
	}})
	if len(unsupported) != 2 || unsupported[0].Path != "binary.dat" || unsupported[1].Path != "large.go" {
		t.Fatalf("terminal unsupported=%#v", unsupported)
	}
}

func TestFrozenReviewScopeSkipsUnavailableFilesBeforeModelExecution(t *testing.T) {
	file := func(pathValue string, complete bool, reason string) map[string]any {
		return map[string]any{
			"path": pathValue, "fileHash": strings.Repeat("a", 40), "sizeBytes": int64(4),
			"category": "code", "mode": "100644", "contentComplete": complete,
			"contentUnavailable": reason,
		}
	}
	reviewable, unsupported, unavailable, err := nativeReviewableFrozenGitScope([]map[string]any{
		file("service.go", true, ""),
		file("logo.png", false, "binary"),
		file("large.go", false, "file_too_large"),
		file("later.go", false, "aggregate_limit"),
	}, 1024)
	if err != nil || len(reviewable) != 1 || len(unsupported) != 2 || unavailable != 3 ||
		reviewable[0]["path"] != "service.go" {
		t.Fatalf("reviewable=%#v unsupported=%#v unavailable=%d err=%v", reviewable, unsupported, unavailable, err)
	}
	terminal := nativeRepositoryReviewUnsupportedScopeFiles(unsupported)
	if len(terminal) != 2 || terminal[0].Path != "logo.png" || terminal[1].Path != "large.go" {
		t.Fatalf("terminal unsupported refs=%#v", terminal)
	}
}

func TestFrozenReviewScopeGroupsCrossFileReferences(t *testing.T) {
	file := func(pathValue, content string) map[string]any {
		return map[string]any{
			"path": pathValue, "content": content, "contentComplete": true,
			"fileHash": strings.Repeat("a", 40), "sizeBytes": int64(len(content)),
		}
	}
	grouped := nativeGroupFrozenReviewScope([]map[string]any{
		file("cmd/main.go", `import "example/pkg/service"`),
		file("docs/guide.txt", "unrelated"),
		file("pkg/service.go", "package service"),
		file("zzz/other.go", "package other"),
	}, 2, 1024)
	if grouped[0]["path"] != "cmd/main.go" || grouped[1]["path"] != "pkg/service.go" {
		t.Fatalf("cross-file grouping=%#v", grouped)
	}
}

func TestFrozenReviewScopeUsesDistinctIDsForVariableSizedGroups(t *testing.T) {
	files := []map[string]any{
		{"path": "a/one.go", "contentComplete": true, "contentBytes": 80},
		{"path": "b/two.go", "contentComplete": true, "contentBytes": 80},
		{"path": "c/three.go", "contentComplete": true, "contentBytes": 80},
	}
	grouped := nativeGroupFrozenReviewScope(files, 3, 100)
	if grouped[0]["reviewGroup"] == grouped[1]["reviewGroup"] ||
		grouped[1]["reviewGroup"] == grouped[2]["reviewGroup"] {
		t.Fatalf("variable-size review group IDs collided: %#v", grouped)
	}
}

func TestWorkflowExecutorReleasesGitWorkspaceAfterPreReviewFailure(t *testing.T) {
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "init")
	runner := &codeReviewTemplateToolRunner{repo: repo}
	workflow := parseWorkflow(t, `
name: Cleanup failed review checkout
on:
  manual: {}
jobs:
  review:
    runs-on: picoclaw
    steps:
      - id: checkout
        uses: tool/git_workspace
        with:
          action: acquire
          repository: repo
      - id: inventory
        uses: function/git.inventory
        with:
          workspace: ${{ steps.checkout.outputs.workspace }}
          commit: refs/heads/does-not-exist
`)
	_, err := (&Executor{WorkspaceDir: workspace, Tools: runner}).Run(
		context.Background(),
		RunRequest{Workflow: workflow, WorkflowRef: "workflows/cleanup.yml", Session: "cleanup-session"},
	)
	if err == nil {
		t.Fatal("workflow unexpectedly succeeded")
	}
	if got := runner.actions; !reflect.DeepEqual(got, []string{"acquire", "release"}) {
		t.Fatalf("workspace actions=%#v, want deferred release", got)
	}
}

func TestFrozenGitScopeIsOpaqueRunBoundAndSingleUse(t *testing.T) {
	exec := ExecutionContext{RunID: "wr_freeze", WorkflowRef: "workflows/review.yml"}
	scope := []map[string]any{{
		"path": "service.go", "fileHash": strings.Repeat("a", 40),
		"sizeBytes": int64(12), "content": "private source", "contentComplete": true,
	}}
	token, freezeErr := storeNativeFrozenGitScope(exec, scope)
	if freezeErr != nil || len(token) != 64 {
		t.Fatalf("freeze token=%q err=%v", token, freezeErr)
	}
	refs := nativeFrozenGitScopeReferences(scope)
	if len(refs) != 1 || refs[0]["content"] != nil {
		t.Fatalf("frozen public refs leaked content: %#v", refs)
	}
	if _, err := consumeNativeFrozenGitScope(
		ExecutionContext{RunID: "other", WorkflowRef: exec.WorkflowRef}, token,
	); err == nil {
		t.Fatal("cross-run frozen scope consumption succeeded")
	}
	consumed, err := consumeNativeFrozenGitScope(exec, token)
	if err != nil || consumed.([]map[string]any)[0]["content"] != "private source" {
		t.Fatalf("consumed scope=%#v err=%v", consumed, err)
	}
	if _, err := consumeNativeFrozenGitScope(exec, token); err == nil {
		t.Fatal("frozen scope token was reusable")
	}
}
