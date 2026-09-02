package fstools

import (
	"os"
	"path/filepath"
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
}
