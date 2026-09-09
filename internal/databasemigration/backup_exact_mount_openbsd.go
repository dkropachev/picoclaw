//go:build openbsd

package databasemigration

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openExactBackupChild(root *os.Root, relative string) (*os.File, error) {
	return openExactBackupChildByMountIdentity(root, relative, openBSDExactBackupMountIdentity)
}

func openBSDExactBackupMountIdentity(file *os.File) (exactBackupMountIdentity, error) {
	if file == nil {
		return exactBackupMountIdentity{}, errExactBackupMountUnknown
	}
	var stat unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &stat); err != nil {
		return exactBackupMountIdentity{}, fmt.Errorf("%w: %v", errExactBackupMountUnknown, err)
	}
	mountPoint, ok := exactBackupMountName(stat.F_mntonname[:])
	if !ok {
		return exactBackupMountIdentity{}, errExactBackupMountUnknown
	}
	return exactBackupMountIdentity{
		mechanism: "openbsd-fstatfs",
		value: [2]uint64{
			uint64(uint32(stat.F_fsid.Val[0])), uint64(uint32(stat.F_fsid.Val[1])),
		},
		mountPoint: mountPoint,
	}, nil
}

func validateExactBackupOpenedMount(root, child *os.File) error {
	rootIdentity, rootErr := openBSDExactBackupMountIdentity(root)
	childIdentity, childErr := openBSDExactBackupMountIdentity(child)
	if rootErr != nil || childErr != nil {
		return fmt.Errorf("%w: root=%v child=%v", errExactBackupMountUnknown, rootErr, childErr)
	}
	return validateExactBackupMountIdentity(rootIdentity, childIdentity)
}
