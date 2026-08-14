package gatetypes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// GateHuman suspends for one typed user decision.
	GateHuman GateKind = "human"

	MaxGateWorkflowIDBytes            = 64
	MaxGateWorkflowDecisionPointBytes = 128
	MaxGateWorkflowStageCount         = MaxWorkflowGateCount
	MaxGateWorkflowJSONBytes          = MaxWorkflowGateInputsBytes
)

// GatePurpose determines how a product domain interprets an absent workflow
// and the terminal result of an explicitly configured workflow. Absence
// policy belongs to that domain and is deliberately not compiled here.
type GatePurpose string

const (
	GatePurposeAttention      GatePurpose = "attention"
	GatePurposeAuthorization  GatePurpose = "authorization"
	GatePurposeClassification GatePurpose = "classification"
)

// GateOutcome is the closed result vocabulary shared by AI and human stages.
// A staged all-of workflow continues only after pass.
type GateOutcome string

const (
	GateOutcomePass   GateOutcome = "pass"
	GateOutcomeRevise GateOutcome = "revise"
	GateOutcomeDefer  GateOutcome = "defer"
	GateOutcomeBlock  GateOutcome = "block"
)

// GateWorkflowSpec is one version-two staged gate workflow. Stages execute in
// source order and stop after the first non-pass result.
type GateWorkflowSpec struct {
	ID            string          `json:"id"             yaml:"id"`
	Name          string          `json:"name"           yaml:"name"`
	Purpose       GatePurpose     `json:"purpose"        yaml:"purpose"`
	DecisionPoint string          `json:"decision_point" yaml:"decision_point"`
	Stages        []GateStageSpec `json:"stages"         yaml:"stages"`
}

// GateStageSpec configures one ordered stage. When is the deterministic pass
// expression for deterministic stages and an optional applicability expression
// for AI or human stages. A false applicability expression skips that stage and
// therefore acts as pass. Criteria is required only for AI stages. Questions
// is optional AI guidance and required human-task content.
type GateStageSpec struct {
	ID        string   `json:"id"                  yaml:"id"`
	Title     string   `json:"title,omitempty"     yaml:"title,omitempty"`
	Kind      GateKind `json:"kind"                yaml:"kind"`
	When      string   `json:"when,omitempty"      yaml:"when,omitempty"`
	Criteria  string   `json:"criteria,omitempty"  yaml:"criteria,omitempty"`
	AgentID   string   `json:"agent_id,omitempty"  yaml:"agent_id,omitempty"`
	Questions any      `json:"questions,omitempty" yaml:"questions,omitempty"`
}

// UnmarshalJSON rejects unknown fields, trailing values, and imprecise number
// conversion in stage questions.
func (spec *GateStageSpec) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID        string          `json:"id"`
		Title     string          `json:"title"`
		Kind      GateKind        `json:"kind"`
		When      string          `json:"when"`
		Criteria  string          `json:"criteria"`
		AgentID   string          `json:"agent_id"`
		Questions json.RawMessage `json:"questions"`
	}
	if err := decodeGateV2JSON(data, &wire); err != nil {
		return err
	}
	decoded := GateStageSpec{
		ID:       wire.ID,
		Title:    wire.Title,
		Kind:     wire.Kind,
		When:     wire.When,
		Criteria: wire.Criteria,
		AgentID:  wire.AgentID,
	}
	if len(wire.Questions) != 0 && !bytes.Equal(bytes.TrimSpace(wire.Questions), []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(wire.Questions))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded.Questions); err != nil {
			return fmt.Errorf("decode gate v2 questions: %w", err)
		}
	}
	*spec = decoded
	return nil
}

// UnmarshalJSON applies the same strict wire contract to a complete workflow.
func (spec *GateWorkflowSpec) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID            string          `json:"id"`
		Name          string          `json:"name"`
		Purpose       GatePurpose     `json:"purpose"`
		DecisionPoint string          `json:"decision_point"`
		Stages        []GateStageSpec `json:"stages"`
	}
	if err := decodeGateV2JSON(data, &wire); err != nil {
		return err
	}
	*spec = GateWorkflowSpec(wire)
	return nil
}

// CanonicalGateWorkflowSpecJSON detaches a workflow into deterministic compact
// JSON. The round trip enforces the strict object contract before bytes become
// a configuration revision or pinned gate snapshot.
func CanonicalGateWorkflowSpecJSON(spec GateWorkflowSpec) ([]byte, error) {
	encoded, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encode gate v2 workflow: %w", err)
	}
	if len(encoded) > MaxGateWorkflowJSONBytes {
		return nil, fmt.Errorf("gate v2 workflow exceeds %d bytes", MaxGateWorkflowJSONBytes)
	}
	var detached GateWorkflowSpec
	if err := decodeGateV2JSON(encoded, &detached); err != nil {
		return nil, fmt.Errorf("detach gate v2 workflow: %w", err)
	}
	canonical, err := json.Marshal(detached)
	if err != nil {
		return nil, fmt.Errorf("encode detached gate v2 workflow: %w", err)
	}
	return canonical, nil
}

func decodeGateV2JSON(data []byte, destination any) error {
	if len(data) > MaxGateWorkflowJSONBytes {
		return fmt.Errorf("gate v2 JSON exceeds %d bytes", MaxGateWorkflowJSONBytes)
	}
	if err := rejectDuplicateGateV2JSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("decode gate v2 JSON: multiple values")
		}
		return fmt.Errorf("decode gate v2 JSON: %w", err)
	}
	return nil
}

func rejectDuplicateGateV2JSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	nodes := 0
	if err := consumeUniqueGateV2JSONValue(decoder, 0, &nodes); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("decode gate v2 JSON: multiple values")
		}
		return err
	}
	return nil
}

func consumeUniqueGateV2JSONValue(
	decoder *json.Decoder,
	depth int,
	nodes *int,
) error {
	if depth > MaxWorkflowGateJSONDepth {
		return fmt.Errorf("gate v2 JSON exceeds depth %d", MaxWorkflowGateJSONDepth)
	}
	*nodes = *nodes + 1
	if *nodes > MaxWorkflowGateJSONNodes {
		return fmt.Errorf("gate v2 JSON exceeds %d nodes", MaxWorkflowGateJSONNodes)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("gate v2 JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("gate v2 JSON contains duplicate key %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeUniqueGateV2JSONValue(decoder, depth+1, nodes); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("gate v2 JSON object is unterminated")
		}
	case '[':
		for decoder.More() {
			if err := consumeUniqueGateV2JSONValue(decoder, depth+1, nodes); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("gate v2 JSON array is unterminated")
		}
	default:
		return fmt.Errorf("gate v2 JSON has unexpected delimiter %q", delimiter)
	}
	return nil
}
