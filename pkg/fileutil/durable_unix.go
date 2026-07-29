//go:build unix

package fileutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

func mkdirDurable(path string, perm os.FileMode) error {
	if err := os.Mkdir(path, perm); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func removeDurable(path string) error {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A prior attempt may have removed the entry and then failed to
			// sync its parent. Re-sync an existing parent before preserving
			// os.Remove's not-exist result so an idempotent caller can safely
			// treat the retry as complete.
			if syncErr := syncDirectory(filepath.Dir(path)); syncErr != nil &&
				!errors.Is(syncErr, fs.ErrNotExist) {
				return errors.Join(err, syncErr)
			}
		}
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncExistingDirectory(path string) error {
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return err
	}
	return dir.Close()
}
