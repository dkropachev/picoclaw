//go:build !windows

package database

import (
	"fmt"
	"os"
)

func createOwnerOnlyDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func prepareOwnerOnlyLeafDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func createOwnerOnlyTempFile(directory, prefix string, mode os.FileMode) (*os.File, error) {
	file, err := os.CreateTemp(directory, prefix+"*")
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(mode); err != nil {
		path := file.Name()
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return file, nil
}

func createOwnerOnlyExclusiveFile(path string, mode os.FileMode) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return file, nil
}

func openOwnerOnlyExistingFile(path string, mode os.FileMode) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil {
		err = validateOwnerOnlyFile(path, info, mode)
	}
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("validate owner-only file: %w", err)
	}
	return file, nil
}
