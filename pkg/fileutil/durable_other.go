//go:build !unix && !windows

package fileutil

import (
	"os"
)

func mkdirDurable(path string, perm os.FileMode) error {
	return os.Mkdir(path, perm)
}

func removeDurable(path string) error {
	return os.Remove(path)
}

func syncExistingDirectory(path string) error {
	_ = path
	return nil
}

func syncDirectory(path string) error {
	_ = path
	return nil
}
