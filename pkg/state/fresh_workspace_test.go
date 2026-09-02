package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteManagerCreatesFreshWorkspaceAndClosesEmptyLegacyHorizon(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "not-created-yet")
	options, err := runtimeStoreOptions(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if options.Legacy == nil {
		t.Fatal("fresh workspace has no legacy horizon contract")
	}
	sources, err := options.Legacy.Sources()
	if err != nil || len(sources) != 0 {
		t.Fatalf("fresh legacy enumeration = %#v, %v", sources, err)
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
	database := openRawRuntimeDatabase(t, manager.databasePath)
	defer database.Close()
	var horizons, imports int
	if err := database.QueryRow(`SELECT
	    (SELECT COUNT(*) FROM storage_import_horizons WHERE component = ?),
	    (SELECT COUNT(*) FROM storage_imports WHERE component = ?)`,
		runtimeDatabaseComponent, runtimeDatabaseComponent,
	).Scan(&horizons, &imports); err != nil || horizons != 1 || imports != 0 {
		t.Fatalf("fresh legacy closeout = horizons:%d imports:%d error:%v", horizons, imports, err)
	}
}
