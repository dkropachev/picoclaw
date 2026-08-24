package reviews

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestGitHubProviderDevelopmentReadAndPullCreationTools(t *testing.T) {
	var requests []workflows.ToolRequest
	provider, err := NewGitHubProvider(providerToolRunnerFunc(func(
		_ context.Context,
		request workflows.ToolRequest,
	) (map[string]any, error) {
		requests = append(requests, request)
		return map[string]any{"text": `{"id":1}`}, nil
	}), "github-development")
	if err != nil {
		t.Fatal(err)
	}

	if _, err = provider.ReadWorkspaceIssueJSON(t.Context(), "octo/repo", 7); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.ReadWorkspaceIssueCommentsJSON(t.Context(), "octo/repo", 7); err != nil {
		t.Fatal(err)
	}
	pullArgs := map[string]any{
		"owner": "octo", "repo": "repo", "title": "Implement the feature",
		"body": "Bounded summary", "head": "picoclaw/feature", "base": "main",
		"draft": true, "maintainer_can_modify": true,
	}
	if _, err = provider.CreateWorkspacePullRequestJSON(t.Context(), pullArgs); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.ListWorkspacePullRequestsJSON(
		t.Context(), "octo/repo", "picoclaw/feature", "main",
	); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.ListWorkspaceCommitsJSON(t.Context(), "octo/repo", "candidate-sha"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 5 {
		t.Fatalf("tool requests = %#v", requests)
	}
	wantTools := []string{
		GitHubIssueReadTool,
		GitHubIssueReadTool,
		GitHubCreatePullRequestTool,
		GitHubListPullRequestsTool,
		GitHubListCommitsTool,
	}
	for index, want := range wantTools {
		if requests[index].MCPServer != DefaultGitHubMCPServer || requests[index].MCPTool != want ||
			!requests[index].MCP {
			t.Fatalf("request %d = %#v", index, requests[index])
		}
	}
	if !reflect.DeepEqual(requests[0].Args, map[string]any{
		"method": "get", "owner": "octo", "repo": "repo", "issue_number": int64(7),
	}) {
		t.Fatalf("issue args = %#v", requests[0].Args)
	}
	if !reflect.DeepEqual(requests[1].Args, map[string]any{
		"method": "get_comments", "owner": "octo", "repo": "repo",
		"issue_number": int64(7), "page": 1, "perPage": 100,
	}) {
		t.Fatalf("comment args = %#v", requests[1].Args)
	}
	if !reflect.DeepEqual(requests[3].Args, map[string]any{
		"owner": "octo", "repo": "repo", "head": "octo:picoclaw/feature", "base": "main",
		"state": "open", "page": 1, "perPage": 100,
	}) {
		t.Fatalf("pull-list args = %#v", requests[3].Args)
	}
	if !reflect.DeepEqual(requests[4].Args, map[string]any{
		"owner": "octo", "repo": "repo", "sha": "candidate-sha", "page": 1, "perPage": 1,
	}) {
		t.Fatalf("commit-list args = %#v", requests[4].Args)
	}
	if pullArgs["title"] != "Implement the feature" {
		t.Fatalf("caller pull args changed: %#v", pullArgs)
	}
}

func TestGitHubProviderDevelopmentToolsRejectUnsafeInputs(t *testing.T) {
	toolCalls := 0
	provider, err := NewGitHubProvider(providerToolRunnerFunc(func(
		context.Context,
		workflows.ToolRequest,
	) (map[string]any, error) {
		toolCalls++
		return map[string]any{"text": `{}`}, nil
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, read := range []func() error{
		func() error { _, err := provider.ReadWorkspaceIssueJSON(t.Context(), "bad", 7); return err },
		func() error { _, err := provider.ReadWorkspaceIssueJSON(t.Context(), "octo/repo", 0); return err },
		func() error { _, err := provider.ReadWorkspaceIssueCommentsJSON(t.Context(), "bad", 7); return err },
		func() error {
			_, err := provider.ListWorkspacePullRequestsJSON(t.Context(), "octo/repo", "", "main")
			return err
		},
		func() error {
			_, err := provider.ListWorkspacePullRequestsJSON(t.Context(), "octo/repo", "head\nunsafe", "main")
			return err
		},
		func() error { _, err := provider.ListWorkspaceCommitsJSON(t.Context(), "bad", "sha"); return err },
		func() error { _, err := provider.ListWorkspaceCommitsJSON(t.Context(), "octo/repo", ""); return err },
		func() error {
			_, err := provider.ListWorkspaceCommitsJSON(t.Context(), "octo/repo", "sha\nunsafe")
			return err
		},
	} {
		if callErr := read(); !errors.Is(callErr, ErrInvalidWorkspaceProviderRequest) {
			t.Fatalf("unsafe provider request error = %v", callErr)
		}
	}
	invalidPulls := []map[string]any{
		nil,
		{"owner": "octo", "repo": "repo", "title": "title", "head": "head", "base": "main", "draft": false},
		{
			"owner": "octo",
			"repo":  "repo",
			"title": "title",
			"head":  "head",
			"base":  "main",
			"draft": true,
			"extra": true,
		},
		{"owner": "octo", "repo": "repo", "title": "title\nunsafe", "head": "head", "base": "main", "draft": true},
		{"owner": "octo", "repo": "repo", "title": "title", "head": 7, "base": "main", "draft": true},
	}
	for _, args := range invalidPulls {
		if _, createErr := provider.CreateWorkspacePullRequestJSON(
			t.Context(), args,
		); !errors.Is(createErr, ErrInvalidWorkspaceProviderRequest) {
			t.Fatalf("unsafe pull args %#v error = %v", args, createErr)
		}
	}
	if toolCalls != 0 {
		t.Fatalf("unsafe requests made %d tool calls", toolCalls)
	}
}

func TestGitHubProviderDevelopmentToolsPropagateRunnerErrors(t *testing.T) {
	want := errors.New("provider unavailable")
	provider, err := NewGitHubProvider(providerToolRunnerFunc(func(
		context.Context,
		workflows.ToolRequest,
	) (map[string]any, error) {
		return nil, want
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []func() error{
		func() error { _, err := provider.ReadWorkspaceIssueJSON(t.Context(), "octo/repo", 7); return err },
		func() error {
			_, err := provider.ReadWorkspaceIssueCommentsJSON(t.Context(), "octo/repo", 7)
			return err
		},
		func() error {
			_, err := provider.CreateWorkspacePullRequestJSON(t.Context(), map[string]any{
				"owner": "octo", "repo": "repo", "title": "title", "head": "head", "base": "main", "draft": true,
			})
			return err
		},
		func() error {
			_, err := provider.ListWorkspacePullRequestsJSON(t.Context(), "octo/repo", "head", "main")
			return err
		},
		func() error { _, err := provider.ListWorkspaceCommitsJSON(t.Context(), "octo/repo", "sha"); return err },
	} {
		if callErr := call(); !errors.Is(callErr, want) {
			t.Fatalf("runner error = %v, want %v", callErr, want)
		}
	}
}
