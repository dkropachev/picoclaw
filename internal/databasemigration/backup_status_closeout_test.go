package databasemigration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func TestBackupStatusCloseoutInputAndEncodingBoundaries(t *testing.T) {
	session := newStatusStateSession(t)
	changedParent := *session
	changedParent.parent += "-changed"
	if err := changedParent.finishWithOps(
		"migration_in_progress", nil, defaultBackupFinishOps(),
	); err == nil || !strings.Contains(err.Error(), "parent provenance") {
		t.Fatalf("changed status parent = %v", err)
	}

	invalidParentIdentity := *session
	invalidParentIdentity.parentIdentity = fileidentity.Identity{}
	if err := invalidParentIdentity.finishWithOps(
		"migration_in_progress", nil, defaultBackupFinishOps(),
	); err == nil || !strings.Contains(err.Error(), "validate database backup parent") {
		t.Fatalf("invalid status parent identity = %v", err)
	}

	partial := *session
	partial.root += backupPartialSuffix
	if err := partial.finishMigrationStatusWithOps(
		"migration_in_progress", nil, defaultBackupFinishOps(),
	); err == nil {
		t.Fatal("partial backup accepted migration status")
	}
	if _, _, err := (&backupSession{}).readMigrationStatusWithOps(
		defaultBackupFinishOps(),
	); err == nil {
		t.Fatal("unavailable session read migration status")
	}
	if validBackupMigrationOutcome("unknown", nil) {
		t.Fatal("unknown migration status outcome is valid")
	}
	if got := boundedBackupError(nil); got != "" {
		t.Fatalf("nil bounded backup error = %q", got)
	}
	long := errors.New(strings.Repeat("x", backupMaxErrorBytes+100))
	if got := boundedBackupError(long); len(got) != backupMaxErrorBytes {
		t.Fatalf("bounded backup error length = %d", len(got))
	}
	invalidUTF8 := errors.New(string([]byte{0xff}) + strings.Repeat("x", backupMaxErrorBytes))
	if got := boundedBackupError(invalidUTF8); len(got) > backupMaxErrorBytes || !strings.Contains(got, "�") {
		t.Fatalf("invalid UTF-8 bounded error = %q", got)
	}
	cutRune := errors.New(strings.Repeat("x", backupMaxErrorBytes-1) + "é-extra")
	if got := boundedBackupError(cutRune); len(got) != backupMaxErrorBytes-1 {
		t.Fatalf("rune-safe bounded error length = %d", len(got))
	}
}

func TestBackupStatusRecordRejectsAlternateRepresentations(t *testing.T) {
	digest := strings.Repeat("a", 64)
	status := backupMigrationStatus{
		Version: backupStatusVersion, SnapshotDigest: digest,
		Revision: 1, Outcome: "migration_in_progress",
	}
	canonical, err := marshalBackupStatus(status)
	if err != nil {
		t.Fatal(err)
	}
	validIdentity := sessionStatusIdentity(t)
	tests := []struct {
		name     string
		payload  []byte
		identity fileidentity.Identity
		digest   string
	}{
		{
			name:     "trailing JSON",
			payload:  append(append([]byte(nil), canonical...), []byte("{}")...),
			identity: validIdentity, digest: digest,
		},
		{
			name: "noncanonical bytes", payload: bytes.TrimSuffix(canonical, []byte{'\n'}),
			identity: validIdentity, digest: digest,
		},
		{
			name: "wrong snapshot", payload: canonical,
			identity: validIdentity, digest: strings.Repeat("b", 64),
		},
		{name: "missing identity", payload: canonical, digest: digest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultBackupFinishOps()
			ops.readStatus = func(string, int64) (
				[]byte, fileidentity.Identity, bool, error,
			) {
				return test.payload, test.identity, true, nil
			}
			if _, _, _, _, err := (&backupSession{}).readStatusRecordWithOps(
				"status", test.digest, ops,
			); err == nil {
				t.Fatal("alternate migration status representation was accepted")
			}
		})
	}
}

func TestBackupStatusPlaceholderRejectsUnavailableOperations(t *testing.T) {
	if err := removeBackupStatusPlaceholderWithOps(
		"relative", nil, backupStatusPlaceholderOps{},
	); err == nil {
		t.Fatal("unavailable status placeholder operations were accepted")
	}
}

func TestBackupStatusVerificationPropagatesCommittedDigestFailure(t *testing.T) {
	session := newStatusStateSession(t)
	canary := errors.New("committed digest read failed")
	ops := defaultBackupFinishOps()
	ops.verify = func(context.Context, *backupSession) error { return nil }
	ops.read = func(string, int64) ([]byte, error) { return nil, canary }
	if _, err := session.verifyStatusArchiveWithOps(ops); !errors.Is(err, canary) {
		t.Fatalf("committed status digest error = %v", err)
	}
}

func TestBackupStatusStageRejectsMissingPublishedIdentity(t *testing.T) {
	session := newStatusStateSession(t)
	path := filepath.Join(session.parent, ".status-missing-identity.partial")
	ops := defaultBackupStatusStageOps()
	ops.createTemp = func(string, string) (*os.File, error) {
		return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	}
	ops.existingWithType = func(string) (
		fileidentity.Identity, fileidentity.ObjectType, bool, error,
	) {
		return fileidentity.Identity{}, fileidentity.ObjectTypeRegular, false, nil
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if staged, err := stageBackupMigrationStatusWithOps(
		session, []byte("status"), ops,
	); staged != nil || err == nil {
		t.Fatalf("status without published identity = %#v, %v", staged, err)
	}
}

func sessionStatusIdentity(t *testing.T) fileidentity.Identity {
	t.Helper()
	session := newStatusStateSession(t)
	return session.identity
}
