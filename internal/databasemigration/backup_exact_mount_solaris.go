//go:build solaris

package databasemigration

import (
	"fmt"
	"os"
)

func openExactBackupChild(*os.Root, string) (*os.File, error) {
	// Solaris/illumos LOFS copies the underlying vfs_dev and vfs_fsid and
	// delegates statvfs to that filesystem. No descriptor-bound per-mount ID is
	// exposed, so fsid comparison would accept same-filesystem loopback mounts.
	return nil, fmt.Errorf("%w: Solaris has no stable per-mount descriptor identity", errExactBackupMountUnknown)
}

func validateExactBackupOpenedMount(*os.File, *os.File) error {
	return errExactBackupMountUnknown
}
