//go:build windows

package fileutil

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func mkdirDurable(path string, perm os.FileMode) error {
	parent := filepath.Dir(path)
	staged, err := os.MkdirTemp(parent, ".picoclaw-mkdir-*")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(staged)
		}
	}()
	if err := os.Chmod(staged, perm); err != nil {
		return err
	}
	if err := moveFileWriteThrough(staged, path, false); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func removeDurable(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		dir, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		_, readErr := dir.Readdirnames(1)
		closeErr := dir.Close()
		switch {
		case readErr == nil:
			return &os.PathError{Op: "remove", Path: path, Err: windows.ERROR_DIR_NOT_EMPTY}
		case !errors.Is(readErr, io.EOF):
			return readErr
		case closeErr != nil:
			return closeErr
		}
	}

	var tombstone string
	if info.IsDir() {
		tombstone, err = unusedDirectoryTombstonePath(filepath.Dir(path))
		if err != nil {
			return err
		}
		if err := moveFileWriteThrough(path, tombstone, false); err != nil {
			return err
		}
		if err := os.Remove(tombstone); err != nil {
			if errors.Is(err, windows.ERROR_DIR_NOT_EMPTY) {
				if rollbackErr := moveFileWriteThrough(
					tombstone,
					path,
					false,
				); rollbackErr != nil {
					return errors.Join(err, rollbackErr)
				}
				return err
			}
			// The write-through move already made the original name's
			// deletion durable. Other tombstone cleanup failures are
			// retryable housekeeping.
		}
		return nil
	} else {
		tombstone, err = reservedFileTombstonePath(filepath.Dir(path))
		if err != nil {
			return err
		}
		if err := moveFileWriteThrough(path, tombstone, true); err != nil {
			_ = os.Remove(tombstone)
			return err
		}
	}

	// The write-through move is the durable logical deletion. Removing the
	// hidden tombstone is housekeeping: a crash can leave it behind without
	// resurrecting the original path.
	_ = os.Remove(tombstone)
	return nil
}

func syncExistingDirectory(path string) error {
	_ = path
	return nil
}

func reservedFileTombstonePath(dir string) (string, error) {
	file, err := os.CreateTemp(dir, ".picoclaw-remove-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func unusedDirectoryTombstonePath(dir string) (string, error) {
	path, err := os.MkdirTemp(dir, ".picoclaw-remove-*")
	if err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

func moveFileWriteThrough(source, target string, replace bool) error {
	sourcePath, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPath, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if replace {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	return windows.MoveFileEx(sourcePath, targetPath, flags)
}

func syncDirectory(path string) error {
	_ = path
	return nil
}
