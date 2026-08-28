package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/logger"
)

type (
	diagnosticNamedString string
	diagnosticNamedBool   bool
	diagnosticNamedInt    int
	diagnosticNamedUint   uint16
	diagnosticNamedFloat  float32
	diagnosticNamedSlice  []diagnosticNamedInt
	diagnosticNamedArray  [2]diagnosticNamedString
	diagnosticNamedMap    map[diagnosticNamedString]diagnosticNamedInt
)

type diagnosticHostileString string

func (diagnosticHostileString) String() string {
	panic("diagnostic String method invoked")
}

func (diagnosticHostileString) MarshalJSON() ([]byte, error) {
	panic("diagnostic MarshalJSON method invoked")
}

type diagnosticHostileMap map[diagnosticHostileString]diagnosticHostileString

func (diagnosticHostileMap) MarshalJSON() ([]byte, error) {
	panic("diagnostic map MarshalJSON method invoked")
}

type diagnosticHostileError struct{}

func (diagnosticHostileError) Error() string {
	panic("diagnostic Error method invoked")
}

func TestNormalizeToolArgumentsForDiagnosticsNamedGrammar(t *testing.T) {
	var nilMap diagnosticNamedMap
	var nilSlice diagnosticNamedSlice
	var nilPointer *diagnosticNamedInt
	arguments := map[string]any{
		"null":        nil,
		"string":      diagnosticNamedString("text"),
		"bool":        diagnosticNamedBool(true),
		"signed":      diagnosticNamedInt(-7),
		"unsigned":    diagnosticNamedUint(9),
		"float":       diagnosticNamedFloat(1.25),
		"slice":       diagnosticNamedSlice{1, 2},
		"array":       diagnosticNamedArray{"left", "right"},
		"map":         diagnosticNamedMap{"nested": 3},
		"nil_map":     nilMap,
		"nil_slice":   nilSlice,
		"nil_pointer": nilPointer,
		"number":      json.Number("12.5e+2"),
	}

	normalized, ok := normalizeToolArgumentsForDiagnostics(arguments).(map[string]any)
	if !ok {
		t.Fatal("named detached argument grammar was rejected")
	}
	want := map[string]any{
		"null":        nil,
		"string":      "text",
		"bool":        true,
		"signed":      int64(-7),
		"unsigned":    uint64(9),
		"float":       float64(1.25),
		"slice":       []any{int64(1), int64(2)},
		"array":       []any{"left", "right"},
		"map":         map[string]any{"nested": int64(3)},
		"nil_map":     map[string]any(nil),
		"nil_slice":   []any(nil),
		"nil_pointer": nil,
		"number":      json.Number("12.5e+2"),
	}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("normalized arguments = %#v, want %#v", normalized, want)
	}
	if value, typeOK := normalized["nil_map"].(map[string]any); !typeOK || value != nil {
		t.Fatalf("typed nil map = %#v", normalized["nil_map"])
	}
	if value, typeOK := normalized["nil_slice"].([]any); !typeOK || value != nil {
		t.Fatalf("typed nil slice = %#v", normalized["nil_slice"])
	}
}

func TestNormalizeToolArgumentsForDiagnosticsCanonicalEquivalence(t *testing.T) {
	named := map[string]any{
		"map":   diagnosticNamedMap{"value": 4},
		"slice": diagnosticNamedSlice{5, 6},
		"array": diagnosticNamedArray{"a", "b"},
	}
	builtIn := map[string]any{
		"map":   map[string]any{"value": int64(4)},
		"slice": []any{int64(5), int64(6)},
		"array": []any{"a", "b"},
	}
	namedNormalized := normalizeToolArgumentsForDiagnostics(named)
	builtInNormalized := normalizeToolArgumentsForDiagnostics(builtIn)
	if !reflect.DeepEqual(namedNormalized, builtInNormalized) {
		t.Fatalf("normalizations differ: %#v != %#v", namedNormalized, builtInNormalized)
	}
	namedObservation := logger.ObserveJSONValue(
		logger.ObservationDomainToolArguments,
		namedNormalized,
	)
	builtInObservation := logger.ObserveJSONValue(
		logger.ObservationDomainToolArguments,
		builtInNormalized,
	)
	if namedObservation != builtInObservation {
		t.Fatalf("equivalent observations differ: %#v != %#v", namedObservation, builtInObservation)
	}
}

func TestNormalizeToolArgumentsForDiagnosticsNeverInvokesMethods(t *testing.T) {
	arguments := map[string]any{
		"hostile": diagnosticHostileMap{
			diagnosticHostileString("key-canary"): diagnosticHostileString("value-canary"),
		},
	}
	normalized, ok := normalizeToolArgumentsForDiagnostics(arguments).(map[string]any)
	if !ok {
		t.Fatal("hostile named scalar grammar was rejected")
	}
	if !reflect.DeepEqual(
		normalized,
		map[string]any{"hostile": map[string]any{"key-canary": "value-canary"}},
	) {
		t.Fatalf("hostile normalization = %#v", normalized)
	}

	// An error is outside the detached argument grammar. Rejecting it must not
	// call Error while attempting to classify or format it.
	assertUnsupportedDiagnosticArguments(
		t,
		normalizeToolArgumentsForDiagnostics(map[string]any{
			"error": diagnosticHostileError{},
		}),
	)
}

func TestNormalizeToolArgumentsForDiagnosticsInvalidGraphsUseSentinel(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	cyclicSlice := make([]any, 1)
	cyclicSlice[0] = cyclicSlice

	deep := map[string]any{"leaf": "value"}
	for range maxDiagnosticArgumentDepth {
		deep = map[string]any{"nested": deep}
	}
	tooManyMapMembers := make(map[string]int, maxDiagnosticArgumentMembers+1)
	for index := 0; index <= maxDiagnosticArgumentMembers; index++ {
		tooManyMapMembers[fmt.Sprintf("key-%d", index)] = index
	}
	tooManyNodes := make(map[string][]int, maxDiagnosticArgumentMembers)
	for index := 0; index < maxDiagnosticArgumentMembers; index++ {
		tooManyNodes[fmt.Sprintf("key-%d", index)] = make([]int, 8)
	}

	tests := map[string]map[string]any{
		"non-nil pointer": func() map[string]any {
			value := 1
			return map[string]any{"value": &value}
		}(),
		"unsupported struct":  {"value": struct{ Value string }{Value: "canary"}},
		"unsupported func":    {"value": func() {}},
		"unsupported uintptr": {"value": uintptr(1)},
		"non-string map":      {"value": map[int]string{1: "canary"}},
		"invalid number":      {"value": json.Number("01")},
		"oversized number": {
			"value": json.Number(strings.Repeat("1", maxDiagnosticArgumentBytes+1)),
		},
		"nonfinite float":    {"value": math.Inf(1)},
		"slice member bound": {"value": make([]int, maxDiagnosticArgumentMembers+1)},
		"array member bound": {"value": [maxDiagnosticArgumentMembers + 1]int{}},
		"map member bound":   {"value": tooManyMapMembers},
		"node bound":         {"value": tooManyNodes},
		"byte bound":         {"value": strings.Repeat("x", maxDiagnosticArgumentBytes+1)},
		"depth bound":        deep,
		"cycle":              cycle,
		"slice cycle":        {"value": cyclicSlice},
	}
	for name, arguments := range tests {
		t.Run(name, func(t *testing.T) {
			assertUnsupportedDiagnosticArguments(
				t,
				normalizeToolArgumentsForDiagnostics(arguments),
			)
		})
	}
}

func TestNormalizeToolArgumentsForDiagnosticsNilAndEmptyRemainDistinct(t *testing.T) {
	rootNil, ok := normalizeToolArgumentsForDiagnostics(nil).(map[string]any)
	if !ok || rootNil != nil {
		t.Fatalf("root typed nil map = %#v", rootNil)
	}
	var nilMap map[string]any
	var nilSlice []any
	normalized, ok := normalizeToolArgumentsForDiagnostics(map[string]any{
		"nil_map":     nilMap,
		"empty_map":   map[string]any{},
		"nil_slice":   nilSlice,
		"empty_slice": []any{},
	}).(map[string]any)
	if !ok {
		t.Fatal("nil/empty graph was rejected")
	}

	nilMapObservation := logger.ObserveJSONValue(
		logger.ObservationDomainToolArguments,
		map[string]any{"value": normalized["nil_map"]},
	)
	emptyMapObservation := logger.ObserveJSONValue(
		logger.ObservationDomainToolArguments,
		map[string]any{"value": normalized["empty_map"]},
	)
	nilSliceObservation := logger.ObserveJSONValue(
		logger.ObservationDomainToolArguments,
		map[string]any{"value": normalized["nil_slice"]},
	)
	emptySliceObservation := logger.ObserveJSONValue(
		logger.ObservationDomainToolArguments,
		map[string]any{"value": normalized["empty_slice"]},
	)
	if nilMapObservation.Digest == emptyMapObservation.Digest ||
		nilSliceObservation.Digest == emptySliceObservation.Digest {
		t.Fatal("typed nil collection was normalized as an empty collection")
	}
}

func TestDiagnosticArgumentProjectorDefensiveBounds(t *testing.T) {
	newProjector := func(bytes, nodes int) diagnosticArgumentProjector {
		return diagnosticArgumentProjector{
			bytes:  bytes,
			nodes:  nodes,
			active: make(map[diagnosticArgumentVisit]struct{}),
		}
	}
	for name, value := range map[string]reflect.Value{
		"bool": reflect.ValueOf(true),
		"int":  reflect.ValueOf(int64(1)),
		"uint": reflect.ValueOf(uint64(1)),
	} {
		t.Run(name+" byte charge", func(t *testing.T) {
			projector := newProjector(maxDiagnosticArgumentBytes-diagnosticArgumentFrameBytes, 0)
			if projected, ok := projector.project(value, 1); ok || projected != nil {
				t.Fatalf("project() = %#v, %v; want rejection", projected, ok)
			}
		})
	}

	invalidProjector := newProjector(0, 0)
	if projected, ok := invalidProjector.project(reflect.Value{}, 1); !ok || projected != nil {
		t.Fatalf("invalid reflect value = %#v, %v; want JSON null", projected, ok)
	}

	nullBoundProjector := newProjector(0, maxDiagnosticArgumentNodes)
	if projected, ok := nullBoundProjector.projectNull(1); ok || projected != nil {
		t.Fatalf("bounded null = %#v, %v; want rejection", projected, ok)
	}

	mapProjector := newProjector(
		maxDiagnosticArgumentBytes-3*diagnosticArgumentFrameBytes,
		0,
	)
	if projected, ok := mapProjector.project(
		reflect.ValueOf(map[string]int{"k": 1}),
		1,
	); ok || projected != nil {
		t.Fatalf("bounded map key = %#v, %v; want rejection", projected, ok)
	}
}

func assertUnsupportedDiagnosticArguments(t *testing.T, value any) {
	t.Helper()
	if _, misleading := value.(map[string]any); misleading {
		t.Fatalf("invalid graph normalized as map: %#v", value)
	}
	if _, ok := value.(unsupportedDiagnosticArguments); !ok {
		t.Fatalf("invalid graph result type = %T", value)
	}
	observation := logger.ObserveJSONValue(logger.ObservationDomainToolArguments, value)
	if observation.State != "unavailable" || observation.Digest != "" {
		t.Fatalf("invalid graph observation = %#v", observation)
	}
}
