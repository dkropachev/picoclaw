//nolint:govet // Independent driver assertions intentionally reuse err.
package sqlbridge

import (
	"database/sql/driver"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestDriverConnectionStatementAndTransactionBoundaries(t *testing.T) {
	target := Target{StoreID: mustStoreID(t, "channel.matrix.coverage"), Mode: ModeRuntime}
	dsn, err := EncodeDSN(target.StoreID, target.Mode)
	if err != nil {
		t.Fatal(err)
	}
	rpc := &recordingRPC{
		pingResponse: PingResponse{Ready: true}, execResponse: ExecResponse{RowsAffected: 1},
		queryResponse: QueryResponse{
			Columns: []string{"value"},
			Rows:    [][]Value{{{Kind: ValueString, String: "ok"}}},
		},
		beginResponse:    BeginResponse{TransactionID: "tx-coverage", TTLNanoSeconds: int64(time.Minute)},
		renewResponse:    TransactionLeaseResponse{Accepted: true, TTLNanoSeconds: int64(time.Minute)},
		rollbackResponse: TransactionResponse{Accepted: true}, commitResponse: TransactionResponse{Accepted: true},
	}
	conntr, err := NewDriver(rpc).OpenConnector(dsn)
	if err != nil || conntr.Driver() == nil {
		t.Fatalf("connector = %#v, %v", conntr, err)
	}
	raw, err := conntr.Connect(nil)
	if err != nil {
		t.Fatal(err)
	}
	conn := raw.(*connection)
	if err := conn.Ping(nil); err != nil || !conn.IsValid() {
		t.Fatalf("ping/valid = %v/%v", err, conn.IsValid())
	}
	rpc.pingResponse.Ready = false
	if database.CodeOf(conn.Ping(t.Context())) != database.CodeUnavailable {
		t.Fatal("not-ready ping succeeded")
	}
	rpc.pingResponse.Ready = true
	if err := conn.CheckNamedValue(nil); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil named value error = %v", err)
	}
	named := &driver.NamedValue{Ordinal: 1, Value: int(7)}
	if err := conn.CheckNamedValue(named); err != nil || named.Value != int64(7) {
		t.Fatalf("converted value = %#v, %v", named, err)
	}
	if err := conn.CheckNamedValue(&driver.NamedValue{Value: make(chan int)}); database.CodeOf(
		err,
	) != database.CodeInvalid {
		t.Fatalf("unsupported named value error = %v", err)
	}

	prepared, err := conn.Prepare("SELECT ?")
	if err != nil || prepared.NumInput() != -1 {
		t.Fatalf("prepared = %#v, %v", prepared, err)
	}
	if _, err := prepared.Exec([]driver.Value{"value"}); err != nil {
		t.Fatal(err)
	}
	rows, err := prepared.Query([]driver.Value{"value"})
	if err != nil {
		t.Fatal(err)
	}
	destination := make([]driver.Value, 1)
	if err := rows.Next(destination); err != nil || destination[0] != "ok" {
		t.Fatalf("row = %#v, %v", destination, err)
	}
	if err := rows.Close(); err != nil || rows.Next(destination) != io.EOF {
		t.Fatalf("closed rows = %v", err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := prepared.Exec(nil); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed statement exec error = %v", err)
	}
	if _, err := prepared.Query(nil); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed statement query error = %v", err)
	}
	if _, err := (&statement{}).ExecContext(t.Context(), nil); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("detached statement error = %v", err)
	}

	tx, err := conn.BeginTx(nil, driver.TxOptions{})
	if err != nil || conn.IsValid() || conn.ResetSession(t.Context()) != driver.ErrBadConn {
		t.Fatalf("begin state = %#v, %v", tx, err)
	}
	if _, err := conn.Begin(); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("nested begin error = %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("second rollback error = %v", err)
	}
	if err := conn.ResetSession(t.Context()); err != nil || !conn.IsValid() {
		t.Fatalf("reset = %v, valid=%v", err, conn.IsValid())
	}

	rpc.beginResponse = BeginResponse{TransactionID: "bad/path", TTLNanoSeconds: 1}
	if _, err := conn.Begin(); database.CodeOf(err) != database.CodeIntegrity || conn.IsValid() {
		t.Fatalf("bad begin response = %v, valid=%v", err, conn.IsValid())
	}
	if database.CodeOf(conn.Ping(t.Context())) != database.CodeUnavailable ||
		conn.ResetSession(t.Context()) != driver.ErrBadConn {
		t.Fatal("unusable connection remained available")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (*connection)(nil).Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := (&connector{}).Connect(t.Context()); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("invalid connector error = %v", err)
	}
	if err := (*transaction)(nil).Commit(); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil transaction error = %v", err)
	}
}

func TestDriverValueCodecAndResponseBoundaries(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	values := []driver.Value{nil, int64(-7), float64(1.5), true, []byte("bytes"), "text", now}
	for _, input := range values {
		wire, _, err := encodeValue(input)
		if err != nil {
			t.Fatalf("encodeValue(%T) error = %v", input, err)
		}
		decoded, _, err := decodeValue(wire)
		if err != nil {
			t.Fatalf("decodeValue(%#v) error = %v", wire, err)
		}
		if bytes, ok := decoded.([]byte); ok && string(bytes) != "bytes" {
			t.Fatalf("decoded bytes = %q", bytes)
		}
	}
	invalidInputs := []driver.Value{
		math.NaN(), math.Inf(1), strings.Repeat("x", MaxValueBytes+1), "bad\x00string",
		time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC),
		struct{}{},
	}
	for _, input := range invalidInputs {
		if _, _, err := encodeValue(input); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("encodeValue(%T) error = %v", input, err)
		}
	}
	invalidWire := []Value{
		{Kind: ValueNull, Integer: 1},
		{Kind: ValueInteger, String: "extra"},
		{Kind: ValueFloat, Float: math.NaN()},
		{Kind: ValueBoolean, Integer: 1},
		{Kind: ValueBytes, Bytes: []byte("x"), String: "extra"},
		{Kind: ValueString, String: "bad\x00string"},
		{Kind: ValueTime, Time: "not-time"},
		{Kind: "unknown"},
	}
	for _, input := range invalidWire {
		if _, _, err := decodeValue(input); database.CodeOf(err) != database.CodeIntegrity {
			t.Errorf("decodeValue(%#v) error = %v", input, err)
		}
	}
	if _, err := decodeRows(QueryResponse{Columns: []string{"a"}, Rows: [][]Value{{}}}); database.CodeOf(
		err,
	) != database.CodeIntegrity {
		t.Fatalf("row width error = %v", err)
	}
	if _, err := decodeRows(QueryResponse{Columns: []string{"bad\x00column"}}); database.CodeOf(
		err,
	) != database.CodeIntegrity {
		t.Fatalf("column error = %v", err)
	}
	if _, err := decodeRows(QueryResponse{Columns: make([]string, MaxResultColumns+1)}); database.CodeOf(
		err,
	) != database.CodeIntegrity {
		t.Fatalf("column bound error = %v", err)
	}
	if _, err := decodeRows(QueryResponse{Rows: make([][]Value, MaxResultRows+1)}); database.CodeOf(
		err,
	) != database.CodeIntegrity {
		t.Fatalf("row bound error = %v", err)
	}
	rows := &bridgeRows{columns: []string{"a"}, rows: [][]driver.Value{{[]byte("copy")}}}
	if err := rows.Next(nil); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("destination width error = %v", err)
	}
	destination := make([]driver.Value, 1)
	if err := rows.Next(destination); err != nil {
		t.Fatal(err)
	}
	destination[0].([]byte)[0] = 'X'
	if string(rows.rows[0][0].([]byte)) != "copy" {
		t.Fatal("row bytes aliased destination")
	}

	tooMany := make([]driver.NamedValue, MaxArguments+1)
	if _, err := encodeArguments(tooMany); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("argument count error = %v", err)
	}
	invalidNamed := []driver.NamedValue{
		{Ordinal: 1, Name: strings.Repeat("n", maximumArgumentNameBytes+1), Value: int64(1)},
		{Ordinal: 1, Name: "bad\x00name", Value: int64(1)},
	}
	for _, input := range invalidNamed {
		if _, err := encodeArguments([]driver.NamedValue{input}); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("invalid named argument error = %v", err)
		}
	}
	if got := namedValues([]driver.Value{"a", int64(2)}); len(got) != 2 || got[1].Ordinal != 2 {
		t.Fatalf("namedValues = %#v", got)
	}
	for _, value := range []string{"tx-1", "ABC_123"} {
		if !validTransactionID(value) {
			t.Errorf("valid transaction ID rejected: %q", value)
		}
	}
	for _, value := range []string{"", "bad/path", strings.Repeat("x", maximumTransactionIDBytes+1)} {
		if validTransactionID(value) {
			t.Errorf("invalid transaction ID accepted: %q", value)
		}
	}
	if !containsNUL("a\x00b") || containsNUL("abc") {
		t.Fatal("containsNUL mismatch")
	}
}
