//go:build !linux && !darwin && !dragonfly && !freebsd && !openbsd && !netbsd && !solaris && !windows

package databasemigration

import "os"

func openExactBackupChild(root *os.Root, relative string) (*os.File, error) {
	// Exact archive traversal already fails closed where fileidentity cannot
	// provide stable physical identities. Retain build support for those ports.
	return openPinnedBackupChild(root, relative)
}

func validateExactBackupOpenedMount(*os.File, *os.File) error {
	return errExactBackupMountUnknown
}
