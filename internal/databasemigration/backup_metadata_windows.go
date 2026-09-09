//go:build windows

package databasemigration

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func validateBackupPlatformFile(info os.FileInfo, file *os.File, logicalMode uint32) error {
	if info == nil || file == nil || logicalMode != 0o600 {
		return errors.New("database backup Windows file metadata is invalid")
	}
	var details windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &details); err != nil {
		return err
	}
	if details.NumberOfLinks != 1 ||
		details.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE|
			windows.FILE_ATTRIBUTE_READONLY) != 0 {
		return errors.New("database backup Windows file is linked or reparsed")
	}
	return nil
}

func validateBackupSourceFile(info os.FileInfo, file *os.File) error {
	if info == nil || file == nil {
		return errors.New("database backup Windows source metadata is invalid")
	}
	var details windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &details); err != nil {
		return err
	}
	if details.NumberOfLinks != 1 ||
		details.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
		return errors.New("database backup Windows source is linked or reparsed")
	}
	return nil
}
