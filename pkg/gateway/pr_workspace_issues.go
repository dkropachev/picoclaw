package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/reviews"
)

type prWorkspaceGitHubIssuePublisher struct {
	provider *reviews.GitHubProvider
}

type prWorkspaceGitHubIssueWire struct {
	ID      json.RawMessage `json:"id"`
	Number  json.RawMessage `json:"number"`
	HTMLURL string          `json:"html_url"`
	URL     string          `json:"url"`
	Body    string          `json:"body"`
}

func (publisher *prWorkspaceGitHubIssuePublisher) CreateIssue(
	ctx context.Context,
	request prworkspace.IssuePublicationRequest,
) (prworkspace.IssuePublicationResult, error) {
	if publisher == nil || publisher.provider == nil || request.Marker == "" {
		return prworkspace.IssuePublicationResult{}, errors.New("GitHub issue publisher is unavailable")
	}
	owner, repo, ok := strings.Cut(request.Repository, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return prworkspace.IssuePublicationResult{}, prworkspace.ErrInvalid
	}
	body := strings.TrimSpace(request.Body) + "\n\n<!-- " + request.Marker + " -->"
	raw, err := publisher.provider.CreateWorkspaceIssueJSON(ctx, map[string]any{
		"owner": owner, "repo": repo, "title": request.Title,
		"body": body, "labels": request.Labels,
	})
	if err != nil {
		// Only failures after dispatch are ambiguous. Deterministic adapter
		// validation failures are known to have no external side effect and can
		// safely enter the ordinary retryable failed state.
		return prworkspace.IssuePublicationResult{
			Ambiguous: reviews.WorkspaceProviderCallMayHaveChangedExternalState(err),
		}, err
	}
	issue, err := decodePRWorkspaceIssue(raw)
	if err != nil {
		return prworkspace.IssuePublicationResult{Ambiguous: true}, err
	}
	if !issueURLBelongsToRepository(issueURL(issue), request.ProviderOrigin, request.Repository) {
		return prworkspace.IssuePublicationResult{
				Ambiguous: true,
			}, errors.New(
				"GitHub issue response belongs to another repository",
			)
	}
	return prworkspace.IssuePublicationResult{ExternalID: issueID(issue), ExternalURL: issueURL(issue)}, nil
}

func (publisher *prWorkspaceGitHubIssuePublisher) FindIssueByMarker(
	ctx context.Context,
	providerOrigin, _ string,
	repository, marker string,
) (prworkspace.IssuePublicationResult, bool, error) {
	owner, repo, repositoryOK := strings.Cut(repository, "/")
	if publisher == nil || publisher.provider == nil || marker == "" || !repositoryOK || owner == "" || repo == "" ||
		strings.Contains(repo, "/") {
		return prworkspace.IssuePublicationResult{}, false, prworkspace.ErrInvalid
	}
	raw, err := publisher.provider.SearchWorkspaceIssuesJSON(ctx, map[string]any{
		"query": `"` + marker + `" in:body is:issue repo:` + repository,
	})
	if err != nil {
		return prworkspace.IssuePublicationResult{}, false, err
	}
	var envelope struct {
		Items []prWorkspaceGitHubIssueWire `json:"items"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		var direct []prWorkspaceGitHubIssueWire
		if directErr := json.Unmarshal(raw, &direct); directErr != nil {
			return prworkspace.IssuePublicationResult{}, false, errors.New("GitHub issue search response is invalid")
		}
		envelope.Items = direct
	}
	var matched []prWorkspaceGitHubIssueWire
	for _, issue := range envelope.Items {
		if strings.Contains(issue.Body, marker) &&
			issueURLBelongsToRepository(issueURL(issue), providerOrigin, repository) {
			matched = append(matched, issue)
		}
	}
	if len(matched) == 0 {
		return prworkspace.IssuePublicationResult{}, false, nil
	}
	if len(matched) != 1 {
		return prworkspace.IssuePublicationResult{}, false, errors.New(
			"multiple GitHub issues contain the publication marker",
		)
	}
	return prworkspace.IssuePublicationResult{
		ExternalID:  issueID(matched[0]),
		ExternalURL: issueURL(matched[0]),
	}, true, nil
}

func issueURLBelongsToRepository(raw, origin, repository string) bool {
	issue, err := url.ParseRequestURI(raw)
	if err != nil || issue.Scheme != "https" || issue.Host == "" || issue.User != nil ||
		issue.RawQuery != "" || issue.Fragment != "" {
		return false
	}
	provider, err := url.ParseRequestURI(origin)
	if err != nil || !strings.EqualFold(issue.Scheme, provider.Scheme) ||
		!strings.EqualFold(issue.Host, provider.Host) {
		return false
	}
	prefix := strings.TrimSuffix(provider.Path, "/") + "/" + repository + "/issues/"
	if len(issue.Path) <= len(prefix) || !strings.EqualFold(issue.Path[:len(prefix)], prefix) {
		return false
	}
	number := issue.Path[len(prefix):]
	if number == "" || strings.Contains(number, "/") {
		return false
	}
	for _, character := range number {
		if character < '0' || character > '9' {
			return false
		}
	}
	return number != "0"
}

func decodePRWorkspaceIssue(raw []byte) (prWorkspaceGitHubIssueWire, error) {
	var issue prWorkspaceGitHubIssueWire
	if err := json.Unmarshal(raw, &issue); err != nil {
		return issue, errors.New("GitHub issue response is invalid")
	}
	if issueID(issue) == "" || !strings.HasPrefix(issueURL(issue), "https://") {
		return issue, errors.New("GitHub issue response is incomplete")
	}
	return issue, nil
}

func issueID(issue prWorkspaceGitHubIssueWire) string {
	if id := githubScalarID(issue.ID); id != "" {
		return id
	}
	return githubScalarID(issue.Number)
}

func issueURL(issue prWorkspaceGitHubIssueWire) string {
	if issue.HTMLURL != "" {
		return issue.HTMLURL
	}
	return issue.URL
}

var _ prworkspace.IssuePublisher = (*prWorkspaceGitHubIssuePublisher)(nil)
