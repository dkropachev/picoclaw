//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSchemaV19FreshInstallRetainsOnlyGenericAndWorkspaceSchemas(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fresh-v19.db")
	store, err := Open(context.Background(), path)
	require.NoError(t, err)

	for _, name := range []string{
		"event_inbox", "event_dispatches", "event_dispatch_workflow_revisions",
		"pr_workspaces", "pr_provider_snapshots", "pr_workspace_history",
	} {
		assert.True(t, schemaObjectExists(t, store.db, "table", name), name)
	}
	legacy, err := legacyPRTablesFromDBForTest(store)
	require.NoError(t, err)
	assert.Empty(t, legacy)
	var version int
	require.NoError(t, store.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 19, version)
	require.NoError(t, store.Close())

	reopened, err := Open(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
}

func TestSchemaV19DestructivelyCutsOverValidV18(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "v18-cutover.db")
	db := openSchemaTestDB(t, path)
	installV18CutoverFixture(t, db)
	var err error
	_, err = db.Exec(`INSERT INTO event_inbox (
		id, source, connector, event_type, dedupe_key, received_at, payload_json,
		attributes_json, routing_status, routing_available_at, routing_updated_at
	) VALUES ('ev_00000000000000000000000000000019', 'github', 'primary',
		'pull_request.synchronize', 'delivery-19', 19, '{}', '{}', 'pending', 19, 19)`)
	require.NoError(t, err)
	assert.True(t, schemaObjectExists(t, db, "table", "pr_review_cases"))
	assert.True(t, schemaObjectExists(t, db, "table", "pr_development_cases"))
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	defer store.Close()
	var retained int
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM event_inbox
		WHERE id = 'ev_00000000000000000000000000000019'`).Scan(&retained))
	assert.Equal(t, 1, retained)
	legacy, err := legacyPRTablesFromDBForTest(store)
	require.NoError(t, err)
	assert.Empty(t, legacy)
	assert.True(t, schemaObjectExists(t, store.db, "table", "pr_workspaces"))
	var version int
	require.NoError(t, store.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 19, version)
}

func TestSchemaV19RejectsCorruptV18BeforeDestructiveCutover(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "corrupt-v18.db")
	db := openSchemaTestDB(t, path)
	installV18CutoverFixture(t, db)
	_, err := db.Exec(`DROP INDEX pr_review_submissions_claim`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	assert.Nil(t, store)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	var validationErr *schemaValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Equal(t, "pr_review_submissions_claim", validationErr.object)

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 18, version)
	assert.True(t, schemaObjectExists(t, db, "table", "pr_review_cases"))
	assert.True(t, schemaObjectExists(t, db, "table", "pr_development_cases"))
	assert.False(t, schemaObjectExists(t, db, "table", "pr_workspaces"))
}

func TestSchemaV19RejectsLegacyPRTablesInAlreadyCurrentDatabase(t *testing.T) {
	t.Parallel()

	for _, table := range []string{
		"pr_review_contamination",
		"pr_development_contamination",
		"PR_REVIEW_contamination",
	} {
		t.Run(table, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "contaminated-v19.db")
			db := openSchemaTestDB(t, path)
			installCurrentSchemaForTest(t, db)
			_, err := db.Exec(`CREATE TABLE ` + table + ` (id TEXT PRIMARY KEY)`)
			require.NoError(t, err)
			require.NoError(t, db.Close())

			store, err := Open(context.Background(), path)
			assert.Nil(t, store)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrSchemaInvalid)
			var validationErr *schemaValidationError
			require.ErrorAs(t, err, &validationErr)
			assert.Equal(t, table, validationErr.object)
			assert.Contains(t, validationErr.problem, "legacy pull request schema remains")

			db = openSchemaTestDB(t, path)
			defer db.Close()
			assert.True(t, schemaObjectExists(t, db, "table", table),
				"current-schema validation must reject contamination without repairing it")
			var version int
			require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
			assert.Equal(t, 19, version)
		})
	}
}

func installV18CutoverFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, schema := range []string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7,
		schemaV8, schemaV9, schemaV10, schemaV11, schemaV12, schemaV13,
		schemaV14, schemaV15, schemaV16,
	} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	require.NoError(t, beginSchemaTestMigration(ctx, conn))
	if _, err = conn.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err == nil {
		_, err = conn.ExecContext(ctx, `DROP TABLE pr_development_thread_controllers`)
	}
	if err == nil {
		_, err = conn.ExecContext(ctx,
			schemaV17PRDevelopmentControllersTable+"\n"+
				schemaV10PRDevelopmentControllerWorkspaceIndex+"\n"+
				schemaV10PRDevelopmentControllerReservationIndex+"\n"+
				schemaV10PRDevelopmentControllerLeaseIndex+"\n"+
				schemaV17,
		)
	}
	if err != nil {
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		require.NoError(t, err)
	}
	require.NoError(t, commitSchemaTestMigration(ctx, conn))
	require.NoError(t, conn.Close())
	_, err = db.Exec(schemaV18)
	require.NoError(t, err)
	_, err = db.Exec(`PRAGMA user_version = 18`)
	require.NoError(t, err)
}

func TestSchemaV19RejectsVersionsOneThroughSeventeen(t *testing.T) {
	t.Parallel()
	for version := 1; version <= 17; version++ {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "unsupported.db")
			db := openSchemaTestDB(t, path)
			_, err := db.Exec(`PRAGMA user_version = ` + strconv.Itoa(version))
			require.NoError(t, err)
			require.NoError(t, db.Close())
			store, err := Open(context.Background(), path)
			assert.Nil(t, store)
			assert.ErrorIs(t, err, ErrSchemaInvalid)
			db = openSchemaTestDB(t, path)
			defer db.Close()
			var unchanged int
			require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&unchanged))
			assert.Equal(t, version, unchanged)
		})
	}
}

func legacyPRTablesFromDBForTest(store *Store) ([]string, error) {
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return legacyPRTablesV19(context.Background(), conn)
}
