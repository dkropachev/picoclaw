//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var _ PRDevelopmentReviewQueue = (*Store)(nil)

// ClaimPRDevelopmentReview atomically leases the oldest completed
// orchestration whose exact parked fence is still unreviewed. An expired
// immutable-review lease may rotate; mutation authority is never acquired or
// returned by this scanner.
func (s *Store) ClaimPRDevelopmentReview(
	ctx context.Context,
	input PRDevelopmentReviewClaimRequest,
) (PRDevelopmentReviewLease, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentReviewLease{}, false, err
	}
	workerLabel, err := normalizePRDevelopmentControllerIdentity(
		"worker label",
		input.WorkerLabel,
		MaxPRDevelopmentControllerIdentityBytes,
		true,
	)
	if err != nil || input.Lease <= 0 {
		return PRDevelopmentReviewLease{}, false, fmt.Errorf(
			"%w: worker label and positive lease are required",
			ErrInvalidPRDevelopmentController,
		)
	}

	var (
		lease   PRDevelopmentReviewLease
		claimed bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		scanNow, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		controllerID, caseID, found, candidateErr := loadOldestEligiblePRDevelopmentReviewCandidate(
			ctx, conn, scanNow,
		)
		if candidateErr != nil || !found {
			return candidateErr
		}
		controller, found, loadErr := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			controllerID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return errors.New("claimable pull request development controller disappeared")
		}
		orchestration, found, loadErr := loadPRDevelopmentRepairOrchestration(
			ctx,
			conn,
			controller.CurrentAttemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found || orchestration.Phase != PRDevelopmentRepairOrchestrationCompleted ||
			orchestration.ControllerID != controller.ID ||
			orchestration.SessionID != controller.OwnerSessionID ||
			orchestration.ThreadID != controller.ThreadID ||
			orchestration.CaseID != caseID {
			return fmt.Errorf(
				"%w: review candidate is outside its exact completed orchestration cohort",
				ErrPRDevelopmentControllerConflict,
			)
		}
		attemptHighWater, highWaterErr := loadPRDevelopmentControllerAttemptHighWater(
			ctx,
			conn,
			controller,
		)
		if highWaterErr != nil {
			return highWaterErr
		}
		claimNow, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if timeErr := requireNonRegressingPRDevelopmentControllerTime(
			claimNow,
			maxPRDevelopmentControllerTime(controller.UpdatedAt, attemptHighWater),
		); timeErr != nil {
			return timeErr
		}
		deadline, deadlineErr := prDevelopmentControllerDeadline(claimNow, input.Lease)
		if deadlineErr != nil {
			return deadlineErr
		}
		updated, fence, reclaimed, acquireErr := acquirePRDevelopmentReviewLease(
			ctx,
			conn,
			controller,
			PRDevelopmentControllerAcquire{
				CaseID:           caseID,
				AttemptID:        controller.CurrentAttemptID,
				ExpectedRevision: controller.Revision,
				Kind:             PRDevelopmentControllerReviewLease,
				WorkerLabel:      workerLabel,
				Lease:            input.Lease,
			},
			claimNow,
			deadline,
		)
		if acquireErr != nil {
			return acquireErr
		}
		if updated.MutationReservationKey != "" {
			return errors.New("reservation-free review claim unexpectedly acquired mutation authority")
		}
		lease = PRDevelopmentReviewLease{
			CaseID:     caseID,
			Controller: updated,
			Fence:      fence,
			Reclaimed:  reclaimed,
		}
		claimed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentReviewLease{}, false, fmt.Errorf(
			"claim pull request development review: %w",
			s.dbError(err),
		)
	}
	return lease, claimed, nil
}

func loadOldestEligiblePRDevelopmentReviewCandidate(
	ctx context.Context,
	queryer rowQueryer,
	now time.Time,
) (string, string, bool, error) {
	var controllerID, caseID string
	err := queryer.QueryRowContext(ctx, `
		SELECT controller.id, session.case_id
		FROM pr_development_thread_controllers AS controller
		JOIN pr_development_repair_sessions AS session
		  ON session.id = controller.owner_session_id
		JOIN pr_development_repair_attempts AS attempt
		  ON attempt.id = controller.current_attempt_id
		 AND attempt.session_id = session.id
		JOIN pr_development_repair_orchestrations AS orchestration
		  ON orchestration.attempt_id = attempt.id
		 AND orchestration.session_id = session.id
		 AND orchestration.case_id = session.case_id
		 AND orchestration.thread_id = controller.thread_id
		 AND orchestration.controller_id = controller.id
		JOIN pr_development_attempt_review_fences AS fence
		  ON fence.attempt_id = attempt.id
		 AND fence.controller_id = controller.id
		 AND fence.thread_id = controller.thread_id
		JOIN pr_development_ledger_entries AS account
		  ON account.id = orchestration.ledger_entry_id
		 AND account.thread_id = controller.thread_id
		 AND account.attempt_id = attempt.id
		 AND account.kind = 'attempt'
		 AND account.ordinal = fence.ordinal * 2
		WHERE orchestration.phase = 'completed'
		  AND attempt.status = 'completed'
		  AND fence.reviewed_at IS NULL
		  AND controller.mutation_reservation_key = ''
		  AND (
			(controller.phase = 'review_pending' AND controller.lease_kind = '' AND
			 controller.lease_owner = '' AND controller.lease_token = '' AND
			 controller.lease_until IS NULL) OR
			(controller.phase = 'review' AND controller.lease_kind = 'review' AND
			 controller.lease_owner <> '' AND controller.lease_token <> '' AND
			 controller.lease_until <= ?)
		  )
		ORDER BY fence.created_at, controller.id
		LIMIT 1`,
		toDBTime(now),
	).Scan(&controllerID, &caseID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return controllerID, caseID, true, nil
}

func normalizePRDevelopmentReviewRetryIdentity(attemptID string) string {
	return "ai-review-changes:" + strings.TrimSpace(attemptID)
}

const prDevelopmentReviewRetryInstruction = "Address the actionable findings from the latest completed local AI review."
