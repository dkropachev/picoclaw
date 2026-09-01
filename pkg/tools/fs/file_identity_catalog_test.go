package fstools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileIdentityCatalogSurvivesArchiveRenameAndProtectsEveryMutationTool(t *testing.T) {
	workspace := t.TempDir()
	activeRoot := filepath.Join(workspace, "active")
	archiveRoot := filepath.Join(workspace, "archive")
	source := filepath.Join(activeRoot, "state.json")
	if err := os.MkdirAll(activeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		TreeRoots: []string{activeRoot, archiveRoot},
	})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Len() != 1 {
		t.Fatalf("catalog identities = %d, want 1", catalog.Len())
	}
	alias := filepath.Join(workspace, "state-alias.json")
	if err := os.Link(source, alias); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	archived := filepath.Join(archiveRoot, "state.json")
	if err := os.Rename(source, archived); err != nil {
		t.Fatal(err)
	}

	mutationTools := buildFileMutationTestTools(t, workspace, true, FileMutationPolicy{
		ProtectedIdentities: catalog,
	})
	for toolName, tool := range mutationTools {
		requireFileMutationPolicyDenied(t, toolName, tool, alias)
		ordinary := filepath.Join(workspace, "ordinary-"+toolName+".txt")
		if err := os.WriteFile(ordinary, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
		result := executeFileMutationTestTool(toolName, tool, ordinary)
		if result == nil || result.IsError {
			t.Fatalf("%s denied ordinary file: %#v", toolName, result)
		}
	}
	for _, path := range []string{alias, archived} {
		content, readErr := os.ReadFile(path)
		if readErr != nil || string(content) != "before" {
			t.Fatalf("protected identity %q = %q, %v", filepath.Base(path), content, readErr)
		}
	}
}

func TestFileIdentityCatalogFailsClosedOnUnsafeBoundsAndRaces(t *testing.T) {
	root := t.TempDir()
	secretName := "private-runtime-secret.json"
	if err := os.WriteFile(filepath.Join(root, secretName), []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		TreeRoots: []string{root}, MaxEntries: 1,
	}); err == nil || catalog != nil || strings.Contains(err.Error(), secretName) {
		t.Fatalf("bounded catalog = %#v, %v", catalog, err)
	}

	symlink := filepath.Join(t.TempDir(), "runtime-tree")
	if err := os.Symlink(root, symlink); err == nil {
		if catalog, catalogErr := NewFileIdentityCatalog(FileIdentityCatalogOptions{
			TreeRoots: []string{symlink},
		}); catalogErr == nil || catalog != nil || strings.Contains(catalogErr.Error(), root) {
			t.Fatalf("symlink catalog = %#v, %v", catalog, catalogErr)
		}
	}

	missing := filepath.Join(root, "created-during-snapshot.json")
	fileIdentityCatalogBetweenSnapshots = func() {
		if err := os.WriteFile(missing, []byte("raced"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { fileIdentityCatalogBetweenSnapshots = nil })
	if catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		ExactPaths: []string{missing},
	}); err == nil || catalog != nil || strings.Contains(err.Error(), filepath.Base(missing)) {
		t.Fatalf("raced catalog = %#v, %v", catalog, err)
	}
}

func TestFileIdentityCatalogRejectsSourceArchiveRenameDuringSnapshot(t *testing.T) {
	root := t.TempDir()
	activeRoot := filepath.Join(root, "active")
	archiveRoot := filepath.Join(root, "archive")
	source := filepath.Join(activeRoot, "state.json")
	archive := filepath.Join(archiveRoot, "state.json")
	for _, directory := range []string{activeRoot, archiveRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(source, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileIdentityCatalogBetweenSnapshots = func() {
		if err := os.Rename(source, archive); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { fileIdentityCatalogBetweenSnapshots = nil })
	catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		TreeRoots: []string{activeRoot, archiveRoot},
	})
	if err == nil || catalog != nil || strings.Contains(err.Error(), source) {
		t.Fatalf("rename-raced catalog = %#v, %v", catalog, err)
	}
}

func TestFileIdentityCatalogDeduplicatesPhysicalHardlinks(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.WriteFile(first, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(first, second); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{TreeRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Len() != 1 {
		t.Fatalf("hardlink identity count = %d, want 1", catalog.Len())
	}
}

func TestFileIdentityCatalogExcludesOnlyExactVolatilePaths(t *testing.T) {
	workspace := t.TempDir()
	activeRoot := filepath.Join(workspace, "sessions")
	archiveRoot := filepath.Join(workspace, "legacy-json", "sessions-v1", "sessions")
	for _, directory := range []string{activeRoot, archiveRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	database := filepath.Join(activeRoot, "sessions.db")
	nearActive := filepath.Join(activeRoot, "sessions.db-retained.json")
	nearArchive := filepath.Join(archiveRoot, "sessions.db-archived.json")
	for _, path := range []string{database, nearActive, nearArchive} {
		if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		TreeRoots:    []string{activeRoot, archiveRoot},
		ExcludePaths: []string{database, database + "-wal", database + "-shm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Len() != 2 {
		t.Fatalf("exact-exclusion catalog identities = %d, want 2", catalog.Len())
	}
	for _, source := range []string{nearActive, nearArchive} {
		alias := filepath.Join(workspace, filepath.Base(source)+".alias")
		if err := os.Link(source, alias); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		info, err := os.Stat(alias)
		if err != nil {
			t.Fatal(err)
		}
		protected, err := catalog.ProtectsPath(alias, info)
		if err != nil || !protected {
			t.Fatalf("near-name identity %q protected=%v err=%v", filepath.Base(source), protected, err)
		}
	}
}

func TestFileIdentityCatalogBatchesWideTreesAndBoundsPathBytesAndDepth(t *testing.T) {
	t.Run("batched-wide-tree", func(t *testing.T) {
		root := t.TempDir()
		for index := range fileIdentityCatalogReadBatch + 37 {
			name := filepath.Join(root, fmt.Sprintf("entry-%04d", index))
			if err := os.WriteFile(name, []byte("before"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
			TreeRoots: []string{root}, MaxEntries: fileIdentityCatalogReadBatch + 64,
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := fileIdentityCatalogReadBatch + 37; catalog.Len() != want {
			t.Fatalf("wide-tree catalog identities = %d, want %d", catalog.Len(), want)
		}
	})

	t.Run("path-bytes", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "bounded-name"), []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
		catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
			TreeRoots: []string{root}, MaxPathBytes: 4,
		})
		if err == nil || catalog != nil || strings.Contains(err.Error(), root) {
			t.Fatalf("path-bounded catalog = %#v, %v", catalog, err)
		}
	})

	t.Run("depth", func(t *testing.T) {
		root := t.TempDir()
		deep := filepath.Join(root, "one", "two")
		if err := os.MkdirAll(deep, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(deep, "state.json"), []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
		catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
			TreeRoots: []string{root}, MaxDepth: 2,
		})
		if err == nil || catalog != nil || strings.Contains(err.Error(), deep) {
			t.Fatalf("depth-bounded catalog = %#v, %v", catalog, err)
		}
	})
}
