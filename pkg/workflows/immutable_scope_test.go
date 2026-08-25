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

func TestFrozenGitScopeCopiesAreIndependentOneShotCapabilities(t *testing.T) {
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "package service\nconst ready = true\n"
	writeTestFile(t, filepath.Join(repo, "service.go"), content)
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.email", "test@example.com")
	gitCmd(t, repo, "config", "user.name", "Test User")
	gitCmd(t, repo, "add", "service.go")
	gitCmd(t, repo, "commit", "-m", "frozen copies")
	blob := strings.TrimSpace(gitCmd(t, repo, "rev-parse", "HEAD:service.go"))
	exec := ExecutionContext{
		WorkspaceDir:     workspace,
		RunID:            "wr-frozen-copies",
		WorkflowRef:      RepositoryModelEvaluationBatchWorkflowRef,
		workspaceCleanup: &workflowWorkspaceCleanup{},
	}
	output, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "freeze", "copies": 2, "max_content_bytes": 1024,
		"files": []map[string]any{{
			"path": "service.go", "fileHash": blob, "sizeBytes": int64(len(content)),
			"category": "code", "source": map[string]any{"workspacePath": repo},
		}},
	}, exec)
	if err != nil {
		t.Fatal(err)
	}
	primary, _ := output["token"].(string)
	secondary, _ := output["secondaryToken"].(string)
	if primary == "" || secondary == "" || primary == secondary {
		t.Fatalf("frozen scope copy tokens = %q, %q", primary, secondary)
	}
	primaryScope, consumeErr := consumeNativeFrozenGitScope(exec, primary)
	if consumeErr != nil {
		t.Fatal(consumeErr)
	}
	primaryFiles := primaryScope.([]map[string]any)
	primaryFiles[0]["content"] = "mutated by candidate runner"
	primaryFiles[0]["path"] = "mutated.go"
	secondaryScope, consumeErr := consumeNativeFrozenGitScope(exec, secondary)
	if consumeErr != nil {
		t.Fatal(consumeErr)
	}
	secondaryFiles := secondaryScope.([]map[string]any)
	if len(secondaryFiles) != 1 || secondaryFiles[0]["content"] != content ||
		secondaryFiles[0]["path"] != "service.go" {
		t.Fatalf("secondary frozen scope was aliased to candidate mutation: %#v", secondaryScope)
	}
	for _, token := range []string{primary, secondary} {
		if _, reuseErr := consumeNativeFrozenGitScope(exec, token); reuseErr == nil {
			t.Fatalf("one-shot token %q was reusable", token)
		}
	}
	nativeFrozenGitScopes.Lock()
	_, primaryRemains := nativeFrozenGitScopes.entries[primary]
	_, secondaryRemains := nativeFrozenGitScopes.entries[secondary]
	nativeFrozenGitScopes.Unlock()
	if primaryRemains || secondaryRemains {
		t.Fatal("consumed frozen scope capability remained in runtime cache")
	}
}

func TestRepositoryFreezeSeparatesFileHydrationFromGroupByteLimit(t *testing.T) {
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("x", 128)
	for _, name := range []string{"first.go", "second.go"} {
		writeTestFile(t, filepath.Join(repo, name), content)
	}
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.email", "test@example.com")
	gitCmd(t, repo, "config", "user.name", "Test User")
	gitCmd(t, repo, "add", ".")
	gitCmd(t, repo, "commit", "-m", "separate freeze limits")

	files := make([]map[string]any, 0, 2)
	for _, name := range []string{"first.go", "second.go"} {
		files = append(files, map[string]any{
			"path": name, "fileHash": strings.TrimSpace(gitCmd(t, repo, "rev-parse", "HEAD:"+name)),
			"sizeBytes": int64(len(content)), "category": "code",
			"source": map[string]any{"workspacePath": repo},
		})
	}
	exec := ExecutionContext{
		WorkspaceDir: workspace, RunID: "wr-separate-freeze-limits",
		WorkflowRef: RepositoryModelEvaluationBatchWorkflowRef,
	}
	output, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "freeze", "files": files,
		"max_file_content_bytes": 1024, "max_group_files": 2,
		"max_group_content_bytes": 64, "max_total_content_bytes": 4096,
	}, exec)
	if err != nil {
		t.Fatal(err)
	}
	if output["reviewableCount"] != 2 || output["unavailableCount"] != 0 {
		t.Fatalf("freeze counts = %#v", output)
	}
	frozen, err := consumeNativeFrozenGitScope(exec, nativeAnyString(output["token"]))
	if err != nil {
		t.Fatal(err)
	}
	hydrated := frozen.([]map[string]any)
	if len(hydrated) != 2 || hydrated[0]["content"] != content || hydrated[1]["content"] != content ||
		hydrated[0]["reviewGroup"] == hydrated[1]["reviewGroup"] {
		t.Fatalf("separately bounded frozen scope = %#v", hydrated)
	}
}

func TestStoredFrozenGitScopeSnapshotsNestedCallerData(t *testing.T) {
	exec := ExecutionContext{
		RunID:       "wr-frozen-snapshot",
		WorkflowRef: RepositoryModelEvaluationBatchWorkflowRef,
	}
	scope := []map[string]any{{
		"path": "service.go",
		"metadata": map[string]any{
			"regions": []map[string]any{{"name": "runtime"}},
		},
	}}
	token, err := storeNativeFrozenGitScope(exec, scope)
	if err != nil {
		t.Fatal(err)
	}
	scope[0]["path"] = "mutated.go"
	regions := scope[0]["metadata"].(map[string]any)["regions"].([]map[string]any)
	regions[0]["name"] = "mutated"

	stored, err := consumeNativeFrozenGitScope(exec, token)
	if err != nil {
		t.Fatal(err)
	}
	files := stored.([]map[string]any)
	storedRegions := files[0]["metadata"].(map[string]any)["regions"].([]map[string]any)
	if files[0]["path"] != "service.go" || storedRegions[0]["name"] != "runtime" {
		t.Fatalf("stored frozen scope followed caller mutation: %#v", stored)
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
				"severity": "high", "title": "Invented binary bug", "symbol": "fixture", "file": "tests/fixture.bin",
				"message": "Invented behavior.", "evidence": "not actually visible", "impact": "unknown",
				"validation": map[string]any{"status": "confirmed", "summary": "claimed", "checks": []any{}},
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
	}, 3, 1024)
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

func TestFrozenReviewScopeHonorsDynamicGroupFileLimit(t *testing.T) {
	files := make([]map[string]any, 0, 5)
	for _, name := range []string{"a.go", "b.go", "c.go", "d.go", "e.go"} {
		files = append(files, map[string]any{
			"path": name, "content": "package sample", "contentComplete": true,
		})
	}
	grouped, _, _, err := nativeReviewableFrozenGitScope(files, 2, 4096)
	if err != nil {
		t.Fatal(err)
	}
	groupSizes := make(map[string]int)
	for _, file := range grouped {
		groupSizes[nativeAnyString(file["reviewGroup"])]++
	}
	if len(groupSizes) != 3 {
		t.Fatalf("group count = %d, want 3: %#v", len(groupSizes), grouped)
	}
	for group, size := range groupSizes {
		if size > 2 {
			t.Fatalf("group %q size = %d, want at most 2", group, size)
		}
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
