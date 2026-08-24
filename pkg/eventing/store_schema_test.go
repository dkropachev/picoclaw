//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreMigrationFailureRollsBack(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-rollback.db")
	db := openSchemaTestDB(t, path)
	_, err := db.Exec(`CREATE TABLE event_dispatches (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)
	_, err = db.Exec(`PRAGMA user_version = 0`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Zero(t, version, "failed migration must not advance the schema version")
	assert.False(t, schemaObjectExists(t, db, "table", "event_inbox"))
	assert.True(t, schemaObjectExists(t, db, "table", "event_dispatches"))
	assert.False(t, schemaObjectExists(t, db, "index", "event_inbox_dedupe"))
}

func TestStoreRejectsCurrentSchemaMissingDispatchRevisionBindings(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing-revision-bindings.db")
	db := openSchemaTestDB(t, path)
	_, err := db.Exec(schemaV1)
	require.NoError(t, err)
	_, err = db.Exec(schemaV19PRWorkspace)
	require.NoError(t, err)
	setCurrentSchemaVersionForTest(t, db)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate retained workflow revision schema")
	var validationErr *schemaValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Equal(t, "event_dispatch_workflow_revisions", validationErr.object)
}

func TestStoreRejectsInvalidCurrentRetainedSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setup      func(*testing.T, *sql.DB)
		wantObject string
	}{
		{
			name: "missing tables",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				setCurrentSchemaVersionForTest(t, db)
			},
			wantObject: "event_inbox",
		},
		{
			name: "malformed table",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				_, err := db.Exec(`CREATE TABLE event_inbox (id TEXT PRIMARY KEY)`)
				require.NoError(t, err)
				setCurrentSchemaVersionForTest(t, db)
			},
			wantObject: "event_inbox",
		},
		{
			name: "missing required index",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				installCurrentSchemaForTest(t, db)
				_, err := db.Exec(`DROP INDEX event_inbox_dedupe`)
				require.NoError(t, err)
			},
			wantObject: "event_inbox_dedupe",
		},
		{
			name: "malformed required index",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				installCurrentSchemaForTest(t, db)
				_, err := db.Exec(`DROP INDEX event_inbox_dedupe`)
				require.NoError(t, err)
				_, err = db.Exec(`CREATE UNIQUE INDEX event_inbox_dedupe
					ON event_inbox(source) WHERE dedupe_key <> ''`)
				require.NoError(t, err)
			},
			wantObject: "event_inbox_dedupe",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "invalid-current.db")
			db := openSchemaTestDB(t, path)
			test.setup(t, db)
			require.NoError(t, db.Close())

			store, err := Open(context.Background(), path)
			require.Error(t, err)
			assert.Nil(t, store)
			assert.ErrorIs(t, err, ErrSchemaInvalid)
			assert.Contains(t, err.Error(), "validate retained event inbox schema")
			var validationErr *schemaValidationError
			require.ErrorAs(t, err, &validationErr)
			assert.Equal(t, test.wantObject, validationErr.object)
		})
	}
}

func TestStoreRejectsUnexpectedUniqueIndex(t *testing.T) {
	t.Parallel()

	for _, indexName := range []string{
		"rogue_unique_source",
		"rogue_unique'); DROP TABLE event_dispatches; --",
	} {
		t.Run(indexName, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "unexpected-unique-index.db")
			db := openSchemaTestDB(t, path)
			installCurrentSchemaForTest(t, db)
			_, err := db.Exec(`CREATE UNIQUE INDEX "` +
				strings.ReplaceAll(indexName, `"`, `""`) + `" ON event_inbox(source)`)
			require.NoError(t, err)
			require.NoError(t, db.Close())

			store, err := Open(context.Background(), path)
			require.Error(t, err)
			assert.Nil(t, store)
			assert.ErrorIs(t, err, ErrSchemaInvalid)
			var validationErr *schemaValidationError
			require.ErrorAs(t, err, &validationErr)
			assert.Equal(t, indexName, validationErr.object)
			assert.Contains(t, validationErr.problem, "unexpected unique index")
		})
	}
}

func TestStoreAllowsUnexpectedNonUniqueIndex(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "operator-non-unique-index.db")
	db := openSchemaTestDB(t, path)
	installCurrentSchemaForTest(t, db)
	_, err := db.Exec(`CREATE INDEX operator_event_source ON event_inbox(source)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, store.Close())
}

func TestCanonicalSchemaSQLIsCommentSafeAndTokenAware(t *testing.T) {
	t.Parallel()

	expected, err := canonicalSchemaSQL(`CREATE TABLE example (
		value TEXT NOT NULL,
		status TEXT CHECK (status IN ('pending', '--literal', '/*literal*/'))
	)`)
	require.NoError(t, err)

	withHarmlessDifferences, err := canonicalSchemaSQL(`create table if not exists example (
		value text -- the constraint continues on the next line
			not null,
		status text /* operator note */
			check(status in ('pending', '--literal', '/*literal*/'))
	); -- trailing operator note`)
	require.NoError(t, err)
	assert.Equal(t, expected, withHarmlessDifferences)

	changedLiteral, err := canonicalSchemaSQL(`CREATE TABLE example (
		value TEXT NOT NULL,
		status TEXT CHECK (status IN ('PENDING', '--literal', '/*literal*/'))
	)`)
	require.NoError(t, err)
	assert.NotEqual(t, expected, changedLiteral)

	mergedTokens, err := canonicalSchemaSQL(`CREATE TABLE example (
		value TEXTNOTNULL,
		status TEXT CHECK (status IN ('pending', '--literal', '/*literal*/'))
	)`)
	require.NoError(t, err)
	assert.NotEqual(t, expected, mergedTokens)
}

func installCurrentSchemaForTest(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(schemaV1 + "\n" + schemaV2 + "\n" + schemaV19PRWorkspace)
	require.NoError(t, err)
	setCurrentSchemaVersionForTest(t, db)
}

func setCurrentSchemaVersionForTest(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	require.NoError(t, err)
}

func openSchemaTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	require.NoError(t, db.Ping())
	return db
}

func beginSchemaTestMigration(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`)
	return err
}

func commitSchemaTestMigration(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `COMMIT`)
	return err
}

func schemaObjectExists(t *testing.T, db *sql.DB, objectType, name string) bool {
	t.Helper()
	var count int
	err := db.QueryRow(
		`SELECT count(*) FROM sqlite_schema WHERE type = ? AND name = ?`,
		objectType,
		name,
	).Scan(&count)
	require.NoError(t, err)
	return count == 1
}
