package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func init() {
	allowUnfencedModelCatalogProviderForTests.Store(true)
}

func TestModelCatalogProviderRejectsMissingBrokerAuthority(t *testing.T) {
	restoreAuthority := database.SuspendProviderTestAuthority()
	t.Cleanup(restoreAuthority)
	allowUnfencedModelCatalogProviderForTests.Store(false)
	t.Cleanup(func() { allowUnfencedModelCatalogProviderForTests.Store(true) })
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	previousClient := database.RuntimeClient()
	database.InstallProcessClient(nil)
	t.Cleanup(func() { database.InstallProcessClient(previousClient) })
	handler := NewModelCatalogBrokerHandler(home)
	if handler.path != "" || database.CodeOf(handler.err) != database.CodeUnauthorized {
		t.Fatalf("unfenced model-catalog handler = %#v", handler)
	}
	if _, err := loadCatalogs(); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced model-catalog load error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, catalogDatabaseFilename)); !os.IsNotExist(err) {
		t.Fatalf("unfenced model-catalog load created database: %v", err)
	}
}
