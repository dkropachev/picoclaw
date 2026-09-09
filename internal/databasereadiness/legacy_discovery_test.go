package databasereadiness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyInputDiscoveryEnforcesExplicitBounds(t *testing.T) {
	t.Run("entry count", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{"a", "b", "c"} {
			if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		assertLegacyDiscoveryLimit(
			t, root,
			legacyDiscoveryLimits{maxEntries: 3, maxFiles: 10, maxDepth: 10},
			"entry limit",
		)
	})

	t.Run("file count", func(t *testing.T) {
		root := t.TempDir()
		writeLegacyDiscoveryFile(t, filepath.Join(root, "a.json"))
		writeLegacyDiscoveryFile(t, filepath.Join(root, "b.json"))
		assertLegacyDiscoveryLimit(
			t, root,
			legacyDiscoveryLimits{maxEntries: 10, maxFiles: 1, maxDepth: 10},
			"file limit",
		)
	})

	t.Run("depth", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o700); err != nil {
			t.Fatal(err)
		}
		assertLegacyDiscoveryLimit(
			t, root,
			legacyDiscoveryLimits{maxEntries: 10, maxFiles: 10, maxDepth: 1},
			"depth limit",
		)
	})

	t.Run("inclusive boundary", func(t *testing.T) {
		root := t.TempDir()
		writeLegacyDiscoveryFile(t, filepath.Join(root, "legacy.json"))
		found, err := legacyInputExistsWithin(
			context.Background(), []string{root}, generationSet{},
			legacyDiscoveryLimits{maxEntries: 2, maxFiles: 1, maxDepth: 1},
		)
		if err != nil || !found {
			t.Fatalf("legacyInputExistsWithin() = %v, %v", found, err)
		}
	})
}

func TestLegacyInputDiscoveryRejectsUnsafeEntryAfterRegularInput(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "a.json")
	writeLegacyDiscoveryFile(t, regular)
	link := filepath.Join(root, "z-link")
	if err := os.Symlink(regular, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	found, err := legacyInputExists(context.Background(), []string{root}, generationSet{})
	if found || !errors.Is(err, errLegacyIntegrity) || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("legacyInputExists() = %v, %v", found, err)
	}
	if _, err := os.Lstat(regular); err != nil {
		t.Fatalf("legacy discovery mutated regular input: %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("legacy discovery mutated symlink: %v, %v", info, err)
	}
}

func TestLegacyInputDiscoveryHonorsContextForRegularRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "legacy.json")
	writeLegacyDiscoveryFile(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	found, err := legacyInputExists(ctx, []string{root}, generationSet{})
	if found || !errors.Is(err, context.Canceled) {
		t.Fatalf("legacyInputExists() = %v, %v", found, err)
	}
}

func TestLegacyInputDiscoverySkipsReservedTrees(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"legacy-json", "backups", ".database"} {
		directory := filepath.Join(root, name)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		writeLegacyDiscoveryFile(t, filepath.Join(directory, "ignored.json"))
	}

	found, err := legacyInputExists(context.Background(), []string{root}, generationSet{})
	if err != nil || found {
		t.Fatalf("legacyInputExists() = %v, %v", found, err)
	}
}

func assertLegacyDiscoveryLimit(
	t *testing.T,
	root string,
	limits legacyDiscoveryLimits,
	want string,
) {
	t.Helper()
	found, err := legacyInputExistsWithin(
		context.Background(), []string{root}, generationSet{}, limits,
	)
	if found || !errors.Is(err, errLegacyIntegrity) || !strings.Contains(err.Error(), want) {
		t.Fatalf("legacyInputExistsWithin() = %v, %v; want %q", found, err, want)
	}
}

func writeLegacyDiscoveryFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
}
