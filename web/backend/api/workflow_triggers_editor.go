package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	workflowTriggerCandidateMaximumIssues       = 128
	workflowTriggerCandidateMaximumPathBytes    = 1024
	workflowTriggerCandidateMaximumMessageBytes = 4096
	workflowTriggerCandidateMaximumEncodedBytes = 64 << 10
)

type workflowTriggersInspectRequest struct {
	YAML *string `json:"yaml"`
}

type workflowTriggerRenderRequest struct {
	YAML        *string         `json:"yaml"`
	Revision    string          `json:"revision"`
	TriggerType string          `json:"trigger_type"`
	Trigger     json.RawMessage `json:"trigger"`
}

type workflowTriggerRenderResponse struct {
	YAML       string                                                                `json:"yaml"`
	Revision   string                                                                `json:"revision"`
	Triggers   map[workflows.WorkflowTriggerKind]workflows.WorkflowTriggerProjection `json:"triggers"`
	Validation *workflows.WorkflowDevelopmentValidation                              `json:"validation"`
}

type workflowTriggerErrorResponse struct {
	Error               string                                   `json:"error"`
	Inspection          *workflows.WorkflowTriggersInspection    `json:"inspection,omitempty"`
	CandidateValidation *workflows.WorkflowDevelopmentValidation `json:"candidate_validation,omitempty"`
}

func (h *Handler) handleInspectWorkflowTriggers(w http.ResponseWriter, r *http.Request) {
	var request workflowTriggersInspectRequest
	if !decodeStrictWorkflowTriggerRequest(w, r, &request) {
		return
	}
	if request.YAML == nil {
		writeWorkflowTriggerError(w, http.StatusBadRequest, "invalid_trigger_request", nil)
		return
	}
	writeWorkflowEventTriggerJSON(
		w,
		http.StatusOK,
		workflows.InspectWorkflowTriggers(*request.YAML),
	)
}

func (h *Handler) handleRenderWorkflowTrigger(w http.ResponseWriter, r *http.Request) {
	var request workflowTriggerRenderRequest
	if !decodeStrictWorkflowTriggerRequest(w, r, &request) {
		return
	}
	if request.YAML == nil ||
		strings.TrimSpace(request.Revision) == "" ||
		strings.TrimSpace(request.TriggerType) == "" ||
		request.Trigger == nil {
		writeWorkflowTriggerError(w, http.StatusBadRequest, "invalid_trigger_request", nil)
		return
	}

	kind := workflows.WorkflowTriggerKind(request.TriggerType)
	if !kind.Valid() {
		writeWorkflowTriggerError(w, http.StatusBadRequest, "unsupported_trigger_type", nil)
		return
	}
	replacement, err := decodeWorkflowTriggerValue(kind, request.Trigger)
	if err != nil {
		writeWorkflowTriggerCandidateError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_workflow_trigger",
			nil,
			err,
		)
		return
	}

	rendered, inspection, err := workflows.RenderWorkflowTrigger(
		*request.YAML,
		request.Revision,
		kind,
		replacement,
	)
	if err != nil {
		h.writeWorkflowTriggerRenderError(w, inspection, err)
		return
	}
	writeWorkflowEventTriggerJSON(
		w,
		http.StatusOK,
		workflowTriggerRenderResponse{
			YAML:       rendered,
			Revision:   inspection.Revision,
			Triggers:   inspection.Triggers,
			Validation: inspection.Validation,
		},
	)
}

func (h *Handler) writeWorkflowTriggerRenderError(
	w http.ResponseWriter,
	inspection workflows.WorkflowTriggersInspection,
	err error,
) {
	status := http.StatusUnprocessableEntity
	code := "invalid_workflow_trigger"
	switch {
	case errors.Is(err, workflows.ErrWorkflowTriggerKind):
		status = http.StatusBadRequest
		code = "unsupported_trigger_type"
	case errors.Is(err, workflows.ErrWorkflowTriggerStaleRevision):
		status = http.StatusConflict
		code = "workflow_trigger_revision_mismatch"
	case errors.Is(err, workflows.ErrWorkflowTriggerNotEditable):
		code = "workflow_trigger_raw_only"
	}
	if code == "invalid_workflow_trigger" {
		writeWorkflowTriggerCandidateError(
			w,
			status,
			code,
			&inspection,
			err,
		)
		return
	}
	writeWorkflowTriggerError(w, status, code, &inspection)
}

func decodeWorkflowTriggerValue(
	kind workflows.WorkflowTriggerKind,
	raw json.RawMessage,
) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if err := rejectWorkflowTriggerJSONNullFields(kind, trimmed); err != nil {
		return nil, err
	}

	switch kind {
	case workflows.WorkflowTriggerManual:
		var trigger map[string]any
		if err := decodeStrictWorkflowTriggerValue(trimmed, &trigger); err != nil {
			return nil, err
		}
		normalized, err := normalizeWorkflowTriggerJSONValue(trigger)
		if err != nil {
			return nil, err
		}
		return normalized.(map[string]any), nil
	case workflows.WorkflowTriggerSchedule:
		var trigger []workflows.ScheduleTrigger
		if err := decodeStrictWorkflowTriggerValue(trimmed, &trigger); err != nil ||
			trigger == nil {
			if err == nil {
				err = errors.New("schedule must be an array")
			}
			return nil, err
		}
		return trigger, nil
	case workflows.WorkflowTriggerChannelMessage:
		var trigger workflows.ChannelMessageTrigger
		if err := decodeStrictWorkflowTriggerValue(trimmed, &trigger); err != nil {
			return nil, err
		}
		return &trigger, nil
	case workflows.WorkflowTriggerCommand:
		var trigger workflows.CommandTrigger
		if err := decodeStrictWorkflowTriggerValue(trimmed, &trigger); err != nil {
			return nil, err
		}
		if err := normalizeWorkflowTriggerInputDefaults(trigger.Args); err != nil {
			return nil, err
		}
		return &trigger, nil
	case workflows.WorkflowTriggerRuntimeEvent:
		var trigger workflows.RuntimeEventTrigger
		if err := decodeStrictWorkflowTriggerValue(trimmed, &trigger); err != nil {
			return nil, err
		}
		return &trigger, nil
	case workflows.WorkflowTriggerEvent:
		var trigger workflows.EventTrigger
		if err := decodeStrictWorkflowTriggerValue(trimmed, &trigger); err != nil {
			return nil, err
		}
		return &trigger, nil
	case workflows.WorkflowTriggerWorkflowCall:
		var trigger workflows.WorkflowCall
		if err := decodeStrictWorkflowTriggerValue(trimmed, &trigger); err != nil {
			return nil, err
		}
		if err := normalizeWorkflowTriggerInputDefaults(trigger.Inputs); err != nil {
			return nil, err
		}
		return &trigger, nil
	default:
		return nil, workflows.ErrWorkflowTriggerKind
	}
}

func decodeStrictWorkflowTriggerValue(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireWorkflowEventTriggerJSONEOF(decoder)
}

func normalizeWorkflowTriggerInputDefaults(inputs map[string]workflows.Input) error {
	for name, input := range inputs {
		if input.Default == nil {
			continue
		}
		normalized, err := normalizeWorkflowTriggerJSONValue(input.Default)
		if err != nil {
			return err
		}
		input.Default = normalized
		inputs[name] = input
	}
	return nil
}

func normalizeWorkflowTriggerJSONValue(value any) (any, error) {
	switch typed := value.(type) {
	case json.Number:
		text := string(typed)
		if !workflows.WorkflowJSONNumberIsBrowserSafe(text) {
			return nil, errors.New("number is outside the browser-safe range")
		}
		if !strings.ContainsAny(text, ".eE") {
			if signed, err := strconv.ParseInt(text, 10, 64); err == nil {
				return signed, nil
			}
			unsigned, err := strconv.ParseUint(text, 10, 64)
			if err != nil {
				return nil, errors.New("integer is outside the browser-safe range")
			}
			return unsigned, nil
		}
		number, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, errors.New("number is not JSON-safe")
		}
		return number, nil
	case []any:
		for index, item := range typed {
			normalized, err := normalizeWorkflowTriggerJSONValue(item)
			if err != nil {
				return nil, err
			}
			typed[index] = normalized
		}
		return typed, nil
	case map[string]any:
		for key, item := range typed {
			normalized, err := normalizeWorkflowTriggerJSONValue(item)
			if err != nil {
				return nil, err
			}
			typed[key] = normalized
		}
		return typed, nil
	default:
		return value, nil
	}
}

func rejectWorkflowTriggerJSONNullFields(
	kind workflows.WorkflowTriggerKind,
	raw []byte,
) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	return walkWorkflowTriggerJSONNullFields(kind, value, nil, false)
}

func walkWorkflowTriggerJSONNullFields(
	kind workflows.WorkflowTriggerKind,
	value any,
	path []string,
	insideDefault bool,
) error {
	switch typed := value.(type) {
	case []any:
		for index, item := range typed {
			if item == nil && !insideDefault {
				return errors.New("null trigger fields are not supported")
			}
			if err := walkWorkflowTriggerJSONNullFields(
				kind,
				item,
				append(path, strconv.Itoa(index)),
				insideDefault,
			); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, item := range typed {
			if insideDefault {
				if err := walkWorkflowTriggerJSONNullFields(
					kind,
					item,
					append(path, key),
					true,
				); err != nil {
					return err
				}
				continue
			}
			if item == nil {
				return errors.New("null trigger fields are not supported")
			}
			isDefault := key == "default" && workflowTriggerJSONInputPath(kind, path)
			if err := walkWorkflowTriggerJSONNullFields(
				kind,
				item,
				append(path, key),
				isDefault,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func workflowTriggerJSONInputPath(
	kind workflows.WorkflowTriggerKind,
	path []string,
) bool {
	if len(path) != 2 {
		return false
	}
	switch kind {
	case workflows.WorkflowTriggerCommand:
		return path[0] == "args"
	case workflows.WorkflowTriggerWorkflowCall:
		return path[0] == "inputs"
	default:
		return false
	}
}

func decodeStrictWorkflowTriggerRequest(
	w http.ResponseWriter,
	r *http.Request,
	destination any,
) bool {
	if r.Body == nil {
		writeWorkflowTriggerError(w, http.StatusBadRequest, "invalid_trigger_request", nil)
		return false
	}
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		writeWorkflowTriggerError(w, http.StatusUnsupportedMediaType, "invalid_trigger_request", nil)
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" {
		writeWorkflowTriggerError(w, http.StatusUnsupportedMediaType, "invalid_trigger_request", nil)
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(
		w,
		r.Body,
		workflowEventTriggerRequestMaxBytes,
	))
	if err != nil {
		var maximum *http.MaxBytesError
		if errors.As(err, &maximum) {
			writeWorkflowTriggerError(
				w,
				http.StatusRequestEntityTooLarge,
				"trigger_request_too_large",
				nil,
			)
			return false
		}
		writeWorkflowTriggerError(w, http.StatusBadRequest, "invalid_trigger_request", nil)
		return false
	}
	if err := rejectDuplicateWorkflowTriggerJSONKeys(raw); err != nil {
		writeWorkflowTriggerError(w, http.StatusBadRequest, "invalid_trigger_request", nil)
		return false
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		writeWorkflowTriggerError(w, http.StatusBadRequest, "invalid_trigger_request", nil)
		return false
	}
	if err := requireWorkflowEventTriggerJSONEOF(decoder); err != nil {
		writeWorkflowTriggerError(w, http.StatusBadRequest, "invalid_trigger_request", nil)
		return false
	}
	return true
}

func rejectDuplicateWorkflowTriggerJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := consumeUniqueWorkflowTriggerJSONValue(decoder); err != nil {
		return err
	}
	return requireWorkflowEventTriggerJSONEOF(decoder)
}

func consumeUniqueWorkflowTriggerJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key must be a string")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := consumeUniqueWorkflowTriggerJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := consumeUniqueWorkflowTriggerJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func writeWorkflowTriggerError(
	w http.ResponseWriter,
	status int,
	code string,
	inspection *workflows.WorkflowTriggersInspection,
) {
	writeWorkflowEventTriggerJSON(
		w,
		status,
		workflowTriggerErrorResponse{
			Error:      code,
			Inspection: inspection,
		},
	)
}

func writeWorkflowTriggerCandidateError(
	w http.ResponseWriter,
	status int,
	code string,
	inspection *workflows.WorkflowTriggersInspection,
	err error,
) {
	writeWorkflowEventTriggerJSON(
		w,
		status,
		workflowTriggerErrorResponse{
			Error:               code,
			Inspection:          inspection,
			CandidateValidation: boundedWorkflowTriggerCandidateValidation(err),
		},
	)
}

func boundedWorkflowTriggerCandidateValidation(
	err error,
) *workflows.WorkflowDevelopmentValidation {
	issues := workflows.ValidationIssues(err)
	if len(issues) == 0 ||
		(len(issues) == 1 && issues[0].Path == "") {
		issues = []workflows.WorkflowValidationIssue{{
			Message: "Trigger value is invalid.",
		}}
	}
	if len(issues) > workflowTriggerCandidateMaximumIssues {
		issues = issues[:workflowTriggerCandidateMaximumIssues]
	}

	validation := &workflows.WorkflowDevelopmentValidation{
		ValidatedAt: time.Now().UTC(),
	}
	for _, issue := range issues {
		issue.Path = truncateWorkflowTriggerCandidateText(
			issue.Path,
			workflowTriggerCandidateMaximumPathBytes,
		)
		issue.Message = truncateWorkflowTriggerCandidateText(
			issue.Message,
			workflowTriggerCandidateMaximumMessageBytes,
		)
		if issue.Message == "" {
			issue.Message = "Trigger value is invalid."
		}
		validation.Errors = append(validation.Errors, issue)
		encoded, marshalErr := json.Marshal(validation)
		if marshalErr != nil ||
			len(encoded) > workflowTriggerCandidateMaximumEncodedBytes {
			validation.Errors = validation.Errors[:len(validation.Errors)-1]
			break
		}
	}
	if len(validation.Errors) == 0 {
		validation.Errors = []workflows.WorkflowValidationIssue{{
			Message: "Trigger value is invalid.",
		}}
	}
	return validation
}

func truncateWorkflowTriggerCandidateText(value string, maximumBytes int) string {
	if maximumBytes <= 0 {
		return ""
	}
	if len(value) <= maximumBytes {
		return value
	}
	const suffix = "..."
	if maximumBytes <= len(suffix) {
		return suffix[:maximumBytes]
	}
	cut := maximumBytes - len(suffix)
	for cut > 0 && cut < len(value) && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + suffix
}
