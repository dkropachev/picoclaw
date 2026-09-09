package databasemigration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func TestPinnedBackupTreeRemovalRequiresOriginalIdentity(t *testing.T) {
	parent := t.TempDir()
	tree := filepath.Join(parent, "tree")
	writeBackupRemovalTree(t, tree)
	expected := backupRemovalDirectoryIdentity(t, tree)

	other := filepath.Join(parent, "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	wrong := backupRemovalDirectoryIdentity(t, other)
	if err := removePinnedBackupTreeIdentity(tree, wrong); err == nil ||
		!strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("wrong-identity removal = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(tree, "nested", "payload")); err != nil {
		t.Fatalf("wrong-identity removal altered target: %v", err)
	}

	if err := removePinnedBackupTreeIdentity(tree, expected); err != nil {
		t.Fatalf("expected-identity removal = %v", err)
	}
	if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed tree remains: %v", err)
	}
	if err := removePinnedBackupTreeIdentity(tree, expected); err != nil {
		t.Fatalf("idempotent expected-identity removal = %v", err)
	}
	matches, globErr := filepath.Glob(filepath.Join(parent, ".database-backup-remove-*"))
	if globErr != nil || len(matches) != 0 {
		t.Fatalf("successful removal tombstones = %q, %v", matches, globErr)
	}
}

func TestPinnedBackupTreeRemovalQuarantinesBeforeDeleting(t *testing.T) {
	parent := t.TempDir()
	tree := filepath.Join(parent, "tree")
	writeBackupRemovalTree(t, tree)
	expected := backupRemovalDirectoryIdentity(t, tree)
	tombstone := filepath.Join(parent, ".database-backup-remove-test")

	ops := defaultBackupTreeRemovalOps()
	ops.reserve = func(string) (string, error) { return tombstone, nil }
	ops.rename = os.Rename
	var removedPath string
	ops.removeTree = func(
		path string,
		root *os.Root,
		leaf string,
		child *os.Root,
		expected fileidentity.Identity,
	) error {
		removedPath = path
		if path != tombstone || leaf != filepath.Base(tombstone) {
			return errors.New("recursive deletion did not target tombstone")
		}
		return removeBackupTreeDurable(path, root, leaf, child, expected)
	}
	if err := removePinnedBackupTreeWithOps(tree, expected, ops); err != nil {
		t.Fatal(err)
	}
	if removedPath != tombstone {
		t.Fatalf("recursive deletion target = %q, want %q", removedPath, tombstone)
	}
	if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original removal name remains: %v", err)
	}
	if _, err := os.Lstat(tombstone); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verified tombstone remains: %v", err)
	}
}

func TestPinnedBackupTreeRemovalLeavesSubstitutedTombstone(t *testing.T) {
	parent := t.TempDir()
	tree := filepath.Join(parent, "tree")
	writeBackupRemovalTree(t, tree)
	expected := backupRemovalDirectoryIdentity(t, tree)
	tombstone := filepath.Join(parent, ".database-backup-remove-test")
	quarantinedOriginal := filepath.Join(parent, "quarantined-original")
	substitute := filepath.Join(parent, "substitute")
	writeBackupRemovalTree(t, substitute)

	ops := defaultBackupTreeRemovalOps()
	ops.reserve = func(string) (string, error) { return tombstone, nil }
	ops.rename = func(source, target string) error {
		if err := os.Rename(source, target); err != nil {
			return err
		}
		if err := os.Rename(target, quarantinedOriginal); err != nil {
			return err
		}
		return os.Rename(substitute, target)
	}
	removeCalled := false
	ops.removeTree = func(string, *os.Root, string, *os.Root, fileidentity.Identity) error {
		removeCalled = true
		return nil
	}
	err := removePinnedBackupTreeWithOps(tree, expected, ops)
	if err == nil || !strings.Contains(err.Error(), "tombstone identity changed") {
		t.Fatalf("substituted tombstone removal = %v", err)
	}
	if removeCalled {
		t.Fatal("substituted tombstone reached recursive deletion")
	}
	if backupRemovalDirectoryIdentity(t, quarantinedOriginal) != expected {
		t.Fatal("original quarantine identity changed")
	}
	if _, err := os.Lstat(filepath.Join(tombstone, "nested", "payload")); err != nil {
		t.Fatalf("substituted tombstone was removed: %v", err)
	}
}

func TestPinnedBackupTreeRemovalLeavesTombstoneOnHandleMismatch(t *testing.T) {
	parent := t.TempDir()
	tree := filepath.Join(parent, "tree")
	writeBackupRemovalTree(t, tree)
	expected := backupRemovalDirectoryIdentity(t, tree)
	tombstone := filepath.Join(parent, ".database-backup-remove-test")
	other := filepath.Join(parent, "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	wrong := backupRemovalDirectoryIdentity(t, other)

	ops := defaultBackupTreeRemovalOps()
	ops.reserve = func(string) (string, error) { return tombstone, nil }
	ops.rename = os.Rename
	opened := ops.opened
	openedCalls := 0
	ops.opened = func(file *os.File) (fileidentity.Identity, fileidentity.ObjectType, error) {
		openedCalls++
		identity, objectType, err := opened(file)
		if openedCalls == 3 && err == nil {
			return wrong, objectType, nil
		}
		return identity, objectType, err
	}
	removeCalled := false
	ops.removeTree = func(string, *os.Root, string, *os.Root, fileidentity.Identity) error {
		removeCalled = true
		return nil
	}
	err := removePinnedBackupTreeWithOps(tree, expected, ops)
	if err == nil || !strings.Contains(err.Error(), "tombstone handle identity changed") {
		t.Fatalf("mismatched tombstone handle removal = %v", err)
	}
	if removeCalled {
		t.Fatal("mismatched tombstone handle reached recursive deletion")
	}
	if backupRemovalDirectoryIdentity(t, tombstone) != expected {
		t.Fatal("mismatched-handle quarantine identity changed")
	}
}

func TestPinnedBackupTreeRemovalLeavesQuarantineWhenSourceNameReappears(t *testing.T) {
	parent := t.TempDir()
	tree := filepath.Join(parent, "tree")
	writeBackupRemovalTree(t, tree)
	expected := backupRemovalDirectoryIdentity(t, tree)
	tombstone := filepath.Join(parent, ".database-backup-remove-test")

	ops := defaultBackupTreeRemovalOps()
	ops.reserve = func(string) (string, error) { return tombstone, nil }
	ops.rename = func(source, target string) error {
		if err := os.Rename(source, target); err != nil {
			return err
		}
		return os.Mkdir(source, 0o700)
	}
	removeCalled := false
	ops.removeTree = func(string, *os.Root, string, *os.Root, fileidentity.Identity) error {
		removeCalled = true
		return nil
	}
	err := removePinnedBackupTreeWithOps(tree, expected, ops)
	if err == nil || !strings.Contains(err.Error(), "source name remained") {
		t.Fatalf("reappeared source-name removal = %v", err)
	}
	if removeCalled {
		t.Fatal("reappeared source name reached recursive deletion")
	}
	if backupRemovalDirectoryIdentity(t, tombstone) != expected {
		t.Fatal("source-name race changed quarantine identity")
	}
	if _, err := os.Lstat(tree); err != nil {
		t.Fatalf("replacement source name is absent: %v", err)
	}
}

func TestPinnedBackupTreeRemovalNeverRecursesIntoLateSubstitute(t *testing.T) {
	parent := t.TempDir()
	tree := filepath.Join(parent, "tree")
	writeBackupRemovalTree(t, tree)
	expected := backupRemovalDirectoryIdentity(t, tree)
	tombstone := filepath.Join(parent, ".database-backup-remove-test")
	quarantinedOriginal := filepath.Join(parent, "quarantined-original")
	substitute := filepath.Join(parent, "substitute")
	writeBackupRemovalTree(t, substitute)

	ops := defaultBackupTreeRemovalOps()
	ops.reserve = func(string) (string, error) { return tombstone, nil }
	ops.rename = os.Rename
	removeTree := ops.removeTree
	ops.removeTree = func(
		path string,
		root *os.Root,
		leaf string,
		retained *os.Root,
		identity fileidentity.Identity,
	) error {
		if err := os.Rename(tombstone, quarantinedOriginal); err != nil {
			return err
		}
		if err := os.Rename(substitute, tombstone); err != nil {
			return err
		}
		return removeTree(path, root, leaf, retained, identity)
	}
	err := removePinnedBackupTreeWithOps(tree, expected, ops)
	if err == nil {
		t.Fatalf("late tombstone substitution = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(tombstone, "nested", "payload")); err != nil {
		t.Fatalf("late substitute was recursively altered: %v", err)
	}
	if _, err := os.Lstat(quarantinedOriginal); err != nil {
		t.Fatalf("original quarantine disappeared: %v", err)
	}
}

func backupRemovalDirectoryIdentity(t *testing.T, path string) fileidentity.Identity {
	t.Helper()
	identity, objectType, exists, err := fileidentity.ExistingWithType(path)
	if err != nil || !exists || objectType != fileidentity.ObjectTypeDirectory {
		t.Fatalf("directory identity for %q = %#v, %v, %t, %v", path, identity, objectType, exists, err)
	}
	return identity
}

func writeBackupRemovalTree(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "nested", "payload"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
}
