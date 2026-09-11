package databaseclaims

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

type orderedCloseHandle struct {
	name  string
	order *[]string
	err   error
}

func (handle *orderedCloseHandle) close() error {
	*handle.order = append(*handle.order, handle.name)
	return handle.err
}

type orderedReplacementHandle struct {
	orderedCloseHandle
}

func (*orderedReplacementHandle) valid() bool                        { return true }
func (*orderedReplacementHandle) matches(fileidentity.Identity) bool { return true }

func TestLeaseAPIsFailClosedWithoutAuthority(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	var absent *Lease
	if _, found := absent.Lookup("global/auth"); found {
		t.Fatal("nil lease lookup succeeded")
	}
	if database.CodeOf(absent.Check()) != database.CodeUnavailable {
		t.Fatalf("nil Check() = %v", absent.Check())
	}
	if release, err := absent.Guard(); release != nil || database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil Guard() release=%t, err=%v", release != nil, err)
	}
	if stores, release, err := absent.GuardStores(); stores != nil || release != nil ||
		database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil GuardStores() = %#v, release=%t, err=%v", stores, release != nil, err)
	}
	if database.CodeOf(absent.Refresh()) != database.CodeUnavailable {
		t.Fatalf("nil Refresh() = %v", absent.Refresh())
	}
	if database.CodeOf(absent.PinReplacement("global/auth", "stage.db")) != database.CodeInvalid {
		t.Fatalf("nil PinReplacement() = %v", absent.PinReplacement("global/auth", "stage.db"))
	}
	if database.CodeOf(absent.RefreshReplacement("")) != database.CodeInvalid {
		t.Fatalf("invalid RefreshReplacement() = %v", absent.RefreshReplacement(""))
	}

	home := secureTestDir(t)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := testClaims.Acquire(testOptions(t, home, &config.Config{}), fence)
	if err != nil {
		t.Fatal(err)
	}
	stores, release, err := lease.GuardStores()
	if err != nil || len(stores) == 0 || release == nil {
		t.Fatalf("GuardStores() = %#v, release=%t, err=%v", stores, release != nil, err)
	}
	wantPath := lease.stores[0].Path
	stores[0].Path = "mutated"
	if lease.stores[0].Path != wantPath {
		t.Fatal("GuardStores exposed lease storage")
	}
	release()
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if release, err := lease.Guard(); release != nil || database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("closed Guard() release=%t, err=%v", release != nil, err)
	}
	if stores, release, err := lease.GuardStores(); stores != nil || release != nil ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("closed GuardStores() = %#v, release=%t, err=%v", stores, release != nil, err)
	}
	if database.CodeOf(lease.Refresh()) != database.CodeIntegrity {
		t.Fatalf("closed Refresh() = %v", lease.Refresh())
	}
	if database.CodeOf(lease.PinReplacement("global/auth", "stage.db")) != database.CodeIntegrity {
		t.Fatalf("closed PinReplacement() = %v", lease.PinReplacement("global/auth", "stage.db"))
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseCloseUsesReverseResourceOrderAndJoinsErrors(t *testing.T) {
	var order []string
	firstErr, secondErr := errors.New("first close"), errors.New("second close")
	claimA := &orderedCloseHandle{name: "claim-a", order: &order}
	pinB := &replacementPin{handle: &orderedReplacementHandle{
		orderedCloseHandle{name: "pin-b", order: &order, err: firstErr},
	}}
	claimC := &orderedCloseHandle{name: "claim-c", order: &order, err: secondErr}
	pinD := &replacementPin{handle: &orderedReplacementHandle{
		orderedCloseHandle{name: "pin-d", order: &order},
	}}
	lease := &Lease{
		resources:       []claimHandle{claimA, pinB, claimC, pinD},
		replacementPins: map[database.StoreID]*replacementPin{"global/auth": pinB, "launcher/auth": pinD},
	}
	err := lease.Close()
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("Close error = %v", err)
	}
	want := []string{"pin-d", "claim-c", "pin-b", "claim-a"}
	if !slices.Equal(order, want) {
		t.Fatalf("Close order = %#v, want %#v", order, want)
	}
	if err := lease.Close(); err != nil || !slices.Equal(order, want) {
		t.Fatalf("repeated Close = %v, order %#v", err, order)
	}
}

func TestReplacementPinCleanupErrorsPoisonWithoutResourceLeaks(t *testing.T) {
	t.Run("repin", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		first := writeReplacementStage(t, lease.home, "close-error-first.db")
		second := writeReplacementStage(t, lease.home, "close-error-second.db")
		if err := lease.PinReplacement("global/auth", first); err != nil {
			t.Fatal(err)
		}
		pin := lease.replacementPins["global/auth"]
		if err := pin.handle.close(); err != nil {
			t.Fatal(err)
		}
		canary := errors.New("repin close canary")
		var order []string
		pin.handle = &orderedReplacementHandle{orderedCloseHandle{
			name: "old-pin", order: &order, err: canary,
		}}
		if err := lease.PinReplacement("global/auth", second); !errors.Is(err, canary) {
			t.Fatalf("repin close error = %v", err)
		}
		if _, exists := lease.replacementPins["global/auth"]; exists ||
			len(lease.resources) != len(lease.claims) {
			t.Fatalf(
				"failed repin retained map=%t resources=%d claims=%d",
				exists, len(lease.resources), len(lease.claims),
			)
		}
		if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("failed repin did not poison lease: %v", err)
		}
	})

	t.Run("replacement consumption", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		stage := writeReplacementStage(t, lease.home, "consume-close-error.db")
		if err := lease.PinReplacement("global/auth", stage); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(stage, filepath.Join(lease.home, "auth.db")); err != nil {
			t.Fatal(err)
		}
		pin := lease.replacementPins["global/auth"]
		if err := pin.handle.close(); err != nil {
			t.Fatal(err)
		}
		canary := errors.New("consume close canary")
		var order []string
		pin.handle = &orderedReplacementHandle{orderedCloseHandle{
			name: "consumed-pin", order: &order, err: canary,
		}}
		if err := lease.RefreshReplacement("global/auth"); !errors.Is(err, canary) {
			t.Fatalf("replacement consume close error = %v", err)
		}
		if _, exists := lease.replacementPins["global/auth"]; exists ||
			len(lease.resources) != len(lease.claims) {
			t.Fatalf(
				"failed consumption retained map=%t resources=%d claims=%d",
				exists, len(lease.resources), len(lease.claims),
			)
		}
		if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("failed consumption did not poison lease: %v", err)
		}
	})
}

func TestReplacementResourceFailureBoundaries(t *testing.T) {
	t.Run("retained resource limit", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		stage := writeReplacementStage(t, lease.home, "resource-limit.db")
		original := lease.resources
		lease.resources = make([]claimHandle, maxLeaseResources)
		err := lease.PinReplacement("global/auth", stage)
		lease.resources = original
		if database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("PinReplacement(resource limit) = %v", err)
		}
	})

	t.Run("missing claim handle", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		stage := writeReplacementStage(t, lease.home, "missing-claim-handle.db")
		lease.acquireClaim = func(string, string) (claimHandle, error) { return nil, nil }
		if err := lease.PinReplacement("global/auth", stage); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("PinReplacement(missing claim handle) = %v", err)
		}
	})

	var absent *replacementPin
	if err := absent.close(); err != nil {
		t.Fatalf("nil replacement pin close = %v", err)
	}
	if err := (&replacementPin{}).close(); err != nil {
		t.Fatalf("empty replacement pin close = %v", err)
	}
	var absentLease *Lease
	if err := absentLease.RefreshReplacement("global/auth"); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil RefreshReplacement = %v", err)
	}
}

func TestAcquireRejectsFinalPhysicalIdentityDrift(t *testing.T) {
	home := secureTestDir(t)
	options := testOptions(t, home, &config.Config{})
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	canary := errors.New("final physical identity canary")

	for _, test := range []struct {
		name  string
		final func([]memberObservation) ([]memberObservation, error)
	}{
		{
			name: "lookup failure",
			final: func([]memberObservation) ([]memberObservation, error) {
				return nil, canary
			},
		},
		{
			name: "identity change",
			final: func(previous []memberObservation) ([]memberObservation, error) {
				changed := cloneObservations(previous)
				if len(changed) == 0 {
					changed = append(changed, memberObservation{ClaimIdentity: strings.Repeat("a", 64)})
				} else {
					changed[0].ClaimIdentity = strings.Repeat("a", 64)
				}
				return changed, nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultClaimAcquireOps()
			ops.claimRoot = func() (string, error) { return "test-root", nil }
			ops.acquire = func(string, string) (claimHandle, error) { return &coverageClaimHandle{}, nil }
			observe := ops.observe
			calls := 0
			var previous []memberObservation
			ops.observe = func(catalog *storecatalog.Catalog) ([]memberObservation, error) {
				calls++
				observations, err := observe(catalog)
				if err != nil {
					return nil, err
				}
				if calls == 3 {
					return test.final(previous)
				}
				previous = cloneObservations(observations)
				return observations, nil
			}
			lease, err := acquireWithOps(options, fence, ops)
			if lease != nil || database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("acquireWithOps() = %#v, %v", lease, err)
			}
			if test.name == "lookup failure" && !errors.Is(err, canary) {
				t.Fatalf("acquire error %v does not retain canary", err)
			}
		})
	}
}

func TestAcquireProjectedRejectsUnavailableInventory(t *testing.T) {
	if lease, err := AcquireProjected(nil, nil); lease != nil ||
		database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("AcquireProjected(nil) = %#v, %v", lease, err)
	}
}

func TestPhysicalClaimCollectionRejectsUnsafeAndOversizedInventories(t *testing.T) {
	if _, err := observeStores([]storecatalog.Spec{{ID: "global/test", Path: "bad\x00path"}}); err == nil {
		t.Fatalf("observeStores(invalid) = %v", err)
	}
	if key, exists, err := physicalClaimKey("bad\x00path"); key != "" || exists ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("physicalClaimKey(invalid) = %q, %t, %v", key, exists, err)
	}

	tooManyStores := make([]storecatalog.Spec, maxCatalogClaimPaths/4+1)
	if identities, err := physicalClaimIdentitiesForStores(tooManyStores); identities != nil ||
		database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("oversized stores = %#v, %v", identities, err)
	}

	mainOverflow := make([]storecatalog.Spec, maxCatalogClaimPaths/4)
	mainRoot := t.TempDir()
	for index := range mainOverflow {
		mainOverflow[index].Path = filepath.Join(mainRoot, fmt.Sprintf("store-%d", index))
	}
	mainOverflow[0].LegacyRoots = []string{filepath.Join(mainRoot, "legacy")}
	if identities, err := physicalClaimIdentitiesForStores(mainOverflow); identities != nil ||
		database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("member-overflow stores = %#v, %v", identities, err)
	}

	legacyOverflow := []storecatalog.Spec{{Path: filepath.Join(t.TempDir(), "store")}}
	legacyOverflow[0].LegacyRoots = make([]string, maxCatalogClaimPaths)
	if identities, err := physicalClaimIdentitiesForStores(legacyOverflow); identities != nil ||
		database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("legacy-overflow stores = %#v, %v", identities, err)
	}
}

func TestPinReplacementRejectsUnclaimedMissingAndContendedStages(t *testing.T) {
	t.Run("unclaimed store", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		stage := filepath.Join(lease.home, "stage-unclaimed.db")
		if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := lease.PinReplacement("global/not-claimed", stage); database.CodeOf(err) != database.CodeInvalid {
			t.Fatalf("PinReplacement(unclaimed) = %v", err)
		}
		if err := lease.Check(); err != nil {
			t.Fatalf("unclaimed PinReplacement poisoned lease: %v", err)
		}
	})

	t.Run("missing stage", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		if err := lease.PinReplacement(
			"global/auth", filepath.Join(lease.home, "missing-stage.db"),
		); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("PinReplacement(missing) = %v", err)
		}
	})

	t.Run("claim limit", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		stage := writeReplacementStage(t, lease.home, "stage-limit.db")
		for index := 0; len(lease.identities) < maxLeaseClaims; index++ {
			lease.identities[fmt.Sprintf("%064x", index)] = struct{}{}
		}
		if err := lease.PinReplacement("global/auth", stage); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("PinReplacement(limit) = %v", err)
		}
	})

	t.Run("contended physical claim", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		stage := writeReplacementStage(t, lease.home, "stage-busy.db")
		claim := holdPhysicalClaim(t, lease, stage)
		defer claim.close()
		if err := lease.PinReplacement("global/auth", stage); database.CodeOf(err) != database.CodeConflict {
			t.Fatalf("PinReplacement(contended) = %v", err)
		}
	})

	t.Run("unsafe physical claim lock", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		stage := writeReplacementStage(t, lease.home, "stage-unsafe-lock.db")
		identity := physicalClaimDigest(t, stage)
		lockPath := filepath.Join(lease.root, identity+".lock")
		t.Cleanup(func() { _ = os.Remove(lockPath) })
		if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(lockPath, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := lease.PinReplacement("global/auth", stage); err == nil {
			t.Fatal("PinReplacement accepted unsafe claim lock")
		}
	})

	t.Run("same stage for two stores", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		stage := writeReplacementStage(t, lease.home, "stage-shared.db")
		if err := lease.PinReplacement("global/auth", stage); err != nil {
			t.Fatal(err)
		}
		var second database.StoreID
		for _, store := range lease.stores {
			if store.ID != "global/auth" {
				second = store.ID
				break
			}
		}
		if second.IsZero() {
			t.Fatal("catalog has no second store")
		}
		if err := lease.PinReplacement(second, stage); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("PinReplacement(shared stage) = %v", err)
		}
	})
}

func TestRefreshRejectsInvalidReplacementAndNewClaimFailures(t *testing.T) {
	t.Run("unclaimed replacement", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		if err := lease.RefreshReplacement("global/not-claimed"); database.CodeOf(err) != database.CodeInvalid {
			t.Fatalf("RefreshReplacement(unclaimed) = %v", err)
		}
		if err := lease.Check(); err != nil {
			t.Fatalf("unclaimed RefreshReplacement poisoned lease: %v", err)
		}
	})

	t.Run("missing replacement main", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		if err := lease.RefreshReplacement("global/auth"); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("RefreshReplacement(missing main) = %v", err)
		}
	})

	t.Run("replacement was not pinned", func(t *testing.T) {
		lease := acquireMigrationLeaseWithMain(t)
		path := filepath.Join(lease.home, "auth.db")
		if err := os.Rename(path, path+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := lease.RefreshReplacement("global/auth"); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("RefreshReplacement(unpinned) = %v", err)
		}
	})

	t.Run("unsafe materialized member", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		target := writeReplacementStage(t, lease.home, "target.db")
		if err := os.Symlink(target, filepath.Join(lease.home, "auth.db-wal")); err != nil {
			t.Fatal(err)
		}
		if err := lease.Refresh(); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("Refresh(unsafe member) = %v", err)
		}
	})

	t.Run("claim limit", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		_ = writeReplacementStage(t, lease.home, "auth.db-wal")
		for index := 0; len(lease.identities) < maxLeaseClaims; index++ {
			lease.identities[fmt.Sprintf("%064x", index)] = struct{}{}
		}
		if err := lease.Refresh(); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("Refresh(limit) = %v", err)
		}
	})

	t.Run("contended physical claim", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		member := writeReplacementStage(t, lease.home, "auth.db-wal")
		claim := holdPhysicalClaim(t, lease, member)
		defer claim.close()
		if err := lease.Refresh(); database.CodeOf(err) != database.CodeConflict {
			t.Fatalf("Refresh(contended) = %v", err)
		}
	})

	t.Run("unsafe physical claim lock", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		member := writeReplacementStage(t, lease.home, "auth.db-wal")
		identity := physicalClaimDigest(t, member)
		lockPath := filepath.Join(lease.root, identity+".lock")
		t.Cleanup(func() { _ = os.Remove(lockPath) })
		if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(lockPath, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := lease.Refresh(); err == nil {
			t.Fatal("Refresh accepted unsafe claim lock")
		}
	})
}

func acquireMigrationLease(t *testing.T) *Lease {
	t.Helper()
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := testClaims.Acquire(testOptions(t, home, &config.Config{}), fence)
	if err != nil {
		_ = fence.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = lease.Close()
		_ = fence.Close()
	})
	return lease
}

func acquireMigrationLeaseWithMain(t *testing.T) *Lease {
	t.Helper()
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	if err := os.WriteFile(filepath.Join(home, "auth.db"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := testClaims.Acquire(testOptions(t, home, &config.Config{}), fence)
	if err != nil {
		_ = fence.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = lease.Close()
		_ = fence.Close()
	})
	return lease
}

func writeReplacementStage(t *testing.T, home, name string) string {
	t.Helper()
	path := filepath.Join(home, name)
	if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func physicalClaimDigest(t *testing.T, path string) string {
	t.Helper()
	key, exists, err := physicalClaimKey(path)
	if err != nil || !exists {
		t.Fatalf("physicalClaimKey(%q) = %q, %t, %v", path, key, exists, err)
	}
	digest := sha256.Sum256([]byte(physicalClaimIdentityVersion + key))
	return hex.EncodeToString(digest[:])
}

func holdPhysicalClaim(t *testing.T, lease *Lease, path string) claimHandle {
	t.Helper()
	claim, err := lease.acquireClaim(lease.root, physicalClaimDigest(t, path))
	if err != nil {
		t.Fatal(err)
	}
	return claim
}
