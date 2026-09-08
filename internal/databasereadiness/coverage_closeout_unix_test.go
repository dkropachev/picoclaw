//go:build unix

package databasereadiness

import (
	"errors"
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
