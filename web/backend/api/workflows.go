package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
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

type workflowDevelopmentFenceRequest struct {
	SessionID               string `json:"session_id"`
	ExpectedSessionRevision string `json:"expected_session_revision"`
	ExpectedDraftRevision   string `json:"expected_draft_revision"`
}

func (request workflowDevelopmentFenceRequest) workflowFence() workflows.WorkflowDevelopmentTestDraftFence {
	return workflows.WorkflowDevelopmentTestDraftFence{
		SessionID:               request.SessionID,
		ExpectedSessionRevision: request.ExpectedSessionRevision,
		ExpectedDraftRevision:   request.ExpectedDraftRevision,
	}
}

type workflowDevelopmentReviseRequest struct {
	workflowDevelopmentFenceRequest
	Prompt     string  `json:"prompt,omitempty"`
	TargetRef  string  `json:"target_ref,omitempty"`
	YAML       *string `json:"yaml,omitempty"`
	Regenerate bool    `json:"regenerate,omitempty"`
}

func (request workflowDevelopmentReviseRequest) reviseRequest() workflows.WorkflowDevelopmentReviseRequest {
	return workflows.WorkflowDevelopmentReviseRequest{
		Prompt:     request.Prompt,
		TargetRef:  request.TargetRef,
		YAML:       request.YAML,
		Regenerate: request.Regenerate,
	}
}

const workflowDevelopmentTestRequestMaxBytes = 1 << 20

type workflowCancelRequest struct {
	Reason optionalWorkflowCancelReason `json:"reason,omitempty"`
}

type optionalWorkflowCancelReason struct {
	Value   string
	Present bool
}

var errInvalidWorkflowCancelReasonEncoding = errors.New(
	"workflow cancellation reason must contain valid UTF-8",
)

func (r *optionalWorkflowCancelReason) UnmarshalJSON(data []byte) error {
	r.Present = true
	if strings.TrimSpace(string(data)) == "null" {
		return errors.New("workflow cancellation reason must be a string")
	}
	if !utf8.Valid(data) {
		return errInvalidWorkflowCancelReasonEncoding
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
	ID           string                        `json:"id"`
	Ref          string                        `json:"ref"`
	Name         string                        `json:"name,omitempty"`
	Status       string                        `json:"status"`
	Trigger      string                        `json:"trigger"`
	Inputs       int                           `json:"inputs"`
	Secrets      int                           `json:"secrets"`
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
	h.registerWorkflowInspectionRoutes(mux)
	h.registerWorkflowAuthoringRoutes(mux)
	mux.HandleFunc("GET /api/workflows", h.handleListWorkflows)
	mux.HandleFunc("GET /api/workflows/definitions", h.handleListWorkflowDefinitions)
	mux.HandleFunc("GET /api/workflows/definitions/{id}", h.handleGetWorkflowDefinition)
	mux.HandleFunc("GET /api/workflows/settings", h.handleGetWorkflowSettings)
	mux.HandleFunc(
		"PATCH /api/workflows/settings",
		h.requireCollectionMutationOrigin(h.handlePatchWorkflowSettings),
	)
	mux.HandleFunc("GET /api/workflows/templates", h.handleListWorkflowTemplates)
	mux.HandleFunc(
		"POST /api/workflows/templates/{name}/install",
		h.requireCollectionMutationOrigin(h.handleInstallWorkflowTemplate),
	)
	mux.HandleFunc(
		"POST /api/workflows/dependencies/check",
		h.handleCheckWorkflowDependencies,
	)
	mux.HandleFunc("GET /api/workflows/compatibility", h.handleGetWorkflowCompatibility)
	mux.HandleFunc(
		"POST /api/workflows/revalidate",
		h.requireCollectionMutationOrigin(h.handleRevalidateWorkflows),
	)
	mux.HandleFunc("POST /api/workflows/validate", h.handleValidateWorkflow)
	mux.HandleFunc(
		"POST /api/workflows/reload",
		h.requireCollectionMutationOrigin(h.handleReloadWorkflows),
	)
	mux.HandleFunc(
		"POST /api/workflows/run",
		h.requireCollectionMutationOrigin(h.handleRunWorkflow),
	)
	mux.HandleFunc("GET /api/workflows/development", h.handleGetWorkflowDevelopment)
	mux.HandleFunc(
		"POST /api/workflows/development/start",
		h.requireCollectionMutationOrigin(h.handleStartWorkflowDevelopment),
	)
	mux.HandleFunc(
		"POST /api/workflows/development/revise",
		h.requireCollectionMutationOrigin(h.handleReviseWorkflowDevelopment),
	)
	mux.HandleFunc(
		"POST /api/workflows/development/ai-revise",
		h.requireCollectionMutationOrigin(h.handleAIReviseWorkflowDevelopment),
	)
	mux.HandleFunc(
		"POST /api/workflows/development/validate",
		h.requireCollectionMutationOrigin(h.handleValidateWorkflowDevelopment),
	)
	mux.HandleFunc(
		"POST /api/workflows/development/test",
		h.requireCollectionMutationOrigin(h.handleTestWorkflowDevelopment),
	)
	mux.HandleFunc(
		"POST /api/workflows/development/publish",
		h.requireCollectionMutationOrigin(h.handlePublishWorkflowDevelopment),
	)
	mux.HandleFunc(
		"POST /api/workflows/development/discard",
		h.requireCollectionMutationOrigin(h.handleDiscardWorkflowDevelopment),
	)
	mux.HandleFunc("GET /api/workflows/runs", h.handleListWorkflowRuns)
	mux.HandleFunc("GET /api/workflows/runs/{run_id}", h.handleGetWorkflowRun)
	mux.HandleFunc("GET /api/workflows/runs/{run_id}/tasks", h.handleListWorkflowHumanTasks)
	mux.HandleFunc(
		"POST /api/workflows/runs/{run_id}/tasks/{task_id}/resume",
		h.requireCollectionMutationOrigin(h.handleResumeWorkflowHumanTask),
	)
	mux.HandleFunc(
		"POST /api/workflows/runs/{run_id}/tasks/{task_id}/cancel",
		h.requireCollectionMutationOrigin(h.handleCancelWorkflowHumanTask),
	)
	mux.HandleFunc("GET /api/workflows/runs/{run_id}/events", h.handleGetWorkflowRunEvents)
	mux.HandleFunc("GET /api/workflows/runs/{run_id}/events/stream", h.handleStreamWorkflowRunEvents)
	mux.HandleFunc("GET /api/workflows/runs/{run_id}/graph", h.handleGetWorkflowRunGraph)
	mux.HandleFunc(
		"POST /api/workflows/runs/{run_id}/cancel",
		h.requireCollectionMutationOrigin(h.handleCancelWorkflowRun),
	)
	mux.HandleFunc(
		"POST /api/workflows/runs/{run_id}/retry",
		h.requireCollectionMutationOrigin(h.handleRetryWorkflowRun),
	)
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
	applyWorkflowDefinitionCompatibility(responseDefs, compatibility)
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
		id, idErr := workflows.WorkflowDefinitionID(def.Ref)
		if idErr != nil {
			return nil, idErr
		}
		response := workflowDefinitionResponse{
			ID:      id,
			Ref:     def.Ref,
			Name:    def.Name,
			Status:  workflows.WorkflowValidationStatusPendingRevalidation,
			Trigger: "none",
			Error:   def.Error,
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
				response.Inputs = len(workflow.On.WorkflowCall.Inputs)
				response.Secrets = len(workflow.On.WorkflowCall.Secrets)
			}
			response.Trigger = workflowDefinitionTriggerLabel(workflow)
			// LoadLocal returns a fresh parsed workflow owned by this response,
			// so this is a safe read-only projection with no shared runtime state.
			response.EventTrigger = workflow.On.Event
		} else {
			response.Status = workflows.WorkflowValidationStatusInvalid
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
	h.configMutationMu.Lock()
	releaseAdmission := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseAdmission()

	admission, err := h.requirePublishedWorkflowDependenciesReady(
		r.Context(),
		req.Ref,
		req.ExpectedDependencyRevision,
	)
	if err != nil {
		writeWorkflowRunDependencyError(w, err)
		return
	}
	cfg, _, executor, err := h.workflowRuntimeFromConfig(
		r.Context(),
		admission.Config,
	)
	if err != nil {
		if errors.Is(err, workflows.ErrWorkflowStorageUnavailable) {
			writeWorkflowRunDependencyError(w, errWorkflowDependencyUnavailable)
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
		writeWorkflowRunDependencyError(w, errWorkflowDependenciesNotReady)
		return
	}
	executor.WorkflowSnapshots = admission.Snapshots
	executor.AdmittedRunCreate = func(
		ctx context.Context,
		candidate *workflows.Run,
		create func() error,
	) error {
		err := workflows.WithGuardedFencedRunnableWorkflowSnapshots(
			ctx,
			cfg.WorkspacePath(),
			admission.orderedSnapshots(),
			executor.RuntimeCompatibility,
			func() error {
				if candidate == nil ||
					candidate.WorkflowRef != admission.Response.RootRef {
					return errWorkflowDependencyRevisionStale
				}
				if err := h.fenceWorkflowDependencyAdmission(
					ctx,
					admission,
					req.ExpectedDependencyRevision,
				); err != nil {
					return err
				}
				return nil
			},
			func(guarded func() error) error {
				return h.guardWorkflowDependencyAdmissionConfig(
					admission,
					guarded,
				)
			},
			func() error {
				if err := create(); err != nil {
					return err
				}
				releaseAdmission()
				return nil
			},
		)
		switch {
		case errors.Is(err, workflows.ErrWorkflowSnapshotAdmissionUnavailable),
			errors.Is(err, workflows.ErrWorkflowStorageUnavailable):
			return fmt.Errorf("%w: %v", errWorkflowDependencyUnavailable, err)
		case errors.Is(err, workflows.ErrWorkflowSnapshotsNotRunnable):
			return fmt.Errorf("%w: %v", errWorkflowDependenciesNotReady, err)
		default:
			return err
		}
	}
	runReq := workflows.RunRequest{
		Ref:      admission.Response.RootRef,
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
			if isWorkflowRunDependencyError(started.Err) {
				writeWorkflowRunDependencyError(w, started.Err)
				return
			}
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
		if isWorkflowRunDependencyError(runErr) {
			writeWorkflowRunDependencyError(w, runErr)
			return
		}
		writeWorkflowJSONStatus(w, http.StatusBadRequest, map[string]any{"result": result, "error": runErr.Error()})
		return
	}
	writeWorkflowJSON(w, result)
}

func (h *Handler) handleCancelWorkflowRun(w http.ResponseWriter, r *http.Request) {
	if !validWorkflowRunResourceID(r.PathValue("run_id")) {
		writeWorkflowRunNotFound(w)
		return
	}
	var req workflowCancelRequest
	if err := decodeWorkflowCancelRequest(w, r, &req); err != nil {
		if errors.Is(err, errInvalidWorkflowCancelReasonEncoding) {
			writeWorkflowJSONStatus(
				w,
				http.StatusBadRequest,
				map[string]any{"error": "invalid_cancel_reason"},
			)
			return
		}
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
	privacy, allowed := workflowRunMutationSnapshot(
		r.Context(),
		store,
		r.PathValue("run_id"),
	)
	if !allowed {
		writeWorkflowRunNotFound(w)
		return
	}
	run, err := executor.CancelRun(r.Context(), r.PathValue("run_id"), reason)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	h.recordCanceledWorkflowDevelopmentRun(r.Context(), run)
	projected := privacy.projectRunForBrowser(r.Context(), store, run)
	if projected == nil {
		writeWorkflowRunNotFound(w)
		return
	}
	writeWorkflowJSON(w, workflowRunCollectionItemFromRun(*projected))
}

func (h *Handler) handleRetryWorkflowRun(w http.ResponseWriter, r *http.Request) {
	if !validWorkflowRunResourceID(r.PathValue("run_id")) {
		writeWorkflowRunNotFound(w)
		return
	}
	var req workflowRetryRequest
	if err := decodeStrictWorkflowJSON(r, &req, true); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	h.configMutationMu.Lock()
	releaseAdmission := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseAdmission()

	admissionConfig, admissionConfigRevision, err := loadStableWorkflowDependencyConfig(
		h.configPath,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	store := workflows.NewFileRunStore(admissionConfig.WorkspacePath())
	if pruneErr := pruneWorkflowRunStore(
		r.Context(),
		admissionConfig,
		store,
	); pruneErr != nil {
		http.Error(w, pruneErr.Error(), http.StatusInternalServerError)
		return
	}
	previousRun, err := store.GetRun(r.Context(), r.PathValue("run_id"))
	if err != nil {
		writeWorkflowRunNotFound(w)
		return
	}
	if isPrivateInternalWorkflowRun(previousRun) {
		writeWorkflowRunNotFound(w)
		return
	}
	if _, allowed := workflowRunMutationSnapshot(
		r.Context(),
		store,
		previousRun.ID,
	); !allowed {
		writeWorkflowRunNotFound(w)
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
	if workflows.IsPrivateWorkflowRun(previousRun) {
		if strings.TrimSpace(req.ExpectedDependencyRevision) != "" {
			writeWorkflowRunDependencyError(w, errWorkflowDependencyRevisionStale)
			return
		}
		cfg, _, executor, runtimeErr := h.workflowRuntimeFromConfig(
			r.Context(),
			admissionConfig,
		)
		if runtimeErr != nil {
			writeWorkflowRunDependencyError(w, errWorkflowDependencyUnavailable)
			return
		}
		defer closeWorkflowRuntime(executor)
		if !cfg.Workflows.Enabled {
			writeWorkflowRunDependencyError(w, errWorkflowDependenciesNotReady)
			return
		}
		configFence := &workflowDependencyAdmission{
			ConfigRevision: admissionConfigRevision,
		}
		executor.AdmittedRunCreate = func(
			_ context.Context,
			candidate *workflows.Run,
			create func() error,
		) error {
			admissionErr := h.guardWorkflowDependencyAdmissionConfig(configFence, func() error {
				if candidate == nil || !workflows.IsPrivateWorkflowRun(candidate) ||
					candidate.WorkflowRef != previousRun.WorkflowRef ||
					candidate.RetryOfRunID != previousRun.ID {
					return workflows.ErrRunAdmissionConflict
				}
				if createErr := create(); createErr != nil {
					return createErr
				}
				releaseAdmission()
				return nil
			})
			if errors.Is(admissionErr, errWorkflowDependencyRevisionStale) {
				return workflows.ErrRunAdmissionConflict
			}
			if errors.Is(admissionErr, workflows.ErrWorkflowSnapshotAdmissionUnavailable) ||
				errors.Is(admissionErr, workflows.ErrWorkflowStorageUnavailable) {
				return workflows.ErrRunAdmissionUnavailable
			}
			return admissionErr
		}
		result, runErr := executor.RetryCaptured(r.Context(), previousRun, req.Secrets)
		if runErr != nil {
			if errors.Is(runErr, workflows.ErrRunAdmissionConflict) {
				writeWorkflowRunDependencyError(w, errWorkflowDependencyRevisionStale)
				return
			}
			if errors.Is(runErr, workflows.ErrRunAdmissionUnavailable) {
				writeWorkflowRunDependencyError(w, errWorkflowDependencyUnavailable)
				return
			}
			if isWorkflowRunDependencyError(runErr) {
				writeWorkflowRunDependencyError(w, runErr)
				return
			}
			writeWorkflowJSONStatus(
				w,
				http.StatusBadRequest,
				map[string]any{"result": result, "error": runErr.Error()},
			)
			return
		}
		writeWorkflowJSON(w, result)
		return
	}
	admission, dependencyErr := h.requirePublishedWorkflowDependenciesReadyFromConfig(
		r.Context(),
		admissionConfig,
		admissionConfigRevision,
		previousRun.WorkflowRef,
		req.ExpectedDependencyRevision,
	)
	if dependencyErr != nil {
		writeWorkflowRunDependencyError(w, dependencyErr)
		return
	}
	cfg, _, executor, err := h.workflowRuntimeFromConfig(
		r.Context(),
		admission.Config,
	)
	if err != nil {
		if errors.Is(err, workflows.ErrWorkflowStorageUnavailable) {
			writeWorkflowRunDependencyError(w, errWorkflowDependencyUnavailable)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer closeWorkflowRuntime(executor)
	if !cfg.Workflows.Enabled {
		writeWorkflowRunDependencyError(w, errWorkflowDependenciesNotReady)
		return
	}
	executor.WorkflowSnapshots = admission.Snapshots
	executor.AdmittedRunCreate = func(
		ctx context.Context,
		candidate *workflows.Run,
		create func() error,
	) error {
		err := workflows.WithGuardedFencedRunnableWorkflowSnapshots(
			ctx,
			cfg.WorkspacePath(),
			admission.orderedSnapshots(),
			executor.RuntimeCompatibility,
			func() error {
				if candidate == nil ||
					candidate.WorkflowRef != admission.Response.RootRef ||
					candidate.RetryOfRunID != previousRun.ID {
					return errWorkflowDependencyRevisionStale
				}
				if err := h.fenceWorkflowDependencyAdmission(
					ctx,
					admission,
					req.ExpectedDependencyRevision,
				); err != nil {
					return err
				}
				return nil
			},
			func(guarded func() error) error {
				return h.guardWorkflowDependencyAdmissionConfig(
					admission,
					guarded,
				)
			},
			func() error {
				if err := create(); err != nil {
					return err
				}
				releaseAdmission()
				return nil
			},
		)
		switch {
		case errors.Is(err, workflows.ErrWorkflowSnapshotAdmissionUnavailable):
			return fmt.Errorf("%w: %v", errWorkflowDependencyUnavailable, err)
		case errors.Is(err, workflows.ErrWorkflowSnapshotsNotRunnable):
			return fmt.Errorf("%w: %v", errWorkflowDependenciesNotReady, err)
		default:
			return err
		}
	}
	result, runErr := executor.RetryCaptured(r.Context(), previousRun, req.Secrets)
	if runErr != nil {
		if isWorkflowRunDependencyError(runErr) {
			writeWorkflowRunDependencyError(w, runErr)
			return
		}
		writeWorkflowJSONStatus(w, http.StatusBadRequest, map[string]any{"result": result, "error": runErr.Error()})
		return
	}
	writeWorkflowJSON(w, result)
}

func (h *Handler) handleListWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	request, ok := parseCollectionListRequest(w, r, workflowRunCollectionSchema)
	if !ok {
		return
	}
	store, err := h.workflowRunStore(r.Context())
	if err != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "workflow_runs_unavailable",
			"Failed to load workflow runs", -1, nil,
		)
		return
	}
	runs, err := store.ListRuns(r.Context())
	if err != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "workflow_runs_unavailable",
			"Failed to load workflow runs", -1, nil,
		)
		return
	}
	privacy := newWorkflowRunPrivacySnapshot(runs)
	// Privacy projection must precede filtering, total calculation, ordering,
	// and cursor creation so hidden run identities cannot affect page metadata.
	projected := privacy.projectRunsForBrowser(r.Context(), store, runs)
	items := make([]workflowRunCollectionSummary, len(projected))
	for index := range projected {
		items[index] = workflowRunCollectionSummaryFromRun(projected[index])
	}
	page, err := pageWorkflowRuns(items, request)
	if err != nil {
		writeCollectionPageError(w, err)
		return
	}
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"runs":            page.Items,
		"total":           page.Total,
		"next_cursor":     page.NextCursor,
		"canonical_query": request.Query.Canonical(),
		"query_schema":    workflowRunSchemaWithSuggestions(items),
	})
}

func (h *Handler) handleGetWorkflowRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	if !validWorkflowRunResourceID(runID) {
		writeCollectionError(
			w, http.StatusBadRequest, "invalid_workflow_run_id",
			"Invalid workflow run ID", -1, nil,
		)
		return
	}
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	store, err := h.workflowRunStore(r.Context())
	if err != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "workflow_runs_unavailable",
			"Failed to load workflow run", -1, nil,
		)
		return
	}
	run, err := store.GetRun(r.Context(), runID)
	if err != nil || isPrivateInternalWorkflowRun(run) {
		writeCollectionError(
			w, http.StatusNotFound, "workflow_run_not_found",
			"Workflow run not found", -1, nil,
		)
		return
	}
	privacy, err := loadWorkflowRunPrivacySnapshot(r.Context(), store)
	if err != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "workflow_runs_unavailable",
			"Failed to load workflow run", -1, nil,
		)
		return
	}
	projected := privacy.projectRunForBrowser(r.Context(), store, run)
	if projected == nil {
		writeCollectionError(
			w, http.StatusNotFound, "workflow_run_not_found",
			"Workflow run not found", -1, nil,
		)
		return
	}
	writeCollectionJSON(
		w,
		http.StatusOK,
		workflowRunCollectionItemFromRun(*projected),
	)
}

func (h *Handler) handleGetWorkflowRunEvents(w http.ResponseWriter, r *http.Request) {
	if !validWorkflowRunResourceID(r.PathValue("run_id")) {
		writeWorkflowRunNotFound(w)
		return
	}
	store, err := h.workflowRunStore(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	runID := r.PathValue("run_id")
	run, err := store.GetRun(r.Context(), runID)
	if err != nil || isPrivateInternalWorkflowRun(run) {
		writeWorkflowRunNotFound(w)
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
			"events": workflows.ProjectRepositoryReviewRunEventsForBrowser(
				run,
				events,
				workflows.IsEventBackedDraftRunFamily(r.Context(), store, run),
				workflows.IsPrivateWorkflowRun(run),
			),
		},
	)
}

func (h *Handler) handleStreamWorkflowRunEvents(w http.ResponseWriter, r *http.Request) {
	if !validWorkflowRunResourceID(r.PathValue("run_id")) {
		writeWorkflowRunNotFound(w)
		return
	}
	store, err := h.workflowRunStore(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	runID := r.PathValue("run_id")
	run, err := store.GetRun(r.Context(), runID)
	if err != nil || isPrivateInternalWorkflowRun(run) {
		writeWorkflowRunNotFound(w)
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
		current, currentErr := store.GetRun(r.Context(), runID)
		if currentErr != nil || isPrivateInternalWorkflowRun(current) {
			return
		}
		run = current
		maskDiagnostics := workflows.IsEventBackedDraftRunFamily(
			r.Context(),
			store,
			run,
		)
		events, err := store.Events(r.Context(), runID)
		if err != nil {
			return
		}
		events = workflows.ProjectRepositoryReviewRunEventsForBrowser(
			run,
			events,
			maskDiagnostics,
			workflows.IsPrivateWorkflowRun(run),
		)
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
	if !validWorkflowRunResourceID(r.PathValue("run_id")) {
		writeWorkflowRunNotFound(w)
		return
	}
	store, err := h.workflowRunStore(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !workflowRunVisible(r.Context(), store, r.PathValue("run_id")) {
		writeWorkflowRunNotFound(w)
		return
	}
	graph, err := workflows.BuildRunGraph(r.Context(), store, r.PathValue("run_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	privacy, err := loadWorkflowRunPrivacySnapshot(r.Context(), store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	graph = projectWorkflowRunGraphWithoutAttention(graph, privacy)
	writeWorkflowJSON(w, graph)
}

func projectWorkflowRunGraphWithoutAttention(
	graph *workflows.RunGraph,
	privacy *workflowRunPrivacySnapshot,
) *workflows.RunGraph {
	if graph == nil {
		return nil
	}
	hidden := make(map[string]struct{})
	if privacy != nil {
		for runID := range privacy.hiddenIDs {
			hidden[runID] = struct{}{}
		}
	}
	for _, node := range graph.Nodes {
		if isPrivateInternalWorkflowRun(&workflows.Run{WorkflowRef: node.WorkflowRef}) {
			hidden[strings.TrimSpace(node.ID)] = struct{}{}
		}
	}
	nodes := make([]workflows.RunGraphNode, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if _, omit := hidden[strings.TrimSpace(node.ID)]; omit {
			continue
		}
		if _, omit := hidden[strings.TrimSpace(node.ParentRunID)]; omit {
			node.ParentRunID = ""
			node.CallerJobID = ""
		}
		if _, omit := hidden[strings.TrimSpace(node.RetryOfRunID)]; omit {
			node.RetryOfRunID = ""
		}
		nodes = append(nodes, node)
	}
	edges := make([]workflows.RunGraphEdge, 0, len(graph.Edges))
	for _, edge := range graph.Edges {
		if _, omit := hidden[strings.TrimSpace(edge.From)]; omit {
			continue
		}
		if _, omit := hidden[strings.TrimSpace(edge.To)]; omit {
			continue
		}
		edges = append(edges, edge)
	}
	projected := *graph
	projected.Nodes = nodes
	projected.Edges = edges
	return &projected
}

func workflowRunVisible(
	ctx context.Context,
	store workflows.RunStore,
	runID string,
) bool {
	if store == nil {
		return false
	}
	run, err := store.GetRun(ctx, runID)
	return err == nil && !isPrivateInternalWorkflowRun(run)
}

func writeWorkflowRunNotFound(w http.ResponseWriter) {
	http.Error(w, "workflow run not found", http.StatusNotFound)
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
	payload := map[string]any{
		"session": projectWorkflowDevelopmentSession(session),
	}
	if session != nil &&
		session.LastTest != nil &&
		workflowDevelopmentTestStatusIsActive(session.LastTest.Status) {
		reconciledSession, reconciliation := h.reconcileRunningWorkflowDevelopmentTest(
			r.Context(),
			workspace,
			session,
		)
		payload["session"] = projectWorkflowDevelopmentSession(reconciledSession)
		if reconciliation != nil {
			payload["reconciliation"] = reconciliation
		}
	}
	writeWorkflowJSON(w, payload)
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
				map[string]any{
					"error":   err.Error(),
					"session": projectWorkflowDevelopmentSession(active),
				},
			)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeWorkflowJSON(w, map[string]any{
		"session": projectWorkflowDevelopmentSession(session),
	})
}

func (h *Handler) handleReviseWorkflowDevelopment(w http.ResponseWriter, r *http.Request) {
	var req workflowDevelopmentReviseRequest
	if !decodeWorkflowDevelopmentMutationJSON(w, r, &req) {
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
	session, err := workflows.ReviseWorkflowDevelopmentFenced(
		workspace,
		req.workflowFence(),
		req.reviseRequest(),
		workflowLocalOptionsFromConfig(cfg)...,
	)
	if err != nil {
		writeWorkflowDevelopmentMutationError(w, err)
		return
	}
	writeWorkflowJSON(w, map[string]any{
		"session": projectWorkflowDevelopmentSession(session),
	})
}

func (h *Handler) handleValidateWorkflowDevelopment(w http.ResponseWriter, r *http.Request) {
	var req workflowDevelopmentFenceRequest
	if !decodeWorkflowDevelopmentMutationJSON(w, r, &req) {
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
	session, err := workflows.ValidateWorkflowDevelopmentFenced(
		workspace,
		req.workflowFence(),
		workflowLocalOptionsFromConfig(cfg)...,
	)
	if err != nil {
		writeWorkflowDevelopmentMutationError(w, err)
		return
	}
	writeWorkflowJSON(w, map[string]any{
		"session": projectWorkflowDevelopmentSession(session),
	})
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
			map[string]any{
				"session": projectWorkflowDevelopmentSession(recorded),
				"error":   "workflow draft is not valid",
			},
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
			map[string]any{
				"session": projectWorkflowDevelopmentSession(recorded),
				"error":   responseError,
			},
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
					"session": projectWorkflowDevelopmentSession(recorded),
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
		asyncRunID := workflows.NewRunID()
		runReq.RunID = asyncRunID
		var initialStateRecorded atomic.Bool
		started := startWorkflowRunBackground(
			executor,
			runReq,
			func(result *workflows.RunResult, runErr error) {
				h.reconcileWorkflowDevelopmentTestCompletion(
					workspace,
					asyncSessionID,
					asyncDraftKey,
					eventID,
					asyncRunID,
					result,
					runErr,
					initialStateRecorded.Load(),
				)
			},
		)
		if started.Run != nil {
			runningResult := &workflows.RunResult{
				RunID:  started.Run.ID,
				Status: workflows.RunStatusRunning,
			}
			writeAcceptedWorkflowDevelopmentTestRun(
				w,
				started,
				session,
				runningResult,
				&initialStateRecorded,
				func() (*workflows.WorkflowDevelopmentSession, bool, error) {
					recorded, recordErr := recordWorkflowDevelopmentTestForEvent(
						workspace,
						eventID,
						runningResult,
						nil,
					)
					return recorded, recordErr == nil, recordErr
				},
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
					map[string]any{
						"session": projectWorkflowDevelopmentSession(recorded),
						"error":   publicErr.Error(),
					},
				)
				return
			}
			writeWorkflowJSONStatus(
				w,
				http.StatusBadRequest,
				map[string]any{
					"session": projectWorkflowDevelopmentSession(recorded),
					"result":  publicResult,
					"error":   publicErr.Error(),
				},
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
				map[string]any{
					"session": projectWorkflowDevelopmentSession(recorded),
					"error":   runErr.Error(),
				},
			)
			return
		}
		writeWorkflowJSON(w, map[string]any{
			"session": projectWorkflowDevelopmentSession(recorded),
			"result":  result,
			"error":   runErr.Error(),
		})
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
	writeWorkflowJSON(w, map[string]any{
		"session": projectWorkflowDevelopmentSession(recorded),
		"result":  result,
	})
}

func (h *Handler) handleDiscardWorkflowDevelopment(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	var request workflows.WorkflowDevelopmentDiscardRequest
	if !decodeCollectionJSON(w, r, &request) {
		return
	}
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
	session, err := workflows.DiscardWorkflowDevelopmentFenced(
		workspace,
		request,
	)
	if err != nil {
		switch {
		case errors.Is(err, workflows.ErrNoActiveDevelopment):
			writeCollectionError(
				w, http.StatusNotFound, "workflow_development_not_found",
				"Workflow development session not found", -1, nil,
			)
		case errors.Is(err, workflows.ErrWorkflowSessionRevisionMismatch):
			writeCollectionError(
				w, http.StatusConflict, "session_revision_mismatch",
				"Workflow development session changed", -1, nil,
			)
		default:
			writeCollectionError(
				w, http.StatusInternalServerError, "workflow_discard_failed",
				"Failed to discard workflow development session", -1, nil,
			)
		}
		return
	}
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"session": projectWorkflowDevelopmentSession(session),
	})
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
	return h.workflowRuntimeWithRunners(
		ctx,
		cfg,
		newWorkflowRuntimeRunners(h.configPath),
	)
}

func (h *Handler) workflowRuntimeFromConfig(
	ctx context.Context,
	cfg *config.Config,
) (*config.Config, *workflows.FileRunStore, *workflows.Executor, error) {
	return h.workflowRuntimeWithRunners(
		ctx,
		cfg,
		workflowRuntimeRunnersForConfig(h.configPath, cfg),
	)
}

func (h *Handler) workflowRuntimeFromConfigWithoutPrune(
	ctx context.Context,
	cfg *config.Config,
) (*config.Config, *workflows.FileRunStore, *workflows.Executor, error) {
	return h.workflowRuntimeWithRunnersMode(
		ctx,
		cfg,
		workflowRuntimeRunnersForConfig(h.configPath, cfg),
		false,
	)
}

func (h *Handler) workflowRuntimeWithRunners(
	ctx context.Context,
	cfg *config.Config,
	runners workflowRuntimeRunners,
) (*config.Config, *workflows.FileRunStore, *workflows.Executor, error) {
	return h.workflowRuntimeWithRunnersMode(ctx, cfg, runners, true)
}

func (h *Handler) workflowRuntimeWithRunnersMode(
	ctx context.Context,
	cfg *config.Config,
	runners workflowRuntimeRunners,
	prune bool,
) (*config.Config, *workflows.FileRunStore, *workflows.Executor, error) {
	if cfg == nil {
		return nil, nil, nil, fmt.Errorf("workflow config is required")
	}
	workspace := cfg.WorkspacePath()
	store := workflows.NewFileRunStore(workspace)
	if prune {
		if err := pruneWorkflowRunStore(ctx, cfg, store); err != nil {
			return nil, nil, nil, err
		}
	}
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
	olderThan := time.Now().UTC().AddDate(0, 0, -days)
	runs, err := store.ListRuns(ctx)
	if err != nil {
		return err
	}
	for index := range runs {
		run := &runs[index]
		if isPrivateInternalWorkflowRun(run) || !terminalBrowserWorkflowRun(run.Status) {
			continue
		}
		completedAt := run.UpdatedAt
		if run.CompletedAt != nil && !run.CompletedAt.IsZero() {
			completedAt = *run.CompletedAt
		}
		if completedAt.Before(olderThan) {
			if err = store.DeleteRun(ctx, run.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func terminalBrowserWorkflowRun(status string) bool {
	switch status {
	case workflows.RunStatusSucceeded, workflows.RunStatusFailed,
		workflows.RunStatusCanceled, workflows.RunStatusSkipped:
		return true
	default:
		return false
	}
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

func decodeWorkflowDevelopmentMutationJSON(
	w http.ResponseWriter,
	r *http.Request,
	dest any,
) bool {
	return decodeCollectionJSON(w, r, dest)
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
	data, err := io.ReadAll(http.MaxBytesReader(
		w,
		r.Body,
		workflowCancelRequestMaxBytes,
	))
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed[0] != '{' {
		return errors.New("workflow cancel request must be a JSON object")
	}
	requestDecoder := json.NewDecoder(bytes.NewReader(raw))
	requestDecoder.DisallowUnknownFields()
	if err := requestDecoder.Decode(dest); err != nil {
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

var workflowBackgroundStartTimeout = 5 * time.Second

const (
	workflowDevelopmentTestRecordAttempts   = 3
	workflowDevelopmentTestRecordRetryDelay = 10 * time.Millisecond
)

type workflowDevelopmentTestRecordOperation func() (
	*workflows.WorkflowDevelopmentSession,
	bool,
	error,
)

type workflowDevelopmentTestReconciliation struct {
	State   string `json:"state"`
	Reason  string `json:"reason"`
	RunID   string `json:"run_id"`
	Message string `json:"message"`
}

func retryWorkflowDevelopmentTestRecord(
	operation workflowDevelopmentTestRecordOperation,
) (*workflows.WorkflowDevelopmentSession, bool, error) {
	var (
		session  *workflows.WorkflowDevelopmentSession
		recorded bool
		err      error
	)
	for attempt := 0; attempt < workflowDevelopmentTestRecordAttempts; attempt++ {
		session, recorded, err = operation()
		if err == nil {
			return session, recorded, nil
		}
		if attempt+1 < workflowDevelopmentTestRecordAttempts {
			time.Sleep(workflowDevelopmentTestRecordRetryDelay)
		}
	}
	return session, recorded, err
}

func writeAcceptedWorkflowDevelopmentTestRun(
	w http.ResponseWriter,
	started backgroundWorkflowStart,
	fallbackSession *workflows.WorkflowDevelopmentSession,
	runningResult *workflows.RunResult,
	initialStateRecorded *atomic.Bool,
	record workflowDevelopmentTestRecordOperation,
) {
	recordedSession, _, recordErr := retryWorkflowDevelopmentTestRecord(record)
	initialStateRecorded.Store(recordErr == nil)
	started.Release()

	payload := map[string]any{
		"session": projectWorkflowDevelopmentSession(fallbackSession),
		"result":  runningResult,
	}
	if recordErr == nil {
		payload["session"] = projectWorkflowDevelopmentSession(recordedSession)
	} else {
		runID := ""
		if runningResult != nil {
			runID = runningResult.RunID
		}
		payload["reconciliation"] = workflowDevelopmentTestReconciliation{
			State:   "degraded",
			Reason:  "draft_test_snapshot_not_recorded",
			RunID:   runID,
			Message: "the workflow run was created, but its development snapshot could not be recorded; inspect the durable run and run a current draft test before publishing",
		}
		logger.ErrorCF(
			"workflows",
			"failed to record accepted workflow development test",
			map[string]any{
				"run_id": runID,
				"error":  recordErr.Error(),
			},
		)
	}
	writeWorkflowJSONStatus(w, http.StatusAccepted, payload)
}

func startWorkflowRunBackground(
	executor *workflows.Executor,
	req workflows.RunRequest,
	onComplete func(*workflows.RunResult, error),
) backgroundWorkflowStart {
	created := make(chan *workflows.Run, 1)
	completed := make(chan backgroundWorkflowStart, 1)
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	if req.RunID == "" {
		req.RunID = workflows.NewRunID()
	}
	req.OnRunCreated = func(run *workflows.Run) {
		created <- run
		<-release
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	go func() {
		result, err := executor.Run(runCtx, req)
		closeWorkflowRuntime(executor)
		cancelRun()
		// Publish executor completion independently of caller callbacks. A
		// callback may need a mutex still held by the accepting HTTP handler.
		completed <- backgroundWorkflowStart{Result: result, Err: err, Release: func() {}}
	}()

	accepted := func(
		run *workflows.Run,
		finished backgroundWorkflowStart,
		completionKnown bool,
	) backgroundWorkflowStart {
		callbackRelease := make(chan struct{})
		releaseAccepted := sync.OnceFunc(func() {
			releaseOnce()
			close(callbackRelease)
		})
		if onComplete != nil {
			go func() {
				if !completionKnown {
					finished = <-completed
				}
				// Accepted callers record the running state before Release.
				<-callbackRelease
				onComplete(finished.Result, finished.Err)
			}()
		}
		return backgroundWorkflowStart{
			Run:     run,
			Release: releaseAccepted,
		}
	}
	acceptedFromFinished := func(
		finished backgroundWorkflowStart,
	) (backgroundWorkflowStart, bool) {
		if finished.Result == nil ||
			strings.TrimSpace(finished.Result.RunID) == "" {
			return backgroundWorkflowStart{}, false
		}
		return accepted(
			&workflows.Run{
				ID:     finished.Result.RunID,
				Status: finished.Result.Status,
			},
			finished,
			true,
		), true
	}

	timer := time.NewTimer(workflowBackgroundStartTimeout)
	defer timer.Stop()
	select {
	case run := <-created:
		return accepted(run, backgroundWorkflowStart{}, false)
	case finished := <-completed:
		if started, durable := acceptedFromFinished(finished); durable {
			return started
		}
		releaseOnce()
		return finished
	case <-timer.C:
		// Do not let a timed-out starter create a durable run after the HTTP
		// response. Cancellation is checked at the executor's create closure,
		// and joining completed proves the background starter has stopped.
		cancelRun()
		releaseOnce()
		select {
		case run := <-created:
			return accepted(run, backgroundWorkflowStart{}, false)
		case finished := <-completed:
			select {
			case run := <-created:
				// Some terminal persistence errors have no RunResult. The
				// create callback is still authoritative durable proof.
				return accepted(run, finished, true)
			default:
			}
			if started, durable := acceptedFromFinished(finished); durable {
				return started
			}
			return backgroundWorkflowStart{
				Err:     fmt.Errorf("workflow run did not start within 5 seconds"),
				Release: func() {},
			}
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

func writeWorkflowDevelopmentMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workflows.ErrNoActiveDevelopment):
		writeCollectionError(
			w, http.StatusNotFound, "workflow_development_not_found",
			"Workflow development session not found", -1, nil,
		)
	case errors.Is(err, workflows.ErrWorkflowDevelopmentFenceMismatch):
		writeCollectionError(
			w, http.StatusConflict, "workflow_development_fence_mismatch",
			"Workflow development session changed", -1, nil,
		)
	case errors.Is(err, workflows.ErrActiveDevelopmentExists),
		errors.Is(err, workflows.ErrDevelopmentBusy):
		writeCollectionError(
			w, http.StatusConflict, "workflow_development_busy",
			"Workflow development operation is already in progress", -1, nil,
		)
	default:
		writeCollectionError(
			w, http.StatusBadRequest, "workflow_development_invalid",
			"Workflow development request is invalid", -1, nil,
		)
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
	expectedRunID string,
	result *workflows.RunResult,
	testErr error,
) (*workflows.WorkflowDevelopmentSession, bool, error) {
	if eventID == "" {
		return workflows.RecordWorkflowDevelopmentTestIfCurrent(
			workspace,
			sessionID,
			draftKey,
			expectedRunID,
			result,
			testErr,
		)
	}
	return workflows.RecordWorkflowDevelopmentEventTestIfCurrent(
		workspace,
		sessionID,
		draftKey,
		eventID,
		expectedRunID,
		result,
		testErr,
	)
}

func (h *Handler) reconcileWorkflowDevelopmentTestCompletion(
	workspace string,
	sessionID string,
	draftKey string,
	eventID string,
	expectedRunID string,
	result *workflows.RunResult,
	runErr error,
	initialStateRecorded bool,
) {
	defer func() {
		if h.workflowDevelopmentTestDone != nil {
			h.workflowDevelopmentTestDone()
		}
	}()
	result, runErr = terminalWorkflowDevelopmentTestOutcome(
		expectedRunID,
		result,
		runErr,
	)
	h.workflowDevelopmentMu.Lock()
	_, recorded, recordErr := retryWorkflowDevelopmentTestRecord(
		func() (*workflows.WorkflowDevelopmentSession, bool, error) {
			return recordWorkflowDevelopmentTestIfCurrentForEvent(
				workspace,
				sessionID,
				draftKey,
				eventID,
				expectedRunID,
				result,
				runErr,
			)
		},
	)
	h.workflowDevelopmentMu.Unlock()
	if recordErr != nil {
		logger.ErrorCF(
			"workflows",
			"failed to reconcile terminal workflow development test",
			map[string]any{
				"run_id": expectedRunID,
				"error":  recordErr.Error(),
			},
		)
		return
	}
	if !recorded && !initialStateRecorded {
		logger.ErrorCF(
			"workflows",
			"terminal workflow development test could not claim an unrecorded initial snapshot",
			map[string]any{"run_id": expectedRunID},
		)
	}
}

func terminalWorkflowDevelopmentTestOutcome(
	expectedRunID string,
	result *workflows.RunResult,
	runErr error,
) (*workflows.RunResult, error) {
	if result == nil {
		result = &workflows.RunResult{
			RunID:  expectedRunID,
			Status: workflows.RunStatusFailed,
		}
		if runErr == nil {
			runErr = errors.New("workflow draft test ended without a terminal result")
		}
		return result, runErr
	}
	cloned := *result
	cloned.Outputs = cloneWorkflowMap(result.Outputs)
	result = &cloned
	if strings.TrimSpace(result.RunID) == "" {
		result.RunID = expectedRunID
	}
	if result.Status == "" || result.Status == workflows.RunStatusRunning {
		result.Status = workflows.RunStatusFailed
		if runErr == nil {
			runErr = errors.New("workflow draft test ended without a terminal status")
		}
	}
	return result, runErr
}

func (h *Handler) reconcileRunningWorkflowDevelopmentTest(
	ctx context.Context,
	workspace string,
	session *workflows.WorkflowDevelopmentSession,
) (*workflows.WorkflowDevelopmentSession, *workflowDevelopmentTestReconciliation) {
	if session == nil ||
		session.LastTest == nil ||
		!workflowDevelopmentTestStatusIsActive(session.LastTest.Status) ||
		strings.TrimSpace(session.LastTest.RunID) == "" {
		return session, nil
	}
	runID := session.LastTest.RunID
	run, runErr := workflows.NewFileRunStore(workspace).GetRun(ctx, runID)
	if runErr != nil {
		return session, &workflowDevelopmentTestReconciliation{
			State:   "degraded",
			Reason:  "draft_test_run_unavailable",
			RunID:   runID,
			Message: "the running development snapshot could not be reconciled with its durable workflow run",
		}
	}
	if workflowDevelopmentTestStatusIsActive(run.Status) &&
		run.Status == session.LastTest.Status {
		return session, nil
	}
	result := &workflows.RunResult{
		RunID:   run.ID,
		Status:  run.Status,
		Outputs: cloneWorkflowMap(run.Outputs),
		Error:   run.Error,
	}
	var terminalErr error
	if strings.TrimSpace(run.Error) != "" {
		terminalErr = errors.New(run.Error)
	} else if run.Status == workflows.RunStatusCanceled {
		reason := strings.TrimSpace(run.CancelReason)
		if reason == "" {
			reason = "workflow draft test was canceled"
		}
		result.Error = reason
		terminalErr = errors.New(reason)
	}

	h.workflowDevelopmentMu.Lock()
	reconciled, _, recordErr := retryWorkflowDevelopmentTestRecord(
		func() (*workflows.WorkflowDevelopmentSession, bool, error) {
			return recordWorkflowDevelopmentTestIfCurrentForEvent(
				workspace,
				session.ID,
				session.LastTest.DraftKey,
				session.LastTest.EventID,
				runID,
				result,
				terminalErr,
			)
		},
	)
	h.workflowDevelopmentMu.Unlock()
	if recordErr == nil {
		return reconciled, nil
	}

	logger.ErrorCF(
		"workflows",
		"failed to reconcile polled terminal workflow development test",
		map[string]any{
			"run_id": runID,
			"error":  recordErr.Error(),
		},
	)
	return projectWorkflowDevelopmentReconciliationFailure(session, runID),
		&workflowDevelopmentTestReconciliation{
			State:   "degraded",
			Reason:  "draft_test_terminal_snapshot_not_recorded",
			RunID:   runID,
			Message: "the workflow run is terminal, but its development snapshot could not be recorded; refresh will retry reconciliation",
		}
}

func projectWorkflowDevelopmentReconciliationFailure(
	session *workflows.WorkflowDevelopmentSession,
	runID string,
) *workflows.WorkflowDevelopmentSession {
	if session == nil {
		return nil
	}
	projected := *session
	if session.LastTest != nil {
		lastTest := *session.LastTest
		lastTest.RunID = runID
		lastTest.Status = "reconciliation_failed"
		lastTest.Error = "terminal workflow run state could not be saved to the development session"
		lastTest.TestedAt = time.Now().UTC()
		projected.LastTest = &lastTest
	}
	projected.Status = workflows.WorkflowDevelopmentStatusEditing
	return &projected
}

func cloneWorkflowMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	cloned := make(map[string]any, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func (h *Handler) recordCanceledWorkflowDevelopmentRun(ctx context.Context, run *workflows.Run) {
	_ = ctx
	if run == nil || run.ID == "" || run.Status != workflows.RunStatusCanceled {
		return
	}
	h.recordWorkflowDevelopmentRunResult(&workflows.RunResult{
		RunID:  run.ID,
		Status: workflows.RunStatusCanceled,
		Error:  run.CancelReason,
	})
}

func (h *Handler) recordWorkflowDevelopmentRunResult(result *workflows.RunResult) {
	if result == nil || strings.TrimSpace(result.RunID) == "" {
		return
	}
	workspace, err := h.workflowWorkspace()
	if err != nil {
		return
	}
	h.recordWorkflowDevelopmentRunResultInWorkspace(workspace, result)
}

func (h *Handler) recordWorkflowDevelopmentRunResultInWorkspace(
	workspace string,
	result *workflows.RunResult,
) {
	if strings.TrimSpace(workspace) == "" || result == nil ||
		strings.TrimSpace(result.RunID) == "" {
		return
	}
	h.workflowDevelopmentMu.Lock()
	defer h.workflowDevelopmentMu.Unlock()
	session, err := workflows.GetWorkflowDevelopmentSession(workspace)
	if err != nil || session == nil || session.LastTest == nil {
		return
	}
	if session.LastTest.RunID != result.RunID ||
		!workflowDevelopmentTestStatusIsActive(session.LastTest.Status) {
		return
	}
	_, _, recordErr := retryWorkflowDevelopmentTestRecord(
		func() (*workflows.WorkflowDevelopmentSession, bool, error) {
			return recordWorkflowDevelopmentTestIfCurrentForEvent(
				workspace,
				session.ID,
				session.LastTest.DraftKey,
				session.LastTest.EventID,
				result.RunID,
				result,
				nil,
			)
		},
	)
	if recordErr != nil {
		logger.ErrorCF(
			"workflows",
			"failed to reconcile workflow development test after human task operation",
			map[string]any{
				"run_id": result.RunID,
				"error":  recordErr.Error(),
			},
		)
	}
}

func workflowDevelopmentTestStatusIsActive(status string) bool {
	return status == workflows.RunStatusRunning || status == workflows.RunStatusWaiting
}

func (h *Handler) tryLockWorkflowDevelopment(w http.ResponseWriter) func() {
	if !h.workflowDevelopmentMu.TryLock() {
		writeCollectionError(
			w, http.StatusConflict, "workflow_development_busy",
			"Workflow development operation is already in progress", -1, nil,
		)
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
