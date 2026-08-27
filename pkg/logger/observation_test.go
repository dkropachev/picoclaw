package logger

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestObserveTextGoldenAndDomainSeparation(t *testing.T) {
	observation := ObserveText(ObservationDomainPrompt, "hello")
	if observation.Digest != "sha256:fded817f7b22168f96d6140f69c06306c84e0e6c2468ab8af491eb5579575a41" {
		t.Fatalf("digest = %q", observation.Digest)
	}
	if observation.Class != "text" || observation.Bytes != 5 ||
		observation.Runes != 5 || !observation.UTF8Valid || observation.Count != 1 ||
		observation.State != observationStateComplete || observation.ReasonCode != "" {
		t.Fatalf("observation = %#v", observation)
	}

	digests := make(map[string]ObservationDomain)
	for domain := ObservationDomainPrompt; domain <= ObservationDomainErrorText; domain++ {
		if domain == ObservationDomainErrorType {
			continue
		}
		got := ObserveText(domain, "same-value")
		if prior, exists := digests[got.Digest]; exists {
			t.Fatalf("domains %d and %d share digest %q", prior, domain, got.Digest)
		}
		digests[got.Digest] = domain
		prefix, ok := prefixForDomain(domain)
		if !ok || got.expectedPrefix != prefix {
			t.Fatalf("domain %d prefix = %d, %v", domain, got.expectedPrefix, ok)
		}
	}

	invalid := ObserveText(0, "canary")
	assertUnavailableObservation(t, invalid, reasonInvalidDomain)
	invalid = ObserveText(ObservationDomainErrorType, "canary")
	assertUnavailableObservation(t, invalid, reasonInvalidDomain)
}

func TestObserveTextAndBytesMetadataAndTypeSeparation(t *testing.T) {
	tests := []struct {
		name      string
		got       Observation
		class     string
		bytes     int
		runes     int
		validUTF8 bool
		count     int
	}{
		{
			name: "empty text", got: ObserveText(ObservationDomainPrompt, ""),
			class: "text", validUTF8: true, count: 1,
		},
		{
			name: "unicode text", got: ObserveText(ObservationDomainPrompt, "a🔒"),
			class: "text", bytes: 5, runes: 2, validUTF8: true, count: 1,
		},
		{
			name: "invalid text", got: ObserveText(ObservationDomainPrompt, string([]byte{0xff})),
			class: "text", bytes: 1, count: 1,
		},
		{
			name: "nil bytes", got: ObserveBytes(ObservationDomainPrompt, nil),
			class: "bytes_nil", validUTF8: true,
		},
		{
			name: "empty bytes", got: ObserveBytes(ObservationDomainPrompt, []byte{}),
			class: "bytes", validUTF8: true, count: 1,
		},
		{
			name: "invalid bytes", got: ObserveBytes(ObservationDomainPrompt, []byte{0xff}),
			class: "bytes", bytes: 1, count: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got.Class != test.class || test.got.Bytes != test.bytes ||
				test.got.Runes != test.runes || test.got.UTF8Valid != test.validUTF8 ||
				test.got.Count != test.count || !validObservationDigest(test.got.Digest) {
				t.Fatalf("observation = %#v", test.got)
			}
		})
	}
	if ObserveText(ObservationDomainPrompt, "").Digest ==
		ObserveBytes(ObservationDomainPrompt, []byte{}).Digest {
		t.Fatal("text and bytes digest types collided")
	}
	if ObserveBytes(ObservationDomainPrompt, nil).Digest ==
		ObserveBytes(ObservationDomainPrompt, []byte{}).Digest {
		t.Fatal("nil and non-nil empty bytes collided")
	}
}

func TestObserveIdentityRequiresIdentityDomain(t *testing.T) {
	empty := ObserveIdentity(ObservationDomainIdentitySession, "")
	present := ObserveIdentity(ObservationDomainIdentitySession, "session-canary")
	if empty.Class != "empty" || empty.Count != 0 || empty.Bytes != 0 ||
		present.Class != "present" || present.Count != 1 || present.Bytes != 14 ||
		empty.Digest == present.Digest {
		t.Fatalf("empty=%#v present=%#v", empty, present)
	}
	if got := ObserveIdentity(ObservationDomainPrompt, "secret"); got.ReasonCode != reasonInvalidDomain {
		t.Fatalf("non-identity observation = %#v", got)
	}
}

func TestObservePathClassification(t *testing.T) {
	tests := []struct {
		value string
		class string
	}{
		{"", "empty"},
		{"notes/file.txt", "relative"},
		{"../notes", "relative"},
		{"/srv/private/file", "absolute"},
		{`C:\\Users\\private`, "absolute"},
		{`\\\\server\\share\\private`, "absolute"},
		{"media://asset", "media_ref"},
		{"frozen-media://sha256/abc", "frozen_ref"},
		{"https://user:secret@example.test/private?q=secret", "url_like"},
		{"bad\x00path", "invalid"},
		{string([]byte{0xff}), "invalid"},
	}
	for _, test := range tests {
		t.Run(test.class+"/"+test.value, func(t *testing.T) {
			got := ObservePath(test.value)
			if got.Class != test.class || got.Bytes != len(test.value) ||
				got.expectedPrefix != ObservationPrefixPath ||
				!validObservationDigest(got.Digest) {
				t.Fatalf("ObservePath(%q) = %#v", test.value, got)
			}
			if strings.Contains(got.Digest, "secret") {
				t.Fatalf("digest leaked input: %q", got.Digest)
			}
		})
	}
}

func TestObserveURLClassification(t *testing.T) {
	tests := []struct {
		value string
		class string
	}{
		{"", "invalid"},
		{"https://user:secret@example.test/private?q=secret#fragment", "https"},
		{"HTTP://example.test", "http"},
		{"ws://example.test/socket", "ws"},
		{"wss://example.test/socket", "wss"},
		{"file:///tmp/private", "file"},
		{"custom:value", "other"},
		{"https:missing-host", "invalid"},
		{"not-a-url", "invalid"},
		{"https://%zz", "invalid"},
		{"https://example.test/\x00", "invalid"},
		{string([]byte{0xff}), "invalid"},
	}
	for _, test := range tests {
		t.Run(test.class+"/"+test.value, func(t *testing.T) {
			got := ObserveURL(test.value)
			if got.Class != test.class || got.Bytes != len(test.value) ||
				got.expectedPrefix != ObservationPrefixURL ||
				!validObservationDigest(got.Digest) {
				t.Fatalf("ObserveURL(%q) = %#v", test.value, got)
			}
		})
	}
}

func TestObservePresenceBoundsAndNoDigest(t *testing.T) {
	classes := []struct {
		class  PresenceClass
		label  string
		prefix ObservationFieldPrefix
	}{
		{PresenceClassCredential, "credential", ObservationPrefixCredential},
		{PresenceClassEnvironment, "environment", ObservationPrefixEnvironment},
		{PresenceClassRequestHeader, "request_header", ObservationPrefixRequestHeader},
		{PresenceClassAuthorization, "authorization", ObservationPrefixAuthorization},
		{PresenceClassCookie, "cookie", ObservationPrefixCookie},
		{PresenceClassPrivateKey, "private_key", ObservationPrefixPrivateKey},
	}
	for _, test := range classes {
		got := ObservePresence(test.class, true, 2, 37)
		if got.Class != test.label || got.Count != 2 || got.Bytes != 37 ||
			got.Digest != "" || got.expectedPrefix != test.prefix || got.UTF8Valid {
			t.Fatalf("ObservePresence(%d) = %#v", test.class, got)
		}
		fields := ObservationFields(test.prefix, got)
		if fields[test.label+"_class"] != test.label ||
			fields[test.label+"_digest"] != "" {
			t.Fatalf("presence fields = %#v", fields)
		}
	}

	absent := ObservePresence(PresenceClassCredential, false, 0, 0)
	if absent.State != observationStateComplete || absent.Count != 0 || absent.Bytes != 0 {
		t.Fatalf("absent = %#v", absent)
	}
	emptyPresent := ObservePresence(PresenceClassCredential, true, 1, 0)
	if emptyPresent.State != observationStateComplete || emptyPresent.Count != 1 ||
		emptyPresent.Bytes != 0 {
		t.Fatalf("empty present = %#v", emptyPresent)
	}

	invalidPresent := ObservePresence(PresenceClassCredential, true, 0, 0)
	assertUnavailableObservation(t, invalidPresent, reasonInvalidBound)
	if invalidPresent.Class != "credential" ||
		invalidPresent.expectedPrefix != ObservationPrefixCredential {
		t.Fatalf("invalid present lost known class/prefix: %#v", invalidPresent)
	}
	invalidFields := ObservationFields(ObservationPrefixCredential, invalidPresent)
	if invalidFields["credential_state"] != observationStateUnavailable ||
		invalidFields["credential_reason_code"] != reasonInvalidBound {
		t.Fatalf("invalid presence fields = %#v", invalidFields)
	}
	for _, got := range []Observation{
		ObservePresence(0, true, 1, 1),
		ObservePresence(PresenceClassCredential, true, -1, 0),
		ObservePresence(PresenceClassCredential, true, 1, -1),
		ObservePresence(PresenceClassCredential, false, 1, 0),
		ObservePresence(PresenceClassCredential, false, 0, 1),
	} {
		assertUnavailableObservation(t, got, reasonInvalidBound)
	}
}

type observationTestError struct {
	errorCalls  *atomic.Int64
	unwrapCalls *atomic.Int64
}

func (err observationTestError) Error() string {
	err.errorCalls.Add(1)
	panic("Error must not be called")
}

func (err observationTestError) Unwrap() error {
	err.unwrapCalls.Add(1)
	panic("Unwrap must not be called")
}

type observationBlockingError struct {
	called *atomic.Bool
	block  <-chan struct{}
}

func (err *observationBlockingError) Error() string {
	err.called.Store(true)
	<-err.block
	return "unblocked"
}

type observationTypedNilError struct{}

func (*observationTypedNilError) Error() string { return "must not be called" }

func TestObserveErrorTypeNeverInvokesErrorMethods(t *testing.T) {
	var errorCalls atomic.Int64
	var unwrapCalls atomic.Int64
	got := ObserveErrorType(ErrorClassProvider, observationTestError{
		errorCalls: &errorCalls, unwrapCalls: &unwrapCalls,
	})
	if errorCalls.Load() != 0 || unwrapCalls.Load() != 0 {
		t.Fatalf("error methods called: Error=%d Unwrap=%d", errorCalls.Load(), unwrapCalls.Load())
	}
	if got.Class != "provider" || got.Count != 1 || got.Bytes == 0 ||
		!validObservationDigest(got.Digest) || got.expectedPrefix != ObservationPrefixError {
		t.Fatalf("error observation = %#v", got)
	}

	block := make(chan struct{})
	var called atomic.Bool
	blocking := &observationBlockingError{called: &called, block: block}
	done := make(chan Observation, 1)
	go func() { done <- ObserveErrorType(ErrorClassTransport, blocking) }()
	select {
	case observed := <-done:
		if observed.Class != "transport" || called.Load() {
			t.Fatalf("blocking error observation = %#v, called=%v", observed, called.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("ObserveErrorType invoked a blocking error method")
	}
}

func TestObserveErrorTypeNilTypedNilUnnamedAndInvalidClass(t *testing.T) {
	nilObservation := ObserveErrorType(ErrorClassProvider, nil)
	if nilObservation.Class != "none" || nilObservation.Count != 0 ||
		nilObservation.Bytes != 0 || nilObservation.Digest != "" {
		t.Fatalf("nil error = %#v", nilObservation)
	}

	var typedNil *observationTypedNilError
	typedNilObservation := ObserveErrorType(ErrorClassUnknown, typedNil)
	if typedNilObservation.Class != "unknown" || typedNilObservation.Count != 0 ||
		!validObservationDigest(typedNilObservation.Digest) {
		t.Fatalf("typed nil = %#v", typedNilObservation)
	}

	unnamed := struct{ observationTypedNilError }{}
	unnamedObservation := ObserveErrorType(ErrorClassInternal, &unnamed)
	assertUnavailableObservation(t, unnamedObservation, reasonUnnamedError)

	invalid := ObserveErrorType(ErrorClass(255), errors.New("canary"))
	assertUnavailableObservation(t, invalid, reasonInvalidBound)
}

func TestObserveJSONValueCanonicalTypeGrammar(t *testing.T) {
	domain := ObservationDomainToolArguments
	equal := func(t *testing.T, left, right any) {
		t.Helper()
		leftObservation := ObserveJSONValue(domain, left)
		rightObservation := ObserveJSONValue(domain, right)
		if leftObservation.Digest != rightObservation.Digest {
			t.Fatalf(
				"digests differ: %#v => %s; %#v => %s",
				left,
				leftObservation.Digest,
				right,
				rightObservation.Digest,
			)
		}
	}
	different := func(t *testing.T, left, right any) {
		t.Helper()
		leftObservation := ObserveJSONValue(domain, left)
		rightObservation := ObserveJSONValue(domain, right)
		if leftObservation.Digest == rightObservation.Digest {
			t.Fatalf("digests match: %#v and %#v => %s", left, right, leftObservation.Digest)
		}
	}

	equal(t, int8(1), int64(1))
	equal(t, uint8(1), uint64(1))
	equal(t, float32(1), float64(1))
	different(t, int64(1), uint64(1))
	different(t, int64(1), float64(1))
	different(t, math.Copysign(0, -1), float64(0))
	different(t, json.Number("1"), json.Number("1.0"))
	different(t, nil, []any(nil))
	different(t, []any(nil), []any{})
	different(t, map[string]any(nil), map[string]any{})
	different(t, false, 0)
	different(t, "", nil)

	first := map[string]any{"b": []any{true, json.Number("1e2")}, "a": "value"}
	second := map[string]any{"a": "value", "b": []any{true, json.Number("1e2")}}
	equal(t, first, second)
	observed := ObserveJSONValue(domain, first)
	first["a"] = "mutated-after-observation"
	if observed.Digest != ObserveJSONValue(domain, second).Digest {
		t.Fatal("source mutation changed detached observation")
	}
	if observed.Class != "json" || observed.Count != 2 || observed.Bytes == 0 ||
		observed.UTF8Valid || observed.Runes != 0 || !validObservationDigest(observed.Digest) {
		t.Fatalf("structured observation = %#v", observed)
	}
}

type observationNamedString string

func TestObserveJSONValueRejectsUnsupportedAndInvalidValues(t *testing.T) {
	invalidNumbers := []json.Number{"01", "+1", "1.", ".1", "1e", "--1", "NaN", "Inf"}
	for _, number := range invalidNumbers {
		got := ObserveJSONValue(ObservationDomainToolArguments, number)
		assertUnavailableObservation(t, got, reasonInvalidNumber)
	}
	for _, value := range []any{
		observationNamedString("named"),
		[1]any{"array"},
		new(string),
		func() {},
		make(chan struct{}),
	} {
		got := ObserveJSONValue(ObservationDomainToolArguments, value)
		assertUnavailableObservation(t, got, reasonUnsupportedType)
	}
	for _, value := range []any{math.NaN(), math.Inf(1), math.Inf(-1)} {
		got := ObserveJSONValue(ObservationDomainToolArguments, value)
		assertUnavailableObservation(t, got, reasonNonfiniteFloat)
	}
	invalidDomain := ObserveJSONValue(0, map[string]any{"secret": true})
	assertUnavailableObservation(t, invalidDomain, reasonInvalidDomain)
}

func TestObserveJSONValueBoundsAndCycles(t *testing.T) {
	cyclicMap := map[string]any{}
	cyclicMap["self"] = cyclicMap
	assertUnavailableObservation(
		t,
		ObserveJSONValue(ObservationDomainToolArguments, cyclicMap),
		reasonCycle,
	)

	cyclicSlice := make([]any, 1)
	cyclicSlice[0] = cyclicSlice
	assertUnavailableObservation(
		t,
		ObserveJSONValue(ObservationDomainToolArguments, cyclicSlice),
		reasonCycle,
	)

	tooDeep := any("leaf")
	for range maxObservationDepth {
		tooDeep = []any{tooDeep}
	}
	assertUnavailableObservation(
		t,
		ObserveJSONValue(ObservationDomainToolArguments, tooDeep),
		reasonDepthLimit,
	)

	tooManyMembers := make([]any, maxObservationMembers+1)
	assertUnavailableObservation(
		t,
		ObserveJSONValue(ObservationDomainToolArguments, tooManyMembers),
		reasonMemberLimit,
	)

	tooManyNodes := make([]any, 9)
	for index := range tooManyNodes {
		items := make([]any, maxObservationMembers)
		for item := range items {
			items[item] = false
		}
		tooManyNodes[index] = items
	}
	assertUnavailableObservation(
		t,
		ObserveJSONValue(ObservationDomainToolArguments, tooManyNodes),
		reasonNodeLimit,
	)

	tooManyBytes := strings.Repeat("x", maxObservationBytes)
	assertUnavailableObservation(
		t,
		ObserveJSONValue(ObservationDomainToolArguments, tooManyBytes),
		reasonByteLimit,
	)

	largeNumber := json.Number(strings.Repeat("1", maxObservationBytes+1))
	got := ObserveJSONValue(ObservationDomainToolArguments, largeNumber)
	assertUnavailableObservation(t, got, reasonByteLimit)
}

func TestObservationFieldsFixedShapeAndFailClosedValidation(t *testing.T) {
	observation := ObserveText(ObservationDomainPrompt, "private-canary")
	fields := ObservationFields(ObservationPrefixPrompt, observation)
	wantKeys := []string{
		"prompt_class", "prompt_bytes", "prompt_runes", "prompt_utf8_valid",
		"prompt_count", "prompt_digest", "prompt_state", "prompt_reason_code",
	}
	if len(fields) != len(wantKeys) {
		t.Fatalf("field count = %d; fields=%#v", len(fields), fields)
	}
	for _, key := range wantKeys {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing field %q in %#v", key, fields)
		}
	}
	if _, ok := fields["prompt_bytes"].(int64); !ok {
		t.Fatalf("bytes field type = %T", fields["prompt_bytes"])
	}
	if strings.Contains(anyStringField(fields), "private-canary") {
		t.Fatalf("fields leaked canary: %#v", fields)
	}

	mismatched := ObservationFields(ObservationPrefixPath, observation)
	if mismatched["path_state"] != observationStateUnavailable ||
		mismatched["path_reason_code"] != reasonInvalidPrefix {
		t.Fatalf("mismatched fields = %#v", mismatched)
	}
	invalidPrefix := ObservationFields(0, observation)
	if invalidPrefix["error_reason_code"] != reasonInvalidPrefix {
		t.Fatalf("invalid-prefix fields = %#v", invalidPrefix)
	}

	mutations := []func(*Observation){
		func(value *Observation) { value.Class = "raw-canary" },
		func(value *Observation) { value.Bytes = -1 },
		func(value *Observation) { value.Digest = "sha256:RAW-CANARY" },
		func(value *Observation) { value.State = "raw-canary" },
		func(value *Observation) { value.ReasonCode = "raw-canary" },
	}
	for index, mutate := range mutations {
		changed := observation
		mutate(&changed)
		got := ObservationFields(ObservationPrefixPrompt, changed)
		if got["prompt_state"] != observationStateUnavailable ||
			got["prompt_reason_code"] != reasonInvalidBound ||
			strings.Contains(anyStringField(got), "raw-canary") {
			t.Fatalf("mutation %d did not fail closed: %#v", index, got)
		}
	}

	zero := ObservationFields(ObservationPrefixPrompt, Observation{})
	if zero["prompt_reason_code"] != reasonInvalidPrefix {
		t.Fatalf("zero observation fields = %#v", zero)
	}
}

func TestObservationEnumsAndReasonCodesAreClosed(t *testing.T) {
	for domain := ObservationDomainPrompt; domain <= ObservationDomainErrorText; domain++ {
		if !validDomain(domain) || observationDomainLabels[domain] == "" {
			t.Fatalf("domain %d is not valid", domain)
		}
		prefix := ObservationFieldPrefix(domain)
		label, ok := observationPrefixLabel(prefix)
		if !ok || label != strings.ReplaceAll(observationDomainLabels[domain], ".", "_") {
			t.Fatalf("domain/prefix %d: domain=%q prefix=%q ok=%v", domain, observationDomainLabels[domain], label, ok)
		}
	}
	for class := ErrorClassNone; class <= ErrorClassUnknown; class++ {
		if _, ok := errorClassLabel(class); !ok {
			t.Fatalf("error class %d invalid", class)
		}
	}
	for _, reason := range []string{
		reasonInvalidDomain, reasonInvalidPrefix, reasonInvalidBound,
		reasonUnsupportedType, reasonCycle, reasonDepthLimit, reasonNodeLimit,
		reasonMemberLimit, reasonByteLimit, reasonInvalidNumber,
		reasonNonfiniteFloat, reasonUnnamedError, reasonInternalPanic,
	} {
		if !validUnavailableReason(reason) {
			t.Fatalf("reason %q invalid", reason)
		}
	}
	if validDomain(0) || validDomain(ObservationDomain(255)) ||
		validUnavailableReason("private-canary") || validObservationDigest("sha256:ABC") {
		t.Fatal("invalid enum or wire value accepted")
	}
}

func FuzzSafeObservation(f *testing.F) {
	f.Add([]byte("safe"), uint8(0))
	f.Add([]byte{0, '\n', 0xff}, uint8(1))
	f.Add([]byte("https://user:secret@example.test/private?q=secret"), uint8(2))
	f.Fuzz(func(t *testing.T, input []byte, shape uint8) {
		canary := string(input)
		var observation Observation
		switch shape % 6 {
		case 0:
			observation = ObserveBytes(ObservationDomainPrompt, input)
		case 1:
			observation = ObserveText(ObservationDomainModelResponse, canary)
		case 2:
			observation = ObservePath(canary)
		case 3:
			observation = ObserveURL(canary)
		case 4:
			observation = ObserveJSONValue(
				ObservationDomainToolArguments,
				map[string]any{"value": canary, "bytes": []any{len(input), input == nil}},
			)
		case 5:
			observation = ObserveIdentity(ObservationDomainIdentityRequest, canary)
		}
		if observation.State != observationStateComplete &&
			observation.State != observationStateUnavailable {
			t.Fatalf("invalid state: %#v", observation)
		}
		fields := ObservationFields(observation.expectedPrefix, observation)
		if len(input) >= 16 && utf8.Valid(input) &&
			strings.Contains(anyStringField(fields), canary) {
			t.Fatalf("observation fields leaked input: %#v", fields)
		}
	})
}

func assertUnavailableObservation(t *testing.T, observation Observation, reason string) {
	t.Helper()
	if observation.State != observationStateUnavailable || observation.ReasonCode != reason ||
		observation.Bytes != 0 || observation.Runes != 0 || observation.UTF8Valid ||
		observation.Count != 0 || observation.Digest != "" {
		t.Fatalf("unavailable observation = %#v; want reason %q", observation, reason)
	}
}

func anyStringField(fields map[string]any) string {
	var values []string
	for _, value := range fields {
		if text, ok := value.(string); ok {
			values = append(values, text)
		}
	}
	return strings.Join(values, "\x00")
}

func TestObservationFieldsReturnsDetachedMap(t *testing.T) {
	observation := ObserveText(ObservationDomainPrompt, "detached")
	first := ObservationFields(ObservationPrefixPrompt, observation)
	first["prompt_class"] = "changed"
	second := ObservationFields(ObservationPrefixPrompt, observation)
	if reflect.DeepEqual(first, second) || second["prompt_class"] != "text" {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
}

func TestObservationLegacyWireEnumsFrozen(t *testing.T) {
	legacyDomains := []string{
		"", "prompt", "message_graph", "model_response", "reasoning",
		"tool_schema", "tool_arguments", "tool_result", "query", "regex",
		"command", "stdout", "transcription", "path", "url", "proxy",
		"provider_body", "response_header", "process_stderr", "identity.agent",
		"identity.session", "identity.chat", "identity.sender", "identity.message",
		"identity.turn", "identity.tool", "identity.tool_call", "identity.hook",
		"identity.runtime", "identity.account", "identity.request", "identity.trace",
		"identity.task", "identity.topic", "identity.space", "identity.provider",
		"identity.mcp_server", "identity.mcp_tool", "identity.audio", "error_type",
		"error_text",
	}
	for numeric, label := range legacyDomains {
		if numeric >= len(observationDomainLabels) ||
			observationDomainLabels[numeric] != label {
			t.Fatalf("legacy domain %d = %q; want %q", numeric, observationDomainLabels[numeric], label)
		}
	}

	legacyPrefixes := []string{
		"", "prompt", "message_graph", "model_response", "reasoning",
		"tool_schema", "tool_arguments", "tool_result", "query", "regex",
		"command", "stdout", "transcription", "path", "url", "proxy",
		"provider_body", "response_header", "process_stderr", "identity_agent",
		"identity_session", "identity_chat", "identity_sender", "identity_message",
		"identity_turn", "identity_tool", "identity_tool_call", "identity_hook",
		"identity_runtime", "identity_account", "identity_request", "identity_trace",
		"identity_task", "identity_topic", "identity_space", "identity_provider",
		"identity_mcp_server", "identity_mcp_tool", "identity_audio", "error_type",
		"error_text", "error", "credential", "environment", "request_header",
		"authorization", "cookie", "private_key",
	}
	for numeric, label := range legacyPrefixes {
		if numeric >= len(observationPrefixLabels) ||
			observationPrefixLabels[numeric] != label {
			t.Fatalf("legacy prefix %d = %q; want %q", numeric, observationPrefixLabels[numeric], label)
		}
	}
	if ObservationDomainErrorText != 40 || ObservationPrefixPrivateKey != 47 {
		t.Fatalf(
			"legacy enum tails moved: domain=%d prefix=%d",
			ObservationDomainErrorText,
			ObservationPrefixPrivateKey,
		)
	}
}

func TestObservationAppendedDomainAndPrefixMapping(t *testing.T) {
	if ObservationDomainHookMessage != 41 ||
		ObservationDomainIdentityContextManager != 47 ||
		len(observationDomainLabels) != 48 {
		t.Fatalf(
			"appended domain wire moved: first=%d last=%d labels=%d",
			ObservationDomainHookMessage,
			ObservationDomainIdentityContextManager,
			len(observationDomainLabels),
		)
	}
	if ObservationPrefixHookMessage != 48 ||
		ObservationPrefixIdentityContextManager != 54 ||
		len(observationPrefixLabels) != 55 {
		t.Fatalf(
			"appended prefix wire moved: first=%d last=%d labels=%d",
			ObservationPrefixHookMessage,
			ObservationPrefixIdentityContextManager,
			len(observationPrefixLabels),
		)
	}
	tests := []struct {
		domain   ObservationDomain
		prefix   ObservationFieldPrefix
		label    string
		identity bool
	}{
		{ObservationDomainHookMessage, ObservationPrefixHookMessage, "hook_message", false},
		{ObservationDomainIdentityChannel, ObservationPrefixIdentityChannel, "identity_channel", true},
		{ObservationDomainIdentityModel, ObservationPrefixIdentityModel, "identity_model", true},
		{ObservationDomainIdentityWorkflow, ObservationPrefixIdentityWorkflow, "identity_workflow", true},
		{ObservationDomainIdentitySkill, ObservationPrefixIdentitySkill, "identity_skill", true},
		{ObservationDomainIdentityRoute, ObservationPrefixIdentityRoute, "identity_route", true},
		{
			ObservationDomainIdentityContextManager,
			ObservationPrefixIdentityContextManager,
			"identity_context_manager",
			true,
		},
	}
	digests := make(map[string]struct{}, len(tests))
	for _, test := range tests {
		prefix, ok := prefixForDomain(test.domain)
		if !ok || prefix != test.prefix {
			t.Fatalf("domain %d prefix = %d, %v; want %d", test.domain, prefix, ok, test.prefix)
		}
		label, ok := observationPrefixLabel(test.prefix)
		if !ok || label != test.label {
			t.Fatalf("prefix %d label = %q, %v; want %q", test.prefix, label, ok, test.label)
		}
		var observation Observation
		if test.identity {
			observation = ObserveIdentity(test.domain, "same-value")
		} else {
			observation = ObserveText(test.domain, "same-value")
			bytesObservation := ObserveBytes(
				test.domain,
				[]byte("same-value"),
			)
			if bytesObservation.State != observationStateComplete ||
				bytesObservation.Digest == observation.Digest {
				t.Fatalf("hook message byte/text separation failed: %#v %#v", observation, bytesObservation)
			}
		}
		if observation.State != observationStateComplete {
			t.Fatalf("domain %d observation unavailable: %#v", test.domain, observation)
		}
		if _, duplicate := digests[observation.Digest]; duplicate {
			t.Fatalf("appended domain digest collision for %d", test.domain)
		}
		digests[observation.Digest] = struct{}{}
	}
	if ObservationDomainHookMessage <= ObservationDomainErrorText ||
		ObservationPrefixHookMessage <= ObservationPrefixPrivateKey {
		t.Fatal("appended enums were not appended")
	}
	if validDomain(ObservationDomainIdentityContextManager + 1) {
		t.Fatal("domain after append-only tail accepted")
	}
	if _, ok := observationPrefixLabel(ObservationPrefixIdentityContextManager + 1); ok {
		t.Fatal("prefix after append-only tail accepted")
	}
}
