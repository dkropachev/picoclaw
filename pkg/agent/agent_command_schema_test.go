package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sipeed/picoclaw/pkg/commands"
)

func TestSummarizeMCPToolParametersNormalizesSchemaForms(t *testing.T) {
	direct := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"zeta": map[string]any{
				"type":        "  string ",
				"description": " final value ",
			},
			"alpha": "schema omitted",
			"middle": map[string]any{
				"type":        17,
				"description": false,
			},
		},
		"required": []string{"zeta"},
	}
	wantDirect := []commands.MCPToolParameterInfo{
		{Name: "alpha"},
		{Name: "middle"},
		{Name: "zeta", Type: "string", Description: "final value", Required: true},
	}
	if got := summarizeMCPToolParameters(direct); !reflect.DeepEqual(got, wantDirect) {
		t.Fatalf("direct schema summary = %#v, want %#v", got, wantDirect)
	}
	if got := normalizeMCPSchema(direct); !reflect.DeepEqual(got, direct) {
		t.Fatalf("direct schema normalization = %#v, want original map", got)
	}

	raw := json.RawMessage(`{
		"type":"object",
		"properties":{"count":{"type":" integer ","description":" item count "}},
		"required":["count",7]
	}`)
	wantRaw := []commands.MCPToolParameterInfo{
		{Name: "count", Type: "integer", Description: "item count", Required: true},
	}
	if got := summarizeMCPToolParameters(raw); !reflect.DeepEqual(got, wantRaw) {
		t.Fatalf("raw schema summary = %#v, want %#v", got, wantRaw)
	}
	if got := summarizeMCPToolParameters([]byte(raw)); !reflect.DeepEqual(got, wantRaw) {
		t.Fatalf("byte schema summary = %#v, want %#v", got, wantRaw)
	}

	type schemaProperty struct {
		Type        string `json:"type"`
		Description string `json:"description"`
	}
	type schemaDocument struct {
		Type       string                    `json:"type"`
		Properties map[string]schemaProperty `json:"properties"`
		Required   []string                  `json:"required"`
	}
	structured := schemaDocument{
		Type: "object",
		Properties: map[string]schemaProperty{
			"query": {Type: "string", Description: "search query"},
		},
		Required: []string{"query"},
	}
	if got := summarizeMCPToolParameters(structured); !reflect.DeepEqual(got, []commands.MCPToolParameterInfo{
		{Name: "query", Type: "string", Description: "search query", Required: true},
	}) {
		t.Fatalf("structured schema summary = %#v", got)
	}

	if got := summarizeMCPToolParameters(nil); got != nil {
		t.Fatalf("nil schema summary = %#v, want nil", got)
	}
	if got := summarizeMCPToolParameters(map[string]any{"properties": "invalid"}); got != nil {
		t.Fatalf("non-object properties summary = %#v, want nil", got)
	}
	if got := summarizeMCPToolParameters(json.RawMessage(`{"properties":`)); got != nil {
		t.Fatalf("malformed raw schema summary = %#v, want nil", got)
	}
	if got := summarizeMCPToolParameters([]byte(`not-json`)); got != nil {
		t.Fatalf("malformed byte schema summary = %#v, want nil", got)
	}

	type cyclicSchema struct {
		Next *cyclicSchema `json:"next"`
	}
	cyclic := &cyclicSchema{}
	cyclic.Next = cyclic
	if got := summarizeMCPToolParameters(cyclic); got != nil {
		t.Fatalf("cyclic schema summary = %#v, want nil", got)
	}
}
