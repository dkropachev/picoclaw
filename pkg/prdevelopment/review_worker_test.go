package prdevelopment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prdevelopment/localci"
)

const (
	reviewWorkerCaseID       = "pdc_11111111111111111111111111111111"
	reviewWorkerThreadID     = "pdt_22222222222222222222222222222222"
	reviewWorkerSessionID    = "pds_33333333333333333333333333333333"
	reviewWorkerAttemptID    = "pdr_44444444444444444444444444444444"
	reviewWorkerControllerID = "pctl_55555555555555555555555555555555"
	reviewWorkerLineID       = "pdln_66666666666666666666666666666666"
	reviewWorkerNextAttempt  = "pdr_77777777777777777777777777777777"
	reviewWorkerLeaseToken   = "review-worker:lease-token"
	reviewWorkerHead         = "1111111111111111111111111111111111111111"
	reviewWorkerSourceTree   = "2222222222222222222222222222222222222222"
	reviewWorkerCandidate    = "3333333333333333333333333333333333333333"
	reviewWorkerCommit       = "4444444444444444444444444444444444444444"
)

func reviewWorkerDigest(character string) string {
	return strings.Repeat(character, 64)
}

type reviewWorkerStoreFake struct {
	mu sync.Mutex

	claimed       bool
	claim         eventing.PRDevelopmentReviewLease
	workbench     eventing.PRDevelopmentWorkbench
	orchestration eventing.PRDevelopmentRepairOrchestration

	claimCalls    int
	claimInputs   []eventing.PRDevelopmentReviewClaimRequest
	renewCalls    []eventing.PRDevelopmentControllerRenew
	releaseCalls  []eventing.PRDevelopmentControllerReviewTransition
	releaseCtxErr []error
	completeCalls []eventing.PRDevelopmentLedgerReviewAppend

	claimErr     error
	workbenchErr error
	runErr       error
	renewErr     error
	releaseErr   error
	completeErrs []error

	renewStarted chan struct{}
	renewAllow   chan struct{}
	renewOnce    sync.Once
	completeHit  chan struct{}
	completeOnce sync.Once
}

func (store *reviewWorkerStoreFake) ClaimPRDevelopmentReview(
	_ context.Context,
	input eventing.PRDevelopmentReviewClaimRequest,
) (eventing.PRDevelopmentReviewLease, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.claimCalls++
	store.claimInputs = append(store.claimInputs, input)
	return store.claim, store.claimed, store.claimErr
}

func (store *reviewWorkerStoreFake) CompletePRDevelopmentReview(
	_ context.Context,
	input eventing.PRDevelopmentLedgerReviewAppend,
) (eventing.PRDevelopmentReviewCompletion, bool, error) {
	store.mu.Lock()
	store.completeCalls = append(store.completeCalls, cloneReviewAppend(input))
	var completeErr error
	if len(store.completeErrs) != 0 {
		completeErr = store.completeErrs[0]
		store.completeErrs = store.completeErrs[1:]
	}
	store.mu.Unlock()
	if store.completeHit != nil {
		store.completeOnce.Do(func() { close(store.completeHit) })
	}
	if completeErr != nil {
		return eventing.PRDevelopmentReviewCompletion{}, false, completeErr
	}
	entry := eventing.PRDevelopmentLedgerEntry{
		ID:            "pdle_88888888888888888888888888888888",
		ThreadID:      reviewWorkerThreadID,
		Ordinal:       store.claim.Fence.Ordinal*2 + 1,
		Kind:          eventing.PRDevelopmentLedgerReview,
		AttemptID:     input.AttemptID,
		FenceOrdinal:  store.claim.Fence.Ordinal,
		CaseID:        input.CaseID,
		Summary:       input.Summary,
		ReviewOutcome: input.Outcome,
		Findings:      cloneReviewFindings(input.Findings),
		FenceHash:     reviewWorkerDigest("d"),
		CreatedAt:     time.Now().UTC(),
	}
	controller := store.claim.Controller
	controller.Revision = input.ExpectedRevision + 1
	controller.Phase = eventing.PRDevelopmentControllerReady
	controller.LeaseKind = ""
	controller.LeaseOwner = ""
	controller.LeaseToken = ""
	controller.LeaseUntil = nil
	controller.FencesDigest = entry.FenceHash
	completion := eventing.PRDevelopmentReviewCompletion{
		Entry: entry, Controller: controller,
	}
	if input.Outcome == eventing.PRDevelopmentLedgerReviewChangesRequired {
		completion.NextAttempt = &eventing.PRDevelopmentRepairAttempt{
			ID:                    reviewWorkerNextAttempt,
			SessionID:             reviewWorkerSessionID,
			Ordinal:               1,
			ExpectedRepairVersion: store.workbench.RepairSession.Version,
			ConversationVersion:   store.workbench.Conversation.Version,
			IdempotencyKey:        "ai-review-changes:" + reviewWorkerAttemptID,
			Instruction: "Address the actionable findings from the latest completed " +
				"local AI review.",
			Status: eventing.PRDevelopmentRepairQueued,
		}
	}
	return completion, true, nil
}

func (store *reviewWorkerStoreFake) GetPRDevelopmentWorkbench(
	context.Context,
	string,
) (eventing.PRDevelopmentWorkbench, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.workbench, store.workbenchErr
}

func (store *reviewWorkerStoreFake) GetPRDevelopmentRepairOrchestration(
	context.Context,
	string,
) (eventing.PRDevelopmentRepairOrchestration, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.orchestration, store.runErr
}

func (store *reviewWorkerStoreFake) RenewPRDevelopmentControllerLease(
	ctx context.Context,
	input eventing.PRDevelopmentControllerRenew,
) error {
	store.mu.Lock()
	store.renewCalls = append(store.renewCalls, input)
	err := store.renewErr
	started := store.renewStarted
	allow := store.renewAllow
	store.mu.Unlock()
	if started != nil {
		store.renewOnce.Do(func() { close(started) })
	}
	if allow != nil {
		select {
		case <-allow:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (store *reviewWorkerStoreFake) ReleasePRDevelopmentControllerReview(
	ctx context.Context,
	input eventing.PRDevelopmentControllerReviewTransition,
) (eventing.PRDevelopmentController, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.releaseCalls = append(store.releaseCalls, input)
	store.releaseCtxErr = append(store.releaseCtxErr, ctx.Err())
	controller := store.claim.Controller
	controller.Phase = eventing.PRDevelopmentControllerReviewPending
	controller.Revision++
	controller.LeaseKind = ""
	controller.LeaseOwner = ""
	controller.LeaseToken = ""
	controller.LeaseUntil = nil
	return controller, store.releaseErr
}

func cloneReviewAppend(
	input eventing.PRDevelopmentLedgerReviewAppend,
) eventing.PRDevelopmentLedgerReviewAppend {
	input.Findings = cloneReviewFindings(input.Findings)
	return input
}

func cloneReviewFindings(
	findings []eventing.PRDevelopmentLedgerReviewFinding,
) []eventing.PRDevelopmentLedgerReviewFinding {
	cloned := make([]eventing.PRDevelopmentLedgerReviewFinding, len(findings))
	copy(cloned, findings)
	for index := range cloned {
		if cloned[index].Line != nil {
			line := *cloned[index].Line
			cloned[index].Line = &line
		}
	}
	return cloned
}

type reviewEvidenceFake struct {
	plan             localci.Plan
	execution        localci.Execution
	attestation      localci.Attestation
	planFound        bool
	executionFound   bool
	attestationFound bool
	err              error
}

func (evidence *reviewEvidenceFake) GetPlan(
	context.Context,
	string,
) (localci.Plan, bool, error) {
	return evidence.plan, evidence.planFound, evidence.err
}

func (evidence *reviewEvidenceFake) GetExecution(
	context.Context,
	string,
) (localci.Execution, bool, error) {
	return evidence.execution, evidence.executionFound, evidence.err
}

func (evidence *reviewEvidenceFake) GetAttestation(
	context.Context,
	string,
) (localci.Attestation, bool, error) {
	return evidence.attestation, evidence.attestationFound, evidence.err
}

type reviewContextLoaderFake struct {
	text                string
	err                 error
	caseID              string
	conversationVersion int64
}

func (loader *reviewContextLoaderFake) Load(
	_ context.Context,
	caseID string,
	conversationVersion int64,
) (string, error) {
	loader.caseID = caseID
	loader.conversationVersion = conversationVersion
	return loader.text, loader.err
}

type reviewWorkspaceFake struct {
	snapshot gitworkspace.PinnedLineReviewSnapshot
	err      error
	requests []gitworkspace.PinnedLineReviewRequest
}

func (workspace *reviewWorkspaceFake) SnapshotPinnedLineReview(
	_ context.Context,
	request gitworkspace.PinnedLineReviewRequest,
) (gitworkspace.PinnedLineReviewSnapshot, error) {
	workspace.requests = append(workspace.requests, request)
	return workspace.snapshot, workspace.err
}

type reviewExecutorFake struct {
	result agent.ControllerLocalReviewResult
	err    error
	run    func(context.Context, agent.ControllerLocalReviewRequest) (
		agent.ControllerLocalReviewResult,
		error,
	)
	requests []agent.ControllerLocalReviewRequest
}

func (executor *reviewExecutorFake) Run(
	ctx context.Context,
	request agent.ControllerLocalReviewRequest,
) (agent.ControllerLocalReviewResult, error) {
	executor.requests = append(executor.requests, request)
	if executor.run != nil {
		return executor.run(ctx, request)
	}
	return executor.result, executor.err
}

type reviewWorkerFixture struct {
	store          *reviewWorkerStoreFake
	evidence       *reviewEvidenceFake
	context        *reviewContextLoaderFake
	workspace      *reviewWorkspaceFake
	executor       *reviewExecutorFake
	contextAgents  []string
	runtimeAgents  []string
	workspaceError error
	worker         *ReviewWorker
}

func newReviewWorkerFixture(t *testing.T) *reviewWorkerFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	leaseUntil := now.Add(time.Hour)
	identities, err := newControllerAttemptIdentities(reviewWorkerAttemptID)
	if err != nil {
		t.Fatalf("newControllerAttemptIdentities() error = %v", err)
	}
	attempt := eventing.PRDevelopmentRepairAttempt{
		ID: reviewWorkerAttemptID, SessionID: reviewWorkerSessionID, Ordinal: 0,
		ConversationVersion: 0, Instruction: "address the submitted review",
		Status: eventing.PRDevelopmentRepairCompleted, Claims: 1,
		Summary: "implemented the requested change", Iterations: 2,
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
	}
	session := eventing.PRDevelopmentRepairSession{
		ID: reviewWorkerSessionID, CaseID: reviewWorkerCaseID, Version: 2,
		AgentID: "pinned-agent", HeadRepository: "owner/repo", HeadRef: "feature",
		HeadSHA: reviewWorkerHead, CloneURL: "https://github.com/owner/repo.git",
		ReviewDigest: reviewWorkerDigest("1"), WorkspaceID: "workspace-1",
		Attempts:  []eventing.PRDevelopmentRepairAttempt{attempt},
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
	}
	fence := eventing.PRDevelopmentAttemptReviewFence{
		AttemptID: reviewWorkerAttemptID, ControllerID: reviewWorkerControllerID,
		ThreadID: reviewWorkerThreadID, LineID: reviewWorkerLineID, Ordinal: 0,
		LineVersion: 1, MutationEpoch: 1, ParkIntentID: identities.ParkIntent,
		BaseCommit: reviewWorkerHead, TipCommit: reviewWorkerCommit,
		Tree: reviewWorkerCandidate, LineReviewDigest: reviewWorkerDigest("2"),
		MutationReservationDigest: reviewWorkerDigest("3"), MutationLeaseEpoch: 1,
		MutationLeaseTokenDigest:   reviewWorkerDigest("4"),
		MutationControllerRevision: 3, PreviousHash: reviewWorkerDigest("5"),
		FenceHash: reviewWorkerDigest("6"), CreatedAt: now,
	}
	controller := eventing.PRDevelopmentController{
		ID: reviewWorkerControllerID, ThreadID: reviewWorkerThreadID,
		OwnerSessionID: reviewWorkerSessionID, AgentID: session.AgentID,
		Revision: 3, Phase: eventing.PRDevelopmentControllerReview,
		LineID: reviewWorkerLineID, WorkspaceID: session.WorkspaceID,
		SourceCloneURL: session.CloneURL, SourceRef: session.HeadRef,
		SourceCommit: session.HeadSHA, SourceTree: reviewWorkerSourceTree,
		LineVersion: 1, MutationEpoch: 1, TipCommit: reviewWorkerCommit,
		Tree: reviewWorkerCandidate, CurrentAttemptID: attempt.ID,
		LeaseKind:  eventing.PRDevelopmentControllerReviewLease,
		LeaseOwner: "review-worker", LeaseToken: reviewWorkerLeaseToken,
		LeaseUntil: &leaseUntil, LeaseEpoch: 2, Claims: 1,
		FenceCount: 1, FencesDigest: fence.FenceHash, CreatedAt: now, UpdatedAt: now,
	}
	receipt := eventing.PRDevelopmentRepairValidationReceipt{
		ControllerID: reviewWorkerControllerID, WorkspaceID: session.WorkspaceID,
		ModelControllerRevision: 2, ModelLineID: reviewWorkerLineID,
		ModelLineVersion: 0, ModelMutationEpoch: 1, ModelMutationLeaseEpoch: 1,
		ModelLeaseTokenDigest:  reviewWorkerDigest("4"),
		ModelReservationDigest: reviewWorkerDigest("3"),
		ContextDigest:          reviewWorkerDigest("7"), PromptDigest: reviewWorkerDigest("8"),
		LineID: reviewWorkerLineID, ControllerRevision: 3, LineVersion: 0,
		MutationEpoch: 1, MutationLeaseEpoch: 1,
		MutationLeaseTokenDigest:  fence.MutationLeaseTokenDigest,
		MutationReservationDigest: fence.MutationReservationDigest,
		ParentCommit:              reviewWorkerHead, ParentTree: reviewWorkerSourceTree,
		CandidateTree: reviewWorkerCandidate, CandidateDigest: reviewWorkerDigest("a"),
		ChangedFiles: 1, CIStatus: eventing.PRDevelopmentCIPassed,
		CIAttestationID:       identities.CIAttestation,
		CIAttestationDigest:   reviewWorkerDigest("0"),
		CIResultKey:           reviewWorkerDigest("f"),
		CIEffectivePlanDigest: reviewWorkerDigest("c"),
		CIExecutionDigest:     reviewWorkerDigest("e"),
		ModelResultDigest:     reviewWorkerDigest("9"),
		ModelSummary:          attempt.Summary, ModelIterations: attempt.Iterations,
		ReceiptHash: reviewWorkerDigest("b"), CreatedAt: now,
	}
	orchestration := eventing.PRDevelopmentRepairOrchestration{
		AttemptID: attempt.ID, SessionID: session.ID, CaseID: session.CaseID,
		ThreadID: reviewWorkerThreadID, AgentID: session.AgentID,
		Instruction:    attempt.Instruction,
		Phase:          eventing.PRDevelopmentRepairOrchestrationCompleted,
		HeadRepository: session.HeadRepository, HeadRef: session.HeadRef,
		HeadSHA: session.HeadSHA, CloneURL: session.CloneURL,
		ReviewDigest: session.ReviewDigest, WorkspaceID: session.WorkspaceID,
		SourceTree: reviewWorkerSourceTree, ControllerID: controller.ID,
		ModelControllerRevision: receipt.ModelControllerRevision,
		ModelLineID:             receipt.ModelLineID, ModelLineVersion: receipt.ModelLineVersion,
		ModelMutationEpoch:      receipt.ModelMutationEpoch,
		ModelMutationLeaseEpoch: receipt.ModelMutationLeaseEpoch,
		ModelLeaseTokenDigest:   receipt.ModelLeaseTokenDigest,
		ModelReservationDigest:  receipt.ModelReservationDigest,
		ContextDigest:           receipt.ContextDigest, PromptDigest: receipt.PromptDigest,
		ModelResultDigest: receipt.ModelResultDigest, Summary: attempt.Summary,
		Iterations: attempt.Iterations, Validation: &receipt,
		ParkOperationID: identities.ParkOperation,
		LedgerEntryID:   "pdle_99999999999999999999999999999999",
		FenceHash:       fence.FenceHash, ValidatedAt: &now, CompletedAt: &now,
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
	}
	conversation := eventing.PRDevelopmentConversation{CaseID: reviewWorkerCaseID}
	store := &reviewWorkerStoreFake{
		claimed: true,
		claim: eventing.PRDevelopmentReviewLease{
			CaseID: reviewWorkerCaseID, Controller: controller, Fence: fence,
		},
		workbench: eventing.PRDevelopmentWorkbench{
			Case: eventing.PRDevelopmentCase{ID: reviewWorkerCaseID},
			Thread: &eventing.PRDevelopmentThreadBinding{
				ID: reviewWorkerThreadID, Kind: eventing.PRDevelopmentThreadProvider,
			},
			Conversation: conversation, RepairSession: &session,
		},
		orchestration: orchestration,
	}
	plan := localci.Plan{
		Version: localci.EvidenceVersion, DiscoveryVersion: localci.DiscoveryVersion,
		DependencyDigest: reviewWorkerDigest("d"), Digest: receipt.CIEffectivePlanDigest,
		Complete: true,
		Steps: []localci.Step{{
			ID: "go-test", Name: "targeted tests", Kind: localci.StepTest,
			Origin: localci.OriginMake, Source: "Makefile", Required: true,
		}},
	}
	execution := localci.Execution{
		Version: localci.EvidenceVersion, Digest: receipt.CIExecutionDigest,
		ResultKey: receipt.CIResultKey,
		Evidence: localci.CandidateEvidence{
			Repository:   "https://github.com/owner/repo",
			ParentCommit: receipt.ParentCommit, Tree: receipt.CandidateTree,
			CandidateDigest:  receipt.CandidateDigest,
			DependencyDigest: plan.DependencyDigest, PlanDigest: plan.Digest,
		},
		Status: localci.StatusPassed,
		Steps: []localci.StepResult{{
			StepID: "go-test", Status: localci.StatusPassed, ExitCode: 0,
			Output: "ok", OutputDigest: reviewWorkerDigest("1"),
		}},
		StartedAt: now.Add(-time.Second), CompletedAt: now,
	}
	attestation := localci.Attestation{
		Version: localci.EvidenceVersion, ID: identities.CIAttestation,
		OwnerID: identities.CIOwner, Digest: receipt.CIAttestationDigest,
		ExecutionDigest: execution.Digest, ResultKey: execution.ResultKey,
		Status: execution.Status, CreatedAt: now,
	}
	fixture := &reviewWorkerFixture{
		store: store,
		evidence: &reviewEvidenceFake{
			plan: plan, execution: execution, attestation: attestation,
			planFound: true, executionFound: true, attestationFound: true,
		},
		context: &reviewContextLoaderFake{text: `{"format":"thread-context"}`},
		workspace: &reviewWorkspaceFake{snapshot: gitworkspace.PinnedLineReviewSnapshot{
			Version: fence.LineVersion, MutationEpoch: fence.MutationEpoch,
			ParkIntentID: fence.ParkIntentID, BaseCommit: fence.BaseCommit,
			Commit: fence.TipCommit, Tree: fence.Tree,
			ChangedPaths: []string{"pkg/example.go"},
			UnifiedDiff:  "diff --git a/pkg/example.go b/pkg/example.go\n+fixed\n",
			ReviewDigest: fence.LineReviewDigest,
		}},
		executor: &reviewExecutorFake{result: agent.ControllerLocalReviewResult{
			Outcome: agent.ControllerLocalReviewPassed,
			Summary: "The candidate is clean.", Findings: []agent.ControllerLocalReviewFinding{},
		}},
	}
	worker, err := newReviewWorkerWithDependencies(reviewWorkerDependencies{
		store: store,
		workspaces: func() (reviewWorkspace, error) {
			return fixture.workspace, fixture.workspaceError
		},
		evidence: fixture.evidence,
		context: func(agentID string) (repairControllerContextLoader, error) {
			fixture.contextAgents = append(fixture.contextAgents, agentID)
			return fixture.context, nil
		},
		runtime: func(agentID string) (LocalReviewExecutor, error) {
			fixture.runtimeAgents = append(fixture.runtimeAgents, agentID)
			return fixture.executor, nil
		},
		workerLabel: "test-review-worker", lease: time.Hour,
	})
	if err != nil {
		t.Fatalf("newReviewWorkerWithDependencies() error = %v", err)
	}
	fixture.worker = worker
	return fixture
}

func TestReviewWorkerNoWork(t *testing.T) {
	store := &reviewWorkerStoreFake{}
	worker, err := newReviewWorkerWithDependencies(reviewWorkerDependencies{store: store})
	if err != nil {
		t.Fatalf("newReviewWorkerWithDependencies() error = %v", err)
	}
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || processed || store.claimCalls != 1 {
		t.Fatalf("ProcessOne() = %v, %v, claims %d", processed, err, store.claimCalls)
	}
}

func TestReviewWorkerPassedUsesExactReservationFreeEvidence(t *testing.T) {
	fixture := newReviewWorkerFixture(t)
	processed, err := fixture.worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = %v, %v", processed, err)
	}
	if len(fixture.store.completeCalls) != 1 || len(fixture.store.releaseCalls) != 0 ||
		len(fixture.workspace.requests) != 1 || len(fixture.executor.requests) != 1 {
		t.Fatalf("calls = complete %d release %d workspace %d model %d",
			len(fixture.store.completeCalls), len(fixture.store.releaseCalls),
			len(fixture.workspace.requests), len(fixture.executor.requests))
	}
	input := fixture.store.completeCalls[0]
	if input.Outcome != eventing.PRDevelopmentLedgerReviewPassed || len(input.Findings) != 0 ||
		input.ControllerID != reviewWorkerControllerID || input.LeaseToken != reviewWorkerLeaseToken {
		t.Fatalf("completion input = %#v", input)
	}
	request := fixture.workspace.requests[0]
	if request.LineID != reviewWorkerLineID || request.ExpectedVersion != 1 ||
		request.ExpectedBase != reviewWorkerHead || request.ExpectedTip != reviewWorkerCommit ||
		request.ExpectedTree != reviewWorkerCandidate {
		t.Fatalf("snapshot request = %#v", request)
	}
	if fixture.context.caseID != reviewWorkerCaseID || fixture.context.conversationVersion != 0 ||
		len(fixture.contextAgents) != 1 || fixture.contextAgents[0] != "pinned-agent" ||
		len(fixture.runtimeAgents) != 1 || fixture.runtimeAgents[0] != "pinned-agent" {
		t.Fatalf("context/runtime binding = %#v %#v %#v",
			fixture.context, fixture.contextAgents, fixture.runtimeAgents)
	}
	var projected map[string]any
	if err = json.Unmarshal([]byte(fixture.executor.requests[0].Context), &projected); err != nil {
		t.Fatalf("review context JSON error = %v", err)
	}
	if projected["format"] != localReviewContextFormat ||
		projected["untrusted_ordered_thread_context"] == nil ||
		projected["untrusted_parked_candidate"] == nil ||
		projected["untrusted_local_ci"] == nil {
		t.Fatalf("review context = %#v", projected)
	}
}

func TestReviewWorkerChangesRequiredMapsFindingsAndSchedulesRetry(t *testing.T) {
	fixture := newReviewWorkerFixture(t)
	line := 17
	fixture.executor.result = agent.ControllerLocalReviewResult{
		Outcome: agent.ControllerLocalReviewChangesRequired,
		Summary: "One correctness issue remains.",
		Findings: []agent.ControllerLocalReviewFinding{{
			Severity: agent.ControllerLocalReviewSeverityHigh,
			Title:    "Missing fence", File: "pkg/example.go", Line: &line,
			Message: "The update is not fenced.", Evidence: "The diff writes directly.",
			Impact: "A stale worker can win.", Recommendation: "Compare the revision.",
			Validation: "Run the focused race test.",
		}},
	}
	processed, err := fixture.worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = %v, %v", processed, err)
	}
	input := fixture.store.completeCalls[0]
	if input.Outcome != eventing.PRDevelopmentLedgerReviewChangesRequired ||
		len(input.Findings) != 1 || input.Findings[0].Severity != eventing.ReviewSeverityHigh ||
		input.Findings[0].Line == nil || *input.Findings[0].Line != line {
		t.Fatalf("changes-required input = %#v", input)
	}
}

func TestReviewWorkerPassedNonGreenBecomesAttention(t *testing.T) {
	fixture := newReviewWorkerFixture(t)
	setReviewFixtureCIStatus(fixture, eventing.PRDevelopmentCIFailed, localci.StatusFailed)
	processed, err := fixture.worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = %v, %v", processed, err)
	}
	input := fixture.store.completeCalls[0]
	if input.Outcome != eventing.PRDevelopmentLedgerReviewAttentionRequired ||
		!strings.Contains(input.Summary, "Local CI is not green") {
		t.Fatalf("non-green completion = %#v", input)
	}
}

func TestReviewWorkerCapacityMapsAndTransactionRaceRemapsToAttention(t *testing.T) {
	t.Run("preflight", func(t *testing.T) {
		fixture := newReviewWorkerFixture(t)
		fixture.store.workbench.RepairSession.Version = eventing.MaxPRDevelopmentRepairVersion - 3
		fixture.executor.result = changesRequiredReviewResult()
		processed, err := fixture.worker.ProcessOne(context.Background())
		if err != nil || !processed {
			t.Fatalf("ProcessOne() = %v, %v", processed, err)
		}
		if got := fixture.store.completeCalls[0].Outcome; got != eventing.PRDevelopmentLedgerReviewAttentionRequired {
			t.Fatalf("completion outcome = %q", got)
		}
	})

	t.Run("transaction race", func(t *testing.T) {
		fixture := newReviewWorkerFixture(t)
		fixture.executor.result = changesRequiredReviewResult()
		fixture.store.completeErrs = []error{eventing.ErrPRDevelopmentRepairCapacity}
		processed, err := fixture.worker.ProcessOne(context.Background())
		if err != nil || !processed {
			t.Fatalf("ProcessOne() = %v, %v", processed, err)
		}
		if len(fixture.store.completeCalls) != 2 ||
			fixture.store.completeCalls[0].Outcome !=
				eventing.PRDevelopmentLedgerReviewChangesRequired ||
			fixture.store.completeCalls[1].Outcome !=
				eventing.PRDevelopmentLedgerReviewAttentionRequired ||
			len(fixture.store.releaseCalls) != 0 {
			t.Fatalf("completion retries = %#v, releases = %d",
				fixture.store.completeCalls, len(fixture.store.releaseCalls))
		}
	})
}

func TestReviewWorkerEveryPreterminalFailureReleasesExactLease(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*reviewWorkerFixture)
	}{
		{name: "missing attestation", mutate: func(f *reviewWorkerFixture) {
			f.evidence.attestationFound = false
		}},
		{name: "evidence mismatch", mutate: func(f *reviewWorkerFixture) {
			f.evidence.execution.Evidence.PlanDigest = reviewWorkerDigest("1")
		}},
		{name: "context failure", mutate: func(f *reviewWorkerFixture) {
			f.context.err = errors.New("context failed")
		}},
		{name: "workspace failure", mutate: func(f *reviewWorkerFixture) {
			f.workspaceError = errors.New("workspace failed")
		}},
		{name: "snapshot drift", mutate: func(f *reviewWorkerFixture) {
			f.workspace.snapshot.ReviewDigest = reviewWorkerDigest("1")
		}},
		{name: "orchestration fence drift", mutate: func(f *reviewWorkerFixture) {
			f.store.orchestration.Validation.ControllerRevision++
		}},
		{name: "model failure", mutate: func(f *reviewWorkerFixture) {
			f.executor.err = errors.New("model failed")
		}},
		{name: "invalid structured result", mutate: func(f *reviewWorkerFixture) {
			f.executor.result.Findings = []agent.ControllerLocalReviewFinding{{
				Severity: agent.ControllerLocalReviewSeverityLow,
				Title:    "unexpected", Message: "passing cannot carry findings",
			}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReviewWorkerFixture(t)
			test.mutate(fixture)
			processed, err := fixture.worker.ProcessOne(context.Background())
			if !processed || err == nil {
				t.Fatalf("ProcessOne() = %v, %v", processed, err)
			}
			if len(fixture.store.completeCalls) != 0 || len(fixture.store.releaseCalls) != 1 {
				t.Fatalf("complete/release calls = %d/%d",
					len(fixture.store.completeCalls), len(fixture.store.releaseCalls))
			}
			release := fixture.store.releaseCalls[0]
			if release.ControllerID != reviewWorkerControllerID ||
				release.AttemptID != reviewWorkerAttemptID ||
				release.ExpectedRevision != 3 ||
				release.LeaseToken != reviewWorkerLeaseToken || release.LeaseEpoch != 2 {
				t.Fatalf("release = %#v", release)
			}
		})
	}
}

func TestReviewWorkerFailureReleaseSurvivesParentCancellation(t *testing.T) {
	fixture := newReviewWorkerFixture(t)
	fixture.context.err = errors.New("context failed")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	processed, err := fixture.worker.ProcessOne(ctx)
	if !processed || err == nil || len(fixture.store.releaseCtxErr) != 1 ||
		fixture.store.releaseCtxErr[0] != nil {
		t.Fatalf("ProcessOne() = %v, %v, release contexts %#v",
			processed, err, fixture.store.releaseCtxErr)
	}
}

func TestReviewWorkerHeartbeatLossCancelsModelAndReleases(t *testing.T) {
	fixture := newReviewWorkerFixture(t)
	lost := errors.New("review lease lost")
	fixture.worker.lease = MinimumRepairControllerLease
	fixture.store.renewErr = lost
	modelStarted := make(chan struct{})
	fixture.executor.run = func(
		ctx context.Context,
		_ agent.ControllerLocalReviewRequest,
	) (agent.ControllerLocalReviewResult, error) {
		close(modelStarted)
		<-ctx.Done()
		return agent.ControllerLocalReviewResult{}, ctx.Err()
	}
	result := make(chan error, 1)
	go func() {
		_, err := fixture.worker.ProcessOne(context.Background())
		result <- err
	}()
	<-modelStarted
	select {
	case err := <-result:
		if !errors.Is(err, lost) {
			t.Fatalf("ProcessOne() error = %v, want heartbeat loss", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ProcessOne() did not stop after review heartbeat loss")
	}
	if len(fixture.store.releaseCalls) != 1 || len(fixture.store.completeCalls) != 0 {
		t.Fatalf("release/complete calls = %d/%d",
			len(fixture.store.releaseCalls), len(fixture.store.completeCalls))
	}
}

func TestReviewWorkerTerminalBarrierDrainsRenewalBeforeCompletion(t *testing.T) {
	fixture := newReviewWorkerFixture(t)
	fixture.worker.lease = MinimumRepairControllerLease
	fixture.store.renewStarted = make(chan struct{})
	fixture.store.renewAllow = make(chan struct{})
	fixture.store.completeHit = make(chan struct{})
	modelReturned := make(chan struct{})
	fixture.executor.run = func(
		context.Context,
		agent.ControllerLocalReviewRequest,
	) (agent.ControllerLocalReviewResult, error) {
		<-fixture.store.renewStarted
		close(modelReturned)
		return fixture.executor.result, nil
	}
	result := make(chan error, 1)
	go func() {
		_, err := fixture.worker.ProcessOne(context.Background())
		result <- err
	}()
	select {
	case <-modelReturned:
	case <-time.After(3 * time.Second):
		t.Fatal("model did not return while renewal was in flight")
	}
	select {
	case <-fixture.store.completeHit:
		t.Fatal("completion crossed an in-flight heartbeat renewal")
	case <-time.After(100 * time.Millisecond):
	}
	close(fixture.store.renewAllow)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("ProcessOne() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ProcessOne() did not complete after renewal drained")
	}
	if len(fixture.store.renewCalls) != 1 || len(fixture.store.releaseCalls) != 0 {
		t.Fatalf("renew/release calls = %d/%d",
			len(fixture.store.renewCalls), len(fixture.store.releaseCalls))
	}
}

func TestReviewWorkerAmbiguousCompletionNeverReleasesRetiredAuthority(t *testing.T) {
	fixture := newReviewWorkerFixture(t)
	uncertain := errors.New("completion response lost")
	fixture.store.completeErrs = []error{uncertain}
	processed, err := fixture.worker.ProcessOne(context.Background())
	if !processed || !errors.Is(err, uncertain) || len(fixture.store.completeCalls) != 1 ||
		len(fixture.store.releaseCalls) != 0 {
		t.Fatalf("ProcessOne() = %v, %v, complete/release %d/%d",
			processed, err, len(fixture.store.completeCalls), len(fixture.store.releaseCalls))
	}
}

func TestReviewWorkerRejectsInvalidLeaseBeforeClaim(t *testing.T) {
	store := &reviewWorkerStoreFake{}
	worker := &ReviewWorker{reviewWorkerDependencies: reviewWorkerDependencies{
		store: store, lease: MinimumRepairControllerLease - time.Nanosecond,
	}}
	processed, err := worker.ProcessOne(context.Background())
	if processed || !errors.Is(err, ErrUnavailable) || store.claimCalls != 0 {
		t.Fatalf("ProcessOne() = %v, %v, claim calls %d", processed, err, store.claimCalls)
	}
}

func TestValidateReviewSnapshotComparesEveryProjectedFenceField(t *testing.T) {
	fixture := newReviewWorkerFixture(t)
	fence := fixture.store.claim.Fence
	valid := fixture.workspace.snapshot
	tests := []struct {
		name   string
		mutate func(*gitworkspace.PinnedLineReviewSnapshot)
	}{
		{name: "version", mutate: func(value *gitworkspace.PinnedLineReviewSnapshot) {
			value.Version++
		}},
		{name: "mutation epoch", mutate: func(value *gitworkspace.PinnedLineReviewSnapshot) {
			value.MutationEpoch++
		}},
		{name: "park intent", mutate: func(value *gitworkspace.PinnedLineReviewSnapshot) {
			value.ParkIntentID += "x"
		}},
		{name: "base", mutate: func(value *gitworkspace.PinnedLineReviewSnapshot) {
			value.BaseCommit = reviewWorkerCommit
		}},
		{name: "tip", mutate: func(value *gitworkspace.PinnedLineReviewSnapshot) {
			value.Commit = reviewWorkerHead
		}},
		{name: "tree", mutate: func(value *gitworkspace.PinnedLineReviewSnapshot) {
			value.Tree = reviewWorkerSourceTree
		}},
		{name: "paths", mutate: func(value *gitworkspace.PinnedLineReviewSnapshot) {
			value.ChangedPaths = nil
		}},
		{name: "diff", mutate: func(value *gitworkspace.PinnedLineReviewSnapshot) {
			value.UnifiedDiff = ""
		}},
		{name: "digest", mutate: func(value *gitworkspace.PinnedLineReviewSnapshot) {
			value.ReviewDigest = reviewWorkerDigest("1")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := valid
			test.mutate(&changed)
			if err := validateReviewSnapshot(fence, changed); err == nil {
				t.Fatal("validateReviewSnapshot() error = nil")
			}
		})
	}
}

func TestValidateReviewSnapshotAcceptsExactNoChangePark(t *testing.T) {
	fixture := newReviewWorkerFixture(t)
	fence := fixture.store.claim.Fence
	fence.NoChanges = true
	fence.TipCommit = fence.BaseCommit
	snapshot := fixture.workspace.snapshot
	snapshot.Commit = snapshot.BaseCommit
	snapshot.ChangedPaths = nil
	snapshot.UnifiedDiff = ""
	if err := validateReviewSnapshot(fence, snapshot); err != nil {
		t.Fatalf("validateReviewSnapshot(no change) error = %v", err)
	}
}

func TestReviewRetryCapacityChecksEveryStoreHeadroomFence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*reviewWorkerClaim)
	}{
		{name: "attempts", mutate: func(claim *reviewWorkerClaim) {
			claim.session.Attempts = make(
				[]eventing.PRDevelopmentRepairAttempt,
				eventing.MaxPRDevelopmentRepairAttempts,
			)
		}},
		{name: "session revision", mutate: func(claim *reviewWorkerClaim) {
			claim.session.Version = eventing.MaxPRDevelopmentRepairVersion - 3
		}},
		{name: "line version", mutate: func(claim *reviewWorkerClaim) {
			claim.lease.Controller.LineVersion = eventing.MaxPRDevelopmentControllerFences
		}},
		{name: "controller revision", mutate: func(claim *reviewWorkerClaim) {
			claim.lease.Controller.Revision = eventing.MaxPRDevelopmentControllerRevision - 6
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReviewWorkerFixture(t)
			claim := reviewWorkerClaim{
				lease:   fixture.store.claim,
				session: *fixture.store.workbench.RepairSession,
			}
			if !reviewRetryHasCapacity(claim) {
				t.Fatal("reviewRetryHasCapacity(valid) = false")
			}
			test.mutate(&claim)
			if reviewRetryHasCapacity(claim) {
				t.Fatal("reviewRetryHasCapacity(exhausted) = true")
			}
		})
	}
}

func TestMapLocalReviewAttentionNeverSchedulesRepair(t *testing.T) {
	fixture := newReviewWorkerFixture(t)
	claim, err := fixture.worker.loadClaim(context.Background(), fixture.store.claim)
	if err != nil {
		t.Fatalf("loadClaim() error = %v", err)
	}
	input, err := mapLocalReviewResult(claim, agent.ControllerLocalReviewResult{
		Outcome:  agent.ControllerLocalReviewAttentionRequired,
		Summary:  "A product decision is required.",
		Findings: []agent.ControllerLocalReviewFinding{},
	})
	if err != nil || input.Outcome != eventing.PRDevelopmentLedgerReviewAttentionRequired ||
		len(input.Findings) != 0 {
		t.Fatalf("mapLocalReviewResult(attention) = %#v, %v", input, err)
	}
}

func changesRequiredReviewResult() agent.ControllerLocalReviewResult {
	return agent.ControllerLocalReviewResult{
		Outcome: agent.ControllerLocalReviewChangesRequired,
		Summary: "A code change is required.",
		Findings: []agent.ControllerLocalReviewFinding{{
			Severity: agent.ControllerLocalReviewSeverityMedium,
			Title:    "Incorrect behavior", Message: "The branch handles the wrong state.",
		}},
	}
}

func setReviewFixtureCIStatus(
	fixture *reviewWorkerFixture,
	status eventing.PRDevelopmentCIStatus,
	localStatus localci.Status,
) {
	fixture.store.orchestration.Validation.CIStatus = status
	fixture.evidence.execution.Status = localStatus
	fixture.evidence.attestation.Status = localStatus
	if localStatus != localci.StatusPassed {
		fixture.evidence.execution.Steps[0].Status = localStatus
		fixture.evidence.execution.Steps[0].ExitCode = 1
	}
}
