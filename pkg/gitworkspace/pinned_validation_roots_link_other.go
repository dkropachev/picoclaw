//go:build !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris && !windows

package gitworkspace

import (
	"io/fs"
	"os"
)

func pinnedValidationFileHasSingleLink(_ *os.File, _ fs.FileInfo) bool {
	return false
}

func pinnedValidationSymlinkHasSingleLink(_ fs.FileInfo) bool {
	return false
}

func openPinnedValidationRegular(_ *os.File, root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY, 0)
}
