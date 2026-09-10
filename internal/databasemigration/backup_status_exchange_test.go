//go:build linux || android || darwin || windows

package databasemigration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackupStatusAtomicExchangeCanRestoreIncumbent(t *testing.T) {
	parent := t.TempDir()
	if err := secureAndValidateBackupDirectory(parent); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(parent, "source")
	target := filepath.Join(parent, "target")
	writeMigrationFile(t, source, []byte("replacement"))
	writeMigrationFile(t, target, []byte("incumbent"))
	displaced, err := exchangeBackupStatusFile(source, target)
	if err != nil {
		t.Fatal(err)
	}
	assertBackupStatusFileContent(t, target, "replacement")
	assertBackupStatusFileContent(t, displaced, "incumbent")
	if err := rollbackBackupStatusFile(source, target, displaced); err != nil {
		t.Fatal(err)
	}
	assertBackupStatusFileContent(t, target, "incumbent")
	assertBackupStatusFileContent(t, source, "replacement")
}

func assertBackupStatusFileContent(t *testing.T, path, want string) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil || string(payload) != want {
		t.Fatalf("%s = %q, %v; want %q", path, payload, err, want)
	}
}
