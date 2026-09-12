package sqliteprovider

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	moderncsqlite "modernc.org/sqlite"
)

type transactionBoundaryTestDriver struct {
	config transactionBoundaryTestConfig
}

type transactionBoundaryTestConfig struct {
	commitHook         bool
	commitErr          error
	rollbackErr        error
	hooksOnly          bool
	beforeCommitReturn func()
}

type transactionBoundaryTestConn struct {
	config       transactionBoundaryTestConfig
	commitHook   moderncsqlite.CommitHookFn
	rollbackHook moderncsqlite.RollbackHookFn
}

type transactionBoundaryTestHooksOnlyConn struct {
	commitHook   moderncsqlite.CommitHookFn
	rollbackHook moderncsqlite.RollbackHookFn
}

type transactionBoundaryTestTx struct{}

var transactionBoundaryDriverSequence atomic.Uint64

func (driverValue transactionBoundaryTestDriver) Open(string) (driver.Conn, error) {
	if driverValue.config.hooksOnly {
		return &transactionBoundaryTestHooksOnlyConn{}, nil
	}
	return &transactionBoundaryTestConn{config: driverValue.config}, nil
}

func (*transactionBoundaryTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}
func (*transactionBoundaryTestConn) Close() error { return nil }
func (*transactionBoundaryTestConn) Begin() (driver.Tx, error) {
	return transactionBoundaryTestTx{}, nil
}

func (conn *transactionBoundaryTestConn) RegisterCommitHook(callback moderncsqlite.CommitHookFn) {
	conn.commitHook = callback
}

func (conn *transactionBoundaryTestConn) RegisterRollbackHook(callback moderncsqlite.RollbackHookFn) {
	conn.rollbackHook = callback
}

func (conn *transactionBoundaryTestConn) ExecContext(
	ctx context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Result, error) {
	switch query {
	case "COMMIT":
		if conn.config.commitErr != nil {
			return nil, conn.config.commitErr
		}
		if conn.config.commitHook && conn.commitHook != nil && conn.commitHook() != 0 {
			if conn.rollbackHook != nil {
				conn.rollbackHook()
			}
			return nil, errors.New("commit hook denied transaction")
		}
		if conn.config.beforeCommitReturn != nil {
			conn.config.beforeCommitReturn()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return driver.RowsAffected(0), nil
	case "ROLLBACK":
		return driver.RowsAffected(0), conn.config.rollbackErr
	default:
		return driver.RowsAffected(0), nil
	}
}

func (*transactionBoundaryTestHooksOnlyConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}
func (*transactionBoundaryTestHooksOnlyConn) Close() error { return nil }
func (*transactionBoundaryTestHooksOnlyConn) Begin() (driver.Tx, error) {
	return transactionBoundaryTestTx{}, nil
}

func (conn *transactionBoundaryTestHooksOnlyConn) RegisterCommitHook(
	callback moderncsqlite.CommitHookFn,
) {
	conn.commitHook = callback
}

func (conn *transactionBoundaryTestHooksOnlyConn) RegisterRollbackHook(
	callback moderncsqlite.RollbackHookFn,
) {
	conn.rollbackHook = callback
}

func (transactionBoundaryTestTx) Commit() error   { return nil }
func (transactionBoundaryTestTx) Rollback() error { return nil }

func openTransactionBoundaryTestConnection(
	t *testing.T,
	config transactionBoundaryTestConfig,
) (*sql.DB, *sql.Conn) {
	t.Helper()
	name := fmt.Sprintf("transaction-boundary-test-%d", transactionBoundaryDriverSequence.Add(1))
	sql.Register(name, transactionBoundaryTestDriver{config: config})
	database, err := sql.Open(name, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	conn, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return database, conn
}

type transactionBoundaryFlipContext struct {
	context.Context
	calls atomic.Uint32
}

func (ctx *transactionBoundaryFlipContext) Err() error {
	if ctx.calls.Add(1) > 1 {
		return context.Canceled
	}
	return nil
}

func TestTransactionBoundaryDefensiveCommitCoverage(t *testing.T) {
	canary := errors.New("transaction boundary canary")
	t.Run("unsupported registrar", func(t *testing.T) {
		database := openProviderScript(t)
		conn, err := database.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		boundary := &TransactionBoundary{conn: conn, state: transactionBoundaryStarted}
		if err := boundary.Commit(t.Context()); err == nil {
			t.Fatal("unsupported registrar committed")
		}
	})

	t.Run("unsupported execution", func(t *testing.T) {
		_, conn := openTransactionBoundaryTestConnection(t, transactionBoundaryTestConfig{hooksOnly: true})
		boundary, err := NewTransactionBoundary(conn)
		if err != nil {
			t.Fatal(err)
		}
		if err := boundary.BeginAttempted(); err != nil {
			t.Fatal(err)
		}
		if err := boundary.Started(); err != nil {
			t.Fatal(err)
		}
		if err := boundary.Commit(t.Context()); err == nil {
			t.Fatal("hook-only driver committed")
		}
	})

	for _, test := range []struct {
		name      string
		config    transactionBoundaryTestConfig
		mutate    func(*TransactionBoundary)
		ctx       func() context.Context
		wantCause error
	}{
		{
			name:   "context changes inside raw",
			config: transactionBoundaryTestConfig{commitHook: true},
			ctx: func() context.Context {
				return &transactionBoundaryFlipContext{Context: context.Background()}
			},
			wantCause: context.Canceled,
		},
		{
			name:   "unstarted clean state",
			config: transactionBoundaryTestConfig{commitHook: true},
			mutate: func(boundary *TransactionBoundary) {
				boundary.state = transactionBoundaryInstalled
			},
			wantCause: errTransactionBoundary,
		},
		{
			name: "unstarted rollback failure",
			config: transactionBoundaryTestConfig{
				commitHook:  true,
				rollbackErr: canary,
			},
			mutate: func(boundary *TransactionBoundary) {
				boundary.state = transactionBoundaryInstalled
			},
			wantCause: canary,
		},
		{
			name:   "violated state",
			config: transactionBoundaryTestConfig{commitHook: true},
			mutate: func(boundary *TransactionBoundary) {
				boundary.violation = true
			},
			wantCause: errTransactionBoundary,
		},
		{
			name:      "missing commit hook event",
			config:    transactionBoundaryTestConfig{},
			wantCause: errTransactionBoundary,
		},
		{
			name: "commit and cleanup errors",
			config: transactionBoundaryTestConfig{
				commitHook:  true,
				commitErr:   canary,
				rollbackErr: errors.New("rollback canary"),
			},
			wantCause: canary,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, conn := openTransactionBoundaryTestConnection(t, test.config)
			boundary, err := NewTransactionBoundary(conn)
			if err != nil {
				t.Fatal(err)
			}
			if err := boundary.BeginAttempted(); err != nil {
				t.Fatal(err)
			}
			if err := boundary.Started(); err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				boundary.stateMu.Lock()
				test.mutate(boundary)
				boundary.stateMu.Unlock()
			}
			ctx := t.Context()
			if test.ctx != nil {
				ctx = test.ctx()
			}
			if err := boundary.Commit(ctx); !errors.Is(err, test.wantCause) {
				t.Fatalf("Commit() error = %v, want %v", err, test.wantCause)
			}
		})
	}

	t.Run("raw connection already closed", func(t *testing.T) {
		_, conn := openTransactionBoundaryTestConnection(t, transactionBoundaryTestConfig{commitHook: true})
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		boundary := &TransactionBoundary{conn: conn, state: transactionBoundaryStarted}
		if err := boundary.Commit(t.Context()); err == nil {
			t.Fatal("closed raw connection committed")
		}
	})
}

func TestTransactionBoundaryDefensiveCheckAndCloseCoverage(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, conn := openTransactionBoundaryTestConnection(t, transactionBoundaryTestConfig{commitHook: true})
	boundary, err := NewTransactionBoundary(conn)
	if err != nil {
		t.Fatal(err)
	}
	if attemptErr := boundary.BeginAttempted(); attemptErr != nil {
		t.Fatal(attemptErr)
	}
	if startErr := boundary.Started(); startErr != nil {
		t.Fatal(startErr)
	}
	if checkErr := boundary.Check(canceled); !errors.Is(checkErr, context.Canceled) {
		t.Fatalf("canceled Check() = %v", checkErr)
	}
	flipped := &transactionBoundaryFlipContext{Context: context.Background()}
	if checkErr := boundary.Check(flipped); !errors.Is(checkErr, context.Canceled) {
		t.Fatalf("in-Raw canceled Check() = %v", checkErr)
	}
	if closeErr := boundary.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	nilConnection := &TransactionBoundary{state: transactionBoundaryStarted}
	if closeErr := nilConnection.Close(); !errors.Is(closeErr, errTransactionBoundary) {
		t.Fatalf("nil-connection Close() = %v", closeErr)
	}

	database := openProviderScript(t)
	unsupported, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer unsupported.Close()
	unsupportedBoundary := &TransactionBoundary{
		conn:  unsupported,
		state: transactionBoundaryStarted,
	}
	if closeErr := unsupportedBoundary.Close(); closeErr == nil {
		t.Fatal("unsupported cleanup registrar closed cleanly")
	}

	_, hookOnly := openTransactionBoundaryTestConnection(t, transactionBoundaryTestConfig{hooksOnly: true})
	hookOnlyBoundary, err := NewTransactionBoundary(hookOnly)
	if err != nil {
		t.Fatal(err)
	}
	if attemptErr := hookOnlyBoundary.BeginAttempted(); attemptErr != nil {
		t.Fatal(attemptErr)
	}
	if startErr := hookOnlyBoundary.Started(); startErr != nil {
		t.Fatal(startErr)
	}
	if closeErr := hookOnlyBoundary.Close(); closeErr == nil {
		t.Fatal("hook-only cleanup closed cleanly")
	}

	saturated := &TransactionBoundary{
		state:   transactionBoundaryCommitArmed,
		commits: ^uint8(0),
	}
	if saturated.commitHook() == 0 || saturated.commits != ^uint8(0) {
		t.Fatal("saturated commit counter was accepted or wrapped")
	}
	saturated.rollbacks = ^uint8(0)
	saturated.rollbackHook()
	if saturated.rollbacks != ^uint8(0) || !saturated.violation {
		t.Fatal("saturated rollback counter wrapped or cleared violation")
	}
}

func TestTransactionBoundaryReportsActualCommitAfterPostArmCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	_, conn := openTransactionBoundaryTestConnection(t, transactionBoundaryTestConfig{
		commitHook:         true,
		beforeCommitReturn: cancel,
	})
	boundary, err := NewTransactionBoundary(conn)
	if err != nil {
		t.Fatal(err)
	}
	if err := boundary.BeginAttempted(); err != nil {
		t.Fatal(err)
	}
	if err := boundary.Started(); err != nil {
		t.Fatal(err)
	}
	if err := boundary.Commit(ctx); err != nil {
		t.Fatalf("durable post-arm commit reported failure: %v", err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("commit fixture did not cancel caller context: %v", ctx.Err())
	}
}
