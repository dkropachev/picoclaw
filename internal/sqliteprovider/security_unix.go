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
func secureProviderFile(path string) error      { return secureUnixProviderPath(path, false) }

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
