//nolint:govet // Independent driver assertions intentionally reuse err.
package sqlbridge

import (
	"context"
	"database/sql/driver"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestDriverRoutesValidatedTypedOperations(t *testing.T) {
	t.Parallel()

	targetID := mustStoreID(t, "channel.matrix.primary-a1b2c3d4")
	dsn, err := EncodeDSN(targetID, ModeRuntime)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 1, 12, 34, 56, 123, time.FixedZone("test", -4*60*60))
	rpc := &recordingRPC{
		pingResponse: PingResponse{Ready: true},
		execResponse: ExecResponse{LastInsertID: 17, RowsAffected: 2},
		queryResponse: QueryResponse{
			Columns: []string{"id", "payload", "created"},
			Rows: [][]Value{{
				{Kind: ValueInteger, Integer: 17},
				{Kind: ValueBytes, Bytes: []byte("detached")},
				{Kind: ValueTime, Time: now.Format(time.RFC3339Nano)},
			}},
		},
		beginResponse:    BeginResponse{TransactionID: "txn_1", TTLNanoSeconds: int64(time.Minute)},
		renewResponse:    TransactionLeaseResponse{Accepted: true, TTLNanoSeconds: int64(time.Minute)},
		commitResponse:   TransactionResponse{Accepted: true},
		rollbackResponse: TransactionResponse{Accepted: true},
	}
	bridge := NewDriver(rpc)
	connector, err := bridge.OpenConnector(dsn)
	if err != nil {
		t.Fatal(err)
	}
	rawConnection, err := connector.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	connection := rawConnection.(*connection)

	bytesArgument := []byte("input")
	result, err := connection.ExecContext(context.Background(),
		"INSERT INTO records(id, payload, created) VALUES (?, ?, ?)",
		[]driver.NamedValue{
			{Ordinal: 1, Value: int64(17)},
			{Ordinal: 2, Name: "payload", Value: bytesArgument},
			{Ordinal: 3, Value: now},
		})
	if err != nil {
		t.Fatal(err)
	}
	bytesArgument[0] = 'X'
	if id, err := result.LastInsertId(); err != nil || id != 17 {
		t.Fatalf("LastInsertId() = %d, %v", id, err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 2 {
		t.Fatalf("RowsAffected() = %d, %v", affected, err)
	}

	rows, err := connection.QueryContext(context.Background(), "SELECT id, payload, created FROM records", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := rows.Columns(); len(got) != 3 || got[0] != "id" || got[2] != "created" {
		t.Fatalf("Columns() = %#v", got)
	}
	destination := make([]driver.Value, 3)
	if err := rows.Next(destination); err != nil {
		t.Fatal(err)
	}
	if destination[0] != int64(17) || string(destination[1].([]byte)) != "detached" ||
		!destination[2].(time.Time).Equal(now) {
		t.Fatalf("row = %#v", destination)
	}
	if err := rows.Next(destination); err != io.EOF {
		t.Fatalf("second Next() = %v, want EOF", err)
	}

	tx, err := connection.BeginTx(context.Background(), driver.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), "UPDATE records SET payload=?", []driver.NamedValue{{
		Ordinal: 1, Value: "updated",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}

	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	if len(rpc.pings) != 1 || rpc.pings[0].Target.StoreID != targetID {
		t.Fatalf("pings = %#v", rpc.pings)
	}
	if len(rpc.execs) != 2 {
		t.Fatalf("exec count = %d, want 2", len(rpc.execs))
	}
	if got := string(rpc.execs[0].Arguments[1].Value.Bytes); got != "input" {
		t.Fatalf("encoded bytes aliased caller: %q", got)
	}
	if rpc.execs[1].TransactionID != "txn_1" {
		t.Fatalf("transactional exec ID = %q", rpc.execs[1].TransactionID)
	}
	if len(rpc.commits) != 1 || rpc.commits[0].TransactionID != "txn_1" {
		t.Fatalf("commits = %#v", rpc.commits)
	}
}

func TestDriverEnforcesModeBeforeRPCAndRollsBackOnClose(t *testing.T) {
	t.Parallel()

	rpc := &recordingRPC{
		pingResponse:     PingResponse{Ready: true},
		execResponse:     ExecResponse{RowsAffected: 1},
		beginResponse:    BeginResponse{TransactionID: "txn_close", TTLNanoSeconds: int64(time.Minute)},
		renewResponse:    TransactionLeaseResponse{Accepted: true, TTLNanoSeconds: int64(time.Minute)},
		commitResponse:   TransactionResponse{Accepted: true},
		rollbackResponse: TransactionResponse{Accepted: true},
	}
	id := mustStoreID(t, "channel.whatsapp.primary-01234567")
	runtimeDSN, _ := EncodeDSN(id, ModeRuntime)
	runtimeRaw, err := NewDriver(rpc).Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	runtimeConnection := runtimeRaw.(*connection)
	if _, err := runtimeConnection.ExecContext(
		context.Background(),
		"CREATE TABLE forbidden(id INTEGER)",
		nil,
	); err == nil {
		t.Fatal("runtime DDL unexpectedly reached RPC")
	}

	offlineDSN, _ := EncodeDSN(id, ModeOffline)
	offlineRaw, err := NewDriver(rpc).Open(offlineDSN)
	if err != nil {
		t.Fatal(err)
	}
	offlineConnection := offlineRaw.(*connection)
	if _, err := offlineConnection.ExecContext(
		context.Background(),
		"CREATE TABLE allowed(id INTEGER)",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := offlineConnection.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := offlineConnection.Close(); err != nil {
		t.Fatal(err)
	}

	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	if len(rpc.execs) != 1 || rpc.execs[0].Statement != "CREATE TABLE allowed(id INTEGER)" {
		t.Fatalf("execs = %#v", rpc.execs)
	}
	if len(rpc.rollbacks) != 1 || rpc.rollbacks[0].TransactionID != "txn_close" {
		t.Fatalf("rollbacks = %#v", rpc.rollbacks)
	}
}

func TestDriverRejectsInvalidAuthorityArgumentsAndResponses(t *testing.T) {
	t.Parallel()

	if _, err := NewDriver(nil).Open("/tmp/store.db"); err == nil {
		t.Fatal("nil RPC accepted a path DSN")
	}
	rpc := &recordingRPC{pingResponse: PingResponse{Ready: true}}
	if _, err := NewDriver(rpc).Open("file:/tmp/store.db"); err == nil {
		t.Fatal("driver accepted a URI DSN")
	}

	id := mustStoreID(t, "channel.matrix.primary-a1b2c3d4")
	dsn, _ := EncodeDSN(id, ModeRuntime)
	raw, err := NewDriver(rpc).Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	connection := raw.(*connection)
	if _, err := connection.ExecContext(
		context.Background(),
		"INSERT INTO records(value) VALUES (?)",
		[]driver.NamedValue{{
			Ordinal: 0, Value: "bad ordinal",
		}},
	); err == nil {
		t.Fatal("invalid argument ordinal accepted")
	}
	if _, err := connection.ExecContext(
		context.Background(),
		"INSERT INTO records(value) VALUES (?)",
		[]driver.NamedValue{{
			Ordinal: 1, Value: make(chan int),
		}},
	); err == nil {
		t.Fatal("unsupported argument accepted")
	}

	rpc.queryResponse = QueryResponse{
		Columns: []string{"one", "two"},
		Rows:    [][]Value{{{Kind: ValueInteger, Integer: 1}}},
	}
	if _, err := connection.QueryContext(context.Background(), "SELECT 1, 2", nil); err == nil ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid row response error = %v", err)
	}
	rpc.beginResponse = BeginResponse{TransactionID: "../../physical"}
	if _, err := connection.Begin(); err == nil || database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid transaction response error = %v", err)
	}
}

func TestDriverRenewsTransactionUntilCommit(t *testing.T) {
	rpc := &recordingRPC{
		pingResponse: PingResponse{Ready: true},
		beginResponse: BeginResponse{
			TransactionID: "txn_heartbeat", TTLNanoSeconds: int64(60 * time.Millisecond),
		},
		renewResponse: TransactionLeaseResponse{
			Accepted: true, TTLNanoSeconds: int64(60 * time.Millisecond),
		},
		commitResponse: TransactionResponse{Accepted: true},
	}
	id := mustStoreID(t, "channel.matrix.heartbeat")
	dsn, err := EncodeDSN(id, ModeRuntime)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := NewDriver(rpc).Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	connection := raw.(*connection)
	transaction, err := connection.Begin()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(140 * time.Millisecond)
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	rpc.mu.Lock()
	renewCount := len(rpc.renews)
	rpc.mu.Unlock()
	if renewCount < 2 {
		t.Fatalf("transaction renewals = %d, want at least two", renewCount)
	}
}

type recordingRPC struct {
	mu sync.Mutex

	pingResponse     PingResponse
	execResponse     ExecResponse
	queryResponse    QueryResponse
	beginResponse    BeginResponse
	renewResponse    TransactionLeaseResponse
	commitResponse   TransactionResponse
	rollbackResponse TransactionResponse

	pings     []PingRequest
	execs     []ExecRequest
	queries   []QueryRequest
	begins    []BeginRequest
	commits   []TransactionRequest
	renews    []TransactionRequest
	rollbacks []TransactionRequest
}

func (rpc *recordingRPC) Ping(_ context.Context, request PingRequest) (PingResponse, error) {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	rpc.pings = append(rpc.pings, request)
	return rpc.pingResponse, nil
}

func (rpc *recordingRPC) Exec(_ context.Context, request ExecRequest) (ExecResponse, error) {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	request.Arguments = cloneArguments(request.Arguments)
	rpc.execs = append(rpc.execs, request)
	return rpc.execResponse, nil
}

func (rpc *recordingRPC) Query(_ context.Context, request QueryRequest) (QueryResponse, error) {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	rpc.queries = append(rpc.queries, request)
	return rpc.queryResponse, nil
}

func (rpc *recordingRPC) Begin(_ context.Context, request BeginRequest) (BeginResponse, error) {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	rpc.begins = append(rpc.begins, request)
	return rpc.beginResponse, nil
}

func (rpc *recordingRPC) Renew(
	_ context.Context,
	request TransactionRequest,
) (TransactionLeaseResponse, error) {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	rpc.renews = append(rpc.renews, request)
	return rpc.renewResponse, nil
}

func (rpc *recordingRPC) Commit(
	_ context.Context,
	request TransactionRequest,
) (TransactionResponse, error) {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	rpc.commits = append(rpc.commits, request)
	return rpc.commitResponse, nil
}

func (rpc *recordingRPC) Rollback(
	_ context.Context,
	request TransactionRequest,
) (TransactionResponse, error) {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	rpc.rollbacks = append(rpc.rollbacks, request)
	return rpc.rollbackResponse, nil
}

func cloneArguments(arguments []Argument) []Argument {
	result := append([]Argument(nil), arguments...)
	for index := range result {
		result[index].Value.Bytes = append([]byte(nil), result[index].Value.Bytes...)
	}
	return result
}

var _ RPC = (*recordingRPC)(nil)
