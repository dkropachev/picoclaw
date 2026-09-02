package fstools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileIdentityCatalogRejectsInvalidOptionsAndInputs(t *testing.T) {
	for name, options := range map[string]FileIdentityCatalogOptions{
		"negative entries": {MaxEntries: -1},
		"excess entries":   {MaxEntries: maximumFileIdentityCatalogEntries + 1},
		"negative bytes":   {MaxPathBytes: -1},
		"excess bytes":     {MaxPathBytes: maximumFileIdentityCatalogPathBytes + 1},
		"negative depth":   {MaxDepth: -1},
		"excess depth":     {MaxDepth: maximumFileIdentityCatalogDepth + 1},
	} {
		t.Run(name, func(t *testing.T) {
			catalog, err := NewFileIdentityCatalog(options)
			if err == nil || catalog != nil {
				t.Fatalf("NewFileIdentityCatalog() = %#v, %v", catalog, err)
			}
		})
	}

	invalidUTF8 := string([]byte{0xff})
	for name, options := range map[string]FileIdentityCatalogOptions{
		"empty exact":      {ExactPaths: []string{""}},
		"spaced exact":     {ExactPaths: []string{" exact "}},
		"nul exact":        {ExactPaths: []string{"exact\x00path"}},
		"utf8 exact":       {ExactPaths: []string{invalidUTF8}},
		"empty tree":       {TreeRoots: []string{""}},
		"spaced tree":      {TreeRoots: []string{" tree "}},
		"nul tree":         {TreeRoots: []string{"tree\x00path"}},
		"utf8 tree":        {TreeRoots: []string{invalidUTF8}},
		"empty exclusion":  {ExcludePaths: []string{""}},
		"spaced exclusion": {ExcludePaths: []string{" excluded "}},
		"nul exclusion":    {ExcludePaths: []string{"excluded\x00path"}},
		"utf8 exclusion":   {ExcludePaths: []string{invalidUTF8}},
	} {
		t.Run(name, func(t *testing.T) {
			catalog, err := NewFileIdentityCatalog(options)
			if err == nil || catalog != nil || strings.Contains(err.Error(), invalidUTF8) {
				t.Fatalf("NewFileIdentityCatalog() = %#v, %v", catalog, err)
			}
		})
	}

	root := t.TempDir()
	regular := filepath.Join(root, "regular")
	directory := filepath.Join(root, "directory")
	if err := os.WriteFile(regular, []byte("regular"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, options := range map[string]FileIdentityCatalogOptions{
		"regular tree":        {TreeRoots: []string{regular}},
		"directory exclusion": {ExcludePaths: []string{directory}},
	} {
		t.Run(name, func(t *testing.T) {
			catalog, err := NewFileIdentityCatalog(options)
			if err == nil || catalog != nil {
				t.Fatalf("unsafe NewFileIdentityCatalog() = %#v, %v", catalog, err)
			}
		})
	}

	symlink := filepath.Join(root, "symlink")
	if err := os.Symlink(regular, symlink); err == nil {
		for name, options := range map[string]FileIdentityCatalogOptions{
			"exact symlink":     {ExactPaths: []string{symlink}},
			"tree symlink":      {TreeRoots: []string{symlink}},
			"exclusion symlink": {ExcludePaths: []string{symlink}},
		} {
			t.Run(name, func(t *testing.T) {
				catalog, catalogErr := NewFileIdentityCatalog(options)
				if catalogErr == nil || catalog != nil {
					t.Fatalf("symlink NewFileIdentityCatalog() = %#v, %v", catalog, catalogErr)
				}
			})
		}
	}

	catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		ExactPaths: []string{directory, filepath.Join(root, "missing-exact")},
		TreeRoots:  []string{filepath.Join(root, "missing-tree")},
	})
	if err != nil || catalog == nil || catalog.Len() != 0 {
		t.Fatalf("directory/missing catalog = %#v, %v", catalog, err)
	}
	catalog, err = NewFileIdentityCatalog(FileIdentityCatalogOptions{
		ExactPaths: []string{regular, regular},
	})
	if err != nil || catalog == nil || catalog.Len() != 1 {
		t.Fatalf("duplicate exact-input catalog = %#v, %v", catalog, err)
	}
}

func TestFileIdentityCatalogExclusionMatchesPhysicalIdentity(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	retained := filepath.Join(root, "retained")
	excluded := filepath.Join(root, "excluded-by-alias")
	alias := filepath.Join(outside, "excluded-alias")
	if err := os.WriteFile(retained, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(excluded, []byte("excluded"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(excluded, alias); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		TreeRoots:    []string{root},
		ExcludePaths: []string{alias},
	})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Len() != 1 {
		t.Fatalf("physical exclusion catalog length = %d, want 1", catalog.Len())
	}
	for path, want := range map[string]bool{retained: true, excluded: false, alias: false} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		protected, queryErr := catalog.ProtectsPath(path, info)
		if queryErr != nil || protected != want {
			t.Fatalf("ProtectsPath(%q) = %t, %v; want %t", filepath.Base(path), protected, queryErr, want)
		}
	}
}

func TestFileIdentityCatalogPublicLookupValidation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "protected")
	if err := os.WriteFile(path, []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{ExactPaths: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })

	if protected, lookupErr := catalog.ProtectsPath("", info); lookupErr == nil || protected {
		t.Fatalf("empty-path ProtectsPath() = %t, %v", protected, lookupErr)
	}
	if protected, lookupErr := catalog.ProtectsPath(path, nil); lookupErr == nil || protected {
		t.Fatalf("nil-info ProtectsPath() = %t, %v", protected, lookupErr)
	}
	if protected, lookupErr := catalog.ProtectsOpenedFile(nil, info); lookupErr == nil || protected {
		t.Fatalf("nil-handle ProtectsOpenedFile() = %t, %v", protected, lookupErr)
	}
	if protected, lookupErr := catalog.ProtectsOpenedFile(opened, nil); lookupErr == nil || protected {
		t.Fatalf("nil-info ProtectsOpenedFile() = %t, %v", protected, lookupErr)
	}
	var absent *FileIdentityCatalog
	if absent.Len() != 0 {
		t.Fatal("nil catalog reported identities")
	}
	if protected, lookupErr := absent.ProtectsPath(path, info); lookupErr != nil || protected {
		t.Fatalf("nil-catalog ProtectsPath() = %t, %v", protected, lookupErr)
	}
	if protected, lookupErr := absent.ProtectsOpenedFile(opened, info); lookupErr != nil || protected {
		t.Fatalf("nil-catalog ProtectsOpenedFile() = %t, %v", protected, lookupErr)
	}

	if removeErr := os.Remove(path); removeErr != nil {
		t.Fatal(removeErr)
	}
	if protected, lookupErr := catalog.ProtectsPath(path, info); lookupErr == nil || protected {
		t.Fatalf("removed-path ProtectsPath() = %t, %v", protected, lookupErr)
	}
}

func TestFileIdentityCatalogPublicLimitsAndSecondPassFailures(t *testing.T) {
	t.Run("relative input expansion is retained", func(t *testing.T) {
		root := t.TempDir()
		t.Chdir(root)
		catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
			ExactPaths: []string{"missing"},
		})
		if err != nil || catalog == nil || catalog.Len() != 0 {
			t.Fatalf("relative missing catalog = %#v, %v", catalog, err)
		}
	})

	t.Run("canonical exclusion exceeds aggregate path budget", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing")
		canonical, resolveErr := resolvePathAgainstExistingAncestor(path)
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
			ExcludePaths: []string{path},
			MaxPathBytes: int64(len(path) + len(canonical) - 1),
		})
		if err == nil || catalog != nil || !strings.Contains(err.Error(), "path-byte") {
			t.Fatalf("retained-path bounded catalog = %#v, %v", catalog, err)
		}
	})

	t.Run("relative exclusion expansion exceeds aggregate path budget", func(t *testing.T) {
		root := t.TempDir()
		t.Chdir(root)
		catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
			ExcludePaths: []string{"x"},
			MaxPathBytes: 1,
		})
		if err == nil || catalog != nil || !strings.Contains(err.Error(), "path-byte") {
			t.Fatalf("expanded-exclusion bounded catalog = %#v, %v", catalog, err)
		}
	})

	t.Run("nested directory exceeds entry budget at recursive boundary", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "child"), 0o700); err != nil {
			t.Fatal(err)
		}
		catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
			TreeRoots:  []string{root},
			MaxEntries: 1,
		})
		if err == nil || catalog != nil || !strings.Contains(err.Error(), "enumerated") {
			t.Fatalf("recursive entry-bounded catalog = %#v, %v", catalog, err)
		}
	})

	t.Run("nested directory exceeds traversal path budget", func(t *testing.T) {
		root := t.TempDir()
		deep := root
		traversalPathBytes := 1 // The tree root itself is recorded as ".".
		for traversalPathBytes <= len(root) {
			deep = filepath.Join(deep, strings.Repeat("a", 100))
			if err := os.Mkdir(deep, 0o700); err != nil {
				t.Fatal(err)
			}
			relative, err := filepath.Rel(root, deep)
			if err != nil {
				t.Fatal(err)
			}
			traversalPathBytes += len(relative)
		}
		catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
			TreeRoots:    []string{root},
			MaxPathBytes: int64(len(root)),
		})
		if err == nil || catalog != nil || !strings.Contains(err.Error(), "enumerated") {
			t.Fatalf("traversal-path bounded catalog = %#v, %v", catalog, err)
		}
	})

	if runtime.GOOS == "windows" {
		return
	}
	t.Run("second pass exact creation is rejected", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "created-between-passes")
		target := filepath.Join(root, "ordinary-target")
		if err := os.WriteFile(target, []byte("ordinary"), 0o600); err != nil {
			t.Fatal(err)
		}
		fileIdentityCatalogBetweenSnapshots = func() {
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() { fileIdentityCatalogBetweenSnapshots = nil })
		catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
			ExactPaths: []string{path},
		})
		if err == nil || catalog != nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("second-pass exact catalog = %#v, %v", catalog, err)
		}
	})

	t.Run("second pass tree creation is rejected", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "tree-created-between-passes")
		fileIdentityCatalogBetweenSnapshots = func() {
			if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() { fileIdentityCatalogBetweenSnapshots = nil })
		catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
			TreeRoots: []string{root},
		})
		if err == nil || catalog != nil {
			t.Fatalf("second-pass tree catalog = %#v, %v", catalog, err)
		}
	})
}

func TestFileIdentityCatalogRejectsUnreadableInputsWithoutPathDisclosure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits provide the deterministic unreadable-input boundary")
	}
	requireEnforcedReadMode(t)
	requirePrivateError := func(t *testing.T, options FileIdentityCatalogOptions, secret string) {
		t.Helper()
		catalog, err := NewFileIdentityCatalog(options)
		if err == nil || catalog != nil || strings.Contains(err.Error(), secret) ||
			strings.Contains(err.Error(), filepath.Base(secret)) {
			t.Fatalf("unreadable catalog = %#v, %v", catalog, err)
		}
	}

	t.Run("exact regular file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "secret-exact")
		if err := os.WriteFile(path, []byte("private"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
		requirePrivateError(t, FileIdentityCatalogOptions{ExactPaths: []string{path}}, path)
	})

	t.Run("excluded regular file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "secret-exclusion")
		if err := os.WriteFile(path, []byte("private"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
		requirePrivateError(t, FileIdentityCatalogOptions{ExcludePaths: []string{path}}, path)
	})

	t.Run("tree root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "secret-tree")
		if err := os.Mkdir(root, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
		requirePrivateError(t, FileIdentityCatalogOptions{TreeRoots: []string{root}}, root)
	})

	t.Run("execute-only tree root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "secret-execute-only-tree")
		if err := os.Mkdir(root, 0o100); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
		requirePrivateError(t, FileIdentityCatalogOptions{TreeRoots: []string{root}}, root)
	})

	t.Run("tree file", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "secret-child")
		if err := os.WriteFile(path, []byte("private"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
		requirePrivateError(t, FileIdentityCatalogOptions{TreeRoots: []string{root}}, path)
	})

	t.Run("tree child directory", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "secret-child-directory")
		if err := os.Mkdir(path, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
		requirePrivateError(t, FileIdentityCatalogOptions{TreeRoots: []string{root}}, path)
	})

	t.Run("inaccessible parent", func(t *testing.T) {
		for _, test := range []struct {
			name    string
			options func(string) FileIdentityCatalogOptions
		}{
			{name: "exact", options: func(path string) FileIdentityCatalogOptions {
				return FileIdentityCatalogOptions{ExactPaths: []string{path}}
			}},
			{name: "tree", options: func(path string) FileIdentityCatalogOptions {
				return FileIdentityCatalogOptions{TreeRoots: []string{path}}
			}},
			{name: "exclusion", options: func(path string) FileIdentityCatalogOptions {
				return FileIdentityCatalogOptions{ExcludePaths: []string{path}}
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				parent := filepath.Join(t.TempDir(), "secret-parent")
				if err := os.Mkdir(parent, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
				path := filepath.Join(parent, "private-child")
				requirePrivateError(t, test.options(path), path)
			})
		}
	})
}

func TestFileIdentityCatalogRejectsRelativeInputsWhenWorkingDirectoryDisappears(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not permit removing the process working directory")
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	removed := filepath.Join(t.TempDir(), "removed-working-directory")
	if err = os.Mkdir(removed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(removed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if restoreErr := os.Chdir(original); restoreErr != nil {
			t.Errorf("restore working directory: %v", restoreErr)
		}
	})
	if err = os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	for name, options := range map[string]FileIdentityCatalogOptions{
		"exact":     {ExactPaths: []string{"relative"}},
		"exclusion": {ExcludePaths: []string{"relative"}},
	} {
		t.Run(name, func(t *testing.T) {
			catalog, catalogErr := NewFileIdentityCatalog(options)
			if catalogErr == nil || catalog != nil {
				t.Fatalf("removed-cwd catalog = %#v, %v", catalog, catalogErr)
			}
		})
	}
}

func TestFileIdentityCatalogPinnedTraversalRejectsRealHandleLifecycleFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits provide the deterministic directory-open boundary")
	}
	requireEnforcedReadMode(t)
	openTraversal := func(t *testing.T, rootPath string) (*os.Root, *fileIdentityCatalogRecorder, *fileIdentityCatalogBudget) {
		t.Helper()
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			t.Fatal(err)
		}
		return root, newFileIdentityCatalogRecorder(false), &fileIdentityCatalogBudget{
			limits: fileIdentityCatalogLimits{entries: 8, pathBytes: 1024, depth: 4},
		}
	}

	t.Run("closed pinned root", func(t *testing.T) {
		rootPath := t.TempDir()
		root, recorder, budget := openTraversal(t, rootPath)
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
		err := collectPinnedFileIdentityCatalogTree(
			root, rootPath, ".", fileIdentityCatalogExclusions{}, 1, budget, recorder,
		)
		if err == nil || !strings.Contains(err.Error(), "directory is unavailable") {
			t.Fatalf("closed pinned-root traversal error = %v", err)
		}
	})

	t.Run("directory loses read authority after root pin", func(t *testing.T) {
		rootPath := filepath.Join(t.TempDir(), "pinned-tree")
		if err := os.Mkdir(rootPath, 0o700); err != nil {
			t.Fatal(err)
		}
		root, recorder, budget := openTraversal(t, rootPath)
		t.Cleanup(func() {
			_ = os.Chmod(rootPath, 0o700)
			_ = root.Close()
		})
		if err := os.Chmod(rootPath, 0o100); err != nil {
			t.Fatal(err)
		}
		err := collectPinnedFileIdentityCatalogTree(
			root, rootPath, ".", fileIdentityCatalogExclusions{}, 1, budget, recorder,
		)
		if err == nil || !strings.Contains(err.Error(), "cannot be opened") {
			t.Fatalf("revoked pinned-root traversal error = %v", err)
		}
	})

	t.Run("directory cannot become a regular-file snapshot", func(t *testing.T) {
		rootPath := t.TempDir()
		info, err := os.Stat(rootPath)
		if err != nil {
			t.Fatal(err)
		}
		if identity, identityErr := snapshotFileIdentity(rootPath, info); identityErr == nil || identity != "" {
			t.Fatalf("directory snapshot identity = %q, %v", identity, identityErr)
		}
	})
}

func requireEnforcedReadMode(t *testing.T) {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "unreadable-mode-probe")
	if err := os.WriteFile(probe, []byte("probe"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(probe, 0o600) })
	file, err := os.Open(probe)
	if err == nil {
		_ = file.Close()
		t.Skip("process privileges bypass Unix read mode bits")
	}
	if !os.IsPermission(err) {
		t.Fatalf("read-mode capability probe failed unexpectedly: %v", err)
	}
}
