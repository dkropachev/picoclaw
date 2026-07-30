//go:build unix

package agent

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openAgentDefinitionFileNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, ErrAgentDefinitionNotRegular
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
