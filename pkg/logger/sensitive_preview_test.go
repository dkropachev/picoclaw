package logger

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDebugSensitivePolicyTruthAndSafeObservation(t *testing.T) {
	canary := "application-preview-canary"
	policies := []DiagnosticPolicy{
		{},
		NewDiagnosticPolicy(false, DEBUG),
		NewDiagnosticPolicy(true, INFO),
		NewDiagnosticPolicy(true, DEBUG),
	}
	records, raw := captureSafeJSONRecords(t, func() {
		for _, policy := range policies {
			DebugSensitiveCF(
				policy,
				ComponentAgent,
				DiagnosticMessageSystemPrompt,
				NewSafeFields(SafeInt(FieldPromptPartCount, 1)),
				SensitivityPrompt,
				ObservationDomainPrompt,
				canary,
			)
		}
	})
	if len(records) != len(policies) {
		t.Fatalf("record count = %d; raw=%s", len(records), raw)
	}
	for index, record := range records {
		if record["prompt_state"] != observationStateComplete ||
			record["prompt_bytes"] != float64(len(canary)) ||
			record["prompt_digest"] == "" {
			t.Fatalf("record %d missing safe observation: %#v", index, record)
		}
		_, hasPreview := record[sensitivePreviewField]
		if hasPreview != (index == len(records)-1) {
			t.Fatalf("record %d preview presence = %v", index, hasPreview)
		}
	}
	preview := previewFromRecord(t, records[len(records)-1])
	if preview["escaped"] != canary || preview["truncated"] != false {
		t.Fatalf("enabled preview = %#v", preview)
	}
	if strings.Count(raw, canary) != 1 {
		t.Fatalf("raw canary count = %d; output=%s", strings.Count(raw, canary), raw)
	}
}

func TestDebugSensitiveRejectsWrongTypePairMessageAndInvalidUTF8(t *testing.T) {
	invalidUTF8 := string([]byte{'x', 0xff, 'y'})
	records, raw := captureSafeJSONRecords(t, func() {
		policy := NewDiagnosticPolicy(true, DEBUG)
		fields := NewSafeFields()
		DebugSensitiveCF(policy, ComponentAgent, DiagnosticMessageSystemPrompt, fields,
			SensitivityPrompt, ObservationDomainPrompt, []byte("wrong-type-canary"))
		DebugSensitiveCF(policy, ComponentAgent, DiagnosticMessageSystemPrompt, fields,
			SensitivityPrompt, ObservationDomainHookMessage, "cross-pair-canary")
		DebugSensitiveCF(policy, ComponentAgent, DiagnosticMessageModelResponse, fields,
			SensitivityPrompt, ObservationDomainPrompt, "message-pair-canary")
		DebugSensitiveCF(policy, ComponentAgent, DiagnosticMessageSystemPrompt, fields,
			SensitivityPrompt, ObservationDomainPrompt, invalidUTF8)
		DebugSensitiveCF(policy, ComponentAgent, DiagnosticMessageInboundMessage, fields,
			SensitivityInboundMessage, ObservationDomainHookMessage, "hook-domain-canary")
		DebugSensitiveCF(policy, ComponentAgent, DiagnosticMessageSystemPrompt, fields,
			SensitivityClass(255), ObservationDomainPrompt, "class-canary")
	})
	if len(records) != 6 {
		t.Fatalf("record count = %d; raw=%s", len(records), raw)
	}
	for index, record := range records {
		if _, ok := record[sensitivePreviewField]; ok {
			t.Fatalf("record %d emitted rejected preview: %#v", index, record)
		}
	}
	if records[0]["prompt_state"] != observationStateUnavailable ||
		records[0]["prompt_reason_code"] != reasonUnsupportedType {
		t.Fatalf("wrong type record = %#v", records[0])
	}
	for _, index := range []int{1, 4, 5} {
		if records[index]["error_state"] != observationStateUnavailable ||
			records[index]["error_reason_code"] != reasonInvalidDomain {
			t.Fatalf("cross-pair record %d = %#v", index, records[index])
		}
	}
	if records[2]["prompt_state"] != observationStateComplete {
		t.Fatalf("message mismatch lost safe observation: %#v", records[2])
	}
	if records[3]["prompt_state"] != observationStateComplete ||
		records[3]["prompt_utf8_valid"] != false {
		t.Fatalf("invalid UTF-8 observation = %#v", records[3])
	}
	for _, canary := range []string{
		"wrong-type-canary", "cross-pair-canary", "message-pair-canary",
		"hook-domain-canary", "class-canary",
	} {
		if strings.Contains(raw, canary) {
			t.Fatalf("rejected canary %q leaked: %s", canary, raw)
		}
	}
}

func TestDebugSensitiveCanonicalToolArgumentsAndSnapshot(t *testing.T) {
	arguments := map[string]any{
		"z": true,
		"a": []any{"x", int64(2)},
		"f": float64(1.5),
	}
	float32Arguments := map[string]any{"value": float32(1.2)}
	float64Arguments := map[string]any{"value": float64(float32(1.2))}
	records, raw := captureSafeJSONRecords(t, func() {
		policy := NewDiagnosticPolicy(true, DEBUG)
		DebugSensitiveCF(policy, ComponentTool, DiagnosticMessageToolArguments,
			NewSafeFields(), SensitivityToolArguments,
			ObservationDomainToolArguments, arguments)
		DebugSensitiveCF(policy, ComponentTool, DiagnosticMessageToolArguments,
			NewSafeFields(), SensitivityToolArguments,
			ObservationDomainToolArguments, float32Arguments)
		DebugSensitiveCF(policy, ComponentTool, DiagnosticMessageToolArguments,
			NewSafeFields(), SensitivityToolArguments,
			ObservationDomainToolArguments, float64Arguments)
	})
	arguments["a"] = "source-mutated"
	if len(records) != 3 {
		t.Fatalf("record count = %d; raw=%s", len(records), raw)
	}
	first := previewFromRecord(t, records[0])
	want := `{\"a\":[\"x\",2],\"f\":1.5,\"z\":true}`
	if first["escaped"] != want {
		t.Fatalf("canonical preview = %q; want %q", first["escaped"], want)
	}
	if strings.Contains(raw, "source-mutated") {
		t.Fatalf("post-call source mutation appeared: %s", raw)
	}
	second := previewFromRecord(t, records[1])
	third := previewFromRecord(t, records[2])
	if second["escaped"] != third["escaped"] ||
		records[1]["tool_arguments_digest"] != records[2]["tool_arguments_digest"] {
		t.Fatalf("float32 was not normalized: second=%#v third=%#v", records[1], records[2])
	}
}

type hostileSensitiveValue struct {
	marshalCalls *atomic.Int64
	stringCalls  *atomic.Int64
}

func (value hostileSensitiveValue) MarshalJSON() ([]byte, error) {
	value.marshalCalls.Add(1)
	return []byte(`"hostile-canary"`), nil
}

func (value hostileSensitiveValue) String() string {
	value.stringCalls.Add(1)
	return "hostile-canary"
}

func TestDebugSensitiveToolArgumentsMethodFreeAndInvalidUTF8(t *testing.T) {
	var marshalCalls atomic.Int64
	var stringCalls atomic.Int64
	hostile := map[string]any{
		"value": hostileSensitiveValue{
			marshalCalls: &marshalCalls,
			stringCalls:  &stringCalls,
		},
	}
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	invalidUTF8 := map[string]any{string([]byte{'k', 0xff}): string([]byte{'v', 0xff})}
	records, raw := captureSafeJSONRecords(t, func() {
		policy := NewDiagnosticPolicy(true, DEBUG)
		for _, arguments := range []map[string]any{hostile, cyclic, invalidUTF8} {
			DebugSensitiveCF(policy, ComponentTool, DiagnosticMessageToolArguments,
				NewSafeFields(), SensitivityToolArguments,
				ObservationDomainToolArguments, arguments)
		}
	})
	if marshalCalls.Load() != 0 || stringCalls.Load() != 0 {
		t.Fatalf("hostile methods called: marshal=%d string=%d", marshalCalls.Load(), stringCalls.Load())
	}
	if len(records) != 3 {
		t.Fatalf("record count = %d; raw=%s", len(records), raw)
	}
	if records[0]["tool_arguments_reason_code"] != reasonUnsupportedType ||
		records[1]["tool_arguments_reason_code"] != reasonCycle {
		t.Fatalf("invalid graphs = %#v %#v", records[0], records[1])
	}
	if records[2]["tool_arguments_state"] != observationStateComplete ||
		records[2]["tool_arguments_digest"] == "" {
		t.Fatalf("invalid UTF-8 graph lost complete observation: %#v", records[2])
	}
	for index, record := range records {
		if _, ok := record[sensitivePreviewField]; ok {
			t.Fatalf("record %d emitted invalid graph preview: %#v", index, record)
		}
	}
	if strings.Contains(raw, "hostile-canary") {
		t.Fatalf("hostile value leaked: %s", raw)
	}
}

func TestSensitivePreviewEscapesConsoleAndJSONForgeryBytes(t *testing.T) {
	rawValue := "prefix\b\f\nforge\r\t\x1b\"\\\u0085\u061c\u200e\u202e\u2066\u2028\u2029suffix"
	record, console, fileRaw := captureSensitiveConsoleAndJSON(t, func() {
		DebugSensitiveCF(
			NewDiagnosticPolicy(true, DEBUG),
			ComponentAgent,
			DiagnosticMessageSystemPrompt,
			NewSafeFields(),
			SensitivityPrompt,
			ObservationDomainPrompt,
			rawValue,
		)
	})
	preview := previewFromRecord(t, record)
	escaped, ok := preview["escaped"].(string)
	if !ok {
		t.Fatalf("preview escaped type = %T", preview["escaped"])
	}
	for _, token := range []string{
		`\b`, `\f`, `\n`, `\r`, `\t`, `\u001b`, `\"`, `\\`, `\u0085`, `\u061c`,
		`\u200e`, `\u202e`, `\u2066`, `\u2028`, `\u2029`,
	} {
		if !strings.Contains(escaped, token) || !strings.Contains(console, token) {
			t.Fatalf("escaped token %q missing: escaped=%q console=%q", token, escaped, console)
		}
	}
	for _, forbidden := range []string{
		"\b", "\f", "\r", "\t", "\x1b", "\u0085", "\u061c", "\u200e", "\u202e",
		"\u2066", "\u2028", "\u2029",
	} {
		if strings.Contains(console, forbidden) || strings.Contains(fileRaw, forbidden) {
			t.Fatalf("raw control/bidi %q present: console=%q file=%q", forbidden, console, fileRaw)
		}
	}
	if strings.Count(console, "\n") != 1 || strings.Count(fileRaw, "\n") != 1 {
		t.Fatalf("record-forging newline appeared: console=%q file=%q", console, fileRaw)
	}
}

func TestSensitivePreviewSerializedBoundAndTokenBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "plain", value: strings.Repeat("a", 10000)},
		{name: "backslash expansion", value: strings.Repeat(`\`, 10000)},
		{name: "quote expansion", value: strings.Repeat(`"`, 10000)},
		{name: "unicode rune", value: strings.Repeat("🔒", 3000)},
		{name: "bidi expansion", value: strings.Repeat("\u202e", 3000)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preview, ok := makeSensitivePreviewWire(test.value)
			if !ok || !preview.truncated || len(preview.serialized) > maxSensitivePreviewWireBytes {
				t.Fatalf("preview = %#v serialized bytes=%d", preview, len(preview.serialized))
			}
			var decoded sensitivePreviewJSON
			if err := json.Unmarshal(preview.serialized, &decoded); err != nil {
				t.Fatalf("Unmarshal() error = %v; wire=%q", err, preview.serialized)
			}
			if decoded.Escaped != preview.escaped || !utf8ValidString(decoded.Escaped) {
				t.Fatalf("decoded preview mismatch: %#v", decoded)
			}
		})
	}
	backslashes, _ := makeSensitivePreviewWire(strings.Repeat(`\`, 10000))
	if strings.Count(backslashes.escaped, `\`)%2 != 0 {
		t.Fatalf("backslash escape token split: %q", backslashes.escaped)
	}
	bidi, _ := makeSensitivePreviewWire(strings.Repeat("\u202e", 3000))
	if !strings.HasSuffix(bidi.escaped, `\u202e`) {
		t.Fatalf("bidi escape token split: %q", bidi.escaped)
	}
	small, ok := makeSensitivePreviewWire("safe")
	if !ok || small.truncated || len(small.serialized) > maxSensitivePreviewWireBytes {
		t.Fatalf("small preview = %#v", small)
	}
	if preview, invalidOK := makeSensitivePreviewWire(string([]byte{'x', 0xff})); invalidOK ||
		preview.serialized != nil {
		t.Fatalf("invalid UTF-8 preview accepted: %#v", preview)
	}

	// The wire for false is one byte longer than true. Find the exact input
	// where every token fits under the provisional truncated=true envelope but
	// the final truncated=false envelope requires removing one complete token.
	boundaryLength := maxSensitivePreviewWireBytes - len(marshalSensitivePreview(nil, true))
	boundaryValue := bytes.Repeat([]byte{'a'}, boundaryLength)
	if len(marshalSensitivePreview(boundaryValue, true)) != maxSensitivePreviewWireBytes ||
		len(marshalSensitivePreview(boundaryValue, false)) <= maxSensitivePreviewWireBytes {
		t.Fatalf("unexpected true/false preview wire boundary: length=%d", boundaryLength)
	}
	boundary, ok := makeSensitivePreviewWire(strings.Repeat("a", boundaryLength))
	if !ok || !boundary.truncated || len(boundary.escaped) != boundaryLength-1 ||
		len(boundary.serialized) > maxSensitivePreviewWireBytes {
		t.Fatalf("false-wire boundary preview = %#v", boundary)
	}
}

func TestDebugSensitiveBoundsAndInternalFieldCollision(t *testing.T) {
	tooLarge := strings.Repeat("x", maxObservationBytes+1)
	promptObservation := SafeObservation(
		ObservationPrefixPrompt,
		ObserveText(ObservationDomainPrompt, "caller-prefix-canary"),
	)
	records, raw := captureSafeJSONRecords(t, func() {
		policy := NewDiagnosticPolicy(true, DEBUG)
		DebugSensitiveCF(policy, ComponentAgent, DiagnosticMessageSystemPrompt,
			NewSafeFields(), SensitivityPrompt, ObservationDomainPrompt, tooLarge)
		DebugSensitiveCF(policy, ComponentAgent, DiagnosticMessageSystemPrompt,
			NewSafeFields(promptObservation), SensitivityPrompt,
			ObservationDomainPrompt, "internal-observation")
	})
	if records[0]["prompt_state"] != observationStateUnavailable ||
		records[0]["prompt_reason_code"] != reasonByteLimit ||
		records[0]["prompt_digest"] != "" {
		t.Fatalf("oversized text record = %#v", records[0])
	}
	if records[1][safeFieldsReasonKey] != safeFieldsInvalid ||
		records[1]["prompt_state"] != observationStateComplete {
		t.Fatalf("internal prefix collision record = %#v", records[1])
	}
	if strings.Count(raw, `"prompt_class"`) != 2 ||
		strings.Contains(raw, "caller-prefix-canary") ||
		strings.Contains(raw, "internal-observation") {
		t.Fatalf("collision leaked/duplicated field: %s", raw)
	}
}

func TestDebugSensitiveInvalidSafeObservationSuppressesPreview(t *testing.T) {
	mutated := ObserveText(ObservationDomainPath, "forged-observation-canary")
	mutated.Digest = ObserveText(ObservationDomainPath, "other-path").Digest
	fields := NewSafeFields(SafeObservation(ObservationPrefixPath, mutated))
	if fields.valid {
		t.Fatalf("mutated observation produced valid safe fields: %#v", fields)
	}

	records, raw := captureSafeJSONRecords(t, func() {
		DebugSensitiveCF(
			NewDiagnosticPolicy(true, DEBUG),
			ComponentAgent,
			DiagnosticMessageSystemPrompt,
			fields,
			SensitivityPrompt,
			ObservationDomainPrompt,
			"raw-preview-canary",
		)
	})
	if len(records) != 1 || records[0][safeFieldsReasonKey] != safeFieldsInvalid {
		t.Fatalf("invalid fields record = %#v; raw=%s", records, raw)
	}
	if _, ok := records[0][sensitivePreviewField]; ok ||
		strings.Contains(raw, "raw-preview-canary") ||
		strings.Contains(raw, "forged-observation-canary") {
		t.Fatalf("invalid observation enabled/leaked preview: %s", raw)
	}
}

func TestSensitivePairAllowlistIsExplicitAndHookNeverPreviews(t *testing.T) {
	allowed := []struct {
		component ComponentID
		class     SensitivityClass
		domain    ObservationDomain
		prefix    ObservationFieldPrefix
		message   DiagnosticMessageID
	}{
		{
			ComponentAgent, SensitivityPrompt, ObservationDomainPrompt,
			ObservationPrefixPrompt, DiagnosticMessageSystemPrompt,
		},
		{
			ComponentAgent, SensitivityInboundMessage, ObservationDomainMessageGraph,
			ObservationPrefixMessageGraph, DiagnosticMessageInboundMessage,
		},
		{
			ComponentAgent, SensitivityHistoryMessage, ObservationDomainMessageGraph,
			ObservationPrefixMessageGraph, DiagnosticMessageHistoryMessage,
		},
		{
			ComponentAgent, SensitivityModelResponse, ObservationDomainModelResponse,
			ObservationPrefixModelResponse, DiagnosticMessageModelResponse,
		},
		{
			ComponentAgent, SensitivityReasoning, ObservationDomainReasoning,
			ObservationPrefixReasoning, DiagnosticMessageModelReasoning,
		},
		{
			ComponentTool, SensitivityToolArguments, ObservationDomainToolArguments,
			ObservationPrefixToolArguments, DiagnosticMessageToolArguments,
		},
	}
	for _, test := range allowed {
		prefix, ok := sensitivePairPrefix(test.class, test.domain)
		if !ok || prefix != test.prefix ||
			!sensitiveMessageAllowed(test.message, test.class) ||
			!sensitiveComponentAllowed(test.component, test.message) {
			t.Fatalf("allowed tuple rejected: %#v", test)
		}
	}
	for class := SensitivityPrompt; class <= SensitivityToolArguments; class++ {
		if _, ok := sensitivePairPrefix(class, ObservationDomainHookMessage); ok {
			t.Fatalf("class %d previewed hook message", class)
		}
		for domain := ObservationDomainIdentityChannel; domain <= ObservationDomainIdentityContextManager; domain++ {
			if _, ok := sensitivePairPrefix(class, domain); ok {
				t.Fatalf("class %d previewed identity domain %d", class, domain)
			}
		}
	}
	if _, ok := sensitivePairPrefix(0, ObservationDomainPrompt); ok {
		t.Fatal("zero sensitivity class accepted")
	}
	for _, test := range []struct {
		component ComponentID
		message   DiagnosticMessageID
	}{
		{ComponentProvider, DiagnosticMessageSystemPrompt},
		{ComponentHooks, DiagnosticMessageModelResponse},
		{ComponentProvider, DiagnosticMessageToolArguments},
		{ComponentTool, DiagnosticMessageHookToolArguments},
		{ComponentAgent, DiagnosticMessageEvent},
	} {
		if sensitiveComponentAllowed(test.component, test.message) {
			t.Fatalf("component/message tuple accepted: %#v", test)
		}
	}
}

func TestDebugSensitiveRejectsMismatchedComponent(t *testing.T) {
	records, raw := captureSafeJSONRecords(t, func() {
		DebugSensitiveCF(
			NewDiagnosticPolicy(true, DEBUG),
			ComponentProvider,
			DiagnosticMessageSystemPrompt,
			NewSafeFields(),
			SensitivityPrompt,
			ObservationDomainPrompt,
			"component-preview-canary",
		)
	})
	if len(records) != 1 || records[0]["prompt_state"] != observationStateComplete {
		t.Fatalf("component-mismatch record = %#v; raw=%s", records, raw)
	}
	if _, ok := records[0][sensitivePreviewField]; ok ||
		strings.Contains(raw, "component-preview-canary") {
		t.Fatalf("component mismatch emitted preview: %s", raw)
	}
}

func TestSensitiveGraphClonerReportsExactBounds(t *testing.T) {
	byteCloner := sensitiveGraphCloner{
		active: make(map[observationVisit]struct{}), utf8Valid: true,
	}
	_, reason := byteCloner.clone(
		map[string]any{"value": strings.Repeat("x", maxObservationBytes)}, 1,
	)
	if reason != reasonByteLimit {
		t.Fatalf("byte exhaustion reason = %q", reason)
	}
	for name, value := range map[string]any{
		"invalid UTF-8 string": string(append(
			bytes.Repeat([]byte{'x'}, maxObservationBytes), 0xff,
		)),
		"invalid number": json.Number(strings.Repeat("1", maxObservationBytes) + "x"),
	} {
		cloner := sensitiveGraphCloner{
			active: make(map[observationVisit]struct{}), utf8Valid: true,
		}
		if _, gotReason := cloner.clone(value, 1); gotReason != reasonByteLimit {
			t.Fatalf("%s exhaustion reason = %q, want %q", name, gotReason, reasonByteLimit)
		}
	}
	oversizedKeyCloner := sensitiveGraphCloner{
		active: make(map[observationVisit]struct{}), utf8Valid: true,
	}
	oversizedKey := string(append(
		bytes.Repeat([]byte{'k'}, maxObservationBytes), 0xff,
	))
	if _, gotReason := oversizedKeyCloner.clone(
		map[string]any{oversizedKey: nil},
		1,
	); gotReason != reasonByteLimit {
		t.Fatalf("map-key exhaustion reason = %q, want %q", gotReason, reasonByteLimit)
	}
	for name, value := range map[string]any{
		"integer": int(1),
		"float32": float32(1),
		"float64": float64(1),
	} {
		cloner := sensitiveGraphCloner{
			bytes:  maxObservationBytes - 7,
			active: make(map[observationVisit]struct{}), utf8Valid: true,
		}
		if _, gotReason := cloner.clone(value, 1); gotReason != reasonByteLimit {
			t.Fatalf("%s charge reason = %q, want %q", name, gotReason, reasonByteLimit)
		}
	}

	nodeGraph := make(map[string]any, maxObservationMembers)
	for index := range maxObservationMembers {
		items := make([]any, 8)
		for itemIndex := range items {
			items[itemIndex] = itemIndex
		}
		nodeGraph[strings.Repeat("k", 4)+string(rune(0x1000+index))] = items
	}
	nodeCloner := sensitiveGraphCloner{
		active: make(map[observationVisit]struct{}), utf8Valid: true,
	}
	_, reason = nodeCloner.clone(nodeGraph, 1)
	if reason != reasonNodeLimit {
		t.Fatalf("node exhaustion reason = %q", reason)
	}
}

func TestSensitiveGraphScalarGrammarAndFailures(t *testing.T) {
	type namedSensitiveInt int
	type namedSensitiveString string

	graph := map[string]any{
		"nil":       nil,
		"bool":      true,
		"string":    "value",
		"int":       int(1),
		"int8":      int8(2),
		"int16":     int16(3),
		"int32":     int32(4),
		"int64":     int64(5),
		"uint":      uint(6),
		"uint8":     uint8(7),
		"uint16":    uint16(8),
		"uint32":    uint32(9),
		"uint64":    uint64(10),
		"float32":   float32(1.25),
		"float64":   float64(2.5),
		"number":    json.Number("3.75e+2"),
		"nil_slice": []any(nil),
		"slice":     []any{"nested", false},
		"nil_map":   map[string]any(nil),
		"map":       map[string]any{"nested": true},
	}
	cloner := sensitiveGraphCloner{
		active: make(map[observationVisit]struct{}), utf8Valid: true,
	}
	snapshot, reason := cloner.clone(graph, 1)
	if reason != "" || !cloner.utf8Valid {
		t.Fatalf("complete grammar clone = %#v, reason=%q", snapshot, reason)
	}
	canonical, ok := canonicalSensitiveJSON(snapshot)
	if !ok || !strings.Contains(canonical, `"nil_map":null`) ||
		!strings.Contains(canonical, `"number":3.75e+2`) {
		t.Fatalf("canonical complete grammar = %q, %v", canonical, ok)
	}

	sliceCycle := []any{nil}
	sliceCycle[0] = sliceCycle
	tooDeep := any("leaf")
	for range maxObservationDepth {
		tooDeep = []any{tooDeep}
	}
	tooManySliceMembers := make([]any, maxObservationMembers+1)
	tooManyMapMembers := make(map[string]any, maxObservationMembers+1)
	for index := range maxObservationMembers + 1 {
		tooManyMapMembers[string(rune(0x2000+index))] = index
	}
	failures := []struct {
		value  any
		reason string
	}{
		{float32(math.NaN()), reasonNonfiniteFloat},
		{math.Inf(1), reasonNonfiniteFloat},
		{json.Number("01"), reasonInvalidNumber},
		{sliceCycle, reasonCycle},
		{tooDeep, reasonDepthLimit},
		{tooManySliceMembers, reasonMemberLimit},
		{tooManyMapMembers, reasonMemberLimit},
		{uintptr(1), reasonUnsupportedType},
		{namedSensitiveInt(1), reasonUnsupportedType},
		{namedSensitiveString("named"), reasonUnsupportedType},
		{make(chan int), reasonUnsupportedType},
	}
	for index, failure := range failures {
		failureCloner := sensitiveGraphCloner{
			active: make(map[observationVisit]struct{}), utf8Valid: true,
		}
		if _, got := failureCloner.clone(failure.value, 1); got != failure.reason {
			t.Fatalf("failure %d reason = %q; want %q", index, got, failure.reason)
		}
	}
}

func TestDebugSensitiveToolArgumentProjectionFailuresStaySafe(t *testing.T) {
	records, raw := captureSafeJSONRecords(t, func() {
		policy := NewDiagnosticPolicy(true, DEBUG)
		DebugSensitiveCF(
			policy,
			ComponentTool,
			DiagnosticMessageToolArguments,
			NewSafeFields(),
			SensitivityToolArguments,
			ObservationDomainToolArguments,
			"wrong-tool-argument-type",
		)
		DebugSensitiveCF(
			policy,
			ComponentTool,
			DiagnosticMessageToolArguments,
			NewSafeFields(),
			SensitivityToolArguments,
			ObservationDomainToolArguments,
			map[string]any{"control": strings.Repeat("\x00", 200_000)},
		)
	})
	if len(records) != 2 {
		t.Fatalf("record count = %d; raw=%s", len(records), raw)
	}
	if records[0]["tool_arguments_reason_code"] != reasonUnsupportedType {
		t.Fatalf("wrong tool type record = %#v", records[0])
	}
	if records[1]["tool_arguments_state"] != observationStateComplete ||
		records[1]["tool_arguments_digest"] == "" {
		t.Fatalf("expanded JSON record lost observation: %#v", records[1])
	}
	for index, record := range records {
		if _, ok := record[sensitivePreviewField]; ok {
			t.Fatalf("record %d emitted rejected preview: %#v", index, record)
		}
	}
	if strings.Contains(raw, "wrong-tool-argument-type") {
		t.Fatalf("wrong-type input leaked: %s", raw)
	}
}

func TestDebugSensitiveDisabledEnvelopeAndScalarCapacity(t *testing.T) {
	prepareLoggerStateTest(t)
	SetLevel(INFO)
	DebugSensitiveCF(
		NewDiagnosticPolicy(true, DEBUG), ComponentAgent,
		DiagnosticMessageSystemPrompt, NewSafeFields(), SensitivityPrompt,
		ObservationDomainPrompt, "disabled-level-canary",
	)
	SetLevel(DEBUG)

	entries := make([]SafeField, 0, 16)
	for domain := ObservationDomainMessageGraph; len(entries) < 16; domain++ {
		if domain == ObservationDomainErrorType {
			continue
		}
		prefix, ok := prefixForDomain(domain)
		if !ok || prefix == ObservationPrefixPrompt {
			continue
		}
		entries = append(entries, SafeObservation(prefix, ObserveText(domain, "safe")))
	}
	path := filepath.Join(t.TempDir(), "capacity.log")
	if err := EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	policy := NewDiagnosticPolicy(true, DEBUG)
	DebugSensitiveCF(policy, ComponentID(255), DiagnosticMessageSystemPrompt,
		NewSafeFields(), SensitivityPrompt, ObservationDomainPrompt, "component-canary")
	DebugSensitiveCF(policy, ComponentAgent, DiagnosticMessageID(65535),
		NewSafeFields(), SensitivityPrompt, ObservationDomainPrompt, "message-canary")
	DebugSensitiveCF(policy, ComponentAgent, DiagnosticMessageSystemPrompt,
		NewSafeFields(entries[:15]...), SensitivityPrompt,
		ObservationDomainPrompt, "capacity-preview-canary")
	DebugSensitiveCF(policy, ComponentAgent, DiagnosticMessageSystemPrompt,
		NewSafeFields(entries...), SensitivityPrompt,
		ObservationDomainPrompt, "overflow-canary")
	DisableFileLogging()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("record count = %d; data=%s", len(lines), data)
	}
	for index, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("record %d decode error = %v", index, err)
		}
		if _, ok := record[sensitivePreviewField]; ok {
			t.Fatalf("record %d unexpectedly previewed: %#v", index, record)
		}
		if index < 2 && record[safeFieldsReasonKey] != safeEnvelopeInvalid {
			t.Fatalf("record %d envelope = %#v", index, record)
		}
		if index == 3 && record[safeFieldsReasonKey] != safeFieldsInvalid {
			t.Fatalf("overflow record = %#v", record)
		}
	}
	for _, canary := range []string{
		"disabled-level-canary", "component-canary", "message-canary",
		"capacity-preview-canary", "overflow-canary",
	} {
		if strings.Contains(string(data), canary) {
			t.Fatalf("suppressed canary %q leaked: %s", canary, data)
		}
	}
}

func previewFromRecord(t *testing.T, record map[string]any) map[string]any {
	t.Helper()
	preview, ok := record[sensitivePreviewField].(map[string]any)
	if !ok {
		t.Fatalf("preview field = %#v", record[sensitivePreviewField])
	}
	return preview
}

func captureSensitiveConsoleAndJSON(
	t *testing.T,
	emit func(),
) (map[string]any, string, string) {
	t.Helper()
	prepareLoggerStateTest(t)
	var console bytes.Buffer
	replaceConsoleOutputForTest(&console)
	path := filepath.Join(t.TempDir(), "sensitive.log")
	if err := EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	emit()
	DisableFileLogging()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &record); err != nil {
		t.Fatalf("Unmarshal() error = %v; data=%q", err, data)
	}
	return record, console.String(), string(data)
}

func utf8ValidString(value string) bool {
	return strings.ToValidUTF8(value, "replacement") == value
}
