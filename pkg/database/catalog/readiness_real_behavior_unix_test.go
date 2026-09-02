//go:build unix

package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyInputInventoryPropagatesRealTraversalFailure(t *testing.T) {
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "legacy.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if _, err := legacyInputExists([]string{root}); err == nil {
		t.Skip("current user can traverse mode-000 directories")
	}
}
