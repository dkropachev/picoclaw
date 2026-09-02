package sqliteprovider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

type controlQueryer interface {
	QueryContext(ctx context.Context, query string, arguments ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, arguments ...any) *sql.Row
}

type controlExecer interface {
	ExecContext(ctx context.Context, query string, arguments ...any) (sql.Result, error)
}

// SchemaVersion returns the provider schema-version control value.
func SchemaVersion(ctx context.Context, queryer controlQueryer) (int, error) {
	if queryer == nil {
		return 0, errors.New("SQLite provider query boundary is unavailable")
	}
	var version int
	if err := queryer.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

// SetSchemaVersion changes the provider schema-version control value inside a
// broker-owned migration transaction.
func SetSchemaVersion(ctx context.Context, execer controlExecer, version int) error {
	if execer == nil || version < 0 {
		return errors.New("SQLite provider schema version is invalid")
	}
	_, err := execer.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(version))
	return err
}

// CheckIntegrity runs both physical and referential provider checks.
func CheckIntegrity(ctx context.Context, queryer controlQueryer) error {
	if queryer == nil {
		return errors.New("SQLite provider query boundary is unavailable")
	}
	if err := CheckIntegrityOnly(ctx, queryer); err != nil {
		return err
	}
	return CheckForeignKeys(ctx, queryer)
}

// CheckIntegrityOnly runs the provider's physical integrity diagnostic.
func CheckIntegrityOnly(ctx context.Context, queryer controlQueryer) error {
	if queryer == nil {
		return errors.New("SQLite provider query boundary is unavailable")
	}
	var result string
	if err := queryer.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("SQLite provider reported corruption")
	}
	return nil
}

// CheckForeignKeys runs the provider's referential-integrity diagnostic.
func CheckForeignKeys(ctx context.Context, queryer controlQueryer) error {
	if queryer == nil {
		return errors.New("SQLite provider query boundary is unavailable")
	}
	rows, err := queryer.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("SQLite provider reported a foreign-key violation")
	}
	return rows.Err()
}
