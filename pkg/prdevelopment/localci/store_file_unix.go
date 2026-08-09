//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package localci

import (
	"io/fs"
	"os"
	"syscall"
)

func privateEvidenceMode(info fs.FileInfo) bool {
	return info != nil && info.Mode().Perm()&0o077 == 0
}

func privateEvidenceFile(info fs.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || !privateEvidenceMode(info) {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}

func openEvidenceRegularFile(root *os.Root, relative string) (*os.File, error) {
	return root.OpenFile(relative, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
