//go:build windows

package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

const applyPatchTransactionLockRetryInterval = 10 * time.Millisecond

type applyPatchTransactionWindowsLock struct {
	mu         sync.Mutex
	file       *os.File
	overlapped windows.Overlapped
	locked     bool
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

func validateApplyPatchTransactionStatePathEntry(path string, info os.FileInfo) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("apply-patch transaction state path contains a reparse point")
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return errors.New("inspect apply-patch transaction state path")
	}
	handle, err := windows.CreateFile(
		pathPointer,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return errors.New("inspect apply-patch transaction state path")
	}
	defer windows.CloseHandle(handle)
	var information windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &information); err != nil {
		return errors.New("inspect apply-patch transaction state path")
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("apply-patch transaction state path contains a reparse point")
	}
	return nil
}

func validateApplyPatchTransactionPrivateObject(info os.FileInfo, directory bool) error {
	if info == nil {
		return errors.New("apply-patch transaction private object is unavailable")
	}
	if directory && !info.IsDir() {
		return errors.New("apply-patch transaction private directory is invalid")
	}
	if !directory && !info.Mode().IsRegular() {
		return errors.New("apply-patch transaction private file is invalid")
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
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, errors.New("open apply-patch transaction lock")
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open apply-patch transaction lock: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("open apply-patch transaction lock: invalid handle")
	}
	failed := true
	defer func() {
		if failed {
			_ = file.Close()
		}
	}()
	var information windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &information); err != nil {
		return nil, err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return nil, errors.New("apply-patch transaction lock is not a regular file")
	}
	if err = windows.FlushFileBuffers(handle); err != nil {
		return nil, fmt.Errorf("sync apply-patch transaction lock: %w", err)
	}
	lock := &applyPatchTransactionWindowsLock{file: file}
	ticker := time.NewTicker(applyPatchTransactionLockRetryInterval)
	defer ticker.Stop()
	for {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		err = windows.LockFileEx(
			handle,
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			&lock.overlapped,
		)
		if err == nil {
			if contextErr := ctx.Err(); contextErr != nil {
				_ = windows.UnlockFileEx(handle, 0, 1, 0, &lock.overlapped)
				return nil, contextErr
			}
			if err = revalidateApplyPatchTransactionLockPath(file, path); err != nil {
				_ = windows.UnlockFileEx(handle, 0, 1, 0, &lock.overlapped)
				return nil, err
			}
			lock.locked = true
			break
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, wrapApplyPatchTransactionLockError(err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
	failed = false
	return lock, nil
}

func wrapApplyPatchTransactionLockError(err error) error {
	written := fmt.Errorf("lock apply-patch transaction state: %w", err)
	if errors.Is(err, windows.ERROR_INVALID_FUNCTION) ||
		errors.Is(err, windows.ERROR_NOT_SUPPORTED) ||
		errors.Is(err, windows.ERROR_CALL_NOT_IMPLEMENTED) {
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
	if err != nil || !os.SameFile(handleInfo, namedInfo) {
		return errors.New("apply-patch transaction lock changed")
	}
	if err = validateApplyPatchTransactionStatePathEntry(path, namedInfo); err != nil {
		return err
	}
	return validateApplyPatchTransactionPrivateObject(namedInfo, false)
}

func (lock *applyPatchTransactionWindowsLock) fileInfo() (os.FileInfo, error) {
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

func (lock *applyPatchTransactionWindowsLock) Close() error {
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
	var unlockErr error
	if lock.locked {
		unlockErr = windows.UnlockFileEx(
			windows.Handle(file.Fd()),
			0,
			1,
			0,
			&lock.overlapped,
		)
	}
	lock.locked = false
	return errors.Join(unlockErr, file.Close())
}
