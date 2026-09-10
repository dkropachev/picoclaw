package databasemigration

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupStatusManifestValidation(t *testing.T) {
	for _, status := range []backupMigrationStatus{
		{Version: backupStatusVersion, SnapshotDigest: strings.Repeat("a", 64), Revision: 1, Outcome: "migration_in_progress"},
		{Version: backupStatusVersion, SnapshotDigest: strings.Repeat("a", 64), Revision: 2, Outcome: "dry_run"},
		{Version: backupStatusVersion, SnapshotDigest: strings.Repeat("a", 64), Revision: 2, Outcome: "complete"},
		{Version: backupStatusVersion, SnapshotDigest: strings.Repeat("a", 64), Revision: 2, Outcome: "failed", Error: "failed"},
		{Version: backupStatusVersion, SnapshotDigest: strings.Repeat("a", 64), Revision: 2, Outcome: "outcome_unknown", Error: "unknown"},
		{Version: backupStatusVersion, SnapshotDigest: strings.Repeat("a", 64), Revision: 2, Outcome: "complete_with_cleanup_error", Error: "cleanup"},
	} {
		if _, err := marshalBackupStatus(status); err != nil {
			t.Fatalf("valid status %#v: %v", status, err)
		}
	}
	for _, status := range []backupMigrationStatus{
		{},
		{Version: backupStatusVersion, SnapshotDigest: "bad", Revision: 2, Outcome: "complete"},
		{Version: backupStatusVersion, SnapshotDigest: strings.Repeat("a", 64), Revision: 2, Outcome: "failed"},
		{Version: backupStatusVersion, SnapshotDigest: strings.Repeat("a", 64), Revision: 2, Outcome: "complete", Error: "bad"},
		{Version: backupStatusVersion, SnapshotDigest: strings.Repeat("a", 64), Revision: 2, Outcome: "failed", Error: strings.Repeat("x", backupMaxErrorBytes+1)},
		{Version: backupStatusVersion, SnapshotDigest: strings.Repeat("a", 64), Revision: 1, Outcome: "complete"},
	} {
		if payload, err := marshalBackupStatus(status); payload != nil || err == nil {
			t.Fatalf("invalid status marshaled: %q %#v", payload, status)
		}
	}
}

func TestBackupManifestValidationRunsBeforeFilesystemAccess(t *testing.T) {
	manifest := validManifestValidationFixture(t)
	manifest.Version++
	session := &backupSession{root: filepath.Join(t.TempDir(), "missing"), manifest: manifest}
	if err := session.verify(t.Context()); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("verification did not reject manifest first: %v", err)
	}
}
