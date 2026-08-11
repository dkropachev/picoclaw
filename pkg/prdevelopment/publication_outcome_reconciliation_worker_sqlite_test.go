//go:build !mipsle && !netbsd && !(freebsd && arm)

package prdevelopment

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

func TestPublicationOutcomeReconciliationWorkerRealSQLiteLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, now, developmentCase, identity, reviewDigest := newPublicationPushSQLiteLifecycle(t)
	initialClaims, err := store.ClaimPRDevelopmentPublications(
		ctx,
		eventing.PRDevelopmentPublicationClaimRequest{
			WorkerLabel: "sqlite-outcome-gate", Limit: 1, Lease: 5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.Len(t, initialClaims, 1)

	provider := &publicationPushSQLiteProvider{
		caseID: developmentCase.ID, identity: identity,
		observation: eventing.PRDevelopmentPublicationProviderObservation{
			Repository: developmentCase.Repository, PullNumber: developmentCase.PullNumber,
			HeadRepository:     developmentCase.HeadRepository,
			HeadRef:            initialClaims[0].SourceRef,
			HeadSHA:            initialClaims[0].SourceCommit,
			HeadCloneURL:       initialClaims[0].SourceCloneURL,
			CurrentReviewState: developmentCase.CurrentReviewState,
			ReviewDigest:       reviewDigest,
		},
		now: func() time.Time {
			*now = now.Add(time.Second)
			return *now
		},
	}
	processor, err := NewPublicationGateProcessor(PublicationGateProcessorConfig{
		Store: store,
		Policies: attention.PolicySourceFunc(func(
			ctx context.Context,
			_ attention.PolicySelector,
			use attention.PolicyUse,
		) error {
			return use(ctx, attention.PolicySnapshot{Revision: "sqlite-outcome-zero-gate-v1"})
		}),
		Provider: provider,
	})
	require.NoError(t, err)
	processed, err := processor.ProcessClaim(ctx, initialClaims[0])
	require.NoError(t, err)
	require.Equal(t, PublicationGatePushReady, processed.Disposition)

	pushClaims, err := store.ClaimPRDevelopmentPublications(
		ctx,
		eventing.PRDevelopmentPublicationClaimRequest{
			WorkerLabel: "sqlite-outcome-push", Limit: 1, Lease: 5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.Len(t, pushClaims, 1)
	pusher := &publicationOutcomeUnknownSQLitePusher{}
	handler, err := NewPublicationPushReadyHandler(PublicationPushReadyHandlerConfig{
		Store: store, Provider: provider, Pusher: pusher,
		LeaseDuration: 10 * time.Minute,
		Now:           func() time.Time { return *now },
	})
	require.NoError(t, err)
	require.NoError(t, handler.HandlePushReadyClaim(ctx, pushClaims[0]))
	unknown, err := store.GetPRDevelopmentPublication(ctx, pushClaims[0].ID)
	require.NoError(t, err)
	require.Equal(t, eventing.PRDevelopmentPublicationOutcomeUnknown, unknown.Status)
	require.NotNil(t, unknown.CompletedAt)
	require.Equal(t, 1, pusher.callCount())

	headObserver := &publicationOutcomeSQLiteHeadObserver{
		caseID: developmentCase.ID, identity: identity,
		observation: eventing.PRDevelopmentPublicationRemoteObservation{
			Repository:     unknown.ProviderObservation.Repository,
			PullNumber:     unknown.ProviderObservation.PullNumber,
			HeadRepository: unknown.ProviderObservation.HeadRepository,
			HeadRef:        unknown.ProviderObservation.HeadRef,
			HeadSHA:        unknown.PushRequest.ExpectedTip,
		},
		now: now,
	}
	worker, err := NewPublicationOutcomeReconciliationWorker(
		PublicationOutcomeReconciliationWorkerConfig{
			Store: store, Observer: headObserver, BatchLimit: 8,
			Now: func() time.Time { return *now },
		},
	)
	require.NoError(t, err)
	worked, err := worker.ProcessOne(ctx)
	require.NoError(t, err)
	require.True(t, worked)

	published, err := store.GetPRDevelopmentPublication(ctx, unknown.ID)
	require.NoError(t, err)
	assert.Equal(t, eventing.PRDevelopmentPublicationPublished, published.Status)
	assert.Equal(t, eventing.PRDevelopmentPublicationPushReconciled, published.PushDisposition)
	assert.Equal(t, publicationOutcomeReconciledResult(unknown), published.PushResult)
	assert.Equal(t, unknown.CompletedAt, published.CompletedAt)
	assert.False(t, published.WorkspaceClean)
	assert.False(t, published.LocalDrift)
	assert.Equal(t, unknown.ProviderObservationHash, published.ProviderObservationHash)
	assert.Equal(t, 1, pusher.callCount(), "reconciliation must never repeat Git")
	assert.Equal(t, 1, headObserver.callCount())

	worked, err = worker.ProcessOne(ctx)
	require.NoError(t, err)
	assert.False(t, worked)
	assert.Equal(t, 1, pusher.callCount())
	assert.Equal(t, 1, headObserver.callCount())
}

type publicationOutcomeUnknownSQLitePusher struct {
	mu    sync.Mutex
	calls int
}

func (pusher *publicationOutcomeUnknownSQLitePusher) PushPinnedLine(
	ctx context.Context,
	_ gitworkspace.PinnedLinePushRequest,
) (gitworkspace.PinnedLinePushResult, error) {
	if err := ctx.Err(); err != nil {
		return gitworkspace.PinnedLinePushResult{}, err
	}
	pusher.mu.Lock()
	pusher.calls++
	pusher.mu.Unlock()
	return gitworkspace.PinnedLinePushResult{}, gitworkspace.ErrPinnedLinePushOutcomeUnknown
}

func (pusher *publicationOutcomeUnknownSQLitePusher) callCount() int {
	pusher.mu.Lock()
	defer pusher.mu.Unlock()
	return pusher.calls
}

type publicationOutcomeSQLiteHeadObserver struct {
	mu          sync.Mutex
	caseID      string
	identity    eventing.PRDevelopmentThreadIdentity
	observation eventing.PRDevelopmentPublicationRemoteObservation
	now         *time.Time
	calls       int
}

func (observer *publicationOutcomeSQLiteHeadObserver) ObservePublicationRemoteHead(
	ctx context.Context,
	stored eventing.PRDevelopmentCase,
	expected eventing.PRDevelopmentThreadIdentity,
) (TimedPublicationRemoteObservation, error) {
	if err := ctx.Err(); err != nil {
		return TimedPublicationRemoteObservation{}, err
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.calls++
	if stored.ID != observer.caseID || expected != observer.identity {
		return TimedPublicationRemoteObservation{}, eventing.ErrInvalidPRDevelopmentPublication
	}
	*observer.now = observer.now.Add(time.Second)
	return TimedPublicationRemoteObservation{
		Observation: observer.observation,
		ObservedAt:  *observer.now,
	}, nil
}

func (observer *publicationOutcomeSQLiteHeadObserver) callCount() int {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.calls
}
