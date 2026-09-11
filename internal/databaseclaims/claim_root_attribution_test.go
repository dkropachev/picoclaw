package databaseclaims

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/sipeed/picoclaw/internal/storecatalog"
)

// stableClaimFileNamesForCatalog returns only the opaque claim filenames that
// this exact test-owned catalog could create. Tests retain this set across an
// operation instead of snapshotting the shared stable claim directory, which
// may be changed legitimately by another concurrently running package.
func stableClaimFileNamesForCatalog(t *testing.T, catalog *storecatalog.Catalog) []string {
	t.Helper()
	if catalog == nil {
		t.Fatal("catalog for stable claim attribution is unavailable")
	}
	lexical, err := catalog.ClaimIDs()
	if err != nil {
		t.Fatal(err)
	}
	physical, err := physicalClaimIdentitiesForStores(catalog.All())
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]struct{}, len(lexical)+len(physical))
	for _, identity := range lexical {
		names[identity.String()+".lock"] = struct{}{}
	}
	for _, identity := range physical {
		names[identity+".lock"] = struct{}{}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func assertStableClaimFilesAbsent(t *testing.T, names []string) {
	t.Helper()
	if len(names) == 0 {
		t.Fatal("stable claim attribution set is empty")
	}
	cache, err := stableClaimCacheRoot()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(cache, claimApplicationDirectory, claimDirectoryName)
	for _, name := range names {
		_, statErr := os.Lstat(filepath.Join(root, name))
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			t.Fatalf("cannot prove stable claim identity %q is absent", name)
		}
		t.Fatalf("test-specific stable claim identity %q exists", name)
	}
}
