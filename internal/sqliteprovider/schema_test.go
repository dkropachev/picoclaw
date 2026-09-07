package sqliteprovider

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
)

func TestValidateUniqueIndexesUsesExactMainSchemaSet(t *testing.T) {
	database := openControlTestDatabase(t)
	if _, err := database.ExecContext(t.Context(), `
		CREATE TABLE records (
			id INTEGER PRIMARY KEY,
			natural_key TEXT UNIQUE,
			value TEXT NOT NULL
		);
		CREATE UNIQUE INDEX records_value_unique ON records(value);
	`); err != nil {
		t.Fatal(err)
	}

	expected := []string{"records_value_unique"}
	if err := ValidateUniqueIndexes(t.Context(), database, "records", expected...); err != nil {
		t.Fatalf("ValidateUniqueIndexes() = %v", err)
	}
	if !slices.Equal(expected, []string{"records_value_unique"}) {
		t.Fatalf("caller expectation mutated: %q", expected)
	}
	if err := ValidateUniqueIndexes(t.Context(), database, "records"); err == nil {
		t.Fatal("unexpected manual unique index accepted")
	}
	if err := ValidateUniqueIndexes(
		t.Context(), database, "records", "records_missing_unique",
	); err == nil {
		t.Fatal("missing manual unique index accepted")
	}
	if err := ValidateUniqueIndexes(
		t.Context(), database, "records", "records_value_unique", "records_value_unique",
	); err == nil {
		t.Fatal("duplicate expected unique index accepted")
	}

	if _, err := database.ExecContext(t.Context(), "DROP INDEX records_value_unique"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateUniqueIndexes(t.Context(), database, "records"); err != nil {
		t.Fatalf("PRIMARY KEY/inline UNIQUE autoindexes counted as manual indexes: %v", err)
	}
}

func TestValidateUniqueIndexesIgnoresTemporaryShadow(t *testing.T) {
	database := openControlTestDatabase(t)
	if _, err := database.ExecContext(t.Context(), `
		CREATE TABLE records (id INTEGER PRIMARY KEY, value TEXT NOT NULL);
		CREATE UNIQUE INDEX records_main_unique ON records(value);
		CREATE TEMP TABLE records (id INTEGER PRIMARY KEY, value TEXT NOT NULL);
		CREATE UNIQUE INDEX records_temp_unique ON records(value);
	`); err != nil {
		t.Fatal(err)
	}

	if err := ValidateUniqueIndexes(
		t.Context(), database, "records", "records_main_unique",
	); err != nil {
		t.Fatalf("main-schema index hidden by temporary table: %v", err)
	}
	if err := ValidateUniqueIndexes(
		t.Context(), database, "records", "records_temp_unique",
	); err == nil {
		t.Fatal("temporary index accepted as main-schema index")
	}
}

func TestValidateUniqueIndexesRejectsAbsentTableAndInvalidInputs(t *testing.T) {
	database := openControlTestDatabase(t)

	if err := ValidateUniqueIndexes(t.Context(), database, "absent"); err == nil {
		t.Fatal("absent table accepted as empty index set")
	}
	for _, table := range []string{"", " ", " records ", "bad\x00table"} {
		if err := ValidateUniqueIndexes(t.Context(), database, table); err == nil {
			t.Fatalf("invalid table %q accepted", table)
		}
	}
	for _, name := range []string{"", " ", " records_index ", "bad\x00index"} {
		if err := ValidateUniqueIndexes(t.Context(), database, "absent", name); err == nil {
			t.Fatalf("invalid expected index %q accepted", name)
		}
	}
	for _, value := range []string{
		strings.Repeat("t", maxSQLiteSchemaIdentifierBytes+1),
		string([]byte{0xff}),
	} {
		if err := ValidateUniqueIndexes(t.Context(), database, value); err == nil {
			t.Fatalf("invalid table identifier of %d bytes accepted", len(value))
		}
		if err := ValidateUniqueIndexes(t.Context(), database, "absent", value); err == nil {
			t.Fatalf("invalid index identifier of %d bytes accepted", len(value))
		}
	}
	tooMany := make([]string, maxSQLiteExpectedUniqueIndexes+1)
	for index := range tooMany {
		tooMany[index] = "index_" + strings.Repeat("x", index)
	}
	if err := ValidateUniqueIndexes(t.Context(), database, "absent", tooMany...); err == nil {
		t.Fatal("oversized expected-index set accepted")
	}
	if err := ValidateUniqueIndexes(nil, database, "absent"); err == nil {
		t.Fatal("nil context accepted")
	}
	if err := ValidateUniqueIndexes(t.Context(), nil, "absent"); err == nil {
		t.Fatal("nil query boundary accepted")
	}
}

type closingSchemaQueryer struct {
	database *sql.DB
	closeAt  int
	calls    int
}

func (queryer *closingSchemaQueryer) QueryRowContext(
	ctx context.Context,
	query string,
	arguments ...any,
) *sql.Row {
	queryer.calls++
	if queryer.calls == queryer.closeAt {
		_ = queryer.database.Close()
	}
	return queryer.database.QueryRowContext(ctx, query, arguments...)
}

func TestValidateUniqueIndexesReturnsRealQueryErrors(t *testing.T) {
	closed := closedControlTestDatabase(t)
	if err := ValidateUniqueIndexes(t.Context(), closed, "records"); err == nil {
		t.Fatal("closed database accepted while checking unexpected indexes")
	}
	if err := ValidateUniqueIndexes(
		t.Context(), closed, "records", "records_value_unique",
	); err == nil {
		t.Fatal("closed database accepted while checking required index")
	}

	indexDatabase := openControlTestDatabase(t)
	if _, err := indexDatabase.ExecContext(t.Context(), `
		CREATE TABLE records (id INTEGER PRIMARY KEY, value TEXT NOT NULL);
		CREATE UNIQUE INDEX records_value_unique ON records(value);
	`); err != nil {
		t.Fatal(err)
	}
	if err := ValidateUniqueIndexes(t.Context(), &closingSchemaQueryer{
		database: indexDatabase,
		closeAt:  2,
	}, "records", "records_value_unique"); err == nil {
		t.Fatal("database closure before required-index query was accepted")
	}

	totalDatabase := openControlTestDatabase(t)
	if _, err := totalDatabase.ExecContext(t.Context(), `
		CREATE TABLE records (id INTEGER PRIMARY KEY, value TEXT NOT NULL);
		CREATE UNIQUE INDEX records_value_unique ON records(value);
	`); err != nil {
		t.Fatal(err)
	}
	if err := ValidateUniqueIndexes(t.Context(), &closingSchemaQueryer{
		database: totalDatabase,
		closeAt:  3,
	}, "records", "records_value_unique"); err == nil {
		t.Fatal("database closure before exact-set query was accepted")
	}
}
