//go:build unix

package repoaudit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	t.Run("controller secure identity", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		decoy, err := os.CreateTemp(t.TempDir(), "controller-decoy-")
		if err != nil {
			t.Fatal(err)
		}
		repositoryReviewOpenLockFile = func(string, int, os.FileMode) (*os.File, error) {
			return decoy, nil
		}
		if _, err := NewStore(t.TempDir()).LockAutomationController(); err == nil {
			t.Fatal("controller accepted a lock opened under a different identity")
		}
		if _, err := decoy.Stat(); err == nil {
			t.Fatal("rejected controller lock was not closed")
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
	t.Run("store secure identity", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		decoy, err := os.CreateTemp(t.TempDir(), "store-decoy-")
		if err != nil {
			t.Fatal(err)
		}
		repositoryReviewOpenLockFile = func(string, int, os.FileMode) (*os.File, error) {
			return decoy, nil
		}
		if _, err := lockRepositoryReviewStore(NewStore(t.TempDir()).root); err == nil {
			t.Fatal("store accepted a lock opened under a different identity")
		}
		if _, err := decoy.Stat(); err == nil {
			t.Fatal("rejected store lock was not closed")
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
	t.Run("issue lock secure identity", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		decoy, err := os.CreateTemp(t.TempDir(), "issue-decoy-")
		if err != nil {
			t.Fatal(err)
		}
		repositoryReviewOpenLockFile = func(string, int, os.FileMode) (*os.File, error) {
			return decoy, nil
		}
		if _, _, err := NewStore(t.TempDir()).TryLockIssueGenerationAttempt(
			"owner/repo", "draft", "generation",
		); err == nil {
			t.Fatal("issue attempt accepted a lock opened under a different identity")
		}
		if _, err := decoy.Stat(); err == nil {
			t.Fatal("rejected issue lock was not closed")
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
		lockPath := repositoryReviewTestLockPath(
			t,
			store.root,
			"issue-generation-"+
				stableID("", "owner/repo", "draft", "generation")+".lock",
		)
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
	t.Run("store-local path errors", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		workspace := filepath.Join(t.TempDir(), "workspace-file")
		if err := os.WriteFile(workspace, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		store := NewStore(workspace)
		calls := map[string]func() error{
			"issue attempt": func() error {
				_, _, err := store.TryLockIssueGenerationAttempt("owner/repo", "draft", "generation")
				return err
			},
			"issue slot": func() error {
				_, err := store.AcquireIssueGenerationSlot(context.Background(), 1)
				return err
			},
			"deduplication slot": func() error {
				_, err := store.AcquireDeduplicationSlot(context.Background())
				return err
			},
			"validation slot": func() error {
				_, err := store.AcquireValidationSlot(context.Background())
				return err
			},
		}
		for name, call := range calls {
			if err := call(); err == nil {
				t.Fatalf("%s accepted a lock path below a regular file", name)
			}
		}
	})
}
