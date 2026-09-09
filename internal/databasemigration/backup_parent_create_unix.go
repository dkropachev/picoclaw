//go:build unix && !aix

package databasemigration

import (
	"errors"
	"os"
	"syscall"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func secureBackupParentCreatedDirectoryHandle(
	file *os.File,
	expected fileidentity.Identity,
) error {
	identity, objectType, identityErr := fileidentity.Opened(file)
	info, statErr := file.Stat()
	var stat *syscall.Stat_t
	if info != nil {
		stat, _ = info.Sys().(*syscall.Stat_t)
	}
	if identityErr != nil || statErr != nil || !expected.Valid() || identity != expected ||
		objectType != fileidentity.ObjectTypeDirectory || info == nil || !info.IsDir() ||
		stat == nil || stat.Uid != uint32(os.Geteuid()) {
		return errors.Join(
			errors.New("created database backup parent handle is unsafe"),
			identityErr, statErr,
		)
	}
	if err := file.Chmod(0o700); err != nil {
		return err
	}
	return validateBackupParentCreatedDirectoryHandle(file, expected)
}

func validateBackupParentCreatedDirectoryHandle(
	file *os.File,
	expected fileidentity.Identity,
) error {
	identity, objectType, identityErr := fileidentity.Opened(file)
	info, statErr := file.Stat()
	var stat *syscall.Stat_t
	if info != nil {
		stat, _ = info.Sys().(*syscall.Stat_t)
	}
	if identityErr != nil || statErr != nil || !expected.Valid() || identity != expected ||
		objectType != fileidentity.ObjectTypeDirectory || info == nil || !info.IsDir() ||
		info.Mode().Perm() != 0o700 || stat == nil ||
		stat.Uid != uint32(os.Geteuid()) {
		return errors.Join(
			errors.New("created database backup parent handle is not owner-private"),
			identityErr, statErr,
		)
	}
	return nil
}

func syncBackupParentCreatedDirectoryHandle(
	file *os.File,
	expected fileidentity.Identity,
) error {
	if err := validateBackupParentCreatedDirectoryHandle(file, expected); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return validateBackupParentCreatedDirectoryHandle(file, expected)
}
