//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorePRDevelopmentReviewClaimAndExpiredReclaimAreReservationFree(t *testing.T) {
	t.Parallel()

	fixture, _ := newCompletedPRDevelopmentAIReviewFixture(t, PRDevelopmentCIPassed)
	ctx := context.Background()
	first, claimed, err := fixture.Operation.Store.ClaimPRDevelopmentReview(
		ctx,
		PRDevelopmentReviewClaimRequest{
			WorkerLabel: "review-worker-a",
			Lease:       time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, fixture.Operation.Case.ID, first.CaseID)
	assert.Equal(t, fixture.Run.AttemptID, first.Fence.AttemptID)
	assert.Equal(t, PRDevelopmentControllerReview, first.Controller.Phase)
	assert.Equal(t, PRDevelopmentControllerReviewLease, first.Controller.LeaseKind)
	assert.Empty(t, first.Controller.MutationReservationKey)
	assert.False(t, first.Reclaimed)

	_, claimed, err = fixture.Operation.Store.ClaimPRDevelopmentReview(
		ctx,
		PRDevelopmentReviewClaimRequest{
			WorkerLabel: "review-worker-b",
			Lease:       time.Minute,
		},
	)
	require.NoError(t, err)
	assert.False(t, claimed, "a live review lease is not transferable")

	require.NotNil(t, first.Controller.LeaseUntil)
	*fixture.Operation.Clock = first.Controller.LeaseUntil.Add(time.Second)
	reclaimed, claimed, err := fixture.Operation.Store.ClaimPRDevelopmentReview(
		ctx,
		PRDevelopmentReviewClaimRequest{
			WorkerLabel: "review-worker-b",
			Lease:       2 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.True(t, reclaimed.Reclaimed)
	assert.NotEqual(t, first.Controller.LeaseToken, reclaimed.Controller.LeaseToken)
	assert.Equal(t, first.Controller.LeaseEpoch+1, reclaimed.Controller.LeaseEpoch)
	assert.Empty(t, reclaimed.Controller.MutationReservationKey)

	err = fixture.Operation.Store.RenewPRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerRenew{
			ControllerID: first.Controller.ID,
			AttemptID:    first.Fence.AttemptID,
			LeaseToken:   first.Controller.LeaseToken,
			LeaseEpoch:   first.Controller.LeaseEpoch,
			Lease:        time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
}

func TestStorePRDevelopmentReviewClaimRejectsNonOrchestratedPark(t *testing.T) {
	t.Parallel()

	fixture := newParkedPRDevelopmentLedgerFixtureForTest(t, ":memory:")
	_, changed, err := fixture.Store.AppendPRDevelopmentLedgerAttempt(
		context.Background(),
		validPRDevelopmentLedgerAttemptAppendForTest(
			fixture.DevelopmentCase.ID,
			fixture.Attempt.ID,
		),
	)
	require.NoError(t, err)
	require.True(t, changed)
	_, claimed, err := fixture.Store.ClaimPRDevelopmentReview(
		context.Background(),
		PRDevelopmentReviewClaimRequest{
			WorkerLabel: "review-worker",
			Lease:       time.Minute,
		},
	)
	require.NoError(t, err)
	assert.False(t, claimed)
}

func TestStorePRDevelopmentReviewClaimOrdersTwoCandidatesAndSkipsLiveOldest(t *testing.T) {
	t.Parallel()

	first, _ := newCompletedPRDevelopmentAIReviewFixture(t, PRDevelopmentCIPassed)
	*first.Operation.Clock = first.Operation.Clock.Add(time.Minute)
	secondInput := validPRDevelopmentInputForTest()
	secondInput.PRDevelopmentCaptureIdentity = addPRDevelopmentDispatch(
		t,
		first.Operation.Store,
		"delivery-development-review-second",
		"workflows/own-pr-feedback.yml",
		"revision-2026-08-09",
	)
	secondInput.Repository = "acme/second-project"
	secondInput.BaseRepository = "Acme/Second-Project"
	secondInput.PullNumber = 43
	secondInput.PullURL = "https://github.com/acme/second-project/pull/43"
	secondInput.ReviewID = "502"
	secondInput.TriggerReviewNodeID = "PRR_kwDOReview502"
	secondInput.ReviewURL = "https://github.com/acme/second-project/pull/43#pullrequestreview-502"
	second := newPRDevelopmentAIReviewOrchestrationOnStore(
		t,
		first.Operation.Store,
		first.Operation.Clock,
		secondInput,
		"orchestration-attempt-second",
		"gw-orchestration-line-second",
		1001,
	)
	completePRDevelopmentAIReviewFixture(t, second, PRDevelopmentCIPassed, 2902)

	ctx := context.Background()
	oldest, claimed, err := first.Operation.Store.ClaimPRDevelopmentReview(
		ctx,
		PRDevelopmentReviewClaimRequest{
			WorkerLabel: "ordered-review-worker-a",
			Lease:       5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, first.Run.AttemptID, oldest.Fence.AttemptID)

	next, claimed, err := first.Operation.Store.ClaimPRDevelopmentReview(
		ctx,
		PRDevelopmentReviewClaimRequest{
			WorkerLabel: "ordered-review-worker-b",
			Lease:       5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, second.Run.AttemptID, next.Fence.AttemptID)
	assert.NotEqual(t, oldest.Controller.ID, next.Controller.ID)
}

func TestStoreCompletePRDevelopmentReviewAtomicallyEnqueuesChangesRetry(t *testing.T) {
	t.Parallel()

	fixture, _ := newCompletedPRDevelopmentAIReviewFixture(t, PRDevelopmentCIPassed)
	ctx := context.Background()
	lease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
	conversation, err := fixture.Operation.Store.AppendPRDevelopmentMessage(
		ctx,
		PRDevelopmentMessageAppend{
			CaseID:          lease.CaseID,
			ExpectedVersion: 0,
			Role:            PRDevelopmentMessageUser,
			Content:         "Preserve the narrow compatibility boundary in the next repair.",
		},
	)
	require.NoError(t, err)
	input := validPRDevelopmentAIReviewCompletionForTest(
		lease,
		PRDevelopmentLedgerReviewChangesRequired,
	)
	completion, changed, err := fixture.Operation.Store.CompletePRDevelopmentReview(
		ctx,
		input,
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, PRDevelopmentControllerReady, completion.Controller.Phase)
	assert.Empty(t, completion.Controller.LeaseToken)
	assert.Empty(t, completion.Controller.MutationReservationKey)
	assert.Equal(t, PRDevelopmentLedgerReviewChangesRequired, completion.Entry.ReviewOutcome)
	require.NotNil(t, completion.NextAttempt)
	assert.Equal(t, 1, completion.NextAttempt.Ordinal)
	assert.Equal(t, PRDevelopmentRepairQueued, completion.NextAttempt.Status)
	assert.Equal(t, conversation.Version, completion.NextAttempt.ConversationVersion)
	assert.Equal(t, prDevelopmentReviewRetryInstruction, completion.NextAttempt.Instruction)
	assert.Equal(
		t,
		normalizePRDevelopmentReviewRetryIdentity(lease.Fence.AttemptID),
		completion.NextAttempt.IdempotencyKey,
	)

	workbench, err := fixture.Operation.Store.GetPRDevelopmentWorkbench(ctx, lease.CaseID)
	require.NoError(t, err)
	require.NotNil(t, workbench.RepairSession)
	require.Len(t, workbench.RepairSession.Attempts, 2)
	assert.Equal(t, completion.NextAttempt.ID, workbench.RepairSession.Attempts[1].ID)
	ledger, err := fixture.Operation.Store.GetPRDevelopmentLedgerForCase(ctx, lease.CaseID)
	require.NoError(t, err)
	require.Len(t, ledger.Entries, 2)
	assert.Equal(t, completion.Entry.ID, ledger.Entries[1].ID)

	*fixture.Operation.Clock = fixture.Operation.Clock.Add(-time.Hour)
	replayed, changed, err := fixture.Operation.Store.CompletePRDevelopmentReview(ctx, input)
	require.NoError(t, err, "exact replay must not consult a regressed clock")
	assert.False(t, changed)
	assert.Equal(t, completion.Entry, replayed.Entry)
	assert.Equal(t, completion.Controller, replayed.Controller)
	require.NotNil(t, replayed.NextAttempt)
	assert.Equal(t, completion.NextAttempt.ID, replayed.NextAttempt.ID)
}

func TestStoreCompletePRDevelopmentReviewRetryInsertRollbackPreservesLease(t *testing.T) {
	t.Parallel()

	fixture, _ := newCompletedPRDevelopmentAIReviewFixture(t, PRDevelopmentCIPassed)
	ctx := context.Background()
	lease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
	input := validPRDevelopmentAIReviewCompletionForTest(
		lease,
		PRDevelopmentLedgerReviewChangesRequired,
	)
	_, err := fixture.Operation.Store.db.Exec(`
		CREATE TRIGGER abort_pr_development_review_retry
		BEFORE INSERT ON pr_development_repair_attempts
		WHEN NEW.idempotency_key LIKE 'ai-review-changes:%'
		BEGIN
			SELECT RAISE(ABORT, 'forced review retry failure');
		END`)
	require.NoError(t, err)
	_, changed, err := fixture.Operation.Store.CompletePRDevelopmentReview(ctx, input)
	require.Error(t, err)
	assert.False(t, changed)

	controller, found, err := loadPRDevelopmentControllerAggregateByID(
		ctx,
		fixture.Operation.Store.db,
		lease.Controller.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, PRDevelopmentControllerReview, controller.Phase)
	assert.Equal(t, lease.Controller.LeaseToken, controller.LeaseToken)
	fence, found, err := loadPRDevelopmentReviewFenceByAttempt(
		ctx,
		fixture.Operation.Store.db,
		lease.Fence.AttemptID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Nil(t, fence.ReviewedAt)
	ledger, err := fixture.Operation.Store.GetPRDevelopmentLedgerForCase(ctx, lease.CaseID)
	require.NoError(t, err)
	require.Len(t, ledger.Entries, 1)

	_, err = fixture.Operation.Store.db.Exec(`DROP TRIGGER abort_pr_development_review_retry`)
	require.NoError(t, err)
	completion, changed, err := fixture.Operation.Store.CompletePRDevelopmentReview(ctx, input)
	require.NoError(t, err)
	assert.True(t, changed)
	require.NotNil(t, completion.NextAttempt)
}

func TestStoreCompletePRDevelopmentReviewCapacityCanBeRemappedToAttention(t *testing.T) {
	t.Parallel()

	fixture, _ := newCompletedPRDevelopmentAIReviewFixture(t, PRDevelopmentCIPassed)
	ctx := context.Background()
	lease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
	_, err := fixture.Operation.Store.db.Exec(`
		UPDATE pr_development_repair_sessions SET version = ? WHERE id = ?`,
		maxPRDevelopmentRepairVersionBeforeAdmission(true)+1,
		lease.Controller.OwnerSessionID,
	)
	require.NoError(t, err)
	changes := validPRDevelopmentAIReviewCompletionForTest(
		lease,
		PRDevelopmentLedgerReviewChangesRequired,
	)
	_, changed, err := fixture.Operation.Store.CompletePRDevelopmentReview(ctx, changes)
	assert.ErrorIs(t, err, ErrPRDevelopmentRepairCapacity)
	assert.False(t, changed)

	attention := validPRDevelopmentAIReviewCompletionForTest(
		lease,
		PRDevelopmentLedgerReviewAttentionRequired,
	)
	completion, changed, err := fixture.Operation.Store.CompletePRDevelopmentReview(
		ctx,
		attention,
	)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, PRDevelopmentLedgerReviewAttentionRequired, completion.Entry.ReviewOutcome)
	assert.Nil(t, completion.NextAttempt)
}

func TestStoreCompletePRDevelopmentReviewPassedRequiresPassedCI(t *testing.T) {
	t.Parallel()

	fixture, _ := newCompletedPRDevelopmentAIReviewFixture(
		t,
		PRDevelopmentCIEnvironmentUnavailable,
	)
	ctx := context.Background()
	lease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
	passed := validPRDevelopmentAIReviewCompletionForTest(
		lease,
		PRDevelopmentLedgerReviewPassed,
	)
	_, changed, err := fixture.Operation.Store.CompletePRDevelopmentReview(ctx, passed)
	assert.ErrorIs(t, err, ErrPRDevelopmentLedgerConflict)
	assert.False(t, changed)

	attention := validPRDevelopmentAIReviewCompletionForTest(
		lease,
		PRDevelopmentLedgerReviewAttentionRequired,
	)
	completion, changed, err := fixture.Operation.Store.CompletePRDevelopmentReview(
		ctx,
		attention,
	)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Nil(t, completion.NextAttempt)
	assert.Equal(t, PRDevelopmentControllerReady, completion.Controller.Phase)
}

func newCompletedPRDevelopmentAIReviewFixture(
	t *testing.T,
	ciStatus PRDevelopmentCIStatus,
) (*prDevelopmentOrchestrationFixture, PRDevelopmentControllerOperationTransition) {
	t.Helper()
	fixture := newPRDevelopmentOrchestrationFixture(t)
	transition := completePRDevelopmentAIReviewFixture(t, fixture, ciStatus, 1901)
	return fixture, transition
}

func completePRDevelopmentAIReviewFixture(
	t *testing.T,
	fixture *prDevelopmentOrchestrationFixture,
	ciStatus PRDevelopmentCIStatus,
	parkOrdinal int,
) PRDevelopmentControllerOperationTransition {
	t.Helper()
	ctx := context.Background()
	run := startAndCompletePRDevelopmentOrchestrationForTest(t, fixture)
	validation := validPRDevelopmentOrchestrationValidationForTest(fixture)
	validation.CIStatus = ciStatus
	_, changed, err := fixture.Operation.Store.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx,
		validation,
	)
	require.NoError(t, err)
	require.True(t, changed)
	_, _, transition := parkOperationForTest(
		t,
		fixture.Operation,
		fixture.Lease,
		nil,
		run.Summary,
		run.Iterations,
		parkOrdinal,
	)
	require.NotNil(t, transition.Fence)
	return transition
}

func newPRDevelopmentAIReviewOrchestrationOnStore(
	t *testing.T,
	store *Store,
	clock *time.Time,
	capture PRDevelopmentCaptureInput,
	idempotencyKey, workspaceID string,
	nextOperationID int,
) *prDevelopmentOrchestrationFixture {
	t.Helper()
	ctx := context.Background()
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	workbench := admitPRDevelopmentRepairForTest(
		t,
		store,
		developmentCase.ID,
		idempotencyKey,
		0,
	)
	require.NotNil(t, workbench.RepairSession)
	attempt := workbench.RepairSession.Attempts[0]
	run, claimed, err := store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{
			WorkerLabel: "orchestration-worker-second",
			Lease:       5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, attempt.ID, run.AttemptID)
	pin := validPRDevelopmentRepairPinForTest(&attempt)
	run, changed, err := store.PinPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationPin{
			AttemptID:      run.AttemptID,
			ClaimToken:     run.ClaimToken,
			HeadRepository: pin.HeadRepository,
			HeadRef:        pin.HeadRef,
			HeadSHA:        pin.HeadSHA,
			CloneURL:       pin.CloneURL,
			ReviewDigest:   pin.ReviewDigest,
			WorkspaceID:    workspaceID,
			SourceTree:     strings.Repeat("b", 40),
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	lease, acquired, err := store.AcquirePRDevelopmentRepairOrchestrationController(
		ctx,
		PRDevelopmentRepairOrchestrationControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        run.AttemptID,
			ClaimToken:       run.ClaimToken,
			ExpectedRevision: 0,
			WorkerLabel:      "orchestration-worker-second",
			Lease:            5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	workbench, err = store.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.NotNil(t, workbench.RepairSession)
	operationFixture := &prDevelopmentOperationFixture{
		Store:      store,
		Clock:      clock,
		Case:       developmentCase,
		Session:    *workbench.RepairSession,
		Attempt:    workbench.RepairSession.Attempts[0],
		Mutation:   lease,
		SourceTree: run.SourceTree,
		NextID:     nextOperationID,
	}
	_, adopted := adoptOperationForTest(t, operationFixture, lease)
	lease = operationLeaseFromTransition(adopted)
	return &prDevelopmentOrchestrationFixture{
		Operation: operationFixture,
		Run:       run,
		Lease:     lease,
	}
}

func claimCompletedPRDevelopmentAIReviewFixture(
	t *testing.T,
	fixture *prDevelopmentOrchestrationFixture,
) PRDevelopmentReviewLease {
	t.Helper()
	lease, claimed, err := fixture.Operation.Store.ClaimPRDevelopmentReview(
		context.Background(),
		PRDevelopmentReviewClaimRequest{
			WorkerLabel: "ai-review-worker",
			Lease:       5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	return lease
}

func validPRDevelopmentAIReviewCompletionForTest(
	lease PRDevelopmentReviewLease,
	outcome PRDevelopmentLedgerReviewOutcome,
) PRDevelopmentLedgerReviewAppend {
	input := PRDevelopmentLedgerReviewAppend{
		CaseID:           lease.CaseID,
		AttemptID:        lease.Fence.AttemptID,
		ControllerID:     lease.Controller.ID,
		ExpectedRevision: lease.Controller.Revision,
		LeaseToken:       lease.Controller.LeaseToken,
		LeaseEpoch:       lease.Controller.LeaseEpoch,
		Summary:          "The immutable local candidate was reviewed against its repair context.",
		Outcome:          outcome,
	}
	if outcome == PRDevelopmentLedgerReviewChangesRequired {
		line := 41
		input.Findings = []PRDevelopmentLedgerReviewFinding{{
			Severity:       ReviewSeverityHigh,
			Title:          "Preserve the exact controller fence",
			File:           "pkg/eventing/pr_development_review_store_sqlite.go",
			Line:           &line,
			Message:        "The candidate needs one additional bounded repair.",
			Evidence:       strings.Repeat("e", 32),
			Impact:         "A stale review could schedule work for different code.",
			Recommendation: "Keep the next attempt bound to this review ledger entry.",
			Validation:     "Run the focused eventing store tests.",
		}}
	}
	return input
}
