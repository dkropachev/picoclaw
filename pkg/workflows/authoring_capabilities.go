package workflows

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/routing"
)

const (
	// MaxWorkflowAuthoringAgents bounds projected live agent identities.
	MaxWorkflowAuthoringAgents = 128
	// MaxWorkflowAuthoringTools bounds projected built-in tool identities.
	MaxWorkflowAuthoringTools = 256
	// MaxWorkflowAuthoringMCPTools bounds projected eager MCP identities.
	MaxWorkflowAuthoringMCPTools = 256
	// MaxWorkflowAuthoringFunctions bounds projected native functions.
	MaxWorkflowAuthoringFunctions = 4
	// MaxWorkflowAuthoringShapeDepth bounds nested properties and items.
	MaxWorkflowAuthoringShapeDepth = 6
	// MaxWorkflowAuthoringShapeProperties bounds properties on one object.
	MaxWorkflowAuthoringShapeProperties = 128
	// MaxWorkflowAuthoringShapeRequired bounds one required declaration.
	MaxWorkflowAuthoringShapeRequired = 128
	// MaxWorkflowAuthoringShapeEnum bounds one scalar enum.
	MaxWorkflowAuthoringShapeEnum = 64
	// MaxWorkflowAuthoringShapeUnits bounds all shape nodes, properties,
	// required names, and enum values projected into one catalog.
	MaxWorkflowAuthoringShapeUnits = 4096
	// MaxWorkflowAuthoringStringBytes bounds each projected identity or schema string.
	MaxWorkflowAuthoringStringBytes = 256
	// MaxWorkflowAuthoringTargetBytes bounds each exact copyable uses target.
	MaxWorkflowAuthoringTargetBytes = 1024
	// MaxWorkflowAuthoringResponseBytes bounds an encoded gateway or launcher response.
	MaxWorkflowAuthoringResponseBytes int64 = 4 << 20

	// RuntimeAuthoringCapabilitiesPath is the protected live-gateway endpoint
	// used by the launcher proxy.
	RuntimeAuthoringCapabilitiesPath = "/runtime/workflows/authoring/capabilities"
)

// WorkflowAuthoringMCPStatus is the fixed state of the live MCP catalog.
type WorkflowAuthoringMCPStatus string

const (
	WorkflowAuthoringMCPReady       WorkflowAuthoringMCPStatus = "ready"
	WorkflowAuthoringMCPDisabled    WorkflowAuthoringMCPStatus = "disabled"
	WorkflowAuthoringMCPUnavailable WorkflowAuthoringMCPStatus = "unavailable"
)

// WorkflowAuthoringLimitCode is a fixed, non-sensitive omission reason.
type WorkflowAuthoringLimitCode string

const (
	WorkflowAuthoringAgentsTruncated        WorkflowAuthoringLimitCode = "agents_truncated"
	WorkflowAuthoringToolsTruncated         WorkflowAuthoringLimitCode = "tools_truncated"
	WorkflowAuthoringMCPToolsTruncated      WorkflowAuthoringLimitCode = "mcp_tools_truncated"
	WorkflowAuthoringFunctionsTruncated     WorkflowAuthoringLimitCode = "functions_truncated"
	WorkflowAuthoringParameterShapesOmitted WorkflowAuthoringLimitCode = "parameter_shapes_omitted"
	WorkflowAuthoringUnsafeFieldsOmitted    WorkflowAuthoringLimitCode = "unsafe_fields_omitted"
)

// WorkflowAuthoringCapabilities is a sanitized snapshot of the current
// workflow action surface. It intentionally contains no descriptions,
// configuration, paths, defaults, examples, or provider errors.
type WorkflowAuthoringCapabilities struct {
	Complete  bool                                  `json:"complete"`
	MCPStatus WorkflowAuthoringMCPStatus            `json:"mcp_status"`
	Agents    []WorkflowAuthoringAgentCapability    `json:"agents"`
	Tools     []WorkflowAuthoringToolCapability     `json:"tools"`
	MCPTools  []WorkflowAuthoringMCPToolCapability  `json:"mcp_tools"`
	Functions []WorkflowAuthoringFunctionCapability `json:"functions"`
	Limits    []WorkflowAuthoringLimitCode          `json:"limits"`
}

type WorkflowAuthoringAgentCapability struct {
	ID        string                          `json:"id"`
	Target    string                          `json:"target"`
	IsDefault bool                            `json:"is_default"`
	Readiness WorkflowDependencyReadinessCode `json:"readiness"`
}

type WorkflowAuthoringToolCapability struct {
	Name                    string                           `json:"name"`
	Target                  string                           `json:"target"`
	Readiness               WorkflowDependencyReadinessCode  `json:"readiness"`
	ParameterShapeProjected bool                             `json:"parameter_shape_projected"`
	ParameterShape          *WorkflowAuthoringParameterShape `json:"parameter_shape,omitempty"`
}

type WorkflowAuthoringMCPToolCapability struct {
	Server                  string                           `json:"server"`
	Tool                    string                           `json:"tool"`
	Target                  string                           `json:"target"`
	Readiness               WorkflowDependencyReadinessCode  `json:"readiness"`
	ParameterShapeProjected bool                             `json:"parameter_shape_projected"`
	ParameterShape          *WorkflowAuthoringParameterShape `json:"parameter_shape,omitempty"`
}

type WorkflowAuthoringFunctionCapability struct {
	Name      string                          `json:"name"`
	Target    string                          `json:"target"`
	Readiness WorkflowDependencyReadinessCode `json:"readiness"`
}

// WorkflowAuthoringParameterShape is the only JSON-Schema subset exposed by
// the authoring catalog. Properties use an array so ordering is deterministic
// and no attacker-controlled object keys are written into browser state.
type WorkflowAuthoringParameterShape struct {
	Type                 string                                 `json:"type,omitempty"`
	Properties           []WorkflowAuthoringParameterProperty   `json:"properties,omitempty"`
	Items                *WorkflowAuthoringParameterShape       `json:"items,omitempty"`
	Enum                 []WorkflowAuthoringScalar              `json:"enum,omitempty"`
	AdditionalProperties *WorkflowAuthoringAdditionalProperties `json:"additional_properties,omitempty"`
}

type WorkflowAuthoringParameterProperty struct {
	Name     string                          `json:"name"`
	Required bool                            `json:"required"`
	Shape    WorkflowAuthoringParameterShape `json:"shape"`
}

// WorkflowAuthoringAdditionalProperties is an exact union: Allowed is set for
// a boolean declaration, while Shape is set for a schema declaration.
type WorkflowAuthoringAdditionalProperties struct {
	Allowed *bool                            `json:"allowed,omitempty"`
	Shape   *WorkflowAuthoringParameterShape `json:"shape,omitempty"`
}

// WorkflowAuthoringScalar is a typed scalar union that marshals to a native
// JSON string, number, boolean, or null.
type WorkflowAuthoringScalar struct {
	kind    workflowAuthoringScalarKind
	text    string
	number  string
	boolean bool
}

type workflowAuthoringScalarKind uint8

const (
	workflowAuthoringScalarNull workflowAuthoringScalarKind = iota
	workflowAuthoringScalarString
	workflowAuthoringScalarNumber
	workflowAuthoringScalarBoolean
)

func (value WorkflowAuthoringScalar) MarshalJSON() ([]byte, error) {
	switch value.kind {
	case workflowAuthoringScalarNull:
		return []byte("null"), nil
	case workflowAuthoringScalarString:
		return json.Marshal(value.text)
	case workflowAuthoringScalarNumber:
		if !WorkflowJSONNumberIsBrowserSafe(value.number) {
			return []byte("null"), nil
		}
		return []byte(value.number), nil
	case workflowAuthoringScalarBoolean:
		return json.Marshal(value.boolean)
	default:
		return []byte("null"), nil
	}
}

func (value *WorkflowAuthoringScalar) UnmarshalJSON(raw []byte) error {
	if value == nil {
		return errors.New("workflow authoring scalar destination is nil")
	}
	switch string(raw) {
	case "null":
		*value = WorkflowAuthoringScalar{kind: workflowAuthoringScalarNull}
		return nil
	case "true":
		*value = WorkflowAuthoringScalar{
			kind:    workflowAuthoringScalarBoolean,
			boolean: true,
		}
		return nil
	case "false":
		*value = WorkflowAuthoringScalar{
			kind: workflowAuthoringScalarBoolean,
		}
		return nil
	}
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil ||
			!safeWorkflowAuthoringString(text, MaxWorkflowAuthoringStringBytes) {
			return errors.New("invalid workflow authoring scalar string")
		}
		*value = WorkflowAuthoringScalar{
			kind: workflowAuthoringScalarString,
			text: text,
		}
		return nil
	}
	text := string(raw)
	projected, ok := workflowAuthoringNumberScalar(text)
	if !ok {
		return errors.New("invalid workflow authoring scalar number")
	}
	*value = projected
	return nil
}

// WorkflowAuthoringShapeSanitizer transactionally projects parameter schemas
// against one aggregate catalog budget.
type WorkflowAuthoringShapeSanitizer struct {
	units int
	work  int
}

// Project returns a whitelisted shape. Failed projections do not consume the
// shared unit budget, so one malformed tool cannot suppress later safe tools.
func (sanitizer *WorkflowAuthoringShapeSanitizer) Project(
	schema map[string]any,
) (*WorkflowAuthoringParameterShape, bool) {
	if sanitizer == nil {
		return nil, false
	}
	trial := sanitizer.units
	shape, ok := projectWorkflowAuthoringShape(schema, 1, &trial, &sanitizer.work)
	if !ok {
		return nil, false
	}
	sanitizer.units = trial
	return &shape, true
}

func projectWorkflowAuthoringShape(
	schema map[string]any,
	depth int,
	units *int,
	work *int,
) (WorkflowAuthoringParameterShape, bool) {
	if depth > MaxWorkflowAuthoringShapeDepth ||
		!consumeWorkflowAuthoringUnits(work, 1) ||
		!consumeWorkflowAuthoringUnits(units, 1) {
		return WorkflowAuthoringParameterShape{}, false
	}
	if schema == nil {
		schema = map[string]any{}
	}
	if workflowAuthoringSchemaUsesUnsupportedStructure(schema) {
		return WorkflowAuthoringParameterShape{}, false
	}

	var out WorkflowAuthoringParameterShape
	if rawType, exists := schema["type"]; exists {
		value, ok := rawType.(string)
		if !ok || !validWorkflowAuthoringSchemaType(value) {
			return WorkflowAuthoringParameterShape{}, false
		}
		out.Type = value
	}

	var properties map[string]any
	var propertyNames []string
	if rawProperties, exists := schema["properties"]; exists {
		var ok bool
		properties, ok = workflowAuthoringStringMap(rawProperties)
		if !ok || len(properties) > MaxWorkflowAuthoringShapeProperties {
			return WorkflowAuthoringParameterShape{}, false
		}
		propertyNames = make([]string, 0, len(properties))
		for name := range properties {
			if !SafeWorkflowAuthoringIdentity(name) {
				return WorkflowAuthoringParameterShape{}, false
			}
			propertyNames = append(propertyNames, name)
		}
		sort.Strings(propertyNames)
	}

	requiredSet := make(map[string]struct{})
	if rawRequired, exists := schema["required"]; exists {
		required, ok := workflowAuthoringStringSlice(
			rawRequired,
			MaxWorkflowAuthoringShapeRequired,
		)
		if !ok {
			return WorkflowAuthoringParameterShape{}, false
		}
		sort.Strings(required)
		required = compactWorkflowAuthoringStrings(required)
		for _, name := range required {
			if _, exists := properties[name]; !exists {
				return WorkflowAuthoringParameterShape{}, false
			}
			requiredSet[name] = struct{}{}
		}
		if !consumeWorkflowAuthoringUnits(work, len(required)) ||
			!consumeWorkflowAuthoringUnits(units, len(required)) {
			return WorkflowAuthoringParameterShape{}, false
		}
	}

	if properties != nil {
		out.Properties = make([]WorkflowAuthoringParameterProperty, 0, len(propertyNames))
		for _, name := range propertyNames {
			if !consumeWorkflowAuthoringUnits(work, 1) ||
				!consumeWorkflowAuthoringUnits(units, 1) {
				return WorkflowAuthoringParameterShape{}, false
			}
			childMap, ok := workflowAuthoringStringMap(properties[name])
			if !ok {
				return WorkflowAuthoringParameterShape{}, false
			}
			child, ok := projectWorkflowAuthoringShape(childMap, depth+1, units, work)
			if !ok {
				return WorkflowAuthoringParameterShape{}, false
			}
			_, required := requiredSet[name]
			out.Properties = append(out.Properties, WorkflowAuthoringParameterProperty{
				Name:     name,
				Required: required,
				Shape:    child,
			})
		}
	}

	if rawItems, exists := schema["items"]; exists {
		items, ok := workflowAuthoringStringMap(rawItems)
		if !ok {
			return WorkflowAuthoringParameterShape{}, false
		}
		projected, ok := projectWorkflowAuthoringShape(items, depth+1, units, work)
		if !ok {
			return WorkflowAuthoringParameterShape{}, false
		}
		out.Items = &projected
	}

	if rawEnum, exists := schema["enum"]; exists {
		values, ok := workflowAuthoringAnySlice(rawEnum, MaxWorkflowAuthoringShapeEnum)
		if !ok ||
			!consumeWorkflowAuthoringUnits(work, len(values)) ||
			!consumeWorkflowAuthoringUnits(units, len(values)) {
			return WorkflowAuthoringParameterShape{}, false
		}
		out.Enum = make([]WorkflowAuthoringScalar, 0, len(values))
		seen := make(map[WorkflowAuthoringScalar]struct{}, len(values))
		for _, raw := range values {
			value, ok := projectWorkflowAuthoringScalar(raw)
			if !ok {
				return WorkflowAuthoringParameterShape{}, false
			}
			if _, duplicate := seen[value]; duplicate {
				return WorkflowAuthoringParameterShape{}, false
			}
			seen[value] = struct{}{}
			out.Enum = append(out.Enum, value)
		}
	}

	if rawAdditional, exists := schema["additionalProperties"]; exists {
		switch value := rawAdditional.(type) {
		case bool:
			out.AdditionalProperties = &WorkflowAuthoringAdditionalProperties{
				Allowed: &value,
			}
		case map[string]any:
			projected, ok := projectWorkflowAuthoringShape(value, depth+1, units, work)
			if !ok {
				return WorkflowAuthoringParameterShape{}, false
			}
			out.AdditionalProperties = &WorkflowAuthoringAdditionalProperties{
				Shape: &projected,
			}
		default:
			return WorkflowAuthoringParameterShape{}, false
		}
	}

	return out, true
}

func validWorkflowAuthoringSchemaType(value string) bool {
	if !safeWorkflowAuthoringString(value, MaxWorkflowAuthoringStringBytes) ||
		value != strings.TrimSpace(value) {
		return false
	}
	switch value {
	case "object", "array", "string", "number", "integer", "boolean", "null":
		return true
	default:
		return false
	}
}

func workflowAuthoringSchemaUsesUnsupportedStructure(schema map[string]any) bool {
	for _, keyword := range []string{
		"$ref",
		"$dynamicRef",
		"$recursiveRef",
		"$defs",
		"definitions",
		"allOf",
		"anyOf",
		"oneOf",
		"not",
		"if",
		"then",
		"else",
		"dependentSchemas",
		"dependencies",
		"patternProperties",
		"propertyNames",
		"prefixItems",
		"contains",
		"unevaluatedProperties",
		"unevaluatedItems",
	} {
		if _, exists := schema[keyword]; exists {
			return true
		}
	}
	return false
}

func projectWorkflowAuthoringScalar(raw any) (WorkflowAuthoringScalar, bool) {
	switch value := raw.(type) {
	case nil:
		return WorkflowAuthoringScalar{kind: workflowAuthoringScalarNull}, true
	case string:
		if !safeWorkflowAuthoringString(value, MaxWorkflowAuthoringStringBytes) {
			return WorkflowAuthoringScalar{}, false
		}
		return WorkflowAuthoringScalar{kind: workflowAuthoringScalarString, text: value}, true
	case bool:
		return WorkflowAuthoringScalar{kind: workflowAuthoringScalarBoolean, boolean: value}, true
	case json.Number:
		return workflowAuthoringNumberScalar(value.String())
	case float64:
		return workflowAuthoringNumberScalar(strconv.FormatFloat(value, 'g', -1, 64))
	case float32:
		return workflowAuthoringNumberScalar(
			strconv.FormatFloat(float64(value), 'g', -1, 32),
		)
	case int:
		return workflowAuthoringIntegerScalar(strconv.FormatInt(int64(value), 10))
	case int8:
		return workflowAuthoringIntegerScalar(strconv.FormatInt(int64(value), 10))
	case int16:
		return workflowAuthoringIntegerScalar(strconv.FormatInt(int64(value), 10))
	case int32:
		return workflowAuthoringIntegerScalar(strconv.FormatInt(int64(value), 10))
	case int64:
		return workflowAuthoringIntegerScalar(strconv.FormatInt(value, 10))
	case uint:
		return workflowAuthoringIntegerScalar(strconv.FormatUint(uint64(value), 10))
	case uint8:
		return workflowAuthoringIntegerScalar(strconv.FormatUint(uint64(value), 10))
	case uint16:
		return workflowAuthoringIntegerScalar(strconv.FormatUint(uint64(value), 10))
	case uint32:
		return workflowAuthoringIntegerScalar(strconv.FormatUint(uint64(value), 10))
	case uint64:
		return workflowAuthoringIntegerScalar(strconv.FormatUint(value, 10))
	default:
		return WorkflowAuthoringScalar{}, false
	}
}

func workflowAuthoringIntegerScalar(text string) (WorkflowAuthoringScalar, bool) {
	return workflowAuthoringNumberScalar(text)
}

func workflowAuthoringNumberScalar(text string) (WorkflowAuthoringScalar, bool) {
	if text == "" ||
		len(text) > MaxWorkflowAuthoringStringBytes ||
		!WorkflowJSONNumberIsBrowserSafe(text) {
		return WorkflowAuthoringScalar{}, false
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return WorkflowAuthoringScalar{}, false
	}
	if number == 0 {
		text = "0"
	} else if math.Trunc(number) == number {
		text = strconv.FormatFloat(number, 'f', -1, 64)
	} else {
		text = strconv.FormatFloat(number, 'g', -1, 64)
	}
	return WorkflowAuthoringScalar{
		kind:   workflowAuthoringScalarNumber,
		number: text,
	}, true
}

func workflowAuthoringStringMap(raw any) (map[string]any, bool) {
	switch value := raw.(type) {
	case map[string]any:
		return value, true
	case nil:
		return nil, false
	default:
		return nil, false
	}
}

func workflowAuthoringStringSlice(raw any, maximum int) ([]string, bool) {
	switch values := raw.(type) {
	case []string:
		if len(values) > maximum {
			return nil, false
		}
		out := append([]string(nil), values...)
		for _, value := range out {
			if !SafeWorkflowAuthoringIdentity(value) {
				return nil, false
			}
		}
		return out, true
	case []any:
		if len(values) > maximum {
			return nil, false
		}
		out := make([]string, 0, len(values))
		for _, rawValue := range values {
			value, ok := rawValue.(string)
			if !ok || !SafeWorkflowAuthoringIdentity(value) {
				return nil, false
			}
			out = append(out, value)
		}
		return out, true
	default:
		return nil, false
	}
}

func workflowAuthoringAnySlice(raw any, maximum int) ([]any, bool) {
	switch values := raw.(type) {
	case []any:
		if len(values) > maximum {
			return nil, false
		}
		return values, true
	case []string:
		if len(values) > maximum {
			return nil, false
		}
		out := make([]any, len(values))
		for index := range values {
			out[index] = values[index]
		}
		return out, true
	default:
		return nil, false
	}
}

func compactWorkflowAuthoringStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func consumeWorkflowAuthoringUnits(units *int, count int) bool {
	if units == nil || count < 0 || *units > MaxWorkflowAuthoringShapeUnits-count {
		return false
	}
	*units += count
	return true
}

// SafeWorkflowAuthoringIdentity validates a public catalog identity.
func SafeWorkflowAuthoringIdentity(value string) bool {
	return value != "" &&
		safeWorkflowAuthoringString(value, MaxWorkflowAuthoringStringBytes) &&
		value == strings.TrimSpace(value)
}

// SafeWorkflowAuthoringAgentID validates an exact, directly addressable
// runtime agent ID without applying lossy routing normalization.
func SafeWorkflowAuthoringAgentID(value string) bool {
	return SafeWorkflowAuthoringIdentity(value) &&
		routing.IsCanonicalAgentID(value)
}

// SafeWorkflowAuthoringTarget validates a complete copyable uses target.
func SafeWorkflowAuthoringTarget(value string) bool {
	return value != "" &&
		safeWorkflowAuthoringString(value, MaxWorkflowAuthoringTargetBytes) &&
		value == strings.TrimSpace(value)
}

func safeWorkflowAuthoringString(value string, maxBytes int) bool {
	if maxBytes < 0 || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.Is(unicode.Cc, character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	return true
}

// NormalizeWorkflowAuthoringLimits sorts and deduplicates omission codes.
func NormalizeWorkflowAuthoringLimits(
	values []WorkflowAuthoringLimitCode,
) []WorkflowAuthoringLimitCode {
	if len(values) == 0 {
		return []WorkflowAuthoringLimitCode{}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

// DecodeWorkflowAuthoringCapabilities strictly decodes and semantically
// validates a gateway response before it reaches the browser. Unknown or
// duplicate fields are rejected recursively.
func DecodeWorkflowAuthoringCapabilities(
	raw []byte,
) (WorkflowAuthoringCapabilities, error) {
	var catalog WorkflowAuthoringCapabilities
	if len(raw) == 0 ||
		int64(len(raw)) > MaxWorkflowAuthoringResponseBytes ||
		!utf8.Valid(raw) {
		return catalog, errors.New("invalid workflow authoring capabilities response")
	}
	if err := rejectDuplicateWorkflowAuthoringJSONKeys(raw); err != nil {
		return catalog, err
	}
	if err := validateWorkflowAuthoringJSONStructure(raw); err != nil {
		return catalog, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&catalog); err != nil {
		return WorkflowAuthoringCapabilities{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return WorkflowAuthoringCapabilities{}, errors.New(
			"workflow authoring capabilities response has trailing data",
		)
	}
	if err := ValidateWorkflowAuthoringCapabilities(catalog); err != nil {
		return WorkflowAuthoringCapabilities{}, err
	}
	return catalog, nil
}

// ValidateWorkflowAuthoringCapabilities verifies the complete public catalog
// contract, including ordering, exact targets, fixed enums, and aggregate
// parameter-shape bounds.
func ValidateWorkflowAuthoringCapabilities(
	catalog WorkflowAuthoringCapabilities,
) error {
	switch catalog.MCPStatus {
	case WorkflowAuthoringMCPReady,
		WorkflowAuthoringMCPDisabled,
		WorkflowAuthoringMCPUnavailable:
	default:
		return errors.New("invalid workflow authoring MCP status")
	}
	if catalog.Agents == nil ||
		catalog.Tools == nil ||
		catalog.MCPTools == nil ||
		catalog.Functions == nil ||
		catalog.Limits == nil {
		return errors.New("workflow authoring capability arrays are required")
	}
	if len(catalog.Agents) > MaxWorkflowAuthoringAgents ||
		len(catalog.Tools) > MaxWorkflowAuthoringTools ||
		len(catalog.MCPTools) > MaxWorkflowAuthoringMCPTools ||
		len(catalog.Functions) > MaxWorkflowAuthoringFunctions {
		return errors.New("workflow authoring capability count exceeded")
	}
	if catalog.MCPStatus != WorkflowAuthoringMCPReady && len(catalog.MCPTools) != 0 {
		return errors.New("MCP capabilities require ready status")
	}
	if err := validateWorkflowAuthoringLimits(catalog.Limits); err != nil {
		return err
	}
	expectedComplete := catalog.MCPStatus != WorkflowAuthoringMCPUnavailable &&
		len(catalog.Limits) == 0
	if catalog.Complete != expectedComplete {
		return errors.New("inconsistent workflow authoring completeness")
	}

	defaults := 0
	previousAgent := ""
	for index, capability := range catalog.Agents {
		if !SafeWorkflowAuthoringAgentID(capability.ID) ||
			capability.Target != "agent/"+capability.ID ||
			!SafeWorkflowAuthoringTarget(capability.Target) ||
			!validWorkflowAuthoringReadiness(capability.Readiness) ||
			index > 0 && capability.ID <= previousAgent {
			return errors.New("invalid workflow authoring agent capability")
		}
		if capability.IsDefault {
			defaults++
		}
		previousAgent = capability.ID
	}
	if defaults > 1 {
		return errors.New("multiple default workflow authoring agents")
	}
	if defaults != 1 {
		return errors.New("default workflow authoring agent is required")
	}

	units := 0
	previousTool := ""
	shapeOmitted := false
	for index, capability := range catalog.Tools {
		if !SafeWorkflowAuthoringIdentity(capability.Name) ||
			strings.EqualFold(capability.Name, "workflow") ||
			capability.Target != "tool/"+capability.Name ||
			!SafeWorkflowAuthoringTarget(capability.Target) ||
			capability.Readiness != WorkflowDependencyReadinessReady ||
			index > 0 && capability.Name <= previousTool {
			return errors.New("invalid workflow authoring tool capability")
		}
		if err := validateWorkflowAuthoringProjectedShape(
			capability.ParameterShapeProjected,
			capability.ParameterShape,
			&units,
		); err != nil {
			return err
		}
		shapeOmitted = shapeOmitted || !capability.ParameterShapeProjected
		previousTool = capability.Name
	}

	previousMCPServer := ""
	previousMCPTool := ""
	for index, capability := range catalog.MCPTools {
		if !SafeWorkflowAuthoringIdentity(capability.Server) ||
			strings.Contains(capability.Server, "/") ||
			!SafeWorkflowAuthoringIdentity(capability.Tool) ||
			strings.Contains(capability.Tool, "/") ||
			capability.Target != "mcp/"+capability.Server+"/"+capability.Tool ||
			!SafeWorkflowAuthoringTarget(capability.Target) ||
			capability.Readiness != WorkflowDependencyReadinessReady {
			return errors.New("invalid workflow authoring MCP capability")
		}
		if index > 0 &&
			(capability.Server < previousMCPServer ||
				capability.Server == previousMCPServer &&
					capability.Tool <= previousMCPTool) {
			return errors.New("unsorted workflow authoring MCP capabilities")
		}
		if err := validateWorkflowAuthoringProjectedShape(
			capability.ParameterShapeProjected,
			capability.ParameterShape,
			&units,
		); err != nil {
			return err
		}
		shapeOmitted = shapeOmitted || !capability.ParameterShapeProjected
		previousMCPServer = capability.Server
		previousMCPTool = capability.Tool
	}

	nativeNames := NativeFunctionNames()
	nativeSet := make(map[string]struct{}, len(nativeNames))
	for _, name := range nativeNames {
		nativeSet[name] = struct{}{}
	}
	previousFunction := ""
	for index, capability := range catalog.Functions {
		_, native := nativeSet[capability.Name]
		if !native ||
			!SafeWorkflowAuthoringIdentity(capability.Name) ||
			capability.Target != "function/"+capability.Name ||
			!SafeWorkflowAuthoringTarget(capability.Target) ||
			capability.Readiness != WorkflowDependencyReadinessReady ||
			index > 0 && capability.Name <= previousFunction {
			return errors.New("invalid workflow authoring function capability")
		}
		previousFunction = capability.Name
	}
	if !workflowAuthoringLimitPresent(catalog.Limits, WorkflowAuthoringFunctionsTruncated) {
		if len(catalog.Functions) != len(nativeNames) {
			return errors.New("incomplete workflow authoring function catalog")
		}
		for index, name := range nativeNames {
			if catalog.Functions[index].Name != name {
				return errors.New("incomplete workflow authoring function catalog")
			}
		}
	}

	hasShapeOmission := workflowAuthoringLimitPresent(
		catalog.Limits,
		WorkflowAuthoringParameterShapesOmitted,
	)
	if hasShapeOmission != shapeOmitted {
		return errors.New("inconsistent workflow authoring shape omission")
	}
	if workflowAuthoringLimitPresent(catalog.Limits, WorkflowAuthoringAgentsTruncated) &&
		len(catalog.Agents) != MaxWorkflowAuthoringAgents {
		return errors.New("inconsistent workflow authoring agent limit")
	}
	if workflowAuthoringLimitPresent(catalog.Limits, WorkflowAuthoringToolsTruncated) &&
		len(catalog.Tools) != MaxWorkflowAuthoringTools {
		return errors.New("inconsistent workflow authoring tool limit")
	}
	if workflowAuthoringLimitPresent(catalog.Limits, WorkflowAuthoringMCPToolsTruncated) &&
		len(catalog.MCPTools) != MaxWorkflowAuthoringMCPTools {
		return errors.New("inconsistent workflow authoring MCP limit")
	}
	if workflowAuthoringLimitPresent(catalog.Limits, WorkflowAuthoringFunctionsTruncated) &&
		len(catalog.Functions) != MaxWorkflowAuthoringFunctions {
		return errors.New("inconsistent workflow authoring function limit")
	}
	return nil
}

func validateWorkflowAuthoringProjectedShape(
	projected bool,
	shape *WorkflowAuthoringParameterShape,
	units *int,
) error {
	if projected != (shape != nil) {
		return errors.New("inconsistent workflow authoring parameter shape")
	}
	if shape == nil {
		return nil
	}
	return validateWorkflowAuthoringShape(shape, 1, units)
}

func validateWorkflowAuthoringShape(
	shape *WorkflowAuthoringParameterShape,
	depth int,
	units *int,
) error {
	if shape == nil ||
		depth > MaxWorkflowAuthoringShapeDepth ||
		!consumeWorkflowAuthoringUnits(units, 1) {
		return errors.New("workflow authoring parameter shape limit exceeded")
	}
	if shape.Type != "" && !validWorkflowAuthoringSchemaType(shape.Type) {
		return errors.New("invalid workflow authoring parameter type")
	}
	if len(shape.Properties) > MaxWorkflowAuthoringShapeProperties ||
		len(shape.Enum) > MaxWorkflowAuthoringShapeEnum {
		return errors.New("workflow authoring parameter shape limit exceeded")
	}
	previousProperty := ""
	requiredCount := 0
	for index := range shape.Properties {
		property := &shape.Properties[index]
		if !SafeWorkflowAuthoringIdentity(property.Name) ||
			index > 0 && property.Name <= previousProperty ||
			!consumeWorkflowAuthoringUnits(units, 1) {
			return errors.New("invalid workflow authoring parameter property")
		}
		if property.Required {
			requiredCount++
			if requiredCount > MaxWorkflowAuthoringShapeRequired ||
				!consumeWorkflowAuthoringUnits(units, 1) {
				return errors.New("workflow authoring required limit exceeded")
			}
		}
		if err := validateWorkflowAuthoringShape(&property.Shape, depth+1, units); err != nil {
			return err
		}
		previousProperty = property.Name
	}
	if shape.Items != nil {
		if err := validateWorkflowAuthoringShape(shape.Items, depth+1, units); err != nil {
			return err
		}
	}
	if !consumeWorkflowAuthoringUnits(units, len(shape.Enum)) {
		return errors.New("workflow authoring enum limit exceeded")
	}
	seenEnum := make(map[WorkflowAuthoringScalar]struct{}, len(shape.Enum))
	for _, value := range shape.Enum {
		if !validWorkflowAuthoringScalar(value) {
			return errors.New("invalid workflow authoring enum scalar")
		}
		if _, duplicate := seenEnum[value]; duplicate {
			return errors.New("duplicate workflow authoring enum scalar")
		}
		seenEnum[value] = struct{}{}
	}
	if additional := shape.AdditionalProperties; additional != nil {
		if (additional.Allowed == nil) == (additional.Shape == nil) {
			return errors.New("invalid workflow authoring additional properties")
		}
		if additional.Shape != nil {
			if err := validateWorkflowAuthoringShape(
				additional.Shape,
				depth+1,
				units,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func validWorkflowAuthoringScalar(value WorkflowAuthoringScalar) bool {
	switch value.kind {
	case workflowAuthoringScalarNull, workflowAuthoringScalarBoolean:
		return true
	case workflowAuthoringScalarString:
		return safeWorkflowAuthoringString(value.text, MaxWorkflowAuthoringStringBytes)
	case workflowAuthoringScalarNumber:
		return WorkflowJSONNumberIsBrowserSafe(value.number)
	default:
		return false
	}
}

func validWorkflowAuthoringReadiness(value WorkflowDependencyReadinessCode) bool {
	switch value {
	case WorkflowDependencyReadinessReady,
		WorkflowDependencyReadinessUnchecked,
		WorkflowDependencyReadinessNotConfigured,
		WorkflowDependencyReadinessDisabled,
		WorkflowDependencyReadinessNotAllowed,
		WorkflowDependencyReadinessNotConnected,
		WorkflowDependencyReadinessNotFound,
		WorkflowDependencyReadinessInvalidConfiguration,
		WorkflowDependencyReadinessNameCollision,
		WorkflowDependencyReadinessUnavailable:
		return true
	default:
		return false
	}
}

func validateWorkflowAuthoringLimits(values []WorkflowAuthoringLimitCode) error {
	previous := WorkflowAuthoringLimitCode("")
	for index, value := range values {
		switch value {
		case WorkflowAuthoringAgentsTruncated,
			WorkflowAuthoringToolsTruncated,
			WorkflowAuthoringMCPToolsTruncated,
			WorkflowAuthoringFunctionsTruncated,
			WorkflowAuthoringParameterShapesOmitted,
			WorkflowAuthoringUnsafeFieldsOmitted:
		default:
			return errors.New("invalid workflow authoring limit code")
		}
		if index > 0 && value <= previous {
			return errors.New("unsorted workflow authoring limit codes")
		}
		previous = value
	}
	return nil
}

func workflowAuthoringLimitPresent(
	values []WorkflowAuthoringLimitCode,
	target WorkflowAuthoringLimitCode,
) bool {
	index := sort.Search(len(values), func(index int) bool {
		return values[index] >= target
	})
	return index < len(values) && values[index] == target
}

func rejectDuplicateWorkflowAuthoringJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := consumeUniqueWorkflowAuthoringJSONValue(decoder, 0); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("workflow authoring capabilities response has trailing data")
	}
	return nil
}

type workflowAuthoringJSONContext uint8

const (
	workflowAuthoringJSONRoot workflowAuthoringJSONContext = iota
	workflowAuthoringJSONAgent
	workflowAuthoringJSONTool
	workflowAuthoringJSONMCPTool
	workflowAuthoringJSONFunction
	workflowAuthoringJSONShape
	workflowAuthoringJSONProperty
	workflowAuthoringJSONAdditional
)

type workflowAuthoringJSONSpec struct {
	allowed  map[string]struct{}
	required []string
}

var workflowAuthoringJSONSpecs = map[workflowAuthoringJSONContext]workflowAuthoringJSONSpec{
	workflowAuthoringJSONRoot: {
		allowed: workflowAuthoringJSONKeySet(
			"complete", "mcp_status", "agents", "tools", "mcp_tools", "functions", "limits",
		),
		required: []string{
			"complete", "mcp_status", "agents", "tools", "mcp_tools", "functions", "limits",
		},
	},
	workflowAuthoringJSONAgent: {
		allowed:  workflowAuthoringJSONKeySet("id", "target", "is_default", "readiness"),
		required: []string{"id", "target", "is_default", "readiness"},
	},
	workflowAuthoringJSONTool: {
		allowed: workflowAuthoringJSONKeySet(
			"name", "target", "readiness", "parameter_shape_projected", "parameter_shape",
		),
		required: []string{"name", "target", "readiness", "parameter_shape_projected"},
	},
	workflowAuthoringJSONMCPTool: {
		allowed: workflowAuthoringJSONKeySet(
			"server", "tool", "target", "readiness",
			"parameter_shape_projected", "parameter_shape",
		),
		required: []string{
			"server", "tool", "target", "readiness", "parameter_shape_projected",
		},
	},
	workflowAuthoringJSONFunction: {
		allowed:  workflowAuthoringJSONKeySet("name", "target", "readiness"),
		required: []string{"name", "target", "readiness"},
	},
	workflowAuthoringJSONShape: {
		allowed: workflowAuthoringJSONKeySet(
			"type", "properties", "items", "enum", "additional_properties",
		),
	},
	workflowAuthoringJSONProperty: {
		allowed:  workflowAuthoringJSONKeySet("name", "required", "shape"),
		required: []string{"name", "required", "shape"},
	},
	workflowAuthoringJSONAdditional: {
		allowed: workflowAuthoringJSONKeySet("allowed", "shape"),
	},
}

func workflowAuthoringJSONKeySet(values ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func validateWorkflowAuthoringJSONStructure(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return err
	}
	if err := validateWorkflowAuthoringJSONContext(
		root,
		workflowAuthoringJSONRoot,
		0,
	); err != nil {
		return err
	}
	return rejectWorkflowAuthoringNullValue(root, false)
}

func validateWorkflowAuthoringJSONContext(
	value any,
	jsonContext workflowAuthoringJSONContext,
	depth int,
) error {
	const maxWorkflowAuthoringJSONDepth = 32
	if depth > maxWorkflowAuthoringJSONDepth {
		return errors.New("workflow authoring JSON depth exceeded")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return errors.New("workflow authoring JSON object is required")
	}
	spec := workflowAuthoringJSONSpecs[jsonContext]
	for key := range object {
		if _, allowed := spec.allowed[key]; !allowed {
			return errors.New("unknown or non-canonical workflow authoring JSON field")
		}
	}
	for _, required := range spec.required {
		if _, present := object[required]; !present {
			return errors.New("required workflow authoring JSON field is missing")
		}
	}

	switch jsonContext {
	case workflowAuthoringJSONRoot:
		for _, child := range []struct {
			key     string
			context workflowAuthoringJSONContext
		}{
			{"agents", workflowAuthoringJSONAgent},
			{"tools", workflowAuthoringJSONTool},
			{"mcp_tools", workflowAuthoringJSONMCPTool},
			{"functions", workflowAuthoringJSONFunction},
		} {
			if err := validateWorkflowAuthoringJSONArray(
				object[child.key],
				child.context,
				depth+1,
			); err != nil {
				return err
			}
		}
	case workflowAuthoringJSONTool, workflowAuthoringJSONMCPTool:
		if shape, present := object["parameter_shape"]; present {
			return validateWorkflowAuthoringJSONContext(
				shape,
				workflowAuthoringJSONShape,
				depth+1,
			)
		}
	case workflowAuthoringJSONShape:
		if properties, present := object["properties"]; present {
			if err := validateWorkflowAuthoringJSONArray(
				properties,
				workflowAuthoringJSONProperty,
				depth+1,
			); err != nil {
				return err
			}
		}
		if items, present := object["items"]; present {
			if err := validateWorkflowAuthoringJSONContext(
				items,
				workflowAuthoringJSONShape,
				depth+1,
			); err != nil {
				return err
			}
		}
		if additional, present := object["additional_properties"]; present {
			if err := validateWorkflowAuthoringJSONContext(
				additional,
				workflowAuthoringJSONAdditional,
				depth+1,
			); err != nil {
				return err
			}
		}
	case workflowAuthoringJSONProperty:
		return validateWorkflowAuthoringJSONContext(
			object["shape"],
			workflowAuthoringJSONShape,
			depth+1,
		)
	case workflowAuthoringJSONAdditional:
		if shape, present := object["shape"]; present {
			return validateWorkflowAuthoringJSONContext(
				shape,
				workflowAuthoringJSONShape,
				depth+1,
			)
		}
	}
	return nil
}

func validateWorkflowAuthoringJSONArray(
	value any,
	elementContext workflowAuthoringJSONContext,
	depth int,
) error {
	values, ok := value.([]any)
	if !ok {
		return errors.New("workflow authoring JSON array is required")
	}
	for _, value := range values {
		if err := validateWorkflowAuthoringJSONContext(
			value,
			elementContext,
			depth,
		); err != nil {
			return err
		}
	}
	return nil
}

func rejectWorkflowAuthoringNullValue(value any, enumElement bool) error {
	switch typed := value.(type) {
	case nil:
		if enumElement {
			return nil
		}
		return errors.New("workflow authoring JSON null is not allowed")
	case map[string]any:
		for name, child := range typed {
			if child == nil {
				return errors.New("workflow authoring JSON field cannot be null")
			}
			if err := rejectWorkflowAuthoringNullValue(child, name == "enum"); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := rejectWorkflowAuthoringNullValue(child, enumElement); err != nil {
				return err
			}
		}
	}
	return nil
}

func consumeUniqueWorkflowAuthoringJSONValue(
	decoder *json.Decoder,
	depth int,
) error {
	const maxWorkflowAuthoringJSONDepth = 32
	if depth > maxWorkflowAuthoringJSONDepth {
		return errors.New("workflow authoring JSON depth exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelimiter := token.(json.Delim)
	if !isDelimiter {
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
				return errors.New("workflow authoring JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate workflow authoring JSON object key")
			}
			seen[key] = struct{}{}
			if err := consumeUniqueWorkflowAuthoringJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("unterminated workflow authoring JSON object")
		}
	case '[':
		for decoder.More() {
			if err := consumeUniqueWorkflowAuthoringJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("unterminated workflow authoring JSON array")
		}
	default:
		return fmt.Errorf("unexpected workflow authoring JSON delimiter %q", delim)
	}
	return nil
}

// MarshalWorkflowAuthoringCapabilities encodes a catalog only when the exact
// response remains within the shared gateway/launcher boundary.
func MarshalWorkflowAuthoringCapabilities(
	catalog WorkflowAuthoringCapabilities,
) ([]byte, bool) {
	if err := ValidateWorkflowAuthoringCapabilities(catalog); err != nil {
		return nil, false
	}
	encoded, err := json.Marshal(catalog)
	if err != nil || int64(len(encoded)) > MaxWorkflowAuthoringResponseBytes ||
		!bytes.Equal(bytes.TrimSpace(encoded), encoded) {
		return nil, false
	}
	return encoded, true
}
