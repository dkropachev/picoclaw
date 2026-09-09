//go:build unix

package databasereadiness

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestLegacyInputDiscoveryRejectsIrregularEntryAfterRegularInput(t *testing.T) {
	root := t.TempDir()
	writeLegacyDiscoveryFile(t, filepath.Join(root, "a.json"))
	if err := syscall.Mkfifo(filepath.Join(root, "z-pipe"), 0o600); err != nil {
		t.Fatal(err)
	}

	found, err := legacyInputExists(context.Background(), []string{root}, generationSet{})
	if found || !errors.Is(err, errLegacyIntegrity) || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("legacyInputExists() = %v, %v", found, err)
	}
}
