package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

const workflowEventTriggerRequestMaxBytes = 1 << 20

type workflowEventTriggerInspectRequest struct {
	YAML *string `json:"yaml"`
}

type workflowEventTriggerRenderRequest struct {
	YAML         *string         `json:"yaml"`
	Revision     string          `json:"revision"`
	EventTrigger json.RawMessage `json:"event_trigger"`
}

type workflowEventTriggerMatchRequest struct {
	YAML    *string `json:"yaml"`
	EventID string  `json:"event_id"`
}

type workflowEventTriggerRenderResponse struct {
	YAML         string                                   `json:"yaml"`
	Revision     string                                   `json:"revision"`
	Editable     bool                                     `json:"editable"`
	Reason       string                                   `json:"reason,omitempty"`
	EventTrigger *workflows.EventTrigger                  `json:"event_trigger"`
	Validation   *workflows.WorkflowDevelopmentValidation `json:"validation"`
}

type workflowEventTriggerMatchResponse struct {
	Error      string                                   `json:"error,omitempty"`
	EventID    string                                   `json:"event_id"`
	Matched    bool                                     `json:"matched"`
	Checks     []workflows.EventTriggerMatchCheck       `json:"checks"`
	Validation *workflows.WorkflowDevelopmentValidation `json:"validation"`
}

type workflowEventTriggerErrorResponse struct {
	Error        string                                   `json:"error"`
	Revision     string                                   `json:"revision,omitempty"`
	Editable     bool                                     `json:"editable"`
	Reason       string                                   `json:"reason,omitempty"`
	EventTrigger *workflows.EventTrigger                  `json:"event_trigger"`
	Validation   *workflows.WorkflowDevelopmentValidation `json:"validation,omitempty"`
}

// registerWorkflowEditorRoutes binds the narrow structured editor separately
// so the main workflow router remains stable while this surface evolves.
func (h *Handler) registerWorkflowEditorRoutes(mux *http.ServeMux) {
	mux.HandleFunc(
		"POST /api/workflows/development/event-trigger/inspect",
		h.handleInspectWorkflowEventTrigger,
	)
	mux.HandleFunc(
		"POST /api/workflows/development/event-trigger/render",
		h.handleRenderWorkflowEventTrigger,
	)
	mux.HandleFunc(
		"POST /api/workflows/development/event-trigger/match",
		h.handleMatchWorkflowEventTrigger,
	)
}

func (h *Handler) handleInspectWorkflowEventTrigger(
	w http.ResponseWriter,
	r *http.Request,
) {
	var req workflowEventTriggerInspectRequest
	if !decodeWorkflowEventTriggerRequest(w, r, &req) {
		return
	}
	if req.YAML == nil {
		writeWorkflowEventTriggerError(w, http.StatusBadRequest, "yaml is required")
		return
	}

	writeWorkflowEventTriggerJSON(
		w,
		http.StatusOK,
		workflows.InspectWorkflowEventTrigger(*req.YAML),
	)
}

func (h *Handler) handleRenderWorkflowEventTrigger(
	w http.ResponseWriter,
	r *http.Request,
) {
	var req workflowEventTriggerRenderRequest
	if !decodeWorkflowEventTriggerRequest(w, r, &req) {
		return
	}
	if req.YAML == nil {
		writeWorkflowEventTriggerError(w, http.StatusBadRequest, "yaml is required")
		return
	}
	if strings.TrimSpace(req.Revision) == "" {
		writeWorkflowEventTriggerError(w, http.StatusBadRequest, "revision is required")
		return
	}
	if req.EventTrigger == nil {
		writeWorkflowEventTriggerError(
			w,
			http.StatusBadRequest,
			"event_trigger is required and may be null",
		)
		return
	}

	trigger, err := decodeWorkflowEventTrigger(req.EventTrigger)
	if err != nil {
		writeWorkflowEventTriggerError(
			w,
			http.StatusBadRequest,
			"event_trigger must be a valid event trigger or null",
		)
		return
	}
	rendered, inspection, err := workflows.RenderWorkflowEventTrigger(
		*req.YAML,
		req.Revision,
		trigger,
	)
	if err != nil {
		h.writeWorkflowEventTriggerRenderError(w, inspection, err)
		return
	}
	writeWorkflowEventTriggerJSON(
		w,
		http.StatusOK,
		newWorkflowEventTriggerRenderResponse(rendered, inspection),
	)
}

func (h *Handler) handleMatchWorkflowEventTrigger(
	w http.ResponseWriter,
	r *http.Request,
) {
	var req workflowEventTriggerMatchRequest
	if !decodeWorkflowEventTriggerRequest(w, r, &req) {
		return
	}
	if req.YAML == nil {
		writeWorkflowEventTriggerError(w, http.StatusBadRequest, "yaml is required")
		return
	}
	if req.EventID == "" {
		writeWorkflowEventTriggerError(w, http.StatusBadRequest, "event_id is required")
		return
	}

	inspection := workflows.InspectWorkflowEventTrigger(*req.YAML)
	if inspection.Validation == nil || !inspection.Validation.Valid {
		writeWorkflowEventTriggerJSON(
			w,
			http.StatusUnprocessableEntity,
			workflowEventTriggerMatchResponse{
				Error:      "workflow YAML is invalid",
				EventID:    req.EventID,
				Checks:     []workflows.EventTriggerMatchCheck{},
				Validation: inspection.Validation,
			},
		)
		return
	}
	if inspection.EventTrigger == nil {
		validation := invalidWorkflowEventTriggerValidation(workflows.ValidationErrors{{
			Path:    "on.event",
			Message: "event trigger is required",
		}})
		writeWorkflowEventTriggerJSON(
			w,
			http.StatusUnprocessableEntity,
			workflowEventTriggerMatchResponse{
				Error:      "event trigger is required",
				EventID:    req.EventID,
				Checks:     []workflows.EventTriggerMatchCheck{},
				Validation: validation,
			},
		)
		return
	}

	event, err := h.loadWorkflowEventEnvelope(r.Context(), req.EventID, false)
	if err != nil {
		writeWorkflowEventTriggerMatchLoadError(w, err)
		return
	}
	result, err := workflows.EvaluateEventTrigger(inspection.EventTrigger, event)
	if err != nil {
		writeWorkflowEventTriggerJSON(
			w,
			http.StatusUnprocessableEntity,
			workflowEventTriggerMatchResponse{
				Error:      "event trigger is invalid",
				EventID:    req.EventID,
				Checks:     []workflows.EventTriggerMatchCheck{},
				Validation: invalidWorkflowEventTriggerValidation(err),
			},
		)
		return
	}
	writeWorkflowEventTriggerJSON(
		w,
		http.StatusOK,
		workflowEventTriggerMatchResponse{
			EventID:    req.EventID,
			Matched:    result.Matched,
			Checks:     result.Checks,
			Validation: inspection.Validation,
		},
	)
}

func newWorkflowEventTriggerRenderResponse(
	raw string,
	inspection workflows.WorkflowEventTriggerInspection,
) workflowEventTriggerRenderResponse {
	return workflowEventTriggerRenderResponse{
		YAML:         raw,
		Revision:     inspection.Revision,
		Editable:     inspection.Editable,
		Reason:       inspection.Reason,
		EventTrigger: inspection.EventTrigger,
		Validation:   inspection.Validation,
	}
}

func (h *Handler) writeWorkflowEventTriggerRenderError(
	w http.ResponseWriter,
	inspection workflows.WorkflowEventTriggerInspection,
	err error,
) {
	status := http.StatusUnprocessableEntity
	message := "event trigger cannot be rendered"
	validation := inspection.Validation
	switch {
	case errors.Is(err, workflows.ErrWorkflowEventTriggerStaleRevision):
		status = http.StatusConflict
		message = "workflow YAML changed; inspect the latest revision and try again"
	case errors.Is(err, workflows.ErrWorkflowEventTriggerNotEditable):
		message = "event trigger requires the raw YAML editor"
	default:
		validation = invalidWorkflowEventTriggerValidation(err)
	}
	writeWorkflowEventTriggerJSON(
		w,
		status,
		workflowEventTriggerErrorResponse{
			Error:        message,
			Revision:     inspection.Revision,
			Editable:     inspection.Editable,
			Reason:       inspection.Reason,
			EventTrigger: inspection.EventTrigger,
			Validation:   validation,
		},
	)
}

func decodeWorkflowEventTrigger(raw json.RawMessage) (*workflows.EventTrigger, error) {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var trigger workflows.EventTrigger
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&trigger); err != nil {
		return nil, err
	}
	if err := requireWorkflowEventTriggerJSONEOF(decoder); err != nil {
		return nil, err
	}
	return &trigger, nil
}

func decodeWorkflowEventTriggerRequest(
	w http.ResponseWriter,
	r *http.Request,
	destination any,
) bool {
	if r.Body == nil {
		writeWorkflowEventTriggerError(w, http.StatusBadRequest, "JSON body is required")
		return false
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil || mediaType != "application/json" {
			writeWorkflowEventTriggerError(
				w,
				http.StatusUnsupportedMediaType,
				"Content-Type must be application/json",
			)
			return false
		}
	}

	decoder := json.NewDecoder(http.MaxBytesReader(
		w,
		r.Body,
		workflowEventTriggerRequestMaxBytes,
	))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeWorkflowEventTriggerDecodeError(w, err)
		return false
	}
	if err := requireWorkflowEventTriggerJSONEOF(decoder); err != nil {
		writeWorkflowEventTriggerDecodeError(w, err)
		return false
	}
	return true
}

func requireWorkflowEventTriggerJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}

func writeWorkflowEventTriggerDecodeError(w http.ResponseWriter, err error) {
	var maximum *http.MaxBytesError
	if errors.As(err, &maximum) {
		writeWorkflowEventTriggerError(
			w,
			http.StatusRequestEntityTooLarge,
			"JSON body exceeds 1 MiB",
		)
		return
	}
	writeWorkflowEventTriggerError(w, http.StatusBadRequest, "invalid JSON body")
}

func writeWorkflowEventTriggerMatchLoadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errWorkflowEventInvalid):
		writeWorkflowEventTriggerError(w, http.StatusBadRequest, "invalid event request")
	case errors.Is(err, errWorkflowEventNotFound):
		writeWorkflowEventTriggerError(w, http.StatusNotFound, "event not found")
	default:
		w.Header().Set("Retry-After", "1")
		writeWorkflowEventTriggerError(
			w,
			http.StatusServiceUnavailable,
			"event service unavailable",
		)
	}
}

func invalidWorkflowEventTriggerValidation(
	err error,
) *workflows.WorkflowDevelopmentValidation {
	return &workflows.WorkflowDevelopmentValidation{
		Errors:      workflows.ValidationIssues(err),
		ValidatedAt: time.Now().UTC(),
	}
}

func writeWorkflowEventTriggerError(
	w http.ResponseWriter,
	status int,
	message string,
) {
	writeWorkflowEventTriggerJSON(
		w,
		status,
		map[string]string{"error": message},
	)
}

func writeWorkflowEventTriggerJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
