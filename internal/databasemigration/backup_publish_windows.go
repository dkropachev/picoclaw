//go:build windows

package databasemigration

import (
	"errors"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func publishBackupDirectory(source, target string) error {
	source, err := backupWindowsSyscallPath(source)
	if err != nil {
		return err
	}
	target, err = backupWindowsSyscallPath(target)
	if err != nil {
		return err
	}
	sourcePointer, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPointer, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(sourcePointer, targetPointer, windows.MOVEFILE_WRITE_THROUGH)
}

func backupWindowsSyscallPath(path string) (string, error) {
	if !validBackupAbsolutePath(path) {
		return "", errors.New("database backup Windows publication path is invalid")
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	volume := filepath.VolumeName(absolute)
	if strings.HasPrefix(volume, `\\?\`) || strings.HasPrefix(volume, `\\.\`) {
		return "", errors.New("database backup Windows namespace path is invalid")
	}
	if strings.HasPrefix(absolute, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(absolute, `\\`), nil
	}
	return `\\?\` + absolute, nil
}
