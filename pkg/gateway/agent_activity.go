package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/routing"
)

type agentActivityHandler struct {
	loop atomic.Pointer[agent.AgentLoop]
}

func newAgentActivityHandler(agentLoop *agent.AgentLoop) *agentActivityHandler {
	handler := &agentActivityHandler{}
	handler.loop.Store(agentLoop)
	return handler
}

func prepareAgentActivityRoute(
	runningServices *services,
	agentLoop *agent.AgentLoop,
) error {
	if err := validateAgentActivityRouteOwner(runningServices, agentLoop); err != nil {
		return err
	}
	if runningServices.agentActivityRelease != nil {
		return refreshAgentActivityRoute(runningServices, agentLoop)
	}
	return registerAgentActivityRoute(runningServices, agentLoop)
}

func validateAgentActivityRouteOwner(
	runningServices *services,
	agentLoop *agent.AgentLoop,
) error {
	if runningServices == nil || runningServices.ChannelManager == nil {
		return fmt.Errorf("shared HTTP channel manager is required for agent activity")
	}
	if agentLoop == nil {
		return fmt.Errorf("live agent runtime is required for agent activity")
	}
	authToken := strings.TrimSpace(runningServices.authToken)
	if runningServices.HealthServer == nil ||
		authToken == "" ||
		authToken != runningServices.authToken ||
		!runningServices.HealthServer.UsesBearerToken(authToken) {
		return fmt.Errorf("protected gateway runtime is required for agent activity")
	}
	return nil
}

func refreshAgentActivityRoute(
	runningServices *services,
	agentLoop *agent.AgentLoop,
) error {
	if runningServices.agentActivityHandler == nil {
		return fmt.Errorf("agent activity route state is unavailable")
	}
	runningServices.agentActivityHandler.loop.Store(agentLoop)
	return nil
}

func registerAgentActivityRoute(
	runningServices *services,
	agentLoop *agent.AgentLoop,
) error {
	handler := newAgentActivityHandler(agentLoop)
	release, err := runningServices.ChannelManager.RegisterHTTPRoute(
		agent.RuntimeAgentActivityRoutePrefix,
		runningServices.HealthServer.Protect(handler),
	)
	if err != nil {
		return fmt.Errorf("register agent activity API: %w", err)
	}
	runningServices.agentActivityHandler = handler
	runningServices.agentActivityRelease = release
	return nil
}

func releaseAgentActivityRoute(runningServices *services) {
	if runningServices == nil || runningServices.agentActivityRelease == nil {
		return
	}
	runningServices.agentActivityRelease()
	runningServices.agentActivityRelease = nil
	runningServices.agentActivityHandler = nil
}

func (handler *agentActivityHandler) ServeHTTP(
	w http.ResponseWriter,
	r *http.Request,
) {
	setAgentActivityRuntimeHeaders(w)
	if r == nil || r.URL == nil {
		writeAgentActivityRuntimeError(w, http.StatusNotFound, "agent_not_found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeAgentActivityRuntimeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	agentID, ok := runtimeAgentActivityID(r)
	if !ok {
		writeAgentActivityRuntimeError(w, http.StatusNotFound, "agent_not_found")
		return
	}
	cursor, limit, err := agent.ParseAgentActivityQuery(r.URL.RawQuery)
	if err != nil {
		writeAgentActivityRuntimeError(
			w,
			http.StatusBadRequest,
			"invalid_agent_activity_query",
		)
		return
	}
	if handler == nil {
		writeAgentActivityRuntimeError(
			w,
			http.StatusServiceUnavailable,
			"agent_activity_unavailable",
		)
		return
	}
	loop := handler.loop.Load()
	if loop == nil {
		writeAgentActivityRuntimeError(
			w,
			http.StatusServiceUnavailable,
			"agent_activity_unavailable",
		)
		return
	}
	registry := loop.GetRegistry()
	if registry == nil {
		writeAgentActivityRuntimeError(
			w,
			http.StatusServiceUnavailable,
			"agent_activity_unavailable",
		)
		return
	}
	if _, found := registry.GetAgent(agentID); !found {
		writeAgentActivityRuntimeError(w, http.StatusNotFound, "agent_not_found")
		return
	}
	page, err := loop.AgentActivity(agentID, cursor, limit)
	if err != nil {
		status := http.StatusServiceUnavailable
		message := "agent_activity_unavailable"
		if errors.Is(err, agent.ErrInvalidAgentActivityQuery) {
			status = http.StatusBadRequest
			message = "invalid_agent_activity_query"
		}
		writeAgentActivityRuntimeError(w, status, message)
		return
	}
	encoded, err := agent.MarshalAgentActivityPage(page)
	if err != nil {
		writeAgentActivityRuntimeError(
			w,
			http.StatusServiceUnavailable,
			"agent_activity_unavailable",
		)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func runtimeAgentActivityID(r *http.Request) (string, bool) {
	if r == nil || r.URL == nil || r.URL.RawPath != "" {
		return "", false
	}
	if !strings.HasPrefix(r.URL.Path, agent.RuntimeAgentActivityRoutePrefix) {
		return "", false
	}
	relative := strings.TrimPrefix(
		r.URL.Path,
		agent.RuntimeAgentActivityRoutePrefix,
	)
	segments := strings.Split(relative, "/")
	if len(segments) != 2 ||
		segments[1] != "activity" ||
		!routing.IsCanonicalAgentID(segments[0]) {
		return "", false
	}
	return segments[0], true
}

func setAgentActivityRuntimeHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeAgentActivityRuntimeError(
	w http.ResponseWriter,
	status int,
	message string,
) {
	setAgentActivityRuntimeHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
