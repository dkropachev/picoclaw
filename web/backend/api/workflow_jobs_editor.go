package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	workflowJobsEditorRequestMaxBytes  = 1 << 20
	workflowJobsEditorJSONMaxDepth     = 32
	workflowJobsEditorJSONMaxTokens    = 65536
	workflowJobsEditorResponseMaxBytes = 8 << 20
)

var workflowJobsEditorResponseByteLimit = workflowJobsEditorResponseMaxBytes

type workflowJobsInspectRequest struct {
	YAML *string `json:"yaml"`
}

type workflowJobsRenderRequest struct {
	YAML      *string         `json:"yaml"`
	Revision  string          `json:"revision"`
	Operation json.RawMessage `json:"operation"`
}

type workflowJobsRenderResponse struct {
	YAML string `json:"yaml"`
	workflows.WorkflowJobsInspection
}

type workflowJobsErrorResponse struct {
	Error      string                            `json:"error"`
	Inspection *workflows.WorkflowJobsInspection `json:"inspection,omitempty"`
}

type workflowJobsMutationWire struct {
	Mode  string          `json:"mode"`
	Value json.RawMessage `json:"value"`
}

type workflowJobInsertWire struct {
	Type   string                     `json:"type"`
	JobID  string                     `json:"job_id"`
	Index  *int                       `json:"index"`
	Fields map[string]json.RawMessage `json:"fields"`
}

type workflowJobDeleteWire struct {
	Type  string `json:"type"`
	JobID string `json:"job_id"`
}

type workflowJobPatchWire struct {
	Type     string                     `json:"type"`
	JobID    string                     `json:"job_id"`
	NewJobID json.RawMessage            `json:"new_job_id"`
	Fields   map[string]json.RawMessage `json:"fields"`
}

type workflowStepInsertWire struct {
	Type   string                     `json:"type"`
	JobID  string                     `json:"job_id"`
	Index  *int                       `json:"index"`
	Fields map[string]json.RawMessage `json:"fields"`
}

type workflowStepDeleteWire struct {
	Type      string `json:"type"`
	JobID     string `json:"job_id"`
	StepIndex *int   `json:"step_index"`
}

type workflowStepMoveWire struct {
	Type      string `json:"type"`
	JobID     string `json:"job_id"`
	StepIndex *int   `json:"step_index"`
	ToIndex   *int   `json:"to_index"`
}

type workflowStepPatchWire struct {
	Type      string                     `json:"type"`
	JobID     string                     `json:"job_id"`
	StepIndex *int                       `json:"step_index"`
	Fields    map[string]json.RawMessage `json:"fields"`
}

func (h *Handler) handleInspectWorkflowJobs(w http.ResponseWriter, r *http.Request) {
	var request workflowJobsInspectRequest
	if !decodeStrictWorkflowJobsRequest(
		w,
		r,
		&request,
		[]string{"yaml"},
	) {
		return
	}
	if request.YAML == nil {
		writeWorkflowJobsError(
			w,
			http.StatusBadRequest,
			"invalid_workflow_jobs_request",
			nil,
		)
		return
	}
	writeWorkflowJobsJSON(
		w,
		http.StatusOK,
		workflows.InspectWorkflowJobs(*request.YAML),
	)
}

func (h *Handler) handleRenderWorkflowJobs(w http.ResponseWriter, r *http.Request) {
	var request workflowJobsRenderRequest
	if !decodeStrictWorkflowJobsRequest(
		w,
		r,
		&request,
		[]string{"yaml", "revision", "operation"},
	) {
		return
	}
	if request.YAML == nil ||
		request.Revision == "" ||
		request.Operation == nil {
		writeWorkflowJobsError(
			w,
			http.StatusBadRequest,
			"invalid_workflow_jobs_request",
			nil,
		)
		return
	}
	if !workflows.WorkflowJobsRevisionMatches(*request.YAML, request.Revision) {
		inspection := workflows.InspectWorkflowJobs(*request.YAML)
		writeWorkflowJobsError(
			w,
			http.StatusConflict,
			"workflow_jobs_revision_mismatch",
			&inspection,
		)
		return
	}
	operation, err := decodeWorkflowJobsOperation(request.Operation)
	if err != nil {
		code := "invalid_workflow_jobs_operation"
		status := http.StatusUnprocessableEntity
		if errors.Is(err, errUnsupportedWorkflowJobsOperation) {
			code = "unsupported_workflow_jobs_operation"
			status = http.StatusBadRequest
		} else if errors.Is(err, errMalformedWorkflowJobsOperation) {
			code = "invalid_workflow_jobs_request"
			status = http.StatusBadRequest
		}
		writeWorkflowJobsError(w, status, code, nil)
		return
	}

	rendered, inspection, err := workflows.RenderWorkflowJobs(
		*request.YAML,
		request.Revision,
		operation,
	)
	if err != nil {
		status := http.StatusUnprocessableEntity
		code := "invalid_workflow_jobs_operation"
		switch {
		case errors.Is(err, workflows.ErrWorkflowJobsStaleRevision):
			status = http.StatusConflict
			code = "workflow_jobs_revision_mismatch"
		case errors.Is(err, workflows.ErrWorkflowJobsNotEditable):
			code = "workflow_jobs_raw_only"
		}
		writeWorkflowJobsError(w, status, code, &inspection)
		return
	}
	writeWorkflowJobsJSON(
		w,
		http.StatusOK,
		workflowJobsRenderResponse{
			YAML:                   rendered,
			WorkflowJobsInspection: inspection,
		},
	)
}

var (
	errMalformedWorkflowJobsOperation   = errors.New("malformed workflow jobs operation")
	errUnsupportedWorkflowJobsOperation = errors.New("unsupported workflow jobs operation")
	errInvalidWorkflowJobsOperation     = errors.New("invalid workflow jobs operation")
)

func decodeWorkflowJobsOperation(raw json.RawMessage) (workflows.WorkflowJobsOperation, error) {
	members, err := workflowJobsJSONObjectMembers(raw)
	if err != nil {
		return nil, errMalformedWorkflowJobsOperation
	}
	typeRaw, exists := members["type"]
	if !exists {
		return nil, errMalformedWorkflowJobsOperation
	}
	var operationType string
	if err := decodeWorkflowJobsJSON(typeRaw, &operationType, false); err != nil ||
		operationType == "" {
		return nil, errMalformedWorkflowJobsOperation
	}
	switch operationType {
	case "job.insert":
		if !workflowJobsExactFields(
			members,
			"type",
			"job_id",
			"index",
			"fields",
		) {
			return nil, errMalformedWorkflowJobsOperation
		}
		var wire workflowJobInsertWire
		if err := decodeWorkflowJobsJSON(raw, &wire, true); err != nil ||
			wire.Type != operationType ||
			!validWorkflowJobsJobID(wire.JobID) ||
			wire.Index == nil ||
			wire.Fields == nil {
			return nil, errMalformedWorkflowJobsOperation
		}
		fields, err := decodeWorkflowJobsMutations(wire.Fields, false, true)
		if err != nil {
			return nil, err
		}
		return workflows.WorkflowJobInsertOperation{
			JobID:  wire.JobID,
			Index:  *wire.Index,
			Fields: fields,
		}, nil
	case "job.delete":
		if !workflowJobsExactFields(members, "type", "job_id") {
			return nil, errMalformedWorkflowJobsOperation
		}
		var wire workflowJobDeleteWire
		if err := decodeWorkflowJobsJSON(raw, &wire, true); err != nil ||
			wire.Type != operationType ||
			!validWorkflowJobsJobID(wire.JobID) {
			return nil, errMalformedWorkflowJobsOperation
		}
		return workflows.WorkflowJobDeleteOperation{JobID: wire.JobID}, nil
	case "job.patch":
		if !workflowJobsAllowedFields(
			members,
			[]string{"type", "job_id", "fields"},
			[]string{"new_job_id"},
		) {
			return nil, errMalformedWorkflowJobsOperation
		}
		var wire workflowJobPatchWire
		if err := decodeWorkflowJobsJSON(raw, &wire, true); err != nil ||
			wire.Type != operationType ||
			!validWorkflowJobsJobID(wire.JobID) ||
			wire.Fields == nil {
			return nil, errMalformedWorkflowJobsOperation
		}
		fields, err := decodeWorkflowJobsMutations(wire.Fields, false, false)
		if err != nil {
			return nil, err
		}
		newJobID, err := decodeWorkflowJobIDMutation(wire.NewJobID)
		if err != nil {
			return nil, err
		}
		return workflows.WorkflowJobPatchOperation{
			JobID:    wire.JobID,
			NewJobID: newJobID,
			Fields:   fields,
		}, nil
	case "step.insert":
		if !workflowJobsExactFields(
			members,
			"type",
			"job_id",
			"index",
			"fields",
		) {
			return nil, errMalformedWorkflowJobsOperation
		}
		var wire workflowStepInsertWire
		if err := decodeWorkflowJobsJSON(raw, &wire, true); err != nil ||
			wire.Type != operationType ||
			!validWorkflowJobsJobID(wire.JobID) ||
			wire.Index == nil ||
			wire.Fields == nil {
			return nil, errMalformedWorkflowJobsOperation
		}
		fields, err := decodeWorkflowJobsMutations(wire.Fields, true, true)
		if err != nil {
			return nil, err
		}
		return workflows.WorkflowStepInsertOperation{
			JobID:  wire.JobID,
			Index:  *wire.Index,
			Fields: fields,
		}, nil
	case "step.delete":
		if !workflowJobsExactFields(members, "type", "job_id", "step_index") {
			return nil, errMalformedWorkflowJobsOperation
		}
		var wire workflowStepDeleteWire
		if err := decodeWorkflowJobsJSON(raw, &wire, true); err != nil ||
			wire.Type != operationType ||
			!validWorkflowJobsJobID(wire.JobID) ||
			wire.StepIndex == nil {
			return nil, errMalformedWorkflowJobsOperation
		}
		return workflows.WorkflowStepDeleteOperation{
			JobID:     wire.JobID,
			StepIndex: *wire.StepIndex,
		}, nil
	case "step.move":
		if !workflowJobsExactFields(
			members,
			"type",
			"job_id",
			"step_index",
			"to_index",
		) {
			return nil, errMalformedWorkflowJobsOperation
		}
		var wire workflowStepMoveWire
		if err := decodeWorkflowJobsJSON(raw, &wire, true); err != nil ||
			wire.Type != operationType ||
			!validWorkflowJobsJobID(wire.JobID) ||
			wire.StepIndex == nil ||
			wire.ToIndex == nil {
			return nil, errMalformedWorkflowJobsOperation
		}
		return workflows.WorkflowStepMoveOperation{
			JobID:     wire.JobID,
			StepIndex: *wire.StepIndex,
			ToIndex:   *wire.ToIndex,
		}, nil
	case "step.patch":
		if !workflowJobsExactFields(
			members,
			"type",
			"job_id",
			"step_index",
			"fields",
		) {
			return nil, errMalformedWorkflowJobsOperation
		}
		var wire workflowStepPatchWire
		if err := decodeWorkflowJobsJSON(raw, &wire, true); err != nil ||
			wire.Type != operationType ||
			!validWorkflowJobsJobID(wire.JobID) ||
			wire.StepIndex == nil ||
			wire.Fields == nil {
			return nil, errMalformedWorkflowJobsOperation
		}
		fields, err := decodeWorkflowJobsMutations(wire.Fields, true, false)
		if err != nil {
			return nil, err
		}
		return workflows.WorkflowStepPatchOperation{
			JobID:     wire.JobID,
			StepIndex: *wire.StepIndex,
			Fields:    fields,
		}, nil
	default:
		return nil, errUnsupportedWorkflowJobsOperation
	}
}

func decodeWorkflowJobIDMutation(raw json.RawMessage) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	members, err := workflowJobsJSONObjectMembers(raw)
	if err != nil ||
		!workflowJobsExactFields(members, "mode", "value") {
		return nil, errInvalidWorkflowJobsOperation
	}
	var mutation workflowJobsMutationWire
	if err := decodeWorkflowJobsJSON(raw, &mutation, true); err != nil ||
		mutation.Mode != string(workflows.WorkflowEditorMutationSet) ||
		mutation.Value == nil {
		return nil, errInvalidWorkflowJobsOperation
	}
	var value string
	if err := decodeWorkflowJobsJSON(mutation.Value, &value, false); err != nil {
		return nil, errInvalidWorkflowJobsOperation
	}
	if !validWorkflowJobsJobID(value) {
		return nil, errInvalidWorkflowJobsOperation
	}
	return &value, nil
}

func validWorkflowJobsJobID(value string) bool {
	if !utf8.ValidString(value) ||
		value == "" ||
		len(value) > workflows.MaxWorkflowJobsEditorIDBytes ||
		strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "\r\n") {
		return false
	}
	for _, character := range value {
		if unicode.Is(unicode.Cc, character) ||
			unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	return true
}

func decodeWorkflowJobsMutations(
	wire map[string]json.RawMessage,
	step bool,
	insert bool,
) (workflows.WorkflowEditorFieldMutations, error) {
	out := make(workflows.WorkflowEditorFieldMutations, len(wire))
	for field, rawMutation := range wire {
		if !workflowJobsFieldSupported(field, step) {
			return nil, errInvalidWorkflowJobsOperation
		}
		members, err := workflowJobsJSONObjectMembers(rawMutation)
		if err != nil {
			return nil, errInvalidWorkflowJobsOperation
		}
		var mutation workflowJobsMutationWire
		if err := decodeWorkflowJobsJSON(
			rawMutation,
			&mutation,
			true,
		); err != nil {
			return nil, errInvalidWorkflowJobsOperation
		}
		mode := workflows.WorkflowEditorMutationMode(mutation.Mode)
		switch mode {
		case workflows.WorkflowEditorMutationRemove:
			if insert ||
				!workflowJobsExactFields(members, "mode") ||
				mutation.Value != nil {
				return nil, errInvalidWorkflowJobsOperation
			}
			out[field] = workflows.WorkflowEditorFieldMutation{Mode: mode}
		case workflows.WorkflowEditorMutationSet:
			if !workflowJobsExactFields(members, "mode", "value") ||
				mutation.Value == nil {
				return nil, errInvalidWorkflowJobsOperation
			}
			value, err := decodeWorkflowJobsFieldValue(field, mutation.Value, step)
			if err != nil {
				return nil, errInvalidWorkflowJobsOperation
			}
			out[field] = workflows.WorkflowEditorFieldMutation{
				Mode:  mode,
				Value: value,
			}
		default:
			return nil, errInvalidWorkflowJobsOperation
		}
	}
	return out, nil
}

func workflowJobsFieldSupported(field string, step bool) bool {
	if step {
		switch field {
		case "id", "name", "uses", "if", "continue_on_error", "with", "context":
			return true
		default:
			return false
		}
	}
	switch field {
	case "name", "runs_on", "needs", "uses", "if", "continue_on_error",
		"with", "secrets", "outputs", "context":
		return true
	default:
		return false
	}
}

func decodeWorkflowJobsFieldValue(
	field string,
	raw json.RawMessage,
	step bool,
) (any, error) {
	switch field {
	case "id", "name", "runs_on", "uses", "if":
		var value string
		if err := decodeWorkflowJobsJSON(raw, &value, false); err != nil {
			return nil, err
		}
		return value, nil
	case "continue_on_error":
		var value bool
		if err := decodeWorkflowJobsJSON(raw, &value, false); err != nil {
			return nil, err
		}
		return value, nil
	case "needs":
		if step {
			return nil, errInvalidWorkflowJobsOperation
		}
		var value []string
		if err := decodeWorkflowJobsJSON(raw, &value, false); err != nil ||
			value == nil {
			return nil, errInvalidWorkflowJobsOperation
		}
		return value, nil
	case "with":
		return decodeWorkflowJobsJSONObject(raw)
	case "secrets":
		if step {
			return nil, errInvalidWorkflowJobsOperation
		}
		var text string
		if len(raw) > 0 && raw[0] == '"' {
			if err := decodeWorkflowJobsJSON(raw, &text, false); err != nil {
				return nil, err
			}
			return text, nil
		}
		return decodeWorkflowJobsJSONObject(raw)
	case "outputs", "context":
		var value map[string]string
		if err := decodeWorkflowJobsJSON(raw, &value, false); err != nil ||
			value == nil {
			return nil, errInvalidWorkflowJobsOperation
		}
		return value, nil
	default:
		return nil, errInvalidWorkflowJobsOperation
	}
}

func decodeWorkflowJobsJSONObject(raw json.RawMessage) (map[string]any, error) {
	var value map[string]any
	if err := decodeWorkflowJobsJSON(raw, &value, false); err != nil ||
		value == nil {
		return nil, errInvalidWorkflowJobsOperation
	}
	normalized, err := normalizeWorkflowTriggerJSONValue(value)
	if err != nil {
		return nil, err
	}
	out, ok := normalized.(map[string]any)
	if !ok {
		return nil, errInvalidWorkflowJobsOperation
	}
	return out, nil
}

func decodeStrictWorkflowJobsRequest(
	w http.ResponseWriter,
	r *http.Request,
	destination any,
	allowedFields []string,
) bool {
	if r.Body == nil {
		writeWorkflowJobsError(w, http.StatusBadRequest, "invalid_workflow_jobs_request", nil)
		return false
	}
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		writeWorkflowJobsError(
			w,
			http.StatusUnsupportedMediaType,
			"invalid_workflow_jobs_request",
			nil,
		)
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" {
		writeWorkflowJobsError(
			w,
			http.StatusUnsupportedMediaType,
			"invalid_workflow_jobs_request",
			nil,
		)
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(
		w,
		r.Body,
		workflowJobsEditorRequestMaxBytes,
	))
	if err != nil {
		var maximum *http.MaxBytesError
		if errors.As(err, &maximum) {
			writeWorkflowJobsError(
				w,
				http.StatusRequestEntityTooLarge,
				"workflow_jobs_request_too_large",
				nil,
			)
			return false
		}
		writeWorkflowJobsError(w, http.StatusBadRequest, "invalid_workflow_jobs_request", nil)
		return false
	}
	if !utf8.Valid(raw) ||
		!validJSONUnicodeScalars(raw) ||
		rejectDuplicateWorkflowJobsJSONKeys(raw) != nil ||
		!workflowJobsRawHasExactFields(raw, allowedFields) ||
		decodeWorkflowJobsJSON(raw, destination, true) != nil {
		writeWorkflowJobsError(w, http.StatusBadRequest, "invalid_workflow_jobs_request", nil)
		return false
	}
	return true
}

func validJSONUnicodeScalars(raw []byte) bool {
	inString := false
	for index := 0; index < len(raw); index++ {
		switch {
		case !inString:
			if raw[index] == '"' {
				inString = true
			}
		case raw[index] == '"':
			inString = false
		case raw[index] != '\\':
			continue
		default:
			if index+1 >= len(raw) {
				return false
			}
			if raw[index+1] != 'u' {
				index++
				continue
			}
			codeUnit, ok := jsonHexCodeUnit(raw[index+2:])
			if !ok {
				return false
			}
			index += 5
			switch {
			case codeUnit >= 0xd800 && codeUnit <= 0xdbff:
				if index+6 >= len(raw) ||
					raw[index+1] != '\\' ||
					raw[index+2] != 'u' {
					return false
				}
				low, lowOK := jsonHexCodeUnit(raw[index+3:])
				if !lowOK || low < 0xdc00 || low > 0xdfff {
					return false
				}
				index += 6
			case codeUnit >= 0xdc00 && codeUnit <= 0xdfff:
				return false
			}
		}
	}
	return !inString
}

func jsonHexCodeUnit(raw []byte) (uint16, bool) {
	if len(raw) < 4 {
		return 0, false
	}
	var value uint16
	for _, character := range raw[:4] {
		value <<= 4
		switch {
		case character >= '0' && character <= '9':
			value += uint16(character - '0')
		case character >= 'a' && character <= 'f':
			value += uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			value += uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func workflowJobsRawHasExactFields(raw []byte, fields []string) bool {
	members, err := workflowJobsJSONObjectMembers(raw)
	if err != nil {
		return false
	}
	return workflowJobsExactFields(members, fields...)
}

func workflowJobsJSONObjectMembers(
	raw []byte,
) (map[string]json.RawMessage, error) {
	var members map[string]json.RawMessage
	if err := decodeWorkflowJobsJSON(raw, &members, false); err != nil ||
		members == nil {
		if err == nil {
			err = errors.New("JSON object required")
		}
		return nil, err
	}
	return members, nil
}

func workflowJobsExactFields(
	members map[string]json.RawMessage,
	fields ...string,
) bool {
	if len(members) != len(fields) {
		return false
	}
	for _, field := range fields {
		if _, exists := members[field]; !exists {
			return false
		}
	}
	return true
}

func workflowJobsAllowedFields(
	members map[string]json.RawMessage,
	required []string,
	optional []string,
) bool {
	if len(members) < len(required) ||
		len(members) > len(required)+len(optional) {
		return false
	}
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, field := range required {
		if _, exists := members[field]; !exists {
			return false
		}
		allowed[field] = struct{}{}
	}
	for _, field := range optional {
		allowed[field] = struct{}{}
	}
	for field := range members {
		if _, exists := allowed[field]; !exists {
			return false
		}
	}
	return true
}

func decodeWorkflowJobsJSON(raw []byte, destination any, strict bool) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireWorkflowEventTriggerJSONEOF(decoder)
}

type workflowJobsJSONBudget struct {
	tokens int
}

func rejectDuplicateWorkflowJobsJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	budget := &workflowJobsJSONBudget{}
	if err := consumeUniqueWorkflowJobsJSONValue(
		decoder,
		0,
		budget,
	); err != nil {
		return err
	}
	return requireWorkflowEventTriggerJSONEOF(decoder)
}

func consumeUniqueWorkflowJobsJSONValue(
	decoder *json.Decoder,
	depth int,
	budget *workflowJobsJSONBudget,
) error {
	if depth > workflowJobsEditorJSONMaxDepth {
		return errors.New("JSON nesting exceeds limit")
	}
	budget.tokens++
	if budget.tokens > workflowJobsEditorJSONMaxTokens {
		return errors.New("JSON token count exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, composite := token.(json.Delim)
	if !composite {
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
			if err := consumeUniqueWorkflowJobsJSONValue(
				decoder,
				depth+1,
				budget,
			); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := consumeUniqueWorkflowJobsJSONValue(
				decoder,
				depth+1,
				budget,
			); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func writeWorkflowJobsError(
	w http.ResponseWriter,
	status int,
	code string,
	inspection *workflows.WorkflowJobsInspection,
) {
	writeWorkflowJobsJSON(
		w,
		status,
		workflowJobsErrorResponse{
			Error:      code,
			Inspection: inspection,
		},
	)
}

func writeWorkflowJobsJSON(w http.ResponseWriter, status int, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		writeWorkflowJobsFailure(w, "workflow_jobs_response_unavailable")
		return
	}
	if len(encoded)+1 > workflowJobsEditorResponseByteLimit {
		writeWorkflowJobsFailure(w, "workflow_jobs_response_too_large")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
	_, _ = w.Write([]byte{'\n'})
}

func writeWorkflowJobsFailure(w http.ResponseWriter, code string) {
	body := []byte(`{"error":"` + code + "\"}\n")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write(body)
}
