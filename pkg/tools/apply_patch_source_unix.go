//go:build unix

package tools

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openApplyPatchSource(path string) (*os.File, error) {
	fd, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("create apply-patch source handle")
	}
	return file, nil
}
