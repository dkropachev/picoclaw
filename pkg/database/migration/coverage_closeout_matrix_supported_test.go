//go:build (goolm || cgo) && !mipsle && !netbsd && !(freebsd && arm) && !android

package migration

import (
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestCoverageMigrationRevalidatesExistingUnversionedStore(t *testing.T) {
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
	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id, err := database.ParseStoreID("channel.matrix.primary-986a1b71")
	if err != nil {
		t.Fatal(err)
	}
	first, err := engine.Run(t.Context(), Options{
		Stores: []database.StoreID{id}, BackupDir: filepath.Join(home, "first-backup"),
	})
	if err != nil || len(first.Stores) != 1 || first.Stores[0].Exists {
		t.Fatalf("initial unversioned migration = %#v, %v", first, err)
	}
	second, err := engine.Run(t.Context(), Options{
		Stores: []database.StoreID{id}, BackupDir: filepath.Join(home, "second-backup"),
	})
	if err != nil || len(second.Stores) != 1 || !second.Stores[0].Exists ||
		!second.Stores[0].Migrated {
		t.Fatalf("existing unversioned migration = %#v, %v", second, err)
	}
}
