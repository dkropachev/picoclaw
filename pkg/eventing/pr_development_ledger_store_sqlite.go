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

var (
	_ PRDevelopmentLedgerReader  = (*Store)(nil)
	_ PRDevelopmentLedgerStore   = (*Store)(nil)
	_ PRDevelopmentContextReader = (*Store)(nil)

	errInvalidStoredPRDevelopmentLedger = errors.New(
		"invalid stored pull request development ledger",
	)
)

const (
	prDevelopmentLedgerEntryColumns = `
		id, thread_id, ordinal, kind, attempt_id, fence_ordinal, case_id,
		case_ordinal, commit_oid, tree_oid, no_changes, summary,
		ci_plan_digest, ci_result_digest, review_outcome, finding_count,
		fence_hash, previous_hash, entry_hash, created_at`
	prDevelopmentLedgerCheckpointColumns = `
		id, thread_id, generation, through_ordinal, source_digest, summary,
		compactor_id, prompt_digest, previous_hash, checkpoint_hash, created_at`

	maxPRDevelopmentLedgerFindingTitleBytes          = 512
	maxPRDevelopmentLedgerFindingFileBytes           = 4096
	maxPRDevelopmentLedgerFindingMessageBytes        = 8192
	maxPRDevelopmentLedgerFindingEvidenceBytes       = 8192
	maxPRDevelopmentLedgerFindingImpactBytes         = 4096
	maxPRDevelopmentLedgerFindingRecommendationBytes = 8192
	maxPRDevelopmentLedgerFindingValidationBytes     = 4096
)

// GetPRDevelopmentLedgerForCase returns the complete immutable ledger for the
// selected verified provider thread. Every entry, finding, checkpoint, thread
// membership, owner session, controller, and review fence is validated inside
// one SQLite read snapshot.
func (s *Store) GetPRDevelopmentLedgerForCase(
	ctx context.Context,
	caseID string,
) (PRDevelopmentLedger, error) {
	snapshot, err := s.GetPRDevelopmentContextSnapshot(ctx, caseID)
	if err != nil {
		return PRDevelopmentLedger{}, err
	}
	return snapshot.Ledger, nil
}

// GetPRDevelopmentContextSnapshot atomically captures the verified provider
// thread high-water, selected membership ordinal, and private ledger.
func (s *Store) GetPRDevelopmentContextSnapshot(
	ctx context.Context,
	caseID string,
) (PRDevelopmentContextSnapshot, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentContextSnapshot{}, err
	}
	caseID = strings.TrimSpace(caseID)
	if !validPrefixedHexID(caseID, prDevelopmentCaseIDPrefix) {
		return PRDevelopmentContextSnapshot{}, fmt.Errorf(
			"%w: invalid development case ID",
			ErrInvalidPRDevelopmentLedger,
		)
	}
	var snapshot PRDevelopmentContextSnapshot
	err := s.withPRDevelopmentConversationReadSnapshot(
		ctx,
		func(queryer rowsQueryer) error {
			thread, loadErr := loadPRDevelopmentThreadForCase(ctx, queryer, caseID)
			if loadErr != nil {
				return loadErr
			}
			if thread.Kind != PRDevelopmentThreadProvider {
				return fmt.Errorf(
					"%w: legacy threads cannot own a development ledger",
					ErrPRDevelopmentLedgerConflict,
				)
			}
			loaded, loadErr := loadPRDevelopmentLedgerAggregate(ctx, queryer, thread)
			if loadErr != nil {
				return loadErr
			}
			selectedOrdinal := -1
			for _, link := range thread.Cases {
				if link.CaseID == caseID {
					selectedOrdinal = link.Ordinal
					break
				}
			}
			if selectedOrdinal < 0 {
				return errors.New("selected development case disappeared from its thread")
			}
			snapshot = PRDevelopmentContextSnapshot{
				SelectedOrdinal: selectedOrdinal,
				Thread:          thread,
				Ledger:          loaded,
			}
			return nil
		},
	)
	if err != nil {
		return PRDevelopmentContextSnapshot{}, fmt.Errorf(
			"get pull request development context snapshot: %w",
			s.dbError(err),
		)
	}
	return snapshot, nil
}

// AppendPRDevelopmentLedgerAttempt records one concise successful local
// attempt account after its immutable parked fence exists.
func (s *Store) AppendPRDevelopmentLedgerAttempt(
	ctx context.Context,
	input PRDevelopmentLedgerAttemptAppend,
) (PRDevelopmentLedgerEntry, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentLedgerEntry{}, false, err
	}
	normalized, err := normalizePRDevelopmentLedgerAttemptAppend(input)
	if err != nil {
		return PRDevelopmentLedgerEntry{}, false, err
	}
	var (
		entry   PRDevelopmentLedgerEntry
		changed bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		thread, binding, controller, fence, ledger, loadErr := loadPRDevelopmentLedgerAppendState(
			ctx,
			conn,
			normalized.CaseID,
			normalized.AttemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		_ = thread
		if fence.Ordinal >= MaxPRDevelopmentControllerFences {
			return ErrPRDevelopmentLedgerCapacity
		}
		expectedOrdinal := fence.Ordinal * 2
		candidate := PRDevelopmentLedgerEntry{
			ThreadID:       controller.ThreadID,
			Ordinal:        expectedOrdinal,
			Kind:           PRDevelopmentLedgerAttempt,
			AttemptID:      fence.AttemptID,
			FenceOrdinal:   fence.Ordinal,
			CaseID:         binding.Case.CaseID,
			CaseOrdinal:    binding.Case.Ordinal,
			Commit:         fence.TipCommit,
			Tree:           fence.Tree,
			NoChanges:      fence.NoChanges,
			Summary:        normalized.Summary,
			CIPlanDigest:   normalized.CIPlanDigest,
			CIResultDigest: normalized.CIResultDigest,
			CIStatus:       PRDevelopmentCIPassed,
			FenceHash:      mutationStagePRDevelopmentReviewFenceHash(fence),
		}
		baseOrdinal := expectedOrdinal
		if len(ledger.Entries) != 0 {
			baseOrdinal = ledger.Entries[0].Ordinal
		}
		entryIndex := expectedOrdinal - baseOrdinal
		if entryIndex < 0 {
			return fmt.Errorf(
				"%w: attempt predates the anchored ledger",
				ErrPRDevelopmentLedgerConflict,
			)
		}
		if entryIndex < len(ledger.Entries) {
			existing := ledger.Entries[entryIndex]
			if equalPRDevelopmentLedgerEntryIntent(existing, candidate) {
				entry = existing
				return nil
			}
			return fmt.Errorf(
				"%w: changed attempt-account replay",
				ErrPRDevelopmentLedgerConflict,
			)
		}
		if fence.ReviewedAt != nil {
			return fmt.Errorf(
				"%w: cannot start an account for an already reviewed fence",
				ErrPRDevelopmentLedgerConflict,
			)
		}
		if expectedOrdinal != baseOrdinal+len(ledger.Entries) {
			return fmt.Errorf(
				"%w: attempt account would create a ledger gap",
				ErrPRDevelopmentLedgerConflict,
			)
		}
		if len(ledger.Entries) >= MaxPRDevelopmentLedgerEntries {
			return ErrPRDevelopmentLedgerCapacity
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		highWater := fence.CreatedAt
		if len(ledger.Entries) != 0 {
			highWater = ledger.Entries[len(ledger.Entries)-1].CreatedAt
		}
		if now.Before(highWater) {
			return fmt.Errorf(
				"%w: store clock regressed behind ledger high-water time",
				ErrInvalidPRDevelopmentLedger,
			)
		}
		candidate.ID, err = newPrefixedID(prDevelopmentLedgerEntryIDPrefix)
		if err != nil {
			return err
		}
		candidate.PreviousHash = ledger.EntriesDigest
		candidate.CreatedAt = now
		candidate.EntryHash = hashPRDevelopmentLedgerEntry(candidate)
		if insertErr := insertPRDevelopmentLedgerEntry(ctx, conn, candidate); insertErr != nil {
			return insertErr
		}
		ledger.Entries = append(ledger.Entries, candidate)
		ledger.EntriesDigest = candidate.EntryHash
		if validateErr := validatePRDevelopmentLedgerAggregate(
			ctx,
			conn,
			thread,
			controller,
			ledger,
		); validateErr != nil {
			return validateErr
		}
		entry = candidate
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentLedgerEntry{}, false, fmt.Errorf(
			"append pull request development attempt ledger entry: %w",
			s.dbError(err),
		)
	}
	return entry, changed, nil
}

// AppendPRDevelopmentLedgerReview atomically records the structured outcome,
// marks the exact immutable fence reviewed, and releases its live review lease
// into ready. A finished fence without a ledger review cannot be backfilled.
func (s *Store) AppendPRDevelopmentLedgerReview(
	ctx context.Context,
	input PRDevelopmentLedgerReviewAppend,
) (PRDevelopmentLedgerEntry, bool, error) {
	completion, changed, err := s.appendPRDevelopmentLedgerReview(ctx, input, false)
	return completion.Entry, changed, err
}

// CompletePRDevelopmentReview atomically appends the exact structured review,
// finishes its reservation-free controller lease into ready, and admits one
// deterministic next repair attempt only for changes_required.
func (s *Store) CompletePRDevelopmentReview(
	ctx context.Context,
	input PRDevelopmentLedgerReviewAppend,
) (PRDevelopmentReviewCompletion, bool, error) {
	return s.appendPRDevelopmentLedgerReview(ctx, input, true)
}

func (s *Store) appendPRDevelopmentLedgerReview(
	ctx context.Context,
	input PRDevelopmentLedgerReviewAppend,
	enqueueChanges bool,
) (PRDevelopmentReviewCompletion, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentReviewCompletion{}, false, err
	}
	normalized, err := normalizePRDevelopmentLedgerReviewAppend(input)
	if err != nil {
		return PRDevelopmentReviewCompletion{}, false, err
	}
	var (
		completion PRDevelopmentReviewCompletion
		changed    bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		thread, binding, controller, fence, ledger, loadErr := loadPRDevelopmentLedgerAppendState(
			ctx,
			conn,
			normalized.CaseID,
			normalized.AttemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		if enqueueChanges {
			orchestration, found, orchestrationErr := loadPRDevelopmentRepairOrchestration(
				ctx,
				conn,
				normalized.AttemptID,
			)
			if orchestrationErr != nil {
				return orchestrationErr
			}
			if !found || orchestration.Phase != PRDevelopmentRepairOrchestrationCompleted ||
				orchestration.ControllerID != controller.ID ||
				orchestration.SessionID != controller.OwnerSessionID ||
				orchestration.CaseID != normalized.CaseID ||
				orchestration.ThreadID != controller.ThreadID ||
				orchestration.Validation == nil {
				return fmt.Errorf(
					"%w: review completion has no exact completed orchestration receipt",
					ErrPRDevelopmentLedgerConflict,
				)
			}
			if normalized.Outcome == PRDevelopmentLedgerReviewPassed &&
				orchestration.Validation.CIStatus != PRDevelopmentCIPassed {
				return fmt.Errorf(
					"%w: passed review requires a passed local-CI receipt",
					ErrPRDevelopmentLedgerConflict,
				)
			}
		}
		expectedOrdinal := fence.Ordinal*2 + 1
		candidate := PRDevelopmentLedgerEntry{
			ThreadID:      controller.ThreadID,
			Ordinal:       expectedOrdinal,
			Kind:          PRDevelopmentLedgerReview,
			AttemptID:     fence.AttemptID,
			FenceOrdinal:  fence.Ordinal,
			CaseID:        binding.Case.CaseID,
			CaseOrdinal:   binding.Case.Ordinal,
			Summary:       normalized.Summary,
			ReviewOutcome: normalized.Outcome,
			Findings:      clonePRDevelopmentLedgerFindings(normalized.Findings),
		}
		if len(ledger.Entries) == 0 {
			return fmt.Errorf(
				"%w: review account has no preceding attempt account",
				ErrPRDevelopmentLedgerConflict,
			)
		}
		baseOrdinal := ledger.Entries[0].Ordinal
		entryIndex := expectedOrdinal - baseOrdinal
		if entryIndex < 0 {
			return fmt.Errorf(
				"%w: review predates the anchored ledger",
				ErrPRDevelopmentLedgerConflict,
			)
		}
		if entryIndex < len(ledger.Entries) {
			transition := PRDevelopmentControllerReviewTransition{
				ControllerID:     normalized.ControllerID,
				AttemptID:        normalized.AttemptID,
				ExpectedRevision: normalized.ExpectedRevision,
				LeaseToken:       normalized.LeaseToken,
				LeaseEpoch:       normalized.LeaseEpoch,
			}
			if fence.ReviewedAt == nil ||
				!equalPRDevelopmentFinishedReviewReplay(fence, transition) {
				return fmt.Errorf(
					"%w: review replay does not match its completing lease",
					ErrPRDevelopmentLedgerConflict,
				)
			}
			candidate.FenceHash = fence.FenceHash
			existing := ledger.Entries[entryIndex]
			if equalPRDevelopmentLedgerEntryIntent(existing, candidate) {
				completion.Entry = existing
				completion.Controller = redactPRDevelopmentControllerAuthority(controller)
				if enqueueChanges {
					next, retryErr := loadExactPRDevelopmentReviewRetry(
						ctx,
						conn,
						controller.OwnerSessionID,
						fence,
						normalized.Outcome,
					)
					if retryErr != nil {
						return retryErr
					}
					completion.NextAttempt = next
				}
				return nil
			}
			return fmt.Errorf(
				"%w: changed review-account replay",
				ErrPRDevelopmentLedgerConflict,
			)
		}
		if fence.ReviewedAt != nil {
			return fmt.Errorf(
				"%w: reviewed fence has no atomic review account",
				ErrPRDevelopmentLedgerConflict,
			)
		}
		if expectedOrdinal != baseOrdinal+len(ledger.Entries) {
			return fmt.Errorf(
				"%w: review account must immediately follow its attempt account",
				ErrPRDevelopmentLedgerConflict,
			)
		}
		if len(ledger.Entries) >= MaxPRDevelopmentLedgerEntries {
			return ErrPRDevelopmentLedgerCapacity
		}
		if normalized.ControllerID != controller.ID {
			return fmt.Errorf(
				"%w: review lease belongs to another controller",
				ErrPRDevelopmentLedgerConflict,
			)
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if timeErr := requireNonRegressingPRDevelopmentControllerTime(
			now,
			controller.UpdatedAt,
		); timeErr != nil {
			return timeErr
		}
		if leaseErr := requireLivePRDevelopmentControllerLease(
			controller,
			normalized.AttemptID,
			normalized.LeaseToken,
			normalized.LeaseEpoch,
			PRDevelopmentControllerReview,
			now,
		); leaseErr != nil {
			return leaseErr
		}
		if controller.Revision != normalized.ExpectedRevision {
			return fmt.Errorf(
				"%w: expected revision %d, current revision %d",
				ErrPRDevelopmentControllerConflict,
				normalized.ExpectedRevision,
				controller.Revision,
			)
		}
		if controller.Revision > MaxPRDevelopmentControllerRevision-1 {
			return fmt.Errorf(
				"%w: controller revision capacity exhausted",
				ErrPRDevelopmentControllerConflict,
			)
		}
		highWater := fence.CreatedAt
		if len(ledger.Entries) != 0 && ledger.Entries[len(ledger.Entries)-1].CreatedAt.After(highWater) {
			highWater = ledger.Entries[len(ledger.Entries)-1].CreatedAt
		}
		if now.Before(highWater) {
			return fmt.Errorf(
				"%w: store clock regressed behind ledger high-water time",
				ErrInvalidPRDevelopmentLedger,
			)
		}
		var retryPlan *prDevelopmentReviewRetryPlan
		if enqueueChanges {
			if normalized.Outcome == PRDevelopmentLedgerReviewChangesRequired {
				planned, planErr := preparePRDevelopmentReviewRetry(
					ctx,
					conn,
					controller,
					fence,
				)
				if planErr != nil {
					return planErr
				}
				retryPlan = &planned
			} else if retryErr := requireNoPRDevelopmentReviewRetry(
				ctx,
				conn,
				controller.OwnerSessionID,
				fence.AttemptID,
			); retryErr != nil {
				return retryErr
			}
		}
		candidate.ID, err = newPrefixedID(prDevelopmentLedgerEntryIDPrefix)
		if err != nil {
			return err
		}
		reviewedAt := now
		fence.ReviewedAt = &reviewedAt
		fence.ReviewLeaseEpoch = controller.LeaseEpoch
		fence.ReviewLeaseTokenDigest = prDevelopmentLeaseTokenDigest(
			PRDevelopmentControllerReviewLease,
			controller.LeaseToken,
		)
		fence.ReviewControllerRevision = controller.Revision
		fence.FenceHash = hashPRDevelopmentReviewFence(fence)
		fenceResult, reviewErr := conn.ExecContext(ctx, `
			UPDATE pr_development_attempt_review_fences
			SET reviewed_at = ?, review_lease_epoch = ?,
				review_lease_token_digest = ?, review_controller_revision = ?,
				fence_hash = ?
			WHERE attempt_id = ? AND controller_id = ? AND reviewed_at IS NULL`,
			toDBTime(now),
			fence.ReviewLeaseEpoch,
			fence.ReviewLeaseTokenDigest,
			fence.ReviewControllerRevision,
			fence.FenceHash,
			fence.AttemptID,
			controller.ID,
		)
		if reviewErr != nil {
			return reviewErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(fenceResult); rowErr != nil {
			return rowErr
		}
		controllerResult, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_thread_controllers
			SET revision = revision + 1, phase = 'ready', lease_kind = '',
				lease_owner = '', lease_token = '', lease_until = NULL,
				fences_digest = ?, updated_at = ?
			WHERE id = ? AND revision = ? AND phase = 'review' AND
				current_attempt_id = ? AND lease_token = ? AND lease_epoch = ?`,
			fence.FenceHash,
			toDBTime(now),
			controller.ID,
			controller.Revision,
			controller.CurrentAttemptID,
			controller.LeaseToken,
			controller.LeaseEpoch,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(controllerResult); rowErr != nil {
			return rowErr
		}
		candidate.FenceHash = fence.FenceHash
		candidate.PreviousHash = ledger.EntriesDigest
		candidate.CreatedAt = now
		candidate.EntryHash = hashPRDevelopmentLedgerEntry(candidate)
		if insertErr := insertPRDevelopmentLedgerEntry(ctx, conn, candidate); insertErr != nil {
			return insertErr
		}
		if retryPlan != nil {
			if appendErr := appendPRDevelopmentRepairAttempt(
				ctx,
				conn,
				&retryPlan.Session,
				retryPlan.Admission,
				now,
			); appendErr != nil {
				return appendErr
			}
			nextSession, loadSessionErr := loadPRDevelopmentRepairSessionByID(
				ctx,
				conn,
				retryPlan.Session.ID,
			)
			if loadSessionErr != nil {
				return loadSessionErr
			}
			next, found := findPRDevelopmentRepairIdempotency(
				&nextSession,
				retryPlan.Admission.IdempotencyKey,
			)
			if !found {
				return errors.New("admitted review repair retry disappeared")
			}
			cloned := next
			completion.NextAttempt = &cloned
		}
		ledger.Entries = append(ledger.Entries, candidate)
		ledger.EntriesDigest = candidate.EntryHash
		controller, found, loadErr := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			controller.ID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return errors.New("reviewed pull request development controller disappeared")
		}
		if validateErr := validatePRDevelopmentLedgerAggregate(
			ctx,
			conn,
			thread,
			controller,
			ledger,
		); validateErr != nil {
			return validateErr
		}
		if enqueueChanges &&
			normalized.Outcome == PRDevelopmentLedgerReviewAttentionRequired {
			conversation, conversationErr := loadPRDevelopmentConversation(
				ctx,
				conn,
				normalized.CaseID,
			)
			if conversationErr != nil {
				return conversationErr
			}
			if enqueueErr := enqueuePRDevelopmentAttentionTrigger(
				ctx,
				conn,
				candidate,
				conversation,
				now,
			); enqueueErr != nil {
				return enqueueErr
			}
		}
		completion.Entry = candidate
		completion.Controller = redactPRDevelopmentControllerAuthority(controller)
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentReviewCompletion{}, false, fmt.Errorf(
			"append pull request development review ledger entry: %w",
			s.dbError(err),
		)
	}
	return completion, changed, nil
}

func redactPRDevelopmentControllerAuthority(
	controller PRDevelopmentController,
) PRDevelopmentController {
	controller.LeaseToken = ""
	controller.MutationReservationKey = ""
	return controller
}

type prDevelopmentReviewRetryPlan struct {
	Session   PRDevelopmentRepairSession
	Admission PRDevelopmentRepairAdmit
}

func preparePRDevelopmentReviewRetry(
	ctx context.Context,
	queryer rowsQueryer,
	controller PRDevelopmentController,
	fence PRDevelopmentAttemptReviewFence,
) (prDevelopmentReviewRetryPlan, error) {
	session, err := loadPRDevelopmentRepairSessionByID(
		ctx,
		queryer,
		controller.OwnerSessionID,
	)
	if err != nil {
		return prDevelopmentReviewRetryPlan{}, err
	}
	if session.CaseID == "" || len(session.Attempts) == 0 ||
		session.Attempts[len(session.Attempts)-1].ID != fence.AttemptID ||
		session.Attempts[len(session.Attempts)-1].Status != PRDevelopmentRepairCompleted ||
		activePRDevelopmentRepairAttempt(&session) != nil {
		return prDevelopmentReviewRetryPlan{}, fmt.Errorf(
			"%w: reviewed attempt is no longer the terminal session tail",
			ErrPRDevelopmentLedgerConflict,
		)
	}
	if len(session.Attempts) >= MaxPRDevelopmentRepairAttempts ||
		session.Version > maxPRDevelopmentRepairVersionBeforeAdmission(
			session.HeadRepository != "",
		) || controller.LineVersion >= MaxPRDevelopmentControllerFences ||
		controller.Revision+1 > MaxPRDevelopmentControllerRevision-
			prDevelopmentControllerMutationRevisionReserve {
		return prDevelopmentReviewRetryPlan{}, fmt.Errorf(
			"%w: changes-required review has no repair-attempt headroom",
			ErrPRDevelopmentRepairCapacity,
		)
	}
	idempotencyKey := normalizePRDevelopmentReviewRetryIdentity(fence.AttemptID)
	if _, found := findPRDevelopmentRepairIdempotency(&session, idempotencyKey); found {
		return prDevelopmentReviewRetryPlan{}, fmt.Errorf(
			"%w: review retry already exists before atomic completion",
			ErrPRDevelopmentLedgerConflict,
		)
	}
	conversation, err := loadPRDevelopmentConversation(ctx, queryer, session.CaseID)
	if err != nil {
		return prDevelopmentReviewRetryPlan{}, err
	}
	return prDevelopmentReviewRetryPlan{
		Session: session,
		Admission: PRDevelopmentRepairAdmit{
			CaseID:                      session.CaseID,
			ExpectedConversationVersion: conversation.Conversation.Version,
			ExpectedRepairVersion:       session.Version,
			IdempotencyKey:              idempotencyKey,
			AgentID:                     session.AgentID,
			Instruction:                 prDevelopmentReviewRetryInstruction,
		},
	}, nil
}

func loadExactPRDevelopmentReviewRetry(
	ctx context.Context,
	queryer rowsQueryer,
	sessionID string,
	fence PRDevelopmentAttemptReviewFence,
	outcome PRDevelopmentLedgerReviewOutcome,
) (*PRDevelopmentRepairAttempt, error) {
	if outcome != PRDevelopmentLedgerReviewChangesRequired {
		if err := requireNoPRDevelopmentReviewRetry(
			ctx,
			queryer,
			sessionID,
			fence.AttemptID,
		); err != nil {
			return nil, err
		}
		return nil, nil
	}
	session, err := loadPRDevelopmentRepairSessionByID(ctx, queryer, sessionID)
	if err != nil {
		return nil, err
	}
	idempotencyKey := normalizePRDevelopmentReviewRetryIdentity(fence.AttemptID)
	next, found := findPRDevelopmentRepairIdempotency(&session, idempotencyKey)
	if !found {
		return nil, fmt.Errorf(
			"%w: changes-required review has no atomic repair retry",
			ErrPRDevelopmentLedgerConflict,
		)
	}
	reviewed := findPRDevelopmentRepairAttempt(&session, fence.AttemptID)
	if reviewed == nil || next.Ordinal != reviewed.Ordinal+1 ||
		next.Instruction != prDevelopmentReviewRetryInstruction ||
		next.ConversationVersion < reviewed.ConversationVersion {
		return nil, fmt.Errorf(
			"%w: changes-required review retry differs from its deterministic binding",
			ErrPRDevelopmentLedgerConflict,
		)
	}
	cloned := next
	return &cloned, nil
}

func requireNoPRDevelopmentReviewRetry(
	ctx context.Context,
	queryer rowQueryer,
	sessionID, attemptID string,
) error {
	var retries int
	if err := queryer.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM pr_development_repair_attempts
		WHERE session_id = ? AND idempotency_key = ?`,
		sessionID,
		normalizePRDevelopmentReviewRetryIdentity(attemptID),
	).Scan(&retries); err != nil {
		return err
	}
	if retries != 0 {
		return fmt.Errorf(
			"%w: non-retry review outcome is bound to a repair retry",
			ErrPRDevelopmentLedgerConflict,
		)
	}
	return nil
}

// AppendPRDevelopmentLedgerCheckpoint logically compacts an exact reviewed
// prefix. It never updates or deletes source entries or earlier checkpoints.
func (s *Store) AppendPRDevelopmentLedgerCheckpoint(
	ctx context.Context,
	input PRDevelopmentLedgerCheckpointAppend,
) (PRDevelopmentLedgerCheckpoint, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentLedgerCheckpoint{}, false, err
	}
	normalized, err := normalizePRDevelopmentLedgerCheckpointAppend(input)
	if err != nil {
		return PRDevelopmentLedgerCheckpoint{}, false, err
	}
	var (
		checkpoint PRDevelopmentLedgerCheckpoint
		changed    bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		thread, loadErr := loadPRDevelopmentThreadForCase(ctx, conn, normalized.CaseID)
		if loadErr != nil {
			return loadErr
		}
		if thread.Kind != PRDevelopmentThreadProvider {
			return fmt.Errorf(
				"%w: legacy threads cannot own a development ledger",
				ErrPRDevelopmentLedgerConflict,
			)
		}
		ledger, loadErr := loadPRDevelopmentLedgerAggregate(ctx, conn, thread)
		if loadErr != nil {
			return loadErr
		}
		if len(ledger.Entries) == 0 {
			return fmt.Errorf(
				"%w: checkpoint source is not an exact reviewed prefix",
				ErrPRDevelopmentLedgerConflict,
			)
		}
		entryIndex := normalized.ThroughOrdinal - ledger.Entries[0].Ordinal
		if entryIndex < 0 || entryIndex >= len(ledger.Entries) ||
			ledger.Entries[entryIndex].Kind != PRDevelopmentLedgerReview ||
			ledger.Entries[entryIndex].EntryHash != normalized.SourceDigest {
			return fmt.Errorf(
				"%w: checkpoint source is not an exact reviewed prefix",
				ErrPRDevelopmentLedgerConflict,
			)
		}
		for _, existing := range ledger.Checkpoints {
			if existing.ThroughOrdinal == normalized.ThroughOrdinal {
				candidate := PRDevelopmentLedgerCheckpoint{
					ThreadID:       thread.ID,
					ThroughOrdinal: normalized.ThroughOrdinal,
					SourceDigest:   normalized.SourceDigest,
					Summary:        normalized.Summary,
					CompactorID:    normalized.CompactorID,
					PromptDigest:   normalized.PromptDigest,
				}
				if equalPRDevelopmentLedgerCheckpointIntent(existing, candidate) {
					checkpoint = existing
					return nil
				}
				return fmt.Errorf(
					"%w: changed checkpoint replay",
					ErrPRDevelopmentLedgerConflict,
				)
			}
		}
		generation := len(ledger.Checkpoints)
		if generation >= MaxPRDevelopmentControllerFences {
			return ErrPRDevelopmentLedgerCapacity
		}
		if ledger.LatestCheckpoint != nil &&
			normalized.ThroughOrdinal <= ledger.LatestCheckpoint.ThroughOrdinal {
			return fmt.Errorf(
				"%w: checkpoint does not advance the compacted prefix",
				ErrPRDevelopmentLedgerConflict,
			)
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		highWater := ledger.Entries[entryIndex].CreatedAt
		if ledger.LatestCheckpoint != nil && ledger.LatestCheckpoint.CreatedAt.After(highWater) {
			highWater = ledger.LatestCheckpoint.CreatedAt
		}
		if now.Before(highWater) {
			return fmt.Errorf(
				"%w: store clock regressed behind checkpoint high-water time",
				ErrInvalidPRDevelopmentLedger,
			)
		}
		checkpoint = PRDevelopmentLedgerCheckpoint{
			ThreadID:       thread.ID,
			Generation:     generation,
			ThroughOrdinal: normalized.ThroughOrdinal,
			SourceDigest:   normalized.SourceDigest,
			Summary:        normalized.Summary,
			CompactorID:    normalized.CompactorID,
			PromptDigest:   normalized.PromptDigest,
			PreviousHash:   ledger.CheckpointsDigest,
			CreatedAt:      now,
		}
		checkpoint.ID, err = newPrefixedID(prDevelopmentLedgerCheckpointIDPrefix)
		if err != nil {
			return err
		}
		checkpoint.CheckpointHash = hashPRDevelopmentLedgerCheckpoint(checkpoint)
		_, insertErr := conn.ExecContext(ctx, `
			INSERT INTO pr_development_ledger_checkpoints (
				id, thread_id, generation, through_ordinal, source_digest, summary,
				compactor_id, prompt_digest, previous_hash, checkpoint_hash, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			checkpoint.ID,
			checkpoint.ThreadID,
			checkpoint.Generation,
			checkpoint.ThroughOrdinal,
			checkpoint.SourceDigest,
			checkpoint.Summary,
			checkpoint.CompactorID,
			checkpoint.PromptDigest,
			checkpoint.PreviousHash,
			checkpoint.CheckpointHash,
			toDBTime(checkpoint.CreatedAt),
		)
		if insertErr != nil {
			return insertErr
		}
		loaded, loadErr := loadPRDevelopmentLedgerAggregate(ctx, conn, thread)
		if loadErr != nil {
			return loadErr
		}
		if loaded.LatestCheckpoint == nil ||
			loaded.LatestCheckpoint.CheckpointHash != checkpoint.CheckpointHash {
			return errors.New("inserted development ledger checkpoint disappeared")
		}
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentLedgerCheckpoint{}, false, fmt.Errorf(
			"append pull request development ledger checkpoint: %w",
			s.dbError(err),
		)
	}
	return checkpoint, changed, nil
}

func loadPRDevelopmentLedgerAppendState(
	ctx context.Context,
	queryer rowsQueryer,
	caseID, attemptID string,
) (
	PRDevelopmentThread,
	PRDevelopmentThreadBinding,
	PRDevelopmentController,
	PRDevelopmentAttemptReviewFence,
	PRDevelopmentLedger,
	error,
) {
	binding, err := loadPRDevelopmentThreadBindingForCase(ctx, queryer, caseID)
	if err != nil {
		return PRDevelopmentThread{}, PRDevelopmentThreadBinding{},
			PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{},
			PRDevelopmentLedger{}, err
	}
	if binding.Kind != PRDevelopmentThreadProvider {
		return PRDevelopmentThread{}, PRDevelopmentThreadBinding{},
			PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{},
			PRDevelopmentLedger{}, fmt.Errorf(
				"%w: legacy threads cannot own a development ledger",
				ErrPRDevelopmentLedgerConflict,
			)
	}
	thread, err := loadPRDevelopmentThread(ctx, queryer, binding.ID)
	if err != nil {
		return PRDevelopmentThread{}, PRDevelopmentThreadBinding{},
			PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{},
			PRDevelopmentLedger{}, err
	}
	controller, found, err := loadPRDevelopmentControllerAggregate(
		ctx,
		queryer,
		thread.ID,
	)
	if err != nil || !found {
		if err == nil {
			err = fmt.Errorf(
				"%w: thread has no development controller",
				ErrPRDevelopmentLedgerConflict,
			)
		}
		return PRDevelopmentThread{}, PRDevelopmentThreadBinding{},
			PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{},
			PRDevelopmentLedger{}, err
	}
	owner, err := loadPRDevelopmentRepairSessionByID(ctx, queryer, controller.OwnerSessionID)
	if err != nil {
		return PRDevelopmentThread{}, PRDevelopmentThreadBinding{},
			PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{},
			PRDevelopmentLedger{}, err
	}
	if owner.CaseID != caseID {
		return PRDevelopmentThread{}, PRDevelopmentThreadBinding{},
			PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{},
			PRDevelopmentLedger{}, fmt.Errorf(
				"%w: selected case does not own the controller session",
				ErrPRDevelopmentLedgerConflict,
			)
	}
	fence, found, err := loadPRDevelopmentReviewFenceByAttempt(ctx, queryer, attemptID)
	if err != nil || !found {
		if err == nil {
			err = fmt.Errorf(
				"%w: attempt has no parked review fence",
				ErrPRDevelopmentLedgerConflict,
			)
		}
		return PRDevelopmentThread{}, PRDevelopmentThreadBinding{},
			PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{},
			PRDevelopmentLedger{}, err
	}
	if fence.ControllerID != controller.ID || fence.ThreadID != thread.ID {
		return PRDevelopmentThread{}, PRDevelopmentThreadBinding{},
			PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{},
			PRDevelopmentLedger{}, fmt.Errorf(
				"%w: attempt fence belongs to another controller",
				ErrPRDevelopmentLedgerConflict,
			)
	}
	ledger, err := loadPRDevelopmentLedgerAggregate(ctx, queryer, thread)
	return thread, binding, controller, fence, ledger, err
}

func insertPRDevelopmentLedgerEntry(
	ctx context.Context,
	conn *sql.Conn,
	entry PRDevelopmentLedgerEntry,
) error {
	var noChanges any
	if entry.Kind == PRDevelopmentLedgerAttempt {
		noChanges = boolDBValue(entry.NoChanges)
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO pr_development_ledger_entries (
			id, thread_id, ordinal, kind, attempt_id, fence_ordinal, case_id,
			case_ordinal, commit_oid, tree_oid, no_changes, summary,
			ci_plan_digest, ci_result_digest, review_outcome, finding_count,
			fence_hash, previous_hash, entry_hash, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID,
		entry.ThreadID,
		entry.Ordinal,
		entry.Kind,
		entry.AttemptID,
		entry.FenceOrdinal,
		entry.CaseID,
		entry.CaseOrdinal,
		entry.Commit,
		entry.Tree,
		noChanges,
		entry.Summary,
		entry.CIPlanDigest,
		entry.CIResultDigest,
		entry.ReviewOutcome,
		len(entry.Findings),
		entry.FenceHash,
		entry.PreviousHash,
		entry.EntryHash,
		toDBTime(entry.CreatedAt),
	)
	if err != nil {
		return err
	}
	for ordinal, finding := range entry.Findings {
		var line any
		if finding.Line != nil {
			line = *finding.Line
		}
		if _, err = conn.ExecContext(ctx, `
			INSERT INTO pr_development_ledger_review_findings (
				entry_id, entry_kind, ordinal, severity, title, file, line,
				message, evidence, impact, recommendation, validation
			) VALUES (?, 'review', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			entry.ID,
			ordinal,
			finding.Severity,
			finding.Title,
			finding.File,
			line,
			finding.Message,
			finding.Evidence,
			finding.Impact,
			finding.Recommendation,
			finding.Validation,
		); err != nil {
			return err
		}
	}
	return nil
}

type storedPRDevelopmentLedgerEntry struct {
	entry        PRDevelopmentLedgerEntry
	findingCount int
}

func loadPRDevelopmentLedgerAggregate(
	ctx context.Context,
	queryer rowsQueryer,
	thread PRDevelopmentThread,
) (PRDevelopmentLedger, error) {
	entries, err := loadPRDevelopmentLedgerEntries(ctx, queryer, thread.ID)
	if err != nil {
		return PRDevelopmentLedger{}, err
	}
	checkpoints, err := loadPRDevelopmentLedgerCheckpoints(ctx, queryer, thread.ID)
	if err != nil {
		return PRDevelopmentLedger{}, err
	}
	ledger := PRDevelopmentLedger{
		ThreadID:          thread.ID,
		Entries:           entries,
		Checkpoints:       checkpoints,
		EntriesDigest:     emptyPRDevelopmentLedgerEntriesDigest(),
		CheckpointsDigest: emptyPRDevelopmentLedgerCheckpointsDigest(),
	}
	if len(entries) != 0 {
		ledger.EntriesDigest = entries[len(entries)-1].EntryHash
	}
	if len(checkpoints) != 0 {
		ledger.CheckpointsDigest = checkpoints[len(checkpoints)-1].CheckpointHash
		ledger.LatestCheckpoint = &ledger.Checkpoints[len(ledger.Checkpoints)-1]
	}
	if len(entries) == 0 && len(checkpoints) == 0 {
		return ledger, nil
	}
	controller, found, err := loadPRDevelopmentControllerAggregate(
		ctx,
		queryer,
		thread.ID,
	)
	if err != nil {
		return PRDevelopmentLedger{}, err
	}
	if !found {
		return PRDevelopmentLedger{}, wrapInvalidStoredPRDevelopmentLedger(
			errors.New("stored ledger has no development controller"),
		)
	}
	if err := validatePRDevelopmentLedgerAggregate(
		ctx,
		queryer,
		thread,
		controller,
		ledger,
	); err != nil {
		return PRDevelopmentLedger{}, err
	}
	return ledger, nil
}

// loadPRDevelopmentWorkbenchLocalEvidence keeps the selected-case read on one
// SQLite snapshot while preserving complete ledger integrity validation. It
// validates the controller once and correlates every retained orchestration
// receipt in one bounded batch instead of reloading the controller and running
// one orchestration aggregate query graph per historical attempt.
func loadPRDevelopmentWorkbenchLocalEvidence(
	ctx context.Context,
	queryer rowsQueryer,
	binding PRDevelopmentThreadBinding,
	session PRDevelopmentRepairSession,
) (PRDevelopmentLocalEvidenceSnapshot, error) {
	thread, err := loadPRDevelopmentThread(ctx, queryer, binding.ID)
	if err != nil {
		return PRDevelopmentLocalEvidenceSnapshot{}, err
	}
	if binding.Kind != PRDevelopmentThreadProvider || thread.Kind != binding.Kind {
		return PRDevelopmentLocalEvidenceSnapshot{}, wrapInvalidStoredPRDevelopmentLedger(
			errors.New("stored local evidence thread is invalid"),
		)
	}
	controller, controlled, err := loadPRDevelopmentControllerAggregate(
		ctx,
		queryer,
		thread.ID,
	)
	if err != nil {
		return PRDevelopmentLocalEvidenceSnapshot{}, err
	}
	entries, findingCounts, err := loadPRDevelopmentLedgerEntriesBase(
		ctx,
		queryer,
		thread.ID,
	)
	if err != nil {
		return PRDevelopmentLocalEvidenceSnapshot{}, err
	}
	for index := range entries {
		if entries[index].Kind == PRDevelopmentLedgerAttempt {
			// Pre-v14 attempt accounts did not persist CI status. Preserve the
			// private compatibility value while keeping ciStatusBound false.
			entries[index].CIStatus = PRDevelopmentCIPassed
		}
	}
	checkpoints, err := loadPRDevelopmentLedgerCheckpoints(ctx, queryer, thread.ID)
	if err != nil {
		return PRDevelopmentLocalEvidenceSnapshot{}, err
	}

	selectedLatestCompletedAttemptID := ""
	if len(session.Attempts) != 0 {
		latest := session.Attempts[len(session.Attempts)-1]
		if latest.Status == PRDevelopmentRepairCompleted {
			selectedLatestCompletedAttemptID = latest.ID
		}
	}
	owner := session
	selectedOwnsController := controlled && controller.OwnerSessionID == session.ID
	if controlled && !selectedOwnsController {
		owner, err = loadPRDevelopmentRepairSessionByID(
			ctx,
			queryer,
			controller.OwnerSessionID,
		)
		if err != nil {
			return PRDevelopmentLocalEvidenceSnapshot{}, err
		}
	}
	latestEvidenceAttemptID := ""
	if selectedOwnsController {
		latestEvidenceAttemptID = selectedLatestCompletedAttemptID
	} else if selectedLatestCompletedAttemptID != "" {
		if !controlled {
			_, orchestrated, loadErr := loadPRDevelopmentRepairOrchestration(
				ctx,
				queryer,
				selectedLatestCompletedAttemptID,
			)
			if loadErr != nil {
				return PRDevelopmentLocalEvidenceSnapshot{}, loadErr
			}
			if orchestrated {
				return PRDevelopmentLocalEvidenceSnapshot{}, wrapInvalidStoredPRDevelopmentLedger(
					errors.New("completed local evidence orchestration has no controller"),
				)
			}
		} else {
			var orchestrated int
			if queryErr := queryer.QueryRowContext(ctx, `
				SELECT EXISTS (
					SELECT 1
					FROM pr_development_repair_orchestrations
					WHERE attempt_id = ?
				)`, selectedLatestCompletedAttemptID).Scan(&orchestrated); queryErr != nil {
				return PRDevelopmentLocalEvidenceSnapshot{}, queryErr
			}
			if orchestrated != 0 {
				return PRDevelopmentLocalEvidenceSnapshot{}, wrapInvalidStoredPRDevelopmentLedger(
					errors.New("completed local evidence orchestration has a foreign controller owner"),
				)
			}
		}
	}
	orchestrations := make(map[string]PRDevelopmentRepairOrchestration)
	var fences []PRDevelopmentAttemptReviewFence
	if controlled {
		fences, err = loadPRDevelopmentReviewFences(ctx, queryer, controller.ID)
		if err != nil {
			return PRDevelopmentLocalEvidenceSnapshot{}, err
		}
		orchestrations, err = bindPRDevelopmentWorkbenchLedgerOrchestrations(
			ctx,
			queryer,
			thread,
			controller,
			owner,
			fences,
			entries,
			findingCounts,
		)
		if err != nil {
			return PRDevelopmentLocalEvidenceSnapshot{}, wrapInvalidStoredPRDevelopmentLedger(err)
		}
		if latestEvidenceAttemptID != "" {
			if _, found := orchestrations[latestEvidenceAttemptID]; !found {
				_, orchestrated, loadErr := loadPRDevelopmentRepairOrchestration(
					ctx,
					queryer,
					latestEvidenceAttemptID,
				)
				if loadErr != nil {
					return PRDevelopmentLocalEvidenceSnapshot{}, loadErr
				}
				if orchestrated {
					return PRDevelopmentLocalEvidenceSnapshot{}, wrapInvalidStoredPRDevelopmentLedger(
						errors.New("completed local evidence orchestration is outside its ledger"),
					)
				}
			}
		}
	}
	if err := validateLoadedPRDevelopmentLedgerEntries(entries, findingCounts); err != nil {
		return PRDevelopmentLocalEvidenceSnapshot{}, err
	}
	ledger := PRDevelopmentLedger{
		ThreadID:          thread.ID,
		Entries:           entries,
		Checkpoints:       checkpoints,
		EntriesDigest:     emptyPRDevelopmentLedgerEntriesDigest(),
		CheckpointsDigest: emptyPRDevelopmentLedgerCheckpointsDigest(),
	}
	if len(entries) != 0 {
		ledger.EntriesDigest = entries[len(entries)-1].EntryHash
	}
	if len(checkpoints) != 0 {
		ledger.CheckpointsDigest = checkpoints[len(checkpoints)-1].CheckpointHash
		ledger.LatestCheckpoint = &ledger.Checkpoints[len(ledger.Checkpoints)-1]
	}
	if len(entries) != 0 || len(checkpoints) != 0 {
		if !controlled {
			return PRDevelopmentLocalEvidenceSnapshot{}, wrapInvalidStoredPRDevelopmentLedger(
				errors.New("stored ledger has no development controller"),
			)
		}
		if err := validatePRDevelopmentLedgerAggregateSnapshot(
			thread,
			controller,
			owner,
			fences,
			ledger,
		); err != nil {
			return PRDevelopmentLocalEvidenceSnapshot{}, err
		}
	}
	snapshot := PRDevelopmentLocalEvidenceSnapshot{Ledger: ledger}
	if controlled {
		snapshot.Controller = &controller
	}
	if latestEvidenceAttemptID != "" {
		if orchestration, found := orchestrations[latestEvidenceAttemptID]; found {
			snapshot.Orchestration = &orchestration
		}
	}
	return snapshot, nil
}

func bindPRDevelopmentWorkbenchLedgerOrchestrations(
	ctx context.Context,
	queryer rowsQueryer,
	thread PRDevelopmentThread,
	controller PRDevelopmentController,
	session PRDevelopmentRepairSession,
	fences []PRDevelopmentAttemptReviewFence,
	entries []PRDevelopmentLedgerEntry,
	findingCounts []int,
) (map[string]PRDevelopmentRepairOrchestration, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT `+prDevelopmentRepairOrchestrationColumns+`
		FROM pr_development_ledger_entries AS entry
		JOIN pr_development_repair_orchestrations AS orchestration
			ON orchestration.attempt_id = entry.attempt_id
		JOIN pr_development_repair_attempts AS attempt
			ON attempt.id = orchestration.attempt_id
		JOIN pr_development_repair_sessions AS session
			ON session.id = orchestration.session_id
		WHERE entry.thread_id = ? AND entry.kind = 'attempt'
		ORDER BY entry.ordinal`,
		thread.ID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	orchestrations := make(map[string]PRDevelopmentRepairOrchestration)
	ordered := make([]PRDevelopmentRepairOrchestration, 0)
	for rows.Next() {
		orchestration, scanErr := scanPRDevelopmentRepairOrchestration(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if validationErr := validateStoredPRDevelopmentRepairOrchestration(
			orchestration,
		); validationErr != nil {
			return nil, fmt.Errorf(
				"invalid stored pull request development repair orchestration: %w",
				validationErr,
			)
		}
		if _, duplicated := orchestrations[orchestration.AttemptID]; duplicated {
			return nil, errors.New("stored repair orchestration attempt is duplicated")
		}
		orchestrations[orchestration.AttemptID] = orchestration
		ordered = append(ordered, orchestration)
		if len(ordered) > MaxPRDevelopmentRepairAttempts {
			return nil, errors.New("stored repair orchestration batch is too large")
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	if len(ordered) == 0 {
		return orchestrations, nil
	}
	operations, err := loadPRDevelopmentControllerOperations(ctx, queryer, controller.ID)
	if err != nil {
		return nil, err
	}
	operationsByID := make(map[string]PRDevelopmentControllerOperation, len(operations))
	for _, operation := range operations {
		operationsByID[operation.ID] = operation
	}
	fencesByAttempt := make(map[string]PRDevelopmentAttemptReviewFence, len(fences))
	for _, fence := range fences {
		fencesByAttempt[fence.AttemptID] = fence
	}
	entryIndexes := make(map[string]int, len(entries)/2+1)
	for index := range entries {
		if entries[index].Kind == PRDevelopmentLedgerAttempt {
			entryIndexes[entries[index].AttemptID] = index
		}
	}
	var suppressed int
	if err := queryer.QueryRowContext(ctx, `
		SELECT claim_suppressed
		FROM pr_development_repair_sessions
		WHERE id = ?`, session.ID).Scan(&suppressed); err != nil {
		return nil, err
	}
	for _, orchestration := range ordered {
		attempt := findPRDevelopmentRepairAttempt(&session, orchestration.AttemptID)
		if attempt == nil || session.ID != orchestration.SessionID ||
			session.CaseID != orchestration.CaseID || session.AgentID != orchestration.AgentID ||
			attempt.Instruction != orchestration.Instruction ||
			thread.Kind != PRDevelopmentThreadProvider || thread.ID != orchestration.ThreadID ||
			controller.OwnerSessionID != session.ID || controller.ThreadID != thread.ID {
			return nil, errors.New(
				"orchestration attempt/session/case/thread ownership is invalid",
			)
		}
		if orchestration.HeadRepository != "" &&
			(session.HeadRepository != orchestration.HeadRepository ||
				session.HeadRef != orchestration.HeadRef ||
				session.HeadSHA != orchestration.HeadSHA ||
				session.CloneURL != orchestration.CloneURL ||
				session.ReviewDigest != orchestration.ReviewDigest ||
				session.WorkspaceID != orchestration.WorkspaceID) {
			return nil, errors.New("orchestration baseline differs from its owner session")
		}
		if orchestration.Phase != PRDevelopmentRepairOrchestrationCompleted ||
			attempt.Status != PRDevelopmentRepairCompleted || attempt.Claims < 1 ||
			attempt.Summary != orchestration.Summary ||
			attempt.Iterations != orchestration.Iterations || suppressed != 1 {
			return nil, errors.New("completed orchestration public attempt fence is invalid")
		}
		operation, operationFound := operationsByID[orchestration.ParkOperationID]
		fence, fenceFound := fencesByAttempt[orchestration.AttemptID]
		entryIndex, entryFound := entryIndexes[orchestration.AttemptID]
		if !operationFound || !fenceFound || !entryFound ||
			entryIndex < 0 || entryIndex >= len(findingCounts) {
			return nil, errors.New("completed orchestration aggregate evidence is missing")
		}
		caseOrdinal := -1
		for _, link := range thread.Cases {
			if link.CaseID == orchestration.CaseID {
				caseOrdinal = link.Ordinal
				break
			}
		}
		if caseOrdinal < 0 {
			return nil, errors.New("completed orchestration case is outside its thread")
		}
		previousHash := emptyPRDevelopmentLedgerEntriesDigest()
		if entryIndex > 0 {
			previousHash = entries[entryIndex-1].EntryHash
		}
		if err := validateCompletedPRDevelopmentRepairOrchestrationSnapshot(
			orchestration,
			operation,
			fence,
			storedPRDevelopmentLedgerEntry{
				entry:        entries[entryIndex],
				findingCount: findingCounts[entryIndex],
			},
			int64(caseOrdinal),
			previousHash,
		); err != nil {
			return nil, fmt.Errorf(
				"invalid stored completed pull request development repair orchestration: %w",
				err,
			)
		}
		entries[entryIndex].CIStatus = orchestration.Validation.CIStatus
		entries[entryIndex].ciStatusBound = true
	}
	return orchestrations, nil
}

func loadPRDevelopmentLedgerEntries(
	ctx context.Context,
	queryer rowsQueryer,
	threadID string,
) ([]PRDevelopmentLedgerEntry, error) {
	entries, findingCounts, err := loadPRDevelopmentLedgerEntriesBase(
		ctx,
		queryer,
		threadID,
	)
	if err != nil {
		return nil, err
	}
	for index := range entries {
		if entries[index].Kind == PRDevelopmentLedgerAttempt {
			entries[index].CIStatus = PRDevelopmentCIPassed
			orchestration, found, loadErr := loadPRDevelopmentRepairOrchestration(
				ctx, queryer, entries[index].AttemptID,
			)
			if loadErr != nil {
				return nil, wrapInvalidStoredPRDevelopmentLedger(loadErr)
			}
			if found {
				if orchestration.Phase != PRDevelopmentRepairOrchestrationCompleted ||
					orchestration.LedgerEntryID != entries[index].ID ||
					orchestration.Validation == nil ||
					!validPRDevelopmentCIStatus(orchestration.Validation.CIStatus) {
					return nil, wrapInvalidStoredPRDevelopmentLedger(
						errors.New("stored attempt orchestration linkage is invalid"),
					)
				}
				entries[index].CIStatus = orchestration.Validation.CIStatus
				entries[index].ciStatusBound = true
			}
		}
	}
	if err := validateLoadedPRDevelopmentLedgerEntries(entries, findingCounts); err != nil {
		return nil, err
	}
	return entries, nil
}

func loadPRDevelopmentLedgerEntriesBase(
	ctx context.Context,
	queryer rowsQueryer,
	threadID string,
) ([]PRDevelopmentLedgerEntry, []int, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT `+prDevelopmentLedgerEntryColumns+`
		FROM pr_development_ledger_entries
		WHERE thread_id = ?
		ORDER BY ordinal`,
		threadID,
	)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	stored := make([]storedPRDevelopmentLedgerEntry, 0)
	for rows.Next() {
		item, scanErr := scanPRDevelopmentLedgerEntry(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, nil, scanErr
		}
		stored = append(stored, item)
		if len(stored) > MaxPRDevelopmentLedgerEntries {
			_ = rows.Close()
			return nil, nil, wrapInvalidStoredPRDevelopmentLedger(
				errors.New("stored ledger has too many entries"),
			)
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, nil, err
	}
	entries := make([]PRDevelopmentLedgerEntry, len(stored))
	entryIndexes := make(map[string]int, len(stored))
	for index := range stored {
		entries[index] = stored[index].entry
		entryIndexes[entries[index].ID] = index
	}
	findings, err := queryer.QueryContext(ctx, `
		SELECT finding.entry_id, finding.ordinal, finding.severity, finding.title,
			finding.file, finding.line, finding.message, finding.evidence,
			finding.impact, finding.recommendation, finding.validation
		FROM pr_development_ledger_review_findings AS finding
		JOIN pr_development_ledger_entries AS entry ON entry.id = finding.entry_id
		WHERE entry.thread_id = ?
		ORDER BY entry.ordinal, finding.ordinal`,
		threadID,
	)
	if err != nil {
		return nil, nil, err
	}
	defer findings.Close()
	for findings.Next() {
		var (
			entryID string
			ordinal int64
			finding PRDevelopmentLedgerReviewFinding
			line    sql.NullInt64
		)
		if scanErr := findings.Scan(
			&entryID,
			&ordinal,
			&finding.Severity,
			&finding.Title,
			&finding.File,
			&line,
			&finding.Message,
			&finding.Evidence,
			&finding.Impact,
			&finding.Recommendation,
			&finding.Validation,
		); scanErr != nil {
			_ = findings.Close()
			return nil, nil, scanErr
		}
		index, found := entryIndexes[entryID]
		if !found || ordinal != int64(len(entries[index].Findings)) {
			_ = findings.Close()
			return nil, nil, wrapInvalidStoredPRDevelopmentLedger(
				errors.New("stored review finding order is invalid"),
			)
		}
		if line.Valid {
			if line.Int64 < 1 || line.Int64 > 1<<31-1 {
				_ = findings.Close()
				return nil, nil, wrapInvalidStoredPRDevelopmentLedger(
					errors.New("stored review finding line is invalid"),
				)
			}
			value := int(line.Int64)
			finding.Line = &value
		}
		if validationErr := validatePRDevelopmentLedgerFinding(finding); validationErr != nil {
			_ = findings.Close()
			return nil, nil, wrapInvalidStoredPRDevelopmentLedger(validationErr)
		}
		entries[index].Findings = append(entries[index].Findings, finding)
	}
	if err = findings.Err(); err != nil {
		_ = findings.Close()
		return nil, nil, err
	}
	if err = findings.Close(); err != nil {
		return nil, nil, err
	}
	findingCounts := make([]int, len(stored))
	for index := range stored {
		findingCounts[index] = stored[index].findingCount
	}
	return entries, findingCounts, nil
}

func validateLoadedPRDevelopmentLedgerEntries(
	entries []PRDevelopmentLedgerEntry,
	findingCounts []int,
) error {
	if len(entries) != len(findingCounts) {
		return wrapInvalidStoredPRDevelopmentLedger(
			errors.New("stored ledger entry accounting is invalid"),
		)
	}
	for index := range entries {
		if len(entries[index].Findings) != findingCounts[index] {
			return wrapInvalidStoredPRDevelopmentLedger(
				errors.New("stored review finding count is invalid"),
			)
		}
		if err := validateStoredPRDevelopmentLedgerEntry(entries[index]); err != nil {
			return wrapInvalidStoredPRDevelopmentLedger(err)
		}
	}
	return nil
}

func scanPRDevelopmentLedgerEntry(
	scanner rowScanner,
) (storedPRDevelopmentLedgerEntry, error) {
	var (
		entry        PRDevelopmentLedgerEntry
		ordinal      int64
		fenceOrdinal int64
		caseOrdinal  int64
		noChanges    sql.NullInt64
		findingCount int64
		createdAt    int64
	)
	if err := scanner.Scan(
		&entry.ID,
		&entry.ThreadID,
		&ordinal,
		&entry.Kind,
		&entry.AttemptID,
		&fenceOrdinal,
		&entry.CaseID,
		&caseOrdinal,
		&entry.Commit,
		&entry.Tree,
		&noChanges,
		&entry.Summary,
		&entry.CIPlanDigest,
		&entry.CIResultDigest,
		&entry.ReviewOutcome,
		&findingCount,
		&entry.FenceHash,
		&entry.PreviousHash,
		&entry.EntryHash,
		&createdAt,
	); err != nil {
		return storedPRDevelopmentLedgerEntry{}, err
	}
	entry.Ordinal = int(ordinal)
	entry.FenceOrdinal = int(fenceOrdinal)
	entry.CaseOrdinal = int(caseOrdinal)
	if int64(entry.Ordinal) != ordinal || int64(entry.FenceOrdinal) != fenceOrdinal ||
		int64(entry.CaseOrdinal) != caseOrdinal || findingCount < 0 ||
		findingCount > MaxPRDevelopmentLedgerReviewFindings {
		return storedPRDevelopmentLedgerEntry{}, wrapInvalidStoredPRDevelopmentLedger(
			errors.New("stored ledger entry integer is invalid"),
		)
	}
	if entry.Kind == PRDevelopmentLedgerAttempt {
		if !noChanges.Valid || (noChanges.Int64 != 0 && noChanges.Int64 != 1) {
			return storedPRDevelopmentLedgerEntry{}, wrapInvalidStoredPRDevelopmentLedger(
				errors.New("stored attempt no-change marker is invalid"),
			)
		}
		entry.NoChanges = noChanges.Int64 == 1
	} else if noChanges.Valid {
		return storedPRDevelopmentLedgerEntry{}, wrapInvalidStoredPRDevelopmentLedger(
			errors.New("stored review has an attempt no-change marker"),
		)
	}
	entry.CreatedAt = fromDBTime(createdAt)
	return storedPRDevelopmentLedgerEntry{
		entry:        entry,
		findingCount: int(findingCount),
	}, nil
}

func loadPRDevelopmentLedgerCheckpoints(
	ctx context.Context,
	queryer rowsQueryer,
	threadID string,
) ([]PRDevelopmentLedgerCheckpoint, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT `+prDevelopmentLedgerCheckpointColumns+`
		FROM pr_development_ledger_checkpoints
		WHERE thread_id = ?
		ORDER BY generation`,
		threadID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	checkpoints := make([]PRDevelopmentLedgerCheckpoint, 0)
	for rows.Next() {
		var checkpoint PRDevelopmentLedgerCheckpoint
		var generation, throughOrdinal, createdAt int64
		if scanErr := rows.Scan(
			&checkpoint.ID,
			&checkpoint.ThreadID,
			&generation,
			&throughOrdinal,
			&checkpoint.SourceDigest,
			&checkpoint.Summary,
			&checkpoint.CompactorID,
			&checkpoint.PromptDigest,
			&checkpoint.PreviousHash,
			&checkpoint.CheckpointHash,
			&createdAt,
		); scanErr != nil {
			return nil, scanErr
		}
		checkpoint.Generation = int(generation)
		checkpoint.ThroughOrdinal = int(throughOrdinal)
		if int64(checkpoint.Generation) != generation ||
			int64(checkpoint.ThroughOrdinal) != throughOrdinal {
			return nil, wrapInvalidStoredPRDevelopmentLedger(
				errors.New("stored ledger checkpoint integer is invalid"),
			)
		}
		checkpoint.CreatedAt = fromDBTime(createdAt)
		if err := validateStoredPRDevelopmentLedgerCheckpoint(checkpoint); err != nil {
			return nil, wrapInvalidStoredPRDevelopmentLedger(err)
		}
		checkpoints = append(checkpoints, checkpoint)
		if len(checkpoints) > MaxPRDevelopmentControllerFences {
			return nil, wrapInvalidStoredPRDevelopmentLedger(
				errors.New("stored ledger has too many checkpoints"),
			)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return checkpoints, nil
}

func validatePRDevelopmentLedgerAggregate(
	ctx context.Context,
	queryer rowsQueryer,
	thread PRDevelopmentThread,
	controller PRDevelopmentController,
	ledger PRDevelopmentLedger,
) error {
	owner, err := loadPRDevelopmentRepairSessionByID(ctx, queryer, controller.OwnerSessionID)
	if err != nil {
		return err
	}
	fences, err := loadPRDevelopmentReviewFences(ctx, queryer, controller.ID)
	if err != nil {
		return err
	}
	return validatePRDevelopmentLedgerAggregateSnapshot(
		thread,
		controller,
		owner,
		fences,
		ledger,
	)
}

func validatePRDevelopmentLedgerAggregateSnapshot(
	thread PRDevelopmentThread,
	controller PRDevelopmentController,
	owner PRDevelopmentRepairSession,
	fences []PRDevelopmentAttemptReviewFence,
	ledger PRDevelopmentLedger,
) error {
	if thread.Kind != PRDevelopmentThreadProvider || ledger.ThreadID != thread.ID ||
		controller.ThreadID != thread.ID {
		return wrapInvalidStoredPRDevelopmentLedger(
			errors.New("stored ledger owner is invalid"),
		)
	}
	if len(ledger.Entries) == 0 {
		if len(ledger.Checkpoints) != 0 ||
			ledger.EntriesDigest != emptyPRDevelopmentLedgerEntriesDigest() ||
			ledger.CheckpointsDigest != emptyPRDevelopmentLedgerCheckpointsDigest() {
			return wrapInvalidStoredPRDevelopmentLedger(
				errors.New("stored empty ledger high-water state is invalid"),
			)
		}
		return nil
	}
	ownerCaseOrdinal := -1
	for _, link := range thread.Cases {
		if link.CaseID == owner.CaseID {
			ownerCaseOrdinal = link.Ordinal
			break
		}
	}
	if ownerCaseOrdinal < 0 {
		return wrapInvalidStoredPRDevelopmentLedger(
			errors.New("stored ledger owner case is outside its thread"),
		)
	}
	baseFenceOrdinal := ledger.Entries[0].FenceOrdinal
	if baseFenceOrdinal < 0 || baseFenceOrdinal >= len(fences) ||
		ledger.Entries[0].Kind != PRDevelopmentLedgerAttempt {
		return wrapInvalidStoredPRDevelopmentLedger(
			errors.New("stored ledger anchor is invalid"),
		)
	}
	previousHash := emptyPRDevelopmentLedgerEntriesDigest()
	var previousCreatedAt time.Time
	for index, entry := range ledger.Entries {
		fenceOrdinal := baseFenceOrdinal + index/2
		if fenceOrdinal >= len(fences) {
			return wrapInvalidStoredPRDevelopmentLedger(
				errors.New("stored ledger advanced beyond controller fences"),
			)
		}
		fence := fences[fenceOrdinal]
		expectedKind := PRDevelopmentLedgerAttempt
		if index%2 == 1 {
			expectedKind = PRDevelopmentLedgerReview
		}
		if entry.ThreadID != thread.ID || entry.Ordinal != baseFenceOrdinal*2+index ||
			entry.Kind != expectedKind || entry.FenceOrdinal != fenceOrdinal ||
			entry.AttemptID != fence.AttemptID || entry.CaseID != owner.CaseID ||
			entry.CaseOrdinal != ownerCaseOrdinal || entry.PreviousHash != previousHash ||
			(!previousCreatedAt.IsZero() && entry.CreatedAt.Before(previousCreatedAt)) {
			return wrapInvalidStoredPRDevelopmentLedger(
				errors.New("stored ledger entry order or ownership is invalid"),
			)
		}
		switch entry.Kind {
		case PRDevelopmentLedgerAttempt:
			if entry.Commit != fence.TipCommit || entry.Tree != fence.Tree ||
				entry.NoChanges != fence.NoChanges ||
				entry.FenceHash != mutationStagePRDevelopmentReviewFenceHash(fence) ||
				entry.CreatedAt.Before(fence.CreatedAt) {
				return wrapInvalidStoredPRDevelopmentLedger(
					errors.New("stored attempt ledger evidence is invalid"),
				)
			}
		case PRDevelopmentLedgerReview:
			if fence.ReviewedAt == nil || entry.FenceHash != fence.FenceHash ||
				entry.CreatedAt.Before(*fence.ReviewedAt) {
				return wrapInvalidStoredPRDevelopmentLedger(
					errors.New("stored review ledger evidence is invalid"),
				)
			}
		default:
			return wrapInvalidStoredPRDevelopmentLedger(
				errors.New("stored ledger entry kind is invalid"),
			)
		}
		if entry.EntryHash != hashPRDevelopmentLedgerEntry(entry) {
			return wrapInvalidStoredPRDevelopmentLedger(
				errors.New("stored ledger entry hash is invalid"),
			)
		}
		previousHash = entry.EntryHash
		previousCreatedAt = entry.CreatedAt
	}
	tail := ledger.Entries[len(ledger.Entries)-1]
	if tail.Kind == PRDevelopmentLedgerAttempt &&
		fences[tail.FenceOrdinal].ReviewedAt != nil {
		return wrapInvalidStoredPRDevelopmentLedger(
			errors.New("stored reviewed fence is missing its atomic review account"),
		)
	}
	if ledger.EntriesDigest != previousHash {
		return wrapInvalidStoredPRDevelopmentLedger(
			errors.New("stored ledger entry high-water digest is invalid"),
		)
	}
	previousHash = emptyPRDevelopmentLedgerCheckpointsDigest()
	previousThrough := baseFenceOrdinal*2 - 1
	previousCreatedAt = time.Time{}
	for generation, checkpoint := range ledger.Checkpoints {
		entryIndex := checkpoint.ThroughOrdinal - baseFenceOrdinal*2
		if checkpoint.ThreadID != thread.ID || checkpoint.Generation != generation ||
			entryIndex < 0 || entryIndex >= len(ledger.Entries) ||
			ledger.Entries[entryIndex].Kind != PRDevelopmentLedgerReview ||
			checkpoint.ThroughOrdinal <= previousThrough ||
			checkpoint.SourceDigest != ledger.Entries[entryIndex].EntryHash ||
			checkpoint.PreviousHash != previousHash ||
			checkpoint.CreatedAt.Before(ledger.Entries[entryIndex].CreatedAt) ||
			(!previousCreatedAt.IsZero() && checkpoint.CreatedAt.Before(previousCreatedAt)) ||
			checkpoint.CheckpointHash != hashPRDevelopmentLedgerCheckpoint(checkpoint) {
			return wrapInvalidStoredPRDevelopmentLedger(
				errors.New("stored ledger checkpoint chain is invalid"),
			)
		}
		previousHash = checkpoint.CheckpointHash
		previousThrough = checkpoint.ThroughOrdinal
		previousCreatedAt = checkpoint.CreatedAt
	}
	if ledger.CheckpointsDigest != previousHash {
		return wrapInvalidStoredPRDevelopmentLedger(
			errors.New("stored ledger checkpoint high-water digest is invalid"),
		)
	}
	return nil
}

func validateStoredPRDevelopmentLedgerEntry(entry PRDevelopmentLedgerEntry) error {
	if !validPrefixedHexID(entry.ID, prDevelopmentLedgerEntryIDPrefix) ||
		!validPrefixedHexID(entry.ThreadID, prDevelopmentThreadIDPrefix) ||
		!validPrefixedHexID(entry.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(entry.CaseID, prDevelopmentCaseIDPrefix) ||
		entry.Ordinal < 0 || entry.Ordinal >= MaxPRDevelopmentLedgerEntries ||
		entry.FenceOrdinal < 0 || entry.FenceOrdinal >= MaxPRDevelopmentControllerFences ||
		entry.CaseOrdinal < 0 || entry.CaseOrdinal >= MaxPRDevelopmentThreadCases ||
		entry.Ordinal/2 != entry.FenceOrdinal ||
		!validStoredPRDevelopmentRepairText(
			entry.Summary,
			MaxPRDevelopmentLedgerSummaryBytes,
		) || !validPRDevelopmentHex(entry.FenceHash, sha256.Size*2) ||
		!validPRDevelopmentHex(entry.PreviousHash, sha256.Size*2) ||
		!validPRDevelopmentHex(entry.EntryHash, sha256.Size*2) ||
		validateDBTimestamp("ledger entry creation time", entry.CreatedAt) != nil {
		return errors.New("stored ledger entry is invalid")
	}
	switch entry.Kind {
	case PRDevelopmentLedgerAttempt:
		if entry.Ordinal%2 != 0 || !validSameWidthPRDevelopmentOIDs(entry.Commit, entry.Tree) ||
			!validPRDevelopmentHex(entry.CIPlanDigest, sha256.Size*2) ||
			!validPRDevelopmentHex(entry.CIResultDigest, sha256.Size*2) ||
			!validPRDevelopmentCIStatus(entry.CIStatus) ||
			entry.ReviewOutcome != "" || len(entry.Findings) != 0 {
			return errors.New("stored attempt ledger shape is invalid")
		}
	case PRDevelopmentLedgerReview:
		if entry.Ordinal%2 != 1 || entry.Commit != "" || entry.Tree != "" ||
			entry.CIPlanDigest != "" || entry.CIResultDigest != "" ||
			entry.CIStatus != "" ||
			!validPRDevelopmentLedgerReviewOutcome(entry.ReviewOutcome) ||
			len(entry.Findings) > MaxPRDevelopmentLedgerReviewFindings ||
			(entry.ReviewOutcome == PRDevelopmentLedgerReviewPassed && len(entry.Findings) != 0) ||
			(entry.ReviewOutcome == PRDevelopmentLedgerReviewChangesRequired && len(entry.Findings) == 0) {
			return errors.New("stored review ledger shape is invalid")
		}
		if reviewBytes, err := validatePRDevelopmentLedgerFindings(entry.Findings); err != nil ||
			reviewBytes > MaxPRDevelopmentLedgerReviewBytes {
			return errors.New("stored review ledger findings are invalid")
		}
	default:
		return errors.New("stored ledger entry kind is invalid")
	}
	return nil
}

func validateStoredPRDevelopmentLedgerCheckpoint(
	checkpoint PRDevelopmentLedgerCheckpoint,
) error {
	if !validPrefixedHexID(checkpoint.ID, prDevelopmentLedgerCheckpointIDPrefix) ||
		!validPrefixedHexID(checkpoint.ThreadID, prDevelopmentThreadIDPrefix) ||
		checkpoint.Generation < 0 ||
		checkpoint.Generation >= MaxPRDevelopmentControllerFences ||
		checkpoint.ThroughOrdinal < 1 ||
		checkpoint.ThroughOrdinal >= MaxPRDevelopmentLedgerEntries ||
		checkpoint.ThroughOrdinal%2 != 1 ||
		!validPRDevelopmentHex(checkpoint.SourceDigest, sha256.Size*2) ||
		!validStoredPRDevelopmentRepairText(
			checkpoint.Summary,
			MaxPRDevelopmentLedgerCheckpointSummaryBytes,
		) || !validPRDevelopmentRepairIdentity(
		checkpoint.CompactorID,
		MaxPRDevelopmentControllerIdentityBytes,
	) || !validPRDevelopmentHex(checkpoint.PromptDigest, sha256.Size*2) ||
		!validPRDevelopmentHex(checkpoint.PreviousHash, sha256.Size*2) ||
		!validPRDevelopmentHex(checkpoint.CheckpointHash, sha256.Size*2) ||
		validateDBTimestamp("ledger checkpoint creation time", checkpoint.CreatedAt) != nil {
		return errors.New("stored ledger checkpoint is invalid")
	}
	return nil
}

func normalizePRDevelopmentLedgerAttemptAppend(
	input PRDevelopmentLedgerAttemptAppend,
) (PRDevelopmentLedgerAttemptAppend, error) {
	input.CaseID = strings.TrimSpace(input.CaseID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.Summary = strings.TrimSpace(input.Summary)
	input.CIPlanDigest = strings.TrimSpace(input.CIPlanDigest)
	input.CIResultDigest = strings.TrimSpace(input.CIResultDigest)
	if !validPrefixedHexID(input.CaseID, prDevelopmentCaseIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validStoredPRDevelopmentRepairText(
			input.Summary,
			MaxPRDevelopmentLedgerSummaryBytes,
		) || !validPRDevelopmentHex(input.CIPlanDigest, sha256.Size*2) ||
		!validPRDevelopmentHex(input.CIResultDigest, sha256.Size*2) {
		return PRDevelopmentLedgerAttemptAppend{}, fmt.Errorf(
			"%w: valid case, attempt, summary, and CI digests are required",
			ErrInvalidPRDevelopmentLedger,
		)
	}
	return input, nil
}

func normalizePRDevelopmentLedgerReviewAppend(
	input PRDevelopmentLedgerReviewAppend,
) (PRDevelopmentLedgerReviewAppend, error) {
	input.CaseID = strings.TrimSpace(input.CaseID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	input.Summary = strings.TrimSpace(input.Summary)
	input.Findings = clonePRDevelopmentLedgerFindings(input.Findings)
	for index := range input.Findings {
		finding := &input.Findings[index]
		finding.Title = strings.TrimSpace(finding.Title)
		finding.File = strings.TrimSpace(finding.File)
		finding.Message = strings.TrimSpace(finding.Message)
		finding.Evidence = strings.TrimSpace(finding.Evidence)
		finding.Impact = strings.TrimSpace(finding.Impact)
		finding.Recommendation = strings.TrimSpace(finding.Recommendation)
		finding.Validation = strings.TrimSpace(finding.Validation)
	}
	reviewBytes, findingErr := validatePRDevelopmentLedgerFindings(input.Findings)
	if !validPrefixedHexID(input.CaseID, prDevelopmentCaseIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		input.ExpectedRevision < 1 ||
		input.ExpectedRevision > MaxPRDevelopmentControllerRevision ||
		!validPRDevelopmentRepairIdentity(
			input.LeaseToken,
			prDevelopmentControllerLeaseTokenBytes,
		) || input.LeaseEpoch < 1 ||
		!validStoredPRDevelopmentRepairText(
			input.Summary,
			MaxPRDevelopmentLedgerSummaryBytes,
		) || !validPRDevelopmentLedgerReviewOutcome(input.Outcome) ||
		findingErr != nil || reviewBytes > MaxPRDevelopmentLedgerReviewBytes ||
		(input.Outcome == PRDevelopmentLedgerReviewPassed && len(input.Findings) != 0) ||
		(input.Outcome == PRDevelopmentLedgerReviewChangesRequired && len(input.Findings) == 0) {
		return PRDevelopmentLedgerReviewAppend{}, fmt.Errorf(
			"%w: valid case, attempt, review lease proof, outcome, summary, and bounded findings are required",
			ErrInvalidPRDevelopmentLedger,
		)
	}
	return input, nil
}

func normalizePRDevelopmentLedgerCheckpointAppend(
	input PRDevelopmentLedgerCheckpointAppend,
) (PRDevelopmentLedgerCheckpointAppend, error) {
	input.CaseID = strings.TrimSpace(input.CaseID)
	input.SourceDigest = strings.TrimSpace(input.SourceDigest)
	input.Summary = strings.TrimSpace(input.Summary)
	input.CompactorID = strings.TrimSpace(input.CompactorID)
	input.PromptDigest = strings.TrimSpace(input.PromptDigest)
	if !validPrefixedHexID(input.CaseID, prDevelopmentCaseIDPrefix) ||
		input.ThroughOrdinal < 1 ||
		input.ThroughOrdinal >= MaxPRDevelopmentLedgerEntries ||
		input.ThroughOrdinal%2 != 1 ||
		!validPRDevelopmentHex(input.SourceDigest, sha256.Size*2) ||
		!validStoredPRDevelopmentRepairText(
			input.Summary,
			MaxPRDevelopmentLedgerCheckpointSummaryBytes,
		) || !validPRDevelopmentRepairIdentity(
		input.CompactorID,
		MaxPRDevelopmentControllerIdentityBytes,
	) || !validPRDevelopmentHex(input.PromptDigest, sha256.Size*2) {
		return PRDevelopmentLedgerCheckpointAppend{}, fmt.Errorf(
			"%w: valid reviewed prefix, summary, compactor, and digests are required",
			ErrInvalidPRDevelopmentLedger,
		)
	}
	return input, nil
}

func validatePRDevelopmentLedgerFindings(
	findings []PRDevelopmentLedgerReviewFinding,
) (int, error) {
	if len(findings) > MaxPRDevelopmentLedgerReviewFindings {
		return 0, errors.New("too many ledger review findings")
	}
	total := 0
	for _, finding := range findings {
		if err := validatePRDevelopmentLedgerFinding(finding); err != nil {
			return 0, err
		}
		total += len(finding.Severity) + len(finding.Title) + len(finding.File) +
			len(finding.Message) + len(finding.Evidence) + len(finding.Impact) +
			len(finding.Recommendation) + len(finding.Validation)
	}
	return total, nil
}

func validatePRDevelopmentLedgerFinding(
	finding PRDevelopmentLedgerReviewFinding,
) error {
	if !validPRDevelopmentReviewSeverity(finding.Severity) ||
		!validStoredPRDevelopmentRepairText(
			finding.Title,
			maxPRDevelopmentLedgerFindingTitleBytes,
		) || !validOptionalPRDevelopmentRepairText(
		finding.File,
		maxPRDevelopmentLedgerFindingFileBytes,
	) || !validStoredPRDevelopmentRepairText(
		finding.Message,
		maxPRDevelopmentLedgerFindingMessageBytes,
	) || !validOptionalPRDevelopmentRepairText(
		finding.Evidence,
		maxPRDevelopmentLedgerFindingEvidenceBytes,
	) || !validOptionalPRDevelopmentRepairText(
		finding.Impact,
		maxPRDevelopmentLedgerFindingImpactBytes,
	) || !validOptionalPRDevelopmentRepairText(
		finding.Recommendation,
		maxPRDevelopmentLedgerFindingRecommendationBytes,
	) || !validOptionalPRDevelopmentRepairText(
		finding.Validation,
		maxPRDevelopmentLedgerFindingValidationBytes,
	) || (finding.Line != nil && (*finding.Line < 1 || *finding.Line > 1<<31-1)) {
		return errors.New("ledger review finding is invalid")
	}
	return nil
}

func validPRDevelopmentReviewSeverity(severity ReviewSeverity) bool {
	switch severity {
	case ReviewSeverityCritical, ReviewSeverityHigh, ReviewSeverityMedium, ReviewSeverityLow:
		return true
	default:
		return false
	}
}

func validPRDevelopmentLedgerReviewOutcome(
	outcome PRDevelopmentLedgerReviewOutcome,
) bool {
	switch outcome {
	case PRDevelopmentLedgerReviewPassed,
		PRDevelopmentLedgerReviewChangesRequired,
		PRDevelopmentLedgerReviewAttentionRequired:
		return true
	default:
		return false
	}
}

func clonePRDevelopmentLedgerFindings(
	findings []PRDevelopmentLedgerReviewFinding,
) []PRDevelopmentLedgerReviewFinding {
	if findings == nil {
		return nil
	}
	cloned := make([]PRDevelopmentLedgerReviewFinding, len(findings))
	copy(cloned, findings)
	for index := range cloned {
		if cloned[index].Line != nil {
			line := *cloned[index].Line
			cloned[index].Line = &line
		}
	}
	return cloned
}

func mutationStagePRDevelopmentReviewFenceHash(
	fence PRDevelopmentAttemptReviewFence,
) string {
	fence.ReviewLeaseEpoch = 0
	fence.ReviewLeaseTokenDigest = ""
	fence.ReviewControllerRevision = 0
	fence.ReviewedAt = nil
	return hashPRDevelopmentReviewFence(fence)
}

func emptyPRDevelopmentLedgerEntriesDigest() string {
	digest := sha256.Sum256([]byte("picoclaw-pr-development-ledger-entries-v1\x00empty"))
	return hex.EncodeToString(digest[:])
}

func emptyPRDevelopmentLedgerCheckpointsDigest() string {
	digest := sha256.Sum256([]byte("picoclaw-pr-development-ledger-checkpoints-v1\x00empty"))
	return hex.EncodeToString(digest[:])
}

func hashPRDevelopmentLedgerEntry(entry PRDevelopmentLedgerEntry) string {
	digest := sha256.New()
	domain := "picoclaw-pr-development-ledger-entry-v1"
	if entry.ciStatusBound {
		domain = "picoclaw-pr-development-ledger-entry-v2"
	}
	writePRDevelopmentLedgerHashField(
		digest,
		domain,
	)
	values := []string{
		entry.ID,
		entry.ThreadID,
		fmt.Sprintf("%d", entry.Ordinal),
		string(entry.Kind),
		entry.AttemptID,
		fmt.Sprintf("%d", entry.FenceOrdinal),
		entry.CaseID,
		fmt.Sprintf("%d", entry.CaseOrdinal),
		entry.Commit,
		entry.Tree,
		fmt.Sprintf("%t", entry.NoChanges),
		entry.Summary,
		entry.CIPlanDigest,
		entry.CIResultDigest,
	}
	if entry.ciStatusBound {
		values = append(values, string(entry.CIStatus))
	}
	values = append(values,
		string(entry.ReviewOutcome),
		fmt.Sprintf("%d", len(entry.Findings)),
		entry.FenceHash,
		entry.PreviousHash,
		fmt.Sprintf("%d", toDBTime(entry.CreatedAt)),
	)
	for _, value := range values {
		writePRDevelopmentLedgerHashField(digest, value)
	}
	for ordinal, finding := range entry.Findings {
		line := ""
		if finding.Line != nil {
			line = fmt.Sprintf("%d", *finding.Line)
		}
		for _, value := range []string{
			fmt.Sprintf("%d", ordinal),
			string(finding.Severity),
			finding.Title,
			finding.File,
			line,
			finding.Message,
			finding.Evidence,
			finding.Impact,
			finding.Recommendation,
			finding.Validation,
		} {
			writePRDevelopmentLedgerHashField(digest, value)
		}
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func hashPRDevelopmentLedgerCheckpoint(
	checkpoint PRDevelopmentLedgerCheckpoint,
) string {
	digest := sha256.New()
	writePRDevelopmentLedgerHashField(
		digest,
		"picoclaw-pr-development-ledger-checkpoint-v1",
	)
	for _, value := range []string{
		checkpoint.ID,
		checkpoint.ThreadID,
		fmt.Sprintf("%d", checkpoint.Generation),
		fmt.Sprintf("%d", checkpoint.ThroughOrdinal),
		checkpoint.SourceDigest,
		checkpoint.Summary,
		checkpoint.CompactorID,
		checkpoint.PromptDigest,
		checkpoint.PreviousHash,
		fmt.Sprintf("%d", toDBTime(checkpoint.CreatedAt)),
	} {
		writePRDevelopmentLedgerHashField(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writePRDevelopmentLedgerHashField(digest hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(value))
}

func equalPRDevelopmentLedgerEntryIntent(
	left, right PRDevelopmentLedgerEntry,
) bool {
	if left.ThreadID != right.ThreadID || left.Ordinal != right.Ordinal ||
		left.Kind != right.Kind || left.AttemptID != right.AttemptID ||
		left.FenceOrdinal != right.FenceOrdinal || left.CaseID != right.CaseID ||
		left.CaseOrdinal != right.CaseOrdinal || left.Commit != right.Commit ||
		left.Tree != right.Tree || left.NoChanges != right.NoChanges ||
		left.Summary != right.Summary || left.CIPlanDigest != right.CIPlanDigest ||
		left.CIResultDigest != right.CIResultDigest ||
		left.ReviewOutcome != right.ReviewOutcome || left.FenceHash != right.FenceHash ||
		len(left.Findings) != len(right.Findings) {
		return false
	}
	for index := range left.Findings {
		if !equalPRDevelopmentLedgerFinding(left.Findings[index], right.Findings[index]) {
			return false
		}
	}
	return true
}

func equalPRDevelopmentLedgerFinding(
	left, right PRDevelopmentLedgerReviewFinding,
) bool {
	if left.Severity != right.Severity || left.Title != right.Title ||
		left.File != right.File || left.Message != right.Message ||
		left.Evidence != right.Evidence || left.Impact != right.Impact ||
		left.Recommendation != right.Recommendation ||
		left.Validation != right.Validation || (left.Line == nil) != (right.Line == nil) {
		return false
	}
	return left.Line == nil || *left.Line == *right.Line
}

func equalPRDevelopmentLedgerCheckpointIntent(
	left, right PRDevelopmentLedgerCheckpoint,
) bool {
	return left.ThreadID == right.ThreadID &&
		left.ThroughOrdinal == right.ThroughOrdinal &&
		left.SourceDigest == right.SourceDigest && left.Summary == right.Summary &&
		left.CompactorID == right.CompactorID && left.PromptDigest == right.PromptDigest
}

func wrapInvalidStoredPRDevelopmentLedger(err error) error {
	return fmt.Errorf("%w: %v", errInvalidStoredPRDevelopmentLedger, err)
}
