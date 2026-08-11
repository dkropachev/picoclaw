//go:build !mipsle && !netbsd && !(freebsd && arm)

package prdevelopment

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

func TestPublicationPushReadyHandlerPublishesRealSQLiteLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, now, developmentCase, identity, reviewDigest := newPublicationPushSQLiteLifecycle(t)
	initialClaims, err := store.ClaimPRDevelopmentPublications(
		ctx,
		eventing.PRDevelopmentPublicationClaimRequest{
			WorkerLabel: "sqlite-publication-gate", Limit: 1, Lease: 5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.Len(t, initialClaims, 1)
	require.Equal(t, eventing.PRDevelopmentPublicationPending, initialClaims[0].ClaimFrom)

	provider := &publicationPushSQLiteProvider{
		caseID:   developmentCase.ID,
		identity: identity,
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
	policySnapshot := attention.PolicySnapshot{Revision: "sqlite-zero-gate-v1"}
	policySource := attention.PolicySourceFunc(func(
		ctx context.Context,
		_ attention.PolicySelector,
		use attention.PolicyUse,
	) error {
		return use(ctx, policySnapshot)
	})
	processor, err := NewPublicationGateProcessor(PublicationGateProcessorConfig{
		Store: store, Policies: policySource, Provider: provider,
	})
	require.NoError(t, err)
	*now = now.Add(time.Second)
	processed, err := processor.ProcessClaim(ctx, initialClaims[0])
	require.NoError(t, err)
	require.Equal(t, PublicationGatePushReady, processed.Disposition)
	require.Equal(t, eventing.PRDevelopmentPublicationPushReady, processed.Publication.Status)

	pushClaims, err := store.ClaimPRDevelopmentPublications(
		ctx,
		eventing.PRDevelopmentPublicationClaimRequest{
			WorkerLabel: "sqlite-publication-push", Limit: 1, Lease: 5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.Len(t, pushClaims, 1)
	require.Equal(t, eventing.PRDevelopmentPublicationPushReady, pushClaims[0].ClaimFrom)

	pusher := &publicationPushSQLitePusher{}
	handler, err := NewPublicationPushReadyHandler(PublicationPushReadyHandlerConfig{
		Store: store, Provider: provider, Pusher: pusher,
		LeaseDuration: 10 * time.Minute,
		Now:           func() time.Time { return *now },
	})
	require.NoError(t, err)
	*now = now.Add(time.Second)
	require.NoError(t, handler.HandlePushReadyClaim(ctx, pushClaims[0]))

	publication, err := store.GetPRDevelopmentPublication(ctx, pushClaims[0].ID)
	require.NoError(t, err)
	assert.Equal(t, eventing.PRDevelopmentPublicationPublished, publication.Status)
	assert.Equal(t, eventing.PRDevelopmentPublicationPushAlreadyCurrent, publication.PushDisposition)
	assert.True(t, publication.WorkspaceClean)
	assert.NotEmpty(t, publication.PushRequestHash)
	assert.NotEmpty(t, publication.PushResultHash)
	assert.NotNil(t, publication.EffectStartedAt)
	assert.NotNil(t, publication.CompletedAt)
	assert.Equal(t, 1, pusher.callCount())
	assert.Equal(t, 2, provider.callCount())
}

func newPublicationPushSQLiteLifecycle(
	t *testing.T,
) (*eventing.Store, *time.Time, eventing.PRDevelopmentCase,
	eventing.PRDevelopmentThreadIdentity, string,
) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	store, err := eventing.Open(
		ctx,
		filepath.Join(t.TempDir(), "publication-push.db"),
		eventing.WithClock(func() time.Time { return now }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	inserted, err := store.Insert(ctx, eventing.Envelope{
		Source: "github", Connector: "github-primary",
		Type: "pull_request_review.submitted", DedupeKey: "sqlite-push-lifecycle",
		Payload: json.RawMessage(`{}`),
		Attributes: map[string]string{
			"body_authenticated": "true", "target_reason": "review_feedback",
		},
	})
	require.NoError(t, err)
	routingClaims, err := store.ClaimRouting(ctx, "sqlite-push-router", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, routingClaims, 1)
	dispatch, created, err := store.CreateRevisionedDispatchForRoutingClaim(
		ctx,
		inserted.Event.Envelope.ID,
		routingClaims[0].Routing.LeaseToken,
		"workflows/own-pr-feedback.yml",
		"sqlite-push-revision",
	)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, store.AckRouting(
		ctx,
		inserted.Event.Envelope.ID,
		routingClaims[0].Routing.LeaseToken,
	))

	headSHA := strings.Repeat("a", 40)
	identity := eventing.PRDevelopmentThreadIdentity{
		Provider: "github", ProviderOrigin: "https://github.com",
		PullAuthorID: "101", RepositoryID: "202", PullRequestID: "303", PullNumber: 42,
	}
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		eventing.PRDevelopmentCaptureRequest{
			Case: eventing.PRDevelopmentCaptureInput{
				PRDevelopmentCaptureIdentity: eventing.PRDevelopmentCaptureIdentity{
					EventID: inserted.Event.Envelope.ID, DispatchID: dispatch.ID,
					RunID: dispatch.RunID, WorkflowRef: dispatch.WorkflowRef,
					WorkflowRevision: dispatch.WorkflowRevision, Connector: "github-primary",
				},
				Repository: "acme/project", PullNumber: 42,
				PullURL:    "https://github.com/acme/project/pull/42",
				PullAuthor: "review-user", TargetUser: "review-user",
				PullState:      eventing.PRDevelopmentPullOpen,
				BaseRepository: "acme/project", BaseRef: "main",
				BaseSHA:        strings.Repeat("1", 40),
				HeadRepository: "review-user/project-fork",
				HeadRef:        "repair/retries", HeadSHA: headSHA,
				ReviewID: "501", TriggerReviewNodeID: "PRR_kwDOReview501",
				ReviewAuthor:         "maintainer-1",
				SubmittedReviewState: eventing.PRDevelopmentReviewChangesRequested,
				CurrentReviewState:   eventing.PRDevelopmentReviewChangesRequested,
				ReviewCommitSHA:      headSHA,
				ReviewSubmittedAt:    now.Add(-time.Hour),
				ReviewURL:            "https://github.com/acme/project/pull/42#pullrequestreview-501",
				Feedback:             "Preserve the exact provider review while repairing locally.",
			},
			Thread: identity,
		},
	)
	require.NoError(t, err)
	require.True(t, created)

	workbench, admitted, err := store.AdmitPRDevelopmentRepair(
		ctx,
		eventing.PRDevelopmentRepairAdmit{
			CaseID:                      developmentCase.ID,
			ExpectedConversationVersion: 0, ExpectedRepairVersion: 0,
			IdempotencyKey: "sqlite-push-attempt", AgentID: "main",
			Instruction: "Address the submitted review locally.",
		},
	)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NotNil(t, workbench.RepairSession)
	attempt := workbench.RepairSession.Attempts[0]
	run, claimed, err := store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		eventing.PRDevelopmentRepairOrchestrationClaim{
			WorkerLabel: "sqlite-orchestration", Lease: 5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, attempt.ID, run.AttemptID)

	reviewDigest := "sha256:" + strings.Repeat("b", 64)
	workspaceID := "gw-sqlite-publication-push"
	sourceTree := strings.Repeat("c", 40)
	run, changed, err := store.PinPRDevelopmentRepairOrchestration(
		ctx,
		eventing.PRDevelopmentRepairOrchestrationPin{
			AttemptID: run.AttemptID, ClaimToken: run.ClaimToken,
			HeadRepository: developmentCase.HeadRepository,
			HeadRef:        developmentCase.HeadRef, HeadSHA: headSHA,
			CloneURL:     "https://github.com/review-user/project-fork.git",
			ReviewDigest: reviewDigest, WorkspaceID: workspaceID, SourceTree: sourceTree,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	controllerLease, acquired, err := store.AcquirePRDevelopmentRepairOrchestrationController(
		ctx,
		eventing.PRDevelopmentRepairOrchestrationControllerAcquire{
			CaseID: developmentCase.ID, AttemptID: run.AttemptID,
			ClaimToken: run.ClaimToken, ExpectedRevision: 0,
			WorkerLabel: "sqlite-orchestration", Lease: 5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	workbench, err = store.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.NotNil(t, workbench.RepairSession)
	session := *workbench.RepairSession

	controller := finalizePublicationPushSQLiteOperation(
		t,
		store,
		controllerLease,
		"pdop_00000000000000000000000000000001",
		eventing.PRDevelopmentControllerOperationAdopt,
		eventing.PRDevelopmentControllerOperationRequest{
			Repository: session.HeadRepository, SourceRef: session.HeadRef,
			SourceCommit: session.HeadSHA, AgentID: session.AgentID,
			WorkspaceID: session.WorkspaceID, LineID: controllerLease.Controller.LineID,
			ExpectedTree: sourceTree,
		},
		eventing.PRDevelopmentControllerOperationResult{
			WorkspaceID: session.WorkspaceID, Version: 0, MutationEpoch: 1,
			Tip: session.HeadSHA, Tree: sourceTree,
		},
	).Controller
	controllerLease = eventing.PRDevelopmentControllerLease{Controller: controller}

	run, changed, err = store.StartPRDevelopmentRepairOrchestrationModel(
		ctx,
		eventing.PRDevelopmentRepairOrchestrationModelStart{
			AttemptID: run.AttemptID, ClaimToken: run.ClaimToken,
			ControllerID: controller.ID, ControllerRevision: controller.Revision,
			MutationLeaseToken: controller.LeaseToken,
			MutationLeaseEpoch: controller.LeaseEpoch,
			ContextDigest:      strings.Repeat("1", 64), PromptDigest: strings.Repeat("2", 64),
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	run, changed, err = store.CompletePRDevelopmentRepairOrchestrationModel(
		ctx,
		eventing.PRDevelopmentRepairOrchestrationModelComplete{
			AttemptID: run.AttemptID, ClaimToken: run.ClaimToken,
			ControllerID: controller.ID, ControllerRevision: controller.Revision,
			MutationLeaseToken: controller.LeaseToken,
			MutationLeaseEpoch: controller.LeaseEpoch,
			ModelResultDigest:  strings.Repeat("3", 64),
			Summary:            "Validated the focused repair locally.", Iterations: 1,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	_, changed, err = store.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx,
		eventing.PRDevelopmentRepairOrchestrationValidation{
			AttemptID: run.AttemptID, ClaimToken: run.ClaimToken,
			ControllerID: controller.ID, ControllerRevision: controller.Revision,
			MutationLeaseToken: controller.LeaseToken,
			MutationLeaseEpoch: controller.LeaseEpoch,
			ParentCommit:       controller.TipCommit, ParentTree: controller.Tree,
			CandidateTree: controller.Tree, CandidateDigest: strings.Repeat("4", 64),
			NoChanges: true, CIStatus: eventing.PRDevelopmentCIPassed,
			CIAttestationID:       "lcatt_sqlite_publication_push",
			CIAttestationDigest:   strings.Repeat("5", 64),
			CIResultKey:           strings.Repeat("6", 64),
			CIEffectivePlanDigest: strings.Repeat("7", 64),
			CIExecutionDigest:     strings.Repeat("8", 64),
		},
	)
	require.NoError(t, err)
	require.True(t, changed)

	parkIntent := "pdlnpark_00000000000000000000000000000001"
	parkTransition := finalizePublicationPushSQLiteOperation(
		t,
		store,
		controllerLease,
		"pdop_00000000000000000000000000000002",
		eventing.PRDevelopmentControllerOperationPark,
		eventing.PRDevelopmentControllerOperationRequest{
			Repository: session.HeadRepository, SourceRef: session.HeadRef,
			SourceCommit: session.HeadSHA, AgentID: session.AgentID,
			WorkspaceID: session.WorkspaceID, LineID: controller.LineID,
			EffectIntentID: parkIntent, ExpectedVersion: controller.LineVersion,
			MutationEpoch: controller.MutationEpoch,
			PreviousTip:   controller.TipCommit, Tip: controller.TipCommit,
			Tree: controller.Tree, NoChanges: true,
			CompletionSummary: run.Summary, CompletionIterations: run.Iterations,
		},
		eventing.PRDevelopmentControllerOperationResult{
			WorkspaceID: session.WorkspaceID,
			Version:     controller.LineVersion + 1, MutationEpoch: controller.MutationEpoch,
			PreviousTip: controller.TipCommit, Tip: controller.TipCommit,
			Tree: controller.Tree, NoChanges: true, WorkspaceClean: true,
			ReviewVersion:       controller.LineVersion + 1,
			ReviewMutationEpoch: controller.MutationEpoch,
			ReviewParkIntentID:  parkIntent, ReviewBaseCommit: controller.TipCommit,
			ReviewCommit: controller.TipCommit, ReviewTree: controller.Tree,
			ReviewDigest: strings.Repeat("f", 64),
		},
	)
	require.NotNil(t, parkTransition.Fence)

	reviewLease, claimed, err := store.ClaimPRDevelopmentReview(
		ctx,
		eventing.PRDevelopmentReviewClaimRequest{
			WorkerLabel: "sqlite-ai-review", Lease: 5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	completion, changed, err := store.CompletePRDevelopmentReview(
		ctx,
		eventing.PRDevelopmentLedgerReviewAppend{
			CaseID: reviewLease.CaseID, AttemptID: reviewLease.Fence.AttemptID,
			ControllerID:     reviewLease.Controller.ID,
			ExpectedRevision: reviewLease.Controller.Revision,
			LeaseToken:       reviewLease.Controller.LeaseToken,
			LeaseEpoch:       reviewLease.Controller.LeaseEpoch,
			Summary:          "The exact local candidate passed review.",
			Outcome:          eventing.PRDevelopmentLedgerReviewPassed,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, completion.Publication)
	return store, &now, developmentCase, identity, reviewDigest
}

func finalizePublicationPushSQLiteOperation(
	t *testing.T,
	store *eventing.Store,
	lease eventing.PRDevelopmentControllerLease,
	operationID string,
	kind eventing.PRDevelopmentControllerOperationKind,
	request eventing.PRDevelopmentControllerOperationRequest,
	result eventing.PRDevelopmentControllerOperationResult,
) eventing.PRDevelopmentControllerOperationTransition {
	t.Helper()
	ctx := context.Background()
	operation, changed, err := store.PreparePRDevelopmentControllerOperation(
		ctx,
		eventing.PRDevelopmentControllerOperationPrepare{
			OperationID: operationID, ControllerID: lease.Controller.ID,
			AttemptID:        lease.Controller.CurrentAttemptID,
			ExpectedRevision: lease.Controller.Revision,
			LeaseToken:       lease.Controller.LeaseToken, LeaseEpoch: lease.Controller.LeaseEpoch,
			Kind: kind, Request: request,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	transition, changed, err := store.FinalizePRDevelopmentControllerOperation(
		ctx,
		eventing.PRDevelopmentControllerOperationFinalize{
			ControllerID: operation.ControllerID, AttemptID: operation.AttemptID,
			OperationID:      operation.ID,
			ExpectedRevision: operation.PreparedControllerRevision,
			LeaseToken:       lease.Controller.LeaseToken,
			LeaseEpoch:       operation.MutationLeaseEpoch,
			Result:           result,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	return transition
}

type publicationPushSQLiteProvider struct {
	mu          sync.Mutex
	caseID      string
	identity    eventing.PRDevelopmentThreadIdentity
	observation eventing.PRDevelopmentPublicationProviderObservation
	now         func() time.Time
	calls       int
}

func (provider *publicationPushSQLiteProvider) ObservePublication(
	ctx context.Context,
	stored eventing.PRDevelopmentCase,
	expected eventing.PRDevelopmentThreadIdentity,
) (TimedPublicationProviderObservation, error) {
	if err := ctx.Err(); err != nil {
		return TimedPublicationProviderObservation{}, err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.calls++
	if stored.ID != provider.caseID || expected != provider.identity {
		return TimedPublicationProviderObservation{}, fmt.Errorf("unexpected publication subject")
	}
	return TimedPublicationProviderObservation{
		Observation: provider.observation, ObservedAt: provider.now(),
	}, nil
}

func (provider *publicationPushSQLiteProvider) callCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.calls
}

type publicationPushSQLitePusher struct {
	mu       sync.Mutex
	calls    int
	requests []gitworkspace.PinnedLinePushRequest
}

func (pusher *publicationPushSQLitePusher) PushPinnedLine(
	ctx context.Context,
	request gitworkspace.PinnedLinePushRequest,
) (gitworkspace.PinnedLinePushResult, error) {
	if err := ctx.Err(); err != nil {
		return gitworkspace.PinnedLinePushResult{}, err
	}
	pusher.mu.Lock()
	defer pusher.mu.Unlock()
	pusher.calls++
	pusher.requests = append(pusher.requests, request)
	return gitworkspace.PinnedLinePushResult{
		WorkspaceID: request.WorkspaceID, Version: request.ExpectedVersion,
		MutationEpoch: request.ExpectedMutationEpoch,
		ParkIntentID:  request.ExpectedParkIntentID,
		BaseCommit:    request.ExpectedBase, Tip: request.ExpectedTip, Tree: request.ExpectedTree,
		RemoteRef:         "refs/heads/" + request.SourceRef,
		ExpectedRemoteTip: request.ExpectedRemoteTip, RemoteTip: request.ExpectedTip,
		Disposition:    gitworkspace.PinnedLinePushAlreadyCurrent,
		WorkspaceClean: true,
	}, nil
}

func (pusher *publicationPushSQLitePusher) callCount() int {
	pusher.mu.Lock()
	defer pusher.mu.Unlock()
	return pusher.calls
}
