package common

import "testing"

func TestNormalizeToolSchemaTransform(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "empty", want: ToolSchemaTransformOff, ok: true},
		{name: "off", input: "off", want: ToolSchemaTransformOff, ok: true},
		{name: "native", input: "native", want: ToolSchemaTransformOff, ok: true},
		{name: "basic alias", input: "basic", want: ToolSchemaTransformSimple, ok: true},
		{name: "strict alias", input: "strict", want: ToolSchemaTransformSimple, ok: true},
		{name: "invalid", input: "custom"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeToolSchemaTransform(tt.input)
			if tt.ok && err != nil {
				t.Fatalf("NormalizeToolSchemaTransform(%q) error = %v", tt.input, err)
			}
			if !tt.ok && err == nil {
				t.Fatalf("NormalizeToolSchemaTransform(%q) expected error", tt.input)
			}
			if got != tt.want {
				t.Fatalf("NormalizeToolSchemaTransform(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTransformToolDefinitions(t *testing.T) {
	tools := []ToolDefinition{
		{
			Type: "function",
			Function: ToolFunctionDefinition{
				Name: "lookup",
				Parameters: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"query": map[string]any{"type": "string"},
					},
				},
			},
		},
		{
			Type: "web_search",
		},
	}

	unchanged, err := TransformToolDefinitions(tools, "off")
	if err != nil {
		t.Fatalf("TransformToolDefinitions(off) error = %v", err)
	}
	if len(unchanged) != len(tools) {
		t.Fatalf("len(unchanged) = %d, want %d", len(unchanged), len(tools))
	}
	if &unchanged[0] != &tools[0] {
		t.Fatal("off transform should return original tool slice")
	}

	transformed, err := TransformToolDefinitions(tools, "simple")
	if err != nil {
		t.Fatalf("TransformToolDefinitions(simple) error = %v", err)
	}
	if len(transformed) != len(tools) {
		t.Fatalf("len(transformed) = %d, want %d", len(transformed), len(tools))
	}
	if _, ok := transformed[0].Function.Parameters["additionalProperties"]; ok {
		t.Fatal("simple transform should remove unsupported additionalProperties")
	}
	transformed[0].Function.Parameters["x-test"] = true
	if _, ok := tools[0].Function.Parameters["x-test"]; ok {
		t.Fatal("simple transform should not mutate original parameters")
	}
	if transformed[1].Type != "web_search" {
		t.Fatalf("non-function tool type = %q, want web_search", transformed[1].Type)
	}
}

func TestTransformToolDefinitionsRejectsInvalidTransform(t *testing.T) {
	if _, err := TransformToolDefinitions(nil, "custom"); err == nil {
		t.Fatal("TransformToolDefinitions(custom) expected error")
	}
}
