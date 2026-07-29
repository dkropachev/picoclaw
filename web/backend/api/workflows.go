package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type workflowValidateRequest struct {
	Ref string `json:"ref"`
}

type workflowRunRequest struct {
	Ref                        string             `json:"ref"`
	Inputs                     map[string]any     `json:"inputs,omitempty"`
	Secrets                    map[string]string  `json:"secrets,omitempty"`
	Session                    string             `json:"session,omitempty"`
	Delivery                   workflows.Delivery `json:"delivery,omitempty"`
	Async                      bool               `json:"async,omitempty"`
	ExpectedDependencyRevision string             `json:"expected_dependency_revision,omitempty"`
}

type workflowDevelopmentTestRequest struct {
	Prompt    string             `json:"prompt,omitempty"`
	TargetRef string             `json:"target_ref,omitempty"`
	YAML      *string            `json:"yaml,omitempty"`
	EventID   string             `json:"event_id,omitempty"`
	Inputs    map[string]any     `json:"inputs,omitempty"`
	Secrets   map[string]string  `json:"secrets,omitempty"`
	Session   string             `json:"session,omitempty"`
	Delivery  workflows.Delivery `json:"delivery,omitempty"`
	Async     bool               `json:"async,omitempty"`
}

const workflowDevelopmentTestRequestMaxBytes = 1 << 20

type workflowCancelRequest struct {
	Reason optionalWorkflowCancelReason `json:"reason,omitempty"`
}

type optionalWorkflowCancelReason struct {
	Value   string
	Present bool
}

func (r *optionalWorkflowCancelReason) UnmarshalJSON(data []byte) error {
	r.Present = true
	if strings.TrimSpace(string(data)) == "null" {
		return errors.New("workflow cancellation reason must be a string")
	}
	return json.Unmarshal(data, &r.Value)
}

const (
	workflowCancelRequestMaxBytes = 16 << 10
)

type workflowRetryRequest struct {
	Secrets                    map[string]string `json:"secrets,omitempty"`
	ExpectedDependencyRevision string            `json:"expected_dependency_revision,omitempty"`
}

type workflowDefinitionResponse struct {
	Ref          string                        `json:"ref"`
	Name         string                        `json:"name,omitempty"`
	Error        string                        `json:"error,omitempty"`
	WorkflowCall *workflowCallContractResponse `json:"workflow_call,omitempty"`
	EventTrigger *workflows.EventTrigger       `json:"event_trigger,omitempty"`
}

type workflowCallContractResponse struct {
	Inputs  map[string]workflows.Input  `json:"inputs,omitempty"`
	Secrets map[string]workflows.Secret `json:"secrets,omitempty"`
}

func (h *Handler) registerWorkflowRoutes(mux *http.ServeMux) {
	h.registerWorkflowEditorRoutes(mux)
	mux.HandleFunc("GET /api/workflows", h.handleListWorkflows)
	mux.HandleFunc("GET /api/workflows/settings", h.handleGetWorkflowSettings)
	mux.HandleFunc("PATCH /api/workflows/settings", h.handlePatchWorkflowSettings)
	mux.HandleFunc("GET /api/workflows/templates", h.handleListWorkflowTemplates)
	mux.HandleFunc(
		"POST /api/workflows/templates/{name}/install",
		h.handleInstallWorkflowTemplate,
	)
	mux.HandleFunc(
		"POST /api/workflows/dependencies/check",
		h.handleCheckWorkflowDependencies,
	)
	mux.HandleFunc("GET /api/workflows/compatibility", h.handleGetWorkflowCompatibility)
	mux.HandleFunc("POST /api/workflows/revalidate", h.handleRevalidateWorkflows)
	mux.HandleFunc("POST /api/workflows/validate", h.handleValidateWorkflow)
	mux.HandleFunc("POST /api/workflows/reload", h.handleReloadWorkflows)
	mux.HandleFunc("POST /api/workflows/run", h.handleRunWorkflow)
	mux.HandleFunc("GET /api/workflows/development", h.handleGetWorkflowDevelopment)
	mux.HandleFunc("POST /api/workflows/development/start", h.handleStartWorkflowDevelopment)
	mux.HandleFunc("POST /api/workflows/development/revise", h.handleReviseWorkflowDevelopment)
	mux.HandleFunc("POST /api/workflows/development/ai-revise", h.handleAIReviseWorkflowDevelopment)
	mux.HandleFunc("POST /api/workflows/development/validate", h.handleValidateWorkflowDevelopment)
	mux.HandleFunc("POST /api/workflows/development/test", h.handleTestWorkflowDevelopment)
	mux.HandleFunc("POST /api/workflows/development/publish", h.handlePublishWorkflowDevelopment)
	mux.HandleFunc("POST /api/workflows/development/discard", h.handleDiscardWorkflowDevelopment)
	mux.HandleFunc("GET /api/workflows/runs", h.handleListWorkflowRuns)
	mux.HandleFunc("GET /api/workflows/runs/{run_id}", h.handleGetWorkflowRun)
	mux.HandleFunc("GET /api/workflows/runs/{run_id}/events", h.handleGetWorkflowRunEvents)
	mux.HandleFunc("GET /api/workflows/runs/{run_id}/events/stream", h.handleStreamWorkflowRunEvents)
	mux.HandleFunc("GET /api/workflows/runs/{run_id}/graph", h.handleGetWorkflowRunGraph)
	mux.HandleFunc("POST /api/workflows/runs/{run_id}/cancel", h.handleCancelWorkflowRun)
	mux.HandleFunc("POST /api/workflows/runs/{run_id}/retry", h.handleRetryWorkflowRun)
}

func (h *Handler) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.workflowConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	workspace := cfg.WorkspacePath()
	localOpts := workflowLocalOptionsFromConfig(cfg)
	defs, err := workflows.ListLocal(r.Context(), workspace, localOpts...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	responseDefs, err := workflowDefinitionResponses(r.Context(), workspace, defs, localOpts...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	compatibility, compatErr := workflows.LoadCompatibilitySummary(
		r.Context(),
		workspace,
		h.workflowCompatibilityRuntime(r.Context()),
		localOpts...,
	)
	if compatErr != nil {
		http.Error(w, compatErr.Error(), http.StatusInternalServerError)
		return
	}
	writeWorkflowJSON(w, map[string]any{"workflows": responseDefs, "compatibility": compatibility})
}

func workflowDefinitionResponses(
	ctx context.Context,
	workspace string,
	defs []workflows.Definition,
	opts ...workflows.LocalOption,
) ([]workflowDefinitionResponse, error) {
	out := make([]workflowDefinitionResponse, 0, len(defs))
	for _, def := range defs {
		response := workflowDefinitionResponse{
			Ref:   def.Ref,
			Name:  def.Name,
			Error: def.Error,
		}
		if def.Error == "" {
			workflow, err := workflows.LoadLocal(ctx, workspace, def.Ref, opts...)
			if err != nil {
				return nil, err
			}
			if workflow.On.WorkflowCall != nil {
				response.WorkflowCall = &workflowCallContractResponse{
					Inputs:  workflow.On.WorkflowCall.Inputs,
					Secrets: workflow.On.WorkflowCall.Secrets,
				}
			}
			// LoadLocal returns a fresh parsed workflow owned by this response,
			// so this is a safe read-only projection with no shared runtime state.
			response.EventTrigger = workflow.On.Event
		}
		out = append(out, response)
	}
	return out, nil
}

func (h *Handler) handleGetWorkflowCompatibility(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.workflowConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	workspace := cfg.WorkspacePath()
	summary, err := workflows.LoadCompatibilitySummary(
		r.Context(),
		workspace,
		h.workflowCompatibilityRuntime(r.Context()),
		workflowLocalOptionsFromConfig(cfg)...,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeWorkflowJSON(w, summary)
}

func (h *Handler) handleRevalidateWorkflows(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.workflowConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	workspace := cfg.WorkspacePath()
	localOpts := workflowLocalOptionsFromConfig(cfg)
	if _, revalidateErr := workflows.RevalidateLocal(
		r.Context(),
		workspace,
		h.workflowCompatibilityRuntime(r.Context()),
		localOpts...,
	); revalidateErr != nil {
		http.Error(w, revalidateErr.Error(), http.StatusInternalServerError)
		return
	}
	summary, err := workflows.LoadCompatibilitySummary(
		r.Context(),
		workspace,
		h.workflowCompatibilityRuntime(r.Context()),
		localOpts...,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeWorkflowJSON(w, summary)
}

func (h *Handler) handleValidateWorkflow(w http.ResponseWriter, r *http.Request) {
	var req workflowValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	cfg, err := h.workflowConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	workflow, err := workflows.LoadLocal(
		r.Context(),
		cfg.WorkspacePath(),
		req.Ref,
		workflowLocalOptionsFromConfig(cfg)...,
	)
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
	cfg, err := h.workflowConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	result, err := workflows.ReloadLocal(
		r.Context(),
		cfg.WorkspacePath(),
		workflowLocalOptionsFromConfig(cfg)...,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeWorkflowJSON(w, result)
}

func (h *Handler) handleRunWorkflow(w http.ResponseWriter, r *http.Request) {
	var req workflowRunRequest
	if err := decodeStrictWorkflowJSON(r, &req, false); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	cfg, _, executor, err := h.workflowRuntime(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	backgroundOwnsRuntime := false
	defer func() {
		if !backgroundOwnsRuntime {
			closeWorkflowRuntime(executor)
		}
	}()
	if !cfg.Workflows.Enabled {
		http.Error(w, "workflows are disabled", http.StatusBadRequest)
		return
	}
	dependencies, err := h.requirePublishedWorkflowDependenciesReady(
		r.Context(),
		req.Ref,
		req.ExpectedDependencyRevision,
	)
	if err != nil {
		writeWorkflowRunDependencyError(w, err)
		return
	}
	if err := workflows.EnsureWorkflowRunnable(
		r.Context(),
		cfg.WorkspacePath(),
		dependencies.RootRef,
		h.workflowCompatibilityRuntime(r.Context()),
		workflowLocalOptionsFromConfig(cfg)...,
	); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	runReq := workflows.RunRequest{
		Ref:      dependencies.RootRef,
		Inputs:   req.Inputs,
		Secrets:  req.Secrets,
		Session:  req.Session,
		Delivery: req.Delivery,
	}
	if req.Async {
		backgroundOwnsRuntime = true
		started := startWorkflowRunBackground(executor, runReq, nil)
		if started.Run != nil {
			started.Release()
			writeWorkflowJSONStatus(w, http.StatusAccepted, workflows.RunResult{
				RunID:  started.Run.ID,
				Status: workflows.RunStatusRunning,
			})
			return
		}
		if started.Err != nil {
			if started.Result != nil {
				writeWorkflowJSONStatus(
					w,
					http.StatusBadRequest,
					map[string]any{"result": started.Result, "error": started.Err.Error()},
				)
				return
			}
			http.Error(w, started.Err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "workflow run did not start", http.StatusInternalServerError)
		return
	}
	result, runErr := executor.Run(r.Context(), runReq)
	if runErr != nil {
		writeWorkflowJSONStatus(w, http.StatusBadRequest, map[string]any{"result": result, "error": runErr.Error()})
		return
	}
	writeWorkflowJSON(w, result)
}

func (h *Handler) handleCancelWorkflowRun(w http.ResponseWriter, r *http.Request) {
	var req workflowCancelRequest
	if err := decodeWorkflowCancelRequest(w, r, &req); err != nil {
		var maximum *http.MaxBytesError
		if errors.As(err, &maximum) {
			writeWorkflowJSONStatus(
				w,
				http.StatusRequestEntityTooLarge,
				map[string]any{"error": "cancel_request_too_large"},
			)
			return
		}
		writeWorkflowJSONStatus(
			w,
			http.StatusBadRequest,
			map[string]any{"error": "invalid_cancel_request"},
		)
		return
	}
	reason := ""
	if req.Reason.Present {
		var err error
		reason, err = workflows.NormalizeWorkflowCancelReason(req.Reason.Value)
		if err != nil || reason == "" {
			writeWorkflowJSONStatus(
				w,
				http.StatusBadRequest,
				map[string]any{"error": "invalid_cancel_reason"},
			)
			return
		}
	}
	_, store, executor, err := h.workflowRuntime(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer closeWorkflowRuntime(executor)
	run, err := executor.CancelRun(r.Context(), r.PathValue("run_id"), reason)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	h.recordCanceledWorkflowDevelopmentRun(r.Context(), run)
	writeWorkflowJSON(
		w,
		workflows.ProjectWorkflowRunForBrowser(
			run,
			workflows.IsEventBackedDraftRunFamily(r.Context(), store, run),
		),
	)
}

func (h *Handler) handleRetryWorkflowRun(w http.ResponseWriter, r *http.Request) {
	var req workflowRetryRequest
	if err := decodeStrictWorkflowJSON(r, &req, true); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	cfg, store, executor, err := h.workflowRuntime(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer closeWorkflowRuntime(executor)
	if !cfg.Workflows.Enabled {
		http.Error(w, "workflows are disabled", http.StatusBadRequest)
		return
	}
	previousRun, err := store.GetRun(r.Context(), r.PathValue("run_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if workflows.IsEventBackedDraftRunFamily(r.Context(), store, previousRun) {
		http.Error(
			w,
			"event-backed draft run retries are unavailable; run the draft test again",
			http.StatusConflict,
		)
		return
	}
	if _, err := h.requirePublishedWorkflowDependenciesReady(
		r.Context(),
		previousRun.WorkflowRef,
		req.ExpectedDependencyRevision,
	); err != nil {
		writeWorkflowRunDependencyError(w, err)
		return
	}
	if err := workflows.EnsureWorkflowRunnable(
		r.Context(),
		cfg.WorkspacePath(),
		previousRun.WorkflowRef,
		h.workflowCompatibilityRuntime(r.Context()),
		workflowLocalOptionsFromConfig(cfg)...,
	); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
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
	writeWorkflowJSON(
		w,
		map[string]any{"runs": workflows.ProjectEventBackedDraftRunsForBrowser(runs)},
	)
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
	writeWorkflowJSON(
		w,
		workflows.ProjectWorkflowRunForBrowser(
			run,
			workflows.IsEventBackedDraftRunFamily(r.Context(), store, run),
		),
	)
}

func (h *Handler) handleGetWorkflowRunEvents(w http.ResponseWriter, r *http.Request) {
	store, err := h.workflowRunStore(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	runID := r.PathValue("run_id")
	run, err := store.GetRun(r.Context(), runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	events, err := store.Events(r.Context(), runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeWorkflowJSON(
		w,
		map[string]any{
			"run_id": runID,
			"events": workflows.ProjectWorkflowRunEventsForBrowser(
				events,
				workflows.IsEventBackedDraftRunFamily(r.Context(), store, run),
			),
		},
	)
}

func (h *Handler) handleStreamWorkflowRunEvents(w http.ResponseWriter, r *http.Request) {
	store, err := h.workflowRunStore(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	runID := r.PathValue("run_id")
	run, err := store.GetRun(r.Context(), runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	maskDiagnostics := workflows.IsEventBackedDraftRunFamily(r.Context(), store, run)
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
		events = workflows.ProjectWorkflowRunEventsForBrowser(events, maskDiagnostics)
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

func (h *Handler) handleGetWorkflowDevelopment(w http.ResponseWriter, r *http.Request) {
	workspace, err := h.workflowWorkspace()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	session, err := workflows.GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeWorkflowJSON(w, map[string]any{"session": session})
}

func (h *Handler) handleStartWorkflowDevelopment(w http.ResponseWriter, r *http.Request) {
	var req workflows.WorkflowDevelopmentStartRequest
	if err := decodeOptionalWorkflowJSON(r, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	unlock := h.tryLockWorkflowDevelopment(w)
	if unlock == nil {
		return
	}
	defer unlock()
	cfg, err := h.workflowConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	workspace := cfg.WorkspacePath()
	session, err := workflows.StartWorkflowDevelopment(
		r.Context(),
		workspace,
		h.workflowCompatibilityRuntime(r.Context()),
		req,
		workflowLocalOptionsFromConfig(cfg)...,
	)
	if err != nil {
		if errors.Is(err, workflows.ErrActiveDevelopmentExists) {
			active, activeErr := workflows.GetWorkflowDevelopmentSession(workspace)
			if activeErr != nil {
				http.Error(w, activeErr.Error(), http.StatusInternalServerError)
				return
			}
			writeWorkflowJSONStatus(
				w,
				http.StatusConflict,
				map[string]any{"error": err.Error(), "session": active},
			)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeWorkflowJSON(w, map[string]any{"session": session})
}

func (h *Handler) handleReviseWorkflowDevelopment(w http.ResponseWriter, r *http.Request) {
	var req workflows.WorkflowDevelopmentReviseRequest
	if err := decodeOptionalWorkflowJSON(r, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	unlock := h.tryLockWorkflowDevelopment(w)
	if unlock == nil {
		return
	}
	defer unlock()
	cfg, err := h.workflowConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	workspace := cfg.WorkspacePath()
	session, err := workflows.ReviseWorkflowDevelopment(
		workspace,
		req,
		workflowLocalOptionsFromConfig(cfg)...,
	)
	if err != nil {
		writeWorkflowDevelopmentError(w, err)
		return
	}
	writeWorkflowJSON(w, map[string]any{"session": session})
}

func (h *Handler) handleValidateWorkflowDevelopment(w http.ResponseWriter, r *http.Request) {
	unlock := h.tryLockWorkflowDevelopment(w)
	if unlock == nil {
		return
	}
	defer unlock()
	cfg, err := h.workflowConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	workspace := cfg.WorkspacePath()
	session, err := workflows.ValidateWorkflowDevelopment(
		workspace,
		workflowLocalOptionsFromConfig(cfg)...,
	)
	if err != nil {
		writeWorkflowDevelopmentError(w, err)
		return
	}
	writeWorkflowJSON(w, map[string]any{"session": session})
}

func (h *Handler) handleTestWorkflowDevelopment(w http.ResponseWriter, r *http.Request) {
	var req workflowDevelopmentTestRequest
	if err := decodeWorkflowDevelopmentTestRequest(w, r, &req); err != nil {
		writeWorkflowDevelopmentTestRequestError(w, err)
		return
	}
	eventID := req.EventID
	if eventID != "" {
		if !validOperatorEventID(eventID) {
			writeWorkflowEventContextError(w, errWorkflowEventInvalid)
			return
		}
		if workflowEventTestHasManualOverrides(req) {
			http.Error(
				w,
				"event-backed draft tests use server-owned inputs, secrets, session, and delivery",
				http.StatusBadRequest,
			)
			return
		}
	}
	unlock := h.tryLockWorkflowDevelopment(w)
	if unlock == nil {
		return
	}
	defer unlock()
	cfg, err := h.workflowConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	workspace := cfg.WorkspacePath()
	localOpts := workflowLocalOptionsFromConfig(cfg)
	if _, reviseErr := workflows.ReviseWorkflowDevelopment(workspace, workflows.WorkflowDevelopmentReviseRequest{
		Prompt:    req.Prompt,
		TargetRef: req.TargetRef,
		YAML:      req.YAML,
	}, localOpts...); reviseErr != nil {
		writeWorkflowDevelopmentError(w, reviseErr)
		return
	}
	session, err := workflows.ValidateWorkflowDevelopment(workspace, localOpts...)
	if err != nil {
		writeWorkflowDevelopmentError(w, err)
		return
	}
	if session.Validation == nil || !session.Validation.Valid {
		recorded, recordErr := recordWorkflowDevelopmentTestForEvent(
			workspace,
			eventID,
			nil,
			errors.New("workflow draft is not valid"),
		)
		if recordErr != nil {
			writeWorkflowDevelopmentError(w, recordErr)
			return
		}
		writeWorkflowJSONStatus(
			w,
			http.StatusBadRequest,
			map[string]any{"session": recorded, "error": "workflow draft is not valid"},
		)
		return
	}
	workflow, err := workflows.Parse([]byte(session.YAML))
	if err != nil {
		responseError := err.Error()
		if eventID != "" {
			responseError = "workflow draft is not valid"
		}
		recorded, recordErr := recordWorkflowDevelopmentTestForEvent(
			workspace,
			eventID,
			nil,
			err,
		)
		if recordErr != nil {
			writeWorkflowDevelopmentError(w, recordErr)
			return
		}
		writeWorkflowJSONStatus(
			w,
			http.StatusBadRequest,
			map[string]any{"session": recorded, "error": responseError},
		)
		return
	}
	runReq := workflows.RunRequest{
		Workflow:    workflow,
		WorkflowRef: "draft:" + session.TargetWorkflowRef,
		Inputs:      req.Inputs,
		Secrets:     req.Secrets,
		Session:     req.Session,
		Delivery:    req.Delivery,
	}
	if eventID != "" {
		envelope, loadErr := h.loadWorkflowEventEnvelope(r.Context(), eventID, true)
		if loadErr != nil {
			writeWorkflowEventContextError(w, loadErr)
			return
		}
		if workflow.On.Event == nil || !workflows.WorkflowMatchesEvent(workflow, envelope) {
			recorded, recordErr := recordWorkflowDevelopmentTestForEvent(
				workspace,
				eventID,
				nil,
				errors.New("selected event does not match workflow event trigger"),
			)
			if recordErr != nil {
				writeWorkflowDevelopmentError(w, recordErr)
				return
			}
			writeWorkflowJSONStatus(
				w,
				http.StatusBadRequest,
				map[string]any{
					"session": recorded,
					"error":   "selected event does not match workflow event trigger",
				},
			)
			return
		}
		eventContext, contextErr := workflows.EventWorkflowRunContextFromEnvelope(
			session.TargetWorkflowRef,
			"",
			envelope,
		)
		if contextErr != nil {
			writeWorkflowEventContextError(w, errWorkflowEventUnavailable)
			return
		}
		runReq.Inputs = eventContext.Inputs
		runReq.Secrets = nil
		runReq.Event = eventContext.Event
		runReq.Origin = eventContext.Origin
		runReq.Session = eventContext.Session
		runReq.Delivery = eventContext.Delivery
	}
	cfg, _, executor, err := h.workflowRuntime(r.Context())
	if err != nil {
		if eventID != "" {
			http.Error(
				w,
				"event-backed draft test is unavailable",
				http.StatusServiceUnavailable,
			)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	backgroundOwnsRuntime := false
	defer func() {
		if !backgroundOwnsRuntime {
			closeWorkflowRuntime(executor)
		}
	}()
	if !cfg.Workflows.Enabled {
		http.Error(w, "workflows are disabled", http.StatusBadRequest)
		return
	}
	if req.Async {
		backgroundOwnsRuntime = true
		asyncSessionID := session.ID
		asyncDraftKey := workflows.WorkflowDevelopmentDraftKey(session.TargetWorkflowRef, session.YAML)
		started := startWorkflowRunBackground(
			executor,
			runReq,
			func(result *workflows.RunResult, runErr error) {
				h.workflowDevelopmentMu.Lock()
				defer h.workflowDevelopmentMu.Unlock()
				_, _, _ = recordWorkflowDevelopmentTestIfCurrentForEvent(
					workspace,
					asyncSessionID,
					asyncDraftKey,
					eventID,
					result,
					runErr,
				)
			},
		)
		if started.Run != nil {
			runningResult := &workflows.RunResult{
				RunID:  started.Run.ID,
				Status: workflows.RunStatusRunning,
			}
			recorded, recordErr := recordWorkflowDevelopmentTestForEvent(
				workspace,
				eventID,
				runningResult,
				nil,
			)
			started.Release()
			if recordErr != nil {
				writeWorkflowDevelopmentError(w, recordErr)
				return
			}
			writeWorkflowJSONStatus(
				w,
				http.StatusAccepted,
				map[string]any{"session": recorded, "result": runningResult},
			)
			return
		}
		if started.Err != nil {
			publicResult, publicErr := workflowDevelopmentTestOutcomeForEvent(
				eventID,
				started.Result,
				started.Err,
			)
			recorded, recordErr := recordWorkflowDevelopmentTestForEvent(
				workspace,
				eventID,
				publicResult,
				publicErr,
			)
			if recordErr != nil {
				writeWorkflowDevelopmentError(w, recordErr)
				return
			}
			if publicResult == nil {
				writeWorkflowJSONStatus(
					w,
					http.StatusBadRequest,
					map[string]any{"session": recorded, "error": publicErr.Error()},
				)
				return
			}
			writeWorkflowJSONStatus(
				w,
				http.StatusBadRequest,
				map[string]any{"session": recorded, "result": publicResult, "error": publicErr.Error()},
			)
			return
		}
		http.Error(w, "workflow draft test did not start", http.StatusInternalServerError)
		return
	}
	result, runErr := executor.Run(r.Context(), runReq)
	result, runErr = workflowDevelopmentTestOutcomeForEvent(eventID, result, runErr)
	if runErr != nil {
		recorded, recordErr := recordWorkflowDevelopmentTestForEvent(
			workspace,
			eventID,
			result,
			runErr,
		)
		if recordErr != nil {
			writeWorkflowDevelopmentError(w, recordErr)
			return
		}
		if result == nil {
			writeWorkflowJSONStatus(
				w,
				http.StatusBadRequest,
				map[string]any{"session": recorded, "error": runErr.Error()},
			)
			return
		}
		writeWorkflowJSON(w, map[string]any{"session": recorded, "result": result, "error": runErr.Error()})
		return
	}
	recorded, recordErr := recordWorkflowDevelopmentTestForEvent(
		workspace,
		eventID,
		result,
		nil,
	)
	if recordErr != nil {
		writeWorkflowDevelopmentError(w, recordErr)
		return
	}
	writeWorkflowJSON(w, map[string]any{"session": recorded, "result": result})
}

func (h *Handler) handleDiscardWorkflowDevelopment(w http.ResponseWriter, r *http.Request) {
	unlock := h.tryLockWorkflowDevelopment(w)
	if unlock == nil {
		return
	}
	defer unlock()
	workspace, err := h.workflowWorkspace()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	session, err := workflows.DiscardWorkflowDevelopment(workspace)
	if err != nil {
		writeWorkflowDevelopmentError(w, err)
		return
	}
	writeWorkflowJSON(w, map[string]any{"session": session})
}

func (h *Handler) workflowWorkspace() (string, error) {
	cfg, err := h.workflowConfig()
	if err != nil {
		return "", err
	}
	return cfg.WorkspacePath(), nil
}

func (h *Handler) workflowConfig() (*config.Config, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return nil, fmt.Errorf("Failed to load config: %w", err)
	}
	return cfg, nil
}

func workflowLocalOptionsFromConfig(cfg *config.Config) []workflows.LocalOption {
	if cfg == nil {
		return nil
	}
	return []workflows.LocalOption{workflows.WithDefinitionsDir(cfg.Workflows.EffectiveDefinitionsDir())}
}

func (h *Handler) workflowCompatibilityRuntime(ctx context.Context) workflows.RuntimeCompatibility {
	version := h.resolveSystemVersionInfo(ctx)
	return workflows.NormalizeRuntimeCompatibility(workflows.RuntimeCompatibility{
		PicoclawVersion: version.Version,
		GitCommit:       version.GitCommit,
	})
}

func (h *Handler) workflowRuntime(
	ctx context.Context,
) (*config.Config, *workflows.FileRunStore, *workflows.Executor, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("Failed to load config: %w", err)
	}
	workspace := cfg.WorkspacePath()
	store := workflows.NewFileRunStore(workspace)
	if err := pruneWorkflowRunStore(ctx, cfg, store); err != nil {
		return nil, nil, nil, err
	}
	runners := newWorkflowRuntimeRunners(h.configPath)
	executor := &workflows.Executor{
		WorkspaceDir:         workspace,
		DefinitionsDir:       cfg.Workflows.EffectiveDefinitionsDir(),
		Store:                store,
		RuntimeCompatibility: h.workflowCompatibilityRuntime(ctx),
		MaxCallDepth:         cfg.Workflows.EffectiveMaxCallDepth(),
		MaxConcurrentRuns:    cfg.Workflows.EffectiveMaxConcurrentRuns(),
		DefaultTimeout:       cfg.Workflows.EffectiveDefaultTimeout(),
		Tools:                runners.Tools,
		Agents:               runners.Agents,
		Functions:            nil,
		RuntimeEvents:        runners.RuntimeEvents,
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

func decodeStrictWorkflowJSON(r *http.Request, dest any, optional bool) error {
	if r.Body == nil {
		if optional {
			return nil
		}
		return io.EOF
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		if optional && errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("workflow request contains multiple JSON values")
		}
		return err
	}
	return nil
}

func decodeWorkflowCancelRequest(
	w http.ResponseWriter,
	r *http.Request,
	dest *workflowCancelRequest,
) error {
	if r.Body == nil {
		return nil
	}
	decoder := json.NewDecoder(http.MaxBytesReader(
		w,
		r.Body,
		workflowCancelRequestMaxBytes,
	))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("workflow cancel request contains multiple JSON values")
		}
		return err
	}
	return nil
}

func decodeWorkflowDevelopmentTestRequest(
	w http.ResponseWriter,
	r *http.Request,
	destination *workflowDevelopmentTestRequest,
) error {
	if r.Body == nil {
		return nil
	}
	decoder := json.NewDecoder(http.MaxBytesReader(
		w,
		r.Body,
		workflowDevelopmentTestRequestMaxBytes,
	))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("workflow draft test request contains multiple JSON values")
		}
		return err
	}
	return nil
}

func writeWorkflowDevelopmentTestRequestError(w http.ResponseWriter, err error) {
	var maximum *http.MaxBytesError
	if errors.As(err, &maximum) {
		http.Error(
			w,
			"workflow draft test request exceeds 1 MiB",
			http.StatusRequestEntityTooLarge,
		)
		return
	}
	http.Error(w, "invalid workflow draft test request", http.StatusBadRequest)
}

func writeWorkflowJSON(w http.ResponseWriter, value any) {
	writeWorkflowJSONStatus(w, http.StatusOK, value)
}

func writeWorkflowJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type backgroundWorkflowStart struct {
	Run     *workflows.Run
	Result  *workflows.RunResult
	Err     error
	Release func()
}

func startWorkflowRunBackground(
	executor *workflows.Executor,
	req workflows.RunRequest,
	onComplete func(*workflows.RunResult, error),
) backgroundWorkflowStart {
	created := make(chan *workflows.Run, 1)
	completed := make(chan backgroundWorkflowStart, 1)
	release := make(chan struct{})
	if req.RunID == "" {
		req.RunID = workflows.NewRunID()
	}
	req.OnRunCreated = func(run *workflows.Run) {
		created <- run
		<-release
	}
	go func() {
		defer closeWorkflowRuntime(executor)
		result, err := executor.Run(context.Background(), req)
		if onComplete != nil {
			onComplete(result, err)
		}
		completed <- backgroundWorkflowStart{Result: result, Err: err, Release: func() {}}
	}()

	select {
	case run := <-created:
		released := false
		return backgroundWorkflowStart{
			Run: run,
			Release: func() {
				if released {
					return
				}
				released = true
				close(release)
			},
		}
	case finished := <-completed:
		close(release)
		return finished
	case <-time.After(5 * time.Second):
		close(release)
		return backgroundWorkflowStart{
			Err:     fmt.Errorf("workflow run did not start within 5 seconds"),
			Release: func() {},
		}
	}
}

func writeWorkflowDevelopmentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workflows.ErrNoActiveDevelopment):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, workflows.ErrActiveDevelopmentExists), errors.Is(err, workflows.ErrDevelopmentBusy):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func writeWorkflowEventContextError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errWorkflowEventInvalid):
		http.Error(w, "workflow event ID is invalid", http.StatusBadRequest)
	case errors.Is(err, errWorkflowEventNotFound):
		http.Error(w, "workflow event was not found", http.StatusNotFound)
	default:
		w.Header().Set("Retry-After", "1")
		http.Error(w, "workflow event service is unavailable", http.StatusServiceUnavailable)
	}
}

func workflowEventTestHasManualOverrides(req workflowDevelopmentTestRequest) bool {
	return len(req.Inputs) != 0 ||
		len(req.Secrets) != 0 ||
		req.Session != "" ||
		req.Delivery.Channel != "" ||
		req.Delivery.ChatID != "" ||
		req.Delivery.TopicID != "" ||
		req.Delivery.ThreadTS != "" ||
		req.Delivery.MessageID != "" ||
		req.Delivery.ReplyToMessageID != "" ||
		len(req.Delivery.ReplyHandles) != 0
}

func recordWorkflowDevelopmentTestForEvent(
	workspace string,
	eventID string,
	result *workflows.RunResult,
	testErr error,
) (*workflows.WorkflowDevelopmentSession, error) {
	if eventID == "" {
		return workflows.RecordWorkflowDevelopmentTest(workspace, result, testErr)
	}
	return workflows.RecordWorkflowDevelopmentEventTest(
		workspace,
		eventID,
		result,
		testErr,
	)
}

func workflowDevelopmentTestOutcomeForEvent(
	eventID string,
	result *workflows.RunResult,
	testErr error,
) (*workflows.RunResult, error) {
	if eventID == "" {
		return result, testErr
	}
	return workflows.SanitizeEventBackedDraftTestOutcome(result, testErr)
}

func recordWorkflowDevelopmentTestIfCurrentForEvent(
	workspace string,
	sessionID string,
	draftKey string,
	eventID string,
	result *workflows.RunResult,
	testErr error,
) (*workflows.WorkflowDevelopmentSession, bool, error) {
	if eventID == "" {
		return workflows.RecordWorkflowDevelopmentTestIfCurrent(
			workspace,
			sessionID,
			draftKey,
			result,
			testErr,
		)
	}
	return workflows.RecordWorkflowDevelopmentEventTestIfCurrent(
		workspace,
		sessionID,
		draftKey,
		eventID,
		result,
		testErr,
	)
}

func (h *Handler) recordCanceledWorkflowDevelopmentRun(ctx context.Context, run *workflows.Run) {
	if run == nil || run.ID == "" || run.Status != workflows.RunStatusCanceled {
		return
	}
	workspace, err := h.workflowWorkspace()
	if err != nil {
		return
	}
	h.workflowDevelopmentMu.Lock()
	defer h.workflowDevelopmentMu.Unlock()
	session, err := workflows.GetWorkflowDevelopmentSession(workspace)
	if err != nil || session == nil || session.LastTest == nil {
		return
	}
	if session.LastTest.RunID != run.ID || session.LastTest.Status != workflows.RunStatusRunning {
		return
	}
	result := &workflows.RunResult{
		RunID:  run.ID,
		Status: workflows.RunStatusCanceled,
		Error:  run.CancelReason,
	}
	_, _, recordErr := recordWorkflowDevelopmentTestIfCurrentForEvent(
		workspace,
		session.ID,
		session.LastTest.DraftKey,
		session.LastTest.EventID,
		result,
		nil,
	)
	_ = recordErr
}

func (h *Handler) tryLockWorkflowDevelopment(w http.ResponseWriter) func() {
	if !h.workflowDevelopmentMu.TryLock() {
		http.Error(w, "workflow development operation already in progress", http.StatusConflict)
		return nil
	}
	return h.workflowDevelopmentMu.Unlock
}

func closeWorkflowRuntime(executor *workflows.Executor) {
	if executor == nil {
		return
	}
	if closer, ok := executor.Agents.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
	if closer, ok := executor.Tools.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
	if closer, ok := executor.RuntimeEvents.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}
