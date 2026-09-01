//go:build !windows

package gitworkspace

import "os"

func managedDirectoryModePrivate(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode().Perm() == 0o700
}

func managedFileModePrivate(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm() == 0o600
}
