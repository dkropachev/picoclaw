//go:build !mipsle && !netbsd && !(freebsd && arm)

package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prdevelopment"
	"github.com/sipeed/picoclaw/pkg/prdevelopment/localci"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type gatewayReviewGenerationContextKey struct{}

type gatewayReviewGenerationLease struct {
	released chan struct{}
}

func TestPRDevelopmentPendingReviewRuntimeDoesNotRequireProviderMCP(t *testing.T) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		workspace+"/eventing/events.db",
		true,
		false,
	)
	msgBus := bus.NewMessageBus()
	provider := &orderedShutdownProvider{closed: make(chan struct{})}
	agentLoop := agent.NewAgentLoop(cfg, msgBus, provider)
	service, err := setupEventAutomationService(context.Background(), cfg, agentLoop)
	if err != nil {
		msgBus.Close()
		agentLoop.Close()
		t.Fatalf("setupEventAutomationService() error = %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := service.Close(closeCtx); closeErr != nil {
			t.Errorf("event automation Close() error = %v", closeErr)
		}
		msgBus.Close()
		agentLoop.Close()
	})

	if service.prLocalCI == nil || service.prLocalCI.evidence == nil {
		t.Fatal("local review evidence runtime was not composed without provider MCP")
	}
}

func TestPRDevelopmentReviewWorkerHoldsRuntimeGeneration(t *testing.T) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		workspace+"/eventing/events.db",
		true,
		false,
	)
	entered := make(chan *gatewayReviewGenerationLease, 1)
	releaseProcess := make(chan struct{})
	var processOnce sync.Once
	reviewProcess := func(ctx context.Context) (bool, error) {
		processed := false
		processOnce.Do(func() {
			processed = true
			lease, _ := ctx.Value(gatewayReviewGenerationContextKey{}).(*gatewayReviewGenerationLease)
			entered <- lease
			select {
			case <-releaseProcess:
			case <-ctx.Done():
			}
		})
		return processed, ctx.Err()
	}
	acquireRuntime := func(ctx context.Context) (context.Context, func(), error) {
		lease := &gatewayReviewGenerationLease{released: make(chan struct{})}
		leaseCtx := context.WithValue(ctx, gatewayReviewGenerationContextKey{}, lease)
		return leaseCtx, func() { close(lease.released) }, nil
	}
	service, err := newEventAutomationServiceWithReviews(
		context.Background(),
		cfg,
		nil,
		nil,
		acquireRuntime,
		eventReviewRuntime{reviewProcess: reviewProcess},
	)
	if err != nil {
		t.Fatalf("newEventAutomationServiceWithReviews() error = %v", err)
	}
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseProcess) })
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := service.Close(closeCtx); closeErr != nil {
			t.Errorf("event automation Close() error = %v", closeErr)
		}
	})

	var lease *gatewayReviewGenerationLease
	select {
	case lease = <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("wired review worker did not enter its runtime generation")
	}
	if lease == nil {
		t.Fatal("review worker context has no runtime generation marker")
	}
	select {
	case <-lease.released:
		t.Fatal("runtime generation was released while review was running")
	default:
	}

	releaseOnce.Do(func() { close(releaseProcess) })
	select {
	case <-lease.released:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime generation was not released after review returned")
	}
}

func TestPRDevelopmentRepairAndReviewReadinessAreConjunctive(t *testing.T) {
	if combinedPRDevelopmentAgentReadiness(nil, func(string) bool { return true }) != nil {
		t.Fatal("combined readiness is non-nil without repair readiness")
	}
	if combinedPRDevelopmentAgentReadiness(func(string) bool { return true }, nil) != nil {
		t.Fatal("combined readiness is non-nil without review readiness")
	}

	var repairAgentID, reviewAgentID string
	ready := combinedPRDevelopmentAgentReadiness(
		func(agentID string) bool {
			repairAgentID = agentID
			return agentID == "pinned"
		},
		func(agentID string) bool {
			reviewAgentID = agentID
			return agentID == "pinned"
		},
	)
	if ready == nil || !ready("pinned") || repairAgentID != "pinned" ||
		reviewAgentID != "pinned" {
		t.Fatalf(
			"combined pinned readiness = %t, repair agent = %q, review agent = %q",
			ready != nil && ready("pinned"),
			repairAgentID,
			reviewAgentID,
		)
	}
	if ready("other") {
		t.Fatal("combined readiness accepted a different agent")
	}
}

func TestPRDevelopmentControllerRunsWhenReviewCompositionIsIncomplete(t *testing.T) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		workspace+"/eventing/events.db",
		true,
		false,
	)
	caseID, captured := seedGatewayPRDevelopmentRepair(t, cfg)
	controllerEntered := make(chan struct{})
	var controllerOnce sync.Once
	reviewRuntime := eventReviewRuntime{
		agent:   gatewayReviewContextAgent{},
		agentID: "main",
		repairAgentReady: func(agentID string) bool {
			return agentID == "main"
		},
		repairVerifier: gatewayRepairVerifier{verified: prdevelopment.VerifiedCase{
			CaseID:         caseID,
			HeadRepository: captured.HeadRepository,
			HeadRef:        captured.HeadRef,
			HeadSHA:        captured.HeadSHA,
			HeadCloneURL:   "https://github.com/" + captured.HeadRepository + ".git",
			ReviewDigest:   "sha256:" + strings.Repeat("b", 64),
		}},
		repairRuntime: func(
			string,
			string,
		) (prdevelopment.LocalRepairExecutor, error) {
			return newGatewayBlockingRepairRunner(), nil
		},
		repairWorkspaces: func() (*gitworkspace.Manager, error) { return nil, nil },
		repairLocalCI:    &prDevelopmentLocalCIRuntime{runner: &localci.Runner{}},
		repairControllerProcess: func(ctx context.Context) (bool, error) {
			processed := false
			controllerOnce.Do(func() {
				processed = true
				close(controllerEntered)
				<-ctx.Done()
			})
			return processed, ctx.Err()
		},
	}
	service, err := newEventAutomationServiceWithReviews(
		context.Background(),
		cfg,
		nil,
		nil,
		nil,
		reviewRuntime,
	)
	if err != nil {
		t.Fatalf("newEventAutomationServiceWithReviews() error = %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := service.Close(closeCtx); closeErr != nil {
			t.Errorf("event automation Close() error = %v", closeErr)
		}
	}()

	select {
	case <-controllerEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("controller did not run with incomplete review composition")
	}
	detail, err := service.prDevelopment.Get(context.Background(), caseID)
	if err != nil {
		t.Fatalf("PR development Get() error = %v", err)
	}
	if detail.RepairAvailable || detail.RepairUnavailableReason != "runtime_unavailable" {
		t.Fatalf("repair admission with incomplete review composition = %#v", detail)
	}
}

func TestPRDevelopmentReviewStopsBeforeLocalCIEvidenceCloses(t *testing.T) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		workspace+"/eventing/events.db",
		true,
		false,
	)
	evidence, err := localci.OpenFileEvidenceStore(workspace + "/review-evidence")
	if err != nil {
		t.Fatalf("OpenFileEvidenceStore() error = %v", err)
	}
	digest := strings.Repeat("d", 64)
	entered := make(chan struct{})
	checked := make(chan error, 1)
	var processOnce sync.Once
	reviewProcess := func(ctx context.Context) (bool, error) {
		processed := false
		processOnce.Do(func() {
			processed = true
			close(entered)
			<-ctx.Done()
			_, _, evidenceErr := evidence.GetPlan(context.Background(), digest)
			checked <- evidenceErr
		})
		return processed, ctx.Err()
	}
	service, err := newEventAutomationServiceWithReviews(
		context.Background(),
		cfg,
		nil,
		nil,
		nil,
		eventReviewRuntime{
			repairLocalCI: &prDevelopmentLocalCIRuntime{evidence: evidence},
			reviewProcess: reviewProcess,
		},
	)
	if err != nil {
		_ = evidence.Close()
		t.Fatalf("newEventAutomationServiceWithReviews() error = %v", err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("review process did not start")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = service.Close(closeCtx); err != nil {
		t.Fatalf("event automation Close() error = %v", err)
	}
	select {
	case evidenceErr := <-checked:
		if evidenceErr != nil {
			t.Fatalf("local-CI evidence closed before reviewer joined: %v", evidenceErr)
		}
	default:
		t.Fatal("reviewer was not joined before Close returned")
	}
	if _, _, err = evidence.GetPlan(context.Background(), digest); err == nil {
		t.Fatal("local-CI evidence remained open after reviewer joined")
	}
}

type gatewayReviewContextAgent struct{}

func (gatewayReviewContextAgent) RunAgent(
	context.Context,
	workflows.AgentRequest,
) (map[string]any, error) {
	return map[string]any{"text": "unused"}, nil
}

func TestPRDevelopmentControllerWorkerHoldsGenerationAndPinnedAgentProjection(
	t *testing.T,
) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		workspace+"/eventing/events.db",
		true,
		false,
	)
	caseID, captured := seedGatewayPRDevelopmentRepair(t, cfg)

	msgBus := bus.NewMessageBus()
	provider := &orderedShutdownProvider{closed: make(chan struct{})}
	agentLoop := agent.NewAgentLoop(cfg, msgBus, provider)
	reviewEvidence, err := localci.OpenFileEvidenceStore(workspace + "/review-evidence")
	if err != nil {
		t.Fatalf("OpenFileEvidenceStore() error = %v", err)
	}
	controllerEntered := make(chan struct{})
	controllerRelease := make(chan struct{})
	var controllerEnterOnce sync.Once
	var controllerReleaseOnce sync.Once
	var service *eventAutomationService
	t.Cleanup(func() {
		controllerReleaseOnce.Do(func() { close(controllerRelease) })
		if service != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if closeErr := service.Close(closeCtx); closeErr != nil {
				t.Errorf("event automation Close() error = %v", closeErr)
			}
		} else if closeErr := reviewEvidence.Close(); closeErr != nil {
			t.Errorf("review evidence Close() error = %v", closeErr)
		}
		msgBus.Close()
		agentLoop.Close()
	})

	reviewRuntime := eventReviewRuntime{
		agent:   agent.NewWorkflowAgentRunner(agentLoop),
		agentID: "replacement",
		repairAgentReady: func(agentID string) bool {
			return agentID == "main"
		},
		repairVerifier: gatewayRepairVerifier{
			verified: prdevelopment.VerifiedCase{
				CaseID:         caseID,
				HeadRepository: captured.HeadRepository,
				HeadRef:        captured.HeadRef,
				HeadSHA:        captured.HeadSHA,
				HeadCloneURL: "https://github.com/" +
					captured.HeadRepository + ".git",
				ReviewDigest: "sha256:" + strings.Repeat("b", 64),
			},
		},
		repairRuntime: func(
			agentID string,
			routingText string,
		) (prdevelopment.LocalRepairExecutor, error) {
			return newGatewayBlockingRepairRunner(), nil
		},
		repairWorkspaces: func() (*gitworkspace.Manager, error) {
			return nil, nil
		},
		repairLocalCI: &prDevelopmentLocalCIRuntime{
			runner:   &localci.Runner{},
			evidence: reviewEvidence,
		},
		repairControllerProcess: func(ctx context.Context) (bool, error) {
			processed := false
			controllerEnterOnce.Do(func() {
				processed = true
				close(controllerEntered)
				select {
				case <-controllerRelease:
				case <-ctx.Done():
				}
			})
			return processed, ctx.Err()
		},
		reviewRuntime: func(string) (prdevelopment.LocalReviewExecutor, error) {
			return gatewayLocalReviewRunner{}, nil
		},
		reviewWorkspaces: func() (*gitworkspace.Manager, error) {
			return nil, nil
		},
	}
	acquireRuntime := func(ctx context.Context) (context.Context, func(), error) {
		return agentLoop.AcquireRuntimeGeneration(ctx, cfg)
	}
	service, err = newEventAutomationServiceWithReviews(
		context.Background(),
		cfg,
		nil,
		nil,
		acquireRuntime,
		reviewRuntime,
	)
	if err != nil {
		t.Fatalf("newEventAutomationServiceWithReviews() error = %v", err)
	}

	select {
	case <-controllerEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("wired controller worker did not enter its runtime generation")
	}
	detail, err := service.prDevelopment.Get(context.Background(), caseID)
	if err != nil {
		t.Fatalf("PR development Get() error = %v", err)
	}
	if !detail.RepairAvailable || detail.RepairSession == nil ||
		detail.RepairSession.AgentID != "main" {
		t.Fatalf("repair projection after default-agent change = %#v", detail)
	}

	type pauseResult struct {
		resume func()
		err    error
	}
	pauseResults := make(chan pauseResult, 1)
	pauseCtx, cancelPause := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelPause()
	go func() {
		resume, pauseErr := agentLoop.PauseRuntimeForReload(pauseCtx)
		pauseResults <- pauseResult{resume: resume, err: pauseErr}
	}()
	select {
	case result := <-pauseResults:
		if result.resume != nil {
			result.resume()
		}
		t.Fatalf("runtime reload pause returned while repair was running: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}

	controllerReleaseOnce.Do(func() { close(controllerRelease) })
	var paused pauseResult
	select {
	case paused = <-pauseResults:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime reload pause did not complete after repair returned")
	}
	if paused.err != nil || paused.resume == nil {
		t.Fatalf(
			"PauseRuntimeForReload() has resume = %t, error = %v",
			paused.resume != nil,
			paused.err,
		)
	}
	defer paused.resume()

	workbench, err := service.store.GetPRDevelopmentWorkbench(
		context.Background(),
		caseID,
	)
	if err != nil {
		t.Fatalf("GetPRDevelopmentWorkbench() error = %v", err)
	}
	if workbench.RepairSession == nil || len(workbench.RepairSession.Attempts) != 1 ||
		workbench.RepairSession.AgentID != "main" ||
		workbench.RepairSession.Attempts[0].Status != eventing.PRDevelopmentRepairQueued {
		t.Fatalf("repair workbench = %#v, want queued attempt on pinned main agent", workbench)
	}
}

func TestPRDevelopmentControllerWorkerReconcilesWhenRuntimeUnavailable(t *testing.T) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		workspace+"/eventing/events.db",
		true,
		false,
	)
	caseID, _ := seedGatewayPRDevelopmentRepair(t, cfg)
	service, err := newEventAutomationServiceWithReviews(
		context.Background(),
		cfg,
		nil,
		nil,
		nil,
		eventReviewRuntime{agentID: "main"},
	)
	if err != nil {
		t.Fatalf("newEventAutomationServiceWithReviews() error = %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := service.Close(closeCtx); closeErr != nil {
			t.Errorf("event automation Close() error = %v", closeErr)
		}
	}()

	detail, err := service.prDevelopment.Get(context.Background(), caseID)
	if err != nil {
		t.Fatalf("PR development Get() error = %v", err)
	}
	if detail.RepairAvailable {
		t.Fatal("repair_available = true without verifier/runtime")
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		workbench, loadErr := service.store.GetPRDevelopmentWorkbench(
			context.Background(),
			caseID,
		)
		if loadErr != nil {
			t.Fatalf("GetPRDevelopmentWorkbench() error = %v", loadErr)
		}
		if workbench.RepairSession != nil && len(workbench.RepairSession.Attempts) == 1 {
			attempt := workbench.RepairSession.Attempts[0]
			if attempt.Status == eventing.PRDevelopmentRepairFailed {
				if attempt.ErrorCode != eventing.PRDevelopmentRepairErrorRuntimeUnavailable {
					t.Fatalf("repair error code = %q", attempt.ErrorCode)
				}
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("unavailable repair was not reconciled: %#v", workbench.RepairSession)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type gatewayRepairVerifier struct {
	verified prdevelopment.VerifiedCase
}

type gatewayLocalReviewRunner struct{}

func (gatewayLocalReviewRunner) Run(
	context.Context,
	agent.ControllerLocalReviewRequest,
) (agent.ControllerLocalReviewResult, error) {
	return agent.ControllerLocalReviewResult{
		Outcome: agent.ControllerLocalReviewPassed,
		Summary: "The immutable local candidate passed review.",
	}, nil
}

func (verifier gatewayRepairVerifier) VerifyCase(
	context.Context,
	eventing.PRDevelopmentCase,
	*eventing.PRDevelopmentThreadIdentity,
) (prdevelopment.VerifiedCase, error) {
	return verifier.verified, nil
}

type gatewayBlockingRepairRunner struct {
	entered   chan struct{}
	release   chan struct{}
	enterOnce sync.Once
	mu        sync.Mutex
	request   agent.LocalRepairRequest
}

func newGatewayBlockingRepairRunner() *gatewayBlockingRepairRunner {
	return &gatewayBlockingRepairRunner{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (runner *gatewayBlockingRepairRunner) Run(
	ctx context.Context,
	request agent.LocalRepairRequest,
) (agent.LocalRepairResult, error) {
	runner.mu.Lock()
	runner.request = request
	runner.mu.Unlock()
	runner.enterOnce.Do(func() { close(runner.entered) })
	select {
	case <-runner.release:
		return agent.LocalRepairResult{
			Content:     "Applied the requested local repair.",
			Iterations:  1,
			WorkspaceID: "workspace-opaque",
		}, nil
	case <-ctx.Done():
		return agent.LocalRepairResult{}, ctx.Err()
	}
}

func seedGatewayPRDevelopmentRepair(
	t *testing.T,
	cfg *config.Config,
) (string, eventing.PRDevelopmentCaptureInput) {
	t.Helper()
	ctx := context.Background()
	store, err := eventing.Open(ctx, cfg.Events.Ingress.DatabasePath)
	if err != nil {
		t.Fatalf("eventing.Open() error = %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = store.Close()
		}
	})

	inserted, err := store.Insert(ctx, eventing.Envelope{
		Source:    "github",
		Connector: "github-primary",
		Type:      "pull_request_review.submitted",
		DedupeKey: "gateway-repair-runtime",
		Payload:   json.RawMessage(`{}`),
		Attributes: map[string]string{
			"body_authenticated": "true",
			"target_reason":      "review_feedback",
		},
	})
	if err != nil {
		t.Fatalf("Insert() error = %v", err)
	}
	claimed, err := store.ClaimRouting(ctx, "repair-test-router", 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimRouting() = %#v, %v", claimed, err)
	}
	dispatch, created, err := store.CreateRevisionedDispatchForRoutingClaim(
		ctx,
		inserted.Event.Envelope.ID,
		claimed[0].Routing.LeaseToken,
		"workflows/own-pr-feedback.yml",
		"revision-repair-runtime",
	)
	if err != nil || !created {
		t.Fatalf("CreateRevisionedDispatchForRoutingClaim() = %#v, %v, %v", dispatch, created, err)
	}
	ackErr := store.AckRouting(
		ctx,
		inserted.Event.Envelope.ID,
		claimed[0].Routing.LeaseToken,
	)
	if ackErr != nil {
		t.Fatalf("AckRouting() error = %v", ackErr)
	}

	captured := eventing.PRDevelopmentCaptureInput{
		PRDevelopmentCaptureIdentity: eventing.PRDevelopmentCaptureIdentity{
			EventID:          inserted.Event.Envelope.ID,
			DispatchID:       dispatch.ID,
			RunID:            dispatch.RunID,
			WorkflowRef:      dispatch.WorkflowRef,
			WorkflowRevision: dispatch.WorkflowRevision,
			Connector:        inserted.Event.Envelope.Connector,
		},
		Repository:           "acme/project",
		PullNumber:           42,
		PullURL:              "https://github.com/acme/project/pull/42",
		PullAuthor:           "review-user",
		TargetUser:           "review-user",
		PullState:            eventing.PRDevelopmentPullOpen,
		BaseRepository:       "acme/project",
		BaseRef:              "main",
		BaseSHA:              strings.Repeat("1", 40),
		HeadRepository:       "review-user/project-fork",
		HeadRef:              "repair/retries",
		HeadSHA:              strings.Repeat("2", 40),
		ReviewID:             "501",
		TriggerReviewNodeID:  "PRR_kwDOReview501",
		ReviewAuthor:         "maintainer-1",
		SubmittedReviewState: eventing.PRDevelopmentReviewChangesRequested,
		CurrentReviewState:   eventing.PRDevelopmentReviewChangesRequested,
		ReviewCommitSHA:      strings.Repeat("a", 40),
		ReviewSubmittedAt:    time.Date(2026, time.August, 5, 12, 34, 56, 0, time.UTC),
		ReviewURL: "https://github.com/acme/project/pull/42" +
			"#pullrequestreview-501",
		Feedback: "Please fix the retry race.",
	}
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		eventing.PRDevelopmentCaptureRequest{
			Case: captured,
			Thread: eventing.PRDevelopmentThreadIdentity{
				Provider:       "github",
				ProviderOrigin: "https://github.com",
				PullAuthorID:   "1001",
				RepositoryID:   "2001",
				PullRequestID:  "3001",
				PullNumber:     captured.PullNumber,
			},
		},
	)
	if err != nil || !created {
		t.Fatalf("CapturePRDevelopmentCase() = %#v, %v, %v", developmentCase, created, err)
	}
	_, admitted, err := store.AdmitPRDevelopmentRepair(
		ctx,
		eventing.PRDevelopmentRepairAdmit{
			CaseID:                      developmentCase.ID,
			ExpectedConversationVersion: 0,
			ExpectedRepairVersion:       0,
			IdempotencyKey:              "gateway-repair-runtime-request",
			AgentID:                     "main",
			Instruction:                 "Apply the requested bounded retry fix.",
		},
	)
	if err != nil || !admitted {
		t.Fatalf("AdmitPRDevelopmentRepair() = %v, %v", admitted, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("seed store Close() error = %v", err)
	}
	closed = true
	return developmentCase.ID, captured
}
