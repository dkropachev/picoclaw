//go:build linux && !mipsle

package prdevelopment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/agent"
	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prdevelopment/localci"
	"github.com/sipeed/picoclaw/pkg/providers"
	sessionstore "github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestPRDevelopmentAcceptanceHappyPath(t *testing.T) {
	isolateAcceptanceGitEnvironment(t)
	ctx := context.Background()
	gitLine := newAcceptanceGitLine(t)
	gitTransport := newAcceptanceGitSSHTransport(t, gitLine.remote)
	const cloneURL = "https://github.com/contributor/PicoClaw.git"
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	store, err := eventing.Open(
		ctx,
		filepath.Join(t.TempDir(), "pr-development-acceptance.db"),
		eventing.WithClock(func() time.Time { return now }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	// Capture one authenticated workflow occurrence through the production sink,
	// rather than seeding the development case directly.
	envelope, _, _ := validCaptureOccurrence()
	envelope.ID = ""
	envelope.DedupeKey = "pr-development-acceptance"
	envelope.Payload = json.RawMessage(`{}`)
	envelope.Attributes["pull_request_head_sha"] = gitLine.sourceCommit
	envelope.Attributes["review_commit_sha"] = gitLine.sourceCommit
	inserted, err := store.Insert(ctx, envelope)
	require.NoError(t, err)
	routing, err := store.ClaimRouting(ctx, "acceptance-router", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, routing, 1)
	dispatch, created, err := store.CreateRevisionedDispatchForRoutingClaim(
		ctx,
		inserted.Event.Envelope.ID,
		routing[0].Routing.LeaseToken,
		workflows.GitHubPRDevelopmentWorkflowRef,
		strings.Repeat("b", 64),
	)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, store.AckRouting(
		ctx,
		inserted.Event.Envelope.ID,
		routing[0].Routing.LeaseToken,
	))

	review := providerReviewValue("CHANGES_REQUESTED", "Remove the race before publishing.")
	review["commit_id"] = gitLine.sourceCommit
	runner := &captureToolRunner{responses: []string{
		acceptanceProviderPullJSON(gitLine.sourceCommit, cloneURL),
		providerReviewsJSON(review),
		acceptanceProviderPullJSON(gitLine.sourceCommit, cloneURL),
		providerReviewsJSON(review),
	}}
	run := &workflows.Run{
		ID:          dispatch.RunID,
		WorkflowRef: dispatch.WorkflowRef,
		Status:      workflows.RunStatusSucceeded,
		Outputs: map[string]any{
			WorkflowCaptureOutput: WorkflowCaptureVersion,
		},
	}
	require.NoError(t, (&CaptureSink{
		Store: store,
		Verifier: &GitHubVerifier{
			Runner: runner,
		},
	}).CaptureSucceededEventRun(ctx, inserted.Event.Envelope, dispatch, run))
	require.Len(t, runner.requests, 2)
	assertReadRequest(t, runner.requests[0], "get", 0)
	assertReadRequest(t, runner.requests[1], "get_reviews", 1)

	thread := eventing.PRDevelopmentThreadIdentity{
		Provider:       "github",
		ProviderOrigin: "https://github.com",
		PullAuthorID:   testPullAuthorID,
		RepositoryID:   testRepositoryID,
		PullRequestID:  testPullID,
		PullNumber:     42,
	}
	identity := eventing.PRDevelopmentCaptureIdentity{
		EventID:          inserted.Event.Envelope.ID,
		DispatchID:       dispatch.ID,
		RunID:            dispatch.RunID,
		WorkflowRef:      dispatch.WorkflowRef,
		WorkflowRevision: dispatch.WorkflowRevision,
		Connector:        inserted.Event.Envelope.Connector,
	}
	developmentCase, found, err := store.LookupPRDevelopmentCapture(ctx, identity, &thread)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, gitLine.sourceCommit, developmentCase.HeadSHA)
	assert.Equal(t, "Remove the race before publishing.", developmentCase.Feedback)

	// The same durable case supports an advisory AI conversation and an explicit
	// user-authorized repair admission before any mutation worker takes a lease.
	chatAgent := &acceptanceChatAgent{}
	service, err := NewService(ServiceConfig{
		Store:         store,
		RepairStore:   store,
		RepairEnabled: true,
		RepairAgentReady: func(agentID string) bool {
			return agentID == "main"
		},
		Agent:   chatAgent,
		AgentID: "main",
	})
	require.NoError(t, err)
	detail, err := service.Chat(ctx, ChatRequest{
		CaseID:          developmentCase.ID,
		ExpectedVersion: 0,
		Content:         "Help me preserve the review contract while fixing the race.",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), detail.ConversationVersion)
	require.Len(t, detail.Messages, 2)
	assert.Equal(t, int32(1), chatAgent.calls.Load())
	const repairInstruction = "Apply the focused race fix and run the targeted local check."
	detail, err = service.Repair(ctx, RepairRequest{
		CaseID:                      developmentCase.ID,
		ExpectedConversationVersion: detail.ConversationVersion,
		ExpectedRepairRevision:      0,
		RequestID:                   "prq_00000000000000000000000000000001",
		Instruction:                 repairInstruction,
	})
	require.NoError(t, err)
	require.NotNil(t, detail.RepairSession)
	require.Len(t, detail.RepairSession.Attempts, 1)
	attemptID := detail.RepairSession.Attempts[0].ID
	assert.Equal(t, eventing.PRDevelopmentRepairQueued, detail.RepairSession.Attempts[0].Status)
	assert.Equal(t, repairInstruction, detail.RepairSession.Attempts[0].Instruction)
	const admittedConversationVersion int64 = 2
	require.Equal(t, admittedConversationVersion, detail.ConversationVersion)
	contextLoader, err := newDevelopmentThreadContextLoader(
		developmentThreadContextLoaderConfig{
			Store:       store,
			Agent:       chatAgent,
			AgentID:     "main",
			CompactorID: "ledger-compactor-v1",
		},
	)
	require.NoError(t, err)
	modelContext, err := contextLoader.Load(
		ctx,
		developmentCase.ID,
		admittedConversationVersion,
	)
	require.NoError(t, err)
	assert.Contains(t, modelContext, "Help me preserve the review contract while fixing the race.")
	assert.NotContains(
		t,
		modelContext,
		repairInstruction,
		"repair authority stays separate from untrusted thread context",
	)
	manager, err := gitworkspace.NewManager(gitworkspace.Options{
		RootDir: filepath.Join(t.TempDir(), "git-workspaces"),
		Now:     func() time.Time { return now },
	})
	require.NoError(t, err)
	evidence, err := localci.OpenFileEvidenceStore(filepath.Join(t.TempDir(), "local-ci-evidence"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, evidence.Close()) })
	sandboxRoot := filepath.Join(t.TempDir(), "local-ci-sandbox")
	require.NoError(t, os.MkdirAll(sandboxRoot, 0o700))
	sandbox, err := localci.NewSandbox(localci.SandboxConfig{TemporaryRoot: sandboxRoot})
	if errors.Is(err, localci.ErrSandboxUnavailable) {
		t.Skipf("real local-CI acceptance requires the hardened Linux sandbox: %v", err)
	}
	require.NoError(t, err)
	ciRunner := &localci.Runner{
		Sandbox: sandbox,
		Store:   evidence,
		Limits: localci.Limits{
			StepTimeout:  30 * time.Second,
			TotalTimeout: 2 * time.Minute,
			OutputBytes:  64 << 10,
		},
		Now: func() time.Time { return now },
	}
	repairProvider := &acceptanceRepairProvider{
		instruction: repairInstruction,
		context:     modelContext,
	}
	repairExecutor, err := agent.NewLocalRepairRunner(agent.LocalRepairRunnerConfig{
		Workspaces:    manager,
		Provider:      repairProvider,
		Model:         "acceptance-repair-model",
		MaxIterations: 4,
		MaxTokens:     2048,
	})
	require.NoError(t, err)
	controllerWorker, err := NewRepairControllerWorker(RepairControllerWorkerConfig{
		Store:    store,
		Verifier: &GitHubVerifier{Runner: runner},
		Runtime: func(agentID, routingText string) (LocalRepairExecutor, error) {
			if agentID != "main" || routingText != repairInstruction {
				return nil, fmt.Errorf("unexpected repair runtime route %q/%q", agentID, routingText)
			}
			return repairExecutor, nil
		},
		Workspaces:         func() (*gitworkspace.Manager, error) { return manager, nil },
		LocalCI:            ciRunner,
		ContextAgent:       chatAgent,
		ContextCompactorID: "ledger-compactor-v1",
		WorkerLabel:        "acceptance-repair-controller",
		LeaseDuration:      10 * time.Minute,
	})
	require.NoError(t, err)
	handled, err := controllerWorker.ProcessOne(ctx)
	require.NoError(t, err)
	require.True(t, handled)
	assert.Equal(t, int32(2), repairProvider.calls.Load())
	assert.Equal(t, gitLine.sourceCommit, acceptanceBareRef(t, gitLine.remote, gitLine.sourceRef))
	assert.Equal(t, gitLine.sourceCommit, acceptanceGitCommand(t, gitLine.worktree, "rev-parse", "HEAD"))
	assert.Empty(t, acceptanceGitCommand(t, gitLine.worktree, "status", "--porcelain"))

	completedWorkbench, err := store.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.NotNil(t, completedWorkbench.RepairSession)
	require.NotNil(t, completedWorkbench.LocalEvidence)
	require.NotNil(t, completedWorkbench.LocalEvidence.Controller)
	session := *completedWorkbench.RepairSession
	require.Len(t, session.Attempts, 1)
	completedAttempt := session.Attempts[0]
	assert.Equal(t, attemptID, completedAttempt.ID)
	assert.Equal(t, eventing.PRDevelopmentRepairCompleted, completedAttempt.Status)
	assert.Equal(t, "Applied the focused race fix.", completedAttempt.Summary)
	assert.Equal(t, 2, completedAttempt.Iterations)
	assert.Equal(t, cloneURL, session.CloneURL)
	require.NotEmpty(t, session.WorkspaceID)
	providerReviewDigest := session.ReviewDigest
	require.NotEmpty(t, providerReviewDigest)
	controller := *completedWorkbench.LocalEvidence.Controller
	assert.Equal(t, eventing.PRDevelopmentControllerReviewPending, controller.Phase)
	assert.Empty(t, controller.MutationReservationKey)
	assert.Empty(t, controller.LeaseToken)
	assert.Equal(t, session.WorkspaceID, controller.WorkspaceID)
	assert.Equal(t, gitLine.sourceCommit, controller.SourceCommit)
	require.NotEqual(t, gitLine.sourceCommit, controller.TipCommit)
	require.NotEqual(t, gitLine.sourceTree, controller.Tree)
	candidateCommit := controller.TipCommit
	candidateTree := controller.Tree
	require.Len(t, completedWorkbench.LocalEvidence.Ledger.Entries, 1)
	attemptEntry := completedWorkbench.LocalEvidence.Ledger.Entries[0]
	assert.Equal(t, 0, attemptEntry.Ordinal)
	assert.Equal(t, eventing.PRDevelopmentLedgerAttempt, attemptEntry.Kind)
	assert.Equal(t, attemptID, attemptEntry.AttemptID)
	assert.Equal(t, completedAttempt.Summary, attemptEntry.Summary)
	assert.Equal(t, candidateCommit, attemptEntry.Commit)
	assert.Equal(t, candidateTree, attemptEntry.Tree)
	assert.Equal(t, eventing.PRDevelopmentCIPassed, attemptEntry.CIStatus)
	require.NotEmpty(t, attemptEntry.CIPlanDigest)
	require.NotEmpty(t, attemptEntry.CIResultDigest)
	orchestration, err := store.GetPRDevelopmentRepairOrchestration(ctx, attemptID)
	require.NoError(t, err)
	require.NotNil(t, orchestration.Validation)
	assert.Equal(t, eventing.PRDevelopmentRepairOrchestrationCompleted, orchestration.Phase)
	assert.Equal(t, candidateTree, orchestration.Validation.CandidateTree)
	assert.Equal(t, attemptEntry.CIPlanDigest, orchestration.Validation.CIEffectivePlanDigest)
	assert.Equal(t, attemptEntry.CIResultDigest, orchestration.Validation.CIExecutionDigest)
	plan, found, err := evidence.GetPlan(ctx, attemptEntry.CIPlanDigest)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, plan.Complete)
	assert.Len(t, plan.Steps, 1, "the repository's targeted quick profile is autodiscovered")
	execution, found, err := evidence.GetExecution(ctx, attemptEntry.CIResultDigest)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, localci.StatusPassed, execution.Status)
	assert.Len(t, execution.Steps, len(plan.Steps))
	assert.Equal(t, candidateTree, execution.Evidence.Tree)
	assert.Equal(t, orchestration.Validation.CandidateDigest, execution.Evidence.CandidateDigest)
	parkedSnapshot, err := manager.SnapshotPinnedLineReview(ctx, gitworkspace.PinnedLineReviewRequest{
		LineID: controller.LineID, ExpectedVersion: controller.LineVersion,
		ExpectedBase: gitLine.sourceCommit, ExpectedTip: candidateCommit, ExpectedTree: candidateTree,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"counter.go"}, parkedSnapshot.ChangedPaths)
	assert.Contains(t, parkedSnapshot.UnifiedDiff, "reviewed race fix")

	reviewExecutor := &acceptanceReviewExecutor{
		candidateCommit: candidateCommit,
		candidateTree:   candidateTree,
	}
	reviewWorker, err := NewReviewWorker(ReviewWorkerConfig{
		Store:              store,
		Workspaces:         func() (*gitworkspace.Manager, error) { return manager, nil },
		Evidence:           evidence,
		ContextAgent:       chatAgent,
		ContextCompactorID: "ledger-compactor-v1",
		Runtime: func(agentID string) (LocalReviewExecutor, error) {
			if agentID != "main" {
				return nil, fmt.Errorf("unexpected review runtime agent %q", agentID)
			}
			return reviewExecutor, nil
		},
		WorkerLabel:   "acceptance-local-review",
		LeaseDuration: 10 * time.Minute,
	})
	require.NoError(t, err)
	handled, err = reviewWorker.ProcessOne(ctx)
	require.NoError(t, err)
	require.True(t, handled)
	assert.Equal(t, int32(1), reviewExecutor.calls.Load())

	completedWorkbench, err = store.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.NotNil(t, completedWorkbench.LocalEvidence)
	require.NotNil(t, completedWorkbench.LocalEvidence.Controller)
	assert.Equal(t, eventing.PRDevelopmentControllerReady, completedWorkbench.LocalEvidence.Controller.Phase)
	assert.Empty(t, completedWorkbench.LocalEvidence.Controller.MutationReservationKey)
	require.Len(t, completedWorkbench.LocalEvidence.Ledger.Entries, 2)
	reviewEntry := completedWorkbench.LocalEvidence.Ledger.Entries[1]
	assert.Equal(t, 1, reviewEntry.Ordinal)
	assert.Equal(t, eventing.PRDevelopmentLedgerReview, reviewEntry.Kind)
	assert.Equal(t, attemptID, reviewEntry.AttemptID)
	assert.Equal(t, "The exact locally checked candidate is ready to publish.", reviewEntry.Summary)
	assert.Equal(t, eventing.PRDevelopmentLedgerReviewPassed, reviewEntry.ReviewOutcome)
	publication, err := store.GetPRDevelopmentPublicationForReview(ctx, reviewEntry.ID)
	require.NoError(t, err)
	assert.Equal(t, candidateCommit, publication.TipCommit)
	assert.Equal(t, candidateTree, publication.Tree)
	assert.Equal(t, gitLine.sourceCommit, publication.SourceCommit)
	assert.Equal(t, attemptEntry.CIPlanDigest, publication.CIPlanDigest)
	assert.Equal(t, attemptEntry.CIResultDigest, publication.CIResultDigest)
	assert.False(t, publication.NoChanges)

	provider := &publicationPushSQLiteProvider{
		caseID:   developmentCase.ID,
		identity: thread,
		observation: eventing.PRDevelopmentPublicationProviderObservation{
			Repository: developmentCase.Repository, PullNumber: developmentCase.PullNumber,
			HeadRepository: developmentCase.HeadRepository, HeadRef: developmentCase.HeadRef,
			HeadSHA: gitLine.sourceCommit, HeadCloneURL: cloneURL,
			CurrentReviewState: developmentCase.CurrentReviewState,
			ReviewDigest:       providerReviewDigest,
		},
		now: func() time.Time {
			now = now.Add(time.Second)
			return now
		},
	}
	policies := sharedattention.PolicySourceFunc(func(
		ctx context.Context,
		selector sharedattention.PolicySelector,
		use sharedattention.PolicyUse,
	) error {
		if selector.Repository != developmentCase.Repository ||
			selector.DecisionPoint != eventing.PRDevelopmentPublicationDecisionBeforePush {
			return fmt.Errorf("unexpected publication policy selector: %#v", selector)
		}
		return use(ctx, sharedattention.PolicySnapshot{
			Revision: "pr-development-acceptance-v1",
			Global: []workflows.GateSpec{
				{ID: "acceptance-disabled", Kind: workflows.GateZero},
				{
					ID:        "acceptance-deterministic",
					Kind:      workflows.GateDeterministic,
					When:      "false",
					Title:     "Check deterministic publication policy",
					Questions: []any{"Approve a deterministic publication exception?"},
				},
				{
					ID:       "acceptance-publication-review",
					Kind:     workflows.GateAIIsolatedContext,
					AgentID:  "acceptance-reviewer",
					Criteria: "Approve only the exact locally checked candidate when no user decision is required.",
					Title:    "Review publication readiness",
				},
				{
					ID:       "acceptance-working-review",
					Kind:     workflows.GateAIWorkingContext,
					AgentID:  "main",
					Criteria: "Use the frozen PR discussion and ask only when a user choice is still required.",
					Title:    "Check the owner conversation",
				},
			},
		})
	})
	processor, err := NewPublicationGateProcessor(PublicationGateProcessorConfig{
		Store: store, Policies: policies, Provider: provider,
	})
	require.NoError(t, err)
	runRoot := t.TempDir()
	runs := workflows.NewFileRunStore(runRoot)
	gateRuntime := newAttentionRuntimeFixture(t)
	gateAgent := &attentionRuntimeGateAgent{
		backend: gateRuntime.sessions, runtimeActive: &gateRuntime.runtimeActive,
	}
	gateExecutor, err := NewPublicationGateExecutor(PublicationGateExecutorConfig{
		Store: store,
		Executor: &workflows.Executor{
			WorkspaceDir: runRoot,
			Store:        runs,
			Agents:       gateAgent,
		},
		Runs:     runs,
		Evidence: evidence,
		Workspaces: func() (AttentionReviewWorkspace, error) {
			return manager, nil
		},
		Provider: provider,
		AcquireRuntime: func(
			ctx context.Context,
			agentID string,
		) (context.Context, sessionstore.SessionStore, func(), error) {
			if agentID != "main" || !gateRuntime.runtimeActive.CompareAndSwap(false, true) {
				return nil, nil, nil, errors.New("unexpected acceptance gate runtime acquisition")
			}
			var once sync.Once
			return ctx, gateRuntime.sessions, func() {
				once.Do(func() { gateRuntime.runtimeActive.Store(false) })
			}, nil
		},
	})
	require.NoError(t, err)
	pending, err := NewPublicationPendingGateHandler(PublicationPendingGateHandlerConfig{
		Store: store, Processor: processor, Executor: gateExecutor,
	})
	require.NoError(t, err)
	waiting, err := NewPublicationGateWaitingHandler(PublicationGateWaitingHandlerConfig{
		Store: store, Runs: runs,
	})
	require.NoError(t, err)
	pushReady, err := NewPublicationPushReadyHandler(PublicationPushReadyHandlerConfig{
		Store: store, Provider: provider, Pusher: manager,
		LeaseDuration: 10 * time.Minute,
		Now:           func() time.Time { return now },
	})
	require.NoError(t, err)
	dispatcher, err := NewPublicationDispatcher(PublicationDispatcherConfig{
		Pending: pending, GateWaiting: waiting, PushReady: pushReady,
	})
	require.NoError(t, err)
	worker, err := NewPublicationWorker(PublicationWorkerConfig{
		Queue: store, Dispatcher: dispatcher,
	})
	require.NoError(t, err)

	handled, err = worker.ProcessOne(ctx)
	require.NoError(t, err)
	require.True(t, handled)
	ready, err := store.GetPRDevelopmentPublication(ctx, publication.ID)
	require.NoError(t, err)
	assert.Equal(t, eventing.PRDevelopmentPublicationPushReady, ready.Status)
	assert.Equal(t, 0, gitTransport.receivePackCalls())
	require.NotEmpty(t, ready.DecisionRunID)
	pinnedPolicy, err := sharedattention.DecodePreparedPolicy(ready.PinnedPolicy)
	require.NoError(t, err)
	effectiveGates := pinnedPolicy.EffectiveGates()
	require.Len(t, effectiveGates, 4)
	assert.Equal(t, []string{
		"acceptance-disabled",
		"acceptance-deterministic",
		"acceptance-publication-review",
		"acceptance-working-review",
	}, []string{
		effectiveGates[0].ID,
		effectiveGates[1].ID,
		effectiveGates[2].ID,
		effectiveGates[3].ID,
	})
	assert.Equal(t, []workflows.GateKind{
		workflows.GateZero,
		workflows.GateDeterministic,
		workflows.GateAIIsolatedContext,
		workflows.GateAIWorkingContext,
	}, []workflows.GateKind{
		effectiveGates[0].Kind,
		effectiveGates[1].Kind,
		effectiveGates[2].Kind,
		effectiveGates[3].Kind,
	})
	gateRun, err := runs.GetRun(ctx, ready.DecisionRunID)
	require.NoError(t, err)
	assert.Equal(t, workflows.RunStatusSucceeded, gateRun.Status)
	assert.False(t, gateRuntime.runtimeActive.Load(), "working-context gate must release its runtime")
	require.Len(t, gateAgent.requests, 2)
	assert.Equal(t, "acceptance-reviewer", gateAgent.requests[0].AgentID)
	assert.True(t, gateAgent.requests[0].EphemeralSession)
	assert.Nil(t, gateAgent.requests[0].FrozenReadOnlySession)
	isolatedRequest, err := json.Marshal(gateAgent.requests[0].Inputs)
	require.NoError(t, err)
	assert.Contains(t, string(isolatedRequest), candidateCommit)
	assert.Contains(t, string(isolatedRequest), "counter.go")
	assert.Equal(t, "main", gateAgent.requests[1].AgentID)
	assert.False(t, gateAgent.requests[1].EphemeralSession)
	assert.NotNil(t, gateAgent.requests[1].FrozenReadOnlySession)
	stepIDs := make([]string, 0, len(gateRun.Steps))
	for stepID := range gateRun.Steps {
		stepIDs = append(stepIDs, stepID)
	}
	sort.Strings(stepIDs)
	assert.Equal(t, []string{
		"gates/gate_acceptance-deterministic_attention",
		"gates/gate_acceptance-publication-review_attention",
		"gates/gate_acceptance-publication-review_decision",
		"gates/gate_acceptance-working-review_attention",
		"gates/gate_acceptance-working-review_decision",
	}, stepIDs)
	assert.Equal(
		t,
		workflows.RunStatusSkipped,
		gateRun.Steps["gates/gate_acceptance-deterministic_attention"].Status,
	)
	assert.Equal(
		t,
		workflows.RunStatusSucceeded,
		gateRun.Steps["gates/gate_acceptance-publication-review_decision"].Status,
	)
	assert.Equal(
		t,
		workflows.RunStatusSkipped,
		gateRun.Steps["gates/gate_acceptance-publication-review_attention"].Status,
	)
	assert.Equal(
		t,
		workflows.RunStatusSucceeded,
		gateRun.Steps["gates/gate_acceptance-working-review_decision"].Status,
	)
	assert.Equal(
		t,
		workflows.RunStatusSkipped,
		gateRun.Steps["gates/gate_acceptance-working-review_attention"].Status,
	)

	handled, err = worker.ProcessOne(ctx)
	require.NoError(t, err)
	require.True(t, handled)
	published, err := store.GetPRDevelopmentPublication(ctx, publication.ID)
	require.NoError(t, err)
	assert.Equal(t, eventing.PRDevelopmentPublicationPublished, published.Status)
	assert.Equal(t, eventing.PRDevelopmentPublicationPushApplied, published.PushDisposition)
	assert.Equal(t, candidateCommit, published.PushResult.RemoteTip)
	assert.Equal(t, gitLine.sourceCommit, published.PushResult.ExpectedRemoteTip)
	assert.Equal(t, candidateCommit, published.PushRequest.ExpectedTip)
	assert.NotEmpty(t, published.PushRequestHash)
	assert.NotEmpty(t, published.PushResultHash)
	assert.NotNil(t, published.CompletedAt)
	assert.Equal(t, 1, gitTransport.receivePackCalls())
	assert.Equal(t, 2, provider.callCount())

	handled, err = worker.ProcessOne(ctx)
	require.NoError(t, err)
	assert.False(t, handled)
	assert.Equal(t, 1, gitTransport.receivePackCalls(), "terminal replay must not repeat the Git effect")
	assert.Equal(t, candidateCommit, acceptanceBareRef(t, gitLine.remote, gitLine.sourceRef))
	assert.Equal(
		t,
		gitLine.sourceCommit,
		acceptanceBareRef(t, gitLine.remote, "main"),
		"publication must not change the PR base ref",
	)
	retained, err := manager.SnapshotPinnedLineReview(ctx, gitworkspace.PinnedLineReviewRequest{
		LineID: publication.LineID, ExpectedVersion: publication.LineVersion,
		ExpectedBase: publication.BaseCommit, ExpectedTip: publication.TipCommit,
		ExpectedTree: publication.Tree,
	})
	require.NoError(t, err)
	assert.Equal(t, parkedSnapshot, retained)
	assert.Equal(t, []string{gitLine.sourceRef, "main"}, acceptanceBareBranches(t, gitLine.remote))
	assert.Len(t, runner.requests, 4, "capture and repair verification expose provider reads, not PR writes")
	for index, request := range runner.requests {
		if index%2 == 0 {
			assertReadRequest(t, request, "get", 0)
		} else {
			assertReadRequest(t, request, "get_reviews", 1)
		}
	}
}

type acceptanceGitLine struct {
	worktree     string
	remote       string
	sourceRef    string
	sourceCommit string
	sourceTree   string
}

func newAcceptanceGitLine(t *testing.T) *acceptanceGitLine {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "origin.git")
	worktree := filepath.Join(root, "worktree")
	require.NoError(t, os.MkdirAll(remote, 0o755))
	require.NoError(t, os.MkdirAll(worktree, 0o755))
	acceptanceGitCommand(t, remote, "init", "--bare")
	acceptanceGitCommand(t, worktree, "init")
	acceptanceGitCommand(t, worktree, "config", "user.name", "PicoClaw Acceptance")
	acceptanceGitCommand(t, worktree, "config", "user.email", "acceptance@example.invalid")
	const sourceRef = "feat/fix-race"
	acceptanceGitCommand(t, worktree, "checkout", "-b", sourceRef)
	require.NoError(t, os.WriteFile(
		filepath.Join(worktree, "go.mod"),
		[]byte("module example.invalid/counter\n\ngo 1.24.0\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(worktree, "counter.go"),
		[]byte("package counter\n\nfunc Value() int { return 1 }\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(worktree, "counter_test.go"),
		[]byte(
			"package counter\n\n"+
				"import \"testing\"\n\n"+
				"func TestReviewedValue(t *testing.T) {\n"+
				"\tif got := Value(); got != 2 {\n"+
				"\t\tt.Fatalf(\"Value() = %d, want 2\", got)\n"+
				"\t}\n"+
				"}\n",
		),
		0o644,
	))
	require.NoError(t, os.MkdirAll(filepath.Join(worktree, ".picoclaw"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(worktree, ".picoclaw", "ci.yml"),
		[]byte(
			"version: 1\n"+
				"steps:\n"+
				"  - id: targeted-race-check\n"+
				"    name: Targeted race regression\n"+
				"    kind: test\n"+
				"    run: grep -q 'return 2' counter.go\n"+
				"    shell: sh\n"+
				"    timeout-seconds: 30\n",
		),
		0o644,
	))
	acceptanceGitCommand(t, worktree, "add", ".picoclaw/ci.yml", "counter.go", "counter_test.go", "go.mod")
	acceptanceGitCommand(t, worktree, "commit", "-m", "Seed reviewed branch")
	sourceCommit := acceptanceGitCommand(t, worktree, "rev-parse", "HEAD")
	sourceTree := acceptanceGitCommand(t, worktree, "rev-parse", "HEAD^{tree}")
	acceptanceGitCommand(t, worktree, "push", remote, sourceCommit+":refs/heads/"+sourceRef)
	acceptanceGitCommand(t, worktree, "push", remote, sourceCommit+":refs/heads/main")
	require.Equal(t, sourceCommit, acceptanceBareRef(t, remote, sourceRef))
	return &acceptanceGitLine{
		worktree: worktree, remote: remote, sourceRef: sourceRef,
		sourceCommit: sourceCommit, sourceTree: sourceTree,
	}
}

func acceptanceGitCommand(t *testing.T, directory string, args ...string) string {
	t.Helper()
	output, err := runAcceptanceGit(context.Background(), directory, args...)
	require.NoError(t, err)
	return strings.TrimSpace(output)
}

func runAcceptanceGit(ctx context.Context, directory string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", directory}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Env = acceptanceGitEnvironment()
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func acceptanceBareRef(t *testing.T, remote, sourceRef string) string {
	t.Helper()
	return acceptanceGitCommand(t, remote, "rev-parse", "refs/heads/"+sourceRef)
}

func acceptanceBareBranches(t *testing.T, remote string) []string {
	t.Helper()
	output := acceptanceGitCommand(t, remote, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if output == "" {
		return nil
	}
	branches := strings.Split(output, "\n")
	sort.Strings(branches)
	return branches
}

func acceptanceProviderPullJSON(headSHA, cloneURL string) string {
	value := map[string]any{
		"number": 42, "state": "OPEN", "draft": false, "merged": false,
		"html_url": "https://github.com/ScyllaDB/PicoClaw/pull/42",
		"user":     map[string]any{"login": "Review-User", "id": json.Number(testPullAuthorID)},
		"head": map[string]any{
			"ref": "feat/fix-race", "sha": headSHA,
			"repo": map[string]any{
				"full_name": "contributor/PicoClaw", "clone_url": cloneURL,
			},
		},
		"base": map[string]any{
			"ref": "main", "sha": headSHA,
			"repo": map[string]any{"full_name": "ScyllaDB/PicoClaw"},
		},
	}
	raw, _ := json.Marshal(value)
	return string(raw)
}

type acceptanceChatAgent struct {
	calls atomic.Int32
}

func (agent *acceptanceChatAgent) RunAgent(
	ctx context.Context,
	request workflows.AgentRequest,
) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	agent.calls.Add(1)
	if request.AgentID != "main" || !request.PrivateContext || !request.EphemeralSession ||
		request.Session != "" || request.Tools != workflows.AgentToolsNone ||
		request.History != "none" || request.Cache != "none" ||
		!strings.Contains(request.Context, "Remove the race before publishing.") {
		return nil, errors.New("development chat escaped its bounded private context")
	}
	return map[string]any{
		"text": "Keep the provider review fixed, make the smallest local change, " +
			"then require the targeted check and local review before publication.",
	}, nil
}

type acceptanceRepairProvider struct {
	instruction string
	context     string
	calls       atomic.Int32
}

func (provider *acceptanceRepairProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	definitions []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if provider == nil || model != "acceptance-repair-model" || len(options) != 2 ||
		options["max_tokens"] != 2048 || options["temperature"] != float64(0) {
		return nil, errors.New("repair model profile changed")
	}
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Function.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "apply_patch,edit_file,list_dir,read_file" {
		return nil, errors.New("repair model capability set changed")
	}

	call := provider.calls.Add(1)
	switch call {
	case 1:
		if len(messages) != 2 || messages[0].Role != "system" ||
			messages[1].Role != "user" ||
			!strings.Contains(messages[0].Content, "You have no shell, process, network") ||
			!strings.Contains(messages[0].Content, "Do not claim validation that you could not run.") ||
			messages[1].Content != "TASK FROM USER:\n"+provider.instruction+
				"\n\nREPOSITORY CONTEXT:\n"+provider.context {
			return nil, errors.New("repair model context crossed its authority boundary")
		}
		return &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
			ID:   "acceptance-edit-counter",
			Name: "edit_file",
			Arguments: map[string]any{
				"path":     "counter.go",
				"old_text": "package counter\n\nfunc Value() int { return 1 }\n",
				"new_text": "package counter\n\nfunc Value() int { return 2 } // reviewed race fix\n",
			},
			Function: &providers.FunctionCall{Name: "edit_file"},
		}}}, nil
	case 2:
		if len(messages) != 4 || messages[2].Role != "assistant" ||
			len(messages[2].ToolCalls) != 1 ||
			messages[2].ToolCalls[0].Name != "edit_file" ||
			messages[3].Role != "tool" ||
			messages[3].ToolCallID != "acceptance-edit-counter" ||
			strings.TrimSpace(messages[3].Content) == "" {
			return nil, errors.New("repair model did not receive the guarded edit result")
		}
		return &providers.LLMResponse{Content: "Applied the focused race fix."}, nil
	default:
		return nil, errors.New("repair model invocation count changed")
	}
}

type acceptanceReviewExecutor struct {
	candidateCommit string
	candidateTree   string
	calls           atomic.Int32
}

func (executor *acceptanceReviewExecutor) Run(
	ctx context.Context,
	request agent.ControllerLocalReviewRequest,
) (agent.ControllerLocalReviewResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ControllerLocalReviewResult{}, err
	}
	if executor.calls.Add(1) != 1 ||
		!strings.Contains(request.Context, executor.candidateCommit) ||
		!strings.Contains(request.Context, executor.candidateTree) ||
		!strings.Contains(request.Context, "counter.go") ||
		!strings.Contains(request.Context, "reviewed race fix") ||
		!strings.Contains(request.Context, "Applied the focused race fix") ||
		!strings.Contains(request.Context, `"status":"passed"`) {
		return agent.ControllerLocalReviewResult{}, errors.New(
			"local review did not receive the exact parked CI-bound candidate",
		)
	}
	return agent.ControllerLocalReviewResult{
		Outcome: agent.ControllerLocalReviewPassed,
		Summary: "The exact locally checked candidate is ready to publish.",
	}, nil
}

type acceptanceGitSSHTransport struct {
	logPath string
}

func newAcceptanceGitSSHTransport(
	t *testing.T,
	bareRepository string,
) *acceptanceGitSSHTransport {
	t.Helper()
	root := t.TempDir()
	logPath := filepath.Join(root, "ssh-effects.log")
	shimPath := filepath.Join(root, "ssh")
	const remoteEnvironment = "PICOCLAW_ACCEPTANCE_BARE_REPOSITORY"
	const logEnvironment = "PICOCLAW_ACCEPTANCE_SSH_LOG"
	const envEnvironment = "PICOCLAW_ACCEPTANCE_ENV"
	const uploadPackEnvironment = "PICOCLAW_ACCEPTANCE_UPLOAD_PACK"
	const receivePackEnvironment = "PICOCLAW_ACCEPTANCE_RECEIVE_PACK"
	envPath, err := exec.LookPath("env")
	require.NoError(t, err)
	uploadPackPath, err := exec.LookPath("git-upload-pack")
	require.NoError(t, err)
	receivePackPath, err := exec.LookPath("git-receive-pack")
	require.NoError(t, err)
	for _, executable := range []string{envPath, uploadPackPath, receivePackPath} {
		require.True(t, filepath.IsAbs(executable), "server executable must resolve absolutely")
	}
	shim := `#!/bin/sh
set -eu
batch_mode=0
clear_forwardings=0
deny_local_command=0
send_protocol=0
while [ "$#" -gt 2 ]; do
  case "$1" in
    -oBatchMode=yes)
      [ "$batch_mode" -eq 0 ] || exit 90
      batch_mode=1
      ;;
    -oClearAllForwardings=yes)
      [ "$clear_forwardings" -eq 0 ] || exit 90
      clear_forwardings=1
      ;;
    -oPermitLocalCommand=no)
      [ "$deny_local_command" -eq 0 ] || exit 90
      deny_local_command=1
      ;;
    -o)
      shift
      [ "$#" -gt 2 ] || exit 90
      [ "$1" = "SendEnv=GIT_PROTOCOL" ] || exit 90
      [ "$send_protocol" -eq 0 ] || exit 90
      send_protocol=1
      ;;
    *)
      exit 90
      ;;
  esac
  shift
done
[ "$#" -eq 2 ] || exit 90
host="$1"
remote_command="$2"
if [ "$host" != "git@github.com" ]; then
  exit 91
fi
case "$remote_command" in
  "git-upload-pack 'contributor/PicoClaw.git'"|"git-upload-pack '/contributor/PicoClaw.git'")
    if [ "$batch_mode" -ne "$clear_forwardings" ] ||
       [ "$batch_mode" -ne "$deny_local_command" ]; then
      exit 93
    fi
    printf '%s\n' upload >> "$PICOCLAW_ACCEPTANCE_SSH_LOG"
    if [ -n "${GIT_PROTOCOL-}" ]; then
      [ "$send_protocol" -eq 1 ] || exit 94
      [ "$GIT_PROTOCOL" = "version=2" ] || exit 94
      exec "$PICOCLAW_ACCEPTANCE_ENV" -i \
        "GIT_PROTOCOL=$GIT_PROTOCOL" \
        "$PICOCLAW_ACCEPTANCE_UPLOAD_PACK" \
        "$PICOCLAW_ACCEPTANCE_BARE_REPOSITORY"
    fi
    exec "$PICOCLAW_ACCEPTANCE_ENV" -i \
      "$PICOCLAW_ACCEPTANCE_UPLOAD_PACK" \
      "$PICOCLAW_ACCEPTANCE_BARE_REPOSITORY"
    ;;
  "git-receive-pack 'contributor/PicoClaw.git'"|"git-receive-pack '/contributor/PicoClaw.git'")
    [ "$batch_mode" -eq 1 ] || exit 93
    [ "$clear_forwardings" -eq 1 ] || exit 93
    [ "$deny_local_command" -eq 1 ] || exit 93
    [ "${GIT_SSH_VARIANT-}" = "ssh" ] || exit 93
    [ "${SSH_ASKPASS_REQUIRE-}" = "never" ] || exit 93
    printf '%s\n' receive >> "$PICOCLAW_ACCEPTANCE_SSH_LOG"
    exec "$PICOCLAW_ACCEPTANCE_ENV" -i \
      "$PICOCLAW_ACCEPTANCE_RECEIVE_PACK" \
      "$PICOCLAW_ACCEPTANCE_BARE_REPOSITORY"
    ;;
  *)
    exit 92
    ;;
esac
`
	require.NoError(t, os.WriteFile(shimPath, []byte(shim), 0o700))
	t.Setenv(remoteEnvironment, bareRepository)
	t.Setenv(logEnvironment, logPath)
	t.Setenv(envEnvironment, envPath)
	t.Setenv(uploadPackEnvironment, uploadPackPath)
	t.Setenv(receivePackEnvironment, receivePackPath)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &acceptanceGitSSHTransport{logPath: logPath}
}

func (transport *acceptanceGitSSHTransport) receivePackCalls() int {
	raw, err := os.ReadFile(transport.logPath)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		return -1
	}
	calls := 0
	for _, entry := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if entry == "receive" {
			calls++
		}
	}
	return calls
}

func isolateAcceptanceGitEnvironment(t *testing.T) {
	t.Helper()
	original := make(map[string]string)
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || upper == "HOME" ||
			upper == "SSH_AUTH_SOCK" || upper == "SSH_ASKPASS" ||
			upper == "SSH_ASKPASS_REQUIRE" {
			original[name] = value
			require.NoError(t, os.Unsetenv(name))
		}
	}
	home := t.TempDir()
	require.NoError(t, os.Setenv("HOME", home))
	require.NoError(t, os.Setenv("GIT_CONFIG_NOSYSTEM", "1"))
	require.NoError(t, os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull))
	require.NoError(t, os.Setenv("GIT_TERMINAL_PROMPT", "0"))
	t.Cleanup(func() {
		for _, entry := range os.Environ() {
			name, _, _ := strings.Cut(entry, "=")
			upper := strings.ToUpper(name)
			if strings.HasPrefix(upper, "GIT_") || upper == "HOME" ||
				upper == "SSH_AUTH_SOCK" || upper == "SSH_ASKPASS" ||
				upper == "SSH_ASKPASS_REQUIRE" {
				_ = os.Unsetenv(name)
			}
		}
		for name, value := range original {
			_ = os.Setenv(name, value)
		}
	})
}

func acceptanceGitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+12)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || upper == "SSH_ASKPASS" ||
			upper == "SSH_ASKPASS_REQUIRE" {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment,
		"GIT_AUTHOR_NAME=PicoClaw Acceptance",
		"GIT_AUTHOR_EMAIL=acceptance@example.invalid",
		"GIT_COMMITTER_NAME=PicoClaw Acceptance",
		"GIT_COMMITTER_EMAIL=acceptance@example.invalid",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=protocol.file.allow",
		"GIT_CONFIG_VALUE_0=always",
		"GIT_TERMINAL_PROMPT=0",
	)
}

var (
	_ workflows.AgentRunner = (*acceptanceChatAgent)(nil)
	_ providers.LLMProvider = (*acceptanceRepairProvider)(nil)
	_ LocalReviewExecutor   = (*acceptanceReviewExecutor)(nil)
)
