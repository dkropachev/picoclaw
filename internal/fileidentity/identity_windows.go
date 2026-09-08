//go:build windows

package fileidentity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Existing returns a stable volume/file-index identity for an existing
// regular file or directory. Reparse points are always rejected.
func Existing(path string) (identity Identity, exists bool, err error) {
	if !validPath(path) {
		return Identity{}, false, ErrInvalidPath
	}
	osPath, syscallPath, err := windowsIdentityPaths(path)
	if err != nil {
		return Identity{}, false, err
	}
	info, err := os.Lstat(osPath)
	if errors.Is(err, os.ErrNotExist) {
		return Identity{}, false, nil
	}
	if err != nil {
		return Identity{}, false, fmt.Errorf("inspect physical file identity: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() && !info.IsDir() {
		return Identity{}, false, ErrUnsafeType
	}
	pathPointer, err := windows.UTF16PtrFromString(syscallPath)
	if err != nil {
		return Identity{}, false, ErrInvalidPath
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return Identity{}, false, nil
	}
	if err != nil {
		return Identity{}, false, fmt.Errorf("open physical file identity: %w", err)
	}
	file := os.NewFile(uintptr(handle), osPath)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return Identity{}, false, errors.New("open physical file identity returned no file")
	}
	defer file.Close()
	fileType, err := windows.GetFileType(handle)
	if err != nil {
		return Identity{}, false, fmt.Errorf("inspect physical file identity type: %w", err)
	}
	if fileType != windows.FILE_TYPE_DISK {
		return Identity{}, false, ErrUnsafeType
	}

	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return Identity{}, false, fmt.Errorf("inspect physical file identity handle: %w", err)
	}
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
		return Identity{}, false, ErrUnsafeType
	}
	if directory := information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0; directory != info.IsDir() {
		return Identity{}, false, ErrUnsafeType
	}
	opened, err := file.Stat()
	if err != nil {
		return Identity{}, false, fmt.Errorf("inspect opened physical file identity: %w", err)
	}
	after, err := os.Lstat(osPath)
	if err != nil {
		return Identity{}, false, fmt.Errorf("reinspect physical file identity: %w", err)
	}
	if !os.SameFile(info, opened) || !os.SameFile(opened, after) ||
		after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() && !after.IsDir() {
		return Identity{}, false, fmt.Errorf("%w: object changed during inspection", ErrUnsafeType)
	}
	return newIdentity(fmt.Sprintf(
		"windows:%x:%x:%x",
		information.VolumeSerialNumber,
		information.FileIndexHigh,
		information.FileIndexLow,
	)), true, nil
}

func windowsIdentityPaths(path string) (string, string, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", "", ErrInvalidPath
	}
	volume := filepath.VolumeName(absolute)
	if strings.HasPrefix(volume, `\\?\`) || strings.HasPrefix(volume, `\\.\`) {
		return "", "", ErrInvalidPath
	}
	if strings.HasPrefix(absolute, `\\`) {
		return absolute, `\\?\UNC\` + strings.TrimPrefix(absolute, `\\`), nil
	}
	return absolute, `\\?\` + absolute, nil
}
