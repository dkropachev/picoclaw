//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type expiredSuspendedResumeRecoveryFixture struct {
	suspendedPRDevelopmentResumeFixture
	Resume PRDevelopmentControllerSuspendedResumeLease
}

func newExpiredSuspendedResumeRecoveryFixture(
	t *testing.T,
) expiredSuspendedResumeRecoveryFixture {
	t.Helper()
	ctx := context.Background()
	fixture := newSuspendedPRDevelopmentResumeFixture(t)
	lease, changed, err := fixture.Store.
		AcquirePRDevelopmentRepairOrchestrationController(
			ctx,
			PRDevelopmentRepairOrchestrationControllerAcquire{
				CaseID:           fixture.Case.ID,
				AttemptID:        fixture.Attempt.ID,
				ClaimToken:       fixture.Run.ClaimToken,
				ExpectedRevision: fixture.Controller.Revision,
				WorkerLabel:      "expired-resume-setup-worker",
				Lease:            5 * time.Minute,
			},
		)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, lease.SuspendedResume)
	return expiredSuspendedResumeRecoveryFixture{
		suspendedPRDevelopmentResumeFixture: fixture,
		Resume:                              *lease.SuspendedResume,
	}
}

func TestStorePRDevelopmentSuspendedResumeRecoveryClaimReclaimFinalizeAndReplay(
	t *testing.T,
) {
	ctx := context.Background()
	fixture := newExpiredSuspendedResumeRecoveryFixture(t)
	require.NotNil(t, fixture.Resume.Suspension.ResumeClaimUntil)
	require.NotNil(t, fixture.Run.ClaimUntil)
	latestExpiry := *fixture.Resume.Suspension.ResumeClaimUntil
	if fixture.Run.ClaimUntil.After(latestExpiry) {
		latestExpiry = *fixture.Run.ClaimUntil
	}
	*fixture.Clock = latestExpiry.Add(time.Second)

	candidate, found, err := fixture.Store.
		NextPRDevelopmentControllerSuspendedResumeRecovery(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, fixture.Case.ID, candidate.CaseID)
	assert.Equal(t, fixture.Resume.Suspension.ID, candidate.SuspensionID)
	assert.Equal(t, fixture.Resume.Controller.Revision, candidate.ExpectedRevision)
	encodedCandidate, err := json.Marshal(candidate)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(encodedCandidate))

	firstInput := PRDevelopmentControllerSuspendedResumeRecoveryClaim{
		CaseID:           candidate.CaseID,
		SuspensionID:     candidate.SuspensionID,
		ControllerID:     candidate.ControllerID,
		AttemptID:        candidate.AttemptID,
		ExpectedRevision: candidate.ExpectedRevision,
		ClaimID: prDevelopmentSuspendedResumeIdentity(
			prDevelopmentSuspendedResumeRecoveryClaimPrefix,
			candidate.SuspensionID,
			"first-recovery-worker",
		),
		WorkerLabel: "first-recovery-worker",
		Lease:       time.Minute,
	}
	first, changed, err := fixture.Store.
		ClaimPRDevelopmentControllerSuspendedResumeRecovery(ctx, firstInput)
	require.NoError(t, err)
	require.True(t, changed)
	assert.True(t, first.Reclaimed)
	assert.Equal(t, fixture.Resume.Suspension.ResumeReservationKey,
		first.Suspension.ResumeReservationKey)
	assert.NotEqual(t, fixture.Resume.Suspension.ResumeClaimToken,
		first.Suspension.ResumeClaimToken)
	assert.Equal(t, fixture.Resume.Suspension.ResumeClaimEpoch+1,
		first.Suspension.ResumeClaimEpoch)

	replayedClaim, changed, err := fixture.Store.
		ClaimPRDevelopmentControllerSuspendedResumeRecovery(ctx, firstInput)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, first.Suspension.ResumeClaimToken,
		replayedClaim.Suspension.ResumeClaimToken)

	*fixture.Clock = first.Suspension.ResumeClaimUntil.Add(time.Second)
	candidate, found, err = fixture.Store.
		NextPRDevelopmentControllerSuspendedResumeRecovery(ctx)
	require.NoError(t, err)
	require.True(t, found)
	secondInput := firstInput
	secondInput.ClaimID = prDevelopmentSuspendedResumeIdentity(
		prDevelopmentSuspendedResumeRecoveryClaimPrefix,
		candidate.SuspensionID,
		"second-recovery-worker",
	)
	secondInput.WorkerLabel = "second-recovery-worker"
	second, changed, err := fixture.Store.
		ClaimPRDevelopmentControllerSuspendedResumeRecovery(ctx, secondInput)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, first.Suspension.ResumeClaimEpoch+1,
		second.Suspension.ResumeClaimEpoch)
	assert.NotEqual(t, first.Suspension.ResumeClaimToken,
		second.Suspension.ResumeClaimToken)

	staleFinalize := PRDevelopmentControllerSuspendedResumeRecoveryFinalize{
		SuspensionID:     first.Suspension.ID,
		ControllerID:     first.Controller.ID,
		AttemptID:        first.Suspension.ResumeAttemptID,
		ExpectedRevision: first.Controller.Revision,
		ClaimID:          first.Suspension.ResumeClaimID,
		ClaimToken:       first.Suspension.ResumeClaimToken,
		ClaimEpoch:       first.Suspension.ResumeClaimEpoch,
		Result:           suspendedResumeResultForTest(first.Suspension),
	}
	_, changed, err = fixture.Store.
		FinalizePRDevelopmentControllerSuspendedResumeRecovery(ctx, staleFinalize)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed)

	require.NoError(t, fixture.Store.RenewPRDevelopmentControllerSuspendedResumeRecovery(
		ctx,
		PRDevelopmentControllerSuspendedResumeRecoveryRenew{
			SuspensionID: second.Suspension.ID,
			ControllerID: second.Controller.ID,
			AttemptID:    second.Suspension.ResumeAttemptID,
			ClaimID:      second.Suspension.ResumeClaimID,
			ClaimToken:   second.Suspension.ResumeClaimToken,
			ClaimEpoch:   second.Suspension.ResumeClaimEpoch,
			Lease:        2 * time.Minute,
		},
	))
	second.Suspension, found, err = loadPRDevelopmentControllerSuspensionByID(
		ctx,
		fixture.Store.db,
		second.Suspension.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	finalize := PRDevelopmentControllerSuspendedResumeRecoveryFinalize{
		SuspensionID:     second.Suspension.ID,
		ControllerID:     second.Controller.ID,
		AttemptID:        second.Suspension.ResumeAttemptID,
		ExpectedRevision: second.Controller.Revision,
		ClaimID:          second.Suspension.ResumeClaimID,
		ClaimToken:       second.Suspension.ResumeClaimToken,
		ClaimEpoch:       second.Suspension.ResumeClaimEpoch,
		Result:           suspendedResumeResultForTest(second.Suspension),
	}
	transition, changed, err := fixture.Store.
		FinalizePRDevelopmentControllerSuspendedResumeRecovery(ctx, finalize)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, PRDevelopmentControllerSuspensionPending,
		transition.Controller.Phase)
	assert.Equal(t, PRDevelopmentControllerSuspensionStatusResumed,
		transition.Resumed.Status)
	assert.Equal(t, PRDevelopmentControllerSuspensionStatusSuspendClaimed,
		transition.NextSuspension.Status)
	assert.Equal(t, PRDevelopmentControllerSuspensionSourceSuspendedResumeRecovery,
		transition.NextSuspension.SourceKind)
	assert.Equal(t, transition.Resumed.ID,
		transition.NextSuspension.SourceRecoveryID)
	assert.Equal(t, transition.Resumed.ResumeFinalHash,
		transition.NextSuspension.PreviousHash)
	assert.Equal(t, fixture.Resume.Suspension.ResumeReservationKey,
		transition.NextSuspension.SuspensionReservationKey)
	assert.Equal(t, second.Suspension.ResumeClaimOwner,
		transition.NextSuspension.SuspendClaimOwner)
	assert.Equal(t, second.Suspension.ResumeClaimToken,
		transition.NextSuspension.SuspendClaimToken)
	assert.Equal(t, second.Suspension.ResumeClaimUntil,
		transition.NextSuspension.SuspendClaimUntil)
	assert.Equal(t, second.Suspension.ResumeClaimEpoch,
		transition.NextSuspension.SuspendClaimEpoch)
	assert.Empty(t, transition.Resumed.ResumeReservationKey)
	assert.Empty(t, transition.Resumed.ResumeRequest.ReservationKey)
	assert.Empty(t, transition.Resumed.ResumeClaimToken)
	assert.Nil(t, transition.Resumed.ResumeClaimUntil)
	assert.NotEmpty(t, transition.Resumed.ResumeClaimTokenDigest)

	var oldKey, oldToken, childKey, childToken string
	var oldRequest, childRequest []byte
	require.NoError(t, fixture.Store.db.QueryRowContext(ctx, `
		SELECT resume_reservation_key, resume_claim_token, resume_request_json
		FROM pr_development_controller_suspensions WHERE id = ?`,
		transition.Resumed.ID,
	).Scan(&oldKey, &oldToken, &oldRequest))
	require.NoError(t, fixture.Store.db.QueryRowContext(ctx, `
		SELECT suspension_reservation_key, suspend_claim_token, suspend_request_json
		FROM pr_development_controller_suspensions WHERE id = ?`,
		transition.NextSuspension.ID,
	).Scan(&childKey, &childToken, &childRequest))
	assert.Empty(t, oldKey)
	assert.Empty(t, oldToken)
	assert.Equal(t, fixture.Resume.Suspension.ResumeReservationKey, childKey)
	assert.Equal(t, second.Suspension.ResumeClaimToken, childToken)
	assert.False(t, bytes.Contains(oldRequest, []byte(childKey)))
	assert.False(t, bytes.Contains(childRequest, []byte(childKey)))

	finalize.Result.AlreadyResumed = true
	replayed, changed, err := fixture.Store.
		FinalizePRDevelopmentControllerSuspendedResumeRecovery(ctx, finalize)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, transition, replayed)

	childResult := suspensionResultForTest(transition.NextSuspension)
	childResult.SuspensionHash = strings.Repeat("c", 64)
	suspended, changed, err := fixture.Store.FinalizePRDevelopmentControllerSuspension(
		ctx,
		PRDevelopmentControllerSuspensionFinalize{
			SuspensionID:     transition.NextSuspension.ID,
			ControllerID:     transition.Controller.ID,
			AttemptID:        transition.NextSuspension.AttemptID,
			ExpectedRevision: transition.Controller.Revision,
			ClaimID:          transition.NextSuspension.SuspendClaimID,
			ClaimToken:       transition.NextSuspension.SuspendClaimToken,
			ClaimEpoch:       transition.NextSuspension.SuspendClaimEpoch,
			Result:           childResult,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, PRDevelopmentControllerSuspended, suspended.Controller.Phase)

	replayed, changed, err = fixture.Store.
		FinalizePRDevelopmentControllerSuspendedResumeRecovery(ctx, finalize)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, PRDevelopmentControllerSuspended, replayed.Controller.Phase)
	assert.Equal(t, PRDevelopmentControllerSuspensionStatusSuspended,
		replayed.NextSuspension.Status)
	assert.Empty(t, replayed.NextSuspension.SuspensionReservationKey)
	assert.Empty(t, replayed.NextSuspension.SuspendClaimToken)
	loaded, err := fixture.Store.GetPRDevelopmentControllerForCase(ctx, fixture.Case.ID)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentControllerSuspended, loaded.Phase)

	continuedRun, claimed, err := fixture.Store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{
			WorkerLabel: "continued-resume-worker",
			Lease:       5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, fixture.Attempt.ID, continuedRun.AttemptID)
	continued, changed, err := fixture.Store.
		AcquirePRDevelopmentRepairOrchestrationController(
			ctx,
			PRDevelopmentRepairOrchestrationControllerAcquire{
				CaseID:           fixture.Case.ID,
				AttemptID:        fixture.Attempt.ID,
				ClaimToken:       continuedRun.ClaimToken,
				ExpectedRevision: suspended.Controller.Revision,
				WorkerLabel:      "continued-resume-worker",
				Lease:            5 * time.Minute,
			},
		)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, continued.SuspendedResume)
	assert.Equal(t, suspended.Suspension.ID,
		continued.SuspendedResume.Suspension.ID)
	assert.Equal(t, fixture.Attempt.ID,
		continued.SuspendedResume.Suspension.ResumeAttemptID)
}

func TestStorePRDevelopmentSuspendedResumeRecoveryWaitsForParentExpiry(
	t *testing.T,
) {
	ctx := context.Background()
	fixture := newExpiredSuspendedResumeRecoveryFixture(t)
	require.NotNil(t, fixture.Resume.Suspension.ResumeClaimUntil)
	require.NoError(t, fixture.Store.RenewPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationRenew{
			AttemptID:  fixture.Run.AttemptID,
			ClaimToken: fixture.Run.ClaimToken,
			Lease:      20 * time.Minute,
		},
	))
	*fixture.Clock = fixture.Resume.Suspension.ResumeClaimUntil.Add(time.Second)

	_, found, err := fixture.Store.
		NextPRDevelopmentControllerSuspendedResumeRecovery(ctx)
	require.NoError(t, err)
	assert.False(t, found)
	_, changed, err := fixture.Store.ClaimPRDevelopmentControllerSuspendedResumeRecovery(
		ctx,
		PRDevelopmentControllerSuspendedResumeRecoveryClaim{
			CaseID:           fixture.Case.ID,
			SuspensionID:     fixture.Resume.Suspension.ID,
			ControllerID:     fixture.Resume.Controller.ID,
			AttemptID:        fixture.Resume.Suspension.ResumeAttemptID,
			ExpectedRevision: fixture.Resume.Controller.Revision,
			ClaimID: prDevelopmentSuspendedResumeIdentity(
				prDevelopmentSuspendedResumeRecoveryClaimPrefix,
				fixture.Resume.Suspension.ID,
				"blocked-recovery-worker",
			),
			WorkerLabel: "blocked-recovery-worker",
			Lease:       time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed)
}

func TestStorePRDevelopmentSuspendedResumeRecoveryChildIsCrashReclaimable(
	t *testing.T,
) {
	ctx := context.Background()
	fixture := newExpiredSuspendedResumeRecoveryFixture(t)
	require.NotNil(t, fixture.Resume.Suspension.ResumeClaimUntil)
	require.NotNil(t, fixture.Run.ClaimUntil)
	latestExpiry := *fixture.Resume.Suspension.ResumeClaimUntil
	if fixture.Run.ClaimUntil.After(latestExpiry) {
		latestExpiry = *fixture.Run.ClaimUntil
	}
	*fixture.Clock = latestExpiry.Add(time.Second)
	candidate, found, err := fixture.Store.
		NextPRDevelopmentControllerSuspendedResumeRecovery(ctx)
	require.NoError(t, err)
	require.True(t, found)
	lease, changed, err := fixture.Store.
		ClaimPRDevelopmentControllerSuspendedResumeRecovery(
			ctx,
			PRDevelopmentControllerSuspendedResumeRecoveryClaim{
				CaseID:           candidate.CaseID,
				SuspensionID:     candidate.SuspensionID,
				ControllerID:     candidate.ControllerID,
				AttemptID:        candidate.AttemptID,
				ExpectedRevision: candidate.ExpectedRevision,
				ClaimID: prDevelopmentSuspendedResumeIdentity(
					prDevelopmentSuspendedResumeRecoveryClaimPrefix,
					candidate.SuspensionID,
					"crash-recovery-worker",
				),
				WorkerLabel: "crash-recovery-worker",
				Lease:       time.Minute,
			},
		)
	require.NoError(t, err)
	require.True(t, changed)
	transition, changed, err := fixture.Store.
		FinalizePRDevelopmentControllerSuspendedResumeRecovery(
			ctx,
			PRDevelopmentControllerSuspendedResumeRecoveryFinalize{
				SuspensionID:     lease.Suspension.ID,
				ControllerID:     lease.Controller.ID,
				AttemptID:        lease.Suspension.ResumeAttemptID,
				ExpectedRevision: lease.Controller.Revision,
				ClaimID:          lease.Suspension.ResumeClaimID,
				ClaimToken:       lease.Suspension.ResumeClaimToken,
				ClaimEpoch:       lease.Suspension.ResumeClaimEpoch,
				Result:           suspendedResumeResultForTest(lease.Suspension),
			},
		)
	require.NoError(t, err)
	require.True(t, changed)
	*fixture.Clock = transition.NextSuspension.SuspendClaimUntil.Add(time.Second)

	childCandidate, found, err := fixture.Store.NextPRDevelopmentControllerSuspension(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, transition.NextSuspension.ID, childCandidate.SuspensionID)
	child, changed, err := fixture.Store.ClaimPRDevelopmentControllerSuspension(
		ctx,
		PRDevelopmentControllerSuspensionClaim{
			CaseID:           childCandidate.CaseID,
			SuspensionID:     childCandidate.SuspensionID,
			ControllerID:     childCandidate.ControllerID,
			AttemptID:        childCandidate.AttemptID,
			ExpectedRevision: childCandidate.ExpectedRevision,
			ClaimID:          "ordinary-suspension-reclaim",
			WorkerLabel:      "ordinary-suspension-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.True(t, child.Reclaimed)
	assert.NotEqual(t, transition.NextSuspension.SuspendClaimToken,
		child.Suspension.SuspendClaimToken)
}

func TestPRDevelopmentSuspendedResumeRecoveryTypesAreJSONPrivate(t *testing.T) {
	for _, value := range []any{
		PRDevelopmentControllerSuspendedResumeRecoveryCandidate{SuspensionID: "secret"},
		PRDevelopmentControllerSuspendedResumeRecoveryClaim{ClaimID: "secret"},
		PRDevelopmentControllerSuspendedResumeRecoveryLease{
			Suspension: PRDevelopmentControllerSuspension{ResumeReservationKey: "secret"},
		},
		PRDevelopmentControllerSuspendedResumeRecoveryRenew{ClaimToken: "secret"},
		PRDevelopmentControllerSuspendedResumeRecoveryFinalize{ClaimToken: "secret"},
		PRDevelopmentControllerSuspendedResumeRecoveryTransition{
			NextSuspension: PRDevelopmentControllerSuspension{SuspensionReservationKey: "secret"},
		},
	} {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		assert.JSONEq(t, `{}`, string(encoded))
		assert.NotContains(t, string(encoded), "secret")
	}
}
