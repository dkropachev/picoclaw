//nolint:govet // Independent failure-boundary assertions use narrow error scopes.
package databaseclaims

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

// testClaimAcquirer keeps real interprocess locks scoped to one test. It never
// consults or mutates the production per-user claim root.
type testClaimAcquirer struct {
	root string
}

func newTestClaimAcquirer(t *testing.T) testClaimAcquirer {
	t.Helper()
	return testClaimAcquirer{root: isolatedTestClaimRoot(t)}
}

func (acquirer testClaimAcquirer) Acquire(
	options storecatalog.Options,
	fence *database.Fence,
) (*Lease, error) {
	return AcquireForTesting(options, nil, fence, acquirer.root)
}

func (acquirer testClaimAcquirer) AcquireProjected(
	projected *storecatalog.Catalog,
	fence *database.Fence,
) (*Lease, error) {
	if projected == nil {
		return nil, database.NewError(database.CodeInvalid, "physical database catalog is unavailable")
	}
	return AcquireForTesting(storecatalog.Options{}, projected, fence, acquirer.root)
}

func TestAcquireRequiresExistingStorageFence(t *testing.T) {
	before := stableClaimRootSnapshot(t)
	home := secureTestDir(t)
	lease, err := Acquire(testOptions(t, home, &config.Config{}), nil)
	if lease != nil || database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("Acquire() without fence = %#v, %v", lease, err)
	}
	if after := stableClaimRootSnapshot(t); after != before {
		t.Fatalf("unauthorized Acquire changed stable claim root from %#v to %#v", before, after)
	}
}

func TestAcquireProjectedRevalidatesDetachedCatalog(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	stableLocks := stableClaimRootSnapshot(t)
	home := secureTestDir(t)
	options := testOptions(t, home, &config.Config{})
	projected, err := storecatalog.Project(options)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.AcquireProjected(projected, fence)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Home() != projected.Home() || len(lease.Stores()) != len(projected.All()) {
		t.Fatalf("projected lease = %q, %d", lease.Home(), len(lease.Stores()))
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	auth, ok := projected.Lookup("global/auth")
	if !ok {
		t.Fatal("projected auth store is absent")
	}
	if err := os.Mkdir(auth.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	if lease, err := testClaims.AcquireProjected(projected, fence); err == nil || lease != nil {
		t.Fatalf("drifted projected Acquire = %#v, %v", lease, err)
	}
	wrongFence, err := database.AcquireOnlineFence(secureTestDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer wrongFence.Close()
	if lease, err := AcquireProjected(projected, wrongFence); lease != nil ||
		database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("wrong-fence projected Acquire = %#v, %v", lease, err)
	}
	if after := stableClaimRootSnapshot(t); after != stableLocks {
		t.Fatalf("unauthorized AcquireProjected changed stable claim root from %#v to %#v", stableLocks, after)
	}
}

func TestAcquireRequiresFenceForExactHomeAndLeaseExpiresWithFence(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	firstHome, secondHome := secureTestDir(t), secureTestDir(t)
	fence, err := database.AcquireOnlineFence(firstHome)
	if err != nil {
		t.Fatal(err)
	}
	if lease, acquireErr := testClaims.Acquire(testOptions(t, secondHome, &config.Config{}), fence); lease != nil ||
		database.CodeOf(acquireErr) != database.CodeUnauthorized {
		t.Fatalf("cross-home Acquire() = %#v, %v", lease, acquireErr)
	}
	lease, err := testClaims.Acquire(testOptions(t, firstHome, &config.Config{}), fence)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Home() == "" || len(lease.Stores()) == 0 {
		t.Fatal("live lease exposed no catalog")
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	if lease.Home() != "" || lease.Stores() != nil {
		t.Fatal("lease remained usable after its storage fence closed")
	}
	if _, found := lease.Lookup("global/auth"); found {
		t.Fatal("closed-fence lease lookup succeeded")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseIsCompleteImmutableAndReacquirable(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	options := testOptions(t, home, &config.Config{})
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()

	projected, err := storecatalog.Project(options)
	if err != nil {
		t.Fatal(err)
	}
	wantClaims, err := projected.ClaimIDs()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := testClaims.Acquire(options, fence)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Home() != projected.Home() {
		t.Fatalf("Home() = %q, want %q", lease.Home(), projected.Home())
	}
	stores := lease.Stores()
	if len(stores) != len(projected.All()) || !sort.SliceIsSorted(stores, func(left, right int) bool {
		return stores[left].ID < stores[right].ID
	}) {
		t.Fatalf("Stores() = %#v", stores)
	}
	first := stores[0]
	lookup, ok := lease.Lookup(first.ID)
	if !ok || lookup.ID != first.ID {
		t.Fatalf("Lookup(%q) = %#v, %t", first.ID, lookup, ok)
	}
	if len(lookup.LegacyRoots) != 0 {
		lookup.LegacyRoots[0] = "changed"
		fresh, _ := lease.Lookup(first.ID)
		if fresh.LegacyRoots[0] == "changed" {
			t.Fatal("Lookup exposed lease storage")
		}
	}
	stores[0].Path = "changed"
	if fresh := lease.Stores(); fresh[0].Path == "changed" {
		t.Fatal("Stores exposed lease storage")
	}
	if missing, found := lease.Lookup(database.StoreID("missing")); found || !missing.ID.IsZero() {
		t.Fatalf("Lookup(missing) = %#v, %t", missing, found)
	}

	root := testClaims.root
	for _, identity := range wantClaims {
		if !identity.Valid() || len(identity.String()) != 64 {
			t.Fatalf("invalid opaque claim identity %q", identity)
		}
		info, statErr := os.Lstat(filepath.Join(root, identity.String()+".lock"))
		if statErr != nil || !info.Mode().IsRegular() {
			t.Fatalf("claim %q missing or unsafe: %v", identity, statErr)
		}
	}

	if competing, acquireErr := testClaims.Acquire(options, fence); competing != nil ||
		database.CodeOf(acquireErr) != database.CodeConflict {
		t.Fatalf("competing Acquire() = %#v, %v", competing, acquireErr)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	reacquired, err := testClaims.Acquire(options, fence)
	if err != nil {
		t.Fatalf("Acquire() after release = %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStableClaimCacheRootIgnoresMutableCacheEnvironment(t *testing.T) {
	first, err := stableClaimCacheRoot()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", secureTestDir(t))
	t.Setenv("LocalAppData", secureTestDir(t))
	second, err := stableClaimCacheRoot()
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("claim root changed with cache environment: %q, want %q", second, first)
	}
}

func TestLeaseRefreshClaimsMaterializedMainAndRejectsReplacement(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	firstHome, secondHome := secureTestDir(t), secureTestDir(t)
	firstFence, err := database.AcquireOnlineFence(firstHome)
	if err != nil {
		t.Fatal(err)
	}
	defer firstFence.Close()
	first, err := testClaims.Acquire(testOptions(t, firstHome, &config.Config{}), firstFence)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	path := filepath.Join(firstHome, "auth.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeClaims := len(first.claims)
	if err := first.Refresh(); err != nil {
		t.Fatal(err)
	}
	if len(first.claims) <= beforeClaims {
		t.Fatal("materialized main acquired no physical claim")
	}
	secondPath := filepath.Join(secondHome, "auth.db")
	if err := os.Link(path, secondPath); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	secondFence, err := database.AcquireOnlineFence(secondHome)
	if err != nil {
		t.Fatal(err)
	}
	defer secondFence.Close()
	if second, err := testClaims.Acquire(testOptions(t, secondHome, &config.Config{}), secondFence); second != nil ||
		database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("materialized alias Acquire = %#v, %v", second, err)
	}
	if err := os.Remove(secondPath); err != nil {
		t.Fatal(err)
	}
	old := path + ".old"
	if err := os.Rename(path, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := first.Refresh(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("replacement Refresh = %v", err)
	}
	if first.Home() != "" || first.Stores() != nil || first.Check() == nil {
		t.Fatal("replaced main did not poison lease")
	}
}

func TestLeaseRefreshReplacementPromotesControlledMain(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.Acquire(testOptions(t, home, &config.Config{}), fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	path := filepath.Join(home, "auth.db")
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lease.Refresh(); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(home, "stage.db")
	if err := os.WriteFile(stage, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lease.PinReplacement("global/auth", stage); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stage, path); err != nil {
		t.Fatal(err)
	}
	if err := lease.RefreshReplacement("global/auth"); err != nil {
		t.Fatal(err)
	}
	if err := lease.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestLeasePinReplacementRetainsExactPhysicalIdentity(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.Acquire(testOptions(t, home, &config.Config{}), fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	stage := filepath.Join(home, "stage.db")
	if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := len(lease.claims)
	if err := lease.PinReplacement("global/auth", stage); err != nil {
		t.Fatal(err)
	}
	if len(lease.claims) <= before {
		t.Fatal("Pin retained no physical claim")
	}
	if err := lease.PinReplacement("global/auth", stage); err != nil || len(lease.claims) != before+1 {
		t.Fatalf("repeat PinReplacement claims=%d err=%v", len(lease.claims), err)
	}
	if err := os.Rename(stage, stage+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stage, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lease.PinReplacement("global/auth", stage); err != nil {
		t.Fatal(err)
	}
	if len(lease.claims) != before+2 {
		t.Fatalf("replacement Pin claims = %d, want %d", len(lease.claims), before+2)
	}
}

func TestLeaseReplacementCapabilityRejectsOnlineFence(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.Acquire(testOptions(t, home, &config.Config{}), fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	stage := filepath.Join(home, "stage.db")
	if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lease.PinReplacement("global/auth", stage); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("online PinReplacement = %v", err)
	}
	if err := lease.RefreshReplacement("global/auth"); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("online RefreshReplacement = %v", err)
	}
}

func TestLeasePinReplacementRejectsCatalogPhysicalAlias(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	other := filepath.Join(home, "launcher-auth.db")
	if err := os.WriteFile(other, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.Acquire(testOptions(t, home, &config.Config{}), fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	stage := filepath.Join(home, "stage.db")
	if err := os.Link(other, stage); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if err := lease.PinReplacement("global/auth", stage); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("catalog-alias PinReplacement = %v", err)
	}
}

func TestLeaseGuardDelaysCloseUntilOwnershipTransferCompletes(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.Acquire(testOptions(t, home, &config.Config{}), fence)
	if err != nil {
		t.Fatal(err)
	}
	release, err := lease.Guard()
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- lease.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("lease closed through live guard: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	release()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("lease close did not resume after guard release")
	}
}

func TestClaimsFencePhysicalOverlapAcrossHomes(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	sharedWorkspace := filepath.Join(secureTestDir(t), "shared-workspace")
	firstHome, secondHome := secureTestDir(t), secureTestDir(t)
	firstFence, err := database.AcquireOnlineFence(firstHome)
	if err != nil {
		t.Fatal(err)
	}
	defer firstFence.Close()
	secondFence, err := database.AcquireOnlineFence(secondHome)
	if err != nil {
		t.Fatal(err)
	}
	defer secondFence.Close()
	configuration := func() *config.Config {
		return &config.Config{Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{Workspace: sharedWorkspace},
		}}
	}
	first, err := testClaims.Acquire(testOptions(t, firstHome, configuration()), firstFence)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, acquireErr := testClaims.Acquire(
		testOptions(t, secondHome, configuration()), secondFence,
	); second != nil ||
		database.CodeOf(acquireErr) != database.CodeConflict {
		t.Fatalf("overlapping Acquire() = %#v, %v", second, acquireErr)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := testClaims.Acquire(testOptions(t, secondHome, configuration()), secondFence)
	if err != nil {
		t.Fatalf("Acquire() after overlap release = %v", err)
	}
	_ = second.Close()
}

func TestClaimsFenceHardlinkedGenerationMembersAcrossHomes(t *testing.T) {
	for _, member := range []struct {
		name     string
		relative string
	}{
		{name: "main", relative: "auth.db"},
		{name: "wal", relative: "auth.db-wal"},
		{name: "shm", relative: "auth.db-shm"},
		{name: "journal", relative: "auth.db-journal"},
		{name: "legacy", relative: "auth.json"},
	} {
		t.Run(member.name, func(t *testing.T) {
			testClaims := newTestClaimAcquirer(t)
			firstHome, secondHome := secureTestDir(t), secureTestDir(t)
			firstPath := filepath.Join(firstHome, member.relative)
			secondPath := filepath.Join(secondHome, member.relative)
			if err := os.WriteFile(firstPath, []byte("physical-claim-test"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(firstPath, secondPath); err != nil {
				t.Skipf("hardlinks unavailable: %v", err)
			}

			firstFence, err := database.AcquireOnlineFence(firstHome)
			if err != nil {
				t.Fatal(err)
			}
			defer firstFence.Close()
			secondFence, err := database.AcquireOnlineFence(secondHome)
			if err != nil {
				t.Fatal(err)
			}
			defer secondFence.Close()

			first, err := testClaims.Acquire(testOptions(t, firstHome, &config.Config{}), firstFence)
			if err != nil {
				t.Fatal(err)
			}
			if second, acquireErr := testClaims.Acquire(
				testOptions(t, secondHome, &config.Config{}), secondFence,
			); second != nil ||
				database.CodeOf(acquireErr) != database.CodeConflict {
				t.Fatalf("hard-linked member Acquire() = %#v, %v", second, acquireErr)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			second, err := testClaims.Acquire(testOptions(t, secondHome, &config.Config{}), secondFence)
			if err != nil {
				t.Fatalf("Acquire() after physical release = %v", err)
			}
			if err := second.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClaimIdentityInputIsBoundedAndValidated(t *testing.T) {
	if identities, err := checkedLexicalClaimIdentities(nil); identities != nil ||
		database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("checkedLexicalClaimIdentities(nil) = %#v, %v", identities, err)
	}
	oversized := make([]storecatalog.ClaimID, maxCatalogClaimPaths+1)
	if identities, err := checkedLexicalClaimIdentities(oversized); identities != nil ||
		database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("checkedLexicalClaimIdentities(oversized) = %#v, %v", identities, err)
	}
	if identities, err := checkedLexicalClaimIdentities([]storecatalog.ClaimID{"invalid"}); identities != nil ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("checkedLexicalClaimIdentities(invalid) = %#v, %v", identities, err)
	}
	if identities, err := catalogPhysicalClaimIdentities(nil); identities != nil ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("catalogPhysicalClaimIdentities(nil) = %#v, %v", identities, err)
	}
	if identities, err := catalogPhysicalClaimIdentities(&storecatalog.Catalog{}); identities != nil ||
		database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("catalogPhysicalClaimIdentities(empty) = %#v, %v", identities, err)
	}
	for _, identity := range []string{
		strings.Repeat("a", 63) + "G",
		strings.Repeat("a", 63) + "/",
	} {
		if validClaimIdentity(identity) {
			t.Fatalf("validClaimIdentity(%q) = true", identity)
		}
	}
}

func TestPhysicalClaimIdentityAndLockNameAreOpaqueAndSafe(t *testing.T) {
	root := secureTestDir(t)
	first := filepath.Join(root, "first")
	alias := filepath.Join(root, "alias")
	if err := os.WriteFile(first, []byte("identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(first, alias); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	firstKey, exists, err := physicalClaimKey(first)
	if err != nil || !exists || firstKey == "" {
		t.Fatalf("physicalClaimKey(first) = %q, %t, %v", firstKey, exists, err)
	}
	aliasKey, exists, err := physicalClaimKey(alias)
	if err != nil || !exists || aliasKey != firstKey {
		t.Fatalf("physicalClaimKey(alias) = %q, %t, %v; want %q", aliasKey, exists, err, firstKey)
	}
	if key, found, keyErr := physicalClaimKey(filepath.Join(root, "missing")); key != "" || found || keyErr != nil {
		t.Fatalf("physicalClaimKey(missing) = %q, %t, %v", key, found, keyErr)
	}

	if claim, claimErr := acquireClaim("unused-invalid-root", "../not-an-identity"); claim != nil ||
		database.CodeOf(claimErr) != database.CodeIntegrity {
		t.Fatalf("acquireClaim(invalid) = %#v, %v", claim, claimErr)
	}
}

func TestConcurrentAcquireHasOneWinnerAndReleasesPartialClaims(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	options := testOptions(t, home, &config.Config{})

	const contenders = 12
	start := make(chan struct{})
	results := make(chan struct {
		lease *Lease
		err   error
	}, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			lease, acquireErr := testClaims.Acquire(options, fence)
			results <- struct {
				lease *Lease
				err   error
			}{lease: lease, err: acquireErr}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	winners := 0
	var winner *Lease
	for result := range results {
		if result.err == nil {
			winners++
			winner = result.lease
			continue
		}
		if result.lease != nil || database.CodeOf(result.err) != database.CodeConflict {
			t.Fatalf("contender result = %#v, %v", result.lease, result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("successful contenders = %d, want 1", winners)
	}
	if err := winner.Close(); err != nil {
		t.Fatal(err)
	}
	lease, err := testClaims.Acquire(options, fence)
	if err != nil {
		t.Fatalf("Acquire() after concurrent release = %v", err)
	}
	_ = lease.Close()
}

func TestAcquireAcceptsMigrationFenceAndRejectsInvalidCatalog(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.Acquire(testOptions(t, home, &config.Config{}), fence)
	if err != nil {
		t.Fatal(err)
	}
	_ = lease.Close()
	invalidOptions := storecatalog.Options{Home: home}
	if invalid, acquireErr := testClaims.Acquire(invalidOptions, fence); invalid != nil || acquireErr == nil {
		t.Fatalf("Acquire(invalid catalog) = %#v, %v", invalid, acquireErr)
	}
	var nilLease *Lease
	if nilLease.Home() != "" || nilLease.Stores() != nil {
		t.Fatal("nil lease accessors returned data")
	}
	if err := nilLease.Close(); err != nil {
		t.Fatalf("nil Close() = %v", err)
	}
}

func TestCatalogEqualityRejectsDifferentCatalogs(t *testing.T) {
	home := secureTestDir(t)
	userHome := secureTestDir(t)
	options := storecatalog.Options{Home: home, Config: &config.Config{}, UserHome: userHome}
	first, err := storecatalog.Project(options)
	if err != nil {
		t.Fatal(err)
	}
	same, err := storecatalog.Project(options)
	if err != nil {
		t.Fatal(err)
	}
	if !catalogsEqual(first, same) {
		t.Fatal("equal projections compared unequal")
	}
	if catalogsEqual(nil, same) {
		t.Fatal("nil catalog compared equal")
	}
	differentHome, err := storecatalog.Project(storecatalog.Options{
		Home: secureTestDir(t), Config: &config.Config{}, UserHome: userHome,
	})
	if err != nil {
		t.Fatal(err)
	}
	if catalogsEqual(first, differentHome) {
		t.Fatal("different homes compared equal")
	}
	differentPaths, err := storecatalog.Project(storecatalog.Options{
		Home: home,
		Config: &config.Config{Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{Workspace: filepath.Join(home, "alternate-workspace")},
		}},
		UserHome: userHome,
	})
	if err != nil {
		t.Fatal(err)
	}
	if catalogsEqual(first, differentPaths) {
		t.Fatal("different physical paths compared equal")
	}
	extraWorkspace, err := storecatalog.Project(storecatalog.Options{
		Home: home,
		Config: &config.Config{Agents: config.AgentsConfig{
			List: []config.AgentConfig{{ID: "other", Workspace: filepath.Join(home, "other-workspace")}},
		}},
		UserHome: userHome,
	})
	if err != nil {
		t.Fatal(err)
	}
	if catalogsEqual(first, extraWorkspace) {
		t.Fatal("different catalog lengths compared equal")
	}
}

func testOptions(t *testing.T, home string, cfg *config.Config) storecatalog.Options {
	t.Helper()
	return storecatalog.Options{Home: home, Config: cfg, UserHome: secureTestDir(t)}
}

func secureTestDir(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
