// Package databasereadiness classifies trusted catalog generations without
// initializing stores or applying application schema changes.
package databasereadiness

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/internal/databaseadapter"
	"github.com/sipeed/picoclaw/internal/databaseclaims"
	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	inspectionTimeout         = 5 * time.Second
	legacyDiscoveryMaxEntries = 65_536
	legacyDiscoveryMaxFiles   = 32_768
	legacyDiscoveryMaxDepth   = 64
	legacyDiscoveryReadBatch  = 256
	legacyPathMaxBytes        = 16 << 10
	legacyComponentMaxBytes   = 255
)

// Snapshot retains pools for generations classified ready. A later owner may
// adopt each exact pool by StoreID. Close releases every retained pool that
// was not adopted.
type Snapshot struct {
	mu             sync.Mutex
	statuses       []database.StoreStatus
	inspections    []sqliteprovider.Inspection
	owned          []sqliteprovider.Inspection
	inspectionByID map[database.StoreID]int
	lease          *databaseclaims.Lease
	release        func(sqliteprovider.Inspection) error
	closed         bool
	err            error
}

type readinessProbeOps struct {
	guardStores    func(*databaseclaims.Lease) ([]storecatalog.Spec, func() error, func(), error)
	preflight      func(*databaseclaims.Lease) []storecatalog.Spec
	exclusions     func([]storecatalog.Spec) (generationSet, error)
	newBudget      func(legacyDiscoveryLimits) (*legacyDiscoveryBudget, error)
	inspect        func(context.Context, string, time.Duration, func() error) (sqliteprovider.Inspection, error)
	infrastructure func(error) bool
	revalidate     func(context.Context, sqliteprovider.Inspection) error
	validateDomain func(
		context.Context,
		database.StoreID,
		string,
		databaseadapter.ValidateFunc,
		sqliteprovider.Inspection,
	) error
	classify func(
		context.Context,
		storecatalog.Spec,
		databaseadapter.Contract,
		sqliteprovider.Inspection,
		error,
		generationSet,
		*legacyDiscoveryBudget,
	) database.StoreStatus
	release  func(sqliteprovider.Inspection) error
	validate func([]database.StoreStatus) ([]database.StoreStatus, error)
	timeout  time.Duration
}

func defaultReadinessProbeOps() readinessProbeOps {
	return readinessProbeOps{
		guardStores: func(
			lease *databaseclaims.Lease,
		) ([]storecatalog.Spec, func() error, func(), error) {
			return lease.GuardStoresRefreshing()
		},
		preflight:      func(lease *databaseclaims.Lease) []storecatalog.Spec { return lease.Stores() },
		exclusions:     generationExclusions,
		newBudget:      newLegacyDiscoveryBudget,
		inspect:        sqliteprovider.InspectClaimed,
		infrastructure: sqliteprovider.IsInspectionInfrastructure,
		revalidate: func(ctx context.Context, inspection sqliteprovider.Inspection) error {
			return inspection.Revalidate(ctx)
		},
		validateDomain: func(
			ctx context.Context,
			id database.StoreID,
			domain string,
			validate databaseadapter.ValidateFunc,
			inspection sqliteprovider.Inspection,
		) error {
			return inspection.ValidateDomain(ctx, id, domain, validate)
		},
		classify: func(
			ctx context.Context,
			spec storecatalog.Spec,
			contract databaseadapter.Contract,
			inspection sqliteprovider.Inspection,
			inspectErr error,
			exclusions generationSet,
			budget *legacyDiscoveryBudget,
		) database.StoreStatus {
			return classifyInspection(ctx, spec, contract, inspection, inspectErr, exclusions, budget)
		},
		release:  func(inspection sqliteprovider.Inspection) error { return inspection.Release() },
		validate: database.ValidateStoreStatuses,
		timeout:  inspectionTimeout,
	}
}

// Adopt transfers the exact ready inspection for id to a trusted owner while
// holding the originating claim lease and fence live across the handoff.
func (snapshot *Snapshot) Adopt(id database.StoreID) (*sql.DB, error) {
	if snapshot == nil || snapshot.lease == nil || !id.Valid() {
		return nil, database.NewError(database.CodeInvalid, "database readiness adoption is invalid")
	}
	release, err := snapshot.lease.Guard()
	if err != nil {
		return nil, err
	}
	defer release()
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	index, ok := snapshot.inspectionByID[id]
	if !ok || index < 0 || index >= len(snapshot.inspections) {
		return nil, database.NewError(database.CodeUnavailable, "database readiness inspection is unavailable")
	}
	database, err := snapshot.inspections[index].Adopt()
	if err == nil {
		delete(snapshot.inspectionByID, id)
	}
	return database, err
}

// Statuses returns a detached, ID-sorted readiness snapshot.
func (snapshot *Snapshot) Statuses() []database.StoreStatus {
	if snapshot == nil || snapshot.lease == nil {
		return nil
	}
	release, err := snapshot.lease.Guard()
	if err != nil {
		return nil
	}
	defer release()
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	statuses, err := database.ValidateStoreStatuses(snapshot.statuses)
	if err != nil {
		return nil
	}
	return statuses
}

// Close releases inspection pools not adopted by a typed store owner. It is
// safe to call more than once.
func (snapshot *Snapshot) Close() error {
	if snapshot == nil {
		return nil
	}
	snapshot.mu.Lock()
	if snapshot.closed {
		err := snapshot.err
		snapshot.mu.Unlock()
		return err
	}
	lease := snapshot.lease
	if lease == nil {
		hasInspections := len(snapshot.inspections) != 0 || len(snapshot.owned) != 0
		snapshot.mu.Unlock()
		if hasInspections {
			return snapshot.closeAfterGuardFailure(database.NewError(
				database.CodeIntegrity,
				"database readiness cleanup lacks claim authority",
			))
		}
		return snapshot.closeAfterGuardFailure(nil)
	}
	snapshot.mu.Unlock()
	releaseGuard, err := lease.Guard()
	if err != nil {
		// Once authority is lost it cannot be reacquired. Best-effort close avoids
		// retaining live pools forever; the joined guard error still tells callers
		// the close happened outside the normal WAL-safe ownership boundary.
		return snapshot.closeAfterGuardFailure(err)
	}
	defer releaseGuard()
	return snapshot.closeUnderHeldGuard()
}

// closeUnderHeldGuard is used only while the caller already owns this
// snapshot's claim/fence guard. It prevents Probe's failure cleanup from
// recursively acquiring a lease guard while GuardStores is live.
func (snapshot *Snapshot) closeUnderHeldGuard() error {
	return snapshot.closeInspections(nil)
}

func (snapshot *Snapshot) closeAfterGuardFailure(guardErr error) error {
	return snapshot.closeInspections(guardErr)
}

func (snapshot *Snapshot) closeInspections(prior error) error {
	if snapshot == nil {
		return prior
	}
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	if snapshot.closed {
		return snapshot.err
	}
	snapshot.err = errors.Join(snapshot.err, prior)
	release := snapshot.release
	if release == nil {
		release = func(inspection sqliteprovider.Inspection) error { return inspection.Release() }
	}
	for _, inspection := range snapshot.inspections {
		snapshot.err = errors.Join(snapshot.err, release(inspection))
	}
	for _, inspection := range snapshot.owned {
		snapshot.err = errors.Join(snapshot.err, release(inspection))
	}
	snapshot.inspections = nil
	snapshot.owned = nil
	snapshot.inspectionByID = nil
	snapshot.closed = true
	return snapshot.err
}

// Probe inspects every store in an already-claimed catalog. Probe neither
// builds a public catalog nor initializes a missing/empty generation.
func Probe(
	ctx context.Context,
	lease *databaseclaims.Lease,
	registry *databaseadapter.Registry,
) (*Snapshot, error) {
	return probeWithOps(ctx, lease, registry, defaultReadinessProbeOps())
}

func probeWithOps(
	ctx context.Context,
	lease *databaseclaims.Lease,
	registry *databaseadapter.Registry,
	ops readinessProbeOps,
) (result *Snapshot, resultErr error) {
	if lease == nil || registry == nil {
		return nil, database.NewError(
			database.CodeInvalid,
			"database readiness boundary is invalid",
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ops.guardStores == nil || ops.preflight == nil || ops.exclusions == nil || ops.newBudget == nil ||
		ops.inspect == nil || ops.infrastructure == nil || ops.revalidate == nil ||
		ops.validateDomain == nil ||
		ops.classify == nil ||
		ops.release == nil || ops.validate == nil ||
		ops.timeout <= 0 {
		return nil, database.NewError(
			database.CodeIntegrity,
			"database readiness operations are unavailable",
		)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	preflight := ops.preflight(lease)
	if len(preflight) == 0 {
		return nil, database.NewError(
			database.CodeIntegrity,
			"database readiness catalog is empty",
		)
	}
	// Registry completeness is checked before refreshing claims or reading
	// generation metadata. Partial registries cannot touch provider state.
	for _, spec := range preflight {
		if _, registered := registry.Lookup(spec.Domain); !registered {
			return nil, database.NewError(
				database.CodeUnsupported,
				"database readiness adapter catalog is incomplete",
			)
		}
	}
	specs, reconcileLease, releaseLease, err := ops.guardStores(lease)
	if err != nil {
		return nil, database.NewError(database.CodeIntegrity, "database readiness claim authority is unavailable")
	}
	if releaseLease != nil {
		defer releaseLease()
	}
	if reconcileLease == nil || releaseLease == nil {
		return nil, database.NewError(
			database.CodeIntegrity,
			"database readiness claim guard release is unavailable",
		)
	}
	if !sameReadinessSpecs(preflight, specs) {
		return nil, database.NewError(
			database.CodeIntegrity,
			"database readiness guarded catalog changed",
		)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}

	snapshot := &Snapshot{
		statuses:       make([]database.StoreStatus, 0, len(specs)),
		inspections:    make([]sqliteprovider.Inspection, 0, len(specs)),
		owned:          make([]sqliteprovider.Inspection, 0, len(specs)),
		inspectionByID: make(map[database.StoreID]int, len(specs)),
		lease:          lease,
		release:        ops.release,
	}
	complete := false
	defer func() {
		if !complete {
			resultErr = errors.Join(resultErr, reconcileLease())
			resultErr = errors.Join(resultErr, snapshot.closeUnderHeldGuard())
		}
	}()

	type storeObservation struct {
		spec       storecatalog.Spec
		adapter    databaseadapter.Adapter
		inspection sqliteprovider.Inspection
		err        error
	}
	observations := make([]storeObservation, 0, len(specs))
	for _, spec := range specs {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		adapter, _ := registry.Lookup(spec.Domain)

		storeCtx, cancelStore := context.WithTimeout(ctx, ops.timeout)
		inspection, inspectErr := ops.inspect(storeCtx, spec.Path, ops.timeout, reconcileLease)
		snapshot.owned = append(snapshot.owned, inspection)
		reconcileErr := reconcileLease()
		var infrastructureErr error
		if ops.infrastructure(inspectErr) {
			infrastructureErr = errors.Join(
				database.NewError(
					database.CodeUnavailable,
					"database readiness provider cleanup failed",
				),
				inspectErr,
			)
		}
		if reconcileErr != nil {
			cancelStore()
			return nil, errors.Join(
				reconcileErr,
				infrastructureErr,
			)
		}
		if contextErr := ctx.Err(); contextErr != nil {
			cancelStore()
			return nil, errors.Join(
				contextErr,
				infrastructureErr,
			)
		}
		if infrastructureErr != nil {
			cancelStore()
			return nil, infrastructureErr
		}
		if timeoutErr := storeCtx.Err(); timeoutErr != nil {
			inspectErr = timeoutErr
		} else if inspectErr == nil {
			revalidateErr := ops.revalidate(storeCtx, inspection)
			if revalidateErr != nil {
				if ops.infrastructure(revalidateErr) {
					cancelStore()
					return nil, readinessInfrastructureError(revalidateErr)
				}
				inspectErr = revalidateErr
			}
		}
		cancelStore()
		observations = append(observations, storeObservation{
			spec: spec, adapter: adapter, inspection: inspection, err: inspectErr,
		})
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if reconcileErr := reconcileLease(); reconcileErr != nil {
		return nil, reconcileErr
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	for index := range observations {
		observation := &observations[index]
		if observation.err == nil {
			revalidateCtx, cancelRevalidate := context.WithTimeout(ctx, ops.timeout)
			revalidateErr := ops.revalidate(revalidateCtx, observation.inspection)
			cancelRevalidate()
			if revalidateErr != nil {
				if contextErr := ctx.Err(); contextErr != nil {
					return nil, contextErr
				}
				if ops.infrastructure(revalidateErr) {
					return nil, readinessInfrastructureError(revalidateErr)
				}
				observation.err = revalidateErr
			}
		}
	}
	for index := range observations {
		observation := &observations[index]
		if observation.err == nil &&
			(!observation.inspection.Exists || observation.inspection.Empty) {
			snapshot.statuses = append(snapshot.statuses, database.StoreStatus{ID: observation.spec.ID})
			continue
		}
		storeCtx, cancelStore := context.WithTimeout(ctx, ops.timeout)
		status := ops.classify(
			storeCtx,
			observation.spec,
			observation.adapter.Contract,
			observation.inspection,
			observation.err,
			generationSet{},
			nil,
		)
		var exactErr error
		if observation.err == nil && status.Readiness == database.StoreReady &&
			observation.inspection.Exists && !observation.inspection.Empty &&
			observation.inspection.Version == observation.adapter.Contract.CurrentVersion &&
			observation.adapter.Validate != nil {
			exactErr = ops.validateDomain(
				storeCtx,
				observation.spec.ID,
				observation.adapter.Domain,
				observation.adapter.Validate,
				observation.inspection,
			)
		}
		var exactInfrastructureErr error
		if ops.infrastructure(exactErr) {
			exactInfrastructureErr = readinessInfrastructureError(exactErr)
		}
		if reconcileErr := reconcileLease(); reconcileErr != nil {
			cancelStore()
			return nil, errors.Join(reconcileErr, exactInfrastructureErr)
		}
		if contextErr := ctx.Err(); contextErr != nil {
			cancelStore()
			return nil, errors.Join(contextErr, exactInfrastructureErr)
		}
		if exactInfrastructureErr != nil {
			cancelStore()
			return nil, exactInfrastructureErr
		}
		if timeoutErr := storeCtx.Err(); timeoutErr != nil {
			status = unavailableStatus(
				observation.spec.ID,
				database.CodeUnavailable,
				"database readiness classification timed out",
			)
		} else if exactErr != nil {
			status = readinessDomainValidationStatus(observation.spec.ID, exactErr)
		} else if observation.err == nil {
			if revalidateErr := ops.revalidate(storeCtx, observation.inspection); revalidateErr != nil {
				if ops.infrastructure(revalidateErr) {
					cancelStore()
					return nil, readinessInfrastructureError(revalidateErr)
				}
				observation.err = revalidateErr
				status = readinessProviderErrorStatus(observation.spec.ID, revalidateErr)
			}
		}
		cancelStore()
		snapshot.statuses = append(snapshot.statuses, status)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if reconcileErr := reconcileLease(); reconcileErr != nil {
		return nil, reconcileErr
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	for index := range observations {
		observation := &observations[index]
		if observation.err == nil {
			revalidateCtx, cancelRevalidate := context.WithTimeout(ctx, ops.timeout)
			revalidateErr := ops.revalidate(revalidateCtx, observation.inspection)
			cancelRevalidate()
			if revalidateErr != nil {
				if contextErr := ctx.Err(); contextErr != nil {
					return nil, contextErr
				}
				if ops.infrastructure(revalidateErr) {
					return nil, readinessInfrastructureError(revalidateErr)
				}
				observation.err = revalidateErr
				snapshot.statuses[index] = readinessProviderErrorStatus(
					observation.spec.ID,
					revalidateErr,
				)
			}
		}
	}
	exclusions, err := ops.exclusions(specs)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if err != nil {
		return nil, database.NewError(
			database.CodeIntegrity,
			"database readiness catalog is invalid",
		)
	}
	legacyBudget, err := ops.newBudget(defaultLegacyDiscoveryLimits())
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if err != nil {
		return nil, err
	}
	if legacyBudget == nil {
		return nil, database.NewError(
			database.CodeIntegrity,
			"database readiness discovery budget is unavailable",
		)
	}
	for index, observation := range observations {
		if observation.err != nil || observation.inspection.Exists && !observation.inspection.Empty {
			continue
		}
		storeCtx, cancelStore := context.WithTimeout(ctx, ops.timeout)
		status := ops.classify(
			storeCtx,
			observation.spec,
			observation.adapter.Contract,
			observation.inspection,
			nil,
			exclusions,
			legacyBudget,
		)
		contextErr := ctx.Err()
		timeoutErr := storeCtx.Err()
		cancelStore()
		if contextErr != nil {
			return nil, contextErr
		}
		if timeoutErr != nil {
			status = unavailableStatus(
				observation.spec.ID,
				database.CodeUnavailable,
				"database readiness final legacy discovery timed out",
			)
		}
		snapshot.statuses[index] = status
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	// Legacy discovery is the final filesystem observation used to derive
	// readiness. Revalidate every still-successful provider observation once
	// more without opening SQLite or reconciling claims. Any drift aborts the
	// whole probe so failure cleanup can reconcile the newly observed member
	// before releasing retained pools; publishing a per-store status here would
	// otherwise leave a late generation unclaimed.
	for index := range observations {
		observation := &observations[index]
		if observation.err != nil {
			continue
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		revalidateCtx, cancelRevalidate := context.WithTimeout(ctx, ops.timeout)
		revalidateErr := ops.revalidate(revalidateCtx, observation.inspection)
		boundedErr := revalidateCtx.Err()
		parentErr := ctx.Err()
		cancelRevalidate()
		if parentErr != nil {
			if ops.infrastructure(revalidateErr) {
				return nil, errors.Join(parentErr, readinessInfrastructureError(revalidateErr))
			}
			return nil, parentErr
		}
		if revalidateErr == nil {
			revalidateErr = boundedErr
		}
		if revalidateErr == nil {
			continue
		}
		if ops.infrastructure(revalidateErr) {
			return nil, readinessInfrastructureError(revalidateErr)
		}
		return nil, readinessFinalRevalidationError(revalidateErr)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	validated, err := ops.validate(snapshot.statuses)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if err != nil {
		return nil, err
	}
	if len(validated) != len(observations) {
		return nil, database.NewError(
			database.CodeIntegrity,
			"database readiness validation returned an invalid status count",
		)
	}
	for index := range validated {
		if validated[index].ID != observations[index].spec.ID {
			return nil, database.NewError(
				database.CodeIntegrity,
				"database readiness validation changed status identity or order",
			)
		}
	}
	snapshot.statuses = validated
	owned := snapshot.owned
	snapshot.owned = nil
	for index, observation := range observations {
		status := validated[index]
		if status.Readiness != database.StoreReady {
			snapshot.owned = owned[index+1:]
			releaseErr := ops.release(owned[index])
			if releaseErr != nil {
				return nil, errors.Join(
					database.NewError(
						database.CodeUnavailable,
						"database readiness pool could not be closed",
					),
					releaseErr,
				)
			}
			continue
		}
		if observation.inspection.Exists {
			snapshot.inspections = append(snapshot.inspections, owned[index])
			snapshot.inspectionByID[observation.spec.ID] = len(snapshot.inspections) - 1
		}
		snapshot.owned = owned[index+1:]
	}
	snapshot.owned = nil
	complete = true
	return snapshot, nil
}

func sameReadinessSpecs(left, right []storecatalog.Spec) bool {
	return reflect.DeepEqual(left, right)
}

func readinessInfrastructureError(err error) error {
	return errors.Join(
		database.NewError(database.CodeUnavailable, "database readiness provider cleanup failed"),
		err,
	)
}

func readinessProviderErrorStatus(id database.StoreID, err error) database.StoreStatus {
	if sqliteprovider.IsInspectionIntegrity(err) {
		return unavailableStatus(id, database.CodeIntegrity, "database store integrity check failed")
	}
	if sqliteprovider.IsBusyOrLocked(err) {
		return unavailableStatus(id, database.CodeUnavailable, "database store is locked")
	}
	return unavailableStatus(id, database.CodeUnavailable, "database store is unavailable")
}

func readinessDomainValidationStatus(id database.StoreID, err error) database.StoreStatus {
	if sqliteprovider.IsInspectionIntegrity(err) {
		return unavailableStatus(
			id,
			database.CodeIntegrity,
			"database exact domain validation failed",
		)
	}
	if sqliteprovider.IsBusyOrLocked(err) {
		return unavailableStatus(
			id,
			database.CodeUnavailable,
			"database exact domain validation is locked",
		)
	}
	return unavailableStatus(
		id,
		database.CodeUnavailable,
		"database exact domain validation is unavailable",
	)
}

func readinessFinalRevalidationError(err error) error {
	if sqliteprovider.IsInspectionIntegrity(err) {
		return database.NewError(
			database.CodeIntegrity,
			"database readiness final generation validation failed",
		)
	}
	return database.NewError(
		database.CodeUnavailable,
		"database readiness final generation validation is unavailable",
	)
}

func classifyInspection(
	ctx context.Context,
	spec storecatalog.Spec,
	contract databaseadapter.Contract,
	inspection sqliteprovider.Inspection,
	inspectErr error,
	exclusions generationSet,
	budgets ...*legacyDiscoveryBudget,
) database.StoreStatus {
	if inspectErr != nil {
		if sqliteprovider.IsInspectionIntegrity(inspectErr) {
			return unavailableStatus(
				spec.ID,
				database.CodeIntegrity,
				"database store integrity check failed",
			)
		}
		if sqliteprovider.IsBusyOrLocked(inspectErr) {
			return unavailableStatus(
				spec.ID,
				database.CodeUnavailable,
				"database store is locked",
			)
		}
		return unavailableStatus(
			spec.ID,
			database.CodeUnavailable,
			"database store is unavailable",
		)
	}

	if !inspection.Exists || inspection.Empty {
		return classifyEmptyInspection(ctx, spec, contract, inspection, exclusions, budgets...)
	}

	switch {
	case inspection.Version < contract.CurrentVersion:
		return migrationStatus(spec.ID)
	case inspection.Version > contract.CurrentVersion:
		return unavailableStatus(
			spec.ID,
			database.CodeUnsupported,
			"database schema is newer than supported",
		)
	}

	for _, object := range contract.RequiredObjects {
		objectsReady, err := inspection.HasSchemaObjects(ctx, object.Type, object.Name)
		if err != nil {
			return unavailableStatus(
				spec.ID,
				database.CodeUnavailable,
				"database schema readiness is unavailable",
			)
		}
		if !objectsReady {
			return migrationStatus(spec.ID)
		}
	}
	for _, columns := range contract.RequiredColumns {
		ready, columnErr := inspection.HasTableColumns(ctx, columns.Table, columns.Columns...)
		if columnErr != nil {
			return unavailableStatus(
				spec.ID,
				database.CodeUnavailable,
				"database schema readiness is unavailable",
			)
		}
		if !ready {
			return migrationStatus(spec.ID)
		}
	}
	if contract.ImportHorizon != "" {
		closed, horizonErr := inspection.HasImportHorizon(ctx, contract.ImportHorizon)
		if horizonErr != nil {
			return importHorizonErrorStatus(spec.ID, horizonErr)
		}
		if !closed {
			return migrationStatus(spec.ID)
		}
	}
	return database.StoreStatus{ID: spec.ID, Readiness: database.StoreReady}
}

func classifyEmptyInspection(
	ctx context.Context,
	spec storecatalog.Spec,
	contract databaseadapter.Contract,
	_ sqliteprovider.Inspection,
	exclusions generationSet,
	budgets ...*legacyDiscoveryBudget,
) database.StoreStatus {
	var legacy bool
	var err error
	if len(budgets) == 1 && budgets[0] != nil {
		legacy, err = legacyInputExistsWithBudget(ctx, spec.LegacyRoots, exclusions, budgets[0])
	} else {
		legacy, err = legacyInputExists(ctx, spec.LegacyRoots, exclusions)
	}
	if err != nil {
		if errors.Is(err, errLegacyIntegrity) {
			return unavailableStatus(
				spec.ID,
				database.CodeIntegrity,
				"database legacy input is unsafe",
			)
		}
		return unavailableStatus(
			spec.ID,
			database.CodeUnavailable,
			"database legacy input is unavailable",
		)
	}
	if legacy || contract.EmptyPolicy == databaseadapter.EmptyMigrateOffline {
		return migrationStatus(spec.ID)
	}
	return database.StoreStatus{ID: spec.ID, Readiness: database.StoreReady}
}

func importHorizonErrorStatus(id database.StoreID, err error) database.StoreStatus {
	if sqliteprovider.IsInspectionIntegrity(err) {
		return unavailableStatus(
			id,
			database.CodeIntegrity,
			"database import readiness is invalid",
		)
	}
	return unavailableStatus(
		id,
		database.CodeUnavailable,
		"database import readiness is unavailable",
	)
}

func migrationStatus(id database.StoreID) database.StoreStatus {
	return database.StoreStatus{
		ID:        id,
		Readiness: database.StoreMigrationRequired,
		Error: database.NewError(
			database.CodeMigrationRequired,
			"database migration is required",
		),
	}
}

func unavailableStatus(
	id database.StoreID,
	code database.ErrorCode,
	message string,
) database.StoreStatus {
	readiness := database.StoreUnavailable
	if code == database.CodeIntegrity {
		readiness = database.StoreIntegrityFailed
	}
	return database.StoreStatus{
		ID: id, Readiness: readiness, Error: database.NewError(code, message),
	}
}

type generationSet struct {
	paths    map[string]struct{}
	physical map[fileidentity.Identity]struct{}
}

type generationExclusionOps struct {
	lstat    func(string) (os.FileInfo, error)
	identity func(string) (fileidentity.Identity, bool, error)
}

var errLegacyIntegrity = errors.New("database legacy input integrity failure")

type legacyDiscoveryLimits struct {
	maxEntries int
	maxFiles   int
	maxDepth   int
}

type legacyDiscoveryBudget struct {
	limits  legacyDiscoveryLimits
	entries int
	files   int
}

func defaultLegacyDiscoveryLimits() legacyDiscoveryLimits {
	return legacyDiscoveryLimits{
		maxEntries: legacyDiscoveryMaxEntries,
		maxFiles:   legacyDiscoveryMaxFiles,
		maxDepth:   legacyDiscoveryMaxDepth,
	}
}

func newLegacyDiscoveryBudget(limits legacyDiscoveryLimits) (*legacyDiscoveryBudget, error) {
	if limits.maxEntries < 1 || limits.maxFiles < 1 || limits.maxDepth < 0 {
		return nil, fmt.Errorf("%w: invalid discovery limits", errLegacyIntegrity)
	}
	return &legacyDiscoveryBudget{limits: limits}, nil
}

func (budget *legacyDiscoveryBudget) enter(depth int, regular bool) error {
	if budget == nil || depth < 0 {
		return fmt.Errorf("%w: invalid discovery budget", errLegacyIntegrity)
	}
	if depth > budget.limits.maxDepth {
		return fmt.Errorf("%w: discovery depth limit exceeded", errLegacyIntegrity)
	}
	if budget.entries >= budget.limits.maxEntries {
		return fmt.Errorf("%w: discovery entry limit exceeded", errLegacyIntegrity)
	}
	budget.entries++
	if regular {
		if budget.files >= budget.limits.maxFiles {
			return fmt.Errorf("%w: discovery file limit exceeded", errLegacyIntegrity)
		}
		budget.files++
	}
	return nil
}

func generationExclusions(specs []storecatalog.Spec) (generationSet, error) {
	return generationExclusionsWithOps(specs, generationExclusionOps{
		lstat: os.Lstat, identity: fileidentity.Existing,
	})
}

func generationExclusionsWithOps(
	specs []storecatalog.Spec,
	ops generationExclusionOps,
) (generationSet, error) {
	if ops.lstat == nil || ops.identity == nil {
		return generationSet{}, errors.New("database generation exclusion operations are unavailable")
	}
	set := generationSet{
		paths:    make(map[string]struct{}, len(specs)*4),
		physical: make(map[fileidentity.Identity]struct{}, len(specs)*4),
	}
	for _, spec := range specs {
		for _, path := range generationPaths(spec.Path) {
			clean, absoluteErr := filepath.Abs(filepath.Clean(path))
			if absoluteErr != nil {
				return generationSet{}, absoluteErr
			}
			set.paths[generationPathKey(clean)] = struct{}{}
			info, statErr := ops.lstat(clean)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return generationSet{}, statErr
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return generationSet{}, errors.New("database generation is unsafe")
			}
			identity, exists, err := ops.identity(clean)
			if err != nil {
				return generationSet{}, err
			}
			if !exists || !identity.Valid() {
				return generationSet{}, errors.New("database generation changed during identity lookup")
			}
			after, statErr := ops.lstat(clean)
			afterIdentity, afterExists, afterIdentityErr := ops.identity(clean)
			if statErr != nil || after == nil || !after.Mode().IsRegular() ||
				after.Mode()&os.ModeSymlink != 0 {
				return generationSet{}, errors.Join(
					errors.New("database generation changed during identity lookup"),
					statErr,
					afterIdentityErr,
				)
			}
			if afterIdentityErr != nil || !afterExists || !afterIdentity.Valid() ||
				afterIdentity != identity {
				return generationSet{}, errors.Join(
					errors.New("database generation changed during identity lookup"),
					afterIdentityErr,
				)
			}
			set.physical[identity] = struct{}{}
		}
	}
	return set, nil
}

func legacyInputExists(
	ctx context.Context,
	roots []string,
	exclusions generationSet,
) (bool, error) {
	return legacyInputExistsWithin(ctx, roots, exclusions, defaultLegacyDiscoveryLimits())
}

func legacyInputExistsWithin(
	ctx context.Context,
	roots []string,
	exclusions generationSet,
	limits legacyDiscoveryLimits,
) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	budget, err := newLegacyDiscoveryBudget(limits)
	if err != nil {
		return false, err
	}
	return legacyInputExistsWithBudget(ctx, roots, exclusions, budget)
}

func legacyInputExistsWithBudget(
	ctx context.Context,
	roots []string,
	exclusions generationSet,
	budget *legacyDiscoveryBudget,
) (found bool, resultErr error) {
	if budget == nil {
		return false, fmt.Errorf("%w: invalid discovery budget", errLegacyIntegrity)
	}
	found = false
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !validLegacyPath(root) {
			return false, fmt.Errorf("%w: invalid path", errLegacyIntegrity)
		}
		pinned, exists, err := openPinnedLegacyPath(root)
		if !exists && err == nil {
			continue
		}
		if err != nil {
			return false, err
		}
		if pinned.objectType == fileidentity.ObjectTypeRegular {
			if err := budget.enter(0, true); err != nil {
				return false, errors.Join(err, pinned.Close())
			}
			match, matchErr := excludedPinnedLegacyFile(pinned, exclusions)
			closeErr := pinned.Close()
			if matchErr != nil {
				return false, errors.Join(matchErr, closeErr)
			}
			if closeErr != nil {
				return false, closeErr
			}
			if !match {
				found = true
			}
			continue
		}
		if pinned.objectType != fileidentity.ObjectTypeDirectory {
			return false, errors.Join(
				fmt.Errorf("%w: non-regular input", errLegacyIntegrity), pinned.Close(),
			)
		}
		rootFound, walkErr := walkPinnedLegacyDirectory(ctx, pinned, 0, exclusions, budget)
		if walkErr != nil {
			return false, walkErr
		}
		found = found || rootFound
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return found, nil
}

func walkLegacyDirectory(
	ctx context.Context,
	path string,
	depth int,
	exclusions generationSet,
	budget *legacyDiscoveryBudget,
) (found bool, resultErr error) {
	directory, exists, err := openPinnedLegacyPath(path)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, fmt.Errorf("%w: legacy directory disappeared", errLegacyIntegrity)
	}
	return walkPinnedLegacyDirectory(ctx, directory, depth, exclusions, budget)
}

func walkPinnedLegacyDirectory(
	ctx context.Context,
	directory *pinnedLegacyFile,
	depth int,
	exclusions generationSet,
	budget *legacyDiscoveryBudget,
) (found bool, resultErr error) {
	if err := ctx.Err(); err != nil {
		return false, errors.Join(err, directory.Close())
	}
	if directory == nil || directory.file == nil ||
		directory.objectType != fileidentity.ObjectTypeDirectory {
		return false, errors.Join(
			fmt.Errorf("%w: unsafe directory", errLegacyIntegrity), directory.Close(),
		)
	}
	if err := budget.enter(depth, false); err != nil {
		return false, errors.Join(err, directory.Close())
	}
	defer func() {
		resultErr = errors.Join(resultErr, directory.Close())
	}()

	entries, err := readLegacyDirectory(ctx, directory.file, budget.limits.maxEntries-budget.entries)
	if err != nil {
		return false, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	found = false
	for _, entry := range entries {
		if contextErr := ctx.Err(); contextErr != nil {
			return false, contextErr
		}
		if !validLegacyComponent(entry.Name()) {
			return false, fmt.Errorf("%w: invalid path component", errLegacyIntegrity)
		}
		child, childErr := openPinnedLegacyChild(directory, entry.Name())
		if childErr != nil {
			return false, childErr
		}
		if child.objectType == fileidentity.ObjectTypeDirectory {
			if depth == 0 && (entry.Name() == "legacy-json" ||
				entry.Name() == "backups" || entry.Name() == ".database") {
				if budgetErr := budget.enter(depth+1, false); budgetErr != nil {
					return false, errors.Join(budgetErr, child.Close())
				}
				if closeErr := child.Close(); closeErr != nil {
					return false, closeErr
				}
				continue
			}
			childFound, walkErr := walkPinnedLegacyDirectory(
				ctx, child, depth+1, exclusions, budget,
			)
			if walkErr != nil {
				return false, walkErr
			}
			found = found || childFound
			continue
		}
		if child.objectType != fileidentity.ObjectTypeRegular {
			return false, errors.Join(
				fmt.Errorf("%w: tree non-regular input", errLegacyIntegrity), child.Close(),
			)
		}
		if budgetErr := budget.enter(depth+1, true); budgetErr != nil {
			return false, errors.Join(budgetErr, child.Close())
		}
		match, matchErr := excludedPinnedLegacyFile(child, exclusions)
		closeErr := child.Close()
		if matchErr != nil {
			return false, errors.Join(matchErr, closeErr)
		}
		if closeErr != nil {
			return false, closeErr
		}
		found = found || !match
	}
	if revalidateErr := directory.revalidateNamed(); revalidateErr != nil {
		return false, revalidateErr
	}
	after, err := directory.file.Stat()
	if err != nil || after == nil || !directory.info.ModTime().Equal(after.ModTime()) {
		return false, errors.Join(
			fmt.Errorf("%w: directory changed during discovery", errLegacyIntegrity),
			err,
		)
	}
	return found, nil
}

func readLegacyDirectory(
	ctx context.Context,
	directory *os.File,
	remaining int,
) ([]os.DirEntry, error) {
	if directory == nil || remaining < 0 {
		return nil, fmt.Errorf("%w: invalid directory budget", errLegacyIntegrity)
	}
	entries := make([]os.DirEntry, 0, min(remaining, legacyDiscoveryReadBatch))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		limit := min(legacyDiscoveryReadBatch, remaining-len(entries)+1)
		if limit < 1 {
			return nil, fmt.Errorf("%w: discovery entry limit exceeded", errLegacyIntegrity)
		}
		batch, err := directory.ReadDir(limit)
		entries = append(entries, batch...)
		if len(entries) > remaining {
			return nil, fmt.Errorf("%w: discovery entry limit exceeded", errLegacyIntegrity)
		}
		if len(batch) == 0 && err == nil {
			return nil, errors.New("database legacy directory read made no progress")
		}
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func excludedLegacyFile(path string, exclusions generationSet) (result bool, resultErr error) {
	pinned, exists, err := openPinnedLegacyPath(path)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, fmt.Errorf("%w: legacy file disappeared", errLegacyIntegrity)
	}
	defer func() { resultErr = errors.Join(resultErr, pinned.Close()) }()
	return excludedPinnedLegacyFile(pinned, exclusions)
}

func excludedPinnedLegacyFile(file *pinnedLegacyFile, exclusions generationSet) (bool, error) {
	if file == nil || file.file == nil || file.objectType != fileidentity.ObjectTypeRegular {
		return false, fmt.Errorf("%w: unsafe legacy file", errLegacyIntegrity)
	}
	absolute, absoluteErr := filepath.Abs(filepath.Clean(file.path))
	if absoluteErr != nil {
		return false, absoluteErr
	}
	if _, exact := exclusions.paths[generationPathKey(absolute)]; exact {
		return true, nil
	}
	if err := file.revalidateNamed(); err != nil {
		return false, err
	}
	if _, aliasesGeneration := exclusions.physical[file.identity]; aliasesGeneration {
		return false, fmt.Errorf("%w: generation alias", errLegacyIntegrity)
	}
	if err := file.revalidateNamed(); err != nil {
		return false, err
	}
	return false, nil
}

func validLegacyPath(path string) bool {
	if path == "" || len(path) > legacyPathMaxBytes || !utf8.ValidString(path) ||
		strings.ContainsRune(path, 0) {
		return false
	}
	clean := filepath.Clean(path)
	for _, component := range strings.Split(clean, string(os.PathSeparator)) {
		if component == "" || component == "." || component == filepath.VolumeName(clean) {
			continue
		}
		if !validLegacyComponent(component) {
			return false
		}
	}
	return true
}

func validLegacyComponent(component string) bool {
	return component != "" && component != "." && component != ".." &&
		len(component) <= legacyComponentMaxBytes && utf8.ValidString(component) &&
		!strings.ContainsRune(component, 0) &&
		!strings.ContainsRune(component, os.PathSeparator)
}

func generationPaths(path string) []string {
	return []string{path, path + "-wal", path + "-shm", path + "-journal"}
}

func generationPathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}
