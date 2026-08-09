//go:build darwin || freebsd || ios || netbsd

package gitworkspace

import (
	"io/fs"
	"syscall"
)

func pinnedValidationNodeChangeToken(info fs.FileInfo) pinnedValidationChangeToken {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return pinnedValidationChangeToken{}
	}
	return pinnedValidationChangeToken{
		seconds: int64(stat.Ctimespec.Sec),
		nanos:   int64(stat.Ctimespec.Nsec),
		valid:   true,
	}
}
