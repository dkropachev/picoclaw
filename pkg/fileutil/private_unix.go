//go:build !windows

package fileutil

import (
	"errors"
	"os"
)

func SecurePrivateDirectory(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("private path is not a real directory")
	}
	if chmodErr := os.Chmod(path, 0o700); chmodErr != nil {
		return nil, chmodErr
	}
	secured, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, secured) || secured.Mode().Perm() != 0o700 {
		return nil, errors.Join(errors.New("private directory changed while securing"), err)
	}
	return secured, nil
}

func ValidatePrivateDirectory(path string, expected os.FileInfo) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if expected == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 || !os.SameFile(info, expected) {
		return errors.New("private directory identity or permissions changed")
	}
	return nil
}

func SecurePrivateFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("private path is not a regular file")
	}
	if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
		return nil, chmodErr
	}
	secured, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, secured) || secured.Mode().Perm()&0o077 != 0 {
		return nil, errors.Join(errors.New("private file changed while securing"), err)
	}
	return secured, nil
}

func ValidatePrivateFile(path string, expected os.FileInfo) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if expected == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 || !os.SameFile(info, expected) {
		return errors.New("private file identity or permissions changed")
	}
	return nil
}
