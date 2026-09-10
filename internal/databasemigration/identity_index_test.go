//nolint:govet // Independent failure-boundary assertions use narrow error scopes.
package databasemigration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func TestPrepareGenerationRecreatesVerifiedMembersAndCleansOnce(t *testing.T) {
	home := migrationHome(t)
	storePath := filepath.Join(home, "auth.db")
	roles := []struct {
		role    string
		suffix  string
		payload string
	}{
		{role: "database", payload: "main"},
		{role: "wal", suffix: "-wal", payload: "wal"},
		{role: "shm", suffix: "-shm", payload: "shm"},
	}
	for _, role := range roles {
		writeIdentityMigrationFile(t, storePath+role.suffix, []byte(role.payload))
	}
	spec := storecatalog.Spec{ID: "global/auth", Path: storePath}
	session, err := snapshotBackup(
		t.Context(), time.Now, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
	)
	if err != nil {
		t.Fatal(err)
	}

	prepared, cleanup, err := session.prepareGeneration(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	main := prepared.path
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
	home := migrationHome(t)
	spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	session, err := snapshotBackup(
		t.Context(), time.Now, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared, cleanup, err := session.prepareGeneration(nil, spec)
	if err != nil {
		t.Fatal(err)
	}
	main := prepared.path
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
	deep := "leaf"
	for range backupMaxArchiveDepth {
		deep = filepath.Join("nested", deep)
	}
	if path, err := backupFilePath(t.TempDir(), deep); path != "" || err == nil {
		t.Fatalf("deep backup manifest path = %q, %v", path, err)
	}
	for _, source := range []string{filepath.Join(t.TempDir(), invalidUTF8), filepath.Join(t.TempDir(), overlong)} {
		session, _ := verifiedIdentityBackupFixture(t, []byte("payload"))
		session.manifest.Files[0].Source = source
		if err := validateBackupManifest(session.manifest); err == nil {
			t.Errorf("manifest serialized source %q", source)
		}
	}
}

func verifiedIdentityBackupFixture(t *testing.T, payload []byte) (*backupSession, string) {
	t.Helper()
	home := migrationHome(t)
	legacy := filepath.Join(home, "source")
	writeIdentityMigrationFile(t, legacy, payload)
	spec := storecatalog.Spec{
		ID: "global/auth", Path: filepath.Join(home, "auth.db"), LegacyRoots: []string{legacy},
	}
	session, err := snapshotBackup(
		t.Context(), time.Now, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(session.root, filepath.FromSlash(session.manifest.Files[0].Backup))
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
