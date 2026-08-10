//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"errors"
)

type prDevelopmentControllerSuspensionStats struct {
	active        *PRDevelopmentControllerSuspension
	latestResumed *PRDevelopmentControllerSuspension
}

func validatePRDevelopmentControllerSuspensionChain(
	ctx context.Context,
	queryer rowsQueryer,
	controller PRDevelopmentController,
	session PRDevelopmentRepairSession,
	attemptOrdinals map[string]int,
) (prDevelopmentControllerSuspensionStats, error) {
	var stats prDevelopmentControllerSuspensionStats
	suspensions, err := loadPRDevelopmentControllerSuspensions(
		ctx,
		queryer,
		controller.ID,
	)
	if err != nil {
		return stats, err
	}
	if len(suspensions) == 0 {
		if controller.Phase == PRDevelopmentControllerSuspensionPending ||
			controller.Phase == PRDevelopmentControllerSuspended {
			return stats, errors.New(
				"stored suspended controller has no suspension evidence",
			)
		}
		return stats, nil
	}
	latestAttemptID := session.Attempts[len(session.Attempts)-1].ID
	for index := range suspensions {
		suspension := suspensions[index]
		if _, owned := attemptOrdinals[suspension.AttemptID]; !owned ||
			suspension.ControllerID != controller.ID ||
			suspension.ThreadID != controller.ThreadID ||
			suspension.OwnerSessionID != controller.OwnerSessionID ||
			suspension.AgentID != controller.AgentID ||
			suspension.WorkspaceID != controller.WorkspaceID ||
			suspension.LineID != controller.LineID ||
			suspension.SourceCloneURL != controller.SourceCloneURL ||
			suspension.SourceRef != controller.SourceRef ||
			suspension.SourceCommit != controller.SourceCommit ||
			suspension.SourceTree != controller.SourceTree ||
			suspension.CreatedAt.Before(controller.CreatedAt) ||
			suspension.CreatedAt.After(controller.UpdatedAt) ||
			suspension.SourceFinalRevision >= controller.Revision {
			return stats, errors.New(
				"stored controller suspension ownership or high-water state is invalid",
			)
		}
		if err := validatePRDevelopmentControllerSuspensionSourceLink(
			ctx,
			queryer,
			suspension,
		); err != nil {
			return stats, err
		}
		if suspension.Status == PRDevelopmentControllerSuspensionStatusResumed {
			if suspension.ResumedAt == nil ||
				suspension.FinalResumeRevision > controller.Revision ||
				suspension.ResumedAt.After(controller.UpdatedAt) {
				return stats, errors.New(
					"stored resumed controller suspension exceeds controller high-water",
				)
			}
			suspensionCopy := suspension
			stats.latestResumed = &suspensionCopy
			continue
		}
		if index != len(suspensions)-1 || stats.active != nil {
			return stats, errors.New(
				"stored active controller suspension is not the chain tail",
			)
		}
		suspensionCopy := suspension
		stats.active = &suspensionCopy
	}
	if stats.active == nil {
		if controller.Phase == PRDevelopmentControllerSuspensionPending ||
			controller.Phase == PRDevelopmentControllerSuspended {
			return stats, errors.New(
				"stored suspended controller has no active suspension",
			)
		}
		return stats, nil
	}
	active := *stats.active
	if controller.LineVersion != active.LineVersion ||
		controller.MutationEpoch != active.MutationEpoch ||
		controller.TipCommit != active.TipCommit || controller.Tree != active.Tree ||
		controller.LeaseEpoch != active.MutationLeaseEpoch {
		return stats, errors.New(
			"stored active suspension differs from controller line state",
		)
	}
	switch active.Status {
	case PRDevelopmentControllerSuspensionStatusSuspendPending,
		PRDevelopmentControllerSuspensionStatusSuspendClaimed:
		if controller.Phase != PRDevelopmentControllerSuspensionPending ||
			controller.CurrentAttemptID != active.AttemptID ||
			controller.Revision != active.SourceFinalRevision+1 ||
			!controller.UpdatedAt.Equal(active.CreatedAt) {
			return stats, errors.New(
				"stored pending suspension differs from controller handoff",
			)
		}
	case PRDevelopmentControllerSuspensionStatusSuspended:
		if controller.Phase != PRDevelopmentControllerSuspended ||
			controller.CurrentAttemptID != active.AttemptID ||
			controller.Revision != active.FinalSuspensionRevision ||
			active.SuspendedAt == nil ||
			!controller.UpdatedAt.Equal(*active.SuspendedAt) {
			return stats, errors.New(
				"stored completed suspension differs from its controller",
			)
		}
	case PRDevelopmentControllerSuspensionStatusResumePending,
		PRDevelopmentControllerSuspensionStatusResumeClaimed:
		if _, owned := attemptOrdinals[active.ResumeAttemptID]; !owned ||
			active.ResumeAttemptID != latestAttemptID ||
			controller.Phase != PRDevelopmentControllerSuspended ||
			controller.CurrentAttemptID != active.ResumeAttemptID ||
			controller.Revision != active.FinalSuspensionRevision+1 ||
			active.ResumePreparedAt == nil ||
			!controller.UpdatedAt.Equal(*active.ResumePreparedAt) {
			return stats, errors.New(
				"stored pending suspended resume differs from its controller",
			)
		}
	default:
		return stats, errors.New("stored active suspension status is invalid")
	}
	return stats, nil
}

func validatePRDevelopmentControllerSuspensionSourceLink(
	ctx context.Context,
	queryer rowsQueryer,
	suspension PRDevelopmentControllerSuspension,
) error {
	if suspension.SourceKind ==
		PRDevelopmentControllerSuspensionSourceSuspendedResumeRecovery {
		resumed, found, err := loadPRDevelopmentControllerSuspensionByID(
			ctx,
			queryer,
			suspension.SourceRecoveryID,
		)
		if err != nil {
			return err
		}
		if !found {
			return errors.New(
				"stored suspended-resume recovery handoff has no source",
			)
		}
		return validatePRDevelopmentControllerSuspendedResumeRecoverySourceLink(
			resumed,
			suspension,
		)
	}
	source, err := loadPRDevelopmentControllerSuspensionStageSource(
		ctx,
		queryer,
		stagePRDevelopmentControllerSuspensionInput{
			ControllerID:        suspension.ControllerID,
			AttemptID:           suspension.AttemptID,
			SourceKind:          suspension.SourceKind,
			SourceRecoveryID:    suspension.SourceRecoveryID,
			SourceOperationID:   suspension.SourceOperationID,
			SourceFinalRevision: suspension.SourceFinalRevision,
			SourceFinalHash:     suspension.SourceFinalHash,
		},
	)
	if err != nil {
		return err
	}
	if source.finalizedAt == nil ||
		!suspension.CreatedAt.Equal(*source.finalizedAt) ||
		suspension.SourceOperationID != source.operationID ||
		suspension.SourceOperationKind != source.operationKind ||
		suspension.Mode != source.mode || suspension.AgentID != source.agentID ||
		suspension.WorkspaceID != source.workspaceID ||
		suspension.LineID != source.lineID ||
		suspension.SourceCloneURL != source.sourceCloneURL ||
		suspension.SourceRef != source.sourceRef ||
		suspension.SourceCommit != source.sourceCommit ||
		suspension.SourceTree != source.sourceTree ||
		suspension.LineVersion != source.lineVersion ||
		suspension.MutationEpoch != source.mutationEpoch ||
		suspension.TipCommit != source.tipCommit || suspension.Tree != source.tree ||
		suspension.SuspensionReservationDigest !=
			source.replacementReservationDigest ||
		suspension.MutationLeaseEpoch != source.newMutationLeaseEpoch ||
		suspension.MutationLeaseTokenDigest != source.newMutationLeaseTokenDigest {
		return errors.New(
			"stored controller suspension differs from its immutable source",
		)
	}
	if source.commitRequest != nil {
		request := suspension.SuspendRequest
		if request.CommitIntentID != source.commitRequest.EffectIntentID ||
			request.CommitExpectedParent != source.commitRequest.ExpectedParent ||
			request.CommitExpectedTree != source.commitRequest.ExpectedTree ||
			request.CommitCandidateDigest != source.commitRequest.CandidateDigest ||
			request.CommitMessage != source.commitRequest.CommitMessage ||
			!request.CommitAuthoredAt.Equal(source.commitRequest.AuthoredAt) {
			return errors.New(
				"stored Commit suspension differs from its immutable request",
			)
		}
	}
	return nil
}

func validatePRDevelopmentControllerResumedAuthority(
	controller PRDevelopmentController,
	latestFence *PRDevelopmentAttemptReviewFence,
	resumed *PRDevelopmentControllerSuspension,
) (bool, error) {
	if resumed == nil || resumed.Status != PRDevelopmentControllerSuspensionStatusResumed ||
		(latestFence != nil &&
			latestFence.MutationControllerRevision >= resumed.FinalResumeRevision) {
		return false, nil
	}
	if controller.CurrentAttemptID != resumed.ResumeAttemptID ||
		controller.LineVersion != resumed.LineVersion ||
		controller.MutationEpoch != resumed.MutationEpoch ||
		controller.TipCommit != resumed.TipCommit || controller.Tree != resumed.Tree {
		return false, errors.New(
			"stored controller continuation differs from its suspended resume",
		)
	}
	reservationMatches := controller.MutationReservationKey != "" &&
		prDevelopmentMutationReservationDigest(controller.MutationReservationKey) ==
			resumed.ResumeReservationDigest
	switch controller.Phase {
	case PRDevelopmentControllerMutation:
		if controller.Revision != resumed.FinalResumeRevision ||
			controller.LeaseKind != PRDevelopmentControllerMutationLease ||
			controller.LeaseOwner != resumed.ResumeClaimOwner ||
			controller.LeaseEpoch != resumed.NewMutationLeaseEpoch ||
			prDevelopmentLeaseTokenDigest(
				PRDevelopmentControllerMutationLease,
				controller.LeaseToken,
			) != resumed.NewMutationLeaseTokenDigest ||
			controller.LeaseUntil == nil || resumed.NewMutationLeaseUntil == nil ||
			controller.LeaseUntil.Before(*resumed.NewMutationLeaseUntil) ||
			!reservationMatches {
			return false, errors.New(
				"stored resumed mutation lost its restored authority",
			)
		}
		return true, nil
	case PRDevelopmentControllerRecoveryRequired:
		if controller.Revision != resumed.FinalResumeRevision+1 ||
			controller.LeaseEpoch != resumed.NewMutationLeaseEpoch ||
			!reservationMatches {
			return false, errors.New(
				"stored resumed recovery lost its restored authority",
			)
		}
		return true, nil
	default:
		return false, nil
	}
}

func validatePRDevelopmentControllerSuspensionFenceHighWater(
	controller PRDevelopmentController,
	fences []PRDevelopmentAttemptReviewFence,
	previousControllerRevision, previousLeaseEpoch int64,
) error {
	if len(fences) == 0 {
		if controller.FencesDigest != emptyPRDevelopmentReviewFencesDigest() ||
			controller.LineVersion != 0 {
			return errors.New(
				"stored suspended controller empty fence high-water state is invalid",
			)
		}
		return nil
	}
	latest := fences[len(fences)-1]
	if controller.FencesDigest != latest.FenceHash ||
		controller.LineVersion != latest.LineVersion ||
		controller.TipCommit != latest.TipCommit || controller.Tree != latest.Tree ||
		controller.Revision <= previousControllerRevision ||
		controller.LeaseEpoch < previousLeaseEpoch {
		return errors.New(
			"stored suspended controller fence high-water state is invalid",
		)
	}
	return nil
}
