//go:build !windows

package fstools

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestFileIdentityCatalogRejectsSpecialTreeEntryWithoutPathDisclosure(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "private-runtime-pipe")
	if err := syscall.Mkfifo(secret, 0o600); err != nil {
		t.Skipf("FIFO unavailable: %v", err)
	}
	catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{TreeRoots: []string{root}})
	if err == nil || catalog != nil || strings.Contains(err.Error(), secret) ||
		strings.Contains(err.Error(), filepath.Base(secret)) {
		t.Fatalf("special-entry catalog = %#v, %v", catalog, err)
	}
	if removeErr := os.Remove(secret); removeErr != nil {
		t.Fatal(removeErr)
	}
}
