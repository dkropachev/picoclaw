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

// ExistingWithType returns identity and type from the same stable lstat pair.
func ExistingWithType(path string) (Identity, ObjectType, bool, error) {
	return existingWithLstatAndType(path, os.Lstat)
}

// Opened resolves identity and type from an already-open descriptor.
func Opened(file *os.File) (Identity, ObjectType, error) {
	if file == nil {
		return Identity{}, 0, ErrInvalidPath
	}
	return openedWithStat(file.Stat)
}

func openedWithStat(statFile func() (os.FileInfo, error)) (Identity, ObjectType, error) {
	info, err := statFile()
	if err != nil {
		return Identity{}, 0, fmt.Errorf("inspect opened physical file identity: %w", err)
	}
	if info == nil {
		return Identity{}, 0, ErrUnsupported
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return Identity{}, 0, ErrUnsafeType
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return Identity{}, 0, ErrUnsupported
	}
	objectType := ObjectTypeRegular
	if info.IsDir() {
		objectType = ObjectTypeDirectory
	}
	return newIdentity(fmt.Sprintf("unix:%x:%x", stat.Dev, stat.Ino)), objectType, nil
}

func existingWithLstat(
	path string,
	lstat func(string) (os.FileInfo, error),
) (identity Identity, exists bool, err error) {
	identity, _, exists, err = existingWithLstatAndType(path, lstat)
	return identity, exists, err
}

func existingWithLstatAndType(
	path string,
	lstat func(string) (os.FileInfo, error),
) (identity Identity, objectType ObjectType, exists bool, err error) {
	if !validPath(path) {
		return Identity{}, 0, false, ErrInvalidPath
	}
	info, err := lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Identity{}, 0, false, nil
	}
	if err != nil {
		return Identity{}, 0, false, fmt.Errorf("inspect physical file identity: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() && !info.IsDir() {
		return Identity{}, 0, false, ErrUnsafeType
	}
	after, err := lstat(path)
	if err != nil {
		return Identity{}, 0, false, fmt.Errorf("reinspect physical file identity: %w", err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() && !after.IsDir() {
		return Identity{}, 0, false, fmt.Errorf("%w: object changed during inspection", ErrUnsafeType)
	}
	stat, ok := after.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return Identity{}, 0, false, ErrUnsupported
	}
	if !os.SameFile(info, after) {
		return Identity{}, 0, false, fmt.Errorf("%w: object changed during inspection", ErrUnsafeType)
	}
	objectType = ObjectTypeRegular
	if after.IsDir() {
		objectType = ObjectTypeDirectory
	}
	return newIdentity(fmt.Sprintf("unix:%x:%x", stat.Dev, stat.Ino)), objectType, true, nil
}
