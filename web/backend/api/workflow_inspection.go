package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	workflowDefinitionInspectionRequestMaxBytes  = 16 << 10
	workflowDefinitionInspectionResponseMaxBytes = 32 << 20
)

type workflowDefinitionInspectionRequest struct {
	Ref *string `json:"ref"`
}

func (h *Handler) registerWorkflowInspectionRoutes(mux *http.ServeMux) {
	mux.HandleFunc(
		"POST /api/workflows/definitions/inspect",
		h.handleInspectWorkflowDefinition,
	)
	mux.HandleFunc(
		"GET /api/workflows/templates/{name}/inspect",
		h.handleInspectWorkflowTemplate,
	)
}

func (h *Handler) handleInspectWorkflowDefinition(w http.ResponseWriter, r *http.Request) {
	var request workflowDefinitionInspectionRequest
	if !decodeWorkflowDefinitionInspectionRequest(w, r, &request) {
		return
	}
	if request.Ref == nil || strings.TrimSpace(*request.Ref) == "" {
		writeWorkflowInspectionError(
			w,
			http.StatusBadRequest,
			"invalid_definition_inspection_request",
		)
		return
	}

	h.configMutationMu.Lock()
	cfg, err := h.workflowConfig()
	if err != nil {
		h.configMutationMu.Unlock()
		writeWorkflowInspectionError(
			w,
			http.StatusServiceUnavailable,
			"workflow_inspection_unavailable",
		)
		return
	}
	inspection, err := workflows.InspectLocalWorkflowDefinition(
		r.Context(),
		cfg.WorkspacePath(),
		*request.Ref,
		workflowLocalOptionsFromConfig(cfg)...,
	)
	h.configMutationMu.Unlock()
	if err != nil {
		writeLocalWorkflowInspectionError(w, err)
		return
	}
	if inspection == nil {
		writeWorkflowInspectionError(
			w,
			http.StatusServiceUnavailable,
			"workflow_inspection_unavailable",
		)
		return
	}
	writeWorkflowInspectionJSON(w, http.StatusOK, inspection)
}

func (h *Handler) handleInspectWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	inspection, err := workflows.InspectBuiltInWorkflowTemplate(r.PathValue("name"))
	if err != nil {
		writeTemplateWorkflowInspectionError(w, err)
		return
	}
	if inspection == nil {
		writeWorkflowInspectionError(
			w,
			http.StatusServiceUnavailable,
			"workflow_inspection_unavailable",
		)
		return
	}
	writeWorkflowInspectionJSON(w, http.StatusOK, inspection)
}

func decodeWorkflowDefinitionInspectionRequest(
	w http.ResponseWriter,
	r *http.Request,
	destination *workflowDefinitionInspectionRequest,
) bool {
	if r.Body == nil {
		writeWorkflowInspectionError(
			w,
			http.StatusBadRequest,
			"invalid_definition_inspection_request",
		)
		return false
	}
	contentType, ok := exactlyOneWorkflowInspectionHeader(
		r.Header,
		"Content-Type",
	)
	if !ok {
		writeWorkflowInspectionError(
			w,
			http.StatusUnsupportedMediaType,
			"invalid_definition_inspection_content_type",
		)
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeWorkflowInspectionError(
			w,
			http.StatusUnsupportedMediaType,
			"invalid_definition_inspection_content_type",
		)
		return false
	}
	for name, value := range parameters {
		if !strings.EqualFold(name, "charset") ||
			!strings.EqualFold(value, "utf-8") {
			writeWorkflowInspectionError(
				w,
				http.StatusUnsupportedMediaType,
				"invalid_definition_inspection_content_type",
			)
			return false
		}
	}

	raw, err := io.ReadAll(http.MaxBytesReader(
		w,
		r.Body,
		workflowDefinitionInspectionRequestMaxBytes,
	))
	if err != nil {
		var maximum *http.MaxBytesError
		if errors.As(err, &maximum) {
			writeWorkflowInspectionError(
				w,
				http.StatusRequestEntityTooLarge,
				"definition_inspection_request_too_large",
			)
			return false
		}
		writeWorkflowInspectionError(
			w,
			http.StatusBadRequest,
			"invalid_definition_inspection_request",
		)
		return false
	}
	if !utf8.Valid(raw) || rejectUnsafeWorkflowInspectionJSON(raw) != nil {
		writeWorkflowInspectionError(
			w,
			http.StatusBadRequest,
			"invalid_definition_inspection_request",
		)
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil ||
		len(fields) != 1 ||
		fields["ref"] == nil {
		writeWorkflowInspectionError(
			w,
			http.StatusBadRequest,
			"invalid_definition_inspection_request",
		)
		return false
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeWorkflowInspectionError(
			w,
			http.StatusBadRequest,
			"invalid_definition_inspection_request",
		)
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeWorkflowInspectionError(
			w,
			http.StatusBadRequest,
			"invalid_definition_inspection_request",
		)
		return false
	}
	return true
}

func exactlyOneWorkflowInspectionHeader(
	header http.Header,
	target string,
) (string, bool) {
	var values []string
	for name, candidates := range header {
		if strings.EqualFold(name, target) {
			values = append(values, candidates...)
		}
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", false
	}
	return values[0], true
}

func rejectUnsafeWorkflowInspectionJSON(raw []byte) error {
	if err := rejectDuplicateWorkflowTriggerJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	return rejectNullWorkflowInspectionJSONValue(value)
}

func rejectNullWorkflowInspectionJSONValue(value any) error {
	switch typed := value.(type) {
	case nil:
		return errors.New("null JSON values are not supported")
	case map[string]any:
		for _, item := range typed {
			if err := rejectNullWorkflowInspectionJSONValue(item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := rejectNullWorkflowInspectionJSONValue(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeLocalWorkflowInspectionError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	code := "workflow_inspection_unavailable"
	switch {
	case errors.Is(err, workflows.ErrWorkflowInspectionSourceInvalid):
		status = http.StatusBadRequest
		code = "invalid_definition_inspection_request"
	case errors.Is(err, workflows.ErrWorkflowInspectionNotFound):
		status = http.StatusNotFound
		code = "workflow_not_found"
	case errors.Is(err, workflows.ErrWorkflowInspectionSourceTooLarge):
		status = http.StatusRequestEntityTooLarge
		code = "workflow_definition_too_large"
	}
	writeWorkflowInspectionError(w, status, code)
}

func writeTemplateWorkflowInspectionError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	code := "workflow_inspection_unavailable"
	switch {
	case errors.Is(err, workflows.ErrWorkflowTemplateUnknown):
		status = http.StatusNotFound
		code = "template_not_found"
	case errors.Is(err, workflows.ErrWorkflowInspectionSourceInvalid):
		status = http.StatusBadRequest
		code = "invalid_definition_inspection_request"
	case errors.Is(err, workflows.ErrWorkflowInspectionSourceTooLarge):
		status = http.StatusRequestEntityTooLarge
		code = "workflow_definition_too_large"
	}
	writeWorkflowInspectionError(w, status, code)
}

func writeWorkflowInspectionError(w http.ResponseWriter, status int, code string) {
	writeWorkflowInspectionJSON(w, status, map[string]string{"error": code})
}

func writeWorkflowInspectionJSON(w http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil ||
		len(payload)+1 > workflowDefinitionInspectionResponseMaxBytes {
		status = http.StatusServiceUnavailable
		payload = []byte(`{"error":"workflow_inspection_unavailable"}`)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(payload, '\n'))
}
