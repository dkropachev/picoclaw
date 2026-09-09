//go:build linux

package databasemigration

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPinnedBackupTreeRemovalRejectsMountedChildWithoutDeletingSource(t *testing.T) {
	parent := t.TempDir()
	tree := filepath.Join(parent, "tree")
	mountPoint := filepath.Join(tree, "mounted")
	if err := os.MkdirAll(mountPoint, 0o700); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	sentinel := filepath.Join(external, "sentinel")
	if err := os.WriteFile(sentinel, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(external, mountPoint, "", unix.MS_BIND, ""); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.ENOSYS) {
			t.Skipf("bind mounts unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unix.Unmount(mountPoint, 0); err != nil &&
			!errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
			t.Errorf("unmount test bind: %v", err)
		}
	})

	expected := backupRemovalDirectoryIdentity(t, tree)
	if err := removePinnedBackupTreeIdentity(tree, expected); !errors.Is(err, errExactBackupMountBoundary) {
		t.Fatalf("mounted-child removal = %v", err)
	}
	if payload, err := os.ReadFile(sentinel); err != nil || string(payload) != "retain" {
		t.Fatalf("external mounted content changed = %q, %v", payload, err)
	}
	if info, err := os.Lstat(tree); err != nil || !info.IsDir() {
		t.Fatalf("rejected removal target changed = %#v, %v", info, err)
	}
}

func TestOpenExactBackupRemovalRootRejectsFIFOReplacement(t *testing.T) {
	parent := t.TempDir()
	leaf := "directory"
	path := filepath.Join(parent, leaf)
	retained := filepath.Join(parent, "retained")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	opened, err := openExactBackupChild(root, leaf)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if err := os.Rename(path, retained); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Skipf("FIFOs unavailable: %v", err)
	}
	child, err := openExactBackupRemovalRoot(root, leaf, opened)
	if child != nil {
		_ = child.Close()
	}
	if err == nil {
		t.Fatal("FIFO replacement opened as removal root")
	}
	if info, statErr := os.Lstat(retained); statErr != nil || !info.IsDir() {
		t.Fatalf("retained directory changed = %#v, %v", info, statErr)
	}
}
