//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPRDevelopmentPublicationAttentionProjectionLifecycle(t *testing.T) {
	t.Run("linked claim from pending is history but not attention", func(t *testing.T) {
		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		pinned, runID := linkPRDevelopmentPublicationDecisionForAttentionTest(t, &fixture)
		assertPRDevelopmentPublicationAttentionProjectionForTest(
			t,
			&fixture,
			pinned,
			runID,
			PRDevelopmentPublicationClaimed,
			PRDevelopmentPublicationPending,
			true,
			false,
		)
		assertListedPRDevelopmentAttention(t, fixture.Store, pinned.CaseID, false)
	})

	t.Run("released gate wait requires attention", func(t *testing.T) {
		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		pinned, runID := linkPRDevelopmentPublicationDecisionForAttentionTest(t, &fixture)
		releasePRDevelopmentPublicationGateWaitForAttentionTest(t, &fixture, runID)
		assertPRDevelopmentPublicationAttentionProjectionForTest(
			t,
			&fixture,
			pinned,
			runID,
			PRDevelopmentPublicationGateWaiting,
			"",
			true,
			true,
		)
		assertListedPRDevelopmentAttention(t, fixture.Store, pinned.CaseID, true)
	})

	t.Run("claimed gate wait remains attention even after lease expiry", func(t *testing.T) {
		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		pinned, runID := linkPRDevelopmentPublicationDecisionForAttentionTest(t, &fixture)
		due := releasePRDevelopmentPublicationGateWaitForAttentionTest(t, &fixture, runID)
		*fixture.Clock = due
		claimed := claimOnePRDevelopmentPublicationForRequeueTest(
			t,
			&fixture,
			"publication-attention-gate-worker",
		)
		require.Equal(t, PRDevelopmentPublicationGateWaiting, claimed.ClaimFrom)
		assertPRDevelopmentPublicationAttentionProjectionForTest(
			t,
			&fixture,
			pinned,
			runID,
			PRDevelopmentPublicationClaimed,
			PRDevelopmentPublicationGateWaiting,
			true,
			true,
		)
		assertListedPRDevelopmentAttention(t, fixture.Store, pinned.CaseID, true)

		require.NotNil(t, claimed.ClaimUntil)
		*fixture.Clock = claimed.ClaimUntil.Add(time.Second)
		assertPRDevelopmentPublicationAttentionProjectionForTest(
			t,
			&fixture,
			pinned,
			runID,
			PRDevelopmentPublicationClaimed,
			PRDevelopmentPublicationGateWaiting,
			true,
			true,
		)
		assertListedPRDevelopmentAttention(t, fixture.Store, pinned.CaseID, true)
	})

	t.Run("push ready and its claim retain non-actionable history", func(t *testing.T) {
		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		pinned, runID := linkPRDevelopmentPublicationDecisionForAttentionTest(t, &fixture)
		markPRDevelopmentPublicationPushReadyForAttentionTest(t, &fixture, runID)
		assertPRDevelopmentPublicationAttentionProjectionForTest(
			t,
			&fixture,
			pinned,
			runID,
			PRDevelopmentPublicationPushReady,
			"",
			true,
			false,
		)
		assertListedPRDevelopmentAttention(t, fixture.Store, pinned.CaseID, false)

		claimed := claimOnePRDevelopmentPublicationForRequeueTest(
			t,
			&fixture,
			"publication-attention-push-worker",
		)
		require.Equal(t, PRDevelopmentPublicationPushReady, claimed.ClaimFrom)
		assertPRDevelopmentPublicationAttentionProjectionForTest(
			t,
			&fixture,
			pinned,
			runID,
			PRDevelopmentPublicationClaimed,
			PRDevelopmentPublicationPushReady,
			true,
			false,
		)
		assertListedPRDevelopmentAttention(t, fixture.Store, pinned.CaseID, false)
	})

	t.Run("push started and published retain non-actionable history", func(t *testing.T) {
		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		pinned, runID := linkPRDevelopmentPublicationDecisionForAttentionTest(t, &fixture)
		markPRDevelopmentPublicationPushReadyForAttentionTest(t, &fixture, runID)
		pushClaim := claimOnePRDevelopmentPublicationForRequeueTest(
			t,
			&fixture,
			"publication-attention-start-worker",
		)
		*fixture.Clock = fixture.Clock.Add(time.Second)
		started, newlyStarted, err := fixture.Store.StartPRDevelopmentPublicationPush(
			context.Background(),
			PRDevelopmentPublicationPushStart{
				PublicationID: pushClaim.ID,
				ClaimToken:    pushClaim.ClaimToken,
				ClaimEpoch:    pushClaim.ClaimEpoch,
				Observation:   fixture.Observation,
				ObservedAt:    *fixture.Clock,
				Request: publicationPushRequestFor(
					pushClaim,
					fixture.Observation.HeadSHA,
				),
			},
		)
		require.NoError(t, err)
		require.True(t, newlyStarted)
		assertPRDevelopmentPublicationAttentionProjectionForTest(
			t,
			&fixture,
			pinned,
			runID,
			PRDevelopmentPublicationPushStarted,
			PRDevelopmentPublicationPushReady,
			true,
			false,
		)
		assertListedPRDevelopmentAttention(t, fixture.Store, pinned.CaseID, false)

		disposition := PRDevelopmentPublicationPushApplied
		if started.ExpectedRemoteTip == started.TipCommit {
			disposition = PRDevelopmentPublicationPushAlreadyCurrent
		}
		published, finalized, err := fixture.Store.FinalizePRDevelopmentPublicationPush(
			context.Background(),
			PRDevelopmentPublicationPushFinalize{
				PublicationID: started.ID,
				ClaimToken:    pushClaim.ClaimToken,
				ClaimEpoch:    pushClaim.ClaimEpoch,
				RequestHash:   started.PushRequestHash,
				Status:        PRDevelopmentPublicationPublished,
				Result: publicationPushResultFor(
					started,
					disposition,
					true,
				),
			},
		)
		require.NoError(t, err)
		require.True(t, finalized)
		assertPRDevelopmentPublicationAttentionProjectionForTest(
			t,
			&fixture,
			pinned,
			runID,
			PRDevelopmentPublicationPublished,
			"",
			true,
			false,
		)
		assertListedPRDevelopmentAttention(t, fixture.Store, published.CaseID, false)
	})
}

func TestPRDevelopmentPublicationAttentionProjectionIsPrivateDetachedAndStable(
	t *testing.T,
) {
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	pinned, runID := linkPRDevelopmentPublicationDecisionForAttentionTest(t, &fixture)

	snapshot, err := fixture.Store.GetCurrentPRDevelopmentAttentionTriggerForCase(
		context.Background(),
		pinned.CaseID,
	)
	require.NoError(t, err)
	require.NotNil(t, snapshot.Publication)
	originalPolicy := append(json.RawMessage(nil), snapshot.Publication.PinnedPolicy...)
	encoded, err := json.Marshal(snapshot.Publication)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(encoded))
	encoded, err = json.Marshal(snapshot)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(encoded))

	snapshot.Publication.PinnedPolicy[0] = '['
	conversation, err := fixture.Store.AppendPRDevelopmentMessage(
		context.Background(),
		PRDevelopmentMessageAppend{
			CaseID:          pinned.CaseID,
			ExpectedVersion: snapshot.ConversationVersion,
			Role:            PRDevelopmentMessageUser,
			Content:         "Keep the linked publication decision on its original subject.",
		},
	)
	require.NoError(t, err)

	after, err := fixture.Store.GetCurrentPRDevelopmentAttentionTriggerForCase(
		context.Background(),
		pinned.CaseID,
	)
	require.NoError(t, err)
	require.NotNil(t, after.Publication)
	assert.Equal(t, conversation.Version, after.ConversationVersion)
	assert.Equal(t, originalPolicy, after.Publication.PinnedPolicy)
	assert.Equal(t, publicationPRDevelopmentDecisionKey(pinned), after.Publication.DecisionRun.Key)
	assert.Equal(t, runID, after.Publication.DecisionRun.RunID)
	assert.True(t, after.PublicationCurrent)
	assert.False(t, after.PublicationAttentionRequired)
}

func TestPRDevelopmentPublicationAttentionProjectionOmitsUnlinkedPublication(
	t *testing.T,
) {
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	snapshot, err := fixture.Store.GetCurrentPRDevelopmentAttentionTriggerForCase(
		context.Background(),
		fixture.Publication.CaseID,
	)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentLedgerReviewPassed, snapshot.CurrentReviewOutcome)
	assert.Nil(t, snapshot.Trigger, "passed review has no local attention trigger")
	assert.Nil(t, snapshot.Publication)
	assert.False(t, snapshot.PublicationCurrent)
	assert.False(t, snapshot.PublicationAttentionRequired)
	assertListedPRDevelopmentAttention(t, fixture.Store, fixture.Publication.CaseID, false)
}

func TestPRDevelopmentPublicationAttentionProjectionSurvivesHistoricalLocalTrigger(
	t *testing.T,
) {
	fixture, attention := newPRDevelopmentAttentionOrchestrationFixtureAt(t, ":memory:")
	ctx := context.Background()
	workbench, err := fixture.Operation.Store.GetPRDevelopmentWorkbench(
		ctx,
		attention.Case.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, workbench.RepairSession)
	workbench, created, err := fixture.Operation.Store.AdmitPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairAdmit{
			CaseID:                      attention.Case.ID,
			ExpectedConversationVersion: workbench.Conversation.Version,
			ExpectedRepairVersion:       workbench.RepairSession.Version,
			IdempotencyKey:              "publication-after-historical-attention",
			AgentID:                     fixture.Operation.Session.AgentID,
			Instruction:                 "Resolve the prior attention request locally.",
		},
	)
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, workbench.RepairSession)
	fixture.Operation.Session = *workbench.RepairSession
	fixture.Operation.Attempt = fixture.Operation.Session.Attempts[len(fixture.Operation.Session.Attempts)-1]

	run, claimed, err := fixture.Operation.Store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{
			WorkerLabel: "publication-after-attention-worker",
			Lease:       time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, fixture.Operation.Attempt.ID, run.AttemptID)
	run, changed, err := fixture.Operation.Store.PinPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationPin{
			AttemptID:      run.AttemptID,
			ClaimToken:     run.ClaimToken,
			HeadRepository: fixture.Operation.Session.HeadRepository,
			HeadRef:        fixture.Operation.Session.HeadRef,
			HeadSHA:        fixture.Operation.Session.HeadSHA,
			CloneURL:       fixture.Operation.Session.CloneURL,
			ReviewDigest:   fixture.Operation.Session.ReviewDigest,
			WorkspaceID:    fixture.Operation.Session.WorkspaceID,
			SourceTree:     attention.Controller.SourceTree,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	lease, acquired, err := fixture.Operation.Store.
		AcquirePRDevelopmentRepairOrchestrationController(
			ctx,
			PRDevelopmentRepairOrchestrationControllerAcquire{
				CaseID:           attention.Case.ID,
				AttemptID:        run.AttemptID,
				ClaimToken:       run.ClaimToken,
				ExpectedRevision: attention.Controller.Revision,
				WorkerLabel:      "publication-after-attention-worker",
				Lease:            time.Minute,
			},
		)
	require.NoError(t, err)
	require.True(t, acquired)
	fixture.Operation.Mutation = lease
	fixture.Operation.SourceTree = attention.Controller.SourceTree
	resumeRequest := operationBaseRequest(fixture.Operation, lease.Controller)
	resumeRequest.ExpectedVersion = lease.Controller.LineVersion
	resumeRequest.ExpectedEpoch = lease.Controller.MutationEpoch
	resumeRequest.ExpectedTip = lease.Controller.TipCommit
	resumeRequest.ExpectedTree = lease.Controller.Tree
	resume := prepareOperationForTest(
		t,
		fixture.Operation,
		lease,
		PRDevelopmentControllerOperationResume,
		fixture.Operation.operationID(),
		resumeRequest,
	)
	resumed := finalizeOperationForTest(
		t,
		fixture.Operation,
		lease,
		resume,
		PRDevelopmentControllerOperationResult{
			WorkspaceID:   fixture.Operation.Session.WorkspaceID,
			Version:       lease.Controller.LineVersion,
			MutationEpoch: lease.Controller.MutationEpoch + 1,
			Tip:           lease.Controller.TipCommit,
			Tree:          lease.Controller.Tree,
		},
	)
	fixture.Run = run
	fixture.Lease = operationLeaseFromTransition(resumed)
	run = startAndCompletePRDevelopmentOrchestrationForTest(t, fixture)
	validation := validPRDevelopmentOrchestrationValidationForTest(fixture)
	validation.CIStatus = PRDevelopmentCIPassed
	_, changed, err = fixture.Operation.Store.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx,
		validation,
	)
	require.NoError(t, err)
	require.True(t, changed)
	_, _, parked := parkOperationForTest(
		t,
		fixture.Operation,
		fixture.Lease,
		nil,
		run.Summary,
		run.Iterations,
		9602,
	)
	require.NotNil(t, parked.Fence)
	reviewLease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
	completion, changed, err := fixture.Operation.Store.CompletePRDevelopmentReview(
		ctx,
		validPRDevelopmentAIReviewCompletionForTest(
			reviewLease,
			PRDevelopmentLedgerReviewPassed,
		),
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, completion.Publication)
	publicationFixture := claimPRDevelopmentPublicationAttentionLifecycleForTest(
		t,
		fixture,
		*completion.Publication,
	)
	pinned, runID := linkPRDevelopmentPublicationDecisionForAttentionTest(
		t,
		&publicationFixture,
	)
	releasePRDevelopmentPublicationGateWaitForAttentionTest(
		t,
		&publicationFixture,
		runID,
	)

	snapshot, err := fixture.Operation.Store.GetCurrentPRDevelopmentAttentionTriggerForCase(
		ctx,
		attention.Case.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, snapshot.Trigger)
	assert.False(t, snapshot.TriggerCurrent)
	require.NotNil(t, snapshot.Publication)
	assert.Equal(t, publicationPRDevelopmentDecisionKey(pinned), snapshot.Publication.DecisionRun.Key)
	assert.True(t, snapshot.PublicationCurrent)
	assert.True(t, snapshot.PublicationAttentionRequired)
	assertListedPRDevelopmentAttention(t, fixture.Operation.Store, attention.Case.ID, true)
}

func TestPRDevelopmentPublicationAttentionProjectionSupersessionAndCorruption(
	t *testing.T,
) {
	t.Run("later repair supersedes linked publication", func(t *testing.T) {
		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		pinned, _ := linkPRDevelopmentPublicationDecisionForAttentionTest(t, &fixture)
		workbench, err := fixture.Store.GetPRDevelopmentWorkbench(
			context.Background(),
			pinned.CaseID,
		)
		require.NoError(t, err)
		require.NotNil(t, workbench.RepairSession)
		_, created, err := fixture.Store.AdmitPRDevelopmentRepair(
			context.Background(),
			PRDevelopmentRepairAdmit{
				CaseID:                      pinned.CaseID,
				ExpectedConversationVersion: workbench.Conversation.Version,
				ExpectedRepairVersion:       workbench.RepairSession.Version,
				IdempotencyKey:              "publication-attention-superseding-repair",
				AgentID:                     fixture.Orchestration.Operation.Session.AgentID,
				Instruction:                 "Continue with a newer local candidate.",
			},
		)
		require.NoError(t, err)
		require.True(t, created)

		snapshot, err := fixture.Store.GetCurrentPRDevelopmentAttentionTriggerForCase(
			context.Background(),
			pinned.CaseID,
		)
		require.NoError(t, err)
		require.NotNil(t, snapshot.Publication)
		assert.False(t, snapshot.PublicationCurrent)
		assert.False(t, snapshot.PublicationAttentionRequired)
		assertListedPRDevelopmentAttention(t, fixture.Store, pinned.CaseID, false)
	})

	t.Run("new provider occurrence supersedes linked publication", func(t *testing.T) {
		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		pinned, _ := linkPRDevelopmentPublicationDecisionForAttentionTest(t, &fixture)
		input := fixture.Orchestration.Operation.Case.PRDevelopmentCaptureInput
		input.PRDevelopmentCaptureIdentity = addPRDevelopmentDispatch(
			t,
			fixture.Store,
			"delivery-publication-attention-newer-provider-case",
			input.WorkflowRef,
			input.WorkflowRevision,
		)
		input.ReviewID = "902"
		input.TriggerReviewNodeID = "PRR_kwDOReview902"
		input.ReviewURL = strings.Split(input.ReviewURL, "#")[0] +
			"#pullrequestreview-902"
		input.Feedback = "A newer independently submitted review supersedes publication."
		input.ReviewSubmittedAt = input.ReviewSubmittedAt.Add(time.Minute)
		*fixture.Clock = fixture.Clock.Add(time.Minute)
		_, created, err := fixture.Store.CapturePRDevelopmentCase(
			context.Background(),
			validPRDevelopmentRequestForTest(input),
		)
		require.NoError(t, err)
		require.True(t, created)

		snapshot, err := fixture.Store.GetCurrentPRDevelopmentAttentionTriggerForCase(
			context.Background(),
			pinned.CaseID,
		)
		require.NoError(t, err)
		require.NotNil(t, snapshot.Publication)
		assert.False(t, snapshot.PublicationCurrent)
		assert.False(t, snapshot.PublicationAttentionRequired)
		assertListedPRDevelopmentAttention(t, fixture.Store, pinned.CaseID, false)
	})

	t.Run("changed controller high water fails detail and list closed", func(t *testing.T) {
		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		pinned, runID := linkPRDevelopmentPublicationDecisionForAttentionTest(t, &fixture)
		releasePRDevelopmentPublicationGateWaitForAttentionTest(t, &fixture, runID)
		assertListedPRDevelopmentAttention(t, fixture.Store, pinned.CaseID, true)
		result, err := fixture.Store.db.Exec(`
			UPDATE pr_development_thread_controllers
			SET revision = revision + 1
			WHERE id = ?`, pinned.ControllerID)
		require.NoError(t, err)
		rows, err := result.RowsAffected()
		require.NoError(t, err)
		require.Equal(t, int64(1), rows)

		_, err = fixture.Store.GetCurrentPRDevelopmentAttentionTriggerForCase(
			context.Background(),
			pinned.CaseID,
		)
		assert.Error(t, err)
		assertListedPRDevelopmentAttention(t, fixture.Store, pinned.CaseID, false)
	})
}

func linkPRDevelopmentPublicationDecisionForAttentionTest(
	t *testing.T,
	fixture *prDevelopmentPublicationLifecycleFixture,
) (PRDevelopmentPublication, string) {
	t.Helper()
	pinned := pinPRDevelopmentPublicationForTest(t, fixture)
	runID := "wr_" + strings.Repeat("a", 32)
	link, existed, err := fixture.Store.AdmitPRDevelopmentPublicationDecisionRun(
		context.Background(),
		PRDevelopmentPublicationDecisionRunAdmission{
			Key:        publicationPRDevelopmentDecisionKey(pinned),
			RunID:      runID,
			ClaimToken: fixture.Claim.ClaimToken,
			ClaimEpoch: fixture.Claim.ClaimEpoch,
		},
		func(context.Context) error { return nil },
	)
	require.NoError(t, err)
	require.False(t, existed)
	require.Equal(t, runID, link.RunID)
	return pinned, runID
}

func claimPRDevelopmentPublicationAttentionLifecycleForTest(
	t *testing.T,
	fixture *prDevelopmentOrchestrationFixture,
	publication PRDevelopmentPublication,
) prDevelopmentPublicationLifecycleFixture {
	t.Helper()
	claimed, err := fixture.Operation.Store.ClaimPRDevelopmentPublications(
		context.Background(),
		PRDevelopmentPublicationClaimRequest{
			WorkerLabel: "publication-after-attention-gate-worker",
			Limit:       1,
			Lease:       5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, publication.ID, claimed[0].ID)
	return prDevelopmentPublicationLifecycleFixture{
		Orchestration: fixture,
		Store:         fixture.Operation.Store,
		Clock:         fixture.Operation.Clock,
		Publication:   publication,
		Claim:         claimed[0],
		Observation: PRDevelopmentPublicationProviderObservation{
			Repository:         fixture.Operation.Case.Repository,
			PullNumber:         fixture.Operation.Case.PullNumber,
			HeadRepository:     fixture.Operation.Case.HeadRepository,
			HeadRef:            publication.SourceRef,
			HeadSHA:            publication.SourceCommit,
			HeadCloneURL:       publication.SourceCloneURL,
			CurrentReviewState: fixture.Operation.Case.CurrentReviewState,
			ReviewDigest:       fixture.Run.ReviewDigest,
		},
	}
}

func releasePRDevelopmentPublicationGateWaitForAttentionTest(
	t *testing.T,
	fixture *prDevelopmentPublicationLifecycleFixture,
	runID string,
) time.Time {
	t.Helper()
	due := fixture.Clock.Add(time.Second)
	waiting, changed, err := fixture.Store.ReleasePRDevelopmentPublicationGateWait(
		context.Background(),
		PRDevelopmentPublicationGateWait{
			PublicationID: fixture.Claim.ID,
			ClaimToken:    fixture.Claim.ClaimToken,
			ClaimEpoch:    fixture.Claim.ClaimEpoch,
			DecisionRunID: runID,
			AvailableAt:   due,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, PRDevelopmentPublicationGateWaiting, waiting.Status)
	return due
}

func markPRDevelopmentPublicationPushReadyForAttentionTest(
	t *testing.T,
	fixture *prDevelopmentPublicationLifecycleFixture,
	runID string,
) {
	t.Helper()
	ready, changed, err := fixture.Store.MarkPRDevelopmentPublicationPushReady(
		context.Background(),
		PRDevelopmentPublicationMarkPushReady{
			PublicationID: fixture.Claim.ID,
			ClaimToken:    fixture.Claim.ClaimToken,
			ClaimEpoch:    fixture.Claim.ClaimEpoch,
			DecisionRunID: runID,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, PRDevelopmentPublicationPushReady, ready.Status)
}

func assertPRDevelopmentPublicationAttentionProjectionForTest(
	t *testing.T,
	fixture *prDevelopmentPublicationLifecycleFixture,
	pinned PRDevelopmentPublication,
	runID string,
	status PRDevelopmentPublicationStatus,
	claimFrom PRDevelopmentPublicationStatus,
	current bool,
	attentionRequired bool,
) {
	t.Helper()
	snapshot, err := fixture.Store.GetCurrentPRDevelopmentAttentionTriggerForCase(
		context.Background(),
		pinned.CaseID,
	)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentLedgerReviewPassed, snapshot.CurrentReviewOutcome)
	assert.False(t, snapshot.AttentionRequired)
	assert.Nil(t, snapshot.Trigger)
	assert.False(t, snapshot.TriggerCurrent)
	require.NotNil(t, snapshot.Publication)
	assert.Equal(t, pinned.CaseID, snapshot.Publication.CaseID)
	assert.Equal(t, publicationPRDevelopmentDecisionKey(pinned), snapshot.Publication.DecisionRun.Key)
	assert.Equal(t, runID, snapshot.Publication.DecisionRun.RunID)
	assert.Equal(t, pinned.PinnedPolicy, snapshot.Publication.PinnedPolicy)
	assert.Equal(t, status, snapshot.Publication.Status)
	assert.Equal(t, claimFrom, snapshot.Publication.ClaimFrom)
	assert.Equal(t, current, snapshot.PublicationCurrent)
	assert.Equal(t, attentionRequired, snapshot.PublicationAttentionRequired)
}
