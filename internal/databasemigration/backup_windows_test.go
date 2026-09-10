//go:build windows

package databasemigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func TestWindowsBackupSnapshotVerifyAndPreparedUse(t *testing.T) {
	home := migrationHome(t)
	legacy := filepath.Join(home, "legacy.json")
	writeMigrationFile(t, legacy, []byte("legacy"))
	spec := storecatalog.Spec{
		ID: "global/auth", Path: filepath.Join(home, "auth.db"), LegacyRoots: []string{legacy},
	}
	writeMigrationFile(t, spec.Path, []byte("database"))
	session, err := snapshotBackup(
		t.Context(), time.Now, home,
		[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, record := range session.manifest.Files {
		path := filepath.Join(session.root, filepath.FromSlash(record.Backup))
		info, statErr := os.Lstat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if err := fileutil.ValidatePrivateFile(path, info); err != nil {
			t.Fatalf("Windows backup DACL is not owner-only: %v", err)
		}
	}
	prepared, cleanup, err := session.prepareGeneration(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.use(t.Context(), func(_ context.Context, path string) error {
		payload, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if string(payload) != "database" {
			return errors.New("prepared Windows database bytes differ")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsPinnedCleanupRetainsHandlesThroughDeletion(t *testing.T) {
	root := migrationHome(t)
	tree := filepath.Join(root, "tree")
	writeMigrationFile(t, filepath.Join(tree, "nested", "payload"), []byte("payload"))
	treeIdentity, objectType, exists, err := fileidentity.ExistingWithType(tree)
	if err != nil || !exists || objectType != fileidentity.ObjectTypeDirectory {
		t.Fatalf("Windows tree identity = %#v, %v, %t, %v", treeIdentity, objectType, exists, err)
	}
	if err := removePinnedBackupTreeIdentity(tree, treeIdentity); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Windows retained-handle tree removal = %v", err)
	}

	file := filepath.Join(root, "file")
	writeMigrationFile(t, file, []byte("file"))
	fileIdentity, objectType, exists, err := fileidentity.ExistingWithType(file)
	if err != nil || !exists || objectType != fileidentity.ObjectTypeRegular {
		t.Fatalf("Windows file identity = %#v, %v, %t, %v", fileIdentity, objectType, exists, err)
	}
	if err := removePinnedBackupFile(file, fileIdentity); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Windows retained-handle file removal = %v", err)
	}
}
