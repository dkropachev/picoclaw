//go:build windows

package gitworkspace

import "os"

// Windows os.FileMode permission bits do not reflect ACL privacy: writable
// directories and files normally report 0777 and 0666. Canonical handle and
// identity validation provide the supported Windows boundary.
func managedDirectoryModePrivate(info os.FileInfo) bool {
	return info != nil && info.IsDir()
}

func managedFileModePrivate(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular()
}
