package prdevelopment

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/eventing"
)

type repairWorkerQueueFake struct {
	mu        sync.Mutex
	workbench eventing.PRDevelopmentWorkbench
	session   eventing.PRDevelopmentRepairSession
	calls     []string
	begin     eventing.PRDevelopmentRepairBegin
	outcome   eventing.PRDevelopmentRepairOutcome
}

func (queue *repairWorkerQueueFake) GetPRDevelopmentWorkbench(
	context.Context,
	string,
) (eventing.PRDevelopmentWorkbench, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.calls = append(queue.calls, "get")
	return cloneRepairWorkerWorkbench(queue.workbench), nil
}

func (queue *repairWorkerQueueFake) ClaimPRDevelopmentRepair(
	context.Context,
	eventing.PRDevelopmentRepairClaimRequest,
) (eventing.PRDevelopmentRepairSession, bool, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.calls = append(queue.calls, "claim")
	return cloneRepairWorkerSession(queue.session), true, nil
}

func (queue *repairWorkerQueueFake) RenewPRDevelopmentRepairLease(
	context.Context,
	string,
	string,
	time.Duration,
) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.calls = append(queue.calls, "renew")
	return nil
}

func (queue *repairWorkerQueueFake) PinPRDevelopmentRepairSession(
	_ context.Context,
	input eventing.PRDevelopmentRepairPin,
) (eventing.PRDevelopmentRepairSession, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.calls = append(queue.calls, "pin")
	queue.session.Version++
	queue.session.HeadRepository = input.HeadRepository
	queue.session.HeadRef = input.HeadRef
	queue.session.HeadSHA = input.HeadSHA
	queue.session.CloneURL = input.CloneURL
	queue.session.ReviewDigest = input.ReviewDigest
	copySession := cloneRepairWorkerSession(queue.session)
	queue.workbench.RepairSession = &copySession
	return cloneRepairWorkerSession(queue.session), nil
}

func (queue *repairWorkerQueueFake) BeginPRDevelopmentRepair(
	_ context.Context,
	input eventing.PRDevelopmentRepairBegin,
) (eventing.PRDevelopmentRepairSession, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.calls = append(queue.calls, "begin")
	queue.begin = input
	queue.session.Version++
	queue.session.Attempts[0].Status = eventing.PRDevelopmentRepairRunning
	copySession := cloneRepairWorkerSession(queue.session)
	queue.workbench.RepairSession = &copySession
	return cloneRepairWorkerSession(queue.session), nil
}

func (queue *repairWorkerQueueFake) FinishPRDevelopmentRepair(
	_ context.Context,
	input eventing.PRDevelopmentRepairOutcome,
) (eventing.PRDevelopmentRepairSession, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.calls = append(queue.calls, "finish")
	queue.outcome = input
	return cloneRepairWorkerSession(queue.session), nil
}

type repairWorkerVerifierFake struct {
	result VerifiedCase
	err    error
	calls  int
}

type repairWorkerCancelVerifier struct {
	entered chan struct{}
}

func (verifier *repairWorkerCancelVerifier) VerifyCase(
	ctx context.Context,
	_ eventing.PRDevelopmentCase,
) (VerifiedCase, error) {
	close(verifier.entered)
	<-ctx.Done()
	return VerifiedCase{}, ctx.Err()
}

func (verifier *repairWorkerVerifierFake) VerifyCase(
	context.Context,
	eventing.PRDevelopmentCase,
) (VerifiedCase, error) {
	verifier.calls++
	return verifier.result, verifier.err
}

type repairWorkerRunnerFake struct {
	result  agent.LocalRepairResult
	err     error
	request agent.LocalRepairRequest
	calls   int
}

func (runner *repairWorkerRunnerFake) Run(
	_ context.Context,
	request agent.LocalRepairRequest,
) (agent.LocalRepairResult, error) {
	runner.calls++
	runner.request = request
	return runner.result, runner.err
}

func TestRepairWorkerPinsBeginsAndCompletesExactAttempt(t *testing.T) {
	queue := newRepairWorkerQueueFake()
	verifier := &repairWorkerVerifierFake{result: repairWorkerVerifiedCase()}
	runner := &repairWorkerRunnerFake{result: agent.LocalRepairResult{
		Content: "Updated retry handling in the local checkout.", Iterations: 3,
		WorkspaceID: "workspace-opaque",
	}}
	worker := &RepairWorker{
		Queue:         queue,
		Verifier:      verifier,
		LeaseDuration: 2 * time.Minute,
		Runtime: func(agentID, routingText string) (LocalRepairExecutor, error) {
			if agentID != "main" || routingText != "Address the review feedback." {
				t.Fatalf("runtime request = (%q, %q)", agentID, routingText)
			}
			return runner, nil
		},
	}
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%v, %v)", processed, err)
	}
	if verifier.calls != 1 || runner.calls != 1 {
		t.Fatalf("verifier/runner calls = %d/%d", verifier.calls, runner.calls)
	}
	if runner.request.Pin.Repository != "https://github.com/octo/fork.git" ||
		runner.request.Pin.SourceRef != "feature" ||
		runner.request.Pin.ExpectedCommit != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
		runner.request.Pin.ReservationKey != "pdrk_secret" ||
		runner.request.Pin.AgentID != "main" ||
		runner.request.Instruction != "Address the review feedback." ||
		runner.request.Context == "" {
		t.Fatalf("runner request = %#v", runner.request)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if !reflect.DeepEqual(queue.calls, []string{"claim", "get", "pin", "begin", "finish"}) {
		t.Fatalf("queue calls = %#v", queue.calls)
	}
	if queue.begin.AttemptID != queue.session.Attempts[0].ID ||
		queue.begin.LeaseToken != queue.session.Attempts[0].LeaseToken ||
		queue.begin.Lease != 2*time.Minute {
		t.Fatalf("begin request = %#v", queue.begin)
	}
	if queue.outcome.Status != eventing.PRDevelopmentRepairCompleted ||
		queue.outcome.Summary != runner.result.Content ||
		queue.outcome.ErrorCode != "" || queue.outcome.Iterations != 3 ||
		queue.outcome.WorkspaceID != "workspace-opaque" {
		t.Fatalf("outcome = %#v", queue.outcome)
	}
}

func TestRepairWorkerProviderDriftFailsBeforeRuntime(t *testing.T) {
	queue := newRepairWorkerQueueFake()
	verifier := &repairWorkerVerifierFake{err: ErrGitHubCaseDrift}
	runtimeCalls := 0
	worker := &RepairWorker{
		Queue: queue, Verifier: verifier,
		Runtime: func(string, string) (LocalRepairExecutor, error) {
			runtimeCalls++
			return nil, nil
		},
	}
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%v, %v)", processed, err)
	}
	if runtimeCalls != 0 {
		t.Fatalf("runtime calls = %d, want zero", runtimeCalls)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.outcome.Status != eventing.PRDevelopmentRepairFailed ||
		queue.outcome.ErrorCode != eventing.PRDevelopmentRepairErrorProviderChanged ||
		queue.outcome.Summary == "" {
		t.Fatalf("outcome = %#v", queue.outcome)
	}
	if !reflect.DeepEqual(queue.calls, []string{"claim", "get", "finish"}) {
		t.Fatalf("queue calls = %#v", queue.calls)
	}
}

func TestRepairWorkerRunnerFailureRequiresExplicitRecovery(t *testing.T) {
	queue := newRepairWorkerQueueFake()
	verifier := &repairWorkerVerifierFake{result: repairWorkerVerifiedCase()}
	runner := &repairWorkerRunnerFake{
		result: agent.LocalRepairResult{Iterations: 2, WorkspaceID: "workspace-opaque"},
		err:    errors.New("provider failed after a possible edit"),
	}
	worker := &RepairWorker{
		Queue: queue, Verifier: verifier,
		Runtime: func(string, string) (LocalRepairExecutor, error) { return runner, nil },
	}
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%v, %v)", processed, err)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.outcome.Status != eventing.PRDevelopmentRepairRecoveryRequired ||
		queue.outcome.ErrorCode != eventing.PRDevelopmentRepairErrorRecoveryRequired ||
		queue.outcome.Iterations != 2 || queue.outcome.WorkspaceID != "workspace-opaque" ||
		queue.outcome.InternalError == "" {
		t.Fatalf("outcome = %#v", queue.outcome)
	}
}

func TestRepairWorkerCompletedResultRequiresWorkspaceFence(t *testing.T) {
	t.Parallel()

	queue := newRepairWorkerQueueFake()
	worker := &RepairWorker{
		Queue:    queue,
		Verifier: &repairWorkerVerifierFake{result: repairWorkerVerifiedCase()},
		Runtime: func(string, string) (LocalRepairExecutor, error) {
			return &repairWorkerRunnerFake{result: agent.LocalRepairResult{
				Content: "Applied edits but lost the workspace fence.", Iterations: 1,
			}}, nil
		},
	}
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%v, %v)", processed, err)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.outcome.Status != eventing.PRDevelopmentRepairRecoveryRequired ||
		queue.outcome.ErrorCode != eventing.PRDevelopmentRepairErrorRecoveryRequired ||
		queue.outcome.WorkspaceID != "" || queue.outcome.InternalError == "" {
		t.Fatalf("outcome = %#v", queue.outcome)
	}
}

func TestRepairWorkerBoundsPublicSummaryToDurableLimit(t *testing.T) {
	t.Parallel()

	queue := newRepairWorkerQueueFake()
	worker := &RepairWorker{
		Queue:    queue,
		Verifier: &repairWorkerVerifierFake{result: repairWorkerVerifiedCase()},
		Runtime: func(string, string) (LocalRepairExecutor, error) {
			return &repairWorkerRunnerFake{result: agent.LocalRepairResult{
				Content:     strings.Repeat("a", eventing.MaxPRDevelopmentRepairSummaryBytes+1),
				Iterations:  1,
				WorkspaceID: "workspace-opaque",
			}}, nil
		},
	}
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%v, %v)", processed, err)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.outcome.Summary) != eventing.MaxPRDevelopmentRepairSummaryBytes {
		t.Fatalf(
			"summary bytes = %d, want %d",
			len(queue.outcome.Summary),
			eventing.MaxPRDevelopmentRepairSummaryBytes,
		)
	}
}

func TestRepairWorkerUnavailableDependenciesTerminalizeBeforeVerification(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		verifier RepairCaseVerifier
		runtime  RepairRuntimeFactory
	}{
		{
			name: "verifier",
			runtime: func(string, string) (LocalRepairExecutor, error) {
				return &repairWorkerRunnerFake{}, nil
			},
		},
		{
			name:     "runtime",
			verifier: &repairWorkerVerifierFake{result: repairWorkerVerifiedCase()},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			queue := newRepairWorkerQueueFake()
			worker := &RepairWorker{
				Queue: queue, Verifier: test.verifier, Runtime: test.runtime,
			}
			processed, err := worker.ProcessOne(context.Background())
			if err != nil || !processed {
				t.Fatalf("ProcessOne() = (%v, %v)", processed, err)
			}
			queue.mu.Lock()
			defer queue.mu.Unlock()
			if !reflect.DeepEqual(queue.calls, []string{"claim", "get", "finish"}) {
				t.Fatalf("queue calls = %#v", queue.calls)
			}
			if queue.outcome.Status != eventing.PRDevelopmentRepairFailed ||
				queue.outcome.ErrorCode != eventing.PRDevelopmentRepairErrorRuntimeUnavailable ||
				queue.outcome.Summary == "" {
				t.Fatalf("outcome = %#v", queue.outcome)
			}
		})
	}
}

func TestRepairWorkerParentCancellationDuringVerificationLeavesPreparation(t *testing.T) {
	t.Parallel()

	queue := newRepairWorkerQueueFake()
	verifier := &repairWorkerCancelVerifier{entered: make(chan struct{})}
	worker := &RepairWorker{
		Queue:    queue,
		Verifier: verifier,
		Runtime: func(string, string) (LocalRepairExecutor, error) {
			return &repairWorkerRunnerFake{}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	type processResult struct {
		processed bool
		err       error
	}
	results := make(chan processResult, 1)
	go func() {
		processed, err := worker.ProcessOne(ctx)
		results <- processResult{processed: processed, err: err}
	}()
	select {
	case <-verifier.entered:
	case <-time.After(time.Second):
		t.Fatal("repair verifier was not entered")
	}
	cancel()
	select {
	case result := <-results:
		if !result.processed || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("ProcessOne() = (%v, %v), want processed cancellation", result.processed, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("ProcessOne() did not return after parent cancellation")
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if !reflect.DeepEqual(queue.calls, []string{"claim", "get"}) {
		t.Fatalf("queue calls = %#v, want preparing lease left for reclaim", queue.calls)
	}
}

type repairWorkerCanceledRenewQueue struct {
	*repairWorkerQueueFake
	entered chan struct{}
}

func (queue *repairWorkerCanceledRenewQueue) RenewPRDevelopmentRepairLease(
	ctx context.Context,
	_, _ string,
	_ time.Duration,
) error {
	close(queue.entered)
	<-ctx.Done()
	return ctx.Err()
}

type repairWorkerStaleRenewQueue struct {
	*repairWorkerQueueFake
}

func (*repairWorkerStaleRenewQueue) RenewPRDevelopmentRepairLease(
	context.Context,
	string,
	string,
	time.Duration,
) error {
	return eventing.ErrStaleLease
}

func TestRepairWorkerIntentionalHeartbeatStopIgnoresCanceledRenewal(t *testing.T) {
	t.Parallel()

	queue := &repairWorkerCanceledRenewQueue{
		repairWorkerQueueFake: newRepairWorkerQueueFake(),
		entered:               make(chan struct{}),
	}
	worker := &RepairWorker{Queue: queue}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	errs := make(chan error, 1)
	go worker.renewLease(ctx, done, errs, "attempt", "token", time.Second, cancel)
	select {
	case <-queue.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("lease renewal was not entered")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lease renewal did not stop after cancellation")
	}
	select {
	case renewErr := <-errs:
		t.Fatalf("canceled renewal published a lease failure: %v", renewErr)
	default:
	}
}

func TestRepairWorkerHeartbeatPublishesLiveStaleLease(t *testing.T) {
	t.Parallel()

	queue := &repairWorkerStaleRenewQueue{
		repairWorkerQueueFake: newRepairWorkerQueueFake(),
	}
	worker := &RepairWorker{Queue: queue}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	errs := make(chan error, 1)
	go worker.renewLease(ctx, done, errs, "attempt", "token", time.Second, cancel)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stale lease renewal did not stop")
	}
	select {
	case renewErr := <-errs:
		if !errors.Is(renewErr, eventing.ErrStaleLease) {
			t.Fatalf("renewal error = %v, want stale lease", renewErr)
		}
	default:
		t.Fatal("stale lease renewal did not publish its failure")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("renewal context error = %v, want cancellation", ctx.Err())
	}
}

func newRepairWorkerQueueFake() *repairWorkerQueueFake {
	now := time.Now().UTC()
	leaseUntil := now.Add(time.Minute)
	attempt := eventing.PRDevelopmentRepairAttempt{
		ID:                  "pdr_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SessionID:           "pds_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Ordinal:             0,
		ConversationVersion: 0,
		Instruction:         "Address the review feedback.",
		Status:              eventing.PRDevelopmentRepairPreparing,
		LeaseOwner:          "worker",
		LeaseToken:          "lease-token",
		LeaseUntil:          &leaseUntil,
		Claims:              1,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	session := eventing.PRDevelopmentRepairSession{
		ID:             attempt.SessionID,
		CaseID:         "pdc_cccccccccccccccccccccccccccccccc",
		Version:        2,
		AgentID:        "main",
		ReservationKey: "pdrk_secret",
		Attempts:       []eventing.PRDevelopmentRepairAttempt{attempt},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	copySession := cloneRepairWorkerSession(session)
	return &repairWorkerQueueFake{
		session: session,
		workbench: eventing.PRDevelopmentWorkbench{
			Case: eventing.PRDevelopmentCase{ID: session.CaseID},
			Conversation: eventing.PRDevelopmentConversation{
				CaseID: session.CaseID, Messages: []eventing.PRDevelopmentMessage{},
			},
			RepairSession: &copySession,
		},
	}
}

func repairWorkerVerifiedCase() VerifiedCase {
	return VerifiedCase{
		CaseID:         "pdc_cccccccccccccccccccccccccccccccc",
		HeadRepository: "octo/fork",
		HeadRef:        "feature",
		HeadSHA:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		HeadCloneURL:   "https://github.com/octo/fork.git",
		ReviewDigest:   "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
}

func cloneRepairWorkerSession(
	session eventing.PRDevelopmentRepairSession,
) eventing.PRDevelopmentRepairSession {
	session.Attempts = append([]eventing.PRDevelopmentRepairAttempt(nil), session.Attempts...)
	return session
}

func cloneRepairWorkerWorkbench(
	workbench eventing.PRDevelopmentWorkbench,
) eventing.PRDevelopmentWorkbench {
	workbench.Conversation.Messages = append(
		[]eventing.PRDevelopmentMessage(nil),
		workbench.Conversation.Messages...,
	)
	if workbench.RepairSession != nil {
		session := cloneRepairWorkerSession(*workbench.RepairSession)
		workbench.RepairSession = &session
	}
	return workbench
}
