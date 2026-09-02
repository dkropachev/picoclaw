//go:build whatsapp_native

package migration

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestMigrationInitializesMissingWhatsAppStoreOfflineWithBackup(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	storeRoot := filepath.Join(home, "whatsapp-data")
	channel := &config.Channel{Enabled: true, Type: config.ChannelWhatsAppNative}
	if err := channel.Decode(&config.WhatsAppSettings{
		UseNative:        true,
		SessionStorePath: storeRoot,
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
	id, err := database.ParseStoreID("channel.whatsapp.primary-986a1b71")
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), Options{Stores: []database.StoreID{id}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stores) != 1 || !result.Stores[0].Migrated || result.Stores[0].Exists {
		t.Fatalf("WhatsApp migration result = %#v", result)
	}
	if result.BackupDir == "" {
		t.Fatal("WhatsApp migration did not retain its mandatory outer backup")
	}
	ready, err := sqliteprovider.HasSchemaObjects(
		t.Context(),
		filepath.Join(storeRoot, "store.db"),
		5*time.Second,
		"whatsmeow_version",
		"whatsmeow_device",
	)
	if err != nil || !ready {
		t.Fatalf("WhatsApp schema ready=%t error=%v", ready, err)
	}
}
