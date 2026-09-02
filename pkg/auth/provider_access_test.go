package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func init() {
	allowUnfencedAuthProviderForTests.Store(true)
}

func TestAuthProviderRejectsMissingBrokerAuthority(t *testing.T) {
	restoreAuthority := database.SuspendProviderTestAuthority()
	t.Cleanup(restoreAuthority)
	allowUnfencedAuthProviderForTests.Store(false)
	t.Cleanup(func() { allowUnfencedAuthProviderForTests.Store(true) })
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	previousClient := database.RuntimeClient()
	database.InstallProcessClient(nil)
	t.Cleanup(func() { database.InstallProcessClient(previousClient) })
	if _, err := openAuthDatabase(t.Context()); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced auth open error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, authDatabaseFilename)); !os.IsNotExist(err) {
		t.Fatalf("unfenced auth open created database: %v", err)
	}
}
