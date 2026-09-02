//go:build (goolm || cgo) && !mipsle && !netbsd && !(freebsd && arm) && !android

//nolint:govet // Independent migration boundary assertions intentionally use narrow errors.
package migration

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestMigrationReportsMalformedExistingUnversionedSchema(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	storeRoot := filepath.Join(home, "matrix-data")
	channel := &config.Channel{Enabled: true, Type: config.ChannelMatrix}
	if err := channel.Decode(&config.MatrixSettings{
		CryptoDatabasePath: storeRoot,
		CryptoPassphrase:   "configured",
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Agents:   config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
		Channels: config.ChannelsConfig{"primary": channel},
	}
	path := filepath.Join(storeRoot, "store.db")
	db, err := sqliteprovider.OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteprovider.ConfigureOffline(t.Context(), db, 5*time.Second); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE mx_version (unexpected TEXT)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := sqliteprovider.SetSchemaVersion(t.Context(), db, 0); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id, err := database.ParseStoreID("channel.matrix.primary-986a1b71")
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), Options{
		Stores: []database.StoreID{id}, BackupDir: filepath.Join(home, "malformed-backup"),
	})
	if !errors.Is(err, ErrMigrationRequired) || len(result.Stores) != 1 ||
		!result.Stores[0].AdapterRequired || result.BackupDir == "" {
		t.Fatalf("malformed unversioned migration = %#v, %v", result, err)
	}
	if manifest := readManifest(t, result.BackupDir); manifest.Outcome != "failed" {
		t.Fatalf("malformed unversioned backup = %#v", manifest)
	}
}
