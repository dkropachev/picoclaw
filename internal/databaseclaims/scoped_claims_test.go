//nolint:govet // Independent failure-boundary assertions intentionally use narrow scopes.
package databaseclaims

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func (acquirer testClaimAcquirer) AcquireReviewScope(
	scope *storecatalog.ReviewScope,
	fence *database.Fence,
) (*Lease, error) {
	return AcquireReviewScopeForTesting(scope, fence, acquirer.root)
}

func newClaimsReviewScope(
	t *testing.T,
	home string,
	cfg *config.Config,
) (*storecatalog.ReviewScope, storecatalog.Options) {
	t.Helper()
	if cfg == nil {
		cfg = config.DefaultConfig()
		cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	}
	options := storecatalog.Options{
		Home: home, Config: cfg, UserHome: secureTestDir(t),
	}
	scope, err := storecatalog.NewReviewScope(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	assertReviewScopeTestOwned(t, scope, home, options.UserHome)
	return scope, options
}

func assertReviewScopeTestOwned(
	t *testing.T,
	scope *storecatalog.ReviewScope,
	roots ...string,
) {
	t.Helper()
	full, _, err := storecatalog.ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range full.All() {
		paths := append([]string{store.Path}, store.LegacyRoots...)
		for _, path := range paths {
			owned := false
			for _, root := range roots {
				relative, relErr := filepath.Rel(root, path)
				if relErr == nil && relative != ".." &&
					!strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
					owned = true
					break
				}
			}
			if !owned {
				t.Fatalf("review-scope test path escaped test-owned roots: %q", path)
			}
		}
	}
}

func TestAcquireReviewScopeClaimsAndExposesOnlySelectedCatalog(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	cfg.Agents.List = []config.AgentConfig{{
		ID: "worker", Workspace: filepath.Join(home, "worker"),
	}}
	scope, _ := newClaimsReviewScope(t, home, cfg)
	full, selected, err := storecatalog.ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.AcquireReviewScope(scope, fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()

	bindings := scope.Bindings()
	storeIDs := lease.StoreIDs()
	if len(lease.Stores()) != len(bindings) || len(storeIDs) != len(bindings) ||
		len(full.All()) <= len(selected.All()) {
		t.Fatalf(
			"review lease sizes stores=%d IDs=%d bindings=%d full=%d selected=%d",
			len(lease.Stores()), len(storeIDs), len(bindings), len(full.All()), len(selected.All()),
		)
	}
	for index, binding := range bindings {
		if storeIDs[index] != binding.ID {
			t.Fatalf("StoreIDs[%d] = %q, want %q", index, storeIDs[index], binding.ID)
		}
		store, found := lease.Lookup(binding.ID)
		if !found || store.ID != binding.ID || store.Domain != binding.Domain {
			t.Fatalf("Lookup(%q) = %#v, %t", binding.ID, store, found)
		}
	}
	if lease.ScopeFingerprint() != scope.Fingerprint() ||
		lease.FullCatalogFingerprint() != scope.FullFingerprint() {
		t.Fatalf(
			"lease fingerprints = %q/%q, want %q/%q",
			lease.ScopeFingerprint(), lease.FullCatalogFingerprint(),
			scope.Fingerprint(), scope.FullFingerprint(),
		)
	}
	storeIDs[0] = "forged/id"
	stores := lease.Stores()
	stores[0].Path = "forged"
	if lease.StoreIDs()[0] == "forged/id" || lease.Stores()[0].Path == "forged" {
		t.Fatal("review lease exposed retained scope state")
	}

	selectedClaims, err := selected.ClaimIDs()
	if err != nil {
		t.Fatal(err)
	}
	fullClaims, err := full.ClaimIDs()
	if err != nil {
		t.Fatal(err)
	}
	selectedSet := make(map[storecatalog.ClaimID]struct{}, len(selectedClaims))
	for _, id := range selectedClaims {
		selectedSet[id] = struct{}{}
		if _, statErr := os.Lstat(filepath.Join(testClaims.root, id.String()+".lock")); statErr != nil {
			t.Fatalf("selected claim %q is absent: %v", id, statErr)
		}
	}
	unselectedChecked := 0
	for _, id := range fullClaims {
		if _, selectedID := selectedSet[id]; selectedID {
			continue
		}
		unselectedChecked++
		if _, statErr := os.Lstat(
			filepath.Join(testClaims.root, id.String()+".lock"),
		); !errors.Is(
			statErr,
			os.ErrNotExist,
		) {
			t.Fatalf("unselected claim %q exists or is unreadable: %v", id, statErr)
		}
	}
	if unselectedChecked == 0 {
		t.Fatal("complete catalog exposed no unselected claim for verification")
	}
}

func TestReviewScopeToleratesUnselectedGenerationChurnWithoutClaims(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	full, _, err := storecatalog.ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	unselected, found := full.Lookup("global/auth")
	if !found {
		t.Fatal("unselected auth store missing")
	}
	if err := os.MkdirAll(filepath.Dir(unselected.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.AcquireReviewScope(scope, fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	initialClaims := len(lease.claims)

	for generation := 0; generation < 3; generation++ {
		if err := os.WriteFile(unselected.Path, []byte{byte(generation)}, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := lease.Check(); err != nil {
			t.Fatalf("unselected generation %d Check() = %v", generation, err)
		}
		physical, err := physicalClaimIdentitiesForStores([]storecatalog.Spec{unselected})
		if err != nil || len(physical) == 0 {
			t.Fatalf("unselected physical identities = %#v, %v", physical, err)
		}
		for _, identity := range physical {
			if _, statErr := os.Lstat(
				filepath.Join(testClaims.root, identity+".lock"),
			); !errors.Is(
				statErr,
				os.ErrNotExist,
			) {
				t.Fatalf("unselected physical claim %q exists: %v", identity, statErr)
			}
		}
		if err := os.Remove(unselected.Path); err != nil {
			t.Fatal(err)
		}
		if err := lease.Check(); err != nil {
			t.Fatalf("removed unselected generation %d Check() = %v", generation, err)
		}
	}
	if len(lease.claims) != initialClaims {
		t.Fatalf("unselected churn changed claims %d -> %d", initialClaims, len(lease.claims))
	}
}

func TestReviewScopeToleratesUnselectedIdentityMovingBetweenMembers(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	full, _, err := storecatalog.ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	first, found := full.Lookup("global/auth")
	if !found {
		t.Fatal("first unselected store missing")
	}
	second, found := full.Lookup("global/model-catalogs")
	if !found {
		t.Fatal("second unselected store missing")
	}
	for _, path := range []string{first.Path, second.Path} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.AcquireReviewScope(scope, fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	initialClaims := len(lease.claims)
	if err := os.WriteFile(first.Path, []byte("unselected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lease.Check(); err != nil {
		t.Fatalf("first unselected assignment Check() = %v", err)
	}
	identities, err := physicalClaimIdentitiesForStores([]storecatalog.Spec{first})
	if err != nil || len(identities) == 0 {
		t.Fatalf("first unselected identities = %#v, %v", identities, err)
	}
	if err := os.Rename(first.Path, second.Path); err != nil {
		t.Fatal(err)
	}
	if err := lease.Check(); err != nil {
		t.Fatalf("moved unselected assignment Check() = %v", err)
	}
	if len(lease.claims) != initialClaims {
		t.Fatalf("unselected identity movement changed claims %d -> %d", initialClaims, len(lease.claims))
	}
	for _, identity := range identities {
		if _, err := os.Lstat(filepath.Join(testClaims.root, identity+".lock")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("moved unselected identity was claimed: %v", err)
		}
	}
}

func TestReviewScopeRefreshClaimsSelectedButNotUnselectedMaterialization(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	full, selected, err := storecatalog.ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	selectedStore, found := selected.Lookup("workspace/repository-reviews")
	if !found {
		t.Fatal("selected review store missing")
	}
	unselectedStore, found := full.Lookup("global/auth")
	if !found {
		t.Fatal("unselected auth store missing")
	}
	for _, path := range []string{selectedStore.Path, unselectedStore.Path} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.AcquireReviewScope(scope, fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	before := len(lease.claims)
	if err := os.WriteFile(selectedStore.Path, []byte("selected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unselectedStore.Path, []byte("unselected"), 0o600); err != nil {
		t.Fatal(err)
	}
	selectedPhysicalBefore, physicalErr := physicalClaimIdentitiesForStores([]storecatalog.Spec{selectedStore})
	if physicalErr != nil || len(selectedPhysicalBefore) == 0 {
		t.Fatalf("selected identity before refresh = %#v, %v", selectedPhysicalBefore, physicalErr)
	}
	newSelectedClaims := 0
	for _, identity := range selectedPhysicalBefore {
		_, alreadyHeld := lease.identities[identity]
		if !alreadyHeld {
			newSelectedClaims++
		}
	}
	if err := lease.Refresh(); err != nil {
		t.Fatal(err)
	}
	if newSelectedClaims == 0 || len(lease.claims) != before+newSelectedClaims {
		t.Fatalf(
			"materialization claims = %d, want %d (new selected=%d)",
			len(lease.claims), before+newSelectedClaims, newSelectedClaims,
		)
	}
	selectedPhysical, _ := physicalClaimIdentitiesForStores([]storecatalog.Spec{selectedStore})
	unselectedPhysical, _ := physicalClaimIdentitiesForStores([]storecatalog.Spec{unselectedStore})
	if len(selectedPhysical) == 0 || len(unselectedPhysical) == 0 {
		t.Fatalf("physical identities selected=%#v unselected=%#v", selectedPhysical, unselectedPhysical)
	}
	for _, identity := range selectedPhysical {
		if _, err := os.Lstat(filepath.Join(testClaims.root, identity+".lock")); err != nil {
			t.Fatalf("selected physical claim %q missing: %v", identity, err)
		}
	}
	for _, identity := range unselectedPhysical {
		if _, err := os.Lstat(filepath.Join(testClaims.root, identity+".lock")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unselected physical claim %q exists: %v", identity, err)
		}
	}
}

func TestReviewScopeRejectsLateSelectedUnselectedPhysicalAlias(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	full, selected, err := storecatalog.ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	selectedStore, _ := selected.Lookup("workspace/repository-reviews")
	unselectedStore, _ := full.Lookup("global/auth")
	for _, path := range []string{selectedStore.Path, unselectedStore.Path} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.AcquireReviewScope(scope, fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := os.WriteFile(selectedStore.Path, []byte("shared"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lease.Refresh(); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(selectedStore.Path, unselectedStore.Path); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("late selected/unselected alias Check() = %v", err)
	}
	if lease.ScopeFingerprint() != "" || lease.StoreIDs() != nil {
		t.Fatal("aliased review lease still exposed scope")
	}
}

func TestReviewScopeConflictsWithReviewAndFullClaimsInBothOrders(t *testing.T) {
	for _, firstKind := range []string{"review", "full"} {
		t.Run(firstKind+" first", func(t *testing.T) {
			testClaims := newTestClaimAcquirer(t)
			home := secureTestDir(t)
			scope, options := newClaimsReviewScope(t, home, nil)
			fence, err := database.AcquireOnlineFence(home)
			if err != nil {
				t.Fatal(err)
			}
			defer fence.Close()
			var first *Lease
			if firstKind == "review" {
				first, err = testClaims.AcquireReviewScope(scope, fence)
			} else {
				first, err = testClaims.Acquire(options, fence)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			var competing *Lease
			if firstKind == "review" {
				competing, err = testClaims.Acquire(options, fence)
			} else {
				competing, err = testClaims.AcquireReviewScope(scope, fence)
			}
			if competing != nil || database.CodeOf(err) != database.CodeConflict {
				t.Fatalf("competing lease = %#v, %v", competing, err)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			if firstKind == "review" {
				competing, err = testClaims.Acquire(options, fence)
			} else {
				competing, err = testClaims.AcquireReviewScope(scope, fence)
			}
			if err != nil || competing == nil {
				t.Fatalf("lease after release = %#v, %v", competing, err)
			}
			if err := competing.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("review review", func(t *testing.T) {
		testClaims := newTestClaimAcquirer(t)
		home := secureTestDir(t)
		scope, _ := newClaimsReviewScope(t, home, nil)
		fence, err := database.AcquireOnlineFence(home)
		if err != nil {
			t.Fatal(err)
		}
		defer fence.Close()
		first, err := testClaims.AcquireReviewScope(scope, fence)
		if err != nil {
			t.Fatal(err)
		}
		defer first.Close()
		second, err := testClaims.AcquireReviewScope(scope, fence)
		if second != nil || database.CodeOf(err) != database.CodeConflict {
			t.Fatalf("second review lease = %#v, %v", second, err)
		}
	})
}

func TestReviewScopeRejectsUnselectedStageAliasWithoutClaimingIt(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	full, _, err := storecatalog.ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	unselected, _ := full.Lookup("global/auth")
	if err := os.WriteFile(unselected.Path, []byte("unselected"), 0o600); err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.AcquireReviewScope(scope, fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	stage := filepath.Join(home, "review-stage.db")
	if err := os.Link(unselected.Path, stage); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if err := lease.PinReplacement(
		"workspace/repository-reviews",
		stage,
	); database.CodeOf(
		err,
	) != database.CodeIntegrity {
		t.Fatalf("unselected-alias PinReplacement() = %v", err)
	}
	physical, _ := physicalClaimIdentitiesForStores([]storecatalog.Spec{unselected})
	for _, identity := range physical {
		if _, statErr := os.Lstat(
			filepath.Join(testClaims.root, identity+".lock"),
		); !errors.Is(
			statErr,
			os.ErrNotExist,
		) {
			t.Fatalf("unselected identity was claimed: %v", statErr)
		}
	}
}

func TestReviewScopeAcquisitionFaultClosesEverySelectedClaim(t *testing.T) {
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	claimOps := defaultClaimAcquireOps()
	claimOps.claimRoot = func() (string, error) { return "test-root", nil }
	closed := 0
	acquired := 0
	canary := errors.New("selected claim acquisition canary")
	claimOps.acquire = func(string, string) (claimHandle, error) {
		acquired++
		if acquired == 3 {
			return nil, canary
		}
		return &coverageClaimHandle{closed: &closed}, nil
	}
	lease, err := acquireReviewScopeWithOps(
		scope,
		fence,
		claimOps,
		defaultReviewScopeValidationOps(),
	)
	if lease != nil || !errors.Is(err, canary) || closed != 2 {
		t.Fatalf("fault acquisition = %#v, %v; acquired=%d closed=%d", lease, err, acquired, closed)
	}
}

func TestReviewScopeSafeMetadataFailsClosedWithAuthority(t *testing.T) {
	var absent *Lease
	if absent.ScopeFingerprint() != "" || absent.FullCatalogFingerprint() != "" ||
		absent.StoreIDs() != nil {
		t.Fatal("nil lease exposed scoped metadata")
	}
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := testClaims.AcquireReviewScope(scope, fence)
	if err != nil {
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	if lease.ScopeFingerprint() != "" || lease.FullCatalogFingerprint() != "" ||
		lease.StoreIDs() != nil || lease.Stores() != nil {
		t.Fatal("expired review lease exposed scoped metadata")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReviewScopeStoreIDsAreSortedAndExact(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.AcquireReviewScope(scope, fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	got := lease.StoreIDs()
	wantBindings := scope.Bindings()
	want := make([]database.StoreID, len(wantBindings))
	for index := range wantBindings {
		want[index] = wantBindings[index].ID
	}
	if !slices.Equal(got, want) || !slices.IsSorted(got) {
		t.Fatalf("StoreIDs() = %#v, want %#v", got, want)
	}
	if strings.Contains(strings.Join(storeIDStrings(got), ","), home) {
		t.Fatal("StoreIDs exposed physical home")
	}
}

func TestAcquireReviewScopeRejectsInvalidAuthorityWithoutStableMutation(t *testing.T) {
	if lease, err := AcquireReviewScope(nil, nil); lease != nil ||
		database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("AcquireReviewScope(nil) = %#v, %v", lease, err)
	}
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	_, selected, err := storecatalog.ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	stableNames := stableClaimFileNamesForCatalog(t, selected)
	assertStableClaimFilesAbsent(t, stableNames)
	if lease, err := AcquireReviewScope(scope, nil); lease != nil ||
		database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("AcquireReviewScope(without fence) = %#v, %v", lease, err)
	}
	assertStableClaimFilesAbsent(t, stableNames)
}

func TestAcquireReviewScopeForTestingRejectsUnsafeExplicitRoot(t *testing.T) {
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	root := filepath.Join(t.TempDir(), "missing")
	if lease, err := AcquireReviewScopeForTesting(scope, fence, root); lease != nil || err == nil {
		t.Fatalf("AcquireReviewScopeForTesting(unsafe root) = %#v, %v", lease, err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe review claim root was mutated: %v", err)
	}
}

func TestReviewScopeRejectsInvalidValidationOperations(t *testing.T) {
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	if state, selected, err := newReviewScopeLeaseState(
		scope,
		reviewScopeValidationOps{},
	); state != nil || selected != nil ||
		database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid review validation operations = %#v, %#v, %v", state, selected, err)
	}
	claimOps := defaultClaimAcquireOps()
	claimOps.observe = nil
	if lease, err := acquireReviewScopeWithOps(
		scope, fence, claimOps, defaultReviewScopeValidationOps(),
	); lease != nil || database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid selected claim operations = %#v, %v", lease, err)
	}
}

func TestReviewScopeValidationFaultClosesEverySelectedClaim(t *testing.T) {
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	claimOps := defaultClaimAcquireOps()
	claimOps.claimRoot = func() (string, error) { return "test-root", nil }
	acquired := 0
	closed := 0
	claimOps.acquire = func(string, string) (claimHandle, error) {
		acquired++
		return &coverageClaimHandle{closed: &closed}, nil
	}
	canary := errors.New("full-scope observation canary")
	scopeOps := defaultReviewScopeValidationOps()
	observations := 0
	scopeOps.observe = func(catalog *storecatalog.Catalog) ([]memberObservation, error) {
		observations++
		if observations == 2 {
			return nil, canary
		}
		return observeCatalog(catalog)
	}
	lease, err := acquireReviewScopeWithOps(scope, fence, claimOps, scopeOps)
	if lease != nil || database.CodeOf(err) != database.CodeIntegrity ||
		errors.Is(err, canary) || acquired == 0 || closed != acquired {
		t.Fatalf(
			"validation fault = %#v, %v; observed=%d acquired=%d closed=%d",
			lease, err, observations, acquired, closed,
		)
	}
}

func TestReviewScopeRetainsSelectedAssignmentHistoryAgainstUnselectedReuse(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	full, selected, err := storecatalog.ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	selectedStore, _ := selected.Lookup("workspace/repository-reviews")
	unselectedStore, _ := full.Lookup("global/auth")
	for _, path := range []string{selectedStore.Path, unselectedStore.Path} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(selectedStore.Path, []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	selectedWAL := selectedStore.Path + "-wal"
	if err := os.WriteFile(selectedWAL, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.AcquireReviewScope(scope, fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	retired := filepath.Join(home, "retired-selected-wal")
	if err := os.Rename(selectedWAL, retired); err != nil {
		t.Fatal(err)
	}
	if err := lease.Refresh(); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(retired, unselectedStore.Path); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("selected identity reassigned to unselected member Check() = %v", err)
	}
}

func TestReviewScopeDetectsCurrentAliasAtEveryOwnershipBoundary(t *testing.T) {
	for _, test := range []struct {
		name      string
		migration bool
		run       func(*Lease) error
	}{
		{name: "guard", run: func(lease *Lease) error {
			release, err := lease.Guard()
			if release != nil {
				release()
			}
			return err
		}},
		{name: "refresh", run: func(lease *Lease) error { return lease.Refresh() }},
		{name: "migration guard", migration: true, run: func(lease *Lease) error {
			guard, err := lease.GuardStoresMigrating()
			if guard != nil {
				guard.Release()
			}
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			testClaims := newTestClaimAcquirer(t)
			home := secureTestDir(t)
			scope, _ := newClaimsReviewScope(t, home, nil)
			full, selected, err := storecatalog.ReviewScopeCatalogs(scope)
			if err != nil {
				t.Fatal(err)
			}
			selectedStore, _ := selected.Lookup("workspace/repository-reviews")
			unselectedStore, _ := full.Lookup("global/auth")
			for _, path := range []string{selectedStore.Path, unselectedStore.Path} {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(selectedStore.Path, []byte("selected"), 0o600); err != nil {
				t.Fatal(err)
			}
			var fence *database.Fence
			if test.migration {
				fence, err = database.AcquireMigrationFence(home)
			} else {
				fence, err = database.AcquireOnlineFence(home)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer fence.Close()
			lease, err := testClaims.AcquireReviewScope(scope, fence)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			if err := os.Link(selectedStore.Path, unselectedStore.Path); err != nil {
				t.Skipf("hardlinks unavailable: %v", err)
			}
			if err := test.run(lease); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("alias boundary = %v", err)
			}
		})
	}
}

func TestReviewScopeRejectsActiveAndDiscardedPinReuseByUnselectedStore(t *testing.T) {
	for _, discard := range []bool{false, true} {
		name := "active"
		if discard {
			name = "discarded"
		}
		t.Run(name, func(t *testing.T) {
			testClaims := newTestClaimAcquirer(t)
			home := secureTestDir(t)
			scope, _ := newClaimsReviewScope(t, home, nil)
			full, selected, err := storecatalog.ReviewScopeCatalogs(scope)
			if err != nil {
				t.Fatal(err)
			}
			selectedStore, _ := selected.Lookup("workspace/repository-reviews")
			unselectedStore, _ := full.Lookup("global/auth")
			if err := os.MkdirAll(filepath.Dir(unselectedStore.Path), 0o700); err != nil {
				t.Fatal(err)
			}
			stage := filepath.Join(home, "selected-stage.db")
			if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
				t.Fatal(err)
			}
			fence, err := database.AcquireMigrationFence(home)
			if err != nil {
				t.Fatal(err)
			}
			defer fence.Close()
			lease, err := testClaims.AcquireReviewScope(scope, fence)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			guard, err := lease.GuardStoresMigrating()
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Release()
			if err := guard.PinReplacement(selectedStore.ID, stage); err != nil {
				t.Fatal(err)
			}
			if discard {
				if err := guard.DiscardReplacement(selectedStore.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Link(stage, unselectedStore.Path); err != nil {
				t.Skipf("hardlinks unavailable: %v", err)
			}
			if err := guard.Check(); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("%s selected stage assigned to unselected store = %v", name, err)
			}
		})
	}
}

func TestReviewScopeGuardReleaseDetectsTransientAlias(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	full, selected, err := storecatalog.ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	selectedStore, _ := selected.Lookup("workspace/repository-reviews")
	unselectedStore, _ := full.Lookup("global/auth")
	for _, path := range []string{selectedStore.Path, unselectedStore.Path} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(selectedStore.Path, []byte("selected"), 0o600); err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.AcquireReviewScope(scope, fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	release, err := lease.Guard()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(selectedStore.Path, unselectedStore.Path); err != nil {
		release()
		t.Skipf("hardlinks unavailable: %v", err)
	}
	release()
	if err := os.Remove(unselectedStore.Path); err != nil {
		t.Fatal(err)
	}
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("lease survived transient alias at Guard release = %v", err)
	}
}

func TestReviewScopeRefreshingGuardReleaseDetectsTransientClaimLoss(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.AcquireReviewScope(scope, fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	_, _, release, err := lease.GuardStoresRefreshing()
	if err != nil {
		t.Fatal(err)
	}
	if len(lease.claims) == 0 {
		release()
		t.Fatal("review lease retained no claim to invalidate")
	}
	original := lease.claims[0]
	lease.claims[0] = &coverageInvalidClaimHandle{}
	release()
	lease.claims[0] = original
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("lease survived transient claim loss at refreshing release = %v", err)
	}
}

func TestReviewScopeMigrationGuardRejectsUnclaimedSelectedExposure(t *testing.T) {
	for _, operation := range []string{"check", "stores"} {
		t.Run(operation, func(t *testing.T) {
			testClaims := newTestClaimAcquirer(t)
			home := secureTestDir(t)
			scope, _ := newClaimsReviewScope(t, home, nil)
			_, selected, err := storecatalog.ReviewScopeCatalogs(scope)
			if err != nil {
				t.Fatal(err)
			}
			selectedStore, _ := selected.Lookup("workspace/repository-reviews")
			if err := os.MkdirAll(filepath.Dir(selectedStore.Path), 0o700); err != nil {
				t.Fatal(err)
			}
			fence, err := database.AcquireMigrationFence(home)
			if err != nil {
				t.Fatal(err)
			}
			defer fence.Close()
			lease, err := testClaims.AcquireReviewScope(scope, fence)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			guard, err := lease.GuardStoresMigrating()
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Release()
			if err := os.WriteFile(selectedStore.Path+"-wal", []byte("wal"), 0o600); err != nil {
				t.Fatal(err)
			}
			if operation == "check" {
				err = guard.Check()
			} else {
				var stores []storecatalog.Spec
				stores, err = guard.Stores()
				if stores != nil {
					t.Fatalf("Stores() exposed unclaimed selected state: %#v", stores)
				}
			}
			if database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("%s with unclaimed selected sidecar = %v", operation, err)
			}
		})
	}
}

func TestReviewScopeMigrationReconcileClaimsNewSelectedSidecar(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	_, selected, err := storecatalog.ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	selectedStore, _ := selected.Lookup("workspace/repository-reviews")
	if err := os.MkdirAll(filepath.Dir(selectedStore.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.AcquireReviewScope(scope, fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	guard, err := lease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	sidecar := selectedStore.Path + "-wal"
	if err := os.WriteFile(sidecar, []byte("wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := guard.Reconcile(); err != nil {
		t.Fatalf("Reconcile() selected sidecar = %v", err)
	}
	if err := guard.Check(); err != nil {
		t.Fatalf("Check() after selected sidecar reconcile = %v", err)
	}
	if err := guard.Release(); err != nil {
		t.Fatalf("Release() after selected sidecar reconcile = %v", err)
	}
}

func TestReviewScopeMigrationGuardRejectsUnpinnedSelectedMainReplacement(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	_, selected, err := storecatalog.ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	selectedStore, _ := selected.Lookup("workspace/repository-reviews")
	if err := os.MkdirAll(filepath.Dir(selectedStore.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(selectedStore.Path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.AcquireReviewScope(scope, fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	guard, err := lease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	replacement := filepath.Join(home, "unpinned.db")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(selectedStore.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, selectedStore.Path); err != nil {
		t.Fatal(err)
	}
	if err := guard.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("unpinned selected main replacement Check() = %v", err)
	}
}

func TestReviewScopeMigrationGuardAcceptsExactPinnedCutover(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	_, selected, err := storecatalog.ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	selectedStore, _ := selected.Lookup("workspace/repository-reviews")
	if err := os.MkdirAll(filepath.Dir(selectedStore.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.AcquireReviewScope(scope, fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	guard, err := lease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(home, "pinned.db")
	if err := os.WriteFile(stage, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := guard.PinReplacement(selectedStore.ID, stage); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stage, selectedStore.Path); err != nil {
		t.Fatal(err)
	}
	if err := guard.ReconcileReplacement(selectedStore.ID); err != nil {
		t.Fatalf("ReconcileReplacement() = %v", err)
	}
	if err := guard.Check(); err != nil {
		t.Fatalf("Check() after exact pinned cutover = %v", err)
	}
	if stores, err := guard.Stores(); err != nil || len(stores) != len(scope.Bindings()) {
		t.Fatalf("Stores() after exact pinned cutover = %d, %v", len(stores), err)
	}
	if err := guard.Release(); err != nil {
		t.Fatalf("Release() after exact pinned cutover = %v", err)
	}
	if err := lease.Check(); err != nil {
		t.Fatalf("lease after exact pinned cutover = %v", err)
	}
}

func TestReviewScopeMigrationUnknownReplacementDoesNotPoison(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.AcquireReviewScope(scope, fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	guard, err := lease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.ReconcileReplacement("global/missing"); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("ReconcileReplacement(unknown) = %v", err)
	}
	if err := guard.Check(); err != nil {
		t.Fatalf("unknown replacement poisoned migration guard: %v", err)
	}
	if err := guard.Release(); err != nil {
		t.Fatalf("Release() after unknown replacement = %v", err)
	}
}

func storeIDStrings(ids []database.StoreID) []string {
	result := make([]string, len(ids))
	for index := range ids {
		result[index] = ids[index].String()
	}
	return result
}

func TestReviewScopeAcquisitionRevalidationFailureBoundaries(t *testing.T) {
	for _, failAt := range []int{1, 4} {
		t.Run(fmt.Sprintf("revalidation %d", failAt), func(t *testing.T) {
			home := secureTestDir(t)
			scope, _ := newClaimsReviewScope(t, home, nil)
			fence, err := database.AcquireOnlineFence(home)
			if err != nil {
				t.Fatal(err)
			}
			defer fence.Close()

			acquired, closed := 0, 0
			claimOps := defaultClaimAcquireOps()
			claimOps.claimRoot = func() (string, error) {
				return filepath.Join(home, "claims"), nil
			}
			claimOps.acquire = func(string, string) (claimHandle, error) {
				acquired++
				return &coverageClaimHandle{closed: &closed}, nil
			}
			scopeOps := defaultReviewScopeValidationOps()
			original := scopeOps.revalidate
			calls := 0
			scopeOps.revalidate = func(candidate *storecatalog.ReviewScope) (*storecatalog.ReviewScope, error) {
				calls++
				if calls == failAt {
					return nil, errors.New("review revalidation canary")
				}
				return original(candidate)
			}
			lease, err := acquireReviewScopeWithOps(scope, fence, claimOps, scopeOps)
			if lease != nil || database.CodeOf(err) != database.CodeIntegrity || calls != failAt {
				t.Fatalf("revalidation %d acquisition = %#v, %v; calls=%d", failAt, lease, err, calls)
			}
			if failAt == 1 && (acquired != 0 || closed != 0) {
				t.Fatalf("early revalidation acquired=%d closed=%d", acquired, closed)
			}
			if failAt == 4 && (acquired == 0 || closed != acquired) {
				t.Fatalf("final revalidation acquired=%d closed=%d", acquired, closed)
			}
		})
	}
}

func TestReviewScopeGuardReleaseRejectsLostClaim(t *testing.T) {
	lease := acquireReviewBoundaryLeaseOnly(t, false)
	release, err := lease.Guard()
	if err != nil {
		t.Fatal(err)
	}
	if len(lease.claims) == 0 {
		release()
		t.Fatal("review lease retained no claims")
	}
	original := lease.claims[0]
	lease.claims[0] = &coverageInvalidClaimHandle{}
	release()
	lease.claims[0] = original
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("Guard release retained authority after claim loss: %v", err)
	}
}

func TestReviewScopeOperationRevalidationFailureBoundaries(t *testing.T) {
	t.Run("refreshing guard entry", func(t *testing.T) {
		lease := acquireReviewBoundaryLease(t, false).lease
		lease.reviewScope.ops.revalidate = func(*storecatalog.ReviewScope) (*storecatalog.ReviewScope, error) {
			return nil, errors.New("refreshing scope canary")
		}
		stores, reconcile, release, err := lease.GuardStoresRefreshing()
		if stores != nil || reconcile != nil || release != nil || database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf(
				"GuardStoresRefreshing scope fault = %#v, %t, %t, %v",
				stores,
				reconcile != nil,
				release != nil,
				err,
			)
		}
	})

	t.Run("replacement pin entry", func(t *testing.T) {
		boundary := acquireReviewBoundaryLease(t, true)
		scope, lease, home := boundary.scope, boundary.lease, boundary.home
		lease.reviewScope.ops.revalidate = func(*storecatalog.ReviewScope) (*storecatalog.ReviewScope, error) {
			return nil, errors.New("pin scope canary")
		}
		if err := lease.PinReplacement(
			scope.Bindings()[0].ID,
			filepath.Join(home, "stage.db"),
		); database.CodeOf(
			err,
		) != database.CodeIntegrity {
			t.Fatalf("PinReplacement scope fault = %v", err)
		}
	})

	t.Run("refresh final validation", func(t *testing.T) {
		lease := acquireReviewBoundaryLease(t, false).lease
		original := lease.reviewScope.ops.revalidate
		calls := 0
		lease.reviewScope.ops.revalidate = func(candidate *storecatalog.ReviewScope) (*storecatalog.ReviewScope, error) {
			calls++
			if calls == 2 {
				return nil, errors.New("final refresh scope canary")
			}
			return original(candidate)
		}
		if err := lease.Refresh(); database.CodeOf(err) != database.CodeIntegrity || calls != 2 {
			t.Fatalf("Refresh final scope fault = %v; calls=%d", err, calls)
		}
	})
}

func TestReviewScopeMigrationRevalidationFailureBoundary(t *testing.T) {
	lease := acquireReviewBoundaryLeaseOnly(t, true)
	guard, err := lease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	original := lease.reviewScope.ops.revalidate
	calls := 0
	lease.reviewScope.ops.revalidate = func(candidate *storecatalog.ReviewScope) (*storecatalog.ReviewScope, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("migration scope canary")
		}
		return original(candidate)
	}
	if err := guard.Reconcile(); database.CodeOf(err) != database.CodeIntegrity || calls != 2 {
		_ = guard.Release()
		t.Fatalf("migration scope fault = %v; calls=%d", err, calls)
	}
	lease.reviewScope.ops.revalidate = original
	if err := guard.Release(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("poisoned migration guard Release() = %v", err)
	}
}

func TestReviewScopePinRejectsAuthorityLostBeforePublication(t *testing.T) {
	boundary := acquireReviewBoundaryLease(t, true)
	scope, lease, home := boundary.scope, boundary.lease, boundary.home
	stage := filepath.Join(home, "authority-loss-stage.db")
	relative, err := filepath.Rel(home, stage)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		t.Fatalf("replacement stage escaped test-owned home: %q, %v", stage, err)
	}
	if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := scope.Bindings()[0].ID
	index, found := lease.byID[target]
	if !found {
		t.Fatalf("review target %q is not claimed", target)
	}
	ops := defaultClaimRefreshOps()
	ops.acquire = lease.acquireClaim
	authorityCalls := 0
	ops.guardedAuthority = func() bool {
		authorityCalls++
		return authorityCalls < 5
	}
	lease.mu.Lock()
	err = lease.pinReplacementLocked(target, stage, index, ops)
	lease.mu.Unlock()
	if database.CodeOf(err) != database.CodeIntegrity ||
		!strings.Contains(err.Error(), "before publication") || authorityCalls != 5 {
		t.Fatalf("final pin authority = %v; calls=%d", err, authorityCalls)
	}
	if lease.replacementPins[target] != nil {
		t.Fatal("authority-lost replacement pin was published")
	}
}

type reviewBoundaryLease struct {
	scope *storecatalog.ReviewScope
	lease *Lease
	fence *database.Fence
	home  string
}

func acquireReviewBoundaryLease(t *testing.T, migration bool) reviewBoundaryLease {
	t.Helper()
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	var (
		fence *database.Fence
		err   error
	)
	if migration {
		fence, err = database.AcquireMigrationFence(home)
	} else {
		fence, err = database.AcquireOnlineFence(home)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fence.Close() })
	lease, err := newTestClaimAcquirer(t).AcquireReviewScope(scope, fence)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	return reviewBoundaryLease{scope: scope, lease: lease, fence: fence, home: home}
}
