package workflows

import (
	"encoding"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

// GateKind selects how one user-attention decision is evaluated. Gates compile
// to ordinary workflow steps; the executor has no gate-specific runtime path.
type GateKind = gatetypes.GateKind

const (
	GateAIWorkingContext  = gatetypes.GateAIWorkingContext
	GateAIIsolatedContext = gatetypes.GateAIIsolatedContext
	GateDeterministic     = gatetypes.GateDeterministic
	GateZero              = gatetypes.GateZero

	MaxWorkflowGateCount          = gatetypes.MaxWorkflowGateCount
	MaxWorkflowGateIDBytes        = gatetypes.MaxWorkflowGateIDBytes
	MaxWorkflowGateNameBytes      = gatetypes.MaxWorkflowGateNameBytes
	MaxWorkflowGateTitleBytes     = gatetypes.MaxWorkflowGateTitleBytes
	MaxWorkflowGateCriteriaBytes  = gatetypes.MaxWorkflowGateCriteriaBytes
	MaxWorkflowGateConditionBytes = gatetypes.MaxWorkflowGateConditionBytes
	MaxWorkflowGateQuestionBytes  = gatetypes.MaxWorkflowGateQuestionBytes
	MaxWorkflowGateSubjectBytes   = gatetypes.MaxWorkflowGateSubjectBytes
	MaxWorkflowGateInputsBytes    = gatetypes.MaxWorkflowGateInputsBytes
	MaxWorkflowGateJSONDepth      = gatetypes.MaxWorkflowGateJSONDepth
	MaxWorkflowGateJSONNodes      = gatetypes.MaxWorkflowGateJSONNodes
)

const (
	workflowGateJobID        = "gates"
	workflowGateSpecsInput   = "_gates"
	workflowGateSubjectInput = "gate_subject"
	workflowGatePrompt       = `Evaluate whether this user-attention gate requires the user's input before work continues.

Apply the configured criteria to the assigned subject and to any conversation context available to this invocation. Set ask_user to true only when a user choice, clarification, or intervention is needed. When ask_user is true, provide a concise reason and specific questions that let the user unblock the work. When it is false, return an empty questions list.`
)

var workflowGateIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

var (
	workflowGateJSONMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	workflowGateTextMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	workflowGateJSONNumberType    = reflect.TypeOf(json.Number(""))
)

// GateSpec describes one gate in an ordered composition.
type GateSpec = gatetypes.GateSpec

// GateCompilation is a runnable inline workflow plus its private frozen root.
// All-zero and empty compositions are represented as Noop and must not be
// submitted to Executor.Run.
type GateCompilation struct {
	Workflow *Workflow `json:"workflow,omitempty"`
	// Inputs remains source-compatible for callers compiled against the initial
	// gate API, but is intentionally always empty. Compiler-owned policy and
	// subject data live only in PrivateRoot.
	Inputs                 map[string]any      `json:"-"`
	PrivateRoot            *PrivateRootRequest `json:"-"`
	Noop                   bool                `json:"noop"`
	GateIDs                []string            `json:"gate_ids"`
	RequiresSession        bool                `json:"requires_session"`
	RequiredSessionAgentID string              `json:"required_session_agent_id,omitempty"`
}

// CompileGateWorkflow lowers an ordered gate composition into one manual,
// inline root workflow. Every step stays in the root because a durable
// human/task cannot cross reusable-workflow or external-event edges.
func CompileGateWorkflow(name string, specs []GateSpec, subject any) (*GateCompilation, error) {
	rawName := name
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(rawName) || len(rawName) > MaxWorkflowGateNameBytes {
		return nil, fmt.Errorf(
			"gate workflow name must be nonblank valid UTF-8 and at most %d bytes",
			MaxWorkflowGateNameBytes,
		)
	}
	if len(specs) > MaxWorkflowGateCount {
		return nil, fmt.Errorf("gate composition exceeds %d gates", MaxWorkflowGateCount)
	}

	compilation := &GateCompilation{
		Inputs:  map[string]any{},
		GateIDs: make([]string, 0, len(specs)),
	}
	seen := make(map[string]struct{}, len(specs))
	active := make([]GateSpec, 0, len(specs))
	workingAgentID := ""
	for index, spec := range specs {
		if err := validateWorkflowGateSpec(fmt.Sprintf("gates[%d]", index), spec); err != nil {
			return nil, err
		}
		if _, exists := seen[spec.ID]; exists {
			return nil, fmt.Errorf("gate %q is duplicated", spec.ID)
		}
		seen[spec.ID] = struct{}{}
		compilation.GateIDs = append(compilation.GateIDs, spec.ID)
		if spec.Kind != GateZero {
			active = append(active, spec)
		}
		if spec.Kind == GateAIWorkingContext {
			if workingAgentID != "" && workingAgentID != spec.AgentID {
				return nil, fmt.Errorf(
					"working-context gates must use one session-owning agent; got %q and %q",
					workingAgentID,
					spec.AgentID,
				)
			}
			workingAgentID = spec.AgentID
			compilation.RequiresSession = true
			compilation.RequiredSessionAgentID = spec.AgentID
		}
	}
	if len(active) == 0 {
		compilation.Noop = true
		return compilation, nil
	}
	normalizedSubject, err := normalizeWorkflowGateValue(
		"gate subject",
		subject,
		MaxWorkflowGateSubjectBytes,
	)
	if err != nil {
		return nil, err
	}
	gateInputs := make(map[string]any, len(active))
	steps := make([]Step, 0, len(active)*2)
	for _, spec := range active {
		input, inputErr := workflowGateInput(spec)
		if inputErr != nil {
			return nil, inputErr
		}
		gateInputs[spec.ID] = input
		switch spec.Kind {
		case GateAIWorkingContext:
			steps = append(steps, workflowAIGateSteps(spec, false)...)
		case GateAIIsolatedContext:
			steps = append(steps, workflowAIGateSteps(spec, true)...)
		case GateDeterministic:
			condition, conditionErr := normalizeWorkflowGateCondition(spec.When)
			if conditionErr != nil {
				return nil, fmt.Errorf("gate %q when: %w", spec.ID, conditionErr)
			}
			steps = append(steps, workflowDeterministicGateStep(spec, condition))
		}
	}

	privateValues := map[string]any{
		workflowGateSpecsInput:   gateInputs,
		workflowGateSubjectInput: normalizedSubject,
	}
	for _, spec := range active {
		if spec.Kind != GateDeterministic {
			continue
		}
		condition, conditionErr := normalizeWorkflowGateCondition(spec.When)
		if conditionErr != nil {
			return nil, fmt.Errorf("gate %q when: %w", spec.ID, conditionErr)
		}
		if pathErr := validateWorkflowGateConditionPaths(condition, privateValues); pathErr != nil {
			return nil, fmt.Errorf("gate %q when: %w", spec.ID, pathErr)
		}
	}
	privateValuesBytes, encodeErr := json.Marshal(privateValues)
	if encodeErr != nil {
		return nil, fmt.Errorf("encode gate inputs: %w", encodeErr)
	}
	if len(privateValuesBytes) > MaxWorkflowGateInputsBytes {
		return nil, fmt.Errorf("gate inputs exceed %d bytes", MaxWorkflowGateInputsBytes)
	}

	compilation.PrivateRoot = &PrivateRootRequest{
		Values:                privateValues,
		privateValuesRevision: workflowHashBytes(privateValuesBytes),
	}
	compilation.Workflow = &Workflow{
		Name: name,
		On:   WorkflowTriggers{Manual: map[string]any{}},
		Jobs: map[string]Job{
			workflowGateJobID: {
				Name:   "Evaluate user-attention gates",
				RunsOn: "picoclaw",
				Steps:  steps,
			},
		},
	}
	if validateErr := Validate(compilation.Workflow); validateErr != nil {
		return nil, fmt.Errorf("compiled gate workflow is invalid: %w", validateErr)
	}
	workflowBytes, encodeErr := json.Marshal(compilation.Workflow)
	if encodeErr != nil {
		return nil, fmt.Errorf("encode compiled gate workflow: %w", encodeErr)
	}
	compilation.Workflow.privateRootRevision = workflowHashBytes(workflowBytes)
	return compilation, nil
}

func validateWorkflowGateSpec(path string, spec GateSpec) error {
	if spec.ID != strings.TrimSpace(spec.ID) || !workflowGateIDPattern.MatchString(spec.ID) ||
		len(spec.ID) > MaxWorkflowGateIDBytes {
		return fmt.Errorf(
			"%s.id must match %s and be at most %d bytes",
			path,
			workflowGateIDPattern.String(),
			MaxWorkflowGateIDBytes,
		)
	}
	switch spec.Kind {
	case GateAIWorkingContext, GateAIIsolatedContext:
		if spec.AgentID != strings.TrimSpace(spec.AgentID) || !routing.IsCanonicalAgentID(spec.AgentID) {
			return fmt.Errorf("%s.agent_id must be an exact canonical agent ID", path)
		}
		if err := validateWorkflowGateText(
			path+".criteria",
			spec.Criteria,
			MaxWorkflowGateCriteriaBytes,
		); err != nil {
			return err
		}
		if err := validateWorkflowGateTitle(path+".title", spec.Title); err != nil {
			return err
		}
		if spec.When != "" {
			return fmt.Errorf("%s.when is only supported for deterministic gates", path)
		}
	case GateDeterministic:
		if spec.AgentID != "" || spec.Criteria != "" {
			return fmt.Errorf("%s deterministic gate cannot configure agent_id or criteria", path)
		}
		if err := validateWorkflowGateTitle(path+".title", spec.Title); err != nil {
			return err
		}
		if !utf8.ValidString(spec.When) || len(spec.When) > MaxWorkflowGateConditionBytes {
			return fmt.Errorf(
				"%s.when must be valid UTF-8 and at most %d bytes",
				path,
				MaxWorkflowGateConditionBytes,
			)
		}
		if _, err := normalizeWorkflowGateCondition(spec.When); err != nil {
			return fmt.Errorf("%s.when: %w", path, err)
		}
		if spec.Questions == nil {
			return fmt.Errorf("%s.questions are required", path)
		}
	case GateZero:
		if spec.AgentID != "" || spec.Criteria != "" || spec.When != "" || spec.Title != "" ||
			spec.Questions != nil {
			return fmt.Errorf("%s zero gate cannot configure behavior fields", path)
		}
	default:
		return fmt.Errorf("%s.kind %q is unsupported", path, spec.Kind)
	}
	return nil
}

func validateWorkflowGateTitle(path, value string) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || len(value) > MaxWorkflowGateTitleBytes {
		return fmt.Errorf(
			"%s must be nonblank valid UTF-8 and at most %d bytes",
			path,
			MaxWorkflowGateTitleBytes,
		)
	}
	return nil
}

func validateWorkflowGateText(path, value string, maxBytes int) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || len(value) > maxBytes {
		return fmt.Errorf("%s must be nonblank valid UTF-8 and at most %d bytes", path, maxBytes)
	}
	return nil
}

func workflowGateInput(spec GateSpec) (map[string]any, error) {
	input := map[string]any{
		"title": strings.TrimSpace(spec.Title),
	}
	if strings.TrimSpace(spec.Criteria) != "" {
		input["criteria"] = strings.TrimSpace(spec.Criteria)
	}
	if spec.Questions != nil {
		questions, err := normalizeWorkflowGateValue(
			fmt.Sprintf("gate %q questions", spec.ID),
			spec.Questions,
			MaxWorkflowGateQuestionBytes,
		)
		if err != nil {
			return nil, err
		}
		if spec.Kind == GateDeterministic && questions == nil {
			return nil, fmt.Errorf("gate %q questions are required", spec.ID)
		}
		input["questions"] = questions
	}
	return input, nil
}

func normalizeWorkflowGateValue(label string, value any, maxBytes int) (any, error) {
	if err := preflightWorkflowGateJSON(label, value, maxBytes); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%s must be finite acyclic JSON: %w", label, err)
	}
	if len(encoded) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	var normalized any
	if err := decodeJSONWithNumbers(encoded, &normalized); err != nil {
		return nil, fmt.Errorf("%s must be valid JSON: %w", label, err)
	}
	return normalized, nil
}

type workflowGateJSONVisit struct {
	kind    reflect.Kind
	pointer uintptr
}

type workflowGateJSONBudget struct {
	label    string
	maxBytes int
	bytes    int
	nodes    int
	active   map[workflowGateJSONVisit]struct{}
}

func preflightWorkflowGateJSON(label string, value any, maxBytes int) error {
	budget := &workflowGateJSONBudget{
		label:    label,
		maxBytes: maxBytes,
		active:   make(map[workflowGateJSONVisit]struct{}),
	}
	if err := budget.visit(reflect.ValueOf(value), 0); err != nil {
		return err
	}
	return nil
}

func (budget *workflowGateJSONBudget) visit(value reflect.Value, depth int) error {
	if depth > MaxWorkflowGateJSONDepth {
		return fmt.Errorf("%s exceeds JSON depth %d", budget.label, MaxWorkflowGateJSONDepth)
	}
	budget.nodes++
	if budget.nodes > MaxWorkflowGateJSONNodes {
		return fmt.Errorf("%s exceeds %d JSON nodes", budget.label, MaxWorkflowGateJSONNodes)
	}
	if !value.IsValid() {
		return budget.addBytes(4)
	}
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return budget.addBytes(4)
		}
		value = value.Elem()
	}
	if workflowGateHasCustomMarshaler(value.Type()) {
		return fmt.Errorf(
			"%s contains custom marshaler type %s; use JSON maps, arrays, and scalars",
			budget.label,
			value.Type(),
		)
	}

	switch value.Kind() {
	case reflect.Bool:
		if value.Bool() {
			return budget.addBytes(len("true"))
		}
		return budget.addBytes(len("false"))
	case reflect.String:
		text := value.String()
		if !utf8.ValidString(text) {
			return fmt.Errorf("%s contains invalid UTF-8", budget.label)
		}
		if value.Type() == workflowGateJSONNumberType {
			if err := budget.checkBytes(len(text)); err != nil {
				return err
			}
			encoded, err := json.Marshal(json.Number(text))
			if err != nil {
				return fmt.Errorf("%s contains an invalid JSON number", budget.label)
			}
			return budget.addBytes(len(encoded))
		}
		return budget.addJSONString(text)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return budget.addBytes(len(strconv.FormatInt(value.Int(), 10)))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return budget.addBytes(len(strconv.FormatUint(value.Uint(), 10)))
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return fmt.Errorf("%s contains a non-finite number", budget.label)
		}
		var encoded []byte
		var err error
		if value.Kind() == reflect.Float32 {
			encoded, err = json.Marshal(float32(number))
		} else {
			encoded, err = json.Marshal(number)
		}
		if err != nil {
			return fmt.Errorf("%s contains an invalid JSON number", budget.label)
		}
		return budget.addBytes(len(encoded))
	case reflect.Map:
		if value.IsNil() {
			return budget.addBytes(4)
		}
		if value.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("%s contains a map with non-string keys", budget.label)
		}
		if workflowGateHasCustomMarshaler(value.Type().Key()) {
			return fmt.Errorf(
				"%s contains custom marshaler map-key type %s",
				budget.label,
				value.Type().Key(),
			)
		}
		leave, err := budget.enter(value)
		if err != nil {
			return err
		}
		defer leave()
		if err := budget.addBytes(2); err != nil {
			return err
		}
		iterator := value.MapRange()
		first := true
		for iterator.Next() {
			if !first {
				if err := budget.addBytes(1); err != nil {
					return err
				}
			}
			first = false
			key := iterator.Key().String()
			if !utf8.ValidString(key) {
				return fmt.Errorf("%s contains an invalid UTF-8 map key", budget.label)
			}
			if err := budget.addJSONString(key); err != nil {
				return err
			}
			if err := budget.addBytes(1); err != nil {
				return err
			}
			if err := budget.visit(iterator.Value(), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice:
		if value.IsNil() {
			return budget.addBytes(4)
		}
		if value.Type().Elem().Kind() == reflect.Uint8 &&
			!workflowGateHasCustomMarshaler(value.Type().Elem()) {
			if err := budget.checkBytes(value.Len() + 2); err != nil {
				return err
			}
			encoded, err := json.Marshal(value.Bytes())
			if err != nil {
				return fmt.Errorf("%s contains invalid bytes", budget.label)
			}
			return budget.addBytes(len(encoded))
		}
		leave, err := budget.enter(value)
		if err != nil {
			return err
		}
		defer leave()
		if err := budget.addBytes(2); err != nil {
			return err
		}
		for index := 0; index < value.Len(); index++ {
			if index != 0 {
				if err := budget.addBytes(1); err != nil {
					return err
				}
			}
			if err := budget.visit(value.Index(index), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Array:
		if err := budget.addBytes(2); err != nil {
			return err
		}
		for index := 0; index < value.Len(); index++ {
			if index != 0 {
				if err := budget.addBytes(1); err != nil {
					return err
				}
			}
			if err := budget.visit(value.Index(index), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Pointer:
		if value.IsNil() {
			return budget.addBytes(4)
		}
		return fmt.Errorf("%s contains a pointer; use JSON maps, arrays, and scalars", budget.label)
	default:
		return fmt.Errorf("%s contains unsupported JSON type %s", budget.label, value.Type())
	}
}

func workflowGateHasCustomMarshaler(valueType reflect.Type) bool {
	if valueType.Implements(workflowGateJSONMarshalerType) ||
		valueType.Implements(workflowGateTextMarshalerType) {
		return true
	}
	return valueType.Kind() != reflect.Pointer &&
		(reflect.PointerTo(valueType).Implements(workflowGateJSONMarshalerType) ||
			reflect.PointerTo(valueType).Implements(workflowGateTextMarshalerType))
}

func (budget *workflowGateJSONBudget) enter(value reflect.Value) (func(), error) {
	visit := workflowGateJSONVisit{kind: value.Kind(), pointer: value.Pointer()}
	if _, exists := budget.active[visit]; exists {
		return nil, fmt.Errorf("%s must be acyclic JSON", budget.label)
	}
	budget.active[visit] = struct{}{}
	return func() { delete(budget.active, visit) }, nil
}

func (budget *workflowGateJSONBudget) addBytes(count int) error {
	if err := budget.checkBytes(count); err != nil {
		return err
	}
	budget.bytes += count
	return nil
}

func (budget *workflowGateJSONBudget) checkBytes(count int) error {
	if count < 0 || budget.bytes > budget.maxBytes-count {
		return fmt.Errorf("%s exceeds %d encoded JSON bytes", budget.label, budget.maxBytes)
	}
	return nil
}

func (budget *workflowGateJSONBudget) addJSONString(value string) error {
	if err := budget.checkBytes(len(value) + 2); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%s contains an invalid JSON string", budget.label)
	}
	return budget.addBytes(len(encoded))
}

func normalizeWorkflowGateCondition(value string) (string, error) {
	condition := strings.TrimSpace(value)
	if strings.HasPrefix(condition, "${{") || strings.HasSuffix(condition, "}}") {
		if !strings.HasPrefix(condition, "${{") || !strings.HasSuffix(condition, "}}") {
			return "", fmt.Errorf("expression delimiters are incomplete")
		}
		condition = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(condition, "${{"), "}}"))
	}
	if err := validateExpressionSyntax(condition); err != nil {
		return "", err
	}
	if err := validateWorkflowGateConditionOperands(condition); err != nil {
		return "", err
	}
	return condition, nil
}

func validateWorkflowGateConditionOperands(expression string) error {
	expression = strings.TrimSpace(expression)
	if terms, ok := splitExpressionLogicalAND(expression); ok {
		for _, term := range terms {
			if err := validateWorkflowGateConditionOperands(term); err != nil {
				return err
			}
		}
		return nil
	}
	for _, operator := range []string{" == ", " != ", " >= ", " <= ", " > ", " < "} {
		if index := strings.Index(expression, operator); index >= 0 {
			if err := validateWorkflowGateConditionOperands(expression[:index]); err != nil {
				return err
			}
			return validateWorkflowGateConditionOperands(expression[index+len(operator):])
		}
	}
	if strings.HasPrefix(expression, "not ") {
		return validateWorkflowGateConditionOperands(strings.TrimSpace(strings.TrimPrefix(expression, "not ")))
	}
	if isQuotedExpressionLiteral(expression) {
		return nil
	}
	switch expression {
	case "true", "false", "null":
		return nil
	}
	if _, err := strconv.ParseFloat(expression, 64); err == nil {
		return nil
	}
	if !expressionPathPattern.MatchString(expression) {
		return fmt.Errorf("unsupported expression syntax %q", expression)
	}
	root := strings.SplitN(expression, ".", 2)[0]
	if root != "inputs" {
		return fmt.Errorf("deterministic gate expression root %q is unsupported; use inputs", root)
	}
	return nil
}

func validateWorkflowGateConditionPaths(condition string, inputs map[string]any) error {
	for _, path := range workflowGateConditionPaths(condition) {
		if !workflowGateInputPathExists(inputs, path) {
			return fmt.Errorf("deterministic gate input path %q does not exist", path)
		}
	}
	return nil
}

func workflowGateConditionPaths(expression string) []string {
	expression = strings.TrimSpace(expression)
	if terms, ok := splitExpressionLogicalAND(expression); ok {
		paths := make([]string, 0, len(terms))
		for _, term := range terms {
			paths = append(paths, workflowGateConditionPaths(term)...)
		}
		return paths
	}
	for _, operator := range []string{" == ", " != ", " >= ", " <= ", " > ", " < "} {
		if index := strings.Index(expression, operator); index >= 0 {
			left := workflowGateConditionPaths(expression[:index])
			return append(left, workflowGateConditionPaths(expression[index+len(operator):])...)
		}
	}
	if strings.HasPrefix(expression, "not ") {
		return workflowGateConditionPaths(strings.TrimSpace(strings.TrimPrefix(expression, "not ")))
	}
	if expressionPathPattern.MatchString(expression) &&
		strings.SplitN(expression, ".", 2)[0] == "inputs" {
		return []string{expression}
	}
	return nil
}

func workflowGateInputPathExists(inputs map[string]any, path string) bool {
	parts := strings.Split(path, ".")
	if len(parts) == 0 || parts[0] != "inputs" {
		return false
	}
	var current any = inputs
	for _, part := range parts[1:] {
		object, ok := current.(map[string]any)
		if !ok {
			return false
		}
		current, ok = object[part]
		if !ok {
			return false
		}
	}
	return true
}

func workflowAIGateSteps(spec GateSpec, isolated bool) []Step {
	decisionID := workflowGateDecisionStepID(spec.ID)
	attentionID := workflowGateAttentionStepID(spec.ID)
	sessionMode := "inherit"
	historyMode := "read_only"
	cacheMode := "session"
	if isolated {
		sessionMode = AgentSessionEphemeral
		historyMode = "none"
		cacheMode = "none"
	}
	scope := map[string]any{
		"gate_id":  spec.ID,
		"criteria": workflowGateInputExpression(spec.ID, "criteria"),
		"subject":  "${{ private." + workflowGateSubjectInput + " }}",
	}
	if spec.Questions != nil {
		scope["question_guidance"] = workflowGateInputExpression(spec.ID, "questions")
	}
	return []Step{
		{
			ID:      decisionID,
			Name:    "Evaluate gate " + spec.ID,
			Uses:    "agent/" + spec.AgentID,
			Context: RunContext{Delivery: "none"},
			With: map[string]any{
				"prompt":  workflowGatePrompt,
				"scope":   scope,
				"session": sessionMode,
				"history": historyMode,
				"cache":   cacheMode,
				"tools":   AgentToolsNone,
				"output":  workflowGateAgentOutputContract(),
			},
		},
		{
			ID:   attentionID,
			Name: "Request attention for gate " + spec.ID,
			Uses: "human/task",
			If:   "${{ steps." + decisionID + ".outputs.structured.ask_user == true }}",
			With: map[string]any{
				"title": workflowGateInputExpression(spec.ID, "title"),
				"questions": map[string]any{
					"gate_id":   spec.ID,
					"reason":    "${{ steps." + decisionID + ".outputs.structured.reason }}",
					"questions": "${{ steps." + decisionID + ".outputs.structured.questions }}",
				},
				"response_schema": workflowGateResponseSchema(),
			},
		},
	}
}

func workflowDeterministicGateStep(spec GateSpec, condition string) Step {
	return Step{
		ID:   workflowGateAttentionStepID(spec.ID),
		Name: "Request attention for gate " + spec.ID,
		Uses: "human/task",
		If:   "${{ " + lowerWorkflowGateCondition(condition) + " }}",
		With: map[string]any{
			"title":           workflowGateInputExpression(spec.ID, "title"),
			"questions":       workflowGateInputExpression(spec.ID, "questions"),
			"response_schema": workflowGateResponseSchema(),
		},
	}
}

func workflowGateAgentOutputContract() map[string]any {
	return map[string]any{
		"format":          "json",
		"repair_attempts": 1,
		"schema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"ask_user", "reason", "questions"},
			"properties": map[string]any{
				"ask_user": map[string]any{"type": "boolean"},
				"reason":   map[string]any{"type": "string"},
				"questions": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
			},
		},
	}
}

func workflowGateResponseSchema() map[string]any {
	return map[string]any{"type": "string"}
}

func workflowGateInputExpression(gateID, field string) string {
	return "${{ private." + workflowGateSpecsInput + "." + gateID + "." + field + " }}"
}

func lowerWorkflowGateCondition(expression string) string {
	expression = strings.TrimSpace(expression)
	if terms, ok := splitExpressionLogicalAND(expression); ok {
		lowered := make([]string, 0, len(terms))
		for _, term := range terms {
			lowered = append(lowered, lowerWorkflowGateCondition(term))
		}
		return strings.Join(lowered, " and ")
	}
	for _, operator := range []string{" == ", " != ", " >= ", " <= ", " > ", " < "} {
		if index := strings.Index(expression, operator); index >= 0 {
			return lowerWorkflowGateCondition(expression[:index]) + operator +
				lowerWorkflowGateCondition(expression[index+len(operator):])
		}
	}
	if strings.HasPrefix(expression, "not ") {
		return "not " + lowerWorkflowGateCondition(
			strings.TrimSpace(strings.TrimPrefix(expression, "not ")),
		)
	}
	if strings.HasPrefix(expression, "inputs.") {
		return "private." + strings.TrimPrefix(expression, "inputs.")
	}
	return expression
}

func workflowGateDecisionStepID(gateID string) string {
	return "gate_" + gateID + "_decision"
}

func workflowGateAttentionStepID(gateID string) string {
	return "gate_" + gateID + "_attention"
}
