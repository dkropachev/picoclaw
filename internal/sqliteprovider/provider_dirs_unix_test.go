//go:build unix && !aix

package sqliteprovider

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestProviderDirectoryCreationRejectsWritableBoundary(t *testing.T) {
	_, trusted := trustedProviderUnixDirectory(unix.Stat_t{
		Mode: unix.S_IFDIR | 0o777,
		Uid:  uint32(os.Geteuid()),
	}, false)
	if trusted {
		t.Fatal("provider trusted a reachable writable directory boundary")
	}
}

func TestProviderDirectoryCreationPinsSafeComponents(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "one", "two")
	if err := makeProviderDirectories(target, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, current := range []string{target, filepath.Dir(target)} {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("created provider component %q = %#v, %v", current, info, err)
		}
	}
}
