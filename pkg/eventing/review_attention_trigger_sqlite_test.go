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
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreSubmittedReviewAtomicallyEnqueuesAttentionOccurrence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, reviewCase, submission := newClaimedReviewSubmission(t)
	detail, err := store.FinishReviewSubmission(ctx, ReviewSubmissionOutcome{
		SubmissionID:     submission.ID,
		LeaseToken:       submission.LeaseToken,
		Status:           ReviewSubmissionSubmitted,
		ExternalReviewID: "review-123",
	})
	require.NoError(t, err)

	trigger, err := store.GetReviewAttentionTrigger(ctx, submission.ID)
	require.NoError(t, err)
	assert.Equal(t, submission.ID, trigger.SubmissionID)
	assert.Equal(t, reviewCase.ID, trigger.CaseID)
	assert.Equal(t, detail.Case.Version, trigger.CaseVersion)
	assert.Equal(t, ReviewAttentionDecisionSubmitted, trigger.DecisionPoint)
	assert.Equal(t, ReviewAttentionPending, trigger.Status)
	assert.Equal(t, *clock, trigger.AvailableAt)
	assert.Zero(t, trigger.Attempts)
	assert.Empty(t, trigger.PolicyRevision)
	assert.Empty(t, trigger.PinnedPolicy)
	assert.Empty(t, trigger.RunID)
	assert.Nil(t, trigger.CompletedAt)
}

func TestStoreNonSubmittedReviewOutcomesNeverEnqueueAttention(t *testing.T) {
	t.Parallel()

	for _, outcome := range []ReviewSubmissionOutcome{
		{Status: ReviewSubmissionUnknown, PublicErrorCode: "outcome_unknown"},
		{Status: ReviewSubmissionFailed, PublicErrorCode: "github_rejected"},
	} {
		t.Run(string(outcome.Status), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, _, _, submission := newClaimedReviewSubmission(t)
			outcome.SubmissionID = submission.ID
			outcome.LeaseToken = submission.LeaseToken
			_, err := store.FinishReviewSubmission(ctx, outcome)
			require.NoError(t, err)
			_, err = store.GetReviewAttentionTrigger(ctx, submission.ID)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	}
}

func TestStoreAttentionEnqueueConflictRollsBackSubmissionOutcome(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, reviewCase, submission := newClaimedReviewSubmission(t)
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO pr_review_attention_triggers (
			submission_id, case_id, case_version, decision_point, status,
			available_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		submission.ID,
		reviewCase.ID,
		reviewCase.Version+2,
		ReviewAttentionDecisionSubmitted,
		ReviewAttentionPending,
		toDBTime(*clock),
		toDBTime(*clock),
		toDBTime(*clock),
	)
	require.NoError(t, err)

	_, err = store.FinishReviewSubmission(ctx, ReviewSubmissionOutcome{
		SubmissionID: submission.ID,
		LeaseToken:   submission.LeaseToken,
		Status:       ReviewSubmissionSubmitted,
	})
	require.Error(t, err)

	storedSubmission, err := store.GetReviewSubmission(ctx, submission.ID)
	require.NoError(t, err)
	assert.Equal(t, ReviewSubmissionClaimed, storedSubmission.Status)
	assert.Equal(t, submission.LeaseToken, storedSubmission.LeaseToken)
	detail, err := store.GetReviewCase(ctx, reviewCase.ID)
	require.NoError(t, err)
	assert.Equal(t, ReviewCaseSubmitting, detail.Case.Status)
	assert.Equal(t, reviewCase.Version+1, detail.Case.Version)
}

func TestStoreReviewReconciliationEnqueuesOnlyConfirmedSubmittedOccurrence(t *testing.T) {
	t.Parallel()

	t.Run("submitted", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, _, reviewCase, submission := newClaimedReviewSubmission(t)
		unknown, err := store.FinishReviewSubmission(ctx, ReviewSubmissionOutcome{
			SubmissionID: submission.ID,
			LeaseToken:   submission.LeaseToken,
			Status:       ReviewSubmissionUnknown,
		})
		require.NoError(t, err)
		_, err = store.GetReviewAttentionTrigger(ctx, submission.ID)
		assert.ErrorIs(t, err, ErrNotFound)

		resolved, err := store.ReconcileReviewSubmission(ctx, ReviewSubmissionReconciliation{
			CaseID:          reviewCase.ID,
			ExpectedVersion: unknown.Case.Version,
			Resolution:      ReviewReconciliationSubmitted,
		})
		require.NoError(t, err)
		trigger, err := store.GetReviewAttentionTrigger(ctx, submission.ID)
		require.NoError(t, err)
		assert.Equal(t, resolved.Case.Version, trigger.CaseVersion)
		assert.Equal(t, ReviewAttentionPending, trigger.Status)
	})

	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, _, reviewCase, submission := newClaimedReviewSubmission(t)
		unknown, err := store.FinishReviewSubmission(ctx, ReviewSubmissionOutcome{
			SubmissionID: submission.ID,
			LeaseToken:   submission.LeaseToken,
			Status:       ReviewSubmissionUnknown,
		})
		require.NoError(t, err)
		_, err = store.ReconcileReviewSubmission(ctx, ReviewSubmissionReconciliation{
			CaseID:          reviewCase.ID,
			ExpectedVersion: unknown.Case.Version,
			Resolution:      ReviewReconciliationAbsent,
		})
		require.NoError(t, err)
		_, err = store.GetReviewAttentionTrigger(ctx, submission.ID)
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

func TestStoreReviewAttentionTriggerPinnedRetryAndDelivery(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, _, submission := newSubmittedReviewAttentionFixture(t)
	claimed, err := store.ClaimReviewAttentionTriggers(ctx, "attention-a", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	assert.Equal(t, ReviewAttentionClaimed, claimed[0].Status)
	assert.Equal(t, 1, claimed[0].Attempts)
	assert.NotEmpty(t, claimed[0].LeaseToken)
	firstLease := claimed[0].LeaseToken

	err = store.CompleteReviewAttentionTrigger(ctx, ReviewAttentionTriggerCompletion{
		SubmissionID: submission.ID,
		LeaseToken:   firstLease,
		Status:       ReviewAttentionNoop,
	})
	assert.ErrorIs(t, err, ErrInvalidTransition)

	revision := "sha256:" + strings.Repeat("a", 64)
	policy := json.RawMessage(
		`{"version":1,"source_revision":"config-7",` +
			`"resolution":{"mode":"replace","effective":[]}}`,
	)
	original := append(json.RawMessage(nil), policy...)
	pinned, err := store.PinReviewAttentionTriggerPolicy(ctx, ReviewAttentionPolicyPin{
		SubmissionID:   submission.ID,
		LeaseToken:     firstLease,
		PolicyRevision: revision,
		PinnedPolicy:   policy,
	})
	require.NoError(t, err)
	assert.Equal(t, revision, pinned.PolicyRevision)
	assert.Equal(t, []byte(original), []byte(pinned.PinnedPolicy))
	policy[1] = 'X'
	stored, err := store.GetReviewAttentionTrigger(ctx, submission.ID)
	require.NoError(t, err)
	assert.Equal(t, []byte(original), []byte(stored.PinnedPolicy))

	_, err = store.PinReviewAttentionTriggerPolicy(ctx, ReviewAttentionPolicyPin{
		SubmissionID:   submission.ID,
		LeaseToken:     firstLease,
		PolicyRevision: revision,
		PinnedPolicy:   original,
	})
	require.NoError(t, err, "an exact pin retry must be idempotent")
	_, err = store.PinReviewAttentionTriggerPolicy(ctx, ReviewAttentionPolicyPin{
		SubmissionID:   submission.ID,
		LeaseToken:     firstLease,
		PolicyRevision: "sha256:" + strings.Repeat("b", 64),
		PinnedPolicy:   original,
	})
	assert.ErrorIs(t, err, ErrReviewConflict)

	require.NoError(t, store.RenewReviewAttentionTriggerLease(
		ctx,
		submission.ID,
		firstLease,
		2*time.Minute,
	))
	availableAt := (*clock).Add(5 * time.Minute)
	require.NoError(t, store.ReleaseReviewAttentionTrigger(ctx, ReviewAttentionTriggerRelease{
		SubmissionID: submission.ID,
		LeaseToken:   firstLease,
		AvailableAt:  availableAt,
		Error:        strings.Repeat("é", maxErrorDetailBytes),
	}))
	released, err := store.GetReviewAttentionTrigger(ctx, submission.ID)
	require.NoError(t, err)
	assert.Equal(t, ReviewAttentionPending, released.Status)
	assert.Equal(t, availableAt, released.AvailableAt)
	assert.Equal(t, revision, released.PolicyRevision)
	assert.Equal(t, []byte(original), []byte(released.PinnedPolicy))
	assert.LessOrEqual(t, len(released.LastError), maxErrorDetailBytes)
	assert.True(t, utf8.ValidString(released.LastError))

	claimed, err = store.ClaimReviewAttentionTriggers(ctx, "attention-b", 1, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, claimed, "released trigger must respect retry availability")
	*clock = (*clock).Add(6 * time.Minute)
	claimed, err = store.ClaimReviewAttentionTriggers(ctx, "attention-b", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	assert.Equal(t, 2, claimed[0].Attempts)
	assert.NotEqual(t, firstLease, claimed[0].LeaseToken)
	assert.Equal(t, []byte(original), []byte(claimed[0].PinnedPolicy))
	assert.ErrorIs(t, store.RenewReviewAttentionTriggerLease(
		ctx,
		submission.ID,
		firstLease,
		time.Minute,
	), ErrStaleLease)

	runID := "wr_" + strings.Repeat("1", 32)
	require.NoError(t, store.CompleteReviewAttentionTrigger(
		ctx,
		ReviewAttentionTriggerCompletion{
			SubmissionID: submission.ID,
			LeaseToken:   claimed[0].LeaseToken,
			Status:       ReviewAttentionDelivered,
			RunID:        runID,
		},
	))
	completed, err := store.GetReviewAttentionTrigger(ctx, submission.ID)
	require.NoError(t, err)
	assert.Equal(t, ReviewAttentionDelivered, completed.Status)
	assert.Equal(t, runID, completed.RunID)
	assert.NotNil(t, completed.CompletedAt)
	assert.Empty(t, completed.LastError)
	assert.Empty(t, completed.LeaseToken)
	claimed, err = store.ClaimReviewAttentionTriggers(ctx, "attention-c", 10, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, claimed, "terminal trigger must never be reclaimed")

	publicJSON, err := json.Marshal(completed)
	require.NoError(t, err)
	for _, privateField := range []string{
		"lease_token", "lease_until", "policy_revision", "pinned_policy", "run_id", "last_error",
	} {
		assert.NotContains(t, string(publicJSON), privateField)
	}
}

func TestStoreReviewAttentionExpiredClaimIsReclaimedWithPinnedPolicy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, _, submission := newSubmittedReviewAttentionFixture(t)
	claimed, err := store.ClaimReviewAttentionTriggers(ctx, "attention-a", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	firstLease := claimed[0].LeaseToken
	policy := json.RawMessage(`{"version":1,"resolution":{"effective":[{"kind":"zero"}]}}`)
	_, err = store.PinReviewAttentionTriggerPolicy(ctx, ReviewAttentionPolicyPin{
		SubmissionID:   submission.ID,
		LeaseToken:     firstLease,
		PolicyRevision: "sha256:" + strings.Repeat("c", 64),
		PinnedPolicy:   policy,
	})
	require.NoError(t, err)

	*clock = (*clock).Add(2 * time.Minute)
	reclaimed, err := store.ClaimReviewAttentionTriggers(ctx, "attention-b", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	assert.NotEqual(t, firstLease, reclaimed[0].LeaseToken)
	assert.Equal(t, 2, reclaimed[0].Attempts)
	assert.Equal(t, []byte(policy), []byte(reclaimed[0].PinnedPolicy))
	assert.ErrorIs(t, store.ReleaseReviewAttentionTrigger(
		ctx,
		ReviewAttentionTriggerRelease{
			SubmissionID: submission.ID,
			LeaseToken:   firstLease,
		},
	), ErrStaleLease)
	require.NoError(t, store.CompleteReviewAttentionTrigger(
		ctx,
		ReviewAttentionTriggerCompletion{
			SubmissionID: submission.ID,
			LeaseToken:   reclaimed[0].LeaseToken,
			Status:       ReviewAttentionNoop,
		},
	))
	completed, err := store.GetReviewAttentionTrigger(ctx, submission.ID)
	require.NoError(t, err)
	assert.Equal(t, ReviewAttentionNoop, completed.Status)
	assert.Empty(t, completed.RunID)
}

func TestStoreReviewAttentionConcurrentClaimsHaveOneFencedOwner(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "review-attention-claims.db")
	store, clock, _, submission := newSubmittedReviewAttentionFixtureAt(t, databasePath)
	now := *clock
	peer, err := Open(ctx, databasePath, WithClock(func() time.Time { return now }))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, peer.Close()) })

	type claimResult struct {
		triggers []ReviewAttentionTrigger
		err      error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, claimant := range []struct {
		queue *Store
		label string
	}{{store, "attention-a"}, {peer, "attention-b"}} {
		go func() {
			ready.Done()
			<-start
			triggers, claimErr := claimant.queue.ClaimReviewAttentionTriggers(
				ctx,
				claimant.label,
				1,
				time.Minute,
			)
			results <- claimResult{triggers: triggers, err: claimErr}
		}()
	}
	ready.Wait()
	close(start)
	first := <-results
	second := <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	all := append(first.triggers, second.triggers...)
	require.Len(t, all, 1, "one occurrence must have exactly one concurrent owner")
	winningLease := all[0].LeaseToken
	require.NotEmpty(t, winningLease)

	foreignLease := "lease_foreign_token"
	assert.ErrorIs(t, store.RenewReviewAttentionTriggerLease(
		ctx,
		submission.ID,
		foreignLease,
		time.Minute,
	), ErrStaleLease)
	_, err = store.PinReviewAttentionTriggerPolicy(ctx, ReviewAttentionPolicyPin{
		SubmissionID:   submission.ID,
		LeaseToken:     foreignLease,
		PolicyRevision: "sha256:" + strings.Repeat("e", 64),
		PinnedPolicy:   json.RawMessage(`{"version":1}`),
	})
	assert.ErrorIs(t, err, ErrStaleLease)
	assert.ErrorIs(t, store.ReleaseReviewAttentionTrigger(
		ctx,
		ReviewAttentionTriggerRelease{
			SubmissionID: submission.ID,
			LeaseToken:   foreignLease,
		},
	), ErrStaleLease)
	assert.ErrorIs(t, store.CompleteReviewAttentionTrigger(
		ctx,
		ReviewAttentionTriggerCompletion{
			SubmissionID: submission.ID,
			LeaseToken:   foreignLease,
			Status:       ReviewAttentionNoop,
		},
	), ErrStaleLease)

	require.NoError(t, store.ReleaseReviewAttentionTrigger(
		ctx,
		ReviewAttentionTriggerRelease{
			SubmissionID: submission.ID,
			LeaseToken:   winningLease,
		},
	))
	reclaimed, err := peer.ClaimReviewAttentionTriggers(ctx, "attention-c", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	assert.NotEqual(t, winningLease, reclaimed[0].LeaseToken)
	assert.ErrorIs(t, store.RenewReviewAttentionTriggerLease(
		ctx,
		submission.ID,
		winningLease,
		time.Minute,
	), ErrStaleLease, "the prior owner must be fenced after reclaim")
}

func TestStoreReviewAttentionPolicyPinHasExactThreeMiBBound(t *testing.T) {
	ctx := context.Background()
	store, _, _, submission := newSubmittedReviewAttentionFixture(t)
	claimed, err := store.ClaimReviewAttentionTriggers(ctx, "attention", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	revision := "sha256:" + strings.Repeat("d", 64)

	for name, policy := range map[string]json.RawMessage{
		"empty":    nil,
		"not json": json.RawMessage(`{"unterminated":`),
		"too large": json.RawMessage(
			`"` + strings.Repeat("x", maxReviewAttentionPolicyBytes) + `"`,
		),
	} {
		t.Run(name, func(t *testing.T) {
			_, pinErr := store.PinReviewAttentionTriggerPolicy(ctx, ReviewAttentionPolicyPin{
				SubmissionID:   submission.ID,
				LeaseToken:     claimed[0].LeaseToken,
				PolicyRevision: revision,
				PinnedPolicy:   policy,
			})
			assert.ErrorIs(t, pinErr, ErrInvalidReview)
		})
	}

	exact := json.RawMessage(
		`"` + strings.Repeat("x", maxReviewAttentionPolicyBytes-2) + `"`,
	)
	pinned, err := store.PinReviewAttentionTriggerPolicy(ctx, ReviewAttentionPolicyPin{
		SubmissionID:   submission.ID,
		LeaseToken:     claimed[0].LeaseToken,
		PolicyRevision: revision,
		PinnedPolicy:   exact,
	})
	require.NoError(t, err)
	assert.Len(t, pinned.PinnedPolicy, maxReviewAttentionPolicyBytes)
}

func TestStoreMigratesV4ToReviewAttentionTriggerSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v4-review-attention.db")
	db := openSchemaTestDB(t, path)
	_, err := db.Exec(schemaV1)
	require.NoError(t, err)
	_, err = db.Exec(schemaV2)
	require.NoError(t, err)
	_, err = db.Exec(schemaV3)
	require.NoError(t, err)
	_, err = db.Exec(schemaV4)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 4)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	defer store.Close()
	assert.True(t, schemaObjectExists(t, store.db, "table", "pr_review_attention_triggers"))
	assert.True(t, schemaObjectExists(t, store.db, "index", "pr_review_attention_triggers_claim"))
	var version int
	require.NoError(t, store.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, schemaVersion, version)
}

func TestStoreV4MigrationDoesNotBackfillHistoricalSubmittedReviews(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration-v4-no-attention-backfill.db")
	store, _, _, submission := newSubmittedReviewAttentionFixtureAt(t, path)
	_, err := store.db.Exec(`DELETE FROM pr_review_attention_triggers`)
	require.NoError(t, err)
	_, err = store.db.Exec(`DROP TABLE pr_review_attention_triggers`)
	require.NoError(t, err)
	setSchemaTestVersion(t, store.db, 4)
	require.NoError(t, store.Close())

	migrated, err := Open(ctx, path)
	require.NoError(t, err)
	defer migrated.Close()
	storedSubmission, err := migrated.GetReviewSubmission(ctx, submission.ID)
	require.NoError(t, err)
	assert.Equal(t, ReviewSubmissionSubmitted, storedSubmission.Status)
	_, err = migrated.GetReviewAttentionTrigger(ctx, submission.ID)
	assert.ErrorIs(t, err, ErrNotFound, "migration must not invent old event occurrences")
}

func TestStoreReviewAttentionMigrationValidationFailureRollsBackVersion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v5-review-attention-rollback.db")
	db := openSchemaTestDB(t, path)
	_, err := db.Exec(schemaV1)
	require.NoError(t, err)
	_, err = db.Exec(schemaV2)
	require.NoError(t, err)
	_, err = db.Exec(schemaV3)
	require.NoError(t, err)
	_, err = db.Exec(schemaV4)
	require.NoError(t, err)
	malformed := strings.Replace(
		schemaV5ReviewAttentionTriggersTable,
		"UNIQUE(case_id, case_version, decision_point)",
		"UNIQUE(case_id, case_version)",
		1,
	)
	_, err = db.Exec(malformed)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 4)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v5")

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 4, version)
	assert.False(t, schemaObjectExists(t, db, "index", "pr_review_attention_triggers_claim"))
}

func TestStoreRejectsCurrentReviewAttentionSchemaWithoutClaimIndex(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "review-attention-missing-index.db")
	db := openSchemaTestDB(t, path)
	installSchemaV1ForTest(t, db)
	_, err := db.Exec(`DROP INDEX pr_review_attention_triggers_claim`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v5")
}

func newClaimedReviewSubmission(
	t *testing.T,
) (*Store, *time.Time, ReviewCase, ReviewSubmission) {
	t.Helper()
	return newClaimedReviewSubmissionAt(t, ":memory:")
}

func newClaimedReviewSubmissionAt(
	t *testing.T,
	databasePath string,
) (*Store, *time.Time, ReviewCase, ReviewSubmission) {
	t.Helper()
	ctx := context.Background()
	store, clock, input := newReviewStoreFixtureAt(t, databasePath)
	reviewCase, created, err := store.CaptureReview(ctx, input)
	require.NoError(t, err)
	require.True(t, created)
	detail, err := store.CreateReviewSubmission(ctx, ReviewSubmissionDraft{
		CaseID:          reviewCase.ID,
		ExpectedVersion: reviewCase.Version,
		Marker:          "picoclaw-review/" + reviewCase.ID + "/1",
		Request:         json.RawMessage(`{"event":"COMMENT"}`),
	})
	require.NoError(t, err)
	require.NotNil(t, detail.Submission)
	claimed, err := store.ClaimReviewSubmissions(ctx, "review-poster", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	return store, clock, reviewCase, claimed[0]
}

func newSubmittedReviewAttentionFixture(
	t *testing.T,
) (*Store, *time.Time, ReviewCaseDetail, ReviewSubmission) {
	t.Helper()
	return newSubmittedReviewAttentionFixtureAt(t, ":memory:")
}

func newSubmittedReviewAttentionFixtureAt(
	t *testing.T,
	databasePath string,
) (*Store, *time.Time, ReviewCaseDetail, ReviewSubmission) {
	t.Helper()
	ctx := context.Background()
	store, clock, _, submission := newClaimedReviewSubmissionAt(t, databasePath)
	detail, err := store.FinishReviewSubmission(ctx, ReviewSubmissionOutcome{
		SubmissionID: submission.ID,
		LeaseToken:   submission.LeaseToken,
		Status:       ReviewSubmissionSubmitted,
	})
	require.NoError(t, err)
	return store, clock, detail, submission
}
