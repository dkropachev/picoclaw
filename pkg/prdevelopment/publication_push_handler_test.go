package prdevelopment

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

func TestPublicationPushReadyHandlerFinalizesProvenOutcomes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		disposition    gitworkspace.PinnedLinePushDisposition
		pushErr        error
		workspaceClean bool
		wantDrift      bool
	}{
		{name: "applied", disposition: gitworkspace.PinnedLinePushApplied, workspaceClean: true},
		{name: "already current", disposition: gitworkspace.PinnedLinePushAlreadyCurrent, workspaceClean: true},
		{name: "reconciled", disposition: gitworkspace.PinnedLinePushReconciled, workspaceClean: true},
		{
			name: "proven remote success with local drift", disposition: gitworkspace.PinnedLinePushApplied,
			pushErr: gitworkspace.ErrPinnedLinePushWorkspaceDrift, wantDrift: true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPublicationPushHandlerFixture(t)
			fixture.pusher.disposition = testCase.disposition
			fixture.pusher.pushErr = testCase.pushErr
			fixture.pusher.workspaceClean = testCase.workspaceClean

			err := fixture.handler(t).HandlePushReadyClaim(context.Background(), fixture.claim)
			require.NoError(t, err)
			stored := fixture.store.snapshot()
			assert.Equal(t, eventing.PRDevelopmentPublicationPublished, stored.Status)
			assert.Equal(t, testCase.wantDrift, stored.LocalDrift)
			assert.Equal(t, testCase.workspaceClean, stored.WorkspaceClean)
			assert.Equal(t, string(testCase.disposition), string(stored.PushDisposition))
			assert.Equal(t, 1, fixture.pusher.calls())
			assert.Equal(t, []string{
				"renew_queue", "authenticate", "provider", "start", "renew_push", "push", "finalize",
			}, fixture.operations())
		})
	}
}

func TestPublicationPushReadyHandlerMapsUnprovenOutcomes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		pushErr        error
		corrupt        bool
		workspaceClean bool
		wantStatus     eventing.PRDevelopmentPublicationStatus
		wantCode       eventing.PRDevelopmentPublicationErrorCode
	}{
		{
			name: "typed conflict", pushErr: gitworkspace.ErrPinnedLineConflict,
			wantStatus: eventing.PRDevelopmentPublicationConflict,
			wantCode:   eventing.PRDevelopmentPublicationErrorPushConflict,
		},
		{
			name: "invalid request", pushErr: gitworkspace.ErrPinnedLineInvalid,
			wantStatus: eventing.PRDevelopmentPublicationFailed,
			wantCode:   eventing.PRDevelopmentPublicationErrorPushFailed,
		},
		{
			name: "remote unavailable", pushErr: gitworkspace.ErrPinnedLinePushRemoteUnavailable,
			wantStatus: eventing.PRDevelopmentPublicationFailed,
			wantCode:   eventing.PRDevelopmentPublicationErrorPushFailed,
		},
		{
			name: "unknown wins over conflict", pushErr: errors.Join(
				gitworkspace.ErrPinnedLinePushOutcomeUnknown,
				gitworkspace.ErrPinnedLineConflict,
			),
			wantStatus: eventing.PRDevelopmentPublicationOutcomeUnknown,
			wantCode:   eventing.PRDevelopmentPublicationErrorOutcomeUnknown,
		},
		{
			name: "canceled", pushErr: context.Canceled,
			wantStatus: eventing.PRDevelopmentPublicationOutcomeUnknown,
			wantCode:   eventing.PRDevelopmentPublicationErrorOutcomeUnknown,
		},
		{
			name: "untyped", pushErr: errors.New("opaque push failure"),
			wantStatus: eventing.PRDevelopmentPublicationOutcomeUnknown,
			wantCode:   eventing.PRDevelopmentPublicationErrorOutcomeUnknown,
		},
		{
			name: "malformed result", corrupt: true,
			wantStatus: eventing.PRDevelopmentPublicationOutcomeUnknown,
			wantCode:   eventing.PRDevelopmentPublicationErrorOutcomeUnknown,
		},
		{
			name: "malformed result with typed conflict", corrupt: true,
			pushErr:    gitworkspace.ErrPinnedLineConflict,
			wantStatus: eventing.PRDevelopmentPublicationOutcomeUnknown,
			wantCode:   eventing.PRDevelopmentPublicationErrorOutcomeUnknown,
		},
		{
			name: "malformed result with invalid request", corrupt: true,
			pushErr:    gitworkspace.ErrPinnedLineInvalid,
			wantStatus: eventing.PRDevelopmentPublicationOutcomeUnknown,
			wantCode:   eventing.PRDevelopmentPublicationErrorOutcomeUnknown,
		},
		{
			name: "malformed result with remote unavailable", corrupt: true,
			pushErr:    gitworkspace.ErrPinnedLinePushRemoteUnavailable,
			wantStatus: eventing.PRDevelopmentPublicationOutcomeUnknown,
			wantCode:   eventing.PRDevelopmentPublicationErrorOutcomeUnknown,
		},
		{
			name: "clean result contradicts workspace drift", workspaceClean: true,
			pushErr:    gitworkspace.ErrPinnedLinePushWorkspaceDrift,
			wantStatus: eventing.PRDevelopmentPublicationOutcomeUnknown,
			wantCode:   eventing.PRDevelopmentPublicationErrorOutcomeUnknown,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPublicationPushHandlerFixture(t)
			fixture.pusher.pushErr = testCase.pushErr
			fixture.pusher.corrupt = testCase.corrupt
			fixture.pusher.workspaceClean = testCase.workspaceClean

			err := fixture.handler(t).HandlePushReadyClaim(context.Background(), fixture.claim)
			require.NoError(t, err)
			stored := fixture.store.snapshot()
			assert.Equal(t, testCase.wantStatus, stored.Status)
			assert.Equal(t, testCase.wantCode, stored.LastErrorCode)
			assert.Empty(t, stored.PushResultHash)
			assert.Equal(t, 1, fixture.pusher.calls())
		})
	}
}

func TestPublicationPushReadyHandlerRequiresObservationProof(t *testing.T) {
	t.Parallel()

	t.Run("successful mismatch is provider changed", func(t *testing.T) {
		t.Parallel()
		fixture := newPublicationPushHandlerFixture(t)
		fixture.provider.observation.Observation.HeadSHA = strings.Repeat("f", 40)

		err := fixture.handler(t).HandlePushReadyClaim(context.Background(), fixture.claim)
		require.NoError(t, err)
		stored := fixture.store.snapshot()
		assert.Equal(t, eventing.PRDevelopmentPublicationConflict, stored.Status)
		assert.Equal(t, eventing.PRDevelopmentPublicationErrorProviderChanged, stored.LastErrorCode)
		assert.Equal(t, 0, fixture.store.startCalls)
		assert.Zero(t, fixture.pusher.calls())
	})

	t.Run("definitive verifier drift is provider changed", func(t *testing.T) {
		t.Parallel()
		fixture := newPublicationPushHandlerFixture(t)
		fixture.provider.err = ErrGitHubCaseDrift

		err := fixture.handler(t).HandlePushReadyClaim(context.Background(), fixture.claim)
		require.NoError(t, err)
		stored := fixture.store.snapshot()
		assert.Equal(t, eventing.PRDevelopmentPublicationConflict, stored.Status)
		assert.Equal(t, eventing.PRDevelopmentPublicationErrorProviderChanged, stored.LastErrorCode)
		assert.Zero(t, fixture.store.startCalls)
		assert.Zero(t, fixture.pusher.calls())
	})

	t.Run("untyped provider error is requeued", func(t *testing.T) {
		t.Parallel()
		fixture := newPublicationPushHandlerFixture(t)
		providerErr := errors.New("provider transport unavailable")
		fixture.provider.err = providerErr

		err := fixture.handler(t).HandlePushReadyClaim(context.Background(), fixture.claim)
		assert.ErrorIs(t, err, providerErr)
		stored := fixture.store.snapshot()
		assert.Equal(t, eventing.PRDevelopmentPublicationPushReady, stored.Status)
		assert.Empty(t, stored.ClaimToken)
		assert.Zero(t, fixture.store.startCalls)
		assert.Zero(t, fixture.pusher.calls())
	})
}

func TestPublicationPushReadyHandlerStartReplayNeverRepeatsGit(t *testing.T) {
	t.Parallel()

	t.Run("committed start response loss becomes unknown without Git", func(t *testing.T) {
		t.Parallel()
		fixture := newPublicationPushHandlerFixture(t)
		fixture.store.loseStartResponse = true

		err := fixture.handler(t).HandlePushReadyClaim(context.Background(), fixture.claim)
		require.NoError(t, err)
		stored := fixture.store.snapshot()
		assert.Equal(t, eventing.PRDevelopmentPublicationOutcomeUnknown, stored.Status)
		assert.Equal(t, 2, fixture.store.startCalls)
		assert.Zero(t, fixture.pusher.calls())
		assert.Equal(t, 1, fixture.store.finalizeCalls)
	})

	t.Run("failed first admission retries identical input and pushes once", func(t *testing.T) {
		t.Parallel()
		fixture := newPublicationPushHandlerFixture(t)
		fixture.store.startErrors = []error{errors.New("transient start failure")}

		err := fixture.handler(t).HandlePushReadyClaim(context.Background(), fixture.claim)
		require.NoError(t, err)
		assert.Equal(t, eventing.PRDevelopmentPublicationPublished, fixture.store.snapshot().Status)
		assert.Equal(t, 2, fixture.store.startCalls)
		assert.Equal(t, fixture.store.startInputs[0], fixture.store.startInputs[1])
		assert.Equal(t, 1, fixture.pusher.calls())
	})

	t.Run("terminal replay cannot reenter Git", func(t *testing.T) {
		t.Parallel()
		fixture := newPublicationPushHandlerFixture(t)
		handler := fixture.handler(t)
		require.NoError(t, handler.HandlePushReadyClaim(context.Background(), fixture.claim))
		firstCalls := fixture.pusher.calls()

		err := handler.HandlePushReadyClaim(context.Background(), fixture.claim)
		assert.ErrorIs(t, err, eventing.ErrStaleLease)
		assert.Equal(t, firstCalls, fixture.pusher.calls())
	})
}

func TestPublicationPushReadyHandlerFinalizeReplayNeverRepeatsGit(t *testing.T) {
	t.Parallel()

	fixture := newPublicationPushHandlerFixture(t)
	fixture.store.loseFinalizeResponse = true

	err := fixture.handler(t).HandlePushReadyClaim(context.Background(), fixture.claim)
	require.NoError(t, err)
	assert.Equal(t, eventing.PRDevelopmentPublicationPublished, fixture.store.snapshot().Status)
	assert.Equal(t, 1, fixture.pusher.calls())
	assert.Equal(t, 2, fixture.store.finalizeCalls)
	require.Len(t, fixture.store.finalizeInputs, 2)
	assert.Equal(t, fixture.store.finalizeInputs[0], fixture.store.finalizeInputs[1])
}

func TestPublicationPushReadyHandlerLeaseLossIsEffectSafe(t *testing.T) {
	t.Parallel()

	t.Run("stale queue lease performs no read or effect", func(t *testing.T) {
		t.Parallel()
		fixture := newPublicationPushHandlerFixture(t)
		fixture.store.renewQueueErr = eventing.ErrStaleLease

		err := fixture.handler(t).HandlePushReadyClaim(context.Background(), fixture.claim)
		assert.ErrorIs(t, err, eventing.ErrStaleLease)
		assert.Equal(t, []string{"renew_queue"}, fixture.operations())
		assert.Zero(t, fixture.provider.calls())
		assert.Zero(t, fixture.pusher.calls())
	})

	t.Run("queue renewal configuration error requeues instead of terminalizing", func(t *testing.T) {
		t.Parallel()
		fixture := newPublicationPushHandlerFixture(t)
		fixture.store.renewQueueErr = eventing.ErrInvalidPRDevelopmentPublication

		err := fixture.handler(t).HandlePushReadyClaim(context.Background(), fixture.claim)
		assert.ErrorIs(t, err, eventing.ErrInvalidPRDevelopmentPublication)
		stored := fixture.store.snapshot()
		assert.Equal(t, eventing.PRDevelopmentPublicationPushReady, stored.Status)
		assert.Empty(t, stored.ClaimToken)
		assert.Equal(t, []string{"renew_queue", "requeue"}, fixture.operations())
		assert.Zero(t, fixture.provider.calls())
		assert.Zero(t, fixture.pusher.calls())
	})

	t.Run("push journal renewal failure never calls Git", func(t *testing.T) {
		t.Parallel()
		fixture := newPublicationPushHandlerFixture(t)
		fixture.store.renewPushErr = eventing.ErrStaleLease

		err := fixture.handler(t).HandlePushReadyClaim(context.Background(), fixture.claim)
		assert.ErrorIs(t, err, eventing.ErrStaleLease)
		stored := fixture.store.snapshot()
		assert.Equal(t, eventing.PRDevelopmentPublicationPushStarted, stored.Status)
		assert.Zero(t, fixture.pusher.calls())
	})

	t.Run("live transient push renewal failure records unknown", func(t *testing.T) {
		t.Parallel()
		fixture := newPublicationPushHandlerFixture(t)
		fixture.store.renewPushErr = errors.New("transient journal renewal failure")

		err := fixture.handler(t).HandlePushReadyClaim(context.Background(), fixture.claim)
		require.NoError(t, err)
		stored := fixture.store.snapshot()
		assert.Equal(t, eventing.PRDevelopmentPublicationOutcomeUnknown, stored.Status)
		assert.Zero(t, fixture.pusher.calls())
	})

	t.Run("mid-push journal loss cancels Git and records unknown", func(t *testing.T) {
		t.Parallel()
		fixture := newPublicationPushHandlerFixture(t)
		fixture.store.renewPushFailAfter = 1
		fixture.pusher.blockUntilCanceled = true
		handler := &PublicationPushReadyHandler{
			store: fixture.store, provider: fixture.provider, pusher: fixture.pusher,
			leaseDuration: 30 * time.Millisecond,
			now:           func() time.Time { return fixture.now },
		}

		err := handler.HandlePushReadyClaim(context.Background(), fixture.claim)
		assert.ErrorIs(t, err, eventing.ErrStaleLease)
		stored := fixture.store.snapshot()
		assert.Equal(t, eventing.PRDevelopmentPublicationPushStarted, stored.Status)
		assert.Equal(t, 1, fixture.pusher.calls())
		assert.GreaterOrEqual(t, fixture.store.pushRenewalCalls(), 2)
	})
}

func TestPublicationPushReadyHandlerMapsStartFencesBeforeGit(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		startErr   error
		wantStatus eventing.PRDevelopmentPublicationStatus
		wantCode   eventing.PRDevelopmentPublicationErrorCode
	}{
		{
			name: "superseded", startErr: eventing.ErrPRDevelopmentPublicationSuperseded,
			wantStatus: eventing.PRDevelopmentPublicationSuperseded,
			wantCode:   eventing.PRDevelopmentPublicationErrorSuperseded,
		},
		{
			name: "local high water conflict", startErr: eventing.ErrPRDevelopmentPublicationConflict,
			wantStatus: eventing.PRDevelopmentPublicationConflict,
			wantCode:   eventing.PRDevelopmentPublicationErrorLocalEvidence,
		},
		{
			name: "invalid durable start", startErr: eventing.ErrInvalidPRDevelopmentPublication,
			wantStatus: eventing.PRDevelopmentPublicationRecoveryRequired,
			wantCode:   eventing.PRDevelopmentPublicationErrorRecoveryRequired,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPublicationPushHandlerFixture(t)
			fixture.store.startErrors = []error{testCase.startErr, testCase.startErr}

			err := fixture.handler(t).HandlePushReadyClaim(context.Background(), fixture.claim)
			require.NoError(t, err)
			stored := fixture.store.snapshot()
			assert.Equal(t, testCase.wantStatus, stored.Status)
			assert.Equal(t, testCase.wantCode, stored.LastErrorCode)
			assert.Zero(t, fixture.pusher.calls())
		})
	}
}

func TestPublicationPushReadyHandlerTerminalizesAuthenticationRecovery(t *testing.T) {
	t.Parallel()

	fixture := newPublicationPushHandlerFixture(t)
	fixture.store.authenticateErr = eventing.ErrPRDevelopmentPublicationRecoveryRequired

	err := fixture.handler(t).HandlePushReadyClaim(context.Background(), fixture.claim)
	require.NoError(t, err)
	stored := fixture.store.snapshot()
	assert.Equal(t, eventing.PRDevelopmentPublicationRecoveryRequired, stored.Status)
	assert.Equal(t, eventing.PRDevelopmentPublicationErrorRecoveryRequired, stored.LastErrorCode)
	assert.Equal(t, []string{"renew_queue", "authenticate", "complete_prestart"}, fixture.operations())
	assert.Zero(t, fixture.provider.calls())
	assert.Zero(t, fixture.pusher.calls())
}

func TestPublicationPushReadyHandlerConstructionAndDispatcherIntegration(t *testing.T) {
	t.Parallel()

	_, err := NewPublicationPushReadyHandler(PublicationPushReadyHandlerConfig{})
	assert.ErrorIs(t, err, ErrUnavailable)
	fixture := newPublicationPushHandlerFixture(t)
	encodedConfig, err := json.Marshal(PublicationPushReadyHandlerConfig{
		Store: fixture.store, Provider: fixture.provider, Pusher: fixture.pusher,
		LeaseDuration: time.Minute, Now: func() time.Time { return fixture.now },
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(encodedConfig))
	authentication := eventing.PRDevelopmentPublicationPushAuthentication{
		Publication: fixture.claim,
		Case: eventing.PRDevelopmentCase{
			PRDevelopmentCaptureInput: eventing.PRDevelopmentCaptureInput{
				Feedback: "must remain private",
			},
		},
		ThreadIdentity: fixture.store.threadIdentity,
	}
	encodedAuthentication, err := json.Marshal(authentication)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(encodedAuthentication))
	handler := fixture.handler(t)
	dispatcher, err := NewPublicationDispatcher(PublicationDispatcherConfig{
		Pending: PublicationPendingClaimHandlerFunc(
			func(context.Context, eventing.PRDevelopmentPublication) error {
				t.Fatal("pending handler called")
				return nil
			},
		),
		GateWaiting: PublicationGateWaitingClaimHandlerFunc(
			func(context.Context, eventing.PRDevelopmentPublication) error {
				t.Fatal("gate-waiting handler called")
				return nil
			},
		),
		PushReady: handler,
	})
	require.NoError(t, err)
	require.NoError(t, dispatcher.DispatchClaim(context.Background(), fixture.claim))
	assert.Equal(t, 1, fixture.pusher.calls())
}

func TestPublicationPushReadyClaimAcceptsCompletedActiveGate(t *testing.T) {
	t.Parallel()

	gateFixture := newPublicationGateExecutorFixture(t, publicationGateExecutorPassingGates())
	execution, err := gateFixture.executor(t).ExecuteClaim(context.Background(), gateFixture.claim)
	require.NoError(t, err)
	require.NotEmpty(t, execution.RunID)
	publication := execution.Publication
	publication.DecisionRunID = execution.RunID
	publication.Status = eventing.PRDevelopmentPublicationClaimed
	publication.ClaimFrom = eventing.PRDevelopmentPublicationPushReady
	publication.ClaimOwner = "active-gate-push-test"
	publication.ClaimToken = strings.Repeat("3", 64)
	publication.ClaimEpoch++
	publication.Claims++
	claimedAt := time.Date(2026, 8, 11, 12, 1, 0, 0, time.UTC)
	claimUntil := claimedAt.Add(time.Minute)
	publication.ClaimedAt = &claimedAt
	publication.ClaimUntil = &claimUntil

	require.NoError(t, validatePublicationPushReadyClaim(publication))
}

type publicationPushHandlerFixture struct {
	mu       sync.Mutex
	ops      []string
	now      time.Time
	claim    eventing.PRDevelopmentPublication
	store    *publicationPushStoreFake
	provider *publicationPushProviderFake
	pusher   *publicationPushPusherFake
}

func newPublicationPushHandlerFixture(t *testing.T) *publicationPushHandlerFixture {
	t.Helper()
	base := newPublicationGateProcessorFixture(t, nil)
	processed, err := base.processor(t).ProcessClaim(context.Background(), base.store.publication)
	require.NoError(t, err)
	require.Equal(t, PublicationGatePushReady, processed.Disposition)
	ready := processed.Publication
	now := base.observed.ObservedAt.Add(time.Second)
	claimUntil := now.Add(time.Minute)
	claim := ready
	claim.Status = eventing.PRDevelopmentPublicationClaimed
	claim.ClaimFrom = eventing.PRDevelopmentPublicationPushReady
	claim.ClaimOwner = "publication-push-test"
	claim.ClaimToken = strings.Repeat("1", 64)
	claim.ClaimEpoch++
	claim.Claims++
	claim.ClaimedAt = timePointer(now)
	claim.ClaimUntil = &claimUntil
	claim.AvailableAt = now
	claim.UpdatedAt = now
	fixture := &publicationPushHandlerFixture{now: now, claim: claim}
	fixture.store = &publicationPushStoreFake{
		fixture:        fixture,
		publication:    claim,
		caseValue:      base.store.snapshot.Case,
		threadIdentity: base.store.snapshot.Thread.Identity,
		now:            now,
	}
	fixture.provider = &publicationPushProviderFake{
		fixture: fixture,
		observation: TimedPublicationProviderObservation{
			Observation: claim.ProviderObservation,
			ObservedAt:  now.Add(time.Second),
		},
	}
	fixture.pusher = &publicationPushPusherFake{
		fixture:        fixture,
		disposition:    gitworkspace.PinnedLinePushApplied,
		workspaceClean: true,
	}
	return fixture
}

func (fixture *publicationPushHandlerFixture) handler(t *testing.T) *PublicationPushReadyHandler {
	t.Helper()
	handler, err := NewPublicationPushReadyHandler(PublicationPushReadyHandlerConfig{
		Store: fixture.store, Provider: fixture.provider, Pusher: fixture.pusher,
		LeaseDuration: time.Minute,
		Now:           func() time.Time { return fixture.now },
	})
	require.NoError(t, err)
	return handler
}

func (fixture *publicationPushHandlerFixture) record(operation string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.ops = append(fixture.ops, operation)
}

func (fixture *publicationPushHandlerFixture) operations() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]string(nil), fixture.ops...)
}

type publicationPushStoreFake struct {
	mu                   sync.Mutex
	fixture              *publicationPushHandlerFixture
	publication          eventing.PRDevelopmentPublication
	caseValue            eventing.PRDevelopmentCase
	threadIdentity       eventing.PRDevelopmentThreadIdentity
	now                  time.Time
	authenticateErr      error
	renewQueueErr        error
	renewPushErr         error
	renewPushFailAfter   int
	renewPushCalls       int
	startErrors          []error
	loseStartResponse    bool
	loseFinalizeResponse bool
	startCalls           int
	finalizeCalls        int
	startInputs          []eventing.PRDevelopmentPublicationPushStart
	finalizeInputs       []eventing.PRDevelopmentPublicationPushFinalize
}

func (store *publicationPushStoreFake) GetPRDevelopmentPublication(
	_ context.Context,
	publicationID string,
) (eventing.PRDevelopmentPublication, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if publicationID != store.publication.ID {
		return eventing.PRDevelopmentPublication{}, eventing.ErrNotFound
	}
	return redactPublicationPushClaim(store.publication), nil
}

func (store *publicationPushStoreFake) RenewPRDevelopmentPublication(
	_ context.Context,
	input eventing.PRDevelopmentPublicationRenew,
) error {
	store.fixture.record("renew_queue")
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.renewQueueErr != nil {
		return store.renewQueueErr
	}
	if !store.liveClaim(input) || store.publication.Status != eventing.PRDevelopmentPublicationClaimed ||
		store.publication.ClaimFrom != eventing.PRDevelopmentPublicationPushReady {
		return eventing.ErrStaleLease
	}
	until := store.now.Add(input.Lease)
	if store.publication.ClaimUntil == nil || until.After(*store.publication.ClaimUntil) {
		store.publication.ClaimUntil = &until
	}
	return nil
}

func (store *publicationPushStoreFake) AuthenticateClaimedPRDevelopmentPublicationPush(
	_ context.Context,
	publicationID string,
	claimToken string,
	claimEpoch int64,
) (eventing.PRDevelopmentPublicationPushAuthentication, error) {
	store.fixture.record("authenticate")
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.authenticateErr != nil {
		return eventing.PRDevelopmentPublicationPushAuthentication{}, store.authenticateErr
	}
	if !store.liveClaim(eventing.PRDevelopmentPublicationRenew{
		PublicationID: publicationID, ClaimToken: claimToken, ClaimEpoch: claimEpoch,
	}) || store.publication.Status != eventing.PRDevelopmentPublicationClaimed ||
		store.publication.ClaimFrom != eventing.PRDevelopmentPublicationPushReady {
		return eventing.PRDevelopmentPublicationPushAuthentication{}, eventing.ErrStaleLease
	}
	return eventing.PRDevelopmentPublicationPushAuthentication{
		Publication: redactPublicationPushClaim(store.publication),
		Case:        store.caseValue, ThreadIdentity: store.threadIdentity,
	}, nil
}

func (store *publicationPushStoreFake) RequeuePRDevelopmentPublication(
	_ context.Context,
	input eventing.PRDevelopmentPublicationRequeue,
) (eventing.PRDevelopmentPublication, bool, error) {
	store.fixture.record("requeue")
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.liveClaim(eventing.PRDevelopmentPublicationRenew{
		PublicationID: input.PublicationID, ClaimToken: input.ClaimToken, ClaimEpoch: input.ClaimEpoch,
	}) || input.ExpectedClaimFrom != eventing.PRDevelopmentPublicationPushReady {
		return eventing.PRDevelopmentPublication{}, false, eventing.ErrStaleLease
	}
	store.publication.Status = eventing.PRDevelopmentPublicationPushReady
	store.publication.AvailableAt = input.AvailableAt
	clearPublicationPushClaim(&store.publication)
	return store.publication, true, nil
}

func (store *publicationPushStoreFake) CompletePRDevelopmentPublicationPrestart(
	_ context.Context,
	input eventing.PRDevelopmentPublicationPrestartCompletion,
) (eventing.PRDevelopmentPublication, bool, error) {
	store.fixture.record("complete_prestart")
	store.mu.Lock()
	defer store.mu.Unlock()
	if terminalPublicationPushStatus(store.publication.Status) {
		return redactPublicationPushClaim(store.publication), false, nil
	}
	if !store.liveClaim(eventing.PRDevelopmentPublicationRenew{
		PublicationID: input.PublicationID, ClaimToken: input.ClaimToken, ClaimEpoch: input.ClaimEpoch,
	}) || store.publication.PushRequestHash != "" {
		return eventing.PRDevelopmentPublication{}, false, eventing.ErrStaleLease
	}
	store.publication.Status = input.Status
	store.publication.LastErrorCode = input.ErrorCode
	store.publication.LastErrorDetail = input.InternalError
	completed := store.now
	store.publication.CompletedAt = &completed
	clearPublicationPushClaim(&store.publication)
	return store.publication, true, nil
}

func (store *publicationPushStoreFake) StartPRDevelopmentPublicationPush(
	_ context.Context,
	input eventing.PRDevelopmentPublicationPushStart,
) (eventing.PRDevelopmentPublication, bool, error) {
	store.fixture.record("start")
	store.mu.Lock()
	defer store.mu.Unlock()
	store.startCalls++
	store.startInputs = append(store.startInputs, input)
	if store.publication.PushRequestHash != "" {
		if store.publication.PushRequest != input.Request ||
			!reflect.DeepEqual(store.publication.ProviderObservation, input.Observation) ||
			store.publication.ProviderObservedAt == nil ||
			!store.publication.ProviderObservedAt.Equal(input.ObservedAt) {
			return eventing.PRDevelopmentPublication{}, false,
				eventing.ErrPRDevelopmentPublicationConflict
		}
		return redactPublicationPushClaim(store.publication), false, nil
	}
	if len(store.startErrors) != 0 {
		err := store.startErrors[0]
		store.startErrors = store.startErrors[1:]
		return eventing.PRDevelopmentPublication{}, false, err
	}
	if !store.liveClaim(eventing.PRDevelopmentPublicationRenew{
		PublicationID: input.PublicationID, ClaimToken: input.ClaimToken, ClaimEpoch: input.ClaimEpoch,
	}) {
		return eventing.PRDevelopmentPublication{}, false, eventing.ErrStaleLease
	}
	store.applyStart(input)
	if store.loseStartResponse {
		store.loseStartResponse = false
		return eventing.PRDevelopmentPublication{}, false, context.DeadlineExceeded
	}
	return redactPublicationPushClaim(store.publication), true, nil
}

func (store *publicationPushStoreFake) applyStart(
	input eventing.PRDevelopmentPublicationPushStart,
) {
	store.publication.Status = eventing.PRDevelopmentPublicationPushStarted
	store.publication.ProviderObservedAt = timePointer(input.ObservedAt)
	store.publication.ExpectedRemoteTip = input.Request.ExpectedRemoteTip
	store.publication.PushRequest = input.Request
	store.publication.PushRequestJSON = []byte(`{"request":"durable"}`)
	store.publication.PushRequestHash = strings.Repeat("a", 64)
	store.publication.Attempts = 1
	effectStartedAt := store.now
	store.publication.EffectStartedAt = &effectStartedAt
}

func (store *publicationPushStoreFake) RenewPRDevelopmentPublicationPush(
	_ context.Context,
	input eventing.PRDevelopmentPublicationRenew,
) error {
	store.fixture.record("renew_push")
	store.mu.Lock()
	defer store.mu.Unlock()
	store.renewPushCalls++
	if store.renewPushErr != nil {
		if errors.Is(store.renewPushErr, eventing.ErrStaleLease) {
			store.publication.ClaimUntil = timePointer(store.now)
		}
		return store.renewPushErr
	}
	if store.renewPushFailAfter > 0 && store.renewPushCalls > store.renewPushFailAfter {
		store.publication.ClaimUntil = timePointer(store.now)
		return eventing.ErrStaleLease
	}
	if !store.liveClaim(input) ||
		store.publication.Status != eventing.PRDevelopmentPublicationPushStarted {
		return eventing.ErrStaleLease
	}
	until := store.now.Add(input.Lease)
	if store.publication.ClaimUntil == nil || until.After(*store.publication.ClaimUntil) {
		store.publication.ClaimUntil = &until
	}
	return nil
}

func (store *publicationPushStoreFake) FinalizePRDevelopmentPublicationPush(
	_ context.Context,
	input eventing.PRDevelopmentPublicationPushFinalize,
) (eventing.PRDevelopmentPublication, bool, error) {
	store.fixture.record("finalize")
	store.mu.Lock()
	defer store.mu.Unlock()
	store.finalizeCalls++
	store.finalizeInputs = append(store.finalizeInputs, input)
	if terminalPublicationPushStatus(store.publication.Status) {
		return redactPublicationPushClaim(store.publication), false, nil
	}
	if !store.liveClaim(eventing.PRDevelopmentPublicationRenew{
		PublicationID: input.PublicationID, ClaimToken: input.ClaimToken, ClaimEpoch: input.ClaimEpoch,
	}) || store.publication.Status != eventing.PRDevelopmentPublicationPushStarted ||
		store.publication.PushRequestHash != input.RequestHash {
		return eventing.PRDevelopmentPublication{}, false, eventing.ErrStaleLease
	}
	store.publication.Status = input.Status
	store.publication.PushResult = input.Result
	store.publication.PushResultJSON = nil
	store.publication.PushResultHash = ""
	store.publication.PushDisposition = input.Result.Disposition
	store.publication.WorkspaceClean = input.Result.WorkspaceClean
	store.publication.LocalDrift = input.LocalDrift
	store.publication.LastErrorCode = input.ErrorCode
	store.publication.LastErrorDetail = input.InternalError
	if input.Status == eventing.PRDevelopmentPublicationPublished {
		store.publication.PushResultJSON = []byte(`{"result":"durable"}`)
		store.publication.PushResultHash = strings.Repeat("b", 64)
	}
	completed := store.now
	store.publication.CompletedAt = &completed
	clearPublicationPushClaim(&store.publication)
	if store.loseFinalizeResponse {
		store.loseFinalizeResponse = false
		return eventing.PRDevelopmentPublication{}, false, context.DeadlineExceeded
	}
	return store.publication, true, nil
}

func (store *publicationPushStoreFake) liveClaim(
	input eventing.PRDevelopmentPublicationRenew,
) bool {
	return store.publication.ID == input.PublicationID &&
		store.publication.ClaimToken == input.ClaimToken &&
		store.publication.ClaimEpoch == input.ClaimEpoch &&
		store.publication.ClaimUntil != nil && store.publication.ClaimUntil.After(store.now)
}

func (store *publicationPushStoreFake) snapshot() eventing.PRDevelopmentPublication {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.publication
}

func (store *publicationPushStoreFake) pushRenewalCalls() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.renewPushCalls
}

type publicationPushProviderFake struct {
	mu          sync.Mutex
	fixture     *publicationPushHandlerFixture
	observation TimedPublicationProviderObservation
	err         error
	callCount   int
}

func (provider *publicationPushProviderFake) ObservePublication(
	_ context.Context,
	stored eventing.PRDevelopmentCase,
	expected eventing.PRDevelopmentThreadIdentity,
) (TimedPublicationProviderObservation, error) {
	provider.fixture.record("provider")
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.callCount++
	if stored != provider.fixture.store.caseValue || expected != provider.fixture.store.threadIdentity {
		return TimedPublicationProviderObservation{}, errors.New("provider input changed")
	}
	return provider.observation, provider.err
}

func (provider *publicationPushProviderFake) calls() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.callCount
}

type publicationPushPusherFake struct {
	mu                 sync.Mutex
	fixture            *publicationPushHandlerFixture
	disposition        gitworkspace.PinnedLinePushDisposition
	workspaceClean     bool
	pushErr            error
	corrupt            bool
	blockUntilCanceled bool
	callCount          int
	requests           []gitworkspace.PinnedLinePushRequest
}

func (pusher *publicationPushPusherFake) PushPinnedLine(
	ctx context.Context,
	request gitworkspace.PinnedLinePushRequest,
) (gitworkspace.PinnedLinePushResult, error) {
	pusher.fixture.record("push")
	pusher.mu.Lock()
	defer pusher.mu.Unlock()
	pusher.callCount++
	pusher.requests = append(pusher.requests, request)
	blockUntilCanceled := pusher.blockUntilCanceled
	if blockUntilCanceled {
		pusher.mu.Unlock()
		<-ctx.Done()
		pusher.mu.Lock()
		return gitworkspace.PinnedLinePushResult{}, ctx.Err()
	}
	if pusher.pushErr != nil && !pusher.corrupt &&
		!errors.Is(pusher.pushErr, gitworkspace.ErrPinnedLinePushWorkspaceDrift) {
		return gitworkspace.PinnedLinePushResult{}, pusher.pushErr
	}
	result := gitworkspace.PinnedLinePushResult{
		WorkspaceID: request.WorkspaceID, Version: request.ExpectedVersion,
		MutationEpoch: request.ExpectedMutationEpoch, ParkIntentID: request.ExpectedParkIntentID,
		BaseCommit: request.ExpectedBase, Tip: request.ExpectedTip, Tree: request.ExpectedTree,
		RemoteRef:         "refs/heads/" + request.SourceRef,
		ExpectedRemoteTip: request.ExpectedRemoteTip, RemoteTip: request.ExpectedTip,
		Disposition: pusher.disposition, WorkspaceClean: pusher.workspaceClean,
	}
	if pusher.corrupt {
		result.RemoteTip = strings.Repeat("0", len(request.ExpectedTip))
	}
	return result, pusher.pushErr
}

func (pusher *publicationPushPusherFake) calls() int {
	pusher.mu.Lock()
	defer pusher.mu.Unlock()
	return pusher.callCount
}

func redactPublicationPushClaim(
	publication eventing.PRDevelopmentPublication,
) eventing.PRDevelopmentPublication {
	publication.ClaimToken = ""
	return publication
}

func clearPublicationPushClaim(publication *eventing.PRDevelopmentPublication) {
	publication.ClaimFrom = ""
	publication.ClaimOwner = ""
	publication.ClaimToken = ""
	publication.ClaimUntil = nil
}

func terminalPublicationPushStatus(status eventing.PRDevelopmentPublicationStatus) bool {
	switch status {
	case eventing.PRDevelopmentPublicationPublished,
		eventing.PRDevelopmentPublicationConflict,
		eventing.PRDevelopmentPublicationSuperseded,
		eventing.PRDevelopmentPublicationFailed,
		eventing.PRDevelopmentPublicationRecoveryRequired,
		eventing.PRDevelopmentPublicationOutcomeUnknown:
		return true
	default:
		return false
	}
}

func timePointer(value time.Time) *time.Time {
	copyValue := value
	return &copyValue
}
