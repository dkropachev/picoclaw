package reviews

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestGitHubProviderRequiresRunner(t *testing.T) {
	if provider, err := NewGitHubProvider(nil, ""); provider != nil || err == nil {
		t.Fatalf("NewGitHubProvider(nil) = (%#v, %v)", provider, err)
	}
}

func TestGitHubProviderReadsExactWorkspaceEvidence(t *testing.T) {
	const diff = "diff --git a/pkg/a.go b/pkg/a.go\n--- a/pkg/a.go\n+++ b/pkg/a.go\n@@ -1 +1 @@\n-old\n+new\n"
	var requests []workflows.ToolRequest
	runner := providerToolRunnerFunc(func(
		_ context.Context,
		request workflows.ToolRequest,
	) (map[string]any, error) {
		requests = append(requests, request)
		if request.Args["method"] == "get_diff" {
			return map[string]any{"text": diff}, nil
		}
		return map[string]any{"text": `{"number":42,"head":{"sha":"abc"}}`}, nil
	})
	provider, err := NewGitHubProvider(runner, "")
	if err != nil {
		t.Fatal(err)
	}

	pull, err := provider.ReadWorkspacePullJSON(t.Context(), "octo/repo", 42)
	if err != nil || string(pull) != `{"number":42,"head":{"sha":"abc"}}` {
		t.Fatalf("ReadWorkspacePullJSON() = %q, %v", pull, err)
	}
	rawDiff, err := provider.ReadWorkspaceDiff(t.Context(), "octo/repo", 42)
	if err != nil || string(rawDiff) != diff {
		t.Fatalf("ReadWorkspaceDiff() = %q, %v", rawDiff, err)
	}
	if len(requests) != 2 {
		t.Fatalf("tool calls = %d", len(requests))
	}
	assertProviderTool(t, requests[0], GitHubPullRequestReadTool, map[string]any{
		"method": "get", "owner": "octo", "repo": "repo",
		"pullNumber": int64(42),
	})
	assertProviderTool(t, requests[1], GitHubPullRequestReadTool, map[string]any{
		"method": "get_diff", "owner": "octo", "repo": "repo",
		"pullNumber": int64(42),
	})
}

func TestGitHubProviderRejectsInvalidEvidenceAndIdentity(t *testing.T) {
	provider, err := NewGitHubProvider(providerToolRunnerFunc(func(
		_ context.Context,
		request workflows.ToolRequest,
	) (map[string]any, error) {
		if request.Args["method"] == "get_diff" {
			return map[string]any{"text": "not a unified diff"}, nil
		}
		return map[string]any{"text": `{"broken":`}, nil
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range []string{"", "octo", "octo/repo/extra", "/repo"} {
		if _, readErr := provider.ReadWorkspacePullJSON(t.Context(), repository, 42); !errors.Is(readErr, ErrInvalidWorkspaceProviderRequest) {
			t.Fatalf("repository %q error = %v", repository, readErr)
		}
	}
	if _, err = provider.ReadWorkspacePullJSON(t.Context(), "octo/repo", 0); !errors.Is(err, ErrInvalidWorkspaceProviderRequest) {
		t.Fatalf("pull zero error = %v", err)
	}
	if _, err = provider.ReadWorkspacePullJSON(t.Context(), "octo/repo", 42); !errors.Is(err, ErrProviderIncompatible) {
		t.Fatalf("malformed pull error = %v", err)
	}
	if _, err = provider.ReadWorkspaceDiff(t.Context(), "octo/repo", 42); !errors.Is(err, ErrProviderIncompatible) {
		t.Fatalf("invalid diff error = %v", err)
	}
}

func TestGitHubProviderRoutesReconciliationIdentityAndIssueTools(t *testing.T) {
	var requests []workflows.ToolRequest
	provider, err := NewGitHubProvider(providerToolRunnerFunc(func(
		_ context.Context,
		request workflows.ToolRequest,
	) (map[string]any, error) {
		requests = append(requests, request)
		return map[string]any{"text": `{}`}, nil
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.ReadWorkspaceReviewsJSON(t.Context(), "octo/repo", 42, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.ReadWorkspaceViewerJSON(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.SearchWorkspaceRepositoriesJSON(t.Context(), "octo/repo", "org"); err != nil {
		t.Fatal(err)
	}
	issueArgs := map[string]any{
		"owner": "octo", "repo": "repo", "title": "Deferred finding",
		"body": "Details", "labels": []string{"picoclaw"},
	}
	if _, err = provider.CreateWorkspaceIssueJSON(t.Context(), issueArgs); err != nil {
		t.Fatal(err)
	}
	if _, exists := issueArgs["method"]; exists {
		t.Fatalf("CreateWorkspaceIssueJSON mutated caller args: %#v", issueArgs)
	}
	if _, err = provider.SearchWorkspaceIssuesJSON(t.Context(), map[string]any{"query": "marker"}); err != nil {
		t.Fatal(err)
	}
	wantTools := []string{
		GitHubPullRequestReadTool,
		GitHubGetMeTool,
		GitHubSearchRepositoriesTool,
		GitHubIssueWriteTool,
		GitHubSearchIssuesTool,
	}
	if len(requests) != len(wantTools) {
		t.Fatalf("tool calls = %d", len(requests))
	}
	for index, tool := range wantTools {
		if requests[index].MCPTool != tool || requests[index].MCPServer != DefaultGitHubMCPServer ||
			!requests[index].MCP {
			t.Fatalf("request %d = %#v", index, requests[index])
		}
	}
	if !reflect.DeepEqual(requests[0].Args, map[string]any{
		"method": "get_reviews", "owner": "octo", "repo": "repo",
		"pullNumber": int64(42), "page": 1, "perPage": 100,
	}) {
		t.Fatalf("review history args = %#v", requests[0].Args)
	}
	if !reflect.DeepEqual(requests[2].Args, map[string]any{
		"query": "repo in:name org:octo", "minimal_output": false,
		"page": 1, "perPage": 100,
	}) {
		t.Fatalf("repository search args = %#v", requests[2].Args)
	}
	if !reflect.DeepEqual(requests[3].Args, map[string]any{
		"method": "create", "owner": "octo", "repo": "repo",
		"title": "Deferred finding", "body": "Details",
		"labels": []any{"picoclaw"},
	}) {
		t.Fatalf("issue create args = %#v", requests[3].Args)
	}
	for _, page := range []int{0, MaxWorkspaceReviewHistoryPages + 1} {
		if _, pageErr := provider.ReadWorkspaceReviewsJSON(t.Context(), "octo/repo", 42, page); !errors.Is(pageErr, ErrInvalidWorkspaceProviderRequest) {
			t.Fatalf("page %d error = %v", page, pageErr)
		}
	}
}

func TestGitHubProviderRejectsUnsafeRepositorySearches(t *testing.T) {
	toolCalls := 0
	provider, err := NewGitHubProvider(providerToolRunnerFunc(func(
		_ context.Context,
		_ workflows.ToolRequest,
	) (map[string]any, error) {
		toolCalls++
		return map[string]any{"text": `{}`}, nil
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		repository string
		qualifier  string
	}{
		{repository: "", qualifier: "user"},
		{repository: "octo", qualifier: "user"},
		{repository: "octo/repo/extra", qualifier: "user"},
		{repository: "octo user:attacker/repo", qualifier: "user"},
		{repository: "octo/repo org:attacker", qualifier: "user"},
		{repository: "octo/repo", qualifier: ""},
		{repository: "octo/repo", qualifier: "team"},
		{repository: "octo/repo", qualifier: "User"},
	}
	for _, test := range tests {
		if _, searchErr := provider.SearchWorkspaceRepositoriesJSON(
			t.Context(), test.repository, test.qualifier,
		); !errors.Is(searchErr, ErrInvalidWorkspaceProviderRequest) {
			t.Errorf("SearchWorkspaceRepositoriesJSON(%q, %q) error = %v", test.repository, test.qualifier, searchErr)
		}
	}
	if toolCalls != 0 {
		t.Fatalf("invalid searches made %d tool calls", toolCalls)
	}
}

func TestGitHubProviderValidatesAndCopiesIssueCreateArgs(t *testing.T) {
	toolCalls := 0
	var issuedLabels any
	provider, err := NewGitHubProvider(providerToolRunnerFunc(func(
		_ context.Context,
		request workflows.ToolRequest,
	) (map[string]any, error) {
		toolCalls++
		issuedLabels = request.Args["labels"]
		request.Args["title"] = "runner mutation"
		return map[string]any{"text": `{"number":1}`}, nil
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	valid := map[string]any{
		"owner": "octo", "repo": "repo", "title": "Original title",
		"body": "", "labels": []string{},
	}
	if _, err = provider.CreateWorkspaceIssueJSON(t.Context(), valid); err != nil {
		t.Fatal(err)
	}
	if valid["title"] != "Original title" {
		t.Fatalf("runner mutation escaped copied args: %#v", valid)
	}
	if _, exists := valid["method"]; exists {
		t.Fatalf("method injected into caller args: %#v", valid)
	}
	if !reflect.DeepEqual(issuedLabels, []any{}) {
		t.Fatalf("empty labels = %#v (%T), want []any{}", issuedLabels, issuedLabels)
	}

	invalid := []map[string]any{
		nil,
		{"repo": "repo", "title": "title"},
		{"owner": "octo", "title": "title"},
		{"owner": "octo", "repo": "repo"},
		{"owner": " ", "repo": "repo", "title": "title"},
		{"owner": "octo", "repo": " ", "title": "title"},
		{"owner": "octo", "repo": "repo", "title": " \t"},
		{"owner": "octo/other", "repo": "repo", "title": "title"},
		{"owner": "octo", "repo": "repo/other", "title": "title"},
		{"owner": "octo", "repo": "repo", "title": "title", "method": "create"},
		{"owner": "octo", "repo": "repo", "title": "title", "assignees": []string{"hubot"}},
	}
	for index, args := range invalid {
		if _, createErr := provider.CreateWorkspaceIssueJSON(t.Context(), args); !errors.Is(createErr, ErrInvalidWorkspaceProviderRequest) {
			t.Errorf("invalid issue args %d error = %v", index, createErr)
		} else if !errors.Is(createErr, ErrWorkspaceProviderCallNotDispatched) {
			t.Errorf("invalid issue args %d were not marked pre-dispatch: %v", index, createErr)
		}
	}
	if toolCalls != 1 {
		t.Fatalf("issue create tool calls = %d, want 1", toolCalls)
	}
}

func TestGitHubProviderPreservesContextCancellation(t *testing.T) {
	provider, err := NewGitHubProvider(providerToolRunnerFunc(func(
		ctx context.Context,
		_ workflows.ToolRequest,
	) (map[string]any, error) {
		return nil, ctx.Err()
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = provider.ReadWorkspacePullJSON(ctx, "octo/repo", 42); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read error = %v", err)
	}
}

func TestGitHubProviderExactArtifactLifecycle(t *testing.T) {
	requireProviderArtifactLifecycle(t)
	t.Run("JSON success", func(t *testing.T) {
		root := privateProviderArtifactRoot(t)
		path := filepath.Join(root, "provider.json")
		writeProviderArtifact(t, path, `{"ok":true}`)
		provider := &GitHubProvider{ArtifactRoot: root}
		raw, err := provider.exactArtifactJSON(providerArtifactTag(path), 1024)
		if err != nil || string(raw) != `{"ok":true}` {
			t.Fatalf("exactArtifactJSON() = %q, %v", raw, err)
		}
		assertProviderArtifactRemoved(t, path)
	})
	t.Run("diff success", func(t *testing.T) {
		root := privateProviderArtifactRoot(t)
		path := filepath.Join(root, "provider.diff")
		writeProviderArtifact(t, path, "diff --git a/a b/a\n")
		provider := &GitHubProvider{ArtifactRoot: root}
		raw, err := provider.exactArtifactText(providerArtifactTag(path), 1024)
		if err != nil || string(raw) != "diff --git a/a b/a\n" {
			t.Fatalf("exactArtifactText() = %q, %v", raw, err)
		}
		assertProviderArtifactRemoved(t, path)
	})
	t.Run("oversize", func(t *testing.T) {
		root := privateProviderArtifactRoot(t)
		path := filepath.Join(root, "oversize.json")
		writeProviderArtifact(t, path, `{"value":"`+strings.Repeat("x", 256)+`"}`)
		provider := &GitHubProvider{ArtifactRoot: root}
		_, err := provider.exactArtifactJSON(providerArtifactTag(path), 32)
		if !errors.Is(err, errProviderResultLimit) {
			t.Fatalf("oversize error = %v", err)
		}
		assertProviderArtifactRemoved(t, path)
	})
	t.Run("invalid JSON", func(t *testing.T) {
		root := privateProviderArtifactRoot(t)
		path := filepath.Join(root, "invalid.json")
		writeProviderArtifact(t, path, `{"broken":`)
		provider := &GitHubProvider{ArtifactRoot: root}
		_, err := provider.exactArtifactJSON(providerArtifactTag(path), 1024)
		if !errors.Is(err, ErrProviderIncompatible) {
			t.Fatalf("invalid JSON error = %v", err)
		}
		assertProviderArtifactRemoved(t, path)
	})
}

func TestGitHubProviderArtifactNeverDeletesUnownedOrChangedTargets(t *testing.T) {
	requireProviderArtifactLifecycle(t)
	t.Run("outside root", func(t *testing.T) {
		root := privateProviderArtifactRoot(t)
		outside := filepath.Join(t.TempDir(), "outside.json")
		writeProviderArtifact(t, outside, `{"outside":true}`)
		provider := &GitHubProvider{ArtifactRoot: root}
		if _, err := provider.exactArtifactJSON(providerArtifactTag(outside), 1024); err == nil {
			t.Fatal("outside artifact accepted")
		}
		assertProviderArtifactContents(t, outside, `{"outside":true}`)
	})
	t.Run("symlink", func(t *testing.T) {
		root := privateProviderArtifactRoot(t)
		target := filepath.Join(t.TempDir(), "target.json")
		writeProviderArtifact(t, target, `{"target":true}`)
		link := filepath.Join(root, "link.json")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("create symlink: %v", err)
		}
		provider := &GitHubProvider{ArtifactRoot: root}
		if _, err := provider.exactArtifactJSON(providerArtifactTag(link), 1024); err == nil {
			t.Fatal("symlink artifact accepted")
		}
		assertProviderArtifactContents(t, target, `{"target":true}`)
	})
	t.Run("path replacement race", func(t *testing.T) {
		root := privateProviderArtifactRoot(t)
		path := filepath.Join(root, "provider.json")
		moved := filepath.Join(root, "original.json")
		writeProviderArtifact(t, path, `{"original":true}`)
		provider := &GitHubProvider{ArtifactRoot: root}
		provider.artifactCleanupHook = func(cleanupPath string) {
			provider.artifactCleanupHook = nil
			if err := os.Rename(cleanupPath, moved); err != nil {
				t.Fatalf("move validated artifact: %v", err)
			}
			writeProviderArtifact(t, cleanupPath, `{"replacement":true}`)
		}
		_, err := provider.exactArtifactJSON(providerArtifactTag(path), 1024)
		if err == nil || !strings.Contains(err.Error(), "changed before cleanup") {
			t.Fatalf("race error = %v", err)
		}
		assertProviderArtifactContents(t, path, `{"replacement":true}`)
		assertProviderArtifactContents(t, moved, `{"original":true}`)
	})
}

func assertProviderTool(
	t *testing.T,
	request workflows.ToolRequest,
	tool string,
	args map[string]any,
) {
	t.Helper()
	if request.Name != "mcp_github_"+tool || request.MCPServer != "github" ||
		request.MCPTool != tool || !request.MCP || !reflect.DeepEqual(request.Args, args) {
		t.Fatalf("tool request = %#v", request)
	}
}

func providerArtifactTag(path string) []string {
	return []string{"[file:" + path + "]"}
}

func privateProviderArtifactRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create private provider artifact root: %v", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("secure private provider artifact root: %v", err)
	}
	return root
}

func requireProviderArtifactLifecycle(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || runtime.GOOS == "js" || runtime.GOOS == "plan9" {
		t.Skip("safe provider artifact consumption is unavailable on this platform")
	}
}

func writeProviderArtifact(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write provider artifact: %v", err)
	}
}

func assertProviderArtifactRemoved(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed provider artifact still exists or stat failed: %v", err)
	}
}

func assertProviderArtifactContents(t *testing.T, path string, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read provider artifact: %v", err)
	}
	if string(raw) != want {
		t.Fatalf("provider artifact = %q, want %q", raw, want)
	}
}

type providerToolRunnerFunc func(
	context.Context,
	workflows.ToolRequest,
) (map[string]any, error)

func (run providerToolRunnerFunc) RunTool(
	ctx context.Context,
	request workflows.ToolRequest,
) (map[string]any, error) {
	return run(ctx, request)
}
