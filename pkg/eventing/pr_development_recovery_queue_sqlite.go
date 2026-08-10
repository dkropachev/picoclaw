//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	maxPRDevelopmentRecoveryStageBatch = 32
	prDevelopmentNextRecoveryQuery     = `
		SELECT kind, case_id, controller_id, attempt_id, recovery_id,
		       operation_id, expected_revision, available_at
		FROM (
			SELECT 'operation_recovery' AS kind, sessions.case_id AS case_id,
			       operations.controller_id AS controller_id,
			       operations.attempt_id AS attempt_id,
			       operations.recovery_id AS recovery_id,
			       operations.id AS operation_id,
			       operations.recovery_revision AS expected_revision,
			       COALESCE(operations.claim_until, operations.recovery_staged_at) AS available_at,
			       operations.recovery_staged_at AS source_position
			FROM pr_development_controller_operation_intents AS operations
			JOIN pr_development_repair_attempts AS attempts
			  ON attempts.id = operations.attempt_id
			JOIN pr_development_repair_sessions AS sessions
			  ON sessions.id = attempts.session_id
			WHERE operations.status = 'recovery_pending' OR
			      (operations.status = 'recovery_claimed' AND
			       operations.claim_until <= ?)
			UNION ALL
			SELECT 'reservation_recovery' AS kind, sessions.case_id AS case_id,
			       recoveries.controller_id AS controller_id,
			       recoveries.attempt_id AS attempt_id,
			       recoveries.id AS recovery_id,
			       '' AS operation_id,
			       recoveries.recovery_revision AS expected_revision,
			       COALESCE(recoveries.claim_until, recoveries.created_at) AS available_at,
			       recoveries.created_at AS source_position
			FROM pr_development_controller_recovery_intents AS recoveries
			JOIN pr_development_repair_attempts AS attempts
			  ON attempts.id = recoveries.attempt_id
			JOIN pr_development_repair_sessions AS sessions
			  ON sessions.id = attempts.session_id
			WHERE recoveries.mode = 'bound' AND
			      (recoveries.status = 'pending' OR
			       (recoveries.status = 'claimed' AND recoveries.claim_until <= ?))
		)
		ORDER BY source_position, recovery_id, operation_id, kind
		LIMIT 1`
)

var _ PRDevelopmentControllerRecoveryScanner = (*Store)(nil)

// StageExpiredPRDevelopmentControllerRecoveries converts a bounded oldest
// prefix of expired mutation leases into their existing v13 operation-local
// or v12 reservation recovery protocol. It performs no external effect and is
// safe to call before every recovery-worker claim scan.
func (s *Store) StageExpiredPRDevelopmentControllerRecoveries(
	ctx context.Context,
	limit int,
) (int, error) {
	if err := s.ready(ctx); err != nil {
		return 0, err
	}
	if limit < 1 || limit > maxPRDevelopmentRecoveryStageBatch {
		return 0, fmt.Errorf(
			"%w: recovery staging limit must be between 1 and %d",
			ErrInvalidPRDevelopmentController,
			maxPRDevelopmentRecoveryStageBatch,
		)
	}
	staged := 0
	err := s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		rows, queryErr := conn.QueryContext(ctx, `
			SELECT id
			FROM pr_development_thread_controllers
			WHERE phase = 'mutation' AND lease_until IS NOT NULL AND lease_until <= ?
			ORDER BY lease_until, updated_at, id
			LIMIT ?`,
			toDBTime(now),
			limit,
		)
		if queryErr != nil {
			return queryErr
		}
		defer func() { _ = rows.Close() }()
		controllerIDs := make([]string, 0, limit)
		for rows.Next() {
			var controllerID string
			if scanErr := rows.Scan(&controllerID); scanErr != nil {
				return scanErr
			}
			controllerIDs = append(controllerIDs, controllerID)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}
		if closeErr := rows.Close(); closeErr != nil {
			return closeErr
		}
		for _, controllerID := range controllerIDs {
			controller, found, loadErr := loadPRDevelopmentControllerAggregateByID(
				ctx,
				conn,
				controllerID,
			)
			if loadErr != nil {
				return loadErr
			}
			if !found || controller.Phase != PRDevelopmentControllerMutation ||
				controller.LeaseUntil == nil || controller.LeaseUntil.After(now) {
				continue
			}
			if expireErr := expirePRDevelopmentMutationLease(
				ctx,
				conn,
				controller,
				now,
			); expireErr != nil {
				return expireErr
			}
			staged++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf(
			"stage expired pull request development controller recoveries: %w",
			s.dbError(err),
		)
	}
	return staged, nil
}

// NextPRDevelopmentControllerRecovery returns the oldest currently claimable
// exact recovery pointer. All eligible bound sources share one stable creation
// order; the unsafe legacy v12-unbound state is intentionally excluded.
// The returned pointer carries no claim token or reservation bearer.
func (s *Store) NextPRDevelopmentControllerRecovery(
	ctx context.Context,
) (PRDevelopmentControllerRecoveryCandidate, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentControllerRecoveryCandidate{}, false, err
	}
	var (
		candidate      PRDevelopmentControllerRecoveryCandidate
		kind           string
		availableAtRaw int64
	)
	err := s.withPRDevelopmentConversationReadSnapshot(
		ctx,
		func(queryer rowsQueryer) error {
			now, clockErr := s.currentTime()
			if clockErr != nil {
				return clockErr
			}
			return queryer.QueryRowContext(ctx, prDevelopmentNextRecoveryQuery,
				toDBTime(now),
				toDBTime(now),
			).Scan(
				&kind,
				&candidate.CaseID,
				&candidate.ControllerID,
				&candidate.AttemptID,
				&candidate.RecoveryID,
				&candidate.OperationID,
				&candidate.ExpectedRevision,
				&availableAtRaw,
			)
		},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentControllerRecoveryCandidate{}, false, nil
	}
	if err != nil {
		return PRDevelopmentControllerRecoveryCandidate{}, false, fmt.Errorf(
			"find next pull request development controller recovery: %w",
			s.dbError(err),
		)
	}
	// Scan used a local string because database/sql cannot assign directly to
	// a defined string type on every supported driver. Bind the validated value
	// explicitly instead.
	switch kind {
	case string(PRDevelopmentControllerRecoveryWorkOperation):
		candidate.Kind = PRDevelopmentControllerRecoveryWorkOperation
	case string(PRDevelopmentControllerRecoveryWorkReservation):
		candidate.Kind = PRDevelopmentControllerRecoveryWorkReservation
	default:
		return PRDevelopmentControllerRecoveryCandidate{}, false, errors.New(
			"stored pull request development recovery kind is invalid",
		)
	}
	candidate.AvailableAt = fromDBTime(availableAtRaw)
	return candidate, true, nil
}
