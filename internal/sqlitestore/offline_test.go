package sqlitestore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestMigrationFenceKeepsDomainSchemaWorkInExclusiveRollbackJournal(t *testing.T) {
	home := t.TempDir()
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fence.Close() })
	db, err := Open(t.Context(), filepath.Join(home, "offline.db"), Options{
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
