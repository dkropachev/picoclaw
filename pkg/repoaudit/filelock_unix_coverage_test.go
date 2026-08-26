//go:build unix

package repoaudit

import (
	"errors"
	"os"
	"strings"
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
	t.Run("issue attempt validation", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		if _, acquired, err := NewStore(t.TempDir()).TryLockIssueGenerationAttempt(
			"", "", "",
		); err == nil || acquired {
			t.Fatalf("invalid issue attempt lock acquired=%v err=%v", acquired, err)
		}
	})
	t.Run("issue lock mkdir", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		repositoryReviewMkdirLockDir = func(string, os.FileMode) error { return sentinel }
		if _, _, err := NewStore(t.TempDir()).TryLockIssueGenerationAttempt(
			"owner/repo", "draft", "generation",
		); !errors.Is(err, sentinel) {
			t.Fatalf("issue lock mkdir error = %v", err)
		}
	})
	t.Run("issue lock open", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		repositoryReviewOpenLockFile = func(string, int, os.FileMode) (*os.File, error) {
			return nil, sentinel
		}
		if _, _, err := NewStore(t.TempDir()).TryLockIssueGenerationAttempt(
			"owner/repo", "draft", "generation",
		); !errors.Is(err, sentinel) {
			t.Fatalf("issue lock open error = %v", err)
		}
	})
	t.Run("issue lock flock", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		repositoryReviewFlock = func(int, int) error { return unix.EPERM }
		if _, _, err := NewStore(t.TempDir()).TryLockIssueGenerationAttempt(
			"owner/repo", "draft", "generation",
		); !errors.Is(err, unix.EPERM) {
			t.Fatalf("issue lock flock error = %v", err)
		}
	})
	t.Run("issue slot boundaries", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		store := NewStore(t.TempDir())
		if _, err := store.AcquireIssueGenerationSlot(t.Context(), 0); err == nil {
			t.Fatal("invalid issue slot maximum was accepted")
		}
		release, err := store.AcquireIssueGenerationSlot(nil, 1)
		if err != nil {
			t.Fatal(err)
		}
		release()
		repositoryReviewMkdirLockDir = func(string, os.FileMode) error { return sentinel }
		if _, err := store.AcquireIssueGenerationSlot(t.Context(), 1); !errors.Is(err, sentinel) {
			t.Fatalf("issue slot mkdir error = %v", err)
		}
	})
	t.Run("issue lock irregular file", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		store := NewStore(t.TempDir())
		lockPath := store.root + ".issue-generation-" +
			stableID("", "owner/repo", "draft", "generation") + ".lock"
		if err := os.MkdirAll(lockPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.TryLockIssueGenerationAttempt(
			"owner/repo", "draft", "generation",
		); err == nil {
			t.Fatal("irregular issue lock was accepted")
		}
	})
	t.Run("issue lock lstat error", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		if _, _, err := tryLockRepositoryReviewIssueFile(
			"/" + strings.Repeat("x", 5000),
		); err == nil {
			t.Fatal("oversized issue lock path was accepted")
		}
	})
}
