//go:build windows

package repoaudit

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func (s Store) LockAutomationController() (func(), error) {
	lockPath, err := repositoryReviewLockPath(s.root, "controller.lock")
	if err != nil {
		return nil, err
	}
	if info, err := os.Lstat(lockPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("repository review controller lock must be a regular file")
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
	overlapped := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, overlapped)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrAutomationControllerLocked
		}
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
		_ = file.Close()
	}, nil
}
