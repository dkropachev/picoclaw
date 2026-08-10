//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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
