//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	_ PRDevelopmentAttentionTriggerQueue      = (*Store)(nil)
	_ PRDevelopmentAttentionTriggerCaseReader = (*Store)(nil)
)

const prDevelopmentAttentionTriggerColumns = `
	review_entry_id, review_entry_hash, case_id, conversation_version,
	transcript_digest, decision_point, status, owner, lease_until, attempts,
	available_at, policy_revision, pinned_policy_json, subject_revision,
	run_id, last_error, created_at, updated_at, completed_at`

// GetPRDevelopmentAttentionTrigger retrieves one automatic local-review
// attention occurrence by its immutable review-entry identity.
func (s *Store) GetPRDevelopmentAttentionTrigger(
	ctx context.Context,
	reviewEntryID string,
) (PRDevelopmentAttentionTrigger, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentAttentionTrigger{}, err
	}
	reviewEntryID = strings.TrimSpace(reviewEntryID)
	if !validPrefixedHexID(reviewEntryID, prDevelopmentLedgerEntryIDPrefix) {
		return PRDevelopmentAttentionTrigger{}, fmt.Errorf(
			"%w: invalid review entry ID",
			ErrInvalidPRDevelopmentAttentionTrigger,
		)
	}
	trigger, err := getPRDevelopmentAttentionTrigger(ctx, s.db, reviewEntryID)
	if err != nil {
		return PRDevelopmentAttentionTrigger{}, s.dbError(err)
	}
	return trigger, nil
}

// GetCurrentPRDevelopmentAttentionTriggerForCase returns one atomic
// case/conversation/ledger projection across local-review and publication-gate
// attention for the later case-owned bridge. It never claims work or exposes a
// mutation capability.
func (s *Store) GetCurrentPRDevelopmentAttentionTriggerForCase(
	ctx context.Context,
	caseID string,
) (PRDevelopmentAttentionTriggerCaseSnapshot, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentAttentionTriggerCaseSnapshot{}, err
	}
	caseID = strings.TrimSpace(caseID)
	if !validPrefixedHexID(caseID, prDevelopmentCaseIDPrefix) {
		return PRDevelopmentAttentionTriggerCaseSnapshot{}, fmt.Errorf(
			"%w: invalid development case ID",
			ErrInvalidPRDevelopmentAttentionTrigger,
		)
	}
	var result PRDevelopmentAttentionTriggerCaseSnapshot
	err := s.withPRDevelopmentConversationReadSnapshot(
		ctx,
		func(queryer rowsQueryer) error {
			storedCase, loadErr := getPRDevelopmentCaseRecord(ctx, queryer, caseID)
			if loadErr != nil {
				return loadErr
			}
			if storedCase.Case.ID != caseID {
				return errors.New("development attention case binding is invalid")
			}
			conversation, loadErr := loadPRDevelopmentConversation(ctx, queryer, caseID)
			if loadErr != nil {
				return loadErr
			}
			thread, loadErr := loadPRDevelopmentThreadForCase(ctx, queryer, caseID)
			if loadErr != nil {
				return loadErr
			}
			ledger, loadErr := loadPRDevelopmentLedgerAggregate(ctx, queryer, thread)
			if loadErr != nil {
				return loadErr
			}
			result = PRDevelopmentAttentionTriggerCaseSnapshot{
				CaseID:              caseID,
				ConversationVersion: conversation.Conversation.Version,
			}
			var current *PRDevelopmentLedgerEntry
			if len(ledger.Entries) != 0 {
				tail := ledger.Entries[len(ledger.Entries)-1]
				if tail.Kind == PRDevelopmentLedgerReview && tail.CaseID == caseID {
					current = &tail
					result.CurrentReviewEntryID = tail.ID
					result.CurrentReviewEntryHash = tail.EntryHash
					result.CurrentReviewOutcome = tail.ReviewOutcome
					result.AttentionRequired = tail.ReviewOutcome ==
						PRDevelopmentLedgerReviewAttentionRequired
				}
			}

			if loadErr := loadCurrentPRDevelopmentAttentionTriggerProjection(
				ctx,
				queryer,
				caseID,
				conversation.Conversation,
				ledger,
				current,
				&result,
			); loadErr != nil {
				return loadErr
			}
			if loadErr := loadCurrentPRDevelopmentPublicationAttentionProjection(
				ctx,
				queryer,
				caseID,
				current,
				&result,
			); loadErr != nil {
				return loadErr
			}
			if result.TriggerCurrent && result.PublicationCurrent {
				return errors.New(
					"development attention sources cannot both own the current review",
				)
			}
			return nil
		},
	)
	if err != nil {
		return PRDevelopmentAttentionTriggerCaseSnapshot{}, fmt.Errorf(
			"get current pull request development attention trigger: %w",
			s.dbError(err),
		)
	}
	return result, nil
}

func loadCurrentPRDevelopmentAttentionTriggerProjection(
	ctx context.Context,
	queryer rowsQueryer,
	caseID string,
	conversation PRDevelopmentConversation,
	ledger PRDevelopmentLedger,
	current *PRDevelopmentLedgerEntry,
	result *PRDevelopmentAttentionTriggerCaseSnapshot,
) error {
	trigger, err := getLatestPRDevelopmentAttentionTriggerForCase(
		ctx,
		queryer,
		caseID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, found := findPRDevelopmentAttentionTriggerEntry(ledger, trigger)
	if !found {
		return errors.New(
			"development attention trigger ledger binding is invalid",
		)
	}
	if trigger.ConversationVersion > conversation.Version ||
		len(conversation.Messages) < int(trigger.ConversationVersion) {
		return errors.New(
			"development attention trigger conversation prefix is unavailable",
		)
	}
	digest := emptyPRDevelopmentTranscriptDigest()
	for _, message := range conversation.Messages[:trigger.ConversationVersion] {
		digest, err = extendPRDevelopmentTranscriptDigest(digest, message)
		if err != nil {
			return err
		}
	}
	if digest != trigger.TranscriptDigest {
		return errors.New(
			"development attention trigger conversation prefix changed",
		)
	}
	triggerCopy := trigger
	result.Trigger = &triggerCopy
	if current == nil || current.ID != trigger.ReviewEntryID ||
		current.EntryHash != trigger.ReviewEntryHash ||
		current.ReviewOutcome != PRDevelopmentLedgerReviewAttentionRequired ||
		trigger.Status == PRDevelopmentAttentionTriggerSuperseded {
		return nil
	}
	_, err = loadAnchoredPRDevelopmentAttentionSnapshot(
		ctx,
		queryer,
		prDevelopmentAttentionOccurrenceAnchor{
			CaseID:              trigger.CaseID,
			ReviewEntryID:       trigger.ReviewEntryID,
			ReviewEntryHash:     trigger.ReviewEntryHash,
			ConversationVersion: trigger.ConversationVersion,
			TranscriptDigest:    trigger.TranscriptDigest,
		},
	)
	if errors.Is(err, ErrPRDevelopmentAttentionSuperseded) {
		return nil
	}
	if err != nil {
		return err
	}
	result.TriggerCurrent = true
	return nil
}

func loadCurrentPRDevelopmentPublicationAttentionProjection(
	ctx context.Context,
	queryer rowsQueryer,
	caseID string,
	current *PRDevelopmentLedgerEntry,
	result *PRDevelopmentAttentionTriggerCaseSnapshot,
) error {
	if current == nil || current.ReviewOutcome != PRDevelopmentLedgerReviewPassed {
		return nil
	}
	publication, err := getPRDevelopmentPublicationByReview(ctx, queryer, current.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if publication.CaseID != caseID ||
		publication.ReviewLedgerEntryID != current.ID ||
		publication.ReviewLedgerEntryHash != current.EntryHash ||
		publication.ReviewOutcome != PRDevelopmentLedgerReviewPassed {
		return errors.New(
			"development publication attention review binding is invalid",
		)
	}
	if publication.DecisionRunID == "" {
		return nil
	}
	result.Publication = &PRDevelopmentPublicationAttentionProjection{
		CaseID: publication.CaseID,
		DecisionRun: PRDevelopmentPublicationDecisionRunLink{
			Key:   publicationPRDevelopmentDecisionKey(publication),
			RunID: publication.DecisionRunID,
		},
		PinnedPolicy: append(json.RawMessage(nil), publication.PinnedPolicy...),
		Status:       publication.Status,
		ClaimFrom:    publication.ClaimFrom,
	}
	_, err = loadCurrentPRDevelopmentPublicationHighWater(ctx, queryer, publication)
	if errors.Is(err, ErrPRDevelopmentPublicationSuperseded) {
		return nil
	}
	if err != nil {
		return err
	}
	result.PublicationCurrent = true
	result.PublicationAttentionRequired = publication.Status == PRDevelopmentPublicationGateWaiting ||
		(publication.Status == PRDevelopmentPublicationClaimed &&
			publication.ClaimFrom == PRDevelopmentPublicationGateWaiting)
	return nil
}

// GetClaimedPRDevelopmentAttentionSnapshot returns the exact completion-time
// occurrence prefix and current admissible review/controller tail under a live
// trigger lease. Conversation messages appended later are excluded.
func (s *Store) GetClaimedPRDevelopmentAttentionSnapshot(
	ctx context.Context,
	reviewEntryID string,
	leaseToken string,
) (PRDevelopmentAttentionTrigger, PRDevelopmentAttentionSnapshot, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentAttentionTrigger{}, PRDevelopmentAttentionSnapshot{}, err
	}
	reviewEntryID = strings.TrimSpace(reviewEntryID)
	leaseToken = strings.TrimSpace(leaseToken)
	if !validPrefixedHexID(reviewEntryID, prDevelopmentLedgerEntryIDPrefix) ||
		leaseToken == "" {
		return PRDevelopmentAttentionTrigger{}, PRDevelopmentAttentionSnapshot{}, fmt.Errorf(
			"%w: valid review entry ID and lease token are required",
			ErrInvalidPRDevelopmentAttentionTrigger,
		)
	}
	now, err := s.currentTime()
	if err != nil {
		return PRDevelopmentAttentionTrigger{}, PRDevelopmentAttentionSnapshot{}, err
	}
	var (
		trigger  PRDevelopmentAttentionTrigger
		snapshot PRDevelopmentAttentionSnapshot
	)
	err = s.withPRDevelopmentConversationReadSnapshot(
		ctx,
		func(queryer rowsQueryer) error {
			loaded, loadErr := getPRDevelopmentAttentionTrigger(
				ctx,
				queryer,
				reviewEntryID,
			)
			if loadErr != nil {
				return loadErr
			}
			if !livePRDevelopmentAttentionTriggerClaim(loaded, leaseToken, now) {
				return ErrStaleLease
			}
			current, loadErr := loadAnchoredPRDevelopmentAttentionSnapshot(
				ctx,
				queryer,
				prDevelopmentAttentionOccurrenceAnchorForTrigger(loaded),
			)
			if loadErr != nil {
				return loadErr
			}
			trigger = loaded
			snapshot = current
			return nil
		},
	)
	if err != nil {
		return PRDevelopmentAttentionTrigger{}, PRDevelopmentAttentionSnapshot{},
			s.dbError(err)
	}
	return trigger, snapshot, nil
}

// ClaimPRDevelopmentAttentionTriggers leases pending or expired automatic
// attention work. Every trigger retains its exact policy and subject pins.
func (s *Store) ClaimPRDevelopmentAttentionTriggers(
	ctx context.Context,
	workerLabel string,
	limit int,
	lease time.Duration,
) ([]PRDevelopmentAttentionTrigger, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	workerLabel = strings.TrimSpace(workerLabel)
	if workerLabel == "" || limit <= 0 || lease <= 0 {
		return nil, fmt.Errorf(
			"%w: worker label, positive limit, and positive lease are required",
			ErrInvalidPRDevelopmentAttentionTrigger,
		)
	}
	if limit > maxReviewListItems {
		limit = maxReviewListItems
	}
	now, err := s.currentTime()
	if err != nil {
		return nil, err
	}
	leaseUntil := now.Add(lease)
	if err = validateDBTimestamp(
		"pull request development attention trigger lease deadline",
		leaseUntil,
	); err != nil {
		return nil, err
	}

	claimed := make([]PRDevelopmentAttentionTrigger, 0, limit)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		ids, queryErr := queryIDs(ctx, conn, `
			SELECT review_entry_id
			FROM pr_development_attention_triggers
			WHERE (status = ? AND available_at <= ?)
			   OR (status = ? AND lease_until <= ?)
			ORDER BY created_at, review_entry_id
			LIMIT ?`,
			PRDevelopmentAttentionTriggerPending,
			toDBTime(now),
			PRDevelopmentAttentionTriggerClaimed,
			toDBTime(now),
			limit,
		)
		if queryErr != nil {
			return queryErr
		}
		for _, reviewEntryID := range ids {
			leaseToken, tokenErr := newLeaseToken(workerLabel)
			if tokenErr != nil {
				return tokenErr
			}
			result, updateErr := conn.ExecContext(ctx, `
				UPDATE pr_development_attention_triggers
				SET status = ?, owner = ?, lease_until = ?, attempts = attempts + 1,
				    updated_at = ?
				WHERE review_entry_id = ? AND
				      ((status = ? AND available_at <= ?) OR
				       (status = ? AND lease_until <= ?))`,
				PRDevelopmentAttentionTriggerClaimed,
				leaseToken,
				toDBTime(leaseUntil),
				toDBTime(now),
				reviewEntryID,
				PRDevelopmentAttentionTriggerPending,
				toDBTime(now),
				PRDevelopmentAttentionTriggerClaimed,
				toDBTime(now),
			)
			if updateErr != nil {
				return updateErr
			}
			affected, rowsErr := result.RowsAffected()
			if rowsErr != nil {
				return rowsErr
			}
			if affected != 1 {
				return errors.New("development attention trigger changed during claim")
			}
			trigger, scanErr := getPRDevelopmentAttentionTrigger(
				ctx,
				conn,
				reviewEntryID,
			)
			if scanErr != nil {
				return scanErr
			}
			claimed = append(claimed, trigger)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf(
			"claim pull request development attention triggers: %w",
			s.dbError(err),
		)
	}
	return claimed, nil
}

// RenewPRDevelopmentAttentionTriggerLease extends only the exact live claim.
func (s *Store) RenewPRDevelopmentAttentionTriggerLease(
	ctx context.Context,
	reviewEntryID string,
	leaseToken string,
	lease time.Duration,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	reviewEntryID = strings.TrimSpace(reviewEntryID)
	leaseToken = strings.TrimSpace(leaseToken)
	if !validPrefixedHexID(reviewEntryID, prDevelopmentLedgerEntryIDPrefix) ||
		leaseToken == "" || lease <= 0 {
		return fmt.Errorf(
			"%w: valid review entry ID, lease token, and positive lease are required",
			ErrInvalidPRDevelopmentAttentionTrigger,
		)
	}
	now, err := s.currentTime()
	if err != nil {
		return err
	}
	leaseUntil := now.Add(lease)
	if err = validateDBTimestamp(
		"pull request development attention trigger lease deadline",
		leaseUntil,
	); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE pr_development_attention_triggers
		SET lease_until = ?, updated_at = ?
		WHERE review_entry_id = ? AND status = ? AND owner = ?
		  AND lease_until > ?`,
		toDBTime(leaseUntil),
		toDBTime(now),
		reviewEntryID,
		PRDevelopmentAttentionTriggerClaimed,
		leaseToken,
		toDBTime(now),
	)
	if err != nil {
		return fmt.Errorf(
			"renew pull request development attention trigger lease: %w",
			s.dbError(err),
		)
	}
	return s.requirePRDevelopmentAttentionTriggerLeaseUpdate(
		ctx,
		result,
		reviewEntryID,
	)
}

// PinPRDevelopmentAttentionTriggerPolicy stores the exact canonical policy
// before subject projection, session capture, model execution, or run effect.
func (s *Store) PinPRDevelopmentAttentionTriggerPolicy(
	ctx context.Context,
	input PRDevelopmentAttentionPolicyPin,
) (PRDevelopmentAttentionTrigger, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentAttentionTrigger{}, err
	}
	input.ReviewEntryID = strings.TrimSpace(input.ReviewEntryID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	if !validPrefixedHexID(input.ReviewEntryID, prDevelopmentLedgerEntryIDPrefix) ||
		input.LeaseToken == "" ||
		!validReviewPolicyRevision(input.PolicyRevision) ||
		len(input.PinnedPolicy) == 0 ||
		len(input.PinnedPolicy) > maxPRDevelopmentAttentionPinnedPolicyBytes ||
		!json.Valid(input.PinnedPolicy) {
		return PRDevelopmentAttentionTrigger{}, fmt.Errorf(
			"%w: valid claim, policy revision, and bounded JSON policy are required",
			ErrInvalidPRDevelopmentAttentionTrigger,
		)
	}
	pinnedPolicy := cloneBytes(input.PinnedPolicy)
	var trigger PRDevelopmentAttentionTrigger
	err := s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		stored, getErr := getPRDevelopmentAttentionTrigger(
			ctx,
			conn,
			input.ReviewEntryID,
		)
		if getErr != nil {
			return getErr
		}
		if !livePRDevelopmentAttentionTriggerClaim(stored, input.LeaseToken, now) {
			return ErrStaleLease
		}
		if stored.PolicyRevision != "" || len(stored.PinnedPolicy) != 0 {
			if stored.PolicyRevision != input.PolicyRevision ||
				!bytes.Equal(stored.PinnedPolicy, pinnedPolicy) {
				return fmt.Errorf(
					"%w: attention trigger policy is already pinned",
					ErrPRDevelopmentAttentionTriggerConflict,
				)
			}
			trigger = stored
			return nil
		}
		current, loadErr := loadAnchoredPRDevelopmentAttentionSnapshot(
			ctx,
			conn,
			prDevelopmentAttentionOccurrenceAnchorForTrigger(stored),
		)
		if loadErr != nil {
			return loadErr
		}
		if current.HighWater != input.Snapshot {
			return fmt.Errorf(
				"%w: attention occurrence changed before policy pin",
				ErrPRDevelopmentAttentionTriggerConflict,
			)
		}
		if _, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_attention_triggers
			SET policy_revision = ?, pinned_policy_json = ?, updated_at = ?
			WHERE review_entry_id = ?`,
			input.PolicyRevision,
			pinnedPolicy,
			toDBTime(now),
			input.ReviewEntryID,
		); updateErr != nil {
			return updateErr
		}
		trigger, getErr = getPRDevelopmentAttentionTrigger(
			ctx,
			conn,
			input.ReviewEntryID,
		)
		return getErr
	})
	if err != nil {
		return PRDevelopmentAttentionTrigger{}, fmt.Errorf(
			"pin pull request development attention policy: %w",
			s.dbError(err),
		)
	}
	return trigger, nil
}

// PinPRDevelopmentAttentionTriggerSubject stores the active composition's
// exact semantic subject revision after policy pinning and before admission.
func (s *Store) PinPRDevelopmentAttentionTriggerSubject(
	ctx context.Context,
	input PRDevelopmentAttentionSubjectPin,
) (PRDevelopmentAttentionTrigger, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentAttentionTrigger{}, err
	}
	input.ReviewEntryID = strings.TrimSpace(input.ReviewEntryID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	if !validPrefixedHexID(input.ReviewEntryID, prDevelopmentLedgerEntryIDPrefix) ||
		input.LeaseToken == "" ||
		!validReviewPolicyRevision(input.PolicyRevision) ||
		!validReviewPolicyRevision(input.SubjectRevision) {
		return PRDevelopmentAttentionTrigger{}, fmt.Errorf(
			"%w: valid claim, policy revision, and subject revision are required",
			ErrInvalidPRDevelopmentAttentionTrigger,
		)
	}
	var trigger PRDevelopmentAttentionTrigger
	err := s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		stored, getErr := getPRDevelopmentAttentionTrigger(
			ctx,
			conn,
			input.ReviewEntryID,
		)
		if getErr != nil {
			return getErr
		}
		if !livePRDevelopmentAttentionTriggerClaim(stored, input.LeaseToken, now) {
			return ErrStaleLease
		}
		if stored.PolicyRevision == "" || len(stored.PinnedPolicy) == 0 ||
			stored.PolicyRevision != input.PolicyRevision {
			return fmt.Errorf(
				"%w: exact policy must be pinned before its subject",
				ErrPRDevelopmentAttentionTriggerConflict,
			)
		}
		if stored.SubjectRevision != "" {
			if stored.SubjectRevision != input.SubjectRevision {
				return fmt.Errorf(
					"%w: attention trigger subject is already pinned",
					ErrPRDevelopmentAttentionTriggerConflict,
				)
			}
			trigger = stored
			return nil
		}
		current, loadErr := loadAnchoredPRDevelopmentAttentionSnapshot(
			ctx,
			conn,
			prDevelopmentAttentionOccurrenceAnchorForTrigger(stored),
		)
		if loadErr != nil {
			return loadErr
		}
		if current.HighWater != input.Snapshot {
			return fmt.Errorf(
				"%w: attention occurrence changed before subject pin",
				ErrPRDevelopmentAttentionTriggerConflict,
			)
		}
		if _, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_attention_triggers
			SET subject_revision = ?, updated_at = ?
			WHERE review_entry_id = ?`,
			input.SubjectRevision,
			toDBTime(now),
			input.ReviewEntryID,
		); updateErr != nil {
			return updateErr
		}
		trigger, getErr = getPRDevelopmentAttentionTrigger(
			ctx,
			conn,
			input.ReviewEntryID,
		)
		return getErr
	})
	if err != nil {
		return PRDevelopmentAttentionTrigger{}, fmt.Errorf(
			"pin pull request development attention subject: %w",
			s.dbError(err),
		)
	}
	return trigger, nil
}

// ReleasePRDevelopmentAttentionTrigger returns a live claim to pending while
// preserving both immutable pins for an exact later retry.
//
//nolint:dupl // Separate schema-specific lease updates keep keys and statuses explicit.
func (s *Store) ReleasePRDevelopmentAttentionTrigger(
	ctx context.Context,
	input PRDevelopmentAttentionTriggerRelease,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	input.ReviewEntryID = strings.TrimSpace(input.ReviewEntryID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	if !validPrefixedHexID(input.ReviewEntryID, prDevelopmentLedgerEntryIDPrefix) ||
		input.LeaseToken == "" {
		return fmt.Errorf(
			"%w: valid review entry ID and lease token are required",
			ErrInvalidPRDevelopmentAttentionTrigger,
		)
	}
	now, err := s.currentTime()
	if err != nil {
		return err
	}
	if input.AvailableAt.IsZero() || input.AvailableAt.Before(now) {
		input.AvailableAt = now
	}
	input.AvailableAt = input.AvailableAt.UTC()
	if err = validateDBTimestamp(
		"pull request development attention trigger availability",
		input.AvailableAt,
	); err != nil {
		return err
	}
	lastError := s.sanitizeDetail(input.Error)
	result, err := s.db.ExecContext(ctx, `
		UPDATE pr_development_attention_triggers
		SET status = ?, owner = '', lease_until = NULL, available_at = ?,
		    last_error = ?, updated_at = ?
		WHERE review_entry_id = ? AND status = ? AND owner = ?
		  AND lease_until > ?`,
		PRDevelopmentAttentionTriggerPending,
		toDBTime(input.AvailableAt),
		lastError,
		toDBTime(now),
		input.ReviewEntryID,
		PRDevelopmentAttentionTriggerClaimed,
		input.LeaseToken,
		toDBTime(now),
	)
	if err != nil {
		return fmt.Errorf(
			"release pull request development attention trigger: %w",
			s.dbError(err),
		)
	}
	return s.requirePRDevelopmentAttentionTriggerLeaseUpdate(
		ctx,
		result,
		input.ReviewEntryID,
	)
}

// CompletePRDevelopmentAttentionTrigger records one terminal delivery,
// zero-gate decision, safe supersession, recovery boundary, or fixed failure.
func (s *Store) CompletePRDevelopmentAttentionTrigger(
	ctx context.Context,
	input PRDevelopmentAttentionTriggerCompletion,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	input.ReviewEntryID = strings.TrimSpace(input.ReviewEntryID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	input.RunID = strings.TrimSpace(input.RunID)
	if !validPrefixedHexID(input.ReviewEntryID, prDevelopmentLedgerEntryIDPrefix) ||
		input.LeaseToken == "" {
		return fmt.Errorf(
			"%w: valid review entry ID and lease token are required",
			ErrInvalidPRDevelopmentAttentionTrigger,
		)
	}
	switch input.Status {
	case PRDevelopmentAttentionTriggerDelivered:
		if !validPrefixedHexID(input.RunID, "wr_") {
			return fmt.Errorf(
				"%w: delivered trigger requires a valid workflow run ID",
				ErrInvalidPRDevelopmentAttentionTrigger,
			)
		}
	case PRDevelopmentAttentionTriggerNoop,
		PRDevelopmentAttentionTriggerSuperseded,
		PRDevelopmentAttentionTriggerRecoveryRequired,
		PRDevelopmentAttentionTriggerFailed:
		if input.RunID != "" {
			return fmt.Errorf(
				"%w: non-delivered trigger cannot carry a workflow run ID",
				ErrInvalidPRDevelopmentAttentionTrigger,
			)
		}
	default:
		return fmt.Errorf(
			"%w: trigger completion status is not terminal",
			ErrInvalidPRDevelopmentAttentionTrigger,
		)
	}

	lastError := s.sanitizeDetail(input.Error)
	err := s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		stored, getErr := getPRDevelopmentAttentionTrigger(
			ctx,
			conn,
			input.ReviewEntryID,
		)
		if getErr != nil {
			return getErr
		}
		if !livePRDevelopmentAttentionTriggerClaim(stored, input.LeaseToken, now) {
			return ErrStaleLease
		}
		switch input.Status {
		case PRDevelopmentAttentionTriggerDelivered:
			if stored.PolicyRevision == "" || len(stored.PinnedPolicy) == 0 ||
				stored.SubjectRevision == "" {
				return fmt.Errorf(
					"%w: delivered trigger requires policy and subject pins",
					ErrInvalidTransition,
				)
			}
			key := prDevelopmentAttentionDecisionKeyForTrigger(stored)
			link, linkErr := getPRDevelopmentAttentionDecisionRun(ctx, conn, key)
			if linkErr != nil {
				return linkErr
			}
			if link.Key != key || link.RunID != input.RunID {
				return fmt.Errorf(
					"%w: delivered workflow run does not match the pinned trigger",
					ErrPRDevelopmentAttentionTriggerConflict,
				)
			}
			if validateErr := validateHistoricalPRDevelopmentAttentionDecisionRun(
				ctx,
				conn,
				link,
			); validateErr != nil {
				return validateErr
			}
		case PRDevelopmentAttentionTriggerNoop:
			if stored.PolicyRevision == "" || len(stored.PinnedPolicy) == 0 ||
				stored.SubjectRevision != "" {
				return fmt.Errorf(
					"%w: no-op trigger requires a policy-only pin",
					ErrInvalidTransition,
				)
			}
		case PRDevelopmentAttentionTriggerRecoveryRequired:
			if stored.PolicyRevision == "" || len(stored.PinnedPolicy) == 0 ||
				stored.SubjectRevision == "" {
				return fmt.Errorf(
					"%w: recovery-required trigger needs exact decision pins",
					ErrInvalidTransition,
				)
			}
		}
		_, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_attention_triggers
			SET status = ?, owner = '', lease_until = NULL, run_id = ?,
			    last_error = ?, updated_at = ?, completed_at = ?
			WHERE review_entry_id = ?`,
			input.Status,
			input.RunID,
			lastError,
			toDBTime(now),
			toDBTime(now),
			input.ReviewEntryID,
		)
		return updateErr
	})
	if err != nil {
		return fmt.Errorf(
			"complete pull request development attention trigger: %w",
			s.dbError(err),
		)
	}
	return nil
}

func enqueuePRDevelopmentAttentionTrigger(
	ctx context.Context,
	conn *sql.Conn,
	entry PRDevelopmentLedgerEntry,
	conversation loadedPRDevelopmentConversation,
	now time.Time,
) error {
	if entry.Kind != PRDevelopmentLedgerReview ||
		entry.ReviewOutcome != PRDevelopmentLedgerReviewAttentionRequired ||
		!validPrefixedHexID(entry.ID, prDevelopmentLedgerEntryIDPrefix) ||
		!validPrefixedHexID(entry.CaseID, prDevelopmentCaseIDPrefix) ||
		!validPRDevelopmentHex(entry.EntryHash, 64) ||
		conversation.Conversation.CaseID != entry.CaseID ||
		conversation.Conversation.Version < 0 ||
		conversation.Conversation.Version > MaxPRDevelopmentMessagesPerCase ||
		!validPRDevelopmentHex(conversation.TranscriptDigest, 64) {
		return errors.New("stored development attention occurrence is invalid")
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO pr_development_attention_triggers (
			review_entry_id, review_entry_hash, case_id, conversation_version,
			transcript_digest, decision_point, status, available_at,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID,
		entry.EntryHash,
		entry.CaseID,
		conversation.Conversation.Version,
		conversation.TranscriptDigest,
		PRDevelopmentAttentionDecisionReviewRequired,
		PRDevelopmentAttentionTriggerPending,
		toDBTime(now),
		toDBTime(now),
		toDBTime(now),
	)
	return err
}

func getPRDevelopmentAttentionTrigger(
	ctx context.Context,
	queryer rowQueryer,
	reviewEntryID string,
) (PRDevelopmentAttentionTrigger, error) {
	return scanPRDevelopmentAttentionTrigger(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentAttentionTriggerColumns+`
		FROM pr_development_attention_triggers
		WHERE review_entry_id = ?`,
		reviewEntryID,
	))
}

func getLatestPRDevelopmentAttentionTriggerForCase(
	ctx context.Context,
	queryer rowQueryer,
	caseID string,
) (PRDevelopmentAttentionTrigger, error) {
	return scanPRDevelopmentAttentionTrigger(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentAttentionTriggerColumns+`
		FROM pr_development_attention_triggers AS trigger
		WHERE trigger.review_entry_id = (
			SELECT entry.id
			FROM pr_development_attention_triggers AS candidate
			JOIN pr_development_ledger_entries AS entry
			  ON entry.id = candidate.review_entry_id
			 AND entry.kind = 'review'
			 AND entry.entry_hash = candidate.review_entry_hash
			WHERE candidate.case_id = ?
			ORDER BY entry.ordinal DESC, candidate.review_entry_id DESC
			LIMIT 1
		)`,
		caseID,
	))
}

func findPRDevelopmentAttentionTriggerEntry(
	ledger PRDevelopmentLedger,
	trigger PRDevelopmentAttentionTrigger,
) (PRDevelopmentLedgerEntry, bool) {
	for _, entry := range ledger.Entries {
		if entry.ID != trigger.ReviewEntryID {
			continue
		}
		if entry.EntryHash != trigger.ReviewEntryHash ||
			entry.CaseID != trigger.CaseID ||
			entry.Kind != PRDevelopmentLedgerReview ||
			entry.ReviewOutcome != PRDevelopmentLedgerReviewAttentionRequired {
			return PRDevelopmentLedgerEntry{}, false
		}
		return entry, true
	}
	return PRDevelopmentLedgerEntry{}, false
}

func scanPRDevelopmentAttentionTrigger(
	scanner rowScanner,
) (PRDevelopmentAttentionTrigger, error) {
	var (
		trigger                           PRDevelopmentAttentionTrigger
		attempts                          int64
		leaseUntil, completedAt           sql.NullInt64
		availableAt, createdAt, updatedAt int64
		pinnedPolicy                      []byte
	)
	if err := scanner.Scan(
		&trigger.ReviewEntryID,
		&trigger.ReviewEntryHash,
		&trigger.CaseID,
		&trigger.ConversationVersion,
		&trigger.TranscriptDigest,
		&trigger.DecisionPoint,
		&trigger.Status,
		&trigger.LeaseToken,
		&leaseUntil,
		&attempts,
		&availableAt,
		&trigger.PolicyRevision,
		&pinnedPolicy,
		&trigger.SubjectRevision,
		&trigger.RunID,
		&trigger.LastError,
		&createdAt,
		&updatedAt,
		&completedAt,
	); err != nil {
		return PRDevelopmentAttentionTrigger{}, err
	}
	if !validPrefixedHexID(trigger.ReviewEntryID, prDevelopmentLedgerEntryIDPrefix) ||
		!validPRDevelopmentHex(trigger.ReviewEntryHash, 64) ||
		!validPrefixedHexID(trigger.CaseID, prDevelopmentCaseIDPrefix) ||
		trigger.ConversationVersion < 0 ||
		trigger.ConversationVersion > MaxPRDevelopmentMessagesPerCase ||
		!validPRDevelopmentHex(trigger.TranscriptDigest, 64) ||
		trigger.DecisionPoint != PRDevelopmentAttentionDecisionReviewRequired ||
		attempts < 0 || len(trigger.LastError) > maxErrorDetailBytes ||
		!utf8.ValidString(trigger.LastError) {
		return PRDevelopmentAttentionTrigger{}, errors.New(
			"stored development attention trigger is invalid",
		)
	}
	if trigger.PolicyRevision == "" {
		if len(pinnedPolicy) != 0 || trigger.SubjectRevision != "" {
			return PRDevelopmentAttentionTrigger{}, errors.New(
				"stored development attention trigger pin is invalid",
			)
		}
	} else if !validReviewPolicyRevision(trigger.PolicyRevision) ||
		len(pinnedPolicy) == 0 ||
		len(pinnedPolicy) > maxPRDevelopmentAttentionPinnedPolicyBytes ||
		!json.Valid(pinnedPolicy) ||
		(trigger.SubjectRevision != "" &&
			!validReviewPolicyRevision(trigger.SubjectRevision)) {
		return PRDevelopmentAttentionTrigger{}, errors.New(
			"stored development attention trigger pin is invalid",
		)
	}

	switch trigger.Status {
	case PRDevelopmentAttentionTriggerPending:
		if trigger.LeaseToken != "" || leaseUntil.Valid || completedAt.Valid ||
			trigger.RunID != "" {
			return PRDevelopmentAttentionTrigger{}, errors.New(
				"stored pending development attention trigger is invalid",
			)
		}
	case PRDevelopmentAttentionTriggerClaimed:
		if trigger.LeaseToken == "" || !leaseUntil.Valid || completedAt.Valid ||
			trigger.RunID != "" {
			return PRDevelopmentAttentionTrigger{}, errors.New(
				"stored claimed development attention trigger is invalid",
			)
		}
	case PRDevelopmentAttentionTriggerDelivered:
		if trigger.LeaseToken != "" || leaseUntil.Valid || !completedAt.Valid ||
			!validPrefixedHexID(trigger.RunID, "wr_") ||
			trigger.PolicyRevision == "" || trigger.SubjectRevision == "" {
			return PRDevelopmentAttentionTrigger{}, errors.New(
				"stored delivered development attention trigger is invalid",
			)
		}
	case PRDevelopmentAttentionTriggerNoop:
		if trigger.LeaseToken != "" || leaseUntil.Valid || !completedAt.Valid ||
			trigger.RunID != "" || trigger.PolicyRevision == "" ||
			trigger.SubjectRevision != "" {
			return PRDevelopmentAttentionTrigger{}, errors.New(
				"stored no-op development attention trigger is invalid",
			)
		}
	case PRDevelopmentAttentionTriggerRecoveryRequired:
		if trigger.LeaseToken != "" || leaseUntil.Valid || !completedAt.Valid ||
			trigger.RunID != "" || trigger.PolicyRevision == "" ||
			trigger.SubjectRevision == "" {
			return PRDevelopmentAttentionTrigger{}, errors.New(
				"stored recovery-required development attention trigger is invalid",
			)
		}
	case PRDevelopmentAttentionTriggerSuperseded,
		PRDevelopmentAttentionTriggerFailed:
		if trigger.LeaseToken != "" || leaseUntil.Valid || !completedAt.Valid ||
			trigger.RunID != "" {
			return PRDevelopmentAttentionTrigger{}, errors.New(
				"stored terminal development attention trigger is invalid",
			)
		}
	default:
		return PRDevelopmentAttentionTrigger{}, errors.New(
			"stored development attention trigger status is invalid",
		)
	}

	convertedAttempts, err := reviewDBInt(attempts)
	if err != nil {
		return PRDevelopmentAttentionTrigger{}, err
	}
	trigger.Attempts = convertedAttempts
	trigger.PinnedPolicy = cloneBytes(pinnedPolicy)
	trigger.LeaseUntil = fromNullableTime(leaseUntil)
	trigger.CompletedAt = fromNullableTime(completedAt)
	trigger.AvailableAt = fromDBTime(availableAt)
	trigger.CreatedAt = fromDBTime(createdAt)
	trigger.UpdatedAt = fromDBTime(updatedAt)
	if validateDBTimestamp("attention trigger availability", trigger.AvailableAt) != nil ||
		validateDBTimestamp("attention trigger creation time", trigger.CreatedAt) != nil ||
		validateDBTimestamp("attention trigger update time", trigger.UpdatedAt) != nil {
		return PRDevelopmentAttentionTrigger{}, errors.New(
			"stored development attention trigger timestamp is invalid",
		)
	}
	return trigger, nil
}

func livePRDevelopmentAttentionTriggerClaim(
	trigger PRDevelopmentAttentionTrigger,
	leaseToken string,
	now time.Time,
) bool {
	return trigger.Status == PRDevelopmentAttentionTriggerClaimed &&
		trigger.LeaseToken == leaseToken && trigger.LeaseUntil != nil &&
		trigger.LeaseUntil.After(now)
}

func prDevelopmentAttentionOccurrenceAnchorForTrigger(
	trigger PRDevelopmentAttentionTrigger,
) prDevelopmentAttentionOccurrenceAnchor {
	return prDevelopmentAttentionOccurrenceAnchor{
		CaseID:              trigger.CaseID,
		ReviewEntryID:       trigger.ReviewEntryID,
		ReviewEntryHash:     trigger.ReviewEntryHash,
		ConversationVersion: trigger.ConversationVersion,
		TranscriptDigest:    trigger.TranscriptDigest,
	}
}

func prDevelopmentAttentionDecisionKeyForTrigger(
	trigger PRDevelopmentAttentionTrigger,
) PRDevelopmentAttentionDecisionKey {
	return PRDevelopmentAttentionDecisionKey{
		CaseID:              trigger.CaseID,
		ReviewEntryID:       trigger.ReviewEntryID,
		ReviewEntryHash:     trigger.ReviewEntryHash,
		ConversationVersion: trigger.ConversationVersion,
		SubjectRevision:     trigger.SubjectRevision,
		DecisionPoint:       trigger.DecisionPoint,
		PolicyRevision:      trigger.PolicyRevision,
	}
}

func (s *Store) requirePRDevelopmentAttentionTriggerLeaseUpdate(
	ctx context.Context,
	result sql.Result,
	reviewEntryID string,
) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	var exists int
	err = s.db.QueryRowContext(ctx, `
		SELECT 1 FROM pr_development_attention_triggers WHERE review_entry_id = ?`,
		reviewEntryID,
	).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return s.dbError(err)
	}
	return ErrStaleLease
}
