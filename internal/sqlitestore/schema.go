package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// migrationStatementForbidden rejects transaction control and connection or
// database attachment statements even when they appear after comments or as a
// later statement in one Exec string. Migration.Apply is trusted Go code, but
// declarative migration SQL must not be able to escape the helper's immediate
// transaction or alter its connection contract.
func migrationStatementForbidden(statement string) (bool, error) {
	tokens, err := sqliteSQLTokens(statement)
	if err != nil {
		return false, err
	}
	if len(tokens) == 0 {
		return true, nil
	}
	atStart := true
	for _, token := range tokens {
		if token == ";" {
			atStart = true
			continue
		}
		if !atStart {
			continue
		}
		switch token {
		case "begin", "commit", "end", "rollback", "savepoint", "release",
			"pragma", "attach", "detach", "vacuum":
			return true, nil
		default:
			atStart = false
		}
	}
	return false, nil
}

// ValidateSchemaObject requires one table, index, trigger, or view to have the
// exact token-aware defining DDL supplied by the subsystem. Harmless SQL
// whitespace, comments, keyword case, trailing semicolons, and IF NOT EXISTS
// do not affect comparison; literals, constraints, ordering, and predicates do.
func ValidateSchemaObject(
	ctx context.Context,
	conn *sql.Conn,
	objectType,
	name,
	expected string,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if conn == nil || strings.TrimSpace(name) == "" || strings.ContainsRune(name, '\x00') ||
		strings.TrimSpace(expected) == "" {
		return errors.New("SQLite schema object validation is invalid")
	}
	switch objectType {
	case "table", "index", "trigger", "view":
	default:
		return errors.New("SQLite schema object type is invalid")
	}
	var actual sql.NullString
	err := conn.QueryRowContext(
		ctx,
		`SELECT sql FROM sqlite_schema WHERE type = ? AND name = ?`,
		objectType,
		name,
	).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("required %s %s is missing", objectType, name)
	}
	if err != nil {
		return fmt.Errorf("inspect schema object %s: %w", name, err)
	}
	if !actual.Valid || strings.TrimSpace(actual.String) == "" {
		return fmt.Errorf("schema object %s has no defining SQL", name)
	}
	canonicalActual, err := canonicalSQLiteSQL(actual.String)
	if err != nil {
		return fmt.Errorf("canonicalize schema object %s: %w", name, err)
	}
	canonicalExpected, err := canonicalSQLiteSQL(expected)
	if err != nil {
		return fmt.Errorf("canonicalize required schema object %s: %w", name, err)
	}
	if canonicalActual != canonicalExpected {
		return fmt.Errorf("schema object %s differs from its required definition", name)
	}
	return nil
}

// ValidateUniqueIndexSet rejects any manually-created unique index not named in
// expected and also requires every expected name to exist as a manual unique
// index. PRIMARY KEY and inline UNIQUE auto-indexes are validated by their
// table DDL and are intentionally excluded from expected.
func ValidateUniqueIndexSet(
	ctx context.Context,
	conn *sql.Conn,
	table string,
	expected ...string,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if conn == nil || strings.TrimSpace(table) == "" || strings.ContainsRune(table, '\x00') {
		return errors.New("SQLite unique-index validation is invalid")
	}
	expected = append([]string(nil), expected...)
	for _, name := range expected {
		if strings.TrimSpace(name) == "" || strings.ContainsRune(name, '\x00') {
			return errors.New("SQLite expected unique-index name is invalid")
		}
	}
	sort.Strings(expected)
	for index := 1; index < len(expected); index++ {
		if expected[index-1] == expected[index] {
			return errors.New("SQLite expected unique-index name is duplicated")
		}
	}
	for _, name := range expected {
		var count int
		if err := conn.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM pragma_index_list(?)
			  WHERE name = ? AND "unique" = 1 AND origin = 'c'`,
			table,
			name,
		).Scan(&count); err != nil {
			return fmt.Errorf("inspect table %s index %s: %w", table, name, err)
		}
		if count != 1 {
			return fmt.Errorf("table %s is missing manual unique index %s", table, name)
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
	if err := conn.QueryRowContext(ctx, query, arguments...).Scan(&unexpected); err != nil {
		return fmt.Errorf("inspect table %s unique indexes: %w", table, err)
	}
	if unexpected != 0 {
		return fmt.Errorf("table %s has %d unexpected manual unique indexes", table, unexpected)
	}
	return nil
}

func canonicalSQLiteSQL(statement string) (string, error) {
	tokens, err := sqliteSQLTokens(statement)
	if err != nil {
		return "", err
	}
	for len(tokens) > 0 && tokens[len(tokens)-1] == ";" {
		tokens = tokens[:len(tokens)-1]
	}
	if len(tokens) >= 5 && tokens[0] == "create" &&
		(tokens[1] == "table" || tokens[1] == "index") &&
		tokens[2] == "if" && tokens[3] == "not" && tokens[4] == "exists" {
		tokens = append(tokens[:2], tokens[5:]...)
	} else if len(tokens) >= 6 && tokens[0] == "create" && tokens[1] == "unique" &&
		tokens[2] == "index" && tokens[3] == "if" && tokens[4] == "not" &&
		tokens[5] == "exists" {
		tokens = append(tokens[:3], tokens[6:]...)
	}
	var canonical strings.Builder
	for _, token := range tokens {
		canonical.WriteString(strconv.Itoa(len(token)))
		canonical.WriteByte(':')
		canonical.WriteString(token)
	}
	return canonical.String(), nil
}

func sqliteSQLTokens(statement string) ([]string, error) {
	if !utf8.ValidString(statement) {
		return nil, errors.New("SQL is not valid UTF-8")
	}
	tokens := make([]string, 0, len(statement)/4)
	for offset := 0; offset < len(statement); {
		character, size := utf8.DecodeRuneInString(statement[offset:])
		if sqliteSQLWhitespace(character) {
			offset += size
			continue
		}
		if strings.HasPrefix(statement[offset:], "--") {
			newline := strings.IndexByte(statement[offset+2:], '\n')
			if newline < 0 {
				offset = len(statement)
			} else {
				offset += 2 + newline
			}
			continue
		}
		if strings.HasPrefix(statement[offset:], "/*") {
			end := strings.Index(statement[offset+2:], "*/")
			if end < 0 {
				return nil, errors.New("unterminated SQL block comment")
			}
			offset += 2 + end + 2
			continue
		}
		if strings.ContainsRune("'\"`[", character) {
			token, next, err := readSQLiteQuotedToken(statement, offset)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token)
			offset = next
			continue
		}
		if sqliteSQLWordRune(character) {
			var token strings.Builder
			for offset < len(statement) {
				character, size = utf8.DecodeRuneInString(statement[offset:])
				if !sqliteSQLWordRune(character) {
					break
				}
				token.WriteRune(lowerSQLiteASCII(character))
				offset += size
			}
			tokens = append(tokens, token.String())
			continue
		}
		operator := ""
		for _, candidate := range []string{
			"->>", "||", "->", "<=", ">=", "!=", "==", "<>", "<<", ">>",
		} {
			if strings.HasPrefix(statement[offset:], candidate) {
				operator = candidate
				break
			}
		}
		if operator == "" {
			operator = statement[offset : offset+size]
		}
		tokens = append(tokens, operator)
		offset += len(operator)
	}
	return tokens, nil
}

func readSQLiteQuotedToken(statement string, offset int) (string, int, error) {
	start := offset
	delimiter := statement[offset]
	if delimiter == '[' {
		delimiter = ']'
	}
	offset++
	for offset < len(statement) {
		if statement[offset] != delimiter {
			offset++
			continue
		}
		offset++
		if offset < len(statement) && statement[offset] == delimiter {
			offset++
			continue
		}
		return statement[start:offset], offset, nil
	}
	return "", 0, fmt.Errorf("unterminated %q SQL token", string(statement[start]))
}

func sqliteSQLWordRune(character rune) bool {
	return character == '_' || character == '$' || character >= utf8.RuneSelf ||
		character >= 'a' && character <= 'z' ||
		character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9'
}

func sqliteSQLWhitespace(character rune) bool {
	return character == ' ' || character == '\t' || character == '\n' ||
		character == '\f' || character == '\r'
}

func lowerSQLiteASCII(character rune) rune {
	if character >= 'A' && character <= 'Z' {
		return character + ('a' - 'A')
	}
	return character
}
