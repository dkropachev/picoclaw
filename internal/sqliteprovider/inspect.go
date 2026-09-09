package sqliteprovider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

const maxInspectionSchemaNames = 1024

var (
	errInspectionIntegrity      = errors.New("SQLite provider integrity failure")
	errInspectionInfrastructure = errors.New("SQLite provider inspection infrastructure failure")
	errInspectionUnavailable    = errors.New("SQLite provider inspection is unavailable")
)

// Inspection is provider-private physical readiness metadata.
type Inspection struct {
	Exists     bool
	Empty      bool
	Version    int
	database   *sql.DB
	path       string
	key        string
	generation inspectedGeneration
	released   *atomic.Bool
}

type inspectionOps struct {
	canonicalize       func(string) (string, string, error)
	validateAncestors  func(string) error
	lstat              func(string) (os.FileInfo, error)
	ensureDirectory    func(string) error
	validateGeneration func(string, bool) error
	captureGeneration  func(string) (inspectedGeneration, error)
	poolFor            func(string, inspectedGeneration, time.Duration, *atomic.Bool) (*sql.DB, error)
	dsn                func(string, time.Duration) (string, error)
	open               func(string) (*sql.DB, error)
	inspectDatabase    func(context.Context, string, inspectedGeneration, *sql.DB) (inspectedGeneration, int, int, error)
	reconcile          func() error
	retain             func(string, *sql.DB, inspectedGeneration, time.Duration, *atomic.Bool) (*sql.DB, error)
	release            func(string, *sql.DB, *atomic.Bool) error
}

func defaultInspectionOps() inspectionOps {
	return inspectionOps{
		canonicalize:       canonicalInspectionLocation,
		validateAncestors:  validateProviderAncestors,
		lstat:              os.Lstat,
		ensureDirectory:    EnsurePrivateDirectory,
		validateGeneration: validateGenerationMembers,
		captureGeneration:  captureInspectedGeneration,
		poolFor:            inspectedPoolFor,
		dsn:                DSN,
		open:               open,
		inspectDatabase:    inspectDatabase,
		reconcile:          func() error { return nil },
		retain:             retainInspectedPool,
		release:            releaseInspectedPool,
	}
}

// Release closes this readiness pool only if no store owner adopted it.
// Inspection copies share one transition token, so concurrent and repeated
// releases are harmless no-ops after the first winning lifecycle transition.
func (inspection Inspection) Release() error {
	if inspection.database == nil {
		return nil
	}
	if inspection.released == nil || !inspection.released.CompareAndSwap(false, true) {
		return nil
	}
	return releaseInspectedPool(inspection.key, inspection.database, inspection.released)
}

// Adopt transfers this sole live inspection reference to the trusted store
// owner. The pool's inspected timeout is immutable and cannot be changed at
// handoff. Success invalidates every Inspection copy and makes the caller
// responsible for closing the returned pool. Failure transfers no ownership
// and leaves the reference releasable unless a concurrent Release won.
func (inspection Inspection) Adopt() (*sql.DB, error) {
	if inspection.database == nil || inspection.path == "" || inspection.key == "" ||
		inspection.released == nil || inspection.released.Load() {
		return nil, errors.New("SQLite provider inspection pool is unavailable")
	}
	return adoptInspectedPool(inspection.path, inspection.key, inspection.released)
}

// IsInspectionIntegrity distinguishes damaged generations from temporary
// unavailability without exposing driver errors to domain clients.
func IsInspectionIntegrity(err error) bool { return errors.Is(err, errInspectionIntegrity) }

// IsInspectionInfrastructure reports a pool-lifecycle or cleanup failure that
// prevents a catalog-wide readiness result from being safely published.
func IsInspectionInfrastructure(err error) bool {
	return errors.Is(err, errInspectionInfrastructure)
}

// Inspect validates an existing generation without creating a missing store or
// applying schema work. Existing sidecars are checked before SQLite opens. All
// context-aware provider work is bounded by busyTimeout and the parent context.
func Inspect(ctx context.Context, path string, busyTimeout time.Duration) (Inspection, error) {
	return inspectWithOps(ctx, path, busyTimeout, defaultInspectionOps())
}

// InspectClaimed is Inspect with a mandatory claim reconciliation immediately
// before any failed opened-pool cleanup. Callers must already hold the
// corresponding exclusive refreshing lease guard.
func InspectClaimed(
	ctx context.Context,
	path string,
	busyTimeout time.Duration,
	reconcile func() error,
) (Inspection, error) {
	ops := defaultInspectionOps()
	ops.reconcile = reconcile
	return inspectWithOps(ctx, path, busyTimeout, ops)
}

func inspectWithOps(
	parent context.Context,
	path string,
	busyTimeout time.Duration,
	ops inspectionOps,
) (result Inspection, resultErr error) {
	if parent == nil {
		return Inspection{}, errors.New("SQLite inspection context is unavailable")
	}
	if err := validateProviderInput(path, busyTimeout); err != nil || path == ":memory:" {
		return Inspection{}, errors.Join(errors.New("SQLite inspection input is invalid"), err)
	}
	if ops.canonicalize == nil || ops.validateAncestors == nil || ops.lstat == nil ||
		ops.ensureDirectory == nil ||
		ops.validateGeneration == nil || ops.captureGeneration == nil || ops.poolFor == nil ||
		ops.dsn == nil || ops.open == nil || ops.inspectDatabase == nil || ops.reconcile == nil ||
		ops.retain == nil ||
		ops.release == nil {
		return Inspection{}, errors.New("SQLite inspection operations are unavailable")
	}
	if err := parent.Err(); err != nil {
		return Inspection{}, err
	}
	path, key, err := ops.canonicalize(path)
	if err != nil {
		return Inspection{}, err
	}
	if err := validateProviderInput(path, busyTimeout); err != nil {
		return Inspection{}, errors.Join(errors.New("SQLite inspection path is invalid"), err)
	}
	ctx, cancel := context.WithTimeout(parent, busyTimeout)
	defer cancel()
	contextError := func(err error) error {
		if parentErr := parent.Err(); parentErr != nil {
			return parentErr
		}
		if boundedErr := ctx.Err(); boundedErr != nil {
			return boundedErr
		}
		return err
	}
	if err := ops.validateAncestors(path); err != nil {
		if errors.Is(err, errProviderUnsafeBoundary) {
			return Inspection{}, errors.Join(errInspectionIntegrity, err)
		}
		return Inspection{}, contextError(err)
	}
	if err := contextError(nil); err != nil {
		return Inspection{}, err
	}
	info, statErr := ops.lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		for _, sidecar := range []string{path + "-wal", path + "-shm", path + "-journal"} {
			if _, sidecarErr := ops.lstat(sidecar); sidecarErr == nil {
				return Inspection{}, fmt.Errorf("%w: sidecar exists without database", errInspectionIntegrity)
			} else if !errors.Is(sidecarErr, os.ErrNotExist) {
				return Inspection{}, contextError(sidecarErr)
			}
			if err := contextError(nil); err != nil {
				return Inspection{}, err
			}
		}
		if absenceErr := validateAbsentInspection(ctx, path, ops.lstat); absenceErr != nil {
			return Inspection{}, contextError(absenceErr)
		}
		return Inspection{path: path, key: key}, contextError(nil)
	}
	if statErr != nil {
		return Inspection{}, contextError(statErr)
	}
	if err := contextError(nil); err != nil {
		return Inspection{}, err
	}
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Inspection{}, fmt.Errorf("%w: database endpoint is unsafe", errInspectionIntegrity)
	}
	if secureErr := ops.ensureDirectory(filepath.Dir(path)); secureErr != nil {
		if errors.Is(secureErr, errProviderUnsafeBoundary) {
			return Inspection{}, errors.Join(errInspectionIntegrity, secureErr)
		}
		return Inspection{}, contextError(fmt.Errorf("secure SQLite provider directory: %w", secureErr))
	}
	if err := contextError(nil); err != nil {
		return Inspection{}, err
	}
	if generationErr := ops.validateGeneration(path, true); generationErr != nil {
		if errors.Is(generationErr, errProviderUnsafeBoundary) {
			return Inspection{}, errors.Join(errInspectionIntegrity, generationErr)
		}
		return Inspection{}, contextError(fmt.Errorf("inspect SQLite provider generation: %w", generationErr))
	}
	if err := contextError(nil); err != nil {
		return Inspection{}, err
	}
	expected, identityErr := ops.captureGeneration(path)
	if identityErr != nil {
		return Inspection{}, contextError(identityErr)
	}
	if err := contextError(nil); err != nil {
		return Inspection{}, err
	}

	owner := new(atomic.Bool)
	database, poolErr := ops.poolFor(key, expected, busyTimeout, owner)
	if poolErr != nil {
		return Inspection{}, errors.Join(errInspectionIntegrity, poolErr)
	}
	opened := database == nil
	if opened {
		dsn, dsnErr := ops.dsn(path, busyTimeout)
		if dsnErr != nil {
			return Inspection{}, contextError(dsnErr)
		}
		var openErr error
		database, openErr = ops.open(dsn)
		if openErr != nil {
			return Inspection{}, contextError(openErr)
		}
		if database == nil {
			return Inspection{}, errors.New("SQLite inspection database is unavailable")
		}
		database.SetMaxOpenConns(1)
		database.SetMaxIdleConns(1)
	}
	retained := false
	registered := !opened
	reconcileAttempted := false
	defer func() {
		if retained {
			return
		}
		var cleanupErr error
		if !reconcileAttempted {
			reconcileAttempted = true
			if reconcileErr := ops.reconcile(); reconcileErr != nil {
				resultErr = errors.Join(resultErr, errInspectionInfrastructure, reconcileErr)
			}
		}
		if registered {
			cleanupErr = ops.release(key, database, owner)
		} else {
			cleanupErr = database.Close()
		}
		if cleanupErr != nil {
			resultErr = errors.Join(resultErr, errInspectionInfrastructure, cleanupErr)
		}
	}()
	if err := contextError(nil); err != nil {
		return Inspection{}, err
	}
	current, version, objectCount, inspectErr := ops.inspectDatabase(ctx, path, expected, database)
	if inspectErr != nil {
		return Inspection{}, contextError(inspectErr)
	}
	if !opened && !sameInspectedGeneration(expected, current) {
		return Inspection{}, fmt.Errorf(
			"%w: reused inspection generation changed", errInspectionIntegrity,
		)
	}
	if err := contextError(nil); err != nil {
		return Inspection{}, err
	}
	reconcileAttempted = true
	if reconcileErr := ops.reconcile(); reconcileErr != nil {
		return Inspection{}, errors.Join(errInspectionInfrastructure, reconcileErr)
	}
	if err := contextError(nil); err != nil {
		return Inspection{}, err
	}
	if opened {
		retainedDatabase, retainErr := ops.retain(
			key, database, current, busyTimeout, owner,
		)
		if retainErr != nil {
			return Inspection{}, contextError(retainErr)
		}
		if retainedDatabase == nil {
			return Inspection{}, errors.New("SQLite retained inspection database is unavailable")
		}
		database = retainedDatabase
		registered = true
		if err := contextError(nil); err != nil {
			return Inspection{}, err
		}
	}
	retained = true
	return Inspection{
		Exists: true, Empty: objectCount == 0 && version == 0, Version: version,
		database: database, path: path, key: key, generation: current, released: owner,
	}, nil
}

// Revalidate proves that this exact existing generation or stable missing
// state still matches what Inspect observed. It performs no creation or schema
// work. A caller using InspectClaimed must retain the same exclusive refreshing
// claim guard through reconciliation and this revalidation.
func (inspection Inspection) Revalidate(ctx context.Context) error {
	if ctx == nil || inspection.path == "" || inspection.key == "" {
		return errors.New("SQLite inspection revalidation is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateProviderAncestors(inspection.path); err != nil {
		if errors.Is(err, errProviderUnsafeBoundary) {
			return errors.Join(errInspectionIntegrity, err)
		}
		return err
	}
	if !inspection.Exists {
		return validateAbsentInspection(ctx, inspection.path, os.Lstat)
	}
	if inspection.database == nil || inspection.released == nil || inspection.released.Load() ||
		!inspection.generation.members[0].present {
		return errors.New("SQLite inspection revalidation pool is unavailable")
	}
	if err := SecureGeneration(inspection.path); err != nil {
		if errors.Is(err, errProviderUnsafeBoundary) {
			return errors.Join(errInspectionIntegrity, err)
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := captureInspectedGeneration(inspection.path)
	if err != nil {
		return err
	}
	after, err := captureInspectedGeneration(inspection.path)
	if err != nil {
		return err
	}
	if !sameInspectedGeneration(inspection.generation, current) ||
		!sameInspectedGeneration(current, after) {
		return fmt.Errorf("%w: inspected generation changed", errInspectionIntegrity)
	}
	return ctx.Err()
}

func validateAbsentInspection(
	ctx context.Context,
	path string,
	lstat func(string) (os.FileInfo, error),
) error {
	if ctx == nil || path == "" || lstat == nil {
		return errors.New("SQLite missing-generation validation is unavailable")
	}
	for pass := 0; pass < 2; pass++ {
		for _, member := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := lstat(member); err == nil {
				return fmt.Errorf("%w: missing generation materialized", errInspectionIntegrity)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return ctx.Err()
}

func inspectDatabase(
	ctx context.Context,
	path string,
	expected inspectedGeneration,
	database *sql.DB,
) (inspectedGeneration, int, int, error) {
	return inspectDatabaseWithGeneration(
		ctx, path, expected, database, captureInspectedGeneration, SecureGeneration,
	)
}

func inspectDatabaseWithGeneration(
	ctx context.Context,
	path string,
	expected inspectedGeneration,
	database *sql.DB,
	capture func(string) (inspectedGeneration, error),
	secure func(string) error,
	busyOrLocked ...func(error) bool,
) (inspectedGeneration, int, int, error) {
	if ctx == nil || database == nil || capture == nil || secure == nil {
		return inspectedGeneration{}, 0, 0, errors.New("SQLite inspection database operations are unavailable")
	}
	isBusyOrLocked := IsBusyOrLocked
	if len(busyOrLocked) == 1 && busyOrLocked[0] != nil {
		isBusyOrLocked = busyOrLocked[0]
	}
	if pingErr := database.PingContext(ctx); pingErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return inspectedGeneration{}, 0, 0, ctxErr
		}
		return inspectedGeneration{}, 0, 0, classifyInspectionDatabaseError(
			pingErr, isBusyOrLocked,
		)
	}
	current, identityErr := capture(path)
	if identityErr != nil {
		return inspectedGeneration{}, 0, 0, identityErr
	}
	if !sameInspectedMain(expected, current) {
		return inspectedGeneration{}, 0, 0, fmt.Errorf(
			"%w: database generation changed while inspecting", errInspectionIntegrity,
		)
	}
	if generationErr := secure(path); generationErr != nil {
		if errors.Is(generationErr, errProviderUnsafeBoundary) {
			return inspectedGeneration{}, 0, 0, errors.Join(errInspectionIntegrity, generationErr)
		}
		return inspectedGeneration{}, 0, 0, generationErr
	}
	if integrityErr := CheckIntegrity(ctx, database); integrityErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return inspectedGeneration{}, 0, 0, ctxErr
		}
		return inspectedGeneration{}, 0, 0, classifyInspectionDatabaseError(
			integrityErr,
			isBusyOrLocked,
			errIntegrityCheck,
			errForeignKeyCheck,
			errForeignKeyViolation,
		)
	}
	version, versionErr := SchemaVersion(ctx, database)
	if versionErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return inspectedGeneration{}, 0, 0, ctxErr
		}
		return inspectedGeneration{}, 0, 0, classifyInspectionDatabaseError(
			versionErr, isBusyOrLocked, errInvalidSchemaVersion,
		)
	}
	var objectCount int
	if catalogErr := database.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM main.sqlite_schema WHERE name NOT LIKE 'sqlite_%'`,
	).Scan(&objectCount); catalogErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return inspectedGeneration{}, 0, 0, ctxErr
		}
		return inspectedGeneration{}, 0, 0, classifyInspectionDatabaseError(
			catalogErr, isBusyOrLocked,
		)
	}
	final, identityErr := capture(path)
	if identityErr != nil {
		return inspectedGeneration{}, 0, 0, identityErr
	}
	if !sameInspectedGeneration(current, final) {
		return inspectedGeneration{}, 0, 0, fmt.Errorf(
			"%w: database generation changed while inspecting", errInspectionIntegrity,
		)
	}
	return final, version, objectCount, nil
}

func classifyInspectionDatabaseError(
	err error,
	busyOrLocked func(error) bool,
	provenIntegrity ...error,
) error {
	if busyOrLocked != nil && busyOrLocked(err) {
		return err
	}
	for _, target := range provenIntegrity {
		if errors.Is(err, target) {
			return errors.Join(errInspectionIntegrity, target)
		}
	}
	if isSQLiteIntegrityFailure(err) {
		return errInspectionIntegrity
	}
	return errInspectionUnavailable
}

// HasSchemaObjects checks exact schema-object presence through the retained
// readiness pool; it never opens a second connection pool.
func (inspection Inspection) HasSchemaObjects(
	ctx context.Context,
	objectType string,
	names ...string,
) (bool, error) {
	if err := inspection.validateQuery(ctx); err != nil {
		return false, err
	}
	if !validInspectionObjectType(objectType) || !validInspectionNames(names) {
		return false, errors.New("SQLite inspection schema-object query is invalid")
	}
	for _, name := range names {
		var count int
		if err := inspection.database.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM main.sqlite_schema
			  WHERE type = ? COLLATE BINARY AND name = ? COLLATE BINARY`,
			objectType,
			name,
		).Scan(&count); err != nil {
			return false, err
		}
		if count != 1 {
			return false, nil
		}
	}
	return true, nil
}

// HasTableColumns checks required columns through the retained readiness pool.
func (inspection Inspection) HasTableColumns(
	ctx context.Context,
	table string,
	columns ...string,
) (bool, error) {
	if err := inspection.validateQuery(ctx); err != nil {
		return false, err
	}
	if !validSQLiteSchemaIdentifier(table) || !validInspectionNames(columns) {
		return false, errors.New("SQLite inspection table-column query is invalid")
	}
	var tableCount int
	if err := inspection.database.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM main.sqlite_schema
		  WHERE type = 'table' AND name = ? COLLATE BINARY`,
		table,
	).Scan(&tableCount); err != nil {
		return false, err
	}
	if tableCount != 1 {
		return false, nil
	}
	for _, column := range columns {
		var count int
		if err := inspection.database.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM pragma_table_info(?, 'main')
			  WHERE name = ? COLLATE BINARY`,
			table,
			column,
		).Scan(&count); err != nil {
			return false, err
		}
		if count != 1 {
			return false, nil
		}
	}
	return true, nil
}

// HasImportHorizon reports whether the shared durable import horizon closed
// for the exact domain component through the retained readiness pool.
func (inspection Inspection) HasImportHorizon(ctx context.Context, component string) (bool, error) {
	if err := inspection.validateQuery(ctx); err != nil {
		return false, err
	}
	if !validSQLiteSchemaIdentifier(component) {
		return false, errors.New("SQLite inspection import-horizon query is invalid")
	}
	var tableCount int
	if err := inspection.database.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM main.sqlite_schema
		  WHERE type = 'table' AND name = 'storage_import_horizons' COLLATE BINARY`,
	).Scan(&tableCount); err != nil {
		return false, err
	}
	if tableCount != 1 {
		return false, nil
	}
	var totalColumns, componentColumns, completedColumns int
	if err := inspection.database.QueryRowContext(
		ctx,
		`SELECT
		   COUNT(*),
		   COUNT(*) FILTER (
		       WHERE name = 'component' COLLATE BINARY
		         AND type = 'TEXT' COLLATE NOCASE AND "notnull" = 1 AND pk = 1
		         AND hidden = 0
		   ),
		   COUNT(*) FILTER (
		       WHERE name = 'completed_at' COLLATE BINARY
		         AND type = 'INTEGER' COLLATE NOCASE AND "notnull" = 1 AND pk = 0
		         AND hidden = 0
		   )
		 FROM pragma_table_xinfo('storage_import_horizons', 'main')`,
	).Scan(&totalColumns, &componentColumns, &completedColumns); err != nil {
		return false, err
	}
	if totalColumns != 2 || componentColumns != 1 || completedColumns != 1 {
		return false, fmt.Errorf("%w: import horizon schema is malformed", errInspectionIntegrity)
	}
	var count, validCount int
	if err := inspection.database.QueryRowContext(
		ctx,
		`SELECT COUNT(*),
		        COUNT(*) FILTER (WHERE typeof(completed_at) = 'integer')
		   FROM main.storage_import_horizons
		  WHERE component = ? COLLATE BINARY`,
		component,
	).Scan(&count, &validCount); err != nil {
		return false, err
	}
	if count > 1 || validCount != count {
		return false, fmt.Errorf("%w: malformed import horizon", errInspectionIntegrity)
	}
	return count == 1, nil
}

func (inspection Inspection) validateQuery(ctx context.Context) error {
	if ctx == nil || inspection.database == nil || inspection.released == nil ||
		inspection.released.Load() {
		return errors.New("SQLite provider inspection pool is unavailable")
	}
	return ctx.Err()
}

func validInspectionObjectType(value string) bool {
	switch value {
	case "table", "index", "trigger", "view":
		return true
	default:
		return false
	}
}

func validInspectionNames(values []string) bool {
	if len(values) == 0 || len(values) > maxInspectionSchemaNames {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validSQLiteSchemaIdentifier(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}
