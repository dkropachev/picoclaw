//go:build unix && !aix

package databasemigration

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockBackupStatusHandle(file *os.File) (func() error, error) {
	if file == nil {
		return nil, errors.New("database backup status lock handle is unavailable")
	}
	fd := int(file.Fd())
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, errors.Join(errors.New("database backup migration status is busy"), err)
	}
	return func() error { return unix.Flock(fd, unix.LOCK_UN) }, nil
}
