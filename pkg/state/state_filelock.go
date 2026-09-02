package state

import (
	"errors"
	"os"

	"github.com/sipeed/picoclaw/pkg/database"
)

func openRuntimeStateLockFile(path string) (*os.File, error) {
	if !runtimeStateLocalProviderAuthorized() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"runtime-state lock access requires database owner fencing",
		)
	}
	lockPath := path + ".lock"
	if info, err := os.Lstat(lockPath); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("runtime-state lock must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	openedInfo, statErr := file.Stat()
	pathInfo, lstatErr := os.Lstat(lockPath)
	if statErr != nil || lstatErr != nil || openedInfo == nil || pathInfo == nil ||
		!openedInfo.Mode().IsRegular() || !pathInfo.Mode().IsRegular() ||
		pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, pathInfo) {
		_ = file.Close()
		return nil, errors.Join(
			errors.New("runtime-state lock changed while opening"),
			statErr,
			lstatErr,
		)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
