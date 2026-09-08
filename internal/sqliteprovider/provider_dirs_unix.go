//go:build unix && !aix

package sqliteprovider

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func makeProviderDirectories(path string, mode os.FileMode) error {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	ancestor := absolute
	missing := make([]string, 0, 4)
	for {
		info, statErr := os.Lstat(ancestor)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return errors.New("SQLite provider directory ancestor is unsafe")
			}
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return errors.New("SQLite provider directory has no existing ancestor")
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
	// Open every existing component relative to a retained parent. A pathname
	// open of only the deepest ancestor leaves intermediate retargeting races.
	fd, err := unix.Open(
		string(os.PathSeparator), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0,
	)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var rootStat unix.Stat_t
	protected := false
	var trusted bool
	if err := unix.Fstat(fd, &rootStat); err == nil {
		protected, trusted = trustedProviderUnixDirectory(rootStat, protected)
	}
	if !trusted {
		return errors.New("SQLite provider root directory is not trusted")
	}
	components := strings.Split(
		strings.TrimPrefix(ancestor, string(os.PathSeparator)), string(os.PathSeparator),
	)
	for _, component := range components {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(
			fd, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0,
		)
		if openErr != nil {
			return openErr
		}
		var componentStat unix.Stat_t
		if err := unix.Fstat(next, &componentStat); err == nil {
			protected, trusted = trustedProviderUnixDirectory(componentStat, protected)
		} else {
			trusted = false
		}
		if !trusted {
			_ = unix.Close(next)
			return errors.New("SQLite provider directory ancestor is not trusted")
		}
		_ = unix.Close(fd)
		fd = next
	}
	for index := len(missing) - 1; index >= 0; index-- {
		name := missing[index]
		created := false
		if err := unix.Mkdirat(fd, name, uint32(mode.Perm())); err != nil {
			if !errors.Is(err, unix.EEXIST) {
				return err
			}
		} else {
			created = true
		}
		next, err := unix.Openat(
			fd, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0,
		)
		if err != nil {
			return err
		}
		var stat unix.Stat_t
		if err := unix.Fstat(next, &stat); err == nil {
			protected, trusted = trustedProviderUnixDirectory(stat, protected)
		} else {
			trusted = false
		}
		if !trusted || stat.Uid != uint32(os.Geteuid()) {
			_ = unix.Close(next)
			return errors.New("SQLite provider created directory identity is unsafe")
		}
		if created {
			if err := unix.Fchmod(next, uint32(mode.Perm())); err != nil {
				_ = unix.Close(next)
				return err
			}
			if err := unix.Fsync(fd); err != nil {
				_ = unix.Close(next)
				return err
			}
			if err := unix.Fsync(next); err != nil {
				_ = unix.Close(next)
				return err
			}
		}
		_ = unix.Close(fd)
		fd = next
	}
	return nil
}

func trustedProviderUnixDirectory(stat unix.Stat_t, protected bool) (bool, bool) {
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return protected, false
	}
	effectiveUID := uint32(os.Geteuid())
	if stat.Uid != effectiveUID && stat.Uid != 0 {
		return protected, false
	}
	if !protected && stat.Mode&0o022 != 0 &&
		!(stat.Uid == 0 && stat.Mode&unix.S_ISVTX != 0) {
		return protected, false
	}
	// Once a current-user directory denies traversal to group and world,
	// descendants remain protected even if a hermetic harness gives a nested
	// directory broader mode bits. Same-user interference is outside this
	// advisory filesystem boundary.
	if stat.Uid == effectiveUID && stat.Mode&0o011 == 0 {
		protected = true
	}
	return protected, true
}
