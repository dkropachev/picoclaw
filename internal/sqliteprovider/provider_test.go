package sqliteprovider

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

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
	adopted, err := OpenStore(path, 5*time.Second)
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
	if adopted, err := OpenStore(path, 5*time.Second); err == nil || adopted != nil {
		if adopted != nil {
			_ = adopted.Close()
		}
		t.Fatalf("replacement generation adoption = %#v, %v", adopted, err)
	}
	if err := inspection.database.Ping(); err == nil {
		t.Fatal("replaced inspection pool remained open")
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
