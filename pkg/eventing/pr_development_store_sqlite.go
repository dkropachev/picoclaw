//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxPRDevelopmentRepositoryBytes = 256
	maxPRDevelopmentAuthorBytes     = 128
	maxPRDevelopmentRefBytes        = 1024
	maxPRDevelopmentReviewIDBytes   = 19
	maxPRDevelopmentNodeIDBytes     = 1024
	maxPRDevelopmentURLBytes        = 4096
	maxPRDevelopmentFeedbackBytes   = 64 << 10
	maxPRDevelopmentCaptureBytes    = 2 << 20
	maxPRDevelopmentListItems       = 100
)

var (
	_ PRDevelopmentCaseStore  = (*Store)(nil)
	_ PRDevelopmentCaseReader = (*Store)(nil)
)

const prDevelopmentCaseColumns = `
	id, event_id, dispatch_id, run_id, workflow_ref, workflow_revision,
	connector, repository, pull_number, pull_url, pull_author, target_user,
	pull_state, pull_draft, pull_merged, base_repository, base_ref, base_sha,
	head_repository, head_ref, head_sha, review_id, trigger_review_node_id,
	review_author, submitted_review_state, current_review_state,
	review_commit_sha, review_submitted_at, review_url, feedback,
	created_at, updated_at, capture_hash`

type storedPRDevelopmentCase struct {
	Case        PRDevelopmentCase
	CaptureHash string
}

// LookupPRDevelopmentCapture checks exact durable provenance before a caller
// repeats a provider read. A missing case is not an error; malformed or
// mismatched provenance is.
func (s *Store) LookupPRDevelopmentCapture(
	ctx context.Context,
	identity PRDevelopmentCaptureIdentity,
) (PRDevelopmentCase, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentCase{}, false, err
	}
	normalized, err := normalizePRDevelopmentCaptureIdentity(identity)
	if err != nil {
		return PRDevelopmentCase{}, false, err
	}
	if err = verifyPRDevelopmentDispatch(ctx, s.db, normalized); err != nil {
		return PRDevelopmentCase{}, false, fmt.Errorf(
			"lookup pull request development capture: %w",
			s.dbError(err),
		)
	}
	stored, found, err := findPRDevelopmentCaptureByProvenance(ctx, s.db, normalized)
	if err != nil {
		return PRDevelopmentCase{}, false, fmt.Errorf(
			"lookup pull request development capture: %w",
			s.dbError(err),
		)
	}
	if !found {
		return PRDevelopmentCase{}, false, nil
	}
	if prDevelopmentCaseIdentity(stored.Case) != normalized {
		return PRDevelopmentCase{}, false, fmt.Errorf(
			"lookup pull request development capture: %w: dispatch or run provenance differs",
			ErrPRDevelopmentConflict,
		)
	}
	return stored.Case, true, nil
}

// CapturePRDevelopmentCase atomically persists one provider-verified review
// occurrence. Exact dispatch/run retries return the committed row only when
// the complete normalized provenance and provider content remain identical.
func (s *Store) CapturePRDevelopmentCase(
	ctx context.Context,
	input PRDevelopmentCaptureInput,
) (PRDevelopmentCase, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentCase{}, false, err
	}
	normalized, err := normalizePRDevelopmentCapture(input)
	if err != nil {
		return PRDevelopmentCase{}, false, err
	}
	captureHash, err := prDevelopmentCaptureHash(normalized)
	if err != nil {
		return PRDevelopmentCase{}, false, err
	}

	var (
		developmentCase PRDevelopmentCase
		created         bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		if verifyErr := verifyPRDevelopmentDispatch(
			ctx,
			conn,
			normalized.PRDevelopmentCaptureIdentity,
		); verifyErr != nil {
			return verifyErr
		}
		existing, found, findErr := findPRDevelopmentCapture(ctx, conn, normalized)
		if findErr != nil {
			return findErr
		}
		if found {
			if prDevelopmentCaseIdentity(existing.Case) !=
				normalized.PRDevelopmentCaptureIdentity {
				return fmt.Errorf(
					"%w: dispatch or run was captured with different provenance",
					ErrPRDevelopmentConflict,
				)
			}
			if existing.CaptureHash != captureHash {
				return fmt.Errorf(
					"%w: dispatch or run was captured with different content",
					ErrPRDevelopmentConflict,
				)
			}
			developmentCase = existing.Case
			return nil
		}

		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		caseID, idErr := newPrefixedID(prDevelopmentCaseIDPrefix)
		if idErr != nil {
			return idErr
		}
		if _, execErr := conn.ExecContext(ctx, `
			INSERT INTO pr_development_cases (
				id, event_id, dispatch_id, run_id, workflow_ref,
				workflow_revision, connector, repository, pull_number,
				pull_url, pull_author, target_user, pull_state, pull_draft,
				pull_merged, base_repository, base_ref, base_sha,
				head_repository, head_ref, head_sha, review_id,
				trigger_review_node_id, review_author,
				submitted_review_state, current_review_state,
				review_commit_sha, review_submitted_at, review_url,
				feedback, capture_hash, created_at, updated_at
			) VALUES (
				?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
				?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
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
			normalized.PullAuthor,
			normalized.TargetUser,
			normalized.PullState,
			boolDBValue(normalized.PullDraft),
			boolDBValue(normalized.PullMerged),
			normalized.BaseRepository,
			normalized.BaseRef,
			normalized.BaseSHA,
			normalized.HeadRepository,
			normalized.HeadRef,
			normalized.HeadSHA,
			normalized.ReviewID,
			normalized.TriggerReviewNodeID,
			normalized.ReviewAuthor,
			normalized.SubmittedReviewState,
			normalized.CurrentReviewState,
			normalized.ReviewCommitSHA,
			toDBTime(normalized.ReviewSubmittedAt),
			normalized.ReviewURL,
			normalized.Feedback,
			captureHash,
			toDBTime(now),
			toDBTime(now),
		); execErr != nil {
			return execErr
		}
		stored, getErr := getPRDevelopmentCaseRecord(ctx, conn, caseID)
		if getErr != nil {
			return getErr
		}
		developmentCase = stored.Case
		created = true
		return nil
	})
	if err != nil {
		return PRDevelopmentCase{}, false, fmt.Errorf(
			"capture pull request development case: %w",
			s.dbError(err),
		)
	}
	return developmentCase, created, nil
}

// GetPRDevelopmentCase returns one exact immutable development case. It does
// not create chat, checkout, execution, publication, or provider authority.
func (s *Store) GetPRDevelopmentCase(
	ctx context.Context,
	id string,
) (PRDevelopmentCase, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentCase{}, err
	}
	id = strings.TrimSpace(id)
	if !validPrefixedHexID(id, prDevelopmentCaseIDPrefix) {
		return PRDevelopmentCase{}, fmt.Errorf(
			"%w: invalid development case ID",
			ErrInvalidPRDevelopment,
		)
	}
	stored, err := getPRDevelopmentCaseRecord(ctx, s.db, id)
	if err != nil {
		return PRDevelopmentCase{}, s.dbError(err)
	}
	return stored.Case, nil
}

// ListPRDevelopmentCases returns immutable captures newest first. Pagination
// is stable because capture rows never change after insertion and the keyset
// uses the complete required list ordering, including the unique case ID.
func (s *Store) ListPRDevelopmentCases(
	ctx context.Context,
	filter PRDevelopmentCaseFilter,
) (PRDevelopmentCasePage, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentCasePage{}, err
	}
	plan, err := buildPRDevelopmentCaseListPlan(filter)
	if err != nil {
		return PRDevelopmentCasePage{}, err
	}
	cases, next, err := collectListPage(
		ctx,
		s,
		plan,
		func(scanner rowScanner) (PRDevelopmentCase, error) {
			stored, scanErr := scanPRDevelopmentCase(scanner)
			return stored.Case, scanErr
		},
		func(developmentCase PRDevelopmentCase) PRDevelopmentCaseCursor {
			return PRDevelopmentCaseCursor{
				UpdatedAt: developmentCase.UpdatedAt,
				ID:        developmentCase.ID,
			}
		},
		listErrorContext{
			query:   "list pull request development cases",
			scan:    "scan pull request development case list",
			iterate: "iterate pull request development case list",
		},
	)
	if err != nil {
		return PRDevelopmentCasePage{}, err
	}
	return PRDevelopmentCasePage{Cases: cases, Next: next}, nil
}

func buildPRDevelopmentCaseListPlan(
	filter PRDevelopmentCaseFilter,
) (listPlan, error) {
	filter.Repository = strings.TrimSpace(filter.Repository)
	if filter.Repository != "" &&
		!validPRDevelopmentRepository(filter.Repository) {
		return listPlan{}, fmt.Errorf(
			"%w: development-case repository filter is invalid",
			ErrInvalidPRDevelopment,
		)
	}
	if filter.PullNumber < 0 || filter.PullNumber > maxReviewPullNumber {
		return listPlan{}, fmt.Errorf(
			"%w: development-case pull number filter must be between 0 and %d",
			ErrInvalidPRDevelopment,
			maxReviewPullNumber,
		)
	}

	var after *listPosition
	if filter.After != nil {
		if err := validateDBTimestamp(
			"development-case cursor updated_at",
			filter.After.UpdatedAt,
		); err != nil {
			return listPlan{}, fmt.Errorf("%w: %v", ErrInvalidPRDevelopment, err)
		}
		cursorID := strings.TrimSpace(filter.After.ID)
		if !validPrefixedHexID(cursorID, prDevelopmentCaseIDPrefix) {
			return listPlan{}, fmt.Errorf(
				"%w: development-case cursor ID is invalid",
				ErrInvalidPRDevelopment,
			)
		}
		after = &listPosition{at: filter.After.UpdatedAt, id: cursorID}
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxPRDevelopmentListItems {
		limit = maxPRDevelopmentListItems
	}
	return buildListPlan(
		prDevelopmentCaseColumns,
		"pr_development_cases",
		"updated_at",
		[]listFilter{
			{
				column:  "repository",
				value:   filter.Repository,
				enabled: filter.Repository != "",
			},
			{
				column:  "pull_number",
				value:   filter.PullNumber,
				enabled: filter.PullNumber > 0,
			},
		},
		after,
		limit,
	), nil
}

func findPRDevelopmentCaptureByProvenance(
	ctx context.Context,
	queryer reviewQueryer,
	identity PRDevelopmentCaptureIdentity,
) (storedPRDevelopmentCase, bool, error) {
	return queryOnePRDevelopmentCandidate(ctx, queryer, `
		SELECT `+prDevelopmentCaseColumns+`
		FROM pr_development_cases
		WHERE dispatch_id = ? OR run_id = ?
		ORDER BY id`,
		identity.DispatchID,
		identity.RunID,
	)
}

func findPRDevelopmentCapture(
	ctx context.Context,
	queryer reviewQueryer,
	input PRDevelopmentCaptureInput,
) (storedPRDevelopmentCase, bool, error) {
	return queryOnePRDevelopmentCandidate(ctx, queryer, `
		SELECT `+prDevelopmentCaseColumns+`
		FROM pr_development_cases
		WHERE dispatch_id = ? OR run_id = ?
		ORDER BY id`,
		input.DispatchID,
		input.RunID,
	)
}

func queryOnePRDevelopmentCandidate(
	ctx context.Context,
	queryer reviewQueryer,
	query string,
	args ...any,
) (storedPRDevelopmentCase, bool, error) {
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return storedPRDevelopmentCase{}, false, err
	}
	defer rows.Close()

	var candidate storedPRDevelopmentCase
	found := false
	for rows.Next() {
		stored, scanErr := scanPRDevelopmentCase(rows)
		if scanErr != nil {
			return storedPRDevelopmentCase{}, false, scanErr
		}
		if found && candidate.Case.ID != stored.Case.ID {
			return storedPRDevelopmentCase{}, false, fmt.Errorf(
				"%w: dispatch and run resolve to different cases",
				ErrPRDevelopmentConflict,
			)
		}
		candidate = stored
		found = true
	}
	if err := rows.Err(); err != nil {
		return storedPRDevelopmentCase{}, false, err
	}
	return candidate, found, nil
}

func getPRDevelopmentCaseRecord(
	ctx context.Context,
	queryer rowQueryer,
	id string,
) (storedPRDevelopmentCase, error) {
	return scanPRDevelopmentCase(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentCaseColumns+`
		FROM pr_development_cases
		WHERE id = ?`,
		id,
	))
}

func scanPRDevelopmentCase(scanner rowScanner) (storedPRDevelopmentCase, error) {
	var (
		stored                storedPRDevelopmentCase
		pullNumber            int64
		pullDraft, pullMerged int64
		reviewSubmittedAt     int64
		createdAt, updatedAt  int64
	)
	err := scanner.Scan(
		&stored.Case.ID,
		&stored.Case.EventID,
		&stored.Case.DispatchID,
		&stored.Case.RunID,
		&stored.Case.WorkflowRef,
		&stored.Case.WorkflowRevision,
		&stored.Case.Connector,
		&stored.Case.Repository,
		&pullNumber,
		&stored.Case.PullURL,
		&stored.Case.PullAuthor,
		&stored.Case.TargetUser,
		&stored.Case.PullState,
		&pullDraft,
		&pullMerged,
		&stored.Case.BaseRepository,
		&stored.Case.BaseRef,
		&stored.Case.BaseSHA,
		&stored.Case.HeadRepository,
		&stored.Case.HeadRef,
		&stored.Case.HeadSHA,
		&stored.Case.ReviewID,
		&stored.Case.TriggerReviewNodeID,
		&stored.Case.ReviewAuthor,
		&stored.Case.SubmittedReviewState,
		&stored.Case.CurrentReviewState,
		&stored.Case.ReviewCommitSHA,
		&reviewSubmittedAt,
		&stored.Case.ReviewURL,
		&stored.Case.Feedback,
		&createdAt,
		&updatedAt,
		&stored.CaptureHash,
	)
	if err != nil {
		return storedPRDevelopmentCase{}, err
	}
	if pullNumber <= 0 ||
		(pullDraft != 0 && pullDraft != 1) ||
		(pullMerged != 0 && pullMerged != 1) {
		return storedPRDevelopmentCase{}, errors.New(
			"stored pull request development case integer is invalid",
		)
	}
	stored.Case.PullNumber = pullNumber
	stored.Case.PullDraft = pullDraft == 1
	stored.Case.PullMerged = pullMerged == 1
	stored.Case.ReviewSubmittedAt = fromDBTime(reviewSubmittedAt)
	stored.Case.CreatedAt = fromDBTime(createdAt)
	stored.Case.UpdatedAt = fromDBTime(updatedAt)

	if !validPrefixedHexID(stored.Case.ID, prDevelopmentCaseIDPrefix) ||
		!validPRDevelopmentHex(stored.CaptureHash, 64) ||
		stored.Case.UpdatedAt.Before(stored.Case.CreatedAt) {
		return storedPRDevelopmentCase{}, errors.New(
			"stored pull request development case identity is invalid",
		)
	}
	input := stored.Case.PRDevelopmentCaptureInput
	normalized, normalizeErr := normalizePRDevelopmentCapture(input)
	if normalizeErr != nil {
		return storedPRDevelopmentCase{}, fmt.Errorf(
			"stored pull request development case content is invalid: %w",
			normalizeErr,
		)
	}
	if normalized != input {
		return storedPRDevelopmentCase{}, errors.New(
			"stored pull request development case content is not canonical",
		)
	}
	captureHash, hashErr := prDevelopmentCaptureHash(normalized)
	if hashErr != nil || captureHash != stored.CaptureHash {
		return storedPRDevelopmentCase{}, errors.New(
			"stored pull request development case capture hash is invalid",
		)
	}
	return stored, nil
}

func verifyPRDevelopmentDispatch(
	ctx context.Context,
	queryer rowQueryer,
	identity PRDevelopmentCaptureIdentity,
) error {
	var eventID, runID, workflowRef, workflowRevision, connector string
	err := queryer.QueryRowContext(ctx, `
		SELECT d.event_id, d.run_id, d.workflow_ref,
		       COALESCE(r.workflow_revision, ''), e.connector
		FROM event_dispatches d
		JOIN event_inbox e ON e.id = d.event_id
		LEFT JOIN event_dispatch_workflow_revisions r ON r.dispatch_id = d.id
		WHERE d.id = ?`,
		identity.DispatchID,
	).Scan(
		&eventID,
		&runID,
		&workflowRef,
		&workflowRevision,
		&connector,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: dispatch %q", ErrNotFound, identity.DispatchID)
	}
	if err != nil {
		return err
	}
	if eventID != identity.EventID ||
		runID != identity.RunID ||
		workflowRef != identity.WorkflowRef ||
		workflowRevision != identity.WorkflowRevision ||
		connector != identity.Connector {
		return fmt.Errorf(
			"%w: trusted development-case identity does not match dispatch",
			ErrPRDevelopmentConflict,
		)
	}
	return nil
}

func normalizePRDevelopmentCaptureIdentity(
	identity PRDevelopmentCaptureIdentity,
) (PRDevelopmentCaptureIdentity, error) {
	identity.EventID = strings.TrimSpace(identity.EventID)
	identity.DispatchID = strings.TrimSpace(identity.DispatchID)
	identity.RunID = strings.TrimSpace(identity.RunID)
	identity.WorkflowRef = strings.TrimSpace(identity.WorkflowRef)
	identity.WorkflowRevision = strings.TrimSpace(identity.WorkflowRevision)
	identity.Connector = strings.TrimSpace(identity.Connector)
	if !validEventID(identity.EventID) {
		return PRDevelopmentCaptureIdentity{}, fmt.Errorf(
			"%w: invalid event ID",
			ErrInvalidPRDevelopment,
		)
	}
	if !validPrefixedHexID(identity.DispatchID, "dsp_") {
		return PRDevelopmentCaptureIdentity{}, fmt.Errorf(
			"%w: invalid dispatch ID",
			ErrInvalidPRDevelopment,
		)
	}
	if !validPrefixedHexID(identity.RunID, "wr_") {
		return PRDevelopmentCaptureIdentity{}, fmt.Errorf(
			"%w: invalid workflow run ID",
			ErrInvalidPRDevelopment,
		)
	}
	for _, field := range []struct {
		name    string
		value   string
		maximum int
	}{
		{name: "workflow reference", value: identity.WorkflowRef, maximum: maxWorkflowRefLength},
		{name: "workflow revision", value: identity.WorkflowRevision, maximum: maxWorkflowRevisionLength},
		{name: "connector", value: identity.Connector, maximum: maxConnectorLength},
	} {
		if err := validatePRDevelopmentString(
			field.name,
			field.value,
			field.maximum,
			true,
		); err != nil {
			return PRDevelopmentCaptureIdentity{}, err
		}
	}
	return identity, nil
}

func normalizePRDevelopmentCapture(
	input PRDevelopmentCaptureInput,
) (PRDevelopmentCaptureInput, error) {
	identity, err := normalizePRDevelopmentCaptureIdentity(
		input.PRDevelopmentCaptureIdentity,
	)
	if err != nil {
		return PRDevelopmentCaptureInput{}, err
	}
	input.PRDevelopmentCaptureIdentity = identity
	input.Repository = strings.TrimSpace(input.Repository)
	input.PullURL = strings.TrimSpace(input.PullURL)
	input.PullAuthor = strings.TrimSpace(input.PullAuthor)
	input.TargetUser = strings.TrimSpace(input.TargetUser)
	input.BaseRepository = strings.TrimSpace(input.BaseRepository)
	input.BaseRef = strings.TrimSpace(input.BaseRef)
	input.BaseSHA = strings.TrimSpace(input.BaseSHA)
	input.HeadRepository = strings.TrimSpace(input.HeadRepository)
	input.HeadRef = strings.TrimSpace(input.HeadRef)
	input.HeadSHA = strings.TrimSpace(input.HeadSHA)
	input.ReviewID = strings.TrimSpace(input.ReviewID)
	input.TriggerReviewNodeID = strings.TrimSpace(input.TriggerReviewNodeID)
	input.ReviewAuthor = strings.TrimSpace(input.ReviewAuthor)
	input.ReviewCommitSHA = strings.TrimSpace(input.ReviewCommitSHA)
	input.ReviewURL = strings.TrimSpace(input.ReviewURL)

	for name, repository := range map[string]string{
		"repository":      input.Repository,
		"base repository": input.BaseRepository,
		"head repository": input.HeadRepository,
	} {
		if !validPRDevelopmentRepository(repository) {
			return PRDevelopmentCaptureInput{}, fmt.Errorf(
				"%w: %s must be a canonical owner/repository identity",
				ErrInvalidPRDevelopment,
				name,
			)
		}
	}
	if !strings.EqualFold(input.Repository, input.BaseRepository) {
		return PRDevelopmentCaptureInput{}, fmt.Errorf(
			"%w: repository and current base repository must match",
			ErrInvalidPRDevelopment,
		)
	}
	if input.PullNumber <= 0 || input.PullNumber > maxReviewPullNumber {
		return PRDevelopmentCaptureInput{}, fmt.Errorf(
			"%w: pull number must be between 1 and %d",
			ErrInvalidPRDevelopment,
			maxReviewPullNumber,
		)
	}
	if err := validatePRDevelopmentURL("pull URL", input.PullURL); err != nil {
		return PRDevelopmentCaptureInput{}, err
	}
	if !validPRDevelopmentLogin(input.PullAuthor, false) ||
		!validPRDevelopmentLogin(input.TargetUser, false) {
		return PRDevelopmentCaptureInput{}, fmt.Errorf(
			"%w: pull author and target user must be canonical provider logins",
			ErrInvalidPRDevelopment,
		)
	}
	if !strings.EqualFold(input.PullAuthor, input.TargetUser) {
		return PRDevelopmentCaptureInput{}, fmt.Errorf(
			"%w: pull author does not match target user",
			ErrInvalidPRDevelopment,
		)
	}
	if input.PullState != PRDevelopmentPullOpen &&
		input.PullState != PRDevelopmentPullClosed {
		return PRDevelopmentCaptureInput{}, fmt.Errorf(
			"%w: pull state must be open or closed",
			ErrInvalidPRDevelopment,
		)
	}
	if input.PullMerged &&
		(input.PullState != PRDevelopmentPullClosed || input.PullDraft) {
		return PRDevelopmentCaptureInput{}, fmt.Errorf(
			"%w: a merged pull request must be closed and not draft",
			ErrInvalidPRDevelopment,
		)
	}
	for name, ref := range map[string]string{
		"base ref": input.BaseRef,
		"head ref": input.HeadRef,
	} {
		if !validPRDevelopmentGitRef(ref) {
			return PRDevelopmentCaptureInput{}, fmt.Errorf(
				"%w: %s is not a canonical Git reference",
				ErrInvalidPRDevelopment,
				name,
			)
		}
	}
	for name, sha := range map[string]string{
		"base SHA":          input.BaseSHA,
		"head SHA":          input.HeadSHA,
		"review commit SHA": input.ReviewCommitSHA,
	} {
		if !validPRDevelopmentHex(sha, 40, 64) {
			return PRDevelopmentCaptureInput{}, fmt.Errorf(
				"%w: %s must be 40 or 64 lowercase hexadecimal characters",
				ErrInvalidPRDevelopment,
				name,
			)
		}
	}
	if !validPRDevelopmentDecimalID(input.ReviewID) {
		return PRDevelopmentCaptureInput{}, fmt.Errorf(
			"%w: review ID must be a canonical positive decimal string",
			ErrInvalidPRDevelopment,
		)
	}
	if !validPRDevelopmentNodeID(input.TriggerReviewNodeID) {
		return PRDevelopmentCaptureInput{}, fmt.Errorf(
			"%w: trigger review node ID is invalid",
			ErrInvalidPRDevelopment,
		)
	}
	if !validPRDevelopmentLogin(input.ReviewAuthor, true) ||
		strings.EqualFold(input.ReviewAuthor, input.TargetUser) {
		return PRDevelopmentCaptureInput{}, fmt.Errorf(
			"%w: review author must be a distinct canonical provider login",
			ErrInvalidPRDevelopment,
		)
	}
	if !validPRDevelopmentSubmittedReviewState(input.SubmittedReviewState) {
		return PRDevelopmentCaptureInput{}, fmt.Errorf(
			"%w: submitted review state is invalid",
			ErrInvalidPRDevelopment,
		)
	}
	if input.CurrentReviewState != input.SubmittedReviewState &&
		input.CurrentReviewState != PRDevelopmentReviewDismissed {
		return PRDevelopmentCaptureInput{}, fmt.Errorf(
			"%w: current review state must equal submitted state or be dismissed",
			ErrInvalidPRDevelopment,
		)
	}
	if input.ReviewSubmittedAt.IsZero() {
		return PRDevelopmentCaptureInput{}, fmt.Errorf(
			"%w: review submitted time is required",
			ErrInvalidPRDevelopment,
		)
	}
	_, offset := input.ReviewSubmittedAt.Zone()
	if offset != 0 {
		return PRDevelopmentCaptureInput{}, fmt.Errorf(
			"%w: review submitted time must be UTC",
			ErrInvalidPRDevelopment,
		)
	}
	if err := validateDBTimestamp(
		"review submitted time",
		input.ReviewSubmittedAt,
	); err != nil {
		return PRDevelopmentCaptureInput{}, fmt.Errorf(
			"%w: %v",
			ErrInvalidPRDevelopment,
			err,
		)
	}
	input.ReviewSubmittedAt = time.Unix(
		0,
		input.ReviewSubmittedAt.UnixNano(),
	).UTC()
	if err := validatePRDevelopmentURL("review URL", input.ReviewURL); err != nil {
		return PRDevelopmentCaptureInput{}, err
	}
	if err := validatePRDevelopmentFeedback(input.Feedback); err != nil {
		return PRDevelopmentCaptureInput{}, err
	}
	return input, nil
}

func prDevelopmentCaptureHash(input PRDevelopmentCaptureInput) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf(
			"%w: encode capture identity: %v",
			ErrInvalidPRDevelopment,
			err,
		)
	}
	if len(encoded) > maxPRDevelopmentCaptureBytes {
		return "", fmt.Errorf(
			"%w: normalized development capture exceeds %d bytes",
			ErrInvalidPRDevelopment,
			maxPRDevelopmentCaptureBytes,
		)
	}
	digest := sha256.Sum256(append(
		[]byte("picoclaw-pr-development-capture-v1\x00"),
		encoded...,
	))
	return hex.EncodeToString(digest[:]), nil
}

func prDevelopmentCaseIdentity(
	developmentCase PRDevelopmentCase,
) PRDevelopmentCaptureIdentity {
	return developmentCase.PRDevelopmentCaptureIdentity
}

func validatePRDevelopmentString(
	field, value string,
	maximum int,
	required bool,
) error {
	if required && value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidPRDevelopment, field)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf(
			"%w: %s is not valid UTF-8",
			ErrInvalidPRDevelopment,
			field,
		)
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf(
			"%w: %s contains a NUL byte",
			ErrInvalidPRDevelopment,
			field,
		)
	}
	if len(value) > maximum {
		return fmt.Errorf(
			"%w: %s exceeds %d bytes",
			ErrInvalidPRDevelopment,
			field,
			maximum,
		)
	}
	return nil
}

func validatePRDevelopmentFeedback(value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf(
			"%w: review feedback is not valid UTF-8",
			ErrInvalidPRDevelopment,
		)
	}
	if len(value) > maxPRDevelopmentFeedbackBytes {
		return fmt.Errorf(
			"%w: review feedback exceeds %d bytes",
			ErrInvalidPRDevelopment,
			maxPRDevelopmentFeedbackBytes,
		)
	}
	return nil
}

func validPRDevelopmentRepository(value string) bool {
	if value == "" ||
		len(value) > maxPRDevelopmentRepositoryBytes ||
		!utf8.ValidString(value) ||
		value != strings.TrimSpace(value) {
		return false
	}
	owner, repository, found := strings.Cut(value, "/")
	if !found || owner == "" || repository == "" || strings.Contains(repository, "/") {
		return false
	}
	return validPRDevelopmentRepositorySegment(owner) &&
		validPRDevelopmentRepositorySegment(repository)
}

func validPRDevelopmentRepositorySegment(value string) bool {
	for _, char := range []byte(value) {
		if char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' ||
			char == '_' || char == '-' || char == '.' {
			continue
		}
		return false
	}
	return value != ""
}

func validPRDevelopmentLogin(value string, allowBot bool) bool {
	if value == "" ||
		len(value) > maxPRDevelopmentAuthorBytes ||
		value != strings.TrimSpace(value) {
		return false
	}
	if allowBot {
		if base, bot := strings.CutSuffix(value, "[bot]"); bot {
			return validPRDevelopmentLogin(base, false)
		}
	}
	for index, char := range []byte(value) {
		if char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' ||
			char == '-' && index > 0 && index < len(value)-1 {
			continue
		}
		return false
	}
	return true
}

func validPRDevelopmentGitRef(value string) bool {
	if value == "" ||
		len(value) > maxPRDevelopmentRefBytes ||
		value != strings.TrimSpace(value) ||
		value == "@" ||
		strings.HasPrefix(value, "/") ||
		strings.HasSuffix(value, "/") ||
		strings.Contains(value, "//") ||
		strings.Contains(value, "..") ||
		strings.Contains(value, "@{") {
		return false
	}
	for _, char := range []byte(value) {
		if char <= ' ' || char == 0x7f || strings.ContainsRune("~^:?*[\\", rune(char)) {
			return false
		}
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" ||
			strings.HasPrefix(component, ".") ||
			strings.HasSuffix(component, ".") ||
			strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return utf8.ValidString(value)
}

func validPRDevelopmentHex(value string, lengths ...int) bool {
	validLength := false
	for _, length := range lengths {
		validLength = validLength || len(value) == length
	}
	if !validLength {
		return false
	}
	for _, char := range []byte(value) {
		if char >= '0' && char <= '9' || char >= 'a' && char <= 'f' {
			continue
		}
		return false
	}
	return true
}

func validPRDevelopmentDecimalID(value string) bool {
	if value == "" ||
		len(value) > maxPRDevelopmentReviewIDBytes {
		return false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == value
}

func validPRDevelopmentNodeID(value string) bool {
	if value == "" ||
		len(value) > maxPRDevelopmentNodeIDBytes ||
		value != strings.TrimSpace(value) {
		return false
	}
	for _, char := range []byte(value) {
		if char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' ||
			char == '_' || char == '-' || char == '+' ||
			char == '/' || char == '=' {
			continue
		}
		return false
	}
	return true
}

func validPRDevelopmentSubmittedReviewState(
	state PRDevelopmentReviewState,
) bool {
	switch state {
	case PRDevelopmentReviewApproved,
		PRDevelopmentReviewChangesRequested,
		PRDevelopmentReviewCommented:
		return true
	default:
		return false
	}
}

func validatePRDevelopmentURL(field, value string) error {
	if err := validatePRDevelopmentString(
		field,
		value,
		maxPRDevelopmentURLBytes,
		true,
	); err != nil {
		return err
	}
	parsed, err := url.Parse(value)
	if err != nil ||
		parsed.Scheme != "https" ||
		parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf(
			"%w: %s must be an absolute HTTPS URL without user information",
			ErrInvalidPRDevelopment,
			field,
		)
	}
	return nil
}

func boolDBValue(value bool) int {
	if value {
		return 1
	}
	return 0
}
