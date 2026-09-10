package databasemigration

import (
	"errors"
	"os"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

type backupTempDirectoryOps struct {
	pinParent      func(string) (fileidentity.Identity, error)
	mkdirTemp      func(string, string) (string, error)
	existing       func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error)
	validateParent func(string, fileidentity.Identity) error
	secure         func(string) error
	remove         func(string, fileidentity.Identity) error
}

func defaultBackupTempDirectoryOps() backupTempDirectoryOps {
	return backupTempDirectoryOps{
		pinParent:      pinPrivateBackupDirectory,
		mkdirTemp:      os.MkdirTemp,
		existing:       fileidentity.ExistingWithType,
		validateParent: validatePinnedPrivateBackupDirectory,
		secure:         secureAndValidateBackupDirectory,
		remove:         removePinnedBackupTreeIdentity,
	}
}

func createPinnedBackupTempDirectory(parentPath, prefix string) (string, error) {
	return createPinnedBackupTempDirectoryWithOps(
		parentPath, prefix, defaultBackupTempDirectoryOps(),
	)
}

func createPinnedBackupTempDirectoryWithOps(
	parentPath,
	prefix string,
	ops backupTempDirectoryOps,
) (string, error) {
	if !validBackupAbsolutePath(parentPath) || !validBackupPathComponent(prefix) ||
		len(prefix) > backupMaxComponent-32 {
		return "", errors.New("database backup temp-directory input is invalid")
	}
	parentIdentity, err := ops.pinParent(parentPath)
	if err != nil {
		return "", err
	}
	path, err := ops.mkdirTemp(parentPath, prefix)
	if err != nil {
		return "", err
	}
	identity, objectType, exists, identityErr := ops.existing(path)
	fail := func(cause error) (string, error) {
		if identityErr == nil && exists && objectType == fileidentity.ObjectTypeDirectory &&
			identity.Valid() {
			cause = errors.Join(cause, ops.remove(path, identity))
		}
		return "", cause
	}
	if identityErr != nil || !exists || objectType != fileidentity.ObjectTypeDirectory {
		return fail(errors.Join(
			errors.New("database backup temp directory identity is unavailable"), identityErr,
		))
	}
	if err := ops.validateParent(parentPath, parentIdentity); err != nil {
		return fail(err)
	}
	if err := ops.secure(path); err != nil {
		return fail(err)
	}
	securedIdentity, securedType, securedExists, securedErr := ops.existing(path)
	if securedErr != nil || !securedExists || securedType != fileidentity.ObjectTypeDirectory ||
		securedIdentity != identity {
		return fail(errors.Join(
			errors.New("database backup temp directory changed while securing"), securedErr,
		))
	}
	if err := ops.validateParent(parentPath, parentIdentity); err != nil {
		return fail(err)
	}
	return path, nil
}
