package tools

import (
	"fmt"
	"math"
	"reflect"
	"strings"
)

type ToolOwnerScope string

const (
	ToolOwnerScopeRegistry ToolOwnerScope = "registry"
	ToolOwnerScopeAgent    ToolOwnerScope = "agent"
	ToolOwnerScopeTurn     ToolOwnerScope = "turn"
)

// ToolOwner identifies construction and lifetime scope only. It is not an
// authorization capability; execution policy must use trusted call context.
type ToolOwner struct {
	Scope      ToolOwnerScope
	AgentID    string
	SessionKey string
	TurnID     string
}

func (owner ToolOwner) validate() error {
	switch owner.Scope {
	case ToolOwnerScopeRegistry:
	case ToolOwnerScopeAgent:
		if owner.AgentID == "" {
			return fmt.Errorf("agent tool owner requires agent ID")
		}
	case ToolOwnerScopeTurn:
		if owner.TurnID == "" {
			return fmt.Errorf("turn tool owner requires turn ID")
		}
	default:
		return fmt.Errorf("unsupported tool owner scope %q", owner.Scope)
	}
	for _, field := range []struct{ name, value string }{
		{name: "agent ID", value: owner.AgentID},
		{name: "session key", value: owner.SessionKey},
		{name: "turn ID", value: owner.TurnID},
	} {
		if field.value != strings.TrimSpace(field.value) {
			return fmt.Errorf("tool owner %s must be exact", field.name)
		}
	}
	return nil
}

type ToolRiskClass string

const (
	ToolRiskUnknown       ToolRiskClass = "unknown"
	ToolRiskReadOnly      ToolRiskClass = "read_only"
	ToolRiskMutation      ToolRiskClass = "mutation"
	ToolRiskProcess       ToolRiskClass = "process"
	ToolRiskNetwork       ToolRiskClass = "network"
	ToolRiskExternalWrite ToolRiskClass = "external_write"
	ToolRiskDestructive   ToolRiskClass = "destructive"
)

type ToolParallelClass string

const (
	ToolParallelUnknown    ToolParallelClass = "unknown"
	ToolParallelSafe       ToolParallelClass = "parallel_safe"
	ToolParallelSerialized ToolParallelClass = "serialized"
)

type ToolIdempotencyClass string

const (
	ToolIdempotencyUnknown       ToolIdempotencyClass = "unknown"
	ToolIdempotencyIdempotent    ToolIdempotencyClass = "idempotent"
	ToolIdempotencyNonIdempotent ToolIdempotencyClass = "non_idempotent"
)

type ToolSharingClass string

const (
	ToolSharingPerOwner        ToolSharingClass = "per_owner"
	ToolSharingImmutableShared ToolSharingClass = "immutable_shared"
)

// ToolTraits is trusted runtime metadata. It is deliberately absent from
// provider-visible tool definitions and does not itself authorize execution.
type ToolTraits struct {
	Risk        ToolRiskClass
	Parallel    ToolParallelClass
	Idempotency ToolIdempotencyClass
	Sharing     ToolSharingClass
}

func (traits ToolTraits) normalized() (ToolTraits, error) {
	if traits.Risk == "" {
		traits.Risk = ToolRiskUnknown
	}
	if traits.Parallel == "" || traits.Parallel == ToolParallelUnknown {
		traits.Parallel = ToolParallelSerialized
	}
	if traits.Idempotency == "" {
		traits.Idempotency = ToolIdempotencyUnknown
	}
	if traits.Sharing == "" {
		traits.Sharing = ToolSharingPerOwner
	}
	switch traits.Risk {
	case ToolRiskUnknown, ToolRiskReadOnly, ToolRiskMutation, ToolRiskProcess,
		ToolRiskNetwork, ToolRiskExternalWrite, ToolRiskDestructive:
	default:
		return ToolTraits{}, fmt.Errorf("unsupported tool risk class %q", traits.Risk)
	}
	switch traits.Parallel {
	case ToolParallelSafe, ToolParallelSerialized:
	default:
		return ToolTraits{}, fmt.Errorf("unsupported tool parallel class %q", traits.Parallel)
	}
	switch traits.Idempotency {
	case ToolIdempotencyUnknown, ToolIdempotencyIdempotent, ToolIdempotencyNonIdempotent:
	default:
		return ToolTraits{}, fmt.Errorf("unsupported tool idempotency class %q", traits.Idempotency)
	}
	switch traits.Sharing {
	case ToolSharingPerOwner, ToolSharingImmutableShared:
	default:
		return ToolTraits{}, fmt.Errorf("unsupported tool sharing class %q", traits.Sharing)
	}
	return traits, nil
}

func conservativeLegacyToolTraits() ToolTraits {
	traits, _ := (ToolTraits{}).normalized()
	return traits
}

type ToolDescriptor struct {
	Name           string
	Description    string
	Parameters     map[string]any
	PromptMetadata PromptMetadata
}

func freezeToolDescriptor(descriptor ToolDescriptor) (ToolDescriptor, error) {
	if descriptor.Name == "" || descriptor.Name != strings.TrimSpace(descriptor.Name) {
		return ToolDescriptor{}, fmt.Errorf("tool descriptor name must be exact and non-empty")
	}
	parameters, err := cloneToolSchemaMap(descriptor.Parameters)
	if err != nil {
		return ToolDescriptor{}, fmt.Errorf("freeze tool %q parameters: %w", descriptor.Name, err)
	}
	descriptor.Parameters = parameters
	defaults := PromptMetadata{
		Layer: ToolPromptLayerCapability, Slot: ToolPromptSlotTooling, Source: ToolPromptSourceRegistry,
	}
	if descriptor.PromptMetadata.Layer == "" {
		descriptor.PromptMetadata.Layer = defaults.Layer
	}
	if descriptor.PromptMetadata.Slot == "" {
		descriptor.PromptMetadata.Slot = defaults.Slot
	}
	if descriptor.PromptMetadata.Source == "" {
		descriptor.PromptMetadata.Source = defaults.Source
	}
	return descriptor, nil
}

func toolDescriptorFromTool(tool Tool) (ToolDescriptor, error) {
	if isTypedNil(tool) {
		return ToolDescriptor{}, fmt.Errorf("tool is nil")
	}
	return freezeToolDescriptor(ToolDescriptor{
		Name:           tool.Name(),
		Description:    tool.Description(),
		Parameters:     tool.Parameters(),
		PromptMetadata: promptMetadataForTool(tool),
	})
}

func cloneToolDescriptor(descriptor ToolDescriptor) ToolDescriptor {
	cloned := descriptor
	cloned.Parameters, _ = cloneToolSchemaMap(descriptor.Parameters)
	return cloned
}

type schemaVisit struct {
	typeOf reflect.Type
	kind   reflect.Kind
	ptr    uintptr
}

func cloneToolSchemaMap(source map[string]any) (map[string]any, error) {
	if source == nil {
		return nil, nil
	}
	cloned, err := cloneToolSchemaValue(reflect.ValueOf(source), make(map[schemaVisit]struct{}), 0)
	if err != nil {
		return nil, err
	}
	return cloned.Interface().(map[string]any), nil
}

func cloneToolSchemaValue(
	value reflect.Value,
	active map[schemaVisit]struct{},
	depth int,
) (reflect.Value, error) {
	if depth > 128 {
		return reflect.Value{}, fmt.Errorf("tool schema is cyclic or too deeply nested")
	}
	if !value.IsValid() {
		return reflect.Value{}, nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		cloned, err := cloneToolSchemaValue(value.Elem(), active, depth+1)
		if err != nil {
			return reflect.Value{}, err
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result, nil
	}

	switch value.Kind() {
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return reflect.Value{}, fmt.Errorf("tool schema maps require string keys")
		}
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		visit := schemaVisit{typeOf: value.Type(), kind: value.Kind(), ptr: value.Pointer()}
		if _, exists := active[visit]; exists {
			return reflect.Value{}, fmt.Errorf("tool schema contains a cycle")
		}
		active[visit] = struct{}{}
		defer delete(active, visit)
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			cloned, err := cloneToolSchemaValue(iterator.Value(), active, depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			result.SetMapIndex(iterator.Key(), cloned)
		}
		return result, nil
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		visit := schemaVisit{typeOf: value.Type(), kind: value.Kind(), ptr: value.Pointer()}
		if _, exists := active[visit]; exists {
			return reflect.Value{}, fmt.Errorf("tool schema contains a cycle")
		}
		active[visit] = struct{}{}
		defer delete(active, visit)
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := range value.Len() {
			cloned, err := cloneToolSchemaValue(value.Index(index), active, depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(index).Set(cloned)
		}
		return result, nil
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for index := range value.Len() {
			cloned, err := cloneToolSchemaValue(value.Index(index), active, depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(index).Set(cloned)
		}
		return result, nil
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return value, nil
	case reflect.Float32, reflect.Float64:
		if number := value.Float(); math.IsNaN(number) || math.IsInf(number, 0) {
			return reflect.Value{}, fmt.Errorf("tool schema contains non-finite number")
		}
		return value, nil
	default:
		return reflect.Value{}, fmt.Errorf("tool schema contains unsupported %s value", value.Kind())
	}
}

func descriptorsEqual(left, right ToolDescriptor) bool {
	return left.Name == right.Name &&
		left.Description == right.Description &&
		left.PromptMetadata == right.PromptMetadata &&
		reflect.DeepEqual(left.Parameters, right.Parameters)
}

func toolDescriptorSchema(descriptor ToolDescriptor) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        descriptor.Name,
			"description": descriptor.Description,
			"parameters":  cloneToolDescriptor(descriptor).Parameters,
		},
	}
}

func isTypedNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func sameInterfaceIdentity(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftValue := reflect.ValueOf(left)
	rightValue := reflect.ValueOf(right)
	if leftValue.Type() != rightValue.Type() {
		return false
	}
	switch leftValue.Kind() {
	case reflect.Func:
		return false
	case reflect.Chan, reflect.Map, reflect.Pointer, reflect.Slice:
		return leftValue.Pointer() == rightValue.Pointer()
	default:
		return leftValue.Type().Comparable() && leftValue.Interface() == rightValue.Interface()
	}
}

func interfacePointer(value any) uintptr {
	if value == nil {
		return 0
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.Pointer()
	default:
		return 0
	}
}

func samePointerIdentity(left, right any) bool {
	leftPointer := interfacePointer(left)
	return leftPointer != 0 && leftPointer == interfacePointer(right) &&
		reflect.TypeOf(left) == reflect.TypeOf(right)
}
