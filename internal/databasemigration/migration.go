// Package databasemigration coordinates exclusively fenced, backed-up,
// provider-owned offline database maintenance.
package databasemigration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseadapter"
	"github.com/sipeed/picoclaw/internal/databaseclaims"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	migrationBusyTimeout   = 5 * time.Second
	maximumMigrationStores = 256
)

var (
	// ErrStorageActive means another online owner or migrator holds storage.
	ErrStorageActive = errors.New("database storage is active")
	// ErrUnknownStore means a requested logical ID is absent from claimed catalog.
	ErrUnknownStore = errors.New("unknown database store ID")
	// ErrIntegrity means provider validation rejected a damaged generation.
	ErrIntegrity = errors.New("database integrity check failed")
	// ErrAdapterRequired means selected state needs a domain callback that was
	// not registered. Generic migration never invents application schemas.
	ErrAdapterRequired = errors.New("database domain migration adapter required")
	// ErrSchemaTooNew means a newer adapter wrote selected generation.
	ErrSchemaTooNew         = errors.New("database schema is newer than supported")
	errPostMigrationCleanup = errors.New("database migration committed but cleanup failed")
)

// Options controls one migration run. BackupDir is a parent; each run creates
// a new database-migrate-<UTC> child. Empty Stores selects full catalog.
type Options struct {
	Stores    []database.StoreID
	BackupDir string
	DryRun    bool
}

// StoreResult reports provider-neutral maintenance for one selected store.
type StoreResult struct {
	ID              database.StoreID `json:"id"`
	Exists          bool             `json:"exists"`
	BeforeVersion   int              `json:"before_version"`
	AfterVersion    int              `json:"after_version"`
	Migrated        bool             `json:"migrated"`
	AdapterRequired bool             `json:"adapter_required,omitempty"`
	cutover         bool
}

// Result reports mandatory backup identity and selected store outcomes.
type Result struct {
	DryRun    bool          `json:"dry_run"`
	BackupDir string        `json:"backup_dir,omitempty"`
	Stores    []StoreResult `json:"stores"`
}

// Engine binds exact catalog path-resolution context and explicit adapter set.
// It does not build or inspect catalog until Run holds migration fence.
type Engine struct {
	options  storecatalog.Options
	catalog  *storecatalog.Catalog
	registry *databaseadapter.Registry
	now      func() time.Time
	running  atomic.Bool
}

// migrationStoreOps keeps provider fault injection scoped to one private call.
// Production always supplies the exact sqliteprovider operations below; tests
// can exercise post-cutover outcome classification without mutable globals.
type migrationStoreOps struct {
	inspect func(context.Context, string, time.Duration) (sqliteprovider.Inspection, error)
	release func(sqliteprovider.Inspection) error
	migrate func(
		context.Context,
		string,
		time.Duration,
		int,
		sqliteprovider.StagedMigration,
		sqliteprovider.StagedValidation,
	) error
	maintain func(context.Context, string, time.Duration) (sqliteprovider.MaintenanceResult, error)
	pin      func(database.StoreID, string) error
}

// New projects and detaches the complete inventory from the supplied Config.
func New(options storecatalog.Options, registry *databaseadapter.Registry) (*Engine, error) {
	if options.Config == nil || registry == nil {
		return nil, database.NewError(
			database.CodeInvalid,
			"database migration engine options are invalid",
		)
	}
	projected, err := storecatalog.Project(options)
	if err != nil {
		return nil, err
	}
	options.Home = projected.Home()
	options.Config = nil
	return &Engine{
		options: options, catalog: projected, registry: registry, now: time.Now,
	}, nil
}

// Run acquires migration fence and full physical claims before catalog build,
// snapshots every selected generation/input, then performs provider recovery
// and optional domain migration. Failures never auto-restore from backup.
func (engine *Engine) Run(
	ctx context.Context,
	options Options,
) (result Result, returnErr error) {
	completed := false
	result.DryRun = options.DryRun
	if engine == nil || engine.registry == nil || engine.catalog == nil || engine.options.Home == "" {
		return result, errors.New("database migration engine is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !engine.running.CompareAndSwap(false, true) {
		return result, ErrStorageActive
	}
	defer engine.running.Store(false)

	fence, err := database.AcquireMigrationFence(engine.options.Home)
	if err != nil {
		if database.CodeOf(err) == database.CodeConflict {
			return result, ErrStorageActive
		}
		return result, err
	}
	defer func() {
		if closeErr := fence.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("release database migration fence: %w", closeErr))
		}
	}()

	lease, err := databaseclaims.AcquireProjected(engine.catalog, fence)
	if err != nil {
		if database.CodeOf(err) == database.CodeConflict {
			return result, ErrStorageActive
		}
		return result, err
	}
	defer func() {
		if closeErr := lease.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("release database catalog claims: %w", closeErr))
		}
	}()

	all := lease.Stores()
	specs, err := selectStores(all, options.Stores)
	if err != nil {
		return result, err
	}
	if !options.DryRun && len(specs) != 1 {
		return result, database.NewError(
			database.CodeInvalid,
			"database migration mutates exactly one store per run",
		)
	}
	result.Stores = make([]StoreResult, len(specs))
	for index, spec := range specs {
		result.Stores[index] = StoreResult{ID: spec.ID}
		info, statErr := os.Lstat(spec.Path)
		switch {
		case statErr == nil:
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return result, fmt.Errorf("store %s generation is unsafe", spec.ID)
			}
			result.Stores[index].Exists = true
		case errors.Is(statErr, os.ErrNotExist):
		default:
			return result, fmt.Errorf("inspect store %s: %w", spec.ID, statErr)
		}
	}

	backup, err := engine.snapshot(ctx, lease.Home(), all, specs, options.BackupDir)
	if backup != nil {
		result.BackupDir = backup.root
	}
	if err != nil {
		return result, err
	}
	if verifyErr := backup.verify(ctx); verifyErr != nil {
		verifyErr = errors.Join(verifyErr, backup.finish("failed", verifyErr))
		return result, fmt.Errorf(
			"verify database migration backup: %w; backup preserved at %s",
			verifyErr,
			backup.root,
		)
	}
	if options.DryRun {
		if err := backup.finish("dry_run", nil); err != nil {
			return result, fmt.Errorf("finalize dry-run database backup: %w", err)
		}
		return result, nil
	}
	if verifyErr := backup.verifyLiveSources(ctx, specs[0]); verifyErr != nil {
		verifyErr = errors.Join(verifyErr, backup.finish("failed", verifyErr))
		return result, fmt.Errorf(
			"verify live database migration sources: %w; backup preserved at %s",
			verifyErr,
			backup.root,
		)
	}
	if err := engine.preflight(ctx, specs, backup, result.Stores); err != nil {
		finalErr := errors.Join(err, backup.finish("failed", err))
		return result, fmt.Errorf("%w; backup preserved at %s", finalErr, backup.root)
	}
	if err := backup.finish("migration_in_progress", nil); err != nil {
		return result, fmt.Errorf("record database migration start: %w", err)
	}
	completedCutovers := 0
	defer func() {
		outcome := "failed"
		if completed || errors.Is(returnErr, errPostMigrationCleanup) {
			outcome = "complete"
		} else if database.CodeOf(returnErr) == database.CodeOutcomeUnknown || completedCutovers > 0 {
			outcome = "outcome_unknown"
		}
		if manifestErr := backup.finish(outcome, returnErr); manifestErr != nil {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("finalize database migration backup: %w", manifestErr),
			)
		}
		if returnErr != nil {
			returnErr = fmt.Errorf("%w; backup preserved at %s", returnErr, backup.root)
		}
	}()
	defer func() {
		if recover() != nil {
			returnErr = errors.New("database migration panicked")
		}
	}()

	for index, spec := range specs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		adapter, registered := engine.registry.Lookup(spec.Domain)
		if !registered {
			result.Stores[index].AdapterRequired = true
			return result, fmt.Errorf("migrate store %s: %w", spec.ID, ErrAdapterRequired)
		}
		if err := engine.migrateStore(
			ctx,
			fence,
			lease.PinReplacement,
			spec,
			adapter,
			backup,
			&result.Stores[index],
		); err != nil {
			return result, err
		}
		if result.Stores[index].cutover {
			if err := lease.RefreshReplacement(spec.ID); err != nil {
				return result, database.NewError(
					database.CodeOutcomeUnknown,
					"installed database generation could not be physically claimed",
				)
			}
			completedCutovers++
		} else if err := lease.Refresh(); err != nil {
			return result, err
		}
	}
	completed = true
	return result, nil
}

func (engine *Engine) preflight(
	ctx context.Context,
	specs []storecatalog.Spec,
	backup *backupSession,
	results []StoreResult,
) error {
	for index, spec := range specs {
		if err := ctx.Err(); err != nil {
			return err
		}
		adapter, registered := engine.registry.Lookup(spec.Domain)
		if !registered {
			results[index].AdapterRequired = true
			return fmt.Errorf("preflight store %s: %w", spec.ID, ErrAdapterRequired)
		}
		source, cleanup, err := backup.prepareGeneration(ctx, spec)
		if err != nil {
			return fmt.Errorf("preflight store %s backup generation: %w", spec.ID, err)
		}
		inspection, inspectErr := sqliteprovider.Inspect(ctx, source, migrationBusyTimeout)
		if inspectErr != nil {
			_ = cleanup()
			if sqliteprovider.IsInspectionIntegrity(inspectErr) {
				return fmt.Errorf("preflight store %s: %w", spec.ID, ErrIntegrity)
			}
			return fmt.Errorf("preflight store %s: %w", spec.ID, inspectErr)
		}
		needsAdapter, stateErr := migrationNeeded(
			ctx, adapter.Contract, inspection, backup.hasLegacy(spec.ID),
		)
		releaseErr := inspection.Release()
		cleanupErr := cleanup()
		if stateErr != nil || releaseErr != nil || cleanupErr != nil {
			return errors.Join(
				fmt.Errorf("preflight store %s state: %w", spec.ID, stateErr),
				releaseErr,
				cleanupErr,
			)
		}
		if inspection.Exists && inspection.Version > adapter.Contract.CurrentVersion {
			return fmt.Errorf("preflight store %s: %w", spec.ID, ErrSchemaTooNew)
		}
		if needsAdapter && adapter.Migrate == nil {
			results[index].AdapterRequired = true
			return fmt.Errorf("preflight store %s: %w", spec.ID, ErrAdapterRequired)
		}
	}
	return nil
}

func (engine *Engine) snapshot(
	ctx context.Context,
	home string,
	all []storecatalog.Spec,
	specs []storecatalog.Spec,
	configuredParent string,
) (*backupSession, error) {
	if engine == nil {
		return nil, errors.New("database migration engine is unavailable")
	}
	return snapshotBackup(ctx, engine.now, home, all, specs, configuredParent)
}

func backupOutcome(err error) string {
	if err == nil {
		return "complete"
	}
	if database.CodeOf(err) == database.CodeOutcomeUnknown {
		return "outcome_unknown"
	}
	return "failed"
}

func (engine *Engine) migrateStore(
	ctx context.Context,
	fence *database.Fence,
	pin func(database.StoreID, string) error,
	spec storecatalog.Spec,
	adapter databaseadapter.Adapter,
	backup *backupSession,
	result *StoreResult,
) (returnErr error) {
	if pin == nil {
		return errors.New("database migration physical pin is unavailable")
	}
	sourcePath, cleanup, err := backup.prepareGeneration(ctx, spec)
	if err != nil {
		return fmt.Errorf("prepare store %s backup generation: %w", spec.ID, err)
	}
	operationErr := engine.migrateStoreFromWithOps(
		ctx,
		fence,
		sourcePath,
		spec,
		adapter,
		backup,
		result,
		migrationStoreOps{
			inspect: sqliteprovider.Inspect,
			release: func(inspection sqliteprovider.Inspection) error { return inspection.Release() },
			migrate: func(
				migrationCtx context.Context,
				target string,
				timeout time.Duration,
				version int,
				migrate sqliteprovider.StagedMigration,
				validate sqliteprovider.StagedValidation,
			) error {
				return sqliteprovider.MigrateStagedOfflineFrom(
					migrationCtx, sourcePath, target, timeout, version, migrate, validate,
				)
			},
			maintain: sqliteprovider.MaintainOffline,
			pin:      pin,
		},
	)
	cleanupErr := cleanup()
	if operationErr == nil && cleanupErr != nil {
		return fmt.Errorf("%w: %v", errPostMigrationCleanup, cleanupErr)
	}
	return errors.Join(operationErr, cleanupErr)
}

func (engine *Engine) migrateStoreWithOps(
	ctx context.Context,
	fence *database.Fence,
	spec storecatalog.Spec,
	adapter databaseadapter.Adapter,
	backup *backupSession,
	result *StoreResult,
	ops migrationStoreOps,
) error {
	return engine.migrateStoreFromWithOps(
		ctx, fence, spec.Path, spec, adapter, backup, result, ops,
	)
}

func (engine *Engine) migrateStoreFromWithOps(
	ctx context.Context,
	fence *database.Fence,
	sourcePath string,
	spec storecatalog.Spec,
	adapter databaseadapter.Adapter,
	backup *backupSession,
	result *StoreResult,
	ops migrationStoreOps,
) error {
	if engine == nil || backup == nil || result == nil || fence == nil ||
		!fence.Authorizes(engine.options.Home) || ops.inspect == nil ||
		ops.release == nil || ops.migrate == nil || ops.maintain == nil || ops.pin == nil {
		return errors.New("database migration state is unavailable")
	}
	inspection, inspectErr := ops.inspect(ctx, sourcePath, migrationBusyTimeout)
	if inspectErr != nil {
		if sqliteprovider.IsInspectionIntegrity(inspectErr) {
			return fmt.Errorf("migrate store %s: %w", spec.ID, ErrIntegrity)
		}
		return fmt.Errorf("inspect store %s after backup: %w", spec.ID, inspectErr)
	}
	result.BeforeVersion = inspection.Version
	result.AfterVersion = inspection.Version

	needsAdapter, stateErr := migrationNeeded(
		ctx, adapter.Contract, inspection, backup.hasLegacy(spec.ID),
	)
	releaseErr := ops.release(inspection)
	if stateErr != nil {
		return errors.Join(
			fmt.Errorf("inspect store %s migration state: %w", spec.ID, stateErr),
			releaseErr,
		)
	}
	if releaseErr != nil {
		return fmt.Errorf("release store %s inspection: %w", spec.ID, releaseErr)
	}
	if inspection.Exists && inspection.Version > adapter.Contract.CurrentVersion {
		return fmt.Errorf(
			"migrate store %s: %w (database=%d supported=%d)",
			spec.ID,
			ErrSchemaTooNew,
			inspection.Version,
			adapter.Contract.CurrentVersion,
		)
	}

	validateStage := func(validationCtx context.Context, stagedPath string) error {
		if verifyErr := backup.verifyStore(validationCtx, spec.ID); verifyErr != nil {
			return fmt.Errorf("reverify database migration backup: %w", verifyErr)
		}
		staged, validationErr := ops.inspect(
			validationCtx, stagedPath, migrationBusyTimeout,
		)
		if validationErr != nil {
			return validationErr
		}
		ready, validationErr := contractReady(validationCtx, adapter.Contract, staged)
		releaseErr := ops.release(staged)
		if validationErr != nil {
			return errors.Join(validationErr, releaseErr)
		}
		if releaseErr != nil {
			return fmt.Errorf("release staged database inspection: %w", releaseErr)
		}
		if !ready {
			return ErrAdapterRequired
		}
		if verifyErr := backup.verifyLiveSources(validationCtx, spec); verifyErr != nil {
			return fmt.Errorf("reverify live database migration sources: %w", verifyErr)
		}
		if pinErr := ops.pin(spec.ID, stagedPath); pinErr != nil {
			return fmt.Errorf("pin staged database generation: %w", pinErr)
		}
		return nil
	}
	migrateStage := func(context.Context, string) error { return nil }
	if needsAdapter {
		if adapter.Migrate == nil {
			result.AdapterRequired = true
			return fmt.Errorf(
				"migrate store %s: %w (database=%d supported=%d)",
				spec.ID,
				ErrAdapterRequired,
				inspection.Version,
				adapter.Contract.CurrentVersion,
			)
		}
		migrateStage = func(callbackCtx context.Context, stagedPath string) (callbackErr error) {
			authorizedCtx, authorizationErr := fence.MigrationContext(callbackCtx, stagedPath)
			if authorizationErr != nil {
				return authorizationErr
			}
			legacyRoots, cleanup, inputErr := backup.prepareLegacyInputs(
				authorizedCtx, spec,
			)
			if inputErr != nil {
				return inputErr
			}
			defer func() { callbackErr = errors.Join(callbackErr, cleanup()) }()
			return adapter.Migrate(authorizedCtx, databaseadapter.Target{
				ID: spec.ID, GenerationPath: stagedPath, LegacyRoots: legacyRoots,
			})
		}
	} else if inspection.Exists && !inspection.Empty {
		maintenanceCtx, authorizationErr := fence.MigrationContext(ctx, sourcePath)
		if authorizationErr != nil {
			return authorizationErr
		}
		maintenance, maintenanceErr := ops.maintain(
			maintenanceCtx,
			sourcePath,
			migrationBusyTimeout,
		)
		result.BeforeVersion = maintenance.BeforeVersion
		result.AfterVersion = maintenance.AfterVersion
		if maintenanceErr != nil {
			if sqliteprovider.IsMaintenanceIntegrity(maintenanceErr) {
				return fmt.Errorf("migrate store %s: %w", spec.ID, ErrIntegrity)
			}
			return fmt.Errorf("maintain store %s: %w", spec.ID, maintenanceErr)
		}
	} else {
		// Missing, legacy-free EmptyInitializeOnline generations belong to later
		// broker initialization, not offline migration.
		return nil
	}

	providerCtx, authorizationErr := fence.MigrationContext(ctx, spec.Path)
	if authorizationErr != nil {
		return authorizationErr
	}
	migrationErr := ops.migrate(
		providerCtx,
		spec.Path,
		migrationBusyTimeout,
		adapter.Contract.CurrentVersion,
		migrateStage,
		validateStage,
	)
	if migrationErr != nil {
		return fmt.Errorf("migrate store %s staged generation: %w", spec.ID, migrationErr)
	}
	after, err := ops.inspect(ctx, spec.Path, migrationBusyTimeout)
	if err != nil {
		return database.NewError(
			database.CodeOutcomeUnknown,
			"installed database generation could not be reinspected",
		)
	}
	ready, readyErr := contractReady(ctx, adapter.Contract, after)
	afterReleaseErr := ops.release(after)
	if readyErr != nil {
		return database.NewError(
			database.CodeOutcomeUnknown,
			"installed database contract could not be revalidated",
		)
	}
	if afterReleaseErr != nil {
		return database.NewError(
			database.CodeOutcomeUnknown,
			"installed database inspection could not be released",
		)
	}
	if !ready {
		return database.NewError(
			database.CodeOutcomeUnknown,
			"installed database generation failed contract revalidation",
		)
	}
	result.AfterVersion = after.Version
	result.Migrated = needsAdapter
	result.cutover = true
	return nil
}

func migrationNeeded(
	ctx context.Context,
	contract databaseadapter.Contract,
	inspection sqliteprovider.Inspection,
	legacyExists bool,
) (bool, error) {
	if !inspection.Exists || inspection.Empty {
		return legacyExists || contract.EmptyPolicy == databaseadapter.EmptyMigrateOffline, nil
	}
	if inspection.Version > contract.CurrentVersion {
		return false, nil
	}
	if inspection.Version < contract.CurrentVersion {
		return true, nil
	}
	ready, err := contractReady(ctx, contract, inspection)
	return !ready, err
}

func contractReady(
	ctx context.Context,
	contract databaseadapter.Contract,
	inspection sqliteprovider.Inspection,
) (bool, error) {
	if !inspection.Exists || inspection.Empty || inspection.Version != contract.CurrentVersion {
		return false, nil
	}
	for _, object := range contract.RequiredObjects {
		objects, err := inspection.HasSchemaObjects(ctx, object.Type, object.Name)
		if err != nil || !objects {
			return false, err
		}
	}
	for _, columns := range contract.RequiredColumns {
		ready, columnErr := inspection.HasTableColumns(ctx, columns.Table, columns.Columns...)
		if columnErr != nil || !ready {
			return false, columnErr
		}
	}
	if contract.ImportHorizon != "" {
		closed, horizonErr := inspection.HasImportHorizon(ctx, contract.ImportHorizon)
		if horizonErr != nil || !closed {
			return false, horizonErr
		}
	}
	return true, nil
}

func selectStores(all []storecatalog.Spec, requested []database.StoreID) ([]storecatalog.Spec, error) {
	if len(requested) == 0 {
		if len(all) > maximumMigrationStores {
			return nil, errors.New("database migration store limit is exceeded")
		}
		return cloneSpecs(all), nil
	}
	if len(requested) > maximumMigrationStores {
		return nil, errors.New("database migration store limit is exceeded")
	}
	byID := make(map[database.StoreID]storecatalog.Spec, len(all))
	for _, spec := range all {
		byID[spec.ID] = spec
	}
	selected := make([]storecatalog.Spec, 0, len(requested))
	seen := make(map[database.StoreID]struct{}, len(requested))
	for _, id := range requested {
		if !id.Valid() {
			return nil, fmt.Errorf("%w: %q", ErrUnknownStore, id)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("duplicate database store ID %q", id)
		}
		seen[id] = struct{}{}
		spec, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownStore, id)
		}
		selected = append(selected, cloneSpec(spec))
	}
	sort.Slice(selected, func(left, right int) bool { return selected[left].ID < selected[right].ID })
	return selected, nil
}

func cloneSpecs(specs []storecatalog.Spec) []storecatalog.Spec {
	result := make([]storecatalog.Spec, len(specs))
	for index := range specs {
		result[index] = cloneSpec(specs[index])
	}
	return result
}

func cloneSpec(spec storecatalog.Spec) storecatalog.Spec {
	spec.LegacyRoots = append([]string(nil), spec.LegacyRoots...)
	return spec
}
