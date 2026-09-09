package databasemigration

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupArchiveLoadRoundTrip(t *testing.T) {
	_, _, session := backupArchiveGenerationSnapshot(t)
	loaded, err := loadBackupSession(nil, session.root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.root != session.root || loaded.identity != session.identity ||
		!bytes.Equal(mustBackupArchiveManifest(t, loaded.manifest), mustBackupArchiveManifest(t, session.manifest)) {
		t.Fatalf("loaded session = %#v, want %#v", loaded, session)
	}
	if err := verifyExactBackupTree(t.Context(), loaded.root, loaded.manifest); err != nil {
		t.Fatal(err)
	}
}

func TestBackupArchiveLoadRejectsMalformedEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *backupSession)
	}{
		{"missing manifest", func(t *testing.T, session *backupSession) {
			mustBackupArchiveOK(t, os.Remove(filepath.Join(session.root, backupManifestName)))
		}},
		{"invalid JSON", func(t *testing.T, session *backupSession) {
			writeMigrationFile(t, filepath.Join(session.root, backupManifestName), []byte("{"))
		}},
		{"trailing JSON", func(t *testing.T, session *backupSession) {
			path := filepath.Join(session.root, backupManifestName)
			payload := mustBackupArchiveRead(t, path)
			writeMigrationFile(t, path, append(payload, []byte("{}")...))
		}},
		{"noncanonical JSON", func(t *testing.T, session *backupSession) {
			path := filepath.Join(session.root, backupManifestName)
			var compact bytes.Buffer
			if err := json.Compact(&compact, mustBackupArchiveRead(t, path)); err != nil {
				t.Fatal(err)
			}
			writeMigrationFile(t, path, compact.Bytes())
		}},
		{"invalid marker", func(t *testing.T, session *backupSession) {
			writeMigrationFile(t, filepath.Join(session.root, backupManifestHash), []byte("bad\n"))
		}},
		{"unexpected member", func(t *testing.T, session *backupSession) {
			writeMigrationFile(t, filepath.Join(session.root, "unexpected"), nil)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, session := backupArchiveGenerationSnapshot(t)
			test.mutate(t, session)
			if loaded, err := loadBackupSession(t.Context(), session.root); loaded != nil || err == nil {
				t.Fatalf("malformed load = %#v, %v", loaded, err)
			}
		})
	}
	if loaded, err := loadBackupSession(t.Context(), "relative"); loaded != nil || err == nil {
		t.Fatalf("relative load = %#v, %v", loaded, err)
	}
	if loaded, err := loadBackupSession(
		t.Context(),
		filepath.Join(t.TempDir(), "x")+backupPartialSuffix,
	); loaded != nil ||
		err == nil {
		t.Fatalf("partial load = %#v, %v", loaded, err)
	}
}

func TestBackupArchiveCommitFaults(t *testing.T) {
	canary := errors.New("archive commit fault")
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *backupSession, *backupCommitOps)
	}{
		{"operations", func(_ *testing.T, _ *backupSession, ops *backupCommitOps) { ops.lstat = nil }},
		{"inspect control", func(_ *testing.T, _ *backupSession, ops *backupCommitOps) {
			ops.lstat = func(string) (os.FileInfo, error) { return nil, canary }
		}},
		{"marshal", func(_ *testing.T, _ *backupSession, ops *backupCommitOps) {
			ops.marshal = func(BackupManifest) ([]byte, error) { return nil, canary }
		}},
		{"manifest write", func(_ *testing.T, _ *backupSession, ops *backupCommitOps) {
			ops.writeExclusive = func(string, []byte, os.FileMode) error { return canary }
		}},
		{"manifest secure", func(_ *testing.T, _ *backupSession, ops *backupCommitOps) {
			ops.secureFile = func(string) error { return canary }
		}},
		{"manifest sync", func(_ *testing.T, _ *backupSession, ops *backupCommitOps) {
			ops.syncDir = func(string) error { return canary }
		}},
		{"marker write", func(_ *testing.T, _ *backupSession, ops *backupCommitOps) {
			write := ops.writeExclusive
			calls := 0
			ops.writeExclusive = func(path string, payload []byte, mode os.FileMode) error {
				calls++
				if calls == 2 {
					return canary
				}
				return write(path, payload, mode)
			}
		}},
		{"marker secure", func(_ *testing.T, _ *backupSession, ops *backupCommitOps) {
			secure := ops.secureFile
			calls := 0
			ops.secureFile = func(path string) error {
				calls++
				if calls == 2 {
					return canary
				}
				return secure(path)
			}
		}},
		{"marker sync", func(_ *testing.T, _ *backupSession, ops *backupCommitOps) {
			syncDir := ops.syncDir
			calls := 0
			ops.syncDir = func(path string) error {
				calls++
				if calls == 2 {
					return canary
				}
				return syncDir(path)
			}
		}},
		{"control collision", func(t *testing.T, session *backupSession, _ *backupCommitOps) {
			writeMigrationFile(t, filepath.Join(session.root, backupManifestName), nil)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := newUncommittedBackupArchive(t)
			ops := defaultBackupCommitOps()
			test.mutate(t, session, &ops)
			if err := commitBackupSnapshotWithOps(session, ops); err == nil {
				t.Fatal("faulted archive commit succeeded")
			}
		})
	}
	if err := commitBackupSnapshotWithOps(nil, defaultBackupCommitOps()); err == nil ||
		!strings.Contains(err.Error(), "input") {
		t.Fatalf("nil archive commit = %v", err)
	}
	session := newUncommittedBackupArchive(t)
	if err := commitBackupSnapshot(session); err != nil {
		t.Fatal(err)
	}
	if err := session.verifyFiles(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func newUncommittedBackupArchive(t *testing.T) *backupSession {
	t.Helper()
	_, _, session := backupArchiveGenerationSnapshot(t)
	finalRoot := session.root
	stageRoot := finalRoot + backupPartialSuffix
	mustBackupArchiveOK(t, os.Rename(finalRoot, stageRoot))
	session.root, session.finalRoot = stageRoot, finalRoot
	mustBackupArchiveOK(t, os.Remove(filepath.Join(stageRoot, backupManifestName)))
	mustBackupArchiveOK(t, os.Remove(filepath.Join(stageRoot, backupManifestHash)))
	return session
}

func mustBackupArchiveRead(t *testing.T, path string) []byte {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func mustBackupArchiveManifest(t *testing.T, manifest BackupManifest) []byte {
	t.Helper()
	payload, err := marshalBackupManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func mustBackupArchiveOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
