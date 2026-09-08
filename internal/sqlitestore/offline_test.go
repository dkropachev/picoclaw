package sqlitestore

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestMigrationFenceKeepsDomainSchemaWorkInExclusiveRollbackJournal(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fence.Close() })
	path := filepath.Join(home, "offline.db")
	migrationCtx, err := fence.MigrationContext(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := Open(migrationCtx, path, Options{
		Component: "offline-test",
		Migrations: []Migration{{
			Version:    1,
			Statements: []string{`CREATE TABLE item(id INTEGER PRIMARY KEY) STRICT`},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if stats := db.Stats(); stats.MaxOpenConnections != 1 {
		t.Fatalf("offline max open connections = %d, want 1", stats.MaxOpenConnections)
	}
	var journal, locking string
	if err := db.QueryRowContext(
		context.Background(),
		`SELECT jm.journal_mode, lm.locking_mode
		   FROM pragma_journal_mode AS jm
		  CROSS JOIN pragma_locking_mode AS lm`,
	).Scan(&journal, &locking); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(journal, "delete") || !strings.EqualFold(locking, "exclusive") {
		t.Fatalf("offline provider mode = journal:%q locking:%q", journal, locking)
	}
}

func TestMigrationFenceDoesNotChangeUnrelatedStoreMode(t *testing.T) {
	fencedHome := t.TempDir()
	if err := os.Chmod(fencedHome, 0o700); err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireMigrationFence(fencedHome)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fence.Close() })
	authorizedPath := filepath.Join(fencedHome, "authorized.db")
	migrationCtx, err := fence.MigrationContext(t.Context(), authorizedPath)
	if err != nil {
		t.Fatal(err)
	}
	unrelatedPath := filepath.Join(t.TempDir(), "unrelated.db")
	if db, err := Open(migrationCtx, unrelatedPath, Options{
		Component: "unrelated-test",
		Migrations: []Migration{{
			Version:    1,
			Statements: []string{`CREATE TABLE item(id INTEGER PRIMARY KEY) STRICT`},
		}},
	}); err == nil || db != nil {
		if db != nil {
			_ = db.Close()
		}
		t.Fatalf("wrong-target migration context = %#v, %v", db, err)
	}
	db, err := Open(t.Context(), unrelatedPath, Options{
		Component: "unrelated-test",
		Migrations: []Migration{{
			Version:    1,
			Statements: []string{`CREATE TABLE item(id INTEGER PRIMARY KEY) STRICT`},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Immediate(migrationCtx, db, func(*sql.Conn) error { return nil }); err == nil {
		t.Fatal("wrong-target migration context authorized unrelated transaction")
	}
	if stats := db.Stats(); stats.MaxOpenConnections != defaultOpenConns {
		t.Fatalf("unrelated max open connections = %d", stats.MaxOpenConnections)
	}
	var journal string
	if err := db.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(journal, "wal") {
		t.Fatalf("unrelated journal mode = %q", journal)
	}
}
