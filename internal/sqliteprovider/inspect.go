package sqliteprovider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

var errInspectionIntegrity = errors.New("SQLite provider integrity failure")

// Inspection is provider-private physical readiness metadata.
type Inspection struct {
	Exists  bool
	Empty   bool
	Version int
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
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		for _, sidecar := range []string{path + "-wal", path + "-shm", path + "-journal"} {
			if _, sidecarErr := os.Lstat(sidecar); sidecarErr == nil {
				return Inspection{}, fmt.Errorf("%w: sidecar exists without database", errInspectionIntegrity)
			} else if !errors.Is(sidecarErr, os.ErrNotExist) {
				return Inspection{}, sidecarErr
			}
		}
		return Inspection{}, nil
	}
	if err != nil {
		return Inspection{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Inspection{}, fmt.Errorf("%w: database endpoint is unsafe", errInspectionIntegrity)
	}
	if generationErr := validateGenerationMembers(path, true); generationErr != nil {
		return Inspection{}, fmt.Errorf("%w: %v", errInspectionIntegrity, generationErr)
	}
	dsn, err := DSN(path, busyTimeout)
	if err != nil {
		return Inspection{}, err
	}
	database, err := open(dsn)
	if err != nil {
		return Inspection{}, err
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		if IsBusyOrLocked(err) {
			return Inspection{}, err
		}
		return Inspection{}, fmt.Errorf("%w: provider open failed", errInspectionIntegrity)
	}
	if err := inspectIntegrity(ctx, database); err != nil {
		return Inspection{}, err
	}
	var version int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return Inspection{}, fmt.Errorf("%w: schema version unavailable", errInspectionIntegrity)
	}
	var objectCount int
	if err := database.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`,
	).Scan(&objectCount); err != nil {
		return Inspection{}, fmt.Errorf("%w: schema catalog unavailable", errInspectionIntegrity)
	}
	return Inspection{Exists: true, Empty: objectCount == 0 && version == 0, Version: version}, nil
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
