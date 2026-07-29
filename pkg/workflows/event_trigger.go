package workflows

import (
	"fmt"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

// UnmarshalYAML preserves explicitly empty event filter lists so validation can
// distinguish them from omitted filters. StringList's shared decoder remains
// unchanged for compatibility with the older workflow triggers.
func (t *EventTrigger) UnmarshalYAML(value *yaml.Node) error {
	value = dereferenceYAMLNode(value)
	if value == nil || value.Kind == 0 {
		return nil
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("event trigger must be a mapping")
	}
	if err := validateEventYAMLMappingKeys(
		value,
		map[string]struct{}{
			"sources": {}, "connectors": {}, "types": {},
			"actor": {}, "subject": {}, "attributes": {},
		},
		"event trigger",
	); err != nil {
		return err
	}

	var raw struct {
		Sources    eventPatternList            `yaml:"sources,omitempty"`
		Connectors eventPatternList            `yaml:"connectors,omitempty"`
		Types      eventPatternList            `yaml:"types,omitempty"`
		Actor      *EventEntityTrigger         `yaml:"actor,omitempty"`
		Subject    *EventEntityTrigger         `yaml:"subject,omitempty"`
		Attributes map[string]eventPatternList `yaml:"attributes,omitempty"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*t = EventTrigger{
		Sources:    StringList(raw.Sources),
		Connectors: StringList(raw.Connectors),
		Types:      StringList(raw.Types),
		Actor:      raw.Actor,
		Subject:    raw.Subject,
	}
	if raw.Attributes != nil {
		t.Attributes = make(map[string]StringList, len(raw.Attributes))
		for key, patterns := range raw.Attributes {
			t.Attributes[key] = StringList(patterns)
		}
	}
	return nil
}

// UnmarshalYAML gives nested actor and subject filters the same empty-list
// validation behavior as top-level event filters.
func (t *EventEntityTrigger) UnmarshalYAML(value *yaml.Node) error {
	value = dereferenceYAMLNode(value)
	if value == nil || value.Kind == 0 {
		return nil
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("event entity trigger must be a mapping")
	}
	if err := validateEventYAMLMappingKeys(
		value,
		map[string]struct{}{"ids": {}, "types": {}, "attributes": {}},
		"event entity trigger",
	); err != nil {
		return err
	}

	var raw struct {
		IDs        eventPatternList            `yaml:"ids,omitempty"`
		Types      eventPatternList            `yaml:"types,omitempty"`
		Attributes map[string]eventPatternList `yaml:"attributes,omitempty"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*t = EventEntityTrigger{
		IDs:   StringList(raw.IDs),
		Types: StringList(raw.Types),
	}
	if raw.Attributes != nil {
		t.Attributes = make(map[string]StringList, len(raw.Attributes))
		for key, patterns := range raw.Attributes {
			t.Attributes[key] = StringList(patterns)
		}
	}
	return nil
}

type eventPatternList StringList

func (patterns *eventPatternList) UnmarshalYAML(value *yaml.Node) error {
	decoded, err := decodeEventPatternList(value)
	if err != nil {
		return err
	}
	*patterns = eventPatternList(decoded)
	return nil
}

func dereferenceYAMLNode(value *yaml.Node) *yaml.Node {
	seen := make(map[*yaml.Node]struct{})
	for value != nil && value.Kind == yaml.AliasNode {
		if _, exists := seen[value]; exists {
			return nil
		}
		seen[value] = struct{}{}
		value = value.Alias
	}
	return value
}

type eventYAMLValidationKind uint8

const (
	eventYAMLValidationMapping eventYAMLValidationKind = iota
	eventYAMLValidationAttributes
	eventYAMLValidationTriggers
)

type eventYAMLValidationKey struct {
	node *yaml.Node
	kind eventYAMLValidationKind
}

type eventYAMLValidationState struct {
	active map[*yaml.Node]struct{}
	done   map[eventYAMLValidationKey]struct{}
}

func newEventYAMLValidationState() *eventYAMLValidationState {
	return &eventYAMLValidationState{
		active: make(map[*yaml.Node]struct{}),
		done:   make(map[eventYAMLValidationKey]struct{}),
	}
}

func (state *eventYAMLValidationState) enter(
	node *yaml.Node,
	kind eventYAMLValidationKind,
	label string,
) (bool, func(), error) {
	key := eventYAMLValidationKey{node: node, kind: kind}
	if _, complete := state.done[key]; complete {
		return true, func() {}, nil
	}
	if _, visiting := state.active[node]; visiting {
		return false, nil, fmt.Errorf("%s contains a cyclic YAML merge", label)
	}
	state.active[node] = struct{}{}
	return false, func() {
		delete(state.active, node)
		state.done[key] = struct{}{}
	}, nil
}

func validateEventYAMLMappingKeys(
	value *yaml.Node,
	allowed map[string]struct{},
	label string,
) error {
	return validateEventYAMLMappingKeysWithState(
		value,
		allowed,
		label,
		newEventYAMLValidationState(),
	)
}

func validateEventYAMLMappingKeysWithState(
	value *yaml.Node,
	allowed map[string]struct{},
	label string,
	state *eventYAMLValidationState,
) error {
	value = dereferenceYAMLNode(value)
	if value == nil || value.Kind != yaml.MappingNode {
		return fmt.Errorf("%s must be a mapping", label)
	}
	complete, leave, err := state.enter(value, eventYAMLValidationMapping, label)
	if err != nil {
		return err
	}
	if complete {
		return nil
	}
	defer leave()
	for index := 0; index+1 < len(value.Content); index += 2 {
		rawKeyNode := value.Content[index]
		keyNode := dereferenceYAMLNode(rawKeyNode)
		valueNode := value.Content[index+1]
		if keyNode == nil || keyNode.Kind != yaml.ScalarNode {
			return fmt.Errorf("%s field name must be a string", label)
		}
		key := keyNode.Value
		if eventYAMLNodeIsMergeKey(rawKeyNode) {
			if err := validateEventYAMLMergeKeys(valueNode, allowed, label, state); err != nil {
				return err
			}
			continue
		}
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("%s has unknown field %q", label, key)
		}
		if eventYAMLNodeIsNull(valueNode) {
			return fmt.Errorf("%s field %q cannot be null", label, key)
		}
		if key == "attributes" {
			if err := validateEventYAMLAttributeMap(valueNode, label, state); err != nil {
				return err
			}
		}
	}
	return nil
}

func eventYAMLNodeIsMergeKey(value *yaml.Node) bool {
	return value != nil &&
		value.Kind == yaml.ScalarNode &&
		value.Value == "<<" &&
		(value.Tag == "" || value.Tag == "!" || value.ShortTag() == "!!merge")
}

func validateEventYAMLAttributeMap(
	value *yaml.Node,
	label string,
	state *eventYAMLValidationState,
) error {
	value = dereferenceYAMLNode(value)
	if value == nil || value.Kind != yaml.MappingNode {
		// Shape errors remain owned by the typed decoder so its existing error
		// behavior stays intact.
		return nil
	}
	complete, leave, err := state.enter(value, eventYAMLValidationAttributes, label)
	if err != nil {
		return err
	}
	if complete {
		return nil
	}
	defer leave()
	for index := 0; index+1 < len(value.Content); index += 2 {
		rawKeyNode := value.Content[index]
		keyNode := dereferenceYAMLNode(rawKeyNode)
		valueNode := value.Content[index+1]
		if keyNode == nil || keyNode.Kind != yaml.ScalarNode {
			return fmt.Errorf("%s attribute name must be a string", label)
		}
		if eventYAMLNodeIsMergeKey(rawKeyNode) {
			if err := validateEventYAMLAttributeMerge(valueNode, label, state); err != nil {
				return err
			}
			continue
		}
		if eventYAMLNodeIsNull(keyNode) || keyNode.ShortTag() != "!!str" {
			return fmt.Errorf("%s attribute name must be a string", label)
		}
		if eventYAMLNodeIsNull(valueNode) {
			return fmt.Errorf(
				"%s attribute %q cannot be null",
				label,
				keyNode.Value,
			)
		}
	}
	return nil
}

func validateEventYAMLAttributeMerge(
	value *yaml.Node,
	label string,
	state *eventYAMLValidationState,
) error {
	value = dereferenceYAMLNode(value)
	if value == nil {
		return fmt.Errorf("%s attribute merge must reference a mapping", label)
	}
	switch value.Kind {
	case yaml.MappingNode:
		return validateEventYAMLAttributeMap(value, label, state)
	case yaml.SequenceNode:
		for _, item := range value.Content {
			if err := validateEventYAMLAttributeMap(item, label, state); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%s attribute merge must reference a mapping", label)
	}
}

func validateEventYAMLMergeKeys(
	value *yaml.Node,
	allowed map[string]struct{},
	label string,
	state *eventYAMLValidationState,
) error {
	value = dereferenceYAMLNode(value)
	if value == nil {
		return fmt.Errorf("%s merge must reference a mapping", label)
	}
	switch value.Kind {
	case yaml.MappingNode:
		return validateEventYAMLMappingKeysWithState(value, allowed, label, state)
	case yaml.SequenceNode:
		for _, item := range value.Content {
			if err := validateEventYAMLMappingKeysWithState(item, allowed, label, state); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%s merge must reference a mapping", label)
	}
}

func eventYAMLNodeIsNull(value *yaml.Node) bool {
	value = dereferenceYAMLNode(value)
	return value == nil || value.Tag == "!!null"
}

func decodeEventPatternList(value *yaml.Node) (StringList, error) {
	value = dereferenceYAMLNode(value)
	if value == nil || value.Kind == 0 {
		return StringList{""}, nil
	}
	switch value.Kind {
	case yaml.ScalarNode:
		var item string
		if err := value.Decode(&item); err != nil {
			return nil, err
		}
		return StringList{strings.TrimSpace(item)}, nil
	case yaml.SequenceNode:
		items := make(StringList, 0, len(value.Content))
		for _, node := range value.Content {
			node = dereferenceYAMLNode(node)
			if node == nil || node.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("expected string or string list")
			}
			var item string
			if err := node.Decode(&item); err != nil {
				return nil, err
			}
			items = append(items, strings.TrimSpace(item))
		}
		return items, nil
	default:
		return nil, fmt.Errorf("expected string or string list")
	}
}

// WorkflowMatchesEvent reports whether workflow's external event trigger
// matches event. Invalid and absent triggers never match.
func WorkflowMatchesEvent(workflow *Workflow, event eventing.Envelope) bool {
	if workflow == nil {
		return false
	}
	return MatchEventTrigger(workflow.On.Event, event)
}

// MatchEventTrigger applies an external event trigger without mutating either
// the trigger or event. Lists use OR semantics, while populated fields use AND
// semantics. Glob patterns are fully anchored and support only '*' and '?'.
func MatchEventTrigger(trigger *EventTrigger, event eventing.Envelope) bool {
	if trigger == nil || len(validateEventTrigger("on.event", trigger)) != 0 {
		return false
	}
	if !eventPatternListMatches(trigger.Sources, event.Source, true) ||
		!eventPatternListMatches(trigger.Connectors, event.Connector, true) ||
		!eventPatternListMatches(trigger.Types, event.Type, true) ||
		!eventAttributeFiltersMatch(trigger.Attributes, event.Attributes) {
		return false
	}
	if trigger.Actor != nil {
		if event.Actor == nil ||
			!eventEntityMatches(
				trigger.Actor,
				event.Actor.ID,
				event.Actor.Type,
				event.Actor.Attributes,
			) {
			return false
		}
	}
	if trigger.Subject != nil {
		if event.Subject == nil ||
			!eventEntityMatches(
				trigger.Subject,
				event.Subject.ID,
				event.Subject.Type,
				event.Subject.Attributes,
			) {
			return false
		}
	}
	return true
}

func eventEntityMatches(
	trigger *EventEntityTrigger,
	id string,
	entityType string,
	attributes map[string]string,
) bool {
	return eventPatternListMatches(trigger.IDs, id, false) &&
		eventPatternListMatches(trigger.Types, entityType, true) &&
		eventAttributeFiltersMatch(trigger.Attributes, attributes)
}

func eventAttributeFiltersMatch(filters map[string]StringList, attributes map[string]string) bool {
	for key, patterns := range filters {
		value, ok := attributes[key]
		if !ok || !eventPatternListMatches(patterns, value, false) {
			return false
		}
	}
	return true
}

func eventPatternListMatches(patterns StringList, value string, fold bool) bool {
	if patterns == nil {
		return true
	}
	for _, pattern := range patterns {
		if eventGlobMatch(strings.TrimSpace(pattern), value, fold) {
			return true
		}
	}
	return false
}

func eventGlobMatch(pattern string, value string, fold bool) bool {
	patternRunes := []rune(pattern)
	valueRunes := []rune(value)

	patternIndex := 0
	valueIndex := 0
	starIndex := -1
	starValueIndex := 0
	for valueIndex < len(valueRunes) {
		if patternIndex < len(patternRunes) &&
			(patternRunes[patternIndex] == '?' ||
				eventGlobRuneEqual(patternRunes[patternIndex], valueRunes[valueIndex], fold)) {
			patternIndex++
			valueIndex++
			continue
		}
		if patternIndex < len(patternRunes) && patternRunes[patternIndex] == '*' {
			starIndex = patternIndex
			patternIndex++
			starValueIndex = valueIndex
			continue
		}
		if starIndex >= 0 {
			patternIndex = starIndex + 1
			starValueIndex++
			valueIndex = starValueIndex
			continue
		}
		return false
	}
	for patternIndex < len(patternRunes) && patternRunes[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(patternRunes)
}

func eventGlobRuneEqual(left rune, right rune, fold bool) bool {
	if left == right {
		return true
	}
	if !fold {
		return false
	}
	for folded := unicode.SimpleFold(left); folded != left; folded = unicode.SimpleFold(folded) {
		if folded == right {
			return true
		}
	}
	return false
}

func validateEventTrigger(path string, trigger *EventTrigger) ValidationErrors {
	if trigger == nil {
		return nil
	}
	var errs ValidationErrors
	errs = append(errs, validateEventPatternList(path+".sources", trigger.Sources)...)
	errs = append(errs, validateEventPatternList(path+".connectors", trigger.Connectors)...)
	errs = append(errs, validateEventPatternList(path+".types", trigger.Types)...)
	errs = append(errs, validateEventEntityTrigger(path+".actor", trigger.Actor)...)
	errs = append(errs, validateEventEntityTrigger(path+".subject", trigger.Subject)...)
	errs = append(errs, validateEventAttributeFilters(path+".attributes", trigger.Attributes)...)
	if !eventTriggerHasEffectiveFilter(trigger) {
		errs = append(errs, ValidationError{Path: path, Message: "at least one filter is required"})
	}
	return errs
}

func validateEventEntityTrigger(path string, trigger *EventEntityTrigger) ValidationErrors {
	if trigger == nil {
		return nil
	}
	var errs ValidationErrors
	errs = append(errs, validateEventPatternList(path+".ids", trigger.IDs)...)
	errs = append(errs, validateEventPatternList(path+".types", trigger.Types)...)
	errs = append(errs, validateEventAttributeFilters(path+".attributes", trigger.Attributes)...)
	if !eventEntityTriggerHasEffectiveFilter(trigger) {
		errs = append(errs, ValidationError{Path: path, Message: "at least one entity filter is required"})
	}
	return errs
}

func validateEventPatternList(path string, patterns StringList) ValidationErrors {
	if patterns == nil {
		return nil
	}
	if len(patterns) == 0 {
		return ValidationErrors{{Path: path, Message: "at least one pattern is required"}}
	}
	var errs ValidationErrors
	for i, pattern := range patterns {
		if strings.TrimSpace(pattern) == "" {
			errs = append(
				errs,
				ValidationError{
					Path:    fmt.Sprintf("%s[%d]", path, i),
					Message: "pattern is required",
				},
			)
		}
	}
	return errs
}

func validateEventAttributeFilters(path string, filters map[string]StringList) ValidationErrors {
	if filters == nil {
		return nil
	}
	if len(filters) == 0 {
		return ValidationErrors{{Path: path, Message: "at least one attribute filter is required"}}
	}
	var errs ValidationErrors
	for key, patterns := range filters {
		keyPath := path + "." + key
		if strings.TrimSpace(key) == "" {
			keyPath = path
			errs = append(errs, ValidationError{Path: keyPath, Message: "attribute name is required"})
		}
		errs = append(errs, validateEventPatternList(keyPath, patterns)...)
	}
	return errs
}

func eventTriggerHasEffectiveFilter(trigger *EventTrigger) bool {
	return eventPatternListHasValue(trigger.Sources) ||
		eventPatternListHasValue(trigger.Connectors) ||
		eventPatternListHasValue(trigger.Types) ||
		eventEntityTriggerHasEffectiveFilter(trigger.Actor) ||
		eventEntityTriggerHasEffectiveFilter(trigger.Subject) ||
		eventAttributeFiltersHaveValue(trigger.Attributes)
}

func eventEntityTriggerHasEffectiveFilter(trigger *EventEntityTrigger) bool {
	return trigger != nil &&
		(eventPatternListHasValue(trigger.IDs) ||
			eventPatternListHasValue(trigger.Types) ||
			eventAttributeFiltersHaveValue(trigger.Attributes))
}

func eventPatternListHasValue(patterns StringList) bool {
	for _, pattern := range patterns {
		if strings.TrimSpace(pattern) != "" {
			return true
		}
	}
	return false
}

func eventAttributeFiltersHaveValue(filters map[string]StringList) bool {
	for key, patterns := range filters {
		if strings.TrimSpace(key) != "" && eventPatternListHasValue(patterns) {
			return true
		}
	}
	return false
}
