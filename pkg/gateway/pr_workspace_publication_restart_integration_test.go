//go:build !mipsle && !netbsd && !(freebsd && arm)

package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/stretchr/testify/require"
)

type restartPublicationPassingGates struct {
	now time.Time
}

func (gates restartPublicationPassingGates) Start(_ context.Context, request prworkspace.GateRequest) (prworkspace.GateRun, error) {
	finished := gates.now
	return prworkspace.GateRun{
		ID:            "pgr_11111111111111111111111111111111",
		DecisionPoint: request.DecisionPoint,
		State:         prworkspace.ExecutionSucceeded,
		Turns: []prworkspace.GateTurn{{
			StageID: "gate", Kind: "deterministic", ActorKind: "deterministic", Status: "answered",
			ExecutionID: "ge_restart-review", ActionRevision: "sha256:restart-action", InputHash: "sha256:restart-input",
			FieldValues: map[string]any{"action": "publish"},
			GateForm:    &prworkspace.GateForm{GateRef: "gates.review-publish", Prompt: "Publish?"},
		}},
		PolicyRevision:  "restart-integration-v1",
		SubjectRevision: request.SubjectDigest,
		CreatedAt:       gates.now,
		FinishedAt:      &finished,
	}, nil
}

func (restartPublicationPassingGates) Respond(
	_ context.Context,
	gate prworkspace.GateRun,
	fieldValues map[string]any,
) (prworkspace.GateRun, error) {
	gate.State = prworkspace.ExecutionSucceeded
	gate.Turns = []prworkspace.GateTurn{{Status: "answered", FieldValues: fieldValues}}
	return gate, nil
}

type restartCapturingReviewPublisher struct {
	requests []prworkspace.ReviewPublicationRequest
	result   prworkspace.ReviewPublicationResult
}

func (publisher *restartCapturingReviewPublisher) PublishReview(
	_ context.Context,
	request prworkspace.ReviewPublicationRequest,
) (prworkspace.ReviewPublicationResult, error) {
	publisher.requests = append(publisher.requests, request)
	return publisher.result, nil
}

func (*restartCapturingReviewPublisher) ReconcileReview(
	context.Context,
	prworkspace.ReviewPublicationRequest,
) (prworkspace.ReviewPublicationResult, bool, error) {
	return prworkspace.ReviewPublicationResult{}, false, nil
}

func TestPRWorkspacePublicationWorkerDispatchesFrozenReviewAfterSQLiteRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	queuedAt := time.Date(2026, time.August, 13, 18, 0, 0, 0, time.UTC)
	databasePath := filepath.Join(t.TempDir(), "publication-restart.sqlite")

	raw, err := eventing.Open(ctx, databasePath, eventing.WithClock(func() time.Time { return queuedAt }))
	require.NoError(t, err)
	durableStore := prworkspace.NewEventingStore(raw)

	workspaceID := "prw_11111111111111111111111111111111"
	charterID := "pcr_11111111111111111111111111111111"
	stageID := "psr_11111111111111111111111111111111"
	findingID := "pfn_11111111111111111111111111111111"
	frozenProvider := prworkspace.ProviderSnapshot{
		Provider: "github", ProviderOrigin: "https://github.example.test",
		RepositoryID: "repository-17", Repository: "octo/frozen-project",
		PullRequestID: "pull-request-19", PullNumber: 19,
		Title: "Frozen review title", Body: "Frozen review body",
		AuthorID: "user-1", AuthorLogin: "octo", AuthenticatedUserID: "reviewer-2",
		BaseRef: "main", BaseSHA: "base-commit",
		HeadRepositoryID: "repository-17", HeadRepository: "octo/frozen-project",
		HeadRef: "fix/retry", HeadSHA: "head-commit", State: "open",
		Owned: true, HeadWritable: true, CanReview: true, CanCreateIssue: true,
		ProviderRevision: "provider-revision-frozen", ObservedAt: queuedAt,
	}
	created, err := durableStore.Create(ctx, prworkspace.CreateInput{
		RequestID: "restart-create-request-0001",
		Workspace: prworkspace.Workspace{
			ID: workspaceID, Provider: frozenProvider.Provider,
			ProviderOrigin: frozenProvider.ProviderOrigin,
			RepositoryID:   frozenProvider.RepositoryID, PullRequestID: frozenProvider.PullRequestID,
			Repository: frozenProvider.Repository, PullNumber: frozenProvider.PullNumber,
			Phase: prworkspace.PhaseCharter, ExecutionState: prworkspace.ExecutionWaitingUser,
			ProviderHeadSHA: frozenProvider.HeadSHA, Version: 1,
			CreatedAt: queuedAt, UpdatedAt: queuedAt,
		},
		Provider: frozenProvider,
	})
	require.NoError(t, err)

	finished := queuedAt
	activeCharterID := charterID
	reviewPhase := prworkspace.PhaseReview
	reviewState := prworkspace.ExecutionSucceeded
	frozenFinding := prworkspace.Finding{
		ID: findingID, Fingerprint: "sha256:frozen-retry-finding",
		Origin: prworkspace.FindingOriginReview, OriginRunID: stageID,
		Severity: "high", Title: "Retry can be lost", Message: "Preserve the retry token.",
		Impact: "A transient failure becomes permanent.", Recommendation: "Retain the token until success.",
		Validation: "Exercise a transient provider failure.",
		Scope: prworkspace.ScopeAssessment{
			Distance: prworkspace.ScopeExact, Size: prworkspace.ChangeSizeXS,
			Presence: prworkspace.WorkCandidatePresent, Files: 1, SemanticLines: 7, Modules: 1,
			TypeCompatible: true, Confidence: 1,
		},
		Disposition: prworkspace.FindingInScope, Version: 1,
		CreatedAt: queuedAt, UpdatedAt: queuedAt,
	}
	seeded, err := durableStore.Mutate(ctx, prworkspace.Mutation{
		WorkspaceID: workspaceID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "restart-seed-request-0001",
		Patch: prworkspace.AggregatePatch{
			Phase: &reviewPhase, ExecutionState: &reviewState, ActiveCharterID: &activeCharterID,
			AppendCharters: []prworkspace.Charter{{
				ID: charterID, Revision: 1, Type: prworkspace.PRTypeFix,
				Goal: "Preserve retries", AcceptanceCriteria: []string{"retry survives a transient failure"},
				IncludedAreas: []string{"pkg/retry"}, ExcludedAreas: []string{"web"},
				BaseSHA: frozenProvider.BaseSHA, HeadSHA: frozenProvider.HeadSHA,
				Confirmed: true, CreatedAt: queuedAt, ConfirmedAt: &finished,
			}},
			AppendStageRuns: []prworkspace.StageRun{{
				ID: stageID, Stage: "review", State: prworkspace.ExecutionSucceeded,
				CharterID: charterID, HeadSHA: frozenProvider.HeadSHA, Attempt: 1,
				Summary: "Frozen review summary", StartedAt: queuedAt, FinishedAt: &finished,
			}},
			UpsertFindings: []prworkspace.Finding{frozenFinding},
		},
	})
	require.NoError(t, err)

	queueService, err := prworkspace.NewService(prworkspace.ServiceConfig{
		Store: durableStore, Gates: restartPublicationPassingGates{now: queuedAt},
		DeferredIssueMode: prworkspace.DeferredIssuesAsk,
		Now:               func() time.Time { return queuedAt },
	})
	require.NoError(t, err)
	queued, err := queueService.QueueReviewPublication(ctx, prworkspace.QueueReviewPublicationRequest{
		WorkspaceID: workspaceID, ExpectedVersion: seeded.Aggregate.Workspace.Version,
		RequestID: "restart-queue-review-0001", ExpectedHeadSHA: frozenProvider.HeadSHA,
		FindingIDs: []string{findingID},
	})
	require.NoError(t, err)
	require.Len(t, queued.Publications, 1)
	require.Equal(t, prworkspace.ExecutionQueued, queued.Publications[0].State)

	// Change mutable provider display data after authorization. A post-restart
	// worker must still use the exact provider embedded in the queued request.
	currentProvider := queued.ProviderSnapshot
	currentProvider.Repository = "octo/renamed-current-project"
	currentProvider.HeadRepository = "octo/renamed-current-project"
	currentProvider.Title = "Current title must not leak into the request"
	currentProvider.ProviderRevision = "provider-revision-current"
	currentProvider.ObservedAt = queuedAt.Add(time.Minute)
	changed, err := durableStore.Mutate(ctx, prworkspace.Mutation{
		WorkspaceID: workspaceID, ExpectedVersion: queued.Workspace.Version,
		RequestID: "restart-provider-change-0001",
		Patch:     prworkspace.AggregatePatch{Provider: &currentProvider},
	})
	require.NoError(t, err)
	require.Equal(t, currentProvider.Repository, changed.Aggregate.ProviderSnapshot.Repository)

	require.NoError(t, raw.Close())
	restartedAt := queuedAt.Add(5 * time.Minute)
	reopened, err := eventing.Open(ctx, databasePath, eventing.WithClock(func() time.Time { return restartedAt }))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	restartedStore := prworkspace.NewEventingStore(reopened)
	restartedService, err := prworkspace.NewService(prworkspace.ServiceConfig{
		Store: restartedStore, Gates: restartPublicationPassingGates{now: restartedAt},
		DeferredIssueMode: prworkspace.DeferredIssuesAsk,
		Now:               func() time.Time { return restartedAt },
	})
	require.NoError(t, err)
	publisher := &restartCapturingReviewPublisher{result: prworkspace.ReviewPublicationResult{
		ExternalID:  "review-9001",
		ExternalURL: "https://github.example.test/octo/frozen-project/pull/19#pullrequestreview-9001",
	}}
	worker := newPRWorkspacePublicationWorker(restartedService, nil, publisher, nil)
	require.NotNil(t, worker)
	worker.now = func() time.Time { return restartedAt }

	processed, err := worker.ProcessOne(ctx)
	require.NoError(t, err)
	require.True(t, processed)
	require.Len(t, publisher.requests, 1)
	publishedRequest := publisher.requests[0]
	require.Equal(t, frozenProvider, publishedRequest.Provider)
	require.NotEqual(t, currentProvider.Repository, publishedRequest.Provider.Repository)
	require.Equal(t, "Frozen review summary", publishedRequest.Summary)
	require.Equal(t, []prworkspace.Finding{frozenFinding}, publishedRequest.Findings)
	require.Contains(t, publishedRequest.Marker, queued.Publications[0].ID)
	require.Contains(t, publishedRequest.Marker, queued.Publications[0].PayloadDigest)

	finalAggregate, err := restartedService.Get(ctx, workspaceID)
	require.NoError(t, err)
	require.Equal(t, currentProvider.Repository, finalAggregate.ProviderSnapshot.Repository)
	require.Len(t, finalAggregate.Publications, 1)
	require.Equal(t, prworkspace.ExecutionSucceeded, finalAggregate.Publications[0].State)
	require.Equal(t, "review-9001", finalAggregate.Publications[0].ExternalID)
}
