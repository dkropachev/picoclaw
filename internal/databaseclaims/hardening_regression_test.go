package databaseclaims

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestAcquireRejectsDuplicatePhysicalAssignments(t *testing.T) {
	for _, test := range []struct {
		name   string
		first  string
		second string
	}{
		{name: "two mains", first: "auth.db", second: "launcher-auth.db"},
		{name: "main and sidecar", first: "auth.db", second: "auth.db-wal"},
		{name: "generation and legacy", first: "auth.db", second: "auth.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			testClaims := newTestClaimAcquirer(t)
			home := secureTestDir(t)
			first := filepath.Join(home, test.first)
			second := filepath.Join(home, test.second)
			if err := os.WriteFile(first, []byte("shared"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(first, second); err != nil {
				t.Skipf("hardlinks unavailable: %v", err)
			}
			fence, err := database.AcquireOnlineFence(home)
			if err != nil {
				t.Fatal(err)
			}
			defer fence.Close()
			if lease, acquireErr := testClaims.Acquire(
				testOptions(t, home, &config.Config{}), fence,
			); lease != nil || database.CodeOf(acquireErr) != database.CodeIntegrity {
				t.Fatalf("duplicate physical assignment lease=%#v err=%v", lease, acquireErr)
			}
		})
	}
}

func TestAcquireRejectsDirectoryGenerationMembers(t *testing.T) {
	for _, relative := range []string{"auth.db", "auth.db-wal"} {
		t.Run(relative, func(t *testing.T) {
			testClaims := newTestClaimAcquirer(t)
			home := secureTestDir(t)
			if err := os.Mkdir(filepath.Join(home, relative), 0o700); err != nil {
				t.Fatal(err)
			}
			fence, err := database.AcquireOnlineFence(home)
			if err != nil {
				t.Fatal(err)
			}
			defer fence.Close()
			if lease, acquireErr := testClaims.Acquire(
				testOptions(t, home, &config.Config{}), fence,
			); lease != nil || database.CodeOf(acquireErr) != database.CodeIntegrity {
				t.Fatalf("directory generation lease=%#v err=%v", lease, acquireErr)
			}
		})
	}
}

func TestRefreshRejectsNewDuplicatePhysicalAssignments(t *testing.T) {
	for _, test := range []struct {
		name   string
		first  string
		second string
	}{
		{name: "two mains", first: "auth.db", second: "launcher-auth.db"},
		{name: "main and sidecar", first: "auth.db", second: "auth.db-wal"},
		{name: "generation and legacy", first: "auth.db", second: "auth.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			lease := acquireMigrationLease(t)
			first := filepath.Join(lease.home, test.first)
			second := filepath.Join(lease.home, test.second)
			if err := os.WriteFile(first, []byte("shared"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(first, second); err != nil {
				t.Skipf("hardlinks unavailable: %v", err)
			}
			if err := lease.Refresh(); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("Refresh duplicate assignment = %v", err)
			}
		})
	}
}

func TestRefreshRejectsPhysicalIdentityReassignment(t *testing.T) {
	for _, test := range []struct {
		name   string
		before string
		after  string
	}{
		{name: "legacy to sidecar", before: "auth.json", after: "auth.db-wal"},
		{name: "sidecar to other store", before: "auth.db-wal", after: "launcher-auth.db-shm"},
	} {
		t.Run(test.name, func(t *testing.T) {
			testClaims := newTestClaimAcquirer(t)
			home := secureTestDir(t)
			before := filepath.Join(home, test.before)
			if err := os.WriteFile(before, []byte("member"), 0o600); err != nil {
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
			if err := os.Rename(before, filepath.Join(home, test.after)); err != nil {
				t.Fatal(err)
			}
			if err := lease.Refresh(); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("Refresh reassigned identity = %v", err)
			}
		})
	}
}

func TestRefreshRetainsAssignmentHistoryAcrossDisappearance(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	legacy := filepath.Join(home, "auth.json")
	parked := filepath.Join(home, "parked-member")
	if err := os.WriteFile(legacy, []byte("member"), 0o600); err != nil {
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
	if err := os.Rename(legacy, parked); err != nil {
		t.Fatal(err)
	}
	if err := lease.Refresh(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parked, filepath.Join(home, "launcher-auth.db-wal")); err != nil {
		t.Fatal(err)
	}
	if err := lease.Refresh(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("Refresh forgotten reassignment = %v", err)
	}
}

func TestPinReplacementReconcilesAndRejectsUnsafeStages(t *testing.T) {
	t.Run("new catalog alias", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		catalogPath := filepath.Join(lease.home, "launcher-auth.db")
		stage := filepath.Join(lease.home, "stage-alias.db")
		if err := os.WriteFile(catalogPath, []byte("catalog"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(catalogPath, stage); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		if err := lease.PinReplacement("global/auth", stage); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("PinReplacement(new catalog alias) = %v", err)
		}
	})

	t.Run("relative", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		if err := lease.PinReplacement("global/auth", "stage.db"); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("PinReplacement(relative) = %v", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		stage := filepath.Join(lease.home, "stage-directory")
		if err := os.Mkdir(stage, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := lease.PinReplacement("global/auth", stage); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("PinReplacement(directory) = %v", err)
		}
	})

	t.Run("non-catalog hardlink", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		stage := writeReplacementStage(t, lease.home, "hardlinked-stage.db")
		alias := filepath.Join(lease.home, "hardlinked-stage-alias.db")
		if err := os.Link(stage, alias); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		if err := lease.PinReplacement("global/auth", stage); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("PinReplacement(hard-linked stage) = %v", err)
		}
	})

	t.Run("exact catalog path", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		catalogPath := filepath.Join(lease.home, "launcher-auth.db")
		if err := os.WriteFile(catalogPath, []byte("catalog"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := lease.Refresh(); err != nil {
			t.Fatal(err)
		}
		if err := lease.PinReplacement("global/auth", catalogPath); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("PinReplacement(exact catalog path) = %v", err)
		}
	})

	t.Run("failed initial reconciliation", func(t *testing.T) {
		lease := acquireMigrationLeaseWithMain(t)
		if err := os.Remove(filepath.Join(lease.home, "auth.db")); err != nil {
			t.Fatal(err)
		}
		stage := writeReplacementStage(t, lease.home, "stage-after-drift.db")
		if err := lease.PinReplacement("global/auth", stage); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("PinReplacement(after main disappearance) = %v", err)
		}
	})
}

func TestReplacementAssignmentsRemainMonotonicAfterRepin(t *testing.T) {
	t.Run("cannot repin old stage to another store", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		first := writeReplacementStage(t, lease.home, "first-stage.db")
		second := writeReplacementStage(t, lease.home, "second-stage.db")
		if err := lease.PinReplacement("global/auth", first); err != nil {
			t.Fatal(err)
		}
		if err := lease.PinReplacement("global/auth", second); err != nil {
			t.Fatal(err)
		}
		if err := lease.PinReplacement("launcher/auth", first); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("repin old stage to another StoreID = %v", err)
		}
	})

	t.Run("cannot materialize old stage for another store", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		first := writeReplacementStage(t, lease.home, "first-materialized-stage.db")
		second := writeReplacementStage(t, lease.home, "second-materialized-stage.db")
		if err := lease.PinReplacement("global/auth", first); err != nil {
			t.Fatal(err)
		}
		if err := lease.PinReplacement("global/auth", second); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(first, filepath.Join(lease.home, "launcher-auth.db")); err != nil {
			t.Fatal(err)
		}
		if err := lease.Refresh(); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("materialize old stage for another StoreID = %v", err)
		}
	})

	t.Run("cannot materialize abandoned stage for same store", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		first := writeReplacementStage(t, lease.home, "abandoned-stage.db")
		second := writeReplacementStage(t, lease.home, "active-stage.db")
		if err := lease.PinReplacement("global/auth", first); err != nil {
			t.Fatal(err)
		}
		if err := lease.PinReplacement("global/auth", second); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(first, filepath.Join(lease.home, "auth.db")); err != nil {
			t.Fatal(err)
		}
		if err := lease.Refresh(); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("ordinary Refresh promoted abandoned stage = %v", err)
		}
	})

	t.Run("replacement refresh rejects abandoned stage", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		first := writeReplacementStage(t, lease.home, "abandoned-replacement.db")
		second := writeReplacementStage(t, lease.home, "active-replacement.db")
		if err := lease.PinReplacement("global/auth", first); err != nil {
			t.Fatal(err)
		}
		if err := lease.PinReplacement("global/auth", second); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(first, filepath.Join(lease.home, "auth.db")); err != nil {
			t.Fatal(err)
		}
		if err := lease.RefreshReplacement("global/auth"); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("RefreshReplacement promoted abandoned stage = %v", err)
		}
	})

	t.Run("active repin can complete controlled cutover", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		first := writeReplacementStage(t, lease.home, "retired-stage.db")
		second := writeReplacementStage(t, lease.home, "current-stage.db")
		if err := lease.PinReplacement("global/auth", first); err != nil {
			t.Fatal(err)
		}
		if err := lease.PinReplacement("global/auth", second); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(second, filepath.Join(lease.home, "auth.db")); err != nil {
			t.Fatal(err)
		}
		if err := lease.RefreshReplacement("global/auth"); err != nil {
			t.Fatalf("RefreshReplacement(active repin) = %v", err)
		}
	})
}

func TestAlternatingRepinsKeepRetainedResourcesBounded(t *testing.T) {
	lease := acquireMigrationLease(t)
	first := writeReplacementStage(t, lease.home, "bounded-first.db")
	second := writeReplacementStage(t, lease.home, "bounded-second.db")
	for index := 0; index < 32; index++ {
		stage := first
		if index%2 != 0 {
			stage = second
		}
		if err := lease.PinReplacement("global/auth", stage); err != nil {
			t.Fatalf("repin %d = %v", index, err)
		}
		if len(lease.resources) != len(lease.claims)+1 {
			t.Fatalf(
				"repin %d retained resources=%d claims=%d",
				index, len(lease.resources), len(lease.claims),
			)
		}
	}
}

func TestRefreshDoesNotReenterFenceWithQueuedClose(t *testing.T) {
	lease := acquireMigrationLease(t)
	ops := defaultClaimRefreshOps()
	originalObserve := ops.observe
	entered := make(chan struct{})
	releaseObservation := make(chan struct{})
	var once sync.Once
	ops.observe = func(catalog *storecatalog.Catalog) ([]memberObservation, error) {
		once.Do(func() {
			close(entered)
			<-releaseObservation
		})
		return originalObserve(catalog)
	}
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- lease.refreshWithOps("", ops) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Refresh did not enter guarded observation")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- lease.fence.Close() }()
	select {
	case err := <-closeDone:
		close(releaseObservation)
		t.Fatalf("fence closed through live Refresh guard: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseObservation)
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatalf("guarded Refresh = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Refresh deadlocked behind queued fence close")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("fence close did not resume after Refresh")
	}
}

func TestRefreshRejectsReplacementPinInvalidatedDuringTransition(t *testing.T) {
	lease := acquireMigrationLease(t)
	stage := writeReplacementStage(t, lease.home, "invalidate-during-refresh.db")
	if err := lease.PinReplacement("global/auth", stage); err != nil {
		t.Fatal(err)
	}
	ops := defaultClaimRefreshOps()
	originalObserve := ops.observe
	entered := make(chan struct{})
	releaseObservation := make(chan struct{})
	var once sync.Once
	ops.observe = func(catalog *storecatalog.Catalog) ([]memberObservation, error) {
		once.Do(func() {
			close(entered)
			<-releaseObservation
		})
		return originalObserve(catalog)
	}
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- lease.refreshWithOps("", ops) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Refresh did not enter observation")
	}
	alias := stage + ".hardlink"
	if err := os.Link(stage, alias); err != nil {
		close(releaseObservation)
		t.Skipf("hardlinks unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(alias) })
	close(releaseObservation)
	select {
	case err := <-refreshDone:
		if database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("Refresh with invalidated pin = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Refresh hung after replacement pin invalidation")
	}
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalidated pin did not poison lease: %v", err)
	}
}

func TestLeaseValidatesReplacementPinProof(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Lease, *replacementPin)
	}{
		{name: "missing physical claim", mutate: func(lease *Lease, pin *replacementPin) {
			delete(lease.identities, pin.claimIdentity)
		}},
		{name: "wrong digest", mutate: func(_ *Lease, pin *replacementPin) {
			pin.claimIdentity = strings.Repeat("f", 64)
		}},
		{name: "wrong target", mutate: func(lease *Lease, pin *replacementPin) {
			lease.pinAssignments[pin.identity.String()] = "other-target"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			lease := acquireMigrationLease(t)
			stage := writeReplacementStage(t, lease.home, "proof-stage.db")
			if err := lease.PinReplacement("global/auth", stage); err != nil {
				t.Fatal(err)
			}
			pin := lease.replacementPins["global/auth"]
			test.mutate(lease, pin)
			if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("corrupt pin proof Check = %v", err)
			}
		})
	}

	t.Run("preexisting assignment blocks pin", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		stage := writeReplacementStage(t, lease.home, "assigned-stage.db")
		identity, exists, err := regularPhysicalIdentity(stage)
		if err != nil || !exists {
			t.Fatalf("stage identity = %#v, %t, %v", identity, exists, err)
		}
		lease.assignments[identity.String()] = "another-member"
		if err := lease.PinReplacement("global/auth", stage); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("PinReplacement(preassigned stage) = %v", err)
		}
	})
}

func TestRefreshReplacementRequiresCutoverAndMigrationFence(t *testing.T) {
	t.Run("online fence", func(t *testing.T) {
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
		if err := lease.RefreshReplacement("global/auth"); database.CodeOf(err) != database.CodeUnauthorized {
			t.Fatalf("online RefreshReplacement = %v", err)
		}
	})

	t.Run("before cutover", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		stage := writeReplacementStage(t, lease.home, "stage-before-cutover.db")
		if err := lease.PinReplacement("global/auth", stage); err != nil {
			t.Fatal(err)
		}
		if err := lease.RefreshReplacement("global/auth"); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("RefreshReplacement(before cutover) = %v", err)
		}
	})
}

func TestRefreshRejectsDisappearedMain(t *testing.T) {
	lease := acquireMigrationLeaseWithMain(t)
	if err := os.Remove(filepath.Join(lease.home, "auth.db")); err != nil {
		t.Fatal(err)
	}
	if err := lease.Refresh(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("Refresh(disappeared main) = %v", err)
	}
}

func TestLeaseRejectsInvalidRetainedReplacementHandle(t *testing.T) {
	lease := acquireMigrationLease(t)
	lease.replacementPins["global/auth"] = nil
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("Check(invalid replacement pin) = %v", err)
	}
	if _, found := findObservation(lease.observations, "global/missing", memberMain); found {
		t.Fatal("missing observation was found")
	}
}

func TestOrdinaryAcquireDoesNotTouchStableClaimRoot(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := secureTestDir(t)
	options := testOptions(t, home, &config.Config{})
	projected, err := storecatalog.Project(options)
	if err != nil {
		t.Fatal(err)
	}
	stableNames := stableClaimFileNamesForCatalog(t, projected)
	assertStableClaimFilesAbsent(t, stableNames)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.Acquire(options, fence)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	assertStableClaimFilesAbsent(t, stableNames)
}
