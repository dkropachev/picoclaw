package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const workflowHumanTaskRequestMaxBytes = 1 << 20

var errWorkflowHumanTaskConfigChanged = errors.New("workflow human-task config changed")

type workflowHumanTaskCancelRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) handleListWorkflowHumanTasks(w http.ResponseWriter, r *http.Request) {
	setWorkflowHumanTaskResponseHeaders(w)
	_, _, executor, err := h.workflowRuntime(r.Context())
	if err != nil {
		writeWorkflowJSONStatus(w, http.StatusInternalServerError, map[string]any{
			"error": "workflow_tasks_unavailable",
		})
		return
	}
	defer closeWorkflowRuntime(executor)

	tasks, err := executor.ListHumanTasks(r.Context(), r.PathValue("run_id"))
	if err != nil {
		writeWorkflowHumanTaskError(w, err)
		return
	}
	writeWorkflowJSON(w, map[string]any{"tasks": tasks})
}

func (h *Handler) handleResumeWorkflowHumanTask(w http.ResponseWriter, r *http.Request) {
	setWorkflowHumanTaskResponseHeaders(w)
	var request workflows.HumanTaskResumeRequest
	if err := decodeWorkflowHumanTaskRequest(w, r, &request); err != nil {
		writeWorkflowHumanTaskDecodeError(w, err, "invalid_task_resume_request")
		return
	}
	h.configMutationMu.Lock()
	releaseAdmission := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseAdmission()
	cfg, configRevision, err := loadStableWorkflowDependencyConfig(h.configPath)
	if err != nil {
		writeWorkflowJSONStatus(w, http.StatusInternalServerError, map[string]any{
			"error": "workflow_tasks_unavailable",
		})
		return
	}
	if !cfg.Workflows.Enabled {
		writeWorkflowJSONStatus(w, http.StatusConflict, map[string]any{
			"error": "workflow_tasks_disabled",
		})
		return
	}
	_, _, executor, err := h.workflowRuntimeFromConfig(r.Context(), cfg)
	if err != nil {
		writeWorkflowJSONStatus(w, http.StatusInternalServerError, map[string]any{
			"error": "workflow_tasks_unavailable",
		})
		return
	}
	defer closeWorkflowRuntime(executor)
	executor.AdmittedHumanTaskClaim = func(
		_ context.Context,
		_ string,
		_ string,
		claim func() (*workflows.Run, workflows.WorkflowHumanTask, bool, error),
	) (*workflows.Run, workflows.WorkflowHumanTask, bool, error) {
		defer releaseAdmission()
		var claimedRun *workflows.Run
		var task workflows.WorkflowHumanTask
		var duplicate bool
		guardErr := config.WithConfigMutationLock(h.configPath, func() error {
			currentRevision, revisionErr := config.ConfigRevision(h.configPath)
			if revisionErr != nil {
				return revisionErr
			}
			if currentRevision != configRevision {
				return errWorkflowHumanTaskConfigChanged
			}
			var claimErr error
			claimedRun, task, duplicate, claimErr = claim()
			return claimErr
		})
		return claimedRun, task, duplicate, guardErr
	}

	result, err := executor.ResumeHumanTask(
		r.Context(),
		r.PathValue("run_id"),
		r.PathValue("task_id"),
		request,
	)
	if result != nil {
		h.recordWorkflowDevelopmentRunResultInWorkspace(cfg.WorkspacePath(), result)
	}
	if err != nil {
		writeWorkflowHumanTaskError(w, err)
		return
	}
	writeWorkflowJSON(w, projectWorkflowHumanTaskRunResult(result))
}

func (h *Handler) handleCancelWorkflowHumanTask(w http.ResponseWriter, r *http.Request) {
	setWorkflowHumanTaskResponseHeaders(w)
	var request workflowHumanTaskCancelRequest
	if err := decodeWorkflowHumanTaskRequest(w, r, &request); err != nil {
		writeWorkflowHumanTaskDecodeError(w, err, "invalid_task_cancel_request")
		return
	}
	reason, err := workflows.NormalizeWorkflowCancelReason(request.Reason)
	if err != nil || reason == "" {
		writeWorkflowJSONStatus(w, http.StatusBadRequest, map[string]any{
			"error": "invalid_task_cancel_reason",
		})
		return
	}
	cfg, store, executor, err := h.workflowRuntime(r.Context())
	if err != nil {
		writeWorkflowJSONStatus(w, http.StatusInternalServerError, map[string]any{
			"error": "workflow_tasks_unavailable",
		})
		return
	}
	defer closeWorkflowRuntime(executor)

	run, err := executor.CancelHumanTask(
		r.Context(),
		r.PathValue("run_id"),
		r.PathValue("task_id"),
		reason,
	)
	if err != nil {
		writeWorkflowHumanTaskError(w, err)
		return
	}
	h.recordWorkflowDevelopmentRunResultInWorkspace(
		cfg.WorkspacePath(),
		&workflows.RunResult{
			RunID: run.ID, Status: workflows.RunStatusCanceled, Error: run.CancelReason,
		},
	)
	writeWorkflowJSON(
		w,
		workflows.ProjectWorkflowRunForBrowserWithStore(
			r.Context(),
			store,
			run,
			workflows.IsEventBackedDraftRunFamily(r.Context(), store, run),
		),
	)
}

func projectWorkflowHumanTaskRunResult(result *workflows.RunResult) *workflows.RunResult {
	if result == nil {
		return nil
	}
	projected := *result
	projected.Error = ""
	return &projected
}

func setWorkflowHumanTaskResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func decodeWorkflowHumanTaskRequest(w http.ResponseWriter, r *http.Request, destination any) error {
	if r.Body == nil {
		return io.EOF
	}
	data, err := io.ReadAll(http.MaxBytesReader(
		w,
		r.Body,
		workflowHumanTaskRequestMaxBytes,
	))
	if err != nil {
		return err
	}
	if !utf8.Valid(data) {
		return errors.New("workflow human-task request must be valid UTF-8")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("workflow human-task request must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("workflow human-task request contains multiple JSON values")
		}
		return err
	}
	return nil
}

func writeWorkflowHumanTaskDecodeError(w http.ResponseWriter, err error, code string) {
	var maximum *http.MaxBytesError
	if errors.As(err, &maximum) {
		writeWorkflowJSONStatus(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": "workflow_task_request_too_large",
		})
		return
	}
	writeWorkflowJSONStatus(w, http.StatusBadRequest, map[string]any{"error": code})
}

func writeWorkflowHumanTaskError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := "workflow_task_operation_failed"
	switch {
	case errors.Is(err, workflows.ErrHumanTaskNotFound):
		status = http.StatusNotFound
		code = "workflow_task_not_found"
	case errors.Is(err, workflows.ErrHumanTaskStale):
		status = http.StatusConflict
		code = "workflow_task_stale"
	case errors.Is(err, workflows.ErrHumanTaskConflict):
		status = http.StatusConflict
		code = "workflow_task_conflict"
	case errors.Is(err, workflows.ErrHumanTaskUnsupported):
		status = http.StatusConflict
		code = "workflow_task_unsupported"
	case errors.Is(err, workflows.ErrHumanTaskResponseInvalid):
		status = http.StatusBadRequest
		code = "workflow_task_response_invalid"
	case errors.Is(err, workflows.ErrRunConcurrencyLimit):
		status = http.StatusConflict
		code = "workflow_task_concurrency_limit"
	case errors.Is(err, errWorkflowHumanTaskConfigChanged):
		status = http.StatusConflict
		code = "workflow_task_config_changed"
	}
	writeWorkflowJSONStatus(w, status, map[string]any{"error": strings.TrimSpace(code)})
}
