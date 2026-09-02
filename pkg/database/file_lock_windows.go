//go:build windows

package database

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/windows"
)

func acquirePlatformFileLock(path string, shared bool) (*os.File, error) {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, NewError(CodeIntegrity, "database storage lock cannot be a symlink")
		}
		if err := validateOwnerOnlyFile(path, info, 0o600); err != nil {
			return nil, err
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect database storage lock: %w", err)
	}
	file, err := openOwnerOnlyLockFile(path)
	if err != nil {
		return nil, fmt.Errorf("open database storage lock: %w", err)
	}
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if !shared {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, overlapped); err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, errFileLockBusy
		}
		return nil, fmt.Errorf("lock database storage root: %w", err)
	}
	platformLockOverlapped.Store(file, overlapped)
	return file, nil
}

var platformLockOverlapped lockOverlappedRegistry

type lockOverlappedRegistry struct {
	mu     sync.Mutex
	values map[*os.File]*windows.Overlapped
}

func (registry *lockOverlappedRegistry) Store(file *os.File, overlapped *windows.Overlapped) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.values == nil {
		registry.values = make(map[*os.File]*windows.Overlapped)
	}
	registry.values[file] = overlapped
}

func (registry *lockOverlappedRegistry) Take(file *os.File) *windows.Overlapped {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	overlapped := registry.values[file]
	delete(registry.values, file)
	return overlapped
}

func releasePlatformFileLock(file *os.File) error {
	if file == nil {
		return nil
	}
	overlapped := platformLockOverlapped.Take(file)
	var unlockErr error
	if overlapped != nil {
		unlockErr = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
	}
	return errors.Join(unlockErr, file.Close())
}
