package databasemigration

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func removeBackupTreeDurable(
	path string,
	root *os.Root,
	leaf string,
	child *os.Root,
	expected fileidentity.Identity,
) error {
	return removeBackupTreeDurableWithSync(
		path, root, leaf, child, expected, fileutil.SyncDirectory,
	)
}

func removeBackupTreeDurableWithSync(
	path string,
	root *os.Root,
	leaf string,
	child *os.Root,
	expected fileidentity.Identity,
	syncDir func(string) error,
) error {
	if syncDir == nil {
		return errors.New("database backup removal directory sync is unavailable")
	}
	entries := 0
	if err := removePinnedBackupTreeContentsWithSync(
		path, child, map[fileidentity.Identity]string{expected: path}, &entries,
		syncDir,
	); err != nil {
		return err
	}
	if err := matchBackupRemovalIdentity(
		path, expected, fileidentity.ObjectTypeDirectory, fileidentity.ExistingWithType,
	); err != nil {
		return err
	}
	if err := root.Remove(leaf); err != nil {
		return err
	}
	if err := requireMissingBackupRemovalPath(path, fileidentity.ExistingWithType); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func removeBackupFileDurable(
	path string,
	root *os.Root,
	leaf string,
	file *os.File,
	expected fileidentity.Identity,
) error {
	return removeBackupFileDurableWithSync(
		path, root, leaf, file, expected, fileutil.SyncDirectory,
	)
}

func removeBackupFileDurableWithSync(
	path string,
	root *os.Root,
	leaf string,
	file *os.File,
	expected fileidentity.Identity,
	syncDir func(string) error,
) error {
	if syncDir == nil {
		return errors.New("database backup removal directory sync is unavailable")
	}
	openedIdentity, objectType, err := fileidentity.Opened(file)
	if err != nil || objectType != fileidentity.ObjectTypeRegular || openedIdentity != expected {
		return errors.Join(errors.New("database backup removal file handle changed"), err)
	}
	if err := matchBackupRemovalIdentity(
		path, expected, fileidentity.ObjectTypeRegular, fileidentity.ExistingWithType,
	); err != nil {
		return err
	}
	if err := root.Remove(leaf); err != nil {
		return err
	}
	if err := requireMissingBackupRemovalPath(path, fileidentity.ExistingWithType); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
