//go:build linux

package databasemigration

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func openExactBackupChild(root *os.Root, relative string) (file *os.File, returnErr error) {
	rootFile, err := openPinnedBackupChild(root, ".")
	if err != nil {
		return nil, fmt.Errorf("open database backup root mount descriptor: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, rootFile.Close())
		if returnErr != nil && file != nil {
			returnErr = errors.Join(returnErr, file.Close())
			file = nil
		}
	}()

	how := &unix.OpenHow{
		Flags: uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS |
			unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	}
	fd, openErr := unix.Openat2(int(rootFile.Fd()), relative, how)
	if openErr == nil {
		file = os.NewFile(uintptr(fd), relative)
		if file == nil {
			_ = unix.Close(fd)
			return nil, errors.New("open database backup child returned an invalid descriptor")
		}
		return file, nil
	}
	if errors.Is(openErr, unix.EXDEV) {
		return nil, errors.Join(errExactBackupMountBoundary, openErr)
	}
	if !linuxOpenat2Unavailable(openErr) {
		return nil, openErr
	}

	// Old kernels and syscall filters can lack openat2. statx mount IDs provide
	// equivalent terminal-mount detection; fdinfo covers pre-STATX_MNT_ID
	// kernels without reducing support to a list of filesystem implementations.
	return openExactBackupChildByMountIdentity(root, relative, linuxExactBackupMountIdentity)
}

func linuxOpenat2Unavailable(err error) bool {
	return errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) ||
		errors.Is(err, unix.E2BIG) || errors.Is(err, unix.EPERM) ||
		errors.Is(err, unix.EACCES)
}

func linuxExactBackupMountIdentity(file *os.File) (exactBackupMountIdentity, error) {
	if file == nil {
		return exactBackupMountIdentity{}, errExactBackupMountUnknown
	}
	var stat unix.Statx_t
	mask := unix.STATX_MNT_ID_UNIQUE | unix.STATX_MNT_ID
	err := unix.Statx(
		int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_NO_AUTOMOUNT,
		mask, &stat,
	)
	if errors.Is(err, unix.EINVAL) {
		stat = unix.Statx_t{}
		err = unix.Statx(
			int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_NO_AUTOMOUNT,
			unix.STATX_MNT_ID, &stat,
		)
	}
	if err == nil {
		switch {
		case stat.Mask&unix.STATX_MNT_ID_UNIQUE != 0:
			if stat.Mnt_id == 0 {
				break
			}
			return exactBackupMountIdentity{
				mechanism: "linux-statx-mount-unique", value: [2]uint64{stat.Mnt_id},
			}, nil
		case stat.Mask&unix.STATX_MNT_ID != 0:
			if stat.Mnt_id == 0 {
				break
			}
			return exactBackupMountIdentity{
				mechanism: "linux-statx-mount", value: [2]uint64{stat.Mnt_id},
			}, nil
		}
	}

	payload, readErr := os.ReadFile("/proc/self/fdinfo/" + strconv.FormatUint(uint64(file.Fd()), 10))
	mountID, parseErr := parseLinuxExactBackupMountID(payload)
	if readErr == nil && parseErr == nil {
		return exactBackupMountIdentity{
			mechanism: "linux-fdinfo-mount", value: [2]uint64{mountID},
		}, nil
	}
	return exactBackupMountIdentity{}, errors.Join(
		errExactBackupMountUnknown, err, readErr, parseErr,
	)
}

func validateExactBackupOpenedMount(root, child *os.File) error {
	rootIdentity, rootErr := linuxExactBackupMountIdentity(root)
	childIdentity, childErr := linuxExactBackupMountIdentity(child)
	if rootErr != nil || childErr != nil {
		return errors.Join(errExactBackupMountUnknown, rootErr, childErr)
	}
	return validateExactBackupMountIdentity(rootIdentity, childIdentity)
}

func parseLinuxExactBackupMountID(payload []byte) (uint64, error) {
	for _, line := range bytes.Split(payload, []byte{'\n'}) {
		fields := bytes.Fields(line)
		if len(fields) != 2 || !bytes.Equal(fields[0], []byte("mnt_id:")) {
			continue
		}
		mountID, err := strconv.ParseUint(string(fields[1]), 10, 64)
		if err != nil || mountID == 0 {
			return 0, errors.Join(errExactBackupMountUnknown, err)
		}
		return mountID, nil
	}
	return 0, fmt.Errorf("%w: fdinfo omits mnt_id", errExactBackupMountUnknown)
}
