//go:build windows

package databasemigration

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func lockBackupStatusHandle(file *os.File) (func() error, error) {
	if file == nil {
		return nil, errors.New("database backup status lock handle is unavailable")
	}
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		overlapped,
	); err != nil {
		return nil, errors.Join(errors.New("database backup migration status is busy"), err)
	}
	return func() error {
		return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
	}, nil
}
