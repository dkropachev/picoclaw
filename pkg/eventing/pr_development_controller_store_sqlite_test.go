//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorePRDevelopmentControllerLifecycleRetainsLineAndSeparatesReview(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(
		t,
		store,
		developmentCase.ID,
	)
	attempt := completed.Attempts[len(completed.Attempts)-1]

	mutation, acquired, err := store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: 0,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "mutation-worker",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	require.True(t, mutation.Created)
	assert.Equal(t, PRDevelopmentControllerMutation, mutation.Controller.Phase)
	assert.NotEmpty(t, mutation.Controller.MutationReservationKey)
	assert.Equal(
		t,
		completed.ReservationKey,
		mutation.Controller.MutationReservationKey,
		"first adoption must retain the reservation already owning the pinned workspace",
	)
	assert.Equal(t, completed.ID, mutation.Controller.OwnerSessionID)
	readOnlyMutation, err := store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentControllerMutation, readOnlyMutation.Phase)
	assert.Empty(t, readOnlyMutation.LeaseToken)
	assert.Empty(t, readOnlyMutation.MutationReservationKey)

	bind := PRDevelopmentControllerLineBind{
		ControllerID:     mutation.Controller.ID,
		AttemptID:        attempt.ID,
		ExpectedRevision: mutation.Controller.Revision,
		LeaseToken:       mutation.Controller.LeaseToken,
		LeaseEpoch:       mutation.Controller.LeaseEpoch,
		WorkspaceID:      completed.WorkspaceID,
		SourceCloneURL:   completed.CloneURL,
		SourceRef:        completed.HeadRef,
		SourceCommit:     completed.HeadSHA,
		SourceTree:       strings.Repeat("b", 40),
		LineVersion:      0,
		MutationEpoch:    1,
		TipCommit:        completed.HeadSHA,
		Tree:             strings.Repeat("b", 40),
	}
	changedPin := bind
	changedPin.SourceCommit = strings.Repeat("f", 40)
	changedPin.TipCommit = changedPin.SourceCommit
	_, changed, err := store.BindPRDevelopmentControllerLine(ctx, changedPin)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed)
	bound, changed, err := store.BindPRDevelopmentControllerLine(ctx, bind)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, int64(1), bound.MutationEpoch)
	boundAt := *clock
	*clock = clock.Add(-time.Minute)
	replayedBound, changed, err := store.BindPRDevelopmentControllerLine(ctx, bind)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, bound, replayedBound)
	*clock = boundAt

	record := PRDevelopmentAttemptReviewFenceRecord{
		ControllerID:     bound.ID,
		AttemptID:        attempt.ID,
		ExpectedRevision: bound.Revision,
		LeaseToken:       bound.LeaseToken,
		LeaseEpoch:       bound.LeaseEpoch,
		LineVersion:      1,
		MutationEpoch:    1,
		ParkIntentID:     "park-attempt-1",
		BaseCommit:       completed.HeadSHA,
		TipCommit:        strings.Repeat("c", 40),
		Tree:             strings.Repeat("d", 40),
		LineReviewDigest: strings.Repeat("e", 64),
	}
	fence, changed, err := store.RecordPRDevelopmentAttemptReviewFence(ctx, record)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, 0, fence.Ordinal)
	assert.NotEmpty(t, fence.MutationReservationDigest)
	assert.NotEmpty(t, fence.MutationLeaseTokenDigest)
	assert.Empty(t, fence.ReviewLeaseTokenDigest)

	replayed, changed, err := store.RecordPRDevelopmentAttemptReviewFence(ctx, record)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, fence, replayed)
	foreignRecord := record
	foreignRecord.LeaseToken = "lease_foreign"
	_, _, err = store.RecordPRDevelopmentAttemptReviewFence(ctx, foreignRecord)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)

	parked, err := store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentControllerReviewPending, parked.Phase)
	assert.Empty(t, parked.MutationReservationKey)
	assert.Empty(t, parked.LeaseToken)
	assert.Equal(t, completed.WorkspaceID, parked.WorkspaceID)
	assert.Equal(t, strings.Repeat("c", 40), parked.TipCommit)

	review, acquired, err := store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: parked.Revision,
			Kind:             PRDevelopmentControllerReviewLease,
			WorkerLabel:      "review-worker-a",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, review.ReviewFence)
	assert.Equal(t, PRDevelopmentControllerReview, review.Controller.Phase)
	assert.Empty(t, review.Controller.MutationReservationKey)
	oldReviewToken := review.Controller.LeaseToken
	oldReviewEpoch := review.Controller.LeaseEpoch
	released, err := store.ReleasePRDevelopmentControllerReview(
		ctx,
		PRDevelopmentControllerReviewTransition{
			ControllerID:     review.Controller.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: review.Controller.Revision,
			LeaseToken:       oldReviewToken,
			LeaseEpoch:       oldReviewEpoch,
		},
	)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentControllerReviewPending, released.Phase)
	assert.Empty(t, released.LeaseToken)
	_, err = store.ReleasePRDevelopmentControllerReview(
		ctx,
		PRDevelopmentControllerReviewTransition{
			ControllerID:     review.Controller.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: review.Controller.Revision,
			LeaseToken:       oldReviewToken,
			LeaseEpoch:       oldReviewEpoch,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)

	review, acquired, err = store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: released.Revision,
			Kind:             PRDevelopmentControllerReviewLease,
			WorkerLabel:      "review-worker-a2",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	oldReviewToken = review.Controller.LeaseToken
	oldReviewEpoch = review.Controller.LeaseEpoch

	*clock = clock.Add(2 * time.Minute)
	reclaimed, acquired, err := store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: review.Controller.Revision,
			Kind:             PRDevelopmentControllerReviewLease,
			WorkerLabel:      "review-worker-b",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	assert.True(t, reclaimed.Reclaimed)
	assert.NotEqual(t, oldReviewToken, reclaimed.Controller.LeaseToken)
	assert.Greater(t, reclaimed.Controller.LeaseEpoch, oldReviewEpoch)

	_, _, err = store.FinishPRDevelopmentControllerReview(
		ctx,
		PRDevelopmentControllerReviewTransition{
			ControllerID:     review.Controller.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: review.Controller.Revision,
			LeaseToken:       oldReviewToken,
			LeaseEpoch:       oldReviewEpoch,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	finish := PRDevelopmentControllerReviewTransition{
		ControllerID:     reclaimed.Controller.ID,
		AttemptID:        attempt.ID,
		ExpectedRevision: reclaimed.Controller.Revision,
		LeaseToken:       reclaimed.Controller.LeaseToken,
		LeaseEpoch:       reclaimed.Controller.LeaseEpoch,
	}
	ready, changed, err := store.FinishPRDevelopmentControllerReview(ctx, finish)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, PRDevelopmentControllerReady, ready.Phase)
	assert.Empty(t, ready.LeaseToken)

	replayedReady, changed, err := store.FinishPRDevelopmentControllerReview(ctx, finish)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, ready, replayedReady)
	foreignFinish := finish
	foreignFinish.LeaseToken = "lease_foreign"
	_, _, err = store.FinishPRDevelopmentControllerReview(ctx, foreignFinish)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)

	next, admitted, err := store.AdmitPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairAdmit{
			CaseID:                      developmentCase.ID,
			ExpectedConversationVersion: 0,
			ExpectedRepairVersion:       completed.Version,
			IdempotencyKey:              "controller-attempt-2",
			AgentID:                     "main",
			Instruction:                 "Address the next ordered review locally.",
		},
	)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NotNil(t, next.RepairSession)
	nextAttempt := next.RepairSession.Attempts[len(next.RepairSession.Attempts)-1]
	_, found, err := store.ClaimPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "legacy-worker", Lease: time.Minute},
	)
	require.NoError(t, err)
	assert.False(t, found, "controller-owned sessions must never return to the legacy queue")

	nextMutation, acquired, err := store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        nextAttempt.ID,
			ExpectedRevision: ready.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "mutation-worker-2",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	assert.NotEqual(
		t,
		mutation.Controller.MutationReservationKey,
		nextMutation.Controller.MutationReservationKey,
	)
	assert.Equal(t, ready.LineID, nextMutation.Controller.LineID)
	assert.Equal(t, ready.WorkspaceID, nextMutation.Controller.WorkspaceID)
	assert.Equal(
		t,
		ready.LineVersion,
		nextMutation.Controller.MutationEpoch,
		"a new bearer is not park-authorized until Resume is durably bound",
	)
	resumed, changed, err := store.BindPRDevelopmentControllerLine(
		ctx,
		PRDevelopmentControllerLineBind{
			ControllerID:     nextMutation.Controller.ID,
			AttemptID:        nextAttempt.ID,
			ExpectedRevision: nextMutation.Controller.Revision,
			LeaseToken:       nextMutation.Controller.LeaseToken,
			LeaseEpoch:       nextMutation.Controller.LeaseEpoch,
			WorkspaceID:      ready.WorkspaceID,
			SourceCloneURL:   ready.SourceCloneURL,
			SourceRef:        ready.SourceRef,
			SourceCommit:     ready.SourceCommit,
			SourceTree:       ready.SourceTree,
			LineVersion:      ready.LineVersion,
			MutationEpoch:    ready.LineVersion + 1,
			TipCommit:        ready.TipCommit,
			Tree:             ready.Tree,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, ready.LineVersion+1, resumed.MutationEpoch)
	_, changed, err = store.RecordPRDevelopmentAttemptReviewFence(
		ctx,
		PRDevelopmentAttemptReviewFenceRecord{
			ControllerID:     resumed.ID,
			AttemptID:        nextAttempt.ID,
			ExpectedRevision: resumed.Revision,
			LeaseToken:       resumed.LeaseToken,
			LeaseEpoch:       resumed.LeaseEpoch,
			LineVersion:      ready.LineVersion + 1,
			MutationEpoch:    ready.LineVersion + 1,
			ParkIntentID:     "park-queued-attempt",
			BaseCommit:       ready.TipCommit,
			TipCommit:        strings.Repeat("f", 40),
			Tree:             strings.Repeat("a", 40),
			LineReviewDigest: strings.Repeat("b", 64),
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed, "an unfinished attempt cannot publish review evidence")

	*clock = clock.Add(2 * time.Minute)
	_, _, err = store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        nextAttempt.ID,
			ExpectedRevision: resumed.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "replacement-worker",
			Lease:            time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	recovery, err := store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentControllerRecoveryRequired, recovery.Phase)
	assert.Empty(t, recovery.MutationReservationKey)
	assert.Equal(
		t,
		nextMutation.Controller.MutationReservationKey,
		rawPRDevelopmentControllerReservationForTest(t, store, recovery.ID),
		"ambiguous filesystem authority must remain durable but absent from read-only views",
	)
}

func TestStorePRDevelopmentControllerHonorsRepairClockHighWater(t *testing.T) {
	t.Parallel()

	t.Run("new attempt admission", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
		developmentCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(capture),
		)
		require.NoError(t, err)
		require.True(t, created)
		completed := completePRDevelopmentRepairForControllerTest(
			t,
			store,
			developmentCase.ID,
		)
		_, ready := finishPRDevelopmentControllerReviewForTest(
			t,
			store,
			developmentCase.ID,
			completed,
		)
		*clock = clock.Add(10 * time.Minute)
		next, admitted, err := store.AdmitPRDevelopmentRepair(
			ctx,
			PRDevelopmentRepairAdmit{
				CaseID:                      developmentCase.ID,
				ExpectedConversationVersion: 0,
				ExpectedRepairVersion:       completed.Version,
				IdempotencyKey:              "clock-high-water-attempt",
				AgentID:                     "main",
				Instruction:                 "Keep the attempt clock-causal.",
			},
		)
		require.NoError(t, err)
		require.True(t, admitted)
		nextAttempt := next.RepairSession.Attempts[len(next.RepairSession.Attempts)-1]
		*clock = clock.Add(-5 * time.Minute)
		_, acquired, err := store.AcquirePRDevelopmentControllerLease(
			ctx,
			PRDevelopmentControllerAcquire{
				CaseID:           developmentCase.ID,
				AttemptID:        nextAttempt.ID,
				ExpectedRevision: ready.Revision,
				Kind:             PRDevelopmentControllerMutationLease,
				WorkerLabel:      "clock-regressed-worker",
				Lease:            time.Minute,
			},
		)
		assert.ErrorIs(t, err, ErrInvalidPRDevelopmentController)
		assert.False(t, acquired)
		loaded, err := store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
		require.NoError(t, err)
		assert.Equal(t, ready.Revision, loaded.Revision)
		assert.Equal(t, PRDevelopmentControllerReady, loaded.Phase)
	})

	t.Run("completed attempt fence", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
		developmentCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(capture),
		)
		require.NoError(t, err)
		require.True(t, created)
		completed := completePRDevelopmentRepairForControllerTest(
			t,
			store,
			developmentCase.ID,
		)
		fixture := bindPRDevelopmentControllerForTest(
			t,
			store,
			developmentCase.ID,
			completed,
		)
		future := clock.Add(10 * time.Minute)
		attempt := completed.Attempts[len(completed.Attempts)-1]
		_, err = store.db.Exec(`
			UPDATE pr_development_repair_attempts
			SET updated_at = ?
			WHERE id = ?`,
			toDBTime(future),
			attempt.ID,
		)
		require.NoError(t, err)
		_, err = store.db.Exec(`
			UPDATE pr_development_repair_sessions
			SET updated_at = ?
			WHERE id = ?`,
			toDBTime(future),
			completed.ID,
		)
		require.NoError(t, err)
		*clock = clock.Add(5 * time.Minute)
		_, changed, err := store.RecordPRDevelopmentAttemptReviewFence(
			ctx,
			PRDevelopmentAttemptReviewFenceRecord{
				ControllerID:     fixture.Bound.ID,
				AttemptID:        attempt.ID,
				ExpectedRevision: fixture.Bound.Revision,
				LeaseToken:       fixture.Bound.LeaseToken,
				LeaseEpoch:       fixture.Bound.LeaseEpoch,
				LineVersion:      1,
				MutationEpoch:    1,
				ParkIntentID:     "clock-regressed-park",
				BaseCommit:       completed.HeadSHA,
				TipCommit:        strings.Repeat("c", 40),
				Tree:             strings.Repeat("d", 40),
				LineReviewDigest: strings.Repeat("e", 64),
			},
		)
		assert.ErrorIs(t, err, ErrInvalidPRDevelopmentController)
		assert.False(t, changed)
		var fences int
		require.NoError(t, store.db.QueryRow(`
			SELECT COUNT(*)
			FROM pr_development_attempt_review_fences`).Scan(&fences))
		assert.Zero(t, fences)
	})
}

func TestPRDevelopmentControllerTypesAreJSONPrivate(t *testing.T) {
	t.Parallel()

	sentinel := "controller-secret-sentinel"
	values := []any{
		PRDevelopmentController{LeaseToken: sentinel, MutationReservationKey: sentinel},
		PRDevelopmentAttemptReviewFence{MutationLeaseTokenDigest: sentinel},
		PRDevelopmentControllerAcquire{WorkerLabel: sentinel},
		PRDevelopmentControllerRenew{LeaseToken: sentinel},
		PRDevelopmentControllerLineBind{LeaseToken: sentinel},
		PRDevelopmentAttemptReviewFenceRecord{LeaseToken: sentinel},
		PRDevelopmentControllerReviewTransition{LeaseToken: sentinel},
		PRDevelopmentControllerLease{
			Controller: PRDevelopmentController{LeaseToken: sentinel},
			ReviewFence: &PRDevelopmentAttemptReviewFence{
				MutationLeaseTokenDigest: sentinel,
			},
		},
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), sentinel)
		assert.Equal(t, `{}`, string(encoded))
	}
}

func TestStorePRDevelopmentControllerRejectsLegacyAndSiblingOwnership(t *testing.T) {
	t.Parallel()

	t.Run("unpinned queued baseline", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
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
			"unpinned-controller",
			0,
		)
		require.NotNil(t, workbench.RepairSession)
		attempt := workbench.RepairSession.Attempts[0]
		_, acquired, err := store.AcquirePRDevelopmentControllerLease(
			ctx,
			PRDevelopmentControllerAcquire{
				CaseID:           developmentCase.ID,
				AttemptID:        attempt.ID,
				ExpectedRevision: 0,
				Kind:             PRDevelopmentControllerMutationLease,
				WorkerLabel:      "controller",
				Lease:            time.Minute,
			},
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
		assert.False(t, acquired)
	})

	t.Run("active legacy lease", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
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
			"legacy-active",
			0,
		)
		claimed, found, err := store.ClaimPRDevelopmentRepair(
			ctx,
			PRDevelopmentRepairClaimRequest{WorkerLabel: "legacy", Lease: time.Minute},
		)
		require.NoError(t, err)
		require.True(t, found)
		attempt := activePRDevelopmentRepairAttempt(&claimed)
		require.NotNil(t, attempt)
		_, acquired, err := store.AcquirePRDevelopmentControllerLease(
			ctx,
			PRDevelopmentControllerAcquire{
				CaseID:           developmentCase.ID,
				AttemptID:        attempt.ID,
				ExpectedRevision: 0,
				Kind:             PRDevelopmentControllerMutationLease,
				WorkerLabel:      "controller",
				Lease:            time.Minute,
			},
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentControllerActive)
		assert.False(t, acquired)
		_, err = store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
		assert.ErrorIs(t, err, ErrNotFound)
		require.NotNil(t, workbench.RepairSession)
	})

	t.Run("sibling admission after controller", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
		ownerCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(capture),
		)
		require.NoError(t, err)
		require.True(t, created)
		siblingCase := captureAdditionalPRDevelopmentThreadCase(
			t,
			store,
			clock,
			capture,
			"controller-sibling-review",
			"601",
		)
		completed := completePRDevelopmentRepairForControllerTest(t, store, ownerCase.ID)
		attempt := completed.Attempts[len(completed.Attempts)-1]
		_, acquired, err := store.AcquirePRDevelopmentControllerLease(
			ctx,
			PRDevelopmentControllerAcquire{
				CaseID:           ownerCase.ID,
				AttemptID:        attempt.ID,
				ExpectedRevision: 0,
				Kind:             PRDevelopmentControllerMutationLease,
				WorkerLabel:      "controller",
				Lease:            time.Minute,
			},
		)
		require.NoError(t, err)
		require.True(t, acquired)
		_, admitted, err := store.AdmitPRDevelopmentRepair(
			ctx,
			PRDevelopmentRepairAdmit{
				CaseID:                      siblingCase.ID,
				ExpectedConversationVersion: 0,
				ExpectedRepairVersion:       0,
				IdempotencyKey:              "sibling-attempt",
				AgentID:                     "main",
				Instruction:                 "Do not create a second thread owner.",
			},
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
		assert.False(t, admitted)
		_, err = store.GetPRDevelopmentControllerForCase(ctx, siblingCase.ID)
		require.NoError(t, err, "all provider-thread cases resolve the same private controller")
	})

	t.Run("sibling session before controller", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
		ownerCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(capture),
		)
		require.NoError(t, err)
		require.True(t, created)
		siblingCase := captureAdditionalPRDevelopmentThreadCase(
			t,
			store,
			clock,
			capture,
			"controller-existing-sibling",
			"602",
		)
		completed := completePRDevelopmentRepairForControllerTest(t, store, ownerCase.ID)
		_ = admitPRDevelopmentRepairForTest(t, store, siblingCase.ID, "sibling-first", 0)
		attempt := completed.Attempts[len(completed.Attempts)-1]
		_, acquired, err := store.AcquirePRDevelopmentControllerLease(
			ctx,
			PRDevelopmentControllerAcquire{
				CaseID:           ownerCase.ID,
				AttemptID:        attempt.ID,
				ExpectedRevision: 0,
				Kind:             PRDevelopmentControllerMutationLease,
				WorkerLabel:      "controller",
				Lease:            time.Minute,
			},
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
		assert.False(t, acquired)
	})
}

func TestStorePRDevelopmentControllerConcurrentCreationHasOneOwner(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "controller-concurrent.db")
	first, clock, capture := newPRDevelopmentStoreFixture(t, path)
	developmentCase, created, err := first.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(t, first, developmentCase.ID)
	attempt := completed.Attempts[len(completed.Attempts)-1]
	second, err := Open(ctx, path, WithClock(func() time.Time { return *clock }))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })

	type result struct {
		lease    PRDevelopmentControllerLease
		acquired bool
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for index, store := range []*Store{first, second} {
		wait.Add(1)
		go func(index int, store *Store) {
			defer wait.Done()
			<-start
			lease, acquired, acquireErr := store.AcquirePRDevelopmentControllerLease(
				ctx,
				PRDevelopmentControllerAcquire{
					CaseID:           developmentCase.ID,
					AttemptID:        attempt.ID,
					ExpectedRevision: 0,
					Kind:             PRDevelopmentControllerMutationLease,
					WorkerLabel:      "worker-" + string(rune('a'+index)),
					Lease:            time.Minute,
				},
			)
			results <- result{lease: lease, acquired: acquired, err: acquireErr}
		}(index, store)
	}
	close(start)
	wait.Wait()
	close(results)

	successes, conflicts := 0, 0
	for item := range results {
		switch {
		case item.err == nil:
			successes++
			assert.True(t, item.acquired)
			assert.True(t, item.lease.Created)
		case errors.Is(item.err, ErrPRDevelopmentControllerConflict):
			conflicts++
			assert.False(t, item.acquired)
		default:
			t.Fatalf("AcquirePRDevelopmentControllerLease() error = %v", item.err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, conflicts)
}

func TestStorePRDevelopmentControllerCorruptionFailsClosed(t *testing.T) {
	t.Parallel()

	t.Run("current attempt from another session", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
		ownerCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(capture),
		)
		require.NoError(t, err)
		require.True(t, created)
		completed := completePRDevelopmentRepairForControllerTest(t, store, ownerCase.ID)
		ownerAttempt := completed.Attempts[len(completed.Attempts)-1]
		controller, acquired, err := store.AcquirePRDevelopmentControllerLease(
			ctx,
			PRDevelopmentControllerAcquire{
				CaseID:           ownerCase.ID,
				AttemptID:        ownerAttempt.ID,
				ExpectedRevision: 0,
				Kind:             PRDevelopmentControllerMutationLease,
				WorkerLabel:      "controller",
				Lease:            time.Minute,
			},
		)
		require.NoError(t, err)
		require.True(t, acquired)
		foreignCase := capturePRDevelopmentListCase(
			t,
			store,
			capture,
			"controller-foreign-attempt",
			"other/project",
			77,
		)
		foreign := admitPRDevelopmentRepairForTest(
			t,
			store,
			foreignCase.ID,
			"foreign-attempt",
			0,
		)
		require.NotNil(t, foreign.RepairSession)
		foreignAttempt := foreign.RepairSession.Attempts[0]
		_, err = store.db.Exec(`
			UPDATE pr_development_thread_controllers
			SET current_attempt_id = ?
			WHERE id = ?`,
			foreignAttempt.ID,
			controller.Controller.ID,
		)
		require.NoError(t, err)
		_, err = store.GetPRDevelopmentControllerForCase(ctx, ownerCase.ID)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "belongs to another session")
	})

	t.Run("fence attempt from another session", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
		ownerCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(capture),
		)
		require.NoError(t, err)
		require.True(t, created)
		completed := completePRDevelopmentRepairForControllerTest(t, store, ownerCase.ID)
		fixture := parkPRDevelopmentControllerForTest(t, store, ownerCase.ID, completed)
		foreignCase := capturePRDevelopmentListCase(
			t,
			store,
			capture,
			"controller-foreign-fence",
			"another/project",
			78,
		)
		foreign := admitPRDevelopmentRepairForTest(
			t,
			store,
			foreignCase.ID,
			"foreign-fence-attempt",
			0,
		)
		require.NotNil(t, foreign.RepairSession)
		corruptFence := fixture.Fence
		corruptFence.AttemptID = foreign.RepairSession.Attempts[0].ID
		corruptFence.FenceHash = hashPRDevelopmentReviewFence(corruptFence)
		_, err = store.db.Exec(`
			UPDATE pr_development_attempt_review_fences
			SET attempt_id = ?, fence_hash = ?
			WHERE attempt_id = ?`,
			corruptFence.AttemptID,
			corruptFence.FenceHash,
			fixture.Fence.AttemptID,
		)
		require.NoError(t, err)
		_, err = store.db.Exec(`
			UPDATE pr_development_thread_controllers
			SET fences_digest = ?
			WHERE id = ?`,
			corruptFence.FenceHash,
			corruptFence.ControllerID,
		)
		require.NoError(t, err)
		_, err = store.GetPRDevelopmentControllerForCase(ctx, ownerCase.ID)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "fence attempt ownership/order")
	})

	t.Run("bound idle state", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
		ownerCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(capture),
		)
		require.NoError(t, err)
		require.True(t, created)
		completed := completePRDevelopmentRepairForControllerTest(t, store, ownerCase.ID)
		fixture := bindPRDevelopmentControllerForTest(t, store, ownerCase.ID, completed)
		conn, err := store.db.Conn(ctx)
		require.NoError(t, err)
		_, err = conn.ExecContext(ctx, `PRAGMA ignore_check_constraints = ON`)
		require.NoError(t, err)
		_, err = conn.ExecContext(ctx, `
			UPDATE pr_development_thread_controllers
			SET phase = 'idle', current_attempt_id = NULL, lease_kind = '',
				lease_owner = '', lease_token = '', lease_until = NULL,
				lease_epoch = 0, claims = 0, mutation_reservation_key = ''
			WHERE id = ?`,
			fixture.Bound.ID,
		)
		require.NoError(t, err)
		_, err = conn.ExecContext(ctx, `PRAGMA ignore_check_constraints = OFF`)
		require.NoError(t, err)
		require.NoError(t, conn.Close())
		_, err = store.GetPRDevelopmentControllerForCase(ctx, ownerCase.ID)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "idle controller")
	})

	t.Run("controller revision behind fence proof", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
		ownerCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(capture),
		)
		require.NoError(t, err)
		require.True(t, created)
		completed := completePRDevelopmentRepairForControllerTest(t, store, ownerCase.ID)
		fixture := parkPRDevelopmentControllerForTest(t, store, ownerCase.ID, completed)
		_, err = store.db.Exec(`
			UPDATE pr_development_thread_controllers
			SET revision = ?
			WHERE id = ?`,
			fixture.Fence.MutationControllerRevision,
			fixture.Fence.ControllerID,
		)
		require.NoError(t, err)
		_, err = store.GetPRDevelopmentControllerForCase(ctx, ownerCase.ID)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "fence high-water")
	})

	t.Run("initial reservation differs from owner workspace reservation", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
		ownerCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(capture),
		)
		require.NoError(t, err)
		require.True(t, created)
		completed := completePRDevelopmentRepairForControllerTest(t, store, ownerCase.ID)
		attempt := completed.Attempts[len(completed.Attempts)-1]
		controller, acquired, err := store.AcquirePRDevelopmentControllerLease(
			ctx,
			PRDevelopmentControllerAcquire{
				CaseID:           ownerCase.ID,
				AttemptID:        attempt.ID,
				ExpectedRevision: 0,
				Kind:             PRDevelopmentControllerMutationLease,
				WorkerLabel:      "controller",
				Lease:            time.Minute,
			},
		)
		require.NoError(t, err)
		require.True(t, acquired)
		_, err = store.db.Exec(`
			UPDATE pr_development_thread_controllers
			SET mutation_reservation_key = ?
			WHERE id = ?`,
			prDevelopmentRepairReservationPrefix+strings.Repeat("a", 32),
			controller.Controller.ID,
		)
		require.NoError(t, err)
		_, err = store.GetPRDevelopmentControllerForCase(ctx, ownerCase.ID)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "initial controller reservation")
	})

	t.Run("ready revision skips its completion proof", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
		ownerCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(capture),
		)
		require.NoError(t, err)
		require.True(t, created)
		completed := completePRDevelopmentRepairForControllerTest(t, store, ownerCase.ID)
		fixture := parkPRDevelopmentControllerForTest(t, store, ownerCase.ID, completed)
		attempt := completed.Attempts[len(completed.Attempts)-1]
		parked, err := store.GetPRDevelopmentControllerForCase(ctx, ownerCase.ID)
		require.NoError(t, err)
		review, acquired, err := store.AcquirePRDevelopmentControllerLease(
			ctx,
			PRDevelopmentControllerAcquire{
				CaseID:           ownerCase.ID,
				AttemptID:        attempt.ID,
				ExpectedRevision: parked.Revision,
				Kind:             PRDevelopmentControllerReviewLease,
				WorkerLabel:      "reviewer",
				Lease:            time.Minute,
			},
		)
		require.NoError(t, err)
		require.True(t, acquired)
		ready, changed, err := store.FinishPRDevelopmentControllerReview(
			ctx,
			PRDevelopmentControllerReviewTransition{
				ControllerID:     review.Controller.ID,
				AttemptID:        attempt.ID,
				ExpectedRevision: review.Controller.Revision,
				LeaseToken:       review.Controller.LeaseToken,
				LeaseEpoch:       review.Controller.LeaseEpoch,
			},
		)
		require.NoError(t, err)
		require.True(t, changed)
		_, err = store.db.Exec(`
			UPDATE pr_development_thread_controllers
			SET revision = revision + 1
			WHERE id = ?`,
			fixture.Bound.ID,
		)
		require.NoError(t, err)
		_, err = store.GetPRDevelopmentControllerForCase(ctx, ownerCase.ID)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "ready controller high-water")
		assert.NotZero(t, ready.Revision)
	})

	t.Run("initial mutation revision is unreachable", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
		ownerCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(capture),
		)
		require.NoError(t, err)
		require.True(t, created)
		completed := completePRDevelopmentRepairForControllerTest(t, store, ownerCase.ID)
		attempt := completed.Attempts[len(completed.Attempts)-1]
		controller, acquired, err := store.AcquirePRDevelopmentControllerLease(
			ctx,
			PRDevelopmentControllerAcquire{
				CaseID:           ownerCase.ID,
				AttemptID:        attempt.ID,
				ExpectedRevision: 0,
				Kind:             PRDevelopmentControllerMutationLease,
				WorkerLabel:      "controller",
				Lease:            time.Minute,
			},
		)
		require.NoError(t, err)
		require.True(t, acquired)
		_, err = store.db.Exec(`
			UPDATE pr_development_thread_controllers
			SET revision = 500
			WHERE id = ?`,
			controller.Controller.ID,
		)
		require.NoError(t, err)
		_, err = store.GetPRDevelopmentControllerForCase(ctx, ownerCase.ID)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "initial mutation controller high-water")
	})

	t.Run("first fence retires a different reservation", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
		ownerCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(capture),
		)
		require.NoError(t, err)
		require.True(t, created)
		completed := completePRDevelopmentRepairForControllerTest(t, store, ownerCase.ID)
		fixture := parkPRDevelopmentControllerForTest(t, store, ownerCase.ID, completed)
		corruptFence := fixture.Fence
		corruptFence.MutationReservationDigest = strings.Repeat("a", 64)
		corruptFence.FenceHash = hashPRDevelopmentReviewFence(corruptFence)
		_, err = store.db.Exec(`
			UPDATE pr_development_attempt_review_fences
			SET mutation_reservation_digest = ?, fence_hash = ?
			WHERE attempt_id = ?`,
			corruptFence.MutationReservationDigest,
			corruptFence.FenceHash,
			corruptFence.AttemptID,
		)
		require.NoError(t, err)
		_, err = store.db.Exec(`
			UPDATE pr_development_thread_controllers
			SET fences_digest = ?
			WHERE id = ?`,
			corruptFence.FenceHash,
			corruptFence.ControllerID,
		)
		require.NoError(t, err)
		_, err = store.GetPRDevelopmentControllerForCase(ctx, ownerCase.ID)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "did not retire the owner reservation")
	})

	t.Run("review completion proof is outside its final hash", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
		ownerCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(capture),
		)
		require.NoError(t, err)
		require.True(t, created)
		completed := completePRDevelopmentRepairForControllerTest(t, store, ownerCase.ID)
		_, ready := finishPRDevelopmentControllerReviewForTest(
			t,
			store,
			ownerCase.ID,
			completed,
		)
		_, err = store.db.Exec(`
			UPDATE pr_development_attempt_review_fences
			SET review_lease_token_digest = ?
			WHERE controller_id = ?`,
			strings.Repeat("a", 64),
			ready.ID,
		)
		require.NoError(t, err)
		_, err = store.GetPRDevelopmentControllerForCase(ctx, ownerCase.ID)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "stored review fence is invalid")
	})

	t.Run("owner reservation belongs to two repair sessions", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
		ownerCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(capture),
		)
		require.NoError(t, err)
		require.True(t, created)
		completed := completePRDevelopmentRepairForControllerTest(t, store, ownerCase.ID)
		foreignCase := capturePRDevelopmentListCase(
			t,
			store,
			capture,
			"controller-duplicate-reservation",
			"duplicate/reservation",
			79,
		)
		foreign := admitPRDevelopmentRepairForTest(
			t,
			store,
			foreignCase.ID,
			"duplicate-reservation-attempt",
			0,
		)
		require.NotNil(t, foreign.RepairSession)
		_, err = store.db.Exec(`
			UPDATE pr_development_repair_sessions
			SET reservation_key = ?
			WHERE id = ?`,
			completed.ReservationKey,
			foreign.RepairSession.ID,
		)
		require.NoError(t, err)
		attempt := completed.Attempts[len(completed.Attempts)-1]
		_, acquired, err := store.AcquirePRDevelopmentControllerLease(
			ctx,
			PRDevelopmentControllerAcquire{
				CaseID:           ownerCase.ID,
				AttemptID:        attempt.ID,
				ExpectedRevision: 0,
				Kind:             PRDevelopmentControllerMutationLease,
				WorkerLabel:      "controller",
				Lease:            time.Minute,
			},
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
		assert.False(t, acquired)
		assert.Contains(t, err.Error(), "one repair-session owner")
	})
}

func TestPRDevelopmentControllerFenceNormalizationSupportsSHA256AndNoChanges(t *testing.T) {
	t.Parallel()
	valid := PRDevelopmentAttemptReviewFenceRecord{
		ControllerID:     "pctl_00000000000000000000000000000001",
		AttemptID:        "pdr_00000000000000000000000000000001",
		ExpectedRevision: 1,
		LeaseToken:       "lease_valid",
		LeaseEpoch:       1,
		LineVersion:      1,
		MutationEpoch:    1,
		ParkIntentID:     "park-no-change",
		BaseCommit:       strings.Repeat("a", 64),
		TipCommit:        strings.Repeat("a", 64),
		Tree:             strings.Repeat("b", 64),
		NoChanges:        true,
		LineReviewDigest: strings.Repeat("c", 64),
	}
	_, err := normalizePRDevelopmentAttemptReviewFenceRecord(valid)
	require.NoError(t, err)
	changed := valid
	changed.NoChanges = false
	_, err = normalizePRDevelopmentAttemptReviewFenceRecord(changed)
	assert.ErrorIs(t, err, ErrInvalidPRDevelopmentController)
	mixedWidth := valid
	mixedWidth.Tree = strings.Repeat("b", 40)
	_, err = normalizePRDevelopmentAttemptReviewFenceRecord(mixedWidth)
	assert.ErrorIs(t, err, ErrInvalidPRDevelopmentController)
}

func TestStorePRDevelopmentControllerExpiredMutationOperationsRequireRecovery(t *testing.T) {
	t.Parallel()

	t.Run("bind", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
		developmentCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(capture),
		)
		require.NoError(t, err)
		require.True(t, created)
		completed := completePRDevelopmentRepairForControllerTest(
			t,
			store,
			developmentCase.ID,
		)
		attempt := completed.Attempts[len(completed.Attempts)-1]
		mutation, acquired, err := store.AcquirePRDevelopmentControllerLease(
			ctx,
			PRDevelopmentControllerAcquire{
				CaseID:           developmentCase.ID,
				AttemptID:        attempt.ID,
				ExpectedRevision: 0,
				Kind:             PRDevelopmentControllerMutationLease,
				WorkerLabel:      "expiring-bind",
				Lease:            time.Minute,
			},
		)
		require.NoError(t, err)
		require.True(t, acquired)
		*clock = clock.Add(2 * time.Minute)
		_, changed, err := store.BindPRDevelopmentControllerLine(
			ctx,
			PRDevelopmentControllerLineBind{
				ControllerID:     mutation.Controller.ID,
				AttemptID:        attempt.ID,
				ExpectedRevision: mutation.Controller.Revision,
				LeaseToken:       mutation.Controller.LeaseToken,
				LeaseEpoch:       mutation.Controller.LeaseEpoch,
				WorkspaceID:      completed.WorkspaceID,
				SourceCloneURL:   completed.CloneURL,
				SourceRef:        completed.HeadRef,
				SourceCommit:     completed.HeadSHA,
				SourceTree:       strings.Repeat("b", 40),
				LineVersion:      0,
				MutationEpoch:    1,
				TipCommit:        completed.HeadSHA,
				Tree:             strings.Repeat("b", 40),
			},
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
		assert.False(t, changed)
		recovery, err := store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
		require.NoError(t, err)
		assert.Equal(t, PRDevelopmentControllerRecoveryRequired, recovery.Phase)
		assert.Empty(t, recovery.MutationReservationKey)
		assert.Equal(
			t,
			mutation.Controller.MutationReservationKey,
			rawPRDevelopmentControllerReservationForTest(t, store, recovery.ID),
		)
	})

	t.Run("record fence", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
		developmentCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(capture),
		)
		require.NoError(t, err)
		require.True(t, created)
		completed := completePRDevelopmentRepairForControllerTest(
			t,
			store,
			developmentCase.ID,
		)
		fixture := bindPRDevelopmentControllerForTest(
			t,
			store,
			developmentCase.ID,
			completed,
		)
		attempt := completed.Attempts[len(completed.Attempts)-1]
		*clock = clock.Add(2 * time.Minute)
		_, changed, err := store.RecordPRDevelopmentAttemptReviewFence(
			ctx,
			PRDevelopmentAttemptReviewFenceRecord{
				ControllerID:     fixture.Bound.ID,
				AttemptID:        attempt.ID,
				ExpectedRevision: fixture.Bound.Revision,
				LeaseToken:       fixture.Bound.LeaseToken,
				LeaseEpoch:       fixture.Bound.LeaseEpoch,
				LineVersion:      1,
				MutationEpoch:    1,
				ParkIntentID:     "expired-park",
				BaseCommit:       completed.HeadSHA,
				TipCommit:        strings.Repeat("c", 40),
				Tree:             strings.Repeat("d", 40),
				LineReviewDigest: strings.Repeat("e", 64),
			},
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
		assert.False(t, changed)
		var fences int
		require.NoError(t, store.db.QueryRow(`
			SELECT COUNT(*) FROM pr_development_attempt_review_fences`).Scan(&fences))
		assert.Zero(t, fences)
		recovery, err := store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
		require.NoError(t, err)
		assert.Equal(t, PRDevelopmentControllerRecoveryRequired, recovery.Phase)
	})
}

func TestStorePRDevelopmentControllerNoChangeFencePreservesTree(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(
		t,
		store,
		developmentCase.ID,
	)
	fixture := bindPRDevelopmentControllerForTest(
		t,
		store,
		developmentCase.ID,
		completed,
	)
	attempt := completed.Attempts[len(completed.Attempts)-1]
	record := PRDevelopmentAttemptReviewFenceRecord{
		ControllerID:     fixture.Bound.ID,
		AttemptID:        attempt.ID,
		ExpectedRevision: fixture.Bound.Revision,
		LeaseToken:       fixture.Bound.LeaseToken,
		LeaseEpoch:       fixture.Bound.LeaseEpoch,
		LineVersion:      1,
		MutationEpoch:    1,
		ParkIntentID:     "park-no-change-tree",
		BaseCommit:       completed.HeadSHA,
		TipCommit:        completed.HeadSHA,
		Tree:             strings.Repeat("c", 40),
		NoChanges:        true,
		LineReviewDigest: strings.Repeat("d", 64),
	}
	_, changed, err := store.RecordPRDevelopmentAttemptReviewFence(ctx, record)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed)
	record.Tree = fixture.Bound.Tree
	fence, changed, err := store.RecordPRDevelopmentAttemptReviewFence(ctx, record)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.True(t, fence.NoChanges)
	assert.Equal(t, fence.BaseCommit, fence.TipCommit)
	assert.Equal(t, fixture.Bound.Tree, fence.Tree)
}

func TestStorePRDevelopmentControllerRevisionHeadroomKeepsReviewFinishable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(
		t,
		store,
		developmentCase.ID,
	)
	fixture := parkPRDevelopmentControllerForTest(
		t,
		store,
		developmentCase.ID,
		completed,
	)
	attempt := completed.Attempts[len(completed.Attempts)-1]
	parked, err := store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	review, acquired, err := store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: parked.Revision,
			Kind:             PRDevelopmentControllerReviewLease,
			WorkerLabel:      "headroom-reviewer",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	reachableLeaseEpoch := fixture.Fence.MutationLeaseEpoch +
		(MaxPRDevelopmentControllerRevision - 1 - fixture.Fence.MutationControllerRevision)
	_, err = store.db.Exec(`
		UPDATE pr_development_thread_controllers
		SET revision = ?, lease_epoch = ?, claims = ?
		WHERE id = ?`,
		MaxPRDevelopmentControllerRevision-1,
		reachableLeaseEpoch,
		reachableLeaseEpoch,
		fixture.Bound.ID,
	)
	require.NoError(t, err)
	transition := PRDevelopmentControllerReviewTransition{
		ControllerID:     fixture.Bound.ID,
		AttemptID:        attempt.ID,
		ExpectedRevision: MaxPRDevelopmentControllerRevision - 1,
		LeaseToken:       review.Controller.LeaseToken,
		LeaseEpoch:       reachableLeaseEpoch,
	}
	_, err = store.ReleasePRDevelopmentControllerReview(ctx, transition)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	ready, changed, err := store.FinishPRDevelopmentControllerReview(ctx, transition)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, int64(MaxPRDevelopmentControllerRevision), ready.Revision)
	assert.Equal(t, PRDevelopmentControllerReady, ready.Phase)

	_, admitted, err := store.AdmitPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairAdmit{
			CaseID:                      developmentCase.ID,
			ExpectedConversationVersion: 0,
			ExpectedRepairVersion:       completed.Version,
			IdempotencyKey:              "no-revision-headroom",
			AgentID:                     "main",
			Instruction:                 "This must not create an unfinishable queued attempt.",
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentRepairCapacity)
	assert.False(t, admitted)
}

func TestStorePRDevelopmentControllerRejectsClockRegressionWithoutCorruption(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(
		t,
		store,
		developmentCase.ID,
	)
	_ = parkPRDevelopmentControllerForTest(t, store, developmentCase.ID, completed)
	attempt := completed.Attempts[len(completed.Attempts)-1]
	parked, err := store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	review, acquired, err := store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: parked.Revision,
			Kind:             PRDevelopmentControllerReviewLease,
			WorkerLabel:      "clock-reviewer",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	updatedAt := review.Controller.UpdatedAt
	*clock = clock.Add(-time.Minute)
	err = store.RenewPRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerRenew{
			ControllerID: review.Controller.ID,
			AttemptID:    attempt.ID,
			LeaseToken:   review.Controller.LeaseToken,
			LeaseEpoch:   review.Controller.LeaseEpoch,
			Lease:        time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrInvalidPRDevelopmentController)
	loaded, err := store.GetPRDevelopmentControllerForCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	assert.Equal(t, updatedAt, loaded.UpdatedAt)
	assert.Equal(t, PRDevelopmentControllerReview, loaded.Phase)
}

func rawPRDevelopmentControllerReservationForTest(
	t *testing.T,
	store *Store,
	controllerID string,
) string {
	t.Helper()
	var reservation string
	require.NoError(t, store.db.QueryRow(`
		SELECT mutation_reservation_key
		FROM pr_development_thread_controllers
		WHERE id = ?`, controllerID).Scan(&reservation))
	return reservation
}

func completePRDevelopmentRepairForControllerTest(
	t *testing.T,
	store *Store,
	caseID string,
) PRDevelopmentRepairSession {
	t.Helper()
	ctx := context.Background()
	workbench := admitPRDevelopmentRepairForTest(t, store, caseID, "controller-attempt-1", 0)
	require.NotNil(t, workbench.RepairSession)
	claimed, found, err := store.ClaimPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "legacy-worker", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, found)
	attempt := activePRDevelopmentRepairAttempt(&claimed)
	require.NotNil(t, attempt)
	pinned, err := store.PinPRDevelopmentRepairSession(
		ctx,
		validPRDevelopmentRepairPinForTest(attempt),
	)
	require.NoError(t, err)
	attempt = activePRDevelopmentRepairAttempt(&pinned)
	require.NotNil(t, attempt)
	running, err := store.BeginPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairBegin{
			AttemptID:  attempt.ID,
			LeaseToken: attempt.LeaseToken,
			Lease:      time.Minute,
		},
	)
	require.NoError(t, err)
	attempt = activePRDevelopmentRepairAttempt(&running)
	require.NotNil(t, attempt)
	completed, err := store.FinishPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairOutcome{
			AttemptID:   attempt.ID,
			LeaseToken:  attempt.LeaseToken,
			Status:      PRDevelopmentRepairCompleted,
			Summary:     "Committed the local repair after focused CI passed.",
			Iterations:  1,
			WorkspaceID: "gw-controller-line",
		},
	)
	require.NoError(t, err)
	return completed
}

type prDevelopmentControllerTestFixture struct {
	Mutation PRDevelopmentControllerLease
	Bound    PRDevelopmentController
	Fence    PRDevelopmentAttemptReviewFence
}

func bindPRDevelopmentControllerForTest(
	t *testing.T,
	store *Store,
	caseID string,
	completed PRDevelopmentRepairSession,
) prDevelopmentControllerTestFixture {
	t.Helper()
	ctx := context.Background()
	attempt := completed.Attempts[len(completed.Attempts)-1]
	mutation, acquired, err := store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           caseID,
			AttemptID:        attempt.ID,
			ExpectedRevision: 0,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "controller-fixture",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	bound, changed, err := store.BindPRDevelopmentControllerLine(
		ctx,
		PRDevelopmentControllerLineBind{
			ControllerID:     mutation.Controller.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: mutation.Controller.Revision,
			LeaseToken:       mutation.Controller.LeaseToken,
			LeaseEpoch:       mutation.Controller.LeaseEpoch,
			WorkspaceID:      completed.WorkspaceID,
			SourceCloneURL:   completed.CloneURL,
			SourceRef:        completed.HeadRef,
			SourceCommit:     completed.HeadSHA,
			SourceTree:       strings.Repeat("b", 40),
			LineVersion:      0,
			MutationEpoch:    1,
			TipCommit:        completed.HeadSHA,
			Tree:             strings.Repeat("b", 40),
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	return prDevelopmentControllerTestFixture{Mutation: mutation, Bound: bound}
}

func parkPRDevelopmentControllerForTest(
	t *testing.T,
	store *Store,
	caseID string,
	completed PRDevelopmentRepairSession,
) prDevelopmentControllerTestFixture {
	t.Helper()
	fixture := bindPRDevelopmentControllerForTest(t, store, caseID, completed)
	attempt := completed.Attempts[len(completed.Attempts)-1]
	fence, changed, err := store.RecordPRDevelopmentAttemptReviewFence(
		context.Background(),
		PRDevelopmentAttemptReviewFenceRecord{
			ControllerID:     fixture.Bound.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: fixture.Bound.Revision,
			LeaseToken:       fixture.Bound.LeaseToken,
			LeaseEpoch:       fixture.Bound.LeaseEpoch,
			LineVersion:      1,
			MutationEpoch:    1,
			ParkIntentID:     "park-controller-fixture",
			BaseCommit:       completed.HeadSHA,
			TipCommit:        strings.Repeat("c", 40),
			Tree:             strings.Repeat("d", 40),
			LineReviewDigest: strings.Repeat("e", 64),
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	fixture.Fence = fence
	return fixture
}

func finishPRDevelopmentControllerReviewForTest(
	t *testing.T,
	store *Store,
	caseID string,
	completed PRDevelopmentRepairSession,
) (prDevelopmentControllerTestFixture, PRDevelopmentController) {
	t.Helper()
	ctx := context.Background()
	fixture := parkPRDevelopmentControllerForTest(t, store, caseID, completed)
	attempt := completed.Attempts[len(completed.Attempts)-1]
	parked, err := store.GetPRDevelopmentControllerForCase(ctx, caseID)
	require.NoError(t, err)
	review, acquired, err := store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           caseID,
			AttemptID:        attempt.ID,
			ExpectedRevision: parked.Revision,
			Kind:             PRDevelopmentControllerReviewLease,
			WorkerLabel:      "controller-review-fixture",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	ready, changed, err := store.FinishPRDevelopmentControllerReview(
		ctx,
		PRDevelopmentControllerReviewTransition{
			ControllerID:     review.Controller.ID,
			AttemptID:        attempt.ID,
			ExpectedRevision: review.Controller.Revision,
			LeaseToken:       review.Controller.LeaseToken,
			LeaseEpoch:       review.Controller.LeaseEpoch,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	return fixture, ready
}
