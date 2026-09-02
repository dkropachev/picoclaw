//go:build unix

package catalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestLegacyInputInventoryUsesRealFilesystemBoundaries(t *testing.T) {
	root := t.TempDir()
	if _, err := legacyInputExists([]string{filepath.Join(root, strings.Repeat("x", 5000))}); err == nil {
		t.Fatal("overlong legacy input was accepted")
	}

	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias.json")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyInputExists([]string{alias}); err == nil {
		t.Fatal("symlinked legacy input was accepted")
	}

	databaseOnly := filepath.Join(root, "database-only")
	if err := os.Mkdir(databaseOnly, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.db", "state.db-wal", "state.db-shm", "state.lock", "store"} {
		if err := os.WriteFile(filepath.Join(databaseOnly, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, skipped := range []string{"legacy-json", "backups", ".database"} {
		directory := filepath.Join(databaseOnly, skipped)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "ignored.json"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if found, err := legacyInputExists([]string{databaseOnly}); err != nil || found {
		t.Fatalf("database-only legacy inventory = %t, %v", found, err)
	}

	nestedAliasRoot := filepath.Join(root, "nested-alias")
	if err := os.Mkdir(nestedAliasRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(nestedAliasRoot, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyInputExists([]string{nestedAliasRoot}); err == nil {
		t.Fatal("legacy tree containing a symlink was accepted")
	}

	fifo := filepath.Join(root, "legacy.fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyInputExists([]string{fifo}); err == nil {
		t.Fatal("non-regular legacy input was accepted")
	}

	ordinaryRoot := filepath.Join(root, "ordinary")
	if err := os.Mkdir(ordinaryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ordinaryRoot, "legacy.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := legacyInputExists([]string{ordinaryRoot}); err != nil || !found {
		t.Fatalf("ordinary legacy inventory = %t, %v", found, err)
	}
}

func TestUnversionedReadinessUsesRetainedInspectionResults(t *testing.T) {
	if err := RequireReady(&Catalog{}, nil); err != nil {
		t.Fatal(err)
	}
	status := database.StoreStatus{ID: "workspace.matrix", Readiness: database.StoreReady}
	if err := validateUnversionedReadiness(
		t.Context(), sqliteprovider.Inspection{},
		storecatalog.Spec{Domain: "channel-matrix"}, &status,
	); err == nil {
		t.Fatal("missing inspection pool was accepted")
	}

	path := filepath.Join(t.TempDir(), "matrix.db")
	createCatalogStore(t, path, `PRAGMA user_version = 1;`)
	inspection, err := sqliteprovider.Inspect(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inspection.Release() })
	status = database.StoreStatus{ID: "workspace.matrix", Readiness: database.StoreReady}
	if err := validateUnversionedReadiness(
		t.Context(), inspection, storecatalog.Spec{Domain: "channel-matrix"}, &status,
	); err != nil || status.Readiness != database.StoreMigrationRequired {
		t.Fatalf("missing Matrix schema readiness = %#v, %v", status, err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	status = database.StoreStatus{ID: "workspace.matrix", Readiness: database.StoreReady}
	if err := validateUnversionedReadiness(
		canceled, inspection, storecatalog.Spec{Domain: "channel-matrix"}, &status,
	); err == nil {
		t.Fatal("canceled schema inspection succeeded")
	}
}
