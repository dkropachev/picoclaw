package logger

import (
	"encoding/json"
	"math"
	"reflect"
	"unicode"
	"unicode/utf8"
)

const (
	maxSensitivePreviewWireBytes = 4096
	sensitivePreviewField        = "sensitive_preview"
)

// SensitivityClass selects one explicit application-data preview class. It is
// matched to an exact ObservationDomain; values are never admitted by range.
type SensitivityClass uint8

const (
	SensitivityPrompt SensitivityClass = iota + 1
	SensitivityInboundMessage
	SensitivityHistoryMessage
	SensitivityModelResponse
	SensitivityReasoning
	SensitivityToolArguments
)

type sensitivePreviewWire struct {
	escaped    string
	truncated  bool
	serialized []byte
}

type sensitivePreviewJSON struct {
	Escaped   string `json:"escaped"`
	Truncated bool   `json:"truncated"`
}

type sensitiveProjection struct {
	prefix        ObservationFieldPrefix
	observation   Observation
	previewSource string
	previewReady  bool
	pairingValid  bool
}

// DebugSensitiveCF is the only raw application-preview sink. It always emits
// a safe observation. A preview additionally requires an enabled immutable
// policy, exact component/message/class/domain pairing, valid typed fields,
// complete method-free input, and a valid UTF-8 projection.
func DebugSensitiveCF(
	policy DiagnosticPolicy,
	component ComponentID,
	message DiagnosticMessageID,
	fields SafeFields,
	class SensitivityClass,
	domain ObservationDomain,
	rawValue any,
) {
	lease, admitted := acquireEmission(DEBUG)
	if !admitted {
		return
	}
	defer lease.release()

	componentText, componentOK := componentLabel(component)
	messageText, messageOK := diagnosticMessageLabel(message)
	envelopeOK := componentOK && messageOK
	if !envelopeOK {
		componentText = componentLabels[ComponentLogger]
		messageText = "Sensitive diagnostic rejected"
	}

	wantsPreview := envelopeOK && fields.valid &&
		policy.allowsApplicationPreview() &&
		sensitiveMessageAllowed(message, class) &&
		sensitiveComponentAllowed(component, message)
	projection := buildSensitiveProjection(class, domain, rawValue, wantsPreview)

	invalidReason := ""
	if !envelopeOK {
		invalidReason = safeEnvelopeInvalid
	} else if !fields.valid || fieldsHasObservationPrefix(fields, projection.prefix) ||
		fields.scalarCount+8 > maxSafeFieldScalars {
		invalidReason = safeFieldsInvalid
	}

	if invalidReason == "" && fields.scalarCount+9 <= maxSafeFieldScalars &&
		wantsPreview && projection.pairingValid && projection.previewReady {
		if preview, ok := makeSensitivePreviewWire(projection.previewSource); ok {
			fields = fields.withSensitivePreview(preview)
		}
	}

	skip, _ := getCallerSkip()
	event := getEvent(lease.logger, DEBUG)
	event.Str(Component, componentText)
	appendSafeFields(event, fields, invalidReason)
	appendSafeObservation(event, projection.prefix, projection.observation)
	event.CallerSkipFrame(skip).Msg(messageText)
}

func buildSensitiveProjection(
	class SensitivityClass,
	domain ObservationDomain,
	rawValue any,
	wantsPreview bool,
) (projection sensitiveProjection) {
	projection = unavailableSensitiveProjection(reasonInternalPanic)
	defer func() {
		if recover() != nil {
			projection = unavailableSensitiveProjection(reasonInternalPanic)
		}
	}()

	prefix, pairingValid := sensitivePairPrefix(class, domain)
	if !pairingValid {
		return unavailableSensitiveProjection(reasonInvalidDomain)
	}
	projection.prefix = prefix
	projection.pairingValid = true

	if class == SensitivityToolArguments {
		arguments, ok := rawValue.(map[string]any)
		if !ok {
			projection.observation = unavailableObservation(
				prefix, "json", reasonUnsupportedType,
			)
			return projection
		}
		cloner := sensitiveGraphCloner{
			active:    make(map[observationVisit]struct{}),
			utf8Valid: true,
		}
		snapshotValue, reason := cloner.clone(arguments, 1)
		if reason != "" {
			projection.observation = unavailableObservation(prefix, "json", reason)
			return projection
		}
		snapshot, ok := snapshotValue.(map[string]any)
		if !ok {
			projection.observation = unavailableObservation(
				prefix, "json", reasonUnsupportedType,
			)
			return projection
		}
		projection.observation = ObserveJSONValue(domain, snapshot)
		if !wantsPreview || projection.observation.State != observationStateComplete ||
			!cloner.utf8Valid {
			return projection
		}
		canonical, ok := canonicalSensitiveJSON(snapshot)
		if !ok {
			return projection
		}
		projection.previewSource = canonical
		projection.previewReady = true
		return projection
	}

	text, ok := rawValue.(string)
	if !ok {
		projection.observation = unavailableObservation(
			prefix, "text", reasonUnsupportedType,
		)
		return projection
	}
	if len(text) > maxObservationBytes {
		projection.observation = unavailableObservation(prefix, "text", reasonByteLimit)
		return projection
	}
	projection.observation = ObserveText(domain, text)
	if wantsPreview && projection.observation.State == observationStateComplete &&
		projection.observation.UTF8Valid {
		projection.previewSource = text
		projection.previewReady = true
	}
	return projection
}

func unavailableSensitiveProjection(reason string) sensitiveProjection {
	return sensitiveProjection{
		prefix:      ObservationPrefixError,
		observation: unavailableObservation(ObservationPrefixError, "unknown", reason),
	}
}

func sensitivePairPrefix(
	class SensitivityClass,
	domain ObservationDomain,
) (ObservationFieldPrefix, bool) {
	switch class {
	case SensitivityPrompt:
		if domain == ObservationDomainPrompt {
			return ObservationPrefixPrompt, true
		}
	case SensitivityInboundMessage, SensitivityHistoryMessage:
		if domain == ObservationDomainMessageGraph {
			return ObservationPrefixMessageGraph, true
		}
	case SensitivityModelResponse:
		if domain == ObservationDomainModelResponse {
			return ObservationPrefixModelResponse, true
		}
	case SensitivityReasoning:
		if domain == ObservationDomainReasoning {
			return ObservationPrefixReasoning, true
		}
	case SensitivityToolArguments:
		if domain == ObservationDomainToolArguments {
			return ObservationPrefixToolArguments, true
		}
	}
	return 0, false
}

func sensitiveMessageAllowed(
	message DiagnosticMessageID,
	class SensitivityClass,
) bool {
	switch class {
	case SensitivityPrompt:
		return message == DiagnosticMessageSystemPrompt
	case SensitivityInboundMessage:
		return message == DiagnosticMessageInboundMessage
	case SensitivityHistoryMessage:
		return message == DiagnosticMessageHistoryMessage
	case SensitivityModelResponse:
		return message == DiagnosticMessageModelResponse
	case SensitivityReasoning:
		return message == DiagnosticMessageModelReasoning
	case SensitivityToolArguments:
		return message == DiagnosticMessageToolArguments ||
			message == DiagnosticMessageHookToolArguments
	default:
		return false
	}
}

func sensitiveComponentAllowed(
	component ComponentID,
	message DiagnosticMessageID,
) bool {
	switch message {
	case DiagnosticMessageSystemPrompt,
		DiagnosticMessageInboundMessage,
		DiagnosticMessageHistoryMessage,
		DiagnosticMessageModelResponse,
		DiagnosticMessageModelReasoning:
		return component == ComponentAgent
	case DiagnosticMessageToolArguments:
		return component == ComponentAgent || component == ComponentTool ||
			component == ComponentToolLoop
	case DiagnosticMessageHookToolArguments:
		return component == ComponentAgent || component == ComponentHooks
	default:
		return false
	}
}

func fieldsHasObservationPrefix(
	fields SafeFields,
	prefix ObservationFieldPrefix,
) bool {
	for _, field := range fields.entries {
		if field.kind == safeFieldKindObservation && field.prefix == prefix {
			return true
		}
	}
	return false
}

func makeSensitivePreviewWire(value string) (sensitivePreviewWire, bool) {
	if !utf8.ValidString(value) {
		return sensitivePreviewWire{}, false
	}

	escaped := make([]byte, 0, min(len(value), maxSensitivePreviewWireBytes))
	tokenEnds := make([]int, 0, min(len(value), maxSensitivePreviewWireBytes))
	truncated := false
	for _, character := range value {
		candidate := appendSensitiveEscape(escaped, character)
		serialized := marshalSensitivePreview(candidate, true)
		if len(serialized) > maxSensitivePreviewWireBytes {
			truncated = true
			break
		}
		escaped = candidate
		tokenEnds = append(tokenEnds, len(escaped))
	}

	if !truncated {
		serialized := marshalSensitivePreview(escaped, false)
		if len(serialized) <= maxSensitivePreviewWireBytes {
			return sensitivePreviewWire{
				escaped: string(escaped), truncated: false, serialized: serialized,
			}, true
		}
		truncated = true
		if len(tokenEnds) > 0 {
			tokenEnds = tokenEnds[:len(tokenEnds)-1]
			end := 0
			if len(tokenEnds) > 0 {
				end = tokenEnds[len(tokenEnds)-1]
			}
			escaped = escaped[:end]
		}
	}

	serialized := marshalSensitivePreview(escaped, truncated)
	if len(serialized) > maxSensitivePreviewWireBytes {
		return sensitivePreviewWire{}, false
	}
	return sensitivePreviewWire{
		escaped: string(escaped), truncated: truncated, serialized: serialized,
	}, true
}

func marshalSensitivePreview(escaped []byte, truncated bool) []byte {
	serialized, _ := json.Marshal(sensitivePreviewJSON{
		Escaped: string(escaped), Truncated: truncated,
	})
	return serialized
}

func appendSensitiveEscape(destination []byte, character rune) []byte {
	switch character {
	case '\b':
		return append(destination, '\\', 'b')
	case '\f':
		return append(destination, '\\', 'f')
	case '\n':
		return append(destination, '\\', 'n')
	case '\r':
		return append(destination, '\\', 'r')
	case '\t':
		return append(destination, '\\', 't')
	case '"':
		return append(destination, '\\', '"')
	case '\\':
		return append(destination, '\\', '\\')
	}
	if unicode.IsControl(character) || isBidiControl(character) ||
		character == '\u2028' || character == '\u2029' {
		return appendSensitiveUnicodeEscape(destination, character)
	}
	return utf8.AppendRune(destination, character)
}

func appendSensitiveUnicodeEscape(destination []byte, character rune) []byte {
	const hexadecimal = "0123456789abcdef"
	return append(destination,
		'\\', 'u',
		hexadecimal[(character>>12)&0x0f],
		hexadecimal[(character>>8)&0x0f],
		hexadecimal[(character>>4)&0x0f],
		hexadecimal[character&0x0f],
	)
}

func isBidiControl(character rune) bool {
	switch character {
	case '\u061c', '\u200e', '\u200f',
		'\u202a', '\u202b', '\u202c', '\u202d', '\u202e',
		'\u2066', '\u2067', '\u2068', '\u2069':
		return true
	default:
		return false
	}
}

type sensitiveGraphCloner struct {
	nodes     int
	bytes     int
	active    map[observationVisit]struct{}
	utf8Valid bool
}

func (cloner *sensitiveGraphCloner) clone(value any, depth int) (any, string) {
	if depth > maxObservationDepth {
		return nil, reasonDepthLimit
	}
	cloner.nodes++
	if cloner.nodes > maxObservationNodes {
		return nil, reasonNodeLimit
	}

	switch typed := value.(type) {
	case nil:
		return nil, ""
	case bool:
		return typed, ""
	case string:
		if !cloner.charge(len(typed)) {
			return nil, reasonByteLimit
		}
		if !utf8.ValidString(typed) {
			cloner.utf8Valid = false
		}
		return typed, ""
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		if !cloner.charge(8) {
			return nil, reasonByteLimit
		}
		return typed, ""
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return nil, reasonNonfiniteFloat
		}
		if !cloner.charge(8) {
			return nil, reasonByteLimit
		}
		return float64(typed), ""
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, reasonNonfiniteFloat
		}
		if !cloner.charge(8) {
			return nil, reasonByteLimit
		}
		return typed, ""
	case json.Number:
		if !cloner.charge(len(typed)) {
			return nil, reasonByteLimit
		}
		if !jsonNumberPattern.MatchString(string(typed)) {
			return nil, reasonInvalidNumber
		}
		return typed, ""
	case []any:
		return cloner.cloneSlice(typed, depth)
	case map[string]any:
		return cloner.cloneMap(typed, depth)
	default:
		return nil, reasonUnsupportedType
	}
}

func (cloner *sensitiveGraphCloner) cloneSlice(
	value []any,
	depth int,
) (any, string) {
	if len(value) > maxObservationMembers {
		return nil, reasonMemberLimit
	}
	if value == nil {
		return []any(nil), ""
	}
	visit := observationVisit{
		kind: reflect.Slice,
		ptr:  reflect.ValueOf(value).Pointer(),
		len:  len(value),
		cap:  cap(value),
	}
	if _, exists := cloner.active[visit]; exists {
		return nil, reasonCycle
	}
	cloner.active[visit] = struct{}{}
	defer delete(cloner.active, visit)

	cloned := make([]any, len(value))
	for index, item := range value {
		var reason string
		cloned[index], reason = cloner.clone(item, depth+1)
		if reason != "" {
			return nil, reason
		}
	}
	return cloned, ""
}

func (cloner *sensitiveGraphCloner) cloneMap(
	value map[string]any,
	depth int,
) (any, string) {
	if len(value) > maxObservationMembers {
		return nil, reasonMemberLimit
	}
	if value == nil {
		return map[string]any(nil), ""
	}
	visit := observationVisit{kind: reflect.Map, ptr: reflect.ValueOf(value).Pointer()}
	if _, exists := cloner.active[visit]; exists {
		return nil, reasonCycle
	}
	cloner.active[visit] = struct{}{}
	defer delete(cloner.active, visit)

	cloned := make(map[string]any, len(value))
	for key, item := range value {
		if !cloner.charge(len(key)) {
			return nil, reasonByteLimit
		}
		if !utf8.ValidString(key) {
			cloner.utf8Valid = false
		}
		clonedItem, reason := cloner.clone(item, depth+1)
		if reason != "" {
			return nil, reason
		}
		cloned[key] = clonedItem
	}
	return cloned, ""
}

func (cloner *sensitiveGraphCloner) charge(size int) bool {
	if size < 0 || size > maxObservationBytes-cloner.bytes {
		return false
	}
	cloner.bytes += size
	return true
}

func canonicalSensitiveJSON(value any) (string, bool) {
	// The cloned graph contains exact built-in JSON types only, so Marshal
	// cannot dispatch caller methods. encoding/json sorts string map keys,
	// producing one deterministic compact representation.
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxObservationBytes || !utf8.Valid(encoded) {
		return "", false
	}
	return string(encoded), true
}
