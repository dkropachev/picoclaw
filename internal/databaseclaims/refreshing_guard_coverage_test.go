package databaseclaims

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

type refreshingGuardLostClaim struct {
	closeCalls atomic.Int32
}

func (claim *refreshingGuardLostClaim) close() error {
	claim.closeCalls.Add(1)
	return nil
}

func (*refreshingGuardLostClaim) valid() bool { return false }

func TestGuardStoresRefreshingRejectsUnavailableLeaseStates(t *testing.T) {
	t.Run("nil lease", func(t *testing.T) {
		var lease *Lease
		stores, reconcile, release, err := lease.GuardStoresRefreshing()
		if stores != nil || reconcile != nil || release != nil ||
			database.CodeOf(err) != database.CodeUnavailable {
			t.Fatalf(
				"GuardStoresRefreshing(nil) = %#v, %t, %t, %v",
				stores, reconcile != nil, release != nil, err,
			)
		}
	})

	t.Run("closed lease", func(t *testing.T) {
		lease, _ := acquireRefreshingGuardLease(t, false)
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
		stores, reconcile, release, err := lease.GuardStoresRefreshing()
		if stores != nil || reconcile != nil || release != nil ||
			database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf(
				"GuardStoresRefreshing(closed) = %#v, %t, %t, %v",
				stores, reconcile != nil, release != nil, err,
			)
		}
	})
}

func TestGuardStoresRefreshingInitialRefreshFailureReleasesOwnership(t *testing.T) {
	lease, path := acquireRefreshingGuardLeaseWithBoundedCleanup(t)
	if err := os.WriteFile(path, []byte("materialized main"), 0o600); err != nil {
		t.Fatal(err)
	}
	canary := errors.New("refreshing guard claim canary")
	claimAttempts := 0
	lease.acquireClaim = func(string, string) (claimHandle, error) {
		claimAttempts++
		return nil, canary
	}

	stores, reconcile, release, err := lease.GuardStoresRefreshing()
	if stores != nil || reconcile != nil || release != nil || !errors.Is(err, canary) {
		t.Fatalf(
			"GuardStoresRefreshing(failed refresh) = %#v, %t, %t, %v",
			stores, reconcile != nil, release != nil, err,
		)
	}
	if claimAttempts != 1 || !lease.poisoned.Load() {
		t.Fatalf("failed refresh attempts=%d poisoned=%t", claimAttempts, lease.poisoned.Load())
	}

	closed := make(chan error, 1)
	go func() { closed <- lease.Close() }()
	select {
	case closeErr := <-closed:
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Lease.Close remained blocked after initial refresh failure")
	}
}

func TestGuardStoresRefreshingSerializesReconcileAndRelease(t *testing.T) {
	lease, path := acquireRefreshingGuardLease(t, false)
	originalAcquire := lease.acquireClaim
	if originalAcquire == nil {
		t.Fatal("test lease has no claim acquisition operation")
	}

	acquireEntered := make(chan struct{})
	allowAcquire := make(chan struct{})
	var enteredOnce sync.Once
	lease.acquireClaim = func(root, identity string) (claimHandle, error) {
		enteredOnce.Do(func() { close(acquireEntered) })
		<-allowAcquire
		return originalAcquire(root, identity)
	}

	_, reconcile, release, err := lease.GuardStoresRefreshing()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if err := os.WriteFile(path, []byte("materialized main"), 0o600); err != nil {
		release()
		t.Fatal(err)
	}

	reconciled := make(chan error, 1)
	go func() { reconciled <- reconcile() }()
	select {
	case <-acquireEntered:
	case <-time.After(time.Second):
		close(allowAcquire)
		release()
		t.Fatal("reconcile did not reach physical claim acquisition")
	}

	releaseStarted := make(chan struct{})
	released := make(chan struct{})
	go func() {
		close(releaseStarted)
		release()
		close(released)
	}()
	<-releaseStarted
	select {
	case <-released:
		close(allowAcquire)
		t.Fatal("release passed a reconcile that was acquiring a physical claim")
	case <-time.After(20 * time.Millisecond):
	}

	close(allowAcquire)
	if reconcileErr := <-reconciled; reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("release did not resume after reconcile completed")
	}

	// Defensive repeated cleanup must be inert rather than unlocking the lease
	// mutex or fence a second time.
	release()
	if err := lease.Check(); err != nil {
		t.Fatalf("lease authority after repeated release = %v", err)
	}
}

func TestClaimsDeltaMissingReplacementStage(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-stage.db")
	if canonical, err := canonicalReplacementPath(missing); canonical != "" ||
		!errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing canonical replacement = %q, %v", canonical, err)
	}
}

func TestPinReplacementRejectsStageIdentityDriftWhileClaiming(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows retained replacement handles intentionally prevent rename")
	}
	lease := acquireMigrationLease(t)
	stage := writeReplacementStage(t, lease.home, "drifting-stage.db")
	stagedIdentity, exists, err := regularPhysicalIdentity(stage)
	if err != nil || !exists {
		t.Fatalf("stage identity = %#v, %t, %v", stagedIdentity, exists, err)
	}
	originalAcquire := lease.acquireClaim
	changed := false
	lease.acquireClaim = func(root, identity string) (claimHandle, error) {
		if !changed {
			changed = true
			if err := os.Rename(stage, stage+".old"); err != nil {
				return nil, err
			}
			if err := os.WriteFile(stage, []byte("replacement"), 0o600); err != nil {
				return nil, err
			}
		}
		return originalAcquire(root, identity)
	}
	if pinErr := lease.PinReplacement("global/auth", stage); database.CodeOf(pinErr) != database.CodeIntegrity {
		t.Fatalf("PinReplacement with drifting stage = %v", pinErr)
	}
	if !changed || lease.replacementPins["global/auth"] != nil || !lease.poisoned.Load() {
		t.Fatalf(
			"drifting stage changed=%t pin=%#v poisoned=%t",
			changed,
			lease.replacementPins["global/auth"],
			lease.poisoned.Load(),
		)
	}
	if _, assigned := lease.pinAssignments[stagedIdentity.String()]; assigned {
		t.Fatal("drifting stage retained a pin assignment")
	}
	for _, resource := range lease.resources {
		if _, temporaryPin := resource.(*replacementPin); temporaryPin {
			t.Fatal("drifting stage retained its temporary handle")
		}
	}
}

func acquireRefreshingGuardLeaseWithBoundedCleanup(t *testing.T) (*Lease, string) {
	t.Helper()
	home := secureTestDir(t)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := newTestClaimAcquirer(t).Acquire(testOptions(t, home, &config.Config{}), fence)
	if err != nil {
		_ = fence.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closed := make(chan struct{})
		go func() {
			_ = lease.Close()
			_ = fence.Close()
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Error("bounded refreshing-guard cleanup timed out")
		}
	})
	return lease, filepath.Join(home, "auth.db")
}

func TestPinReplacementRejectsClaimLostBeforeSecondReconcile(t *testing.T) {
	lease := acquireMigrationLease(t)
	stage := writeReplacementStage(t, lease.home, "lost-claim-stage.db")
	claim := &refreshingGuardLostClaim{}
	lease.acquireClaim = func(string, string) (claimHandle, error) { return claim, nil }

	err := lease.PinReplacement("global/auth", stage)
	if database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("PinReplacement with immediately invalid claim = %v", err)
	}
	if lease.replacementPins["global/auth"] != nil {
		t.Fatal("failed replacement retained a published stage pin")
	}
	if !lease.poisoned.Load() {
		t.Fatal("invalid replacement claim did not poison the lease")
	}
	for _, resource := range lease.resources {
		if _, temporaryPin := resource.(*replacementPin); temporaryPin {
			t.Fatal("failed replacement retained its temporary stage handle")
		}
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if got := claim.closeCalls.Load(); got != 1 {
		t.Fatalf("invalid physical claim close calls = %d, want 1", got)
	}
}
