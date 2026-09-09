//go:build unix && !aix

package databasemigration

import (
	"errors"
	"os"
	"syscall"
)

func validateBackupPlatformFile(info os.FileInfo, _ *os.File, logicalMode uint32) error {
	if info == nil || uint32(info.Mode().Perm()) != logicalMode {
		return errors.New("database backup file mode is invalid")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || stat.Nlink != 1 {
		return errors.New("database backup file has an unsafe hard-link count")
	}
	return nil
}

func validateBackupSourceFile(info os.FileInfo, _ *os.File) error {
	if info == nil {
		return errors.New("database backup source metadata is invalid")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("database backup source has unsafe ownership or hard-link count")
	}
	return nil
}
