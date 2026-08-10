//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func requireNoActivePRDevelopmentControllerOperation(
	ctx context.Context,
	queryer rowsQueryer,
	controllerID string,
) error {
	_, found, err := loadActivePRDevelopmentControllerOperation(
		ctx,
		queryer,
		controllerID,
	)
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf(
			"%w: controller has a prepared operation",
			ErrPRDevelopmentControllerActive,
		)
	}
	return nil
}

// stagePRDevelopmentControllerOperationRecoveryForExpiry transfers an
// unresolved Git effect into its own recovery lifecycle. The caller changes
// the controller to recovery_required in the same immediate transaction.
func stagePRDevelopmentControllerOperationRecoveryForExpiry(
	ctx context.Context,
	conn *sql.Conn,
	controller PRDevelopmentController,
	operation PRDevelopmentControllerOperation,
	now time.Time,
) error {
	if operation.Status != PRDevelopmentControllerOperationPending ||
		operation.ControllerID != controller.ID ||
		operation.AttemptID != controller.CurrentAttemptID ||
		operation.PreparedControllerRevision != controller.Revision ||
		operation.AgentID != controller.AgentID ||
		operation.LineID != controller.LineID ||
		operation.MutationReservationDigest != prDevelopmentMutationReservationDigest(
			controller.MutationReservationKey,
		) ||
		operation.MutationLeaseEpoch != controller.LeaseEpoch ||
		operation.MutationLeaseTokenDigest != prDevelopmentLeaseTokenDigest(
			PRDevelopmentControllerMutationLease,
			controller.LeaseToken,
		) || controller.Phase != PRDevelopmentControllerMutation ||
		controller.LeaseKind != PRDevelopmentControllerMutationLease ||
		controller.LeaseUntil == nil || controller.LeaseUntil.After(now) {
		return fmt.Errorf(
			"%w: prepared operation does not own the expired mutation lease",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if _, found, err := loadActivePRDevelopmentRecoveryIntent(
		ctx,
		conn,
		controller.ID,
	); err != nil {
		return err
	} else if found {
		return fmt.Errorf(
			"%w: operation recovery conflicts with a legacy recovery intent",
			ErrPRDevelopmentControllerConflict,
		)
	}

	staged := operation
	staged.Status = PRDevelopmentControllerOperationRecoveryPending
	staged.RecoveryRevision = controller.Revision + 1
	staged.ExpiredControllerRevision = controller.Revision
	staged.ExpiredLeaseEpoch = controller.LeaseEpoch
	staged.ExpiredLeaseTokenDigest = operation.MutationLeaseTokenDigest
	leaseUntil := *controller.LeaseUntil
	staged.RecoveryLeaseUntil = &leaseUntil
	stagedAt := now
	staged.RecoveryStagedAt = &stagedAt
	if operation.Kind != PRDevelopmentControllerOperationPark {
		recoveryID, err := newPrefixedID(prDevelopmentRecoveryIntentIDPrefix)
		if err != nil {
			return err
		}
		replacement, err := newUniquePRDevelopmentOperationReplacement(ctx, conn)
		if err != nil {
			return err
		}
		staged.RecoveryID = recoveryID
		staged.ReplacementReservationKey = replacement
		staged.ReplacementReservationDigest = prDevelopmentMutationReservationDigest(
			replacement,
		)
	}
	staged.RecoveryHash = hashPRDevelopmentOperationRecovery(staged)
	result, err := conn.ExecContext(ctx, `
		UPDATE pr_development_controller_operation_intents
		SET status = 'recovery_pending', recovery_id = ?,
			replacement_reservation_key = ?, replacement_reservation_digest = ?,
			recovery_revision = ?, expired_controller_revision = ?,
			expired_lease_epoch = ?, expired_lease_token_digest = ?,
			recovery_lease_until = ?, recovery_staged_at = ?, recovery_hash = ?,
			updated_at = ?
		WHERE id = ? AND controller_id = ? AND attempt_id = ? AND
			status = 'pending' AND prepared_controller_revision = ? AND
			mutation_lease_epoch = ? AND mutation_lease_token_digest = ?`,
		staged.RecoveryID,
		staged.ReplacementReservationKey,
		staged.ReplacementReservationDigest,
		staged.RecoveryRevision,
		staged.ExpiredControllerRevision,
		staged.ExpiredLeaseEpoch,
		staged.ExpiredLeaseTokenDigest,
		toDBTime(leaseUntil),
		toDBTime(now),
		staged.RecoveryHash,
		toDBTime(now),
		operation.ID,
		controller.ID,
		controller.CurrentAttemptID,
		controller.Revision,
		controller.LeaseEpoch,
		operation.MutationLeaseTokenDigest,
	)
	if err != nil {
		return err
	}
	return requireOnePRDevelopmentControllerRow(result)
}

func newUniquePRDevelopmentOperationReplacement(
	ctx context.Context,
	queryer rowsQueryer,
) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		reservation, err := newPrefixedID(prDevelopmentControllerKeyPrefix)
		if err != nil {
			return "", err
		}
		digest := prDevelopmentMutationReservationDigest(reservation)
		var collisions int
		if err := queryer.QueryRowContext(ctx, `
			SELECT
				(SELECT COUNT(*) FROM pr_development_thread_controllers
				 WHERE mutation_reservation_key = ?) +
				(SELECT COUNT(*) FROM pr_development_attempt_review_fences
				 WHERE mutation_reservation_digest = ?) +
				(SELECT COUNT(*) FROM pr_development_controller_recovery_intents
				 WHERE previous_reservation_digest = ? OR replacement_reservation_digest = ?) +
				(SELECT COUNT(*) FROM pr_development_controller_operation_intents
				 WHERE mutation_reservation_digest = ? OR replacement_reservation_digest = ?)`,
			reservation,
			digest,
			digest,
			digest,
			digest,
			digest,
		).Scan(&collisions); err != nil {
			return "", err
		}
		if collisions == 0 {
			return reservation, nil
		}
	}
	return "", errors.New(
		"generate unique pull request development operation replacement reservation",
	)
}

// ClaimPRDevelopmentControllerOperationRecovery leases only exact
// reconciliation of the already-prepared operation. It grants no mutation
// authority and can be reclaimed after expiry because every permitted Git
// effect is exactly replayable.
func (s *Store) ClaimPRDevelopmentControllerOperationRecovery(
	ctx context.Context,
	input PRDevelopmentControllerOperationRecoveryClaim,
) (PRDevelopmentControllerOperationRecoveryLease, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentControllerOperationRecoveryLease{}, false, err
	}
	normalized, err := normalizePRDevelopmentControllerOperationRecoveryClaim(input)
	if err != nil {
		return PRDevelopmentControllerOperationRecoveryLease{}, false, err
	}
	var (
		lease   PRDevelopmentControllerOperationRecoveryLease
		changed bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		relation, relationErr := loadPRDevelopmentControllerAttemptRelation(
			ctx,
			conn,
			normalized.CaseID,
			normalized.AttemptID,
		)
		if relationErr != nil {
			return relationErr
		}
		controller, found, controllerLoadErr := loadPRDevelopmentControllerAggregate(
			ctx,
			conn,
			relation.Thread.ID,
		)
		if controllerLoadErr != nil {
			return controllerLoadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		operation, found, operationLoadErr := loadPRDevelopmentControllerOperationByID(
			ctx,
			conn,
			normalized.OperationID,
		)
		if operationLoadErr != nil {
			return operationLoadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		if controller.OwnerSessionID != relation.Session.ID ||
			controller.CurrentAttemptID != normalized.AttemptID ||
			controller.Phase != PRDevelopmentControllerRecoveryRequired ||
			controller.Revision != normalized.ExpectedRevision ||
			operation.ControllerID != controller.ID ||
			operation.AttemptID != normalized.AttemptID ||
			operation.RecoveryRevision != controller.Revision ||
			operation.ExpiredLeaseEpoch != controller.LeaseEpoch ||
			operation.MutationReservationDigest != prDevelopmentMutationReservationDigest(
				controller.MutationReservationKey,
			) {
			return fmt.Errorf(
				"%w: operation recovery is not controller-current",
				ErrPRDevelopmentControllerConflict,
			)
		}
		now, timeErr := s.currentTime()
		if timeErr != nil {
			return timeErr
		}
		if timeOrderErr := requireNonRegressingPRDevelopmentControllerTime(
			now,
			maxPRDevelopmentControllerTime(
				controller.UpdatedAt,
				relation.Session.UpdatedAt,
				relation.Attempt.UpdatedAt,
				operation.UpdatedAt,
			),
		); timeOrderErr != nil {
			return timeOrderErr
		}
		deadline, deadlineErr := prDevelopmentControllerDeadline(now, normalized.Lease)
		if deadlineErr != nil {
			return deadlineErr
		}
		if operation.Status == PRDevelopmentControllerOperationRecoveryClaimed &&
			operation.ClaimUntil != nil && operation.ClaimUntil.After(now) {
			if operation.ClaimID != normalized.ClaimID ||
				operation.ClaimOwner != normalized.WorkerLabel {
				return ErrPRDevelopmentControllerActive
			}
			lease = PRDevelopmentControllerOperationRecoveryLease{
				Controller: controller,
				Operation:  operation,
			}
			return nil
		}
		if operation.Status == PRDevelopmentControllerOperationRecoveryClaimed &&
			operation.ClaimID == normalized.ClaimID &&
			operation.ClaimOwner != normalized.WorkerLabel {
			return fmt.Errorf(
				"%w: operation recovery claim ID belongs to another worker",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if operation.Status != PRDevelopmentControllerOperationRecoveryPending &&
			operation.Status != PRDevelopmentControllerOperationRecoveryClaimed {
			return fmt.Errorf(
				"%w: operation recovery is not claimable",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if operation.ClaimEpoch == int64(^uint64(0)>>1) {
			return fmt.Errorf(
				"%w: operation recovery claim epoch capacity exhausted",
				ErrPRDevelopmentControllerConflict,
			)
		}
		var duplicate int
		if duplicateQueryErr := conn.QueryRowContext(ctx, `
			SELECT
				(SELECT COUNT(*) FROM pr_development_controller_operation_intents
				 WHERE claim_id = ? AND id <> ?) +
				(SELECT COUNT(*) FROM pr_development_controller_recovery_intents
				 WHERE claim_id = ?)`,
			normalized.ClaimID,
			operation.ID,
			normalized.ClaimID,
		).Scan(&duplicate); duplicateQueryErr != nil {
			return duplicateQueryErr
		}
		if duplicate != 0 {
			return fmt.Errorf(
				"%w: operation recovery claim ID is already bound",
				ErrPRDevelopmentControllerConflict,
			)
		}
		token, tokenErr := newLeaseToken(normalized.WorkerLabel)
		if tokenErr != nil {
			return tokenErr
		}
		reclaimed := operation.Status == PRDevelopmentControllerOperationRecoveryClaimed
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_controller_operation_intents
			SET status = 'recovery_claimed', claim_id = ?, claim_owner = ?,
				claim_token = ?, claim_until = ?, claim_epoch = claim_epoch + 1,
				claims = claims + 1, claimed_at = COALESCE(claimed_at, ?),
				updated_at = ?
			WHERE id = ? AND controller_id = ? AND attempt_id = ? AND
				recovery_revision = ? AND
				(status = 'recovery_pending' OR
				 (status = 'recovery_claimed' AND claim_until <= ?))`,
			normalized.ClaimID,
			normalized.WorkerLabel,
			token,
			toDBTime(deadline),
			toDBTime(now),
			toDBTime(now),
			operation.ID,
			controller.ID,
			normalized.AttemptID,
			controller.Revision,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		if rowCountErr := requireOnePRDevelopmentControllerRow(result); rowCountErr != nil {
			return rowCountErr
		}
		claimed, found, claimedLoadErr := loadPRDevelopmentControllerOperationByID(
			ctx,
			conn,
			operation.ID,
		)
		if claimedLoadErr != nil {
			return claimedLoadErr
		}
		if !found {
			return errors.New("claimed operation recovery disappeared")
		}
		lease = PRDevelopmentControllerOperationRecoveryLease{
			Controller: controller,
			Operation:  claimed,
			Reclaimed:  reclaimed,
		}
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentControllerOperationRecoveryLease{}, false, fmt.Errorf(
			"claim pull request development controller operation recovery: %w",
			s.dbError(err),
		)
	}
	return lease, changed, nil
}

// RenewPRDevelopmentControllerOperationRecovery extends only the exact live
// reconciliation claim. It does not restore or extend mutation authority.
func (s *Store) RenewPRDevelopmentControllerOperationRecovery(
	ctx context.Context,
	input PRDevelopmentControllerOperationRecoveryRenew,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	normalized, err := normalizePRDevelopmentControllerOperationRecoveryRenew(input)
	if err != nil {
		return err
	}
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		operation, found, operationLoadErr := loadPRDevelopmentControllerOperationByID(
			ctx,
			conn,
			normalized.OperationID,
		)
		if operationLoadErr != nil {
			return operationLoadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		controller, found, controllerLoadErr := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			normalized.ControllerID,
		)
		if controllerLoadErr != nil {
			return controllerLoadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		if operation.ControllerID != controller.ID ||
			operation.AttemptID != normalized.AttemptID ||
			operation.RecoveryID != normalized.RecoveryID ||
			operation.RecoveryRevision != controller.Revision ||
			controller.Phase != PRDevelopmentControllerRecoveryRequired ||
			controller.CurrentAttemptID != normalized.AttemptID {
			return fmt.Errorf(
				"%w: operation recovery claim is no longer controller-current",
				ErrPRDevelopmentControllerConflict,
			)
		}
		now, timeErr := s.currentTime()
		if timeErr != nil {
			return timeErr
		}
		if timeOrderErr := requireNonRegressingPRDevelopmentControllerTime(
			now,
			maxPRDevelopmentControllerTime(controller.UpdatedAt, operation.UpdatedAt),
		); timeOrderErr != nil {
			return timeOrderErr
		}
		deadline, deadlineErr := prDevelopmentControllerDeadline(now, normalized.Lease)
		if deadlineErr != nil {
			return deadlineErr
		}
		if operation.ClaimUntil != nil && operation.ClaimUntil.After(deadline) {
			deadline = *operation.ClaimUntil
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_controller_operation_intents
			SET claim_until = ?, updated_at = ?
			WHERE id = ? AND controller_id = ? AND attempt_id = ? AND
				recovery_id = ? AND status = 'recovery_claimed' AND
				claim_id = ? AND claim_token = ? AND claim_epoch = ? AND
				claim_until > ?`,
			toDBTime(deadline),
			toDBTime(now),
			normalized.OperationID,
			normalized.ControllerID,
			normalized.AttemptID,
			normalized.RecoveryID,
			normalized.ClaimID,
			normalized.ClaimToken,
			normalized.ClaimEpoch,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		if rowCountErr := requireOnePRDevelopmentControllerRow(result); rowCountErr != nil {
			return fmt.Errorf("%w: %v", ErrStaleLease, rowCountErr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf(
			"renew pull request development controller operation recovery: %w",
			s.dbError(err),
		)
	}
	return nil
}

// FinalizePRDevelopmentControllerOperationRecovery records the exact
// reconciled Git result. Adopt, Resume, and Commit prove their intermediate
// fresh mutation authority and immediately transfer it to durable suspension
// in the same transaction. Park retires the old bearer and atomically enters
// reservation-free review without issuing a replacement.
func (s *Store) FinalizePRDevelopmentControllerOperationRecovery(
	ctx context.Context,
	input PRDevelopmentControllerOperationRecoveryFinalize,
) (PRDevelopmentControllerOperationTransition, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentControllerOperationTransition{}, false, err
	}
	normalized, err := normalizePRDevelopmentControllerOperationRecoveryFinalize(input)
	if err != nil {
		return PRDevelopmentControllerOperationTransition{}, false, err
	}
	var (
		transition PRDevelopmentControllerOperationTransition
		changed    bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		operation, found, operationLoadErr := loadPRDevelopmentControllerOperationByID(
			ctx,
			conn,
			normalized.OperationID,
		)
		if operationLoadErr != nil {
			return operationLoadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		if operation.ControllerID != normalized.ControllerID ||
			operation.AttemptID != normalized.AttemptID ||
			operation.RecoveryID != normalized.RecoveryID ||
			operation.RecoveryRevision != normalized.ExpectedRevision ||
			operation.ClaimID != normalized.ClaimID ||
			operation.ClaimEpoch != normalized.ClaimEpoch {
			return fmt.Errorf(
				"%w: operation recovery finalization differs from its durable intent",
				ErrPRDevelopmentControllerConflict,
			)
		}
		result, resultNormalizeErr := normalizePRDevelopmentControllerOperationResultForRecovery(
			operation.Kind,
			operation,
			normalized.Result,
			true,
		)
		if resultNormalizeErr != nil {
			return resultNormalizeErr
		}
		resultJSON, resultHash, resultEncodeErr := encodePRDevelopmentOperationResult(result)
		if resultEncodeErr != nil {
			return resultEncodeErr
		}
		providedJSON, _, providedEncodeErr := encodePRDevelopmentOperationResult(normalized.Result)
		if providedEncodeErr != nil {
			return providedEncodeErr
		}
		if !bytesEqualPRDevelopmentOperation(resultJSON, providedJSON) {
			return fmt.Errorf(
				"%w: operation recovery result differs from its exact effect fence",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if !equalPRDevelopmentOperationRecoveryRotation(
			operation,
			result,
			normalized.Rotation,
		) {
			return fmt.Errorf(
				"%w: reservation rotation differs from the recovered operation",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if operation.Kind == PRDevelopmentControllerOperationPark {
			if normalized.RecoveryID != "" || normalized.Lease != 0 {
				return fmt.Errorf(
					"%w: Park recovery cannot receive replacement mutation authority",
					ErrInvalidPRDevelopmentController,
				)
			}
		} else if !validPrefixedHexID(
			normalized.RecoveryID,
			prDevelopmentRecoveryIntentIDPrefix,
		) || normalized.Lease <= 0 {
			return fmt.Errorf(
				"%w: recovered mutation operation requires its replacement and lease",
				ErrInvalidPRDevelopmentController,
			)
		}
		rotationHash := ""
		if operation.Kind != PRDevelopmentControllerOperationPark {
			rotationHash = hashPRDevelopmentRecoveryRotationResult(normalized.Rotation)
		}
		claimDigest := prDevelopmentRecoveryClaimTokenDigest(normalized.ClaimToken)
		controller, found, controllerLoadErr := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			normalized.ControllerID,
		)
		if controllerLoadErr != nil {
			return controllerLoadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		operations, operationChainErr := loadPRDevelopmentControllerOperations(ctx, conn, controller.ID)
		if operationChainErr != nil {
			return operationChainErr
		}
		if len(operations) == 0 || operations[len(operations)-1].ID != operation.ID {
			return fmt.Errorf(
				"%w: recovered operation is no longer the operation-chain tail",
				ErrPRDevelopmentControllerConflict,
			)
		}
		now, timeErr := s.currentTime()
		if timeErr != nil {
			return timeErr
		}
		if timeOrderErr := requireNonRegressingPRDevelopmentControllerTime(
			now,
			maxPRDevelopmentControllerTime(controller.UpdatedAt, operation.UpdatedAt),
		); timeOrderErr != nil {
			return timeOrderErr
		}
		if operation.Status == PRDevelopmentControllerOperationFinalized {
			if operation.ResultHash != resultHash ||
				!bytesEqualPRDevelopmentOperation(operation.ResultJSON, resultJSON) ||
				operation.RotationResultHash != rotationHash ||
				operation.RecoveryClaimTokenDigest != claimDigest {
				return fmt.Errorf(
					"%w: finalized operation recovery is bound to different proof",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if operation.Kind != PRDevelopmentControllerOperationPark {
				if _, stageErr := stagePRDevelopmentControllerSuspension(
					ctx,
					conn,
					stagePRDevelopmentControllerSuspensionInput{
						ControllerID:        operation.ControllerID,
						AttemptID:           operation.AttemptID,
						SourceKind:          PRDevelopmentControllerSuspensionSourceOperationRecovery,
						SourceRecoveryID:    operation.RecoveryID,
						SourceOperationID:   operation.ID,
						SourceFinalRevision: operation.FinalControllerRevision,
						SourceFinalHash:     operation.FinalHash,
					},
					now,
				); stageErr != nil {
					return stageErr
				}
				reloaded, reloadedFound, reloadErr := loadPRDevelopmentControllerAggregateByID(
					ctx,
					conn,
					controller.ID,
				)
				if reloadErr != nil {
					return reloadErr
				}
				if !reloadedFound {
					return errors.New("suspended operation controller disappeared")
				}
				transition = PRDevelopmentControllerOperationTransition{
					Controller: reloaded,
					Operation:  operation,
				}
				return nil
			}
			fence, replayErr := requireImmediatePRDevelopmentParkRecoveryReplay(
				ctx,
				conn,
				controller,
				operation,
			)
			if replayErr != nil {
				return replayErr
			}
			transition = PRDevelopmentControllerOperationTransition{
				Controller: controller,
				Operation:  operation,
				Fence:      fence,
			}
			return nil
		}
		if operation.Status != PRDevelopmentControllerOperationRecoveryClaimed ||
			operation.ClaimToken != normalized.ClaimToken ||
			operation.ClaimUntil == nil || !operation.ClaimUntil.After(now) {
			return ErrStaleLease
		}
		if controller.Phase != PRDevelopmentControllerRecoveryRequired ||
			controller.Revision != operation.RecoveryRevision ||
			controller.CurrentAttemptID != operation.AttemptID ||
			controller.LeaseKind != "" || controller.LeaseToken != "" ||
			controller.MutationReservationKey == "" ||
			prDevelopmentMutationReservationDigest(
				controller.MutationReservationKey,
			) != operation.MutationReservationDigest ||
			controller.LeaseEpoch != operation.ExpiredLeaseEpoch {
			return fmt.Errorf(
				"%w: operation recovery controller high-water changed",
				ErrPRDevelopmentControllerConflict,
			)
		}

		var (
			fence               *PRDevelopmentAttemptReviewFence
			mutationToken       string
			mutationTokenDigest string
			mutationLeaseEpoch  int64
			mutationLeaseUntil  *time.Time
			finalRevision       = controller.Revision
			finalPhase          = PRDevelopmentControllerReviewPending
		)
		if operation.Kind == PRDevelopmentControllerOperationPark {
			parked, parkErr := finalizePRDevelopmentParkOperation(
				ctx,
				conn,
				controller,
				operation,
				result,
				now,
				operation.PreparedControllerRevision,
			)
			if parkErr != nil {
				return parkErr
			}
			fence = &parked
		} else {
			if controller.LeaseEpoch == int64(^uint64(0)>>1) {
				return fmt.Errorf(
					"%w: controller lease epoch capacity exhausted",
					ErrPRDevelopmentControllerConflict,
				)
			}
			deadline, deadlineErr := prDevelopmentControllerDeadline(now, normalized.Lease)
			if deadlineErr != nil {
				return deadlineErr
			}
			mutationLeaseUntil = &deadline
			issuedMutationToken, tokenErr := newLeaseToken(operation.ClaimOwner)
			if tokenErr != nil {
				return tokenErr
			}
			mutationToken = issuedMutationToken
			mutationTokenDigest = prDevelopmentLeaseTokenDigest(
				PRDevelopmentControllerMutationLease,
				mutationToken,
			)
			mutationLeaseEpoch = controller.LeaseEpoch + 1
			if operation.Kind == PRDevelopmentControllerOperationAdopt ||
				operation.Kind == PRDevelopmentControllerOperationResume {
				finalRevision += 2
			} else {
				finalRevision++
			}
			finalPhase = PRDevelopmentControllerMutation
			if finalRevision > MaxPRDevelopmentControllerRevision-2 {
				return fmt.Errorf(
					"%w: recovered operation lacks Park and review revision headroom",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if auditInsertErr := insertFinalizedPRDevelopmentRecoveryAuditForOperation(
				ctx,
				conn,
				operation,
				normalized.Rotation,
				rotationHash,
				claimDigest,
				mutationTokenDigest,
				mutationLeaseEpoch,
				deadline,
				now,
			); auditInsertErr != nil {
				return auditInsertErr
			}
			if operationFinalizeErr := finalizeRecoveredPRDevelopmentMutationOperation(
				ctx,
				conn,
				controller,
				operation,
				result,
				mutationToken,
				deadline,
				finalRevision,
				now,
			); operationFinalizeErr != nil {
				return operationFinalizeErr
			}
			if _, orchestrationErr := terminalizePRDevelopmentRepairOrchestrationAfterRecovery(
				ctx,
				conn,
				operation.AttemptID,
				prDevelopmentRepairOrchestrationMutationFence{
					controllerID:     operation.ControllerID,
					controllerRev:    operation.PreparedControllerRevision,
					lineID:           operation.LineID,
					lineVersion:      operation.LineVersion,
					mutationEpoch:    operation.MutationEpoch,
					leaseEpoch:       operation.MutationLeaseEpoch,
					leaseTokenDigest: operation.MutationLeaseTokenDigest,
					reservationHash:  operation.MutationReservationDigest,
				},
				now,
			); orchestrationErr != nil {
				return orchestrationErr
			}
		}

		operation.Status = PRDevelopmentControllerOperationFinalized
		operation.ReplacementReservationKey = ""
		operation.ClaimToken = ""
		operation.ClaimUntil = nil
		operation.RotationResultHash = rotationHash
		operation.RecoveryClaimTokenDigest = claimDigest
		operation.NewMutationLeaseEpoch = mutationLeaseEpoch
		operation.NewMutationLeaseTokenDigest = mutationTokenDigest
		operation.NewMutationLeaseUntil = mutationLeaseUntil
		operation.Result = result
		operation.ResultJSON = resultJSON
		operation.ResultHash = resultHash
		operation.FinalControllerRevision = finalRevision
		operation.FinalControllerPhase = finalPhase
		if fence != nil {
			operation.FinalFenceHash = fence.FenceHash
		}
		operation.FinalizedAt = &now
		operation.UpdatedAt = now
		operation.FinalHash = hashPRDevelopmentOperationFinal(operation)
		updateResult, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_controller_operation_intents
			SET status = 'finalized', replacement_reservation_key = '',
				claim_token = '', claim_until = NULL, rotation_result_hash = ?,
				recovery_claim_token_digest = ?, new_mutation_lease_epoch = ?,
				new_mutation_lease_token_digest = ?, new_mutation_lease_until = ?,
				result_json = ?, result_hash = ?, final_controller_revision = ?,
				final_controller_phase = ?, final_fence_hash = ?, final_hash = ?,
				finalized_at = ?, updated_at = ?
			WHERE id = ? AND controller_id = ? AND attempt_id = ? AND
				recovery_revision = ? AND status = 'recovery_claimed' AND
				claim_id = ? AND claim_token = ? AND claim_epoch = ? AND
				claim_until > ?`,
			operation.RotationResultHash,
			operation.RecoveryClaimTokenDigest,
			operation.NewMutationLeaseEpoch,
			operation.NewMutationLeaseTokenDigest,
			nullablePRDevelopmentOperationTime(operation.NewMutationLeaseUntil),
			operation.ResultJSON,
			operation.ResultHash,
			operation.FinalControllerRevision,
			operation.FinalControllerPhase,
			operation.FinalFenceHash,
			operation.FinalHash,
			toDBTime(now),
			toDBTime(now),
			operation.ID,
			operation.ControllerID,
			operation.AttemptID,
			operation.RecoveryRevision,
			operation.ClaimID,
			normalized.ClaimToken,
			operation.ClaimEpoch,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		if rowCountErr := requireOnePRDevelopmentControllerRow(updateResult); rowCountErr != nil {
			return rowCountErr
		}
		if operation.Kind != PRDevelopmentControllerOperationPark {
			if _, stageErr := stagePRDevelopmentControllerSuspension(
				ctx,
				conn,
				stagePRDevelopmentControllerSuspensionInput{
					ControllerID:        operation.ControllerID,
					AttemptID:           operation.AttemptID,
					SourceKind:          PRDevelopmentControllerSuspensionSourceOperationRecovery,
					SourceRecoveryID:    operation.RecoveryID,
					SourceOperationID:   operation.ID,
					SourceFinalRevision: operation.FinalControllerRevision,
					SourceFinalHash:     operation.FinalHash,
				},
				now,
			); stageErr != nil {
				return stageErr
			}
		}
		loadedOperation, found, loadedOperationErr := loadPRDevelopmentControllerOperationByID(
			ctx,
			conn,
			operation.ID,
		)
		if loadedOperationErr != nil {
			return loadedOperationErr
		}
		if !found {
			return errors.New("finalized recovered operation disappeared")
		}
		loadedController, found, loadedControllerErr := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			controller.ID,
		)
		if loadedControllerErr != nil {
			return loadedControllerErr
		}
		if !found {
			return errors.New("recovered operation controller disappeared")
		}
		transition = PRDevelopmentControllerOperationTransition{
			Controller: loadedController,
			Operation:  loadedOperation,
			Fence:      fence,
		}
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentControllerOperationTransition{}, false, fmt.Errorf(
			"finalize pull request development controller operation recovery: %w",
			s.dbError(err),
		)
	}
	return transition, changed, nil
}

func finalizeRecoveredPRDevelopmentMutationOperation(
	ctx context.Context,
	conn *sql.Conn,
	controller PRDevelopmentController,
	operation PRDevelopmentControllerOperation,
	result PRDevelopmentControllerOperationResult,
	mutationToken string,
	mutationLeaseUntil time.Time,
	finalRevision int64,
	now time.Time,
) error {
	workspaceID := controller.WorkspaceID
	sourceCloneURL := controller.SourceCloneURL
	sourceRef := controller.SourceRef
	sourceCommit := controller.SourceCommit
	sourceTree := controller.SourceTree
	lineVersion := controller.LineVersion
	mutationEpoch := controller.MutationEpoch
	tipCommit := controller.TipCommit
	tree := controller.Tree
	if operation.Kind == PRDevelopmentControllerOperationAdopt ||
		operation.Kind == PRDevelopmentControllerOperationResume {
		workspaceID = result.WorkspaceID
		sourceCloneURL = operation.SourceCloneURL
		sourceRef = operation.SourceRef
		sourceCommit = operation.SourceCommit
		sourceTree = operation.SourceTree
		lineVersion = result.Version
		mutationEpoch = result.MutationEpoch
		tipCommit = result.Tip
		tree = result.Tree
	}
	updateResult, err := conn.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET revision = ?, phase = 'mutation', workspace_id = ?,
			source_clone_url = ?, source_ref = ?, source_commit = ?, source_tree = ?,
			line_version = ?, mutation_epoch = ?, tip_commit = ?, tree = ?,
			lease_kind = 'mutation', lease_owner = ?, lease_token = ?, lease_until = ?,
			lease_epoch = lease_epoch + 1, claims = claims + 1,
			mutation_reservation_key = ?, updated_at = ?
		WHERE id = ? AND revision = ? AND phase = 'recovery_required' AND
			current_attempt_id = ? AND lease_kind = '' AND lease_token = '' AND
			lease_epoch = ? AND mutation_reservation_key = ?`,
		finalRevision,
		workspaceID,
		sourceCloneURL,
		sourceRef,
		sourceCommit,
		sourceTree,
		lineVersion,
		mutationEpoch,
		tipCommit,
		tree,
		operation.ClaimOwner,
		mutationToken,
		toDBTime(mutationLeaseUntil),
		operation.ReplacementReservationKey,
		toDBTime(now),
		controller.ID,
		controller.Revision,
		controller.CurrentAttemptID,
		controller.LeaseEpoch,
		controller.MutationReservationKey,
	)
	if err != nil {
		return err
	}
	return requireOnePRDevelopmentControllerRow(updateResult)
}

func requireImmediatePRDevelopmentParkRecoveryReplay(
	ctx context.Context,
	conn *sql.Conn,
	controller PRDevelopmentController,
	operation PRDevelopmentControllerOperation,
) (*PRDevelopmentAttemptReviewFence, error) {
	if operation.FinalizedAt == nil ||
		controller.Revision != operation.FinalControllerRevision ||
		controller.Phase != operation.FinalControllerPhase ||
		controller.CurrentAttemptID != operation.AttemptID ||
		!controller.UpdatedAt.Equal(*operation.FinalizedAt) {
		return nil, fmt.Errorf(
			"%w: finalized operation recovery is no longer immediate",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if operation.Kind != PRDevelopmentControllerOperationPark ||
		controller.LeaseKind != "" || controller.LeaseToken != "" ||
		controller.LeaseUntil != nil || controller.MutationReservationKey != "" {
		return nil, fmt.Errorf(
			"%w: finalized Park recovery retained mutation authority",
			ErrPRDevelopmentControllerConflict,
		)
	}
	fence, found, err := loadPRDevelopmentReviewFenceByAttempt(
		ctx,
		conn,
		operation.AttemptID,
	)
	if err != nil {
		return nil, err
	}
	if !found || fence.FenceHash != operation.FinalFenceHash {
		return nil, fmt.Errorf(
			"%w: finalized Park recovery lost its review fence",
			ErrPRDevelopmentControllerConflict,
		)
	}
	return &fence, nil
}

func nullablePRDevelopmentOperationTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return toDBTime(*value)
}

func bytesEqualPRDevelopmentOperation(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func normalizePRDevelopmentControllerOperationRecoveryClaim(
	input PRDevelopmentControllerOperationRecoveryClaim,
) (PRDevelopmentControllerOperationRecoveryClaim, error) {
	input.CaseID = strings.TrimSpace(input.CaseID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	var err error
	input.ClaimID, err = normalizePRDevelopmentControllerIdentity(
		"operation recovery claim ID",
		input.ClaimID,
		MaxPRDevelopmentControllerIdentityBytes,
		true,
	)
	if err != nil {
		return PRDevelopmentControllerOperationRecoveryClaim{}, err
	}
	input.WorkerLabel, err = normalizePRDevelopmentControllerIdentity(
		"worker label",
		input.WorkerLabel,
		MaxPRDevelopmentControllerIdentityBytes,
		true,
	)
	if err != nil {
		return PRDevelopmentControllerOperationRecoveryClaim{}, err
	}
	if !validPrefixedHexID(input.CaseID, prDevelopmentCaseIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(input.OperationID, prDevelopmentOperationIDPrefix) ||
		input.ExpectedRevision < 2 ||
		input.ExpectedRevision > MaxPRDevelopmentControllerRevision ||
		input.Lease <= 0 {
		return PRDevelopmentControllerOperationRecoveryClaim{}, fmt.Errorf(
			"%w: valid operation recovery identity, revision, worker, and lease are required",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func normalizePRDevelopmentControllerOperationRecoveryRenew(
	input PRDevelopmentControllerOperationRecoveryRenew,
) (PRDevelopmentControllerOperationRecoveryRenew, error) {
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	input.RecoveryID = strings.TrimSpace(input.RecoveryID)
	var err error
	for _, field := range []struct {
		name    string
		value   *string
		maximum int
	}{
		{"operation recovery claim ID", &input.ClaimID, MaxPRDevelopmentControllerIdentityBytes},
		{"operation recovery claim token", &input.ClaimToken, prDevelopmentControllerLeaseTokenBytes},
	} {
		*field.value, err = normalizePRDevelopmentControllerIdentity(
			field.name,
			*field.value,
			field.maximum,
			true,
		)
		if err != nil {
			return PRDevelopmentControllerOperationRecoveryRenew{}, err
		}
	}
	validRecoveryID := input.RecoveryID == "" || validPrefixedHexID(
		input.RecoveryID,
		prDevelopmentRecoveryIntentIDPrefix,
	)
	if !validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(input.OperationID, prDevelopmentOperationIDPrefix) ||
		!validRecoveryID || input.ClaimEpoch < 1 || input.Lease <= 0 {
		return PRDevelopmentControllerOperationRecoveryRenew{}, fmt.Errorf(
			"%w: valid operation recovery claim proof and lease are required",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func normalizePRDevelopmentControllerOperationRecoveryFinalize(
	input PRDevelopmentControllerOperationRecoveryFinalize,
) (PRDevelopmentControllerOperationRecoveryFinalize, error) {
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	input.RecoveryID = strings.TrimSpace(input.RecoveryID)
	var err error
	for _, field := range []struct {
		name    string
		value   *string
		maximum int
	}{
		{"operation recovery claim ID", &input.ClaimID, MaxPRDevelopmentControllerIdentityBytes},
		{"operation recovery claim token", &input.ClaimToken, prDevelopmentControllerLeaseTokenBytes},
	} {
		*field.value, err = normalizePRDevelopmentControllerIdentity(
			field.name,
			*field.value,
			field.maximum,
			true,
		)
		if err != nil {
			return PRDevelopmentControllerOperationRecoveryFinalize{}, err
		}
	}
	if input.Rotation.WorkspaceID != "" {
		input.Rotation.WorkspaceID, err = normalizePRDevelopmentControllerIdentity(
			"operation recovery rotation workspace ID",
			input.Rotation.WorkspaceID,
			MaxPRDevelopmentControllerIdentityBytes,
			true,
		)
		if err != nil {
			return PRDevelopmentControllerOperationRecoveryFinalize{}, err
		}
	}
	input.Rotation.Tip = strings.TrimSpace(input.Rotation.Tip)
	input.Rotation.Tree = strings.TrimSpace(input.Rotation.Tree)
	input.Rotation.RotationHash = strings.TrimSpace(input.Rotation.RotationHash)
	input.Rotation.AlreadyRotated = false
	validRecoveryID := input.RecoveryID == "" || validPrefixedHexID(
		input.RecoveryID,
		prDevelopmentRecoveryIntentIDPrefix,
	)
	validRotation := input.Rotation.Version >= 0 &&
		input.Rotation.Version <= MaxPRDevelopmentControllerFences &&
		input.Rotation.MutationEpoch >= 0 &&
		input.Rotation.MutationEpoch <= MaxPRDevelopmentControllerFences+1
	if input.Rotation.WorkspaceID == "" {
		validRotation = validRotation && !input.Rotation.Bound &&
			input.Rotation.Version == 0 && input.Rotation.MutationEpoch == 0 &&
			input.Rotation.Tip == "" && input.Rotation.Tree == "" &&
			input.Rotation.RotationHash == ""
	} else {
		validRotation = validRotation && input.Rotation.Bound &&
			validSameWidthPRDevelopmentOIDs(input.Rotation.Tip, input.Rotation.Tree) &&
			validPRDevelopmentHex(input.Rotation.RotationHash, sha256.Size*2)
	}
	if !validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(input.OperationID, prDevelopmentOperationIDPrefix) ||
		!validRecoveryID || input.ExpectedRevision < 2 ||
		input.ExpectedRevision > MaxPRDevelopmentControllerRevision ||
		input.ClaimEpoch < 1 || input.Lease < 0 || !validRotation {
		return PRDevelopmentControllerOperationRecoveryFinalize{}, fmt.Errorf(
			"%w: invalid operation recovery finalization proof",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func equalPRDevelopmentOperationRecoveryRotation(
	operation PRDevelopmentControllerOperation,
	result PRDevelopmentControllerOperationResult,
	rotation PRDevelopmentControllerRecoveryRotationResult,
) bool {
	if operation.Kind == PRDevelopmentControllerOperationPark {
		return rotation == (PRDevelopmentControllerRecoveryRotationResult{})
	}
	if rotation.WorkspaceID != operation.WorkspaceID || !rotation.Bound {
		return false
	}
	switch operation.Kind {
	case PRDevelopmentControllerOperationAdopt,
		PRDevelopmentControllerOperationResume:
		return rotation.Version == result.Version &&
			rotation.MutationEpoch == result.MutationEpoch &&
			rotation.Tip == result.Tip && rotation.Tree == result.Tree
	case PRDevelopmentControllerOperationCommit:
		return rotation.Version == operation.LineVersion &&
			rotation.MutationEpoch == operation.MutationEpoch &&
			rotation.Tip == operation.TipCommit && rotation.Tree == operation.Tree
	default:
		return false
	}
}

func insertFinalizedPRDevelopmentRecoveryAuditForOperation(
	ctx context.Context,
	conn *sql.Conn,
	operation PRDevelopmentControllerOperation,
	rotation PRDevelopmentControllerRecoveryRotationResult,
	rotationHash, claimDigest, mutationTokenDigest string,
	mutationLeaseEpoch int64,
	mutationLeaseUntil, now time.Time,
) error {
	if operation.RecoveryStagedAt == nil || operation.ClaimedAt == nil ||
		operation.Kind == PRDevelopmentControllerOperationPark {
		return errors.New("operation recovery audit linkage is incomplete")
	}
	if _, found, err := loadActivePRDevelopmentRecoveryIntent(
		ctx,
		conn,
		operation.ControllerID,
	); err != nil {
		return err
	} else if found {
		return fmt.Errorf(
			"%w: operation recovery conflicts with an active legacy recovery",
			ErrPRDevelopmentControllerConflict,
		)
	}
	intents, err := loadPRDevelopmentRecoveryIntents(
		ctx,
		conn,
		operation.ControllerID,
	)
	if err != nil {
		return err
	}
	if len(intents) >= MaxPRDevelopmentControllerRecoveries {
		return fmt.Errorf(
			"%w: controller recovery audit history capacity exhausted",
			ErrPRDevelopmentControllerConflict,
		)
	}
	previousHash := emptyPRDevelopmentRecoveryDigest()
	if len(intents) > 0 {
		latest := intents[len(intents)-1]
		if latest.Status != PRDevelopmentControllerRecoveryFinalized ||
			latest.FinalHash == "" {
			return errors.New("operation recovery audit has an unresolved predecessor")
		}
		previousHash = latest.FinalHash
	}
	intent := PRDevelopmentControllerRecoveryIntent{
		ID:                           operation.RecoveryID,
		ControllerID:                 operation.ControllerID,
		AttemptID:                    operation.AttemptID,
		Ordinal:                      len(intents),
		RecoveryRevision:             operation.RecoveryRevision,
		Mode:                         PRDevelopmentControllerRecoveryBound,
		Status:                       PRDevelopmentControllerRecoveryFinalized,
		AgentID:                      operation.AgentID,
		WorkspaceID:                  rotation.WorkspaceID,
		LineID:                       operation.LineID,
		SourceCloneURL:               operation.SourceCloneURL,
		SourceRef:                    operation.SourceRef,
		SourceCommit:                 operation.SourceCommit,
		SourceTree:                   operation.SourceTree,
		LineVersion:                  rotation.Version,
		MutationEpoch:                rotation.MutationEpoch,
		TipCommit:                    rotation.Tip,
		Tree:                         rotation.Tree,
		PreviousReservationDigest:    operation.MutationReservationDigest,
		ReplacementReservationDigest: operation.ReplacementReservationDigest,
		ExpiredControllerRevision:    operation.ExpiredControllerRevision,
		ExpiredLeaseEpoch:            operation.ExpiredLeaseEpoch,
		ExpiredLeaseTokenDigest:      operation.ExpiredLeaseTokenDigest,
		PreviousHash:                 previousHash,
		ClaimID:                      operation.ClaimID,
		ClaimOwner:                   operation.ClaimOwner,
		ClaimEpoch:                   operation.ClaimEpoch,
		Claims:                       operation.Claims,
		RotationResultHash:           rotationHash,
		RecoveryClaimTokenDigest:     claimDigest,
		NewMutationLeaseEpoch:        mutationLeaseEpoch,
		NewMutationLeaseTokenDigest:  mutationTokenDigest,
		NewMutationLeaseUntil:        &mutationLeaseUntil,
		FinalRevision:                operation.RecoveryRevision + 1,
		CreatedAt:                    *operation.RecoveryStagedAt,
		ClaimedAt:                    operation.ClaimedAt,
		FinalizedAt:                  &now,
		UpdatedAt:                    now,
	}
	intent.IntentHash = hashPRDevelopmentRecoveryIntent(intent)
	intent.FinalHash = hashPRDevelopmentRecoveryFinal(
		intent,
		rotationHash,
		claimDigest,
		mutationLeaseEpoch,
		mutationTokenDigest,
		mutationLeaseUntil,
		intent.FinalRevision,
		now,
	)
	_, err = conn.ExecContext(ctx, `
		INSERT INTO pr_development_controller_recovery_intents (
			id, controller_id, attempt_id, ordinal, recovery_revision, mode, status,
			agent_id, workspace_id, line_id, source_clone_url, source_ref, source_commit,
			source_tree, line_version, mutation_epoch, tip_commit, tree,
			previous_reservation_key, replacement_reservation_key,
			previous_reservation_digest, replacement_reservation_digest,
			expired_controller_revision, expired_lease_epoch, expired_lease_token_digest,
			previous_hash, intent_hash, claim_id, claim_owner, claim_token, claim_until,
			claim_epoch, claims, rotation_result_hash, recovery_claim_token_digest,
			new_mutation_lease_epoch, new_mutation_lease_token_digest,
			new_mutation_lease_until, final_revision, final_hash, created_at,
			claimed_at, finalized_at, updated_at
		) VALUES (
			?, ?, ?, ?, ?, ?, 'finalized', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '',
			?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		)`,
		intent.ID,
		intent.ControllerID,
		intent.AttemptID,
		intent.Ordinal,
		intent.RecoveryRevision,
		intent.Mode,
		intent.AgentID,
		intent.WorkspaceID,
		intent.LineID,
		intent.SourceCloneURL,
		intent.SourceRef,
		intent.SourceCommit,
		intent.SourceTree,
		intent.LineVersion,
		intent.MutationEpoch,
		intent.TipCommit,
		intent.Tree,
		intent.PreviousReservationDigest,
		intent.ReplacementReservationDigest,
		intent.ExpiredControllerRevision,
		intent.ExpiredLeaseEpoch,
		intent.ExpiredLeaseTokenDigest,
		intent.PreviousHash,
		intent.IntentHash,
		intent.ClaimID,
		intent.ClaimOwner,
		intent.ClaimEpoch,
		intent.Claims,
		intent.RotationResultHash,
		intent.RecoveryClaimTokenDigest,
		intent.NewMutationLeaseEpoch,
		intent.NewMutationLeaseTokenDigest,
		toDBTime(*intent.NewMutationLeaseUntil),
		intent.FinalRevision,
		intent.FinalHash,
		toDBTime(intent.CreatedAt),
		toDBTime(*intent.ClaimedAt),
		toDBTime(*intent.FinalizedAt),
		toDBTime(intent.UpdatedAt),
	)
	return err
}
