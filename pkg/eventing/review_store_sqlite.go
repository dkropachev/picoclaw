//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxReviewFindings                = 200
	maxReviewMessagesPerAppend       = 50
	maxReviewListItems               = 100
	maxReviewRepositoryBytes         = 512
	maxReviewURLBytes                = 4096
	maxReviewFileBytes               = 4096
	maxReviewTitleBytes              = 8192
	maxReviewTextBytes               = MaxReviewMessageBytes
	maxReviewListTextItems           = 256
	maxReviewMarkerBytes             = 1024
	maxReviewSubmissionBytes         = 1 << 20
	maxReviewCaptureBytes            = 4 << 20
	maxReviewPullNumber        int64 = 1<<31 - 1
	maxReviewLine                    = 1<<31 - 1
)

var _ ReviewStore = (*Store)(nil)

const reviewCaseColumns = `
	id, event_id, dispatch_id, run_id, workflow_ref, workflow_revision,
	connector, repository, pull_number, pull_url, base_sha, head_sha,
	draft_schema_version, summary, tests_json, residual_risks_json, status, version,
	active_findings, total_findings, public_error_code, created_at,
	updated_at, resolved_at, submitted_at`

const reviewFindingColumns = `
	id, case_id, ordinal, state, severity, title, file, line, message,
	evidence, impact, recommendation, validation, dropped_reason, revision,
	created_at, updated_at, dropped_at`

const reviewMessageColumns = `
	id, case_id, ordinal, finding_id, kind, role, content, created_at`

const reviewSubmissionColumns = `
	id, case_id, draft_version, marker, status, claim_from, owner,
	lease_until, attempts, request_json, public_error_code, internal_error,
	external_review_id, external_url, created_at, updated_at, submitted_at`

type reviewQueryer interface {
	rowQueryer
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// CaptureReview idempotently persists a typed workflow review draft. Both the
// dispatch and run identity are unique capture keys. A retry with different
// normalized content is rejected even if the case has since been edited.
func (s *Store) CaptureReview(
	ctx context.Context,
	input ReviewCaptureInput,
) (ReviewCase, bool, error) {
	if err := s.ready(ctx); err != nil {
		return ReviewCase{}, false, err
	}
	normalized, err := normalizeReviewCapture(input)
	if err != nil {
		return ReviewCase{}, false, err
	}
	captureHash, err := reviewCaptureHash(normalized)
	if err != nil {
		return ReviewCase{}, false, err
	}
	testsJSON, err := json.Marshal(normalized.Draft.Tests)
	if err != nil {
		return ReviewCase{}, false, fmt.Errorf("%w: encode tests: %v", ErrInvalidReview, err)
	}
	residualRisksJSON, err := json.Marshal(normalized.Draft.ResidualRisks)
	if err != nil {
		return ReviewCase{}, false, fmt.Errorf(
			"%w: encode residual risks: %v",
			ErrInvalidReview,
			err,
		)
	}

	var (
		reviewCase ReviewCase
		created    bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		existingID, existingHash, findErr := findReviewCapture(
			ctx,
			conn,
			normalized.DispatchID,
			normalized.RunID,
		)
		if findErr != nil {
			return findErr
		}
		if existingID != "" {
			if existingHash != captureHash {
				return fmt.Errorf(
					"%w: dispatch or run was already captured with different content",
					ErrReviewConflict,
				)
			}
			reviewCase, findErr = getReviewCaseRecord(ctx, conn, existingID)
			return findErr
		}

		if verifyErr := verifyReviewDispatch(ctx, conn, normalized); verifyErr != nil {
			return verifyErr
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		caseID, idErr := newPrefixedID(reviewCaseIDPrefix)
		if idErr != nil {
			return idErr
		}
		status := ReviewCaseOpen
		var resolvedAt any
		if len(normalized.Draft.Findings) == 0 {
			status = ReviewCaseAllDropped
			resolvedAt = toDBTime(now)
		}
		if _, execErr := conn.ExecContext(ctx, `
			INSERT INTO pr_review_cases (
				id, event_id, dispatch_id, run_id, workflow_ref,
				workflow_revision, connector, repository, pull_number,
				pull_url, base_sha, head_sha, draft_schema_version, summary, tests_json,
				residual_risks_json, capture_hash, status, version,
				active_findings, total_findings, created_at, updated_at,
				resolved_at
			) VALUES (
				?, ?, ?, ?, ?, ?,
				?, ?, ?, ?, ?, ?,
				?, ?, ?, ?, ?, ?,
				1, ?, ?, ?, ?, ?
			)`,
			caseID,
			normalized.EventID,
			normalized.DispatchID,
			normalized.RunID,
			normalized.WorkflowRef,
			normalized.WorkflowRevision,
			normalized.Connector,
			normalized.Repository,
			normalized.PullNumber,
			normalized.PullURL,
			normalized.BaseSHA,
			normalized.HeadSHA,
			normalized.Draft.SchemaVersion,
			normalized.Draft.Summary,
			testsJSON,
			residualRisksJSON,
			captureHash,
			status,
			len(normalized.Draft.Findings),
			len(normalized.Draft.Findings),
			toDBTime(now),
			toDBTime(now),
			resolvedAt,
		); execErr != nil {
			return execErr
		}
		for ordinal, draft := range normalized.Draft.Findings {
			findingID, idErr := newPrefixedID(reviewFindingIDPrefix)
			if idErr != nil {
				return idErr
			}
			if _, execErr := conn.ExecContext(ctx, `
				INSERT INTO pr_review_findings (
					id, case_id, ordinal, state, severity, title, file, line,
					message, evidence, impact, recommendation, validation,
					created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				findingID,
				caseID,
				ordinal,
				ReviewFindingActive,
				draft.Severity,
				draft.Title,
				draft.File,
				nullableReviewLine(draft.Line),
				draft.Message,
				draft.Evidence,
				draft.Impact,
				draft.Recommendation,
				draft.Validation,
				toDBTime(now),
				toDBTime(now),
			); execErr != nil {
				return execErr
			}
		}
		reviewCase, findErr = getReviewCaseRecord(ctx, conn, caseID)
		if findErr != nil {
			return findErr
		}
		created = true
		return nil
	})
	if err != nil {
		return ReviewCase{}, false, fmt.Errorf(
			"capture pull request review: %w",
			s.dbError(err),
		)
	}
	return reviewCase, created, nil
}

func findReviewCapture(
	ctx context.Context,
	conn *sql.Conn,
	dispatchID, runID string,
) (string, string, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT id, capture_hash
		FROM pr_review_cases
		WHERE dispatch_id = ? OR run_id = ?
		ORDER BY id`,
		dispatchID,
		runID,
	)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()

	var id, captureHash string
	for rows.Next() {
		var candidateID, candidateHash string
		if err := rows.Scan(&candidateID, &candidateHash); err != nil {
			return "", "", err
		}
		if id != "" && id != candidateID {
			return "", "", fmt.Errorf(
				"%w: dispatch and run identify different review cases",
				ErrReviewConflict,
			)
		}
		id, captureHash = candidateID, candidateHash
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	return id, captureHash, nil
}

func verifyReviewDispatch(
	ctx context.Context,
	conn *sql.Conn,
	input ReviewCaptureInput,
) error {
	var (
		eventID, runID, workflowRef string
		workflowRevision, connector string
	)
	err := conn.QueryRowContext(ctx, `
		SELECT d.event_id, d.run_id, d.workflow_ref,
		       COALESCE(r.workflow_revision, ''), e.connector
		FROM event_dispatches d
		JOIN event_inbox e ON e.id = d.event_id
		LEFT JOIN event_dispatch_workflow_revisions r ON r.dispatch_id = d.id
		WHERE d.id = ?`,
		input.DispatchID,
	).Scan(
		&eventID,
		&runID,
		&workflowRef,
		&workflowRevision,
		&connector,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: dispatch %q", ErrNotFound, input.DispatchID)
	}
	if err != nil {
		return err
	}
	if eventID != input.EventID ||
		runID != input.RunID ||
		workflowRef != input.WorkflowRef ||
		workflowRevision != input.WorkflowRevision ||
		connector != input.Connector {
		return fmt.Errorf(
			"%w: trusted review identity does not match dispatch",
			ErrReviewConflict,
		)
	}
	return nil
}

// GetReviewCase returns the complete case aggregate.
func (s *Store) GetReviewCase(
	ctx context.Context,
	id string,
) (ReviewCaseDetail, error) {
	if err := s.ready(ctx); err != nil {
		return ReviewCaseDetail{}, err
	}
	id = strings.TrimSpace(id)
	if !validPrefixedHexID(id, reviewCaseIDPrefix) {
		return ReviewCaseDetail{}, fmt.Errorf("%w: invalid review case ID", ErrInvalidReview)
	}
	var detail ReviewCaseDetail
	snapshotErr := s.withReviewReadSnapshot(ctx, func(queryer reviewQueryer) error {
		var readErr error
		detail, readErr = getReviewCaseDetailWith(ctx, queryer, id)
		return readErr
	})
	if snapshotErr != nil {
		return ReviewCaseDetail{}, s.dbError(snapshotErr)
	}
	return detail, nil
}

// withReviewReadSnapshot holds one SQLite read transaction across every query
// that composes a review aggregate. WAL readers then observe one database
// snapshot even when another process commits a case mutation between queries.
func (s *Store) withReviewReadSnapshot(
	ctx context.Context,
	operation func(reviewQueryer) error,
) (err error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	// Rollback is harmless after Commit and guarantees release on every early
	// return or panic from the aggregate composer.
	defer func() { _ = tx.Rollback() }()
	if err = operation(tx); err != nil {
		return err
	}
	err = tx.Commit()
	return err
}

// ListReviewCases returns a newest-first stable keyset page.
func (s *Store) ListReviewCases(
	ctx context.Context,
	filter ReviewCaseFilter,
) (ReviewCasePage, error) {
	if err := s.ready(ctx); err != nil {
		return ReviewCasePage{}, err
	}
	plan, err := buildReviewCaseListPlan(filter)
	if err != nil {
		return ReviewCasePage{}, err
	}
	rows, err := s.db.QueryContext(ctx, plan.query, plan.args...)
	if err != nil {
		return ReviewCasePage{}, fmt.Errorf("list review cases: %w", s.dbError(err))
	}
	defer rows.Close()

	cases := make([]ReviewCase, 0, plan.limit+1)
	for rows.Next() {
		reviewCase, scanErr := scanReviewCase(rows)
		if scanErr != nil {
			return ReviewCasePage{}, fmt.Errorf("scan review case list: %w", scanErr)
		}
		cases = append(cases, reviewCase)
	}
	if err := rows.Err(); err != nil {
		return ReviewCasePage{}, fmt.Errorf("iterate review case list: %w", s.dbError(err))
	}
	if len(cases) > plan.limit {
		last := cases[plan.limit-1]
		return ReviewCasePage{
			Cases: cases[:plan.limit],
			Next: &ReviewCaseCursor{
				UpdatedAt: last.UpdatedAt,
				ID:        last.ID,
			},
		}, nil
	}
	return ReviewCasePage{Cases: cases}, nil
}

func buildReviewCaseListPlan(filter ReviewCaseFilter) (listPlan, error) {
	if filter.Status != "" && !validReviewCaseStatus(filter.Status) {
		return listPlan{}, fmt.Errorf(
			"%w: unknown review case status %q",
			ErrInvalidReview,
			filter.Status,
		)
	}
	filter.Connector = strings.TrimSpace(filter.Connector)
	filter.Repository = strings.TrimSpace(filter.Repository)
	if filter.PullNumber < 0 {
		return listPlan{}, fmt.Errorf("%w: pull number cannot be negative", ErrInvalidReview)
	}
	if err := validateReviewString(
		"connector",
		filter.Connector,
		maxConnectorLength,
		false,
	); err != nil {
		return listPlan{}, err
	}
	if err := validateReviewString(
		"repository",
		filter.Repository,
		maxReviewRepositoryBytes,
		false,
	); err != nil {
		return listPlan{}, err
	}
	if filter.After != nil {
		if err := validateDBTimestamp("review case cursor updated_at", filter.After.UpdatedAt); err != nil {
			return listPlan{}, fmt.Errorf("%w: %v", ErrInvalidReview, err)
		}
		if !validPrefixedHexID(strings.TrimSpace(filter.After.ID), reviewCaseIDPrefix) {
			return listPlan{}, fmt.Errorf("%w: invalid review case cursor ID", ErrInvalidReview)
		}
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxReviewListItems {
		limit = maxReviewListItems
	}
	query := `SELECT ` + reviewCaseColumns + ` FROM pr_review_cases WHERE 1 = 1`
	args := make([]any, 0, 10)
	if filter.Status != "" {
		query += ` AND status = ?`
		args = append(args, filter.Status)
	}
	if filter.Connector != "" {
		query += ` AND connector = ?`
		args = append(args, filter.Connector)
	}
	if filter.Repository != "" {
		query += ` AND repository = ?`
		args = append(args, filter.Repository)
	}
	if filter.PullNumber > 0 {
		query += ` AND pull_number = ?`
		args = append(args, filter.PullNumber)
	}
	if filter.After != nil {
		query += ` AND (updated_at < ? OR (updated_at = ? AND id < ?))`
		position := toDBTime(filter.After.UpdatedAt)
		args = append(args, position, position, strings.TrimSpace(filter.After.ID))
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	return listPlan{query: query, args: args, limit: limit}, nil
}

func getReviewCaseRecord(
	ctx context.Context,
	queryer rowQueryer,
	id string,
) (ReviewCase, error) {
	return scanReviewCase(queryer.QueryRowContext(ctx, `
		SELECT `+reviewCaseColumns+` FROM pr_review_cases WHERE id = ?`,
		id,
	))
}

func getReviewCaseDetailWith(
	ctx context.Context,
	queryer reviewQueryer,
	id string,
) (ReviewCaseDetail, error) {
	reviewCase, err := getReviewCaseRecord(ctx, queryer, id)
	if err != nil {
		return ReviewCaseDetail{}, err
	}
	findings, err := queryReviewFindings(ctx, queryer, id)
	if err != nil {
		return ReviewCaseDetail{}, err
	}
	messages, err := queryReviewMessages(ctx, queryer, id)
	if err != nil {
		return ReviewCaseDetail{}, err
	}
	submission, err := getLatestReviewSubmission(ctx, queryer, id)
	if err != nil {
		return ReviewCaseDetail{}, err
	}
	return ReviewCaseDetail{
		Case:       reviewCase,
		Findings:   findings,
		Messages:   messages,
		Submission: submission,
	}, nil
}

func queryReviewFindings(
	ctx context.Context,
	queryer reviewQueryer,
	caseID string,
) ([]ReviewFinding, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT `+reviewFindingColumns+`
		FROM pr_review_findings
		WHERE case_id = ?
		ORDER BY ordinal`,
		caseID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	findings := make([]ReviewFinding, 0)
	for rows.Next() {
		finding, scanErr := scanReviewFinding(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		findings = append(findings, finding)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return findings, nil
}

func queryReviewMessages(
	ctx context.Context,
	queryer reviewQueryer,
	caseID string,
) ([]ReviewMessage, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT `+reviewMessageColumns+`
		FROM pr_review_messages
		WHERE case_id = ?
		ORDER BY ordinal`,
		caseID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	messages := make([]ReviewMessage, 0)
	for rows.Next() {
		message, scanErr := scanReviewMessage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func getLatestReviewSubmission(
	ctx context.Context,
	queryer rowQueryer,
	caseID string,
) (*ReviewSubmission, error) {
	submission, err := scanReviewSubmission(queryer.QueryRowContext(ctx, `
		SELECT `+reviewSubmissionColumns+`
		FROM pr_review_submissions
		WHERE case_id = ?
		ORDER BY draft_version DESC, id DESC
		LIMIT 1`,
		caseID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &submission, nil
}

func scanReviewCase(scanner rowScanner) (ReviewCase, error) {
	var (
		reviewCase                    ReviewCase
		testsJSON, residualRisksJSON  []byte
		pullNumber                    int64
		draftSchemaVersion            int64
		version                       int64
		activeFindings, totalFindings int64
		createdAt, updatedAt          int64
		resolvedAt, submittedAt       sql.NullInt64
	)
	err := scanner.Scan(
		&reviewCase.ID,
		&reviewCase.EventID,
		&reviewCase.DispatchID,
		&reviewCase.RunID,
		&reviewCase.WorkflowRef,
		&reviewCase.WorkflowRevision,
		&reviewCase.Connector,
		&reviewCase.Repository,
		&pullNumber,
		&reviewCase.PullURL,
		&reviewCase.BaseSHA,
		&reviewCase.HeadSHA,
		&draftSchemaVersion,
		&reviewCase.Summary,
		&testsJSON,
		&residualRisksJSON,
		&reviewCase.Status,
		&version,
		&activeFindings,
		&totalFindings,
		&reviewCase.PublicErrorCode,
		&createdAt,
		&updatedAt,
		&resolvedAt,
		&submittedAt,
	)
	if err != nil {
		return ReviewCase{}, err
	}
	if pullNumber <= 0 ||
		draftSchemaVersion != ReviewDraftSchemaVersion ||
		version < 1 {
		return ReviewCase{}, fmt.Errorf("stored review case integer is invalid")
	}
	active, err := reviewDBInt(activeFindings)
	if err != nil {
		return ReviewCase{}, err
	}
	total, err := reviewDBInt(totalFindings)
	if err != nil {
		return ReviewCase{}, err
	}
	if err := json.Unmarshal(testsJSON, &reviewCase.Tests); err != nil {
		return ReviewCase{}, fmt.Errorf("decode stored review tests: %w", err)
	}
	if err := json.Unmarshal(residualRisksJSON, &reviewCase.ResidualRisks); err != nil {
		return ReviewCase{}, fmt.Errorf("decode stored review residual risks: %w", err)
	}
	reviewCase.Tests = append([]string(nil), reviewCase.Tests...)
	reviewCase.ResidualRisks = append([]string(nil), reviewCase.ResidualRisks...)
	reviewCase.PullNumber = pullNumber
	reviewCase.Version = version
	reviewCase.ActiveFindings = active
	reviewCase.TotalFindings = total
	reviewCase.CreatedAt = fromDBTime(createdAt)
	reviewCase.UpdatedAt = fromDBTime(updatedAt)
	reviewCase.ResolvedAt = fromNullableTime(resolvedAt)
	reviewCase.SubmittedAt = fromNullableTime(submittedAt)
	return reviewCase, nil
}

func scanReviewFinding(scanner rowScanner) (ReviewFinding, error) {
	var (
		finding              ReviewFinding
		ordinal, revision    int64
		line, droppedAt      sql.NullInt64
		createdAt, updatedAt int64
	)
	err := scanner.Scan(
		&finding.ID,
		&finding.CaseID,
		&ordinal,
		&finding.State,
		&finding.Severity,
		&finding.Title,
		&finding.File,
		&line,
		&finding.Message,
		&finding.Evidence,
		&finding.Impact,
		&finding.Recommendation,
		&finding.Validation,
		&finding.DroppedReason,
		&revision,
		&createdAt,
		&updatedAt,
		&droppedAt,
	)
	if err != nil {
		return ReviewFinding{}, err
	}
	convertedOrdinal, err := reviewDBInt(ordinal)
	if err != nil {
		return ReviewFinding{}, err
	}
	if revision < 1 {
		return ReviewFinding{}, fmt.Errorf("stored review finding revision is invalid")
	}
	if line.Valid {
		convertedLine, err := reviewDBInt(line.Int64)
		if err != nil || convertedLine <= 0 {
			return ReviewFinding{}, fmt.Errorf("stored review finding line is invalid")
		}
		finding.Line = &convertedLine
	}
	finding.Ordinal = convertedOrdinal
	finding.Revision = revision
	finding.CreatedAt = fromDBTime(createdAt)
	finding.UpdatedAt = fromDBTime(updatedAt)
	finding.DroppedAt = fromNullableTime(droppedAt)
	return finding, nil
}

func scanReviewMessage(scanner rowScanner) (ReviewMessage, error) {
	var (
		message   ReviewMessage
		ordinal   int64
		findingID sql.NullString
		createdAt int64
	)
	err := scanner.Scan(
		&message.ID,
		&message.CaseID,
		&ordinal,
		&findingID,
		&message.Kind,
		&message.Role,
		&message.Content,
		&createdAt,
	)
	if err != nil {
		return ReviewMessage{}, err
	}
	convertedOrdinal, err := reviewDBInt(ordinal)
	if err != nil {
		return ReviewMessage{}, err
	}
	if findingID.Valid {
		message.FindingID = findingID.String
	}
	message.Ordinal = convertedOrdinal
	message.CreatedAt = fromDBTime(createdAt)
	return message, nil
}

func scanReviewSubmission(scanner rowScanner) (ReviewSubmission, error) {
	var (
		submission              ReviewSubmission
		draftVersion, attempts  int64
		requestJSON             []byte
		leaseUntil, submittedAt sql.NullInt64
		createdAt, updatedAt    int64
	)
	err := scanner.Scan(
		&submission.ID,
		&submission.CaseID,
		&draftVersion,
		&submission.Marker,
		&submission.Status,
		&submission.ClaimFrom,
		&submission.LeaseToken,
		&leaseUntil,
		&attempts,
		&requestJSON,
		&submission.PublicErrorCode,
		&submission.InternalError,
		&submission.ExternalReviewID,
		&submission.ExternalURL,
		&createdAt,
		&updatedAt,
		&submittedAt,
	)
	if err != nil {
		return ReviewSubmission{}, err
	}
	convertedAttempts, err := reviewDBInt(attempts)
	if err != nil {
		return ReviewSubmission{}, err
	}
	if draftVersion < 1 || !json.Valid(requestJSON) {
		return ReviewSubmission{}, fmt.Errorf("stored review submission is invalid")
	}
	submission.DraftVersion = draftVersion
	submission.Attempts = convertedAttempts
	submission.Request = cloneBytes(requestJSON)
	submission.LeaseUntil = fromNullableTime(leaseUntil)
	submission.CreatedAt = fromDBTime(createdAt)
	submission.UpdatedAt = fromDBTime(updatedAt)
	submission.SubmittedAt = fromNullableTime(submittedAt)
	return submission, nil
}

func normalizeReviewCapture(input ReviewCaptureInput) (ReviewCaptureInput, error) {
	input.EventID = strings.TrimSpace(input.EventID)
	input.DispatchID = strings.TrimSpace(input.DispatchID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.WorkflowRef = strings.TrimSpace(input.WorkflowRef)
	input.WorkflowRevision = strings.TrimSpace(input.WorkflowRevision)
	input.Connector = strings.TrimSpace(input.Connector)
	input.Repository = strings.TrimSpace(input.Repository)
	input.PullURL = strings.TrimSpace(input.PullURL)
	input.BaseSHA = strings.ToLower(strings.TrimSpace(input.BaseSHA))
	input.HeadSHA = strings.ToLower(strings.TrimSpace(input.HeadSHA))
	if !validEventID(input.EventID) {
		return ReviewCaptureInput{}, fmt.Errorf("%w: invalid event ID", ErrInvalidReview)
	}
	if !validPrefixedHexID(input.DispatchID, "dsp_") {
		return ReviewCaptureInput{}, fmt.Errorf("%w: invalid dispatch ID", ErrInvalidReview)
	}
	if !validPrefixedHexID(input.RunID, "wr_") {
		return ReviewCaptureInput{}, fmt.Errorf("%w: invalid workflow run ID", ErrInvalidReview)
	}
	if err := validateReviewString(
		"workflow reference",
		input.WorkflowRef,
		maxWorkflowRefLength,
		true,
	); err != nil {
		return ReviewCaptureInput{}, err
	}
	if err := validateReviewString(
		"workflow revision",
		input.WorkflowRevision,
		maxWorkflowRevisionLength,
		false,
	); err != nil {
		return ReviewCaptureInput{}, err
	}
	if err := validateReviewString(
		"connector",
		input.Connector,
		maxConnectorLength,
		true,
	); err != nil {
		return ReviewCaptureInput{}, err
	}
	if err := validateReviewString(
		"repository",
		input.Repository,
		maxReviewRepositoryBytes,
		true,
	); err != nil {
		return ReviewCaptureInput{}, err
	}
	if input.PullNumber <= 0 || input.PullNumber > maxReviewPullNumber {
		return ReviewCaptureInput{}, fmt.Errorf(
			"%w: pull number must be between 1 and %d",
			ErrInvalidReview,
			maxReviewPullNumber,
		)
	}
	if err := validateReviewURL(input.PullURL); err != nil {
		return ReviewCaptureInput{}, err
	}
	if !validReviewGitOID(input.BaseSHA) || !validReviewGitOID(input.HeadSHA) {
		return ReviewCaptureInput{}, fmt.Errorf(
			"%w: base and head SHA must be 40 or 64 lowercase hexadecimal characters",
			ErrInvalidReview,
		)
	}
	if input.Draft.SchemaVersion != ReviewDraftSchemaVersion {
		return ReviewCaptureInput{}, fmt.Errorf(
			"%w: review draft schema version is %d, want %d",
			ErrInvalidReview,
			input.Draft.SchemaVersion,
			ReviewDraftSchemaVersion,
		)
	}
	var err error
	input.Draft.Summary, err = normalizeReviewText(
		"review summary",
		input.Draft.Summary,
		maxReviewTextBytes,
		true,
	)
	if err != nil {
		return ReviewCaptureInput{}, err
	}
	input.Draft.Tests, err = normalizeReviewStringList("review tests", input.Draft.Tests)
	if err != nil {
		return ReviewCaptureInput{}, err
	}
	input.Draft.ResidualRisks, err = normalizeReviewStringList(
		"review residual risks",
		input.Draft.ResidualRisks,
	)
	if err != nil {
		return ReviewCaptureInput{}, err
	}
	if len(input.Draft.Findings) > maxReviewFindings {
		return ReviewCaptureInput{}, fmt.Errorf(
			"%w: review has %d findings; maximum is %d",
			ErrInvalidReview,
			len(input.Draft.Findings),
			maxReviewFindings,
		)
	}
	findings := make([]ReviewFindingDraft, len(input.Draft.Findings))
	for index, draft := range input.Draft.Findings {
		normalized, err := normalizeReviewFindingDraft(draft)
		if err != nil {
			return ReviewCaptureInput{}, fmt.Errorf("finding %d: %w", index, err)
		}
		findings[index] = normalized
	}
	input.Draft.Findings = findings
	return input, nil
}

func normalizeReviewFindingDraft(input ReviewFindingDraft) (ReviewFindingDraft, error) {
	if !validReviewSeverity(input.Severity) {
		return ReviewFindingDraft{}, fmt.Errorf(
			"%w: unknown finding severity %q",
			ErrInvalidReview,
			input.Severity,
		)
	}
	var err error
	input.Title, err = normalizeReviewText(
		"finding title",
		input.Title,
		maxReviewTitleBytes,
		true,
	)
	if err != nil {
		return ReviewFindingDraft{}, err
	}
	input.File, err = normalizeReviewText(
		"finding file",
		input.File,
		maxReviewFileBytes,
		false,
	)
	if err != nil {
		return ReviewFindingDraft{}, err
	}
	if input.File != "" {
		cleaned := path.Clean(input.File)
		if strings.HasPrefix(input.File, "/") ||
			cleaned == "." ||
			cleaned == ".." ||
			strings.HasPrefix(cleaned, "../") ||
			cleaned != input.File ||
			strings.ContainsRune(input.File, '\\') {
			return ReviewFindingDraft{}, fmt.Errorf(
				"%w: finding file must be a clean repository-relative slash path",
				ErrInvalidReview,
			)
		}
	}
	if input.Line != nil {
		if *input.Line <= 0 || *input.Line > maxReviewLine || input.File == "" {
			return ReviewFindingDraft{}, fmt.Errorf(
				"%w: finding line requires a file and must be between 1 and %d",
				ErrInvalidReview,
				maxReviewLine,
			)
		}
		line := *input.Line
		input.Line = &line
	}
	for _, field := range []struct {
		name     string
		value    *string
		required bool
	}{
		{name: "finding message", value: &input.Message, required: true},
		{name: "finding evidence", value: &input.Evidence},
		{name: "finding impact", value: &input.Impact},
		{name: "finding recommendation", value: &input.Recommendation},
		{name: "finding validation", value: &input.Validation},
	} {
		*field.value, err = normalizeReviewText(
			field.name,
			*field.value,
			maxReviewTextBytes,
			field.required,
		)
		if err != nil {
			return ReviewFindingDraft{}, err
		}
	}
	return input, nil
}

func normalizeReviewStringList(field string, input []string) ([]string, error) {
	if len(input) > maxReviewListTextItems {
		return nil, fmt.Errorf(
			"%w: %s has %d entries; maximum is %d",
			ErrInvalidReview,
			field,
			len(input),
			maxReviewListTextItems,
		)
	}
	output := make([]string, len(input))
	for index, value := range input {
		normalized, err := normalizeReviewText(
			fmt.Sprintf("%s entry %d", field, index),
			value,
			maxReviewTextBytes,
			true,
		)
		if err != nil {
			return nil, err
		}
		output[index] = normalized
	}
	return output, nil
}

func normalizeReviewText(
	field, value string,
	maximum int,
	required bool,
) (string, error) {
	value = strings.TrimSpace(value)
	if err := validateReviewString(field, value, maximum, required); err != nil {
		return "", err
	}
	return value, nil
}

func validateReviewString(
	field, value string,
	maximum int,
	required bool,
) error {
	if required && value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidReview, field)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalidReview, field)
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%w: %s contains a NUL byte", ErrInvalidReview, field)
	}
	if len(value) > maximum {
		return fmt.Errorf(
			"%w: %s exceeds %d bytes",
			ErrInvalidReview,
			field,
			maximum,
		)
	}
	return nil
}

func validateReviewURL(value string) error {
	if err := validateReviewString("pull URL", value, maxReviewURLBytes, true); err != nil {
		return err
	}
	parsed, err := url.Parse(value)
	if err != nil ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") ||
		parsed.Host == "" {
		return fmt.Errorf("%w: pull URL must be an absolute HTTP(S) URL", ErrInvalidReview)
	}
	return nil
}

func validPrefixedHexID(value, prefix string) bool {
	if len(value) != len(prefix)+32 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, char := range value[len(prefix):] {
		if char < '0' || char > '9' {
			if char < 'a' || char > 'f' {
				return false
			}
		}
	}
	return true
}

func validReviewGitOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			if char < 'a' || char > 'f' {
				return false
			}
		}
	}
	return true
}

func validReviewSeverity(value ReviewSeverity) bool {
	switch value {
	case ReviewSeverityCritical,
		ReviewSeverityHigh,
		ReviewSeverityMedium,
		ReviewSeverityLow:
		return true
	default:
		return false
	}
}

func validReviewCaseStatus(value ReviewCaseStatus) bool {
	switch value {
	case ReviewCaseOpen,
		ReviewCaseAllDropped,
		ReviewCaseSubmitting,
		ReviewCaseSubmissionUnknown,
		ReviewCaseSubmitted,
		ReviewCaseStale:
		return true
	default:
		return false
	}
}

func reviewCaptureHash(input ReviewCaptureInput) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("%w: encode capture identity: %v", ErrInvalidReview, err)
	}
	if len(encoded) > maxReviewCaptureBytes {
		return "", fmt.Errorf(
			"%w: normalized review capture exceeds %d bytes",
			ErrInvalidReview,
			maxReviewCaptureBytes,
		)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func nullableReviewLine(line *int) any {
	if line == nil {
		return nil
	}
	return *line
}

func reviewDBInt(value int64) (int, error) {
	converted := int(value)
	if value < 0 || int64(converted) != value {
		return 0, fmt.Errorf("stored review integer is invalid")
	}
	return converted, nil
}

// UpdateReviewFinding replaces the editable content of one active finding
// under an optimistic case version.
func (s *Store) UpdateReviewFinding(
	ctx context.Context,
	input ReviewFindingUpdate,
) (ReviewCaseDetail, error) {
	if err := s.ready(ctx); err != nil {
		return ReviewCaseDetail{}, err
	}
	input.CaseID = strings.TrimSpace(input.CaseID)
	input.FindingID = strings.TrimSpace(input.FindingID)
	if err := validateReviewFindingMutation(
		input.CaseID,
		input.FindingID,
		input.ExpectedVersion,
	); err != nil {
		return ReviewCaseDetail{}, err
	}
	finding, normalizeErr := normalizeReviewFindingDraft(input.Finding)
	if normalizeErr != nil {
		return ReviewCaseDetail{}, normalizeErr
	}

	var detail ReviewCaseDetail
	transactionErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		reviewCase, versionErr := requireReviewCaseVersion(
			ctx,
			conn,
			input.CaseID,
			input.ExpectedVersion,
		)
		if versionErr != nil {
			return versionErr
		}
		if reviewCase.Status != ReviewCaseOpen {
			return fmt.Errorf(
				"%w: findings can only be edited while a review case is open",
				ErrInvalidTransition,
			)
		}
		stored, findingErr := getReviewFindingRecord(
			ctx,
			conn,
			input.CaseID,
			input.FindingID,
		)
		if findingErr != nil {
			return findingErr
		}
		if stored.State != ReviewFindingActive {
			return fmt.Errorf(
				"%w: dropped review findings cannot be edited",
				ErrInvalidTransition,
			)
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if _, execErr := conn.ExecContext(ctx, `
			UPDATE pr_review_findings
			SET severity = ?, title = ?, file = ?, line = ?, message = ?,
			    evidence = ?, impact = ?, recommendation = ?, validation = ?,
			    revision = revision + 1, updated_at = ?
			WHERE id = ? AND case_id = ?`,
			finding.Severity,
			finding.Title,
			finding.File,
			nullableReviewLine(finding.Line),
			finding.Message,
			finding.Evidence,
			finding.Impact,
			finding.Recommendation,
			finding.Validation,
			toDBTime(now),
			input.FindingID,
			input.CaseID,
		); execErr != nil {
			return execErr
		}
		if _, execErr := conn.ExecContext(ctx, `
			UPDATE pr_review_cases
			SET version = version + 1, updated_at = ?, public_error_code = ''
			WHERE id = ?`,
			toDBTime(now),
			input.CaseID,
		); execErr != nil {
			return execErr
		}
		loadedDetail, detailErr := getReviewCaseDetailWith(ctx, conn, input.CaseID)
		detail = loadedDetail
		return detailErr
	})
	if transactionErr != nil {
		return ReviewCaseDetail{}, fmt.Errorf(
			"update pull request review finding: %w",
			s.dbError(transactionErr),
		)
	}
	return detail, nil
}

// DropReviewFinding removes one active finding from the submitted draft.
func (s *Store) DropReviewFinding(
	ctx context.Context,
	input ReviewFindingTransition,
) (ReviewCaseDetail, error) {
	if err := s.ready(ctx); err != nil {
		return ReviewCaseDetail{}, err
	}
	input.CaseID = strings.TrimSpace(input.CaseID)
	input.FindingID = strings.TrimSpace(input.FindingID)
	if err := validateReviewFindingMutation(
		input.CaseID,
		input.FindingID,
		input.ExpectedVersion,
	); err != nil {
		return ReviewCaseDetail{}, err
	}
	reason, normalizeErr := normalizeReviewText(
		"finding drop reason",
		input.Reason,
		maxReviewTextBytes,
		false,
	)
	if normalizeErr != nil {
		return ReviewCaseDetail{}, normalizeErr
	}

	var detail ReviewCaseDetail
	transactionErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		reviewCase, versionErr := requireReviewCaseVersion(
			ctx,
			conn,
			input.CaseID,
			input.ExpectedVersion,
		)
		if versionErr != nil {
			return versionErr
		}
		if reviewCase.Status != ReviewCaseOpen {
			return fmt.Errorf(
				"%w: findings can only be dropped while a review case is open",
				ErrInvalidTransition,
			)
		}
		finding, findingErr := getReviewFindingRecord(
			ctx,
			conn,
			input.CaseID,
			input.FindingID,
		)
		if findingErr != nil {
			return findingErr
		}
		if finding.State != ReviewFindingActive {
			return fmt.Errorf(
				"%w: review finding is already dropped",
				ErrInvalidTransition,
			)
		}
		if reviewCase.ActiveFindings <= 0 {
			return fmt.Errorf("stored review case active finding count is invalid")
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		activeFindings := reviewCase.ActiveFindings - 1
		status := ReviewCaseOpen
		var resolvedAt any
		if activeFindings == 0 {
			status = ReviewCaseAllDropped
			resolvedAt = toDBTime(now)
		}
		if _, execErr := conn.ExecContext(ctx, `
			UPDATE pr_review_findings
			SET state = ?, dropped_reason = ?, dropped_at = ?,
			    revision = revision + 1, updated_at = ?
			WHERE id = ? AND case_id = ?`,
			ReviewFindingDropped,
			reason,
			toDBTime(now),
			toDBTime(now),
			input.FindingID,
			input.CaseID,
		); execErr != nil {
			return execErr
		}
		if _, execErr := conn.ExecContext(ctx, `
			UPDATE pr_review_cases
			SET status = ?, active_findings = ?, version = version + 1,
			    updated_at = ?, resolved_at = ?, public_error_code = ''
			WHERE id = ?`,
			status,
			activeFindings,
			toDBTime(now),
			resolvedAt,
			input.CaseID,
		); execErr != nil {
			return execErr
		}
		loadedDetail, detailErr := getReviewCaseDetailWith(ctx, conn, input.CaseID)
		detail = loadedDetail
		return detailErr
	})
	if transactionErr != nil {
		return ReviewCaseDetail{}, fmt.Errorf(
			"drop pull request review finding: %w",
			s.dbError(transactionErr),
		)
	}
	return detail, nil
}

// RestoreReviewFinding puts one dropped finding back into the draft.
func (s *Store) RestoreReviewFinding(
	ctx context.Context,
	input ReviewFindingTransition,
) (ReviewCaseDetail, error) {
	if err := s.ready(ctx); err != nil {
		return ReviewCaseDetail{}, err
	}
	input.CaseID = strings.TrimSpace(input.CaseID)
	input.FindingID = strings.TrimSpace(input.FindingID)
	if err := validateReviewFindingMutation(
		input.CaseID,
		input.FindingID,
		input.ExpectedVersion,
	); err != nil {
		return ReviewCaseDetail{}, err
	}

	var detail ReviewCaseDetail
	transactionErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		reviewCase, versionErr := requireReviewCaseVersion(
			ctx,
			conn,
			input.CaseID,
			input.ExpectedVersion,
		)
		if versionErr != nil {
			return versionErr
		}
		if reviewCase.Status != ReviewCaseOpen &&
			reviewCase.Status != ReviewCaseAllDropped {
			return fmt.Errorf(
				"%w: findings cannot be restored in review case state %q",
				ErrInvalidTransition,
				reviewCase.Status,
			)
		}
		finding, findingErr := getReviewFindingRecord(
			ctx,
			conn,
			input.CaseID,
			input.FindingID,
		)
		if findingErr != nil {
			return findingErr
		}
		if finding.State != ReviewFindingDropped {
			return fmt.Errorf(
				"%w: review finding is already active",
				ErrInvalidTransition,
			)
		}
		if reviewCase.ActiveFindings >= reviewCase.TotalFindings {
			return fmt.Errorf("stored review case active finding count is invalid")
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if _, execErr := conn.ExecContext(ctx, `
			UPDATE pr_review_findings
			SET state = ?, dropped_reason = '', dropped_at = NULL,
			    revision = revision + 1, updated_at = ?
			WHERE id = ? AND case_id = ?`,
			ReviewFindingActive,
			toDBTime(now),
			input.FindingID,
			input.CaseID,
		); execErr != nil {
			return execErr
		}
		if _, execErr := conn.ExecContext(ctx, `
			UPDATE pr_review_cases
			SET status = ?, active_findings = active_findings + 1,
			    version = version + 1, updated_at = ?, resolved_at = NULL,
			    public_error_code = ''
			WHERE id = ?`,
			ReviewCaseOpen,
			toDBTime(now),
			input.CaseID,
		); execErr != nil {
			return execErr
		}
		loadedDetail, detailErr := getReviewCaseDetailWith(ctx, conn, input.CaseID)
		detail = loadedDetail
		return detailErr
	})
	if transactionErr != nil {
		return ReviewCaseDetail{}, fmt.Errorf(
			"restore pull request review finding: %w",
			s.dbError(transactionErr),
		)
	}
	return detail, nil
}

// AppendReviewMessages atomically appends conversation records and advances
// the case version once.
func (s *Store) AppendReviewMessages(
	ctx context.Context,
	input ReviewMessageAppend,
) (ReviewCaseDetail, error) {
	if err := s.ready(ctx); err != nil {
		return ReviewCaseDetail{}, err
	}
	input.CaseID = strings.TrimSpace(input.CaseID)
	if !validPrefixedHexID(input.CaseID, reviewCaseIDPrefix) ||
		input.ExpectedVersion < 1 {
		return ReviewCaseDetail{}, fmt.Errorf(
			"%w: valid case ID and positive expected version are required",
			ErrInvalidReview,
		)
	}
	if len(input.Messages) == 0 ||
		len(input.Messages) > maxReviewMessagesPerAppend {
		return ReviewCaseDetail{}, fmt.Errorf(
			"%w: message append must contain between 1 and %d messages",
			ErrInvalidReview,
			maxReviewMessagesPerAppend,
		)
	}
	messages := make([]ReviewMessageDraft, len(input.Messages))
	for index, message := range input.Messages {
		normalized, err := normalizeReviewMessageDraft(message)
		if err != nil {
			return ReviewCaseDetail{}, fmt.Errorf("message %d: %w", index, err)
		}
		messages[index] = normalized
	}

	var detail ReviewCaseDetail
	transactionErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		reviewCase, versionErr := requireReviewCaseVersion(
			ctx,
			conn,
			input.CaseID,
			input.ExpectedVersion,
		)
		if versionErr != nil {
			return versionErr
		}
		if reviewCase.Status != ReviewCaseOpen &&
			reviewCase.Status != ReviewCaseAllDropped {
			return fmt.Errorf(
				"%w: messages cannot be appended in review case state %q",
				ErrInvalidTransition,
				reviewCase.Status,
			)
		}
		var (
			storedMessageCount int64
			storedContentBytes int64
		)
		if queryErr := conn.QueryRowContext(ctx, `
			SELECT COUNT(*),
			       COALESCE(SUM(LENGTH(CAST(content AS BLOB))), 0)
			FROM pr_review_messages
			WHERE case_id = ?`,
			input.CaseID,
		).Scan(&storedMessageCount, &storedContentBytes); queryErr != nil {
			return queryErr
		}
		appendContentBytes := int64(0)
		for _, message := range messages {
			appendContentBytes += int64(len(message.Content))
		}
		if storedMessageCount > MaxReviewMessagesPerCase ||
			int64(len(messages)) >
				int64(MaxReviewMessagesPerCase)-storedMessageCount {
			return fmt.Errorf(
				"%w: review transcript cannot exceed %d messages",
				ErrInvalidTransition,
				MaxReviewMessagesPerCase,
			)
		}
		if storedContentBytes > MaxReviewTranscriptBytes ||
			appendContentBytes >
				int64(MaxReviewTranscriptBytes)-storedContentBytes {
			return fmt.Errorf(
				"%w: review transcript cannot exceed %d bytes",
				ErrInvalidTransition,
				MaxReviewTranscriptBytes,
			)
		}
		for _, message := range messages {
			if message.FindingID == "" {
				continue
			}
			if _, findingErr := getReviewFindingRecord(
				ctx,
				conn,
				input.CaseID,
				message.FindingID,
			); findingErr != nil {
				return findingErr
			}
		}
		var lastOrdinal int64
		if queryErr := conn.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(ordinal), -1)
			FROM pr_review_messages
			WHERE case_id = ?`,
			input.CaseID,
		).Scan(&lastOrdinal); queryErr != nil {
			return queryErr
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		for index, message := range messages {
			messageID, idErr := newPrefixedID(reviewMessageIDPrefix)
			if idErr != nil {
				return idErr
			}
			if _, execErr := conn.ExecContext(ctx, `
				INSERT INTO pr_review_messages (
					id, case_id, ordinal, finding_id, kind, role, content,
					created_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				messageID,
				input.CaseID,
				lastOrdinal+1+int64(index),
				nullableString(message.FindingID),
				message.Kind,
				message.Role,
				message.Content,
				toDBTime(now),
			); execErr != nil {
				return execErr
			}
		}
		if _, execErr := conn.ExecContext(ctx, `
			UPDATE pr_review_cases
			SET version = version + 1, updated_at = ?
			WHERE id = ?`,
			toDBTime(now),
			input.CaseID,
		); execErr != nil {
			return execErr
		}
		loadedDetail, detailErr := getReviewCaseDetailWith(ctx, conn, input.CaseID)
		detail = loadedDetail
		return detailErr
	})
	if transactionErr != nil {
		return ReviewCaseDetail{}, fmt.Errorf(
			"append pull request review messages: %w",
			s.dbError(transactionErr),
		)
	}
	return detail, nil
}

func validateReviewFindingMutation(
	caseID, findingID string,
	expectedVersion int64,
) error {
	if !validPrefixedHexID(strings.TrimSpace(caseID), reviewCaseIDPrefix) ||
		!validPrefixedHexID(strings.TrimSpace(findingID), reviewFindingIDPrefix) ||
		expectedVersion < 1 {
		return fmt.Errorf(
			"%w: valid case/finding IDs and positive expected version are required",
			ErrInvalidReview,
		)
	}
	return nil
}

func normalizeReviewMessageDraft(
	input ReviewMessageDraft,
) (ReviewMessageDraft, error) {
	input.FindingID = strings.TrimSpace(input.FindingID)
	if input.FindingID != "" &&
		!validPrefixedHexID(input.FindingID, reviewFindingIDPrefix) {
		return ReviewMessageDraft{}, fmt.Errorf(
			"%w: invalid review finding ID",
			ErrInvalidReview,
		)
	}
	switch input.Kind {
	case ReviewMessageChat, ReviewMessageRephrase:
	default:
		return ReviewMessageDraft{}, fmt.Errorf(
			"%w: unknown review message kind %q",
			ErrInvalidReview,
			input.Kind,
		)
	}
	if input.Kind == ReviewMessageRephrase && input.FindingID == "" {
		return ReviewMessageDraft{}, fmt.Errorf(
			"%w: a rephrase message requires a review finding ID",
			ErrInvalidReview,
		)
	}
	switch input.Role {
	case ReviewMessageUser, ReviewMessageAssistant:
	default:
		return ReviewMessageDraft{}, fmt.Errorf(
			"%w: unknown review message role %q",
			ErrInvalidReview,
			input.Role,
		)
	}
	var err error
	input.Content, err = normalizeReviewText(
		"review message content",
		input.Content,
		maxReviewTextBytes,
		true,
	)
	if err != nil {
		return ReviewMessageDraft{}, err
	}
	return input, nil
}

func requireReviewCaseVersion(
	ctx context.Context,
	queryer rowQueryer,
	caseID string,
	expectedVersion int64,
) (ReviewCase, error) {
	reviewCase, err := getReviewCaseRecord(ctx, queryer, strings.TrimSpace(caseID))
	if err != nil {
		return ReviewCase{}, err
	}
	if reviewCase.Version != expectedVersion {
		return ReviewCase{}, fmt.Errorf(
			"%w: case version is %d, expected %d",
			ErrReviewConflict,
			reviewCase.Version,
			expectedVersion,
		)
	}
	return reviewCase, nil
}

func getReviewFindingRecord(
	ctx context.Context,
	queryer rowQueryer,
	caseID, findingID string,
) (ReviewFinding, error) {
	return scanReviewFinding(queryer.QueryRowContext(ctx, `
		SELECT `+reviewFindingColumns+`
		FROM pr_review_findings
		WHERE case_id = ? AND id = ?`,
		strings.TrimSpace(caseID),
		strings.TrimSpace(findingID),
	))
}

// CreateReviewSubmission freezes an editable case version into a durable
// pending outbox request and moves the case to submitting.
func (s *Store) CreateReviewSubmission(
	ctx context.Context,
	input ReviewSubmissionDraft,
) (ReviewCaseDetail, error) {
	if err := s.ready(ctx); err != nil {
		return ReviewCaseDetail{}, err
	}
	input.CaseID = strings.TrimSpace(input.CaseID)
	input.Marker = strings.TrimSpace(input.Marker)
	if !validPrefixedHexID(input.CaseID, reviewCaseIDPrefix) ||
		input.ExpectedVersion < 1 {
		return ReviewCaseDetail{}, fmt.Errorf(
			"%w: valid case ID and positive expected version are required",
			ErrInvalidReview,
		)
	}
	if err := validateReviewString(
		"submission marker",
		input.Marker,
		maxReviewMarkerBytes,
		true,
	); err != nil {
		return ReviewCaseDetail{}, err
	}
	request, normalizeErr := normalizeReviewSubmissionRequest(input.Request)
	if normalizeErr != nil {
		return ReviewCaseDetail{}, normalizeErr
	}

	var detail ReviewCaseDetail
	transactionErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		existing, findErr := findReviewSubmissionDraft(
			ctx,
			conn,
			input.CaseID,
			input.ExpectedVersion,
			input.Marker,
		)
		if findErr != nil {
			return findErr
		}
		if existing != nil {
			if existing.CaseID != input.CaseID ||
				existing.DraftVersion != input.ExpectedVersion ||
				existing.Marker != input.Marker ||
				string(existing.Request) != string(request) {
				return fmt.Errorf(
					"%w: submission marker or draft version has different content",
					ErrReviewConflict,
				)
			}
			loadedDetail, detailErr := getReviewCaseDetailWith(ctx, conn, input.CaseID)
			detail = loadedDetail
			return detailErr
		}

		reviewCase, versionErr := requireReviewCaseVersion(
			ctx,
			conn,
			input.CaseID,
			input.ExpectedVersion,
		)
		if versionErr != nil {
			return versionErr
		}
		if reviewCase.Status != ReviewCaseOpen {
			return fmt.Errorf(
				"%w: only an open review case can be submitted",
				ErrInvalidTransition,
			)
		}
		if reviewCase.ActiveFindings <= 0 {
			return fmt.Errorf(
				"%w: a review submission requires at least one active finding",
				ErrInvalidTransition,
			)
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		submissionID, idErr := newPrefixedID(reviewSubmissionIDPrefix)
		if idErr != nil {
			return idErr
		}
		if _, execErr := conn.ExecContext(ctx, `
			INSERT INTO pr_review_submissions (
				id, case_id, draft_version, marker, status, request_json,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			submissionID,
			input.CaseID,
			input.ExpectedVersion,
			input.Marker,
			ReviewSubmissionPending,
			request,
			toDBTime(now),
			toDBTime(now),
		); execErr != nil {
			return execErr
		}
		if _, execErr := conn.ExecContext(ctx, `
			UPDATE pr_review_cases
			SET status = ?, version = version + 1, updated_at = ?,
			    public_error_code = ''
			WHERE id = ?`,
			ReviewCaseSubmitting,
			toDBTime(now),
			input.CaseID,
		); execErr != nil {
			return execErr
		}
		loadedDetail, detailErr := getReviewCaseDetailWith(ctx, conn, input.CaseID)
		detail = loadedDetail
		return detailErr
	})
	if transactionErr != nil {
		return ReviewCaseDetail{}, fmt.Errorf(
			"create pull request review submission: %w",
			s.dbError(transactionErr),
		)
	}
	return detail, nil
}

func findReviewSubmissionDraft(
	ctx context.Context,
	queryer reviewQueryer,
	caseID string,
	draftVersion int64,
	marker string,
) (*ReviewSubmission, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT `+reviewSubmissionColumns+`
		FROM pr_review_submissions
		WHERE (case_id = ? AND draft_version = ?) OR marker = ?
		ORDER BY id`,
		caseID,
		draftVersion,
		marker,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var existing *ReviewSubmission
	for rows.Next() {
		submission, scanErr := scanReviewSubmission(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if existing != nil && existing.ID != submission.ID {
			return nil, fmt.Errorf(
				"%w: marker and draft version identify different submissions",
				ErrReviewConflict,
			)
		}
		submissionCopy := submission
		existing = &submissionCopy
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return existing, nil
}

func normalizeReviewSubmissionRequest(
	input json.RawMessage,
) (json.RawMessage, error) {
	if len(input) == 0 || len(input) > maxReviewSubmissionBytes {
		return nil, fmt.Errorf(
			"%w: submission request must contain at most %d bytes",
			ErrInvalidReview,
			maxReviewSubmissionBytes,
		)
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, input); err != nil {
		return nil, fmt.Errorf("%w: submission request is invalid JSON", ErrInvalidReview)
	}
	normalized := []byte(compacted.String())
	if len(normalized) < 2 || normalized[0] != '{' {
		return nil, fmt.Errorf(
			"%w: submission request must be a JSON object",
			ErrInvalidReview,
		)
	}
	return cloneBytes(normalized), nil
}

// GetReviewSubmission retrieves one outbox record.
func (s *Store) GetReviewSubmission(
	ctx context.Context,
	id string,
) (ReviewSubmission, error) {
	if err := s.ready(ctx); err != nil {
		return ReviewSubmission{}, err
	}
	id = strings.TrimSpace(id)
	if !validPrefixedHexID(id, reviewSubmissionIDPrefix) {
		return ReviewSubmission{}, fmt.Errorf(
			"%w: invalid review submission ID",
			ErrInvalidReview,
		)
	}
	submission, err := scanReviewSubmission(s.db.QueryRowContext(ctx, `
		SELECT `+reviewSubmissionColumns+`
		FROM pr_review_submissions
		WHERE id = ?`,
		id,
	))
	if err != nil {
		return ReviewSubmission{}, s.dbError(err)
	}
	return submission, nil
}

// ClaimReviewSubmissions first terminalizes expired claims as unknown, then
// leases pending work. A worker that vanished while owning a claim may have
// changed GitHub, so its request must never be submitted again blindly.
func (s *Store) ClaimReviewSubmissions(
	ctx context.Context,
	workerLabel string,
	limit int,
	lease time.Duration,
) ([]ReviewSubmission, error) {
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
	now, clockErr := s.currentTime()
	if clockErr != nil {
		return nil, clockErr
	}
	leaseUntil := now.Add(lease)
	if validationErr := validateDBTimestamp("review submission lease deadline", leaseUntil); validationErr != nil {
		return nil, validationErr
	}

	claimed := make([]ReviewSubmission, 0, limit)
	transactionErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		if expireErr := expireReviewSubmissionClaims(ctx, conn, now); expireErr != nil {
			return expireErr
		}
		ids, queryErr := queryIDs(ctx, conn, `
			SELECT id
			FROM pr_review_submissions
			WHERE status = ?
			ORDER BY created_at, id
			LIMIT ?`,
			ReviewSubmissionPending,
			limit,
		)
		if queryErr != nil {
			return queryErr
		}
		for _, id := range ids {
			token, tokenErr := newLeaseToken(workerLabel)
			if tokenErr != nil {
				return tokenErr
			}
			if _, execErr := conn.ExecContext(ctx, `
				UPDATE pr_review_submissions
				SET claim_from = ?, status = ?, owner = ?, lease_until = ?,
				    attempts = attempts + 1, updated_at = ?
				WHERE id = ?`,
				ReviewSubmissionPending,
				ReviewSubmissionClaimed,
				token,
				toDBTime(leaseUntil),
				toDBTime(now),
				id,
			); execErr != nil {
				return execErr
			}
			submission, scanErr := scanReviewSubmission(conn.QueryRowContext(ctx, `
				SELECT `+reviewSubmissionColumns+`
				FROM pr_review_submissions
				WHERE id = ?`,
				id,
			))
			if scanErr != nil {
				return scanErr
			}
			claimed = append(claimed, submission)
		}
		return nil
	})
	if transactionErr != nil {
		return nil, fmt.Errorf(
			"claim pull request review submissions: %w",
			s.dbError(transactionErr),
		)
	}
	return claimed, nil
}

func expireReviewSubmissionClaims(
	ctx context.Context,
	conn *sql.Conn,
	now time.Time,
) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT id, case_id
		FROM pr_review_submissions
		WHERE status = ? AND lease_until <= ?
		ORDER BY created_at, id
		LIMIT ?`,
		ReviewSubmissionClaimed,
		toDBTime(now),
		maxReviewListItems,
	)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type expiredClaim struct {
		id     string
		caseID string
	}
	expired := make([]expiredClaim, 0)
	for rows.Next() {
		var claim expiredClaim
		if err := rows.Scan(&claim.id, &claim.caseID); err != nil {
			return err
		}
		expired = append(expired, claim)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, claim := range expired {
		if _, err := conn.ExecContext(ctx, `
			UPDATE pr_review_submissions
			SET status = ?, claim_from = '', owner = '', lease_until = NULL,
			    public_error_code = ?, internal_error = ?, updated_at = ?
			WHERE id = ? AND status = ?`,
			ReviewSubmissionUnknown,
			"worker_outcome_unknown",
			"submission worker lease expired before a durable outcome was recorded",
			toDBTime(now),
			claim.id,
			ReviewSubmissionClaimed,
		); err != nil {
			return err
		}
		result, err := conn.ExecContext(ctx, `
			UPDATE pr_review_cases
			SET status = ?, version = version + 1, public_error_code = ?,
			    updated_at = ?, resolved_at = ?
			WHERE id = ? AND status = ?`,
			ReviewCaseSubmissionUnknown,
			"worker_outcome_unknown",
			toDBTime(now),
			toDBTime(now),
			claim.caseID,
			ReviewCaseSubmitting,
		)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf(
				"%w: expired review submission belongs to a non-submitting case",
				ErrInvalidTransition,
			)
		}
	}
	return nil
}

// RenewReviewSubmissionLease extends a live owned outbox claim.
func (s *Store) RenewReviewSubmissionLease(
	ctx context.Context,
	id, leaseToken string,
	lease time.Duration,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	leaseToken = strings.TrimSpace(leaseToken)
	if !validPrefixedHexID(id, reviewSubmissionIDPrefix) ||
		leaseToken == "" ||
		lease <= 0 {
		return fmt.Errorf(
			"%w: valid submission ID, lease token, and positive lease are required",
			ErrInvalidReview,
		)
	}
	now, clockErr := s.currentTime()
	if clockErr != nil {
		return clockErr
	}
	leaseUntil := now.Add(lease)
	if validationErr := validateDBTimestamp("review submission lease deadline", leaseUntil); validationErr != nil {
		return validationErr
	}
	result, execErr := s.db.ExecContext(ctx, `
		UPDATE pr_review_submissions
		SET lease_until = ?, updated_at = ?
		WHERE id = ? AND status = ? AND owner = ? AND lease_until > ?`,
		toDBTime(leaseUntil),
		toDBTime(now),
		id,
		ReviewSubmissionClaimed,
		leaseToken,
		toDBTime(now),
	)
	if execErr != nil {
		return fmt.Errorf("renew review submission lease: %w", s.dbError(execErr))
	}
	return s.requireLeaseUpdate(ctx, result, "pr_review_submissions", id)
}

// FinishReviewSubmission records a fenced outbox outcome and advances the
// owning review case state atomically.
func (s *Store) FinishReviewSubmission(
	ctx context.Context,
	input ReviewSubmissionOutcome,
) (ReviewCaseDetail, error) {
	if err := s.ready(ctx); err != nil {
		return ReviewCaseDetail{}, err
	}
	input.SubmissionID = strings.TrimSpace(input.SubmissionID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	input.PublicErrorCode = strings.TrimSpace(input.PublicErrorCode)
	input.ExternalReviewID = strings.TrimSpace(input.ExternalReviewID)
	input.ExternalURL = strings.TrimSpace(input.ExternalURL)
	if !validPrefixedHexID(input.SubmissionID, reviewSubmissionIDPrefix) ||
		input.LeaseToken == "" {
		return ReviewCaseDetail{}, fmt.Errorf(
			"%w: valid submission ID and lease token are required",
			ErrInvalidReview,
		)
	}
	switch input.Status {
	case ReviewSubmissionSubmitted,
		ReviewSubmissionUnknown,
		ReviewSubmissionFailed:
	default:
		return ReviewCaseDetail{}, fmt.Errorf(
			"%w: submission outcome status must be submitted, unknown, or failed",
			ErrInvalidReview,
		)
	}
	if input.Stale && input.Status != ReviewSubmissionFailed {
		return ReviewCaseDetail{}, fmt.Errorf(
			"%w: only a failed submission can mark a review stale",
			ErrInvalidReview,
		)
	}
	for _, field := range []struct {
		name    string
		value   string
		maximum int
	}{
		{name: "public error code", value: input.PublicErrorCode, maximum: 256},
		{name: "external review ID", value: input.ExternalReviewID, maximum: 1024},
		{name: "external review URL", value: input.ExternalURL, maximum: maxReviewURLBytes},
	} {
		if err := validateReviewString(field.name, field.value, field.maximum, false); err != nil {
			return ReviewCaseDetail{}, err
		}
	}
	if input.ExternalURL != "" {
		parsed, err := url.Parse(input.ExternalURL)
		if err != nil ||
			(parsed.Scheme != "https" && parsed.Scheme != "http") ||
			parsed.Host == "" {
			return ReviewCaseDetail{}, fmt.Errorf(
				"%w: external review URL must be absolute HTTP(S)",
				ErrInvalidReview,
			)
		}
	}
	input.InternalError = s.sanitizeDetail(input.InternalError)

	var detail ReviewCaseDetail
	transactionErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, err := s.currentTime()
		if err != nil {
			return err
		}
		submission, err := scanReviewSubmission(conn.QueryRowContext(ctx, `
			SELECT `+reviewSubmissionColumns+`
			FROM pr_review_submissions
			WHERE id = ?`,
			input.SubmissionID,
		))
		if err != nil {
			return err
		}
		if submission.Status != ReviewSubmissionClaimed ||
			submission.LeaseToken != input.LeaseToken ||
			submission.LeaseUntil == nil ||
			!submission.LeaseUntil.After(now) {
			return ErrStaleLease
		}
		reviewCase, err := getReviewCaseRecord(ctx, conn, submission.CaseID)
		if err != nil {
			return err
		}
		if submission.ClaimFrom != ReviewSubmissionPending {
			return fmt.Errorf("stored review submission claim origin is invalid")
		}
		if reviewCase.Status != ReviewCaseSubmitting {
			return fmt.Errorf(
				"%w: pending submission belongs to case state %q",
				ErrInvalidTransition,
				reviewCase.Status,
			)
		}

		caseStatus := ReviewCaseOpen
		var resolvedAt, caseSubmittedAt, submissionSubmittedAt any
		switch input.Status {
		case ReviewSubmissionSubmitted:
			caseStatus = ReviewCaseSubmitted
			resolvedAt = toDBTime(now)
			caseSubmittedAt = toDBTime(now)
			submissionSubmittedAt = toDBTime(now)
		case ReviewSubmissionUnknown:
			caseStatus = ReviewCaseSubmissionUnknown
			resolvedAt = toDBTime(now)
		case ReviewSubmissionFailed:
			if input.Stale {
				caseStatus = ReviewCaseStale
				resolvedAt = toDBTime(now)
			}
		}
		if _, execErr := conn.ExecContext(ctx, `
			UPDATE pr_review_submissions
			SET status = ?, claim_from = '', owner = '', lease_until = NULL,
			    public_error_code = ?, internal_error = ?,
			    external_review_id = ?, external_url = ?, updated_at = ?,
			    submitted_at = ?
			WHERE id = ?`,
			input.Status,
			input.PublicErrorCode,
			input.InternalError,
			input.ExternalReviewID,
			input.ExternalURL,
			toDBTime(now),
			submissionSubmittedAt,
			input.SubmissionID,
		); execErr != nil {
			return execErr
		}
		if _, execErr := conn.ExecContext(ctx, `
			UPDATE pr_review_cases
			SET status = ?, version = version + 1, public_error_code = ?,
			    updated_at = ?, resolved_at = ?, submitted_at = ?
			WHERE id = ?`,
			caseStatus,
			input.PublicErrorCode,
			toDBTime(now),
			resolvedAt,
			caseSubmittedAt,
			submission.CaseID,
		); execErr != nil {
			return execErr
		}
		if input.Status == ReviewSubmissionSubmitted {
			if enqueueErr := enqueueReviewAttentionTrigger(
				ctx,
				conn,
				input.SubmissionID,
				submission.CaseID,
				reviewCase.Version+1,
				now,
			); enqueueErr != nil {
				return enqueueErr
			}
		}
		detail, err = getReviewCaseDetailWith(ctx, conn, submission.CaseID)
		return err
	})
	if transactionErr != nil {
		return ReviewCaseDetail{}, fmt.Errorf(
			"finish pull request review submission: %w",
			s.dbError(transactionErr),
		)
	}
	return detail, nil
}

// ReconcileReviewSubmission resolves the latest terminal-unknown submission
// from an explicit human assertion. It never performs or schedules a remote
// write. "absent" reopens a new editable case version; "submitted" closes the
// case as successfully submitted.
func (s *Store) ReconcileReviewSubmission(
	ctx context.Context,
	input ReviewSubmissionReconciliation,
) (ReviewCaseDetail, error) {
	if err := s.ready(ctx); err != nil {
		return ReviewCaseDetail{}, err
	}
	input.CaseID = strings.TrimSpace(input.CaseID)
	if !validPrefixedHexID(input.CaseID, reviewCaseIDPrefix) ||
		input.ExpectedVersion < 1 {
		return ReviewCaseDetail{}, fmt.Errorf(
			"%w: valid case ID and positive expected version are required",
			ErrInvalidReview,
		)
	}
	switch input.Resolution {
	case ReviewReconciliationSubmitted, ReviewReconciliationAbsent:
	default:
		return ReviewCaseDetail{}, fmt.Errorf(
			"%w: reconciliation resolution must be submitted or absent",
			ErrInvalidReview,
		)
	}

	var detail ReviewCaseDetail
	err := s.withImmediate(ctx, func(conn *sql.Conn) error {
		reviewCase, err := requireReviewCaseVersion(
			ctx,
			conn,
			input.CaseID,
			input.ExpectedVersion,
		)
		if err != nil {
			return err
		}
		if reviewCase.Status != ReviewCaseSubmissionUnknown {
			return fmt.Errorf(
				"%w: only a submission-unknown review can be reconciled",
				ErrInvalidTransition,
			)
		}
		submission, err := getLatestReviewSubmission(ctx, conn, input.CaseID)
		if err != nil {
			return err
		}
		if submission == nil ||
			submission.Status != ReviewSubmissionUnknown {
			return fmt.Errorf(
				"%w: review case has no latest unknown submission",
				ErrInvalidTransition,
			)
		}
		now, err := s.currentTime()
		if err != nil {
			return err
		}

		submissionStatus := ReviewSubmissionSubmitted
		submissionPublicCode := ""
		caseStatus := ReviewCaseSubmitted
		var resolvedAt, caseSubmittedAt, submissionSubmittedAt any
		resolvedAt = toDBTime(now)
		caseSubmittedAt = toDBTime(now)
		submissionSubmittedAt = toDBTime(now)
		if input.Resolution == ReviewReconciliationAbsent {
			submissionStatus = ReviewSubmissionFailed
			submissionPublicCode = "reconciled_absent"
			caseStatus = ReviewCaseOpen
			resolvedAt = nil
			caseSubmittedAt = nil
			submissionSubmittedAt = nil
		}

		result, err := conn.ExecContext(ctx, `
			UPDATE pr_review_submissions
			SET status = ?, public_error_code = ?, updated_at = ?,
			    submitted_at = ?
			WHERE id = ? AND case_id = ? AND status = ?`,
			submissionStatus,
			submissionPublicCode,
			toDBTime(now),
			submissionSubmittedAt,
			submission.ID,
			input.CaseID,
			ReviewSubmissionUnknown,
		)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf(
				"%w: unknown submission changed during reconciliation",
				ErrReviewConflict,
			)
		}
		result, err = conn.ExecContext(ctx, `
			UPDATE pr_review_cases
			SET status = ?, version = version + 1, public_error_code = '',
			    updated_at = ?, resolved_at = ?, submitted_at = ?
			WHERE id = ? AND status = ? AND version = ?`,
			caseStatus,
			toDBTime(now),
			resolvedAt,
			caseSubmittedAt,
			input.CaseID,
			ReviewCaseSubmissionUnknown,
			input.ExpectedVersion,
		)
		if err != nil {
			return err
		}
		affected, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf(
				"%w: review case changed during reconciliation",
				ErrReviewConflict,
			)
		}
		if input.Resolution == ReviewReconciliationSubmitted {
			if enqueueErr := enqueueReviewAttentionTrigger(
				ctx,
				conn,
				submission.ID,
				input.CaseID,
				input.ExpectedVersion+1,
				now,
			); enqueueErr != nil {
				return enqueueErr
			}
		}
		detail, err = getReviewCaseDetailWith(ctx, conn, input.CaseID)
		return err
	})
	if err != nil {
		return ReviewCaseDetail{}, fmt.Errorf(
			"reconcile pull request review submission: %w",
			s.dbError(err),
		)
	}
	return detail, nil
}
