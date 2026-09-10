// Package databasemigration coordinates exclusively fenced, backed-up,
// provider-owned offline database maintenance.
package databasemigration

import (
	"context"
	"errors"
	"fmt"
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
	migrationBusyTimeout         = 5 * time.Second
	maximumMigrationStores       = 256
	maximumProviderLeaseDuration = 10 * time.Minute
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

	migrationRequired bool
	providerRequired  bool
	cutover           bool
}

// Result reports mandatory backup identity and selected store outcomes.
type Result struct {
	DryRun    bool          `json:"dry_run"`
	BackupDir string        `json:"backup_dir,omitempty"`
	Stores    []StoreResult `json:"stores"`
}

// Engine binds exact catalog path-resolution context and explicit adapter set.
// It does not acquire live storage ownership until Run holds a migration fence.
type Engine struct {
	options  storecatalog.Options
	catalog  *storecatalog.Catalog
	registry *databaseadapter.Registry
	now      func() time.Time
	running  atomic.Bool
}

// migrationStoreOps keeps provider fault injection at the complete authority
// boundary. It deliberately exposes neither a raw source path nor claims pin
// callbacks, so tests cannot bypass the production child-lease protocol.
type migrationStoreOps struct {
	inspect  func(context.Context, string, time.Duration) (sqliteprovider.Inspection, error)
	release  func(sqliteprovider.Inspection) error
	provider func(
		context.Context,
		*databaseclaims.MigrationRefreshingGuard,
		storecatalog.Spec,
		sqliteprovider.ImmutableGenerationSource,
		time.Duration,
		int,
		sqliteprovider.StagedMigration,
		sqliteprovider.StagedValidation,
	) (sqliteprovider.MaintenanceResult, error)
}

// migrationRunOps keeps lifecycle fault injection scoped to one private call.
// Production Run always installs the exact concrete operations below.
type migrationRunOps struct {
	acquireFence func(string) (*database.Fence, error)
	releaseFence func(*database.Fence) error
	acquireLease func(*storecatalog.Catalog, *database.Fence) (*databaseclaims.Lease, error)
	releaseLease func(*databaseclaims.Lease) error
	acquireGuard func(*databaseclaims.Lease) (*databaseclaims.MigrationRefreshingGuard, error)
	releaseGuard func(*databaseclaims.MigrationRefreshingGuard) error
	guardStores  func(*databaseclaims.MigrationRefreshingGuard) ([]storecatalog.Spec, error)
	guardCheck   func(*databaseclaims.MigrationRefreshingGuard) error
	snapshot     func(
		*Engine, context.Context, string, []storecatalog.Spec, []storecatalog.Spec, string,
	) (*backupSession, error)
	verifyBackup func(*backupSession, context.Context) error
	finishBackup func(*backupSession, string, error) error
	verifyLive   func(*backupSession, context.Context, storecatalog.Spec) error
	preflight    func(
		*Engine, context.Context, []storecatalog.Spec, *backupSession, []StoreResult,
	) error
	lookup  func(*databaseadapter.Registry, string) (databaseadapter.Adapter, bool)
	migrate func(
		*Engine,
		context.Context,
		*database.Fence,
		*databaseclaims.MigrationRefreshingGuard,
		storecatalog.Spec,
		databaseadapter.Adapter,
		*backupSession,
		*StoreResult,
	) error
}

func defaultMigrationStoreOps() migrationStoreOps {
	return migrationStoreOps{
		inspect: sqliteprovider.Inspect,
		release: func(inspection sqliteprovider.Inspection) error {
			return inspection.Release()
		},
		provider: runMigrationProvider,
	}
}

func defaultMigrationRunOps() migrationRunOps {
	return migrationRunOps{
		acquireFence: database.AcquireMigrationFence,
		releaseFence: func(fence *database.Fence) error { return fence.Close() },
		acquireLease: databaseclaims.AcquireProjected,
		releaseLease: func(lease *databaseclaims.Lease) error { return lease.Close() },
		acquireGuard: func(
			lease *databaseclaims.Lease,
		) (*databaseclaims.MigrationRefreshingGuard, error) {
			return lease.GuardStoresMigrating()
		},
		releaseGuard: func(guard *databaseclaims.MigrationRefreshingGuard) error {
			return guard.Release()
		},
		guardStores: func(
			guard *databaseclaims.MigrationRefreshingGuard,
		) ([]storecatalog.Spec, error) {
			return guard.Stores()
		},
		guardCheck: func(guard *databaseclaims.MigrationRefreshingGuard) error {
			return guard.Check()
		},
		snapshot: func(
			engine *Engine,
			ctx context.Context,
			home string,
			all []storecatalog.Spec,
			selected []storecatalog.Spec,
			parent string,
		) (*backupSession, error) {
			return engine.snapshot(ctx, home, all, selected, parent)
		},
		verifyBackup: func(backup *backupSession, ctx context.Context) error {
			return backup.verify(ctx)
		},
		finishBackup: func(backup *backupSession, outcome string, cause error) error {
			return backup.finish(outcome, cause)
		},
		verifyLive: func(
			backup *backupSession,
			ctx context.Context,
			spec storecatalog.Spec,
		) error {
			return backup.verifyLiveSources(ctx, spec)
		},
		preflight: func(
			engine *Engine,
			ctx context.Context,
			specs []storecatalog.Spec,
			backup *backupSession,
			results []StoreResult,
		) error {
			return engine.preflight(ctx, specs, backup, results)
		},
		lookup: func(
			registry *databaseadapter.Registry,
			domain string,
		) (databaseadapter.Adapter, bool) {
			return registry.Lookup(domain)
		},
		migrate: func(
			engine *Engine,
			ctx context.Context,
			fence *database.Fence,
			guard *databaseclaims.MigrationRefreshingGuard,
			spec storecatalog.Spec,
			adapter databaseadapter.Adapter,
			backup *backupSession,
			result *StoreResult,
		) error {
			return engine.migrateStore(ctx, fence, guard, spec, adapter, backup, result)
		},
	}
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

// Run acquires the migration fence and complete physical claims, snapshots and
// verifies the selected inputs, then performs provider-owned staged migration.
// Failures preserve the committed backup and never restore it automatically.
func (engine *Engine) Run(
	ctx context.Context,
	options Options,
) (result Result, returnErr error) {
	return engine.runWithOps(ctx, options, defaultMigrationRunOps())
}

//nolint:cyclop,gocognit // The order is the migration safety contract.
func (engine *Engine) runWithOps(
	ctx context.Context,
	options Options,
	ops migrationRunOps,
) (result Result, returnErr error) {
	result.DryRun = options.DryRun
	if engine == nil || engine.registry == nil || engine.catalog == nil ||
		engine.options.Home == "" || !validMigrationRunOps(ops) {
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

	var (
		fence         *database.Fence
		lease         *databaseclaims.Lease
		guard         *databaseclaims.MigrationRefreshingGuard
		backup        *backupSession
		statusStarted bool
	)
	defer func() {
		if recovered := recover(); recovered != nil {
			returnErr = errors.New("database migration panicked")
		}
		if migrationCutoverCompleted(result) && returnErr != nil &&
			!errors.Is(returnErr, errPostMigrationCleanup) &&
			database.CodeOf(returnErr) != database.CodeOutcomeUnknown {
			returnErr = migrationOutcomeUnknown(
				"installed database migration completion is uncertain",
				returnErr,
			)
		}
		if statusStarted {
			outcome := migrationTerminalOutcome(options.DryRun, result, returnErr)
			cause := returnErr
			if outcome == "complete" || outcome == "dry_run" {
				cause = nil
			}
			if statusErr := ops.finishBackup(backup, outcome, cause); statusErr != nil {
				statusErr = fmt.Errorf("finalize database migration backup status: %w", statusErr)
				if migrationCutoverMayHaveOccurred(result, returnErr) {
					returnErr = migrationOutcomeUnknown(
						"installed database migration status is uncertain",
						returnErr,
						statusErr,
					)
				} else {
					returnErr = errors.Join(returnErr, statusErr)
				}
			}
		}
		if guard != nil {
			guardErr := ops.releaseGuard(guard)
			if guardErr != nil && migrationCutoverMayHaveOccurred(result, returnErr) {
				returnErr = migrationOutcomeUnknown(
					"installed database migration authority could not be released",
					returnErr,
					guardErr,
				)
			} else if guardErr != nil {
				returnErr = errors.Join(
					returnErr,
					fmt.Errorf("release database migration guard: %w", guardErr),
				)
			}
			guard = nil
		}
		if lease != nil {
			if closeErr := ops.releaseLease(lease); closeErr != nil {
				returnErr = errors.Join(
					returnErr,
					fmt.Errorf("release database catalog claims: %w", closeErr),
				)
			}
			lease = nil
		}
		if fence != nil {
			if closeErr := ops.releaseFence(fence); closeErr != nil {
				returnErr = errors.Join(
					returnErr,
					fmt.Errorf("release database migration fence: %w", closeErr),
				)
			}
			fence = nil
		}
		if returnErr != nil && backup != nil && backup.root != "" {
			returnErr = fmt.Errorf("%w; backup preserved at %s", returnErr, backup.root)
		}
	}()

	var err error
	fence, err = ops.acquireFence(engine.options.Home)
	if err != nil {
		if database.CodeOf(err) == database.CodeConflict {
			return result, ErrStorageActive
		}
		return result, err
	}
	lease, err = ops.acquireLease(engine.catalog, fence)
	if err != nil {
		if database.CodeOf(err) == database.CodeConflict {
			return result, ErrStorageActive
		}
		return result, err
	}
	guard, err = ops.acquireGuard(lease)
	if err != nil {
		if database.CodeOf(err) == database.CodeConflict {
			return result, ErrStorageActive
		}
		return result, err
	}
	all, err := ops.guardStores(guard)
	if err != nil {
		return result, err
	}
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
	for index := range specs {
		result.Stores[index].ID = specs[index].ID
	}
	if checkErr := ops.guardCheck(guard); checkErr != nil {
		return result, checkErr
	}
	backup, err = ops.snapshot(
		engine,
		ctx,
		engine.options.Home,
		all,
		specs,
		options.BackupDir,
	)
	if backup != nil {
		result.BackupDir = backup.root
	}
	if err != nil {
		return result, err
	}
	if err := ops.verifyBackup(backup, ctx); err != nil {
		return result, fmt.Errorf("verify database migration backup: %w", err)
	}
	if err := populateMigrationStoreExistence(backup, specs, result.Stores); err != nil {
		return result, err
	}
	if options.DryRun {
		if err := ops.finishBackup(backup, "migration_in_progress", nil); err != nil {
			return result, fmt.Errorf("record database dry-run start: %w", err)
		}
		statusStarted = true
		return result, nil
	}
	if err := ops.guardCheck(guard); err != nil {
		return result, err
	}
	if err := ops.verifyLive(backup, ctx, specs[0]); err != nil {
		return result, fmt.Errorf("verify live database migration sources: %w", err)
	}
	if err := ops.preflight(engine, ctx, specs, backup, result.Stores); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	adapter, registered := ops.lookup(engine.registry, specs[0].Domain)
	if !registered {
		result.Stores[0].AdapterRequired = true
		return result, fmt.Errorf("migrate store %s: %w", specs[0].ID, ErrAdapterRequired)
	}
	if err := ops.guardCheck(guard); err != nil {
		return result, err
	}
	if err := ops.finishBackup(backup, "migration_in_progress", nil); err != nil {
		return result, fmt.Errorf("record database migration start: %w", err)
	}
	statusStarted = true
	if err := ops.migrate(
		engine,
		ctx,
		fence,
		guard,
		specs[0],
		adapter,
		backup,
		&result.Stores[0],
	); err != nil {
		return result, err
	}
	if err := ops.guardCheck(guard); err != nil {
		return result, err
	}
	return result, nil
}

func validMigrationRunOps(ops migrationRunOps) bool {
	return ops.acquireFence != nil && ops.releaseFence != nil &&
		ops.acquireLease != nil && ops.releaseLease != nil &&
		ops.acquireGuard != nil && ops.releaseGuard != nil &&
		ops.guardStores != nil && ops.guardCheck != nil &&
		ops.snapshot != nil && ops.verifyBackup != nil && ops.finishBackup != nil &&
		ops.verifyLive != nil && ops.preflight != nil && ops.lookup != nil && ops.migrate != nil
}

func migrationTerminalOutcome(dryRun bool, result Result, err error) string {
	if err == nil {
		if dryRun {
			return "dry_run"
		}
		return "complete"
	}
	if database.CodeOf(err) == database.CodeOutcomeUnknown {
		return "outcome_unknown"
	}
	if migrationCutoverCompleted(result) && errors.Is(err, errPostMigrationCleanup) {
		return "complete_with_cleanup_error"
	}
	return "failed"
}

func migrationCutoverCompleted(result Result) bool {
	for index := range result.Stores {
		if result.Stores[index].cutover {
			return true
		}
	}
	return false
}

func migrationCutoverMayHaveOccurred(result Result, err error) bool {
	return migrationCutoverCompleted(result) || database.CodeOf(err) == database.CodeOutcomeUnknown
}

func migrationOutcomeUnknown(message string, causes ...error) error {
	values := make([]error, 0, len(causes)+1)
	values = append(values, database.NewError(database.CodeOutcomeUnknown, message))
	values = append(values, causes...)
	return errors.Join(values...)
}

func populateMigrationStoreExistence(
	backup *backupSession,
	specs []storecatalog.Spec,
	results []StoreResult,
) error {
	if backup == nil || len(specs) != len(results) {
		return errors.New("database migration backup inventory is unavailable")
	}
	byID := make(map[database.StoreID]BackupStoreManifest, len(backup.manifest.Stores))
	for _, stored := range backup.manifest.Stores {
		id := database.StoreID(stored.StoreID)
		if !id.Valid() {
			return errors.New("database migration backup store identity is invalid")
		}
		if _, duplicate := byID[id]; duplicate {
			return errors.New("database migration backup store identity is duplicated")
		}
		byID[id] = stored
	}
	for index, spec := range specs {
		stored, found := byID[spec.ID]
		if !found {
			return fmt.Errorf("database migration backup omits store %s", spec.ID)
		}
		results[index].Exists = stored.Exists
	}
	return nil
}

func (engine *Engine) preflight(
	ctx context.Context,
	specs []storecatalog.Spec,
	backup *backupSession,
	results []StoreResult,
) error {
	if engine == nil || engine.registry == nil || backup == nil || len(specs) != len(results) {
		return errors.New("database migration preflight state is unavailable")
	}
	for index, spec := range specs {
		if err := ctx.Err(); err != nil {
			return err
		}
		adapter, registered := engine.registry.Lookup(spec.Domain)
		if !registered {
			results[index].AdapterRequired = true
			return fmt.Errorf("preflight store %s: %w", spec.ID, ErrAdapterRequired)
		}
		legacyExists, err := backupLegacyExists(backup, spec.ID)
		if err != nil {
			return fmt.Errorf("preflight store %s legacy inventory: %w", spec.ID, err)
		}
		prepared, cleanup, err := backup.prepareGeneration(ctx, spec)
		if err != nil {
			return errors.Join(
				fmt.Errorf("preflight store %s backup generation: %w", spec.ID, err),
				callMigrationCleanup(cleanup),
			)
		}
		var (
			inspection   sqliteprovider.Inspection
			needsAdapter bool
		)
		useErr := prepared.use(ctx, func(useCtx context.Context, sourcePath string) error {
			var inspectErr error
			inspection, inspectErr = sqliteprovider.Inspect(
				useCtx,
				sourcePath,
				migrationBusyTimeout,
			)
			if inspectErr != nil {
				if sqliteprovider.IsInspectionIntegrity(inspectErr) {
					return fmt.Errorf("%w: %v", ErrIntegrity, inspectErr)
				}
				return inspectErr
			}
			var stateErr error
			needsAdapter, stateErr = migrationNeeded(
				useCtx,
				adapter.Contract,
				inspection,
				legacyExists,
			)
			return errors.Join(stateErr, inspection.Release())
		})
		cleanupErr := callMigrationCleanup(cleanup)
		if useErr != nil || cleanupErr != nil {
			var stateErr error
			if useErr != nil {
				stateErr = fmt.Errorf("preflight store %s state: %w", spec.ID, useErr)
			}
			return errors.Join(stateErr, cleanupErr)
		}
		results[index].BeforeVersion = inspection.Version
		results[index].AfterVersion = inspection.Version
		results[index].migrationRequired = needsAdapter
		results[index].providerRequired = needsAdapter || inspection.Exists && !inspection.Empty
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

func backupLegacyExists(backup *backupSession, id database.StoreID) (bool, error) {
	if backup == nil || !id.Valid() {
		return false, errors.New("database migration legacy inventory is unavailable")
	}
	found := false
	exists := false
	for _, stored := range backup.manifest.Stores {
		if stored.StoreID != id.String() {
			continue
		}
		if found || len(stored.LegacyRoots) != len(stored.LegacyRootKinds) {
			return false, errors.New("database migration legacy inventory is invalid")
		}
		found = true
		for _, kind := range stored.LegacyRootKinds {
			switch kind {
			case "missing":
			case "file", "directory":
				exists = true
			default:
				return false, errors.New("database migration legacy root kind is invalid")
			}
		}
	}
	if !found {
		return false, errors.New("database migration legacy store is unavailable")
	}
	return exists, nil
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

func (engine *Engine) migrateStore(
	ctx context.Context,
	fence *database.Fence,
	guard *databaseclaims.MigrationRefreshingGuard,
	spec storecatalog.Spec,
	adapter databaseadapter.Adapter,
	backup *backupSession,
	result *StoreResult,
) (returnErr error) {
	if engine == nil || fence == nil || guard == nil || backup == nil || result == nil {
		return errors.New("database migration state is unavailable")
	}
	if !result.providerRequired {
		return nil
	}
	source, cleanup, err := backup.prepareImmutableGenerationSource(ctx, spec)
	if err != nil {
		return errors.Join(
			fmt.Errorf("prepare store %s immutable backup generation: %w", spec.ID, err),
			callMigrationCleanup(cleanup),
		)
	}
	return runMigrationPreparedSource(cleanup, func() error {
		return engine.migrateStoreWithOps(
			ctx,
			fence,
			guard,
			source,
			spec,
			adapter,
			backup,
			result,
			defaultMigrationStoreOps(),
		)
	})
}

func runMigrationPreparedSource(
	cleanup func() error,
	run func() error,
) (returnErr error) {
	if cleanup == nil || run == nil {
		return errors.New("database migration prepared source is unavailable")
	}
	defer func() {
		cleanupErr := callMigrationCleanup(cleanup)
		if returnErr == nil && cleanupErr != nil {
			returnErr = errors.Join(errPostMigrationCleanup, cleanupErr)
			return
		}
		returnErr = errors.Join(returnErr, cleanupErr)
	}()
	return run()
}

func callMigrationCleanup(cleanup func() error) (returnErr error) {
	if cleanup == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			returnErr = errors.New("database migration cleanup panicked")
		}
	}()
	return cleanup()
}

func (engine *Engine) migrateStoreWithOps(
	ctx context.Context,
	fence *database.Fence,
	guard *databaseclaims.MigrationRefreshingGuard,
	source sqliteprovider.ImmutableGenerationSource,
	spec storecatalog.Spec,
	adapter databaseadapter.Adapter,
	backup *backupSession,
	result *StoreResult,
	ops migrationStoreOps,
) error {
	if engine == nil || backup == nil || result == nil || fence == nil || guard == nil ||
		!fence.Authorizes(engine.options.Home) || ops.inspect == nil ||
		ops.release == nil || ops.provider == nil {
		return errors.New("database migration state is unavailable")
	}
	if !result.providerRequired {
		return nil
	}

	validateStage := func(validationCtx context.Context, stagedPath string) error {
		if verifyErr := backup.verifyStore(validationCtx, spec.ID); verifyErr != nil {
			return fmt.Errorf("reverify database migration backup: %w", verifyErr)
		}
		staged, validationErr := ops.inspect(
			validationCtx,
			stagedPath,
			migrationBusyTimeout,
		)
		if validationErr != nil {
			return validationErr
		}
		ready, validationErr := contractReady(validationCtx, adapter.Contract, staged)
		releaseErr := ops.release(staged)
		if validationErr != nil || releaseErr != nil {
			return errors.Join(validationErr, releaseErr)
		}
		if !ready {
			return ErrAdapterRequired
		}
		if verifyErr := backup.verifyLiveSources(validationCtx, spec); verifyErr != nil {
			return fmt.Errorf("reverify live database migration sources: %w", verifyErr)
		}
		return nil
	}

	migrateStage := sqliteprovider.StagedMigration(func(context.Context, string) error { return nil })
	if result.migrationRequired {
		if adapter.Migrate == nil {
			result.AdapterRequired = true
			return fmt.Errorf("migrate store %s: %w", spec.ID, ErrAdapterRequired)
		}
		migrateStage = func(callbackCtx context.Context, stagedPath string) (callbackErr error) {
			authorizedCtx, authorizationErr := fence.MigrationContext(callbackCtx, stagedPath)
			if authorizationErr != nil {
				return authorizationErr
			}
			legacy, cleanup, inputErr := backup.prepareLegacyInputs(authorizedCtx, spec)
			if inputErr != nil {
				return errors.Join(inputErr, callMigrationCleanup(cleanup))
			}
			defer func() {
				callbackErr = errors.Join(callbackErr, callMigrationCleanup(cleanup))
			}()
			invoke := func(adapterCtx context.Context, legacyRoots []string) error {
				return adapter.Migrate(adapterCtx, databaseadapter.Target{
					ID:             spec.ID,
					GenerationPath: stagedPath,
					LegacyRoots:    append([]string(nil), legacyRoots...),
				})
			}
			if legacy == nil {
				return invoke(authorizedCtx, nil)
			}
			return legacy.use(authorizedCtx, invoke)
		}
	}

	maintenance, migrationErr := ops.provider(
		ctx,
		guard,
		spec,
		source,
		migrationBusyTimeout,
		adapter.Contract.CurrentVersion,
		migrateStage,
		validateStage,
	)
	result.BeforeVersion = maintenance.BeforeVersion
	result.AfterVersion = maintenance.AfterVersion
	if migrationErr != nil {
		return fmt.Errorf("migrate store %s staged generation: %w", spec.ID, migrationErr)
	}
	result.cutover = true
	result.Migrated = result.migrationRequired
	return nil
}

func runMigrationProvider(
	ctx context.Context,
	guard *databaseclaims.MigrationRefreshingGuard,
	spec storecatalog.Spec,
	source sqliteprovider.ImmutableGenerationSource,
	busyTimeout time.Duration,
	expectedVersion int,
	migrate sqliteprovider.StagedMigration,
	validate sqliteprovider.StagedValidation,
) (result sqliteprovider.MaintenanceResult, returnErr error) {
	if ctx == nil || guard == nil {
		return result, errors.New("database migration provider authority is unavailable")
	}
	providerCtx, cancel := context.WithTimeout(ctx, maximumProviderLeaseDuration)
	defer cancel()
	providerLease, drain, err := guard.NewProviderLease(providerCtx, spec.ID)
	if err != nil {
		return result, err
	}
	return runMigrationProviderChild(
		drain,
		func() (sqliteprovider.MaintenanceResult, error) {
			return sqliteprovider.MigrateStagedOfflineFrom(
				providerCtx,
				providerLease,
				source,
				busyTimeout,
				expectedVersion,
				migrate,
				validate,
			)
		},
	)
}

func runMigrationProviderChild(
	drain func(context.Context, error) error,
	run func() (sqliteprovider.MaintenanceResult, error),
) (result sqliteprovider.MaintenanceResult, returnErr error) {
	if drain == nil || run == nil {
		return result, errors.New("database migration provider child is unavailable")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			returnErr = migrationOutcomeUnknown(
				"database migration provider panicked",
			)
		}
		drainErr := callMigrationProviderDrain(drain, returnErr)
		if drainErr != nil {
			returnErr = migrationOutcomeUnknown(
				"installed database provider authority did not drain",
				returnErr,
				drainErr,
			)
			return
		}
		returnErr = errors.Join(returnErr, drainErr)
	}()
	return run()
}

func callMigrationProviderDrain(
	drain func(context.Context, error) error,
	cause error,
) (returnErr error) {
	if drain == nil {
		return errors.New("database migration provider drain is unavailable")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			returnErr = errors.New("database migration provider drain panicked")
		}
	}()
	return drain(context.Background(), cause)
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
		ready, columnErr := inspection.HasTableColumns(
			ctx,
			columns.Table,
			columns.Columns...,
		)
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

func selectStores(
	all []storecatalog.Spec,
	requested []database.StoreID,
) ([]storecatalog.Spec, error) {
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
		spec, found := byID[id]
		if !found {
			return nil, fmt.Errorf("%w: %q", ErrUnknownStore, id)
		}
		selected = append(selected, cloneSpec(spec))
	}
	sort.Slice(selected, func(left, right int) bool {
		return selected[left].ID < selected[right].ID
	})
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
