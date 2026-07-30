//go:build windows

package api

import (
	"os"

	"golang.org/x/sys/windows"
)

func openAgentDefinitionNoFollow(path string) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	info, statErr := file.Stat()
	if statErr != nil {
		file.Close()
		return nil, statErr
	}
	if info.Mode()&os.ModeSymlink != 0 {
		file.Close()
		return nil, errAgentDefinitionNotRegular
	}
	return file, nil
}

func openAgentCapabilityCatalogDirectory(path string) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	info, statErr := file.Stat()
	if statErr != nil {
		file.Close()
		return nil, statErr
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		file.Close()
		return nil, errAgentDefinitionNotRegular
	}
	return file, nil
}
