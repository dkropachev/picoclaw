//go:build netbsd

package databasemigration

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openExactBackupChild(root *os.Root, relative string) (*os.File, error) {
	return openExactBackupChildByMountIdentity(root, relative, netBSDExactBackupMountIdentity)
}

func netBSDExactBackupMountIdentity(file *os.File) (exactBackupMountIdentity, error) {
	if file == nil {
		return exactBackupMountIdentity{}, errExactBackupMountUnknown
	}
	var stat unix.Statvfs_t
	if err := unix.Fstatvfs(int(file.Fd()), &stat); err != nil {
		return exactBackupMountIdentity{}, fmt.Errorf("%w: %v", errExactBackupMountUnknown, err)
	}
	mountPoint, ok := exactBackupMountName(stat.Mntonname[:])
	if !ok {
		return exactBackupMountIdentity{}, errExactBackupMountUnknown
	}
	return exactBackupMountIdentity{
		mechanism: "netbsd-fstatvfs",
		value: [2]uint64{
			uint64(uint32(stat.Fsidx.X__fsid_val[0])), uint64(uint32(stat.Fsidx.X__fsid_val[1])),
		},
		mountPoint: mountPoint,
	}, nil
}

func validateExactBackupOpenedMount(root, child *os.File) error {
	rootIdentity, rootErr := netBSDExactBackupMountIdentity(root)
	childIdentity, childErr := netBSDExactBackupMountIdentity(child)
	if rootErr != nil || childErr != nil {
		return fmt.Errorf("%w: root=%v child=%v", errExactBackupMountUnknown, rootErr, childErr)
	}
	return validateExactBackupMountIdentity(rootIdentity, childIdentity)
}
