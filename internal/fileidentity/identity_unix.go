//go:build unix && !aix

package fileidentity

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// Existing returns a stable device/inode identity for an existing regular
// file or directory. Missing paths return exists=false without error.
func Existing(path string) (identity Identity, exists bool, err error) {
	return existingWithLstat(path, os.Lstat)
}

func existingWithLstat(
	path string,
	lstat func(string) (os.FileInfo, error),
) (identity Identity, exists bool, err error) {
	if !validPath(path) {
		return Identity{}, false, ErrInvalidPath
	}
	info, err := lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Identity{}, false, nil
	}
	if err != nil {
		return Identity{}, false, fmt.Errorf("inspect physical file identity: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() && !info.IsDir() {
		return Identity{}, false, ErrUnsafeType
	}
	after, err := lstat(path)
	if err != nil {
		return Identity{}, false, fmt.Errorf("reinspect physical file identity: %w", err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() && !after.IsDir() {
		return Identity{}, false, fmt.Errorf("%w: object changed during inspection", ErrUnsafeType)
	}
	stat, ok := after.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return Identity{}, false, ErrUnsupported
	}
	if !os.SameFile(info, after) {
		return Identity{}, false, fmt.Errorf("%w: object changed during inspection", ErrUnsafeType)
	}
	return newIdentity(fmt.Sprintf("unix:%x:%x", stat.Dev, stat.Ino)), true, nil
}
