package gatetypes

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
)

var (
	gateWorkflowV2IDPattern            = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	gateWorkflowV2DecisionPointPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)
	gateWorkflowV2AgentIDPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	gateWorkflowV2ExpressionPath       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+)*$`)

	gateWorkflowV2JSONMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	gateWorkflowV2TextMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	gateWorkflowV2JSONNumberType    = reflect.TypeOf(json.Number(""))
)

// ValidateGateWorkflowSpecV2 validates a persisted workflow with the exact
// same dependency-free contract used by the runtime compiler.
func ValidateGateWorkflowSpecV2(spec GateWorkflowSpec) error {
	_, err := ValidateAndNormalizeGateWorkflowSpecV2(spec)
	return err
}

// ValidateAndNormalizeGateWorkflowSpecV2 validates, detaches, and normalizes
// one workflow. Keeping this contract in gatetypes lets configuration writes
// and workflow compilation share one source of truth without an import cycle.
func ValidateAndNormalizeGateWorkflowSpecV2(spec GateWorkflowSpec) (GateWorkflowSpec, error) {
	if spec.ID != strings.TrimSpace(spec.ID) || !gateWorkflowV2IDPattern.MatchString(spec.ID) ||
		len(spec.ID) > MaxGateWorkflowIDBytes {
		return GateWorkflowSpec{}, fmt.Errorf(
			"gate v2 workflow id must match %s and be at most %d bytes",
			gateWorkflowV2IDPattern.String(),
			MaxGateWorkflowIDBytes,
		)
	}
	if err := validateGateWorkflowV2Text("gate v2 workflow name", spec.Name, MaxWorkflowGateNameBytes); err != nil {
		return GateWorkflowSpec{}, err
	}
	switch spec.Purpose {
	case GatePurposeAttention, GatePurposeAuthorization, GatePurposeClassification:
	default:
		return GateWorkflowSpec{}, fmt.Errorf("gate v2 workflow purpose %q is unsupported", spec.Purpose)
	}
	if spec.DecisionPoint != strings.TrimSpace(spec.DecisionPoint) ||
		!gateWorkflowV2DecisionPointPattern.MatchString(spec.DecisionPoint) ||
		len(spec.DecisionPoint) > MaxGateWorkflowDecisionPointBytes {
		return GateWorkflowSpec{}, fmt.Errorf(
			"gate v2 decision_point must match %s and be at most %d bytes",
			gateWorkflowV2DecisionPointPattern.String(),
			MaxGateWorkflowDecisionPointBytes,
		)
	}
	if len(spec.Stages) == 0 || len(spec.Stages) > MaxGateWorkflowStageCount {
		return GateWorkflowSpec{}, fmt.Errorf(
			"gate v2 workflow must contain between 1 and %d stages",
			MaxGateWorkflowStageCount,
		)
	}

	normalized := GateWorkflowSpec{
		ID: spec.ID, Name: strings.TrimSpace(spec.Name), Purpose: spec.Purpose,
		DecisionPoint: spec.DecisionPoint,
		Stages:        make([]GateStageSpec, 0, len(spec.Stages)),
	}
	seen := make(map[string]struct{}, len(spec.Stages))
	workingAgentID := ""
	for index, stage := range spec.Stages {
		path := fmt.Sprintf("gate v2 stages[%d]", index)
		validated, err := validateAndNormalizeGateWorkflowStageV2(path, stage)
		if err != nil {
			return GateWorkflowSpec{}, err
		}
		if _, exists := seen[validated.ID]; exists {
			return GateWorkflowSpec{}, fmt.Errorf("gate v2 stage %q is duplicated", validated.ID)
		}
		seen[validated.ID] = struct{}{}
		if validated.Kind == GateAIWorkingContext {
			if workingAgentID != "" && workingAgentID != validated.AgentID {
				return GateWorkflowSpec{}, fmt.Errorf(
					"working-context gate v2 stages must use one session-owning agent; got %q and %q",
					workingAgentID,
					validated.AgentID,
				)
			}
			workingAgentID = validated.AgentID
		}
		normalized.Stages = append(normalized.Stages, validated)
	}
	if _, err := CanonicalGateWorkflowSpecJSON(normalized); err != nil {
		return GateWorkflowSpec{}, err
	}
	return normalized, nil
}

func validateAndNormalizeGateWorkflowStageV2(path string, stage GateStageSpec) (GateStageSpec, error) {
	if stage.ID != strings.TrimSpace(stage.ID) || !gateWorkflowV2IDPattern.MatchString(stage.ID) ||
		len(stage.ID) > MaxWorkflowGateIDBytes {
		return GateStageSpec{}, fmt.Errorf(
			"%s.id must match %s and be at most %d bytes",
			path, gateWorkflowV2IDPattern.String(), MaxWorkflowGateIDBytes,
		)
	}
	normalized := GateStageSpec{ID: stage.ID, Kind: stage.Kind}
	switch stage.Kind {
	case GateAIWorkingContext, GateAIIsolatedContext:
		if stage.AgentID != strings.TrimSpace(stage.AgentID) ||
			!gateWorkflowV2AgentIDPattern.MatchString(stage.AgentID) {
			return GateStageSpec{}, fmt.Errorf("%s.agent_id must be an exact canonical agent ID", path)
		}
		if err := validateGateWorkflowV2Title(path+".title", stage.Title); err != nil {
			return GateStageSpec{}, err
		}
		if err := validateGateWorkflowV2Text(path+".criteria", stage.Criteria, MaxWorkflowGateCriteriaBytes); err != nil {
			return GateStageSpec{}, err
		}
		if stage.When != "" {
			return GateStageSpec{}, fmt.Errorf("%s.when is only supported for deterministic stages", path)
		}
		normalized.Title = strings.TrimSpace(stage.Title)
		normalized.Criteria = strings.TrimSpace(stage.Criteria)
		normalized.AgentID = stage.AgentID
	case GateDeterministic:
		if stage.AgentID != "" || stage.Criteria != "" || stage.Questions != nil {
			return GateStageSpec{}, fmt.Errorf(
				"%s deterministic stage cannot configure agent_id, criteria, or questions",
				path,
			)
		}
		if err := validateGateWorkflowV2Title(path+".title", stage.Title); err != nil {
			return GateStageSpec{}, err
		}
		if !utf8.ValidString(stage.When) || len(stage.When) > MaxWorkflowGateConditionBytes {
			return GateStageSpec{}, fmt.Errorf(
				"%s.when must be valid UTF-8 and at most %d bytes",
				path, MaxWorkflowGateConditionBytes,
			)
		}
		condition, err := normalizeGateWorkflowConditionV2(stage.When)
		if err != nil {
			return GateStageSpec{}, fmt.Errorf("%s.when: %w", path, err)
		}
		normalized.Title = strings.TrimSpace(stage.Title)
		normalized.When = condition
	case GateHuman:
		if stage.AgentID != "" || stage.Criteria != "" || stage.When != "" {
			return GateStageSpec{}, fmt.Errorf(
				"%s human stage cannot configure agent_id, criteria, or when",
				path,
			)
		}
		if err := validateGateWorkflowV2Title(path+".title", stage.Title); err != nil {
			return GateStageSpec{}, err
		}
		if stage.Questions == nil {
			return GateStageSpec{}, fmt.Errorf("%s.questions are required", path)
		}
		normalized.Title = strings.TrimSpace(stage.Title)
	case GateZero:
		if stage.AgentID != "" || stage.Criteria != "" || stage.When != "" ||
			stage.Title != "" || stage.Questions != nil {
			return GateStageSpec{}, fmt.Errorf("%s zero stage cannot configure behavior fields", path)
		}
	default:
		return GateStageSpec{}, fmt.Errorf("%s.kind %q is unsupported", path, stage.Kind)
	}
	if stage.Questions != nil {
		questions, err := normalizeGateWorkflowJSONV2(
			path+".questions", stage.Questions, MaxWorkflowGateQuestionBytes,
		)
		if err != nil {
			return GateStageSpec{}, err
		}
		if questions == nil {
			return GateStageSpec{}, fmt.Errorf("%s.questions are required", path)
		}
		normalized.Questions = questions
	}
	return normalized, nil
}

func validateGateWorkflowV2Title(path, value string) error {
	return validateGateWorkflowV2Text(path, value, MaxWorkflowGateTitleBytes)
}

func validateGateWorkflowV2Text(path, value string, maxBytes int) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || len(value) > maxBytes {
		return fmt.Errorf("%s must be nonblank valid UTF-8 and at most %d bytes", path, maxBytes)
	}
	return nil
}

func normalizeGateWorkflowConditionV2(value string) (string, error) {
	condition := strings.TrimSpace(value)
	if strings.HasPrefix(condition, "${{") || strings.HasSuffix(condition, "}}") {
		if !strings.HasPrefix(condition, "${{") || !strings.HasSuffix(condition, "}}") {
			return "", fmt.Errorf("expression delimiters are incomplete")
		}
		condition = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(condition, "${{"), "}}"))
	}
	if err := validateGateWorkflowExpressionSyntaxV2(condition); err != nil {
		return "", err
	}
	if err := validateGateWorkflowExpressionOperandsV2(condition); err != nil {
		return "", err
	}
	return condition, nil
}

func validateGateWorkflowExpressionSyntaxV2(expression string) error {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return fmt.Errorf("expression is empty")
	}
	if terms, ok := splitGateWorkflowExpressionANDV2(expression); ok {
		if len(terms) > MaxWorkflowGateCount {
			return fmt.Errorf("expression exceeds %d AND terms", MaxWorkflowGateCount)
		}
		for _, term := range terms {
			if err := validateGateWorkflowExpressionSyntaxV2(term); err != nil {
				return err
			}
		}
		return nil
	}
	for _, operator := range []string{" == ", " != ", " >= ", " <= ", " > ", " < "} {
		if index := strings.Index(expression, operator); index >= 0 {
			if err := validateGateWorkflowExpressionSyntaxV2(expression[:index]); err != nil {
				return err
			}
			return validateGateWorkflowExpressionSyntaxV2(expression[index+len(operator):])
		}
	}
	if strings.HasPrefix(expression, "not ") {
		return validateGateWorkflowExpressionSyntaxV2(strings.TrimSpace(strings.TrimPrefix(expression, "not ")))
	}
	if isGateWorkflowQuotedLiteralV2(expression) {
		return nil
	}
	switch expression {
	case "true", "false", "null":
		return nil
	}
	if _, err := strconv.ParseFloat(expression, 64); err == nil {
		return nil
	}
	if gateWorkflowV2ExpressionPath.MatchString(expression) {
		return nil
	}
	return fmt.Errorf("unsupported expression syntax %q", expression)
}

func validateGateWorkflowExpressionOperandsV2(expression string) error {
	expression = strings.TrimSpace(expression)
	if terms, ok := splitGateWorkflowExpressionANDV2(expression); ok {
		for _, term := range terms {
			if err := validateGateWorkflowExpressionOperandsV2(term); err != nil {
				return err
			}
		}
		return nil
	}
	for _, operator := range []string{" == ", " != ", " >= ", " <= ", " > ", " < "} {
		if index := strings.Index(expression, operator); index >= 0 {
			if err := validateGateWorkflowExpressionOperandsV2(expression[:index]); err != nil {
				return err
			}
			return validateGateWorkflowExpressionOperandsV2(expression[index+len(operator):])
		}
	}
	if strings.HasPrefix(expression, "not ") {
		return validateGateWorkflowExpressionOperandsV2(strings.TrimSpace(strings.TrimPrefix(expression, "not ")))
	}
	if isGateWorkflowQuotedLiteralV2(expression) {
		return nil
	}
	switch expression {
	case "true", "false", "null":
		return nil
	}
	if _, err := strconv.ParseFloat(expression, 64); err == nil {
		return nil
	}
	if !gateWorkflowV2ExpressionPath.MatchString(expression) {
		return fmt.Errorf("unsupported expression syntax %q", expression)
	}
	root := strings.SplitN(expression, ".", 2)[0]
	if root != "inputs" {
		return fmt.Errorf("deterministic gate expression root %q is unsupported; use inputs", root)
	}
	return nil
}

func splitGateWorkflowExpressionANDV2(expression string) ([]string, bool) {
	const operator = " and "
	var parts []string
	start, quote := 0, byte(0)
	escaped := false
	for index := 0; index < len(expression); index++ {
		character := expression[index]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if strings.HasPrefix(expression[index:], operator) {
			parts = append(parts, strings.TrimSpace(expression[start:index]))
			index += len(operator) - 1
			start = index + 1
		}
	}
	if len(parts) == 0 {
		return nil, false
	}
	return append(parts, strings.TrimSpace(expression[start:])), true
}

func isGateWorkflowQuotedLiteralV2(expression string) bool {
	if len(expression) < 2 {
		return false
	}
	return strings.HasPrefix(expression, "'") && strings.HasSuffix(expression, "'") ||
		strings.HasPrefix(expression, `"`) && strings.HasSuffix(expression, `"`)
}

func normalizeGateWorkflowJSONV2(label string, value any, maxBytes int) (any, error) {
	if err := preflightGateWorkflowJSONV2(label, value, maxBytes); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%s must be finite acyclic JSON: %w", label, err)
	}
	if len(encoded) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, fmt.Errorf("%s must be valid JSON: %w", label, err)
	}
	return normalized, nil
}

type gateWorkflowJSONV2Visit struct {
	kind    reflect.Kind
	pointer uintptr
}

type gateWorkflowJSONV2Budget struct {
	label    string
	maxBytes int
	bytes    int
	nodes    int
	active   map[gateWorkflowJSONV2Visit]struct{}
}

func preflightGateWorkflowJSONV2(label string, value any, maxBytes int) error {
	budget := &gateWorkflowJSONV2Budget{
		label: label, maxBytes: maxBytes, active: make(map[gateWorkflowJSONV2Visit]struct{}),
	}
	return budget.visit(reflect.ValueOf(value), 0)
}

func (budget *gateWorkflowJSONV2Budget) visit(value reflect.Value, depth int) error {
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
	if gateWorkflowV2HasCustomMarshaler(value.Type()) {
		return fmt.Errorf(
			"%s contains custom marshaler type %s; use JSON maps, arrays, and scalars",
			budget.label, value.Type(),
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
		if value.Type() == gateWorkflowV2JSONNumberType {
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
		encoded, err := json.Marshal(number)
		if value.Kind() == reflect.Float32 {
			encoded, err = json.Marshal(float32(number))
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
		if gateWorkflowV2HasCustomMarshaler(value.Type().Key()) {
			return fmt.Errorf("%s contains custom marshaler map-key type %s", budget.label, value.Type().Key())
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
			!gateWorkflowV2HasCustomMarshaler(value.Type().Elem()) {
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

func gateWorkflowV2HasCustomMarshaler(valueType reflect.Type) bool {
	if valueType.Implements(gateWorkflowV2JSONMarshalerType) ||
		valueType.Implements(gateWorkflowV2TextMarshalerType) {
		return true
	}
	return valueType.Kind() != reflect.Pointer &&
		(reflect.PointerTo(valueType).Implements(gateWorkflowV2JSONMarshalerType) ||
			reflect.PointerTo(valueType).Implements(gateWorkflowV2TextMarshalerType))
}

func (budget *gateWorkflowJSONV2Budget) enter(value reflect.Value) (func(), error) {
	visit := gateWorkflowJSONV2Visit{kind: value.Kind(), pointer: value.Pointer()}
	if _, exists := budget.active[visit]; exists {
		return nil, fmt.Errorf("%s must be acyclic JSON", budget.label)
	}
	budget.active[visit] = struct{}{}
	return func() { delete(budget.active, visit) }, nil
}

func (budget *gateWorkflowJSONV2Budget) addBytes(count int) error {
	if err := budget.checkBytes(count); err != nil {
		return err
	}
	budget.bytes += count
	return nil
}

func (budget *gateWorkflowJSONV2Budget) checkBytes(count int) error {
	if count < 0 || budget.bytes > budget.maxBytes-count {
		return fmt.Errorf("%s exceeds %d encoded JSON bytes", budget.label, budget.maxBytes)
	}
	return nil
}

func (budget *gateWorkflowJSONV2Budget) addJSONString(value string) error {
	if err := budget.checkBytes(len(value) + 2); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%s contains an invalid JSON string", budget.label)
	}
	return budget.addBytes(len(encoded))
}
