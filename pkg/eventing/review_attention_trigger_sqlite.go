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

const (
	maxReviewAttentionPolicyBytes = 3 << 20

	schemaV5ReviewAttentionTriggersTable = `CREATE TABLE IF NOT EXISTS pr_review_attention_triggers (
	submission_id TEXT PRIMARY KEY REFERENCES pr_review_submissions(id) ON DELETE RESTRICT,
	case_id TEXT NOT NULL REFERENCES pr_review_cases(id) ON DELETE RESTRICT,
	case_version INTEGER NOT NULL CHECK (case_version >= 1),
	decision_point TEXT NOT NULL CHECK (decision_point <> ''),
	status TEXT NOT NULL CHECK (status IN ('pending', 'claimed', 'delivered', 'noop')),
	owner TEXT NOT NULL DEFAULT '',
	lease_until INTEGER,
	attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
	available_at INTEGER NOT NULL,
	policy_revision TEXT NOT NULL DEFAULT '',
	pinned_policy_json BLOB NOT NULL DEFAULT X'',
	run_id TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	completed_at INTEGER,
	UNIQUE(case_id, case_version, decision_point),
	CHECK ((policy_revision = '' AND length(pinned_policy_json) = 0) OR
	       (policy_revision <> '' AND length(pinned_policy_json) > 0)),
	CHECK (typeof(pinned_policy_json) = 'blob' AND length(pinned_policy_json) <= 3145728),
	CHECK (typeof(last_error) = 'text' AND length(CAST(last_error AS BLOB)) <= 16384),
	CHECK ((status = 'claimed' AND owner <> '' AND lease_until IS NOT NULL) OR
	       (status <> 'claimed' AND owner = '' AND lease_until IS NULL)),
	CHECK ((status IN ('delivered', 'noop') AND completed_at IS NOT NULL) OR
	       (status IN ('pending', 'claimed') AND completed_at IS NULL)),
	CHECK (status IN ('pending', 'claimed') OR policy_revision <> ''),
	CHECK ((status = 'delivered' AND run_id <> '') OR
	       (status <> 'delivered' AND run_id = ''))
);`
	schemaV5ReviewAttentionTriggersClaimIndex = `CREATE INDEX IF NOT EXISTS pr_review_attention_triggers_claim
	ON pr_review_attention_triggers(status, available_at, lease_until, created_at, submission_id);`
	schemaV5 = schemaV5ReviewAttentionTriggersTable + "\n" +
		schemaV5ReviewAttentionTriggersClaimIndex
)

var _ ReviewAttentionTriggerQueue = (*Store)(nil)

const reviewAttentionTriggerColumns = `
	submission_id, case_id, case_version, decision_point, status, owner,
	lease_until, attempts, available_at, policy_revision, pinned_policy_json,
	run_id, last_error, created_at, updated_at, completed_at`

func validateSchemaV5(ctx context.Context, conn *sql.Conn) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_review_attention_triggers",
		createSQL: schemaV5ReviewAttentionTriggersTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("submission_id")}},
			{
				origin: "u",
				columns: []schemaIndexColumn{
					binary("case_id"),
					binary("case_version"),
					binary("decision_point"),
				},
			},
		},
	}); err != nil {
		return err
	}
	return validateSchemaIndex(ctx, conn, schemaIndexSpec{
		name:      "pr_review_attention_triggers_claim",
		createSQL: schemaV5ReviewAttentionTriggersClaimIndex,
	})
}

// GetReviewAttentionTrigger retrieves one automatic submitted-review
// occurrence by its immutable submission identity.
func (s *Store) GetReviewAttentionTrigger(
	ctx context.Context,
	submissionID string,
) (ReviewAttentionTrigger, error) {
	if err := s.ready(ctx); err != nil {
		return ReviewAttentionTrigger{}, err
	}
	submissionID = strings.TrimSpace(submissionID)
	if !validPrefixedHexID(submissionID, reviewSubmissionIDPrefix) {
		return ReviewAttentionTrigger{}, fmt.Errorf(
			"%w: invalid review submission ID",
			ErrInvalidReview,
		)
	}
	trigger, err := scanReviewAttentionTrigger(s.db.QueryRowContext(ctx, `
		SELECT `+reviewAttentionTriggerColumns+`
		FROM pr_review_attention_triggers
		WHERE submission_id = ?`,
		submissionID,
	))
	if err != nil {
		return ReviewAttentionTrigger{}, s.dbError(err)
	}
	return trigger, nil
}

// ClaimReviewAttentionTriggers leases pending or expired automatic attention
// work. An expired launch claim is safe to retry because its exact policy is
// pinned before any external launch and launch admission is deterministic.
func (s *Store) ClaimReviewAttentionTriggers(
	ctx context.Context,
	workerLabel string,
	limit int,
	lease time.Duration,
) ([]ReviewAttentionTrigger, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	workerLabel = strings.TrimSpace(workerLabel)
	if workerLabel == "" || limit <= 0 || lease <= 0 {
		return nil, fmt.Errorf(
			"worker label, positive limit, and positive lease are required",
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
	if validationErr := validateDBTimestamp(
		"review attention trigger lease deadline",
		leaseUntil,
	); validationErr != nil {
		return nil, validationErr
	}

	claimed := make([]ReviewAttentionTrigger, 0, limit)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		ids, queryErr := queryIDs(ctx, conn, `
			SELECT submission_id
			FROM pr_review_attention_triggers
			WHERE (status = ? AND available_at <= ?)
			   OR (status = ? AND lease_until <= ?)
			ORDER BY created_at, submission_id
			LIMIT ?`,
			ReviewAttentionPending,
			toDBTime(now),
			ReviewAttentionClaimed,
			toDBTime(now),
			limit,
		)
		if queryErr != nil {
			return queryErr
		}
		for _, submissionID := range ids {
			leaseToken, tokenErr := newLeaseToken(workerLabel)
			if tokenErr != nil {
				return tokenErr
			}
			result, updateErr := conn.ExecContext(ctx, `
				UPDATE pr_review_attention_triggers
				SET status = ?, owner = ?, lease_until = ?,
				    attempts = attempts + 1, updated_at = ?
				WHERE submission_id = ? AND
				      ((status = ? AND available_at <= ?) OR
				       (status = ? AND lease_until <= ?))`,
				ReviewAttentionClaimed,
				leaseToken,
				toDBTime(leaseUntil),
				toDBTime(now),
				submissionID,
				ReviewAttentionPending,
				toDBTime(now),
				ReviewAttentionClaimed,
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
				return fmt.Errorf("review attention trigger changed during claim")
			}
			trigger, scanErr := scanReviewAttentionTrigger(conn.QueryRowContext(ctx, `
				SELECT `+reviewAttentionTriggerColumns+`
				FROM pr_review_attention_triggers
				WHERE submission_id = ?`,
				submissionID,
			))
			if scanErr != nil {
				return scanErr
			}
			claimed = append(claimed, trigger)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf(
			"claim pull request review attention triggers: %w",
			s.dbError(err),
		)
	}
	return claimed, nil
}

// RenewReviewAttentionTriggerLease extends a live owned launch claim.
func (s *Store) RenewReviewAttentionTriggerLease(
	ctx context.Context,
	submissionID, leaseToken string,
	lease time.Duration,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	submissionID = strings.TrimSpace(submissionID)
	leaseToken = strings.TrimSpace(leaseToken)
	if !validPrefixedHexID(submissionID, reviewSubmissionIDPrefix) ||
		leaseToken == "" || lease <= 0 {
		return fmt.Errorf(
			"%w: valid submission ID, lease token, and positive lease are required",
			ErrInvalidReview,
		)
	}
	now, err := s.currentTime()
	if err != nil {
		return err
	}
	leaseUntil := now.Add(lease)
	if validationErr := validateDBTimestamp(
		"review attention trigger lease deadline",
		leaseUntil,
	); validationErr != nil {
		return validationErr
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE pr_review_attention_triggers
		SET lease_until = ?, updated_at = ?
		WHERE submission_id = ? AND status = ? AND owner = ?
		  AND lease_until > ?`,
		toDBTime(leaseUntil),
		toDBTime(now),
		submissionID,
		ReviewAttentionClaimed,
		leaseToken,
		toDBTime(now),
	)
	if err != nil {
		return fmt.Errorf("renew review attention trigger lease: %w", s.dbError(err))
	}
	return s.requireReviewAttentionLeaseUpdate(ctx, result, submissionID)
}

// PinReviewAttentionTriggerPolicy immutably stores the exact canonical policy
// envelope under a live claim. Repeating the same exact pin is idempotent;
// attempting to change an existing pin is a conflict.
func (s *Store) PinReviewAttentionTriggerPolicy(
	ctx context.Context,
	input ReviewAttentionPolicyPin,
) (ReviewAttentionTrigger, error) {
	if err := s.ready(ctx); err != nil {
		return ReviewAttentionTrigger{}, err
	}
	input.SubmissionID = strings.TrimSpace(input.SubmissionID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	if !validPrefixedHexID(input.SubmissionID, reviewSubmissionIDPrefix) ||
		input.LeaseToken == "" ||
		!validReviewPolicyRevision(input.PolicyRevision) ||
		len(input.PinnedPolicy) == 0 ||
		len(input.PinnedPolicy) > maxReviewAttentionPolicyBytes ||
		!json.Valid(input.PinnedPolicy) {
		return ReviewAttentionTrigger{}, fmt.Errorf(
			"%w: valid submission, lease, policy revision, and bounded JSON policy are required",
			ErrInvalidReview,
		)
	}
	pinnedPolicy := cloneBytes(input.PinnedPolicy)
	var trigger ReviewAttentionTrigger
	err := s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		stored, getErr := scanReviewAttentionTrigger(conn.QueryRowContext(ctx, `
			SELECT `+reviewAttentionTriggerColumns+`
			FROM pr_review_attention_triggers
			WHERE submission_id = ?`,
			input.SubmissionID,
		))
		if getErr != nil {
			return getErr
		}
		if stored.Status != ReviewAttentionClaimed ||
			stored.LeaseToken != input.LeaseToken ||
			stored.LeaseUntil == nil || !stored.LeaseUntil.After(now) {
			return ErrStaleLease
		}
		if stored.PolicyRevision != "" {
			if stored.PolicyRevision != input.PolicyRevision ||
				!bytes.Equal(stored.PinnedPolicy, pinnedPolicy) {
				return fmt.Errorf(
					"%w: review attention policy is already pinned",
					ErrReviewConflict,
				)
			}
			trigger = stored
			return nil
		}
		if _, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_review_attention_triggers
			SET policy_revision = ?, pinned_policy_json = ?, updated_at = ?
			WHERE submission_id = ?`,
			input.PolicyRevision,
			pinnedPolicy,
			toDBTime(now),
			input.SubmissionID,
		); updateErr != nil {
			return updateErr
		}
		trigger, getErr = scanReviewAttentionTrigger(conn.QueryRowContext(ctx, `
			SELECT `+reviewAttentionTriggerColumns+`
			FROM pr_review_attention_triggers
			WHERE submission_id = ?`,
			input.SubmissionID,
		))
		return getErr
	})
	if err != nil {
		return ReviewAttentionTrigger{}, fmt.Errorf(
			"pin pull request review attention policy: %w",
			s.dbError(err),
		)
	}
	return trigger, nil
}

// ReleaseReviewAttentionTrigger returns a live owned claim to pending while
// retaining any immutable policy pin for an exact retry.
//
//nolint:dupl // Separate schema-specific lease updates keep keys and statuses explicit.
func (s *Store) ReleaseReviewAttentionTrigger(
	ctx context.Context,
	input ReviewAttentionTriggerRelease,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	input.SubmissionID = strings.TrimSpace(input.SubmissionID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	if !validPrefixedHexID(input.SubmissionID, reviewSubmissionIDPrefix) ||
		input.LeaseToken == "" {
		return fmt.Errorf(
			"%w: valid submission ID and lease token are required",
			ErrInvalidReview,
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
	if validationErr := validateDBTimestamp(
		"review attention trigger availability",
		input.AvailableAt,
	); validationErr != nil {
		return validationErr
	}
	lastError := s.sanitizeDetail(input.Error)
	result, err := s.db.ExecContext(ctx, `
		UPDATE pr_review_attention_triggers
		SET status = ?, owner = '', lease_until = NULL, available_at = ?,
		    last_error = ?, updated_at = ?
		WHERE submission_id = ? AND status = ? AND owner = ?
		  AND lease_until > ?`,
		ReviewAttentionPending,
		toDBTime(input.AvailableAt),
		lastError,
		toDBTime(now),
		input.SubmissionID,
		ReviewAttentionClaimed,
		input.LeaseToken,
		toDBTime(now),
	)
	if err != nil {
		return fmt.Errorf("release review attention trigger: %w", s.dbError(err))
	}
	return s.requireReviewAttentionLeaseUpdate(ctx, result, input.SubmissionID)
}

// CompleteReviewAttentionTrigger records a delivered workflow run or a
// policy-confirmed no-op under a live owned claim.
func (s *Store) CompleteReviewAttentionTrigger(
	ctx context.Context,
	input ReviewAttentionTriggerCompletion,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	input.SubmissionID = strings.TrimSpace(input.SubmissionID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	input.RunID = strings.TrimSpace(input.RunID)
	if !validPrefixedHexID(input.SubmissionID, reviewSubmissionIDPrefix) ||
		input.LeaseToken == "" {
		return fmt.Errorf(
			"%w: valid submission ID and lease token are required",
			ErrInvalidReview,
		)
	}
	switch input.Status {
	case ReviewAttentionDelivered:
		if !validPrefixedHexID(input.RunID, "wr_") {
			return fmt.Errorf(
				"%w: delivered attention trigger requires a valid workflow run ID",
				ErrInvalidReview,
			)
		}
	case ReviewAttentionNoop:
		if input.RunID != "" {
			return fmt.Errorf(
				"%w: no-op attention trigger cannot have a workflow run ID",
				ErrInvalidReview,
			)
		}
	default:
		return fmt.Errorf(
			"%w: attention completion status must be delivered or noop",
			ErrInvalidTransition,
		)
	}
	err := s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		stored, getErr := scanReviewAttentionTrigger(conn.QueryRowContext(ctx, `
			SELECT `+reviewAttentionTriggerColumns+`
			FROM pr_review_attention_triggers
			WHERE submission_id = ?`,
			input.SubmissionID,
		))
		if getErr != nil {
			return getErr
		}
		if stored.Status != ReviewAttentionClaimed ||
			stored.LeaseToken != input.LeaseToken ||
			stored.LeaseUntil == nil || !stored.LeaseUntil.After(now) {
			return ErrStaleLease
		}
		if stored.PolicyRevision == "" || len(stored.PinnedPolicy) == 0 {
			return fmt.Errorf(
				"%w: review attention policy must be pinned before completion",
				ErrInvalidTransition,
			)
		}
		_, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_review_attention_triggers
			SET status = ?, owner = '', lease_until = NULL, run_id = ?,
			    last_error = '', updated_at = ?, completed_at = ?
			WHERE submission_id = ?`,
			input.Status,
			input.RunID,
			toDBTime(now),
			toDBTime(now),
			input.SubmissionID,
		)
		return updateErr
	})
	if err != nil {
		return fmt.Errorf("complete review attention trigger: %w", s.dbError(err))
	}
	return nil
}

func enqueueReviewAttentionTrigger(
	ctx context.Context,
	conn *sql.Conn,
	submissionID, caseID string,
	caseVersion int64,
	now time.Time,
) error {
	if !validPrefixedHexID(submissionID, reviewSubmissionIDPrefix) ||
		!validPrefixedHexID(caseID, reviewCaseIDPrefix) || caseVersion < 1 {
		return fmt.Errorf("stored submitted review occurrence is invalid")
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO pr_review_attention_triggers (
			submission_id, case_id, case_version, decision_point, status,
			available_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		submissionID,
		caseID,
		caseVersion,
		ReviewAttentionDecisionSubmitted,
		ReviewAttentionPending,
		toDBTime(now),
		toDBTime(now),
		toDBTime(now),
	)
	return err
}

func scanReviewAttentionTrigger(scanner rowScanner) (ReviewAttentionTrigger, error) {
	var (
		trigger                           ReviewAttentionTrigger
		attempts                          int64
		leaseUntil, completedAt           sql.NullInt64
		availableAt, createdAt, updatedAt int64
		pinnedPolicy                      []byte
	)
	if err := scanner.Scan(
		&trigger.SubmissionID,
		&trigger.CaseID,
		&trigger.CaseVersion,
		&trigger.DecisionPoint,
		&trigger.Status,
		&trigger.LeaseToken,
		&leaseUntil,
		&attempts,
		&availableAt,
		&trigger.PolicyRevision,
		&pinnedPolicy,
		&trigger.RunID,
		&trigger.LastError,
		&createdAt,
		&updatedAt,
		&completedAt,
	); err != nil {
		return ReviewAttentionTrigger{}, err
	}
	if !validPrefixedHexID(trigger.SubmissionID, reviewSubmissionIDPrefix) ||
		!validPrefixedHexID(trigger.CaseID, reviewCaseIDPrefix) ||
		trigger.CaseVersion < 1 ||
		!validReviewDecisionPoint(trigger.DecisionPoint) ||
		attempts < 0 || len(trigger.LastError) > maxErrorDetailBytes ||
		!utf8.ValidString(trigger.LastError) {
		return ReviewAttentionTrigger{}, fmt.Errorf("stored review attention trigger is invalid")
	}
	switch trigger.Status {
	case ReviewAttentionPending:
		if trigger.LeaseToken != "" || leaseUntil.Valid || completedAt.Valid || trigger.RunID != "" {
			return ReviewAttentionTrigger{}, fmt.Errorf("stored pending review attention trigger is invalid")
		}
	case ReviewAttentionClaimed:
		if trigger.LeaseToken == "" || !leaseUntil.Valid || completedAt.Valid || trigger.RunID != "" {
			return ReviewAttentionTrigger{}, fmt.Errorf("stored claimed review attention trigger is invalid")
		}
	case ReviewAttentionDelivered:
		if trigger.LeaseToken != "" || leaseUntil.Valid || !completedAt.Valid ||
			!validPrefixedHexID(trigger.RunID, "wr_") {
			return ReviewAttentionTrigger{}, fmt.Errorf("stored delivered review attention trigger is invalid")
		}
	case ReviewAttentionNoop:
		if trigger.LeaseToken != "" || leaseUntil.Valid || !completedAt.Valid || trigger.RunID != "" {
			return ReviewAttentionTrigger{}, fmt.Errorf("stored no-op review attention trigger is invalid")
		}
	default:
		return ReviewAttentionTrigger{}, fmt.Errorf("stored review attention trigger status is invalid")
	}
	if trigger.PolicyRevision == "" {
		if len(pinnedPolicy) != 0 {
			return ReviewAttentionTrigger{}, fmt.Errorf("stored review attention policy pin is invalid")
		}
	} else if !validReviewPolicyRevision(trigger.PolicyRevision) ||
		len(pinnedPolicy) == 0 || len(pinnedPolicy) > maxReviewAttentionPolicyBytes ||
		!json.Valid(pinnedPolicy) {
		return ReviewAttentionTrigger{}, fmt.Errorf("stored review attention policy pin is invalid")
	}
	if (trigger.Status == ReviewAttentionDelivered || trigger.Status == ReviewAttentionNoop) &&
		trigger.PolicyRevision == "" {
		return ReviewAttentionTrigger{}, fmt.Errorf("stored terminal review attention trigger has no policy pin")
	}
	trigger.PinnedPolicy = cloneBytes(pinnedPolicy)
	convertedAttempts, err := reviewDBInt(attempts)
	if err != nil {
		return ReviewAttentionTrigger{}, err
	}
	trigger.Attempts = convertedAttempts
	trigger.LeaseUntil = fromNullableTime(leaseUntil)
	trigger.CompletedAt = fromNullableTime(completedAt)
	trigger.AvailableAt = fromDBTime(availableAt)
	trigger.CreatedAt = fromDBTime(createdAt)
	trigger.UpdatedAt = fromDBTime(updatedAt)
	return trigger, nil
}

func (s *Store) requireReviewAttentionLeaseUpdate(
	ctx context.Context,
	result sql.Result,
	submissionID string,
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
		SELECT 1 FROM pr_review_attention_triggers WHERE submission_id = ?`,
		submissionID,
	).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return s.dbError(err)
	}
	return ErrStaleLease
}
