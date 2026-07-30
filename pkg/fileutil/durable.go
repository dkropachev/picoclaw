package fileutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// MkdirAllDurable creates path and every missing parent directory, making each
// new directory entry durable before creating the next child. Existing
// directories are left unchanged.
func MkdirAllDurable(path string, perm os.FileMode) error {
	if path == "" {
		return os.MkdirAll(path, perm)
	}
	clean := filepath.Clean(path)
	info, err := os.Stat(clean)
	if err == nil {
		if info.IsDir() {
			// A previous creation may have reached os.Mkdir but failed while
			// syncing its parent. Retrying must re-establish that durability
			// instead of treating existence as proof of success.
			return syncExistingDirectory(clean)
		}
		return &os.PathError{Op: "mkdir", Path: clean, Err: syscall.ENOTDIR}
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	parent := filepath.Dir(clean)
	if parent != clean {
		if err := MkdirAllDurable(parent, perm); err != nil {
			return err
		}
	}
	if err := mkdirDurable(clean, perm); err != nil {
		// Match os.MkdirAll's concurrent-creator behavior. The platform helper
		// may race with another process after the initial Stat.
		if info, statErr := os.Lstat(clean); statErr == nil && info.IsDir() {
			return syncExistingDirectory(clean)
		}
		return err
	}
	return nil
}

// RemoveDurable removes one file or empty directory and makes its logical
// deletion durable before returning. It preserves os.Remove's not-exist and
// non-empty-directory behavior.
func RemoveDurable(path string) error {
	if path == "" {
		return os.Remove(path)
	}
	return removeDurable(path)
}

// SyncDirectory makes prior entry changes in path durable. Callers use this
// after an atomic rename that did not otherwise create or remove an entry
// through this package.
func SyncDirectory(path string) error {
	if path == "" {
		return syncDirectory(path)
	}
	return syncDirectory(filepath.Clean(path))
}
