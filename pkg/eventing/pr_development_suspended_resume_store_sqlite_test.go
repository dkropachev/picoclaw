//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type suspendedPRDevelopmentResumeFixture struct {
	Store      *Store
	Clock      *time.Time
	Case       PRDevelopmentCase
	Attempt    PRDevelopmentRepairAttempt
	Run        PRDevelopmentRepairOrchestration
	Controller PRDevelopmentController
	Suspension PRDevelopmentControllerSuspension
}

func newSuspendedPRDevelopmentResumeFixture(
	t *testing.T,
) suspendedPRDevelopmentResumeFixture {
	t.Helper()
	ctx := context.Background()
	staged := newStagedPRDevelopmentSuspensionFixture(t)
	candidate, found, err := staged.Store.NextPRDevelopmentControllerSuspension(ctx)
	require.NoError(t, err)
	require.True(t, found)
	lease, changed, err := staged.Store.ClaimPRDevelopmentControllerSuspension(
		ctx,
		PRDevelopmentControllerSuspensionClaim{
			CaseID:           candidate.CaseID,
			SuspensionID:     candidate.SuspensionID,
			ControllerID:     candidate.ControllerID,
			AttemptID:        candidate.AttemptID,
			ExpectedRevision: candidate.ExpectedRevision,
			ClaimID:          "suspended-resume-setup-claim",
			WorkerLabel:      "suspended-resume-setup-worker",
			Lease:            5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	transition, changed, err := staged.Store.FinalizePRDevelopmentControllerSuspension(
		ctx,
		PRDevelopmentControllerSuspensionFinalize{
			SuspensionID:     lease.Suspension.ID,
			ControllerID:     lease.Controller.ID,
			AttemptID:        lease.Suspension.AttemptID,
			ExpectedRevision: lease.Controller.Revision,
			ClaimID:          lease.Suspension.SuspendClaimID,
			ClaimToken:       lease.Suspension.SuspendClaimToken,
			ClaimEpoch:       lease.Suspension.SuspendClaimEpoch,
			Result:           suspensionResultForTest(lease.Suspension),
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, PRDevelopmentControllerSuspended, transition.Controller.Phase)

	workbench, err := staged.Store.GetPRDevelopmentWorkbench(ctx, staged.Case.ID)
	require.NoError(t, err)
	require.NotNil(t, workbench.RepairSession)
	workbench, admitted, err := staged.Store.AdmitPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairAdmit{
			CaseID:                      staged.Case.ID,
			ExpectedConversationVersion: workbench.Conversation.Version,
			ExpectedRepairVersion:       workbench.RepairSession.Version,
			IdempotencyKey:              "suspended-resume-next-attempt",
			AgentID:                     transition.Controller.AgentID,
			Instruction:                 "Continue from the retained local candidate.",
		},
	)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NotNil(t, workbench.RepairSession)
	attempt := workbench.RepairSession.Attempts[len(workbench.RepairSession.Attempts)-1]
	run, claimed, err := staged.Store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{
			WorkerLabel: "suspended-resume-worker",
			Lease:       5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, attempt.ID, run.AttemptID)
	run, changed, err = staged.Store.PinPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationPin{
			AttemptID:      run.AttemptID,
			ClaimToken:     run.ClaimToken,
			HeadRepository: workbench.RepairSession.HeadRepository,
			HeadRef:        workbench.RepairSession.HeadRef,
			HeadSHA:        workbench.RepairSession.HeadSHA,
			CloneURL:       workbench.RepairSession.CloneURL,
			ReviewDigest:   workbench.RepairSession.ReviewDigest,
			WorkspaceID:    workbench.RepairSession.WorkspaceID,
			SourceTree:     transition.Controller.SourceTree,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	return suspendedPRDevelopmentResumeFixture{
		Store:      staged.Store,
		Clock:      staged.Clock,
		Case:       staged.Case,
		Attempt:    attempt,
		Run:        run,
		Controller: transition.Controller,
		Suspension: transition.Suspension,
	}
}

func TestStorePRDevelopmentControllerSuspendedResumeAcquireFinalizeAndReplay(
	t *testing.T,
) {
	ctx := context.Background()
	fixture := newSuspendedPRDevelopmentResumeFixture(t)
	acquire := PRDevelopmentRepairOrchestrationControllerAcquire{
		CaseID:           fixture.Case.ID,
		AttemptID:        fixture.Attempt.ID,
		ClaimToken:       fixture.Run.ClaimToken,
		ExpectedRevision: fixture.Controller.Revision,
		WorkerLabel:      "suspended-resume-worker",
		Lease:            5 * time.Minute,
	}
	lease, changed, err := fixture.Store.AcquirePRDevelopmentRepairOrchestrationController(
		ctx,
		acquire,
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, lease.SuspendedResume)
	resume := *lease.SuspendedResume
	assert.Equal(t, PRDevelopmentControllerSuspended, lease.Controller.Phase)
	assert.Equal(t, fixture.Attempt.ID, lease.Controller.CurrentAttemptID)
	assert.Equal(t, PRDevelopmentControllerSuspensionStatusResumeClaimed, resume.Suspension.Status)
	assert.NotEmpty(t, resume.Suspension.ResumeReservationKey)
	assert.NotEqual(t, fixture.Suspension.SuspensionReservationDigest,
		resume.Suspension.ResumeReservationDigest)
	assert.Equal(t, resume.Suspension.ResumeReservationKey,
		resume.Suspension.ResumeRequest.ReservationKey)

	var rawKey string
	var requestBlob []byte
	require.NoError(t, fixture.Store.db.QueryRowContext(ctx, `
		SELECT resume_reservation_key, resume_request_json
		FROM pr_development_controller_suspensions
		WHERE id = ?`, resume.Suspension.ID).Scan(&rawKey, &requestBlob))
	assert.Equal(t, resume.Suspension.ResumeReservationKey, rawKey)
	assert.False(t, bytes.Contains(requestBlob, []byte(rawKey)))

	replayedAcquire, changed, err := fixture.Store.
		AcquirePRDevelopmentRepairOrchestrationController(ctx, acquire)
	require.NoError(t, err)
	assert.False(t, changed)
	require.NotNil(t, replayedAcquire.SuspendedResume)
	assert.Equal(t, resume.Suspension.ResumeClaimToken,
		replayedAcquire.SuspendedResume.Suspension.ResumeClaimToken)
	assert.Equal(t, resume.Suspension.ResumeReservationKey,
		replayedAcquire.SuspendedResume.Suspension.ResumeReservationKey)
	require.NoError(t, fixture.Store.RenewPRDevelopmentControllerSuspendedResume(
		ctx,
		PRDevelopmentControllerSuspendedResumeRenew{
			ControllerID:            resume.Controller.ID,
			AttemptID:               fixture.Attempt.ID,
			SuspensionID:            resume.Suspension.ID,
			OrchestrationClaimToken: fixture.Run.ClaimToken,
			ClaimID:                 resume.Suspension.ResumeClaimID,
			ClaimToken:              resume.Suspension.ResumeClaimToken,
			ClaimEpoch:              resume.Suspension.ResumeClaimEpoch,
			Lease:                   10 * time.Minute,
		},
	))
	renewedResume, found, err := loadPRDevelopmentControllerSuspensionByID(
		ctx,
		fixture.Store.db,
		resume.Suspension.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, renewedResume.ResumeClaimUntil)
	assert.False(t, renewedResume.ResumeClaimUntil.After(*fixture.Run.ClaimUntil))

	result := suspendedResumeResultForTest(resume.Suspension)
	finalize := PRDevelopmentControllerSuspendedResumeFinalize{
		ControllerID:            resume.Controller.ID,
		AttemptID:               fixture.Attempt.ID,
		SuspensionID:            resume.Suspension.ID,
		ExpectedRevision:        resume.Controller.Revision,
		OrchestrationClaimToken: fixture.Run.ClaimToken,
		ClaimID:                 resume.Suspension.ResumeClaimID,
		ClaimToken:              resume.Suspension.ResumeClaimToken,
		ClaimEpoch:              resume.Suspension.ResumeClaimEpoch,
		Result:                  result,
		Lease:                   5 * time.Minute,
	}
	mutation, changed, err := fixture.Store.FinalizePRDevelopmentControllerSuspendedResume(
		ctx,
		finalize,
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Nil(t, mutation.SuspendedResume)
	assert.Equal(t, PRDevelopmentControllerMutation, mutation.Controller.Phase)
	assert.Equal(t, fixture.Attempt.ID, mutation.Controller.CurrentAttemptID)
	assert.Equal(t, rawKey, mutation.Controller.MutationReservationKey)
	assert.NotEmpty(t, mutation.Controller.LeaseToken)
	assert.Equal(t, resume.Controller.Revision+1, mutation.Controller.Revision)

	var claimToken string
	require.NoError(t, fixture.Store.db.QueryRowContext(ctx, `
		SELECT resume_reservation_key, resume_claim_token, resume_request_json
		FROM pr_development_controller_suspensions
		WHERE id = ?`, resume.Suspension.ID).Scan(&rawKey, &claimToken, &requestBlob))
	assert.Empty(t, rawKey)
	assert.Empty(t, claimToken)
	assert.False(t, bytes.Contains(
		requestBlob,
		[]byte(resume.Suspension.ResumeReservationKey),
	))

	// A normal heartbeat may extend the installed mutation lease before an
	// exact lost-response replay reaches finalization. That extension must not
	// invalidate the immutable resume evidence.
	require.NoError(t, fixture.Store.RenewPRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerRenew{
			ControllerID: mutation.Controller.ID,
			AttemptID:    fixture.Attempt.ID,
			LeaseToken:   mutation.Controller.LeaseToken,
			LeaseEpoch:   mutation.Controller.LeaseEpoch,
			Lease:        10 * time.Minute,
		},
	))
	finalize.Result.AlreadyResumed = true
	replayed, changed, err := fixture.Store.FinalizePRDevelopmentControllerSuspendedResume(
		ctx,
		finalize,
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, mutation.Controller.ID, replayed.Controller.ID)
	assert.Equal(t, mutation.Controller.LeaseToken, replayed.Controller.LeaseToken)
	assert.True(t, replayed.Controller.LeaseUntil.After(*mutation.Controller.LeaseUntil))
	loaded, err := fixture.Store.GetPRDevelopmentControllerForCase(ctx, fixture.Case.ID)
	require.NoError(t, err)
	assert.Equal(t, replayed.Controller.ID, loaded.ID)
	assert.Equal(t, replayed.Controller.Revision, loaded.Revision)
	assert.Equal(t, replayed.Controller.Phase, loaded.Phase)
	assert.Empty(t, loaded.LeaseToken)
	assert.Empty(t, loaded.MutationReservationKey)
}

func TestStorePRDevelopmentControllerSuspendedResumeCannotOutliveOrchestration(
	t *testing.T,
) {
	ctx := context.Background()
	fixture := newSuspendedPRDevelopmentResumeFixture(t)
	acquire := PRDevelopmentRepairOrchestrationControllerAcquire{
		CaseID:           fixture.Case.ID,
		AttemptID:        fixture.Attempt.ID,
		ClaimToken:       fixture.Run.ClaimToken,
		ExpectedRevision: fixture.Controller.Revision,
		WorkerLabel:      "suspended-resume-worker",
		Lease:            5 * time.Minute,
	}
	lease, changed, err := fixture.Store.AcquirePRDevelopmentRepairOrchestrationController(
		ctx,
		acquire,
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, lease.SuspendedResume)
	resume := *lease.SuspendedResume
	require.NotNil(t, fixture.Run.ClaimUntil)
	require.NotNil(t, resume.Suspension.ResumeClaimUntil)
	assert.False(t, resume.Suspension.ResumeClaimUntil.After(*fixture.Run.ClaimUntil))

	*fixture.Clock = fixture.Run.ClaimUntil.Add(time.Second)
	err = fixture.Store.RenewPRDevelopmentControllerSuspendedResume(
		ctx,
		PRDevelopmentControllerSuspendedResumeRenew{
			ControllerID:            resume.Controller.ID,
			AttemptID:               fixture.Attempt.ID,
			SuspensionID:            resume.Suspension.ID,
			OrchestrationClaimToken: fixture.Run.ClaimToken,
			ClaimID:                 resume.Suspension.ResumeClaimID,
			ClaimToken:              resume.Suspension.ResumeClaimToken,
			ClaimEpoch:              resume.Suspension.ResumeClaimEpoch,
			Lease:                   5 * time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrStaleLease)
	_, changed, err = fixture.Store.FinalizePRDevelopmentControllerSuspendedResume(
		ctx,
		PRDevelopmentControllerSuspendedResumeFinalize{
			ControllerID:            resume.Controller.ID,
			AttemptID:               fixture.Attempt.ID,
			SuspensionID:            resume.Suspension.ID,
			ExpectedRevision:        resume.Controller.Revision,
			OrchestrationClaimToken: fixture.Run.ClaimToken,
			ClaimID:                 resume.Suspension.ResumeClaimID,
			ClaimToken:              resume.Suspension.ResumeClaimToken,
			ClaimEpoch:              resume.Suspension.ResumeClaimEpoch,
			Result:                  suspendedResumeResultForTest(resume.Suspension),
			Lease:                   5 * time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrStaleLease)
	assert.False(t, changed)

	_, claimed, err := fixture.Store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{
			WorkerLabel: "suspended-resume-worker",
			Lease:       5 * time.Minute,
		},
	)
	require.NoError(t, err)
	assert.False(t, claimed, "ordinary scheduling must yield to resume recovery")
	candidate, found, err := fixture.Store.
		NextPRDevelopmentControllerSuspendedResumeRecovery(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, resume.Suspension.ID, candidate.SuspensionID)
	assert.Equal(t, fixture.Attempt.ID, candidate.AttemptID)
	loaded, found, err := loadPRDevelopmentControllerSuspensionByID(
		ctx,
		fixture.Store.db,
		resume.Suspension.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, PRDevelopmentControllerSuspensionStatusResumeClaimed, loaded.Status)
	assert.Equal(t, resume.Suspension.ResumeReservationKey, loaded.ResumeReservationKey)
	assert.Equal(t, resume.Suspension.ResumeClaimToken, loaded.ResumeClaimToken)
}

func TestStorePRDevelopmentControllerSuspendedResumeReservesRecoveryChainSlot(
	t *testing.T,
) {
	controller := PRDevelopmentController{
		Revision: MaxPRDevelopmentControllerRevision -
			prDevelopmentControllerMutationRevisionReserve,
	}
	suspension := PRDevelopmentControllerSuspension{
		Ordinal: MaxPRDevelopmentControllerFences - 2,
	}
	require.NoError(t, requirePRDevelopmentSuspendedResumeRecoveryCapacity(
		controller,
		suspension,
	))

	suspension.Ordinal++
	err := requirePRDevelopmentSuspendedResumeRecoveryCapacity(controller, suspension)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)

	suspension.Ordinal--
	controller.Revision++
	err = requirePRDevelopmentSuspendedResumeRecoveryCapacity(controller, suspension)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)

	controller.Revision--
	controller.LeaseEpoch = int64(^uint64(0) >> 1)
	err = requirePRDevelopmentSuspendedResumeRecoveryCapacity(controller, suspension)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
}

func suspendedResumeResultForTest(
	suspension PRDevelopmentControllerSuspension,
) PRDevelopmentControllerSuspendedResumeResult {
	request := suspension.ResumeRequest
	return PRDevelopmentControllerSuspendedResumeResult{
		WorkspaceID:      request.WorkspaceID,
		Version:          request.ExpectedVersion,
		MutationEpoch:    request.ExpectedMutationEpoch,
		Tip:              request.ExpectedTip,
		Tree:             request.ExpectedTree,
		CandidateTree:    request.CandidateTree,
		CandidateDigest:  request.CandidateDigest,
		ChangedFileCount: request.ChangedFileCount,
		SuspensionHash:   request.SuspensionHash,
		RotationHash:     strings.Repeat("9", 64),
	}
}
