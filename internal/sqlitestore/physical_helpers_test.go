package sqlitestore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

var (
	lstatSQLitePath           = os.Lstat
	chmodSQLitePath           = os.Chmod
	mkdirAllSQLiteDirectories = fileutil.MkdirAllDurable
	syncSQLiteDirectory       = fileutil.SyncDirectory
	openSQLiteFile            = func(path string, flag int, mode os.FileMode) (sqliteFile, error) {
		return os.OpenFile(path, flag, mode)
	}
)

type sqliteFile interface {
	Stat() (os.FileInfo, error)
	Chmod(mode os.FileMode) error
	Sync() error
	Close() error
}

func prepareDatabaseFile(path string) error {
	parent := filepath.Dir(path)
	if err := ensurePrivateDir(parent); err != nil {
		return err
	}
	created := false
	if info, err := lstatSQLitePath(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("database must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		created = true
	}
	file, err := openSQLiteFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	openedInfo, statErr := file.Stat()
	pathInfo, lstatErr := lstatSQLitePath(path)
	if statErr != nil || lstatErr != nil || !openedInfo.Mode().IsRegular() ||
		!pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(openedInfo, pathInfo) {
		_ = file.Close()
		return errors.Join(errors.New("database changed while opening"), statErr, lstatErr)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if created {
		return syncSQLiteDirectory(parent)
	}
	return nil
}

func ensurePrivateDir(path string) error {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return errors.New("database directory is invalid")
	}
	if err := mkdirAllSQLiteDirectories(path, 0o700); err != nil {
		return err
	}
	info, err := lstatSQLitePath(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("database directory must be a real directory")
	}
	return chmodSQLitePath(path, 0o700)
}

func secureSQLiteFiles(path string) error {
	for index, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := lstatSQLitePath(candidate)
		if index > 0 && os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is not a regular SQLite file", filepath.Base(candidate))
		}
		file, err := openSQLiteFile(candidate, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		openedInfo, statErr := file.Stat()
		currentInfo, lstatErr := lstatSQLitePath(candidate)
		if statErr != nil || lstatErr != nil || !openedInfo.Mode().IsRegular() ||
			!currentInfo.Mode().IsRegular() || currentInfo.Mode()&os.ModeSymlink != 0 ||
			!os.SameFile(openedInfo, currentInfo) {
			_ = file.Close()
			return errors.Join(
				fmt.Errorf("%s changed while opening", filepath.Base(candidate)), statErr, lstatErr,
			)
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return nil
}
