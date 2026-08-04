//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	maxReviewDecisionPointBytes = 128
	reviewPolicyRevisionPrefix  = "sha256:"

	schemaV4ReviewDecisionRunsTable = `CREATE TABLE IF NOT EXISTS pr_review_decision_runs (
	case_id TEXT NOT NULL REFERENCES pr_review_cases(id) ON DELETE RESTRICT,
	case_version INTEGER NOT NULL CHECK (case_version >= 1),
	decision_point TEXT NOT NULL CHECK (decision_point <> ''),
	policy_revision TEXT NOT NULL CHECK (policy_revision <> ''),
	run_id TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL,
	PRIMARY KEY(case_id, case_version, decision_point, policy_revision)
);`
	schemaV4 = schemaV4ReviewDecisionRunsTable
)

var _ ReviewDecisionRunStore = (*Store)(nil)

func validateSchemaV4(ctx context.Context, conn *sql.Conn) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	return validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_review_decision_runs",
		createSQL: schemaV4ReviewDecisionRunsTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{
				origin: "pk",
				columns: []schemaIndexColumn{
					binary("case_id"),
					binary("case_version"),
					binary("decision_point"),
					binary("policy_revision"),
				},
			},
			{origin: "u", columns: []schemaIndexColumn{binary("run_id")}},
		},
	})
}

// GetReviewDecisionRun returns the durable workflow run bound to one exact
// review decision.
func (s *Store) GetReviewDecisionRun(
	ctx context.Context,
	key ReviewDecisionKey,
) (ReviewDecisionRunLink, error) {
	if err := s.ready(ctx); err != nil {
		return ReviewDecisionRunLink{}, err
	}
	normalized, err := normalizeReviewDecisionKey(key)
	if err != nil {
		return ReviewDecisionRunLink{}, err
	}
	link, err := getReviewDecisionRun(ctx, s.db, normalized)
	if err != nil {
		return ReviewDecisionRunLink{}, s.dbError(err)
	}
	return link, nil
}

// AdmitReviewDecisionRun invokes create at most once for an exact committed
// decision key. The proposed link is inserted before create and both are
// guarded by BEGIN IMMEDIATE; a create error rolls the link back. If create
// succeeds but COMMIT fails, the returned uncertainty error prevents callers
// from blindly repeating an external side effect.
func (s *Store) AdmitReviewDecisionRun(
	ctx context.Context,
	admission ReviewDecisionRunAdmission,
	create func(context.Context) error,
) (ReviewDecisionRunLink, bool, error) {
	if err := s.ready(ctx); err != nil {
		return ReviewDecisionRunLink{}, false, err
	}
	normalized, err := normalizeReviewDecisionRunAdmission(admission)
	if err != nil {
		return ReviewDecisionRunLink{}, false, err
	}
	if create == nil {
		return ReviewDecisionRunLink{}, false, fmt.Errorf(
			"%w: workflow run create callback is required",
			ErrInvalidReview,
		)
	}

	var (
		link              ReviewDecisionRunLink
		existed           bool
		callbackSucceeded bool
	)
	transactionErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		existing, findErr := getReviewDecisionRun(ctx, conn, normalized.Key)
		switch {
		case findErr == nil:
			if existing.RunID != normalized.RunID {
				return fmt.Errorf(
					"%w: review decision is already bound to another workflow run",
					ErrReviewConflict,
				)
			}
			link = existing
			existed = true
			return nil
		case !errors.Is(findErr, sql.ErrNoRows):
			return findErr
		}

		conflicting, findErr := getReviewDecisionRunByRunID(ctx, conn, normalized.RunID)
		switch {
		case findErr == nil:
			if conflicting.Key != normalized.Key {
				return fmt.Errorf(
					"%w: workflow run is already bound to another review decision",
					ErrReviewConflict,
				)
			}
			// The composite-key lookup above is authoritative. Reaching this
			// branch would mean the database violates its required uniqueness.
			return fmt.Errorf(
				"%w: inconsistent review decision workflow-run binding",
				ErrReviewConflict,
			)
		case !errors.Is(findErr, sql.ErrNoRows):
			return findErr
		}

		if _, versionErr := requireReviewCaseVersion(
			ctx,
			conn,
			normalized.Key.CaseID,
			normalized.Key.CaseVersion,
		); versionErr != nil {
			return versionErr
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		link = ReviewDecisionRunLink{
			Key:       normalized.Key,
			RunID:     normalized.RunID,
			CreatedAt: now,
		}
		if _, insertErr := conn.ExecContext(ctx, `
			INSERT INTO pr_review_decision_runs (
				case_id, case_version, decision_point, policy_revision,
				run_id, created_at
			) VALUES (?, ?, ?, ?, ?, ?)`,
			link.Key.CaseID,
			link.Key.CaseVersion,
			link.Key.DecisionPoint,
			link.Key.PolicyRevision,
			link.RunID,
			toDBTime(link.CreatedAt),
		); insertErr != nil {
			return insertErr
		}
		if callbackErr := create(ctx); callbackErr != nil {
			return callbackErr
		}
		callbackSucceeded = true
		return nil
	})
	if transactionErr != nil {
		if callbackSucceeded {
			return ReviewDecisionRunLink{}, false, fmt.Errorf(
				"%w: %w",
				ErrReviewDecisionAdmissionUncertain,
				s.dbError(transactionErr),
			)
		}
		return ReviewDecisionRunLink{}, false, fmt.Errorf(
			"admit review decision workflow run: %w",
			s.dbError(transactionErr),
		)
	}
	return link, existed, nil
}

func normalizeReviewDecisionRunAdmission(
	admission ReviewDecisionRunAdmission,
) (ReviewDecisionRunAdmission, error) {
	key, err := normalizeReviewDecisionKey(admission.Key)
	if err != nil {
		return ReviewDecisionRunAdmission{}, err
	}
	if !validPrefixedHexID(admission.RunID, "wr_") {
		return ReviewDecisionRunAdmission{}, fmt.Errorf(
			"%w: invalid workflow run ID",
			ErrInvalidReview,
		)
	}
	admission.Key = key
	return admission, nil
}

func normalizeReviewDecisionKey(key ReviewDecisionKey) (ReviewDecisionKey, error) {
	if !validPrefixedHexID(key.CaseID, reviewCaseIDPrefix) || key.CaseVersion < 1 {
		return ReviewDecisionKey{}, fmt.Errorf(
			"%w: valid case ID and positive case version are required",
			ErrInvalidReview,
		)
	}
	if !validReviewDecisionPoint(key.DecisionPoint) {
		return ReviewDecisionKey{}, fmt.Errorf(
			"%w: review decision point must match [a-z][a-z0-9._-]{0,127}",
			ErrInvalidReview,
		)
	}
	if !validReviewPolicyRevision(key.PolicyRevision) {
		return ReviewDecisionKey{}, fmt.Errorf(
			"%w: review decision policy revision must be a lowercase SHA-256 digest",
			ErrInvalidReview,
		)
	}
	return key, nil
}

func validReviewDecisionPoint(value string) bool {
	if len(value) == 0 || len(value) > maxReviewDecisionPointBytes ||
		value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		char := value[index]
		if (char >= 'a' && char <= 'z') ||
			(char >= '0' && char <= '9') ||
			char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func validReviewPolicyRevision(value string) bool {
	if len(value) != len(reviewPolicyRevisionPrefix)+64 ||
		value[:len(reviewPolicyRevisionPrefix)] != reviewPolicyRevisionPrefix {
		return false
	}
	for _, char := range value[len(reviewPolicyRevisionPrefix):] {
		if char < '0' || char > '9' {
			if char < 'a' || char > 'f' {
				return false
			}
		}
	}
	return true
}

func getReviewDecisionRun(
	ctx context.Context,
	queryer rowQueryer,
	key ReviewDecisionKey,
) (ReviewDecisionRunLink, error) {
	return scanReviewDecisionRun(queryer.QueryRowContext(ctx, `
		SELECT case_id, case_version, decision_point, policy_revision,
			run_id, created_at
		FROM pr_review_decision_runs
		WHERE case_id = ? AND case_version = ?
			AND decision_point = ? AND policy_revision = ?`,
		key.CaseID,
		key.CaseVersion,
		key.DecisionPoint,
		key.PolicyRevision,
	))
}

func getReviewDecisionRunByRunID(
	ctx context.Context,
	queryer rowQueryer,
	runID string,
) (ReviewDecisionRunLink, error) {
	return scanReviewDecisionRun(queryer.QueryRowContext(ctx, `
		SELECT case_id, case_version, decision_point, policy_revision,
			run_id, created_at
		FROM pr_review_decision_runs
		WHERE run_id = ?`,
		runID,
	))
}

func scanReviewDecisionRun(scanner rowScanner) (ReviewDecisionRunLink, error) {
	var (
		link      ReviewDecisionRunLink
		createdAt int64
	)
	if err := scanner.Scan(
		&link.Key.CaseID,
		&link.Key.CaseVersion,
		&link.Key.DecisionPoint,
		&link.Key.PolicyRevision,
		&link.RunID,
		&createdAt,
	); err != nil {
		return ReviewDecisionRunLink{}, err
	}
	link.CreatedAt = fromDBTime(createdAt)
	return link, nil
}
