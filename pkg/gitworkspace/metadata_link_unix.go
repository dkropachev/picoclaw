//go:build android || darwin || dragonfly || freebsd || ios || linux || netbsd || openbsd || solaris

package gitworkspace

import (
	"io/fs"
	"syscall"
)

func pinnedMetadataFileHasSingleLink(_ string, info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}
