package databasemigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func TestPrepareGenerationRecreatesVerifiedMembersAndCleansOnce(t *testing.T) {
	root := filepath.Join(t.TempDir(), "backup")
	if err := ensurePrivateBackupDirectory(root); err != nil {
		t.Fatal(err)
	}
	storeID := "global/auth"
	roles := []struct {
		role    string
		suffix  string
		payload string
	}{
		{role: "database", payload: "main"},
		{role: "wal", suffix: "-wal", payload: "wal"},
		{role: "shm", suffix: "-shm", payload: "shm"},
	}
	session := &backupSession{root: root, manifest: BackupManifest{
		Version: backupManifestVersion, CreatedAt: time.Unix(1, 0).UTC(),
		Stores:             []BackupStoreManifest{{StoreID: storeID, Exists: true}},
		CatalogGenerations: []string{filepath.Join(t.TempDir(), "store.db")},
	}}
	for index, role := range roles {
		relative := filepath.Join("files", role.role)
		path := filepath.Join(root, relative)
		writeIdentityMigrationFile(t, path, []byte(role.payload))
		if err := secureAndValidateBackupFile(path); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(role.payload))
		session.manifest.Files = append(session.manifest.Files, BackupFileManifest{
			StoreID:        storeID,
			Role:           role.role,
			Source:         filepath.Join(t.TempDir(), "live", role.role),
			SourceIdentity: "fixture-" + role.role,
			Backup:         filepath.ToSlash(relative),
			SHA256:         hex.EncodeToString(digest[:]),
			Size:           int64(len(role.payload)),
			Mode:           0o600,
			SourceMode: func() uint32 {
				if index == 0 {
					return 0o640
				}
				return 0o600
			}(),
		})
	}
	if err := session.finish("snapshot_complete", nil); err != nil {
		t.Fatal(err)
	}

	main, cleanup, err := session.prepareGeneration(t.Context(), storecatalog.Spec{ID: "global/auth"})
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range roles {
		payload, readErr := os.ReadFile(main + role.suffix)
		if readErr != nil || string(payload) != role.payload {
			t.Errorf("recreated %s = %q, %v", role.role, payload, readErr)
		}
	}
	workRoot := filepath.Dir(main)
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("second cleanup = %v", err)
	}
	if _, err := os.Lstat(workRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disposable generation survived cleanup: %v", err)
	}
}

func TestPrepareGenerationReturnsAbsentPrivateTargetForMissingStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "backup")
	if err := ensurePrivateBackupDirectory(root); err != nil {
		t.Fatal(err)
	}
	session := &backupSession{root: root, manifest: BackupManifest{
		Version: backupManifestVersion, CreatedAt: time.Unix(1, 0).UTC(),
		Stores:             []BackupStoreManifest{{StoreID: "global/auth", Exists: false}},
		CatalogGenerations: []string{filepath.Join(t.TempDir(), "store.db")},
	}}
	if err := session.finish("snapshot_complete", nil); err != nil {
		t.Fatal(err)
	}
	main, cleanup, err := session.prepareGeneration(nil, storecatalog.Spec{
		ID: "global/auth", Path: filepath.Join(t.TempDir(), "live-must-not-be-read.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(main); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing generation target exists: %v", err)
	}
	info, err := os.Lstat(filepath.Dir(main))
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("disposable generation root = %#v, %v", info, err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestBackupPathAndManifestBoundsRejectInvalidUTF8AndLongComponents(t *testing.T) {
	invalidUTF8 := string([]byte{'b', 'a', 'd', 0xff})
	overlong := strings.Repeat("x", backupMaxComponent+1)
	for _, value := range []string{invalidUTF8, overlong, filepath.Join("stores", overlong)} {
		if path, err := backupFilePath(t.TempDir(), value); path != "" || err == nil {
			t.Errorf("backupFilePath(%q) = %q, %v", value, path, err)
		}
	}
	for _, source := range []string{filepath.Join(t.TempDir(), invalidUTF8), filepath.Join(t.TempDir(), overlong)} {
		session, _ := verifiedIdentityBackupFixture(t, []byte("payload"))
		session.manifest.Files[0].Source = source
		if err := session.finish("snapshot_complete", nil); err == nil {
			t.Errorf("manifest serialized source %q", source)
		}
	}
}

func TestBackupReadersRejectNoProgress(t *testing.T) {
	if entries, err := readBackupDirectoryBatch(noProgressDirectoryReader{}); len(entries) != 0 || err == nil {
		t.Fatalf("readBackupDirectoryBatch() = %d entries, %v", len(entries), err)
	}
	if written, err := copyWithContext(
		context.Background(), io.Discard, sha256.New(), noProgressReader{}, 1,
	); written != 0 || err == nil {
		t.Fatalf("copyWithContext(no progress) = %d, %v", written, err)
	}
}

type noProgressDirectoryReader struct{}

func (noProgressDirectoryReader) ReadDir(int) ([]os.DirEntry, error) { return nil, nil }

type noProgressReader struct{}

func (noProgressReader) Read([]byte) (int, error) { return 0, nil }

func verifiedIdentityBackupFixture(t *testing.T, payload []byte) (*backupSession, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "backup")
	if err := ensurePrivateBackupDirectory(root); err != nil {
		t.Fatal(err)
	}
	relative := filepath.Join("files", "payload")
	path := filepath.Join(root, relative)
	writeIdentityMigrationFile(t, path, payload)
	if err := secureAndValidateBackupFile(path); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	session := &backupSession{root: root, manifest: BackupManifest{
		Version: backupManifestVersion, CreatedAt: time.Unix(1, 0).UTC(),
		Stores:             []BackupStoreManifest{{StoreID: "global/auth", LegacyRoots: 1}},
		CatalogGenerations: []string{filepath.Join(t.TempDir(), "store.db")},
		Files: []BackupFileManifest{{
			StoreID: "global/auth", Role: "legacy", SourceIdentity: "fixture-source",
			Source: filepath.Join(t.TempDir(), "source"),
			Backup: filepath.ToSlash(relative), SHA256: hex.EncodeToString(digest[:]),
			Size: int64(len(payload)), Mode: 0o600, SourceMode: 0o600,
		}},
	}}
	if err := session.finish("snapshot_complete", nil); err != nil {
		t.Fatal(err)
	}
	return session, path
}

func writeIdentityMigrationFile(t *testing.T, path string, payload []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}
