package workflows

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkflowTriggerKind identifies one direct entry in the workflow on mapping.
type WorkflowTriggerKind string

const (
	WorkflowTriggerManual         WorkflowTriggerKind = "manual"
	WorkflowTriggerSchedule       WorkflowTriggerKind = "schedule"
	WorkflowTriggerChannelMessage WorkflowTriggerKind = "channel_message"
	WorkflowTriggerCommand        WorkflowTriggerKind = "command"
	WorkflowTriggerRuntimeEvent   WorkflowTriggerKind = "runtime_event"
	WorkflowTriggerEvent          WorkflowTriggerKind = "event"
	WorkflowTriggerWorkflowCall   WorkflowTriggerKind = "workflow_call"
)

var workflowTriggerKinds = []WorkflowTriggerKind{
	WorkflowTriggerManual,
	WorkflowTriggerSchedule,
	WorkflowTriggerChannelMessage,
	WorkflowTriggerCommand,
	WorkflowTriggerRuntimeEvent,
	WorkflowTriggerEvent,
	WorkflowTriggerWorkflowCall,
}

// Valid reports whether kind is a trigger family supported by the workflow
// schema and structured editor.
func (kind WorkflowTriggerKind) Valid() bool {
	switch kind {
	case WorkflowTriggerManual,
		WorkflowTriggerSchedule,
		WorkflowTriggerChannelMessage,
		WorkflowTriggerCommand,
		WorkflowTriggerRuntimeEvent,
		WorkflowTriggerEvent,
		WorkflowTriggerWorkflowCall:
		return true
	default:
		return false
	}
}

// WorkflowTriggerProjection is one lossless structured-editor projection.
// Value is always emitted as JSON; an absent or unsafe trigger has a null value.
type WorkflowTriggerProjection struct {
	Present  bool   `json:"present"`
	Editable bool   `json:"editable"`
	Reason   string `json:"reason,omitempty"`
	Value    any    `json:"value"`

	jsonValue any
}

// MarshalJSON preserves explicitly present empty lists, maps, false values,
// and empty strings that the workflow structs intentionally tag omitempty.
// The Go-facing Value remains typed for callers and legacy adapters.
func (projection WorkflowTriggerProjection) MarshalJSON() ([]byte, error) {
	value := projection.Value
	if projection.jsonValue != nil {
		value = projection.jsonValue
	}
	return json.Marshal(struct {
		Present  bool   `json:"present"`
		Editable bool   `json:"editable"`
		Reason   string `json:"reason,omitempty"`
		Value    any    `json:"value"`
	}{
		Present:  projection.Present,
		Editable: projection.Editable,
		Reason:   projection.Reason,
		Value:    value,
	})
}

// WorkflowTriggersInspection projects every supported trigger family from one
// exact source snapshot.
type WorkflowTriggersInspection struct {
	Revision   string                                            `json:"revision"`
	Triggers   map[WorkflowTriggerKind]WorkflowTriggerProjection `json:"triggers"`
	Validation *WorkflowDevelopmentValidation                    `json:"validation"`
}

// InspectWorkflowTriggers uses both the YAML node tree and the workflow parser.
// AST checks run independently of semantic validation so invalid but losslessly
// represented trigger values remain editable. Shapes that the typed parser
// would normalize or discard remain available only through the raw editor.
func InspectWorkflowTriggers(raw string) WorkflowTriggersInspection {
	inspection := newWorkflowTriggersInspection(raw)
	document, err := decodeWorkflowEditorDocument(raw)
	if err != nil {
		reason := "Fix YAML syntax errors before using the structured trigger editor."
		if errors.Is(err, errWorkflowEditorMultipleDocuments) {
			reason = "Workflow YAML must contain exactly one document."
		}
		setAllWorkflowTriggerReasons(&inspection, reason)
		return inspection
	}
	root, reason := editableWorkflowRoot(document)
	if reason != "" {
		setAllWorkflowTriggerReasons(&inspection, reason)
		return inspection
	}
	nodes, triggerPathReason := workflowEditorTriggerNodes(root)
	for _, kind := range workflowTriggerKinds {
		node, present := nodes[kind]
		projection := inspection.Triggers[kind]
		projection.Present = present
		if present {
			projection.Reason = workflowTriggerNodeReason(kind, node)
		}
		inspection.Triggers[kind] = projection
	}

	for _, kind := range workflowTriggerKinds {
		projection := inspection.Triggers[kind]
		if projection.Reason != "" {
			continue
		}
		if projection.Present {
			value, decodeErr := decodeWorkflowTriggerNode(kind, nodes[kind])
			if decodeErr != nil {
				projection.Reason = "This trigger cannot be parsed by the structured editor."
				inspection.Triggers[kind] = projection
				continue
			}
			projection.Value = value
			projection.jsonValue = workflowTriggerJSONProjection(
				projection.Value,
				nodes[kind],
			)
			if reason := workflowTriggerTypedRoundTripReason(
				projection.Value,
				nodes[kind],
			); reason != "" {
				projection.Reason = reason
				inspection.Triggers[kind] = projection
				continue
			}
		}
		inspection.Triggers[kind] = projection
	}

	unsafeReason := unsafeWorkflowEditorNodeReason(root)
	globalReason := unsafeReason
	if globalReason == "" {
		globalReason = triggerPathReason
	}
	for _, kind := range workflowTriggerKinds {
		projection := inspection.Triggers[kind]
		if globalReason != "" {
			projection.Editable = false
			if unsafeReason != "" || projection.Reason == "" {
				projection.Reason = globalReason
			}
		} else if projection.Reason == "" {
			projection.Editable = true
		}
		inspection.Triggers[kind] = projection
	}
	return inspection
}

func workflowTriggerTypedRoundTripReason(value any, source *yaml.Node) string {
	encoded, err := workflowTriggerYAMLNode(value)
	if err != nil {
		return "This trigger cannot be safely encoded by the structured editor."
	}
	sourceProjection := workflowTriggerJSONProjection(value, source)
	encodedProjection := workflowTriggerJSONProjection(value, encoded)
	if !workflowTriggerValuesEqual(sourceProjection, encodedProjection) {
		return "Explicit trigger field presence would be normalized; use the raw YAML editor."
	}
	return ""
}

func newWorkflowTriggersInspection(raw string) WorkflowTriggersInspection {
	triggers := make(map[WorkflowTriggerKind]WorkflowTriggerProjection, len(workflowTriggerKinds))
	for _, kind := range workflowTriggerKinds {
		triggers[kind] = WorkflowTriggerProjection{}
	}
	return WorkflowTriggersInspection{
		Revision:   workflowEditorRevision(raw),
		Triggers:   triggers,
		Validation: validateDevelopmentYAML(raw),
	}
}

func setAllWorkflowTriggerReasons(
	inspection *WorkflowTriggersInspection,
	reason string,
) {
	for _, kind := range workflowTriggerKinds {
		projection := inspection.Triggers[kind]
		projection.Editable = false
		projection.Reason = reason
		projection.Value = nil
		inspection.Triggers[kind] = projection
	}
}

// RenderWorkflowTrigger replaces one direct on entry. A nil replacement
// removes that family. The revision must describe the exact raw source bytes.
func RenderWorkflowTrigger(
	raw string,
	revision string,
	kind WorkflowTriggerKind,
	replacement any,
) (string, WorkflowTriggersInspection, error) {
	inspection := InspectWorkflowTriggers(raw)
	if !kind.Valid() {
		return "", inspection, ErrWorkflowTriggerKind
	}
	if revision == "" || revision != inspection.Revision {
		return "", inspection, ErrWorkflowTriggerStaleRevision
	}
	current := inspection.Triggers[kind]
	if !current.Editable {
		return "", inspection, fmt.Errorf(
			"%w: %s",
			ErrWorkflowTriggerNotEditable,
			current.Reason,
		)
	}

	var replacementNode *yaml.Node
	if replacement != nil {
		var jobs map[string]Job
		if kind == WorkflowTriggerWorkflowCall {
			document, decodeErr := decodeWorkflowEditorDocument(raw)
			if decodeErr != nil {
				return "", inspection, decodeErr
			}
			root, rootReason := editableWorkflowRoot(document)
			if rootReason != "" {
				return "", inspection, fmt.Errorf("%w: source workflow cannot be parsed", ErrWorkflowTriggerValue)
			}
			jobs, decodeErr = decodeWorkflowEditorJobs(root)
			if decodeErr != nil {
				return "", inspection, fmt.Errorf(
					"%w: source workflow jobs cannot be parsed",
					ErrWorkflowTriggerValue,
				)
			}
		}
		if err := validateWorkflowTriggerReplacement(kind, replacement, jobs); err != nil {
			return "", inspection, err
		}
		encodedNode, encodeErr := workflowTriggerYAMLNode(replacement)
		if encodeErr != nil {
			return "", inspection, fmt.Errorf("%w: encode replacement", ErrWorkflowTriggerValue)
		}
		replacementNode = encodedNode
		if reason := workflowTriggerNodeReason(kind, replacementNode); reason != "" {
			return "", inspection, fmt.Errorf("%w: replacement is not safely projectable", ErrWorkflowTriggerValue)
		}
		if current.Present && workflowTriggerValuesEqual(current.Value, replacement) {
			return raw, inspection, nil
		}
	} else if !current.Present {
		return raw, inspection, nil
	}

	document, err := decodeWorkflowEditorDocument(raw)
	if err != nil {
		return "", inspection, err
	}
	root := document.Content[0]
	if err := replaceWorkflowTriggerNode(root, kind, replacementNode); err != nil {
		return "", inspection, err
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
	nextInspection := InspectWorkflowTriggers(next)
	nextProjection := nextInspection.Triggers[kind]
	if !nextProjection.Editable ||
		(replacement == nil && nextProjection.Present) ||
		(replacement != nil &&
			(!nextProjection.Present ||
				!workflowTriggerValuesEqual(nextProjection.Value, replacement))) {
		return "", inspection, fmt.Errorf(
			"%w: rendered trigger did not project back to the requested value",
			ErrWorkflowTriggerValue,
		)
	}
	return next, nextInspection, nil
}

func validateWorkflowTriggerReplacement(
	kind WorkflowTriggerKind,
	value any,
	jobs map[string]Job,
) error {
	var errs ValidationErrors
	switch kind {
	case WorkflowTriggerManual:
		trigger, ok := value.(map[string]any)
		if !ok || trigger == nil {
			return fmt.Errorf("%w: manual must be an object", ErrWorkflowTriggerValue)
		}
		if len(trigger) != 0 {
			errs = append(errs, ValidationError{
				Path: "on.manual", Message: "manual trigger must be an empty mapping",
			})
		}
	case WorkflowTriggerSchedule:
		trigger, ok := value.([]ScheduleTrigger)
		if !ok || trigger == nil {
			return fmt.Errorf("%w: schedule must be an array", ErrWorkflowTriggerValue)
		}
		errs = validateScheduleTriggers(trigger)
	case WorkflowTriggerChannelMessage:
		trigger, ok := value.(*ChannelMessageTrigger)
		if !ok || trigger == nil {
			return fmt.Errorf("%w: channel_message must be an object", ErrWorkflowTriggerValue)
		}
		errs = validateChannelTrigger("on.channel_message", trigger)
	case WorkflowTriggerCommand:
		trigger, ok := value.(*CommandTrigger)
		if !ok || trigger == nil {
			return fmt.Errorf("%w: command must be an object", ErrWorkflowTriggerValue)
		}
		errs = validateCommandTrigger("on.command", trigger)
	case WorkflowTriggerRuntimeEvent:
		trigger, ok := value.(*RuntimeEventTrigger)
		if !ok || trigger == nil {
			return fmt.Errorf("%w: runtime_event must be an object", ErrWorkflowTriggerValue)
		}
		errs = validateRuntimeEventTrigger("on.runtime_event", trigger)
	case WorkflowTriggerEvent:
		trigger, ok := value.(*EventTrigger)
		if !ok || trigger == nil {
			return fmt.Errorf("%w: event must be an object", ErrWorkflowTriggerValue)
		}
		errs = validateEventTrigger("on.event", trigger)
	case WorkflowTriggerWorkflowCall:
		trigger, ok := value.(*WorkflowCall)
		if !ok || trigger == nil {
			return fmt.Errorf("%w: workflow_call must be an object", ErrWorkflowTriggerValue)
		}
		errs = validateWorkflowCall(trigger, jobs)
	default:
		return ErrWorkflowTriggerKind
	}
	if len(errs) != 0 {
		return errs
	}
	return nil
}

func workflowTriggerValuesEqual(left, right any) bool {
	return workflowTriggerReflectEqual(
		reflect.ValueOf(left),
		reflect.ValueOf(right),
	)
}

func workflowTriggerReflectEqual(left, right reflect.Value) bool {
	for left.IsValid() && left.Kind() == reflect.Interface {
		if left.IsNil() {
			return right.IsValid() &&
				right.Kind() == reflect.Interface &&
				right.IsNil()
		}
		left = left.Elem()
	}
	for right.IsValid() && right.Kind() == reflect.Interface {
		if right.IsNil() {
			return left.IsValid() &&
				left.Kind() == reflect.Interface &&
				left.IsNil()
		}
		right = right.Elem()
	}
	if !left.IsValid() || !right.IsValid() {
		return !left.IsValid() && !right.IsValid()
	}
	if workflowTriggerNumericKind(left.Kind()) &&
		workflowTriggerNumericKind(right.Kind()) {
		leftNumber, leftOK := workflowTriggerNumber(left)
		rightNumber, rightOK := workflowTriggerNumber(right)
		return leftOK && rightOK && leftNumber.Cmp(rightNumber) == 0
	}
	if left.Kind() != right.Kind() {
		return false
	}

	switch left.Kind() {
	case reflect.Pointer:
		if left.IsNil() || right.IsNil() {
			return left.IsNil() && right.IsNil()
		}
		return workflowTriggerReflectEqual(left.Elem(), right.Elem())
	case reflect.Map:
		if left.IsNil() || right.IsNil() {
			return left.IsNil() && right.IsNil()
		}
		if left.Type() != right.Type() || left.Len() != right.Len() {
			return false
		}
		iter := left.MapRange()
		for iter.Next() {
			key := iter.Key()
			rightValue := right.MapIndex(key)
			if !rightValue.IsValid() ||
				!workflowTriggerReflectEqual(iter.Value(), rightValue) {
				return false
			}
		}
		return true
	case reflect.Slice:
		if left.IsNil() || right.IsNil() {
			return left.IsNil() && right.IsNil()
		}
		if left.Len() != right.Len() {
			return false
		}
		for index := 0; index < left.Len(); index++ {
			if !workflowTriggerReflectEqual(left.Index(index), right.Index(index)) {
				return false
			}
		}
		return true
	case reflect.Array:
		if left.Len() != right.Len() {
			return false
		}
		for index := 0; index < left.Len(); index++ {
			if !workflowTriggerReflectEqual(left.Index(index), right.Index(index)) {
				return false
			}
		}
		return true
	case reflect.Struct:
		if left.Type() != right.Type() || left.NumField() != right.NumField() {
			return false
		}
		for index := 0; index < left.NumField(); index++ {
			if !workflowTriggerReflectEqual(left.Field(index), right.Field(index)) {
				return false
			}
		}
		return true
	default:
		if left.Type() != right.Type() ||
			!left.CanInterface() ||
			!right.CanInterface() {
			return false
		}
		return reflect.DeepEqual(left.Interface(), right.Interface())
	}
}

func workflowTriggerNumericKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

func workflowTriggerNumber(value reflect.Value) (*big.Rat, bool) {
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return new(big.Rat).SetInt64(value.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		integer := new(big.Int).SetUint64(value.Uint())
		return new(big.Rat).SetInt(integer), true
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, false
		}
		return new(big.Rat).SetFloat64(number), true
	default:
		return nil, false
	}
}

func workflowTriggerYAMLNode(value any) (*yaml.Node, error) {
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return nil, err
	}
	return &node, nil
}

func decodeWorkflowTriggerNode(
	kind WorkflowTriggerKind,
	node *yaml.Node,
) (any, error) {
	switch kind {
	case WorkflowTriggerManual:
		var trigger map[string]any
		if err := node.Decode(&trigger); err != nil {
			return nil, err
		}
		return trigger, nil
	case WorkflowTriggerSchedule:
		var trigger []ScheduleTrigger
		if err := node.Decode(&trigger); err != nil {
			return nil, err
		}
		return trigger, nil
	case WorkflowTriggerChannelMessage:
		var trigger ChannelMessageTrigger
		if err := node.Decode(&trigger); err != nil {
			return nil, err
		}
		return &trigger, nil
	case WorkflowTriggerCommand:
		var trigger CommandTrigger
		if err := node.Decode(&trigger); err != nil {
			return nil, err
		}
		return &trigger, nil
	case WorkflowTriggerRuntimeEvent:
		var trigger RuntimeEventTrigger
		if err := node.Decode(&trigger); err != nil {
			return nil, err
		}
		return &trigger, nil
	case WorkflowTriggerEvent:
		var trigger EventTrigger
		if err := node.Decode(&trigger); err != nil {
			return nil, err
		}
		return &trigger, nil
	case WorkflowTriggerWorkflowCall:
		var trigger WorkflowCall
		if err := node.Decode(&trigger); err != nil {
			return nil, err
		}
		return &trigger, nil
	default:
		return nil, ErrWorkflowTriggerKind
	}
}

func decodeWorkflowEditorJobs(root *yaml.Node) (map[string]Job, error) {
	var jobsNode *yaml.Node
	jobsMappings := 0
	if root != nil && root.Kind == yaml.MappingNode {
		for index := 0; index+1 < len(root.Content); index += 2 {
			key := root.Content[index]
			if key != nil &&
				key.Kind == yaml.ScalarNode &&
				strings.TrimSpace(key.Value) == "jobs" {
				jobsMappings++
				jobsNode = root.Content[index+1]
			}
		}
	}
	if jobsMappings > 1 {
		return nil, errors.New("duplicate jobs mappings require the raw editor")
	}
	if jobsNode == nil || eventYAMLNodeIsNull(jobsNode) {
		return nil, nil
	}
	var jobs map[string]Job
	if err := jobsNode.Decode(&jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

func replaceWorkflowTriggerNode(
	root *yaml.Node,
	kind WorkflowTriggerKind,
	replacement *yaml.Node,
) error {
	onIndexes := workflowRootOnPairIndexes(root)
	if len(onIndexes) == 0 {
		if replacement == nil {
			return nil
		}
		onNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(
			root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "on"},
			onNode,
		)
		onNode.Content = append(
			onNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: string(kind)},
			replacement,
		)
		return nil
	}

	onNode := root.Content[onIndexes[0]+1]
	indexes := workflowMappingPairIndexes(onNode, string(kind))
	if len(indexes) == 0 {
		if replacement == nil {
			return nil
		}
		onNode.Content = append(
			onNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: string(kind)},
			replacement,
		)
		return nil
	}
	index := indexes[0]
	if replacement == nil {
		onNode.Content = append(onNode.Content[:index], onNode.Content[index+2:]...)
		return nil
	}
	previous := onNode.Content[index+1]
	replacement.HeadComment = previous.HeadComment
	replacement.LineComment = previous.LineComment
	replacement.FootComment = previous.FootComment
	onNode.Content[index+1] = replacement
	return nil
}

func workflowEditorTriggerNodes(
	root *yaml.Node,
) (map[WorkflowTriggerKind]*yaml.Node, string) {
	nodes := make(map[WorkflowTriggerKind]*yaml.Node, len(workflowTriggerKinds))
	onIndexes := workflowRootOnPairIndexes(root)
	if len(onIndexes) == 0 {
		return nodes, ""
	}
	reason := ""
	setReason := func(candidate string) {
		if reason == "" {
			reason = candidate
		}
	}
	if len(onIndexes) > 1 {
		setReason("Duplicate on mappings require the raw editor.")
	}
	for _, onIndex := range onIndexes {
		onNode := root.Content[onIndex+1]
		if !workflowSafeCollectionNode(onNode, yaml.MappingNode, "!!map") {
			setReason("The on value must be a plain mapping before using the structured editor.")
			continue
		}
		if len(onNode.Content)%2 != 0 {
			setReason("The on mapping is malformed and requires the raw editor.")
		}
		for index := 0; index+1 < len(onNode.Content); index += 2 {
			key := onNode.Content[index]
			if !workflowSafeStringScalar(key) || strings.ContainsAny(key.Value, "\r\n") {
				setReason("Trigger names must be plain single-line strings.")
				continue
			}
			kind := WorkflowTriggerKind(key.Value)
			if !kind.Valid() {
				setReason("Unknown workflow trigger names require the raw editor.")
				continue
			}
			if _, exists := nodes[kind]; exists {
				setReason("Duplicate workflow trigger mappings require the raw editor.")
				continue
			}
			nodes[kind] = onNode.Content[index+1]
		}
	}
	return nodes, reason
}

func workflowTriggerNodeReason(kind WorkflowTriggerKind, node *yaml.Node) string {
	if node == nil || eventYAMLNodeIsNull(node) {
		return "Explicit null triggers require the raw editor."
	}
	switch kind {
	case WorkflowTriggerManual:
		if !workflowSafeCollectionNode(node, yaml.MappingNode, "!!map") {
			return "The manual trigger must be a plain mapping."
		}
		if len(node.Content) != 0 {
			return "Non-empty manual trigger settings require the raw editor."
		}
		return ""
	case WorkflowTriggerSchedule:
		if !workflowSafeCollectionNode(node, yaml.SequenceNode, "!!seq") {
			return "The schedule trigger must be a plain sequence."
		}
		for _, item := range node.Content {
			if reason := workflowKnownMappingReason(
				item,
				map[string]workflowNodeCheck{
					"cron": workflowStringNodeCheck(false),
				},
			); reason != "" {
				return "Schedule entries are not safely projectable: " + reason
			}
		}
		return ""
	case WorkflowTriggerChannelMessage:
		return workflowKnownMappingReason(node, map[string]workflowNodeCheck{
			"channels":     workflowStringListNodeReason,
			"chats":        workflowStringListNodeReason,
			"senders":      workflowStringListNodeReason,
			"mentioned":    workflowBoolNodeReason,
			"command":      workflowStringNodeCheck(false),
			"text_matches": workflowStringNodeCheck(false),
			"passthrough":  workflowBoolNodeReason,
			"conversation": workflowConversationNodeReason,
		})
	case WorkflowTriggerCommand:
		return workflowKnownMappingReason(node, map[string]workflowNodeCheck{
			"name":         workflowStringNodeCheck(false),
			"channels":     workflowStringListNodeReason,
			"chats":        workflowStringListNodeReason,
			"senders":      workflowStringListNodeReason,
			"args":         workflowInputMapNodeReason,
			"passthrough":  workflowBoolNodeReason,
			"conversation": workflowConversationNodeReason,
		})
	case WorkflowTriggerRuntimeEvent:
		return workflowKnownMappingReason(node, map[string]workflowNodeCheck{
			"kinds":    workflowStringListNodeReason,
			"sources":  workflowStringListNodeReason,
			"agents":   workflowStringListNodeReason,
			"sessions": workflowStringListNodeReason,
			"channels": workflowStringListNodeReason,
			"chats":    workflowStringListNodeReason,
		})
	case WorkflowTriggerEvent:
		return workflowKnownMappingReason(node, map[string]workflowNodeCheck{
			"sources":    workflowStringListNodeReason,
			"connectors": workflowStringListNodeReason,
			"types":      workflowStringListNodeReason,
			"actor":      workflowEventEntityNodeReason,
			"subject":    workflowEventEntityNodeReason,
			"attributes": workflowStringListMapNodeReason,
		})
	case WorkflowTriggerWorkflowCall:
		return workflowKnownMappingReason(node, map[string]workflowNodeCheck{
			"inputs":  workflowInputMapNodeReason,
			"secrets": workflowSecretMapNodeReason,
			"outputs": workflowOutputMapNodeReason,
		})
	default:
		return "Unknown workflow trigger names require the raw editor."
	}
}

type workflowNodeCheck func(*yaml.Node) string

func workflowKnownMappingReason(
	node *yaml.Node,
	fields map[string]workflowNodeCheck,
) string {
	return workflowMappingReason(node, fields, nil)
}

func workflowDynamicMappingReason(
	node *yaml.Node,
	valueCheck workflowNodeCheck,
) string {
	return workflowMappingReason(node, nil, valueCheck)
}

func workflowMappingReason(
	node *yaml.Node,
	fields map[string]workflowNodeCheck,
	dynamic workflowNodeCheck,
) string {
	if !workflowSafeCollectionNode(node, yaml.MappingNode, "!!map") {
		return "A plain mapping is required."
	}
	if len(node.Content)%2 != 0 {
		return "Malformed mappings require the raw editor."
	}
	seen := make(map[string]struct{}, len(node.Content)/2)
	for index := 0; index+1 < len(node.Content); index += 2 {
		key := node.Content[index]
		value := node.Content[index+1]
		if !workflowSafeStringScalar(key) || strings.ContainsAny(key.Value, "\r\n") {
			return "Mapping keys containing line breaks or unsafe tags require the raw editor."
		}
		if _, exists := seen[key.Value]; exists {
			return "Duplicate mapping fields require the raw editor."
		}
		seen[key.Value] = struct{}{}
		if eventYAMLNodeIsNull(value) {
			return "Explicit null fields require the raw editor."
		}
		check := dynamic
		if fields != nil {
			var ok bool
			check, ok = fields[key.Value]
			if !ok {
				return "Unknown fields require the raw editor."
			}
		}
		if check != nil {
			if reason := check(value); reason != "" {
				return reason
			}
		}
	}
	return ""
}

func workflowSafeCollectionNode(node *yaml.Node, kind yaml.Kind, tag string) bool {
	return node != nil &&
		node.Kind == kind &&
		(node.Tag == "" || node.ShortTag() == tag)
}

func workflowSafeStringScalar(node *yaml.Node) bool {
	return node != nil &&
		node.Kind == yaml.ScalarNode &&
		!eventYAMLNodeIsNull(node) &&
		(node.Tag == "" || node.ShortTag() == "!!str")
}

func workflowStringNodeCheck(requireStableTrim bool) workflowNodeCheck {
	return func(node *yaml.Node) string {
		if !workflowSafeStringScalar(node) {
			return "String values must use plain string tags."
		}
		if strings.ContainsAny(node.Value, "\r\n") {
			return "Projected values containing line breaks require the raw editor."
		}
		if requireStableTrim &&
			(strings.TrimSpace(node.Value) != node.Value || node.Value == "") {
			return "Whitespace-normalized list values require the raw editor."
		}
		return ""
	}
}

func workflowStringListNodeReason(node *yaml.Node) string {
	check := workflowStringNodeCheck(true)
	switch {
	case workflowSafeStringScalar(node):
		return check(node)
	case workflowSafeCollectionNode(node, yaml.SequenceNode, "!!seq"):
		for _, item := range node.Content {
			if reason := check(item); reason != "" {
				return reason
			}
		}
		return ""
	default:
		return "String-list filters must be strings or plain sequences."
	}
}

func workflowBoolNodeReason(node *yaml.Node) string {
	if node == nil ||
		node.Kind != yaml.ScalarNode ||
		node.ShortTag() != "!!bool" ||
		(node.Value != "true" && node.Value != "false") {
		return "Boolean values must use true or false."
	}
	return ""
}

func workflowConversationNodeReason(node *yaml.Node) string {
	return workflowKnownMappingReason(node, map[string]workflowNodeCheck{
		"session":  workflowStringNodeCheck(false),
		"delivery": workflowStringNodeCheck(false),
	})
}

func workflowInputMapNodeReason(node *yaml.Node) string {
	return workflowDynamicMappingReason(node, func(input *yaml.Node) string {
		return workflowKnownMappingReason(input, map[string]workflowNodeCheck{
			"type":     workflowStringNodeCheck(false),
			"required": workflowBoolNodeReason,
			"default":  workflowJSONDefaultNodeReason,
		})
	})
}

func workflowSecretMapNodeReason(node *yaml.Node) string {
	return workflowDynamicMappingReason(node, func(secret *yaml.Node) string {
		return workflowKnownMappingReason(secret, map[string]workflowNodeCheck{
			"required": workflowBoolNodeReason,
		})
	})
}

func workflowOutputMapNodeReason(node *yaml.Node) string {
	return workflowDynamicMappingReason(node, func(output *yaml.Node) string {
		return workflowKnownMappingReason(output, map[string]workflowNodeCheck{
			"value": workflowStringNodeCheck(false),
		})
	})
}

func workflowEventEntityNodeReason(node *yaml.Node) string {
	return workflowKnownMappingReason(node, map[string]workflowNodeCheck{
		"ids":        workflowStringListNodeReason,
		"types":      workflowStringListNodeReason,
		"attributes": workflowStringListMapNodeReason,
	})
}

func workflowStringListMapNodeReason(node *yaml.Node) string {
	return workflowDynamicMappingReason(node, workflowStringListNodeReason)
}

var workflowJSONNumberPattern = regexp.MustCompile(
	`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`,
)

const workflowJSONMaximumSafeInteger int64 = 1<<53 - 1

func workflowJSONDefaultNodeReason(node *yaml.Node) string {
	if eventYAMLNodeIsNull(node) {
		return "A top-level null default requires the raw editor."
	}
	return workflowJSONValueNodeReason(node, false)
}

func workflowJSONValueNodeReason(node *yaml.Node, nested bool) string {
	if node == nil {
		return "Unsafe default values require the raw editor."
	}
	if eventYAMLNodeIsNull(node) {
		if nested {
			return ""
		}
		return "A top-level null default requires the raw editor."
	}
	switch node.Kind {
	case yaml.ScalarNode:
		switch node.ShortTag() {
		case "!!str":
			if strings.ContainsAny(node.Value, "\r\n") {
				return "Projected values containing line breaks require the raw editor."
			}
			return ""
		case "!!bool":
			if node.Value == "true" || node.Value == "false" {
				return ""
			}
		case "!!int", "!!float":
			if WorkflowJSONNumberIsBrowserSafe(node.Value) {
				return ""
			}
		}
		return "Defaults must use JSON-safe scalar values."
	case yaml.SequenceNode:
		if !workflowSafeCollectionNode(node, yaml.SequenceNode, "!!seq") {
			return "Defaults must use plain JSON arrays."
		}
		for _, item := range node.Content {
			if reason := workflowJSONValueNodeReason(item, true); reason != "" {
				return reason
			}
		}
		return ""
	case yaml.MappingNode:
		if !workflowSafeCollectionNode(node, yaml.MappingNode, "!!map") ||
			len(node.Content)%2 != 0 {
			return "Defaults must use plain JSON objects."
		}
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index+1 < len(node.Content); index += 2 {
			key := node.Content[index]
			if !workflowSafeStringScalar(key) || strings.ContainsAny(key.Value, "\r\n") {
				return "Default object keys must be single-line strings."
			}
			if _, exists := seen[key.Value]; exists {
				return "Duplicate default object keys require the raw editor."
			}
			seen[key.Value] = struct{}{}
			if reason := workflowJSONValueNodeReason(node.Content[index+1], true); reason != "" {
				return reason
			}
		}
		return ""
	default:
		return "Defaults must contain only JSON-compatible values."
	}
}

// WorkflowJSONNumberIsBrowserSafe reports whether a JSON-number spelling
// survives conversion through a browser float without changing its exact
// decimal value. Exact integers must also remain within JavaScript's safe
// integer range even when written with a decimal point or exponent.
func WorkflowJSONNumberIsBrowserSafe(text string) bool {
	if !workflowJSONNumberPattern.MatchString(text) {
		return false
	}
	exact, ok := new(big.Rat).SetString(text)
	if !ok {
		return false
	}
	if exact.IsInt() {
		limit := big.NewInt(workflowJSONMaximumSafeInteger)
		absolute := new(big.Int).Abs(new(big.Int).Set(exact.Num()))
		if absolute.Cmp(limit) > 0 {
			return false
		}
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
		return false
	}
	roundTrip, ok := new(big.Rat).SetString(
		strconv.FormatFloat(number, 'g', -1, 64),
	)
	return ok && exact.Cmp(roundTrip) == 0
}

func workflowTriggerJSONProjection(value any, node *yaml.Node) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var base any
	if err := decoder.Decode(&base); err != nil {
		return value
	}
	projected, ok := mergeWorkflowTriggerJSONProjection(base, true, node)
	if !ok {
		return base
	}
	return projected
}

func mergeWorkflowTriggerJSONProjection(
	base any,
	hasBase bool,
	node *yaml.Node,
) (any, bool) {
	if node == nil {
		return nil, false
	}
	if eventYAMLNodeIsNull(node) {
		return nil, true
	}
	switch node.Kind {
	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return nil, false
		}
		projected := make(map[string]any, len(node.Content)/2)
		if baseMap, ok := base.(map[string]any); ok {
			for key, value := range baseMap {
				projected[key] = value
			}
		}
		for index := 0; index+1 < len(node.Content); index += 2 {
			key := node.Content[index].Value
			childBase, childHasBase := projected[key]
			child, ok := mergeWorkflowTriggerJSONProjection(
				childBase,
				childHasBase,
				node.Content[index+1],
			)
			if !ok {
				return nil, false
			}
			projected[key] = child
		}
		return projected, true
	case yaml.SequenceNode:
		baseItems, _ := base.([]any)
		projected := make([]any, len(node.Content))
		for index, childNode := range node.Content {
			var childBase any
			childHasBase := index < len(baseItems)
			if childHasBase {
				childBase = baseItems[index]
			}
			child, ok := mergeWorkflowTriggerJSONProjection(
				childBase,
				childHasBase,
				childNode,
			)
			if !ok {
				return nil, false
			}
			projected[index] = child
		}
		return projected, true
	case yaml.ScalarNode:
		if hasBase {
			return base, true
		}
		switch node.ShortTag() {
		case "!!str":
			return node.Value, true
		case "!!bool":
			return node.Value == "true", true
		case "!!int":
			if number, err := strconv.ParseInt(node.Value, 10, 64); err == nil {
				return number, true
			}
			number, err := strconv.ParseUint(node.Value, 10, 64)
			return number, err == nil
		case "!!float":
			number, err := strconv.ParseFloat(node.Value, 64)
			return number, err == nil && !math.IsInf(number, 0) && !math.IsNaN(number)
		default:
			return nil, false
		}
	default:
		return nil, false
	}
}

func cloneWorkflowEditorValue(value any) any {
	if value == nil {
		return nil
	}
	return cloneWorkflowEditorReflect(reflect.ValueOf(value)).Interface()
}

func cloneWorkflowEditorReflect(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.New(value.Type()).Elem()
		out.Set(cloneWorkflowEditorReflect(value.Elem()))
		return out
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.New(value.Type().Elem())
		out.Elem().Set(cloneWorkflowEditorReflect(value.Elem()))
		return out
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			out.SetMapIndex(
				cloneWorkflowEditorReflect(iter.Key()),
				cloneWorkflowEditorReflect(iter.Value()),
			)
		}
		return out
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			out.Index(index).Set(cloneWorkflowEditorReflect(value.Index(index)))
		}
		return out
	case reflect.Array:
		out := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			out.Index(index).Set(cloneWorkflowEditorReflect(value.Index(index)))
		}
		return out
	case reflect.Struct:
		out := reflect.New(value.Type()).Elem()
		out.Set(value)
		for index := 0; index < value.NumField(); index++ {
			if out.Field(index).CanSet() && value.Field(index).CanInterface() {
				out.Field(index).Set(cloneWorkflowEditorReflect(value.Field(index)))
			}
		}
		return out
	default:
		return value
	}
}
