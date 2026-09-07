package sqliteprovider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxSQLiteSchemaIdentifierBytes = 1024
	maxSQLiteExpectedUniqueIndexes = 256
)

type schemaQueryer interface {
	QueryRowContext(ctx context.Context, query string, arguments ...any) *sql.Row
}

// ValidateUniqueIndexes requires a main-schema table to have exactly the named
// set of manually-created unique indexes. PRIMARY KEY and inline UNIQUE
// auto-indexes are represented by table DDL and are intentionally ignored.
func ValidateUniqueIndexes(
	ctx context.Context,
	queryer schemaQueryer,
	table string,
	expected ...string,
) error {
	if ctx == nil || queryer == nil || !validSQLiteSchemaIdentifier(table) ||
		len(expected) > maxSQLiteExpectedUniqueIndexes {
		return errors.New("SQLite provider unique-index validation is invalid")
	}

	seen := make(map[string]struct{}, len(expected))
	for _, name := range expected {
		if !validSQLiteSchemaIdentifier(name) {
			return errors.New("SQLite provider expected unique-index name is invalid")
		}
		if _, duplicate := seen[name]; duplicate {
			return errors.New("SQLite provider expected unique-index name is duplicated")
		}
		seen[name] = struct{}{}
	}

	var tableCount int
	if err := queryer.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM main.sqlite_schema WHERE type = 'table' AND name = ? COLLATE BINARY`,
		table,
	).Scan(&tableCount); err != nil {
		return fmt.Errorf("inspect SQLite provider table: %w", err)
	}
	if tableCount != 1 {
		return errors.New("required SQLite provider table is missing")
	}

	for _, name := range expected {
		var count int
		if err := queryer.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM pragma_index_list(?, 'main')
			  WHERE name = ? COLLATE BINARY AND "unique" = 1 AND origin = 'c'`,
			table,
			name,
		).Scan(&count); err != nil {
			return fmt.Errorf("inspect SQLite provider index: %w", err)
		}
		if count != 1 {
			return errors.New("required SQLite provider unique index is missing")
		}
	}

	var total int
	if err := queryer.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM pragma_index_list(?, 'main')
		  WHERE "unique" = 1 AND origin = 'c'`,
		table,
	).Scan(&total); err != nil {
		return fmt.Errorf("inspect SQLite provider unique indexes: %w", err)
	}
	if total != len(expected) {
		return errors.New("unexpected SQLite provider unique index exists")
	}
	return nil
}

func validSQLiteSchemaIdentifier(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		len(value) <= maxSQLiteSchemaIdentifierBytes && !strings.ContainsRune(value, 0)
}
