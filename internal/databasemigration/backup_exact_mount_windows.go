//go:build windows

package databasemigration

import "os"

func openExactBackupChild(root *os.Root, relative string) (*os.File, error) {
	// Every traversed directory is checked through FILE_FLAG_OPEN_REPARSE_POINT
	// by fileidentity.ExistingWithType. Windows volume mount points and junctions
	// are therefore rejected before descent.
	return openPinnedBackupChild(root, relative)
}

func validateExactBackupOpenedMount(*os.File, *os.File) error {
	return nil
}
