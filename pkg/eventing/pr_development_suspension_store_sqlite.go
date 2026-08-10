//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const prDevelopmentControllerSuspensionColumns = `
	id, controller_id, thread_id, owner_session_id, attempt_id, ordinal,
	source_kind, source_recovery_id, source_operation_id, source_operation_kind,
	source_final_revision, source_final_hash, mode, status, agent_id,
	workspace_id, line_id, source_clone_url, source_ref, source_commit,
	source_tree, line_version, mutation_epoch, tip_commit, tree,
	suspension_reservation_key, suspension_reservation_digest,
	mutation_lease_epoch, mutation_lease_token_digest, suspend_intent_id,
	suspend_request_json, suspend_request_hash, previous_hash, intent_hash,
	suspend_claim_id, suspend_claim_owner, suspend_claim_token,
	suspend_claim_until, suspend_claim_epoch, suspend_claims, suspend_claimed_at,
	suspend_claim_token_digest, suspend_result_json, suspend_result_hash,
	candidate_tree, candidate_digest, changed_file_count, suspension_hash,
	prepared_commit, prepared_tree, prepared_commit_applied,
	final_suspension_revision, suspension_final_hash, suspended_at,
	resume_attempt_id, resume_intent_id, resume_reservation_key,
	resume_reservation_digest, resume_request_json, resume_request_hash,
	resume_intent_hash, resume_prepared_at, resume_claim_id, resume_claim_owner,
	resume_claim_token, resume_claim_until, resume_claim_epoch, resume_claims,
	resume_claimed_at, resume_claim_token_digest, resume_result_json,
	resume_result_hash, rotation_hash, new_mutation_lease_epoch,
	new_mutation_lease_token_digest, new_mutation_lease_until,
	final_resume_revision, resume_final_hash, resumed_at, created_at, updated_at`

const maxPRDevelopmentSuspensionChangedFiles = 1000

var _ PRDevelopmentControllerSuspensionExecutionStore = (*Store)(nil)

type stagePRDevelopmentControllerSuspensionInput struct {
	ControllerID        string
	AttemptID           string
	SourceKind          PRDevelopmentControllerSuspensionSourceKind
	SourceRecoveryID    string
	SourceOperationID   string
	SourceFinalRevision int64
	SourceFinalHash     string
}

type prDevelopmentControllerSuspensionStageSource struct {
	controllerID                 string
	attemptID                    string
	operationID                  string
	operationKind                PRDevelopmentControllerOperationKind
	mode                         PRDevelopmentControllerSuspensionMode
	agentID                      string
	workspaceID                  string
	lineID                       string
	sourceCloneURL               string
	sourceRef                    string
	sourceCommit                 string
	sourceTree                   string
	lineVersion                  int64
	mutationEpoch                int64
	tipCommit                    string
	tree                         string
	replacementReservationDigest string
	newMutationLeaseEpoch        int64
	newMutationLeaseTokenDigest  string
	newMutationLeaseUntil        *time.Time
	claimOwner                   string
	finalizedAt                  *time.Time
	commitRequest                *PRDevelopmentControllerOperationRequest
	commitResult                 *PRDevelopmentControllerOperationResult
}

// NextPRDevelopmentControllerSuspension returns the oldest exact suspension
// that is pending or whose prior worker claim expired. It intentionally omits
// both the reservation bearer and any claim credential.
func (s *Store) NextPRDevelopmentControllerSuspension(
	ctx context.Context,
) (PRDevelopmentControllerSuspensionWorkCandidate, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentControllerSuspensionWorkCandidate{}, false, err
	}
	var candidate PRDevelopmentControllerSuspensionWorkCandidate
	err := s.withPRDevelopmentConversationReadSnapshot(
		ctx,
		func(queryer rowsQueryer) error {
			now, err := s.currentTime()
			if err != nil {
				return err
			}
			var availableAtRaw int64
			if err = queryer.QueryRowContext(ctx, `
				SELECT suspensions.id, sessions.case_id,
				       COALESCE(suspensions.suspend_claim_until, suspensions.created_at)
				FROM pr_development_controller_suspensions AS suspensions
				JOIN pr_development_repair_attempts AS attempts
				  ON attempts.id = suspensions.attempt_id
				JOIN pr_development_repair_sessions AS sessions
				  ON sessions.id = attempts.session_id
				WHERE suspensions.status = 'suspend_pending' OR
				      (suspensions.status = 'suspend_claimed' AND
				       suspensions.suspend_claim_until <= ?)
				ORDER BY COALESCE(suspensions.suspend_claim_until, suspensions.created_at),
				         suspensions.id
				LIMIT 1`,
				toDBTime(now),
			).Scan(
				&candidate.SuspensionID,
				&candidate.CaseID,
				&availableAtRaw,
			); err != nil {
				return err
			}
			suspension, found, err := loadPRDevelopmentControllerSuspensionByID(
				ctx,
				queryer,
				candidate.SuspensionID,
			)
			if err != nil {
				return err
			}
			if !found {
				return errors.New("claimable controller suspension disappeared")
			}
			controller, found, err := loadPRDevelopmentControllerAggregateByID(
				ctx,
				queryer,
				suspension.ControllerID,
			)
			if err != nil {
				return err
			}
			if !found {
				return errors.New("claimable controller suspension owner disappeared")
			}
			if controller.Phase != PRDevelopmentControllerSuspensionPending ||
				controller.Revision != suspension.SourceFinalRevision+1 ||
				controller.CurrentAttemptID != suspension.AttemptID ||
				(suspension.Status == PRDevelopmentControllerSuspensionStatusSuspendClaimed &&
					(suspension.SuspendClaimUntil == nil ||
						suspension.SuspendClaimUntil.After(now))) {
				return errors.New("claimable controller suspension is not controller-current")
			}
			candidate.ControllerID = suspension.ControllerID
			candidate.AttemptID = suspension.AttemptID
			candidate.SourceKind = suspension.SourceKind
			candidate.Mode = suspension.Mode
			candidate.ExpectedRevision = suspension.SourceFinalRevision + 1
			candidate.AvailableAt = fromDBTime(availableAtRaw)
			return nil
		},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentControllerSuspensionWorkCandidate{}, false, nil
	}
	if err != nil {
		return PRDevelopmentControllerSuspensionWorkCandidate{}, false, fmt.Errorf(
			"next pull request development controller suspension: %w",
			s.dbError(err),
		)
	}
	return candidate, true, nil
}

// ClaimPRDevelopmentControllerSuspension acquires the exact durable Git
// request. A live same-ID/same-owner call is a no-write replay; an expired
// claim rotates the private token and increments its fencing epoch.
func (s *Store) ClaimPRDevelopmentControllerSuspension(
	ctx context.Context,
	input PRDevelopmentControllerSuspensionClaim,
) (PRDevelopmentControllerSuspensionLease, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentControllerSuspensionLease{}, false, err
	}
	normalized, err := normalizePRDevelopmentControllerSuspensionClaim(input)
	if err != nil {
		return PRDevelopmentControllerSuspensionLease{}, false, err
	}
	var (
		lease   PRDevelopmentControllerSuspensionLease
		changed bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		relation, err := loadPRDevelopmentControllerAttemptRelation(
			ctx,
			conn,
			normalized.CaseID,
			normalized.AttemptID,
		)
		if err != nil {
			return err
		}
		suspension, found, err := loadPRDevelopmentControllerSuspensionByID(
			ctx,
			conn,
			normalized.SuspensionID,
		)
		if err != nil {
			return err
		}
		if !found {
			return sql.ErrNoRows
		}
		controller, found, err := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			normalized.ControllerID,
		)
		if err != nil {
			return err
		}
		if !found {
			return sql.ErrNoRows
		}
		if suspension.ControllerID != controller.ID ||
			suspension.AttemptID != normalized.AttemptID ||
			controller.ThreadID != relation.Thread.ID ||
			controller.OwnerSessionID != relation.Session.ID ||
			controller.CurrentAttemptID != normalized.AttemptID ||
			controller.Revision != normalized.ExpectedRevision ||
			normalized.ExpectedRevision != suspension.SourceFinalRevision+1 ||
			controller.Phase != PRDevelopmentControllerSuspensionPending {
			return fmt.Errorf(
				"%w: suspension is no longer the exact controller high-water state",
				ErrPRDevelopmentControllerConflict,
			)
		}
		now, err := s.currentTime()
		if err != nil {
			return err
		}
		if err = requireNonRegressingPRDevelopmentControllerTime(
			now,
			maxPRDevelopmentControllerTime(
				controller.UpdatedAt,
				suspension.UpdatedAt,
				relation.Session.UpdatedAt,
				relation.Attempt.UpdatedAt,
			),
		); err != nil {
			return err
		}
		deadline, err := prDevelopmentControllerDeadline(now, normalized.Lease)
		if err != nil {
			return err
		}
		if suspension.SuspendClaimUntil != nil && suspension.SuspendClaimUntil.After(deadline) {
			deadline = *suspension.SuspendClaimUntil
		}
		if suspension.Status == PRDevelopmentControllerSuspensionStatusSuspendClaimed &&
			suspension.SuspendClaimUntil != nil && suspension.SuspendClaimUntil.After(now) {
			if suspension.SuspendClaimID != normalized.ClaimID ||
				suspension.SuspendClaimOwner != normalized.WorkerLabel {
				return ErrPRDevelopmentControllerActive
			}
			lease = PRDevelopmentControllerSuspensionLease{
				Controller: controller,
				Suspension: suspension,
			}
			return nil
		}
		if suspension.Status == PRDevelopmentControllerSuspensionStatusSuspendClaimed &&
			suspension.SuspendClaimID == normalized.ClaimID &&
			suspension.SuspendClaimOwner != normalized.WorkerLabel {
			return fmt.Errorf(
				"%w: suspension claim ID is bound to another worker",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if suspension.Status != PRDevelopmentControllerSuspensionStatusSuspendPending &&
			suspension.Status != PRDevelopmentControllerSuspensionStatusSuspendClaimed {
			return fmt.Errorf(
				"%w: controller suspension is not claimable",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if suspension.SuspendClaimEpoch == int64(^uint64(0)>>1) {
			return fmt.Errorf(
				"%w: controller suspension claim epoch capacity exhausted",
				ErrPRDevelopmentControllerConflict,
			)
		}
		var duplicate int
		if err = conn.QueryRowContext(ctx, `
			SELECT
				(SELECT COUNT(*) FROM pr_development_controller_recovery_intents
				 WHERE claim_id = ?) +
				(SELECT COUNT(*) FROM pr_development_controller_operation_intents
				 WHERE claim_id = ?) +
				(SELECT COUNT(*) FROM pr_development_controller_suspensions
				 WHERE suspend_claim_id = ? AND id <> ?) +
				(SELECT COUNT(*) FROM pr_development_controller_suspensions
				 WHERE resume_claim_id = ?)`,
			normalized.ClaimID,
			normalized.ClaimID,
			normalized.ClaimID,
			suspension.ID,
			normalized.ClaimID,
		).Scan(&duplicate); err != nil {
			return err
		}
		if duplicate != 0 {
			return fmt.Errorf(
				"%w: suspension claim ID is already bound",
				ErrPRDevelopmentControllerConflict,
			)
		}
		token, err := newLeaseToken(normalized.WorkerLabel)
		if err != nil {
			return err
		}
		reclaimed := suspension.Status == PRDevelopmentControllerSuspensionStatusSuspendClaimed
		result, err := conn.ExecContext(ctx, `
			UPDATE pr_development_controller_suspensions
			SET status = 'suspend_claimed', suspend_claim_id = ?,
				suspend_claim_owner = ?, suspend_claim_token = ?,
				suspend_claim_until = ?, suspend_claim_epoch = suspend_claim_epoch + 1,
				suspend_claims = suspend_claims + 1,
				suspend_claimed_at = COALESCE(suspend_claimed_at, ?), updated_at = ?
			WHERE id = ? AND controller_id = ? AND attempt_id = ? AND
				source_final_revision + 1 = ? AND
				(status = 'suspend_pending' OR
				 (status = 'suspend_claimed' AND suspend_claim_until <= ?))`,
			normalized.ClaimID,
			normalized.WorkerLabel,
			token,
			toDBTime(deadline),
			toDBTime(now),
			toDBTime(now),
			suspension.ID,
			controller.ID,
			normalized.AttemptID,
			controller.Revision,
			toDBTime(now),
		)
		if err != nil {
			return err
		}
		if err = requireOnePRDevelopmentControllerRow(result); err != nil {
			return err
		}
		claimed, found, err := loadPRDevelopmentControllerSuspensionByID(
			ctx,
			conn,
			suspension.ID,
		)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("claimed controller suspension disappeared")
		}
		lease = PRDevelopmentControllerSuspensionLease{
			Controller: controller,
			Suspension: claimed,
			Reclaimed:  reclaimed,
		}
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentControllerSuspensionLease{}, false, fmt.Errorf(
			"claim pull request development controller suspension: %w",
			s.dbError(err),
		)
	}
	return lease, changed, nil
}

// RenewPRDevelopmentControllerSuspension extends no filesystem authority; it
// only keeps the exact worker claim alive while the fixed Git request runs.
func (s *Store) RenewPRDevelopmentControllerSuspension(
	ctx context.Context,
	input PRDevelopmentControllerSuspensionRenew,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	normalized, err := normalizePRDevelopmentControllerSuspensionRenew(input)
	if err != nil {
		return err
	}
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		suspension, found, err := loadPRDevelopmentControllerSuspensionByID(
			ctx,
			conn,
			normalized.SuspensionID,
		)
		if err != nil {
			return err
		}
		if !found {
			return sql.ErrNoRows
		}
		controller, found, err := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			normalized.ControllerID,
		)
		if err != nil {
			return err
		}
		if !found {
			return sql.ErrNoRows
		}
		if suspension.ControllerID != controller.ID ||
			suspension.AttemptID != normalized.AttemptID ||
			controller.Phase != PRDevelopmentControllerSuspensionPending ||
			controller.CurrentAttemptID != normalized.AttemptID ||
			controller.Revision != suspension.SourceFinalRevision+1 {
			return fmt.Errorf(
				"%w: suspension claim is no longer controller-current",
				ErrPRDevelopmentControllerConflict,
			)
		}
		now, err := s.currentTime()
		if err != nil {
			return err
		}
		if err = requireNonRegressingPRDevelopmentControllerTime(
			now,
			maxPRDevelopmentControllerTime(controller.UpdatedAt, suspension.UpdatedAt),
		); err != nil {
			return err
		}
		deadline, err := prDevelopmentControllerDeadline(now, normalized.Lease)
		if err != nil {
			return err
		}
		if suspension.SuspendClaimUntil != nil && suspension.SuspendClaimUntil.After(deadline) {
			deadline = *suspension.SuspendClaimUntil
		}
		result, err := conn.ExecContext(ctx, `
			UPDATE pr_development_controller_suspensions
			SET suspend_claim_until = ?, updated_at = ?
			WHERE id = ? AND controller_id = ? AND attempt_id = ? AND
				status = 'suspend_claimed' AND suspend_claim_id = ? AND
				suspend_claim_token = ? AND suspend_claim_epoch = ? AND
				suspend_claim_until > ?`,
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
		if err != nil {
			return err
		}
		if err = requireOnePRDevelopmentControllerRow(result); err != nil {
			return fmt.Errorf("%w: %v", ErrStaleLease, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf(
			"renew pull request development controller suspension: %w",
			s.dbError(err),
		)
	}
	return nil
}

// FinalizePRDevelopmentControllerSuspension records the exact successful Git
// result, erases every raw suspension bearer, and advances the controller to
// reservation-free suspended state.
func (s *Store) FinalizePRDevelopmentControllerSuspension(
	ctx context.Context,
	input PRDevelopmentControllerSuspensionFinalize,
) (PRDevelopmentControllerSuspensionTransition, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentControllerSuspensionTransition{}, false, err
	}
	normalized, err := normalizePRDevelopmentControllerSuspensionFinalize(input)
	if err != nil {
		return PRDevelopmentControllerSuspensionTransition{}, false, err
	}
	var (
		transition PRDevelopmentControllerSuspensionTransition
		changed    bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		suspension, found, err := loadPRDevelopmentControllerSuspensionByID(
			ctx,
			conn,
			normalized.SuspensionID,
		)
		if err != nil {
			return err
		}
		if !found {
			return sql.ErrNoRows
		}
		controller, found, err := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			normalized.ControllerID,
		)
		if err != nil {
			return err
		}
		if !found {
			return sql.ErrNoRows
		}
		if suspension.ControllerID != normalized.ControllerID ||
			suspension.AttemptID != normalized.AttemptID ||
			normalized.ExpectedRevision != suspension.SourceFinalRevision+1 ||
			suspension.SuspendClaimID != normalized.ClaimID ||
			suspension.SuspendClaimEpoch != normalized.ClaimEpoch {
			return fmt.Errorf(
				"%w: suspension finalization differs from its durable intent",
				ErrPRDevelopmentControllerConflict,
			)
		}
		result, err := normalizePRDevelopmentControllerSuspensionResult(
			suspension,
			normalized.Result,
		)
		if err != nil {
			return err
		}
		resultJSON, resultHash, err := encodePRDevelopmentControllerSuspensionResult(result)
		if err != nil {
			return err
		}
		claimDigest := prDevelopmentControllerSuspensionClaimTokenDigest(
			normalized.ClaimToken,
		)
		if suspension.Status == PRDevelopmentControllerSuspensionStatusSuspended {
			now, err := s.currentTime()
			if err != nil {
				return err
			}
			if err = requireNonRegressingPRDevelopmentControllerTime(
				now,
				maxPRDevelopmentControllerTime(controller.UpdatedAt, suspension.UpdatedAt),
			); err != nil {
				return err
			}
			if suspension.SuspendClaimTokenDigest != claimDigest ||
				suspension.SuspendResultHash != resultHash ||
				!bytes.Equal(suspension.SuspendResultJSON, resultJSON) ||
				suspension.SuspendedAt == nil ||
				controller.Phase != PRDevelopmentControllerSuspended ||
				controller.Revision != suspension.FinalSuspensionRevision ||
				controller.CurrentAttemptID != suspension.AttemptID ||
				!controller.UpdatedAt.Equal(*suspension.SuspendedAt) {
				return fmt.Errorf(
					"%w: finalized suspension replay is no longer current",
					ErrPRDevelopmentControllerConflict,
				)
			}
			transition = PRDevelopmentControllerSuspensionTransition{
				Controller: controller,
				Suspension: suspension,
			}
			return nil
		}
		if suspension.Status != PRDevelopmentControllerSuspensionStatusSuspendClaimed ||
			suspension.SuspendClaimToken != normalized.ClaimToken ||
			controller.Phase != PRDevelopmentControllerSuspensionPending ||
			controller.Revision != normalized.ExpectedRevision ||
			controller.CurrentAttemptID != normalized.AttemptID {
			return fmt.Errorf(
				"%w: operation does not hold the exact suspension claim",
				ErrPRDevelopmentControllerConflict,
			)
		}
		now, err := s.currentTime()
		if err != nil {
			return err
		}
		if suspension.SuspendClaimUntil == nil || !suspension.SuspendClaimUntil.After(now) {
			return ErrStaleLease
		}
		if err = requireNonRegressingPRDevelopmentControllerTime(
			now,
			maxPRDevelopmentControllerTime(controller.UpdatedAt, suspension.UpdatedAt),
		); err != nil {
			return err
		}
		if err = requirePRDevelopmentControllerSuspensionResultSource(
			ctx,
			conn,
			suspension,
			result,
		); err != nil {
			return err
		}
		finalRevision := controller.Revision + 1
		finalized := suspension
		finalized.Status = PRDevelopmentControllerSuspensionStatusSuspended
		finalized.SuspensionReservationKey = ""
		finalized.SuspendRequest.ReservationKey = ""
		finalized.SuspendClaimToken = ""
		finalized.SuspendClaimUntil = nil
		finalized.SuspendClaimTokenDigest = claimDigest
		finalized.SuspendResult = result
		finalized.SuspendResultJSON = resultJSON
		finalized.SuspendResultHash = resultHash
		finalized.FinalSuspensionRevision = finalRevision
		finalized.SuspendedAt = &now
		finalized.UpdatedAt = now
		finalized.SuspensionFinalHash = hashPRDevelopmentControllerSuspensionFinal(finalized)
		rowResult, err := conn.ExecContext(ctx, `
			UPDATE pr_development_controller_suspensions
			SET status = 'suspended', suspension_reservation_key = '',
				suspend_claim_token = '', suspend_claim_until = NULL,
				suspend_claim_token_digest = ?, suspend_result_json = ?,
				suspend_result_hash = ?, candidate_tree = ?, candidate_digest = ?,
				changed_file_count = ?, suspension_hash = ?, prepared_commit = ?,
				prepared_tree = ?, prepared_commit_applied = ?,
				final_suspension_revision = ?, suspension_final_hash = ?,
				suspended_at = ?, updated_at = ?
			WHERE id = ? AND controller_id = ? AND attempt_id = ? AND
				status = 'suspend_claimed' AND suspend_claim_id = ? AND
				suspend_claim_token = ? AND suspend_claim_epoch = ? AND
				suspend_claim_until > ?`,
			claimDigest,
			resultJSON,
			resultHash,
			result.CandidateTree,
			result.CandidateDigest,
			result.ChangedFileCount,
			result.SuspensionHash,
			result.PreparedCommit,
			result.PreparedTree,
			result.PreparedCommitApplied,
			finalRevision,
			finalized.SuspensionFinalHash,
			toDBTime(now),
			toDBTime(now),
			suspension.ID,
			controller.ID,
			normalized.AttemptID,
			normalized.ClaimID,
			normalized.ClaimToken,
			normalized.ClaimEpoch,
			toDBTime(now),
		)
		if err != nil {
			return err
		}
		if err = requireOnePRDevelopmentControllerRow(rowResult); err != nil {
			return err
		}
		controllerResult, err := conn.ExecContext(ctx, `
			UPDATE pr_development_thread_controllers
			SET revision = revision + 1, phase = 'suspended', updated_at = ?
			WHERE id = ? AND revision = ? AND phase = 'suspension_pending' AND
				current_attempt_id = ? AND lease_kind = '' AND lease_owner = '' AND
				lease_token = '' AND lease_until IS NULL AND
				mutation_reservation_key = ''`,
			toDBTime(now),
			controller.ID,
			controller.Revision,
			controller.CurrentAttemptID,
		)
		if err != nil {
			return err
		}
		if err = requireOnePRDevelopmentControllerRow(controllerResult); err != nil {
			return err
		}
		loadedSuspension, found, err := loadPRDevelopmentControllerSuspensionByID(
			ctx,
			conn,
			suspension.ID,
		)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("finalized controller suspension disappeared")
		}
		loadedController, found, err := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			controller.ID,
		)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("suspended controller disappeared")
		}
		transition = PRDevelopmentControllerSuspensionTransition{
			Controller: loadedController,
			Suspension: loadedSuspension,
		}
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentControllerSuspensionTransition{}, false, fmt.Errorf(
			"finalize pull request development controller suspension: %w",
			s.dbError(err),
		)
	}
	return transition, changed, nil
}

func loadPRDevelopmentControllerSuspensionByID(
	ctx context.Context,
	queryer rowsQueryer,
	suspensionID string,
) (PRDevelopmentControllerSuspension, bool, error) {
	suspension, err := scanPRDevelopmentControllerSuspension(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentControllerSuspensionColumns+`
		FROM pr_development_controller_suspensions
		WHERE id = ?`, suspensionID))
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentControllerSuspension{}, false, nil
	}
	if err != nil {
		return PRDevelopmentControllerSuspension{}, false, err
	}
	return suspension, true, nil
}

func loadActivePRDevelopmentControllerSuspension(
	ctx context.Context,
	queryer rowsQueryer,
	controllerID string,
) (PRDevelopmentControllerSuspension, bool, error) {
	suspension, err := scanPRDevelopmentControllerSuspension(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentControllerSuspensionColumns+`
		FROM pr_development_controller_suspensions
		WHERE controller_id = ? AND status <> 'resumed'`, controllerID))
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentControllerSuspension{}, false, nil
	}
	if err != nil {
		return PRDevelopmentControllerSuspension{}, false, err
	}
	return suspension, true, nil
}

func loadPRDevelopmentControllerSuspensionBySource(
	ctx context.Context,
	queryer rowsQueryer,
	sourceKind PRDevelopmentControllerSuspensionSourceKind,
	sourceRecoveryID string,
) (PRDevelopmentControllerSuspension, bool, error) {
	suspension, err := scanPRDevelopmentControllerSuspension(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentControllerSuspensionColumns+`
		FROM pr_development_controller_suspensions
		WHERE source_kind = ? AND source_recovery_id = ?`,
		sourceKind,
		sourceRecoveryID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentControllerSuspension{}, false, nil
	}
	if err != nil {
		return PRDevelopmentControllerSuspension{}, false, err
	}
	return suspension, true, nil
}

func loadPRDevelopmentControllerSuspensions(
	ctx context.Context,
	queryer rowsQueryer,
	controllerID string,
) ([]PRDevelopmentControllerSuspension, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT `+prDevelopmentControllerSuspensionColumns+`
		FROM pr_development_controller_suspensions
		WHERE controller_id = ?
		ORDER BY ordinal`, controllerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	suspensions := make([]PRDevelopmentControllerSuspension, 0)
	previousHash := emptyPRDevelopmentControllerSuspensionDigest()
	for rows.Next() {
		suspension, scanErr := scanPRDevelopmentControllerSuspension(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if suspension.Ordinal != len(suspensions) || suspension.PreviousHash != previousHash {
			return nil, errors.New("stored controller suspension chain is not contiguous")
		}
		if len(suspensions) > 0 &&
			suspensions[len(suspensions)-1].Status != PRDevelopmentControllerSuspensionStatusResumed {
			return nil, errors.New("stored controller suspension has an unresolved predecessor")
		}
		suspensions = append(suspensions, suspension)
		if len(suspensions) > MaxPRDevelopmentControllerFences {
			return nil, errors.New("stored controller has too many suspensions")
		}
		previousHash = suspension.ResumeFinalHash
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return suspensions, nil
}

func scanPRDevelopmentControllerSuspension(
	scanner rowScanner,
) (PRDevelopmentControllerSuspension, error) {
	var (
		suspension                                     PRDevelopmentControllerSuspension
		ordinal, suspendClaims, resumeClaims           int64
		changedFileCount, preparedCommitApplied        int64
		suspendClaimUntil, suspendClaimedAt            sql.NullInt64
		suspendedAt, resumePreparedAt                  sql.NullInt64
		resumeClaimUntil, resumeClaimedAt              sql.NullInt64
		newMutationLeaseUntil, resumedAt               sql.NullInt64
		resumeAttemptID                                sql.NullString
		createdAt, updatedAt                           int64
		candidateTree, candidateDigest, suspensionHash string
		preparedCommit, preparedTree, rotationHash     string
	)
	if err := scanner.Scan(
		&suspension.ID,
		&suspension.ControllerID,
		&suspension.ThreadID,
		&suspension.OwnerSessionID,
		&suspension.AttemptID,
		&ordinal,
		&suspension.SourceKind,
		&suspension.SourceRecoveryID,
		&suspension.SourceOperationID,
		&suspension.SourceOperationKind,
		&suspension.SourceFinalRevision,
		&suspension.SourceFinalHash,
		&suspension.Mode,
		&suspension.Status,
		&suspension.AgentID,
		&suspension.WorkspaceID,
		&suspension.LineID,
		&suspension.SourceCloneURL,
		&suspension.SourceRef,
		&suspension.SourceCommit,
		&suspension.SourceTree,
		&suspension.LineVersion,
		&suspension.MutationEpoch,
		&suspension.TipCommit,
		&suspension.Tree,
		&suspension.SuspensionReservationKey,
		&suspension.SuspensionReservationDigest,
		&suspension.MutationLeaseEpoch,
		&suspension.MutationLeaseTokenDigest,
		&suspension.SuspendIntentID,
		&suspension.SuspendRequestJSON,
		&suspension.SuspendRequestHash,
		&suspension.PreviousHash,
		&suspension.IntentHash,
		&suspension.SuspendClaimID,
		&suspension.SuspendClaimOwner,
		&suspension.SuspendClaimToken,
		&suspendClaimUntil,
		&suspension.SuspendClaimEpoch,
		&suspendClaims,
		&suspendClaimedAt,
		&suspension.SuspendClaimTokenDigest,
		&suspension.SuspendResultJSON,
		&suspension.SuspendResultHash,
		&candidateTree,
		&candidateDigest,
		&changedFileCount,
		&suspensionHash,
		&preparedCommit,
		&preparedTree,
		&preparedCommitApplied,
		&suspension.FinalSuspensionRevision,
		&suspension.SuspensionFinalHash,
		&suspendedAt,
		&resumeAttemptID,
		&suspension.ResumeIntentID,
		&suspension.ResumeReservationKey,
		&suspension.ResumeReservationDigest,
		&suspension.ResumeRequestJSON,
		&suspension.ResumeRequestHash,
		&suspension.ResumeIntentHash,
		&resumePreparedAt,
		&suspension.ResumeClaimID,
		&suspension.ResumeClaimOwner,
		&suspension.ResumeClaimToken,
		&resumeClaimUntil,
		&suspension.ResumeClaimEpoch,
		&resumeClaims,
		&resumeClaimedAt,
		&suspension.ResumeClaimTokenDigest,
		&suspension.ResumeResultJSON,
		&suspension.ResumeResultHash,
		&rotationHash,
		&suspension.NewMutationLeaseEpoch,
		&suspension.NewMutationLeaseTokenDigest,
		&newMutationLeaseUntil,
		&suspension.FinalResumeRevision,
		&suspension.ResumeFinalHash,
		&resumedAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		return PRDevelopmentControllerSuspension{}, err
	}
	suspension.Ordinal = int(ordinal)
	suspension.SuspendClaims = int(suspendClaims)
	suspension.ResumeClaims = int(resumeClaims)
	if int64(suspension.Ordinal) != ordinal ||
		int64(suspension.SuspendClaims) != suspendClaims ||
		int64(suspension.ResumeClaims) != resumeClaims ||
		int64(int(changedFileCount)) != changedFileCount ||
		(preparedCommitApplied != 0 && preparedCommitApplied != 1) {
		return PRDevelopmentControllerSuspension{}, errors.New(
			"stored controller suspension integer overflows",
		)
	}
	suspension.SuspendClaimUntil = fromNullableTime(suspendClaimUntil)
	suspension.SuspendClaimedAt = fromNullableTime(suspendClaimedAt)
	suspension.SuspendedAt = fromNullableTime(suspendedAt)
	suspension.ResumePreparedAt = fromNullableTime(resumePreparedAt)
	if resumeAttemptID.Valid {
		suspension.ResumeAttemptID = resumeAttemptID.String
	}
	suspension.ResumeClaimUntil = fromNullableTime(resumeClaimUntil)
	suspension.ResumeClaimedAt = fromNullableTime(resumeClaimedAt)
	suspension.NewMutationLeaseUntil = fromNullableTime(newMutationLeaseUntil)
	suspension.ResumedAt = fromNullableTime(resumedAt)
	suspension.CreatedAt = fromDBTime(createdAt)
	suspension.UpdatedAt = fromDBTime(updatedAt)

	request, err := decodePRDevelopmentControllerSuspensionRequest(
		suspension.SuspendRequestJSON,
	)
	if err != nil {
		return PRDevelopmentControllerSuspension{}, fmt.Errorf(
			"stored controller suspension request is invalid: %w", err,
		)
	}
	if suspension.Status == PRDevelopmentControllerSuspensionStatusSuspendPending ||
		suspension.Status == PRDevelopmentControllerSuspensionStatusSuspendClaimed {
		request.ReservationKey = suspension.SuspensionReservationKey
	}
	suspension.SuspendRequest = request
	if len(suspension.SuspendResultJSON) != 0 {
		result, resultErr := decodePRDevelopmentControllerSuspensionResult(
			suspension.SuspendResultJSON,
		)
		if resultErr != nil {
			return PRDevelopmentControllerSuspension{}, fmt.Errorf(
				"stored controller suspension result is invalid: %w", resultErr,
			)
		}
		suspension.SuspendResult = result
		if result.CandidateTree != candidateTree ||
			result.CandidateDigest != candidateDigest ||
			result.ChangedFileCount != int(changedFileCount) ||
			result.SuspensionHash != suspensionHash ||
			result.PreparedCommit != preparedCommit ||
			result.PreparedTree != preparedTree ||
			result.PreparedCommitApplied != (preparedCommitApplied == 1) {
			return PRDevelopmentControllerSuspension{}, errors.New(
				"stored controller suspension result projection differs from its payload",
			)
		}
	} else if candidateTree != "" || candidateDigest != "" || changedFileCount != 0 ||
		suspensionHash != "" || preparedCommit != "" || preparedTree != "" ||
		preparedCommitApplied != 0 {
		return PRDevelopmentControllerSuspension{}, errors.New(
			"stored unfinished controller suspension has result projection",
		)
	}
	if len(suspension.ResumeRequestJSON) != 0 {
		resumeRequest, resumeErr := decodePRDevelopmentControllerSuspendedResumeRequest(
			suspension.ResumeRequestJSON,
		)
		if resumeErr != nil {
			return PRDevelopmentControllerSuspension{}, fmt.Errorf(
				"stored controller suspended resume request is invalid: %w", resumeErr,
			)
		}
		if suspension.Status == PRDevelopmentControllerSuspensionStatusResumePending ||
			suspension.Status == PRDevelopmentControllerSuspensionStatusResumeClaimed {
			resumeRequest.ReservationKey = suspension.ResumeReservationKey
		}
		suspension.ResumeRequest = resumeRequest
	}
	if len(suspension.ResumeResultJSON) != 0 {
		resumeResult, resumeErr := decodePRDevelopmentControllerSuspendedResumeResult(
			suspension.ResumeResultJSON,
		)
		if resumeErr != nil {
			return PRDevelopmentControllerSuspension{}, fmt.Errorf(
				"stored controller suspended resume result is invalid: %w", resumeErr,
			)
		}
		if resumeResult.RotationHash != rotationHash {
			return PRDevelopmentControllerSuspension{}, errors.New(
				"stored controller suspended resume rotation differs from its payload",
			)
		}
		suspension.ResumeResult = resumeResult
	} else if rotationHash != "" {
		return PRDevelopmentControllerSuspension{}, errors.New(
			"stored unfinished controller suspended resume has a rotation hash",
		)
	}
	if err := validateStoredPRDevelopmentControllerSuspension(suspension); err != nil {
		return PRDevelopmentControllerSuspension{}, err
	}
	suspension.SuspendRequestJSON = bytes.Clone(suspension.SuspendRequestJSON)
	suspension.SuspendResultJSON = bytes.Clone(suspension.SuspendResultJSON)
	suspension.ResumeRequestJSON = bytes.Clone(suspension.ResumeRequestJSON)
	suspension.ResumeResultJSON = bytes.Clone(suspension.ResumeResultJSON)
	return suspension, nil
}

func stagePRDevelopmentControllerSuspension(
	ctx context.Context,
	conn *sql.Conn,
	input stagePRDevelopmentControllerSuspensionInput,
	now time.Time,
) (PRDevelopmentControllerSuspension, error) {
	if conn == nil {
		return PRDevelopmentControllerSuspension{}, errors.New(
			"controller suspension staging transaction is unavailable",
		)
	}
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.SourceRecoveryID = strings.TrimSpace(input.SourceRecoveryID)
	input.SourceOperationID = strings.TrimSpace(input.SourceOperationID)
	input.SourceFinalHash = strings.TrimSpace(input.SourceFinalHash)
	if !validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		input.SourceFinalRevision < 1 ||
		input.SourceFinalRevision > MaxPRDevelopmentControllerRevision-2 ||
		!validPRDevelopmentHex(input.SourceFinalHash, sha256.Size*2) {
		return PRDevelopmentControllerSuspension{}, fmt.Errorf(
			"%w: controller suspension source identity is invalid",
			ErrInvalidPRDevelopmentController,
		)
	}
	if validateDBTimestamp("controller suspension stage time", now) != nil {
		return PRDevelopmentControllerSuspension{}, fmt.Errorf(
			"%w: controller suspension stage time is invalid",
			ErrInvalidPRDevelopmentController,
		)
	}
	source, err := loadPRDevelopmentControllerSuspensionStageSource(ctx, conn, input)
	if err != nil {
		return PRDevelopmentControllerSuspension{}, err
	}
	existing, found, err := loadPRDevelopmentControllerSuspensionBySource(
		ctx,
		conn,
		input.SourceKind,
		input.SourceRecoveryID,
	)
	if err != nil {
		return PRDevelopmentControllerSuspension{}, err
	}
	if found {
		if existing.ControllerID != input.ControllerID ||
			existing.AttemptID != input.AttemptID ||
			existing.SourceOperationID != input.SourceOperationID ||
			existing.SourceFinalRevision != input.SourceFinalRevision ||
			existing.SourceFinalHash != input.SourceFinalHash {
			return PRDevelopmentControllerSuspension{}, fmt.Errorf(
				"%w: controller suspension source replay changed",
				ErrPRDevelopmentControllerConflict,
			)
		}
		return existing, nil
	}
	// The source finalizer and this append run in one immediate transaction.
	// Load only the structurally validated row here: aggregate validation now
	// requires the suspension link that this helper is about to create.
	controller, err := scanPRDevelopmentController(conn.QueryRowContext(ctx, `
		SELECT `+prDevelopmentControllerColumns+`
		FROM pr_development_thread_controllers
		WHERE id = ?`, input.ControllerID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PRDevelopmentControllerSuspension{}, sql.ErrNoRows
		}
		return PRDevelopmentControllerSuspension{}, err
	}
	if err := requirePRDevelopmentControllerSuspensionStageSource(
		controller,
		input,
		source,
		now,
	); err != nil {
		return PRDevelopmentControllerSuspension{}, err
	}
	suspensions, err := loadPRDevelopmentControllerSuspensions(
		ctx,
		conn,
		controller.ID,
	)
	if err != nil {
		return PRDevelopmentControllerSuspension{}, err
	}
	if len(suspensions) >= MaxPRDevelopmentControllerFences {
		return PRDevelopmentControllerSuspension{}, fmt.Errorf(
			"%w: controller suspension history capacity exhausted",
			ErrPRDevelopmentControllerConflict,
		)
	}
	previousHash := emptyPRDevelopmentControllerSuspensionDigest()
	if len(suspensions) > 0 {
		previous := suspensions[len(suspensions)-1]
		if previous.Status != PRDevelopmentControllerSuspensionStatusResumed ||
			!validPRDevelopmentHex(previous.ResumeFinalHash, sha256.Size*2) {
			return PRDevelopmentControllerSuspension{}, ErrPRDevelopmentControllerActive
		}
		previousHash = previous.ResumeFinalHash
	}
	suspensionID := deterministicPRDevelopmentControllerSuspensionID(
		input.SourceKind,
		input.SourceRecoveryID,
		input.SourceOperationID,
	)
	request := PRDevelopmentControllerSuspensionRequest{
		Repository:            controller.SourceCloneURL,
		SourceRef:             controller.SourceRef,
		SourceCommit:          controller.SourceCommit,
		ReservationKey:        controller.MutationReservationKey,
		AgentID:               controller.AgentID,
		WorkspaceID:           controller.WorkspaceID,
		LineID:                controller.LineID,
		IntentID:              suspensionID,
		ExpectedVersion:       controller.LineVersion,
		ExpectedMutationEpoch: controller.MutationEpoch,
		ExpectedTip:           controller.TipCommit,
		ExpectedTree:          controller.Tree,
	}
	if source.commitRequest != nil {
		request.CommitIntentID = source.commitRequest.EffectIntentID
		request.CommitExpectedParent = source.commitRequest.ExpectedParent
		request.CommitExpectedTree = source.commitRequest.ExpectedTree
		request.CommitCandidateDigest = source.commitRequest.CandidateDigest
		request.CommitMessage = source.commitRequest.CommitMessage
		request.CommitAuthoredAt = source.commitRequest.AuthoredAt
	}
	requestJSON, requestHash, err := encodePRDevelopmentControllerSuspensionRequest(request)
	if err != nil {
		return PRDevelopmentControllerSuspension{}, err
	}
	suspension := PRDevelopmentControllerSuspension{
		ID:                          suspensionID,
		ControllerID:                controller.ID,
		ThreadID:                    controller.ThreadID,
		OwnerSessionID:              controller.OwnerSessionID,
		AttemptID:                   controller.CurrentAttemptID,
		Ordinal:                     len(suspensions),
		SourceKind:                  input.SourceKind,
		SourceRecoveryID:            input.SourceRecoveryID,
		SourceOperationID:           input.SourceOperationID,
		SourceOperationKind:         source.operationKind,
		SourceFinalRevision:         input.SourceFinalRevision,
		SourceFinalHash:             input.SourceFinalHash,
		Mode:                        source.mode,
		Status:                      PRDevelopmentControllerSuspensionStatusSuspendPending,
		AgentID:                     controller.AgentID,
		WorkspaceID:                 controller.WorkspaceID,
		LineID:                      controller.LineID,
		SourceCloneURL:              controller.SourceCloneURL,
		SourceRef:                   controller.SourceRef,
		SourceCommit:                controller.SourceCommit,
		SourceTree:                  controller.SourceTree,
		LineVersion:                 controller.LineVersion,
		MutationEpoch:               controller.MutationEpoch,
		TipCommit:                   controller.TipCommit,
		Tree:                        controller.Tree,
		SuspensionReservationKey:    controller.MutationReservationKey,
		SuspensionReservationDigest: prDevelopmentMutationReservationDigest(controller.MutationReservationKey),
		MutationLeaseEpoch:          controller.LeaseEpoch,
		MutationLeaseTokenDigest: prDevelopmentLeaseTokenDigest(
			PRDevelopmentControllerMutationLease,
			controller.LeaseToken,
		),
		SuspendIntentID:    suspensionID,
		SuspendRequest:     request,
		SuspendRequestJSON: requestJSON,
		SuspendRequestHash: requestHash,
		PreviousHash:       previousHash,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	suspension.IntentHash = hashPRDevelopmentControllerSuspensionIntent(suspension)
	if _, err = conn.ExecContext(ctx, `
		INSERT INTO pr_development_controller_suspensions (
			id, controller_id, thread_id, owner_session_id, attempt_id, ordinal,
			source_kind, source_recovery_id, source_operation_id,
			source_operation_kind, source_final_revision, source_final_hash,
			mode, status, agent_id, workspace_id, line_id, source_clone_url,
			source_ref, source_commit, source_tree, line_version, mutation_epoch,
			tip_commit, tree, suspension_reservation_key,
			suspension_reservation_digest, mutation_lease_epoch,
			mutation_lease_token_digest, suspend_intent_id, suspend_request_json,
			suspend_request_hash, previous_hash, intent_hash, created_at, updated_at
		) VALUES (
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'suspend_pending', ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		)`,
		suspension.ID,
		suspension.ControllerID,
		suspension.ThreadID,
		suspension.OwnerSessionID,
		suspension.AttemptID,
		suspension.Ordinal,
		suspension.SourceKind,
		suspension.SourceRecoveryID,
		suspension.SourceOperationID,
		suspension.SourceOperationKind,
		suspension.SourceFinalRevision,
		suspension.SourceFinalHash,
		suspension.Mode,
		suspension.AgentID,
		suspension.WorkspaceID,
		suspension.LineID,
		suspension.SourceCloneURL,
		suspension.SourceRef,
		suspension.SourceCommit,
		suspension.SourceTree,
		suspension.LineVersion,
		suspension.MutationEpoch,
		suspension.TipCommit,
		suspension.Tree,
		suspension.SuspensionReservationKey,
		suspension.SuspensionReservationDigest,
		suspension.MutationLeaseEpoch,
		suspension.MutationLeaseTokenDigest,
		suspension.SuspendIntentID,
		suspension.SuspendRequestJSON,
		suspension.SuspendRequestHash,
		suspension.PreviousHash,
		suspension.IntentHash,
		toDBTime(now),
		toDBTime(now),
	); err != nil {
		return PRDevelopmentControllerSuspension{}, err
	}
	controllerResult, err := conn.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET revision = revision + 1, phase = 'suspension_pending', lease_kind = '',
			lease_owner = '', lease_token = '', lease_until = NULL,
			mutation_reservation_key = '', updated_at = ?
		WHERE id = ? AND revision = ? AND phase = 'mutation' AND
			current_attempt_id = ? AND lease_kind = 'mutation' AND
			lease_owner = ? AND lease_token = ? AND lease_until = ? AND
			lease_epoch = ? AND mutation_reservation_key = ?`,
		toDBTime(now),
		controller.ID,
		controller.Revision,
		controller.CurrentAttemptID,
		controller.LeaseOwner,
		controller.LeaseToken,
		toDBTime(*controller.LeaseUntil),
		controller.LeaseEpoch,
		controller.MutationReservationKey,
	)
	if err != nil {
		return PRDevelopmentControllerSuspension{}, err
	}
	if err = requireOnePRDevelopmentControllerRow(controllerResult); err != nil {
		return PRDevelopmentControllerSuspension{}, err
	}
	loaded, loadedFound, err := loadPRDevelopmentControllerSuspensionByID(
		ctx,
		conn,
		suspension.ID,
	)
	if err != nil {
		return PRDevelopmentControllerSuspension{}, err
	}
	if !loadedFound {
		return PRDevelopmentControllerSuspension{}, errors.New(
			"staged controller suspension disappeared",
		)
	}
	return loaded, nil
}

func loadPRDevelopmentControllerSuspensionStageSource(
	ctx context.Context,
	queryer rowsQueryer,
	input stagePRDevelopmentControllerSuspensionInput,
) (prDevelopmentControllerSuspensionStageSource, error) {
	switch input.SourceKind {
	case PRDevelopmentControllerSuspensionSourceControllerRecovery:
		if input.SourceOperationID != "" ||
			!validPrefixedHexID(input.SourceRecoveryID, prDevelopmentRecoveryIntentIDPrefix) {
			return prDevelopmentControllerSuspensionStageSource{}, fmt.Errorf(
				"%w: controller recovery suspension source is invalid",
				ErrInvalidPRDevelopmentController,
			)
		}
		intent, found, err := loadPRDevelopmentRecoveryIntentByID(
			ctx,
			queryer,
			input.SourceRecoveryID,
		)
		if err != nil {
			return prDevelopmentControllerSuspensionStageSource{}, err
		}
		if !found {
			return prDevelopmentControllerSuspensionStageSource{}, sql.ErrNoRows
		}
		if intent.ControllerID != input.ControllerID ||
			intent.AttemptID != input.AttemptID ||
			intent.Mode != PRDevelopmentControllerRecoveryBound ||
			intent.Status != PRDevelopmentControllerRecoveryFinalized ||
			intent.FinalRevision != input.SourceFinalRevision ||
			intent.FinalHash != input.SourceFinalHash ||
			intent.FinalizedAt == nil || intent.NewMutationLeaseUntil == nil {
			return prDevelopmentControllerSuspensionStageSource{}, fmt.Errorf(
				"%w: controller recovery suspension source is not exact and final",
				ErrPRDevelopmentControllerConflict,
			)
		}
		return prDevelopmentControllerSuspensionStageSource{
			controllerID:                 intent.ControllerID,
			attemptID:                    intent.AttemptID,
			mode:                         PRDevelopmentControllerSuspensionCandidate,
			agentID:                      intent.AgentID,
			workspaceID:                  intent.WorkspaceID,
			lineID:                       intent.LineID,
			sourceCloneURL:               intent.SourceCloneURL,
			sourceRef:                    intent.SourceRef,
			sourceCommit:                 intent.SourceCommit,
			sourceTree:                   intent.SourceTree,
			lineVersion:                  intent.LineVersion,
			mutationEpoch:                intent.MutationEpoch,
			tipCommit:                    intent.TipCommit,
			tree:                         intent.Tree,
			replacementReservationDigest: intent.ReplacementReservationDigest,
			newMutationLeaseEpoch:        intent.NewMutationLeaseEpoch,
			newMutationLeaseTokenDigest:  intent.NewMutationLeaseTokenDigest,
			newMutationLeaseUntil:        intent.NewMutationLeaseUntil,
			claimOwner:                   intent.ClaimOwner,
			finalizedAt:                  intent.FinalizedAt,
		}, nil
	case PRDevelopmentControllerSuspensionSourceOperationRecovery:
		if !validPrefixedHexID(input.SourceRecoveryID, prDevelopmentRecoveryIntentIDPrefix) ||
			!validPrefixedHexID(input.SourceOperationID, prDevelopmentOperationIDPrefix) {
			return prDevelopmentControllerSuspensionStageSource{}, fmt.Errorf(
				"%w: operation recovery suspension source is invalid",
				ErrInvalidPRDevelopmentController,
			)
		}
		operation, found, err := loadPRDevelopmentControllerOperationByID(
			ctx,
			queryer,
			input.SourceOperationID,
		)
		if err != nil {
			return prDevelopmentControllerSuspensionStageSource{}, err
		}
		if !found {
			return prDevelopmentControllerSuspensionStageSource{}, sql.ErrNoRows
		}
		if operation.ControllerID != input.ControllerID ||
			operation.AttemptID != input.AttemptID ||
			operation.RecoveryID != input.SourceRecoveryID ||
			operation.RecoveryStagedAt == nil ||
			operation.Status != PRDevelopmentControllerOperationFinalized ||
			operation.Kind == PRDevelopmentControllerOperationPark ||
			operation.FinalControllerPhase != PRDevelopmentControllerMutation ||
			operation.FinalControllerRevision != input.SourceFinalRevision ||
			operation.FinalHash != input.SourceFinalHash || operation.FinalizedAt == nil ||
			operation.NewMutationLeaseUntil == nil {
			return prDevelopmentControllerSuspensionStageSource{}, fmt.Errorf(
				"%w: operation recovery suspension source is not exact and final",
				ErrPRDevelopmentControllerConflict,
			)
		}
		mode := PRDevelopmentControllerSuspensionCandidate
		var commitRequest *PRDevelopmentControllerOperationRequest
		var commitResult *PRDevelopmentControllerOperationResult
		if operation.Kind == PRDevelopmentControllerOperationCommit {
			mode = PRDevelopmentControllerSuspensionCommitRecovery
			request := operation.Request
			result := operation.Result
			commitRequest = &request
			commitResult = &result
		}
		workspaceID := operation.WorkspaceID
		lineVersion := operation.LineVersion
		mutationEpoch := operation.MutationEpoch
		tipCommit := operation.TipCommit
		tree := operation.Tree
		if operation.Kind == PRDevelopmentControllerOperationAdopt ||
			operation.Kind == PRDevelopmentControllerOperationResume {
			workspaceID = operation.Result.WorkspaceID
			lineVersion = operation.Result.Version
			mutationEpoch = operation.Result.MutationEpoch
			tipCommit = operation.Result.Tip
			tree = operation.Result.Tree
		}
		return prDevelopmentControllerSuspensionStageSource{
			controllerID:                 operation.ControllerID,
			attemptID:                    operation.AttemptID,
			operationID:                  operation.ID,
			operationKind:                operation.Kind,
			mode:                         mode,
			agentID:                      operation.AgentID,
			workspaceID:                  workspaceID,
			lineID:                       operation.LineID,
			sourceCloneURL:               operation.SourceCloneURL,
			sourceRef:                    operation.SourceRef,
			sourceCommit:                 operation.SourceCommit,
			sourceTree:                   operation.SourceTree,
			lineVersion:                  lineVersion,
			mutationEpoch:                mutationEpoch,
			tipCommit:                    tipCommit,
			tree:                         tree,
			replacementReservationDigest: operation.ReplacementReservationDigest,
			newMutationLeaseEpoch:        operation.NewMutationLeaseEpoch,
			newMutationLeaseTokenDigest:  operation.NewMutationLeaseTokenDigest,
			newMutationLeaseUntil:        operation.NewMutationLeaseUntil,
			claimOwner:                   operation.ClaimOwner,
			finalizedAt:                  operation.FinalizedAt,
			commitRequest:                commitRequest,
			commitResult:                 commitResult,
		}, nil
	case PRDevelopmentControllerSuspensionSourceSuspendedResumeRecovery:
		return prDevelopmentControllerSuspensionStageSource{}, fmt.Errorf(
			"%w: suspended-resume recovery requires its dedicated staging input",
			ErrInvalidPRDevelopmentController,
		)
	default:
		return prDevelopmentControllerSuspensionStageSource{}, fmt.Errorf(
			"%w: controller suspension source kind is invalid",
			ErrInvalidPRDevelopmentController,
		)
	}
}

func requirePRDevelopmentControllerSuspensionStageSource(
	controller PRDevelopmentController,
	input stagePRDevelopmentControllerSuspensionInput,
	source prDevelopmentControllerSuspensionStageSource,
	now time.Time,
) error {
	if source.finalizedAt == nil || source.newMutationLeaseUntil == nil ||
		requireNonRegressingPRDevelopmentControllerTime(
			now,
			maxPRDevelopmentControllerTime(
				controller.UpdatedAt,
				*source.finalizedAt,
			),
		) != nil ||
		controller.ID != source.controllerID || controller.ID != input.ControllerID ||
		controller.CurrentAttemptID != source.attemptID ||
		controller.CurrentAttemptID != input.AttemptID ||
		controller.Revision != input.SourceFinalRevision ||
		controller.Phase != PRDevelopmentControllerMutation ||
		controller.LeaseKind != PRDevelopmentControllerMutationLease ||
		controller.LeaseOwner != source.claimOwner || controller.LeaseUntil == nil ||
		!controller.LeaseUntil.Equal(*source.newMutationLeaseUntil) ||
		controller.LeaseEpoch != source.newMutationLeaseEpoch ||
		prDevelopmentLeaseTokenDigest(
			PRDevelopmentControllerMutationLease,
			controller.LeaseToken,
		) != source.newMutationLeaseTokenDigest ||
		prDevelopmentMutationReservationDigest(
			controller.MutationReservationKey,
		) != source.replacementReservationDigest ||
		controller.AgentID != source.agentID ||
		controller.WorkspaceID != source.workspaceID ||
		controller.LineID != source.lineID ||
		controller.SourceCloneURL != source.sourceCloneURL ||
		controller.SourceRef != source.sourceRef ||
		controller.SourceCommit != source.sourceCommit ||
		controller.SourceTree != source.sourceTree ||
		controller.LineVersion != source.lineVersion ||
		controller.MutationEpoch != source.mutationEpoch ||
		controller.TipCommit != source.tipCommit || controller.Tree != source.tree {
		return fmt.Errorf(
			"%w: controller suspension source is no longer the exact mutation state",
			ErrPRDevelopmentControllerConflict,
		)
	}
	return nil
}

func validateStoredPRDevelopmentControllerSuspension(
	suspension PRDevelopmentControllerSuspension,
) error {
	if !validPrefixedHexID(suspension.ID, prDevelopmentSuspensionIDPrefix) ||
		!validPrefixedHexID(suspension.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(suspension.ThreadID, prDevelopmentThreadIDPrefix) ||
		!validPrefixedHexID(suspension.OwnerSessionID, prDevelopmentRepairSessionIDPrefix) ||
		!validPrefixedHexID(suspension.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		suspension.Ordinal < 0 || suspension.Ordinal >= MaxPRDevelopmentControllerFences ||
		suspension.SourceFinalRevision < 1 ||
		suspension.SourceFinalRevision > MaxPRDevelopmentControllerRevision-2 ||
		!validPRDevelopmentHex(suspension.SourceFinalHash, sha256.Size*2) ||
		!validPRDevelopmentRepairAgentID(suspension.AgentID) ||
		!validPRDevelopmentRepairIdentity(
			suspension.WorkspaceID,
			MaxPRDevelopmentControllerIdentityBytes,
		) || !validPrefixedHexID(suspension.LineID, prDevelopmentLineIDPrefix) ||
		!validPRDevelopmentRepairCloneURL(suspension.SourceCloneURL) ||
		!validPRDevelopmentGitRef(suspension.SourceRef) ||
		!validPRDevelopmentHex(suspension.SourceCommit, 40, 64) ||
		!validSameWidthPRDevelopmentOIDs(
			suspension.SourceCommit,
			suspension.SourceTree,
			suspension.TipCommit,
			suspension.Tree,
		) || suspension.LineVersion < 0 ||
		suspension.LineVersion > MaxPRDevelopmentControllerFences ||
		suspension.MutationEpoch != suspension.LineVersion+1 ||
		suspension.MutationLeaseEpoch < 1 ||
		!validPRDevelopmentHex(
			suspension.MutationLeaseTokenDigest,
			sha256.Size*2,
		) || !validPRDevelopmentHex(
		suspension.SuspensionReservationDigest,
		sha256.Size*2,
	) || suspension.SuspendIntentID != suspension.ID ||
		!validPRDevelopmentHex(suspension.SuspendRequestHash, sha256.Size*2) ||
		!validPRDevelopmentHex(suspension.PreviousHash, sha256.Size*2) ||
		suspension.IntentHash != hashPRDevelopmentControllerSuspensionIntent(suspension) ||
		validateDBTimestamp("controller suspension creation time", suspension.CreatedAt) != nil ||
		validateDBTimestamp("controller suspension update time", suspension.UpdatedAt) != nil ||
		suspension.UpdatedAt.Before(suspension.CreatedAt) {
		return errors.New("stored controller suspension header is invalid")
	}
	if err := validateStoredPRDevelopmentControllerSuspensionSource(suspension); err != nil {
		return err
	}
	requestJSON, requestHash, err := encodePRDevelopmentControllerSuspensionRequest(
		suspension.SuspendRequest,
	)
	if err != nil || !bytes.Equal(requestJSON, suspension.SuspendRequestJSON) ||
		requestHash != suspension.SuspendRequestHash ||
		validatePRDevelopmentControllerSuspensionRequest(
			suspension,
			suspension.SuspendRequest,
		) != nil {
		return errors.New("stored controller suspension request evidence is invalid")
	}
	for name, value := range map[string]*time.Time{
		"suspend claim deadline":      suspension.SuspendClaimUntil,
		"suspend claim time":          suspension.SuspendClaimedAt,
		"suspension time":             suspension.SuspendedAt,
		"resume preparation time":     suspension.ResumePreparedAt,
		"resume claim deadline":       suspension.ResumeClaimUntil,
		"resume claim time":           suspension.ResumeClaimedAt,
		"new mutation lease deadline": suspension.NewMutationLeaseUntil,
		"resume time":                 suspension.ResumedAt,
	} {
		if value != nil && validateDBTimestamp("controller suspension "+name, *value) != nil {
			return errors.New("stored controller suspension timestamp is invalid")
		}
	}
	if err := validateStoredPRDevelopmentControllerSuspensionInitialLifecycle(suspension); err != nil {
		return err
	}
	return validateStoredPRDevelopmentControllerSuspensionResumeLifecycle(suspension)
}

func validateStoredPRDevelopmentControllerSuspensionSource(
	suspension PRDevelopmentControllerSuspension,
) error {
	switch suspension.SourceKind {
	case PRDevelopmentControllerSuspensionSourceControllerRecovery:
		if !validPrefixedHexID(
			suspension.SourceRecoveryID,
			prDevelopmentRecoveryIntentIDPrefix,
		) || suspension.SourceOperationID != "" || suspension.SourceOperationKind != "" ||
			suspension.Mode != PRDevelopmentControllerSuspensionCandidate {
			return errors.New("stored controller recovery suspension source is invalid")
		}
	case PRDevelopmentControllerSuspensionSourceOperationRecovery:
		if !validPrefixedHexID(
			suspension.SourceRecoveryID,
			prDevelopmentRecoveryIntentIDPrefix,
		) || !validPrefixedHexID(
			suspension.SourceOperationID,
			prDevelopmentOperationIDPrefix,
		) {
			return errors.New("stored operation recovery suspension source is invalid")
		}
		switch suspension.SourceOperationKind {
		case PRDevelopmentControllerOperationAdopt, PRDevelopmentControllerOperationResume:
			if suspension.Mode != PRDevelopmentControllerSuspensionCandidate {
				return errors.New("stored line recovery suspension mode is invalid")
			}
		case PRDevelopmentControllerOperationCommit:
			if suspension.Mode != PRDevelopmentControllerSuspensionCommitRecovery {
				return errors.New("stored Commit recovery suspension mode is invalid")
			}
		default:
			return errors.New("stored operation recovery suspension kind is invalid")
		}
	case PRDevelopmentControllerSuspensionSourceSuspendedResumeRecovery:
		if !validPrefixedHexID(
			suspension.SourceRecoveryID,
			prDevelopmentSuspensionIDPrefix,
		) || suspension.SourceOperationID != "" || suspension.SourceOperationKind != "" ||
			suspension.Mode != PRDevelopmentControllerSuspensionCandidate {
			return errors.New("stored suspended-resume recovery suspension source is invalid")
		}
	default:
		return errors.New("stored controller suspension source kind is invalid")
	}
	if suspension.ID != deterministicPRDevelopmentControllerSuspensionID(
		suspension.SourceKind,
		suspension.SourceRecoveryID,
		suspension.SourceOperationID,
	) {
		return errors.New("stored controller suspension ID is not source-deterministic")
	}
	return nil
}

func validatePRDevelopmentControllerSuspensionRequest(
	suspension PRDevelopmentControllerSuspension,
	request PRDevelopmentControllerSuspensionRequest,
) error {
	if request.Repository != suspension.SourceCloneURL ||
		request.SourceRef != suspension.SourceRef ||
		request.SourceCommit != suspension.SourceCommit ||
		request.ReservationKey != suspension.SuspensionReservationKey ||
		request.AgentID != suspension.AgentID ||
		request.WorkspaceID != suspension.WorkspaceID ||
		request.LineID != suspension.LineID || request.IntentID != suspension.ID ||
		request.ExpectedVersion != suspension.LineVersion ||
		request.ExpectedMutationEpoch != suspension.MutationEpoch ||
		request.ExpectedTip != suspension.TipCommit || request.ExpectedTree != suspension.Tree {
		return errors.New("controller suspension request differs from its row fence")
	}
	if suspension.Mode == PRDevelopmentControllerSuspensionCandidate {
		if request.CommitIntentID != "" || request.CommitExpectedParent != "" ||
			request.CommitExpectedTree != "" || request.CommitCandidateDigest != "" ||
			request.CommitMessage != "" || !request.CommitAuthoredAt.IsZero() {
			return errors.New("candidate suspension request has Commit fields")
		}
		return nil
	}
	if suspension.Mode != PRDevelopmentControllerSuspensionCommitRecovery ||
		!validPrefixedHexID(request.CommitIntentID, prDevelopmentCommitIntentIDPrefix) ||
		request.CommitExpectedParent != suspension.TipCommit ||
		!validSameWidthPRDevelopmentOIDs(
			suspension.SourceCommit,
			request.CommitExpectedParent,
			request.CommitExpectedTree,
		) || request.CommitExpectedTree == suspension.Tree ||
		!validPRDevelopmentHex(request.CommitCandidateDigest, sha256.Size*2) ||
		!validPRDevelopmentRepairIdentity(
			request.CommitMessage,
			prDevelopmentOperationCommitMessageBytes,
		) || request.CommitAuthoredAt.IsZero() ||
		validateDBTimestamp(
			"controller suspension Commit authored time",
			request.CommitAuthoredAt,
		) != nil {
		return errors.New("Commit recovery suspension request is invalid")
	}
	return nil
}

func validateStoredPRDevelopmentControllerSuspensionInitialLifecycle(
	suspension PRDevelopmentControllerSuspension,
) error {
	finalized := suspension.Status == PRDevelopmentControllerSuspensionStatusSuspended ||
		suspension.Status == PRDevelopmentControllerSuspensionStatusResumePending ||
		suspension.Status == PRDevelopmentControllerSuspensionStatusResumeClaimed ||
		suspension.Status == PRDevelopmentControllerSuspensionStatusResumed
	claimed := suspension.Status == PRDevelopmentControllerSuspensionStatusSuspendClaimed
	if suspension.Status != PRDevelopmentControllerSuspensionStatusSuspendPending &&
		!claimed && !finalized {
		return errors.New("stored controller suspension status is invalid")
	}
	if claimed || finalized {
		if !validPRDevelopmentRepairIdentity(
			suspension.SuspendClaimID,
			MaxPRDevelopmentControllerIdentityBytes,
		) || !validPRDevelopmentRepairIdentity(
			suspension.SuspendClaimOwner,
			MaxPRDevelopmentControllerIdentityBytes,
		) || suspension.SuspendClaimEpoch < 1 ||
			int64(suspension.SuspendClaims) != suspension.SuspendClaimEpoch ||
			suspension.SuspendClaimedAt == nil ||
			suspension.SuspendClaimedAt.Before(suspension.CreatedAt) {
			return errors.New("stored controller suspension claim evidence is invalid")
		}
	} else if suspension.SuspendClaimID != "" || suspension.SuspendClaimOwner != "" ||
		suspension.SuspendClaimEpoch != 0 || suspension.SuspendClaims != 0 ||
		suspension.SuspendClaimedAt != nil {
		return errors.New("stored pending controller suspension has claim evidence")
	}
	if claimed {
		if !validPRDevelopmentRepairIdentity(
			suspension.SuspendClaimToken,
			prDevelopmentControllerLeaseTokenBytes,
		) || suspension.SuspendClaimUntil == nil ||
			!suspension.SuspendClaimUntil.After(suspension.UpdatedAt) ||
			suspension.SuspendClaimTokenDigest != "" {
			return errors.New("stored live controller suspension claim is invalid")
		}
	} else if suspension.SuspendClaimToken != "" || suspension.SuspendClaimUntil != nil {
		return errors.New("stored controller suspension retains an inactive claim token")
	}
	if !finalized {
		if !validPrefixedHexID(
			suspension.SuspensionReservationKey,
			prDevelopmentControllerKeyPrefix,
		) || prDevelopmentMutationReservationDigest(
			suspension.SuspensionReservationKey,
		) != suspension.SuspensionReservationDigest || len(suspension.SuspendResultJSON) != 0 ||
			suspension.SuspendResultHash != "" || suspension.FinalSuspensionRevision != 0 ||
			suspension.SuspensionFinalHash != "" || suspension.SuspendedAt != nil {
			return errors.New("stored unfinished controller suspension state is invalid")
		}
		return nil
	}
	if suspension.SuspensionReservationKey != "" ||
		suspension.SuspendRequest.ReservationKey != "" ||
		!validPRDevelopmentHex(suspension.SuspendClaimTokenDigest, sha256.Size*2) ||
		len(suspension.SuspendResultJSON) == 0 ||
		!validPRDevelopmentHex(suspension.SuspendResultHash, sha256.Size*2) ||
		suspension.FinalSuspensionRevision != suspension.SourceFinalRevision+2 ||
		!validPRDevelopmentHex(suspension.SuspensionFinalHash, sha256.Size*2) ||
		suspension.SuspendedAt == nil ||
		suspension.SuspendedAt.Before(*suspension.SuspendClaimedAt) ||
		suspension.SuspensionFinalHash != hashPRDevelopmentControllerSuspensionFinal(suspension) {
		return errors.New("stored finalized controller suspension state is invalid")
	}
	normalized, err := normalizePRDevelopmentControllerSuspensionResult(
		suspension,
		suspension.SuspendResult,
	)
	if err != nil {
		return fmt.Errorf("stored controller suspension result shape is invalid: %w", err)
	}
	canonical, resultHash, err := encodePRDevelopmentControllerSuspensionResult(normalized)
	if err != nil || !bytes.Equal(canonical, suspension.SuspendResultJSON) ||
		resultHash != suspension.SuspendResultHash {
		return errors.New("stored controller suspension final result evidence is invalid")
	}
	return nil
}

func normalizePRDevelopmentControllerSuspensionResult(
	suspension PRDevelopmentControllerSuspension,
	provided PRDevelopmentControllerSuspensionResult,
) (PRDevelopmentControllerSuspensionResult, error) {
	provided.AlreadySuspended = false
	provided.WorkspaceID = strings.TrimSpace(provided.WorkspaceID)
	provided.Tip = strings.TrimSpace(provided.Tip)
	provided.Tree = strings.TrimSpace(provided.Tree)
	provided.CandidateTree = strings.TrimSpace(provided.CandidateTree)
	provided.CandidateDigest = strings.TrimSpace(provided.CandidateDigest)
	provided.SuspensionHash = strings.TrimSpace(provided.SuspensionHash)
	provided.PreparedCommit = strings.TrimSpace(provided.PreparedCommit)
	provided.PreparedTree = strings.TrimSpace(provided.PreparedTree)
	if provided.WorkspaceID != suspension.WorkspaceID ||
		provided.Version != suspension.LineVersion ||
		provided.MutationEpoch != suspension.MutationEpoch ||
		provided.Tip != suspension.TipCommit || provided.Tree != suspension.Tree ||
		!validSameWidthPRDevelopmentOIDs(
			suspension.SourceCommit,
			provided.CandidateTree,
		) || !validPRDevelopmentHex(provided.CandidateDigest, sha256.Size*2) ||
		provided.ChangedFileCount < 0 ||
		provided.ChangedFileCount > maxPRDevelopmentSuspensionChangedFiles ||
		!validPRDevelopmentHex(provided.SuspensionHash, sha256.Size*2) {
		return PRDevelopmentControllerSuspensionResult{}, fmt.Errorf(
			"%w: suspension result does not prove the exact line fence",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if suspension.Mode == PRDevelopmentControllerSuspensionCandidate {
		if provided.PreparedCommit != "" || provided.PreparedTree != "" ||
			provided.PreparedCommitApplied {
			return PRDevelopmentControllerSuspensionResult{}, fmt.Errorf(
				"%w: candidate suspension returned prepared Commit evidence",
				ErrPRDevelopmentControllerConflict,
			)
		}
		return provided, nil
	}
	request := suspension.SuspendRequest
	if suspension.Mode != PRDevelopmentControllerSuspensionCommitRecovery ||
		!validSameWidthPRDevelopmentOIDs(
			suspension.SourceCommit,
			provided.PreparedCommit,
			provided.PreparedTree,
		) || provided.PreparedCommit == request.CommitExpectedParent ||
		provided.PreparedTree != request.CommitExpectedTree ||
		(!provided.PreparedCommitApplied &&
			(provided.CandidateTree != request.CommitExpectedTree ||
				provided.CandidateDigest != request.CommitCandidateDigest)) {
		return PRDevelopmentControllerSuspensionResult{}, fmt.Errorf(
			"%w: Commit recovery suspension result differs from the prepared effect",
			ErrPRDevelopmentControllerConflict,
		)
	}
	return provided, nil
}

func validateStoredPRDevelopmentControllerSuspensionResumeLifecycle(
	suspension PRDevelopmentControllerSuspension,
) error {
	if suspension.Status == PRDevelopmentControllerSuspensionStatusSuspendPending ||
		suspension.Status == PRDevelopmentControllerSuspensionStatusSuspendClaimed ||
		suspension.Status == PRDevelopmentControllerSuspensionStatusSuspended {
		if suspension.ResumeAttemptID != "" || suspension.ResumeIntentID != "" ||
			suspension.ResumeReservationKey != "" || suspension.ResumeReservationDigest != "" ||
			len(suspension.ResumeRequestJSON) != 0 || suspension.ResumeRequestHash != "" ||
			suspension.ResumeIntentHash != "" || suspension.ResumePreparedAt != nil ||
			suspension.ResumeClaimID != "" || suspension.ResumeClaimOwner != "" ||
			suspension.ResumeClaimToken != "" || suspension.ResumeClaimUntil != nil ||
			suspension.ResumeClaimEpoch != 0 || suspension.ResumeClaims != 0 ||
			suspension.ResumeClaimedAt != nil || suspension.ResumeClaimTokenDigest != "" ||
			len(suspension.ResumeResultJSON) != 0 || suspension.ResumeResultHash != "" ||
			suspension.NewMutationLeaseEpoch != 0 ||
			suspension.NewMutationLeaseTokenDigest != "" ||
			suspension.NewMutationLeaseUntil != nil || suspension.FinalResumeRevision != 0 ||
			suspension.ResumeFinalHash != "" || suspension.ResumedAt != nil {
			return errors.New("stored controller suspension has premature resume evidence")
		}
		return nil
	}
	if suspension.Status != PRDevelopmentControllerSuspensionStatusResumePending &&
		suspension.Status != PRDevelopmentControllerSuspensionStatusResumeClaimed &&
		suspension.Status != PRDevelopmentControllerSuspensionStatusResumed {
		return errors.New("stored controller suspension resume status is invalid")
	}
	if !validPrefixedHexID(
		suspension.ResumeAttemptID,
		prDevelopmentRepairAttemptIDPrefix,
	) || !validPRDevelopmentRepairIdentity(
		suspension.ResumeIntentID,
		MaxPRDevelopmentControllerIdentityBytes,
	) || !validPRDevelopmentHex(suspension.ResumeReservationDigest, sha256.Size*2) ||
		len(suspension.ResumeRequestJSON) == 0 ||
		!validPRDevelopmentHex(suspension.ResumeRequestHash, sha256.Size*2) ||
		!validPRDevelopmentHex(suspension.ResumeIntentHash, sha256.Size*2) ||
		suspension.ResumePreparedAt == nil ||
		suspension.ResumeIntentHash != hashPRDevelopmentControllerSuspensionResumeIntent(suspension) {
		return errors.New("stored controller suspension resume intent is invalid")
	}
	requestJSON, requestHash, err := encodePRDevelopmentControllerSuspendedResumeRequest(
		suspension.ResumeRequest,
	)
	if err != nil || !bytes.Equal(requestJSON, suspension.ResumeRequestJSON) ||
		requestHash != suspension.ResumeRequestHash ||
		validatePRDevelopmentControllerSuspendedResumeRequest(suspension) != nil {
		return errors.New("stored controller suspension resume request is invalid")
	}
	claimed := suspension.Status == PRDevelopmentControllerSuspensionStatusResumeClaimed
	resumed := suspension.Status == PRDevelopmentControllerSuspensionStatusResumed
	if claimed || resumed {
		if !validPRDevelopmentRepairIdentity(
			suspension.ResumeClaimID,
			MaxPRDevelopmentControllerIdentityBytes,
		) || !validPRDevelopmentRepairIdentity(
			suspension.ResumeClaimOwner,
			MaxPRDevelopmentControllerIdentityBytes,
		) || suspension.ResumeClaimEpoch < 1 ||
			int64(suspension.ResumeClaims) != suspension.ResumeClaimEpoch ||
			suspension.ResumeClaimedAt == nil {
			return errors.New("stored controller suspension resume claim is invalid")
		}
	}
	if resumed {
		if suspension.ResumeReservationKey != "" ||
			suspension.ResumeRequest.ReservationKey != "" ||
			suspension.ResumeClaimToken != "" || suspension.ResumeClaimUntil != nil ||
			!validPRDevelopmentHex(suspension.ResumeClaimTokenDigest, sha256.Size*2) ||
			len(suspension.ResumeResultJSON) == 0 ||
			!validPRDevelopmentHex(suspension.ResumeResultHash, sha256.Size*2) ||
			!validPRDevelopmentHex(suspension.ResumeResult.RotationHash, sha256.Size*2) ||
			suspension.NewMutationLeaseEpoch < 1 ||
			!validPRDevelopmentHex(suspension.NewMutationLeaseTokenDigest, sha256.Size*2) ||
			suspension.NewMutationLeaseUntil == nil ||
			suspension.FinalResumeRevision <= suspension.FinalSuspensionRevision ||
			!validPRDevelopmentHex(suspension.ResumeFinalHash, sha256.Size*2) ||
			suspension.ResumedAt == nil ||
			suspension.ResumedAt.Before(*suspension.ResumeClaimedAt) ||
			suspension.ResumeFinalHash != hashPRDevelopmentControllerSuspensionResumeFinal(suspension) {
			return errors.New("stored finalized controller suspension resume is invalid")
		}
		canonical, resultHash, resultErr := encodePRDevelopmentControllerSuspendedResumeResult(
			suspension.ResumeResult,
		)
		if resultErr != nil || !bytes.Equal(canonical, suspension.ResumeResultJSON) ||
			resultHash != suspension.ResumeResultHash ||
			!equalStoredPRDevelopmentControllerSuspendedResumeResult(suspension) {
			return errors.New("stored controller suspension resume result evidence is invalid")
		}
		return nil
	}
	if !validPrefixedHexID(
		suspension.ResumeReservationKey,
		prDevelopmentControllerKeyPrefix,
	) || prDevelopmentMutationReservationDigest(
		suspension.ResumeReservationKey,
	) != suspension.ResumeReservationDigest || len(suspension.ResumeResultJSON) != 0 ||
		suspension.ResumeResultHash != "" || suspension.NewMutationLeaseEpoch != 0 ||
		suspension.NewMutationLeaseTokenDigest != "" ||
		suspension.NewMutationLeaseUntil != nil || suspension.FinalResumeRevision != 0 ||
		suspension.ResumeFinalHash != "" || suspension.ResumedAt != nil {
		return errors.New("stored unfinished controller suspension resume is invalid")
	}
	if claimed {
		if !validPRDevelopmentRepairIdentity(
			suspension.ResumeClaimToken,
			prDevelopmentControllerLeaseTokenBytes,
		) || suspension.ResumeClaimUntil == nil ||
			!suspension.ResumeClaimUntil.After(suspension.UpdatedAt) ||
			suspension.ResumeClaimTokenDigest != "" {
			return errors.New("stored live controller suspension resume claim is invalid")
		}
	} else if suspension.ResumeClaimID != "" || suspension.ResumeClaimOwner != "" ||
		suspension.ResumeClaimToken != "" || suspension.ResumeClaimUntil != nil ||
		suspension.ResumeClaimEpoch != 0 || suspension.ResumeClaims != 0 ||
		suspension.ResumeClaimedAt != nil || suspension.ResumeClaimTokenDigest != "" {
		return errors.New("stored pending controller suspension resume has claim evidence")
	}
	return nil
}

func validatePRDevelopmentControllerSuspendedResumeRequest(
	suspension PRDevelopmentControllerSuspension,
) error {
	request := suspension.ResumeRequest
	result := suspension.SuspendResult
	if request.Repository != suspension.SourceCloneURL ||
		request.SourceRef != suspension.SourceRef ||
		request.SourceCommit != suspension.SourceCommit ||
		request.ReservationKey != suspension.ResumeReservationKey ||
		request.AgentID != suspension.AgentID ||
		request.WorkspaceID != suspension.WorkspaceID ||
		request.LineID != suspension.LineID ||
		request.IntentID != suspension.ResumeIntentID ||
		request.ExpectedVersion != suspension.LineVersion ||
		request.ExpectedMutationEpoch != suspension.MutationEpoch ||
		request.ExpectedTip != suspension.TipCommit || request.ExpectedTree != suspension.Tree ||
		request.SuspensionHash != result.SuspensionHash ||
		request.CandidateTree != result.CandidateTree ||
		request.CandidateDigest != result.CandidateDigest ||
		request.ChangedFileCount != result.ChangedFileCount {
		return errors.New("controller suspended resume request differs from its suspension")
	}
	return nil
}

func equalStoredPRDevelopmentControllerSuspendedResumeResult(
	suspension PRDevelopmentControllerSuspension,
) bool {
	request := suspension.ResumeRequest
	result := suspension.ResumeResult
	return !result.AlreadyResumed && result.WorkspaceID == request.WorkspaceID &&
		result.Version == request.ExpectedVersion &&
		result.MutationEpoch == request.ExpectedMutationEpoch &&
		result.Tip == request.ExpectedTip && result.Tree == request.ExpectedTree &&
		result.CandidateTree == request.CandidateTree &&
		result.CandidateDigest == request.CandidateDigest &&
		result.ChangedFileCount == request.ChangedFileCount &&
		result.SuspensionHash == request.SuspensionHash &&
		validPRDevelopmentHex(result.RotationHash, sha256.Size*2)
}

func requirePRDevelopmentControllerSuspensionResultSource(
	ctx context.Context,
	queryer rowsQueryer,
	suspension PRDevelopmentControllerSuspension,
	result PRDevelopmentControllerSuspensionResult,
) error {
	switch suspension.SourceKind {
	case PRDevelopmentControllerSuspensionSourceControllerRecovery:
		intent, found, err := loadPRDevelopmentRecoveryIntentByID(
			ctx,
			queryer,
			suspension.SourceRecoveryID,
		)
		if err != nil {
			return err
		}
		if !found || intent.Status != PRDevelopmentControllerRecoveryFinalized ||
			intent.Mode != PRDevelopmentControllerRecoveryBound ||
			intent.ControllerID != suspension.ControllerID ||
			intent.AttemptID != suspension.AttemptID ||
			intent.FinalRevision != suspension.SourceFinalRevision ||
			intent.FinalHash != suspension.SourceFinalHash {
			return fmt.Errorf(
				"%w: suspension controller recovery source changed",
				ErrPRDevelopmentControllerConflict,
			)
		}
	case PRDevelopmentControllerSuspensionSourceOperationRecovery:
		operation, found, err := loadPRDevelopmentControllerOperationByID(
			ctx,
			queryer,
			suspension.SourceOperationID,
		)
		if err != nil {
			return err
		}
		if !found || operation.Status != PRDevelopmentControllerOperationFinalized ||
			operation.RecoveryStagedAt == nil ||
			operation.RecoveryID != suspension.SourceRecoveryID ||
			operation.ControllerID != suspension.ControllerID ||
			operation.AttemptID != suspension.AttemptID ||
			operation.Kind != suspension.SourceOperationKind ||
			operation.FinalControllerRevision != suspension.SourceFinalRevision ||
			operation.FinalHash != suspension.SourceFinalHash {
			return fmt.Errorf(
				"%w: suspension operation recovery source changed",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if operation.Kind == PRDevelopmentControllerOperationCommit &&
			(result.PreparedCommit != operation.Result.Commit ||
				result.PreparedTree != operation.Request.ExpectedTree ||
				(!result.PreparedCommitApplied &&
					(result.CandidateTree != operation.Request.ExpectedTree ||
						result.CandidateDigest != operation.Request.CandidateDigest ||
						result.ChangedFileCount != operation.Result.ChangedFiles))) {
			return fmt.Errorf(
				"%w: Commit suspension result differs from recovered operation proof",
				ErrPRDevelopmentControllerConflict,
			)
		}
	case PRDevelopmentControllerSuspensionSourceSuspendedResumeRecovery:
		return fmt.Errorf(
			"%w: suspended-resume recovery source validation is not implemented",
			ErrPRDevelopmentControllerConflict,
		)
	default:
		return fmt.Errorf(
			"%w: controller suspension source kind is invalid",
			ErrPRDevelopmentControllerConflict,
		)
	}
	return nil
}

func normalizePRDevelopmentControllerSuspensionClaim(
	input PRDevelopmentControllerSuspensionClaim,
) (PRDevelopmentControllerSuspensionClaim, error) {
	input.CaseID = strings.TrimSpace(input.CaseID)
	input.SuspensionID = strings.TrimSpace(input.SuspensionID)
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	var err error
	input.ClaimID, err = normalizePRDevelopmentControllerIdentity(
		"suspension claim ID",
		input.ClaimID,
		MaxPRDevelopmentControllerIdentityBytes,
		true,
	)
	if err != nil {
		return PRDevelopmentControllerSuspensionClaim{}, err
	}
	input.WorkerLabel, err = normalizePRDevelopmentControllerIdentity(
		"suspension worker label",
		input.WorkerLabel,
		MaxPRDevelopmentControllerIdentityBytes,
		true,
	)
	if err != nil {
		return PRDevelopmentControllerSuspensionClaim{}, err
	}
	if !validPrefixedHexID(input.CaseID, prDevelopmentCaseIDPrefix) ||
		!validPrefixedHexID(input.SuspensionID, prDevelopmentSuspensionIDPrefix) ||
		!validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		input.ExpectedRevision < 1 ||
		input.ExpectedRevision > MaxPRDevelopmentControllerRevision || input.Lease <= 0 {
		return PRDevelopmentControllerSuspensionClaim{}, fmt.Errorf(
			"%w: controller suspension claim is invalid",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func normalizePRDevelopmentControllerSuspensionRenew(
	input PRDevelopmentControllerSuspensionRenew,
) (PRDevelopmentControllerSuspensionRenew, error) {
	input.SuspensionID = strings.TrimSpace(input.SuspensionID)
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	for _, item := range []struct {
		name    string
		value   *string
		maximum int
	}{
		{"suspension claim ID", &input.ClaimID, MaxPRDevelopmentControllerIdentityBytes},
		{"suspension claim token", &input.ClaimToken, prDevelopmentControllerLeaseTokenBytes},
	} {
		value, err := normalizePRDevelopmentControllerIdentity(
			item.name,
			*item.value,
			item.maximum,
			true,
		)
		if err != nil {
			return PRDevelopmentControllerSuspensionRenew{}, err
		}
		*item.value = value
	}
	if !validPrefixedHexID(input.SuspensionID, prDevelopmentSuspensionIDPrefix) ||
		!validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		input.ClaimEpoch < 1 || input.Lease <= 0 {
		return PRDevelopmentControllerSuspensionRenew{}, fmt.Errorf(
			"%w: controller suspension renewal is invalid",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func normalizePRDevelopmentControllerSuspensionFinalize(
	input PRDevelopmentControllerSuspensionFinalize,
) (PRDevelopmentControllerSuspensionFinalize, error) {
	input.SuspensionID = strings.TrimSpace(input.SuspensionID)
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	for _, item := range []struct {
		name    string
		value   *string
		maximum int
	}{
		{"suspension claim ID", &input.ClaimID, MaxPRDevelopmentControllerIdentityBytes},
		{"suspension claim token", &input.ClaimToken, prDevelopmentControllerLeaseTokenBytes},
	} {
		value, err := normalizePRDevelopmentControllerIdentity(
			item.name,
			*item.value,
			item.maximum,
			true,
		)
		if err != nil {
			return PRDevelopmentControllerSuspensionFinalize{}, err
		}
		*item.value = value
	}
	if !validPrefixedHexID(input.SuspensionID, prDevelopmentSuspensionIDPrefix) ||
		!validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		input.ExpectedRevision < 1 ||
		input.ExpectedRevision > MaxPRDevelopmentControllerRevision-1 ||
		input.ClaimEpoch < 1 {
		return PRDevelopmentControllerSuspensionFinalize{}, fmt.Errorf(
			"%w: controller suspension finalization is invalid",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}
