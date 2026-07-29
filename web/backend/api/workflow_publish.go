package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

const workflowDevelopmentPublishRequestMaxBytes = 1 << 20

func (h *Handler) handlePublishWorkflowDevelopment(w http.ResponseWriter, r *http.Request) {
	var request workflows.WorkflowDevelopmentPublishRequest
	if err := decodeWorkflowDevelopmentPublishRequest(w, r, &request); err != nil {
		var maximum *http.MaxBytesError
		if errors.As(err, &maximum) {
			writeWorkflowPublishErrorCode(
				w,
				http.StatusRequestEntityTooLarge,
				"publish_request_too_large",
			)
			return
		}
		writeWorkflowPublishErrorCode(w, http.StatusBadRequest, "invalid_publish_request")
		return
	}
	if !validWorkflowDevelopmentPublishRequest(request) {
		writeWorkflowPublishErrorCode(w, http.StatusBadRequest, "invalid_publish_request")
		return
	}

	// Keep dashboard workflow-setting changes from changing the workspace or
	// definitions root between the dependency fence and activation.
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()
	if !h.workflowDevelopmentMu.TryLock() {
		writeWorkflowPublishErrorCode(w, http.StatusConflict, "workflow_development_busy")
		return
	}
	defer h.workflowDevelopmentMu.Unlock()

	cfg, err := h.workflowConfig()
	if err != nil {
		writeWorkflowPublishErrorCode(w, http.StatusServiceUnavailable, "workflow_publish_unavailable")
		return
	}
	result, err := workflows.PublishWorkflowDevelopmentFenced(
		r.Context(),
		cfg.WorkspacePath(),
		request,
		h.workflowCompatibilityRuntime(r.Context()),
		func(
			ctx context.Context,
			input workflows.WorkflowDevelopmentPublishGateInput,
		) (workflows.WorkflowDevelopmentPublishGateResult, error) {
			evaluation, evaluateErr := h.evaluateCurrentWorkflowDependencies(
				ctx,
				input.WorkflowRef,
				input.YAML,
			)
			if evaluateErr != nil {
				return workflows.WorkflowDevelopmentPublishGateResult{},
					errWorkflowDependencyUnavailable
			}
			return workflows.WorkflowDevelopmentPublishGateResult{
				Revision: evaluation.Revision,
				Ready:    evaluation.Ready,
			}, nil
		},
		workflowLocalOptionsFromConfig(cfg)...,
	)
	if err != nil {
		writeWorkflowPublishError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeWorkflowJSON(w, result)
}

func decodeWorkflowDevelopmentPublishRequest(
	w http.ResponseWriter,
	r *http.Request,
	destination *workflows.WorkflowDevelopmentPublishRequest,
) error {
	if r.Body == nil {
		return io.EOF
	}
	decoder := json.NewDecoder(http.MaxBytesReader(
		w,
		r.Body,
		workflowDevelopmentPublishRequestMaxBytes,
	))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("workflow publish request contains multiple JSON values")
		}
		return err
	}
	return nil
}

func validWorkflowDevelopmentPublishRequest(
	request workflows.WorkflowDevelopmentPublishRequest,
) bool {
	return strings.TrimSpace(request.SessionID) != "" &&
		strings.TrimSpace(request.ExpectedSessionRevision) != "" &&
		strings.TrimSpace(request.ExpectedDraftRevision) != "" &&
		strings.TrimSpace(request.ExpectedBaseTargetRevision) != "" &&
		strings.TrimSpace(request.ExpectedDependencyRevision) != ""
}

func writeWorkflowPublishError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workflows.ErrWorkflowRecoveryConflict):
		writeWorkflowPublishErrorCode(
			w,
			http.StatusConflict,
			"workflow_transaction_recovery_conflict",
		)
	case errors.Is(err, workflows.ErrNoActiveDevelopment):
		writeWorkflowPublishErrorCode(w, http.StatusNotFound, "workflow_development_not_found")
	case errors.Is(err, workflows.ErrDevelopmentBusy):
		writeWorkflowPublishErrorCode(w, http.StatusConflict, "workflow_development_busy")
	case errors.Is(err, workflows.ErrWorkflowSessionRevisionMismatch):
		writeWorkflowPublishErrorCode(w, http.StatusConflict, "session_revision_mismatch")
	case errors.Is(err, workflows.ErrWorkflowDraftRevisionMismatch):
		writeWorkflowPublishErrorCode(w, http.StatusConflict, "draft_revision_mismatch")
	case errors.Is(err, workflows.ErrWorkflowTargetRevisionMismatch):
		writeWorkflowPublishErrorCode(w, http.StatusConflict, "target_revision_mismatch")
	case errors.Is(err, workflows.ErrWorkflowDevelopmentDependencyRevisionMismatch):
		writeWorkflowPublishErrorCode(w, http.StatusConflict, "dependency_revision_mismatch")
	case errors.Is(err, workflows.ErrWorkflowDevelopmentPublishNotReady):
		writeWorkflowPublishErrorCode(w, http.StatusUnprocessableEntity, "workflow_dependencies_not_ready")
	case errors.Is(err, workflows.ErrWorkflowDevelopmentDraftNotReady):
		writeWorkflowPublishErrorCode(w, http.StatusUnprocessableEntity, "workflow_publish_not_ready")
	case errors.Is(err, errWorkflowDependencyUnavailable):
		writeWorkflowPublishErrorCode(w, http.StatusServiceUnavailable, "dependency_check_unavailable")
	case errors.Is(err, workflows.ErrWorkflowTemplateRecoveryFailed):
		writeWorkflowPublishErrorCode(w, http.StatusServiceUnavailable, "workflow_publish_recovery_failed")
	case errors.Is(err, workflows.ErrWorkflowDevelopmentPublishRecoveryFailed):
		writeWorkflowPublishErrorCode(w, http.StatusServiceUnavailable, "workflow_publish_recovery_failed")
	case errors.Is(err, workflows.ErrWorkflowDevelopmentPublishRollbackFailed):
		writeWorkflowPublishErrorCode(w, http.StatusInternalServerError, "workflow_publish_rollback_failed")
	case errors.Is(err, workflows.ErrWorkflowDevelopmentPublishGateRequired):
		writeWorkflowPublishErrorCode(w, http.StatusInternalServerError, "workflow_publish_gate_required")
	default:
		writeWorkflowPublishErrorCode(w, http.StatusInternalServerError, "workflow_publish_failed")
	}
}

func writeWorkflowPublishErrorCode(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Cache-Control", "no-store")
	writeWorkflowJSONStatus(w, status, map[string]any{"error": code})
}
