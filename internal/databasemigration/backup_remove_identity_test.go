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
