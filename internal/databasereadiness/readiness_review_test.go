package databasereadiness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseadapter"
	"github.com/sipeed/picoclaw/internal/databaseclaims"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestProbeBoundsEveryStorePhaseAndPreservesParentDeadline(t *testing.T) {
	home := t.TempDir()
	lease, _ := acquireReadinessLease(t, home)
	specs := lease.Stores()
	if len(specs) == 0 {
		t.Fatal("claimed catalog is empty")
	}
	spec := specs[0]
	registry := readinessRegistry(t, []storecatalog.Spec{spec}, "", databaseadapter.Contract{})
	base := func() readinessProbeOps {
		ops := defaultReadinessProbeOps()
		ops.preflight = func(*databaseclaims.Lease) []storecatalog.Spec {
			return []storecatalog.Spec{spec}
		}
		ops.timeout = 5 * time.Millisecond
		ops.guardStores = func(
			*databaseclaims.Lease,
		) ([]storecatalog.Spec, func() error, func(), error) {
			return []storecatalog.Spec{spec}, func() error { return nil }, func() {}, nil
		}
		ops.exclusions = func([]storecatalog.Spec) (generationSet, error) {
			return generationSet{}, nil
		}
		ops.revalidate = func(context.Context, sqliteprovider.Inspection) error { return nil }
		return ops
	}

	for _, test := range []struct {
		name   string
		mutate func(*readinessProbeOps, *int)
	}{
		{
			name: "provider inspection",
			mutate: func(ops *readinessProbeOps, _ *int) {
				ops.inspect = func(
					ctx context.Context,
					_ string,
					_ time.Duration,
					_ func() error,
				) (sqliteprovider.Inspection, error) {
					<-ctx.Done()
					return sqliteprovider.Inspection{}, ctx.Err()
				}
			},
		},
		{
			name: "classification and legacy",
			mutate: func(ops *readinessProbeOps, classifications *int) {
				ops.inspect = func(
					context.Context,
					string,
					time.Duration,
					func() error,
				) (sqliteprovider.Inspection, error) {
					return sqliteprovider.Inspection{Exists: true}, nil
				}
				ops.classify = func(
					ctx context.Context,
					_ storecatalog.Spec,
					_ databaseadapter.Contract,
					_ sqliteprovider.Inspection,
					_ error,
					_ generationSet,
					_ *legacyDiscoveryBudget,
				) database.StoreStatus {
					*classifications++
					<-ctx.Done()
					return database.StoreStatus{ID: spec.ID, Readiness: database.StoreReady}
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := base()
			releases := 0
			classifications := 0
			test.mutate(&ops, &classifications)
			ops.release = func(sqliteprovider.Inspection) error {
				releases++
				return nil
			}
			started := time.Now()
			snapshot, err := probeWithOps(t.Context(), lease, registry, ops)
			if err != nil || snapshot == nil || time.Since(started) > time.Second {
				t.Fatalf("bounded Probe = %#v, %v after %s", snapshot, err, time.Since(started))
			}
			statuses := snapshot.Statuses()
			if len(statuses) != 1 || statuses[0].Readiness != database.StoreUnavailable ||
				database.CodeOf(statuses[0].Error) != database.CodeUnavailable || releases != 1 {
				t.Fatalf("timeout statuses = %#v; releases=%d", statuses, releases)
			}
			if err := snapshot.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}

	ops := base()
	ops.timeout = time.Second
	releases := 0
	ops.inspect = func(
		ctx context.Context,
		_ string,
		_ time.Duration,
		_ func() error,
	) (sqliteprovider.Inspection, error) {
		<-ctx.Done()
		return sqliteprovider.Inspection{}, ctx.Err()
	}
	ops.release = func(sqliteprovider.Inspection) error {
		releases++
		return nil
	}
	parent, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if snapshot, err := probeWithOps(parent, lease, registry, ops); snapshot != nil ||
		!errors.Is(err, context.DeadlineExceeded) || releases != 1 {
		t.Fatalf("parent deadline = %#v, %v; releases=%d", snapshot, err, releases)
	}
}

func TestProbeClassifiesLegacyMaterializedBeforeFinalDiscovery(t *testing.T) {
	for _, directoryRoot := range []bool{false, true} {
		name := "missing root"
		if directoryRoot {
			name = "child in empty root"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			lease, _ := acquireReadinessLease(t, home)
			spec := lease.Stores()[0]
			legacyRoot := filepath.Join(t.TempDir(), "legacy")
			materialized := legacyRoot
			if directoryRoot {
				if err := os.Mkdir(legacyRoot, 0o700); err != nil {
					t.Fatal(err)
				}
				materialized = filepath.Join(legacyRoot, "late.json")
			}
			spec.LegacyRoots = []string{legacyRoot}
			registry := readinessRegistry(t, []storecatalog.Spec{spec}, "", databaseadapter.Contract{})
			reconciles := 0
			ops := defaultReadinessProbeOps()
			ops.preflight = func(*databaseclaims.Lease) []storecatalog.Spec {
				return []storecatalog.Spec{spec}
			}
			ops.guardStores = func(
				*databaseclaims.Lease,
			) ([]storecatalog.Spec, func() error, func(), error) {
				reconcile := func() error {
					reconciles++
					if reconciles == 3 {
						return os.WriteFile(materialized, []byte("late"), 0o600)
					}
					return nil
				}
				return []storecatalog.Spec{spec}, reconcile, func() {}, nil
			}
			ops.inspect = func(
				context.Context,
				string,
				time.Duration,
				func() error,
			) (sqliteprovider.Inspection, error) {
				return sqliteprovider.Inspection{}, nil
			}
			ops.revalidate = func(context.Context, sqliteprovider.Inspection) error { return nil }
			ops.exclusions = func([]storecatalog.Spec) (generationSet, error) {
				return generationSet{}, nil
			}
			ops.release = func(sqliteprovider.Inspection) error { return nil }
			snapshot, err := probeWithOps(t.Context(), lease, registry, ops)
			if err != nil || snapshot == nil {
				t.Fatalf("late legacy appearance = %#v, %v", snapshot, err)
			}
			t.Cleanup(func() { _ = snapshot.Close() })
			statuses := snapshot.Statuses()
			if len(statuses) != 1 || statuses[0].Readiness != database.StoreMigrationRequired {
				t.Fatalf("late legacy status = %#v", statuses)
			}
		})
	}
}

func TestSnapshotCloseCleansAfterLostOrMissingLeaseAndJoinsErrors(t *testing.T) {
	cleanupErr := errors.New("snapshot cleanup canary")
	for _, test := range []struct {
		name     string
		snapshot func(*testing.T) *Snapshot
	}{
		{
			name: "lost lease",
			snapshot: func(t *testing.T) *Snapshot {
				home := t.TempDir()
				lease, fence := acquireReadinessLease(t, home)
				if err := fence.Close(); err != nil {
					t.Fatal(err)
				}
				return &Snapshot{lease: lease, inspections: []sqliteprovider.Inspection{{Exists: true}}}
			},
		},
		{
			name: "missing lease",
			snapshot: func(*testing.T) *Snapshot {
				return &Snapshot{inspections: []sqliteprovider.Inspection{{Exists: true}}}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := test.snapshot(t)
			releases := 0
			snapshot.release = func(sqliteprovider.Inspection) error {
				releases++
				return cleanupErr
			}
			first := snapshot.Close()
			if database.CodeOf(first) != database.CodeIntegrity || !errors.Is(first, cleanupErr) ||
				releases != 1 {
				t.Fatalf("first Close = %v; releases=%d", first, releases)
			}
			second := snapshot.Close()
			if !errors.Is(second, cleanupErr) || releases != 1 {
				t.Fatalf("idempotent Close = %v; releases=%d", second, releases)
			}
		})
	}
}

func TestProbeJoinsPrimaryAndRetainedCleanupFailures(t *testing.T) {
	home := t.TempDir()
	lease, _ := acquireReadinessLease(t, home)
	spec := lease.Stores()[0]
	registry := readinessRegistry(t, []storecatalog.Spec{spec}, "", databaseadapter.Contract{})
	primaryErr := errors.New("status validation canary")
	cleanupErr := errors.New("retained cleanup canary")
	ops := defaultReadinessProbeOps()
	ops.preflight = func(*databaseclaims.Lease) []storecatalog.Spec {
		return []storecatalog.Spec{spec}
	}
	ops.guardStores = func(
		*databaseclaims.Lease,
	) ([]storecatalog.Spec, func() error, func(), error) {
		return []storecatalog.Spec{spec}, func() error { return nil }, func() {}, nil
	}
	ops.exclusions = func([]storecatalog.Spec) (generationSet, error) { return generationSet{}, nil }
	ops.inspect = func(
		context.Context,
		string,
		time.Duration,
		func() error,
	) (sqliteprovider.Inspection, error) {
		return sqliteprovider.Inspection{Exists: true}, nil
	}
	ops.revalidate = func(context.Context, sqliteprovider.Inspection) error { return nil }
	ops.classify = func(
		context.Context,
		storecatalog.Spec,
		databaseadapter.Contract,
		sqliteprovider.Inspection,
		error,
		generationSet,
		*legacyDiscoveryBudget,
	) database.StoreStatus {
		return database.StoreStatus{ID: spec.ID, Readiness: database.StoreReady}
	}
	releases := 0
	ops.release = func(sqliteprovider.Inspection) error {
		releases++
		return cleanupErr
	}
	ops.validate = func([]database.StoreStatus) ([]database.StoreStatus, error) {
		return nil, primaryErr
	}
	if snapshot, err := probeWithOps(t.Context(), lease, registry, ops); snapshot != nil ||
		!errors.Is(err, primaryErr) || !errors.Is(err, cleanupErr) || releases != 1 {
		t.Fatalf("Probe cleanup join = %#v, %v; releases=%d", snapshot, err, releases)
	}
}

func TestImportHorizonOrdinaryQueryFailureIsUnavailable(t *testing.T) {
	status := importHorizonErrorStatus("global/auth", errors.New("query I/O canary"))
	if status.Readiness != database.StoreUnavailable ||
		database.CodeOf(status.Error) != database.CodeUnavailable {
		t.Fatalf("ordinary horizon query failure = %#v", status)
	}
}

func TestProbeReconcilesBeforeCleanupOnClassificationCancelAndPanic(t *testing.T) {
	for _, panicDuringClassify := range []bool{false, true} {
		name := "cancel"
		if panicDuringClassify {
			name = "panic"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			lease, _ := acquireReadinessLease(t, home)
			spec := lease.Stores()[0]
			registry := readinessRegistry(t, []storecatalog.Spec{spec}, "", databaseadapter.Contract{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events := make([]string, 0, 6)
			classifying := false
			ops := defaultReadinessProbeOps()
			ops.preflight = func(*databaseclaims.Lease) []storecatalog.Spec {
				return []storecatalog.Spec{spec}
			}
			ops.guardStores = func(
				*databaseclaims.Lease,
			) ([]storecatalog.Spec, func() error, func(), error) {
				reconcile := func() error {
					if classifying {
						events = append(events, "reconcile")
					}
					return nil
				}
				release := func() { events = append(events, "guard-release") }
				return []storecatalog.Spec{spec}, reconcile, release, nil
			}
			ops.inspect = func(
				context.Context,
				string,
				time.Duration,
				func() error,
			) (sqliteprovider.Inspection, error) {
				return sqliteprovider.Inspection{Exists: true}, nil
			}
			ops.revalidate = func(context.Context, sqliteprovider.Inspection) error { return nil }
			ops.exclusions = func([]storecatalog.Spec) (generationSet, error) {
				return generationSet{}, nil
			}
			ops.classify = func(
				context.Context,
				storecatalog.Spec,
				databaseadapter.Contract,
				sqliteprovider.Inspection,
				error,
				generationSet,
				*legacyDiscoveryBudget,
			) database.StoreStatus {
				classifying = true
				events = append(events, "classify")
				if panicDuringClassify {
					panic("classification canary")
				}
				cancel()
				return database.StoreStatus{ID: spec.ID, Readiness: database.StoreReady}
			}
			ops.release = func(sqliteprovider.Inspection) error {
				events = append(events, "release")
				return nil
			}

			if panicDuringClassify {
				func() {
					defer func() {
						if recover() == nil {
							t.Fatal("classification panic was not propagated")
						}
					}()
					_, _ = probeWithOps(ctx, lease, registry, ops)
				}()
			} else if snapshot, err := probeWithOps(ctx, lease, registry, ops); snapshot != nil ||
				!errors.Is(err, context.Canceled) {
				t.Fatalf("canceled classification Probe = %#v, %v", snapshot, err)
			}
			if len(events) < 4 || events[0] != "classify" || events[1] != "reconcile" ||
				events[len(events)-2] != "release" || events[len(events)-1] != "guard-release" {
				t.Fatalf("classification cleanup events = %#v", events)
			}
		})
	}
}
