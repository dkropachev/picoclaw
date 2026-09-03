//nolint:govet // Independent failure-boundary assertions intentionally reuse narrow error names.
package database

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type coverageFrameWriter struct {
	writes int
	errAt  int
	short  bool
}

type observedWaitContext struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func (ctx *observedWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
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
	conflicting := envelope
	conflicting.Payload = []byte(`{"value":2}`)
	if _, _, _, err := registry.begin(t.Context(), conflicting); CodeOf(err) != CodeConflict {
		t.Fatalf("reused idempotency key error = %v", err)
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
	waitContext := &observedWaitContext{
		Context: context.Background(),
		waiting: make(chan struct{}),
	}
	type waitResult struct {
		replay   *ResponseEnvelope
		shutdown bool
		err      error
	}
	waited := make(chan waitResult, 1)
	go func() {
		_, replay, shutdown, waitErr := registry.begin(waitContext, envelope)
		waited <- waitResult{replay: replay, shutdown: shutdown, err: waitErr}
	}()
	select {
	case <-waitContext.waiting:
	case <-time.After(time.Second):
		t.Fatal("idempotency replay did not begin waiting")
	}
	completed, shutdown := registry.complete(record, response, true)
	if !shutdown || string(completed.Payload) != string(response.Payload) {
		t.Fatalf("completed idempotency result = %#v, %v", completed, shutdown)
	}
	select {
	case result := <-waited:
		if result.err != nil || result.replay == nil || !result.shutdown ||
			string(result.replay.Payload) != string(response.Payload) {
			t.Fatalf("coalesced idempotency replay = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("idempotency replay was not released by completion")
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

func TestStrictRequestPayloadAndFrameDecoding(t *testing.T) {
	type payload struct {
		Value int `json:"value"`
	}
	encoded, err := MarshalCanonical(payload{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Payload: encoded}
	var decoded payload
	if err := request.DecodePayload(&decoded); err != nil || decoded.Value != 7 {
		t.Fatalf("decoded request payload = %#v, %v", decoded, err)
	}

	var framed bytes.Buffer
	if err := WriteFrame(&framed, payload{Value: 9}); err != nil {
		t.Fatal(err)
	}
	decoded = payload{}
	if err := readFrameStrict(&framed, &decoded); err != nil || decoded.Value != 9 {
		t.Fatalf("strict framed payload = %#v, %v", decoded, err)
	}
}
