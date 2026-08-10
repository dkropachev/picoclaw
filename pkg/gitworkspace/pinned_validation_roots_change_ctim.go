//go:build aix || android || dragonfly || illumos || linux || openbsd || solaris

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
		seconds: pinnedValidationTimePart(stat.Ctim.Sec),
		nanos:   pinnedValidationTimePart(stat.Ctim.Nsec),
		valid:   true,
	}
}
