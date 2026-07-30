package workflows

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	// ErrWorkflowTriggerStaleRevision prevents a structured edit from
	// overwriting YAML that changed after it was inspected.
	ErrWorkflowTriggerStaleRevision = errors.New("workflow YAML revision is stale")
	// ErrWorkflowTriggerNotEditable reports YAML whose aliases, merges, tags,
	// or shape cannot be changed losslessly by the structured editor.
	ErrWorkflowTriggerNotEditable = errors.New("workflow trigger is not safely editable")
	// ErrWorkflowTriggerKind reports a trigger family outside the supported
	// workflow schema.
	ErrWorkflowTriggerKind = errors.New("unsupported workflow trigger type")
	// ErrWorkflowTriggerValue reports a replacement whose Go or projected YAML
	// shape is not safe for the structured editor.
	ErrWorkflowTriggerValue = errors.New("invalid workflow trigger value")

	// ErrWorkflowEventTriggerStaleRevision prevents a structured edit from
	// overwriting YAML that changed after it was inspected.
	ErrWorkflowEventTriggerStaleRevision = ErrWorkflowTriggerStaleRevision
	// ErrWorkflowEventTriggerNotEditable reports YAML whose aliases, merges, or
	// shape cannot be changed safely by the narrow structured editor.
	ErrWorkflowEventTriggerNotEditable = ErrWorkflowTriggerNotEditable
)

// WorkflowEventTriggerInspection is the authoritative parsed projection used
// by the structured event-trigger editor.
type WorkflowEventTriggerInspection struct {
	Revision     string                         `json:"revision"`
	Editable     bool                           `json:"editable"`
	Reason       string                         `json:"reason,omitempty"`
	EventTrigger *EventTrigger                  `json:"event_trigger"`
	Validation   *WorkflowDevelopmentValidation `json:"validation"`
}

// InspectWorkflowEventTrigger parses raw without changing it. Invalid workflow
// YAML is still returned as structured validation feedback; unsafe AST shapes
// remain available through the raw editor but are marked non-editable.
func InspectWorkflowEventTrigger(raw string) WorkflowEventTriggerInspection {
	all := InspectWorkflowTriggers(raw)
	event := all.Triggers[WorkflowTriggerEvent]
	inspection := WorkflowEventTriggerInspection{
		Revision:   all.Revision,
		Editable:   event.Editable,
		Reason:     event.Reason,
		Validation: all.Validation,
	}
	if trigger, ok := event.Value.(*EventTrigger); ok {
		inspection.EventTrigger = trigger
	}
	return inspection
}

// RenderWorkflowEventTrigger replaces only the direct on.event AST entry.
// trigger == nil removes the event trigger. revision must come from inspecting
// the exact raw bytes supplied in raw.
func RenderWorkflowEventTrigger(
	raw string,
	revision string,
	trigger *EventTrigger,
) (string, WorkflowEventTriggerInspection, error) {
	var replacement any
	if trigger != nil {
		replacement = trigger
	}
	rendered, all, err := RenderWorkflowTrigger(
		raw,
		revision,
		WorkflowTriggerEvent,
		replacement,
	)
	event := all.Triggers[WorkflowTriggerEvent]
	inspection := WorkflowEventTriggerInspection{
		Revision:   all.Revision,
		Editable:   event.Editable,
		Reason:     event.Reason,
		Validation: all.Validation,
	}
	if projected, ok := event.Value.(*EventTrigger); ok {
		inspection.EventTrigger = projected
	}
	return rendered, inspection, err
}

func workflowEditorRevision(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(sum[:])
}

var errWorkflowEditorMultipleDocuments = errors.New(
	"workflow YAML contains multiple documents",
)

func decodeWorkflowEditorDocument(raw string) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(strings.NewReader(raw))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return &document, nil
		}
		return nil, err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errWorkflowEditorMultipleDocuments
		}
		return nil, err
	}
	return &document, nil
}

func editableWorkflowRoot(document *yaml.Node) (*yaml.Node, string) {
	if document == nil ||
		document.Kind != yaml.DocumentNode ||
		len(document.Content) != 1 {
		return nil, "Workflow YAML must contain exactly one document."
	}
	root := document.Content[0]
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, "Workflow YAML must use a top-level mapping."
	}
	return root, ""
}

func unsafeWorkflowEditorNodeReason(node *yaml.Node) string {
	if node == nil {
		return ""
	}
	if node.Kind == yaml.AliasNode {
		return "YAML aliases require the raw editor."
	}
	if node.Kind == yaml.MappingNode {
		for index := 0; index+1 < len(node.Content); index += 2 {
			if eventYAMLNodeIsMergeKey(node.Content[index]) {
				return "YAML merge keys require the raw editor."
			}
		}
	}
	for _, child := range node.Content {
		if reason := unsafeWorkflowEditorNodeReason(child); reason != "" {
			return reason
		}
	}
	return ""
}

func validateWorkflowEditorTriggerPath(root *yaml.Node) string {
	onIndexes := workflowRootOnPairIndexes(root)
	if len(onIndexes) > 1 {
		return "Duplicate on mappings require the raw editor."
	}
	if len(onIndexes) == 0 {
		return ""
	}
	onNode := root.Content[onIndexes[0]+1]
	if onNode == nil || onNode.Kind != yaml.MappingNode {
		return "The on value must be a mapping before using the structured editor."
	}
	eventIndexes := workflowMappingPairIndexes(onNode, "event")
	if len(eventIndexes) > 1 {
		return "Duplicate on.event mappings require the raw editor."
	}
	return ""
}

func workflowMappingPairIndexes(mapping *yaml.Node, names ...string) []int {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	var indexes []int
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		key := mapping.Content[index]
		if key == nil || key.Kind != yaml.ScalarNode {
			continue
		}
		if _, ok := allowed[key.Value]; ok {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func workflowRootOnPairIndexes(root *yaml.Node) []int {
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	var indexes []int
	for index := 0; index+1 < len(root.Content); index += 2 {
		key := root.Content[index]
		if key == nil ||
			key.Kind != yaml.ScalarNode ||
			key.Value != "on" ||
			(key.Tag != "" && key.ShortTag() != "!!str") {
			continue
		}
		indexes = append(indexes, index)
	}
	return indexes
}

func replaceWorkflowEventTriggerNode(root *yaml.Node, trigger *EventTrigger) (bool, error) {
	onIndexes := workflowRootOnPairIndexes(root)
	if len(onIndexes) == 0 {
		if trigger == nil {
			return false, nil
		}
		onNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(
			root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "on"},
			onNode,
		)
		return addWorkflowEventTriggerNode(onNode, trigger)
	}

	onNode := root.Content[onIndexes[0]+1]
	eventIndexes := workflowMappingPairIndexes(onNode, "event")
	if len(eventIndexes) == 0 {
		if trigger == nil {
			return false, nil
		}
		return addWorkflowEventTriggerNode(onNode, trigger)
	}
	eventIndex := eventIndexes[0]
	if trigger == nil {
		onNode.Content = append(
			onNode.Content[:eventIndex],
			onNode.Content[eventIndex+2:]...,
		)
		return true, nil
	}

	replacement, err := workflowEventTriggerYAMLNode(trigger)
	if err != nil {
		return false, err
	}
	previous := onNode.Content[eventIndex+1]
	replacement.HeadComment = previous.HeadComment
	replacement.LineComment = previous.LineComment
	replacement.FootComment = previous.FootComment
	onNode.Content[eventIndex+1] = replacement
	return true, nil
}

func addWorkflowEventTriggerNode(onNode *yaml.Node, trigger *EventTrigger) (bool, error) {
	value, err := workflowEventTriggerYAMLNode(trigger)
	if err != nil {
		return false, err
	}
	onNode.Content = append(
		onNode.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "event"},
		value,
	)
	return true, nil
}

func workflowEventTriggerYAMLNode(trigger *EventTrigger) (*yaml.Node, error) {
	var node yaml.Node
	if err := node.Encode(trigger); err != nil {
		return nil, err
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("encoded event trigger must be a mapping")
	}
	return &node, nil
}

func cloneEventTrigger(trigger *EventTrigger) *EventTrigger {
	if trigger == nil {
		return nil
	}
	out := &EventTrigger{
		Sources:    cloneEventPatternList(trigger.Sources),
		Connectors: cloneEventPatternList(trigger.Connectors),
		Types:      cloneEventPatternList(trigger.Types),
		Actor:      cloneEventEntityTrigger(trigger.Actor),
		Subject:    cloneEventEntityTrigger(trigger.Subject),
		Attributes: cloneEventTriggerAttributes(trigger.Attributes),
	}
	return out
}

func cloneEventEntityTrigger(trigger *EventEntityTrigger) *EventEntityTrigger {
	if trigger == nil {
		return nil
	}
	return &EventEntityTrigger{
		IDs:        cloneEventPatternList(trigger.IDs),
		Types:      cloneEventPatternList(trigger.Types),
		Attributes: cloneEventTriggerAttributes(trigger.Attributes),
	}
}

func cloneEventTriggerAttributes(
	attributes map[string]StringList,
) map[string]StringList {
	if attributes == nil {
		return nil
	}
	out := make(map[string]StringList, len(attributes))
	for key, patterns := range attributes {
		out[key] = cloneEventPatternList(patterns)
	}
	return out
}

func cloneEventPatternList(patterns StringList) StringList {
	if patterns == nil {
		return nil
	}
	return append(StringList{}, patterns...)
}

func workflowEventTriggerProjectionReason(trigger *EventTrigger) string {
	if trigger == nil {
		return ""
	}
	if eventPatternListsContainLineBreak(
		trigger.Sources,
		trigger.Connectors,
		trigger.Types,
	) ||
		eventAttributesContainLineBreak(trigger.Attributes) {
		return "Event filters containing line breaks require the raw editor."
	}
	for _, entity := range []*EventEntityTrigger{trigger.Actor, trigger.Subject} {
		if entity == nil {
			continue
		}
		if eventPatternListsContainLineBreak(entity.IDs, entity.Types) ||
			eventAttributesContainLineBreak(entity.Attributes) {
			return "Event filters containing line breaks require the raw editor."
		}
	}
	return ""
}

func eventPatternListsContainLineBreak(lists ...StringList) bool {
	for _, patterns := range lists {
		for _, pattern := range patterns {
			if strings.ContainsAny(pattern, "\r\n") {
				return true
			}
		}
	}
	return false
}

func eventAttributesContainLineBreak(attributes map[string]StringList) bool {
	for key, patterns := range attributes {
		if strings.ContainsAny(key, "\r\n") ||
			eventPatternListsContainLineBreak(patterns) {
			return true
		}
	}
	return false
}
