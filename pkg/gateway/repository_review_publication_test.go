package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestCreateRepositoryReviewIssueUsesFrozenDraftAndStableMarker(t *testing.T) {
	var request workflows.ToolRequest
	provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
		_ context.Context,
		input workflows.ToolRequest,
	) (map[string]any, error) {
		request = input
		return map[string]any{"text": `{"id":12,"html_url":"https://github.com/owner/repo/issues/12"}`}, nil
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	draft := repoaudit.IssueDraft{
		ID: "rid_test", Repository: "owner/repo", Title: "Validated bug",
		Body: "Exact finding body", Labels: []string{"bug", "concurrency"},
	}
	marker := repositoryReviewIssueMarker(draft.ID)
	result, err := createRepositoryReviewIssue(t.Context(), provider, draft.Repository, draft, marker)
	if err != nil || result.ExternalID != "12" || result.ExternalURL != "https://github.com/owner/repo/issues/12" {
		t.Fatalf("create result=%#v err=%v", result, err)
	}
	if request.MCPTool != reviews.GitHubIssueWriteTool || request.Args["title"] != draft.Title ||
		!strings.Contains(request.Args["body"].(string), "<!-- "+marker+" -->") ||
		!reflect.DeepEqual(request.Args["labels"], []any{"bug", "concurrency"}) {
		t.Fatalf("issue request=%#v", request)
	}
}

func TestFindRepositoryReviewIssueRecoversOnlyExactRepositoryMarker(t *testing.T) {
	provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
		_ context.Context,
		input workflows.ToolRequest,
	) (map[string]any, error) {
		if input.MCPTool != reviews.GitHubSearchIssuesTool {
			t.Fatalf("tool=%q", input.MCPTool)
		}
		return map[string]any{"text": `{"items":[` +
			`{"id":9,"html_url":"https://github.com/foreign/repo/issues/9","body":"picoclaw-repository-review:rid_test"},` +
			`{"id":12,"html_url":"https://github.com/owner/repo/issues/12","body":"picoclaw-repository-review:rid_test"}` +
			`]}`}, nil
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	result, found, err := findRepositoryReviewIssue(
		t.Context(), provider, "owner/repo", "picoclaw-repository-review:rid_test",
	)
	if err != nil || !found || result.ExternalID != "12" {
		t.Fatalf("find result=%#v found=%v err=%v", result, found, err)
	}
}

func TestRepositoryReviewPublicationRouteParsingIsExact(t *testing.T) {
	valid := httptest.NewRequest(
		http.MethodPost,
		repositoryReviewPublicationRoute+"rrp_test/issue-drafts/rid_test/publish",
		strings.NewReader(`{"expected_version":1}`),
	)
	repositoryID, draftID, ok := repositoryReviewPublicationRouteIDs(valid)
	if !ok || repositoryID != "rrp_test" || draftID != "rid_test" {
		t.Fatalf("route IDs=%q/%q ok=%v", repositoryID, draftID, ok)
	}
	for _, target := range []string{
		repositoryReviewPublicationRoute + "rrp_test/issue-drafts/rid_test",
		repositoryReviewPublicationRoute + "rrp_test/issue-drafts/rid_test/publish/extra",
		repositoryReviewPublicationRoute + "rrp_test/issue-drafts//publish",
	} {
		request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{}`))
		if _, _, accepted := repositoryReviewPublicationRouteIDs(request); accepted {
			t.Fatalf("accepted invalid route %q", target)
		}
	}
}

func TestRepositoryReviewIssueCreateAmbiguityIsStatusAware(t *testing.T) {
	if repositoryReviewIssueCreateAmbiguous(workflows.ErrToolCallNotDispatched) {
		t.Fatal("pre-dispatch error became ambiguous")
	}
	if repositoryReviewIssueCreateAmbiguous(errors.New("API error status: 403 forbidden")) {
		t.Fatal("definitive provider rejection became ambiguous")
	}
	if repositoryReviewIssueCreateAmbiguous(errors.New("GitHub issue create status: 422 validation failed")) {
		t.Fatal("definitive 422 validation rejection became ambiguous")
	}
	if !repositoryReviewIssueCreateAmbiguous(errors.New("connection reset by peer")) {
		t.Fatal("transport error was treated as definite")
	}
	if !repositoryReviewIssueCreateAmbiguous(errors.New("GitHub issue create status: 429 rate limit exceeded")) {
		t.Fatal("post-dispatch rate limit was treated as definite")
	}
	if !repositoryReviewIssueCreateAmbiguous(errors.New("GitHub issue create status: 503 overloaded")) {
		t.Fatal("post-dispatch overload was treated as definite")
	}
}

func TestRepositoryReviewGitHubIdentityRequiresCanonicalDerivedShape(t *testing.T) {
	for _, value := range []string{"owner/repo", "owner/repo.name", "owner/repo_name"} {
		if !validRepositoryReviewGitHubIdentity(value) {
			t.Fatalf("rejected canonical identity %q", value)
		}
	}
	for _, value := range []string{
		"Owner/Repo", "/tmp/repo", "https://github.com/owner/repo", "owner/repo/extra",
		"foo_bar/foo_bar",
	} {
		if validRepositoryReviewGitHubIdentity(value) {
			t.Fatalf("accepted unverified identity %q", value)
		}
	}
}
