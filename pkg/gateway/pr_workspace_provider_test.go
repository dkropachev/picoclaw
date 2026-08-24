package gateway

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
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
	testDevelopmentIssueJSON = `{
  "id": 77,
  "number": 7,
  "title": "Add durable notifications",
  "body": "Build a focused inbox.",
  "state": "open",
  "html_url": "https://github.com/octo/repo/issues/7",
  "updated_at": "2026-08-20T12:00:00Z",
  "user": {"id": 55, "login": "issue-author"}
}`
	testDevelopmentRepositoryJSON = `{
  "total_count": 1,
  "incomplete_results": false,
  "items": [{
    "id": 42,
    "owner": {"login": "octo", "id": 11},
    "name": "repo",
    "full_name": "octo/repo",
    "html_url": "https://github.com/octo/repo",
    "has_issues": true,
    "permissions": {"admin": false, "maintain": false, "push": true, "triage": true, "pull": true},
    "default_branch": "main"
  }]
}`
	testDevelopmentCommitSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
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

func TestConfiguredRepositoryIntakeFencesStableProviderID(t *testing.T) {
	repositoryJSON := strings.Replace(testPRWorkspaceRepositoryJSON,
		`"id": 1333775490`, `"id": 43`, 1)
	repositoryJSON = strings.Replace(repositoryJSON,
		`"has_issues": true,`, `"has_issues": true, "default_branch": "main",`, 1)
	provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
		_ context.Context, request workflows.ToolRequest,
	) (map[string]any, error) {
		switch request.MCPTool {
		case reviews.GitHubSearchRepositoriesTool:
			return map[string]any{"text": repositoryJSON}, nil
		case reviews.GitHubListCommitsTool:
			return map[string]any{"text": `[{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]`}, nil
		case reviews.GitHubGetMeTool:
			return map[string]any{"text": `{"id":40304587,"login":"dkropachev"}`}, nil
		default:
			return nil, errors.New("unexpected tool: " + request.MCPTool)
		}
	}), "")
	require.NoError(t, err)
	identity := "https://github.com|42"
	resolver := &prWorkspaceGitHubResolver{
		provider: provider,
		repositories: map[string]config.PRLifecycleRepositoryDescriptor{
			identity: {Name: testPRWorkspaceRepository, DefaultBranch: "main"},
		},
	}
	_, err = resolver.ResolveRepository(t.Context(), prworkspace.RepositoryResolveRequest{
		RepositoryIdentity: identity, Brief: "Add notifications",
	})
	require.ErrorContains(t, err, "identity changed")
}

func TestDevelopmentGitHubResolverIssueBriefAndRepositoryCatalog(t *testing.T) {
	var requests []workflows.ToolRequest
	provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
		_ context.Context, request workflows.ToolRequest,
	) (map[string]any, error) {
		requests = append(requests, request)
		switch request.MCPTool {
		case reviews.GitHubIssueReadTool:
			switch request.Args["method"] {
			case "get":
				return map[string]any{"text": testDevelopmentIssueJSON}, nil
			case "get_comments":
				return map[string]any{"text": `[{"id":1,"body":"Keep provider comments untrusted."}]`}, nil
			}
		case reviews.GitHubSearchRepositoriesTool:
			return map[string]any{"text": testDevelopmentRepositoryJSON}, nil
		case reviews.GitHubListCommitsTool:
			return map[string]any{"text": `[{"sha":"` + testDevelopmentCommitSHA + `"}]`}, nil
		case reviews.GitHubGetMeTool:
			return map[string]any{"text": `{"id":99,"login":"automation-user"}`}, nil
		}
		return nil, errors.New("unexpected tool: " + request.MCPTool)
	}), "")
	require.NoError(t, err)

	resolver := &prWorkspaceGitHubResolver{
		provider: provider, canCreateIssue: true, canCreatePullRequest: true,
		repositories: map[string]config.PRLifecycleRepositoryDescriptor{
			"https://github.com|7":  {Name: "octo/another", DefaultBranch: "trunk"},
			"https://github.com|42": {Name: "octo/repo", DefaultBranch: "main"},
		},
	}
	issue, err := resolver.ResolveIssue(t.Context(), prworkspace.IssueResolveRequest{
		IssueURL: "https://github.com/octo/repo/issues/7",
	})
	require.NoError(t, err)
	require.Equal(t, "77", issue.SourceID)
	require.Equal(t, int64(7), issue.SourceNumber)
	require.Equal(t, "https://github.com/octo/repo/issues/7", issue.SourceURL)
	require.Equal(t, "42", issue.RepositoryID)
	require.Equal(t, "octo/repo", issue.Repository)
	require.Equal(t, "issue-author", issue.AuthorLogin)
	require.Equal(t, "99", issue.AuthenticatedUserID)
	require.Equal(t, "main", issue.BaseRef)
	require.Equal(t, testDevelopmentCommitSHA, issue.BaseSHA)
	require.Equal(t, issue.BaseSHA, issue.HeadSHA)
	require.True(t, issue.HeadWritable)
	require.True(t, issue.CanCreateIssue)
	require.True(t, issue.CanCreatePullRequest)
	require.Contains(t, issue.Body, "Provider issue comments (untrusted evidence)")
	require.Contains(t, issue.Body, "Keep provider comments untrusted")
	require.NotEmpty(t, issue.ProviderRevision)
	require.False(t, issue.ObservedAt.IsZero())

	brief, err := resolver.ResolveRepository(t.Context(), prworkspace.RepositoryResolveRequest{
		RepositoryIdentity: "https://github.com|42", Brief: "Add mobile notifications",
	})
	require.NoError(t, err)
	require.Equal(t, "Feature brief", brief.Title)
	require.Equal(t, "Add mobile notifications", brief.Body)
	require.Equal(t, "automation-user", brief.AuthorLogin)
	require.True(t, strings.HasPrefix(brief.SourceID, "sha256:"))
	require.True(t, strings.HasPrefix(brief.ProviderRevision, "sha256:"))

	configured, err := resolver.ListConfiguredRepositories(t.Context())
	require.NoError(t, err)
	require.Equal(t, []prworkspace.ConfiguredRepository{
		{Identity: "https://github.com|42", Name: "octo/repo", DefaultBranch: "main", CanImplement: true},
		{Identity: "https://github.com|7", Name: "octo/another", DefaultBranch: "trunk", CanImplement: true},
	}, configured)

	verified, err := resolver.VerifyRepository(t.Context(), "https://GITHUB.com/octo/repo.git")
	require.NoError(t, err)
	require.Equal(t, prworkspace.ConfiguredRepository{
		Identity: "https://github.com|42", Name: "octo/repo", DefaultBranch: "main", CanImplement: true,
	}, verified)

	require.GreaterOrEqual(t, len(requests), 9)
	for _, request := range requests {
		require.True(t, request.MCP)
		require.Equal(t, reviews.DefaultGitHubMCPServer, request.MCPServer)
	}
}

func TestDevelopmentGitHubResolverRejectsUntrustedIntake(t *testing.T) {
	t.Run("unavailable boundaries", func(t *testing.T) {
		var resolver *prWorkspaceGitHubResolver
		_, err := resolver.ResolveIssue(t.Context(), prworkspace.IssueResolveRequest{})
		require.ErrorContains(t, err, "unavailable")
		_, err = resolver.ResolveRepository(t.Context(), prworkspace.RepositoryResolveRequest{})
		require.ErrorContains(t, err, "unavailable")
		_, err = resolver.ListConfiguredRepositories(t.Context())
		require.ErrorContains(t, err, "unavailable")
	})

	for name, raw := range map[string]string{
		"closed issue": strings.Replace(testDevelopmentIssueJSON, `"state": "open"`, `"state": "closed"`, 1),
		"wrong issue URL": strings.Replace(
			testDevelopmentIssueJSON, "/octo/repo/issues/7", "/octo/repo/issues/8", 1,
		),
		"missing author": strings.Replace(
			testDevelopmentIssueJSON, `"user": {"id": 55, "login": "issue-author"}`, `"user": null`, 1,
		),
	} {
		t.Run(name, func(t *testing.T) {
			provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
				_ context.Context, request workflows.ToolRequest,
			) (map[string]any, error) {
				if request.MCPTool != reviews.GitHubIssueReadTool {
					return nil, errors.New("unexpected downstream authority call")
				}
				return map[string]any{"text": raw}, nil
			}), "")
			require.NoError(t, err)
			resolver := &prWorkspaceGitHubResolver{provider: provider}
			_, err = resolver.ResolveIssue(t.Context(), prworkspace.IssueResolveRequest{
				IssueURL: "https://github.com/octo/repo/issues/7",
			})
			require.ErrorContains(t, err, "identity is invalid")
		})
	}

	provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
		_ context.Context, request workflows.ToolRequest,
	) (map[string]any, error) {
		if request.MCPTool != reviews.GitHubSearchRepositoriesTool {
			return nil, errors.New("unexpected tool")
		}
		readOnly := strings.Replace(testDevelopmentRepositoryJSON, `"push": true`, `"push": false`, 1)
		return map[string]any{"text": readOnly}, nil
	}), "")
	require.NoError(t, err)
	resolver := &prWorkspaceGitHubResolver{provider: provider, canCreatePullRequest: true}
	_, err = resolver.VerifyRepository(t.Context(), "https://github.com/octo/repo")
	require.ErrorContains(t, err, "not writable")
	for _, invalid := range []string{
		"http://github.com/octo/repo", "https://user@github.com/octo/repo",
		"https://github.com/octo/repo?tab=readme", "https://github.com/octo/repo/tree/main",
	} {
		_, err = resolver.VerifyRepository(t.Context(), invalid)
		require.ErrorIs(t, err, prworkspace.ErrInvalid)
	}
}

func TestDevelopmentGitHubIdentityAndEvidenceBounds(t *testing.T) {
	for name, test := range map[string]struct {
		raw  string
		want string
		err  string
	}{
		"forty":      {raw: `[{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]`, want: strings.Repeat("a", 40)},
		"sixty four": {raw: `[{"sha":"` + strings.Repeat("B", 64) + `"}]`, want: strings.Repeat("b", 64)},
		"multiple":   {raw: `[{"sha":"` + strings.Repeat("a", 40) + `"},{"sha":"` + strings.Repeat("b", 40) + `"}]`, err: "response"},
		"short":      {raw: `[{"sha":"abc"}]`, err: "identity"},
		"not hex":    {raw: `[{"sha":"` + strings.Repeat("z", 40) + `"}]`, err: "identity"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeSingleGitHubCommitSHA([]byte(test.raw))
			if test.err != "" {
				require.ErrorContains(t, err, test.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}

	prefix := strings.Repeat("x", (480<<10)-1)
	bounded := boundedGitHubIssueEvidence(prefix + "éoutside")
	require.True(t, strings.HasSuffix(bounded, "\n[truncated]"))
	require.True(t, strings.HasPrefix(bounded, prefix))
	require.True(t, len(bounded) <= (480<<10)+len("\n[truncated]"))
}

func TestGitHubResolverLoadsRevisionFencedReviewEvidence(t *testing.T) {
	provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
		_ context.Context, request workflows.ToolRequest,
	) (map[string]any, error) {
		switch request.MCPTool {
		case reviews.GitHubPullRequestReadTool:
			if request.Args["method"] == "get_diff" {
				return map[string]any{"text": "diff --git a/a.go b/a.go\n+fixed\n"}, nil
			}
			return map[string]any{"text": testPRWorkspacePullJSON}, nil
		case reviews.GitHubSearchRepositoriesTool:
			return map[string]any{"text": testPRWorkspaceRepositoryJSON}, nil
		case reviews.GitHubGetMeTool:
			return map[string]any{"text": `{"id":40304587,"login":"dkropachev"}`}, nil
		default:
			return nil, errors.New("unexpected tool: " + request.MCPTool)
		}
	}), "")
	require.NoError(t, err)
	resolver := &prWorkspaceGitHubResolver{provider: provider, canReview: true}
	expected, err := resolver.ResolvePullRequest(t.Context(), prworkspace.ResolveRequest{
		PullRequestURL: "https://github.com/" + testPRWorkspaceRepository + "/pull/1",
	})
	require.NoError(t, err)
	evidence, err := resolver.LoadReviewEvidence(t.Context(), expected)
	require.NoError(t, err)
	require.Equal(t, expected.ProviderRevision, evidence.ProviderRevision)
	require.Equal(t, expected.BaseSHA, evidence.BaseSHA)
	require.Equal(t, expected.HeadSHA, evidence.HeadSHA)
	require.Contains(t, evidence.UnifiedDiff, "+fixed")

	changed := expected
	changed.HeadSHA = "changed-head"
	_, err = resolver.LoadReviewEvidence(t.Context(), changed)
	require.ErrorIs(t, err, prworkspace.ErrConflict)
	var nilResolver *prWorkspaceGitHubResolver
	_, err = nilResolver.LoadReviewEvidence(t.Context(), prworkspace.ProviderSnapshot{})
	require.ErrorContains(t, err, "unavailable")
}

func TestDevelopmentFeatureRepositoryRejectsIncompleteViewerAuthority(t *testing.T) {
	for name, viewer := range map[string]string{
		"malformed":  `{`,
		"missing id": `{"id":0,"login":"automation-user"}`,
	} {
		t.Run(name, func(t *testing.T) {
			provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
				_ context.Context, request workflows.ToolRequest,
			) (map[string]any, error) {
				switch request.MCPTool {
				case reviews.GitHubSearchRepositoriesTool:
					return map[string]any{"text": testDevelopmentRepositoryJSON}, nil
				case reviews.GitHubListCommitsTool:
					return map[string]any{"text": `[{"sha":"` + testDevelopmentCommitSHA + `"}]`}, nil
				case reviews.GitHubGetMeTool:
					return map[string]any{"text": viewer}, nil
				default:
					return nil, errors.New("unexpected tool")
				}
			}), "")
			require.NoError(t, err)
			resolver := &prWorkspaceGitHubResolver{
				provider: provider,
				repositories: map[string]config.PRLifecycleRepositoryDescriptor{
					"https://github.com|42": {Name: "octo/repo", DefaultBranch: "main"},
				},
			}
			_, err = resolver.ResolveRepository(t.Context(), prworkspace.RepositoryResolveRequest{
				RepositoryIdentity: "https://github.com|42", Brief: "Add notifications",
			})
			require.Error(t, err)
		})
	}
}
