//go:build !mipsle && !netbsd && !(freebsd && arm)

package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	eventwebhook "github.com/sipeed/picoclaw/pkg/eventing/webhook"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestGitHubOwnPRFeedbackWorkflowCapturesProviderVerifiedDevelopmentCase(
	t *testing.T,
) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		workspace+"/eventing/events.db",
		true,
		true,
	)
	secret := gatewayGitHubWebhookSecret('d')
	configureGatewayGitHubWebhook(cfg, secret)
	webhook := cfg.Events.Ingress.Webhooks[gatewayGitHubConnector]
	webhook.Repositories = []string{"acme/project"}
	webhook.TargetUser = "review-user"
	cfg.Events.Ingress.Webhooks[gatewayGitHubConnector] = webhook
	if _, err := workflows.InstallGitHubPRDevelopmentWorkflow(
		context.Background(),
		workspace,
		false,
	); err != nil {
		t.Fatalf("InstallGitHubPRDevelopmentWorkflow() error = %v", err)
	}

	runStore := workflows.NewFileRunStore(workspace)
	reviewBody := "Please fix\x00the race."
	tools := &gatewayPRDevelopmentToolRunner{reviewBody: reviewBody}
	executor := &workflows.Executor{
		WorkspaceDir:   workspace,
		DefinitionsDir: cfg.Workflows.EffectiveDefinitionsDir(),
		Store:          runStore,
		Tools:          tools,
		DefaultTimeout: 2 * time.Second,
	}
	service, err := newEventAutomationService(
		context.Background(),
		cfg,
		executor,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("newEventAutomationService() error = %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := service.Close(closeCtx); closeErr != nil {
			t.Errorf("event automation Close() error = %v", closeErr)
		}
	})

	controller := eventwebhook.NewController()
	generation, err := controller.Activate(service.webhookBackend)
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	t.Cleanup(func() {
		drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if drainErr := controller.Deactivate(drainCtx, generation); drainErr != nil {
			t.Errorf("Deactivate() error = %v", drainErr)
		}
	})
	server := httptest.NewServer(controller)
	t.Cleanup(server.Close)

	body := gatewayOwnPRFeedbackBody(t)
	status, responseBody := performGatewayWebhookRequest(
		t,
		server.Client(),
		gatewayGitHubSignedRequest(
			t,
			server.URL+eventwebhook.RoutePrefix+gatewayGitHubConnector,
			secret,
			"own-pr-development-capture",
			"pull_request_review",
			body,
		),
	)
	if status != http.StatusAccepted {
		t.Fatalf("webhook status = %d, want accepted: %s", status, responseBody)
	}
	var accepted struct {
		EventID  string `json:"event_id"`
		Inserted bool   `json:"inserted"`
	}
	if decodeErr := json.Unmarshal(responseBody, &accepted); decodeErr != nil {
		t.Fatalf("Unmarshal(response) error = %v", decodeErr)
	}
	if accepted.EventID == "" || !accepted.Inserted {
		t.Fatalf("accepted response = %#v", accepted)
	}

	dispatch, run := waitForGatewayWebhookWorkflow(
		t,
		service.store,
		runStore,
		accepted.EventID,
	)
	identity := eventing.PRDevelopmentCaptureIdentity{
		EventID:          accepted.EventID,
		DispatchID:       dispatch.ID,
		RunID:            run.ID,
		WorkflowRef:      dispatch.WorkflowRef,
		WorkflowRevision: dispatch.WorkflowRevision,
		Connector:        gatewayGitHubConnector,
	}
	developmentCase, exists, err := service.store.LookupPRDevelopmentCapture(
		context.Background(),
		identity,
	)
	if err != nil || !exists {
		t.Fatalf("LookupPRDevelopmentCapture() = %#v, %v, %v", developmentCase, exists, err)
	}
	if developmentCase.Repository != "acme/project" ||
		developmentCase.PullNumber != 42 ||
		developmentCase.PullAuthor != "review-user" ||
		developmentCase.TargetUser != "review-user" ||
		developmentCase.HeadRepository != "review-user/project-fork" ||
		developmentCase.HeadSHA != strings.Repeat("3", 40) ||
		developmentCase.ReviewID != "501" ||
		developmentCase.SubmittedReviewState != "changes_requested" ||
		developmentCase.CurrentReviewState != "changes_requested" ||
		developmentCase.Feedback != reviewBody {
		t.Fatalf("development case = %#v", developmentCase)
	}
	calls := tools.snapshot()
	if len(calls) != 3 {
		t.Fatalf("GitHub MCP calls = %#v, want template get plus sink get/get_reviews", calls)
	}
	getCalls := 0
	reviewCalls := 0
	for _, call := range calls {
		if !call.MCP || call.MCPServer != "github" ||
			call.MCPTool != "pull_request_read" {
			t.Fatalf("unexpected provider capability = %#v", call)
		}
		switch call.Args["method"] {
		case "get":
			getCalls++
		case "get_reviews":
			reviewCalls++
		default:
			t.Fatalf("unexpected provider method = %#v", call.Args)
		}
	}
	if getCalls != 2 || reviewCalls != 1 {
		t.Fatalf("provider call counts: get=%d reviews=%d", getCalls, reviewCalls)
	}
}

type gatewayPRDevelopmentToolRunner struct {
	mu         sync.Mutex
	requests   []workflows.ToolRequest
	reviewBody string
}

func (r *gatewayPRDevelopmentToolRunner) RunTool(
	_ context.Context,
	request workflows.ToolRequest,
) (map[string]any, error) {
	r.mu.Lock()
	r.requests = append(r.requests, request)
	r.mu.Unlock()
	if request.MCPServer != "github" || request.MCPTool != "pull_request_read" {
		return nil, errors.New("unexpected provider capability")
	}
	var value any
	switch request.Args["method"] {
	case "get":
		value = map[string]any{
			"number":   42,
			"state":    "open",
			"draft":    false,
			"merged":   false,
			"html_url": "https://github.com/acme/project/pull/42",
			"user":     map[string]any{"login": "review-user"},
			"head": map[string]any{
				"ref":  "fix/race",
				"sha":  strings.Repeat("3", 40),
				"repo": map[string]any{"full_name": "review-user/project-fork"},
			},
			"base": map[string]any{
				"ref":  "main",
				"sha":  strings.Repeat("1", 40),
				"repo": map[string]any{"full_name": "acme/project"},
			},
		}
	case "get_reviews":
		value = []any{map[string]any{
			"id":           501,
			"state":        "CHANGES_REQUESTED",
			"body":         r.reviewBody,
			"html_url":     "https://github.com/acme/project/pull/42#pullrequestreview-501",
			"user":         map[string]any{"login": "maintainer"},
			"commit_id":    strings.Repeat("a", 40),
			"submitted_at": "2026-08-05T12:34:56Z",
		}}
	default:
		return nil, errors.New("unexpected pull_request_read method")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"text":          string(raw),
		"artifact_tags": []string{},
	}, nil
}

func (r *gatewayPRDevelopmentToolRunner) snapshot() []workflows.ToolRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]workflows.ToolRequest(nil), r.requests...)
}

func gatewayOwnPRFeedbackBody(t *testing.T) string {
	t.Helper()
	value := map[string]any{
		"action": "submitted",
		"pull_request": map[string]any{
			"number":   42,
			"html_url": "https://github.com/acme/project/pull/42",
			"title":    "Fix the race",
			"body":     "Untrusted pull request text",
			"draft":    false,
			"user":     map[string]any{"login": "review-user"},
			"head": map[string]any{
				"ref":  "fix/race",
				"sha":  strings.Repeat("2", 40),
				"repo": map[string]any{"full_name": "review-user/project-fork"},
			},
			"base": map[string]any{
				"ref":  "main",
				"sha":  strings.Repeat("1", 40),
				"repo": map[string]any{"full_name": "acme/project"},
			},
		},
		"review": map[string]any{
			"id":           501,
			"node_id":      "PRR_node_501",
			"html_url":     "https://github.com/acme/project/pull/42#pullrequestreview-501",
			"body":         "Please fix the race.",
			"user":         map[string]any{"login": "maintainer"},
			"state":        "changes_requested",
			"commit_id":    strings.Repeat("a", 40),
			"submitted_at": "2026-08-05T12:34:56Z",
		},
		"repository": map[string]any{
			"id":             901,
			"node_id":        "R_repo",
			"name":           "project",
			"full_name":      "acme/project",
			"html_url":       "https://github.com/acme/project",
			"owner":          map[string]any{"login": "acme"},
			"default_branch": "main",
			"visibility":     "public",
			"private":        false,
			"fork":           false,
		},
		"sender": map[string]any{
			"id":       7,
			"node_id":  "U_sender",
			"login":    "maintainer",
			"type":     "User",
			"html_url": "https://github.com/maintainer",
		},
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
