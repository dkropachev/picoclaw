//go:build windows

package fstools

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func fileIdentityFromOpenedHandle(file *os.File, _ os.FileInfo) (string, error) {
	var identity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &identity); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"%x:%x:%x",
		identity.VolumeSerialNumber,
		identity.FileIndexHigh,
		identity.FileIndexLow,
	), nil
}
