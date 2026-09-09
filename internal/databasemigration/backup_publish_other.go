//go:build !linux && !android && !darwin && !windows

package databasemigration

import (
	"errors"
)

func publishBackupDirectory(string, string) error {
	return errors.New("atomic no-replace database backup publication is unsupported")
}
