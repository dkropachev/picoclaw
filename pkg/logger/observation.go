package logger

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	observationDigestPreamble = "picoclaw-safe-log-value-v1\x00"
	maxObservationDepth       = 16
	maxObservationNodes       = 4096
	maxObservationMembers     = 512
	maxObservationBytes       = 1 << 20
)

const (
	observationStateComplete    = "complete"
	observationStateUnavailable = "unavailable"
)

const (
	reasonInvalidDomain   = "invalid_domain"
	reasonInvalidPrefix   = "invalid_prefix"
	reasonInvalidBound    = "invalid_bound"
	reasonUnsupportedType = "unsupported_type"
	reasonCycle           = "cycle"
	reasonDepthLimit      = "depth_limit"
	reasonNodeLimit       = "node_limit"
	reasonMemberLimit     = "member_limit"
	reasonByteLimit       = "byte_limit"
	reasonInvalidNumber   = "invalid_number"
	reasonNonfiniteFloat  = "nonfinite_float"
	reasonUnnamedError    = "unnamed_error_type"
	reasonUnnamedPanic    = "unnamed_panic_type"
	reasonInternalPanic   = "internal_panic"
)

// ObservationDomain selects the fixed domain separator used by a safe-log
// digest. Zero and values outside the declared constants are invalid.
type ObservationDomain uint8

const (
	ObservationDomainPrompt ObservationDomain = iota + 1
	ObservationDomainMessageGraph
	ObservationDomainModelResponse
	ObservationDomainReasoning
	ObservationDomainToolSchema
	ObservationDomainToolArguments
	ObservationDomainToolResult
	ObservationDomainQuery
	ObservationDomainRegex
	ObservationDomainCommand
	ObservationDomainStdout
	ObservationDomainTranscription
	ObservationDomainPath
	ObservationDomainURL
	ObservationDomainProxy
	ObservationDomainProviderBody
	ObservationDomainResponseHeader
	ObservationDomainProcessStderr
	ObservationDomainIdentityAgent
	ObservationDomainIdentitySession
	ObservationDomainIdentityChat
	ObservationDomainIdentitySender
	ObservationDomainIdentityMessage
	ObservationDomainIdentityTurn
	ObservationDomainIdentityTool
	ObservationDomainIdentityToolCall
	ObservationDomainIdentityHook
	ObservationDomainIdentityRuntime
	ObservationDomainIdentityAccount
	ObservationDomainIdentityRequest
	ObservationDomainIdentityTrace
	ObservationDomainIdentityTask
	ObservationDomainIdentityTopic
	ObservationDomainIdentitySpace
	ObservationDomainIdentityProvider
	ObservationDomainIdentityMCPServer
	ObservationDomainIdentityMCPTool
	ObservationDomainIdentityAudio
	ObservationDomainErrorType
	ObservationDomainErrorText
	// ObservationDomainHookMessage begins append-only domains. Existing numeric
	// values are part of the digest wire contract and must never move.
	ObservationDomainHookMessage
	ObservationDomainIdentityChannel
	ObservationDomainIdentityModel
	ObservationDomainIdentityWorkflow
	ObservationDomainIdentitySkill
	ObservationDomainIdentityRoute
	ObservationDomainIdentityContextManager
	ObservationDomainIdentityHookStage
	ObservationDomainIdentityHookAction
	ObservationDomainIdentityRuntimeEventKind
	ObservationDomainPanicType
	// ObservationDomainIdentityWorkspace begins the remaining identities
	// allocated from the closed P015b2 A/B/G/C census. Workspace, worker, and
	// light-model are first consumed by deferred b2b/b2c sinks; keeping their
	// meanings here prevents later repurposing.
	ObservationDomainIdentityWorkspace
	ObservationDomainIdentityWorker
	ObservationDomainIdentityPromptPart
	ObservationDomainIdentityPromptSource
	ObservationDomainIdentityPromptLayer
	ObservationDomainIdentityPromptSlot
	ObservationDomainIdentityRouteAgent
	ObservationDomainIdentityRouteChannel
	ObservationDomainIdentityRouteSession
	ObservationDomainIdentityTargetChannel
	ObservationDomainIdentityProviderModel
	ObservationDomainIdentityLightModel
	ObservationDomainIdentityParentTurn
	ObservationDomainIdentityChildTurn
	ObservationDomainIdentityReason
	ObservationDomainIdentityScope
	ObservationDomainIdentityToolSurface
	ObservationDomainConfigPath
	ObservationDomainHomePath
)

var observationDomainLabels = [...]string{
	"",
	"prompt",
	"message_graph",
	"model_response",
	"reasoning",
	"tool_schema",
	"tool_arguments",
	"tool_result",
	"query",
	"regex",
	"command",
	"stdout",
	"transcription",
	"path",
	"url",
	"proxy",
	"provider_body",
	"response_header",
	"process_stderr",
	"identity.agent",
	"identity.session",
	"identity.chat",
	"identity.sender",
	"identity.message",
	"identity.turn",
	"identity.tool",
	"identity.tool_call",
	"identity.hook",
	"identity.runtime",
	"identity.account",
	"identity.request",
	"identity.trace",
	"identity.task",
	"identity.topic",
	"identity.space",
	"identity.provider",
	"identity.mcp_server",
	"identity.mcp_tool",
	"identity.audio",
	"error_type",
	"error_text",
	"hook_message",
	"identity.channel",
	"identity.model",
	"identity.workflow",
	"identity.skill",
	"identity.route",
	"identity.context_manager",
	"identity.hook_stage",
	"identity.hook_action",
	"identity.runtime_event_kind",
	"panic_type",
	"identity.workspace",
	"identity.worker",
	"identity.prompt_part",
	"identity.prompt_source",
	"identity.prompt_layer",
	"identity.prompt_slot",
	"identity.route_agent",
	"identity.route_channel",
	"identity.route_session",
	"identity.target_channel",
	"identity.provider_model",
	"identity.light_model",
	"identity.parent_turn",
	"identity.child_turn",
	"identity.reason",
	"identity.scope",
	"identity.tool_surface",
	"config_path",
	"home_path",
}

// ObservationFieldPrefix selects one fixed family of structured log keys.
// It is intentionally an enum so caller-controlled strings cannot become log
// field names.
type ObservationFieldPrefix uint8

const (
	ObservationPrefixPrompt ObservationFieldPrefix = iota + 1
	ObservationPrefixMessageGraph
	ObservationPrefixModelResponse
	ObservationPrefixReasoning
	ObservationPrefixToolSchema
	ObservationPrefixToolArguments
	ObservationPrefixToolResult
	ObservationPrefixQuery
	ObservationPrefixRegex
	ObservationPrefixCommand
	ObservationPrefixStdout
	ObservationPrefixTranscription
	ObservationPrefixPath
	ObservationPrefixURL
	ObservationPrefixProxy
	ObservationPrefixProviderBody
	ObservationPrefixResponseHeader
	ObservationPrefixProcessStderr
	ObservationPrefixIdentityAgent
	ObservationPrefixIdentitySession
	ObservationPrefixIdentityChat
	ObservationPrefixIdentitySender
	ObservationPrefixIdentityMessage
	ObservationPrefixIdentityTurn
	ObservationPrefixIdentityTool
	ObservationPrefixIdentityToolCall
	ObservationPrefixIdentityHook
	ObservationPrefixIdentityRuntime
	ObservationPrefixIdentityAccount
	ObservationPrefixIdentityRequest
	ObservationPrefixIdentityTrace
	ObservationPrefixIdentityTask
	ObservationPrefixIdentityTopic
	ObservationPrefixIdentitySpace
	ObservationPrefixIdentityProvider
	ObservationPrefixIdentityMCPServer
	ObservationPrefixIdentityMCPTool
	ObservationPrefixIdentityAudio
	ObservationPrefixErrorType
	ObservationPrefixErrorText
	ObservationPrefixError
	ObservationPrefixCredential
	ObservationPrefixEnvironment
	ObservationPrefixRequestHeader
	ObservationPrefixAuthorization
	ObservationPrefixCookie
	ObservationPrefixPrivateKey
	// ObservationPrefixHookMessage begins append-only prefixes. They
	// intentionally do not share domain numbers.
	ObservationPrefixHookMessage
	ObservationPrefixIdentityChannel
	ObservationPrefixIdentityModel
	ObservationPrefixIdentityWorkflow
	ObservationPrefixIdentitySkill
	ObservationPrefixIdentityRoute
	ObservationPrefixIdentityContextManager
	ObservationPrefixIdentityHookStage
	ObservationPrefixIdentityHookAction
	ObservationPrefixIdentityRuntimeEventKind
	ObservationPrefixPanic
	ObservationPrefixIdentityWorkspace
	ObservationPrefixIdentityWorker
	ObservationPrefixIdentityPromptPart
	ObservationPrefixIdentityPromptSource
	ObservationPrefixIdentityPromptLayer
	ObservationPrefixIdentityPromptSlot
	ObservationPrefixIdentityRouteAgent
	ObservationPrefixIdentityRouteChannel
	ObservationPrefixIdentityRouteSession
	ObservationPrefixIdentityTargetChannel
	ObservationPrefixIdentityProviderModel
	ObservationPrefixIdentityLightModel
	ObservationPrefixIdentityParentTurn
	ObservationPrefixIdentityChildTurn
	ObservationPrefixIdentityReason
	ObservationPrefixIdentityScope
	ObservationPrefixIdentityToolSurface
	ObservationPrefixConfigPath
	ObservationPrefixHomePath
)

var observationPrefixLabels = [...]string{
	"",
	"prompt",
	"message_graph",
	"model_response",
	"reasoning",
	"tool_schema",
	"tool_arguments",
	"tool_result",
	"query",
	"regex",
	"command",
	"stdout",
	"transcription",
	"path",
	"url",
	"proxy",
	"provider_body",
	"response_header",
	"process_stderr",
	"identity_agent",
	"identity_session",
	"identity_chat",
	"identity_sender",
	"identity_message",
	"identity_turn",
	"identity_tool",
	"identity_tool_call",
	"identity_hook",
	"identity_runtime",
	"identity_account",
	"identity_request",
	"identity_trace",
	"identity_task",
	"identity_topic",
	"identity_space",
	"identity_provider",
	"identity_mcp_server",
	"identity_mcp_tool",
	"identity_audio",
	"error_type",
	"error_text",
	"error",
	"credential",
	"environment",
	"request_header",
	"authorization",
	"cookie",
	"private_key",
	"hook_message",
	"identity_channel",
	"identity_model",
	"identity_workflow",
	"identity_skill",
	"identity_route",
	"identity_context_manager",
	"identity_hook_stage",
	"identity_hook_action",
	"identity_runtime_event_kind",
	"panic",
	"identity_workspace",
	"identity_worker",
	"identity_prompt_part",
	"identity_prompt_source",
	"identity_prompt_layer",
	"identity_prompt_slot",
	"identity_route_agent",
	"identity_route_channel",
	"identity_route_session",
	"identity_target_channel",
	"identity_provider_model",
	"identity_light_model",
	"identity_parent_turn",
	"identity_child_turn",
	"identity_reason",
	"identity_scope",
	"identity_tool_surface",
	"config_path",
	"home_path",
}

// ErrorClass is a trusted, fixed classification supplied by an error owner.
// ObserveErrorType never derives a class by invoking methods on the error.
type ErrorClass uint8

const (
	ErrorClassNone ErrorClass = iota
	ErrorClassCanceled
	ErrorClassDeadline
	ErrorClassValidation
	ErrorClassNotFound
	ErrorClassPermission
	ErrorClassConflict
	ErrorClassTransport
	ErrorClassProvider
	ErrorClassInternal
	ErrorClassUnknown
)

var errorClassLabels = [...]string{
	"none",
	"canceled",
	"deadline",
	"validation",
	"not_found",
	"permission",
	"conflict",
	"transport",
	"provider",
	"internal",
	"unknown",
}

// PresenceClass identifies sensitive values that must expose only presence
// and caller-known counts, never a digest or content.
type PresenceClass uint8

const (
	PresenceClassCredential PresenceClass = iota + 1
	PresenceClassEnvironment
	PresenceClassRequestHeader
	PresenceClassAuthorization
	PresenceClassCookie
	PresenceClassPrivateKey
)

var presenceClassLabels = [...]string{
	"",
	"credential",
	"environment",
	"request_header",
	"authorization",
	"cookie",
	"private_key",
}

// Observation is a detached scalar description of sensitive data. Construct
// observations through the Observe functions; ObservationFields validates the
// value again so a mutated or hand-built Observation cannot inject log text.
type Observation struct {
	Class      string
	Bytes      int
	Runes      int
	UTF8Valid  bool
	Count      int
	Digest     string
	State      string
	ReasonCode string

	expectedPrefix ObservationFieldPrefix
	digestRequired bool
	valid          bool
	integrity      observationIntegrity
}

type observationIntegrity struct {
	class      string
	bytes      int
	runes      int
	utf8Valid  bool
	count      int
	digest     string
	state      string
	reasonCode string
}

// ObserveText describes exact string bytes without retaining the string.
func ObserveText(domain ObservationDomain, value string) Observation {
	prefix, ok := prefixForDomain(domain)
	if !ok || domain == ObservationDomainErrorType || domain == ObservationDomainPanicType {
		return unavailableObservation(ObservationPrefixError, "unknown", reasonInvalidDomain)
	}
	if len(value) > maxObservationBytes {
		return unavailableObservation(prefix, "text", reasonByteLimit)
	}
	validUTF8 := utf8.ValidString(value)
	runes := 0
	if validUTF8 {
		runes = utf8.RuneCountInString(value)
	}
	return completeObservation(
		prefix,
		"text",
		len(value),
		runes,
		validUTF8,
		1,
		observationDigest(domain, "text", []byte(value)),
		true,
	)
}

// ObserveBytes describes exact bytes without retaining the byte slice.
func ObserveBytes(domain ObservationDomain, value []byte) Observation {
	prefix, ok := prefixForDomain(domain)
	if !ok || domain == ObservationDomainErrorType || domain == ObservationDomainPanicType {
		return unavailableObservation(ObservationPrefixError, "unknown", reasonInvalidDomain)
	}
	typeLabel := "bytes"
	class := "bytes"
	count := 1
	if value == nil {
		typeLabel = "bytes:nil"
		class = "bytes_nil"
		count = 0
	}
	if len(value) > maxObservationBytes {
		return unavailableObservation(prefix, class, reasonByteLimit)
	}
	validUTF8 := utf8.Valid(value)
	runes := 0
	if validUTF8 {
		runes = utf8.RuneCount(value)
	}
	return completeObservation(
		prefix,
		class,
		len(value),
		runes,
		validUTF8,
		count,
		observationDigest(domain, typeLabel, value),
		true,
	)
}

// ObservePath classifies a path lexically and digests its exact bytes. It does
// not resolve, authorize, stat, or follow the path.
func ObservePath(value string) Observation {
	return observePath(ObservationDomainPath, ObservationPrefixPath, value)
}

// ObserveConfigPath classifies a gateway config path without sharing a field
// prefix or digest domain with another path in the same safe record.
func ObserveConfigPath(value string) Observation {
	return observePath(ObservationDomainConfigPath, ObservationPrefixConfigPath, value)
}

// ObserveHomePath classifies a gateway home path without sharing a field
// prefix or digest domain with another path in the same safe record.
func ObserveHomePath(value string) Observation {
	return observePath(ObservationDomainHomePath, ObservationPrefixHomePath, value)
}

func observePath(
	domain ObservationDomain,
	prefix ObservationFieldPrefix,
	value string,
) Observation {
	if len(value) > maxObservationBytes {
		return unavailableObservation(prefix, "unknown", reasonByteLimit)
	}
	class := classifyObservationPath(value)
	validUTF8 := utf8.ValidString(value)
	runes := 0
	if validUTF8 {
		runes = utf8.RuneCountInString(value)
	}
	count := 1
	if value == "" {
		count = 0
	}
	return completeObservation(
		prefix,
		class,
		len(value),
		runes,
		validUTF8,
		count,
		observationDigest(domain, "path", []byte(value)),
		true,
	)
}

// ObserveURL reports only a fixed scheme/validity class and an exact digest.
// No URL component is retained in the Observation.
func ObserveURL(value string) Observation {
	if len(value) > maxObservationBytes {
		return unavailableObservation(ObservationPrefixURL, "unknown", reasonByteLimit)
	}
	class := classifyObservationURL(value)
	validUTF8 := utf8.ValidString(value)
	runes := 0
	if validUTF8 {
		runes = utf8.RuneCountInString(value)
	}
	count := 1
	if value == "" {
		count = 0
	}
	return completeObservation(
		ObservationPrefixURL,
		class,
		len(value),
		runes,
		validUTF8,
		count,
		observationDigest(ObservationDomainURL, "url", []byte(value)),
		true,
	)
}

// ObserveIdentity describes one configured/runtime identity under its exact
// identity domain. Non-identity domains fail closed.
func ObserveIdentity(domain ObservationDomain, value string) Observation {
	prefix, ok := identityPrefixForDomain(domain)
	if !ok {
		return unavailableObservation(ObservationPrefixError, "unknown", reasonInvalidDomain)
	}
	class := "present"
	count := 1
	if value == "" {
		class = "empty"
		count = 0
	}
	if len(value) > maxObservationBytes {
		return unavailableObservation(prefix, class, reasonByteLimit)
	}
	validUTF8 := utf8.ValidString(value)
	runes := 0
	if validUTF8 {
		runes = utf8.RuneCountInString(value)
	}
	return completeObservation(
		prefix,
		class,
		len(value),
		runes,
		validUTF8,
		count,
		observationDigest(domain, "identity", []byte(value)),
		true,
	)
}

// ObservePresence reports caller-known metadata for sensitive values that must
// not receive a digest. A positive count encodes presence, including for an
// empty value; absence requires both count and bytes to be zero.
func ObservePresence(class PresenceClass, present bool, count, bytes int) Observation {
	label, prefix, ok := presenceClass(class)
	if !ok {
		return unavailableObservation(ObservationPrefixError, "unknown", reasonInvalidBound)
	}
	if count < 0 || bytes < 0 || present && count == 0 ||
		!present && (count != 0 || bytes != 0) {
		return unavailableObservation(prefix, label, reasonInvalidBound)
	}
	return completeObservation(prefix, label, bytes, 0, false, count, "", false)
}

// ObserveErrorType observes only caller-supplied class and concrete type
// identity. It deliberately invokes no methods on err.
func ObserveErrorType(class ErrorClass, err error) (observation Observation) {
	classLabel, ok := errorClassLabel(class)
	if !ok {
		return unavailableObservation(ObservationPrefixError, "unknown", reasonInvalidBound)
	}
	if err == nil {
		return completeObservation(
			ObservationPrefixError,
			"none",
			0,
			0,
			false,
			0,
			"",
			false,
		)
	}

	defer func() {
		if recover() != nil {
			observation = unavailableObservation(
				ObservationPrefixError,
				classLabel,
				reasonInternalPanic,
			)
		}
	}()

	typeOf := reflect.TypeOf(err)
	pointerDepth := 0
	for typeOf.Kind() == reflect.Pointer {
		pointerDepth++
		if pointerDepth > maxObservationDepth {
			return unavailableObservation(
				ObservationPrefixError,
				classLabel,
				reasonInvalidBound,
			)
		}
		typeOf = typeOf.Elem()
	}
	if typeOf.Name() == "" {
		return unavailableObservation(
			ObservationPrefixError,
			classLabel,
			reasonUnnamedError,
		)
	}

	payload := make([]byte, 1, 1+16+len(typeOf.PkgPath())+len(typeOf.Name()))
	payload[0] = byte(pointerDepth)
	payload = appendFramedBytes(payload, []byte(typeOf.PkgPath()))
	payload = appendFramedBytes(payload, []byte(typeOf.Name()))
	if len(payload) > maxObservationBytes {
		return unavailableObservation(
			ObservationPrefixError,
			classLabel,
			reasonByteLimit,
		)
	}

	count := 1
	value := reflect.ValueOf(err)
	if isNilableKind(value.Kind()) && value.IsNil() {
		count = 0
	}
	return completeObservation(
		ObservationPrefixError,
		classLabel,
		len(payload),
		0,
		false,
		count,
		observationDigest(ObservationDomainErrorType, "error:type", payload),
		true,
	)
}

// ObservePanic observes only a recovered value's concrete type identity. It
// never formats the value or invokes String, Error, Format, or other methods.
func ObservePanic(recovered any) (observation Observation) {
	if recovered == nil {
		return completeObservation(
			ObservationPrefixPanic,
			"none",
			0,
			0,
			false,
			0,
			"",
			false,
		)
	}

	defer func() {
		if recover() != nil {
			observation = unavailableObservation(
				ObservationPrefixPanic,
				"panic",
				reasonInternalPanic,
			)
		}
	}()

	typeOf := reflect.TypeOf(recovered)
	pointerDepth := 0
	for typeOf.Kind() == reflect.Pointer {
		pointerDepth++
		if pointerDepth > maxObservationDepth {
			return unavailableObservation(
				ObservationPrefixPanic,
				"panic",
				reasonInvalidBound,
			)
		}
		typeOf = typeOf.Elem()
	}
	if typeOf.Name() == "" {
		return unavailableObservation(
			ObservationPrefixPanic,
			"panic",
			reasonUnnamedPanic,
		)
	}

	payload := make([]byte, 1, 1+16+len(typeOf.PkgPath())+len(typeOf.Name()))
	payload[0] = byte(pointerDepth)
	payload = appendFramedBytes(payload, []byte(typeOf.PkgPath()))
	payload = appendFramedBytes(payload, []byte(typeOf.Name()))
	if len(payload) > maxObservationBytes {
		return unavailableObservation(
			ObservationPrefixPanic,
			"panic",
			reasonByteLimit,
		)
	}

	count := 1
	value := reflect.ValueOf(recovered)
	if isNilableKind(value.Kind()) && value.IsNil() {
		count = 0
	}
	return completeObservation(
		ObservationPrefixPanic,
		"panic",
		len(payload),
		0,
		false,
		count,
		observationDigest(ObservationDomainPanicType, "panic:type", payload),
		true,
	)
}

// ObserveJSONValue canonicalizes an already detached, exclusively owned
// JSON-compatible graph. Concurrent mutation by the caller is unsupported.
func ObserveJSONValue(domain ObservationDomain, value any) (observation Observation) {
	prefix, ok := prefixForDomain(domain)
	if !ok || domain == ObservationDomainErrorType || domain == ObservationDomainPanicType {
		return unavailableObservation(ObservationPrefixError, "unknown", reasonInvalidDomain)
	}

	defer func() {
		if recover() != nil {
			observation = unavailableObservation(prefix, "json", reasonInternalPanic)
		}
	}()

	encoder := observationGraphEncoder{active: make(map[observationVisit]struct{})}
	canonical, reason := encoder.encode(value, 1)
	if reason != "" {
		return unavailableObservation(prefix, "json", reason)
	}
	return completeObservation(
		prefix,
		"json",
		len(canonical),
		0,
		false,
		topLevelObservationCount(value),
		observationDigest(domain, "json-graph-v1", canonical),
		true,
	)
}

// ObservationFields returns exactly eight fixed scalar fields. It fails closed
// when the prefix does not match the observation or public fields were changed.
func ObservationFields(prefix ObservationFieldPrefix, observation Observation) map[string]any {
	label, prefixOK := observationPrefixLabel(prefix)
	if !prefixOK {
		prefix = ObservationPrefixError
		label = observationPrefixLabels[prefix]
		observation = unavailableObservation(prefix, "unknown", reasonInvalidPrefix)
	} else if !observation.valid || observation.expectedPrefix != prefix {
		observation = unavailableObservation(prefix, "unknown", reasonInvalidPrefix)
	} else if !validObservation(observation) {
		observation = unavailableObservation(prefix, "unknown", reasonInvalidBound)
	}

	return map[string]any{
		label + "_class":       observation.Class,
		label + "_bytes":       int64(observation.Bytes),
		label + "_runes":       int64(observation.Runes),
		label + "_utf8_valid":  observation.UTF8Valid,
		label + "_count":       int64(observation.Count),
		label + "_digest":      observation.Digest,
		label + "_state":       observation.State,
		label + "_reason_code": observation.ReasonCode,
	}
}

func completeObservation(
	prefix ObservationFieldPrefix,
	class string,
	bytes, runes int,
	validUTF8 bool,
	count int,
	digest string,
	digestRequired bool,
) Observation {
	observation := Observation{
		Class:          class,
		Bytes:          bytes,
		Runes:          runes,
		UTF8Valid:      validUTF8,
		Count:          count,
		Digest:         digest,
		State:          observationStateComplete,
		expectedPrefix: prefix,
		digestRequired: digestRequired,
		valid:          true,
	}
	sealObservation(&observation)
	return observation
}

func unavailableObservation(
	prefix ObservationFieldPrefix,
	class, reason string,
) Observation {
	observation := Observation{
		Class:          class,
		State:          observationStateUnavailable,
		ReasonCode:     reason,
		expectedPrefix: prefix,
		valid:          true,
	}
	sealObservation(&observation)
	return observation
}

func sealObservation(observation *Observation) {
	observation.integrity = observationIntegrity{
		class:      observation.Class,
		bytes:      observation.Bytes,
		runes:      observation.Runes,
		utf8Valid:  observation.UTF8Valid,
		count:      observation.Count,
		digest:     observation.Digest,
		state:      observation.State,
		reasonCode: observation.ReasonCode,
	}
}

func observationIntegrityValid(observation Observation) bool {
	return observation.integrity == (observationIntegrity{
		class:      observation.Class,
		bytes:      observation.Bytes,
		runes:      observation.Runes,
		utf8Valid:  observation.UTF8Valid,
		count:      observation.Count,
		digest:     observation.Digest,
		state:      observation.State,
		reasonCode: observation.ReasonCode,
	})
}

func observationDigest(domain ObservationDomain, typeLabel string, value []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(observationDigestPreamble))
	writeObservationFrame(hash, []byte(observationDomainLabels[domain]))
	writeObservationFrame(hash, []byte(typeLabel))
	writeObservationFrame(hash, value)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

type observationHashWriter interface {
	Write(value []byte) (int, error)
}

func writeObservationFrame(writer observationHashWriter, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}

func appendFramedBytes(dst, value []byte) []byte {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	dst = append(dst, length[:]...)
	return append(dst, value...)
}

func validDomain(domain ObservationDomain) bool {
	return domain > 0 && int(domain) < len(observationDomainLabels)
}

func prefixForDomain(domain ObservationDomain) (ObservationFieldPrefix, bool) {
	if !validDomain(domain) {
		return 0, false
	}
	// Domain constants and their corresponding prefix constants deliberately
	// share the same declared order through error_text.
	if domain <= ObservationDomainErrorText {
		return ObservationFieldPrefix(domain), true
	}
	switch domain {
	case ObservationDomainHookMessage:
		return ObservationPrefixHookMessage, true
	case ObservationDomainIdentityChannel:
		return ObservationPrefixIdentityChannel, true
	case ObservationDomainIdentityModel:
		return ObservationPrefixIdentityModel, true
	case ObservationDomainIdentityWorkflow:
		return ObservationPrefixIdentityWorkflow, true
	case ObservationDomainIdentitySkill:
		return ObservationPrefixIdentitySkill, true
	case ObservationDomainIdentityRoute:
		return ObservationPrefixIdentityRoute, true
	case ObservationDomainIdentityContextManager:
		return ObservationPrefixIdentityContextManager, true
	case ObservationDomainIdentityHookStage:
		return ObservationPrefixIdentityHookStage, true
	case ObservationDomainIdentityHookAction:
		return ObservationPrefixIdentityHookAction, true
	case ObservationDomainIdentityRuntimeEventKind:
		return ObservationPrefixIdentityRuntimeEventKind, true
	case ObservationDomainPanicType:
		return ObservationPrefixPanic, true
	case ObservationDomainIdentityWorkspace:
		return ObservationPrefixIdentityWorkspace, true
	case ObservationDomainIdentityWorker:
		return ObservationPrefixIdentityWorker, true
	case ObservationDomainIdentityPromptPart:
		return ObservationPrefixIdentityPromptPart, true
	case ObservationDomainIdentityPromptSource:
		return ObservationPrefixIdentityPromptSource, true
	case ObservationDomainIdentityPromptLayer:
		return ObservationPrefixIdentityPromptLayer, true
	case ObservationDomainIdentityPromptSlot:
		return ObservationPrefixIdentityPromptSlot, true
	case ObservationDomainIdentityRouteAgent:
		return ObservationPrefixIdentityRouteAgent, true
	case ObservationDomainIdentityRouteChannel:
		return ObservationPrefixIdentityRouteChannel, true
	case ObservationDomainIdentityRouteSession:
		return ObservationPrefixIdentityRouteSession, true
	case ObservationDomainIdentityTargetChannel:
		return ObservationPrefixIdentityTargetChannel, true
	case ObservationDomainIdentityProviderModel:
		return ObservationPrefixIdentityProviderModel, true
	case ObservationDomainIdentityLightModel:
		return ObservationPrefixIdentityLightModel, true
	case ObservationDomainIdentityParentTurn:
		return ObservationPrefixIdentityParentTurn, true
	case ObservationDomainIdentityChildTurn:
		return ObservationPrefixIdentityChildTurn, true
	case ObservationDomainIdentityReason:
		return ObservationPrefixIdentityReason, true
	case ObservationDomainIdentityScope:
		return ObservationPrefixIdentityScope, true
	case ObservationDomainIdentityToolSurface:
		return ObservationPrefixIdentityToolSurface, true
	case ObservationDomainConfigPath:
		return ObservationPrefixConfigPath, true
	case ObservationDomainHomePath:
		return ObservationPrefixHomePath, true
	default:
		return 0, false
	}
}

func identityPrefixForDomain(domain ObservationDomain) (ObservationFieldPrefix, bool) {
	if domain >= ObservationDomainIdentityAgent && domain <= ObservationDomainIdentityAudio {
		return prefixForDomain(domain)
	}
	switch domain {
	case ObservationDomainIdentityChannel,
		ObservationDomainIdentityModel,
		ObservationDomainIdentityWorkflow,
		ObservationDomainIdentitySkill,
		ObservationDomainIdentityRoute,
		ObservationDomainIdentityContextManager,
		ObservationDomainIdentityHookStage,
		ObservationDomainIdentityHookAction,
		ObservationDomainIdentityRuntimeEventKind,
		ObservationDomainIdentityWorkspace,
		ObservationDomainIdentityWorker,
		ObservationDomainIdentityPromptPart,
		ObservationDomainIdentityPromptSource,
		ObservationDomainIdentityPromptLayer,
		ObservationDomainIdentityPromptSlot,
		ObservationDomainIdentityRouteAgent,
		ObservationDomainIdentityRouteChannel,
		ObservationDomainIdentityRouteSession,
		ObservationDomainIdentityTargetChannel,
		ObservationDomainIdentityProviderModel,
		ObservationDomainIdentityLightModel,
		ObservationDomainIdentityParentTurn,
		ObservationDomainIdentityChildTurn,
		ObservationDomainIdentityReason,
		ObservationDomainIdentityScope,
		ObservationDomainIdentityToolSurface:
		return prefixForDomain(domain)
	default:
		return 0, false
	}
}

func observationPrefixLabel(prefix ObservationFieldPrefix) (string, bool) {
	if prefix == 0 || int(prefix) >= len(observationPrefixLabels) {
		return "", false
	}
	return observationPrefixLabels[prefix], true
}

func errorClassLabel(class ErrorClass) (string, bool) {
	if int(class) < 0 || int(class) >= len(errorClassLabels) {
		return "", false
	}
	return errorClassLabels[class], true
}

func presenceClass(class PresenceClass) (string, ObservationFieldPrefix, bool) {
	if class == 0 || int(class) >= len(presenceClassLabels) {
		return "", 0, false
	}
	return presenceClassLabels[class],
		ObservationPrefixCredential + ObservationFieldPrefix(class-1), true
}

func validObservation(observation Observation) bool {
	if !observationIntegrityValid(observation) || observation.Bytes < 0 ||
		observation.Runes < 0 || observation.Count < 0 ||
		!validObservationClass(observation.Class) {
		return false
	}
	switch observation.State {
	case observationStateComplete:
		if observation.ReasonCode != "" {
			return false
		}
		if observation.digestRequired {
			return validObservationDigest(observation.Digest)
		}
		return observation.Digest == ""
	case observationStateUnavailable:
		return validUnavailableReason(observation.ReasonCode) &&
			observation.Bytes == 0 && observation.Runes == 0 &&
			!observation.UTF8Valid && observation.Count == 0 &&
			observation.Digest == ""
	default:
		return false
	}
}

func validObservationDigest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range value[len("sha256:"):] {
		if char < '0' || char > '9' && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validObservationClass(class string) bool {
	switch class {
	case "unknown", "text", "bytes_nil", "bytes", "json",
		"empty", "present", "relative", "absolute", "media_ref",
		"frozen_ref", "url_like", "invalid", "http", "https", "ws",
		"wss", "file", "other", "none", "canceled", "deadline",
		"validation", "not_found", "permission", "conflict", "transport",
		"provider", "internal", "credential", "environment",
		"request_header", "authorization", "cookie", "private_key", "panic":
		return true
	default:
		return false
	}
}

func validUnavailableReason(reason string) bool {
	switch reason {
	case reasonInvalidDomain, reasonInvalidPrefix, reasonInvalidBound,
		reasonUnsupportedType, reasonCycle, reasonDepthLimit, reasonNodeLimit,
		reasonMemberLimit, reasonByteLimit, reasonInvalidNumber,
		reasonNonfiniteFloat, reasonUnnamedError, reasonUnnamedPanic,
		reasonInternalPanic:
		return true
	default:
		return false
	}
}

func classifyObservationPath(value string) string {
	if value == "" {
		return "empty"
	}
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return "invalid"
	}
	if strings.HasPrefix(value, "frozen-media://") {
		return "frozen_ref"
	}
	if strings.HasPrefix(value, "media://") {
		return "media_ref"
	}
	if filepath.IsAbs(value) || portableAbsolutePath(value) {
		return "absolute"
	}
	if looksLikeObservationURL(value) {
		return "url_like"
	}
	return "relative"
}

func portableAbsolutePath(value string) bool {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) {
		return true
	}
	return len(value) >= 3 && isASCIILetter(value[0]) && value[1] == ':' &&
		(value[2] == '/' || value[2] == '\\')
}

func looksLikeObservationURL(value string) bool {
	colon := strings.IndexByte(value, ':')
	if colon <= 0 || colon+2 >= len(value) || value[colon+1:colon+3] != "//" {
		return false
	}
	if !isASCIILetter(value[0]) {
		return false
	}
	for index := 1; index < colon; index++ {
		char := value[index]
		if !isASCIILetter(char) && (char < '0' || char > '9') &&
			char != '+' && char != '-' && char != '.' {
			return false
		}
	}
	return true
}

func classifyObservationURL(value string) string {
	if value == "" || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return "invalid"
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return "invalid"
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "http", "https", "ws", "wss":
		if parsed.Host == "" {
			return "invalid"
		}
		return scheme
	case "file":
		return "file"
	default:
		return "other"
	}
}

func isASCIILetter(char byte) bool {
	return char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z'
}

func isNilableKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return true
	default:
		return false
	}
}

type observationVisit struct {
	kind reflect.Kind
	ptr  uintptr
	len  int
	cap  int
}

type observationGraphEncoder struct {
	nodes  int
	active map[observationVisit]struct{}
}

var jsonNumberPattern = regexp.MustCompile(
	`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`,
)

func (encoder *observationGraphEncoder) encode(value any, depth int) ([]byte, string) {
	if depth > maxObservationDepth {
		return nil, reasonDepthLimit
	}
	encoder.nodes++
	if encoder.nodes > maxObservationNodes {
		return nil, reasonNodeLimit
	}

	switch typed := value.(type) {
	case nil:
		return frameObservationNode(0x00, nil)
	case bool:
		payload := []byte{0}
		if typed {
			payload[0] = 1
		}
		return frameObservationNode(0x01, payload)
	case string:
		return frameObservationString(0x02, typed)
	case int:
		return frameObservationSigned(int64(typed))
	case int8:
		return frameObservationSigned(int64(typed))
	case int16:
		return frameObservationSigned(int64(typed))
	case int32:
		return frameObservationSigned(int64(typed))
	case int64:
		return frameObservationSigned(typed)
	case uint:
		return frameObservationUnsigned(uint64(typed))
	case uint8:
		return frameObservationUnsigned(uint64(typed))
	case uint16:
		return frameObservationUnsigned(uint64(typed))
	case uint32:
		return frameObservationUnsigned(uint64(typed))
	case uint64:
		return frameObservationUnsigned(typed)
	case float32:
		return frameObservationFloat(float64(typed))
	case float64:
		return frameObservationFloat(typed)
	case json.Number:
		if len(typed) > maxObservationBytes-9 {
			return nil, reasonByteLimit
		}
		if !jsonNumberPattern.MatchString(string(typed)) {
			return nil, reasonInvalidNumber
		}
		return frameObservationString(0x06, string(typed))
	case []any:
		return encoder.encodeSlice(typed, depth)
	case map[string]any:
		return encoder.encodeMap(typed, depth)
	default:
		return nil, reasonUnsupportedType
	}
}

func (encoder *observationGraphEncoder) encodeSlice(
	value []any,
	depth int,
) ([]byte, string) {
	if len(value) > maxObservationMembers {
		return nil, reasonMemberLimit
	}
	payload := make([]byte, 9)
	if value != nil {
		payload[0] = 1
	}
	binary.BigEndian.PutUint64(payload[1:], uint64(len(value)))
	if value == nil {
		return frameObservationNode(0x07, payload)
	}

	visit := observationVisit{
		kind: reflect.Slice,
		ptr:  reflect.ValueOf(value).Pointer(),
		len:  len(value),
		cap:  cap(value),
	}
	if _, exists := encoder.active[visit]; exists {
		return nil, reasonCycle
	}
	encoder.active[visit] = struct{}{}
	defer delete(encoder.active, visit)

	for _, item := range value {
		encoded, reason := encoder.encode(item, depth+1)
		if reason != "" {
			return nil, reason
		}
		var ok bool
		payload, ok = appendObservationBounded(payload, encoded)
		if !ok {
			return nil, reasonByteLimit
		}
	}
	return frameObservationNode(0x07, payload)
}

func (encoder *observationGraphEncoder) encodeMap(
	value map[string]any,
	depth int,
) ([]byte, string) {
	if len(value) > maxObservationMembers {
		return nil, reasonMemberLimit
	}
	payload := make([]byte, 9)
	if value != nil {
		payload[0] = 1
	}
	binary.BigEndian.PutUint64(payload[1:], uint64(len(value)))
	if value == nil {
		return frameObservationNode(0x08, payload)
	}

	visit := observationVisit{kind: reflect.Map, ptr: reflect.ValueOf(value).Pointer()}
	if _, exists := encoder.active[visit]; exists {
		return nil, reasonCycle
	}
	encoder.active[visit] = struct{}{}
	defer delete(encoder.active, visit)

	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		encodedKey, reason := encoder.encode(key, depth+1)
		if reason != "" {
			return nil, reason
		}
		encodedValue, reason := encoder.encode(value[key], depth+1)
		if reason != "" {
			return nil, reason
		}
		var ok bool
		payload, ok = appendObservationBounded(payload, encodedKey)
		if !ok {
			return nil, reasonByteLimit
		}
		payload, ok = appendObservationBounded(payload, encodedValue)
		if !ok {
			return nil, reasonByteLimit
		}
	}
	return frameObservationNode(0x08, payload)
}

func frameObservationSigned(value int64) ([]byte, string) {
	payload := make([]byte, 8)
	binary.BigEndian.PutUint64(payload, uint64(value))
	return frameObservationNode(0x03, payload)
}

func frameObservationUnsigned(value uint64) ([]byte, string) {
	payload := make([]byte, 8)
	binary.BigEndian.PutUint64(payload, value)
	return frameObservationNode(0x04, payload)
}

func frameObservationFloat(value float64) ([]byte, string) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, reasonNonfiniteFloat
	}
	payload := make([]byte, 8)
	binary.BigEndian.PutUint64(payload, math.Float64bits(value))
	return frameObservationNode(0x05, payload)
}

func frameObservationString(tag byte, value string) ([]byte, string) {
	if len(value) > maxObservationBytes-9 {
		return nil, reasonByteLimit
	}
	framed := make([]byte, 9, 9+len(value))
	framed[0] = tag
	binary.BigEndian.PutUint64(framed[1:], uint64(len(value)))
	framed = append(framed, value...)
	return framed, ""
}

func frameObservationNode(tag byte, payload []byte) ([]byte, string) {
	if len(payload) > maxObservationBytes-9 {
		return nil, reasonByteLimit
	}
	framed := make([]byte, 9, 9+len(payload))
	framed[0] = tag
	binary.BigEndian.PutUint64(framed[1:], uint64(len(payload)))
	framed = append(framed, payload...)
	return framed, ""
}

func appendObservationBounded(dst, value []byte) ([]byte, bool) {
	if len(value) > maxObservationBytes-len(dst) {
		return dst, false
	}
	return append(dst, value...), true
}

func topLevelObservationCount(value any) int {
	switch typed := value.(type) {
	case nil:
		return 0
	case []any:
		return len(typed)
	case map[string]any:
		return len(typed)
	default:
		return 1
	}
}

// compile-time guards: domain and domain-derived prefix order must remain
// one-to-one through error_text.
var (
	_ [int(ObservationPrefixErrorText) - int(ObservationDomainErrorText)]struct{}
	_ [int(ObservationDomainErrorText) - int(ObservationPrefixErrorText)]struct{}
	_ = runtime.GOOS // documents that filepath.IsAbs uses the native GOOS branch
)
