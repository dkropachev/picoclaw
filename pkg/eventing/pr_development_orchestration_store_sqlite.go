//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"
)

var _ PRDevelopmentRepairOrchestrationStore = (*Store)(nil)

const prDevelopmentRepairOrchestrationColumns = `
	orchestration.attempt_id, orchestration.session_id, orchestration.case_id,
	orchestration.thread_id, attempt.instruction, session.agent_id,
	orchestration.phase, orchestration.claim_owner, orchestration.claim_token,
	orchestration.claim_until, orchestration.claim_epoch, orchestration.claims,
	orchestration.head_repository, orchestration.head_ref, orchestration.head_sha,
	orchestration.clone_url, orchestration.review_digest,
	orchestration.workspace_id, orchestration.source_tree,
	orchestration.controller_id, orchestration.model_controller_revision,
	orchestration.model_line_id, orchestration.model_line_version,
	orchestration.model_mutation_epoch, orchestration.model_lease_epoch,
	orchestration.model_lease_token_digest, orchestration.model_reservation_digest,
	orchestration.context_digest,
	orchestration.prompt_digest, orchestration.model_result_digest,
	orchestration.summary, orchestration.iterations,
	orchestration.validation_controller_revision,
	orchestration.validation_line_id, orchestration.validation_line_version,
	orchestration.validation_mutation_epoch, orchestration.validation_lease_epoch,
	orchestration.validation_lease_token_digest,
	orchestration.validation_reservation_digest,
	orchestration.parent_commit, orchestration.parent_tree,
	orchestration.candidate_tree, orchestration.candidate_digest,
	orchestration.changed_files, orchestration.no_changes,
	orchestration.ci_status, orchestration.ci_attestation_id,
	orchestration.ci_attestation_digest, orchestration.ci_result_key,
	orchestration.ci_effective_plan_digest, orchestration.ci_execution_digest,
	orchestration.receipt_hash, orchestration.park_operation_id,
	orchestration.ledger_entry_id, orchestration.fence_hash,
	orchestration.failed_claim_token_digest,
	orchestration.created_at, orchestration.model_started_at,
	orchestration.model_completed_at, orchestration.validated_at,
	orchestration.completed_at, orchestration.failed_at,
	orchestration.recovery_required_at,
	orchestration.updated_at`

const maxPRDevelopmentRepairOrchestrationCandidates = 32

// ClaimPRDevelopmentRepairOrchestration atomically transfers an oldest
// provider-thread queued attempt out of the legacy queue, or reclaims a safe
// expired checkpoint. An expired Editing checkpoint is terminalized first and
// is never returned for another model invocation.
func (s *Store) ClaimPRDevelopmentRepairOrchestration(
	ctx context.Context,
	input PRDevelopmentRepairOrchestrationClaim,
) (PRDevelopmentRepairOrchestration, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentRepairOrchestration{}, false, err
	}
	worker, err := normalizePRDevelopmentRepairIdentity(
		"orchestration worker label",
		input.WorkerLabel,
		MaxPRDevelopmentControllerIdentityBytes,
		true,
	)
	if err != nil || input.Lease <= 0 {
		return PRDevelopmentRepairOrchestration{}, false, fmt.Errorf(
			"%w: bounded worker label and positive lease are required",
			ErrInvalidPRDevelopmentOrchestration,
		)
	}

	var (
		orchestration PRDevelopmentRepairOrchestration
		claimed       bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if expireErr := expireEditingPRDevelopmentRepairOrchestrations(
			ctx, conn, now,
		); expireErr != nil {
			return expireErr
		}
		deadline, deadlineErr := prDevelopmentControllerDeadline(now, input.Lease)
		if deadlineErr != nil {
			return deadlineErr
		}
		token, tokenErr := newLeaseToken(worker)
		if tokenErr != nil {
			return tokenErr
		}

		var existingAttemptID string
		queryErr := conn.QueryRowContext(ctx, `
			SELECT orchestration.attempt_id
			FROM pr_development_repair_orchestrations AS orchestration
			WHERE orchestration.phase IN ('bootstrap', 'edited', 'validated') AND
			      orchestration.claim_until <= ? AND
			      NOT EXISTS (
				      SELECT 1
				      FROM pr_development_controller_suspensions AS suspensions
				      WHERE suspensions.resume_attempt_id = orchestration.attempt_id AND
				            suspensions.status = 'resume_claimed'
			      ) AND
			      NOT EXISTS (
				      SELECT 1
				      FROM pr_development_controller_suspensions AS suspensions
				      WHERE suspensions.attempt_id = orchestration.attempt_id AND
				            suspensions.status IN ('suspend_pending', 'suspend_claimed')
			      )
			ORDER BY orchestration.created_at, orchestration.attempt_id
			LIMIT 1`,
			toDBTime(now),
		).Scan(&existingAttemptID)
		if queryErr != nil && !errors.Is(queryErr, sql.ErrNoRows) {
			return queryErr
		}
		if queryErr == nil {
			result, updateErr := conn.ExecContext(ctx, `
				UPDATE pr_development_repair_orchestrations
				SET claim_owner = ?, claim_token = ?, claim_until = ?,
					claim_epoch = claim_epoch + 1, claims = claims + 1,
					updated_at = ?
				WHERE attempt_id = ? AND phase IN ('bootstrap', 'edited', 'validated')
					AND claim_until <= ? AND
					NOT EXISTS (
						SELECT 1
						FROM pr_development_controller_suspensions AS suspensions
						WHERE suspensions.resume_attempt_id =
							pr_development_repair_orchestrations.attempt_id AND
							suspensions.status = 'resume_claimed'
					) AND
					NOT EXISTS (
						SELECT 1
						FROM pr_development_controller_suspensions AS suspensions
						WHERE suspensions.attempt_id =
							pr_development_repair_orchestrations.attempt_id AND
							suspensions.status IN ('suspend_pending', 'suspend_claimed')
					)`,
				worker,
				token,
				toDBTime(deadline),
				toDBTime(now),
				existingAttemptID,
				toDBTime(now),
			)
			if updateErr != nil {
				return updateErr
			}
			if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
				return rowErr
			}
			loaded, found, loadErr := loadPRDevelopmentRepairOrchestration(
				ctx, conn, existingAttemptID,
			)
			if loadErr != nil {
				return loadErr
			}
			if !found {
				return errors.New("reclaimed repair orchestration disappeared")
			}
			orchestration = loaded
			claimed = true
			return nil
		}

		var candidate struct {
			attemptID string
			sessionID string
			caseID    string
			threadID  string
		}
		queryErr = conn.QueryRowContext(ctx, `
			SELECT attempt.id, session.id, session.case_id, thread.id
			FROM pr_development_repair_attempts AS attempt
			JOIN pr_development_repair_sessions AS session ON session.id = attempt.session_id
			JOIN pr_development_thread_cases AS membership ON membership.case_id = session.case_id
			JOIN pr_development_threads AS thread ON thread.id = membership.thread_id
			WHERE attempt.status = 'queued' AND attempt.claims = 0
				AND thread.identity_kind = 'provider'
				AND (
					EXISTS (
						SELECT 1
						FROM pr_development_repair_orchestration_cohorts AS cohort
						WHERE cohort.session_id = session.id AND cohort.thread_id = thread.id
					) OR EXISTS (
						SELECT 1
						FROM pr_development_thread_controllers AS retained
						WHERE retained.owner_session_id = session.id
						  AND retained.thread_id = thread.id AND retained.phase = 'ready'
					)
				)
				AND (
					(session.head_repository = '' AND session.version <= ?) OR
					(session.head_repository <> '' AND session.version <= ?)
				)
				AND NOT EXISTS (
					SELECT 1 FROM pr_development_repair_orchestrations AS existing
					WHERE existing.attempt_id = attempt.id
				)
				AND (
					session.claim_suppressed = 0 OR EXISTS (
						SELECT 1 FROM pr_development_thread_controllers AS controller
						WHERE controller.owner_session_id = session.id
					)
				)
			ORDER BY attempt.created_at, attempt.id
			LIMIT 1`,
			MaxPRDevelopmentRepairVersion-2,
			MaxPRDevelopmentRepairVersion-1,
		).Scan(
			&candidate.attemptID,
			&candidate.sessionID,
			&candidate.caseID,
			&candidate.threadID,
		)
		if errors.Is(queryErr, sql.ErrNoRows) {
			return nil
		}
		if queryErr != nil {
			return queryErr
		}
		session, loadErr := loadPRDevelopmentRepairSessionByAttempt(
			ctx, conn, candidate.attemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		if session.ID != candidate.sessionID || session.CaseID != candidate.caseID ||
			len(session.Attempts) == 0 ||
			session.Attempts[len(session.Attempts)-1].ID != candidate.attemptID ||
			session.Attempts[len(session.Attempts)-1].Status != PRDevelopmentRepairQueued {
			return fmt.Errorf(
				"%w: claim candidate is not the queued owner-session latest attempt",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
		_, insertErr := conn.ExecContext(ctx, `
			INSERT INTO pr_development_repair_orchestrations (
				attempt_id, session_id, case_id, thread_id, phase,
				claim_owner, claim_token, claim_until, claim_epoch, claims,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, 'bootstrap', ?, ?, ?, 1, 1, ?, ?)`,
			candidate.attemptID,
			candidate.sessionID,
			candidate.caseID,
			candidate.threadID,
			worker,
			token,
			toDBTime(deadline),
			toDBTime(now),
			toDBTime(now),
		)
		if insertErr != nil {
			return insertErr
		}
		if _, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_sessions
			SET claim_suppressed = 1
			WHERE id = ? AND claim_suppressed = 0`, candidate.sessionID); updateErr != nil {
			return updateErr
		}
		loaded, found, loadErr := loadPRDevelopmentRepairOrchestration(
			ctx, conn, candidate.attemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return errors.New("claimed repair orchestration disappeared")
		}
		orchestration = loaded
		claimed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentRepairOrchestration{}, false, fmt.Errorf(
			"claim pull request development repair orchestration: %w",
			s.dbError(err),
		)
	}
	return orchestration, claimed, nil
}

// RenewPRDevelopmentRepairOrchestration extends only the exact live private
// orchestration claim. It does not renew the separate mutation controller.
func (s *Store) RenewPRDevelopmentRepairOrchestration(
	ctx context.Context,
	input PRDevelopmentRepairOrchestrationRenew,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.ClaimToken = strings.TrimSpace(input.ClaimToken)
	if !validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPRDevelopmentRepairIdentity(input.ClaimToken, maxPRDevelopmentRepairLeaseBytes) ||
		input.Lease <= 0 {
		return fmt.Errorf(
			"%w: valid attempt, claim token, and positive lease are required",
			ErrInvalidPRDevelopmentOrchestration,
		)
	}
	err := s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		deadline, deadlineErr := prDevelopmentControllerDeadline(now, input.Lease)
		if deadlineErr != nil {
			return deadlineErr
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_orchestrations
			SET claim_until = CASE WHEN claim_until > ? THEN claim_until ELSE ? END
			WHERE attempt_id = ? AND claim_token = ?
				AND phase IN ('bootstrap', 'editing', 'edited', 'validated')
				AND claim_until > ?`,
			toDBTime(deadline),
			toDBTime(deadline),
			input.AttemptID,
			input.ClaimToken,
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
		return nil
	})
	if err != nil {
		return fmt.Errorf(
			"renew pull request development repair orchestration: %w",
			s.dbError(err),
		)
	}
	return nil
}

// GetPRDevelopmentRepairOrchestration returns one validated private
// checkpoint without its raw claim token.
func (s *Store) GetPRDevelopmentRepairOrchestration(
	ctx context.Context,
	attemptID string,
) (PRDevelopmentRepairOrchestration, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentRepairOrchestration{}, err
	}
	attemptID = strings.TrimSpace(attemptID)
	if !validPrefixedHexID(attemptID, prDevelopmentRepairAttemptIDPrefix) {
		return PRDevelopmentRepairOrchestration{}, fmt.Errorf(
			"%w: valid attempt ID is required",
			ErrInvalidPRDevelopmentOrchestration,
		)
	}
	var orchestration PRDevelopmentRepairOrchestration
	err := s.withPRDevelopmentConversationReadSnapshot(ctx, func(queryer rowsQueryer) error {
		loaded, found, loadErr := loadPRDevelopmentRepairOrchestration(
			ctx, queryer, attemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		orchestration = loaded
		return nil
	})
	if err != nil {
		return PRDevelopmentRepairOrchestration{}, fmt.Errorf(
			"get pull request development repair orchestration: %w",
			s.dbError(err),
		)
	}
	orchestration.ClaimToken = ""
	return orchestration, nil
}

// PinPRDevelopmentRepairOrchestration stores the all-or-none provider pin and
// acquired clean workspace/source-tree baseline while the public attempt is
// still queued. Repeating every exact field is an idempotent replay.
func (s *Store) PinPRDevelopmentRepairOrchestration(
	ctx context.Context,
	input PRDevelopmentRepairOrchestrationPin,
) (PRDevelopmentRepairOrchestration, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentRepairOrchestration{}, false, err
	}
	normalizedPin, err := normalizePRDevelopmentRepairPin(PRDevelopmentRepairPin{
		AttemptID:      input.AttemptID,
		LeaseToken:     input.ClaimToken,
		HeadRepository: input.HeadRepository,
		HeadRef:        input.HeadRef,
		HeadSHA:        input.HeadSHA,
		CloneURL:       input.CloneURL,
		ReviewDigest:   input.ReviewDigest,
	})
	if err != nil {
		return PRDevelopmentRepairOrchestration{}, false, fmt.Errorf(
			"%w: provider pin is invalid",
			ErrInvalidPRDevelopmentOrchestration,
		)
	}
	input.AttemptID = normalizedPin.AttemptID
	input.ClaimToken = normalizedPin.LeaseToken
	input.HeadRepository = normalizedPin.HeadRepository
	input.HeadRef = normalizedPin.HeadRef
	input.HeadSHA = normalizedPin.HeadSHA
	input.CloneURL = normalizedPin.CloneURL
	input.ReviewDigest = normalizedPin.ReviewDigest
	input.WorkspaceID, err = normalizePRDevelopmentRepairIdentity(
		"workspace ID", input.WorkspaceID, maxPRDevelopmentRepairWorkspaceBytes, true,
	)
	input.SourceTree = strings.TrimSpace(input.SourceTree)
	if err != nil || !validSameWidthPRDevelopmentOIDs(input.HeadSHA, input.SourceTree) {
		return PRDevelopmentRepairOrchestration{}, false, fmt.Errorf(
			"%w: valid workspace and exact source tree are required",
			ErrInvalidPRDevelopmentOrchestration,
		)
	}

	var (
		orchestration PRDevelopmentRepairOrchestration
		changed       bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		current, found, loadErr := loadPRDevelopmentRepairOrchestration(
			ctx, conn, input.AttemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		if leaseErr := requireLivePRDevelopmentRepairOrchestrationClaim(
			current, input.ClaimToken, now, PRDevelopmentRepairOrchestrationBootstrap,
		); leaseErr != nil {
			return leaseErr
		}
		exact := equalPRDevelopmentRepairOrchestrationPin(current, input)
		if current.HeadRepository != "" {
			if !exact {
				return fmt.Errorf(
					"%w: provider/workspace baseline is immutable",
					ErrPRDevelopmentOrchestrationConflict,
				)
			}
			orchestration = current
			return nil
		}
		session, sessionErr := loadPRDevelopmentRepairSessionByAttempt(
			ctx, conn, input.AttemptID,
		)
		if sessionErr != nil {
			return sessionErr
		}
		if session.ID != current.SessionID || len(session.Attempts) == 0 ||
			session.Attempts[len(session.Attempts)-1].ID != input.AttemptID ||
			session.Attempts[len(session.Attempts)-1].Status != PRDevelopmentRepairQueued {
			return fmt.Errorf(
				"%w: pin no longer belongs to a queued latest attempt",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
		pinMatches := session.HeadRepository == "" ||
			session.HeadRepository == input.HeadRepository &&
				session.HeadRef == input.HeadRef &&
				session.HeadSHA == input.HeadSHA &&
				session.CloneURL == input.CloneURL &&
				session.ReviewDigest == input.ReviewDigest
		workspaceMatches := session.WorkspaceID == "" || session.WorkspaceID == input.WorkspaceID
		if !pinMatches || !workspaceMatches {
			return fmt.Errorf(
				"%w: session provider/workspace baseline is immutable",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
		if session.HeadRepository == "" || session.WorkspaceID == "" {
			if session.Version >= MaxPRDevelopmentRepairVersion {
				return ErrPRDevelopmentRepairCapacity
			}
			result, updateErr := conn.ExecContext(ctx, `
				UPDATE pr_development_repair_sessions
				SET head_repository = ?, head_ref = ?, head_sha = ?, clone_url = ?,
					review_digest = ?, workspace_id = ?, version = version + 1,
					updated_at = ?
				WHERE id = ? AND version = ? AND claim_suppressed = 1`,
				input.HeadRepository,
				input.HeadRef,
				input.HeadSHA,
				input.CloneURL,
				input.ReviewDigest,
				input.WorkspaceID,
				toDBTime(now),
				session.ID,
				session.Version,
			)
			if updateErr != nil {
				return updateErr
			}
			if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
				return rowErr
			}
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_orchestrations
			SET head_repository = ?, head_ref = ?, head_sha = ?, clone_url = ?,
				review_digest = ?, workspace_id = ?, source_tree = ?, updated_at = ?
			WHERE attempt_id = ? AND phase = 'bootstrap' AND claim_token = ?
				AND claim_until > ? AND head_repository = ''`,
			input.HeadRepository,
			input.HeadRef,
			input.HeadSHA,
			input.CloneURL,
			input.ReviewDigest,
			input.WorkspaceID,
			input.SourceTree,
			toDBTime(now),
			input.AttemptID,
			input.ClaimToken,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
			return rowErr
		}
		loaded, loadedFound, reloadErr := loadPRDevelopmentRepairOrchestration(
			ctx, conn, input.AttemptID,
		)
		if reloadErr != nil {
			return reloadErr
		}
		if !loadedFound {
			return errors.New("pinned repair orchestration disappeared")
		}
		orchestration = loaded
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentRepairOrchestration{}, false, fmt.Errorf(
			"pin pull request development repair orchestration: %w",
			s.dbError(err),
		)
	}
	return orchestration, changed, nil
}

// AcquirePRDevelopmentRepairOrchestrationController creates the initial
// controller or acquires a Ready controller only for the exact live
// orchestration claim. Generic acquisition remains unchanged and cannot use a
// session whose legacy queue is already suppressed.
func (s *Store) AcquirePRDevelopmentRepairOrchestrationController(
	ctx context.Context,
	input PRDevelopmentRepairOrchestrationControllerAcquire,
) (PRDevelopmentControllerLease, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentControllerLease{}, false, err
	}
	input.ClaimToken = strings.TrimSpace(input.ClaimToken)
	normalized, err := normalizePRDevelopmentControllerAcquire(
		PRDevelopmentControllerAcquire{
			CaseID:           input.CaseID,
			AttemptID:        input.AttemptID,
			ExpectedRevision: input.ExpectedRevision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      input.WorkerLabel,
			Lease:            input.Lease,
		},
	)
	if err != nil || !validPRDevelopmentRepairIdentity(
		input.ClaimToken, maxPRDevelopmentRepairLeaseBytes,
	) {
		return PRDevelopmentControllerLease{}, false, fmt.Errorf(
			"%w: exact claim and valid controller acquisition are required",
			ErrInvalidPRDevelopmentOrchestration,
		)
	}

	var (
		lease            PRDevelopmentControllerLease
		changed          bool
		recoveryRequired bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		orchestration, found, loadErr := loadPRDevelopmentRepairOrchestration(
			ctx, conn, normalized.AttemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		if orchestration.CaseID != normalized.CaseID ||
			orchestration.HeadRepository == "" || orchestration.WorkspaceID == "" {
			return fmt.Errorf(
				"%w: orchestration has no exact pinned baseline",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
		if leaseErr := requireLivePRDevelopmentRepairOrchestrationClaimAnySafe(
			orchestration, input.ClaimToken, now,
		); leaseErr != nil {
			return leaseErr
		}
		relation, relationErr := loadPRDevelopmentControllerAttemptRelation(
			ctx, conn, normalized.CaseID, normalized.AttemptID,
		)
		if relationErr != nil {
			return relationErr
		}
		if relation.Thread.ID != orchestration.ThreadID ||
			relation.Session.ID != orchestration.SessionID ||
			relation.Session.HeadRepository != orchestration.HeadRepository ||
			relation.Session.HeadRef != orchestration.HeadRef ||
			relation.Session.HeadSHA != orchestration.HeadSHA ||
			relation.Session.CloneURL != orchestration.CloneURL ||
			relation.Session.ReviewDigest != orchestration.ReviewDigest ||
			relation.Session.WorkspaceID != orchestration.WorkspaceID {
			return fmt.Errorf(
				"%w: orchestration relation or pinned baseline changed",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
		controller, controllerFound, controllerErr := loadPRDevelopmentControllerAggregate(
			ctx, conn, relation.Thread.ID,
		)
		if controllerErr != nil {
			return controllerErr
		}
		deadline, deadlineErr := prDevelopmentControllerDeadline(now, normalized.Lease)
		if deadlineErr != nil {
			return deadlineErr
		}
		if !controllerFound {
			if normalized.ExpectedRevision != 0 {
				return fmt.Errorf(
					"%w: expected revision is not initial",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if siblingErr := requireNoSiblingPRDevelopmentRepairSessions(
				ctx, conn, relation.Thread.ID, relation.Session.ID,
			); siblingErr != nil {
				return siblingErr
			}
			created, createErr := insertLeasedPRDevelopmentController(
				ctx, conn, relation, normalized, now, deadline, true,
			)
			if createErr != nil {
				return createErr
			}
			lease.Controller = created
			lease.Created = true
			changed = true
		} else {
			if controller.OwnerSessionID != relation.Session.ID ||
				controller.AgentID != relation.Session.AgentID {
				return fmt.Errorf(
					"%w: controller belongs to another owner",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if controller.Phase == PRDevelopmentControllerMutation &&
				controller.CurrentAttemptID == normalized.AttemptID &&
				orchestration.ControllerID == controller.ID {
				initialReplay := normalized.ExpectedRevision == 0 && controller.Revision == 1
				if (!initialReplay && controller.Revision != normalized.ExpectedRevision) ||
					controller.LeaseOwner != normalized.WorkerLabel ||
					(controller.WorkspaceID != "" &&
						(controller.WorkspaceID != orchestration.WorkspaceID ||
							controller.SourceCloneURL != orchestration.CloneURL ||
							controller.SourceRef != orchestration.HeadRef ||
							controller.SourceCommit != orchestration.HeadSHA ||
							controller.SourceTree != orchestration.SourceTree)) {
					return fmt.Errorf(
						"%w: existing mutation lease is not an exact orchestration replay",
						ErrPRDevelopmentOrchestrationConflict,
					)
				}
				if controller.LeaseUntil == nil || !controller.LeaseUntil.After(now) {
					if expireErr := expirePRDevelopmentMutationLease(
						ctx, conn, controller, now,
					); expireErr != nil {
						return expireErr
					}
					recoveryRequired = true
					return nil
				}
				if deadline.After(*controller.LeaseUntil) {
					if now.Before(controller.UpdatedAt) {
						return fmt.Errorf(
							"%w: store clock regressed behind replayed controller",
							ErrInvalidPRDevelopmentController,
						)
					}
					result, updateErr := conn.ExecContext(ctx, `
						UPDATE pr_development_thread_controllers
						SET lease_until = ?, updated_at = ?
						WHERE id = ? AND revision = ? AND phase = 'mutation' AND
							current_attempt_id = ? AND lease_kind = 'mutation' AND
							lease_owner = ? AND lease_token = ? AND lease_epoch = ? AND
							lease_until > ?`,
						toDBTime(deadline),
						toDBTime(now),
						controller.ID,
						controller.Revision,
						controller.CurrentAttemptID,
						controller.LeaseOwner,
						controller.LeaseToken,
						controller.LeaseEpoch,
						toDBTime(now),
					)
					if updateErr != nil {
						return updateErr
					}
					if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
						return rowErr
					}
					extended, extendedFound, reloadErr := loadPRDevelopmentControllerAggregateByID(
						ctx, conn, controller.ID,
					)
					if reloadErr != nil {
						return reloadErr
					}
					if !extendedFound {
						return errors.New("extended orchestration controller disappeared")
					}
					controller = extended
				}
				lease.Controller = controller
				return nil
			}
			if controller.Phase == PRDevelopmentControllerSuspended {
				if controller.WorkspaceID != orchestration.WorkspaceID ||
					controller.SourceCloneURL != orchestration.CloneURL ||
					controller.SourceRef != orchestration.HeadRef ||
					controller.SourceCommit != orchestration.HeadSHA ||
					controller.SourceTree != orchestration.SourceTree {
					return fmt.Errorf(
						"%w: suspended line differs from its exact orchestration pin",
						ErrPRDevelopmentOrchestrationConflict,
					)
				}
				resume, resumeChanged, resumeErr := s.acquirePRDevelopmentControllerSuspendedResume(
					ctx,
					conn,
					relation,
					controller,
					normalized,
					orchestration,
					now,
					deadline,
				)
				if resumeErr != nil {
					return resumeErr
				}
				lease.Controller = resume.Controller
				lease.SuspendedResume = &resume
				changed = resumeChanged
			} else {
				if controller.Phase != PRDevelopmentControllerReady ||
					controller.WorkspaceID != orchestration.WorkspaceID ||
					controller.SourceCloneURL != orchestration.CloneURL ||
					controller.SourceRef != orchestration.HeadRef ||
					controller.SourceCommit != orchestration.HeadSHA ||
					controller.SourceTree != orchestration.SourceTree {
					return fmt.Errorf(
						"%w: only the exact retained Ready or Suspended line may be resumed",
						ErrPRDevelopmentOrchestrationConflict,
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
				updated, acquireErr := acquirePRDevelopmentMutationLease(
					ctx, conn, controller, normalized, now, deadline,
				)
				if errors.Is(acquireErr, ErrPRDevelopmentControllerRecoveryRequired) {
					recoveryRequired = true
					return nil
				}
				if acquireErr != nil {
					return acquireErr
				}
				lease.Controller = updated
				changed = true
			}
		}
		if orchestration.ControllerID != "" &&
			orchestration.ControllerID != lease.Controller.ID {
			return fmt.Errorf(
				"%w: orchestration controller is immutable",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
		if orchestration.ControllerID == "" {
			result, updateErr := conn.ExecContext(ctx, `
				UPDATE pr_development_repair_orchestrations
				SET controller_id = ?, updated_at = ?
				WHERE attempt_id = ? AND controller_id = '' AND claim_token = ?
					AND claim_until > ?`,
				lease.Controller.ID,
				toDBTime(now),
				normalized.AttemptID,
				input.ClaimToken,
				toDBTime(now),
			)
			if updateErr != nil {
				return updateErr
			}
			if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
				return rowErr
			}
		}
		return nil
	})
	if err != nil {
		return PRDevelopmentControllerLease{}, false, fmt.Errorf(
			"acquire pull request development repair orchestration controller: %w",
			s.dbError(err),
		)
	}
	if recoveryRequired {
		return PRDevelopmentControllerLease{}, false, fmt.Errorf(
			"acquire pull request development repair orchestration controller: %w",
			ErrPRDevelopmentControllerRecoveryRequired,
		)
	}
	return lease, changed, nil
}

// StartPRDevelopmentRepairOrchestrationModel durably crosses the one-way
// boundary into Editing. If that claim later expires, claim scanning
// terminalizes it instead of invoking the model again.
func (s *Store) StartPRDevelopmentRepairOrchestrationModel(
	ctx context.Context,
	input PRDevelopmentRepairOrchestrationModelStart,
) (PRDevelopmentRepairOrchestration, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentRepairOrchestration{}, false, err
	}
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.ClaimToken = strings.TrimSpace(input.ClaimToken)
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.MutationLeaseToken = strings.TrimSpace(input.MutationLeaseToken)
	input.ContextDigest = strings.TrimSpace(input.ContextDigest)
	input.PromptDigest = strings.TrimSpace(input.PromptDigest)
	if !validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPRDevelopmentRepairIdentity(input.ClaimToken, maxPRDevelopmentRepairLeaseBytes) ||
		!validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPRDevelopmentRepairIdentity(
			input.MutationLeaseToken, prDevelopmentControllerLeaseTokenBytes,
		) || input.ControllerRevision < 1 ||
		input.ControllerRevision > MaxPRDevelopmentControllerRevision ||
		input.MutationLeaseEpoch < 1 ||
		!validPRDevelopmentHex(input.ContextDigest, sha256.Size*2) ||
		!validPRDevelopmentHex(input.PromptDigest, sha256.Size*2) {
		return PRDevelopmentRepairOrchestration{}, false, fmt.Errorf(
			"%w: exact live controller and model-context digests are required",
			ErrInvalidPRDevelopmentOrchestration,
		)
	}
	return s.transitionPRDevelopmentRepairOrchestrationModelStart(ctx, input)
}

func (s *Store) transitionPRDevelopmentRepairOrchestrationModelStart(
	ctx context.Context,
	input PRDevelopmentRepairOrchestrationModelStart,
) (PRDevelopmentRepairOrchestration, bool, error) {
	var (
		orchestration PRDevelopmentRepairOrchestration
		changed       bool
	)
	err := s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		current, found, loadErr := loadPRDevelopmentRepairOrchestration(
			ctx, conn, input.AttemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		if current.Phase == PRDevelopmentRepairOrchestrationEditing &&
			current.ContextDigest == input.ContextDigest &&
			current.PromptDigest == input.PromptDigest {
			if current.ModelControllerRevision != input.ControllerRevision ||
				current.ModelMutationLeaseEpoch != input.MutationLeaseEpoch ||
				current.ModelLeaseTokenDigest != prDevelopmentLeaseTokenDigest(
					PRDevelopmentControllerMutationLease, input.MutationLeaseToken,
				) {
				return fmt.Errorf(
					"%w: model start replay has different mutation authority",
					ErrPRDevelopmentOrchestrationConflict,
				)
			}
			if leaseErr := requireLivePRDevelopmentRepairOrchestrationClaim(
				current,
				input.ClaimToken,
				now,
				PRDevelopmentRepairOrchestrationEditing,
			); leaseErr != nil {
				return leaseErr
			}
			controller, controllerErr := loadExactPRDevelopmentRepairOrchestrationController(
				ctx, conn, current, input.ControllerID, input.ControllerRevision,
				input.MutationLeaseToken, input.MutationLeaseEpoch, now,
			)
			if controllerErr != nil {
				return controllerErr
			}
			if !equalPRDevelopmentRepairOrchestrationModelFence(current, controller) {
				return fmt.Errorf(
					"%w: stored model start fence differs from its live controller",
					ErrPRDevelopmentOrchestrationConflict,
				)
			}
			orchestration = current
			return nil
		}
		if leaseErr := requireLivePRDevelopmentRepairOrchestrationClaim(
			current,
			input.ClaimToken,
			now,
			PRDevelopmentRepairOrchestrationBootstrap,
		); leaseErr != nil {
			return leaseErr
		}
		controller, controllerErr := loadExactPRDevelopmentRepairOrchestrationController(
			ctx, conn, current, input.ControllerID, input.ControllerRevision,
			input.MutationLeaseToken, input.MutationLeaseEpoch, now,
		)
		if controllerErr != nil {
			return controllerErr
		}
		if controller.WorkspaceID == "" ||
			controller.WorkspaceID != current.WorkspaceID ||
			controller.MutationEpoch != controller.LineVersion+1 {
			return fmt.Errorf(
				"%w: model editing requires an adopted/resumed mutation epoch",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_orchestrations
			SET phase = 'editing', model_controller_revision = ?,
				model_line_id = ?, model_line_version = ?, model_mutation_epoch = ?,
				model_lease_epoch = ?, model_lease_token_digest = ?,
				model_reservation_digest = ?, context_digest = ?, prompt_digest = ?,
				model_started_at = ?, updated_at = ?
			WHERE attempt_id = ? AND phase = 'bootstrap' AND claim_token = ?
				AND claim_until > ? AND controller_id = ?`,
			controller.Revision,
			controller.LineID,
			controller.LineVersion,
			controller.MutationEpoch,
			controller.LeaseEpoch,
			prDevelopmentLeaseTokenDigest(
				PRDevelopmentControllerMutationLease, controller.LeaseToken,
			),
			prDevelopmentMutationReservationDigest(
				controller.MutationReservationKey,
			),
			input.ContextDigest,
			input.PromptDigest,
			toDBTime(now),
			toDBTime(now),
			input.AttemptID,
			input.ClaimToken,
			toDBTime(now),
			input.ControllerID,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
			return rowErr
		}
		loaded, loadedFound, reloadErr := loadPRDevelopmentRepairOrchestration(
			ctx, conn, input.AttemptID,
		)
		if reloadErr != nil {
			return reloadErr
		}
		if !loadedFound {
			return errors.New("started repair orchestration disappeared")
		}
		orchestration = loaded
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentRepairOrchestration{}, false, fmt.Errorf(
			"start pull request development repair orchestration model: %w",
			s.dbError(err),
		)
	}
	return orchestration, changed, nil
}

// CompletePRDevelopmentRepairOrchestrationModel records the bounded model
// result without terminalizing the public attempt or releasing mutation
// authority.
func (s *Store) CompletePRDevelopmentRepairOrchestrationModel(
	ctx context.Context,
	input PRDevelopmentRepairOrchestrationModelComplete,
) (PRDevelopmentRepairOrchestration, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentRepairOrchestration{}, false, err
	}
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.ClaimToken = strings.TrimSpace(input.ClaimToken)
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.MutationLeaseToken = strings.TrimSpace(input.MutationLeaseToken)
	input.ModelResultDigest = strings.TrimSpace(input.ModelResultDigest)
	input.Summary = strings.TrimSpace(input.Summary)
	if !validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPRDevelopmentRepairIdentity(input.ClaimToken, maxPRDevelopmentRepairLeaseBytes) ||
		!validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPRDevelopmentRepairIdentity(
			input.MutationLeaseToken, prDevelopmentControllerLeaseTokenBytes,
		) || input.ControllerRevision < 1 ||
		input.ControllerRevision > MaxPRDevelopmentControllerRevision ||
		input.MutationLeaseEpoch < 1 ||
		!validPRDevelopmentHex(input.ModelResultDigest, sha256.Size*2) ||
		!validStoredPRDevelopmentRepairText(
			input.Summary, MaxPRDevelopmentRepairSummaryBytes,
		) || input.Iterations < 1 || input.Iterations > MaxPRDevelopmentRepairIterations {
		return PRDevelopmentRepairOrchestration{}, false, fmt.Errorf(
			"%w: exact controller, model digest, summary, and iterations are required",
			ErrInvalidPRDevelopmentOrchestration,
		)
	}

	var (
		orchestration PRDevelopmentRepairOrchestration
		changed       bool
	)
	err := s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		current, found, loadErr := loadPRDevelopmentRepairOrchestration(
			ctx, conn, input.AttemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		if current.Phase == PRDevelopmentRepairOrchestrationEditing ||
			current.Phase == PRDevelopmentRepairOrchestrationEdited {
			if current.ModelControllerRevision != input.ControllerRevision ||
				current.ModelMutationLeaseEpoch != input.MutationLeaseEpoch ||
				current.ModelLeaseTokenDigest != prDevelopmentLeaseTokenDigest(
					PRDevelopmentControllerMutationLease, input.MutationLeaseToken,
				) {
				return fmt.Errorf(
					"%w: model completion has different start authority",
					ErrPRDevelopmentOrchestrationConflict,
				)
			}
		}
		if current.Phase == PRDevelopmentRepairOrchestrationEdited &&
			current.ModelResultDigest == input.ModelResultDigest &&
			current.Summary == input.Summary && current.Iterations == input.Iterations {
			if leaseErr := requireLivePRDevelopmentRepairOrchestrationClaim(
				current, input.ClaimToken, now, PRDevelopmentRepairOrchestrationEdited,
			); leaseErr != nil {
				return leaseErr
			}
			controller, controllerErr := loadExactPRDevelopmentRepairOrchestrationController(
				ctx, conn, current, input.ControllerID, input.ControllerRevision,
				input.MutationLeaseToken, input.MutationLeaseEpoch, now,
			)
			if controllerErr != nil {
				return controllerErr
			}
			if !equalPRDevelopmentRepairOrchestrationModelFence(current, controller) {
				return fmt.Errorf(
					"%w: stored model completion fence differs from its live controller",
					ErrPRDevelopmentOrchestrationConflict,
				)
			}
			orchestration = current
			return nil
		}
		if leaseErr := requireLivePRDevelopmentRepairOrchestrationClaim(
			current, input.ClaimToken, now, PRDevelopmentRepairOrchestrationEditing,
		); leaseErr != nil {
			return leaseErr
		}
		controller, controllerErr := loadExactPRDevelopmentRepairOrchestrationController(
			ctx, conn, current, input.ControllerID, input.ControllerRevision,
			input.MutationLeaseToken, input.MutationLeaseEpoch, now,
		)
		if controllerErr != nil {
			return controllerErr
		}
		if !equalPRDevelopmentRepairOrchestrationModelFence(current, controller) {
			return fmt.Errorf(
				"%w: stored model completion fence differs from its live controller",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_orchestrations
			SET phase = 'edited', model_result_digest = ?, summary = ?,
				iterations = ?, model_completed_at = ?, updated_at = ?
			WHERE attempt_id = ? AND phase = 'editing' AND claim_token = ?
				AND claim_until > ?`,
			input.ModelResultDigest,
			input.Summary,
			input.Iterations,
			toDBTime(now),
			toDBTime(now),
			input.AttemptID,
			input.ClaimToken,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
			return rowErr
		}
		loaded, loadedFound, reloadErr := loadPRDevelopmentRepairOrchestration(
			ctx, conn, input.AttemptID,
		)
		if reloadErr != nil {
			return reloadErr
		}
		if !loadedFound {
			return errors.New("completed model orchestration disappeared")
		}
		orchestration = loaded
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentRepairOrchestration{}, false, fmt.Errorf(
			"complete pull request development repair orchestration model: %w",
			s.dbError(err),
		)
	}
	return orchestration, changed, nil
}

// RecordPRDevelopmentRepairOrchestrationValidation immutably binds the exact
// edited candidate to a persisted local-CI attestation. Any attested terminal
// status is accepted; later gates, not this receipt, decide whether it is green.
func (s *Store) RecordPRDevelopmentRepairOrchestrationValidation(
	ctx context.Context,
	input PRDevelopmentRepairOrchestrationValidation,
) (PRDevelopmentRepairOrchestration, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentRepairOrchestration{}, false, err
	}
	if err := normalizePRDevelopmentRepairOrchestrationValidation(&input); err != nil {
		return PRDevelopmentRepairOrchestration{}, false, err
	}

	var (
		orchestration PRDevelopmentRepairOrchestration
		changed       bool
	)
	err := s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		current, found, loadErr := loadPRDevelopmentRepairOrchestration(
			ctx, conn, input.AttemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		if leaseErr := requireLivePRDevelopmentRepairOrchestrationClaimAny(
			current, input.ClaimToken, now,
			PRDevelopmentRepairOrchestrationEdited,
			PRDevelopmentRepairOrchestrationValidated,
		); leaseErr != nil {
			return leaseErr
		}
		controller, controllerErr := loadExactPRDevelopmentRepairOrchestrationController(
			ctx, conn, current, input.ControllerID, input.ControllerRevision,
			input.MutationLeaseToken, input.MutationLeaseEpoch, now,
		)
		if controllerErr != nil {
			return controllerErr
		}
		if current.ModelControllerRevision != controller.Revision ||
			current.ModelLineID != controller.LineID ||
			current.ModelLineVersion != controller.LineVersion ||
			current.ModelMutationEpoch != controller.MutationEpoch ||
			current.ModelMutationLeaseEpoch != controller.LeaseEpoch ||
			current.ModelLeaseTokenDigest != prDevelopmentLeaseTokenDigest(
				PRDevelopmentControllerMutationLease, controller.LeaseToken,
			) || current.ModelReservationDigest != prDevelopmentMutationReservationDigest(
			controller.MutationReservationKey,
		) {
			return fmt.Errorf(
				"%w: validation controller differs from the immutable model-start fence",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
		if controller.WorkspaceID != current.WorkspaceID ||
			controller.MutationEpoch != controller.LineVersion+1 ||
			input.ParentCommit != controller.TipCommit || input.ParentTree != controller.Tree {
			return fmt.Errorf(
				"%w: validation parent differs from the active controller fence",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
		receipt := PRDevelopmentRepairValidationReceipt{
			ControllerID:            controller.ID,
			WorkspaceID:             controller.WorkspaceID,
			ModelControllerRevision: current.ModelControllerRevision,
			ModelLineID:             current.ModelLineID,
			ModelLineVersion:        current.ModelLineVersion,
			ModelMutationEpoch:      current.ModelMutationEpoch,
			ModelMutationLeaseEpoch: current.ModelMutationLeaseEpoch,
			ModelLeaseTokenDigest:   current.ModelLeaseTokenDigest,
			ModelReservationDigest:  current.ModelReservationDigest,
			ContextDigest:           current.ContextDigest,
			PromptDigest:            current.PromptDigest,
			LineID:                  controller.LineID,
			ControllerRevision:      controller.Revision,
			LineVersion:             controller.LineVersion,
			MutationEpoch:           controller.MutationEpoch,
			MutationLeaseEpoch:      controller.LeaseEpoch,
			MutationLeaseTokenDigest: prDevelopmentLeaseTokenDigest(
				PRDevelopmentControllerMutationLease, controller.LeaseToken,
			),
			MutationReservationDigest: prDevelopmentMutationReservationDigest(
				controller.MutationReservationKey,
			),
			ParentCommit:          input.ParentCommit,
			ParentTree:            input.ParentTree,
			CandidateTree:         input.CandidateTree,
			CandidateDigest:       input.CandidateDigest,
			ChangedFiles:          input.ChangedFiles,
			NoChanges:             input.NoChanges,
			CIStatus:              input.CIStatus,
			CIAttestationID:       input.CIAttestationID,
			CIAttestationDigest:   input.CIAttestationDigest,
			CIResultKey:           input.CIResultKey,
			CIEffectivePlanDigest: input.CIEffectivePlanDigest,
			CIExecutionDigest:     input.CIExecutionDigest,
			ModelResultDigest:     current.ModelResultDigest,
			ModelSummary:          current.Summary,
			ModelIterations:       current.Iterations,
			CreatedAt:             now,
		}
		receipt.ReceiptHash = hashPRDevelopmentRepairValidationReceipt(receipt)
		if current.Phase == PRDevelopmentRepairOrchestrationValidated {
			if current.Validation == nil ||
				!equalPRDevelopmentRepairValidationReceipt(*current.Validation, receipt, false) {
				return fmt.Errorf(
					"%w: validation receipt is bound to different evidence",
					ErrPRDevelopmentOrchestrationConflict,
				)
			}
			orchestration = current
			return nil
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_orchestrations
			SET phase = 'validated', validation_controller_revision = ?,
				validation_line_id = ?, validation_line_version = ?,
				validation_mutation_epoch = ?, validation_lease_epoch = ?,
				validation_lease_token_digest = ?, validation_reservation_digest = ?,
				parent_commit = ?, parent_tree = ?, candidate_tree = ?,
				candidate_digest = ?, changed_files = ?, no_changes = ?, ci_status = ?,
				ci_attestation_id = ?, ci_attestation_digest = ?, ci_result_key = ?,
				ci_effective_plan_digest = ?, ci_execution_digest = ?, receipt_hash = ?,
				validated_at = ?, updated_at = ?
			WHERE attempt_id = ? AND phase = 'edited' AND claim_token = ?
				AND claim_until > ?`,
			receipt.ControllerRevision,
			receipt.LineID,
			receipt.LineVersion,
			receipt.MutationEpoch,
			receipt.MutationLeaseEpoch,
			receipt.MutationLeaseTokenDigest,
			receipt.MutationReservationDigest,
			receipt.ParentCommit,
			receipt.ParentTree,
			receipt.CandidateTree,
			receipt.CandidateDigest,
			receipt.ChangedFiles,
			boolDBValue(receipt.NoChanges),
			receipt.CIStatus,
			receipt.CIAttestationID,
			receipt.CIAttestationDigest,
			receipt.CIResultKey,
			receipt.CIEffectivePlanDigest,
			receipt.CIExecutionDigest,
			receipt.ReceiptHash,
			toDBTime(now),
			toDBTime(now),
			input.AttemptID,
			input.ClaimToken,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
			return rowErr
		}
		loaded, loadedFound, reloadErr := loadPRDevelopmentRepairOrchestration(
			ctx, conn, input.AttemptID,
		)
		if reloadErr != nil {
			return reloadErr
		}
		if !loadedFound {
			return errors.New("validated repair orchestration disappeared")
		}
		orchestration = loaded
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentRepairOrchestration{}, false, fmt.Errorf(
			"record pull request development repair orchestration validation: %w",
			s.dbError(err),
		)
	}
	return orchestration, changed, nil
}

// FailPRDevelopmentRepairOrchestration terminalizes only an unpinned
// bootstrap that has not acquired a controller or started the model. The
// worker must release any acquired-but-not-durably-pinned checkout first.
// Suppression is cleared only when the thread has no retained controller.
func (s *Store) FailPRDevelopmentRepairOrchestration(
	ctx context.Context,
	input PRDevelopmentRepairOrchestrationFail,
) (PRDevelopmentRepairOrchestration, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentRepairOrchestration{}, false, err
	}
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.ClaimToken = strings.TrimSpace(input.ClaimToken)
	input.Summary = strings.TrimSpace(input.Summary)
	if !validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPRDevelopmentRepairIdentity(input.ClaimToken, maxPRDevelopmentRepairLeaseBytes) ||
		!validStoredPRDevelopmentRepairText(
			input.Summary, MaxPRDevelopmentRepairSummaryBytes,
		) || !validPRDevelopmentRepairFailureCode(input.ErrorCode) {
		return PRDevelopmentRepairOrchestration{}, false, fmt.Errorf(
			"%w: bootstrap failure requires exact claim and bounded safe outcome",
			ErrInvalidPRDevelopmentOrchestration,
		)
	}
	input.InternalError = s.sanitizePRDevelopmentRepairInternalError(input.InternalError)
	claimDigest := hashPRDevelopmentRepairOrchestrationClaimToken(input.ClaimToken)
	var (
		orchestration PRDevelopmentRepairOrchestration
		changed       bool
	)
	err := s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		current, found, loadErr := loadPRDevelopmentRepairOrchestration(
			ctx, conn, input.AttemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		session, sessionErr := loadPRDevelopmentRepairSessionByAttempt(
			ctx, conn, input.AttemptID,
		)
		if sessionErr != nil {
			return sessionErr
		}
		attempt := findPRDevelopmentRepairAttempt(&session, input.AttemptID)
		if attempt == nil {
			return fmt.Errorf(
				"%w: failure attempt is absent from its owner session",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
		if current.Phase == PRDevelopmentRepairOrchestrationFailed {
			if current.FailedClaimTokenDigest != claimDigest ||
				attempt.Status != PRDevelopmentRepairFailed || attempt.Summary != input.Summary ||
				attempt.ErrorCode != input.ErrorCode || attempt.InternalError != input.InternalError {
				return fmt.Errorf(
					"%w: changed bootstrap failure replay",
					ErrPRDevelopmentOrchestrationConflict,
				)
			}
			orchestration = current
			return nil
		}
		if len(session.Attempts) == 0 ||
			session.Attempts[len(session.Attempts)-1].ID != input.AttemptID {
			return fmt.Errorf(
				"%w: failure attempt is no longer owner-session latest",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
		if leaseErr := requireLivePRDevelopmentRepairOrchestrationClaim(
			current, input.ClaimToken, now, PRDevelopmentRepairOrchestrationBootstrap,
		); leaseErr != nil {
			return leaseErr
		}
		if current.HeadRepository != "" || current.WorkspaceID != "" ||
			current.ControllerID != "" || current.ContextDigest != "" ||
			current.ModelStartedAt != nil || current.Validation != nil ||
			attempt.Status != PRDevelopmentRepairQueued || attempt.Claims != 0 ||
			session.Version >= MaxPRDevelopmentRepairVersion {
			return fmt.Errorf(
				"%w: only a pre-controller bootstrap may fail safely",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
		controller, controllerFound, controllerErr := loadPRDevelopmentControllerAggregate(
			ctx, conn, current.ThreadID,
		)
		if controllerErr != nil {
			return controllerErr
		}
		if controllerFound && (controller.OwnerSessionID != session.ID ||
			controller.Phase != PRDevelopmentControllerReady ||
			controller.CurrentAttemptID == input.AttemptID) {
			return fmt.Errorf(
				"%w: bootstrap failure cannot disturb an active retained controller",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
		attemptResult, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_attempts
			SET status = 'failed', claims = 1, summary = ?, error_code = ?,
				internal_error = ?, updated_at = ?
			WHERE id = ? AND session_id = ? AND status = 'queued' AND claims = 0`,
			input.Summary,
			input.ErrorCode,
			input.InternalError,
			toDBTime(now),
			input.AttemptID,
			session.ID,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(attemptResult); rowErr != nil {
			return rowErr
		}
		suppressed := 0
		if controllerFound {
			suppressed = 1
		}
		sessionResult, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_sessions
			SET version = version + 1, claim_suppressed = ?, updated_at = ?
			WHERE id = ? AND version = ? AND claim_suppressed = 1`,
			suppressed,
			toDBTime(now),
			session.ID,
			session.Version,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(sessionResult); rowErr != nil {
			return rowErr
		}
		runResult, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_orchestrations
			SET phase = 'failed', claim_owner = '', claim_token = '',
				claim_until = NULL, failed_claim_token_digest = ?, failed_at = ?,
				updated_at = ?
			WHERE attempt_id = ? AND phase = 'bootstrap' AND claim_token = ?
				AND claim_until > ? AND controller_id = ''`,
			claimDigest,
			toDBTime(now),
			toDBTime(now),
			input.AttemptID,
			input.ClaimToken,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(runResult); rowErr != nil {
			return rowErr
		}
		loaded, loadedFound, reloadErr := loadPRDevelopmentRepairOrchestration(
			ctx, conn, input.AttemptID,
		)
		if reloadErr != nil {
			return reloadErr
		}
		if !loadedFound {
			return errors.New("failed repair orchestration disappeared")
		}
		orchestration = loaded
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentRepairOrchestration{}, false, fmt.Errorf(
			"fail pull request development repair orchestration: %w",
			s.dbError(err),
		)
	}
	return orchestration, changed, nil
}

func hashPRDevelopmentRepairOrchestrationClaimToken(token string) string {
	digest := sha256.New()
	writePRDevelopmentOrchestrationHashField(
		digest, "picoclaw-pr-development-repair-orchestration-claim-v1",
	)
	writePRDevelopmentOrchestrationHashField(digest, token)
	return hex.EncodeToString(digest.Sum(nil))
}

type prDevelopmentRepairOrchestrationMutationFence struct {
	controllerID     string
	controllerRev    int64
	lineID           string
	lineVersion      int64
	mutationEpoch    int64
	leaseEpoch       int64
	leaseTokenDigest string
	reservationHash  string
}

// terminalizePRDevelopmentRepairOrchestrationAfterRecovery turns a
// post-model authority rotation into an explicit fail-closed public outcome.
// The reconciled controller and its fresh credential remain fully accounted
// by the recovery transition, while this attempt can no longer reuse an
// immutable model/CI receipt under the superseded mutation fence.
func terminalizePRDevelopmentRepairOrchestrationAfterRecovery(
	ctx context.Context,
	conn *sql.Conn,
	attemptID string,
	fence prDevelopmentRepairOrchestrationMutationFence,
	now time.Time,
) (bool, error) {
	orchestration, found, err := loadStoredPRDevelopmentRepairOrchestration(
		ctx, conn, attemptID,
	)
	if err != nil || !found {
		return false, err
	}
	if orchestration.Phase == PRDevelopmentRepairOrchestrationBootstrap {
		return false, nil
	}
	if orchestration.Phase == PRDevelopmentRepairOrchestrationRecoveryRequired {
		return false, nil
	}
	if orchestration.Phase != PRDevelopmentRepairOrchestrationEditing &&
		orchestration.Phase != PRDevelopmentRepairOrchestrationEdited &&
		orchestration.Phase != PRDevelopmentRepairOrchestrationValidated {
		return false, fmt.Errorf(
			"%w: recovered authority reached a non-recoverable orchestration phase",
			ErrPRDevelopmentOrchestrationConflict,
		)
	}
	if orchestration.ControllerID != fence.controllerID ||
		orchestration.ModelControllerRevision != fence.controllerRev ||
		orchestration.ModelLineID != fence.lineID ||
		orchestration.ModelLineVersion != fence.lineVersion ||
		orchestration.ModelMutationEpoch != fence.mutationEpoch ||
		orchestration.ModelMutationLeaseEpoch != fence.leaseEpoch ||
		orchestration.ModelLeaseTokenDigest != fence.leaseTokenDigest ||
		orchestration.ModelReservationDigest != fence.reservationHash {
		return false, fmt.Errorf(
			"%w: recovered authority differs from the orchestration model fence",
			ErrPRDevelopmentOrchestrationConflict,
		)
	}
	session, err := loadPRDevelopmentRepairSessionByAttempt(ctx, conn, attemptID)
	if err != nil {
		return false, err
	}
	if session.ID != orchestration.SessionID || len(session.Attempts) == 0 ||
		session.Attempts[len(session.Attempts)-1].ID != attemptID ||
		session.Attempts[len(session.Attempts)-1].Status != PRDevelopmentRepairQueued ||
		session.Attempts[len(session.Attempts)-1].Claims != 0 ||
		session.Version >= MaxPRDevelopmentRepairVersion || now.Before(session.UpdatedAt) ||
		now.Before(orchestration.UpdatedAt) {
		return false, fmt.Errorf(
			"%w: recovered orchestration is not its queued owner-session tail",
			ErrPRDevelopmentOrchestrationConflict,
		)
	}
	summary := orchestration.Summary
	iterations := orchestration.Iterations
	if summary == "" {
		summary = "Model work was interrupted while reconciling local Git authority."
		iterations = 0
	}
	attemptResult, err := conn.ExecContext(ctx, `
		UPDATE pr_development_repair_attempts
		SET status = 'recovery_required', claims = 1, summary = ?, error_code = ?,
			internal_error = ?, iterations = ?, updated_at = ?
		WHERE id = ? AND session_id = ? AND status = 'queued' AND claims = 0`,
		summary,
		PRDevelopmentRepairErrorRecoveryRequired,
		"post-model controller authority rotated during local authority recovery",
		iterations,
		toDBTime(now),
		attemptID,
		session.ID,
	)
	if err != nil {
		return false, err
	}
	if rowErr := requireOnePRDevelopmentControllerRow(attemptResult); rowErr != nil {
		return false, rowErr
	}
	sessionResult, err := conn.ExecContext(ctx, `
		UPDATE pr_development_repair_sessions
		SET version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND claim_suppressed = 1 AND version < ?`,
		toDBTime(now),
		session.ID,
		session.Version,
		MaxPRDevelopmentRepairVersion,
	)
	if err != nil {
		return false, err
	}
	if rowErr := requireOnePRDevelopmentControllerRow(sessionResult); rowErr != nil {
		return false, rowErr
	}
	orchestrationResult, err := conn.ExecContext(ctx, `
		UPDATE pr_development_repair_orchestrations
		SET phase = 'recovery_required', claim_owner = '', claim_token = '',
			claim_until = NULL, recovery_required_at = ?, updated_at = ?
		WHERE attempt_id = ? AND phase IN ('editing', 'edited', 'validated') AND
			controller_id = ? AND model_controller_revision = ? AND
			model_lease_epoch = ? AND model_lease_token_digest = ? AND
			model_reservation_digest = ?`,
		toDBTime(now),
		toDBTime(now),
		attemptID,
		fence.controllerID,
		fence.controllerRev,
		fence.leaseEpoch,
		fence.leaseTokenDigest,
		fence.reservationHash,
	)
	if err != nil {
		return false, err
	}
	if rowErr := requireOnePRDevelopmentControllerRow(orchestrationResult); rowErr != nil {
		return false, rowErr
	}
	return true, nil
}

// preflightPRDevelopmentRepairOrchestrationOperation adds the v14 causal
// ordering and exact-evidence rules without changing legacy v13 operation
// behavior for attempts that have no orchestration row.
func preflightPRDevelopmentRepairOrchestrationOperation(
	ctx context.Context,
	conn *sql.Conn,
	controller PRDevelopmentController,
	relation prDevelopmentControllerAttemptRelation,
	operations []PRDevelopmentControllerOperation,
	kind PRDevelopmentControllerOperationKind,
	request PRDevelopmentControllerOperationRequest,
) error {
	orchestration, found, err := loadPRDevelopmentRepairOrchestration(
		ctx, conn, relation.Attempt.ID,
	)
	if err != nil || !found {
		return err
	}
	if orchestration.ControllerID != controller.ID ||
		orchestration.SessionID != relation.Session.ID ||
		orchestration.CaseID != relation.Session.CaseID ||
		orchestration.ThreadID != relation.Thread.ID ||
		orchestration.AgentID != controller.AgentID ||
		orchestration.HeadRepository != relation.Session.HeadRepository ||
		orchestration.HeadRef != relation.Session.HeadRef ||
		orchestration.HeadSHA != relation.Session.HeadSHA ||
		orchestration.CloneURL != relation.Session.CloneURL ||
		orchestration.ReviewDigest != relation.Session.ReviewDigest ||
		orchestration.WorkspaceID != relation.Session.WorkspaceID {
		return fmt.Errorf(
			"%w: controller operation differs from its orchestration owner baseline",
			ErrPRDevelopmentOrchestrationConflict,
		)
	}
	switch kind {
	case PRDevelopmentControllerOperationAdopt:
		if orchestration.Phase != PRDevelopmentRepairOrchestrationBootstrap ||
			controller.WorkspaceID != "" || controller.SourceTree != "" ||
			request.ExpectedTree != orchestration.SourceTree {
			return fmt.Errorf(
				"%w: Adopt must bind the exact bootstrap source tree",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
	case PRDevelopmentControllerOperationResume:
		if orchestration.Phase != PRDevelopmentRepairOrchestrationBootstrap ||
			controller.WorkspaceID != orchestration.WorkspaceID ||
			controller.SourceCloneURL != orchestration.CloneURL ||
			controller.SourceRef != orchestration.HeadRef ||
			controller.SourceCommit != orchestration.HeadSHA ||
			controller.SourceTree != orchestration.SourceTree {
			return fmt.Errorf(
				"%w: Resume must use the exact retained bootstrap line",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
	case PRDevelopmentControllerOperationCommit:
		receipt := orchestration.Validation
		if orchestration.Phase != PRDevelopmentRepairOrchestrationValidated ||
			receipt == nil || receipt.NoChanges || receipt.ControllerID != controller.ID ||
			receipt.WorkspaceID != controller.WorkspaceID ||
			receipt.LineID != controller.LineID ||
			receipt.ControllerRevision != controller.Revision ||
			receipt.LineVersion != controller.LineVersion ||
			receipt.MutationEpoch != controller.MutationEpoch ||
			receipt.MutationLeaseEpoch != controller.LeaseEpoch ||
			receipt.MutationLeaseTokenDigest != prDevelopmentLeaseTokenDigest(
				PRDevelopmentControllerMutationLease, controller.LeaseToken,
			) || receipt.MutationReservationDigest != prDevelopmentMutationReservationDigest(
			controller.MutationReservationKey,
		) || receipt.ParentCommit != controller.TipCommit ||
			receipt.ParentTree != controller.Tree ||
			request.ExpectedParent != receipt.ParentCommit ||
			request.ExpectedTree != receipt.CandidateTree ||
			request.CandidateDigest != receipt.CandidateDigest {
			return fmt.Errorf(
				"%w: Commit has no exact validated candidate receipt",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
	case PRDevelopmentControllerOperationPark:
		return preflightPRDevelopmentRepairOrchestrationPark(
			ctx, conn, controller, relation, operations, request,
		)
	default:
		return fmt.Errorf(
			"%w: unsupported orchestrated controller operation",
			ErrPRDevelopmentOrchestrationConflict,
		)
	}
	return nil
}

// preflightPRDevelopmentRepairOrchestrationPark proves before the external
// Park effect that the immutable receipt, optional Commit, and next ledger
// position all describe the exact controller candidate. Legacy v13 attempts
// without an orchestration row retain their established behavior.
func preflightPRDevelopmentRepairOrchestrationPark(
	ctx context.Context,
	conn *sql.Conn,
	controller PRDevelopmentController,
	relation prDevelopmentControllerAttemptRelation,
	operations []PRDevelopmentControllerOperation,
	request PRDevelopmentControllerOperationRequest,
) error {
	orchestration, found, err := loadPRDevelopmentRepairOrchestration(
		ctx, conn, relation.Attempt.ID,
	)
	if err != nil || !found {
		return err
	}
	if orchestration.Phase != PRDevelopmentRepairOrchestrationValidated ||
		orchestration.Validation == nil || orchestration.ControllerID != controller.ID ||
		orchestration.SessionID != relation.Session.ID ||
		orchestration.CaseID != relation.Session.CaseID ||
		orchestration.ThreadID != relation.Thread.ID ||
		orchestration.Summary != request.CompletionSummary ||
		orchestration.Iterations != request.CompletionIterations {
		return fmt.Errorf(
			"%w: Park has no exact validated orchestration account",
			ErrPRDevelopmentOrchestrationConflict,
		)
	}
	receipt := *orchestration.Validation
	if receipt.ControllerID != controller.ID || receipt.WorkspaceID != controller.WorkspaceID ||
		receipt.LineID != controller.LineID || receipt.ControllerRevision != controller.Revision ||
		receipt.LineVersion != controller.LineVersion ||
		receipt.MutationEpoch != controller.MutationEpoch ||
		receipt.MutationLeaseEpoch != controller.LeaseEpoch ||
		receipt.MutationLeaseTokenDigest != prDevelopmentLeaseTokenDigest(
			PRDevelopmentControllerMutationLease, controller.LeaseToken,
		) || receipt.MutationReservationDigest != prDevelopmentMutationReservationDigest(
		controller.MutationReservationKey,
	) || receipt.ParentCommit != controller.TipCommit || receipt.ParentTree != controller.Tree ||
		receipt.ModelResultDigest != orchestration.ModelResultDigest ||
		receipt.NoChanges != request.NoChanges || receipt.CandidateTree != request.Tree {
		return fmt.Errorf(
			"%w: Park candidate differs from its immutable validation receipt",
			ErrPRDevelopmentOrchestrationConflict,
		)
	}
	commit, committed := finalizedPRDevelopmentCommitOperation(
		operations, controller.CurrentAttemptID,
	)
	if receipt.NoChanges {
		if committed || request.Tip != receipt.ParentCommit ||
			request.Tree != receipt.ParentTree || receipt.ChangedFiles != 0 {
			return fmt.Errorf(
				"%w: no-change receipt cannot Park a committed candidate",
				ErrPRDevelopmentOrchestrationConflict,
			)
		}
	} else if !committed || commit.Request.ExpectedParent != receipt.ParentCommit ||
		commit.Request.ExpectedTree != receipt.CandidateTree ||
		commit.Request.CandidateDigest != receipt.CandidateDigest ||
		commit.Result.ParentCommit != receipt.ParentCommit ||
		commit.Result.Tree != receipt.CandidateTree ||
		commit.Result.CandidateDigest != receipt.CandidateDigest ||
		commit.Result.ChangedFiles != receipt.ChangedFiles ||
		commit.Result.Commit != request.Tip {
		return fmt.Errorf(
			"%w: committed Park candidate differs from its validation receipt",
			ErrPRDevelopmentOrchestrationConflict,
		)
	}
	return preflightPRDevelopmentRepairOrchestrationLedger(
		ctx, conn, controller.ThreadID, controller.FenceCount,
	)
}

func preflightPRDevelopmentRepairOrchestrationLedger(
	ctx context.Context,
	queryer rowsQueryer,
	threadID string,
	fenceOrdinal int,
) error {
	if fenceOrdinal < 0 || fenceOrdinal >= MaxPRDevelopmentControllerFences {
		return ErrPRDevelopmentLedgerCapacity
	}
	entries, err := loadPRDevelopmentLedgerEntries(ctx, queryer, threadID)
	if err != nil {
		return err
	}
	if len(entries) >= MaxPRDevelopmentLedgerEntries {
		return ErrPRDevelopmentLedgerCapacity
	}
	expectedOrdinal := fenceOrdinal * 2
	if len(entries) == 0 {
		return nil
	}
	tail := entries[len(entries)-1]
	if tail.Kind != PRDevelopmentLedgerReview || tail.Ordinal != expectedOrdinal-1 {
		return fmt.Errorf(
			"%w: Park attempt account would create a ledger gap",
			ErrPRDevelopmentLedgerConflict,
		)
	}
	return nil
}

// finalizePRDevelopmentRepairOrchestrationPark runs inside the same SQLite
// transaction as attempt completion, review-fence creation, controller
// release, and operation finalization (normal or recovery). No durable state
// can observe Park without its attempt ledger account.
func finalizePRDevelopmentRepairOrchestrationPark(
	ctx context.Context,
	conn *sql.Conn,
	controller PRDevelopmentController,
	operation PRDevelopmentControllerOperation,
	result PRDevelopmentControllerOperationResult,
	fence PRDevelopmentAttemptReviewFence,
	now time.Time,
) error {
	orchestration, found, err := loadStoredPRDevelopmentRepairOrchestration(
		ctx, conn, operation.AttemptID,
	)
	if err != nil || !found {
		return err
	}
	if orchestration.Phase != PRDevelopmentRepairOrchestrationValidated ||
		orchestration.Validation == nil || orchestration.ControllerID != controller.ID ||
		orchestration.ThreadID != controller.ThreadID ||
		orchestration.Summary != operation.Request.CompletionSummary ||
		orchestration.Iterations != operation.Request.CompletionIterations {
		return fmt.Errorf(
			"%w: Park finalization lost its validated orchestration account",
			ErrPRDevelopmentOrchestrationConflict,
		)
	}
	receipt := *orchestration.Validation
	if receipt.ControllerRevision != operation.PreparedControllerRevision ||
		receipt.MutationLeaseEpoch != operation.MutationLeaseEpoch ||
		receipt.MutationLeaseTokenDigest != operation.MutationLeaseTokenDigest ||
		receipt.MutationReservationDigest != operation.MutationReservationDigest ||
		receipt.ParentCommit != operation.TipCommit ||
		receipt.ParentTree != operation.Tree || receipt.NoChanges != result.NoChanges ||
		receipt.CandidateTree != result.Tree || fence.TipCommit != result.Tip ||
		fence.Tree != receipt.CandidateTree || fence.NoChanges != receipt.NoChanges {
		return fmt.Errorf(
			"%w: Park result differs from its consumed validation receipt",
			ErrPRDevelopmentOrchestrationConflict,
		)
	}
	if preflightErr := preflightPRDevelopmentRepairOrchestrationLedger(
		ctx, conn, controller.ThreadID, fence.Ordinal,
	); preflightErr != nil {
		return preflightErr
	}
	var caseOrdinal int64
	if queryErr := conn.QueryRowContext(ctx, `
		SELECT ordinal
		FROM pr_development_thread_cases
		WHERE thread_id = ? AND case_id = ?`,
		controller.ThreadID,
		orchestration.CaseID,
	).Scan(&caseOrdinal); queryErr != nil {
		return queryErr
	}
	if caseOrdinal < 0 || caseOrdinal >= MaxPRDevelopmentThreadCases {
		return fmt.Errorf(
			"%w: orchestration owner case ordinal is invalid",
			ErrPRDevelopmentLedgerConflict,
		)
	}
	entries, err := loadPRDevelopmentLedgerEntries(ctx, conn, controller.ThreadID)
	if err != nil {
		return err
	}
	if len(entries) != 0 && now.Before(entries[len(entries)-1].CreatedAt) {
		return fmt.Errorf(
			"%w: store clock regressed behind the orchestration ledger high-water",
			ErrInvalidPRDevelopmentLedger,
		)
	}
	previousHash := emptyPRDevelopmentLedgerEntriesDigest()
	if len(entries) != 0 {
		previousHash = entries[len(entries)-1].EntryHash
	}
	entryID, err := newPrefixedID(prDevelopmentLedgerEntryIDPrefix)
	if err != nil {
		return err
	}
	entry := PRDevelopmentLedgerEntry{
		ID:             entryID,
		ThreadID:       controller.ThreadID,
		Ordinal:        fence.Ordinal * 2,
		Kind:           PRDevelopmentLedgerAttempt,
		AttemptID:      fence.AttemptID,
		FenceOrdinal:   fence.Ordinal,
		CaseID:         orchestration.CaseID,
		CaseOrdinal:    int(caseOrdinal),
		Commit:         fence.TipCommit,
		Tree:           fence.Tree,
		NoChanges:      fence.NoChanges,
		Summary:        orchestration.Summary,
		CIPlanDigest:   receipt.CIEffectivePlanDigest,
		CIResultDigest: receipt.CIExecutionDigest,
		CIStatus:       receipt.CIStatus,
		ciStatusBound:  true,
		FenceHash:      mutationStagePRDevelopmentReviewFenceHash(fence),
		PreviousHash:   previousHash,
		CreatedAt:      now,
	}
	entry.EntryHash = hashPRDevelopmentLedgerEntry(entry)
	if validationErr := validateStoredPRDevelopmentLedgerEntry(entry); validationErr != nil {
		return validationErr
	}
	if insertErr := insertPRDevelopmentLedgerEntry(ctx, conn, entry); insertErr != nil {
		return insertErr
	}
	update, updateErr := conn.ExecContext(ctx, `
		UPDATE pr_development_repair_orchestrations
		SET phase = 'completed', claim_owner = '', claim_token = '',
			claim_until = NULL, park_operation_id = ?, ledger_entry_id = ?,
			fence_hash = ?, completed_at = ?, updated_at = ?
		WHERE attempt_id = ? AND phase = 'validated' AND receipt_hash = ?`,
		operation.ID,
		entry.ID,
		fence.FenceHash,
		toDBTime(now),
		toDBTime(now),
		operation.AttemptID,
		receipt.ReceiptHash,
	)
	if updateErr != nil {
		return updateErr
	}
	return requireOnePRDevelopmentControllerRow(update)
}

func expireEditingPRDevelopmentRepairOrchestrations(
	ctx context.Context,
	conn *sql.Conn,
	now time.Time,
) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT attempt_id, session_id
		FROM pr_development_repair_orchestrations
		WHERE phase = 'editing' AND claim_until <= ?
		ORDER BY created_at, attempt_id
		LIMIT ?`,
		toDBTime(now),
		maxPRDevelopmentRepairOrchestrationCandidates,
	)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type expired struct{ attemptID, sessionID string }
	items := make([]expired, 0)
	for rows.Next() {
		var item expired
		if scanErr := rows.Scan(&item.attemptID, &item.sessionID); scanErr != nil {
			return scanErr
		}
		items = append(items, item)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return rowsErr
	}
	if closeErr := rows.Close(); closeErr != nil {
		return closeErr
	}
	for _, item := range items {
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_orchestrations
			SET phase = 'recovery_required', claim_owner = '', claim_token = '',
				claim_until = NULL, recovery_required_at = ?, updated_at = ?
			WHERE attempt_id = ? AND phase = 'editing' AND claim_until <= ?`,
			toDBTime(now),
			toDBTime(now),
			item.attemptID,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
			return rowErr
		}
		result, updateErr = conn.ExecContext(ctx, `
			UPDATE pr_development_repair_attempts
			SET status = 'recovery_required', claims = 1,
				summary = ?, error_code = 'recovery_required', internal_error = ?,
				updated_at = ?
			WHERE id = ? AND session_id = ? AND status = 'queued' AND claims = 0`,
			"AI editing ownership expired; inspect the preserved retained line before continuing.",
			"repair orchestration claim expired after model editing began",
			toDBTime(now),
			item.attemptID,
			item.sessionID,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
			return rowErr
		}
		result, updateErr = conn.ExecContext(ctx, `
			UPDATE pr_development_repair_sessions
			SET version = version + 1, updated_at = ?
			WHERE id = ? AND version < ?`,
			toDBTime(now),
			item.sessionID,
			MaxPRDevelopmentRepairVersion,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
			return rowErr
		}
	}
	return nil
}

func requireLivePRDevelopmentRepairOrchestrationClaim(
	orchestration PRDevelopmentRepairOrchestration,
	claimToken string,
	now time.Time,
	phase PRDevelopmentRepairOrchestrationPhase,
) error {
	return requireLivePRDevelopmentRepairOrchestrationClaimAny(
		orchestration, claimToken, now, phase,
	)
}

func requireLivePRDevelopmentRepairOrchestrationClaimAnySafe(
	orchestration PRDevelopmentRepairOrchestration,
	claimToken string,
	now time.Time,
) error {
	return requireLivePRDevelopmentRepairOrchestrationClaimAny(
		orchestration,
		claimToken,
		now,
		PRDevelopmentRepairOrchestrationBootstrap,
		PRDevelopmentRepairOrchestrationEdited,
		PRDevelopmentRepairOrchestrationValidated,
	)
}

func requireLivePRDevelopmentRepairOrchestrationClaimAny(
	orchestration PRDevelopmentRepairOrchestration,
	claimToken string,
	now time.Time,
	phases ...PRDevelopmentRepairOrchestrationPhase,
) error {
	phaseAllowed := false
	for _, phase := range phases {
		if orchestration.Phase == phase {
			phaseAllowed = true
			break
		}
	}
	if !phaseAllowed || orchestration.ClaimToken != claimToken ||
		orchestration.ClaimUntil == nil || !orchestration.ClaimUntil.After(now) {
		return ErrStaleLease
	}
	return nil
}

func loadExactPRDevelopmentRepairOrchestrationController(
	ctx context.Context,
	queryer rowsQueryer,
	orchestration PRDevelopmentRepairOrchestration,
	controllerID string,
	revision int64,
	leaseToken string,
	leaseEpoch int64,
	now time.Time,
) (PRDevelopmentController, error) {
	controller, found, err := loadPRDevelopmentControllerAggregateByID(
		ctx, queryer, controllerID,
	)
	if err != nil {
		return PRDevelopmentController{}, err
	}
	if !found {
		return PRDevelopmentController{}, sql.ErrNoRows
	}
	if orchestration.ControllerID != controller.ID ||
		controller.OwnerSessionID != orchestration.SessionID ||
		controller.ThreadID != orchestration.ThreadID ||
		controller.CurrentAttemptID != orchestration.AttemptID ||
		controller.Revision != revision || controller.LeaseEpoch != leaseEpoch ||
		controller.Phase != PRDevelopmentControllerMutation ||
		controller.LeaseKind != PRDevelopmentControllerMutationLease ||
		controller.LeaseToken != leaseToken || controller.LeaseUntil == nil ||
		!controller.LeaseUntil.After(now) || controller.MutationReservationKey == "" {
		return PRDevelopmentController{}, fmt.Errorf(
			"%w: orchestration does not own the exact live mutation controller",
			ErrPRDevelopmentOrchestrationConflict,
		)
	}
	return controller, nil
}

func normalizePRDevelopmentRepairOrchestrationValidation(
	input *PRDevelopmentRepairOrchestrationValidation,
) error {
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.ClaimToken = strings.TrimSpace(input.ClaimToken)
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.MutationLeaseToken = strings.TrimSpace(input.MutationLeaseToken)
	input.ParentCommit = strings.TrimSpace(input.ParentCommit)
	input.ParentTree = strings.TrimSpace(input.ParentTree)
	input.CandidateTree = strings.TrimSpace(input.CandidateTree)
	input.CandidateDigest = strings.TrimSpace(input.CandidateDigest)
	input.CIAttestationID = strings.TrimSpace(input.CIAttestationID)
	input.CIAttestationDigest = strings.TrimSpace(input.CIAttestationDigest)
	input.CIResultKey = strings.TrimSpace(input.CIResultKey)
	input.CIEffectivePlanDigest = strings.TrimSpace(input.CIEffectivePlanDigest)
	input.CIExecutionDigest = strings.TrimSpace(input.CIExecutionDigest)
	validCandidateShape := input.NoChanges && input.ChangedFiles == 0 &&
		input.CandidateTree == input.ParentTree || !input.NoChanges &&
		input.ChangedFiles >= 1 && input.ChangedFiles <= 10_000 &&
		input.CandidateTree != input.ParentTree
	if !validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPRDevelopmentRepairIdentity(input.ClaimToken, maxPRDevelopmentRepairLeaseBytes) ||
		!validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPRDevelopmentRepairIdentity(
			input.MutationLeaseToken, prDevelopmentControllerLeaseTokenBytes,
		) || input.ControllerRevision < 1 ||
		input.ControllerRevision > MaxPRDevelopmentControllerRevision ||
		input.MutationLeaseEpoch < 1 ||
		!validSameWidthPRDevelopmentOIDs(
			input.ParentCommit, input.ParentTree, input.CandidateTree,
		) || !validCandidateShape ||
		!validPRDevelopmentHex(input.CandidateDigest, sha256.Size*2) ||
		!validPRDevelopmentCIStatus(input.CIStatus) ||
		!validPRDevelopmentCIIdentity(input.CIAttestationID) ||
		!validPRDevelopmentHex(input.CIAttestationDigest, sha256.Size*2) ||
		!validPRDevelopmentHex(input.CIResultKey, sha256.Size*2) ||
		!validPRDevelopmentHex(input.CIEffectivePlanDigest, sha256.Size*2) ||
		!validPRDevelopmentHex(input.CIExecutionDigest, sha256.Size*2) {
		return fmt.Errorf(
			"%w: exact candidate and attested terminal local-CI evidence are required",
			ErrInvalidPRDevelopmentOrchestration,
		)
	}
	return nil
}

func validPRDevelopmentCIStatus(status PRDevelopmentCIStatus) bool {
	switch status {
	case PRDevelopmentCIPassed,
		PRDevelopmentCIFailed,
		PRDevelopmentCIIncomplete,
		PRDevelopmentCIPlanChanged,
		PRDevelopmentCITimedOut,
		PRDevelopmentCICanceled,
		PRDevelopmentCIOutputLimitExceeded,
		PRDevelopmentCIEnvironmentUnavailable,
		PRDevelopmentCIInfrastructureError:
		return true
	default:
		return false
	}
}

func validPRDevelopmentCIIdentity(value string) bool {
	if len(value) < 1 || len(value) > 256 {
		return false
	}
	for index, character := range []byte(value) {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			index > 0 && strings.ContainsRune("._:/@+-", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func equalPRDevelopmentRepairOrchestrationPin(
	current PRDevelopmentRepairOrchestration,
	input PRDevelopmentRepairOrchestrationPin,
) bool {
	return current.HeadRepository == input.HeadRepository &&
		current.HeadRef == input.HeadRef && current.HeadSHA == input.HeadSHA &&
		current.CloneURL == input.CloneURL && current.ReviewDigest == input.ReviewDigest &&
		current.WorkspaceID == input.WorkspaceID && current.SourceTree == input.SourceTree
}

func equalPRDevelopmentRepairOrchestrationModelFence(
	orchestration PRDevelopmentRepairOrchestration,
	controller PRDevelopmentController,
) bool {
	return orchestration.ControllerID == controller.ID &&
		orchestration.WorkspaceID == controller.WorkspaceID &&
		orchestration.ModelControllerRevision == controller.Revision &&
		orchestration.ModelLineID == controller.LineID &&
		orchestration.ModelLineVersion == controller.LineVersion &&
		orchestration.ModelMutationEpoch == controller.MutationEpoch &&
		orchestration.ModelMutationLeaseEpoch == controller.LeaseEpoch &&
		orchestration.ModelLeaseTokenDigest == prDevelopmentLeaseTokenDigest(
			PRDevelopmentControllerMutationLease, controller.LeaseToken,
		) && orchestration.ModelReservationDigest == prDevelopmentMutationReservationDigest(
		controller.MutationReservationKey,
	)
}

func loadPRDevelopmentRepairOrchestration(
	ctx context.Context,
	queryer rowsQueryer,
	attemptID string,
) (PRDevelopmentRepairOrchestration, bool, error) {
	orchestration, found, err := loadStoredPRDevelopmentRepairOrchestration(
		ctx, queryer, attemptID,
	)
	if err != nil || !found {
		return orchestration, found, err
	}
	if relationErr := validatePRDevelopmentRepairOrchestrationRelation(
		ctx, queryer, orchestration,
	); relationErr != nil {
		return PRDevelopmentRepairOrchestration{}, false, fmt.Errorf(
			"invalid stored pull request development repair orchestration relation: %w",
			relationErr,
		)
	}
	if orchestration.Phase == PRDevelopmentRepairOrchestrationCompleted {
		if completedErr := validateCompletedPRDevelopmentRepairOrchestrationAggregate(
			ctx, queryer, orchestration,
		); completedErr != nil {
			return PRDevelopmentRepairOrchestration{}, false, fmt.Errorf(
				"invalid stored completed pull request development repair orchestration: %w",
				completedErr,
			)
		}
	}
	return orchestration, true, nil
}

// loadStoredPRDevelopmentRepairOrchestration validates the self-contained row
// only. It is reserved for the Park transaction while its public attempt and
// orchestration rows are crossing their atomic terminal boundary.
func loadStoredPRDevelopmentRepairOrchestration(
	ctx context.Context,
	queryer rowsQueryer,
	attemptID string,
) (PRDevelopmentRepairOrchestration, bool, error) {
	orchestration, err := scanPRDevelopmentRepairOrchestration(
		queryer.QueryRowContext(ctx, `
			SELECT `+prDevelopmentRepairOrchestrationColumns+`
			FROM pr_development_repair_orchestrations AS orchestration
			JOIN pr_development_repair_attempts AS attempt
				ON attempt.id = orchestration.attempt_id
			JOIN pr_development_repair_sessions AS session
				ON session.id = orchestration.session_id
			WHERE orchestration.attempt_id = ?`,
			attemptID,
		),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentRepairOrchestration{}, false, nil
	}
	if err != nil {
		return PRDevelopmentRepairOrchestration{}, false, err
	}
	if validationErr := validateStoredPRDevelopmentRepairOrchestration(
		orchestration,
	); validationErr != nil {
		return PRDevelopmentRepairOrchestration{}, false, fmt.Errorf(
			"invalid stored pull request development repair orchestration: %w",
			validationErr,
		)
	}
	return orchestration, true, nil
}

func validatePRDevelopmentRepairOrchestrationRelation(
	ctx context.Context,
	queryer rowsQueryer,
	orchestration PRDevelopmentRepairOrchestration,
) error {
	session, err := loadPRDevelopmentRepairSessionByAttempt(
		ctx, queryer, orchestration.AttemptID,
	)
	if err != nil {
		return err
	}
	binding, err := loadPRDevelopmentThreadBindingForCase(
		ctx, queryer, orchestration.CaseID,
	)
	if err != nil {
		return err
	}
	attempt := findPRDevelopmentRepairAttempt(&session, orchestration.AttemptID)
	if attempt == nil || session.ID != orchestration.SessionID ||
		session.CaseID != orchestration.CaseID || session.AgentID != orchestration.AgentID ||
		attempt.Instruction != orchestration.Instruction ||
		binding.Kind != PRDevelopmentThreadProvider || binding.ID != orchestration.ThreadID {
		return errors.New("orchestration attempt/session/case/thread ownership is invalid")
	}
	eligible, err := isPRDevelopmentRepairOrchestrationEligible(
		ctx, queryer, session.ID, orchestration.ThreadID,
	)
	if err != nil {
		return err
	}
	if !eligible {
		return errors.New("orchestration attempt has no exact cohort or retained controller owner")
	}
	if orchestration.HeadRepository != "" &&
		(session.HeadRepository != orchestration.HeadRepository ||
			session.HeadRef != orchestration.HeadRef || session.HeadSHA != orchestration.HeadSHA ||
			session.CloneURL != orchestration.CloneURL ||
			session.ReviewDigest != orchestration.ReviewDigest ||
			session.WorkspaceID != orchestration.WorkspaceID) {
		return errors.New("orchestration baseline differs from its owner session")
	}
	var suppressed int
	if err := queryer.QueryRowContext(ctx, `
		SELECT claim_suppressed
		FROM pr_development_repair_sessions
		WHERE id = ?`, session.ID).Scan(&suppressed); err != nil {
		return err
	}
	switch orchestration.Phase {
	case PRDevelopmentRepairOrchestrationBootstrap,
		PRDevelopmentRepairOrchestrationEditing,
		PRDevelopmentRepairOrchestrationEdited,
		PRDevelopmentRepairOrchestrationValidated:
		if len(session.Attempts) == 0 ||
			session.Attempts[len(session.Attempts)-1].ID != attempt.ID ||
			attempt.Status != PRDevelopmentRepairQueued || attempt.Claims != 0 || suppressed != 1 {
			return errors.New("live orchestration public attempt fence is invalid")
		}
		if orchestration.ControllerID != "" {
			controller, found, loadErr := loadPRDevelopmentControllerAggregateByID(
				ctx, queryer, orchestration.ControllerID,
			)
			if loadErr != nil {
				return loadErr
			}
			if !found || controller.ThreadID != orchestration.ThreadID ||
				controller.OwnerSessionID != orchestration.SessionID ||
				controller.CurrentAttemptID != orchestration.AttemptID ||
				(controller.Phase != PRDevelopmentControllerMutation &&
					(controller.Phase != PRDevelopmentControllerSuspended ||
						orchestration.Phase != PRDevelopmentRepairOrchestrationBootstrap)) {
				return errors.New("live orchestration controller ownership is invalid")
			}
			if orchestration.Phase != PRDevelopmentRepairOrchestrationBootstrap &&
				!equalPRDevelopmentRepairOrchestrationModelFence(orchestration, controller) {
				return errors.New("live orchestration model fence differs from its controller")
			}
		}
	case PRDevelopmentRepairOrchestrationCompleted:
		if attempt.Status != PRDevelopmentRepairCompleted || attempt.Claims < 1 ||
			attempt.Summary != orchestration.Summary ||
			attempt.Iterations != orchestration.Iterations || suppressed != 1 {
			return errors.New("completed orchestration public attempt fence is invalid")
		}
	case PRDevelopmentRepairOrchestrationFailed:
		if attempt.Status != PRDevelopmentRepairFailed || attempt.Claims < 1 ||
			(suppressed != 0 && suppressed != 1) {
			return errors.New("failed orchestration public attempt fence is invalid")
		}
	case PRDevelopmentRepairOrchestrationRecoveryRequired:
		if attempt.Status != PRDevelopmentRepairRecoveryRequired || attempt.Claims < 1 ||
			suppressed != 1 {
			return errors.New("recovery orchestration public attempt fence is invalid")
		}
	}
	return nil
}

func validateCompletedPRDevelopmentRepairOrchestrationAggregate(
	ctx context.Context,
	queryer rowsQueryer,
	orchestration PRDevelopmentRepairOrchestration,
) error {
	operation, operationFound, err := loadPRDevelopmentControllerOperationByID(
		ctx, queryer, orchestration.ParkOperationID,
	)
	if err != nil {
		return err
	}
	if !operationFound {
		return errors.New("completed orchestration Park operation is missing")
	}
	fence, found, err := loadPRDevelopmentReviewFenceByAttempt(
		ctx, queryer, orchestration.AttemptID,
	)
	if err != nil {
		return err
	}
	storedEntry, err := scanPRDevelopmentLedgerEntry(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentLedgerEntryColumns+`
		FROM pr_development_ledger_entries
		WHERE id = ?`, orchestration.LedgerEntryID))
	if err != nil {
		return err
	}
	entry := storedEntry.entry
	var caseOrdinal int64
	if err := queryer.QueryRowContext(ctx, `
		SELECT ordinal FROM pr_development_thread_cases
		WHERE thread_id = ? AND case_id = ?`,
		orchestration.ThreadID,
		orchestration.CaseID,
	).Scan(&caseOrdinal); err != nil {
		return err
	}
	previousHash := emptyPRDevelopmentLedgerEntriesDigest()
	if entry.Ordinal > 0 {
		predecessorErr := queryer.QueryRowContext(ctx, `
			SELECT entry_hash FROM pr_development_ledger_entries
			WHERE thread_id = ? AND ordinal = ?`,
			orchestration.ThreadID,
			entry.Ordinal-1,
		).Scan(&previousHash)
		if errors.Is(predecessorErr, sql.ErrNoRows) {
			var earlierEntries int
			if err := queryer.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM pr_development_ledger_entries
				WHERE thread_id = ? AND ordinal < ?`,
				orchestration.ThreadID,
				entry.Ordinal,
			).Scan(&earlierEntries); err != nil {
				return err
			}
			if earlierEntries != 0 {
				return errors.New("completed orchestration ledger predecessor is missing")
			}
			// A migrated controller may already have review fences while its
			// pre-ledger history is empty. Its first v14 account is anchored at
			// that fence-derived nonzero ordinal and starts from the empty digest.
			previousHash = emptyPRDevelopmentLedgerEntriesDigest()
		} else if predecessorErr != nil {
			return predecessorErr
		}
	}
	if !found {
		return errors.New("completed orchestration review fence is missing")
	}
	return validateCompletedPRDevelopmentRepairOrchestrationSnapshot(
		orchestration,
		operation,
		fence,
		storedEntry,
		caseOrdinal,
		previousHash,
	)
}

func validateCompletedPRDevelopmentRepairOrchestrationSnapshot(
	orchestration PRDevelopmentRepairOrchestration,
	operation PRDevelopmentControllerOperation,
	fence PRDevelopmentAttemptReviewFence,
	storedEntry storedPRDevelopmentLedgerEntry,
	caseOrdinal int64,
	previousHash string,
) error {
	if orchestration.Validation == nil {
		return errors.New("completed orchestration has no receipt")
	}
	receipt := *orchestration.Validation
	if operation.Kind != PRDevelopmentControllerOperationPark ||
		operation.Status != PRDevelopmentControllerOperationFinalized ||
		operation.ControllerID != orchestration.ControllerID ||
		operation.AttemptID != orchestration.AttemptID ||
		operation.FinalFenceHash != orchestration.FenceHash ||
		operation.Request.CompletionSummary != orchestration.Summary ||
		operation.Request.CompletionIterations != orchestration.Iterations ||
		operation.Result.Tree != receipt.CandidateTree ||
		operation.Result.NoChanges != receipt.NoChanges {
		return errors.New("completed orchestration Park operation is not exact")
	}
	mutationStageFenceHash := mutationStagePRDevelopmentReviewFenceHash(fence)
	if fence.ControllerID != orchestration.ControllerID ||
		fence.ThreadID != orchestration.ThreadID ||
		mutationStageFenceHash != orchestration.FenceHash ||
		fence.TipCommit != operation.Result.Tip || fence.Tree != receipt.CandidateTree ||
		fence.NoChanges != receipt.NoChanges ||
		fence.MutationControllerRevision != receipt.ControllerRevision ||
		fence.MutationLeaseEpoch != receipt.MutationLeaseEpoch ||
		fence.MutationLeaseTokenDigest != receipt.MutationLeaseTokenDigest ||
		fence.MutationReservationDigest != receipt.MutationReservationDigest {
		return errors.New("completed orchestration review fence is not exact")
	}
	entry := storedEntry.entry
	entry.CIStatus = receipt.CIStatus
	entry.ciStatusBound = true
	if storedEntry.findingCount != 0 || entry.ThreadID != orchestration.ThreadID ||
		entry.ID != orchestration.LedgerEntryID ||
		entry.AttemptID != orchestration.AttemptID || entry.Kind != PRDevelopmentLedgerAttempt ||
		entry.Ordinal != fence.Ordinal*2 || entry.FenceOrdinal != fence.Ordinal ||
		entry.CaseID != orchestration.CaseID || int64(entry.CaseOrdinal) != caseOrdinal ||
		entry.Commit != fence.TipCommit || entry.Tree != receipt.CandidateTree ||
		entry.NoChanges != receipt.NoChanges || entry.Summary != orchestration.Summary ||
		entry.CIPlanDigest != receipt.CIEffectivePlanDigest ||
		entry.CIResultDigest != receipt.CIExecutionDigest ||
		entry.FenceHash != mutationStageFenceHash ||
		entry.PreviousHash != previousHash || orchestration.CompletedAt == nil ||
		!entry.CreatedAt.Equal(*orchestration.CompletedAt) ||
		validateStoredPRDevelopmentLedgerEntry(entry) != nil ||
		entry.EntryHash != hashPRDevelopmentLedgerEntry(entry) {
		return errors.New("completed orchestration ledger account is not exact")
	}
	return nil
}

func scanPRDevelopmentRepairOrchestration(
	scanner rowScanner,
) (PRDevelopmentRepairOrchestration, error) {
	var (
		orchestration           PRDevelopmentRepairOrchestration
		claimUntil              sql.NullInt64
		claimEpoch, claims      int64
		iterations              int64
		validationRevision      int64
		validationLineVersion   int64
		validationMutationEpoch int64
		validationLeaseEpoch    int64
		changedFiles            int64
		noChanges               sql.NullInt64
		ciStatus                PRDevelopmentCIStatus
		validationLineID        string
		validationLeaseDigest   string
		validationReservation   string
		parentCommit            string
		parentTree              string
		candidateTree           string
		candidateDigest         string
		ciAttestationID         string
		ciAttestationDigest     string
		ciResultKey             string
		ciEffectivePlanDigest   string
		ciExecutionDigest       string
		receiptHash             string
		createdAt               int64
		modelStartedAt          sql.NullInt64
		modelCompletedAt        sql.NullInt64
		validatedAt             sql.NullInt64
		completedAt             sql.NullInt64
		failedAt                sql.NullInt64
		recoveryRequiredAt      sql.NullInt64
		updatedAt               int64
	)
	if err := scanner.Scan(
		&orchestration.AttemptID,
		&orchestration.SessionID,
		&orchestration.CaseID,
		&orchestration.ThreadID,
		&orchestration.Instruction,
		&orchestration.AgentID,
		&orchestration.Phase,
		&orchestration.ClaimOwner,
		&orchestration.ClaimToken,
		&claimUntil,
		&claimEpoch,
		&claims,
		&orchestration.HeadRepository,
		&orchestration.HeadRef,
		&orchestration.HeadSHA,
		&orchestration.CloneURL,
		&orchestration.ReviewDigest,
		&orchestration.WorkspaceID,
		&orchestration.SourceTree,
		&orchestration.ControllerID,
		&orchestration.ModelControllerRevision,
		&orchestration.ModelLineID,
		&orchestration.ModelLineVersion,
		&orchestration.ModelMutationEpoch,
		&orchestration.ModelMutationLeaseEpoch,
		&orchestration.ModelLeaseTokenDigest,
		&orchestration.ModelReservationDigest,
		&orchestration.ContextDigest,
		&orchestration.PromptDigest,
		&orchestration.ModelResultDigest,
		&orchestration.Summary,
		&iterations,
		&validationRevision,
		&validationLineID,
		&validationLineVersion,
		&validationMutationEpoch,
		&validationLeaseEpoch,
		&validationLeaseDigest,
		&validationReservation,
		&parentCommit,
		&parentTree,
		&candidateTree,
		&candidateDigest,
		&changedFiles,
		&noChanges,
		&ciStatus,
		&ciAttestationID,
		&ciAttestationDigest,
		&ciResultKey,
		&ciEffectivePlanDigest,
		&ciExecutionDigest,
		&receiptHash,
		&orchestration.ParkOperationID,
		&orchestration.LedgerEntryID,
		&orchestration.FenceHash,
		&orchestration.FailedClaimTokenDigest,
		&createdAt,
		&modelStartedAt,
		&modelCompletedAt,
		&validatedAt,
		&completedAt,
		&failedAt,
		&recoveryRequiredAt,
		&updatedAt,
	); err != nil {
		return PRDevelopmentRepairOrchestration{}, err
	}
	if int64(int(claims)) != claims || int64(int(iterations)) != iterations ||
		int64(int(changedFiles)) != changedFiles {
		return PRDevelopmentRepairOrchestration{}, errors.New(
			"stored repair orchestration integer overflows",
		)
	}
	orchestration.ClaimEpoch = claimEpoch
	orchestration.Claims = int(claims)
	orchestration.Iterations = int(iterations)
	orchestration.ClaimUntil = fromNullableTime(claimUntil)
	orchestration.CreatedAt = fromDBTime(createdAt)
	orchestration.ModelStartedAt = fromNullableTime(modelStartedAt)
	orchestration.ModelCompletedAt = fromNullableTime(modelCompletedAt)
	orchestration.ValidatedAt = fromNullableTime(validatedAt)
	orchestration.CompletedAt = fromNullableTime(completedAt)
	orchestration.FailedAt = fromNullableTime(failedAt)
	orchestration.RecoveryRequiredAt = fromNullableTime(recoveryRequiredAt)
	orchestration.UpdatedAt = fromDBTime(updatedAt)
	if receiptHash != "" {
		if !noChanges.Valid || (noChanges.Int64 != 0 && noChanges.Int64 != 1) {
			return PRDevelopmentRepairOrchestration{}, errors.New(
				"stored repair orchestration no-change evidence is invalid",
			)
		}
		created := orchestration.ValidatedAt
		if created == nil {
			return PRDevelopmentRepairOrchestration{}, errors.New(
				"stored repair orchestration receipt has no creation time",
			)
		}
		orchestration.Validation = &PRDevelopmentRepairValidationReceipt{
			ControllerID:              orchestration.ControllerID,
			WorkspaceID:               orchestration.WorkspaceID,
			ModelControllerRevision:   orchestration.ModelControllerRevision,
			ModelLineID:               orchestration.ModelLineID,
			ModelLineVersion:          orchestration.ModelLineVersion,
			ModelMutationEpoch:        orchestration.ModelMutationEpoch,
			ModelMutationLeaseEpoch:   orchestration.ModelMutationLeaseEpoch,
			ModelLeaseTokenDigest:     orchestration.ModelLeaseTokenDigest,
			ModelReservationDigest:    orchestration.ModelReservationDigest,
			ContextDigest:             orchestration.ContextDigest,
			PromptDigest:              orchestration.PromptDigest,
			LineID:                    validationLineID,
			ControllerRevision:        validationRevision,
			LineVersion:               validationLineVersion,
			MutationEpoch:             validationMutationEpoch,
			MutationLeaseEpoch:        validationLeaseEpoch,
			MutationLeaseTokenDigest:  validationLeaseDigest,
			MutationReservationDigest: validationReservation,
			ParentCommit:              parentCommit,
			ParentTree:                parentTree,
			CandidateTree:             candidateTree,
			CandidateDigest:           candidateDigest,
			ChangedFiles:              int(changedFiles),
			NoChanges:                 noChanges.Int64 == 1,
			CIStatus:                  ciStatus,
			CIAttestationID:           ciAttestationID,
			CIAttestationDigest:       ciAttestationDigest,
			CIResultKey:               ciResultKey,
			CIEffectivePlanDigest:     ciEffectivePlanDigest,
			CIExecutionDigest:         ciExecutionDigest,
			ModelResultDigest:         orchestration.ModelResultDigest,
			ModelSummary:              orchestration.Summary,
			ModelIterations:           orchestration.Iterations,
			ReceiptHash:               receiptHash,
			CreatedAt:                 *created,
		}
	}
	return orchestration, nil
}

func validateStoredPRDevelopmentRepairOrchestration(
	orchestration PRDevelopmentRepairOrchestration,
) error {
	if !validPrefixedHexID(orchestration.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(orchestration.SessionID, prDevelopmentRepairSessionIDPrefix) ||
		!validPrefixedHexID(orchestration.CaseID, prDevelopmentCaseIDPrefix) ||
		!validPrefixedHexID(orchestration.ThreadID, prDevelopmentThreadIDPrefix) ||
		!validPRDevelopmentRepairAgentID(orchestration.AgentID) ||
		!validStoredPRDevelopmentRepairText(
			orchestration.Instruction, MaxPRDevelopmentRepairInstructionBytes,
		) || orchestration.ClaimEpoch < 1 || orchestration.Claims < 1 ||
		orchestration.Claims != int(orchestration.ClaimEpoch) ||
		validateDBTimestamp("orchestration creation time", orchestration.CreatedAt) != nil ||
		validateDBTimestamp("orchestration update time", orchestration.UpdatedAt) != nil ||
		orchestration.UpdatedAt.Before(orchestration.CreatedAt) {
		return errors.New("stored repair orchestration header is invalid")
	}
	live := orchestration.Phase == PRDevelopmentRepairOrchestrationBootstrap ||
		orchestration.Phase == PRDevelopmentRepairOrchestrationEditing ||
		orchestration.Phase == PRDevelopmentRepairOrchestrationEdited ||
		orchestration.Phase == PRDevelopmentRepairOrchestrationValidated
	if live != (orchestration.ClaimOwner != "") ||
		live != (orchestration.ClaimToken != "") || live != (orchestration.ClaimUntil != nil) {
		return errors.New("stored repair orchestration claim is partial")
	}
	if live && (!validPRDevelopmentRepairIdentity(
		orchestration.ClaimOwner, MaxPRDevelopmentControllerIdentityBytes,
	) || !validPRDevelopmentRepairIdentity(
		orchestration.ClaimToken, maxPRDevelopmentRepairLeaseBytes,
	) || validateDBTimestamp(
		"orchestration claim deadline", *orchestration.ClaimUntil,
	) != nil) {
		return errors.New("stored repair orchestration claim is invalid")
	}
	pinned := orchestration.HeadRepository != ""
	if pinned != (orchestration.HeadRef != "") || pinned != (orchestration.HeadSHA != "") ||
		pinned != (orchestration.CloneURL != "") || pinned != (orchestration.ReviewDigest != "") ||
		pinned != (orchestration.WorkspaceID != "") || pinned != (orchestration.SourceTree != "") {
		return errors.New("stored repair orchestration baseline is partial")
	}
	if pinned && (!validPRDevelopmentRepository(orchestration.HeadRepository) ||
		!validPRDevelopmentGitRef(orchestration.HeadRef) ||
		!validPRDevelopmentRepairCloneURL(orchestration.CloneURL) ||
		!validPRDevelopmentRepairReviewDigest(orchestration.ReviewDigest) ||
		!validPRDevelopmentRepairIdentity(
			orchestration.WorkspaceID, maxPRDevelopmentRepairWorkspaceBytes,
		) || !validSameWidthPRDevelopmentOIDs(
		orchestration.HeadSHA, orchestration.SourceTree,
	)) {
		return errors.New("stored repair orchestration baseline is invalid")
	}
	if orchestration.ControllerID != "" &&
		!validPrefixedHexID(orchestration.ControllerID, prDevelopmentControllerIDPrefix) {
		return errors.New("stored repair orchestration controller is invalid")
	}
	modelStarted := orchestration.Phase == PRDevelopmentRepairOrchestrationEditing ||
		orchestration.Phase == PRDevelopmentRepairOrchestrationEdited ||
		orchestration.Phase == PRDevelopmentRepairOrchestrationValidated ||
		orchestration.Phase == PRDevelopmentRepairOrchestrationCompleted ||
		orchestration.Phase == PRDevelopmentRepairOrchestrationRecoveryRequired
	if modelStarted {
		if orchestration.ControllerID == "" ||
			orchestration.ModelControllerRevision < 1 ||
			orchestration.ModelControllerRevision > MaxPRDevelopmentControllerRevision ||
			!validPrefixedHexID(orchestration.ModelLineID, prDevelopmentLineIDPrefix) ||
			orchestration.ModelLineVersion < 0 ||
			orchestration.ModelLineVersion >= MaxPRDevelopmentControllerFences ||
			orchestration.ModelMutationEpoch != orchestration.ModelLineVersion+1 ||
			orchestration.ModelMutationLeaseEpoch < 1 ||
			!validPRDevelopmentHex(orchestration.ModelLeaseTokenDigest, sha256.Size*2) ||
			!validPRDevelopmentHex(orchestration.ModelReservationDigest, sha256.Size*2) {
			return errors.New("stored repair orchestration model fence is invalid")
		}
	} else if orchestration.ModelControllerRevision != 0 ||
		orchestration.ModelLineID != "" || orchestration.ModelLineVersion != 0 ||
		orchestration.ModelMutationEpoch != 0 ||
		orchestration.ModelMutationLeaseEpoch != 0 ||
		orchestration.ModelLeaseTokenDigest != "" ||
		orchestration.ModelReservationDigest != "" {
		return errors.New("stored repair orchestration has an unexpected model fence")
	}
	if orchestration.Validation != nil {
		if err := validateStoredPRDevelopmentRepairValidationReceipt(
			*orchestration.Validation,
		); err != nil {
			return err
		}
	}
	switch orchestration.Phase {
	case PRDevelopmentRepairOrchestrationBootstrap:
		if orchestration.ContextDigest != "" || orchestration.ModelResultDigest != "" ||
			orchestration.Validation != nil {
			return errors.New("stored bootstrap orchestration has later evidence")
		}
	case PRDevelopmentRepairOrchestrationEditing:
		if !validPRDevelopmentHex(orchestration.ContextDigest, sha256.Size*2) ||
			!validPRDevelopmentHex(orchestration.PromptDigest, sha256.Size*2) ||
			orchestration.ModelStartedAt == nil || orchestration.Validation != nil {
			return errors.New("stored editing orchestration is invalid")
		}
	case PRDevelopmentRepairOrchestrationEdited,
		PRDevelopmentRepairOrchestrationValidated,
		PRDevelopmentRepairOrchestrationCompleted:
		if !validPRDevelopmentHex(orchestration.ContextDigest, sha256.Size*2) ||
			!validPRDevelopmentHex(orchestration.PromptDigest, sha256.Size*2) ||
			!validPRDevelopmentHex(orchestration.ModelResultDigest, sha256.Size*2) ||
			!validStoredPRDevelopmentRepairText(
				orchestration.Summary, MaxPRDevelopmentRepairSummaryBytes,
			) || orchestration.Iterations < 1 ||
			orchestration.Iterations > MaxPRDevelopmentRepairIterations ||
			orchestration.ModelStartedAt == nil || orchestration.ModelCompletedAt == nil ||
			(orchestration.Phase != PRDevelopmentRepairOrchestrationEdited) !=
				(orchestration.Validation != nil) {
			return errors.New("stored post-model orchestration is invalid")
		}
	case PRDevelopmentRepairOrchestrationRecoveryRequired:
		editingRecovery := validPRDevelopmentHex(
			orchestration.ContextDigest, sha256.Size*2,
		) && validPRDevelopmentHex(
			orchestration.PromptDigest, sha256.Size*2,
		) && orchestration.ModelResultDigest == "" && orchestration.Summary == "" &&
			orchestration.Iterations == 0 && orchestration.ModelStartedAt != nil &&
			orchestration.ModelCompletedAt == nil && orchestration.Validation == nil
		postModelRecovery := validPRDevelopmentHex(
			orchestration.ContextDigest, sha256.Size*2,
		) && validPRDevelopmentHex(
			orchestration.PromptDigest, sha256.Size*2,
		) && validPRDevelopmentHex(
			orchestration.ModelResultDigest, sha256.Size*2,
		) && validStoredPRDevelopmentRepairText(
			orchestration.Summary, MaxPRDevelopmentRepairSummaryBytes,
		) && orchestration.Iterations >= 1 &&
			orchestration.Iterations <= MaxPRDevelopmentRepairIterations &&
			orchestration.ModelStartedAt != nil && orchestration.ModelCompletedAt != nil
		if orchestration.RecoveryRequiredAt == nil ||
			(!editingRecovery && !postModelRecovery) {
			return errors.New("stored recovery orchestration evidence is invalid")
		}
	case PRDevelopmentRepairOrchestrationFailed:
		if orchestration.FailedAt == nil || orchestration.ControllerID != "" ||
			orchestration.ContextDigest != "" || orchestration.ModelStartedAt != nil ||
			orchestration.Validation != nil ||
			!validPRDevelopmentHex(
				orchestration.FailedClaimTokenDigest, sha256.Size*2,
			) {
			return errors.New("stored failed orchestration is invalid")
		}
	default:
		return errors.New("stored repair orchestration phase is invalid")
	}
	if orchestration.Phase == PRDevelopmentRepairOrchestrationCompleted {
		if !validPrefixedHexID(orchestration.ParkOperationID, prDevelopmentOperationIDPrefix) ||
			!validPrefixedHexID(orchestration.LedgerEntryID, prDevelopmentLedgerEntryIDPrefix) ||
			!validPRDevelopmentHex(orchestration.FenceHash, sha256.Size*2) ||
			orchestration.CompletedAt == nil {
			return errors.New("stored completed orchestration tuple is invalid")
		}
	} else if orchestration.ParkOperationID != "" || orchestration.LedgerEntryID != "" ||
		orchestration.FenceHash != "" || orchestration.CompletedAt != nil {
		return errors.New("stored non-completed orchestration has a Park tuple")
	}
	return nil
}

func validateStoredPRDevelopmentRepairValidationReceipt(
	receipt PRDevelopmentRepairValidationReceipt,
) error {
	if !validPrefixedHexID(receipt.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPRDevelopmentRepairIdentity(
			receipt.WorkspaceID, maxPRDevelopmentRepairWorkspaceBytes,
		) || receipt.ModelControllerRevision < 1 ||
		receipt.ModelControllerRevision > MaxPRDevelopmentControllerRevision ||
		!validPrefixedHexID(receipt.ModelLineID, prDevelopmentLineIDPrefix) ||
		receipt.ModelLineVersion < 0 ||
		receipt.ModelLineVersion >= MaxPRDevelopmentControllerFences ||
		receipt.ModelMutationEpoch != receipt.ModelLineVersion+1 ||
		receipt.ModelMutationLeaseEpoch < 1 ||
		!validPRDevelopmentHex(receipt.ModelLeaseTokenDigest, sha256.Size*2) ||
		!validPRDevelopmentHex(receipt.ModelReservationDigest, sha256.Size*2) ||
		!validPRDevelopmentHex(receipt.ContextDigest, sha256.Size*2) ||
		!validPRDevelopmentHex(receipt.PromptDigest, sha256.Size*2) ||
		!validPrefixedHexID(receipt.LineID, prDevelopmentLineIDPrefix) ||
		receipt.ControllerRevision < 1 ||
		receipt.ControllerRevision > MaxPRDevelopmentControllerRevision ||
		receipt.LineVersion < 0 || receipt.LineVersion >= MaxPRDevelopmentControllerFences ||
		receipt.MutationEpoch != receipt.LineVersion+1 || receipt.MutationLeaseEpoch < 1 ||
		!validPRDevelopmentHex(receipt.MutationLeaseTokenDigest, sha256.Size*2) ||
		!validPRDevelopmentHex(receipt.MutationReservationDigest, sha256.Size*2) ||
		!validSameWidthPRDevelopmentOIDs(
			receipt.ParentCommit, receipt.ParentTree, receipt.CandidateTree,
		) || !validPRDevelopmentHex(receipt.CandidateDigest, sha256.Size*2) ||
		(receipt.NoChanges && (receipt.ChangedFiles != 0 ||
			receipt.CandidateTree != receipt.ParentTree)) ||
		(!receipt.NoChanges && (receipt.ChangedFiles < 1 || receipt.ChangedFiles > 10_000 ||
			receipt.CandidateTree == receipt.ParentTree)) ||
		!validPRDevelopmentCIStatus(receipt.CIStatus) ||
		!validPRDevelopmentCIIdentity(receipt.CIAttestationID) ||
		!validPRDevelopmentHex(receipt.CIAttestationDigest, sha256.Size*2) ||
		!validPRDevelopmentHex(receipt.CIResultKey, sha256.Size*2) ||
		!validPRDevelopmentHex(receipt.CIEffectivePlanDigest, sha256.Size*2) ||
		!validPRDevelopmentHex(receipt.CIExecutionDigest, sha256.Size*2) ||
		!validPRDevelopmentHex(receipt.ModelResultDigest, sha256.Size*2) ||
		!validStoredPRDevelopmentRepairText(
			receipt.ModelSummary, MaxPRDevelopmentRepairSummaryBytes,
		) || receipt.ModelIterations < 1 ||
		receipt.ModelIterations > MaxPRDevelopmentRepairIterations ||
		!validPRDevelopmentHex(receipt.ReceiptHash, sha256.Size*2) ||
		receipt.ReceiptHash != hashPRDevelopmentRepairValidationReceipt(receipt) ||
		validateDBTimestamp("orchestration receipt time", receipt.CreatedAt) != nil {
		return errors.New("stored repair orchestration validation receipt is invalid")
	}
	return nil
}

func hashPRDevelopmentRepairValidationReceipt(
	receipt PRDevelopmentRepairValidationReceipt,
) string {
	digest := sha256.New()
	writePRDevelopmentOrchestrationHashField(
		digest, "picoclaw-pr-development-repair-validation-receipt-v2",
	)
	for _, value := range []string{
		receipt.ControllerID,
		receipt.WorkspaceID,
		fmt.Sprintf("%d", receipt.ModelControllerRevision),
		receipt.ModelLineID,
		fmt.Sprintf("%d", receipt.ModelLineVersion),
		fmt.Sprintf("%d", receipt.ModelMutationEpoch),
		fmt.Sprintf("%d", receipt.ModelMutationLeaseEpoch),
		receipt.ModelLeaseTokenDigest,
		receipt.ModelReservationDigest,
		receipt.ContextDigest,
		receipt.PromptDigest,
		receipt.LineID,
		fmt.Sprintf("%d", receipt.ControllerRevision),
		fmt.Sprintf("%d", receipt.LineVersion),
		fmt.Sprintf("%d", receipt.MutationEpoch),
		fmt.Sprintf("%d", receipt.MutationLeaseEpoch),
		receipt.MutationLeaseTokenDigest,
		receipt.MutationReservationDigest,
		receipt.ParentCommit,
		receipt.ParentTree,
		receipt.CandidateTree,
		receipt.CandidateDigest,
		fmt.Sprintf("%d", receipt.ChangedFiles),
		fmt.Sprintf("%t", receipt.NoChanges),
		string(receipt.CIStatus),
		receipt.CIAttestationID,
		receipt.CIAttestationDigest,
		receipt.CIResultKey,
		receipt.CIEffectivePlanDigest,
		receipt.CIExecutionDigest,
		receipt.ModelResultDigest,
		receipt.ModelSummary,
		fmt.Sprintf("%d", receipt.ModelIterations),
		fmt.Sprintf("%d", toDBTime(receipt.CreatedAt)),
	} {
		writePRDevelopmentOrchestrationHashField(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writePRDevelopmentOrchestrationHashField(digest hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(value))
}

func equalPRDevelopmentRepairValidationReceipt(
	left, right PRDevelopmentRepairValidationReceipt,
	includeTime bool,
) bool {
	if left.ControllerID != right.ControllerID || left.WorkspaceID != right.WorkspaceID ||
		left.ModelControllerRevision != right.ModelControllerRevision ||
		left.ModelLineID != right.ModelLineID ||
		left.ModelLineVersion != right.ModelLineVersion ||
		left.ModelMutationEpoch != right.ModelMutationEpoch ||
		left.ModelMutationLeaseEpoch != right.ModelMutationLeaseEpoch ||
		left.ModelLeaseTokenDigest != right.ModelLeaseTokenDigest ||
		left.ModelReservationDigest != right.ModelReservationDigest ||
		left.ContextDigest != right.ContextDigest || left.PromptDigest != right.PromptDigest ||
		left.LineID != right.LineID || left.ControllerRevision != right.ControllerRevision ||
		left.LineVersion != right.LineVersion || left.MutationEpoch != right.MutationEpoch ||
		left.MutationLeaseEpoch != right.MutationLeaseEpoch ||
		left.MutationLeaseTokenDigest != right.MutationLeaseTokenDigest ||
		left.MutationReservationDigest != right.MutationReservationDigest ||
		left.ParentCommit != right.ParentCommit || left.ParentTree != right.ParentTree ||
		left.CandidateTree != right.CandidateTree ||
		left.CandidateDigest != right.CandidateDigest ||
		left.ChangedFiles != right.ChangedFiles || left.NoChanges != right.NoChanges ||
		left.CIStatus != right.CIStatus ||
		left.CIAttestationID != right.CIAttestationID ||
		left.CIAttestationDigest != right.CIAttestationDigest ||
		left.CIResultKey != right.CIResultKey ||
		left.CIEffectivePlanDigest != right.CIEffectivePlanDigest ||
		left.CIExecutionDigest != right.CIExecutionDigest ||
		left.ModelResultDigest != right.ModelResultDigest ||
		left.ModelSummary != right.ModelSummary ||
		left.ModelIterations != right.ModelIterations {
		return false
	}
	return !includeTime || left.CreatedAt.Equal(right.CreatedAt)
}
