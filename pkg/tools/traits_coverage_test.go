package tools

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"
)

type traitsCoverageTool struct {
	name        string
	description string
	parameters  map[string]any
	metadata    PromptMetadata
}

func (tool *traitsCoverageTool) Name() string { return tool.name }

func (tool *traitsCoverageTool) Description() string { return tool.description }

func (tool *traitsCoverageTool) Parameters() map[string]any { return tool.parameters }

func (tool *traitsCoverageTool) PromptMetadata() PromptMetadata { return tool.metadata }

func (*traitsCoverageTool) Execute(context.Context, map[string]any) *ToolResult {
	return SilentResult("ok")
}

func TestToolTraitsNormalizationBoundaries(t *testing.T) {
	wantDefaults := ToolTraits{
		Risk:        ToolRiskUnknown,
		Parallel:    ToolParallelSerialized,
		Idempotency: ToolIdempotencyUnknown,
		Sharing:     ToolSharingPerOwner,
	}
	for name, input := range map[string]ToolTraits{
		"empty":            {},
		"parallel unknown": {Parallel: ToolParallelUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := input.normalized()
			if err != nil {
				t.Fatal(err)
			}
			if got != wantDefaults {
				t.Fatalf("normalized() = %#v, want %#v", got, wantDefaults)
			}
		})
	}

	for _, risk := range []ToolRiskClass{
		ToolRiskUnknown,
		ToolRiskReadOnly,
		ToolRiskMutation,
		ToolRiskProcess,
		ToolRiskNetwork,
		ToolRiskExternalWrite,
		ToolRiskDestructive,
	} {
		traits := ToolTraits{
			Risk:        risk,
			Parallel:    ToolParallelSafe,
			Idempotency: ToolIdempotencyIdempotent,
			Sharing:     ToolSharingImmutableShared,
		}
		if got, err := traits.normalized(); err != nil || got != traits {
			t.Fatalf("normalized(%q) = %#v, %v", risk, got, err)
		}
	}
	for _, idempotency := range []ToolIdempotencyClass{
		ToolIdempotencyUnknown,
		ToolIdempotencyIdempotent,
		ToolIdempotencyNonIdempotent,
	} {
		traits := ToolTraits{Idempotency: idempotency}
		got, err := traits.normalized()
		if err != nil || got.Idempotency != idempotency {
			t.Fatalf("normalized(%q) = %#v, %v", idempotency, got, err)
		}
	}

	tests := []struct {
		name    string
		traits  ToolTraits
		message string
	}{
		{name: "risk", traits: ToolTraits{Risk: "surprising"}, message: "risk class"},
		{name: "parallel", traits: ToolTraits{Parallel: "sometimes"}, message: "parallel class"},
		{name: "idempotency", traits: ToolTraits{Idempotency: "maybe"}, message: "idempotency class"},
		{name: "sharing", traits: ToolTraits{Sharing: "global"}, message: "sharing class"},
	}
	for _, test := range tests {
		t.Run("reject "+test.name, func(t *testing.T) {
			got, err := test.traits.normalized()
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("normalized() = %#v, %v", got, err)
			}
			if got != (ToolTraits{}) {
				t.Fatalf("invalid normalized traits = %#v", got)
			}
		})
	}
}

func TestToolDescriptorFreezeValidationAndDefaults(t *testing.T) {
	for _, name := range []string{"", " padded", "padded "} {
		t.Run("reject name "+name, func(t *testing.T) {
			if _, err := freezeToolDescriptor(ToolDescriptor{Name: name}); err == nil ||
				!strings.Contains(err.Error(), "exact and non-empty") {
				t.Fatalf("freezeToolDescriptor(%q) error = %v", name, err)
			}
		})
	}

	input := ToolDescriptor{
		Name: "defaults",
		Parameters: map[string]any{
			"properties": map[string]any{"value": []string{"one", "two"}},
		},
	}
	frozen, err := freezeToolDescriptor(input)
	if err != nil {
		t.Fatal(err)
	}
	wantMetadata := PromptMetadata{
		Layer:  ToolPromptLayerCapability,
		Slot:   ToolPromptSlotTooling,
		Source: ToolPromptSourceRegistry,
	}
	if frozen.PromptMetadata != wantMetadata {
		t.Fatalf("default prompt metadata = %#v, want %#v", frozen.PromptMetadata, wantMetadata)
	}
	input.Parameters["properties"].(map[string]any)["value"].([]string)[0] = "mutated"
	gotValue := frozen.Parameters["properties"].(map[string]any)["value"].([]string)[0]
	if gotValue != "one" {
		t.Fatalf("frozen descriptor retained caller schema alias: %q", gotValue)
	}

	partial, err := freezeToolDescriptor(ToolDescriptor{
		Name: "partial",
		PromptMetadata: PromptMetadata{
			Layer: "custom-layer",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if partial.PromptMetadata != (PromptMetadata{
		Layer: "custom-layer", Slot: ToolPromptSlotTooling, Source: ToolPromptSourceRegistry,
	}) {
		t.Fatalf("partial prompt metadata = %#v", partial.PromptMetadata)
	}

	if _, err := freezeToolDescriptor(ToolDescriptor{
		Name:       "bad-schema",
		Parameters: map[string]any{"bad": make(chan int)},
	}); err == nil || !strings.Contains(err.Error(), `freeze tool "bad-schema" parameters`) {
		t.Fatalf("schema freeze error = %v", err)
	}
}

func TestToolDescriptorFromToolBoundaries(t *testing.T) {
	var nilTool *traitsCoverageTool
	if descriptor, err := toolDescriptorFromTool(nilTool); err == nil ||
		!reflect.DeepEqual(descriptor, ToolDescriptor{}) || !strings.Contains(err.Error(), "tool is nil") {
		t.Fatalf("toolDescriptorFromTool(typed nil) = %#v, %v", descriptor, err)
	}

	tool := &traitsCoverageTool{
		name:        "from_tool",
		description: "descriptor source",
		parameters:  map[string]any{"type": "object"},
		metadata:    PromptMetadata{Slot: ToolPromptSlotMCP},
	}
	descriptor, err := toolDescriptorFromTool(tool)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Name != tool.name || descriptor.Description != tool.description ||
		descriptor.PromptMetadata != (PromptMetadata{
			Layer:  ToolPromptLayerCapability,
			Slot:   ToolPromptSlotMCP,
			Source: ToolPromptSourceRegistry,
		}) {
		t.Fatalf("tool descriptor = %#v", descriptor)
	}
	tool.parameters["type"] = "mutated"
	if descriptor.Parameters["type"] != "object" {
		t.Fatalf("tool descriptor retained live parameter alias: %#v", descriptor.Parameters)
	}
}

func TestCloneToolSchemaPreservesSupportedValuesAndDetaches(t *testing.T) {
	if cloned, err := cloneToolSchemaMap(nil); err != nil || cloned != nil {
		t.Fatalf("cloneToolSchemaMap(nil) = %#v, %v", cloned, err)
	}

	typedMap := map[string]int{"one": 1}
	typedSlice := []string{"one", "two"}
	var nilMap map[string]string
	var nilSlice []int
	source := map[string]any{
		"nil interface": nil,
		"nil map":       nilMap,
		"nil slice":     nilSlice,
		"map":           typedMap,
		"slice":         typedSlice,
		"array":         [2]int{1, 2},
		"string":        "value",
		"bool":          true,
		"int":           int(-1),
		"int8":          int8(-2),
		"int16":         int16(-3),
		"int32":         int32(-4),
		"int64":         int64(-5),
		"uint":          uint(1),
		"uint8":         uint8(2),
		"uint16":        uint16(3),
		"uint32":        uint32(4),
		"uint64":        uint64(5),
		"float32":       float32(1.25),
		"float64":       float64(2.5),
	}
	cloned, err := cloneToolSchemaMap(source)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cloned, source) {
		t.Fatalf("cloned schema = %#v, want %#v", cloned, source)
	}
	if cloned["nil map"].(map[string]string) != nil || cloned["nil slice"].([]int) != nil {
		t.Fatalf("typed nil values were not preserved: %#v", cloned)
	}
	typedMap["one"] = 9
	typedSlice[0] = "mutated"
	if cloned["map"].(map[string]int)["one"] != 1 || cloned["slice"].([]string)[0] != "one" {
		t.Fatalf("clone retained nested aliases: %#v", cloned)
	}

	invalid, err := cloneToolSchemaValue(reflect.Value{}, make(map[schemaVisit]struct{}), 0)
	if err != nil || invalid.IsValid() {
		t.Fatalf("clone invalid reflect value = %#v, %v", invalid, err)
	}
}

func TestCloneToolSchemaRejectsUnsafeShapes(t *testing.T) {
	cyclicMap := map[string]any{}
	cyclicMap["self"] = cyclicMap
	cyclicSlice := make([]any, 1)
	cyclicSlice[0] = cyclicSlice

	deep := any("leaf")
	for range 130 {
		deep = map[string]any{"next": deep}
	}

	tests := []struct {
		name    string
		value   any
		message string
	}{
		{name: "map key", value: map[int]string{1: "one"}, message: "string keys"},
		{name: "map element", value: map[string]any{"bad": func() {}}, message: "unsupported func"},
		{name: "slice element", value: []any{func() {}}, message: "unsupported func"},
		{name: "array element", value: [1]any{func() {}}, message: "unsupported func"},
		{name: "nan float32", value: float32(math.NaN()), message: "non-finite"},
		{name: "positive infinity", value: math.Inf(1), message: "non-finite"},
		{name: "negative infinity", value: math.Inf(-1), message: "non-finite"},
		{name: "complex", value: complex64(1i), message: "unsupported complex64"},
		{name: "struct", value: struct{}{}, message: "unsupported struct"},
		{name: "pointer", value: new(int), message: "unsupported ptr"},
		{name: "uintptr", value: uintptr(1), message: "unsupported uintptr"},
		{name: "cyclic map", value: cyclicMap, message: "contains a cycle"},
		{name: "cyclic slice", value: cyclicSlice, message: "contains a cycle"},
		{name: "too deep", value: deep, message: "too deeply nested"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := cloneToolSchemaMap(map[string]any{"value": test.value})
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("cloneToolSchemaMap() error = %v", err)
			}
		})
	}
}

func TestToolInterfaceIdentityBoundaries(t *testing.T) {
	channel := make(chan int)
	otherChannel := make(chan int)
	mapping := map[string]int{"one": 1}
	otherMapping := map[string]int{"one": 1}
	pointer := new(int)
	otherPointer := new(int)
	slice := []int{1, 2}
	sliceAlias := slice[:]
	otherSlice := []int{1, 2}
	function := func() {}

	tests := []struct {
		name        string
		left, right any
		want        bool
	}{
		{name: "both nil", want: true},
		{name: "one nil", right: 1},
		{name: "different types", left: int(1), right: int64(1)},
		{name: "function identity unsupported", left: function, right: function},
		{name: "same channel", left: channel, right: channel, want: true},
		{name: "different channel", left: channel, right: otherChannel},
		{name: "same map", left: mapping, right: mapping, want: true},
		{name: "different map", left: mapping, right: otherMapping},
		{name: "same pointer", left: pointer, right: pointer, want: true},
		{name: "different pointer", left: pointer, right: otherPointer},
		{name: "same slice", left: slice, right: sliceAlias, want: true},
		{name: "different slice", left: slice, right: otherSlice},
		{name: "equal comparable", left: "value", right: "value", want: true},
		{name: "unequal comparable", left: "left", right: "right"},
		{name: "uncomparable values", left: struct{ Values []int }{slice}, right: struct{ Values []int }{slice}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sameInterfaceIdentity(test.left, test.right); got != test.want {
				t.Fatalf("sameInterfaceIdentity() = %t, want %t", got, test.want)
			}
		})
	}

	if interfacePointer(nil) != 0 || interfacePointer(1) != 0 || interfacePointer(function) != 0 {
		t.Fatal("non-pointer-like value produced an interface pointer")
	}
	for name, value := range map[string]any{
		"channel": channel,
		"map":     mapping,
		"pointer": pointer,
		"slice":   slice,
	} {
		if got := interfacePointer(value); got == 0 {
			t.Errorf("interfacePointer(%s) = 0", name)
		}
	}

	type namedMap map[string]int
	if !samePointerIdentity(mapping, mapping) {
		t.Fatal("samePointerIdentity rejected the same map")
	}
	if samePointerIdentity(mapping, otherMapping) || samePointerIdentity(1, 1) ||
		samePointerIdentity(mapping, namedMap(mapping)) {
		t.Fatal("samePointerIdentity accepted a different identity or dynamic type")
	}
	var nilPointer *int
	if interfacePointer(nilPointer) != 0 || samePointerIdentity(nilPointer, nilPointer) {
		t.Fatal("typed nil pointer was treated as a live identity")
	}
}
