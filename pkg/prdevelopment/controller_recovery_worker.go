package prdevelopment

import (
	"context"
	"crypto/sha256"
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
	defaultControllerRecoveryWorkerLabel = "gateway-pr-development-recovery"
	controllerRecoveryStageBatch         = 32
	controllerRecoveryClaimIDPrefix      = "pdrc_"
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

// ControllerRecoveryWorker reconciles at most one oldest exact recovery per
// call. Every non-Park effect is finalized into suspension_pending; Park moves
// directly to the existing reservation-free review handoff.
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

// ProcessOne stages a bounded prefix of expired mutation leases and processes
// at most one exact oldest claimable recovery. A claimed recovery is never
// released on failure: its idempotent Git effect remains reclaimable after the
// scheduling lease expires.
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
	if _, err := worker.store.StageExpiredPRDevelopmentControllerRecoveries(
		ctx,
		controllerRecoveryStageBatch,
	); err != nil {
		return false, fmt.Errorf("stage expired controller recoveries: %w", err)
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
		return true, worker.processReservationRecovery(ctx, lease)

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
		return true, worker.processOperationRecovery(ctx, lease)

	default:
		return false, fmt.Errorf("%w: unknown controller recovery kind", ErrUnavailable)
	}
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

func (worker *ControllerRecoveryWorker) processReservationRecovery(
	ctx context.Context,
	lease eventing.PRDevelopmentControllerRecoveryLease,
) error {
	intent := lease.Intent
	return worker.processClaim(
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
				return validateSuspensionPendingController(
					controller,
					lease.Controller.ID,
					intent.AttemptID,
					intent.LineID,
				)
			}, nil
		},
	)
}

func (worker *ControllerRecoveryWorker) processOperationRecovery(
	ctx context.Context,
	lease eventing.PRDevelopmentControllerOperationRecoveryLease,
) error {
	operation := lease.Operation
	return worker.processClaim(
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
				return validateSuspensionPendingController(
					transition.Controller,
					lease.Controller.ID,
					operation.AttemptID,
					operation.LineID,
				)
			}, nil
		},
	)
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

func validateSuspensionPendingController(
	controller eventing.PRDevelopmentController,
	controllerID, attemptID, lineID string,
) error {
	if controller.ID != controllerID || controller.CurrentAttemptID != attemptID ||
		controller.LineID != lineID ||
		controller.Phase != eventing.PRDevelopmentControllerSuspensionPending ||
		controller.LeaseKind != "" || controller.LeaseOwner != "" ||
		controller.LeaseToken != "" || controller.LeaseUntil != nil ||
		controller.MutationReservationKey != "" {
		return errors.New("controller recovery did not enter bearer-free suspension_pending")
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
