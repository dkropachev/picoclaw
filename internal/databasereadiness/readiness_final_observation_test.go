package databasereadiness

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseadapter"
	"github.com/sipeed/picoclaw/internal/databaseclaims"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestProbeFinalObservationRejectsMissingGenerationMaterializedLate(t *testing.T) {
	for _, phase := range []string{"exclusions", "legacy"} {
		t.Run(phase, func(t *testing.T) {
			home := t.TempDir()
			lease, _ := acquireReadinessLease(t, home)
			var spec storecatalog.Spec
			for _, candidate := range lease.Stores() {
				if candidate.ID == "global/auth" {
					spec = candidate
					break
				}
			}
			if !spec.ID.Valid() {
				t.Fatal("global/auth store is absent")
			}
			registry := readinessRegistry(t, []storecatalog.Spec{spec}, "", databaseadapter.Contract{})

			ops := defaultReadinessProbeOps()
			ops.preflight = func(*databaseclaims.Lease) []storecatalog.Spec {
				return []storecatalog.Spec{spec}
			}
			events := make([]string, 0, 7)
			reconciles := 0
			guardReleases := 0
			guardStores := ops.guardStores
			ops.guardStores = func(
				lease *databaseclaims.Lease,
			) ([]storecatalog.Spec, func() error, func(), error) {
				_, realReconcile, realRelease, err := guardStores(lease)
				if err != nil {
					return nil, nil, nil, err
				}
				reconcile := func() error {
					reconciles++
					err := realReconcile()
					if _, statErr := os.Lstat(spec.Path); statErr == nil {
						events = append(events, "reconcile-materialized")
					} else {
						events = append(events, "reconcile")
					}
					return err
				}
				release := func() {
					guardReleases++
					events = append(events, "guard-release")
					realRelease()
				}
				return []storecatalog.Spec{spec}, reconcile, release, nil
			}

			inspect := ops.inspect
			inspectCalls := 0
			ops.inspect = func(
				ctx context.Context,
				path string,
				timeout time.Duration,
				reconcile func() error,
			) (sqliteprovider.Inspection, error) {
				inspectCalls++
				return inspect(ctx, path, timeout, reconcile)
			}
			revalidate := ops.revalidate
			revalidateCalls := 0
			ops.revalidate = func(ctx context.Context, inspection sqliteprovider.Inspection) error {
				revalidateCalls++
				return revalidate(ctx, inspection)
			}
			materialize := func(event string) {
				writeReadinessFile(t, spec.Path, nil)
				events = append(events, event)
			}
			if phase == "exclusions" {
				exclusions := ops.exclusions
				ops.exclusions = func(specs []storecatalog.Spec) (generationSet, error) {
					set, err := exclusions(specs)
					if err == nil {
						materialize("materialize-exclusions")
					}
					return set, err
				}
			} else {
				classify := ops.classify
				ops.classify = func(
					ctx context.Context,
					spec storecatalog.Spec,
					contract databaseadapter.Contract,
					inspection sqliteprovider.Inspection,
					inspectErr error,
					exclusions generationSet,
					budget *legacyDiscoveryBudget,
				) database.StoreStatus {
					status := classify(ctx, spec, contract, inspection, inspectErr, exclusions, budget)
					materialize("materialize-legacy")
					return status
				}
			}

			releaseInspection := ops.release
			inspectionReleases := 0
			ops.release = func(inspection sqliteprovider.Inspection) error {
				inspectionReleases++
				events = append(events, "inspection-release")
				return releaseInspection(inspection)
			}
			validateCalls := 0
			validate := ops.validate
			ops.validate = func(statuses []database.StoreStatus) ([]database.StoreStatus, error) {
				validateCalls++
				return validate(statuses)
			}

			snapshot, err := probeWithOps(t.Context(), lease, registry, ops)
			if snapshot != nil || database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("late materialization Probe = %#v, %v", snapshot, err)
			}
			wantEvents := []string{
				"reconcile", "reconcile", "reconcile", "materialize-" + phase,
				"reconcile-materialized", "inspection-release", "guard-release",
			}
			if inspectCalls != 1 || revalidateCalls != 4 || validateCalls != 0 ||
				reconciles != 4 || inspectionReleases != 1 || guardReleases != 1 ||
				!reflect.DeepEqual(events, wantEvents) {
				t.Fatalf(
					"late materialization lifecycle: inspect=%d revalidate=%d validate=%d "+
						"reconcile=%d inspection-release=%d guard-release=%d events=%#v",
					inspectCalls, revalidateCalls, validateCalls, reconciles,
					inspectionReleases, guardReleases, events,
				)
			}
		})
	}
}

func TestProbeChecksParentCancellationAfterExclusionsAndLegacy(t *testing.T) {
	for _, phase := range []string{"exclusions", "legacy"} {
		t.Run(phase, func(t *testing.T) {
			home := t.TempDir()
			lease, _ := acquireReadinessLease(t, home)
			spec := lease.Stores()[0]
			registry := readinessRegistry(t, []storecatalog.Spec{spec}, "", databaseadapter.Contract{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			ops := defaultReadinessProbeOps()
			ops.preflight = func(*databaseclaims.Lease) []storecatalog.Spec {
				return []storecatalog.Spec{spec}
			}
			events := make([]string, 0, 7)
			reconciles := 0
			guardReleases := 0
			ops.guardStores = func(
				*databaseclaims.Lease,
			) ([]storecatalog.Spec, func() error, func(), error) {
				return []storecatalog.Spec{spec}, func() error {
						reconciles++
						events = append(events, "reconcile")
						return nil
					}, func() {
						guardReleases++
						events = append(events, "guard-release")
					}, nil
			}
			inspection := sqliteprovider.Inspection{}
			if phase == "exclusions" {
				inspection.Exists = true
			}
			ops.inspect = func(
				context.Context, string, time.Duration, func() error,
			) (sqliteprovider.Inspection, error) {
				return inspection, nil
			}
			revalidateCalls := 0
			ops.revalidate = func(context.Context, sqliteprovider.Inspection) error {
				revalidateCalls++
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
				if phase == "legacy" {
					cancel()
					events = append(events, "cancel-legacy")
				}
				return database.StoreStatus{ID: spec.ID, Readiness: database.StoreReady}
			}
			ops.exclusions = func([]storecatalog.Spec) (generationSet, error) {
				if phase == "exclusions" {
					cancel()
					events = append(events, "cancel-exclusions")
				}
				return generationSet{}, nil
			}
			inspectionReleases := 0
			ops.release = func(sqliteprovider.Inspection) error {
				inspectionReleases++
				events = append(events, "inspection-release")
				return nil
			}
			validateCalls := 0
			ops.validate = func([]database.StoreStatus) ([]database.StoreStatus, error) {
				validateCalls++
				return nil, errors.New("validation must not run after cancellation")
			}

			snapshot, err := probeWithOps(ctx, lease, registry, ops)
			if snapshot != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("post-%s cancellation Probe = %#v, %v", phase, snapshot, err)
			}
			wantRevalidations := 4
			if phase == "legacy" {
				wantRevalidations = 3
			}
			wantReconciles := 4
			cancelIndex := 3
			if phase == "exclusions" {
				wantReconciles = 5
				cancelIndex = 4
			}
			if revalidateCalls != wantRevalidations || validateCalls != 0 ||
				reconciles != wantReconciles || inspectionReleases != 1 || guardReleases != 1 ||
				len(events) != cancelIndex+4 || events[cancelIndex] != "cancel-"+phase ||
				events[cancelIndex+1] != "reconcile" ||
				events[cancelIndex+2] != "inspection-release" ||
				events[cancelIndex+3] != "guard-release" {
				t.Fatalf(
					"post-%s cancellation lifecycle: revalidate=%d validate=%d reconcile=%d "+
						"inspection-release=%d guard-release=%d events=%#v",
					phase, revalidateCalls, validateCalls, reconciles,
					inspectionReleases, guardReleases, events,
				)
			}
		})
	}
}

func TestProbeFinalRevalidationIsBounded(t *testing.T) {
	home := t.TempDir()
	lease, _ := acquireReadinessLease(t, home)
	spec := lease.Stores()[0]
	registry := readinessRegistry(t, []storecatalog.Spec{spec}, "", databaseadapter.Contract{})
	ops := defaultReadinessProbeOps()
	ops.timeout = 5 * time.Millisecond
	ops.preflight = func(*databaseclaims.Lease) []storecatalog.Spec { return []storecatalog.Spec{spec} }
	events := make([]string, 0, 2)
	ops.guardStores = func(
		*databaseclaims.Lease,
	) ([]storecatalog.Spec, func() error, func(), error) {
		return []storecatalog.Spec{spec}, func() error { return nil }, func() {
			events = append(events, "guard-release")
		}, nil
	}
	ops.inspect = func(
		context.Context, string, time.Duration, func() error,
	) (sqliteprovider.Inspection, error) {
		return sqliteprovider.Inspection{Exists: true}, nil
	}
	calls := 0
	ops.revalidate = func(ctx context.Context, _ sqliteprovider.Inspection) error {
		calls++
		if calls == 5 {
			<-ctx.Done()
			return ctx.Err()
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
	ops.exclusions = func([]storecatalog.Spec) (generationSet, error) { return generationSet{}, nil }
	ops.release = func(sqliteprovider.Inspection) error {
		events = append(events, "inspection-release")
		return nil
	}

	started := time.Now()
	snapshot, err := probeWithOps(t.Context(), lease, registry, ops)
	if snapshot != nil || database.CodeOf(err) != database.CodeUnavailable ||
		time.Since(started) > time.Second || calls != 5 ||
		!reflect.DeepEqual(events, []string{"inspection-release", "guard-release"}) {
		t.Fatalf(
			"bounded final revalidation = %#v, %v after %s; calls=%d events=%#v",
			snapshot, err, time.Since(started), calls, events,
		)
	}
}

func TestProbeFinalBoundariesPrioritizeParentCancellation(t *testing.T) {
	canary := errors.New("final boundary infrastructure canary")
	for _, test := range []struct {
		name           string
		cancelBudget   bool
		cancelValidate bool
		cancelFinal    bool
		infrastructure bool
		wantCalls      int
	}{
		{name: "after budget", cancelBudget: true, wantCalls: 4},
		{name: "after validation", cancelValidate: true, wantCalls: 5},
		{name: "during final revalidation", cancelFinal: true, wantCalls: 5},
		{
			name: "during final infrastructure failure", cancelFinal: true,
			infrastructure: true, wantCalls: 5,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			lease, _ := acquireReadinessLease(t, home)
			spec := lease.Stores()[0]
			registry := readinessRegistry(t, []storecatalog.Spec{spec}, "", databaseadapter.Contract{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			ops := defaultReadinessProbeOps()
			ops.preflight = func(*databaseclaims.Lease) []storecatalog.Spec {
				return []storecatalog.Spec{spec}
			}
			events := make([]string, 0, 8)
			reconciles := 0
			ops.guardStores = func(
				*databaseclaims.Lease,
			) ([]storecatalog.Spec, func() error, func(), error) {
				return []storecatalog.Spec{spec}, func() error {
						reconciles++
						events = append(events, "reconcile")
						return nil
					}, func() {
						events = append(events, "guard-release")
					}, nil
			}
			ops.inspect = func(
				context.Context, string, time.Duration, func() error,
			) (sqliteprovider.Inspection, error) {
				return sqliteprovider.Inspection{Exists: true}, nil
			}
			revalidateCalls := 0
			ops.revalidate = func(context.Context, sqliteprovider.Inspection) error {
				revalidateCalls++
				if test.cancelFinal && revalidateCalls == 5 {
					cancel()
					events = append(events, "cancel-final")
					return canary
				}
				return nil
			}
			ops.infrastructure = func(err error) bool {
				return test.infrastructure && errors.Is(err, canary)
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
			newBudget := ops.newBudget
			ops.newBudget = func(limits legacyDiscoveryLimits) (*legacyDiscoveryBudget, error) {
				budget, err := newBudget(limits)
				if test.cancelBudget {
					cancel()
					events = append(events, "cancel-budget")
				}
				return budget, err
			}
			validate := ops.validate
			ops.validate = func(statuses []database.StoreStatus) ([]database.StoreStatus, error) {
				validated, err := validate(statuses)
				if test.cancelValidate {
					cancel()
					events = append(events, "cancel-validation")
				}
				return validated, err
			}
			inspectionReleases := 0
			ops.release = func(sqliteprovider.Inspection) error {
				inspectionReleases++
				events = append(events, "inspection-release")
				return nil
			}

			snapshot, err := probeWithOps(ctx, lease, registry, ops)
			if snapshot != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled final boundary = %#v, %v", snapshot, err)
			}
			if test.infrastructure {
				if !errors.Is(err, canary) || database.CodeOf(err) != database.CodeUnavailable {
					t.Fatalf("joined final infrastructure failure = %v", err)
				}
			} else if errors.Is(err, canary) {
				t.Fatalf("ordinary final error displaced parent cancellation: %v", err)
			}
			if revalidateCalls != test.wantCalls || reconciles != 5 || inspectionReleases != 1 ||
				len(events) < 3 || events[len(events)-3] != "reconcile" ||
				events[len(events)-2] != "inspection-release" ||
				events[len(events)-1] != "guard-release" {
				t.Fatalf(
					"canceled final lifecycle: revalidate=%d reconcile=%d release=%d events=%#v",
					revalidateCalls, reconciles, inspectionReleases, events,
				)
			}
		})
	}
}

func TestProbeFinalLegacyDiscoveryTimeoutBecomesUnavailable(t *testing.T) {
	home := t.TempDir()
	lease, _ := acquireReadinessLease(t, home)
	spec := lease.Stores()[0]
	registry := readinessRegistry(t, []storecatalog.Spec{spec}, "", databaseadapter.Contract{})
	ops := defaultReadinessProbeOps()
	ops.timeout = 5 * time.Millisecond
	ops.preflight = func(*databaseclaims.Lease) []storecatalog.Spec { return []storecatalog.Spec{spec} }
	ops.guardStores = func(
		*databaseclaims.Lease,
	) ([]storecatalog.Spec, func() error, func(), error) {
		return []storecatalog.Spec{spec}, func() error { return nil }, func() {}, nil
	}
	ops.inspect = func(
		context.Context, string, time.Duration, func() error,
	) (sqliteprovider.Inspection, error) {
		return sqliteprovider.Inspection{}, nil
	}
	ops.revalidate = func(context.Context, sqliteprovider.Inspection) error { return nil }
	ops.classify = func(
		ctx context.Context,
		_ storecatalog.Spec,
		_ databaseadapter.Contract,
		_ sqliteprovider.Inspection,
		_ error,
		_ generationSet,
		_ *legacyDiscoveryBudget,
	) database.StoreStatus {
		<-ctx.Done()
		return database.StoreStatus{ID: spec.ID, Readiness: database.StoreReady}
	}
	ops.exclusions = func([]storecatalog.Spec) (generationSet, error) { return generationSet{}, nil }
	releases := 0
	ops.release = func(sqliteprovider.Inspection) error { releases++; return nil }

	started := time.Now()
	snapshot, err := probeWithOps(t.Context(), lease, registry, ops)
	if err != nil || snapshot == nil || time.Since(started) > time.Second {
		t.Fatalf("bounded legacy discovery = %#v, %v after %s", snapshot, err, time.Since(started))
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	statuses := snapshot.Statuses()
	if len(statuses) != 1 || statuses[0].Readiness != database.StoreUnavailable ||
		database.CodeOf(statuses[0].Error) != database.CodeUnavailable || releases != 1 {
		t.Fatalf("legacy timeout statuses = %#v; releases=%d", statuses, releases)
	}
}
