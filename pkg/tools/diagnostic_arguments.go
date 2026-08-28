package tools

import (
	"encoding/json"
	"math"
	"reflect"
	"regexp"
)

const (
	maxDiagnosticArgumentDepth   = 16
	maxDiagnosticArgumentNodes   = 4096
	maxDiagnosticArgumentMembers = 512
	maxDiagnosticArgumentBytes   = 1 << 20
	diagnosticArgumentFrameBytes = 9
)

var (
	diagnosticJSONNumberType = reflect.TypeOf(json.Number(""))
	diagnosticJSONNumberRE   = regexp.MustCompile(
		`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`,
	)
)

// unsupportedDiagnosticArguments is deliberately not a map. Passing it to
// logger.DebugSensitiveCF or logger.ObserveJSONValue therefore produces an
// unavailable observation instead of misrepresenting invalid input as an
// empty argument object.
type unsupportedDiagnosticArguments struct{}

// normalizeToolArgumentsForDiagnostics converts a detached argument graph to
// the logger's exact built-in JSON grammar without invoking String, Error,
// MarshalJSON, formatting, or any other caller method. Concurrent mutation is
// outside this helper's contract; callers pass the detached invocation graph.
//
// The returned value is either map[string]any or the private unsupported
// sentinel above. Typed nil maps and slices retain their nil distinction, and
// a typed nil pointer becomes JSON null. Non-nil pointers and all other
// non-JSON kinds fail closed.
func normalizeToolArgumentsForDiagnostics(arguments map[string]any) (normalized any) {
	normalized = unsupportedDiagnosticArguments{}
	defer func() {
		if recover() != nil {
			normalized = unsupportedDiagnosticArguments{}
		}
	}()

	projector := diagnosticArgumentProjector{
		active: make(map[diagnosticArgumentVisit]struct{}),
	}
	value, ok := projector.project(reflect.ValueOf(arguments), 1)
	if !ok {
		return unsupportedDiagnosticArguments{}
	}
	result, ok := value.(map[string]any)
	if !ok {
		return unsupportedDiagnosticArguments{}
	}
	return result
}

type diagnosticArgumentVisit struct {
	kind reflect.Kind
	ptr  uintptr
	len  int
	cap  int
}

type diagnosticArgumentProjector struct {
	nodes  int
	bytes  int
	active map[diagnosticArgumentVisit]struct{}
}

func (projector *diagnosticArgumentProjector) project(
	value reflect.Value,
	depth int,
) (any, bool) {
	for value.IsValid() && value.Kind() == reflect.Interface {
		if value.IsNil() {
			return projector.projectNull(depth)
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return projector.projectNull(depth)
	}
	if depth > maxDiagnosticArgumentDepth || !projector.takeNode() {
		return nil, false
	}

	if value.Type() == diagnosticJSONNumberType {
		number := value.String()
		if !projector.charge(len(number)) || !diagnosticJSONNumberRE.MatchString(number) {
			return nil, false
		}
		return json.Number(number), true
	}

	switch value.Kind() {
	case reflect.Bool:
		if !projector.charge(1) {
			return nil, false
		}
		return value.Bool(), true
	case reflect.String:
		text := value.String()
		if !projector.charge(len(text)) {
			return nil, false
		}
		return text, true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if !projector.charge(8) {
			return nil, false
		}
		return value.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if !projector.charge(8) {
			return nil, false
		}
		return value.Uint(), true
	case reflect.Float32, reflect.Float64:
		floating := value.Float()
		if math.IsNaN(floating) || math.IsInf(floating, 0) || !projector.charge(8) {
			return nil, false
		}
		return floating, true
	case reflect.Pointer:
		if value.IsNil() {
			return nil, true
		}
		return nil, false
	case reflect.Map:
		return projector.projectMap(value, depth)
	case reflect.Slice:
		return projector.projectSlice(value, depth)
	case reflect.Array:
		return projector.projectArray(value, depth)
	default:
		return nil, false
	}
}

func (projector *diagnosticArgumentProjector) projectNull(depth int) (any, bool) {
	if depth > maxDiagnosticArgumentDepth || !projector.takeNode() {
		return nil, false
	}
	return nil, true
}

func (projector *diagnosticArgumentProjector) projectMap(
	value reflect.Value,
	depth int,
) (any, bool) {
	if value.Type().Key().Kind() != reflect.String ||
		value.Len() > maxDiagnosticArgumentMembers || !projector.charge(9) {
		return nil, false
	}
	if value.IsNil() {
		return map[string]any(nil), true
	}

	visit := diagnosticArgumentVisit{kind: reflect.Map, ptr: value.Pointer()}
	if _, cyclic := projector.active[visit]; cyclic {
		return nil, false
	}
	projector.active[visit] = struct{}{}
	defer delete(projector.active, visit)

	result := make(map[string]any, value.Len())
	iterator := value.MapRange()
	for iterator.Next() {
		key := iterator.Key().String()
		if !projector.takeNode() || !projector.charge(len(key)) {
			return nil, false
		}
		item, ok := projector.project(iterator.Value(), depth+1)
		if !ok {
			return nil, false
		}
		result[key] = item
	}
	return result, true
}

func (projector *diagnosticArgumentProjector) projectSlice(
	value reflect.Value,
	depth int,
) (any, bool) {
	if value.Len() > maxDiagnosticArgumentMembers || !projector.charge(9) {
		return nil, false
	}
	if value.IsNil() {
		return []any(nil), true
	}

	visit := diagnosticArgumentVisit{
		kind: reflect.Slice,
		ptr:  value.Pointer(),
		len:  value.Len(),
		cap:  value.Cap(),
	}
	if _, cyclic := projector.active[visit]; cyclic {
		return nil, false
	}
	projector.active[visit] = struct{}{}
	defer delete(projector.active, visit)

	return projector.projectSequence(value, depth)
}

func (projector *diagnosticArgumentProjector) projectArray(
	value reflect.Value,
	depth int,
) (any, bool) {
	if value.Len() > maxDiagnosticArgumentMembers || !projector.charge(9) {
		return nil, false
	}
	return projector.projectSequence(value, depth)
}

func (projector *diagnosticArgumentProjector) projectSequence(
	value reflect.Value,
	depth int,
) (any, bool) {
	result := make([]any, value.Len())
	for index := 0; index < value.Len(); index++ {
		item, ok := projector.project(value.Index(index), depth+1)
		if !ok {
			return nil, false
		}
		result[index] = item
	}
	return result, true
}

func (projector *diagnosticArgumentProjector) takeNode() bool {
	if projector.nodes >= maxDiagnosticArgumentNodes {
		return false
	}
	projector.nodes++
	return projector.charge(diagnosticArgumentFrameBytes)
}

func (projector *diagnosticArgumentProjector) charge(size int) bool {
	if size < 0 || size > maxDiagnosticArgumentBytes-projector.bytes {
		return false
	}
	projector.bytes += size
	return true
}
