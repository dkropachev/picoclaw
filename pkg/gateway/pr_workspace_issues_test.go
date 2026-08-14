package gateway

import (
	"context"
	"errors"
	"reflect"
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
	if request.MCPTool != reviews.GitHubIssueWriteTool || !reflect.DeepEqual(request.Args["labels"], []any{"picoclaw"}) {
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
		{name: "runner validation is definitely pre-dispatch", title: "Deferred finding", runnerErr: workflows.ErrToolCallNotDispatched, wantAmbiguous: false},
		{name: "transport failure remains ambiguous", title: "Deferred finding", runnerErr: errors.New("transport failed"), wantAmbiguous: true},
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
