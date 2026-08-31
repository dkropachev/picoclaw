package wecom

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

func lockWecomReqIDDatabase(databasePath string) (func(), error) {
	if strings.TrimSpace(databasePath) == "" {
		return nil, errors.New("WeCom request-route database path is required")
	}
	lockDirectory := databasePath + ".locks"
	if err := sqlitestore.EnsurePrivateDir(filepath.Dir(databasePath)); err != nil {
		return nil, err
	}
	if err := sqlitestore.EnsurePrivateDir(lockDirectory); err != nil {
		return nil, err
	}
	return lockWecomReqIDFile(filepath.Join(lockDirectory, "store"))
}

func openWecomReqIDLockFile(path string) (*os.File, error) {
	lockPath := path + ".lock"
	if info, err := os.Lstat(lockPath); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("WeCom request-route lock must be a regular file")
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
			errors.New("WeCom request-route lock changed while opening"),
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
