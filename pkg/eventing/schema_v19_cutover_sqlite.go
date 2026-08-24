//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

func validateRetainedSchemaV19(ctx context.Context, conn *sql.Conn) error {
	if err := validateSchemaV1(ctx, conn); err != nil {
		return fmt.Errorf("validate retained event inbox schema: %w", err)
	}
	if err := validateSchemaV2(ctx, conn); err != nil {
		return fmt.Errorf("validate retained workflow revision schema: %w", err)
	}
	return nil
}

// validateLegacySchemaV18 deliberately validates the whole declared v18
// schema before deleting it. A corrupt legacy database must not be silently
// blessed as v19 merely because its contents are being discarded.
func validateLegacySchemaV18(ctx context.Context, conn *sql.Conn) error {
	validators := []func(context.Context, *sql.Conn) error{
		validateSchemaV1,
		validateSchemaV2,
		validateSchemaV3,
		validateSchemaV4,
		validateSchemaV5,
		validateSchemaV6,
		validateSchemaV7,
		validateSchemaV8,
		validateSchemaV9,
		func(ctx context.Context, conn *sql.Conn) error {
			return validateSchemaV10ForVersion(ctx, conn, true, true)
		},
		func(ctx context.Context, conn *sql.Conn) error { return validateSchemaV11ForVersion(ctx, conn, true) },
		validateSchemaV12,
		validateSchemaV13,
		func(ctx context.Context, conn *sql.Conn) error { return validateSchemaV14ForVersion(ctx, conn, true) },
		validateSchemaV15,
		validateSchemaV16,
		func(ctx context.Context, conn *sql.Conn) error { return validateSchemaV17ForVersion(ctx, conn, true) },
		validateSchemaV18,
	}
	for _, validate := range validators {
		if err := validate(ctx, conn); err != nil {
			return err
		}
	}
	return nil
}

func dropLegacyPRSchemaV19(ctx context.Context, conn *sql.Conn) error {
	tables, err := legacyPRTablesV19(ctx, conn)
	if err != nil {
		return err
	}
	remaining := make(map[string]struct{}, len(tables))
	for _, table := range tables {
		remaining[table] = struct{}{}
	}
	for len(remaining) != 0 {
		var droppable []string
		for candidate := range remaining {
			referenced := false
			for child := range remaining {
				if child == candidate {
					continue
				}
				parents, err := foreignKeyParentsV19(ctx, conn, child)
				if err != nil {
					return err
				}
				if _, ok := parents[candidate]; ok {
					referenced = true
					break
				}
			}
			if !referenced {
				droppable = append(droppable, candidate)
			}
		}
		if len(droppable) == 0 {
			return fmt.Errorf("%w: legacy pull request tables contain a foreign-key cycle", ErrSchemaInvalid)
		}
		sort.Strings(droppable)
		for _, table := range droppable {
			if _, err := conn.ExecContext(ctx, "DROP TABLE "+quoteSQLiteIdentifierV19(table)); err != nil {
				return fmt.Errorf("drop %s: %w", table, err)
			}
			delete(remaining, table)
		}
	}
	return nil
}

// dropAllPRWorkspaceSchemaV20 performs the explicitly destructive development
// workspace cutover. Generic event and workflow tables are deliberately outside
// the pr_* namespace and remain intact.
func dropAllPRWorkspaceSchemaV20(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master
		WHERE type = 'table' AND lower(name) GLOB 'pr_*' ORDER BY name`)
	if err != nil {
		return err
	}
	defer func() {
		_ = rows.Close()
	}()
	var tables []string
	for rows.Next() {
		var table string
		if scanErr := rows.Scan(&table); scanErr != nil {
			return scanErr
		}
		tables = append(tables, table)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return rowsErr
	}
	if closeErr := rows.Close(); closeErr != nil {
		return closeErr
	}
	remaining := make(map[string]struct{}, len(tables))
	for _, table := range tables {
		remaining[table] = struct{}{}
	}
	for len(remaining) > 0 {
		var droppable []string
		for candidate := range remaining {
			referenced := false
			for child := range remaining {
				if child == candidate {
					continue
				}
				parents, parentErr := foreignKeyParentsV19(ctx, conn, child)
				if parentErr != nil {
					return parentErr
				}
				if _, ok := parents[candidate]; ok {
					referenced = true
					break
				}
			}
			if !referenced {
				droppable = append(droppable, candidate)
			}
		}
		if len(droppable) == 0 {
			return fmt.Errorf("%w: development tables contain a foreign-key cycle", ErrSchemaInvalid)
		}
		sort.Strings(droppable)
		for _, table := range droppable {
			if _, dropErr := conn.ExecContext(ctx, "DROP TABLE "+quoteSQLiteIdentifierV19(table)); dropErr != nil {
				return fmt.Errorf("drop %s: %w", table, dropErr)
			}
			delete(remaining, table)
		}
	}
	return nil
}

func validateLegacyPRSchemaAbsentV19(ctx context.Context, conn *sql.Conn) error {
	tables, err := legacyPRTablesV19(ctx, conn)
	if err != nil {
		return err
	}
	if len(tables) != 0 {
		return schemaErrorf(tables[0], "legacy pull request schema remains after v19 cutover")
	}
	return nil
}

func legacyPRTablesV19(ctx context.Context, conn *sql.Conn) ([]string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master
		WHERE type = 'table' AND
		      (lower(name) GLOB 'pr_review_*' OR lower(name) GLOB 'pr_development_*')
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	return tables, rows.Err()
}

func foreignKeyParentsV19(ctx context.Context, conn *sql.Conn, table string) (map[string]struct{}, error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_list("+quoteSQLiteStringLiteral(table)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	parents := make(map[string]struct{})
	for rows.Next() {
		var id, sequence int
		var parent, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &parent, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return nil, err
		}
		parents[parent] = struct{}{}
	}
	return parents, rows.Err()
}

func quoteSQLiteIdentifierV19(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
