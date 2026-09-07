//go:build (goolm || cgo) && !mipsle && !netbsd && !(freebsd && arm) && !android

package migration

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestMigrationInitializesMissingMatrixStoreOffline(t *testing.T) {
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
		Channels: config.ChannelsConfig{"primary": matrix},
	}
	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rawID, ok := storecatalog.ChannelStoreID(config.ChannelMatrix, "primary")
	if !ok {
		t.Fatal("matrix channel store ID is invalid")
	}
	id, err := database.ParseStoreID(rawID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), Options{Stores: []database.StoreID{id}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stores) != 1 || !result.Stores[0].Migrated || result.Stores[0].Exists {
		t.Fatalf("Matrix migration result = %#v", result)
	}
	if result.BackupDir == "" {
		t.Fatal("Matrix migration did not retain its mandatory outer backup")
	}
	ready, err := sqliteprovider.HasSchemaObjects(
		t.Context(),
		filepath.Join(storeRoot, "store.db"),
		5*time.Second,
		"crypto_version",
		"mx_version",
	)
	if err != nil || !ready {
		t.Fatalf("Matrix schema ready=%t error=%v", ready, err)
	}
}
