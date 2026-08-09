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
	"github.com/sipeed/picoclaw/pkg/prdevelopment"
)

func TestPRDevelopmentRepairWorkerUsesPinnedAgentAfterDefaultChanges(
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
	runner := newGatewayBlockingRepairRunner()
	var service *eventAutomationService
	t.Cleanup(func() {
		runner.unblock()
		if service != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := service.Close(closeCtx); err != nil {
				t.Errorf("event automation Close() error = %v", err)
			}
		}
		msgBus.Close()
		agentLoop.Close()
	})

	runtimeArgs := make(chan [2]string, 1)
	reviewRuntime := eventReviewRuntime{
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
			runtimeArgs <- [2]string{agentID, routingText}
			return runner, nil
		},
	}
	acquireRuntime := func(ctx context.Context) (context.Context, func(), error) {
		return agentLoop.AcquireRuntimeGeneration(ctx, cfg)
	}
	var err error
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
	case <-runner.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("wired repair worker did not invoke the local repair runner")
	}
	select {
	case got := <-runtimeArgs:
		if got != [2]string{"main", "Apply the requested bounded retry fix."} {
			t.Fatalf("repair runtime args = %#v", got)
		}
	default:
		t.Fatal("repair runtime factory was not called")
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

	runner.unblock()
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
		workbench.RepairSession.Attempts[0].Status != eventing.PRDevelopmentRepairCompleted {
		t.Fatalf("repair workbench = %#v, want one completed attempt", workbench)
	}
	request := runner.snapshotRequest()
	if request.Pin.AgentID != "main" || request.Pin.Repository == "" ||
		request.Instruction != "Apply the requested bounded retry fix." {
		t.Fatalf("local repair request = %#v", request)
	}
}

func TestPRDevelopmentRepairWorkerReconcilesWhenRuntimeUnavailable(t *testing.T) {
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

func (verifier gatewayRepairVerifier) VerifyCase(
	context.Context,
	eventing.PRDevelopmentCase,
	*eventing.PRDevelopmentThreadIdentity,
) (prdevelopment.VerifiedCase, error) {
	return verifier.verified, nil
}

type gatewayBlockingRepairRunner struct {
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
	mu          sync.Mutex
	request     agent.LocalRepairRequest
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

func (runner *gatewayBlockingRepairRunner) unblock() {
	runner.releaseOnce.Do(func() { close(runner.release) })
}

func (runner *gatewayBlockingRepairRunner) snapshotRequest() agent.LocalRepairRequest {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.request
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
