//go:build unix && !aix

package databasemigration

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type backupForeignOwnerInfo struct{ os.FileInfo }

func (info backupForeignOwnerInfo) Sys() any {
	return &syscall.Stat_t{Nlink: 1, Uid: uint32(os.Geteuid()) + 1}
}

func TestBackupSourceValidationRejectsForeignOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateBackupSourceFile(backupForeignOwnerInfo{FileInfo: info}, nil); err == nil {
		t.Fatal("foreign-owned backup source was accepted")
	}
}
