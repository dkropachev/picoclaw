package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type workflowValidateRequest struct {
	Ref string `json:"ref"`
}

type workflowRunRequest struct {
	Ref      string             `json:"ref"`
	Inputs   map[string]any     `json:"inputs,omitempty"`
	Secrets  map[string]string  `json:"secrets,omitempty"`
	Session  string             `json:"session,omitempty"`
	Delivery workflows.Delivery `json:"delivery,omitempty"`
}

type workflowCancelRequest struct {
	Reason string `json:"reason,omitempty"`
}

type workflowRetryRequest struct {
	Secrets map[string]string `json:"secrets,omitempty"`
}

func (h *Handler) registerWorkflowRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/workflows", h.handleListWorkflows)
	mux.HandleFunc("POST /api/workflows/validate", h.handleValidateWorkflow)
	mux.HandleFunc("POST /api/workflows/reload", h.handleReloadWorkflows)
	mux.HandleFunc("POST /api/workflows/run", h.handleRunWorkflow)
	mux.HandleFunc("GET /api/workflows/runs", h.handleListWorkflowRuns)
	mux.HandleFunc("GET /api/workflows/runs/{run_id}", h.handleGetWorkflowRun)
	mux.HandleFunc("GET /api/workflows/runs/{run_id}/events", h.handleGetWorkflowRunEvents)
	mux.HandleFunc("GET /api/workflows/runs/{run_id}/events/stream", h.handleStreamWorkflowRunEvents)
	mux.HandleFunc("GET /api/workflows/runs/{run_id}/graph", h.handleGetWorkflowRunGraph)
	mux.HandleFunc("POST /api/workflows/runs/{run_id}/cancel", h.handleCancelWorkflowRun)
	mux.HandleFunc("POST /api/workflows/runs/{run_id}/retry", h.handleRetryWorkflowRun)
}

func (h *Handler) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	workspace, err := h.workflowWorkspace()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defs, err := workflows.ListLocal(r.Context(), workspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeWorkflowJSON(w, map[string]any{"workflows": defs})
}

func (h *Handler) handleValidateWorkflow(w http.ResponseWriter, r *http.Request) {
	var req workflowValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	workspace, err := h.workflowWorkspace()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	workflow, err := workflows.LoadLocal(r.Context(), workspace, req.Ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := workflows.Validate(workflow); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeWorkflowJSON(w, map[string]any{"ref": req.Ref, "valid": true})
}

func (h *Handler) handleReloadWorkflows(w http.ResponseWriter, r *http.Request) {
	workspace, err := h.workflowWorkspace()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	result, err := workflows.ReloadLocal(r.Context(), workspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeWorkflowJSON(w, result)
}

func (h *Handler) handleRunWorkflow(w http.ResponseWriter, r *http.Request) {
	var req workflowRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	cfg, _, executor, err := h.workflowRuntime(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !cfg.Workflows.Enabled {
		http.Error(w, "workflows are disabled", http.StatusBadRequest)
		return
	}
	result, runErr := executor.Run(r.Context(), workflows.RunRequest{
		Ref:      req.Ref,
		Inputs:   req.Inputs,
		Secrets:  req.Secrets,
		Session:  req.Session,
		Delivery: req.Delivery,
	})
	if runErr != nil {
		writeWorkflowJSONStatus(w, http.StatusBadRequest, map[string]any{"result": result, "error": runErr.Error()})
		return
	}
	writeWorkflowJSON(w, result)
}

func (h *Handler) handleCancelWorkflowRun(w http.ResponseWriter, r *http.Request) {
	var req workflowCancelRequest
	if err := decodeOptionalWorkflowJSON(r, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	store, err := h.workflowRunStore(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	run, err := store.CancelRun(r.Context(), r.PathValue("run_id"), req.Reason)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeWorkflowJSON(w, run)
}

func (h *Handler) handleRetryWorkflowRun(w http.ResponseWriter, r *http.Request) {
	var req workflowRetryRequest
	if err := decodeOptionalWorkflowJSON(r, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	cfg, _, executor, err := h.workflowRuntime(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !cfg.Workflows.Enabled {
		http.Error(w, "workflows are disabled", http.StatusBadRequest)
		return
	}
	result, runErr := executor.Retry(r.Context(), r.PathValue("run_id"), req.Secrets)
	if runErr != nil {
		writeWorkflowJSONStatus(w, http.StatusBadRequest, map[string]any{"result": result, "error": runErr.Error()})
		return
	}
	writeWorkflowJSON(w, result)
}

func (h *Handler) handleListWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	store, err := h.workflowRunStore(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	runs, err := store.ListRuns(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeWorkflowJSON(w, map[string]any{"runs": runs})
}

func (h *Handler) handleGetWorkflowRun(w http.ResponseWriter, r *http.Request) {
	store, err := h.workflowRunStore(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	run, err := store.GetRun(r.Context(), r.PathValue("run_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeWorkflowJSON(w, run)
}

func (h *Handler) handleGetWorkflowRunEvents(w http.ResponseWriter, r *http.Request) {
	store, err := h.workflowRunStore(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	events, err := store.Events(r.Context(), r.PathValue("run_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeWorkflowJSON(w, map[string]any{"run_id": r.PathValue("run_id"), "events": events})
}

func (h *Handler) handleStreamWorkflowRunEvents(w http.ResponseWriter, r *http.Request) {
	store, err := h.workflowRunStore(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	runID := r.PathValue("run_id")
	if _, err := store.GetRun(r.Context(), runID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sent := 0
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		events, err := store.Events(r.Context(), runID)
		if err != nil {
			return
		}
		for ; sent < len(events); sent++ {
			data, err := json.Marshal(events[sent])
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", events[sent].Kind, data)
		}
		flusher.Flush()
		if r.URL.Query().Get("once") == "true" {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) handleGetWorkflowRunGraph(w http.ResponseWriter, r *http.Request) {
	store, err := h.workflowRunStore(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	graph, err := workflows.BuildRunGraph(r.Context(), store, r.PathValue("run_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeWorkflowJSON(w, graph)
}

func (h *Handler) workflowWorkspace() (string, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return "", fmt.Errorf("Failed to load config: %w", err)
	}
	return cfg.WorkspacePath(), nil
}

func (h *Handler) workflowRuntime(ctx context.Context) (*config.Config, *workflows.FileRunStore, *workflows.Executor, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("Failed to load config: %w", err)
	}
	workspace := cfg.WorkspacePath()
	store := workflows.NewFileRunStore(workspace)
	if err := pruneWorkflowRunStore(ctx, cfg, store); err != nil {
		return nil, nil, nil, err
	}
	executor := &workflows.Executor{
		WorkspaceDir:      workspace,
		Store:             store,
		MaxCallDepth:      cfg.Workflows.EffectiveMaxCallDepth(),
		MaxConcurrentRuns: cfg.Workflows.EffectiveMaxConcurrentRuns(),
		DefaultTimeout:    cfg.Workflows.EffectiveDefaultTimeout(),
		Tools:             nil,
		Agents:            nil,
		Functions:         nil,
	}
	return cfg, store, executor, nil
}

func (h *Handler) workflowRunStore(ctx context.Context) (*workflows.FileRunStore, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return nil, fmt.Errorf("Failed to load config: %w", err)
	}
	store := workflows.NewFileRunStore(cfg.WorkspacePath())
	if err := pruneWorkflowRunStore(ctx, cfg, store); err != nil {
		return nil, err
	}
	return store, nil
}

func pruneWorkflowRunStore(ctx context.Context, cfg *config.Config, store workflows.RunStore) error {
	if cfg == nil || store == nil {
		return nil
	}
	days := cfg.Workflows.EffectiveRetentionDays()
	if days <= 0 {
		return nil
	}
	_, err := store.PruneTerminalRuns(ctx, time.Now().UTC().AddDate(0, 0, -days))
	return err
}

func decodeOptionalWorkflowJSON(r *http.Request, dest any) error {
	if r.Body == nil {
		return nil
	}
	err := json.NewDecoder(r.Body).Decode(dest)
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func writeWorkflowJSON(w http.ResponseWriter, value any) {
	writeWorkflowJSONStatus(w, http.StatusOK, value)
}

func writeWorkflowJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
