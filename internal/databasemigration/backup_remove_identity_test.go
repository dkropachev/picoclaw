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

func TestPinnedBackupTreeRemovalRequiresDurableQuarantine(t *testing.T) {
	parent := migrationHome(t)
	tree := filepath.Join(parent, "tree")
	writeBackupRemovalTree(t, tree)
	expected := backupRemovalDirectoryIdentity(t, tree)
	tombstone := filepath.Join(parent, ".database-backup-remove-test")
	canary := errors.New("quarantine sync canary")

	ops := defaultBackupTreeRemovalOps()
	ops.reserve = func(string) (string, error) { return tombstone, nil }
	ops.rename = os.Rename
	syncCalls := 0
	ops.syncDir = func(path string) error {
		syncCalls++
		if path != parent {
			t.Fatalf("quarantine sync path = %q, want %q", path, parent)
		}
		return canary
	}
	removeCalled := false
	ops.removeTree = func(string, *os.Root, string, *os.Root, fileidentity.Identity) error {
		removeCalled = true
		return nil
	}
	if err := removePinnedBackupTreeWithOps(tree, expected, ops); !errors.Is(err, canary) {
		t.Fatalf("quarantine sync fault = %v", err)
	}
	if syncCalls != 1 || removeCalled {
		t.Fatalf("quarantine sync ordering: calls=%d remove=%t", syncCalls, removeCalled)
	}
	if backupRemovalDirectoryIdentity(t, tombstone) != expected {
		t.Fatal("sync failure changed quarantined identity")
	}
}

func TestPinnedBackupTreeRemovalMissingRetryResyncsParent(t *testing.T) {
	parent := migrationHome(t)
	existing := filepath.Join(parent, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	expected := backupRemovalDirectoryIdentity(t, existing)
	missing := filepath.Join(parent, "missing")
	canary := errors.New("missing retry sync canary")
	ops := defaultBackupTreeRemovalOps()
	ops.syncDir = func(path string) error {
		if path != parent {
			t.Fatalf("missing retry sync path = %q, want %q", path, parent)
		}
		return canary
	}
	if err := removePinnedBackupTreeWithOps(missing, expected, ops); !errors.Is(err, canary) {
		t.Fatalf("missing retry sync fault = %v", err)
	}
}

func TestBackupRemovalUnlinksRequireDirectorySync(t *testing.T) {
	canary := errors.New("removal sync canary")

	t.Run("child unlink and empty retry", func(t *testing.T) {
		rootPath := filepath.Join(migrationHome(t), "tree")
		if err := os.Mkdir(rootPath, 0o700); err != nil {
			t.Fatal(err)
		}
		child := filepath.Join(rootPath, "payload")
		writeMigrationFile(t, child, []byte("payload"))
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			t.Fatal(err)
		}
		identity := backupRemovalDirectoryIdentity(t, rootPath)
		entries := 0
		err = removePinnedBackupTreeContentsWithSync(
			rootPath, root, map[fileidentity.Identity]string{identity: rootPath}, &entries,
			func(path string) error {
				if path != rootPath {
					t.Fatalf("child sync path = %q, want %q", path, rootPath)
				}
				return canary
			},
		)
		if closeErr := root.Close(); !errors.Is(err, canary) || closeErr != nil {
			t.Fatalf("child unlink sync fault = %v, close=%v", err, closeErr)
		}
		if _, statErr := os.Lstat(child); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("child unlink did not happen before sync: %v", statErr)
		}

		root, err = os.OpenRoot(rootPath)
		if err != nil {
			t.Fatal(err)
		}
		entries = 0
		syncCalls := 0
		err = removePinnedBackupTreeContentsWithSync(
			rootPath, root, map[fileidentity.Identity]string{identity: rootPath}, &entries,
			func(path string) error {
				syncCalls++
				if path != rootPath {
					t.Fatalf("retry sync path = %q, want %q", path, rootPath)
				}
				return nil
			},
		)
		if closeErr := root.Close(); err != nil || closeErr != nil || syncCalls != 1 {
			t.Fatalf("empty retry sync = calls=%d err=%v close=%v", syncCalls, err, closeErr)
		}
	})

	t.Run("top-level tree unlink", func(t *testing.T) {
		parent := migrationHome(t)
		tree := filepath.Join(parent, "tree")
		if err := os.Mkdir(tree, 0o700); err != nil {
			t.Fatal(err)
		}
		expected := backupRemovalDirectoryIdentity(t, tree)
		parentRoot, err := os.OpenRoot(parent)
		if err != nil {
			t.Fatal(err)
		}
		defer parentRoot.Close()
		childRoot, err := parentRoot.OpenRoot("tree")
		if err != nil {
			t.Fatal(err)
		}
		defer childRoot.Close()
		err = removeBackupTreeDurableWithSync(
			tree, parentRoot, "tree", childRoot, expected,
			func(path string) error {
				if path == tree {
					return nil
				}
				if path != parent {
					t.Fatalf("top-level sync path = %q, want %q", path, parent)
				}
				return canary
			},
		)
		if !errors.Is(err, canary) {
			t.Fatalf("top-level tree sync fault = %v", err)
		}
		if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("tree unlink did not happen before sync: %v", err)
		}
	})

	t.Run("top-level file unlink", func(t *testing.T) {
		parent := migrationHome(t)
		path := filepath.Join(parent, "file")
		writeMigrationFile(t, path, []byte("payload"))
		expected, err := backupExistingIdentity(path, fileidentity.ObjectTypeRegular)
		if err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(parent)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		file, err := root.Open("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		err = removeBackupFileDurableWithSync(
			path, root, "file", file, expected,
			func(directory string) error {
				if directory != parent {
					t.Fatalf("file sync path = %q, want %q", directory, parent)
				}
				return canary
			},
		)
		if !errors.Is(err, canary) {
			t.Fatalf("top-level file sync fault = %v", err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("file unlink did not happen before sync: %v", err)
		}
	})
}

func TestBackupRemovalRejectsMissingSyncOperation(t *testing.T) {
	if err := removeBackupTreeDurableWithSync(
		"", nil, "", nil, fileidentity.Identity{}, nil,
	); err == nil || !strings.Contains(err.Error(), "sync is unavailable") {
		t.Fatalf("tree removal without sync = %v", err)
	}
	if err := removeBackupFileDurableWithSync(
		"", nil, "", nil, fileidentity.Identity{}, nil,
	); err == nil || !strings.Contains(err.Error(), "sync is unavailable") {
		t.Fatalf("file removal without sync = %v", err)
	}
	rootPath := migrationHome(t)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	entries := 0
	if err := removePinnedBackupTreeContentsWithSync(
		rootPath, root, map[fileidentity.Identity]string{}, &entries, nil,
	); err == nil || !strings.Contains(err.Error(), "sync is unavailable") {
		t.Fatalf("tree contents without sync = %v", err)
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
