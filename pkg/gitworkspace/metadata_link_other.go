//go:build !android && !darwin && !dragonfly && !freebsd && !ios && !linux && !netbsd && !openbsd && !solaris && !windows

package gitworkspace

import "io/fs"

func pinnedMetadataFileHasSingleLink(_ string, _ fs.FileInfo) bool {
	return false
}
