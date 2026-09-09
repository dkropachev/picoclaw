//go:build unix

package databasereadiness

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestLegacyDiscoveryRejectsIrregularRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := legacyInputExists(t.Context(), []string{path}, generationSet{})
	if found || !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("irregular legacy root = %t, %v", found, err)
	}
}

func TestLegacyDiscoveryPropagatesDirectoryOpenFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	root := t.TempDir()
	budget, err := newLegacyDiscoveryBudget(defaultLegacyDiscoveryLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	if found, err := walkLegacyDirectory(
		t.Context(), root, 0, generationSet{}, budget,
	); found || err == nil {
		t.Fatalf("unopenable legacy directory = %t, %v", found, err)
	}
}
