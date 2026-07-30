package workflows

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	MaxWorkflowJobsEditorJobs             = 256
	MaxWorkflowJobsEditorSteps            = 4096
	MaxWorkflowJobsEditorIDBytes          = 256
	MaxWorkflowJobsEditorJSONKeyBytes     = 256
	MaxWorkflowJobsEditorStringBytes      = 16 << 10
	MaxWorkflowJobsEditorJSONDepth        = 16
	MaxWorkflowJobsEditorJSONEntries      = 4096
	MaxWorkflowJobsEditorJSONEncodedBytes = 256 << 10
	MaxWorkflowJobsEditorYAMLDepth        = 64
	MaxWorkflowJobsEditorYAMLNodes        = 65536
	MaxWorkflowJobsEditorValidationBytes  = 1 << 20
)

var (
	ErrWorkflowJobsStaleRevision = errors.New("workflow jobs YAML revision is stale")
	ErrWorkflowJobsNotEditable   = errors.New("workflow jobs are not safely editable")
	ErrWorkflowJobsOperation     = errors.New("invalid workflow jobs operation")
	ErrWorkflowJobsTarget        = errors.New("workflow jobs target was not found")

	workflowJobsValidateSemantic = validateWorkflowJobsYAML
)

type WorkflowJobsEditorLimit string

const (
	WorkflowJobsEditorLimitJobs         WorkflowJobsEditorLimit = "jobs_truncated"
	WorkflowJobsEditorLimitSteps        WorkflowJobsEditorLimit = "steps_truncated"
	WorkflowJobsEditorLimitUnsafeFields WorkflowJobsEditorLimit = "unsafe_fields_omitted"
	WorkflowJobsEditorLimitValidation   WorkflowJobsEditorLimit = "validation_truncated"
)

type WorkflowEditorFieldProjection struct {
	Present bool `json:"present"`
	Value   any  `json:"value"`
}

type WorkflowJobProjection struct {
	ID                    string                                   `json:"id"`
	Index                 int                                      `json:"index"`
	Editable              bool                                     `json:"editable"`
	Reason                string                                   `json:"reason,omitempty"`
	AdvancedFieldsPresent bool                                     `json:"advanced_fields_present"`
	StepsPresent          bool                                     `json:"steps_present"`
	Fields                map[string]WorkflowEditorFieldProjection `json:"fields"`
	Steps                 []WorkflowStepProjection                 `json:"steps"`

	sourceStepCount        int
	stepsContainerEditable bool
	identityEditable       bool
}

type WorkflowStepProjection struct {
	Index                 int                                      `json:"index"`
	Editable              bool                                     `json:"editable"`
	Reason                string                                   `json:"reason,omitempty"`
	AdvancedFieldsPresent bool                                     `json:"advanced_fields_present"`
	Fields                map[string]WorkflowEditorFieldProjection `json:"fields"`
}

type WorkflowJobsInspection struct {
	Revision   string                         `json:"revision"`
	Editable   bool                           `json:"editable"`
	Reason     string                         `json:"reason,omitempty"`
	Complete   bool                           `json:"complete"`
	Limits     []WorkflowJobsEditorLimit      `json:"limits"`
	Jobs       []WorkflowJobProjection        `json:"jobs"`
	Validation *WorkflowDevelopmentValidation `json:"validation"`
}

type WorkflowEditorMutationMode string

const (
	WorkflowEditorMutationSet    WorkflowEditorMutationMode = "set"
	WorkflowEditorMutationRemove WorkflowEditorMutationMode = "remove"
)

type WorkflowEditorFieldMutation struct {
	Mode  WorkflowEditorMutationMode
	Value any
}

type WorkflowEditorFieldMutations map[string]WorkflowEditorFieldMutation

type WorkflowJobsOperation interface {
	workflowJobsOperation()
}

type WorkflowJobInsertOperation struct {
	JobID  string
	Index  int
	Fields WorkflowEditorFieldMutations
}

func (WorkflowJobInsertOperation) workflowJobsOperation() {}

type WorkflowJobDeleteOperation struct {
	JobID string
}

func (WorkflowJobDeleteOperation) workflowJobsOperation() {}

type WorkflowJobPatchOperation struct {
	JobID    string
	NewJobID *string
	Fields   WorkflowEditorFieldMutations
}

func (WorkflowJobPatchOperation) workflowJobsOperation() {}

type WorkflowStepInsertOperation struct {
	JobID  string
	Index  int
	Fields WorkflowEditorFieldMutations
}

func (WorkflowStepInsertOperation) workflowJobsOperation() {}

type WorkflowStepDeleteOperation struct {
	JobID     string
	StepIndex int
}

func (WorkflowStepDeleteOperation) workflowJobsOperation() {}

type WorkflowStepMoveOperation struct {
	JobID     string
	StepIndex int
	ToIndex   int
}

func (WorkflowStepMoveOperation) workflowJobsOperation() {}

type WorkflowStepPatchOperation struct {
	JobID     string
	StepIndex int
	Fields    WorkflowEditorFieldMutations
}

func (WorkflowStepPatchOperation) workflowJobsOperation() {}

var workflowJobEditorFieldNames = []string{
	"name",
	"runs_on",
	"needs",
	"uses",
	"if",
	"continue_on_error",
	"with",
	"secrets",
	"outputs",
	"context",
}

var workflowStepEditorFieldNames = []string{
	"id",
	"name",
	"uses",
	"if",
	"continue_on_error",
	"with",
	"context",
}

var workflowJobEditorYAMLFields = map[string]string{
	"name":              "name",
	"runs_on":           "runs-on",
	"needs":             "needs",
	"uses":              "uses",
	"if":                "if",
	"continue_on_error": "continue-on-error",
	"with":              "with",
	"secrets":           "secrets",
	"outputs":           "outputs",
	"context":           "context",
}

var workflowStepEditorYAMLFields = map[string]string{
	"id":                "id",
	"name":              "name",
	"uses":              "uses",
	"if":                "if",
	"continue_on_error": "continue-on-error",
	"with":              "with",
	"context":           "context",
}

// InspectWorkflowJobs returns an ordered, bounded projection of the jobs AST
// without changing the supplied source. Safe unknown fields remain in the raw
// source and are reported as advanced fields.
func InspectWorkflowJobs(raw string) WorkflowJobsInspection {
	inspection := WorkflowJobsInspection{
		Revision: workflowEditorRevision(raw),
		Complete: true,
		Limits:   make([]WorkflowJobsEditorLimit, 0),
		Jobs:     make([]WorkflowJobProjection, 0),
	}
	if reason := workflowJobsSourceDirectiveReason(raw); reason != "" {
		inspection.Reason = reason
		inspection.addLimit(WorkflowJobsEditorLimitUnsafeFields)
		inspection.Validation = newWorkflowJobsInvalidValidation(
			"Workflow YAML requires the raw editor.",
		)
		return inspection
	}
	document, err := decodeWorkflowEditorDocument(raw)
	if err != nil {
		reason := "Fix YAML syntax errors before using the structured jobs editor."
		if errors.Is(err, errWorkflowEditorMultipleDocuments) {
			reason = "Workflow YAML must contain exactly one document."
		}
		inspection.Reason = reason
		inspection.addLimit(WorkflowJobsEditorLimitUnsafeFields)
		inspection.Validation = newWorkflowJobsInvalidValidation(
			"Workflow YAML is not valid.",
		)
		return inspection
	}
	root, reason := editableWorkflowRoot(document)
	if reason != "" {
		inspection.Reason = reason
		inspection.addLimit(WorkflowJobsEditorLimitUnsafeFields)
		inspection.Validation = newWorkflowJobsInvalidValidation(reason)
		return inspection
	}
	if !workflowSafeCollectionNode(root, yaml.MappingNode, "!!map") {
		inspection.Reason = "Workflow YAML must use a plain top-level mapping."
		inspection.addLimit(WorkflowJobsEditorLimitUnsafeFields)
		inspection.Validation = newWorkflowJobsInvalidValidation(inspection.Reason)
		return inspection
	}
	if reason = workflowJobsASTReason(root); reason != "" {
		inspection.Reason = reason
		inspection.addLimit(WorkflowJobsEditorLimitUnsafeFields)
		inspection.Validation = newWorkflowJobsInvalidValidation(
			"Workflow YAML requires the raw editor.",
		)
		return inspection
	}
	var validationTruncated bool
	inspection.Validation, validationTruncated = workflowJobsValidateSemantic(raw)
	if validationTruncated {
		inspection.addLimit(WorkflowJobsEditorLimitValidation)
	}

	jobsIndexes := workflowMappingPairIndexes(root, "jobs")
	if len(jobsIndexes) > 1 {
		inspection.Reason = "Duplicate jobs mappings require the raw editor."
		inspection.addLimit(WorkflowJobsEditorLimitUnsafeFields)
		return inspection
	}
	if len(jobsIndexes) == 0 {
		inspection.Editable = true
		return inspection
	}
	jobsNode := root.Content[jobsIndexes[0]+1]
	if !workflowSafeCollectionNode(jobsNode, yaml.MappingNode, "!!map") {
		inspection.Reason = "The jobs value must be a plain mapping."
		inspection.addLimit(WorkflowJobsEditorLimitUnsafeFields)
		return inspection
	}
	if len(jobsNode.Content)%2 != 0 {
		inspection.Reason = "The jobs mapping is malformed and requires the raw editor."
		inspection.addLimit(WorkflowJobsEditorLimitUnsafeFields)
		return inspection
	}

	jobCount := len(jobsNode.Content) / 2
	if jobCount > MaxWorkflowJobsEditorJobs {
		inspection.addLimit(WorkflowJobsEditorLimitJobs)
		inspection.Reason = "The jobs mapping exceeds the structured editor limit."
	}
	projectedSteps := 0
	seenIDs := make(map[string]struct{}, min(jobCount, MaxWorkflowJobsEditorJobs))
	projectedCount := min(jobCount, MaxWorkflowJobsEditorJobs)
	for index := 0; index < projectedCount; index++ {
		key := jobsNode.Content[index*2]
		value := jobsNode.Content[index*2+1]
		job := inspectWorkflowJobNode(index, key, value)
		if job.ID != "" {
			if _, exists := seenIDs[job.ID]; exists {
				job.Editable = false
				job.Reason = "Duplicate job IDs require the raw editor."
				inspection.addLimit(WorkflowJobsEditorLimitUnsafeFields)
			}
			seenIDs[job.ID] = struct{}{}
		}
		remaining := MaxWorkflowJobsEditorSteps - projectedSteps
		if remaining < 0 {
			remaining = 0
		}
		if len(job.Steps) > remaining {
			job.Steps = job.Steps[:remaining]
		}
		projectedSteps += len(job.Steps)
		if job.sourceStepCount > remaining {
			inspection.addLimit(WorkflowJobsEditorLimitSteps)
			if job.Reason == "" {
				job.Reason = "The workflow exceeds the structured step limit."
			}
			job.Editable = false
		}
		if !job.Editable && inspection.Reason == "" {
			inspection.Reason = "One or more jobs require the raw editor."
		}
		if !job.Editable && job.sourceStepCount <= remaining {
			inspection.addLimit(WorkflowJobsEditorLimitUnsafeFields)
		}
		for _, step := range job.Steps {
			if !step.Editable {
				inspection.addLimit(WorkflowJobsEditorLimitUnsafeFields)
				if inspection.Reason == "" {
					inspection.Reason = "One or more steps require the raw editor."
				}
				break
			}
		}
		inspection.Jobs = append(inspection.Jobs, job)
	}
	inspection.Editable = !inspection.hasLimit(WorkflowJobsEditorLimitJobs) &&
		!inspection.hasLimit(WorkflowJobsEditorLimitSteps)
	if inspection.Editable {
		if !inspection.hasLimit(WorkflowJobsEditorLimitUnsafeFields) {
			inspection.Reason = ""
		}
	}
	return inspection
}

func inspectWorkflowJobNode(index int, key, node *yaml.Node) WorkflowJobProjection {
	job := WorkflowJobProjection{
		Index:                  index,
		Fields:                 newWorkflowEditorFields(workflowJobEditorFieldNames),
		Steps:                  make([]WorkflowStepProjection, 0),
		Editable:               true,
		stepsContainerEditable: true,
		identityEditable:       true,
	}
	if key != nil && workflowSafeStringScalar(key) {
		job.ID = key.Value
	}
	idReason := workflowJobsSafeIDReason(key)
	if !workflowSafeCollectionNode(node, yaml.MappingNode, "!!map") ||
		len(node.Content)%2 != 0 {
		job.Editable = false
		job.stepsContainerEditable = false
		job.Reason = "Each job must be a plain mapping."
		return job
	}

	seen := make(map[string]struct{}, len(node.Content)/2)
	setReason := func(reason string) {
		job.Editable = false
		if job.Reason == "" {
			job.Reason = reason
		}
	}
	if idReason != "" {
		job.identityEditable = false
		setReason(idReason)
	}
	for pair := 0; pair+1 < len(node.Content); pair += 2 {
		fieldKey := node.Content[pair]
		fieldValue := node.Content[pair+1]
		if reason := workflowJobsStructuralKeyReason(fieldKey); reason != "" {
			setReason(reason)
			continue
		}
		if _, exists := seen[fieldKey.Value]; exists {
			setReason("Duplicate job fields require the raw editor.")
			continue
		}
		seen[fieldKey.Value] = struct{}{}
		if fieldKey.Value == "steps" {
			job.StepsPresent = true
			steps, stepCount, reason := inspectWorkflowStepNodes(fieldValue)
			job.Steps = steps
			job.sourceStepCount = stepCount
			if stepCount > MaxWorkflowJobsEditorSteps && reason == "" {
				reason = "The workflow exceeds the structured step limit."
			}
			if reason != "" {
				job.stepsContainerEditable = false
				setReason(reason)
			}
			continue
		}
		jsonName := workflowEditorJSONFieldName(workflowJobEditorYAMLFields, fieldKey.Value)
		if jsonName == "" {
			job.AdvancedFieldsPresent = true
			continue
		}
		value, reason := projectWorkflowEditorKnownField(jsonName, fieldValue, false)
		if reason != "" {
			setReason(reason)
			continue
		}
		job.Fields[jsonName] = WorkflowEditorFieldProjection{Present: true, Value: value}
	}
	return job
}

func inspectWorkflowStepNodes(
	node *yaml.Node,
) ([]WorkflowStepProjection, int, string) {
	if !workflowSafeCollectionNode(node, yaml.SequenceNode, "!!seq") {
		return nil, 0, "Job steps must be a plain sequence."
	}
	steps := make([]WorkflowStepProjection, 0, min(len(node.Content), MaxWorkflowJobsEditorSteps))
	for index, stepNode := range node.Content {
		if index >= MaxWorkflowJobsEditorSteps {
			break
		}
		step := WorkflowStepProjection{
			Index:    index,
			Editable: true,
			Fields:   newWorkflowEditorFields(workflowStepEditorFieldNames),
		}
		if !workflowSafeCollectionNode(stepNode, yaml.MappingNode, "!!map") ||
			len(stepNode.Content)%2 != 0 {
			step.Editable = false
			step.Reason = "Each step must be a plain mapping."
			steps = append(steps, step)
			continue
		}
		seen := make(map[string]struct{}, len(stepNode.Content)/2)
		for pair := 0; pair+1 < len(stepNode.Content); pair += 2 {
			fieldKey := stepNode.Content[pair]
			fieldValue := stepNode.Content[pair+1]
			if reason := workflowJobsStructuralKeyReason(fieldKey); reason != "" {
				step.Editable = false
				step.Reason = reason
				break
			}
			if _, exists := seen[fieldKey.Value]; exists {
				step.Editable = false
				step.Reason = "Duplicate step fields require the raw editor."
				break
			}
			seen[fieldKey.Value] = struct{}{}
			jsonName := workflowEditorJSONFieldName(
				workflowStepEditorYAMLFields,
				fieldKey.Value,
			)
			if jsonName == "" {
				step.AdvancedFieldsPresent = true
				continue
			}
			value, reason := projectWorkflowEditorKnownField(jsonName, fieldValue, true)
			if reason != "" {
				step.Editable = false
				step.Reason = reason
				break
			}
			step.Fields[jsonName] = WorkflowEditorFieldProjection{
				Present: true,
				Value:   value,
			}
		}
		steps = append(steps, step)
	}
	return steps, len(node.Content), ""
}

func newWorkflowEditorFields(names []string) map[string]WorkflowEditorFieldProjection {
	fields := make(map[string]WorkflowEditorFieldProjection, len(names))
	for _, name := range names {
		fields[name] = WorkflowEditorFieldProjection{}
	}
	return fields
}

func workflowEditorJSONFieldName(fields map[string]string, yamlName string) string {
	for jsonName, candidate := range fields {
		if candidate == yamlName {
			return jsonName
		}
	}
	return ""
}

func projectWorkflowEditorKnownField(
	name string,
	node *yaml.Node,
	step bool,
) (any, string) {
	if eventYAMLNodeIsNull(node) {
		return nil, "Explicit null fields require the raw editor."
	}
	switch name {
	case "id":
		if reason := workflowJobsIdentityNodeReason(node); reason != "" {
			return nil, reason
		}
		return node.Value, ""
	case "name", "runs_on", "if":
		if reason := workflowJobsStringNodeReason(node, true); reason != "" {
			return nil, reason
		}
		return node.Value, ""
	case "uses":
		if reason := workflowJobsStringNodeReason(node, true); reason != "" {
			return nil, reason
		}
		return node.Value, ""
	case "continue_on_error":
		if reason := workflowBoolNodeReason(node); reason != "" {
			return nil, reason
		}
		return node.Value == "true", ""
	case "needs":
		if step {
			return nil, "Unsupported step fields require the raw editor."
		}
		values, reason := workflowJobsStringListProjection(node)
		return values, reason
	case "with":
		if !workflowSafeCollectionNode(node, yaml.MappingNode, "!!map") {
			return nil, "The with field must be a plain JSON object."
		}
		value, reason := workflowJobsJSONProjection(node)
		return value, reason
	case "secrets":
		if step {
			return nil, "Unsupported step fields require the raw editor."
		}
		if workflowSafeStringScalar(node) {
			if node.Value != "inherit" {
				return nil, "Job secrets must be inherit or a plain JSON object."
			}
			return node.Value, ""
		}
		if !workflowSafeCollectionNode(node, yaml.MappingNode, "!!map") {
			return nil, "Job secrets must be inherit or a plain JSON object."
		}
		value, reason := workflowJobsJSONProjection(node)
		return value, reason
	case "outputs":
		if step {
			return nil, "Unsupported step fields require the raw editor."
		}
		return workflowJobsStringMapProjection(node)
	case "context":
		return workflowJobsContextProjection(node)
	default:
		return nil, "Unsupported workflow fields require the raw editor."
	}
}

func workflowJobsStringNodeReason(node *yaml.Node, allowMultiline bool) string {
	if !workflowSafeStringScalar(node) {
		return "String values must use plain string tags."
	}
	if len(node.Value) > MaxWorkflowJobsEditorStringBytes ||
		!utf8.ValidString(node.Value) {
		return "String values exceed the structured editor limit."
	}
	if !allowMultiline && strings.ContainsAny(node.Value, "\r\n") {
		return "Multiline values require the raw editor."
	}
	if !workflowJobsSafeStringControls(node.Value) {
		return "String values contain unsupported control characters."
	}
	return ""
}

func workflowJobsStringListProjection(node *yaml.Node) ([]string, string) {
	switch {
	case workflowSafeStringScalar(node):
		if reason := workflowJobsIdentityNodeReason(node); reason != "" {
			return nil, reason
		}
		return []string{node.Value}, ""
	case workflowSafeCollectionNode(node, yaml.SequenceNode, "!!seq"):
		if len(node.Content) > MaxWorkflowJobsEditorJSONEntries {
			return nil, "String lists exceed the structured editor entry limit."
		}
		values := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if reason := workflowJobsIdentityNodeReason(item); reason != "" {
				return nil, "Needs must contain bounded single-line job IDs."
			}
			values = append(values, item.Value)
		}
		return values, ""
	default:
		return nil, "The needs field must be a string or plain string sequence."
	}
}

func workflowJobsStringMapProjection(node *yaml.Node) (any, string) {
	if !workflowSafeCollectionNode(node, yaml.MappingNode, "!!map") ||
		len(node.Content)%2 != 0 {
		return nil, "Outputs must be a plain string mapping."
	}
	if len(node.Content)/2 > MaxWorkflowJobsEditorJSONEntries {
		return nil, "Outputs exceed the structured editor entry limit."
	}
	out := make(map[string]string, len(node.Content)/2)
	for index := 0; index+1 < len(node.Content); index += 2 {
		key := node.Content[index]
		value := node.Content[index+1]
		if reason := workflowJobsJSONKeyReason(key); reason != "" {
			return nil, reason
		}
		if _, exists := out[key.Value]; exists {
			return nil, "Duplicate output names require the raw editor."
		}
		if reason := workflowJobsStringNodeReason(value, true); reason != "" {
			return nil, "Output values must be bounded strings."
		}
		out[key.Value] = value.Value
	}
	return out, ""
}

func workflowJobsContextProjection(node *yaml.Node) (any, string) {
	if !workflowSafeCollectionNode(node, yaml.MappingNode, "!!map") ||
		len(node.Content)%2 != 0 {
		return nil, "Context must be a plain mapping."
	}
	out := make(map[string]string, len(node.Content)/2)
	for index := 0; index+1 < len(node.Content); index += 2 {
		key := node.Content[index]
		value := node.Content[index+1]
		if reason := workflowJobsJSONKeyReason(key); reason != "" {
			return nil, reason
		}
		if key.Value != "session" && key.Value != "delivery" {
			return nil, "Unknown context fields require the raw editor."
		}
		if _, exists := out[key.Value]; exists {
			return nil, "Duplicate context fields require the raw editor."
		}
		if reason := workflowJobsStringNodeReason(value, true); reason != "" {
			return nil, reason
		}
		out[key.Value] = value.Value
	}
	return out, ""
}

func workflowJobsJSONProjection(node *yaml.Node) (any, string) {
	state := workflowJobsJSONState{}
	value, reason := state.project(node, 0)
	if reason != "" {
		return nil, reason
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > MaxWorkflowJobsEditorJSONEncodedBytes {
		return nil, "JSON-compatible values exceed the structured editor limit."
	}
	return value, ""
}

type workflowJobsJSONState struct {
	entries int
}

func (state *workflowJobsJSONState) project(node *yaml.Node, depth int) (any, string) {
	if node == nil || depth > MaxWorkflowJobsEditorJSONDepth {
		return nil, "JSON-compatible values exceed the structured editor depth limit."
	}
	state.entries++
	if state.entries > MaxWorkflowJobsEditorJSONEntries {
		return nil, "JSON-compatible values exceed the structured editor entry limit."
	}
	if eventYAMLNodeIsNull(node) {
		return nil, ""
	}
	switch node.Kind {
	case yaml.ScalarNode:
		switch node.ShortTag() {
		case "!!str":
			if reason := workflowJobsStringNodeReason(node, true); reason != "" {
				return nil, reason
			}
			return node.Value, ""
		case "!!bool":
			if reason := workflowBoolNodeReason(node); reason != "" {
				return nil, reason
			}
			return node.Value == "true", ""
		case "!!int":
			if !WorkflowJSONNumberIsBrowserSafe(node.Value) {
				return nil, "Numbers must be browser-safe JSON values."
			}
			if value, err := strconv.ParseInt(node.Value, 10, 64); err == nil {
				return value, ""
			}
			value, err := strconv.ParseUint(node.Value, 10, 64)
			if err != nil {
				return nil, "Numbers must be browser-safe JSON values."
			}
			return value, ""
		case "!!float":
			if !WorkflowJSONNumberIsBrowserSafe(node.Value) {
				return nil, "Numbers must be browser-safe JSON values."
			}
			value, err := strconv.ParseFloat(node.Value, 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, "Numbers must be finite JSON values."
			}
			return value, ""
		default:
			return nil, "Values must be JSON-compatible."
		}
	case yaml.SequenceNode:
		if !workflowSafeCollectionNode(node, yaml.SequenceNode, "!!seq") {
			return nil, "Arrays must be plain sequences."
		}
		out := make([]any, 0, len(node.Content))
		for _, item := range node.Content {
			value, reason := state.project(item, depth+1)
			if reason != "" {
				return nil, reason
			}
			out = append(out, value)
		}
		return out, ""
	case yaml.MappingNode:
		if !workflowSafeCollectionNode(node, yaml.MappingNode, "!!map") ||
			len(node.Content)%2 != 0 {
			return nil, "Objects must be plain mappings."
		}
		out := make(map[string]any, len(node.Content)/2)
		for index := 0; index+1 < len(node.Content); index += 2 {
			key := node.Content[index]
			if reason := workflowJobsJSONKeyReason(key); reason != "" {
				return nil, reason
			}
			if _, exists := out[key.Value]; exists {
				return nil, "Duplicate object keys require the raw editor."
			}
			value, reason := state.project(node.Content[index+1], depth+1)
			if reason != "" {
				return nil, reason
			}
			out[key.Value] = value
		}
		return out, ""
	default:
		return nil, "Values must be JSON-compatible."
	}
}

func workflowJobsJSONKeyReason(node *yaml.Node) string {
	if !workflowSafeStringScalar(node) ||
		!utf8.ValidString(node.Value) ||
		len(node.Value) == 0 ||
		len(node.Value) > MaxWorkflowJobsEditorJSONKeyBytes ||
		strings.ContainsAny(node.Value, "\r\n") ||
		workflowJobsContainsControlOrFormat(node.Value) {
		return "Object keys must be bounded non-empty strings."
	}
	return ""
}

func workflowJobsStructuralKeyReason(node *yaml.Node) string {
	if reason := workflowJobsJSONKeyReason(node); reason != "" {
		return "Mapping keys must be bounded plain single-line strings."
	}
	return ""
}

func workflowJobsSafeIDReason(node *yaml.Node) string {
	if !workflowSafeStringScalar(node) ||
		!utf8.ValidString(node.Value) ||
		len(node.Value) == 0 ||
		len(node.Value) > MaxWorkflowJobsEditorIDBytes ||
		strings.TrimSpace(node.Value) != node.Value ||
		strings.ContainsAny(node.Value, "\r\n") ||
		workflowJobsContainsControlOrFormat(node.Value) {
		return "Job IDs must be bounded, non-empty single-line strings."
	}
	return ""
}

func workflowJobsIdentityNodeReason(node *yaml.Node) string {
	if !workflowSafeStringScalar(node) ||
		!workflowJobsIdentityShapeSafe(node.Value) {
		return "IDs must be bounded single-line strings without control characters."
	}
	return ""
}

func workflowJobsSourceDirectiveReason(raw string) string {
	source := strings.TrimPrefix(raw, "\ufeff")
	for len(source) > 0 {
		line := source
		if newline := strings.IndexByte(source, '\n'); newline >= 0 {
			line = source[:newline]
			source = source[newline+1:]
		} else {
			source = ""
		}
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if workflowJobsDirectiveLine(line, "%YAML") ||
			workflowJobsDirectiveLine(line, "%TAG") {
			return "YAML directives require the raw editor."
		}
		if strings.HasPrefix(line, "%") {
			continue
		}
		break
	}
	return ""
}

func workflowJobsDirectiveLine(line, directive string) bool {
	if !strings.HasPrefix(line, directive) {
		return false
	}
	return len(line) == len(directive) ||
		line[len(directive)] == ' ' ||
		line[len(directive)] == '\t'
}

func workflowJobsASTReason(root *yaml.Node) string {
	type visit struct {
		node  *yaml.Node
		depth int
	}
	stack := []visit{{node: root}}
	visited := 0
	for len(stack) > 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		if current.node == nil {
			continue
		}
		visited++
		if visited > MaxWorkflowJobsEditorYAMLNodes ||
			current.depth > MaxWorkflowJobsEditorYAMLDepth {
			return "Workflow YAML exceeds the structured editor complexity limit."
		}
		node := current.node
		if node.Anchor != "" {
			return "YAML anchors require the raw editor."
		}
		if node.Kind == yaml.AliasNode {
			return "YAML aliases require the raw editor."
		}
		switch node.Kind {
		case yaml.DocumentNode:
		case yaml.MappingNode:
			if node.Tag != "" && node.ShortTag() != "!!map" {
				return "Custom YAML tags require the raw editor."
			}
			if len(node.Content)%2 != 0 {
				return "Malformed mappings require the raw editor."
			}
			seen := make(map[string]struct{}, len(node.Content)/2)
			for index := 0; index+1 < len(node.Content); index += 2 {
				key := node.Content[index]
				if eventYAMLNodeIsMergeKey(key) {
					return "YAML merge keys require the raw editor."
				}
				if reason := workflowJobsStructuralKeyReason(key); reason != "" {
					return reason
				}
				if _, exists := seen[key.Value]; exists {
					return "Duplicate mapping fields require the raw editor."
				}
				seen[key.Value] = struct{}{}
			}
		case yaml.SequenceNode:
			if node.Tag != "" && node.ShortTag() != "!!seq" {
				return "Custom YAML tags require the raw editor."
			}
		case yaml.ScalarNode:
			switch node.ShortTag() {
			case "!!str", "!!bool", "!!int", "!!float", "!!null":
			default:
				return "Custom YAML scalar tags require the raw editor."
			}
		default:
			return "Unsupported YAML nodes require the raw editor."
		}
		if len(stack)+len(node.Content) > MaxWorkflowJobsEditorYAMLNodes {
			return "Workflow YAML exceeds the structured editor complexity limit."
		}
		for index := len(node.Content) - 1; index >= 0; index-- {
			stack = append(stack, visit{
				node:  node.Content[index],
				depth: current.depth + 1,
			})
		}
	}
	return ""
}

func (inspection *WorkflowJobsInspection) addLimit(limit WorkflowJobsEditorLimit) {
	for _, current := range inspection.Limits {
		if current == limit {
			return
		}
	}
	inspection.Limits = append(inspection.Limits, limit)
	sort.Slice(inspection.Limits, func(left, right int) bool {
		return inspection.Limits[left] < inspection.Limits[right]
	})
	inspection.Complete = false
}

func (inspection *WorkflowJobsInspection) hasLimit(limit WorkflowJobsEditorLimit) bool {
	for _, current := range inspection.Limits {
		if current == limit {
			return true
		}
	}
	return false
}

func validateWorkflowJobsYAML(raw string) (*WorkflowDevelopmentValidation, bool) {
	validation := &WorkflowDevelopmentValidation{ValidatedAt: time.Now().UTC()}
	workflow, err := Parse([]byte(raw))
	if err != nil {
		validation.Errors = []WorkflowValidationIssue{{
			Message: "Workflow YAML could not be parsed.",
		}}
		return validation, false
	}
	if err := Validate(workflow); err != nil {
		var truncated bool
		validation.Errors, truncated = boundedWorkflowJobsValidationIssues(
			ValidationIssues(err),
		)
		return validation, truncated
	}
	validation.Valid = true
	return validation, false
}

func newWorkflowJobsInvalidValidation(message string) *WorkflowDevelopmentValidation {
	return &WorkflowDevelopmentValidation{
		Errors: []WorkflowValidationIssue{{
			Message: sanitizeWorkflowJobsValidationText(message, true),
		}},
		ValidatedAt: time.Now().UTC(),
	}
}

func boundedWorkflowJobsValidationIssues(
	issues []WorkflowValidationIssue,
) ([]WorkflowValidationIssue, bool) {
	truncated := false
	if len(issues) > 1024 {
		issues = issues[:1024]
		truncated = true
	}
	out := make([]WorkflowValidationIssue, 0, len(issues))
	usedBytes := 0
	for _, issue := range issues {
		path, pathTruncated := sanitizeWorkflowJobsValidationTextBounded(
			issue.Path,
			false,
		)
		message, messageTruncated := sanitizeWorkflowJobsValidationTextBounded(
			issue.Message,
			true,
		)
		truncated = truncated || pathTruncated || messageTruncated
		if message == "" {
			message = "Workflow validation failed."
		}
		candidateBytes := len(path) + len(message) + 64
		if usedBytes+candidateBytes > MaxWorkflowJobsEditorValidationBytes {
			truncated = true
			break
		}
		usedBytes += candidateBytes
		out = append(out, WorkflowValidationIssue{Path: path, Message: message})
	}
	if len(out) == 0 {
		out = append(out, WorkflowValidationIssue{
			Message: "Workflow validation failed.",
		})
	}
	return out, truncated
}

func sanitizeWorkflowJobsValidationText(value string, formatting bool) string {
	sanitized, _ := sanitizeWorkflowJobsValidationTextBounded(value, formatting)
	return sanitized
}

func sanitizeWorkflowJobsValidationTextBounded(
	value string,
	formatting bool,
) (string, bool) {
	var sanitized strings.Builder
	sanitized.Grow(min(len(value), MaxWorkflowJobsEditorStringBytes))
	truncated := false
	processedBytes := 0
	for offset, character := range value {
		allowed := !unicode.Is(unicode.Cf, character) &&
			(!unicode.Is(unicode.Cc, character) ||
				(formatting &&
					(character == '\t' || character == '\n' || character == '\r')))
		output := character
		if !allowed || character == utf8.RuneError {
			output = ' '
		}
		if sanitized.Len()+utf8.RuneLen(output) > MaxWorkflowJobsEditorStringBytes {
			truncated = true
			break
		}
		sanitized.WriteRune(output)
		processedBytes = offset + utf8.RuneLen(character)
	}
	out := sanitized.String()
	if processedBytes < len(value) {
		truncated = true
	}
	return out, truncated
}

// RenderWorkflowJobs applies one narrow operation to YAML nodes. Ordinary
// semantic validation failures are returned in the candidate inspection.
func RenderWorkflowJobs(
	raw string,
	revision string,
	operation WorkflowJobsOperation,
) (string, WorkflowJobsInspection, error) {
	inspection := InspectWorkflowJobs(raw)
	if !WorkflowJobsRevisionMatches(raw, revision) {
		return "", inspection, ErrWorkflowJobsStaleRevision
	}
	if operation == nil {
		return "", inspection, ErrWorkflowJobsOperation
	}
	if !inspection.Editable {
		return "", inspection, fmt.Errorf("%w: %s", ErrWorkflowJobsNotEditable, inspection.Reason)
	}
	if err := requireWorkflowJobsOperationEditable(inspection, operation); err != nil {
		return "", inspection, err
	}
	document, err := decodeWorkflowEditorDocument(raw)
	if err != nil {
		return "", inspection, err
	}
	root, reason := editableWorkflowRoot(document)
	if reason != "" {
		return "", inspection, ErrWorkflowJobsNotEditable
	}
	changed, err := applyWorkflowJobsOperation(root, operation)
	if err != nil {
		return "", inspection, err
	}
	if !changed {
		return raw, inspection, nil
	}
	var rendered bytes.Buffer
	encoder := yaml.NewEncoder(&rendered)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		_ = encoder.Close()
		return "", inspection, err
	}
	if err := encoder.Close(); err != nil {
		return "", inspection, err
	}
	next := rendered.String()
	nextInspection := InspectWorkflowJobs(next)
	if !nextInspection.Editable {
		return "", inspection, fmt.Errorf(
			"%w: rendered jobs did not remain safely editable",
			ErrWorkflowJobsOperation,
		)
	}
	return next, nextInspection, nil
}

// WorkflowJobsRevisionMatches checks the exact-source optimistic concurrency
// fence without decoding or semantically inspecting the YAML.
func WorkflowJobsRevisionMatches(raw, revision string) bool {
	return revision != "" && revision == workflowEditorRevision(raw)
}

func requireWorkflowJobsOperationEditable(
	inspection WorkflowJobsInspection,
	operation WorkflowJobsOperation,
) error {
	job := func(id string) (*WorkflowJobProjection, error) {
		if err := validateWorkflowJobsID(id); err != nil {
			return nil, err
		}
		for index := range inspection.Jobs {
			if inspection.Jobs[index].ID == id {
				return &inspection.Jobs[index], nil
			}
		}
		return nil, ErrWorkflowJobsTarget
	}
	step := func(id string, index int) error {
		selectedJob, err := job(id)
		if err != nil {
			return err
		}
		if index < 0 || index >= len(selectedJob.Steps) {
			return ErrWorkflowJobsTarget
		}
		if !selectedJob.identityEditable {
			return fmt.Errorf(
				"%w: job identity requires the raw editor",
				ErrWorkflowJobsNotEditable,
			)
		}
		if !selectedJob.Steps[index].Editable {
			return fmt.Errorf(
				"%w: %s",
				ErrWorkflowJobsNotEditable,
				selectedJob.Steps[index].Reason,
			)
		}
		return nil
	}
	requireStepInsert := func(id string) error {
		selectedJob, err := job(id)
		if err != nil {
			return err
		}
		if !selectedJob.stepsContainerEditable {
			return fmt.Errorf(
				"%w: job steps container requires the raw editor",
				ErrWorkflowJobsNotEditable,
			)
		}
		if !selectedJob.identityEditable {
			return fmt.Errorf(
				"%w: job identity requires the raw editor",
				ErrWorkflowJobsNotEditable,
			)
		}
		return nil
	}
	requireStepMove := func(id string, fromIndex, toIndex int) error {
		selectedJob, err := job(id)
		if err != nil {
			return err
		}
		if fromIndex < 0 ||
			fromIndex >= len(selectedJob.Steps) ||
			toIndex < 0 ||
			toIndex >= len(selectedJob.Steps) {
			return ErrWorkflowJobsTarget
		}
		if !selectedJob.identityEditable {
			return fmt.Errorf(
				"%w: job identity requires the raw editor",
				ErrWorkflowJobsNotEditable,
			)
		}
		first, last := fromIndex, toIndex
		if first > last {
			first, last = last, first
		}
		for index := first; index <= last; index++ {
			if !selectedJob.Steps[index].Editable {
				return fmt.Errorf(
					"%w: move range contains a raw-only step",
					ErrWorkflowJobsNotEditable,
				)
			}
		}
		return nil
	}
	switch typed := operation.(type) {
	case WorkflowJobInsertOperation:
		return nil
	case *WorkflowJobInsertOperation:
		if typed == nil {
			return ErrWorkflowJobsOperation
		}
		return nil
	case WorkflowJobDeleteOperation:
		selectedJob, err := job(typed.JobID)
		if err != nil {
			return err
		}
		if !selectedJob.Editable {
			return fmt.Errorf("%w: %s", ErrWorkflowJobsNotEditable, selectedJob.Reason)
		}
		for _, selectedStep := range selectedJob.Steps {
			if !selectedStep.Editable {
				return fmt.Errorf(
					"%w: job contains a raw-only step",
					ErrWorkflowJobsNotEditable,
				)
			}
		}
		return nil
	case *WorkflowJobDeleteOperation:
		if typed == nil {
			return ErrWorkflowJobsOperation
		}
		return requireWorkflowJobsOperationEditable(
			inspection,
			*typed,
		)
	case WorkflowJobPatchOperation:
		selectedJob, err := job(typed.JobID)
		if err != nil {
			return err
		}
		if !selectedJob.Editable {
			return fmt.Errorf("%w: %s", ErrWorkflowJobsNotEditable, selectedJob.Reason)
		}
		return nil
	case *WorkflowJobPatchOperation:
		if typed == nil {
			return ErrWorkflowJobsOperation
		}
		return requireWorkflowJobsOperationEditable(
			inspection,
			*typed,
		)
	case WorkflowStepInsertOperation:
		return requireStepInsert(typed.JobID)
	case *WorkflowStepInsertOperation:
		if typed == nil {
			return ErrWorkflowJobsOperation
		}
		return requireStepInsert(typed.JobID)
	case WorkflowStepDeleteOperation:
		return step(typed.JobID, typed.StepIndex)
	case *WorkflowStepDeleteOperation:
		if typed == nil {
			return ErrWorkflowJobsOperation
		}
		return step(typed.JobID, typed.StepIndex)
	case WorkflowStepMoveOperation:
		return requireStepMove(typed.JobID, typed.StepIndex, typed.ToIndex)
	case *WorkflowStepMoveOperation:
		if typed == nil {
			return ErrWorkflowJobsOperation
		}
		return requireStepMove(typed.JobID, typed.StepIndex, typed.ToIndex)
	case WorkflowStepPatchOperation:
		return step(typed.JobID, typed.StepIndex)
	case *WorkflowStepPatchOperation:
		if typed == nil {
			return ErrWorkflowJobsOperation
		}
		return step(typed.JobID, typed.StepIndex)
	default:
		return ErrWorkflowJobsOperation
	}
}

func applyWorkflowJobsOperation(root *yaml.Node, operation WorkflowJobsOperation) (bool, error) {
	switch typed := operation.(type) {
	case WorkflowJobInsertOperation:
		return applyWorkflowJobInsert(root, typed)
	case *WorkflowJobInsertOperation:
		if typed == nil {
			return false, ErrWorkflowJobsOperation
		}
		return applyWorkflowJobInsert(root, *typed)
	case WorkflowJobDeleteOperation:
		return applyWorkflowJobDelete(root, typed)
	case *WorkflowJobDeleteOperation:
		if typed == nil {
			return false, ErrWorkflowJobsOperation
		}
		return applyWorkflowJobDelete(root, *typed)
	case WorkflowJobPatchOperation:
		return applyWorkflowJobPatch(root, typed)
	case *WorkflowJobPatchOperation:
		if typed == nil {
			return false, ErrWorkflowJobsOperation
		}
		return applyWorkflowJobPatch(root, *typed)
	case WorkflowStepInsertOperation:
		return applyWorkflowStepInsert(root, typed)
	case *WorkflowStepInsertOperation:
		if typed == nil {
			return false, ErrWorkflowJobsOperation
		}
		return applyWorkflowStepInsert(root, *typed)
	case WorkflowStepDeleteOperation:
		return applyWorkflowStepDelete(root, typed)
	case *WorkflowStepDeleteOperation:
		if typed == nil {
			return false, ErrWorkflowJobsOperation
		}
		return applyWorkflowStepDelete(root, *typed)
	case WorkflowStepMoveOperation:
		return applyWorkflowStepMove(root, typed)
	case *WorkflowStepMoveOperation:
		if typed == nil {
			return false, ErrWorkflowJobsOperation
		}
		return applyWorkflowStepMove(root, *typed)
	case WorkflowStepPatchOperation:
		return applyWorkflowStepPatch(root, typed)
	case *WorkflowStepPatchOperation:
		if typed == nil {
			return false, ErrWorkflowJobsOperation
		}
		return applyWorkflowStepPatch(root, *typed)
	default:
		return false, ErrWorkflowJobsOperation
	}
}

func applyWorkflowJobInsert(
	root *yaml.Node,
	operation WorkflowJobInsertOperation,
) (bool, error) {
	if err := validateWorkflowJobsID(operation.JobID); err != nil {
		return false, err
	}
	jobs, err := ensureWorkflowJobsMapping(root)
	if err != nil {
		return false, err
	}
	if operation.Index < 0 || operation.Index > len(jobs.Content)/2 {
		return false, fmt.Errorf("%w: job insertion index", ErrWorkflowJobsOperation)
	}
	if workflowMappingPairIndex(jobs, operation.JobID) >= 0 {
		return false, fmt.Errorf("%w: duplicate job id", ErrWorkflowJobsOperation)
	}
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if _, err := applyWorkflowEditorFields(
		node,
		operation.Fields,
		workflowJobEditorYAMLFields,
		false,
		true,
	); err != nil {
		return false, err
	}
	offset := operation.Index * 2
	pair := []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: operation.JobID},
		node,
	}
	jobs.Content = append(jobs.Content, nil, nil)
	copy(jobs.Content[offset+2:], jobs.Content[offset:len(jobs.Content)-2])
	copy(jobs.Content[offset:offset+2], pair)
	return true, nil
}

func applyWorkflowJobDelete(
	root *yaml.Node,
	operation WorkflowJobDeleteOperation,
) (bool, error) {
	jobs, err := existingWorkflowJobsMapping(root)
	if err != nil {
		return false, err
	}
	if jobs == nil {
		return false, ErrWorkflowJobsTarget
	}
	index := workflowMappingPairIndex(jobs, operation.JobID)
	if index < 0 {
		return false, ErrWorkflowJobsTarget
	}
	jobs.Content = append(jobs.Content[:index], jobs.Content[index+2:]...)
	return true, nil
}

func applyWorkflowJobPatch(
	root *yaml.Node,
	operation WorkflowJobPatchOperation,
) (bool, error) {
	jobs, err := existingWorkflowJobsMapping(root)
	if err != nil || jobs == nil {
		if err != nil {
			return false, err
		}
		return false, ErrWorkflowJobsTarget
	}
	index := workflowMappingPairIndex(jobs, operation.JobID)
	if index < 0 {
		return false, ErrWorkflowJobsTarget
	}
	changed := false
	if operation.NewJobID != nil {
		if idErr := validateWorkflowJobsID(*operation.NewJobID); idErr != nil {
			return false, idErr
		}
		if *operation.NewJobID != operation.JobID {
			if workflowMappingPairIndex(jobs, *operation.NewJobID) >= 0 {
				return false, fmt.Errorf("%w: duplicate job id", ErrWorkflowJobsOperation)
			}
			jobs.Content[index].Value = *operation.NewJobID
			jobs.Content[index].Tag = "!!str"
			changed = true
		}
	}
	fieldsChanged, err := applyWorkflowEditorFields(
		jobs.Content[index+1],
		operation.Fields,
		workflowJobEditorYAMLFields,
		false,
		false,
	)
	return changed || fieldsChanged, err
}

func applyWorkflowStepInsert(
	root *yaml.Node,
	operation WorkflowStepInsertOperation,
) (bool, error) {
	job, err := workflowJobsTargetJob(root, operation.JobID)
	if err != nil {
		return false, err
	}
	steps, err := ensureWorkflowJobSteps(job)
	if err != nil {
		return false, err
	}
	if operation.Index < 0 || operation.Index > len(steps.Content) {
		return false, fmt.Errorf("%w: step insertion index", ErrWorkflowJobsOperation)
	}
	step := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if _, err := applyWorkflowEditorFields(
		step,
		operation.Fields,
		workflowStepEditorYAMLFields,
		true,
		true,
	); err != nil {
		return false, err
	}
	steps.Content = append(steps.Content, nil)
	copy(steps.Content[operation.Index+1:], steps.Content[operation.Index:len(steps.Content)-1])
	steps.Content[operation.Index] = step
	return true, nil
}

func applyWorkflowStepDelete(
	root *yaml.Node,
	operation WorkflowStepDeleteOperation,
) (bool, error) {
	steps, err := workflowJobsTargetSteps(root, operation.JobID)
	if err != nil {
		return false, err
	}
	if operation.StepIndex < 0 || operation.StepIndex >= len(steps.Content) {
		return false, ErrWorkflowJobsTarget
	}
	steps.Content = append(
		steps.Content[:operation.StepIndex],
		steps.Content[operation.StepIndex+1:]...,
	)
	return true, nil
}

func applyWorkflowStepMove(
	root *yaml.Node,
	operation WorkflowStepMoveOperation,
) (bool, error) {
	steps, err := workflowJobsTargetSteps(root, operation.JobID)
	if err != nil {
		return false, err
	}
	if operation.StepIndex < 0 ||
		operation.StepIndex >= len(steps.Content) ||
		operation.ToIndex < 0 ||
		operation.ToIndex >= len(steps.Content) {
		return false, ErrWorkflowJobsTarget
	}
	if operation.StepIndex == operation.ToIndex {
		return false, nil
	}
	moved := steps.Content[operation.StepIndex]
	if operation.StepIndex < operation.ToIndex {
		copy(
			steps.Content[operation.StepIndex:operation.ToIndex],
			steps.Content[operation.StepIndex+1:operation.ToIndex+1],
		)
	} else {
		copy(
			steps.Content[operation.ToIndex+1:operation.StepIndex+1],
			steps.Content[operation.ToIndex:operation.StepIndex],
		)
	}
	steps.Content[operation.ToIndex] = moved
	return true, nil
}

func applyWorkflowStepPatch(
	root *yaml.Node,
	operation WorkflowStepPatchOperation,
) (bool, error) {
	steps, err := workflowJobsTargetSteps(root, operation.JobID)
	if err != nil {
		return false, err
	}
	if operation.StepIndex < 0 || operation.StepIndex >= len(steps.Content) {
		return false, ErrWorkflowJobsTarget
	}
	return applyWorkflowEditorFields(
		steps.Content[operation.StepIndex],
		operation.Fields,
		workflowStepEditorYAMLFields,
		true,
		false,
	)
}

func applyWorkflowEditorFields(
	mapping *yaml.Node,
	mutations WorkflowEditorFieldMutations,
	fields map[string]string,
	step bool,
	insert bool,
) (bool, error) {
	if !workflowSafeCollectionNode(mapping, yaml.MappingNode, "!!map") {
		return false, ErrWorkflowJobsNotEditable
	}
	changed := false
	for _, jsonName := range workflowEditorOrderedMutationNames(fields, step) {
		mutation, exists := mutations[jsonName]
		if !exists {
			continue
		}
		yamlName, supported := fields[jsonName]
		if !supported {
			return false, fmt.Errorf("%w: unsupported field", ErrWorkflowJobsOperation)
		}
		index := workflowMappingPairIndex(mapping, yamlName)
		switch mutation.Mode {
		case WorkflowEditorMutationRemove:
			if insert {
				return false, fmt.Errorf("%w: insert fields must use set", ErrWorkflowJobsOperation)
			}
			if mutation.Value != nil {
				return false, fmt.Errorf("%w: remove must not include value", ErrWorkflowJobsOperation)
			}
			if index >= 0 {
				mapping.Content = append(
					mapping.Content[:index],
					mapping.Content[index+2:]...,
				)
				changed = true
			}
		case WorkflowEditorMutationSet:
			replacement, err := workflowEditorMutationNode(jsonName, mutation.Value, step)
			if err != nil {
				return false, err
			}
			if index >= 0 {
				current, reason := projectWorkflowEditorKnownField(
					jsonName,
					mapping.Content[index+1],
					step,
				)
				if reason == "" && workflowTriggerValuesEqual(current, mutation.Value) {
					continue
				}
				copyWorkflowEditorNodeComments(replacement, mapping.Content[index+1])
				mapping.Content[index+1] = replacement
			} else {
				mapping.Content = append(
					mapping.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: yamlName},
					replacement,
				)
			}
			changed = true
		default:
			return false, fmt.Errorf("%w: unsupported mutation mode", ErrWorkflowJobsOperation)
		}
	}
	if len(mutations) > len(fields) {
		return false, fmt.Errorf("%w: unsupported field", ErrWorkflowJobsOperation)
	}
	for name := range mutations {
		if _, exists := fields[name]; !exists {
			return false, fmt.Errorf("%w: unsupported field", ErrWorkflowJobsOperation)
		}
	}
	return changed, nil
}

func workflowEditorOrderedMutationNames(fields map[string]string, step bool) []string {
	if step {
		return workflowStepEditorFieldNames
	}
	return workflowJobEditorFieldNames
}

func workflowEditorMutationNode(name string, value any, step bool) (*yaml.Node, error) {
	if err := validateWorkflowEditorMutationValue(name, value, step); err != nil {
		return nil, err
	}
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return nil, fmt.Errorf("%w: encode field", ErrWorkflowJobsOperation)
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		node = *node.Content[0]
	}
	if reason := workflowJobsASTReason(&node); reason != "" {
		return nil, fmt.Errorf("%w: unsafe field value", ErrWorkflowJobsOperation)
	}
	if _, reason := projectWorkflowEditorKnownField(name, &node, step); reason != "" {
		return nil, fmt.Errorf("%w: %s", ErrWorkflowJobsOperation, reason)
	}
	return &node, nil
}

func validateWorkflowEditorMutationValue(name string, value any, step bool) error {
	invalid := func() error {
		return fmt.Errorf("%w: invalid %s value", ErrWorkflowJobsOperation, name)
	}
	switch name {
	case "id":
		text, ok := value.(string)
		if !ok || !workflowJobsIdentityShapeSafe(text) {
			return invalid()
		}
	case "name", "runs_on", "if":
		text, ok := value.(string)
		if !ok || !workflowJobsMutationStringSafe(text, true) {
			return invalid()
		}
	case "uses":
		text, ok := value.(string)
		if !ok || !workflowJobsMutationStringSafe(text, false) {
			return invalid()
		}
		if text != "" {
			if step {
				if strings.TrimSpace(text) != text || !validStepUses(text) {
					return fmt.Errorf("%w: invalid step target", ErrWorkflowJobsOperation)
				}
			} else {
				canonical, err := CanonicalLocalRef(text)
				if err != nil || canonical != text {
					return fmt.Errorf("%w: invalid reusable workflow target", ErrWorkflowJobsOperation)
				}
			}
		}
	case "continue_on_error":
		if _, ok := value.(bool); !ok {
			return invalid()
		}
	case "needs":
		if step {
			return invalid()
		}
		values, ok := value.([]string)
		if !ok {
			return invalid()
		}
		for _, item := range values {
			if !workflowJobsIdentityShapeSafe(item) ||
				strings.TrimSpace(item) != item ||
				item == "" {
				return invalid()
			}
		}
	case "with":
		if _, ok := value.(map[string]any); !ok {
			return invalid()
		}
		if err := validateWorkflowJobsJSONValue(value); err != nil {
			return err
		}
	case "secrets":
		if step {
			return invalid()
		}
		if text, ok := value.(string); ok {
			if text != "inherit" {
				return invalid()
			}
			break
		}
		if _, ok := value.(map[string]any); !ok {
			return invalid()
		}
		if err := validateWorkflowJobsJSONValue(value); err != nil {
			return err
		}
	case "outputs":
		if step {
			return invalid()
		}
		values, ok := value.(map[string]string)
		if !ok {
			return invalid()
		}
		for key, item := range values {
			if !workflowJobsMutationKeySafe(key) ||
				!workflowJobsMutationStringSafe(item, true) {
				return invalid()
			}
		}
	case "context":
		values, ok := value.(map[string]string)
		if !ok {
			return invalid()
		}
		for key, item := range values {
			if (key != "session" && key != "delivery") ||
				!workflowJobsMutationStringSafe(item, true) {
				return invalid()
			}
		}
	default:
		return invalid()
	}
	return nil
}

func validateWorkflowJobsJSONValue(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > MaxWorkflowJobsEditorJSONEncodedBytes {
		return fmt.Errorf("%w: JSON value exceeds limit", ErrWorkflowJobsOperation)
	}
	var entries int
	var visit func(any, int) error
	visit = func(candidate any, depth int) error {
		if depth > MaxWorkflowJobsEditorJSONDepth {
			return fmt.Errorf("%w: JSON value exceeds depth", ErrWorkflowJobsOperation)
		}
		entries++
		if entries > MaxWorkflowJobsEditorJSONEntries {
			return fmt.Errorf("%w: JSON value exceeds entries", ErrWorkflowJobsOperation)
		}
		switch typed := candidate.(type) {
		case nil, bool:
			return nil
		case string:
			if !workflowJobsMutationStringSafe(typed, true) {
				return fmt.Errorf("%w: JSON string exceeds limit", ErrWorkflowJobsOperation)
			}
		case int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64:
			number := fmt.Sprint(typed)
			if !WorkflowJSONNumberIsBrowserSafe(number) {
				return fmt.Errorf("%w: unsafe JSON number", ErrWorkflowJobsOperation)
			}
		case float32:
			number := strconv.FormatFloat(float64(typed), 'g', -1, 32)
			if !WorkflowJSONNumberIsBrowserSafe(number) {
				return fmt.Errorf("%w: unsafe JSON number", ErrWorkflowJobsOperation)
			}
		case float64:
			number := strconv.FormatFloat(typed, 'g', -1, 64)
			if !WorkflowJSONNumberIsBrowserSafe(number) {
				return fmt.Errorf("%w: unsafe JSON number", ErrWorkflowJobsOperation)
			}
		case []any:
			for _, item := range typed {
				if err := visit(item, depth+1); err != nil {
					return err
				}
			}
		case map[string]any:
			for key, item := range typed {
				if !workflowJobsMutationKeySafe(key) {
					return fmt.Errorf("%w: invalid JSON key", ErrWorkflowJobsOperation)
				}
				if err := visit(item, depth+1); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("%w: non-JSON value", ErrWorkflowJobsOperation)
		}
		return nil
	}
	return visit(value, 0)
}

func workflowJobsMutationStringSafe(value string, multiline bool) bool {
	return utf8.ValidString(value) &&
		len(value) <= MaxWorkflowJobsEditorStringBytes &&
		(multiline || !strings.ContainsAny(value, "\r\n")) &&
		workflowJobsSafeStringControls(value)
}

func workflowJobsMutationKeySafe(value string) bool {
	return utf8.ValidString(value) &&
		value != "" &&
		len(value) <= MaxWorkflowJobsEditorJSONKeyBytes &&
		!strings.ContainsAny(value, "\r\n") &&
		!workflowJobsContainsControlOrFormat(value)
}

func workflowJobsIdentityShapeSafe(value string) bool {
	return utf8.ValidString(value) &&
		len(value) <= MaxWorkflowJobsEditorIDBytes &&
		!strings.ContainsAny(value, "\r\n") &&
		!workflowJobsContainsControlOrFormat(value)
}

func validateWorkflowJobsID(id string) error {
	if !workflowJobsIdentityShapeSafe(id) ||
		id == "" ||
		strings.TrimSpace(id) != id ||
		strings.ContainsAny(id, "\r\n") {
		return fmt.Errorf("%w: invalid job id", ErrWorkflowJobsOperation)
	}
	return nil
}

func workflowJobsSafeStringControls(value string) bool {
	for _, character := range value {
		if unicode.Is(unicode.Cf, character) {
			return false
		}
		if unicode.Is(unicode.Cc, character) &&
			character != '\t' &&
			character != '\n' &&
			character != '\r' {
			return false
		}
	}
	return true
}

func workflowJobsContainsControlOrFormat(value string) bool {
	for _, character := range value {
		if unicode.Is(unicode.Cc, character) ||
			unicode.Is(unicode.Cf, character) {
			return true
		}
	}
	return false
}

func ensureWorkflowJobsMapping(root *yaml.Node) (*yaml.Node, error) {
	indexes := workflowMappingPairIndexes(root, "jobs")
	if len(indexes) > 1 {
		return nil, ErrWorkflowJobsNotEditable
	}
	if len(indexes) == 1 {
		node := root.Content[indexes[0]+1]
		if !workflowSafeCollectionNode(node, yaml.MappingNode, "!!map") {
			return nil, ErrWorkflowJobsNotEditable
		}
		return node, nil
	}
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(
		root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "jobs"},
		node,
	)
	return node, nil
}

func existingWorkflowJobsMapping(root *yaml.Node) (*yaml.Node, error) {
	indexes := workflowMappingPairIndexes(root, "jobs")
	if len(indexes) == 0 {
		return nil, nil
	}
	if len(indexes) > 1 {
		return nil, ErrWorkflowJobsNotEditable
	}
	node := root.Content[indexes[0]+1]
	if !workflowSafeCollectionNode(node, yaml.MappingNode, "!!map") {
		return nil, ErrWorkflowJobsNotEditable
	}
	return node, nil
}

func workflowJobsTargetJob(root *yaml.Node, id string) (*yaml.Node, error) {
	jobs, err := existingWorkflowJobsMapping(root)
	if err != nil || jobs == nil {
		if err != nil {
			return nil, err
		}
		return nil, ErrWorkflowJobsTarget
	}
	index := workflowMappingPairIndex(jobs, id)
	if index < 0 {
		return nil, ErrWorkflowJobsTarget
	}
	return jobs.Content[index+1], nil
}

func workflowJobsTargetSteps(root *yaml.Node, id string) (*yaml.Node, error) {
	job, err := workflowJobsTargetJob(root, id)
	if err != nil {
		return nil, err
	}
	index := workflowMappingPairIndex(job, "steps")
	if index < 0 {
		return nil, ErrWorkflowJobsTarget
	}
	node := job.Content[index+1]
	if !workflowSafeCollectionNode(node, yaml.SequenceNode, "!!seq") {
		return nil, ErrWorkflowJobsNotEditable
	}
	return node, nil
}

func ensureWorkflowJobSteps(job *yaml.Node) (*yaml.Node, error) {
	index := workflowMappingPairIndex(job, "steps")
	if index >= 0 {
		node := job.Content[index+1]
		if !workflowSafeCollectionNode(node, yaml.SequenceNode, "!!seq") {
			return nil, ErrWorkflowJobsNotEditable
		}
		return node, nil
	}
	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	job.Content = append(
		job.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "steps"},
		node,
	)
	return node, nil
}

func workflowMappingPairIndex(mapping *yaml.Node, name string) int {
	indexes := workflowMappingPairIndexes(mapping, name)
	if len(indexes) != 1 {
		return -1
	}
	return indexes[0]
}

func copyWorkflowEditorNodeComments(destination, source *yaml.Node) {
	if destination == nil || source == nil {
		return
	}
	if destination.Kind == source.Kind {
		destination.Style = source.Style
	}
	destination.HeadComment = source.HeadComment
	destination.LineComment = source.LineComment
	destination.FootComment = source.FootComment
}
