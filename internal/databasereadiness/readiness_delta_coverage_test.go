//nolint:maintidx // One table-driven lifecycle test deliberately covers ordered failure phases.
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
	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestProbeLifecycleFailurePhasesAreBoundedAndCleaned(t *testing.T) {
	home := t.TempDir()
	lease, _ := acquireReadinessLease(t, home)
	spec := lease.Stores()[0]
	registry := readinessRegistry(t, []storecatalog.Spec{spec}, "", databaseadapter.Contract{})
	canary := errors.New("probe delta canary")

	base := func() (readinessProbeOps, *int) {
		ops := defaultReadinessProbeOps()
		ops.preflight = func(*databaseclaims.Lease) []storecatalog.Spec {
			return []storecatalog.Spec{spec}
		}
		ops.guardStores = func(
			*databaseclaims.Lease,
		) ([]storecatalog.Spec, func() error, func(), error) {
			return []storecatalog.Spec{spec}, func() error { return nil }, func() {}, nil
		}
		ops.exclusions = func([]storecatalog.Spec) (generationSet, error) {
			return generationSet{}, nil
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
			return nil
		}
		return ops, &releases
	}

	for _, test := range []struct {
		name         string
		mutate       func(*readinessProbeOps)
		code         database.ErrorCode
		cause        error
		wantReleases int
		wantExisting bool
	}{
		{
			name: "invalid operations",
			mutate: func(ops *readinessProbeOps) {
				ops.inspect = nil
			},
			code: database.CodeIntegrity,
		},
		{
			name: "inspection infrastructure",
			mutate: func(ops *readinessProbeOps) {
				ops.inspect = func(
					context.Context,
					string,
					time.Duration,
					func() error,
				) (sqliteprovider.Inspection, error) {
					return sqliteprovider.Inspection{Exists: true}, canary
				}
				ops.infrastructure = func(error) bool { return true }
			},
			code: database.CodeUnavailable, wantReleases: 1, wantExisting: true,
		},
		{
			name: "first reconcile",
			mutate: func(ops *readinessProbeOps) {
				ops.guardStores = func(
					*databaseclaims.Lease,
				) ([]storecatalog.Spec, func() error, func(), error) {
					return []storecatalog.Spec{spec}, func() error { return canary }, func() {}, nil
				}
			},
			cause: canary, wantReleases: 1, wantExisting: true,
		},
		{
			name: "revalidation infrastructure",
			mutate: func(ops *readinessProbeOps) {
				ops.revalidate = func(context.Context, sqliteprovider.Inspection) error { return canary }
				ops.infrastructure = func(error) bool { return true }
			},
			code: database.CodeUnavailable, wantReleases: 1, wantExisting: true,
		},
		{
			name: "exclusions",
			mutate: func(ops *readinessProbeOps) {
				ops.exclusions = func([]storecatalog.Spec) (generationSet, error) {
					return generationSet{}, canary
				}
			},
			code: database.CodeIntegrity, wantReleases: 1, wantExisting: true,
		},
		{
			name: "final budget error",
			mutate: func(ops *readinessProbeOps) {
				ops.inspect = func(
					context.Context,
					string,
					time.Duration,
					func() error,
				) (sqliteprovider.Inspection, error) {
					return sqliteprovider.Inspection{}, nil
				}
				ops.newBudget = func(legacyDiscoveryLimits) (*legacyDiscoveryBudget, error) {
					return nil, canary
				}
			},
			cause: canary, wantReleases: 1,
		},
		{
			name: "nil final budget",
			mutate: func(ops *readinessProbeOps) {
				ops.inspect = func(
					context.Context,
					string,
					time.Duration,
					func() error,
				) (sqliteprovider.Inspection, error) {
					return sqliteprovider.Inspection{}, nil
				}
				ops.newBudget = func(legacyDiscoveryLimits) (*legacyDiscoveryBudget, error) {
					return nil, nil
				}
			},
			code: database.CodeIntegrity, wantReleases: 1,
		},
		{
			name: "status validation",
			mutate: func(ops *readinessProbeOps) {
				ops.validate = func([]database.StoreStatus) ([]database.StoreStatus, error) {
					return nil, canary
				}
			},
			cause: canary, wantReleases: 1, wantExisting: true,
		},
		{
			name: "status validation count",
			mutate: func(ops *readinessProbeOps) {
				ops.validate = func([]database.StoreStatus) ([]database.StoreStatus, error) {
					return nil, nil
				}
			},
			code: database.CodeIntegrity, wantReleases: 1, wantExisting: true,
		},
		{
			name: "status validation identity",
			mutate: func(ops *readinessProbeOps) {
				ops.validate = func([]database.StoreStatus) ([]database.StoreStatus, error) {
					return []database.StoreStatus{{
						ID: "global/auth", Readiness: database.StoreReady,
					}}, nil
				}
			},
			code: database.CodeIntegrity, wantReleases: 1, wantExisting: true,
		},
		{
			name: "nonready release",
			mutate: func(ops *readinessProbeOps) {
				ops.classify = func(
					context.Context,
					storecatalog.Spec,
					databaseadapter.Contract,
					sqliteprovider.Inspection,
					error,
					generationSet,
					*legacyDiscoveryBudget,
				) database.StoreStatus {
					return migrationStatus(spec.ID)
				}
			},
			code: database.CodeUnavailable, cause: canary, wantReleases: 1, wantExisting: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops, releases := base()
			releasedExisting := false
			baseRelease := ops.release
			ops.release = func(inspection sqliteprovider.Inspection) error {
				releasedExisting = releasedExisting || inspection.Exists
				return baseRelease(inspection)
			}
			test.mutate(&ops)
			if test.name == "nonready release" {
				ops.release = func(inspection sqliteprovider.Inspection) error {
					*releases++
					releasedExisting = releasedExisting || inspection.Exists
					return canary
				}
			}
			snapshot, err := probeWithOps(t.Context(), lease, registry, ops)
			if snapshot != nil || err == nil {
				t.Fatalf("failure phase = %#v, %v", snapshot, err)
			}
			if test.code != "" && database.CodeOf(err) != test.code {
				t.Fatalf("failure code = %s, want %s (%v)", database.CodeOf(err), test.code, err)
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("failure lost cause: %v", err)
			}
			if *releases != test.wantReleases {
				t.Fatalf("inspection releases = %d, want %d", *releases, test.wantReleases)
			}
			if releasedExisting != test.wantExisting {
				t.Fatalf("released existing=%t, want %t", releasedExisting, test.wantExisting)
			}
		})
	}

	t.Run("missing guard callback releases guard", func(t *testing.T) {
		ops, releases := base()
		guardReleases := 0
		ops.guardStores = func(
			*databaseclaims.Lease,
		) ([]storecatalog.Spec, func() error, func(), error) {
			return []storecatalog.Spec{spec}, nil, func() { guardReleases++ }, nil
		}
		if snapshot, err := probeWithOps(t.Context(), lease, registry, ops); snapshot != nil ||
			database.CodeOf(err) != database.CodeIntegrity || guardReleases != 1 || *releases != 0 {
			t.Fatalf(
				"missing guard callback = %#v, %v; guard releases=%d inspection releases=%d",
				snapshot, err, guardReleases, *releases,
			)
		}
	})
}

func TestReadinessProviderStatusHelpersClassifyExactFailures(t *testing.T) {
	canary := errors.New("provider status canary")
	if err := readinessInfrastructureError(canary); !errors.Is(err, canary) ||
		database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("infrastructure error = %v", err)
	}
	for _, test := range []struct {
		name string
		err  error
		want database.StoreReadiness
		code database.ErrorCode
	}{
		{name: "ordinary", err: canary, want: database.StoreUnavailable, code: database.CodeUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			status := readinessProviderErrorStatus("global/auth", test.err)
			if status.Readiness != test.want || database.CodeOf(status.Error) != test.code {
				t.Fatalf("provider error status = %#v", status)
			}
		})
	}
	status := importHorizonErrorStatus("global/auth", context.DeadlineExceeded)
	if status.Readiness != database.StoreUnavailable ||
		database.CodeOf(status.Error) != database.CodeUnavailable {
		t.Fatalf("horizon unavailable status = %#v", status)
	}
	status = importHorizonErrorStatus("global/auth", errors.New("ordinary horizon error"))
	if status.Readiness != database.StoreUnavailable ||
		database.CodeOf(status.Error) != database.CodeUnavailable {
		t.Fatalf("ordinary horizon unavailable status = %#v", status)
	}
}

func TestProbeMapsRevalidationFailuresToStoreStatuses(t *testing.T) {
	home := t.TempDir()
	lease, _ := acquireReadinessLease(t, home)
	spec := lease.Stores()[0]
	registry := readinessRegistry(t, []storecatalog.Spec{spec}, "", databaseadapter.Contract{})
	for _, test := range []struct {
		name string
		err  error
		want database.StoreReadiness
		code database.ErrorCode
	}{
		{name: "ordinary", err: errors.New("revalidation unavailable"), want: database.StoreUnavailable, code: database.CodeUnavailable},
		{name: "context", err: context.DeadlineExceeded, want: database.StoreUnavailable, code: database.CodeUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultReadinessProbeOps()
			guardReleases := 0
			inspectionReleases := 0
			releasedExisting := false
			ops.preflight = func(*databaseclaims.Lease) []storecatalog.Spec {
				return []storecatalog.Spec{spec}
			}
			ops.guardStores = func(
				*databaseclaims.Lease,
			) ([]storecatalog.Spec, func() error, func(), error) {
				return []storecatalog.Spec{spec}, func() error { return nil }, func() {
					guardReleases++
				}, nil
			}
			ops.inspect = func(
				context.Context,
				string,
				time.Duration,
				func() error,
			) (sqliteprovider.Inspection, error) {
				return sqliteprovider.Inspection{Exists: true}, nil
			}
			calls := 0
			ops.revalidate = func(context.Context, sqliteprovider.Inspection) error {
				calls++
				if calls == 1 {
					return test.err
				}
				return nil
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
				return readinessProviderErrorStatus(spec.ID, test.err)
			}
			ops.exclusions = func([]storecatalog.Spec) (generationSet, error) {
				return generationSet{}, nil
			}
			ops.release = func(inspection sqliteprovider.Inspection) error {
				inspectionReleases++
				releasedExisting = releasedExisting || inspection.Exists
				if !inspection.Exists {
					return errors.New("released inspection identity is wrong")
				}
				return nil
			}
			snapshot, err := probeWithOps(t.Context(), lease, registry, ops)
			if err != nil || snapshot == nil {
				t.Fatalf("Probe = %#v, %v", snapshot, err)
			}
			t.Cleanup(func() { _ = snapshot.Close() })
			statuses := snapshot.Statuses()
			if len(statuses) != 1 || statuses[0].Readiness != test.want ||
				database.CodeOf(statuses[0].Error) != test.code {
				t.Fatalf("statuses = %#v", statuses)
			}
			if inspectionReleases != 1 || !releasedExisting || guardReleases != 1 {
				t.Fatalf(
					"status cleanup: inspection=%d existing=%t guard=%d",
					inspectionReleases, releasedExisting, guardReleases,
				)
			}
		})
	}
}

func TestSnapshotCleanupJoinsBothOwnedCollectionsAndPriorError(t *testing.T) {
	canary := errors.New("snapshot prior canary")
	releaseErr := errors.New("snapshot release canary")
	releases := 0
	snapshot := &Snapshot{
		inspections: []sqliteprovider.Inspection{{Exists: true}},
		owned:       []sqliteprovider.Inspection{{Exists: true}},
		release: func(sqliteprovider.Inspection) error {
			releases++
			return releaseErr
		},
	}
	err := snapshot.closeInspections(canary)
	if !errors.Is(err, canary) || !errors.Is(err, releaseErr) || releases != 2 {
		t.Fatalf("closeInspections = %v; releases=%d", err, releases)
	}
	if err := snapshot.closeInspections(errors.New("ignored")); !errors.Is(err, canary) || releases != 2 {
		t.Fatalf("repeat closeInspections = %v; releases=%d", err, releases)
	}
}

func TestReadinessLegacyExclusionAndCleanupBoundaries(t *testing.T) {
	canary := errors.New("nil snapshot canary")
	var absent *Snapshot
	if err := absent.closeInspections(canary); !errors.Is(err, canary) {
		t.Fatalf("nil closeInspections = %v", err)
	}
	cleanupPath := filepath.Join(t.TempDir(), "cleanup.db")
	databaseHandle, err := sqliteprovider.OpenStore(cleanupPath, 25*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if configureErr := sqliteprovider.Configure(
		t.Context(), databaseHandle, 25*time.Millisecond, false,
	); configureErr != nil {
		_ = databaseHandle.Close()
		t.Fatal(configureErr)
	}
	if closeErr := databaseHandle.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	inspection, err := sqliteprovider.Inspect(t.Context(), cleanupPath, 25*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inspection.Release() })
	if err := (&Snapshot{inspections: []sqliteprovider.Inspection{inspection}}).closeInspections(nil); err != nil {
		t.Fatalf("default inspection release = %v", err)
	}
	if err := inspection.Revalidate(t.Context()); err == nil {
		t.Fatal("default snapshot cleanup left inspection usable")
	}
	if _, err := generationExclusionsWithOps(nil, generationExclusionOps{}); err == nil {
		t.Fatal("nil generation exclusion operations succeeded")
	}
	root := t.TempDir()
	path := filepath.Join(root, "legacy.json")
	if err := os.WriteFile(path, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if excluded, err := excludedLegacyFile(filepath.Join(root, "missing"), generationSet{}); excluded ||
		!errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("missing excluded file = %t, %v", excluded, err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(path, alias); err == nil {
		if excluded, err := excludedLegacyFile(alias, generationSet{}); excluded ||
			!errors.Is(err, errLegacyIntegrity) {
			t.Fatalf("symlink excluded file = %t, %v", excluded, err)
		}
	}
	if excluded, err := excludedPinnedLegacyFile(nil, generationSet{}); excluded ||
		!errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("nil pinned exclusion = %t, %v", excluded, err)
	}

	pin := func(t *testing.T) *pinnedLegacyFile {
		t.Helper()
		pinned, exists, err := openPinnedLegacyPath(path)
		if err != nil || !exists {
			t.Fatalf("pin legacy = %#v, %t, %v", pinned, exists, err)
		}
		t.Cleanup(func() { _ = pinned.Close() })
		return pinned
	}
	t.Run("exact path", func(t *testing.T) {
		pinned := pin(t)
		if excluded, err := excludedPinnedLegacyFile(
			pinned,
			generationSet{paths: map[string]struct{}{generationPathKey(path): {}}},
		); err != nil || !excluded {
			t.Fatalf("exact pinned exclusion = %t, %v", excluded, err)
		}
	})
	t.Run("physical alias", func(t *testing.T) {
		pinned := pin(t)
		if excluded, err := excludedPinnedLegacyFile(
			pinned,
			generationSet{physical: map[fileidentity.Identity]struct{}{pinned.identity: {}}},
		); excluded || !errors.Is(err, errLegacyIntegrity) {
			t.Fatalf("physical pinned exclusion = %t, %v", excluded, err)
		}
	})
	t.Run("named path changed", func(t *testing.T) {
		pinned := pin(t)
		if err := os.Rename(path, path+".moved"); err != nil {
			t.Fatal(err)
		}
		if excluded, err := excludedPinnedLegacyFile(pinned, generationSet{}); excluded ||
			!errors.Is(err, errLegacyIntegrity) {
			t.Fatalf("changed pinned exclusion = %t, %v", excluded, err)
		}
	})
}

func TestReadinessProviderHelpersRecognizeRealCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(path, []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, corruption := sqliteprovider.Inspect(t.Context(), path, 25*time.Millisecond)
	if !sqliteprovider.IsInspectionIntegrity(corruption) {
		t.Fatalf("corrupt inspection = %v", corruption)
	}
	status := readinessProviderErrorStatus("global/auth", corruption)
	if status.Readiness != database.StoreIntegrityFailed ||
		database.CodeOf(status.Error) != database.CodeIntegrity {
		t.Fatalf("corrupt provider status = %#v", status)
	}
	status = importHorizonErrorStatus("global/auth", corruption)
	if status.Readiness != database.StoreIntegrityFailed ||
		database.CodeOf(status.Error) != database.CodeIntegrity {
		t.Fatalf("corrupt horizon status = %#v", status)
	}
}

func TestProbeReconcileFailuresAtEveryPostInspectionBoundary(t *testing.T) {
	home := t.TempDir()
	lease, _ := acquireReadinessLease(t, home)
	spec := lease.Stores()[0]
	registry := readinessRegistry(t, []storecatalog.Spec{spec}, "", databaseadapter.Contract{})
	canary := errors.New("reconcile phase canary")
	for _, failCall := range []int{2, 3, 4} {
		t.Run(database.StoreID(string(rune('0'+failCall))).String(), func(t *testing.T) {
			ops := defaultReadinessProbeOps()
			events := make([]string, 0, 8)
			guardReleases := 0
			inspectionReleases := 0
			releasedExisting := false
			ops.preflight = func(*databaseclaims.Lease) []storecatalog.Spec {
				return []storecatalog.Spec{spec}
			}
			calls := 0
			ops.guardStores = func(
				*databaseclaims.Lease,
			) ([]storecatalog.Spec, func() error, func(), error) {
				return []storecatalog.Spec{spec}, func() error {
						calls++
						events = append(events, "reconcile")
						if calls == failCall {
							return canary
						}
						return nil
					}, func() {
						guardReleases++
						events = append(events, "guard-release")
					}, nil
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
			ops.classify = func(
				_ context.Context,
				_ storecatalog.Spec,
				_ databaseadapter.Contract,
				_ sqliteprovider.Inspection,
				inspectErr error,
				_ generationSet,
				_ *legacyDiscoveryBudget,
			) database.StoreStatus {
				if inspectErr != nil {
					return readinessProviderErrorStatus(spec.ID, inspectErr)
				}
				return database.StoreStatus{ID: spec.ID, Readiness: database.StoreReady}
			}
			ops.exclusions = func([]storecatalog.Spec) (generationSet, error) {
				return generationSet{}, nil
			}
			ops.release = func(inspection sqliteprovider.Inspection) error {
				inspectionReleases++
				releasedExisting = releasedExisting || inspection.Exists
				events = append(events, "inspection-release")
				if !inspection.Exists {
					return errors.New("released inspection identity is wrong")
				}
				return nil
			}
			if snapshot, err := probeWithOps(t.Context(), lease, registry, ops); snapshot != nil ||
				!errors.Is(err, canary) {
				t.Fatalf("reconcile failure call %d = %#v, %v", failCall, snapshot, err)
			}
			if calls != failCall+1 || inspectionReleases != 1 || !releasedExisting ||
				guardReleases != 1 || len(events) < 3 ||
				events[len(events)-2] != "inspection-release" ||
				events[len(events)-1] != "guard-release" {
				t.Fatalf(
					"failure cleanup calls: reconcile=%d inspection=%d existing=%t guard=%d events=%#v",
					calls, inspectionReleases, releasedExisting, guardReleases, events,
				)
			}
		})
	}
}

func TestProbeRevalidationFailuresAcrossAllPasses(t *testing.T) {
	home := t.TempDir()
	lease, _ := acquireReadinessLease(t, home)
	spec := lease.Stores()[0]
	registry := readinessRegistry(t, []storecatalog.Spec{spec}, "", databaseadapter.Contract{})
	canary := errors.New("revalidation phase canary")
	for _, test := range []struct {
		name           string
		failCall       int
		infrastructure bool
		abort          bool
	}{
		{name: "initial status", failCall: 1},
		{name: "initial infrastructure", failCall: 1, infrastructure: true},
		{name: "batch status", failCall: 2},
		{name: "batch infrastructure", failCall: 2, infrastructure: true},
		{name: "classification status", failCall: 3},
		{name: "classification infrastructure", failCall: 3, infrastructure: true},
		{name: "final status", failCall: 4},
		{name: "final infrastructure", failCall: 4, infrastructure: true},
		{name: "post-legacy abort", failCall: 5, abort: true},
		{name: "post-legacy infrastructure", failCall: 5, infrastructure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultReadinessProbeOps()
			events := make([]string, 0, 2)
			guardReleases := 0
			inspectionReleases := 0
			releasedExisting := false
			ops.preflight = func(*databaseclaims.Lease) []storecatalog.Spec {
				return []storecatalog.Spec{spec}
			}
			ops.guardStores = func(
				*databaseclaims.Lease,
			) ([]storecatalog.Spec, func() error, func(), error) {
				return []storecatalog.Spec{spec}, func() error { return nil }, func() {
					guardReleases++
					events = append(events, "guard-release")
				}, nil
			}
			ops.inspect = func(
				context.Context,
				string,
				time.Duration,
				func() error,
			) (sqliteprovider.Inspection, error) {
				return sqliteprovider.Inspection{Exists: true}, nil
			}
			calls := 0
			ops.revalidate = func(context.Context, sqliteprovider.Inspection) error {
				calls++
				if calls == test.failCall {
					return canary
				}
				return nil
			}
			ops.infrastructure = func(err error) bool {
				return test.infrastructure && errors.Is(err, canary)
			}
			ops.classify = func(
				_ context.Context,
				_ storecatalog.Spec,
				_ databaseadapter.Contract,
				_ sqliteprovider.Inspection,
				inspectErr error,
				_ generationSet,
				_ *legacyDiscoveryBudget,
			) database.StoreStatus {
				if inspectErr != nil {
					return readinessProviderErrorStatus(spec.ID, inspectErr)
				}
				return database.StoreStatus{ID: spec.ID, Readiness: database.StoreReady}
			}
			ops.exclusions = func([]storecatalog.Spec) (generationSet, error) {
				return generationSet{}, nil
			}
			ops.release = func(inspection sqliteprovider.Inspection) error {
				inspectionReleases++
				releasedExisting = releasedExisting || inspection.Exists
				events = append(events, "inspection-release")
				if !inspection.Exists {
					return errors.New("released inspection identity is wrong")
				}
				return nil
			}
			snapshot, err := probeWithOps(t.Context(), lease, registry, ops)
			if test.infrastructure || test.abort {
				if snapshot != nil || database.CodeOf(err) != database.CodeUnavailable {
					t.Fatalf("aborted revalidation = %#v, %v", snapshot, err)
				}
				if inspectionReleases != 1 || !releasedExisting || guardReleases != 1 {
					t.Fatalf(
						"aborted cleanup: inspection=%d existing=%t guard=%d",
						inspectionReleases, releasedExisting, guardReleases,
					)
				}
				if calls != test.failCall || len(events) != 2 ||
					events[0] != "inspection-release" || events[1] != "guard-release" {
					t.Fatalf("aborted phase calls=%d events=%#v", calls, events)
				}
				return
			}
			if err != nil || snapshot == nil {
				t.Fatalf("status revalidation = %#v, %v", snapshot, err)
			}
			t.Cleanup(func() { _ = snapshot.Close() })
			status := snapshot.Statuses()[0]
			if status.Readiness != database.StoreUnavailable ||
				database.CodeOf(status.Error) != database.CodeUnavailable {
				t.Fatalf("revalidation status = %#v", status)
			}
			if inspectionReleases != 1 || !releasedExisting || guardReleases != 1 {
				t.Fatalf(
					"status cleanup: inspection=%d existing=%t guard=%d",
					inspectionReleases, releasedExisting, guardReleases,
				)
			}
			if calls != test.failCall || len(events) != 2 ||
				events[0] != "inspection-release" || events[1] != "guard-release" {
				t.Fatalf("status phase calls=%d events=%#v", calls, events)
			}
		})
	}
}

func TestProbeContextCancellationAtGuardedPhaseBoundaries(t *testing.T) {
	home := t.TempDir()
	lease, _ := acquireReadinessLease(t, home)
	spec := lease.Stores()[0]
	registry := readinessRegistry(t, []storecatalog.Spec{spec}, "", databaseadapter.Contract{})
	for _, test := range []struct {
		name          string
		cancelAtRecon int
		cancelAtCheck int
	}{
		{name: "after batch reconcile", cancelAtRecon: 2},
		{name: "after classification reconcile", cancelAtRecon: 3},
		{name: "after final reconcile", cancelAtRecon: 4},
		{name: "between stores", cancelAtCheck: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ops := defaultReadinessProbeOps()
			guardReleases := 0
			inspectionReleases := 0
			releasedExisting := false
			preflight := []storecatalog.Spec{spec}
			if test.cancelAtCheck != 0 {
				preflight = append(preflight, spec)
			}
			ops.preflight = func(*databaseclaims.Lease) []storecatalog.Spec { return preflight }
			reconciles := 0
			ops.guardStores = func(
				*databaseclaims.Lease,
			) ([]storecatalog.Spec, func() error, func(), error) {
				return preflight, func() error {
					reconciles++
					if reconciles == test.cancelAtRecon {
						cancel()
					}
					return nil
				}, func() { guardReleases++ }, nil
			}
			inspections := 0
			ops.inspect = func(
				context.Context,
				string,
				time.Duration,
				func() error,
			) (sqliteprovider.Inspection, error) {
				inspections++
				return sqliteprovider.Inspection{Exists: true}, nil
			}
			ops.revalidate = func(context.Context, sqliteprovider.Inspection) error {
				if test.cancelAtCheck != 0 && inspections == 1 {
					cancel()
				}
				return nil
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
				return database.StoreStatus{ID: spec.ID, Readiness: database.StoreReady}
			}
			ops.exclusions = func([]storecatalog.Spec) (generationSet, error) {
				return generationSet{}, nil
			}
			ops.release = func(inspection sqliteprovider.Inspection) error {
				inspectionReleases++
				releasedExisting = releasedExisting || inspection.Exists
				if !inspection.Exists {
					return errors.New("released inspection identity is wrong")
				}
				return nil
			}
			if snapshot, err := probeWithOps(ctx, lease, registry, ops); snapshot != nil ||
				!errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation phase = %#v, %v", snapshot, err)
			}
			if inspectionReleases != 1 || !releasedExisting || guardReleases != 1 {
				t.Fatalf(
					"cancellation cleanup: inspection=%d existing=%t guard=%d",
					inspectionReleases, releasedExisting, guardReleases,
				)
			}
		})
	}
}
