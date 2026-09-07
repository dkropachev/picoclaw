//nolint:govet // Independent error-path assertions intentionally reuse err.
package sqlbridge

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestBrokerHandlerRequestAndTransactionBoundaries(t *testing.T) {
	handler, err := NewBrokerHandler(t.TempDir(), &config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	call := func(operation string, input any) (any, error) {
		t.Helper()
		payload, err := database.MarshalCanonical(input)
		if err != nil {
			t.Fatal(err)
		}
		return handler.Handle(t.Context(), database.Request{
			Domain: RPCDomain, Version: RPCVersion, Operation: operation, Payload: payload,
		})
	}
	target := Target{StoreID: mustStoreID(t, "channel/matrix/coverage"), Mode: ModeRuntime}
	offline := target
	offline.Mode = ModeOffline
	invalid := []struct {
		operation string
		input     any
	}{
		{RPCOperationResolve, ResolveChannelRequest{ChannelType: " matrix", ChannelName: "name"}},
		{RPCOperationResolve, ResolveChannelRequest{ChannelType: config.ChannelWeCom, ChannelName: "name"}},
		{RPCOperationPing, PingRequest{}},
		{RPCOperationExec, ExecRequest{Target: target, Statement: "CREATE TABLE bad(id INTEGER)"}},
		{RPCOperationQuery, QueryRequest{Target: target, Statement: "SELECT ?", Arguments: []Argument{{Ordinal: 0}}}},
		{RPCOperationBegin, BeginRequest{Target: target, Isolation: 99}},
		{RPCOperationRenew, TransactionRequest{Target: target, TransactionID: "bad/path"}},
	}
	for _, test := range invalid {
		if _, err := call(test.operation, test.input); database.CodeOf(err) != database.CodeInvalid &&
			database.CodeOf(err) != database.CodeUnsupported {
			t.Errorf("%s invalid error = %v", test.operation, err)
		}
	}
	if _, err := call(RPCOperationBegin, BeginRequest{Target: offline}); database.CodeOf(
		err,
	) != database.CodeUnauthorized {
		t.Fatalf("offline begin error = %v", err)
	}
	for _, operation := range []string{RPCOperationRenew, RPCOperationCommit, RPCOperationRollback} {
		if _, err := call(operation, TransactionRequest{Target: target, TransactionID: "tx-missing"}); database.CodeOf(
			err,
		) != database.CodeConflict {
			t.Errorf("%s missing transaction error = %v", operation, err)
		}
	}
	if _, err := call("unknown", PingRequest{Target: target}); database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("unknown operation error = %v", err)
	}
	if _, err := (*BrokerHandler)(nil).Handle(t.Context(), database.Request{}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("nil handler error = %v", err)
	}
	if got := (*BrokerHandler)(nil).effectiveTransactionTTL(); got != defaultBridgeTransactionTTL {
		t.Fatalf("nil TTL = %v", got)
	}
	handler.transactionTTL = time.Second
	if got := handler.effectiveTransactionTTL(); got != time.Second {
		t.Fatalf("configured TTL = %v", got)
	}
	response, renewErr := handler.renew(TransactionRequest{Target: target, TransactionID: "missing"})
	if database.CodeOf(renewErr) != database.CodeConflict || response.Accepted {
		t.Fatalf("renew missing = %#v, %v", response, renewErr)
	}
	finishResponse, finishErr := handler.finish(TransactionRequest{Target: target, TransactionID: "missing"}, true)
	if database.CodeOf(finishErr) != database.CodeConflict || finishResponse.Accepted {
		t.Fatalf("finish missing = %#v, %v", finishResponse, finishErr)
	}
}

func TestBrokerWireCodecAndBackendErrorMatrix(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	values := []Value{
		{Kind: ValueNull},
		{Kind: ValueInteger, Integer: -1},
		{Kind: ValueFloat, Float: 1.25},
		{Kind: ValueBoolean, Boolean: true},
		{Kind: ValueBytes, Bytes: []byte("bytes")},
		{Kind: ValueString, String: "text"},
		{Kind: ValueTime, Time: now.Format(time.RFC3339Nano)},
	}
	arguments := make([]Argument, len(values))
	for index, value := range values {
		arguments[index] = Argument{Name: "arg", Ordinal: index + 1, Value: value}
	}
	decoded, err := decodeArguments(arguments)
	if err != nil || len(decoded) != len(values) {
		t.Fatalf("decoded arguments = %#v, %v", decoded, err)
	}
	named, ok := decoded[1].(sql.NamedArg)
	if !ok || named.Name != "arg" || named.Value != int64(-1) {
		t.Fatalf("named argument = %#v", decoded[1])
	}
	if _, err := decodeArguments([]Argument{{Ordinal: 1, Value: Value{Kind: "bad"}}}); database.CodeOf(
		err,
	) != database.CodeInvalid {
		t.Fatalf("invalid decoded argument error = %v", err)
	}
	brokerValues := []any{nil, int64(-1), float64(2.5), true, []byte("bytes"), "text", now}
	for _, value := range brokerValues {
		wire, _, err := encodeBrokerValue(value)
		if err != nil {
			t.Errorf("encodeBrokerValue(%T) error = %v", value, err)
		}
		if _, err := decodeArgumentValue(wire); err != nil {
			t.Errorf("decodeArgumentValue(%#v) error = %v", wire, err)
		}
	}
	for _, value := range []any{math.NaN(), math.Inf(-1), string([]byte{0xff}), struct{}{}} {
		if _, _, err := encodeBrokerValue(value); database.CodeOf(err) != database.CodeIntegrity {
			t.Errorf("invalid broker value %T error = %v", value, err)
		}
	}
	if _, err := decodeArgumentValue(Value{Kind: ValueTime, Time: "not-time"}); err == nil {
		t.Fatal("invalid time decoded")
	}
	id, err := newBridgeTransactionID()
	if err != nil || !strings.HasPrefix(id, "tx-") || !validTransactionID(id) {
		t.Fatalf("transaction ID = %q, %v", id, err)
	}
	if bridgeBackendError(nil) != nil {
		t.Fatal("nil backend error mapped non-nil")
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if database.CodeOf(bridgeBackendError(err)) != database.CodeDeadline {
			t.Errorf("deadline mapping for %v failed", err)
		}
	}
	if database.CodeOf(bridgeBackendError(errors.New("boom"))) != database.CodeInternal {
		t.Fatal("internal backend mapping failed")
	}
	if err := (*BrokerHandler)(nil).Close(); err != nil {
		t.Fatalf("nil close = %v", err)
	}
}
