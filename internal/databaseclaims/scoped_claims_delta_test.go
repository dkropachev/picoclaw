package databaseclaims

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestReviewScopeValidationHelpersRejectMalformedDetachedState(t *testing.T) {
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	full, selected, err := storecatalog.ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name     string
		catalogs func(*storecatalog.ReviewScope) (*storecatalog.Catalog, *storecatalog.Catalog, error)
		wantCode database.ErrorCode
	}{
		{
			name: "catalog bridge error",
			catalogs: func(*storecatalog.ReviewScope) (*storecatalog.Catalog, *storecatalog.Catalog, error) {
				return nil, nil, errors.New("catalog bridge canary")
			},
			wantCode: database.CodeInvalid,
		},
		{
			name: "missing selected catalog",
			catalogs: func(*storecatalog.ReviewScope) (*storecatalog.Catalog, *storecatalog.Catalog, error) {
				return full, nil, nil
			},
			wantCode: database.CodeInvalid,
		},
		{
			name: "bindings do not match selected catalog",
			catalogs: func(*storecatalog.ReviewScope) (*storecatalog.Catalog, *storecatalog.Catalog, error) {
				return full, full, nil
			},
			wantCode: database.CodeInvalid,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultReviewScopeValidationOps()
			ops.catalogs = test.catalogs
			state, candidate, stateErr := newReviewScopeLeaseState(scope, ops)
			if state != nil || candidate != nil || database.CodeOf(stateErr) != test.wantCode {
				t.Fatalf("malformed lease state = %#v, %#v, %v", state, candidate, stateErr)
			}
		})
	}

	if reviewBindingsMatchSelected(scope.Bindings(), nil) {
		t.Fatal("nil selected catalog matched review bindings")
	}
	bindings := scope.Bindings()
	if reviewBindingsMatchSelected(bindings[:len(bindings)-1], selected) {
		t.Fatal("short binding set matched selected catalog")
	}
	bindings[0].Domain = "forged"
	if reviewBindingsMatchSelected(bindings, selected) {
		t.Fatal("changed binding matched selected catalog")
	}
	var absent *reviewScopeLeaseState
	if _, observeErr := absent.observeGeneration(selected); database.CodeOf(observeErr) != database.CodeIntegrity {
		t.Fatalf("nil review state observation = %v", observeErr)
	}

	state, candidate, err := newReviewScopeLeaseState(scope, defaultReviewScopeValidationOps())
	if err != nil {
		t.Fatal(err)
	}
	state.ops.revalidate = func(*storecatalog.ReviewScope) (*storecatalog.ReviewScope, error) {
		return scope, nil
	}
	state.ops.catalogs = func(*storecatalog.ReviewScope) (*storecatalog.Catalog, *storecatalog.Catalog, error) {
		return full, full, nil
	}
	if _, err := state.observeGeneration(candidate); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("changed selected catalog observation = %v", err)
	}
}

func TestLegacyLeaseScopedMetadataRemainsEmptyAndStoreIDsAreSorted(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	options := testOptions(t, home, &config.Config{})
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.Acquire(options, fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()

	if lease.ScopeFingerprint() != "" || lease.FullCatalogFingerprint() != "" {
		t.Fatal("complete-catalog lease exposed review-scope fingerprints")
	}
	ids := lease.StoreIDs()
	if len(ids) != len(lease.Stores()) || !slices.IsSorted(ids) {
		t.Fatalf("complete-catalog StoreIDs = %#v", ids)
	}
	ids[0] = "forged/id"
	if lease.StoreIDs()[0] == "forged/id" {
		t.Fatal("complete-catalog StoreIDs retained caller mutation")
	}
}

func TestReviewScopeCurrentUnselectedClaimIdentityIsDeniedToStages(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	scope, _ := newClaimsReviewScope(t, home, nil)
	full, _, err := storecatalog.ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	unselected, found := full.Lookup("global/auth")
	if !found {
		t.Fatal("unselected auth store is absent")
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(unselected.Path), 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if writeErr := os.WriteFile(unselected.Path, []byte("unselected"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	observed, err := observeStores([]storecatalog.Spec{unselected})
	if err != nil {
		t.Fatal(err)
	}
	main, found := findObservation(observed, unselected.ID, memberMain)
	if !found {
		t.Fatal("unselected main observation is absent")
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
	if !lease.reviewScopeCatalogContains(main.ClaimIdentity, "different-identity") ||
		!lease.reviewScopeCatalogContains("different-claim", main.Identity.String()) ||
		lease.reviewScopeCatalogContains("different-claim", "different-identity") {
		t.Fatal("current unselected deny-set membership is inconsistent")
	}
}

func TestReviewScopeValidationFailuresDoNotRequireHardLinks(t *testing.T) {
	t.Run("exposure", func(t *testing.T) {
		lease := acquireReviewBoundaryLeaseOnly(t, false)
		lease.reviewScope.ops.revalidate = failingReviewScopeRevalidation
		if home := lease.Home(); home != "" {
			t.Fatalf("scope-invalid lease exposed home %q", home)
		}
	})

	t.Run("migration guard entry", func(t *testing.T) {
		lease := acquireReviewBoundaryLeaseOnly(t, true)
		lease.reviewScope.ops.revalidate = failingReviewScopeRevalidation
		if guard, err := lease.GuardStoresMigrating(); guard != nil ||
			database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("scope-invalid migration guard = %#v, %v", guard, err)
		}
	})

	t.Run("refresh entry", func(t *testing.T) {
		lease := acquireReviewBoundaryLeaseOnly(t, false)
		lease.reviewScope.ops.revalidate = failingReviewScopeRevalidation
		if err := lease.Refresh(); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("scope-invalid refresh = %v", err)
		}
	})
}

func TestReviewScopeRejectsSyntheticSelectedHistoryReuseWithoutHardLinks(t *testing.T) {
	for _, history := range []string{"catalog", "pin"} {
		t.Run(history, func(t *testing.T) {
			lease := acquireReviewBoundaryLeaseOnly(t, false)
			path := filepath.Join(lease.home, "synthetic-unselected.db")
			if err := os.WriteFile(path, []byte("synthetic"), 0o600); err != nil {
				t.Fatal(err)
			}
			identity, exists, err := regularPhysicalIdentity(path)
			if err != nil || !exists {
				t.Fatalf("synthetic physical identity = %#v, %t, %v", identity, exists, err)
			}
			previousAssignment := "workspace/repository-reviews\x00main\x00selected"
			if history == "catalog" {
				lease.assignments[identity.String()] = previousAssignment
			} else {
				lease.pinAssignments[identity.String()] = previousAssignment
			}
			observe := lease.reviewScope.ops.observe
			lease.reviewScope.ops.observe = func(
				catalog *storecatalog.Catalog,
			) ([]memberObservation, error) {
				observed, observeErr := observe(catalog)
				if observeErr != nil {
					return nil, observeErr
				}
				return append(observed, memberObservation{
					StoreID: "global/auth", Role: memberMain, Path: path,
					Type: memberRegular, Identity: identity,
					ClaimIdentity: digestPhysicalIdentity(identity),
				}), nil
			}
			if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("synthetic %s history reuse = %v", history, err)
			}
		})
	}
}

func failingReviewScopeRevalidation(
	*storecatalog.ReviewScope,
) (*storecatalog.ReviewScope, error) {
	return nil, errors.New("review scope revalidation canary")
}

func acquireReviewBoundaryLeaseOnly(t *testing.T, migration bool) *Lease {
	t.Helper()
	return acquireReviewBoundaryLease(t, migration).lease
}
