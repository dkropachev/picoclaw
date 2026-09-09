package databasemigration

import (
	"errors"
	"fmt"
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

func TestPinnedBackupTreeRemovalReopensMutatedDirectoryBatches(t *testing.T) {
	parent := migrationHome(t)
	tree := filepath.Join(parent, "tree")
	if err := os.Mkdir(tree, 0o700); err != nil {
		t.Fatal(err)
	}
	for index := range 257 {
		writeMigrationFile(t, filepath.Join(tree, fmt.Sprintf("entry-%03d", index)), []byte("x"))
	}
	if err := removePinnedBackupTree(tree); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("multi-batch tree remains: %v", err)
	}
}

func TestPinnedBackupTreeRemovalRequiresDurableQuarantine(t *testing.T) {
	parent := migrationHome(t)
	tree := filepath.Join(parent, "tree")
	writeBackupRemovalTree(t, tree)
	expected := backupRemovalDirectoryIdentity(t, tree)
	canary := errors.New("quarantine sync canary")
	root, err := os.OpenRoot(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	child, err := root.OpenRoot("tree")
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	ops := defaultBackupRemovalOps()
	ops.sync = func(*os.Root, string) error { return canary }
	if err := removeBackupTreeDurableWithOps(
		tree, root, "tree", child, expected, ops,
	); !errors.Is(err, canary) {
		t.Fatalf("quarantine sync fault = %v", err)
	}
	tombstones, err := filepath.Glob(filepath.Join(parent, ".database-backup-remove-*"))
	if err != nil || len(tombstones) != 1 || backupRemovalDirectoryIdentity(t, tombstones[0]) != expected {
		t.Fatal("sync failure changed quarantined identity")
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
		if closeErr := root.Close(); err != nil || closeErr != nil || syncCalls < 1 {
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

func TestBackupRemovalRejectsMismatchedPinnedNames(t *testing.T) {
	t.Run("file hard-link name", func(t *testing.T) {
		parent := migrationHome(t)
		path := filepath.Join(parent, "original")
		alias := filepath.Join(parent, "alias")
		writeMigrationFile(t, path, []byte("payload"))
		if err := os.Link(path, alias); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		expected, err := backupExistingIdentity(path, fileidentity.ObjectTypeRegular)
		if err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(parent)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		file, err := root.Open("original")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if err := removeBackupFileDurableWithSync(
			path, root, "alias", file, expected, func(string) error { return nil },
		); err == nil || !strings.Contains(err.Error(), "binding") {
			t.Fatalf("mismatched file removal name = %v", err)
		}
		if payload, err := os.ReadFile(path); err != nil || string(payload) != "payload" {
			t.Fatalf("mismatched file removal changed original = %q, %v", payload, err)
		}
		if payload, err := os.ReadFile(alias); err != nil || string(payload) != "payload" {
			t.Fatalf("mismatched file removal changed alias = %q, %v", payload, err)
		}
	})

	t.Run("directory name", func(t *testing.T) {
		parent := migrationHome(t)
		path := filepath.Join(parent, "original")
		decoy := filepath.Join(parent, "decoy")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(decoy, 0o700); err != nil {
			t.Fatal(err)
		}
		expected := backupRemovalDirectoryIdentity(t, path)
		root, err := os.OpenRoot(parent)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		child, err := root.OpenRoot("original")
		if err != nil {
			t.Fatal(err)
		}
		defer child.Close()
		if err := removeBackupTreeDurableWithSync(
			path, root, "decoy", child, expected, func(string) error { return nil },
		); err == nil || !strings.Contains(err.Error(), "binding") {
			t.Fatalf("mismatched tree removal name = %v", err)
		}
		if info, err := os.Lstat(path); err != nil || !info.IsDir() {
			t.Fatalf("mismatched tree removal changed original = %#v, %v", info, err)
		}
		if info, err := os.Lstat(decoy); err != nil || !info.IsDir() {
			t.Fatalf("mismatched tree removal changed decoy = %#v, %v", info, err)
		}
	})
}

func TestBackupRemovalContentsRejectMismatchedPinnedRoot(t *testing.T) {
	newRoots := func(t *testing.T) (string, string, *os.Root, fileidentity.Identity) {
		t.Helper()
		base := migrationHome(t)
		declared := filepath.Join(base, "declared")
		opened := filepath.Join(base, "opened")
		if err := os.Mkdir(declared, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(opened, 0o700); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(opened)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = root.Close() })
		return declared, opened, root, backupRemovalDirectoryIdentity(t, declared)
	}

	t.Run("different child identity", func(t *testing.T) {
		declared, opened, root, declaredIdentity := newRoots(t)
		writeMigrationFile(t, filepath.Join(declared, "payload"), []byte("declared"))
		writeMigrationFile(t, filepath.Join(opened, "payload"), []byte("opened"))
		entries := 0
		err := removePinnedBackupTreeContentsWithSync(
			declared, root,
			map[fileidentity.Identity]string{declaredIdentity: declared}, &entries,
			func(string) error { return nil },
		)
		if err == nil || !strings.Contains(err.Error(), "root identity") {
			t.Fatalf("mismatched pinned-root child = %v", err)
		}
		for path, want := range map[string]string{
			filepath.Join(declared, "payload"): "declared",
			filepath.Join(opened, "payload"):   "opened",
		} {
			if payload, readErr := os.ReadFile(path); readErr != nil || string(payload) != want {
				t.Fatalf("mismatched root changed %q = %q, %v", path, payload, readErr)
			}
		}
	})

	t.Run("hard-link child name", func(t *testing.T) {
		declared, opened, root, declaredIdentity := newRoots(t)
		declaredFile := filepath.Join(declared, "payload")
		openedFile := filepath.Join(opened, "payload")
		writeMigrationFile(t, declaredFile, []byte("payload"))
		if err := os.Link(declaredFile, openedFile); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		entries := 0
		err := removePinnedBackupTreeContentsWithSync(
			declared, root,
			map[fileidentity.Identity]string{declaredIdentity: declared}, &entries,
			func(string) error { return nil },
		)
		if err == nil || !strings.Contains(err.Error(), "root identity") {
			t.Fatalf("mismatched pinned-root hard link = %v", err)
		}
		if payload, err := os.ReadFile(declaredFile); err != nil || string(payload) != "payload" {
			t.Fatalf("declared hard-link source changed = %q, %v", payload, err)
		}
		if payload, err := os.ReadFile(openedFile); err != nil || string(payload) != "payload" {
			t.Fatalf("mismatched root changed opened hard link = %q, %v", payload, err)
		}
	})
}

func TestBackupRemovalNeverDeletesLateFileSubstitute(t *testing.T) {
	parent := migrationHome(t)
	path := filepath.Join(parent, "expected")
	writeMigrationFile(t, path, []byte("expected"))
	writeMigrationFile(t, filepath.Join(parent, "substitute"), []byte("substitute"))
	expected, err := backupExistingIdentity(path, fileidentity.ObjectTypeRegular)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := root.Open("expected")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ops := defaultBackupRemovalOps()
	ops.beforeRemove = func(root *os.Root, quarantine string) error {
		return errors.Join(
			root.Rename(quarantine, "retained-expected"),
			root.Rename("substitute", quarantine),
		)
	}
	if err := removeBackupFileDurableWithOps(
		path, root, "expected", file, expected, ops,
	); err == nil || !strings.Contains(err.Error(), "binding") {
		t.Fatalf("late file substitution = %v", err)
	}
	for leaf, want := range map[string]string{
		"retained-expected": "expected",
		"substitute":        "substitute",
	} {
		if leaf == "substitute" {
			matches, _ := filepath.Glob(filepath.Join(parent, ".database-backup-remove-*"))
			if len(matches) != 1 {
				t.Fatalf("substitute quarantine = %q", matches)
			}
			leaf = filepath.Base(matches[0])
		}
		if got, err := root.ReadFile(leaf); err != nil || string(got) != want {
			t.Fatalf("retained %s = %q, %v", want, got, err)
		}
	}
}

func TestBackupRemovalNeverRecursesIntoLateTreeSubstitute(t *testing.T) {
	parent := migrationHome(t)
	tree := filepath.Join(parent, "tree")
	writeBackupRemovalTree(t, tree)
	writeBackupRemovalTree(t, filepath.Join(parent, "substitute"))
	expected := backupRemovalDirectoryIdentity(t, tree)
	root, child := openBackupRemovalTree(t, parent, "tree")
	ops := defaultBackupRemovalOps()
	ops.afterQuarantine = func(root *os.Root, quarantine string) error {
		return errors.Join(
			root.Rename(quarantine, "retained-expected"),
			root.Rename("substitute", quarantine),
		)
	}
	if err := removeBackupTreeDurableWithOps(
		tree, root, "tree", child, expected, ops,
	); err == nil || !strings.Contains(err.Error(), "binding") {
		t.Fatalf("late tree substitution = %v", err)
	}
	assertPinnedRemovalPayload(t, filepath.Join(parent, "retained-expected"))
	matches, _ := filepath.Glob(filepath.Join(parent, ".database-backup-remove-*"))
	if len(matches) != 1 {
		t.Fatalf("substitute quarantine = %q", matches)
	}
	assertPinnedRemovalPayload(t, matches[0])
}

func TestBackupRemovalRejectsEntryAddedAfterPreflight(t *testing.T) {
	parent := migrationHome(t)
	tree := filepath.Join(parent, "tree")
	writeBackupRemovalTree(t, tree)
	expected := backupRemovalDirectoryIdentity(t, tree)
	root, child := openBackupRemovalTree(t, parent, "tree")
	ops := defaultBackupRemovalOps()
	added := false
	ops.afterQuarantine = func(*os.Root, string) error {
		if added {
			return nil
		}
		added = true
		return child.WriteFile("late", []byte("late"), 0o600)
	}
	if err := removeBackupTreeDurableWithOps(
		tree, root, "tree", child, expected, ops,
	); err == nil || !strings.Contains(err.Error(), "inventory changed") {
		t.Fatalf("late inventory addition = %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(parent, ".database-backup-remove-*"))
	if len(matches) != 1 {
		t.Fatalf("retained quarantine = %q", matches)
	}
	assertPinnedRemovalPayload(t, matches[0])
	if got, err := os.ReadFile(filepath.Join(matches[0], "late")); err != nil || string(got) != "late" {
		t.Fatalf("late entry changed = %q, %v", got, err)
	}
}

func TestBackupRemovalUsesRetainedParentAfterPathReplacement(t *testing.T) {
	base := migrationHome(t)
	parent := filepath.Join(base, "parent")
	moved := filepath.Join(base, "moved")
	tree := filepath.Join(parent, "tree")
	writeBackupRemovalTree(t, tree)
	expected := backupRemovalDirectoryIdentity(t, tree)
	root, child := openBackupRemovalTree(t, parent, "tree")
	ops := defaultBackupRemovalOps()
	replaced := false
	ops.beforeRename = func(*os.Root, string) error {
		if replaced {
			return nil
		}
		replaced = true
		if err := os.Rename(parent, moved); err != nil {
			return err
		}
		return writeBackupRemovalTreeError(filepath.Join(parent, "tree"))
	}
	if err := removeBackupTreeDurableWithOps(
		tree, root, "tree", child, expected, ops,
	); err != nil {
		t.Fatal(err)
	}
	assertPinnedRemovalPayload(t, filepath.Join(parent, "tree"))
	if _, err := os.Lstat(filepath.Join(moved, "tree")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retained-parent target remains: %v", err)
	}
}

func openBackupRemovalTree(t *testing.T, parent, leaf string) (*os.Root, *os.Root) {
	t.Helper()
	root, err := os.OpenRoot(parent)
	if err != nil {
		t.Fatal(err)
	}
	child, err := root.OpenRoot(leaf)
	if err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Close(); _ = root.Close() })
	return root, child
}

func writeBackupRemovalTreeError(path string) error {
	if err := os.MkdirAll(filepath.Join(path, "nested"), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(path, "nested", "payload"), []byte("payload"), 0o600)
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
