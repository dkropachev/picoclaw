//go:build unix

package repoaudit

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func restoreRepositoryReviewLockHooks(t *testing.T) {
	t.Helper()
	mkdir := repositoryReviewMkdirLockDir
	open := repositoryReviewOpenLockFile
	flock := repositoryReviewFlock
	t.Cleanup(func() {
		repositoryReviewMkdirLockDir = mkdir
		repositoryReviewOpenLockFile = open
		repositoryReviewFlock = flock
	})
}

func TestRepositoryReviewUnixLockSyscallFailures(t *testing.T) {
	sentinel := errors.New("injected lock syscall failure")

	t.Run("controller mkdir", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		repositoryReviewMkdirLockDir = func(string, os.FileMode) error { return sentinel }
		if _, err := NewStore(t.TempDir()).LockAutomationController(); !errors.Is(err, sentinel) {
			t.Fatalf("controller mkdir error = %v", err)
		}
	})
	t.Run("controller open", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		repositoryReviewOpenLockFile = func(string, int, os.FileMode) (*os.File, error) {
			return nil, sentinel
		}
		if _, err := NewStore(t.TempDir()).LockAutomationController(); !errors.Is(err, sentinel) {
			t.Fatalf("controller open error = %v", err)
		}
	})
	t.Run("controller flock", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		repositoryReviewFlock = func(int, int) error { return unix.EPERM }
		if _, err := NewStore(t.TempDir()).LockAutomationController(); !errors.Is(err, unix.EPERM) {
			t.Fatalf("controller flock error = %v", err)
		}
	})
	t.Run("store mkdir", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		repositoryReviewMkdirLockDir = func(string, os.FileMode) error { return sentinel }
		if _, err := lockRepositoryReviewStore(NewStore(t.TempDir()).root); !errors.Is(err, sentinel) {
			t.Fatalf("store mkdir error = %v", err)
		}
	})
	t.Run("store open", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		repositoryReviewOpenLockFile = func(string, int, os.FileMode) (*os.File, error) {
			return nil, sentinel
		}
		if _, err := lockRepositoryReviewStore(NewStore(t.TempDir()).root); !errors.Is(err, sentinel) {
			t.Fatalf("store open error = %v", err)
		}
	})
	t.Run("store flock", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		repositoryReviewFlock = func(int, int) error { return unix.EPERM }
		if _, err := lockRepositoryReviewStore(NewStore(t.TempDir()).root); !errors.Is(err, unix.EPERM) {
			t.Fatalf("store flock error = %v", err)
		}
	})
}
