package sqliteprovider

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func transactionBoundaryTestConnection(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
	database, err := OpenStore(":memory:", time.Second)
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

func beginBoundaryTest(t *testing.T, conn *sql.Conn, boundary *TransactionBoundary, begin string) {
	t.Helper()
	if err := boundary.BeginAttempted(); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), begin); err != nil {
		t.Fatal(err)
	}
	if err := boundary.Started(); err != nil {
		t.Fatal(err)
	}
	if err := boundary.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionBoundaryAllowsOneOwnerCommit(t *testing.T) {
	_, conn := transactionBoundaryTestConnection(t)
	boundary, err := NewTransactionBoundary(conn)
	if err != nil {
		t.Fatal(err)
	}
	beginBoundaryTest(t, conn, boundary, "BEGIN IMMEDIATE")
	if _, err := conn.ExecContext(t.Context(), `CREATE TABLE committed (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := boundary.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := boundary.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := boundary.Close(); err != nil {
		t.Fatal(err)
	}
	if err := boundary.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if err := boundary.Check(t.Context()); err == nil {
		t.Fatal("closed boundary passed Check")
	}
	if err := boundary.Commit(t.Context()); err == nil {
		t.Fatal("closed boundary committed again")
	}
	var present int
	if err := conn.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_schema
		WHERE type = 'table' AND name = 'committed'`).Scan(&present); err != nil || present != 1 {
		t.Fatalf("owner commit table = %d, %v", present, err)
	}
}

func TestTransactionBoundaryDeniesCallbackCommitEvenWhenIgnored(t *testing.T) {
	database, conn := transactionBoundaryTestConnection(t)
	boundary, err := NewTransactionBoundary(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer boundary.Close()
	beginBoundaryTest(t, conn, boundary, "BEGIN IMMEDIATE")
	if _, err := conn.ExecContext(t.Context(), `CREATE TABLE escaped (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), "COMMIT"); err == nil {
		t.Fatal("callback COMMIT was accepted")
	}
	// Simulate a callback swallowing the denied COMMIT and starting more work.
	if _, err := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `CREATE TABLE after_escape (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := boundary.Check(t.Context()); !errors.Is(err, errTransactionBoundary) {
		t.Fatalf("boundary Check() = %v", err)
	}
	if err := boundary.Close(); !errors.Is(err, errTransactionBoundary) {
		t.Fatalf("violated boundary Close() = %v", err)
	}
	for _, table := range []string{"escaped", "after_escape"} {
		var present int
		if err := database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_schema
			WHERE type = 'table' AND name = ?`, table).Scan(&present); err != nil || present != 0 {
			t.Fatalf("callback commit retained %s = %d, %v", table, present, err)
		}
	}
}

func TestTransactionBoundaryRecordsCallbackRollback(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(context.Context, *sql.Conn) error
	}{
		{
			name: "explicit",
			run: func(ctx context.Context, conn *sql.Conn) error {
				_, err := conn.ExecContext(ctx, "ROLLBACK")
				return err
			},
		},
		{
			name: "constraint",
			run: func(ctx context.Context, conn *sql.Conn) error {
				if _, err := conn.ExecContext(ctx, `CREATE TABLE unique_values (
					value INTEGER UNIQUE
				)`); err != nil {
					return err
				}
				if _, err := conn.ExecContext(ctx, `INSERT INTO unique_values VALUES (1)`); err != nil {
					return err
				}
				_, err := conn.ExecContext(ctx, `INSERT OR ROLLBACK INTO unique_values VALUES (1)`)
				if err == nil {
					return errors.New("duplicate insert unexpectedly succeeded")
				}
				return nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, conn := transactionBoundaryTestConnection(t)
			boundary, err := NewTransactionBoundary(conn)
			if err != nil {
				t.Fatal(err)
			}
			defer boundary.Close()
			beginBoundaryTest(t, conn, boundary, "BEGIN EXCLUSIVE")
			if err := test.run(t.Context(), conn); err != nil {
				t.Fatal(err)
			}
			if err := boundary.Check(t.Context()); !errors.Is(err, errTransactionBoundary) {
				t.Fatalf("rollback boundary Check() = %v", err)
			}
		})
	}
}

func TestTransactionBoundaryRejectsInvalidAndReusedTransitions(t *testing.T) {
	if boundary, err := NewTransactionBoundary(nil); boundary != nil || err == nil {
		t.Fatalf("nil connection boundary = %#v, %v", boundary, err)
	}
	if err := (*TransactionBoundary)(nil).Started(); err == nil {
		t.Fatal("nil boundary started")
	}
	if err := (*TransactionBoundary)(nil).BeginAttempted(); err == nil {
		t.Fatal("nil boundary began an attempt")
	}
	if err := (*TransactionBoundary)(nil).Check(t.Context()); err == nil {
		t.Fatal("nil boundary passed Check")
	}
	if err := (*TransactionBoundary)(nil).Commit(t.Context()); err == nil {
		t.Fatal("nil boundary committed")
	}
	if err := (*TransactionBoundary)(nil).Close(); err != nil {
		t.Fatalf("nil boundary Close() = %v", err)
	}

	for _, test := range []struct {
		name string
		run  func(*TransactionBoundary) error
	}{
		{
			name: "started before attempt",
			run:  func(boundary *TransactionBoundary) error { return boundary.Started() },
		},
		{
			name: "duplicate attempt",
			run: func(boundary *TransactionBoundary) error {
				if err := boundary.BeginAttempted(); err != nil {
					return err
				}
				return boundary.BeginAttempted()
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, conn := transactionBoundaryTestConnection(t)
			boundary, err := NewTransactionBoundary(conn)
			if err != nil {
				t.Fatal(err)
			}
			if err := test.run(boundary); !errors.Is(err, errTransactionBoundary) {
				t.Fatalf("invalid transition error = %v", err)
			}
			if err := boundary.Close(); !errors.Is(err, errTransactionBoundary) {
				t.Fatalf("invalid transition close error = %v", err)
			}
		})
	}

	_, conn := transactionBoundaryTestConnection(t)
	boundary, err := NewTransactionBoundary(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer boundary.Close()
	if err := boundary.BeginAttempted(); err != nil {
		t.Fatal(err)
	}
	if err := boundary.Started(); err != nil {
		t.Fatal(err)
	}
	if err := boundary.Started(); !errors.Is(err, errTransactionBoundary) {
		t.Fatalf("second Started() = %v", err)
	}
	if err := boundary.Check(nil); err == nil {
		t.Fatal("nil-context Check succeeded")
	}
	if err := boundary.Commit(nil); err == nil {
		t.Fatal("nil-context Commit succeeded")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := boundary.Commit(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Commit() = %v", err)
	}
}

func TestTransactionBoundaryCloseUnregistersHooks(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(strings.ToLower(map[bool]string{false: "installed", true: "started"}[started]), func(t *testing.T) {
			_, conn := transactionBoundaryTestConnection(t)
			boundary, err := NewTransactionBoundary(conn)
			if err != nil {
				t.Fatal(err)
			}
			if started {
				beginBoundaryTest(t, conn, boundary, "BEGIN IMMEDIATE")
			}
			if err := boundary.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ExecContext(t.Context(), "COMMIT"); err != nil {
				t.Fatalf("unregistered commit hook still denied COMMIT: %v", err)
			}
		})
	}
}

func TestTransactionBoundaryConcurrentCommitHasOneWinner(t *testing.T) {
	_, conn := transactionBoundaryTestConnection(t)
	boundary, err := NewTransactionBoundary(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer boundary.Close()
	beginBoundaryTest(t, conn, boundary, "BEGIN IMMEDIATE")
	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			results <- boundary.Commit(t.Context())
		}()
	}
	ready.Wait()
	close(start)
	first, second := <-results, <-results
	if (first == nil) == (second == nil) {
		t.Fatalf("concurrent commits = %v / %v", first, second)
	}
}

func TestTransactionBoundaryCommitFailureRollsBackAndDiscards(t *testing.T) {
	database, conn := transactionBoundaryTestConnection(t)
	if _, err := conn.ExecContext(t.Context(), "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	boundary, err := NewTransactionBoundary(conn)
	if err != nil {
		t.Fatal(err)
	}
	beginBoundaryTest(t, conn, boundary, "BEGIN IMMEDIATE")
	for _, statement := range []string{
		`CREATE TABLE parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE child (
			parent_id INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED
		)`,
		`INSERT INTO child(parent_id) VALUES (7)`,
	} {
		if _, err := conn.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := boundary.Commit(t.Context()); err == nil {
		t.Fatal("deferred foreign-key violation committed")
	}
	if err := boundary.Close(); err != nil {
		t.Fatalf("second close after failed commit = %v", err)
	}
	var present int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
		WHERE name IN ('parent', 'child')`).Scan(&present); err != nil || present != 0 {
		t.Fatalf("failed owner commit retained schema = %d, %v", present, err)
	}
}

func TestTransactionBoundaryRejectsUnsupportedAndClosedConnections(t *testing.T) {
	unsupported := openProviderScript(t)
	conn, err := unsupported.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if boundary, err := NewTransactionBoundary(conn); boundary != nil || err == nil {
		t.Fatalf("unsupported driver boundary = %#v, %v", boundary, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	_, closed := transactionBoundaryTestConnection(t)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if boundary, err := NewTransactionBoundary(closed); boundary != nil || err == nil {
		t.Fatalf("closed connection boundary = %#v, %v", boundary, err)
	}
}
