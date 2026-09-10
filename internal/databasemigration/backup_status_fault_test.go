package databasemigration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func TestBackupStatusHighLevelFaultsPreserveMonotonicEvidence(t *testing.T) {
	canary := errors.New("backup status fault canary")
	for _, session := range []*backupSession{nil, {}} {
		if err := session.finishWithOps(
			"migration_in_progress", nil, defaultBackupFinishOps(),
		); err == nil || !strings.Contains(err.Error(), "session is unavailable") {
			t.Fatalf("unavailable status session finish = %v", err)
		}
	}
	t.Run("unavailable operations", func(t *testing.T) {
		session := newStatusStateSession(t)
		operations := defaultBackupFinishOps()
		operations.createStatus = nil
		if err := session.finishMigrationStatusWithOps(
			"migration_in_progress", nil, operations,
		); err == nil {
			t.Fatal("nil status operation passed")
		}
	})

	t.Run("invalid status namespace", func(t *testing.T) {
		session := newStatusStateSession(t)
		session.root = "relative"
		if err := session.finishMigrationStatusWithOps(
			"migration_in_progress", nil, defaultBackupFinishOps(),
		); err == nil {
			t.Fatal("invalid status namespace passed")
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*backupFinishOps)
	}{
		{name: "archive verification", mutate: func(operations *backupFinishOps) {
			operations.verify = func(_ context.Context, _ *backupSession) error { return canary }
		}},
		{name: "current status read", mutate: func(operations *backupFinishOps) {
			operations.readStatus = func(
				string, int64,
			) ([]byte, fileidentity.Identity, bool, error) {
				return nil, fileidentity.Identity{}, false, canary
			}
		}},
		{name: "status marshal", mutate: func(operations *backupFinishOps) {
			operations.marshalStatus = func(backupMigrationStatus) ([]byte, error) {
				return nil, canary
			}
		}},
		{name: "exclusive create", mutate: func(operations *backupFinishOps) {
			operations.createStatus = func(
				*backupSession, string, []byte,
			) (fileidentity.Identity, error) {
				return fileidentity.Identity{}, canary
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := newStatusStateSession(t)
			operations := defaultBackupFinishOps()
			test.mutate(&operations)
			if err := session.finishMigrationStatusWithOps(
				"migration_in_progress", nil, operations,
			); !errors.Is(err, canary) {
				t.Fatalf("fault result = %v", err)
			}
		})
	}

	t.Run("post-create observation", func(t *testing.T) {
		session := newStatusStateSession(t)
		operations := defaultBackupFinishOps()
		readStatus := operations.readStatus
		calls := 0
		operations.readStatus = func(
			path string, limit int64,
		) ([]byte, fileidentity.Identity, bool, error) {
			calls++
			if calls == 2 {
				return nil, fileidentity.Identity{}, false, canary
			}
			return readStatus(path, limit)
		}
		if err := session.finishMigrationStatusWithOps(
			"migration_in_progress", nil, operations,
		); !errors.Is(err, canary) {
			t.Fatalf("observation fault = %v", err)
		}
		loaded, err := loadBackupSession(t.Context(), session.root)
		if err != nil {
			t.Fatal(err)
		}
		status, exists, err := loaded.readMigrationStatus()
		if err != nil || !exists || status.Revision != 1 {
			t.Fatalf("durable status after observation fault = %#v, %t, %v", status, exists, err)
		}
	})

	t.Run("invalid installed identity", func(t *testing.T) {
		session := newStatusStateSession(t)
		operations := defaultBackupFinishOps()
		createStatus := operations.createStatus
		operations.createStatus = func(
			session *backupSession, path string, payload []byte,
		) (fileidentity.Identity, error) {
			_, err := createStatus(session, path, payload)
			return fileidentity.Identity{}, err
		}
		if err := session.finishMigrationStatusWithOps(
			"migration_in_progress", nil, operations,
		); err == nil {
			t.Fatal("invalid installed identity passed")
		}
	})

	t.Run("final archive verification", func(t *testing.T) {
		session := newStatusStateSession(t)
		operations := defaultBackupFinishOps()
		verify := operations.verify
		calls := 0
		operations.verify = func(ctx context.Context, session *backupSession) error {
			calls++
			if calls == 2 {
				return canary
			}
			return verify(ctx, session)
		}
		if err := session.finishMigrationStatusWithOps(
			"migration_in_progress", nil, operations,
		); !errors.Is(err, canary) {
			t.Fatalf("final verification fault = %v", err)
		}
	})

	t.Run("terminal replacement", func(t *testing.T) {
		session := newStatusStateSession(t)
		if err := session.finish("migration_in_progress", nil); err != nil {
			t.Fatal(err)
		}
		operations := defaultBackupFinishOps()
		operations.replaceStatus = func(
			*backupSession, string, []byte, fileidentity.Identity, []byte,
		) (fileidentity.Identity, error) {
			return fileidentity.Identity{}, canary
		}
		if err := session.finishMigrationStatusWithOps(
			"complete", nil, operations,
		); !errors.Is(err, canary) {
			t.Fatalf("replacement fault = %v", err)
		}
		status, exists, err := session.readMigrationStatus()
		if err != nil || !exists || status.Revision != 1 {
			t.Fatalf("replacement fault changed incumbent = %#v, %t, %v", status, exists, err)
		}
	})
}

func TestBackupStatusArchiveAndRecordFaultBoundaries(t *testing.T) {
	canary := errors.New("backup status archive fault canary")
	if _, err := (&backupSession{}).verifyStatusArchiveWithOps(defaultBackupFinishOps()); err == nil {
		t.Fatal("empty status archive session passed")
	}

	t.Run("parent before verification", func(t *testing.T) {
		session := newStatusStateSession(t)
		if err := os.Chmod(session.parent, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(session.parent, 0o700) })
		if _, err := session.verifyStatusArchiveWithOps(defaultBackupFinishOps()); err == nil {
			t.Fatal("public status parent passed")
		}
	})

	t.Run("archive verifier", func(t *testing.T) {
		session := newStatusStateSession(t)
		operations := defaultBackupFinishOps()
		operations.verify = func(context.Context, *backupSession) error { return canary }
		if _, err := session.verifyStatusArchiveWithOps(operations); !errors.Is(err, canary) {
			t.Fatalf("archive verifier fault = %v", err)
		}
	})

	t.Run("root identity", func(t *testing.T) {
		session := newStatusStateSession(t)
		other := t.TempDir()
		identity, objectType, exists, err := fileidentity.ExistingWithType(other)
		if err != nil || !exists || objectType != fileidentity.ObjectTypeDirectory {
			t.Fatal(err)
		}
		session.identity = identity
		operations := defaultBackupFinishOps()
		operations.verify = func(context.Context, *backupSession) error { return nil }
		if _, err := session.verifyStatusArchiveWithOps(operations); err == nil {
			t.Fatal("wrong archive identity passed")
		}
	})

	t.Run("parent after digest", func(t *testing.T) {
		session := newStatusStateSession(t)
		operations := defaultBackupFinishOps()
		read := operations.read
		operations.read = func(path string, limit int64) ([]byte, error) {
			payload, err := read(path, limit)
			if err == nil && filepath.Base(path) == backupManifestHash {
				if chmodErr := os.Chmod(session.parent, 0o755); chmodErr != nil {
					return nil, chmodErr
				}
			}
			return payload, err
		}
		t.Cleanup(func() { _ = os.Chmod(session.parent, 0o700) })
		if _, err := session.verifyStatusArchiveWithOps(operations); err == nil {
			t.Fatal("post-digest parent drift passed")
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*backupFinishOps)
	}{
		{name: "manifest read", mutate: func(operations *backupFinishOps) {
			operations.read = func(string, int64) ([]byte, error) { return nil, canary }
		}},
		{name: "manifest marshal", mutate: func(operations *backupFinishOps) {
			operations.marshal = func(BackupManifest) ([]byte, error) { return nil, canary }
		}},
		{name: "durable manifest mismatch", mutate: func(operations *backupFinishOps) {
			read := operations.read
			operations.read = func(path string, limit int64) ([]byte, error) {
				payload, err := read(path, limit)
				if filepath.Base(path) == backupManifestName {
					payload = append(append([]byte{}, payload...), ' ')
				}
				return payload, err
			}
		}},
		{name: "marker read", mutate: func(operations *backupFinishOps) {
			read := operations.read
			operations.read = func(path string, limit int64) ([]byte, error) {
				if filepath.Base(path) == backupManifestHash {
					return nil, canary
				}
				return read(path, limit)
			}
		}},
		{name: "marker mismatch", mutate: func(operations *backupFinishOps) {
			read := operations.read
			operations.read = func(path string, limit int64) ([]byte, error) {
				if filepath.Base(path) == backupManifestHash {
					return []byte(strings.Repeat("0", sha256.Size*2) + "\n"), nil
				}
				return read(path, limit)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := newStatusStateSession(t)
			operations := defaultBackupFinishOps()
			test.mutate(&operations)
			if _, err := session.committedStatusDigestWithOps(operations); err == nil {
				t.Fatal("faulted durable digest passed")
			}
		})
	}

	t.Run("record identity", func(t *testing.T) {
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
		operations := defaultBackupFinishOps()
		operations.readStatus = func(
			string, int64,
		) ([]byte, fileidentity.Identity, bool, error) {
			return payload, fileidentity.Identity{}, true, nil
		}
		if _, _, _, exists, err := session.readStatusRecordWithOps(
			session.root+backupStatusSuffix, digest, operations,
		); err == nil || exists {
			t.Fatalf("identity-free status record = exists:%t error:%v", exists, err)
		}
	})

	t.Run("disappeared pinned state", func(t *testing.T) {
		session := newStatusStateSession(t)
		if err := session.finish("migration_in_progress", nil); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(session.root + backupStatusSuffix); err != nil {
			t.Fatal(err)
		}
		if _, _, err := session.readMigrationStatus(); err == nil {
			t.Fatal("disappeared pinned status passed")
		}
	})
}

func TestBackupStatusPinnedReadAndStageBoundaries(t *testing.T) {
	if payload, identity, exists, err := readPinnedBackupStatus("relative", 1); err == nil ||
		payload != nil || identity.Valid() || exists {
		t.Fatalf("relative status read = %q, %v, %t, %v", payload, identity, exists, err)
	}
	if _, _, _, err := readPinnedBackupStatus(filepath.Join(t.TempDir(), "missing"), 0); err == nil {
		t.Fatal("zero status read limit passed")
	}
	root := t.TempDir()
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readPinnedBackupStatus(directory, backupMaxStatusSize); err == nil {
		t.Fatal("directory status passed")
	}
	public := filepath.Join(root, "public")
	if err := os.WriteFile(public, []byte("status"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readPinnedBackupStatus(public, backupMaxStatusSize); err == nil {
		t.Fatal("public status passed")
	}
	hardlink := filepath.Join(root, "hardlink")
	private := filepath.Join(root, "private")
	if err := os.WriteFile(private, []byte("status"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(private, hardlink); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, _, _, err := readPinnedBackupStatus(private, backupMaxStatusSize); err == nil {
		t.Fatal("hard-linked status passed")
	}

	if _, _, err := readLockedBackupStatus(nil, private, nil, 1); err == nil {
		t.Fatal("nil locked status passed")
	}
	closed, openErr := os.Open(private)
	if openErr != nil {
		t.Fatal(openErr)
	}
	closedInfo, statErr := closed.Stat()
	if statErr != nil {
		t.Fatal(statErr)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readLockedBackupStatus(
		closed, private, closedInfo, backupMaxStatusSize,
	); err == nil {
		t.Fatal("closed locked status passed")
	}

	session := newStatusStateSession(t)
	if staged, err := stageBackupMigrationStatus(nil, []byte("status")); staged != nil || err == nil {
		t.Fatalf("nil staged status = %#v, %v", staged, err)
	}
	if staged, err := stageBackupMigrationStatus(session, nil); staged != nil || err == nil {
		t.Fatalf("empty staged status = %#v, %v", staged, err)
	}
	if staged, err := stageBackupMigrationStatus(
		session, make([]byte, backupMaxStatusSize+1),
	); staged != nil || err == nil {
		t.Fatalf("oversized staged status = %#v, %v", staged, err)
	}
	if err := (*stagedBackupStatus)(nil).remove(); err == nil {
		t.Fatal("nil staged status removal passed")
	}
	if _, err := createInitialBackupMigrationStatus(nil, "", nil); err == nil {
		t.Fatal("nil initial status session passed")
	}
	if _, err := createInitialBackupMigrationStatus(session, "relative", []byte("status")); err == nil {
		t.Fatal("relative initial status path passed")
	}
	if _, err := createInitialBackupMigrationStatus(
		session, session.root+backupStatusSuffix, make([]byte, backupMaxStatusSize+1),
	); err == nil {
		t.Fatal("oversized initial status passed")
	}
	if err := os.WriteFile(session.root+backupStatusSuffix, []byte("incumbent"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := createInitialBackupMigrationStatus(
		session, session.root+backupStatusSuffix, []byte("replacement"),
	); err == nil {
		t.Fatal("initial status overwrote incumbent")
	}
	incumbent, readErr := os.ReadFile(session.root + backupStatusSuffix)
	if readErr != nil || !bytes.Equal(incumbent, []byte("incumbent")) {
		t.Fatalf("initial-status collision changed incumbent: %q, %v", incumbent, readErr)
	}
}

func TestBackupStatusReplacementDefensiveBoundaries(t *testing.T) {
	if _, err := replaceBackupMigrationStatus(nil, "", nil, fileidentity.Identity{}, nil); err == nil {
		t.Fatal("nil status replacement passed")
	}
	session := newStatusStateSession(t)
	if err := session.finish("migration_in_progress", nil); err != nil {
		t.Fatal(err)
	}
	statusPath := session.root + backupStatusSuffix
	incumbent, identity, exists, err := readPinnedBackupStatus(statusPath, backupMaxStatusSize)
	if err != nil || !exists {
		t.Fatal(err)
	}
	if _, err := replaceBackupMigrationStatus(
		session, "relative", []byte("replacement"), identity, incumbent,
	); err == nil {
		t.Fatal("relative status replacement passed")
	}
	if _, err := replaceBackupMigrationStatus(
		session, statusPath, []byte("replacement"), identity, []byte("wrong"),
	); err == nil {
		t.Fatal("wrong expected status bytes passed")
	}
	if _, err := replaceBackupMigrationStatus(
		session, statusPath, make([]byte, backupMaxStatusSize+1), identity, incumbent,
	); err == nil {
		t.Fatal("oversized replacement status passed")
	}
	if err := os.Chmod(session.parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := replaceBackupMigrationStatus(
		session, statusPath, []byte("replacement"), identity, incumbent,
	); err == nil {
		t.Fatal("replacement under public parent passed")
	}
	if err := os.Chmod(session.parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(statusPath); err != nil {
		t.Fatal(err)
	}
	if _, err := replaceBackupMigrationStatus(
		session, statusPath, []byte("replacement"), identity, incumbent,
	); err == nil {
		t.Fatal("replacement of missing status passed")
	}
}

func TestBackupStatusFinalDigestDriftIsRejected(t *testing.T) {
	session := newStatusStateSession(t)
	operations := defaultBackupFinishOps()
	originalManifest, err := operations.marshal(session.manifest)
	if err != nil {
		t.Fatal(err)
	}
	alternateManifest := append(append([]byte{}, originalManifest...), '\n')
	digests := [2]string{}
	for index, payload := range [][]byte{originalManifest, alternateManifest} {
		digest := sha256.Sum256(payload)
		digests[index] = hex.EncodeToString(digest[:])
	}
	manifestCalls := 0
	operations.marshal = func(BackupManifest) ([]byte, error) {
		manifestCalls++
		if manifestCalls == 1 {
			return originalManifest, nil
		}
		return alternateManifest, nil
	}
	readCalls := 0
	operations.read = func(path string, _ int64) ([]byte, error) {
		phase := readCalls / 2
		readCalls++
		if filepath.Base(path) == backupManifestName {
			if phase == 0 {
				return originalManifest, nil
			}
			return alternateManifest, nil
		}
		return []byte(digests[phase] + "\n"), nil
	}
	operations.verify = func(context.Context, *backupSession) error { return nil }
	if err := session.finishMigrationStatusWithOps(
		"migration_in_progress", nil, operations,
	); err == nil || !strings.Contains(err.Error(), "digest changed") {
		t.Fatalf("final digest drift = %v", err)
	}
}

func TestBackupStatusAdditionalFilesystemFailureShapes(t *testing.T) {
	t.Run("status suffix exceeds path bound", func(t *testing.T) {
		root := t.TempDir()
		for len(root)+2 <= backupMaxPathBytes-1 {
			root = filepath.Join(root, "a")
		}
		parent := filepath.Dir(root)
		if !validBackupAbsolutePath(root) || !validBackupAbsolutePath(parent) {
			t.Fatalf("constructed path is not a valid pre-suffix boundary: %d", len(root))
		}
		if path, err := backupMigrationStatusPath(root, parent); path != "" || err == nil {
			t.Fatalf("overlong status path = %q, %v", path, err)
		}
		identity, objectType, exists, err := fileidentity.ExistingWithType(t.TempDir())
		if err != nil || !exists || objectType != fileidentity.ObjectTypeDirectory {
			t.Fatal(err)
		}
		session := &backupSession{
			root: root, parent: parent, identity: identity, parentIdentity: identity,
		}
		if _, _, err := session.readMigrationStatusWithOps(defaultBackupFinishOps()); err == nil {
			t.Fatal("overlong status path read passed")
		}
	})

	t.Run("symlinked ancestor", func(t *testing.T) {
		realParent := t.TempDir()
		path := filepath.Join(realParent, "status")
		if err := os.WriteFile(path, []byte("status"), 0o600); err != nil {
			t.Fatal(err)
		}
		aliasParent := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(realParent, aliasParent); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, _, _, err := readPinnedBackupStatus(
			filepath.Join(aliasParent, "status"), backupMaxStatusSize,
		); err == nil {
			t.Fatal("status below symlinked ancestor passed")
		}
	})

	t.Run("locked handle metadata and read failures", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "status")
		if err := os.WriteFile(path, []byte("status"), 0o600); err != nil {
			t.Fatal(err)
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			t.Fatal(openErr)
		}
		info, statErr := file.Stat()
		if statErr != nil {
			t.Fatal(statErr)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readLockedBackupStatus(
			file, path, info, backupMaxStatusSize,
		); err == nil {
			t.Fatal("locked status metadata drift passed")
		}
		_ = file.Close()

		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(root, "alias")
		if err := os.Link(path, alias); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		file, openErr = os.Open(path)
		if openErr != nil {
			t.Fatal(openErr)
		}
		info, statErr = file.Stat()
		if statErr != nil {
			t.Fatal(statErr)
		}
		if _, _, err := readLockedBackupStatus(
			file, path, info, backupMaxStatusSize,
		); err == nil {
			t.Fatal("locked hard-linked status passed")
		}
		_ = file.Close()
		if err := os.Remove(alias); err != nil {
			t.Fatal(err)
		}

		writeOnly, writeOpenErr := os.OpenFile(path, os.O_WRONLY, 0)
		if writeOpenErr != nil {
			t.Fatal(writeOpenErr)
		}
		info, statErr = writeOnly.Stat()
		if statErr != nil {
			t.Fatal(statErr)
		}
		if _, _, err := readLockedBackupStatus(
			writeOnly, path, info, backupMaxStatusSize,
		); err == nil {
			t.Fatal("write-only locked status read passed")
		}
		_ = writeOnly.Close()
	})

	t.Run("staging rejects parent metadata and creation failures", func(t *testing.T) {
		session := newStatusStateSession(t)
		if err := os.Chmod(session.parent, 0o755); err != nil {
			t.Fatal(err)
		}
		if staged, err := stageBackupMigrationStatus(
			session, []byte("status"),
		); staged != nil || err == nil {
			t.Fatalf("stage under public parent = %#v, %v", staged, err)
		}
		if err := os.Chmod(session.parent, 0o500); err != nil {
			t.Fatal(err)
		}
		if staged, err := stageBackupMigrationStatus(
			session, []byte("status"),
		); staged != nil || err == nil {
			t.Fatalf("stage under read-only parent = %#v, %v", staged, err)
		}
		if err := os.Chmod(session.parent, 0o700); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("incumbent handle lock", func(t *testing.T) {
		session := newStatusStateSession(t)
		if err := session.finish("migration_in_progress", nil); err != nil {
			t.Fatal(err)
		}
		path := session.root + backupStatusSuffix
		payload, identity, exists, err := readPinnedBackupStatus(path, backupMaxStatusSize)
		if err != nil || !exists {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		unlock, err := lockBackupStatusHandle(file)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		if _, err := replaceBackupMigrationStatus(
			session, path, []byte("replacement"), identity, payload,
		); err == nil {
			t.Fatal("replacement ignored incumbent handle lock")
		}
	})
}

func TestBackupStatusReadFinalDigestDriftIsRejected(t *testing.T) {
	session := newStatusStateSession(t)
	operations := defaultBackupFinishOps()
	originalManifest, err := operations.marshal(session.manifest)
	if err != nil {
		t.Fatal(err)
	}
	alternateManifest := append(append([]byte{}, originalManifest...), '\n')
	digests := [2]string{}
	for index, payload := range [][]byte{originalManifest, alternateManifest} {
		digest := sha256.Sum256(payload)
		digests[index] = hex.EncodeToString(digest[:])
	}
	manifestCalls := 0
	operations.marshal = func(BackupManifest) ([]byte, error) {
		manifestCalls++
		if manifestCalls == 1 {
			return originalManifest, nil
		}
		return alternateManifest, nil
	}
	readCalls := 0
	operations.read = func(path string, _ int64) ([]byte, error) {
		phase := readCalls / 2
		readCalls++
		if filepath.Base(path) == backupManifestName {
			if phase == 0 {
				return originalManifest, nil
			}
			return alternateManifest, nil
		}
		return []byte(digests[phase] + "\n"), nil
	}
	operations.verify = func(context.Context, *backupSession) error { return nil }
	if _, _, err := session.readMigrationStatusWithOps(operations); err == nil ||
		!strings.Contains(err.Error(), "changed during") {
		t.Fatalf("status read final digest drift = %v", err)
	}
}

func TestBackupStatusReadOperationFaults(t *testing.T) {
	canary := errors.New("backup status read operation canary")
	for _, test := range []struct {
		name   string
		mutate func(*backupStatusReadOps)
	}{
		{
			name: "opened identity",
			mutate: func(operations *backupStatusReadOps) {
				operations.openedIdentity = func(
					*os.File, fileidentity.ObjectType,
				) (fileidentity.Identity, error) {
					return fileidentity.Identity{}, canary
				}
			},
		},
		{
			name: "opened metadata",
			mutate: func(operations *backupStatusReadOps) {
				validate := operations.validatePrivateFile
				calls := 0
				operations.validatePrivateFile = func(path string, info os.FileInfo) error {
					calls++
					if calls == 2 {
						return canary
					}
					return validate(path, info)
				}
			},
		},
		{
			name: "payload read",
			mutate: func(operations *backupStatusReadOps) {
				operations.readAll = func(io.Reader) ([]byte, error) { return nil, canary }
			},
		},
		{
			name: "final handle stat",
			mutate: func(operations *backupStatusReadOps) {
				operations.stat = func(*os.File) (os.FileInfo, error) { return nil, canary }
			},
		},
	} {
		t.Run("pinned "+test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "status")
			if err := os.WriteFile(path, []byte("status"), 0o600); err != nil {
				t.Fatal(err)
			}
			operations := defaultBackupStatusReadOps()
			openPinned := operations.openPinned
			var captured *os.File
			operations.openPinned = func(
				path string, directory bool,
			) (*os.File, os.FileInfo, error) {
				file, info, err := openPinned(path, directory)
				captured = file
				return file, info, err
			}
			test.mutate(&operations)
			if _, _, _, err := readPinnedBackupStatusWithOps(
				path, backupMaxStatusSize, operations,
			); !errors.Is(err, canary) {
				t.Fatalf("pinned read fault = %v", err)
			}
			if captured == nil {
				t.Fatal("pinned read did not open its target")
			}
			if _, err := captured.Stat(); err == nil {
				t.Fatal("pinned read fault left its handle open")
			}
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(*backupStatusReadOps)
	}{
		{
			name: "seek",
			mutate: func(operations *backupStatusReadOps) {
				operations.seek = func(*os.File, int64, int) (int64, error) {
					return 0, canary
				}
			},
		},
		{
			name: "final handle stat",
			mutate: func(operations *backupStatusReadOps) {
				operations.stat = func(*os.File) (os.FileInfo, error) { return nil, canary }
			},
		},
		{
			name: "final private metadata",
			mutate: func(operations *backupStatusReadOps) {
				validate := operations.validatePrivateFile
				calls := 0
				operations.validatePrivateFile = func(path string, info os.FileInfo) error {
					calls++
					if calls == 2 {
						return canary
					}
					return validate(path, info)
				}
			},
		},
		{
			name: "final platform metadata",
			mutate: func(operations *backupStatusReadOps) {
				validate := operations.validatePlatformFile
				calls := 0
				operations.validatePlatformFile = func(
					info os.FileInfo, file *os.File, mode uint32,
				) error {
					calls++
					if calls == 2 {
						return canary
					}
					return validate(info, file, mode)
				}
			},
		},
	} {
		t.Run("locked "+test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "status")
			if err := os.WriteFile(path, []byte("status"), 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			info, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			operations := defaultBackupStatusReadOps()
			test.mutate(&operations)
			if _, _, err := readLockedBackupStatusWithOps(
				file, path, info, backupMaxStatusSize, operations,
			); !errors.Is(err, canary) {
				t.Fatalf("locked read fault = %v", err)
			}
		})
	}
}

func TestBackupStatusStageOperationFaultsCleanUpOwnedFiles(t *testing.T) {
	canary := errors.New("backup status stage operation canary")
	trackCleanup := func(operations *backupStatusStageOps, calls *int) {
		removePinned := operations.removePinned
		operations.removePinned = func(path string, identity fileidentity.Identity) error {
			*calls++
			return removePinned(path, identity)
		}
	}
	for _, test := range []struct {
		name               string
		cleanupCalls       int
		placeholderRemains bool
	}{
		{"placeholder stat", 1, false},
		{"placeholder close", 1, false},
		{"placeholder removal", 1, true},
		{"exclusive write", 0, false},
		{"staged readback", 2, false},
		{"final parent validation", 2, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := newStatusStateSession(t)
			operations := defaultBackupStatusStageOps()
			stagedPath := filepath.Join(session.parent, ".database-migration-status-test.partial")
			created := false
			operations.createTemp = func(string, string) (*os.File, error) {
				created = true
				return os.OpenFile(stagedPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
			}
			cleanupCalls := 0
			switch test.name {
			case "placeholder stat":
				operations.stat = func(*os.File) (os.FileInfo, error) { return nil, canary }
			case "placeholder close":
				closePlaceholder := operations.close
				operations.close = func(file *os.File) error { return errors.Join(closePlaceholder(file), canary) }
			case "placeholder removal":
				operations.removePinned = func(_ string, identity fileidentity.Identity) error {
					cleanupCalls++
					if !identity.Valid() {
						t.Fatal("placeholder identity was not captured")
					}
					return canary
				}
			case "exclusive write":
				operations.writeExclusive = func(string, []byte, os.FileMode) error { return canary }
			case "staged readback":
				operations.readStatus = func(string, int64) ([]byte, fileidentity.Identity, bool, error) {
					return nil, fileidentity.Identity{}, false, canary
				}
			case "final parent validation":
				validateParent, calls := operations.validateParent, 0
				operations.validateParent = func(path string, identity fileidentity.Identity) error {
					calls++
					if calls == 2 {
						return canary
					}
					return validateParent(path, identity)
				}
			}
			if test.cleanupCalls > 0 && test.name != "placeholder removal" {
				trackCleanup(&operations, &cleanupCalls)
			}
			if staged, err := stageBackupMigrationStatusWithOps(
				session, []byte("status"), operations,
			); staged != nil || !errors.Is(err, canary) {
				t.Fatalf("faulted stage = %#v, %v", staged, err)
			}
			if !created {
				t.Fatal("faulted stage did not create its placeholder")
			}
			_, statErr := os.Lstat(stagedPath)
			if test.placeholderRemains {
				if statErr != nil {
					t.Fatalf("failed removal lost owned placeholder: %v", statErr)
				}
				t.Cleanup(func() { _ = os.Remove(stagedPath) })
			} else if !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("faulted stage left owned file behind: %v", statErr)
			}
			if cleanupCalls != test.cleanupCalls {
				t.Fatalf(
					"cleanup calls = %d, want %d", cleanupCalls, test.cleanupCalls,
				)
			}
		})
	}
}

func TestBackupStatusPlaceholderRemovalPreservesSubstitute(t *testing.T) {
	parent := migrationHome(t)
	for _, substitute := range []bool{false, true} {
		placeholder, err := os.CreateTemp(parent, ".database-migration-status-*.partial")
		if err != nil {
			t.Fatal(err)
		}
		path, retained := placeholder.Name(), placeholder.Name()+".retained"
		operations := defaultBackupStatusPlaceholderOps()
		if substitute {
			removePinned := operations.removePinned
			operations.removePinned = func(path string, identity fileidentity.Identity) error {
				if renameErr := os.Rename(path, retained); renameErr != nil {
					return renameErr
				}
				if writeErr := os.WriteFile(path, []byte("substitute"), 0o600); writeErr != nil {
					return writeErr
				}
				return removePinned(path, identity)
			}
		}
		err = removeBackupStatusPlaceholderWithOps(path, placeholder, operations)
		if !substitute {
			if err != nil {
				t.Fatal(err)
			}
			if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("removed status placeholder remains: %v", statErr)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "target changed") {
			t.Fatalf("substituted placeholder removal = %v", err)
		}
		for path, want := range map[string]string{path: "substitute", retained: ""} {
			payload, err := os.ReadFile(path)
			if err != nil || string(payload) != want {
				t.Fatalf("preserved placeholder %q = %q, %v; want %q", path, payload, err, want)
			}
			t.Cleanup(func() { _ = os.Remove(path) })
		}
	}
}

func TestBackupStatusReplacementRereadsLockOwningHandle(t *testing.T) {
	parent := migrationHome(t)
	path := filepath.Join(parent, "status")
	writeMigrationFile(t, path, []byte("status"))
	root := mustBackupRemovalRoot(t, parent)
	defer root.Close()
	file := mustBackupRemovalFile(t, root, "status")
	defer file.Close()
	opened := mustMigrationInfo(t, path)
	wantIdentity := mustBackupRemovalIdentity(t, path, fileidentity.ObjectTypeRegular)
	called := false
	payload, identity, err := readLockedBackupStatusForReplacement(
		file, path, opened, backupMaxStatusSize,
		func(gotFile *os.File, gotPath string, gotOpened os.FileInfo, gotLimit int64) (
			[]byte, fileidentity.Identity, error,
		) {
			called = true
			if gotFile != file || gotPath != path || gotOpened != opened ||
				gotLimit != backupMaxStatusSize {
				t.Fatal("replacement reread did not use lock-owning handle and metadata")
			}
			return []byte("status"), wantIdentity, nil
		},
	)
	if err != nil || !called || string(payload) != "status" || identity != wantIdentity {
		t.Fatalf("lock-owning reread = %q, %#v, called=%t, error=%v", payload, identity, called, err)
	}
	if _, _, err := readLockedBackupStatusForReplacement(
		file, path, opened, backupMaxStatusSize, nil,
	); err == nil {
		t.Fatal("nil lock-owning status reader passed")
	}
}

func TestBackupStatusExchangeRollbackRequiresExactDisplacedIncumbent(t *testing.T) {
	expectedIdentity := mustBackupRemovalIdentity(
		t, backupArchiveStatusFixtureFile(t), fileidentity.ObjectTypeRegular,
	)
	stagedIdentity := mustBackupRemovalIdentity(
		t, backupArchiveStatusFixtureFile(t), fileidentity.ObjectTypeRegular,
	)
	staged := &stagedBackupStatus{identity: stagedIdentity, payload: []byte("new")}
	rollbackCalls := 0
	rollback := func() error {
		rollbackCalls++
		return nil
	}
	if err := validateExchangedBackupStatus(
		[]byte("old"), expectedIdentity, staged,
		[]byte("substitute"), expectedIdentity, nil,
		[]byte("new"), stagedIdentity, true, nil, rollback,
	); err == nil || rollbackCalls != 0 {
		t.Fatalf("unproven displaced status rollback = %v, calls=%d", err, rollbackCalls)
	}
	if err := validateExchangedBackupStatus(
		[]byte("old"), expectedIdentity, staged,
		[]byte("old"), expectedIdentity, nil,
		[]byte("unexpected"), stagedIdentity, true, nil, rollback,
	); err == nil || rollbackCalls != 1 {
		t.Fatalf("unproven installed status rollback = %v, calls=%d", err, rollbackCalls)
	}
	if err := validateExchangedBackupStatus(
		[]byte("old"), expectedIdentity, staged,
		[]byte("old"), expectedIdentity, nil,
		[]byte("new"), stagedIdentity, true, nil, rollback,
	); err != nil || rollbackCalls != 1 {
		t.Fatalf("exact status exchange = %v, rollback calls=%d", err, rollbackCalls)
	}
}

func backupArchiveStatusFixtureFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "status")
	writeMigrationFile(t, path, []byte("status"))
	return path
}
