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

func TestPRWorkspaceIssuePublisherNormalizesLabelsBeforeDispatch(t *testing.T) {
	var request workflows.ToolRequest
	provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
		_ context.Context,
		input workflows.ToolRequest,
	) (map[string]any, error) {
		request = input
		return map[string]any{"text": `{"id":7,"html_url":"https://github.com/octo/repo/issues/7"}`}, nil
	}), "")
	if err != nil {
		t.Fatal(err)
	}

	result, err := (&prWorkspaceGitHubIssuePublisher{provider: provider}).CreateIssue(
		t.Context(),
		prworkspace.IssuePublicationRequest{
			ProviderOrigin: "https://github.com", Repository: "octo/repo",
			Title: "Deferred finding", Body: "Details", Labels: []string{"picoclaw"},
			Marker: "picoclaw-pr-publication:ppb_test:sha256:test",
		},
	)
	if err != nil || result.Ambiguous || result.ExternalID != "7" {
		t.Fatalf("CreateIssue() = (%#v, %v)", result, err)
	}
	if request.MCPTool != reviews.GitHubIssueWriteTool ||
		!reflect.DeepEqual(request.Args["labels"], []any{"picoclaw"}) {
		t.Fatalf("issue write request = %#v", request)
	}
}

func TestPRWorkspaceIssuePublisherDistinguishesPreDispatchAndAmbiguousFailures(t *testing.T) {
	tests := []struct {
		name          string
		title         string
		runnerErr     error
		wantAmbiguous bool
	}{
		{name: "adapter validation is definitely pre-dispatch", title: " ", wantAmbiguous: false},
		{
			name:          "runner validation is definitely pre-dispatch",
			title:         "Deferred finding",
			runnerErr:     workflows.ErrToolCallNotDispatched,
			wantAmbiguous: false,
		},
		{
			name:          "transport failure remains ambiguous",
			title:         "Deferred finding",
			runnerErr:     errors.New("transport failed"),
			wantAmbiguous: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
				context.Context,
				workflows.ToolRequest,
			) (map[string]any, error) {
				return nil, test.runnerErr
			}), "")
			if err != nil {
				t.Fatal(err)
			}
			result, createErr := (&prWorkspaceGitHubIssuePublisher{provider: provider}).CreateIssue(
				t.Context(),
				prworkspace.IssuePublicationRequest{
					ProviderOrigin: "https://github.com", Repository: "octo/repo",
					Title: test.title, Marker: "picoclaw-pr-publication:ppb_test:sha256:test",
				},
			)
			if createErr == nil || result.Ambiguous != test.wantAmbiguous {
				t.Fatalf("CreateIssue() = (%#v, %v), want ambiguous=%v", result, createErr, test.wantAmbiguous)
			}
			if !test.wantAmbiguous && !errors.Is(createErr, reviews.ErrWorkspaceProviderCallNotDispatched) {
				t.Fatalf("pre-dispatch error = %v", createErr)
			}
		})
	}
}

func TestIssueURLRepositoryComparisonIsCaseInsensitive(t *testing.T) {
	if !issueURLBelongsToRepository(
		"https://github.com/Owner/Repo/issues/12",
		"https://github.com",
		"owner/repo",
	) {
		t.Fatal("canonical GitHub issue URL casing did not match repository identity")
	}
}

func TestPRWorkspaceIssuePublisherFindByMarkerValidatesAndReconcilesExactlyOneIssue(t *testing.T) {
	marker := "picoclaw-pr-publication:ppb_test:sha256:test"
	for _, test := range []struct {
		name       string
		publisher  *prWorkspaceGitHubIssuePublisher
		repository string
		marker     string
	}{
		{name: "nil publisher", repository: "octo/repo", marker: marker},
		{
			name: "nil provider", publisher: &prWorkspaceGitHubIssuePublisher{},
			repository: "octo/repo", marker: marker,
		},
		{
			name: "empty marker", publisher: &prWorkspaceGitHubIssuePublisher{},
			repository: "octo/repo",
		},
		{
			name: "invalid repository", publisher: &prWorkspaceGitHubIssuePublisher{},
			repository: "octo/repo/extra", marker: marker,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, found, err := test.publisher.FindIssueByMarker(
				t.Context(), "https://github.com", "ignored", test.repository, test.marker,
			)
			if found || !errors.Is(err, prworkspace.ErrInvalid) {
				t.Fatalf("FindIssueByMarker() found=%v err=%v", found, err)
			}
		})
	}

	tests := []struct {
		name        string
		response    string
		providerErr error
		wantFound   bool
		wantID      string
		wantURL     string
		wantError   string
	}{
		{name: "provider failure", providerErr: errors.New("GitHub unavailable"), wantError: "GitHub unavailable"},
		{name: "invalid wire shape", response: `42`, wantError: "search response is invalid"},
		{name: "empty envelope", response: `{"items":[]}`},
		{
			name: "direct array",
			response: `[{"number":7,"html_url":"https://github.com/octo/repo/issues/7",` +
				`"body":"<!-- ` + marker + ` -->"}]`,
			wantFound: true, wantID: "7", wantURL: "https://github.com/octo/repo/issues/7",
		},
		{
			name: "filters foreign repository and body mismatch",
			response: `{"items":[` +
				`{"id":6,"html_url":"https://github.com/other/repo/issues/6","body":"` + marker + `"},` +
				`{"id":7,"html_url":"https://github.com/octo/repo/issues/7","body":"different marker"},` +
				`{"id":8,"html_url":"https://github.com/octo/repo/issues/8","body":"` + marker + `"}` +
				`]}`,
			wantFound: true, wantID: "8", wantURL: "https://github.com/octo/repo/issues/8",
		},
		{
			name: "multiple exact matches",
			response: `{"items":[` +
				`{"id":8,"html_url":"https://github.com/octo/repo/issues/8","body":"` + marker + `"},` +
				`{"id":9,"html_url":"https://github.com/octo/repo/issues/9","body":"` + marker + `"}` +
				`]}`,
			wantError: "multiple GitHub issues",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
				_ context.Context,
				request workflows.ToolRequest,
			) (map[string]any, error) {
				calls++
				if request.MCPTool != reviews.GitHubSearchIssuesTool ||
					request.Args["query"] != `"`+marker+`" in:body is:issue repo:octo/repo` {
					t.Fatalf("issue search request=%#v", request)
				}
				if test.providerErr != nil {
					return nil, test.providerErr
				}
				return map[string]any{"text": test.response}, nil
			}), "")
			if err != nil {
				t.Fatal(err)
			}
			result, found, findErr := (&prWorkspaceGitHubIssuePublisher{
				provider: provider,
			}).FindIssueByMarker(
				t.Context(), "https://github.com", "ignored", "octo/repo", marker,
			)
			if calls != 1 || found != test.wantFound || result.ExternalID != test.wantID ||
				result.ExternalURL != test.wantURL ||
				(test.wantError == "" && findErr != nil) ||
				(test.wantError != "" && (findErr == nil || !strings.Contains(findErr.Error(), test.wantError))) {
				t.Fatalf(
					"FindIssueByMarker() result=%#v found=%v err=%v calls=%d",
					result, found, findErr, calls,
				)
			}
		})
	}
}
