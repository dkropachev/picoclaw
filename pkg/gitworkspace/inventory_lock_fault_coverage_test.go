//go:build android || darwin || dragonfly || freebsd || ios || linux || netbsd || openbsd || solaris

package gitworkspace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestInventoryLocksRejectNamedPipes(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, "inventory.lock")
	if err := unix.Mkfifo(lockPath, 0o600); err != nil {
		t.Skipf("named pipes unavailable: %v", err)
	}
	if unlock, err := lockInventoryFile(t.Context(), lockPath); err == nil || unlock != nil ||
		!strings.Contains(err.Error(), "regular file") {
		t.Fatalf("named-pipe inventory lock = unlock:%v error:%v", unlock != nil, err)
	}

	identity, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(root, "operation.lock")
	if err := unix.Mkfifo(childPath, 0o600); err != nil {
		t.Skipf("named pipes unavailable: %v", err)
	}
	if unlock, err := lockInventoryFileInDirectory(
		t.Context(), root, filepath.Base(childPath), identity,
	); err == nil || unlock != nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("named-pipe operation lock = unlock:%v error:%v", unlock != nil, err)
	}
}

func TestInventoryLockDetectsRootReplacementAfterContention(t *testing.T) {
	root := t.TempDir()
	lockRoot := filepath.Join(root, "locks")
	if err := os.Mkdir(lockRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	identity, err := os.Lstat(lockRoot)
	if err != nil {
		t.Fatal(err)
	}
	lockName := "operation.lock"
	lockPath := filepath.Join(lockRoot, lockName)
	blocker, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	if err := os.Chmod(lockPath, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(blocker.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type lockResult struct {
		unlock func()
		err    error
	}
	result := make(chan lockResult, 1)
	go func() {
		unlock, lockErr := lockInventoryFileInDirectory(ctx, lockRoot, lockName, identity)
		result <- lockResult{unlock: unlock, err: lockErr}
	}()

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	probe := time.NewTicker(5 * time.Millisecond)
	defer probe.Stop()
	secured := false
	for !secured {
		select {
		case outcome := <-result:
			if outcome.unlock != nil {
				outcome.unlock()
			}
			t.Fatalf("lock attempt returned before contention setup completed: %v", outcome.err)
		case <-deadline.C:
			t.Fatal("lock attempt did not secure the contended file")
		case <-probe.C:
			info, statErr := os.Stat(lockPath)
			secured = statErr == nil && info.Mode().Perm() == 0o600
		}
	}

	displacedRoot := filepath.Join(root, "locks-original")
	if err := os.Rename(lockRoot, displacedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lockRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(blocker.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	outcome := <-result
	if outcome.unlock != nil {
		outcome.unlock()
	}
	if outcome.err == nil || !strings.Contains(outcome.err.Error(), "root") {
		t.Fatalf("replaced operation lock root = unlock:%v error:%v", outcome.unlock != nil, outcome.err)
	}
}

func TestSecureOpenedInventoryLockPropagatesChmodFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires a read-only procfs file")
	}
	file, err := os.Open("/proc/self/status")
	if err != nil {
		t.Skipf("procfs unavailable: %v", err)
	}
	defer file.Close()
	if err := secureOpenedInventoryLock(file); err == nil {
		t.Fatal("read-only procfs lock was secured")
	}
}
