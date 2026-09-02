//go:build darwin

package fstools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileIdentityCatalogPreservesCaseDistinctDarwinInputsAndExclusions(t *testing.T) {
	root := t.TempDir()
	lower := filepath.Join(root, "sessions.db")
	upper := filepath.Join(root, "SESSIONS.DB")
	if err := os.WriteFile(lower, []byte("volatile"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(upper, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	lowerInfo, err := os.Stat(lower)
	if err != nil {
		t.Fatal(err)
	}
	upperInfo, err := os.Stat(upper)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(lowerInfo, upperInfo) {
		t.Skip("test volume is case-insensitive")
	}
	catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		TreeRoots: []string{root}, ExcludePaths: []string{lower},
	})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Len() != 1 {
		t.Fatalf("case-distinct exclusion catalog identities = %d, want 1", catalog.Len())
	}
	protected, err := catalog.ProtectsPath(upper, upperInfo)
	if err != nil || !protected {
		t.Fatalf("case-distinct retained identity protected=%t err=%v", protected, err)
	}
	if protected, err = catalog.ProtectsPath(lower, lowerInfo); err != nil || protected {
		t.Fatalf("exact excluded identity protected=%t err=%v", protected, err)
	}

	lowerTree := filepath.Join(root, "tree")
	upperTree := filepath.Join(root, "TREE")
	for _, directory := range []string{lowerTree, upperTree} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "state.json"), []byte(directory), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err = NewFileIdentityCatalog(FileIdentityCatalogOptions{
		TreeRoots: []string{lowerTree, upperTree},
	})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Len() != 2 {
		t.Fatalf("case-distinct tree catalog identities = %d, want 2", catalog.Len())
	}
}
