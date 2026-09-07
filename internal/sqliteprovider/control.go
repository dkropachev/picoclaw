// This file owns SQLite-specific control statements and schema catalog queries
// for the single-owner database provider.
package sqliteprovider

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

const maxSQLiteSchemaVersion int64 = 1<<31 - 1

var (
	errInvalidControlBoundary = errors.New("SQLite provider control boundary is invalid")
	errInvalidSchemaVersion   = errors.New("SQLite provider schema version is invalid")
	errIntegrityCheck         = errors.New("SQLite provider integrity check failed")
	errForeignKeyCheck        = errors.New("SQLite provider foreign-key check failed")
	errForeignKeyViolation    = errors.New("SQLite provider reported a foreign-key violation")
)

type controlQueryer interface {
	QueryContext(ctx context.Context, query string, arguments ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, arguments ...any) *sql.Row
}

type controlExecer interface {
	ExecContext(ctx context.Context, query string, arguments ...any) (sql.Result, error)
}

// SchemaVersion returns the main database schema-version control value.
func SchemaVersion(ctx context.Context, queryer controlQueryer) (int, error) {
	if ctx == nil || queryer == nil {
		return 0, errInvalidControlBoundary
	}
	var version int64
	if err := queryer.QueryRowContext(ctx, "PRAGMA main.user_version").Scan(&version); err != nil {
		return 0, err
	}
	if version < 0 || version > maxSQLiteSchemaVersion {
		return 0, errInvalidSchemaVersion
	}
	return int(version), nil
}

// SetSchemaVersion changes the main database schema-version control value.
// Callers must already own the required migration transaction and fence.
func SetSchemaVersion(ctx context.Context, execer controlExecer, version int) error {
	if ctx == nil || execer == nil {
		return errInvalidControlBoundary
	}
	if version < 0 || int64(version) > maxSQLiteSchemaVersion {
		return errInvalidSchemaVersion
	}
	_, err := execer.ExecContext(ctx, "PRAGMA main.user_version = "+strconv.Itoa(version))
	return err
}

// CheckIntegrity runs both physical and referential checks against the main
// database. Diagnostic details from SQLite are deliberately not returned.
func CheckIntegrity(ctx context.Context, queryer controlQueryer) error {
	if ctx == nil || queryer == nil {
		return errInvalidControlBoundary
	}
	if err := CheckIntegrityOnly(ctx, queryer); err != nil {
		return err
	}
	return CheckForeignKeys(ctx, queryer)
}

// CheckIntegrityOnly runs the main database physical integrity diagnostic.
// Diagnostic details from SQLite are deliberately not returned.
func CheckIntegrityOnly(ctx context.Context, queryer controlQueryer) error {
	if ctx == nil || queryer == nil {
		return errInvalidControlBoundary
	}
	var result string
	if err := queryer.QueryRowContext(ctx, "PRAGMA main.integrity_check(1)").Scan(&result); err != nil {
		return controlDiagnosticError(ctx, errIntegrityCheck)
	}
	if result != "ok" {
		return errIntegrityCheck
	}
	return nil
}

// CheckForeignKeys runs the main database referential-integrity diagnostic.
// Row details and SQLite diagnostics are deliberately not returned.
func CheckForeignKeys(ctx context.Context, queryer controlQueryer) error {
	if ctx == nil || queryer == nil {
		return errInvalidControlBoundary
	}
	rows, err := queryer.QueryContext(ctx, "PRAGMA main.foreign_key_check")
	if err != nil {
		return controlDiagnosticError(ctx, errForeignKeyCheck)
	}
	defer rows.Close()
	if rows.Next() {
		return errForeignKeyViolation
	}
	if err := rows.Err(); err != nil {
		return controlDiagnosticError(ctx, errForeignKeyCheck)
	}
	return nil
}

func controlDiagnosticError(ctx context.Context, fallback error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fallback
}
