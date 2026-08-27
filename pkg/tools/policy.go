package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers"
)

const (
	maxToolPolicyArgumentDepth = 64
	maxToolPolicyArgumentNodes = 100_000
	maxToolPolicyArgumentBytes = 4 << 20
	maxToolPolicyReasonCodeLen = 64
	MaxToolPolicyNameLen       = 512
	MaxToolPolicyCallIDLen     = 512
	maxToolPolicyAgentIDLen    = 256
	maxToolPolicySessionKeyLen = 2048
	maxToolPolicyTurnIDLen     = 512
)

var (
	// ErrToolPolicyUnavailable marks an infrastructure failure at the mandatory
	// model-tool authorization boundary. Ordinary policy denial is represented by
	// a deny decision and is not this error.
	ErrToolPolicyUnavailable = errors.New("tool policy unavailable")

	toolPolicyReasonCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)
	toolPolicyJSONNumberPattern = regexp.MustCompile(
		`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`,
	)
)

// ToolPolicySource identifies the trusted model-dispatch path evaluating a
// call. It is audit context, not an authorization capability on its own.
type ToolPolicySource string

const (
	ToolPolicySourceAgentPipeline  ToolPolicySource = "agent_pipeline"
	ToolPolicySourceGenericLoop    ToolPolicySource = "generic_tool_loop"
	ToolPolicySourceLocalRepair    ToolPolicySource = "local_repair"
	ToolPolicySourceLegacySubagent ToolPolicySource = "legacy_subagent"
)

// ToolFulfillmentKind distinguishes a registry effect from a trusted hook's
// synthetic response. Both kinds cross the same policy boundary.
type ToolFulfillmentKind string

const (
	ToolFulfillmentExecute     ToolFulfillmentKind = "execute"
	ToolFulfillmentHookRespond ToolFulfillmentKind = "hook_respond"
)

type ToolPolicyDecisionKind string

const (
	ToolPolicyDecisionAllow ToolPolicyDecisionKind = "allow"
	ToolPolicyDecisionDeny  ToolPolicyDecisionKind = "deny"
)

// ToolPolicySubject is trusted call-site context. These values must not be
// populated from model arguments, provider-visible definitions, or ToolOwner.
type ToolPolicySubject struct {
	AgentID    string
	SessionKey string
	TurnID     string
	ToolCallID string
	Source     ToolPolicySource
}

// ToolHookProvenance records the administrative hook, if any, that supplied a
// final rewrite or synthetic response. Source describes provenance; Trusted is
// the independently declared authority classification.
type ToolHookProvenance struct {
	Name    string
	Source  string
	Trusted bool
}

// ToolPolicyRequest is a detached, decision-only projection. Policy
// implementations never receive a registry, executable tool, or mutable turn.
type ToolPolicyRequest struct {
	Subject     ToolPolicySubject
	Tool        string
	Arguments   map[string]any
	Traits      ToolTraits
	Fulfillment ToolFulfillmentKind
	Hook        ToolHookProvenance
}

type ToolPolicyDecision struct {
	Kind       ToolPolicyDecisionKind
	ReasonCode string
}

// ToolPolicy evaluates one fully normalized model-authored tool action.
// Implementations must be safe for concurrent calls.
type ToolPolicy interface {
	EvaluateTool(ctx context.Context, request ToolPolicyRequest) (ToolPolicyDecision, error)
}

// CompatibilityAllowToolPolicy explicitly preserves the historical allow
// behavior after mandatory offered-set, profile, registry, and schema checks.
// It is stateless and safe for concurrent use.
type CompatibilityAllowToolPolicy struct{}

func (CompatibilityAllowToolPolicy) EvaluateTool(
	context.Context,
	ToolPolicyRequest,
) (ToolPolicyDecision, error) {
	return ToolPolicyDecision{
		Kind:       ToolPolicyDecisionAllow,
		ReasonCode: "compatibility_allow",
	}, nil
}

// EvaluateToolPolicy invokes policy with a detached request and validates the
// result. Nil policy, panic, invalid decisions, and context cancellation are
// infrastructure failures and never become implicit allow.
func EvaluateToolPolicy(
	ctx context.Context,
	policy ToolPolicy,
	request ToolPolicyRequest,
) (decision ToolPolicyDecision, err error) {
	if ctx == nil {
		return ToolPolicyDecision{}, fmt.Errorf("%w: context is nil", ErrToolPolicyUnavailable)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return ToolPolicyDecision{}, contextErr
	}
	if isTypedNil(policy) {
		return ToolPolicyDecision{}, fmt.Errorf("%w: policy is nil", ErrToolPolicyUnavailable)
	}
	request, err = detachToolPolicyRequest(request)
	if err != nil {
		return ToolPolicyDecision{}, fmt.Errorf("%w: invalid request: %v", ErrToolPolicyUnavailable, err)
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			decision = ToolPolicyDecision{}
			err = fmt.Errorf("%w: policy panic", ErrToolPolicyUnavailable)
		}
	}()

	decision, err = policy.EvaluateTool(ctx, request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return ToolPolicyDecision{}, contextErr
		}
		return ToolPolicyDecision{}, fmt.Errorf("%w: evaluation failed", ErrToolPolicyUnavailable)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return ToolPolicyDecision{}, contextErr
	}
	if decisionErr := validateToolPolicyDecision(decision); decisionErr != nil {
		return ToolPolicyDecision{}, fmt.Errorf("%w: %v", ErrToolPolicyUnavailable, decisionErr)
	}
	return decision, nil
}

func detachToolPolicyRequest(request ToolPolicyRequest) (ToolPolicyRequest, error) {
	if err := ValidateToolPolicyName(request.Tool); err != nil {
		return ToolPolicyRequest{}, err
	}
	if err := validateBoundedPolicyString(
		"agent ID",
		request.Subject.AgentID,
		maxToolPolicyAgentIDLen,
		true,
	); err != nil {
		return ToolPolicyRequest{}, err
	}
	if err := validateBoundedPolicyString(
		"session key",
		request.Subject.SessionKey,
		maxToolPolicySessionKeyLen,
		true,
	); err != nil {
		return ToolPolicyRequest{}, err
	}
	if err := validateBoundedPolicyString("turn ID", request.Subject.TurnID, maxToolPolicyTurnIDLen, true); err != nil {
		return ToolPolicyRequest{}, err
	}
	if err := validateBoundedPolicyString(
		"tool call ID",
		request.Subject.ToolCallID,
		MaxToolPolicyCallIDLen,
		false,
	); err != nil {
		return ToolPolicyRequest{}, err
	}
	if err := validateBoundedPolicyString("hook name", request.Hook.Name, MaxToolPolicyNameLen, true); err != nil {
		return ToolPolicyRequest{}, err
	}
	if err := validateBoundedPolicyString(
		"hook source",
		request.Hook.Source,
		maxToolPolicyAgentIDLen,
		true,
	); err != nil {
		return ToolPolicyRequest{}, err
	}
	if !request.Hook.Trusted && (request.Hook.Name != "" || request.Hook.Source != "") {
		return ToolPolicyRequest{}, fmt.Errorf("untrusted hook provenance must be empty")
	}
	if request.Hook.Trusted && (request.Hook.Name == "" || request.Hook.Source == "") {
		return ToolPolicyRequest{}, fmt.Errorf("trusted hook provenance must be complete")
	}
	switch request.Subject.Source {
	case ToolPolicySourceAgentPipeline, ToolPolicySourceGenericLoop,
		ToolPolicySourceLocalRepair, ToolPolicySourceLegacySubagent:
	default:
		return ToolPolicyRequest{}, fmt.Errorf("unsupported policy source %q", request.Subject.Source)
	}
	switch request.Fulfillment {
	case ToolFulfillmentExecute, ToolFulfillmentHookRespond:
	default:
		return ToolPolicyRequest{}, fmt.Errorf("unsupported fulfillment %q", request.Fulfillment)
	}
	traits, err := request.Traits.normalized()
	if err != nil {
		return ToolPolicyRequest{}, fmt.Errorf("normalize traits: %w", err)
	}
	request.Traits = traits
	request.Arguments, err = DetachToolArguments(request.Arguments)
	if err != nil {
		return ToolPolicyRequest{}, err
	}
	return request, nil
}

// ValidateToolPolicyName applies the exact public model-tool identity bound
// used before policy, events, logs, and persisted denial text.
func ValidateToolPolicyName(name string) error {
	return validateBoundedPolicyString("tool name", name, MaxToolPolicyNameLen, false)
}

func validateBoundedPolicyString(name, value string, limit int, allowEmpty bool) error {
	if value == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("%s must be non-empty", name)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be exact", name)
	}
	if len(value) > limit {
		return fmt.Errorf("%s exceeds maximum length", name)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("%s contains control characters", name)
		}
	}
	return nil
}

func validateToolPolicyDecision(decision ToolPolicyDecision) error {
	switch decision.Kind {
	case ToolPolicyDecisionAllow, ToolPolicyDecisionDeny:
	default:
		return fmt.Errorf("unsupported decision %q", decision.Kind)
	}
	if decision.ReasonCode == "" {
		return fmt.Errorf("reason code is required")
	}
	if len(decision.ReasonCode) > maxToolPolicyReasonCodeLen ||
		!toolPolicyReasonCodePattern.MatchString(decision.ReasonCode) {
		return fmt.Errorf("reason code is invalid")
	}
	return nil
}

// ValidateOfferedToolDefinitions rejects an ambiguous provider capability set
// before it is sent. Names are exact and unique; schemas must be detachable.
func ValidateOfferedToolDefinitions(definitions []providers.ToolDefinition) error {
	seen := make(map[string]struct{}, len(definitions))
	for index, definition := range definitions {
		name := definition.Function.Name
		if err := validateBoundedPolicyString("offered tool name", name, MaxToolPolicyNameLen, false); err != nil {
			return fmt.Errorf("definition %d: %w", index+1, err)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("definition %d duplicates offered tool %q", index+1, name)
		}
		seen[name] = struct{}{}
		if _, err := cloneToolSchemaMap(definition.Function.Parameters); err != nil {
			return fmt.Errorf("definition %d schema is invalid: %w", index+1, err)
		}
	}
	return nil
}

// DetachOfferedToolDefinitions validates and recursively clones provider-facing
// schemas so a provider cannot mutate the retained authorization surface.
func DetachOfferedToolDefinitions(
	definitions []providers.ToolDefinition,
) ([]providers.ToolDefinition, error) {
	if err := ValidateOfferedToolDefinitions(definitions); err != nil {
		return nil, err
	}
	if len(definitions) == 0 {
		return nil, nil
	}
	detached := make([]providers.ToolDefinition, len(definitions))
	for index, definition := range definitions {
		detached[index] = definition
		parameters, err := cloneToolSchemaMap(definition.Function.Parameters)
		if err != nil {
			return nil, fmt.Errorf("definition %d schema clone failed: %w", index+1, err)
		}
		detached[index].Function.Parameters = parameters
	}
	return detached, nil
}

// ValidateModelToolCallBatch validates protocol identity and exact offered-name
// membership for a complete provider response before any call is persisted or
// dispatched.
func ValidateModelToolCallBatch(
	calls []providers.ToolCall,
	definitions []providers.ToolDefinition,
) error {
	if err := ValidateOfferedToolDefinitions(definitions); err != nil {
		return err
	}
	if err := ValidateModelToolCallIdentity(calls); err != nil {
		return err
	}
	offered := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		offered[definition.Function.Name] = struct{}{}
	}
	for index, call := range calls {
		if _, exists := offered[call.Name]; !exists {
			return fmt.Errorf("tool call %d names unoffered tool %q", index+1, call.Name)
		}
	}
	return nil
}

// ValidateModelToolCallIdentity checks protocol identity without requiring
// offered membership. Agent Pipeline uses this before persistence so its
// established P008/unoffered skipped-result path remains structurally complete;
// ExecuteTools enforces exact offered membership before hooks.
func ValidateModelToolCallIdentity(calls []providers.ToolCall) error {
	seenIDs := make(map[string]struct{}, len(calls))
	for index, call := range calls {
		if err := validateBoundedPolicyString("tool call ID", call.ID, MaxToolPolicyCallIDLen, false); err != nil {
			return fmt.Errorf("tool call %d: %w", index+1, err)
		}
		if _, exists := seenIDs[call.ID]; exists {
			return fmt.Errorf("tool call %d duplicates ID %q", index+1, call.ID)
		}
		seenIDs[call.ID] = struct{}{}
		if err := validateBoundedPolicyString("tool call name", call.Name, MaxToolPolicyNameLen, false); err != nil {
			return fmt.Errorf("tool call %d: %w", index+1, err)
		}
	}
	return nil
}

// DetachToolArguments makes a bounded recursive copy of a JSON-like argument
// graph while preserving its concrete map, slice, array, and scalar types.
// Unsupported values and cycles are rejected before policy or execution.
func DetachToolArguments(arguments map[string]any) (map[string]any, error) {
	if arguments == nil {
		return map[string]any{}, nil
	}
	state := toolArgumentCloneState{
		active: make(map[toolArgumentVisit]struct{}),
	}
	cloned, err := state.clone(reflect.ValueOf(arguments), 0)
	if err != nil {
		return nil, err
	}
	return cloned.Interface().(map[string]any), nil
}

type toolArgumentVisit struct {
	typeOf reflect.Type
	kind   reflect.Kind
	ptr    uintptr
}

type toolArgumentCloneState struct {
	active map[toolArgumentVisit]struct{}
	nodes  int
	bytes  int
}

func (state *toolArgumentCloneState) addBytes(count int) error {
	if count < 0 || state.bytes > maxToolPolicyArgumentBytes-count {
		return fmt.Errorf("tool arguments exceed maximum bytes")
	}
	state.bytes += count
	return nil
}

func (state *toolArgumentCloneState) clone(value reflect.Value, depth int) (reflect.Value, error) {
	if depth > maxToolPolicyArgumentDepth {
		return reflect.Value{}, fmt.Errorf("tool arguments exceed maximum depth")
	}
	state.nodes++
	if state.nodes > maxToolPolicyArgumentNodes {
		return reflect.Value{}, fmt.Errorf("tool arguments exceed maximum nodes")
	}
	if !value.IsValid() {
		return reflect.Value{}, nil
	}

	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		cloned, err := state.clone(value.Elem(), depth+1)
		if err != nil {
			return reflect.Value{}, err
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result, nil
	}

	if value.Type() == reflect.TypeOf(json.Number("")) {
		number := value.Interface().(json.Number)
		if err := state.addBytes(len(number.String())); err != nil {
			return reflect.Value{}, err
		}
		if !toolPolicyJSONNumberPattern.MatchString(number.String()) {
			return reflect.Value{}, fmt.Errorf("invalid JSON number")
		}
		return value, nil
	}

	switch value.Kind() {
	case reflect.Bool:
		if err := state.addBytes(1); err != nil {
			return reflect.Value{}, err
		}
		return value, nil
	case reflect.String:
		if err := state.addBytes(len(value.String())); err != nil {
			return reflect.Value{}, err
		}
		return value, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if err := state.addBytes(8); err != nil {
			return reflect.Value{}, err
		}
		return value, nil
	case reflect.Float32, reflect.Float64:
		if math.IsNaN(value.Float()) || math.IsInf(value.Float(), 0) {
			return reflect.Value{}, fmt.Errorf("tool arguments contain a non-finite number")
		}
		if err := state.addBytes(8); err != nil {
			return reflect.Value{}, err
		}
		return value, nil
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return reflect.Value{}, fmt.Errorf("tool argument maps require string keys")
		}
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		if value.Len() > maxToolPolicyArgumentNodes-state.nodes {
			return reflect.Value{}, fmt.Errorf("tool arguments exceed maximum nodes")
		}
		visit := toolArgumentVisit{typeOf: value.Type(), kind: value.Kind(), ptr: value.Pointer()}
		if _, exists := state.active[visit]; exists {
			return reflect.Value{}, fmt.Errorf("tool arguments contain a cycle")
		}
		state.active[visit] = struct{}{}
		defer delete(state.active, visit)
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			if err := state.addBytes(len(iterator.Key().String())); err != nil {
				return reflect.Value{}, err
			}
			cloned, err := state.clone(iterator.Value(), depth+1)
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
		if value.Len() > maxToolPolicyArgumentNodes-state.nodes {
			return reflect.Value{}, fmt.Errorf("tool arguments exceed maximum nodes")
		}
		visit := toolArgumentVisit{typeOf: value.Type(), kind: value.Kind(), ptr: value.Pointer()}
		if _, exists := state.active[visit]; exists {
			return reflect.Value{}, fmt.Errorf("tool arguments contain a cycle")
		}
		state.active[visit] = struct{}{}
		defer delete(state.active, visit)
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			cloned, err := state.clone(value.Index(index), depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(index).Set(cloned)
		}
		return result, nil
	case reflect.Array:
		if value.Len() > maxToolPolicyArgumentNodes-state.nodes {
			return reflect.Value{}, fmt.Errorf("tool arguments exceed maximum nodes")
		}
		result := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			cloned, err := state.clone(value.Index(index), depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(index).Set(cloned)
		}
		return result, nil
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		return reflect.Value{}, fmt.Errorf("tool arguments contain unsupported pointer values")
	default:
		return reflect.Value{}, fmt.Errorf("tool arguments contain unsupported %s values", value.Kind())
	}
}
