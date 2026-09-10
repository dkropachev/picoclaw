package databasemigration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

type backupMigrationStatus struct {
	Version        int    `json:"version"`
	SnapshotDigest string `json:"snapshot_digest"`
	Revision       uint64 `json:"revision"`
	Outcome        string `json:"outcome"`
	Error          string `json:"error,omitempty"`
}

func (b *backupSession) finish(outcome string, migrationErr error) error {
	return b.finishWithOps(outcome, migrationErr, defaultBackupFinishOps())
}

type backupFinishOps struct {
	verify        func(context.Context, *backupSession) error
	marshal       func(BackupManifest) ([]byte, error)
	marshalStatus func(backupMigrationStatus) ([]byte, error)
	read          func(string, int64) ([]byte, error)
	readStatus    func(string, int64) ([]byte, fileidentity.Identity, bool, error)
	createStatus  func(*backupSession, string, []byte) (fileidentity.Identity, error)
	replaceStatus func(
		*backupSession,
		string,
		[]byte,
		fileidentity.Identity,
		[]byte,
	) (fileidentity.Identity, error)
	syncDir func(string) error
}

func defaultBackupFinishOps() backupFinishOps {
	return backupFinishOps{
		verify:  func(ctx context.Context, session *backupSession) error { return session.verify(ctx) },
		marshal: marshalBackupManifest, marshalStatus: marshalBackupStatus,
		read: readPrivateBackupFile, readStatus: readPinnedBackupStatus,
		createStatus:  createInitialBackupMigrationStatus,
		replaceStatus: replaceBackupMigrationStatus,
		syncDir:       fileutil.SyncDirectory,
	}
}

func (b *backupSession) finishWithOps(
	outcome string,
	migrationErr error,
	ops backupFinishOps,
) error {
	if b == nil || b.root == "" {
		return errors.New("database backup session is unavailable")
	}
	if b.parent != filepath.Dir(b.root) {
		return errors.New("database backup parent provenance changed")
	}
	if err := validatePinnedPrivateBackupDirectory(b.parent, b.parentIdentity); err != nil {
		return fmt.Errorf("validate database backup parent before finish: %w", err)
	}
	return b.finishMigrationStatusWithOps(outcome, migrationErr, ops)
}

func validBackupMigrationOutcome(outcome string, migrationErr error) bool {
	switch outcome {
	case "migration_in_progress", "dry_run", "complete":
		return migrationErr == nil
	case "failed", "outcome_unknown", "complete_with_cleanup_error":
		return migrationErr != nil
	default:
		return false
	}
}

func marshalBackupStatus(status backupMigrationStatus) ([]byte, error) {
	if status.Version != backupStatusVersion || !validBackupDigest(status.SnapshotDigest) ||
		!validBackupStatusRevision(status) ||
		!validBackupMigrationOutcome(status.Outcome, statusError(status)) ||
		len(status.Error) > backupMaxErrorBytes || !utf8.ValidString(status.Error) ||
		strings.ContainsRune(status.Error, 0) {
		return nil, errors.New("database backup migration status is invalid")
	}
	// Struct contains only bounded scalar strings, so JSON encoding cannot fail.
	payload, _ := json.MarshalIndent(status, "", "  ")
	payload = append(payload, '\n')
	return payload, nil
}

func (b *backupSession) readMigrationStatus() (backupMigrationStatus, bool, error) {
	return b.readMigrationStatusWithOps(defaultBackupFinishOps())
}

func statusError(status backupMigrationStatus) error {
	if status.Error == "" {
		return nil
	}
	return errors.New(status.Error)
}

func boundedBackupError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToValidUTF8(err.Error(), "�")
	message = strings.ReplaceAll(message, "\x00", "�")
	if len(message) > backupMaxErrorBytes {
		message = message[:backupMaxErrorBytes]
	}
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}
