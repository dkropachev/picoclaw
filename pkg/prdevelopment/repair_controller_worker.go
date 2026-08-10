package prdevelopment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prdevelopment/localci"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	defaultRepairControllerWorkerLabel = "gateway-pr-development-controller"

	// MinimumRepairControllerLease keeps the first heartbeat at or before one
	// third of the lease even though controllerHeartbeat deliberately floors
	// its ticker at one second. Shorter leases are rejected instead of risking
	// expiration before a delayed first renewal.
	MinimumRepairControllerLease = 3 * time.Second
)

// RepairControllerWorkspaceFactory resolves the concrete Git manager owned by
// the caller's current runtime generation. The caller must keep that generation
// leased for the complete ProcessOne call.
type RepairControllerWorkspaceFactory func() (*gitworkspace.Manager, error)

// RepairControllerWorkerConfig contains only trusted, process-local
// dependencies. Neither the browser nor a workflow can construct or invoke a
// controller worker.
type RepairControllerWorkerConfig struct {
	Store              *eventing.Store
	Verifier           RepairCaseVerifier
	Runtime            RepairRuntimeFactory
	Workspaces         RepairControllerWorkspaceFactory
	LocalCI            *localci.Runner
	ContextAgent       workflows.AgentRunner
	ContextCompactorID string
	WorkerLabel        string
	// LeaseDuration defaults to five minutes. A nonzero value must be at least
	// MinimumRepairControllerLease.
	LeaseDuration time.Duration
}

type repairControllerStateStore interface {
	eventing.PRDevelopmentWorkbenchReader
	eventing.PRDevelopmentRepairOrchestrationStore
	eventing.PRDevelopmentControllerReader
	eventing.PRDevelopmentControllerSuspendedResumeStore
	controllerHeartbeatStore
}

type repairControllerContextLoader interface {
	Load(ctx context.Context, caseID string, conversationVersion int64) (string, error)
}

type repairControllerWorkspace interface {
	AcquirePinned(
		ctx context.Context,
		request gitworkspace.PinnedAcquireRequest,
	) (gitworkspace.WorkspaceInfo, error)
	ReleasePinned(
		ctx context.Context,
		request gitworkspace.PinnedReleaseRequest,
	) ([]gitworkspace.WorkspaceInfo, error)
	SnapshotPinnedValidationCandidate(
		ctx context.Context,
		request gitworkspace.PinnedCandidateRequest,
	) (gitworkspace.PinnedCandidate, error)
	ResumeSuspendedPinnedLine(
		ctx context.Context,
		request gitworkspace.PinnedLineSuspendedResumeRequest,
	) (gitworkspace.PinnedLineSuspendedResumeResult, error)
}

type repairControllerLocalCI interface {
	RunPinned(
		ctx context.Context,
		workspace repairControllerWorkspace,
		request localci.PinnedRunRequest,
	) (localci.RunResult, error)
}

type repairControllerEffects interface {
	Adopt(ctx context.Context, expectedSourceTree string) (controllerLineState, error)
	Resume(ctx context.Context) (controllerLineState, error)
	CommitCandidate(
		ctx context.Context,
		candidate gitworkspace.PinnedCandidate,
		message string,
	) (controllerCommitOutcome, error)
	Park(
		ctx context.Context,
		commit controllerCommitOutcome,
		summary string,
		iterations int,
		terminal func(),
	) (eventing.PRDevelopmentAttemptReviewFence, error)
}

type repairControllerEffectFactory func(
	journal controllerOperationJournal,
	workspace repairControllerWorkspace,
	session eventing.PRDevelopmentRepairSession,
	lease eventing.PRDevelopmentControllerLease,
) (repairControllerEffects, error)

type repairControllerWorkerDependencies struct {
	store       repairControllerStateStore
	journal     controllerOperationJournal
	context     func(string) (repairControllerContextLoader, error)
	verifier    RepairCaseVerifier
	runtime     RepairRuntimeFactory
	workspaces  func() (repairControllerWorkspace, error)
	localCI     repairControllerLocalCI
	effects     repairControllerEffectFactory
	workerLabel string
	lease       time.Duration
}

// RepairControllerWorker executes at most one provider-thread repair attempt
// per call. The model edits locally; the worker owns provider fencing, local
// CI, deterministic Commit, and atomic Park-to-ledger finalization.
type RepairControllerWorker struct {
	repairControllerWorkerDependencies
}

type productionRepairControllerCI struct {
	runner *localci.Runner
}

func (ci productionRepairControllerCI) RunPinned(
	ctx context.Context,
	workspace repairControllerWorkspace,
	request localci.PinnedRunRequest,
) (localci.RunResult, error) {
	manager, ok := workspace.(*gitworkspace.Manager)
	if !ok || manager == nil || ci.runner == nil {
		return localci.RunResult{}, fmt.Errorf(
			"%w: local CI requires the concrete controller Git manager",
			ErrUnavailable,
		)
	}
	return ci.runner.RunPinned(ctx, manager, request)
}

// NewRepairControllerWorker builds the production trusted worker and its
// package-private bounded thread-context loader.
func NewRepairControllerWorker(
	config RepairControllerWorkerConfig,
) (*RepairControllerWorker, error) {
	if config.Store == nil {
		return nil, fmt.Errorf("%w: repair controller store is required", ErrUnavailable)
	}
	label := strings.TrimSpace(config.WorkerLabel)
	if label == "" {
		label = defaultRepairControllerWorkerLabel
	}
	if label != config.WorkerLabel && config.WorkerLabel != "" {
		return nil, errors.New("repair controller worker label must be exact")
	}
	lease, err := normalizeRepairControllerLease(config.LeaseDuration)
	if err != nil {
		return nil, err
	}
	if lease == 0 {
		lease = defaultRepairLease
	}
	dependencies := repairControllerWorkerDependencies{
		store:       config.Store,
		journal:     config.Store,
		verifier:    config.Verifier,
		runtime:     config.Runtime,
		effects:     productionRepairControllerEffectFactory,
		workerLabel: label,
		lease:       lease,
	}
	if config.ContextAgent != nil {
		dependencies.context = func(agentID string) (repairControllerContextLoader, error) {
			return newDevelopmentThreadContextLoader(developmentThreadContextLoaderConfig{
				Store:       config.Store,
				Agent:       config.ContextAgent,
				AgentID:     agentID,
				CompactorID: config.ContextCompactorID,
			})
		}
	}
	if config.Workspaces != nil {
		dependencies.workspaces = func() (repairControllerWorkspace, error) {
			manager, resolveErr := config.Workspaces()
			if resolveErr != nil {
				return nil, resolveErr
			}
			if manager == nil {
				return nil, errors.New("controller Git workspace manager is unavailable")
			}
			return manager, nil
		}
	}
	if config.LocalCI != nil {
		dependencies.localCI = productionRepairControllerCI{runner: config.LocalCI}
	}
	return &RepairControllerWorker{repairControllerWorkerDependencies: dependencies}, nil
}

func productionRepairControllerEffectFactory(
	journal controllerOperationJournal,
	workspace repairControllerWorkspace,
	session eventing.PRDevelopmentRepairSession,
	lease eventing.PRDevelopmentControllerLease,
) (repairControllerEffects, error) {
	manager, ok := workspace.(*gitworkspace.Manager)
	if !ok || manager == nil {
		return nil, errors.New("controller effects require the concrete Git workspace manager")
	}
	return newControllerEffectRunner(journal, manager, session, lease)
}

func newRepairControllerWorkerWithDependencies(
	dependencies repairControllerWorkerDependencies,
) (*RepairControllerWorker, error) {
	if dependencies.store == nil || dependencies.journal == nil {
		return nil, fmt.Errorf("%w: repair controller store is incomplete", ErrUnavailable)
	}
	if dependencies.workerLabel == "" {
		dependencies.workerLabel = defaultRepairControllerWorkerLabel
	}
	lease, err := normalizeRepairControllerLease(dependencies.lease)
	if err != nil {
		return nil, err
	}
	if lease == 0 {
		dependencies.lease = defaultRepairLease
	} else {
		dependencies.lease = lease
	}
	return &RepairControllerWorker{repairControllerWorkerDependencies: dependencies}, nil
}

func normalizeRepairControllerLease(lease time.Duration) (time.Duration, error) {
	if lease == 0 {
		return 0, nil
	}
	if lease < MinimumRepairControllerLease {
		return 0, fmt.Errorf(
			"repair controller lease must be zero or at least %s",
			MinimumRepairControllerLease,
		)
	}
	return lease, nil
}

// ProcessOne claims and processes at most one provider-thread attempt.
func (worker *RepairControllerWorker) ProcessOne(ctx context.Context) (bool, error) {
	if worker == nil || worker.store == nil {
		return false, ErrUnavailable
	}
	if worker.lease < MinimumRepairControllerLease {
		return false, fmt.Errorf(
			"%w: repair controller lease is below %s",
			ErrUnavailable,
			MinimumRepairControllerLease,
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	run, claimed, err := worker.store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		eventing.PRDevelopmentRepairOrchestrationClaim{
			WorkerLabel: worker.workerLabel,
			Lease:       worker.lease,
		},
	)
	if err != nil || !claimed {
		return claimed, err
	}
	workCtx, heartbeat := startControllerHeartbeat(
		ctx,
		worker.store,
		run.AttemptID,
		run.ClaimToken,
		worker.lease,
	)
	processErr := worker.processClaim(workCtx, heartbeat, run)
	heartbeatErr := heartbeat.Stop()
	if heartbeatErr != nil {
		if processErr != nil {
			return true, fmt.Errorf("%v; controller heartbeat: %w", processErr, heartbeatErr)
		}
		return true, heartbeatErr
	}
	return true, processErr
}

type repairControllerClaim struct {
	run       eventing.PRDevelopmentRepairOrchestration
	workbench eventing.PRDevelopmentWorkbench
	session   eventing.PRDevelopmentRepairSession
	attempt   eventing.PRDevelopmentRepairAttempt
	context   string
	verified  VerifiedCase
}

type repairControllerLineFence struct {
	controllerID  string
	revision      int64
	leaseToken    string
	leaseEpoch    int64
	reservation   string
	workspaceID   string
	lineID        string
	lineVersion   int64
	mutationEpoch int64
	tip           string
	tree          string
}

type repairControllerBaselineState uint8

const (
	repairControllerBaselineUnchanged repairControllerBaselineState = iota
	repairControllerBaselineAcquiredUnpinned
	repairControllerBaselinePinAttempted
	repairControllerBaselineDurable
)

func (worker *RepairControllerWorker) processClaim(
	ctx context.Context,
	heartbeat *controllerHeartbeat,
	run eventing.PRDevelopmentRepairOrchestration,
) error {
	claim, err := worker.loadClaim(ctx, run)
	if err != nil {
		return worker.failBootstrapIfSafe(ctx, run, nil, "",
			eventing.PRDevelopmentRepairErrorInternal,
			"Local repair could not start because its durable state is invalid.", err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = worker.requireServices(run.Phase); err != nil {
		return worker.failBootstrapIfSafe(ctx, run, nil, "",
			eventing.PRDevelopmentRepairErrorRuntimeUnavailable,
			"The configured local repair runtime is unavailable.", err)
	}
	contextDigest := run.ContextDigest
	promptDigest := run.PromptDigest
	if run.Phase != eventing.PRDevelopmentRepairOrchestrationBootstrap &&
		(!validControllerSHA256(contextDigest) ||
			!validControllerSHA256(promptDigest) ||
			!validControllerSHA256(run.ModelResultDigest)) {
		return errors.New("durable repair model checkpoint digests are incomplete")
	}
	expectedThread, err := repairThreadIdentity(
		claim.workbench.Thread,
		claim.session.CaseID,
	)
	if err != nil || expectedThread == nil || claim.workbench.Thread.ID != run.ThreadID {
		if err == nil {
			err = errors.New("repair orchestration is not bound to one provider thread")
		}
		return worker.failBootstrapIfSafe(ctx, run, nil, "",
			eventing.PRDevelopmentRepairErrorInternal,
			"Local repair could not start because its durable thread identity is invalid.", err)
	}
	claim.verified, err = worker.verifier.VerifyCase(
		ctx,
		claim.workbench.Case,
		expectedThread,
	)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		code, summary := classifyRepairPreparationError(err)
		return worker.failBootstrapIfSafe(ctx, run, nil, "", code, summary, err)
	}
	if err = validateRepairControllerVerifiedCase(claim); err != nil {
		return worker.failBootstrapIfSafe(ctx, run, nil, "",
			eventing.PRDevelopmentRepairErrorProviderChanged,
			"The pull request changed before local repair could start.", err)
	}

	priorController, controllerFound, err := worker.loadController(ctx, claim)
	if err != nil {
		return worker.failBootstrapIfSafe(ctx, run, nil, "",
			eventing.PRDevelopmentRepairErrorInternal,
			"Local repair could not establish its retained development line.", err)
	}

	var runner LocalRepairExecutor
	prepareBootstrapModel := func() (
		eventing.PRDevelopmentRepairErrorCode,
		string,
		error,
	) {
		loader, loaderErr := worker.context(claim.session.AgentID)
		if loaderErr != nil || isNilServiceValue(loader) {
			if loaderErr == nil {
				loaderErr = errors.New("thread context loader factory returned no loader")
			}
			return eventing.PRDevelopmentRepairErrorRuntimeUnavailable,
				"The configured local repair runtime is unavailable.", loaderErr
		}
		claim.context, loaderErr = loader.Load(
			ctx,
			claim.session.CaseID,
			claim.attempt.ConversationVersion,
		)
		if loaderErr != nil {
			return eventing.PRDevelopmentRepairErrorInternal,
				"Local repair could not prepare its bounded repository context.", loaderErr
		}
		contextDigest = controllerContextDigest(claim.context)
		promptDigest = agent.ControllerLocalRepairPromptDigest()
		runner, loaderErr = worker.runtime(
			claim.session.AgentID,
			claim.attempt.Instruction,
		)
		if loaderErr != nil || isNilServiceValue(runner) {
			if loaderErr == nil {
				loaderErr = errors.New("local repair runtime factory returned no runner")
			}
			return eventing.PRDevelopmentRepairErrorRuntimeUnavailable,
				"The configured local repair runtime is unavailable.", loaderErr
		}
		return "", "", nil
	}
	workspace, err := worker.workspaces()
	if err != nil || isNilServiceValue(workspace) {
		if err == nil {
			err = errors.New("controller Git workspace factory returned no manager")
		}
		return worker.failBootstrapIfSafe(ctx, run, nil, "",
			eventing.PRDevelopmentRepairErrorWorkspaceUnavailable,
			"The local repair workspace is unavailable.", err)
	}

	var baselineState repairControllerBaselineState
	run, claim.session, baselineState, err = worker.pinBaseline(
		ctx,
		run,
		claim.session,
		claim.verified,
		priorController,
		controllerFound,
		workspace,
	)
	if err != nil {
		switch baselineState {
		case repairControllerBaselineUnchanged:
			return worker.failBootstrapIfSafe(ctx, run, nil, "",
				eventing.PRDevelopmentRepairErrorWorkspaceUnavailable,
				"Local repair could not establish its exact local source baseline.", err)
		case repairControllerBaselineAcquiredUnpinned:
			return worker.failBootstrapIfSafe(
				ctx, run, workspace, claim.session.ReservationKey,
				eventing.PRDevelopmentRepairErrorWorkspaceUnavailable,
				"Local repair could not establish its exact local source baseline.", err,
			)
		default:
			// A durable pin or an ambiguous Pin transaction must remain
			// reclaimable. Never release or terminalize across that boundary.
			return err
		}
	}
	claim.run = run

	expectedRevision := int64(0)
	if controllerFound {
		expectedRevision = priorController.Revision
	}
	controllerLease, _, err := worker.store.AcquirePRDevelopmentRepairOrchestrationController(
		ctx,
		eventing.PRDevelopmentRepairOrchestrationControllerAcquire{
			CaseID:           claim.session.CaseID,
			AttemptID:        claim.attempt.ID,
			ClaimToken:       run.ClaimToken,
			ExpectedRevision: expectedRevision,
			WorkerLabel:      worker.workerLabel,
			Lease:            worker.lease,
		},
	)
	if err != nil {
		// The local store may have committed controller acquisition before a
		// lost response. Never terminalize or release across that ambiguity.
		return fmt.Errorf("acquire retained development controller: %w", err)
	}
	resumedSuspended := false
	if controllerLease.SuspendedResume != nil {
		resumeLease := *controllerLease.SuspendedResume
		if err = validateRepairControllerSuspendedResumeLease(
			claim,
			run,
			priorController,
			controllerFound,
			controllerLease,
		); err != nil {
			return err
		}
		heartbeat.SetSuspendedResume(resumeLease)
		resumeResult, resumeErr := resumeSuspendedController(ctx, workspace, resumeLease)
		if resumeErr != nil {
			return resumeErr
		}
		// Refresh the parent and child claims once after Git so the following
		// atomic finalization starts with a full, parent-capped lease window.
		// The write-side transition barrier below drains this renewal and any
		// ticker renewal before consuming the child claim.
		if err = heartbeat.renew(ctx); err != nil {
			return fmt.Errorf("refresh suspended resume claim before finalization: %w", err)
		}
		// Stop renewing the consumed resume claim, but keep the surrounding
		// orchestration claim alive while the store installs its replacement
		// mutation lease.
		heartbeat.BeginResumeTransition()
		if err = ctx.Err(); err != nil {
			return err
		}
		controllerLease, _, err = worker.store.FinalizePRDevelopmentControllerSuspendedResume(
			ctx,
			eventing.PRDevelopmentControllerSuspendedResumeFinalize{
				ControllerID:            resumeLease.Controller.ID,
				AttemptID:               claim.attempt.ID,
				SuspensionID:            resumeLease.Suspension.ID,
				ExpectedRevision:        resumeLease.Controller.Revision,
				OrchestrationClaimToken: run.ClaimToken,
				ClaimID:                 resumeLease.Suspension.ResumeClaimID,
				ClaimToken:              resumeLease.Suspension.ResumeClaimToken,
				ClaimEpoch:              resumeLease.Suspension.ResumeClaimEpoch,
				Result:                  resumeResult,
				Lease:                   worker.lease,
			},
		)
		if err != nil {
			// Git may already have resumed the line. Leave the durable claim for
			// exact replay/recovery; never retry Git from an invented request.
			return fmt.Errorf("finalize suspended retained development line: %w", err)
		}
		resumedSuspended = true
	}
	if err = validateRepairControllerLease(
		claim,
		run,
		priorController,
		controllerFound,
		resumedSuspended,
		controllerLease,
	); err != nil {
		return err
	}
	heartbeat.SetController(controllerLease.Controller)
	effects, err := worker.effects(worker.journal, workspace, claim.session, controllerLease)
	if err != nil || isNilServiceValue(effects) {
		if err == nil {
			err = errors.New("controller effect factory returned no effect runner")
		}
		return err
	}
	line, err := establishRepairControllerLine(ctx, effects, controllerLease.Controller, run.SourceTree)
	if err != nil {
		return err
	}
	if run.Phase == eventing.PRDevelopmentRepairOrchestrationBootstrap {
		_, _, prepareErr := prepareBootstrapModel()
		if prepareErr != nil {
			// Mutation ownership is now durable (whether newly adopted, resumed
			// from Ready, or restored from Suspended). Leave it reclaimable;
			// terminalizing bootstrap here would cross a Git/store boundary.
			return fmt.Errorf("prepare model after retained line acquisition: %w", prepareErr)
		}
	}
	fence := repairControllerLineFence{
		controllerID:  controllerLease.Controller.ID,
		revision:      line.Revision,
		leaseToken:    controllerLease.Controller.LeaseToken,
		leaseEpoch:    controllerLease.Controller.LeaseEpoch,
		reservation:   controllerLease.Controller.MutationReservationKey,
		workspaceID:   line.WorkspaceID,
		lineID:        line.LineID,
		lineVersion:   line.LineVersion,
		mutationEpoch: line.MutationEpoch,
		tip:           line.Tip,
		tree:          line.Tree,
	}

	if run.Phase == eventing.PRDevelopmentRepairOrchestrationBootstrap {
		run, _, err = worker.store.StartPRDevelopmentRepairOrchestrationModel(
			ctx,
			eventing.PRDevelopmentRepairOrchestrationModelStart{
				AttemptID:          run.AttemptID,
				ClaimToken:         run.ClaimToken,
				ControllerID:       fence.controllerID,
				ControllerRevision: fence.revision,
				MutationLeaseToken: fence.leaseToken,
				MutationLeaseEpoch: fence.leaseEpoch,
				ContextDigest:      contextDigest,
				PromptDigest:       promptDigest,
			},
		)
		if err != nil {
			return err
		}
		if err = validateRepairControllerModelFence(run, fence); err != nil {
			return err
		}
		result, runErr := runner.Run(ctx, agent.LocalRepairRequest{
			Pin:         repairControllerPin(claim.session, controllerLease.Controller.MutationReservationKey),
			Instruction: claim.attempt.Instruction,
			Context:     claim.context,
		})
		if runErr != nil {
			return fmt.Errorf("run isolated local repair model: %w", runErr)
		}
		if result.WorkspaceID != fence.workspaceID || result.Iterations < 1 ||
			result.Iterations > eventing.MaxPRDevelopmentRepairIterations {
			return errors.New("local repair model returned an invalid durable result fence")
		}
		modelDigest := controllerModelResultDigest(
			result.Content,
			result.WorkspaceID,
			result.Iterations,
		)
		run, _, err = worker.store.CompletePRDevelopmentRepairOrchestrationModel(
			ctx,
			eventing.PRDevelopmentRepairOrchestrationModelComplete{
				AttemptID:          run.AttemptID,
				ClaimToken:         run.ClaimToken,
				ControllerID:       fence.controllerID,
				ControllerRevision: fence.revision,
				MutationLeaseToken: fence.leaseToken,
				MutationLeaseEpoch: fence.leaseEpoch,
				ModelResultDigest:  modelDigest,
				Summary:            boundedRepairSummary(result.Content),
				Iterations:         result.Iterations,
			},
		)
		if err != nil {
			return err
		}
	} else if err = validateRepairControllerModelFence(run, fence); err != nil {
		return err
	}
	if run.Phase != eventing.PRDevelopmentRepairOrchestrationEdited &&
		run.Phase != eventing.PRDevelopmentRepairOrchestrationValidated {
		return errors.New("repair orchestration did not reach a resumable edited checkpoint")
	}
	if run.ModelResultDigest == "" || run.Summary == "" || run.Iterations < 1 {
		return errors.New("repair orchestration edited checkpoint is incomplete")
	}

	candidate, err := worker.validateCandidate(ctx, run, claim.session, fence, workspace)
	if err != nil {
		return err
	}
	message, err := controllerCommitMessage(claim.attempt.Ordinal)
	if err != nil {
		return err
	}
	commit, err := effects.CommitCandidate(ctx, candidate, message)
	if err != nil {
		return err
	}
	_, err = effects.Park(
		ctx,
		commit,
		run.Summary,
		run.Iterations,
		heartbeat.BeginTerminal,
	)
	return err
}

func validateRepairControllerModelFence(
	run eventing.PRDevelopmentRepairOrchestration,
	fence repairControllerLineFence,
) error {
	if run.ControllerID != fence.controllerID ||
		run.ModelControllerRevision != fence.revision ||
		run.ModelLineID != fence.lineID ||
		run.ModelLineVersion != fence.lineVersion ||
		run.ModelMutationEpoch != fence.mutationEpoch ||
		run.ModelMutationLeaseEpoch != fence.leaseEpoch ||
		!validControllerSHA256(run.ModelLeaseTokenDigest) ||
		!validControllerSHA256(run.ModelReservationDigest) ||
		!validControllerSHA256(run.ContextDigest) ||
		!validControllerSHA256(run.PromptDigest) {
		return errors.New("durable model checkpoint differs from the active controller fence")
	}
	return nil
}

func (worker *RepairControllerWorker) requireServices(
	phase eventing.PRDevelopmentRepairOrchestrationPhase,
) error {
	if isNilServiceValue(worker.verifier) || worker.workspaces == nil || worker.effects == nil ||
		worker.journal == nil {
		return errors.New("repair controller provider, workspace, or effect runtime is unavailable")
	}
	switch phase {
	case eventing.PRDevelopmentRepairOrchestrationBootstrap:
		if worker.context == nil || worker.runtime == nil || isNilServiceValue(worker.localCI) {
			return errors.New("repair controller model, context, or local CI runtime is unavailable")
		}
	case eventing.PRDevelopmentRepairOrchestrationEdited:
		if isNilServiceValue(worker.localCI) {
			return errors.New("repair controller local CI runtime is unavailable")
		}
	case eventing.PRDevelopmentRepairOrchestrationValidated:
		// The immutable receipt is sufficient; CI and model capabilities are
		// deliberately not resolved again.
	default:
		return errors.New("repair controller orchestration phase is unsupported")
	}
	return nil
}

func (worker *RepairControllerWorker) loadClaim(
	ctx context.Context,
	run eventing.PRDevelopmentRepairOrchestration,
) (repairControllerClaim, error) {
	if run.AttemptID == "" || run.SessionID == "" || run.CaseID == "" ||
		run.ThreadID == "" || run.AgentID == "" || run.Instruction == "" ||
		run.ClaimToken == "" || run.ClaimUntil == nil ||
		(run.Phase != eventing.PRDevelopmentRepairOrchestrationBootstrap &&
			run.Phase != eventing.PRDevelopmentRepairOrchestrationEdited &&
			run.Phase != eventing.PRDevelopmentRepairOrchestrationValidated) {
		return repairControllerClaim{}, errors.New("claimed repair orchestration is incomplete")
	}
	workbench, err := worker.store.GetPRDevelopmentWorkbench(ctx, run.CaseID)
	if err != nil {
		return repairControllerClaim{}, err
	}
	if workbench.Case.ID != run.CaseID || workbench.Thread == nil ||
		workbench.Thread.ID != run.ThreadID ||
		workbench.Thread.Kind != eventing.PRDevelopmentThreadProvider ||
		workbench.RepairSession == nil || workbench.RepairSession.ID != run.SessionID ||
		workbench.RepairSession.CaseID != run.CaseID ||
		workbench.RepairSession.AgentID != run.AgentID || len(workbench.RepairSession.Attempts) == 0 {
		return repairControllerClaim{}, errors.New("repair orchestration differs from its atomic workbench")
	}
	session := *workbench.RepairSession
	attempt := session.Attempts[len(session.Attempts)-1]
	if attempt.ID != run.AttemptID || attempt.SessionID != session.ID ||
		attempt.Ordinal != len(session.Attempts)-1 || attempt.Status != eventing.PRDevelopmentRepairQueued ||
		attempt.Claims != 0 || attempt.Instruction != run.Instruction ||
		attempt.ConversationVersion < 0 ||
		attempt.ConversationVersion > workbench.Conversation.Version ||
		workbench.Conversation.CaseID != run.CaseID {
		return repairControllerClaim{}, errors.New("repair orchestration is not the queued latest attempt")
	}
	if err = validateConversation(run.CaseID, workbench.Conversation); err != nil {
		return repairControllerClaim{}, err
	}
	return repairControllerClaim{
		run:       run,
		workbench: workbench,
		session:   session,
		attempt:   attempt,
	}, nil
}

func validateRepairControllerVerifiedCase(claim repairControllerClaim) error {
	verified := claim.verified
	if verified.CaseID != claim.session.CaseID ||
		!strings.EqualFold(verified.Repository, claim.workbench.Case.Repository) ||
		verified.PullNumber != claim.workbench.Case.PullNumber ||
		verified.HeadRepository == "" || verified.HeadRef == "" ||
		verified.HeadSHA == "" || verified.HeadCloneURL == "" ||
		verified.ReviewDigest == "" ||
		verified.CurrentReviewState != claim.workbench.Case.SubmittedReviewState {
		return errors.New("verified provider case differs from the durable case")
	}
	if claim.session.HeadRepository != "" &&
		(claim.session.HeadRepository != verified.HeadRepository ||
			claim.session.HeadRef != verified.HeadRef ||
			claim.session.HeadSHA != verified.HeadSHA ||
			claim.session.CloneURL != verified.HeadCloneURL ||
			claim.session.ReviewDigest != verified.ReviewDigest) {
		return errors.New("verified provider case differs from the immutable session pin")
	}
	return nil
}

func (worker *RepairControllerWorker) loadController(
	ctx context.Context,
	claim repairControllerClaim,
) (eventing.PRDevelopmentController, bool, error) {
	controller, err := worker.store.GetPRDevelopmentControllerForCase(
		ctx,
		claim.session.CaseID,
	)
	if errors.Is(err, eventing.ErrNotFound) {
		if claim.run.ControllerID != "" {
			return eventing.PRDevelopmentController{}, false,
				errors.New("repair orchestration names a missing controller")
		}
		return eventing.PRDevelopmentController{}, false, nil
	}
	if err != nil {
		return eventing.PRDevelopmentController{}, false, err
	}
	if controller.ThreadID != claim.run.ThreadID ||
		controller.OwnerSessionID != claim.session.ID ||
		controller.AgentID != claim.session.AgentID ||
		controller.ID == "" || controller.LineID == "" {
		return eventing.PRDevelopmentController{}, false,
			errors.New("retained controller differs from the repair owner")
	}
	if claim.run.ControllerID != "" && controller.ID != claim.run.ControllerID {
		return eventing.PRDevelopmentController{}, false,
			errors.New("repair orchestration controller identity changed")
	}
	switch controller.Phase {
	case eventing.PRDevelopmentControllerReady:
		if controller.CurrentAttemptID == claim.attempt.ID {
			return eventing.PRDevelopmentController{}, false,
				errors.New("ready controller already names the queued attempt")
		}
	case eventing.PRDevelopmentControllerMutation:
		if controller.CurrentAttemptID != claim.attempt.ID || claim.run.ControllerID == "" {
			return eventing.PRDevelopmentController{}, false,
				errors.New("live mutation controller belongs to another attempt")
		}
	case eventing.PRDevelopmentControllerSuspended:
		if controller.LeaseKind != "" || controller.LeaseToken != "" ||
			controller.LeaseUntil != nil || controller.MutationReservationKey != "" {
			return eventing.PRDevelopmentController{}, false,
				errors.New("suspended controller unexpectedly retains mutation authority")
		}
		if controller.CurrentAttemptID == claim.attempt.ID {
			if claim.run.ControllerID != controller.ID {
				return eventing.PRDevelopmentController{}, false,
					errors.New("prepared suspended resume is not bound to this orchestration")
			}
		} else if claim.run.ControllerID != "" {
			return eventing.PRDevelopmentController{}, false,
				errors.New("suspended controller belongs to a different prepared attempt")
		}
	default:
		return eventing.PRDevelopmentController{}, false,
			fmt.Errorf("retained controller phase %q cannot start repair", controller.Phase)
	}
	if controller.WorkspaceID != "" &&
		(controller.WorkspaceID != claim.session.WorkspaceID ||
			controller.SourceCloneURL != claim.verified.HeadCloneURL ||
			controller.SourceRef != claim.verified.HeadRef ||
			controller.SourceCommit != claim.verified.HeadSHA ||
			controller.SourceTree == "") {
		return eventing.PRDevelopmentController{}, false,
			errors.New("retained controller source differs from the provider/session pin")
	}
	return controller, true, nil
}

func (worker *RepairControllerWorker) pinBaseline(
	ctx context.Context,
	run eventing.PRDevelopmentRepairOrchestration,
	session eventing.PRDevelopmentRepairSession,
	verified VerifiedCase,
	controller eventing.PRDevelopmentController,
	controllerFound bool,
	workspace repairControllerWorkspace,
) (
	eventing.PRDevelopmentRepairOrchestration,
	eventing.PRDevelopmentRepairSession,
	repairControllerBaselineState,
	error,
) {
	if run.HeadRepository != "" {
		if run.HeadRepository != verified.HeadRepository || run.HeadRef != verified.HeadRef ||
			run.HeadSHA != verified.HeadSHA || run.CloneURL != verified.HeadCloneURL ||
			run.ReviewDigest != verified.ReviewDigest || run.WorkspaceID == "" ||
			run.SourceTree == "" || session.WorkspaceID != run.WorkspaceID {
			return run, session, repairControllerBaselineDurable, errors.New(
				"durable orchestration provider/workspace pin changed",
			)
		}
		if controllerFound && controller.WorkspaceID != "" &&
			(controller.WorkspaceID != run.WorkspaceID || controller.SourceTree != run.SourceTree) {
			return run, session, repairControllerBaselineDurable, errors.New(
				"durable orchestration pin differs from retained line",
			)
		}
		if !controllerFound {
			pinned, err := workspace.AcquirePinned(ctx, repairControllerPin(session, session.ReservationKey))
			if err != nil {
				return run, session, repairControllerBaselineDurable, err
			}
			if pinned.ID != run.WorkspaceID {
				return run, session, repairControllerBaselineDurable, errors.New(
					"reacquired initial workspace identity changed",
				)
			}
			candidate, snapshotErr := workspace.SnapshotPinnedValidationCandidate(
				ctx,
				gitworkspace.PinnedCandidateRequest{
					Pin:         repairControllerPin(session, session.ReservationKey),
					WorkspaceID: pinned.ID,
				},
			)
			if snapshotErr != nil {
				return run, session, repairControllerBaselineDurable, snapshotErr
			}
			if candidate.ParentCommit != run.HeadSHA || candidate.Tree != run.SourceTree ||
				candidate.ChangedFiles != 0 {
				return run, session, repairControllerBaselineDurable,
					errors.New("reacquired initial workspace differs from its clean baseline")
			}
			return run, session, repairControllerBaselineDurable, nil
		}
		return run, session, repairControllerBaselineDurable, nil
	}
	if controllerFound {
		if controller.WorkspaceID == "" || controller.SourceTree == "" {
			return run, session, repairControllerBaselineDurable, errors.New(
				"unbound controller has no persisted orchestration pin",
			)
		}
		pinned, _, err := worker.store.PinPRDevelopmentRepairOrchestration(
			ctx,
			eventing.PRDevelopmentRepairOrchestrationPin{
				AttemptID:      run.AttemptID,
				ClaimToken:     run.ClaimToken,
				HeadRepository: verified.HeadRepository,
				HeadRef:        verified.HeadRef,
				HeadSHA:        verified.HeadSHA,
				CloneURL:       verified.HeadCloneURL,
				ReviewDigest:   verified.ReviewDigest,
				WorkspaceID:    controller.WorkspaceID,
				SourceTree:     controller.SourceTree,
			},
		)
		if err != nil {
			return run, session, repairControllerBaselinePinAttempted, err
		}
		return pinned, session, repairControllerBaselineDurable, nil
	}
	if session.HeadRepository != "" || session.WorkspaceID != "" || session.ReservationKey == "" {
		return run, session, repairControllerBaselineUnchanged, errors.New("initial repair session baseline is partial")
	}
	pin := gitworkspace.PinnedAcquireRequest{
		Repository:     verified.HeadCloneURL,
		SourceRef:      verified.HeadRef,
		ExpectedCommit: verified.HeadSHA,
		ReservationKey: session.ReservationKey,
		AgentID:        session.AgentID,
	}
	pinned, err := workspace.AcquirePinned(ctx, pin)
	if err != nil {
		// Acquire may have persisted ownership before failing to project its
		// result. Treat the reservation as possibly owned and release it before
		// any safe terminal failure.
		return run, session, repairControllerBaselineAcquiredUnpinned, err
	}
	candidate, err := workspace.SnapshotPinnedValidationCandidate(
		ctx,
		gitworkspace.PinnedCandidateRequest{Pin: pin, WorkspaceID: pinned.ID},
	)
	if err != nil {
		return run, session, repairControllerBaselineAcquiredUnpinned, err
	}
	if candidate.WorkspaceID != pinned.ID || candidate.ParentCommit != verified.HeadSHA ||
		candidate.ChangedFiles != 0 || candidate.Tree == "" {
		return run, session, repairControllerBaselineAcquiredUnpinned, errors.New(
			"initial pinned workspace is not the exact clean provider tip",
		)
	}
	pinnedRun, _, err := worker.store.PinPRDevelopmentRepairOrchestration(
		ctx,
		eventing.PRDevelopmentRepairOrchestrationPin{
			AttemptID:      run.AttemptID,
			ClaimToken:     run.ClaimToken,
			HeadRepository: verified.HeadRepository,
			HeadRef:        verified.HeadRef,
			HeadSHA:        verified.HeadSHA,
			CloneURL:       verified.HeadCloneURL,
			ReviewDigest:   verified.ReviewDigest,
			WorkspaceID:    pinned.ID,
			SourceTree:     candidate.Tree,
		},
	)
	if err != nil {
		return run, session, repairControllerBaselinePinAttempted, err
	}
	session.HeadRepository = verified.HeadRepository
	session.HeadRef = verified.HeadRef
	session.HeadSHA = verified.HeadSHA
	session.CloneURL = verified.HeadCloneURL
	session.ReviewDigest = verified.ReviewDigest
	session.WorkspaceID = pinned.ID
	return pinnedRun, session, repairControllerBaselineDurable, nil
}

func validateRepairControllerLease(
	claim repairControllerClaim,
	run eventing.PRDevelopmentRepairOrchestration,
	prior eventing.PRDevelopmentController,
	priorFound bool,
	resumedSuspended bool,
	lease eventing.PRDevelopmentControllerLease,
) error {
	controller := lease.Controller
	if lease.ReviewFence != nil || lease.SuspendedResume != nil || controller.ID == "" ||
		controller.ThreadID != run.ThreadID || controller.OwnerSessionID != claim.session.ID ||
		controller.AgentID != claim.session.AgentID || controller.CurrentAttemptID != run.AttemptID ||
		controller.Phase != eventing.PRDevelopmentControllerMutation ||
		controller.LeaseKind != eventing.PRDevelopmentControllerMutationLease ||
		controller.LeaseToken == "" || controller.LeaseEpoch < 1 ||
		controller.MutationReservationKey == "" {
		return errors.New("acquired repair controller lease is incomplete or changed")
	}
	if run.ControllerID != "" && controller.ID != run.ControllerID {
		return errors.New("acquired repair controller identity changed")
	}
	if priorFound && controller.ID != prior.ID {
		return errors.New("acquired a different retained controller")
	}
	if priorFound && prior.Phase == eventing.PRDevelopmentControllerSuspended &&
		!resumedSuspended {
		return errors.New("suspended retained controller bypassed exact resume")
	}
	if controller.WorkspaceID != "" &&
		(controller.WorkspaceID != run.WorkspaceID ||
			controller.WorkspaceID != claim.session.WorkspaceID ||
			controller.SourceCloneURL != run.CloneURL ||
			controller.SourceRef != run.HeadRef ||
			controller.SourceCommit != run.HeadSHA ||
			controller.SourceTree != run.SourceTree ||
			controller.TipCommit == "" || controller.Tree == "" ||
			(controller.MutationEpoch != controller.LineVersion &&
				controller.MutationEpoch != controller.LineVersion+1)) {
		return errors.New("acquired repair controller source or line fence changed")
	}
	return nil
}

func validateRepairControllerSuspendedResumeLease(
	claim repairControllerClaim,
	run eventing.PRDevelopmentRepairOrchestration,
	prior eventing.PRDevelopmentController,
	priorFound bool,
	lease eventing.PRDevelopmentControllerLease,
) error {
	if lease.ReviewFence != nil || lease.SuspendedResume == nil {
		return errors.New("acquired suspended resume lease is missing")
	}
	resume := *lease.SuspendedResume
	controller := lease.Controller
	resumeController := resume.Controller
	suspension := resume.Suspension
	if controller.ID == "" || controller != resumeController ||
		controller.ID != suspension.ControllerID || controller.ThreadID != run.ThreadID ||
		controller.OwnerSessionID != claim.session.ID ||
		controller.AgentID != claim.session.AgentID ||
		controller.CurrentAttemptID != claim.attempt.ID ||
		controller.Phase != eventing.PRDevelopmentControllerSuspended ||
		controller.LeaseKind != "" || controller.LeaseToken != "" ||
		controller.LeaseUntil != nil || controller.MutationReservationKey != "" ||
		suspension.Status != eventing.PRDevelopmentControllerSuspensionStatusResumeClaimed ||
		suspension.ResumeAttemptID != claim.attempt.ID ||
		suspension.ResumeClaimID == "" || suspension.ResumeClaimToken == "" ||
		suspension.ResumeClaimEpoch < 1 || suspension.ResumeClaimUntil == nil ||
		suspension.ResumeReservationKey == "" {
		return errors.New("acquired suspended resume lease is incomplete or changed")
	}
	if run.ControllerID != "" && controller.ID != run.ControllerID {
		return errors.New("acquired suspended resume controller identity changed")
	}
	if priorFound {
		if controller.ID != prior.ID || prior.Phase != eventing.PRDevelopmentControllerSuspended ||
			(controller.Revision != prior.Revision && controller.Revision != prior.Revision+1) {
			return errors.New("acquired a different suspended retained controller")
		}
	}
	if controller.WorkspaceID == "" || controller.WorkspaceID != run.WorkspaceID ||
		controller.WorkspaceID != claim.session.WorkspaceID ||
		controller.SourceCloneURL != run.CloneURL || controller.SourceRef != run.HeadRef ||
		controller.SourceCommit != run.HeadSHA || controller.SourceTree != run.SourceTree ||
		controller.LineID == "" || controller.TipCommit == "" || controller.Tree == "" ||
		controller.WorkspaceID != suspension.WorkspaceID ||
		controller.LineID != suspension.LineID ||
		controller.SourceCloneURL != suspension.SourceCloneURL ||
		controller.SourceRef != suspension.SourceRef ||
		controller.SourceCommit != suspension.SourceCommit ||
		controller.SourceTree != suspension.SourceTree ||
		controller.LineVersion != suspension.LineVersion ||
		controller.MutationEpoch != suspension.MutationEpoch ||
		controller.TipCommit != suspension.TipCommit || controller.Tree != suspension.Tree {
		return errors.New("acquired suspended resume source or line fence changed")
	}
	return nil
}

func establishRepairControllerLine(
	ctx context.Context,
	effects repairControllerEffects,
	controller eventing.PRDevelopmentController,
	sourceTree string,
) (controllerLineState, error) {
	switch {
	case controller.WorkspaceID == "":
		return effects.Adopt(ctx, sourceTree)
	case controller.MutationEpoch == controller.LineVersion:
		return effects.Resume(ctx)
	case controller.MutationEpoch == controller.LineVersion+1:
		if controller.SourceTree != sourceTree || controller.WorkspaceID == "" ||
			controller.TipCommit == "" || controller.Tree == "" {
			return controllerLineState{}, errors.New("active retained line differs from orchestration source")
		}
		return lineState(controller), nil
	default:
		return controllerLineState{}, errors.New("retained controller mutation epoch is invalid")
	}
}

func (worker *RepairControllerWorker) validateCandidate(
	ctx context.Context,
	run eventing.PRDevelopmentRepairOrchestration,
	session eventing.PRDevelopmentRepairSession,
	fence repairControllerLineFence,
	workspace repairControllerWorkspace,
) (gitworkspace.PinnedCandidate, error) {
	if run.Phase == eventing.PRDevelopmentRepairOrchestrationValidated {
		return validatedRepairControllerCandidate(run, fence)
	}
	pin := repairControllerPin(session, fence.reservation)
	if pin.ReservationKey == "" {
		return gitworkspace.PinnedCandidate{}, errors.New("repair controller reservation is unavailable")
	}
	candidate, err := workspace.SnapshotPinnedValidationCandidate(
		ctx,
		gitworkspace.PinnedCandidateRequest{Pin: pin, WorkspaceID: fence.workspaceID},
	)
	if err != nil {
		return gitworkspace.PinnedCandidate{}, err
	}
	if err = validateRepairControllerCandidate(candidate, fence); err != nil {
		return gitworkspace.PinnedCandidate{}, err
	}
	identities, err := newControllerAttemptIdentities(run.AttemptID)
	if err != nil {
		return gitworkspace.PinnedCandidate{}, err
	}
	ciResult, err := worker.localCI.RunPinned(
		ctx,
		workspace,
		localci.PinnedRunRequest{
			AttestationID: identities.CIAttestation,
			OwnerID:       identities.CIOwner,
			Candidate: gitworkspace.PinnedCandidateValidationRequest{
				Pin:                     pin,
				WorkspaceID:             candidate.WorkspaceID,
				ExpectedParent:          candidate.ParentCommit,
				ExpectedTree:            candidate.Tree,
				ExpectedCandidateDigest: candidate.CandidateDigest,
				NoChanges:               candidate.ChangedFiles == 0,
			},
		},
	)
	if err != nil {
		return gitworkspace.PinnedCandidate{}, err
	}
	status, err := validateRepairControllerCIResult(ciResult, identities, candidate)
	if err != nil {
		return gitworkspace.PinnedCandidate{}, err
	}
	validated, _, err := worker.store.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx,
		eventing.PRDevelopmentRepairOrchestrationValidation{
			AttemptID:             run.AttemptID,
			ClaimToken:            run.ClaimToken,
			ControllerID:          fence.controllerID,
			ControllerRevision:    fence.revision,
			MutationLeaseToken:    fence.leaseToken,
			MutationLeaseEpoch:    fence.leaseEpoch,
			ParentCommit:          candidate.ParentCommit,
			ParentTree:            fence.tree,
			CandidateTree:         candidate.Tree,
			CandidateDigest:       candidate.CandidateDigest,
			ChangedFiles:          candidate.ChangedFiles,
			NoChanges:             candidate.ChangedFiles == 0,
			CIStatus:              status,
			CIAttestationID:       ciResult.Attestation.ID,
			CIAttestationDigest:   ciResult.Attestation.Digest,
			CIResultKey:           ciResult.Execution.ResultKey,
			CIEffectivePlanDigest: ciResult.Plan.Effective.Digest,
			CIExecutionDigest:     ciResult.Execution.Digest,
		},
	)
	if err != nil {
		return gitworkspace.PinnedCandidate{}, err
	}
	if validated.Phase != eventing.PRDevelopmentRepairOrchestrationValidated ||
		validated.Validation == nil ||
		!equalRepairControllerValidationReceipt(
			validated,
			fence,
			candidate,
			status,
			ciResult,
		) {
		return gitworkspace.PinnedCandidate{}, errors.New("local CI receipt was not durably recorded")
	}
	return candidate, nil
}

func validatedRepairControllerCandidate(
	run eventing.PRDevelopmentRepairOrchestration,
	fence repairControllerLineFence,
) (gitworkspace.PinnedCandidate, error) {
	receipt := run.Validation
	if receipt == nil || !equalRepairControllerValidationFence(receipt, run, fence) {
		return gitworkspace.PinnedCandidate{}, errors.New(
			"validated repair checkpoint differs from the active controller",
		)
	}
	candidate := gitworkspace.PinnedCandidate{
		WorkspaceID:     receipt.WorkspaceID,
		ParentCommit:    receipt.ParentCommit,
		Tree:            receipt.CandidateTree,
		CandidateDigest: receipt.CandidateDigest,
		ChangedFiles:    receipt.ChangedFiles,
	}
	if err := validateRepairControllerCandidate(candidate, fence); err != nil {
		return gitworkspace.PinnedCandidate{}, err
	}
	if receipt.NoChanges != (candidate.ChangedFiles == 0) {
		return gitworkspace.PinnedCandidate{}, errors.New("validated no-change evidence is inconsistent")
	}
	identities, err := newControllerAttemptIdentities(run.AttemptID)
	if err != nil || receipt.CIAttestationID != identities.CIAttestation ||
		!validRepairControllerCIStatus(receipt.CIStatus) {
		return gitworkspace.PinnedCandidate{}, errors.New("validated local CI identity is inconsistent")
	}
	return candidate, nil
}

func validateRepairControllerCandidate(
	candidate gitworkspace.PinnedCandidate,
	fence repairControllerLineFence,
) error {
	if candidate.WorkspaceID != fence.workspaceID || candidate.ParentCommit != fence.tip ||
		!validObjectID(candidate.ParentCommit) || !validObjectID(candidate.Tree) ||
		!validControllerSHA256(candidate.CandidateDigest) || candidate.ChangedFiles < 0 ||
		candidate.ChangedFiles > 10_000 ||
		(candidate.ChangedFiles == 0) != (candidate.Tree == fence.tree) {
		return errors.New("repair candidate differs from the active controller parent")
	}
	return nil
}

func equalRepairControllerValidationFence(
	receipt *eventing.PRDevelopmentRepairValidationReceipt,
	run eventing.PRDevelopmentRepairOrchestration,
	fence repairControllerLineFence,
) bool {
	return receipt.ControllerID == fence.controllerID &&
		receipt.WorkspaceID == fence.workspaceID &&
		receipt.ModelControllerRevision == run.ModelControllerRevision &&
		receipt.ModelLineID == run.ModelLineID &&
		receipt.ModelLineVersion == run.ModelLineVersion &&
		receipt.ModelMutationEpoch == run.ModelMutationEpoch &&
		receipt.ModelMutationLeaseEpoch == run.ModelMutationLeaseEpoch &&
		receipt.ModelLeaseTokenDigest == run.ModelLeaseTokenDigest &&
		receipt.ModelReservationDigest == run.ModelReservationDigest &&
		receipt.ContextDigest == run.ContextDigest &&
		receipt.PromptDigest == run.PromptDigest &&
		receipt.LineID == fence.lineID &&
		receipt.ControllerRevision == fence.revision &&
		receipt.LineVersion == fence.lineVersion &&
		receipt.MutationEpoch == fence.mutationEpoch &&
		receipt.MutationLeaseEpoch == fence.leaseEpoch &&
		receipt.ParentCommit == fence.tip &&
		receipt.ParentTree == fence.tree &&
		receipt.ModelResultDigest == run.ModelResultDigest &&
		receipt.ModelSummary == run.Summary &&
		receipt.ModelIterations == run.Iterations &&
		validControllerSHA256(receipt.ModelLeaseTokenDigest) &&
		validControllerSHA256(receipt.ModelReservationDigest) &&
		validControllerSHA256(receipt.MutationLeaseTokenDigest) &&
		validControllerSHA256(receipt.MutationReservationDigest) &&
		validControllerSHA256(receipt.ReceiptHash) &&
		validControllerSHA256(receipt.CIAttestationDigest) &&
		validControllerSHA256(receipt.CIResultKey) &&
		validControllerSHA256(receipt.CIEffectivePlanDigest) &&
		validControllerSHA256(receipt.CIExecutionDigest)
}

func equalRepairControllerValidationReceipt(
	run eventing.PRDevelopmentRepairOrchestration,
	fence repairControllerLineFence,
	candidate gitworkspace.PinnedCandidate,
	status eventing.PRDevelopmentCIStatus,
	ciResult localci.RunResult,
) bool {
	receipt := run.Validation
	return receipt != nil &&
		equalRepairControllerValidationFence(receipt, run, fence) &&
		receipt.ParentCommit == candidate.ParentCommit &&
		receipt.CandidateTree == candidate.Tree &&
		receipt.CandidateDigest == candidate.CandidateDigest &&
		receipt.ChangedFiles == candidate.ChangedFiles &&
		receipt.NoChanges == (candidate.ChangedFiles == 0) &&
		receipt.CIStatus == status &&
		receipt.CIAttestationID == ciResult.Attestation.ID &&
		receipt.CIAttestationDigest == ciResult.Attestation.Digest &&
		receipt.CIResultKey == ciResult.Execution.ResultKey &&
		receipt.CIEffectivePlanDigest == ciResult.Plan.Effective.Digest &&
		receipt.CIExecutionDigest == ciResult.Execution.Digest
}

func validRepairControllerCIStatus(status eventing.PRDevelopmentCIStatus) bool {
	switch status {
	case eventing.PRDevelopmentCIPassed,
		eventing.PRDevelopmentCIFailed,
		eventing.PRDevelopmentCIIncomplete,
		eventing.PRDevelopmentCIPlanChanged,
		eventing.PRDevelopmentCITimedOut,
		eventing.PRDevelopmentCICanceled,
		eventing.PRDevelopmentCIOutputLimitExceeded,
		eventing.PRDevelopmentCIEnvironmentUnavailable,
		eventing.PRDevelopmentCIInfrastructureError:
		return true
	default:
		return false
	}
}

func validateRepairControllerCIResult(
	result localci.RunResult,
	identities controllerAttemptIdentities,
	candidate gitworkspace.PinnedCandidate,
) (eventing.PRDevelopmentCIStatus, error) {
	attestation := result.Attestation
	execution := result.Execution
	if attestation.ID != identities.CIAttestation || attestation.OwnerID != identities.CIOwner ||
		attestation.ExecutionDigest != execution.Digest ||
		attestation.ResultKey != execution.ResultKey || attestation.Status != execution.Status ||
		execution.Evidence.ParentCommit != candidate.ParentCommit ||
		execution.Evidence.Tree != candidate.Tree ||
		execution.Evidence.CandidateDigest != candidate.CandidateDigest ||
		execution.Evidence.PlanDigest != result.Plan.Effective.Digest ||
		!validControllerSHA256(attestation.Digest) ||
		!validControllerSHA256(execution.Digest) ||
		!validControllerSHA256(execution.ResultKey) ||
		!validControllerSHA256(result.Plan.Effective.Digest) {
		return "", errors.New("local CI attestation differs from the exact repair candidate")
	}
	switch execution.Status {
	case localci.StatusPassed:
		return eventing.PRDevelopmentCIPassed, nil
	case localci.StatusFailed:
		return eventing.PRDevelopmentCIFailed, nil
	case localci.StatusIncomplete:
		return eventing.PRDevelopmentCIIncomplete, nil
	case localci.StatusPlanChanged:
		return eventing.PRDevelopmentCIPlanChanged, nil
	case localci.StatusTimedOut:
		return eventing.PRDevelopmentCITimedOut, nil
	case localci.StatusCanceled:
		return eventing.PRDevelopmentCICanceled, nil
	case localci.StatusOutputLimitExceeded:
		return eventing.PRDevelopmentCIOutputLimitExceeded, nil
	case localci.StatusEnvironmentUnavailable:
		return eventing.PRDevelopmentCIEnvironmentUnavailable, nil
	case localci.StatusInfrastructureError:
		return eventing.PRDevelopmentCIInfrastructureError, nil
	default:
		return "", fmt.Errorf("unknown terminal local CI status %q", execution.Status)
	}
}

func repairControllerPin(
	session eventing.PRDevelopmentRepairSession,
	reservation string,
) gitworkspace.PinnedAcquireRequest {
	return gitworkspace.PinnedAcquireRequest{
		Repository:     session.CloneURL,
		SourceRef:      session.HeadRef,
		ExpectedCommit: session.HeadSHA,
		ReservationKey: reservation,
		AgentID:        session.AgentID,
	}
}

func (worker *RepairControllerWorker) failBootstrapIfSafe(
	ctx context.Context,
	run eventing.PRDevelopmentRepairOrchestration,
	workspace repairControllerWorkspace,
	releaseReservation string,
	code eventing.PRDevelopmentRepairErrorCode,
	summary string,
	cause error,
) error {
	if run.Phase != eventing.PRDevelopmentRepairOrchestrationBootstrap ||
		run.ControllerID != "" || run.HeadRepository != "" ||
		run.HeadRef != "" || run.HeadSHA != "" || run.CloneURL != "" ||
		run.ReviewDigest != "" || run.WorkspaceID != "" || run.SourceTree != "" {
		return cause
	}
	parentErr := ctx.Err()
	if releaseReservation != "" {
		if workspace == nil {
			return cause
		}
		releaseCtx, cancelRelease := context.WithTimeout(
			context.WithoutCancel(ctx),
			defaultRepairFinishTimeout,
		)
		_, releaseErr := workspace.ReleasePinned(
			releaseCtx,
			gitworkspace.PinnedReleaseRequest{
				ReservationKey: releaseReservation,
				AgentID:        run.AgentID,
			},
		)
		cancelRelease()
		if releaseErr != nil {
			// The DB baseline is still empty and the Bootstrap claim remains
			// reclaimable. Never terminalize while its reservation may be live.
			return fmt.Errorf("release unpinned repair bootstrap workspace: %w", releaseErr)
		}
	}
	if parentErr != nil {
		// Shutdown after a possibly owning pre-Pin effect must still release
		// that reservation, but it need not consume the queued attempt.
		return parentErr
	}
	finishCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		defaultRepairFinishTimeout,
	)
	defer cancel()
	_, _, err := worker.store.FailPRDevelopmentRepairOrchestration(
		finishCtx,
		eventing.PRDevelopmentRepairOrchestrationFail{
			AttemptID:     run.AttemptID,
			ClaimToken:    run.ClaimToken,
			Summary:       boundedRepairSummary(summary),
			ErrorCode:     code,
			InternalError: boundedRepairInternalError(cause),
		},
	)
	if err != nil {
		return fmt.Errorf("safe repair bootstrap failure: %w (original: %v)", err, cause)
	}
	return nil
}
