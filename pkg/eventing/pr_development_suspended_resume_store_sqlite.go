//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	prDevelopmentSuspendedResumeIntentPrefix = "pdsri_"
	prDevelopmentSuspendedResumeClaimPrefix  = "pdsrc_"
)

func (s *Store) acquirePRDevelopmentControllerSuspendedResume(
	ctx context.Context,
	conn *sql.Conn,
	relation prDevelopmentControllerAttemptRelation,
	controller PRDevelopmentController,
	acquire PRDevelopmentControllerAcquire,
	orchestration PRDevelopmentRepairOrchestration,
	now, deadline time.Time,
) (PRDevelopmentControllerSuspendedResumeLease, bool, error) {
	if controller.Phase != PRDevelopmentControllerSuspended ||
		controller.OwnerSessionID != relation.Session.ID ||
		controller.AgentID != relation.Session.AgentID || controller.WorkspaceID == "" ||
		controller.WorkspaceID != relation.Session.WorkspaceID ||
		controller.SourceCloneURL != relation.Session.CloneURL ||
		controller.SourceRef != relation.Session.HeadRef ||
		controller.SourceCommit != relation.Session.HeadSHA ||
		controller.SourceTree == "" || controller.MutationReservationKey != "" ||
		controller.LeaseKind != "" || controller.LeaseToken != "" ||
		controller.LeaseUntil != nil {
		return PRDevelopmentControllerSuspendedResumeLease{}, false, fmt.Errorf(
			"%w: controller is not an exact bearer-free suspended line",
			ErrPRDevelopmentControllerConflict,
		)
	}
	suspension, found, err := loadActivePRDevelopmentControllerSuspension(
		ctx,
		conn,
		controller.ID,
	)
	if err != nil {
		return PRDevelopmentControllerSuspendedResumeLease{}, false, err
	}
	if !found || suspension.ControllerID != controller.ID ||
		suspension.OwnerSessionID != controller.OwnerSessionID ||
		suspension.AgentID != controller.AgentID ||
		suspension.WorkspaceID != controller.WorkspaceID ||
		suspension.LineID != controller.LineID ||
		suspension.SourceCloneURL != controller.SourceCloneURL ||
		suspension.SourceRef != controller.SourceRef ||
		suspension.SourceCommit != controller.SourceCommit ||
		suspension.SourceTree != controller.SourceTree ||
		suspension.LineVersion != controller.LineVersion ||
		suspension.MutationEpoch != controller.MutationEpoch ||
		suspension.TipCommit != controller.TipCommit || suspension.Tree != controller.Tree ||
		suspension.FinalSuspensionRevision < 1 || suspension.SuspensionFinalHash == "" {
		return PRDevelopmentControllerSuspendedResumeLease{}, false, fmt.Errorf(
			"%w: active suspension differs from its controller",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if err := requireNonRegressingPRDevelopmentControllerTime(
		now,
		maxPRDevelopmentControllerTime(controller.UpdatedAt, suspension.UpdatedAt),
	); err != nil {
		return PRDevelopmentControllerSuspendedResumeLease{}, false, err
	}

	if orchestration.AttemptID != relation.Attempt.ID ||
		(orchestration.ControllerID != "" && orchestration.ControllerID != controller.ID) ||
		orchestration.ClaimToken == "" ||
		orchestration.ClaimUntil == nil || !orchestration.ClaimUntil.After(now) {
		return PRDevelopmentControllerSuspendedResumeLease{}, false, fmt.Errorf(
			"%w: suspended resume has no exact live orchestration parent",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if deadline.After(*orchestration.ClaimUntil) {
		deadline = *orchestration.ClaimUntil
	}
	claimID := prDevelopmentSuspendedResumeClaimIdentity(
		suspension.ID,
		relation.Attempt.ID,
		orchestration.ClaimToken,
	)
	changed := false
	switch suspension.Status {
	case PRDevelopmentControllerSuspensionStatusSuspended:
		sameAttemptRecovery, sameAttemptErr :=
			isPRDevelopmentSuspendedResumeRecoveryContinuation(
				ctx,
				conn,
				suspension,
				relation.Attempt.ID,
			)
		if sameAttemptErr != nil {
			return PRDevelopmentControllerSuspendedResumeLease{}, false, sameAttemptErr
		}
		if controller.Revision != acquire.ExpectedRevision ||
			controller.Revision != suspension.FinalSuspensionRevision ||
			controller.CurrentAttemptID != suspension.AttemptID ||
			(relation.Attempt.ID == suspension.AttemptID && !sameAttemptRecovery) {
			return PRDevelopmentControllerSuspendedResumeLease{}, false, fmt.Errorf(
				"%w: suspended controller cannot prepare this attempt",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if capacityErr := requirePRDevelopmentSuspendedResumeRecoveryCapacity(
			controller,
			suspension,
		); capacityErr != nil {
			return PRDevelopmentControllerSuspendedResumeLease{}, false, capacityErr
		}
		reservation, reservationErr := newUniquePRDevelopmentMutationReservation(
			ctx,
			conn,
		)
		if reservationErr != nil {
			return PRDevelopmentControllerSuspendedResumeLease{}, false, reservationErr
		}
		intentID := prDevelopmentSuspendedResumeIdentity(
			prDevelopmentSuspendedResumeIntentPrefix,
			suspension.ID,
			relation.Attempt.ID,
		)
		request := PRDevelopmentControllerSuspendedResumeRequest{
			Repository:            suspension.SourceCloneURL,
			SourceRef:             suspension.SourceRef,
			SourceCommit:          suspension.SourceCommit,
			ReservationKey:        reservation,
			AgentID:               suspension.AgentID,
			WorkspaceID:           suspension.WorkspaceID,
			LineID:                suspension.LineID,
			IntentID:              intentID,
			ExpectedVersion:       suspension.LineVersion,
			ExpectedMutationEpoch: suspension.MutationEpoch,
			ExpectedTip:           suspension.TipCommit,
			ExpectedTree:          suspension.Tree,
			SuspensionHash:        suspension.SuspendResult.SuspensionHash,
			CandidateTree:         suspension.SuspendResult.CandidateTree,
			CandidateDigest:       suspension.SuspendResult.CandidateDigest,
			ChangedFileCount:      suspension.SuspendResult.ChangedFileCount,
		}
		requestJSON, requestHash, encodeErr :=
			encodePRDevelopmentControllerSuspendedResumeRequest(request)
		if encodeErr != nil {
			return PRDevelopmentControllerSuspendedResumeLease{}, false, encodeErr
		}
		claimToken, tokenErr := newLeaseToken(acquire.WorkerLabel)
		if tokenErr != nil {
			return PRDevelopmentControllerSuspendedResumeLease{}, false, tokenErr
		}
		suspension.Status = PRDevelopmentControllerSuspensionStatusResumeClaimed
		suspension.ResumeAttemptID = relation.Attempt.ID
		suspension.ResumeIntentID = intentID
		suspension.ResumeReservationKey = reservation
		suspension.ResumeReservationDigest = prDevelopmentMutationReservationDigest(reservation)
		suspension.ResumeRequest = request
		suspension.ResumeRequestJSON = requestJSON
		suspension.ResumeRequestHash = requestHash
		suspension.ResumePreparedAt = &now
		suspension.ResumeIntentHash = hashPRDevelopmentControllerSuspensionResumeIntent(suspension)
		suspension.ResumeClaimID = claimID
		suspension.ResumeClaimOwner = acquire.WorkerLabel
		suspension.ResumeClaimToken = claimToken
		suspension.ResumeClaimUntil = &deadline
		suspension.ResumeClaimEpoch = 1
		suspension.ResumeClaims = 1
		suspension.ResumeClaimedAt = &now
		suspension.UpdatedAt = now
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_controller_suspensions
			SET status = 'resume_claimed', resume_attempt_id = ?, resume_intent_id = ?,
				resume_reservation_key = ?, resume_reservation_digest = ?,
				resume_request_json = ?, resume_request_hash = ?, resume_intent_hash = ?,
				resume_prepared_at = ?, resume_claim_id = ?, resume_claim_owner = ?,
				resume_claim_token = ?, resume_claim_until = ?, resume_claim_epoch = 1,
				resume_claims = 1, resume_claimed_at = ?, updated_at = ?
			WHERE id = ? AND controller_id = ? AND status = 'suspended' AND
				final_suspension_revision = ? AND suspension_final_hash = ? AND
				resume_attempt_id IS NULL AND resume_reservation_key = ''`,
			suspension.ResumeAttemptID,
			suspension.ResumeIntentID,
			suspension.ResumeReservationKey,
			suspension.ResumeReservationDigest,
			suspension.ResumeRequestJSON,
			suspension.ResumeRequestHash,
			suspension.ResumeIntentHash,
			toDBTime(now),
			suspension.ResumeClaimID,
			suspension.ResumeClaimOwner,
			suspension.ResumeClaimToken,
			toDBTime(deadline),
			toDBTime(now),
			toDBTime(now),
			suspension.ID,
			controller.ID,
			controller.Revision,
			suspension.SuspensionFinalHash,
		)
		if updateErr != nil {
			return PRDevelopmentControllerSuspendedResumeLease{}, false, updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
			return PRDevelopmentControllerSuspendedResumeLease{}, false, rowErr
		}
		controllerResult, controllerErr := conn.ExecContext(ctx, `
			UPDATE pr_development_thread_controllers
			SET revision = revision + 1, current_attempt_id = ?, updated_at = ?
			WHERE id = ? AND revision = ? AND phase = 'suspended' AND
				current_attempt_id = ? AND lease_kind = '' AND lease_token = '' AND
				mutation_reservation_key = ''`,
			relation.Attempt.ID,
			toDBTime(now),
			controller.ID,
			controller.Revision,
			controller.CurrentAttemptID,
		)
		if controllerErr != nil {
			return PRDevelopmentControllerSuspendedResumeLease{}, false, controllerErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(controllerResult); rowErr != nil {
			return PRDevelopmentControllerSuspendedResumeLease{}, false, rowErr
		}
		changed = true

	case PRDevelopmentControllerSuspensionStatusResumePending,
		PRDevelopmentControllerSuspensionStatusResumeClaimed:
		if suspension.ResumeAttemptID != relation.Attempt.ID ||
			controller.CurrentAttemptID != relation.Attempt.ID ||
			controller.Revision != suspension.FinalSuspensionRevision+1 ||
			(acquire.ExpectedRevision != controller.Revision &&
				acquire.ExpectedRevision != suspension.FinalSuspensionRevision) ||
			suspension.ResumeIntentID != prDevelopmentSuspendedResumeIdentity(
				prDevelopmentSuspendedResumeIntentPrefix,
				suspension.ID,
				relation.Attempt.ID,
			) || suspension.ResumeReservationKey == "" {
			return PRDevelopmentControllerSuspendedResumeLease{}, false, fmt.Errorf(
				"%w: prepared suspended resume differs from this attempt",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if suspension.Status != PRDevelopmentControllerSuspensionStatusResumeClaimed ||
			suspension.ResumeClaimUntil == nil || !suspension.ResumeClaimUntil.After(now) {
			return PRDevelopmentControllerSuspendedResumeLease{}, false,
				ErrPRDevelopmentControllerRecoveryRequired
		}
		if suspension.ResumeClaimID != claimID ||
			suspension.ResumeClaimOwner != acquire.WorkerLabel ||
			suspension.ResumeClaimToken == "" {
			return PRDevelopmentControllerSuspendedResumeLease{}, false,
				ErrPRDevelopmentControllerActive
		}

	default:
		return PRDevelopmentControllerSuspendedResumeLease{}, false, fmt.Errorf(
			"%w: active suspension cannot be resumed",
			ErrPRDevelopmentControllerConflict,
		)
	}
	loadedController, found, err := loadPRDevelopmentControllerAggregateByID(
		ctx,
		conn,
		controller.ID,
	)
	if err != nil {
		return PRDevelopmentControllerSuspendedResumeLease{}, false, err
	}
	if !found {
		return PRDevelopmentControllerSuspendedResumeLease{}, false,
			errors.New("suspended-resume controller disappeared")
	}
	loadedSuspension, found, err := loadPRDevelopmentControllerSuspensionByID(
		ctx,
		conn,
		suspension.ID,
	)
	if err != nil {
		return PRDevelopmentControllerSuspendedResumeLease{}, false, err
	}
	if !found {
		return PRDevelopmentControllerSuspendedResumeLease{}, false,
			errors.New("suspended-resume intent disappeared")
	}
	return PRDevelopmentControllerSuspendedResumeLease{
		Controller: loadedController,
		Suspension: loadedSuspension,
		Reclaimed:  false,
	}, changed, nil
}

func isPRDevelopmentSuspendedResumeRecoveryContinuation(
	ctx context.Context,
	queryer rowsQueryer,
	suspension PRDevelopmentControllerSuspension,
	attemptID string,
) (bool, error) {
	if suspension.AttemptID != attemptID || suspension.SourceKind !=
		PRDevelopmentControllerSuspensionSourceSuspendedResumeRecovery {
		return false, nil
	}
	resumed, found, err := loadPRDevelopmentControllerSuspensionByID(
		ctx,
		queryer,
		suspension.SourceRecoveryID,
	)
	if err != nil {
		return false, err
	}
	if !found || resumed.ResumeAttemptID != attemptID {
		return false, fmt.Errorf(
			"%w: suspended-resume recovery continuation lost its exact source",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if err = validatePRDevelopmentControllerSuspendedResumeRecoverySourceLink(
		resumed,
		suspension,
	); err != nil {
		return false, fmt.Errorf(
			"%w: suspended-resume recovery continuation source changed: %v",
			ErrPRDevelopmentControllerConflict,
			err,
		)
	}
	return true, nil
}

// A normal resume can become crash-ambiguous after Git has installed its fresh
// bearer. Reserve enough durable history to append the recovery-only child,
// finalize its suspension, and still retain the ordinary controller mutation
// headroom before minting that bearer.
func requirePRDevelopmentSuspendedResumeRecoveryCapacity(
	controller PRDevelopmentController,
	suspension PRDevelopmentControllerSuspension,
) error {
	const recoveryRevisionReserve int64 = 3
	revisionReserve := prDevelopmentControllerMutationRevisionReserve
	if revisionReserve < recoveryRevisionReserve {
		revisionReserve = recoveryRevisionReserve
	}
	if suspension.Ordinal >= MaxPRDevelopmentControllerFences-1 ||
		controller.LeaseEpoch == int64(^uint64(0)>>1) ||
		controller.Revision > MaxPRDevelopmentControllerRevision-revisionReserve {
		return fmt.Errorf(
			"%w: suspended resume has no recovery history capacity",
			ErrPRDevelopmentControllerConflict,
		)
	}
	return nil
}

// RenewPRDevelopmentControllerSuspendedResume extends only one exact live
// resume claim; the precommitted reservation and request remain immutable.
func (s *Store) RenewPRDevelopmentControllerSuspendedResume(
	ctx context.Context,
	input PRDevelopmentControllerSuspendedResumeRenew,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	normalized, err := normalizePRDevelopmentControllerSuspendedResumeRenew(input)
	if err != nil {
		return err
	}
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		orchestration, found, loadErr := loadPRDevelopmentRepairOrchestration(
			ctx,
			conn,
			normalized.AttemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if orchestration.ControllerID != normalized.ControllerID ||
			orchestration.Phase != PRDevelopmentRepairOrchestrationBootstrap ||
			prDevelopmentSuspendedResumeClaimIdentity(
				normalized.SuspensionID,
				normalized.AttemptID,
				normalized.OrchestrationClaimToken,
			) != normalized.ClaimID {
			return fmt.Errorf(
				"%w: suspended resume renewal differs from its orchestration parent",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if claimErr := requireLivePRDevelopmentRepairOrchestrationClaim(
			orchestration,
			normalized.OrchestrationClaimToken,
			now,
			PRDevelopmentRepairOrchestrationBootstrap,
		); claimErr != nil {
			return claimErr
		}
		deadline, deadlineErr := prDevelopmentControllerDeadline(now, normalized.Lease)
		if deadlineErr != nil {
			return deadlineErr
		}
		if deadline.After(*orchestration.ClaimUntil) {
			deadline = *orchestration.ClaimUntil
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_controller_suspensions
			SET resume_claim_until = ?, updated_at = ?
			WHERE id = ? AND controller_id = ? AND resume_attempt_id = ? AND
				status = 'resume_claimed' AND resume_claim_id = ? AND
				resume_claim_token = ? AND resume_claim_epoch = ? AND
				resume_claim_until > ?`,
			toDBTime(deadline),
			toDBTime(now),
			normalized.SuspensionID,
			normalized.ControllerID,
			normalized.AttemptID,
			normalized.ClaimID,
			normalized.ClaimToken,
			normalized.ClaimEpoch,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		return requireOnePRDevelopmentControllerRow(result)
	})
	if err != nil {
		return fmt.Errorf(
			"renew pull request development suspended resume: %w",
			s.dbError(err),
		)
	}
	return nil
}

// FinalizePRDevelopmentControllerSuspendedResume installs the precommitted
// fresh bearer as the sole mutation owner only after exact Git proof.
func (s *Store) FinalizePRDevelopmentControllerSuspendedResume(
	ctx context.Context,
	input PRDevelopmentControllerSuspendedResumeFinalize,
) (PRDevelopmentControllerLease, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentControllerLease{}, false, err
	}
	normalized, err := normalizePRDevelopmentControllerSuspendedResumeFinalize(input)
	if err != nil {
		return PRDevelopmentControllerLease{}, false, err
	}
	resultJSON, resultHash, err := encodePRDevelopmentControllerSuspendedResumeResult(
		normalized.Result,
	)
	if err != nil {
		return PRDevelopmentControllerLease{}, false, err
	}
	var (
		lease            PRDevelopmentControllerLease
		changed          bool
		recoveryRequired bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		suspension, found, loadErr := loadPRDevelopmentControllerSuspensionByID(
			ctx,
			conn,
			normalized.SuspensionID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		controller, found, controllerErr := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			normalized.ControllerID,
		)
		if controllerErr != nil {
			return controllerErr
		}
		if !found {
			return sql.ErrNoRows
		}
		orchestration, found, orchestrationErr := loadPRDevelopmentRepairOrchestration(
			ctx,
			conn,
			normalized.AttemptID,
		)
		if orchestrationErr != nil {
			return orchestrationErr
		}
		if !found {
			return sql.ErrNoRows
		}
		if suspension.ControllerID != normalized.ControllerID ||
			suspension.ResumeAttemptID != normalized.AttemptID ||
			suspension.ResumeClaimID != normalized.ClaimID ||
			suspension.ResumeClaimEpoch != normalized.ClaimEpoch ||
			orchestration.ControllerID != controller.ID ||
			orchestration.Phase != PRDevelopmentRepairOrchestrationBootstrap ||
			prDevelopmentSuspendedResumeClaimIdentity(
				suspension.ID,
				normalized.AttemptID,
				normalized.OrchestrationClaimToken,
			) != normalized.ClaimID {
			return fmt.Errorf(
				"%w: suspended resume finalization differs from its durable intent",
				ErrPRDevelopmentControllerConflict,
			)
		}
		now, timeErr := s.currentTime()
		if timeErr != nil {
			return timeErr
		}
		if timeErr = requireNonRegressingPRDevelopmentControllerTime(
			now,
			maxPRDevelopmentControllerTime(controller.UpdatedAt, suspension.UpdatedAt),
		); timeErr != nil {
			return timeErr
		}
		if claimErr := requireLivePRDevelopmentRepairOrchestrationClaim(
			orchestration,
			normalized.OrchestrationClaimToken,
			now,
			PRDevelopmentRepairOrchestrationBootstrap,
		); claimErr != nil {
			return claimErr
		}
		claimDigest := prDevelopmentControllerSuspensionResumeClaimTokenDigest(
			normalized.ClaimToken,
		)
		if suspension.Status == PRDevelopmentControllerSuspensionStatusResumed {
			if suspension.ResumeResultHash != resultHash ||
				suspension.ResumeClaimTokenDigest != claimDigest ||
				suspension.ResumeFinalHash != hashPRDevelopmentControllerSuspensionResumeFinal(
					suspension,
				) || controller.Phase != PRDevelopmentControllerMutation ||
				controller.CurrentAttemptID != normalized.AttemptID ||
				controller.Revision != suspension.FinalResumeRevision ||
				controller.LeaseEpoch != suspension.NewMutationLeaseEpoch ||
				prDevelopmentLeaseTokenDigest(
					PRDevelopmentControllerMutationLease,
					controller.LeaseToken,
				) != suspension.NewMutationLeaseTokenDigest ||
				controller.MutationReservationKey == "" ||
				prDevelopmentMutationReservationDigest(
					controller.MutationReservationKey,
				) != suspension.ResumeReservationDigest ||
				controller.LeaseUntil == nil || suspension.NewMutationLeaseUntil == nil ||
				controller.LeaseUntil.Before(*suspension.NewMutationLeaseUntil) {
				return fmt.Errorf(
					"%w: finalized suspended resume is no longer current",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if !controller.LeaseUntil.After(now) {
				if expireErr := expirePRDevelopmentMutationLease(
					ctx,
					conn,
					controller,
					now,
				); expireErr != nil {
					return expireErr
				}
				recoveryRequired = true
				return nil
			}
			lease.Controller = controller
			return nil
		}
		if suspension.Status != PRDevelopmentControllerSuspensionStatusResumeClaimed ||
			suspension.ResumeClaimToken != normalized.ClaimToken ||
			suspension.ResumeClaimUntil == nil || !suspension.ResumeClaimUntil.After(now) {
			return ErrStaleLease
		}
		if controller.Phase != PRDevelopmentControllerSuspended ||
			controller.Revision != normalized.ExpectedRevision ||
			controller.CurrentAttemptID != normalized.AttemptID ||
			controller.MutationReservationKey != "" || controller.LeaseKind != "" ||
			controller.LeaseToken != "" || controller.LeaseUntil != nil ||
			suspension.ResumeReservationKey == "" ||
			!equalPRDevelopmentControllerSuspendedResumeResult(
				suspension,
				normalized.Result,
			) {
			return fmt.Errorf(
				"%w: suspended resume controller or result fence changed",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if controller.LeaseEpoch == int64(^uint64(0)>>1) ||
			controller.Revision >= MaxPRDevelopmentControllerRevision {
			return fmt.Errorf(
				"%w: suspended resume controller capacity exhausted",
				ErrPRDevelopmentControllerConflict,
			)
		}
		deadline, deadlineErr := prDevelopmentControllerDeadline(now, normalized.Lease)
		if deadlineErr != nil {
			return deadlineErr
		}
		mutationToken, tokenErr := newLeaseToken(suspension.ResumeClaimOwner)
		if tokenErr != nil {
			return tokenErr
		}
		mutationEpoch := controller.LeaseEpoch + 1
		finalRevision := controller.Revision + 1
		mutationTokenDigest := prDevelopmentLeaseTokenDigest(
			PRDevelopmentControllerMutationLease,
			mutationToken,
		)
		suspension.Status = PRDevelopmentControllerSuspensionStatusResumed
		suspension.ResumeReservationKey = ""
		suspension.ResumeClaimToken = ""
		suspension.ResumeClaimUntil = nil
		suspension.ResumeClaimTokenDigest = claimDigest
		suspension.ResumeResult = normalized.Result
		suspension.ResumeResult.AlreadyResumed = false
		suspension.ResumeResultJSON = resultJSON
		suspension.ResumeResultHash = resultHash
		suspension.NewMutationLeaseEpoch = mutationEpoch
		suspension.NewMutationLeaseTokenDigest = mutationTokenDigest
		suspension.NewMutationLeaseUntil = &deadline
		suspension.FinalResumeRevision = finalRevision
		suspension.ResumedAt = &now
		suspension.UpdatedAt = now
		suspension.ResumeFinalHash = hashPRDevelopmentControllerSuspensionResumeFinal(suspension)
		rowResult, rowErr := conn.ExecContext(ctx, `
			UPDATE pr_development_controller_suspensions
			SET status = 'resumed', resume_reservation_key = '',
				resume_claim_token = '', resume_claim_until = NULL,
				resume_claim_token_digest = ?, resume_result_json = ?,
				resume_result_hash = ?, rotation_hash = ?,
				new_mutation_lease_epoch = ?, new_mutation_lease_token_digest = ?,
				new_mutation_lease_until = ?, final_resume_revision = ?,
				resume_final_hash = ?, resumed_at = ?, updated_at = ?
			WHERE id = ? AND controller_id = ? AND resume_attempt_id = ? AND
				status = 'resume_claimed' AND resume_claim_id = ? AND
				resume_claim_token = ? AND resume_claim_epoch = ? AND
				resume_claim_until > ?`,
			suspension.ResumeClaimTokenDigest,
			suspension.ResumeResultJSON,
			suspension.ResumeResultHash,
			suspension.ResumeResult.RotationHash,
			suspension.NewMutationLeaseEpoch,
			suspension.NewMutationLeaseTokenDigest,
			toDBTime(deadline),
			suspension.FinalResumeRevision,
			suspension.ResumeFinalHash,
			toDBTime(now),
			toDBTime(now),
			suspension.ID,
			controller.ID,
			suspension.ResumeAttemptID,
			suspension.ResumeClaimID,
			normalized.ClaimToken,
			suspension.ResumeClaimEpoch,
			toDBTime(now),
		)
		if rowErr != nil {
			return rowErr
		}
		if rowErr = requireOnePRDevelopmentControllerRow(rowResult); rowErr != nil {
			return rowErr
		}
		controllerResult, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_thread_controllers
			SET revision = revision + 1, phase = 'mutation', lease_kind = 'mutation',
				lease_owner = ?, lease_token = ?, lease_until = ?,
				lease_epoch = lease_epoch + 1, claims = claims + 1,
				mutation_reservation_key = ?, line_version = ?, mutation_epoch = ?,
				tip_commit = ?, tree = ?, updated_at = ?
			WHERE id = ? AND revision = ? AND phase = 'suspended' AND
				current_attempt_id = ? AND lease_kind = '' AND lease_token = '' AND
				mutation_reservation_key = ''`,
			suspension.ResumeClaimOwner,
			mutationToken,
			toDBTime(deadline),
			suspension.ResumeRequest.ReservationKey,
			normalized.Result.Version,
			normalized.Result.MutationEpoch,
			normalized.Result.Tip,
			normalized.Result.Tree,
			toDBTime(now),
			controller.ID,
			controller.Revision,
			controller.CurrentAttemptID,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(controllerResult); rowErr != nil {
			return rowErr
		}
		loaded, found, reloadErr := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			controller.ID,
		)
		if reloadErr != nil {
			return reloadErr
		}
		if !found {
			return errors.New("resumed controller disappeared")
		}
		lease.Controller = loaded
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentControllerLease{}, false, fmt.Errorf(
			"finalize pull request development suspended resume: %w",
			s.dbError(err),
		)
	}
	if recoveryRequired {
		return PRDevelopmentControllerLease{}, false, fmt.Errorf(
			"finalize pull request development suspended resume: %w",
			ErrPRDevelopmentControllerRecoveryRequired,
		)
	}
	return lease, changed, nil
}

func normalizePRDevelopmentControllerSuspendedResumeRenew(
	input PRDevelopmentControllerSuspendedResumeRenew,
) (PRDevelopmentControllerSuspendedResumeRenew, error) {
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.SuspensionID = strings.TrimSpace(input.SuspensionID)
	var err error
	for _, field := range []struct {
		name    string
		value   *string
		maximum int
	}{
		{"suspended resume orchestration claim token", &input.OrchestrationClaimToken, maxPRDevelopmentRepairLeaseBytes},
		{"suspended resume claim ID", &input.ClaimID, MaxPRDevelopmentControllerIdentityBytes},
		{"suspended resume claim token", &input.ClaimToken, prDevelopmentControllerLeaseTokenBytes},
	} {
		*field.value, err = normalizePRDevelopmentControllerIdentity(
			field.name,
			*field.value,
			field.maximum,
			true,
		)
		if err != nil {
			return PRDevelopmentControllerSuspendedResumeRenew{}, err
		}
	}
	if !validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(input.SuspensionID, prDevelopmentSuspensionIDPrefix) ||
		input.ClaimEpoch < 1 || input.Lease <= 0 {
		return PRDevelopmentControllerSuspendedResumeRenew{}, fmt.Errorf(
			"%w: valid suspended resume renewal proof is required",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func normalizePRDevelopmentControllerSuspendedResumeFinalize(
	input PRDevelopmentControllerSuspendedResumeFinalize,
) (PRDevelopmentControllerSuspendedResumeFinalize, error) {
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.SuspensionID = strings.TrimSpace(input.SuspensionID)
	var err error
	for _, field := range []struct {
		name    string
		value   *string
		maximum int
	}{
		{"suspended resume orchestration claim token", &input.OrchestrationClaimToken, maxPRDevelopmentRepairLeaseBytes},
		{"suspended resume claim ID", &input.ClaimID, MaxPRDevelopmentControllerIdentityBytes},
		{"suspended resume claim token", &input.ClaimToken, prDevelopmentControllerLeaseTokenBytes},
	} {
		*field.value, err = normalizePRDevelopmentControllerIdentity(
			field.name,
			*field.value,
			field.maximum,
			true,
		)
		if err != nil {
			return PRDevelopmentControllerSuspendedResumeFinalize{}, err
		}
	}
	input.Result.AlreadyResumed = false
	input.Result.WorkspaceID = strings.TrimSpace(input.Result.WorkspaceID)
	input.Result.Tip = strings.TrimSpace(input.Result.Tip)
	input.Result.Tree = strings.TrimSpace(input.Result.Tree)
	input.Result.CandidateTree = strings.TrimSpace(input.Result.CandidateTree)
	input.Result.CandidateDigest = strings.TrimSpace(input.Result.CandidateDigest)
	input.Result.SuspensionHash = strings.TrimSpace(input.Result.SuspensionHash)
	input.Result.RotationHash = strings.TrimSpace(input.Result.RotationHash)
	if !validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(input.SuspensionID, prDevelopmentSuspensionIDPrefix) ||
		input.ExpectedRevision < 1 ||
		input.ExpectedRevision >= MaxPRDevelopmentControllerRevision ||
		input.ClaimEpoch < 1 || input.Lease <= 0 || input.Result.WorkspaceID == "" ||
		input.Result.Version < 0 || input.Result.Version > MaxPRDevelopmentControllerFences ||
		input.Result.MutationEpoch < input.Result.Version ||
		input.Result.MutationEpoch > input.Result.Version+1 ||
		!validSameWidthPRDevelopmentOIDs(
			input.Result.Tip,
			input.Result.Tree,
			input.Result.CandidateTree,
		) || !validPRDevelopmentHex(input.Result.CandidateDigest, sha256.Size*2) ||
		input.Result.ChangedFileCount < 0 ||
		input.Result.ChangedFileCount > maxPRDevelopmentSuspensionChangedFiles ||
		!validPRDevelopmentHex(input.Result.SuspensionHash, sha256.Size*2) ||
		!validPRDevelopmentHex(input.Result.RotationHash, sha256.Size*2) {
		return PRDevelopmentControllerSuspendedResumeFinalize{}, fmt.Errorf(
			"%w: valid suspended resume finalization proof is required",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func equalPRDevelopmentControllerSuspendedResumeResult(
	suspension PRDevelopmentControllerSuspension,
	result PRDevelopmentControllerSuspendedResumeResult,
) bool {
	request := suspension.ResumeRequest
	return result.WorkspaceID == request.WorkspaceID &&
		result.Version == request.ExpectedVersion &&
		result.MutationEpoch == request.ExpectedMutationEpoch &&
		result.Tip == request.ExpectedTip && result.Tree == request.ExpectedTree &&
		result.CandidateTree == request.CandidateTree &&
		result.CandidateDigest == request.CandidateDigest &&
		result.ChangedFileCount == request.ChangedFileCount &&
		result.SuspensionHash == request.SuspensionHash
}

func prDevelopmentSuspendedResumeIdentity(prefix, suspensionID, attemptID string) string {
	digest := sha256.New()
	writePRDevelopmentControllerHashField(
		digest,
		"picoclaw-pr-development-controller-suspended-resume-identity-v1\x00",
	)
	writePRDevelopmentControllerHashField(digest, prefix)
	writePRDevelopmentControllerHashField(digest, suspensionID)
	writePRDevelopmentControllerHashField(digest, attemptID)
	return prefix + hex.EncodeToString(digest.Sum(nil)[:16])
}

func prDevelopmentSuspendedResumeClaimIdentity(
	suspensionID, attemptID, orchestrationClaimToken string,
) string {
	digest := sha256.New()
	writePRDevelopmentControllerHashField(
		digest,
		"picoclaw-pr-development-controller-suspended-resume-claim-identity-v1\x00",
	)
	writePRDevelopmentControllerHashField(digest, suspensionID)
	writePRDevelopmentControllerHashField(digest, attemptID)
	writePRDevelopmentControllerHashField(
		digest,
		hashPRDevelopmentRepairOrchestrationClaimToken(orchestrationClaimToken),
	)
	return prDevelopmentSuspendedResumeClaimPrefix +
		hex.EncodeToString(digest.Sum(nil)[:16])
}
