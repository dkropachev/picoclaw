//go:build windows

package databasemigration

import (
	"errors"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var backupRemovalReOpenFile = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

type backupRemovalRenameInformation struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

type backupRemovalDispositionInformation struct {
	Flags uint32
}

func renameBackupRemovalRootNoReplace(
	root *os.Root,
	_ string,
	target string,
	opened *os.File,
) (*os.File, error) {
	if opened == nil {
		return nil, errors.New("database backup removal source handle is unavailable")
	}
	parent, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	handle, err := reopenBackupRemovalHandle(windows.Handle(opened.Fd()))
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16FromString(target)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	nameBytes := (len(name) - 1) * 2
	var layout backupRemovalRenameInformation
	buffer := make([]byte, int(unsafe.Offsetof(layout.FileName))+nameBytes)
	info := (*backupRemovalRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.RootDirectory = windows.Handle(parent.Fd())
	info.FileNameLength = uint32(nameBytes)
	copy(
		unsafe.Slice(&info.FileName[0], nameBytes/2),
		name[:len(name)-1],
	)
	var status windows.IO_STATUS_BLOCK
	if err := windows.NtSetInformationFile(
		handle, &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation,
	); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return os.NewFile(uintptr(handle), target), nil
}

func removeBackupRemovalRootEntry(
	_ *os.Root,
	_ string,
	exact *os.File,
	_ bool,
) error {
	if exact == nil {
		return errors.New("database backup removal exact handle is unavailable")
	}
	info := backupRemovalDispositionInformation{Flags: windows.FILE_DISPOSITION_DELETE |
		windows.FILE_DISPOSITION_POSIX_SEMANTICS |
		windows.FILE_DISPOSITION_FORCE_IMAGE_SECTION_CHECK |
		windows.FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE}
	return windows.SetFileInformationByHandle(
		windows.Handle(exact.Fd()), windows.FileDispositionInfoEx,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)),
	)
}

// The exact source handle is reopened with write-through semantics before its
// rename. Later tombstone disposition is physical housekeeping after that
// durable logical removal.
func syncBackupRemovalRoot(*os.Root) error { return nil }

func reopenBackupRemovalHandle(original windows.Handle) (windows.Handle, error) {
	result, _, callErr := backupRemovalReOpenFile.Call(
		uintptr(original),
		uintptr(windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE),
		uintptr(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE),
		uintptr(windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS|
			windows.FILE_FLAG_WRITE_THROUGH),
	)
	handle := windows.Handle(result)
	if handle == windows.InvalidHandle {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return windows.InvalidHandle, callErr
		}
		return windows.InvalidHandle, syscall.EINVAL
	}
	return handle, nil
}
