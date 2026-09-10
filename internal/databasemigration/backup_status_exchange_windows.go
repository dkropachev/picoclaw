//go:build windows

package databasemigration

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const replaceFileWriteThrough = 0x00000001

var replaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

func exchangeBackupStatusFile(source, target string) (string, error) {
	if err := validateBackupStatusExchangePaths(source, target); err != nil {
		return "", err
	}
	placeholder, err := os.CreateTemp(
		filepath.Dir(source), ".database-migration-status-displaced-*.partial",
	)
	if err != nil {
		return "", err
	}
	displaced := placeholder.Name()
	if removeErr := removeBackupStatusPlaceholderWithOps(
		displaced, placeholder, defaultBackupStatusPlaceholderOps(),
	); removeErr != nil {
		return "", removeErr
	}
	if err := replaceBackupStatusWindows(target, source, displaced); err != nil {
		return "", err
	}
	return displaced, nil
}

func rollbackBackupStatusFile(source, target, displaced string) error {
	if err := validateBackupStatusExchangePaths(displaced, target); err != nil {
		return err
	}
	if filepath.Dir(source) != filepath.Dir(target) || source == displaced {
		return errors.New("database backup status rollback path is invalid")
	}
	return replaceBackupStatusWindows(target, displaced, source)
}

func replaceBackupStatusWindows(target, replacement, backup string) error {
	target, err := backupWindowsSyscallPath(target)
	if err != nil {
		return err
	}
	replacement, err = backupWindowsSyscallPath(replacement)
	if err != nil {
		return err
	}
	backup, err = backupWindowsSyscallPath(backup)
	if err != nil {
		return err
	}
	targetPointer, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	replacementPointer, err := windows.UTF16PtrFromString(replacement)
	if err != nil {
		return err
	}
	backupPointer, err := windows.UTF16PtrFromString(backup)
	if err != nil {
		return err
	}
	result, _, callErr := replaceFileW.Call(
		uintptr(unsafe.Pointer(targetPointer)),
		uintptr(unsafe.Pointer(replacementPointer)),
		uintptr(unsafe.Pointer(backupPointer)),
		replaceFileWriteThrough,
		0,
		0,
	)
	if result == 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return callErr
		}
		return syscall.EINVAL
	}
	return nil
}

func validateBackupStatusExchangePaths(source, target string) error {
	if !validBackupAbsolutePath(source) || !validBackupAbsolutePath(target) ||
		filepath.Dir(source) != filepath.Dir(target) || source == target {
		return errors.New("database backup status exchange paths are invalid")
	}
	return nil
}
