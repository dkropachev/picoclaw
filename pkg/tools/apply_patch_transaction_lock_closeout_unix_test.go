//go:build unix

package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestApplyPatchTransactionLockCloseoutValidation(t *testing.T) {
	directory := t.TempDir()
	regular := filepath.Join(directory, "regular")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(directory, "symlink")
	if err := os.Symlink("regular", symlink); err != nil {
		t.Fatal(err)
	}
	if err := validateApplyPatchTransactionStatePath(filepath.Join(symlink, "child")); err == nil {
		t.Fatal("symlink state path succeeded")
	}
	if err := validateApplyPatchTransactionStatePathEntry("symlink", nil); err == nil {
		t.Fatal("nil path entry succeeded")
	}
	symlinkInfo, symlinkStatErr := os.Lstat(symlink)
	if symlinkStatErr != nil {
		t.Fatal(symlinkStatErr)
	}
	if err := validateApplyPatchTransactionStatePathEntry("symlink", symlinkInfo); err == nil {
		t.Fatal("symlink path entry succeeded")
	}
	if err := validateApplyPatchTransactionPrivateObject(nil, false); err == nil {
		t.Fatal("nil private object succeeded")
	}
	regularInfo, regularStatErr := os.Lstat(regular)
	if regularStatErr != nil {
		t.Fatal(regularStatErr)
	}
	if err := validateApplyPatchTransactionPrivateObject(regularInfo, true); err == nil {
		t.Fatal("regular file accepted as private directory")
	}
	if err := os.Chmod(regular, 0o644); err != nil {
		t.Fatal(err)
	}
	regularInfo, _ = os.Lstat(regular)
	if err := validateApplyPatchTransactionPrivateObject(regularInfo, false); err == nil {
		t.Fatal("wrong-mode private file succeeded")
	}
	privateDirectory := filepath.Join(directory, "private-directory")
	if err := os.Mkdir(privateDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	privateDirectoryInfo, _ := os.Lstat(privateDirectory)
	if err := validateApplyPatchTransactionPrivateObject(privateDirectoryInfo, true); err == nil {
		t.Fatal("wrong-mode private directory succeeded")
	}
}

func TestApplyPatchTransactionLockCloseoutAcquisitionAndCancellation(t *testing.T) {
	directory := t.TempDir()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	lock, lockErr := acquireApplyPatchTransactionFileLock(
		canceled,
		filepath.Join(directory, "canceled.lock"),
	)
	if !errors.Is(lockErr, context.Canceled) || lock != nil {
		t.Fatalf("pre-canceled lock = %#v, %v", lock, lockErr)
	}
	if err := os.Mkdir(filepath.Join(directory, "directory.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if lock, err := acquireApplyPatchTransactionFileLock(
		context.Background(), filepath.Join(directory, "directory.lock"),
	); err == nil || lock != nil {
		t.Fatalf("directory lock = %#v, %v", lock, err)
	}
	wrongMode := filepath.Join(directory, "wrong-mode.lock")
	if err := os.WriteFile(wrongMode, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if lock, err := acquireApplyPatchTransactionFileLock(context.Background(), wrongMode); err == nil || lock != nil {
		t.Fatalf("wrong-mode lock = %#v, %v", lock, err)
	}
	lockPath := filepath.Join(directory, "contended.lock")
	first, err := acquireApplyPatchTransactionFileLock(context.Background(), lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer waitCancel()
	second, secondErr := acquireApplyPatchTransactionFileLock(waitCtx, lockPath)
	if !errors.Is(secondErr, context.DeadlineExceeded) || second != nil {
		t.Fatalf("contended lock = %#v, %v", second, secondErr)
	}
	if info, err := first.fileInfo(); err != nil || info == nil {
		t.Fatalf("held lock info = %#v, %v", info, err)
	}
}

func TestApplyPatchTransactionLockCloseoutRevalidationAndMethods(t *testing.T) {
	if err := revalidateApplyPatchTransactionLockPath(nil, "missing"); err == nil {
		t.Fatal("nil lock revalidation succeeded")
	}
	if info, err := (*applyPatchTransactionUnixLock)(nil).fileInfo(); err == nil || info != nil {
		t.Fatalf("nil lock info = %#v, %v", info, err)
	}
	if err := (*applyPatchTransactionUnixLock)(nil).Close(); err != nil {
		t.Fatalf("nil lock close = %v", err)
	}
	if err := (&applyPatchTransactionUnixLock{}).Close(); err != nil {
		t.Fatalf("empty lock close = %v", err)
	}

	directory := t.TempDir()
	path := filepath.Join(directory, "lock")
	file, openErr := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if err := revalidateApplyPatchTransactionLockPath(file, path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := revalidateApplyPatchTransactionLockPath(file, path); err == nil {
		t.Fatal("replaced named lock revalidated")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	closedFile, closedOpenErr := os.Open(path)
	if closedOpenErr != nil {
		t.Fatal(closedOpenErr)
	}
	if err := closedFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := revalidateApplyPatchTransactionLockPath(closedFile, path); err == nil {
		t.Fatal("closed lock handle revalidated")
	}

	unlockedFile, unlockedOpenErr := os.OpenFile(filepath.Join(directory, "unlocked"), os.O_CREATE|os.O_RDWR, 0o600)
	if unlockedOpenErr != nil {
		t.Fatal(unlockedOpenErr)
	}
	unlocked := &applyPatchTransactionUnixLock{file: unlockedFile}
	if info, err := unlocked.fileInfo(); err == nil || info != nil {
		t.Fatalf("unlocked file info = %#v, %v", info, err)
	}
	if err := unlocked.Close(); err != nil {
		t.Fatalf("unlocked close = %v", err)
	}
}

func TestApplyPatchTransactionLockCloseoutErrorClassification(t *testing.T) {
	for _, unsupported := range []error{unix.ENOSYS, unix.EOPNOTSUPP, unix.EINVAL} {
		err := wrapApplyPatchTransactionLockError(unsupported)
		if !errors.Is(err, errApplyPatchTransactionUnsupported) || !errors.Is(err, unsupported) {
			t.Fatalf("unsupported lock error = %v", err)
		}
	}
	ordinary := wrapApplyPatchTransactionLockError(unix.EPERM)
	if errors.Is(ordinary, errApplyPatchTransactionUnsupported) || !errors.Is(ordinary, unix.EPERM) {
		t.Fatalf("ordinary lock error = %v", ordinary)
	}
}
