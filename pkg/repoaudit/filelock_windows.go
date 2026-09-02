//go:build windows

package repoaudit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func lockRepositoryReviewStore(root string) (func(), error) {
	return lockRepositoryReviewStoreMode(root, false)
}

func tryLockRepositoryReviewStore(root string) (func(), error) {
	return lockRepositoryReviewStoreMode(root, true)
}

func lockRepositoryReviewStoreMode(root string, nonblocking bool) (func(), error) {
	if err := reviewProviderAuthorityError(); err != nil {
		return nil, err
	}
	lockPath, err := repositoryReviewLockPath(root, "store.lock")
	if err != nil {
		return nil, err
	}
	if info, err := os.Lstat(lockPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("repository review lock must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := secureRepositoryReviewLockFile(lockPath, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	var overlapped windows.Overlapped
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if nonblocking {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	if err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		flags,
		0,
		1,
		0,
		&overlapped,
	); err != nil {
		_ = file.Close()
		if nonblocking && errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("lock repository review store: %w", err)
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
		_ = file.Close()
	}, nil
}
