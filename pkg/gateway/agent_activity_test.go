package gateway

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/channels"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/health"
)

func TestAgentActivityRuntimeHandlerUsesLiveAgentAndSafeProjection(t *testing.T) {
	loop, messageBus := newGatewayWorkflowAuthoringTestLoop(t)
	defer loop.Close()
	defer messageBus.Close()
	handler := newAgentActivityHandler(loop)

	loop.RuntimeEventBus().PublishNonBlocking(runtimeevents.Event{
		Kind:   runtimeevents.KindAgentToolExecStart,
		Source: runtimeevents.Source{Component: "agent", Name: "main"},
		Scope: runtimeevents.Scope{
			AgentID:    "main",
			SessionKey: "private-session",
			ChatID:     "private-chat",
		},
		Attrs: map[string]any{"private": "attribute"},
		Payload: agent.ToolExecStartPayload{
			Tool:      "exec_command",
			Arguments: map[string]any{"token": "private-argument"},
		},
	})

	var recorder *httptest.ResponseRecorder
	deadline := time.Now().Add(2 * time.Second)
	for {
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(
			recorder,
			httptest.NewRequest(
				http.MethodGet,
				agent.RuntimeAgentActivityRoutePrefix+"main/activity?limit=10",
				nil,
			),
		)
		if recorder.Code == http.StatusOK {
			page, err := agent.DecodeAgentActivityPage(recorder.Body.Bytes())
			if err == nil && len(page.Events) == 1 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"activity did not appear: status=%d body=%s",
				recorder.Code,
				recorder.Body.String(),
			)
		}
		time.Sleep(time.Millisecond)
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("private")) ||
		bytes.Contains(recorder.Body.Bytes(), []byte("argument")) {
		t.Fatalf("runtime response leaked private fields: %s", recorder.Body.Bytes())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" ||
		recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %#v", recorder.Header())
	}
}

func TestAgentActivityRuntimeHandlerRejectsNonExactAndNonLiveRequests(t *testing.T) {
	loop, messageBus := newGatewayWorkflowAuthoringTestLoop(t)
	defer loop.Close()
	defer messageBus.Close()
	handler := newAgentActivityHandler(loop)

	tests := []struct {
		name   string
		method string
		target string
		status int
		body   string
	}{
		{
			name:   "method",
			method: http.MethodPost,
			target: agent.RuntimeAgentActivityRoutePrefix + "main/activity",
			status: http.StatusMethodNotAllowed,
			body:   "{\"error\":\"method_not_allowed\"}\n",
		},
		{
			name:   "unknown agent",
			method: http.MethodGet,
			target: agent.RuntimeAgentActivityRoutePrefix + "missing/activity",
			status: http.StatusNotFound,
			body:   "{\"error\":\"agent_not_found\"}\n",
		},
		{
			name:   "noncanonical agent",
			method: http.MethodGet,
			target: agent.RuntimeAgentActivityRoutePrefix + "Main/activity",
			status: http.StatusNotFound,
			body:   "{\"error\":\"agent_not_found\"}\n",
		},
		{
			name:   "wrong suffix",
			method: http.MethodGet,
			target: agent.RuntimeAgentActivityRoutePrefix + "main/activity/",
			status: http.StatusNotFound,
			body:   "{\"error\":\"agent_not_found\"}\n",
		},
		{
			name:   "invalid query",
			method: http.MethodGet,
			target: agent.RuntimeAgentActivityRoutePrefix + "main/activity?unknown=1",
			status: http.StatusBadRequest,
			body:   "{\"error\":\"invalid_agent_activity_query\"}\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(
				recorder,
				httptest.NewRequest(test.method, test.target, nil),
			)
			if recorder.Code != test.status ||
				recorder.Body.String() != test.body {
				t.Fatalf(
					"response = %d %q",
					recorder.Code,
					recorder.Body.String(),
				)
			}
		})
	}

	recorder := httptest.NewRecorder()
	(&agentActivityHandler{}).ServeHTTP(
		recorder,
		httptest.NewRequest(
			http.MethodGet,
			agent.RuntimeAgentActivityRoutePrefix+"main/activity",
			nil,
		),
	)
	if recorder.Code != http.StatusServiceUnavailable ||
		recorder.Body.String() !=
			"{\"error\":\"agent_activity_unavailable\"}\n" {
		t.Fatalf(
			"nil loop response = %d %q",
			recorder.Code,
			recorder.Body.String(),
		)
	}
}

func TestAgentActivityRuntimeRouteRequiresBearerUpdatesAndReleases(t *testing.T) {
	loop, messageBus := newGatewayWorkflowAuthoringTestLoop(t)
	defer loop.Close()
	defer messageBus.Close()
	cfg := loop.GetConfig()
	manager, err := channels.NewManager(cfg, messageBus, nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	const token = "agent-activity-test-token"
	healthServer := health.NewServer("127.0.0.1", 0, token)
	manager.SetupHTTPServerListeners(
		[]net.Listener{listener},
		listener.Addr().String(),
		healthServer,
	)
	runningServices := &services{
		ChannelManager: manager,
		HealthServer:   healthServer,
		authToken:      token,
	}
	if err = prepareAgentActivityRoute(runningServices, loop); err != nil {
		t.Fatalf("prepareAgentActivityRoute() error = %v", err)
	}
	if err = manager.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		releaseAgentActivityRoute(runningServices)
		_ = manager.StopAll(ctx)
	})

	endpoint := "http://" + listener.Addr().String() +
		agent.RuntimeAgentActivityRoutePrefix +
		"main/activity"
	client := &http.Client{Timeout: 5 * time.Second}
	request, _ := http.NewRequest(http.MethodGet, endpoint, nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("unauthenticated request: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized ||
		response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf(
			"unauthenticated response = %d %#v",
			response.StatusCode,
			response.Header,
		)
	}

	request, _ = http.NewRequest(http.MethodGet, endpoint, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("authenticated request: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated response = %d %s", response.StatusCode, body)
	}
	if _, err = agent.DecodeAgentActivityPage(body); err != nil {
		t.Fatalf("decode authenticated response: %v", err)
	}

	reloadedLoop, reloadedBus := newGatewayWorkflowAuthoringTestLoopWithID(t, "reloaded")
	defer reloadedLoop.Close()
	defer reloadedBus.Close()
	if err = prepareAgentActivityRoute(runningServices, reloadedLoop); err != nil {
		t.Fatalf("update agent activity route: %v", err)
	}
	reloadedEndpoint := "http://" + listener.Addr().String() +
		agent.RuntimeAgentActivityRoutePrefix +
		"reloaded/activity"
	request, _ = http.NewRequest(http.MethodGet, reloadedEndpoint, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("reloaded request: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reloaded route status = %d", response.StatusCode)
	}

	releaseAgentActivityRoute(runningServices)
	request, _ = http.NewRequest(http.MethodGet, reloadedEndpoint, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("released request: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("released route status = %d, want 404", response.StatusCode)
	}
}

func TestPrepareAgentActivityRouteFailsClosedWithoutBearer(t *testing.T) {
	loop, messageBus := newGatewayWorkflowAuthoringTestLoop(t)
	defer loop.Close()
	defer messageBus.Close()
	cfg := loop.GetConfig()
	manager, err := channels.NewManager(cfg, messageBus, nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	healthServer := health.NewServer("127.0.0.1", 0, "")
	runningServices := &services{
		ChannelManager: manager,
		HealthServer:   healthServer,
	}
	if err = prepareAgentActivityRoute(runningServices, loop); err == nil {
		t.Fatal("prepareAgentActivityRoute() succeeded without bearer token")
	}
}
