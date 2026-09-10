//go:build unix

package sqliteprovider

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func validateProviderPathSyntax(string) error { return nil }

func validateProviderAncestors(path string) error {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	for ancestor := filepath.Dir(absolute); ; ancestor = filepath.Dir(ancestor) {
		info, statErr := os.Lstat(ancestor)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return errors.Join(errProviderUnsafeBoundary, errors.New("SQLite provider ancestor is unsafe"))
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return nil
		}
	}
}

func secureProviderDirectory(path string) error { return secureUnixProviderPath(path, true) }

func validateProviderLiveFileInfo(info os.FileInfo) error {
	return validateUnixProviderLiveInfo(info, uint32(os.Geteuid()))
}

// secureProviderFile validates and narrows a live SQLite generation member by
// pathname without opening it. Closing any descriptor for an inode can release
// all traditional POSIX record locks that this process holds for that inode,
// including descriptors owned by SQLite; chmod does not acquire such a
// descriptor.
func secureProviderFile(path string) error {
	expected, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return secureUnixProviderLiveFile(
		path, expected, os.Lstat, hardenUnixProviderLiveFileMode, uint32(os.Geteuid()),
	)
}

func secureUnixProviderLiveFile(
	path string,
	expected os.FileInfo,
	lstat func(string) (os.FileInfo, error),
	chmod func(string, os.FileInfo, os.FileMode) error,
	effectiveUID uint32,
) error {
	if lstat == nil || chmod == nil {
		return errors.New("SQLite provider live-file security operations are unavailable")
	}
	if err := validateUnixProviderLiveIdentity(expected, effectiveUID); err != nil {
		return err
	}
	const permissionBits = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if expected.Mode()&permissionBits != 0o600 {
		if err := validateUnixProviderHardenableMode(expected.Mode()); err != nil {
			return err
		}
		if err := chmod(path, expected, 0o600); err != nil {
			return err
		}
	}
	current, err := lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(expected, current) {
		return errProviderGenerationTransition
	}
	if err := validateUnixProviderLiveInfo(current, effectiveUID); err != nil {
		return err
	}
	return nil
}

// hardenUnixProviderLiveFileMode retains and secures the member's parent, then
// applies chmod relative to that directory. It never obtains a descriptor for
// the SQLite inode. AT_SYMLINK_NOFOLLOW is preferred; where the platform,
// kernel, or syscall policy lacks no-follow chmod, the protected owner-only
// parent makes the flags-free fallback safe from other-user entry replacement.
func hardenUnixProviderLiveFileMode(
	path string,
	expected os.FileInfo,
	mode os.FileMode,
) error {
	parent := filepath.Dir(path)
	parentExpected, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	fd, err := unix.Open(
		parent,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return err
	}
	parentFile := os.NewFile(uintptr(fd), parent)
	defer parentFile.Close()
	if err := secureUnixProviderHandle(
		parent, true, parentExpected, parentFile, uint32(os.Geteuid()),
	); err != nil {
		return err
	}

	name := filepath.Base(path)
	var before unix.Stat_t
	if err := unix.Fstatat(fd, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !sameUnixProviderFileInfoAndStat(expected, &before) {
		return errProviderGenerationTransition
	}
	if err := validateUnixProviderLiveStat(&before, uint32(os.Geteuid()), false); err != nil {
		return err
	}
	if err := validateUnixProviderHardenableStatMode(uint64(before.Mode)); err != nil {
		return err
	}
	if err := chmodUnixProviderAt(
		fd, name, uint32(mode.Perm()), parentExpected, expected, uint32(os.Geteuid()),
	); err != nil {
		return err
	}
	return validateSecuredUnixProviderLiveFile(
		fd, name, expected, uint32(os.Geteuid()),
	)
}

func validateSecuredUnixProviderLiveFile(
	parentFD int,
	name string,
	expected os.FileInfo,
	effectiveUID uint32,
) error {
	return validateSecuredUnixProviderLiveFileWithStat(
		expected,
		effectiveUID,
		func(stat *unix.Stat_t) error {
			return unix.Fstatat(parentFD, name, stat, unix.AT_SYMLINK_NOFOLLOW)
		},
	)
}

func validateSecuredUnixProviderLiveFileWithStat(
	expected os.FileInfo,
	effectiveUID uint32,
	fstatat func(*unix.Stat_t) error,
) error {
	if fstatat == nil {
		return errors.New("SQLite provider secured live-file inspection is unavailable")
	}
	var secured unix.Stat_t
	if err := fstatat(&secured); err != nil {
		return err
	}
	if !sameUnixProviderFileInfoAndStat(expected, &secured) {
		return errProviderGenerationTransition
	}
	return validateUnixProviderLiveStat(&secured, effectiveUID, true)
}

func chmodUnixProviderAt(
	parentFD int,
	name string,
	mode uint32,
	parentExpected os.FileInfo,
	expected os.FileInfo,
	effectiveUID uint32,
) error {
	return chmodUnixProviderAtWithOps(
		parentFD,
		name,
		mode,
		parentExpected,
		expected,
		effectiveUID,
		unixProviderAtOps{
			fchmodat: unix.Fchmodat,
			fstat:    unix.Fstat,
			fstatat:  unix.Fstatat,
		},
	)
}

type unixProviderAtOps struct {
	fchmodat func(int, string, uint32, int) error
	fstat    func(int, *unix.Stat_t) error
	fstatat  func(int, string, *unix.Stat_t, int) error
}

func chmodUnixProviderAtWithOps(
	parentFD int,
	name string,
	mode uint32,
	parentExpected os.FileInfo,
	expected os.FileInfo,
	effectiveUID uint32,
	ops unixProviderAtOps,
) error {
	if ops.fchmodat == nil || ops.fstat == nil || ops.fstatat == nil {
		return errors.New("SQLite provider relative chmod operations are unavailable")
	}
	err := ops.fchmodat(parentFD, name, mode, unix.AT_SYMLINK_NOFOLLOW)
	if !unixProviderNoFollowChmodFallback(err) {
		return err
	}

	// A no-follow chmod syscall is unavailable through this platform, kernel, or
	// syscall policy. Before using flags-free fchmodat, re-prove both the retained
	// parent and the exact child. The current-user 0700 parent excludes entry
	// replacement by another user; same-user interference is outside this
	// advisory boundary.
	var parentStat unix.Stat_t
	if err := ops.fstat(parentFD, &parentStat); err != nil {
		return err
	}
	if err := validateUnixProviderParentStat(&parentStat, effectiveUID); err != nil {
		return err
	}
	if !sameUnixProviderFileInfoAndStat(parentExpected, &parentStat) {
		return errProviderGenerationTransition
	}
	var childStat unix.Stat_t
	if err := ops.fstatat(
		parentFD, name, &childStat, unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		return err
	}
	if !sameUnixProviderFileInfoAndStat(expected, &childStat) {
		return errProviderGenerationTransition
	}
	if err := validateUnixProviderLiveStat(&childStat, effectiveUID, false); err != nil {
		return err
	}
	if err := validateUnixProviderHardenableStatMode(uint64(childStat.Mode)); err != nil {
		return err
	}
	return ops.fchmodat(parentFD, name, mode, 0)
}

func unixProviderNoFollowChmodFallback(err error) bool {
	return errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EPERM) ||
		errors.Is(err, unix.EACCES) || errors.Is(err, unix.EINVAL)
}

func sameUnixProviderFileInfoAndStat(info os.FileInfo, stat *unix.Stat_t) bool {
	expected, ok := info.Sys().(*syscall.Stat_t)
	return ok && expected != nil && stat != nil &&
		expected.Dev == stat.Dev && expected.Ino == stat.Ino
}

func validateUnixProviderLiveStat(stat *unix.Stat_t, effectiveUID uint32, exactMode bool) error {
	if stat == nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider live generation member is not a regular file"),
		)
	}
	if stat.Uid != effectiveUID {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider live generation member is owned by another user"),
		)
	}
	switch stat.Nlink {
	case 0:
		return errProviderGenerationTransition
	case 1:
	default:
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider live generation member has a hardlink alias"),
		)
	}
	if exactMode && stat.Mode&0o7777 != 0o600 {
		if err := validateUnixProviderHardenableStatMode(uint64(stat.Mode)); err != nil {
			return err
		}
		return errors.Join(
			errProviderUnsafeBoundary,
			errProviderFileModeNeedsHardening,
			errors.New("SQLite provider live generation member mode is not 0600"),
		)
	}
	return nil
}

func validateUnixProviderParentStat(stat *unix.Stat_t, effectiveUID uint32) error {
	if stat == nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		stat.Uid != effectiveUID || stat.Mode&0o7777 != 0o700 {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider live-file parent is unsafe"),
		)
	}
	return nil
}

func validateUnixProviderHardenableMode(mode os.FileMode) error {
	const specialBits = os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	permissions := mode.Perm()
	if mode&specialBits != 0 || permissions&0o600 != 0o600 || permissions&^os.FileMode(0o666) != 0 {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider live generation member mode cannot be safely narrowed to 0600"),
		)
	}
	return nil
}

func validateUnixProviderHardenableStatMode(mode uint64) error {
	permissions := mode & 0o7777
	if permissions&0o600 != 0o600 || permissions & ^uint64(0o666) != 0 {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider live generation member mode cannot be safely narrowed to 0600"),
		)
	}
	return nil
}

func validateUnixProviderLiveInfo(info os.FileInfo, effectiveUID uint32) error {
	if err := validateUnixProviderLiveIdentity(info, effectiveUID); err != nil {
		return err
	}
	const permissionBits = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if info.Mode()&permissionBits != 0o600 {
		if err := validateUnixProviderHardenableMode(info.Mode()); err != nil {
			return err
		}
		return errors.Join(
			errProviderUnsafeBoundary,
			errProviderFileModeNeedsHardening,
			errors.New("SQLite provider live generation member mode is not 0600"),
		)
	}
	return nil
}

func validateUnixProviderLiveIdentity(info os.FileInfo, effectiveUID uint32) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider live generation member is not a regular file"),
		)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return errors.New("SQLite provider live generation metadata is unavailable")
	}
	if stat.Uid != effectiveUID {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider live generation member is owned by another user"),
		)
	}
	switch stat.Nlink {
	case 0:
		return errProviderGenerationTransition
	case 1:
	default:
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider live generation member has a hardlink alias"),
		)
	}
	return nil
}

func secureUnixProviderPath(path string, directory bool) error {
	expected, err := os.Lstat(path)
	if err != nil {
		return err
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("SQLite provider security handle is unavailable")
	}
	defer file.Close()
	return secureUnixProviderHandle(path, directory, expected, file, uint32(os.Geteuid()))
}

func secureUnixProviderHandle(
	path string,
	directory bool,
	expected os.FileInfo,
	file *os.File,
	effectiveUID uint32,
) error {
	opened, err := file.Stat()
	if err != nil || expected == nil || !os.SameFile(expected, opened) ||
		expected.Mode()&os.ModeSymlink != 0 || directory != opened.IsDir() ||
		!directory && !opened.Mode().IsRegular() {
		return errors.Join(errProviderUnsafeBoundary, errors.New("SQLite provider security boundary is unsafe"))
	}
	stat, ok := opened.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || stat.Uid != effectiveUID {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider security boundary is owned by another user"),
		)
	}
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	secured, statErr := file.Stat()
	current, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || secured == nil || current == nil ||
		!os.SameFile(opened, secured) || !os.SameFile(secured, current) ||
		current.Mode()&os.ModeSymlink != 0 || secured.Mode().Perm() != mode {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider security boundary changed while securing"),
			statErr,
			lstatErr,
		)
	}
	return nil
}
