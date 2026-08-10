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

const prDevelopmentSuspendedResumeRecoveryClaimPrefix = "pdsrrc_"

var _ PRDevelopmentControllerSuspendedResumeRecoveryStore = (*Store)(nil)

// NextPRDevelopmentControllerSuspendedResumeRecovery returns the oldest
// expired resume claim without returning either of its private credentials.
func (s *Store) NextPRDevelopmentControllerSuspendedResumeRecovery(
	ctx context.Context,
) (PRDevelopmentControllerSuspendedResumeRecoveryCandidate, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentControllerSuspendedResumeRecoveryCandidate{}, false, err
	}
	var candidate PRDevelopmentControllerSuspendedResumeRecoveryCandidate
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
				       suspensions.resume_claim_until
				FROM pr_development_controller_suspensions AS suspensions
				JOIN pr_development_repair_attempts AS attempts
				  ON attempts.id = suspensions.resume_attempt_id
				JOIN pr_development_repair_sessions AS sessions
				  ON sessions.id = attempts.session_id
				JOIN pr_development_thread_controllers AS controllers
				  ON controllers.id = suspensions.controller_id
				JOIN pr_development_repair_orchestrations AS orchestration
				  ON orchestration.attempt_id = suspensions.resume_attempt_id
				WHERE suspensions.status = 'resume_claimed' AND
				      suspensions.resume_claim_until <= ? AND
				      orchestration.phase = 'bootstrap' AND
				      orchestration.controller_id = suspensions.controller_id AND
				      orchestration.claim_until <= ? AND
				      controllers.phase = 'suspended' AND
				      controllers.revision = suspensions.final_suspension_revision + 1 AND
				      controllers.current_attempt_id = suspensions.resume_attempt_id AND
				      controllers.lease_kind = '' AND controllers.lease_token = '' AND
				      controllers.mutation_reservation_key = ''
				ORDER BY suspensions.resume_claim_until, suspensions.id
				LIMIT 1`,
				toDBTime(now),
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
			if !found || suspension.Status !=
				PRDevelopmentControllerSuspensionStatusResumeClaimed ||
				suspension.ResumeClaimUntil == nil ||
				suspension.ResumeClaimUntil.After(now) {
				return errors.New("expired suspended resume candidate disappeared")
			}
			candidate.ControllerID = suspension.ControllerID
			candidate.AttemptID = suspension.ResumeAttemptID
			candidate.ExpectedRevision = suspension.FinalSuspensionRevision + 1
			candidate.AvailableAt = fromDBTime(availableAtRaw)
			return nil
		},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentControllerSuspendedResumeRecoveryCandidate{}, false, nil
	}
	if err != nil {
		return PRDevelopmentControllerSuspendedResumeRecoveryCandidate{}, false,
			fmt.Errorf(
				"next pull request development suspended resume recovery: %w",
				s.dbError(err),
			)
	}
	return candidate, true, nil
}

// ClaimPRDevelopmentControllerSuspendedResumeRecovery rotates only the
// expired resume child claim. The immutable request and staged bearer do not
// change, and the controller remains bearer-free and suspended.
func (s *Store) ClaimPRDevelopmentControllerSuspendedResumeRecovery(
	ctx context.Context,
	input PRDevelopmentControllerSuspendedResumeRecoveryClaim,
) (PRDevelopmentControllerSuspendedResumeRecoveryLease, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentControllerSuspendedResumeRecoveryLease{}, false, err
	}
	normalized, err := normalizePRDevelopmentControllerSuspendedResumeRecoveryClaim(input)
	if err != nil {
		return PRDevelopmentControllerSuspendedResumeRecoveryLease{}, false, err
	}
	var (
		lease   PRDevelopmentControllerSuspendedResumeRecoveryLease
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
		suspension, controller, err :=
			loadCurrentPRDevelopmentSuspendedResumeRecovery(
				ctx,
				conn,
				normalized.SuspensionID,
				normalized.ControllerID,
				normalized.AttemptID,
				normalized.ExpectedRevision,
			)
		if err != nil {
			return err
		}
		if controller.ThreadID != relation.Thread.ID ||
			controller.OwnerSessionID != relation.Session.ID {
			return fmt.Errorf(
				"%w: suspended resume recovery is not owned by this case",
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
		if suspension.ResumeClaimUntil != nil && suspension.ResumeClaimUntil.After(now) {
			if suspension.ResumeClaimID != normalized.ClaimID ||
				suspension.ResumeClaimOwner != normalized.WorkerLabel {
				return ErrPRDevelopmentControllerActive
			}
			lease = PRDevelopmentControllerSuspendedResumeRecoveryLease{
				Controller: controller,
				Suspension: suspension,
				Reclaimed:  true,
			}
			return nil
		}
		orchestration, found, err := loadPRDevelopmentRepairOrchestration(
			ctx,
			conn,
			normalized.AttemptID,
		)
		if err != nil {
			return err
		}
		if !found || orchestration.Phase != PRDevelopmentRepairOrchestrationBootstrap ||
			orchestration.ControllerID != controller.ID ||
			orchestration.ClaimUntil == nil || orchestration.ClaimUntil.After(now) {
			return fmt.Errorf(
				"%w: suspended resume recovery scheduling parent is still live or changed",
				ErrPRDevelopmentControllerConflict,
			)
		}
		deadline, err := prDevelopmentControllerDeadline(now, normalized.Lease)
		if err != nil {
			return err
		}
		if suspension.ResumeClaimID == normalized.ClaimID &&
			suspension.ResumeClaimOwner != normalized.WorkerLabel {
			return fmt.Errorf(
				"%w: suspended resume recovery claim ID belongs to another worker",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if suspension.ResumeClaimEpoch == int64(^uint64(0)>>1) ||
			controller.Revision > MaxPRDevelopmentControllerRevision-2 ||
			suspension.Ordinal >= MaxPRDevelopmentControllerFences-1 {
			return fmt.Errorf(
				"%w: suspended resume recovery capacity exhausted",
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
				 WHERE suspend_claim_id = ?) +
				(SELECT COUNT(*) FROM pr_development_controller_suspensions
				 WHERE resume_claim_id = ? AND id <> ?)`,
			normalized.ClaimID,
			normalized.ClaimID,
			normalized.ClaimID,
			normalized.ClaimID,
			suspension.ID,
		).Scan(&duplicate); err != nil {
			return err
		}
		if duplicate != 0 {
			return fmt.Errorf(
				"%w: suspended resume recovery claim ID is already bound",
				ErrPRDevelopmentControllerConflict,
			)
		}
		token, err := newLeaseToken(normalized.WorkerLabel)
		if err != nil {
			return err
		}
		if err = requireAvailablePRDevelopmentSuspendedResumeRecoveryToken(
			ctx,
			conn,
			token,
		); err != nil {
			return err
		}
		result, err := conn.ExecContext(ctx, `
			UPDATE pr_development_controller_suspensions
			SET resume_claim_id = ?, resume_claim_owner = ?,
				resume_claim_token = ?, resume_claim_until = ?,
				resume_claim_epoch = resume_claim_epoch + 1,
				resume_claims = resume_claims + 1,
				resume_claimed_at = ?, updated_at = ?
			WHERE id = ? AND controller_id = ? AND resume_attempt_id = ? AND
				status = 'resume_claimed' AND final_suspension_revision + 1 = ? AND
				resume_claim_until <= ?`,
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
			return errors.New("claimed suspended resume recovery disappeared")
		}
		lease = PRDevelopmentControllerSuspendedResumeRecoveryLease{
			Controller: controller,
			Suspension: claimed,
			Reclaimed:  true,
		}
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentControllerSuspendedResumeRecoveryLease{}, false,
			fmt.Errorf(
				"claim pull request development suspended resume recovery: %w",
				s.dbError(err),
			)
	}
	return lease, changed, nil
}

// RenewPRDevelopmentControllerSuspendedResumeRecovery renews only a live
// recovery claim while leaving its exact Git request immutable.
func (s *Store) RenewPRDevelopmentControllerSuspendedResumeRecovery(
	ctx context.Context,
	input PRDevelopmentControllerSuspendedResumeRecoveryRenew,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	normalized, err := normalizePRDevelopmentControllerSuspendedResumeRecoveryRenew(input)
	if err != nil {
		return err
	}
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		suspension, controller, err :=
			loadCurrentPRDevelopmentSuspendedResumeRecovery(
				ctx,
				conn,
				normalized.SuspensionID,
				normalized.ControllerID,
				normalized.AttemptID,
				0,
			)
		if err != nil {
			return err
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
		if suspension.ResumeClaimUntil != nil &&
			suspension.ResumeClaimUntil.After(deadline) {
			deadline = *suspension.ResumeClaimUntil
		}
		result, err := conn.ExecContext(ctx, `
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
			"renew pull request development suspended resume recovery: %w",
			s.dbError(err),
		)
	}
	return nil
}

// FinalizePRDevelopmentControllerSuspendedResumeRecovery records the exact
// resume replay and atomically moves its sole bearer to a hash-linked child
// suspension. No mutation lease exists between the two retained states.
func (s *Store) FinalizePRDevelopmentControllerSuspendedResumeRecovery(
	ctx context.Context,
	input PRDevelopmentControllerSuspendedResumeRecoveryFinalize,
) (PRDevelopmentControllerSuspendedResumeRecoveryTransition, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentControllerSuspendedResumeRecoveryTransition{}, false, err
	}
	normalized, err := normalizePRDevelopmentControllerSuspendedResumeRecoveryFinalize(input)
	if err != nil {
		return PRDevelopmentControllerSuspendedResumeRecoveryTransition{}, false, err
	}
	resultJSON, resultHash, err := encodePRDevelopmentControllerSuspendedResumeResult(
		normalized.Result,
	)
	if err != nil {
		return PRDevelopmentControllerSuspendedResumeRecoveryTransition{}, false, err
	}
	var (
		transition PRDevelopmentControllerSuspendedResumeRecoveryTransition
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
		claimDigest := prDevelopmentControllerSuspensionResumeClaimTokenDigest(
			normalized.ClaimToken,
		)
		if suspension.Status == PRDevelopmentControllerSuspensionStatusResumed {
			return replayPRDevelopmentControllerSuspendedResumeRecovery(
				ctx,
				conn,
				suspension,
				normalized,
				resultHash,
				claimDigest,
				&transition,
			)
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
			suspension.ResumeAttemptID != normalized.AttemptID ||
			suspension.ResumeClaimID != normalized.ClaimID ||
			suspension.ResumeClaimEpoch != normalized.ClaimEpoch ||
			suspension.Status != PRDevelopmentControllerSuspensionStatusResumeClaimed ||
			controller.Phase != PRDevelopmentControllerSuspended ||
			controller.CurrentAttemptID != normalized.AttemptID ||
			controller.Revision != normalized.ExpectedRevision ||
			normalized.ExpectedRevision != suspension.FinalSuspensionRevision+1 ||
			controller.MutationReservationKey != "" || controller.LeaseKind != "" ||
			controller.LeaseOwner != "" || controller.LeaseToken != "" ||
			controller.LeaseUntil != nil || suspension.ResumeReservationKey == "" {
			return fmt.Errorf(
				"%w: suspended resume recovery is no longer controller-current",
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
		if suspension.ResumeClaimToken != normalized.ClaimToken ||
			suspension.ResumeClaimUntil == nil ||
			!suspension.ResumeClaimUntil.After(now) {
			return ErrStaleLease
		}
		if !equalPRDevelopmentControllerSuspendedResumeResult(
			suspension,
			normalized.Result,
		) {
			return fmt.Errorf(
				"%w: suspended resume recovery result changed its exact request",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if controller.Revision > MaxPRDevelopmentControllerRevision-2 ||
			suspension.Ordinal >= MaxPRDevelopmentControllerFences-1 {
			return fmt.Errorf(
				"%w: suspended resume recovery capacity exhausted",
				ErrPRDevelopmentControllerConflict,
			)
		}
		chain, err := loadPRDevelopmentControllerSuspensions(ctx, conn, controller.ID)
		if err != nil {
			return err
		}
		if len(chain) == 0 || chain[len(chain)-1].ID != suspension.ID ||
			suspension.Ordinal != len(chain)-1 {
			return fmt.Errorf(
				"%w: suspended resume recovery is not the chain tail",
				ErrPRDevelopmentControllerConflict,
			)
		}

		bearer := suspension.ResumeReservationKey
		claimOwner := suspension.ResumeClaimOwner
		claimEpoch := suspension.ResumeClaimEpoch
		handoffDigest := prDevelopmentControllerSuspendedResumeRecoveryHandoffDigest(
			normalized.ClaimToken,
		)
		handoffUntil := *suspension.ResumeClaimUntil
		suspension.Status = PRDevelopmentControllerSuspensionStatusResumed
		suspension.ResumeReservationKey = ""
		suspension.ResumeRequest.ReservationKey = ""
		suspension.ResumeClaimToken = ""
		suspension.ResumeClaimUntil = nil
		suspension.ResumeClaimTokenDigest = claimDigest
		suspension.ResumeResult = normalized.Result
		suspension.ResumeResult.AlreadyResumed = false
		suspension.ResumeResultJSON = resultJSON
		suspension.ResumeResultHash = resultHash
		suspension.NewMutationLeaseEpoch = controller.LeaseEpoch
		suspension.NewMutationLeaseTokenDigest = handoffDigest
		suspension.NewMutationLeaseUntil = &handoffUntil
		suspension.FinalResumeRevision = controller.Revision
		suspension.ResumedAt = &now
		suspension.UpdatedAt = now
		suspension.ResumeFinalHash =
			hashPRDevelopmentControllerSuspensionResumeFinal(suspension)
		rowResult, err := conn.ExecContext(ctx, `
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
			toDBTime(handoffUntil),
			suspension.FinalResumeRevision,
			suspension.ResumeFinalHash,
			toDBTime(now),
			toDBTime(now),
			suspension.ID,
			controller.ID,
			suspension.ResumeAttemptID,
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
		next, err := appendPRDevelopmentSuspendedResumeRecoverySuspension(
			ctx,
			conn,
			controller,
			suspension,
			bearer,
			handoffDigest,
			normalized.ClaimID,
			claimOwner,
			normalized.ClaimToken,
			claimEpoch,
			handoffUntil,
			now,
		)
		if err != nil {
			return err
		}
		controllerResult, err := conn.ExecContext(ctx, `
			UPDATE pr_development_thread_controllers
			SET revision = revision + 1, phase = 'suspension_pending',
				line_version = ?, mutation_epoch = ?, tip_commit = ?, tree = ?,
				lease_kind = '', lease_owner = '', lease_token = '',
				lease_until = NULL, mutation_reservation_key = '', updated_at = ?
			WHERE id = ? AND revision = ? AND phase = 'suspended' AND
				current_attempt_id = ? AND lease_kind = '' AND lease_owner = '' AND
				lease_token = '' AND lease_until IS NULL AND
				mutation_reservation_key = ''`,
			normalized.Result.Version,
			normalized.Result.MutationEpoch,
			normalized.Result.Tip,
			normalized.Result.Tree,
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
		loadedController, found, err := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			controller.ID,
		)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("suspended resume recovery controller disappeared")
		}
		loadedResumed, found, err := loadPRDevelopmentControllerSuspensionByID(
			ctx,
			conn,
			suspension.ID,
		)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("finalized suspended resume recovery disappeared")
		}
		loadedNext, found, err := loadPRDevelopmentControllerSuspensionByID(
			ctx,
			conn,
			next.ID,
		)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("suspended resume recovery handoff disappeared")
		}
		transition = PRDevelopmentControllerSuspendedResumeRecoveryTransition{
			Controller:     loadedController,
			Resumed:        loadedResumed,
			NextSuspension: loadedNext,
		}
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentControllerSuspendedResumeRecoveryTransition{}, false,
			fmt.Errorf(
				"finalize pull request development suspended resume recovery: %w",
				s.dbError(err),
			)
	}
	return transition, changed, nil
}

func loadCurrentPRDevelopmentSuspendedResumeRecovery(
	ctx context.Context,
	queryer rowsQueryer,
	suspensionID, controllerID, attemptID string,
	expectedRevision int64,
) (PRDevelopmentControllerSuspension, PRDevelopmentController, error) {
	suspension, found, err := loadPRDevelopmentControllerSuspensionByID(
		ctx,
		queryer,
		suspensionID,
	)
	if err != nil {
		return PRDevelopmentControllerSuspension{}, PRDevelopmentController{}, err
	}
	if !found {
		return PRDevelopmentControllerSuspension{}, PRDevelopmentController{}, sql.ErrNoRows
	}
	controller, found, err := loadPRDevelopmentControllerAggregateByID(
		ctx,
		queryer,
		controllerID,
	)
	if err != nil {
		return PRDevelopmentControllerSuspension{}, PRDevelopmentController{}, err
	}
	if !found {
		return PRDevelopmentControllerSuspension{}, PRDevelopmentController{}, sql.ErrNoRows
	}
	if suspension.ControllerID != controller.ID ||
		suspension.ResumeAttemptID != attemptID ||
		suspension.Status != PRDevelopmentControllerSuspensionStatusResumeClaimed ||
		controller.Phase != PRDevelopmentControllerSuspended ||
		controller.CurrentAttemptID != attemptID ||
		controller.Revision != suspension.FinalSuspensionRevision+1 ||
		(expectedRevision != 0 && controller.Revision != expectedRevision) ||
		controller.MutationReservationKey != "" || controller.LeaseKind != "" ||
		controller.LeaseOwner != "" || controller.LeaseToken != "" ||
		controller.LeaseUntil != nil || suspension.ResumeReservationKey == "" {
		return PRDevelopmentControllerSuspension{}, PRDevelopmentController{}, fmt.Errorf(
			"%w: suspended resume is not recoverable",
			ErrPRDevelopmentControllerConflict,
		)
	}
	return suspension, controller, nil
}

func appendPRDevelopmentSuspendedResumeRecoverySuspension(
	ctx context.Context,
	conn *sql.Conn,
	controller PRDevelopmentController,
	resumed PRDevelopmentControllerSuspension,
	bearer, handoffDigest string,
	resumeClaimID, claimOwner, claimToken string,
	claimEpoch int64,
	claimUntil time.Time,
	now time.Time,
) (PRDevelopmentControllerSuspension, error) {
	if resumed.Status != PRDevelopmentControllerSuspensionStatusResumed ||
		resumed.ResumeFinalHash == "" || resumed.FinalResumeRevision != controller.Revision ||
		resumed.ResumeAttemptID != controller.CurrentAttemptID || bearer == "" ||
		prDevelopmentMutationReservationDigest(bearer) != resumed.ResumeReservationDigest ||
		!validPRDevelopmentHex(handoffDigest, sha256.Size*2) ||
		!validPRDevelopmentRepairIdentity(
			resumeClaimID,
			MaxPRDevelopmentControllerIdentityBytes,
		) || !validPRDevelopmentRepairIdentity(
		claimOwner,
		MaxPRDevelopmentControllerIdentityBytes,
	) || !validPRDevelopmentRepairIdentity(
		claimToken,
		prDevelopmentControllerLeaseTokenBytes,
	) || claimEpoch < 1 || !claimUntil.After(now) {
		return PRDevelopmentControllerSuspension{}, fmt.Errorf(
			"%w: suspended resume recovery handoff source is invalid",
			ErrPRDevelopmentControllerConflict,
		)
	}
	suspensionID := deterministicPRDevelopmentControllerSuspensionID(
		PRDevelopmentControllerSuspensionSourceSuspendedResumeRecovery,
		resumed.ID,
		"",
	)
	claimID := prDevelopmentSuspendedResumeIdentity(
		"pdsrrs_",
		resumed.ID,
		resumeClaimID,
	)
	var duplicate int
	if err := conn.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM pr_development_controller_recovery_intents
			 WHERE claim_id = ?) +
			(SELECT COUNT(*) FROM pr_development_controller_operation_intents
			 WHERE claim_id = ?) +
			(SELECT COUNT(*) FROM pr_development_controller_suspensions
			 WHERE suspend_claim_id = ?) +
			(SELECT COUNT(*) FROM pr_development_controller_suspensions
			 WHERE resume_claim_id = ?)`,
		claimID,
		claimID,
		claimID,
		claimID,
	).Scan(&duplicate); err != nil {
		return PRDevelopmentControllerSuspension{}, err
	}
	if duplicate != 0 {
		return PRDevelopmentControllerSuspension{}, fmt.Errorf(
			"%w: suspended resume recovery child claim ID is already bound",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if err := requireAvailablePRDevelopmentSuspendedResumeRecoveryToken(
		ctx,
		conn,
		claimToken,
	); err != nil {
		return PRDevelopmentControllerSuspension{}, err
	}
	request := PRDevelopmentControllerSuspensionRequest{
		Repository:            controller.SourceCloneURL,
		SourceRef:             controller.SourceRef,
		SourceCommit:          controller.SourceCommit,
		ReservationKey:        bearer,
		AgentID:               controller.AgentID,
		WorkspaceID:           resumed.ResumeResult.WorkspaceID,
		LineID:                controller.LineID,
		IntentID:              suspensionID,
		ExpectedVersion:       resumed.ResumeResult.Version,
		ExpectedMutationEpoch: resumed.ResumeResult.MutationEpoch,
		ExpectedTip:           resumed.ResumeResult.Tip,
		ExpectedTree:          resumed.ResumeResult.Tree,
	}
	requestJSON, requestHash, err := encodePRDevelopmentControllerSuspensionRequest(request)
	if err != nil {
		return PRDevelopmentControllerSuspension{}, err
	}
	next := PRDevelopmentControllerSuspension{
		ID:                          suspensionID,
		ControllerID:                controller.ID,
		ThreadID:                    controller.ThreadID,
		OwnerSessionID:              controller.OwnerSessionID,
		AttemptID:                   controller.CurrentAttemptID,
		Ordinal:                     resumed.Ordinal + 1,
		SourceKind:                  PRDevelopmentControllerSuspensionSourceSuspendedResumeRecovery,
		SourceRecoveryID:            resumed.ID,
		SourceFinalRevision:         resumed.FinalResumeRevision,
		SourceFinalHash:             resumed.ResumeFinalHash,
		Mode:                        PRDevelopmentControllerSuspensionCandidate,
		Status:                      PRDevelopmentControllerSuspensionStatusSuspendClaimed,
		AgentID:                     controller.AgentID,
		WorkspaceID:                 resumed.ResumeResult.WorkspaceID,
		LineID:                      controller.LineID,
		SourceCloneURL:              controller.SourceCloneURL,
		SourceRef:                   controller.SourceRef,
		SourceCommit:                controller.SourceCommit,
		SourceTree:                  controller.SourceTree,
		LineVersion:                 resumed.ResumeResult.Version,
		MutationEpoch:               resumed.ResumeResult.MutationEpoch,
		TipCommit:                   resumed.ResumeResult.Tip,
		Tree:                        resumed.ResumeResult.Tree,
		SuspensionReservationKey:    bearer,
		SuspensionReservationDigest: resumed.ResumeReservationDigest,
		MutationLeaseEpoch:          controller.LeaseEpoch,
		MutationLeaseTokenDigest:    handoffDigest,
		SuspendIntentID:             suspensionID,
		SuspendRequest:              request,
		SuspendRequestJSON:          requestJSON,
		SuspendRequestHash:          requestHash,
		PreviousHash:                resumed.ResumeFinalHash,
		SuspendClaimID:              claimID,
		SuspendClaimOwner:           claimOwner,
		SuspendClaimToken:           claimToken,
		SuspendClaimUntil:           &claimUntil,
		SuspendClaimEpoch:           claimEpoch,
		SuspendClaims:               int(claimEpoch),
		SuspendClaimedAt:            &now,
		CreatedAt:                   now,
		UpdatedAt:                   now,
	}
	next.IntentHash = hashPRDevelopmentControllerSuspensionIntent(next)
	_, err = conn.ExecContext(ctx, `
		INSERT INTO pr_development_controller_suspensions (
			id, controller_id, thread_id, owner_session_id, attempt_id, ordinal,
			source_kind, source_recovery_id, source_operation_id,
			source_operation_kind, source_final_revision, source_final_hash,
			mode, status, agent_id, workspace_id, line_id, source_clone_url,
			source_ref, source_commit, source_tree, line_version, mutation_epoch,
			tip_commit, tree, suspension_reservation_key,
			suspension_reservation_digest, mutation_lease_epoch,
			mutation_lease_token_digest, suspend_intent_id, suspend_request_json,
			suspend_request_hash, previous_hash, intent_hash, suspend_claim_id,
			suspend_claim_owner, suspend_claim_token, suspend_claim_until,
			suspend_claim_epoch, suspend_claims, suspend_claimed_at,
			created_at, updated_at
		) VALUES (
			?, ?, ?, ?, ?, ?, 'suspended_resume_recovery', ?, '', '', ?, ?,
			'candidate', 'suspend_claimed', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		)`,
		next.ID,
		next.ControllerID,
		next.ThreadID,
		next.OwnerSessionID,
		next.AttemptID,
		next.Ordinal,
		next.SourceRecoveryID,
		next.SourceFinalRevision,
		next.SourceFinalHash,
		next.AgentID,
		next.WorkspaceID,
		next.LineID,
		next.SourceCloneURL,
		next.SourceRef,
		next.SourceCommit,
		next.SourceTree,
		next.LineVersion,
		next.MutationEpoch,
		next.TipCommit,
		next.Tree,
		next.SuspensionReservationKey,
		next.SuspensionReservationDigest,
		next.MutationLeaseEpoch,
		next.MutationLeaseTokenDigest,
		next.SuspendIntentID,
		next.SuspendRequestJSON,
		next.SuspendRequestHash,
		next.PreviousHash,
		next.IntentHash,
		next.SuspendClaimID,
		next.SuspendClaimOwner,
		next.SuspendClaimToken,
		toDBTime(claimUntil),
		next.SuspendClaimEpoch,
		next.SuspendClaims,
		toDBTime(now),
		toDBTime(now),
		toDBTime(now),
	)
	if err != nil {
		return PRDevelopmentControllerSuspension{}, err
	}
	return next, nil
}

func replayPRDevelopmentControllerSuspendedResumeRecovery(
	ctx context.Context,
	queryer rowsQueryer,
	resumed PRDevelopmentControllerSuspension,
	input PRDevelopmentControllerSuspendedResumeRecoveryFinalize,
	resultHash, claimDigest string,
	transition *PRDevelopmentControllerSuspendedResumeRecoveryTransition,
) error {
	if resumed.ControllerID != input.ControllerID ||
		resumed.ResumeAttemptID != input.AttemptID ||
		resumed.FinalResumeRevision != input.ExpectedRevision ||
		resumed.ResumeClaimID != input.ClaimID ||
		resumed.ResumeClaimEpoch != input.ClaimEpoch ||
		resumed.ResumeClaimTokenDigest != claimDigest ||
		resumed.ResumeResultHash != resultHash ||
		resumed.ResumeFinalHash != hashPRDevelopmentControllerSuspensionResumeFinal(resumed) ||
		!equalPRDevelopmentControllerSuspendedResumeResult(resumed, input.Result) {
		return fmt.Errorf(
			"%w: finalized suspended resume recovery replay changed",
			ErrPRDevelopmentControllerConflict,
		)
	}
	next, found, err := loadPRDevelopmentControllerSuspensionBySource(
		ctx,
		queryer,
		PRDevelopmentControllerSuspensionSourceSuspendedResumeRecovery,
		resumed.ID,
	)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("finalized suspended resume recovery lost its handoff")
	}
	if err = validatePRDevelopmentControllerSuspendedResumeRecoverySourceLink(
		resumed,
		next,
	); err != nil {
		return err
	}
	controller, found, err := loadPRDevelopmentControllerAggregateByID(
		ctx,
		queryer,
		resumed.ControllerID,
	)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("finalized suspended resume recovery lost its controller")
	}
	*transition = PRDevelopmentControllerSuspendedResumeRecoveryTransition{
		Controller:     controller,
		Resumed:        resumed,
		NextSuspension: next,
	}
	return nil
}

func validatePRDevelopmentControllerSuspendedResumeRecoverySourceLink(
	resumed, suspension PRDevelopmentControllerSuspension,
) error {
	if resumed.Status != PRDevelopmentControllerSuspensionStatusResumed ||
		suspension.SourceKind !=
			PRDevelopmentControllerSuspensionSourceSuspendedResumeRecovery ||
		suspension.SourceRecoveryID != resumed.ID ||
		suspension.ControllerID != resumed.ControllerID ||
		suspension.ThreadID != resumed.ThreadID ||
		suspension.OwnerSessionID != resumed.OwnerSessionID ||
		suspension.AttemptID != resumed.ResumeAttemptID ||
		suspension.Ordinal != resumed.Ordinal+1 ||
		suspension.SourceOperationID != "" || suspension.SourceOperationKind != "" ||
		suspension.SourceFinalRevision != resumed.FinalResumeRevision ||
		suspension.SourceFinalHash != resumed.ResumeFinalHash ||
		suspension.Mode != PRDevelopmentControllerSuspensionCandidate ||
		suspension.AgentID != resumed.AgentID ||
		suspension.WorkspaceID != resumed.ResumeResult.WorkspaceID ||
		suspension.LineID != resumed.LineID ||
		suspension.SourceCloneURL != resumed.SourceCloneURL ||
		suspension.SourceRef != resumed.SourceRef ||
		suspension.SourceCommit != resumed.SourceCommit ||
		suspension.SourceTree != resumed.SourceTree ||
		suspension.LineVersion != resumed.ResumeResult.Version ||
		suspension.MutationEpoch != resumed.ResumeResult.MutationEpoch ||
		suspension.TipCommit != resumed.ResumeResult.Tip ||
		suspension.Tree != resumed.ResumeResult.Tree ||
		suspension.SuspensionReservationDigest != resumed.ResumeReservationDigest ||
		suspension.MutationLeaseEpoch != resumed.NewMutationLeaseEpoch ||
		suspension.MutationLeaseTokenDigest != resumed.NewMutationLeaseTokenDigest ||
		resumed.ResumedAt == nil || !suspension.CreatedAt.Equal(*resumed.ResumedAt) {
		return errors.New(
			"stored suspended-resume recovery handoff differs from its final source",
		)
	}
	return nil
}

func prDevelopmentControllerSuspendedResumeRecoveryHandoffDigest(token string) string {
	return prDevelopmentControllerSuspensionTokenDigest(
		"picoclaw-pr-development-controller-suspended-resume-recovery-handoff-v1\x00",
		token,
	)
}

func requireAvailablePRDevelopmentSuspendedResumeRecoveryToken(
	ctx context.Context,
	queryer rowsQueryer,
	token string,
) error {
	var duplicate int
	if err := queryer.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM pr_development_repair_attempts
			 WHERE lease_token = ?) +
			(SELECT COUNT(*) FROM pr_development_repair_orchestrations
			 WHERE claim_token = ?) +
			(SELECT COUNT(*) FROM pr_development_thread_controllers
			 WHERE lease_token = ?) +
			(SELECT COUNT(*) FROM pr_development_controller_recovery_intents
			 WHERE claim_token = ?) +
			(SELECT COUNT(*) FROM pr_development_controller_operation_intents
			 WHERE claim_token = ?) +
			(SELECT COUNT(*) FROM pr_development_controller_suspensions
			 WHERE suspend_claim_token = ? OR resume_claim_token = ?)`,
		token,
		token,
		token,
		token,
		token,
		token,
		token,
	).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate != 0 {
		return fmt.Errorf(
			"%w: suspended resume recovery claim token is already bound",
			ErrPRDevelopmentControllerConflict,
		)
	}
	return nil
}

func normalizePRDevelopmentControllerSuspendedResumeRecoveryClaim(
	input PRDevelopmentControllerSuspendedResumeRecoveryClaim,
) (PRDevelopmentControllerSuspendedResumeRecoveryClaim, error) {
	input.CaseID = strings.TrimSpace(input.CaseID)
	input.SuspensionID = strings.TrimSpace(input.SuspensionID)
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	var err error
	input.ClaimID, err = normalizePRDevelopmentControllerIdentity(
		"suspended resume recovery claim ID",
		input.ClaimID,
		MaxPRDevelopmentControllerIdentityBytes,
		true,
	)
	if err != nil {
		return PRDevelopmentControllerSuspendedResumeRecoveryClaim{}, err
	}
	input.WorkerLabel, err = normalizePRDevelopmentControllerIdentity(
		"suspended resume recovery worker label",
		input.WorkerLabel,
		MaxPRDevelopmentControllerIdentityBytes,
		true,
	)
	if err != nil {
		return PRDevelopmentControllerSuspendedResumeRecoveryClaim{}, err
	}
	if !validPrefixedHexID(input.CaseID, prDevelopmentCaseIDPrefix) ||
		!validPrefixedHexID(input.SuspensionID, prDevelopmentSuspensionIDPrefix) ||
		!validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(
			input.ClaimID,
			prDevelopmentSuspendedResumeRecoveryClaimPrefix,
		) ||
		input.ExpectedRevision < 1 ||
		input.ExpectedRevision > MaxPRDevelopmentControllerRevision-2 ||
		input.Lease <= 0 {
		return PRDevelopmentControllerSuspendedResumeRecoveryClaim{}, fmt.Errorf(
			"%w: suspended resume recovery claim is invalid",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func normalizePRDevelopmentControllerSuspendedResumeRecoveryRenew(
	input PRDevelopmentControllerSuspendedResumeRecoveryRenew,
) (PRDevelopmentControllerSuspendedResumeRecoveryRenew, error) {
	input.SuspensionID = strings.TrimSpace(input.SuspensionID)
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	for _, item := range []struct {
		name    string
		value   *string
		maximum int
	}{
		{"suspended resume recovery claim ID", &input.ClaimID, MaxPRDevelopmentControllerIdentityBytes},
		{"suspended resume recovery claim token", &input.ClaimToken, prDevelopmentControllerLeaseTokenBytes},
	} {
		value, err := normalizePRDevelopmentControllerIdentity(
			item.name,
			*item.value,
			item.maximum,
			true,
		)
		if err != nil {
			return PRDevelopmentControllerSuspendedResumeRecoveryRenew{}, err
		}
		*item.value = value
	}
	if !validPrefixedHexID(input.SuspensionID, prDevelopmentSuspensionIDPrefix) ||
		!validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(
			input.ClaimID,
			prDevelopmentSuspendedResumeRecoveryClaimPrefix,
		) ||
		input.ClaimEpoch < 1 || input.Lease <= 0 {
		return PRDevelopmentControllerSuspendedResumeRecoveryRenew{}, fmt.Errorf(
			"%w: suspended resume recovery renewal is invalid",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func normalizePRDevelopmentControllerSuspendedResumeRecoveryFinalize(
	input PRDevelopmentControllerSuspendedResumeRecoveryFinalize,
) (PRDevelopmentControllerSuspendedResumeRecoveryFinalize, error) {
	input.SuspensionID = strings.TrimSpace(input.SuspensionID)
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	for _, item := range []struct {
		name    string
		value   *string
		maximum int
	}{
		{"suspended resume recovery claim ID", &input.ClaimID, MaxPRDevelopmentControllerIdentityBytes},
		{"suspended resume recovery claim token", &input.ClaimToken, prDevelopmentControllerLeaseTokenBytes},
	} {
		value, err := normalizePRDevelopmentControllerIdentity(
			item.name,
			*item.value,
			item.maximum,
			true,
		)
		if err != nil {
			return PRDevelopmentControllerSuspendedResumeRecoveryFinalize{}, err
		}
		*item.value = value
	}
	input.Result.AlreadyResumed = false
	input.Result.WorkspaceID = strings.TrimSpace(input.Result.WorkspaceID)
	input.Result.Tip = strings.TrimSpace(input.Result.Tip)
	input.Result.Tree = strings.TrimSpace(input.Result.Tree)
	input.Result.CandidateTree = strings.TrimSpace(input.Result.CandidateTree)
	input.Result.CandidateDigest = strings.TrimSpace(input.Result.CandidateDigest)
	input.Result.SuspensionHash = strings.TrimSpace(input.Result.SuspensionHash)
	input.Result.RotationHash = strings.TrimSpace(input.Result.RotationHash)
	if !validPrefixedHexID(input.SuspensionID, prDevelopmentSuspensionIDPrefix) ||
		!validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(
			input.ClaimID,
			prDevelopmentSuspendedResumeRecoveryClaimPrefix,
		) ||
		input.ExpectedRevision < 1 ||
		input.ExpectedRevision > MaxPRDevelopmentControllerRevision-2 ||
		input.ClaimEpoch < 1 || input.Result.WorkspaceID == "" ||
		input.Result.Version < 0 ||
		input.Result.Version > MaxPRDevelopmentControllerFences ||
		input.Result.MutationEpoch != input.Result.Version+1 ||
		!validSameWidthPRDevelopmentOIDs(
			input.Result.Tip,
			input.Result.Tree,
			input.Result.CandidateTree,
		) || !validPRDevelopmentHex(input.Result.CandidateDigest, sha256.Size*2) ||
		input.Result.ChangedFileCount < 0 ||
		input.Result.ChangedFileCount > maxPRDevelopmentSuspensionChangedFiles ||
		!validPRDevelopmentHex(input.Result.SuspensionHash, sha256.Size*2) ||
		!validPRDevelopmentHex(input.Result.RotationHash, sha256.Size*2) {
		return PRDevelopmentControllerSuspendedResumeRecoveryFinalize{}, fmt.Errorf(
			"%w: suspended resume recovery finalization proof is invalid",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}
