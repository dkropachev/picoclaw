//go:build windows

package fstools

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func snapshotFileIdentity(path string, expected os.FileInfo) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || expected == nil || !os.SameFile(expected, opened) {
		return "", errors.New("file identity changed while opening")
	}
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
