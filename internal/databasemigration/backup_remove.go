package databasemigration

import (
	"errors"
	"os"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func removeBackupTreeDurable(
	path string,
	root *os.Root,
	leaf string,
	child *os.Root,
	expected fileidentity.Identity,
) error {
	entries := 0
	if err := removePinnedBackupTreeContents(
		path, child, map[fileidentity.Identity]string{expected: path}, &entries,
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
	return requireMissingBackupRemovalPath(path, fileidentity.ExistingWithType)
}

func removeBackupFileDurable(
	path string,
	root *os.Root,
	leaf string,
	file *os.File,
	expected fileidentity.Identity,
) error {
	openedIdentity, objectType, err := fileidentity.Opened(file)
	if err != nil || objectType != fileidentity.ObjectTypeRegular || openedIdentity != expected {
		return errors.Join(errors.New("database backup removal file handle changed"), err)
	}
	if err := matchBackupRemovalIdentity(
		path, expected, fileidentity.ObjectTypeRegular, fileidentity.ExistingWithType,
	); err != nil {
		return err
	}
	return root.Remove(leaf)
}
