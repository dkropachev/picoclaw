//go:build windows

package gitworkspace

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

func pinnedMetadataFileHasSingleLink(path string, _ fs.FileInfo) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(
		windows.Handle(file.Fd()),
		&information,
	); err != nil {
		return false
	}
	return information.NumberOfLinks == 1
}
