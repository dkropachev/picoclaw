//go:build unix

package sqliteprovider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenStoreRejectsHardlinkAlias(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "store.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(root, "alias.db")); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if _, err := OpenStore(path, 5*time.Second); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "hardlink") {
		t.Fatalf("hard-linked store error = %v", err)
	}
}
