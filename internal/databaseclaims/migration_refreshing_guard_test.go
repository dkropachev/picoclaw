package databaseclaims

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestMigrationRefreshingGuardPromotesExactReplacementWithoutReentry(t *testing.T) {
	for _, withMain := range []bool{false, true} {
		name := "missing"
		if withMain {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			var lease *Lease
			if withMain {
				lease = acquireMigrationLeaseWithMain(t)
			} else {
				lease = acquireMigrationLease(t)
			}
			guard, err := lease.GuardStoresMigrating()
			if err != nil {
				t.Fatal(err)
			}
			stores, err := guard.Stores()
			if err != nil || len(stores) == 0 {
				_ = guard.Release()
				t.Fatalf("migration stores = %d, %v", len(stores), err)
			}
			var target string
			for _, store := range stores {
				if store.ID == "global/auth" {
					target = store.Path
					store.Path = "detached"
				}
			}
			if target == "" {
				_ = guard.Release()
				t.Fatal("auth store is absent")
			}
			fresh, err := guard.Stores()
			if err != nil || len(fresh) == 0 || fresh[0].Path == "detached" {
				_ = guard.Release()
				t.Fatalf("guard exposed mutable stores: %#v, %v", fresh, err)
			}
			stage := writeReplacementStage(t, lease.home, "migration-stage.db")
			if err := guard.PinReplacement("global/auth", stage); err != nil {
				_ = guard.Release()
				t.Fatal(err)
			}
			if withMain {
				if err := os.Rename(target, target+".old"); err != nil {
					_ = guard.Release()
					t.Fatal(err)
				}
			}
			if err := os.Rename(stage, target); err != nil {
				_ = guard.Release()
				t.Fatal(err)
			}
			if err := guard.ReconcileReplacement("global/auth"); err != nil {
				_ = guard.Release()
				t.Fatal(err)
			}
			if err := guard.Check(); err != nil {
				_ = guard.Release()
				t.Fatal(err)
			}
			if err := guard.Release(); err != nil {
				t.Fatal(err)
			}
			if err := guard.Release(); err != nil {
				t.Fatalf("repeat migration guard release = %v", err)
			}
			if err := guard.Check(); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("released migration guard Check = %v", err)
			}
			if stores, err := guard.Stores(); stores != nil ||
				database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("released migration stores = %#v, %v", stores, err)
			}
			if err := lease.Check(); err != nil {
				t.Fatal(err)
			}
			followup, followupErr := lease.GuardStoresMigrating()
			if followupErr != nil {
				t.Fatalf("follow-up migration guard rejected reconciled main baseline: %v", followupErr)
			}
			if err := followup.Release(); err != nil {
				t.Fatalf("follow-up migration guard release: %v", err)
			}
		})
	}
}

func TestMigrationRefreshingGuardRejectsMainMaterializedBeforeEntry(t *testing.T) {
	for _, refreshBeforeGuard := range []bool{false, true} {
		name := "direct"
		if refreshBeforeGuard {
			name = "after ordinary refresh"
		}
		t.Run(name, func(t *testing.T) {
			lease := acquireMigrationLease(t)
			index, ok := lease.byID["global/auth"]
			if !ok {
				t.Fatal("auth store is absent")
			}
			if err := os.WriteFile(lease.stores[index].Path, []byte("unapproved-main"), 0o600); err != nil {
				t.Fatal(err)
			}
			if refreshBeforeGuard {
				if err := lease.Refresh(); err != nil {
					t.Fatalf("ordinary refresh before migration guard: %v", err)
				}
			}
			guard, err := lease.GuardStoresMigrating()
			if guard != nil || database.CodeOf(err) != database.CodeIntegrity {
				if guard != nil {
					_ = guard.Release()
				}
				t.Fatalf("migration guard after unapproved main = %#v, %v", guard, err)
			}
			if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("unapproved pre-entry main did not poison lease: %v", err)
			}
		})
	}
}

func TestMigrationRefreshingGuardReconcilesMembersAndRetiresUnusedPin(t *testing.T) {
	lease := acquireMigrationLeaseWithMain(t)
	guard, err := lease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	stores, err := guard.Stores()
	if err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	var path string
	for _, store := range stores {
		if store.ID == "global/auth" {
			path = store.Path
		}
	}
	if path == "" {
		_ = guard.Release()
		t.Fatal("auth store is absent")
	}
	before := len(lease.identities)
	if err := os.WriteFile(path+"-wal", []byte("wal"), 0o600); err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	if err := guard.Reconcile(); err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	if len(lease.identities) < before+1 {
		_ = guard.Release()
		t.Fatalf("migration reconciliation identities = %d, want at least %d", len(lease.identities), before+1)
	}
	stage := writeReplacementStage(t, lease.home, "unused-stage.db")
	if err := guard.PinReplacement("global/auth", stage); err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	if lease.replacementPins["global/auth"] == nil {
		_ = guard.Release()
		t.Fatal("migration guard retained no replacement pin")
	}
	if err := guard.Release(); err != nil {
		t.Fatal(err)
	}
	if lease.replacementPins["global/auth"] != nil {
		t.Fatal("migration guard release retained unused replacement pin")
	}
	if err := lease.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationRefreshingGuardReleaseRejectsUnreconciledCutover(t *testing.T) {
	lease := acquireMigrationLease(t)
	guard, err := lease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	stores, err := guard.Stores()
	if err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	var target string
	for _, store := range stores {
		if store.ID == "global/auth" {
			target = store.Path
		}
	}
	stage := writeReplacementStage(t, lease.home, "unreconciled-stage.db")
	if err := guard.PinReplacement("global/auth", stage); err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	if err := os.Rename(stage, target); err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	if err := guard.Release(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("unreconciled cutover release = %v", err)
	}
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("unreconciled cutover did not poison lease: %v", err)
	}
}

func TestMigrationRefreshingGuardRejectsEveryUnapprovedMissingMainMaterialization(t *testing.T) {
	for _, test := range []struct {
		name string
		pin  bool
	}{
		{name: "unpinned"},
		{name: "different from pin", pin: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			lease := acquireMigrationLease(t)
			guard, err := lease.GuardStoresMigrating()
			if err != nil {
				t.Fatal(err)
			}
			stores, err := guard.Stores()
			if err != nil {
				_ = guard.Release()
				t.Fatal(err)
			}
			var target string
			for _, store := range stores {
				if store.ID == "global/auth" {
					target = store.Path
				}
			}
			if test.pin {
				stage := writeReplacementStage(t, lease.home, "unused-pinned-stage.db")
				if err := guard.PinReplacement("global/auth", stage); err != nil {
					_ = guard.Release()
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(target, []byte("unapproved-main"), 0o600); err != nil {
				_ = guard.Release()
				t.Fatal(err)
			}
			candidate := writeReplacementStage(t, lease.home, "candidate-after-main.db")
			if err := guard.PinReplacement("global/auth", candidate); database.CodeOf(err) != database.CodeIntegrity {
				_ = guard.Release()
				t.Fatalf("pin after unapproved main = %v", err)
			}
			if err := guard.Release(); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("unapproved main release = %v", err)
			}
			if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("unapproved main did not poison lease: %v", err)
			}
		})
	}
}

func TestMigrationRefreshingGuardSerializesLeaseClose(t *testing.T) {
	lease := acquireMigrationLease(t)
	guard, err := lease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- lease.Close() }()
	select {
	case err := <-closed:
		_ = guard.Release()
		t.Fatalf("lease closed through migration guard: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	if err := guard.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("lease close did not resume after migration guard release")
	}
}

func TestMigrationRefreshingGuardCopiesShareOneLifecycle(t *testing.T) {
	lease := acquireMigrationLease(t)
	guard, err := lease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	copyOfGuard := *guard
	if err := copyOfGuard.Release(); err != nil {
		t.Fatal(err)
	}
	if err := guard.Release(); err != nil {
		t.Fatalf("original after copied guard release = %v", err)
	}
	if err := copyOfGuard.Release(); err != nil {
		t.Fatalf("repeat copied guard release = %v", err)
	}
	if err := lease.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationRefreshingGuardPoisonsAuthorityLostDuringFinalObservation(t *testing.T) {
	lease := acquireMigrationLease(t)
	guard, err := lease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	observe := guard.state.ops.observe
	guard.state.ops.observe = func(catalog *storecatalog.Catalog) ([]memberObservation, error) {
		observed, observeErr := observe(catalog)
		guard.state.ops.guardedAuthority = func() bool { return false }
		return observed, observeErr
	}
	if err := guard.Reconcile(); database.CodeOf(err) != database.CodeIntegrity {
		_ = guard.Release()
		t.Fatalf("final-observation authority loss = %v", err)
	}
	if err := guard.Release(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("authority-lost guard release = %v", err)
	}
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("authority loss did not poison lease: %v", err)
	}
}

func TestMigrationRefreshingGuardFinalObservationRejectsUnclaimedLateSidecar(t *testing.T) {
	lease := acquireMigrationLeaseWithMain(t)
	guard, err := lease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	stores, err := guard.Stores()
	if err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	var path string
	for _, store := range stores {
		if store.ID == "global/auth" {
			path = store.Path
		}
	}
	observe := guard.state.ops.observe
	observeCalls := 0
	guard.state.ops.observe = func(catalog *storecatalog.Catalog) ([]memberObservation, error) {
		observeCalls++
		if observeCalls == 4 {
			if writeErr := os.WriteFile(path+"-wal", []byte("late-wal"), 0o600); writeErr != nil {
				return nil, writeErr
			}
		}
		return observe(catalog)
	}
	if err := guard.Reconcile(); database.CodeOf(err) != database.CodeIntegrity {
		_ = guard.Release()
		t.Fatalf("late unclaimed sidecar reconcile = %v", err)
	}
	if observeCalls < 4 {
		_ = guard.Release()
		t.Fatalf("late sidecar was not injected: observe calls=%d", observeCalls)
	}
	identity, exists, identityErr := fileidentity.Existing(path + "-wal")
	if identityErr != nil || !exists {
		_ = guard.Release()
		t.Fatalf("late sidecar identity = %#v, %t, %v", identity, exists, identityErr)
	}
	if _, claimed := lease.identities[digestPhysicalIdentity(identity)]; claimed {
		_ = guard.Release()
		t.Fatal("late sidecar unexpectedly held a physical claim")
	}
	if err := guard.Release(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("late-sidecar guard release = %v", err)
	}
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("late unclaimed sidecar did not poison lease: %v", err)
	}
}

func TestMigrationRefreshingGuardRejectsWrongAuthorityAndInputs(t *testing.T) {
	if err := (*Lease)(nil).pinReplacementLocked(
		"global/auth", "stage.db", 0, defaultClaimRefreshOps(),
	); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("nil under-lock replacement pin = %v", err)
	}
	if guard, err := (*Lease)(nil).GuardStoresMigrating(); guard != nil ||
		database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil migration guard = %#v, %v", guard, err)
	}
	if stores, err := (*MigrationRefreshingGuard)(nil).Stores(); stores != nil ||
		database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil migration stores = %#v, %v", stores, err)
	}
	if err := (*MigrationRefreshingGuard)(nil).Check(); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil migration Check = %v", err)
	}
	if err := (*MigrationRefreshingGuard)(nil).Reconcile(); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil migration Reconcile = %v", err)
	}
	if err := (*MigrationRefreshingGuard)(nil).PinReplacement("", ""); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil migration PinReplacement = %v", err)
	}
	if err := (*MigrationRefreshingGuard)(nil).ReconcileReplacement(""); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil migration ReconcileReplacement = %v", err)
	}
	if err := (*MigrationRefreshingGuard)(nil).Release(); err != nil {
		t.Fatalf("nil migration Release = %v", err)
	}

	onlineLease, _ := acquireRefreshingGuardLease(t, false)
	if guard, err := onlineLease.GuardStoresMigrating(); guard != nil ||
		database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("online migration guard = %#v, %v", guard, err)
	}

	lease := acquireMigrationLease(t)
	guard, err := lease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.PinReplacement("bad", "relative"); database.CodeOf(err) != database.CodeInvalid {
		_ = guard.Release()
		t.Fatalf("invalid migration pin = %v", err)
	}
	missingStage := filepath.Join(lease.home, "stage.db")
	if err := guard.PinReplacement("global/missing", missingStage); database.CodeOf(err) != database.CodeInvalid {
		_ = guard.Release()
		t.Fatalf("unknown migration pin = %v", err)
	}
	if err := guard.ReconcileReplacement("global/auth"); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("unpinned migration replacement = %v", err)
	}
	if err := guard.Release(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("poisoned migration guard release = %v", err)
	}
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("unpinned migration replacement did not poison lease: %v", err)
	}
}

func TestMigrationRefreshingGuardRejectsPreexistingReplacementPin(t *testing.T) {
	lease := acquireMigrationLease(t)
	stage := writeReplacementStage(t, lease.home, "preexisting-stage.db")
	if err := lease.PinReplacement("global/auth", stage); err != nil {
		t.Fatal(err)
	}
	if guard, err := lease.GuardStoresMigrating(); guard != nil ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("migration guard with preexisting pin = %#v, %v", guard, err)
	}
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("preexisting pin did not poison lease: %v", err)
	}
}

func TestMigrationRefreshingGuardRejectsCorruptApprovedMainBaseline(t *testing.T) {
	for _, substitute := range []bool{false, true} {
		name := "missing key"
		if substitute {
			name = "substituted key"
		}
		t.Run(name, func(t *testing.T) {
			lease := acquireMigrationLease(t)
			delete(lease.approvedMains, "global/auth")
			if substitute {
				lease.approvedMains["global/unknown"] = fileidentity.Identity{}
			}
			guard, err := lease.GuardStoresMigrating()
			if guard != nil || database.CodeOf(err) != database.CodeIntegrity {
				if guard != nil {
					_ = guard.Release()
				}
				t.Fatalf("migration guard with corrupt approved baseline = %#v, %v", guard, err)
			}
			if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("corrupt approved baseline did not poison lease: %v", err)
			}
		})
	}
}

func TestMigrationRefreshingGuardEntryFailureCoverage(t *testing.T) {
	t.Run("initial refresh", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		index := lease.byID["global/auth"]
		if err := os.WriteFile(lease.stores[index].Path+"-wal", []byte("wal"), 0o600); err != nil {
			t.Fatal(err)
		}
		canary := errors.New("initial refresh canary")
		lease.acquireClaim = func(string, string) (claimHandle, error) { return nil, canary }
		guard, err := lease.GuardStoresMigrating()
		if guard != nil || !errors.Is(err, canary) {
			t.Fatalf("migration guard initial refresh failure = %#v, %v", guard, err)
		}
	})

	t.Run("final validation", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		index := lease.byID["global/auth"]
		if err := os.WriteFile(lease.stores[index].Path+"-wal", []byte("wal"), 0o600); err != nil {
			t.Fatal(err)
		}
		originalAcquire := lease.acquireClaim
		lease.acquireClaim = func(root, identity string) (claimHandle, error) {
			claim, err := originalAcquire(root, identity)
			if err == nil {
				delete(lease.approvedMains, "global/auth")
				lease.approvedMains["global/unknown"] = fileidentity.Identity{}
			}
			return claim, err
		}
		guard, err := lease.GuardStoresMigrating()
		if guard != nil || database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("migration guard final validation failure = %#v, %v", guard, err)
		}
	})
}

func TestMigrationRefreshingGuardMethodFailureCoverage(t *testing.T) {
	t.Run("released", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		guard, err := lease.GuardStoresMigrating()
		if err != nil {
			t.Fatal(err)
		}
		if err := guard.Release(); err != nil {
			t.Fatal(err)
		}
		for operation, err := range map[string]error{
			"reconcile":             guard.Reconcile(),
			"pin":                   guard.PinReplacement("global/auth", "stage.db"),
			"reconcile replacement": guard.ReconcileReplacement("global/auth"),
		} {
			if database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("released guard %s = %v", operation, err)
			}
		}
	})

	t.Run("reconcile refresh", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		guard, err := lease.GuardStoresMigrating()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = guard.Release() }()
		index := lease.byID["global/auth"]
		if err := os.WriteFile(lease.stores[index].Path+"-wal", []byte("wal"), 0o600); err != nil {
			t.Fatal(err)
		}
		canary := errors.New("reconcile refresh canary")
		guard.state.ops.acquire = func(string, string) (claimHandle, error) { return nil, canary }
		if err := guard.Reconcile(); !errors.Is(err, canary) {
			t.Fatalf("migration reconcile refresh failure = %v", err)
		}
	})

	t.Run("pin helper", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		guard, err := lease.GuardStoresMigrating()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = guard.Release() }()
		missing := filepath.Join(lease.home, "missing-stage.db")
		if err := guard.PinReplacement("global/auth", missing); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("migration pin missing stage = %v", err)
		}
	})

	t.Run("unknown replacement", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		guard, err := lease.GuardStoresMigrating()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = guard.Release() }()
		if err := guard.ReconcileReplacement("global/missing"); database.CodeOf(err) != database.CodeInvalid {
			t.Fatalf("unknown migration replacement = %v", err)
		}
	})

	t.Run("missing pin ledger", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		guard, err := lease.GuardStoresMigrating()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = guard.Release() }()
		guard.state.pinned["global/auth"] = struct{}{}
		if err := guard.ReconcileReplacement("global/auth"); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("missing migration pin ledger entry = %v", err)
		}
	})

	t.Run("replacement refresh", func(t *testing.T) {
		lease := acquireMigrationLease(t)
		guard, err := lease.GuardStoresMigrating()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = guard.Release() }()
		stage := writeReplacementStage(t, lease.home, "refresh-failure-stage.db")
		if err := guard.PinReplacement("global/auth", stage); err != nil {
			t.Fatal(err)
		}
		canary := errors.New("replacement refresh canary")
		guard.state.ops.revalidate = func(*storecatalog.Catalog) (*storecatalog.Catalog, error) {
			return nil, canary
		}
		if err := guard.ReconcileReplacement("global/auth"); !errors.Is(err, canary) {
			t.Fatalf("replacement refresh failure = %v", err)
		}
	})
}

func TestMigrationRefreshingGuardObservationFailureCoverage(t *testing.T) {
	nilStateErr := (*migrationRefreshingGuardState)(nil).validateExpectedMainsLocked()
	if database.CodeOf(nilStateErr) != database.CodeIntegrity {
		t.Fatalf("nil migration observation state = %v", nilStateErr)
	}

	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *MigrationRefreshingGuard)
		match  func(error) bool
	}{
		{
			name: "invalid state",
			mutate: func(_ *testing.T, guard *MigrationRefreshingGuard) {
				guard.state.expectedMain = nil
			},
			match: func(err error) bool { return database.CodeOf(err) == database.CodeIntegrity },
		},
		{
			name: "approved baseline drift",
			mutate: func(t *testing.T, guard *MigrationRefreshingGuard) {
				stage := writeReplacementStage(t, guard.state.lease.home, "baseline-drift.db")
				identity, exists, err := fileidentity.Existing(stage)
				if err != nil || !exists {
					t.Fatalf("baseline drift identity = %#v, %t, %v", identity, exists, err)
				}
				guard.state.lease.approvedMains["global/auth"] = identity
			},
			match: func(err error) bool { return database.CodeOf(err) == database.CodeIntegrity },
		},
		{
			name: "catalog revalidation",
			mutate: func(_ *testing.T, guard *MigrationRefreshingGuard) {
				guard.state.ops.revalidate = func(*storecatalog.Catalog) (*storecatalog.Catalog, error) {
					return nil, errors.New("catalog revalidation canary")
				}
			},
			match: func(err error) bool { return strings.Contains(err.Error(), "catalog revalidation canary") },
		},
		{
			name: "physical observation",
			mutate: func(_ *testing.T, guard *MigrationRefreshingGuard) {
				guard.state.ops.observe = func(*storecatalog.Catalog) ([]memberObservation, error) {
					return nil, errors.New("physical observation canary")
				}
			},
			match: func(err error) bool { return strings.Contains(err.Error(), "physical observation canary") },
		},
		{
			name: "duplicate main",
			mutate: func(_ *testing.T, guard *MigrationRefreshingGuard) {
				guard.state.ops.observe = func(*storecatalog.Catalog) ([]memberObservation, error) {
					return []memberObservation{
						{StoreID: "global/auth", Role: memberMain},
						{StoreID: "global/auth", Role: memberMain},
					}, nil
				}
			},
			match: func(err error) bool { return database.CodeOf(err) == database.CodeIntegrity },
		},
		{
			name: "unknown main",
			mutate: func(_ *testing.T, guard *MigrationRefreshingGuard) {
				guard.state.ops.observe = func(*storecatalog.Catalog) ([]memberObservation, error) {
					return []memberObservation{{StoreID: "global/unknown", Role: memberMain}}, nil
				}
			},
			match: func(err error) bool { return database.CodeOf(err) == database.CodeIntegrity },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			lease := acquireMigrationLease(t)
			guard, err := lease.GuardStoresMigrating()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = guard.Release() }()
			test.mutate(t, guard)
			err = guard.Reconcile()
			if err == nil || !test.match(err) {
				t.Fatalf("migration observation %s failure = %v", test.name, err)
			}
		})
	}
}

func TestRefreshReplacementRejectsUnavailableApprovedMainBaseline(t *testing.T) {
	lease := acquireMigrationLease(t)
	stage := writeReplacementStage(t, lease.home, "missing-baseline-stage.db")
	if err := lease.PinReplacement("global/auth", stage); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stage, filepath.Join(lease.home, "auth.db")); err != nil {
		t.Fatal(err)
	}
	delete(lease.approvedMains, "global/auth")
	if err := lease.RefreshReplacement("global/auth"); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("replacement without approved main baseline = %v", err)
	}
}

type callbackReplacementHandle struct {
	onClose func()
}

func (handle *callbackReplacementHandle) close() error {
	if handle != nil && handle.onClose != nil {
		handle.onClose()
		handle.onClose = nil
	}
	return nil
}

func (*callbackReplacementHandle) valid() bool { return true }

func (*callbackReplacementHandle) matches(fileidentity.Identity) bool { return true }

func TestMigrationRefreshingGuardRetirementCoverage(t *testing.T) {
	t.Run("unowned resource", func(t *testing.T) {
		unowned := &replacementPin{handle: &callbackReplacementHandle{}}
		owned := &replacementPin{handle: &callbackReplacementHandle{}}
		lease := &Lease{
			resources:       []claimHandle{unowned, owned},
			replacementPins: map[database.StoreID]*replacementPin{"global/auth": owned},
		}
		if err := lease.retireReplacementPinsLocked(
			map[database.StoreID]struct{}{"global/auth": {}},
		); err != nil {
			t.Fatal(err)
		}
		if len(lease.resources) != 1 || lease.resources[0] != unowned {
			t.Fatalf("unowned replacement resource = %#v", lease.resources)
		}
	})

	t.Run("ownership drift", func(t *testing.T) {
		shared := &replacementPin{handle: &callbackReplacementHandle{}}
		trigger := &replacementPin{}
		lease := &Lease{
			resources: []claimHandle{shared, trigger},
			replacementPins: map[database.StoreID]*replacementPin{
				"global/auth": shared, "launcher/auth": trigger,
			},
		}
		trigger.handle = &callbackReplacementHandle{onClose: func() {
			lease.replacementPins["global/auth"] = trigger
		}}
		ids := map[database.StoreID]struct{}{"global/auth": {}, "launcher/auth": {}}
		if err := lease.retireReplacementPinsLocked(ids); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("replacement retirement ownership drift = %v", err)
		}
	})

	t.Run("missing resources sort", func(t *testing.T) {
		shared := &replacementPin{handle: &callbackReplacementHandle{}}
		other := &replacementPin{handle: &callbackReplacementHandle{}}
		lease := &Lease{replacementPins: map[database.StoreID]*replacementPin{
			"global/auth": shared, "launcher/auth": shared, "runtime/sessions": other,
		}}
		ids := map[database.StoreID]struct{}{
			"global/auth": {}, "launcher/auth": {}, "runtime/sessions": {},
		}
		if err := lease.retireReplacementPinsLocked(ids); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("replacement retirement missing resources = %v", err)
		}
		if len(lease.replacementPins) != 0 {
			t.Fatalf("replacement retirement retained %d pins", len(lease.replacementPins))
		}
	})
}

func TestMigrationRefreshingGuardRetiresPinsInReverseWithoutSkippingFaults(t *testing.T) {
	var order []string
	closeCanary := errors.New("replacement close canary")
	first := &replacementPin{handle: &orderedReplacementHandle{orderedCloseHandle{
		name: "first", order: &order, err: closeCanary,
	}}}
	second := &replacementPin{handle: &orderedReplacementHandle{orderedCloseHandle{
		name: "second", order: &order,
	}}}
	lease := &Lease{
		resources: []claimHandle{first, second},
		replacementPins: map[database.StoreID]*replacementPin{
			"global/auth": first, "launcher/auth": second,
		},
	}
	ids := map[database.StoreID]struct{}{"global/auth": {}, "launcher/auth": {}}
	if err := lease.retireReplacementPinsLocked(ids); !errors.Is(err, closeCanary) {
		t.Fatalf("reverse retirement error = %v", err)
	}
	if !slices.Equal(order, []string{"second", "first"}) ||
		len(lease.replacementPins) != 0 || len(lease.resources) != 0 {
		t.Fatalf(
			"reverse retirement order=%#v pins=%d resources=%d",
			order, len(lease.replacementPins), len(lease.resources),
		)
	}

	order = nil
	valid := &replacementPin{handle: &orderedReplacementHandle{orderedCloseHandle{
		name: "valid", order: &order,
	}}}
	missingResource := &replacementPin{handle: &orderedReplacementHandle{orderedCloseHandle{
		name: "missing-resource", order: &order,
	}}}
	lease = &Lease{
		resources: []claimHandle{valid},
		replacementPins: map[database.StoreID]*replacementPin{
			"global/auth": valid, "launcher/auth": missingResource,
		},
	}
	ids = map[database.StoreID]struct{}{
		"global/auth": {}, "launcher/auth": {}, "global/missing": {},
	}
	if err := lease.retireReplacementPinsLocked(ids); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("corrupt retirement ledger = %v", err)
	}
	if !slices.Equal(order, []string{"valid", "missing-resource"}) ||
		len(lease.replacementPins) != 0 || len(lease.resources) != 0 {
		t.Fatalf(
			"corrupt retirement cleanup order=%#v pins=%d resources=%d",
			order, len(lease.replacementPins), len(lease.resources),
		)
	}
}
