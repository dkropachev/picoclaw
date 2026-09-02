package dashboardauth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/web/backend/launcherconfig"
)

func init() {
	allowUnfencedLauncherAuthProviderForTests.Store(true)
}

func TestLauncherAuthProviderRejectsMissingBrokerAuthority(t *testing.T) {
	restoreAuthority := database.SuspendProviderTestAuthority()
	t.Cleanup(restoreAuthority)
	allowUnfencedLauncherAuthProviderForTests.Store(false)
	t.Cleanup(func() { allowUnfencedLauncherAuthProviderForTests.Store(true) })
	home := t.TempDir()
	path := filepath.Join(home, databaseFilename)
	handler := NewBrokerHandler(home, filepath.Join(home, launcherconfig.FileName))
	if handler.home != "" || database.CodeOf(handler.err) != database.CodeUnauthorized {
		t.Fatalf("unfenced launcher-auth handler = %#v", handler)
	}
	if _, err := openLocalWithLauncherConfig(
		path,
		filepath.Join(home, launcherconfig.FileName),
	); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced launcher-auth open error = %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("unfenced launcher-auth open created database: %v", err)
	}
}
