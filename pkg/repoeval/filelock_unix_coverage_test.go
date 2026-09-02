//go:build unix

package repoeval

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRepositoryEvaluationFileLockFailureBranches(t *testing.T) {
	originalMkdir := repositoryEvaluationMkdirLockDir
	originalOpen := repositoryEvaluationOpenLockFile
	originalFlock := repositoryEvaluationFlock
	t.Cleanup(func() {
		repositoryEvaluationMkdirLockDir = originalMkdir
		repositoryEvaluationOpenLockFile = originalOpen
		repositoryEvaluationFlock = originalFlock
	})

	t.Run("irregular", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(repositoryEvaluationTestLockPath(t, root, "store.lock"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := lockRepositoryEvaluationStore(root); err == nil {
			t.Fatal("lock accepted directory lock path")
		}
	})
	t.Run("broad permissions", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "state")
		if err := os.WriteFile(repositoryEvaluationTestLockPath(t, root, "store.lock"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := lockRepositoryEvaluationStore(root); err == nil {
			t.Fatal("lock accepted broad lock permissions")
		}
	})
	t.Run("mkdir", func(t *testing.T) {
		repositoryEvaluationMkdirLockDir = func(string, os.FileMode) error { return errors.New("mkdir") }
		t.Cleanup(func() { repositoryEvaluationMkdirLockDir = originalMkdir })
		if _, err := lockRepositoryEvaluationStore(filepath.Join(t.TempDir(), "state")); err == nil {
			t.Fatal("lock ignored mkdir error")
		}
	})
	t.Run("open", func(t *testing.T) {
		repositoryEvaluationMkdirLockDir = originalMkdir
		repositoryEvaluationOpenLockFile = func(string, int, os.FileMode) (*os.File, error) { return nil, errors.New("open") }
		t.Cleanup(func() { repositoryEvaluationOpenLockFile = originalOpen })
		if _, err := lockRepositoryEvaluationStore(filepath.Join(t.TempDir(), "state")); err == nil {
			t.Fatal("lock ignored open error")
		}
	})
	t.Run("flock", func(t *testing.T) {
		repositoryEvaluationOpenLockFile = originalOpen
		repositoryEvaluationFlock = func(int, int) error { return errors.New("flock") }
		t.Cleanup(func() { repositoryEvaluationFlock = originalFlock })
		if _, err := lockRepositoryEvaluationStore(filepath.Join(t.TempDir(), "state")); err == nil {
			t.Fatal("lock ignored flock error")
		}
	})
	t.Run("unlock", func(t *testing.T) {
		repositoryEvaluationFlock = func(_ int, operation int) error {
			if operation != unix.LOCK_EX && operation != unix.LOCK_UN {
				t.Errorf("unexpected flock operation %d", operation)
			}
			return nil
		}
		unlock, err := lockRepositoryEvaluationStore(filepath.Join(t.TempDir(), "state"))
		if err != nil {
			t.Fatal(err)
		}
		unlock()
	})
}

func TestRepositoryEvaluationControllerLockBranches(t *testing.T) {
	originalMkdir := repositoryEvaluationMkdirLockDir
	originalOpen := repositoryEvaluationOpenLockFile
	originalFlock := repositoryEvaluationFlock
	t.Cleanup(func() {
		repositoryEvaluationMkdirLockDir = originalMkdir
		repositoryEvaluationOpenLockFile = originalOpen
		repositoryEvaluationFlock = originalFlock
	})

	t.Run("irregular", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := os.Mkdir(repositoryEvaluationTestLockPath(t, store.root, "controller.lock"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LockController(); err == nil {
			t.Fatal("controller accepted directory lock")
		}
	})
	t.Run("broad permissions", func(t *testing.T) {
		store := NewStore(t.TempDir())
		lockPath := repositoryEvaluationTestLockPath(t, store.root, "controller.lock")
		if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LockController(); err == nil {
			t.Fatal("controller accepted broad lock permissions")
		}
	})
	t.Run("inaccessible lock paths", func(t *testing.T) {
		parent := t.TempDir()
		store := NewStore(filepath.Join(parent, "workspace"))
		if err := os.Chmod(parent, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
		if _, err := store.LockController(); err == nil {
			t.Skip("filesystem user bypasses directory permissions")
		}
		if _, err := lockRepositoryEvaluationStore(store.root); err == nil {
			t.Skip("filesystem user bypasses directory permissions")
		}
	})
	t.Run("mkdir", func(t *testing.T) {
		repositoryEvaluationMkdirLockDir = func(string, os.FileMode) error { return errors.New("mkdir") }
		t.Cleanup(func() { repositoryEvaluationMkdirLockDir = originalMkdir })
		if _, err := NewStore(t.TempDir()).LockController(); err == nil {
			t.Fatal("controller ignored mkdir error")
		}
	})
	t.Run("open", func(t *testing.T) {
		repositoryEvaluationMkdirLockDir = originalMkdir
		repositoryEvaluationOpenLockFile = func(string, int, os.FileMode) (*os.File, error) {
			return nil, errors.New("open")
		}
		t.Cleanup(func() { repositoryEvaluationOpenLockFile = originalOpen })
		if _, err := NewStore(t.TempDir()).LockController(); err == nil {
			t.Fatal("controller ignored open error")
		}
	})
	t.Run("flock errors and unlock", func(t *testing.T) {
		repositoryEvaluationOpenLockFile = originalOpen
		store := NewStore(t.TempDir())
		repositoryEvaluationFlock = func(int, int) error { return unix.EWOULDBLOCK }
		if _, err := store.LockController(); !errors.Is(err, ErrControllerLocked) {
			t.Fatalf("controller contention error = %v", err)
		}
		repositoryEvaluationFlock = func(_ int, operation int) error {
			if operation == unix.LOCK_EX|unix.LOCK_NB {
				return unix.EPERM
			}
			return nil
		}
		if _, err := NewStore(t.TempDir()).LockController(); !errors.Is(err, unix.EPERM) {
			t.Fatalf("controller flock error = %v", err)
		}
		repositoryEvaluationFlock = originalFlock
		unlock, err := NewStore(t.TempDir()).LockController()
		if err != nil {
			t.Fatal(err)
		}
		unlock()
	})
}
