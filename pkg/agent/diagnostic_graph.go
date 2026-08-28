package agent

import (
	"encoding/json"
	"math"
	"reflect"
	"regexp"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const (
	agentMessageGraphDiagnosticFrame    = "picoclaw.agent.message_graph.v1"
	agentToolDefinitionsDiagnosticFrame = "picoclaw.agent.tool_definitions.v1"

	// These limits do not widen logger.ObserveJSONValue's grammar. They bound
	// work while projecting the broader provider graph and leave enough depth
	// for the fixed message/tool envelopes before the logger's depth limit.
	maxAgentDiagnosticValueDepth   = 10
	maxAgentDiagnosticNodes        = 4096
	maxAgentDiagnosticMembers      = 512
	maxAgentDiagnosticBytes        = 1 << 20
	agentDiagnosticNodeBytes       = 9
	agentDiagnosticCollectionBytes = 9
)

var (
	agentDiagnosticJSONNumberType = reflect.TypeOf(json.Number(""))
	agentDiagnosticJSONNumberRE   = regexp.MustCompile(
		`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`,
	)
)

// agentDiagnosticUnsupported is outside the logger observation grammar. It
// turns any projection failure into a sealed unavailable observation without
// retaining or rendering the rejected value.
type agentDiagnosticUnsupported struct{}

type agentDiagnosticMessageRoleCounts struct {
	system    int
	user      int
	assistant int
	tool      int
	unknown   int
}

func countAgentDiagnosticMessageRoles(
	messages []providers.Message,
) agentDiagnosticMessageRoleCounts {
	var counts agentDiagnosticMessageRoleCounts
	for index := range messages {
		switch messages[index].Role {
		case "system":
			counts.system++
		case "user":
			counts.user++
		case "assistant":
			counts.assistant++
		case "tool":
			counts.tool++
		default:
			// Developer and future provider-neutral roles remain visible as a
			// count without expanding a fixed enum from attacker-controlled text.
			counts.unknown++
		}
	}
	return counts
}

// observeAgentMessageGraph projects every provider-neutral message field into
// a fixed positional frame and immediately reduces it to a safe observation.
// The input must be a detached graph that is not concurrently mutated.
func observeAgentMessageGraph(messages []providers.Message) (observation logger.Observation) {
	observation = unavailableAgentDiagnostic(logger.ObservationDomainMessageGraph)
	defer func() {
		if recover() != nil {
			observation = unavailableAgentDiagnostic(logger.ObservationDomainMessageGraph)
		}
	}()

	framer := newAgentDiagnosticFramer()
	frame, ok := framer.messageGraph(messages)
	if !ok {
		return observation
	}
	return logger.ObserveJSONValue(logger.ObservationDomainMessageGraph, frame)
}

// observeAgentToolDefinitions projects every provider-neutral tool-definition
// field into a fixed positional frame and immediately reduces it to a safe
// observation. The input must be a detached graph that is not concurrently
// mutated.
func observeAgentToolDefinitions(
	definitions []providers.ToolDefinition,
) (observation logger.Observation) {
	observation = unavailableAgentDiagnostic(logger.ObservationDomainToolSchema)
	defer func() {
		if recover() != nil {
			observation = unavailableAgentDiagnostic(logger.ObservationDomainToolSchema)
		}
	}()

	framer := newAgentDiagnosticFramer()
	frame, ok := framer.toolDefinitions(definitions)
	if !ok {
		return observation
	}
	return logger.ObserveJSONValue(logger.ObservationDomainToolSchema, frame)
}

func unavailableAgentDiagnostic(domain logger.ObservationDomain) logger.Observation {
	return logger.ObserveJSONValue(domain, agentDiagnosticUnsupported{})
}

// normalizeAgentDiagnosticValue projects a detached provider argument/schema
// graph into the logger's exact built-in JSON grammar. The sealed unsupported
// sentinel makes every projection failure fail closed at both SafeObservation
// and DebugSensitiveCF without retaining or rendering the rejected value.
func normalizeAgentDiagnosticValue(value any) (normalized any) {
	normalized = agentDiagnosticUnsupported{}
	defer func() {
		if recover() != nil {
			normalized = agentDiagnosticUnsupported{}
		}
	}()

	projected, ok := newAgentDiagnosticFramer().projectDetached(value, 1)
	if !ok {
		return normalized
	}
	return projected
}

type agentDiagnosticVisit struct {
	kind reflect.Kind
	ptr  uintptr
	len  int
	cap  int
}

type agentDiagnosticFramer struct {
	nodes  int
	bytes  int
	active map[agentDiagnosticVisit]struct{}
}

func newAgentDiagnosticFramer() *agentDiagnosticFramer {
	return &agentDiagnosticFramer{active: make(map[agentDiagnosticVisit]struct{})}
}

func (framer *agentDiagnosticFramer) messageGraph(
	messages []providers.Message,
) ([]any, bool) {
	frame, ok := framer.slice(2, false)
	if !ok || !framer.putString(frame, 0, agentMessageGraphDiagnosticFrame) {
		return nil, false
	}
	projected, ok := framer.messages(messages)
	if !ok {
		return nil, false
	}
	frame[1] = projected
	return frame, true
}

func (framer *agentDiagnosticFramer) toolDefinitions(
	definitions []providers.ToolDefinition,
) ([]any, bool) {
	frame, ok := framer.slice(2, false)
	if !ok || !framer.putString(frame, 0, agentToolDefinitionsDiagnosticFrame) {
		return nil, false
	}
	projected, ok := framer.definitionList(definitions)
	if !ok {
		return nil, false
	}
	frame[1] = projected
	return frame, true
}

func (framer *agentDiagnosticFramer) messages(
	messages []providers.Message,
) ([]any, bool) {
	frame, ok := framer.slice(len(messages), messages == nil)
	if !ok || messages == nil {
		return frame, ok
	}
	for index := range messages {
		projected, projectedOK := framer.message(messages[index])
		if !projectedOK {
			return nil, false
		}
		frame[index] = projected
	}
	return frame, true
}

func (framer *agentDiagnosticFramer) message(message providers.Message) ([]any, bool) {
	frame, ok := framer.slice(14, false)
	if !ok ||
		!framer.putString(frame, 0, message.Role) ||
		!framer.putString(frame, 1, message.Content) ||
		!framer.putString(frame, 2, message.ModelName) {
		return nil, false
	}

	if frame[3], ok = framer.createdAt(message.CreatedAt); !ok {
		return nil, false
	}
	if frame[4], ok = framer.strings(message.Media); !ok {
		return nil, false
	}
	if frame[5], ok = framer.attachments(message.Attachments); !ok {
		return nil, false
	}
	if frame[6], ok = framer.promptParts(message.Parts); !ok {
		return nil, false
	}
	if !framer.putString(frame, 7, message.ReasoningContent) {
		return nil, false
	}
	if frame[8], ok = framer.contentBlocks(message.SystemParts); !ok {
		return nil, false
	}
	if frame[9], ok = framer.toolCalls(message.ToolCalls); !ok {
		return nil, false
	}
	if !framer.putString(frame, 10, message.ToolCallID) ||
		!framer.putString(frame, 11, message.PromptLayer) ||
		!framer.putString(frame, 12, message.PromptSlot) ||
		!framer.putString(frame, 13, message.PromptSource) {
		return nil, false
	}
	return frame, true
}

// createdAt deliberately excludes time.Time's monotonic clock reading. Unix
// seconds plus nanoseconds identify the instant, while the numeric zone offset
// preserves its provider-visible RFC3339 representation without retaining a
// process-local location or zone-name pointer.
func (framer *agentDiagnosticFramer) createdAt(value *time.Time) ([]any, bool) {
	if value == nil {
		return framer.slice(0, true)
	}
	frame, ok := framer.slice(3, false)
	if !ok || !framer.putInt64(frame, 0, value.Unix()) ||
		!framer.putInt64(frame, 1, int64(value.Nanosecond())) {
		return nil, false
	}
	_, offset := value.Zone()
	if !framer.putInt64(frame, 2, int64(offset)) {
		return nil, false
	}
	return frame, true
}

func (framer *agentDiagnosticFramer) strings(values []string) ([]any, bool) {
	frame, ok := framer.slice(len(values), values == nil)
	if !ok || values == nil {
		return frame, ok
	}
	for index := range values {
		if !framer.putString(frame, index, values[index]) {
			return nil, false
		}
	}
	return frame, true
}

func (framer *agentDiagnosticFramer) attachments(
	values []providers.Attachment,
) ([]any, bool) {
	frame, ok := framer.slice(len(values), values == nil)
	if !ok || values == nil {
		return frame, ok
	}
	for index := range values {
		projected, projectedOK := framer.stringTuple(
			values[index].Type,
			values[index].Ref,
			values[index].URL,
			values[index].Filename,
			values[index].ContentType,
		)
		if !projectedOK {
			return nil, false
		}
		frame[index] = projected
	}
	return frame, true
}

func (framer *agentDiagnosticFramer) promptParts(
	values []providers.PromptPart,
) ([]any, bool) {
	frame, ok := framer.slice(len(values), values == nil)
	if !ok || values == nil {
		return frame, ok
	}
	for index := range values {
		projected, projectedOK := framer.stringTuple(
			values[index].Type,
			values[index].Text,
			values[index].URI,
			values[index].MIMEType,
			values[index].Filename,
			values[index].Detail,
		)
		if !projectedOK {
			return nil, false
		}
		frame[index] = projected
	}
	return frame, true
}

func (framer *agentDiagnosticFramer) contentBlocks(
	values []providers.ContentBlock,
) ([]any, bool) {
	frame, ok := framer.slice(len(values), values == nil)
	if !ok || values == nil {
		return frame, ok
	}
	for index := range values {
		block, projectedOK := framer.contentBlock(values[index])
		if !projectedOK {
			return nil, false
		}
		frame[index] = block
	}
	return frame, true
}

func (framer *agentDiagnosticFramer) contentBlock(
	value providers.ContentBlock,
) ([]any, bool) {
	frame, ok := framer.slice(6, false)
	if !ok || !framer.putString(frame, 0, value.Type) ||
		!framer.putString(frame, 1, value.Text) {
		return nil, false
	}
	if value.CacheControl == nil {
		frame[2], ok = framer.slice(0, true)
	} else {
		frame[2], ok = framer.stringTuple(value.CacheControl.Type)
	}
	if !ok || !framer.putString(frame, 3, value.PromptLayer) ||
		!framer.putString(frame, 4, value.PromptSlot) ||
		!framer.putString(frame, 5, value.PromptSource) {
		return nil, false
	}
	return frame, true
}

func (framer *agentDiagnosticFramer) toolCalls(
	values []providers.ToolCall,
) ([]any, bool) {
	frame, ok := framer.slice(len(values), values == nil)
	if !ok || values == nil {
		return frame, ok
	}
	for index := range values {
		call, projectedOK := framer.toolCall(values[index])
		if !projectedOK {
			return nil, false
		}
		frame[index] = call
	}
	return frame, true
}

func (framer *agentDiagnosticFramer) toolCall(value providers.ToolCall) ([]any, bool) {
	frame, ok := framer.slice(7, false)
	if !ok || !framer.putString(frame, 0, value.ID) ||
		!framer.putString(frame, 1, value.Type) {
		return nil, false
	}
	if value.Function == nil {
		frame[2], ok = framer.slice(0, true)
	} else {
		frame[2], ok = framer.stringTuple(
			value.Function.Name,
			value.Function.Arguments,
			value.Function.ThoughtSignature,
		)
	}
	if !ok || !framer.putString(frame, 3, value.Name) {
		return nil, false
	}
	if frame[4], ok = framer.projectDetached(value.Arguments, 1); !ok {
		return nil, false
	}
	if !framer.putString(frame, 5, value.ThoughtSignature) {
		return nil, false
	}
	if frame[6], ok = framer.extraContent(value.ExtraContent); !ok {
		return nil, false
	}
	return frame, true
}

func (framer *agentDiagnosticFramer) extraContent(
	value *providers.ExtraContent,
) ([]any, bool) {
	if value == nil {
		return framer.slice(0, true)
	}
	frame, ok := framer.slice(2, false)
	if !ok {
		return nil, false
	}
	if value.Google == nil {
		frame[0], ok = framer.slice(0, true)
	} else {
		frame[0], ok = framer.stringTuple(value.Google.ThoughtSignature)
	}
	if !ok || !framer.putString(frame, 1, value.ToolFeedbackExplanation) {
		return nil, false
	}
	return frame, true
}

func (framer *agentDiagnosticFramer) definitionList(
	values []providers.ToolDefinition,
) ([]any, bool) {
	frame, ok := framer.slice(len(values), values == nil)
	if !ok || values == nil {
		return frame, ok
	}
	for index := range values {
		definition, projectedOK := framer.toolDefinition(values[index])
		if !projectedOK {
			return nil, false
		}
		frame[index] = definition
	}
	return frame, true
}

func (framer *agentDiagnosticFramer) toolDefinition(
	value providers.ToolDefinition,
) ([]any, bool) {
	frame, ok := framer.slice(5, false)
	if !ok || !framer.putString(frame, 0, value.Type) {
		return nil, false
	}
	function, ok := framer.slice(3, false)
	if !ok || !framer.putString(function, 0, value.Function.Name) ||
		!framer.putString(function, 1, value.Function.Description) {
		return nil, false
	}
	if function[2], ok = framer.projectDetached(value.Function.Parameters, 1); !ok {
		return nil, false
	}
	frame[1] = function
	if !framer.putString(frame, 2, value.PromptLayer) ||
		!framer.putString(frame, 3, value.PromptSlot) ||
		!framer.putString(frame, 4, value.PromptSource) {
		return nil, false
	}
	return frame, true
}

func (framer *agentDiagnosticFramer) stringTuple(values ...string) ([]any, bool) {
	frame, ok := framer.slice(len(values), false)
	if !ok {
		return nil, false
	}
	for index := range values {
		if !framer.putString(frame, index, values[index]) {
			return nil, false
		}
	}
	return frame, true
}

func (framer *agentDiagnosticFramer) putString(
	frame []any,
	index int,
	value string,
) bool {
	if !framer.takeNode(len(value)) {
		return false
	}
	frame[index] = value
	return true
}

func (framer *agentDiagnosticFramer) putInt64(
	frame []any,
	index int,
	value int64,
) bool {
	if !framer.takeNode(8) {
		return false
	}
	frame[index] = value
	return true
}

func (framer *agentDiagnosticFramer) slice(length int, nilValue bool) ([]any, bool) {
	if length < 0 || length > maxAgentDiagnosticMembers ||
		!framer.takeNode(agentDiagnosticCollectionBytes) {
		return nil, false
	}
	if nilValue {
		return []any(nil), true
	}
	return make([]any, length), true
}

// projectDetached normalizes the broader detached argument/schema grammar to
// the logger's exact built-in JSON grammar. It never invokes String, Error,
// MarshalJSON, formatting, or any other method on a projected value.
func (framer *agentDiagnosticFramer) projectDetached(
	value any,
	depth int,
) (any, bool) {
	return framer.projectValue(reflect.ValueOf(value), depth)
}

func (framer *agentDiagnosticFramer) projectValue(
	value reflect.Value,
	depth int,
) (any, bool) {
	for value.IsValid() && value.Kind() == reflect.Interface {
		if value.IsNil() {
			return framer.projectNull(depth)
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return framer.projectNull(depth)
	}
	if depth > maxAgentDiagnosticValueDepth {
		return nil, false
	}

	if value.Type() == agentDiagnosticJSONNumberType {
		number := value.String()
		if !framer.takeNode(len(number)) ||
			!agentDiagnosticJSONNumberRE.MatchString(number) {
			return nil, false
		}
		return json.Number(number), true
	}
	if agentDiagnosticTypeHasSemanticMethods(value.Type()) {
		return nil, false
	}

	switch value.Kind() {
	case reflect.Bool:
		if !framer.takeNode(1) {
			return nil, false
		}
		return value.Bool(), true
	case reflect.String:
		text := value.String()
		if !framer.takeNode(len(text)) {
			return nil, false
		}
		return text, true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if !framer.takeNode(8) {
			return nil, false
		}
		return value.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if !framer.takeNode(8) {
			return nil, false
		}
		return value.Uint(), true
	case reflect.Float32, reflect.Float64:
		floating := value.Float()
		if math.IsNaN(floating) || math.IsInf(floating, 0) ||
			!framer.takeNode(8) {
			return nil, false
		}
		return floating, true
	case reflect.Pointer:
		if value.IsNil() {
			return framer.projectNull(depth)
		}
		return nil, false
	case reflect.Map:
		return framer.projectMap(value, depth)
	case reflect.Slice:
		return framer.projectSlice(value, depth)
	case reflect.Array:
		return framer.projectArray(value, depth)
	default:
		return nil, false
	}
}

// agentDiagnosticTypeHasSemanticMethods rejects named values whose wire or
// display meaning can differ from their underlying reflect.Kind. Checking the
// pointer method set as well prevents addressability from changing projection
// semantics. json.Number is the sole trusted exception and is handled before
// this boundary with its own strict numeric grammar.
func agentDiagnosticTypeHasSemanticMethods(valueType reflect.Type) bool {
	if valueType.Name() == "" {
		return false
	}
	if valueType.NumMethod() != 0 {
		return true
	}
	switch valueType.Kind() {
	case reflect.Pointer, reflect.Interface:
		return false
	default:
		return reflect.PointerTo(valueType).NumMethod() != 0
	}
}

func (framer *agentDiagnosticFramer) projectNull(depth int) (any, bool) {
	if depth > maxAgentDiagnosticValueDepth || !framer.takeNode(0) {
		return nil, false
	}
	return nil, true
}

func (framer *agentDiagnosticFramer) projectMap(
	value reflect.Value,
	depth int,
) (any, bool) {
	if value.Type().Key().Kind() != reflect.String ||
		agentDiagnosticTypeHasSemanticMethods(value.Type().Key()) ||
		value.Len() > maxAgentDiagnosticMembers ||
		!framer.takeNode(agentDiagnosticCollectionBytes) {
		return nil, false
	}
	if value.IsNil() {
		return map[string]any(nil), true
	}

	visit := agentDiagnosticVisit{kind: reflect.Map, ptr: value.Pointer()}
	if _, cyclic := framer.active[visit]; cyclic {
		return nil, false
	}
	framer.active[visit] = struct{}{}
	defer delete(framer.active, visit)

	result := make(map[string]any, value.Len())
	iterator := value.MapRange()
	for iterator.Next() {
		key := iterator.Key().String()
		if !framer.takeNode(len(key)) {
			return nil, false
		}
		item, ok := framer.projectValue(iterator.Value(), depth+1)
		if !ok {
			return nil, false
		}
		result[key] = item
	}
	return result, true
}

func (framer *agentDiagnosticFramer) projectSlice(
	value reflect.Value,
	depth int,
) (any, bool) {
	if value.Len() > maxAgentDiagnosticMembers ||
		!framer.takeNode(agentDiagnosticCollectionBytes) {
		return nil, false
	}
	if value.IsNil() {
		return []any(nil), true
	}

	visit := agentDiagnosticVisit{
		kind: reflect.Slice,
		ptr:  value.Pointer(),
		len:  value.Len(),
		cap:  value.Cap(),
	}
	if _, cyclic := framer.active[visit]; cyclic {
		return nil, false
	}
	framer.active[visit] = struct{}{}
	defer delete(framer.active, visit)

	return framer.projectSequence(value, depth)
}

func (framer *agentDiagnosticFramer) projectArray(
	value reflect.Value,
	depth int,
) (any, bool) {
	if value.Len() > maxAgentDiagnosticMembers ||
		!framer.takeNode(agentDiagnosticCollectionBytes) {
		return nil, false
	}
	return framer.projectSequence(value, depth)
}

func (framer *agentDiagnosticFramer) projectSequence(
	value reflect.Value,
	depth int,
) (any, bool) {
	result := make([]any, value.Len())
	for index := 0; index < value.Len(); index++ {
		item, ok := framer.projectValue(value.Index(index), depth+1)
		if !ok {
			return nil, false
		}
		result[index] = item
	}
	return result, true
}

func (framer *agentDiagnosticFramer) takeNode(payloadBytes int) bool {
	if framer.nodes >= maxAgentDiagnosticNodes || payloadBytes < 0 ||
		payloadBytes > maxAgentDiagnosticBytes-agentDiagnosticNodeBytes ||
		framer.bytes > maxAgentDiagnosticBytes-agentDiagnosticNodeBytes-payloadBytes {
		return false
	}
	framer.nodes++
	framer.bytes += agentDiagnosticNodeBytes + payloadBytes
	return true
}
