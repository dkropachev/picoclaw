//go:build unix

package sqliteprovider

import (
	"os"
	"syscall"
)

func generationHasSingleLink(_ string, info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return !ok || stat.Nlink == 1
}

func generationOwnedByCurrentUser(_ string, info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return !ok || stat.Uid == uint32(os.Geteuid())
}
