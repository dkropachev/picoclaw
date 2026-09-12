package sqlitestore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	moderncsqlite "modernc.org/sqlite"

	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

var (
	transactionBoundaryBeginDriverID atomic.Uint64
	errNoTestTransaction             = errors.New("test transaction is not active")
)

type transactionBoundaryBeginDriver struct {
	attempts  *atomic.Uint32
	commitErr error
}

type transactionBoundaryBeginConn struct {
	attempts     *atomic.Uint32
	commitErr    error
	active       bool
	commitHook   moderncsqlite.CommitHookFn
	rollbackHook moderncsqlite.RollbackHookFn
}

type transactionBoundaryBeginTx struct{}

type transactionBoundaryUnsupportedDriver struct{}

type transactionBoundaryUnsupportedConn struct{}

func (value transactionBoundaryBeginDriver) Open(string) (driver.Conn, error) {
	return &transactionBoundaryBeginConn{
		attempts:  value.attempts,
		commitErr: value.commitErr,
	}, nil
}

func (transactionBoundaryUnsupportedDriver) Open(string) (driver.Conn, error) {
	return transactionBoundaryUnsupportedConn{}, nil
}

func (transactionBoundaryUnsupportedConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}

func (transactionBoundaryUnsupportedConn) Close() error { return nil }

func (transactionBoundaryUnsupportedConn) Begin() (driver.Tx, error) {
	return transactionBoundaryBeginTx{}, nil
}

func (*transactionBoundaryBeginConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}
func (*transactionBoundaryBeginConn) Close() error { return nil }
func (*transactionBoundaryBeginConn) Begin() (driver.Tx, error) {
	return transactionBoundaryBeginTx{}, nil
}

func (conn *transactionBoundaryBeginConn) RegisterCommitHook(
	callback moderncsqlite.CommitHookFn,
) {
	conn.commitHook = callback
}

func (conn *transactionBoundaryBeginConn) RegisterRollbackHook(
	callback moderncsqlite.RollbackHookFn,
) {
	conn.rollbackHook = callback
}

func (conn *transactionBoundaryBeginConn) ExecContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Result, error) {
	switch query {
	case "BEGIN IMMEDIATE":
		conn.active = true
		if conn.attempts.Add(1) == 1 {
			return nil, context.Canceled
		}
		return driver.RowsAffected(0), nil
	case "COMMIT":
		if !conn.active {
			return nil, errNoTestTransaction
		}
		if conn.commitHook != nil && conn.commitHook() != 0 {
			conn.active = false
			if conn.rollbackHook != nil {
				conn.rollbackHook()
			}
			return nil, errors.New("test commit denied")
		}
		if conn.commitErr != nil {
			return nil, conn.commitErr
		}
		conn.active = false
		return driver.RowsAffected(0), nil
	case "ROLLBACK":
		if !conn.active {
			return nil, errNoTestTransaction
		}
		conn.active = false
		if conn.rollbackHook != nil {
			conn.rollbackHook()
		}
		return driver.RowsAffected(0), nil
	default:
		return driver.RowsAffected(0), nil
	}
}

func (transactionBoundaryBeginTx) Commit() error   { return nil }
func (transactionBoundaryBeginTx) Rollback() error { return nil }

func TestImmediateDiscardsAmbiguousCanceledBegin(t *testing.T) {
	var attempts atomic.Uint32
	name := fmt.Sprintf("transaction-boundary-begin-%d", transactionBoundaryBeginDriverID.Add(1))
	sql.Register(name, transactionBoundaryBeginDriver{attempts: &attempts})
	database, err := sql.Open(name, "test")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer database.Close()
	callbackCalls := 0
	err = Immediate(t.Context(), database, func(*sql.Conn) error {
		callbackCalls++
		return nil
	})
	if !errors.Is(err, context.Canceled) || callbackCalls != 0 {
		t.Fatalf("ambiguous BEGIN = callbacks:%d error:%v", callbackCalls, err)
	}
	if _, err := database.ExecContext(t.Context(), "ROLLBACK"); !errors.Is(err, errNoTestTransaction) {
		t.Fatalf("ambiguous BEGIN leaked an active pooled transaction: %v", err)
	}
	if err := Immediate(t.Context(), database, func(*sql.Conn) error {
		callbackCalls++
		return nil
	}); err != nil || callbackCalls != 1 {
		t.Fatalf("Immediate after ambiguous BEGIN = callbacks:%d error:%v", callbackCalls, err)
	}
}

func TestImmediateRejectsDriverWithoutTransactionHooks(t *testing.T) {
	name := fmt.Sprintf("transaction-boundary-unsupported-%d", transactionBoundaryBeginDriverID.Add(1))
	sql.Register(name, transactionBoundaryUnsupportedDriver{})
	database, err := sql.Open(name, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	callbackCalls := 0
	err = Immediate(t.Context(), database, func(*sql.Conn) error {
		callbackCalls++
		return nil
	})
	if err == nil || callbackCalls != 0 {
		t.Fatalf("unsupported transaction boundary = callbacks:%d error:%v", callbackCalls, err)
	}
}

func TestOpenLegacySealCannotCommitOwnerTransaction(t *testing.T) {
	legacyRoot := t.TempDir()
	if err := os.Chmod(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	databaseHome := t.TempDir()
	databasePath := filepath.Join(databaseHome, "sealed.db")
	options := testOptions()
	options.Legacy = deferredLegacyOptions(
		legacyRoot,
		func() ([]LegacySource, error) { return nil, nil },
		func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
			t.Fatal("zero-source importer was invoked")
			return ImportResult{}, nil
		},
	)
	options.Legacy.Seal = func(ctx context.Context, conn *sql.Conn) error {
		// Reproduce the escape that exposed #435 and deliberately ignore it.
		_, _ = conn.ExecContext(ctx, "COMMIT")
		return nil
	}
	database, err := Open(
		deferredMigrationContext(t, databaseHome, databasePath),
		databasePath,
		options,
	)
	if database != nil {
		_ = database.Close()
	}
	if dblayer.CodeOf(err) != dblayer.CodeIntegrity {
		t.Fatalf("Open() transaction escape error = %v", err)
	}
	raw, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var committed int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
		WHERE name IN ('records', 'storage_imports', 'storage_import_issues',
			'storage_import_horizons', 'storage_imports_archive_status_idx')`).Scan(
		&committed,
	); err != nil || committed != 0 {
		t.Fatalf("callback COMMIT retained migration schema = %d, %v", committed, err)
	}
}

func TestImmediateDeniesCallbackTransactionTerminationWithoutCommit(t *testing.T) {
	for _, statement := range []string{"COMMIT", "END", "ROLLBACK"} {
		t.Run(statement, func(t *testing.T) {
			database, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			err = Immediate(t.Context(), database, func(conn *sql.Conn) error {
				if _, execErr := conn.ExecContext(t.Context(), `CREATE TABLE escaped (id INTEGER)`); execErr != nil {
					return execErr
				}
				// Deliberately swallow the transaction-control result.
				_, _ = conn.ExecContext(t.Context(), statement)
				return nil
			})
			if dblayer.CodeOf(err) != dblayer.CodeIntegrity {
				t.Fatalf("Immediate(%s) error = %v", statement, err)
			}
			var present int
			if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
				WHERE type = 'table' AND name = 'escaped'`).Scan(&present); err != nil || present != 0 {
				t.Fatalf("Immediate(%s) retained schema = %d, %v", statement, present, err)
			}
		})
	}
}

func TestImmediateNoOpTransactionUsesOwnerCommitBoundary(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := Immediate(t.Context(), database, func(*sql.Conn) error { return nil }); err != nil {
		t.Fatalf("no-op Immediate() error = %v", err)
	}
	if err := Immediate(t.Context(), database, func(*sql.Conn) error { return nil }); err != nil {
		t.Fatalf("reused no-op Immediate() error = %v", err)
	}
}

func TestImmediateReportsOwnerCommitFailure(t *testing.T) {
	var attempts atomic.Uint32
	attempts.Store(1)
	canary := errors.New("owner commit canary")
	name := fmt.Sprintf("transaction-boundary-commit-%d", transactionBoundaryBeginDriverID.Add(1))
	sql.Register(name, transactionBoundaryBeginDriver{
		attempts:  &attempts,
		commitErr: canary,
	})
	database, err := sql.Open(name, "test")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer database.Close()
	if err := Immediate(t.Context(), database, func(*sql.Conn) error { return nil }); !errors.Is(err, canary) {
		t.Fatalf("owner COMMIT error = %v", err)
	}
	if _, err := database.ExecContext(t.Context(), "ROLLBACK"); !errors.Is(err, errNoTestTransaction) {
		t.Fatalf("owner COMMIT failure leaked an active pooled transaction: %v", err)
	}
}

func TestImmediateFinalGuardIsInsideTransactionBoundary(t *testing.T) {
	for _, test := range []struct {
		name  string
		guard func(*sql.Conn, error) error
		code  dblayer.ErrorCode
	}{
		{
			name:  "guard error",
			guard: func(_ *sql.Conn, canary error) error { return canary },
		},
		{
			name: "guard transaction escape",
			guard: func(conn *sql.Conn, _ error) error {
				_, _ = conn.ExecContext(context.Background(), "COMMIT")
				return nil
			},
			code: dblayer.CodeIntegrity,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			canary := errors.New("final guard canary")
			var captured *sql.Conn
			err = immediateWithBeforeCommit(t.Context(), database, func(conn *sql.Conn) error {
				captured = conn
				_, execErr := conn.ExecContext(
					t.Context(),
					`CREATE TABLE guarded_partial (id INTEGER)`,
				)
				return execErr
			}, func() error {
				return test.guard(captured, canary)
			})
			if test.code != "" {
				if dblayer.CodeOf(err) != test.code {
					t.Fatalf("final guard boundary error = %v", err)
				}
			} else if !errors.Is(err, canary) {
				t.Fatalf("final guard error = %v", err)
			}
			var present int
			if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
				WHERE type = 'table' AND name = 'guarded_partial'`).Scan(
				&present,
			); err != nil || present != 0 {
				t.Fatalf("final guard retained partial schema = %d, %v", present, err)
			}
		})
	}
}

func TestImmediateReportsRollbackConflictAsBoundaryViolation(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	err = Immediate(t.Context(), database, func(conn *sql.Conn) error {
		if _, execErr := conn.ExecContext(t.Context(), `CREATE TABLE unique_values (
			value INTEGER UNIQUE
		)`); execErr != nil {
			return execErr
		}
		if _, execErr := conn.ExecContext(t.Context(), `INSERT INTO unique_values VALUES (1)`); execErr != nil {
			return execErr
		}
		_, conflictErr := conn.ExecContext(
			t.Context(),
			`INSERT OR ROLLBACK INTO unique_values VALUES (1)`,
		)
		if conflictErr == nil {
			return errors.New("duplicate insert unexpectedly succeeded")
		}
		return conflictErr
	})
	if dblayer.CodeOf(err) != dblayer.CodeIntegrity {
		t.Fatalf("Immediate(OR ROLLBACK) error = %v", err)
	}
	var present int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
		WHERE type = 'table' AND name = 'unique_values'`).Scan(&present); err != nil || present != 0 {
		t.Fatalf("rollback conflict retained schema = %d, %v", present, err)
	}
}

func TestImmediateContextCancellationRollsBackAndRemovesHooks(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer database.Close()
	canceled, cancel := context.WithCancel(t.Context())
	err = Immediate(canceled, database, func(conn *sql.Conn) error {
		if _, execErr := conn.ExecContext(t.Context(), `CREATE TABLE canceled (id INTEGER)`); execErr != nil {
			return execErr
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Immediate() error = %v", err)
	}
	// Reusing the single pooled connection proves the hooks were removed.
	if _, err := database.ExecContext(t.Context(), `CREATE TABLE reused (id INTEGER)`); err != nil {
		t.Fatalf("reused connection retained boundary hooks: %v", err)
	}
}

func TestImmediateCallbackErrorAndPanicRollBackAndRemoveHooks(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(error) error
	}{
		{name: "error", run: func(canary error) error { return canary }},
		{name: "panic", run: func(canary error) error { panic(canary) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			database.SetMaxOpenConns(1)
			database.SetMaxIdleConns(1)
			defer database.Close()
			canary := errors.New("callback canary")
			var immediateErr error
			panicked := false
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						panicked = true
						if recovered != canary {
							t.Fatalf("unexpected panic = %#v", recovered)
						}
					}
				}()
				immediateErr = Immediate(t.Context(), database, func(conn *sql.Conn) error {
					if _, execErr := conn.ExecContext(
						t.Context(),
						`CREATE TABLE callback_partial (id INTEGER)`,
					); execErr != nil {
						return execErr
					}
					return test.run(canary)
				})
			}()
			if test.name == "error" && !errors.Is(immediateErr, canary) {
				t.Fatalf("callback error = %v", immediateErr)
			}
			if (test.name == "panic") != panicked {
				t.Fatalf("callback panic state = %t", panicked)
			}
			if _, err := database.ExecContext(
				t.Context(),
				`CREATE TABLE callback_reused (id INTEGER)`,
			); err != nil {
				t.Fatalf("callback cleanup retained hooks: %v", err)
			}
			var partial int
			if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
				WHERE type = 'table' AND name = 'callback_partial'`).Scan(
				&partial,
			); err != nil || partial != 0 {
				t.Fatalf("callback failure retained partial schema = %d, %v", partial, err)
			}
		})
	}
}
