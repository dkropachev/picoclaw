//go:build unix && !aix

package sqliteprovider

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func providerOpenFile(path string, flag int, mode os.FileMode) (providerFile, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(absolute)
	components := strings.Split(strings.TrimPrefix(parent, string(os.PathSeparator)), string(os.PathSeparator))
	parentFD, err := unix.Open(
		string(os.PathSeparator), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(parentFD) }()
	for _, component := range components {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(
			parentFD, component,
			unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
			0,
		)
		if openErr != nil {
			return nil, openErr
		}
		_ = unix.Close(parentFD)
		parentFD = next
	}
	fd, err := unix.Openat(
		parentFD,
		filepath.Base(absolute),
		flag|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		uint32(mode.Perm()),
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("SQLite provider file handle is unavailable")
	}
	return file, nil
}
