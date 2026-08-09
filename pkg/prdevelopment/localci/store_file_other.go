//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package localci

import (
	"io/fs"
	"os"
)

func privateEvidenceMode(info fs.FileInfo) bool {
	return info != nil
}

func privateEvidenceFile(info fs.FileInfo) bool {
	return info != nil && info.Mode().IsRegular()
}

func openEvidenceRegularFile(root *os.Root, relative string) (*os.File, error) {
	return root.Open(relative)
}
