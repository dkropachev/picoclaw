package databasemigration

import (
	"errors"
	"fmt"
	"os"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func backupExistingIdentity(path string, want fileidentity.ObjectType) (fileidentity.Identity, error) {
	identity, objectType, exists, err := fileidentity.ExistingWithType(path)
	if err != nil || !exists || objectType != want {
		return fileidentity.Identity{}, errors.Join(
			errors.New("database backup path identity or type is unavailable"), err,
		)
	}
	return identity, nil
}

func pinPrivateBackupDirectory(path string) (fileidentity.Identity, error) {
	if !validBackupAbsolutePath(path) {
		return fileidentity.Identity{}, errors.New("private database backup directory path is invalid")
	}
	info, err := os.Lstat(path)
	if err != nil || info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fileidentity.Identity{}, errors.Join(
			errors.New("private database backup directory is unsafe"), err,
		)
	}
	if validateErr := fileutil.ValidatePrivateDirectory(path, info); validateErr != nil {
		return fileidentity.Identity{}, fmt.Errorf(
			"validate private database backup directory: %w", validateErr,
		)
	}
	root, err := openExactBackupRoot(path)
	if err != nil {
		return fileidentity.Identity{}, err
	}
	file, err := root.Open(".")
	if err != nil {
		return fileidentity.Identity{}, errors.Join(err, root.Close())
	}
	identity, identityErr := backupPathMatchesOpened(
		path, file, fileidentity.ObjectTypeDirectory,
	)
	closeErr := file.Close()
	rootCloseErr := root.Close()
	if identityErr != nil || closeErr != nil || rootCloseErr != nil {
		return fileidentity.Identity{}, errors.Join(identityErr, closeErr, rootCloseErr)
	}
	return identity, nil
}

func validatePinnedPrivateBackupDirectory(path string, expected fileidentity.Identity) error {
	if !expected.Valid() {
		return errors.New("private database backup directory identity is invalid")
	}
	identity, err := pinPrivateBackupDirectory(path)
	if err != nil || identity != expected {
		return errors.Join(errors.New("private database backup directory identity changed"), err)
	}
	return nil
}

func backupOpenedIdentity(file *os.File, want fileidentity.ObjectType) (fileidentity.Identity, error) {
	identity, objectType, err := fileidentity.Opened(file)
	if err != nil || objectType != want {
		return fileidentity.Identity{}, errors.Join(
			errors.New("database backup opened identity or type is unavailable"), err,
		)
	}
	return identity, nil
}

func backupPathMatchesOpened(
	path string,
	file *os.File,
	want fileidentity.ObjectType,
) (fileidentity.Identity, error) {
	pathIdentity, err := backupExistingIdentity(path, want)
	if err != nil {
		return fileidentity.Identity{}, err
	}
	openedIdentity, err := backupOpenedIdentity(file, want)
	if err != nil {
		return fileidentity.Identity{}, err
	}
	if pathIdentity != openedIdentity {
		return fileidentity.Identity{}, errors.New("database backup path and handle identities differ")
	}
	return openedIdentity, nil
}
