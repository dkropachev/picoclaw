package migration

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database/catalog"
)

func TestMigrationBackupRejectsUnencodableTimestamp(t *testing.T) {
	home, _, cfg := migrationFixture(t)
	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	engine.now = func() time.Time {
		return time.Date(10_000, time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	_, err = engine.Run(t.Context(), Options{
		Stores:    []catalog.StoreID{mustStoreID(t, "workspace/workflows")},
		BackupDir: filepath.Join(home, "invalid-time-backup"),
		DryRun:    true,
	})
	if err == nil || !strings.Contains(err.Error(), "write database backup manifest") {
		t.Fatalf("unencodable backup timestamp error = %v", err)
	}
}
