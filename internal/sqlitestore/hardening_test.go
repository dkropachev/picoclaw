package sqlitestore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMigrationSQLValidationRejectsTransactionEscapes(t *testing.T) {
	tests := []struct {
		name       string
		migrations []Migration
	}{
		{name: "missing"},
		{name: "noncontiguous", migrations: []Migration{{Version: 2, Statements: []string{"SELECT 1"}}}},
		{name: "no work", migrations: []Migration{{Version: 1}}},
		{name: "empty SQL", migrations: []Migration{{Version: 1, Statements: []string{" "}}}},
		{name: "comment only", migrations: []Migration{{Version: 1, Statements: []string{"/* harmless */"}}}},
		{name: "later commit", migrations: []Migration{{Version: 1, Statements: []string{
			"CREATE TABLE escaped (id INTEGER); COMMIT",
		}}}},
		{name: "commented pragma", migrations: []Migration{{Version: 1, Statements: []string{
			"-- comment\n PRAGMA user_version = 9",
		}}}},
		{name: "rollback", migrations: []Migration{{Version: 1, Statements: []string{"ROLLBACK"}}}},
		{name: "end", migrations: []Migration{{Version: 1, Statements: []string{"END"}}}},
		{name: "savepoint", migrations: []Migration{{Version: 1, Statements: []string{"SAVEPOINT x"}}}},
		{name: "release", migrations: []Migration{{Version: 1, Statements: []string{"RELEASE x"}}}},
		{name: "attach", migrations: []Migration{{Version: 1, Statements: []string{"ATTACH 'other' AS other"}}}},
		{name: "detach", migrations: []Migration{{Version: 1, Statements: []string{"DETACH other"}}}},
		{name: "vacuum", migrations: []Migration{{Version: 1, Statements: []string{"VACUUM"}}}},
		{name: "unterminated comment", migrations: []Migration{{Version: 1, Statements: []string{"/*"}}}},
		{name: "unterminated quote", migrations: []Migration{{Version: 1, Statements: []string{"SELECT '"}}}},
		{name: "invalid UTF8", migrations: []Migration{{Version: 1, Statements: []string{string([]byte{0xff})}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateMigrations(test.migrations); err == nil {
				t.Fatal("validateMigrations() unexpectedly succeeded")
			}
		})
	}

	allowed := []Migration{{Version: 1, Statements: []string{
		`CREATE TABLE allowed (
			value TEXT NOT NULL DEFAULT 'COMMIT; PRAGMA user_version = 9'
		) /* ROLLBACK */ STRICT;`,
	}}}
	if err := validateMigrations(allowed); err != nil {
		t.Fatalf("validateMigrations(allowed) error = %v", err)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestExportedSchemaValidationIsExact(t *testing.T) {
	db, err := Open(t.Context(), filepath.Join(t.TempDir(), "schema.db"), testOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	expected := `create table if not exists records (
		id text primary key,
		value text not null,
		version integer not null default 1 check(version > 0)
	) strict`
	if err := ValidateSchemaObject(t.Context(), conn, "table", "records", expected); err != nil {
		t.Fatalf("ValidateSchemaObject() error = %v", err)
	}
	if err := ValidateSchemaObject(
		t.Context(), conn, "table", "records", strings.Replace(expected, "version > 0", "version >= 0", 1),
	); err == nil {
		t.Fatal("ValidateSchemaObject() accepted changed constraint")
	}
	if err := ValidateSchemaObject(t.Context(), conn, "table", "missing", expected); err == nil {
		t.Fatal("ValidateSchemaObject() accepted missing table")
	}
	if err := ValidateSchemaObject(
		t.Context(), conn, "index", "sqlite_autoindex_records_1", "CREATE INDEX x ON records(id)",
	); err == nil || !strings.Contains(err.Error(), "no defining SQL") {
		t.Fatalf("ValidateSchemaObject(autoindex) error = %v", err)
	}
	if err := ValidateSchemaObject(t.Context(), nil, "table", "records", expected); err == nil {
		t.Fatal("ValidateSchemaObject() accepted nil connection")
	}
	if err := ValidateSchemaObject(t.Context(), conn, "database", "records", expected); err == nil {
		t.Fatal("ValidateSchemaObject() accepted invalid object type")
	}
	if err := ValidateSchemaObject(t.Context(), conn, "table", "\x00", expected); err == nil {
		t.Fatal("ValidateSchemaObject() accepted invalid object name")
	}
	if err := ValidateSchemaObject(nil, conn, "table", "records", expected); err != nil {
		t.Fatalf("ValidateSchemaObject(nil context) error = %v", err)
	}
	if err := ValidateSchemaObject(
		t.Context(), conn, "table", "records", string([]byte{0xff}),
	); err == nil {
		t.Fatal("ValidateSchemaObject() accepted invalid expected UTF-8")
	}

	if _, err := conn.ExecContext(
		t.Context(),
		`CREATE UNIQUE INDEX records_value_unique ON records(value)`,
	); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSchemaObject(
		t.Context(),
		conn,
		"index",
		"records_value_unique",
		`CREATE UNIQUE INDEX IF NOT EXISTS records_value_unique ON records(value)`,
	); err != nil {
		t.Fatalf("ValidateSchemaObject(index) error = %v", err)
	}
	if err := ValidateUniqueIndexSet(
		t.Context(), conn, "records", "records_value_unique",
	); err != nil {
		t.Fatalf("ValidateUniqueIndexSet() error = %v", err)
	}
	if err := ValidateUniqueIndexSet(t.Context(), conn, "records"); err == nil {
		t.Fatal("ValidateUniqueIndexSet() ignored unexpected unique index")
	}
	if err := ValidateUniqueIndexSet(
		t.Context(), conn, "records", "records_missing_unique",
	); err == nil {
		t.Fatal("ValidateUniqueIndexSet() accepted missing expected index")
	}
	if err := ValidateUniqueIndexSet(
		t.Context(), conn, "records", "records_value_unique", "records_value_unique",
	); err == nil {
		t.Fatal("ValidateUniqueIndexSet() accepted duplicate expectation")
	}
	if err := ValidateUniqueIndexSet(t.Context(), nil, "records"); err == nil {
		t.Fatal("ValidateUniqueIndexSet() accepted nil connection")
	}
	if err := ValidateUniqueIndexSet(nil, conn, "records", "\x00"); err == nil {
		t.Fatal("ValidateUniqueIndexSet() accepted invalid expected name")
	}
	closed, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSchemaObject(t.Context(), closed, "table", "records", expected); err == nil {
		t.Fatal("ValidateSchemaObject() accepted closed connection")
	}
	if err := ValidateUniqueIndexSet(t.Context(), closed, "records"); err == nil {
		t.Fatal("ValidateUniqueIndexSet() accepted closed connection")
	}
	if err := ValidateUniqueIndexSet(
		t.Context(), closed, "records", "records_value_unique",
	); err == nil {
		t.Fatal("ValidateUniqueIndexSet() queried an expected index on a closed connection")
	}
}

func TestSchemaValidationRejectsInvalidStoredUTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-schema.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE records (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`PRAGMA writable_schema = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(
		`UPDATE sqlite_schema SET sql = CAST(x'ff' AS TEXT) WHERE name = 'records'`,
	); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := ValidateSchemaObject(
		t.Context(), conn, "table", "records", `CREATE TABLE records (id TEXT PRIMARY KEY)`,
	); err == nil || !strings.Contains(err.Error(), "canonicalize") {
		t.Fatalf("ValidateSchemaObject() error = %v", err)
	}
}

func TestSchemaTokenizerQuotedCommentsAndOperators(t *testing.T) {
	left := `-- leading comment without a terminator`
	right := `/* leading */ CREATE TABLE "quoted" (
		[select] TEXT DEFAULT 'it''s -- literal',
		value INTEGER CHECK(value >= 1 AND value <> 2),
		payload TEXT CHECK(payload ->> '$.id' != '')
	);`
	if canonical, err := canonicalSQLiteSQL(left); err != nil || canonical != "" {
		t.Fatalf("canonical comment = %q, %v", canonical, err)
	}
	first, err := canonicalSQLiteSQL(right)
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalSQLiteSQL(strings.ReplaceAll(right, "[select]", `"select"`))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("different quoted identifier forms collapsed")
	}
	if _, err := canonicalSQLiteSQL("/*"); err == nil {
		t.Fatal("unterminated comment accepted")
	}
	if _, _, err := readSQLiteQuotedToken("[unterminated", 0); err == nil {
		t.Fatal("unterminated bracket identifier accepted")
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestImportSchemaValidationRejectsWeakenedDDLAndRogueUniqueIndex(t *testing.T) {
	for _, mutate := range []func(*testing.T, *sql.DB){
		func(t *testing.T, db *sql.DB) {
			t.Helper()
			if _, err := db.Exec(`DROP TABLE storage_import_issues`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE storage_import_issues (
				component TEXT, source_id TEXT, sequence INTEGER,
				issue_code TEXT, record_digest BLOB
			)`); err != nil {
				t.Fatal(err)
			}
		},
		func(t *testing.T, db *sql.DB) {
			t.Helper()
			if _, err := db.Exec(
				`CREATE UNIQUE INDEX rogue_storage_import_source ON storage_imports(source_id)`,
			); err != nil {
				t.Fatal(err)
			}
		},
	} {
		path := filepath.Join(t.TempDir(), "schema.db")
		db, err := Open(t.Context(), path, testOptions())
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		raw, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		mutate(t, raw)
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(t.Context(), path, testOptions()); !errors.Is(err, ErrInvalidSchema) {
			t.Fatalf("Open() error = %v, want ErrInvalidSchema", err)
		}
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestIntegrityFailurePreventsCommittingNextMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign-key.db")
	versionOne := Options{
		Component: "integrity-test",
		Migrations: []Migration{{
			Version: 1,
			Statements: []string{
				`CREATE TABLE parents (id TEXT PRIMARY KEY) STRICT`,
				`CREATE TABLE children (
					id TEXT PRIMARY KEY,
					parent_id TEXT NOT NULL REFERENCES parents(id)
				) STRICT`,
			},
		}},
	}
	db, err := Open(t.Context(), path, versionOne)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO children(id, parent_id) VALUES ('child', 'missing')`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	var applied atomic.Bool
	versionTwo := versionOne
	versionTwo.Migrations = append(versionTwo.Migrations, Migration{
		Version:    2,
		Statements: []string{`ALTER TABLE parents ADD COLUMN value TEXT`},
		Apply: func(context.Context, *sql.Conn) error {
			applied.Store(true)
			return nil
		},
	})
	if _, err := Open(t.Context(), path, versionTwo); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Open() error = %v, want ErrIntegrity", err)
	}
	if applied.Load() {
		t.Fatal("next migration ran before the integrity check")
	}
	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version int
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("user_version = %d, %v", version, err)
	}
}

func TestLegacySourcesAreSortedFencedAndBounded(t *testing.T) {
	t.Run("deterministic order", func(t *testing.T) {
		root := t.TempDir()
		writeLegacyTestFile(t, root, "z.json", "z")
		writeLegacyTestFile(t, root, "a.json", "a")
		options := testOptions()
		options.Legacy = legacyTestOptions(
			root,
			[]LegacySource{{ID: "z", Relative: "z.json"}, {ID: "a", Relative: "a.json"}},
			func(ctx context.Context, conn *sql.Conn, input LegacyInput) (ImportResult, error) {
				_, err := conn.ExecContext(
					ctx,
					`INSERT INTO records(id, value) VALUES (?, ?)`,
					string(input.Data),
					input.Relative,
				)
				return ImportResult{Imported: 1}, err
			},
		)
		db, err := Open(t.Context(), filepath.Join(root, "db", "store.db"), options)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		rows, err := db.Query(`SELECT id FROM records ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var order []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			order = append(order, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if strings.Join(order, ",") != "a,z" {
			t.Fatalf("import order = %v", order)
		}
	})

	t.Run("explicit dependency order then relative and id", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{"event.json", "run-z.json", "run-a.json"} {
			writeLegacyTestFile(t, root, name, name)
		}
		options := testOptions()
		options.Legacy = legacyTestOptions(
			root,
			[]LegacySource{
				{ID: "event", Relative: "event.json", Order: 20},
				{ID: "run-z", Relative: "run-z.json", Order: 10},
				{ID: "run-a", Relative: "run-a.json", Order: 10},
			},
			func(ctx context.Context, conn *sql.Conn, input LegacyInput) (ImportResult, error) {
				_, err := conn.ExecContext(
					ctx,
					`INSERT INTO records(id, value) VALUES (?, ?)`,
					input.ID,
					input.Relative,
				)
				return ImportResult{Imported: 1}, err
			},
		)
		db, err := Open(t.Context(), filepath.Join(root, "db", "store.db"), options)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		rows, err := db.Query(`SELECT id FROM records ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var order []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			order = append(order, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if strings.Join(order, ",") != "run-a,run-z,event" {
			t.Fatalf("import order = %v", order)
		}
	})

	t.Run("source count", func(t *testing.T) {
		root := t.TempDir()
		options := testOptions()
		legacy := legacyTestOptions(root, []LegacySource{
			{ID: "a", Relative: "a.json"},
			{ID: "b", Relative: "b.json"},
		}, nil)
		legacy.MaxSources = 1
		options.Legacy = legacy
		if _, err := Open(t.Context(), filepath.Join(root, "db", "store.db"), options); err == nil ||
			!strings.Contains(err.Error(), "source count") {
			t.Fatalf("Open() error = %v", err)
		}
	})

	t.Run("invalid configured bounds", func(t *testing.T) {
		root := t.TempDir()
		for _, mutate := range []func(*LegacyOptions){
			func(options *LegacyOptions) { options.MaxSources = -1 },
			func(options *LegacyOptions) { options.MaxSources = maximumLegacyMaxSources + 1 },
			func(options *LegacyOptions) { options.MaxTotalBytes = -1 },
			func(options *LegacyOptions) { options.MaxTotalBytes = maximumLegacyMaxTotalBytes + 1 },
		} {
			options := testOptions()
			options.Legacy = legacyTestOptions(root, nil, nil)
			mutate(options.Legacy)
			if _, err := Open(t.Context(), filepath.Join(t.TempDir(), "store.db"), options); err == nil {
				t.Fatal("Open() accepted invalid legacy enumeration bounds")
			}
		}
	})

	t.Run("aggregate bytes", func(t *testing.T) {
		root := t.TempDir()
		writeLegacyTestFile(t, root, "a.json", "1234")
		writeLegacyTestFile(t, root, "b.json", "5678")
		options := testOptions()
		legacy := legacyTestOptions(root, []LegacySource{
			{ID: "a", Relative: "a.json"},
			{ID: "b", Relative: "b.json"},
		}, nil)
		legacy.MaxTotalBytes = 7
		options.Legacy = legacy
		if _, err := Open(t.Context(), filepath.Join(root, "db", "store.db"), options); err == nil ||
			!strings.Contains(err.Error(), "aggregate size") {
			t.Fatalf("Open() error = %v", err)
		}
	})

	t.Run("ID relative fence", func(t *testing.T) {
		root := t.TempDir()
		writeLegacyTestFile(t, root, "first.json", "same")
		sources := []LegacySource{{ID: "source", Relative: "first.json"}}
		options := testOptions()
		options.Legacy = legacyTestOptions(root, sources, nil)
		databasePath := filepath.Join(root, "db", "store.db")
		db, err := Open(t.Context(), databasePath, options)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		writeLegacyTestFile(t, root, "second.json", "same")
		options.Legacy.Sources = func() ([]LegacySource, error) {
			return []LegacySource{{ID: "source", Relative: "second.json"}}, nil
		}
		if _, err := Open(t.Context(), databasePath, options); err == nil ||
			!strings.Contains(err.Error(), "path changed") {
			t.Fatalf("Open() error = %v", err)
		}
	})

	t.Run("archive recursion", func(t *testing.T) {
		root := t.TempDir()
		archiveRelative := "legacy-json/test-store-v1/old.json"
		writeLegacyTestFile(t, root, archiveRelative, "old")
		options := testOptions()
		options.Legacy = legacyTestOptions(
			root,
			[]LegacySource{{ID: "archive", Relative: archiveRelative}},
			nil,
		)
		if _, err := Open(t.Context(), filepath.Join(root, "db", "store.db"), options); err == nil ||
			!strings.Contains(err.Error(), "inside the archive") {
			t.Fatalf("Open() error = %v", err)
		}
	})

	t.Run("enumeration errors and duplicates", func(t *testing.T) {
		root := t.TempDir()
		for _, sources := range [][]LegacySource{
			{{ID: "same", Relative: "a.json"}, {ID: "same", Relative: "b.json"}},
			{{ID: "a", Relative: "same.json"}, {ID: "b", Relative: "same.json"}},
			{{ID: "bad!", Relative: "bad.json"}},
			{{ID: "bad", Relative: "../bad.json"}},
		} {
			options := testOptions()
			options.Legacy = legacyTestOptions(root, sources, nil)
			if _, err := Open(t.Context(), filepath.Join(t.TempDir(), "store.db"), options); err == nil {
				t.Fatalf("Open() accepted sources %#v", sources)
			}
		}
		options := testOptions()
		options.Legacy = legacyTestOptions(root, nil, nil)
		options.Legacy.Sources = func() ([]LegacySource, error) {
			return nil, errors.New("enumeration failed")
		}
		if _, err := Open(t.Context(), filepath.Join(t.TempDir(), "store.db"), options); err == nil ||
			!strings.Contains(err.Error(), "enumeration failed") {
			t.Fatalf("Open() error = %v", err)
		}
	})

	t.Run("incomplete and canceled", func(t *testing.T) {
		root := t.TempDir()
		writeLegacyTestFile(t, root, "source.json", "payload")
		for _, incomplete := range []*LegacyOptions{
			{
				SourceRoot: root, ArchiveRoot: filepath.Join(root, "archive"),
				Import: func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
					return ImportResult{}, nil
				},
			},
			{
				SourceRoot: root, ArchiveRoot: filepath.Join(root, "archive"),
				Sources: func() ([]LegacySource, error) { return nil, nil },
			},
		} {
			options := testOptions()
			options.Legacy = incomplete
			if _, err := Open(t.Context(), filepath.Join(t.TempDir(), "store.db"), options); err == nil {
				t.Fatal("Open() accepted incomplete legacy migration")
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		options := testOptions()
		options.Legacy = legacyTestOptions(
			root,
			[]LegacySource{{ID: "source", Relative: "source.json"}},
			nil,
		)
		options.Legacy.Sources = func() ([]LegacySource, error) {
			cancel()
			return []LegacySource{{ID: "source", Relative: "source.json"}}, nil
		}
		if _, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"), options); !errors.Is(err, context.Canceled) {
			t.Fatalf("Open(canceled enumeration) error = %v", err)
		}
	})

	t.Run("invalid importer accounting", func(t *testing.T) {
		root := t.TempDir()
		writeLegacyTestFile(t, root, "source.json", "payload")
		options := testOptions()
		options.Legacy = legacyTestOptions(
			root,
			[]LegacySource{{ID: "source", Relative: "source.json"}},
			func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
				return ImportResult{Skipped: -1}, nil
			},
		)
		if _, err := Open(t.Context(), filepath.Join(root, "db", "store.db"), options); err == nil ||
			!strings.Contains(err.Error(), "accounting") {
			t.Fatalf("Open() error = %v", err)
		}
	})
}

func TestLegacySourceFileBoundsModesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	writeLegacyTestFile(t, root, "nested/value.json", "payload")
	if input, found, err := readLegacySource(
		root,
		LegacySource{ID: "value", Relative: "nested/value.json"},
		1024,
	); err != nil || !found || input.Mode.Perm() != 0o600 || string(input.Data) != "payload" {
		t.Fatalf("readLegacySource() = %#v, %v, %v", input, found, err)
	}
	if _, found, err := readLegacySource(
		root,
		LegacySource{ID: "missing", Relative: "nested/missing.json"},
		1024,
	); err != nil || found {
		t.Fatalf("missing readLegacySource() = %v, %v", found, err)
	}
	for _, maximum := range []int64{-1, 1<<30 + 1} {
		if _, _, err := readLegacySource(
			root,
			LegacySource{ID: "value", Relative: "nested/value.json", MaxBytes: maximum},
			1024,
		); err == nil {
			t.Fatalf("readLegacySource(max=%d) unexpectedly succeeded", maximum)
		}
	}
	if _, _, err := readLegacySource(
		root,
		LegacySource{ID: "value", Relative: "nested/value.json", MaxBytes: 3},
		1024,
	); err == nil {
		t.Fatal("readLegacySource() accepted oversized source")
	}
	if err := os.Chmod(filepath.Join(root, "nested", "value.json"), 0o622); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readLegacySource(
		root,
		LegacySource{ID: "value", Relative: "nested/value.json"},
		1024,
	); err == nil {
		t.Fatal("readLegacySource() accepted unsafe mode")
	}

	if err := os.Chmod(filepath.Join(root, "nested", "value.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err == nil {
		if _, _, err := readLegacySource(
			root,
			LegacySource{ID: "linked", Relative: "link/value.json"},
			1024,
		); err == nil {
			t.Fatal("readLegacySource() followed symlink directory")
		}
	}
}

func TestLegacyRejectsWritableAndSymlinkedDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory mode and symlink assertions")
	}
	t.Run("writable source root", func(t *testing.T) {
		root := t.TempDir()
		writeLegacyTestFile(t, root, "source.json", "payload")
		options := testOptions()
		options.Legacy = legacyTestOptions(
			root,
			[]LegacySource{{ID: "source", Relative: "source.json"}},
			nil,
		)
		if err := os.Chmod(root, 0o722); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(t.Context(), filepath.Join(t.TempDir(), "store.db"), options); err == nil ||
			!strings.Contains(err.Error(), "non-writable real directory") {
			t.Fatalf("Open() error = %v", err)
		}
	})

	t.Run("writable nested source directory", func(t *testing.T) {
		root := t.TempDir()
		writeLegacyTestFile(t, root, "nested/source.json", "payload")
		if err := os.Chmod(filepath.Join(root, "nested"), 0o722); err != nil {
			t.Fatal(err)
		}
		options := testOptions()
		options.Legacy = legacyTestOptions(
			root,
			[]LegacySource{{ID: "source", Relative: "nested/source.json"}},
			nil,
		)
		if _, err := Open(t.Context(), filepath.Join(t.TempDir(), "store.db"), options); err == nil ||
			!strings.Contains(err.Error(), "source directory") {
			t.Fatalf("Open() error = %v", err)
		}
	})

	t.Run("symlinked archive ancestor", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		writeLegacyTestFile(t, root, "source.json", "payload")
		if err := os.Symlink(outside, filepath.Join(root, "legacy-json")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		options := testOptions()
		options.Legacy = legacyTestOptions(
			root,
			[]LegacySource{{ID: "source", Relative: "source.json"}},
			nil,
		)
		if _, err := Open(t.Context(), filepath.Join(t.TempDir(), "store.db"), options); err == nil ||
			!strings.Contains(err.Error(), "archive ancestor") {
			t.Fatalf("Open() error = %v", err)
		}
		if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
			t.Fatalf("outside archive was mutated: %v, %v", entries, err)
		}
	})

	t.Run("writable archive ancestor", func(t *testing.T) {
		root := t.TempDir()
		writeLegacyTestFile(t, root, "source.json", "payload")
		archiveAncestor := filepath.Join(root, "legacy-json")
		if err := os.Mkdir(archiveAncestor, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(archiveAncestor, 0o722); err != nil {
			t.Fatal(err)
		}
		options := testOptions()
		options.Legacy = legacyTestOptions(
			root,
			[]LegacySource{{ID: "source", Relative: "source.json"}},
			nil,
		)
		if _, err := Open(t.Context(), filepath.Join(t.TempDir(), "store.db"), options); err == nil ||
			!strings.Contains(err.Error(), "archive ancestor") {
			t.Fatalf("Open() error = %v", err)
		}
	})
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestLegacySourceAndInspectionIOFailures(t *testing.T) {
	root := t.TempDir()
	writeLegacyTestFile(t, root, "source.json", "payload")
	writeLegacyTestFile(t, root, "other.json", "other")
	source := LegacySource{ID: "source", Relative: "source.json"}
	originalSourceOpen := legacySourceOpen
	originalSourceReadAll := legacySourceReadAll
	originalInspectOpen := legacyInspectOpen
	originalInspectCopy := legacyInspectCopy
	t.Cleanup(func() {
		legacySourceOpen = originalSourceOpen
		legacySourceReadAll = originalSourceReadAll
		legacyInspectOpen = originalInspectOpen
		legacyInspectCopy = originalInspectCopy
	})

	legacySourceOpen = func(string) (*os.File, error) { return nil, errors.New("open failed") }
	if _, _, err := readLegacySource(root, source, 1024); err == nil {
		t.Fatal("readLegacySource() ignored open failure")
	}
	legacySourceOpen = func(string) (*os.File, error) {
		return os.Open(filepath.Join(root, "other.json"))
	}
	if _, _, err := readLegacySource(root, source, 1024); err == nil ||
		!strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("readLegacySource(changed) error = %v", err)
	}
	legacySourceOpen = originalSourceOpen
	legacySourceReadAll = func(io.Reader) ([]byte, error) { return nil, errors.New("read failed") }
	if _, _, err := readLegacySource(root, source, 1024); err == nil {
		t.Fatal("readLegacySource() ignored read failure")
	}
	legacySourceReadAll = func(io.Reader) ([]byte, error) { return []byte("too long"), nil }
	if _, _, err := readLegacySource(root, source, 7); err == nil {
		t.Fatal("readLegacySource() ignored post-read size failure")
	}
	closedSource, err := os.Open(filepath.Join(root, "source.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := closedSource.Close(); err != nil {
		t.Fatal(err)
	}
	legacySourceReadAll = originalSourceReadAll
	legacySourceOpen = func(string) (*os.File, error) { return closedSource, nil }
	if _, _, err := readLegacySource(root, source, 1024); err == nil {
		t.Fatal("readLegacySource() ignored Stat failure")
	}
	legacySourceOpen = originalSourceOpen
	legacySourceReadAll = originalSourceReadAll

	legacyInspectOpen = func(string) (*os.File, error) { return nil, errors.New("open failed") }
	if _, _, err := inspectLegacyRegularFile(filepath.Join(root, "source.json"), 1024); err == nil {
		t.Fatal("inspectLegacyRegularFile() ignored open failure")
	}
	legacyInspectOpen = func(string) (*os.File, error) {
		return os.Open(filepath.Join(root, "other.json"))
	}
	if _, _, err := inspectLegacyRegularFile(filepath.Join(root, "source.json"), 1024); err == nil ||
		!strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("inspectLegacyRegularFile(changed) error = %v", err)
	}
	legacyInspectOpen = originalInspectOpen
	legacyInspectCopy = func(io.Writer, io.Reader) (int64, error) {
		return 0, errors.New("copy failed")
	}
	if _, _, err := inspectLegacyRegularFile(filepath.Join(root, "source.json"), 1024); err == nil {
		t.Fatal("inspectLegacyRegularFile() ignored copy failure")
	}
	legacyInspectCopy = func(io.Writer, io.Reader) (int64, error) { return 8, nil }
	if _, _, err := inspectLegacyRegularFile(filepath.Join(root, "source.json"), 7); err == nil {
		t.Fatal("inspectLegacyRegularFile() ignored copied-byte size failure")
	}
	closedInspect, err := os.Open(filepath.Join(root, "source.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := closedInspect.Close(); err != nil {
		t.Fatal(err)
	}
	legacyInspectCopy = originalInspectCopy
	legacyInspectOpen = func(string) (*os.File, error) { return closedInspect, nil }
	if _, _, err := inspectLegacyRegularFile(filepath.Join(root, "source.json"), 1024); err == nil {
		t.Fatal("inspectLegacyRegularFile() ignored Stat failure")
	}
	legacyInspectOpen = originalInspectOpen
	legacyInspectCopy = originalInspectCopy

	tooLong := strings.Repeat("x", 5000)
	if _, _, err := readLegacySource(
		root,
		LegacySource{ID: "long", Relative: tooLong},
		1024,
	); err == nil {
		t.Fatal("readLegacySource() ignored Lstat failure")
	}
	if _, _, err := inspectLegacyRegularFile(filepath.Join(root, tooLong), 1024); err == nil {
		t.Fatal("inspectLegacyRegularFile() ignored Lstat failure")
	}
	if _, _, err := inspectLegacyRegularFile(filepath.Join(root, "source.json"), 3); err == nil {
		t.Fatal("inspectLegacyRegularFile() accepted oversized file")
	}
}

func TestLegacyImportRecordAndIssueInsertFailuresRollBack(t *testing.T) {
	for _, failure := range []string{"import-record", "issue-record"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			writeLegacyTestFile(t, root, "source.json", "payload")
			options := testOptions()
			options.Legacy = legacyTestOptions(
				root,
				[]LegacySource{{ID: "source", Relative: "source.json"}},
				func(ctx context.Context, conn *sql.Conn, input LegacyInput) (ImportResult, error) {
					table := "storage_imports"
					result := ImportResult{Imported: 1}
					if failure == "issue-record" {
						table = "storage_import_issues"
						result.Skipped = 1
						result.Issues = []ImportIssue{{
							Code: "invalid-record", RecordDigest: sha256.Sum256([]byte("invalid")),
						}}
					}
					_, err := conn.ExecContext(
						ctx,
						fmt.Sprintf(`CREATE TRIGGER reject_insert BEFORE INSERT ON %s
						BEGIN SELECT RAISE(ABORT, 'rejected'); END`, table),
					)
					return result, err
				},
			)
			databasePath := filepath.Join(root, "db", "store.db")
			if _, err := Open(t.Context(), databasePath, options); err == nil ||
				!strings.Contains(err.Error(), "legacy import") {
				t.Fatalf("Open() error = %v", err)
			}
			raw, err := sql.Open("sqlite", databasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			var version int
			if err := raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 0 {
				t.Fatalf("user_version = %d, %v", version, err)
			}
		})
	}
}

func TestImportLegacySourcesQueryAndRootFailures(t *testing.T) {
	root := t.TempDir()
	writeLegacyTestFile(t, root, "source.json", "payload")
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	options := *legacyTestOptions(
		root,
		[]LegacySource{{ID: "source", Relative: "source.json"}},
		nil,
	)
	if _, err := importLegacySources(t.Context(), conn, "test-store", options); err == nil ||
		!strings.Contains(err.Error(), "import horizon") {
		t.Fatalf("importLegacySources(missing schema) error = %v", err)
	}
	options.SourceRoot = ""
	if _, err := importLegacySources(t.Context(), conn, "test-store", options); err == nil ||
		!strings.Contains(err.Error(), "source and archive roots") {
		t.Fatalf("importLegacySources(invalid roots) error = %v", err)
	}
}

func TestArchiveRetriesCrashStatesWithoutOverwrite(t *testing.T) {
	for _, state := range []string{"hardlink", "destination-only", "independent", "missing", "changed"} {
		t.Run(state, func(t *testing.T) {
			root, sourcePath, archivePath, databasePath, options := pendingArchiveFixture(t)
			if err := os.RemoveAll(filepath.Join(root, "legacy-json")); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(archivePath), 0o700); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "hardlink":
				if err := os.Link(sourcePath, archivePath); err != nil {
					t.Skipf("hard links unavailable: %v", err)
				}
			case "destination-only":
				if err := os.Rename(sourcePath, archivePath); err != nil {
					t.Fatal(err)
				}
			case "independent":
				if err := os.WriteFile(archivePath, []byte("payload"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(sourcePath); err != nil {
					t.Fatal(err)
				}
			case "changed":
				if err := os.WriteFile(sourcePath, []byte("changed"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			db, err := Open(t.Context(), databasePath, options)
			switch state {
			case "hardlink", "destination-only":
				if err != nil {
					t.Fatalf("Open() error = %v", err)
				}
				defer db.Close()
				if _, statErr := os.Stat(sourcePath); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("source remains: %v", statErr)
				}
				if data, readErr := os.ReadFile(archivePath); readErr != nil || string(data) != "payload" {
					t.Fatalf("archive = %q, %v", data, readErr)
				}
			default:
				if err == nil {
					db.Close()
					t.Fatal("Open() unexpectedly accepted an unsafe crash state")
				}
				if state == "independent" {
					if data, readErr := os.ReadFile(sourcePath); readErr != nil || string(data) != "payload" {
						t.Fatalf("source was overwritten or removed: %q, %v", data, readErr)
					}
				}
			}
		})
	}
}

func TestArchiveFailureHooksDoNotRemoveSource(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.json")
	archiveRoot := filepath.Join(root, "archive")
	archivePath := filepath.Join(archiveRoot, "nested", "source.json")
	writeLegacyTestFile(t, root, "source.json", "payload")
	digest := sha256.Sum256([]byte("payload"))

	originalInspect := legacyArchiveInspect
	originalLink := legacyArchiveLink
	originalSync := legacyArchiveSync
	originalRemove := legacyArchiveRemove
	t.Cleanup(func() {
		legacyArchiveInspect = originalInspect
		legacyArchiveLink = originalLink
		legacyArchiveSync = originalSync
		legacyArchiveRemove = originalRemove
	})

	legacyArchiveLink = func(string, string) error { return errors.New("link failed") }
	if err := archiveLegacySource(sourcePath, archivePath, archiveRoot, digest[:], 1024, 0o600); err == nil {
		t.Fatal("archiveLegacySource() ignored link failure")
	}
	legacyArchiveLink = originalLink
	legacyArchiveSync = func(string) error { return errors.New("sync failed") }
	if err := archiveLegacySource(sourcePath, archivePath, archiveRoot, digest[:], 1024, 0o600); err == nil {
		t.Fatal("archiveLegacySource() ignored sync failure")
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("sync failure removed source: %v", err)
	}
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
	legacyArchiveSync = originalSync
	legacyArchiveRemove = func(string) error { return errors.New("remove failed") }
	if err := archiveLegacySource(sourcePath, archivePath, archiveRoot, digest[:], 1024, 0o600); err == nil {
		t.Fatal("archiveLegacySource() ignored remove failure")
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("remove failure lost source: %v", err)
	}

	legacyArchiveRemove = originalRemove
	legacyArchiveInspect = func(path string, limit int64) (legacyFileSnapshot, bool, error) {
		if path == sourcePath {
			return legacyFileSnapshot{}, false, errors.New("inspect source failed")
		}
		return originalInspect(path, limit)
	}
	if err := archiveLegacySource(sourcePath, archivePath, archiveRoot, digest[:], 1024, 0o600); err == nil {
		t.Fatal("archiveLegacySource() ignored source inspection failure")
	}
}

func TestArchiveRejectsDigestModeAndParentMismatches(t *testing.T) {
	for _, failure := range []string{
		"destination-digest", "destination-mode", "source-digest", "existing-source-digest", "parent",
	} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			sourcePath := filepath.Join(root, "source.json")
			archiveRoot := filepath.Join(root, "archive")
			archivePath := filepath.Join(archiveRoot, "source.json")
			writeLegacyTestFile(t, root, "source.json", "payload")
			if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte("payload"))
			expectedMode := os.FileMode(0o600)
			switch failure {
			case "destination-digest":
				if err := os.WriteFile(archivePath, []byte("other"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "destination-mode":
				if err := os.WriteFile(archivePath, []byte("payload"), 0o400); err != nil {
					t.Fatal(err)
				}
			case "source-digest":
				digest = sha256.Sum256([]byte("other"))
			case "existing-source-digest":
				if err := os.WriteFile(archivePath, []byte("payload"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(sourcePath, []byte("other"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "parent":
				if err := os.RemoveAll(archiveRoot); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), archiveRoot); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			if err := archiveLegacySource(
				sourcePath,
				archivePath,
				archiveRoot,
				digest[:],
				1024,
				expectedMode,
			); err == nil {
				t.Fatalf("archiveLegacySource() accepted %s mismatch", failure)
			}
		})
	}
}

func TestArchiveInterruptedHardLinkRemoveFailure(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.json")
	archiveRoot := filepath.Join(root, "archive")
	archivePath := filepath.Join(archiveRoot, "source.json")
	writeLegacyTestFile(t, root, "source.json", "payload")
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(sourcePath, archivePath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	digest := sha256.Sum256([]byte("payload"))
	originalRemove := legacyArchiveRemove
	legacyArchiveRemove = func(string) error { return errors.New("remove failed") }
	t.Cleanup(func() { legacyArchiveRemove = originalRemove })
	if err := archiveLegacySource(
		sourcePath, archivePath, archiveRoot, digest[:], 1024, 0o600,
	); err == nil {
		t.Fatal("archiveLegacySource() ignored interrupted-link remove failure")
	}
}

func TestArchiveRetrySyncsExistingDestinationBeforeSourceRemoval(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.json")
	archiveRoot := filepath.Join(root, "archive")
	archivePath := filepath.Join(archiveRoot, "source.json")
	writeLegacyTestFile(t, root, "source.json", "payload")
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(sourcePath, archivePath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	digest := sha256.Sum256([]byte("payload"))
	originalSync := legacyArchiveSync
	syncCalls := 0
	legacyArchiveSync = func(string) error {
		syncCalls++
		return errors.New("injected sync failure")
	}
	t.Cleanup(func() { legacyArchiveSync = originalSync })
	if err := archiveLegacySource(
		sourcePath, archivePath, archiveRoot, digest[:], 1024, 0o600,
	); err == nil || !strings.Contains(err.Error(), "sync existing archive") {
		t.Fatalf("archiveLegacySource() error = %v", err)
	}
	if syncCalls != 1 {
		t.Fatalf("sync calls = %d, want 1", syncCalls)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("sync failure removed source: %v", err)
	}

	legacyArchiveSync = originalSync
	if err := archiveLegacySource(
		sourcePath, archivePath, archiveRoot, digest[:], 1024, 0o600,
	); err != nil {
		t.Fatalf("archiveLegacySource(retry) error = %v", err)
	}
	if _, err := os.Stat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry retained source: %v", err)
	}
}

func TestArchiveSyncsSourceBeforeFirstLink(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.json")
	archiveRoot := filepath.Join(root, "archive")
	archivePath := filepath.Join(archiveRoot, "source.json")
	writeLegacyTestFile(t, root, "source.json", "payload")
	digest := sha256.Sum256([]byte("payload"))
	source, found, err := inspectLegacyRegularFile(sourcePath, 1024)
	if err != nil || !found {
		t.Fatalf("inspect source = %v, %v", found, err)
	}
	originalSyncSource := legacyArchiveSyncSource
	originalLink := legacyArchiveLink
	synced := false
	legacyArchiveSyncSource = func(
		string,
		legacyFileSnapshot,
		[]byte,
		int64,
		os.FileMode,
	) (os.FileInfo, error) {
		synced = true
		return source.info, nil
	}
	legacyArchiveLink = func(oldName, newName string) error {
		if !synced {
			return errors.New("link ran before source sync")
		}
		return os.Link(oldName, newName)
	}
	t.Cleanup(func() {
		legacyArchiveSyncSource = originalSyncSource
		legacyArchiveLink = originalLink
	})
	if err := archiveLegacySource(
		sourcePath, archivePath, archiveRoot, digest[:], 1024, 0o600,
	); err != nil {
		t.Fatalf("archiveLegacySource() error = %v", err)
	}
	if !synced {
		t.Fatal("archive did not sync source before link")
	}
}

func TestArchiveRefusesAtomicSourceReplacementBeforeRemoval(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.json")
	archiveRoot := filepath.Join(root, "archive")
	archivePath := filepath.Join(archiveRoot, "source.json")
	writeLegacyTestFile(t, root, "source.json", "payload")
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(sourcePath, archivePath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	digest := sha256.Sum256([]byte("payload"))
	originalBeforeRemove := legacyArchiveBeforeRemove
	legacyArchiveBeforeRemove = func() {
		replacement := filepath.Join(root, "replacement.json")
		if err := os.WriteFile(replacement, []byte("payload"), 0o600); err != nil {
			t.Errorf("write replacement: %v", err)
			return
		}
		if err := os.Rename(replacement, sourcePath); err != nil {
			t.Errorf("publish replacement: %v", err)
		}
	}
	t.Cleanup(func() { legacyArchiveBeforeRemove = originalBeforeRemove })
	if err := archiveLegacySource(
		sourcePath, archivePath, archiveRoot, digest[:], 1024, 0o600,
	); err == nil || !strings.Contains(err.Error(), "immediately before removal") {
		t.Fatalf("archiveLegacySource() error = %v", err)
	}
	if data, err := os.ReadFile(sourcePath); err != nil || string(data) != "payload" {
		t.Fatalf("replacement source was deleted: %q, %v", data, err)
	}
	if sourceInfo, err := os.Stat(sourcePath); err != nil {
		t.Fatal(err)
	} else if archiveInfo, err := os.Stat(archivePath); err != nil {
		t.Fatal(err)
	} else if os.SameFile(sourceInfo, archiveInfo) {
		t.Fatal("test did not replace the source inode")
	}
}

func TestArchivePublicationVerificationFailures(t *testing.T) {
	for _, failure := range []string{"destination-inspect", "missing-after-link", "changed-after-link"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			sourcePath := filepath.Join(root, "source.json")
			archiveRoot := filepath.Join(root, "archive")
			archivePath := filepath.Join(archiveRoot, "source.json")
			writeLegacyTestFile(t, root, "source.json", "payload")
			digest := sha256.Sum256([]byte("payload"))
			originalInspect := legacyArchiveInspect
			originalLink := legacyArchiveLink
			originalSync := legacyArchiveSync
			originalRemove := legacyArchiveRemove
			t.Cleanup(func() {
				legacyArchiveInspect = originalInspect
				legacyArchiveLink = originalLink
				legacyArchiveSync = originalSync
				legacyArchiveRemove = originalRemove
			})
			archiveInspections := 0
			legacyArchiveInspect = func(path string, limit int64) (legacyFileSnapshot, bool, error) {
				if path != archivePath {
					return originalInspect(path, limit)
				}
				archiveInspections++
				switch failure {
				case "destination-inspect":
					if archiveInspections == 1 {
						return legacyFileSnapshot{}, false, errors.New("destination inspect failed")
					}
				case "missing-after-link":
					if archiveInspections == 2 {
						return legacyFileSnapshot{}, false, nil
					}
				case "changed-after-link":
					if archiveInspections == 2 {
						return legacyFileSnapshot{digest: sha256.Sum256([]byte("changed")), mode: 0o600}, true, nil
					}
				}
				return originalInspect(path, limit)
			}
			if err := archiveLegacySource(
				sourcePath,
				archivePath,
				archiveRoot,
				digest[:],
				1024,
				0o600,
			); err == nil {
				t.Fatal("archiveLegacySource() ignored injected verification failure")
			}
			if _, err := os.Stat(sourcePath); err != nil {
				t.Fatalf("verification failure removed source: %v", err)
			}
		})
	}
}

func TestArchivePendingRecordValidationAndUpdateFailures(t *testing.T) {
	for _, failure := range []string{
		"invalid-id", "archive-relative", "symlink-parent", "update-error", "update-ignored",
	} {
		t.Run(failure, func(t *testing.T) {
			root, sourcePath, archivePath, databasePath, options := pendingArchiveFixture(t)
			if err := os.RemoveAll(filepath.Join(root, "legacy-json")); err != nil {
				t.Fatal(err)
			}
			raw, err := sql.Open("sqlite", databasePath)
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "invalid-id":
				_, err = raw.Exec(`UPDATE storage_imports SET source_id = 'bad!'`)
			case "archive-relative":
				archiveRelative, relErr := filepath.Rel(root, archivePath)
				if relErr != nil {
					t.Fatal(relErr)
				}
				_, err = raw.Exec(
					`UPDATE storage_imports SET source_relative = ?`,
					filepath.ToSlash(archiveRelative),
				)
			case "symlink-parent":
				outside := t.TempDir()
				if err = os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(sourcePath, filepath.Join(outside, "source.json")); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(filepath.Join(root, "nested")); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(outside, filepath.Join(root, "nested")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				_, err = raw.Exec(`UPDATE storage_imports SET source_relative = 'nested/source.json'`)
			case "update-error":
				_, err = raw.Exec(`CREATE TRIGGER reject_archive_update
					BEFORE UPDATE ON storage_imports
					BEGIN SELECT RAISE(ABORT, 'rejected'); END`)
			case "update-ignored":
				_, err = raw.Exec(`CREATE TRIGGER ignore_archive_update
					BEFORE UPDATE ON storage_imports
					BEGIN SELECT RAISE(IGNORE); END`)
			}
			if err != nil {
				raw.Close()
				t.Fatal(err)
			}
			err = archiveImportedSources(
				t.Context(),
				raw,
				"test-store",
				*options.Legacy,
			)
			if closeErr := raw.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if err == nil {
				t.Fatalf("archive accepted pending-record failure %s", failure)
			}
		})
	}

	t.Run("list query error", func(t *testing.T) {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "closed.db"))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		options := *legacyTestOptions(root, nil, nil)
		if err := archiveImportedSources(t.Context(), db, "test-store", options); err == nil {
			t.Fatal("archiveImportedSources() accepted closed database")
		}
	})

	t.Run("missing import table", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		root := t.TempDir()
		options := *legacyTestOptions(root, nil, nil)
		if err := archiveImportedSources(t.Context(), db, "test-store", options); err == nil ||
			!strings.Contains(err.Error(), "pending legacy archives") {
			t.Fatalf("archiveImportedSources() error = %v", err)
		}
	})

	t.Run("malformed import row", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`CREATE TABLE storage_imports (
			component TEXT,
			source_id TEXT,
			source_relative TEXT,
			source_digest BLOB,
			source_limit TEXT,
			source_mode INTEGER,
			archive_status TEXT
		)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO storage_imports VALUES (
			'test-store', 'source', 'source.json', zeroblob(32), 'not-an-integer', 384, 'pending'
		)`); err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		options := *legacyTestOptions(root, nil, nil)
		if err := archiveImportedSources(t.Context(), db, "test-store", options); err == nil ||
			!strings.Contains(err.Error(), "scan") {
			t.Fatalf("archiveImportedSources() error = %v", err)
		}
	})

	t.Run("invalid roots", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := archiveImportedSources(
			t.Context(), db, "test-store", LegacyOptions{},
		); err == nil {
			t.Fatal("archiveImportedSources() accepted invalid roots")
		}
	})
}

type delayedErrorContext struct {
	context.Context
	remaining atomic.Int32
}

func (ctx *delayedErrorContext) Err() error {
	if ctx.remaining.Add(-1) < 0 {
		return context.Canceled
	}
	return nil
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestArchiveHonorsContextBetweenPendingSources(t *testing.T) {
	root := t.TempDir()
	writeLegacyTestFile(t, root, "a.json", "a")
	writeLegacyTestFile(t, root, "b.json", "b")
	archiveRoot := filepath.Join(root, "legacy-json", "test-store-v1")
	db, err := sql.Open("sqlite", filepath.Join(root, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(importSchema); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"a", "b"} {
		data := []byte(source)
		digest := sha256.Sum256(data)
		if _, err := db.Exec(
			`INSERT INTO storage_imports (
				component, source_id, source_relative, source_digest, source_size,
				source_limit, source_mode, imported_count, skipped_count,
				archive_status, imported_at
			) VALUES ('test-store', ?, ?, ?, 1, 10, 384, 1, 0, 'pending', 1)`,
			source,
			source+".json",
			digest[:],
		); err != nil {
			t.Fatal(err)
		}
	}
	ctx := &delayedErrorContext{Context: context.Background()}
	// Immediate currently checks Err once, then the archive loop checks once
	// per source. Permit transaction admission and the first source only.
	ctx.remaining.Store(1)
	err = archiveImportedSources(ctx, db, "test-store", LegacyOptions{
		SourceRoot: root, ArchiveRoot: archiveRoot,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("archiveImportedSources() error = %v", err)
	}
}

func TestArchivePropagatesRowsIterationFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(importSchema); err != nil {
		t.Fatal(err)
	}
	originalRowsErr := legacyArchiveRowsErr
	legacyArchiveRowsErr = func(*sql.Rows) error { return errors.New("rows failed") }
	t.Cleanup(func() { legacyArchiveRowsErr = originalRowsErr })
	if err := archiveImportedSources(t.Context(), db, "test-store", LegacyOptions{
		SourceRoot:  root,
		ArchiveRoot: filepath.Join(root, "archive"),
	}); err == nil || !strings.Contains(err.Error(), "rows failed") {
		t.Fatalf("archiveImportedSources() error = %v", err)
	}
}

func TestEnsureArchiveParentNestedAndHostile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "archive")
	destination := filepath.Join(root, "deep", "nested", "source.json")
	if err := ensureArchiveParent(root, destination); err != nil {
		t.Fatalf("ensureArchiveParent() error = %v", err)
	}
	if info, err := os.Stat(filepath.Dir(destination)); err != nil || !info.IsDir() {
		t.Fatalf("archive parent = %#v, %v", info, err)
	}
	if err := ensureArchiveParent(root, filepath.Join(filepath.Dir(root), "outside.json")); err == nil {
		t.Fatal("ensureArchiveParent() accepted escaping destination")
	}
	blockedRoot := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blockedRoot, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureArchiveParent(blockedRoot, filepath.Join(blockedRoot, "source.json")); err == nil {
		t.Fatal("ensureArchiveParent() accepted file root")
	}
	symlinkRoot := filepath.Join(t.TempDir(), "archive")
	if err := os.MkdirAll(symlinkRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(symlinkRoot, "link")); err == nil {
		if err := ensureArchiveParent(
			symlinkRoot,
			filepath.Join(symlinkRoot, "link", "source.json"),
		); err == nil {
			t.Fatal("ensureArchiveParent() accepted symlink component")
		}
	}
	originalMkdir := legacyArchiveMkdir
	originalChmod := legacyArchiveChmod
	t.Cleanup(func() {
		legacyArchiveMkdir = originalMkdir
		legacyArchiveChmod = originalChmod
	})
	mkdirRoot := filepath.Join(t.TempDir(), "mkdir-root")
	if err := os.MkdirAll(mkdirRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyArchiveMkdir = func(string, os.FileMode) error { return errors.New("mkdir failed") }
	if err := ensureArchiveParent(
		mkdirRoot,
		filepath.Join(mkdirRoot, "missing", "source.json"),
	); err == nil {
		t.Fatal("ensureArchiveParent() ignored mkdir failure")
	}
	legacyArchiveMkdir = originalMkdir
	chmodRoot := filepath.Join(t.TempDir(), "chmod-root")
	if err := os.MkdirAll(filepath.Join(chmodRoot, "existing"), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyArchiveChmod = func(string, os.FileMode) error { return errors.New("chmod failed") }
	if err := ensureArchiveParent(
		chmodRoot,
		filepath.Join(chmodRoot, "existing", "source.json"),
	); err == nil {
		t.Fatal("ensureArchiveParent() ignored chmod failure")
	}
	legacyArchiveChmod = originalChmod
	missingRoot := filepath.Join(t.TempDir(), "missing-after-mkdir")
	if err := os.MkdirAll(missingRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyArchiveMkdir = func(string, os.FileMode) error { return nil }
	if err := ensureArchiveParent(
		missingRoot,
		filepath.Join(missingRoot, "never-created", "source.json"),
	); err == nil {
		t.Fatal("ensureArchiveParent() ignored missing directory after mkdir")
	}
}

func TestLegacyPathResolutionErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("removing the process working directory is not portable on Windows")
	}
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	unavailable := t.TempDir()
	if err := os.Chdir(unavailable); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if restoreErr := os.Chdir(originalWorkingDirectory); restoreErr != nil {
			t.Errorf("restore working directory: %v", restoreErr)
		}
	})
	if err := os.Remove(unavailable); err != nil {
		t.Fatal(err)
	}
	absRoot := filepath.Join(originalWorkingDirectory, "absolute-root")
	if err := validateLegacyRoots(LegacyOptions{
		SourceRoot: "relative", ArchiveRoot: "relative/archive",
	}); err == nil {
		t.Fatal("validateLegacyRoots() ignored source Abs failure")
	}
	if err := validateLegacyRoots(LegacyOptions{
		SourceRoot: absRoot, ArchiveRoot: "relative/archive",
	}); err == nil {
		t.Fatal("validateLegacyRoots() ignored archive Abs failure")
	}
	if err := validateLegacySourceOutsideArchive(
		LegacyOptions{SourceRoot: "relative", ArchiveRoot: absRoot},
		"source.json",
	); err == nil {
		t.Fatal("validateLegacySourceOutsideArchive() ignored source Abs failure")
	}
	if err := validateLegacySourceOutsideArchive(
		LegacyOptions{SourceRoot: absRoot, ArchiveRoot: "relative"},
		"source.json",
	); err == nil {
		t.Fatal("validateLegacySourceOutsideArchive() ignored archive Abs failure")
	}
	if pathWithin("relative", absRoot) || pathWithin(absRoot, "relative") {
		t.Fatal("pathWithin() accepted Abs failure")
	}
	if pathsEqual("relative", absRoot) || pathsEqual(absRoot, "relative") {
		t.Fatal("pathsEqual() accepted Abs failure")
	}
}

func TestRejectSymlinkPathRootAndMissingComponentErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if err := rejectSymlinkPath(missing, "source.json"); err == nil {
		t.Fatal("rejectSymlinkPath() accepted missing root")
	}
	fileRoot := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(fileRoot, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rejectSymlinkPath(fileRoot, "source.json"); err == nil {
		t.Fatal("rejectSymlinkPath() accepted file root")
	}
	realRoot := t.TempDir()
	if err := rejectSymlinkPath(realRoot, "missing/source.json"); err == nil {
		t.Fatal("rejectSymlinkPath() accepted missing intermediate component")
	}
}

func TestConcurrentOpenImportsAndArchivesOnce(t *testing.T) {
	root := t.TempDir()
	writeLegacyTestFile(t, root, "legacy.json", "record")
	var imports atomic.Int32
	options := testOptions()
	options.Legacy = legacyTestOptions(
		root,
		[]LegacySource{{ID: "legacy", Relative: "legacy.json"}},
		func(ctx context.Context, conn *sql.Conn, input LegacyInput) (ImportResult, error) {
			imports.Add(1)
			_, err := conn.ExecContext(
				ctx,
				`INSERT INTO records(id, value) VALUES ('record', ?)`,
				string(input.Data),
			)
			return ImportResult{Imported: 1}, err
		},
	)
	databasePath := filepath.Join(root, "db", "store.db")
	start := make(chan struct{})
	errorsCh := make(chan error, 4)
	var wait sync.WaitGroup
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			db, err := Open(context.Background(), databasePath, options)
			if err == nil {
				err = db.Close()
			}
			errorsCh <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent Open() error = %v", err)
		}
	}
	if imports.Load() != 1 {
		t.Fatalf("imports = %d, want 1", imports.Load())
	}
	archivePath := filepath.Join(root, "legacy-json", "test-store-v1", "legacy.json")
	if data, err := os.ReadFile(archivePath); err != nil || string(data) != "record" {
		t.Fatalf("archive = %q, %v", data, err)
	}
}

func pendingArchiveFixture(
	t *testing.T,
) (root, sourcePath, archivePath, databasePath string, options Options) {
	t.Helper()
	root = t.TempDir()
	sourcePath = filepath.Join(root, "legacy.json")
	writeLegacyTestFile(t, root, "legacy.json", "payload")
	legacy := legacyTestOptions(
		root,
		[]LegacySource{{ID: "legacy", Relative: "legacy.json"}},
		func(ctx context.Context, conn *sql.Conn, input LegacyInput) (ImportResult, error) {
			_, err := conn.ExecContext(
				ctx,
				`INSERT INTO records(id, value) VALUES ('legacy', ?)`,
				string(input.Data),
			)
			return ImportResult{Imported: 1}, err
		},
	)
	options = testOptions()
	options.Legacy = legacy
	databasePath = filepath.Join(root, "db", "store.db")
	originalMkdir := legacyArchiveMkdir
	legacyArchiveMkdir = func(string, os.FileMode) error {
		return errors.New("injected archive creation failure")
	}
	if db, err := Open(t.Context(), databasePath, options); err == nil {
		db.Close()
		legacyArchiveMkdir = originalMkdir
		t.Fatal("Open() unexpectedly archived through an obstructed root")
	}
	legacyArchiveMkdir = originalMkdir
	archivePath = filepath.Join(legacy.ArchiveRoot, "legacy.json")
	return root, sourcePath, archivePath, databasePath, options
}

func legacyTestOptions(
	root string,
	sources []LegacySource,
	importer LegacyImporter,
) *LegacyOptions {
	_ = os.Chmod(root, 0o700)
	if importer == nil {
		importer = func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
			return ImportResult{}, nil
		}
	}
	return &LegacyOptions{
		SourceRoot:  root,
		ArchiveRoot: filepath.Join(root, "legacy-json", "test-store-v1"),
		Sources: func() ([]LegacySource, error) {
			return append([]LegacySource(nil), sources...), nil
		},
		Import: importer,
		Now: func() time.Time {
			return time.Date(2026, 8, 31, 12, 34, 56, 789, time.UTC)
		},
	}
}

func writeLegacyTestFile(t *testing.T, root, relative, data string) {
	t.Helper()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestImportIssueDigestRemainsPayloadFree(t *testing.T) {
	digest := sha256.Sum256([]byte("secret payload"))
	result := ImportResult{
		Skipped: 1,
		Issues:  []ImportIssue{{Code: "invalid-record", RecordDigest: digest}},
	}
	if err := validateImportResult(result); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(result.Issues[0]) == "secret payload" {
		t.Fatal("issue retained source payload")
	}
}

func TestLegacyValidationHelpersRejectInvalidValues(t *testing.T) {
	validDigest := sha256.Sum256([]byte("record"))
	invalidResults := []ImportResult{
		{Imported: -1},
		{Imported: maximumLegacyRecordCount + 1},
		{Skipped: -1},
		{Skipped: maximumLegacyRecordCount + 1},
		{Issues: make([]ImportIssue, maxImportIssues+1), Skipped: maxImportIssues + 1},
		{Issues: []ImportIssue{{Code: "bad!", RecordDigest: validDigest}}, Skipped: 1},
		{Issues: []ImportIssue{{Code: "bad", RecordDigest: [sha256.Size]byte{}}}, Skipped: 1},
	}
	for index, result := range invalidResults {
		if err := validateImportResult(result); err == nil {
			t.Fatalf("validateImportResult(%d) unexpectedly succeeded", index)
		}
	}
	for _, path := range []string{"", ".", "..", "../escape", "/absolute", "bad\x00path"} {
		if validRelativePath(path) {
			t.Fatalf("validRelativePath(%q) = true", path)
		}
	}
	if equalDigest([]byte("short"), validDigest[:]) {
		t.Fatal("equalDigest() accepted short input")
	}
	if !pathsEqual(".", filepath.Clean(".")) {
		t.Fatal("pathsEqual() rejected equivalent paths")
	}
	if legacyNow(LegacyOptions{}).IsZero() {
		t.Fatal("legacyNow() returned zero time")
	}
	for _, options := range []LegacyOptions{
		{},
		{SourceRoot: " root", ArchiveRoot: "archive"},
		{SourceRoot: "/tmp/root", ArchiveRoot: "/tmp/root"},
		{SourceRoot: "/tmp/root", ArchiveRoot: "/tmp/other"},
	} {
		if err := validateLegacyRoots(options); err == nil {
			t.Fatalf("validateLegacyRoots(%#v) unexpectedly succeeded", options)
		}
	}
}
