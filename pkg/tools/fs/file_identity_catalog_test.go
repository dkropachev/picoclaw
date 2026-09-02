package fstools

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestFileIdentityCatalogUsesMobileSafeDefaultsAndFixedFirstPassState(t *testing.T) {
	if defaultFileIdentityCatalogEntries != 131_072 ||
		defaultFileIdentityCatalogPathBytes != 32<<20 ||
		defaultFileIdentityCatalogDepth != 32 {
		t.Fatalf(
			"catalog defaults = entries:%d paths:%d depth:%d",
			defaultFileIdentityCatalogEntries,
			defaultFileIdentityCatalogPathBytes,
			defaultFileIdentityCatalogDepth,
		)
	}

	digestType := reflect.TypeOf(fileIdentityCatalogDigest{})
	if digestType.Size() > 80 {
		t.Fatalf("first-pass retained digest size = %d, want <= 80", digestType.Size())
	}
	for index := range digestType.NumField() {
		kind := digestType.Field(index).Type.Kind()
		if kind == reflect.Map || kind == reflect.Slice || kind == reflect.Pointer ||
			kind == reflect.Interface {
			t.Fatalf("first-pass digest field %d retains variable-size state: %s", index, kind)
		}
	}
}

func TestFileIdentityCatalogStreamsNearEntryLimitAndDeduplicatesFinalSet(t *testing.T) {
	root := t.TempDir()
	const entries = fileIdentityCatalogReadBatch*2 + 3
	seed := filepath.Join(root, "entry-0000")
	if err := os.WriteFile(seed, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	for index := 1; index < entries; index++ {
		path := filepath.Join(root, fmt.Sprintf("entry-%04d", index))
		if err := os.Link(seed, path); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
	}

	// One budget entry names the pinned root; every other entry is streamed in
	// bounded ReadDir batches. The final set stores only one physical identity.
	catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		TreeRoots: []string{root}, MaxEntries: entries + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Len() != 1 {
		t.Fatalf("near-limit hardlink catalog identities = %d, want 1", catalog.Len())
	}
	if catalog, err = NewFileIdentityCatalog(FileIdentityCatalogOptions{
		TreeRoots: []string{root}, MaxEntries: entries,
	}); err == nil || catalog != nil {
		t.Fatalf("over-limit streaming catalog = %#v, %v", catalog, err)
	}
}

func TestFileIdentityCatalogBoundsLargeExclusionSetsWithAggregateLimit(t *testing.T) {
	root := t.TempDir()
	const exclusions = 1_200
	paths := make([]string, 0, exclusions)
	for index := range exclusions {
		paths = append(paths, filepath.Join(root, fmt.Sprintf("database-%04d.db", index)))
	}
	catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{ExcludePaths: paths})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Len() != 0 {
		t.Fatalf("exclusion-only catalog identities = %d, want 0", catalog.Len())
	}
	if catalog, err = NewFileIdentityCatalog(FileIdentityCatalogOptions{
		ExcludePaths: paths,
		MaxEntries:   exclusions - 1,
	}); err == nil || catalog != nil {
		t.Fatalf("over-limit exclusion catalog = %#v, %v", catalog, err)
	}
}

func TestFileIdentityCatalogBoundsPreparedInputsInAggregate(t *testing.T) {
	root := t.TempDir()
	exact := filepath.Join(root, "exact")
	tree := filepath.Join(root, "tree")
	excluded := filepath.Join(root, "excluded")
	if catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		ExactPaths: []string{exact}, TreeRoots: []string{tree},
		ExcludePaths: []string{excluded}, MaxEntries: 2,
	}); err == nil || catalog != nil || !strings.Contains(err.Error(), "input limit") {
		t.Fatalf("aggregate input-count catalog = %#v, %v", catalog, err)
	}
	pathBytes := int64(len(exact) + len(tree) + len(excluded) - 1)
	if catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		ExactPaths: []string{exact}, TreeRoots: []string{tree},
		ExcludePaths: []string{excluded}, MaxPathBytes: pathBytes,
	}); err == nil || catalog != nil || !strings.Contains(err.Error(), "path-byte") {
		t.Fatalf("aggregate input-byte catalog = %#v, %v", catalog, err)
	}
	t.Chdir(root)
	if catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		ExactPaths: []string{"x"}, MaxPathBytes: 1,
	}); err == nil || catalog != nil || !strings.Contains(err.Error(), "path-byte") {
		t.Fatalf("absolute-expansion catalog = %#v, %v", catalog, err)
	}
}

func TestFileIdentityCatalogStreamingDigestIsOrderIndependent(t *testing.T) {
	root := t.TempDir()
	paths := []string{filepath.Join(root, "first"), filepath.Join(root, "second")}
	infos := make([]os.FileInfo, 0, len(paths))
	identities := make([]string, 0, len(paths))
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := snapshotFileIdentity(path, info)
		if err != nil {
			t.Fatal(err)
		}
		infos = append(infos, info)
		identities = append(identities, identity)
	}
	forward := newFileIdentityCatalogRecorder(false)
	reverse := newFileIdentityCatalogRecorder(false)
	for index := range paths {
		if err := forward.record('f', paths[index], infos[index], identities[index]); err != nil {
			t.Fatal(err)
		}
	}
	for index := len(paths) - 1; index >= 0; index-- {
		if err := reverse.record('f', paths[index], infos[index], identities[index]); err != nil {
			t.Fatal(err)
		}
	}
	if forward.finish() != reverse.finish() {
		t.Fatal("streaming digest depends on enumeration order")
	}
}

func TestFileIdentityCatalogProtectsOpenedFileFromActualHandle(t *testing.T) {
	root := t.TempDir()
	protectedPath := filepath.Join(root, "protected")
	ordinaryPath := filepath.Join(root, "ordinary")
	for _, path := range []string{protectedPath, ordinaryPath} {
		if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		ExactPaths: []string{protectedPath},
	})
	if err != nil {
		t.Fatal(err)
	}

	protectedInfo, err := os.Stat(protectedPath)
	if err != nil {
		t.Fatal(err)
	}
	protectedFile, err := os.Open(protectedPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = protectedFile.Close() })
	protected, err := catalog.ProtectsOpenedFile(protectedFile, protectedInfo)
	if err != nil || !protected {
		t.Fatalf("protected opened handle = %t, %v", protected, err)
	}

	ordinaryInfo, err := os.Stat(ordinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	ordinaryFile, err := os.Open(ordinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ordinaryFile.Close() })
	protected, err = catalog.ProtectsOpenedFile(ordinaryFile, ordinaryInfo)
	if err != nil || protected {
		t.Fatalf("ordinary opened handle = %t, %v", protected, err)
	}
	if protected, err = catalog.ProtectsOpenedFile(protectedFile, ordinaryInfo); err == nil || protected {
		t.Fatalf("mismatched preflight = %t, %v", protected, err)
	}
	if directory, openErr := os.Open(root); openErr != nil {
		t.Fatal(openErr)
	} else {
		directoryInfo, statErr := directory.Stat()
		if statErr != nil {
			_ = directory.Close()
			t.Fatal(statErr)
		}
		protected, err = catalog.ProtectsOpenedFile(directory, directoryInfo)
		_ = directory.Close()
		if err == nil || protected {
			t.Fatalf("directory opened handle = %t, %v", protected, err)
		}
	}
	if closeErr := ordinaryFile.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if protected, err = catalog.ProtectsOpenedFile(ordinaryFile, ordinaryInfo); err == nil || protected {
		t.Fatalf("closed opened handle = %t, %v", protected, err)
	}
}
