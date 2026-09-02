//nolint:govet // Independent provider assertions intentionally reuse err.
package githubpoll

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestGitHubPollConfigurationAndValidationResidualMatrix(t *testing.T) {
	store := newMemoryInserter()
	runner := &pollToolRunner{}
	cases := []Config{
		{ToolRunner: runner, Connectors: []Connector{{Name: "main"}}},
		{Store: store, Connectors: []Connector{{Name: "main"}}},
		{Store: store, ToolRunner: runner},
		{Store: store, ToolRunner: runner, Connectors: []Connector{{Name: ""}}},
		{Store: store, ToolRunner: runner, Connectors: []Connector{{Name: string([]byte{0xff})}}},
		{Store: store, ToolRunner: runner, Connectors: []Connector{{Name: strings.Repeat("x", maxRepositoryBytes+1)}}},
		{Store: store, ToolRunner: runner, Connectors: []Connector{{Name: "main"}, {Name: "MAIN"}}},
		{Store: store, ToolRunner: runner, Connectors: []Connector{{Name: "main", Repositories: []string{"bad"}}}},
		{Store: store, ToolRunner: runner, Connectors: []Connector{{
			Name: "main", Repositories: []string{"owner/repo", "OWNER/REPO"},
		}}},
	}
	for index, config := range cases {
		if poller, err := New(config); err == nil || poller != nil {
			t.Errorf("invalid config %d = %#v, %v", index, poller, err)
		}
	}
	poller, err := New(Config{
		Store: store, ToolRunner: runner,
		Connectors: []Connector{{Name: " main ", Repositories: []string{"Owner/Repo"}, TargetUser: " user "}},
	})
	if err != nil || poller.now == nil || poller.connectors[0].name != "main" ||
		poller.connectors[0].targetUser != "user" {
		t.Fatalf("valid config = %#v, %v", poller, err)
	}

	valid := validResidualNotification()
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*notification){
		func(value *notification) { value.ID = " bad " },
		func(value *notification) { value.Repository.FullName = "bad" },
		func(value *notification) { value.Repository.HTMLURL = "http://example.test/repo" },
		func(value *notification) { value.Reason = "" },
		func(value *notification) { value.Subject.Type = "" },
		func(value *notification) { value.UpdatedAt = "bad" },
	}
	for index, mutate := range mutations {
		candidate := valid
		mutate(&candidate)
		if err := candidate.validate(); err == nil {
			t.Errorf("invalid notification %d accepted: %#v", index, candidate)
		}
	}

	if err := decodeExactJSON([]byte(`{"ok":true}`), &map[string]any{}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{`, `{} {}`, `{} trailing`} {
		if err := decodeExactJSON([]byte(raw), &map[string]any{}); err == nil {
			t.Errorf("invalid exact JSON accepted: %q", raw)
		}
	}
}

func TestGitHubPollIdentityEnrichmentAndEnvelopeResidualMatrix(t *testing.T) {
	notification := validResidualNotification()
	owner, repo, number, err := pullRequestIdentity(notification)
	if err != nil || owner != "Owner" || repo != "Repo" || number != 7 {
		t.Fatalf("pull request identity = %q/%q/%d, %v", owner, repo, number, err)
	}
	badIdentity := notification
	badIdentity.Repository.FullName = "bad"
	if _, _, _, err := pullRequestIdentity(badIdentity); err == nil {
		t.Fatal("invalid repository identity accepted")
	}
	badIdentity = notification
	badIdentity.Subject.URL = "https://api.github.test/repos/other/repo/pulls/7"
	if _, _, _, err := pullRequestIdentity(badIdentity); err == nil {
		t.Fatal("mismatched resource identity accepted")
	}
	for _, test := range []struct {
		raw, owner, repo, collection string
		want                         int
	}{
		{"https://api.github.test/repos/Owner/Repo/pulls/7", "owner", "repo", "pulls", 7},
		{"http://api.github.test/repos/Owner/Repo/pulls/7", "owner", "repo", "pulls", 0},
		{"https://api.github.test/repos/Owner/Repo/issues/nope", "owner", "repo", "issues", 0},
		{"https://api.github.test/repos/Owner/Other/pulls/7", "owner", "repo", "pulls", 0},
	} {
		got, err := resourceNumber(test.raw, test.owner, test.repo, test.collection)
		if test.want == 0 && err == nil || test.want != 0 && (err != nil || got != test.want) {
			t.Errorf("resourceNumber(%q) = %d, %v", test.raw, got, err)
		}
	}
	validPR := pullRequest{
		Number: 7, HTMLURL: "https://github.test/Owner/Repo/pull/7", User: &pullRequestUser{Login: "user"},
		Head: &pullRequestBranch{Ref: "feature", SHA: strings.Repeat("b", 40), Repo: &pullRequestBranchRepo{
			FullName: "Fork/Repo",
		}},
		Base: &pullRequestBranch{Ref: "main", SHA: strings.Repeat("a", 40), Repo: &pullRequestBranchRepo{
			FullName: "Owner/Repo",
		}},
	}
	if err := validatePullRequestEnrichment(validPR, "Owner/Repo", 7); err != nil {
		t.Fatal(err)
	}
	prMutations := []func(*pullRequest){
		func(value *pullRequest) { value.Number = 8 },
		func(value *pullRequest) { value.HTMLURL = "" },
		func(value *pullRequest) { value.User = nil },
		func(value *pullRequest) { value.Head = nil },
		func(value *pullRequest) { value.Base = nil },
		func(value *pullRequest) { value.Base.Repo = nil },
		func(value *pullRequest) { value.Head.Repo.FullName = "bad" },
	}
	for index, mutate := range prMutations {
		candidate := validPR
		head, base := *validPR.Head, *validPR.Base
		headRepo, baseRepo := *validPR.Head.Repo, *validPR.Base.Repo
		head.Repo, base.Repo = &headRepo, &baseRepo
		candidate.Head, candidate.Base = &head, &base
		mutate(&candidate)
		if err := validatePullRequestEnrichment(candidate, "Owner/Repo", 7); err == nil {
			t.Errorf("invalid pull request %d accepted", index)
		}
	}
	if err := validatePullRequestEnrichment(validPR, "bad", 7); err == nil {
		t.Fatal("invalid repository accepted")
	}

	poller := &Poller{now: func() time.Time { return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC) }}
	envelope, err := poller.envelope(notification, &validPR, connector{name: "main", targetUser: "target"})
	if err != nil || envelope.Actor == nil || envelope.Attributes["pull_request_number"] != "7" {
		t.Fatalf("pull request envelope = %#v, %v", envelope, err)
	}
	issue := notification
	issue.Subject.Type = "Issue"
	issue.Subject.URL = "https://api.github.test/repos/Owner/Repo/issues/9"
	envelope, err = poller.envelope(issue, nil, connector{name: "main"})
	if err != nil || envelope.Attributes["issue_number"] != "9" {
		t.Fatalf("issue envelope = %#v, %v", envelope, err)
	}
	issue.Subject.URL = "https://api.github.test/repos/Other/Repo/issues/9"
	if _, err := poller.envelope(issue, nil, connector{name: "main"}); err == nil {
		t.Fatal("invalid issue envelope accepted")
	}
}

func TestGitHubPollPureProjectionResiduals(t *testing.T) {
	for reason, want := range map[string]string{
		"review_requested": "requested_reviewer", "mention": "mention", "assign": "assignee", "other": "",
	} {
		if got := notificationTargetReason(reason); got != want {
			t.Errorf("target reason %q = %q", reason, got)
		}
	}
	for value, want := range map[string]bool{
		"https://example.test/path": true, "": false, "http://example.test": false,
		"https://user@example.test": false, "https://example.test/#fragment": false,
	} {
		if got := validHTTPSURL(value); got != want {
			t.Errorf("validHTTPSURL(%q) = %v", value, got)
		}
	}
	for value, want := range map[string]bool{
		strings.Repeat("a", 40): true, strings.Repeat("b", 64): true,
		strings.Repeat("A", 40): false, "short": false,
	} {
		if got := validGitObjectID(value); got != want {
			t.Errorf("validGitObjectID(%q) = %v", value, got)
		}
	}
	if got := htmlResourceURL("bad", "issues", 1); got != "" {
		t.Fatalf("invalid resource URL = %q", got)
	}
	if got := htmlResourceURL("https://example.test/repo/", "issues", 2); got != "https://example.test/repo/issues/2" {
		t.Fatalf("resource URL = %q", got)
	}
	if got := boundedString(string([]byte{0xff}), 10); got != "" {
		t.Fatalf("invalid UTF-8 bounded string = %q", got)
	}
	if got := boundedString("ééé", 5); got != "éé" {
		t.Fatalf("bounded UTF-8 string = %q", got)
	}
	attributes := boundedAttributes(map[string]string{
		"empty": " ", "valid": " value ", "invalid": string([]byte{0xff}),
	})
	if len(attributes) != 1 || attributes["valid"] != "value" {
		t.Fatalf("bounded attributes = %#v", attributes)
	}
	repository := notificationRepository{
		ID: json.RawMessage(`"repo-id"`), FullName: "Owner/Repo", HTMLURL: "https://github.test/Owner/Repo",
	}
	subject := repositorySubject(repository)
	if subject.ID != "repo-id" || subject.Name != "Owner/Repo" {
		t.Fatalf("repository subject = %#v", subject)
	}
}

func validResidualNotification() notification {
	return notification{
		ID: "notification-7", Reason: "review_requested", UpdatedAt: "2026-09-02T12:00:00Z",
		Repository: notificationRepository{
			ID: json.RawMessage(`7`), Name: "Repo", FullName: "Owner/Repo",
			HTMLURL: "https://github.test/Owner/Repo", Owner: notificationRepositoryOwner{Login: "Owner"},
		},
		Subject: notificationSubject{
			Type: "PullRequest", URL: "https://api.github.test/repos/Owner/Repo/pulls/7",
		},
	}
}

type residualInserterFunc func(context.Context, eventing.Envelope) (eventing.InsertResult, error)

func (function residualInserterFunc) Insert(
	ctx context.Context,
	envelope eventing.Envelope,
) (eventing.InsertResult, error) {
	return function(ctx, envelope)
}

func TestGitHubPollToolAndIngestionFailureResiduals(t *testing.T) {
	if _, err := (*Poller)(nil).Poll(t.Context()); err == nil {
		t.Fatal("nil poller succeeded")
	}
	poller := &Poller{
		store: newMemoryInserter(), now: time.Now,
		connectors: []connector{{name: "main", repositories: map[string]struct{}{"owner/repo": {}}}},
	}
	poller.tools = toolRunnerFunc(func(context.Context, workflows.ToolRequest) (map[string]any, error) {
		return nil, errors.New("provider failed")
	})
	if _, err := poller.listNotifications(t.Context(), 1); err == nil {
		t.Fatal("notification provider failure lost")
	}
	if _, err := poller.readPullRequest(t.Context(), "owner", "repo", 1); err == nil {
		t.Fatal("pull request provider failure lost")
	}
	poller.tools = toolRunnerFunc(func(_ context.Context, request workflows.ToolRequest) (map[string]any, error) {
		if request.MCPTool == PullRequestReadTool {
			return map[string]any{"text": `not-json`}, nil
		}
		return map[string]any{"text": `[{}]`}, nil
	})
	if _, err := poller.readPullRequest(t.Context(), "owner", "repo", 1); err == nil {
		t.Fatal("invalid pull request tool result accepted")
	}
	if _, err := poller.Poll(t.Context()); err == nil {
		t.Fatal("poll accepted invalid provider notification")
	}
	for _, output := range []map[string]any{
		nil,
		{},
		{"text": 1},
		{"text": " "},
		{"text": string([]byte{0xff})},
		{"text": "not-json"},
		{"text": `{}`, "artifact_tags": "bad"},
		{"text": `{}`, "artifact_tags": []string{"[file:/tmp/x]"}},
	} {
		if _, err := poller.exactToolText(output); err == nil {
			t.Errorf("invalid exact tool output accepted: %#v", output)
		}
	}
	artifactRoot := t.TempDir()
	poller.artifactRoot = artifactRoot
	for _, output := range []map[string]any{
		{"artifact_tags": []string{}},
		{"artifact_tags": []string{"bad"}},
		{"artifact_tags": []string{"[file:" + filepath.Join(artifactRoot, "missing") + "]"}},
	} {
		if _, err := poller.exactArtifactText(output); err == nil {
			t.Errorf("invalid artifact accepted: %#v", output)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := poller.exactArtifactText(map[string]any{
		"artifact_tags": []string{"[file:" + outside + "]"},
	}); err == nil {
		t.Fatal("outside artifact accepted")
	}
	poller.artifactRoot = filepath.Join(t.TempDir(), "missing-root")
	if _, err := poller.exactToolText(map[string]any{
		"text":          `{}`,
		"artifact_tags": []string{"[file:" + outside + "]"},
	}); err == nil {
		t.Fatal("artifact with missing root accepted")
	}

	invalid := validResidualNotification()
	invalid.ID = " bad "
	if _, _, err := poller.ingestNotification(t.Context(), invalid); err == nil {
		t.Fatal("invalid notification ingested")
	}
	unmatched := validResidualNotification()
	unmatched.Repository.FullName = "other/repo"
	if matched, inserted, err := poller.ingestNotification(t.Context(), unmatched); err != nil || matched != 0 ||
		inserted != 0 {
		t.Fatalf("unmatched notification = %d/%d/%v", matched, inserted, err)
	}
	poller.tools = toolRunnerFunc(func(context.Context, workflows.ToolRequest) (map[string]any, error) {
		return map[string]any{"text": `{}`}, nil
	})
	if _, _, err := poller.ingestNotification(t.Context(), validResidualNotification()); err == nil {
		t.Fatal("incomplete pull request enrichment accepted")
	}
	issue := validResidualNotification()
	issue.Subject.Type = "Issue"
	issue.Subject.URL = "https://api.github.test/repos/Owner/Repo/issues/7"
	poller.store = residualInserterFunc(func(context.Context, eventing.Envelope) (eventing.InsertResult, error) {
		return eventing.InsertResult{}, errors.New("insert failed")
	})
	if _, _, err := poller.ingestNotification(t.Context(), issue); err == nil {
		t.Fatal("insert failure lost")
	}
}

func TestGitHubPollProjectionFallbackResiduals(t *testing.T) {
	repository := notificationRepository{
		FullName: "Owner/Repo", HTMLURL: "https://github.test/Owner/Repo",
	}
	payload := webhookRepositoryPayload(repository)
	owner := payload["owner"].(map[string]any)
	if owner["login"] != "Owner" {
		t.Fatalf("repository owner fallback = %#v", owner)
	}
	pr := pullRequest{
		Number: 1, User: &pullRequestUser{Login: "user"},
		Head: &pullRequestBranch{Ref: "feature", SHA: strings.Repeat("b", 40)},
		Base: &pullRequestBranch{
			Ref: "main", SHA: strings.Repeat("a", 40), Repo: &pullRequestBranchRepo{FullName: "Owner/Repo"},
		},
	}
	projected := webhookPullRequestPayload(pr, "Owner/Repo")
	head := projected["head"].(map[string]any)
	headRepo := head["repo"].(map[string]any)
	if headRepo["full_name"] != "Owner/Repo" {
		t.Fatalf("head repository fallback = %#v", headRepo)
	}
	if normalizedTypePart("///") != "other" {
		t.Fatal("empty normalized event type did not fall back")
	}
	poller := &Poller{now: time.Now}
	item := validResidualNotification()
	item.Subject.Type = "Release"
	item.Reason = "subscribed"
	envelope, err := poller.envelope(item, nil, connector{name: "main"})
	if err != nil || envelope.Attributes["targets_user"] != "false" || envelope.Actor != nil {
		t.Fatalf("untargeted envelope = %#v, %v", envelope, err)
	}
}
