//go:build !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris

package gitworkspace

import "io/fs"

func pinnedValidationNodeChangeToken(fs.FileInfo) pinnedValidationChangeToken {
	return pinnedValidationChangeToken{}
}
