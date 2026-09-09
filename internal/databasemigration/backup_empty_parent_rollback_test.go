package databasemigration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedEmptyBackupDirectoryRollbackRemovesOnlyEmptyIdentity(t *testing.T) {
	parent := t.TempDir()
	empty := filepath.Join(parent, "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	emptyIdentity := backupRemovalDirectoryIdentity(t, empty)
	if err := removePinnedEmptyBackupDirectoryIdentity(empty, emptyIdentity); err != nil {
		t.Fatalf("empty rollback = %v", err)
	}
	if _, err := os.Lstat(empty); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty rollback target remains: %v", err)
	}

	nonempty := filepath.Join(parent, "nonempty")
	if err := os.Mkdir(nonempty, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(nonempty, "payload")
	if err := os.WriteFile(payload, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	nonemptyIdentity := backupRemovalDirectoryIdentity(t, nonempty)
	if err := removePinnedEmptyBackupDirectoryIdentity(nonempty, nonemptyIdentity); err == nil ||
		!strings.Contains(err.Error(), "not empty") {
		t.Fatalf("nonempty rollback = %v", err)
	}
	if got, err := os.ReadFile(payload); err != nil || string(got) != "retain" {
		t.Fatalf("nonempty rollback changed payload = %q, %v", got, err)
	}
}
