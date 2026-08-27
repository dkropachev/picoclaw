package workflows

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWorkflowAuthoringShapeSanitizerAcceptsSupportedGoRepresentations(t *testing.T) {
	t.Run("required string slice is sorted and deduplicated", func(t *testing.T) {
		sanitizer := &WorkflowAuthoringShapeSanitizer{}
		shape, ok := sanitizer.Project(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"alpha": map[string]any{"type": "string"},
				"beta":  map[string]any{"type": "boolean"},
			},
			"required": []string{"beta", "alpha", "beta"},
		})
		if !ok || shape == nil {
			t.Fatal("Project() rejected []string required declaration")
		}
		if len(shape.Properties) != 2 ||
			!shape.Properties[0].Required ||
			!shape.Properties[1].Required {
			t.Fatalf("projected properties = %#v, want both required", shape.Properties)
		}
		unique := &WorkflowAuthoringShapeSanitizer{}
		if _, ok := unique.Project(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"alpha": map[string]any{"type": "string"},
				"beta":  map[string]any{"type": "boolean"},
			},
			"required": []string{"alpha", "beta"},
		}); !ok || sanitizer.units != unique.units {
			t.Fatalf(
				"duplicate required fields consumed %d units, unique fields consumed %d",
				sanitizer.units,
				unique.units,
			)
		}
	})

	t.Run("string enum slice is converted without aliasing", func(t *testing.T) {
		values := []string{"alpha", "beta"}
		shape, ok := (&WorkflowAuthoringShapeSanitizer{}).Project(map[string]any{
			"type": "string",
			"enum": values,
		})
		if !ok || shape == nil {
			t.Fatal("Project() rejected []string enum declaration")
		}
		values[0] = "mutated"
		encoded, err := json.Marshal(shape)
		if err != nil {
			t.Fatalf("json.Marshal(shape): %v", err)
		}
		if got, want := string(encoded), `{"type":"string","enum":["alpha","beta"]}`; got != want {
			t.Fatalf("encoded shape = %s, want %s", got, want)
		}
	})

	t.Run("all supported scalar widths preserve their values", func(t *testing.T) {
		shape, ok := (&WorkflowAuthoringShapeSanitizer{}).Project(map[string]any{
			"enum": []any{
				true,
				float32(1.25),
				int8(-8),
				int16(-16),
				int32(-32),
				uint(9),
				uint8(8),
				uint16(16),
				uint32(32),
			},
		})
		if !ok || shape == nil {
			t.Fatal("Project() rejected supported scalar widths")
		}
		encoded, err := json.Marshal(shape)
		if err != nil {
			t.Fatalf("json.Marshal(shape): %v", err)
		}
		var projected map[string]any
		if err := json.Unmarshal(encoded, &projected); err != nil {
			t.Fatalf("json.Unmarshal(shape): %v", err)
		}
		want := []any{true, 1.25, -8.0, -16.0, -32.0, 9.0, 8.0, 16.0, 32.0}
		if !reflect.DeepEqual(projected["enum"], want) {
			t.Fatalf("projected enum = %#v, want %#v", projected["enum"], want)
		}
	})
}

func TestWorkflowAuthoringShapeSanitizerRejectsMalformedGoRepresentations(t *testing.T) {
	tests := []struct {
		name   string
		schema map[string]any
	}{
		{
			name: "nil property schema",
			schema: map[string]any{
				"properties": map[string]any{"alpha": nil},
			},
		},
		{
			name: "scalar property schema",
			schema: map[string]any{
				"properties": map[string]any{"alpha": "string"},
			},
		},
		{
			name: "non-string required declaration",
			schema: map[string]any{
				"properties": map[string]any{"alpha": map[string]any{}},
				"required":   1,
			},
		},
		{
			name: "non-string required element",
			schema: map[string]any{
				"properties": map[string]any{"alpha": map[string]any{}},
				"required":   []any{1},
			},
		},
		{
			name:   "non-array enum declaration",
			schema: map[string]any{"enum": "alpha"},
		},
		{
			name:   "unsupported enum scalar",
			schema: map[string]any{"enum": []any{struct{}{}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sanitizer := &WorkflowAuthoringShapeSanitizer{}
			shape, ok := sanitizer.Project(test.schema)
			if ok || shape != nil {
				t.Fatalf("Project() = (%#v, %t), want rejection", shape, ok)
			}
			if sanitizer.units != 0 {
				t.Fatalf("failed projection consumed %d response units", sanitizer.units)
			}
		})
	}
}
