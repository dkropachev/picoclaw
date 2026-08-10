//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	operationTestSummary    = "Committed the local repair after focused CI passed."
	operationTestIterations = 1
)

type prDevelopmentOperationFixture struct {
	Store      *Store
	Clock      *time.Time
	Case       PRDevelopmentCase
	Session    PRDevelopmentRepairSession
	Attempt    PRDevelopmentRepairAttempt
	Mutation   PRDevelopmentControllerLease
	SourceTree string
	NextID     int
}

func newPRDevelopmentOperationFixture(
	t *testing.T,
	databasePath string,
) *prDevelopmentOperationFixture {
	t.Helper()
	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, databasePath)
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	session := completePRDevelopmentRepairForControllerTest(
		t,
		store,
		developmentCase.ID,
	)
	attempt := session.Attempts[len(session.Attempts)-1]
	mutation, acquired, err := store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: 0,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "operation-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	return &prDevelopmentOperationFixture{
		Store:      store,
		Clock:      clock,
		Case:       developmentCase,
		Session:    session,
		Attempt:    attempt,
		Mutation:   mutation,
		SourceTree: strings.Repeat("b", 40),
		NextID:     1,
	}
}

func newLegacyBoundPRDevelopmentOperationFixture(
	t *testing.T,
	databasePath string,
) *prDevelopmentOperationFixture {
	t.Helper()
	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, databasePath)
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	session := completePRDevelopmentRepairForControllerTest(
		t,
		store,
		developmentCase.ID,
	)
	attempt := session.Attempts[len(session.Attempts)-1]
	bound := bindPRDevelopmentControllerForTest(
		t,
		store,
		developmentCase.ID,
		session,
	)
	return &prDevelopmentOperationFixture{
		Store:      store,
		Clock:      clock,
		Case:       developmentCase,
		Session:    session,
		Attempt:    attempt,
		Mutation:   PRDevelopmentControllerLease{Controller: bound.Bound},
		SourceTree: bound.Bound.SourceTree,
		NextID:     1,
	}
}

func (fixture *prDevelopmentOperationFixture) operationID() string {
	id := fmt.Sprintf("pdop_%032x", fixture.NextID)
	fixture.NextID++
	return id
}

func operationEffectID(prefix string, ordinal int) string {
	return fmt.Sprintf("%s%032x", prefix, ordinal)
}

func operationBaseRequest(
	fixture *prDevelopmentOperationFixture,
	controller PRDevelopmentController,
) PRDevelopmentControllerOperationRequest {
	return PRDevelopmentControllerOperationRequest{
		Repository:   fixture.Session.HeadRepository,
		SourceRef:    fixture.Session.HeadRef,
		SourceCommit: fixture.Session.HeadSHA,
		AgentID:      fixture.Session.AgentID,
		WorkspaceID:  fixture.Session.WorkspaceID,
		LineID:       controller.LineID,
	}
}

func prepareOperationForTest(
	t *testing.T,
	fixture *prDevelopmentOperationFixture,
	lease PRDevelopmentControllerLease,
	kind PRDevelopmentControllerOperationKind,
	operationID string,
	request PRDevelopmentControllerOperationRequest,
) PRDevelopmentControllerOperation {
	t.Helper()
	operation, changed, err := fixture.Store.PreparePRDevelopmentControllerOperation(
		context.Background(),
		PRDevelopmentControllerOperationPrepare{
			OperationID:      operationID,
			ControllerID:     lease.Controller.ID,
			AttemptID:        lease.Controller.CurrentAttemptID,
			ExpectedRevision: lease.Controller.Revision,
			LeaseToken:       lease.Controller.LeaseToken,
			LeaseEpoch:       lease.Controller.LeaseEpoch,
			Kind:             kind,
			Request:          request,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	return operation
}

func finalizeOperationForTest(
	t *testing.T,
	fixture *prDevelopmentOperationFixture,
	lease PRDevelopmentControllerLease,
	operation PRDevelopmentControllerOperation,
	result PRDevelopmentControllerOperationResult,
) PRDevelopmentControllerOperationTransition {
	t.Helper()
	transition, changed, err := fixture.Store.FinalizePRDevelopmentControllerOperation(
		context.Background(),
		PRDevelopmentControllerOperationFinalize{
			ControllerID:     operation.ControllerID,
			AttemptID:        operation.AttemptID,
			OperationID:      operation.ID,
			ExpectedRevision: operation.PreparedControllerRevision,
			LeaseToken:       lease.Controller.LeaseToken,
			LeaseEpoch:       operation.MutationLeaseEpoch,
			Result:           result,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	return transition
}

func adoptOperationForTest(
	t *testing.T,
	fixture *prDevelopmentOperationFixture,
	lease PRDevelopmentControllerLease,
) (PRDevelopmentControllerOperation, PRDevelopmentControllerOperationTransition) {
	t.Helper()
	request := operationBaseRequest(fixture, lease.Controller)
	request.ExpectedTree = fixture.SourceTree
	operation := prepareOperationForTest(
		t,
		fixture,
		lease,
		PRDevelopmentControllerOperationAdopt,
		fixture.operationID(),
		request,
	)
	result := PRDevelopmentControllerOperationResult{
		WorkspaceID:   fixture.Session.WorkspaceID,
		Version:       0,
		MutationEpoch: 1,
		Tip:           fixture.Session.HeadSHA,
		Tree:          fixture.SourceTree,
	}
	return operation, finalizeOperationForTest(t, fixture, lease, operation, result)
}

func commitOperationForTest(
	t *testing.T,
	fixture *prDevelopmentOperationFixture,
	lease PRDevelopmentControllerLease,
	ordinal int,
) (PRDevelopmentControllerOperation, PRDevelopmentControllerOperationResult,
	PRDevelopmentControllerOperationTransition,
) {
	t.Helper()
	candidateTree := strings.Repeat("c", len(lease.Controller.TipCommit))
	request := operationBaseRequest(fixture, lease.Controller)
	request.ExpectedTree = candidateTree
	request.EffectIntentID = operationEffectID("pdcmt_", ordinal)
	request.ExpectedParent = lease.Controller.TipCommit
	request.CandidateDigest = strings.Repeat("d", 64)
	request.CommitMessage = "Apply the focused local repair"
	request.AuthoredAt = fixture.Clock.UTC().Truncate(time.Second)
	operation := prepareOperationForTest(
		t,
		fixture,
		lease,
		PRDevelopmentControllerOperationCommit,
		fixture.operationID(),
		request,
	)
	result := PRDevelopmentControllerOperationResult{
		WorkspaceID:     fixture.Session.WorkspaceID,
		Tree:            candidateTree,
		WorkspaceClean:  true,
		IntentID:        request.EffectIntentID,
		ParentCommit:    request.ExpectedParent,
		CandidateDigest: request.CandidateDigest,
		Commit:          strings.Repeat("e", len(lease.Controller.TipCommit)),
		ChangedFiles:    2,
	}
	return operation, result, finalizeOperationForTest(t, fixture, lease, operation, result)
}

func parkOperationForTest(
	t *testing.T,
	fixture *prDevelopmentOperationFixture,
	lease PRDevelopmentControllerLease,
	operations []PRDevelopmentControllerOperation,
	summary string,
	iterations int,
	ordinal int,
) (PRDevelopmentControllerOperation, PRDevelopmentControllerOperationResult,
	PRDevelopmentControllerOperationTransition,
) {
	t.Helper()
	request := operationBaseRequest(fixture, lease.Controller)
	request.EffectIntentID = operationEffectID("pdlnpark_", ordinal)
	request.ExpectedVersion = lease.Controller.LineVersion
	request.MutationEpoch = lease.Controller.MutationEpoch
	request.PreviousTip = lease.Controller.TipCommit
	request.Tip = lease.Controller.TipCommit
	request.Tree = lease.Controller.Tree
	request.NoChanges = true
	for index := len(operations) - 1; index >= 0; index-- {
		if operations[index].Kind == PRDevelopmentControllerOperationCommit {
			request.Tip = operations[index].Result.Commit
			request.Tree = operations[index].Result.Tree
			request.NoChanges = false
			break
		}
	}
	request.CompletionSummary = summary
	request.CompletionIterations = iterations
	operation := prepareOperationForTest(
		t,
		fixture,
		lease,
		PRDevelopmentControllerOperationPark,
		fixture.operationID(),
		request,
	)
	result := PRDevelopmentControllerOperationResult{
		WorkspaceID:         fixture.Session.WorkspaceID,
		Version:             request.ExpectedVersion + 1,
		MutationEpoch:       request.MutationEpoch,
		PreviousTip:         request.PreviousTip,
		Tip:                 request.Tip,
		Tree:                request.Tree,
		NoChanges:           request.NoChanges,
		WorkspaceClean:      true,
		ReviewVersion:       request.ExpectedVersion + 1,
		ReviewMutationEpoch: request.MutationEpoch,
		ReviewParkIntentID:  request.EffectIntentID,
		ReviewBaseCommit:    request.PreviousTip,
		ReviewCommit:        request.Tip,
		ReviewTree:          request.Tree,
		ReviewDigest:        strings.Repeat("f", 64),
	}
	return operation, result, finalizeOperationForTest(t, fixture, lease, operation, result)
}

func operationLeaseFromTransition(
	transition PRDevelopmentControllerOperationTransition,
) PRDevelopmentControllerLease {
	return PRDevelopmentControllerLease{Controller: transition.Controller}
}

func TestStorePRDevelopmentControllerOperationNormalLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOperationFixture(t, ":memory:")
	legacyVersion := fixture.Session.Version
	legacyAttempt := fixture.Attempt

	adoptRequest := operationBaseRequest(fixture, fixture.Mutation.Controller)
	adoptRequest.ExpectedTree = fixture.SourceTree
	adoptID := fixture.operationID()
	adopt := prepareOperationForTest(
		t,
		fixture,
		fixture.Mutation,
		PRDevelopmentControllerOperationAdopt,
		adoptID,
		adoptRequest,
	)
	replayedPrepare, changed, err := fixture.Store.PreparePRDevelopmentControllerOperation(
		ctx,
		PRDevelopmentControllerOperationPrepare{
			OperationID:      adoptID,
			ControllerID:     fixture.Mutation.Controller.ID,
			AttemptID:        fixture.Attempt.ID,
			ExpectedRevision: fixture.Mutation.Controller.Revision,
			LeaseToken:       fixture.Mutation.Controller.LeaseToken,
			LeaseEpoch:       fixture.Mutation.Controller.LeaseEpoch,
			Kind:             PRDevelopmentControllerOperationAdopt,
			Request:          adoptRequest,
		},
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, adopt, replayedPrepare)

	changedRequest := adoptRequest
	changedRequest.ExpectedTree = strings.Repeat("9", 40)
	_, changed, err = fixture.Store.PreparePRDevelopmentControllerOperation(
		ctx,
		PRDevelopmentControllerOperationPrepare{
			OperationID:      adoptID,
			ControllerID:     fixture.Mutation.Controller.ID,
			AttemptID:        fixture.Attempt.ID,
			ExpectedRevision: fixture.Mutation.Controller.Revision,
			LeaseToken:       fixture.Mutation.Controller.LeaseToken,
			LeaseEpoch:       fixture.Mutation.Controller.LeaseEpoch,
			Kind:             PRDevelopmentControllerOperationAdopt,
			Request:          changedRequest,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed)

	_, changed, err = fixture.Store.BindPRDevelopmentControllerLine(
		ctx,
		PRDevelopmentControllerLineBind{
			ControllerID:     fixture.Mutation.Controller.ID,
			AttemptID:        fixture.Attempt.ID,
			ExpectedRevision: fixture.Mutation.Controller.Revision,
			LeaseToken:       fixture.Mutation.Controller.LeaseToken,
			LeaseEpoch:       fixture.Mutation.Controller.LeaseEpoch,
			WorkspaceID:      fixture.Session.WorkspaceID,
			SourceCloneURL:   fixture.Session.CloneURL,
			SourceRef:        fixture.Session.HeadRef,
			SourceCommit:     fixture.Session.HeadSHA,
			SourceTree:       fixture.SourceTree,
			LineVersion:      0,
			MutationEpoch:    1,
			TipCommit:        fixture.Session.HeadSHA,
			Tree:             fixture.SourceTree,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerActive)
	assert.False(t, changed)

	adoptResult := PRDevelopmentControllerOperationResult{
		WorkspaceID:   fixture.Session.WorkspaceID,
		Version:       0,
		MutationEpoch: 1,
		Tip:           fixture.Session.HeadSHA,
		Tree:          fixture.SourceTree,
	}
	adoptTransition := finalizeOperationForTest(
		t,
		fixture,
		fixture.Mutation,
		adopt,
		adoptResult,
	)
	assert.Equal(t, PRDevelopmentControllerOperationFinalized, adoptTransition.Operation.Status)
	assert.Equal(t, int64(1), adoptTransition.Controller.MutationEpoch)
	assert.Equal(t, fixture.SourceTree, adoptTransition.Controller.Tree)

	replayResult := adoptResult
	replayResult.AlreadyOwned = true
	replayedAdopt, changed, err := fixture.Store.FinalizePRDevelopmentControllerOperation(
		ctx,
		PRDevelopmentControllerOperationFinalize{
			ControllerID:     adopt.ControllerID,
			AttemptID:        adopt.AttemptID,
			OperationID:      adopt.ID,
			ExpectedRevision: adopt.PreparedControllerRevision,
			LeaseToken:       fixture.Mutation.Controller.LeaseToken,
			LeaseEpoch:       adopt.MutationLeaseEpoch,
			Result:           replayResult,
		},
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, adoptTransition.Operation, replayedAdopt.Operation)

	boundLease := operationLeaseFromTransition(adoptTransition)
	commit, commitResult, commitTransition := commitOperationForTest(
		t,
		fixture,
		boundLease,
		1,
	)
	assert.Equal(t, boundLease.Controller.Revision, commitTransition.Controller.Revision)
	changedCommit := commitResult
	changedCommit.Commit = strings.Repeat("1", 40)
	_, changed, err = fixture.Store.FinalizePRDevelopmentControllerOperation(
		ctx,
		PRDevelopmentControllerOperationFinalize{
			ControllerID:     commit.ControllerID,
			AttemptID:        commit.AttemptID,
			OperationID:      commit.ID,
			ExpectedRevision: commit.PreparedControllerRevision,
			LeaseToken:       boundLease.Controller.LeaseToken,
			LeaseEpoch:       commit.MutationLeaseEpoch,
			Result:           changedCommit,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed)

	parkRequest := operationBaseRequest(fixture, commitTransition.Controller)
	parkRequest.EffectIntentID = operationEffectID("pdlnpark_", 1)
	parkRequest.ExpectedVersion = commitTransition.Controller.LineVersion
	parkRequest.MutationEpoch = commitTransition.Controller.MutationEpoch
	parkRequest.PreviousTip = commitTransition.Controller.TipCommit
	parkRequest.Tip = commitResult.Commit
	parkRequest.Tree = commitResult.Tree
	parkRequest.CompletionSummary = legacyAttempt.Summary
	parkRequest.CompletionIterations = legacyAttempt.Iterations
	park := prepareOperationForTest(
		t,
		fixture,
		boundLease,
		PRDevelopmentControllerOperationPark,
		fixture.operationID(),
		parkRequest,
	)
	_, changed, err = fixture.Store.RecordPRDevelopmentAttemptReviewFence(
		ctx,
		PRDevelopmentAttemptReviewFenceRecord{
			ControllerID:     park.ControllerID,
			AttemptID:        park.AttemptID,
			ExpectedRevision: park.PreparedControllerRevision,
			LeaseToken:       boundLease.Controller.LeaseToken,
			LeaseEpoch:       park.MutationLeaseEpoch,
			LineVersion:      1,
			MutationEpoch:    1,
			ParkIntentID:     parkRequest.EffectIntentID,
			BaseCommit:       parkRequest.PreviousTip,
			TipCommit:        parkRequest.Tip,
			Tree:             parkRequest.Tree,
			LineReviewDigest: strings.Repeat("f", 64),
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerActive)
	assert.False(t, changed)

	parkResult := PRDevelopmentControllerOperationResult{
		WorkspaceID:         fixture.Session.WorkspaceID,
		Version:             1,
		MutationEpoch:       1,
		PreviousTip:         parkRequest.PreviousTip,
		Tip:                 parkRequest.Tip,
		Tree:                parkRequest.Tree,
		WorkspaceClean:      true,
		ReviewVersion:       1,
		ReviewMutationEpoch: 1,
		ReviewParkIntentID:  parkRequest.EffectIntentID,
		ReviewBaseCommit:    parkRequest.PreviousTip,
		ReviewCommit:        parkRequest.Tip,
		ReviewTree:          parkRequest.Tree,
		ReviewDigest:        strings.Repeat("f", 64),
	}
	parkTransition := finalizeOperationForTest(
		t,
		fixture,
		boundLease,
		park,
		parkResult,
	)
	require.NotNil(t, parkTransition.Fence)
	assert.Equal(t, PRDevelopmentControllerReviewPending, parkTransition.Controller.Phase)
	assert.Empty(t, parkTransition.Controller.MutationReservationKey)
	assert.Equal(t, commitResult.Commit, parkTransition.Fence.TipCommit)

	replayedPark, changed, err := fixture.Store.FinalizePRDevelopmentControllerOperation(
		ctx,
		PRDevelopmentControllerOperationFinalize{
			ControllerID:     park.ControllerID,
			AttemptID:        park.AttemptID,
			OperationID:      park.ID,
			ExpectedRevision: park.PreparedControllerRevision,
			LeaseToken:       boundLease.Controller.LeaseToken,
			LeaseEpoch:       park.MutationLeaseEpoch,
			Result:           parkResult,
		},
	)
	require.NoError(t, err)
	assert.False(t, changed)
	require.NotNil(t, replayedPark.Fence)
	assert.Equal(t, parkTransition.Fence.FenceHash, replayedPark.Fence.FenceHash)

	workbench, err := fixture.Store.GetPRDevelopmentWorkbench(ctx, fixture.Case.ID)
	require.NoError(t, err)
	require.NotNil(t, workbench.RepairSession)
	assert.Equal(t, legacyVersion, workbench.RepairSession.Version)
	terminalAttempt := workbench.RepairSession.Attempts[len(workbench.RepairSession.Attempts)-1]
	assert.Equal(t, legacyAttempt, terminalAttempt)

	encoded, err := json.Marshal(parkTransition)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(encoded))
	var requestJSON, resultJSON string
	require.NoError(t, fixture.Store.db.QueryRowContext(ctx, `
		SELECT request_json, result_json
		FROM pr_development_controller_operation_intents
		WHERE id = ?`, park.ID).Scan(&requestJSON, &resultJSON))
	for _, secret := range []string{
		fixture.Mutation.Controller.LeaseToken,
		fixture.Mutation.Controller.MutationReservationKey,
	} {
		assert.NotContains(t, requestJSON, secret)
		assert.NotContains(t, resultJSON, secret)
	}
}

func finishOperationReviewForTest(
	t *testing.T,
	fixture *prDevelopmentOperationFixture,
	parked PRDevelopmentControllerOperationTransition,
) PRDevelopmentController {
	t.Helper()
	ctx := context.Background()
	review, acquired, err := fixture.Store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           fixture.Case.ID,
			AttemptID:        parked.Controller.CurrentAttemptID,
			ExpectedRevision: parked.Controller.Revision,
			Kind:             PRDevelopmentControllerReviewLease,
			WorkerLabel:      "operation-reviewer",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	ready, changed, err := fixture.Store.FinishPRDevelopmentControllerReview(
		ctx,
		PRDevelopmentControllerReviewTransition{
			ControllerID:     review.Controller.ID,
			AttemptID:        review.Controller.CurrentAttemptID,
			ExpectedRevision: review.Controller.Revision,
			LeaseToken:       review.Controller.LeaseToken,
			LeaseEpoch:       review.Controller.LeaseEpoch,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	return ready
}

func TestStorePRDevelopmentControllerOperationQueuedResumeNoChangeParkIsAtomic(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOperationFixture(t, ":memory:")
	_, adopted := adoptOperationForTest(t, fixture, fixture.Mutation)
	boundLease := operationLeaseFromTransition(adopted)
	_, _, firstPark := parkOperationForTest(
		t,
		fixture,
		boundLease,
		nil,
		fixture.Attempt.Summary,
		fixture.Attempt.Iterations,
		1,
	)
	ready := finishOperationReviewForTest(t, fixture, firstPark)

	beforeAdmission, err := fixture.Store.GetPRDevelopmentWorkbench(ctx, fixture.Case.ID)
	require.NoError(t, err)
	require.NotNil(t, beforeAdmission.RepairSession)
	next, admitted, err := fixture.Store.AdmitPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairAdmit{
			CaseID:                      fixture.Case.ID,
			ExpectedConversationVersion: 0,
			ExpectedRepairVersion:       beforeAdmission.RepairSession.Version,
			IdempotencyKey:              "operation-queued-attempt-2",
			AgentID:                     fixture.Session.AgentID,
			Instruction:                 "Verify the next review and record a no-change attempt.",
		},
	)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NotNil(t, next.RepairSession)
	queuedSessionVersion := next.RepairSession.Version
	queuedAttempt := next.RepairSession.Attempts[len(next.RepairSession.Attempts)-1]
	assert.Equal(t, PRDevelopmentRepairQueued, queuedAttempt.Status)
	assert.Zero(t, queuedAttempt.Claims)

	mutation, acquired, err := fixture.Store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           fixture.Case.ID,
			AttemptID:        queuedAttempt.ID,
			ExpectedRevision: ready.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "operation-worker-2",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	assert.Equal(t, mutation.Controller.LineVersion, mutation.Controller.MutationEpoch)

	resumeRequest := operationBaseRequest(fixture, mutation.Controller)
	resumeRequest.ExpectedVersion = mutation.Controller.LineVersion
	resumeRequest.ExpectedEpoch = mutation.Controller.MutationEpoch
	resumeRequest.ExpectedTip = mutation.Controller.TipCommit
	resumeRequest.ExpectedTree = mutation.Controller.Tree
	resume := prepareOperationForTest(
		t,
		fixture,
		mutation,
		PRDevelopmentControllerOperationResume,
		fixture.operationID(),
		resumeRequest,
	)
	resumeResult := PRDevelopmentControllerOperationResult{
		WorkspaceID:   fixture.Session.WorkspaceID,
		Version:       mutation.Controller.LineVersion,
		MutationEpoch: mutation.Controller.MutationEpoch + 1,
		Tip:           mutation.Controller.TipCommit,
		Tree:          mutation.Controller.Tree,
	}
	resumed := finalizeOperationForTest(t, fixture, mutation, resume, resumeResult)
	assert.Equal(t, resumed.Controller.LineVersion+1, resumed.Controller.MutationEpoch)

	resumedLease := operationLeaseFromTransition(resumed)
	park, parkResult, parked := parkOperationForTest(
		t,
		fixture,
		resumedLease,
		nil,
		"No code changes were needed after focused local validation.",
		2,
		2,
	)
	require.NotNil(t, parked.Fence)
	assert.True(t, parked.Fence.NoChanges)
	assert.Equal(t, mutation.Controller.Tree, parked.Fence.Tree)
	assert.Equal(t, PRDevelopmentControllerReviewPending, parked.Controller.Phase)

	after, err := fixture.Store.GetPRDevelopmentWorkbench(ctx, fixture.Case.ID)
	require.NoError(t, err)
	require.NotNil(t, after.RepairSession)
	assert.Equal(t, queuedSessionVersion+1, after.RepairSession.Version)
	completedAttempt := after.RepairSession.Attempts[len(after.RepairSession.Attempts)-1]
	assert.Equal(t, queuedAttempt.ID, completedAttempt.ID)
	assert.Equal(t, PRDevelopmentRepairCompleted, completedAttempt.Status)
	assert.Equal(t, 1, completedAttempt.Claims)
	assert.Equal(t, park.Request.CompletionSummary, completedAttempt.Summary)
	assert.Equal(t, park.Request.CompletionIterations, completedAttempt.Iterations)

	_, changed, err := fixture.Store.FinalizePRDevelopmentControllerOperation(
		ctx,
		PRDevelopmentControllerOperationFinalize{
			ControllerID:     park.ControllerID,
			AttemptID:        park.AttemptID,
			OperationID:      park.ID,
			ExpectedRevision: park.PreparedControllerRevision,
			LeaseToken:       resumedLease.Controller.LeaseToken,
			LeaseEpoch:       park.MutationLeaseEpoch,
			Result:           parkResult,
		},
	)
	require.NoError(t, err)
	assert.False(t, changed)
	replayed, err := fixture.Store.GetPRDevelopmentWorkbench(ctx, fixture.Case.ID)
	require.NoError(t, err)
	require.NotNil(t, replayed.RepairSession)
	assert.Equal(t, after.RepairSession.Version, replayed.RepairSession.Version)
	assert.Equal(t, completedAttempt, replayed.RepairSession.Attempts[len(replayed.RepairSession.Attempts)-1])
}

type preparedPRDevelopmentOperationRecovery struct {
	Fixture   *prDevelopmentOperationFixture
	Lease     PRDevelopmentControllerLease
	Operation PRDevelopmentControllerOperation
	Result    PRDevelopmentControllerOperationResult
}

func preparePRDevelopmentOperationRecoveryTarget(
	t *testing.T,
	kind PRDevelopmentControllerOperationKind,
) preparedPRDevelopmentOperationRecovery {
	t.Helper()
	ctx := context.Background()
	fixture := newPRDevelopmentOperationFixture(t, ":memory:")
	switch kind {
	case PRDevelopmentControllerOperationAdopt:
		request := operationBaseRequest(fixture, fixture.Mutation.Controller)
		request.ExpectedTree = fixture.SourceTree
		operation := prepareOperationForTest(
			t,
			fixture,
			fixture.Mutation,
			kind,
			fixture.operationID(),
			request,
		)
		return preparedPRDevelopmentOperationRecovery{
			Fixture:   fixture,
			Lease:     fixture.Mutation,
			Operation: operation,
			Result: PRDevelopmentControllerOperationResult{
				WorkspaceID:   fixture.Session.WorkspaceID,
				Version:       0,
				MutationEpoch: 1,
				Tip:           fixture.Session.HeadSHA,
				Tree:          fixture.SourceTree,
			},
		}
	case PRDevelopmentControllerOperationCommit:
		_, adopted := adoptOperationForTest(t, fixture, fixture.Mutation)
		lease := operationLeaseFromTransition(adopted)
		candidateTree := strings.Repeat("c", len(lease.Controller.TipCommit))
		request := operationBaseRequest(fixture, lease.Controller)
		request.ExpectedTree = candidateTree
		request.EffectIntentID = operationEffectID("pdcmt_", 11)
		request.ExpectedParent = lease.Controller.TipCommit
		request.CandidateDigest = strings.Repeat("d", 64)
		request.CommitMessage = "Commit an exactly recovered local candidate"
		request.AuthoredAt = fixture.Clock.UTC().Truncate(time.Second)
		operation := prepareOperationForTest(
			t,
			fixture,
			lease,
			kind,
			fixture.operationID(),
			request,
		)
		return preparedPRDevelopmentOperationRecovery{
			Fixture:   fixture,
			Lease:     lease,
			Operation: operation,
			Result: PRDevelopmentControllerOperationResult{
				WorkspaceID:     fixture.Session.WorkspaceID,
				Tree:            candidateTree,
				WorkspaceClean:  true,
				IntentID:        request.EffectIntentID,
				ParentCommit:    request.ExpectedParent,
				CandidateDigest: request.CandidateDigest,
				Commit:          strings.Repeat("e", len(lease.Controller.TipCommit)),
				ChangedFiles:    1,
			},
		}
	case PRDevelopmentControllerOperationPark:
		_, adopted := adoptOperationForTest(t, fixture, fixture.Mutation)
		lease := operationLeaseFromTransition(adopted)
		request := operationBaseRequest(fixture, lease.Controller)
		request.EffectIntentID = operationEffectID("pdlnpark_", 12)
		request.ExpectedVersion = lease.Controller.LineVersion
		request.MutationEpoch = lease.Controller.MutationEpoch
		request.PreviousTip = lease.Controller.TipCommit
		request.Tip = lease.Controller.TipCommit
		request.Tree = lease.Controller.Tree
		request.NoChanges = true
		request.CompletionSummary = fixture.Attempt.Summary
		request.CompletionIterations = fixture.Attempt.Iterations
		operation := prepareOperationForTest(
			t,
			fixture,
			lease,
			kind,
			fixture.operationID(),
			request,
		)
		return preparedPRDevelopmentOperationRecovery{
			Fixture:   fixture,
			Lease:     lease,
			Operation: operation,
			Result: PRDevelopmentControllerOperationResult{
				WorkspaceID:         fixture.Session.WorkspaceID,
				Version:             request.ExpectedVersion + 1,
				MutationEpoch:       request.MutationEpoch,
				PreviousTip:         request.PreviousTip,
				Tip:                 request.Tip,
				Tree:                request.Tree,
				NoChanges:           true,
				WorkspaceClean:      true,
				ReviewVersion:       request.ExpectedVersion + 1,
				ReviewMutationEpoch: request.MutationEpoch,
				ReviewParkIntentID:  request.EffectIntentID,
				ReviewBaseCommit:    request.PreviousTip,
				ReviewCommit:        request.Tip,
				ReviewTree:          request.Tree,
				ReviewDigest:        strings.Repeat("f", 64),
			},
		}
	case PRDevelopmentControllerOperationResume:
		_, adopted := adoptOperationForTest(t, fixture, fixture.Mutation)
		firstLease := operationLeaseFromTransition(adopted)
		_, _, parked := parkOperationForTest(
			t,
			fixture,
			firstLease,
			nil,
			fixture.Attempt.Summary,
			fixture.Attempt.Iterations,
			13,
		)
		ready := finishOperationReviewForTest(t, fixture, parked)
		workbench, err := fixture.Store.GetPRDevelopmentWorkbench(ctx, fixture.Case.ID)
		require.NoError(t, err)
		require.NotNil(t, workbench.RepairSession)
		next, admitted, err := fixture.Store.AdmitPRDevelopmentRepair(
			ctx,
			PRDevelopmentRepairAdmit{
				CaseID:                      fixture.Case.ID,
				ExpectedConversationVersion: 0,
				ExpectedRepairVersion:       workbench.RepairSession.Version,
				IdempotencyKey:              "operation-resume-recovery-attempt",
				AgentID:                     fixture.Session.AgentID,
				Instruction:                 "Resume the retained line under exact recovery.",
			},
		)
		require.NoError(t, err)
		require.True(t, admitted)
		require.NotNil(t, next.RepairSession)
		fixture.Session = *next.RepairSession
		fixture.Attempt = fixture.Session.Attempts[len(fixture.Session.Attempts)-1]
		lease, acquired, err := fixture.Store.AcquirePRDevelopmentControllerLease(
			ctx,
			PRDevelopmentControllerAcquire{
				CaseID:           fixture.Case.ID,
				AttemptID:        fixture.Attempt.ID,
				ExpectedRevision: ready.Revision,
				Kind:             PRDevelopmentControllerMutationLease,
				WorkerLabel:      "operation-resume-recovery-worker",
				Lease:            time.Minute,
			},
		)
		require.NoError(t, err)
		require.True(t, acquired)
		request := operationBaseRequest(fixture, lease.Controller)
		request.ExpectedVersion = lease.Controller.LineVersion
		request.ExpectedEpoch = lease.Controller.MutationEpoch
		request.ExpectedTip = lease.Controller.TipCommit
		request.ExpectedTree = lease.Controller.Tree
		operation := prepareOperationForTest(
			t,
			fixture,
			lease,
			kind,
			fixture.operationID(),
			request,
		)
		return preparedPRDevelopmentOperationRecovery{
			Fixture:   fixture,
			Lease:     lease,
			Operation: operation,
			Result: PRDevelopmentControllerOperationResult{
				WorkspaceID:   fixture.Session.WorkspaceID,
				Version:       lease.Controller.LineVersion,
				MutationEpoch: lease.Controller.MutationEpoch + 1,
				Tip:           lease.Controller.TipCommit,
				Tree:          lease.Controller.Tree,
			},
		}
	default:
		t.Fatalf("unsupported recovery target %q", kind)
		return preparedPRDevelopmentOperationRecovery{}
	}
}

func stagePRDevelopmentOperationExpiryForTest(
	t *testing.T,
	prepared preparedPRDevelopmentOperationRecovery,
) PRDevelopmentController {
	t.Helper()
	require.NotNil(t, prepared.Lease.Controller.LeaseUntil)
	*prepared.Fixture.Clock = prepared.Lease.Controller.LeaseUntil.Add(time.Second)
	_, changed, err := prepared.Fixture.Store.FinalizePRDevelopmentControllerOperation(
		context.Background(),
		PRDevelopmentControllerOperationFinalize{
			ControllerID:     prepared.Operation.ControllerID,
			AttemptID:        prepared.Operation.AttemptID,
			OperationID:      prepared.Operation.ID,
			ExpectedRevision: prepared.Operation.PreparedControllerRevision,
			LeaseToken:       prepared.Lease.Controller.LeaseToken,
			LeaseEpoch:       prepared.Operation.MutationLeaseEpoch,
			Result:           prepared.Result,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	assert.False(t, changed)
	controller, loadErr := prepared.Fixture.Store.GetPRDevelopmentControllerForCase(
		context.Background(),
		prepared.Fixture.Case.ID,
	)
	require.NoError(t, loadErr)
	assert.Equal(t, PRDevelopmentControllerRecoveryRequired, controller.Phase)
	assert.Empty(t, controller.MutationReservationKey)
	return controller
}

func recoveryRotationForOperationTest(
	prepared preparedPRDevelopmentOperationRecovery,
	alreadyRotated bool,
) PRDevelopmentControllerRecoveryRotationResult {
	if prepared.Operation.Kind == PRDevelopmentControllerOperationPark {
		return PRDevelopmentControllerRecoveryRotationResult{}
	}
	rotation := PRDevelopmentControllerRecoveryRotationResult{
		WorkspaceID:    prepared.Operation.WorkspaceID,
		Bound:          true,
		RotationHash:   strings.Repeat("8", 64),
		AlreadyRotated: alreadyRotated,
	}
	switch prepared.Operation.Kind {
	case PRDevelopmentControllerOperationAdopt,
		PRDevelopmentControllerOperationResume:
		rotation.Version = prepared.Result.Version
		rotation.MutationEpoch = prepared.Result.MutationEpoch
		rotation.Tip = prepared.Result.Tip
		rotation.Tree = prepared.Result.Tree
	case PRDevelopmentControllerOperationCommit:
		rotation.Version = prepared.Operation.LineVersion
		rotation.MutationEpoch = prepared.Operation.MutationEpoch
		rotation.Tip = prepared.Operation.TipCommit
		rotation.Tree = prepared.Operation.Tree
	}
	return rotation
}

func recoverPreparedPRDevelopmentOperationForTest(
	t *testing.T,
	prepared preparedPRDevelopmentOperationRecovery,
	marker string,
) PRDevelopmentControllerOperationTransition {
	t.Helper()
	ctx := context.Background()
	controller := stagePRDevelopmentOperationExpiryForTest(t, prepared)
	claimed, changed, err := prepared.Fixture.Store.ClaimPRDevelopmentControllerOperationRecovery(
		ctx,
		PRDevelopmentControllerOperationRecoveryClaim{
			CaseID:           prepared.Fixture.Case.ID,
			AttemptID:        prepared.Operation.AttemptID,
			OperationID:      prepared.Operation.ID,
			ExpectedRevision: controller.Revision,
			ClaimID:          marker + "-claim",
			WorkerLabel:      marker + "-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	finalize := PRDevelopmentControllerOperationRecoveryFinalize{
		ControllerID:     claimed.Controller.ID,
		AttemptID:        claimed.Operation.AttemptID,
		OperationID:      claimed.Operation.ID,
		RecoveryID:       claimed.Operation.RecoveryID,
		ExpectedRevision: claimed.Controller.Revision,
		ClaimID:          claimed.Operation.ClaimID,
		ClaimToken:       claimed.Operation.ClaimToken,
		ClaimEpoch:       claimed.Operation.ClaimEpoch,
		Rotation:         recoveryRotationForOperationTest(prepared, true),
		Result:           prepared.Result,
	}
	if prepared.Operation.Kind != PRDevelopmentControllerOperationPark {
		finalize.Lease = time.Minute
	}
	transition, changed, err := prepared.Fixture.Store.FinalizePRDevelopmentControllerOperationRecovery(
		ctx,
		finalize,
	)
	require.NoError(t, err)
	require.True(t, changed)
	return transition
}

func TestStorePRDevelopmentControllerOperationRecoveryTerminatesAtSuspension(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	preparedAdopt := preparePRDevelopmentOperationRecoveryTarget(
		t,
		PRDevelopmentControllerOperationAdopt,
	)
	adopted := recoverPreparedPRDevelopmentOperationForTest(
		t,
		preparedAdopt,
		"same-attempt-adopt-recovery",
	)
	fixture := preparedAdopt.Fixture
	assert.Equal(t, PRDevelopmentControllerSuspensionPending, adopted.Controller.Phase)
	assert.Empty(t, adopted.Controller.MutationReservationKey)
	assert.Empty(t, adopted.Controller.LeaseToken)
	suspension, found, err := loadPRDevelopmentControllerSuspensionBySource(
		ctx,
		fixture.Store.db,
		PRDevelopmentControllerSuspensionSourceOperationRecovery,
		adopted.Operation.RecoveryID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, adopted.Operation.ID, suspension.SourceOperationID)
	assert.Equal(t, PRDevelopmentControllerSuspensionStatusSuspendPending, suspension.Status)

	intents, err := loadPRDevelopmentRecoveryIntents(
		ctx,
		fixture.Store.db,
		adopted.Controller.ID,
	)
	require.NoError(t, err)
	require.Len(t, intents, 1)
	assert.Equal(t, preparedAdopt.Operation.AttemptID, intents[0].AttemptID)
	assert.Equal(t, adopted.Operation.RecoveryID, intents[0].ID)
	_, err = fixture.Store.GetPRDevelopmentControllerForCase(ctx, fixture.Case.ID)
	require.NoError(t, err)
}

func TestStorePRDevelopmentControllerLegacyUnboundRecoveryPrecedesRecoveredAdoptOperation(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOperationFixture(t, ":memory:")
	require.NotNil(t, fixture.Mutation.Controller.LeaseUntil)
	*fixture.Clock = fixture.Mutation.Controller.LeaseUntil.Add(time.Second)
	_, _, err := fixture.Store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           fixture.Case.ID,
			AttemptID:        fixture.Attempt.ID,
			ExpectedRevision: fixture.Mutation.Controller.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "legacy-unbound-replacement",
			Lease:            time.Minute,
		},
	)
	require.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	legacyRecovery, err := fixture.Store.GetPRDevelopmentControllerForCase(ctx, fixture.Case.ID)
	require.NoError(t, err)
	legacyClaim, changed, err := fixture.Store.ClaimPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryClaim{
			CaseID:           fixture.Case.ID,
			AttemptID:        fixture.Attempt.ID,
			ExpectedRevision: legacyRecovery.Revision,
			ClaimID:          "legacy-unbound-before-operation-claim",
			WorkerLabel:      "legacy-unbound-before-operation-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, PRDevelopmentControllerRecoveryUnbound, legacyClaim.Intent.Mode)
	legacyRecovered, changed, err := fixture.Store.FinalizePRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryFinalize{
			ControllerID:     legacyClaim.Controller.ID,
			AttemptID:        legacyClaim.Intent.AttemptID,
			RecoveryID:       legacyClaim.Intent.ID,
			ExpectedRevision: legacyClaim.Controller.Revision,
			ClaimID:          legacyClaim.Intent.ClaimID,
			ClaimToken:       legacyClaim.Intent.ClaimToken,
			ClaimEpoch:       legacyClaim.Intent.ClaimEpoch,
			Rotation: PRDevelopmentControllerRecoveryRotationResult{
				WorkspaceID:  fixture.Session.WorkspaceID,
				RotationHash: strings.Repeat("6", 64),
			},
			Lease: time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Empty(t, legacyRecovered.WorkspaceID)

	adoptLease := PRDevelopmentControllerLease{Controller: legacyRecovered}
	request := operationBaseRequest(fixture, legacyRecovered)
	request.ExpectedTree = fixture.SourceTree
	adopt := prepareOperationForTest(
		t,
		fixture,
		adoptLease,
		PRDevelopmentControllerOperationAdopt,
		fixture.operationID(),
		request,
	)
	adoptResult := PRDevelopmentControllerOperationResult{
		WorkspaceID:   fixture.Session.WorkspaceID,
		Version:       0,
		MutationEpoch: 1,
		Tip:           fixture.Session.HeadSHA,
		Tree:          fixture.SourceTree,
	}
	adopted := recoverPreparedPRDevelopmentOperationForTest(
		t,
		preparedPRDevelopmentOperationRecovery{
			Fixture:   fixture,
			Lease:     adoptLease,
			Operation: adopt,
			Result:    adoptResult,
		},
		"adopt-after-legacy-unbound-recovery",
	)
	assert.Equal(t, fixture.Session.WorkspaceID, adopted.Controller.WorkspaceID)
	intents, err := loadPRDevelopmentRecoveryIntents(ctx, fixture.Store.db, adopted.Controller.ID)
	require.NoError(t, err)
	require.Len(t, intents, 2)
	assert.Equal(t, PRDevelopmentControllerRecoveryUnbound, intents[0].Mode)
	assert.Equal(t, PRDevelopmentControllerRecoveryBound, intents[1].Mode)
	assert.Equal(t, intents[0].ReplacementReservationDigest, intents[1].PreviousReservationDigest)
	_, err = fixture.Store.GetPRDevelopmentControllerForCase(ctx, fixture.Case.ID)
	require.NoError(t, err)
}

func TestStorePRDevelopmentControllerOperationContinuesFromLegacyBoundEpoch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("Commit then Park", func(t *testing.T) {
		t.Parallel()
		fixture := newLegacyBoundPRDevelopmentOperationFixture(t, ":memory:")
		commit, _, committed := commitOperationForTest(
			t,
			fixture,
			fixture.Mutation,
			301,
		)
		assert.Zero(t, commit.Ordinal)

		_, changed, err := fixture.Store.BindPRDevelopmentControllerLine(
			ctx,
			PRDevelopmentControllerLineBind{
				ControllerID:     committed.Controller.ID,
				AttemptID:        fixture.Attempt.ID,
				ExpectedRevision: committed.Controller.Revision,
				LeaseToken:       committed.Controller.LeaseToken,
				LeaseEpoch:       committed.Controller.LeaseEpoch,
				WorkspaceID:      committed.Controller.WorkspaceID,
				SourceCloneURL:   committed.Controller.SourceCloneURL,
				SourceRef:        committed.Controller.SourceRef,
				SourceCommit:     committed.Controller.SourceCommit,
				SourceTree:       committed.Controller.SourceTree,
				LineVersion:      committed.Controller.LineVersion,
				MutationEpoch:    committed.Controller.MutationEpoch,
				TipCommit:        committed.Controller.TipCommit,
				Tree:             committed.Controller.Tree,
			},
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
		assert.False(t, changed)

		_, changed, err = fixture.Store.RecordPRDevelopmentAttemptReviewFence(
			ctx,
			PRDevelopmentAttemptReviewFenceRecord{
				ControllerID:     committed.Controller.ID,
				AttemptID:        fixture.Attempt.ID,
				ExpectedRevision: committed.Controller.Revision,
				LeaseToken:       committed.Controller.LeaseToken,
				LeaseEpoch:       committed.Controller.LeaseEpoch,
				LineVersion:      committed.Controller.LineVersion + 1,
				MutationEpoch:    committed.Controller.MutationEpoch,
				ParkIntentID:     "legacy-fence-after-finalized-v13-commit",
				BaseCommit:       committed.Controller.TipCommit,
				TipCommit:        committed.Operation.Result.Commit,
				Tree:             committed.Operation.Result.Tree,
				LineReviewDigest: strings.Repeat("7", 64),
			},
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
		assert.False(t, changed)

		park, _, parked := parkOperationForTest(
			t,
			fixture,
			operationLeaseFromTransition(committed),
			[]PRDevelopmentControllerOperation{committed.Operation},
			fixture.Attempt.Summary,
			fixture.Attempt.Iterations,
			302,
		)
		assert.Equal(t, 1, park.Ordinal)
		assert.Equal(t, PRDevelopmentControllerReviewPending, parked.Controller.Phase)
		loaded, err := fixture.Store.GetPRDevelopmentControllerForCase(ctx, fixture.Case.ID)
		require.NoError(t, err)
		assert.Equal(t, parked.Controller.ID, loaded.ID)
		assert.Equal(t, parked.Controller.TipCommit, loaded.TipCommit)
	})

	t.Run("Park only no changes", func(t *testing.T) {
		t.Parallel()
		fixture := newLegacyBoundPRDevelopmentOperationFixture(t, ":memory:")
		park, _, parked := parkOperationForTest(
			t,
			fixture,
			fixture.Mutation,
			nil,
			fixture.Attempt.Summary,
			fixture.Attempt.Iterations,
			303,
		)
		assert.Zero(t, park.Ordinal)
		require.NotNil(t, parked.Fence)
		assert.True(t, parked.Fence.NoChanges)
		loaded, err := fixture.Store.GetPRDevelopmentControllerForCase(ctx, fixture.Case.ID)
		require.NoError(t, err)
		assert.Equal(t, PRDevelopmentControllerReviewPending, loaded.Phase)
		assert.Equal(t, fixture.SourceTree, loaded.Tree)
	})
}

func TestStorePRDevelopmentControllerAggregateRejectsFencedOperationSequenceWithoutPark(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	fixture := newLegacyBoundPRDevelopmentOperationFixture(t, ":memory:")
	_, _, committed := commitOperationForTest(t, fixture, fixture.Mutation, 311)
	park, _, _ := parkOperationForTest(
		t,
		fixture,
		operationLeaseFromTransition(committed),
		[]PRDevelopmentControllerOperation{committed.Operation},
		fixture.Attempt.Summary,
		fixture.Attempt.Iterations,
		312,
	)
	result, err := fixture.Store.db.ExecContext(ctx, `
		DELETE FROM pr_development_controller_operation_intents
		WHERE id = ?`, park.ID)
	require.NoError(t, err)
	deleted, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)

	_, err = fixture.Store.GetPRDevelopmentControllerForCase(ctx, fixture.Case.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not terminate with Park")
}

func TestStorePRDevelopmentControllerOperationParkMultilineSummarySurvivesReload(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "park-multiline-summary.db")
	fixture := newLegacyBoundPRDevelopmentOperationFixture(t, path)
	multilineSummary := "Validated the local candidate.\n\nChecks passed:\n- lint\n- targeted tests"
	result, err := fixture.Store.db.ExecContext(ctx, `
		UPDATE pr_development_repair_attempts
		SET summary = ?
		WHERE id = ? AND status = 'completed'`,
		multilineSummary,
		fixture.Attempt.ID,
	)
	require.NoError(t, err)
	updated, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), updated)
	fixture.Attempt.Summary = multilineSummary
	park, _, parked := parkOperationForTest(
		t,
		fixture,
		fixture.Mutation,
		nil,
		multilineSummary,
		fixture.Attempt.Iterations,
		321,
	)
	require.NotNil(t, parked.Fence)
	require.NoError(t, fixture.Store.Close())

	reopened, err := Open(ctx, path, WithClock(func() time.Time { return *fixture.Clock }))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	loaded, err := reopened.GetPRDevelopmentControllerForCase(ctx, fixture.Case.ID)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentControllerReviewPending, loaded.Phase)
	loadedPark, found, err := loadPRDevelopmentControllerOperationByID(ctx, reopened.db, park.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, multilineSummary, loadedPark.Request.CompletionSummary)
	assert.Contains(t, string(loadedPark.RequestJSON), `\n\nChecks passed:\n- lint`)
}

func TestStorePRDevelopmentControllerOperationParkAuditRejectsTamperedRepairAccount(
	t *testing.T,
) {
	t.Parallel()
	tests := []struct {
		name   string
		update string
		value  any
	}{
		{
			name:   "summary",
			update: "summary = ?",
			value:  "Tampered terminal summary",
		},
		{
			name:   "iterations",
			update: "iterations = ?",
			value:  2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newLegacyBoundPRDevelopmentOperationFixture(t, ":memory:")
			_, _, _ = parkOperationForTest(
				t,
				fixture,
				fixture.Mutation,
				nil,
				fixture.Attempt.Summary,
				fixture.Attempt.Iterations,
				322,
			)
			_, err := fixture.Store.db.ExecContext(
				ctx,
				"UPDATE pr_development_repair_attempts SET "+test.update+" WHERE id = ?",
				test.value,
				fixture.Attempt.ID,
			)
			require.NoError(t, err)
			_, err = fixture.Store.GetPRDevelopmentControllerForCase(ctx, fixture.Case.ID)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "differs from its completed repair account")
		})
	}
}

func TestStorePRDevelopmentControllerLegacyRecoveryRejectsActiveOperationClaimID(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	prepared := preparePRDevelopmentOperationRecoveryTarget(
		t,
		PRDevelopmentControllerOperationAdopt,
	)
	firstController := stagePRDevelopmentOperationExpiryForTest(t, prepared)
	operationClaim, changed, err := prepared.Fixture.Store.ClaimPRDevelopmentControllerOperationRecovery(
		ctx,
		PRDevelopmentControllerOperationRecoveryClaim{
			CaseID:           prepared.Fixture.Case.ID,
			AttemptID:        prepared.Operation.AttemptID,
			OperationID:      prepared.Operation.ID,
			ExpectedRevision: firstController.Revision,
			ClaimID:          "cross-generation-duplicate-claim",
			WorkerLabel:      "operation-claim-owner",
			Lease:            10 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)

	secondInput := validPRDevelopmentInputForTest()
	secondInput.PRDevelopmentCaptureIdentity = addPRDevelopmentDispatch(
		t,
		prepared.Fixture.Store,
		"delivery-operation-duplicate-claim-second-controller",
		secondInput.WorkflowRef,
		secondInput.WorkflowRevision,
	)
	secondInput.Repository = "acme/second-project"
	secondInput.BaseRepository = secondInput.Repository
	secondInput.PullNumber = 77
	secondInput.PullURL = "https://github.com/acme/second-project/pull/77"
	secondInput.ReviewURL = secondInput.PullURL + "#pullrequestreview-501"
	secondCase, created, err := prepared.Fixture.Store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(secondInput),
	)
	require.NoError(t, err)
	require.True(t, created)
	secondSession := completePRDevelopmentRepairForControllerTest(
		t,
		prepared.Fixture.Store,
		secondCase.ID,
	)
	secondAttempt := secondSession.Attempts[len(secondSession.Attempts)-1]
	secondMutation, acquired, err := prepared.Fixture.Store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           secondCase.ID,
			AttemptID:        secondAttempt.ID,
			ExpectedRevision: 0,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "legacy-duplicate-claim-mutation",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, secondMutation.Controller.LeaseUntil)
	*prepared.Fixture.Clock = secondMutation.Controller.LeaseUntil.Add(time.Second)
	_, _, err = prepared.Fixture.Store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           secondCase.ID,
			AttemptID:        secondAttempt.ID,
			ExpectedRevision: secondMutation.Controller.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "legacy-duplicate-claim-replacement",
			Lease:            time.Minute,
		},
	)
	require.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	secondRecovery, err := prepared.Fixture.Store.GetPRDevelopmentControllerForCase(
		ctx,
		secondCase.ID,
	)
	require.NoError(t, err)

	_, changed, err = prepared.Fixture.Store.ClaimPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryClaim{
			CaseID:           secondCase.ID,
			AttemptID:        secondAttempt.ID,
			ExpectedRevision: secondRecovery.Revision,
			ClaimID:          operationClaim.Operation.ClaimID,
			WorkerLabel:      "legacy-duplicate-claim-owner",
			Lease:            time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed)
	legacyIntent, found, err := loadActivePRDevelopmentRecoveryIntent(
		ctx,
		prepared.Fixture.Store.db,
		secondRecovery.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, PRDevelopmentControllerRecoveryPending, legacyIntent.Status)
	assert.Empty(t, legacyIntent.ClaimID)
}

func TestStorePRDevelopmentControllerOperationRejectsUnixEpochCommitAuthoredTime(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOperationFixture(t, ":memory:")
	_, adopted := adoptOperationForTest(t, fixture, fixture.Mutation)
	lease := operationLeaseFromTransition(adopted)
	request := operationBaseRequest(fixture, lease.Controller)
	request.ExpectedTree = strings.Repeat("3", len(lease.Controller.TipCommit))
	request.EffectIntentID = operationEffectID("pdcmt_", 401)
	request.ExpectedParent = lease.Controller.TipCommit
	request.CandidateDigest = strings.Repeat("4", 64)
	request.CommitMessage = "This epoch timestamp must never collapse to the zero sentinel"
	request.AuthoredAt = time.Unix(0, 0).UTC()
	_, changed, err := fixture.Store.PreparePRDevelopmentControllerOperation(
		ctx,
		PRDevelopmentControllerOperationPrepare{
			OperationID:      fixture.operationID(),
			ControllerID:     lease.Controller.ID,
			AttemptID:        fixture.Attempt.ID,
			ExpectedRevision: lease.Controller.Revision,
			LeaseToken:       lease.Controller.LeaseToken,
			LeaseEpoch:       lease.Controller.LeaseEpoch,
			Kind:             PRDevelopmentControllerOperationCommit,
			Request:          request,
		},
	)
	assert.ErrorIs(t, err, ErrInvalidPRDevelopmentController)
	assert.False(t, changed)
	var operationCount int
	require.NoError(t, fixture.Store.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM pr_development_controller_operation_intents
		WHERE controller_id = ?`, lease.Controller.ID).Scan(&operationCount))
	assert.Equal(t, 1, operationCount, "invalid Commit must not append after Adopt")
}

func seedFullPRDevelopmentRecoveryAuditForOperationTest(
	t *testing.T,
	fixture *prDevelopmentOperationFixture,
) {
	t.Helper()
	ctx := context.Background()
	require.NotNil(t, fixture.Mutation.Controller.LeaseUntil)
	tx, err := fixture.Store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", 44), ", ")
	statement, err := tx.PrepareContext(ctx, `
		INSERT INTO pr_development_controller_recovery_intents (`+
		prDevelopmentRecoveryIntentColumns+`) VALUES (`+placeholders+`)`)
	require.NoError(t, err)
	defer func() { require.NoError(t, statement.Close()) }()

	previousHash := emptyPRDevelopmentRecoveryDigest()
	previousReservationDigest := prDevelopmentMutationReservationDigest(
		fixture.Mutation.Controller.MutationReservationKey,
	)
	expiredLeaseEpoch := fixture.Mutation.Controller.LeaseEpoch
	expiredLeaseTokenDigest := prDevelopmentLeaseTokenDigest(
		PRDevelopmentControllerMutationLease,
		fixture.Mutation.Controller.LeaseToken,
	)
	baseTime := *fixture.Mutation.Controller.LeaseUntil
	var (
		lastReservationKey string
		lastLeaseToken     string
		lastLeaseUntil     time.Time
		lastRevision       int64
	)
	for ordinal := 0; ordinal < MaxPRDevelopmentControllerRecoveries; ordinal++ {
		createdAt := baseTime.Add(time.Duration(ordinal*2) * time.Second)
		newLeaseUntil := createdAt.Add(time.Second)
		replacementKey := fmt.Sprintf("pdck_%032x", ordinal+1)
		replacementDigest := prDevelopmentMutationReservationDigest(replacementKey)
		newLeaseToken := fmt.Sprintf("lease_capacity-operation-%d", ordinal+1)
		newLeaseTokenDigest := prDevelopmentLeaseTokenDigest(
			PRDevelopmentControllerMutationLease,
			newLeaseToken,
		)
		recoveryRevision := int64(ordinal*2 + 2)
		intent := PRDevelopmentControllerRecoveryIntent{
			ID:                           fmt.Sprintf("pdri_%032x", ordinal+1),
			ControllerID:                 fixture.Mutation.Controller.ID,
			AttemptID:                    fixture.Attempt.ID,
			Ordinal:                      ordinal,
			RecoveryRevision:             recoveryRevision,
			Mode:                         PRDevelopmentControllerRecoveryUnbound,
			Status:                       PRDevelopmentControllerRecoveryFinalized,
			AgentID:                      fixture.Mutation.Controller.AgentID,
			WorkspaceID:                  fixture.Session.WorkspaceID,
			LineID:                       fixture.Mutation.Controller.LineID,
			SourceCloneURL:               fixture.Session.CloneURL,
			SourceRef:                    fixture.Session.HeadRef,
			SourceCommit:                 fixture.Session.HeadSHA,
			PreviousReservationDigest:    previousReservationDigest,
			ReplacementReservationDigest: replacementDigest,
			ExpiredControllerRevision:    recoveryRevision - 1,
			ExpiredLeaseEpoch:            expiredLeaseEpoch,
			ExpiredLeaseTokenDigest:      expiredLeaseTokenDigest,
			PreviousHash:                 previousHash,
			ClaimID:                      fmt.Sprintf("operation-capacity-claim-%d", ordinal+1),
			ClaimOwner:                   "operation-capacity-worker",
			ClaimEpoch:                   1,
			Claims:                       1,
			RotationResultHash:           strings.Repeat("8", 64),
			RecoveryClaimTokenDigest:     fmt.Sprintf("%064x", ordinal+1),
			NewMutationLeaseEpoch:        expiredLeaseEpoch + 1,
			NewMutationLeaseTokenDigest:  newLeaseTokenDigest,
			NewMutationLeaseUntil:        &newLeaseUntil,
			FinalRevision:                recoveryRevision + 1,
			CreatedAt:                    createdAt,
			ClaimedAt:                    &createdAt,
			FinalizedAt:                  &createdAt,
			UpdatedAt:                    createdAt,
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
		require.NoError(t, err, "seed finalized operation recovery audit %d", ordinal)
		previousHash = intent.FinalHash
		previousReservationDigest = replacementDigest
		expiredLeaseEpoch = intent.NewMutationLeaseEpoch
		expiredLeaseTokenDigest = intent.NewMutationLeaseTokenDigest
		lastReservationKey = replacementKey
		lastLeaseToken = newLeaseToken
		lastLeaseUntil = newLeaseUntil
		lastRevision = intent.FinalRevision
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET revision = ?, lease_owner = ?, lease_token = ?, lease_until = ?,
			lease_epoch = ?, claims = ?, mutation_reservation_key = ?, updated_at = ?
		WHERE id = ?`,
		lastRevision,
		"operation-capacity-worker",
		lastLeaseToken,
		toDBTime(lastLeaseUntil),
		expiredLeaseEpoch,
		expiredLeaseEpoch,
		lastReservationKey,
		toDBTime(lastLeaseUntil.Add(-time.Second)),
		fixture.Mutation.Controller.ID,
	)
	require.NoError(t, err)
	updated, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), updated)
	require.NoError(t, tx.Commit())
	*fixture.Clock = lastLeaseUntil.Add(-time.Second)
}

func TestStorePRDevelopmentControllerOperationPrepareRejectsFullRecoveryAuditCapacity(
	t *testing.T,
) {
	ctx := context.Background()
	fixture := newPRDevelopmentOperationFixture(t, ":memory:")
	seedFullPRDevelopmentRecoveryAuditForOperationTest(t, fixture)
	controller, found, err := loadPRDevelopmentControllerAggregateByID(
		ctx,
		fixture.Store.db,
		fixture.Mutation.Controller.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, MaxPRDevelopmentControllerRecoveries*2+1, int(controller.Revision))
	assert.Equal(t, int64(MaxPRDevelopmentControllerRecoveries+1), controller.LeaseEpoch)

	request := operationBaseRequest(fixture, controller)
	request.ExpectedTree = fixture.SourceTree
	_, changed, err := fixture.Store.PreparePRDevelopmentControllerOperation(
		ctx,
		PRDevelopmentControllerOperationPrepare{
			OperationID:      fixture.operationID(),
			ControllerID:     controller.ID,
			AttemptID:        fixture.Attempt.ID,
			ExpectedRevision: controller.Revision,
			LeaseToken:       controller.LeaseToken,
			LeaseEpoch:       controller.LeaseEpoch,
			Kind:             PRDevelopmentControllerOperationAdopt,
			Request:          request,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.Contains(t, err.Error(), "recovery audit capacity")
	assert.False(t, changed)
	var operationCount int
	require.NoError(t, fixture.Store.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM pr_development_controller_operation_intents
		WHERE controller_id = ?`, controller.ID).Scan(&operationCount))
	assert.Zero(t, operationCount)
}

func TestStorePRDevelopmentControllerOperationCommitAdvancesHighWaterBeforeParkAndReopen(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "commit-high-water-reopen.db")
	fixture := newPRDevelopmentOperationFixture(t, path)
	_, adopted := adoptOperationForTest(t, fixture, fixture.Mutation)
	adoptedAt := adopted.Controller.UpdatedAt
	*fixture.Clock = fixture.Clock.Add(time.Second)
	boundLease := operationLeaseFromTransition(adopted)
	_, _, committed := commitOperationForTest(t, fixture, boundLease, 501)
	assert.True(t, committed.Controller.UpdatedAt.After(adoptedAt))
	assert.Equal(t, *fixture.Clock, committed.Controller.UpdatedAt)

	_, _, parked := parkOperationForTest(
		t,
		fixture,
		operationLeaseFromTransition(committed),
		[]PRDevelopmentControllerOperation{committed.Operation},
		fixture.Attempt.Summary,
		fixture.Attempt.Iterations,
		502,
	)
	assert.Equal(t, PRDevelopmentControllerReviewPending, parked.Controller.Phase)
	require.NoError(t, fixture.Store.Close())
	reopened, err := Open(ctx, path, WithClock(func() time.Time { return *fixture.Clock }))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	loaded, err := reopened.GetPRDevelopmentControllerForCase(ctx, fixture.Case.ID)
	require.NoError(t, err)
	assert.Equal(t, parked.Controller.ID, loaded.ID)
	assert.Equal(t, parked.Controller.TipCommit, loaded.TipCommit)
}

func TestStorePRDevelopmentControllerBoundRecoveryReservesSuspensionHeadroom(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOperationFixture(t, ":memory:")
	_, adopted := adoptOperationForTest(t, fixture, fixture.Mutation)
	_, _, firstPark := parkOperationForTest(
		t,
		fixture,
		operationLeaseFromTransition(adopted),
		nil,
		fixture.Attempt.Summary,
		fixture.Attempt.Iterations,
		601,
	)

	// This is the state produced by many safe review release/reclaim cycles.
	// Seed it directly so the regression reaches the controller revision ceiling
	// without tens of thousands of public transitions.
	review, acquired, err := fixture.Store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           fixture.Case.ID,
			AttemptID:        firstPark.Controller.CurrentAttemptID,
			ExpectedRevision: firstPark.Controller.Revision,
			Kind:             PRDevelopmentControllerReviewLease,
			WorkerLabel:      "park-headroom-reviewer",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	highReviewRevision := int64(MaxPRDevelopmentControllerRevision) - 7
	highReviewLeaseEpoch := highReviewRevision - 1
	seeded, err := fixture.Store.db.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET revision = ?, lease_epoch = ?, claims = ?
		WHERE id = ? AND revision = ? AND phase = 'review' AND
			lease_token = ? AND lease_epoch = ?`,
		highReviewRevision,
		highReviewLeaseEpoch,
		highReviewLeaseEpoch,
		review.Controller.ID,
		review.Controller.Revision,
		review.Controller.LeaseToken,
		review.Controller.LeaseEpoch,
	)
	require.NoError(t, err)
	seededRows, err := seeded.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), seededRows)
	review.Controller.Revision = highReviewRevision
	review.Controller.LeaseEpoch = highReviewLeaseEpoch
	review.Controller.Claims = int(highReviewLeaseEpoch)
	ready, changed, err := fixture.Store.FinishPRDevelopmentControllerReview(
		ctx,
		PRDevelopmentControllerReviewTransition{
			ControllerID:     review.Controller.ID,
			AttemptID:        review.Controller.CurrentAttemptID,
			ExpectedRevision: review.Controller.Revision,
			LeaseToken:       review.Controller.LeaseToken,
			LeaseEpoch:       review.Controller.LeaseEpoch,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, int64(MaxPRDevelopmentControllerRevision)-6, ready.Revision)

	workbench, err := fixture.Store.GetPRDevelopmentWorkbench(ctx, fixture.Case.ID)
	require.NoError(t, err)
	require.NotNil(t, workbench.RepairSession)
	next, admitted, err := fixture.Store.AdmitPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairAdmit{
			CaseID:                      fixture.Case.ID,
			ExpectedConversationVersion: 0,
			ExpectedRepairVersion:       workbench.RepairSession.Version,
			IdempotencyKey:              "expired-park-two-revision-headroom",
			AgentID:                     fixture.Session.AgentID,
			Instruction:                 "Reach Park at the exact revision boundary.",
		},
	)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NotNil(t, next.RepairSession)
	fixture.Session = *next.RepairSession
	fixture.Attempt = fixture.Session.Attempts[len(fixture.Session.Attempts)-1]
	mutation, acquired, err := fixture.Store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           fixture.Case.ID,
			AttemptID:        fixture.Attempt.ID,
			ExpectedRevision: ready.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "park-headroom-mutation-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, int64(MaxPRDevelopmentControllerRevision)-5, mutation.Controller.Revision)

	resumeRequest := operationBaseRequest(fixture, mutation.Controller)
	resumeRequest.ExpectedVersion = mutation.Controller.LineVersion
	resumeRequest.ExpectedEpoch = mutation.Controller.MutationEpoch
	resumeRequest.ExpectedTip = mutation.Controller.TipCommit
	resumeRequest.ExpectedTree = mutation.Controller.Tree
	resume := prepareOperationForTest(
		t,
		fixture,
		mutation,
		PRDevelopmentControllerOperationResume,
		fixture.operationID(),
		resumeRequest,
	)
	resumed := finalizeOperationForTest(
		t,
		fixture,
		mutation,
		resume,
		PRDevelopmentControllerOperationResult{
			WorkspaceID:   fixture.Session.WorkspaceID,
			Version:       mutation.Controller.LineVersion,
			MutationEpoch: mutation.Controller.MutationEpoch + 1,
			Tip:           mutation.Controller.TipCommit,
			Tree:          mutation.Controller.Tree,
		},
	)
	require.Equal(t, int64(MaxPRDevelopmentControllerRevision)-4, resumed.Controller.Revision)
	require.NotNil(t, resumed.Controller.LeaseUntil)
	*fixture.Clock = resumed.Controller.LeaseUntil.Add(time.Second)
	_, _, err = fixture.Store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           fixture.Case.ID,
			AttemptID:        fixture.Attempt.ID,
			ExpectedRevision: resumed.Controller.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "park-headroom-recovery-trigger",
			Lease:            time.Minute,
		},
	)
	require.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	legacyRecovery, err := fixture.Store.GetPRDevelopmentControllerForCase(
		ctx,
		fixture.Case.ID,
	)
	require.NoError(t, err)
	require.Equal(t, int64(MaxPRDevelopmentControllerRevision)-3, legacyRecovery.Revision)
	legacyClaim, changed, err := fixture.Store.ClaimPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryClaim{
			CaseID:           fixture.Case.ID,
			AttemptID:        fixture.Attempt.ID,
			ExpectedRevision: legacyRecovery.Revision,
			ClaimID:          "park-headroom-legacy-recovery-claim",
			WorkerLabel:      "park-headroom-legacy-recovery-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	recovered, changed, err := fixture.Store.FinalizePRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryFinalize{
			ControllerID:     legacyClaim.Controller.ID,
			AttemptID:        legacyClaim.Intent.AttemptID,
			RecoveryID:       legacyClaim.Intent.ID,
			ExpectedRevision: legacyClaim.Controller.Revision,
			ClaimID:          legacyClaim.Intent.ClaimID,
			ClaimToken:       legacyClaim.Intent.ClaimToken,
			ClaimEpoch:       legacyClaim.Intent.ClaimEpoch,
			Rotation: PRDevelopmentControllerRecoveryRotationResult{
				WorkspaceID:   legacyClaim.Intent.WorkspaceID,
				Bound:         true,
				Version:       legacyClaim.Intent.LineVersion,
				MutationEpoch: legacyClaim.Intent.MutationEpoch,
				Tip:           legacyClaim.Intent.TipCommit,
				Tree:          legacyClaim.Intent.Tree,
				RotationHash:  strings.Repeat("7", 64),
			},
			Lease: time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, int64(MaxPRDevelopmentControllerRevision)-1, recovered.Revision)
	require.Equal(t, PRDevelopmentControllerSuspensionPending, recovered.Phase)
	require.Empty(t, recovered.MutationReservationKey)
	suspension, found, err := loadPRDevelopmentControllerSuspensionBySource(
		ctx,
		fixture.Store.db,
		PRDevelopmentControllerSuspensionSourceControllerRecovery,
		legacyClaim.Intent.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(MaxPRDevelopmentControllerRevision)-2, suspension.SourceFinalRevision)
	assert.Equal(t, recovered.Revision, suspension.SourceFinalRevision+1)
}

func TestStorePRDevelopmentControllerOperationRecoveryByKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		kind           PRDevelopmentControllerOperationKind
		alreadyRotated bool
	}{
		{name: "Adopt before ambiguous Git effect", kind: PRDevelopmentControllerOperationAdopt},
		{name: "Adopt after ambiguous Git effect", kind: PRDevelopmentControllerOperationAdopt, alreadyRotated: true},
		{name: "Resume", kind: PRDevelopmentControllerOperationResume},
		{name: "Commit", kind: PRDevelopmentControllerOperationCommit, alreadyRotated: true},
		{name: "Park", kind: PRDevelopmentControllerOperationPark, alreadyRotated: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			prepared := preparePRDevelopmentOperationRecoveryTarget(t, test.kind)
			controller := stagePRDevelopmentOperationExpiryForTest(t, prepared)

			var legacyTotal, legacyPending int
			require.NoError(t, prepared.Fixture.Store.db.QueryRowContext(ctx, `
				SELECT COUNT(*), COALESCE(SUM(CASE WHEN status <> 'finalized' THEN 1 ELSE 0 END), 0)
				FROM pr_development_controller_recovery_intents
				WHERE controller_id = ?`, controller.ID).Scan(&legacyTotal, &legacyPending))
			assert.Zero(t, legacyTotal, "operation expiry must not manufacture legacy recovery")
			assert.Zero(t, legacyPending)

			claimInput := PRDevelopmentControllerOperationRecoveryClaim{
				CaseID:           prepared.Fixture.Case.ID,
				AttemptID:        prepared.Operation.AttemptID,
				OperationID:      prepared.Operation.ID,
				ExpectedRevision: controller.Revision,
				ClaimID:          "operation-recovery-claim-" + string(test.kind),
				WorkerLabel:      "operation-recovery-worker",
				Lease:            time.Minute,
			}
			claimed, changed, err := prepared.Fixture.Store.ClaimPRDevelopmentControllerOperationRecovery(
				ctx,
				claimInput,
			)
			require.NoError(t, err)
			require.True(t, changed)
			assert.Equal(t, PRDevelopmentControllerOperationRecoveryClaimed, claimed.Operation.Status)
			assert.NotEmpty(t, claimed.Controller.MutationReservationKey)
			if test.kind == PRDevelopmentControllerOperationPark {
				assert.Empty(t, claimed.Operation.RecoveryID)
				assert.Empty(t, claimed.Operation.ReplacementReservationKey)
			} else {
				assert.NotEmpty(t, claimed.Operation.RecoveryID)
				assert.NotEmpty(t, claimed.Operation.ReplacementReservationKey)
			}

			replayedClaim, changed, err := prepared.Fixture.Store.ClaimPRDevelopmentControllerOperationRecovery(
				ctx,
				claimInput,
			)
			require.NoError(t, err)
			assert.False(t, changed)
			assert.Equal(t, claimed.Operation.ClaimToken, replayedClaim.Operation.ClaimToken)

			foreignClaim := claimInput
			foreignClaim.ClaimID += "-foreign"
			foreignClaim.WorkerLabel = "foreign-operation-recovery-worker"
			_, changed, err = prepared.Fixture.Store.ClaimPRDevelopmentControllerOperationRecovery(
				ctx,
				foreignClaim,
			)
			assert.ErrorIs(t, err, ErrPRDevelopmentControllerActive)
			assert.False(t, changed)

			finalize := PRDevelopmentControllerOperationRecoveryFinalize{
				ControllerID:     claimed.Controller.ID,
				AttemptID:        claimed.Operation.AttemptID,
				OperationID:      claimed.Operation.ID,
				RecoveryID:       claimed.Operation.RecoveryID,
				ExpectedRevision: claimed.Controller.Revision,
				ClaimID:          claimed.Operation.ClaimID,
				ClaimToken:       claimed.Operation.ClaimToken,
				ClaimEpoch:       claimed.Operation.ClaimEpoch,
				Rotation:         recoveryRotationForOperationTest(prepared, test.alreadyRotated),
				Result:           prepared.Result,
			}
			if test.kind != PRDevelopmentControllerOperationPark {
				finalize.Lease = time.Minute
			}
			if test.kind == PRDevelopmentControllerOperationCommit {
				finalize.Result.WorkspaceClean = false
			}
			transition, changed, err := prepared.Fixture.Store.FinalizePRDevelopmentControllerOperationRecovery(
				ctx,
				finalize,
			)
			require.NoError(t, err)
			require.True(t, changed)
			assert.Equal(t, PRDevelopmentControllerOperationFinalized, transition.Operation.Status)
			assert.Empty(t, transition.Operation.ReplacementReservationKey)
			assert.Empty(t, transition.Operation.ClaimToken)
			if test.kind == PRDevelopmentControllerOperationPark {
				assert.Equal(t, PRDevelopmentControllerReviewPending, transition.Controller.Phase)
				assert.Empty(t, transition.Controller.MutationReservationKey)
				require.NotNil(t, transition.Fence)
			} else {
				assert.Equal(t, PRDevelopmentControllerSuspensionPending, transition.Controller.Phase)
				assert.Empty(t, transition.Controller.MutationReservationKey)
				assert.Empty(t, transition.Controller.LeaseToken)
				assert.Nil(t, transition.Fence)
				suspension, found, loadErr := loadPRDevelopmentControllerSuspensionBySource(
					ctx,
					prepared.Fixture.Store.db,
					PRDevelopmentControllerSuspensionSourceOperationRecovery,
					transition.Operation.RecoveryID,
				)
				require.NoError(t, loadErr)
				require.True(t, found)
				assert.Equal(t, transition.Operation.ID, suspension.SourceOperationID)
				assert.Equal(t, transition.Operation.FinalHash, suspension.SourceFinalHash)
				assert.Equal(t, transition.Operation.FinalControllerRevision, suspension.SourceFinalRevision)
				assert.Equal(t, PRDevelopmentControllerSuspensionStatusSuspendPending, suspension.Status)
				assert.NotEmpty(t, suspension.SuspensionReservationKey)
				if test.kind == PRDevelopmentControllerOperationCommit {
					assert.Equal(t, PRDevelopmentControllerSuspensionCommitRecovery, suspension.Mode)
					assert.False(t, transition.Operation.Result.WorkspaceClean)
				} else {
					assert.Equal(t, PRDevelopmentControllerSuspensionCandidate, suspension.Mode)
				}
			}

			require.NoError(t, prepared.Fixture.Store.db.QueryRowContext(ctx, `
				SELECT COUNT(*), COALESCE(SUM(CASE WHEN status <> 'finalized' THEN 1 ELSE 0 END), 0)
				FROM pr_development_controller_recovery_intents
				WHERE controller_id = ?`, controller.ID).Scan(&legacyTotal, &legacyPending))
			if test.kind == PRDevelopmentControllerOperationPark {
				assert.Zero(t, legacyTotal)
			} else {
				assert.Equal(t, 1, legacyTotal)
				var recoveryID, status, oldBearer, newBearer string
				require.NoError(t, prepared.Fixture.Store.db.QueryRowContext(ctx, `
					SELECT id, status, previous_reservation_key, replacement_reservation_key
					FROM pr_development_controller_recovery_intents
					WHERE controller_id = ?`, controller.ID).Scan(
					&recoveryID,
					&status,
					&oldBearer,
					&newBearer,
				))
				assert.Equal(t, transition.Operation.RecoveryID, recoveryID)
				assert.Equal(t, string(PRDevelopmentControllerRecoveryFinalized), status)
				assert.Empty(t, oldBearer)
				assert.Empty(t, newBearer)
			}
			assert.Zero(t, legacyPending)

			replayed, changed, err := prepared.Fixture.Store.FinalizePRDevelopmentControllerOperationRecovery(
				ctx,
				finalize,
			)
			require.NoError(t, err)
			assert.False(t, changed)
			assert.Equal(t, transition.Operation.FinalHash, replayed.Operation.FinalHash)
			assert.Equal(t, transition.Controller, replayed.Controller)
		})
	}
}

func TestStorePRDevelopmentControllerOperationRecoveryClaimRenewReclaimFencesStaleWorker(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	prepared := preparePRDevelopmentOperationRecoveryTarget(
		t,
		PRDevelopmentControllerOperationCommit,
	)
	controller := stagePRDevelopmentOperationExpiryForTest(t, prepared)
	claimInput := PRDevelopmentControllerOperationRecoveryClaim{
		CaseID:           prepared.Fixture.Case.ID,
		AttemptID:        prepared.Operation.AttemptID,
		OperationID:      prepared.Operation.ID,
		ExpectedRevision: controller.Revision,
		ClaimID:          "operation-recovery-first-claim",
		WorkerLabel:      "operation-recovery-first-worker",
		Lease:            30 * time.Second,
	}
	first, changed, err := prepared.Fixture.Store.ClaimPRDevelopmentControllerOperationRecovery(
		ctx,
		claimInput,
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, first.Operation.ClaimUntil)
	firstDeadline := *first.Operation.ClaimUntil

	require.NoError(t, prepared.Fixture.Store.RenewPRDevelopmentControllerOperationRecovery(
		ctx,
		PRDevelopmentControllerOperationRecoveryRenew{
			ControllerID: first.Controller.ID,
			AttemptID:    first.Operation.AttemptID,
			OperationID:  first.Operation.ID,
			RecoveryID:   first.Operation.RecoveryID,
			ClaimID:      first.Operation.ClaimID,
			ClaimToken:   first.Operation.ClaimToken,
			ClaimEpoch:   first.Operation.ClaimEpoch,
			Lease:        2 * time.Minute,
		},
	))
	renewed, found, err := loadPRDevelopmentControllerOperationByID(
		ctx,
		prepared.Fixture.Store.db,
		first.Operation.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, renewed.ClaimUntil)
	assert.True(t, renewed.ClaimUntil.After(firstDeadline))

	err = prepared.Fixture.Store.RenewPRDevelopmentControllerOperationRecovery(
		ctx,
		PRDevelopmentControllerOperationRecoveryRenew{
			ControllerID: first.Controller.ID,
			AttemptID:    first.Operation.AttemptID,
			OperationID:  first.Operation.ID,
			RecoveryID:   first.Operation.RecoveryID,
			ClaimID:      first.Operation.ClaimID,
			ClaimToken:   "lease_stale-operation-recovery-token",
			ClaimEpoch:   first.Operation.ClaimEpoch,
			Lease:        3 * time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrStaleLease)

	*prepared.Fixture.Clock = renewed.ClaimUntil.Add(time.Second)
	secondInput := claimInput
	secondInput.ClaimID = "operation-recovery-second-claim"
	secondInput.WorkerLabel = "operation-recovery-second-worker"
	second, changed, err := prepared.Fixture.Store.ClaimPRDevelopmentControllerOperationRecovery(
		ctx,
		secondInput,
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.True(t, second.Reclaimed)
	assert.Greater(t, second.Operation.ClaimEpoch, first.Operation.ClaimEpoch)
	assert.NotEqual(t, first.Operation.ClaimToken, second.Operation.ClaimToken)

	err = prepared.Fixture.Store.RenewPRDevelopmentControllerOperationRecovery(
		ctx,
		PRDevelopmentControllerOperationRecoveryRenew{
			ControllerID: first.Controller.ID,
			AttemptID:    first.Operation.AttemptID,
			OperationID:  first.Operation.ID,
			RecoveryID:   first.Operation.RecoveryID,
			ClaimID:      first.Operation.ClaimID,
			ClaimToken:   first.Operation.ClaimToken,
			ClaimEpoch:   first.Operation.ClaimEpoch,
			Lease:        time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrStaleLease)

	staleFinalize := PRDevelopmentControllerOperationRecoveryFinalize{
		ControllerID:     first.Controller.ID,
		AttemptID:        first.Operation.AttemptID,
		OperationID:      first.Operation.ID,
		RecoveryID:       first.Operation.RecoveryID,
		ExpectedRevision: first.Controller.Revision,
		ClaimID:          first.Operation.ClaimID,
		ClaimToken:       first.Operation.ClaimToken,
		ClaimEpoch:       first.Operation.ClaimEpoch,
		Rotation:         recoveryRotationForOperationTest(prepared, false),
		Result:           prepared.Result,
		Lease:            time.Minute,
	}
	_, changed, err = prepared.Fixture.Store.FinalizePRDevelopmentControllerOperationRecovery(
		ctx,
		staleFinalize,
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed)

	validFinalize := staleFinalize
	validFinalize.ClaimID = second.Operation.ClaimID
	validFinalize.ClaimToken = second.Operation.ClaimToken
	validFinalize.ClaimEpoch = second.Operation.ClaimEpoch
	transition, changed, err := prepared.Fixture.Store.FinalizePRDevelopmentControllerOperationRecovery(
		ctx,
		validFinalize,
	)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, second.Operation.ClaimID, transition.Operation.ClaimID)
	assert.Equal(t, second.Operation.ClaimEpoch, transition.Operation.ClaimEpoch)
}

func TestStorePRDevelopmentControllerOperationPersistsAcrossReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "operation-reopen.db")
	fixture := newPRDevelopmentOperationFixture(t, path)
	request := operationBaseRequest(fixture, fixture.Mutation.Controller)
	request.ExpectedTree = fixture.SourceTree
	operation := prepareOperationForTest(
		t,
		fixture,
		fixture.Mutation,
		PRDevelopmentControllerOperationAdopt,
		fixture.operationID(),
		request,
	)
	require.NoError(t, fixture.Store.Close())
	reopened, err := Open(ctx, path, WithClock(func() time.Time { return *fixture.Clock }))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	fixture.Store = reopened

	replayed, changed, err := reopened.PreparePRDevelopmentControllerOperation(
		ctx,
		PRDevelopmentControllerOperationPrepare{
			OperationID:      operation.ID,
			ControllerID:     operation.ControllerID,
			AttemptID:        operation.AttemptID,
			ExpectedRevision: operation.PreparedControllerRevision,
			LeaseToken:       fixture.Mutation.Controller.LeaseToken,
			LeaseEpoch:       operation.MutationLeaseEpoch,
			Kind:             operation.Kind,
			Request:          request,
		},
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, operation.IntentHash, replayed.IntentHash)

	transition := finalizeOperationForTest(
		t,
		fixture,
		fixture.Mutation,
		operation,
		PRDevelopmentControllerOperationResult{
			WorkspaceID:   fixture.Session.WorkspaceID,
			Version:       0,
			MutationEpoch: 1,
			Tip:           fixture.Session.HeadSHA,
			Tree:          fixture.SourceTree,
		},
	)
	assert.Equal(t, PRDevelopmentControllerOperationFinalized, transition.Operation.Status)
	assert.Equal(t, fixture.Session.WorkspaceID, transition.Controller.WorkspaceID)
}

func TestStorePRDevelopmentControllerOperationTamperFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("request hash", func(t *testing.T) {
		t.Parallel()
		fixture := newPRDevelopmentOperationFixture(t, ":memory:")
		request := operationBaseRequest(fixture, fixture.Mutation.Controller)
		request.ExpectedTree = fixture.SourceTree
		operation := prepareOperationForTest(
			t,
			fixture,
			fixture.Mutation,
			PRDevelopmentControllerOperationAdopt,
			fixture.operationID(),
			request,
		)
		_, err := fixture.Store.db.ExecContext(ctx, `
			UPDATE pr_development_controller_operation_intents
			SET request_hash = ? WHERE id = ?`, strings.Repeat("0", 64), operation.ID)
		require.NoError(t, err)
		_, _, err = fixture.Store.PreparePRDevelopmentControllerOperation(
			ctx,
			PRDevelopmentControllerOperationPrepare{
				OperationID:      operation.ID,
				ControllerID:     operation.ControllerID,
				AttemptID:        operation.AttemptID,
				ExpectedRevision: operation.PreparedControllerRevision,
				LeaseToken:       fixture.Mutation.Controller.LeaseToken,
				LeaseEpoch:       operation.MutationLeaseEpoch,
				Kind:             operation.Kind,
				Request:          request,
			},
		)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "request hash is invalid")
	})

	t.Run("final hash", func(t *testing.T) {
		t.Parallel()
		fixture := newPRDevelopmentOperationFixture(t, ":memory:")
		operation, transition := adoptOperationForTest(t, fixture, fixture.Mutation)
		_, err := fixture.Store.db.ExecContext(ctx, `
			UPDATE pr_development_controller_operation_intents
			SET final_hash = ? WHERE id = ?`, strings.Repeat("0", 64), operation.ID)
		require.NoError(t, err)
		_, _, err = fixture.Store.FinalizePRDevelopmentControllerOperation(
			ctx,
			PRDevelopmentControllerOperationFinalize{
				ControllerID:     operation.ControllerID,
				AttemptID:        operation.AttemptID,
				OperationID:      operation.ID,
				ExpectedRevision: operation.PreparedControllerRevision,
				LeaseToken:       fixture.Mutation.Controller.LeaseToken,
				LeaseEpoch:       operation.MutationLeaseEpoch,
				Result:           transition.Operation.Result,
			},
		)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "final evidence is invalid")
	})
}

func TestStorePRDevelopmentControllerOperationRecoveryRejectsForeignReplacementAuthority(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	prepared := preparePRDevelopmentOperationRecoveryTarget(
		t,
		PRDevelopmentControllerOperationAdopt,
	)
	_ = stagePRDevelopmentOperationExpiryForTest(t, prepared)

	secondInput := validPRDevelopmentInputForTest()
	secondInput.PRDevelopmentCaptureIdentity = addPRDevelopmentDispatch(
		t,
		prepared.Fixture.Store,
		"delivery-operation-foreign-replacement-controller",
		secondInput.WorkflowRef,
		secondInput.WorkflowRevision,
	)
	secondInput.Repository = "acme/foreign-authority-project"
	secondInput.BaseRepository = secondInput.Repository
	secondInput.PullNumber = 91
	secondInput.PullURL = "https://github.com/acme/foreign-authority-project/pull/91"
	secondInput.ReviewURL = secondInput.PullURL + "#pullrequestreview-901"
	secondCase, created, err := prepared.Fixture.Store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(secondInput),
	)
	require.NoError(t, err)
	require.True(t, created)
	secondSession := completePRDevelopmentRepairForControllerTest(
		t,
		prepared.Fixture.Store,
		secondCase.ID,
	)
	secondAttempt := secondSession.Attempts[len(secondSession.Attempts)-1]
	secondFixture := bindPRDevelopmentControllerForTest(
		t,
		prepared.Fixture.Store,
		secondCase.ID,
		secondSession,
	)
	require.NotNil(t, secondFixture.Bound.LeaseUntil)
	*prepared.Fixture.Clock = secondFixture.Bound.LeaseUntil.Add(time.Second)
	_, _, err = prepared.Fixture.Store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           secondCase.ID,
			AttemptID:        secondAttempt.ID,
			ExpectedRevision: secondFixture.Bound.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "foreign-replacement-expiry-worker",
			Lease:            time.Minute,
		},
	)
	require.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	secondRecovery, err := prepared.Fixture.Store.GetPRDevelopmentControllerForCase(
		ctx,
		secondCase.ID,
	)
	require.NoError(t, err)
	secondClaim, changed, err := prepared.Fixture.Store.ClaimPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryClaim{
			CaseID:           secondCase.ID,
			AttemptID:        secondAttempt.ID,
			ExpectedRevision: secondRecovery.Revision,
			ClaimID:          "foreign-replacement-authority-claim",
			WorkerLabel:      "foreign-replacement-authority-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	secondRecovered, changed, err := prepared.Fixture.Store.FinalizePRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryFinalize{
			ControllerID:     secondClaim.Controller.ID,
			AttemptID:        secondAttempt.ID,
			RecoveryID:       secondClaim.Intent.ID,
			ExpectedRevision: secondRecovery.Revision,
			ClaimID:          secondClaim.Intent.ClaimID,
			ClaimToken:       secondClaim.Intent.ClaimToken,
			ClaimEpoch:       secondClaim.Intent.ClaimEpoch,
			Rotation: PRDevelopmentControllerRecoveryRotationResult{
				WorkspaceID:   secondClaim.Intent.WorkspaceID,
				Bound:         true,
				Version:       secondClaim.Intent.LineVersion,
				MutationEpoch: secondClaim.Intent.MutationEpoch,
				Tip:           secondClaim.Intent.TipCommit,
				Tree:          secondClaim.Intent.Tree,
				RotationHash:  strings.Repeat("9", 64),
			},
			Lease: time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, PRDevelopmentControllerSuspensionPending, secondRecovered.Phase)
	secondSuspension, found, err := loadPRDevelopmentControllerSuspensionBySource(
		ctx,
		prepared.Fixture.Store.db,
		PRDevelopmentControllerSuspensionSourceControllerRecovery,
		secondClaim.Intent.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	foreignReservationKey := secondSuspension.SuspensionReservationKey
	require.True(t, validPrefixedHexID(foreignReservationKey, prDevelopmentControllerKeyPrefix))

	tampered, found, err := loadPRDevelopmentControllerOperationByID(
		ctx,
		prepared.Fixture.Store.db,
		prepared.Operation.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	tampered.ReplacementReservationKey = foreignReservationKey
	tampered.ReplacementReservationDigest = prDevelopmentMutationReservationDigest(
		foreignReservationKey,
	)
	tampered.RecoveryHash = hashPRDevelopmentOperationRecovery(tampered)
	result, err := prepared.Fixture.Store.db.ExecContext(ctx, `
		UPDATE pr_development_controller_operation_intents
		SET replacement_reservation_key = ?, replacement_reservation_digest = ?,
			recovery_hash = ?
		WHERE id = ?`,
		tampered.ReplacementReservationKey,
		tampered.ReplacementReservationDigest,
		tampered.RecoveryHash,
		tampered.ID,
	)
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	_, err = prepared.Fixture.Store.GetPRDevelopmentControllerForCase(
		ctx,
		prepared.Fixture.Case.ID,
	)
	require.Error(t, err)
	assert.Contains(
		t,
		err.Error(),
		"stored active operation recovery replacement reuses foreign authority",
	)

	stored, found, err := loadPRDevelopmentControllerOperationByID(
		ctx,
		prepared.Fixture.Store.db,
		prepared.Operation.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, tampered.ReplacementReservationKey, stored.ReplacementReservationKey)
	assert.Equal(t, tampered.RecoveryHash, stored.RecoveryHash)
}

func TestStorePRDevelopmentControllerOperationPopulatedV12MigrationCanPark(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "populated-v12-operation-migration.db")
	fixture := newLegacyBoundPRDevelopmentOperationFixture(t, path)
	require.NoError(t, fixture.Store.Close())

	legacy := openSchemaTestDB(t, path)
	_, err := legacy.Exec(`DROP TABLE pr_development_controller_operation_intents`)
	require.NoError(t, err)
	setSchemaTestVersion(t, legacy, 12)
	require.NoError(t, legacy.Close())

	reopened, err := Open(ctx, path, WithClock(func() time.Time { return *fixture.Clock }))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	fixture.Store = reopened

	var operationCount int
	require.NoError(t, reopened.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pr_development_controller_operation_intents`,
	).Scan(&operationCount))
	assert.Zero(t, operationCount, "v13 migration must start without synthetic operations")

	park, _, parked := parkOperationForTest(
		t,
		fixture,
		fixture.Mutation,
		nil,
		fixture.Attempt.Summary,
		fixture.Attempt.Iterations,
		401,
	)
	assert.Zero(t, park.Ordinal)
	require.NotNil(t, parked.Fence)
	assert.True(t, parked.Fence.NoChanges)
	loaded, err := reopened.GetPRDevelopmentControllerForCase(ctx, fixture.Case.ID)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentControllerReviewPending, loaded.Phase)
	storedPark, found, err := loadPRDevelopmentControllerOperationByID(
		ctx,
		reopened.db,
		park.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, PRDevelopmentControllerOperationFinalized, storedPark.Status)
}

func TestStorePRDevelopmentControllerOperationLeaseEpochHeadroomFailsBeforeInsert(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOperationFixture(t, ":memory:")
	maxLeaseEpoch := int64(^uint64(0) >> 1)

	for _, kind := range []PRDevelopmentControllerOperationKind{
		PRDevelopmentControllerOperationAdopt,
		PRDevelopmentControllerOperationResume,
		PRDevelopmentControllerOperationCommit,
	} {
		controller := fixture.Mutation.Controller
		controller.LeaseEpoch = maxLeaseEpoch
		err := requirePRDevelopmentControllerOperationHeadroom(controller, kind)
		require.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
		assert.Contains(t, err.Error(), "operation lacks recovery lease-epoch headroom")
	}

	_, err := fixture.Store.db.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET lease_epoch = ?, claims = ?
		WHERE id = ?`,
		maxLeaseEpoch,
		maxLeaseEpoch,
		fixture.Mutation.Controller.ID,
	)
	require.NoError(t, err)
	request := operationBaseRequest(fixture, fixture.Mutation.Controller)
	request.ExpectedTree = fixture.SourceTree
	_, changed, err := fixture.Store.PreparePRDevelopmentControllerOperation(
		ctx,
		PRDevelopmentControllerOperationPrepare{
			OperationID:      fixture.operationID(),
			ControllerID:     fixture.Mutation.Controller.ID,
			AttemptID:        fixture.Attempt.ID,
			ExpectedRevision: fixture.Mutation.Controller.Revision,
			LeaseToken:       fixture.Mutation.Controller.LeaseToken,
			LeaseEpoch:       maxLeaseEpoch,
			Kind:             PRDevelopmentControllerOperationAdopt,
			Request:          request,
		},
	)
	require.Error(t, err)
	assert.False(t, changed)
	var operationCount int
	require.NoError(t, fixture.Store.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM pr_development_controller_operation_intents
		WHERE controller_id = ?`, fixture.Mutation.Controller.ID,
	).Scan(&operationCount))
	assert.Zero(t, operationCount)
}

func TestStorePRDevelopmentControllerAggregateRejectsFinalizedOperationAboveHighWater(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOperationFixture(t, ":memory:")
	_, transition := adoptOperationForTest(t, fixture, fixture.Mutation)
	operation := transition.Operation
	require.Equal(
		t,
		transition.Controller.Revision,
		operation.FinalControllerRevision,
	)

	tampered, found, err := loadPRDevelopmentControllerOperationByID(
		ctx,
		fixture.Store.db,
		operation.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	tampered.PreparedControllerRevision++
	tampered.FinalControllerRevision++
	require.Greater(
		t,
		tampered.FinalControllerRevision,
		transition.Controller.Revision,
	)
	tampered.IntentHash = hashPRDevelopmentOperationIntent(tampered)
	tampered.FinalHash = hashPRDevelopmentOperationFinal(tampered)
	result, err := fixture.Store.db.ExecContext(ctx, `
		UPDATE pr_development_controller_operation_intents
		SET prepared_controller_revision = ?, intent_hash = ?,
			final_controller_revision = ?, final_hash = ?
		WHERE id = ?`,
		tampered.PreparedControllerRevision,
		tampered.IntentHash,
		tampered.FinalControllerRevision,
		tampered.FinalHash,
		tampered.ID,
	)
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	stored, found, err := loadPRDevelopmentControllerOperationByID(
		ctx,
		fixture.Store.db,
		tampered.ID,
	)
	require.NoError(t, err, "coherently rehashed operation must remain structurally valid")
	require.True(t, found)
	assert.Equal(t, tampered.IntentHash, stored.IntentHash)
	assert.Equal(t, tampered.FinalHash, stored.FinalHash)

	_, err = fixture.Store.GetPRDevelopmentControllerForCase(ctx, fixture.Case.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stored finalized operation exceeds controller high-water")
}
