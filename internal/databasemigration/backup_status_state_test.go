package databasemigration

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func newStatusStateSession(t *testing.T) *backupSession {
	t.Helper()
	home := migrationHome(t)
	spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	session, err := snapshotBackup(
		t.Context(), time.Now, home,
		[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestMigrationStatusRequiresExclusiveTwoRevisionStateMachine(t *testing.T) {
	terminals := []struct {
		outcome string
		cause   error
	}{
		{outcome: "dry_run"},
		{outcome: "complete"},
		{outcome: "failed", cause: errors.New("migration failed")},
		{outcome: "outcome_unknown", cause: errors.New("outcome unknown")},
		{outcome: "complete_with_cleanup_error", cause: errors.New("cleanup failed")},
	}
	for _, terminal := range terminals {
		t.Run(terminal.outcome, func(t *testing.T) {
			session := newStatusStateSession(t)
			if err := session.finish(terminal.outcome, terminal.cause); err == nil {
				t.Fatal("terminal status succeeded without in-progress evidence")
			}
			if _, err := os.Lstat(session.root + backupStatusSuffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected terminal status created evidence: %v", err)
			}
			if err := session.finish("migration_in_progress", nil); err != nil {
				t.Fatal(err)
			}
			inProgress, exists, statusErr := session.readMigrationStatus()
			if statusErr != nil || !exists || inProgress.Revision != 1 ||
				inProgress.Outcome != "migration_in_progress" {
				t.Fatalf("in-progress status = %#v, %t, %v", inProgress, exists, statusErr)
			}
			if repeatErr := session.finish("migration_in_progress", nil); repeatErr == nil {
				t.Fatal("in-progress status was initialized twice")
			}
			if terminalErr := session.finish(terminal.outcome, terminal.cause); terminalErr != nil {
				t.Fatal(terminalErr)
			}
			committed, exists, committedErr := session.readMigrationStatus()
			if committedErr != nil || !exists || committed.Revision != 2 ||
				committed.Outcome != terminal.outcome ||
				(terminal.cause != nil) != (committed.Error != "") {
				t.Fatalf("terminal status = %#v, %t, %v", committed, exists, committedErr)
			}
			before, beforeErr := os.ReadFile(session.root + backupStatusSuffix)
			if beforeErr != nil {
				t.Fatal(beforeErr)
			}
			if overwriteErr := session.finish("complete", nil); overwriteErr == nil {
				t.Fatal("terminal status was overwritten")
			}
			after, afterErr := os.ReadFile(session.root + backupStatusSuffix)
			if afterErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected overwrite changed terminal evidence: %v", afterErr)
			}
		})
	}
}

func TestMigrationStatusRejectsStaleIdentityAndConcurrentTerminalWriters(t *testing.T) {
	session := newStatusStateSession(t)
	if err := session.finish("migration_in_progress", nil); err != nil {
		t.Fatal(err)
	}
	stale, loadErr := loadBackupSession(t.Context(), session.root)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if terminalErr := session.finish("complete", nil); terminalErr != nil {
		t.Fatal(terminalErr)
	}
	committed, committedErr := os.ReadFile(session.root + backupStatusSuffix)
	if committedErr != nil {
		t.Fatal(committedErr)
	}
	if staleErr := stale.finish("failed", errors.New("stale writer")); staleErr == nil {
		t.Fatal("stale loaded session overwrote terminal evidence")
	}
	unchanged, unchangedErr := os.ReadFile(session.root + backupStatusSuffix)
	if unchangedErr != nil || !bytes.Equal(committed, unchanged) {
		t.Fatalf("stale writer changed terminal evidence: %v", unchangedErr)
	}

	replaced := newStatusStateSession(t)
	if initializeErr := replaced.finish("migration_in_progress", nil); initializeErr != nil {
		t.Fatal(initializeErr)
	}
	path := replaced.root + backupStatusSuffix
	payload, payloadErr := os.ReadFile(path)
	if payloadErr != nil {
		t.Fatal(payloadErr)
	}
	if removeErr := os.Remove(path); removeErr != nil {
		t.Fatal(removeErr)
	}
	if writeErr := os.WriteFile(path, payload, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if replaceErr := replaced.finish("complete", nil); replaceErr == nil ||
		!strings.Contains(replaceErr.Error(), "identity") {
		t.Fatalf("identity-replaced in-progress status update = %v", replaceErr)
	}
	remaining, remainingErr := os.ReadFile(path)
	if remainingErr != nil || !bytes.Equal(payload, remaining) {
		t.Fatalf("identity-replaced status was changed: %v", remainingErr)
	}
}

func TestMigrationStatusExclusiveCreateAndReplacementPreconditions(t *testing.T) {
	t.Run("exclusive create race", func(t *testing.T) {
		session := newStatusStateSession(t)
		ops := defaultBackupFinishOps()
		createStatus := ops.createStatus
		var competitor []byte
		ops.createStatus = func(
			session *backupSession, path string, payload []byte,
		) (fileidentity.Identity, error) {
			competitor = append([]byte{}, payload...)
			if err := writePrivateBackupFileExclusive(path, competitor, 0o600); err != nil {
				return fileidentity.Identity{}, err
			}
			return createStatus(session, path, payload)
		}
		if err := session.finishWithOps("migration_in_progress", nil, ops); err == nil {
			t.Fatal("exclusive-create race passed")
		}
		got, err := os.ReadFile(session.root + backupStatusSuffix)
		if err != nil || !bytes.Equal(got, competitor) {
			t.Fatalf("exclusive-create race overwrote contender: %v", err)
		}
	})

	t.Run("replacement payload race", func(t *testing.T) {
		session := newStatusStateSession(t)
		if err := session.finish("migration_in_progress", nil); err != nil {
			t.Fatal(err)
		}
		ops := defaultBackupFinishOps()
		replaceStatus := ops.replaceStatus
		var competitor []byte
		ops.replaceStatus = func(
			session *backupSession,
			path string,
			payload []byte,
			identity fileidentity.Identity,
			expected []byte,
		) (fileidentity.Identity, error) {
			competitor = append(append([]byte{}, expected...), ' ')
			if err := os.WriteFile(path, competitor, 0o600); err != nil {
				return fileidentity.Identity{}, err
			}
			return replaceStatus(session, path, payload, identity, expected)
		}
		if err := session.finishWithOps("complete", nil, ops); err == nil {
			t.Fatal("changed replacement precondition passed")
		}
		got, err := os.ReadFile(session.root + backupStatusSuffix)
		if err != nil || !bytes.Equal(got, competitor) {
			t.Fatalf("failed CAS replaced contender: %v", err)
		}
	})
}

func TestMigrationStatusConcurrentTerminalCallsCommitExactlyOne(t *testing.T) {
	session := newStatusStateSession(t)
	if err := session.finish("migration_in_progress", nil); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(1)
	for _, outcome := range []string{"complete", "dry_run"} {
		go func() {
			start.Wait()
			results <- session.finish(outcome, nil)
		}()
	}
	start.Done()
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("terminal successes = %d, want 1", successes)
	}
	status, exists, err := session.readMigrationStatus()
	if err != nil || !exists || status.Revision != 2 ||
		status.Outcome != "complete" && status.Outcome != "dry_run" {
		t.Fatalf("concurrent terminal status = %#v, %t, %v", status, exists, err)
	}
}

func TestSnapshotRejectsPreexistingSiblingStatusNamespace(t *testing.T) {
	home := migrationHome(t)
	parent := filepath.Join(home, "backups")
	if err := ensurePrivateBackupDirectory(parent); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 9, 9, 9, 30, 0, 0, time.UTC)
	root := filepath.Join(parent, "database-migrate-"+fixed.Format("20060102T150405.000000000Z"))
	statusPath := root + backupStatusSuffix
	stale := []byte("unrelated status")
	if err := os.WriteFile(statusPath, stale, 0o600); err != nil {
		t.Fatal(err)
	}
	spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	if session, err := snapshotBackup(
		t.Context(), func() time.Time { return fixed }, home,
		[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, parent,
	); session != nil || err == nil {
		t.Fatalf("snapshot with stale status = %#v, %v", session, err)
	}
	got, err := os.ReadFile(statusPath)
	if err != nil || !bytes.Equal(got, stale) {
		t.Fatalf("snapshot changed stale status namespace: %v", err)
	}
}

func TestSnapshotRejectsSiblingStatusAppearingDuringPublication(t *testing.T) {
	home := migrationHome(t)
	parent := filepath.Join(home, "backups")
	if err := ensurePrivateBackupDirectory(parent); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 9, 9, 9, 31, 0, 0, time.UTC)
	root := filepath.Join(parent, "database-migrate-"+fixed.Format("20060102T150405.000000000Z"))
	statusPath := root + backupStatusSuffix
	stale := []byte("raced status")
	operations := defaultBackupSnapshotOps()
	publish := operations.rename
	operations.rename = func(source, target string) error {
		if err := publish(source, target); err != nil {
			return err
		}
		return os.WriteFile(statusPath, stale, 0o600)
	}
	spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	if session, err := snapshotBackupWithOps(
		t.Context(), func() time.Time { return fixed }, home,
		[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, parent, operations,
	); session != nil || err == nil {
		t.Fatalf("snapshot with raced status = %#v, %v", session, err)
	}
	got, err := os.ReadFile(statusPath)
	if err != nil || !bytes.Equal(got, stale) {
		t.Fatalf("snapshot changed raced status namespace: %v", err)
	}
	if loaded, loadErr := loadBackupSession(t.Context(), root); loaded != nil || loadErr == nil {
		t.Fatalf("raced malformed status loaded = %#v, %v", loaded, loadErr)
	}
}

func TestMigrationStatusReadRequiresCompleteDurableArchive(t *testing.T) {
	for _, target := range []string{"manifest", "marker", "payload"} {
		t.Run(target, func(t *testing.T) {
			home := migrationHome(t)
			spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
			writeMigrationFile(t, spec.Path, []byte("database"))
			session, err := snapshotBackup(
				t.Context(), time.Now, home,
				[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := session.finish("migration_in_progress", nil); err != nil {
				t.Fatal(err)
			}
			removePath := filepath.Join(session.root, backupManifestName)
			switch target {
			case "marker":
				removePath = filepath.Join(session.root, backupManifestHash)
			case "payload":
				if len(session.manifest.Files) != 1 {
					t.Fatalf("manifest files = %#v", session.manifest.Files)
				}
				removePath = filepath.Join(
					session.root, filepath.FromSlash(session.manifest.Files[0].Backup),
				)
			}
			if err := os.Remove(removePath); err != nil {
				t.Fatal(err)
			}
			if status, exists, err := session.readMigrationStatus(); err == nil || exists {
				t.Fatalf("status from incomplete archive = %#v, %t, %v", status, exists, err)
			}
		})
	}
}

func TestMigrationStatusBoundsWorstCaseJSONExpansion(t *testing.T) {
	for _, message := range []string{
		strings.Repeat("\x01", backupMaxErrorBytes),
		strings.Repeat("<", backupMaxErrorBytes),
		strings.Repeat("\\", backupMaxErrorBytes),
	} {
		session := newStatusStateSession(t)
		if err := session.finish("migration_in_progress", nil); err != nil {
			t.Fatal(err)
		}
		if err := session.finish("failed", errors.New(message)); err != nil {
			t.Fatal(err)
		}
		payload, err := os.ReadFile(session.root + backupStatusSuffix)
		if err != nil || int64(len(payload)) > backupMaxStatusSize {
			t.Fatalf("expanded status bytes = %d, %v", len(payload), err)
		}
		status, exists, err := session.readMigrationStatus()
		if err != nil || !exists || status.Error != message {
			t.Fatalf("expanded status = %#v, %t, %v", status, exists, err)
		}
	}
}

func TestMigrationStatusCrashEvidenceRemainsRecoverable(t *testing.T) {
	canary := errors.New("status sync canary")
	t.Run("orphan staged bytes leave final absent", func(t *testing.T) {
		session := newStatusStateSession(t)
		digest, err := session.committedStatusDigestWithOps(defaultBackupFinishOps())
		if err != nil {
			t.Fatal(err)
		}
		payload, err := marshalBackupStatus(backupMigrationStatus{
			Version: backupStatusVersion, SnapshotDigest: digest,
			Revision: 1, Outcome: "migration_in_progress",
		})
		if err != nil {
			t.Fatal(err)
		}
		staged, err := stageBackupMigrationStatus(session, payload)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = staged.remove() })
		if writeErr := os.WriteFile(
			filepath.Join(session.parent, ".database-migration-status-crash"),
			[]byte("partial"),
			0o600,
		); writeErr != nil {
			t.Fatal(writeErr)
		}
		loaded, err := loadBackupSession(t.Context(), session.root)
		if err != nil {
			t.Fatal(err)
		}
		if status, exists, err := loaded.readMigrationStatus(); err != nil || exists {
			t.Fatalf("orphan staged status = %#v, %t, %v", status, exists, err)
		}
	})

	t.Run("revision one parent sync failure", func(t *testing.T) {
		session := newStatusStateSession(t)
		ops := defaultBackupFinishOps()
		ops.syncDir = func(string) error { return canary }
		if err := session.finishWithOps("migration_in_progress", nil, ops); !errors.Is(err, canary) {
			t.Fatalf("revision-one sync failure = %v", err)
		}
		loaded, err := loadBackupSession(t.Context(), session.root)
		if err != nil {
			t.Fatal(err)
		}
		status, exists, err := loaded.readMigrationStatus()
		if err != nil || !exists || status.Revision != 1 {
			t.Fatalf("recovered revision one = %#v, %t, %v", status, exists, err)
		}
	})

	t.Run("terminal parent sync failure", func(t *testing.T) {
		session := newStatusStateSession(t)
		if err := session.finish("migration_in_progress", nil); err != nil {
			t.Fatal(err)
		}
		ops := defaultBackupFinishOps()
		ops.syncDir = func(string) error { return canary }
		if err := session.finishWithOps("complete", nil, ops); !errors.Is(err, canary) {
			t.Fatalf("terminal sync failure = %v", err)
		}
		loaded, err := loadBackupSession(t.Context(), session.root)
		if err != nil {
			t.Fatal(err)
		}
		status, exists, err := loaded.readMigrationStatus()
		if err != nil || !exists || status.Revision != 2 || status.Outcome != "complete" {
			t.Fatalf("recovered terminal = %#v, %t, %v", status, exists, err)
		}
	})
}
