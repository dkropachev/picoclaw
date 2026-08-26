//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package tools

import (
	"fmt"
	"os"
)

func applyPatchLinkCount(_ *os.File, _ os.FileInfo) (uint64, error) {
	return 0, fmt.Errorf("file link metadata is unsupported on this platform")
}
