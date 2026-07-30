package gateway

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/health"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWorkflowAuthoringCapabilitiesHandlerProjectsLiveLoop(t *testing.T) {
	loop, messageBus := newGatewayWorkflowAuthoringTestLoop(t)
	defer loop.Close()
	defer messageBus.Close()
	handler := newWorkflowAuthoringCapabilitiesHandler(loop)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, workflows.RuntimeAuthoringCapabilitiesPath, nil),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" ||
		recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %#v", recorder.Header())
	}
	catalog, err := workflows.DecodeWorkflowAuthoringCapabilities(recorder.Body.Bytes())
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(catalog.Agents) != 1 ||
		catalog.Agents[0].Target != "agent/main" ||
		catalog.MCPStatus != workflows.WorkflowAuthoringMCPDisabled {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestWorkflowAuthoringCapabilitiesHandlerRejectsNonExactRequests(t *testing.T) {
	handler := &workflowAuthoringCapabilitiesHandler{}
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, workflows.RuntimeAuthoringCapabilitiesPath, nil),
		httptest.NewRequest(http.MethodGet, workflows.RuntimeAuthoringCapabilitiesPath+"?private=1", nil),
		httptest.NewRequest(http.MethodGet, workflows.RuntimeAuthoringCapabilitiesPath+"/", nil),
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusOK ||
			recorder.Header().Get("Cache-Control") != "no-store" ||
			recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s %s response = %d %#v", request.Method, request.URL, recorder.Code, recorder.Header())
		}
		if body := recorder.Body.String(); body !=
			"{\"error\":\"workflow_authoring_capabilities_unavailable\"}\n" {
			t.Fatalf("error body = %q", body)
		}
	}
}

func TestWorkflowAuthoringRouteRequiresPIDBearerAndReleases(t *testing.T) {
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
	const token = "workflow-authoring-test-token"
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
	if err = prepareWorkflowAuthoringRoute(runningServices, loop); err != nil {
		t.Fatalf("prepareWorkflowAuthoringRoute() error = %v", err)
	}
	if err = manager.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		releaseWorkflowAuthoringRoute(runningServices)
		_ = manager.StopAll(ctx)
	})

	endpoint := "http://" + listener.Addr().String() + workflows.RuntimeAuthoringCapabilitiesPath
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
		t.Fatalf("unauthenticated response = %d %#v", response.StatusCode, response.Header)
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

	reloadedLoop, reloadedBus := newGatewayWorkflowAuthoringTestLoopWithID(t, "reloaded")
	defer reloadedLoop.Close()
	defer reloadedBus.Close()
	if err = prepareWorkflowAuthoringRoute(runningServices, reloadedLoop); err != nil {
		t.Fatalf("update workflow authoring route: %v", err)
	}
	request, _ = http.NewRequest(http.MethodGet, endpoint, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("reloaded route request: %v", err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	reloadedCatalog, decodeErr := workflows.DecodeWorkflowAuthoringCapabilities(body)
	if decodeErr != nil ||
		len(reloadedCatalog.Agents) != 1 ||
		reloadedCatalog.Agents[0].ID != "reloaded" {
		t.Fatalf("reloaded route catalog = %#v, error = %v", reloadedCatalog, decodeErr)
	}

	releaseWorkflowAuthoringRoute(runningServices)
	request, _ = http.NewRequest(http.MethodGet, endpoint, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("released route request: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("released route status = %d, want 404", response.StatusCode)
	}

	if err = prepareWorkflowAuthoringRoute(runningServices, loop); err != nil {
		t.Fatalf("re-prepare workflow authoring route: %v", err)
	}
	request, _ = http.NewRequest(http.MethodGet, endpoint, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("re-registered route request: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("re-registered route status = %d, want 200", response.StatusCode)
	}
}

func newGatewayWorkflowAuthoringTestLoop(
	t *testing.T,
) (*agent.AgentLoop, *bus.MessageBus) {
	return newGatewayWorkflowAuthoringTestLoopWithID(t, "")
}

func newGatewayWorkflowAuthoringTestLoopWithID(
	t *testing.T,
	agentID string,
) (*agent.AgentLoop, *bus.MessageBus) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.MCP.Enabled = false
	if agentID != "" {
		cfg.Agents.List = []config.AgentConfig{{
			ID:      agentID,
			Default: true,
		}}
	}
	messageBus := bus.NewMessageBus()
	return agent.NewAgentLoop(
		cfg,
		messageBus,
		&startupBlockedProvider{reason: "not used"},
	), messageBus
}
