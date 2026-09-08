//go:build windows

package databaseclaims

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/pkg/database"
)

type windowsReplacementHandle struct {
	file     *os.File
	identity fileidentity.Identity
}

func openReplacementHandle(path string) (replacementHandle, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open physical database replacement stage: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, database.NewError(database.CodeUnavailable, "replacement stage handle is unavailable")
	}
	identity, objectType, identityErr := fileidentity.Opened(file)
	if identityErr != nil || objectType != fileidentity.ObjectTypeRegular {
		_ = file.Close()
		return nil, database.NewError(database.CodeIntegrity, "replacement stage handle identity is unsafe")
	}
	result := &windowsReplacementHandle{file: file, identity: identity}
	if !result.valid() {
		_ = file.Close()
		return nil, database.NewError(database.CodeIntegrity, "replacement stage handle is unsafe")
	}
	return result, nil
}

func (handle *windowsReplacementHandle) matches(identity fileidentity.Identity) bool {
	if handle == nil || !handle.valid() || !identity.Valid() || handle.identity != identity {
		return false
	}
	opened, objectType, err := fileidentity.Opened(handle.file)
	return err == nil && objectType == fileidentity.ObjectTypeRegular && opened == identity
}

func (handle *windowsReplacementHandle) valid() bool {
	if handle == nil || handle.file == nil {
		return false
	}
	var basic windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(handle.file.Fd()), &basic); err != nil ||
		basic.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
		return false
	}
	var tag windowsFileAttributeTagInformation
	return windows.GetFileInformationByHandleEx(
		windows.Handle(handle.file.Fd()), windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&tag)), uint32(unsafe.Sizeof(tag)),
	) == nil && tag.ReparseTag == 0 && windowsClaimHandleHasSingleLink(windows.Handle(handle.file.Fd()))
}

func (handle *windowsReplacementHandle) close() error {
	if handle == nil || handle.file == nil {
		return nil
	}
	file := handle.file
	handle.file = nil
	return file.Close()
}
