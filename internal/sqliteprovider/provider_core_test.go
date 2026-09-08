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

func TestProviderDSNAndLiveConfiguration(t *testing.T) {
	dsn, err := DSN(filepath.Join(t.TempDir(), "store with spaces.db"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"file:", "mode=rw", "foreign_keys", "busy_timeout", "5000", "synchronous", "FULL",
	} {
		if !strings.Contains(dsn, required) {
			t.Fatalf("DSN() = %q, missing %q", dsn, required)
		}
	}
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
		&foreignKeys, &busyTimeout, &synchronous,
	); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 {
		t.Fatalf("provider settings = %d, %d, %d", foreignKeys, busyTimeout, synchronous)
	}
}

func TestProviderRejectsInvalidPathsTimeoutsAndSymlinkAncestors(t *testing.T) {
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
	if runtime.GOOS == "windows" {
		return
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

func TestOpenStoreSecuresExistingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows validates DACLs instead of POSIX modes")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	database, err := OpenStore(filepath.Join(root, "store.db"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("provider directory = %v, %v", info, err)
	}
}
