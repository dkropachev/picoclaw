//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPRDevelopmentAttentionTypesAreJSONPrivate(t *testing.T) {
	t.Parallel()

	sentinel := "private-attention-sentinel"
	values := []any{
		PRDevelopmentAttentionHighWater{CaseID: sentinel},
		PRDevelopmentAttentionSnapshot{
			Case:      PRDevelopmentCase{ID: sentinel},
			HighWater: PRDevelopmentAttentionHighWater{CaseID: sentinel},
		},
		PRDevelopmentAttentionDecisionKey{CaseID: sentinel},
		PRDevelopmentAttentionDecisionRunAdmission{RunID: sentinel},
		PRDevelopmentAttentionDecisionRunLink{RunID: sentinel},
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		assert.Equal(t, `{}`, string(encoded))
		assert.NotContains(t, string(encoded), sentinel)
	}
}

func TestStorePRDevelopmentAttentionSnapshotIsAtomicRichTail(t *testing.T) {
	t.Parallel()

	store, snapshot := newPRDevelopmentAttentionFixture(t)
	loaded, err := store.GetPRDevelopmentAttentionSnapshot(
		context.Background(),
		snapshot.Case.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, snapshot, loaded)
	assert.Equal(t, loaded.Case.ID, loaded.Conversation.CaseID)
	assert.Equal(t, loaded.Case.ID, loaded.OwnerSession.CaseID)
	assert.Equal(t, loaded.OwnerSession.ID, loaded.Controller.OwnerSessionID)
	assert.Equal(t, loaded.Controller.CurrentAttemptID, loaded.Fence.AttemptID)
	assert.Equal(t, loaded.Fence.AttemptID, loaded.ReviewEntry.AttemptID)
	assert.Equal(t, PRDevelopmentLedgerReviewAttentionRequired, loaded.ReviewEntry.ReviewOutcome)
	assert.Equal(t, loaded.ReviewEntry, loaded.Ledger.Entries[len(loaded.Ledger.Entries)-1])
	assert.Equal(t, loaded.Conversation.Version, loaded.HighWater.ConversationVersion)
	assert.Equal(t, loaded.Thread.CasesDigest, loaded.HighWater.ThreadCasesDigest)
	assert.Equal(t, loaded.Ledger.EntriesDigest, loaded.HighWater.LedgerEntriesDigest)
	assert.Equal(t, loaded.Ledger.CheckpointsDigest, loaded.HighWater.LedgerCheckpointsDigest)
	assert.Equal(t, loaded.Controller.Revision, loaded.HighWater.ControllerRevision)
	assert.Equal(t, loaded.Controller.FencesDigest, loaded.HighWater.ControllerFencesDigest)
}

func TestStorePRDevelopmentAttentionAdmissionIsAtomicAndHistoricallyIdempotent(
	t *testing.T,
) {
	t.Parallel()

	store, snapshot := newPRDevelopmentAttentionFixture(t)
	admission := testPRDevelopmentAttentionAdmission(
		snapshot,
		"wr_00000000000000000000000000001501",
	)
	var calls atomic.Int32
	link, existed, err := store.AdmitPRDevelopmentAttentionDecisionRun(
		context.Background(),
		admission,
		func(ctx context.Context) error {
			calls.Add(1)
			return ctx.Err()
		},
	)
	require.NoError(t, err)
	assert.False(t, existed)
	assert.Equal(t, admission.Key, link.Key)
	assert.Equal(t, admission.Snapshot, link.Snapshot)
	assert.Equal(t, admission.RunID, link.RunID)
	assert.Equal(t, int32(1), calls.Load())

	stored, err := store.GetPRDevelopmentAttentionDecisionRun(
		context.Background(),
		admission.Key,
	)
	require.NoError(t, err)
	assert.Equal(t, link, stored)

	conversation, err := store.AppendPRDevelopmentMessage(
		context.Background(),
		PRDevelopmentMessageAppend{
			CaseID:          snapshot.Case.ID,
			ExpectedVersion: snapshot.Conversation.Version,
			Role:            PRDevelopmentMessageUser,
			Content:         "Please explain the local review concern.",
		},
	)
	require.NoError(t, err)
	assert.Equal(t, snapshot.Conversation.Version+1, conversation.Version)
	advanced, admitted, err := store.AdmitPRDevelopmentRepair(
		context.Background(),
		PRDevelopmentRepairAdmit{
			CaseID:                      snapshot.Case.ID,
			ExpectedConversationVersion: conversation.Version,
			ExpectedRepairVersion:       snapshot.OwnerSession.Version,
			IdempotencyKey:              "attention-historical-later-attempt",
			AgentID:                     snapshot.OwnerSession.AgentID,
			Instruction:                 "Continue only after the attention decision is resolved.",
		},
	)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NotNil(t, advanced.RepairSession)
	require.Len(t, advanced.RepairSession.Attempts, snapshot.HighWater.OwnerAttemptCount+1)

	// Exact semantic replay is resolved from durable history before inspecting
	// either the now-stale subject or the caller's compact snapshot. Later
	// conversation and owner-session appends do not invalidate immutable prefix
	// integrity.
	replay := admission
	replay.Snapshot = PRDevelopmentAttentionHighWater{}
	retried, existed, err := store.AdmitPRDevelopmentAttentionDecisionRun(
		context.Background(),
		replay,
		func(context.Context) error {
			calls.Add(1)
			return errors.New("must not run")
		},
	)
	require.NoError(t, err)
	assert.True(t, existed)
	assert.Equal(t, link, retried)
	assert.Equal(t, int32(1), calls.Load())

	stale := admission
	stale.Key.DecisionPoint = "pr_development.review_attention_retry"
	stale.RunID = "wr_00000000000000000000000000001502"
	_, existed, err = store.AdmitPRDevelopmentAttentionDecisionRun(
		context.Background(),
		stale,
		func(context.Context) error {
			calls.Add(1)
			return nil
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentAttentionConflict)
	assert.False(t, existed)
	assert.Equal(t, int32(1), calls.Load())
}

func TestStorePRDevelopmentAttentionAdmissionSerializesAcrossStoreHandles(
	t *testing.T,
) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "concurrent-pr-development-attention.db")
	fixture, snapshot := newPRDevelopmentAttentionOrchestrationFixtureAt(t, path)
	first := fixture.Operation.Store
	fixedNow := *fixture.Operation.Clock
	second, err := Open(
		context.Background(),
		path,
		WithClock(func() time.Time { return fixedNow }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })

	admission := testPRDevelopmentAttentionAdmission(
		snapshot,
		"wr_00000000000000000000000000001506",
	)
	type result struct {
		link    PRDevelopmentAttentionDecisionRunLink
		existed bool
		err     error
	}
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	attempting := make(chan struct{}, 2)
	callbackEntered := make(chan struct{}, 1)
	releaseCallback := make(chan struct{})
	results := make(chan result, 2)
	var callbacks atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, store := range []*Store{first, second} {
		go func() {
			ready <- struct{}{}
			<-start
			attempting <- struct{}{}
			link, existed, admitErr := store.AdmitPRDevelopmentAttentionDecisionRun(
				ctx,
				admission,
				func(context.Context) error {
					callbacks.Add(1)
					select {
					case callbackEntered <- struct{}{}:
					default:
					}
					select {
					case <-releaseCallback:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				},
			)
			results <- result{link: link, existed: existed, err: admitErr}
		}()
	}
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("timed out waiting for concurrent admissions to become ready")
		}
	}
	close(start)
	for range 2 {
		select {
		case <-attempting:
		case <-ctx.Done():
			t.Fatal("timed out waiting for both Store handles to attempt admission")
		}
	}
	select {
	case <-callbackEntered:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the winning create callback")
	}
	close(releaseCallback)

	completed := make([]result, 0, 2)
	for range 2 {
		select {
		case candidate := <-results:
			completed = append(completed, candidate)
		case <-ctx.Done():
			t.Fatal("timed out waiting for serialized admission results")
		}
	}
	firstResult, secondResult := completed[0], completed[1]
	require.NoError(t, firstResult.err)
	require.NoError(t, secondResult.err)
	assert.Equal(t, int32(1), callbacks.Load())
	assert.Equal(t, firstResult.link, secondResult.link)
	created, historical := 0, 0
	for _, candidate := range []result{firstResult, secondResult} {
		if candidate.existed {
			historical++
		} else {
			created++
		}
	}
	assert.Equal(t, 1, created)
	assert.Equal(t, 1, historical)

	var rows int
	require.NoError(t, first.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_attention_decision_runs`).Scan(&rows))
	assert.Equal(t, 1, rows)
}

func TestStorePRDevelopmentAttentionAdmissionRollsBackCreateFailureAndReportsUncertainty(
	t *testing.T,
) {
	t.Parallel()

	t.Run("callback failure", func(t *testing.T) {
		t.Parallel()

		store, snapshot := newPRDevelopmentAttentionFixture(t)
		admission := testPRDevelopmentAttentionAdmission(
			snapshot,
			"wr_00000000000000000000000000001503",
		)
		callbackErr := errors.New("workflow create failed")
		_, existed, err := store.AdmitPRDevelopmentAttentionDecisionRun(
			context.Background(),
			admission,
			func(context.Context) error { return callbackErr },
		)
		assert.ErrorIs(t, err, callbackErr)
		assert.False(t, existed)
		_, err = store.GetPRDevelopmentAttentionDecisionRun(
			context.Background(),
			admission.Key,
		)
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("post-callback commit cancellation", func(t *testing.T) {
		t.Parallel()

		store, snapshot := newPRDevelopmentAttentionFixture(t)
		admission := testPRDevelopmentAttentionAdmission(
			snapshot,
			"wr_00000000000000000000000000001504",
		)
		ctx, cancel := context.WithCancel(context.Background())
		var calls atomic.Int32
		_, existed, err := store.AdmitPRDevelopmentAttentionDecisionRun(
			ctx,
			admission,
			func(context.Context) error {
				calls.Add(1)
				cancel()
				return nil
			},
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentAttentionAdmissionUncertain)
		assert.ErrorIs(t, err, context.Canceled)
		assert.False(t, existed)
		assert.Equal(t, int32(1), calls.Load())
	})
}

func TestStorePRDevelopmentAttentionAdmissionRejectsChangedSnapshotAndIdentities(
	t *testing.T,
) {
	t.Parallel()

	store, snapshot := newPRDevelopmentAttentionFixture(t)
	valid := testPRDevelopmentAttentionAdmission(
		snapshot,
		"wr_00000000000000000000000000001505",
	)
	tests := []struct {
		name   string
		mutate func(*PRDevelopmentAttentionDecisionRunAdmission)
	}{
		{name: "case", mutate: func(value *PRDevelopmentAttentionDecisionRunAdmission) {
			value.Key.CaseID = "pdc_bad"
		}},
		{name: "review hash", mutate: func(value *PRDevelopmentAttentionDecisionRunAdmission) {
			value.Key.ReviewEntryHash = strings.Repeat("A", 64)
		}},
		{name: "subject revision", mutate: func(value *PRDevelopmentAttentionDecisionRunAdmission) {
			value.Key.SubjectRevision = "sha256:" + strings.Repeat("A", 64)
		}},
		{name: "decision point", mutate: func(value *PRDevelopmentAttentionDecisionRunAdmission) {
			value.Key.DecisionPoint = "PR/attention"
		}},
		{name: "policy revision", mutate: func(value *PRDevelopmentAttentionDecisionRunAdmission) {
			value.Key.PolicyRevision = "policy-v1"
		}},
		{name: "run ID", mutate: func(value *PRDevelopmentAttentionDecisionRunAdmission) {
			value.RunID = "wr_bad"
		}},
		{name: "controller audit fence", mutate: func(value *PRDevelopmentAttentionDecisionRunAdmission) {
			value.Snapshot.ControllerRevision++
		}},
		{name: "session audit fence", mutate: func(value *PRDevelopmentAttentionDecisionRunAdmission) {
			value.Snapshot.OwnerSessionVersion++
		}},
		{name: "thread high water", mutate: func(value *PRDevelopmentAttentionDecisionRunAdmission) {
			value.Snapshot.ThreadCasesDigest = strings.Repeat("f", 64)
		}},
		{name: "ledger high water", mutate: func(value *PRDevelopmentAttentionDecisionRunAdmission) {
			value.Snapshot.LedgerEntriesDigest = strings.Repeat("f", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			input := valid
			test.mutate(&input)
			var calls atomic.Int32
			_, existed, err := store.AdmitPRDevelopmentAttentionDecisionRun(
				context.Background(),
				input,
				func(context.Context) error {
					calls.Add(1)
					return nil
				},
			)
			if test.name == "controller audit fence" || test.name == "session audit fence" ||
				test.name == "thread high water" || test.name == "ledger high water" {
				assert.ErrorIs(t, err, ErrPRDevelopmentAttentionConflict)
			} else {
				assert.ErrorIs(t, err, ErrInvalidPRDevelopmentAttention)
			}
			assert.False(t, existed)
			assert.Zero(t, calls.Load())
		})
	}

	_, _, err := store.AdmitPRDevelopmentAttentionDecisionRun(
		context.Background(),
		valid,
		nil,
	)
	assert.ErrorIs(t, err, ErrInvalidPRDevelopmentAttention)
}

func TestStorePRDevelopmentAttentionHistoricalReplayValidatesImmutablePrefixes(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name    string
		runID   string
		prepare func(
			*testing.T,
			*Store,
			PRDevelopmentAttentionSnapshot,
		) PRDevelopmentAttentionSnapshot
		mutate func(*testing.T, *Store, PRDevelopmentAttentionSnapshot)
	}{
		{
			name:  "conversation",
			runID: "wr_00000000000000000000000000001601",
			prepare: func(
				t *testing.T,
				store *Store,
				snapshot PRDevelopmentAttentionSnapshot,
			) PRDevelopmentAttentionSnapshot {
				t.Helper()
				_, err := store.AppendPRDevelopmentMessage(
					context.Background(),
					PRDevelopmentMessageAppend{
						CaseID:          snapshot.Case.ID,
						ExpectedVersion: snapshot.Conversation.Version,
						Role:            PRDevelopmentMessageUser,
						Content:         "immutable prefix",
					},
				)
				require.NoError(t, err)
				loaded, err := store.GetPRDevelopmentAttentionSnapshot(
					context.Background(),
					snapshot.Case.ID,
				)
				require.NoError(t, err)
				return loaded
			},
			mutate: func(t *testing.T, store *Store, snapshot PRDevelopmentAttentionSnapshot) {
				t.Helper()
				_, err := store.db.Exec(`
					UPDATE pr_development_messages SET content = 'tampered prefix'
					WHERE case_id = ? AND ordinal = 0`,
					snapshot.Case.ID,
				)
				require.NoError(t, err)
			},
		},
		{
			name:  "ledger",
			runID: "wr_00000000000000000000000000001602",
			mutate: func(t *testing.T, store *Store, snapshot PRDevelopmentAttentionSnapshot) {
				t.Helper()
				_, err := store.db.Exec(`
					UPDATE pr_development_ledger_entries SET summary = 'tampered ledger prefix'
					WHERE id = ?`,
					snapshot.ReviewEntry.ID,
				)
				require.NoError(t, err)
			},
		},
		{
			name:  "thread membership",
			runID: "wr_00000000000000000000000000001603",
			mutate: func(t *testing.T, store *Store, snapshot PRDevelopmentAttentionSnapshot) {
				t.Helper()
				result, err := store.db.Exec(`
					UPDATE pr_development_thread_cases SET link_hash = ?
					WHERE case_id = ? AND thread_id = ? AND ordinal = ?`,
					strings.Repeat("f", 64),
					snapshot.Case.ID,
					snapshot.Thread.ID,
					snapshot.HighWater.SelectedOrdinal,
				)
				require.NoError(t, err)
				affected, err := result.RowsAffected()
				require.NoError(t, err)
				require.Equal(t, int64(1), affected)
			},
		},
		{
			name:  "owner session prefix",
			runID: "wr_00000000000000000000000000001604",
			mutate: func(t *testing.T, store *Store, snapshot PRDevelopmentAttentionSnapshot) {
				t.Helper()
				require.Greater(t, snapshot.HighWater.OwnerSessionVersion, int64(1))
				result, err := store.db.Exec(`
					UPDATE pr_development_repair_sessions SET version = version - 1
					WHERE id = ?`,
					snapshot.HighWater.OwnerSessionID,
				)
				require.NoError(t, err)
				affected, err := result.RowsAffected()
				require.NoError(t, err)
				require.Equal(t, int64(1), affected)
			},
		},
		{
			name:  "review fence prefix",
			runID: "wr_00000000000000000000000000001605",
			mutate: func(t *testing.T, store *Store, snapshot PRDevelopmentAttentionSnapshot) {
				t.Helper()
				result, err := store.db.Exec(`
					UPDATE pr_development_attempt_review_fences SET fence_hash = ?
					WHERE attempt_id = ?`,
					strings.Repeat("f", 64),
					snapshot.HighWater.AttemptID,
				)
				require.NoError(t, err)
				affected, err := result.RowsAffected()
				require.NoError(t, err)
				require.Equal(t, int64(1), affected)
			},
		},
		{
			name:  "controller fence prefix",
			runID: "wr_00000000000000000000000000001606",
			mutate: func(t *testing.T, store *Store, snapshot PRDevelopmentAttentionSnapshot) {
				t.Helper()
				result, err := store.db.Exec(`
					UPDATE pr_development_thread_controllers SET fences_digest = ?
					WHERE id = ?`,
					strings.Repeat("f", 64),
					snapshot.HighWater.ControllerID,
				)
				require.NoError(t, err)
				affected, err := result.RowsAffected()
				require.NoError(t, err)
				require.Equal(t, int64(1), affected)
			},
		},
		{
			name:  "checkpoint prefix",
			runID: "wr_00000000000000000000000000001607",
			prepare: func(
				t *testing.T,
				store *Store,
				snapshot PRDevelopmentAttentionSnapshot,
			) PRDevelopmentAttentionSnapshot {
				t.Helper()
				checkpoint, changed, err := store.AppendPRDevelopmentLedgerCheckpoint(
					context.Background(),
					PRDevelopmentLedgerCheckpointAppend{
						CaseID:         snapshot.Case.ID,
						ThroughOrdinal: snapshot.ReviewEntry.Ordinal,
						SourceDigest:   snapshot.ReviewEntry.EntryHash,
						Summary:        "The admitted attention review prefix was compacted.",
						CompactorID:    "attention-prefix-test",
						PromptDigest:   strings.Repeat("c", 64),
					},
				)
				require.NoError(t, err)
				require.True(t, changed)
				loaded, err := store.GetPRDevelopmentAttentionSnapshot(
					context.Background(),
					snapshot.Case.ID,
				)
				require.NoError(t, err)
				require.NotNil(t, loaded.Ledger.LatestCheckpoint)
				require.Equal(t, checkpoint.ID, loaded.Ledger.LatestCheckpoint.ID)
				return loaded
			},
			mutate: func(t *testing.T, store *Store, snapshot PRDevelopmentAttentionSnapshot) {
				t.Helper()
				require.NotNil(t, snapshot.Ledger.LatestCheckpoint)
				result, err := store.db.Exec(`
					UPDATE pr_development_ledger_checkpoints SET summary = 'tampered checkpoint prefix'
					WHERE id = ?`,
					snapshot.Ledger.LatestCheckpoint.ID,
				)
				require.NoError(t, err)
				affected, err := result.RowsAffected()
				require.NoError(t, err)
				require.Equal(t, int64(1), affected)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store, snapshot := newPRDevelopmentAttentionFixture(t)
			if test.prepare != nil {
				snapshot = test.prepare(t, store, snapshot)
			}
			admission := testPRDevelopmentAttentionAdmission(
				snapshot,
				test.runID,
			)
			_, _, err := store.AdmitPRDevelopmentAttentionDecisionRun(
				context.Background(),
				admission,
				func(context.Context) error { return nil },
			)
			require.NoError(t, err)
			test.mutate(t, store, snapshot)

			_, err = store.GetPRDevelopmentAttentionDecisionRun(
				context.Background(),
				admission.Key,
			)
			require.Error(t, err)
			var calls atomic.Int32
			_, existed, err := store.AdmitPRDevelopmentAttentionDecisionRun(
				context.Background(),
				admission,
				func(context.Context) error {
					calls.Add(1)
					return nil
				},
			)
			require.Error(t, err)
			assert.False(t, existed)
			assert.Zero(t, calls.Load())
		})
	}
}

func TestStorePRDevelopmentAttentionHistoricalReplaySurvivesLaterReviewedFence(
	t *testing.T,
) {
	t.Parallel()

	fixture, snapshot := newPRDevelopmentAttentionOrchestrationFixtureAt(t, ":memory:")
	store := fixture.Operation.Store
	admission := testPRDevelopmentAttentionAdmission(
		snapshot,
		"wr_00000000000000000000000000001608",
	)
	want, existed, err := store.AdmitPRDevelopmentAttentionDecisionRun(
		context.Background(),
		admission,
		func(context.Context) error { return nil },
	)
	require.NoError(t, err)
	require.False(t, existed)

	workbench, err := store.GetPRDevelopmentWorkbench(
		context.Background(),
		snapshot.Case.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, workbench.RepairSession)
	next, admitted, err := store.AdmitPRDevelopmentRepair(
		context.Background(),
		PRDevelopmentRepairAdmit{
			CaseID:                      snapshot.Case.ID,
			ExpectedConversationVersion: snapshot.Conversation.Version,
			ExpectedRepairVersion:       workbench.RepairSession.Version,
			IdempotencyKey:              "attention-later-reviewed-fence",
			AgentID:                     workbench.RepairSession.AgentID,
			Instruction:                 "Advance the retained line with one later reviewed fence.",
		},
	)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NotNil(t, next.RepairSession)
	fixture.Operation.Session = *next.RepairSession
	fixture.Operation.Attempt = next.RepairSession.Attempts[len(next.RepairSession.Attempts)-1]

	mutation, acquired, err := store.AcquirePRDevelopmentControllerLease(
		context.Background(),
		PRDevelopmentControllerAcquire{
			CaseID:           snapshot.Case.ID,
			AttemptID:        fixture.Operation.Attempt.ID,
			ExpectedRevision: snapshot.Controller.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "attention-later-mutation",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	resumeRequest := operationBaseRequest(fixture.Operation, mutation.Controller)
	resumeRequest.ExpectedVersion = mutation.Controller.LineVersion
	resumeRequest.ExpectedEpoch = mutation.Controller.MutationEpoch
	resumeRequest.ExpectedTip = mutation.Controller.TipCommit
	resumeRequest.ExpectedTree = mutation.Controller.Tree
	resume := prepareOperationForTest(
		t,
		fixture.Operation,
		mutation,
		PRDevelopmentControllerOperationResume,
		fixture.Operation.operationID(),
		resumeRequest,
	)
	resumed := finalizeOperationForTest(
		t,
		fixture.Operation,
		mutation,
		resume,
		PRDevelopmentControllerOperationResult{
			WorkspaceID:   fixture.Operation.Session.WorkspaceID,
			Version:       mutation.Controller.LineVersion,
			MutationEpoch: mutation.Controller.MutationEpoch + 1,
			Tip:           mutation.Controller.TipCommit,
			Tree:          mutation.Controller.Tree,
		},
	)
	resumedLease := operationLeaseFromTransition(resumed)
	_, _, parked := parkOperationForTest(
		t,
		fixture.Operation,
		resumedLease,
		nil,
		"The later retained-line validation required no code change.",
		2,
		9601,
	)
	require.NotNil(t, parked.Fence)
	_, changed, err := store.AppendPRDevelopmentLedgerAttempt(
		context.Background(),
		validPRDevelopmentLedgerAttemptAppendForTest(
			snapshot.Case.ID,
			fixture.Operation.Attempt.ID,
		),
	)
	require.NoError(t, err)
	require.True(t, changed)
	reviewLease, acquired, err := store.AcquirePRDevelopmentControllerLease(
		context.Background(),
		PRDevelopmentControllerAcquire{
			CaseID:           snapshot.Case.ID,
			AttemptID:        fixture.Operation.Attempt.ID,
			ExpectedRevision: parked.Controller.Revision,
			Kind:             PRDevelopmentControllerReviewLease,
			WorkerLabel:      "attention-later-review",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	reviewInput := validPRDevelopmentLedgerReviewAppendForTest(
		snapshot.Case.ID,
		fixture.Operation.Attempt.ID,
		reviewLease.Controller,
	)
	reviewInput.Summary = "The later immutable fence passed local review."
	reviewInput.Outcome = PRDevelopmentLedgerReviewPassed
	reviewInput.Findings = nil
	_, changed, err = store.AppendPRDevelopmentLedgerReview(
		context.Background(),
		reviewInput,
	)
	require.NoError(t, err)
	require.True(t, changed)

	advanced, err := store.GetPRDevelopmentContextSnapshot(
		context.Background(),
		snapshot.Case.ID,
	)
	require.NoError(t, err)
	assert.Len(t, advanced.Ledger.Entries, snapshot.HighWater.LedgerEntryCount+2)
	controller, err := store.GetPRDevelopmentControllerForCase(
		context.Background(),
		snapshot.Case.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, snapshot.HighWater.ControllerFenceCount+1, controller.FenceCount)

	got, err := store.GetPRDevelopmentAttentionDecisionRun(
		context.Background(),
		admission.Key,
	)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	replay := admission
	replay.Snapshot = PRDevelopmentAttentionHighWater{}
	var calls atomic.Int32
	replayed, existed, err := store.AdmitPRDevelopmentAttentionDecisionRun(
		context.Background(),
		replay,
		func(context.Context) error {
			calls.Add(1)
			return nil
		},
	)
	require.NoError(t, err)
	assert.True(t, existed)
	assert.Equal(t, want, replayed)
	assert.Zero(t, calls.Load())
}

func TestStorePRDevelopmentAttentionSnapshotRequiresAttentionReadyTail(t *testing.T) {
	t.Parallel()

	fixture, _ := newCompletedPRDevelopmentAIReviewFixture(t, PRDevelopmentCIPassed)
	_, err := fixture.Operation.Store.GetPRDevelopmentAttentionSnapshot(
		context.Background(),
		fixture.Operation.Case.ID,
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentAttentionConflict)

	lease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
	passed := validPRDevelopmentAIReviewCompletionForTest(
		lease,
		PRDevelopmentLedgerReviewPassed,
	)
	_, changed, err := fixture.Operation.Store.CompletePRDevelopmentReview(
		context.Background(),
		passed,
	)
	require.NoError(t, err)
	require.True(t, changed)
	_, err = fixture.Operation.Store.GetPRDevelopmentAttentionSnapshot(
		context.Background(),
		fixture.Operation.Case.ID,
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentAttentionConflict)
}

func TestStoreMigratesV14ToPRDevelopmentAttentionSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v14-pr-development-attention.db")
	db := openSchemaTestDB(t, path)
	for _, schema := range []string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7,
		schemaV8, schemaV9, schemaV10, schemaV11, schemaV12, schemaV13, schemaV14,
	} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
	setSchemaTestVersion(t, db, 14)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	assert.True(t, schemaObjectExists(
		t,
		store.db,
		"table",
		"pr_development_attention_decision_runs",
	))
	assert.True(t, schemaObjectExists(
		t,
		store.db,
		"index",
		"pr_development_ledger_entries_attention",
	))
	var version int
	require.NoError(t, store.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, schemaVersion, version)
}

func TestStorePRDevelopmentAttentionSchemaV15ValidationRollsBackMigration(
	t *testing.T,
) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v15-pr-development-attention-rollback.db")
	db := openSchemaTestDB(t, path)
	for _, schema := range []string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7,
		schemaV8, schemaV9, schemaV10, schemaV11, schemaV12, schemaV13, schemaV14,
	} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
	setSchemaTestVersion(t, db, 14)
	malformed := strings.Replace(
		schemaV15PRDevelopmentAttentionDecisionRunsTable,
		"length(CAST(decision_point AS BLOB)) <= 128",
		"length(CAST(decision_point AS BLOB)) <= 129",
		1,
	)
	require.NotEqual(t, schemaV15PRDevelopmentAttentionDecisionRunsTable, malformed)
	_, err := db.Exec(malformed)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v15")

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 14, version)
	assert.False(t, schemaObjectExists(
		t,
		db,
		"index",
		"pr_development_ledger_entries_attention",
	), "the v15 parent index must roll back with the failed migration")
}

func newPRDevelopmentAttentionFixture(
	t *testing.T,
) (*Store, PRDevelopmentAttentionSnapshot) {
	t.Helper()
	fixture, snapshot := newPRDevelopmentAttentionOrchestrationFixtureAt(t, ":memory:")
	return fixture.Operation.Store, snapshot
}

func newPRDevelopmentAttentionOrchestrationFixtureAt(
	t *testing.T,
	databasePath string,
) (*prDevelopmentOrchestrationFixture, PRDevelopmentAttentionSnapshot) {
	t.Helper()
	store, clock, capture := newPRDevelopmentStoreFixture(t, databasePath)
	fixture := newPRDevelopmentAIReviewOrchestrationOnStore(
		t,
		store,
		clock,
		capture,
		"pr-development-attention-attempt",
		"gw-pr-development-attention-line",
		1500,
	)
	completePRDevelopmentAIReviewFixture(t, fixture, PRDevelopmentCIPassed, 9501)
	lease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
	attention := validPRDevelopmentAIReviewCompletionForTest(
		lease,
		PRDevelopmentLedgerReviewAttentionRequired,
	)
	_, changed, err := fixture.Operation.Store.CompletePRDevelopmentReview(
		context.Background(),
		attention,
	)
	require.NoError(t, err)
	require.True(t, changed)
	snapshot, err := fixture.Operation.Store.GetPRDevelopmentAttentionSnapshot(
		context.Background(),
		fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	return fixture, snapshot
}

func testPRDevelopmentAttentionAdmission(
	snapshot PRDevelopmentAttentionSnapshot,
	runID string,
) PRDevelopmentAttentionDecisionRunAdmission {
	return PRDevelopmentAttentionDecisionRunAdmission{
		Key: PRDevelopmentAttentionDecisionKey{
			CaseID:              snapshot.HighWater.CaseID,
			ReviewEntryID:       snapshot.HighWater.ReviewEntryID,
			ReviewEntryHash:     snapshot.HighWater.ReviewEntryHash,
			ConversationVersion: snapshot.HighWater.ConversationVersion,
			SubjectRevision:     "sha256:" + strings.Repeat("a", 64),
			DecisionPoint:       "pr_development.review_attention_required",
			PolicyRevision:      "sha256:" + strings.Repeat("b", 64),
		},
		Snapshot: snapshot.HighWater,
		RunID:    runID,
	}
}
