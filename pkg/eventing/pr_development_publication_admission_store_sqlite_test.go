//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompletePRDevelopmentReviewAtomicallyEnqueuesPublication(t *testing.T) {
	t.Parallel()

	fixture, _ := newCompletedPRDevelopmentAIReviewFixture(t, PRDevelopmentCIPassed)
	store := fixture.Operation.Store
	ctx := context.Background()
	lease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
	input := validPRDevelopmentAIReviewCompletionForTest(
		lease,
		PRDevelopmentLedgerReviewPassed,
	)

	completion, changed, err := store.CompletePRDevelopmentReview(ctx, input)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, completion.Publication)
	publication := *completion.Publication

	assert.True(t, validPrefixedHexID(publication.ID, prDevelopmentPublicationIDPrefix))
	assert.Equal(t, lease.CaseID, publication.CaseID)
	assert.Equal(t, lease.Controller.ThreadID, publication.ThreadID)
	assert.Equal(t, lease.Controller.ID, publication.ControllerID)
	assert.Equal(t, completion.Controller.Revision, publication.ControllerRevision)
	assert.Equal(t, lease.Controller.OwnerSessionID, publication.OwnerSessionID)
	assert.Equal(t, lease.Fence.AttemptID, publication.AttemptID)
	assert.Equal(t, lease.Fence.Ordinal, publication.FenceOrdinal)
	assert.Equal(t, completion.Entry.FenceHash, publication.FenceHash)
	assert.Equal(t, completion.Entry.ID, publication.ReviewLedgerEntryID)
	assert.Equal(t, completion.Entry.EntryHash, publication.ReviewLedgerEntryHash)
	assert.Equal(t, PRDevelopmentLedgerReviewPassed, publication.ReviewOutcome)
	assert.Equal(t, PRDevelopmentCIPassed, publication.CIStatus)
	assert.Equal(t, PRDevelopmentPublicationPending, publication.Status)
	assert.Empty(t, publication.ClaimToken)
	assert.Nil(t, publication.ClaimUntil)
	assert.False(t, publication.CreatedAt.IsZero())
	assert.Equal(t, publication.CreatedAt, publication.UpdatedAt)
	encoded, err := json.Marshal(publication)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(encoded))

	snapshot, err := store.GetPRDevelopmentContextSnapshot(ctx, lease.CaseID)
	require.NoError(t, err)
	require.Len(t, snapshot.Ledger.Entries, 2)
	attempt := snapshot.Ledger.Entries[0]
	review := snapshot.Ledger.Entries[1]
	assert.Equal(t, attempt.ID, publication.AttemptLedgerEntryID)
	assert.Equal(t, attempt.EntryHash, publication.AttemptLedgerEntryHash)
	assert.Equal(t, review.ID, publication.ReviewLedgerEntryID)
	assert.Equal(t, review.EntryHash, publication.ReviewLedgerEntryHash)

	stored, err := store.GetPRDevelopmentPublicationForReview(ctx, review.ID)
	require.NoError(t, err)
	assert.Equal(t, publication, stored)

	replayed, changed, err := store.CompletePRDevelopmentReview(ctx, input)
	require.NoError(t, err)
	assert.False(t, changed)
	require.NotNil(t, replayed.Publication)
	assert.Equal(t, publication, *replayed.Publication)

	var count int
	require.NoError(t, store.db.QueryRow(`
		SELECT count(*) FROM pr_development_publications
		WHERE review_ledger_entry_id = ?`, review.ID).Scan(&count))
	assert.Equal(t, 1, count)

	claimed, err := store.ClaimPRDevelopmentPublications(
		ctx,
		PRDevelopmentPublicationClaimRequest{
			WorkerLabel: "publication-admission-redaction",
			Limit:       1,
			Lease:       time.Minute,
		},
	)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NotEmpty(t, claimed[0].ClaimToken)
	replayed, changed, err = store.CompletePRDevelopmentReview(ctx, input)
	require.NoError(t, err)
	assert.False(t, changed)
	require.NotNil(t, replayed.Publication)
	assert.Empty(t, replayed.Publication.ClaimToken)
	require.NoError(t, store.RenewPRDevelopmentPublication(
		ctx,
		PRDevelopmentPublicationRenew{
			PublicationID: claimed[0].ID,
			ClaimToken:    claimed[0].ClaimToken,
			ClaimEpoch:    claimed[0].ClaimEpoch,
			Lease:         10 * time.Minute,
		},
	))
}

func TestCompletePRDevelopmentReviewOnlyEnqueuesPassedPublication(t *testing.T) {
	t.Parallel()

	for _, outcome := range []PRDevelopmentLedgerReviewOutcome{
		PRDevelopmentLedgerReviewChangesRequired,
		PRDevelopmentLedgerReviewAttentionRequired,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()

			fixture, _ := newCompletedPRDevelopmentAIReviewFixture(
				t,
				PRDevelopmentCIPassed,
			)
			store := fixture.Operation.Store
			lease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
			completion, changed, err := store.CompletePRDevelopmentReview(
				context.Background(),
				validPRDevelopmentAIReviewCompletionForTest(lease, outcome),
			)
			require.NoError(t, err)
			require.True(t, changed)
			assert.Nil(t, completion.Publication)

			var count int
			require.NoError(t, store.db.QueryRow(
				`SELECT count(*) FROM pr_development_publications`,
			).Scan(&count))
			assert.Zero(t, count)
		})
	}
}

func TestCompletePRDevelopmentReviewRollsBackWhenPublicationEnqueueFails(t *testing.T) {
	t.Parallel()

	fixture, _ := newCompletedPRDevelopmentAIReviewFixture(t, PRDevelopmentCIPassed)
	store := fixture.Operation.Store
	ctx := context.Background()
	lease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
	_, err := store.db.Exec(`CREATE TRIGGER reject_development_publication_enqueue
		BEFORE INSERT ON pr_development_publications
		BEGIN SELECT RAISE(ABORT, 'injected publication enqueue failure'); END`)
	require.NoError(t, err)

	_, changed, err := store.CompletePRDevelopmentReview(
		ctx,
		validPRDevelopmentAIReviewCompletionForTest(
			lease,
			PRDevelopmentLedgerReviewPassed,
		),
	)
	require.Error(t, err)
	assert.False(t, changed)

	controller, found, err := loadPRDevelopmentControllerAggregateByID(
		ctx,
		store.db,
		lease.Controller.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, PRDevelopmentControllerReview, controller.Phase)
	assert.Equal(t, lease.Controller.LeaseToken, controller.LeaseToken)

	fence, found, err := loadPRDevelopmentReviewFenceByAttempt(
		ctx,
		store.db,
		lease.Fence.AttemptID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Nil(t, fence.ReviewedAt)

	snapshot, err := store.GetPRDevelopmentContextSnapshot(ctx, lease.CaseID)
	require.NoError(t, err)
	require.Len(t, snapshot.Ledger.Entries, 1)
	assert.Equal(t, PRDevelopmentLedgerAttempt, snapshot.Ledger.Entries[0].Kind)

	var count int
	require.NoError(t, store.db.QueryRow(
		`SELECT count(*) FROM pr_development_publications`,
	).Scan(&count))
	assert.Zero(t, count)
}

func TestAppendPRDevelopmentLedgerReviewCannotBypassPassedPublication(t *testing.T) {
	t.Parallel()

	fixture, _ := newCompletedPRDevelopmentAIReviewFixture(t, PRDevelopmentCIPassed)
	store := fixture.Operation.Store
	ctx := context.Background()
	lease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
	input := validPRDevelopmentAIReviewCompletionForTest(
		lease,
		PRDevelopmentLedgerReviewPassed,
	)

	_, changed, err := store.AppendPRDevelopmentLedgerReview(ctx, input)
	assert.ErrorIs(t, err, ErrPRDevelopmentLedgerConflict)
	assert.False(t, changed)

	controller, found, err := loadPRDevelopmentControllerAggregateByID(
		ctx,
		store.db,
		lease.Controller.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, PRDevelopmentControllerReview, controller.Phase)
	assert.Equal(t, lease.Controller.LeaseToken, controller.LeaseToken)
	fence, found, err := loadPRDevelopmentReviewFenceByAttempt(
		ctx,
		store.db,
		lease.Fence.AttemptID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Nil(t, fence.ReviewedAt)

	var publications int
	require.NoError(t, store.db.QueryRow(
		`SELECT count(*) FROM pr_development_publications`,
	).Scan(&publications))
	assert.Zero(t, publications)

	completion, changed, err := store.CompletePRDevelopmentReview(ctx, input)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, completion.Publication)
}
