package repoeval

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func repositoryEvaluationTestLockPath(t *testing.T, root, name string) string {
	t.Helper()
	path, err := repositoryEvaluationLockPath(root, name)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRepositoryEvaluationLocksUsePrivateStoreLocalNamespace(t *testing.T) {
	store := NewSQLiteStore(t.TempDir())
	if err := os.Mkdir(store.root+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	unlockStore, err := store.lock()
	if err != nil {
		t.Fatal(err)
	}
	unlockStore()
	unlockController, err := store.LockController()
	if err != nil {
		t.Fatal(err)
	}
	unlockController()
	directory := filepath.Join(store.root, repositoryEvaluationLockDirectory)
	for _, name := range []string{"store.lock", "controller.lock"} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("lock %q is unsafe: %#v, %v", name, info, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("lock %q mode=%04o", name, info.Mode().Perm())
		}
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
		if release, err := store.lock(); err == nil {
			release()
			t.Fatal("symlinked private store lock was accepted")
		}
	}
}
