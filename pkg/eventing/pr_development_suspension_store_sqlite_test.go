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

type stagedPRDevelopmentSuspensionFixture struct {
	Store      *Store
	Clock      *time.Time
	Case       PRDevelopmentCase
	Attempt    PRDevelopmentRepairAttempt
	Controller PRDevelopmentController
	Suspension PRDevelopmentControllerSuspension
	Bearer     string
	Recovery   PRDevelopmentControllerRecoveryFinalize
}

func newStagedPRDevelopmentSuspensionFixture(
	t *testing.T,
) stagedPRDevelopmentSuspensionFixture {
	t.Helper()
	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(t, store, developmentCase.ID)
	bound := bindPRDevelopmentControllerForTest(t, store, developmentCase.ID, completed)
	attempt := completed.Attempts[len(completed.Attempts)-1]
	*clock = clock.Add(2 * time.Minute)
	_, _, err = store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: bound.Bound.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "suspension-expiry-worker",
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
			ClaimID:          "stage-suspension-recovery-claim",
			WorkerLabel:      "stage-suspension-recovery-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	bearer := claim.Intent.ReplacementReservationKey
	recoveryFinalize := PRDevelopmentControllerRecoveryFinalize{
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
			RotationHash:  strings.Repeat("8", 64),
		},
		Lease: time.Minute,
	}
	controller, changed, err := store.FinalizePRDevelopmentControllerRecovery(
		ctx,
		recoveryFinalize,
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, PRDevelopmentControllerSuspensionPending, controller.Phase)
	suspension, found, err := loadActivePRDevelopmentControllerSuspension(
		ctx,
		store.db,
		controller.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	return stagedPRDevelopmentSuspensionFixture{
		Store:      store,
		Clock:      clock,
		Case:       developmentCase,
		Attempt:    attempt,
		Controller: controller,
		Suspension: suspension,
		Bearer:     bearer,
		Recovery:   recoveryFinalize,
	}
}

func TestStorePRDevelopmentControllerSuspensionClaimReclaimFinalizeAndReplay(
	t *testing.T,
) {
	ctx := context.Background()
	fixture := newStagedPRDevelopmentSuspensionFixture(t)
	stagedUpdatedAt := fixture.Suspension.UpdatedAt
	replayedController, changed, err := fixture.Store.FinalizePRDevelopmentControllerRecovery(
		ctx,
		fixture.Recovery,
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, fixture.Controller, replayedController)
	replayedSuspension, found, err := loadPRDevelopmentControllerSuspensionByID(
		ctx,
		fixture.Store.db,
		fixture.Suspension.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, stagedUpdatedAt, replayedSuspension.UpdatedAt)
	candidate, found, err := fixture.Store.NextPRDevelopmentControllerSuspension(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, fixture.Case.ID, candidate.CaseID)
	assert.Equal(t, fixture.Suspension.ID, candidate.SuspensionID)
	assert.Equal(t, fixture.Controller.Revision, candidate.ExpectedRevision)

	firstInput := PRDevelopmentControllerSuspensionClaim{
		CaseID:           fixture.Case.ID,
		SuspensionID:     fixture.Suspension.ID,
		ControllerID:     fixture.Controller.ID,
		AttemptID:        fixture.Attempt.ID,
		ExpectedRevision: fixture.Controller.Revision,
		ClaimID:          "suspension-claim-first",
		WorkerLabel:      "suspension-worker-first",
		Lease:            time.Minute,
	}
	first, changed, err := fixture.Store.ClaimPRDevelopmentControllerSuspension(ctx, firstInput)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, fixture.Bearer, first.Suspension.SuspendRequest.ReservationKey)
	assert.Equal(t, fixture.Bearer, first.Suspension.SuspensionReservationKey)
	firstReplay, changed, err := fixture.Store.ClaimPRDevelopmentControllerSuspension(
		ctx,
		firstInput,
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, first.Suspension.SuspendClaimToken, firstReplay.Suspension.SuspendClaimToken)

	*fixture.Clock = first.Suspension.SuspendClaimUntil.Add(time.Second)
	candidate, found, err = fixture.Store.NextPRDevelopmentControllerSuspension(ctx)
	require.NoError(t, err)
	require.True(t, found)
	secondInput := firstInput
	secondInput.ClaimID = "suspension-claim-second"
	secondInput.WorkerLabel = "suspension-worker-second"
	second, changed, err := fixture.Store.ClaimPRDevelopmentControllerSuspension(
		ctx,
		secondInput,
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.True(t, second.Reclaimed)
	assert.Equal(t, first.Suspension.SuspendClaimEpoch+1, second.Suspension.SuspendClaimEpoch)
	assert.NotEqual(t, first.Suspension.SuspendClaimToken, second.Suspension.SuspendClaimToken)

	staleFinalize := PRDevelopmentControllerSuspensionFinalize{
		SuspensionID:     fixture.Suspension.ID,
		ControllerID:     fixture.Controller.ID,
		AttemptID:        fixture.Attempt.ID,
		ExpectedRevision: fixture.Controller.Revision,
		ClaimID:          first.Suspension.SuspendClaimID,
		ClaimToken:       first.Suspension.SuspendClaimToken,
		ClaimEpoch:       first.Suspension.SuspendClaimEpoch,
		Result:           suspensionResultForTest(fixture.Suspension),
	}
	_, changed, err = fixture.Store.FinalizePRDevelopmentControllerSuspension(
		ctx,
		staleFinalize,
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed)

	require.NoError(t, fixture.Store.RenewPRDevelopmentControllerSuspension(
		ctx,
		PRDevelopmentControllerSuspensionRenew{
			SuspensionID: fixture.Suspension.ID,
			ControllerID: fixture.Controller.ID,
			AttemptID:    fixture.Attempt.ID,
			ClaimID:      second.Suspension.SuspendClaimID,
			ClaimToken:   second.Suspension.SuspendClaimToken,
			ClaimEpoch:   second.Suspension.SuspendClaimEpoch,
			Lease:        2 * time.Minute,
		},
	))
	finalize := staleFinalize
	finalize.ClaimID = second.Suspension.SuspendClaimID
	finalize.ClaimToken = second.Suspension.SuspendClaimToken
	finalize.ClaimEpoch = second.Suspension.SuspendClaimEpoch
	transition, changed, err := fixture.Store.FinalizePRDevelopmentControllerSuspension(
		ctx,
		finalize,
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, PRDevelopmentControllerSuspended, transition.Controller.Phase)
	assert.Equal(t, PRDevelopmentControllerSuspensionStatusSuspended, transition.Suspension.Status)
	assert.Empty(t, transition.Suspension.SuspensionReservationKey)
	assert.Empty(t, transition.Suspension.SuspendRequest.ReservationKey)
	assert.Empty(t, transition.Suspension.SuspendClaimToken)
	assert.NotEmpty(t, transition.Suspension.SuspendClaimTokenDigest)
	assert.Equal(t, fixture.Suspension.SourceFinalRevision+2, transition.Controller.Revision)

	var rawKey string
	var requestBlob []byte
	require.NoError(t, fixture.Store.db.QueryRowContext(ctx, `
		SELECT suspension_reservation_key, suspend_request_json
		FROM pr_development_controller_suspensions
		WHERE id = ?`, fixture.Suspension.ID).Scan(&rawKey, &requestBlob))
	assert.Empty(t, rawKey)
	assert.False(t, bytes.Contains(requestBlob, []byte(fixture.Bearer)))

	finalize.Result.AlreadySuspended = true
	replayed, changed, err := fixture.Store.FinalizePRDevelopmentControllerSuspension(
		ctx,
		finalize,
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, transition, replayed)
	_, found, err = fixture.Store.NextPRDevelopmentControllerSuspension(ctx)
	require.NoError(t, err)
	assert.False(t, found)
}

func TestPRDevelopmentControllerSuspensionChangedFileLimitMatchesGit(t *testing.T) {
	ctx := context.Background()
	fixture := newStagedPRDevelopmentSuspensionFixture(t)
	lease, changed, err := fixture.Store.ClaimPRDevelopmentControllerSuspension(
		ctx,
		PRDevelopmentControllerSuspensionClaim{
			CaseID:           fixture.Case.ID,
			SuspensionID:     fixture.Suspension.ID,
			ControllerID:     fixture.Controller.ID,
			AttemptID:        fixture.Attempt.ID,
			ExpectedRevision: fixture.Controller.Revision,
			ClaimID:          "suspension-capacity-claim",
			WorkerLabel:      "suspension-capacity-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	result := suspensionResultForTest(fixture.Suspension)
	result.ChangedFileCount = maxPRDevelopmentSuspensionChangedFiles
	transition, changed, err := fixture.Store.FinalizePRDevelopmentControllerSuspension(
		ctx,
		PRDevelopmentControllerSuspensionFinalize{
			SuspensionID:     fixture.Suspension.ID,
			ControllerID:     fixture.Controller.ID,
			AttemptID:        fixture.Attempt.ID,
			ExpectedRevision: fixture.Controller.Revision,
			ClaimID:          lease.Suspension.SuspendClaimID,
			ClaimToken:       lease.Suspension.SuspendClaimToken,
			ClaimEpoch:       lease.Suspension.SuspendClaimEpoch,
			Result:           result,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, maxPRDevelopmentSuspensionChangedFiles,
		transition.Suspension.SuspendResult.ChangedFileCount)

	result.ChangedFileCount++
	_, err = normalizePRDevelopmentControllerSuspensionResult(
		fixture.Suspension,
		result,
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
}

func TestPRDevelopmentControllerSuspensionRequestCodecsNeverRetainRawBearers(
	t *testing.T,
) {
	sentinel := "pdck_0123456789abcdef0123456789abcdef"
	suspend := PRDevelopmentControllerSuspensionRequest{
		Repository:            "https://example.com/acme/repo.git",
		SourceRef:             "refs/heads/feature",
		SourceCommit:          strings.Repeat("1", 40),
		ReservationKey:        sentinel,
		AgentID:               "repair-agent",
		WorkspaceID:           "workspace-1",
		LineID:                "pdln_0123456789abcdef0123456789abcdef",
		IntentID:              "pdsi_0123456789abcdef0123456789abcdef",
		ExpectedMutationEpoch: 1,
		ExpectedTip:           strings.Repeat("1", 40),
		ExpectedTree:          strings.Repeat("2", 40),
	}
	encoded, _, err := encodePRDevelopmentControllerSuspensionRequest(suspend)
	require.NoError(t, err)
	assert.False(t, bytes.Contains(encoded, []byte(sentinel)))
	decoded, err := decodePRDevelopmentControllerSuspensionRequest(encoded)
	require.NoError(t, err)
	assert.Empty(t, decoded.ReservationKey)

	resume := PRDevelopmentControllerSuspendedResumeRequest{
		Repository:            suspend.Repository,
		SourceRef:             suspend.SourceRef,
		SourceCommit:          suspend.SourceCommit,
		ReservationKey:        sentinel,
		AgentID:               suspend.AgentID,
		WorkspaceID:           suspend.WorkspaceID,
		LineID:                suspend.LineID,
		IntentID:              "pdsri_0123456789abcdef0123456789abcdef",
		ExpectedMutationEpoch: 1,
		ExpectedTip:           suspend.ExpectedTip,
		ExpectedTree:          suspend.ExpectedTree,
		SuspensionHash:        strings.Repeat("3", 64),
		CandidateTree:         strings.Repeat("4", 40),
		CandidateDigest:       strings.Repeat("5", 64),
		ChangedFileCount:      1,
	}
	encoded, _, err = encodePRDevelopmentControllerSuspendedResumeRequest(resume)
	require.NoError(t, err)
	assert.False(t, bytes.Contains(encoded, []byte(sentinel)))
	decodedResume, err := decodePRDevelopmentControllerSuspendedResumeRequest(encoded)
	require.NoError(t, err)
	assert.Empty(t, decodedResume.ReservationKey)
}

func TestStorePRDevelopmentCommitRecoverySuspensionBindsPreparedEffect(t *testing.T) {
	ctx := context.Background()
	prepared := preparePRDevelopmentOperationRecoveryTarget(
		t,
		PRDevelopmentControllerOperationCommit,
	)
	recovered := recoverPreparedPRDevelopmentOperationForTest(
		t,
		prepared,
		"commit-suspension",
	)
	require.Equal(t, PRDevelopmentControllerSuspensionPending, recovered.Controller.Phase)
	suspension, found, err := loadActivePRDevelopmentControllerSuspension(
		ctx,
		prepared.Fixture.Store.db,
		recovered.Controller.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, PRDevelopmentControllerSuspensionCommitRecovery, suspension.Mode)
	assert.Equal(t, prepared.Operation.ID, suspension.SourceOperationID)
	assert.Equal(t, prepared.Operation.Request.EffectIntentID, suspension.SuspendRequest.CommitIntentID)
	assert.Equal(t, prepared.Operation.Request.ExpectedTree, suspension.SuspendRequest.CommitExpectedTree)
	claim, changed, err := prepared.Fixture.Store.ClaimPRDevelopmentControllerSuspension(
		ctx,
		PRDevelopmentControllerSuspensionClaim{
			CaseID:           prepared.Fixture.Case.ID,
			SuspensionID:     suspension.ID,
			ControllerID:     recovered.Controller.ID,
			AttemptID:        suspension.AttemptID,
			ExpectedRevision: recovered.Controller.Revision,
			ClaimID:          "commit-suspension-worker-claim",
			WorkerLabel:      "commit-suspension-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	result := PRDevelopmentControllerSuspensionResult{
		WorkspaceID:      suspension.WorkspaceID,
		Version:          suspension.LineVersion,
		MutationEpoch:    suspension.MutationEpoch,
		Tip:              suspension.TipCommit,
		Tree:             suspension.Tree,
		CandidateTree:    prepared.Operation.Request.ExpectedTree,
		CandidateDigest:  prepared.Operation.Request.CandidateDigest,
		ChangedFileCount: prepared.Result.ChangedFiles,
		SuspensionHash:   strings.Repeat("7", 64),
		PreparedCommit:   prepared.Result.Commit,
		PreparedTree:     prepared.Operation.Request.ExpectedTree,
	}
	mismatch := result
	mismatch.PreparedCommit = strings.Repeat("9", len(result.PreparedCommit))
	_, changed, err = prepared.Fixture.Store.FinalizePRDevelopmentControllerSuspension(
		ctx,
		PRDevelopmentControllerSuspensionFinalize{
			SuspensionID:     suspension.ID,
			ControllerID:     recovered.Controller.ID,
			AttemptID:        suspension.AttemptID,
			ExpectedRevision: recovered.Controller.Revision,
			ClaimID:          claim.Suspension.SuspendClaimID,
			ClaimToken:       claim.Suspension.SuspendClaimToken,
			ClaimEpoch:       claim.Suspension.SuspendClaimEpoch,
			Result:           mismatch,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed)
	transition, changed, err := prepared.Fixture.Store.FinalizePRDevelopmentControllerSuspension(
		ctx,
		PRDevelopmentControllerSuspensionFinalize{
			SuspensionID:     suspension.ID,
			ControllerID:     recovered.Controller.ID,
			AttemptID:        suspension.AttemptID,
			ExpectedRevision: recovered.Controller.Revision,
			ClaimID:          claim.Suspension.SuspendClaimID,
			ClaimToken:       claim.Suspension.SuspendClaimToken,
			ClaimEpoch:       claim.Suspension.SuspendClaimEpoch,
			Result:           result,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, PRDevelopmentControllerSuspended, transition.Controller.Phase)
	assert.Equal(t, prepared.Result.Commit, transition.Suspension.SuspendResult.PreparedCommit)
}

func TestPRDevelopmentControllerSuspensionExecutionTypesAreJSONPrivate(t *testing.T) {
	values := []any{
		PRDevelopmentControllerSuspensionWorkCandidate{SuspensionID: "secret"},
		PRDevelopmentControllerSuspensionClaim{ClaimID: "secret"},
		PRDevelopmentControllerSuspensionLease{
			Suspension: PRDevelopmentControllerSuspension{SuspensionReservationKey: "secret"},
		},
		PRDevelopmentControllerSuspensionRenew{ClaimToken: "secret"},
		PRDevelopmentControllerSuspensionFinalize{ClaimToken: "secret"},
		PRDevelopmentControllerSuspensionTransition{
			Suspension: PRDevelopmentControllerSuspension{SuspensionReservationKey: "secret"},
		},
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		assert.JSONEq(t, `{}`, string(encoded))
	}
}

func TestPRDevelopmentControllerSuspensionCodecsAreCanonicalAndIgnoreReplayMarkers(
	t *testing.T,
) {
	result := PRDevelopmentControllerSuspensionResult{
		WorkspaceID:      "workspace-1",
		Version:          2,
		MutationEpoch:    3,
		Tip:              strings.Repeat("1", 40),
		Tree:             strings.Repeat("2", 40),
		CandidateTree:    strings.Repeat("3", 40),
		CandidateDigest:  strings.Repeat("4", 64),
		ChangedFileCount: 2,
		SuspensionHash:   strings.Repeat("5", 64),
	}
	firstJSON, firstHash, err := encodePRDevelopmentControllerSuspensionResult(result)
	require.NoError(t, err)
	result.AlreadySuspended = true
	replayJSON, replayHash, err := encodePRDevelopmentControllerSuspensionResult(result)
	require.NoError(t, err)
	assert.Equal(t, firstJSON, replayJSON)
	assert.Equal(t, firstHash, replayHash)
	_, err = decodePRDevelopmentControllerSuspensionResult(
		append(bytes.Clone(firstJSON), []byte(` `)...),
	)
	assert.Error(t, err)
	unknown := bytes.Replace(
		firstJSON,
		[]byte(`"workspace_id":`),
		[]byte(`"unknown":0,"workspace_id":`),
		1,
	)
	_, err = decodePRDevelopmentControllerSuspensionResult(unknown)
	assert.Error(t, err)

	resume := PRDevelopmentControllerSuspendedResumeResult{
		WorkspaceID:      "workspace-1",
		Version:          2,
		MutationEpoch:    3,
		Tip:              strings.Repeat("1", 40),
		Tree:             strings.Repeat("2", 40),
		CandidateTree:    strings.Repeat("3", 40),
		CandidateDigest:  strings.Repeat("4", 64),
		ChangedFileCount: 2,
		SuspensionHash:   strings.Repeat("5", 64),
		RotationHash:     strings.Repeat("6", 64),
	}
	firstJSON, firstHash, err = encodePRDevelopmentControllerSuspendedResumeResult(resume)
	require.NoError(t, err)
	resume.AlreadyResumed = true
	replayJSON, replayHash, err = encodePRDevelopmentControllerSuspendedResumeResult(resume)
	require.NoError(t, err)
	assert.Equal(t, firstJSON, replayJSON)
	assert.Equal(t, firstHash, replayHash)
}

func suspensionResultForTest(
	suspension PRDevelopmentControllerSuspension,
) PRDevelopmentControllerSuspensionResult {
	return PRDevelopmentControllerSuspensionResult{
		WorkspaceID:      suspension.WorkspaceID,
		Version:          suspension.LineVersion,
		MutationEpoch:    suspension.MutationEpoch,
		Tip:              suspension.TipCommit,
		Tree:             suspension.Tree,
		CandidateTree:    suspension.Tree,
		CandidateDigest:  strings.Repeat("a", 64),
		ChangedFileCount: 1,
		SuspensionHash:   strings.Repeat("b", 64),
	}
}
