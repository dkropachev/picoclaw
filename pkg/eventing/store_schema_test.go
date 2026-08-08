//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
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
	assert.Contains(t, err.Error(), "create eventing schema v1")

	db = openSchemaTestDB(t, path)
	defer db.Close()

	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Zero(t, version, "failed migration must not advance the schema version")
	assert.False(
		t,
		schemaObjectExists(t, db, "table", "event_inbox"),
		"tables created before the migration failure must roll back",
	)
	assert.True(
		t,
		schemaObjectExists(t, db, "table", "event_dispatches"),
		"preexisting schema objects must survive migration rollback",
	)
	assert.False(
		t,
		schemaObjectExists(t, db, "index", "event_inbox_dedupe"),
		"indexes created before the migration failure must roll back",
	)
}

func TestStoreMigrationValidationFailureRollsBackVersion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-validation-rollback.db")
	db := openSchemaTestDB(t, path)
	installSchemaV1ForTest(t, db)
	_, err := db.Exec(`DROP INDEX event_inbox_dedupe`)
	require.NoError(t, err)
	_, err = db.Exec(`
		CREATE UNIQUE INDEX event_inbox_dedupe
		ON event_inbox(source)
		WHERE dedupe_key <> ''`)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 0)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Zero(t, version, "validation failure must roll back the version advance")
}

func TestStoreMigratesV1DispatchesWithoutInventingWorkflowRevision(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v1-revision.db")
	db := openSchemaTestDB(t, path)
	_, err := db.Exec(schemaV1)
	require.NoError(t, err)
	const (
		eventID    = "ev_00000000000000000000000000000001"
		dispatchID = "dsp_00000000000000000000000000000001"
		runID      = "wr_00000000000000000000000000000001"
	)
	_, err = db.Exec(`
		INSERT INTO event_inbox (
			id, source, connector, event_type, dedupe_key, received_at,
			payload_json, attributes_json, routing_status,
			routing_available_at, routing_updated_at
		) VALUES (?, 'github', 'primary', 'issues.opened', '', 1,
			'{}', '{}', 'succeeded', 1, 1
		)`,
		eventID,
	)
	require.NoError(t, err)
	_, err = db.Exec(`
		INSERT INTO event_dispatches (
			id, event_id, workflow_ref, run_id, status,
			available_at, created_at, updated_at
		) VALUES (?, ?, 'workflows/legacy.yml', ?, 'pending', 1, 1, 1)`,
		dispatchID,
		eventID,
		runID,
	)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 1)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	defer store.Close()
	legacy, err := store.GetDispatch(context.Background(), dispatchID)
	require.NoError(t, err)
	assert.Empty(t, legacy.WorkflowRevision)

	var version int
	require.NoError(t, store.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, schemaVersion, version)
	assert.True(
		t,
		schemaObjectExists(t, store.db, "table", "event_dispatch_workflow_revisions"),
	)
}

func TestStoreMigratesV2ToReviewSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v2-review.db")
	db := openSchemaTestDB(t, path)
	_, err := db.Exec(schemaV1)
	require.NoError(t, err)
	_, err = db.Exec(schemaV2)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 2)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	defer store.Close()

	var version int
	require.NoError(t, store.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, schemaVersion, version)
	for _, object := range []struct {
		kind string
		name string
	}{
		{kind: "table", name: "pr_review_cases"},
		{kind: "table", name: "pr_review_findings"},
		{kind: "table", name: "pr_review_messages"},
		{kind: "table", name: "pr_review_submissions"},
		{kind: "index", name: "pr_review_cases_list"},
		{kind: "index", name: "pr_review_submissions_claim"},
	} {
		assert.True(t, schemaObjectExists(t, store.db, object.kind, object.name))
	}
}

func TestStoreReviewMigrationValidationFailureRollsBackVersion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v3-review-rollback.db")
	db := openSchemaTestDB(t, path)
	_, err := db.Exec(schemaV1)
	require.NoError(t, err)
	_, err = db.Exec(schemaV2)
	require.NoError(t, err)
	malformedReviewCases := strings.Replace(
		schemaV3ReviewCasesTable,
		"CHECK (active_findings <= total_findings)",
		"CHECK (active_findings <= total_findings AND total_findings < 999)",
		1,
	)
	_, err = db.Exec(malformedReviewCases)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 2)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v3")

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 2, version, "failed v3 validation must roll back the version")
	assert.False(
		t,
		schemaObjectExists(t, db, "table", "pr_review_findings"),
		"objects created by the failed v3 migration must roll back",
	)
}

func TestStoreRejectsCurrentReviewSchemaMissingRequiredIndex(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing-review-index.db")
	db := openSchemaTestDB(t, path)
	installSchemaV1ForTest(t, db)
	_, err := db.Exec(`DROP INDEX pr_review_submissions_claim`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v3")
	var validationErr *schemaValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Equal(t, "pr_review_submissions_claim", validationErr.object)
}

func TestStoreRejectsCurrentSchemaMissingDispatchRevisionBindings(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing-revision-bindings.db")
	db := openSchemaTestDB(t, path)
	_, err := db.Exec(schemaV1)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, schemaVersion)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v2")
	var validationErr *schemaValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Equal(t, "event_dispatch_workflow_revisions", validationErr.object)
}

func TestStoreRejectsInvalidCurrentSchema(t *testing.T) {
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
				setSchemaTestVersion(t, db, schemaVersion)
			},
			wantObject: "event_inbox",
		},
		{
			name: "malformed table",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				_, err := db.Exec(`CREATE TABLE event_inbox (id TEXT PRIMARY KEY)`)
				require.NoError(t, err)
				setSchemaTestVersion(t, db, schemaVersion)
			},
			wantObject: "event_inbox",
		},
		{
			name: "missing required index",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				installSchemaV1ForTest(t, db)
				_, err := db.Exec(`DROP INDEX event_inbox_dedupe`)
				require.NoError(t, err)
			},
			wantObject: "event_inbox_dedupe",
		},
		{
			name: "malformed required index",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				installSchemaV1ForTest(t, db)
				_, err := db.Exec(`DROP INDEX event_inbox_dedupe`)
				require.NoError(t, err)
				_, err = db.Exec(`
					CREATE UNIQUE INDEX event_inbox_dedupe
					ON event_inbox(source)
					WHERE dedupe_key <> ''`)
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
			assert.Contains(t, err.Error(), "validate eventing schema v1")

			var validationErr *schemaValidationError
			require.ErrorAs(t, err, &validationErr)
			assert.Equal(t, test.wantObject, validationErr.object)

			db = openSchemaTestDB(t, path)
			defer db.Close()
			var version int
			require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
			assert.Equal(t, schemaVersion, version)
		})
	}
}

func TestStoreRejectsAdversarialCurrentSchema(t *testing.T) {
	t.Parallel()

	commentedConstraint := strings.Replace(
		schemaV1EventInboxTable,
		"CHECK (routing_attempts >= 0)",
		"/* CHECK (routing_attempts >= 0) */",
		1,
	)
	carriageReturnCommentedConstraint := strings.Replace(
		schemaV1EventInboxTable,
		"\trouting_attempts INTEGER NOT NULL DEFAULT 0 CHECK (routing_attempts >= 0),",
		"\trouting_attempts INTEGER NOT NULL DEFAULT 0 -- operator note\r"+
			"CHECK (routing_attempts >= 0)\n,",
		1,
	)
	extendedStatusConstraint := strings.Replace(
		schemaV1EventInboxTable,
		"'succeeded', 'dead'",
		"'succeeded', 'dead', 'paused'",
		1,
	)
	generatedUniqueColumn := strings.Replace(
		schemaV1EventInboxTable,
		"\trouting_updated_at INTEGER NOT NULL,\n",
		"\trouting_updated_at INTEGER NOT NULL,\n"+
			"\trogue_key TEXT GENERATED ALWAYS AS (source) STORED UNIQUE,\n",
		1,
	)
	changedUniqueConflict := strings.Replace(
		schemaV1EventDispatchesTable,
		"run_id TEXT NOT NULL UNIQUE,",
		"run_id TEXT NOT NULL UNIQUE ON CONFLICT REPLACE,",
		1,
	)
	changedStatusLiteralCase := strings.Replace(
		schemaV1EventDispatchesTable,
		"'pending'",
		"'PENDING'",
		1,
	)

	tests := []struct {
		name        string
		schema      string
		wantObject  string
		wantProblem string
	}{
		{
			name: "commented-out check constraint",
			schema: strings.Replace(
				schemaV1,
				schemaV1EventInboxTable,
				commentedConstraint,
				1,
			),
			wantObject:  "event_inbox",
			wantProblem: "definition differs",
		},
		{
			name: "carriage return does not end line comment",
			schema: strings.Replace(
				schemaV1,
				schemaV1EventInboxTable,
				carriageReturnCommentedConstraint,
				1,
			),
			wantObject:  "event_inbox",
			wantProblem: "definition differs",
		},
		{
			name: "extended check constraint",
			schema: strings.Replace(
				schemaV1,
				schemaV1EventInboxTable,
				extendedStatusConstraint,
				1,
			),
			wantObject:  "event_inbox",
			wantProblem: "definition differs",
		},
		{
			name: "generated unique column",
			schema: strings.Replace(
				schemaV1,
				schemaV1EventInboxTable,
				generatedUniqueColumn,
				1,
			),
			wantObject:  "event_inbox",
			wantProblem: "hidden or generated",
		},
		{
			name: "changed unique conflict behavior",
			schema: strings.Replace(
				schemaV1,
				schemaV1EventDispatchesTable,
				changedUniqueConflict,
				1,
			),
			wantObject:  "event_dispatches",
			wantProblem: "definition differs",
		},
		{
			name: "changed status literal case",
			schema: strings.Replace(
				schemaV1,
				schemaV1EventDispatchesTable,
				changedStatusLiteralCase,
				1,
			),
			wantObject:  "event_dispatches",
			wantProblem: "definition differs",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "adversarial-current.db")
			db := openSchemaTestDB(t, path)
			installSchemaTextForTest(t, db, test.schema)
			require.NoError(t, db.Close())

			store, err := Open(context.Background(), path)
			require.Error(t, err)
			assert.Nil(t, store)
			assert.ErrorIs(t, err, ErrSchemaInvalid)

			var validationErr *schemaValidationError
			require.ErrorAs(t, err, &validationErr)
			assert.Equal(t, test.wantObject, validationErr.object)
			assert.Contains(t, validationErr.problem, test.wantProblem)
		})
	}
}

func TestStoreRejectsUnexpectedUniqueIndex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		indexName string
	}{
		{
			name:      "ordinary name",
			indexName: "rogue_unique_source",
		},
		{
			name:      "pragma injection name",
			indexName: "rogue_unique'); DROP TABLE event_dispatches; --",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "unexpected-unique-index.db")
			db := openSchemaTestDB(t, path)
			installSchemaV1ForTest(t, db)
			_, err := db.Exec(
				`CREATE UNIQUE INDEX "` +
					strings.ReplaceAll(test.indexName, `"`, `""`) +
					`" ON event_inbox(source)`,
			)
			require.NoError(t, err)
			require.NoError(t, db.Close())

			store, err := Open(context.Background(), path)
			require.Error(t, err)
			assert.Nil(t, store)
			assert.ErrorIs(t, err, ErrSchemaInvalid)

			var validationErr *schemaValidationError
			require.ErrorAs(t, err, &validationErr)
			assert.Equal(t, test.indexName, validationErr.object)
			assert.Contains(t, validationErr.problem, "unexpected unique index")

			db = openSchemaTestDB(t, path)
			defer db.Close()
			assert.True(t, schemaObjectExists(t, db, "table", "event_dispatches"))
		})
	}
}

func TestStoreAllowsUnexpectedNonUniqueIndex(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "operator-non-unique-index.db")
	db := openSchemaTestDB(t, path)
	installSchemaV1ForTest(t, db)
	_, err := db.Exec(`CREATE INDEX operator_event_source ON event_inbox(source)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	require.NotNil(t, store)
	require.NoError(t, store.Close())
}

func TestCanonicalSchemaSQLIsCommentSafeAndTokenAware(t *testing.T) {
	t.Parallel()

	expected, err := canonicalSchemaSQL(`
		CREATE TABLE example (
			value TEXT NOT NULL,
			status TEXT CHECK (status IN ('pending', '--literal', '/*literal*/'))
		)`)
	require.NoError(t, err)

	withHarmlessDifferences, err := canonicalSchemaSQL(`
		create table if not exists example (
			value text -- the constraint continues on the next line
				not null,
			status text /* operator note */
				check(status in ('pending', '--literal', '/*literal*/'))
		); -- trailing operator note`)
	require.NoError(t, err)
	assert.Equal(t, expected, withHarmlessDifferences)

	changedLiteral, err := canonicalSchemaSQL(`
		CREATE TABLE example (
			value TEXT NOT NULL,
			status TEXT CHECK (status IN ('PENDING', '--literal', '/*literal*/'))
		)`)
	require.NoError(t, err)
	assert.NotEqual(t, expected, changedLiteral, "quoted status literals are case-sensitive")

	mergedTokens, err := canonicalSchemaSQL(`
		CREATE TABLE example (
			value TEXTNOTNULL,
			status TEXT CHECK (status IN ('pending', '--literal', '/*literal*/'))
		)`)
	require.NoError(t, err)
	assert.NotEqual(t, expected, mergedTokens, "whitespace removal must not merge SQL tokens")

	asciiIdentifier, err := canonicalSchemaSQL(
		`CREATE TABLE example (workflow_ref TEXT)`,
	)
	require.NoError(t, err)
	unicodeLookalike, err := canonicalSchemaSQL(
		"CREATE TABLE example (wor\u212Aflow_ref TEXT)",
	)
	require.NoError(t, err)
	assert.NotEqual(
		t,
		asciiIdentifier,
		unicodeLookalike,
		"Unicode lookalikes must not canonicalize as ASCII identifiers",
	)

	nonSQLiteWhitespace, err := canonicalSchemaSQL(
		"CREATE TABLE example (workflow_ref TEXT\u00a0NOT NULL)",
	)
	require.NoError(t, err)
	asciiWhitespace, err := canonicalSchemaSQL(
		"CREATE TABLE example (workflow_ref TEXT NOT NULL)",
	)
	require.NoError(t, err)
	assert.NotEqual(
		t,
		asciiWhitespace,
		nonSQLiteWhitespace,
		"non-ASCII space must not be discarded as SQL whitespace",
	)
	verticalTab, err := canonicalSchemaSQL(
		"CREATE TABLE example (workflow_ref TEXT\vNOT NULL)",
	)
	require.NoError(t, err)
	assert.NotEqual(
		t,
		asciiWhitespace,
		verticalTab,
		"vertical tab is not SQLite SQL whitespace",
	)
}

func openSchemaTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	require.NoError(t, db.Ping())
	return db
}

func installSchemaV1ForTest(t *testing.T, db *sql.DB) {
	t.Helper()

	_, err := db.Exec(schemaV1)
	require.NoError(t, err)
	_, err = db.Exec(schemaV2)
	require.NoError(t, err)
	_, err = db.Exec(schemaV3)
	require.NoError(t, err)
	_, err = db.Exec(schemaV4)
	require.NoError(t, err)
	_, err = db.Exec(schemaV5)
	require.NoError(t, err)
	_, err = db.Exec(schemaV6)
	require.NoError(t, err)
	_, err = db.Exec(schemaV7)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, schemaVersion)
}

func installSchemaTextForTest(t *testing.T, db *sql.DB, schema string) {
	t.Helper()

	_, err := db.Exec(schema)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, schemaVersion)
}

func setSchemaTestVersion(t *testing.T, db *sql.DB, version int) {
	t.Helper()

	_, err := db.Exec(`PRAGMA user_version = ` + strconv.Itoa(version))
	require.NoError(t, err)
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
