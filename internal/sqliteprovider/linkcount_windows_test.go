//go:build windows

package sqliteprovider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestOpenStoreRejectsWindowsHardlinkAlias(t *testing.T) {
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
		t.Fatalf("hardlinked Windows store error = %v", err)
	}
}

func TestWindowsLinkCountMetadataFailureFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	original := windowsGenerationInformation
	windowsGenerationInformation = func(windows.Handle, *windows.ByHandleFileInformation) error {
		return windows.ERROR_INVALID_DATA
	}
	t.Cleanup(func() { windowsGenerationInformation = original })
	if generationHasSingleLink(path, info) {
		t.Fatal("generation with unavailable Windows link metadata was accepted")
	}
}
