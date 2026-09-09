//go:build unix && !aix

package databasereadiness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func openLegacyNoFollow(
	path string,
	objectType fileidentity.ObjectType,
) (*os.File, bool, error) {
	parent, err := openLegacyUnixDirectory(filepath.Dir(path))
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, false, nil
		}
		return nil, false, err
	}
	file, exists, openErr := openLegacyUnixAt(
		int(parent.Fd()), filepath.Base(path), path, objectType,
	)
	closeErr := parent.Close()
	if openErr != nil || closeErr != nil {
		if file != nil {
			_ = file.Close()
		}
		return nil, false, errors.Join(openErr, closeErr)
	}
	return file, exists, nil
}

func openLegacyChildNoFollow(
	parent *os.File,
	path string,
	name string,
	objectType fileidentity.ObjectType,
) (*os.File, bool, error) {
	if parent == nil {
		return nil, false, fmt.Errorf("%w: legacy parent handle is unavailable", errLegacyIntegrity)
	}
	return openLegacyUnixAt(int(parent.Fd()), name, path, objectType)
}

func openLegacyUnixDirectory(path string) (*os.File, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW |
		unix.O_NONBLOCK | unix.O_DIRECTORY
	fd, err := unix.Open(string(os.PathSeparator), flags, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), string(os.PathSeparator))
	if current == nil {
		_ = unix.Close(fd)
		return nil, errors.New("legacy ancestor handle is unavailable")
	}
	trimmed := strings.TrimPrefix(absolute, string(os.PathSeparator))
	if trimmed == "" {
		return current, nil
	}
	for _, component := range strings.Split(trimmed, string(os.PathSeparator)) {
		if !validLegacyComponent(component) {
			_ = current.Close()
			return nil, fmt.Errorf("%w: legacy ancestor component is invalid", errLegacyIntegrity)
		}
		nextFD, openErr := unix.Openat(int(current.Fd()), component, flags, 0)
		if openErr != nil {
			_ = current.Close()
			if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
				return nil, errors.Join(errLegacyIntegrity, openErr)
			}
			return nil, openErr
		}
		next := os.NewFile(uintptr(nextFD), filepath.Join(current.Name(), component))
		if next == nil {
			_ = unix.Close(nextFD)
			_ = current.Close()
			return nil, errors.New("legacy ancestor handle is unavailable")
		}
		if closeErr := current.Close(); closeErr != nil {
			_ = next.Close()
			return nil, closeErr
		}
		current = next
	}
	return current, nil
}

func openLegacyUnixAt(
	directoryFD int,
	name string,
	displayPath string,
	objectType fileidentity.ObjectType,
) (*os.File, bool, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if objectType == fileidentity.ObjectTypeDirectory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(directoryFD, name, flags, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return nil, false, errors.Join(errLegacyIntegrity, err)
	}
	if err != nil {
		return nil, false, err
	}
	file := os.NewFile(uintptr(fd), displayPath)
	if file == nil {
		_ = unix.Close(fd)
		return nil, false, errors.New("legacy input handle is unavailable")
	}
	return file, true, nil
}
