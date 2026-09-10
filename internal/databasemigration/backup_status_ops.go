package databasemigration

import (
	"errors"
	"os"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

type backupStatusReadOps = backupReadOps

func defaultBackupStatusReadOps() backupStatusReadOps {
	return defaultBackupReadOps()
}

type backupStatusStageOps struct {
	validateParent   func(string, fileidentity.Identity) error
	createTemp       func(string, string) (*os.File, error)
	openedIdentity   func(*os.File, fileidentity.ObjectType) (fileidentity.Identity, error)
	existingWithType func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error)
	stat             func(*os.File) (os.FileInfo, error)
	close            func(*os.File) error
	writeExclusive   func(string, []byte, os.FileMode) error
	lstat            func(string) (os.FileInfo, error)
	readStatus       func(string, int64) ([]byte, fileidentity.Identity, bool, error)
	removePinned     func(string, fileidentity.Identity) error
}

func defaultBackupStatusStageOps() backupStatusStageOps {
	return backupStatusStageOps{
		validateParent:   validatePinnedPrivateBackupDirectory,
		createTemp:       os.CreateTemp,
		openedIdentity:   backupOpenedIdentity,
		existingWithType: fileidentity.ExistingWithType,
		stat:             func(file *os.File) (os.FileInfo, error) { return file.Stat() },
		close:            func(file *os.File) error { return file.Close() },
		writeExclusive:   writePrivateBackupFileExclusive,
		lstat:            os.Lstat,
		readStatus:       readPinnedBackupStatus,
		removePinned:     removePinnedBackupFile,
	}
}

type backupStatusPlaceholderOps struct {
	openedIdentity func(*os.File, fileidentity.ObjectType) (fileidentity.Identity, error)
	stat           func(*os.File) (os.FileInfo, error)
	close          func(*os.File) error
	removePinned   func(string, fileidentity.Identity) error
}

func defaultBackupStatusPlaceholderOps() backupStatusPlaceholderOps {
	return backupStatusPlaceholderOps{
		openedIdentity: backupOpenedIdentity,
		stat:           func(file *os.File) (os.FileInfo, error) { return file.Stat() },
		close:          func(file *os.File) error { return file.Close() },
		removePinned:   removePinnedBackupFile,
	}
}

func removeBackupStatusPlaceholderWithOps(
	path string,
	placeholder *os.File,
	ops backupStatusPlaceholderOps,
) error {
	if placeholder == nil || !validBackupAbsolutePath(path) ||
		ops.openedIdentity == nil || ops.stat == nil || ops.close == nil ||
		ops.removePinned == nil {
		return errors.New("database backup status placeholder operations are unavailable")
	}
	identity, identityErr := ops.openedIdentity(
		placeholder, fileidentity.ObjectTypeRegular,
	)
	_, statErr := ops.stat(placeholder)
	closeErr := ops.close(placeholder)
	if identityErr != nil || statErr != nil || closeErr != nil {
		var cleanupErr error
		if identity.Valid() {
			cleanupErr = ops.removePinned(path, identity)
		}
		return errors.Join(identityErr, statErr, closeErr, cleanupErr)
	}
	return ops.removePinned(path, identity)
}
