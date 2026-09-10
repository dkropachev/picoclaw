//go:build linux || android

package databasemigration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxBackupStatusExchangeRejectsInvalidAndMissingPaths(t *testing.T) {
	if displaced, err := exchangeBackupStatusFile("relative", "other"); displaced != "" || err == nil {
		t.Fatalf("relative status exchange = %q, %v", displaced, err)
	}
	root := t.TempDir()
	source := filepath.Join(root, "missing-source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if displaced, err := exchangeBackupStatusFile(source, target); displaced != "" || err == nil {
		t.Fatalf("missing status exchange = %q, %v", displaced, err)
	}
	if err := rollbackBackupStatusFile(source, target, target); err == nil {
		t.Fatal("status rollback accepted an unrelated displaced path")
	}
	if err := rollbackBackupStatusFile("relative", "other", "relative"); err == nil {
		t.Fatal("status rollback accepted invalid paths")
	}
	if err := validateBackupStatusExchangePaths(target, target); err == nil {
		t.Fatal("status exchange accepted identical paths")
	}
}
