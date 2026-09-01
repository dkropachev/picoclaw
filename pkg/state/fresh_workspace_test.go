package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteManagerCreatesFreshWorkspaceWithoutLegacyContract(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "not-created-yet")
	options, err := runtimeStoreOptions(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if options.Legacy != nil {
		t.Fatal("fresh workspace unexpectedly enabled legacy migration")
	}
	manager, err := NewSQLiteManager(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if manager.databasePath != filepath.Join(workspace, "state", runtimeDatabaseFilename) {
		t.Fatalf("database path = %q", manager.databasePath)
	}
	if info, err := os.Stat(manager.databasePath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("fresh database = %#v, %v", info, err)
	}
}
