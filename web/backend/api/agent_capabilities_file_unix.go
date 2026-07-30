//go:build unix

package api

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openAgentDefinitionNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, errAgentDefinitionNotRegular
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openAgentCapabilityCatalogDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_DIRECTORY,
		0,
	)
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return nil, errAgentDefinitionNotRegular
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
