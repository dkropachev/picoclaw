//go:build unix

package databaseclaims

import (
	"os"
	"path/filepath"
	"testing"
)

func isolatedTestClaimRoot(t *testing.T) string {
	t.Helper()
	root, err := PrepareRootForTesting(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func productionHierarchyTestRoot(t *testing.T) string {
	t.Helper()
	cache, err := stableClaimCacheRoot()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Dir(cache), ".picoclaw-claim-hierarchy-test-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}
