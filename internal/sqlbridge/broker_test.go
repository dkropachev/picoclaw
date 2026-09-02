//nolint:govet // Independent bridge assertions intentionally reuse err.
package sqlbridge

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	dbcatalog "github.com/sipeed/picoclaw/pkg/database/catalog"
)

func TestBrokerHandlerRequiresAuthenticatedSupervisorAuthority(t *testing.T) {
	restoreAuthority := database.SuspendProviderTestAuthority()
	t.Cleanup(restoreAuthority)
	home := t.TempDir()
	if handler, err := NewBrokerHandler(home, &config.Config{}); handler != nil ||
		database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced SQL bridge handler = %#v, %v", handler, err)
	}
}

func TestBrokerHandlerExecQueryAndTransaction(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	storeRoot := filepath.Join(home, "matrix-data")
	matrix := &config.Channel{Enabled: true, Type: config.ChannelMatrix}
	if err := matrix.Decode(&config.MatrixSettings{
		CryptoDatabasePath: storeRoot,
		CryptoPassphrase:   "configured",
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Agents:   config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
		Channels: config.ChannelsConfig{"secure matrix": matrix},
	}
	physicalPath := filepath.Join(storeRoot, "store.db")
	physical, err := sqliteprovider.OpenStore(physicalPath, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := physical.Exec(`CREATE TABLE bridge_values (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		value TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if err := physical.Close(); err != nil {
		t.Fatal(err)
	}

	logical, err := dbcatalog.New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	storeID, err := logical.LookupChannel(config.ChannelMatrix, "secure matrix")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	resolvedStoreID, err := ResolveChannelStore(
		t.Context(), client, config.ChannelMatrix, "secure matrix",
	)
	if err != nil || resolvedStoreID != storeID {
		t.Fatalf("resolved Matrix store = %q, %v; want %q", resolvedStoreID, err, storeID)
	}
	if _, err := ResolveChannelStore(
		t.Context(), client, config.ChannelMatrix, "uncataloged",
	); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("uncataloged Matrix resolution error = %v", err)
	}
	dsn, err := EncodeDSN(storeID, ModeRuntime)
	if err != nil {
		t.Fatal(err)
	}
	connector, err := NewDriver(NewBrokerRPC(client)).OpenConnector(dsn)
	if err != nil {
		t.Fatal(err)
	}
	bridgeDB := sql.OpenDB(connector)
	defer bridgeDB.Close()

	if _, err := bridgeDB.ExecContext(context.Background(),
		`INSERT INTO bridge_values(value) VALUES (?)`, "one"); err != nil {
		t.Fatal(err)
	}
	transaction, err := bridgeDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(context.Background(),
		`INSERT INTO bridge_values(value) VALUES (?)`, "two"); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := bridgeDB.QueryContext(context.Background(),
		`SELECT value FROM bridge_values ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0] != "one" || values[1] != "two" {
		t.Fatalf("bridge values = %v", values)
	}
	handler.mu.Lock()
	handler.transactionTTL = 40 * time.Millisecond
	handler.mu.Unlock()
	target := Target{StoreID: storeID, Mode: ModeRuntime}
	var begun BeginResponse
	if err := client.CallWithOptions(
		t.Context(), RPCDomain, RPCVersion, RPCOperationBegin,
		BeginRequest{Target: target}, &begun, database.CallOptions{Mutation: true},
	); err != nil {
		t.Fatal(err)
	}
	var inserted ExecResponse
	if err := client.CallWithOptions(
		t.Context(), RPCDomain, RPCVersion, RPCOperationExec,
		ExecRequest{
			Target: target, TransactionID: begun.TransactionID,
			Statement: "INSERT INTO bridge_values(value) VALUES ('expired')",
		},
		&inserted, database.CallOptions{Mutation: true},
	); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * handler.transactionTTL)
	var committed TransactionResponse
	err = client.CallWithOptions(
		t.Context(), RPCDomain, RPCVersion, RPCOperationCommit,
		TransactionRequest{Target: target, TransactionID: begun.TransactionID},
		&committed, database.CallOptions{Mutation: true},
	)
	if database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("expired transaction commit error = %v", err)
	}
	var count QueryResponse
	if err := client.Call(
		t.Context(), RPCDomain, RPCVersion, RPCOperationQuery,
		QueryRequest{Target: target, Statement: "SELECT COUNT(*) FROM bridge_values"}, &count,
	); err != nil {
		t.Fatal(err)
	}
	if len(count.Rows) != 1 || len(count.Rows[0]) != 1 || count.Rows[0][0].Integer != 2 {
		t.Fatalf("expired transaction was not rolled back: %#v", count)
	}
	if _, err := bridgeDB.Exec(
		`CREATE TABLE forbidden (id INTEGER)`,
	); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("runtime DDL error = %v, want Unsupported", err)
	}
}
