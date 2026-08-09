//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertPRDevelopmentRecoveryIntentForTest(
	ctx context.Context,
	statement *sql.Stmt,
	intent *PRDevelopmentControllerRecoveryIntent,
) error {
	_, err := statement.ExecContext(
		ctx,
		intent.ID,
		intent.ControllerID,
		intent.AttemptID,
		intent.Ordinal,
		intent.RecoveryRevision,
		intent.Mode,
		intent.Status,
		intent.AgentID,
		intent.WorkspaceID,
		intent.LineID,
		intent.SourceCloneURL,
		intent.SourceRef,
		intent.SourceCommit,
		intent.SourceTree,
		intent.LineVersion,
		intent.MutationEpoch,
		intent.TipCommit,
		intent.Tree,
		intent.PreviousReservationKey,
		intent.ReplacementReservationKey,
		intent.PreviousReservationDigest,
		intent.ReplacementReservationDigest,
		intent.ExpiredControllerRevision,
		intent.ExpiredLeaseEpoch,
		intent.ExpiredLeaseTokenDigest,
		intent.PreviousHash,
		intent.IntentHash,
		intent.ClaimID,
		intent.ClaimOwner,
		intent.ClaimToken,
		nil,
		intent.ClaimEpoch,
		intent.Claims,
		intent.RotationResultHash,
		intent.RecoveryClaimTokenDigest,
		intent.NewMutationLeaseEpoch,
		intent.NewMutationLeaseTokenDigest,
		toDBTime(*intent.NewMutationLeaseUntil),
		intent.FinalRevision,
		intent.FinalHash,
		toDBTime(intent.CreatedAt),
		toDBTime(*intent.ClaimedAt),
		toDBTime(*intent.FinalizedAt),
		toDBTime(intent.UpdatedAt),
	)
	return err
}

func TestStorePRDevelopmentControllerRecoveryRotatesAuthorityAndReplaysExactly(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(
		t,
		store,
		developmentCase.ID,
	)
	fixture := bindPRDevelopmentControllerForTest(
		t,
		store,
		developmentCase.ID,
		completed,
	)
	attempt := completed.Attempts[len(completed.Attempts)-1]

	*clock = clock.Add(2 * time.Minute)
	_, _, err = store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: fixture.Bound.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "replacement-mutation-worker",
			Lease:            time.Minute,
		},
	)
	require.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)

	recovery, err := store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.Equal(t, PRDevelopmentControllerRecoveryRequired, recovery.Phase)
	require.Equal(t, fixture.Bound.Revision+1, recovery.Revision)

	claimInput := PRDevelopmentControllerRecoveryClaim{
		CaseID:           developmentCase.ID,
		AttemptID:        attempt.ID,
		ExpectedRevision: recovery.Revision,
		ClaimID:          "recovery-claim-one",
		WorkerLabel:      "recovery-worker",
		Lease:            time.Minute,
	}
	claim, changed, err := store.ClaimPRDevelopmentControllerRecovery(ctx, claimInput)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, PRDevelopmentControllerRecoveryClaimed, claim.Intent.Status)
	assert.Equal(t, completed.AgentID, claim.Intent.AgentID)
	assert.NotEqual(t, claim.Intent.AgentID, claim.Intent.ClaimOwner)
	require.Equal(t, fixture.Bound.MutationReservationKey, claim.Intent.PreviousReservationKey)
	require.NotEmpty(t, claim.Intent.ReplacementReservationKey)
	require.NotEqual(
		t,
		claim.Intent.PreviousReservationKey,
		claim.Intent.ReplacementReservationKey,
	)
	require.NotEmpty(t, claim.Intent.ClaimToken)
	require.NotNil(t, claim.Intent.ClaimUntil)
	_, err = store.db.ExecContext(ctx, `
		UPDATE pr_development_controller_recovery_intents
		SET claim_until = updated_at
		WHERE id = ?`, claim.Intent.ID)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "constraint")

	replayedClaim, changed, err := store.ClaimPRDevelopmentControllerRecovery(
		ctx,
		claimInput,
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, claim.Intent.ClaimToken, replayedClaim.Intent.ClaimToken)
	assert.Equal(t, claim.Intent.ClaimEpoch, replayedClaim.Intent.ClaimEpoch)

	_, _, err = store.ClaimPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryClaim{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: recovery.Revision,
			ClaimID:          "competing-recovery-claim",
			WorkerLabel:      "competing-worker",
			Lease:            time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerActive)

	originalDeadline := *claim.Intent.ClaimUntil
	require.NoError(t, store.RenewPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryRenew{
			ControllerID: claim.Controller.ID,
			AttemptID:    attempt.ID,
			RecoveryID:   claim.Intent.ID,
			ClaimID:      claim.Intent.ClaimID,
			ClaimToken:   claim.Intent.ClaimToken,
			ClaimEpoch:   claim.Intent.ClaimEpoch,
			Lease:        time.Second,
		},
	))
	renewedIntent, found, err := loadPRDevelopmentRecoveryIntentByID(
		ctx,
		store.db,
		claim.Intent.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, renewedIntent.ClaimUntil)
	assert.Equal(t, originalDeadline, *renewedIntent.ClaimUntil)

	finalize := PRDevelopmentControllerRecoveryFinalize{
		ControllerID:     claim.Controller.ID,
		AttemptID:        attempt.ID,
		RecoveryID:       claim.Intent.ID,
		ExpectedRevision: recovery.Revision,
		ClaimID:          claim.Intent.ClaimID,
		ClaimToken:       claim.Intent.ClaimToken,
		ClaimEpoch:       claim.Intent.ClaimEpoch,
		Rotation: PRDevelopmentControllerRecoveryRotationResult{
			WorkspaceID:   claim.Intent.WorkspaceID,
			Bound:         true,
			Version:       claim.Intent.LineVersion,
			MutationEpoch: claim.Intent.MutationEpoch,
			Tip:           claim.Intent.TipCommit,
			Tree:          claim.Intent.Tree,
			RotationHash:  strings.Repeat("9", 64),
		},
		Lease: time.Minute,
	}
	recovered, changed, err := store.FinalizePRDevelopmentControllerRecovery(ctx, finalize)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, PRDevelopmentControllerMutation, recovered.Phase)
	assert.Equal(t, recovery.Revision+1, recovered.Revision)
	assert.Equal(t, claim.Intent.ReplacementReservationKey, recovered.MutationReservationKey)
	assert.Equal(t, fixture.Bound.LeaseEpoch+1, recovered.LeaseEpoch)
	assert.NotEqual(t, fixture.Bound.LeaseToken, recovered.LeaseToken)

	finalizedIntent, found, err := loadPRDevelopmentRecoveryIntentByID(
		ctx,
		store.db,
		claim.Intent.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, PRDevelopmentControllerRecoveryFinalized, finalizedIntent.Status)
	assert.Empty(t, finalizedIntent.PreviousReservationKey)
	assert.Empty(t, finalizedIntent.ReplacementReservationKey)
	assert.Empty(t, finalizedIntent.ClaimToken)
	assert.NotEmpty(t, finalizedIntent.FinalHash)

	replayed, changed, err := store.FinalizePRDevelopmentControllerRecovery(ctx, finalize)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, recovered, replayed)
	require.NoError(t, store.RenewPRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerRenew{
			ControllerID: recovered.ID,
			AttemptID:    attempt.ID,
			LeaseToken:   recovered.LeaseToken,
			LeaseEpoch:   recovered.LeaseEpoch,
			Lease:        2 * time.Minute,
		},
	))
	sameClockProgress, err := store.GetPRDevelopmentControllerForCase(
		ctx,
		developmentCase.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, recovered.UpdatedAt, sameClockProgress.UpdatedAt)
	require.NotNil(t, recovered.LeaseUntil)
	require.NotNil(t, sameClockProgress.LeaseUntil)
	assert.True(t, sameClockProgress.LeaseUntil.After(*recovered.LeaseUntil))
	leakedAfterSameClockRenew, changed, err := store.FinalizePRDevelopmentControllerRecovery(
		ctx,
		finalize,
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed)
	assert.Empty(t, leakedAfterSameClockRenew.MutationReservationKey)
	require.NotNil(t, finalizedIntent.NewMutationLeaseUntil)
	earlyRecoveredDeadline := finalizedIntent.NewMutationLeaseUntil.Add(-time.Second)
	_, err = store.db.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET lease_until = ?
		WHERE id = ?`, toDBTime(earlyRecoveredDeadline), recovered.ID)
	require.NoError(t, err)
	_, err = store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "replacement authority")
	_, err = store.db.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET lease_until = ?
		WHERE id = ?`, toDBTime(*sameClockProgress.LeaseUntil), recovered.ID)
	require.NoError(t, err)
	changedRotation := finalize
	changedRotation.Rotation.RotationHash = strings.Repeat("6", 64)
	_, changed, err = store.FinalizePRDevelopmentControllerRecovery(
		ctx,
		changedRotation,
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed)
	_, err = store.db.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET lease_owner = 'drifted-recovery-owner'
		WHERE id = ?`, recovered.ID)
	require.NoError(t, err)
	leaked, changed, err := store.FinalizePRDevelopmentControllerRecovery(ctx, finalize)
	require.Error(t, err)
	assert.False(t, changed)
	assert.Empty(t, leaked.MutationReservationKey)
	_, err = store.db.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET lease_owner = ?
		WHERE id = ?`, claim.Intent.ClaimOwner, recovered.ID)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET mutation_reservation_key = ?
		WHERE id = ?`, claim.Intent.PreviousReservationKey, recovered.ID)
	require.NoError(t, err)
	_, err = store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "latest recovered authority")
	_, err = store.db.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET mutation_reservation_key = ?
		WHERE id = ?`, recovered.MutationReservationKey, recovered.ID)
	require.NoError(t, err)
	_, err = store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	*clock = clock.Add(time.Second)
	require.NoError(t, store.RenewPRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerRenew{
			ControllerID: recovered.ID,
			AttemptID:    attempt.ID,
			LeaseToken:   recovered.LeaseToken,
			LeaseEpoch:   recovered.LeaseEpoch,
			Lease:        time.Minute,
		},
	))
	_, changed, err = store.FinalizePRDevelopmentControllerRecovery(ctx, finalize)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed)

	_, changed, err = store.BindPRDevelopmentControllerLine(
		ctx,
		PRDevelopmentControllerLineBind{
			ControllerID:     recovered.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: fixture.Bound.Revision,
			LeaseToken:       fixture.Bound.LeaseToken,
			LeaseEpoch:       fixture.Bound.LeaseEpoch,
			WorkspaceID:      recovered.WorkspaceID,
			SourceCloneURL:   recovered.SourceCloneURL,
			SourceRef:        recovered.SourceRef,
			SourceCommit:     recovered.SourceCommit,
			SourceTree:       recovered.SourceTree,
			LineVersion:      recovered.LineVersion,
			MutationEpoch:    recovered.MutationEpoch,
			TipCommit:        recovered.TipCommit,
			Tree:             recovered.Tree,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed)

	fence, changed, err := store.RecordPRDevelopmentAttemptReviewFence(
		ctx,
		PRDevelopmentAttemptReviewFenceRecord{
			ControllerID:     recovered.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: recovered.Revision,
			LeaseToken:       recovered.LeaseToken,
			LeaseEpoch:       recovered.LeaseEpoch,
			LineVersion:      1,
			MutationEpoch:    1,
			ParkIntentID:     "park-after-recovery",
			BaseCommit:       recovered.TipCommit,
			TipCommit:        strings.Repeat("c", 40),
			Tree:             strings.Repeat("d", 40),
			LineReviewDigest: strings.Repeat("e", 64),
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, finalizedIntent.ReplacementReservationDigest, fence.MutationReservationDigest)

	_, changed, err = store.FinalizePRDevelopmentControllerRecovery(ctx, finalize)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed)

	tampered := finalizedIntent
	tampered.RecoveryRevision--
	tampered.ExpiredControllerRevision--
	tampered.FinalRevision--
	tampered.IntentHash = hashPRDevelopmentRecoveryIntent(tampered)
	tampered.FinalHash = hashPRDevelopmentRecoveryFinal(
		tampered,
		tampered.RotationResultHash,
		tampered.RecoveryClaimTokenDigest,
		tampered.NewMutationLeaseEpoch,
		tampered.NewMutationLeaseTokenDigest,
		*tampered.NewMutationLeaseUntil,
		tampered.FinalRevision,
		*tampered.FinalizedAt,
	)
	_, err = store.db.ExecContext(ctx, `
		UPDATE pr_development_controller_recovery_intents
		SET recovery_revision = ?, expired_controller_revision = ?, final_revision = ?,
			intent_hash = ?, final_hash = ?
		WHERE id = ?`,
		tampered.RecoveryRevision,
		tampered.ExpiredControllerRevision,
		tampered.FinalRevision,
		tampered.IntentHash,
		tampered.FinalHash,
		tampered.ID,
	)
	require.NoError(t, err)
	_, err = store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "first recovery revision")
}

func TestStorePRDevelopmentControllerRecoveryClaimReclaimsExpiredExactIntent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(t, store, developmentCase.ID)
	fixture := bindPRDevelopmentControllerForTest(t, store, developmentCase.ID, completed)
	attempt := completed.Attempts[len(completed.Attempts)-1]
	*clock = clock.Add(2 * time.Minute)
	_, _, err = store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: fixture.Bound.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "expire-for-reclaim",
			Lease:            time.Minute,
		},
	)
	require.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	recovery, err := store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	first, changed, err := store.ClaimPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryClaim{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: recovery.Revision,
			ClaimID:          "first-recovery-claim",
			WorkerLabel:      "first-recovery-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)

	*clock = clock.Add(2 * time.Minute)
	second, changed, err := store.ClaimPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryClaim{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: recovery.Revision,
			ClaimID:          "second-recovery-claim",
			WorkerLabel:      "second-recovery-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.True(t, second.Reclaimed)
	assert.Equal(t, first.Intent.ID, second.Intent.ID)
	assert.Equal(t, first.Intent.PreviousReservationKey, second.Intent.PreviousReservationKey)
	assert.Equal(t, first.Intent.ReplacementReservationKey, second.Intent.ReplacementReservationKey)
	assert.Equal(t, first.Intent.ClaimEpoch+1, second.Intent.ClaimEpoch)
	assert.NotEqual(t, first.Intent.ClaimToken, second.Intent.ClaimToken)

	err = store.RenewPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryRenew{
			ControllerID: first.Controller.ID,
			AttemptID:    attempt.ID,
			RecoveryID:   first.Intent.ID,
			ClaimID:      first.Intent.ClaimID,
			ClaimToken:   first.Intent.ClaimToken,
			ClaimEpoch:   first.Intent.ClaimEpoch,
			Lease:        time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrStaleLease)
}

func TestStorePRDevelopmentControllerRecoveryUnboundFinalizesAndBinds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(t, store, developmentCase.ID)
	attempt := completed.Attempts[len(completed.Attempts)-1]
	mutation, acquired, err := store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: 0,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "unbound-mutation-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	*clock = clock.Add(2 * time.Minute)
	_, _, err = store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: mutation.Controller.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "unbound-replacement-worker",
			Lease:            time.Minute,
		},
	)
	require.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	recovery, err := store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	claim, changed, err := store.ClaimPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryClaim{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: recovery.Revision,
			ClaimID:          "unbound-recovery-claim",
			WorkerLabel:      "unbound-recovery-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, PRDevelopmentControllerRecoveryUnbound, claim.Intent.Mode)
	finalize := PRDevelopmentControllerRecoveryFinalize{
		ControllerID:     claim.Controller.ID,
		AttemptID:        attempt.ID,
		RecoveryID:       claim.Intent.ID,
		ExpectedRevision: recovery.Revision,
		ClaimID:          claim.Intent.ClaimID,
		ClaimToken:       claim.Intent.ClaimToken,
		ClaimEpoch:       claim.Intent.ClaimEpoch,
		Rotation: PRDevelopmentControllerRecoveryRotationResult{
			WorkspaceID:  claim.Intent.WorkspaceID,
			RotationHash: strings.Repeat("8", 64),
		},
		Lease: time.Minute,
	}
	recovered, changed, err := store.FinalizePRDevelopmentControllerRecovery(ctx, finalize)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Empty(t, recovered.WorkspaceID)
	bound, changed, err := store.BindPRDevelopmentControllerLine(
		ctx,
		PRDevelopmentControllerLineBind{
			ControllerID:     recovered.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: recovered.Revision,
			LeaseToken:       recovered.LeaseToken,
			LeaseEpoch:       recovered.LeaseEpoch,
			WorkspaceID:      completed.WorkspaceID,
			SourceCloneURL:   completed.CloneURL,
			SourceRef:        completed.HeadRef,
			SourceCommit:     completed.HeadSHA,
			SourceTree:       strings.Repeat("b", 40),
			LineVersion:      0,
			MutationEpoch:    1,
			TipCommit:        completed.HeadSHA,
			Tree:             strings.Repeat("b", 40),
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, completed.WorkspaceID, bound.WorkspaceID)
}

func TestStorePRDevelopmentControllerRecoveryExpiredFinalizeReplayStartsNextIntent(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(t, store, developmentCase.ID)
	fixture := bindPRDevelopmentControllerForTest(t, store, developmentCase.ID, completed)
	attempt := completed.Attempts[len(completed.Attempts)-1]
	*clock = clock.Add(2 * time.Minute)
	_, _, err = store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: fixture.Bound.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "expire-before-finalize-replay",
			Lease:            time.Minute,
		},
	)
	require.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	recovery, err := store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	claim, _, err := store.ClaimPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryClaim{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: recovery.Revision,
			ClaimID:          "expiring-finalize-claim",
			WorkerLabel:      "expiring-finalize-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	finalize := PRDevelopmentControllerRecoveryFinalize{
		ControllerID:     claim.Controller.ID,
		AttemptID:        attempt.ID,
		RecoveryID:       claim.Intent.ID,
		ExpectedRevision: recovery.Revision,
		ClaimID:          claim.Intent.ClaimID,
		ClaimToken:       claim.Intent.ClaimToken,
		ClaimEpoch:       claim.Intent.ClaimEpoch,
		Rotation: PRDevelopmentControllerRecoveryRotationResult{
			WorkspaceID:   claim.Intent.WorkspaceID,
			Bound:         true,
			Version:       claim.Intent.LineVersion,
			MutationEpoch: claim.Intent.MutationEpoch,
			Tip:           claim.Intent.TipCommit,
			Tree:          claim.Intent.Tree,
			RotationHash:  strings.Repeat("7", 64),
		},
		Lease: time.Minute,
	}
	firstRecovered, changed, err := store.FinalizePRDevelopmentControllerRecovery(ctx, finalize)
	require.NoError(t, err)
	require.True(t, changed)
	*clock = clock.Add(2 * time.Minute)
	_, changed, err = store.FinalizePRDevelopmentControllerRecovery(ctx, finalize)
	require.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	assert.False(t, changed)
	secondRecovery, err := store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentControllerRecoveryRequired, secondRecovery.Phase)
	assert.Equal(t, firstRecovered.Revision+1, secondRecovery.Revision)
	intents, err := loadPRDevelopmentRecoveryIntents(ctx, store.db, firstRecovered.ID)
	require.NoError(t, err)
	require.Len(t, intents, 2)
	require.NotNil(t, intents[0].NewMutationLeaseUntil)
	earlySuccessorCreation := intents[0].NewMutationLeaseUntil.Add(-time.Second)
	intents[1].CreatedAt = earlySuccessorCreation
	intents[1].UpdatedAt = earlySuccessorCreation
	intents[1].IntentHash = hashPRDevelopmentRecoveryIntent(intents[1])
	_, err = store.db.ExecContext(ctx, `
		UPDATE pr_development_controller_recovery_intents
		SET created_at = ?, updated_at = ?, intent_hash = ?
		WHERE id = ?`,
		toDBTime(earlySuccessorCreation),
		toDBTime(earlySuccessorCreation),
		intents[1].IntentHash,
		intents[1].ID,
	)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET updated_at = ?
		WHERE id = ?`, toDBTime(earlySuccessorCreation), firstRecovered.ID)
	require.NoError(t, err)
	_, err = store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lease deadline succession")
}

func TestStorePRDevelopmentControllerRecoveryRejectsExpiredPreResumeMutation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(t, store, developmentCase.ID)
	_, ready := finishPRDevelopmentControllerReviewForTest(
		t,
		store,
		developmentCase.ID,
		completed,
	)
	next, admitted, err := store.AdmitPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairAdmit{
			CaseID:                      developmentCase.ID,
			ExpectedConversationVersion: 0,
			ExpectedRepairVersion:       completed.Version,
			IdempotencyKey:              "pre-resume-recovery-attempt",
			AgentID:                     "main",
			Instruction:                 "Exercise pre-resume recovery fencing.",
		},
	)
	require.NoError(t, err)
	require.True(t, admitted)
	nextAttempt := next.RepairSession.Attempts[len(next.RepairSession.Attempts)-1]
	mutation, acquired, err := store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        nextAttempt.ID,
			ExpectedRevision: ready.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "pre-resume-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, mutation.Controller.LineVersion, mutation.Controller.MutationEpoch)
	*clock = clock.Add(2 * time.Minute)
	_, _, err = store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        nextAttempt.ID,
			ExpectedRevision: mutation.Controller.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "pre-resume-replacement",
			Lease:            time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	unchanged, err := store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentControllerMutation, unchanged.Phase)
	assert.Equal(t, mutation.Controller.Revision, unchanged.Revision)
	var intentCount int
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pr_development_controller_recovery_intents`,
	).Scan(&intentCount))
	assert.Zero(t, intentCount)
}

func TestStorePRDevelopmentControllerRecoveryLegacyMissingIntentFailsClosed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(t, store, developmentCase.ID)
	fixture := bindPRDevelopmentControllerForTest(t, store, developmentCase.ID, completed)
	attempt := completed.Attempts[len(completed.Attempts)-1]
	*clock = clock.Add(2 * time.Minute)
	_, _, err = store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: fixture.Bound.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "expire-for-legacy",
			Lease:            time.Minute,
		},
	)
	require.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	result, err := store.db.ExecContext(
		ctx,
		`DELETE FROM pr_development_controller_recovery_intents WHERE controller_id = ?`,
		fixture.Bound.ID,
	)
	require.NoError(t, err)
	deleted, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	recovery, err := store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentControllerRecoveryRequired, recovery.Phase)
	_, changed, err := store.ClaimPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryClaim{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: recovery.Revision,
			ClaimID:          "legacy-recovery-claim",
			WorkerLabel:      "legacy-recovery-worker",
			Lease:            time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	assert.False(t, changed)
	var intentCount int
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pr_development_controller_recovery_intents`,
	).Scan(&intentCount))
	assert.Zero(t, intentCount, "claim must never synthesize missing recovery proof")
}

func TestStorePRDevelopmentControllerRecoveryOrdinalMatchesWorkspaceCapacity(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 8_192, MaxPRDevelopmentControllerRecoveries)
	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(t, store, developmentCase.ID)
	fixture := bindPRDevelopmentControllerForTest(t, store, developmentCase.ID, completed)
	attempt := completed.Attempts[len(completed.Attempts)-1]
	*clock = clock.Add(2 * time.Minute)
	_, _, err = store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: fixture.Bound.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "capacity-expiry-worker",
			Lease:            time.Minute,
		},
	)
	require.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	_, err = store.db.ExecContext(ctx, `
		UPDATE pr_development_controller_recovery_intents
		SET ordinal = ?`, MaxPRDevelopmentControllerRecoveries)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "constraint")
}

func TestStorePRDevelopmentControllerRecoveryCapacityRejectsBeforeMutation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(t, store, developmentCase.ID)
	fixture := bindPRDevelopmentControllerForTest(t, store, developmentCase.ID, completed)
	attempt := completed.Attempts[len(completed.Attempts)-1]

	seededAt := fixture.Bound.UpdatedAt
	seededLeaseUntil := seededAt.Add(time.Minute)
	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", 44), ", ")
	statement, err := tx.PrepareContext(ctx, `
		INSERT INTO pr_development_controller_recovery_intents (`+
		prDevelopmentRecoveryIntentColumns+`) VALUES (`+placeholders+`)`)
	require.NoError(t, err)
	defer func() { require.NoError(t, statement.Close()) }()
	for ordinal := 0; ordinal < MaxPRDevelopmentControllerRecoveries; ordinal++ {
		recoveryRevision := int64(ordinal) + 2
		intent := PRDevelopmentControllerRecoveryIntent{
			ID:                           fmt.Sprintf("pdri_%032x", ordinal+1),
			ControllerID:                 fixture.Bound.ID,
			AttemptID:                    attempt.ID,
			Ordinal:                      ordinal,
			RecoveryRevision:             recoveryRevision,
			Mode:                         PRDevelopmentControllerRecoveryUnbound,
			Status:                       PRDevelopmentControllerRecoveryFinalized,
			AgentID:                      fixture.Bound.AgentID,
			WorkspaceID:                  completed.WorkspaceID,
			LineID:                       fixture.Bound.LineID,
			SourceCloneURL:               completed.CloneURL,
			SourceRef:                    completed.HeadRef,
			SourceCommit:                 completed.HeadSHA,
			PreviousReservationDigest:    fmt.Sprintf("%064x", ordinal*2+1),
			ReplacementReservationDigest: fmt.Sprintf("%064x", ordinal*2+2),
			ExpiredControllerRevision:    recoveryRevision - 1,
			ExpiredLeaseEpoch:            1,
			ExpiredLeaseTokenDigest:      strings.Repeat("a", 64),
			PreviousHash:                 emptyPRDevelopmentRecoveryDigest(),
			ClaimID:                      fmt.Sprintf("capacity-claim-%d", ordinal),
			ClaimOwner:                   "capacity-worker",
			ClaimEpoch:                   1,
			Claims:                       1,
			RotationResultHash:           strings.Repeat("b", 64),
			RecoveryClaimTokenDigest:     strings.Repeat("c", 64),
			NewMutationLeaseEpoch:        2,
			NewMutationLeaseTokenDigest:  strings.Repeat("d", 64),
			NewMutationLeaseUntil:        &seededLeaseUntil,
			FinalRevision:                recoveryRevision + 1,
			CreatedAt:                    seededAt,
			ClaimedAt:                    &seededAt,
			FinalizedAt:                  &seededAt,
			UpdatedAt:                    seededAt,
		}
		intent.IntentHash = hashPRDevelopmentRecoveryIntent(intent)
		intent.FinalHash = hashPRDevelopmentRecoveryFinal(
			intent,
			intent.RotationResultHash,
			intent.RecoveryClaimTokenDigest,
			intent.NewMutationLeaseEpoch,
			intent.NewMutationLeaseTokenDigest,
			*intent.NewMutationLeaseUntil,
			intent.FinalRevision,
			*intent.FinalizedAt,
		)
		err = insertPRDevelopmentRecoveryIntentForTest(ctx, statement, &intent)
		require.NoError(t, err, "seed finalized recovery ordinal %d", ordinal)
	}
	require.NoError(t, tx.Commit())

	loadController := func() PRDevelopmentController {
		t.Helper()
		controller, loadErr := scanPRDevelopmentController(store.db.QueryRowContext(ctx, `
			SELECT `+prDevelopmentControllerColumns+`
			FROM pr_development_thread_controllers
			WHERE id = ?`, fixture.Bound.ID))
		require.NoError(t, loadErr)
		return controller
	}
	loadRawBearer := func() []byte {
		t.Helper()
		var bearer []byte
		require.NoError(t, store.db.QueryRowContext(ctx, `
			SELECT CAST(mutation_reservation_key AS BLOB)
			FROM pr_development_thread_controllers
			WHERE id = ?`, fixture.Bound.ID).Scan(&bearer))
		return append([]byte(nil), bearer...)
	}

	beforeController := loadController()
	beforeBearer := loadRawBearer()
	beforeHistory, err := loadPRDevelopmentRecoveryIntents(ctx, store.db, fixture.Bound.ID)
	require.NoError(t, err)
	require.Len(t, beforeHistory, MaxPRDevelopmentControllerRecoveries)

	*clock = clock.Add(2 * time.Minute)
	conn, err := store.db.Conn(ctx)
	require.NoError(t, err)
	expiryErr := expirePRDevelopmentMutationLease(ctx, conn, fixture.Bound, *clock)
	require.NoError(t, conn.Close())
	require.ErrorIs(t, expiryErr, ErrPRDevelopmentControllerConflict)
	assert.Contains(t, expiryErr.Error(), "recovery history capacity exhausted")

	afterController := loadController()
	afterBearer := loadRawBearer()
	afterHistory, err := loadPRDevelopmentRecoveryIntents(ctx, store.db, fixture.Bound.ID)
	require.NoError(t, err)
	assert.Equal(t, beforeController, afterController, "controller row changed on rejection")
	assert.Equal(t, beforeBearer, afterBearer, "raw reservation bearer changed on rejection")
	assert.Equal(t, beforeHistory, afterHistory, "recovery history changed on rejection")
}

func TestStorePRDevelopmentControllerRecoveryRejectsCrossControllerAuthorityReuse(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	firstCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	firstCompleted := completePRDevelopmentRepairForControllerTest(
		t,
		store,
		firstCase.ID,
	)
	firstFixture := bindPRDevelopmentControllerForTest(
		t,
		store,
		firstCase.ID,
		firstCompleted,
	)
	firstAttempt := firstCompleted.Attempts[len(firstCompleted.Attempts)-1]
	*clock = clock.Add(2 * time.Minute)
	_, _, err = store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           firstCase.ID,
			AttemptID:        firstAttempt.ID,
			ExpectedRevision: firstFixture.Bound.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "cross-controller-expiry",
			Lease:            time.Minute,
		},
	)
	require.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	firstRecovery, err := store.GetPRDevelopmentControllerForCase(ctx, firstCase.ID)
	require.NoError(t, err)
	firstClaim, _, err := store.ClaimPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryClaim{
			CaseID:           firstCase.ID,
			AttemptID:        firstAttempt.ID,
			ExpectedRevision: firstRecovery.Revision,
			ClaimID:          "cross-controller-recovery-claim",
			WorkerLabel:      "cross-controller-recovery-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	firstRecovered, _, err := store.FinalizePRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryFinalize{
			ControllerID:     firstClaim.Controller.ID,
			AttemptID:        firstAttempt.ID,
			RecoveryID:       firstClaim.Intent.ID,
			ExpectedRevision: firstRecovery.Revision,
			ClaimID:          firstClaim.Intent.ClaimID,
			ClaimToken:       firstClaim.Intent.ClaimToken,
			ClaimEpoch:       firstClaim.Intent.ClaimEpoch,
			Rotation: PRDevelopmentControllerRecoveryRotationResult{
				WorkspaceID:   firstClaim.Intent.WorkspaceID,
				Bound:         true,
				Version:       firstClaim.Intent.LineVersion,
				MutationEpoch: firstClaim.Intent.MutationEpoch,
				Tip:           firstClaim.Intent.TipCommit,
				Tree:          firstClaim.Intent.Tree,
				RotationHash:  strings.Repeat("5", 64),
			},
			Lease: time.Minute,
		},
	)
	require.NoError(t, err)
	_, changed, err := store.RecordPRDevelopmentAttemptReviewFence(
		ctx,
		PRDevelopmentAttemptReviewFenceRecord{
			ControllerID:     firstRecovered.ID,
			AttemptID:        firstAttempt.ID,
			ExpectedRevision: firstRecovered.Revision,
			LeaseToken:       firstRecovered.LeaseToken,
			LeaseEpoch:       firstRecovered.LeaseEpoch,
			LineVersion:      1,
			MutationEpoch:    1,
			ParkIntentID:     "cross-controller-park",
			BaseCommit:       firstRecovered.TipCommit,
			TipCommit:        strings.Repeat("c", 40),
			Tree:             strings.Repeat("d", 40),
			LineReviewDigest: strings.Repeat("e", 64),
		},
	)
	require.NoError(t, err)
	require.True(t, changed)

	secondCase := capturePRDevelopmentListCase(
		t,
		store,
		capture,
		"cross-controller-delivery",
		"other/project",
		43,
	)
	secondCompleted := completePRDevelopmentRepairForControllerTest(
		t,
		store,
		secondCase.ID,
	)
	secondAttempt := secondCompleted.Attempts[len(secondCompleted.Attempts)-1]
	secondMutation, acquired, err := store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           secondCase.ID,
			AttemptID:        secondAttempt.ID,
			ExpectedRevision: 0,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "foreign-active-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)

	intent, found, err := loadPRDevelopmentRecoveryIntentByID(
		ctx,
		store.db,
		firstClaim.Intent.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	intent.ReplacementReservationDigest = prDevelopmentMutationReservationDigest(
		secondMutation.Controller.MutationReservationKey,
	)
	intent.IntentHash = hashPRDevelopmentRecoveryIntent(intent)
	intent.FinalHash = hashPRDevelopmentRecoveryFinal(
		intent,
		intent.RotationResultHash,
		intent.RecoveryClaimTokenDigest,
		intent.NewMutationLeaseEpoch,
		intent.NewMutationLeaseTokenDigest,
		*intent.NewMutationLeaseUntil,
		intent.FinalRevision,
		*intent.FinalizedAt,
	)
	_, err = store.db.ExecContext(ctx, `
		UPDATE pr_development_controller_recovery_intents
		SET replacement_reservation_digest = ?, intent_hash = ?, final_hash = ?
		WHERE id = ?`,
		intent.ReplacementReservationDigest,
		intent.IntentHash,
		intent.FinalHash,
		intent.ID,
	)
	require.NoError(t, err)
	_, err = store.GetPRDevelopmentControllerForCase(ctx, secondCase.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "foreign recovery history")
	err = store.RenewPRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerRenew{
			ControllerID: secondMutation.Controller.ID,
			AttemptID:    secondAttempt.ID,
			LeaseToken:   secondMutation.Controller.LeaseToken,
			LeaseEpoch:   secondMutation.Controller.LeaseEpoch,
			Lease:        time.Minute,
		},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "foreign recovery history")
	_, err = store.GetPRDevelopmentControllerForCase(ctx, firstCase.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reuses a repair reservation")
}

func TestStoreMigratesV11PostReviewRecoveryAtLegacyRevisionHighWater(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration-v11-controller-high-water.db")
	now := time.Date(2026, time.August, 5, 16, 0, 0, 0, time.UTC)
	store, err := Open(ctx, path, WithClock(func() time.Time { return now }))
	require.NoError(t, err)
	capture := validPRDevelopmentInputForTest()
	capture.PRDevelopmentCaptureIdentity = addPRDevelopmentDispatch(
		t,
		store,
		"legacy-high-water-delivery",
		"workflows/own-pr-feedback.yml",
		"revision-legacy-high-water",
	)
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(
		t,
		store,
		developmentCase.ID,
	)
	_, ready := finishPRDevelopmentControllerReviewForTest(
		t,
		store,
		developmentCase.ID,
		completed,
	)
	next, admitted, err := store.AdmitPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairAdmit{
			CaseID:                      developmentCase.ID,
			ExpectedConversationVersion: 0,
			ExpectedRepairVersion:       completed.Version,
			IdempotencyKey:              "legacy-high-water-attempt",
			AgentID:                     "main",
			Instruction:                 "Preserve a reachable v11 high-water state.",
		},
	)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NotNil(t, next.RepairSession)
	nextAttempt := next.RepairSession.Attempts[len(next.RepairSession.Attempts)-1]
	mutation, acquired, err := store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        nextAttempt.ID,
			ExpectedRevision: ready.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "legacy-high-water-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	resumed, changed, err := store.BindPRDevelopmentControllerLine(
		ctx,
		PRDevelopmentControllerLineBind{
			ControllerID:     mutation.Controller.ID,
			AttemptID:        nextAttempt.ID,
			ExpectedRevision: mutation.Controller.Revision,
			LeaseToken:       mutation.Controller.LeaseToken,
			LeaseEpoch:       mutation.Controller.LeaseEpoch,
			WorkspaceID:      ready.WorkspaceID,
			SourceCloneURL:   ready.SourceCloneURL,
			SourceRef:        ready.SourceRef,
			SourceCommit:     ready.SourceCommit,
			SourceTree:       ready.SourceTree,
			LineVersion:      ready.LineVersion,
			MutationEpoch:    ready.LineVersion + 1,
			TipCommit:        ready.TipCommit,
			Tree:             ready.Tree,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	fence, found, err := loadLatestPRDevelopmentReviewFence(ctx, store.db, resumed.ID)
	require.NoError(t, err)
	require.True(t, found)
	delta := MaxPRDevelopmentControllerRevision - 5 - fence.ReviewControllerRevision
	require.Positive(t, delta)
	fence.ReviewControllerRevision += delta
	fence.ReviewLeaseEpoch += delta
	fence.FenceHash = hashPRDevelopmentReviewFence(fence)
	_, err = store.db.ExecContext(ctx, `
		UPDATE pr_development_attempt_review_fences
		SET review_lease_epoch = ?, review_controller_revision = ?, fence_hash = ?
		WHERE controller_id = ? AND attempt_id = ?`,
		fence.ReviewLeaseEpoch,
		fence.ReviewControllerRevision,
		fence.FenceHash,
		fence.ControllerID,
		fence.AttemptID,
	)
	require.NoError(t, err)
	legacyRevision := fence.ReviewControllerRevision + 4
	legacyLeaseEpoch := fence.ReviewLeaseEpoch + 1
	_, err = store.db.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET revision = ?, phase = 'recovery_required', lease_kind = '',
			lease_owner = '', lease_token = '', lease_until = NULL,
			lease_epoch = ?, claims = ?, fences_digest = ?
		WHERE id = ?`,
		legacyRevision,
		legacyLeaseEpoch,
		legacyLeaseEpoch,
		fence.FenceHash,
		resumed.ID,
	)
	require.NoError(t, err)
	require.Equal(t, int64(MaxPRDevelopmentControllerRevision-1), legacyRevision)
	require.NoError(t, store.Close())

	legacyDB := openSchemaTestDB(t, path)
	_, err = legacyDB.Exec(`DROP TABLE pr_development_controller_recovery_intents`)
	require.NoError(t, err)
	setSchemaTestVersion(t, legacyDB, 11)
	require.NoError(t, legacyDB.Close())

	reopened, err := Open(ctx, path, WithClock(func() time.Time { return now }))
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	legacy, err := reopened.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentControllerRecoveryRequired, legacy.Phase)
	assert.Equal(t, legacyRevision, legacy.Revision)
	assert.Equal(t, ready.LineVersion+1, legacy.MutationEpoch)
	_, changed, err = reopened.ClaimPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryClaim{
			CaseID:           developmentCase.ID,
			AttemptID:        nextAttempt.ID,
			ExpectedRevision: legacyRevision,
			ClaimID:          "legacy-high-water-recovery-claim",
			WorkerLabel:      "legacy-high-water-recovery-worker",
			Lease:            time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	assert.False(t, changed)
	var recoveryRows int
	require.NoError(t, reopened.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pr_development_controller_recovery_intents`,
	).Scan(&recoveryRows))
	assert.Zero(t, recoveryRows, "migration and claim must not synthesize recovery proof")

	fence.ReviewControllerRevision++
	fence.ReviewLeaseEpoch++
	fence.FenceHash = hashPRDevelopmentReviewFence(fence)
	_, err = reopened.db.ExecContext(ctx, `
		UPDATE pr_development_attempt_review_fences
		SET review_lease_epoch = ?, review_controller_revision = ?, fence_hash = ?
		WHERE controller_id = ? AND attempt_id = ?`,
		fence.ReviewLeaseEpoch,
		fence.ReviewControllerRevision,
		fence.FenceHash,
		fence.ControllerID,
		fence.AttemptID,
	)
	require.NoError(t, err)
	legacyRevision = fence.ReviewControllerRevision + 4
	legacyLeaseEpoch = fence.ReviewLeaseEpoch + 1
	_, err = reopened.db.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET revision = ?, lease_epoch = ?, claims = ?, fences_digest = ?
		WHERE id = ?`,
		legacyRevision,
		legacyLeaseEpoch,
		legacyLeaseEpoch,
		fence.FenceHash,
		resumed.ID,
	)
	require.NoError(t, err)
	_, err = reopened.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "legacy revision ceiling")
}

func TestPRDevelopmentControllerRecoveryTypesAreJSONPrivate(t *testing.T) {
	t.Parallel()

	value := PRDevelopmentControllerRecoveryLease{
		Controller: PRDevelopmentController{MutationReservationKey: "raw-old"},
		Intent: PRDevelopmentControllerRecoveryIntent{
			PreviousReservationKey:    "raw-old",
			ReplacementReservationKey: "raw-new",
			ClaimToken:                "raw-claim",
		},
	}
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(encoded))
}
