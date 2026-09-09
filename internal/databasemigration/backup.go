package databasemigration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func ensurePrivateBackupDirectory(path string) error {
	return ensurePrivateBackupDirectoryWithOps(path, defaultEnsureBackupDirectoryOps())
}

type ensureBackupDirectoryOps struct {
	validate func(string) error
	ensure   func(string) error
	lstat    func(string) (os.FileInfo, error)
	resolve  func(string) (string, error)
	absolute func(string) (string, error)
	secure   func(string) error
	sync     func(string) error
}

func defaultEnsureBackupDirectoryOps() ensureBackupDirectoryOps {
	return ensureBackupDirectoryOps{
		validate: validateBackupAncestors,
		ensure:   sqliteprovider.EnsurePrivateDirectory,
		lstat:    os.Lstat,
		resolve:  filepath.EvalSymlinks,
		absolute: filepath.Abs,
		secure:   secureAndValidateBackupDirectory,
		sync:     fileutil.SyncDirectory,
	}
}

func ensurePrivateBackupDirectoryWithOps(path string, ops ensureBackupDirectoryOps) error {
	if !validBackupAbsolutePath(path) {
		return errors.New("database backup directory path is invalid")
	}
	if err := ops.validate(path); err != nil {
		return err
	}
	if err := ops.ensure(path); err != nil {
		return fmt.Errorf("create database backup directory: %w", err)
	}
	info, err := ops.lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("database backup directory is unsafe")
	}
	resolved, err := ops.resolve(path)
	if err != nil {
		return err
	}
	absolute, err := ops.absolute(path)
	if err != nil {
		return err
	}
	resolvedAbsolute, err := ops.absolute(resolved)
	if err != nil || filepath.Clean(absolute) != filepath.Clean(resolvedAbsolute) {
		return errors.New("database backup directory contains a symlink")
	}
	if err := ops.secure(path); err != nil {
		return err
	}
	if err := ops.validate(path); err != nil {
		return err
	}
	return ops.sync(path)
}
