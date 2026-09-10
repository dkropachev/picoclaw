package databasemigration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

type backupCommitOps struct {
	lstat          func(string) (os.FileInfo, error)
	marshal        func(BackupManifest) ([]byte, error)
	writeExclusive func(string, []byte, os.FileMode) error
	secureFile     func(string) error
	syncDir        func(string) error
}

func defaultBackupCommitOps() backupCommitOps {
	return backupCommitOps{
		lstat: os.Lstat, marshal: marshalBackupManifest,
		writeExclusive: writePrivateBackupFileExclusive,
		secureFile:     secureAndValidateBackupFile,
		syncDir:        fileutil.SyncDirectory,
	}
}

func commitBackupSnapshot(session *backupSession) error {
	return commitBackupSnapshotWithOps(session, defaultBackupCommitOps())
}

func commitBackupSnapshotWithOps(session *backupSession, ops backupCommitOps) error {
	if session == nil || !validBackupAbsolutePath(session.root) ||
		!validBackupAbsolutePath(session.finalRoot) || !session.identity.Valid() ||
		!session.parentIdentity.Valid() || session.parent != filepath.Dir(session.root) ||
		filepath.Dir(session.finalRoot) != session.parent ||
		backupPathHasSuffix(session.finalRoot, backupPartialSuffix) ||
		backupPathKey(session.root) != backupPathKey(session.finalRoot+backupPartialSuffix) {
		return errors.New("database backup commit input is invalid")
	}
	if ops.lstat == nil || ops.marshal == nil || ops.writeExclusive == nil ||
		ops.secureFile == nil || ops.syncDir == nil {
		return errors.New("database backup commit operations are unavailable")
	}
	if err := validatePinnedPrivateBackupDirectory(
		session.parent, session.parentIdentity,
	); err != nil {
		return fmt.Errorf("validate database backup parent before commit: %w", err)
	}
	if err := validatePinnedPrivateBackupDirectory(session.root, session.identity); err != nil {
		return fmt.Errorf("validate database backup stage before commit: %w", err)
	}
	manifestPath := filepath.Join(session.root, backupManifestName)
	markerPath := filepath.Join(session.root, backupManifestHash)
	for _, path := range []string{manifestPath, markerPath} {
		if _, err := ops.lstat(path); err == nil {
			return errors.New("database backup control file already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect database backup control file: %w", err)
		}
	}
	payload, err := ops.marshal(session.manifest)
	if err != nil {
		return err
	}
	if err := ops.writeExclusive(manifestPath, payload, 0o600); err != nil {
		return err
	}
	if err := ops.secureFile(manifestPath); err != nil {
		return err
	}
	if err := ops.syncDir(session.root); err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	marker := append([]byte(hex.EncodeToString(digest[:])), '\n')
	if err := ops.writeExclusive(markerPath, marker, 0o600); err != nil {
		return err
	}
	if err := ops.secureFile(markerPath); err != nil {
		return err
	}
	if err := ops.syncDir(session.root); err != nil {
		return err
	}
	return nil
}
