package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/eventing"
	eventgithubpoll "github.com/sipeed/picoclaw/pkg/eventing/githubpoll"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestEventAutomationRunsOnePollOnlyGitHubIngressLoop(t *testing.T) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		workspace+"/events.db",
		true,
		false,
	)
	cfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{
		"github-poll": {
			Enabled:           true,
			Format:            config.EventWebhookFormatGitHub,
			Repositories:      []string{"scylladb/picoclaw"},
			TargetUser:        "reviewer",
			PollNotifications: true,
		},
	}
	runner := &gatewayNotificationPollRunner{}
	service, err := newEventAutomationServiceWithRuntime(
		context.Background(),
		cfg,
		nil,
		nil,
		nil,
		eventReviewRuntime{notificationMCP: runner},
	)
	if err != nil {
		t.Fatalf("newEventAutomationServiceWithRuntime() error = %v", err)
	}
	if service == nil || service.githubPoller == nil {
		t.Fatal("GitHub notification poller was not configured")
	}
	if service.webhookBackend != nil {
		t.Fatal("poll-only connector unexpectedly registered a public webhook route")
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if closeErr := service.Close(closeCtx); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	})

	var stored eventing.StoredEvent
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		page, listErr := service.store.List(context.Background(), eventing.EventFilter{
			Source:    "github",
			Connector: "github-poll",
			Limit:     10,
		})
		if listErr != nil {
			t.Fatalf("List() error = %v", listErr)
		}
		if len(page.Events) > 0 {
			stored = page.Events[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stored.Envelope.ID == "" {
		t.Fatal("poll loop did not store a GitHub notification")
	}
	if stored.Envelope.Type != "issues.mention" ||
		stored.Envelope.Attributes["source_authenticated"] != "true" ||
		stored.Envelope.Attributes["provider_authenticated"] != "true" ||
		stored.Envelope.Attributes["targets_user"] != "true" {
		t.Fatalf("stored notification = %#v", stored.Envelope)
	}
	if calls := runner.snapshot(); len(calls) != 1 {
		t.Fatalf("MCP calls before first 60-second tick = %d, want 1", len(calls))
	} else if calls[0].MCPTool != eventgithubpoll.ListNotificationsTool ||
		calls[0].Args["filter"] != "include_read_notifications" ||
		calls[0].Args["perPage"] != 50 {
		t.Fatalf("list notification call = %#v", calls[0])
	}
}

func TestEventAutomationRuntimeRequiresAgentMCPForNotificationPolling(
	t *testing.T,
) {
	cfg := eventAutomationTestConfig(t.TempDir(), "", true, false)
	cfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{
		"github": {
			Enabled:           true,
			Format:            config.EventWebhookFormatGitHub,
			PollNotifications: true,
		},
	}
	err := validateEventAutomationRuntime(context.Background(), cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "agent MCP runtime") {
		t.Fatalf(
			"validateEventAutomationRuntime() error = %v, want MCP requirement",
			err,
		)
	}
}

func TestGitHubNotificationPollWorkerWaitsAfterSlowScan(t *testing.T) {
	const interval = 100 * time.Millisecond
	runner := &gatewayNotificationCadenceRunner{
		secondStarted: make(chan struct{}),
		releaseSecond: make(chan struct{}),
		thirdStarted:  make(chan struct{}),
	}
	poller, err := eventgithubpoll.New(eventgithubpoll.Config{
		Store:      gatewayDiscardNotificationInserter{},
		ToolRunner: runner,
		Connectors: []eventgithubpoll.Connector{{Name: "github"}},
	})
	if err != nil {
		t.Fatalf("githubpoll.New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	workers.Add(1)
	go runGitHubNotificationPollWorker(ctx, &workers, poller, interval)
	t.Cleanup(func() {
		cancel()
		workers.Wait()
	})

	select {
	case <-runner.secondStarted:
	case <-time.After(5 * interval):
		t.Fatal("second scheduled poll did not start")
	}
	// Keep the second scan running past two cadence boundaries. A ticker-based
	// loop would queue one of those ticks and start the third scan immediately.
	time.Sleep(2 * interval)
	close(runner.releaseSecond)
	select {
	case <-runner.thirdStarted:
		t.Fatal("slow scan caused an immediate catch-up poll")
	case <-time.After(interval / 2):
	}
	select {
	case <-runner.thirdStarted:
	case <-time.After(2 * interval):
		t.Fatal("third poll did not start after the post-scan interval")
	}
}

type gatewayNotificationPollRunner struct {
	mu    sync.Mutex
	calls []workflows.ToolRequest
}

func (r *gatewayNotificationPollRunner) RunTool(
	_ context.Context,
	request workflows.ToolRequest,
) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, request)
	notification := []map[string]any{{
		"id":         "gateway-notification-1",
		"reason":     "mention",
		"unread":     true,
		"updated_at": "2026-07-30T12:00:00Z",
		"url":        "https://api.github.com/notifications/threads/1",
		"repository": map[string]any{
			"id":             123,
			"node_id":        "R_123",
			"name":           "PicoClaw",
			"full_name":      "ScyllaDB/PicoClaw",
			"private":        false,
			"html_url":       "https://github.com/ScyllaDB/PicoClaw",
			"url":            "https://api.github.com/repos/ScyllaDB/PicoClaw",
			"default_branch": "main",
			"owner":          map[string]any{"login": "ScyllaDB"},
		},
		"subject": map[string]any{
			"title": "Mentioned in an issue",
			"url":   "https://api.github.com/repos/ScyllaDB/PicoClaw/issues/9",
			"type":  "Issue",
		},
	}}
	data, _ := json.Marshal(notification)
	return map[string]any{"text": string(data)}, nil
}

func (r *gatewayNotificationPollRunner) snapshot() []workflows.ToolRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]workflows.ToolRequest(nil), r.calls...)
}

type gatewayNotificationCadenceRunner struct {
	mu            sync.Mutex
	calls         int
	secondStarted chan struct{}
	releaseSecond chan struct{}
	thirdStarted  chan struct{}
}

func (r *gatewayNotificationCadenceRunner) RunTool(
	ctx context.Context,
	_ workflows.ToolRequest,
) (map[string]any, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	switch call {
	case 2:
		close(r.secondStarted)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.releaseSecond:
		}
	case 3:
		close(r.thirdStarted)
	}
	return map[string]any{"text": "[]"}, nil
}

type gatewayDiscardNotificationInserter struct{}

func (gatewayDiscardNotificationInserter) Insert(
	context.Context,
	eventing.Envelope,
) (eventing.InsertResult, error) {
	return eventing.InsertResult{}, nil
}
