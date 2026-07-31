package githubpoll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestPollIngestsTargetedReviewRequestAndDeduplicatesRepoll(t *testing.T) {
	store := newMemoryInserter()
	runner := &pollToolRunner{
		notifications: [][]map[string]any{{
			testNotification(
				"101",
				"review_requested",
				"PullRequest",
				"ScyllaDB/PicoClaw",
				"2026-07-30T12:00:00Z",
				42,
			),
		}},
		pullRequests: map[string]map[string]any{
			"scylladb/picoclaw#42": testPullRequest(42),
		},
	}
	poller, err := New(Config{
		Store:      store,
		ToolRunner: runner,
		Connectors: []Connector{{
			Name:         "github-main",
			Repositories: []string{"scylladb/picoclaw"},
			TargetUser:   "PicoBot",
		}},
		Now: func() time.Time {
			return time.Date(2026, 7, 30, 12, 1, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	first, err := poller.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll(first) error = %v", err)
	}
	if first.Inserted != 1 || first.Matched != 1 {
		t.Fatalf("Poll(first) = %#v", first)
	}
	second, err := poller.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll(second) error = %v", err)
	}
	if second.Inserted != 0 || second.Matched != 1 {
		t.Fatalf("Poll(second) = %#v", second)
	}

	events := store.snapshot()
	if len(events) != 1 {
		t.Fatalf("stored events = %d, want 1", len(events))
	}
	envelope := events[0]
	if envelope.Source != "github" ||
		envelope.Connector != "github-main" ||
		envelope.Type != "pull_request.review_requested" {
		t.Fatalf("event identity = %#v", envelope)
	}
	for key, want := range map[string]string{
		"body_authenticated":     "false",
		"provider_authenticated": "true",
		"source_authenticated":   "true",
		"targets_user":           "true",
		"target_reason":          "requested_reviewer",
		"target_user":            "PicoBot",
		"repository_full_name":   "ScyllaDB/PicoClaw",
		"pull_request_number":    "42",
		"pull_request_head_sha":  strings.Repeat("b", 40),
		"pull_request_base_sha":  strings.Repeat("a", 40),
		"pull_request_author":    "octocat",
	} {
		if got := envelope.Attributes[key]; got != want {
			t.Fatalf("attribute %q = %q, want %q", key, got, want)
		}
	}
	var payload map[string]any
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	pullRequest := payload["pull_request"].(map[string]any)
	head := pullRequest["head"].(map[string]any)
	headRepository := head["repo"].(map[string]any)
	if got := headRepository["clone_url"]; got != "https://github.com/contributor/picoclaw.git" {
		t.Fatalf("head clone_url = %#v", got)
	}
	base := pullRequest["base"].(map[string]any)
	baseRepository := base["repo"].(map[string]any)
	if got := baseRepository["full_name"]; got != "scylladb/picoclaw" {
		t.Fatalf("base full_name = %#v", got)
	}
	if got := baseRepository["clone_url"]; got != "https://github.com/scylladb/picoclaw.git" {
		t.Fatalf("base clone_url = %#v", got)
	}

	requests := runner.snapshot()
	if len(requests) != 4 {
		t.Fatalf("tool requests = %d, want 4", len(requests))
	}
	for _, request := range []workflows.ToolRequest{requests[0], requests[2]} {
		if request.MCPServer != DefaultMCPServer ||
			request.MCPTool != ListNotificationsTool ||
			request.Args["filter"] != "include_read_notifications" ||
			request.Args["page"] != 1 ||
			request.Args["perPage"] != notificationsPerPage {
			t.Fatalf("list request = %#v", request)
		}
	}
	if request := requests[1]; request.MCPTool != PullRequestReadTool ||
		request.Args["method"] != "get" ||
		request.Args["owner"] != "ScyllaDB" ||
		request.Args["repo"] != "PicoClaw" ||
		request.Args["pullNumber"] != 42 {
		t.Fatalf("pull request read = %#v", request)
	}
}

func TestPollUpdatedNotificationCreatesNewDurableEvent(t *testing.T) {
	store := newMemoryInserter()
	first := testNotification(
		"202",
		"mention",
		"Issue",
		"scylladb/picoclaw",
		"2026-07-30T12:00:00Z",
		8,
	)
	runner := &pollToolRunner{notifications: [][]map[string]any{{first}}}
	poller, err := New(Config{
		Store:      store,
		ToolRunner: runner,
		Connectors: []Connector{{Name: "github"}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := poller.Poll(context.Background()); err != nil {
		t.Fatalf("Poll(first) error = %v", err)
	}
	updated := testNotification(
		"202",
		"mention",
		"Issue",
		"scylladb/picoclaw",
		"2026-07-30T12:05:00Z",
		8,
	)
	runner.setNotifications([][]map[string]any{{updated}})
	if _, err := poller.Poll(context.Background()); err != nil {
		t.Fatalf("Poll(updated) error = %v", err)
	}
	events := store.snapshot()
	if len(events) != 2 {
		t.Fatalf("stored events = %d, want 2", len(events))
	}
	if events[0].DedupeKey == events[1].DedupeKey {
		t.Fatalf("updated notification reused dedupe key %q", events[0].DedupeKey)
	}
	if events[1].Type != "issues.mention" {
		t.Fatalf("updated event type = %q", events[1].Type)
	}
}

func TestPollAppliesCaseInsensitiveConnectorRepositoryAllowlist(t *testing.T) {
	store := newMemoryInserter()
	runner := &pollToolRunner{
		notifications: [][]map[string]any{{
			testNotification(
				"301",
				"mention",
				"Issue",
				"SCYLLADB/ALLOWED",
				"2026-07-30T12:00:00Z",
				1,
			),
			testNotification(
				"302",
				"mention",
				"Issue",
				"scylladb/ignored",
				"2026-07-30T12:00:00Z",
				2,
			),
		}},
	}
	poller, err := New(Config{
		Store:      store,
		ToolRunner: runner,
		Connectors: []Connector{
			{Name: "scoped", Repositories: []string{"scylladb/allowed"}},
			{Name: "all"},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := poller.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if result.Matched != 3 || result.Inserted != 3 {
		t.Fatalf("Poll() = %#v", result)
	}
	events := store.snapshot()
	if len(events) != 3 {
		t.Fatalf("stored events = %d, want 3", len(events))
	}
}

func TestPollReviewRequestEnrichmentFailureDoesNotInsert(t *testing.T) {
	store := newMemoryInserter()
	runner := &pollToolRunner{
		notifications: [][]map[string]any{{
			testNotification(
				"401",
				"review_requested",
				"PullRequest",
				"scylladb/picoclaw",
				"2026-07-30T12:00:00Z",
				42,
			),
		}},
		pullRequests: map[string]map[string]any{
			"scylladb/picoclaw#42": {
				"number":   42,
				"html_url": "https://github.com/scylladb/picoclaw/pull/42",
			},
		},
	}
	poller, err := New(Config{
		Store:      store,
		ToolRunner: runner,
		Connectors: []Connector{{Name: "github"}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := poller.Poll(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "enrichment is incomplete") {
		t.Fatalf("Poll() error = %v, want incomplete enrichment", err)
	}
	if events := store.snapshot(); len(events) != 0 {
		t.Fatalf("stored events = %d, want 0", len(events))
	}
}

func TestPollRejectsNonTextOrTrailingMCPJSON(t *testing.T) {
	for _, test := range []struct {
		name    string
		outputs map[string]any
	}{
		{name: "missing text", outputs: map[string]any{"json": []any{}}},
		{name: "trailing JSON", outputs: map[string]any{"text": `[] {}`}},
		{
			name: "model-facing artifact notice without artifact",
			outputs: map[string]any{
				"text":          "[MCP returned a large text result; saved as a local artifact.]",
				"artifact_tags": []string{},
			},
		},
		{
			name: "invalid artifact tag type",
			outputs: map[string]any{
				"text":          "[MCP returned a large text result; saved as a local artifact.]",
				"artifact_tags": []any{"[file:/tmp/untrusted]"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			poller, err := New(Config{
				Store: newMemoryInserter(),
				ToolRunner: toolRunnerFunc(func(
					context.Context,
					workflows.ToolRequest,
				) (map[string]any, error) {
					return test.outputs, nil
				}),
				Connectors: []Connector{{Name: "github"}},
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if _, err := poller.Poll(context.Background()); err == nil {
				t.Fatal("Poll() error = nil, want exact JSON rejection")
			}
		})
	}
}

func TestPollRejectsProviderPageLargerThanRequestedBound(t *testing.T) {
	notifications := make([]map[string]any, notificationsPerPage+1)
	for index := range notifications {
		notifications[index] = testNotification(
			fmt.Sprintf("oversized-%d", index),
			"mention",
			"Issue",
			"scylladb/picoclaw",
			"2026-07-30T12:00:00Z",
			index+1,
		)
	}
	store := newMemoryInserter()
	poller, err := New(Config{
		Store: store,
		ToolRunner: &pollToolRunner{
			notifications: [][]map[string]any{notifications},
		},
		Connectors: []Connector{{Name: "github"}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := poller.Poll(context.Background()); err == nil ||
		!strings.Contains(
			err.Error(),
			fmt.Sprintf("maximum is %d", notificationsPerPage),
		) {
		t.Fatalf("Poll() error = %v, want bounded-page error", err)
	}
	if events := store.snapshot(); len(events) != 0 {
		t.Fatalf("stored events = %d, want 0", len(events))
	}
}

func TestPollConsumesExactLargeTextArtifactWithinConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "github_list_notifications_1.txt")
	if err := os.WriteFile(path, []byte("[]"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	poller, err := New(Config{
		Store: newMemoryInserter(),
		ToolRunner: toolRunnerFunc(func(
			context.Context,
			workflows.ToolRequest,
		) (map[string]any, error) {
			return map[string]any{
				"text": "[MCP returned a large text result (2 chars); omitted from model context and saved as a local artifact.]",
				"artifact_tags": []string{
					"[file:" + path + "]",
				},
			}, nil
		}),
		Connectors:   []Connector{{Name: "github"}},
		ArtifactRoot: root,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := poller.Poll(context.Background()); err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed artifact still exists or stat failed: %v", err)
	}
}

func TestPollRejectsSymlinkedExactTextArtifactWithoutDeletingTarget(t *testing.T) {
	root := t.TempDir()
	targetRoot := t.TempDir()
	target := filepath.Join(targetRoot, "notifications.json")
	if err := os.WriteFile(target, []byte("[]"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	link := filepath.Join(root, "github_list_notifications_link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	poller, err := New(Config{
		Store: newMemoryInserter(),
		ToolRunner: toolRunnerFunc(func(
			context.Context,
			workflows.ToolRequest,
		) (map[string]any, error) {
			return map[string]any{
				"text": "[MCP returned a large text result; saved as a local artifact.]",
				"artifact_tags": []string{
					"[file:" + link + "]",
				},
			}, nil
		}),
		Connectors:   []Connector{{Name: "github"}},
		ArtifactRoot: root,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := poller.Poll(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Poll() error = %v, want symlink rejection", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "[]" {
		t.Fatalf("outside target changed or unreadable: data=%q error=%v", data, err)
	}
}

func TestPollContinuesAfterProviderMaximumSizedPage(t *testing.T) {
	firstPage := make([]map[string]any, notificationsPerPage)
	for index := range firstPage {
		firstPage[index] = testNotification(
			fmt.Sprintf("page-one-%d", index),
			"mention",
			"Issue",
			"scylladb/picoclaw",
			"2026-07-30T12:00:00Z",
			index+1,
		)
	}
	secondPage := []map[string]any{
		testNotification(
			"page-two",
			"mention",
			"Issue",
			"scylladb/picoclaw",
			"2026-07-30T12:01:00Z",
			notificationsPerPage+1,
		),
	}
	store := newMemoryInserter()
	runner := &pollToolRunner{
		notifications: [][]map[string]any{firstPage, secondPage},
	}
	poller, err := New(Config{
		Store:      store,
		ToolRunner: runner,
		Connectors: []Connector{{Name: "github"}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := poller.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	want := notificationsPerPage + 1
	if result.Notifications != want ||
		result.Matched != want ||
		result.Inserted != want ||
		len(store.snapshot()) != want {
		t.Fatalf("Poll() = %#v, stored = %d, want %d", result, len(store.snapshot()), want)
	}
	requests := runner.snapshot()
	if len(requests) != 2 ||
		requests[0].Args["page"] != 1 ||
		requests[1].Args["page"] != 2 ||
		requests[0].Args["perPage"] != notificationsPerPage ||
		requests[1].Args["perPage"] != notificationsPerPage {
		t.Fatalf("notification requests = %#v, want provider-max pages 1 and 2", requests)
	}
}

func TestNotificationEventTypesAreStableAndBounded(t *testing.T) {
	for _, test := range []struct {
		subject string
		reason  string
		want    string
	}{
		{subject: "PullRequest", reason: "review_requested", want: "pull_request.review_requested"},
		{subject: "PullRequest", reason: "mention", want: "pull_request.mention"},
		{subject: "PullRequest", reason: "assign", want: "pull_request.assign"},
		{subject: "Issue", reason: "mention", want: "issues.mention"},
		{subject: "Issue", reason: "assign", want: "issues.assign"},
		{subject: "Release", reason: "subscribed", want: "notification.subscribed"},
	} {
		if got := notificationEventType(test.subject, test.reason); got != test.want {
			t.Fatalf(
				"notificationEventType(%q, %q) = %q, want %q",
				test.subject,
				test.reason,
				got,
				test.want,
			)
		}
	}
	if got := notificationEventType(
		"Other",
		strings.Repeat("unsafe/", 30),
	); len(got) > len("notification.")+maxNotificationReason {
		t.Fatalf("bounded notification type length = %d: %q", len(got), got)
	}
}

func TestPullRequestEnrichmentRequiresMatchingBaseRepository(t *testing.T) {
	valid := pullRequest{
		Number:  42,
		HTMLURL: "https://github.com/scylladb/picoclaw/pull/42",
		User:    &pullRequestUser{Login: "octocat"},
		Head: &pullRequestBranch{
			SHA:  strings.Repeat("b", 40),
			Repo: &pullRequestBranchRepo{FullName: "contributor/picoclaw"},
		},
		Base: &pullRequestBranch{
			SHA:  strings.Repeat("a", 40),
			Repo: &pullRequestBranchRepo{FullName: "scylladb/picoclaw"},
		},
	}
	if err := validatePullRequestEnrichment(
		valid,
		"ScyllaDB/PicoClaw",
		42,
	); err != nil {
		t.Fatalf("valid fork enrichment rejected: %v", err)
	}

	for _, test := range []struct {
		name     string
		fullName string
		nilRepo  bool
	}{
		{name: "missing", nilRepo: true},
		{name: "invalid", fullName: "not-a-repository"},
		{name: "mismatch", fullName: "other/repository"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			base := *valid.Base
			candidate.Base = &base
			if test.nilRepo {
				candidate.Base.Repo = nil
			} else {
				candidate.Base.Repo = &pullRequestBranchRepo{
					FullName: test.fullName,
				}
			}
			if err := validatePullRequestEnrichment(
				candidate,
				"scylladb/picoclaw",
				42,
			); err == nil ||
				!strings.Contains(err.Error(), "base repository") {
				t.Fatalf("validation error = %v, want base repository rejection", err)
			}
		})
	}
}

type memoryInserter struct {
	mu       sync.Mutex
	byDedupe map[string]eventing.Envelope
	order    []eventing.Envelope
}

func newMemoryInserter() *memoryInserter {
	return &memoryInserter{byDedupe: make(map[string]eventing.Envelope)}
}

func (s *memoryInserter) Insert(
	_ context.Context,
	input eventing.Envelope,
) (eventing.InsertResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := input.Source + "\x00" + input.Connector + "\x00" + input.DedupeKey
	if existing, ok := s.byDedupe[key]; ok {
		return eventing.InsertResult{
			Event: eventing.StoredEvent{Envelope: existing},
		}, nil
	}
	normalized, err := eventing.NormalizeEnvelope(input, time.Now())
	if err != nil {
		return eventing.InsertResult{}, err
	}
	s.byDedupe[key] = normalized
	s.order = append(s.order, normalized)
	return eventing.InsertResult{
		Event:    eventing.StoredEvent{Envelope: normalized},
		Inserted: true,
	}, nil
}

func (s *memoryInserter) snapshot() []eventing.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]eventing.Envelope(nil), s.order...)
}

type pollToolRunner struct {
	mu            sync.Mutex
	notifications [][]map[string]any
	pullRequests  map[string]map[string]any
	requests      []workflows.ToolRequest
}

func (r *pollToolRunner) RunTool(
	_ context.Context,
	request workflows.ToolRequest,
) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, request)
	switch request.MCPTool {
	case ListNotificationsTool:
		page, _ := request.Args["page"].(int)
		var values []map[string]any
		if page > 0 && page <= len(r.notifications) {
			values = r.notifications[page-1]
		}
		if values == nil {
			values = []map[string]any{}
		}
		data, _ := json.Marshal(values)
		return map[string]any{"text": string(data)}, nil
	case PullRequestReadTool:
		key := strings.ToLower(fmt.Sprintf(
			"%s/%s#%v",
			request.Args["owner"],
			request.Args["repo"],
			request.Args["pullNumber"],
		))
		value, exists := r.pullRequests[key]
		if !exists {
			return nil, errors.New("pull request not found")
		}
		data, _ := json.Marshal(value)
		return map[string]any{"text": string(data)}, nil
	default:
		return nil, fmt.Errorf("unexpected tool %q", request.MCPTool)
	}
}

func (r *pollToolRunner) snapshot() []workflows.ToolRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]workflows.ToolRequest(nil), r.requests...)
}

func (r *pollToolRunner) setNotifications(values [][]map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notifications = values
}

type toolRunnerFunc func(
	context.Context,
	workflows.ToolRequest,
) (map[string]any, error)

func (f toolRunnerFunc) RunTool(
	ctx context.Context,
	request workflows.ToolRequest,
) (map[string]any, error) {
	return f(ctx, request)
}

func testNotification(
	id, reason, subjectType, repository, updatedAt string,
	number int,
) map[string]any {
	owner, repo, _ := strings.Cut(repository, "/")
	collection := "issues"
	if subjectType == "PullRequest" {
		collection = "pulls"
	}
	return map[string]any{
		"id":         id,
		"reason":     reason,
		"unread":     true,
		"updated_at": updatedAt,
		"url":        "https://api.github.com/notifications/threads/" + id,
		"repository": map[string]any{
			"id":             123,
			"node_id":        "R_123",
			"name":           repo,
			"full_name":      repository,
			"private":        false,
			"html_url":       "https://github.com/" + repository,
			"url":            "https://api.github.com/repos/" + repository,
			"default_branch": "main",
			"owner": map[string]any{
				"login": owner,
			},
		},
		"subject": map[string]any{
			"title": "Provider content",
			"url": fmt.Sprintf(
				"https://api.github.com/repos/%s/%s/%d",
				repository,
				collection,
				number,
			),
			"type": subjectType,
		},
	}
}

func testPullRequest(number int) map[string]any {
	return map[string]any{
		"number":   number,
		"title":    "Fix the retry path",
		"body":     "Untrusted pull request body",
		"draft":    false,
		"html_url": fmt.Sprintf("https://github.com/scylladb/picoclaw/pull/%d", number),
		"user": map[string]any{
			"login": "octocat",
		},
		"head": map[string]any{
			"ref": "fix/retry",
			"sha": strings.Repeat("b", 40),
			"repo": map[string]any{
				"full_name": "contributor/picoclaw",
			},
		},
		"base": map[string]any{
			"ref": "main",
			"sha": strings.Repeat("a", 40),
			"repo": map[string]any{
				"full_name": "scylladb/picoclaw",
			},
		},
	}
}
