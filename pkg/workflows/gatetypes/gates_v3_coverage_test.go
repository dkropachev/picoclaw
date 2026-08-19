package gatetypes

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGateActionValidationRejectsEveryMixedShape(t *testing.T) {
	tests := []GateAction{
		{Type: " human"},
		{Type: GateActionAI, AgentID: "agent", Prompt: string([]byte{0xff})},
		{Type: GateActionWorkflow, WorkflowRef: strings.Repeat("x", MaxGateActionRefBytes+1)},
		{Type: GateActionAI, AgentID: "agent", Prompt: "decide", Fields: map[string]any{}},
		{Type: GateActionAI, Prompt: "decide"},
		{Type: GateActionDeterministic, Fields: map[string]any{}, Prompt: "mixed"},
		{Type: GateActionWorkflow, WorkflowRef: "workflows/action.yml", Fields: map[string]any{}},
		{Type: "unsupported"},
	}
	for index, action := range tests {
		if err := ValidateGateAction(action); err == nil {
			t.Fatalf("invalid gate action %d was accepted: %#v", index, action)
		}
	}
}

func TestGateJSONAndYAMLDecodersRejectAmbiguousDocuments(t *testing.T) {
	jsonTargets := []any{
		&GateAction{},
		&GateDefinition{},
		&GateField{},
		&GateFieldOption{},
	}
	for _, target := range jsonTargets {
		if err := json.Unmarshal([]byte(`{"unknown":true}`), target); err == nil {
			t.Fatalf("unknown JSON field accepted by %T", target)
		}
	}
	if err := decodeStrictGateJSON([]byte(`{} {}`), &GateAction{}); err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
	if err := decodeStrictGateJSON([]byte(`{} trailing`), &GateAction{}); err == nil {
		t.Fatal("invalid trailing JSON was accepted")
	}

	var action GateAction
	if err := yaml.Unmarshal([]byte("type: human\nunknown: true\n"), &action); err == nil {
		t.Fatal("unknown YAML action field was accepted")
	}
	if err := yaml.Unmarshal([]byte("type: human\ntype: ai\n"), &action); err == nil {
		t.Fatal("duplicate YAML action field was accepted")
	}
	var option GateFieldOption
	if err := option.UnmarshalYAML(nil); err == nil {
		t.Fatal("nil YAML option was accepted")
	}
	badDecode := &yaml.Node{
		Kind: yaml.MappingNode,
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "id"},
			{
				Kind: yaml.SequenceNode, Tag: "!!seq",
				Content: []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: "nested"}},
			},
		},
	}
	if err := option.UnmarshalYAML(badDecode); err == nil {
		t.Fatal("invalid YAML option value was accepted")
	}
}
