//go:build (unix && !aix) || windows

package databaseclaims_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/internal/databaseclaims"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestAcquireForTestingSupportsCompleteLifecycleBelowTempDir(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	root, err := databaseclaims.PrepareRootForTesting(root)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := databaseclaims.AcquireForTesting(storecatalog.Options{
		Home: home, Config: &config.Config{}, UserHome: t.TempDir(),
	}, nil, fence, root)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	live := filepath.Join(home, "auth.db")
	if err := os.WriteFile(live, []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lease.Refresh(); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(home, "stage.db")
	if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lease.PinReplacement("global/auth", stage); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(live, live+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stage, live); err != nil {
		t.Fatal(err)
	}
	if err := lease.RefreshReplacement("global/auth"); err != nil {
		t.Fatal(err)
	}
	if err := lease.Check(); err != nil {
		t.Fatal(err)
	}
}
