// Package gatetypes defines the dependency-free configuration types shared by
// workflow gate compilation and trusted policy persistence.
package gatetypes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// GateKind selects how one user-attention decision is evaluated.
type GateKind string

const (
	GateAIWorkingContext  GateKind = "ai_working_context"
	GateAIIsolatedContext GateKind = "ai_isolated_context"
	GateDeterministic     GateKind = "deterministic"
	GateZero              GateKind = "zero"

	MaxWorkflowGateCount          = 64
	MaxWorkflowGateIDBytes        = 64
	MaxWorkflowGateNameBytes      = 4 << 10
	MaxWorkflowGateTitleBytes     = 4 << 10
	MaxWorkflowGateCriteriaBytes  = 16 << 10
	MaxWorkflowGateConditionBytes = 4 << 10
	MaxWorkflowGateQuestionBytes  = 128 << 10
	MaxWorkflowGateSubjectBytes   = 1 << 20
	MaxWorkflowGateInputsBytes    = 2 << 20
	MaxWorkflowGateJSONDepth      = 64
	MaxWorkflowGateJSONNodes      = 100_000

	MaxGatePolicyDecisionPointBytes = 128
	MaxGatePolicyDecisionPoints     = 128
	MaxGatePolicyRepositories       = 1024
	MaxGatePolicyEntries            = 8192
	MaxGatePolicyGateEntries        = 8192
	MaxGatePolicyCatalogBytes       = 1 << 20
	MaxGatePolicyRepositoryBytes    = 256
)

// GateSpec describes one gate in an ordered composition.
//
// AI gates require AgentID, Criteria, and Title. Questions is optional
// guidance for the model. Deterministic gates require When, Title, and
// Questions. A zero gate is the composition identity and accepts no behavior
// fields.
type GateSpec struct {
	ID        string   `json:"id"                  yaml:"id"`
	Kind      GateKind `json:"kind"                yaml:"kind"`
	AgentID   string   `json:"agent_id,omitempty"  yaml:"agent_id,omitempty"`
	Criteria  string   `json:"criteria,omitempty"  yaml:"criteria,omitempty"`
	When      string   `json:"when,omitempty"      yaml:"when,omitempty"`
	Title     string   `json:"title,omitempty"     yaml:"title,omitempty"`
	Questions any      `json:"questions,omitempty" yaml:"questions,omitempty"`
}

// UnmarshalJSON preserves numbers in model questions as json.Number so a
// config load/save cycle cannot round integer tokens through float64.
func (spec *GateSpec) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID        string          `json:"id"`
		Kind      GateKind        `json:"kind"`
		AgentID   string          `json:"agent_id"`
		Criteria  string          `json:"criteria"`
		When      string          `json:"when"`
		Title     string          `json:"title"`
		Questions json.RawMessage `json:"questions"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode gate: multiple JSON values")
		}
		return fmt.Errorf("decode gate: %w", err)
	}
	decoded := GateSpec{
		ID:       wire.ID,
		Kind:     wire.Kind,
		AgentID:  wire.AgentID,
		Criteria: wire.Criteria,
		When:     wire.When,
		Title:    wire.Title,
	}
	if len(wire.Questions) != 0 && !bytes.Equal(bytes.TrimSpace(wire.Questions), []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(wire.Questions))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded.Questions); err != nil {
			return fmt.Errorf("decode gate questions: %w", err)
		}
	}
	*spec = decoded
	return nil
}

// GatePolicyMode selects how one repository policy combines with the global
// policy for the same decision point.
type GatePolicyMode string

const (
	GatePolicyInherit GatePolicyMode = "inherit"
	GatePolicyOverlay GatePolicyMode = "overlay"
	GatePolicyReplace GatePolicyMode = "replace"
	GatePolicyDisable GatePolicyMode = "disable"
)

// RepositoryGatePolicy is one already-selected repository override. Policy
// persistence and trusted repository lookup are deliberately outside the
// workflow resolver.
type RepositoryGatePolicy struct {
	Mode  GatePolicyMode `json:"mode"`
	Gates []GateSpec     `json:"gates,omitempty"`
}
