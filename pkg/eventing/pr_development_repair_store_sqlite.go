//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	_ PRDevelopmentWorkbenchReader = (*Store)(nil)
	_ PRDevelopmentRepairAdmitter  = (*Store)(nil)
	_ PRDevelopmentRepairQueue     = (*Store)(nil)

	errInvalidStoredPRDevelopmentRepair = errors.New(
		"invalid stored pull request development repair",
	)
)

const (
	prDevelopmentRepairSessionColumns = `
		id, case_id, version, agent_id, head_repository, head_ref, head_sha,
		clone_url, review_digest, reservation_key, workspace_id,
		created_at, updated_at`
	prDevelopmentRepairAttemptColumns = `
		id, session_id, ordinal, expected_repair_version, conversation_version,
		idempotency_key, instruction, status, lease_owner, lease_token,
		lease_until, claims, summary, error_code, internal_error, iterations,
		created_at, updated_at`
	maxPRDevelopmentRepairWorkerBytes     = 256
	maxPRDevelopmentRepairWorkspaceBytes  = 256
	maxPRDevelopmentRepairLeaseBytes      = 128
	maxPRDevelopmentRepairClaimCandidates = 32

	// Public repair revisions reserve every remaining public transition before
	// admitting or claiming work. This lets a normally reachable attempt always
	// reach a terminal state without exceeding MaxPRDevelopmentRepairVersion.
	// Reclaiming an expired preparing lease is private and consumes no revision.
	prDevelopmentRepairUnpinnedQueuedTransitions    int64 = 4 // claim, pin, begin, finish
	prDevelopmentRepairPinnedQueuedTransitions      int64 = 3 // claim, begin, finish
	prDevelopmentRepairUnpinnedPreparingTransitions int64 = 3 // pin, begin, finish
	prDevelopmentRepairPinnedPreparingTransitions   int64 = 2 // begin, finish
)

// GetPRDevelopmentWorkbench reads the case, complete bounded conversation,
// and optional singleton repair session from one SQLite snapshot.
func (s *Store) GetPRDevelopmentWorkbench(
	ctx context.Context,
	caseID string,
) (PRDevelopmentWorkbench, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentWorkbench{}, err
	}
	caseID = strings.TrimSpace(caseID)
	if !validPrefixedHexID(caseID, prDevelopmentCaseIDPrefix) {
		return PRDevelopmentWorkbench{}, fmt.Errorf(
			"%w: invalid development case ID",
			ErrInvalidPRDevelopmentRepair,
		)
	}

	var workbench PRDevelopmentWorkbench
	err := s.withPRDevelopmentConversationReadSnapshot(
		ctx,
		func(queryer rowsQueryer) error {
			loaded, loadErr := loadPRDevelopmentWorkbench(ctx, queryer, caseID)
			workbench = loaded
			return loadErr
		},
	)
	if err != nil {
		return PRDevelopmentWorkbench{}, fmt.Errorf(
			"get pull request development workbench: %w",
			s.dbError(err),
		)
	}
	return workbench, nil
}

// AdmitPRDevelopmentRepair atomically fences both workbench versions and
// appends one queued attempt. An exact idempotency replay returns the current
// aggregate even if later lifecycle transitions advanced its repair version.
func (s *Store) AdmitPRDevelopmentRepair(
	ctx context.Context,
	input PRDevelopmentRepairAdmit,
) (PRDevelopmentWorkbench, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentWorkbench{}, false, err
	}
	normalized, err := normalizePRDevelopmentRepairAdmit(input)
	if err != nil {
		return PRDevelopmentWorkbench{}, false, err
	}

	var (
		workbench PRDevelopmentWorkbench
		admitted  bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		current, loadErr := loadPRDevelopmentWorkbench(ctx, conn, normalized.CaseID)
		if loadErr != nil {
			return loadErr
		}
		if current.RepairSession != nil {
			if replay, found := findPRDevelopmentRepairIdempotency(
				current.RepairSession,
				normalized.IdempotencyKey,
			); found {
				if current.RepairSession.AgentID != normalized.AgentID ||
					replay.ExpectedRepairVersion != normalized.ExpectedRepairVersion ||
					replay.ConversationVersion != normalized.ExpectedConversationVersion ||
					replay.Instruction != normalized.Instruction {
					return fmt.Errorf(
						"%w: idempotency key is bound to different repair input",
						ErrPRDevelopmentRepairConflict,
					)
				}
				workbench = current
				return nil
			}
		}
		if current.Conversation.Version != normalized.ExpectedConversationVersion {
			return fmt.Errorf(
				"%w: expected version %d, current version %d",
				ErrPRDevelopmentConversationConflict,
				normalized.ExpectedConversationVersion,
				current.Conversation.Version,
			)
		}
		if current.Thread == nil {
			return errors.New("stored pull request development case has no thread binding")
		}
		controller, controlled, controllerErr := loadPRDevelopmentControllerAggregate(
			ctx,
			conn,
			current.Thread.ID,
		)
		if controllerErr != nil {
			return controllerErr
		}
		if controlled {
			if current.RepairSession == nil ||
				current.RepairSession.ID != controller.OwnerSessionID {
				return fmt.Errorf(
					"%w: provider thread repair attempts belong to its controller owner",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if controller.Phase != PRDevelopmentControllerReady {
				return ErrPRDevelopmentControllerActive
			}
			if controller.LineVersion >= MaxPRDevelopmentControllerFences ||
				controller.Revision > MaxPRDevelopmentControllerRevision-4 {
				return fmt.Errorf(
					"%w: controller has insufficient attempt-transition headroom",
					ErrPRDevelopmentRepairCapacity,
				)
			}
		}

		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if current.RepairSession == nil {
			if normalized.ExpectedRepairVersion != 0 {
				return fmt.Errorf(
					"%w: expected repair version %d, current version 0",
					ErrPRDevelopmentRepairConflict,
					normalized.ExpectedRepairVersion,
				)
			}
			if createErr := insertPRDevelopmentRepairSession(
				ctx,
				conn,
				normalized,
				now,
			); createErr != nil {
				return createErr
			}
		} else {
			session := current.RepairSession
			if session.Version != normalized.ExpectedRepairVersion {
				return fmt.Errorf(
					"%w: expected repair version %d, current version %d",
					ErrPRDevelopmentRepairConflict,
					normalized.ExpectedRepairVersion,
					session.Version,
				)
			}
			if session.AgentID != normalized.AgentID {
				return fmt.Errorf(
					"%w: repair session agent is immutable",
					ErrPRDevelopmentRepairConflict,
				)
			}
			if activePRDevelopmentRepairAttempt(session) != nil {
				return ErrPRDevelopmentRepairActive
			}
			if len(session.Attempts) >= MaxPRDevelopmentRepairAttempts {
				return fmt.Errorf(
					"%w: session cannot exceed %d attempts",
					ErrPRDevelopmentRepairCapacity,
					MaxPRDevelopmentRepairAttempts,
				)
			}
			if session.Version > maxPRDevelopmentRepairVersionBeforeAdmission(
				session.HeadRepository != "",
			) {
				return fmt.Errorf(
					"%w: session has insufficient repair revision headroom",
					ErrPRDevelopmentRepairCapacity,
				)
			}
			if appendErr := appendPRDevelopmentRepairAttempt(
				ctx,
				conn,
				session,
				normalized,
				now,
			); appendErr != nil {
				return appendErr
			}
		}
		workbench, loadErr = loadPRDevelopmentWorkbench(ctx, conn, normalized.CaseID)
		if loadErr != nil {
			return loadErr
		}
		admitted = true
		return nil
	})
	if err != nil {
		return PRDevelopmentWorkbench{}, false, fmt.Errorf(
			"admit pull request development repair: %w",
			s.dbError(err),
		)
	}
	return workbench, admitted, nil
}

// ClaimPRDevelopmentRepair terminalizes a bounded batch of expired running
// attempts first, then examines a bounded batch and leases at most one queued
// or expired-preparing attempt. Preparing is safe to reclaim because no local
// runner has started; running is never rerun blindly.
func (s *Store) ClaimPRDevelopmentRepair(
	ctx context.Context,
	input PRDevelopmentRepairClaimRequest,
) (PRDevelopmentRepairSession, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentRepairSession{}, false, err
	}
	workerLabel, err := normalizePRDevelopmentRepairIdentity(
		"worker label",
		input.WorkerLabel,
		maxPRDevelopmentRepairWorkerBytes,
		true,
	)
	if err != nil || input.Lease <= 0 {
		return PRDevelopmentRepairSession{}, false, fmt.Errorf(
			"%w: worker label and positive lease are required",
			ErrInvalidPRDevelopmentRepair,
		)
	}
	var (
		session PRDevelopmentRepairSession
		claimed bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		scanNow, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if expireErr := expireRunningPRDevelopmentRepairs(ctx, conn, scanNow); expireErr != nil {
			return expireErr
		}
		candidates, candidateErr := collectPRDevelopmentRepairClaimCandidates(
			ctx,
			conn,
			scanNow,
		)
		if candidateErr != nil {
			return candidateErr
		}
		for _, candidate := range candidates {
			// The claim query deliberately uses only cheap schema-level predicates.
			// Validate the complete aggregate before mutation so one legacy or
			// corrupt row cannot permanently block later valid work.
			if _, loadErr := loadPRDevelopmentRepairSessionByAttempt(
				ctx,
				conn,
				candidate.attemptID,
			); loadErr != nil {
				if errors.Is(loadErr, errInvalidStoredPRDevelopmentRepair) {
					if suppressErr := suppressPRDevelopmentRepairClaim(
						ctx,
						conn,
						candidate.sessionID,
					); suppressErr != nil {
						return suppressErr
					}
					continue
				}
				return loadErr
			}
			leaseToken, tokenErr := newLeaseToken(workerLabel)
			if tokenErr != nil {
				return tokenErr
			}
			// Loading and validating a bounded aggregate can consume a meaningful
			// part of a short lease. Sample again immediately before ownership is
			// written so every successful claim starts with a live full lease.
			claimNow, claimClockErr := s.currentTime()
			if claimClockErr != nil {
				return claimClockErr
			}
			leaseUntil := claimNow.Add(input.Lease).UTC()
			if validationErr := validateDBTimestamp(
				"pull request development repair lease deadline",
				leaseUntil,
			); validationErr != nil {
				return validationErr
			}
			var result sql.Result
			var updateErr error
			switch candidate.status {
			case PRDevelopmentRepairQueued:
				result, updateErr = conn.ExecContext(ctx, `
					UPDATE pr_development_repair_attempts
					SET status = ?, lease_owner = ?, lease_token = ?, lease_until = ?,
					    claims = claims + 1, updated_at = ?
					WHERE id = ? AND status = ?`,
					PRDevelopmentRepairPreparing,
					workerLabel,
					leaseToken,
					toDBTime(leaseUntil),
					toDBTime(claimNow),
					candidate.attemptID,
					PRDevelopmentRepairQueued,
				)
			case PRDevelopmentRepairPreparing:
				// Lease owner, token, deadline, and claim count are private worker
				// state. A safe preparing reclaim must not manufacture a public
				// lifecycle transition or advance public timestamps.
				result, updateErr = conn.ExecContext(ctx, `
					UPDATE pr_development_repair_attempts
					SET lease_owner = ?, lease_token = ?, lease_until = ?,
					    claims = claims + 1
					WHERE id = ? AND status = ? AND lease_until <= ?`,
					workerLabel,
					leaseToken,
					toDBTime(leaseUntil),
					candidate.attemptID,
					PRDevelopmentRepairPreparing,
					toDBTime(claimNow),
				)
			default:
				return fmt.Errorf("pull request development repair claim status is invalid")
			}
			if updateErr != nil {
				return updateErr
			}
			if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
				if rowsErr != nil {
					return rowsErr
				}
				return fmt.Errorf("pull request development repair changed during claim")
			}
			if candidate.status == PRDevelopmentRepairQueued {
				if bumpErr := bumpPRDevelopmentRepairSessionForAttempt(
					ctx,
					conn,
					candidate.attemptID,
					claimNow,
				); bumpErr != nil {
					return bumpErr
				}
			}
			var loadErr error
			session, loadErr = loadPRDevelopmentRepairSessionByAttempt(
				ctx,
				conn,
				candidate.attemptID,
			)
			if loadErr != nil {
				return loadErr
			}
			claimed = true
			return nil
		}
		return nil
	})
	if err != nil {
		return PRDevelopmentRepairSession{}, false, fmt.Errorf(
			"claim pull request development repair: %w",
			s.dbError(err),
		)
	}
	return session, claimed, nil
}

type prDevelopmentRepairClaimCandidate struct {
	attemptID string
	sessionID string
	status    PRDevelopmentRepairStatus
}

func collectPRDevelopmentRepairClaimCandidates(
	ctx context.Context,
	conn *sql.Conn,
	now time.Time,
) ([]prDevelopmentRepairClaimCandidate, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT attempt.id, session.id, attempt.status
		FROM pr_development_repair_attempts AS attempt
		JOIN pr_development_repair_sessions AS session
		  ON session.id = attempt.session_id
		WHERE session.claim_suppressed = 0 AND session.version >= 1
		  AND NOT EXISTS (
			SELECT 1 FROM pr_development_thread_controllers AS controller
			WHERE controller.owner_session_id = session.id
		  ) AND (
			(attempt.status = ? AND (
				(session.head_repository = '' AND session.version <= ?) OR
				(session.head_repository <> '' AND session.version <= ?)
			)) OR
			(attempt.status = ? AND attempt.lease_until <= ? AND (
				(session.head_repository = '' AND session.version <= ?) OR
				(session.head_repository <> '' AND session.version <= ?)
			))
		)
		ORDER BY attempt.created_at, attempt.id
		LIMIT ?`,
		PRDevelopmentRepairQueued,
		MaxPRDevelopmentRepairVersion-prDevelopmentRepairUnpinnedQueuedTransitions,
		MaxPRDevelopmentRepairVersion-prDevelopmentRepairPinnedQueuedTransitions,
		PRDevelopmentRepairPreparing,
		toDBTime(now),
		MaxPRDevelopmentRepairVersion-prDevelopmentRepairUnpinnedPreparingTransitions,
		MaxPRDevelopmentRepairVersion-prDevelopmentRepairPinnedPreparingTransitions,
		maxPRDevelopmentRepairClaimCandidates,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	candidates := make([]prDevelopmentRepairClaimCandidate, 0)
	for rows.Next() {
		var candidate prDevelopmentRepairClaimCandidate
		if scanErr := rows.Scan(
			&candidate.attemptID,
			&candidate.sessionID,
			&candidate.status,
		); scanErr != nil {
			return nil, scanErr
		}
		candidates = append(candidates, candidate)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	if closeErr := rows.Close(); closeErr != nil {
		return nil, closeErr
	}
	return candidates, nil
}

func suppressPRDevelopmentRepairClaim(
	ctx context.Context,
	conn *sql.Conn,
	sessionID string,
) error {
	result, err := conn.ExecContext(ctx, `
		UPDATE pr_development_repair_sessions
		SET claim_suppressed = 1
		WHERE id = ? AND claim_suppressed = 0`,
		sessionID,
	)
	if err != nil {
		return err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
		if rowsErr != nil {
			return rowsErr
		}
		return fmt.Errorf("stored pull request development repair suppression changed unexpectedly")
	}
	return nil
}

// RenewPRDevelopmentRepairLease monotonically extends a live owned preparing
// or running lease under an immediate transaction. Private heartbeat changes
// deliberately do not advance public repair version or timestamps.
func (s *Store) RenewPRDevelopmentRepairLease(
	ctx context.Context,
	attemptID, leaseToken string,
	lease time.Duration,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	attemptID = strings.TrimSpace(attemptID)
	leaseToken = strings.TrimSpace(leaseToken)
	if !validPrefixedHexID(attemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPRDevelopmentRepairIdentity(leaseToken, maxPRDevelopmentRepairLeaseBytes) ||
		lease <= 0 {
		return fmt.Errorf(
			"%w: valid attempt ID, lease token, and positive lease are required",
			ErrInvalidPRDevelopmentRepair,
		)
	}
	err := s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		leaseUntil := now.Add(lease).UTC()
		if validationErr := validateDBTimestamp(
			"pull request development repair lease deadline",
			leaseUntil,
		); validationErr != nil {
			return validationErr
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_attempts
			SET lease_until = CASE WHEN lease_until < ? THEN ? ELSE lease_until END
			WHERE id = ? AND status IN (?, ?) AND lease_token = ?
			  AND lease_until > ?`,
			toDBTime(leaseUntil),
			toDBTime(leaseUntil),
			attemptID,
			PRDevelopmentRepairPreparing,
			PRDevelopmentRepairRunning,
			leaseToken,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
			if rowsErr != nil {
				return rowsErr
			}
			var exists int
			queryErr := conn.QueryRowContext(ctx, `
				SELECT 1 FROM pr_development_repair_attempts WHERE id = ?`,
				attemptID,
			).Scan(&exists)
			if errors.Is(queryErr, sql.ErrNoRows) {
				return ErrNotFound
			}
			if queryErr != nil {
				return queryErr
			}
			return ErrStaleLease
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf(
			"renew pull request development repair lease: %w",
			s.dbError(err),
		)
	}
	return nil
}

// PinPRDevelopmentRepairSession immutably stores the exact live provider pin
// under a live preparing lease. An exact repeat is idempotent and does not
// advance the repair version.
func (s *Store) PinPRDevelopmentRepairSession(
	ctx context.Context,
	input PRDevelopmentRepairPin,
) (PRDevelopmentRepairSession, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentRepairSession{}, err
	}
	normalized, err := normalizePRDevelopmentRepairPin(input)
	if err != nil {
		return PRDevelopmentRepairSession{}, err
	}
	var session PRDevelopmentRepairSession
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		stored, loadErr := loadPRDevelopmentRepairSessionByAttempt(
			ctx,
			conn,
			normalized.AttemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		attempt := findPRDevelopmentRepairAttempt(&stored, normalized.AttemptID)
		if !livePRDevelopmentRepairLease(
			attempt,
			normalized.LeaseToken,
			now,
			PRDevelopmentRepairPreparing,
		) {
			return ErrStaleLease
		}
		if stored.HeadRepository != "" {
			if stored.HeadRepository != normalized.HeadRepository ||
				stored.HeadRef != normalized.HeadRef ||
				stored.HeadSHA != normalized.HeadSHA ||
				stored.CloneURL != normalized.CloneURL ||
				stored.ReviewDigest != normalized.ReviewDigest {
				return fmt.Errorf(
					"%w: repair session provider pin is immutable",
					ErrPRDevelopmentRepairConflict,
				)
			}
			session = stored
			return nil
		}
		result, updateErr := conn.ExecContext(ctx, `
				UPDATE pr_development_repair_sessions
				SET head_repository = ?, head_ref = ?, head_sha = ?, clone_url = ?,
				    review_digest = ?, version = version + 1, updated_at = ?
				WHERE id = ? AND head_repository = '' AND version = ? AND version <= ?`,
			normalized.HeadRepository,
			normalized.HeadRef,
			normalized.HeadSHA,
			normalized.CloneURL,
			normalized.ReviewDigest,
			toDBTime(now),
			stored.ID,
			stored.Version,
			MaxPRDevelopmentRepairVersion-
				prDevelopmentRepairUnpinnedPreparingTransitions,
		)
		if updateErr != nil {
			return updateErr
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
			if rowsErr != nil {
				return rowsErr
			}
			return fmt.Errorf("stored pull request development repair session changed unexpectedly")
		}
		session, loadErr = loadPRDevelopmentRepairSessionByID(ctx, conn, stored.ID)
		return loadErr
	})
	if err != nil {
		return PRDevelopmentRepairSession{}, fmt.Errorf(
			"pin pull request development repair session: %w",
			s.dbError(err),
		)
	}
	return session, nil
}

// BeginPRDevelopmentRepair advances a durably pinned live preparing claim to
// running immediately before the isolated runner is invoked.
func (s *Store) BeginPRDevelopmentRepair(
	ctx context.Context,
	input PRDevelopmentRepairBegin,
) (PRDevelopmentRepairSession, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentRepairSession{}, err
	}
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	if !validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPRDevelopmentRepairIdentity(input.LeaseToken, maxPRDevelopmentRepairLeaseBytes) ||
		input.Lease <= 0 {
		return PRDevelopmentRepairSession{}, fmt.Errorf(
			"%w: valid attempt ID, lease token, and positive lease are required",
			ErrInvalidPRDevelopmentRepair,
		)
	}
	var session PRDevelopmentRepairSession
	err := s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		leaseUntil := now.Add(input.Lease).UTC()
		if validationErr := validateDBTimestamp(
			"pull request development repair execution lease deadline",
			leaseUntil,
		); validationErr != nil {
			return validationErr
		}
		stored, loadErr := loadPRDevelopmentRepairSessionByAttempt(
			ctx,
			conn,
			input.AttemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		attempt := findPRDevelopmentRepairAttempt(&stored, input.AttemptID)
		if !livePRDevelopmentRepairLease(
			attempt,
			input.LeaseToken,
			now,
			PRDevelopmentRepairPreparing,
		) {
			return ErrStaleLease
		}
		if stored.HeadRepository == "" {
			return fmt.Errorf(
				"%w: repair session must be durably pinned before execution",
				ErrInvalidTransition,
			)
		}
		if stored.Version > MaxPRDevelopmentRepairVersion-
			prDevelopmentRepairPinnedPreparingTransitions {
			return fmt.Errorf(
				"%w: repair session lacks terminal revision headroom",
				ErrInvalidTransition,
			)
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_attempts
			SET status = ?, lease_until = ?, updated_at = ?
			WHERE id = ? AND status = ? AND lease_token = ? AND lease_until > ?`,
			PRDevelopmentRepairRunning,
			toDBTime(leaseUntil),
			toDBTime(now),
			input.AttemptID,
			PRDevelopmentRepairPreparing,
			input.LeaseToken,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
			if rowsErr != nil {
				return rowsErr
			}
			return ErrStaleLease
		}
		if bumpErr := bumpPRDevelopmentRepairSessionForAttempt(
			ctx,
			conn,
			input.AttemptID,
			now,
		); bumpErr != nil {
			return bumpErr
		}
		session, loadErr = loadPRDevelopmentRepairSessionByAttempt(
			ctx,
			conn,
			input.AttemptID,
		)
		return loadErr
	})
	if err != nil {
		return PRDevelopmentRepairSession{}, fmt.Errorf(
			"begin pull request development repair: %w",
			s.dbError(err),
		)
	}
	return session, nil
}

// FinishPRDevelopmentRepair records a terminal, safe bounded outcome under a
// live lease and advances the singleton session atomically.
func (s *Store) FinishPRDevelopmentRepair(
	ctx context.Context,
	input PRDevelopmentRepairOutcome,
) (PRDevelopmentRepairSession, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentRepairSession{}, err
	}
	normalized, err := s.normalizePRDevelopmentRepairOutcome(input)
	if err != nil {
		return PRDevelopmentRepairSession{}, err
	}
	var session PRDevelopmentRepairSession
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		stored, loadErr := loadPRDevelopmentRepairSessionByAttempt(
			ctx,
			conn,
			normalized.AttemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		attempt := findPRDevelopmentRepairAttempt(&stored, normalized.AttemptID)
		if attempt == nil || attempt.LeaseToken != normalized.LeaseToken ||
			attempt.LeaseUntil == nil || !attempt.LeaseUntil.After(now) ||
			(attempt.Status != PRDevelopmentRepairPreparing &&
				attempt.Status != PRDevelopmentRepairRunning) {
			return ErrStaleLease
		}
		switch normalized.Status {
		case PRDevelopmentRepairFailed:
			if attempt.Status != PRDevelopmentRepairPreparing {
				return fmt.Errorf(
					"%w: failed repair must finish from preparing",
					ErrInvalidTransition,
				)
			}
		case PRDevelopmentRepairCompleted, PRDevelopmentRepairRecoveryRequired:
			if attempt.Status != PRDevelopmentRepairRunning {
				return fmt.Errorf(
					"%w: executable repair outcome must finish from running",
					ErrInvalidTransition,
				)
			}
		}
		if attempt.Status == PRDevelopmentRepairPreparing && normalized.WorkspaceID != "" {
			return fmt.Errorf(
				"%w: preparing repair cannot report an acquired workspace",
				ErrInvalidPRDevelopmentRepair,
			)
		}
		workspaceID := stored.WorkspaceID
		if workspaceID != "" && normalized.WorkspaceID != "" &&
			workspaceID != normalized.WorkspaceID {
			return fmt.Errorf(
				"%w: repair session workspace is immutable",
				ErrPRDevelopmentRepairConflict,
			)
		}
		if workspaceID == "" {
			workspaceID = normalized.WorkspaceID
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_attempts
			SET status = ?, lease_owner = '', lease_token = '', lease_until = NULL,
			    summary = ?, error_code = ?, internal_error = ?, iterations = ?,
			    updated_at = ?
			WHERE id = ? AND status = ? AND lease_token = ? AND lease_until > ?`,
			normalized.Status,
			normalized.Summary,
			normalized.ErrorCode,
			normalized.InternalError,
			normalized.Iterations,
			toDBTime(now),
			normalized.AttemptID,
			attempt.Status,
			normalized.LeaseToken,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
			if rowsErr != nil {
				return rowsErr
			}
			return ErrStaleLease
		}
		result, updateErr = conn.ExecContext(ctx, `
			UPDATE pr_development_repair_sessions
			SET workspace_id = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND version = ? AND version < ?`,
			workspaceID,
			toDBTime(now),
			stored.ID,
			stored.Version,
			MaxPRDevelopmentRepairVersion,
		)
		if updateErr != nil {
			return updateErr
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
			if rowsErr != nil {
				return rowsErr
			}
			return fmt.Errorf("stored pull request development repair session changed unexpectedly")
		}
		session, loadErr = loadPRDevelopmentRepairSessionByID(ctx, conn, stored.ID)
		return loadErr
	})
	if err != nil {
		return PRDevelopmentRepairSession{}, fmt.Errorf(
			"finish pull request development repair: %w",
			s.dbError(err),
		)
	}
	return session, nil
}

func loadPRDevelopmentWorkbench(
	ctx context.Context,
	queryer rowsQueryer,
	caseID string,
) (PRDevelopmentWorkbench, error) {
	storedCase, err := getPRDevelopmentCaseRecord(ctx, queryer, caseID)
	if err != nil {
		return PRDevelopmentWorkbench{}, err
	}
	thread, err := loadPRDevelopmentThreadBindingForCase(ctx, queryer, caseID)
	if err != nil {
		return PRDevelopmentWorkbench{}, err
	}
	conversation, err := loadPRDevelopmentConversation(ctx, queryer, caseID)
	if err != nil {
		return PRDevelopmentWorkbench{}, err
	}
	session, found, err := loadPRDevelopmentRepairSessionByCase(ctx, queryer, caseID)
	if err != nil {
		return PRDevelopmentWorkbench{}, err
	}
	if found {
		for _, attempt := range session.Attempts {
			if attempt.ConversationVersion > conversation.Conversation.Version {
				return PRDevelopmentWorkbench{}, fmt.Errorf(
					"stored pull request development repair references a future conversation version",
				)
			}
		}
	}
	workbench := PRDevelopmentWorkbench{
		Case:         storedCase.Case,
		Thread:       &thread,
		Conversation: conversation.Conversation,
	}
	if found {
		workbench.RepairSession = &session
	}
	return workbench, nil
}

func insertPRDevelopmentRepairSession(
	ctx context.Context,
	conn *sql.Conn,
	input PRDevelopmentRepairAdmit,
	now time.Time,
) error {
	sessionID, err := newPrefixedID(prDevelopmentRepairSessionIDPrefix)
	if err != nil {
		return err
	}
	attemptID, err := newPrefixedID(prDevelopmentRepairAttemptIDPrefix)
	if err != nil {
		return err
	}
	reservationKey, err := newUniquePRDevelopmentRepairReservation(ctx, conn)
	if err != nil {
		return err
	}
	if _, insertErr := conn.ExecContext(ctx, `
		INSERT INTO pr_development_repair_sessions (
			id, case_id, version, agent_id, reservation_key, created_at, updated_at
		) VALUES (?, ?, 1, ?, ?, ?, ?)`,
		sessionID,
		input.CaseID,
		input.AgentID,
		reservationKey,
		toDBTime(now),
		toDBTime(now),
	); insertErr != nil {
		return insertErr
	}
	_, err = conn.ExecContext(ctx, `
		INSERT INTO pr_development_repair_attempts (
			id, session_id, ordinal, expected_repair_version,
			conversation_version, idempotency_key, instruction, status,
			created_at, updated_at
		) VALUES (?, ?, 0, 0, ?, ?, ?, ?, ?, ?)`,
		attemptID,
		sessionID,
		input.ExpectedConversationVersion,
		input.IdempotencyKey,
		input.Instruction,
		PRDevelopmentRepairQueued,
		toDBTime(now),
		toDBTime(now),
	)
	return err
}

func newUniquePRDevelopmentRepairReservation(
	ctx context.Context,
	queryer rowsQueryer,
) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		reservation, err := newPrefixedID(prDevelopmentRepairReservationPrefix)
		if err != nil {
			return "", err
		}
		owners, err := countPRDevelopmentRepairReservationOwners(
			ctx,
			queryer,
			reservation,
		)
		if err != nil {
			return "", err
		}
		if owners == 0 {
			return reservation, nil
		}
	}
	return "", errors.New("generate unique pull request development repair reservation")
}

func countPRDevelopmentRepairReservationOwners(
	ctx context.Context,
	queryer rowQueryer,
	reservation string,
) (int, error) {
	var owners int
	err := queryer.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM pr_development_repair_sessions
		WHERE reservation_key = ?`, reservation).Scan(&owners)
	return owners, err
}

func appendPRDevelopmentRepairAttempt(
	ctx context.Context,
	conn *sql.Conn,
	session *PRDevelopmentRepairSession,
	input PRDevelopmentRepairAdmit,
	now time.Time,
) error {
	attemptID, err := newPrefixedID(prDevelopmentRepairAttemptIDPrefix)
	if err != nil {
		return err
	}
	if _, insertErr := conn.ExecContext(ctx, `
		INSERT INTO pr_development_repair_attempts (
			id, session_id, ordinal, expected_repair_version,
			conversation_version, idempotency_key, instruction, status,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attemptID,
		session.ID,
		len(session.Attempts),
		input.ExpectedRepairVersion,
		input.ExpectedConversationVersion,
		input.IdempotencyKey,
		input.Instruction,
		PRDevelopmentRepairQueued,
		toDBTime(now),
		toDBTime(now),
	); insertErr != nil {
		return insertErr
	}
	result, err := conn.ExecContext(ctx, `
		UPDATE pr_development_repair_sessions
		SET version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND version <= ?`,
		toDBTime(now),
		session.ID,
		session.Version,
		maxPRDevelopmentRepairVersionBeforeAdmission(session.HeadRepository != ""),
	)
	if err != nil {
		return err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
		if rowsErr != nil {
			return rowsErr
		}
		return fmt.Errorf("stored pull request development repair session changed unexpectedly")
	}
	return nil
}

func expireRunningPRDevelopmentRepairs(
	ctx context.Context,
	conn *sql.Conn,
	now time.Time,
) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT attempt.id, attempt.session_id
		FROM pr_development_repair_attempts AS attempt
		JOIN pr_development_repair_sessions AS session
		  ON session.id = attempt.session_id
		WHERE attempt.status = ? AND attempt.lease_until <= ?
		  AND session.head_repository <> ''
		  AND session.claim_suppressed = 0
		  AND NOT EXISTS (
			SELECT 1 FROM pr_development_thread_controllers AS controller
			WHERE controller.owner_session_id = session.id
		  )
		  AND session.version >= 1 AND session.version < ?
		ORDER BY attempt.created_at, attempt.id
		LIMIT ?`,
		PRDevelopmentRepairRunning,
		toDBTime(now),
		MaxPRDevelopmentRepairVersion,
		maxPRDevelopmentRepairClaimCandidates,
	)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type expiredRepair struct {
		attemptID string
		sessionID string
	}
	expired := make([]expiredRepair, 0)
	for rows.Next() {
		var item expiredRepair
		if scanErr := rows.Scan(&item.attemptID, &item.sessionID); scanErr != nil {
			return scanErr
		}
		expired = append(expired, item)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return rowsErr
	}
	if closeErr := rows.Close(); closeErr != nil {
		return closeErr
	}
	for _, item := range expired {
		result, err := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_attempts
			SET status = ?, lease_owner = '', lease_token = '', lease_until = NULL,
			    summary = ?, error_code = ?, internal_error = ?, updated_at = ?
			WHERE id = ? AND status = ? AND lease_until <= ?`,
			PRDevelopmentRepairRecoveryRequired,
			"Local repair ownership expired after execution began; inspect the preserved workspace before continuing.",
			PRDevelopmentRepairErrorRecoveryRequired,
			"repair worker lease expired after the running transition",
			toDBTime(now),
			item.attemptID,
			PRDevelopmentRepairRunning,
			toDBTime(now),
		)
		if err != nil {
			return err
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
			if rowsErr != nil {
				return rowsErr
			}
			return fmt.Errorf("expired pull request development repair changed unexpectedly")
		}
		result, err = conn.ExecContext(ctx, `
			UPDATE pr_development_repair_sessions
			SET version = version + 1, updated_at = ?
			WHERE id = ? AND version < ?`,
			toDBTime(now),
			item.sessionID,
			MaxPRDevelopmentRepairVersion,
		)
		if err != nil {
			return err
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
			if rowsErr != nil {
				return rowsErr
			}
			return fmt.Errorf("expired pull request development repair session is missing")
		}
	}
	return nil
}

func maxPRDevelopmentRepairVersionBeforeAdmission(pinned bool) int64 {
	transitions := prDevelopmentRepairUnpinnedQueuedTransitions
	if pinned {
		transitions = prDevelopmentRepairPinnedQueuedTransitions
	}
	// Appending the queued attempt is itself one additional public transition.
	return MaxPRDevelopmentRepairVersion - transitions - 1
}

func bumpPRDevelopmentRepairSessionForAttempt(
	ctx context.Context,
	conn *sql.Conn,
	attemptID string,
	now time.Time,
) error {
	result, err := conn.ExecContext(ctx, `
		UPDATE pr_development_repair_sessions
		SET version = version + 1, updated_at = ?
		WHERE id = (
			SELECT session_id FROM pr_development_repair_attempts WHERE id = ?
		) AND version < ?`,
		toDBTime(now),
		attemptID,
		MaxPRDevelopmentRepairVersion,
	)
	if err != nil {
		return err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
		if rowsErr != nil {
			return rowsErr
		}
		return fmt.Errorf("pull request development repair session is missing")
	}
	return nil
}

func loadPRDevelopmentRepairSessionByCase(
	ctx context.Context,
	queryer rowsQueryer,
	caseID string,
) (PRDevelopmentRepairSession, bool, error) {
	session, err := scanPRDevelopmentRepairSession(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentRepairSessionColumns+`
		FROM pr_development_repair_sessions
		WHERE case_id = ?`,
		caseID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentRepairSession{}, false, nil
	}
	if err != nil {
		return PRDevelopmentRepairSession{}, false, err
	}
	loaded, err := loadPRDevelopmentRepairAttempts(ctx, queryer, session)
	return loaded, true, err
}

func loadPRDevelopmentRepairSessionByID(
	ctx context.Context,
	queryer rowsQueryer,
	sessionID string,
) (PRDevelopmentRepairSession, error) {
	session, err := scanPRDevelopmentRepairSession(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentRepairSessionColumns+`
		FROM pr_development_repair_sessions
		WHERE id = ?`,
		sessionID,
	))
	if err != nil {
		return PRDevelopmentRepairSession{}, err
	}
	return loadPRDevelopmentRepairAttempts(ctx, queryer, session)
}

func loadPRDevelopmentRepairSessionByAttempt(
	ctx context.Context,
	queryer rowsQueryer,
	attemptID string,
) (PRDevelopmentRepairSession, error) {
	var sessionID string
	if err := queryer.QueryRowContext(ctx, `
		SELECT session_id
		FROM pr_development_repair_attempts
		WHERE id = ?`,
		attemptID,
	).Scan(&sessionID); err != nil {
		return PRDevelopmentRepairSession{}, err
	}
	return loadPRDevelopmentRepairSessionByID(ctx, queryer, sessionID)
}

func scanPRDevelopmentRepairSession(
	scanner rowScanner,
) (PRDevelopmentRepairSession, error) {
	var (
		session              PRDevelopmentRepairSession
		createdAt, updatedAt int64
	)
	if err := scanner.Scan(
		&session.ID,
		&session.CaseID,
		&session.Version,
		&session.AgentID,
		&session.HeadRepository,
		&session.HeadRef,
		&session.HeadSHA,
		&session.CloneURL,
		&session.ReviewDigest,
		&session.ReservationKey,
		&session.WorkspaceID,
		&createdAt,
		&updatedAt,
	); err != nil {
		return PRDevelopmentRepairSession{}, err
	}
	session.CreatedAt = fromDBTime(createdAt)
	session.UpdatedAt = fromDBTime(updatedAt)
	return session, nil
}

func loadPRDevelopmentRepairAttempts(
	ctx context.Context,
	queryer rowsQueryer,
	session PRDevelopmentRepairSession,
) (PRDevelopmentRepairSession, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT `+prDevelopmentRepairAttemptColumns+`
		FROM pr_development_repair_attempts
		WHERE session_id = ?
		ORDER BY ordinal`,
		session.ID,
	)
	if err != nil {
		return PRDevelopmentRepairSession{}, err
	}
	defer func() { _ = rows.Close() }()
	session.Attempts = make([]PRDevelopmentRepairAttempt, 0)
	for rows.Next() {
		attempt, scanErr := scanPRDevelopmentRepairAttempt(rows)
		if scanErr != nil {
			return PRDevelopmentRepairSession{}, scanErr
		}
		session.Attempts = append(session.Attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return PRDevelopmentRepairSession{}, err
	}
	if err := validateStoredPRDevelopmentRepairSession(session); err != nil {
		return PRDevelopmentRepairSession{}, wrapInvalidStoredPRDevelopmentRepair(err)
	}
	return session, nil
}

func scanPRDevelopmentRepairAttempt(
	scanner rowScanner,
) (PRDevelopmentRepairAttempt, error) {
	var (
		attempt              PRDevelopmentRepairAttempt
		ordinal              int64
		claims               int64
		iterations           int64
		leaseUntil           sql.NullInt64
		createdAt, updatedAt int64
	)
	if err := scanner.Scan(
		&attempt.ID,
		&attempt.SessionID,
		&ordinal,
		&attempt.ExpectedRepairVersion,
		&attempt.ConversationVersion,
		&attempt.IdempotencyKey,
		&attempt.Instruction,
		&attempt.Status,
		&attempt.LeaseOwner,
		&attempt.LeaseToken,
		&leaseUntil,
		&claims,
		&attempt.Summary,
		&attempt.ErrorCode,
		&attempt.InternalError,
		&iterations,
		&createdAt,
		&updatedAt,
	); err != nil {
		return PRDevelopmentRepairAttempt{}, err
	}
	attempt.Ordinal = int(ordinal)
	attempt.Claims = int(claims)
	attempt.Iterations = int(iterations)
	if int64(attempt.Ordinal) != ordinal || int64(attempt.Claims) != claims ||
		int64(attempt.Iterations) != iterations {
		return PRDevelopmentRepairAttempt{}, wrapInvalidStoredPRDevelopmentRepair(
			errors.New("stored pull request development repair attempt integer overflows"),
		)
	}
	attempt.LeaseUntil = fromNullableTime(leaseUntil)
	attempt.CreatedAt = fromDBTime(createdAt)
	attempt.UpdatedAt = fromDBTime(updatedAt)
	if err := validateStoredPRDevelopmentRepairAttempt(attempt); err != nil {
		return PRDevelopmentRepairAttempt{}, wrapInvalidStoredPRDevelopmentRepair(err)
	}
	return attempt, nil
}

func wrapInvalidStoredPRDevelopmentRepair(err error) error {
	return fmt.Errorf("%w: %v", errInvalidStoredPRDevelopmentRepair, err)
}

func validateStoredPRDevelopmentRepairSession(
	session PRDevelopmentRepairSession,
) error {
	if !validPrefixedHexID(session.ID, prDevelopmentRepairSessionIDPrefix) ||
		!validPrefixedHexID(session.CaseID, prDevelopmentCaseIDPrefix) ||
		session.Version < 1 ||
		session.Version > MaxPRDevelopmentRepairVersion ||
		!validPRDevelopmentRepairAgentID(session.AgentID) ||
		!validPrefixedHexID(session.ReservationKey, prDevelopmentRepairReservationPrefix) ||
		validateDBTimestamp("repair session creation time", session.CreatedAt) != nil ||
		validateDBTimestamp("repair session update time", session.UpdatedAt) != nil ||
		session.UpdatedAt.Before(session.CreatedAt) ||
		!validOptionalPRDevelopmentRepairIdentity(
			session.WorkspaceID,
			maxPRDevelopmentRepairWorkspaceBytes,
		) {
		return fmt.Errorf("stored pull request development repair session is invalid")
	}
	pinned := session.HeadRepository != ""
	if pinned != (session.HeadRef != "") ||
		pinned != (session.HeadSHA != "") ||
		pinned != (session.CloneURL != "") ||
		pinned != (session.ReviewDigest != "") ||
		(session.WorkspaceID != "" && !pinned) {
		return fmt.Errorf("stored pull request development repair pin is partial")
	}
	if pinned && (!validPRDevelopmentRepository(session.HeadRepository) ||
		!validPRDevelopmentGitRef(session.HeadRef) ||
		!validPRDevelopmentHex(session.HeadSHA, 40, 64) ||
		!validPRDevelopmentRepairCloneURL(session.CloneURL) ||
		!validPRDevelopmentRepairReviewDigest(session.ReviewDigest)) {
		return fmt.Errorf("stored pull request development repair pin is invalid")
	}
	if len(session.Attempts) == 0 ||
		len(session.Attempts) > MaxPRDevelopmentRepairAttempts {
		return fmt.Errorf("stored pull request development repair attempt count is invalid")
	}
	active := 0
	completed := false
	markers := make(map[string]struct{}, len(session.Attempts))
	for ordinal, attempt := range session.Attempts {
		if attempt.SessionID != session.ID || attempt.Ordinal != ordinal ||
			attempt.ExpectedRepairVersion >= session.Version ||
			attempt.CreatedAt.Before(session.CreatedAt) ||
			attempt.UpdatedAt.After(session.UpdatedAt) {
			return fmt.Errorf("stored pull request development repair attempt ordering is invalid")
		}
		if ordinal == 0 && attempt.ExpectedRepairVersion != 0 {
			return fmt.Errorf("stored first pull request development repair version is invalid")
		}
		if ordinal > 0 {
			previous := session.Attempts[ordinal-1]
			if attempt.CreatedAt.Before(previous.CreatedAt) ||
				attempt.ExpectedRepairVersion <= previous.ExpectedRepairVersion {
				return fmt.Errorf("stored pull request development repair history is not monotonic")
			}
		}
		if _, duplicate := markers[attempt.IdempotencyKey]; duplicate {
			return fmt.Errorf("stored pull request development repair idempotency is duplicated")
		}
		markers[attempt.IdempotencyKey] = struct{}{}
		if activePRDevelopmentRepairStatus(attempt.Status) {
			active++
			if ordinal != len(session.Attempts)-1 {
				return fmt.Errorf("stored nonterminal pull request development repair is not latest")
			}
		}
		if attempt.Status == PRDevelopmentRepairCompleted {
			completed = true
		}
		if (attempt.Status == PRDevelopmentRepairRunning ||
			attempt.Status == PRDevelopmentRepairCompleted ||
			attempt.Status == PRDevelopmentRepairRecoveryRequired) && !pinned {
			return fmt.Errorf("stored executable pull request development repair is not pinned")
		}
	}
	if active > 1 {
		return fmt.Errorf("stored pull request development repair has multiple active attempts")
	}
	if completed && session.WorkspaceID == "" {
		return fmt.Errorf("stored completed pull request development repair has no workspace")
	}
	return nil
}

func validateStoredPRDevelopmentRepairAttempt(
	attempt PRDevelopmentRepairAttempt,
) error {
	if !validPrefixedHexID(attempt.ID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(attempt.SessionID, prDevelopmentRepairSessionIDPrefix) ||
		attempt.Ordinal < 0 || attempt.Ordinal >= MaxPRDevelopmentRepairAttempts ||
		attempt.ExpectedRepairVersion < 0 ||
		attempt.ExpectedRepairVersion > MaxPRDevelopmentRepairVersion ||
		attempt.ConversationVersion < 0 ||
		attempt.ConversationVersion > MaxPRDevelopmentMessagesPerCase ||
		!validStoredPRDevelopmentRepairText(
			attempt.IdempotencyKey,
			MaxPRDevelopmentRepairIdempotencyBytes,
		) ||
		!validStoredPRDevelopmentRepairText(
			attempt.Instruction,
			MaxPRDevelopmentRepairInstructionBytes,
		) ||
		attempt.Claims < 0 ||
		attempt.Iterations < 0 ||
		attempt.Iterations > MaxPRDevelopmentRepairIterations ||
		validateDBTimestamp("repair attempt creation time", attempt.CreatedAt) != nil ||
		validateDBTimestamp("repair attempt update time", attempt.UpdatedAt) != nil ||
		attempt.UpdatedAt.Before(attempt.CreatedAt) {
		return fmt.Errorf("stored pull request development repair attempt is invalid")
	}
	leased := attempt.Status == PRDevelopmentRepairPreparing ||
		attempt.Status == PRDevelopmentRepairRunning
	if leased != (attempt.LeaseOwner != "") ||
		leased != (attempt.LeaseToken != "") ||
		leased != (attempt.LeaseUntil != nil) {
		return fmt.Errorf("stored pull request development repair lease is partial")
	}
	if leased {
		if !validPRDevelopmentRepairIdentity(
			attempt.LeaseOwner,
			maxPRDevelopmentRepairWorkerBytes,
		) || !validPRDevelopmentRepairIdentity(
			attempt.LeaseToken,
			maxPRDevelopmentRepairLeaseBytes,
		) || validateDBTimestamp("repair attempt lease deadline", *attempt.LeaseUntil) != nil {
			return fmt.Errorf("stored pull request development repair lease is invalid")
		}
	}
	switch attempt.Status {
	case PRDevelopmentRepairQueued:
		if attempt.Claims != 0 || !emptyPRDevelopmentRepairOutcome(attempt) {
			return fmt.Errorf("stored queued pull request development repair is invalid")
		}
	case PRDevelopmentRepairPreparing, PRDevelopmentRepairRunning:
		if attempt.Claims < 1 || !emptyPRDevelopmentRepairOutcome(attempt) {
			return fmt.Errorf("stored active pull request development repair is invalid")
		}
	case PRDevelopmentRepairCompleted:
		if attempt.Claims < 1 ||
			!validStoredPRDevelopmentRepairText(
				attempt.Summary,
				MaxPRDevelopmentRepairSummaryBytes,
			) || attempt.ErrorCode != "" || attempt.InternalError != "" ||
			attempt.Iterations < 1 {
			return fmt.Errorf("stored completed pull request development repair is invalid")
		}
	case PRDevelopmentRepairFailed:
		if attempt.Claims < 1 ||
			!validStoredPRDevelopmentRepairText(
				attempt.Summary,
				MaxPRDevelopmentRepairSummaryBytes,
			) || !validPRDevelopmentRepairFailureCode(attempt.ErrorCode) ||
			!validOptionalPRDevelopmentRepairText(attempt.InternalError, maxErrorDetailBytes) {
			return fmt.Errorf("stored failed pull request development repair is invalid")
		}
	case PRDevelopmentRepairRecoveryRequired:
		if attempt.Claims < 1 ||
			!validStoredPRDevelopmentRepairText(
				attempt.Summary,
				MaxPRDevelopmentRepairSummaryBytes,
			) || attempt.ErrorCode != PRDevelopmentRepairErrorRecoveryRequired ||
			!validOptionalPRDevelopmentRepairText(attempt.InternalError, maxErrorDetailBytes) {
			return fmt.Errorf("stored recovery-required pull request development repair is invalid")
		}
	default:
		return fmt.Errorf("stored pull request development repair status is invalid")
	}
	return nil
}

func normalizePRDevelopmentRepairAdmit(
	input PRDevelopmentRepairAdmit,
) (PRDevelopmentRepairAdmit, error) {
	input.CaseID = strings.TrimSpace(input.CaseID)
	input.AgentID = strings.TrimSpace(input.AgentID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.Instruction = strings.TrimSpace(input.Instruction)
	if !validPrefixedHexID(input.CaseID, prDevelopmentCaseIDPrefix) ||
		input.ExpectedConversationVersion < 0 ||
		input.ExpectedConversationVersion > MaxPRDevelopmentMessagesPerCase ||
		input.ExpectedRepairVersion < 0 ||
		input.ExpectedRepairVersion > MaxPRDevelopmentRepairVersion ||
		!validPRDevelopmentRepairAgentID(input.AgentID) ||
		!validStoredPRDevelopmentRepairText(
			input.IdempotencyKey,
			MaxPRDevelopmentRepairIdempotencyBytes,
		) ||
		!validStoredPRDevelopmentRepairText(
			input.Instruction,
			MaxPRDevelopmentRepairInstructionBytes,
		) {
		return PRDevelopmentRepairAdmit{}, fmt.Errorf(
			"%w: valid case, versions, idempotency key, agent, and instruction are required",
			ErrInvalidPRDevelopmentRepair,
		)
	}
	return input, nil
}

func normalizePRDevelopmentRepairPin(
	input PRDevelopmentRepairPin,
) (PRDevelopmentRepairPin, error) {
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	input.HeadRepository = strings.TrimSpace(input.HeadRepository)
	input.HeadRef = strings.TrimSpace(input.HeadRef)
	input.HeadSHA = strings.TrimSpace(input.HeadSHA)
	input.CloneURL = strings.TrimSpace(input.CloneURL)
	input.ReviewDigest = strings.TrimSpace(input.ReviewDigest)
	if !validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPRDevelopmentRepairIdentity(input.LeaseToken, maxPRDevelopmentRepairLeaseBytes) ||
		!validPRDevelopmentRepository(input.HeadRepository) ||
		!validPRDevelopmentGitRef(input.HeadRef) ||
		!validPRDevelopmentHex(input.HeadSHA, 40, 64) ||
		!validPRDevelopmentRepairCloneURL(input.CloneURL) ||
		!validPRDevelopmentRepairReviewDigest(input.ReviewDigest) {
		return PRDevelopmentRepairPin{}, fmt.Errorf(
			"%w: valid attempt, lease, head pin, clone URL, and review digest are required",
			ErrInvalidPRDevelopmentRepair,
		)
	}
	return input, nil
}

func (s *Store) normalizePRDevelopmentRepairOutcome(
	input PRDevelopmentRepairOutcome,
) (PRDevelopmentRepairOutcome, error) {
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	input.Summary = strings.TrimSpace(input.Summary)
	input.WorkspaceID = strings.TrimSpace(input.WorkspaceID)
	if !validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPRDevelopmentRepairIdentity(input.LeaseToken, maxPRDevelopmentRepairLeaseBytes) ||
		!validStoredPRDevelopmentRepairText(
			input.Summary,
			MaxPRDevelopmentRepairSummaryBytes,
		) || !validOptionalPRDevelopmentRepairIdentity(
		input.WorkspaceID,
		maxPRDevelopmentRepairWorkspaceBytes,
	) || input.Iterations < 0 ||
		input.Iterations > MaxPRDevelopmentRepairIterations {
		return PRDevelopmentRepairOutcome{}, fmt.Errorf(
			"%w: valid attempt, lease, summary, workspace, and iteration count are required",
			ErrInvalidPRDevelopmentRepair,
		)
	}
	switch input.Status {
	case PRDevelopmentRepairCompleted:
		if input.ErrorCode != "" || input.InternalError != "" || input.Iterations < 1 ||
			input.WorkspaceID == "" {
			return PRDevelopmentRepairOutcome{}, fmt.Errorf(
				"%w: completed repair requires iterations and workspace and forbids error fields",
				ErrInvalidPRDevelopmentRepair,
			)
		}
	case PRDevelopmentRepairFailed:
		if !validPRDevelopmentRepairFailureCode(input.ErrorCode) {
			return PRDevelopmentRepairOutcome{}, fmt.Errorf(
				"%w: failed repair requires a safe failure code",
				ErrInvalidPRDevelopmentRepair,
			)
		}
		input.InternalError = s.sanitizePRDevelopmentRepairInternalError(
			input.InternalError,
		)
	case PRDevelopmentRepairRecoveryRequired:
		if input.ErrorCode != PRDevelopmentRepairErrorRecoveryRequired {
			return PRDevelopmentRepairOutcome{}, fmt.Errorf(
				"%w: recovery-required repair requires its matching error code",
				ErrInvalidPRDevelopmentRepair,
			)
		}
		input.InternalError = s.sanitizePRDevelopmentRepairInternalError(
			input.InternalError,
		)
	default:
		return PRDevelopmentRepairOutcome{}, fmt.Errorf(
			"%w: outcome must be completed, failed, or recovery_required",
			ErrInvalidTransition,
		)
	}
	return input, nil
}

func (s *Store) sanitizePRDevelopmentRepairInternalError(value string) string {
	value = strings.ReplaceAll(value, "\x00", "\uFFFD")
	return s.sanitizeDetail(value)
}

func normalizePRDevelopmentRepairIdentity(
	field, value string,
	maximum int,
	required bool,
) (string, error) {
	value = strings.TrimSpace(value)
	if (!required && value == "") || validPRDevelopmentRepairIdentity(value, maximum) {
		return value, nil
	}
	return "", fmt.Errorf(
		"%w: %s must be bounded exact UTF-8 without control characters",
		ErrInvalidPRDevelopmentRepair,
		field,
	)
}

func validPRDevelopmentRepairAgentID(value string) bool {
	if len(value) < 1 || len(value) > MaxPRDevelopmentRepairAgentIDBytes {
		return false
	}
	for index, character := range []byte(value) {
		if character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' ||
			index > 0 && (character == '_' || character == '-') {
			continue
		}
		return false
	}
	return true
}

func validPRDevelopmentRepairIdentity(value string, maximum int) bool {
	if !validStoredPRDevelopmentRepairText(value, maximum) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	return true
}

func validOptionalPRDevelopmentRepairIdentity(value string, maximum int) bool {
	return value == "" || validPRDevelopmentRepairIdentity(value, maximum)
}

func validStoredPRDevelopmentRepairText(value string, maximum int) bool {
	return value != "" && value == strings.TrimSpace(value) &&
		len(value) <= maximum && utf8.ValidString(value) &&
		!strings.ContainsRune(value, '\x00')
}

func validOptionalPRDevelopmentRepairText(value string, maximum int) bool {
	return value == "" || len(value) <= maximum && utf8.ValidString(value) &&
		!strings.ContainsRune(value, '\x00')
}

func validPRDevelopmentRepairCloneURL(value string) bool {
	if !validPRDevelopmentRepairIdentity(value, maxPRDevelopmentURLBytes) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" &&
		parsed.User == nil
}

func validPRDevelopmentRepairReviewDigest(value string) bool {
	digest, found := strings.CutPrefix(value, "sha256:")
	return found && validPRDevelopmentHex(digest, 64)
}

func validPRDevelopmentRepairFailureCode(
	code PRDevelopmentRepairErrorCode,
) bool {
	switch code {
	case PRDevelopmentRepairErrorProviderChanged,
		PRDevelopmentRepairErrorNotActionable,
		PRDevelopmentRepairErrorRuntimeUnavailable,
		PRDevelopmentRepairErrorWorkspaceUnavailable,
		PRDevelopmentRepairErrorRepairFailed,
		PRDevelopmentRepairErrorInternal:
		return true
	default:
		return false
	}
}

func activePRDevelopmentRepairStatus(status PRDevelopmentRepairStatus) bool {
	return status == PRDevelopmentRepairQueued ||
		status == PRDevelopmentRepairPreparing ||
		status == PRDevelopmentRepairRunning
}

func activePRDevelopmentRepairAttempt(
	session *PRDevelopmentRepairSession,
) *PRDevelopmentRepairAttempt {
	if session == nil {
		return nil
	}
	for index := range session.Attempts {
		if activePRDevelopmentRepairStatus(session.Attempts[index].Status) {
			return &session.Attempts[index]
		}
	}
	return nil
}

func findPRDevelopmentRepairAttempt(
	session *PRDevelopmentRepairSession,
	attemptID string,
) *PRDevelopmentRepairAttempt {
	if session == nil {
		return nil
	}
	for index := range session.Attempts {
		if session.Attempts[index].ID == attemptID {
			return &session.Attempts[index]
		}
	}
	return nil
}

func findPRDevelopmentRepairIdempotency(
	session *PRDevelopmentRepairSession,
	key string,
) (PRDevelopmentRepairAttempt, bool) {
	if session == nil {
		return PRDevelopmentRepairAttempt{}, false
	}
	for _, attempt := range session.Attempts {
		if attempt.IdempotencyKey == key {
			return attempt, true
		}
	}
	return PRDevelopmentRepairAttempt{}, false
}

func livePRDevelopmentRepairLease(
	attempt *PRDevelopmentRepairAttempt,
	leaseToken string,
	now time.Time,
	status PRDevelopmentRepairStatus,
) bool {
	return attempt != nil && attempt.Status == status &&
		attempt.LeaseToken == leaseToken && attempt.LeaseUntil != nil &&
		attempt.LeaseUntil.After(now)
}

func emptyPRDevelopmentRepairOutcome(attempt PRDevelopmentRepairAttempt) bool {
	return attempt.Summary == "" && attempt.ErrorCode == "" &&
		attempt.InternalError == "" && attempt.Iterations == 0
}
