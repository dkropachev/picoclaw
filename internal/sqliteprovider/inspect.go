package sqliteprovider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
}

// Release closes this readiness pool only if no store owner adopted it.
func (inspection Inspection) Release() error {
	if inspection.database == nil {
		return nil
	}
	return releaseInspectedPool(inspection.path, inspection.database)
}

// IsInspectionIntegrity distinguishes damaged generations from temporary
// unavailability without exposing driver errors to domain clients.
func IsInspectionIntegrity(err error) bool { return errors.Is(err, errInspectionIntegrity) }

// Inspect validates an existing generation without creating a missing store or
// applying schema work. Existing sidecars are checked before SQLite opens.
func Inspect(ctx context.Context, path string, busyTimeout time.Duration) (Inspection, error) {
	if ctx == nil {
		ctx = context.Background()
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
	database, poolErr := inspectedPoolFor(path, info)
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
	}
	retained := false
	defer func() {
		if opened && !retained {
			_ = database.Close()
		}
	}()
	if pingErr := database.PingContext(ctx); pingErr != nil {
		if IsBusyOrLocked(pingErr) {
			return Inspection{}, pingErr
		}
		return Inspection{}, fmt.Errorf("%w: provider open failed", errInspectionIntegrity)
	}
	current, statErr := os.Lstat(path)
	if statErr != nil || current == nil || !current.Mode().IsRegular() ||
		current.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, current) {
		return Inspection{}, fmt.Errorf("%w: database generation changed while inspecting", errInspectionIntegrity)
	}
	if generationErr := SecureGeneration(path); generationErr != nil {
		return Inspection{}, fmt.Errorf("%w: %v", errInspectionIntegrity, generationErr)
	}
	if integrityErr := inspectIntegrity(ctx, database); integrityErr != nil {
		return Inspection{}, integrityErr
	}
	var version int
	if versionErr := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); versionErr != nil {
		return Inspection{}, fmt.Errorf("%w: schema version unavailable", errInspectionIntegrity)
	}
	var objectCount int
	if catalogErr := database.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`,
	).Scan(&objectCount); catalogErr != nil {
		return Inspection{}, fmt.Errorf("%w: schema catalog unavailable", errInspectionIntegrity)
	}
	if opened {
		var retainErr error
		database, retainErr = retainInspectedPool(path, database, current)
		if retainErr != nil {
			return Inspection{}, retainErr
		}
	}
	retained = true
	return Inspection{
		Exists: true, Empty: objectCount == 0 && version == 0, Version: version,
		database: database, path: path,
	}, nil
}

// HasSchemaObjects checks exact schema-object presence through the retained
// readiness pool; it never opens a second connection pool.
func (inspection Inspection) HasSchemaObjects(ctx context.Context, names ...string) (bool, error) {
	if inspection.database == nil {
		return false, errors.New("SQLite provider inspection pool is unavailable")
	}
	for _, name := range names {
		var count int
		if err := inspection.database.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`,
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
	if inspection.database == nil {
		return false, errors.New("SQLite provider inspection pool is unavailable")
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
	if inspection.database == nil {
		return false, errors.New("SQLite provider inspection pool is unavailable")
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

func inspectIntegrity(ctx context.Context, database *sql.DB) error {
	var result string
	if err := database.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&result); err != nil {
		return fmt.Errorf("%w: integrity check failed", errInspectionIntegrity)
	}
	if result != "ok" {
		return fmt.Errorf("%w: corruption reported", errInspectionIntegrity)
	}
	rows, err := database.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("%w: foreign-key check failed", errInspectionIntegrity)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%w: foreign-key violation reported", errInspectionIntegrity)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: foreign-key check failed", errInspectionIntegrity)
	}
	return nil
}

// HasSchemaObjects reports whether an existing generation contains every
// named schema object. It performs no repair or schema mutation.
func HasSchemaObjects(
	ctx context.Context,
	path string,
	busyTimeout time.Duration,
	names ...string,
) (bool, error) {
	if err := SecureGeneration(path); err != nil {
		return false, err
	}
	dsn, err := DSN(path, busyTimeout)
	if err != nil {
		return false, err
	}
	database, err := open(dsn)
	if err != nil {
		return false, err
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer database.Close()
	for _, name := range names {
		var count int
		if err := database.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`,
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

// HasTableColumns reports whether a table contains each named column without
// changing the schema.
func HasTableColumns(
	ctx context.Context,
	path string,
	busyTimeout time.Duration,
	table string,
	columns ...string,
) (bool, error) {
	if err := SecureGeneration(path); err != nil {
		return false, err
	}
	dsn, err := DSN(path, busyTimeout)
	if err != nil {
		return false, err
	}
	database, err := open(dsn)
	if err != nil {
		return false, err
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer database.Close()
	for _, column := range columns {
		var count int
		if err := database.QueryRowContext(
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
