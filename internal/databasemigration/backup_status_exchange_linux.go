//go:build linux || android

package databasemigration

import (
	"errors"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func exchangeBackupStatusFile(source, target string) (string, error) {
	if err := validateBackupStatusExchangePaths(source, target); err != nil {
		return "", err
	}
	if err := unix.Renameat2(
		unix.AT_FDCWD, source, unix.AT_FDCWD, target, unix.RENAME_EXCHANGE,
	); err != nil {
		return "", err
	}
	return source, nil
}

func rollbackBackupStatusFile(source, target, displaced string) error {
	if displaced != source {
		return errors.New("database backup status rollback path is invalid")
	}
	if err := validateBackupStatusExchangePaths(source, target); err != nil {
		return err
	}
	return unix.Renameat2(
		unix.AT_FDCWD, source, unix.AT_FDCWD, target, unix.RENAME_EXCHANGE,
	)
}

func validateBackupStatusExchangePaths(source, target string) error {
	if !validBackupAbsolutePath(source) || !validBackupAbsolutePath(target) ||
		filepath.Dir(source) != filepath.Dir(target) || source == target {
		return errors.New("database backup status exchange paths are invalid")
	}
	return nil
}
