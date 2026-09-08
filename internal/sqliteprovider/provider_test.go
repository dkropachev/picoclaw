package sqliteprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

func maintainOfflineFixture(
	t *testing.T,
	ctx context.Context,
	path string,
	timeout time.Duration,
) (MaintenanceResult, error) {
	t.Helper()
	home := filepath.Dir(path)
	if err := os.Chmod(home, 0o700); err != nil {
		return MaintenanceResult{}, err
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		return MaintenanceResult{}, err
	}
	defer fence.Close()
	migrationCtx, err := fence.MigrationContext(ctx, path)
	if err != nil {
		return MaintenanceResult{}, err
	}
	return MaintainOffline(migrationCtx, path, timeout)
}

func TestDSNOwnsDurabilityConfiguration(t *testing.T) {
	t.Parallel()

	dsn, err := DSN(filepath.Join(t.TempDir(), "store with spaces.db"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"file:", "foreign_keys", "busy_timeout", "5000", "synchronous", "FULL",
	} {
		if !strings.Contains(dsn, required) {
			t.Fatalf("DSN() = %q, missing %q", dsn, required)
		}
	}
}

func TestOpenAndBusyClassification(t *testing.T) {
	t.Parallel()

	dsn, err := DSN(":memory:", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = dsn
	database, err := OpenStore(":memory:", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := Configure(t.Context(), database, 5*time.Second, true); err != nil {
		t.Fatal(err)
	}

	var foreignKeys, busyTimeout, synchronous int
	if err := database.QueryRow(`SELECT fk.foreign_keys, bt.timeout, sm.synchronous
		FROM pragma_foreign_keys AS fk
		CROSS JOIN pragma_busy_timeout AS bt
		CROSS JOIN pragma_synchronous AS sm`).Scan(
		&foreignKeys,
		&busyTimeout,
		&synchronous,
	); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 {
		t.Fatalf("provider settings = %d, %d, %d", foreignKeys, busyTimeout, synchronous)
	}
	if IsBusyOrLocked(errors.New("ordinary")) {
		t.Fatal("ordinary error classified as retryable")
	}
}

func TestInspectionPoolIsAdoptedByStoreOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retained.db")
	database, openErr := OpenStore(path, 5*time.Second)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if _, err := database.Exec(`CREATE TABLE retained (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(t.Context(), path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Exists || inspection.database == nil {
		t.Fatalf("inspection = %#v", inspection)
	}
	repeated, err := Inspect(t.Context(), path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.database != inspection.database {
		t.Fatal("repeat inspection opened a duplicate pool")
	}
	if err := repeated.Release(); err != nil {
		t.Fatal(err)
	}
	adopted, err := inspection.Adopt(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if adopted != inspection.database {
		t.Fatal("store owner opened a second pool after readiness inspection")
	}
	if err := adopted.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInspectionPoolAdoptionRejectsGenerationReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "retained.db")
	database, openErr := OpenStore(path, 5*time.Second)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if _, err := database.Exec(`CREATE TABLE retained (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(t.Context(), path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	old := path + ".old"
	if err := os.Rename(path, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if adopted, err := inspection.Adopt(5 * time.Second); err == nil || adopted != nil {
		if adopted != nil {
			_ = adopted.Close()
		}
		t.Fatalf("replacement generation adoption = %#v, %v", adopted, err)
	}
	if err := inspection.database.Ping(); err != nil {
		t.Fatalf("failed adoption invalidated readiness pool: %v", err)
	}
	if err := inspection.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestInspectionPoolAdoptionRequiresMatchingBusyTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retained.db")
	database, err := OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE retained (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(t.Context(), path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := inspection.Adopt(2 * time.Second)
	if err == nil || opened != nil {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatalf("mismatched pool adoption = %#v, %v", opened, err)
	}
	if err := inspection.database.Ping(); err != nil {
		t.Fatalf("mismatched adoption closed readiness pool: %v", err)
	}
	if err := inspection.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestRepeatedInspectionsRetainIndependentPoolReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retained.db")
	database, err := OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE retained (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	first, err := Inspect(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Inspect(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if first.database != second.database {
		t.Fatal("repeat inspection did not retain the exact pool")
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := second.database.Ping(); err != nil {
		t.Fatalf("first release closed second inspection: %v", err)
	}
	adopted, err := second.Adopt(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if adopted != second.database {
		t.Fatal("sole readiness owner was not adopted")
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
	if err := adopted.Ping(); err != nil {
		t.Fatalf("snapshot release closed adopted pool: %v", err)
	}
	if err := adopted.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInspectionPoolCannotBeAdoptedWhileMultipleSnapshotsOwnIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retained.db")
	database, err := OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE retained (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	first, err := Inspect(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Inspect(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if adopted, err := first.Adopt(time.Second); err == nil || adopted != nil {
		if adopted != nil {
			_ = adopted.Close()
		}
		t.Fatalf("multiply-owned pool adoption = %#v, %v", adopted, err)
	}
	if err := first.database.Ping(); err != nil {
		t.Fatalf("failed adoption invalidated live snapshots: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestInspectionRejectsConflictingPoolConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retained.db")
	database, err := OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE retained (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Release()
	if ready, err := inspection.HasSchemaObjects(nil, "table", "records"); err == nil || ready {
		t.Fatalf("nil-context schema query = %t, %v", ready, err)
	}
	if ready, err := inspection.HasTableColumns(nil, "records", "id"); err == nil || ready {
		t.Fatalf("nil-context column query = %t, %v", ready, err)
	}
	if ready, err := inspection.HasImportHorizon(nil, "records"); err == nil || ready {
		t.Fatalf("nil-context horizon query = %t, %v", ready, err)
	}
	if conflicting, err := Inspect(t.Context(), path, 2*time.Second); err == nil || conflicting.Exists {
		t.Fatalf("conflicting inspection = %#v, %v", conflicting, err)
	}
	if err := inspection.database.Ping(); err != nil {
		t.Fatalf("configuration conflict closed retained pool: %v", err)
	}
}

func TestProviderRejectsInvalidPathsAndBusyTimeouts(t *testing.T) {
	for _, timeout := range []time.Duration{0, time.Millisecond - 1, time.Minute + 1} {
		if dsn, err := DSN(":memory:", timeout); err == nil || dsn != "" {
			t.Errorf("DSN timeout %s = %q, %v", timeout, dsn, err)
		}
		if database, err := OpenStore(":memory:", timeout); err == nil || database != nil {
			if database != nil {
				_ = database.Close()
			}
			t.Errorf("OpenStore timeout %s = %#v, %v", timeout, database, err)
		}
	}
	for _, path := range []string{"", " ", "file:store.db", "bad\x00path"} {
		if dsn, err := DSN(path, time.Second); err == nil || dsn != "" {
			t.Errorf("DSN path %q = %q, %v", path, dsn, err)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing.db")
	if inspection, err := Inspect(t.Context(), missing, 0); err == nil || inspection.Exists {
		t.Fatalf("Inspect invalid timeout = %#v, %v", inspection, err)
	}
	if _, err := maintainOfflineFixture(t, t.Context(), missing, 0); err == nil {
		t.Fatal("MaintainOffline accepted invalid timeout")
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid provider input mutated path: %v", err)
	}
}

func TestOfflineProviderOperationsRequireExactLiveTargetAuthority(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(home, "source.db")
	target := filepath.Join(home, "target.db")
	called := false
	if _, err := MaintainOffline(t.Context(), target, time.Second); err == nil {
		t.Fatal("maintenance accepted missing authority")
	}
	if err := MigrateStagedOfflineFrom(
		t.Context(), source, target, time.Second, 1,
		func(context.Context, string) error { called = true; return nil },
		func(context.Context, string) error { return nil },
	); err == nil || called {
		t.Fatalf("staged migration without authority called=%t err=%v", called, err)
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	wrongCtx, err := fence.MigrationContext(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateStagedOfflineFrom(
		wrongCtx, source, target, time.Second, 1,
		func(context.Context, string) error { called = true; return nil },
		func(context.Context, string) error { return nil },
	); err == nil || called {
		t.Fatalf("wrong-target authority called=%t err=%v", called, err)
	}
	targetCtx, err := fence.MigrationContext(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	if err := MigrateStagedOfflineFrom(
		targetCtx, source, target, time.Second, 1,
		func(context.Context, string) error { called = true; return nil },
		func(context.Context, string) error { return nil },
	); err == nil || called {
		t.Fatalf("expired authority called=%t err=%v", called, err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected provider operation mutated target: %v", err)
	}
}

func TestProviderRejectsSymlinkedAncestorBeforeCreatingStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reparse-point coverage is platform-specific")
	}
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realDirectory, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	path := filepath.Join(alias, "store.db")
	if database, err := OpenStore(path, time.Second); err == nil || database != nil {
		if database != nil {
			_ = database.Close()
		}
		t.Fatalf("symlinked ancestor open = %#v, %v", database, err)
	}
	if _, err := os.Lstat(filepath.Join(realDirectory, "store.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected alias created store: %v", err)
	}
}

func TestInspectionSecuresProviderDirectoryBeforeOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows validates DACLs instead of POSIX modes")
	}
	root := t.TempDir()
	path := filepath.Join(root, "retained.db")
	database, openErr := OpenStore(path, 5*time.Second)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if _, err := database.Exec(`CREATE TABLE retained (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(t.Context(), path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inspection.Release() })
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("provider directory = %v, %v", info, err)
	}
}

func TestInspectionReportsSchemaAndImportReadiness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ready.db")
	database, err := OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := Configure(t.Context(), database, 5*time.Second, false); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		CREATE TABLE records(id INTEGER PRIMARY KEY, value TEXT NOT NULL);
		CREATE VIEW record_view AS SELECT id, value FROM records;
		CREATE TABLE storage_import_horizons(
			component TEXT PRIMARY KEY,
			closed_at TEXT NOT NULL
		);
		INSERT INTO storage_import_horizons(component, closed_at)
		VALUES ('records', '2026-09-08T00:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	if err := SetSchemaVersion(t.Context(), database, 3); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	inspection, err := Inspect(t.Context(), path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Release()
	if !inspection.Exists || inspection.Empty || inspection.Version != 3 {
		t.Fatalf("inspection = %#v", inspection)
	}
	objects, err := inspection.HasSchemaObjects(
		t.Context(), "table", "records", "storage_import_horizons",
	)
	if err != nil || !objects {
		t.Fatalf("schema objects ready=%t err=%v", objects, err)
	}
	if objects, err = inspection.HasSchemaObjects(t.Context(), "table", "missing"); err != nil || objects {
		t.Fatalf("missing object ready=%t err=%v", objects, err)
	}
	if objects, err = inspection.HasSchemaObjects(t.Context(), "view", "record_view"); err != nil || !objects {
		t.Fatalf("typed view ready=%t err=%v", objects, err)
	}
	if objects, err = inspection.HasSchemaObjects(t.Context(), "table", "record_view"); err != nil || objects {
		t.Fatalf("view satisfied table contract=%t err=%v", objects, err)
	}
	if columns, err := inspection.HasTableColumns(t.Context(), "record_view", "id"); err != nil || columns {
		t.Fatalf("view satisfied table-column contract=%t err=%v", columns, err)
	}
	columns, err := inspection.HasTableColumns(t.Context(), "records", "id", "value")
	if err != nil || !columns {
		t.Fatalf("table columns ready=%t err=%v", columns, err)
	}
	if columns, err = inspection.HasTableColumns(t.Context(), "records", "missing"); err != nil || columns {
		t.Fatalf("missing column ready=%t err=%v", columns, err)
	}
	closed, err := inspection.HasImportHorizon(t.Context(), "records")
	if err != nil || !closed {
		t.Fatalf("import horizon closed=%t err=%v", closed, err)
	}
	if closed, err = inspection.HasImportHorizon(t.Context(), "missing"); err != nil || closed {
		t.Fatalf("missing import horizon closed=%t err=%v", closed, err)
	}
	if err := inspection.Release(); err != nil {
		t.Fatal(err)
	}
	if ready, err := inspection.HasSchemaObjects(t.Context(), "table", "records"); err == nil || ready {
		t.Fatalf("released schema query = %t, %v", ready, err)
	}
}
