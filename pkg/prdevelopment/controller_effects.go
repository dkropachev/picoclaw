package prdevelopment

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

var errControllerEffectConflict = errors.New(
	"pull request development controller effect conflict",
)

// controllerOperationJournal is deliberately narrower than the complete
// eventing controller store. The trusted local adapter can only stage and
// finalize exact write-ahead Git effects; it cannot acquire, rotate, or widen
// its own authority.
type controllerOperationJournal interface {
	PreparePRDevelopmentControllerOperation(
		ctx context.Context,
		input eventing.PRDevelopmentControllerOperationPrepare,
	) (eventing.PRDevelopmentControllerOperation, bool, error)
	FinalizePRDevelopmentControllerOperation(
		ctx context.Context,
		input eventing.PRDevelopmentControllerOperationFinalize,
	) (eventing.PRDevelopmentControllerOperationTransition, bool, error)
}

// controllerGitBackend mirrors only the atomic operations whose exact inputs
// and outputs are journaled by eventing. Production always supplies a concrete
// *gitworkspace.Manager through newControllerEffectRunner.
type controllerGitBackend interface {
	AdoptPinnedLine(
		ctx context.Context,
		request gitworkspace.PinnedLineAdoptRequest,
	) (gitworkspace.PinnedLineLease, error)
	ResumePinnedLine(
		ctx context.Context,
		request gitworkspace.PinnedLineResumeRequest,
	) (gitworkspace.PinnedLineLease, error)
	CommitPinned(
		ctx context.Context,
		request gitworkspace.PinnedCommitRequest,
	) (gitworkspace.PinnedCommitResult, error)
	PreviewPinnedLineReview(
		ctx context.Context,
		request gitworkspace.PinnedLineParkRequest,
	) (gitworkspace.PinnedLineReviewSnapshot, error)
	ParkPinnedLine(
		ctx context.Context,
		request gitworkspace.PinnedLineParkRequest,
	) (gitworkspace.PinnedLineParkResult, error)
	SnapshotPinnedLineReview(
		ctx context.Context,
		request gitworkspace.PinnedLineReviewRequest,
	) (gitworkspace.PinnedLineReviewSnapshot, error)
}

var (
	_ controllerOperationJournal = (*eventing.Store)(nil)
	_ controllerGitBackend       = (*gitworkspace.Manager)(nil)
)

// controllerEffectRunner is the trusted write-ahead boundary between one
// exact mutation controller lease and gitworkspace. All authority-bearing
// values are unexported and are actively erased after Park finalizes. The type
// therefore JSON-encodes as an empty object even if it is accidentally handed
// to a browser-facing encoder.
//
// A runner is intentionally serial. Its small in-memory caches make ambiguous
// same-process retries reuse an already durable operation and pre-Park review;
// process-crash reconciliation belongs to the durable operation recovery path.
type controllerEffectRunner struct {
	mu sync.Mutex

	journal controllerOperationJournal
	git     controllerGitBackend

	source       controllerEffectSource
	controller   eventing.PRDevelopmentController
	leaseToken   string
	reservation  string
	identities   controllerAttemptIdentities
	commitTime   time.Time
	prepared     map[eventing.PRDevelopmentControllerOperationKind]eventing.PRDevelopmentControllerOperation
	parkPreview  *controllerParkPreview
	terminalPark *controllerTerminalPark
}

type controllerEffectSource struct {
	sessionID      string
	attemptID      string
	agentID        string
	headRepository string
	cloneURL       string
	sourceRef      string
	sourceCommit   string
	workspaceID    string
}

// controllerLineState is non-authorizing high-water evidence returned after
// Adopt or Resume. It deliberately omits the mutation reservation and lease.
type controllerLineState struct {
	ControllerID  string
	WorkspaceID   string
	LineID        string
	Revision      int64
	LineVersion   int64
	MutationEpoch int64
	Tip           string
	Tree          string
}

// controllerCommitOutcome binds Park to the exact candidate inspected by
// CommitCandidate. A zero-change outcome is still bound to the controller and
// attempt; callers cannot use the Go zero value to bypass candidate inspection.
type controllerCommitOutcome struct {
	controllerID    string
	attemptID       string
	workspaceID     string
	parentCommit    string
	tree            string
	candidateDigest string
	commit          string
	changedFiles    int
	changed         bool
}

type controllerParkPreview struct {
	request  eventing.PRDevelopmentControllerOperationRequest
	snapshot gitworkspace.PinnedLineReviewSnapshot
}

type controllerTerminalPark struct {
	request    eventing.PRDevelopmentControllerOperationRequest
	commit     controllerCommitOutcome
	summary    string
	iterations int
	fence      eventing.PRDevelopmentAttemptReviewFence
}

// newControllerEffectRunner installs the production concrete Git manager
// behind the narrow trusted adapter.
func newControllerEffectRunner(
	journal controllerOperationJournal,
	manager *gitworkspace.Manager,
	session eventing.PRDevelopmentRepairSession,
	lease eventing.PRDevelopmentControllerLease,
) (*controllerEffectRunner, error) {
	if manager == nil {
		return nil, errors.New("controller Git workspace manager is unavailable")
	}
	return newControllerEffectRunnerWithBackend(journal, manager, session, lease)
}

func newControllerEffectRunnerWithBackend(
	journal controllerOperationJournal,
	git controllerGitBackend,
	session eventing.PRDevelopmentRepairSession,
	lease eventing.PRDevelopmentControllerLease,
) (*controllerEffectRunner, error) {
	if journal == nil || git == nil {
		return nil, errors.New("controller effect dependencies are unavailable")
	}
	controller := lease.Controller
	if lease.ReviewFence != nil || controller.ID == "" || controller.OwnerSessionID == "" ||
		controller.CurrentAttemptID == "" || controller.AgentID == "" ||
		controller.LineID == "" || controller.Revision < 1 ||
		controller.Phase != eventing.PRDevelopmentControllerMutation ||
		controller.LeaseKind != eventing.PRDevelopmentControllerMutationLease ||
		controller.LeaseToken == "" || controller.LeaseEpoch < 1 ||
		controller.MutationReservationKey == "" || session.ID == "" ||
		session.ID != controller.OwnerSessionID || session.AgentID != controller.AgentID ||
		session.HeadRepository == "" || session.CloneURL == "" || session.HeadRef == "" ||
		session.HeadSHA == "" || session.WorkspaceID == "" || len(session.Attempts) == 0 {
		return nil, fmt.Errorf("%w: mutation owner is incomplete", errControllerEffectConflict)
	}
	attempt := session.Attempts[len(session.Attempts)-1]
	if attempt.ID != controller.CurrentAttemptID || attempt.SessionID != session.ID {
		return nil, fmt.Errorf("%w: mutation attempt is not session-current", errControllerEffectConflict)
	}
	identities, err := newControllerAttemptIdentities(attempt.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: durable identities are invalid", errControllerEffectConflict)
	}
	authoredAt, err := controllerCommitTime(attempt.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("%w: deterministic commit time is invalid", errControllerEffectConflict)
	}
	return &controllerEffectRunner{
		journal: journal,
		git:     git,
		source: controllerEffectSource{
			sessionID:      session.ID,
			attemptID:      attempt.ID,
			agentID:        session.AgentID,
			headRepository: session.HeadRepository,
			cloneURL:       session.CloneURL,
			sourceRef:      session.HeadRef,
			sourceCommit:   session.HeadSHA,
			workspaceID:    session.WorkspaceID,
		},
		controller:  controller,
		leaseToken:  controller.LeaseToken,
		reservation: controller.MutationReservationKey,
		identities:  identities,
		commitTime:  authoredAt,
		prepared:    make(map[eventing.PRDevelopmentControllerOperationKind]eventing.PRDevelopmentControllerOperation),
	}, nil
}

func (runner *controllerEffectRunner) Adopt(
	ctx context.Context,
	expectedSourceTree string,
) (controllerLineState, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if err := runner.requireMutation(false); err != nil {
		return controllerLineState{}, err
	}
	request := runner.baseRequest()
	request.ExpectedTree = expectedSourceTree
	operation, err := runner.prepare(
		ctx,
		eventing.PRDevelopmentControllerOperationAdopt,
		runner.identities.AdoptOperation,
		request,
	)
	if err != nil {
		return controllerLineState{}, err
	}
	lease, err := runner.git.AdoptPinnedLine(ctxOrBackground(ctx), gitworkspace.PinnedLineAdoptRequest{
		Pin:          runner.pinForOperation(operation),
		WorkspaceID:  operation.Request.WorkspaceID,
		LineID:       operation.Request.LineID,
		ExpectedTree: operation.Request.ExpectedTree,
	})
	if err != nil {
		return controllerLineState{}, fmt.Errorf("adopt retained development line: %w", err)
	}
	result := eventing.PRDevelopmentControllerOperationResult{
		WorkspaceID:   lease.WorkspaceID,
		Version:       lease.Version,
		MutationEpoch: lease.MutationEpoch,
		Tip:           lease.Tip,
		Tree:          lease.Tree,
		AlreadyOwned:  lease.AlreadyOwned,
	}
	transition, err := runner.finalize(ctx, operation, result)
	if err != nil {
		return controllerLineState{}, err
	}
	if err = runner.acceptLineTransition(operation, transition, result); err != nil {
		return controllerLineState{}, err
	}
	return lineState(transition.Controller), nil
}

func (runner *controllerEffectRunner) Resume(
	ctx context.Context,
) (controllerLineState, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if err := runner.requireMutation(true); err != nil {
		return controllerLineState{}, err
	}
	if runner.controller.MutationEpoch != runner.controller.LineVersion {
		return controllerLineState{}, fmt.Errorf(
			"%w: retained line is not parked", errControllerEffectConflict,
		)
	}
	request := runner.baseRequest()
	request.ExpectedVersion = runner.controller.LineVersion
	request.ExpectedEpoch = runner.controller.MutationEpoch
	request.ExpectedTip = runner.controller.TipCommit
	request.ExpectedTree = runner.controller.Tree
	operation, err := runner.prepare(
		ctx,
		eventing.PRDevelopmentControllerOperationResume,
		runner.identities.ResumeOperation,
		request,
	)
	if err != nil {
		return controllerLineState{}, err
	}
	lease, err := runner.git.ResumePinnedLine(ctxOrBackground(ctx), gitworkspace.PinnedLineResumeRequest{
		Pin:             runner.pinForOperation(operation),
		WorkspaceID:     operation.Request.WorkspaceID,
		LineID:          operation.Request.LineID,
		ExpectedVersion: operation.Request.ExpectedVersion,
		ExpectedEpoch:   operation.Request.ExpectedEpoch,
		ExpectedTip:     operation.Request.ExpectedTip,
		ExpectedTree:    operation.Request.ExpectedTree,
	})
	if err != nil {
		return controllerLineState{}, fmt.Errorf("resume retained development line: %w", err)
	}
	result := eventing.PRDevelopmentControllerOperationResult{
		WorkspaceID:   lease.WorkspaceID,
		Version:       lease.Version,
		MutationEpoch: lease.MutationEpoch,
		Tip:           lease.Tip,
		Tree:          lease.Tree,
		AlreadyOwned:  lease.AlreadyOwned,
	}
	transition, err := runner.finalize(ctx, operation, result)
	if err != nil {
		return controllerLineState{}, err
	}
	if err = runner.acceptLineTransition(operation, transition, result); err != nil {
		return controllerLineState{}, err
	}
	return lineState(transition.Controller), nil
}

// CommitCandidate stages and commits only a changed candidate. A clean
// validation candidate returns a controller-bound no-change outcome without
// creating either a Commit operation or an empty Git commit.
func (runner *controllerEffectRunner) CommitCandidate(
	ctx context.Context,
	candidate gitworkspace.PinnedCandidate,
	message string,
) (controllerCommitOutcome, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if err := runner.requireActiveLine(); err != nil {
		return controllerCommitOutcome{}, err
	}
	if candidate.WorkspaceID != runner.source.workspaceID ||
		candidate.ParentCommit != runner.controller.TipCommit ||
		!validControllerSHA256(candidate.CandidateDigest) || candidate.ChangedFiles < 0 {
		return controllerCommitOutcome{}, fmt.Errorf(
			"%w: candidate does not match the active line", errControllerEffectConflict,
		)
	}
	if candidate.ChangedFiles == 0 {
		if candidate.Tree != runner.controller.Tree {
			return controllerCommitOutcome{}, fmt.Errorf(
				"%w: no-change candidate tree changed", errControllerEffectConflict,
			)
		}
		return runner.commitOutcome(candidate, gitworkspace.PinnedCommitResult{}, false), nil
	}
	if candidate.Tree == runner.controller.Tree {
		return controllerCommitOutcome{}, fmt.Errorf(
			"%w: changed candidate retained the parent tree", errControllerEffectConflict,
		)
	}
	request := runner.baseRequest()
	request.EffectIntentID = runner.identities.CommitIntent
	request.ExpectedParent = candidate.ParentCommit
	request.ExpectedTree = candidate.Tree
	request.CandidateDigest = candidate.CandidateDigest
	request.CommitMessage = message
	request.AuthoredAt = runner.commitTime
	operation, err := runner.prepare(
		ctx,
		eventing.PRDevelopmentControllerOperationCommit,
		runner.identities.CommitOperation,
		request,
	)
	if err != nil {
		return controllerCommitOutcome{}, err
	}
	committed, err := runner.git.CommitPinned(ctxOrBackground(ctx), gitworkspace.PinnedCommitRequest{
		Pin:                     runner.pinForOperation(operation),
		WorkspaceID:             operation.Request.WorkspaceID,
		IntentID:                operation.Request.EffectIntentID,
		ExpectedParent:          operation.Request.ExpectedParent,
		ExpectedTree:            operation.Request.ExpectedTree,
		ExpectedCandidateDigest: operation.Request.CandidateDigest,
		Message:                 operation.Request.CommitMessage,
		AuthoredAt:              operation.Request.AuthoredAt,
	})
	if err != nil {
		return controllerCommitOutcome{}, fmt.Errorf("commit validated repair candidate: %w", err)
	}
	result := eventing.PRDevelopmentControllerOperationResult{
		WorkspaceID:     committed.WorkspaceID,
		Tree:            committed.Tree,
		WorkspaceClean:  committed.WorkspaceClean,
		AlreadyApplied:  committed.AlreadyApplied,
		IntentID:        committed.IntentID,
		ParentCommit:    committed.ParentCommit,
		CandidateDigest: committed.CandidateDigest,
		Commit:          committed.Commit,
		ChangedFiles:    committed.ChangedFiles,
	}
	transition, err := runner.finalize(ctx, operation, result)
	if err != nil {
		return controllerCommitOutcome{}, err
	}
	if err = runner.acceptCommitTransition(operation, transition, result); err != nil {
		return controllerCommitOutcome{}, err
	}
	return runner.commitOutcome(candidate, committed, true), nil
}

// Park preflights the exact bounded review before staging or applying Park,
// then compares the complete post-Park snapshot with that preview before the
// eventing transaction is allowed to retire mutation authority.
func (runner *controllerEffectRunner) Park(
	ctx context.Context,
	commit controllerCommitOutcome,
	summary string,
	iterations int,
	beforeFinalize func(),
) (eventing.PRDevelopmentAttemptReviewFence, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.terminalPark != nil {
		if runner.terminalPark.commit != commit || runner.terminalPark.summary != summary ||
			runner.terminalPark.iterations != iterations {
			return eventing.PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
				"%w: terminal Park replay changed", errControllerEffectConflict,
			)
		}
		return runner.terminalPark.fence, nil
	}
	request, err := runner.parkRequest(commit, summary, iterations)
	if err != nil {
		return eventing.PRDevelopmentAttemptReviewFence{}, err
	}
	if err = runner.requireActiveLine(); err != nil {
		return eventing.PRDevelopmentAttemptReviewFence{}, err
	}
	parkRequest := runner.gitParkRequest(request)
	if runner.parkPreview == nil {
		preview, previewErr := runner.git.PreviewPinnedLineReview(
			ctxOrBackground(ctx), parkRequest,
		)
		if previewErr != nil {
			return eventing.PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
				"preview retained development line review: %w", previewErr,
			)
		}
		if previewErr = requireReviewSnapshot(request, preview); previewErr != nil {
			return eventing.PRDevelopmentAttemptReviewFence{}, previewErr
		}
		runner.parkPreview = &controllerParkPreview{request: request, snapshot: preview}
	} else if runner.parkPreview.request != request {
		return eventing.PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
			"%w: Park proposal changed after review preview", errControllerEffectConflict,
		)
	}
	operation, err := runner.prepare(
		ctx,
		eventing.PRDevelopmentControllerOperationPark,
		runner.identities.ParkOperation,
		request,
	)
	if err != nil {
		return eventing.PRDevelopmentAttemptReviewFence{}, err
	}
	// Execute only the canonical operation returned by the journal. prepare
	// already required exact equality with the preflighted proposal.
	parkRequest = runner.gitParkRequest(operation.Request)
	parked, err := runner.git.ParkPinnedLine(ctxOrBackground(ctx), parkRequest)
	if err != nil {
		return eventing.PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
			"park retained development line: %w", err,
		)
	}
	if err = requireParkResult(operation.Request, parked); err != nil {
		return eventing.PRDevelopmentAttemptReviewFence{}, err
	}
	snapshot, err := runner.git.SnapshotPinnedLineReview(
		ctxOrBackground(ctx),
		gitworkspace.PinnedLineReviewRequest{
			LineID:          operation.Request.LineID,
			ExpectedVersion: operation.Request.ExpectedVersion + 1,
			ExpectedBase:    operation.Request.PreviousTip,
			ExpectedTip:     operation.Request.Tip,
			ExpectedTree:    operation.Request.Tree,
		},
	)
	if err != nil {
		return eventing.PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
			"snapshot parked development line review: %w", err,
		)
	}
	if err = requireReviewSnapshot(operation.Request, snapshot); err != nil {
		return eventing.PRDevelopmentAttemptReviewFence{}, err
	}
	if !equalReviewSnapshots(runner.parkPreview.snapshot, snapshot) {
		return eventing.PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
			"%w: parked review differs from its preflight", errControllerEffectConflict,
		)
	}
	result := eventing.PRDevelopmentControllerOperationResult{
		WorkspaceID:         parked.WorkspaceID,
		Version:             parked.Version,
		MutationEpoch:       parked.MutationEpoch,
		PreviousTip:         parked.PreviousTip,
		Tip:                 parked.Tip,
		Tree:                parked.Tree,
		NoChanges:           parked.NoChanges,
		WorkspaceClean:      parked.WorkspaceClean,
		AlreadyParked:       parked.AlreadyParked,
		ReviewVersion:       snapshot.Version,
		ReviewMutationEpoch: snapshot.MutationEpoch,
		ReviewParkIntentID:  snapshot.ParkIntentID,
		ReviewBaseCommit:    snapshot.BaseCommit,
		ReviewCommit:        snapshot.Commit,
		ReviewTree:          snapshot.Tree,
		ReviewDigest:        snapshot.ReviewDigest,
	}
	// Keep both durable leases alive through all potentially slow Git and
	// review-snapshot work. The caller's barrier drains renewal I/O only at the
	// final atomic store boundary, where Finalize retires both authorities.
	if beforeFinalize != nil {
		beforeFinalize()
	}
	transition, err := runner.finalize(ctx, operation, result)
	if err != nil {
		return eventing.PRDevelopmentAttemptReviewFence{}, err
	}
	fence, err := runner.acceptParkTransition(operation, transition, result)
	if err != nil {
		return eventing.PRDevelopmentAttemptReviewFence{}, err
	}
	runner.terminalPark = &controllerTerminalPark{
		request:    request,
		commit:     commit,
		summary:    summary,
		iterations: iterations,
		fence:      fence,
	}
	runner.parkPreview = nil
	runner.leaseToken = ""
	runner.reservation = ""
	return fence, nil
}

func (runner *controllerEffectRunner) baseRequest() eventing.PRDevelopmentControllerOperationRequest {
	return eventing.PRDevelopmentControllerOperationRequest{
		Repository:   runner.source.headRepository,
		SourceRef:    runner.source.sourceRef,
		SourceCommit: runner.source.sourceCommit,
		AgentID:      runner.source.agentID,
		WorkspaceID:  runner.source.workspaceID,
		LineID:       runner.controller.LineID,
	}
}

func (runner *controllerEffectRunner) prepare(
	ctx context.Context,
	kind eventing.PRDevelopmentControllerOperationKind,
	operationID string,
	request eventing.PRDevelopmentControllerOperationRequest,
) (eventing.PRDevelopmentControllerOperation, error) {
	if cached, ok := runner.prepared[kind]; ok {
		if err := runner.requirePreparedOperation(cached, kind, operationID, request); err != nil {
			return eventing.PRDevelopmentControllerOperation{}, err
		}
		return cached, nil
	}
	operation, _, err := runner.journal.PreparePRDevelopmentControllerOperation(
		ctxOrBackground(ctx),
		eventing.PRDevelopmentControllerOperationPrepare{
			OperationID:      operationID,
			ControllerID:     runner.controller.ID,
			AttemptID:        runner.source.attemptID,
			ExpectedRevision: runner.controller.Revision,
			LeaseToken:       runner.leaseToken,
			LeaseEpoch:       runner.controller.LeaseEpoch,
			Kind:             kind,
			Request:          request,
		},
	)
	if err != nil {
		return eventing.PRDevelopmentControllerOperation{}, fmt.Errorf(
			"prepare controller %s operation: %w", kind, err,
		)
	}
	if err = runner.requirePreparedOperation(operation, kind, operationID, request); err != nil {
		return eventing.PRDevelopmentControllerOperation{}, err
	}
	runner.prepared[kind] = operation
	return operation, nil
}

func (runner *controllerEffectRunner) finalize(
	ctx context.Context,
	operation eventing.PRDevelopmentControllerOperation,
	result eventing.PRDevelopmentControllerOperationResult,
) (eventing.PRDevelopmentControllerOperationTransition, error) {
	transition, _, err := runner.journal.FinalizePRDevelopmentControllerOperation(
		ctxOrBackground(ctx),
		eventing.PRDevelopmentControllerOperationFinalize{
			ControllerID:     operation.ControllerID,
			AttemptID:        operation.AttemptID,
			OperationID:      operation.ID,
			ExpectedRevision: operation.PreparedControllerRevision,
			LeaseToken:       runner.leaseToken,
			LeaseEpoch:       operation.MutationLeaseEpoch,
			Result:           result,
		},
	)
	if err != nil {
		return eventing.PRDevelopmentControllerOperationTransition{}, fmt.Errorf(
			"finalize controller %s operation: %w", operation.Kind, err,
		)
	}
	delete(runner.prepared, operation.Kind)
	return transition, nil
}

func (runner *controllerEffectRunner) requirePreparedOperation(
	operation eventing.PRDevelopmentControllerOperation,
	kind eventing.PRDevelopmentControllerOperationKind,
	operationID string,
	request eventing.PRDevelopmentControllerOperationRequest,
) error {
	statusCurrent := operation.Status == eventing.PRDevelopmentControllerOperationPending ||
		runner.isFinalizedCommitPrepareReplay(operation, kind)
	if operation.ID != operationID || operation.ControllerID != runner.controller.ID ||
		operation.AttemptID != runner.source.attemptID || operation.Kind != kind ||
		!statusCurrent ||
		operation.PreparedControllerRevision != runner.controller.Revision ||
		operation.AgentID != runner.source.agentID ||
		operation.WorkspaceID != runner.source.workspaceID ||
		operation.LineID != runner.controller.LineID ||
		operation.SourceCloneURL != runner.source.cloneURL ||
		operation.SourceRef != runner.source.sourceRef ||
		operation.SourceCommit != runner.source.sourceCommit ||
		operation.MutationLeaseEpoch != runner.controller.LeaseEpoch ||
		operation.MutationReservationDigest == "" ||
		operation.MutationLeaseTokenDigest == "" ||
		operation.EffectIntentID != request.EffectIntentID ||
		operation.Request != request {
		return fmt.Errorf(
			"%w: journal returned a noncanonical %s operation",
			errControllerEffectConflict,
			kind,
		)
	}
	return nil
}

func (runner *controllerEffectRunner) isFinalizedCommitPrepareReplay(
	operation eventing.PRDevelopmentControllerOperation,
	kind eventing.PRDevelopmentControllerOperationKind,
) bool {
	result := operation.Result
	return kind == eventing.PRDevelopmentControllerOperationCommit &&
		operation.Status == eventing.PRDevelopmentControllerOperationFinalized &&
		operation.RecoveryStagedAt == nil &&
		operation.FinalizedAt != nil &&
		operation.FinalControllerRevision == operation.PreparedControllerRevision &&
		operation.FinalControllerPhase == eventing.PRDevelopmentControllerMutation &&
		operation.FinalFenceHash == "" &&
		validControllerSHA256(operation.ResultHash) &&
		validControllerSHA256(operation.FinalHash) &&
		operation.SourceTree == runner.controller.SourceTree &&
		operation.LineVersion == runner.controller.LineVersion &&
		operation.MutationEpoch == runner.controller.MutationEpoch &&
		operation.TipCommit == runner.controller.TipCommit &&
		operation.Tree == runner.controller.Tree &&
		result.WorkspaceID == operation.WorkspaceID &&
		result.IntentID == operation.Request.EffectIntentID &&
		result.ParentCommit == operation.Request.ExpectedParent &&
		result.Tree == operation.Request.ExpectedTree &&
		result.CandidateDigest == operation.Request.CandidateDigest &&
		validObjectID(result.Commit) && result.Commit != result.ParentCommit &&
		len(result.Commit) == len(result.ParentCommit) &&
		result.ChangedFiles > 0 && result.WorkspaceClean &&
		!result.AlreadyOwned && !result.AlreadyApplied && !result.AlreadyParked
}

func (runner *controllerEffectRunner) pinForOperation(
	operation eventing.PRDevelopmentControllerOperation,
) gitworkspace.PinnedAcquireRequest {
	return gitworkspace.PinnedAcquireRequest{
		Repository:     operation.SourceCloneURL,
		SourceRef:      operation.Request.SourceRef,
		ExpectedCommit: operation.Request.SourceCommit,
		ReservationKey: runner.reservation,
		AgentID:        operation.Request.AgentID,
	}
}

func (runner *controllerEffectRunner) gitParkRequest(
	request eventing.PRDevelopmentControllerOperationRequest,
) gitworkspace.PinnedLineParkRequest {
	return gitworkspace.PinnedLineParkRequest{
		Pin: gitworkspace.PinnedAcquireRequest{
			Repository:     runner.source.cloneURL,
			SourceRef:      request.SourceRef,
			ExpectedCommit: request.SourceCommit,
			ReservationKey: runner.reservation,
			AgentID:        request.AgentID,
		},
		WorkspaceID:     request.WorkspaceID,
		LineID:          request.LineID,
		IntentID:        request.EffectIntentID,
		ExpectedVersion: request.ExpectedVersion,
		MutationEpoch:   request.MutationEpoch,
		PreviousTip:     request.PreviousTip,
		Tip:             request.Tip,
		Tree:            request.Tree,
		NoChanges:       request.NoChanges,
	}
}

func (runner *controllerEffectRunner) parkRequest(
	commit controllerCommitOutcome,
	summary string,
	iterations int,
) (eventing.PRDevelopmentControllerOperationRequest, error) {
	if commit.controllerID != runner.controller.ID ||
		commit.attemptID != runner.source.attemptID ||
		commit.workspaceID != runner.source.workspaceID ||
		commit.parentCommit != runner.controller.TipCommit {
		return eventing.PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
			"%w: Park candidate is not controller-bound", errControllerEffectConflict,
		)
	}
	request := runner.baseRequest()
	request.EffectIntentID = runner.identities.ParkIntent
	request.ExpectedVersion = runner.controller.LineVersion
	request.MutationEpoch = runner.controller.MutationEpoch
	request.PreviousTip = runner.controller.TipCommit
	request.CompletionSummary = summary
	request.CompletionIterations = iterations
	if commit.changed {
		if commit.commit == "" || commit.commit == commit.parentCommit ||
			commit.tree == runner.controller.Tree || commit.changedFiles < 1 ||
			!validControllerSHA256(commit.candidateDigest) {
			return eventing.PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
				"%w: changed Park candidate is incomplete", errControllerEffectConflict,
			)
		}
		request.Tip = commit.commit
		request.Tree = commit.tree
		request.NoChanges = false
	} else {
		if commit.commit != "" || commit.changedFiles != 0 ||
			commit.tree != runner.controller.Tree {
			return eventing.PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
				"%w: no-change Park candidate changed", errControllerEffectConflict,
			)
		}
		request.Tip = runner.controller.TipCommit
		request.Tree = runner.controller.Tree
		request.NoChanges = true
	}
	return request, nil
}

func (runner *controllerEffectRunner) commitOutcome(
	candidate gitworkspace.PinnedCandidate,
	committed gitworkspace.PinnedCommitResult,
	changed bool,
) controllerCommitOutcome {
	outcome := controllerCommitOutcome{
		controllerID:    runner.controller.ID,
		attemptID:       runner.source.attemptID,
		workspaceID:     runner.source.workspaceID,
		parentCommit:    candidate.ParentCommit,
		tree:            candidate.Tree,
		candidateDigest: candidate.CandidateDigest,
		changedFiles:    candidate.ChangedFiles,
		changed:         changed,
	}
	if changed {
		outcome.commit = committed.Commit
		outcome.tree = committed.Tree
		outcome.candidateDigest = committed.CandidateDigest
		outcome.changedFiles = committed.ChangedFiles
	}
	return outcome
}

func (runner *controllerEffectRunner) requireMutation(unboundForbidden bool) error {
	if runner.controller.Phase != eventing.PRDevelopmentControllerMutation ||
		runner.controller.LeaseKind != eventing.PRDevelopmentControllerMutationLease ||
		runner.controller.LeaseToken == "" || runner.controller.LeaseToken != runner.leaseToken ||
		runner.controller.MutationReservationKey == "" ||
		runner.controller.MutationReservationKey != runner.reservation ||
		runner.controller.CurrentAttemptID != runner.source.attemptID {
		return fmt.Errorf("%w: mutation authority changed", errControllerEffectConflict)
	}
	if unboundForbidden && runner.controller.WorkspaceID == "" {
		return fmt.Errorf("%w: retained line is unbound", errControllerEffectConflict)
	}
	if !unboundForbidden && runner.controller.WorkspaceID != "" {
		return fmt.Errorf("%w: retained line is already bound", errControllerEffectConflict)
	}
	return nil
}

func (runner *controllerEffectRunner) requireActiveLine() error {
	if err := runner.requireMutation(true); err != nil {
		return err
	}
	if runner.controller.WorkspaceID != runner.source.workspaceID ||
		runner.controller.SourceCloneURL != runner.source.cloneURL ||
		runner.controller.SourceRef != runner.source.sourceRef ||
		runner.controller.SourceCommit != runner.source.sourceCommit ||
		runner.controller.SourceTree == "" || runner.controller.TipCommit == "" ||
		runner.controller.Tree == "" ||
		runner.controller.MutationEpoch != runner.controller.LineVersion+1 {
		return fmt.Errorf("%w: active retained line changed", errControllerEffectConflict)
	}
	return nil
}

func (runner *controllerEffectRunner) acceptLineTransition(
	operation eventing.PRDevelopmentControllerOperation,
	transition eventing.PRDevelopmentControllerOperationTransition,
	result eventing.PRDevelopmentControllerOperationResult,
) error {
	if err := requireFinalizedOperation(operation, transition, result); err != nil {
		return err
	}
	controller := transition.Controller
	if transition.Fence != nil || controller.ID != runner.controller.ID ||
		controller.OwnerSessionID != runner.source.sessionID ||
		controller.CurrentAttemptID != runner.source.attemptID ||
		controller.Phase != eventing.PRDevelopmentControllerMutation ||
		controller.Revision != operation.PreparedControllerRevision+1 ||
		controller.LeaseKind != eventing.PRDevelopmentControllerMutationLease ||
		controller.LeaseToken != runner.leaseToken ||
		controller.LeaseEpoch != operation.MutationLeaseEpoch ||
		controller.MutationReservationKey != runner.reservation ||
		controller.WorkspaceID != result.WorkspaceID ||
		controller.LineVersion != result.Version ||
		controller.MutationEpoch != result.MutationEpoch ||
		controller.TipCommit != result.Tip || controller.Tree != result.Tree {
		return fmt.Errorf("%w: line finalization changed", errControllerEffectConflict)
	}
	runner.controller = controller
	return nil
}

func (runner *controllerEffectRunner) acceptCommitTransition(
	operation eventing.PRDevelopmentControllerOperation,
	transition eventing.PRDevelopmentControllerOperationTransition,
	result eventing.PRDevelopmentControllerOperationResult,
) error {
	if err := requireFinalizedOperation(operation, transition, result); err != nil {
		return err
	}
	controller := transition.Controller
	if transition.Fence != nil || controller.ID != runner.controller.ID ||
		controller.CurrentAttemptID != runner.source.attemptID ||
		controller.Phase != eventing.PRDevelopmentControllerMutation ||
		controller.Revision != operation.PreparedControllerRevision ||
		controller.LeaseKind != eventing.PRDevelopmentControllerMutationLease ||
		controller.LeaseToken != runner.leaseToken ||
		controller.LeaseEpoch != operation.MutationLeaseEpoch ||
		controller.MutationReservationKey != runner.reservation ||
		controller.WorkspaceID != runner.controller.WorkspaceID ||
		controller.LineVersion != runner.controller.LineVersion ||
		controller.MutationEpoch != runner.controller.MutationEpoch ||
		controller.TipCommit != runner.controller.TipCommit ||
		controller.Tree != runner.controller.Tree {
		return fmt.Errorf("%w: Commit finalization changed controller", errControllerEffectConflict)
	}
	runner.controller = controller
	return nil
}

func (runner *controllerEffectRunner) acceptParkTransition(
	operation eventing.PRDevelopmentControllerOperation,
	transition eventing.PRDevelopmentControllerOperationTransition,
	result eventing.PRDevelopmentControllerOperationResult,
) (eventing.PRDevelopmentAttemptReviewFence, error) {
	if err := requireFinalizedOperation(operation, transition, result); err != nil {
		return eventing.PRDevelopmentAttemptReviewFence{}, err
	}
	controller := transition.Controller
	if transition.Fence == nil || controller.ID != runner.controller.ID ||
		controller.CurrentAttemptID != runner.source.attemptID ||
		controller.Phase != eventing.PRDevelopmentControllerReviewPending ||
		controller.Revision != operation.PreparedControllerRevision+1 ||
		controller.LeaseKind != "" || controller.LeaseOwner != "" ||
		controller.LeaseToken != "" || controller.LeaseUntil != nil ||
		controller.MutationReservationKey != "" ||
		controller.LineVersion != result.Version ||
		controller.MutationEpoch != result.MutationEpoch ||
		controller.TipCommit != result.Tip || controller.Tree != result.Tree {
		return eventing.PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
			"%w: Park did not retire mutation authority", errControllerEffectConflict,
		)
	}
	fence := *transition.Fence
	if fence.AttemptID != runner.source.attemptID ||
		fence.ControllerID != runner.controller.ID ||
		fence.LineID != runner.controller.LineID ||
		fence.LineVersion != result.Version ||
		fence.MutationEpoch != result.MutationEpoch ||
		fence.ParkIntentID != result.ReviewParkIntentID ||
		fence.BaseCommit != result.ReviewBaseCommit ||
		fence.TipCommit != result.ReviewCommit || fence.Tree != result.ReviewTree ||
		fence.NoChanges != result.NoChanges ||
		fence.LineReviewDigest != result.ReviewDigest {
		return eventing.PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
			"%w: Park review fence changed", errControllerEffectConflict,
		)
	}
	runner.controller = controller
	return fence, nil
}

func requireFinalizedOperation(
	prepared eventing.PRDevelopmentControllerOperation,
	transition eventing.PRDevelopmentControllerOperationTransition,
	result eventing.PRDevelopmentControllerOperationResult,
) error {
	durableResult := result
	durableResult.AlreadyOwned = false
	durableResult.AlreadyApplied = false
	durableResult.AlreadyParked = false
	operation := transition.Operation
	if operation.ID != prepared.ID || operation.ControllerID != prepared.ControllerID ||
		operation.AttemptID != prepared.AttemptID || operation.Kind != prepared.Kind ||
		operation.Status != eventing.PRDevelopmentControllerOperationFinalized ||
		operation.PreparedControllerRevision != prepared.PreparedControllerRevision ||
		operation.MutationLeaseEpoch != prepared.MutationLeaseEpoch ||
		operation.Request != prepared.Request || operation.Result != durableResult {
		return fmt.Errorf("%w: operation finalization changed", errControllerEffectConflict)
	}
	return nil
}

func requireParkResult(
	request eventing.PRDevelopmentControllerOperationRequest,
	result gitworkspace.PinnedLineParkResult,
) error {
	if result.WorkspaceID != request.WorkspaceID ||
		result.Version != request.ExpectedVersion+1 ||
		result.MutationEpoch != request.MutationEpoch ||
		result.PreviousTip != request.PreviousTip || result.Tip != request.Tip ||
		result.Tree != request.Tree || result.NoChanges != request.NoChanges ||
		!result.WorkspaceClean {
		return fmt.Errorf("%w: Park result changed", errControllerEffectConflict)
	}
	return nil
}

func requireReviewSnapshot(
	request eventing.PRDevelopmentControllerOperationRequest,
	snapshot gitworkspace.PinnedLineReviewSnapshot,
) error {
	if snapshot.Version != request.ExpectedVersion+1 ||
		snapshot.MutationEpoch != request.MutationEpoch ||
		snapshot.ParkIntentID != request.EffectIntentID ||
		snapshot.BaseCommit != request.PreviousTip || snapshot.Commit != request.Tip ||
		snapshot.Tree != request.Tree || !validControllerSHA256(snapshot.ReviewDigest) {
		return fmt.Errorf("%w: review snapshot changed", errControllerEffectConflict)
	}
	return nil
}

func equalReviewSnapshots(
	left, right gitworkspace.PinnedLineReviewSnapshot,
) bool {
	return left.Version == right.Version &&
		left.MutationEpoch == right.MutationEpoch &&
		left.ParkIntentID == right.ParkIntentID &&
		left.BaseCommit == right.BaseCommit && left.Commit == right.Commit &&
		left.Tree == right.Tree && slices.Equal(left.ChangedPaths, right.ChangedPaths) &&
		left.UnifiedDiff == right.UnifiedDiff && left.ReviewDigest == right.ReviewDigest
}

func lineState(controller eventing.PRDevelopmentController) controllerLineState {
	return controllerLineState{
		ControllerID:  controller.ID,
		WorkspaceID:   controller.WorkspaceID,
		LineID:        controller.LineID,
		Revision:      controller.Revision,
		LineVersion:   controller.LineVersion,
		MutationEpoch: controller.MutationEpoch,
		Tip:           controller.TipCommit,
		Tree:          controller.Tree,
	}
}

func validControllerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
