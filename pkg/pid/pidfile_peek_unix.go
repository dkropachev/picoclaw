//go:build unix

package pid

import (
	"os"

	"golang.org/x/sys/unix"
)

func openPidFileForPeek(path string) (*os.File, error) {
	return os.OpenFile(
		path,
		os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW,
		0,
	)
}
