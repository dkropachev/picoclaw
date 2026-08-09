//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"path/filepath"
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
