package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func init() {
	allowUnfencedAdaptationProviderForTests.Store(true)
}

func TestToolAdaptationRejectsMissingBrokerAuthority(t *testing.T) {
	restoreAuthority := database.SuspendProviderTestAuthority()
	t.Cleanup(restoreAuthority)
	allowUnfencedAdaptationProviderForTests.Store(false)
	t.Cleanup(func() { allowUnfencedAdaptationProviderForTests.Store(true) })
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	previousClient := database.RuntimeClient()
	database.InstallProcessClient(nil)
	t.Cleanup(func() { database.InstallProcessClient(previousClient) })
	handler := NewAdaptationBrokerHandler(home)
	if handler.store != nil || database.CodeOf(handler.err) != database.CodeUnauthorized {
		t.Fatalf("unfenced tool-adaptation handler = %#v", handler)
	}
	if _, found := LatestToolAdaptationObservation(ToolAdaptationProfile{Provider: "openai"}); found {
		t.Fatal("unfenced tool-adaptation read succeeded")
	}
	if _, err := os.Lstat(filepath.Join(home, toolAdaptationDatabaseFilename)); !os.IsNotExist(err) {
		t.Fatalf("unfenced tool-adaptation read created database: %v", err)
	}
}
