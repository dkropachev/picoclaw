//go:build !linux && !android && !darwin && !windows

package databasemigration

import "errors"

func exchangeBackupStatusFile(string, string) (string, error) {
	return "", errors.New("atomic database backup status exchange is unsupported")
}

func rollbackBackupStatusFile(string, string, string) error {
	return errors.New("atomic database backup status rollback is unsupported")
}
