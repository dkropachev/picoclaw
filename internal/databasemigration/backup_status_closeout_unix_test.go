//go:build unix && !aix

package databasemigration

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestUnixBackupStatusLockRejectsNilHandle(t *testing.T) {
	if release, err := lockBackupStatusHandle(nil); release != nil || err == nil {
		t.Fatalf("nil status lock handle = %t, %v", release != nil, err)
	}
}

func TestUnixLockedBackupStatusRejectsHardlinkAddedDuringRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status")
	if err := os.WriteFile(path, []byte("status"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "status-alias")
	ops := defaultBackupStatusReadOps()
	ops.readAll = func(reader io.Reader) ([]byte, error) {
		payload, readErr := io.ReadAll(reader)
		if linkErr := os.Link(path, alias); linkErr != nil {
			t.Skipf("hard links unavailable: %v", linkErr)
		}
		return payload, readErr
	}
	if _, _, err := readLockedBackupStatusWithOps(
		file, path, info, backupMaxStatusSize, ops,
	); err == nil {
		t.Fatal("locked status read accepted a hardlink added during the read")
	}
}
