package sqliteprovider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type schemaQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ValidateUniqueIndexes owns the SQLite-specific index catalog query used by
// broker-side schema adapters.
func ValidateUniqueIndexes(
	ctx context.Context,
	queryer schemaQueryer,
	table string,
	expected ...string,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if queryer == nil || strings.TrimSpace(table) == "" || strings.ContainsRune(table, 0) {
		return errors.New("SQLite provider unique-index validation is invalid")
	}
	expected = append([]string(nil), expected...)
	for _, name := range expected {
		if strings.TrimSpace(name) == "" || strings.ContainsRune(name, 0) {
			return errors.New("SQLite provider expected unique-index name is invalid")
		}
	}
	sort.Strings(expected)
	for index := 1; index < len(expected); index++ {
		if expected[index-1] == expected[index] {
			return errors.New("SQLite provider expected unique-index name is duplicated")
		}
	}
	for _, name := range expected {
		var count int
		if err := queryer.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM pragma_index_list(?)
			  WHERE name = ? AND "unique" = 1 AND origin = 'c'`,
			table,
			name,
		).Scan(&count); err != nil {
			return fmt.Errorf("inspect SQLite provider index: %w", err)
		}
		if count != 1 {
			return errors.New("required SQLite provider unique index is missing")
		}
	}
	query := `SELECT COUNT(*) FROM pragma_index_list(?)
		WHERE "unique" = 1 AND origin = 'c'`
	arguments := []any{table}
	if len(expected) > 0 {
		query += " AND name NOT IN (" + strings.TrimRight(strings.Repeat("?,", len(expected)), ",") + ")"
		for _, name := range expected {
			arguments = append(arguments, name)
		}
	}
	var unexpected int
	if err := queryer.QueryRowContext(ctx, query, arguments...).Scan(&unexpected); err != nil {
		return fmt.Errorf("inspect SQLite provider unique indexes: %w", err)
	}
	if unexpected != 0 {
		return errors.New("unexpected SQLite provider unique index exists")
	}
	return nil
}
