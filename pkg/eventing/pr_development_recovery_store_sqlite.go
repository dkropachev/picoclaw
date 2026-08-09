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

const prDevelopmentRecoveryIntentColumns = `
	id, controller_id, attempt_id, ordinal, recovery_revision, mode, status,
	agent_id, workspace_id, line_id, source_clone_url, source_ref, source_commit,
	source_tree, line_version, mutation_epoch, tip_commit, tree,
	previous_reservation_key, replacement_reservation_key,
	previous_reservation_digest, replacement_reservation_digest,
	expired_controller_revision, expired_lease_epoch, expired_lease_token_digest,
	previous_hash, intent_hash, claim_id, claim_owner, claim_token, claim_until,
	claim_epoch, claims, rotation_result_hash, recovery_claim_token_digest,
	new_mutation_lease_epoch, new_mutation_lease_token_digest, new_mutation_lease_until, final_revision,
	final_hash, created_at, claimed_at, finalized_at, updated_at`

func insertPRDevelopmentRecoveryIntentForExpiry(
	ctx context.Context,
	conn *sql.Conn,
	controller PRDevelopmentController,
	now time.Time,
) (PRDevelopmentControllerRecoveryIntent, error) {
	if controller.Phase != PRDevelopmentControllerMutation ||
		controller.LeaseKind != PRDevelopmentControllerMutationLease ||
		controller.LeaseUntil == nil || controller.LeaseUntil.After(now) ||
		controller.MutationReservationKey == "" ||
		controller.CurrentAttemptID == "" {
		return PRDevelopmentControllerRecoveryIntent{}, fmt.Errorf(
			"%w: controller mutation lease is not expired",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if controller.WorkspaceID != "" &&
		controller.MutationEpoch != controller.LineVersion+1 {
		return PRDevelopmentControllerRecoveryIntent{},
			ErrPRDevelopmentControllerRecoveryRequired
	}
	if _, found, err := loadActivePRDevelopmentRecoveryIntent(
		ctx, conn, controller.ID,
	); err != nil {
		return PRDevelopmentControllerRecoveryIntent{}, err
	} else if found {
		return PRDevelopmentControllerRecoveryIntent{}, fmt.Errorf(
			"%w: controller already has an active recovery intent",
			ErrPRDevelopmentControllerConflict,
		)
	}
	session, err := loadPRDevelopmentRepairSessionByID(
		ctx, conn, controller.OwnerSessionID,
	)
	if err != nil {
		return PRDevelopmentControllerRecoveryIntent{}, err
	}
	if session.WorkspaceID == "" || session.CloneURL == "" || session.HeadRef == "" ||
		session.HeadSHA == "" || session.AgentID != controller.AgentID {
		return PRDevelopmentControllerRecoveryIntent{}, errors.New(
			"controller recovery owner pin is incomplete",
		)
	}
	intents, err := loadPRDevelopmentRecoveryIntents(ctx, conn, controller.ID)
	if err != nil {
		return PRDevelopmentControllerRecoveryIntent{}, err
	}
	if len(intents) >= MaxPRDevelopmentControllerRecoveries {
		return PRDevelopmentControllerRecoveryIntent{}, fmt.Errorf(
			"%w: controller recovery history capacity exhausted",
			ErrPRDevelopmentControllerConflict,
		)
	}
	previousHash := emptyPRDevelopmentRecoveryDigest()
	if len(intents) > 0 {
		latest := intents[len(intents)-1]
		if latest.Status != PRDevelopmentControllerRecoveryFinalized || latest.FinalHash == "" {
			return PRDevelopmentControllerRecoveryIntent{}, errors.New(
				"controller recovery chain has an unresolved predecessor",
			)
		}
		previousHash = latest.FinalHash
	}
	replacement, err := newUniquePRDevelopmentMutationReservation(ctx, conn)
	if err != nil {
		return PRDevelopmentControllerRecoveryIntent{}, err
	}
	intentID, err := newPrefixedID(prDevelopmentRecoveryIntentIDPrefix)
	if err != nil {
		return PRDevelopmentControllerRecoveryIntent{}, err
	}
	intent := PRDevelopmentControllerRecoveryIntent{
		ID:                        intentID,
		ControllerID:              controller.ID,
		AttemptID:                 controller.CurrentAttemptID,
		Ordinal:                   len(intents),
		RecoveryRevision:          controller.Revision + 1,
		Mode:                      PRDevelopmentControllerRecoveryUnbound,
		Status:                    PRDevelopmentControllerRecoveryPending,
		AgentID:                   controller.AgentID,
		WorkspaceID:               session.WorkspaceID,
		LineID:                    controller.LineID,
		SourceCloneURL:            session.CloneURL,
		SourceRef:                 session.HeadRef,
		SourceCommit:              session.HeadSHA,
		PreviousReservationKey:    controller.MutationReservationKey,
		ReplacementReservationKey: replacement,
		PreviousReservationDigest: prDevelopmentMutationReservationDigest(
			controller.MutationReservationKey,
		),
		ReplacementReservationDigest: prDevelopmentMutationReservationDigest(
			replacement,
		),
		ExpiredControllerRevision: controller.Revision,
		ExpiredLeaseEpoch:         controller.LeaseEpoch,
		ExpiredLeaseTokenDigest: prDevelopmentLeaseTokenDigest(
			PRDevelopmentControllerMutationLease,
			controller.LeaseToken,
		),
		PreviousHash: previousHash,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if controller.WorkspaceID != "" {
		intent.Mode = PRDevelopmentControllerRecoveryBound
		intent.WorkspaceID = controller.WorkspaceID
		intent.SourceCloneURL = controller.SourceCloneURL
		intent.SourceRef = controller.SourceRef
		intent.SourceCommit = controller.SourceCommit
		intent.SourceTree = controller.SourceTree
		intent.LineVersion = controller.LineVersion
		intent.MutationEpoch = controller.MutationEpoch
		intent.TipCommit = controller.TipCommit
		intent.Tree = controller.Tree
	}
	intent.IntentHash = hashPRDevelopmentRecoveryIntent(intent)
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
			new_mutation_lease_until, final_revision,
			final_hash, created_at, claimed_at, finalized_at, updated_at
		) VALUES (
			?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, '', '', '', NULL, 0, 0, '', '', 0, '', NULL, 0, '', ?, NULL, NULL, ?
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
		intent.PreviousReservationKey,
		intent.ReplacementReservationKey,
		intent.PreviousReservationDigest,
		intent.ReplacementReservationDigest,
		intent.ExpiredControllerRevision,
		intent.ExpiredLeaseEpoch,
		intent.ExpiredLeaseTokenDigest,
		intent.PreviousHash,
		intent.IntentHash,
		toDBTime(now),
		toDBTime(now),
	)
	if err != nil {
		return PRDevelopmentControllerRecoveryIntent{}, err
	}
	return intent, nil
}

// ClaimPRDevelopmentControllerRecovery acquires the exact intent that was
// persisted with an expired mutation lease. A live claim with the same
// caller-durable ClaimID is an exact no-write replay; an expired claim can be
// safely reclaimed because every worker executes the same rotation intent.
func (s *Store) ClaimPRDevelopmentControllerRecovery(
	ctx context.Context,
	input PRDevelopmentControllerRecoveryClaim,
) (PRDevelopmentControllerRecoveryLease, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentControllerRecoveryLease{}, false, err
	}
	normalized, err := normalizePRDevelopmentControllerRecoveryClaim(input)
	if err != nil {
		return PRDevelopmentControllerRecoveryLease{}, false, err
	}
	var (
		lease   PRDevelopmentControllerRecoveryLease
		changed bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		relation, relationErr := loadPRDevelopmentControllerAttemptRelation(
			ctx, conn, normalized.CaseID, normalized.AttemptID,
		)
		if relationErr != nil {
			return relationErr
		}
		controller, found, loadErr := loadPRDevelopmentControllerAggregate(
			ctx, conn, relation.Thread.ID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		if controller.OwnerSessionID != relation.Session.ID ||
			controller.CurrentAttemptID != normalized.AttemptID {
			return fmt.Errorf(
				"%w: recovery attempt is not the controller owner",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if controller.Revision != normalized.ExpectedRevision {
			return fmt.Errorf(
				"%w: expected revision %d, current revision %d",
				ErrPRDevelopmentControllerConflict,
				normalized.ExpectedRevision,
				controller.Revision,
			)
		}
		if controller.Phase != PRDevelopmentControllerRecoveryRequired {
			return fmt.Errorf(
				"%w: controller is not awaiting recovery",
				ErrPRDevelopmentControllerConflict,
			)
		}
		intent, intentFound, intentErr := loadActivePRDevelopmentRecoveryIntent(
			ctx, conn, controller.ID,
		)
		if intentErr != nil {
			return intentErr
		}
		if !intentFound {
			// A v11 database may already contain a recovery_required row. It
			// has no expired-token proof or precommitted replacement bearer,
			// so v12 deliberately refuses to manufacture either after the fact.
			return ErrPRDevelopmentControllerRecoveryRequired
		}
		if intent.AttemptID != normalized.AttemptID ||
			intent.RecoveryRevision != controller.Revision {
			return fmt.Errorf(
				"%w: active recovery intent differs from controller high-water state",
				ErrPRDevelopmentControllerConflict,
			)
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if timeErr := requireNonRegressingPRDevelopmentControllerTime(
			now,
			maxPRDevelopmentControllerTime(
				controller.UpdatedAt, relation.Session.UpdatedAt,
				relation.Attempt.UpdatedAt, intent.UpdatedAt,
			),
		); timeErr != nil {
			return timeErr
		}
		deadline, deadlineErr := prDevelopmentControllerDeadline(now, normalized.Lease)
		if deadlineErr != nil {
			return deadlineErr
		}
		if intent.ClaimUntil != nil && intent.ClaimUntil.After(deadline) {
			deadline = *intent.ClaimUntil
		}
		if intent.Status == PRDevelopmentControllerRecoveryClaimed &&
			intent.ClaimUntil != nil && intent.ClaimUntil.After(now) {
			if intent.ClaimID != normalized.ClaimID ||
				intent.ClaimOwner != normalized.WorkerLabel {
				return ErrPRDevelopmentControllerActive
			}
			lease = PRDevelopmentControllerRecoveryLease{
				Controller: controller,
				Intent:     intent,
			}
			return nil
		}
		if intent.Status == PRDevelopmentControllerRecoveryClaimed &&
			intent.ClaimID == normalized.ClaimID &&
			intent.ClaimOwner != normalized.WorkerLabel {
			return fmt.Errorf(
				"%w: recovery claim ID is bound to a different worker",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if intent.Status != PRDevelopmentControllerRecoveryPending &&
			intent.Status != PRDevelopmentControllerRecoveryClaimed {
			return fmt.Errorf(
				"%w: recovery intent is not claimable",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if intent.ClaimEpoch == int64(^uint64(0)>>1) {
			return fmt.Errorf(
				"%w: recovery claim epoch capacity exhausted",
				ErrPRDevelopmentControllerConflict,
			)
		}
		var duplicate int
		if queryErr := conn.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM pr_development_controller_recovery_intents
			WHERE claim_id = ? AND id <> ?`,
			normalized.ClaimID,
			intent.ID,
		).Scan(&duplicate); queryErr != nil {
			return queryErr
		}
		if duplicate != 0 {
			return fmt.Errorf(
				"%w: recovery claim ID is already bound",
				ErrPRDevelopmentControllerConflict,
			)
		}
		token, tokenErr := newLeaseToken(normalized.WorkerLabel)
		if tokenErr != nil {
			return tokenErr
		}
		reclaimed := intent.Status == PRDevelopmentControllerRecoveryClaimed
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_controller_recovery_intents
			SET status = 'claimed', claim_id = ?, claim_owner = ?, claim_token = ?,
				claim_until = ?, claim_epoch = claim_epoch + 1, claims = claims + 1,
				claimed_at = COALESCE(claimed_at, ?), updated_at = ?
			WHERE id = ? AND controller_id = ? AND recovery_revision = ? AND
				(status = 'pending' OR (status = 'claimed' AND claim_until <= ?))`,
			normalized.ClaimID,
			normalized.WorkerLabel,
			token,
			toDBTime(deadline),
			toDBTime(now),
			toDBTime(now),
			intent.ID,
			controller.ID,
			controller.Revision,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
			return rowErr
		}
		claimed, claimedFound, claimedErr := loadPRDevelopmentRecoveryIntentByID(
			ctx, conn, intent.ID,
		)
		if claimedErr != nil {
			return claimedErr
		}
		if !claimedFound {
			return errors.New("claimed pull request development recovery disappeared")
		}
		lease = PRDevelopmentControllerRecoveryLease{
			Controller: controller,
			Intent:     claimed,
			Reclaimed:  reclaimed,
		}
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentControllerRecoveryLease{}, false, fmt.Errorf(
			"claim pull request development controller recovery: %w",
			s.dbError(err),
		)
	}
	return lease, changed, nil
}

// RenewPRDevelopmentControllerRecovery renews no filesystem authority. It
// extends only the exact live claim for an already-fixed rotation intent.
func (s *Store) RenewPRDevelopmentControllerRecovery(
	ctx context.Context,
	input PRDevelopmentControllerRecoveryRenew,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	normalized, err := normalizePRDevelopmentControllerRecoveryRenew(input)
	if err != nil {
		return err
	}
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		intent, found, loadErr := loadPRDevelopmentRecoveryIntentByID(
			ctx, conn, normalized.RecoveryID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		controller, controllerFound, controllerErr := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			normalized.ControllerID,
		)
		if controllerErr != nil {
			return controllerErr
		}
		if !controllerFound {
			return sql.ErrNoRows
		}
		if controller.Phase != PRDevelopmentControllerRecoveryRequired ||
			controller.CurrentAttemptID != normalized.AttemptID ||
			controller.Revision != intent.RecoveryRevision {
			return fmt.Errorf(
				"%w: recovery claim is no longer controller-current",
				ErrPRDevelopmentControllerConflict,
			)
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if timeErr := requireNonRegressingPRDevelopmentControllerTime(
			now, maxPRDevelopmentControllerTime(intent.UpdatedAt, controller.UpdatedAt),
		); timeErr != nil {
			return timeErr
		}
		deadline, deadlineErr := prDevelopmentControllerDeadline(now, normalized.Lease)
		if deadlineErr != nil {
			return deadlineErr
		}
		if intent.ClaimUntil != nil && intent.ClaimUntil.After(deadline) {
			deadline = *intent.ClaimUntil
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_controller_recovery_intents
			SET claim_until = ?, updated_at = ?
			WHERE id = ? AND controller_id = ? AND attempt_id = ? AND
				status = 'claimed' AND claim_id = ? AND claim_token = ? AND
				claim_epoch = ? AND claim_until > ?`,
			toDBTime(deadline),
			toDBTime(now),
			normalized.RecoveryID,
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
		if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
			return fmt.Errorf("%w: %v", ErrStaleLease, rowErr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf(
			"renew pull request development controller recovery: %w",
			s.dbError(err),
		)
	}
	return nil
}

// FinalizePRDevelopmentControllerRecovery fences an exact successful
// reservation rotation and restores mutation authority with new credentials.
func (s *Store) FinalizePRDevelopmentControllerRecovery(
	ctx context.Context,
	input PRDevelopmentControllerRecoveryFinalize,
) (PRDevelopmentController, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentController{}, false, err
	}
	normalized, err := normalizePRDevelopmentControllerRecoveryFinalize(input)
	if err != nil {
		return PRDevelopmentController{}, false, err
	}
	rotationHash := hashPRDevelopmentRecoveryRotationResult(normalized.Rotation)
	var (
		controller       PRDevelopmentController
		changed          bool
		recoveryRequired bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		intent, found, loadErr := loadPRDevelopmentRecoveryIntentByID(
			ctx, conn, normalized.RecoveryID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		current, controllerFound, controllerErr := loadPRDevelopmentControllerAggregateByID(
			ctx, conn, normalized.ControllerID,
		)
		if controllerErr != nil {
			return controllerErr
		}
		if !controllerFound {
			return sql.ErrNoRows
		}
		if intent.ControllerID != normalized.ControllerID ||
			intent.AttemptID != normalized.AttemptID ||
			intent.RecoveryRevision != normalized.ExpectedRevision ||
			intent.ClaimID != normalized.ClaimID ||
			intent.ClaimEpoch != normalized.ClaimEpoch ||
			!equalPRDevelopmentRecoveryRotationResult(intent, normalized.Rotation) {
			return fmt.Errorf(
				"%w: recovery finalization differs from its durable intent",
				ErrPRDevelopmentControllerConflict,
			)
		}
		claimDigest := prDevelopmentRecoveryClaimTokenDigest(normalized.ClaimToken)
		if intent.Status == PRDevelopmentControllerRecoveryFinalized {
			now, clockErr := s.currentTime()
			if clockErr != nil {
				return clockErr
			}
			if timeErr := requireNonRegressingPRDevelopmentControllerTime(
				now,
				maxPRDevelopmentControllerTime(current.UpdatedAt, intent.UpdatedAt),
			); timeErr != nil {
				return timeErr
			}
			if intent.RotationResultHash != rotationHash ||
				intent.RecoveryClaimTokenDigest != claimDigest ||
				current.Revision != intent.FinalRevision ||
				current.Phase != PRDevelopmentControllerMutation ||
				current.CurrentAttemptID != intent.AttemptID ||
				current.LeaseOwner != intent.ClaimOwner ||
				current.LeaseEpoch != intent.NewMutationLeaseEpoch ||
				prDevelopmentLeaseTokenDigest(
					PRDevelopmentControllerMutationLease,
					current.LeaseToken,
				) != intent.NewMutationLeaseTokenDigest ||
				intent.NewMutationLeaseUntil == nil || current.LeaseUntil == nil ||
				!current.LeaseUntil.Equal(*intent.NewMutationLeaseUntil) ||
				prDevelopmentMutationReservationDigest(
					current.MutationReservationKey,
				) != intent.ReplacementReservationDigest || intent.FinalizedAt == nil {
				return fmt.Errorf(
					"%w: finalized recovery replay is no longer current",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if current.LeaseUntil == nil || !current.LeaseUntil.After(now) {
				if expireErr := expirePRDevelopmentMutationLease(
					ctx, conn, current, now,
				); expireErr != nil {
					return expireErr
				}
				recoveryRequired = true
				return nil
			}
			if !current.UpdatedAt.Equal(*intent.FinalizedAt) {
				return fmt.Errorf(
					"%w: finalized recovery replay followed mutation lease progress",
					ErrPRDevelopmentControllerConflict,
				)
			}
			controller = current
			return nil
		}
		if intent.Status != PRDevelopmentControllerRecoveryClaimed ||
			intent.ClaimToken != normalized.ClaimToken {
			return fmt.Errorf(
				"%w: operation does not hold the exact recovery claim",
				ErrPRDevelopmentControllerConflict,
			)
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if intent.ClaimUntil == nil || !intent.ClaimUntil.After(now) {
			return ErrStaleLease
		}
		if timeErr := requireNonRegressingPRDevelopmentControllerTime(
			now, maxPRDevelopmentControllerTime(current.UpdatedAt, intent.UpdatedAt),
		); timeErr != nil {
			return timeErr
		}
		if current.Phase != PRDevelopmentControllerRecoveryRequired ||
			current.Revision != intent.RecoveryRevision ||
			current.CurrentAttemptID != intent.AttemptID ||
			current.MutationReservationKey != intent.PreviousReservationKey ||
			current.LeaseEpoch != intent.ExpiredLeaseEpoch {
			return fmt.Errorf(
				"%w: recovery controller high-water state changed",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if current.LeaseEpoch == int64(^uint64(0)>>1) {
			return fmt.Errorf(
				"%w: controller lease epoch capacity exhausted",
				ErrPRDevelopmentControllerConflict,
			)
		}
		requiredHeadroom := int64(3) // finalize, park, review finish
		if intent.Mode == PRDevelopmentControllerRecoveryUnbound {
			requiredHeadroom++ // first retained-line bind
		}
		if current.Revision > MaxPRDevelopmentControllerRevision-requiredHeadroom {
			return fmt.Errorf(
				"%w: recovery has no finalization revision headroom",
				ErrPRDevelopmentControllerConflict,
			)
		}
		deadline, deadlineErr := prDevelopmentControllerDeadline(now, normalized.Lease)
		if deadlineErr != nil {
			return deadlineErr
		}
		mutationToken, tokenErr := newLeaseToken(intent.ClaimOwner)
		if tokenErr != nil {
			return tokenErr
		}
		mutationLeaseEpoch := current.LeaseEpoch + 1
		finalRevision := current.Revision + 1
		mutationTokenDigest := prDevelopmentLeaseTokenDigest(
			PRDevelopmentControllerMutationLease,
			mutationToken,
		)
		finalHash := hashPRDevelopmentRecoveryFinal(
			intent,
			rotationHash,
			claimDigest,
			mutationLeaseEpoch,
			mutationTokenDigest,
			deadline,
			finalRevision,
			now,
		)
		intentResult, intentErr := conn.ExecContext(ctx, `
			UPDATE pr_development_controller_recovery_intents
			SET status = 'finalized', previous_reservation_key = '',
				replacement_reservation_key = '', claim_token = '', claim_until = NULL,
				rotation_result_hash = ?, recovery_claim_token_digest = ?,
				new_mutation_lease_epoch = ?, new_mutation_lease_token_digest = ?,
				new_mutation_lease_until = ?, final_revision = ?, final_hash = ?,
				finalized_at = ?, updated_at = ?
			WHERE id = ? AND controller_id = ? AND attempt_id = ? AND
				recovery_revision = ? AND status = 'claimed' AND claim_id = ? AND
				claim_token = ? AND claim_epoch = ? AND claim_until > ?`,
			rotationHash,
			claimDigest,
			mutationLeaseEpoch,
			mutationTokenDigest,
			toDBTime(deadline),
			finalRevision,
			finalHash,
			toDBTime(now),
			toDBTime(now),
			intent.ID,
			current.ID,
			current.CurrentAttemptID,
			current.Revision,
			intent.ClaimID,
			intent.ClaimToken,
			intent.ClaimEpoch,
			toDBTime(now),
		)
		if intentErr != nil {
			return intentErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(intentResult); rowErr != nil {
			return rowErr
		}
		controllerResult, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_thread_controllers
			SET revision = revision + 1, phase = 'mutation', lease_kind = 'mutation',
				lease_owner = ?, lease_token = ?, lease_until = ?,
				lease_epoch = lease_epoch + 1, claims = claims + 1,
				mutation_reservation_key = ?, updated_at = ?
			WHERE id = ? AND revision = ? AND phase = 'recovery_required' AND
				current_attempt_id = ? AND lease_kind = '' AND lease_token = '' AND
				mutation_reservation_key = ?`,
			intent.ClaimOwner,
			mutationToken,
			toDBTime(deadline),
			intent.ReplacementReservationKey,
			toDBTime(now),
			current.ID,
			current.Revision,
			current.CurrentAttemptID,
			current.MutationReservationKey,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(controllerResult); rowErr != nil {
			return rowErr
		}
		loaded, loadedFound, reloadErr := loadPRDevelopmentControllerAggregateByID(
			ctx, conn, current.ID,
		)
		if reloadErr != nil {
			return reloadErr
		}
		if !loadedFound {
			return errors.New("recovered pull request development controller disappeared")
		}
		controller = loaded
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentController{}, false, fmt.Errorf(
			"finalize pull request development controller recovery: %w",
			s.dbError(err),
		)
	}
	if recoveryRequired {
		return PRDevelopmentController{}, false, fmt.Errorf(
			"finalize pull request development controller recovery: %w",
			ErrPRDevelopmentControllerRecoveryRequired,
		)
	}
	return controller, changed, nil
}

func normalizePRDevelopmentControllerRecoveryClaim(
	input PRDevelopmentControllerRecoveryClaim,
) (PRDevelopmentControllerRecoveryClaim, error) {
	input.CaseID = strings.TrimSpace(input.CaseID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	var err error
	input.ClaimID, err = normalizePRDevelopmentControllerIdentity(
		"recovery claim ID", input.ClaimID, MaxPRDevelopmentControllerIdentityBytes, true,
	)
	if err != nil {
		return PRDevelopmentControllerRecoveryClaim{}, err
	}
	input.WorkerLabel, err = normalizePRDevelopmentControllerIdentity(
		"worker label", input.WorkerLabel, MaxPRDevelopmentControllerIdentityBytes, true,
	)
	if err != nil {
		return PRDevelopmentControllerRecoveryClaim{}, err
	}
	if !validPrefixedHexID(input.CaseID, prDevelopmentCaseIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		input.ExpectedRevision < 2 ||
		input.ExpectedRevision > MaxPRDevelopmentControllerRevision ||
		input.Lease <= 0 {
		return PRDevelopmentControllerRecoveryClaim{}, fmt.Errorf(
			"%w: valid case, attempt, revision, claim, worker, and lease are required",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func normalizePRDevelopmentControllerRecoveryRenew(
	input PRDevelopmentControllerRecoveryRenew,
) (PRDevelopmentControllerRecoveryRenew, error) {
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.RecoveryID = strings.TrimSpace(input.RecoveryID)
	var err error
	for _, field := range []struct {
		name    string
		value   *string
		maximum int
	}{
		{"recovery claim ID", &input.ClaimID, MaxPRDevelopmentControllerIdentityBytes},
		{"recovery claim token", &input.ClaimToken, prDevelopmentControllerLeaseTokenBytes},
	} {
		*field.value, err = normalizePRDevelopmentControllerIdentity(
			field.name, *field.value, field.maximum, true,
		)
		if err != nil {
			return PRDevelopmentControllerRecoveryRenew{}, err
		}
	}
	if !validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(input.RecoveryID, prDevelopmentRecoveryIntentIDPrefix) ||
		input.ClaimEpoch < 1 || input.Lease <= 0 {
		return PRDevelopmentControllerRecoveryRenew{}, fmt.Errorf(
			"%w: valid recovery identity, claim proof, and lease are required",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func normalizePRDevelopmentControllerRecoveryFinalize(
	input PRDevelopmentControllerRecoveryFinalize,
) (PRDevelopmentControllerRecoveryFinalize, error) {
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.RecoveryID = strings.TrimSpace(input.RecoveryID)
	var err error
	for _, field := range []struct {
		name    string
		value   *string
		maximum int
	}{
		{"recovery claim ID", &input.ClaimID, MaxPRDevelopmentControllerIdentityBytes},
		{"recovery claim token", &input.ClaimToken, prDevelopmentControllerLeaseTokenBytes},
		{"workspace ID", &input.Rotation.WorkspaceID, MaxPRDevelopmentControllerIdentityBytes},
	} {
		*field.value, err = normalizePRDevelopmentControllerIdentity(
			field.name, *field.value, field.maximum, true,
		)
		if err != nil {
			return PRDevelopmentControllerRecoveryFinalize{}, err
		}
	}
	input.Rotation.Tip = strings.TrimSpace(input.Rotation.Tip)
	input.Rotation.Tree = strings.TrimSpace(input.Rotation.Tree)
	input.Rotation.RotationHash = strings.TrimSpace(input.Rotation.RotationHash)
	validRotation := input.Rotation.Version >= 0 &&
		input.Rotation.Version <= MaxPRDevelopmentControllerFences &&
		input.Rotation.MutationEpoch >= 0 &&
		input.Rotation.MutationEpoch <= MaxPRDevelopmentControllerFences+1 &&
		validPRDevelopmentHex(input.Rotation.RotationHash, sha256.Size*2)
	if input.Rotation.Bound {
		validRotation = validRotation && validSameWidthPRDevelopmentOIDs(
			input.Rotation.Tip, input.Rotation.Tree,
		) && input.Rotation.MutationEpoch == input.Rotation.Version+1
	} else {
		validRotation = validRotation && input.Rotation.Version == 0 &&
			input.Rotation.MutationEpoch == 0 && input.Rotation.Tip == "" &&
			input.Rotation.Tree == ""
	}
	if !validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(input.RecoveryID, prDevelopmentRecoveryIntentIDPrefix) ||
		input.ExpectedRevision < 2 ||
		input.ExpectedRevision > MaxPRDevelopmentControllerRevision ||
		input.ClaimEpoch < 1 || input.Lease <= 0 || !validRotation {
		return PRDevelopmentControllerRecoveryFinalize{}, fmt.Errorf(
			"%w: invalid recovery finalization proof",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func loadActivePRDevelopmentRecoveryIntent(
	ctx context.Context,
	queryer rowsQueryer,
	controllerID string,
) (PRDevelopmentControllerRecoveryIntent, bool, error) {
	intent, err := scanPRDevelopmentRecoveryIntent(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentRecoveryIntentColumns+`
		FROM pr_development_controller_recovery_intents
		WHERE controller_id = ? AND status <> 'finalized'`, controllerID))
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentControllerRecoveryIntent{}, false, nil
	}
	if err != nil {
		return PRDevelopmentControllerRecoveryIntent{}, false, err
	}
	return intent, true, nil
}

func loadPRDevelopmentRecoveryIntentByID(
	ctx context.Context,
	queryer rowsQueryer,
	intentID string,
) (PRDevelopmentControllerRecoveryIntent, bool, error) {
	intent, err := scanPRDevelopmentRecoveryIntent(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentRecoveryIntentColumns+`
		FROM pr_development_controller_recovery_intents
		WHERE id = ?`, intentID))
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentControllerRecoveryIntent{}, false, nil
	}
	if err != nil {
		return PRDevelopmentControllerRecoveryIntent{}, false, err
	}
	return intent, true, nil
}

func loadPRDevelopmentRecoveryIntents(
	ctx context.Context,
	queryer rowsQueryer,
	controllerID string,
) ([]PRDevelopmentControllerRecoveryIntent, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT `+prDevelopmentRecoveryIntentColumns+`
		FROM pr_development_controller_recovery_intents
		WHERE controller_id = ?
		ORDER BY ordinal ASC`, controllerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	intents := make([]PRDevelopmentControllerRecoveryIntent, 0)
	for rows.Next() {
		intent, scanErr := scanPRDevelopmentRecoveryIntent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		intents = append(intents, intent)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return intents, nil
}

type prDevelopmentControllerRecoveryStats struct {
	finalizedByAttempt map[string]int
	active             *PRDevelopmentControllerRecoveryIntent
}

type prDevelopmentRecoveryFenceOwner struct {
	controllerID string
	attemptID    string
}

func validatePRDevelopmentControllerRecoveryChain(
	ctx context.Context,
	queryer rowsQueryer,
	controller PRDevelopmentController,
	session PRDevelopmentRepairSession,
	fences []PRDevelopmentAttemptReviewFence,
	attemptOrdinals map[string]int,
) (prDevelopmentControllerRecoveryStats, error) {
	stats := prDevelopmentControllerRecoveryStats{
		finalizedByAttempt: make(map[string]int),
	}
	intents, err := loadPRDevelopmentRecoveryIntents(ctx, queryer, controller.ID)
	if err != nil {
		return stats, err
	}
	activeReservationOwners := make(map[string]string)
	controllerRows, err := queryer.QueryContext(ctx, `
		SELECT id, mutation_reservation_key
		FROM pr_development_thread_controllers
		WHERE mutation_reservation_key <> ''`)
	if err != nil {
		return stats, err
	}
	defer controllerRows.Close()
	for controllerRows.Next() {
		var controllerID, reservation string
		if scanErr := controllerRows.Scan(&controllerID, &reservation); scanErr != nil {
			return stats, scanErr
		}
		digest := prDevelopmentMutationReservationDigest(reservation)
		if _, duplicate := activeReservationOwners[digest]; duplicate {
			return stats, errors.New(
				"stored active controller reservation digest is not unique",
			)
		}
		activeReservationOwners[digest] = controllerID
	}
	if rowsErr := controllerRows.Err(); rowsErr != nil {
		return stats, rowsErr
	}
	if closeErr := controllerRows.Close(); closeErr != nil {
		return stats, closeErr
	}
	retiredReservationOwners := make(map[string]prDevelopmentRecoveryFenceOwner)
	fenceRows, err := queryer.QueryContext(ctx, `
		SELECT controller_id, attempt_id, mutation_reservation_digest
		FROM pr_development_attempt_review_fences`)
	if err != nil {
		return stats, err
	}
	defer fenceRows.Close()
	for fenceRows.Next() {
		var owner prDevelopmentRecoveryFenceOwner
		var digest string
		if scanErr := fenceRows.Scan(
			&owner.controllerID,
			&owner.attemptID,
			&digest,
		); scanErr != nil {
			return stats, scanErr
		}
		if _, duplicate := retiredReservationOwners[digest]; duplicate {
			return stats, errors.New(
				"stored retired controller reservation digest is not unique",
			)
		}
		retiredReservationOwners[digest] = owner
	}
	if rowsErr := fenceRows.Err(); rowsErr != nil {
		return stats, rowsErr
	}
	if closeErr := fenceRows.Close(); closeErr != nil {
		return stats, closeErr
	}
	repairReservationOwners := make(map[string]string)
	repairRows, err := queryer.QueryContext(ctx, `
		SELECT id, reservation_key
		FROM pr_development_repair_sessions`)
	if err != nil {
		return stats, err
	}
	defer repairRows.Close()
	for repairRows.Next() {
		var sessionID, reservation string
		if scanErr := repairRows.Scan(&sessionID, &reservation); scanErr != nil {
			return stats, scanErr
		}
		digest := prDevelopmentMutationReservationDigest(reservation)
		if _, duplicate := repairReservationOwners[digest]; duplicate {
			return stats, errors.New(
				"stored repair reservation digest is not unique",
			)
		}
		repairReservationOwners[digest] = sessionID
	}
	if rowsErr := repairRows.Err(); rowsErr != nil {
		return stats, rowsErr
	}
	if closeErr := repairRows.Close(); closeErr != nil {
		return stats, closeErr
	}
	expectedPreviousHash := emptyPRDevelopmentRecoveryDigest()
	var (
		previousIntent      *PRDevelopmentControllerRecoveryIntent
		previousCreatedAt   time.Time
		previousFinalizedAt time.Time
	)
	latestIntentByAttempt := make(map[string]PRDevelopmentControllerRecoveryIntent)
	for ordinal := range intents {
		intent := intents[ordinal]
		if _, owned := attemptOrdinals[intent.AttemptID]; !owned ||
			intent.ControllerID != controller.ID || intent.Ordinal != ordinal ||
			intent.AgentID != controller.AgentID || intent.LineID != controller.LineID ||
			intent.SourceCloneURL != session.CloneURL ||
			intent.SourceRef != session.HeadRef || intent.SourceCommit != session.HeadSHA ||
			intent.PreviousHash != expectedPreviousHash ||
			intent.CreatedAt.Before(controller.CreatedAt) ||
			(!previousCreatedAt.IsZero() && intent.CreatedAt.Before(previousCreatedAt)) ||
			(!previousFinalizedAt.IsZero() && intent.CreatedAt.Before(previousFinalizedAt)) ||
			intent.RecoveryRevision > controller.Revision {
			return stats, errors.New("stored controller recovery chain is invalid")
		}
		priorIntent, hasPriorIntent := latestIntentByAttempt[intent.AttemptID]
		if hasPriorIntent {
			expectedRecoveryRevision := priorIntent.FinalRevision + 1
			if priorIntent.Mode == PRDevelopmentControllerRecoveryUnbound &&
				intent.Mode == PRDevelopmentControllerRecoveryBound {
				expectedRecoveryRevision++
			}
			if priorIntent.Mode == PRDevelopmentControllerRecoveryBound &&
				intent.Mode == PRDevelopmentControllerRecoveryUnbound {
				return stats, errors.New(
					"stored controller recovery cannot become unbound",
				)
			}
			if priorIntent.Status != PRDevelopmentControllerRecoveryFinalized ||
				intent.RecoveryRevision != expectedRecoveryRevision {
				return stats, errors.New(
					"stored controller recovery revision succession is unreachable",
				)
			}
		} else {
			var priorFence *PRDevelopmentAttemptReviewFence
			for fenceIndex := range fences {
				candidate := fences[fenceIndex]
				if attemptOrdinals[candidate.AttemptID] >=
					attemptOrdinals[intent.AttemptID] {
					break
				}
				candidateCopy := candidate
				priorFence = &candidateCopy
			}
			expectedRecoveryRevision := int64(2)
			expectedLeaseEpoch := int64(1)
			if priorFence == nil {
				if intent.Mode == PRDevelopmentControllerRecoveryBound {
					expectedRecoveryRevision++
				}
			} else {
				if priorFence.ReviewedAt == nil ||
					intent.Mode != PRDevelopmentControllerRecoveryBound {
					return stats, errors.New(
						"stored post-review recovery episode is invalid",
					)
				}
				expectedRecoveryRevision = priorFence.ReviewControllerRevision + 4
				expectedLeaseEpoch = priorFence.ReviewLeaseEpoch + 1
			}
			if intent.RecoveryRevision != expectedRecoveryRevision ||
				intent.ExpiredLeaseEpoch != expectedLeaseEpoch {
				return stats, errors.New(
					"stored first recovery revision or lease proof is unreachable",
				)
			}
		}
		previousDigest := intent.PreviousReservationDigest
		if repairOwner, reserved := repairReservationOwners[previousDigest]; reserved &&
			(repairOwner != session.ID || intent.LineVersion != 0) {
			return stats, errors.New(
				"stored recovery predecessor reuses a foreign repair reservation",
			)
		}
		if activeOwner, active := activeReservationOwners[previousDigest]; active && activeOwner != controller.ID {
			return stats, errors.New(
				"stored recovery predecessor is active on another controller",
			)
		}
		replacementDigest := intent.ReplacementReservationDigest
		if _, reserved := repairReservationOwners[replacementDigest]; reserved {
			return stats, errors.New(
				"stored recovery replacement reuses a repair reservation",
			)
		}
		if activeOwner, active := activeReservationOwners[replacementDigest]; active && activeOwner != controller.ID {
			return stats, errors.New(
				"stored recovery replacement is active on another controller",
			)
		}
		if _, retired := retiredReservationOwners[previousDigest]; retired {
			return stats, errors.New(
				"stored recovery predecessor was already retired by a fence",
			)
		}
		retiredOwner, retired := retiredReservationOwners[replacementDigest]
		foreignRetirement := retiredOwner.controllerID != controller.ID ||
			retiredOwner.attemptID != intent.AttemptID
		if retired && foreignRetirement {
			return stats, errors.New(
				"stored recovery replacement was retired by a foreign fence",
			)
		}
		var (
			predecessorController string
			predecessorAttempt    string
			predecessorOrdinal    int
		)
		predecessorErr := queryer.QueryRowContext(ctx, `
			SELECT controller_id, attempt_id, ordinal
			FROM pr_development_controller_recovery_intents
			WHERE replacement_reservation_digest = ?`,
			intent.PreviousReservationDigest,
		).Scan(&predecessorController, &predecessorAttempt, &predecessorOrdinal)
		if predecessorErr != nil && !errors.Is(predecessorErr, sql.ErrNoRows) {
			return stats, predecessorErr
		}
		if errors.Is(predecessorErr, sql.ErrNoRows) && intent.LineVersion == 0 &&
			intent.PreviousReservationDigest !=
				prDevelopmentMutationReservationDigest(session.ReservationKey) {
			return stats, errors.New(
				"stored initial recovery does not descend from the owner reservation",
			)
		}
		if predecessorErr == nil &&
			(predecessorController != controller.ID ||
				predecessorAttempt != intent.AttemptID ||
				predecessorOrdinal+1 != intent.Ordinal) {
			return stats, errors.New(
				"stored recovery authority has a foreign predecessor",
			)
		}
		var (
			successorController string
			successorAttempt    string
			successorOrdinal    int
		)
		successorErr := queryer.QueryRowContext(ctx, `
			SELECT controller_id, attempt_id, ordinal
			FROM pr_development_controller_recovery_intents
			WHERE previous_reservation_digest = ?`,
			intent.ReplacementReservationDigest,
		).Scan(&successorController, &successorAttempt, &successorOrdinal)
		if successorErr != nil && !errors.Is(successorErr, sql.ErrNoRows) {
			return stats, successorErr
		}
		if successorErr == nil &&
			(successorController != controller.ID ||
				successorAttempt != intent.AttemptID ||
				successorOrdinal != intent.Ordinal+1) {
			return stats, errors.New(
				"stored recovery authority has a foreign successor",
			)
		}
		if previousIntent != nil && intent.AttemptID == previousIntent.AttemptID {
			if previousIntent.Status != PRDevelopmentControllerRecoveryFinalized ||
				intent.PreviousReservationDigest !=
					previousIntent.ReplacementReservationDigest ||
				intent.ExpiredLeaseEpoch != previousIntent.NewMutationLeaseEpoch {
				return stats, errors.New(
					"stored controller recovery reservation succession is invalid",
				)
			}
			if intent.ExpiredLeaseTokenDigest !=
				previousIntent.NewMutationLeaseTokenDigest {
				return stats, errors.New(
					"stored controller recovery lease proof succession is invalid",
				)
			}
			if previousIntent.NewMutationLeaseUntil == nil ||
				intent.CreatedAt.Before(*previousIntent.NewMutationLeaseUntil) {
				return stats, errors.New(
					"stored controller recovery lease deadline succession is invalid",
				)
			}
		} else if previousIntent != nil {
			if attemptOrdinals[intent.AttemptID] <=
				attemptOrdinals[previousIntent.AttemptID] ||
				intent.PreviousReservationDigest ==
					previousIntent.ReplacementReservationDigest {
				return stats, errors.New(
					"stored controller recovery reused prior-attempt authority",
				)
			}
		}
		if intent.Status != PRDevelopmentControllerRecoveryFinalized &&
			validPrefixedHexID(
				intent.PreviousReservationKey,
				prDevelopmentRepairReservationPrefix,
			) && (ordinal != 0 || intent.PreviousReservationDigest !=
			prDevelopmentMutationReservationDigest(session.ReservationKey)) {
			return stats, errors.New(
				"stored controller recovery reused the initial repair reservation",
			)
		}
		if intent.Mode == PRDevelopmentControllerRecoveryUnbound {
			if intent.WorkspaceID != session.WorkspaceID {
				return stats, errors.New(
					"stored unbound controller recovery owner is invalid",
				)
			}
		} else {
			if controller.WorkspaceID == "" || intent.WorkspaceID != controller.WorkspaceID ||
				intent.SourceTree != controller.SourceTree ||
				intent.LineVersion > int64(len(fences)) {
				return stats, errors.New(
					"stored bound controller recovery owner is invalid",
				)
			}
			expectedTip := controller.SourceCommit
			expectedTree := controller.SourceTree
			if intent.LineVersion > 0 {
				priorFence := fences[intent.LineVersion-1]
				expectedTip = priorFence.TipCommit
				expectedTree = priorFence.Tree
			}
			if intent.TipCommit != expectedTip || intent.Tree != expectedTree ||
				intent.MutationEpoch != intent.LineVersion+1 {
				return stats, errors.New(
					"stored bound controller recovery line snapshot is invalid",
				)
			}
		}
		if intent.Status == PRDevelopmentControllerRecoveryFinalized {
			if intent.FinalizedAt == nil || intent.FinalRevision > controller.Revision ||
				intent.FinalizedAt.After(controller.UpdatedAt) {
				return stats, errors.New(
					"stored finalized controller recovery exceeds controller high-water state",
				)
			}
			stats.finalizedByAttempt[intent.AttemptID]++
			expectedPreviousHash = intent.FinalHash
			previousFinalizedAt = *intent.FinalizedAt
		} else {
			if ordinal != len(intents)-1 || stats.active != nil {
				return stats, errors.New(
					"stored unresolved controller recovery is not the chain tail",
				)
			}
			active := intent
			stats.active = &active
		}
		intentCopy := intent
		previousIntent = &intentCopy
		latestIntentByAttempt[intent.AttemptID] = intent
		previousCreatedAt = intent.CreatedAt
	}
	if err := validatePRDevelopmentActiveReservationRecoveryHistory(
		ctx,
		queryer,
		controller,
		intents,
		stats.active,
	); err != nil {
		return stats, err
	}
	if controller.Phase == PRDevelopmentControllerRecoveryRequired {
		if stats.active == nil {
			if len(intents) != 0 {
				return stats, errors.New(
					"stored recovery-required controller lost its active intent",
				)
			}
			// A v11 recovery row has no trustworthy expired-token proof. It
			// remains readable for operators but Claim refuses to recover it.
			return stats, nil
		}
		active := *stats.active
		if active.RecoveryRevision != controller.Revision ||
			active.AttemptID != controller.CurrentAttemptID ||
			active.ExpiredLeaseEpoch != controller.LeaseEpoch ||
			active.PreviousReservationKey != controller.MutationReservationKey ||
			active.CreatedAt != controller.UpdatedAt {
			return stats, errors.New(
				"stored active controller recovery differs from controller state",
			)
		}
		if (active.Mode == PRDevelopmentControllerRecoveryUnbound &&
			controller.WorkspaceID != "") ||
			(active.Mode == PRDevelopmentControllerRecoveryBound &&
				(controller.WorkspaceID != active.WorkspaceID ||
					controller.LineVersion != active.LineVersion ||
					controller.MutationEpoch != active.MutationEpoch ||
					controller.TipCommit != active.TipCommit ||
					controller.Tree != active.Tree)) {
			return stats, errors.New(
				"stored active recovery mode differs from controller line state",
			)
		}
	} else if stats.active != nil {
		return stats, errors.New(
			"stored controller outside recovery has an unresolved recovery intent",
		)
	}
	if controller.Phase == PRDevelopmentControllerMutation {
		for index := len(intents) - 1; index >= 0; index-- {
			intent := intents[index]
			if intent.AttemptID != controller.CurrentAttemptID ||
				intent.Status != PRDevelopmentControllerRecoveryFinalized {
				continue
			}
			if prDevelopmentMutationReservationDigest(
				controller.MutationReservationKey,
			) != intent.ReplacementReservationDigest ||
				controller.LeaseOwner != intent.ClaimOwner ||
				controller.LeaseEpoch != intent.NewMutationLeaseEpoch ||
				prDevelopmentLeaseTokenDigest(
					PRDevelopmentControllerMutationLease,
					controller.LeaseToken,
				) != intent.NewMutationLeaseTokenDigest ||
				controller.LeaseUntil == nil || intent.NewMutationLeaseUntil == nil ||
				controller.LeaseUntil.Before(*intent.NewMutationLeaseUntil) {
				return stats, errors.New(
					"stored recovered mutation does not use its replacement authority",
				)
			}
			break
		}
	}
	for _, fence := range fences {
		var latestFinalized *PRDevelopmentControllerRecoveryIntent
		for _, intent := range intents {
			if intent.AttemptID == fence.AttemptID &&
				intent.Status != PRDevelopmentControllerRecoveryFinalized {
				return stats, errors.New(
					"stored fenced attempt retains unresolved recovery",
				)
			}
			if intent.AttemptID == fence.AttemptID &&
				intent.FinalRevision > fence.MutationControllerRevision {
				return stats, errors.New(
					"stored attempt recovery follows its immutable fence",
				)
			}
			if intent.AttemptID == fence.AttemptID &&
				intent.Status == PRDevelopmentControllerRecoveryFinalized {
				intentCopy := intent
				latestFinalized = &intentCopy
			}
		}
		if latestFinalized != nil &&
			(fence.MutationReservationDigest !=
				latestFinalized.ReplacementReservationDigest ||
				fence.MutationLeaseEpoch != latestFinalized.NewMutationLeaseEpoch ||
				fence.MutationLeaseTokenDigest !=
					latestFinalized.NewMutationLeaseTokenDigest) {
			return stats, errors.New(
				"stored fence did not retire the latest recovered authority",
			)
		}
	}
	return stats, nil
}

func validatePRDevelopmentActiveReservationRecoveryHistory(
	ctx context.Context,
	queryer rowsQueryer,
	controller PRDevelopmentController,
	intents []PRDevelopmentControllerRecoveryIntent,
	active *PRDevelopmentControllerRecoveryIntent,
) error {
	if controller.MutationReservationKey == "" {
		return nil
	}
	digest := prDevelopmentMutationReservationDigest(controller.MutationReservationKey)
	rows, err := queryer.QueryContext(ctx, `
		SELECT controller_id, attempt_id, ordinal, status,
			previous_reservation_digest, replacement_reservation_digest
		FROM pr_development_controller_recovery_intents
		WHERE previous_reservation_digest = ? OR replacement_reservation_digest = ?
		ORDER BY controller_id, ordinal`,
		digest,
		digest,
	)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type recoveryAuthorityMatch struct {
		controllerID       string
		attemptID          string
		ordinal            int
		status             PRDevelopmentControllerRecoveryStatus
		matchesPrevious    bool
		matchesReplacement bool
	}
	matches := make([]recoveryAuthorityMatch, 0, 2)
	for rows.Next() {
		var (
			match                             recoveryAuthorityMatch
			previousDigest, replacementDigest string
		)
		if scanErr := rows.Scan(
			&match.controllerID,
			&match.attemptID,
			&match.ordinal,
			&match.status,
			&previousDigest,
			&replacementDigest,
		); scanErr != nil {
			return scanErr
		}
		if match.controllerID != controller.ID {
			return errors.New(
				"stored active controller reservation appears in foreign recovery history",
			)
		}
		match.matchesPrevious = previousDigest == digest
		match.matchesReplacement = replacementDigest == digest
		matches = append(matches, match)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return rowsErr
	}
	if len(matches) == 0 {
		return nil
	}
	switch controller.Phase {
	case PRDevelopmentControllerMutation:
		var latest *PRDevelopmentControllerRecoveryIntent
		for index := len(intents) - 1; index >= 0; index-- {
			candidate := intents[index]
			if candidate.AttemptID == controller.CurrentAttemptID &&
				candidate.Status == PRDevelopmentControllerRecoveryFinalized {
				candidateCopy := candidate
				latest = &candidateCopy
				break
			}
		}
		if latest == nil || len(matches) != 1 ||
			matches[0].attemptID != controller.CurrentAttemptID ||
			matches[0].ordinal != latest.Ordinal ||
			matches[0].status != PRDevelopmentControllerRecoveryFinalized ||
			matches[0].matchesPrevious || !matches[0].matchesReplacement ||
			latest.ReplacementReservationDigest != digest {
			return errors.New(
				"stored active controller reservation is not its latest recovered authority",
			)
		}
	case PRDevelopmentControllerRecoveryRequired:
		if active == nil || active.AttemptID != controller.CurrentAttemptID ||
			active.PreviousReservationDigest != digest {
			return errors.New(
				"stored recovery controller reservation is not its active predecessor",
			)
		}
		for _, match := range matches {
			activePredecessor := match.attemptID == active.AttemptID &&
				match.ordinal == active.Ordinal &&
				match.status != PRDevelopmentControllerRecoveryFinalized &&
				match.matchesPrevious && !match.matchesReplacement
			priorReplacement := active.Ordinal > 0 &&
				match.attemptID == active.AttemptID &&
				match.ordinal == active.Ordinal-1 &&
				match.status == PRDevelopmentControllerRecoveryFinalized &&
				!match.matchesPrevious && match.matchesReplacement
			if !activePredecessor && !priorReplacement {
				return errors.New(
					"stored recovery controller reservation reactivates stale authority",
				)
			}
		}
	default:
		return errors.New(
			"stored controller reservation has recovery history outside mutation",
		)
	}
	return nil
}

func scanPRDevelopmentRecoveryIntent(
	scanner rowScanner,
) (PRDevelopmentControllerRecoveryIntent, error) {
	var (
		intent PRDevelopmentControllerRecoveryIntent
		claimUntil, newMutationLeaseUntil,
		claimedAt, finalizedAt sql.NullInt64
		claims               int64
		createdAt, updatedAt int64
	)
	if err := scanner.Scan(
		&intent.ID,
		&intent.ControllerID,
		&intent.AttemptID,
		&intent.Ordinal,
		&intent.RecoveryRevision,
		&intent.Mode,
		&intent.Status,
		&intent.AgentID,
		&intent.WorkspaceID,
		&intent.LineID,
		&intent.SourceCloneURL,
		&intent.SourceRef,
		&intent.SourceCommit,
		&intent.SourceTree,
		&intent.LineVersion,
		&intent.MutationEpoch,
		&intent.TipCommit,
		&intent.Tree,
		&intent.PreviousReservationKey,
		&intent.ReplacementReservationKey,
		&intent.PreviousReservationDigest,
		&intent.ReplacementReservationDigest,
		&intent.ExpiredControllerRevision,
		&intent.ExpiredLeaseEpoch,
		&intent.ExpiredLeaseTokenDigest,
		&intent.PreviousHash,
		&intent.IntentHash,
		&intent.ClaimID,
		&intent.ClaimOwner,
		&intent.ClaimToken,
		&claimUntil,
		&intent.ClaimEpoch,
		&claims,
		&intent.RotationResultHash,
		&intent.RecoveryClaimTokenDigest,
		&intent.NewMutationLeaseEpoch,
		&intent.NewMutationLeaseTokenDigest,
		&newMutationLeaseUntil,
		&intent.FinalRevision,
		&intent.FinalHash,
		&createdAt,
		&claimedAt,
		&finalizedAt,
		&updatedAt,
	); err != nil {
		return PRDevelopmentControllerRecoveryIntent{}, err
	}
	intent.Claims = int(claims)
	if int64(intent.Claims) != claims {
		return PRDevelopmentControllerRecoveryIntent{}, errors.New(
			"stored recovery claim count overflows",
		)
	}
	intent.ClaimUntil = fromNullableTime(claimUntil)
	intent.NewMutationLeaseUntil = fromNullableTime(newMutationLeaseUntil)
	intent.ClaimedAt = fromNullableTime(claimedAt)
	intent.FinalizedAt = fromNullableTime(finalizedAt)
	intent.CreatedAt = fromDBTime(createdAt)
	intent.UpdatedAt = fromDBTime(updatedAt)
	if err := validateStoredPRDevelopmentRecoveryIntent(intent); err != nil {
		return PRDevelopmentControllerRecoveryIntent{}, fmt.Errorf(
			"invalid stored pull request development recovery intent: %w",
			err,
		)
	}
	return intent, nil
}

func validateStoredPRDevelopmentRecoveryIntent(
	intent PRDevelopmentControllerRecoveryIntent,
) error {
	if !validPrefixedHexID(intent.ID, prDevelopmentRecoveryIntentIDPrefix) ||
		!validPrefixedHexID(intent.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(intent.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		intent.Ordinal < 0 || intent.Ordinal >= MaxPRDevelopmentControllerRecoveries ||
		intent.RecoveryRevision < 2 ||
		intent.RecoveryRevision > MaxPRDevelopmentControllerRevision ||
		intent.ExpiredControllerRevision != intent.RecoveryRevision-1 ||
		intent.ExpiredLeaseEpoch < 1 ||
		!validPRDevelopmentRepairAgentID(intent.AgentID) ||
		!validPRDevelopmentRepairIdentity(
			intent.WorkspaceID, MaxPRDevelopmentControllerIdentityBytes,
		) ||
		!validPrefixedHexID(intent.LineID, prDevelopmentLineIDPrefix) ||
		!validPRDevelopmentRepairCloneURL(intent.SourceCloneURL) ||
		!validPRDevelopmentGitRef(intent.SourceRef) ||
		!validPRDevelopmentHex(intent.SourceCommit, 40, 64) ||
		!validPRDevelopmentHex(intent.PreviousReservationDigest, sha256.Size*2) ||
		!validPRDevelopmentHex(intent.ReplacementReservationDigest, sha256.Size*2) ||
		intent.PreviousReservationDigest == intent.ReplacementReservationDigest ||
		!validPRDevelopmentHex(intent.ExpiredLeaseTokenDigest, sha256.Size*2) ||
		!validPRDevelopmentHex(intent.PreviousHash, sha256.Size*2) ||
		!validPRDevelopmentHex(intent.IntentHash, sha256.Size*2) ||
		intent.IntentHash != hashPRDevelopmentRecoveryIntent(intent) ||
		intent.ClaimEpoch < 0 || intent.Claims != int(intent.ClaimEpoch) ||
		validateDBTimestamp("recovery creation time", intent.CreatedAt) != nil ||
		validateDBTimestamp("recovery update time", intent.UpdatedAt) != nil ||
		intent.UpdatedAt.Before(intent.CreatedAt) {
		return errors.New("stored recovery header is invalid")
	}
	if intent.Mode == PRDevelopmentControllerRecoveryUnbound {
		if intent.SourceTree != "" || intent.LineVersion != 0 ||
			intent.MutationEpoch != 0 || intent.TipCommit != "" || intent.Tree != "" {
			return errors.New("stored unbound recovery snapshot is invalid")
		}
	} else if intent.Mode != PRDevelopmentControllerRecoveryBound ||
		!validSameWidthPRDevelopmentOIDs(
			intent.SourceCommit, intent.SourceTree, intent.TipCommit, intent.Tree,
		) || intent.LineVersion < 0 ||
		intent.LineVersion > MaxPRDevelopmentControllerFences ||
		intent.MutationEpoch != intent.LineVersion+1 {
		return errors.New("stored bound recovery snapshot is invalid")
	}
	if intent.Status != PRDevelopmentControllerRecoveryFinalized {
		if (!validPrefixedHexID(
			intent.PreviousReservationKey, prDevelopmentControllerKeyPrefix,
		) && !validPrefixedHexID(
			intent.PreviousReservationKey, prDevelopmentRepairReservationPrefix,
		)) || !validPrefixedHexID(
			intent.ReplacementReservationKey, prDevelopmentControllerKeyPrefix,
		) || prDevelopmentMutationReservationDigest(
			intent.PreviousReservationKey,
		) != intent.PreviousReservationDigest ||
			prDevelopmentMutationReservationDigest(
				intent.ReplacementReservationKey,
			) != intent.ReplacementReservationDigest {
			return errors.New("stored recovery raw reservation evidence is invalid")
		}
	}
	switch intent.Status {
	case PRDevelopmentControllerRecoveryPending:
		if intent.ClaimID != "" || intent.ClaimOwner != "" || intent.ClaimToken != "" ||
			intent.ClaimUntil != nil || intent.ClaimEpoch != 0 || intent.ClaimedAt != nil ||
			intent.RotationResultHash != "" || intent.RecoveryClaimTokenDigest != "" ||
			intent.NewMutationLeaseEpoch != 0 || intent.NewMutationLeaseTokenDigest != "" ||
			intent.NewMutationLeaseUntil != nil || intent.FinalRevision != 0 ||
			intent.FinalizedAt != nil || intent.FinalHash != "" {
			return errors.New("stored pending recovery claim state is invalid")
		}
	case PRDevelopmentControllerRecoveryClaimed:
		if !validPRDevelopmentRepairIdentity(
			intent.ClaimID, MaxPRDevelopmentControllerIdentityBytes,
		) || !validPRDevelopmentRepairIdentity(
			intent.ClaimOwner, MaxPRDevelopmentControllerIdentityBytes,
		) || !validPRDevelopmentRepairIdentity(
			intent.ClaimToken, prDevelopmentControllerLeaseTokenBytes,
		) || intent.ClaimUntil == nil || intent.ClaimedAt == nil ||
			intent.ClaimEpoch < 1 ||
			validateDBTimestamp("recovery claim deadline", *intent.ClaimUntil) != nil ||
			validateDBTimestamp("recovery claim time", *intent.ClaimedAt) != nil ||
			intent.ClaimedAt.Before(intent.CreatedAt) ||
			!intent.ClaimUntil.After(intent.UpdatedAt) ||
			intent.UpdatedAt.Before(*intent.ClaimedAt) ||
			intent.RotationResultHash != "" || intent.RecoveryClaimTokenDigest != "" ||
			intent.NewMutationLeaseEpoch != 0 || intent.NewMutationLeaseTokenDigest != "" ||
			intent.NewMutationLeaseUntil != nil || intent.FinalRevision != 0 ||
			intent.FinalizedAt != nil || intent.FinalHash != "" {
			return errors.New("stored claimed recovery state is invalid")
		}
	case PRDevelopmentControllerRecoveryFinalized:
		if intent.PreviousReservationKey != "" || intent.ReplacementReservationKey != "" ||
			!validPRDevelopmentRepairIdentity(
				intent.ClaimID, MaxPRDevelopmentControllerIdentityBytes,
			) || !validPRDevelopmentRepairIdentity(
			intent.ClaimOwner, MaxPRDevelopmentControllerIdentityBytes,
		) || intent.ClaimToken != "" || intent.ClaimUntil != nil ||
			intent.ClaimedAt == nil || intent.FinalizedAt == nil || intent.ClaimEpoch < 1 ||
			!validPRDevelopmentHex(intent.RotationResultHash, sha256.Size*2) ||
			!validPRDevelopmentHex(intent.RecoveryClaimTokenDigest, sha256.Size*2) ||
			intent.NewMutationLeaseEpoch != intent.ExpiredLeaseEpoch+1 ||
			!validPRDevelopmentHex(intent.NewMutationLeaseTokenDigest, sha256.Size*2) ||
			intent.NewMutationLeaseUntil == nil ||
			validateDBTimestamp(
				"new mutation lease deadline", *intent.NewMutationLeaseUntil,
			) != nil ||
			intent.FinalRevision != intent.RecoveryRevision+1 ||
			!validPRDevelopmentHex(intent.FinalHash, sha256.Size*2) ||
			intent.FinalHash != hashPRDevelopmentRecoveryFinal(
				intent,
				intent.RotationResultHash,
				intent.RecoveryClaimTokenDigest,
				intent.NewMutationLeaseEpoch,
				intent.NewMutationLeaseTokenDigest,
				*intent.NewMutationLeaseUntil,
				intent.FinalRevision,
				*intent.FinalizedAt,
			) || validateDBTimestamp("recovery claim time", *intent.ClaimedAt) != nil ||
			validateDBTimestamp("recovery finalization time", *intent.FinalizedAt) != nil ||
			intent.FinalizedAt.Before(*intent.ClaimedAt) ||
			!intent.NewMutationLeaseUntil.After(*intent.FinalizedAt) ||
			intent.UpdatedAt.Before(*intent.FinalizedAt) {
			return errors.New("stored finalized recovery state is invalid")
		}
	default:
		return errors.New("stored recovery status is invalid")
	}
	return nil
}

func emptyPRDevelopmentRecoveryDigest() string {
	sum := sha256.Sum256([]byte("picoclaw/pr-development-controller-recovery/v1/empty"))
	return hex.EncodeToString(sum[:])
}

func hashPRDevelopmentRecoveryIntent(
	intent PRDevelopmentControllerRecoveryIntent,
) string {
	digest := sha256.New()
	for _, value := range []string{
		"picoclaw/pr-development-controller-recovery/v1/intent",
		intent.ID,
		intent.ControllerID,
		intent.AttemptID,
		fmt.Sprintf("%d", intent.Ordinal),
		fmt.Sprintf("%d", intent.RecoveryRevision),
		string(intent.Mode),
		intent.AgentID,
		intent.WorkspaceID,
		intent.LineID,
		intent.SourceCloneURL,
		intent.SourceRef,
		intent.SourceCommit,
		intent.SourceTree,
		fmt.Sprintf("%d", intent.LineVersion),
		fmt.Sprintf("%d", intent.MutationEpoch),
		intent.TipCommit,
		intent.Tree,
		intent.PreviousReservationDigest,
		intent.ReplacementReservationDigest,
		fmt.Sprintf("%d", intent.ExpiredControllerRevision),
		fmt.Sprintf("%d", intent.ExpiredLeaseEpoch),
		intent.ExpiredLeaseTokenDigest,
		intent.PreviousHash,
		fmt.Sprintf("%d", toDBTime(intent.CreatedAt)),
	} {
		writePRDevelopmentControllerHashField(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func hashPRDevelopmentRecoveryRotationResult(
	result PRDevelopmentControllerRecoveryRotationResult,
) string {
	digest := sha256.New()
	for _, value := range []string{
		"picoclaw/pr-development-controller-recovery/v1/rotation-result",
		result.WorkspaceID,
		fmt.Sprintf("%t", result.Bound),
		fmt.Sprintf("%d", result.Version),
		fmt.Sprintf("%d", result.MutationEpoch),
		result.Tip,
		result.Tree,
		result.RotationHash,
	} {
		writePRDevelopmentControllerHashField(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func hashPRDevelopmentRecoveryFinal(
	intent PRDevelopmentControllerRecoveryIntent,
	rotationResultHash, claimTokenDigest string,
	mutationLeaseEpoch int64,
	mutationLeaseTokenDigest string,
	mutationLeaseUntil time.Time,
	finalRevision int64,
	finalizedAt time.Time,
) string {
	digest := sha256.New()
	claimedAt := ""
	if intent.ClaimedAt != nil {
		claimedAt = fmt.Sprintf("%d", toDBTime(*intent.ClaimedAt))
	}
	for _, value := range []string{
		"picoclaw/pr-development-controller-recovery/v1/final",
		intent.IntentHash,
		intent.ClaimID,
		intent.ClaimOwner,
		fmt.Sprintf("%d", intent.ClaimEpoch),
		fmt.Sprintf("%d", intent.Claims),
		claimedAt,
		rotationResultHash,
		claimTokenDigest,
		fmt.Sprintf("%d", mutationLeaseEpoch),
		mutationLeaseTokenDigest,
		fmt.Sprintf("%d", toDBTime(mutationLeaseUntil)),
		fmt.Sprintf("%d", finalRevision),
		fmt.Sprintf("%d", toDBTime(finalizedAt)),
	} {
		writePRDevelopmentControllerHashField(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func prDevelopmentRecoveryClaimTokenDigest(token string) string {
	digest := sha256.New()
	writePRDevelopmentControllerHashField(
		digest,
		"picoclaw/pr-development-controller-recovery/v1/claim-token",
	)
	writePRDevelopmentControllerHashField(digest, token)
	return hex.EncodeToString(digest.Sum(nil))
}

func equalPRDevelopmentRecoveryRotationResult(
	intent PRDevelopmentControllerRecoveryIntent,
	result PRDevelopmentControllerRecoveryRotationResult,
) bool {
	bound := intent.Mode == PRDevelopmentControllerRecoveryBound
	return result.WorkspaceID == intent.WorkspaceID && result.Bound == bound &&
		result.Version == intent.LineVersion &&
		result.MutationEpoch == intent.MutationEpoch &&
		result.Tip == intent.TipCommit && result.Tree == intent.Tree
}
