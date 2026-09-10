//go:build unix && !aix

package databasemigration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackupStatusHandleLockRejectsConcurrentOpenDescription(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status")
	if err := os.WriteFile(path, []byte("status"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	unlock, err := lockBackupStatusHandle(first)
	if err != nil {
		t.Fatal(err)
	}
	if contenderUnlock, contenderErr := lockBackupStatusHandle(second); contenderErr == nil {
		_ = contenderUnlock()
		t.Fatal("concurrent status lock succeeded")
	}
	if unlockErr := unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}
	reacquired, err := lockBackupStatusHandle(second)
	if err != nil {
		t.Fatal(err)
	}
	if unlockErr := reacquired(); unlockErr != nil {
		t.Fatal(unlockErr)
	}
}
