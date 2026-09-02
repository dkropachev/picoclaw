package database

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestStructuredErrorCodesAreStableAndMatchable(t *testing.T) {
	codes := []ErrorCode{
		CodeUnavailable, CodeMigrationRequired, CodeConflict, CodeNotFound,
		CodeAlreadyExists, CodeDeadline, CodeIntegrity, CodeInvalid,
		CodeUnauthorized, CodeUnsupported, CodeOutcomeUnknown, CodeInternal,
	}
	seen := make(map[ErrorCode]struct{}, len(codes))
	for _, code := range codes {
		if !code.Valid() {
			t.Fatalf("code %q is not valid", code)
		}
		if _, duplicate := seen[code]; duplicate {
			t.Fatalf("duplicate code %q", code)
		}
		seen[code] = struct{}{}
		err := NewError(code, "safe message")
		if CodeOf(err) != code {
			t.Fatalf("CodeOf(%v) = %q, want %q", err, CodeOf(err), code)
		}
		if !errors.Is(err, &Error{Code: code}) {
			t.Fatalf("errors.Is did not match code %q", code)
		}
	}
	if got := NewError("driver-specific", "leak"); got.Code != CodeInternal || strings.Contains(got.Message, "leak") {
		t.Fatalf("invalid error code was not collapsed: %#v", got)
	}
	bounded := NewError(CodeInternal, strings.Repeat("private\n", 200))
	if len(bounded.Message) > maxStructuredErrorMessageBytes || strings.ContainsAny(bounded.Message, "\r\n") {
		t.Fatalf("structured error was not bounded to one line: %#v", bounded)
	}
}

func TestDiscoveryErrorsAreStructuredAndDoNotExposePaths(t *testing.T) {
	missing := t.TempDir() + "/private/missing-home"
	_, err := Connect(missing)
	if CodeOf(err) != CodeUnavailable {
		t.Fatalf("Connect() error = %v, want Unavailable", err)
	}
	if strings.Contains(err.Error(), missing) {
		t.Fatalf("discovery error exposed path: %v", err)
	}
}

func TestStoreIDAndReadinessValidation(t *testing.T) {
	for _, value := range []string{
		"global/auth", "workspace/workflows", "channels/matrix.primary", "local-ci/cache_v2",
	} {
		id, err := ParseStoreID(value)
		if err != nil || string(id) != value || !id.Valid() {
			t.Fatalf("ParseStoreID(%q) = %q, %v", value, id, err)
		}
	}
	for _, value := range []string{
		"", "Global/auth", "/global", "global/", "global//auth", "global/../auth",
		"global\\auth", "sqlite:auth", " global/auth", strings.Repeat("a", maxStoreIDBytes+1),
	} {
		if _, err := ParseStoreID(value); CodeOf(err) != CodeInvalid {
			t.Fatalf("ParseStoreID(%q) error = %v, want Invalid", value, err)
		}
	}

	statuses, err := ValidateStoreStatuses([]StoreStatus{
		{
			ID: "workspace/workflows", Readiness: StoreMigrationRequired,
			Error: NewError(CodeMigrationRequired, "offline migration is required"),
		},
		{ID: "global/auth", Readiness: StoreReady},
	})
	if err != nil {
		t.Fatalf("ValidateStoreStatuses() error = %v", err)
	}
	if len(statuses) != 2 || statuses[0].ID != "global/auth" || statuses[1].ID != "workspace/workflows" {
		t.Fatalf("statuses not sorted: %#v", statuses)
	}
	if statuses == nil {
		t.Fatal("validated empty/list projection must be detached")
	}
	if _, err := ValidateStoreStatuses([]StoreStatus{
		{ID: "global/auth", Readiness: StoreReady},
		{ID: "global/auth", Readiness: StoreUnavailable},
	}); CodeOf(err) != CodeIntegrity {
		t.Fatalf("duplicate store error = %v, want Integrity", err)
	}
	if _, err := ValidateStoreStatuses([]StoreStatus{{
		ID: "global/auth", Readiness: StoreReady, Error: NewError(CodeInternal, "unexpected"),
	}}); CodeOf(err) != CodeIntegrity {
		t.Fatalf("ready-with-error result = %v, want Integrity", err)
	}
}

func TestBrokerRequiredReadinessFailsClosed(t *testing.T) {
	status := BrokerStatus{
		RequiredStores: []StoreID{"global/auth"},
		Stores:         []StoreStatus{{ID: "global/auth", Readiness: StoreReady}},
	}
	if err := RequireBrokerReady(status); err != nil {
		t.Fatalf("ready required store rejected: %v", err)
	}
	status.Stores[0] = StoreStatus{
		ID: "global/auth", Readiness: StoreMigrationRequired,
		Error: NewError(CodeMigrationRequired, "offline migration is required"),
	}
	if err := RequireBrokerReady(status); CodeOf(err) != CodeMigrationRequired {
		t.Fatalf("migration readiness error = %v, want MigrationRequired", err)
	}
	status.RequiredStores = nil
	if err := RequireBrokerReady(status); CodeOf(err) != CodeUnavailable {
		t.Fatalf("missing required-store catalog error = %v, want Unavailable", err)
	}
}

func TestCanonicalJSONRejectsAlternateRepresentations(t *testing.T) {
	encoded, err := MarshalCanonical(map[string]any{"z": 2, "a": map[string]any{"y": 1, "x": 0}})
	if err != nil {
		t.Fatalf("MarshalCanonical() error = %v", err)
	}
	const expected = `{"a":{"x":0,"y":1},"z":2}`
	if string(encoded) != expected {
		t.Fatalf("canonical JSON = %s, want %s", encoded, expected)
	}
	var decoded map[string]any
	if err := UnmarshalCanonical(encoded, &decoded); err != nil {
		t.Fatalf("UnmarshalCanonical() error = %v", err)
	}
	for _, raw := range [][]byte{
		[]byte(`{"z":2,"a":{"x":0,"y":1}}`),
		[]byte(" {\"a\":1}"),
		[]byte(`{"a":1,"a":1}`),
		[]byte(`{"a":1.0}`),
		[]byte(`{"a":1e0}`),
		[]byte(`{"a":-0}`),
		[]byte("{\"a\":1}\n"),
	} {
		if err := UnmarshalCanonical(raw, &decoded); !errors.Is(err, ErrNonCanonicalJSON) {
			t.Fatalf("UnmarshalCanonical(%q) error = %v, want noncanonical", raw, err)
		}
	}
	for raw, expected := range map[string]string{
		"1.2300e2":               "123",
		"0.000001":               "0.000001",
		"0.0000001":              "1e-7",
		"1000000000000000000000": "1e21",
		"-0.0100":                "-0.01",
	} {
		actual, err := normalizeCanonicalNumber(raw)
		if err != nil || actual != expected {
			t.Errorf("normalizeCanonicalNumber(%q) = %q, %v; want %q", raw, actual, err, expected)
		}
	}
}

func TestLengthPrefixedFramesAreBoundedAndCanonical(t *testing.T) {
	type frameValue struct {
		Value string `json:"value"`
	}
	var buffer bytes.Buffer
	if err := WriteFrame(&buffer, frameValue{Value: "ok"}); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	var decoded frameValue
	if err := ReadFrame(&buffer, &decoded); err != nil || decoded.Value != "ok" {
		t.Fatalf("ReadFrame() = %#v, %v", decoded, err)
	}

	var oversized [4]byte
	binary.BigEndian.PutUint32(oversized[:], MaxFrameSize+1)
	if _, err := readFrameBytes(bytes.NewReader(oversized[:])); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized frame error = %v", err)
	}
	var empty [4]byte
	if _, err := readFrameBytes(bytes.NewReader(empty[:])); !errors.Is(err, ErrEmptyFrame) {
		t.Fatalf("empty frame error = %v", err)
	}

	noncanonical := []byte(`{"value": "ok"}`)
	var framed bytes.Buffer
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(noncanonical)))
	framed.Write(prefix[:])
	framed.Write(noncanonical)
	if err := ReadFrame(&framed, &decoded); !errors.Is(err, ErrNonCanonicalJSON) {
		t.Fatalf("noncanonical frame error = %v", err)
	}
}
