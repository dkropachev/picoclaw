package repoaudit

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"testing"
)

func repositoryReviewTestLockPath(t *testing.T, root, name string) string {
	t.Helper()
	path, err := repositoryReviewLockPath(root, name)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRepositoryReviewLockPathAndFileFailureBoundaries(t *testing.T) {
	if _, err := repositoryReviewLockPath("", "store.lock"); err == nil {
		t.Fatal("empty lock root was accepted")
	}

	directory := t.TempDir()
	directoryFile, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer directoryFile.Close()
	if err := secureRepositoryReviewLockFile(directory, directoryFile); err == nil {
		t.Fatal("directory lock handle was accepted")
	}
	if err := secureRepositoryReviewLockFile(filepath.Join(directory, "missing"), nil); err == nil {
		t.Fatal("nil lock handle was accepted")
	}

	lockPath := filepath.Join(directory, "lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	decoyPath := filepath.Join(directory, "decoy")
	if err := os.WriteFile(decoyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	decoy, err := os.OpenFile(decoyPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer decoy.Close()
	if err := secureRepositoryReviewLockFile(lockPath, decoy); err == nil {
		t.Fatal("lock handle opened under a different identity was accepted")
	}
}

func TestRepositoryReviewLocksUsePrivateStoreLocalNamespace(t *testing.T) {
	store := NewSQLiteStore(t.TempDir())
	// A pre-upgrade sibling lock is no longer authoritative and cannot block
	// the protected store-local lock namespace.
	if err := os.Mkdir(store.root+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	unlockStore, err := store.lock("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	unlockStore()
	unlockController, err := store.LockAutomationController()
	if err != nil {
		t.Fatal(err)
	}
	unlockController()
	unlockAttempt, acquired, err := store.TryLockIssueGenerationAttempt(
		"owner/repo", "draft", "generation",
	)
	if err != nil || !acquired {
		t.Fatalf("attempt lock acquired=%t err=%v", acquired, err)
	}
	unlockAttempt()
	for _, acquire := range []func() (func(), error){
		func() (func(), error) { return store.AcquireIssueGenerationSlot(context.Background(), 1) },
		func() (func(), error) { return store.AcquireValidationSlot(context.Background()) },
		func() (func(), error) { return store.AcquireDeduplicationSlot(context.Background()) },
	} {
		release, acquireErr := acquire()
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		release()
	}
	directory := filepath.Join(store.root, repositoryReviewLockDirectory)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
		info, statErr := entry.Info()
		if statErr != nil || !info.Mode().IsRegular() {
			t.Fatalf("lock %q is unsafe: %#v, %v", entry.Name(), info, statErr)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("lock %q mode=%04o", entry.Name(), info.Mode().Perm())
		}
	}
	sort.Strings(got)
	want := []string{
		"controller.lock",
		"deduplication-slot-00.lock",
		"issue-generation-" + stableID("", "owner/repo", "draft", "generation") + ".lock",
		"issue-writer-slot-00.lock",
		"store.lock",
		"validation-slot-00.lock",
	}
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("lock inventory=%v want=%v", got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(directory)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("lock directory mode=%v err=%v", info, err)
		}
		storeLock := filepath.Join(directory, "store.lock")
		if err := os.Remove(storeLock); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(t.TempDir(), "target"), storeLock); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if release, err := store.lock("owner/repo"); err == nil {
			release()
			t.Fatal("symlinked private store lock was accepted")
		}
	}
}
