//go:build !windows && (!unix || aix)

package databasemigration

import (
	"errors"
	"os"
)

func validateBackupPlatformFile(os.FileInfo, *os.File, uint32) error {
	return errors.New("database backup file metadata is unsupported on this platform")
}

func validateBackupSourceFile(os.FileInfo, *os.File) error {
	return errors.New("database backup source metadata is unsupported on this platform")
}
