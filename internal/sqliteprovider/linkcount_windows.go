//go:build windows

package sqliteprovider

import (
	"os"

	"golang.org/x/sys/windows"
)

func classifyGenerationLinkCount(path string, expected os.FileInfo) generationLinkClass {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return generationLinkUnavailable
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return generationLinkUnavailable
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return generationLinkUnavailable
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || expected == nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return generationLinkUnavailable
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return generationLinkUnavailable
	}
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return generationLinkUnsafe
	}
	switch information.NumberOfLinks {
	case 0:
		return generationLinkZero
	case 1:
		return generationLinkSingle
	default:
		return generationLinkMultiple
	}
}
