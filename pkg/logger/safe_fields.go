package logger

import (
	"sort"

	"github.com/rs/zerolog"
)

const (
	maxSafeFieldScalars = 128

	safeFieldsStateKey  = "safe_fields_state"
	safeFieldsReasonKey = "safe_fields_reason_code"
	safeFieldsInvalid   = "invalid_fields"
	safeEnvelopeInvalid = "invalid_envelope"
)

// ComponentID selects a fixed logger component label. Values are append-only;
// zero and unknown values fail closed to the logger component.
type ComponentID uint8

const (
	ComponentAgent ComponentID = iota + 1
	ComponentTool
	ComponentTools
	ComponentToolLoop
	ComponentHooks
	ComponentEvents
	ComponentGateway
	ComponentConfig
	ComponentWorkflow
	ComponentVoice
	ComponentProvider
	ComponentAudio
	ComponentMCP
	ComponentSearch
	ComponentFilesystem
	ComponentWeb
	ComponentRuntime
	ComponentEvolution
	ComponentSubturn
	ComponentLogger
	ComponentDevice
	ComponentDiscovery
	ComponentEventing
	ComponentGitWorkspace
	ComponentPRWorkspace
	ComponentSeahorse
	ComponentShell
	ComponentVoiceTTS
	ComponentWebSearch
)

var componentLabels = [...]string{
	"",
	"agent",
	"tool",
	"tools",
	"toolloop",
	"hooks",
	"events",
	"gateway",
	"config",
	"workflow",
	"voice",
	"provider",
	"audio",
	"mcp",
	"search",
	"filesystem",
	"web",
	"runtime",
	"evolution",
	"subturn",
	"logger",
	"device",
	"discovery",
	"eventing",
	"git-workspace",
	"pr-workspace",
	"seahorse",
	"shell",
	"voice-tts",
	"web_search",
}

// DiagnosticMessageID selects a fixed literal message. Values are append-only
// so later sink migrations can extend the closed inventory without changing
// existing records.
type DiagnosticMessageID uint16

const (
	DiagnosticMessageEvent DiagnosticMessageID = iota + 1
	DiagnosticMessageInboundMessage
	DiagnosticMessageHistoryMessage
	DiagnosticMessageSystemPrompt
	DiagnosticMessageModelResponse
	DiagnosticMessageModelReasoning
	DiagnosticMessageToolArguments
	DiagnosticMessageHookToolArguments
	DiagnosticMessageToolExecutionStarted
	DiagnosticMessageToolExecutionCompleted
	DiagnosticMessageToolExecutionFailed
	DiagnosticMessageToolExecutionPanic
	DiagnosticMessageToolArgumentValidationFailed
	DiagnosticMessageToolNotFound
	DiagnosticMessageLLMIteration
	DiagnosticMessageLLMCallFailed
	DiagnosticMessageLLMDirectResponse
	DiagnosticMessageLLMRequestedToolCalls
	DiagnosticMessageProcessHookStderr
	DiagnosticMessageHookDecodeFailed
	DiagnosticMessageRuntimeEvent
)

var diagnosticMessageLabels = [...]string{
	"",
	"Diagnostic event",
	"Processing inbound message",
	"History message diagnostics",
	"System prompt diagnostics",
	"Model response diagnostics",
	"Model reasoning diagnostics",
	"Tool arguments diagnostics",
	"Hook tool arguments diagnostics",
	"Tool execution started",
	"Tool execution completed",
	"Tool execution failed",
	"Tool execution panic recovered",
	"Tool argument validation failed",
	"Tool not found",
	"LLM iteration",
	"LLM call failed",
	"LLM response without tool calls",
	"LLM requested tool calls",
	"Process hook stderr",
	"Failed to decode process hook message",
	"Runtime event",
}

// FieldKey selects one fixed structured key and its required value type.
// Values are append-only because source/AST manifests may inventory them.
type FieldKey uint8

const (
	// FieldIteration begins fixed int fields.
	FieldIteration FieldKey = iota + 1
	FieldMaxIterations
	FieldAttempt
	FieldRetryCount
	FieldDepth
	FieldMaxDepth
	FieldCount
	FieldMessageCount
	FieldHistoryMessageCount
	FieldMediaCount
	FieldToolCount
	FieldToolCallCount
	FieldArgumentCount
	FieldResultCount
	FieldModelCount
	FieldPromptPartCount
	FieldOverlayCount
	FieldSystemMessageCount
	FieldUserMessageCount
	FieldAssistantMessageCount
	FieldToolMessageCount
	FieldUnknownCount
	FieldActiveCount
	FieldSuccessCount
	FieldFailureCount
	FieldDroppedCount
	FieldQueueDepth
	FieldIndex

	// FieldDurationMilliseconds begins fixed int64 fields.
	FieldDurationMilliseconds
	FieldBytes
	FieldStaticBytes
	FieldDynamicBytes
	FieldTotalBytes
	FieldContentBytes
	FieldArgumentsBytes
	FieldResultBytes
	FieldSchemaBytes
	FieldInputBytes
	FieldOutputBytes
	FieldLimitBytes
	FieldTimeoutMilliseconds

	// FieldAsync begins fixed bool fields.
	FieldAsync
	FieldSuccess
	FieldTruncated
	FieldStreaming
	FieldCached
	FieldSuppressed
	FieldAccepted
	FieldEnabled
	FieldRetryable
	FieldDelivered
	FieldHandled
	FieldChanged
	FieldAvailable

	// FieldState begins fixed-enum fields.
	FieldState
	FieldAction
	FieldOutcome
	FieldRole
	FieldReason
)

type safeFieldKind uint8

const (
	safeFieldKindInt safeFieldKind = iota + 1
	safeFieldKindInt64
	safeFieldKindBool
	safeFieldKindEnum
	safeFieldKindObservation
)

// SafeEnumValue is a fixed value for enum-valued SafeFields. It cannot carry
// caller-controlled text.
type SafeEnumValue uint8

const (
	SafeEnumPending SafeEnumValue = iota + 1
	SafeEnumStarted
	SafeEnumCompleted
	SafeEnumFailed
	SafeEnumCanceled
	SafeEnumAllowed
	SafeEnumDenied
	SafeEnumDirect
	SafeEnumToolCalls
	SafeEnumSync
	SafeEnumAsync
	SafeEnumSystem
	SafeEnumUser
	SafeEnumAssistant
	SafeEnumTool
	SafeEnumTimeout
	SafeEnumSkipped
	SafeEnumUnavailable
	SafeEnumReady
	SafeEnumRunning
	SafeEnumStopped
)

var safeEnumLabels = [...]string{
	"",
	"pending",
	"started",
	"completed",
	"failed",
	"canceled",
	"allowed",
	"denied",
	"direct",
	"tool_calls",
	"sync",
	"async",
	"system",
	"user",
	"assistant",
	"tool",
	"timeout",
	"skipped",
	"unavailable",
	"ready",
	"running",
	"stopped",
}

// SafeField is one opaque typed field constructor result.
type SafeField struct {
	key         FieldKey
	kind        safeFieldKind
	intValue    int
	int64Value  int64
	boolValue   bool
	enumValue   SafeEnumValue
	prefix      ObservationFieldPrefix
	observation Observation
	valid       bool
}

// SafeInt constructs a non-negative int field only for an int FieldKey.
func SafeInt(key FieldKey, value int) SafeField {
	_, kind := safeFieldSpec(key)
	return SafeField{
		key: key, kind: safeFieldKindInt, intValue: value,
		valid: kind == safeFieldKindInt && value >= 0,
	}
}

// SafeInt64 constructs a non-negative int64 field only for an int64 FieldKey.
func SafeInt64(key FieldKey, value int64) SafeField {
	_, kind := safeFieldSpec(key)
	return SafeField{
		key: key, kind: safeFieldKindInt64, int64Value: value,
		valid: kind == safeFieldKindInt64 && value >= 0,
	}
}

// SafeBool constructs a bool field only for a bool FieldKey.
func SafeBool(key FieldKey, value bool) SafeField {
	_, kind := safeFieldSpec(key)
	return SafeField{
		key: key, kind: safeFieldKindBool, boolValue: value,
		valid: kind == safeFieldKindBool,
	}
}

// SafeEnum constructs a fixed enum field only for an enum FieldKey.
func SafeEnum(key FieldKey, value SafeEnumValue) SafeField {
	_, kind := safeFieldSpec(key)
	return SafeField{
		key: key, kind: safeFieldKindEnum, enumValue: value,
		valid: kind == safeFieldKindEnum && safeEnumAllowed(key, value),
	}
}

// SafeObservation expands one sealed Observation under its fixed prefix.
func SafeObservation(prefix ObservationFieldPrefix, observation Observation) SafeField {
	_, prefixOK := observationPrefixLabel(prefix)
	return SafeField{
		kind: safeFieldKindObservation, prefix: prefix, observation: observation,
		valid: prefixOK && observation.valid &&
			observation.expectedPrefix == prefix && validObservation(observation),
	}
}

// SafeFields is an immutable collection of typed safe fields. NewSafeFields
// copies the source slice. Zero is invalid; use NewSafeFields() for valid empty
// fields.
type SafeFields struct {
	entries     []SafeField
	preview     *sensitivePreviewWire
	scalarCount int
	valid       bool
}

// NewSafeFields validates and detaches entries. Duplicate keys/prefixes and
// invalid constructor results fail the whole collection closed.
func NewSafeFields(entries ...SafeField) SafeFields {
	if len(entries) > maxSafeFieldScalars {
		return SafeFields{}
	}
	copyEntries := append([]SafeField(nil), entries...)
	seenKeys := make(map[FieldKey]struct{}, len(copyEntries))
	seenPrefixes := make(map[ObservationFieldPrefix]struct{}, len(copyEntries))
	scalarCount := 0
	for _, entry := range copyEntries {
		if !safeFieldValid(entry) {
			return SafeFields{}
		}
		if entry.kind == safeFieldKindObservation {
			scalarCount += 8
			if _, exists := seenPrefixes[entry.prefix]; exists {
				return SafeFields{}
			}
			seenPrefixes[entry.prefix] = struct{}{}
			continue
		}
		scalarCount++
		if _, exists := seenKeys[entry.key]; exists {
			return SafeFields{}
		}
		seenKeys[entry.key] = struct{}{}
	}
	if scalarCount > maxSafeFieldScalars {
		return SafeFields{}
	}
	sort.Slice(copyEntries, func(left, right int) bool {
		return safeFieldSortLabel(copyEntries[left]) < safeFieldSortLabel(copyEntries[right])
	})
	return SafeFields{entries: copyEntries, scalarCount: scalarCount, valid: true}
}

func (fields SafeFields) withSensitivePreview(preview sensitivePreviewWire) SafeFields {
	if !fields.valid || fields.scalarCount >= maxSafeFieldScalars {
		return fields
	}
	fields.preview = &preview
	fields.scalarCount++
	return fields
}

// DebugSafeCF emits a safe Debug record with a fixed component and typed fields.
func DebugSafeCF(component ComponentID, message DiagnosticMessageID, fields SafeFields) {
	_ = emitSafeRecord(DEBUG, component, message, fields, "")
}

// InfoSafeCF emits a safe Info record with a fixed component and typed fields.
func InfoSafeCF(component ComponentID, message DiagnosticMessageID, fields SafeFields) {
	_ = emitSafeRecord(INFO, component, message, fields, "")
}

// WarnSafeCF emits a safe Warn record with a fixed component and typed fields.
func WarnSafeCF(component ComponentID, message DiagnosticMessageID, fields SafeFields) {
	_ = emitSafeRecord(WARN, component, message, fields, "")
}

// ErrorSafeCF emits a safe Error record with a fixed component and typed fields.
func ErrorSafeCF(component ComponentID, message DiagnosticMessageID, fields SafeFields) {
	_ = emitSafeRecord(ERROR, component, message, fields, "")
}

// FatalSafeCF emits a safe Fatal record with a fixed component and typed fields.
func FatalSafeCF(component ComponentID, message DiagnosticMessageID, fields SafeFields) {
	if emitSafeRecord(FATAL, component, message, fields, "") {
		return
	}
	// Fatal termination is independent of the package's output threshold. A
	// disabled zerolog logger invokes the configured fatal exit callback without
	// writing, preserving suppression while retaining conventional Fatal
	// semantics.
	disabled := zerolog.Nop()
	disabled.Fatal().Msg("")
}

func emitSafeRecord(
	level LogLevel,
	component ComponentID,
	message DiagnosticMessageID,
	fields SafeFields,
	invalidReason string,
) bool {
	lease, ok := acquireEmission(level)
	if !ok {
		return false
	}
	defer lease.release()

	componentText, componentOK := componentLabel(component)
	messageText, messageOK := diagnosticMessageLabel(message)
	if !componentOK || !messageOK {
		componentText = componentLabels[ComponentLogger]
		messageText = "Safe diagnostic rejected"
		fields = SafeFields{}
		invalidReason = safeEnvelopeInvalid
	}

	skip, _ := getCallerSkip()
	event := getEvent(lease.logger, level)
	event.Str(Component, componentText)
	appendSafeFields(event, fields, invalidReason)
	event.CallerSkipFrame(skip).Msg(messageText)
	return true
}

func appendSafeFields(event *zerolog.Event, fields SafeFields, invalidReason string) {
	if invalidReason == "" && !fields.valid {
		invalidReason = safeFieldsInvalid
	}
	if invalidReason != "" {
		event.Str(safeFieldsStateKey, observationStateUnavailable)
		event.Str(safeFieldsReasonKey, invalidReason)
		return
	}

	for _, field := range fields.entries {
		label, _ := safeFieldSpec(field.key)
		switch field.kind {
		case safeFieldKindInt:
			event.Int(label, field.intValue)
		case safeFieldKindInt64:
			event.Int64(label, field.int64Value)
		case safeFieldKindBool:
			event.Bool(label, field.boolValue)
		case safeFieldKindEnum:
			event.Str(label, safeEnumLabels[field.enumValue])
		case safeFieldKindObservation:
			appendSafeObservation(event, field.prefix, field.observation)
		}
	}
	if fields.preview != nil {
		event.RawJSON(sensitivePreviewField, fields.preview.serialized)
	}
}

func appendSafeObservation(
	event *zerolog.Event,
	prefix ObservationFieldPrefix,
	observation Observation,
) {
	label, ok := observationPrefixLabel(prefix)
	if !ok {
		prefix = ObservationPrefixError
		label = observationPrefixLabels[prefix]
	}
	fields := ObservationFields(prefix, observation)
	event.Str(label+"_class", fields[label+"_class"].(string))
	event.Int64(label+"_bytes", fields[label+"_bytes"].(int64))
	event.Int64(label+"_runes", fields[label+"_runes"].(int64))
	event.Bool(label+"_utf8_valid", fields[label+"_utf8_valid"].(bool))
	event.Int64(label+"_count", fields[label+"_count"].(int64))
	event.Str(label+"_digest", fields[label+"_digest"].(string))
	event.Str(label+"_state", fields[label+"_state"].(string))
	event.Str(label+"_reason_code", fields[label+"_reason_code"].(string))
}

func safeFieldValid(field SafeField) bool {
	if !field.valid {
		return false
	}
	if field.kind == safeFieldKindObservation {
		_, ok := observationPrefixLabel(field.prefix)
		return ok && field.observation.valid &&
			field.observation.expectedPrefix == field.prefix &&
			validObservation(field.observation)
	}
	_, expectedKind := safeFieldSpec(field.key)
	if expectedKind != field.kind {
		return false
	}
	switch field.kind {
	case safeFieldKindInt:
		return field.intValue >= 0
	case safeFieldKindInt64:
		return field.int64Value >= 0
	case safeFieldKindBool:
		return true
	case safeFieldKindEnum:
		return safeEnumAllowed(field.key, field.enumValue)
	default:
		return false
	}
}

func componentLabel(component ComponentID) (string, bool) {
	if component == 0 || int(component) >= len(componentLabels) {
		return "", false
	}
	return componentLabels[component], true
}

func diagnosticMessageLabel(message DiagnosticMessageID) (string, bool) {
	if message == 0 || int(message) >= len(diagnosticMessageLabels) {
		return "", false
	}
	return diagnosticMessageLabels[message], true
}

func validSafeEnum(value SafeEnumValue) bool {
	return value > 0 && int(value) < len(safeEnumLabels)
}

func safeEnumAllowed(key FieldKey, value SafeEnumValue) bool {
	if !validSafeEnum(value) {
		return false
	}
	switch key {
	case FieldState:
		switch value {
		case SafeEnumPending, SafeEnumStarted, SafeEnumCompleted, SafeEnumFailed,
			SafeEnumCanceled, SafeEnumUnavailable, SafeEnumReady,
			SafeEnumRunning, SafeEnumStopped:
			return true
		}
	case FieldAction:
		switch value {
		case SafeEnumAllowed, SafeEnumDenied, SafeEnumDirect, SafeEnumToolCalls,
			SafeEnumSync, SafeEnumAsync, SafeEnumSkipped:
			return true
		}
	case FieldOutcome:
		switch value {
		case SafeEnumCompleted, SafeEnumFailed, SafeEnumCanceled, SafeEnumTimeout,
			SafeEnumSkipped:
			return true
		}
	case FieldRole:
		switch value {
		case SafeEnumSystem, SafeEnumUser, SafeEnumAssistant, SafeEnumTool:
			return true
		}
	case FieldReason:
		switch value {
		case SafeEnumCanceled, SafeEnumDenied, SafeEnumTimeout, SafeEnumSkipped,
			SafeEnumUnavailable:
			return true
		}
	}
	return false
}

func safeFieldSortLabel(field SafeField) string {
	if field.kind == safeFieldKindObservation {
		label, _ := observationPrefixLabel(field.prefix)
		return label
	}
	label, _ := safeFieldSpec(field.key)
	return label
}

func safeFieldSpec(key FieldKey) (string, safeFieldKind) {
	switch key {
	case FieldIteration:
		return "iteration", safeFieldKindInt
	case FieldMaxIterations:
		return "max_iterations", safeFieldKindInt
	case FieldAttempt:
		return "attempt", safeFieldKindInt
	case FieldRetryCount:
		return "retry_count", safeFieldKindInt
	case FieldDepth:
		return "depth", safeFieldKindInt
	case FieldMaxDepth:
		return "max_depth", safeFieldKindInt
	case FieldCount:
		return "count", safeFieldKindInt
	case FieldMessageCount:
		return "message_count", safeFieldKindInt
	case FieldHistoryMessageCount:
		return "history_message_count", safeFieldKindInt
	case FieldMediaCount:
		return "media_count", safeFieldKindInt
	case FieldToolCount:
		return "tool_count", safeFieldKindInt
	case FieldToolCallCount:
		return "tool_call_count", safeFieldKindInt
	case FieldArgumentCount:
		return "argument_count", safeFieldKindInt
	case FieldResultCount:
		return "result_count", safeFieldKindInt
	case FieldModelCount:
		return "model_count", safeFieldKindInt
	case FieldPromptPartCount:
		return "prompt_part_count", safeFieldKindInt
	case FieldOverlayCount:
		return "overlay_count", safeFieldKindInt
	case FieldSystemMessageCount:
		return "system_message_count", safeFieldKindInt
	case FieldUserMessageCount:
		return "user_message_count", safeFieldKindInt
	case FieldAssistantMessageCount:
		return "assistant_message_count", safeFieldKindInt
	case FieldToolMessageCount:
		return "tool_message_count", safeFieldKindInt
	case FieldUnknownCount:
		return "unknown_count", safeFieldKindInt
	case FieldActiveCount:
		return "active_count", safeFieldKindInt
	case FieldSuccessCount:
		return "success_count", safeFieldKindInt
	case FieldFailureCount:
		return "failure_count", safeFieldKindInt
	case FieldDroppedCount:
		return "dropped_count", safeFieldKindInt
	case FieldQueueDepth:
		return "queue_depth", safeFieldKindInt
	case FieldIndex:
		return "index", safeFieldKindInt
	case FieldDurationMilliseconds:
		return "duration_ms", safeFieldKindInt64
	case FieldBytes:
		return "bytes", safeFieldKindInt64
	case FieldStaticBytes:
		return "static_bytes", safeFieldKindInt64
	case FieldDynamicBytes:
		return "dynamic_bytes", safeFieldKindInt64
	case FieldTotalBytes:
		return "total_bytes", safeFieldKindInt64
	case FieldContentBytes:
		return "content_bytes", safeFieldKindInt64
	case FieldArgumentsBytes:
		return "arguments_bytes", safeFieldKindInt64
	case FieldResultBytes:
		return "result_bytes", safeFieldKindInt64
	case FieldSchemaBytes:
		return "schema_bytes", safeFieldKindInt64
	case FieldInputBytes:
		return "input_bytes", safeFieldKindInt64
	case FieldOutputBytes:
		return "output_bytes", safeFieldKindInt64
	case FieldLimitBytes:
		return "limit_bytes", safeFieldKindInt64
	case FieldTimeoutMilliseconds:
		return "timeout_ms", safeFieldKindInt64
	case FieldAsync:
		return "async", safeFieldKindBool
	case FieldSuccess:
		return "success", safeFieldKindBool
	case FieldTruncated:
		return "truncated", safeFieldKindBool
	case FieldStreaming:
		return "streaming", safeFieldKindBool
	case FieldCached:
		return "cached", safeFieldKindBool
	case FieldSuppressed:
		return "suppressed", safeFieldKindBool
	case FieldAccepted:
		return "accepted", safeFieldKindBool
	case FieldEnabled:
		return "enabled", safeFieldKindBool
	case FieldRetryable:
		return "retryable", safeFieldKindBool
	case FieldDelivered:
		return "delivered", safeFieldKindBool
	case FieldHandled:
		return "handled", safeFieldKindBool
	case FieldChanged:
		return "changed", safeFieldKindBool
	case FieldAvailable:
		return "available", safeFieldKindBool
	case FieldState:
		return "state", safeFieldKindEnum
	case FieldAction:
		return "action", safeFieldKindEnum
	case FieldOutcome:
		return "outcome", safeFieldKindEnum
	case FieldRole:
		return "role", safeFieldKindEnum
	case FieldReason:
		return "reason", safeFieldKindEnum
	default:
		return "", 0
	}
}
