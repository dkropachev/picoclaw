//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type prDevelopmentPublicationRequeueFixture struct {
	Lifecycle prDevelopmentPublicationLifecycleFixture
	Claim     PRDevelopmentPublication
}

func newPRDevelopmentPublicationRequeueFixture(
	t *testing.T,
	origin PRDevelopmentPublicationStatus,
) prDevelopmentPublicationRequeueFixture {
	t.Helper()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	claim := fixture.Claim
	switch origin {
	case PRDevelopmentPublicationPending:
	case PRDevelopmentPublicationGateWaiting:
		pinned := pinPRDevelopmentPublicationForTest(t, &fixture)
		runID := "wr_" + strings.Repeat("9", 32)
		created := 0
		_, existed, err := fixture.Store.AdmitPRDevelopmentPublicationDecisionRun(
			context.Background(),
			PRDevelopmentPublicationDecisionRunAdmission{
				Key:        publicationPRDevelopmentDecisionKey(pinned),
				RunID:      runID,
				ClaimToken: claim.ClaimToken,
				ClaimEpoch: claim.ClaimEpoch,
			},
			func(context.Context) error {
				created++
				return nil
			},
		)
		require.NoError(t, err)
		require.False(t, existed)
		require.Equal(t, 1, created)
		availableAt := fixture.Clock.Add(time.Second)
		_, changed, err := fixture.Store.ReleasePRDevelopmentPublicationGateWait(
			context.Background(),
			PRDevelopmentPublicationGateWait{
				PublicationID: claim.ID,
				ClaimToken:    claim.ClaimToken,
				ClaimEpoch:    claim.ClaimEpoch,
				DecisionRunID: runID,
				AvailableAt:   availableAt,
			},
		)
		require.NoError(t, err)
		require.True(t, changed)
		*fixture.Clock = availableAt
		claim = claimOnePRDevelopmentPublicationForRequeueTest(
			t,
			&fixture,
			"publication-gate-requeue-worker",
		)
	case PRDevelopmentPublicationPushReady:
		_, claim = claimPushReadyPRDevelopmentPublicationForTest(t, &fixture)
	default:
		t.Fatalf("unsupported requeue fixture origin %q", origin)
	}
	require.Equal(t, origin, claim.ClaimFrom)
	return prDevelopmentPublicationRequeueFixture{Lifecycle: fixture, Claim: claim}
}

func claimOnePRDevelopmentPublicationForRequeueTest(
	t *testing.T,
	fixture *prDevelopmentPublicationLifecycleFixture,
	worker string,
) PRDevelopmentPublication {
	t.Helper()

	claimed, err := fixture.Store.ClaimPRDevelopmentPublications(
		context.Background(),
		PRDevelopmentPublicationClaimRequest{
			WorkerLabel: worker,
			Limit:       1,
			Lease:       5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	return claimed[0]
}

func assertPRDevelopmentPublicationRequeuePreserved(
	t *testing.T,
	before PRDevelopmentPublication,
	after PRDevelopmentPublication,
	origin PRDevelopmentPublicationStatus,
	availableAt time.Time,
	updatedAt time.Time,
) {
	t.Helper()

	expected := before
	expected.Status = origin
	expected.ClaimFrom = ""
	expected.ClaimOwner = ""
	expected.ClaimToken = ""
	expected.ClaimUntil = nil
	expected.AvailableAt = availableAt
	expected.UpdatedAt = updatedAt
	assert.Equal(t, expected, after)
}

func TestPRDevelopmentPublicationRequeueRestoresEveryPreEffectPhase(
	t *testing.T,
) {
	t.Parallel()

	for _, origin := range []PRDevelopmentPublicationStatus{
		PRDevelopmentPublicationPending,
		PRDevelopmentPublicationGateWaiting,
		PRDevelopmentPublicationPushReady,
	} {
		t.Run(string(origin), func(t *testing.T) {
			t.Parallel()

			fixture := newPRDevelopmentPublicationRequeueFixture(t, origin)
			claim := fixture.Claim
			availableAt := fixture.Lifecycle.Clock.Add(2 * time.Minute)
			input := PRDevelopmentPublicationRequeue{
				PublicationID:     claim.ID,
				ClaimToken:        claim.ClaimToken,
				ClaimEpoch:        claim.ClaimEpoch,
				ExpectedClaimFrom: origin,
				AvailableAt:       availableAt,
			}

			requeued, changed, err := fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
				context.Background(),
				input,
			)
			require.NoError(t, err)
			require.True(t, changed)
			assert.Equal(t, origin, requeued.Status)
			assert.Empty(t, requeued.ClaimFrom)
			assert.Empty(t, requeued.ClaimOwner)
			assert.Empty(t, requeued.ClaimToken)
			assert.Nil(t, requeued.ClaimUntil)
			assert.Equal(t, availableAt, requeued.AvailableAt)
			assert.Equal(t, *fixture.Lifecycle.Clock, requeued.UpdatedAt)
			assertPRDevelopmentPublicationRequeuePreserved(
				t,
				claim,
				requeued,
				origin,
				availableAt,
				*fixture.Lifecycle.Clock,
			)
			encoded, encodeErr := json.Marshal(requeued)
			require.NoError(t, encodeErr)
			assert.JSONEq(t, `{}`, string(encoded))

			claimed, claimErr := fixture.Lifecycle.Store.ClaimPRDevelopmentPublications(
				context.Background(),
				PRDevelopmentPublicationClaimRequest{
					WorkerLabel: "publication-requeue-too-early",
					Limit:       1,
					Lease:       time.Minute,
				},
			)
			require.NoError(t, claimErr)
			assert.Empty(t, claimed, "requeued work must remain unavailable before its due time")

			*fixture.Lifecycle.Clock = availableAt
			fresh := claimOnePRDevelopmentPublicationForRequeueTest(
				t,
				&fixture.Lifecycle,
				"publication-requeue-due-worker",
			)
			assert.Equal(t, PRDevelopmentPublicationClaimed, fresh.Status)
			assert.Equal(t, origin, fresh.ClaimFrom)
			assert.Equal(t, claim.ClaimEpoch+1, fresh.ClaimEpoch)
			assert.Equal(t, claim.Claims+1, fresh.Claims)
			assert.Equal(t, claim.Attempts, fresh.Attempts)
			assert.NotEmpty(t, fresh.ClaimToken)
			assert.NotEqual(t, claim.ClaimToken, fresh.ClaimToken)
			assertPRDevelopmentPublicationRequeuePreservedPins(t, claim, fresh)
		})
	}
}

func assertPRDevelopmentPublicationRequeuePreservedPins(
	t *testing.T,
	before PRDevelopmentPublication,
	after PRDevelopmentPublication,
) {
	t.Helper()

	assert.Equal(t, before.PolicyRevision, after.PolicyRevision)
	assert.Equal(t, before.PinnedPolicy, after.PinnedPolicy)
	assert.Equal(t, before.PinnedPolicyHash, after.PinnedPolicyHash)
	assert.Equal(t, before.SubjectRevision, after.SubjectRevision)
	assert.Equal(t, before.PinnedSubject, after.PinnedSubject)
	assert.Equal(t, before.PinnedSubjectHash, after.PinnedSubjectHash)
	assert.Equal(t, before.ProviderObservation, after.ProviderObservation)
	assert.Equal(t, before.ProviderObservationJSON, after.ProviderObservationJSON)
	assert.Equal(t, before.ProviderObservationHash, after.ProviderObservationHash)
	assert.Equal(t, before.ProviderPinnedAt, after.ProviderPinnedAt)
	assert.Equal(t, before.ProviderObservedAt, after.ProviderObservedAt)
	assert.Equal(t, before.DecisionRunID, after.DecisionRunID)
}

func TestPRDevelopmentPublicationRequeueReplaysOnlyExactReleasedIntent(
	t *testing.T,
) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationRequeueFixture(
		t,
		PRDevelopmentPublicationPending,
	)
	input := PRDevelopmentPublicationRequeue{
		PublicationID:     fixture.Claim.ID,
		ClaimToken:        fixture.Claim.ClaimToken,
		ClaimEpoch:        fixture.Claim.ClaimEpoch,
		ExpectedClaimFrom: PRDevelopmentPublicationPending,
		AvailableAt:       fixture.Lifecycle.Clock.Add(time.Minute),
	}
	first, changed, err := fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
		context.Background(),
		input,
	)
	require.NoError(t, err)
	require.True(t, changed)

	replayed, changed, err := fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
		context.Background(),
		input,
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, first, replayed)

	*fixture.Lifecycle.Clock = input.AvailableAt.Add(time.Second)
	replayed, changed, err = fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
		context.Background(),
		input,
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, first, replayed)

	changedTime := input
	changedTime.AvailableAt = changedTime.AvailableAt.Add(time.Second)
	_, changed, err = fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
		context.Background(),
		changedTime,
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentPublicationConflict)
	assert.False(t, changed)

	changedEpoch := input
	changedEpoch.ClaimEpoch++
	_, changed, err = fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
		context.Background(),
		changedEpoch,
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentPublicationConflict)
	assert.False(t, changed)

	changedOrigin := input
	changedOrigin.ExpectedClaimFrom = PRDevelopmentPublicationPushReady
	_, changed, err = fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
		context.Background(),
		changedOrigin,
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentPublicationConflict)
	assert.False(t, changed)

	stored, loadErr := fixture.Lifecycle.Store.GetPRDevelopmentPublication(
		context.Background(),
		fixture.Claim.ID,
	)
	require.NoError(t, loadErr)
	assert.Equal(t, first, stored)
}

func TestPRDevelopmentPublicationRequeueRejectsOldAuthorityAfterFreshClaim(
	t *testing.T,
) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationRequeueFixture(
		t,
		PRDevelopmentPublicationPending,
	)
	input := PRDevelopmentPublicationRequeue{
		PublicationID:     fixture.Claim.ID,
		ClaimToken:        fixture.Claim.ClaimToken,
		ClaimEpoch:        fixture.Claim.ClaimEpoch,
		ExpectedClaimFrom: PRDevelopmentPublicationPending,
		AvailableAt:       fixture.Lifecycle.Clock.Add(time.Minute),
	}
	_, changed, err := fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
		context.Background(),
		input,
	)
	require.NoError(t, err)
	require.True(t, changed)
	*fixture.Lifecycle.Clock = input.AvailableAt
	fresh := claimOnePRDevelopmentPublicationForRequeueTest(
		t,
		&fixture.Lifecycle,
		"publication-requeue-new-epoch",
	)
	require.Equal(t, fixture.Claim.ClaimEpoch+1, fresh.ClaimEpoch)

	_, changed, err = fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
		context.Background(),
		input,
	)
	assert.ErrorIs(t, err, ErrStaleLease)
	assert.False(t, changed)
}

func TestPRDevelopmentPublicationRequeueRequiresExactLiveAuthority(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		mutate func(*prDevelopmentPublicationRequeueFixture, *PRDevelopmentPublicationRequeue)
	}{
		{
			name: "wrong token",
			mutate: func(
				_ *prDevelopmentPublicationRequeueFixture,
				input *PRDevelopmentPublicationRequeue,
			) {
				input.ClaimToken = "lease_deadbeef_" + strings.Repeat("f", 32)
			},
		},
		{
			name: "wrong epoch",
			mutate: func(
				_ *prDevelopmentPublicationRequeueFixture,
				input *PRDevelopmentPublicationRequeue,
			) {
				input.ClaimEpoch++
			},
		},
		{
			name: "expired claim",
			mutate: func(
				fixture *prDevelopmentPublicationRequeueFixture,
				input *PRDevelopmentPublicationRequeue,
			) {
				if fixture.Claim.ClaimUntil == nil {
					panic("requeue fixture has no claim deadline")
				}
				*fixture.Lifecycle.Clock = *fixture.Claim.ClaimUntil
				input.AvailableAt = fixture.Lifecycle.Clock.Add(time.Minute)
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixture := newPRDevelopmentPublicationRequeueFixture(
				t,
				PRDevelopmentPublicationPending,
			)
			input := PRDevelopmentPublicationRequeue{
				PublicationID:     fixture.Claim.ID,
				ClaimToken:        fixture.Claim.ClaimToken,
				ClaimEpoch:        fixture.Claim.ClaimEpoch,
				ExpectedClaimFrom: PRDevelopmentPublicationPending,
				AvailableAt:       fixture.Lifecycle.Clock.Add(time.Minute),
			}
			testCase.mutate(&fixture, &input)

			_, changed, err := fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
				context.Background(),
				input,
			)
			assert.ErrorIs(t, err, ErrStaleLease)
			assert.False(t, changed)
			stored, loadErr := fixture.Lifecycle.Store.GetPRDevelopmentPublication(
				context.Background(),
				fixture.Claim.ID,
			)
			require.NoError(t, loadErr)
			assert.Equal(t, PRDevelopmentPublicationClaimed, stored.Status)
			assert.Equal(t, fixture.Claim.ClaimEpoch, stored.ClaimEpoch)
		})
	}
}

func TestPRDevelopmentPublicationRequeueRejectsLiveOriginMismatch(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationRequeueFixture(
		t,
		PRDevelopmentPublicationPending,
	)
	before, err := fixture.Lifecycle.Store.GetPRDevelopmentPublication(
		context.Background(),
		fixture.Claim.ID,
	)
	require.NoError(t, err)
	_, changed, err := fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
		context.Background(),
		PRDevelopmentPublicationRequeue{
			PublicationID:     fixture.Claim.ID,
			ClaimToken:        fixture.Claim.ClaimToken,
			ClaimEpoch:        fixture.Claim.ClaimEpoch,
			ExpectedClaimFrom: PRDevelopmentPublicationPushReady,
			AvailableAt:       fixture.Lifecycle.Clock.Add(time.Minute),
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentPublicationConflict)
	assert.False(t, changed)
	after, loadErr := fixture.Lifecycle.Store.GetPRDevelopmentPublication(
		context.Background(),
		fixture.Claim.ID,
	)
	require.NoError(t, loadErr)
	assert.Equal(t, before, after)
}

func TestPRDevelopmentPublicationRequeueCancellationDoesNotMutate(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationRequeueFixture(
		t,
		PRDevelopmentPublicationPending,
	)
	before, err := fixture.Lifecycle.Store.GetPRDevelopmentPublication(
		context.Background(),
		fixture.Claim.ID,
	)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, changed, err := fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
		ctx,
		PRDevelopmentPublicationRequeue{
			PublicationID:     fixture.Claim.ID,
			ClaimToken:        fixture.Claim.ClaimToken,
			ClaimEpoch:        fixture.Claim.ClaimEpoch,
			ExpectedClaimFrom: PRDevelopmentPublicationPending,
			AvailableAt:       fixture.Lifecycle.Clock.Add(time.Minute),
		},
	)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, changed)
	after, loadErr := fixture.Lifecycle.Store.GetPRDevelopmentPublication(
		context.Background(),
		fixture.Claim.ID,
	)
	require.NoError(t, loadErr)
	assert.Equal(t, before, after)
}

func TestPRDevelopmentPublicationRequeueRejectsStartedAndTerminalWork(t *testing.T) {
	t.Parallel()

	t.Run("push started", func(t *testing.T) {
		t.Parallel()

		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		started, claim := startPRDevelopmentPublicationForTest(t, &fixture)
		_, changed, err := fixture.Store.RequeuePRDevelopmentPublication(
			context.Background(),
			PRDevelopmentPublicationRequeue{
				PublicationID:     claim.ID,
				ClaimToken:        claim.ClaimToken,
				ClaimEpoch:        claim.ClaimEpoch,
				ExpectedClaimFrom: PRDevelopmentPublicationPushReady,
				AvailableAt:       fixture.Clock.Add(time.Minute),
			},
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentPublicationConflict)
		assert.False(t, changed)
		stored, loadErr := fixture.Store.GetPRDevelopmentPublication(
			context.Background(),
			claim.ID,
		)
		require.NoError(t, loadErr)
		assert.Equal(t, started, stored)
	})

	t.Run("terminal", func(t *testing.T) {
		t.Parallel()

		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		claim := fixture.Claim
		failed, changed, err := fixture.Store.CompletePRDevelopmentPublicationPrestart(
			context.Background(),
			PRDevelopmentPublicationPrestartCompletion{
				PublicationID: claim.ID,
				ClaimToken:    claim.ClaimToken,
				ClaimEpoch:    claim.ClaimEpoch,
				Status:        PRDevelopmentPublicationFailed,
				ErrorCode:     PRDevelopmentPublicationErrorInternal,
				InternalError: "terminal publication must not be requeued",
			},
		)
		require.NoError(t, err)
		require.True(t, changed)
		_, changed, err = fixture.Store.RequeuePRDevelopmentPublication(
			context.Background(),
			PRDevelopmentPublicationRequeue{
				PublicationID:     claim.ID,
				ClaimToken:        claim.ClaimToken,
				ClaimEpoch:        claim.ClaimEpoch,
				ExpectedClaimFrom: PRDevelopmentPublicationPending,
				AvailableAt:       fixture.Clock.Add(time.Minute),
			},
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentPublicationConflict)
		assert.False(t, changed)
		stored, loadErr := fixture.Store.GetPRDevelopmentPublication(
			context.Background(),
			claim.ID,
		)
		require.NoError(t, loadErr)
		assert.Equal(t, failed, stored)
	})
}

func TestPRDevelopmentPublicationRequeueSerializesAgainstPushStart(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	_, claim := claimPushReadyPRDevelopmentPublicationForTest(t, &fixture)
	*fixture.Clock = fixture.Clock.Add(time.Second)
	startInput := PRDevelopmentPublicationPushStart{
		PublicationID: claim.ID,
		ClaimToken:    claim.ClaimToken,
		ClaimEpoch:    claim.ClaimEpoch,
		Observation:   fixture.Observation,
		ObservedAt:    *fixture.Clock,
		Request:       publicationPushRequestFor(claim, fixture.Observation.HeadSHA),
	}
	requeueInput := PRDevelopmentPublicationRequeue{
		PublicationID:     claim.ID,
		ClaimToken:        claim.ClaimToken,
		ClaimEpoch:        claim.ClaimEpoch,
		ExpectedClaimFrom: PRDevelopmentPublicationPushReady,
		AvailableAt:       fixture.Clock.Add(time.Minute),
	}
	type startResult struct {
		started bool
		err     error
	}
	type requeueResult struct {
		changed bool
		err     error
	}
	startResults := make(chan startResult, 1)
	requeueResults := make(chan requeueResult, 1)
	begin := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)
	go func() {
		ready.Done()
		<-begin
		_, started, err := fixture.Store.StartPRDevelopmentPublicationPush(
			context.Background(),
			startInput,
		)
		startResults <- startResult{started: started, err: err}
	}()
	go func() {
		ready.Done()
		<-begin
		_, changed, err := fixture.Store.RequeuePRDevelopmentPublication(
			context.Background(),
			requeueInput,
		)
		requeueResults <- requeueResult{changed: changed, err: err}
	}()
	ready.Wait()
	close(begin)
	startOutcome := <-startResults
	requeueOutcome := <-requeueResults
	assert.NotEqual(t, startOutcome.err == nil, requeueOutcome.err == nil)
	stored, err := fixture.Store.GetPRDevelopmentPublication(
		context.Background(),
		claim.ID,
	)
	require.NoError(t, err)
	if startOutcome.err == nil {
		assert.True(t, startOutcome.started)
		assert.ErrorIs(t, requeueOutcome.err, ErrPRDevelopmentPublicationConflict)
		assert.False(t, requeueOutcome.changed)
		assert.Equal(t, PRDevelopmentPublicationPushStarted, stored.Status)
		assert.NotEmpty(t, stored.PushRequestHash)
	} else {
		assert.ErrorIs(t, startOutcome.err, ErrStaleLease)
		assert.False(t, startOutcome.started)
		require.NoError(t, requeueOutcome.err)
		assert.True(t, requeueOutcome.changed)
		assert.Equal(t, PRDevelopmentPublicationPushReady, stored.Status)
		assert.Empty(t, stored.PushRequestHash)
	}
}

func TestPRDevelopmentPublicationRequeueSerializesIdenticalCalls(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationRequeueFixture(
		t,
		PRDevelopmentPublicationPending,
	)
	input := PRDevelopmentPublicationRequeue{
		PublicationID:     fixture.Claim.ID,
		ClaimToken:        fixture.Claim.ClaimToken,
		ClaimEpoch:        fixture.Claim.ClaimEpoch,
		ExpectedClaimFrom: PRDevelopmentPublicationPending,
		AvailableAt:       fixture.Lifecycle.Clock.Add(time.Minute),
	}
	type result struct {
		publication PRDevelopmentPublication
		changed     bool
		err         error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			publication, changed, err := fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
				context.Background(),
				input,
			)
			results <- result{publication: publication, changed: changed, err: err}
		}()
	}
	ready.Wait()
	close(start)
	first := <-results
	second := <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	assert.NotEqual(t, first.changed, second.changed)
	assert.Equal(t, first.publication, second.publication)
}

func TestPRDevelopmentPublicationRequeueDoesNotRequireCurrentHighWater(
	t *testing.T,
) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationRequeueFixture(
		t,
		PRDevelopmentPublicationPending,
	)
	result, err := fixture.Lifecycle.Store.db.Exec(`
		UPDATE pr_development_thread_controllers
		SET revision = revision + 1
		WHERE id = ?`, fixture.Claim.ControllerID)
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	availableAt := fixture.Lifecycle.Clock.Add(time.Minute)
	requeued, changed, err := fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
		context.Background(),
		PRDevelopmentPublicationRequeue{
			PublicationID:     fixture.Claim.ID,
			ClaimToken:        fixture.Claim.ClaimToken,
			ClaimEpoch:        fixture.Claim.ClaimEpoch,
			ExpectedClaimFrom: PRDevelopmentPublicationPending,
			AvailableAt:       availableAt,
		},
	)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, PRDevelopmentPublicationPending, requeued.Status)
	assert.Equal(t, availableAt, requeued.AvailableAt)
}

func TestPRDevelopmentPublicationRequeueRejectsInvalidAvailability(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		availableAt func(time.Time) time.Time
	}{
		{name: "zero", availableAt: func(time.Time) time.Time { return time.Time{} }},
		{
			name: "before transition",
			availableAt: func(now time.Time) time.Time {
				return now.Add(-time.Nanosecond)
			},
		},
		{
			name: "outside durable range",
			availableAt: func(time.Time) time.Time {
				return time.Date(2500, time.January, 1, 0, 0, 0, 0, time.UTC)
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixture := newPRDevelopmentPublicationRequeueFixture(
				t,
				PRDevelopmentPublicationPending,
			)
			_, changed, err := fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
				context.Background(),
				PRDevelopmentPublicationRequeue{
					PublicationID:     fixture.Claim.ID,
					ClaimToken:        fixture.Claim.ClaimToken,
					ClaimEpoch:        fixture.Claim.ClaimEpoch,
					ExpectedClaimFrom: PRDevelopmentPublicationPending,
					AvailableAt:       testCase.availableAt(*fixture.Lifecycle.Clock),
				},
			)
			assert.Error(t, err)
			assert.False(t, changed)
			stored, loadErr := fixture.Lifecycle.Store.GetPRDevelopmentPublication(
				context.Background(),
				fixture.Claim.ID,
			)
			require.NoError(t, loadErr)
			assert.Equal(t, PRDevelopmentPublicationClaimed, stored.Status)
		})
	}
}

func TestPRDevelopmentPublicationRequeueAcceptsAvailabilityAtTransition(
	t *testing.T,
) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationRequeueFixture(
		t,
		PRDevelopmentPublicationPending,
	)
	requeued, changed, err := fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
		context.Background(),
		PRDevelopmentPublicationRequeue{
			PublicationID:     fixture.Claim.ID,
			ClaimToken:        fixture.Claim.ClaimToken,
			ClaimEpoch:        fixture.Claim.ClaimEpoch,
			ExpectedClaimFrom: PRDevelopmentPublicationPending,
			AvailableAt:       *fixture.Lifecycle.Clock,
		},
	)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, *fixture.Lifecycle.Clock, requeued.AvailableAt)
}

func TestPRDevelopmentPublicationRequeueRejectsInvalidExpectedOrigin(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationRequeueFixture(
		t,
		PRDevelopmentPublicationPending,
	)
	for _, origin := range []PRDevelopmentPublicationStatus{
		"",
		PRDevelopmentPublicationClaimed,
		PRDevelopmentPublicationPushStarted,
		PRDevelopmentPublicationPublished,
	} {
		_, changed, err := fixture.Lifecycle.Store.RequeuePRDevelopmentPublication(
			context.Background(),
			PRDevelopmentPublicationRequeue{
				PublicationID:     fixture.Claim.ID,
				ClaimToken:        fixture.Claim.ClaimToken,
				ClaimEpoch:        fixture.Claim.ClaimEpoch,
				ExpectedClaimFrom: origin,
				AvailableAt:       fixture.Lifecycle.Clock.Add(time.Minute),
			},
		)
		assert.Error(t, err)
		assert.False(t, changed)
	}
}
