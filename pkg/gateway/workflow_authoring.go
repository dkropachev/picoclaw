package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type workflowAuthoringCapabilitiesHandler struct {
	loop atomic.Pointer[agent.AgentLoop]
}

func newWorkflowAuthoringCapabilitiesHandler(
	agentLoop *agent.AgentLoop,
) *workflowAuthoringCapabilitiesHandler {
	handler := &workflowAuthoringCapabilitiesHandler{}
	handler.loop.Store(agentLoop)
	return handler
}

func prepareWorkflowAuthoringRoute(
	runningServices *services,
	agentLoop *agent.AgentLoop,
) error {
	if runningServices == nil || runningServices.ChannelManager == nil {
		return fmt.Errorf("shared HTTP channel manager is required for workflow authoring")
	}
	if agentLoop == nil {
		return fmt.Errorf("live agent runtime is required for workflow authoring")
	}
	authToken := strings.TrimSpace(runningServices.authToken)
	if runningServices.HealthServer == nil ||
		authToken == "" ||
		authToken != runningServices.authToken ||
		!runningServices.HealthServer.UsesBearerToken(authToken) {
		return fmt.Errorf("protected gateway runtime is required for workflow authoring")
	}
	if runningServices.workflowAuthoringRelease != nil {
		if runningServices.workflowAuthoringHandler == nil {
			return fmt.Errorf("workflow authoring route state is unavailable")
		}
		runningServices.workflowAuthoringHandler.loop.Store(agentLoop)
		return nil
	}

	handler := newWorkflowAuthoringCapabilitiesHandler(agentLoop)
	release, err := runningServices.ChannelManager.RegisterHTTPRoute(
		workflows.RuntimeAuthoringCapabilitiesPath,
		runningServices.HealthServer.Protect(handler),
	)
	if err != nil {
		return fmt.Errorf("register workflow authoring API: %w", err)
	}
	runningServices.workflowAuthoringHandler = handler
	runningServices.workflowAuthoringRelease = release
	return nil
}

func releaseWorkflowAuthoringRoute(runningServices *services) {
	if runningServices == nil || runningServices.workflowAuthoringRelease == nil {
		return
	}
	runningServices.workflowAuthoringRelease()
	runningServices.workflowAuthoringRelease = nil
	runningServices.workflowAuthoringHandler = nil
}

func (handler *workflowAuthoringCapabilitiesHandler) ServeHTTP(
	w http.ResponseWriter,
	r *http.Request,
) {
	setWorkflowAuthoringHeaders(w)
	if r == nil || r.URL == nil ||
		r.Method != http.MethodGet ||
		r.URL.Path != workflows.RuntimeAuthoringCapabilitiesPath ||
		r.URL.RawQuery != "" {
		status := http.StatusBadRequest
		if r != nil && r.Method != http.MethodGet {
			status = http.StatusMethodNotAllowed
			w.Header().Set("Allow", http.MethodGet)
		}
		writeWorkflowAuthoringError(w, status)
		return
	}
	if handler == nil {
		writeWorkflowAuthoringError(w, http.StatusServiceUnavailable)
		return
	}
	loop := handler.loop.Load()
	if loop == nil {
		writeWorkflowAuthoringError(w, http.StatusServiceUnavailable)
		return
	}
	encoded, err := loop.WorkflowAuthoringCapabilitiesJSON(r.Context())
	if err != nil {
		writeWorkflowAuthoringError(w, http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func setWorkflowAuthoringHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeWorkflowAuthoringError(w http.ResponseWriter, status int) {
	setWorkflowAuthoringHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "workflow_authoring_capabilities_unavailable",
	})
}
