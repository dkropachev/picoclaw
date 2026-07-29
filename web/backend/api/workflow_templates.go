package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

const workflowTemplateInstallRequestMaxBytes = 1 << 20

type workflowTemplateInstallRequest struct {
	Overwrite bool `json:"overwrite,omitempty"`
}

func (h *Handler) handleListWorkflowTemplates(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.workflowConfig()
	if err != nil {
		writeWorkflowTemplateError(w, workflows.ErrWorkflowTemplateCatalogUnavailable)
		return
	}
	templates, err := workflows.ListWorkflowTemplates(
		r.Context(),
		cfg.WorkspacePath(),
		workflowLocalOptionsFromConfig(cfg)...,
	)
	if err != nil {
		writeWorkflowTemplateError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeWorkflowJSON(w, map[string]any{"templates": templates})
}

func (h *Handler) handleInstallWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	var request workflowTemplateInstallRequest
	if err := decodeWorkflowTemplateInstallRequest(w, r, &request); err != nil {
		if maxBytesErr := new(http.MaxBytesError); errors.As(err, &maxBytesErr) {
			http.Error(w, "workflow template request exceeds 1 MiB", http.StatusRequestEntityTooLarge)
			return
		}
		writeWorkflowJSONStatus(w, http.StatusBadRequest, map[string]any{
			"error": "invalid_template_request",
		})
		return
	}
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()
	unlock := h.tryLockWorkflowDevelopment(w)
	if unlock == nil {
		return
	}
	defer unlock()

	cfg, err := h.workflowConfig()
	if err != nil {
		writeWorkflowTemplateError(w, workflows.ErrWorkflowTemplateInstallFailed)
		return
	}
	workspace := cfg.WorkspacePath()
	active, err := workflows.GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		writeWorkflowTemplateError(w, workflows.ErrWorkflowTemplateInstallFailed)
		return
	}
	if active != nil {
		writeWorkflowJSONStatus(w, http.StatusConflict, map[string]any{
			"error": "workflow_development_active",
		})
		return
	}
	localOpts := workflowLocalOptionsFromConfig(cfg)
	result, err := workflows.InstallWorkflowTemplateWithCompatibility(
		r.Context(),
		workspace,
		r.PathValue("name"),
		request.Overwrite,
		h.workflowCompatibilityRuntime(r.Context()),
		localOpts...,
	)
	if err != nil {
		writeWorkflowTemplateError(w, err)
		return
	}
	templates, err := workflows.ListWorkflowTemplates(
		r.Context(),
		workspace,
		localOpts...,
	)
	if err != nil {
		writeWorkflowTemplateError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeWorkflowJSON(w, map[string]any{
		"result":    result,
		"templates": templates,
	})
}

func decodeWorkflowTemplateInstallRequest(
	w http.ResponseWriter,
	r *http.Request,
	destination *workflowTemplateInstallRequest,
) error {
	if r.Body == nil {
		return nil
	}
	decoder := json.NewDecoder(http.MaxBytesReader(
		w,
		r.Body,
		workflowTemplateInstallRequestMaxBytes,
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
			return errors.New("workflow template request contains multiple JSON values")
		}
		return err
	}
	return nil
}

func writeWorkflowTemplateError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := "template_install_failed"
	switch {
	case errors.Is(err, workflows.ErrWorkflowRecoveryConflict):
		status = http.StatusConflict
		code = "workflow_transaction_recovery_conflict"
	case errors.Is(err, workflows.ErrWorkflowTemplateRecoveryFailed):
		status = http.StatusServiceUnavailable
		code = "template_recovery_failed"
	case errors.Is(err, workflows.ErrWorkflowTemplateUnknown):
		status = http.StatusNotFound
		code = "template_not_found"
	case errors.Is(err, workflows.ErrWorkflowTemplateOverwriteRequired):
		status = http.StatusConflict
		code = "template_overwrite_required"
	case errors.Is(err, workflows.ErrWorkflowTemplateTargetBlocked):
		status = http.StatusConflict
		code = "template_target_blocked"
	case errors.Is(err, workflows.ErrWorkflowTemplateCatalogUnavailable):
		status = http.StatusServiceUnavailable
		code = "template_catalog_unavailable"
	case errors.Is(err, workflows.ErrActiveDevelopmentExists):
		status = http.StatusConflict
		code = "workflow_development_active"
	case errors.Is(err, workflows.ErrWorkflowTemplateRevalidationFailed):
		status = http.StatusUnprocessableEntity
		code = "template_revalidation_failed"
	case errors.Is(err, workflows.ErrWorkflowTemplateRollbackFailed):
		code = "template_rollback_failed"
	}
	writeWorkflowJSONStatus(w, status, map[string]any{"error": code})
}
