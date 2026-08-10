//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestStoreMigratesV9ToEmptyPRDevelopmentControllerSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v9-controller.db")
	db := openSchemaTestDB(t, path)
	for _, schema := range []string{
		schemaV1,
		schemaV2,
		schemaV3,
		schemaV4,
		schemaV5,
		schemaV6,
		schemaV7,
		schemaV8,
		schemaV9,
	} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
	setSchemaTestVersion(t, db, 9)
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
		{"table", "pr_development_thread_controllers"},
		{"table", "pr_development_attempt_review_fences"},
		{"index", "pr_development_thread_controllers_workspace"},
		{"index", "pr_development_thread_controllers_reservation"},
		{"index", "pr_development_thread_controllers_lease"},
	} {
		assert.True(t, schemaObjectExists(t, store.db, object.kind, object.name))
	}
	for _, table := range []string{
		"pr_development_thread_controllers",
		"pr_development_attempt_review_fences",
	} {
		var count int
		require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
		assert.Zero(t, count, "v10 migration must not manufacture controller state")
	}
}

func TestStoreMigratesV10ToEmptyPRDevelopmentLedgerSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v10-ledger.db")
	db := openSchemaTestDB(t, path)
	for _, schema := range []string{
		schemaV1,
		schemaV2,
		schemaV3,
		schemaV4,
		schemaV5,
		schemaV6,
		schemaV7,
		schemaV8,
		schemaV9,
		schemaV10,
	} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
	setSchemaTestVersion(t, db, 10)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	defer store.Close()

	var version int
	require.NoError(t, store.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, schemaVersion, version)
	for _, table := range []string{
		"pr_development_ledger_entries",
		"pr_development_ledger_review_findings",
		"pr_development_ledger_checkpoints",
	} {
		assert.True(t, schemaObjectExists(t, store.db, "table", table))

		var count int
		require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
		assert.Zero(t, count, "v11 migration must not manufacture ledger state")
	}
}

func TestStoreMigratesV11ToEmptyPRDevelopmentControllerRecoverySchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v11-controller-recovery.db")
	db := openSchemaTestDB(t, path)
	for _, schema := range []string{
		schemaV1,
		schemaV2,
		schemaV3,
		schemaV4,
		schemaV5,
		schemaV6,
		schemaV7,
		schemaV8,
		schemaV9,
		schemaV10,
		schemaV11,
	} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
	setSchemaTestVersion(t, db, 11)
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
		{"table", "pr_development_controller_recovery_intents"},
		{"index", "pr_development_controller_recovery_active"},
		{"index", "pr_development_controller_recovery_claim"},
		{"index", "pr_development_controller_recovery_previous_key"},
		{"index", "pr_development_controller_recovery_replacement_key"},
		{"index", "pr_development_controller_recovery_claimable"},
	} {
		assert.True(t, schemaObjectExists(t, store.db, object.kind, object.name))
	}
	var count int
	require.NoError(t, store.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_controller_recovery_intents`,
	).Scan(&count))
	assert.Zero(t, count, "v12 migration must not manufacture recovery authority")
}

func TestStoreMigratesV12ToEmptyPRDevelopmentControllerOperationSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v12-controller-operation.db")
	db := openSchemaTestDB(t, path)
	for _, schema := range []string{
		schemaV1,
		schemaV2,
		schemaV3,
		schemaV4,
		schemaV5,
		schemaV6,
		schemaV7,
		schemaV8,
		schemaV9,
		schemaV10,
		schemaV11,
		schemaV12,
	} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
	setSchemaTestVersion(t, db, 12)
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
		{"table", "pr_development_controller_operation_intents"},
		{"index", "pr_development_controller_operation_active"},
		{"index", "pr_development_controller_operation_effect"},
		{"index", "pr_development_controller_operation_recovery"},
		{"index", "pr_development_controller_operation_replacement_key"},
		{"index", "pr_development_controller_operation_replacement_digest"},
		{"index", "pr_development_controller_operation_claim"},
		{"index", "pr_development_controller_operation_claimable"},
	} {
		assert.True(t, schemaObjectExists(t, store.db, object.kind, object.name))
	}
	var count int
	require.NoError(t, store.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_controller_operation_intents`,
	).Scan(&count))
	assert.Zero(t, count, "v13 migration must not manufacture operation authority")
}

func TestStoreMigratesV16ControllersByteExactlyWithPopulatedForeignKeys(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration-v16-suspension-populated.db")
	fixture := newPRDevelopmentOperationFixture(t, path)
	adopt, adopted := adoptOperationForTest(t, fixture, fixture.Mutation)
	_, _, parked := parkOperationForTest(
		t,
		fixture,
		operationLeaseFromTransition(adopted),
		[]PRDevelopmentControllerOperation{adopt},
		operationTestSummary,
		operationTestIterations,
		1,
	)
	controllerID := parked.Controller.ID
	require.NoError(t, fixture.Store.Close())

	db := openSchemaTestDB(t, path)
	setSchemaTestVersion(t, db, 16)
	before := readControllerRowsForSchemaTest(t, db)
	require.NotEmpty(t, before)
	var fences, operations int
	require.NoError(t, db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_attempt_review_fences
		WHERE controller_id = ?`, controllerID).Scan(&fences))
	require.Positive(t, fences)
	require.NoError(t, db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_controller_operation_intents
		WHERE controller_id = ?`, controllerID).Scan(&operations))
	require.Positive(t, operations)
	assert.False(t, schemaObjectExists(
		t, db, "table", "pr_development_controller_suspensions",
	))
	require.NoError(t, db.Close())

	migrated, err := Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, migrated.Close()) })
	after := readControllerRowsForSchemaTest(t, migrated.db)
	assert.Equal(t, before, after, "v17 must preserve every controller SQLite value")

	var version, suspensions, migratedFences, migratedOperations int
	require.NoError(t, migrated.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, schemaVersion, version)
	require.NoError(t, migrated.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_controller_suspensions`,
	).Scan(&suspensions))
	assert.Zero(t, suspensions, "migration must not infer suspension work")
	require.NoError(t, migrated.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_attempt_review_fences
		WHERE controller_id = ?`, controllerID).Scan(&migratedFences))
	assert.Equal(t, fences, migratedFences)
	require.NoError(t, migrated.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_controller_operation_intents
		WHERE controller_id = ?`, controllerID).Scan(&migratedOperations))
	assert.Equal(t, operations, migratedOperations)
	assertNoForeignKeyViolationsForSchemaTest(t, migrated.db)
}

func TestStoreMigratesEmptyV16WithoutInferringSuspensions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v16-suspension-empty.db")
	db := openSchemaTestDB(t, path)
	for _, schema := range []string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7,
		schemaV8, schemaV9, schemaV10, schemaV11, schemaV12, schemaV13,
		schemaV14, schemaV15, schemaV16,
	} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
	setSchemaTestVersion(t, db, 16)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	for _, table := range []string{
		"pr_development_thread_controllers",
		"pr_development_controller_suspensions",
	} {
		var count int
		require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
		assert.Zero(t, count, "v17 migration must not manufacture "+table)
	}
	assertNoForeignKeyViolationsForSchemaTest(t, store.db)
}

func TestStoreMigratesEmptyV17ToEmptyPublicationJournal(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v17-publication-empty.db")
	db := openSchemaTestDB(t, path)
	installSchemaThroughV17ForTest(t, db)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	var version, publications int
	require.NoError(t, store.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, schemaVersion, version)
	require.NoError(t, store.db.QueryRow(
		`SELECT COUNT(*) FROM pr_development_publications`,
	).Scan(&publications))
	assert.Zero(t, publications, "v18 must not invent publication work")
	for _, object := range []struct {
		kind string
		name string
	}{
		{"table", "pr_development_publications"},
		{"index", "pr_development_attempt_review_fences_publication"},
		{"index", "pr_development_repair_orchestration_publication"},
		{"index", "pr_development_publications_occurrence"},
		{"index", "pr_development_publications_decision_run"},
		{"index", "pr_development_publications_push_started"},
		{"index", "pr_development_publications_claimable"},
	} {
		assert.True(t, schemaObjectExists(t, store.db, object.kind, object.name), object.name)
	}
	assertNoForeignKeyViolationsForSchemaTest(t, store.db)
}

func TestStoreMigratesPopulatedV17WithoutBackfillingPublicationJournal(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration-v17-publication-populated.db")
	store, publication, replayInput := newPassedPRDevelopmentPublicationForSchemaTest(t, path)

	parentTables := []string{
		"pr_development_thread_controllers",
		"pr_development_attempt_review_fences",
		"pr_development_repair_orchestrations",
		"pr_development_ledger_entries",
	}
	controllersBefore := readControllerRowsForSchemaTest(t, store.db)
	require.NotEmpty(t, controllersBefore)
	before := make(map[string]int, len(parentTables))
	for _, table := range parentTables {
		var count int
		require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
		require.Positive(t, count, table)
		before[table] = count
	}
	var admittedPublications int
	require.NoError(t, store.db.QueryRow(
		`SELECT COUNT(*) FROM pr_development_publications WHERE id = ?`, publication.ID,
	).Scan(&admittedPublications))
	require.Equal(t, 1, admittedPublications, "the historical evidence must be publication-eligible")
	require.NoError(t, store.Close())

	db := openSchemaTestDB(t, path)
	setSchemaTestVersion(t, db, 17)
	assert.False(t, schemaObjectExists(t, db, "table", "pr_development_publications"))
	require.NoError(t, db.Close())

	migrated, err := Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, migrated.Close()) })
	assert.Equal(
		t,
		controllersBefore,
		readControllerRowsForSchemaTest(t, migrated.db),
		"v18 must preserve every controller SQLite value",
	)
	for _, table := range parentTables {
		var after int
		require.NoError(t, migrated.db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&after))
		assert.Equal(t, before[table], after, table)
	}
	for _, proof := range []struct {
		query string
		args  []any
	}{
		{
			query: `SELECT 1 FROM pr_development_thread_controllers
				WHERE id = ? AND thread_id = ? AND line_id = ?`,
			args: []any{publication.ControllerID, publication.ThreadID, publication.LineID},
		},
		{
			query: `SELECT 1 FROM pr_development_attempt_review_fences
				WHERE attempt_id = ? AND controller_id = ? AND ordinal = ? AND fence_hash = ?`,
			args: []any{
				publication.AttemptID, publication.ControllerID,
				publication.FenceOrdinal, publication.FenceHash,
			},
		},
		{
			query: `SELECT 1 FROM pr_development_repair_orchestrations
				WHERE attempt_id = ? AND receipt_hash = ?`,
			args: []any{publication.AttemptID, publication.OrchestrationReceiptHash},
		},
		{
			query: `SELECT 1 FROM pr_development_ledger_entries
				WHERE id = ? AND kind = ? AND entry_hash = ?`,
			args: []any{
				publication.AttemptLedgerEntryID, publication.AttemptLedgerEntryKind,
				publication.AttemptLedgerEntryHash,
			},
		},
		{
			query: `SELECT 1 FROM pr_development_ledger_entries
				WHERE id = ? AND kind = ? AND entry_hash = ?`,
			args: []any{
				publication.ReviewLedgerEntryID, publication.ReviewLedgerEntryKind,
				publication.ReviewLedgerEntryHash,
			},
		},
	} {
		var one int
		require.NoError(t, migrated.db.QueryRow(proof.query, proof.args...).Scan(&one))
		assert.Equal(t, 1, one)
	}
	legacyReplay, changed, err := migrated.AppendPRDevelopmentLedgerReview(
		ctx,
		replayInput,
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, publication.ReviewLedgerEntryID, legacyReplay.ID)
	assert.Equal(t, publication.ReviewLedgerEntryHash, legacyReplay.EntryHash)
	replayed, changed, err := migrated.CompletePRDevelopmentReview(ctx, replayInput)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Nil(t, replayed.Publication, "historical passed reviews must never be backfilled")
	var publications int
	require.NoError(t, migrated.db.QueryRow(
		`SELECT COUNT(*) FROM pr_development_publications`,
	).Scan(&publications))
	assert.Zero(t, publications, "migration cannot infer publication eligibility")
	assertNoForeignKeyViolationsForSchemaTest(t, migrated.db)
}

func TestStoreV18RejectsFutureVersionWithoutTouchingV17Layout(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "future-v19-v17-layout.db")
	db := openSchemaTestDB(t, path)
	installSchemaThroughV17ForTest(t, db)
	_, err := db.Exec(`PRAGMA user_version = 19`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaTooNew)

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 19, version)
	assert.True(t, schemaObjectExists(
		t, db, "table", "pr_development_controller_suspensions",
	), "the untouched fixture must remain a true v17 layout")
	assert.False(t, schemaObjectExists(
		t, db, "table", "pr_development_publications",
	), "future-version fencing must occur before v18 DDL")
	assert.False(t, schemaObjectExists(
		t, db, "index", "pr_development_attempt_review_fences_publication",
	))
	assert.False(t, schemaObjectExists(
		t, db, "index", "pr_development_repair_orchestration_publication",
	))
	require.NoError(t, validateControllerTableForSchemaTest(
		context.Background(), db, schemaV17PRDevelopmentControllersTable,
	))
}

func TestStoreV17MigrationFailureRollsBackControllerRebuild(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v17-controller-rollback.db")
	db := openSchemaTestDB(t, path)
	installSchemaV1ForTest(t, db)
	setSchemaTestVersion(t, db, 16)
	malformed := strings.Replace(
		schemaV17PRDevelopmentControllerSuspensionsTable,
		"changed_file_count >= 0 AND changed_file_count <= 1000",
		"changed_file_count >= 0 AND changed_file_count <= 1001",
		1,
	)
	require.NotEqual(t, schemaV17PRDevelopmentControllerSuspensionsTable, malformed)
	_, err := db.Exec(malformed)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "schema v17")

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 16, version)
	require.NoError(t, validateControllerTableForSchemaTest(
		context.Background(), db, schemaV10PRDevelopmentControllersTable,
	))
	assert.True(t, schemaObjectExists(
		t, db, "table", "pr_development_controller_suspensions",
	), "the preexisting malformed table must survive rollback")
	assert.False(t, schemaObjectExists(
		t, db, "index", "pr_development_controller_suspensions_active",
	), "v17 indexes created after the controller rebuild must roll back")
}

func TestStoreV18MigrationValidationFailureRollsBackPublicationIndexes(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v18-publication-rollback.db")
	db := openSchemaTestDB(t, path)
	installSchemaThroughV17ForTest(t, db)
	malformed := strings.Replace(
		schemaV18PRDevelopmentPublicationsTable,
		"attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts IN (0, 1))",
		"attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts > 0)",
		1,
	)
	require.NotEqual(t, schemaV18PRDevelopmentPublicationsTable, malformed)
	_, err := db.Exec(malformed)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "schema v18")

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 17, version)
	assert.True(t, schemaObjectExists(
		t, db, "table", "pr_development_publications",
	), "the preexisting malformed table must survive rollback")
	for _, index := range []string{
		"pr_development_attempt_review_fences_publication",
		"pr_development_repair_orchestration_publication",
		"pr_development_publications_occurrence",
		"pr_development_publications_decision_run",
		"pr_development_publications_push_started",
		"pr_development_publications_claimable",
	} {
		assert.False(t, schemaObjectExists(t, db, "index", index), index)
	}
}

func TestPRDevelopmentControllerSuspensionTypesAreJSONPrivate(t *testing.T) {
	t.Parallel()

	sentinel := "private-suspension-sentinel"
	values := []any{
		PRDevelopmentControllerSuspensionRequest{Repository: sentinel},
		PRDevelopmentControllerSuspensionResult{WorkspaceID: sentinel},
		PRDevelopmentControllerSuspendedResumeRequest{Repository: sentinel},
		PRDevelopmentControllerSuspendedResumeResult{WorkspaceID: sentinel},
		PRDevelopmentControllerSuspension{
			ID: sentinel,
			SuspendRequest: PRDevelopmentControllerSuspensionRequest{
				Repository: sentinel,
			},
		},
		PRDevelopmentControllerSuspendedResumeLease{
			Controller: PRDevelopmentController{ID: sentinel},
		},
		PRDevelopmentControllerSuspendedResumeRenew{ControllerID: sentinel},
		PRDevelopmentControllerSuspendedResumeFinalize{ControllerID: sentinel},
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		assert.Equal(t, `{}`, string(encoded))
		assert.NotContains(t, string(encoded), sentinel)
	}
}

func TestStoreMigratesV13ToEmptyPRDevelopmentRepairOrchestrationSchema(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "migration-v13-orchestration.db")
	db := openSchemaTestDB(t, path)
	for _, schema := range []string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7,
		schemaV8, schemaV9, schemaV10, schemaV11, schemaV12, schemaV13,
	} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
	setSchemaTestVersion(t, db, 13)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	defer store.Close()
	var version int
	require.NoError(t, store.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, schemaVersion, version)
	for _, object := range []struct{ kind, name string }{
		{"table", "pr_development_repair_orchestration_cohorts"},
		{"table", "pr_development_repair_orchestrations"},
		{"index", "pr_development_repair_orchestration_claimable"},
		{"index", "pr_development_repair_orchestration_receipt"},
		{"index", "pr_development_repair_orchestration_park"},
		{"index", "pr_development_repair_orchestration_ledger"},
	} {
		assert.True(t, schemaObjectExists(t, store.db, object.kind, object.name))
	}
	var count int
	require.NoError(t, store.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_repair_orchestrations`).Scan(&count))
	assert.Zero(t, count, "v14 migration must not manufacture orchestration state")
	require.NoError(t, store.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_repair_orchestration_cohorts`).Scan(&count))
	assert.Zero(t, count, "v14 migration must not guess legacy cohort ownership")
}

func TestStoreV14MigrationGrandfathersMultipleProviderThreadRepairSessionOwners(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration-v14-duplicate-thread-owner.db")
	store, clock, capture := newPRDevelopmentStoreFixture(t, path)
	first, created, err := store.CapturePRDevelopmentCase(
		ctx, validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	second := captureAdditionalPRDevelopmentThreadCase(
		t, store, clock, capture, "delivery-v13-duplicate-thread-owner", "814",
	)
	owned, admitted, err := store.AdmitPRDevelopmentRepair(ctx, PRDevelopmentRepairAdmit{
		CaseID: first.ID, ExpectedConversationVersion: 0, ExpectedRepairVersion: 0,
		IdempotencyKey: "existing-v13-owner", AgentID: "main",
		Instruction: "Existing provider-thread repair owner.",
	})
	require.NoError(t, err)
	require.True(t, admitted)
	require.NotNil(t, owned.RepairSession)

	// Simulate a pre-v14 database that admitted a second case in the same
	// provider thread before the one-owner invariant existed.
	err = store.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := store.currentTime()
		if clockErr != nil {
			return clockErr
		}
		_, insertErr := insertPRDevelopmentRepairSession(ctx, conn, PRDevelopmentRepairAdmit{
			CaseID: second.ID, ExpectedConversationVersion: 0, ExpectedRepairVersion: 0,
			IdempotencyKey: "conflicting-v13-owner", AgentID: "main",
			Instruction: "Conflicting provider-thread repair owner.",
		}, now)
		return insertErr
	})
	require.NoError(t, err)
	var sessions int
	require.NoError(t, store.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_repair_sessions`).Scan(&sessions))
	require.Equal(t, 2, sessions)
	_, err = store.db.Exec(`DROP TABLE pr_development_repair_orchestrations`)
	require.NoError(t, err)
	_, err = store.db.Exec(`DROP TABLE pr_development_repair_orchestration_cohorts`)
	require.NoError(t, err)
	setSchemaTestVersion(t, store.db, 13)

	migrated, err := Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, migrated.Close()) })
	var version int
	require.NoError(t, migrated.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, schemaVersion, version)
	var cohorts int
	require.NoError(t, migrated.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_repair_orchestration_cohorts`).Scan(&cohorts))
	assert.Zero(t, cohorts, "migrated sibling owners must remain in the legacy cohort")
	legacy, claimed, err := migrated.ClaimPRDevelopmentRepair(
		ctx, PRDevelopmentRepairClaimRequest{WorkerLabel: "legacy-v13", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotEmpty(t, legacy.Attempts)
	_, claimed, err = migrated.ClaimPRDevelopmentRepairOrchestration(
		ctx, PRDevelopmentRepairOrchestrationClaim{WorkerLabel: "v14", Lease: time.Minute},
	)
	require.NoError(t, err)
	assert.False(t, claimed, "unmarked migrated owners must not enter orchestration")
}

func TestStoreV14MigrationKeepsPinnedProviderSessionAndLaterAttemptLegacyOwned(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration-v14-pinned-legacy-owner.db")
	store, _, capture := newPRDevelopmentStoreFixture(t, path)
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx, validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(
		t, store, developmentCase.ID,
	)
	require.NotEmpty(t, completed.HeadRepository)
	require.NotEmpty(t, completed.WorkspaceID)
	_, err = store.db.Exec(`
		DELETE FROM pr_development_repair_orchestration_cohorts WHERE session_id = ?`,
		completed.ID,
	)
	require.NoError(t, err)
	queued, admitted, err := store.AdmitPRDevelopmentRepair(ctx, PRDevelopmentRepairAdmit{
		CaseID: developmentCase.ID, ExpectedConversationVersion: 0,
		ExpectedRepairVersion: completed.Version,
		IdempotencyKey:        "legacy-pinned-later-attempt", AgentID: "main",
		Instruction: "Continue the legacy-owned retained workspace repair.",
	})
	require.NoError(t, err)
	require.True(t, admitted)
	require.NotNil(t, queued.RepairSession)
	require.Len(t, queued.RepairSession.Attempts, 2)
	queuedAttemptID := queued.RepairSession.Attempts[1].ID
	_, err = store.db.Exec(`DROP TABLE pr_development_repair_orchestrations`)
	require.NoError(t, err)
	_, err = store.db.Exec(`DROP TABLE pr_development_repair_orchestration_cohorts`)
	require.NoError(t, err)
	setSchemaTestVersion(t, store.db, 13)

	migrated, err := Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, migrated.Close()) })
	legacy, claimed, err := migrated.ClaimPRDevelopmentRepair(
		ctx, PRDevelopmentRepairClaimRequest{WorkerLabel: "legacy-pinned", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	legacyAttempt := activePRDevelopmentRepairAttempt(&legacy)
	require.NotNil(t, legacyAttempt)
	assert.Equal(t, queuedAttemptID, legacyAttempt.ID)
	_, claimed, err = migrated.ClaimPRDevelopmentRepairOrchestration(
		ctx, PRDevelopmentRepairOrchestrationClaim{WorkerLabel: "v14", Lease: time.Minute},
	)
	require.NoError(t, err)
	assert.False(t, claimed)
}

func TestStoreV14MigrationRoutesExactReadyControllerOwnerToOrchestration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration-v14-ready-controller-owner.db")
	store, _, capture := newPRDevelopmentStoreFixture(t, path)
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx, validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(
		t, store, developmentCase.ID,
	)
	_, ready := finishPRDevelopmentControllerReviewForTest(
		t, store, developmentCase.ID, completed,
	)
	require.Equal(t, PRDevelopmentControllerReady, ready.Phase)
	var legacyLedgerEntries int
	require.NoError(t, store.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_ledger_entries`).Scan(&legacyLedgerEntries))
	require.Zero(t, legacyLedgerEntries)
	_, err = store.db.Exec(`
		DELETE FROM pr_development_repair_orchestration_cohorts WHERE session_id = ?`,
		completed.ID,
	)
	require.NoError(t, err)
	_, err = store.db.Exec(`DROP TABLE pr_development_repair_orchestrations`)
	require.NoError(t, err)
	_, err = store.db.Exec(`DROP TABLE pr_development_repair_orchestration_cohorts`)
	require.NoError(t, err)
	setSchemaTestVersion(t, store.db, 13)

	migrated, err := Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, migrated.Close()) })
	workbench, admitted, err := migrated.AdmitPRDevelopmentRepair(ctx, PRDevelopmentRepairAdmit{
		CaseID: developmentCase.ID, ExpectedConversationVersion: 0,
		ExpectedRepairVersion: completed.Version,
		IdempotencyKey:        "ready-controller-later-attempt", AgentID: "main",
		Instruction: "Continue from the exact retained controller line.",
	})
	require.NoError(t, err)
	require.True(t, admitted)
	require.NotNil(t, workbench.RepairSession)
	require.Len(t, workbench.RepairSession.Attempts, 2)
	queuedAttemptID := workbench.RepairSession.Attempts[1].ID
	var cohorts int
	require.NoError(t, migrated.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_repair_orchestration_cohorts`).Scan(&cohorts))
	assert.Zero(t, cohorts, "retained controller evidence is eligibility, not guessed cohort data")
	_, claimed, err := migrated.ClaimPRDevelopmentRepair(
		ctx, PRDevelopmentRepairClaimRequest{WorkerLabel: "legacy", Lease: time.Minute},
	)
	require.NoError(t, err)
	assert.False(t, claimed)
	run, claimed, err := migrated.ClaimPRDevelopmentRepairOrchestration(
		ctx, PRDevelopmentRepairOrchestrationClaim{WorkerLabel: "v14", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, queuedAttemptID, run.AttemptID)
	run, pinned, err := migrated.PinPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationPin{
			AttemptID: run.AttemptID, ClaimToken: run.ClaimToken,
			HeadRepository: completed.HeadRepository, HeadRef: completed.HeadRef,
			HeadSHA: completed.HeadSHA, CloneURL: completed.CloneURL,
			ReviewDigest: completed.ReviewDigest, WorkspaceID: completed.WorkspaceID,
			SourceTree: ready.SourceTree,
		},
	)
	require.NoError(t, err)
	require.True(t, pinned)
	lease, acquired, err := migrated.AcquirePRDevelopmentRepairOrchestrationController(
		ctx,
		PRDevelopmentRepairOrchestrationControllerAcquire{
			CaseID: developmentCase.ID, AttemptID: run.AttemptID,
			ClaimToken: run.ClaimToken, ExpectedRevision: ready.Revision,
			WorkerLabel: "v14-ready-owner", Lease: time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	loaded, err := migrated.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded.RepairSession)
	operationFixture := &prDevelopmentOperationFixture{
		Store: migrated, Case: developmentCase, Session: *loaded.RepairSession,
		Attempt:  loaded.RepairSession.Attempts[len(loaded.RepairSession.Attempts)-1],
		Mutation: lease, SourceTree: ready.SourceTree, NextID: 814,
	}
	request := operationBaseRequest(operationFixture, lease.Controller)
	request.ExpectedVersion = lease.Controller.LineVersion
	request.ExpectedEpoch = lease.Controller.MutationEpoch
	request.ExpectedTip = lease.Controller.TipCommit
	request.ExpectedTree = lease.Controller.Tree
	resume := prepareOperationForTest(
		t, operationFixture, lease, PRDevelopmentControllerOperationResume,
		operationFixture.operationID(), request,
	)
	resumed := finalizeOperationForTest(
		t,
		operationFixture,
		lease,
		resume,
		PRDevelopmentControllerOperationResult{
			WorkspaceID: completed.WorkspaceID, Version: lease.Controller.LineVersion,
			MutationEpoch: lease.Controller.MutationEpoch + 1,
			Tip:           lease.Controller.TipCommit, Tree: lease.Controller.Tree,
		},
	)
	assert.Equal(t, lease.Controller.MutationEpoch+1, resumed.Controller.MutationEpoch)
	orchestrationFixture := &prDevelopmentOrchestrationFixture{
		Operation: operationFixture, Run: run, Lease: operationLeaseFromTransition(resumed),
	}
	run = startAndCompletePRDevelopmentOrchestrationForTest(t, orchestrationFixture)
	validated, changed, err := migrated.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx, validPRDevelopmentOrchestrationValidationForTest(orchestrationFixture),
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, validated.Validation)
	_, _, parked := parkOperationForTest(
		t,
		operationFixture,
		orchestrationFixture.Lease,
		nil,
		run.Summary,
		run.Iterations,
		815,
	)
	require.NotNil(t, parked.Fence)
	assert.Equal(t, 1, parked.Fence.Ordinal)
	completedRun, err := migrated.GetPRDevelopmentRepairOrchestration(ctx, run.AttemptID)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentRepairOrchestrationCompleted, completedRun.Phase)
	ledger, err := migrated.GetPRDevelopmentLedgerForCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.Len(t, ledger.Entries, 1)
	assert.Equal(t, 2, ledger.Entries[0].Ordinal)
	assert.Equal(t, emptyPRDevelopmentLedgerEntriesDigest(), ledger.Entries[0].PreviousHash)
	assert.Equal(t, completedRun.LedgerEntryID, ledger.Entries[0].ID)
	dummyEarlier := ledger.Entries[0]
	dummyEarlier.ID = prDevelopmentLedgerEntryIDPrefix + strings.Repeat("f", 32)
	dummyEarlier.Ordinal = 0
	dummyEarlier.AttemptID = completed.Attempts[0].ID
	dummyEarlier.FenceOrdinal = 0
	dummyEarlier.FenceHash = strings.Repeat("a", 64)
	dummyEarlier.PreviousHash = emptyPRDevelopmentLedgerEntriesDigest()
	dummyEarlier.EntryHash = strings.Repeat("b", 64)
	require.NoError(t, migrated.withImmediate(ctx, func(conn *sql.Conn) error {
		return insertPRDevelopmentLedgerEntry(ctx, conn, dummyEarlier)
	}))
	_, err = migrated.GetPRDevelopmentRepairOrchestration(ctx, run.AttemptID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ledger predecessor is missing")
}

func TestStorePRDevelopmentRepairOrchestrationMigrationValidationRollsBack(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "migration-v14-orchestration-rollback.db")
	db := openSchemaTestDB(t, path)
	for _, schema := range []string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7,
		schemaV8, schemaV9, schemaV10, schemaV11, schemaV12, schemaV13,
	} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
	malformed := strings.Replace(
		schemaV14PRDevelopmentRepairOrchestrationsTable,
		"changed_files >= 0 AND changed_files <= 10000",
		"changed_files >= 0 AND changed_files <= 10001",
		1,
	)
	require.NotEqual(t, schemaV14PRDevelopmentRepairOrchestrationsTable, malformed)
	_, err := db.Exec(malformed)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 13)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v14")
	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 13, version)
	assert.True(t, schemaObjectExists(
		t, db, "table", "pr_development_repair_orchestrations",
	))
	assert.False(t, schemaObjectExists(
		t, db, "index", "pr_development_repair_orchestration_claimable",
	), "v14 indexes created during failed validation must roll back")
}

func TestStorePRDevelopmentControllerOperationMigrationValidationRollsBack(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v13-operation-rollback.db")
	db := openSchemaTestDB(t, path)
	for _, schema := range []string{
		schemaV1,
		schemaV2,
		schemaV3,
		schemaV4,
		schemaV5,
		schemaV6,
		schemaV7,
		schemaV8,
		schemaV9,
		schemaV10,
		schemaV11,
		schemaV12,
	} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
	malformed := strings.Replace(
		schemaV13PRDevelopmentControllerOperationIntentsTable,
		"ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 24576),",
		"ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal <= 24576),",
		1,
	)
	require.NotEqual(t, schemaV13PRDevelopmentControllerOperationIntentsTable, malformed)
	_, err := db.Exec(malformed)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 12)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v13")

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 12, version)
	assert.True(t, schemaObjectExists(
		t,
		db,
		"table",
		"pr_development_controller_operation_intents",
	))
	assert.False(t, schemaObjectExists(
		t,
		db,
		"index",
		"pr_development_controller_operation_active",
	), "indexes created in the failed v13 migration must roll back")
}

func TestStorePRDevelopmentControllerRecoveryMigrationValidationRollsBack(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v12-recovery-rollback.db")
	db := openSchemaTestDB(t, path)
	for _, schema := range []string{
		schemaV1,
		schemaV2,
		schemaV3,
		schemaV4,
		schemaV5,
		schemaV6,
		schemaV7,
		schemaV8,
		schemaV9,
		schemaV10,
		schemaV11,
	} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
	malformed := strings.Replace(
		schemaV12PRDevelopmentControllerRecoveryIntentsTable,
		"ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 8192),",
		"ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal <= 8192),",
		1,
	)
	require.NotEqual(t, schemaV12PRDevelopmentControllerRecoveryIntentsTable, malformed)
	_, err := db.Exec(malformed)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 11)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v12")

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 11, version)
	assert.True(t, schemaObjectExists(
		t,
		db,
		"table",
		"pr_development_controller_recovery_intents",
	))
	assert.False(t, schemaObjectExists(
		t,
		db,
		"index",
		"pr_development_controller_recovery_active",
	), "indexes created in the failed v12 migration must roll back")
}

func TestStorePRDevelopmentLedgerMigrationValidationRollsBack(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v11-ledger-rollback.db")
	db := openSchemaTestDB(t, path)
	for _, schema := range []string{
		schemaV1,
		schemaV2,
		schemaV3,
		schemaV4,
		schemaV5,
		schemaV6,
		schemaV7,
		schemaV8,
		schemaV9,
		schemaV10,
	} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
	malformedLedgerEntries := strings.Replace(
		schemaV11PRDevelopmentLedgerEntriesTable,
		"ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 16384),",
		"ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal <= 16384),",
		1,
	)
	require.NotEqual(t, schemaV11PRDevelopmentLedgerEntriesTable, malformedLedgerEntries)
	_, err := db.Exec(malformedLedgerEntries)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 10)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v11")

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 10, version)
	assert.True(t, schemaObjectExists(
		t,
		db,
		"table",
		"pr_development_ledger_entries",
	), "the preexisting malformed table must survive migration rollback")
	for _, table := range []string{
		"pr_development_ledger_review_findings",
		"pr_development_ledger_checkpoints",
	} {
		assert.False(
			t,
			schemaObjectExists(t, db, "table", table),
			"objects created inside the failed v11 migration must roll back",
		)
	}
}

func TestStoreRejectsMalformedCurrentPRDevelopmentLedgerSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "malformed-current-ledger.db")
	db := openSchemaTestDB(t, path)
	installSchemaV1ForTest(t, db)
	_, err := db.Exec(`DROP TABLE pr_development_ledger_checkpoints`)
	require.NoError(t, err)
	malformedCheckpoints := strings.Replace(
		schemaV11PRDevelopmentLedgerCheckpointsTable,
		"generation INTEGER NOT NULL CHECK (generation >= 0 AND generation < 8192),",
		"generation INTEGER NOT NULL CHECK (generation >= 0 AND generation <= 8192),",
		1,
	)
	require.NotEqual(t, schemaV11PRDevelopmentLedgerCheckpointsTable, malformedCheckpoints)
	_, err = db.Exec(malformedCheckpoints)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v11")
	var validationErr *schemaValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Equal(t, "pr_development_ledger_checkpoints", validationErr.object)

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, schemaVersion, version)
}

func TestStoreRejectsMalformedCurrentPRDevelopmentPublicationSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "malformed-current-publication.db")
	db := openSchemaTestDB(t, path)
	installSchemaV1ForTest(t, db)
	_, err := db.Exec(`DROP TABLE pr_development_publications`)
	require.NoError(t, err)
	malformed := strings.Replace(
		schemaV18PRDevelopmentPublicationsTable,
		"provider_observation_json BLOB NOT NULL DEFAULT X'' CHECK (",
		"provider_observation_json TEXT NOT NULL DEFAULT '' CHECK (",
		1,
	)
	require.NotEqual(t, schemaV18PRDevelopmentPublicationsTable, malformed)
	_, err = db.Exec(malformed)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v18")
	var validationErr *schemaValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Equal(t, "pr_development_publications", validationErr.object)
}

func TestStoreRejectsCurrentPRDevelopmentPublicationMissingExactIndex(t *testing.T) {
	t.Parallel()

	indexes := []string{
		"pr_development_attempt_review_fences_publication",
		"pr_development_repair_orchestration_publication",
		"pr_development_publications_occurrence",
		"pr_development_publications_decision_run",
		"pr_development_publications_push_started",
		"pr_development_publications_claimable",
	}
	for _, index := range indexes {
		t.Run(index, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "missing-publication-index.db")
			db := openSchemaTestDB(t, path)
			installSchemaV1ForTest(t, db)
			_, err := db.Exec(`DROP INDEX "` + index + `"`)
			require.NoError(t, err)
			require.NoError(t, db.Close())

			store, err := Open(context.Background(), path)
			require.Error(t, err)
			assert.Nil(t, store)
			assert.ErrorIs(t, err, ErrSchemaInvalid)
			var validationErr *schemaValidationError
			require.ErrorAs(t, err, &validationErr)
			assert.Equal(t, index, validationErr.object)
		})
	}
}

func TestStoreRejectsCurrentPRDevelopmentPublicationWrongPartialFence(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "wrong-publication-push-fence.db")
	db := openSchemaTestDB(t, path)
	installSchemaV1ForTest(t, db)
	_, err := db.Exec(`DROP INDEX pr_development_publications_push_started`)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE UNIQUE INDEX pr_development_publications_push_started
		ON pr_development_publications(controller_id) WHERE status = 'push_ready'`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	var validationErr *schemaValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Equal(t, "pr_development_publications_push_started", validationErr.object)
	assert.Contains(t, validationErr.problem, "definition differs")
}

func TestPRDevelopmentPublicationSchemaUsesExactCompositeForeignKeys(t *testing.T) {
	t.Parallel()

	db := openSchemaTestDB(t, filepath.Join(t.TempDir(), "publication-foreign-keys.db"))
	defer db.Close()
	installSchemaV1ForTest(t, db)

	type foreignKeyColumn struct {
		sequence int
		from     string
		to       string
	}
	type foreignKeyGroup struct {
		table   string
		columns []foreignKeyColumn
	}
	rows, err := db.Query(`PRAGMA foreign_key_list('pr_development_publications')`)
	require.NoError(t, err)
	defer rows.Close()
	groups := make(map[int]foreignKeyGroup)
	for rows.Next() {
		var (
			id, sequence                  int
			table, from, to               string
			onUpdate, onDelete, matchMode string
		)
		require.NoError(t, rows.Scan(
			&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &matchMode,
		))
		assert.Equal(t, "NO ACTION", onUpdate)
		assert.Equal(t, "RESTRICT", onDelete)
		assert.Equal(t, "NONE", matchMode)
		group := groups[id]
		if group.table == "" {
			group.table = table
		}
		require.Equal(t, group.table, table)
		group.columns = append(group.columns, foreignKeyColumn{
			sequence: sequence,
			from:     from,
			to:       to,
		})
		groups[id] = group
	}
	require.NoError(t, rows.Err())

	actual := make([]string, 0, len(groups))
	for _, group := range groups {
		sort.Slice(group.columns, func(i, j int) bool {
			return group.columns[i].sequence < group.columns[j].sequence
		})
		value := group.table + ":"
		for index, column := range group.columns {
			if index != 0 {
				value += ","
			}
			value += column.from + "->" + column.to
		}
		actual = append(actual, value)
	}
	sort.Strings(actual)
	expected := []string{
		"pr_development_attempt_review_fences:attempt_id->attempt_id,controller_id->controller_id,fence_ordinal->ordinal,fence_hash->fence_hash",
		"pr_development_cases:case_id->id",
		"pr_development_ledger_entries:attempt_ledger_entry_id->id,attempt_ledger_entry_kind->kind,attempt_ledger_entry_hash->entry_hash",
		"pr_development_ledger_entries:review_ledger_entry_id->id,review_ledger_entry_kind->kind,review_ledger_entry_hash->entry_hash",
		"pr_development_repair_attempts:attempt_id->id",
		"pr_development_repair_orchestrations:attempt_id->attempt_id,orchestration_receipt_hash->receipt_hash",
		"pr_development_repair_sessions:owner_session_id->id",
		"pr_development_thread_controllers:controller_id->id,thread_id->thread_id,line_id->line_id",
		"pr_development_threads:thread_id->id",
	}
	sort.Strings(expected)
	assert.Equal(t, expected, actual)
}

func TestPRDevelopmentPublicationCompositeForeignKeysRejectCrossEvidence(t *testing.T) {
	t.Parallel()

	wrongHash := func(current string) string {
		candidate := strings.Repeat("f", 64)
		if candidate == current {
			candidate = strings.Repeat("e", 64)
		}
		return candidate
	}
	tests := []struct {
		name   string
		column string
		value  func(PRDevelopmentPublication) any
	}{
		{
			name:   "controller line",
			column: "line_id",
			value:  func(PRDevelopmentPublication) any { return "line_missing" },
		},
		{
			name:   "review fence",
			column: "fence_hash",
			value: func(publication PRDevelopmentPublication) any {
				return wrongHash(publication.FenceHash)
			},
		},
		{
			name:   "orchestration receipt",
			column: "orchestration_receipt_hash",
			value: func(publication PRDevelopmentPublication) any {
				return wrongHash(publication.OrchestrationReceiptHash)
			},
		},
		{
			name:   "attempt ledger proof",
			column: "attempt_ledger_entry_hash",
			value: func(publication PRDevelopmentPublication) any {
				return wrongHash(publication.AttemptLedgerEntryHash)
			},
		},
		{
			name:   "review ledger proof",
			column: "review_ledger_entry_hash",
			value: func(publication PRDevelopmentPublication) any {
				return wrongHash(publication.ReviewLedgerEntryHash)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store, publication, _ := newPassedPRDevelopmentPublicationForSchemaTest(
				t,
				":memory:",
			)
			_, err := store.db.Exec(
				`UPDATE pr_development_publications SET `+test.column+` = ? WHERE id = ?`,
				test.value(publication),
				publication.ID,
			)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "FOREIGN KEY constraint failed")
		})
	}
}

func TestPRDevelopmentPublicationOccurrenceIsUniqueByGlobalReviewEntry(t *testing.T) {
	t.Parallel()

	store, publication, _ := newPassedPRDevelopmentPublicationForSchemaTest(t, ":memory:")
	identity := addPRDevelopmentDispatch(
		t,
		store,
		"delivery-publication-duplicate-review",
		"workflows/own-pr-feedback.yml",
		"revision-publication-duplicate-review",
	)
	input := validPRDevelopmentInputForTest()
	input.PRDevelopmentCaptureIdentity = identity
	secondCase, created, err := store.CapturePRDevelopmentCase(
		context.Background(),
		validPRDevelopmentRequestForTest(input),
	)
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, publication.CaseID, secondCase.ID)

	rows, err := store.db.Query(`PRAGMA table_info(pr_development_publications)`)
	require.NoError(t, err)
	defer rows.Close()
	selectExpressions := make([]string, 0, 80)
	arguments := make([]any, 0, 3)
	for rows.Next() {
		var (
			columnID, notNull, primaryKey int
			name, columnType              string
			defaultValue                  sql.NullString
		)
		require.NoError(t, rows.Scan(
			&columnID,
			&name,
			&columnType,
			&notNull,
			&defaultValue,
			&primaryKey,
		))
		switch name {
		case "id":
			newID, idErr := newPrefixedID(prDevelopmentPublicationIDPrefix)
			require.NoError(t, idErr)
			selectExpressions = append(selectExpressions, "?")
			arguments = append(arguments, newID)
		case "case_id":
			selectExpressions = append(selectExpressions, "?")
			arguments = append(arguments, secondCase.ID)
		default:
			selectExpressions = append(
				selectExpressions,
				`"`+strings.ReplaceAll(name, `"`, `""`)+`"`,
			)
		}
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, selectExpressions)
	arguments = append(arguments, publication.ID)
	_, err = store.db.Exec(
		`INSERT INTO pr_development_publications SELECT `+
			strings.Join(selectExpressions, ", ")+
			` FROM pr_development_publications WHERE id = ?`,
		arguments...,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNIQUE constraint failed")
	assert.Contains(t, err.Error(), "review_ledger_entry_id")

	var count int
	require.NoError(t, store.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_publications
		WHERE review_ledger_entry_id = ?`,
		publication.ReviewLedgerEntryID,
	).Scan(&count))
	assert.Equal(t, 1, count)
}

func TestPRDevelopmentPublicationSchemaEnforcesProgressiveGatePins(t *testing.T) {
	t.Parallel()

	store, publication, _ := newPassedPRDevelopmentPublicationForSchemaTest(t, ":memory:")
	policyRevision := "sha256:" + strings.Repeat("a", 64)
	subjectRevision := "sha256:" + strings.Repeat("b", 64)
	policyHash := strings.Repeat("c", 64)
	subjectHash := strings.Repeat("d", 64)
	providerHash := strings.Repeat("e", 64)
	createdAt := toDBTime(publication.CreatedAt)

	_, err := store.db.Exec(`UPDATE pr_development_publications
		SET subject_revision = ?, pinned_subject_json = ?, pinned_subject_hash = ?
		WHERE id = ?`, subjectRevision, []byte(`{}`), subjectHash, publication.ID)
	require.Error(t, err, "a subject cannot exist before its policy")

	_, err = store.db.Exec(`UPDATE pr_development_publications
		SET policy_revision = ?, pinned_policy_json = ?, pinned_policy_hash = ?
		WHERE id = ?`, policyRevision, []byte(`{}`), policyHash, publication.ID)
	require.NoError(t, err, "the policy-only progressive state must be durable")

	_, err = store.db.Exec(`UPDATE pr_development_publications
		SET status = 'gate_waiting', decision_run_id = ? WHERE id = ?`,
		"wr_"+strings.Repeat("1", 32), publication.ID)
	require.Error(t, err, "a gate cannot wait before subject/provider evidence exists")

	_, err = store.db.Exec(`UPDATE pr_development_publications
		SET subject_revision = ?, pinned_subject_json = ?, pinned_subject_hash = ?
		WHERE id = ?`, subjectRevision, []byte(`{}`), subjectHash, publication.ID)
	require.NoError(t, err)
	_, err = store.db.Exec(`UPDATE pr_development_publications
		SET status = 'gate_waiting', decision_run_id = ? WHERE id = ?`,
		"wr_"+strings.Repeat("1", 32), publication.ID)
	require.Error(t, err, "the decision identity must include provider evidence")

	_, err = store.db.Exec(`UPDATE pr_development_publications SET
		provider_observation_json = ?, provider_observation_hash = ?,
		provider_pinned_at = ?, provider_observed_at = ? WHERE id = ?`,
		[]byte(`{}`), providerHash, createdAt+1, createdAt+1, publication.ID)
	require.Error(t, err, "provider evidence cannot postdate the row high-water")

	_, err = store.db.Exec(`UPDATE pr_development_publications SET
		provider_observation_json = ?, provider_observation_hash = ?,
		provider_pinned_at = ?, provider_observed_at = ? WHERE id = ?`,
		[]byte(`{}`), providerHash, createdAt, createdAt, publication.ID)
	require.NoError(t, err)
	_, err = store.db.Exec(`UPDATE pr_development_publications
		SET provider_observed_at = ? WHERE id = ?`,
		createdAt+1, publication.ID)
	require.Error(t, err, "the latest provider time cannot advance before push start")
	_, err = store.db.Exec(`UPDATE pr_development_publications
		SET provider_pinned_at = ?, provider_observed_at = ? WHERE id = ?`,
		createdAt+1, createdAt, publication.ID)
	require.Error(t, err, "the latest provider time cannot predate its immutable pin")
	_, err = store.db.Exec(`UPDATE pr_development_publications
		SET status = 'gate_waiting' WHERE id = ?`, publication.ID)
	require.Error(t, err, "gate-waiting must bind one decision run")

	decisionRunID := "wr_" + strings.Repeat("2", 32)
	_, err = store.db.Exec(`UPDATE pr_development_publications SET
		status = 'gate_waiting', decision_run_id = ? WHERE id = ?`,
		decisionRunID, publication.ID)
	require.NoError(t, err)

	_, err = store.db.Exec(`UPDATE pr_development_publications SET
		status = 'claimed', claim_from = 'gate_waiting', claim_owner = 'schema-worker',
		claim_token = 'schema-claim', claim_until = ?, claim_epoch = 1, claims = 1,
		claimed_at = ? WHERE id = ?`,
		createdAt+int64(time.Hour), createdAt+1, publication.ID)
	require.Error(t, err, "claim evidence cannot postdate the row high-water")

	_, err = store.db.Exec(`UPDATE pr_development_publications SET
		status = 'claimed', claim_from = 'gate_waiting', claim_owner = 'schema-worker',
		claim_token = 'schema-claim', claim_until = ?, claim_epoch = 1, claims = 1,
		claimed_at = ? WHERE id = ?`,
		createdAt+int64(time.Hour), createdAt, publication.ID)
	require.NoError(t, err, "ordinary gate claims remain a scheduling state")

	_, err = store.db.Exec(`UPDATE pr_development_publications SET
		status = 'gate_waiting', claim_from = '', claim_owner = '', claim_token = '',
		claim_until = NULL WHERE id = ?`, publication.ID)
	require.NoError(t, err, "gate waiting releases the reservation while retaining history")
}

func TestPRDevelopmentPublicationSchemaAcceptsOnlyCoherentEffectShapes(t *testing.T) {
	t.Parallel()

	type effectShape struct {
		name             string
		status           string
		terminal         bool
		request          bool
		effect           bool
		claim            bool
		result           bool
		directReconciled bool
		reconciled       bool
		reconcileAtEnd   bool
		errorCode        string
		wantViolation    bool
	}
	shapes := []effectShape{
		{
			name: "conflict before effect", status: "conflict", terminal: true,
			errorCode: "provider_changed",
		},
		{
			name: "failed before effect", status: "failed", terminal: true,
			errorCode: "runtime_unavailable",
		},
		{
			name: "recovery before effect", status: "recovery_required", terminal: true,
			errorCode: "recovery_required",
		},
		{
			name: "superseded before effect", status: "superseded", terminal: true,
			errorCode: "superseded",
		},
		{
			name: "conflict after effect", status: "conflict", terminal: true,
			request: true, effect: true, errorCode: "push_conflict",
		},
		{
			name: "failed after effect", status: "failed", terminal: true,
			request: true, effect: true, errorCode: "push_failed",
		},
		{
			name: "recovery after effect", status: "recovery_required", terminal: true,
			request: true, effect: true, errorCode: "recovery_required",
		},
		{name: "push in flight", status: "push_started", request: true, effect: true, claim: true},
		{
			name: "outcome unknown", status: "outcome_unknown", terminal: true,
			request: true, effect: true, errorCode: "outcome_unknown",
		},
		{name: "published", status: "published", terminal: true, request: true, effect: true, result: true},
		{
			name: "published direct reconciled", status: "published", terminal: true,
			request: true, effect: true, result: true, directReconciled: true,
		},
		{
			name: "published after reconciliation", status: "published", terminal: true,
			request: true, effect: true, result: true, reconciled: true,
		},
		{
			name: "reconciliation at unknown completion", status: "published", terminal: true,
			request: true, effect: true, result: true, reconciled: true,
			reconcileAtEnd: true, wantViolation: true,
		},
		{
			name: "superseded after effect", status: "superseded", terminal: true,
			request: true, effect: true, errorCode: "superseded", wantViolation: true,
		},
		{
			name: "superseded with unknown code", status: "superseded", terminal: true,
			errorCode: "outcome_unknown", wantViolation: true,
		},
		{
			name: "unknown with internal code", status: "outcome_unknown", terminal: true,
			request: true, effect: true, errorCode: "internal", wantViolation: true,
		},
		{
			name: "post-effect gate failure", status: "failed", terminal: true,
			request: true, effect: true, errorCode: "gate_failed", wantViolation: true,
		},
		{
			name: "push without its live claim", status: "push_started",
			request: true, effect: true, wantViolation: true,
		},
		{
			name: "published without result", status: "published", terminal: true,
			request: true, effect: true, wantViolation: true,
		},
		{name: "terminal without error code", status: "failed", terminal: true, wantViolation: true},
		{
			name: "request without effect marker", status: "conflict", terminal: true,
			request: true, errorCode: "push_conflict", wantViolation: true,
		},
	}

	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			t.Parallel()

			store, publication, _ := newPassedPRDevelopmentPublicationForSchemaTest(
				t,
				":memory:",
			)
			createdAt := toDBTime(publication.CreatedAt)
			_, err := store.db.Exec(`UPDATE pr_development_publications SET
				policy_revision = ?, pinned_policy_json = ?, pinned_policy_hash = ?,
				subject_revision = ?, pinned_subject_json = ?, pinned_subject_hash = ?,
				provider_observation_json = ?, provider_observation_hash = ?,
				provider_pinned_at = ?, provider_observed_at = ? WHERE id = ?`,
				"sha256:"+strings.Repeat("a", 64), []byte(`{}`), strings.Repeat("b", 64),
				"sha256:"+strings.Repeat("c", 64), []byte(`{}`), strings.Repeat("d", 64),
				[]byte(`{}`), strings.Repeat("e", 64), createdAt, createdAt, publication.ID,
			)
			require.NoError(t, err)

			tx, err := store.db.Begin()
			require.NoError(t, err)
			defer tx.Rollback()

			var (
				claimFrom, claimOwner, claimToken string
				claimUntil, claimedAt             any
				claimEpoch, claims, attempts      int
				expectedRemoteTip                 string
				requestJSON                       []byte
				requestHash                       string
				resultJSON                        []byte
				resultHash, disposition           string
				workspaceClean                    int
				reconciliationJSON                []byte
				reconciliationHash                string
				reconciliationObservedAt          any
				effectStartedAt, completedAt      any
				updatedAt                         = createdAt
				errorDetail                       string
			)
			requestJSON = []byte{}
			resultJSON = []byte{}
			reconciliationJSON = []byte{}
			if shape.claim {
				claimFrom, claimOwner, claimToken = "push_ready", "schema-worker", "schema-push"
				claimUntil, claimedAt = createdAt+int64(time.Hour), createdAt
				claimEpoch, claims = 1, 1
			}
			if shape.request {
				expectedRemoteTip = publication.TipCommit
				requestJSON = []byte(`{}`)
				requestHash = strings.Repeat("1", 64)
			}
			if shape.effect {
				effectStartedAt = createdAt
				attempts = 1
			}
			if shape.result {
				resultJSON = []byte(`{}`)
				resultHash = strings.Repeat("2", 64)
				disposition = "applied"
				workspaceClean = 1
			}
			if shape.directReconciled {
				disposition = "reconciled"
			}
			if shape.reconciled {
				reconciliationJSON = []byte(`{}`)
				reconciliationHash = strings.Repeat("3", 64)
				reconciliationObservedAt = createdAt + 1
				updatedAt = createdAt + 1
				disposition = "reconciled"
			}
			if shape.reconcileAtEnd {
				reconciliationObservedAt = createdAt
			}
			if shape.terminal {
				completedAt = createdAt
			}
			if shape.errorCode != "" {
				errorDetail = "schema lifecycle test"
			}

			_, err = tx.Exec(`UPDATE pr_development_publications SET
				status = ?, claim_from = ?, claim_owner = ?, claim_token = ?,
				claim_until = ?, claim_epoch = ?, claims = ?, claimed_at = ?,
				attempts = ?,
				expected_remote_tip = ?, push_request_json = ?, push_request_hash = ?,
				push_result_json = ?, push_result_hash = ?, push_disposition = ?,
				workspace_clean = ?, local_drift = 0,
				reconciliation_observation_json = ?,
				reconciliation_observation_hash = ?, reconciliation_observed_at = ?,
				last_error_code = ?, last_error_detail = ?, effect_started_at = ?,
				completed_at = ?, updated_at = ? WHERE id = ?`,
				shape.status, claimFrom, claimOwner, claimToken,
				claimUntil, claimEpoch, claims, claimedAt,
				attempts,
				expectedRemoteTip, requestJSON, requestHash,
				resultJSON, resultHash, disposition, workspaceClean,
				reconciliationJSON, reconciliationHash, reconciliationObservedAt,
				shape.errorCode, errorDetail, effectStartedAt, completedAt, updatedAt,
				publication.ID,
			)
			if shape.wantViolation {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestStorePRDevelopmentControllerMigrationValidationRollsBack(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v10-controller-rollback.db")
	db := openSchemaTestDB(t, path)
	for _, schema := range []string{
		schemaV1,
		schemaV2,
		schemaV3,
		schemaV4,
		schemaV5,
		schemaV6,
		schemaV7,
		schemaV8,
		schemaV9,
	} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
	_, err := db.Exec(`
		CREATE TABLE pr_development_thread_controllers (
			id TEXT PRIMARY KEY,
			thread_id TEXT NOT NULL UNIQUE,
			owner_session_id TEXT NOT NULL UNIQUE,
			line_id TEXT NOT NULL UNIQUE,
			workspace_id TEXT NOT NULL DEFAULT '',
			mutation_reservation_key TEXT NOT NULL DEFAULT '',
			phase TEXT NOT NULL,
			lease_until INTEGER,
			updated_at INTEGER NOT NULL,
			UNIQUE(id, thread_id, line_id)
		)`)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 9)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v10")

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 9, version)
	assert.False(t, schemaObjectExists(
		t,
		db,
		"table",
		"pr_development_attempt_review_fences",
	), "objects created inside the failed v10 migration must roll back")
	assert.False(t, schemaObjectExists(
		t,
		db,
		"index",
		"pr_development_thread_controllers_lease",
	))
}

func TestStoreRejectsCurrentControllerSchemaMissingReservationIndex(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing-controller-reservation-index.db")
	db := openSchemaTestDB(t, path)
	installSchemaV1ForTest(t, db)
	_, err := db.Exec(`DROP INDEX pr_development_thread_controllers_reservation`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v10")
	var validationErr *schemaValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Equal(t, "pr_development_thread_controllers_reservation", validationErr.object)
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

func newPassedPRDevelopmentPublicationForSchemaTest(
	t *testing.T,
	databasePath string,
) (*Store, PRDevelopmentPublication, PRDevelopmentLedgerReviewAppend) {
	t.Helper()

	store, clock, capture := newPRDevelopmentStoreFixture(t, databasePath)
	fixture := newPRDevelopmentAIReviewOrchestrationOnStore(
		t,
		store,
		clock,
		capture,
		"schema-v18-passed-attempt",
		"gw-schema-v18-passed-line",
		1800,
	)
	completePRDevelopmentAIReviewFixture(t, fixture, PRDevelopmentCIPassed, 9801)
	lease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
	input := validPRDevelopmentAIReviewCompletionForTest(
		lease,
		PRDevelopmentLedgerReviewPassed,
	)
	completion, changed, err := store.CompletePRDevelopmentReview(
		context.Background(),
		input,
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, completion.Publication)
	return store, *completion.Publication, input
}

func installSchemaV1ForTest(t *testing.T, db *sql.DB) {
	t.Helper()

	installSchemaThroughV17ForTest(t, db)
	_, err := db.Exec(schemaV18)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, schemaVersion)
}

func installSchemaThroughV17ForTest(t *testing.T, db *sql.DB) {
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
	_, err = db.Exec(schemaV8)
	require.NoError(t, err)
	_, err = db.Exec(schemaV9)
	require.NoError(t, err)
	_, err = db.Exec(schemaV10)
	require.NoError(t, err)
	_, err = db.Exec(schemaV11)
	require.NoError(t, err)
	_, err = db.Exec(schemaV12)
	require.NoError(t, err)
	_, err = db.Exec(schemaV13)
	require.NoError(t, err)
	_, err = db.Exec(schemaV14)
	require.NoError(t, err)
	_, err = db.Exec(schemaV15)
	require.NoError(t, err)
	_, err = db.Exec(schemaV16)
	require.NoError(t, err)
	installSchemaV17ForTest(t, db)
	setSchemaTestVersion(t, db, 17)
}

func installSchemaV17ForTest(t *testing.T, db *sql.DB) {
	t.Helper()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, beginSchemaTestMigration(ctx, conn))
	if err = migrateSchemaV17(ctx, conn); err != nil {
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		require.NoError(t, err)
	}
	require.NoError(t, commitSchemaTestMigration(ctx, conn))
}

func installSchemaTextForTest(t *testing.T, db *sql.DB, schema string) {
	t.Helper()

	_, err := db.Exec(schema)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, schemaVersion)
}

func setSchemaTestVersion(t *testing.T, db *sql.DB, version int) {
	t.Helper()

	if version < 18 {
		_, err := db.Exec(`DROP TABLE IF EXISTS pr_development_publications`)
		require.NoError(t, err)
		_, err = db.Exec(`DROP INDEX IF EXISTS pr_development_attempt_review_fences_publication`)
		require.NoError(t, err)
		_, err = db.Exec(`DROP INDEX IF EXISTS pr_development_repair_orchestration_publication`)
		require.NoError(t, err)
	}
	if version < 17 && schemaObjectExists(
		t,
		db,
		"table",
		"pr_development_controller_suspensions",
	) {
		downgradeSchemaV17ForTest(t, db)
	}
	if version < 16 {
		_, err := db.Exec(`DROP TABLE IF EXISTS pr_development_attention_triggers`)
		require.NoError(t, err)
	}
	if version < 15 {
		_, err := db.Exec(`DROP TABLE IF EXISTS pr_development_attention_decision_runs`)
		require.NoError(t, err)
		_, err = db.Exec(`DROP INDEX IF EXISTS pr_development_ledger_entries_attention`)
		require.NoError(t, err)
	}
	_, err := db.Exec(`PRAGMA user_version = ` + strconv.Itoa(version))
	require.NoError(t, err)
}

func downgradeSchemaV17ForTest(t *testing.T, db *sql.DB) {
	t.Helper()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, beginSchemaTestMigration(ctx, conn))
	rollback := true
	defer func() {
		if rollback {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	_, err = conn.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `DROP TABLE pr_development_controller_suspensions`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx,
		`CREATE TEMP TABLE pr_development_thread_controllers_v17_downgrade AS SELECT `+
			schemaV17PRDevelopmentControllerColumns+
			` FROM pr_development_thread_controllers`,
	)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `DROP TABLE pr_development_thread_controllers`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, schemaV10PRDevelopmentControllersTable)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx,
		`INSERT INTO pr_development_thread_controllers (`+
			schemaV17PRDevelopmentControllerColumns+`) SELECT `+
			schemaV17PRDevelopmentControllerColumns+
			` FROM pr_development_thread_controllers_v17_downgrade`,
	)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `DROP TABLE pr_development_thread_controllers_v17_downgrade`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx,
		schemaV10PRDevelopmentControllerWorkspaceIndex+"\n"+
			schemaV10PRDevelopmentControllerReservationIndex+"\n"+
			schemaV10PRDevelopmentControllerLeaseIndex,
	)
	require.NoError(t, err)
	require.NoError(t, commitSchemaTestMigration(ctx, conn))
	rollback = false
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

func readControllerRowsForSchemaTest(t *testing.T, db *sql.DB) [][]any {
	t.Helper()

	rows, err := db.Query(`SELECT ` + schemaV17PRDevelopmentControllerColumns + `
		FROM pr_development_thread_controllers ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	columns, err := rows.Columns()
	require.NoError(t, err)
	result := make([][]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		require.NoError(t, rows.Scan(destinations...))
		for index, value := range values {
			if bytes, ok := value.([]byte); ok {
				values[index] = append([]byte(nil), bytes...)
			}
		}
		result = append(result, values)
	}
	require.NoError(t, rows.Err())
	return result
}

func assertNoForeignKeyViolationsForSchemaTest(t *testing.T, db *sql.DB) {
	t.Helper()

	rows, err := db.Query(`PRAGMA foreign_key_check`)
	require.NoError(t, err)
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var foreignKey int
		require.NoError(t, rows.Scan(&table, &rowID, &parent, &foreignKey))
		t.Fatalf(
			"foreign-key violation: table=%s rowid=%v parent=%s key=%d",
			table,
			rowID,
			parent,
			foreignKey,
		)
	}
	require.NoError(t, rows.Err())
}

func validateControllerTableForSchemaTest(
	ctx context.Context,
	db *sql.DB,
	expected string,
) error {
	var actual string
	if err := db.QueryRowContext(ctx, `
		SELECT sql FROM sqlite_schema
		WHERE type = 'table' AND name = 'pr_development_thread_controllers'`,
	).Scan(&actual); err != nil {
		return err
	}
	return validateSchemaDefinition(
		"pr_development_thread_controllers",
		actual,
		expected,
	)
}
