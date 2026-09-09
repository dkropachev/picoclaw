//go:build unix && !aix

package databasereadiness

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func TestPinnedLegacyOpenRejectsFIFOReplacementWithoutBlocking(t *testing.T) {
	for _, directory := range []bool{false, true} {
		name := "regular"
		if directory {
			name = "directory"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input")
			if directory {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("legacy"), 0o600); err != nil {
				t.Fatal(err)
			}
			opener := func(
				candidate string,
				objectType fileidentity.ObjectType,
			) (*os.File, bool, error) {
				if err := os.Rename(candidate, candidate+".old"); err != nil {
					return nil, false, err
				}
				if err := syscall.Mkfifo(candidate, 0o600); err != nil {
					return nil, false, err
				}
				return openLegacyNoFollow(candidate, objectType)
			}
			type result struct {
				file *pinnedLegacyFile
				err  error
			}
			finished := make(chan result, 1)
			go func() {
				file, _, err := openPinnedLegacyPathWith(
					path, fileidentity.ExistingWithType, opener,
				)
				finished <- result{file: file, err: err}
			}()
			select {
			case got := <-finished:
				if got.file != nil {
					_ = got.file.Close()
				}
				if !errors.Is(got.err, errLegacyIntegrity) {
					t.Fatalf("FIFO replacement = %#v, %v", got.file, got.err)
				}
			case <-time.After(time.Second):
				t.Fatal("FIFO replacement blocked past the bounded test deadline")
			}
		})
	}
}

func TestPinnedLegacyChildBindsIdentityBeforeOpen(t *testing.T) {
	root := t.TempDir()
	childPath := filepath.Join(root, "legacy.json")
	if err := os.WriteFile(childPath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent, exists, err := openPinnedLegacyPath(root)
	if err != nil || !exists {
		t.Fatalf("pin parent = %#v, %t, %v", parent, exists, err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	opener := func(
		parentHandle *os.File,
		path string,
		name string,
		objectType fileidentity.ObjectType,
	) (*os.File, bool, error) {
		if renameErr := os.Rename(path, path+".old"); renameErr != nil {
			return nil, false, renameErr
		}
		if writeErr := os.WriteFile(path, []byte("second"), 0o600); writeErr != nil {
			return nil, false, writeErr
		}
		return openLegacyChildNoFollow(parentHandle, path, name, objectType)
	}
	child, err := openPinnedLegacyChildWith(
		parent, "legacy.json", fileidentity.ExistingWithType, opener,
	)
	if child != nil {
		_ = child.Close()
	}
	if !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("child identity replacement = %#v, %v", child, err)
	}
}

func TestPinnedLegacyRootRejectsSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDirectory, "legacy.json"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realDirectory, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	found, err := legacyInputExists(
		t.Context(), []string{filepath.Join(alias, "legacy.json")}, generationSet{},
	)
	if found || !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("symlink ancestor = %t, %v", found, err)
	}
}

func TestPinnedLegacyRevalidationRejectsRetargetedSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "parent")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parentPath, "legacy.json")
	if err := os.WriteFile(path, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, exists, err := openPinnedLegacyPath(path)
	if err != nil || !exists {
		t.Fatalf("pin legacy file = %#v, %t, %v", file, exists, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	realPath := filepath.Join(root, "real")
	if err := os.Rename(parentPath, realPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, parentPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := file.revalidateNamed(); !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("retargeted ancestor revalidation = %v", err)
	}
}

func TestMissingLegacyRootStillRejectsRetargetedSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "parent")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	realPath := filepath.Join(root, "real")
	if err := os.Rename(parentPath, realPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, parentPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	found, err := legacyInputExists(
		t.Context(), []string{filepath.Join(parentPath, "missing.json")}, generationSet{},
	)
	if found || !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("missing leaf below symlink ancestor = %t, %v", found, err)
	}
}
