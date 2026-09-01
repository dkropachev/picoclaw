//go:build windows

package gitworkspace

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

const inventoryFileLockRetryInterval = 50 * time.Millisecond

func lockInventoryFile(ctx context.Context, path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("git workspace inventory lock is a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	var overlapped windows.Overlapped
	for {
		err = windows.LockFileEx(
			windows.Handle(file.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			&overlapped,
		)
		if err == nil {
			var once sync.Once
			return func() {
				once.Do(func() {
					_ = windows.UnlockFileEx(
						windows.Handle(file.Fd()),
						0,
						1,
						0,
						&overlapped,
					)
					_ = file.Close()
				})
			}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = file.Close()
			return nil, fmt.Errorf("acquire advisory file lock: %w", err)
		}
		timer := time.NewTimer(inventoryFileLockRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func lockInventoryFileInDirectory(
	ctx context.Context,
	directory,
	filename string,
	expected os.FileInfo,
) (func(), error) {
	if filename == "" || filepath.Base(filename) != filename || filename == "." || filename == ".." {
		return nil, errors.New("git workspace operation lock filename is invalid")
	}
	if err := validatePrivateManagedDirectory(
		directory,
		expected,
		"git workspace operation lock root",
	); err != nil {
		return nil, err
	}
	unlock, err := lockInventoryFile(ctx, filepath.Join(directory, filename))
	if err != nil {
		return nil, err
	}
	if err := validatePrivateManagedDirectory(
		directory,
		expected,
		"git workspace operation lock root",
	); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}
