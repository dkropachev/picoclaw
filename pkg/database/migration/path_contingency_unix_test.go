//go:build unix

package migration

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMigrationPathValidationFailsFromRemovedWorkingDirectory(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	removed := filepath.Join(parent, "removed")
	if err := os.Mkdir(removed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(removed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	if err := os.Remove(removed); err != nil {
		if errors.Is(err, syscall.EBUSY) {
			t.Skip("platform does not unlink the current working directory")
		}
		t.Fatal(err)
	}
	if err := validateBackupAncestors("relative-backup"); err == nil {
		t.Fatal("relative backup ancestor resolved from a removed working directory")
	}
	if _, err := validateBackupParent("relative-backup", parent); err == nil {
		t.Fatal("relative backup parent resolved from a removed working directory")
	}
}
