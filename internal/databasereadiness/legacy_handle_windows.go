//go:build windows

package databasereadiness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"golang.org/x/sys/windows"
)

func openLegacyNoFollow(
	path string,
	objectType fileidentity.ObjectType,
) (*os.File, bool, error) {
	ancestors, err := openLegacyWindowsAncestors(filepath.Dir(path))
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil, false, nil
		}
		return nil, false, err
	}
	file, exists, openErr := openLegacyWindowsObject(path, objectType)
	closeErr := closeLegacyWindowsAncestors(ancestors)
	if openErr != nil || closeErr != nil {
		if file != nil {
			_ = file.Close()
		}
		return nil, false, errors.Join(openErr, closeErr)
	}
	return file, exists, nil
}

func openLegacyChildNoFollow(
	_ *os.File,
	path string,
	_ string,
	objectType fileidentity.ObjectType,
) (*os.File, bool, error) {
	return openLegacyWindowsObject(path, objectType)
}

func openLegacyWindowsObject(
	path string,
	objectType fileidentity.ObjectType,
) (*os.File, bool, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, errors.Join(errLegacyIntegrity, err)
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT | windows.FILE_FLAG_BACKUP_SEMANTICS)
	desiredAccess := uint32(windows.FILE_READ_ATTRIBUTES)
	if objectType == fileidentity.ObjectTypeDirectory {
		desiredAccess |= windows.FILE_LIST_DIRECTORY
	}
	handle, err := windows.CreateFile(
		name,
		desiredAccess,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		flags,
		0,
	)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, false, errors.New("legacy input handle is unavailable")
	}
	return file, true, nil
}

func openLegacyWindowsAncestors(path string) ([]*os.File, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	volume := filepath.VolumeName(absolute)
	current := volume + string(os.PathSeparator)
	remainder := strings.TrimPrefix(absolute, volume)
	components := strings.FieldsFunc(remainder, func(character rune) bool {
		return character == '/' || character == '\\'
	})
	handles := make([]*os.File, 0, len(components))
	for _, component := range components {
		if !validLegacyComponent(component) {
			return nil, errors.Join(
				errLegacyIntegrity,
				closeLegacyWindowsAncestors(handles),
			)
		}
		current = filepath.Join(current, component)
		handle, exists, openErr := openLegacyWindowsObject(
			current, fileidentity.ObjectTypeDirectory,
		)
		if openErr != nil || !exists {
			if openErr == nil {
				openErr = windows.ERROR_PATH_NOT_FOUND
			}
			return nil, errors.Join(openErr, closeLegacyWindowsAncestors(handles))
		}
		identity, objectType, identityErr := fileidentity.Opened(handle)
		if identityErr != nil || !identity.Valid() || objectType != fileidentity.ObjectTypeDirectory {
			return nil, errors.Join(
				fmt.Errorf("%w: legacy ancestor is unsafe", errLegacyIntegrity),
				classifyLegacyIdentityError(identityErr),
				handle.Close(),
				closeLegacyWindowsAncestors(handles),
			)
		}
		handles = append(handles, handle)
	}
	return handles, nil
}

func closeLegacyWindowsAncestors(handles []*os.File) error {
	var result error
	for index := len(handles) - 1; index >= 0; index-- {
		result = errors.Join(result, handles[index].Close())
	}
	return result
}
