package prdevelopment

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

const (
	defaultControllerRecoveryWorkerLabel                = "gateway-pr-development-recovery"
	controllerRecoveryStageBatch                        = 32
	controllerRecoveryClaimIDPrefix                     = "pdrc_"
	controllerSuspensionClaimIDPrefix                   = "pdsc_"
	controllerSuspendedResumeRecoveryClaimIDPrefix      = "pdsrrc_"
	controllerSuspendedResumeRecoveryChildClaimIDPrefix = "pdsrrs_"
	controllerSuspensionChangedFilesMax                 = 10_000
)

// ControllerRecoveryWorkspaceFactory resolves the Git manager owned by the
// caller's current runtime generation. The caller must retain that generation
// for the complete ProcessOne call.
type ControllerRecoveryWorkspaceFactory func() (*gitworkspace.Manager, error)

// ControllerRecoveryWorkerConfig contains only trusted process-local recovery
// dependencies. It intentionally has no provider, model, CI, or workflow
// capability.
type ControllerRecoveryWorkerConfig struct {
	Store       *eventing.Store
	Workspaces  ControllerRecoveryWorkspaceFactory
	WorkerLabel string
	// LeaseDuration defaults to five minutes. A nonzero value must satisfy the
	// same heartbeat floor as the other controller workers.
	LeaseDuration time.Duration
}

type controllerRecoveryStore interface {
	eventing.PRDevelopmentControllerRecoveryScanner
	eventing.PRDevelopmentControllerSuspensionExecutionStore
	eventing.PRDevelopmentControllerSuspendedResumeRecoveryStore
	ClaimPRDevelopmentControllerRecovery(
		ctx context.Context,
		input eventing.PRDevelopmentControllerRecoveryClaim,
	) (eventing.PRDevelopmentControllerRecoveryLease, bool, error)
	RenewPRDevelopmentControllerRecovery(
		ctx context.Context,
		input eventing.PRDevelopmentControllerRecoveryRenew,
	) error
	FinalizePRDevelopmentControllerRecovery(
		ctx context.Context,
		input eventing.PRDevelopmentControllerRecoveryFinalize,
	) (eventing.PRDevelopmentController, bool, error)
	ClaimPRDevelopmentControllerOperationRecovery(
		ctx context.Context,
		input eventing.PRDevelopmentControllerOperationRecoveryClaim,
	) (eventing.PRDevelopmentControllerOperationRecoveryLease, bool, error)
	RenewPRDevelopmentControllerOperationRecovery(
		ctx context.Context,
		input eventing.PRDevelopmentControllerOperationRecoveryRenew,
	) error
	FinalizePRDevelopmentControllerOperationRecovery(
		ctx context.Context,
		input eventing.PRDevelopmentControllerOperationRecoveryFinalize,
	) (eventing.PRDevelopmentControllerOperationTransition, bool, error)
}

// controllerRecoveryGit exposes exactly the local, replayable effects needed
// to reconcile an expired controller operation. In particular, it cannot
// acquire or release a generic workspace and has no remote operation.
type controllerRecoveryGit interface {
	ResumeSuspendedPinnedLine(
		ctx context.Context,
		request gitworkspace.PinnedLineSuspendedResumeRequest,
	) (gitworkspace.PinnedLineSuspendedResumeResult, error)
	SuspendPinnedLine(
		ctx context.Context,
		request gitworkspace.PinnedLineSuspendRequest,
	) (gitworkspace.PinnedLineSuspendResult, error)
	SuspendPinnedLineCommitRecovery(
		ctx context.Context,
		request gitworkspace.PinnedLineCommitSuspensionRequest,
	) (gitworkspace.PinnedLineSuspendResult, error)
	RotatePinnedReservation(
		ctx context.Context,
		request gitworkspace.PinnedReservationRotationRequest,
	) (gitworkspace.PinnedReservationRotationResult, error)
	RecoverPinnedLineAdoptReservation(
		ctx context.Context,
		request gitworkspace.PinnedLineAdoptRecoveryRequest,
	) (gitworkspace.PinnedLineReservationRecoveryResult, error)
	RecoverPinnedLineResumeReservation(
		ctx context.Context,
		request gitworkspace.PinnedLineResumeRecoveryRequest,
	) (gitworkspace.PinnedLineReservationRecoveryResult, error)
	CommitPinned(
		ctx context.Context,
		request gitworkspace.PinnedCommitRequest,
	) (gitworkspace.PinnedCommitResult, error)
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
	_ controllerRecoveryStore = (*eventing.Store)(nil)
	_ controllerRecoveryGit   = (*gitworkspace.Manager)(nil)
)

type controllerRecoveryWorkerDependencies struct {
	store             controllerRecoveryStore
	workspaces        func() (controllerRecoveryGit, error)
	workerLabel       string
	lease             time.Duration
	heartbeatInterval func(time.Duration) time.Duration
}

// ControllerRecoveryWorker reconciles at most one durable suspension or exact
// recovery per call. Every non-Park recovery is finalized into
// suspension_pending; Park moves directly to the existing reservation-free
// review handoff.
type ControllerRecoveryWorker struct {
	controllerRecoveryWorkerDependencies
}

// NewControllerRecoveryWorker constructs the production recovery worker.
func NewControllerRecoveryWorker(
	config ControllerRecoveryWorkerConfig,
) (*ControllerRecoveryWorker, error) {
	if config.Store == nil {
		return nil, fmt.Errorf("%w: controller recovery store is required", ErrUnavailable)
	}
	if config.Workspaces == nil {
		return nil, fmt.Errorf("%w: controller recovery Git factory is required", ErrUnavailable)
	}
	label := strings.TrimSpace(config.WorkerLabel)
	if label == "" {
		label = defaultControllerRecoveryWorkerLabel
	}
	if label != config.WorkerLabel && config.WorkerLabel != "" {
		return nil, errors.New("controller recovery worker label must be exact")
	}
	lease, err := normalizeRepairControllerLease(config.LeaseDuration)
	if err != nil {
		return nil, err
	}
	if lease == 0 {
		lease = defaultRepairLease
	}
	return newControllerRecoveryWorkerWithDependencies(
		controllerRecoveryWorkerDependencies{
			store: config.Store,
			workspaces: func() (controllerRecoveryGit, error) {
				manager, resolveErr := config.Workspaces()
				if resolveErr != nil {
					return nil, resolveErr
				}
				if manager == nil {
					return nil, errors.New("controller recovery Git manager is unavailable")
				}
				return manager, nil
			},
			workerLabel: label,
			lease:       lease,
		},
	)
}

func newControllerRecoveryWorkerWithDependencies(
	dependencies controllerRecoveryWorkerDependencies,
) (*ControllerRecoveryWorker, error) {
	if dependencies.store == nil || isNilServiceValue(dependencies.store) {
		return nil, fmt.Errorf("%w: controller recovery store is required", ErrUnavailable)
	}
	if dependencies.workspaces == nil {
		return nil, fmt.Errorf("%w: controller recovery Git factory is required", ErrUnavailable)
	}
	if dependencies.workerLabel == "" {
		dependencies.workerLabel = defaultControllerRecoveryWorkerLabel
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
	if dependencies.heartbeatInterval == nil {
		dependencies.heartbeatInterval = controllerRecoveryHeartbeatEvery
	}
	return &ControllerRecoveryWorker{
		controllerRecoveryWorkerDependencies: dependencies,
	}, nil
}

// ProcessOne processes an already-durable suspension before it can stage or
// claim another recovery. If staging atomically creates a suspension handoff,
// the second suspension scan consumes that handoff before any other recovery.
// A claim is never released on failure: its idempotent Git effect remains
// reclaimable after the scheduling lease expires.
func (worker *ControllerRecoveryWorker) ProcessOne(ctx context.Context) (bool, error) {
	if worker == nil || worker.store == nil || isNilServiceValue(worker.store) ||
		worker.workspaces == nil {
		return false, ErrUnavailable
	}
	if worker.lease < MinimumRepairControllerLease {
		return false, fmt.Errorf(
			"%w: controller recovery lease is below %s",
			ErrUnavailable,
			MinimumRepairControllerLease,
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	processed, found, err := worker.processNextSuspension(ctx)
	if found || err != nil {
		return processed, err
	}
	processed, found, err = worker.processNextSuspendedResumeRecovery(ctx)
	if found || err != nil {
		return processed, err
	}
	if _, err := worker.store.StageExpiredPRDevelopmentControllerRecoveries(
		ctx,
		controllerRecoveryStageBatch,
	); err != nil {
		return false, fmt.Errorf("stage expired controller recoveries: %w", err)
	}
	processed, found, err = worker.processNextSuspension(ctx)
	if found || err != nil {
		return processed, err
	}
	candidate, found, err := worker.store.NextPRDevelopmentControllerRecovery(ctx)
	if err != nil {
		return false, fmt.Errorf("scan controller recoveries: %w", err)
	}
	if !found {
		return false, nil
	}
	if err = validateControllerRecoveryCandidate(candidate); err != nil {
		return false, err
	}
	claimID := controllerRecoveryClaimID(worker.workerLabel, candidate)

	switch candidate.Kind {
	case eventing.PRDevelopmentControllerRecoveryWorkReservation:
		lease, _, claimErr := worker.store.ClaimPRDevelopmentControllerRecovery(
			ctx,
			eventing.PRDevelopmentControllerRecoveryClaim{
				CaseID:           candidate.CaseID,
				AttemptID:        candidate.AttemptID,
				ExpectedRevision: candidate.ExpectedRevision,
				ClaimID:          claimID,
				WorkerLabel:      worker.workerLabel,
				Lease:            worker.lease,
			},
		)
		if claimErr != nil {
			return false, fmt.Errorf("claim reservation recovery: %w", claimErr)
		}
		if err = validateControllerReservationRecoveryLease(
			candidate,
			claimID,
			worker.workerLabel,
			lease,
		); err != nil {
			return true, err
		}
		handoff, recoveryErr := worker.processReservationRecovery(ctx, lease)
		if recoveryErr != nil {
			return true, recoveryErr
		}
		if !handoff.required {
			return true, nil
		}
		if err = worker.processPostRecoverySuspension(ctx, handoff); err != nil {
			return true, err
		}
		return true, nil

	case eventing.PRDevelopmentControllerRecoveryWorkOperation:
		lease, _, claimErr := worker.store.ClaimPRDevelopmentControllerOperationRecovery(
			ctx,
			eventing.PRDevelopmentControllerOperationRecoveryClaim{
				CaseID:           candidate.CaseID,
				AttemptID:        candidate.AttemptID,
				OperationID:      candidate.OperationID,
				ExpectedRevision: candidate.ExpectedRevision,
				ClaimID:          claimID,
				WorkerLabel:      worker.workerLabel,
				Lease:            worker.lease,
			},
		)
		if claimErr != nil {
			return false, fmt.Errorf("claim operation recovery: %w", claimErr)
		}
		if err = validateControllerOperationRecoveryLease(
			candidate,
			claimID,
			worker.workerLabel,
			lease,
		); err != nil {
			return true, err
		}
		handoff, recoveryErr := worker.processOperationRecovery(ctx, lease)
		if recoveryErr != nil {
			return true, recoveryErr
		}
		if !handoff.required {
			return true, nil
		}
		if err = worker.processPostRecoverySuspension(ctx, handoff); err != nil {
			return true, err
		}
		return true, nil

	default:
		return false, fmt.Errorf("%w: unknown controller recovery kind", ErrUnavailable)
	}
}

type controllerRecoverySuspensionHandoff struct {
	required         bool
	controllerID     string
	attemptID        string
	sourceKind       eventing.PRDevelopmentControllerSuspensionSourceKind
	expectedRevision int64
}

func (worker *ControllerRecoveryWorker) processPostRecoverySuspension(
	ctx context.Context,
	handoff controllerRecoverySuspensionHandoff,
) error {
	processed, found, err := worker.processNextSuspensionFor(
		ctx,
		handoff,
	)
	if err != nil {
		return fmt.Errorf("process controller recovery suspension handoff: %w", err)
	}
	if !found || !processed {
		return errors.New("controller recovery produced no executable suspension handoff")
	}
	return nil
}

func (worker *ControllerRecoveryWorker) processNextSuspension(
	ctx context.Context,
) (processed, found bool, err error) {
	return worker.processNextSuspensionFor(ctx, controllerRecoverySuspensionHandoff{})
}

func (worker *ControllerRecoveryWorker) processNextSuspensionFor(
	ctx context.Context,
	expected controllerRecoverySuspensionHandoff,
) (processed, found bool, err error) {
	candidate, found, err := worker.store.NextPRDevelopmentControllerSuspension(ctx)
	if err != nil {
		return false, false, fmt.Errorf("scan controller suspensions: %w", err)
	}
	if !found {
		return false, false, nil
	}
	if err = validateControllerSuspensionCandidate(candidate); err != nil {
		return false, true, err
	}
	if expected.required &&
		(candidate.ControllerID != expected.controllerID ||
			candidate.AttemptID != expected.attemptID ||
			candidate.SourceKind != expected.sourceKind ||
			candidate.ExpectedRevision != expected.expectedRevision) {
		return false, true, errors.New(
			"controller recovery suspension scan returned a different handoff",
		)
	}
	claimID := controllerSuspensionClaimID(worker.workerLabel, candidate)
	lease, _, err := worker.store.ClaimPRDevelopmentControllerSuspension(
		ctx,
		eventing.PRDevelopmentControllerSuspensionClaim{
			CaseID:           candidate.CaseID,
			SuspensionID:     candidate.SuspensionID,
			ControllerID:     candidate.ControllerID,
			AttemptID:        candidate.AttemptID,
			ExpectedRevision: candidate.ExpectedRevision,
			ClaimID:          claimID,
			WorkerLabel:      worker.workerLabel,
			Lease:            worker.lease,
		},
	)
	if err != nil {
		return false, true, fmt.Errorf("claim controller suspension: %w", err)
	}
	if err = validateControllerSuspensionLease(
		candidate,
		claimID,
		worker.workerLabel,
		lease,
	); err != nil {
		return true, true, err
	}
	return true, true, worker.processSuspension(ctx, lease)
}

func (worker *ControllerRecoveryWorker) processNextSuspendedResumeRecovery(
	ctx context.Context,
) (processed, found bool, err error) {
	candidate, found, err :=
		worker.store.NextPRDevelopmentControllerSuspendedResumeRecovery(ctx)
	if err != nil {
		return false, false, fmt.Errorf("scan suspended resume recoveries: %w", err)
	}
	if !found {
		return false, false, nil
	}
	if err = validateControllerSuspendedResumeRecoveryCandidate(candidate); err != nil {
		return false, true, err
	}
	claimID := controllerSuspendedResumeRecoveryClaimID(worker.workerLabel, candidate)
	lease, _, err := worker.store.ClaimPRDevelopmentControllerSuspendedResumeRecovery(
		ctx,
		eventing.PRDevelopmentControllerSuspendedResumeRecoveryClaim{
			CaseID:           candidate.CaseID,
			SuspensionID:     candidate.SuspensionID,
			ControllerID:     candidate.ControllerID,
			AttemptID:        candidate.AttemptID,
			ExpectedRevision: candidate.ExpectedRevision,
			ClaimID:          claimID,
			WorkerLabel:      worker.workerLabel,
			Lease:            worker.lease,
		},
	)
	if err != nil {
		return false, true, fmt.Errorf("claim suspended resume recovery: %w", err)
	}
	if err = validateControllerSuspendedResumeRecoveryLease(
		candidate,
		claimID,
		worker.workerLabel,
		lease,
	); err != nil {
		return true, true, err
	}
	return true, true, worker.processSuspendedResumeRecovery(ctx, lease)
}

type controllerRecoveryExecute func(
	context.Context,
	controllerRecoveryGit,
) (func(context.Context) error, error)

func (worker *ControllerRecoveryWorker) processClaim(
	ctx context.Context,
	renew func(context.Context, time.Duration) error,
	execute controllerRecoveryExecute,
) error {
	workCtx, heartbeat := startControllerRecoveryHeartbeat(
		ctx,
		renew,
		worker.lease,
		worker.heartbeatInterval(worker.lease),
	)
	workspace, err := worker.workspaces()
	if err != nil || isNilServiceValue(workspace) {
		if err == nil {
			err = errors.New("controller recovery Git factory returned no manager")
		}
		return errors.Join(err, heartbeat.Stop())
	}
	finalize, err := execute(workCtx, workspace)
	if err != nil {
		return errors.Join(err, heartbeat.Stop())
	}
	if finalize == nil {
		return errors.Join(
			errors.New("controller recovery produced no finalization"),
			heartbeat.Stop(),
		)
	}
	// Refresh the exact scheduling claim after the potentially long Git effect.
	// Finalization then starts with a full lease window; the terminal barrier
	// below still drains any ticker renewal that was already in flight.
	if renewErr := renew(workCtx, worker.lease); renewErr != nil {
		return errors.Join(
			fmt.Errorf("renew controller recovery claim before finalization: %w", renewErr),
			heartbeat.Stop(),
		)
	}

	// Fence renewals before the atomic store transition. The work context stays
	// usable, while an overtaken stale-renewal error is suppressed.
	if terminalErr := heartbeat.BeginTerminal(); terminalErr != nil {
		return errors.Join(terminalErr, heartbeat.Stop())
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return errors.Join(contextErr, heartbeat.Stop())
	}
	finalizeErr := finalize(ctx)
	return errors.Join(finalizeErr, heartbeat.Stop())
}

func (worker *ControllerRecoveryWorker) processSuspension(
	ctx context.Context,
	lease eventing.PRDevelopmentControllerSuspensionLease,
) error {
	suspension := lease.Suspension
	return worker.processClaim(
		ctx,
		func(renewCtx context.Context, duration time.Duration) error {
			return worker.store.RenewPRDevelopmentControllerSuspension(
				renewCtx,
				eventing.PRDevelopmentControllerSuspensionRenew{
					SuspensionID: suspension.ID,
					ControllerID: suspension.ControllerID,
					AttemptID:    suspension.AttemptID,
					ClaimID:      suspension.SuspendClaimID,
					ClaimToken:   suspension.SuspendClaimToken,
					ClaimEpoch:   suspension.SuspendClaimEpoch,
					Lease:        duration,
				},
			)
		},
		func(
			effectCtx context.Context,
			workspace controllerRecoveryGit,
		) (func(context.Context) error, error) {
			result, effectErr := executeControllerSuspension(
				effectCtx,
				workspace,
				suspension,
			)
			if effectErr != nil {
				return nil, effectErr
			}
			return func(finalizeCtx context.Context) error {
				transition, _, finalizeErr := worker.store.FinalizePRDevelopmentControllerSuspension(
					finalizeCtx,
					eventing.PRDevelopmentControllerSuspensionFinalize{
						SuspensionID:     suspension.ID,
						ControllerID:     suspension.ControllerID,
						AttemptID:        suspension.AttemptID,
						ExpectedRevision: lease.Controller.Revision,
						ClaimID:          suspension.SuspendClaimID,
						ClaimToken:       suspension.SuspendClaimToken,
						ClaimEpoch:       suspension.SuspendClaimEpoch,
						Result:           result,
					},
				)
				if finalizeErr != nil {
					return fmt.Errorf("finalize controller suspension: %w", finalizeErr)
				}
				return validateControllerSuspendedTransition(
					transition,
					suspension,
					result,
				)
			}, nil
		},
	)
}

func (worker *ControllerRecoveryWorker) processSuspendedResumeRecovery(
	ctx context.Context,
	lease eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease,
) error {
	resume := lease.Suspension
	return worker.processClaim(
		ctx,
		func(renewCtx context.Context, duration time.Duration) error {
			return worker.store.RenewPRDevelopmentControllerSuspendedResumeRecovery(
				renewCtx,
				eventing.PRDevelopmentControllerSuspendedResumeRecoveryRenew{
					SuspensionID: resume.ID,
					ControllerID: resume.ControllerID,
					AttemptID:    resume.ResumeAttemptID,
					ClaimID:      resume.ResumeClaimID,
					ClaimToken:   resume.ResumeClaimToken,
					ClaimEpoch:   resume.ResumeClaimEpoch,
					Lease:        duration,
				},
			)
		},
		func(
			effectCtx context.Context,
			workspace controllerRecoveryGit,
		) (func(context.Context) error, error) {
			result, effectErr := resumeSuspendedController(
				effectCtx,
				workspace,
				eventing.PRDevelopmentControllerSuspendedResumeLease{
					Controller: lease.Controller,
					Suspension: resume,
					Reclaimed:  lease.Reclaimed,
				},
			)
			if effectErr != nil {
				return nil, effectErr
			}
			return func(finalizeCtx context.Context) error {
				transition, changed, finalizeErr :=
					worker.store.FinalizePRDevelopmentControllerSuspendedResumeRecovery(
						finalizeCtx,
						eventing.PRDevelopmentControllerSuspendedResumeRecoveryFinalize{
							SuspensionID:     resume.ID,
							ControllerID:     resume.ControllerID,
							AttemptID:        resume.ResumeAttemptID,
							ExpectedRevision: lease.Controller.Revision,
							ClaimID:          resume.ResumeClaimID,
							ClaimToken:       resume.ResumeClaimToken,
							ClaimEpoch:       resume.ResumeClaimEpoch,
							Result:           result,
						},
					)
				if finalizeErr != nil {
					return fmt.Errorf("finalize suspended resume recovery: %w", finalizeErr)
				}
				if err := validateControllerSuspendedResumeRecoveryTransition(
					transition,
					lease,
					result,
				); err != nil {
					return err
				}

				switch transition.NextSuspension.Status {
				case eventing.PRDevelopmentControllerSuspensionStatusSuspendClaimed:
					childLease := eventing.PRDevelopmentControllerSuspensionLease{
						Controller: transition.Controller,
						Suspension: transition.NextSuspension,
						Reclaimed:  true,
					}
					ownsTransferredClaim :=
						controllerSuspendedResumeRecoveryOwnsTransferredChild(
							transition.Resumed,
							lease.Suspension,
							worker.workerLabel,
							transition.NextSuspension,
						)
					if !ownsTransferredClaim {
						if changed {
							return errors.New(
								"new suspended resume recovery lost its transferred child claim",
							)
						}
						return nil
					}
					if err := validateControllerSuspendedResumeRecoveryChildLease(
						transition.Resumed,
						lease.Suspension,
						worker.workerLabel,
						childLease,
					); err != nil {
						return err
					}
					child := childLease.Suspension
					if err := worker.store.RenewPRDevelopmentControllerSuspension(
						finalizeCtx,
						eventing.PRDevelopmentControllerSuspensionRenew{
							SuspensionID: child.ID,
							ControllerID: child.ControllerID,
							AttemptID:    child.AttemptID,
							ClaimID:      child.SuspendClaimID,
							ClaimToken:   child.SuspendClaimToken,
							ClaimEpoch:   child.SuspendClaimEpoch,
							Lease:        worker.lease,
						},
					); err != nil {
						return fmt.Errorf(
							"renew transferred controller suspension claim before Git: %w",
							err,
						)
					}
					return worker.processSuspension(finalizeCtx, childLease)

				case eventing.PRDevelopmentControllerSuspensionStatusSuspended:
					return validateControllerSuspendedResumeRecoveryTerminalChild(
						transition,
						lease.Suspension,
					)

				default:
					return errors.New(
						"suspended resume recovery returned an invalid child suspension state",
					)
				}
			}, nil
		},
	)
}

func executeControllerSuspension(
	ctx context.Context,
	workspace controllerRecoveryGit,
	suspension eventing.PRDevelopmentControllerSuspension,
) (eventing.PRDevelopmentControllerSuspensionResult, error) {
	request := suspension.SuspendRequest
	pin := gitworkspace.PinnedAcquireRequest{
		Repository:     request.Repository,
		SourceRef:      request.SourceRef,
		ExpectedCommit: request.SourceCommit,
		ReservationKey: request.ReservationKey,
		AgentID:        request.AgentID,
	}
	suspendRequest := gitworkspace.PinnedLineSuspendRequest{
		Pin:                   pin,
		WorkspaceID:           request.WorkspaceID,
		LineID:                request.LineID,
		IntentID:              request.IntentID,
		ExpectedVersion:       request.ExpectedVersion,
		ExpectedMutationEpoch: request.ExpectedMutationEpoch,
		ExpectedTip:           request.ExpectedTip,
		ExpectedTree:          request.ExpectedTree,
	}
	var (
		suspended gitworkspace.PinnedLineSuspendResult
		err       error
	)
	switch suspension.Mode {
	case eventing.PRDevelopmentControllerSuspensionCandidate:
		suspended, err = workspace.SuspendPinnedLine(ctx, suspendRequest)
	case eventing.PRDevelopmentControllerSuspensionCommitRecovery:
		suspended, err = workspace.SuspendPinnedLineCommitRecovery(
			ctx,
			gitworkspace.PinnedLineCommitSuspensionRequest{
				Suspend: suspendRequest,
				Commit: gitworkspace.PinnedCommitRequest{
					Pin:                     pin,
					WorkspaceID:             request.WorkspaceID,
					IntentID:                request.CommitIntentID,
					ExpectedParent:          request.CommitExpectedParent,
					ExpectedTree:            request.CommitExpectedTree,
					ExpectedCandidateDigest: request.CommitCandidateDigest,
					Message:                 request.CommitMessage,
					AuthoredAt:              request.CommitAuthoredAt,
				},
			},
		)
	default:
		return eventing.PRDevelopmentControllerSuspensionResult{}, fmt.Errorf(
			"%w: unknown controller suspension mode",
			ErrUnavailable,
		)
	}
	if err != nil {
		return eventing.PRDevelopmentControllerSuspensionResult{}, fmt.Errorf(
			"suspend recovered controller line: %w",
			err,
		)
	}
	return mapControllerSuspensionResult(suspension, suspended)
}

func mapControllerSuspensionResult(
	suspension eventing.PRDevelopmentControllerSuspension,
	result gitworkspace.PinnedLineSuspendResult,
) (eventing.PRDevelopmentControllerSuspensionResult, error) {
	request := suspension.SuspendRequest
	if result.WorkspaceID != request.WorkspaceID ||
		result.Version != request.ExpectedVersion ||
		result.MutationEpoch != request.ExpectedMutationEpoch ||
		result.Tip != request.ExpectedTip || result.Tree != request.ExpectedTree ||
		!sameControllerObjectIDWidth(request.SourceCommit, result.CandidateTree) ||
		!validControllerSHA256(result.CandidateDigest) ||
		result.ChangedFileCount < 0 ||
		result.ChangedFileCount > controllerSuspensionChangedFilesMax ||
		!validControllerSHA256(result.SuspensionHash) {
		return eventing.PRDevelopmentControllerSuspensionResult{}, errors.New(
			"controller suspension returned changed line evidence",
		)
	}
	switch suspension.Mode {
	case eventing.PRDevelopmentControllerSuspensionCandidate:
		if result.PreparedCommit != "" || result.PreparedTree != "" ||
			result.PreparedCommitApplied {
			return eventing.PRDevelopmentControllerSuspensionResult{}, errors.New(
				"candidate suspension returned prepared Commit evidence",
			)
		}
	case eventing.PRDevelopmentControllerSuspensionCommitRecovery:
		if !sameControllerObjectIDWidth(
			request.SourceCommit,
			result.PreparedCommit,
			result.PreparedTree,
		) || result.PreparedCommit == request.CommitExpectedParent ||
			result.PreparedTree != request.CommitExpectedTree ||
			(!result.PreparedCommitApplied &&
				(result.CandidateTree != request.CommitExpectedTree ||
					result.CandidateDigest != request.CommitCandidateDigest)) {
			return eventing.PRDevelopmentControllerSuspensionResult{}, errors.New(
				"Commit recovery suspension returned changed prepared evidence",
			)
		}
	default:
		return eventing.PRDevelopmentControllerSuspensionResult{}, fmt.Errorf(
			"%w: unknown controller suspension mode",
			ErrUnavailable,
		)
	}
	return eventing.PRDevelopmentControllerSuspensionResult{
		WorkspaceID:           result.WorkspaceID,
		Version:               result.Version,
		MutationEpoch:         result.MutationEpoch,
		Tip:                   result.Tip,
		Tree:                  result.Tree,
		CandidateTree:         result.CandidateTree,
		CandidateDigest:       result.CandidateDigest,
		ChangedFileCount:      result.ChangedFileCount,
		SuspensionHash:        result.SuspensionHash,
		PreparedCommit:        result.PreparedCommit,
		PreparedTree:          result.PreparedTree,
		PreparedCommitApplied: result.PreparedCommitApplied,
		AlreadySuspended:      result.AlreadySuspended,
	}, nil
}

func (worker *ControllerRecoveryWorker) processReservationRecovery(
	ctx context.Context,
	lease eventing.PRDevelopmentControllerRecoveryLease,
) (controllerRecoverySuspensionHandoff, error) {
	intent := lease.Intent
	handoff := controllerRecoverySuspensionHandoff{}
	err := worker.processClaim(
		ctx,
		func(renewCtx context.Context, duration time.Duration) error {
			return worker.store.RenewPRDevelopmentControllerRecovery(
				renewCtx,
				eventing.PRDevelopmentControllerRecoveryRenew{
					ControllerID: lease.Controller.ID,
					AttemptID:    intent.AttemptID,
					RecoveryID:   intent.ID,
					ClaimID:      intent.ClaimID,
					ClaimToken:   intent.ClaimToken,
					ClaimEpoch:   intent.ClaimEpoch,
					Lease:        duration,
				},
			)
		},
		func(
			effectCtx context.Context,
			workspace controllerRecoveryGit,
		) (func(context.Context) error, error) {
			rotated, rotateErr := workspace.RotatePinnedReservation(
				effectCtx,
				gitworkspace.PinnedReservationRotationRequest{
					Pin: pinnedControllerRecoveryPin(
						intent.SourceCloneURL,
						intent.SourceRef,
						intent.SourceCommit,
						intent.PreviousReservationKey,
						intent.AgentID,
					),
					WorkspaceID:               intent.WorkspaceID,
					IntentID:                  intent.ID,
					ReplacementReservationKey: intent.ReplacementReservationKey,
					RequireSuspensionCapacity: true,
					LineID:                    intent.LineID,
					ExpectedVersion:           intent.LineVersion,
					ExpectedMutationEpoch:     intent.MutationEpoch,
					ExpectedTip:               intent.TipCommit,
					ExpectedTree:              intent.Tree,
				},
			)
			if rotateErr != nil {
				return nil, fmt.Errorf("rotate bound controller recovery reservation: %w", rotateErr)
			}
			rotation, mapErr := controllerRecoveryRotation(
				rotated,
				intent.WorkspaceID,
				intent.LineVersion,
				intent.MutationEpoch,
				intent.TipCommit,
				intent.Tree,
			)
			if mapErr != nil {
				return nil, mapErr
			}
			return func(finalizeCtx context.Context) error {
				controller, _, finalizeErr := worker.store.FinalizePRDevelopmentControllerRecovery(
					finalizeCtx,
					eventing.PRDevelopmentControllerRecoveryFinalize{
						ControllerID:     lease.Controller.ID,
						AttemptID:        intent.AttemptID,
						RecoveryID:       intent.ID,
						ExpectedRevision: intent.RecoveryRevision,
						ClaimID:          intent.ClaimID,
						ClaimToken:       intent.ClaimToken,
						ClaimEpoch:       intent.ClaimEpoch,
						Rotation:         rotation,
						Lease:            worker.lease,
					},
				)
				if finalizeErr != nil {
					return fmt.Errorf("finalize bound controller recovery: %w", finalizeErr)
				}
				if validateErr := validateRecoveredSuspensionController(
					controller,
					lease.Controller.ID,
					intent.AttemptID,
					intent.LineID,
				); validateErr != nil {
					return validateErr
				}
				if controller.Phase == eventing.PRDevelopmentControllerSuspensionPending {
					handoff = controllerRecoverySuspensionHandoff{
						required:         true,
						controllerID:     controller.ID,
						attemptID:        intent.AttemptID,
						sourceKind:       eventing.PRDevelopmentControllerSuspensionSourceControllerRecovery,
						expectedRevision: controller.Revision,
					}
				}
				return nil
			}, nil
		},
	)
	return handoff, err
}

func (worker *ControllerRecoveryWorker) processOperationRecovery(
	ctx context.Context,
	lease eventing.PRDevelopmentControllerOperationRecoveryLease,
) (controllerRecoverySuspensionHandoff, error) {
	operation := lease.Operation
	handoff := controllerRecoverySuspensionHandoff{}
	err := worker.processClaim(
		ctx,
		func(renewCtx context.Context, duration time.Duration) error {
			return worker.store.RenewPRDevelopmentControllerOperationRecovery(
				renewCtx,
				eventing.PRDevelopmentControllerOperationRecoveryRenew{
					ControllerID: lease.Controller.ID,
					AttemptID:    operation.AttemptID,
					OperationID:  operation.ID,
					RecoveryID:   operation.RecoveryID,
					ClaimID:      operation.ClaimID,
					ClaimToken:   operation.ClaimToken,
					ClaimEpoch:   operation.ClaimEpoch,
					Lease:        duration,
				},
			)
		},
		func(
			effectCtx context.Context,
			workspace controllerRecoveryGit,
		) (func(context.Context) error, error) {
			var (
				rotation eventing.PRDevelopmentControllerRecoveryRotationResult
				result   eventing.PRDevelopmentControllerOperationResult
				err      error
			)
			switch operation.Kind {
			case eventing.PRDevelopmentControllerOperationAdopt:
				rotation, result, err = recoverControllerAdopt(
					effectCtx,
					workspace,
					lease.Controller.MutationReservationKey,
					operation,
				)
			case eventing.PRDevelopmentControllerOperationResume:
				rotation, result, err = recoverControllerResume(
					effectCtx,
					workspace,
					lease.Controller.MutationReservationKey,
					operation,
				)
			case eventing.PRDevelopmentControllerOperationCommit:
				rotation, result, err = recoverControllerCommit(
					effectCtx,
					workspace,
					lease.Controller.MutationReservationKey,
					operation,
				)
			case eventing.PRDevelopmentControllerOperationPark:
				result, err = recoverControllerPark(
					effectCtx,
					workspace,
					lease.Controller.MutationReservationKey,
					operation,
				)
			default:
				err = fmt.Errorf("%w: unknown recovered controller operation", ErrUnavailable)
			}
			if err != nil {
				return nil, err
			}

			return func(finalizeCtx context.Context) error {
				transition, _, finalizeErr := worker.store.FinalizePRDevelopmentControllerOperationRecovery(
					finalizeCtx,
					eventing.PRDevelopmentControllerOperationRecoveryFinalize{
						ControllerID:     lease.Controller.ID,
						AttemptID:        operation.AttemptID,
						OperationID:      operation.ID,
						RecoveryID:       operation.RecoveryID,
						ExpectedRevision: operation.RecoveryRevision,
						ClaimID:          operation.ClaimID,
						ClaimToken:       operation.ClaimToken,
						ClaimEpoch:       operation.ClaimEpoch,
						Rotation:         rotation,
						Result:           result,
						Lease: func() time.Duration {
							if operation.Kind == eventing.PRDevelopmentControllerOperationPark {
								return 0
							}
							return worker.lease
						}(),
					},
				)
				if finalizeErr != nil {
					return fmt.Errorf("finalize controller %s recovery: %w", operation.Kind, finalizeErr)
				}
				if operation.Kind == eventing.PRDevelopmentControllerOperationPark {
					return validateRecoveredParkTransition(transition, operation)
				}
				if transition.Fence != nil || transition.Operation.ID != operation.ID ||
					transition.Operation.Status != eventing.PRDevelopmentControllerOperationFinalized {
					return errors.New("controller operation recovery returned a changed transition")
				}
				if validateErr := validateRecoveredSuspensionController(
					transition.Controller,
					lease.Controller.ID,
					operation.AttemptID,
					operation.LineID,
				); validateErr != nil {
					return validateErr
				}
				if transition.Controller.Phase ==
					eventing.PRDevelopmentControllerSuspensionPending {
					handoff = controllerRecoverySuspensionHandoff{
						required:         true,
						controllerID:     transition.Controller.ID,
						attemptID:        operation.AttemptID,
						sourceKind:       eventing.PRDevelopmentControllerSuspensionSourceOperationRecovery,
						expectedRevision: transition.Controller.Revision,
					}
				}
				return nil
			}, nil
		},
	)
	return handoff, err
}

func recoverControllerAdopt(
	ctx context.Context,
	workspace controllerRecoveryGit,
	oldReservation string,
	operation eventing.PRDevelopmentControllerOperation,
) (
	eventing.PRDevelopmentControllerRecoveryRotationResult,
	eventing.PRDevelopmentControllerOperationResult,
	error,
) {
	recovered, err := workspace.RecoverPinnedLineAdoptReservation(
		ctx,
		gitworkspace.PinnedLineAdoptRecoveryRequest{
			Adopt: gitworkspace.PinnedLineAdoptRequest{
				Pin:          operationRecoveryPin(operation, oldReservation),
				WorkspaceID:  operation.Request.WorkspaceID,
				LineID:       operation.Request.LineID,
				ExpectedTree: operation.Request.ExpectedTree,
			},
			IntentID:                  operation.RecoveryID,
			ReplacementReservationKey: operation.ReplacementReservationKey,
			RequireSuspensionCapacity: true,
		},
	)
	if err != nil {
		return eventing.PRDevelopmentControllerRecoveryRotationResult{},
			eventing.PRDevelopmentControllerOperationResult{},
			fmt.Errorf("recover controller Adopt reservation: %w", err)
	}
	rotation, err := controllerLineRecoveryRotation(
		recovered,
		operation.WorkspaceID,
		0,
		1,
		operation.SourceCommit,
		operation.SourceTree,
	)
	if err != nil {
		return eventing.PRDevelopmentControllerRecoveryRotationResult{},
			eventing.PRDevelopmentControllerOperationResult{}, err
	}
	return rotation, eventing.PRDevelopmentControllerOperationResult{
		WorkspaceID:   recovered.WorkspaceID,
		Version:       recovered.Version,
		MutationEpoch: recovered.MutationEpoch,
		Tip:           recovered.Tip,
		Tree:          recovered.Tree,
	}, nil
}

func recoverControllerResume(
	ctx context.Context,
	workspace controllerRecoveryGit,
	oldReservation string,
	operation eventing.PRDevelopmentControllerOperation,
) (
	eventing.PRDevelopmentControllerRecoveryRotationResult,
	eventing.PRDevelopmentControllerOperationResult,
	error,
) {
	recovered, err := workspace.RecoverPinnedLineResumeReservation(
		ctx,
		gitworkspace.PinnedLineResumeRecoveryRequest{
			Resume: gitworkspace.PinnedLineResumeRequest{
				Pin:             operationRecoveryPin(operation, oldReservation),
				WorkspaceID:     operation.Request.WorkspaceID,
				LineID:          operation.Request.LineID,
				ExpectedVersion: operation.Request.ExpectedVersion,
				ExpectedEpoch:   operation.Request.ExpectedEpoch,
				ExpectedTip:     operation.Request.ExpectedTip,
				ExpectedTree:    operation.Request.ExpectedTree,
			},
			IntentID:                  operation.RecoveryID,
			ReplacementReservationKey: operation.ReplacementReservationKey,
			RequireSuspensionCapacity: true,
		},
	)
	if err != nil {
		return eventing.PRDevelopmentControllerRecoveryRotationResult{},
			eventing.PRDevelopmentControllerOperationResult{},
			fmt.Errorf("recover controller Resume reservation: %w", err)
	}
	rotation, err := controllerLineRecoveryRotation(
		recovered,
		operation.WorkspaceID,
		operation.LineVersion,
		operation.MutationEpoch+1,
		operation.TipCommit,
		operation.Tree,
	)
	if err != nil {
		return eventing.PRDevelopmentControllerRecoveryRotationResult{},
			eventing.PRDevelopmentControllerOperationResult{}, err
	}
	return rotation, eventing.PRDevelopmentControllerOperationResult{
		WorkspaceID:   recovered.WorkspaceID,
		Version:       recovered.Version,
		MutationEpoch: recovered.MutationEpoch,
		Tip:           recovered.Tip,
		Tree:          recovered.Tree,
	}, nil
}

func recoverControllerCommit(
	ctx context.Context,
	workspace controllerRecoveryGit,
	oldReservation string,
	operation eventing.PRDevelopmentControllerOperation,
) (
	eventing.PRDevelopmentControllerRecoveryRotationResult,
	eventing.PRDevelopmentControllerOperationResult,
	error,
) {
	rotated, err := workspace.RotatePinnedReservation(
		ctx,
		gitworkspace.PinnedReservationRotationRequest{
			Pin:                       operationRecoveryPin(operation, oldReservation),
			WorkspaceID:               operation.WorkspaceID,
			IntentID:                  operation.RecoveryID,
			ReplacementReservationKey: operation.ReplacementReservationKey,
			RequireSuspensionCapacity: true,
			LineID:                    operation.LineID,
			ExpectedVersion:           operation.LineVersion,
			ExpectedMutationEpoch:     operation.MutationEpoch,
			ExpectedTip:               operation.TipCommit,
			ExpectedTree:              operation.Tree,
		},
	)
	if err != nil {
		return eventing.PRDevelopmentControllerRecoveryRotationResult{},
			eventing.PRDevelopmentControllerOperationResult{},
			fmt.Errorf("rotate controller Commit recovery reservation: %w", err)
	}
	rotation, err := controllerRecoveryRotation(
		rotated,
		operation.WorkspaceID,
		operation.LineVersion,
		operation.MutationEpoch,
		operation.TipCommit,
		operation.Tree,
	)
	if err != nil {
		return eventing.PRDevelopmentControllerRecoveryRotationResult{},
			eventing.PRDevelopmentControllerOperationResult{}, err
	}
	request := operation.Request
	committed, commitErr := workspace.CommitPinned(
		ctx,
		gitworkspace.PinnedCommitRequest{
			Pin: operationRecoveryPin(
				operation,
				operation.ReplacementReservationKey,
			),
			WorkspaceID:             request.WorkspaceID,
			IntentID:                request.EffectIntentID,
			ExpectedParent:          request.ExpectedParent,
			ExpectedTree:            request.ExpectedTree,
			ExpectedCandidateDigest: request.CandidateDigest,
			Message:                 request.CommitMessage,
			AuthoredAt:              request.AuthoredAt,
		},
	)
	if commitErr != nil && !errors.Is(commitErr, gitworkspace.ErrPinnedCommitWorkspaceDrift) {
		return eventing.PRDevelopmentControllerRecoveryRotationResult{},
			eventing.PRDevelopmentControllerOperationResult{},
			fmt.Errorf("recover controller Commit effect: %w", commitErr)
	}
	if err = validateRecoveredControllerCommit(operation, committed, commitErr); err != nil {
		return eventing.PRDevelopmentControllerRecoveryRotationResult{},
			eventing.PRDevelopmentControllerOperationResult{}, err
	}
	return rotation, eventing.PRDevelopmentControllerOperationResult{
		WorkspaceID:     committed.WorkspaceID,
		WorkspaceClean:  committed.WorkspaceClean,
		AlreadyApplied:  committed.AlreadyApplied,
		IntentID:        committed.IntentID,
		ParentCommit:    committed.ParentCommit,
		Tree:            committed.Tree,
		CandidateDigest: committed.CandidateDigest,
		Commit:          committed.Commit,
		ChangedFiles:    committed.ChangedFiles,
	}, nil
}

func recoverControllerPark(
	ctx context.Context,
	workspace controllerRecoveryGit,
	oldReservation string,
	operation eventing.PRDevelopmentControllerOperation,
) (eventing.PRDevelopmentControllerOperationResult, error) {
	request := operation.Request
	parked, err := workspace.ParkPinnedLine(
		ctx,
		gitworkspace.PinnedLineParkRequest{
			Pin:             operationRecoveryPin(operation, oldReservation),
			WorkspaceID:     request.WorkspaceID,
			LineID:          request.LineID,
			IntentID:        request.EffectIntentID,
			ExpectedVersion: request.ExpectedVersion,
			MutationEpoch:   request.MutationEpoch,
			PreviousTip:     request.PreviousTip,
			Tip:             request.Tip,
			Tree:            request.Tree,
			NoChanges:       request.NoChanges,
		},
	)
	if err != nil {
		return eventing.PRDevelopmentControllerOperationResult{},
			fmt.Errorf("recover controller Park effect: %w", err)
	}
	if err = validateRecoveredControllerPark(operation, parked); err != nil {
		return eventing.PRDevelopmentControllerOperationResult{}, err
	}
	snapshot, err := workspace.SnapshotPinnedLineReview(
		ctx,
		gitworkspace.PinnedLineReviewRequest{
			LineID:          request.LineID,
			ExpectedVersion: parked.Version,
			ExpectedBase:    parked.PreviousTip,
			ExpectedTip:     parked.Tip,
			ExpectedTree:    parked.Tree,
		},
	)
	if err != nil {
		return eventing.PRDevelopmentControllerOperationResult{},
			fmt.Errorf("snapshot recovered Park review: %w", err)
	}
	if err = validateRecoveredControllerParkSnapshot(operation, parked, snapshot); err != nil {
		return eventing.PRDevelopmentControllerOperationResult{}, err
	}
	return eventing.PRDevelopmentControllerOperationResult{
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
	}, nil
}

func pinnedControllerRecoveryPin(
	repository, sourceRef, sourceCommit, reservation, agentID string,
) gitworkspace.PinnedAcquireRequest {
	return gitworkspace.PinnedAcquireRequest{
		Repository:     repository,
		SourceRef:      sourceRef,
		ExpectedCommit: sourceCommit,
		ReservationKey: reservation,
		AgentID:        agentID,
	}
}

func operationRecoveryPin(
	operation eventing.PRDevelopmentControllerOperation,
	reservation string,
) gitworkspace.PinnedAcquireRequest {
	return pinnedControllerRecoveryPin(
		operation.SourceCloneURL,
		operation.Request.SourceRef,
		operation.Request.SourceCommit,
		reservation,
		operation.Request.AgentID,
	)
}

func controllerRecoveryRotation(
	result gitworkspace.PinnedReservationRotationResult,
	workspaceID string,
	version, mutationEpoch int64,
	tip, tree string,
) (eventing.PRDevelopmentControllerRecoveryRotationResult, error) {
	if result.WorkspaceID != workspaceID || !result.Bound ||
		result.Version != version || result.MutationEpoch != mutationEpoch ||
		result.Tip != tip || result.Tree != tree ||
		!validControllerSHA256(result.RotationHash) {
		return eventing.PRDevelopmentControllerRecoveryRotationResult{}, errors.New(
			"controller recovery reservation rotation returned changed evidence",
		)
	}
	return eventing.PRDevelopmentControllerRecoveryRotationResult{
		WorkspaceID:    result.WorkspaceID,
		Bound:          result.Bound,
		Version:        result.Version,
		MutationEpoch:  result.MutationEpoch,
		Tip:            result.Tip,
		Tree:           result.Tree,
		RotationHash:   result.RotationHash,
		AlreadyRotated: result.AlreadyRotated,
	}, nil
}

func controllerLineRecoveryRotation(
	result gitworkspace.PinnedLineReservationRecoveryResult,
	workspaceID string,
	version, mutationEpoch int64,
	tip, tree string,
) (eventing.PRDevelopmentControllerRecoveryRotationResult, error) {
	if result.WorkspaceID != workspaceID || result.Version != version ||
		result.MutationEpoch != mutationEpoch || result.Tip != tip ||
		result.Tree != tree || !validControllerSHA256(result.RotationHash) {
		return eventing.PRDevelopmentControllerRecoveryRotationResult{}, errors.New(
			"controller line recovery returned changed evidence",
		)
	}
	return eventing.PRDevelopmentControllerRecoveryRotationResult{
		WorkspaceID:    result.WorkspaceID,
		Bound:          true,
		Version:        result.Version,
		MutationEpoch:  result.MutationEpoch,
		Tip:            result.Tip,
		Tree:           result.Tree,
		RotationHash:   result.RotationHash,
		AlreadyRotated: result.AlreadyRotated,
	}, nil
}

func validateRecoveredControllerCommit(
	operation eventing.PRDevelopmentControllerOperation,
	result gitworkspace.PinnedCommitResult,
	commitErr error,
) error {
	request := operation.Request
	if result.WorkspaceID != request.WorkspaceID || result.IntentID != request.EffectIntentID ||
		result.ParentCommit != request.ExpectedParent || result.Tree != request.ExpectedTree ||
		result.CandidateDigest != request.CandidateDigest ||
		!validControllerSHA256(result.CandidateDigest) ||
		result.ChangedFiles < 1 ||
		!sameControllerObjectIDWidth(result.ParentCommit, result.Tree, result.Commit) ||
		result.Commit == result.ParentCommit {
		return errors.New("controller Commit recovery returned changed evidence")
	}
	if commitErr == nil && !result.WorkspaceClean {
		return errors.New("controller Commit recovery reported unproven workspace drift")
	}
	if errors.Is(commitErr, gitworkspace.ErrPinnedCommitWorkspaceDrift) && result.WorkspaceClean {
		return errors.New("controller Commit recovery drift result claimed a clean workspace")
	}
	return nil
}

func sameControllerObjectIDWidth(values ...string) bool {
	if len(values) == 0 || !validObjectID(values[0]) {
		return false
	}
	width := len(values[0])
	for _, value := range values {
		if len(value) != width || !validObjectID(value) {
			return false
		}
	}
	return true
}

func validateRecoveredControllerPark(
	operation eventing.PRDevelopmentControllerOperation,
	result gitworkspace.PinnedLineParkResult,
) error {
	request := operation.Request
	if result.WorkspaceID != request.WorkspaceID ||
		result.Version != request.ExpectedVersion+1 ||
		result.MutationEpoch != request.MutationEpoch ||
		result.PreviousTip != request.PreviousTip || result.Tip != request.Tip ||
		result.Tree != request.Tree || result.NoChanges != request.NoChanges ||
		!result.WorkspaceClean {
		return errors.New("controller Park recovery returned changed evidence")
	}
	return nil
}

func validateRecoveredControllerParkSnapshot(
	operation eventing.PRDevelopmentControllerOperation,
	parked gitworkspace.PinnedLineParkResult,
	snapshot gitworkspace.PinnedLineReviewSnapshot,
) error {
	request := operation.Request
	if snapshot.Version != parked.Version ||
		snapshot.MutationEpoch != parked.MutationEpoch ||
		snapshot.ParkIntentID != request.EffectIntentID ||
		snapshot.BaseCommit != parked.PreviousTip || snapshot.Commit != parked.Tip ||
		snapshot.Tree != parked.Tree || !validControllerSHA256(snapshot.ReviewDigest) {
		return errors.New("recovered controller Park snapshot returned changed evidence")
	}
	return nil
}

func validateControllerSuspendedResumeRecoveryCandidate(
	candidate eventing.PRDevelopmentControllerSuspendedResumeRecoveryCandidate,
) error {
	if candidate.CaseID == "" || candidate.CaseID != strings.TrimSpace(candidate.CaseID) ||
		candidate.SuspensionID == "" ||
		candidate.SuspensionID != strings.TrimSpace(candidate.SuspensionID) ||
		candidate.ControllerID == "" ||
		candidate.ControllerID != strings.TrimSpace(candidate.ControllerID) ||
		candidate.AttemptID == "" ||
		candidate.AttemptID != strings.TrimSpace(candidate.AttemptID) ||
		candidate.ExpectedRevision < 1 {
		return fmt.Errorf(
			"%w: suspended resume recovery candidate is incomplete",
			ErrUnavailable,
		)
	}
	return nil
}

func validateControllerSuspendedResumeRecoveryLease(
	candidate eventing.PRDevelopmentControllerSuspendedResumeRecoveryCandidate,
	claimID, workerLabel string,
	lease eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease,
) error {
	controller, suspension := lease.Controller, lease.Suspension
	request := suspension.ResumeRequest
	if !lease.Reclaimed || controller.ID != candidate.ControllerID ||
		controller.CurrentAttemptID != candidate.AttemptID ||
		controller.Revision != candidate.ExpectedRevision ||
		controller.Phase != eventing.PRDevelopmentControllerSuspended ||
		controller.LeaseKind != "" || controller.LeaseOwner != "" ||
		controller.LeaseToken != "" || controller.LeaseUntil != nil ||
		controller.MutationReservationKey != "" ||
		suspension.ID != candidate.SuspensionID ||
		suspension.ControllerID != candidate.ControllerID ||
		suspension.ResumeAttemptID != candidate.AttemptID ||
		suspension.FinalSuspensionRevision+1 != candidate.ExpectedRevision ||
		suspension.Status != eventing.PRDevelopmentControllerSuspensionStatusResumeClaimed ||
		suspension.ResumeClaimID != claimID ||
		suspension.ResumeClaimOwner != workerLabel ||
		suspension.ResumeClaimToken == "" || suspension.ResumeClaimUntil == nil ||
		suspension.ResumeClaimEpoch < 1 ||
		suspension.ResumeClaims != int(suspension.ResumeClaimEpoch) ||
		suspension.ResumeClaimedAt == nil || suspension.ResumeClaimTokenDigest != "" ||
		suspension.ResumeReservationKey == "" ||
		suspension.ResumeReservationDigest != controllerRecoveryReservationDigest(
			suspension.ResumeReservationKey,
		) ||
		suspension.ResumeIntentID == "" || request.IntentID != suspension.ResumeIntentID ||
		len(suspension.ResumeRequestJSON) == 0 ||
		!validControllerSHA256(suspension.ResumeRequestHash) ||
		!validControllerSHA256(suspension.ResumeIntentHash) ||
		suspension.ResumePreparedAt == nil {
		return errors.New("suspended resume recovery claim returned changed authority")
	}
	if controller.ThreadID != suspension.ThreadID ||
		controller.OwnerSessionID != suspension.OwnerSessionID ||
		controller.AgentID != suspension.AgentID ||
		controller.WorkspaceID != suspension.WorkspaceID ||
		controller.LineID != suspension.LineID ||
		controller.SourceCloneURL != suspension.SourceCloneURL ||
		controller.SourceRef != suspension.SourceRef ||
		controller.SourceCommit != suspension.SourceCommit ||
		controller.SourceTree != suspension.SourceTree ||
		controller.LineVersion != suspension.LineVersion ||
		controller.MutationEpoch != suspension.MutationEpoch ||
		controller.TipCommit != suspension.TipCommit || controller.Tree != suspension.Tree ||
		!controller.UpdatedAt.Equal(*suspension.ResumePreparedAt) {
		return errors.New("suspended resume recovery changed its retained line fence")
	}
	if suspension.SuspensionReservationKey != "" ||
		suspension.SuspendRequest.ReservationKey != "" ||
		suspension.SuspendClaimToken != "" || suspension.SuspendClaimUntil != nil ||
		!validControllerSHA256(suspension.SuspendClaimTokenDigest) ||
		len(suspension.SuspendResultJSON) == 0 ||
		!validControllerSHA256(suspension.SuspendResultHash) ||
		!validControllerSHA256(suspension.SuspensionFinalHash) ||
		suspension.SuspendedAt == nil ||
		suspension.ResumeResult != (eventing.PRDevelopmentControllerSuspendedResumeResult{}) ||
		len(suspension.ResumeResultJSON) != 0 || suspension.ResumeResultHash != "" ||
		suspension.NewMutationLeaseEpoch != 0 ||
		suspension.NewMutationLeaseTokenDigest != "" ||
		suspension.NewMutationLeaseUntil != nil || suspension.FinalResumeRevision != 0 ||
		suspension.ResumeFinalHash != "" || suspension.ResumedAt != nil {
		return errors.New("suspended resume recovery claim contains changed terminal evidence")
	}
	if request.Repository != suspension.SourceCloneURL ||
		request.SourceRef != suspension.SourceRef ||
		request.SourceCommit != suspension.SourceCommit ||
		request.ReservationKey != suspension.ResumeReservationKey ||
		request.AgentID != suspension.AgentID || request.WorkspaceID != suspension.WorkspaceID ||
		request.LineID != suspension.LineID ||
		request.ExpectedVersion != suspension.LineVersion ||
		request.ExpectedMutationEpoch != suspension.MutationEpoch ||
		request.ExpectedTip != suspension.TipCommit || request.ExpectedTree != suspension.Tree ||
		request.SuspensionHash != suspension.SuspendResult.SuspensionHash ||
		request.CandidateTree != suspension.SuspendResult.CandidateTree ||
		request.CandidateDigest != suspension.SuspendResult.CandidateDigest ||
		request.ChangedFileCount != suspension.SuspendResult.ChangedFileCount ||
		strings.Contains(string(suspension.ResumeRequestJSON), suspension.ResumeReservationKey) {
		return errors.New("suspended resume recovery request changed its durable line fence")
	}
	return nil
}

func validateControllerSuspendedResumeRecoveryTransition(
	transition eventing.PRDevelopmentControllerSuspendedResumeRecoveryTransition,
	lease eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease,
	result eventing.PRDevelopmentControllerSuspendedResumeResult,
) error {
	claimed, resumed := lease.Suspension, transition.Resumed
	expectedResult := result
	expectedResult.AlreadyResumed = false
	expectedRequest := claimed.ResumeRequest
	expectedRequest.ReservationKey = ""
	if resumed.ID != claimed.ID || resumed.ControllerID != claimed.ControllerID ||
		resumed.ThreadID != claimed.ThreadID ||
		resumed.OwnerSessionID != claimed.OwnerSessionID ||
		resumed.AttemptID != claimed.AttemptID || resumed.Ordinal != claimed.Ordinal ||
		resumed.SourceKind != claimed.SourceKind ||
		resumed.SourceRecoveryID != claimed.SourceRecoveryID ||
		resumed.SourceOperationID != claimed.SourceOperationID ||
		resumed.SourceOperationKind != claimed.SourceOperationKind ||
		resumed.SourceFinalRevision != claimed.SourceFinalRevision ||
		resumed.SourceFinalHash != claimed.SourceFinalHash || resumed.Mode != claimed.Mode ||
		resumed.AgentID != claimed.AgentID || resumed.WorkspaceID != claimed.WorkspaceID ||
		resumed.LineID != claimed.LineID ||
		resumed.SourceCloneURL != claimed.SourceCloneURL ||
		resumed.SourceRef != claimed.SourceRef ||
		resumed.SourceCommit != claimed.SourceCommit || resumed.SourceTree != claimed.SourceTree ||
		resumed.LineVersion != claimed.LineVersion ||
		resumed.MutationEpoch != claimed.MutationEpoch ||
		resumed.TipCommit != claimed.TipCommit || resumed.Tree != claimed.Tree ||
		resumed.SuspensionReservationDigest != claimed.SuspensionReservationDigest ||
		resumed.MutationLeaseEpoch != claimed.MutationLeaseEpoch ||
		resumed.MutationLeaseTokenDigest != claimed.MutationLeaseTokenDigest ||
		resumed.SuspendIntentID != claimed.SuspendIntentID ||
		resumed.SuspendRequest != claimed.SuspendRequest ||
		string(resumed.SuspendRequestJSON) != string(claimed.SuspendRequestJSON) ||
		resumed.SuspendRequestHash != claimed.SuspendRequestHash ||
		resumed.PreviousHash != claimed.PreviousHash || resumed.IntentHash != claimed.IntentHash ||
		resumed.SuspendClaimID != claimed.SuspendClaimID ||
		resumed.SuspendClaimOwner != claimed.SuspendClaimOwner ||
		resumed.SuspendClaimEpoch != claimed.SuspendClaimEpoch ||
		resumed.SuspendClaims != claimed.SuspendClaims ||
		!sameControllerOptionalTime(resumed.SuspendClaimedAt, claimed.SuspendClaimedAt) ||
		resumed.SuspendResult != claimed.SuspendResult ||
		string(resumed.SuspendResultJSON) != string(claimed.SuspendResultJSON) ||
		resumed.SuspendResultHash != claimed.SuspendResultHash ||
		resumed.FinalSuspensionRevision != claimed.FinalSuspensionRevision ||
		resumed.SuspensionFinalHash != claimed.SuspensionFinalHash ||
		!sameControllerOptionalTime(resumed.SuspendedAt, claimed.SuspendedAt) ||
		!resumed.CreatedAt.Equal(claimed.CreatedAt) {
		return errors.New("suspended resume recovery changed its retained suspension")
	}
	if resumed.Status != eventing.PRDevelopmentControllerSuspensionStatusResumed ||
		resumed.ResumeAttemptID != claimed.ResumeAttemptID ||
		resumed.ResumeIntentID != claimed.ResumeIntentID ||
		resumed.ResumeReservationKey != "" ||
		resumed.ResumeReservationDigest != claimed.ResumeReservationDigest ||
		resumed.ResumeRequest != expectedRequest ||
		string(resumed.ResumeRequestJSON) != string(claimed.ResumeRequestJSON) ||
		resumed.ResumeRequestHash != claimed.ResumeRequestHash ||
		resumed.ResumeIntentHash != claimed.ResumeIntentHash ||
		!sameControllerOptionalTime(resumed.ResumePreparedAt, claimed.ResumePreparedAt) ||
		resumed.ResumeClaimID != claimed.ResumeClaimID ||
		resumed.ResumeClaimOwner != claimed.ResumeClaimOwner ||
		resumed.ResumeClaimToken != "" || resumed.ResumeClaimUntil != nil ||
		resumed.ResumeClaimEpoch != claimed.ResumeClaimEpoch ||
		resumed.ResumeClaims != claimed.ResumeClaims ||
		!sameControllerOptionalTime(resumed.ResumeClaimedAt, claimed.ResumeClaimedAt) ||
		resumed.ResumeClaimTokenDigest != controllerRecoverySuspensionTokenDigest(
			"picoclaw-pr-development-controller-suspension-resume-claim-token-v1\x00",
			claimed.ResumeClaimToken,
		) ||
		resumed.ResumeResult != expectedResult || len(resumed.ResumeResultJSON) == 0 ||
		!validControllerSHA256(resumed.ResumeResultHash) ||
		resumed.NewMutationLeaseEpoch != lease.Controller.LeaseEpoch ||
		resumed.NewMutationLeaseTokenDigest != controllerRecoverySuspensionTokenDigest(
			"picoclaw-pr-development-controller-suspended-resume-recovery-handoff-v1\x00",
			claimed.ResumeClaimToken,
		) ||
		resumed.NewMutationLeaseUntil == nil ||
		resumed.NewMutationLeaseUntil.Before(*claimed.ResumeClaimUntil) ||
		resumed.FinalResumeRevision != lease.Controller.Revision ||
		!validControllerSHA256(resumed.ResumeFinalHash) || resumed.ResumedAt == nil ||
		!resumed.UpdatedAt.Equal(*resumed.ResumedAt) {
		return errors.New("suspended resume recovery returned changed resume evidence")
	}
	for _, bearer := range []string{claimed.ResumeReservationKey, claimed.ResumeClaimToken} {
		if bearer != "" &&
			(strings.Contains(string(resumed.ResumeRequestJSON), bearer) ||
				strings.Contains(string(resumed.ResumeResultJSON), bearer)) {
			return errors.New("suspended resume recovery serialized a raw bearer")
		}
	}
	return validateControllerSuspendedResumeRecoveryChildLink(
		transition.Controller,
		resumed,
		transition.NextSuspension,
		claimed,
	)
}

func validateControllerSuspendedResumeRecoveryChildLink(
	controller eventing.PRDevelopmentController,
	resumed, child, claimed eventing.PRDevelopmentControllerSuspension,
) error {
	result := resumed.ResumeResult
	if child.ControllerID != resumed.ControllerID || child.ThreadID != resumed.ThreadID ||
		child.OwnerSessionID != resumed.OwnerSessionID ||
		child.AttemptID != resumed.ResumeAttemptID || child.Ordinal != resumed.Ordinal+1 ||
		child.SourceKind !=
			eventing.PRDevelopmentControllerSuspensionSourceSuspendedResumeRecovery ||
		child.SourceRecoveryID != resumed.ID || child.SourceOperationID != "" ||
		child.SourceOperationKind != "" ||
		child.SourceFinalRevision != resumed.FinalResumeRevision ||
		child.SourceFinalHash != resumed.ResumeFinalHash ||
		child.Mode != eventing.PRDevelopmentControllerSuspensionCandidate ||
		child.AgentID != resumed.AgentID || child.WorkspaceID != result.WorkspaceID ||
		child.LineID != resumed.LineID || child.SourceCloneURL != resumed.SourceCloneURL ||
		child.SourceRef != resumed.SourceRef || child.SourceCommit != resumed.SourceCommit ||
		child.SourceTree != resumed.SourceTree || child.LineVersion != result.Version ||
		child.MutationEpoch != result.MutationEpoch || child.TipCommit != result.Tip ||
		child.Tree != result.Tree ||
		child.SuspensionReservationDigest != resumed.ResumeReservationDigest ||
		child.MutationLeaseEpoch != resumed.NewMutationLeaseEpoch ||
		child.MutationLeaseTokenDigest != resumed.NewMutationLeaseTokenDigest ||
		child.SuspendIntentID != child.ID || child.SuspendRequest.IntentID != child.ID ||
		child.SuspendClaimID == "" || child.SuspendClaimOwner == "" ||
		child.SuspendClaimEpoch < 1 || child.SuspendClaims < 1 ||
		child.SuspendClaims != int(child.SuspendClaimEpoch) ||
		child.SuspendClaimedAt == nil || resumed.ResumedAt == nil ||
		!child.CreatedAt.Equal(*resumed.ResumedAt) ||
		child.SuspendClaimedAt.Before(child.CreatedAt) ||
		child.PreviousHash != resumed.ResumeFinalHash ||
		controller.ID != child.ControllerID ||
		controller.CurrentAttemptID != child.AttemptID ||
		controller.ThreadID != child.ThreadID ||
		controller.OwnerSessionID != child.OwnerSessionID || controller.AgentID != child.AgentID ||
		controller.WorkspaceID != child.WorkspaceID || controller.LineID != child.LineID ||
		controller.SourceCloneURL != child.SourceCloneURL ||
		controller.SourceRef != child.SourceRef ||
		controller.SourceCommit != child.SourceCommit ||
		controller.SourceTree != child.SourceTree || controller.LineVersion != child.LineVersion ||
		controller.MutationEpoch != child.MutationEpoch ||
		controller.TipCommit != child.TipCommit || controller.Tree != child.Tree ||
		controller.LeaseKind != "" || controller.LeaseOwner != "" ||
		controller.LeaseToken != "" || controller.LeaseUntil != nil ||
		controller.MutationReservationKey != "" ||
		controllerSuspensionHasResumeEvidence(child) {
		return errors.New("suspended resume recovery returned a changed child suspension")
	}
	request := child.SuspendRequest
	if request.Repository != child.SourceCloneURL || request.SourceRef != child.SourceRef ||
		request.SourceCommit != child.SourceCommit || request.AgentID != child.AgentID ||
		request.WorkspaceID != child.WorkspaceID || request.LineID != child.LineID ||
		request.ExpectedVersion != child.LineVersion ||
		request.ExpectedMutationEpoch != child.MutationEpoch ||
		request.ExpectedTip != child.TipCommit || request.ExpectedTree != child.Tree ||
		request.CommitIntentID != "" || request.CommitExpectedParent != "" ||
		request.CommitExpectedTree != "" || request.CommitCandidateDigest != "" ||
		request.CommitMessage != "" || !request.CommitAuthoredAt.IsZero() ||
		len(child.SuspendRequestJSON) == 0 ||
		!validControllerSHA256(child.SuspendRequestHash) ||
		!validControllerSHA256(child.IntentHash) {
		return errors.New("suspended resume recovery child request changed its line fence")
	}
	if claimed.ResumeReservationKey != "" &&
		strings.Contains(string(child.SuspendRequestJSON), claimed.ResumeReservationKey) {
		return errors.New("suspended resume recovery child serialized a raw reservation")
	}

	switch child.Status {
	case eventing.PRDevelopmentControllerSuspensionStatusSuspendClaimed:
		if controller.Phase != eventing.PRDevelopmentControllerSuspensionPending ||
			controller.Revision != child.SourceFinalRevision+1 ||
			child.SuspensionReservationKey != claimed.ResumeReservationKey ||
			child.SuspendRequest.ReservationKey != claimed.ResumeReservationKey ||
			child.SuspendClaimToken == "" || child.SuspendClaimUntil == nil ||
			child.SuspendClaimTokenDigest != "" ||
			child.SuspendResult != (eventing.PRDevelopmentControllerSuspensionResult{}) ||
			len(child.SuspendResultJSON) != 0 || child.SuspendResultHash != "" ||
			child.FinalSuspensionRevision != 0 || child.SuspensionFinalHash != "" ||
			child.SuspendedAt != nil {
			return errors.New("suspended resume recovery returned changed live child authority")
		}

	case eventing.PRDevelopmentControllerSuspensionStatusSuspended:
		if controller.Phase != eventing.PRDevelopmentControllerSuspended ||
			controller.Revision != child.SourceFinalRevision+2 ||
			child.SuspensionReservationKey != "" || child.SuspendRequest.ReservationKey != "" ||
			child.SuspendClaimToken != "" || child.SuspendClaimUntil != nil ||
			!validControllerSHA256(child.SuspendClaimTokenDigest) ||
			len(child.SuspendResultJSON) == 0 ||
			!validControllerSHA256(child.SuspendResultHash) ||
			child.FinalSuspensionRevision != controller.Revision ||
			!validControllerSHA256(child.SuspensionFinalHash) || child.SuspendedAt == nil ||
			!child.UpdatedAt.Equal(*child.SuspendedAt) ||
			!controller.UpdatedAt.Equal(*child.SuspendedAt) {
			return errors.New("suspended resume recovery returned changed terminal child authority")
		}

	default:
		return errors.New("suspended resume recovery returned an invalid child status")
	}
	return nil
}

func controllerSuspendedResumeRecoveryOwnsTransferredChild(
	resumed, claimedResume eventing.PRDevelopmentControllerSuspension,
	workerLabel string,
	child eventing.PRDevelopmentControllerSuspension,
) bool {
	return child.Status == eventing.PRDevelopmentControllerSuspensionStatusSuspendClaimed &&
		child.SuspendClaimID == controllerSuspendedResumeRecoveryChildClaimID(
			resumed.ID,
			resumed.ResumeClaimID,
		) && child.SuspendClaimOwner == workerLabel &&
		child.SuspendClaimOwner == resumed.ResumeClaimOwner &&
		child.SuspendClaimToken == claimedResume.ResumeClaimToken &&
		child.SuspendClaimEpoch == resumed.ResumeClaimEpoch &&
		child.SuspendClaimUntil != nil && resumed.NewMutationLeaseUntil != nil &&
		!child.SuspendClaimUntil.Before(*resumed.NewMutationLeaseUntil)
}

func validateControllerSuspendedResumeRecoveryChildLease(
	resumed, claimedResume eventing.PRDevelopmentControllerSuspension,
	workerLabel string,
	lease eventing.PRDevelopmentControllerSuspensionLease,
) error {
	child := lease.Suspension
	if child.SuspensionReservationKey != claimedResume.ResumeReservationKey ||
		child.SuspendRequest.ReservationKey != claimedResume.ResumeReservationKey ||
		child.SuspendClaimToken != claimedResume.ResumeClaimToken ||
		child.SuspendClaimUntil == nil || resumed.NewMutationLeaseUntil == nil ||
		child.SuspendClaimUntil.Before(*resumed.NewMutationLeaseUntil) ||
		child.SuspendClaimTokenDigest != "" ||
		child.SuspendResult != (eventing.PRDevelopmentControllerSuspensionResult{}) ||
		len(child.SuspendResultJSON) != 0 || child.SuspendResultHash != "" ||
		child.FinalSuspensionRevision != 0 || child.SuspensionFinalHash != "" ||
		child.SuspendedAt != nil {
		return errors.New("suspended resume recovery child claim changed transferred authority")
	}
	candidate := eventing.PRDevelopmentControllerSuspensionWorkCandidate{
		CaseID:           "suspended-resume-recovery",
		SuspensionID:     child.ID,
		ControllerID:     child.ControllerID,
		AttemptID:        child.AttemptID,
		SourceKind:       child.SourceKind,
		Mode:             child.Mode,
		ExpectedRevision: lease.Controller.Revision,
	}
	return validateControllerSuspensionLease(
		candidate,
		child.SuspendClaimID,
		workerLabel,
		lease,
	)
}

func validateControllerSuspendedResumeRecoveryTerminalChild(
	transition eventing.PRDevelopmentControllerSuspendedResumeRecoveryTransition,
	claimedResume eventing.PRDevelopmentControllerSuspension,
) error {
	child := transition.NextSuspension
	if child.Status != eventing.PRDevelopmentControllerSuspensionStatusSuspended ||
		transition.Resumed.NewMutationLeaseUntil == nil {
		return errors.New("suspended resume recovery replay returned changed terminal authority")
	}
	resumeResult, suspensionResult := transition.Resumed.ResumeResult, child.SuspendResult
	if suspensionResult.WorkspaceID != child.WorkspaceID ||
		suspensionResult.Version != child.LineVersion ||
		suspensionResult.MutationEpoch != child.MutationEpoch ||
		suspensionResult.Tip != child.TipCommit || suspensionResult.Tree != child.Tree ||
		suspensionResult.CandidateTree != resumeResult.CandidateTree ||
		suspensionResult.CandidateDigest != resumeResult.CandidateDigest ||
		suspensionResult.ChangedFileCount != resumeResult.ChangedFileCount ||
		!validControllerSHA256(suspensionResult.SuspensionHash) ||
		suspensionResult.PreparedCommit != "" || suspensionResult.PreparedTree != "" ||
		suspensionResult.PreparedCommitApplied || suspensionResult.AlreadySuspended {
		return errors.New("suspended resume recovery terminal child changed retained candidate evidence")
	}
	claimedChild := child
	claimedChild.Status = eventing.PRDevelopmentControllerSuspensionStatusSuspendClaimed
	claimedChild.SuspensionReservationKey = claimedResume.ResumeReservationKey
	claimedChild.SuspendRequest.ReservationKey = claimedResume.ResumeReservationKey
	claimedChild.SuspendClaimToken = "replayed-suspension-claim-token"
	claimedChild.SuspendClaimUntil = transition.Resumed.NewMutationLeaseUntil
	claimedChild.SuspendClaimTokenDigest = ""
	claimedChild.SuspendResult = eventing.PRDevelopmentControllerSuspensionResult{}
	claimedChild.SuspendResultJSON = nil
	claimedChild.SuspendResultHash = ""
	claimedChild.FinalSuspensionRevision = 0
	claimedChild.SuspensionFinalHash = ""
	claimedChild.SuspendedAt = nil
	claimedChild.UpdatedAt = child.CreatedAt
	return validateControllerSuspendedTransition(
		eventing.PRDevelopmentControllerSuspensionTransition{
			Controller: transition.Controller,
			Suspension: child,
		},
		claimedChild,
		child.SuspendResult,
	)
}

func validateControllerSuspensionCandidate(
	candidate eventing.PRDevelopmentControllerSuspensionWorkCandidate,
) error {
	if candidate.CaseID == "" || candidate.CaseID != strings.TrimSpace(candidate.CaseID) ||
		candidate.SuspensionID == "" ||
		candidate.SuspensionID != strings.TrimSpace(candidate.SuspensionID) ||
		candidate.ControllerID == "" ||
		candidate.ControllerID != strings.TrimSpace(candidate.ControllerID) ||
		candidate.AttemptID == "" || candidate.AttemptID != strings.TrimSpace(candidate.AttemptID) ||
		candidate.ExpectedRevision < 2 {
		return fmt.Errorf("%w: controller suspension candidate is incomplete", ErrUnavailable)
	}
	switch candidate.SourceKind {
	case eventing.PRDevelopmentControllerSuspensionSourceControllerRecovery,
		eventing.PRDevelopmentControllerSuspensionSourceSuspendedResumeRecovery:
		if candidate.Mode != eventing.PRDevelopmentControllerSuspensionCandidate {
			return fmt.Errorf("%w: controller suspension candidate mode is invalid", ErrUnavailable)
		}
	case eventing.PRDevelopmentControllerSuspensionSourceOperationRecovery:
		if candidate.Mode != eventing.PRDevelopmentControllerSuspensionCandidate &&
			candidate.Mode != eventing.PRDevelopmentControllerSuspensionCommitRecovery {
			return fmt.Errorf("%w: operation suspension candidate mode is invalid", ErrUnavailable)
		}
	default:
		return fmt.Errorf("%w: controller suspension source is invalid", ErrUnavailable)
	}
	return nil
}

func validateControllerSuspensionLease(
	candidate eventing.PRDevelopmentControllerSuspensionWorkCandidate,
	claimID, workerLabel string,
	lease eventing.PRDevelopmentControllerSuspensionLease,
) error {
	controller, suspension := lease.Controller, lease.Suspension
	request := suspension.SuspendRequest
	if controller.ID != candidate.ControllerID ||
		controller.CurrentAttemptID != candidate.AttemptID ||
		controller.Revision != candidate.ExpectedRevision ||
		controller.Phase != eventing.PRDevelopmentControllerSuspensionPending ||
		controller.LeaseKind != "" || controller.LeaseOwner != "" ||
		controller.LeaseToken != "" || controller.LeaseUntil != nil ||
		controller.MutationReservationKey != "" ||
		suspension.ID != candidate.SuspensionID ||
		suspension.ControllerID != candidate.ControllerID ||
		suspension.AttemptID != candidate.AttemptID ||
		suspension.SourceKind != candidate.SourceKind || suspension.Mode != candidate.Mode ||
		suspension.SourceFinalRevision+1 != candidate.ExpectedRevision ||
		suspension.Status != eventing.PRDevelopmentControllerSuspensionStatusSuspendClaimed ||
		suspension.SuspendClaimID != claimID ||
		suspension.SuspendClaimOwner != workerLabel ||
		suspension.SuspendClaimToken == "" || suspension.SuspendClaimUntil == nil ||
		suspension.SuspendClaimedAt == nil || suspension.SuspendClaimEpoch < 1 ||
		suspension.SuspendClaims < 1 || int64(suspension.SuspendClaims) != suspension.SuspendClaimEpoch ||
		suspension.SuspendClaimTokenDigest != "" ||
		suspension.SuspensionReservationKey == "" ||
		request.ReservationKey != suspension.SuspensionReservationKey ||
		suspension.SuspendIntentID != suspension.ID || request.IntentID != suspension.ID {
		return errors.New("controller suspension claim returned changed authority")
	}
	if controller.ThreadID != suspension.ThreadID ||
		controller.OwnerSessionID != suspension.OwnerSessionID ||
		controller.AgentID != suspension.AgentID ||
		controller.WorkspaceID != suspension.WorkspaceID || controller.LineID != suspension.LineID ||
		controller.SourceCloneURL != suspension.SourceCloneURL ||
		controller.SourceRef != suspension.SourceRef ||
		controller.SourceCommit != suspension.SourceCommit ||
		controller.SourceTree != suspension.SourceTree ||
		controller.LineVersion != suspension.LineVersion ||
		controller.MutationEpoch != suspension.MutationEpoch ||
		controller.TipCommit != suspension.TipCommit || controller.Tree != suspension.Tree {
		return errors.New("controller suspension claim changed its retained line fence")
	}
	if request.Repository != suspension.SourceCloneURL ||
		request.SourceRef != suspension.SourceRef || request.SourceCommit != suspension.SourceCommit ||
		request.AgentID != suspension.AgentID || request.WorkspaceID != suspension.WorkspaceID ||
		request.LineID != suspension.LineID ||
		request.ExpectedVersion != suspension.LineVersion ||
		request.ExpectedMutationEpoch != suspension.MutationEpoch ||
		request.ExpectedTip != suspension.TipCommit || request.ExpectedTree != suspension.Tree ||
		suspension.MutationEpoch != suspension.LineVersion+1 ||
		!sameControllerObjectIDWidth(
			suspension.SourceCommit,
			suspension.SourceTree,
			suspension.TipCommit,
			suspension.Tree,
		) {
		return errors.New("controller suspension request changed its durable line fence")
	}
	if strings.Contains(
		string(suspension.SuspendRequestJSON),
		suspension.SuspensionReservationKey,
	) {
		return errors.New("controller suspension request blob contains a raw reservation bearer")
	}
	if err := validateControllerSuspensionSource(suspension); err != nil {
		return err
	}
	if suspension.Mode == eventing.PRDevelopmentControllerSuspensionCandidate {
		if request.CommitIntentID != "" || request.CommitExpectedParent != "" ||
			request.CommitExpectedTree != "" || request.CommitCandidateDigest != "" ||
			request.CommitMessage != "" || !request.CommitAuthoredAt.IsZero() {
			return errors.New("candidate suspension claim contains Commit authority")
		}
	} else if request.CommitIntentID == "" ||
		request.CommitExpectedParent != suspension.TipCommit ||
		!sameControllerObjectIDWidth(
			suspension.SourceCommit,
			request.CommitExpectedParent,
			request.CommitExpectedTree,
		) || request.CommitExpectedTree == suspension.Tree ||
		!validControllerSHA256(request.CommitCandidateDigest) ||
		request.CommitMessage == "" || request.CommitAuthoredAt.IsZero() {
		return errors.New("Commit recovery suspension claim changed its prepared request")
	}
	if suspension.FinalSuspensionRevision != 0 || suspension.SuspendedAt != nil ||
		suspension.SuspensionFinalHash != "" || suspension.SuspendResultHash != "" ||
		len(suspension.SuspendResultJSON) != 0 ||
		suspension.SuspendResult != (eventing.PRDevelopmentControllerSuspensionResult{}) ||
		controllerSuspensionHasResumeEvidence(suspension) {
		return errors.New("controller suspension claim contains premature terminal authority")
	}
	return nil
}

func validateControllerSuspensionSource(
	suspension eventing.PRDevelopmentControllerSuspension,
) error {
	switch suspension.SourceKind {
	case eventing.PRDevelopmentControllerSuspensionSourceControllerRecovery:
		if suspension.SourceRecoveryID == "" || suspension.SourceOperationID != "" ||
			suspension.SourceOperationKind != "" ||
			suspension.Mode != eventing.PRDevelopmentControllerSuspensionCandidate {
			return errors.New("controller recovery suspension claim has a changed source")
		}
	case eventing.PRDevelopmentControllerSuspensionSourceOperationRecovery:
		if suspension.SourceRecoveryID == "" || suspension.SourceOperationID == "" {
			return errors.New("operation recovery suspension claim has an incomplete source")
		}
		switch suspension.SourceOperationKind {
		case eventing.PRDevelopmentControllerOperationAdopt,
			eventing.PRDevelopmentControllerOperationResume:
			if suspension.Mode != eventing.PRDevelopmentControllerSuspensionCandidate {
				return errors.New("line recovery suspension claim has a changed mode")
			}
		case eventing.PRDevelopmentControllerOperationCommit:
			if suspension.Mode != eventing.PRDevelopmentControllerSuspensionCommitRecovery {
				return errors.New("Commit recovery suspension claim has a changed mode")
			}
		default:
			return errors.New("operation recovery suspension claim has an invalid source kind")
		}
	case eventing.PRDevelopmentControllerSuspensionSourceSuspendedResumeRecovery:
		if suspension.SourceRecoveryID == "" || suspension.SourceOperationID != "" ||
			suspension.SourceOperationKind != "" ||
			suspension.Mode != eventing.PRDevelopmentControllerSuspensionCandidate {
			return errors.New("suspended-resume recovery suspension claim has a changed source")
		}
	default:
		return errors.New("controller suspension claim has an invalid source")
	}
	return nil
}

func controllerSuspensionHasResumeEvidence(
	suspension eventing.PRDevelopmentControllerSuspension,
) bool {
	return suspension.ResumeAttemptID != "" || suspension.ResumeIntentID != "" ||
		suspension.ResumeReservationKey != "" || suspension.ResumeReservationDigest != "" ||
		suspension.ResumeRequest != (eventing.PRDevelopmentControllerSuspendedResumeRequest{}) ||
		len(suspension.ResumeRequestJSON) != 0 || suspension.ResumeRequestHash != "" ||
		suspension.ResumeIntentHash != "" || suspension.ResumePreparedAt != nil ||
		suspension.ResumeClaimID != "" || suspension.ResumeClaimOwner != "" ||
		suspension.ResumeClaimToken != "" || suspension.ResumeClaimUntil != nil ||
		suspension.ResumeClaimEpoch != 0 || suspension.ResumeClaims != 0 ||
		suspension.ResumeClaimedAt != nil || suspension.ResumeClaimTokenDigest != "" ||
		suspension.ResumeResult != (eventing.PRDevelopmentControllerSuspendedResumeResult{}) ||
		len(suspension.ResumeResultJSON) != 0 || suspension.ResumeResultHash != "" ||
		suspension.NewMutationLeaseEpoch != 0 || suspension.NewMutationLeaseTokenDigest != "" ||
		suspension.NewMutationLeaseUntil != nil || suspension.FinalResumeRevision != 0 ||
		suspension.ResumeFinalHash != "" || suspension.ResumedAt != nil
}

func sameControllerOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func validateControllerSuspendedTransition(
	transition eventing.PRDevelopmentControllerSuspensionTransition,
	claimed eventing.PRDevelopmentControllerSuspension,
	result eventing.PRDevelopmentControllerSuspensionResult,
) error {
	controller, suspension := transition.Controller, transition.Suspension
	if controller.ID != claimed.ControllerID ||
		controller.CurrentAttemptID != claimed.AttemptID ||
		controller.Revision != claimed.SourceFinalRevision+2 ||
		controller.Phase != eventing.PRDevelopmentControllerSuspended ||
		controller.ThreadID != claimed.ThreadID ||
		controller.OwnerSessionID != claimed.OwnerSessionID ||
		controller.AgentID != claimed.AgentID || controller.WorkspaceID != claimed.WorkspaceID ||
		controller.LineID != claimed.LineID || controller.SourceCloneURL != claimed.SourceCloneURL ||
		controller.SourceRef != claimed.SourceRef || controller.SourceCommit != claimed.SourceCommit ||
		controller.SourceTree != claimed.SourceTree || controller.LineVersion != claimed.LineVersion ||
		controller.MutationEpoch != claimed.MutationEpoch ||
		controller.TipCommit != claimed.TipCommit || controller.Tree != claimed.Tree ||
		controller.LeaseKind != "" || controller.LeaseOwner != "" ||
		controller.LeaseToken != "" || controller.LeaseUntil != nil ||
		controller.MutationReservationKey != "" {
		return errors.New("controller suspension did not return a bearer-free suspended controller")
	}
	if suspension.ID != claimed.ID || suspension.ControllerID != claimed.ControllerID ||
		suspension.ThreadID != claimed.ThreadID ||
		suspension.OwnerSessionID != claimed.OwnerSessionID ||
		suspension.AttemptID != claimed.AttemptID || suspension.Ordinal != claimed.Ordinal ||
		suspension.SourceKind != claimed.SourceKind ||
		suspension.SourceRecoveryID != claimed.SourceRecoveryID ||
		suspension.SourceOperationID != claimed.SourceOperationID ||
		suspension.SourceOperationKind != claimed.SourceOperationKind ||
		suspension.SourceFinalRevision != claimed.SourceFinalRevision ||
		suspension.SourceFinalHash != claimed.SourceFinalHash ||
		suspension.Mode != claimed.Mode ||
		suspension.Status != eventing.PRDevelopmentControllerSuspensionStatusSuspended ||
		suspension.SuspendIntentID != claimed.SuspendIntentID ||
		suspension.SuspendClaimID != claimed.SuspendClaimID ||
		suspension.SuspendClaimOwner != claimed.SuspendClaimOwner ||
		suspension.SuspendClaimEpoch != claimed.SuspendClaimEpoch ||
		suspension.SuspendClaims != claimed.SuspendClaims ||
		!sameControllerOptionalTime(suspension.SuspendClaimedAt, claimed.SuspendClaimedAt) ||
		suspension.FinalSuspensionRevision != controller.Revision ||
		suspension.SuspendedAt == nil || !suspension.CreatedAt.Equal(claimed.CreatedAt) ||
		!suspension.UpdatedAt.Equal(*suspension.SuspendedAt) ||
		!controller.UpdatedAt.Equal(*suspension.SuspendedAt) {
		return errors.New("controller suspension finalization returned changed durable identity")
	}
	if suspension.AgentID != claimed.AgentID || suspension.WorkspaceID != claimed.WorkspaceID ||
		suspension.LineID != claimed.LineID || suspension.SourceCloneURL != claimed.SourceCloneURL ||
		suspension.SourceRef != claimed.SourceRef || suspension.SourceCommit != claimed.SourceCommit ||
		suspension.SourceTree != claimed.SourceTree || suspension.LineVersion != claimed.LineVersion ||
		suspension.MutationEpoch != claimed.MutationEpoch || suspension.TipCommit != claimed.TipCommit ||
		suspension.Tree != claimed.Tree ||
		suspension.SuspensionReservationDigest != claimed.SuspensionReservationDigest ||
		suspension.MutationLeaseEpoch != claimed.MutationLeaseEpoch ||
		suspension.MutationLeaseTokenDigest != claimed.MutationLeaseTokenDigest ||
		suspension.SuspendRequestHash != claimed.SuspendRequestHash ||
		suspension.PreviousHash != claimed.PreviousHash || suspension.IntentHash != claimed.IntentHash ||
		string(suspension.SuspendRequestJSON) != string(claimed.SuspendRequestJSON) {
		return errors.New("controller suspension finalization changed its retained line proof")
	}
	expectedRequest := claimed.SuspendRequest
	expectedRequest.ReservationKey = ""
	expectedResult := result
	expectedResult.AlreadySuspended = false
	if suspension.SuspensionReservationKey != "" ||
		suspension.SuspendRequest != expectedRequest ||
		suspension.SuspendClaimToken != "" || suspension.SuspendClaimUntil != nil ||
		!validControllerSHA256(suspension.SuspendClaimTokenDigest) ||
		suspension.SuspendResult != expectedResult || len(suspension.SuspendResultJSON) == 0 ||
		!validControllerSHA256(suspension.SuspendResultHash) ||
		!validControllerSHA256(suspension.SuspensionFinalHash) ||
		controllerSuspensionHasResumeEvidence(suspension) {
		return errors.New("controller suspension finalization retained bearer authority")
	}
	for _, bearer := range []string{
		claimed.SuspensionReservationKey,
		claimed.SuspendClaimToken,
	} {
		if bearer != "" &&
			(strings.Contains(string(suspension.SuspendRequestJSON), bearer) ||
				strings.Contains(string(suspension.SuspendResultJSON), bearer)) {
			return errors.New("controller suspension finalization serialized a raw bearer")
		}
	}
	return nil
}

func validateControllerRecoveryCandidate(
	candidate eventing.PRDevelopmentControllerRecoveryCandidate,
) error {
	if candidate.CaseID == "" || candidate.CaseID != strings.TrimSpace(candidate.CaseID) ||
		candidate.ControllerID == "" ||
		candidate.ControllerID != strings.TrimSpace(candidate.ControllerID) ||
		candidate.AttemptID == "" || candidate.AttemptID != strings.TrimSpace(candidate.AttemptID) ||
		candidate.ExpectedRevision < 1 {
		return fmt.Errorf("%w: controller recovery candidate is incomplete", ErrUnavailable)
	}
	switch candidate.Kind {
	case eventing.PRDevelopmentControllerRecoveryWorkReservation:
		if candidate.RecoveryID == "" || candidate.OperationID != "" {
			return fmt.Errorf("%w: reservation recovery candidate is invalid", ErrUnavailable)
		}
	case eventing.PRDevelopmentControllerRecoveryWorkOperation:
		if candidate.OperationID == "" {
			return fmt.Errorf("%w: operation recovery candidate is invalid", ErrUnavailable)
		}
	default:
		return fmt.Errorf("%w: unknown controller recovery candidate", ErrUnavailable)
	}
	return nil
}

func validateControllerReservationRecoveryLease(
	candidate eventing.PRDevelopmentControllerRecoveryCandidate,
	claimID, workerLabel string,
	lease eventing.PRDevelopmentControllerRecoveryLease,
) error {
	controller, intent := lease.Controller, lease.Intent
	if controller.ID != candidate.ControllerID ||
		controller.CurrentAttemptID != candidate.AttemptID ||
		controller.Revision != candidate.ExpectedRevision ||
		controller.Phase != eventing.PRDevelopmentControllerRecoveryRequired ||
		intent.ID != candidate.RecoveryID || intent.ControllerID != controller.ID ||
		intent.AttemptID != candidate.AttemptID ||
		intent.RecoveryRevision != candidate.ExpectedRevision ||
		intent.Mode != eventing.PRDevelopmentControllerRecoveryBound ||
		intent.Status != eventing.PRDevelopmentControllerRecoveryClaimed ||
		intent.ClaimID != claimID || intent.ClaimOwner != workerLabel ||
		intent.ClaimToken == "" || intent.ClaimUntil == nil || intent.ClaimEpoch < 1 ||
		intent.PreviousReservationKey == "" || intent.ReplacementReservationKey == "" ||
		intent.PreviousReservationKey == intent.ReplacementReservationKey ||
		controller.MutationReservationKey != intent.PreviousReservationKey ||
		intent.AgentID == "" || intent.WorkspaceID == "" || intent.LineID == "" {
		return errors.New("reservation recovery claim returned changed authority")
	}
	return nil
}

func validateControllerOperationRecoveryLease(
	candidate eventing.PRDevelopmentControllerRecoveryCandidate,
	claimID, workerLabel string,
	lease eventing.PRDevelopmentControllerOperationRecoveryLease,
) error {
	controller, operation := lease.Controller, lease.Operation
	if controller.ID != candidate.ControllerID ||
		controller.CurrentAttemptID != candidate.AttemptID ||
		controller.Revision != candidate.ExpectedRevision ||
		controller.Phase != eventing.PRDevelopmentControllerRecoveryRequired ||
		controller.MutationReservationKey == "" ||
		operation.ID != candidate.OperationID || operation.ControllerID != controller.ID ||
		operation.AttemptID != candidate.AttemptID ||
		operation.RecoveryRevision != candidate.ExpectedRevision ||
		operation.Status != eventing.PRDevelopmentControllerOperationRecoveryClaimed ||
		operation.ClaimID != claimID || operation.ClaimOwner != workerLabel ||
		operation.ClaimToken == "" || operation.ClaimUntil == nil || operation.ClaimEpoch < 1 ||
		operation.SourceCloneURL == "" || operation.WorkspaceID == "" || operation.LineID == "" ||
		operation.Request.AgentID != operation.AgentID ||
		operation.Request.WorkspaceID != operation.WorkspaceID ||
		operation.Request.LineID != operation.LineID ||
		operation.Request.SourceRef != operation.SourceRef ||
		operation.Request.SourceCommit != operation.SourceCommit {
		return errors.New("operation recovery claim returned changed authority")
	}
	if operation.Kind == eventing.PRDevelopmentControllerOperationPark {
		if operation.RecoveryID != "" || operation.ReplacementReservationKey != "" ||
			candidate.RecoveryID != "" {
			return errors.New("Park recovery claim unexpectedly carried replacement authority")
		}
		return nil
	}
	if operation.RecoveryID == "" || operation.RecoveryID != candidate.RecoveryID ||
		operation.ReplacementReservationKey == "" ||
		operation.ReplacementReservationKey == controller.MutationReservationKey {
		return errors.New("operation recovery claim has invalid replacement authority")
	}
	return nil
}

func validateRecoveredSuspensionController(
	controller eventing.PRDevelopmentController,
	controllerID, attemptID, lineID string,
) error {
	if controller.ID != controllerID || controller.CurrentAttemptID != attemptID ||
		controller.LineID != lineID ||
		(controller.Phase != eventing.PRDevelopmentControllerSuspensionPending &&
			controller.Phase != eventing.PRDevelopmentControllerSuspended) ||
		controller.LeaseKind != "" || controller.LeaseOwner != "" ||
		controller.LeaseToken != "" || controller.LeaseUntil != nil ||
		controller.MutationReservationKey != "" {
		return errors.New("controller recovery did not enter bearer-free suspension lifecycle")
	}
	return nil
}

func validateRecoveredParkTransition(
	transition eventing.PRDevelopmentControllerOperationTransition,
	operation eventing.PRDevelopmentControllerOperation,
) error {
	controller := transition.Controller
	if transition.Operation.ID != operation.ID ||
		transition.Operation.Status != eventing.PRDevelopmentControllerOperationFinalized ||
		transition.Fence == nil || transition.Fence.AttemptID != operation.AttemptID ||
		transition.Fence.ControllerID != operation.ControllerID ||
		transition.Fence.LineID != operation.LineID ||
		controller.ID != operation.ControllerID ||
		controller.CurrentAttemptID != operation.AttemptID ||
		controller.Phase != eventing.PRDevelopmentControllerReviewPending ||
		controller.LeaseKind != "" || controller.LeaseToken != "" ||
		controller.LeaseUntil != nil || controller.MutationReservationKey != "" {
		return errors.New("controller Park recovery returned changed review handoff")
	}
	return nil
}

func controllerRecoveryClaimID(
	workerLabel string,
	candidate eventing.PRDevelopmentControllerRecoveryCandidate,
) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("picoclaw-pr-development-controller-recovery-claim-v1\x00"))
	for _, value := range []string{
		workerLabel,
		string(candidate.Kind),
		candidate.CaseID,
		candidate.ControllerID,
		candidate.AttemptID,
		candidate.RecoveryID,
		candidate.OperationID,
		strconv.FormatInt(candidate.ExpectedRevision, 10),
	} {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	return controllerRecoveryClaimIDPrefix + hex.EncodeToString(digest.Sum(nil)[:16])
}

func controllerSuspensionClaimID(
	workerLabel string,
	candidate eventing.PRDevelopmentControllerSuspensionWorkCandidate,
) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("picoclaw-pr-development-controller-suspension-claim-v1\x00"))
	for _, value := range []string{
		workerLabel,
		candidate.CaseID,
		candidate.SuspensionID,
		candidate.ControllerID,
		candidate.AttemptID,
		string(candidate.SourceKind),
		string(candidate.Mode),
		strconv.FormatInt(candidate.ExpectedRevision, 10),
	} {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	return controllerSuspensionClaimIDPrefix + hex.EncodeToString(digest.Sum(nil)[:16])
}

func controllerSuspendedResumeRecoveryClaimID(
	workerLabel string,
	candidate eventing.PRDevelopmentControllerSuspendedResumeRecoveryCandidate,
) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte(
		"picoclaw-pr-development-controller-suspended-resume-recovery-claim-v1\x00",
	))
	for _, value := range []string{
		workerLabel,
		candidate.CaseID,
		candidate.SuspensionID,
		candidate.ControllerID,
		candidate.AttemptID,
		strconv.FormatInt(candidate.ExpectedRevision, 10),
	} {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	return controllerSuspendedResumeRecoveryClaimIDPrefix +
		hex.EncodeToString(digest.Sum(nil)[:16])
}

func controllerSuspendedResumeRecoveryChildClaimID(
	resumedSuspensionID, recoveryClaimID string,
) string {
	digest := sha256.New()
	for _, value := range []string{
		"picoclaw-pr-development-controller-suspended-resume-identity-v1\x00",
		controllerSuspendedResumeRecoveryChildClaimIDPrefix,
		resumedSuspensionID,
		recoveryClaimID,
	} {
		writeControllerRecoveryHashField(digest, value)
	}
	return controllerSuspendedResumeRecoveryChildClaimIDPrefix +
		hex.EncodeToString(digest.Sum(nil)[:16])
}

func writeControllerRecoveryHashField(digest interface{ Write([]byte) (int, error) }, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(value))
}

func controllerRecoveryReservationDigest(reservation string) string {
	digest := sha256.Sum256([]byte(
		"picoclaw-pr-development-mutation-reservation-v1\x00" + reservation,
	))
	return hex.EncodeToString(digest[:])
}

func controllerRecoverySuspensionTokenDigest(domain, token string) string {
	digest := sha256.New()
	writeControllerRecoveryHashField(digest, domain)
	writeControllerRecoveryHashField(digest, token)
	return hex.EncodeToString(digest.Sum(nil))
}

type controllerRecoveryHeartbeat struct {
	renew  func(context.Context, time.Duration) error
	lease  time.Duration
	cancel context.CancelFunc
	done   chan struct{}
	errs   chan error

	mu       sync.RWMutex
	terminal atomic.Bool
	interval time.Duration
}

func startControllerRecoveryHeartbeat(
	parent context.Context,
	renew func(context.Context, time.Duration) error,
	lease, interval time.Duration,
) (context.Context, *controllerRecoveryHeartbeat) {
	workCtx, cancel := context.WithCancel(parent)
	heartbeat := &controllerRecoveryHeartbeat{
		renew:    renew,
		lease:    lease,
		cancel:   cancel,
		done:     make(chan struct{}),
		errs:     make(chan error, 1),
		interval: interval,
	}
	go heartbeat.run(workCtx)
	return workCtx, heartbeat
}

func controllerRecoveryHeartbeatEvery(lease time.Duration) time.Duration {
	interval := lease / 3
	if interval < time.Second {
		return time.Second
	}
	return interval
}

func (heartbeat *controllerRecoveryHeartbeat) BeginTerminal() error {
	if heartbeat == nil {
		return nil
	}
	heartbeat.terminal.Store(true)
	heartbeat.mu.Lock()
	heartbeat.mu.Unlock()
	select {
	case err := <-heartbeat.errs:
		return err
	default:
		return nil
	}
}

func (heartbeat *controllerRecoveryHeartbeat) Stop() error {
	if heartbeat == nil {
		return nil
	}
	heartbeat.cancel()
	<-heartbeat.done
	select {
	case err := <-heartbeat.errs:
		return err
	default:
		return nil
	}
}

func (heartbeat *controllerRecoveryHeartbeat) run(ctx context.Context) {
	defer close(heartbeat.done)
	interval := heartbeat.interval
	if interval <= 0 {
		interval = controllerRecoveryHeartbeatEvery(heartbeat.lease)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if heartbeat.renewClaim(ctx) {
				return
			}
		}
	}
}

// renewClaim reports whether the heartbeat must stop. Any renewal that began
// before the terminal barrier publishes its error while still holding the
// read side of that barrier, so BeginTerminal cannot miss a known claim loss.
func (heartbeat *controllerRecoveryHeartbeat) renewClaim(ctx context.Context) bool {
	heartbeat.mu.RLock()
	defer heartbeat.mu.RUnlock()
	if heartbeat.terminal.Load() {
		return false
	}
	if err := heartbeat.renew(ctx, heartbeat.lease); err != nil {
		if ctx.Err() != nil {
			return true
		}
		select {
		case heartbeat.errs <- fmt.Errorf("renew controller recovery claim: %w", err):
		default:
		}
		heartbeat.cancel()
		return true
	}
	return false
}
