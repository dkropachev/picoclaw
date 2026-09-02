//go:build linux

package repoaudit

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRepositoryReviewAutomationSnapshotLockFailure(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := os.Mkdir(
		repositoryReviewTestLockPath(t, store.root, "store.lock"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RepositoryReviewAutomationSnapshot(
		t.Context(),
		"rra_snapshot_lock_failure",
	); err == nil {
		t.Fatal("snapshot ignored an unsafe lock")
	}
}

func TestRepositoryReviewPurgeChildRootPermissionBoundaries(t *testing.T) {
	t.Run("open denied", func(t *testing.T) {
		rootPath := t.TempDir()
		childPath := filepath.Join(rootPath, "child")
		if err := os.Mkdir(childPath, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(childPath, 0o700) })
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		if _, _, err := openRepositoryReviewPurgeChildRoot(root, "child"); err == nil {
			t.Fatal("archive helper opened an unreadable child")
		}
	})

	t.Run("opened child cannot stat", func(t *testing.T) {
		rootPath := t.TempDir()
		childPath := filepath.Join(rootPath, "child")
		if err := os.Mkdir(childPath, 0o400); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(childPath, 0o700) })
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		if _, _, err := openRepositoryReviewPurgeChildRoot(root, "child"); err == nil {
			t.Fatal("archive helper trusted an unsearchable opened child")
		}
	})
}

func TestRepositoryReviewPurgeArchiveRootAbsolutePathFailure(t *testing.T) {
	original, err := os.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	parent := t.TempDir()
	removed := filepath.Join(parent, "removed-working-directory")
	if err := os.Mkdir(removed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(removed); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(removed); err != nil {
		_ = original.Chdir()
		t.Fatal(err)
	}
	_, _, absoluteErr := openRepositoryReviewPurgeArchiveRoot("relative")
	if err := original.Chdir(); err != nil {
		t.Fatal(err)
	}
	if absoluteErr == nil {
		t.Fatal("archive root resolved a relative path from a removed working directory")
	}
}

func TestRepositoryReviewPurgeChildRootDetectsExchange(t *testing.T) {
	rootPath := t.TempDir()
	childPath := filepath.Join(rootPath, "child")
	otherPath := filepath.Join(rootPath, "other")
	if err := os.Mkdir(childPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(otherPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				_ = unix.Renameat2(
					unix.AT_FDCWD,
					childPath,
					unix.AT_FDCWD,
					otherPath,
					unix.RENAME_EXCHANGE,
				)
			}
		}
	}()
	defer func() {
		close(stop)
		<-done
	}()

	for attempt := 0; attempt < 50_000; attempt++ {
		child, found, err := openRepositoryReviewPurgeChildRoot(root, "child")
		if child != nil {
			_ = child.Close()
		}
		if err != nil && !found && strings.Contains(err.Error(), "changed while opening") {
			return
		}
		if attempt%100 == 0 {
			runtime.Gosched()
		}
	}
	t.Fatal("archive helper did not observe a continuously exchanged child")
}

func TestRepositoryReviewAuditedArchiveLinuxFailures(t *testing.T) {
	t.Run("sync closed root", func(t *testing.T) {
		root, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
		if err := syncRepositoryReviewPurgeRoot(root); err == nil {
			t.Fatal("purge root sync accepted a closed root")
		}
	})

	t.Run("open denied", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "unreadable.json")
		if err := os.WriteFile(path, []byte("archive"), 0); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(directory)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		if err := removeRepositoryReviewAuditedArchive(root, repositoryReviewPurgeArchiveRecord{
			Name: "unreadable.json", Limit: 7,
		}); err == nil {
			t.Fatal("archive purge opened an unreadable file")
		}
	})

	t.Run("read failure", func(t *testing.T) {
		root, err := os.OpenRoot("/proc/self")
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		info, err := root.Lstat("mem")
		if err != nil {
			t.Fatal(err)
		}
		if err := removeRepositoryReviewAuditedArchive(root, repositoryReviewPurgeArchiveRecord{
			Name: "mem", Limit: 1, Mode: info.Mode().Perm(),
		}); err == nil {
			t.Fatal("archive purge ignored a source read failure")
		}
	})

	t.Run("remove denied", func(t *testing.T) {
		directory := t.TempDir()
		data := []byte("archive")
		path := filepath.Join(directory, "retained.json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(directory)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		if err := os.Chmod(directory, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
		digest := sha256.Sum256(data)
		if err := removeRepositoryReviewAuditedArchive(root, repositoryReviewPurgeArchiveRecord{
			Name: "retained.json", Digest: digest, Limit: int64(len(data)), Mode: 0o600,
		}); err == nil {
			t.Fatal("archive purge ignored a denied removal")
		}
	})

	t.Run("closed archive root", func(t *testing.T) {
		root, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
		if err := removeRepositoryReviewAuditedArchive(root, repositoryReviewPurgeArchiveRecord{
			Name: "closed",
		}); err == nil || errors.Is(err, os.ErrNotExist) {
			t.Fatalf("closed archive root error = %v", err)
		}
	})
}
