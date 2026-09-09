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
	identity, _, exists, err = ExistingWithType(path)
	return identity, exists, err
}

// ExistingWithType returns a stable full-width identity and the type checked
// against the same identity-bearing Windows handles.
func ExistingWithType(path string) (identity Identity, objectType ObjectType, exists bool, err error) {
	if !validPath(path) {
		return Identity{}, 0, false, ErrInvalidPath
	}
	osPath, syscallPath, err := windowsIdentityPaths(path)
	if err != nil {
		return Identity{}, 0, false, err
	}
	info, err := os.Lstat(osPath)
	if errors.Is(err, os.ErrNotExist) {
		return Identity{}, 0, false, nil
	}
	if err != nil {
		return Identity{}, 0, false, fmt.Errorf("inspect physical file identity: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() && !info.IsDir() {
		return Identity{}, 0, false, ErrUnsafeType
	}
	firstHandle, firstExists, err := openWindowsIdentityHandle(syscallPath)
	if !firstExists {
		return Identity{}, 0, false, err
	}
	if err != nil {
		return Identity{}, 0, false, err
	}
	defer windows.CloseHandle(firstHandle)
	first, err := inspectWindowsIdentityHandle(firstHandle)
	if err != nil {
		return Identity{}, 0, false, err
	}
	if !windowsIdentityTypeMatches(info, first.attributes) {
		return Identity{}, 0, false, ErrUnsafeType
	}

	after, err := os.Lstat(osPath)
	if err != nil {
		return Identity{}, 0, false, fmt.Errorf("reinspect physical file identity: %w", err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() && !after.IsDir() {
		return Identity{}, 0, false, fmt.Errorf("%w: object changed during inspection", ErrUnsafeType)
	}
	secondHandle, secondExists, err := openWindowsIdentityHandle(syscallPath)
	if err != nil {
		return Identity{}, 0, false, err
	}
	if !secondExists {
		return Identity{}, 0, false, fmt.Errorf("%w: object changed during inspection", ErrUnsafeType)
	}
	defer windows.CloseHandle(secondHandle)
	second, err := inspectWindowsIdentityHandle(secondHandle)
	if err != nil {
		return Identity{}, 0, false, err
	}
	if !windowsIdentityTypeMatches(after, second.attributes) {
		return Identity{}, 0, false, fmt.Errorf("%w: object changed during inspection", ErrUnsafeType)
	}
	stable, err := stableWindowsFileIdentity(first.identity, second.identity)
	if err != nil {
		return Identity{}, 0, false, err
	}
	objectType = ObjectTypeRegular
	if after.IsDir() {
		objectType = ObjectTypeDirectory
	}
	return stable, objectType, true, nil
}

// Opened resolves a full-width identity and type from an already-open handle.
func Opened(file *os.File) (Identity, ObjectType, error) {
	if file == nil {
		return Identity{}, 0, ErrInvalidPath
	}
	inspected, err := inspectWindowsIdentityHandle(windows.Handle(file.Fd()))
	if err != nil {
		return Identity{}, 0, err
	}
	identity, err := stableWindowsFileIdentity(inspected.identity, inspected.identity)
	if err != nil {
		return Identity{}, 0, err
	}
	objectType := ObjectTypeRegular
	if inspected.attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		objectType = ObjectTypeDirectory
	}
	return identity, objectType, nil
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
