package workflows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

const (
	GateExecUses = "gate/exec"
	// MaxGateActionWorkflowDepth is the closed nesting limit enforced for
	// private workflow actions and their recursive readiness checks.
	MaxGateActionWorkflowDepth = defaultMaxCallDepth

	GateActorHuman         = "human"
	GateActorAI            = "ai"
	GateActorDeterministic = "deterministic"
	GateActorWorkflow      = "workflow"

	MaxGateActionRevisionBytes  = 512
	privateRootKindGateActionV3 = "gate-action-v3"
)

type GateAction = gatetypes.GateAction
type GateActionType = gatetypes.GateActionType
type GateDefinition = gatetypes.GateDefinition
type GateField = gatetypes.GateField
type GateFieldOption = gatetypes.GateFieldOption
type GateFieldType = gatetypes.GateFieldType

const (
	GateActionHuman         = gatetypes.GateActionHuman
	GateActionAI            = gatetypes.GateActionAI
	GateActionDeterministic = gatetypes.GateActionDeterministic
	GateActionWorkflow      = gatetypes.GateActionWorkflow

	GateFieldShortText = gatetypes.GateFieldShortText
	GateFieldLongText  = gatetypes.GateFieldLongText
	GateFieldBoolean   = gatetypes.GateFieldBoolean
	GateFieldSelect    = gatetypes.GateFieldSelect
	GateSessionSource  = AgentSessionSource
)

// GateActionResolveRequest is detached before being given to an injected
// configuration resolver. GateRef is always the canonical full local path
// gates.<id> and DefaultAction is nil when the workflow intentionally has no
// fallback.
type GateActionResolveRequest struct {
	WorkflowRef      string         `json:"workflow-ref"`
	WorkflowRevision string         `json:"workflow-revision,omitempty"`
	GateRef          string         `json:"gate-ref"`
	Gate             GateDefinition `json:"gate"`
	DefaultAction    *GateAction    `json:"default-action,omitempty"`
	RunID            string         `json:"run-id"`
	JobID            string         `json:"job-id"`
	StepID           string         `json:"step-id"`
}

// GateActionResolution contains an optional override and the immutable
// configuration revision that selected it. A nil Action explicitly means
// "use the workflow default".
type GateActionResolution struct {
	Action   *GateAction `json:"action,omitempty"`
	Revision string      `json:"revision,omitempty"`
}

type GateActionResolver interface {
	ResolveGateAction(context.Context, GateActionResolveRequest) (GateActionResolution, error)
}

// GateForm is the durable browser-safe form projected by a Human gate action.
type GateForm struct {
	GateRef string      `json:"gate-ref"`
	Prompt  string      `json:"prompt"`
	Fields  []GateField `json:"fields,omitempty"`
}

// GateCompilationV3 is a trusted one-step invocation compiled from a
// published workflow's gate catalog. The returned workflow retains the entire
// catalog so gate-ref resolution and action validation use the published
// definitions, while unrelated catalog jobs cannot execute in this private
// invocation.
type GateCompilationV3 struct {
	Workflow    *Workflow           `json:"-"`
	PrivateRoot *PrivateRootRequest `json:"-"`
	GateRef     string              `json:"gate-ref"`
}

func CompileGateWorkflowV3(
	workflow *Workflow,
	gateRef string,
	privateValues map[string]any,
) (*GateCompilationV3, error) {
	if workflow == nil {
		return nil, fmt.Errorf("gate workflow is required")
	}
	gateRef, err := canonicalGateRef(gateRef)
	if err != nil {
		return nil, err
	}
	gateID := strings.TrimPrefix(gateRef, "gates.")
	if _, exists := workflow.Gates[gateID]; !exists {
		return nil, fmt.Errorf("gate-ref %q does not exist", gateRef)
	}
	encodedGates, err := json.Marshal(workflow.Gates)
	if err != nil {
		return nil, fmt.Errorf("clone workflow gates: %w", err)
	}
	var gates map[string]GateDefinition
	if err := decodeJSONWithNumbers(encodedGates, &gates); err != nil {
		return nil, fmt.Errorf("clone workflow gates: %w", err)
	}
	compiled := &Workflow{
		Name:  strings.TrimSpace(workflow.Name),
		On:    WorkflowTriggers{Manual: map[string]any{}},
		Gates: gates,
		Jobs: map[string]Job{
			workflowGateJobID: {
				Name:   "Execute " + gateRef,
				RunsOn: "picoclaw",
				Steps: []Step{{
					ID:   "gate-exec",
					Uses: GateExecUses,
					With: map[string]any{"gate-ref": gateRef},
				}},
			},
		},
	}
	if compiled.Name == "" {
		compiled.Name = "Execute " + gateRef
	}
	if err := Validate(compiled); err != nil {
		return nil, fmt.Errorf("compiled gate workflow is invalid: %w", err)
	}
	if privateValues == nil {
		privateValues = map[string]any{}
	}
	normalized, err := normalizeWorkflowGateValue(
		"private gate values",
		privateValues,
		MaxWorkflowGateInputsBytes,
	)
	if err != nil {
		return nil, err
	}
	values, ok := normalized.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("private gate values must be an object")
	}
	encodedValues, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encode private gate values: %w", err)
	}
	privateRoot := &PrivateRootRequest{
		Values:                values,
		privateValuesRevision: workflowHashBytes(encodedValues),
	}
	encodedWorkflow, err := json.Marshal(compiled)
	if err != nil {
		return nil, fmt.Errorf("encode compiled gate workflow: %w", err)
	}
	compiled.privateRootRevision = workflowHashBytes(encodedWorkflow)
	return &GateCompilationV3{
		Workflow: compiled, PrivateRoot: privateRoot, GateRef: gateRef,
	}, nil
}

func compileGateActionWorkflowV3(
	workflow *Workflow,
	privateValues map[string]any,
) (*GateCompilationV3, error) {
	if workflow == nil {
		return nil, fmt.Errorf("gate action workflow is required")
	}
	encoded, err := json.Marshal(workflow)
	if err != nil {
		return nil, fmt.Errorf("clone gate action workflow: %w", err)
	}
	var compiled Workflow
	if err := decodeJSONWithNumbers(encoded, &compiled); err != nil {
		return nil, fmt.Errorf("clone gate action workflow: %w", err)
	}
	if compiled.On.WorkflowCall == nil {
		return nil, fmt.Errorf("gate action workflow must declare workflow-call")
	}
	if err := Validate(&compiled); err != nil {
		return nil, fmt.Errorf("gate action workflow is invalid: %w", err)
	}
	if privateValues == nil {
		privateValues = map[string]any{}
	}
	normalized, err := normalizeWorkflowGateValue(
		"private gate action values", privateValues, MaxWorkflowGateInputsBytes,
	)
	if err != nil {
		return nil, err
	}
	values, ok := normalized.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("private gate action values must be an object")
	}
	encodedValues, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encode private gate action values: %w", err)
	}
	privateRoot := &PrivateRootRequest{
		Values: values, privateValuesRevision: workflowHashBytes(encodedValues),
	}
	encodedWorkflow, err := json.Marshal(&compiled)
	if err != nil {
		return nil, fmt.Errorf("encode compiled gate action workflow: %w", err)
	}
	compiled.privateRootKind = privateRootKindGateActionV3
	compiled.privateRootRevision = workflowHashBytes(encodedWorkflow)
	return &GateCompilationV3{Workflow: &compiled, PrivateRoot: privateRoot}, nil
}

type resolvedGateAction struct {
	GateRef                string
	Gate                   GateDefinition
	Action                 GateAction
	ActionRevision         string
	ActionWorkflow         *Workflow
	ActionWorkflowRevision string
	ExecutionID            string
	InputHash              string
}

type gateActionWorkflowContinuation struct {
	ChildRunID     string         `json:"child-run-id"`
	ChildTaskID    string         `json:"child-task-id"`
	GateRef        string         `json:"gate-ref"`
	Gate           GateDefinition `json:"gate"`
	ExecutionID    string         `json:"execution-id"`
	ActionRevision string         `json:"action-revision"`
	InputHash      string         `json:"input-hash"`
}

type gateActionWorkflowWaitingError struct {
	ChildRunID string
	Resolved   resolvedGateAction
}

func (err gateActionWorkflowWaitingError) Error() string {
	return "gate action workflow is waiting for input"
}

func (e *Executor) newGateActionWorkflowProxyTask(
	ctx context.Context,
	parent *Run,
	jobID string,
	stepID string,
	waiting gateActionWorkflowWaitingError,
) (WorkflowHumanTask, error) {
	if parent == nil {
		return WorkflowHumanTask{}, ErrHumanTaskConflict
	}
	childExecutor := *e
	childExecutor.GateActions = nil
	tasks, err := childExecutor.ListHumanTasks(ctx, waiting.ChildRunID)
	if err != nil {
		return WorkflowHumanTask{}, err
	}
	var child *WorkflowHumanTask
	for index := range tasks {
		if tasks[index].Status == HumanTaskStatusWaiting {
			child = &tasks[index]
			break
		}
	}
	if child == nil {
		return WorkflowHumanTask{}, fmt.Errorf(
			"%w: waiting gate action workflow has no Human task",
			ErrHumanTaskConflict,
		)
	}
	actorKind := child.ActorKind
	if actorKind == "" {
		actorKind = GateActorHuman
	}
	executionID := child.ExecutionID
	if executionID == "" {
		executionID = waiting.Resolved.ExecutionID
	}
	proxy := cloneWorkflowHumanTask(*child)
	proxy.ID = gateProxyHumanTaskID(parent.ID, jobID, stepID, child.ID)
	proxy.RunID = parent.ID
	proxy.WorkflowRef = parent.WorkflowRef
	proxy.JobID = jobID
	proxy.StepID = stepID
	proxy.Status = HumanTaskStatusWaiting
	proxy.ResponseID = ""
	proxy.Response = nil
	proxy.AnsweredAt = nil
	proxy.CanceledAt = nil
	proxy.RetryAt = nil
	proxy.ActorKind = actorKind
	proxy.ExecutionID = executionID
	proxy.ActionRevision = waiting.Resolved.ActionRevision
	proxy.GateWorkflow = &gateActionWorkflowContinuation{
		ChildRunID:     waiting.ChildRunID,
		ChildTaskID:    child.ID,
		GateRef:        waiting.Resolved.GateRef,
		Gate:           cloneGateDefinition(waiting.Resolved.Gate),
		ExecutionID:    waiting.Resolved.ExecutionID,
		ActionRevision: waiting.Resolved.ActionRevision,
		InputHash:      waiting.Resolved.InputHash,
	}
	return proxy, nil
}

func gateExecutionID(runID, jobID, stepID string) string {
	digest := sha256.Sum256([]byte(runID + "\x00" + jobID + "\x00" + stepID))
	return "ge_" + hex.EncodeToString(digest[:16])
}

func canonicalGateRef(value string) (string, error) {
	if value != strings.TrimSpace(value) || !strings.HasPrefix(value, "gates.") {
		return "", fmt.Errorf("gate-ref must use the full gates.<gate-id> path")
	}
	id := strings.TrimPrefix(value, "gates.")
	if !gatetypes.GateIDPattern.MatchString(id) || len(id) > gatetypes.MaxGateDefinitionIDBytes {
		return "", fmt.Errorf("gate-ref must use the full gates.<gate-id> path")
	}
	return "gates." + id, nil
}

func gateDefinitionForRef(workflow *Workflow, ref string) (GateDefinition, error) {
	canonical, err := canonicalGateRef(ref)
	if err != nil {
		return GateDefinition{}, err
	}
	if workflow == nil {
		return GateDefinition{}, fmt.Errorf("workflow is unavailable")
	}
	id := strings.TrimPrefix(canonical, "gates.")
	gate, exists := workflow.Gates[id]
	if !exists {
		return GateDefinition{}, fmt.Errorf("gate-ref %q does not exist", canonical)
	}
	return cloneGateDefinition(gate), nil
}

func cloneGateDefinition(gate GateDefinition) GateDefinition {
	gate.Fields = append([]GateField(nil), gate.Fields...)
	for index := range gate.Fields {
		gate.Fields[index].Options = append([]GateFieldOption(nil), gate.Fields[index].Options...)
	}
	if gate.DefaultAction != nil {
		action := cloneGateAction(*gate.DefaultAction)
		gate.DefaultAction = &action
	}
	return gate
}

func cloneGateAction(action GateAction) GateAction {
	if action.Fields != nil {
		action.Fields = cloneMap(action.Fields)
	}
	return action
}

func (e *Executor) resolveGateAction(
	ctx context.Context,
	run *Run,
	jobID string,
	stepID string,
	gateRef string,
) (resolvedGateAction, error) {
	if run == nil || run.execution == nil || run.execution.Workflow == nil {
		return resolvedGateAction{}, fmt.Errorf("gate/exec requires a pinned workflow snapshot")
	}
	gateRef, err := canonicalGateRef(gateRef)
	if err != nil {
		return resolvedGateAction{}, err
	}
	gate, err := gateDefinitionForRef(run.execution.Workflow, gateRef)
	if err != nil {
		return resolvedGateAction{}, err
	}
	request := GateActionResolveRequest{
		WorkflowRef:      run.WorkflowRef,
		WorkflowRevision: run.execution.WorkflowRevision,
		GateRef:          gateRef,
		Gate:             cloneGateDefinition(gate),
		RunID:            run.ID,
		JobID:            jobID,
		StepID:           stepID,
	}
	if gate.DefaultAction != nil {
		fallback := cloneGateAction(*gate.DefaultAction)
		request.DefaultAction = &fallback
	}
	resolution := GateActionResolution{}
	if e != nil && e.GateActions != nil {
		resolution, err = e.GateActions.ResolveGateAction(ctx, request)
		if err != nil {
			return resolvedGateAction{}, fmt.Errorf("resolve gate action: %w", err)
		}
	}
	var action GateAction
	if resolution.Action != nil {
		action = cloneGateAction(*resolution.Action)
	} else if gate.DefaultAction != nil {
		action = cloneGateAction(*gate.DefaultAction)
	} else {
		return resolvedGateAction{}, fmt.Errorf("gate %q has no configured action or default-action", gateRef)
	}
	if err := validateRuntimeGateAction(action); err != nil {
		return resolvedGateAction{}, fmt.Errorf("gate %q action: %w", gateRef, err)
	}
	revision := strings.TrimSpace(resolution.Revision)
	if revision != resolution.Revision || !utf8.ValidString(revision) || len(revision) > MaxGateActionRevisionBytes {
		return resolvedGateAction{}, fmt.Errorf("gate %q action revision is invalid", gateRef)
	}
	if revision == "" {
		encoded, encodeErr := json.Marshal(action)
		if encodeErr != nil {
			return resolvedGateAction{}, fmt.Errorf("encode gate action: %w", encodeErr)
		}
		digest := sha256.Sum256(encoded)
		revision = "sha256:" + hex.EncodeToString(digest[:])
	}
	var actionWorkflow *Workflow
	actionWorkflowRevision := ""
	if action.Type == GateActionWorkflow {
		loaded, _, loadErr := e.loadWorkflow(ctx, RunRequest{Ref: action.WorkflowRef})
		if loadErr != nil {
			return resolvedGateAction{}, fmt.Errorf("load gate action workflow: %w", loadErr)
		}
		if validateErr := Validate(loaded); validateErr != nil {
			return resolvedGateAction{}, fmt.Errorf("validate gate action workflow: %w", validateErr)
		}
		snapshot, snapshotErr := newWorkflowExecutionState(loaded)
		if snapshotErr != nil {
			return resolvedGateAction{}, snapshotErr
		}
		actionWorkflow = snapshot.Workflow
		actionWorkflowRevision = snapshot.WorkflowRevision
		revision = combinedGateActionWorkflowRevision(
			revision,
			action.WorkflowRef,
			actionWorkflowRevision,
		)
	}
	pinned := map[string]any{
		"workflow-revision": run.execution.WorkflowRevision,
		"gate-ref":          gateRef,
		"gate":              gate,
		"action":            action,
		"action-revision":   revision,
	}
	if run.privateRoot != nil {
		pinned["private-root-revision"] = run.privateRoot.Revision
		pinned["private-run-binding"] = run.privateRoot.RunBinding
	}
	encoded, encodeErr := json.Marshal(pinned)
	if encodeErr != nil {
		return resolvedGateAction{}, fmt.Errorf("encode gate execution input: %w", encodeErr)
	}
	digest := sha256.Sum256(encoded)
	return resolvedGateAction{
		GateRef:                gateRef,
		Gate:                   gate,
		Action:                 action,
		ActionRevision:         revision,
		ActionWorkflow:         actionWorkflow,
		ActionWorkflowRevision: actionWorkflowRevision,
		ExecutionID:            gateExecutionID(run.ID, jobID, stepID),
		InputHash:              "sha256:" + hex.EncodeToString(digest[:]),
	}, nil
}

func combinedGateActionWorkflowRevision(
	resolverRevision string,
	workflowRef string,
	workflowRevision string,
) string {
	digest := sha256.Sum256([]byte(
		"gate-action-workflow-v1\x00" + resolverRevision + "\x00" + workflowRef + "\x00" + workflowRevision,
	))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validateRuntimeGateAction(action GateAction) error {
	if err := gatetypes.ValidateGateAction(action); err != nil {
		return err
	}
	switch action.Type {
	case GateActionAI:
		if action.Session == AgentSessionSource {
			if action.AgentID != "" || action.History != "" || action.Cache != "" || action.Tools != "" {
				return fmt.Errorf("source AI action derives agent, history, cache, and tools")
			}
			return nil
		}
		if action.AgentID != strings.TrimSpace(action.AgentID) || !routing.IsCanonicalAgentID(action.AgentID) {
			return fmt.Errorf("agent-id must be an exact canonical agent ID")
		}
		if action.Session != AgentSessionPrivate && !validAgentSessionMode(action.Session) {
			return fmt.Errorf("unsupported session mode")
		}
		if action.History != "" && !validHistoryMode(action.History) {
			return fmt.Errorf("unsupported history mode")
		}
		if action.Cache != "" && !validCacheMode(action.Cache) {
			return fmt.Errorf("unsupported cache mode")
		}
		if action.Tools != "" && !validAgentToolsMode(action.Tools) {
			return fmt.Errorf("unsupported tools mode")
		}
	case GateActionWorkflow:
		canonical, err := CanonicalLocalRef(action.WorkflowRef)
		if err != nil || canonical != action.WorkflowRef {
			return fmt.Errorf("workflow-ref must be an exact canonical local workflow reference")
		}
	}
	return nil
}

func gateFieldValuesSchema(fields []GateField) map[string]any {
	properties := make(map[string]any, len(fields))
	required := make([]string, 0, len(fields))
	for _, field := range fields {
		var schema map[string]any
		switch field.Type {
		case GateFieldShortText, GateFieldLongText:
			schema = map[string]any{"type": "string"}
			if field.Required {
				required = append(required, field.ID)
			}
		case GateFieldBoolean:
			schema = map[string]any{"type": "boolean"}
			if field.Required {
				required = append(required, field.ID)
			}
		case GateFieldSelect:
			enum := make([]any, 0, len(field.Options))
			for _, option := range field.Options {
				enum = append(enum, option.ID)
			}
			if field.MaxSelections == 1 {
				schema = map[string]any{"type": "string", "enum": enum}
			} else {
				schema = map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string", "enum": enum},
				}
			}
			if field.MinSelections > 0 {
				required = append(required, field.ID)
			}
		}
		properties[field.ID] = schema
	}
	fieldValues := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		fieldValues["required"] = required
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"field-values": fieldValues,
		},
		"required":             []string{"field-values"},
		"additionalProperties": false,
	}
}

func validateGateFieldValues(fields []GateField, raw any) (map[string]any, error) {
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("field-values must be an object")
	}
	known := make(map[string]GateField, len(fields))
	for _, field := range fields {
		known[field.ID] = field
	}
	for id := range values {
		if _, exists := known[id]; !exists {
			return nil, fmt.Errorf("field-values contains unknown field %q", id)
		}
	}
	out := make(map[string]any, len(values))
	for _, field := range fields {
		value, exists := values[field.ID]
		if !exists {
			if field.Required || field.Type == GateFieldSelect && field.MinSelections > 0 {
				return nil, fmt.Errorf("field %q is required", field.ID)
			}
			continue
		}
		switch field.Type {
		case GateFieldShortText, GateFieldLongText:
			text, valid := value.(string)
			if !valid || !utf8.ValidString(text) {
				return nil, fmt.Errorf("field %q must be a string", field.ID)
			}
			if field.Required && strings.TrimSpace(text) == "" {
				return nil, fmt.Errorf("field %q must be nonblank", field.ID)
			}
			out[field.ID] = text
		case GateFieldBoolean:
			boolean, valid := value.(bool)
			if !valid {
				return nil, fmt.Errorf("field %q must be a boolean", field.ID)
			}
			out[field.ID] = boolean
		case GateFieldSelect:
			selected, err := normalizeGateSelections(field, value)
			if err != nil {
				return nil, err
			}
			if field.MaxSelections == 1 {
				if len(selected) == 1 {
					out[field.ID] = selected[0]
				}
			} else {
				items := make([]any, len(selected))
				for index, item := range selected {
					items[index] = item
				}
				out[field.ID] = items
			}
		}
	}
	encoded, err := json.Marshal(map[string]any{"field-values": out})
	if err != nil || len(encoded) > MaxHumanTaskPayloadBytes {
		return nil, fmt.Errorf("field-values exceed %d bytes", MaxHumanTaskPayloadBytes)
	}
	return out, nil
}

// ValidateGateFieldValues validates and detaches one completed gate response.
// Product integrations can use it for local fallbacks without reimplementing
// the runtime's type, cardinality, option, and unknown-field checks.
func ValidateGateFieldValues(
	fields []GateField,
	values map[string]any,
) (map[string]any, error) {
	return validateGateFieldValues(fields, values)
}

func (e *Executor) executeGate(
	ctx context.Context,
	run *Run,
	jobID string,
	stepID string,
	with map[string]any,
	execCtx ExecutionContext,
	jobs map[string]JobExecution,
	callDepth int,
) (map[string]any, *WorkflowHumanTask, error) {
	if rawDepth, exists := execCtx.Inputs["gate-action-depth"]; exists {
		switch value := rawDepth.(type) {
		case json.Number:
			if parsed, err := value.Int64(); err == nil && parsed > int64(callDepth) {
				callDepth = int(parsed)
			}
		case float64:
			if value > float64(callDepth) {
				callDepth = int(value)
			}
		case int:
			if value > callDepth {
				callDepth = value
			}
		}
	}
	if callDepth > defaultMaxCallDepth {
		return nil, nil, fmt.Errorf("workflow call depth exceeded")
	}
	gateRef, ok := with["gate-ref"].(string)
	if !ok {
		return nil, nil, fmt.Errorf("gate/exec gate-ref must be a string")
	}
	resolved, err := e.resolveGateAction(ctx, run, jobID, stepID, gateRef)
	if err != nil {
		return nil, nil, err
	}
	switch resolved.Action.Type {
	case GateActionHuman:
		if run.ParentRunID != "" {
			return nil, nil, fmt.Errorf(
				"%w: Human gate/exec cannot run inside a reusable workflow call",
				ErrHumanTaskUnsupported,
			)
		}
		task, err := newWorkflowGateHumanTask(run, jobID, stepID, resolved)
		if err != nil {
			return nil, nil, err
		}
		return nil, &task, nil
	case GateActionAI:
		fieldValues, err := e.executeAIGateAction(ctx, resolved, execCtx)
		if err != nil {
			return nil, nil, err
		}
		return gateExecutionOutputs(resolved, GateActorAI, fieldValues), nil, nil
	case GateActionDeterministic:
		rendered, err := renderMap(
			resolved.Action.Fields,
			expressionCtxFrom(execCtx, jobs),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("evaluate deterministic gate action: %w", err)
		}
		fieldValues, err := validateGateFieldValues(resolved.Gate.Fields, rendered)
		if err != nil {
			return nil, nil, fmt.Errorf("deterministic gate action: %w", err)
		}
		return gateExecutionOutputs(resolved, GateActorDeterministic, fieldValues), nil, nil
	case GateActionWorkflow:
		fieldValues, childRunID, err := e.executeGateWorkflowAction(
			ctx, run, resolved, execCtx, callDepth,
		)
		if childRunID != "" && !IsPrivateWorkflowRun(run) {
			seen := false
			for _, existing := range run.ChildRunIDs {
				if existing == childRunID {
					seen = true
					break
				}
			}
			if !seen {
				run.ChildRunIDs = append(run.ChildRunIDs, childRunID)
			}
		}
		if err != nil {
			return nil, nil, err
		}
		return gateExecutionOutputs(resolved, GateActorWorkflow, fieldValues), nil, nil
	default:
		return nil, nil, fmt.Errorf("unsupported gate action type %q", resolved.Action.Type)
	}
}

func (e *Executor) executeAIGateAction(
	ctx context.Context,
	resolved resolvedGateAction,
	execCtx ExecutionContext,
) (map[string]any, error) {
	if e == nil || e.Agents == nil {
		return nil, fmt.Errorf("agent runner not configured")
	}
	private := execCtx.privateValues != nil || execCtx.frozenReadOnlySession != nil
	sourceMode := resolved.Action.Session == AgentSessionSource
	agentID := resolved.Action.AgentID
	tools := strings.TrimSpace(resolved.Action.Tools)
	if tools == "" {
		tools = AgentToolsInherit
	}
	history := strings.TrimSpace(resolved.Action.History)
	cache := strings.TrimSpace(resolved.Action.Cache)
	sessionMode := strings.TrimSpace(resolved.Action.Session)
	if sourceMode {
		if execCtx.frozenReadOnlySession == nil {
			return nil, fmt.Errorf("source AI gate action requires originating execution provenance")
		}
		agentID = execCtx.frozenReadOnlySession.AgentID
		history = "read_only"
		cache = "none"
		tools = AgentToolsNone
		sessionMode = AgentSessionPrivate
	}
	if sessionMode == AgentSessionPrivate {
		if execCtx.frozenReadOnlySession == nil {
			return nil, fmt.Errorf("private AI gate action requires a frozen read-only session")
		}
		history = "read_only"
		if cache == "" {
			cache = "session"
		}
	}
	if private {
		if tools != AgentToolsNone {
			return nil, fmt.Errorf("private AI gate action requires tools: none")
		}
		if history != "none" && history != "read_only" {
			return nil, fmt.Errorf("private AI gate action requires history: none or read_only")
		}
	}
	sessionKey := execCtx.Session
	if sessionMode == AgentSessionEphemeral {
		sessionKey = ""
	}
	var frozenSession *FrozenReadOnlySession
	if history == "read_only" && execCtx.frozenReadOnlySession == nil {
		return nil, fmt.Errorf("read-only AI gate action requires a frozen session")
	}
	if history == "read_only" && execCtx.frozenReadOnlySession != nil {
		if execCtx.frozenReadOnlySession.AgentID != agentID {
			return nil, fmt.Errorf(
				"%w: read-only agent does not match captured session",
				ErrPrivateWorkflowContext,
			)
		}
		frozenSession = cloneFrozenReadOnlySession(execCtx.frozenReadOnlySession)
		sessionKey = ""
	}
	inputs := map[string]any{
		"gate-ref":    resolved.GateRef,
		"gate-prompt": resolved.Gate.Prompt,
		"gate-fields": cloneGateDefinition(resolved.Gate).Fields,
	}
	if execCtx.privateValues != nil {
		inputs["gate-inputs"] = cloneMap(execCtx.privateValues)
	} else {
		inputs["workflow-inputs"] = cloneMap(execCtx.Inputs)
		inputs["event"] = cloneMap(execCtx.Event)
	}
	prompt := strings.TrimSpace(resolved.Action.Prompt) + "\n\nGate prompt:\n" + resolved.Gate.Prompt
	isolatedSystemPrompt := ""
	if private && history == "none" && sessionMode == AgentSessionEphemeral && tools == AgentToolsNone {
		isolatedSystemPrompt = strings.TrimSpace(resolved.Action.Prompt)
	}
	outputs, err := e.Agents.RunAgent(ctx, AgentRequest{
		AgentID:               agentID,
		Prompt:                prompt,
		Session:               sessionKey,
		EphemeralSession:      sessionMode == AgentSessionEphemeral,
		History:               history,
		Cache:                 cache,
		Tools:                 tools,
		Delivery:              execCtx.Delivery,
		Inputs:                inputs,
		Output:                &AgentOutputContract{Format: "json", Schema: gateFieldValuesSchema(resolved.Gate.Fields), RepairAttempts: 1},
		PrivateContext:        private,
		IsolatedSystemPrompt:  isolatedSystemPrompt,
		FrozenReadOnlySession: frozenSession,
	})
	if err != nil {
		return nil, fmt.Errorf("AI gate action: %w", err)
	}
	structured, ok := outputs["structured"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("AI gate action did not return structured field-values")
	}
	rawFields, exists := structured["field-values"]
	if !exists {
		return nil, fmt.Errorf("AI gate action did not return field-values")
	}
	fieldValues, err := validateGateFieldValues(resolved.Gate.Fields, rawFields)
	if err != nil {
		return nil, fmt.Errorf("AI gate action: %w", err)
	}
	return fieldValues, nil
}

func (e *Executor) executeGateWorkflowAction(
	ctx context.Context,
	run *Run,
	resolved resolvedGateAction,
	execCtx ExecutionContext,
	callDepth int,
) (map[string]any, string, error) {
	child := resolved.ActionWorkflow
	if child == nil || resolved.ActionWorkflowRevision == "" {
		return nil, "", fmt.Errorf("gate action workflow snapshot is unavailable")
	}
	if child.On.WorkflowCall == nil || child.On.WorkflowCall.Outputs["field-values"].Value == "" {
		return nil, "", fmt.Errorf("gate action workflow must declare workflow-call output field-values")
	}
	childExecution, err := newWorkflowExecutionState(child)
	if err != nil {
		return nil, "", err
	}
	if childExecution.WorkflowRevision != resolved.ActionWorkflowRevision {
		return nil, "", fmt.Errorf("gate action workflow snapshot changed after resolution")
	}
	childRunID := gateActionWorkflowChildRunID(
		run.ID,
		resolved.ExecutionID,
		resolved.ActionRevision,
		resolved.InputHash,
		childExecution.WorkflowRevision,
	)
	gateFields, err := detachedGateFields(resolved.Gate.Fields)
	if err != nil {
		return nil, "", err
	}
	inputs := map[string]any{
		"gate-ref":                     resolved.GateRef,
		"gate-prompt":                  resolved.Gate.Prompt,
		"gate-fields":                  gateFields,
		"workflow-inputs":              cloneMap(execCtx.Inputs),
		"event":                        cloneMap(execCtx.Event),
		"gate-action-depth":            callDepth + 1,
		"gate-parent-run-id":           run.ID,
		"gate-parent-execution-id":     resolved.ExecutionID,
		"gate-parent-action-revision":  resolved.ActionRevision,
		"gate-parent-input-hash":       resolved.InputHash,
		"gate-child-workflow-revision": childExecution.WorkflowRevision,
	}
	childExecutor := *e
	// Nested gates use their own workflow defaults. A repository override for
	// the outer gate must never accidentally match or control an inner gate.
	childExecutor.GateActions = nil
	childExecutor.MaxConcurrentRuns = 0
	if childExecutor.Store == nil {
		childExecutor.Store = NewFileRunStore(childExecutor.WorkspaceDir)
	}
	request := RunRequest{
		RunID:       childRunID,
		Workflow:    child,
		WorkflowRef: resolved.Action.WorkflowRef,
		Inputs:      inputs,
		Session:     execCtx.Session,
		Delivery:    execCtx.Delivery,
		CallDepth:   callDepth + 1,
	}
	if execCtx.privateValues != nil || execCtx.frozenReadOnlySession != nil {
		privateValues := cloneMap(execCtx.privateValues)
		privateValues["gate-ref"] = resolved.GateRef
		privateValues["gate-prompt"] = resolved.Gate.Prompt
		privateValues["gate-fields"] = cloneJSONValue(gateFields)
		privateValues["gate-parent-run-id"] = run.ID
		privateValues["gate-parent-execution-id"] = resolved.ExecutionID
		privateValues["gate-parent-action-revision"] = resolved.ActionRevision
		privateValues["gate-parent-input-hash"] = resolved.InputHash
		privateValues["gate-child-workflow-revision"] = childExecution.WorkflowRevision
		depth := 1
		if existing, ok := privateValues["gate-action-depth"].(json.Number); ok {
			if parsed, parseErr := existing.Int64(); parseErr == nil {
				depth = int(parsed) + 1
			}
		} else if existing, ok := privateValues["gate-action-depth"].(float64); ok {
			depth = int(existing) + 1
		}
		if depth > defaultMaxCallDepth {
			return nil, "", fmt.Errorf("workflow call depth exceeded")
		}
		privateValues["gate-action-depth"] = depth
		compiled, compileErr := compileGateActionWorkflowV3(child, privateValues)
		if compileErr != nil {
			return nil, "", compileErr
		}
		if execCtx.frozenReadOnlySession != nil {
			compiled.PrivateRoot.ReadOnlySession = &ReadOnlySessionRef{
				AgentID:          execCtx.frozenReadOnlySession.AgentID,
				Session:          execCtx.frozenReadOnlySession.Snapshot.Key,
				ExpectedRevision: execCtx.frozenReadOnlySession.Snapshot.Revision,
			}
			// The exact frozen session is already captured by the parent. The
			// ordinary capturer cannot reconstruct it from an opaque snapshot
			// key, so nested private-session action workflows are rejected until
			// a dedicated inherited-frozen-root request is introduced.
			return nil, "", fmt.Errorf(
				"%w: nested action workflow cannot inherit a frozen read-only session",
				ErrPrivateWorkflowContext,
			)
		}
		request = RunRequest{
			RunID:       childRunID,
			Workflow:    compiled.Workflow,
			WorkflowRef: resolved.Action.WorkflowRef,
			PrivateRoot: compiled.PrivateRoot,
		}
	}
	if existing, exists, existingErr := gateActionWorkflowExistingChild(
		ctx,
		childExecutor.Store,
		childRunID,
		resolved.Action.WorkflowRef,
		childExecution.WorkflowRevision,
		run.ID,
		resolved,
		execCtx.privateValues != nil || execCtx.frozenReadOnlySession != nil,
	); existingErr != nil {
		return nil, childRunID, existingErr
	} else if exists {
		return gateActionWorkflowExistingResult(existing, resolved)
	}
	result, err := childExecutor.Run(ctx, request)
	if result == nil {
		if existing, exists, existingErr := gateActionWorkflowExistingChild(
			ctx,
			childExecutor.Store,
			childRunID,
			resolved.Action.WorkflowRef,
			childExecution.WorkflowRevision,
			run.ID,
			resolved,
			execCtx.privateValues != nil || execCtx.frozenReadOnlySession != nil,
		); existingErr == nil && exists {
			return gateActionWorkflowExistingResult(existing, resolved)
		}
		return nil, "", err
	}
	if err != nil {
		return nil, result.RunID, err
	}
	if result.Status == RunStatusWaiting {
		return nil, result.RunID, gateActionWorkflowWaitingError{
			ChildRunID: result.RunID,
			Resolved:   resolved,
		}
	}
	childRun, readErr := childExecutor.Store.GetRun(ctx, result.RunID)
	if readErr != nil {
		return nil, result.RunID, readErr
	}
	rawFields, exists := childRun.Outputs["field-values"]
	if !exists {
		return nil, result.RunID, fmt.Errorf("gate action workflow must output field-values")
	}
	fieldValues, validationErr := validateGateFieldValues(resolved.Gate.Fields, rawFields)
	if validationErr != nil {
		return nil, result.RunID, fmt.Errorf("gate action workflow: %w", validationErr)
	}
	return fieldValues, result.RunID, nil
}

func gateActionWorkflowChildRunID(
	parentRunID string,
	executionID string,
	actionRevision string,
	inputHash string,
	workflowRevision string,
) string {
	digest := sha256.Sum256([]byte(
		"gate-action-child-v1\x00" + parentRunID + "\x00" + executionID + "\x00" +
			actionRevision + "\x00" + inputHash + "\x00" + workflowRevision,
	))
	return "wga_" + hex.EncodeToString(digest[:16])
}

func gateActionWorkflowExistingChild(
	ctx context.Context,
	store RunStore,
	childRunID string,
	workflowRef string,
	workflowRevision string,
	parentRunID string,
	resolved resolvedGateAction,
	private bool,
) (*Run, bool, error) {
	if store == nil {
		return nil, false, fmt.Errorf("workflow run store is required")
	}
	child, err := store.GetRun(ctx, childRunID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if child == nil || child.ID != childRunID || child.WorkflowRef != workflowRef ||
		IsPrivateWorkflowRun(child) != private {
		return nil, false, fmt.Errorf("gate action child binding conflict")
	}
	values := child.Inputs
	if private {
		if child.privateRoot == nil {
			return nil, false, fmt.Errorf("gate action child binding conflict")
		}
		values = child.privateRoot.Values
	}
	bindings := map[string]string{
		"gate-parent-run-id":           parentRunID,
		"gate-parent-execution-id":     resolved.ExecutionID,
		"gate-parent-action-revision":  resolved.ActionRevision,
		"gate-parent-input-hash":       resolved.InputHash,
		"gate-child-workflow-revision": workflowRevision,
	}
	for key, expected := range bindings {
		actual, ok := values[key].(string)
		if !ok || actual != expected {
			return nil, false, fmt.Errorf("gate action child binding conflict")
		}
	}
	if child.execution != nil && child.execution.WorkflowRevision != workflowRevision {
		return nil, false, fmt.Errorf("gate action child workflow revision conflict")
	}
	return child, true, nil
}

func gateActionWorkflowExistingResult(
	child *Run,
	resolved resolvedGateAction,
) (map[string]any, string, error) {
	if child == nil {
		return nil, "", fmt.Errorf("gate action child is unavailable")
	}
	switch child.Status {
	case RunStatusWaiting:
		return nil, child.ID, gateActionWorkflowWaitingError{
			ChildRunID: child.ID,
			Resolved:   resolved,
		}
	case RunStatusSucceeded:
		rawFields, exists := child.Outputs["field-values"]
		if !exists {
			return nil, child.ID, fmt.Errorf("gate action workflow must output field-values")
		}
		fieldValues, err := validateGateFieldValues(resolved.Gate.Fields, rawFields)
		if err != nil {
			return nil, child.ID, fmt.Errorf("gate action workflow: %w", err)
		}
		return fieldValues, child.ID, nil
	case RunStatusRunning:
		return nil, child.ID, fmt.Errorf("gate action child recovery is already in progress")
	default:
		return nil, child.ID, fmt.Errorf("gate action workflow ended in state %s", child.Status)
	}
}

func detachedGateFields(fields []GateField) (any, error) {
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode gate fields: %w", err)
	}
	var detached any
	if err := decodeJSONWithNumbers(encoded, &detached); err != nil {
		return nil, fmt.Errorf("decode gate fields: %w", err)
	}
	return detached, nil
}

func gateExecutionOutputs(
	resolved resolvedGateAction,
	actorKind string,
	fieldValues map[string]any,
) map[string]any {
	return map[string]any{
		"field-values":    cloneMap(fieldValues),
		"actor-kind":      actorKind,
		"execution-id":    resolved.ExecutionID,
		"action-revision": resolved.ActionRevision,
		"input-hash":      resolved.InputHash,
	}
}

func (e *Executor) resumeGateActionWorkflowProxy(
	ctx context.Context,
	store RunStore,
	run *Run,
	task WorkflowHumanTask,
	response HumanTaskResumeRequest,
) (bool, error) {
	if run == nil || task.GateWorkflow == nil {
		return false, ErrHumanTaskConflict
	}
	continuation := task.GateWorkflow
	childExecutor := *e
	childExecutor.Store = store
	childExecutor.GateActions = nil
	childExecutor.MaxConcurrentRuns = 0
	childResult, err := childExecutor.ResumeHumanTask(
		ctx,
		continuation.ChildRunID,
		continuation.ChildTaskID,
		response,
	)
	if err != nil {
		return false, fmt.Errorf("resume gate action workflow: %w", err)
	}
	if childResult == nil {
		return false, ErrHumanTaskConflict
	}
	resolved := resolvedGateAction{
		GateRef:        continuation.GateRef,
		Gate:           cloneGateDefinition(continuation.Gate),
		ActionRevision: continuation.ActionRevision,
		ExecutionID:    continuation.ExecutionID,
		InputHash:      continuation.InputHash,
	}
	if childResult.Status == RunStatusWaiting {
		proxy, proxyErr := e.newGateActionWorkflowProxyTask(
			ctx,
			run,
			task.JobID,
			task.StepID,
			gateActionWorkflowWaitingError{
				ChildRunID: continuation.ChildRunID,
				Resolved:   resolved,
			},
		)
		if proxyErr != nil {
			return false, proxyErr
		}
		if run.humanTasks == nil {
			run.humanTasks = make(map[string]WorkflowHumanTask)
		}
		run.humanTasks[proxy.ID] = proxy
		stepKey := task.JobID + "/" + task.StepID
		step := run.Steps[stepKey]
		step.Status = RunStatusWaiting
		step.Error = ""
		step.Outputs = map[string]any{
			"execution-id": continuation.ExecutionID,
			"input-hash":   continuation.InputHash,
			"child-run-id": continuation.ChildRunID,
		}
		run.Steps[stepKey] = step
		job := run.Jobs[task.JobID]
		job.Status = RunStatusWaiting
		job.Error = ""
		run.Jobs[task.JobID] = job
		stepIndex, indexErr := workflowStepIndex(run.execution.Workflow, task.JobID, task.StepID)
		if indexErr != nil {
			return false, indexErr
		}
		run.execution.Cursor = &WorkflowExecutionCursor{JobID: task.JobID, StepIndex: stepIndex}
		return true, nil
	}
	if childResult.Status != RunStatusSucceeded {
		return false, fmt.Errorf("gate action workflow ended in state %s", childResult.Status)
	}
	childRun, err := store.GetRun(ctx, continuation.ChildRunID)
	if err != nil {
		return false, err
	}
	rawFields, exists := childRun.Outputs["field-values"]
	if !exists {
		return false, fmt.Errorf("gate action workflow must output field-values")
	}
	fieldValues, err := validateGateFieldValues(continuation.Gate.Fields, rawFields)
	if err != nil {
		return false, fmt.Errorf("gate action workflow: %w", err)
	}
	stepKey := task.JobID + "/" + task.StepID
	step := run.Steps[stepKey]
	step.Status = RunStatusSucceeded
	step.Error = ""
	step.Outputs = gateExecutionOutputs(resolved, GateActorWorkflow, fieldValues)
	run.Steps[stepKey] = step
	return false, nil
}

func workflowStepIndex(workflow *Workflow, jobID, stepID string) (int, error) {
	if workflow == nil {
		return 0, ErrHumanTaskConflict
	}
	job, exists := workflow.Jobs[jobID]
	if !exists {
		return 0, ErrHumanTaskConflict
	}
	for index, step := range job.Steps {
		candidate := strings.TrimSpace(step.ID)
		if candidate == "" {
			candidate = fmt.Sprintf("step_%d", index+1)
		}
		if candidate == stepID {
			return index, nil
		}
	}
	return 0, ErrHumanTaskConflict
}

func gateActionChildRunForTask(run *Run, taskID string) string {
	if run == nil {
		return ""
	}
	task, exists := run.humanTasks[taskID]
	if !exists || task.GateWorkflow == nil {
		return ""
	}
	return task.GateWorkflow.ChildRunID
}

func gateActionWaitingChildRun(run *Run) string {
	if run == nil {
		return ""
	}
	for _, task := range run.humanTasks {
		if task.Status == HumanTaskStatusWaiting && task.GateWorkflow != nil {
			return task.GateWorkflow.ChildRunID
		}
	}
	return ""
}

func (e *Executor) reconcilePrivateCompiledGateRun(
	ctx context.Context,
	store RunStore,
	requested *Run,
) (*RunResult, error) {
	conflict := func() (*RunResult, error) {
		return sanitizePrivateRunOutcome(requested, nil, ErrRunAdmissionConflict)
	}
	if requested == nil || requested.privateRoot == nil || requested.execution == nil ||
		requested.execution.Workflow == nil || store == nil {
		return conflict()
	}
	existing, err := store.GetRun(ctx, requested.ID)
	if err != nil || existing == nil || existing.privateRoot == nil ||
		validateRunPrivateContext(existing) != nil {
		return conflict()
	}
	if existing.ID != requested.ID || existing.WorkflowRef != requested.WorkflowRef ||
		existing.ContextVisibility != WorkflowContextVisibilityPrivate ||
		existing.ParentRunID != requested.ParentRunID || existing.CallerJobID != requested.CallerJobID ||
		existing.RetryOfRunID != requested.RetryOfRunID ||
		existing.execution == nil || existing.execution.Workflow == nil ||
		existing.execution.WorkflowRevision != requested.execution.WorkflowRevision ||
		canonicalJSON(existing.execution.Workflow) != canonicalJSON(requested.execution.Workflow) ||
		canonicalJSON(existing.privateRoot) != canonicalJSON(requested.privateRoot) {
		return conflict()
	}
	jobID, stepID, gateRef, ok := privateCompiledGateExecIdentity(requested.execution.Workflow)
	if !ok {
		return conflict()
	}
	resolved, err := e.resolveGateAction(ctx, requested, jobID, stepID, gateRef)
	if err != nil {
		return conflict()
	}
	actionRevision, executionID, inputHash, ok := persistedPrivateGateActionIdentity(
		existing,
		jobID,
		stepID,
	)
	if !ok || actionRevision != resolved.ActionRevision ||
		executionID != resolved.ExecutionID || inputHash != resolved.InputHash {
		return conflict()
	}
	result := &RunResult{
		RunID:   existing.ID,
		Status:  existing.Status,
		Outputs: cloneMap(existing.Outputs),
		Error:   existing.Error,
	}
	switch existing.Status {
	case RunStatusWaiting, RunStatusSucceeded, RunStatusSkipped:
		return sanitizePrivateRunOutcome(existing, result, nil)
	case RunStatusFailed:
		failure := errors.New(strings.TrimSpace(existing.Error))
		if strings.TrimSpace(existing.Error) == "" {
			failure = ErrPrivateWorkflowFailed
		}
		return sanitizePrivateRunOutcome(existing, result, failure)
	case RunStatusCanceled:
		return sanitizePrivateRunOutcome(existing, result, ErrRunCanceled)
	default:
		return conflict()
	}
}

func privateCompiledGateExecIdentity(workflow *Workflow) (string, string, string, bool) {
	if workflow == nil {
		return "", "", "", false
	}
	var jobID, stepID, gateRef string
	for candidateJobID, job := range workflow.Jobs {
		for index, step := range job.Steps {
			if strings.TrimSpace(step.Uses) != GateExecUses {
				continue
			}
			if gateRef != "" {
				return "", "", "", false
			}
			candidateRef, ok := step.With["gate-ref"].(string)
			if !ok {
				return "", "", "", false
			}
			candidateStepID := strings.TrimSpace(step.ID)
			if candidateStepID == "" {
				candidateStepID = fmt.Sprintf("step_%d", index+1)
			}
			jobID, stepID, gateRef = candidateJobID, candidateStepID, candidateRef
		}
	}
	return jobID, stepID, gateRef, gateRef != ""
}

func persistedPrivateGateActionIdentity(
	run *Run,
	jobID string,
	stepID string,
) (string, string, string, bool) {
	if run == nil {
		return "", "", "", false
	}
	if step, exists := run.Steps[jobID+"/"+stepID]; exists {
		actionRevision, revisionOK := step.Outputs["action-revision"].(string)
		executionID, executionOK := step.Outputs["execution-id"].(string)
		inputHash, hashOK := step.Outputs["input-hash"].(string)
		if revisionOK && executionOK && hashOK && actionRevision != "" &&
			executionID != "" && inputHash != "" {
			return actionRevision, executionID, inputHash, true
		}
	}
	for _, task := range run.humanTasks {
		if task.JobID != jobID || task.StepID != stepID {
			continue
		}
		if task.GateWorkflow != nil {
			return task.GateWorkflow.ActionRevision,
				task.GateWorkflow.ExecutionID,
				task.GateWorkflow.InputHash,
				task.GateWorkflow.ActionRevision != "" &&
					task.GateWorkflow.ExecutionID != "" && task.GateWorkflow.InputHash != ""
		}
		return task.ActionRevision,
			task.ExecutionID,
			task.InputHash,
			task.ActionRevision != "" && task.ExecutionID != "" && task.InputHash != ""
	}
	return "", "", "", false
}

func normalizeGateSelections(field GateField, value any) ([]string, error) {
	var selected []string
	if field.MaxSelections == 1 {
		item, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("field %q must be one option ID", field.ID)
		}
		selected = []string{item}
	} else {
		switch items := value.(type) {
		case []any:
			selected = make([]string, 0, len(items))
			for _, item := range items {
				text, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("field %q selections must be option IDs", field.ID)
				}
				selected = append(selected, text)
			}
		case []string:
			selected = append([]string(nil), items...)
		default:
			return nil, fmt.Errorf("field %q must be an option ID array", field.ID)
		}
	}
	if len(selected) < field.MinSelections || len(selected) > field.MaxSelections {
		return nil, fmt.Errorf(
			"field %q must select between %d and %d options",
			field.ID, field.MinSelections, field.MaxSelections,
		)
	}
	allowed := make(map[string]bool, len(field.Options))
	for _, option := range field.Options {
		allowed[option.ID] = true
	}
	seen := make(map[string]bool, len(selected))
	for _, item := range selected {
		if !allowed[item] {
			return nil, fmt.Errorf("field %q contains unknown option %q", field.ID, item)
		}
		if seen[item] {
			return nil, fmt.Errorf("field %q contains duplicate option %q", field.ID, item)
		}
		seen[item] = true
	}
	return selected, nil
}
