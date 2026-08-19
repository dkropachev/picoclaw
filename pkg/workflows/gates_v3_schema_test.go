package workflows

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

func TestGateV3WorkflowParsesAndValidatesCanonicalContract(t *testing.T) {
	workflow, parseErr := Parse([]byte(`
name: Pull request lifecycle
gates:
  charter-decision:
    prompt: Review the revised PR charter.
    fields:
      - id: action
        type: select
        label: What should happen?
        min-selections: 1
        max-selections: 1
        options:
          - id: approve
            label: Approve revised charter
          - id: revise
            label: Request another revision
      - id: explanation
        type: long-text
        label: Explain the decision
        required: false
    default-action:
      type: human
on:
  manual: {}
jobs:
  lifecycle:
    runs-on: picoclaw
    steps:
      - id: decide-charter
        uses: gate/exec
        with:
          gate-ref: gates.charter-decision
`))
	if parseErr != nil {
		t.Fatalf("Parse() error = %v", parseErr)
	}
	if err := Validate(workflow); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	gate := workflow.Gates["charter-decision"]
	if gate.Prompt != "Review the revised PR charter." || len(gate.Fields) != 2 ||
		gate.DefaultAction == nil || gate.DefaultAction.Type != GateActionHuman {
		t.Fatalf("parsed gate = %#v", gate)
	}
	if gate.Fields[0].Options[0].ID != "approve" {
		t.Fatalf("select options = %#v", gate.Fields[0].Options)
	}

	encoded, err := json.Marshal(workflow)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	text := string(encoded)
	for _, want := range []string{`"gates"`, `"default-action"`, `"min-selections"`, `"max-selections"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("JSON %s does not contain %s", text, want)
		}
	}
	for _, forbidden := range []string{`default_action`, `min_selections`, `max_selections`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("JSON contains non-kebab field %q: %s", forbidden, text)
		}
	}
}

func TestGateV3YAMLRejectsRemovedAndDynamicFields(t *testing.T) {
	for _, test := range []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "purpose",
			yaml: `gates: {decision: {prompt: Decide, purpose: authorization}}`,
			want: `unsupported gate field "purpose"`,
		},
		{
			name: "presentation",
			yaml: `gates: {decision: {prompt: Decide, presentation: cards}}`,
			want: `unsupported gate field "presentation"`,
		},
		{
			name: "visible-when",
			yaml: `gates: {decision: {prompt: Decide, fields: [{id: note, type: long-text, label: Note, visible-when: {field: x}}]}}`,
			want: `unsupported gate field "visible-when"`,
		},
		{
			name: "select-value",
			yaml: `gates: {decision: {prompt: Decide, fields: [{id: action, type: select, label: Action, min-selections: 1, max-selections: 1, options: [{id: yes, label: Yes, value: true}]}]}}`,
			want: `unsupported gate field "value"`,
		},
		{
			name: "dynamic-options",
			yaml: `gates: {decision: {prompt: Decide, fields: [{id: action, type: select, label: Action, options-from: inputs.actions}]}}`,
			want: `unsupported gate field "options-from"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse(
				[]byte(
					test.yaml + "\njobs: {main: {runs-on: picoclaw, steps: [{uses: gate/exec, with: {gate-ref: gates.decision}}]}}\n",
				),
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestGateV3ValidationRejectsInvalidReferencesAndFields(t *testing.T) {
	validGate := GateDefinition{
		Prompt: "Decide",
		Fields: []GateField{{
			ID: "action", Type: GateFieldSelect, Label: "Action",
			MinSelections: 1, MaxSelections: 1,
			Options: []GateFieldOption{{ID: "approve", Label: "Approve"}},
		}},
		DefaultAction: &GateAction{Type: GateActionHuman},
	}
	for _, test := range []struct {
		name   string
		gateID string
		gate   GateDefinition
		ref    string
		want   string
	}{
		{name: "snake gate id", gateID: "bad_gate", gate: validGate, ref: "gates.bad_gate", want: "canonical kebab-case"},
		{name: "relative ref", gateID: "decision", gate: validGate, ref: "decision", want: "full gates.<gate-id>"},
		{name: "missing ref", gateID: "decision", gate: validGate, ref: "gates.other", want: "referenced gate does not exist"},
		{name: "bad cardinality", gateID: "decision", gate: GateDefinition{
			Prompt: "Decide",
			Fields: []GateField{{
				ID: "action", Type: GateFieldSelect, Label: "Action", MaxSelections: 2,
				Options: []GateFieldOption{{ID: "approve", Label: "Approve"}},
			}},
		}, ref: "gates.decision", want: "select cardinality"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workflow := &Workflow{
				Gates: map[string]gatetypes.GateDefinition{test.gateID: test.gate},
				Jobs: map[string]Job{"main": {RunsOn: "picoclaw", Steps: []Step{{
					Uses: GateExecUses, With: map[string]any{"gate-ref": test.ref},
				}}}},
			}
			err := Validate(workflow)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateGateActionV3EnforcesAtomicShapes(t *testing.T) {
	valid := []GateAction{
		{Type: GateActionHuman},
		{Type: GateActionAI, AgentID: "reviewer", Prompt: "Decide", Session: "ephemeral", Tools: "none"},
		{Type: GateActionAI, Prompt: "Decide from source", Session: AgentSessionSource},
		{Type: GateActionDeterministic, Fields: map[string]any{}},
		{Type: GateActionWorkflow, WorkflowRef: "workflows/gates/decide.yaml"},
	}
	for _, action := range valid {
		if err := validateRuntimeGateAction(action); err != nil {
			t.Fatalf("validateRuntimeGateAction(%#v) error = %v", action, err)
		}
	}
	invalid := []GateAction{
		{Type: GateActionHuman, Prompt: "not allowed"},
		{Type: GateActionAI, AgentID: "reviewer"},
		{Type: GateActionAI, AgentID: "reviewer", Prompt: "Invalid source", Session: AgentSessionSource},
		{Type: GateActionDeterministic},
		{Type: GateActionWorkflow, WorkflowRef: "not-local"},
	}
	for _, action := range invalid {
		if err := validateRuntimeGateAction(action); err == nil {
			t.Fatalf("validateRuntimeGateAction(%#v) succeeded", action)
		}
	}
}
