//go:build windows

package tools

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func applyPatchLinkCount(file *os.File, _ os.FileInfo) (uint64, error) {
	if file == nil {
		return 0, fmt.Errorf("file handle is unavailable")
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &information); err != nil {
		return 0, err
	}
	if information.NumberOfLinks == 0 {
		return 0, fmt.Errorf("file link metadata is invalid")
	}
	return uint64(information.NumberOfLinks), nil
}
