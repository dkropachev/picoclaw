package storecatalog

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestClaimIDsAreOpaqueSortedDeterministicAndComplete(t *testing.T) {
	home := t.TempDir()
	catalog, err := Project(catalogTestOptions(t, home, &config.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	first, err := catalog.ClaimIDs()
	if err != nil {
		t.Fatal(err)
	}
	second, err := catalog.ClaimIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 || !slices.Equal(first, second) || !slices.IsSorted(first) {
		t.Fatalf("ClaimIDs() = %#v then %#v", first, second)
	}
	seen := make(map[ClaimID]struct{}, len(first))
	for _, identity := range first {
		if !identity.Valid() || identity.String() != string(identity) ||
			strings.Contains(identity.String(), filepath.Clean(home)) {
			t.Fatalf("unsafe claim identity %q", identity)
		}
		if _, duplicate := seen[identity]; duplicate {
			t.Fatalf("duplicate claim identity %q", identity)
		}
		seen[identity] = struct{}{}
	}

	specs := catalog.All()
	want := make(map[ClaimID]struct{})
	for _, spec := range specs {
		for _, path := range []string{spec.Path, spec.Path + "-wal", spec.Path + "-shm", spec.Path + "-journal"} {
			want[claimIDForCatalogPath(path)] = struct{}{}
		}
		for _, path := range spec.LegacyRoots {
			want[claimIDForCatalogPath(path)] = struct{}{}
		}
	}
	if len(first) != len(want) {
		t.Fatalf("claim count = %d, want %d", len(first), len(want))
	}
	for identity := range want {
		if _, ok := seen[identity]; !ok {
			t.Errorf("missing claim identity %q", identity)
		}
	}
}

func TestClaimIDValidationAndInvalidCatalogBoundaries(t *testing.T) {
	valid := claimIDForCatalogPath(filepath.Join(t.TempDir(), "state.db"))
	if !valid.Valid() {
		t.Fatalf("generated identity %q is invalid", valid)
	}
	for _, invalid := range []ClaimID{
		"", "short", ClaimID(strings.Repeat("A", 64)), ClaimID(strings.Repeat("g", 64)),
	} {
		if invalid.Valid() {
			t.Errorf("ClaimID(%q).Valid() = true", invalid)
		}
	}

	validPath := filepath.Join(t.TempDir(), "auth.db")
	for _, catalog := range []*Catalog{
		nil,
		{},
		{home: validPath},
		{home: validPath, specs: []Spec{{ID: "bad id", Path: validPath}}},
		{home: validPath, specs: []Spec{{ID: "global/auth"}}},
		{home: validPath, specs: []Spec{{ID: "global/auth", Path: validPath, LegacyRoots: []string{""}}}},
	} {
		if claims, err := catalog.ClaimIDs(); claims != nil || err == nil {
			t.Errorf("invalid Catalog.ClaimIDs() = %#v, %v", claims, err)
		}
	}
}

func TestClaimIDUsesCanonicalCatalogPathKey(t *testing.T) {
	root := t.TempDir()
	canonical := filepath.Join(root, "state.db")
	alias := filepath.Join(root, ".", "nested", "..", "state.db")
	if got, want := claimIDForCatalogPath(alias), claimIDForCatalogPath(canonical); got != want {
		t.Fatalf("canonical claim identity = %q, want %q", got, want)
	}
}
