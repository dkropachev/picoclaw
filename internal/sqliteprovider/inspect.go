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

var errInspectionIntegrity = errors.New("SQLite provider integrity failure")

// Inspection is provider-private physical readiness metadata.
type Inspection struct {
	Exists   bool
	Empty    bool
	Version  int
	database *sql.DB
	path     string
	released *atomic.Bool
}

// Release closes this readiness pool only if no store owner adopted it.
func (inspection Inspection) Release() error {
	if inspection.database == nil {
		return nil
	}
	if inspection.released == nil || !inspection.released.CompareAndSwap(false, true) {
		return nil
	}
	return releaseInspectedPool(inspection.path, inspection.database, inspection.released)
}

// Adopt transfers this sole live inspection reference to the trusted store
// owner. Unlike OpenStore, it cannot be selected by path alone.
func (inspection Inspection) Adopt(busyTimeout time.Duration) (*sql.DB, error) {
	if inspection.database == nil || inspection.released == nil || inspection.released.Load() {
		return nil, errors.New("SQLite provider inspection pool is unavailable")
	}
	return adoptInspectedPool(
		inspection.path, busyTimeout, inspection.released,
	)
}

// IsInspectionIntegrity distinguishes damaged generations from temporary
// unavailability without exposing driver errors to domain clients.
func IsInspectionIntegrity(err error) bool { return errors.Is(err, errInspectionIntegrity) }

// Inspect validates an existing generation without creating a missing store or
// applying schema work. Existing sidecars are checked before SQLite opens.
func Inspect(ctx context.Context, path string, busyTimeout time.Duration) (Inspection, error) {
	if ctx == nil {
		return Inspection{}, errors.New("SQLite inspection context is unavailable")
	}
	if err := validateProviderInput(path, busyTimeout); err != nil || path == ":memory:" {
		return Inspection{}, errors.Join(errors.New("SQLite inspection input is invalid"), err)
	}
	if err := ctx.Err(); err != nil {
		return Inspection{}, err
	}
	if err := validateProviderAncestors(path); err != nil {
		return Inspection{}, err
	}
	info, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		for _, sidecar := range []string{path + "-wal", path + "-shm", path + "-journal"} {
			if _, sidecarErr := os.Lstat(sidecar); sidecarErr == nil {
				return Inspection{}, fmt.Errorf("%w: sidecar exists without database", errInspectionIntegrity)
			} else if !errors.Is(sidecarErr, os.ErrNotExist) {
				return Inspection{}, sidecarErr
			}
		}
		return Inspection{}, nil
	}
	if statErr != nil {
		return Inspection{}, statErr
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Inspection{}, fmt.Errorf("%w: database endpoint is unsafe", errInspectionIntegrity)
	}
	if secureErr := EnsurePrivateDirectory(filepath.Dir(path)); secureErr != nil {
		return Inspection{}, fmt.Errorf("secure SQLite provider directory: %w", secureErr)
	}
	if generationErr := validateGenerationMembers(path, true); generationErr != nil {
		return Inspection{}, fmt.Errorf("%w: %v", errInspectionIntegrity, generationErr)
	}
	owner := new(atomic.Bool)
	database, poolErr := inspectedPoolFor(path, info, busyTimeout, owner)
	if poolErr != nil {
		return Inspection{}, fmt.Errorf("%w: %v", errInspectionIntegrity, poolErr)
	}
	opened := database == nil
	if opened {
		dsn, dsnErr := DSN(path, busyTimeout)
		if dsnErr != nil {
			return Inspection{}, dsnErr
		}
		var openErr error
		database, openErr = open(dsn)
		if openErr != nil {
			return Inspection{}, openErr
		}
		database.SetMaxOpenConns(1)
		database.SetMaxIdleConns(1)
	}
	retained := false
	defer func() {
		if retained {
			return
		}
		if opened {
			_ = database.Close()
		} else {
			_ = releaseInspectedPool(path, database, owner)
		}
	}()
	current, version, objectCount, inspectErr := inspectDatabase(ctx, path, info, database)
	if inspectErr != nil {
		return Inspection{}, inspectErr
	}
	if opened {
		retainedDatabase, retainErr := retainInspectedPool(
			path, database, current, busyTimeout, owner,
		)
		if retainErr != nil {
			return Inspection{}, retainErr
		}
		database = retainedDatabase
	}
	retained = true
	return Inspection{
		Exists: true, Empty: objectCount == 0 && version == 0, Version: version,
		database: database, path: path, released: owner,
	}, nil
}

func inspectDatabase(
	ctx context.Context,
	path string,
	expected os.FileInfo,
	database *sql.DB,
) (os.FileInfo, int, int, error) {
	return inspectDatabaseWithGeneration(
		ctx, path, expected, database, os.Lstat, SecureGeneration,
	)
}

func inspectDatabaseWithGeneration(
	ctx context.Context,
	path string,
	expected os.FileInfo,
	database *sql.DB,
	lstat func(string) (os.FileInfo, error),
	secure func(string) error,
	busyOrLocked ...func(error) bool,
) (os.FileInfo, int, int, error) {
	isBusyOrLocked := IsBusyOrLocked
	if len(busyOrLocked) == 1 && busyOrLocked[0] != nil {
		isBusyOrLocked = busyOrLocked[0]
	}
	if pingErr := database.PingContext(ctx); pingErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, 0, ctxErr
		}
		if isBusyOrLocked(pingErr) {
			return nil, 0, 0, pingErr
		}
		return nil, 0, 0, fmt.Errorf("%w: provider open failed", errInspectionIntegrity)
	}
	current, statErr := lstat(path)
	if statErr != nil || current == nil || !current.Mode().IsRegular() ||
		current.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, current) {
		return nil, 0, 0, fmt.Errorf("%w: database generation changed while inspecting", errInspectionIntegrity)
	}
	if generationErr := secure(path); generationErr != nil {
		return nil, 0, 0, fmt.Errorf("%w: %v", errInspectionIntegrity, generationErr)
	}
	if integrityErr := CheckIntegrity(ctx, database); integrityErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, 0, ctxErr
		}
		if isBusyOrLocked(integrityErr) {
			return nil, 0, 0, integrityErr
		}
		return nil, 0, 0, fmt.Errorf("%w: provider integrity check failed", errInspectionIntegrity)
	}
	version, versionErr := SchemaVersion(ctx, database)
	if versionErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, 0, ctxErr
		}
		if isBusyOrLocked(versionErr) {
			return nil, 0, 0, versionErr
		}
		return nil, 0, 0, fmt.Errorf("%w: schema version unavailable", errInspectionIntegrity)
	}
	var objectCount int
	if catalogErr := database.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`,
	).Scan(&objectCount); catalogErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, 0, ctxErr
		}
		if isBusyOrLocked(catalogErr) {
			return nil, 0, 0, catalogErr
		}
		return nil, 0, 0, fmt.Errorf("%w: schema catalog unavailable", errInspectionIntegrity)
	}
	return current, version, objectCount, nil
}

// HasSchemaObjects checks exact schema-object presence through the retained
// readiness pool; it never opens a second connection pool.
func (inspection Inspection) HasSchemaObjects(
	ctx context.Context,
	objectType string,
	names ...string,
) (bool, error) {
	if ctx == nil || inspection.database == nil || inspection.released == nil ||
		inspection.released.Load() {
		return false, errors.New("SQLite provider inspection pool is unavailable")
	}
	for _, name := range names {
		var count int
		if err := inspection.database.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM sqlite_schema WHERE type=? AND name=?`,
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
	if ctx == nil || inspection.database == nil || inspection.released == nil ||
		inspection.released.Load() {
		return false, errors.New("SQLite provider inspection pool is unavailable")
	}
	var tableCount int
	if err := inspection.database.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`,
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
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`,
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
	if ctx == nil || inspection.database == nil || inspection.released == nil ||
		inspection.released.Load() {
		return false, errors.New("SQLite provider inspection pool is unavailable")
	}
	var tableCount int
	if err := inspection.database.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='storage_import_horizons'`,
	).Scan(&tableCount); err != nil {
		return false, err
	}
	if tableCount != 1 {
		return false, nil
	}
	var count int
	if err := inspection.database.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM storage_import_horizons WHERE component=?`,
		component,
	).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}
