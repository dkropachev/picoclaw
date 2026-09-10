//go:build unix

package sqliteprovider

import (
	"os"
	"syscall"
)

func classifyGenerationLinkCount(_ string, info os.FileInfo) generationLinkClass {
	if info == nil {
		return generationLinkUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return generationLinkUnavailable
	}
	switch stat.Nlink {
	case 0:
		return generationLinkZero
	case 1:
		return generationLinkSingle
	default:
		return generationLinkMultiple
	}
}

func classifyGenerationOwner(_ string, info os.FileInfo) generationOwnerClass {
	if info == nil {
		return generationOwnerUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return generationOwnerUnavailable
	}
	if stat.Uid == uint32(os.Geteuid()) {
		return generationOwnerCurrent
	}
	return generationOwnerForeign
}
