package gateway

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	testPRWorkspaceRepository = "dkropachev/picoclaw-pr-workspace-e2e-20260814"
	testPRWorkspacePullJSON   = `{
  "number": 1,
  "title": "Add time-aware greetings",
  "body": "Exercise the unified PR workspace",
  "state": "open",
  "merged": false,
  "html_url": "https://github.com/dkropachev/picoclaw-pr-workspace-e2e-20260814/pull/1",
  "user": {"id": 40304587, "login": "dkropachev"},
  "base": {"ref": "main", "sha": "base-sha", "repo": {"full_name": "dkropachev/picoclaw-pr-workspace-e2e-20260814"}},
  "head": {"ref": "feature/time-aware-greetings", "sha": "head-sha", "repo": {"full_name": "dkropachev/picoclaw-pr-workspace-e2e-20260814"}},
  "updated_at": "2026-08-14T00:00:00Z"
}`
	testPRWorkspaceRepositoryJSON = `{
  "total_count": 1,
  "incomplete_results": false,
  "items": [{
    "id": 1333775490,
    "owner": {"login": "dkropachev", "id": 40304587},
    "name": "picoclaw-pr-workspace-e2e-20260814",
    "full_name": "dkropachev/picoclaw-pr-workspace-e2e-20260814",
    "html_url": "https://github.com/dkropachev/picoclaw-pr-workspace-e2e-20260814",
    "has_issues": true,
    "permissions": {"admin": true, "maintain": true, "push": true, "triage": true, "pull": true}
  }]
}`
)

type prWorkspaceProviderToolRunnerFunc func(context.Context, workflows.ToolRequest) (map[string]any, error)

func (run prWorkspaceProviderToolRunnerFunc) RunTool(
	ctx context.Context,
	request workflows.ToolRequest,
) (map[string]any, error) {
	return run(ctx, request)
}

func TestPRWorkspaceGitHubResolverAcceptsCurrentMinimalPullContract(t *testing.T) {
	var requests []workflows.ToolRequest
	provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
		_ context.Context,
		request workflows.ToolRequest,
	) (map[string]any, error) {
		requests = append(requests, request)
		switch request.MCPTool {
		case reviews.GitHubPullRequestReadTool:
			return map[string]any{"text": testPRWorkspacePullJSON}, nil
		case reviews.GitHubSearchRepositoriesTool:
			return map[string]any{"text": testPRWorkspaceRepositoryJSON}, nil
		case reviews.GitHubGetMeTool:
			return map[string]any{"text": `{"id":40304587,"login":"dkropachev"}`}, nil
		default:
			return nil, errors.New("unexpected tool: " + request.MCPTool)
		}
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	resolver := &prWorkspaceGitHubResolver{provider: provider, canReview: true, canCreateIssue: true}
	snapshot, err := resolver.ResolvePullRequest(t.Context(), prworkspace.ResolveRequest{
		PullRequestURL: "https://github.com/dkropachev/picoclaw-pr-workspace-e2e-20260814/pull/1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Provider != "github" || snapshot.ProviderOrigin != "https://github.com" ||
		snapshot.RepositoryID != "1333775490" || snapshot.Repository != testPRWorkspaceRepository ||
		snapshot.PullRequestID != stableGitHubPullID("https://github.com", "1333775490", 1) ||
		snapshot.PullNumber != 1 || snapshot.AuthorID != "40304587" ||
		snapshot.AuthenticatedUserID != "40304587" || snapshot.HeadRepositoryID != "1333775490" ||
		snapshot.State != "open" || !snapshot.Owned || !snapshot.HeadWritable ||
		!snapshot.CanReview || !snapshot.CanCreateIssue || snapshot.ProviderRevision == "" ||
		snapshot.ObservedAt.IsZero() {
		t.Fatalf("resolved snapshot = %#v", snapshot)
	}
	if len(requests) != 3 {
		t.Fatalf("tool requests = %#v", requests)
	}
	if requests[0].MCPTool != reviews.GitHubPullRequestReadTool || !reflect.DeepEqual(requests[0].Args, map[string]any{
		"method": "get", "owner": "dkropachev", "repo": "picoclaw-pr-workspace-e2e-20260814", "pullNumber": int64(1),
	}) {
		t.Fatalf("pull request = %#v", requests[0])
	}
	if requests[1].MCPTool != reviews.GitHubSearchRepositoriesTool ||
		!reflect.DeepEqual(requests[1].Args, map[string]any{
			"query":          "picoclaw-pr-workspace-e2e-20260814 in:name user:dkropachev",
			"minimal_output": false, "page": 1, "perPage": 100,
		}) {
		t.Fatalf("repository request = %#v", requests[1])
	}
	if requests[2].MCPTool != reviews.GitHubGetMeTool || len(requests[2].Args) != 0 {
		t.Fatalf("viewer request = %#v", requests[2])
	}
}

func TestPRWorkspaceGitHubResolverRequiresAuthoritativeViewer(t *testing.T) {
	wantErr := errors.New("viewer unavailable")
	provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
		_ context.Context,
		request workflows.ToolRequest,
	) (map[string]any, error) {
		switch request.MCPTool {
		case reviews.GitHubPullRequestReadTool:
			return map[string]any{"text": testPRWorkspacePullJSON}, nil
		case reviews.GitHubSearchRepositoriesTool:
			return map[string]any{"text": testPRWorkspaceRepositoryJSON}, nil
		case reviews.GitHubGetMeTool:
			return nil, wantErr
		default:
			return nil, errors.New("unexpected tool")
		}
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	resolver := &prWorkspaceGitHubResolver{provider: provider}
	_, err = resolver.ResolvePullRequest(t.Context(), prworkspace.ResolveRequest{
		ProviderOrigin: "https://github.com", Repository: testPRWorkspaceRepository, PullNumber: 1,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("ResolvePullRequest() error = %v", err)
	}
}

func TestExactPRWorkspaceRepositoryRejectsUnverifiedAuthority(t *testing.T) {
	valid := testPRWorkspaceRepositoryJSON
	itemStart := strings.Index(valid, `"items": [`) + len(`"items": [`)
	itemEnd := strings.LastIndex(valid, "]\n}")
	item := valid[itemStart:itemEnd]
	duplicate := valid[:itemEnd] + "," + item + valid[itemEnd:]
	duplicate = strings.Replace(duplicate, `"total_count": 1`, `"total_count": 2`, 1)
	tests := map[string]struct {
		raw       string
		wantFound bool
		wantError string
	}{
		"valid":          {raw: valid, wantFound: true},
		"no exact match": {raw: strings.ReplaceAll(valid, testPRWorkspaceRepository, "dkropachev/similar-repo")},
		"incomplete search": {
			raw:       strings.Replace(valid, `"incomplete_results": false`, `"incomplete_results": true`, 1),
			wantError: "search response",
		},
		"duplicate identity": {raw: duplicate, wantError: "identity is ambiguous"},
		"missing permissions": {raw: strings.Replace(valid,
			`"permissions": {"admin": true, "maintain": true, "push": true, "triage": true, "pull": true}`,
			`"permissions": null`, 1), wantError: "authority is incomplete"},
		"wrong provider URL": {
			raw:       strings.Replace(valid, "https://github.com/dkropachev", "https://example.test/dkropachev", 1),
			wantError: "authority is incomplete",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, found, err := exactPRWorkspaceRepository(
				[]byte(test.raw),
				"https://github.com",
				testPRWorkspaceRepository,
			)
			if found != test.wantFound {
				t.Fatalf("found = %t, want %t", found, test.wantFound)
			}
			if test.wantError == "" && err != nil {
				t.Fatalf("error = %v", err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestPRWorkspaceGitHubResolverFallsBackToOrganizationRepositorySearch(t *testing.T) {
	var queries []string
	provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
		_ context.Context,
		request workflows.ToolRequest,
	) (map[string]any, error) {
		switch request.MCPTool {
		case reviews.GitHubPullRequestReadTool:
			return map[string]any{"text": testPRWorkspacePullJSON}, nil
		case reviews.GitHubSearchRepositoriesTool:
			queries = append(queries, request.Args["query"].(string))
			if len(queries) == 1 {
				return map[string]any{"text": `{"total_count":0,"incomplete_results":false,"items":[]}`}, nil
			}
			return map[string]any{"text": testPRWorkspaceRepositoryJSON}, nil
		case reviews.GitHubGetMeTool:
			return map[string]any{"text": `{"id":40304587,"login":"dkropachev"}`}, nil
		default:
			return nil, errors.New("unexpected tool")
		}
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	resolver := &prWorkspaceGitHubResolver{provider: provider}
	if _, err = resolver.ResolvePullRequest(t.Context(), prworkspace.ResolveRequest{
		ProviderOrigin: "https://github.com", Repository: testPRWorkspaceRepository, PullNumber: 1,
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"picoclaw-pr-workspace-e2e-20260814 in:name user:dkropachev",
		"picoclaw-pr-workspace-e2e-20260814 in:name org:dkropachev",
	}
	if !reflect.DeepEqual(queries, want) {
		t.Fatalf("search queries = %#v, want %#v", queries, want)
	}
}
