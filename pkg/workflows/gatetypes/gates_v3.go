package gatetypes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	MaxGateDefinitionCount       = 128
	MaxGateDefinitionIDBytes     = 128
	MaxGatePromptBytes           = 16 << 10
	MaxGateFieldCount            = 64
	MaxGateFieldIDBytes          = 128
	MaxGateFieldLabelBytes       = 4 << 10
	MaxGateFieldOptionCount      = 128
	MaxGateFieldOptionIDBytes    = 128
	MaxGateFieldOptionLabelBytes = 4 << 10
	MaxGateActionPromptBytes     = 32 << 10
	MaxGateActionRefBytes        = 512
)

var GateIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

// GateActionType selects who or what supplies values for one gate execution.
// The application workflow remains solely responsible for interpreting the
// returned fields.
type GateActionType string

const (
	GateActionHuman         GateActionType = "human"
	GateActionAI            GateActionType = "ai"
	GateActionDeterministic GateActionType = "deterministic"
	GateActionWorkflow      GateActionType = "workflow"
)

// GateAction is the workflow default or a configuration-owned atomic
// replacement. Fields that do not belong to the selected Type are rejected;
// defaults and overrides are never partially merged.
type GateAction struct {
	Type        GateActionType `json:"type"                   yaml:"type"`
	AgentID     string         `json:"agent-id,omitempty"     yaml:"agent-id,omitempty"`
	Prompt      string         `json:"prompt,omitempty"       yaml:"prompt,omitempty"`
	Session     string         `json:"session,omitempty"      yaml:"session,omitempty"`
	History     string         `json:"history,omitempty"      yaml:"history,omitempty"`
	Cache       string         `json:"cache,omitempty"        yaml:"cache,omitempty"`
	Tools       string         `json:"tools,omitempty"        yaml:"tools,omitempty"`
	Fields      map[string]any `json:"fields,omitempty"       yaml:"fields,omitempty"`
	WorkflowRef string         `json:"workflow-ref,omitempty" yaml:"workflow-ref,omitempty"`
}

// GateFieldType is the closed, static field vocabulary understood by every
// gate action implementation.
type GateFieldType string

const (
	GateFieldShortText GateFieldType = "short-text"
	GateFieldLongText  GateFieldType = "long-text"
	GateFieldBoolean   GateFieldType = "boolean"
	GateFieldSelect    GateFieldType = "select"
)

type GateFieldOption struct {
	ID    string `json:"id"    yaml:"id"`
	Label string `json:"label" yaml:"label"`
}

type GateField struct {
	ID            string            `json:"id"                       yaml:"id"`
	Type          GateFieldType     `json:"type"                     yaml:"type"`
	Label         string            `json:"label"                    yaml:"label"`
	Required      bool              `json:"required,omitempty"       yaml:"required,omitempty"`
	MinSelections int               `json:"min-selections,omitempty" yaml:"min-selections,omitempty"`
	MaxSelections int               `json:"max-selections,omitempty" yaml:"max-selections,omitempty"`
	Options       []GateFieldOption `json:"options,omitempty"        yaml:"options,omitempty"`
}

type GateDefinition struct {
	Prompt        string      `json:"prompt"                   yaml:"prompt"`
	Fields        []GateField `json:"fields,omitempty"         yaml:"fields,omitempty"`
	DefaultAction *GateAction `json:"default-action,omitempty" yaml:"default-action,omitempty"`
}

// ValidateGateAction enforces the dependency-free structural action contract.
// The workflows package adds runtime-specific agent and local-workflow checks.
func ValidateGateAction(action GateAction) error {
	typeName := GateActionType(strings.TrimSpace(string(action.Type)))
	if typeName != action.Type {
		return errors.New("gate action type must not contain surrounding whitespace")
	}
	if !utf8.ValidString(action.Prompt) || len(action.Prompt) > MaxGateActionPromptBytes {
		return fmt.Errorf("gate action prompt must be valid UTF-8 and at most %d bytes", MaxGateActionPromptBytes)
	}
	if !utf8.ValidString(action.WorkflowRef) || len(action.WorkflowRef) > MaxGateActionRefBytes {
		return fmt.Errorf("gate action workflow-ref must be valid UTF-8 and at most %d bytes", MaxGateActionRefBytes)
	}
	hasAI := action.AgentID != "" || action.Prompt != "" || action.Session != "" ||
		action.History != "" || action.Cache != "" || action.Tools != ""
	hasFields := action.Fields != nil
	hasWorkflow := action.WorkflowRef != ""
	switch typeName {
	case GateActionHuman:
		if hasAI || hasFields || hasWorkflow {
			return errors.New("human gate action cannot configure AI, fields, or workflow-ref")
		}
	case GateActionAI:
		if strings.TrimSpace(action.Prompt) == "" {
			return errors.New("AI gate action requires prompt")
		}
		if hasFields || hasWorkflow {
			return errors.New("AI gate action cannot configure fields or workflow-ref")
		}
		if action.Session == "source" {
			if action.AgentID != "" || action.History != "" || action.Cache != "" || action.Tools != "" {
				return errors.New("source AI gate action derives agent, history, cache, and tools")
			}
		} else if strings.TrimSpace(action.AgentID) == "" {
			return errors.New("AI gate action requires agent-id")
		}
	case GateActionDeterministic:
		if action.Fields == nil {
			return errors.New("deterministic gate action requires fields")
		}
		if hasAI || hasWorkflow {
			return errors.New("deterministic gate action cannot configure AI or workflow-ref")
		}
	case GateActionWorkflow:
		if strings.TrimSpace(action.WorkflowRef) == "" {
			return errors.New("workflow gate action requires workflow-ref")
		}
		if hasAI || hasFields {
			return errors.New("workflow gate action cannot configure AI or fields")
		}
	default:
		return fmt.Errorf("unsupported gate action type %q", action.Type)
	}
	return nil
}

func (action *GateAction) UnmarshalJSON(data []byte) error {
	type plain GateAction
	var decoded plain
	if err := decodeStrictGateJSON(data, &decoded); err != nil {
		return err
	}
	*action = GateAction(decoded)
	return nil
}

func (action *GateAction) UnmarshalYAML(value *yaml.Node) error {
	if err := rejectUnknownGateYAMLKeys(value, map[string]bool{
		"type": true, "agent-id": true, "prompt": true, "session": true,
		"history": true, "cache": true, "tools": true, "fields": true,
		"workflow-ref": true,
	}); err != nil {
		return err
	}
	type plain GateAction
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*action = GateAction(decoded)
	return nil
}

func (definition *GateDefinition) UnmarshalJSON(data []byte) error {
	type plain GateDefinition
	var decoded plain
	if err := decodeStrictGateJSON(data, &decoded); err != nil {
		return err
	}
	*definition = GateDefinition(decoded)
	return nil
}

func (definition *GateDefinition) UnmarshalYAML(value *yaml.Node) error {
	if err := rejectUnknownGateYAMLKeys(value, map[string]bool{
		"prompt": true, "fields": true, "default-action": true,
	}); err != nil {
		return err
	}
	type plain GateDefinition
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*definition = GateDefinition(decoded)
	return nil
}

func (field *GateField) UnmarshalJSON(data []byte) error {
	type plain GateField
	var decoded plain
	if err := decodeStrictGateJSON(data, &decoded); err != nil {
		return err
	}
	*field = GateField(decoded)
	return nil
}

func (field *GateField) UnmarshalYAML(value *yaml.Node) error {
	if err := rejectUnknownGateYAMLKeys(value, map[string]bool{
		"id": true, "type": true, "label": true, "required": true,
		"min-selections": true, "max-selections": true, "options": true,
	}); err != nil {
		return err
	}
	type plain GateField
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*field = GateField(decoded)
	return nil
}

func (option *GateFieldOption) UnmarshalJSON(data []byte) error {
	type plain GateFieldOption
	var decoded plain
	if err := decodeStrictGateJSON(data, &decoded); err != nil {
		return err
	}
	*option = GateFieldOption(decoded)
	return nil
}

func (option *GateFieldOption) UnmarshalYAML(value *yaml.Node) error {
	if err := rejectUnknownGateYAMLKeys(value, map[string]bool{"id": true, "label": true}); err != nil {
		return err
	}
	type plain GateFieldOption
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*option = GateFieldOption(decoded)
	return nil
}

func decodeStrictGateJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func rejectUnknownGateYAMLKeys(value *yaml.Node, allowed map[string]bool) error {
	if value == nil || value.Kind != yaml.MappingNode {
		return errors.New("gate value must be a mapping")
	}
	seen := make(map[string]bool, len(value.Content)/2)
	for index := 0; index+1 < len(value.Content); index += 2 {
		key := strings.TrimSpace(value.Content[index].Value)
		if !allowed[key] {
			return fmt.Errorf("unsupported gate field %q", key)
		}
		if seen[key] {
			return fmt.Errorf("duplicate gate field %q", key)
		}
		seen[key] = true
	}
	return nil
}
