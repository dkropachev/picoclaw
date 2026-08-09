//go:build windows

package gitworkspace

import (
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func pinnedValidationFileHasSingleLink(file *os.File, _ fs.FileInfo) bool {
	if file == nil {
		return false
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(
		windows.Handle(file.Fd()),
		&information,
	); err != nil {
		return false
	}
	return information.NumberOfLinks == 1
}

// Windows does not expose a no-follow link count through os.FileInfo. Named
// symlink identity is still checked before and after Readlink, and Windows
// hard-link creation does not follow a symbolic-link reparse point.
func pinnedValidationSymlinkHasSingleLink(_ fs.FileInfo) bool {
	return true
}

func openPinnedValidationRegular(
	directory *os.File,
	_ *os.Root,
	name string,
) (*os.File, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		RootDirectory: windows.Handle(directory.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE,
	}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	var status windows.IO_STATUS_BLOCK
	var handle windows.Handle
	err = windows.NtCreateFile(
		&handle,
		windows.FILE_GENERIC_READ,
		attributes,
		&status,
		nil,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|
			windows.FILE_OPEN_REPARSE_POINT|
			windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), name)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, windows.ERROR_INVALID_HANDLE
	}
	return file, nil
}
