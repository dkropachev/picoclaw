//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPRDevelopmentAttentionTriggerTypesAreJSONPrivate(t *testing.T) {
	values := []any{
		PRDevelopmentAttentionTrigger{
			ReviewEntryID:   "pdle_secret",
			LeaseToken:      "lease-secret",
			PinnedPolicy:    json.RawMessage(`{"secret":true}`),
			SubjectRevision: "sha256:secret",
			RunID:           "wr_secret",
		},
		PRDevelopmentAttentionTriggerCaseSnapshot{
			CaseID:  "pdc_secret",
			Trigger: &PRDevelopmentAttentionTrigger{LeaseToken: "lease-secret"},
		},
		PRDevelopmentAttentionPolicyPin{LeaseToken: "lease-secret"},
		PRDevelopmentAttentionSubjectPin{SubjectRevision: "sha256:secret"},
		PRDevelopmentAttentionTriggerRelease{Error: "private error"},
		PRDevelopmentAttentionTriggerCompletion{RunID: "wr_secret"},
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		assert.JSONEq(t, `{}`, string(encoded))
	}
}

func TestCompletePRDevelopmentReviewAtomicallyEnqueuesAttentionOccurrence(
	t *testing.T,
) {
	fixture, transition := newCompletedPRDevelopmentAIReviewFixture(
		t,
		PRDevelopmentCIPassed,
	)
	store := fixture.Operation.Store
	ctx := context.Background()
	conversation, err := store.AppendPRDevelopmentMessage(
		ctx,
		PRDevelopmentMessageAppend{
			CaseID:          fixture.Operation.Case.ID,
			ExpectedVersion: 0,
			Role:            PRDevelopmentMessageUser,
			Content:         "Use the smallest safe local change.",
		},
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), conversation.Version)

	lease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
	completion, changed, err := store.CompletePRDevelopmentReview(
		ctx,
		validPRDevelopmentAIReviewCompletionForTest(
			lease,
			PRDevelopmentLedgerReviewAttentionRequired,
		),
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Nil(t, completion.NextAttempt)
	require.Equal(t, transition.Fence.AttemptID, completion.Entry.AttemptID)

	trigger, err := store.GetPRDevelopmentAttentionTrigger(ctx, completion.Entry.ID)
	require.NoError(t, err)
	assert.Equal(t, completion.Entry.ID, trigger.ReviewEntryID)
	assert.Equal(t, completion.Entry.EntryHash, trigger.ReviewEntryHash)
	assert.Equal(t, fixture.Operation.Case.ID, trigger.CaseID)
	assert.Equal(t, conversation.Version, trigger.ConversationVersion)
	assert.Equal(t, PRDevelopmentAttentionDecisionReviewRequired, trigger.DecisionPoint)
	assert.Equal(t, PRDevelopmentAttentionTriggerPending, trigger.Status)
	assert.Empty(t, trigger.PolicyRevision)
	assert.Empty(t, trigger.SubjectRevision)
	assert.Empty(t, trigger.RunID)
	assert.Equal(t, trigger.CreatedAt, trigger.UpdatedAt)
	assert.Equal(t, trigger.CreatedAt, trigger.AvailableAt)

	projection, err := store.GetCurrentPRDevelopmentAttentionTriggerForCase(
		ctx,
		fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	assert.True(t, projection.AttentionRequired)
	assert.True(t, projection.TriggerCurrent)
	require.NotNil(t, projection.Trigger)
	assert.Equal(t, trigger.ReviewEntryID, projection.Trigger.ReviewEntryID)
}

func TestCompletePRDevelopmentReviewOnlyEnqueuesProductionAttentionOutcome(
	t *testing.T,
) {
	for _, outcome := range []PRDevelopmentLedgerReviewOutcome{
		PRDevelopmentLedgerReviewPassed,
		PRDevelopmentLedgerReviewChangesRequired,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			fixture, _ := newCompletedPRDevelopmentAIReviewFixture(
				t,
				PRDevelopmentCIPassed,
			)
			lease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
			completion, changed, err := fixture.Operation.Store.CompletePRDevelopmentReview(
				context.Background(),
				validPRDevelopmentAIReviewCompletionForTest(lease, outcome),
			)
			require.NoError(t, err)
			require.True(t, changed)
			_, err = fixture.Operation.Store.GetPRDevelopmentAttentionTrigger(
				context.Background(),
				completion.Entry.ID,
			)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	}

	fixture, _ := newCompletedPRDevelopmentAIReviewFixture(t, PRDevelopmentCIPassed)
	lease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
	entry, changed, err := fixture.Operation.Store.AppendPRDevelopmentLedgerReview(
		context.Background(),
		validPRDevelopmentAIReviewCompletionForTest(
			lease,
			PRDevelopmentLedgerReviewAttentionRequired,
		),
	)
	require.NoError(t, err)
	require.True(t, changed)
	_, err = fixture.Operation.Store.GetPRDevelopmentAttentionTrigger(
		context.Background(),
		entry.ID,
	)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestCompletePRDevelopmentReviewRollsBackWhenAttentionEnqueueFails(
	t *testing.T,
) {
	fixture, _ := newCompletedPRDevelopmentAIReviewFixture(t, PRDevelopmentCIPassed)
	store := fixture.Operation.Store
	lease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
	_, err := store.db.Exec(`CREATE TRIGGER reject_development_attention_enqueue
		BEFORE INSERT ON pr_development_attention_triggers
		BEGIN SELECT RAISE(ABORT, 'injected attention enqueue failure'); END`)
	require.NoError(t, err)
	input := validPRDevelopmentAIReviewCompletionForTest(
		lease,
		PRDevelopmentLedgerReviewAttentionRequired,
	)
	_, _, err = store.CompletePRDevelopmentReview(context.Background(), input)
	require.Error(t, err)

	controller, found, err := loadPRDevelopmentControllerAggregateByID(
		context.Background(),
		store.db,
		lease.Controller.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, PRDevelopmentControllerReview, controller.Phase)
	assert.Equal(t, lease.Controller.LeaseToken, controller.LeaseToken)
	contextSnapshot, err := store.GetPRDevelopmentContextSnapshot(
		context.Background(),
		fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.Len(t, contextSnapshot.Ledger.Entries, 1)
	assert.Equal(t, PRDevelopmentLedgerAttempt, contextSnapshot.Ledger.Entries[0].Kind)

	_, err = store.db.Exec(`DROP TRIGGER reject_development_attention_enqueue`)
	require.NoError(t, err)
	completion, changed, err := store.CompletePRDevelopmentReview(
		context.Background(),
		input,
	)
	require.NoError(t, err)
	require.True(t, changed)
	_, err = store.GetPRDevelopmentAttentionTrigger(
		context.Background(),
		completion.Entry.ID,
	)
	require.NoError(t, err)
}

func TestPRDevelopmentAttentionTriggerLeasePinsAndAnchoredConversation(
	t *testing.T,
) {
	fixture, snapshot := newPRDevelopmentAttentionOrchestrationFixtureAt(t, ":memory:")
	store := fixture.Operation.Store
	ctx := context.Background()
	trigger := claimOnePRDevelopmentAttentionTrigger(t, store, "attention-worker")
	require.Equal(t, snapshot.ReviewEntry.ID, trigger.ReviewEntryID)

	conversation, err := store.AppendPRDevelopmentMessage(
		ctx,
		PRDevelopmentMessageAppend{
			CaseID:          snapshot.Case.ID,
			ExpectedVersion: snapshot.Conversation.Version,
			Role:            PRDevelopmentMessageUser,
			Content:         "This later message must not change the occurrence prefix.",
		},
	)
	require.NoError(t, err)
	require.Greater(t, conversation.Version, trigger.ConversationVersion)

	claimed, anchored, err := store.GetClaimedPRDevelopmentAttentionSnapshot(
		ctx,
		trigger.ReviewEntryID,
		trigger.LeaseToken,
	)
	require.NoError(t, err)
	assert.Equal(t, trigger.ConversationVersion, anchored.Conversation.Version)
	assert.Len(t, anchored.Conversation.Messages, int(trigger.ConversationVersion))
	assert.Equal(t, trigger.TranscriptDigest, anchored.HighWater.TranscriptDigest)
	assert.Equal(t, trigger.LeaseToken, claimed.LeaseToken)

	policy := PRDevelopmentAttentionPolicyPin{
		ReviewEntryID:  trigger.ReviewEntryID,
		LeaseToken:     trigger.LeaseToken,
		PolicyRevision: "sha256:" + strings.Repeat("b", 64),
		PinnedPolicy:   json.RawMessage(`{"version":1}`),
		Snapshot:       anchored.HighWater,
	}
	_, err = store.PinPRDevelopmentAttentionTriggerSubject(
		ctx,
		PRDevelopmentAttentionSubjectPin{
			ReviewEntryID:   trigger.ReviewEntryID,
			LeaseToken:      trigger.LeaseToken,
			PolicyRevision:  policy.PolicyRevision,
			SubjectRevision: "sha256:" + strings.Repeat("a", 64),
			Snapshot:        anchored.HighWater,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentAttentionTriggerConflict)
	pinnedPolicy, err := store.PinPRDevelopmentAttentionTriggerPolicy(ctx, policy)
	require.NoError(t, err)
	assert.Equal(t, policy.PolicyRevision, pinnedPolicy.PolicyRevision)
	assert.Empty(t, pinnedPolicy.SubjectRevision)
	replayedPolicy, err := store.PinPRDevelopmentAttentionTriggerPolicy(ctx, policy)
	require.NoError(t, err)
	assert.Equal(t, pinnedPolicy.PolicyRevision, replayedPolicy.PolicyRevision)

	changedPolicy := policy
	changedPolicy.PinnedPolicy = json.RawMessage(`{"version":2}`)
	_, err = store.PinPRDevelopmentAttentionTriggerPolicy(ctx, changedPolicy)
	assert.ErrorIs(t, err, ErrPRDevelopmentAttentionTriggerConflict)

	subject := PRDevelopmentAttentionSubjectPin{
		ReviewEntryID:   trigger.ReviewEntryID,
		LeaseToken:      trigger.LeaseToken,
		PolicyRevision:  policy.PolicyRevision,
		SubjectRevision: "sha256:" + strings.Repeat("a", 64),
		Snapshot:        anchored.HighWater,
	}
	pinnedSubject, err := store.PinPRDevelopmentAttentionTriggerSubject(ctx, subject)
	require.NoError(t, err)
	assert.Equal(t, subject.SubjectRevision, pinnedSubject.SubjectRevision)
	_, err = store.PinPRDevelopmentAttentionTriggerSubject(ctx, subject)
	require.NoError(t, err)
	changedSubject := subject
	changedSubject.SubjectRevision = "sha256:" + strings.Repeat("c", 64)
	_, err = store.PinPRDevelopmentAttentionTriggerSubject(ctx, changedSubject)
	assert.ErrorIs(t, err, ErrPRDevelopmentAttentionTriggerConflict)

	require.NoError(t, store.RenewPRDevelopmentAttentionTriggerLease(
		ctx,
		trigger.ReviewEntryID,
		trigger.LeaseToken,
		2*time.Minute,
	))
	availableAt := (*fixture.Operation.Clock).Add(5 * time.Minute)
	err = store.ReleasePRDevelopmentAttentionTrigger(
		ctx,
		PRDevelopmentAttentionTriggerRelease{
			ReviewEntryID: trigger.ReviewEntryID,
			LeaseToken:    trigger.LeaseToken,
			AvailableAt:   availableAt,
			Error:         "private provider detail",
		},
	)
	require.NoError(t, err)
	notReady, err := store.ClaimPRDevelopmentAttentionTriggers(
		ctx,
		"attention-worker-too-soon",
		1,
		time.Minute,
	)
	require.NoError(t, err)
	assert.Empty(t, notReady)
	*fixture.Operation.Clock = (*fixture.Operation.Clock).Add(6 * time.Minute)
	reclaimed := claimOnePRDevelopmentAttentionTrigger(t, store, "attention-worker-retry")
	assert.NotEqual(t, trigger.LeaseToken, reclaimed.LeaseToken)
	assert.Equal(t, 2, reclaimed.Attempts)
	assert.Equal(t, policy.PolicyRevision, reclaimed.PolicyRevision)
	assert.Equal(t, subject.SubjectRevision, reclaimed.SubjectRevision)
	assert.NotEmpty(t, reclaimed.LastError)
	assert.ErrorIs(t, store.RenewPRDevelopmentAttentionTriggerLease(
		ctx,
		trigger.ReviewEntryID,
		trigger.LeaseToken,
		time.Minute,
	), ErrStaleLease)
}

func TestPRDevelopmentAttentionTriggerCompletionStatesAndLinkedDelivery(
	t *testing.T,
) {
	t.Run("delivered", func(t *testing.T) {
		store, snapshot := newPRDevelopmentAttentionFixture(t)
		trigger := claimOnePRDevelopmentAttentionTrigger(t, store, "delivery-worker")
		err := store.CompletePRDevelopmentAttentionTrigger(
			context.Background(),
			PRDevelopmentAttentionTriggerCompletion{
				ReviewEntryID: trigger.ReviewEntryID,
				LeaseToken:    trigger.LeaseToken,
				Status:        PRDevelopmentAttentionTriggerDelivered,
				RunID:         "wr_" + strings.Repeat("9", 32),
			},
		)
		assert.ErrorIs(t, err, ErrInvalidTransition)
		pinActivePRDevelopmentAttentionTrigger(t, store, trigger, snapshot)
		runID := "wr_" + strings.Repeat("1", 32)
		admission := testPRDevelopmentAttentionAdmission(snapshot, runID)
		_, _, err = store.AdmitPRDevelopmentAttentionDecisionRun(
			context.Background(),
			admission,
			func(context.Context) error { return nil },
		)
		require.NoError(t, err)
		err = store.CompletePRDevelopmentAttentionTrigger(
			context.Background(),
			PRDevelopmentAttentionTriggerCompletion{
				ReviewEntryID: trigger.ReviewEntryID,
				LeaseToken:    trigger.LeaseToken,
				Status:        PRDevelopmentAttentionTriggerDelivered,
				RunID:         "wr_" + strings.Repeat("8", 32),
			},
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentAttentionTriggerConflict)
		err = store.CompletePRDevelopmentAttentionTrigger(
			context.Background(),
			PRDevelopmentAttentionTriggerCompletion{
				ReviewEntryID: trigger.ReviewEntryID,
				LeaseToken:    trigger.LeaseToken,
				Status:        PRDevelopmentAttentionTriggerDelivered,
				RunID:         runID,
			},
		)
		require.NoError(t, err)
		terminal, err := store.GetPRDevelopmentAttentionTrigger(
			context.Background(),
			trigger.ReviewEntryID,
		)
		require.NoError(t, err)
		assert.Equal(t, PRDevelopmentAttentionTriggerDelivered, terminal.Status)
		assert.Equal(t, runID, terminal.RunID)
		require.NotNil(t, terminal.CompletedAt)
		claimed, err := store.ClaimPRDevelopmentAttentionTriggers(
			context.Background(),
			"another-worker",
			1,
			time.Minute,
		)
		require.NoError(t, err)
		assert.Empty(t, claimed)
	})

	t.Run("noop", func(t *testing.T) {
		store, snapshot := newPRDevelopmentAttentionFixture(t)
		trigger := claimOnePRDevelopmentAttentionTrigger(t, store, "noop-worker")
		err := store.CompletePRDevelopmentAttentionTrigger(
			context.Background(),
			PRDevelopmentAttentionTriggerCompletion{
				ReviewEntryID: trigger.ReviewEntryID,
				LeaseToken:    trigger.LeaseToken,
				Status:        PRDevelopmentAttentionTriggerNoop,
			},
		)
		assert.ErrorIs(t, err, ErrInvalidTransition)
		pinPolicyOnlyPRDevelopmentAttentionTrigger(t, store, trigger, snapshot)
		err = store.CompletePRDevelopmentAttentionTrigger(
			context.Background(),
			PRDevelopmentAttentionTriggerCompletion{
				ReviewEntryID: trigger.ReviewEntryID,
				LeaseToken:    trigger.LeaseToken,
				Status:        PRDevelopmentAttentionTriggerNoop,
			},
		)
		require.NoError(t, err)
	})

	for _, status := range []PRDevelopmentAttentionTriggerStatus{
		PRDevelopmentAttentionTriggerSuperseded,
		PRDevelopmentAttentionTriggerFailed,
	} {
		t.Run(string(status), func(t *testing.T) {
			store, _ := newPRDevelopmentAttentionFixture(t)
			trigger := claimOnePRDevelopmentAttentionTrigger(t, store, "terminal-worker")
			err := store.CompletePRDevelopmentAttentionTrigger(
				context.Background(),
				PRDevelopmentAttentionTriggerCompletion{
					ReviewEntryID: trigger.ReviewEntryID,
					LeaseToken:    trigger.LeaseToken,
					Status:        status,
					Error:         "fixed safe failure",
				},
			)
			require.NoError(t, err)
		})
	}

	t.Run("recovery_required", func(t *testing.T) {
		store, snapshot := newPRDevelopmentAttentionFixture(t)
		trigger := claimOnePRDevelopmentAttentionTrigger(t, store, "recovery-worker")
		err := store.CompletePRDevelopmentAttentionTrigger(
			context.Background(),
			PRDevelopmentAttentionTriggerCompletion{
				ReviewEntryID: trigger.ReviewEntryID,
				LeaseToken:    trigger.LeaseToken,
				Status:        PRDevelopmentAttentionTriggerRecoveryRequired,
			},
		)
		assert.ErrorIs(t, err, ErrInvalidTransition)
		pinActivePRDevelopmentAttentionTrigger(t, store, trigger, snapshot)
		err = store.CompletePRDevelopmentAttentionTrigger(
			context.Background(),
			PRDevelopmentAttentionTriggerCompletion{
				ReviewEntryID: trigger.ReviewEntryID,
				LeaseToken:    trigger.LeaseToken,
				Status:        PRDevelopmentAttentionTriggerRecoveryRequired,
				Error:         "attention workflow admission is uncertain",
			},
		)
		require.NoError(t, err)
	})
}

func TestPRDevelopmentAttentionTriggerConcurrentClaimAndPinAcrossStores(
	t *testing.T,
) {
	path := filepath.Join(t.TempDir(), "attention-trigger-concurrent.db")
	fixture, snapshot := newPRDevelopmentAttentionOrchestrationFixtureAt(t, path)
	first := fixture.Operation.Store
	fixedNow := *fixture.Operation.Clock
	second, err := Open(context.Background(), path, WithClock(func() time.Time { return fixedNow }))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })

	type claimResult struct {
		items []PRDevelopmentAttentionTrigger
		err   error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	var wait sync.WaitGroup
	for index, store := range []*Store{first, second} {
		wait.Add(1)
		go func(index int, store *Store) {
			defer wait.Done()
			<-start
			items, claimErr := store.ClaimPRDevelopmentAttentionTriggers(
				context.Background(),
				"concurrent-worker-"+string(rune('a'+index)),
				1,
				time.Minute,
			)
			results <- claimResult{items: items, err: claimErr}
		}(index, store)
	}
	close(start)
	wait.Wait()
	close(results)
	var claimed PRDevelopmentAttentionTrigger
	total := 0
	for result := range results {
		require.NoError(t, result.err)
		total += len(result.items)
		if len(result.items) == 1 {
			claimed = result.items[0]
		}
	}
	require.Equal(t, 1, total)

	_, anchored, err := first.GetClaimedPRDevelopmentAttentionSnapshot(
		context.Background(),
		claimed.ReviewEntryID,
		claimed.LeaseToken,
	)
	require.NoError(t, err)
	require.Equal(t, snapshot.HighWater, anchored.HighWater)
	pin := PRDevelopmentAttentionPolicyPin{
		ReviewEntryID:  claimed.ReviewEntryID,
		LeaseToken:     claimed.LeaseToken,
		PolicyRevision: "sha256:" + strings.Repeat("b", 64),
		PinnedPolicy:   json.RawMessage(`{"version":1}`),
		Snapshot:       anchored.HighWater,
	}
	start = make(chan struct{})
	pinErrors := make(chan error, 2)
	for _, store := range []*Store{first, second} {
		wait.Add(1)
		go func(store *Store) {
			defer wait.Done()
			<-start
			_, pinErr := store.PinPRDevelopmentAttentionTriggerPolicy(
				context.Background(),
				pin,
			)
			pinErrors <- pinErr
		}(store)
	}
	close(start)
	wait.Wait()
	close(pinErrors)
	for pinErr := range pinErrors {
		require.NoError(t, pinErr)
	}
}

func TestPRDevelopmentAttentionTriggerTamperAndSupersessionFailClosed(
	t *testing.T,
) {
	t.Run("conversation digest", func(t *testing.T) {
		store, _ := newPRDevelopmentAttentionFixture(t)
		trigger := claimOnePRDevelopmentAttentionTrigger(t, store, "tamper-worker")
		_, err := store.db.Exec(`
			UPDATE pr_development_attention_triggers
			SET transcript_digest = ?
			WHERE review_entry_id = ?`,
			strings.Repeat("f", 64),
			trigger.ReviewEntryID,
		)
		require.NoError(t, err)
		_, _, err = store.GetClaimedPRDevelopmentAttentionSnapshot(
			context.Background(),
			trigger.ReviewEntryID,
			trigger.LeaseToken,
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentAttentionTriggerConflict)
		_, err = store.GetCurrentPRDevelopmentAttentionTriggerForCase(
			context.Background(),
			trigger.CaseID,
		)
		require.Error(t, err)
	})

	t.Run("later attempt", func(t *testing.T) {
		fixture, _ := newPRDevelopmentAttentionOrchestrationFixtureAt(t, ":memory:")
		store := fixture.Operation.Store
		trigger := claimOnePRDevelopmentAttentionTrigger(t, store, "superseded-worker")
		current, err := store.GetPRDevelopmentWorkbench(
			context.Background(),
			fixture.Operation.Case.ID,
		)
		require.NoError(t, err)
		require.NotNil(t, current.RepairSession)
		workbench, _, err := store.AdmitPRDevelopmentRepair(
			context.Background(),
			PRDevelopmentRepairAdmit{
				CaseID:                      fixture.Operation.Case.ID,
				ExpectedConversationVersion: current.Conversation.Version,
				ExpectedRepairVersion:       current.RepairSession.Version,
				IdempotencyKey:              "superseding-user-attempt",
				AgentID:                     fixture.Operation.Session.AgentID,
				Instruction:                 "Continue after the local review.",
			},
		)
		require.NoError(t, err)
		require.NotNil(t, workbench.RepairSession)
		_, _, err = store.GetClaimedPRDevelopmentAttentionSnapshot(
			context.Background(),
			trigger.ReviewEntryID,
			trigger.LeaseToken,
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentAttentionSuperseded)
		projection, err := store.GetCurrentPRDevelopmentAttentionTriggerForCase(
			context.Background(),
			trigger.CaseID,
		)
		require.NoError(t, err)
		require.NotNil(t, projection.Trigger)
		assert.False(t, projection.TriggerCurrent)
	})

	t.Run("later provider-thread case owns controller", func(t *testing.T) {
		fixture, snapshot := newPRDevelopmentAttentionOrchestrationFixtureAt(t, ":memory:")
		store := fixture.Operation.Store
		trigger := claimOnePRDevelopmentAttentionTrigger(t, store, "cross-case-worker")
		sibling := captureAdditionalPRDevelopmentThreadCase(
			t,
			store,
			fixture.Operation.Clock,
			fixture.Operation.Case.PRDevelopmentCaptureInput,
			"attention-controller-owner-sibling",
			"1701",
		)
		result, err := store.db.Exec(`
			UPDATE pr_development_repair_sessions
			SET case_id = ?
			WHERE id = ?`,
			sibling.ID,
			snapshot.OwnerSession.ID,
		)
		require.NoError(t, err)
		rows, err := result.RowsAffected()
		require.NoError(t, err)
		require.Equal(t, int64(1), rows)

		_, _, err = store.GetClaimedPRDevelopmentAttentionSnapshot(
			context.Background(),
			trigger.ReviewEntryID,
			trigger.LeaseToken,
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentAttentionSuperseded)
		_, err = store.GetPRDevelopmentAttentionSnapshot(
			context.Background(),
			snapshot.Case.ID,
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentAttentionConflict)
	})
}

func TestStoreMigratesV15WithoutSynthesizingDevelopmentAttentionTriggers(
	t *testing.T,
) {
	path := filepath.Join(t.TempDir(), "attention-trigger-v15.db")
	fixture, snapshot := newPRDevelopmentAttentionOrchestrationFixtureAt(t, path)
	require.NoError(t, fixture.Operation.Store.Close())

	db := openSchemaTestDB(t, path)
	_, err := db.Exec(`DROP TABLE pr_development_attention_triggers`)
	require.NoError(t, err)
	_, err = db.Exec(`PRAGMA user_version = 15`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	_, err = store.GetPRDevelopmentAttentionSnapshot(context.Background(), snapshot.Case.ID)
	require.NoError(t, err)
	_, err = store.GetPRDevelopmentAttentionTrigger(
		context.Background(),
		snapshot.ReviewEntry.ID,
	)
	assert.ErrorIs(t, err, ErrNotFound)
	projection, err := store.GetCurrentPRDevelopmentAttentionTriggerForCase(
		context.Background(),
		snapshot.Case.ID,
	)
	require.NoError(t, err)
	assert.True(t, projection.AttentionRequired)
	assert.Nil(t, projection.Trigger)
	assert.False(t, projection.TriggerCurrent)
}

func TestStorePRDevelopmentAttentionTriggerSchemaV16ValidationRollsBack(
	t *testing.T,
) {
	seedPath := filepath.Join(t.TempDir(), "seed-v15.db")
	seed, _ := newPRDevelopmentAttentionOrchestrationFixtureAt(t, seedPath)
	require.NoError(t, seed.Operation.Store.Close())
	db := openSchemaTestDB(t, seedPath)
	_, err := db.Exec(`DROP TABLE pr_development_attention_triggers`)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE pr_development_attention_triggers (
		review_entry_id TEXT PRIMARY KEY,
		status TEXT NOT NULL,
		available_at INTEGER NOT NULL,
		lease_until INTEGER,
		created_at INTEGER NOT NULL
	)`)
	require.NoError(t, err)
	_, err = db.Exec(`PRAGMA user_version = 15`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	opened, err := Open(context.Background(), seedPath)
	require.Error(t, err)
	assert.Nil(t, opened)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v16")
	db = openSchemaTestDB(t, seedPath)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 15, version)
	assert.False(t, schemaObjectExists(
		t,
		db,
		"index",
		"pr_development_attention_triggers_claim",
	))
}

func claimOnePRDevelopmentAttentionTrigger(
	t *testing.T,
	store *Store,
	worker string,
) PRDevelopmentAttentionTrigger {
	t.Helper()
	claimed, err := store.ClaimPRDevelopmentAttentionTriggers(
		context.Background(),
		worker,
		1,
		5*time.Minute,
	)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	return claimed[0]
}

func pinPolicyOnlyPRDevelopmentAttentionTrigger(
	t *testing.T,
	store *Store,
	trigger PRDevelopmentAttentionTrigger,
	snapshot PRDevelopmentAttentionSnapshot,
) PRDevelopmentAttentionTrigger {
	t.Helper()
	pinned, err := store.PinPRDevelopmentAttentionTriggerPolicy(
		context.Background(),
		PRDevelopmentAttentionPolicyPin{
			ReviewEntryID:  trigger.ReviewEntryID,
			LeaseToken:     trigger.LeaseToken,
			PolicyRevision: "sha256:" + strings.Repeat("b", 64),
			PinnedPolicy:   json.RawMessage(`{"version":1}`),
			Snapshot:       snapshot.HighWater,
		},
	)
	require.NoError(t, err)
	return pinned
}

func pinActivePRDevelopmentAttentionTrigger(
	t *testing.T,
	store *Store,
	trigger PRDevelopmentAttentionTrigger,
	snapshot PRDevelopmentAttentionSnapshot,
) PRDevelopmentAttentionTrigger {
	t.Helper()
	pinned := pinPolicyOnlyPRDevelopmentAttentionTrigger(t, store, trigger, snapshot)
	active, err := store.PinPRDevelopmentAttentionTriggerSubject(
		context.Background(),
		PRDevelopmentAttentionSubjectPin{
			ReviewEntryID:   trigger.ReviewEntryID,
			LeaseToken:      trigger.LeaseToken,
			PolicyRevision:  pinned.PolicyRevision,
			SubjectRevision: "sha256:" + strings.Repeat("a", 64),
			Snapshot:        snapshot.HighWater,
		},
	)
	require.NoError(t, err)
	return active
}
