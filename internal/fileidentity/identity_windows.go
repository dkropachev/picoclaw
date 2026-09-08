//go:build windows

package fileidentity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsFileIDInformation struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

type windowsHandleIdentity struct {
	identity   windowsFileIdentity
	attributes uint32
}

// Existing returns a stable volume/128-bit file identity for an existing
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
	firstHandle, firstExists, err := openWindowsIdentityHandle(syscallPath)
	if !firstExists {
		return Identity{}, false, err
	}
	if err != nil {
		return Identity{}, false, err
	}
	defer windows.CloseHandle(firstHandle)
	first, err := inspectWindowsIdentityHandle(firstHandle)
	if err != nil {
		return Identity{}, false, err
	}
	if !windowsIdentityTypeMatches(info, first.attributes) {
		return Identity{}, false, ErrUnsafeType
	}

	after, err := os.Lstat(osPath)
	if err != nil {
		return Identity{}, false, fmt.Errorf("reinspect physical file identity: %w", err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() && !after.IsDir() {
		return Identity{}, false, fmt.Errorf("%w: object changed during inspection", ErrUnsafeType)
	}
	secondHandle, secondExists, err := openWindowsIdentityHandle(syscallPath)
	if err != nil {
		return Identity{}, false, err
	}
	if !secondExists {
		return Identity{}, false, fmt.Errorf("%w: object changed during inspection", ErrUnsafeType)
	}
	defer windows.CloseHandle(secondHandle)
	second, err := inspectWindowsIdentityHandle(secondHandle)
	if err != nil {
		return Identity{}, false, err
	}
	if !windowsIdentityTypeMatches(after, second.attributes) {
		return Identity{}, false, fmt.Errorf("%w: object changed during inspection", ErrUnsafeType)
	}
	stable, err := stableWindowsFileIdentity(first.identity, second.identity)
	if err != nil {
		return Identity{}, false, err
	}
	return stable, true, nil
}

func openWindowsIdentityHandle(path string) (windows.Handle, bool, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, false, ErrInvalidPath
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
		return windows.InvalidHandle, false, nil
	}
	if err != nil {
		return windows.InvalidHandle, false, fmt.Errorf("open physical file identity: %w", err)
	}
	return handle, true, nil
}

func inspectWindowsIdentityHandle(handle windows.Handle) (windowsHandleIdentity, error) {
	fileType, err := windows.GetFileType(handle)
	if err != nil {
		return windowsHandleIdentity{}, fmt.Errorf("inspect physical file identity type: %w", err)
	}
	if fileType != windows.FILE_TYPE_DISK {
		return windowsHandleIdentity{}, ErrUnsafeType
	}

	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return windowsHandleIdentity{}, fmt.Errorf("inspect physical file identity attributes: %w", err)
	}
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
		return windowsHandleIdentity{}, ErrUnsafeType
	}
	var fileID windowsFileIDInformation
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileIdInfo,
		(*byte)(unsafe.Pointer(&fileID)),
		uint32(unsafe.Sizeof(fileID)),
	); err != nil {
		return windowsHandleIdentity{}, fmt.Errorf(
			"%w: inspect 128-bit physical file identity: %v",
			ErrUnsupported,
			err,
		)
	}
	return windowsHandleIdentity{
		identity: windowsFileIdentity{
			volumeSerialNumber: fileID.VolumeSerialNumber,
			fileID:             fileID.FileID,
		},
		attributes: information.FileAttributes,
	}, nil
}

func windowsIdentityTypeMatches(info os.FileInfo, attributes uint32) bool {
	if info == nil || attributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
		return false
	}
	directory := attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	return directory == info.IsDir() && (info.Mode().IsRegular() || info.IsDir())
}

func windowsIdentityPaths(path string) (string, string, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", "", ErrInvalidPath
	}
	volume := filepath.VolumeName(absolute)
	normalized := strings.ReplaceAll(absolute, "/", `\`)
	if strings.HasPrefix(volume, `\\?\`) || strings.HasPrefix(volume, `\\.\`) ||
		strings.HasPrefix(normalized, `\??\`) {
		return "", "", ErrInvalidPath
	}
	if strings.HasPrefix(absolute, `\\`) {
		return absolute, `\\?\UNC\` + strings.TrimPrefix(absolute, `\\`), nil
	}
	return absolute, `\\?\` + absolute, nil
}
