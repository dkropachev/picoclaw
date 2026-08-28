package logger

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestSafeLoggerClosedEnumsAreExhaustive(t *testing.T) {
	if ComponentAgent != 1 || ComponentWebSearch != 29 || len(componentLabels) != 30 {
		t.Fatalf(
			"component wire moved: first=%d last=%d labels=%d",
			ComponentAgent, ComponentWebSearch, len(componentLabels),
		)
	}
	seenComponents := make(map[string]ComponentID)
	for component := ComponentAgent; component <= ComponentWebSearch; component++ {
		label, ok := componentLabel(component)
		if !ok || label == "" {
			t.Fatalf("component %d invalid", component)
		}
		if prior, duplicate := seenComponents[label]; duplicate {
			t.Fatalf("component label %q shared by %d and %d", label, prior, component)
		}
		seenComponents[label] = component
	}
	if _, ok := componentLabel(0); ok {
		t.Fatal("zero component accepted")
	}
	if _, ok := componentLabel(ComponentWebSearch + 1); ok {
		t.Fatal("component after append-only tail accepted")
	}

	if DiagnosticMessageEvent != 1 || DiagnosticMessageRuntimeEvent != 21 ||
		DiagnosticMessageToolCall != 22 || DiagnosticMessageHookCloseFailed != 54 ||
		len(diagnosticMessageLabels) != 55 {
		t.Fatalf(
			"message wire moved: first=%d last=%d labels=%d",
			DiagnosticMessageEvent,
			DiagnosticMessageHookCloseFailed,
			len(diagnosticMessageLabels),
		)
	}
	seenMessages := make(map[string]DiagnosticMessageID)
	for message := DiagnosticMessageEvent; message <= DiagnosticMessageHookCloseFailed; message++ {
		label, ok := diagnosticMessageLabel(message)
		if !ok || label == "" {
			t.Fatalf("message %d invalid", message)
		}
		if prior, duplicate := seenMessages[label]; duplicate {
			t.Fatalf("message label %q shared by %d and %d", label, prior, message)
		}
		seenMessages[label] = message
	}
	if _, ok := diagnosticMessageLabel(0); ok {
		t.Fatal("zero message accepted")
	}
	if _, ok := diagnosticMessageLabel(DiagnosticMessageHookCloseFailed + 1); ok {
		t.Fatal("message after append-only tail accepted")
	}
}

func TestSafeFieldKeyKindsAndEnumFamiliesAreExhaustive(t *testing.T) {
	if FieldIteration != 1 || FieldDurationMilliseconds != 29 ||
		FieldAsync != 42 || FieldState != 55 || FieldReason != 59 ||
		FieldRequestedCount != 60 || FieldSource != 63 {
		t.Fatalf(
			"field wire moved: first=%d int64=%d bool=%d enum=%d last=%d",
			FieldIteration,
			FieldDurationMilliseconds,
			FieldAsync,
			FieldState,
			FieldSource,
		)
	}
	if SafeEnumPending != 1 || SafeEnumStopped != 21 ||
		SafeEnumInProcess != 22 || SafeEnumUnknown != 24 || len(safeEnumLabels) != 25 {
		t.Fatalf(
			"safe enum wire moved: first=%d last=%d labels=%d",
			SafeEnumPending, SafeEnumUnknown, len(safeEnumLabels),
		)
	}
	seenLabels := make(map[string]FieldKey)
	for key := FieldIteration; key <= FieldSource; key++ {
		label, kind := safeFieldSpec(key)
		if label == "" || kind == 0 {
			t.Fatalf("field key %d missing spec", key)
		}
		if prior, duplicate := seenLabels[label]; duplicate {
			t.Fatalf("field label %q shared by %d and %d", label, prior, key)
		}
		seenLabels[label] = key

		var field SafeField
		switch kind {
		case safeFieldKindInt:
			field = SafeInt(key, 1)
		case safeFieldKindInt64:
			field = SafeInt64(key, 1)
		case safeFieldKindBool:
			field = SafeBool(key, true)
		case safeFieldKindEnum:
			field = SafeEnum(key, firstAllowedSafeEnum(t, key))
		default:
			t.Fatalf("field key %d has unsupported kind %d", key, kind)
		}
		if !field.valid || !safeFieldValid(field) {
			t.Fatalf("field key %d rejected matching constructor", key)
		}
	}
	if label, kind := safeFieldSpec(0); label != "" || kind != 0 {
		t.Fatalf("zero key spec = %q, %d", label, kind)
	}
	if label, kind := safeFieldSpec(FieldSource + 1); label != "" || kind != 0 {
		t.Fatalf("key after append-only tail spec = %q, %d", label, kind)
	}

	for _, key := range []FieldKey{
		FieldState, FieldAction, FieldOutcome, FieldRole, FieldReason, FieldSource,
	} {
		for value := SafeEnumPending; value <= SafeEnumUnknown; value++ {
			if got := SafeEnum(key, value).valid; got != safeEnumAllowed(key, value) {
				t.Fatalf("key %d enum %d validity = %v", key, value, got)
			}
		}
	}
	if SafeEnum(FieldRole, SafeEnumFailed).valid ||
		SafeEnum(FieldState, SafeEnumUser).valid {
		t.Fatal("enum value crossed its fixed family")
	}
}

func TestSafeFieldsRejectInvalidDuplicateAndExpandedOverflow(t *testing.T) {
	tooManyEntries := make([]SafeField, maxSafeFieldScalars+1)
	mutatedObservation := ObserveText(ObservationDomainPrompt, "mutation-canary")
	mutatedObservation.Digest = ObserveText(ObservationDomainPrompt, "other").Digest
	invalidCollections := []SafeFields{
		{},
		NewSafeFields(SafeField{}),
		NewSafeFields(SafeInt(FieldIteration, -1)),
		NewSafeFields(SafeInt(FieldDurationMilliseconds, 1)),
		NewSafeFields(SafeInt64(FieldIteration, 1)),
		NewSafeFields(SafeBool(FieldIteration, true)),
		NewSafeFields(SafeEnum(FieldRole, SafeEnumFailed)),
		NewSafeFields(SafeInt(FieldCount, 1), SafeInt(FieldCount, 2)),
		NewSafeFields(SafeObservation(0, Observation{})),
		NewSafeFields(SafeObservation(ObservationPrefixPrompt, mutatedObservation)),
		NewSafeFields(SafeObservation(
			ObservationPrefixPath,
			ObserveText(ObservationDomainPrompt, "prefix-canary"),
		)),
		NewSafeFields(
			SafeObservation(ObservationPrefixPrompt, ObserveText(ObservationDomainPrompt, "a")),
			SafeObservation(ObservationPrefixPrompt, ObserveText(ObservationDomainPrompt, "b")),
		),
		NewSafeFields(tooManyEntries...),
	}
	for index, fields := range invalidCollections {
		if fields.valid {
			t.Fatalf("invalid collection %d accepted: %#v", index, fields)
		}
	}
	if fields := NewSafeFields(); !fields.valid || fields.scalarCount != 0 {
		t.Fatalf("valid empty fields = %#v", fields)
	}

	entries := make([]SafeField, 0, 17)
	for domain := ObservationDomainPrompt; len(entries) < 17; domain++ {
		if domain == ObservationDomainErrorType {
			continue
		}
		prefix, ok := prefixForDomain(domain)
		if !ok {
			t.Fatalf("domain %d has no prefix", domain)
		}
		entries = append(entries, SafeObservation(prefix, ObserveText(domain, "bounded")))
	}
	if fields := NewSafeFields(entries[:16]...); !fields.valid || fields.scalarCount != 128 {
		t.Fatalf("128 expanded fields rejected: %#v", fields)
	}
	if fields := NewSafeFields(entries...); fields.valid {
		t.Fatalf("136 expanded fields accepted: %#v", fields)
	}
}

func TestSafeFieldsDetachSourceAndEmitDeterministically(t *testing.T) {
	observation := ObserveText(ObservationDomainPrompt, "private-observation")
	source := []SafeField{
		SafeBool(FieldSuccess, true),
		SafeObservation(ObservationPrefixPrompt, observation),
		SafeInt(FieldAttempt, 3),
	}
	fields := NewSafeFields(source...)
	source[0] = SafeBool(FieldSuccess, false)
	observation.Digest = "private-mutation"

	records, raw := captureSafeJSONRecords(t, func() {
		InfoSafeCF(ComponentAgent, DiagnosticMessageEvent, fields)
	})
	if len(records) != 1 || records[0]["success"] != true ||
		records[0]["attempt"] != float64(3) ||
		records[0]["prompt_digest"] == "private-mutation" {
		t.Fatalf("detached record = %#v", records)
	}
	if strings.Index(raw, `"attempt"`) > strings.Index(raw, `"prompt_class"`) ||
		strings.Index(raw, `"prompt_class"`) > strings.Index(raw, `"success"`) {
		t.Fatalf("safe fields not deterministically sorted: %s", raw)
	}
	if strings.Contains(raw, "safe_fields.go") || strings.Contains(raw, "sensitive_preview.go") {
		t.Fatalf("logger helper reported as caller: %s", raw)
	}
}

func TestSafeFieldsTypedProjectionAndDefensiveValidation(t *testing.T) {
	fields := NewSafeFields(
		SafeInt64(FieldDurationMilliseconds, 17),
		SafeBool(FieldHandled, true),
		SafeEnum(FieldRole, SafeEnumAssistant),
	)
	records, raw := captureSafeJSONRecords(t, func() {
		InfoSafeCF(ComponentLogger, DiagnosticMessageEvent, fields)
	})
	if len(records) != 1 || records[0]["duration_ms"] != float64(17) ||
		records[0]["handled"] != true || records[0]["role"] != "assistant" {
		t.Fatalf("typed projection = %#v; raw=%s", records, raw)
	}

	preview := sensitivePreviewWire{serialized: marshalSensitivePreview([]byte("safe"), false)}
	if got := (SafeFields{}).withSensitivePreview(preview); got.preview != nil {
		t.Fatalf("invalid fields accepted preview: %#v", got)
	}
	fullEntries := make([]SafeField, 0, maxSafeFieldScalars/8)
	for domain := ObservationDomainPrompt; len(fullEntries) < maxSafeFieldScalars/8; domain++ {
		if domain == ObservationDomainErrorType {
			continue
		}
		prefix, ok := prefixForDomain(domain)
		if !ok {
			t.Fatalf("domain %d has no prefix", domain)
		}
		fullEntries = append(fullEntries, SafeObservation(prefix, ObserveText(domain, "safe")))
	}
	full := NewSafeFields(fullEntries...)
	if !full.valid || full.withSensitivePreview(preview).preview != nil {
		t.Fatalf("scalar-cap fields accepted preview: %#v", full)
	}

	for index, field := range []SafeField{
		{key: FieldIteration, kind: safeFieldKindInt64, int64Value: 1, valid: true},
		{key: FieldIteration, kind: safeFieldKind(255), valid: true},
	} {
		if safeFieldValid(field) {
			t.Fatalf("forged safe field %d accepted: %#v", index, field)
		}
	}
	if safeEnumAllowed(FieldRole, 0) || safeEnumAllowed(FieldRole, SafeEnumUnknown+1) {
		t.Fatal("out-of-range safe enum accepted")
	}

	var buffer bytes.Buffer
	eventLogger := zerolog.New(&buffer)
	event := eventLogger.Info()
	appendSafeObservation(event, 0, Observation{})
	event.Msg("defensive-prefix")
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buffer.Bytes()), &record); err != nil {
		t.Fatalf("Unmarshal(defensive observation) error = %v", err)
	}
	if record["error_state"] != observationStateUnavailable ||
		record["error_reason_code"] != reasonInvalidPrefix {
		t.Fatalf("defensive observation = %#v", record)
	}
}

func TestSafeEmittersFailClosedAndCoverAllLevels(t *testing.T) {
	var fatalExitCalls int
	records, raw := captureSafeJSONRecords(t, func() {
		zerolog.FatalExitFunc = func() { fatalExitCalls++ }
		DebugSafeCF(ComponentAgent, DiagnosticMessageEvent, NewSafeFields())
		InfoSafeCF(ComponentAgent, DiagnosticMessageEvent, NewSafeFields())
		WarnSafeCF(ComponentAgent, DiagnosticMessageEvent, NewSafeFields())
		ErrorSafeCF(ComponentAgent, DiagnosticMessageEvent, NewSafeFields())
		FatalSafeCF(ComponentAgent, DiagnosticMessageEvent, NewSafeFields())
		InfoSafeCF(ComponentID(255), DiagnosticMessageEvent, NewSafeFields())
		InfoSafeCF(ComponentAgent, DiagnosticMessageID(65535), NewSafeFields())
		InfoSafeCF(ComponentAgent, DiagnosticMessageEvent, SafeFields{})
	})
	if len(records) != 8 {
		t.Fatalf("record count = %d; raw=%s", len(records), raw)
	}
	if fatalExitCalls != 1 {
		t.Fatalf("fatal exit calls = %d, want 1", fatalExitCalls)
	}
	for _, index := range []int{5, 6} {
		if records[index]["message"] != "Safe diagnostic rejected" ||
			records[index][safeFieldsReasonKey] != safeEnvelopeInvalid {
			t.Fatalf("invalid envelope record %d = %#v", index, records[index])
		}
	}
	if records[7][safeFieldsReasonKey] != safeFieldsInvalid ||
		records[7][safeFieldsStateKey] != observationStateUnavailable {
		t.Fatalf("zero fields record = %#v", records[7])
	}
	if strings.Contains(raw, "private-canary") {
		t.Fatalf("unexpected canary in output: %s", raw)
	}
}

func TestFatalSafeCFExitsWithoutOutputWhenLoggingDisabled(t *testing.T) {
	prepareLoggerStateTest(t)
	path := filepath.Join(t.TempDir(), "disabled-fatal.log")
	if err := EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	var fatalExitCalls int
	zerolog.FatalExitFunc = func() { fatalExitCalls++ }
	SetLevel(zerolog.Disabled)

	FatalSafeCF(ComponentLogger, DiagnosticMessageEvent, NewSafeFields())
	if fatalExitCalls != 1 {
		t.Fatalf("fatal exit calls = %d, want 1", fatalExitCalls)
	}
	DisableFileLogging()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("disabled Fatal emitted output: %q", data)
	}
}

func TestLegacyFatalConvenienceVariantsExit(t *testing.T) {
	prepareLoggerStateTest(t)
	var fatalExitCalls int
	zerolog.FatalExitFunc = func() { fatalExitCalls++ }

	FatalC("logger-test", "fatal component")
	Fatalf("fatal %s", "formatted")
	FatalF("fatal fields", map[string]any{"count": 1})
	FatalCF("logger-test", "fatal component fields", map[string]any{"count": 2})
	if fatalExitCalls != 4 {
		t.Fatalf("fatal exit calls = %d, want 4", fatalExitCalls)
	}
}

func firstAllowedSafeEnum(t *testing.T, key FieldKey) SafeEnumValue {
	t.Helper()
	for value := SafeEnumPending; value <= SafeEnumUnknown; value++ {
		if safeEnumAllowed(key, value) {
			return value
		}
	}
	t.Fatalf("field key %d has no allowed enum", key)
	return 0
}

func captureSafeJSONRecords(
	t *testing.T,
	emit func(),
) ([]map[string]any, string) {
	t.Helper()
	prepareLoggerStateTest(t)
	path := filepath.Join(t.TempDir(), "safe.log")
	if err := EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	emit()
	DisableFileLogging()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("Unmarshal(%q) error = %v", line, err)
		}
		records = append(records, record)
	}
	return records, string(data)
}
