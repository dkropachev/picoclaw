//go:build unix

package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const applyPatchTransactionLockRetryInterval = 10 * time.Millisecond

type applyPatchTransactionUnixLock struct {
	mu     sync.Mutex
	file   *os.File
	locked bool
}

func validateApplyPatchTransactionStatePath(path string) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if err = validateApplyPatchTransactionStatePathEntry(current, info); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return errors.New("inspect apply-patch transaction state path")
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func validateApplyPatchTransactionStatePathEntry(_ string, info os.FileInfo) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("apply-patch transaction state path contains a symlink")
	}
	return nil
}

func validateApplyPatchTransactionPrivateObject(info os.FileInfo, directory bool) error {
	if info == nil {
		return errors.New("apply-patch transaction private object is unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("apply-patch transaction private object has an invalid owner")
	}
	if directory {
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			return errors.New("apply-patch transaction private directory must have mode 0700")
		}
		return nil
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("apply-patch transaction private file must have mode 0600")
	}
	return nil
}

func acquireApplyPatchTransactionFileLock(
	ctx context.Context,
	path string,
) (applyPatchTransactionHeldLock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateApplyPatchTransactionStatePath(filepath.Dir(path)); err != nil {
		return nil, err
	}
	existed := true
	if info, err := os.Lstat(path); err == nil {
		if err = validateApplyPatchTransactionStatePathEntry(path, info); err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("apply-patch transaction lock is not a regular file")
		}
	} else if os.IsNotExist(err) {
		existed = false
	} else {
		return nil, errors.New("inspect apply-patch transaction lock")
	}
	fd, err := unix.Open(
		path,
		unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("open apply-patch transaction lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open apply-patch transaction lock: invalid descriptor")
	}
	failed := true
	defer func() {
		if failed {
			_ = file.Close()
		}
	}()
	if !existed {
		if err = file.Chmod(0o600); err != nil {
			return nil, fmt.Errorf("secure apply-patch transaction lock: %w", err)
		}
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if err = validateApplyPatchTransactionPrivateObject(info, false); err != nil {
		return nil, err
	}
	if err = file.Sync(); err != nil {
		return nil, fmt.Errorf("sync apply-patch transaction lock: %w", err)
	}
	named, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, named) || named.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(errors.New("apply-patch transaction lock changed while opening"), err)
	}
	if err = fileutil.SyncDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	ticker := time.NewTicker(applyPatchTransactionLockRetryInterval)
	defer ticker.Stop()
	for {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			if contextErr := ctx.Err(); contextErr != nil {
				_ = unix.Flock(fd, unix.LOCK_UN)
				return nil, contextErr
			}
			if err = revalidateApplyPatchTransactionLockPath(file, path); err != nil {
				_ = unix.Flock(fd, unix.LOCK_UN)
				return nil, err
			}
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return nil, wrapApplyPatchTransactionLockError(err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
	lock := &applyPatchTransactionUnixLock{file: file, locked: true}
	failed = false
	return lock, nil
}

func wrapApplyPatchTransactionLockError(err error) error {
	written := fmt.Errorf("lock apply-patch transaction state: %w", err)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
		return errors.Join(errApplyPatchTransactionUnsupported, written)
	}
	return written
}

func revalidateApplyPatchTransactionLockPath(file *os.File, path string) error {
	if file == nil {
		return errors.New("apply-patch transaction lock is unavailable")
	}
	handleInfo, err := file.Stat()
	if err != nil {
		return errors.New("inspect apply-patch transaction lock handle")
	}
	namedInfo, err := os.Lstat(path)
	if err != nil || !os.SameFile(handleInfo, namedInfo) ||
		namedInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("apply-patch transaction lock changed")
	}
	return validateApplyPatchTransactionPrivateObject(namedInfo, false)
}

func (lock *applyPatchTransactionUnixLock) fileInfo() (os.FileInfo, error) {
	if lock == nil {
		return nil, errors.New("apply-patch transaction lock is closed")
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.file == nil || !lock.locked {
		return nil, errors.New("apply-patch transaction lock is closed")
	}
	return lock.file.Stat()
}

func (lock *applyPatchTransactionUnixLock) Close() error {
	if lock == nil {
		return nil
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.file == nil {
		return nil
	}
	file := lock.file
	lock.file = nil
	wasLocked := lock.locked
	lock.locked = false
	var unlockErr error
	if wasLocked {
		unlockErr = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	}
	return errors.Join(unlockErr, file.Close())
}
