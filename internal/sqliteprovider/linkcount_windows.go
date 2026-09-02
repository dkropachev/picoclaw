//go:build windows

package sqliteprovider

import (
	"os"

	"golang.org/x/sys/windows"
)

var (
	windowsOpenGenerationMetadata = windows.CreateFile
	windowsGenerationInformation  = windows.GetFileInformationByHandle
)

func generationHasSingleLink(path string, expected os.FileInfo) bool {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	handle, err := windowsOpenGenerationMetadata(
		pathPointer,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return false
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return false
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || expected == nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return false
	}
	var information windows.ByHandleFileInformation
	if err := windowsGenerationInformation(handle, &information); err != nil {
		return false
	}
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return false
	}
	return information.NumberOfLinks == 1
}
