package prdevelopment

import (
	"context"
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
	controllerWorkerCaseID       = "pdc_11111111111111111111111111111111"
	controllerWorkerThreadID     = "pdt_22222222222222222222222222222222"
	controllerWorkerSessionID    = "pds_33333333333333333333333333333333"
	controllerWorkerAttemptID    = "pdr_44444444444444444444444444444444"
	controllerWorkerControllerID = "pctl_55555555555555555555555555555555"
	controllerWorkerLineID       = "pdln_66666666666666666666666666666666"
	controllerWorkerReservation  = "pdrk_77777777777777777777777777777777"
	controllerWorkerRotatedKey   = "pdck_88888888888888888888888888888888"
	controllerWorkerLeaseToken   = "worker:lease-token"
	controllerWorkerHead         = "1111111111111111111111111111111111111111"
	controllerWorkerSourceTree   = "2222222222222222222222222222222222222222"
	controllerWorkerCandidate    = "3333333333333333333333333333333333333333"
	controllerWorkerCommit       = "4444444444444444444444444444444444444444"
)

func controllerWorkerDigest(character string) string {
	return strings.Repeat(character, 64)
}

type repairControllerWorkerStoreFake struct {
	mu sync.Mutex

	claimed       bool
	run           eventing.PRDevelopmentRepairOrchestration
	workbench     eventing.PRDevelopmentWorkbench
	controller    eventing.PRDevelopmentController
	controllerErr error
	lease         eventing.PRDevelopmentControllerLease

	claimCalls      int
	renewCalls      int
	controllerRenew int
	resumeRenew     int
	pinCalls        []eventing.PRDevelopmentRepairOrchestrationPin
	acquireCalls    []eventing.PRDevelopmentRepairOrchestrationControllerAcquire
	resumeFinalizes []eventing.PRDevelopmentControllerSuspendedResumeFinalize
	starts          []eventing.PRDevelopmentRepairOrchestrationModelStart
	completes       []eventing.PRDevelopmentRepairOrchestrationModelComplete
	validations     []eventing.PRDevelopmentRepairOrchestrationValidation
	failures        []eventing.PRDevelopmentRepairOrchestrationFail

	pinErr             error
	acquireErr         error
	renewErr           error
	controllerRenewErr error
	resumeRenewErr     error
	resumeFinalizeErr  error
	resumeFinalLease   eventing.PRDevelopmentControllerLease
	events             *[]string
}

func (store *repairControllerWorkerStoreFake) ClaimPRDevelopmentRepairOrchestration(
	context.Context,
	eventing.PRDevelopmentRepairOrchestrationClaim,
) (eventing.PRDevelopmentRepairOrchestration, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.claimCalls++
	return store.run, store.claimed, nil
}

func (store *repairControllerWorkerStoreFake) RenewPRDevelopmentRepairOrchestration(
	context.Context,
	eventing.PRDevelopmentRepairOrchestrationRenew,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.renewCalls++
	return store.renewErr
}

func (store *repairControllerWorkerStoreFake) GetPRDevelopmentRepairOrchestration(
	context.Context,
	string,
) (eventing.PRDevelopmentRepairOrchestration, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.run, nil
}

func (store *repairControllerWorkerStoreFake) PinPRDevelopmentRepairOrchestration(
	_ context.Context,
	input eventing.PRDevelopmentRepairOrchestrationPin,
) (eventing.PRDevelopmentRepairOrchestration, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pinCalls = append(store.pinCalls, input)
	if store.pinErr != nil {
		return eventing.PRDevelopmentRepairOrchestration{}, false, store.pinErr
	}
	store.run.HeadRepository = input.HeadRepository
	store.run.HeadRef = input.HeadRef
	store.run.HeadSHA = input.HeadSHA
	store.run.CloneURL = input.CloneURL
	store.run.ReviewDigest = input.ReviewDigest
	store.run.WorkspaceID = input.WorkspaceID
	store.run.SourceTree = input.SourceTree
	return store.run, true, nil
}

func (store *repairControllerWorkerStoreFake) AcquirePRDevelopmentRepairOrchestrationController(
	_ context.Context,
	input eventing.PRDevelopmentRepairOrchestrationControllerAcquire,
) (eventing.PRDevelopmentControllerLease, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.acquireCalls = append(store.acquireCalls, input)
	if store.acquireErr != nil {
		return eventing.PRDevelopmentControllerLease{}, false, store.acquireErr
	}
	return store.lease, true, nil
}

func (store *repairControllerWorkerStoreFake) StartPRDevelopmentRepairOrchestrationModel(
	_ context.Context,
	input eventing.PRDevelopmentRepairOrchestrationModelStart,
) (eventing.PRDevelopmentRepairOrchestration, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.starts = append(store.starts, input)
	store.run.Phase = eventing.PRDevelopmentRepairOrchestrationEditing
	store.run.ControllerID = input.ControllerID
	store.run.ModelControllerRevision = input.ControllerRevision
	store.run.ModelLineID = controllerWorkerLineID
	store.run.ModelLineVersion = 0
	store.run.ModelMutationEpoch = 1
	store.run.ModelMutationLeaseEpoch = input.MutationLeaseEpoch
	store.run.ModelLeaseTokenDigest = controllerWorkerDigest("6")
	store.run.ModelReservationDigest = controllerWorkerDigest("7")
	store.run.ContextDigest = input.ContextDigest
	store.run.PromptDigest = input.PromptDigest
	return store.run, true, nil
}

func (store *repairControllerWorkerStoreFake) CompletePRDevelopmentRepairOrchestrationModel(
	_ context.Context,
	input eventing.PRDevelopmentRepairOrchestrationModelComplete,
) (eventing.PRDevelopmentRepairOrchestration, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.completes = append(store.completes, input)
	store.run.Phase = eventing.PRDevelopmentRepairOrchestrationEdited
	store.run.ModelResultDigest = input.ModelResultDigest
	store.run.Summary = input.Summary
	store.run.Iterations = input.Iterations
	return store.run, true, nil
}

func (store *repairControllerWorkerStoreFake) RecordPRDevelopmentRepairOrchestrationValidation(
	_ context.Context,
	input eventing.PRDevelopmentRepairOrchestrationValidation,
) (eventing.PRDevelopmentRepairOrchestration, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.validations = append(store.validations, input)
	store.run.Phase = eventing.PRDevelopmentRepairOrchestrationValidated
	store.run.Validation = &eventing.PRDevelopmentRepairValidationReceipt{
		ControllerID:              input.ControllerID,
		WorkspaceID:               store.run.WorkspaceID,
		ModelControllerRevision:   store.run.ModelControllerRevision,
		ModelLineID:               store.run.ModelLineID,
		ModelLineVersion:          store.run.ModelLineVersion,
		ModelMutationEpoch:        store.run.ModelMutationEpoch,
		ModelMutationLeaseEpoch:   store.run.ModelMutationLeaseEpoch,
		ModelLeaseTokenDigest:     store.run.ModelLeaseTokenDigest,
		ModelReservationDigest:    store.run.ModelReservationDigest,
		ContextDigest:             store.run.ContextDigest,
		PromptDigest:              store.run.PromptDigest,
		LineID:                    controllerWorkerLineID,
		ControllerRevision:        input.ControllerRevision,
		LineVersion:               0,
		MutationEpoch:             1,
		MutationLeaseEpoch:        input.MutationLeaseEpoch,
		MutationLeaseTokenDigest:  controllerWorkerDigest("8"),
		MutationReservationDigest: controllerWorkerDigest("9"),
		ParentCommit:              input.ParentCommit,
		ParentTree:                input.ParentTree,
		CandidateTree:             input.CandidateTree,
		CandidateDigest:           input.CandidateDigest,
		ChangedFiles:              input.ChangedFiles,
		NoChanges:                 input.NoChanges,
		CIStatus:                  input.CIStatus,
		CIAttestationID:           input.CIAttestationID,
		CIAttestationDigest:       input.CIAttestationDigest,
		CIResultKey:               input.CIResultKey,
		CIEffectivePlanDigest:     input.CIEffectivePlanDigest,
		CIExecutionDigest:         input.CIExecutionDigest,
		ModelResultDigest:         store.run.ModelResultDigest,
		ModelSummary:              store.run.Summary,
		ModelIterations:           store.run.Iterations,
		ReceiptHash:               controllerWorkerDigest("f"),
	}
	return store.run, true, nil
}

func (store *repairControllerWorkerStoreFake) FailPRDevelopmentRepairOrchestration(
	_ context.Context,
	input eventing.PRDevelopmentRepairOrchestrationFail,
) (eventing.PRDevelopmentRepairOrchestration, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.failures = append(store.failures, input)
	if store.events != nil {
		*store.events = append(*store.events, "fail")
	}
	store.run.Phase = eventing.PRDevelopmentRepairOrchestrationFailed
	return store.run, true, nil
}

func (store *repairControllerWorkerStoreFake) GetPRDevelopmentWorkbench(
	context.Context,
	string,
) (eventing.PRDevelopmentWorkbench, error) {
	return store.workbench, nil
}

func (store *repairControllerWorkerStoreFake) GetPRDevelopmentControllerForCase(
	context.Context,
	string,
) (eventing.PRDevelopmentController, error) {
	if store.controllerErr != nil {
		return eventing.PRDevelopmentController{}, store.controllerErr
	}
	return store.controller, nil
}

func (store *repairControllerWorkerStoreFake) RenewPRDevelopmentControllerLease(
	context.Context,
	eventing.PRDevelopmentControllerRenew,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.controllerRenew++
	return store.controllerRenewErr
}

func (store *repairControllerWorkerStoreFake) RenewPRDevelopmentControllerSuspendedResume(
	context.Context,
	eventing.PRDevelopmentControllerSuspendedResumeRenew,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.resumeRenew++
	return store.resumeRenewErr
}

func (store *repairControllerWorkerStoreFake) FinalizePRDevelopmentControllerSuspendedResume(
	_ context.Context,
	input eventing.PRDevelopmentControllerSuspendedResumeFinalize,
) (eventing.PRDevelopmentControllerLease, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.resumeFinalizes = append(store.resumeFinalizes, input)
	if store.events != nil {
		*store.events = append(*store.events, "resume-finalize")
	}
	if store.resumeFinalizeErr != nil {
		return eventing.PRDevelopmentControllerLease{}, false, store.resumeFinalizeErr
	}
	return store.resumeFinalLease, true, nil
}

func (store *repairControllerWorkerStoreFake) PreparePRDevelopmentControllerOperation(
	context.Context,
	eventing.PRDevelopmentControllerOperationPrepare,
) (eventing.PRDevelopmentControllerOperation, bool, error) {
	return eventing.PRDevelopmentControllerOperation{}, false, errors.New("unexpected real effect journal call")
}

func (store *repairControllerWorkerStoreFake) FinalizePRDevelopmentControllerOperation(
	context.Context,
	eventing.PRDevelopmentControllerOperationFinalize,
) (eventing.PRDevelopmentControllerOperationTransition, bool, error) {
	return eventing.PRDevelopmentControllerOperationTransition{}, false, errors.New(
		"unexpected real effect journal call",
	)
}

type repairControllerContextLoaderFake struct {
	text   string
	calls  int
	events *[]string
}

func (loader *repairControllerContextLoaderFake) Load(
	context.Context,
	string,
	int64,
) (string, error) {
	loader.calls++
	if loader.events != nil {
		*loader.events = append(*loader.events, "context")
	}
	return loader.text, nil
}

type repairControllerVerifierFake struct {
	verified VerifiedCase
	err      error
	calls    int
}

func (verifier *repairControllerVerifierFake) VerifyCase(
	context.Context,
	eventing.PRDevelopmentCase,
	*eventing.PRDevelopmentThreadIdentity,
) (VerifiedCase, error) {
	verifier.calls++
	return verifier.verified, verifier.err
}

type repairControllerExecutorFake struct {
	result        agent.LocalRepairResult
	err           error
	request       agent.LocalRepairRequest
	calls         int
	waitForCancel bool
	events        *[]string
}

func (executor *repairControllerExecutorFake) Run(
	ctx context.Context,
	request agent.LocalRepairRequest,
) (agent.LocalRepairResult, error) {
	executor.calls++
	executor.request = request
	if executor.events != nil {
		*executor.events = append(*executor.events, "model")
	}
	if executor.waitForCancel {
		<-ctx.Done()
		return agent.LocalRepairResult{}, ctx.Err()
	}
	return executor.result, executor.err
}

type repairControllerWorkspaceFake struct {
	workspace        gitworkspace.WorkspaceInfo
	snapshots        []gitworkspace.PinnedCandidate
	acquires         []gitworkspace.PinnedAcquireRequest
	releases         []gitworkspace.PinnedReleaseRequest
	snapshotRequests []gitworkspace.PinnedCandidateRequest
	acquireErr       error
	acquireHook      func()
	snapshotErr      error
	releaseErr       error
	resumeRequests   []gitworkspace.PinnedLineSuspendedResumeRequest
	resumeResults    []gitworkspace.PinnedLineSuspendedResumeResult
	resumeErr        error
	events           *[]string
}

func (workspace *repairControllerWorkspaceFake) AcquirePinned(
	_ context.Context,
	request gitworkspace.PinnedAcquireRequest,
) (gitworkspace.WorkspaceInfo, error) {
	workspace.acquires = append(workspace.acquires, request)
	if workspace.acquireHook != nil {
		workspace.acquireHook()
	}
	return workspace.workspace, workspace.acquireErr
}

func (workspace *repairControllerWorkspaceFake) ReleasePinned(
	_ context.Context,
	request gitworkspace.PinnedReleaseRequest,
) ([]gitworkspace.WorkspaceInfo, error) {
	workspace.releases = append(workspace.releases, request)
	if workspace.events != nil {
		*workspace.events = append(*workspace.events, "release")
	}
	return nil, workspace.releaseErr
}

func (workspace *repairControllerWorkspaceFake) SnapshotPinnedValidationCandidate(
	_ context.Context,
	request gitworkspace.PinnedCandidateRequest,
) (gitworkspace.PinnedCandidate, error) {
	workspace.snapshotRequests = append(workspace.snapshotRequests, request)
	if workspace.snapshotErr != nil {
		return gitworkspace.PinnedCandidate{}, workspace.snapshotErr
	}
	if len(workspace.snapshots) == 0 {
		return gitworkspace.PinnedCandidate{}, errors.New("unexpected candidate snapshot")
	}
	result := workspace.snapshots[0]
	workspace.snapshots = workspace.snapshots[1:]
	return result, nil
}

func (workspace *repairControllerWorkspaceFake) ResumeSuspendedPinnedLine(
	_ context.Context,
	request gitworkspace.PinnedLineSuspendedResumeRequest,
) (gitworkspace.PinnedLineSuspendedResumeResult, error) {
	workspace.resumeRequests = append(workspace.resumeRequests, request)
	if workspace.events != nil {
		*workspace.events = append(*workspace.events, "resume-git")
	}
	if workspace.resumeErr != nil {
		return gitworkspace.PinnedLineSuspendedResumeResult{}, workspace.resumeErr
	}
	if len(workspace.resumeResults) == 0 {
		return gitworkspace.PinnedLineSuspendedResumeResult{}, errors.New(
			"unexpected suspended resume",
		)
	}
	result := workspace.resumeResults[0]
	workspace.resumeResults = workspace.resumeResults[1:]
	return result, nil
}

type repairControllerCIFake struct {
	status   localci.Status
	requests []localci.PinnedRunRequest
	calls    int
	tamper   func(*localci.RunResult)
}

func (ci *repairControllerCIFake) RunPinned(
	_ context.Context,
	_ repairControllerWorkspace,
	request localci.PinnedRunRequest,
) (localci.RunResult, error) {
	ci.calls++
	ci.requests = append(ci.requests, request)
	status := ci.status
	if status == "" {
		status = localci.StatusPassed
	}
	result := localci.RunResult{
		Plan: localci.ResolvedPlan{Effective: localci.Plan{Digest: controllerWorkerDigest("a")}},
		Execution: localci.Execution{
			Digest:    controllerWorkerDigest("b"),
			ResultKey: controllerWorkerDigest("c"),
			Status:    status,
			Evidence: localci.CandidateEvidence{
				ParentCommit:    request.Candidate.ExpectedParent,
				Tree:            request.Candidate.ExpectedTree,
				CandidateDigest: request.Candidate.ExpectedCandidateDigest,
				PlanDigest:      controllerWorkerDigest("a"),
			},
		},
		Attestation: localci.Attestation{
			ID:              request.AttestationID,
			OwnerID:         request.OwnerID,
			Digest:          controllerWorkerDigest("d"),
			ExecutionDigest: controllerWorkerDigest("b"),
			ResultKey:       controllerWorkerDigest("c"),
			Status:          status,
		},
	}
	if ci.tamper != nil {
		ci.tamper(&result)
	}
	return result, nil
}

type repairControllerEffectsFake struct {
	line           controllerLineState
	adopts         int
	resumes        int
	commits        []gitworkspace.PinnedCandidate
	messages       []string
	parks          int
	parkSummary    string
	parkIterations int
}

func (effects *repairControllerEffectsFake) Adopt(
	context.Context,
	string,
) (controllerLineState, error) {
	effects.adopts++
	return effects.line, nil
}

func (effects *repairControllerEffectsFake) Resume(
	context.Context,
) (controllerLineState, error) {
	effects.resumes++
	return effects.line, nil
}

func (effects *repairControllerEffectsFake) CommitCandidate(
	_ context.Context,
	candidate gitworkspace.PinnedCandidate,
	message string,
) (controllerCommitOutcome, error) {
	effects.commits = append(effects.commits, candidate)
	effects.messages = append(effects.messages, message)
	return controllerCommitOutcome{
		controllerID:    effects.line.ControllerID,
		attemptID:       controllerWorkerAttemptID,
		workspaceID:     candidate.WorkspaceID,
		parentCommit:    candidate.ParentCommit,
		tree:            candidate.Tree,
		candidateDigest: candidate.CandidateDigest,
		commit:          controllerWorkerCommit,
		changedFiles:    candidate.ChangedFiles,
		changed:         candidate.ChangedFiles > 0,
	}, nil
}

func (effects *repairControllerEffectsFake) Park(
	_ context.Context,
	_ controllerCommitOutcome,
	summary string,
	iterations int,
	beforeFinalize func(),
) (eventing.PRDevelopmentAttemptReviewFence, error) {
	if beforeFinalize != nil {
		beforeFinalize()
	}
	effects.parks++
	effects.parkSummary = summary
	effects.parkIterations = iterations
	return eventing.PRDevelopmentAttemptReviewFence{AttemptID: controllerWorkerAttemptID}, nil
}

type repairControllerWorkerFixture struct {
	store         *repairControllerWorkerStoreFake
	context       *repairControllerContextLoaderFake
	verifier      *repairControllerVerifierFake
	executor      *repairControllerExecutorFake
	workspace     *repairControllerWorkspaceFake
	ci            *repairControllerCIFake
	effects       *repairControllerEffectsFake
	contextAgents []string
	runtimeAgents []string
	worker        *RepairControllerWorker
}

func newRepairControllerWorkerFixture(
	t *testing.T,
	phase eventing.PRDevelopmentRepairOrchestrationPhase,
	changed bool,
	ciStatus localci.Status,
) *repairControllerWorkerFixture {
	t.Helper()
	now := time.Now().UTC()
	claimUntil := now.Add(time.Hour)
	attempt := eventing.PRDevelopmentRepairAttempt{
		ID:                  controllerWorkerAttemptID,
		SessionID:           controllerWorkerSessionID,
		Ordinal:             0,
		ConversationVersion: 0,
		Instruction:         "address the review",
		Status:              eventing.PRDevelopmentRepairQueued,
		CreatedAt:           now.Truncate(time.Second),
		UpdatedAt:           now.Truncate(time.Second),
	}
	session := eventing.PRDevelopmentRepairSession{
		ID:             controllerWorkerSessionID,
		CaseID:         controllerWorkerCaseID,
		AgentID:        "pinned-agent",
		ReservationKey: controllerWorkerReservation,
		Attempts:       []eventing.PRDevelopmentRepairAttempt{attempt},
	}
	run := eventing.PRDevelopmentRepairOrchestration{
		AttemptID:   attempt.ID,
		SessionID:   session.ID,
		CaseID:      session.CaseID,
		ThreadID:    controllerWorkerThreadID,
		AgentID:     session.AgentID,
		Instruction: attempt.Instruction,
		Phase:       phase,
		ClaimToken:  "claim-token",
		ClaimUntil:  &claimUntil,
	}
	controller := eventing.PRDevelopmentController{}
	controllerErr := eventing.ErrNotFound
	leaseController := eventing.PRDevelopmentController{
		ID:                     controllerWorkerControllerID,
		ThreadID:               controllerWorkerThreadID,
		OwnerSessionID:         controllerWorkerSessionID,
		AgentID:                session.AgentID,
		Revision:               1,
		Phase:                  eventing.PRDevelopmentControllerMutation,
		LineID:                 controllerWorkerLineID,
		CurrentAttemptID:       attempt.ID,
		LeaseKind:              eventing.PRDevelopmentControllerMutationLease,
		LeaseToken:             controllerWorkerLeaseToken,
		LeaseEpoch:             1,
		MutationReservationKey: controllerWorkerReservation,
	}
	if phase != eventing.PRDevelopmentRepairOrchestrationBootstrap {
		session.HeadRepository = "owner/repo"
		session.HeadRef = "feature"
		session.HeadSHA = controllerWorkerHead
		session.CloneURL = "https://github.com/owner/repo.git"
		session.ReviewDigest = controllerWorkerDigest("e")
		session.WorkspaceID = "workspace-1"
		run.HeadRepository = session.HeadRepository
		run.HeadRef = session.HeadRef
		run.HeadSHA = session.HeadSHA
		run.CloneURL = session.CloneURL
		run.ReviewDigest = session.ReviewDigest
		run.WorkspaceID = session.WorkspaceID
		run.SourceTree = controllerWorkerSourceTree
		run.ControllerID = controllerWorkerControllerID
		run.ModelControllerRevision = 2
		run.ModelLineID = controllerWorkerLineID
		run.ModelLineVersion = 0
		run.ModelMutationEpoch = 1
		run.ModelMutationLeaseEpoch = 1
		run.ModelLeaseTokenDigest = controllerWorkerDigest("6")
		run.ModelReservationDigest = controllerWorkerDigest("7")
		run.ContextDigest = controllerWorkerDigest("1")
		run.PromptDigest = controllerWorkerDigest("2")
		run.ModelResultDigest = controllerWorkerDigest("3")
		run.Summary = "edited the requested files"
		run.Iterations = 2
		controllerErr = nil
		controller = leaseController
		controller.Revision = 2
		controller.WorkspaceID = session.WorkspaceID
		controller.SourceCloneURL = session.CloneURL
		controller.SourceRef = session.HeadRef
		controller.SourceCommit = session.HeadSHA
		controller.SourceTree = controllerWorkerSourceTree
		controller.LineVersion = 0
		controller.MutationEpoch = 1
		controller.TipCommit = controllerWorkerHead
		controller.Tree = controllerWorkerSourceTree
		controller.MutationReservationKey = ""
		controller.LeaseToken = ""
		leaseController = controller
		leaseController.LeaseToken = controllerWorkerLeaseToken
		leaseController.MutationReservationKey = controllerWorkerRotatedKey
	}
	store := &repairControllerWorkerStoreFake{
		claimed:       true,
		run:           run,
		controller:    controller,
		controllerErr: controllerErr,
		lease:         eventing.PRDevelopmentControllerLease{Controller: leaseController},
	}
	thread := &eventing.PRDevelopmentThreadBinding{
		ID:        controllerWorkerThreadID,
		Kind:      eventing.PRDevelopmentThreadProvider,
		CaseCount: 1,
		Case: eventing.PRDevelopmentThreadCaseLink{
			CaseID:  controllerWorkerCaseID,
			Ordinal: 0,
		},
		Identity: eventing.PRDevelopmentThreadIdentity{
			Provider:       "github",
			ProviderOrigin: "https://github.com",
			PullAuthorID:   "10",
			RepositoryID:   "20",
			PullRequestID:  "30",
			PullNumber:     7,
		},
	}
	caseRecord := eventing.PRDevelopmentCase{
		ID: controllerWorkerCaseID,
		PRDevelopmentCaptureInput: eventing.PRDevelopmentCaptureInput{
			Repository:           "owner/repo",
			PullNumber:           7,
			SubmittedReviewState: eventing.PRDevelopmentReviewChangesRequested,
		},
	}
	store.workbench = eventing.PRDevelopmentWorkbench{
		Case:          caseRecord,
		Thread:        thread,
		Conversation:  eventing.PRDevelopmentConversation{CaseID: controllerWorkerCaseID},
		RepairSession: &session,
	}
	verified := VerifiedCase{
		CaseID:             controllerWorkerCaseID,
		Repository:         "owner/repo",
		PullNumber:         7,
		HeadRepository:     "owner/repo",
		HeadRef:            "feature",
		HeadSHA:            controllerWorkerHead,
		HeadCloneURL:       "https://github.com/owner/repo.git",
		CurrentReviewState: eventing.PRDevelopmentReviewChangesRequested,
		ReviewDigest:       controllerWorkerDigest("e"),
	}
	candidateTree := controllerWorkerCandidate
	changedFiles := 1
	if !changed {
		candidateTree = controllerWorkerSourceTree
		changedFiles = 0
	}
	workspace := &repairControllerWorkspaceFake{
		workspace: gitworkspace.WorkspaceInfo{ID: "workspace-1"},
		snapshots: []gitworkspace.PinnedCandidate{
			{
				WorkspaceID:     "workspace-1",
				ParentCommit:    controllerWorkerHead,
				Tree:            controllerWorkerSourceTree,
				CandidateDigest: controllerWorkerDigest("4"),
				ChangedFiles:    0,
			},
			{
				WorkspaceID:     "workspace-1",
				ParentCommit:    controllerWorkerHead,
				Tree:            candidateTree,
				CandidateDigest: controllerWorkerDigest("5"),
				ChangedFiles:    changedFiles,
			},
		},
	}
	if phase != eventing.PRDevelopmentRepairOrchestrationBootstrap {
		workspace.snapshots = workspace.snapshots[1:]
	}
	contextLoader := &repairControllerContextLoaderFake{text: `{"context":"bounded"}`}
	executor := &repairControllerExecutorFake{result: agent.LocalRepairResult{
		Content:     "edited the requested files",
		Iterations:  2,
		WorkspaceID: "workspace-1",
	}}
	effects := &repairControllerEffectsFake{line: controllerLineState{
		ControllerID:  controllerWorkerControllerID,
		WorkspaceID:   "workspace-1",
		LineID:        controllerWorkerLineID,
		Revision:      2,
		LineVersion:   0,
		MutationEpoch: 1,
		Tip:           controllerWorkerHead,
		Tree:          controllerWorkerSourceTree,
	}}
	fixture := &repairControllerWorkerFixture{
		store:     store,
		context:   contextLoader,
		verifier:  &repairControllerVerifierFake{verified: verified},
		executor:  executor,
		workspace: workspace,
		ci:        &repairControllerCIFake{status: ciStatus},
		effects:   effects,
	}
	worker, err := newRepairControllerWorkerWithDependencies(repairControllerWorkerDependencies{
		store:   store,
		journal: store,
		context: func(agentID string) (repairControllerContextLoader, error) {
			fixture.contextAgents = append(fixture.contextAgents, agentID)
			return contextLoader, nil
		},
		verifier: fixture.verifier,
		runtime: func(agentID, _ string) (LocalRepairExecutor, error) {
			fixture.runtimeAgents = append(fixture.runtimeAgents, agentID)
			return executor, nil
		},
		workspaces: func() (repairControllerWorkspace, error) { return workspace, nil },
		localCI:    fixture.ci,
		effects: func(
			controllerOperationJournal,
			repairControllerWorkspace,
			eventing.PRDevelopmentRepairSession,
			eventing.PRDevelopmentControllerLease,
		) (repairControllerEffects, error) {
			return effects, nil
		},
		workerLabel: "test-controller-worker",
		lease:       time.Hour,
	})
	if err != nil {
		t.Fatalf("newRepairControllerWorkerWithDependencies() error = %v", err)
	}
	fixture.worker = worker
	return fixture
}

func TestRepairControllerWorkerNoWork(t *testing.T) {
	store := &repairControllerWorkerStoreFake{}
	worker, err := newRepairControllerWorkerWithDependencies(repairControllerWorkerDependencies{
		store: store, journal: store,
	})
	if err != nil {
		t.Fatalf("newRepairControllerWorkerWithDependencies() error = %v", err)
	}
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || processed {
		t.Fatalf("ProcessOne() = %v, %v, want false, nil", processed, err)
	}
}

func TestRepairControllerWorkerLeaseBoundary(t *testing.T) {
	tests := []struct {
		name      string
		lease     time.Duration
		want      time.Duration
		wantError bool
	}{
		{name: "default", lease: 0, want: defaultRepairLease},
		{name: "minimum", lease: MinimumRepairControllerLease, want: MinimumRepairControllerLease},
		{name: "below minimum", lease: MinimumRepairControllerLease - time.Nanosecond, wantError: true},
		{name: "negative", lease: -time.Second, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			worker, err := NewRepairControllerWorker(RepairControllerWorkerConfig{
				Store:         new(eventing.Store),
				LeaseDuration: test.lease,
			})
			if test.wantError {
				if err == nil || worker != nil {
					t.Fatalf("NewRepairControllerWorker() = %#v, %v, want error", worker, err)
				}
				return
			}
			gotLease := time.Duration(0)
			if worker != nil {
				gotLease = worker.lease
			}
			if err != nil || worker == nil || gotLease != test.want {
				t.Fatalf("NewRepairControllerWorker() = %#v, %v, lease = %v, want %v",
					worker, err, gotLease, test.want)
			}
		})
	}
}

func TestRepairControllerWorkerRejectsInvalidLeaseBeforeClaim(t *testing.T) {
	store := &repairControllerWorkerStoreFake{}
	worker := &RepairControllerWorker{repairControllerWorkerDependencies: repairControllerWorkerDependencies{
		store: store,
		lease: MinimumRepairControllerLease - time.Nanosecond,
	}}
	processed, err := worker.ProcessOne(context.Background())
	if processed || !errors.Is(err, ErrUnavailable) || store.claimCalls != 0 {
		t.Fatalf("ProcessOne() = %v, %v, claims = %d, want rejected before claim",
			processed, err, store.claimCalls)
	}
}

func TestRepairControllerWorkerChangedNoChangeAndNonGreen(t *testing.T) {
	tests := []struct {
		name       string
		changed    bool
		status     localci.Status
		wantStatus eventing.PRDevelopmentCIStatus
	}{
		{"changed green", true, localci.StatusPassed, eventing.PRDevelopmentCIPassed},
		{"no change green", false, localci.StatusPassed, eventing.PRDevelopmentCIPassed},
		{"changed failed", true, localci.StatusFailed, eventing.PRDevelopmentCIFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRepairControllerWorkerFixture(
				t,
				eventing.PRDevelopmentRepairOrchestrationBootstrap,
				test.changed,
				test.status,
			)
			processed, err := fixture.worker.ProcessOne(context.Background())
			if err != nil || !processed {
				t.Fatalf("ProcessOne() = %v, %v, want true, nil", processed, err)
			}
			if fixture.executor.calls != 1 || fixture.effects.adopts != 1 ||
				fixture.effects.resumes != 0 || fixture.ci.calls != 1 ||
				len(fixture.store.validations) != 1 || len(fixture.effects.commits) != 1 ||
				fixture.effects.parks != 1 {
				t.Fatalf("normal flow counts = model %d adopt %d resume %d ci %d validation %d commit %d park %d",
					fixture.executor.calls, fixture.effects.adopts, fixture.effects.resumes,
					fixture.ci.calls, len(fixture.store.validations), len(fixture.effects.commits),
					fixture.effects.parks)
			}
			validation := fixture.store.validations[0]
			if validation.CIStatus != test.wantStatus ||
				validation.NoChanges != !test.changed ||
				fixture.ci.requests[0].Candidate.NoChanges != !test.changed {
				t.Fatalf("validation = %#v, CI request = %#v", validation, fixture.ci.requests[0])
			}
			if got := fixture.executor.request.Pin.ReservationKey; got != controllerWorkerReservation {
				t.Fatalf("model reservation = %q, want %q", got, controllerWorkerReservation)
			}
			if got := fixture.contextAgents; len(got) != 1 || got[0] != "pinned-agent" {
				t.Fatalf("context agents = %#v, want pinned-agent", got)
			}
			if got := fixture.runtimeAgents; len(got) != 1 || got[0] != "pinned-agent" {
				t.Fatalf("runtime agents = %#v, want pinned-agent", got)
			}
		})
	}
}

func TestRepairControllerWorkerResumesSuspendedLineBeforeModel(t *testing.T) {
	fixture := newRepairControllerWorkerFixture(
		t,
		eventing.PRDevelopmentRepairOrchestrationBootstrap,
		false,
		localci.StatusPassed,
	)
	now := time.Now().UTC()
	claimUntil := now.Add(time.Hour)
	resumeReservation := "pdck_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	suspensionHash := controllerWorkerDigest("a")
	candidateDigest := controllerWorkerDigest("4")
	prior := eventing.PRDevelopmentController{
		ID:               controllerWorkerControllerID,
		ThreadID:         controllerWorkerThreadID,
		OwnerSessionID:   controllerWorkerSessionID,
		AgentID:          "pinned-agent",
		Revision:         4,
		Phase:            eventing.PRDevelopmentControllerSuspended,
		WorkspaceID:      "workspace-1",
		LineID:           controllerWorkerLineID,
		CurrentAttemptID: "pdr_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		SourceCloneURL:   "https://github.com/owner/repo.git",
		SourceRef:        "feature",
		SourceCommit:     controllerWorkerHead,
		SourceTree:       controllerWorkerSourceTree,
		LineVersion:      0,
		MutationEpoch:    1,
		TipCommit:        controllerWorkerHead,
		Tree:             controllerWorkerSourceTree,
		LeaseEpoch:       1,
	}
	prepared := prior
	prepared.Revision++
	prepared.CurrentAttemptID = controllerWorkerAttemptID
	suspension := eventing.PRDevelopmentControllerSuspension{
		ID:                      "pdsi_cccccccccccccccccccccccccccccccc",
		ControllerID:            controllerWorkerControllerID,
		ThreadID:                controllerWorkerThreadID,
		OwnerSessionID:          controllerWorkerSessionID,
		AttemptID:               prior.CurrentAttemptID,
		Status:                  eventing.PRDevelopmentControllerSuspensionStatusResumeClaimed,
		AgentID:                 "pinned-agent",
		WorkspaceID:             "workspace-1",
		LineID:                  controllerWorkerLineID,
		SourceCloneURL:          prior.SourceCloneURL,
		SourceRef:               prior.SourceRef,
		SourceCommit:            prior.SourceCommit,
		SourceTree:              prior.SourceTree,
		LineVersion:             prior.LineVersion,
		MutationEpoch:           prior.MutationEpoch,
		TipCommit:               prior.TipCommit,
		Tree:                    prior.Tree,
		FinalSuspensionRevision: prior.Revision,
		ResumeAttemptID:         controllerWorkerAttemptID,
		ResumeIntentID:          "pdsri_dddddddddddddddddddddddddddddddd",
		ResumeReservationKey:    resumeReservation,
		ResumeClaimID:           "pdsrc_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		ResumeClaimToken:        "resume-claim-token",
		ResumeClaimUntil:        &claimUntil,
		ResumeClaimEpoch:        2,
		SuspendResult: eventing.PRDevelopmentControllerSuspensionResult{
			WorkspaceID:      "workspace-1",
			Version:          prior.LineVersion,
			MutationEpoch:    prior.MutationEpoch,
			Tip:              prior.TipCommit,
			Tree:             prior.Tree,
			CandidateTree:    controllerWorkerSourceTree,
			CandidateDigest:  candidateDigest,
			ChangedFileCount: 0,
			SuspensionHash:   suspensionHash,
		},
	}
	suspension.ResumeRequest = eventing.PRDevelopmentControllerSuspendedResumeRequest{
		Repository:            prior.SourceCloneURL,
		SourceRef:             prior.SourceRef,
		SourceCommit:          prior.SourceCommit,
		ReservationKey:        resumeReservation,
		AgentID:               prior.AgentID,
		WorkspaceID:           prior.WorkspaceID,
		LineID:                prior.LineID,
		IntentID:              suspension.ResumeIntentID,
		ExpectedVersion:       prior.LineVersion,
		ExpectedMutationEpoch: prior.MutationEpoch,
		ExpectedTip:           prior.TipCommit,
		ExpectedTree:          prior.Tree,
		SuspensionHash:        suspensionHash,
		CandidateTree:         suspension.SuspendResult.CandidateTree,
		CandidateDigest:       candidateDigest,
		ChangedFileCount:      0,
	}
	fixture.store.controller = prior
	fixture.store.controllerErr = nil
	fixture.store.lease = eventing.PRDevelopmentControllerLease{
		Controller: prepared,
		SuspendedResume: &eventing.PRDevelopmentControllerSuspendedResumeLease{
			Controller: prepared,
			Suspension: suspension,
		},
	}
	resumed := prepared
	resumed.Revision++
	resumed.Phase = eventing.PRDevelopmentControllerMutation
	resumed.LeaseKind = eventing.PRDevelopmentControllerMutationLease
	resumed.LeaseToken = controllerWorkerLeaseToken
	resumed.LeaseUntil = &claimUntil
	resumed.LeaseEpoch++
	resumed.MutationReservationKey = resumeReservation
	fixture.store.resumeFinalLease = eventing.PRDevelopmentControllerLease{Controller: resumed}
	fixture.effects.line.Revision = resumed.Revision
	fixture.effects.line.MutationEpoch = resumed.MutationEpoch
	fixture.workspace.resumeResults = []gitworkspace.PinnedLineSuspendedResumeResult{{
		WorkspaceID:      prior.WorkspaceID,
		Version:          prior.LineVersion,
		MutationEpoch:    prior.MutationEpoch,
		Tip:              prior.TipCommit,
		Tree:             prior.Tree,
		CandidateTree:    suspension.SuspendResult.CandidateTree,
		CandidateDigest:  candidateDigest,
		ChangedFileCount: 0,
		SuspensionHash:   suspensionHash,
		RotationHash:     controllerWorkerDigest("b"),
	}}
	session := fixture.store.workbench.RepairSession
	session.HeadRepository = "owner/repo"
	session.HeadRef = prior.SourceRef
	session.HeadSHA = prior.SourceCommit
	session.CloneURL = prior.SourceCloneURL
	session.ReviewDigest = controllerWorkerDigest("e")
	session.WorkspaceID = prior.WorkspaceID
	events := []string{}
	fixture.store.events = &events
	fixture.workspace.events = &events
	fixture.context.events = &events
	fixture.executor.events = &events

	processed, err := fixture.worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = %v, %v, want true, nil", processed, err)
	}
	if got := strings.Join(events[:4], ","); got != "resume-git,resume-finalize,context,model" {
		t.Fatalf("resume/model order = %q", got)
	}
	if len(fixture.workspace.resumeRequests) != 1 ||
		len(fixture.store.resumeFinalizes) != 1 || fixture.executor.calls != 1 {
		t.Fatalf(
			"resume flow counts = git %d, finalize %d, model %d",
			len(fixture.workspace.resumeRequests), len(fixture.store.resumeFinalizes),
			fixture.executor.calls,
		)
	}
	if fixture.effects.adopts != 0 || fixture.effects.resumes != 0 {
		t.Fatalf("ordinary line effects ran after suspended resume: adopt %d, resume %d",
			fixture.effects.adopts, fixture.effects.resumes)
	}
	if got := fixture.executor.request.Pin.ReservationKey; got != resumeReservation {
		t.Fatalf("model reservation = %q, want resumed %q", got, resumeReservation)
	}
	finalize := fixture.store.resumeFinalizes[0]
	if finalize.ExpectedRevision != prepared.Revision ||
		finalize.OrchestrationClaimToken != fixture.store.run.ClaimToken ||
		finalize.ClaimToken != suspension.ResumeClaimToken ||
		finalize.Result.RotationHash != controllerWorkerDigest("b") {
		t.Fatalf("resume finalization = %#v", finalize)
	}
}

func TestValidateRepairControllerLeaseRejectsSuspendedResumeBypass(t *testing.T) {
	claim := repairControllerClaim{
		session: eventing.PRDevelopmentRepairSession{
			ID:          controllerWorkerSessionID,
			AgentID:     "pinned-agent",
			WorkspaceID: "workspace-1",
		},
		attempt: eventing.PRDevelopmentRepairAttempt{ID: controllerWorkerAttemptID},
	}
	run := eventing.PRDevelopmentRepairOrchestration{
		AttemptID:   controllerWorkerAttemptID,
		ThreadID:    controllerWorkerThreadID,
		WorkspaceID: "workspace-1",
		CloneURL:    "https://github.com/owner/repo.git",
		HeadRef:     "feature",
		HeadSHA:     controllerWorkerHead,
		SourceTree:  controllerWorkerSourceTree,
	}
	prior := eventing.PRDevelopmentController{
		ID:       controllerWorkerControllerID,
		Phase:    eventing.PRDevelopmentControllerSuspended,
		Revision: 4,
	}
	controller := eventing.PRDevelopmentController{
		ID:                     controllerWorkerControllerID,
		ThreadID:               controllerWorkerThreadID,
		OwnerSessionID:         controllerWorkerSessionID,
		AgentID:                "pinned-agent",
		Revision:               6,
		Phase:                  eventing.PRDevelopmentControllerMutation,
		CurrentAttemptID:       controllerWorkerAttemptID,
		LeaseKind:              eventing.PRDevelopmentControllerMutationLease,
		LeaseToken:             controllerWorkerLeaseToken,
		LeaseEpoch:             2,
		MutationReservationKey: controllerWorkerRotatedKey,
		WorkspaceID:            "workspace-1",
		SourceCloneURL:         run.CloneURL,
		SourceRef:              run.HeadRef,
		SourceCommit:           run.HeadSHA,
		SourceTree:             run.SourceTree,
		LineID:                 controllerWorkerLineID,
		LineVersion:            0,
		MutationEpoch:          1,
		TipCommit:              controllerWorkerHead,
		Tree:                   controllerWorkerSourceTree,
	}
	lease := eventing.PRDevelopmentControllerLease{Controller: controller}
	err := validateRepairControllerLease(claim, run, prior, true, false, lease)
	if err == nil || !strings.Contains(err.Error(), "bypassed exact resume") {
		t.Fatalf("validateRepairControllerLease() error = %v, want resume bypass", err)
	}
	if err = validateRepairControllerLease(claim, run, prior, true, true, lease); err != nil {
		t.Fatalf("validateRepairControllerLease() after exact resume error = %v", err)
	}
}

func TestValidateRepairControllerSuspendedResumeRejectsNestedControllerDrift(t *testing.T) {
	outer := eventing.PRDevelopmentController{
		ID:               controllerWorkerControllerID,
		ThreadID:         controllerWorkerThreadID,
		OwnerSessionID:   controllerWorkerSessionID,
		AgentID:          "pinned-agent",
		Revision:         5,
		Phase:            eventing.PRDevelopmentControllerSuspended,
		CurrentAttemptID: controllerWorkerAttemptID,
	}
	nested := outer
	nested.Revision++
	err := validateRepairControllerSuspendedResumeLease(
		repairControllerClaim{
			session: eventing.PRDevelopmentRepairSession{
				ID:      controllerWorkerSessionID,
				AgentID: "pinned-agent",
			},
			attempt: eventing.PRDevelopmentRepairAttempt{ID: controllerWorkerAttemptID},
		},
		eventing.PRDevelopmentRepairOrchestration{
			AttemptID: controllerWorkerAttemptID,
			ThreadID:  controllerWorkerThreadID,
		},
		outer,
		true,
		eventing.PRDevelopmentControllerLease{
			Controller: outer,
			SuspendedResume: &eventing.PRDevelopmentControllerSuspendedResumeLease{
				Controller: nested,
				Suspension: eventing.PRDevelopmentControllerSuspension{
					ControllerID: controllerWorkerControllerID,
				},
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "incomplete or changed") {
		t.Fatalf("validateRepairControllerSuspendedResumeLease() error = %v, want drift", err)
	}
}

func TestRepairControllerWorkerReleasesPinOnBootstrapFailure(t *testing.T) {
	fixture := newRepairControllerWorkerFixture(
		t,
		eventing.PRDevelopmentRepairOrchestrationBootstrap,
		true,
		localci.StatusPassed,
	)
	events := []string{}
	fixture.store.events = &events
	fixture.workspace.events = &events
	fixture.workspace.snapshotErr = errors.New("clean snapshot failed")
	processed, err := fixture.worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = %v, %v, want true, nil", processed, err)
	}
	if len(fixture.store.failures) != 1 || len(fixture.workspace.releases) != 1 {
		t.Fatalf("failures = %d, releases = %d, want one each",
			len(fixture.store.failures), len(fixture.workspace.releases))
	}
	if got := strings.Join(events, ","); got != "release,fail" {
		t.Fatalf("bootstrap cleanup order = %q, want release,fail", got)
	}
	if got := fixture.workspace.releases[0].ReservationKey; got != controllerWorkerReservation {
		t.Fatalf("released reservation = %q, want %q", got, controllerWorkerReservation)
	}
	if len(fixture.store.acquireCalls) != 0 || fixture.executor.calls != 0 {
		t.Fatalf("controller/model ran after bootstrap failure")
	}
}

func TestRepairControllerWorkerDoesNotFailOrReleaseAmbiguousPin(t *testing.T) {
	fixture := newRepairControllerWorkerFixture(
		t,
		eventing.PRDevelopmentRepairOrchestrationBootstrap,
		true,
		localci.StatusPassed,
	)
	fixture.store.pinErr = errors.New("ambiguous pin persistence")
	processed, err := fixture.worker.ProcessOne(context.Background())
	if !processed || err == nil {
		t.Fatalf("ProcessOne() = %v, %v, want ambiguous pin error", processed, err)
	}
	if len(fixture.store.failures) != 0 || len(fixture.workspace.releases) != 0 ||
		len(fixture.store.acquireCalls) != 0 {
		t.Fatalf("ambiguous Pin changed lifecycle: failures=%d releases=%d controllers=%d",
			len(fixture.store.failures), len(fixture.workspace.releases),
			len(fixture.store.acquireCalls))
	}
}

func TestRepairControllerWorkerAcquireErrorReleasesBeforeFail(t *testing.T) {
	fixture := newRepairControllerWorkerFixture(
		t,
		eventing.PRDevelopmentRepairOrchestrationBootstrap,
		true,
		localci.StatusPassed,
	)
	events := []string{}
	fixture.store.events = &events
	fixture.workspace.events = &events
	fixture.workspace.acquireErr = errors.New("result projection failed")
	processed, err := fixture.worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = %v, %v, want safe failure", processed, err)
	}
	if got := strings.Join(events, ","); got != "release,fail" {
		t.Fatalf("Acquire cleanup order = %q, want release,fail", got)
	}
}

func TestRepairControllerWorkerReleaseFailureLeavesBootstrapReclaimable(t *testing.T) {
	fixture := newRepairControllerWorkerFixture(
		t,
		eventing.PRDevelopmentRepairOrchestrationBootstrap,
		true,
		localci.StatusPassed,
	)
	fixture.workspace.snapshotErr = errors.New("clean snapshot failed")
	fixture.workspace.releaseErr = errors.New("release ambiguous")
	processed, err := fixture.worker.ProcessOne(context.Background())
	if !processed || err == nil {
		t.Fatalf("ProcessOne() = %v, %v, want release error", processed, err)
	}
	if len(fixture.store.failures) != 0 ||
		fixture.store.run.Phase != eventing.PRDevelopmentRepairOrchestrationBootstrap {
		t.Fatalf("release failure terminalized Bootstrap: failures=%d phase=%q",
			len(fixture.store.failures), fixture.store.run.Phase)
	}
}

func TestRepairControllerWorkerCancellationReleasesPossiblePrePinOwner(t *testing.T) {
	fixture := newRepairControllerWorkerFixture(
		t,
		eventing.PRDevelopmentRepairOrchestrationBootstrap,
		true,
		localci.StatusPassed,
	)
	ctx, cancel := context.WithCancel(context.Background())
	fixture.workspace.acquireHook = cancel
	fixture.workspace.acquireErr = context.Canceled
	processed, err := fixture.worker.ProcessOne(ctx)
	if !processed || !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessOne() = %v, %v, want cancellation", processed, err)
	}
	if len(fixture.workspace.releases) != 1 || len(fixture.store.failures) != 0 ||
		fixture.store.run.Phase != eventing.PRDevelopmentRepairOrchestrationBootstrap {
		t.Fatalf("canceled pre-Pin cleanup = releases %d failures %d phase %q",
			len(fixture.workspace.releases), len(fixture.store.failures), fixture.store.run.Phase)
	}
}

func TestRepairControllerWorkerUsesFreshReservationAndSkipsModelOnEditedResume(t *testing.T) {
	fixture := newRepairControllerWorkerFixture(
		t,
		eventing.PRDevelopmentRepairOrchestrationEdited,
		true,
		localci.StatusPassed,
	)
	fixture.worker.context = nil
	fixture.worker.runtime = nil
	processed, err := fixture.worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = %v, %v, want true, nil", processed, err)
	}
	if fixture.executor.calls != 0 || fixture.context.calls != 0 ||
		len(fixture.store.starts) != 0 || len(fixture.store.completes) != 0 {
		t.Fatalf("edited checkpoint reran model/context: model=%d context=%d start=%d complete=%d",
			fixture.executor.calls, fixture.context.calls,
			len(fixture.store.starts), len(fixture.store.completes))
	}
	if got := fixture.workspace.snapshotRequests[0].Pin.ReservationKey; got != controllerWorkerRotatedKey {
		t.Fatalf("snapshot reservation = %q, want %q", got, controllerWorkerRotatedKey)
	}
	if got := fixture.ci.requests[0].Candidate.Pin.ReservationKey; got != controllerWorkerRotatedKey {
		t.Fatalf("CI reservation = %q, want %q", got, controllerWorkerRotatedKey)
	}
}

func TestRepairControllerWorkerValidatedResumeSkipsModelAndCI(t *testing.T) {
	fixture := newRepairControllerWorkerFixture(
		t,
		eventing.PRDevelopmentRepairOrchestrationValidated,
		true,
		localci.StatusPassed,
	)
	fixture.store.run.Validation = &eventing.PRDevelopmentRepairValidationReceipt{
		ControllerID:              controllerWorkerControllerID,
		WorkspaceID:               "workspace-1",
		ModelControllerRevision:   2,
		ModelLineID:               controllerWorkerLineID,
		ModelLineVersion:          0,
		ModelMutationEpoch:        1,
		ModelMutationLeaseEpoch:   1,
		ModelLeaseTokenDigest:     controllerWorkerDigest("6"),
		ModelReservationDigest:    controllerWorkerDigest("7"),
		ContextDigest:             fixture.store.run.ContextDigest,
		PromptDigest:              fixture.store.run.PromptDigest,
		LineID:                    controllerWorkerLineID,
		ControllerRevision:        2,
		LineVersion:               0,
		MutationEpoch:             1,
		MutationLeaseEpoch:        1,
		MutationLeaseTokenDigest:  controllerWorkerDigest("8"),
		MutationReservationDigest: controllerWorkerDigest("9"),
		ParentCommit:              controllerWorkerHead,
		ParentTree:                controllerWorkerSourceTree,
		CandidateTree:             controllerWorkerCandidate,
		CandidateDigest:           controllerWorkerDigest("5"),
		ChangedFiles:              1,
		NoChanges:                 false,
		CIStatus:                  eventing.PRDevelopmentCIPassed,
		CIAttestationID:           "pr-development:ci:attestation:" + controllerWorkerAttemptID,
		CIAttestationDigest:       controllerWorkerDigest("d"),
		CIResultKey:               controllerWorkerDigest("c"),
		CIEffectivePlanDigest:     controllerWorkerDigest("a"),
		CIExecutionDigest:         controllerWorkerDigest("b"),
		ModelResultDigest:         fixture.store.run.ModelResultDigest,
		ModelSummary:              fixture.store.run.Summary,
		ModelIterations:           fixture.store.run.Iterations,
		ReceiptHash:               controllerWorkerDigest("f"),
	}
	fixture.worker.context = nil
	fixture.worker.runtime = nil
	fixture.worker.localCI = nil
	processed, err := fixture.worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = %v, %v, want true, nil", processed, err)
	}
	if fixture.executor.calls != 0 || fixture.ci.calls != 0 ||
		len(fixture.workspace.snapshotRequests) != 0 || fixture.effects.parks != 1 {
		t.Fatalf("validated resume effects = model %d ci %d snapshots %d park %d",
			fixture.executor.calls, fixture.ci.calls,
			len(fixture.workspace.snapshotRequests), fixture.effects.parks)
	}
}

func TestRepairControllerWorkerProviderDriftDoesNotMutate(t *testing.T) {
	fixture := newRepairControllerWorkerFixture(
		t,
		eventing.PRDevelopmentRepairOrchestrationEdited,
		true,
		localci.StatusPassed,
	)
	fixture.verifier.verified.HeadSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	processed, err := fixture.worker.ProcessOne(context.Background())
	if !processed || err == nil {
		t.Fatalf("ProcessOne() = %v, %v, want true, provider drift error", processed, err)
	}
	if len(fixture.store.acquireCalls) != 0 || fixture.executor.calls != 0 ||
		fixture.ci.calls != 0 || fixture.effects.parks != 0 {
		t.Fatal("provider drift reached controller mutation")
	}
}

func TestRepairControllerWorkerRejectsMismatchedCIAttestation(t *testing.T) {
	fixture := newRepairControllerWorkerFixture(
		t,
		eventing.PRDevelopmentRepairOrchestrationBootstrap,
		true,
		localci.StatusPassed,
	)
	fixture.ci.tamper = func(result *localci.RunResult) {
		result.Attestation.ExecutionDigest = controllerWorkerDigest("9")
	}
	processed, err := fixture.worker.ProcessOne(context.Background())
	if !processed || err == nil {
		t.Fatalf("ProcessOne() = %v, %v, want bound-attestation error", processed, err)
	}
	if len(fixture.store.validations) != 0 || len(fixture.effects.commits) != 0 ||
		fixture.effects.parks != 0 {
		t.Fatal("mismatched CI evidence reached Commit/Park")
	}
}

func TestRepairControllerWorkerHeartbeatLossCancelsModel(t *testing.T) {
	fixture := newRepairControllerWorkerFixture(
		t,
		eventing.PRDevelopmentRepairOrchestrationBootstrap,
		true,
		localci.StatusPassed,
	)
	fixture.store.renewErr = errors.New("claim lost")
	fixture.executor.waitForCancel = true
	fixture.worker.lease = MinimumRepairControllerLease
	processed, err := fixture.worker.ProcessOne(context.Background())
	if !processed || err == nil || !strings.Contains(err.Error(), "claim lost") {
		t.Fatalf("ProcessOne() = %v, %v, want heartbeat loss", processed, err)
	}
	if fixture.executor.calls != 1 || fixture.ci.calls != 0 || fixture.effects.parks != 0 {
		t.Fatalf("heartbeat loss flow = model %d ci %d park %d",
			fixture.executor.calls, fixture.ci.calls, fixture.effects.parks)
	}
}

func TestRepairControllerWorkerMissingRuntimeFailsFreshBootstrap(t *testing.T) {
	fixture := newRepairControllerWorkerFixture(
		t,
		eventing.PRDevelopmentRepairOrchestrationBootstrap,
		true,
		localci.StatusPassed,
	)
	fixture.worker.runtime = nil
	processed, err := fixture.worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = %v, %v, want safe failure", processed, err)
	}
	if len(fixture.store.failures) != 1 ||
		fixture.store.failures[0].ErrorCode != eventing.PRDevelopmentRepairErrorRuntimeUnavailable {
		t.Fatalf("failures = %#v", fixture.store.failures)
	}
}

func TestValidateRepairControllerCIResultMapsEveryTerminalStatus(t *testing.T) {
	identities, err := newControllerAttemptIdentities(controllerWorkerAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	candidate := gitworkspace.PinnedCandidate{
		WorkspaceID:     "workspace-1",
		ParentCommit:    controllerWorkerHead,
		Tree:            controllerWorkerCandidate,
		CandidateDigest: controllerWorkerDigest("5"),
		ChangedFiles:    1,
	}
	statuses := map[localci.Status]eventing.PRDevelopmentCIStatus{
		localci.StatusPassed:                 eventing.PRDevelopmentCIPassed,
		localci.StatusFailed:                 eventing.PRDevelopmentCIFailed,
		localci.StatusIncomplete:             eventing.PRDevelopmentCIIncomplete,
		localci.StatusPlanChanged:            eventing.PRDevelopmentCIPlanChanged,
		localci.StatusTimedOut:               eventing.PRDevelopmentCITimedOut,
		localci.StatusCanceled:               eventing.PRDevelopmentCICanceled,
		localci.StatusOutputLimitExceeded:    eventing.PRDevelopmentCIOutputLimitExceeded,
		localci.StatusEnvironmentUnavailable: eventing.PRDevelopmentCIEnvironmentUnavailable,
		localci.StatusInfrastructureError:    eventing.PRDevelopmentCIInfrastructureError,
	}
	for status, want := range statuses {
		t.Run(string(status), func(t *testing.T) {
			ci := &repairControllerCIFake{status: status}
			result, runErr := ci.RunPinned(
				context.Background(),
				&repairControllerWorkspaceFake{},
				localci.PinnedRunRequest{
					AttestationID: identities.CIAttestation,
					OwnerID:       identities.CIOwner,
					Candidate: gitworkspace.PinnedCandidateValidationRequest{
						ExpectedParent:          candidate.ParentCommit,
						ExpectedTree:            candidate.Tree,
						ExpectedCandidateDigest: candidate.CandidateDigest,
					},
				},
			)
			if runErr != nil {
				t.Fatal(runErr)
			}
			got, validateErr := validateRepairControllerCIResult(result, identities, candidate)
			if validateErr != nil || got != want {
				t.Fatalf("validateRepairControllerCIResult() = %q, %v, want %q", got, validateErr, want)
			}
		})
	}
}
