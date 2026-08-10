//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRecoveryScannerQueryUsesStableGlobalSourceOrder(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	for _, statement := range []string{
		`CREATE TABLE pr_development_repair_sessions (id TEXT, case_id TEXT)`,
		`CREATE TABLE pr_development_repair_attempts (id TEXT, session_id TEXT)`,
		`CREATE TABLE pr_development_controller_operation_intents (
			id TEXT, controller_id TEXT, attempt_id TEXT, recovery_id TEXT,
			recovery_revision INTEGER, recovery_staged_at INTEGER,
			status TEXT, claim_until INTEGER
		)`,
		`CREATE TABLE pr_development_controller_recovery_intents (
			id TEXT, controller_id TEXT, attempt_id TEXT, recovery_revision INTEGER,
			created_at INTEGER, mode TEXT, status TEXT, claim_until INTEGER
		)`,
		`INSERT INTO pr_development_repair_sessions VALUES
			('session-old', 'case-old'), ('session-new', 'case-new')`,
		`INSERT INTO pr_development_repair_attempts VALUES
			('attempt-old', 'session-old'), ('attempt-new', 'session-new')`,
		`INSERT INTO pr_development_controller_recovery_intents VALUES
			('recovery-old', 'controller-old', 'attempt-old', 3,
			 100, 'bound', 'pending', NULL)`,
		`INSERT INTO pr_development_controller_operation_intents VALUES
			('operation-new', 'controller-new', 'attempt-new', 'recovery-new', 7,
			 200, 'recovery_pending', NULL)`,
	} {
		_, err = db.Exec(statement)
		require.NoError(t, err)
	}

	scanKind := func(now int64) (string, int64) {
		t.Helper()
		var kind, caseID, controllerID, attemptID, recoveryID, operationID string
		var revision, availableAt int64
		err = db.QueryRow(
			prDevelopmentNextRecoveryQuery,
			now,
			now,
		).Scan(
			&kind,
			&caseID,
			&controllerID,
			&attemptID,
			&recoveryID,
			&operationID,
			&revision,
			&availableAt,
		)
		require.NoError(t, err)
		return kind, availableAt
	}

	kind, availableAt := scanKind(300)
	require.Equal(t, string(PRDevelopmentControllerRecoveryWorkReservation), kind)
	require.EqualValues(t, 100, availableAt)
	_, err = db.Exec(`UPDATE pr_development_controller_recovery_intents
		SET status = 'claimed', claim_until = 250 WHERE id = 'recovery-old'`)
	require.NoError(t, err)
	kind, availableAt = scanKind(300)
	require.Equal(t, string(PRDevelopmentControllerRecoveryWorkReservation), kind)
	require.EqualValues(t, 250, availableAt,
		"a mutable reclaim deadline must not change stable source priority")
	_, err = db.Exec(`UPDATE pr_development_controller_recovery_intents
		SET claim_until = 400 WHERE id = 'recovery-old'`)
	require.NoError(t, err)
	kind, availableAt = scanKind(300)
	require.Equal(t, string(PRDevelopmentControllerRecoveryWorkOperation), kind)
	require.EqualValues(t, 200, availableAt)
}

func TestStoreRecoveryScannerStagesAndFindsBoundReservationRecovery(t *testing.T) {
	t.Parallel()

	fixture := newLegacyBoundPRDevelopmentOperationFixture(t, ":memory:")
	*fixture.Clock = fixture.Clock.Add(2 * time.Minute)

	staged, err := fixture.Store.StageExpiredPRDevelopmentControllerRecoveries(
		context.Background(),
		maxPRDevelopmentRecoveryStageBatch,
	)
	require.NoError(t, err)
	require.Equal(t, 1, staged)

	candidate, found, err := fixture.Store.NextPRDevelopmentControllerRecovery(
		context.Background(),
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, PRDevelopmentControllerRecoveryWorkReservation, candidate.Kind)
	require.Equal(t, fixture.Case.ID, candidate.CaseID)
	require.Equal(t, fixture.Mutation.Controller.ID, candidate.ControllerID)
	require.Equal(t, fixture.Attempt.ID, candidate.AttemptID)
	require.Empty(t, candidate.OperationID)
	require.NotEmpty(t, candidate.RecoveryID)
	require.Equal(t, fixture.Mutation.Controller.Revision+1, candidate.ExpectedRevision)
	require.Equal(t, *fixture.Clock, candidate.AvailableAt)

	staged, err = fixture.Store.StageExpiredPRDevelopmentControllerRecoveries(
		context.Background(),
		maxPRDevelopmentRecoveryStageBatch,
	)
	require.NoError(t, err)
	require.Zero(t, staged, "staging replay must not create another recovery intent")
}

func TestStoreRecoveryScannerPrefersPreparedOperationRecovery(t *testing.T) {
	t.Parallel()

	prepared := preparePRDevelopmentOperationRecoveryTarget(
		t,
		PRDevelopmentControllerOperationCommit,
	)
	*prepared.Fixture.Clock = prepared.Fixture.Clock.Add(2 * time.Minute)

	staged, err := prepared.Fixture.Store.StageExpiredPRDevelopmentControllerRecoveries(
		context.Background(),
		1,
	)
	require.NoError(t, err)
	require.Equal(t, 1, staged)

	candidate, found, err := prepared.Fixture.Store.NextPRDevelopmentControllerRecovery(
		context.Background(),
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, PRDevelopmentControllerRecoveryWorkOperation, candidate.Kind)
	require.Equal(t, prepared.Fixture.Case.ID, candidate.CaseID)
	require.Equal(t, prepared.Operation.ControllerID, candidate.ControllerID)
	require.Equal(t, prepared.Operation.AttemptID, candidate.AttemptID)
	require.Equal(t, prepared.Operation.ID, candidate.OperationID)
	require.True(t, validPrefixedHexID(
		candidate.RecoveryID,
		prDevelopmentRecoveryIntentIDPrefix,
	), "staging must expose the newly assigned durable recovery ID")
	require.Equal(t, prepared.Operation.PreparedControllerRevision+1, candidate.ExpectedRevision)
}

func TestStoreRecoveryScannerLeavesLegacyUnboundRecoveryOperatorBlocked(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentOperationFixture(t, ":memory:")
	*fixture.Clock = fixture.Clock.Add(2 * time.Minute)

	staged, err := fixture.Store.StageExpiredPRDevelopmentControllerRecoveries(
		context.Background(),
		maxPRDevelopmentRecoveryStageBatch,
	)
	require.NoError(t, err)
	require.Equal(t, 1, staged)

	_, found, err := fixture.Store.NextPRDevelopmentControllerRecovery(
		context.Background(),
	)
	require.NoError(t, err)
	require.False(t, found)

	controller, err := fixture.Store.GetPRDevelopmentControllerForCase(
		context.Background(),
		fixture.Case.ID,
	)
	require.NoError(t, err)
	require.Equal(t, PRDevelopmentControllerRecoveryRequired, controller.Phase)
}

func TestStoreRecoveryScannerRejectsUnboundedStageLimit(t *testing.T) {
	t.Parallel()

	store, _, _ := newPRDevelopmentStoreFixture(t, ":memory:")
	for _, limit := range []int{0, maxPRDevelopmentRecoveryStageBatch + 1} {
		staged, err := store.StageExpiredPRDevelopmentControllerRecoveries(
			context.Background(),
			limit,
		)
		require.Error(t, err)
		require.Zero(t, staged)
	}
}
