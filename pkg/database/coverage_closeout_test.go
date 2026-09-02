//nolint:govet // Independent failure-boundary assertions intentionally reuse narrow error names.
package database

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type coverageFrameWriter struct {
	writes int
	errAt  int
	short  bool
}

func (writer *coverageFrameWriter) Write(payload []byte) (int, error) {
	writer.writes++
	if writer.writes == writer.errAt {
		return 0, errors.New("frame writer canary")
	}
	if writer.short && len(payload) > 0 {
		return 0, nil
	}
	return len(payload), nil
}

func TestCoverageCanonicalJSONBoundaries(t *testing.T) {
	if _, err := MarshalCanonical(func() {}); err == nil {
		t.Fatal("MarshalCanonical accepted an unsupported value")
	}
	canonical, err := MarshalCanonical(map[string]any{
		"z": json.Number("1e3"),
		"a": []any{json.Number("0.000001"), json.Number("1e-7"), json.Number("1e21")},
	})
	if err != nil || string(canonical) != `{"a":[0.000001,1e-7,1e21],"z":1000}` {
		t.Fatalf("canonical JSON = %s, %v", canonical, err)
	}
	var decoded map[string]any
	if err := UnmarshalCanonical(canonical, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		raw  string
		dest any
	}{
		{name: "malformed", raw: `{`},
		{name: "whitespace", raw: ` {}`},
		{name: "key order", raw: `{"z":1,"a":2}`},
		{name: "trailing value", raw: `{}[]`},
		{name: "trailing malformed", raw: `{}x`},
		{name: "nil destination", raw: `{}`, dest: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := test.dest
			if test.name != "nil destination" {
				destination = &decoded
			}
			if err := UnmarshalCanonical([]byte(test.raw), destination); err == nil {
				t.Fatal("invalid canonical JSON accepted")
			}
		})
	}
	var strict struct {
		Value int `json:"value"`
	}
	if err := unmarshalCanonicalStrict([]byte(`{"unknown":1}`), &strict); err == nil {
		t.Fatal("strict decoder accepted an unknown field")
	}
	if err := unmarshalCanonicalStrict([]byte(` {"value":1}`), &strict); err == nil {
		t.Fatal("strict decoder accepted whitespace")
	}
	if err := unmarshalCanonicalStrict([]byte(`{`), &strict); err == nil {
		t.Fatal("strict decoder accepted malformed JSON")
	}
	if _, err := (canonicalNumber("")).MarshalJSON(); err == nil {
		t.Fatal("empty canonical number marshaled")
	}
	for _, raw := range []string{"1e+", "1.", ".1"} {
		if _, err := normalizeCanonicalNumber(raw); err == nil {
			t.Fatalf("invalid canonical number %q accepted", raw)
		}
	}
	for raw, want := range map[string]string{
		"-0": "0", "100.00": "100", "0.00120": "0.0012", "12.34": "12.34", "-1E+3": "-1000",
		"1234567890123456789010": "1.23456789012345678901e21",
	} {
		if got, err := normalizeCanonicalNumber(raw); err != nil || got != want {
			t.Fatalf("normalize %q = %q, %v; want %q", raw, got, err, want)
		}
	}
	var channel chan int
	if err := UnmarshalCanonical([]byte(`{}`), &channel); err == nil {
		t.Fatal("canonical JSON decoded into unsupported destination")
	}
	if err := unmarshalCanonicalStrict([]byte(`{}`), nil); err == nil {
		t.Fatal("strict canonical JSON decoded into nil destination")
	}
	if _, err := canonicalizeJSON([]byte(`1e99999999999`)); err == nil {
		t.Fatal("canonical JSON accepted overflowing exponent")
	}
	for _, value := range []any{
		json.Number("1e99999999999"),
		[]any{json.Number("1e99999999999")},
		map[string]any{"value": json.Number("1e99999999999")},
	} {
		if _, err := normalizeCanonicalNumbers(value); err == nil {
			t.Fatalf("invalid nested canonical number accepted: %#v", value)
		}
	}
}

func TestCoverageStructuredErrorBoundaries(t *testing.T) {
	var nilError *Error
	if nilError.Error() != "" {
		t.Fatal("nil structured error emitted text")
	}
	if got := (&Error{Code: CodeInvalid}).Error(); got != string(CodeInvalid) {
		t.Fatalf("blank structured error = %q", got)
	}
	if CodeOf(nil) != "" || CodeOf(errors.New("plain")) != CodeInternal {
		t.Fatal("CodeOf classification mismatch")
	}
	if NewError("future", "secret").Code != CodeInternal {
		t.Fatal("invalid error code escaped wire contract")
	}
	allCodes := []ErrorCode{
		CodeUnavailable, CodeMigrationRequired, CodeConflict, CodeNotFound, CodeAlreadyExists,
		CodeDeadline, CodeIntegrity, CodeInvalid, CodeUnauthorized, CodeUnsupported,
		CodeOutcomeUnknown, CodeInternal,
	}
	for _, code := range allCodes {
		err := NewError(code, "")
		if err.Code != code || strings.TrimSpace(err.Message) == "" || !errors.Is(err, &Error{Code: code}) {
			t.Fatalf("default %s error = %#v", code, err)
		}
	}
	message := strings.Repeat("é", maxStructuredErrorMessageBytes) + "\n\tsecret"
	bounded := NewError(CodeInternal, message)
	if len(bounded.Message) > maxStructuredErrorMessageBytes || !strings.Contains(bounded.Error(), "Internal:") {
		t.Fatalf("bounded error = %#v", bounded)
	}
	invalidBoundary := NewError(CodeInternal, strings.Repeat("a", maxStructuredErrorMessageBytes-1)+"é")
	if len(invalidBoundary.Message) != maxStructuredErrorMessageBytes-1 {
		t.Fatalf("UTF-8 boundary message length = %d", len(invalidBoundary.Message))
	}
	if protocolError(nil) != nil || protocolError(context.DeadlineExceeded).Code != CodeDeadline ||
		protocolError(context.Canceled).Code != CodeDeadline ||
		protocolError(NewError(CodeNotFound, "missing")).Code != CodeNotFound ||
		protocolError(errors.New("driver detail")).Code != CodeInternal {
		t.Fatal("protocol error mapping mismatch")
	}
}

func TestCoverageWindowsSIDValidationEdges(t *testing.T) {
	if validWindowsSIDString("S-1-2") || validWindowsSIDString("S-1--21") ||
		validWindowsSIDString("S-"+strings.Repeat("1", 185)) {
		t.Fatal("invalid Windows SID edge accepted")
	}
}

func TestCoverageProtocolValidationBoundaries(t *testing.T) {
	valid := RequestEnvelope{
		Protocol: ProtocolVersion, RequestID: "request_1", Token: strings.Repeat("a", 64),
		BrokerEpoch: strings.Repeat("b", 32), Domain: "domain.one", DomainVersion: 1,
		Operation: "read-item", DeadlineUnixNs: time.Now().Add(time.Second).UnixNano(),
		Payload: json.RawMessage(`{}`), IdempotencyKey: "stable:key",
	}
	if err := validRequestEnvelope(valid); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*RequestEnvelope){
		func(value *RequestEnvelope) { value.Protocol++ },
		func(value *RequestEnvelope) { value.RequestID = "bad request" },
		func(value *RequestEnvelope) { value.Domain = "Bad" },
		func(value *RequestEnvelope) { value.Operation = "-bad" },
		func(value *RequestEnvelope) { value.DomainVersion = 0 },
		func(value *RequestEnvelope) { value.IdempotencyKey = " padded " },
		func(value *RequestEnvelope) { value.DeadlineUnixNs = 0 },
		func(value *RequestEnvelope) { value.Payload = []byte(`[]`) },
	}
	for index, mutate := range mutations {
		candidate := valid
		mutate(&candidate)
		if err := validRequestEnvelope(candidate); err == nil {
			t.Fatalf("invalid request mutation %d accepted", index)
		}
	}
	response := ResponseEnvelope{
		Protocol: ProtocolVersion, RequestID: valid.RequestID, BrokerEpoch: valid.BrokerEpoch,
		Payload: json.RawMessage(`{}`),
	}
	if err := validResponseEnvelope(response, valid.RequestID, valid.BrokerEpoch); err != nil {
		t.Fatal(err)
	}
	badResponses := []ResponseEnvelope{
		{Protocol: 2, RequestID: valid.RequestID, BrokerEpoch: valid.BrokerEpoch, Payload: []byte(`{}`)},
		{Protocol: 1, RequestID: "other", BrokerEpoch: valid.BrokerEpoch, Payload: []byte(`{}`)},
		{Protocol: 1, RequestID: valid.RequestID, BrokerEpoch: "other", Payload: []byte(`{}`)},
		{Protocol: 1, RequestID: valid.RequestID, BrokerEpoch: valid.BrokerEpoch},
		{
			Protocol:    1,
			RequestID:   valid.RequestID,
			BrokerEpoch: valid.BrokerEpoch,
			Payload:     []byte(`{}`),
			Error:       NewError(CodeInvalid, "bad"),
		},
		{
			Protocol:    1,
			RequestID:   valid.RequestID,
			BrokerEpoch: valid.BrokerEpoch,
			Error:       &Error{Code: "bad", Message: "bad"},
		},
		{Protocol: 1, RequestID: valid.RequestID, BrokerEpoch: valid.BrokerEpoch, Error: &Error{Code: CodeInvalid}},
		{Protocol: 1, RequestID: valid.RequestID, BrokerEpoch: valid.BrokerEpoch, Payload: []byte(`[]`)},
	}
	for index, candidate := range badResponses {
		if err := validResponseEnvelope(candidate, valid.RequestID, valid.BrokerEpoch); err == nil {
			t.Fatalf("invalid response %d accepted", index)
		}
	}
	if !validRequestID("A-z_9") || validRequestID("") || validRequestID("bad id") ||
		validRequestID(strings.Repeat("a", maxRequestIDBytes+1)) {
		t.Fatal("request ID validation mismatch")
	}
	if !validProtocolName("a.b-c_d", 20) || validProtocolName("", 20) ||
		validProtocolName("A", 20) || validProtocolName("-bad", 20) ||
		validProtocolName(strings.Repeat("a", 21), 20) {
		t.Fatal("protocol name validation mismatch")
	}
	if !validIdempotencyKey("visible:stable") || validIdempotencyKey("") ||
		validIdempotencyKey(" padded ") || validIdempotencyKey("bad\nkey") ||
		validIdempotencyKey(strings.Repeat("x", maxIdempotencyKeyBytes+1)) {
		t.Fatal("idempotency key validation mismatch")
	}
	called := false
	handler := HandlerFunc(func(context.Context, Request) (any, error) { called = true; return EmptyPayload{}, nil })
	if _, err := handler.Handle(t.Context(), Request{}); err != nil || !called {
		t.Fatal("HandlerFunc did not dispatch")
	}
}

func TestCoverageStoreStatusAndReadinessBoundaries(t *testing.T) {
	if StoreID("x").IsZero() || !StoreID("").IsZero() || StoreID("global/auth").String() != "global/auth" {
		t.Fatal("StoreID helpers mismatch")
	}
	for _, invalid := range []string{"", " padded", "a//b", "a/../b", "A", "-a", "a:b"} {
		if _, err := ParseStoreID(invalid); err == nil {
			t.Fatalf("invalid store ID %q accepted", invalid)
		}
	}
	if _, err := ParseStoreID("global/auth-store_1.v2"); err != nil {
		t.Fatal(err)
	}
	if StoreReadiness("future").Valid() {
		t.Fatal("future readiness accepted")
	}
	for _, readiness := range []StoreReadiness{
		StoreReady, StoreMigrationRequired, StoreIntegrityFailed, StoreUnavailable,
	} {
		if !readiness.Valid() {
			t.Fatalf("readiness %q rejected", readiness)
		}
	}
	validStatuses := []StoreStatus{
		{ID: "workspace/workflows", Readiness: StoreUnavailable, Error: NewError(CodeUnavailable, "offline")},
		{ID: "global/auth", Readiness: StoreReady},
	}
	validated, err := ValidateStoreStatuses(validStatuses)
	if err != nil || validated[0].ID != "global/auth" || validated[1].Error == validStatuses[0].Error {
		t.Fatalf("validated statuses = %#v, %v", validated, err)
	}
	invalidStatuses := [][]StoreStatus{
		{{ID: "bad id", Readiness: StoreReady}},
		{{ID: "global/auth", Readiness: StoreReady}, {ID: "global/auth", Readiness: StoreReady}},
		{{ID: "global/auth", Readiness: "future"}},
		{{ID: "global/auth", Readiness: StoreReady, Error: NewError(CodeInternal, "bad")}},
		{{ID: "global/auth", Readiness: StoreUnavailable, Error: &Error{Code: "future", Message: "bad"}}},
		{{ID: "global/auth", Readiness: StoreUnavailable, Error: &Error{Code: CodeUnavailable}}},
	}
	for index, statuses := range invalidStatuses {
		if _, err := ValidateStoreStatuses(statuses); err == nil {
			t.Fatalf("invalid status set %d accepted", index)
		}
	}
	if (StoreStatus{ID: "global/auth", Readiness: StoreReady}).String() != "global/auth:ready" {
		t.Fatal("StoreStatus String mismatch")
	}

	if RequireBrokerReady(BrokerStatus{}) == nil {
		t.Fatal("empty broker readiness accepted")
	}
	readyStatus := BrokerStatus{
		RequiredStores: []StoreID{"global/auth"},
		Stores:         []StoreStatus{{ID: "global/auth", Readiness: StoreReady}},
	}
	if err := RequireBrokerReady(readyStatus); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*BrokerStatus){
		func(status *BrokerStatus) { status.RequiredStores[0] = "bad id" },
		func(status *BrokerStatus) { status.RequiredStores = append(status.RequiredStores, "global/auth") },
		func(status *BrokerStatus) { status.Stores = nil },
		func(status *BrokerStatus) { status.Stores[0].Readiness = StoreUnavailable },
		func(status *BrokerStatus) {
			status.Stores[0].Readiness = StoreMigrationRequired
			status.Stores[0].Error = NewError(CodeMigrationRequired, "migrate")
		},
	} {
		candidate := BrokerStatus{
			RequiredStores: append([]StoreID(nil), readyStatus.RequiredStores...),
			Stores:         append([]StoreStatus(nil), readyStatus.Stores...),
		}
		mutate(&candidate)
		if err := RequireBrokerReady(candidate); err == nil {
			t.Fatal("invalid broker readiness accepted")
		}
	}
}

func TestCoverageFrameBoundaries(t *testing.T) {
	if err := writeFrameBytes(nil, []byte(`{}`)); err == nil {
		t.Fatal("nil frame writer accepted")
	}
	if err := writeFrameBytes(io.Discard, nil); !errors.Is(err, ErrEmptyFrame) {
		t.Fatalf("empty frame error = %v", err)
	}
	if err := writeFrameBytes(io.Discard, make([]byte, uint64(MaxFrameSize)+1)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversize frame error = %v", err)
	}
	for _, failure := range []struct {
		name   string
		errAt  int
		short  bool
		prefix string
	}{
		{name: "length", errAt: 1, prefix: "length"},
		{name: "body", errAt: 2, prefix: "body"},
		{name: "short", short: true, prefix: "length"},
	} {
		writer := &coverageFrameWriter{errAt: failure.errAt, short: failure.short}
		if err := writeFrameBytes(writer, []byte(`{}`)); err == nil || !strings.Contains(err.Error(), failure.prefix) {
			t.Fatalf("%s frame write = %v", failure.name, err)
		}
	}
	if _, err := readFrameBytes(nil); err == nil {
		t.Fatal("nil frame reader accepted")
	}
	if _, err := readFrameBytes(bytes.NewReader([]byte{0, 0})); err == nil {
		t.Fatal("short length accepted")
	}
	var frame bytes.Buffer
	_ = binary.Write(&frame, binary.BigEndian, uint32(0))
	if _, err := readFrameBytes(&frame); !errors.Is(err, ErrEmptyFrame) {
		t.Fatalf("zero frame = %v", err)
	}
	frame.Reset()
	_ = binary.Write(&frame, binary.BigEndian, MaxFrameSize+1)
	if _, err := readFrameBytes(&frame); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("large frame = %v", err)
	}
	frame.Reset()
	_ = binary.Write(&frame, binary.BigEndian, uint32(4))
	frame.WriteString("{}")
	if _, err := readFrameBytes(&frame); err == nil {
		t.Fatal("short frame body accepted")
	}
	if err := WriteFrame(io.Discard, func() {}); err == nil {
		t.Fatal("unsupported frame value accepted")
	}
	if err := ReadFrame(strings.NewReader(""), &map[string]any{}); err == nil {
		t.Fatal("empty framed stream accepted")
	}
}

func TestCoverageManifestValidationAndLifecycle(t *testing.T) {
	home := t.TempDir()
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(home); CodeOf(err) != CodeUnavailable {
		t.Fatalf("missing manifest = %v", err)
	}
	token, _ := randomHex(tokenBytes)
	epoch, _ := randomHex(epochBytes)
	valid := Manifest{
		PID: os.Getpid(), Protocol: ProtocolVersion, Token: token,
		Endpoint: endpointForStateDirectory(stateDir), Epoch: epoch,
	}
	for _, candidate := range []Manifest{
		{},
		{PID: 1, Protocol: 2, Token: token, Epoch: epoch, Endpoint: valid.Endpoint},
		{PID: 1, Protocol: 1, Token: "BAD", Epoch: epoch, Endpoint: valid.Endpoint},
		{PID: 1, Protocol: 1, Token: token, Epoch: "BAD", Endpoint: valid.Endpoint},
		{PID: 1, Protocol: 1, Token: token, Epoch: epoch, Endpoint: "foreign"},
	} {
		if err := validateManifest(candidate, stateDir); err == nil {
			t.Fatalf("invalid manifest accepted: %#v", candidate)
		}
	}
	if err := writeManifest(stateDir, valid); err != nil {
		t.Fatal(err)
	}
	read, err := ReadManifest(home)
	if err != nil || read != valid {
		t.Fatalf("read manifest = %#v, %v", read, err)
	}
	if err := removeManifestForEpoch(home, "wrong"); CodeOf(err) != CodeConflict {
		t.Fatalf("wrong epoch removal = %v", err)
	}
	if err := removeManifestForEpoch(home, epoch); err != nil {
		t.Fatal(err)
	}
	if err := removeManifestForEpoch(home, epoch); err != nil {
		t.Fatalf("repeat manifest removal = %v", err)
	}
	if _, err := randomHex(0); CodeOf(err) != CodeInvalid {
		t.Fatalf("invalid random size = %v", err)
	}
	if validLowerHex("ABCDEF", 6) || validLowerHex("abcd", 6) || !validLowerHex("abcdef", 6) {
		t.Fatal("lower hex validation mismatch")
	}
}

func TestCoverageManifestRejectsUnsafeFiles(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload []byte
		mode    os.FileMode
	}{
		{name: "empty", payload: nil, mode: 0o600},
		{name: "noncanonical", payload: []byte(" {}"), mode: 0o600},
		{name: "oversize", payload: bytes.Repeat([]byte("x"), int(manifestMaxBytes)+1), mode: 0o600},
		{name: "public mode", payload: []byte(`{}`), mode: 0o644},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			stateDir, err := prepareStateDirectory(home)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(stateDir, manifestFileName)
			if err := os.WriteFile(path, test.payload, test.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadManifest(home); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		home := t.TempDir()
		stateDir, _ := prepareStateDirectory(home)
		target := filepath.Join(stateDir, "target")
		if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(stateDir, manifestFileName)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := ReadManifest(home); CodeOf(err) != CodeIntegrity {
			t.Fatalf("symlink manifest = %v", err)
		}
	})
}

func TestCoverageInheritedAuthorityLifecycle(t *testing.T) {
	original := RuntimeClient()
	t.Cleanup(func() { InstallProcessClient(original) })
	InstallProcessClient(nil)
	if RuntimeClient() != nil {
		t.Fatal("nil process client was not installed")
	}
	if _, err := InheritedAuthorityEnvironment("bad\x00home"); err == nil {
		t.Fatal("invalid inherited authority home accepted")
	}
	if _, _, err := ConnectInherited(t.Context()); CodeOf(err) != CodeUnauthorized {
		t.Fatalf("missing inherited authority = %v", err)
	}
	for _, encoded := range []string{
		"%%%", base64.RawURLEncoding.EncodeToString([]byte("not json")),
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte("x"), (64<<10)+1)),
	} {
		t.Setenv(inheritedAuthorityEnvironment, encoded)
		if _, _, err := ConnectInherited(t.Context()); CodeOf(err) != CodeUnauthorized {
			t.Fatalf("invalid inherited authority = %v", err)
		}
	}

	home := t.TempDir()
	server, err := StartServer(context.Background(), ServerOptions{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeServer(t, server) })
	environment, err := InheritedAuthorityEnvironment(home)
	if err != nil {
		t.Fatal(err)
	}
	_, encoded, found := strings.Cut(environment, "=")
	if !found {
		t.Fatalf("authority environment = %q", environment)
	}
	t.Setenv(inheritedAuthorityEnvironment, encoded)
	client, canonicalHome, err := ConnectInherited(nil)
	if err != nil || client == nil || canonicalHome == "" || RuntimeClient() != client {
		t.Fatalf("inherited connection = %p, %q, %v", client, canonicalHome, err)
	}
	if os.Getenv(inheritedAuthorityEnvironment) != "" {
		t.Fatal("inherited authority was not consumed")
	}
}

func TestCoverageServerDirectControlAndDomainBoundaries(t *testing.T) {
	if (*Server)(nil).Manifest() != (Manifest{}) {
		t.Fatal("nil server returned a manifest")
	}
	select {
	case <-(*Server)(nil).Done():
	default:
		t.Fatal("nil server Done channel remained open")
	}
	if err := (*Server)(nil).Close(nil); err != nil {
		t.Fatal(err)
	}
	(*Server)(nil).initiateShutdown()

	if _, err := StartServer(nil, ServerOptions{Home: "bad\x00home"}); err == nil {
		t.Fatal("server accepted invalid home")
	}
	if _, err := StartServer(
		nil,
		ServerOptions{Home: t.TempDir(), CatalogFingerprint: "bad"},
	); CodeOf(
		err,
	) != CodeInvalid {
		t.Fatalf("invalid catalog fingerprint = %v", err)
	}
	if _, err := StartServer(nil, ServerOptions{
		Home: t.TempDir(), RequiredStores: []StoreID{"bad id"},
	}); CodeOf(err) != CodeInvalid {
		t.Fatalf("invalid required store = %v", err)
	}
	if _, err := StartServer(nil, ServerOptions{
		Home: t.TempDir(), RequiredStores: []StoreID{"global/auth", "global/auth"},
	}); CodeOf(err) != CodeIntegrity {
		t.Fatalf("duplicate required store = %v", err)
	}

	server := &Server{
		manifest: Manifest{PID: 42, Epoch: "epoch"}, startedAt: time.Now(),
		requiredStores: []StoreID{"global/auth"}, now: time.Now,
	}
	for _, request := range []Request{
		{Domain: ControlDomain, Version: 2, Operation: ControlOperationPing, Payload: []byte(`{}`)},
		{Domain: ControlDomain, Version: 1, Operation: ControlOperationPing, Payload: []byte(` {}`)},
		{Domain: ControlDomain, Version: 1, Operation: "unknown", Payload: []byte(`{}`)},
		{Domain: "unknown", Version: 1, Operation: "read", Payload: []byte(`{}`)},
	} {
		if _, _, err := server.handle(t.Context(), request); err == nil {
			t.Fatalf("invalid direct request accepted: %#v", request)
		}
	}
	result, shutdown, err := server.handle(t.Context(), Request{
		Domain: ControlDomain, Version: 1, Operation: ControlOperationPing, Payload: []byte(`{}`),
	})
	if err != nil || shutdown || result.(PingResponse).PID != 42 {
		t.Fatalf("direct ping = %#v, %v, %v", result, shutdown, err)
	}
	result, shutdown, err = server.handle(t.Context(), Request{
		Domain: ControlDomain, Version: 1, Operation: ControlOperationShutdown, Payload: []byte(`{}`),
	})
	if err != nil || !shutdown || !result.(ShutdownResponse).Accepted {
		t.Fatalf("direct shutdown = %#v, %v, %v", result, shutdown, err)
	}

	server.statusProvider = func(context.Context) ([]StoreStatus, error) {
		return nil, errors.New("status canary")
	}
	if _, _, err := server.handle(t.Context(), Request{
		Domain: ControlDomain, Version: 1, Operation: ControlOperationStatus, Payload: []byte(`{}`),
	}); err == nil {
		t.Fatal("status provider error ignored")
	}
	server.statusProvider = func(context.Context) ([]StoreStatus, error) {
		return []StoreStatus{{ID: "bad id", Readiness: StoreReady}}, nil
	}
	if _, _, err := server.handle(t.Context(), Request{
		Domain: ControlDomain, Version: 1, Operation: ControlOperationStatus, Payload: []byte(`{}`),
	}); err == nil {
		t.Fatal("invalid status provider result accepted")
	}
	server.statusProvider = func(context.Context) ([]StoreStatus, error) { return nil, nil }
	if _, _, err := server.handle(t.Context(), Request{
		Domain: ControlDomain, Version: 1, Operation: ControlOperationStatus, Payload: []byte(`{}`),
	}); err == nil {
		t.Fatal("missing required status accepted")
	}
	server.statusProvider = func(context.Context) ([]StoreStatus, error) {
		return []StoreStatus{{ID: "global/auth", Readiness: StoreReady}}, nil
	}
	if _, _, err := server.handle(t.Context(), Request{
		Domain: ControlDomain, Version: 1, Operation: ControlOperationStatus, Payload: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	server.handler = HandlerFunc(func(context.Context, Request) (any, error) { panic("handler panic") })
	if _, _, err := server.handle(t.Context(), Request{Domain: "domain"}); CodeOf(err) != CodeInternal {
		t.Fatalf("handler panic = %v", err)
	}
	server.handler = HandlerFunc(func(context.Context, Request) (any, error) {
		return EmptyPayload{}, errors.New("domain canary")
	})
	if _, _, err := server.handle(t.Context(), Request{Domain: "domain"}); err == nil {
		t.Fatal("handler error ignored")
	}

	if validated, err := validateRequiredStores(
		[]StoreID{"workspace/x", "global/auth"},
	); err != nil ||
		validated[0] != "global/auth" {
		t.Fatalf("required store sorting = %#v, %v", validated, err)
	}
	if err := requiredStoresHaveStatuses(nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := requiredStoresHaveStatuses([]StoreID{"global/auth"}, nil); CodeOf(err) != CodeIntegrity {
		t.Fatalf("missing required status = %v", err)
	}
	if safeRequestID("bad id") != "invalid" || safeRequestID("good_id") != "good_id" {
		t.Fatal("safe request ID mismatch")
	}
	if payload, err := marshalPayload(nil); err != nil || string(payload) != `{}` {
		t.Fatalf("nil payload = %s, %v", payload, err)
	}
	if _, err := marshalPayload([]string{"not", "object"}); CodeOf(err) != CodeInvalid {
		t.Fatalf("array payload = %v", err)
	}
	if _, err := marshalPayload(func() {}); err == nil {
		t.Fatal("unsupported payload marshaled")
	}
	if err := callCloseHandler(func() error { panic("close panic") }); CodeOf(err) != CodeInternal {
		t.Fatalf("close handler panic = %v", err)
	}
	if canary := errors.New("close canary"); !errors.Is(callCloseHandler(func() error { return canary }), canary) {
		t.Fatal("close handler error changed")
	}
}

func TestCoverageServerDispatchAndStartupFailureBoundaries(t *testing.T) {
	server := newIdempotencyTestServer(HandlerFunc(func(context.Context, Request) (any, error) {
		return func() {}, nil
	}))
	envelope := idempotencyTestEnvelope(t, "stable", "request", "value")
	response, _ := server.dispatchContext(nil, envelope)
	if CodeOf(response.Error) != CodeInternal {
		t.Fatalf("invalid domain result = %#v", response)
	}
	envelope.IdempotencyKey = ""
	envelope.Payload = []byte(`[]`)
	response, _ = server.dispatch(envelope)
	if CodeOf(response.Error) != CodeInvalid {
		t.Fatalf("invalid request payload = %#v", response)
	}

	home := t.TempDir()
	first, err := StartServer(context.Background(), ServerOptions{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if second, err := StartServer(context.Background(), ServerOptions{Home: home}); second != nil ||
		CodeOf(err) != CodeAlreadyExists {
		t.Fatalf("duplicate server = %#v, %v", second, err)
	}
	closeServer(t, first)

	fencedHome := t.TempDir()
	fence, err := AcquireMigrationFence(fencedHome)
	if err != nil {
		t.Fatal(err)
	}
	if started, err := StartServer(context.Background(), ServerOptions{Home: fencedHome}); started != nil ||
		CodeOf(err) != CodeConflict {
		t.Fatalf("server through migration fence = %#v, %v", started, err)
	}
	_ = fence.Close()

	manifestHome := t.TempDir()
	stateDir, err := prepareStateDirectory(manifestHome)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(stateDir, manifestFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if started, err := StartServer(context.Background(), ServerOptions{Home: manifestHome}); started != nil ||
		CodeOf(err) != CodeIntegrity {
		t.Fatalf("server with unsafe manifest = %#v, %v", started, err)
	}
}

func TestCoverageServerCloseDeadlineAndHandlerErrors(t *testing.T) {
	home := t.TempDir()
	release := make(chan struct{})
	server, err := StartServer(context.Background(), ServerOptions{
		Home: home,
		CloseHandler: func() error {
			<-release
			return errors.New("close handler canary")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.Close(ctx); CodeOf(err) != CodeDeadline {
		t.Fatalf("canceled server close = %v", err)
	}
	close(release)
	select {
	case <-server.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("server close handler did not finish")
	}
	if err := server.Close(nil); err == nil || !strings.Contains(err.Error(), "close handler canary") {
		t.Fatalf("server close error = %v", err)
	}
}

func TestCoverageServerShutdownCallbackPanicIsContained(t *testing.T) {
	server, err := StartServer(context.Background(), ServerOptions{
		Home: t.TempDir(),
		OnShutdownRequested: func() {
			panic("shutdown callback")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := ConnectWithManifest(server.home, server.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("panic shutdown callback did not finish")
	}
	if err := server.Close(nil); CodeOf(err) != CodeInternal {
		t.Fatalf("shutdown callback close error = %v", err)
	}
}

func TestCoverageClientLocalValidation(t *testing.T) {
	var client *Client
	if client.Epoch() != "" || client.Refresh() == nil || client.Call(nil, "d", 1, "o", nil, nil) == nil ||
		client.CallWithOptions(nil, "d", 1, "o", nil, nil, CallOptions{}) == nil {
		t.Fatal("nil client validation mismatch")
	}
	if _, err := Connect("bad\x00home"); CodeOf(err) == "" {
		t.Fatal("invalid client home accepted")
	}
	if _, err := Connect(t.TempDir()); CodeOf(err) != CodeUnavailable {
		t.Fatalf("missing broker connect = %v", err)
	}
	if _, err := ConnectWithManifest("bad\x00home", Manifest{}); err == nil {
		t.Fatal("inherited client accepted invalid home")
	}
	if err := safeDiscoveryError(nil); err != nil {
		t.Fatal(err)
	}
	if CodeOf(safeDiscoveryError(os.ErrPermission)) != CodeUnauthorized ||
		CodeOf(safeDiscoveryError(NewError(CodeIntegrity, "bad"))) != CodeIntegrity ||
		CodeOf(safeDiscoveryError(errors.New("plain"))) != CodeUnavailable {
		t.Fatal("safe discovery error mapping mismatch")
	}
	if CodeOf(dispatchedCallError(true, nil)) != CodeOutcomeUnknown ||
		CodeOf(dispatchedCallError(false, context.Canceled)) != CodeDeadline ||
		CodeOf(dispatchedCallError(false, nil)) != CodeUnavailable {
		t.Fatal("dispatched call error mapping mismatch")
	}

	home := t.TempDir()
	server, err := StartServer(context.Background(), ServerOptions{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeServer(t, server) })
	client, err = ConnectWithManifest(home, server.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CallWithOptions(
		t.Context(),
		"Bad",
		1,
		"read",
		EmptyPayload{},
		nil,
		CallOptions{},
	); CodeOf(
		err,
	) != CodeInvalid {
		t.Fatalf("invalid domain call = %v", err)
	}
	if err := client.CallWithOptions(
		t.Context(),
		"domain",
		1,
		"read",
		func() {},
		nil,
		CallOptions{},
	); CodeOf(
		err,
	) != CodeInvalid {
		t.Fatalf("invalid payload call = %v", err)
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := client.CallWithOptions(
		expired,
		"domain",
		1,
		"read",
		EmptyPayload{},
		nil,
		CallOptions{},
	); CodeOf(
		err,
	) != CodeDeadline {
		t.Fatalf("expired call = %v", err)
	}
}

func TestCoverageIdempotencyRegistryLimitsAndCancellation(t *testing.T) {
	envelope := RequestEnvelope{
		RequestID: "request", Domain: "domain", DomainVersion: 1, Operation: "mutate",
		IdempotencyKey: "stable", Payload: []byte(`{"value":1}`),
	}
	if record, replay, shutdown, err := (*idempotencyRegistry)(
		nil,
	).begin(t.Context(), RequestEnvelope{}); err != nil || record != nil || replay != nil ||
		shutdown {
		t.Fatalf("unkeyed nil registry = %#v, %#v, %v, %v", record, replay, shutdown, err)
	}
	if _, _, _, err := (*idempotencyRegistry)(nil).begin(t.Context(), envelope); CodeOf(err) != CodeUnavailable {
		t.Fatalf("keyed nil registry = %v", err)
	}
	registry := newIdempotencyRegistry()
	record, _, _, err := registry.begin(t.Context(), envelope)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := registry.begin(canceled, envelope); CodeOf(err) != CodeDeadline {
		t.Fatalf("waiting replay cancellation = %v", err)
	}
	response := ResponseEnvelope{
		Protocol: 1, RequestID: "request", BrokerEpoch: "epoch",
		Payload: []byte(`{"ok":true}`),
	}
	completed, shutdown := registry.complete(record, response, true)
	if !shutdown || string(completed.Payload) != string(response.Payload) {
		t.Fatalf("completed idempotency result = %#v, %v", completed, shutdown)
	}
	completed.Payload[0] = 'x'
	_, replay, replayShutdown, err := registry.begin(t.Context(), envelope)
	if err != nil || replay == nil || !replayShutdown || string(replay.Payload) != string(response.Payload) {
		t.Fatalf("detached replay = %#v, %v, %v", replay, replayShutdown, err)
	}
	if got, flag := (*idempotencyRegistry)(nil).complete(nil, response, true); !flag ||
		string(got.Payload) != string(response.Payload) {
		t.Fatal("nil registry completion changed response")
	}
	if got, flag := registry.complete(nil, response, true); !flag || string(got.Payload) == "" {
		t.Fatal("nil record completion changed response")
	}

	full := newIdempotencyRegistry()
	for index := 0; index < maxIdempotencyRecords; index++ {
		full.records[string(rune(index+1))] = &idempotencyRecord{}
	}
	if _, _, _, err := full.begin(t.Context(), envelope); CodeOf(err) != CodeUnavailable {
		t.Fatalf("full idempotency registry = %v", err)
	}
	bounded := newIdempotencyRegistry()
	bounded.resultBytes = maxIdempotencyResultBytes
	boundedRecord := &idempotencyRecord{ready: make(chan struct{})}
	stored, storedShutdown := bounded.complete(boundedRecord, response, true)
	if storedShutdown || CodeOf(stored.Error) != CodeOutcomeUnknown || len(stored.Payload) != 0 {
		t.Fatalf("bounded idempotency response = %#v, shutdown=%v", stored, storedShutdown)
	}
	withError := cloneResponseEnvelope(ResponseEnvelope{
		Payload: []byte("payload"), Error: NewError(CodeConflict, "conflict"),
	})
	if withError.Error == nil || responseEnvelopeBytes(withError) <= len(withError.Payload) {
		t.Fatal("response clone/size mismatch")
	}
}

func TestCoverageHomeAndStateValidation(t *testing.T) {
	for _, invalid := range []string{"", " padded ", "bad\x00home"} {
		if _, err := CanonicalHome(invalid); CodeOf(err) != CodeInvalid {
			t.Fatalf("invalid canonical home %q = %v", invalid, err)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := CanonicalHome(missing); CodeOf(err) != CodeUnavailable {
		t.Fatalf("missing canonical home = %v", err)
	}
	prepared, err := PrepareHome(missing)
	if err != nil || prepared != missing {
		t.Fatalf("prepared home = %q, %v", prepared, err)
	}
	if canonical, err := CanonicalHome(missing); err != nil || canonical != prepared {
		t.Fatalf("canonical home = %q, %v", canonical, err)
	}
	fileHome := filepath.Join(t.TempDir(), "home-file")
	if err := os.WriteFile(fileHome, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareHome(fileHome); CodeOf(err) != CodeInvalid {
		t.Fatalf("file home = %v", err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(missing, alias); err == nil {
		if _, err := CanonicalHome(alias); CodeOf(err) != CodeInvalid {
			t.Fatalf("symlink home = %v", err)
		}
		if err := rejectExistingAncestorAlias(filepath.Join(alias, "child")); CodeOf(err) != CodeInvalid {
			t.Fatalf("symlink ancestor = %v", err)
		}
	}
	if _, err := StateDirectory(missing); CodeOf(err) != CodeUnavailable {
		t.Fatalf("missing state directory = %v", err)
	}
	stateDir, err := prepareStateDirectory(missing)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := StateDirectory(missing); err != nil || got != stateDir {
		t.Fatalf("state directory = %q, %v", got, err)
	}
	if err := os.Chmod(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := StateDirectory(missing); CodeOf(err) != CodeIntegrity {
		t.Fatalf("public state directory = %v", err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareStateDirectory(missing); CodeOf(err) != CodeIntegrity {
		t.Fatalf("file state boundary = %v", err)
	}
}

func TestCoverageManifestExistingBoundaryAndInvalidContent(t *testing.T) {
	home := t.TempDir()
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := randomHex(tokenBytes)
	epoch, _ := randomHex(epochBytes)
	valid := Manifest{
		PID: os.Getpid(), Protocol: ProtocolVersion, Token: token, Epoch: epoch,
		Endpoint: endpointForStateDirectory(stateDir),
	}
	if err := writeManifest(stateDir, Manifest{}); err == nil {
		t.Fatal("invalid manifest published")
	}
	path := filepath.Join(stateDir, manifestFileName)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(stateDir, valid); CodeOf(err) != CodeIntegrity {
		t.Fatalf("directory manifest boundary = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	invalidRaw, err := MarshalCanonical(Manifest{
		PID: 1, Protocol: ProtocolVersion, Token: "bad", Epoch: epoch, Endpoint: valid.Endpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, invalidRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(home); CodeOf(err) != CodeIntegrity {
		t.Fatalf("canonical invalid manifest = %v", err)
	}
	if err := removeManifestForEpoch(home, epoch); CodeOf(err) != CodeIntegrity {
		t.Fatalf("invalid manifest removal = %v", err)
	}
	if validLowerHex("gg", 2) {
		t.Fatal("non-hex lowercase identity accepted")
	}
}
