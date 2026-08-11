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

const prDevelopmentCaseLocalAttentionRequiredPredicate = `EXISTS (
		SELECT 1
		FROM pr_development_thread_cases AS membership
		JOIN pr_development_threads AS development_thread
		  ON development_thread.id = membership.thread_id
		 AND development_thread.identity_kind = 'provider'
		JOIN pr_development_thread_controllers AS controller
		  ON controller.thread_id = membership.thread_id
		JOIN pr_development_repair_sessions AS owner_session
		  ON owner_session.id = controller.owner_session_id
		 AND owner_session.case_id = membership.case_id
		JOIN pr_development_repair_attempts AS attempt
		  ON attempt.id = controller.current_attempt_id
		 AND attempt.session_id = owner_session.id
		JOIN pr_development_ledger_entries AS review
		  ON review.thread_id = membership.thread_id
		 AND review.ordinal = (
			SELECT MAX(tail.ordinal)
			FROM pr_development_ledger_entries AS tail
			WHERE tail.thread_id = membership.thread_id
		 )
		JOIN pr_development_attempt_review_fences AS fence
		  ON fence.attempt_id = attempt.id
		 AND fence.controller_id = controller.id
		 AND fence.thread_id = membership.thread_id
		JOIN pr_development_attention_triggers AS attention
		  ON attention.review_entry_id = review.id
		 AND attention.review_entry_hash = review.entry_hash
		 AND attention.case_id = membership.case_id
		JOIN pr_development_conversations AS conversation
		  ON conversation.case_id = membership.case_id
		 AND conversation.version >= attention.conversation_version
		WHERE membership.case_id = pr_development_cases.id
		  AND review.kind = 'review'
		  AND review.case_id = membership.case_id
		  AND review.case_ordinal = membership.ordinal
		  AND review.attempt_id = attempt.id
		  AND review.fence_ordinal = fence.ordinal
		  AND review.fence_hash = fence.fence_hash
		  AND review.review_outcome = 'attention_required'
		  AND attention.decision_point = 'pr_development.review_attention_required'
		  AND attention.status IN ('pending', 'claimed', 'delivered')
		  AND controller.phase = 'ready'
		  AND controller.lease_kind = ''
		  AND controller.lease_owner = ''
		  AND controller.lease_token = ''
		  AND controller.lease_until IS NULL
		  AND controller.mutation_reservation_key = ''
		  AND controller.fence_count = fence.ordinal + 1
		  AND controller.fences_digest = fence.fence_hash
		  AND controller.line_version = fence.line_version
		  AND attempt.status = 'completed'
		  AND attempt.lease_owner = ''
		  AND attempt.lease_token = ''
		  AND attempt.lease_until IS NULL
		  AND attempt.ordinal = (
			SELECT MAX(owner_attempt.ordinal)
			FROM pr_development_repair_attempts AS owner_attempt
			WHERE owner_attempt.session_id = owner_session.id
		  )
		  AND fence.reviewed_at IS NOT NULL
		  AND (
			attention.status <> 'delivered' OR EXISTS (
				SELECT 1
				FROM pr_development_attention_decision_runs AS decision_run
				WHERE decision_run.case_id = attention.case_id
				  AND decision_run.review_entry_id = attention.review_entry_id
				  AND decision_run.review_entry_hash = attention.review_entry_hash
				  AND decision_run.conversation_version = attention.conversation_version
				  AND decision_run.subject_revision = attention.subject_revision
				  AND decision_run.decision_point = attention.decision_point
				  AND decision_run.policy_revision = attention.policy_revision
				  AND decision_run.run_id = attention.run_id
			)
		  )
	)`

// The publication predicate mirrors the local durable portion of
// loadCurrentPRDevelopmentPublicationHighWater. It deliberately does not load
// or inspect the private workflow store: a linked active gate wait is a coarse
// browser hint only, never response or effect authority.
const prDevelopmentCasePublicationAttentionRequiredPredicate = `EXISTS (
		SELECT 1
		FROM pr_development_thread_cases AS publication_membership
		JOIN pr_development_threads AS publication_thread
		  ON publication_thread.id = publication_membership.thread_id
		 AND publication_thread.identity_kind = 'provider'
		JOIN pr_development_ledger_entries AS publication_review
		  ON publication_review.thread_id = publication_membership.thread_id
		 AND publication_review.ordinal = (
			SELECT MAX(publication_tail.ordinal)
			FROM pr_development_ledger_entries AS publication_tail
			WHERE publication_tail.thread_id = publication_membership.thread_id
		 )
		JOIN pr_development_publications AS publication
		  ON publication.review_ledger_entry_id = publication_review.id
		 AND publication.review_ledger_entry_kind = publication_review.kind
		 AND publication.review_ledger_entry_hash = publication_review.entry_hash
		JOIN pr_development_ledger_entries AS publication_attempt_entry
		  ON publication_attempt_entry.id = publication.attempt_ledger_entry_id
		 AND publication_attempt_entry.kind = publication.attempt_ledger_entry_kind
		 AND publication_attempt_entry.entry_hash = publication.attempt_ledger_entry_hash
		JOIN pr_development_repair_sessions AS publication_owner_session
		  ON publication_owner_session.id = publication.owner_session_id
		 AND publication_owner_session.case_id = publication_membership.case_id
		JOIN pr_development_repair_attempts AS publication_attempt
		  ON publication_attempt.id = publication.attempt_id
		 AND publication_attempt.session_id = publication_owner_session.id
		JOIN pr_development_thread_controllers AS publication_controller
		  ON publication_controller.id = publication.controller_id
		 AND publication_controller.thread_id = publication_membership.thread_id
		 AND publication_controller.owner_session_id = publication_owner_session.id
		JOIN pr_development_attempt_review_fences AS publication_fence
		  ON publication_fence.attempt_id = publication_attempt.id
		 AND publication_fence.controller_id = publication_controller.id
		 AND publication_fence.thread_id = publication_membership.thread_id
		JOIN pr_development_repair_orchestrations AS publication_orchestration
		  ON publication_orchestration.attempt_id = publication_attempt.id
		 AND publication_orchestration.receipt_hash = publication.orchestration_receipt_hash
		WHERE publication_membership.case_id = pr_development_cases.id
		  AND publication_membership.ordinal = publication_thread.case_count - 1
		  AND NOT EXISTS (
			SELECT 1
			FROM pr_development_thread_cases AS later_publication_membership
			WHERE later_publication_membership.thread_id = publication_membership.thread_id
			  AND later_publication_membership.ordinal > publication_membership.ordinal
		  )
		  AND publication.case_id = publication_membership.case_id
		  AND publication.thread_id = publication_membership.thread_id
		  AND publication.review_outcome = 'passed'
		  AND publication.orchestration_phase = 'completed'
		  AND publication.ci_status = 'passed'
		  AND publication.decision_run_id <> ''
		  AND length(publication.policy_revision) = 71
		  AND length(publication.pinned_policy_json) >= 2
		  AND length(publication.pinned_policy_hash) = 64
		  AND length(publication.subject_revision) = 71
		  AND length(publication.pinned_subject_json) >= 2
		  AND length(publication.pinned_subject_hash) = 64
		  AND length(publication.provider_observation_json) >= 2
		  AND length(publication.provider_observation_hash) = 64
		  AND publication.provider_pinned_at IS NOT NULL
		  AND publication.provider_observed_at IS NOT NULL
		  AND (
			publication.status = 'gate_waiting' OR
			(publication.status = 'claimed' AND publication.claim_from = 'gate_waiting')
		  )
		  AND publication.completed_at IS NULL
		  AND publication_review.kind = 'review'
		  AND publication_review.case_id = publication_membership.case_id
		  AND publication_review.case_ordinal = publication_membership.ordinal
		  AND publication_review.attempt_id = publication_attempt.id
		  AND publication_review.fence_ordinal = publication.fence_ordinal
		  AND publication_review.fence_hash = publication.fence_hash
		  AND publication_review.review_outcome = 'passed'
		  AND publication_review.finding_count = 0
		  AND publication_attempt_entry.thread_id = publication_membership.thread_id
		  AND publication_attempt_entry.kind = 'attempt'
		  AND publication_attempt_entry.case_id = publication_membership.case_id
		  AND publication_attempt_entry.case_ordinal = publication_membership.ordinal
		  AND publication_attempt_entry.attempt_id = publication_attempt.id
		  AND publication_attempt_entry.fence_ordinal = publication.fence_ordinal
		  AND publication_attempt_entry.commit_oid = publication.tip_commit
		  AND publication_attempt_entry.tree_oid = publication.tree
		  AND publication_attempt_entry.no_changes = publication.no_changes
		  AND publication_attempt_entry.ci_plan_digest = publication.ci_plan_digest
		  AND publication_attempt_entry.ci_result_digest = publication.ci_result_digest
		  AND publication_review.ordinal = publication_attempt_entry.ordinal + 1
		  AND publication_review.previous_hash = publication_attempt_entry.entry_hash
		  AND publication_attempt.status = 'completed'
		  AND publication_attempt.lease_owner = ''
		  AND publication_attempt.lease_token = ''
		  AND publication_attempt.lease_until IS NULL
		  AND publication_attempt.ordinal = (
			SELECT MAX(publication_owner_attempt.ordinal)
			FROM pr_development_repair_attempts AS publication_owner_attempt
			WHERE publication_owner_attempt.session_id = publication_owner_session.id
		  )
		  AND publication_controller.revision = publication.controller_revision
		  AND publication_controller.phase = 'ready'
		  AND publication_controller.current_attempt_id = publication_attempt.id
		  AND publication_controller.lease_kind = ''
		  AND publication_controller.lease_owner = ''
		  AND publication_controller.lease_token = ''
		  AND publication_controller.lease_until IS NULL
		  AND publication_controller.mutation_reservation_key = ''
		  AND publication_controller.workspace_id = publication.workspace_id
		  AND publication_controller.line_id = publication.line_id
		  AND publication_controller.source_clone_url = publication.source_clone_url
		  AND publication_controller.source_ref = publication.source_ref
		  AND publication_controller.source_commit = publication.source_commit
		  AND publication_controller.source_tree = publication.source_tree
		  AND publication_controller.line_version = publication.line_version
		  AND publication_controller.mutation_epoch = publication.mutation_epoch
		  AND publication_controller.tip_commit = publication.tip_commit
		  AND publication_controller.tree = publication.tree
		  AND publication_controller.fence_count = publication.fence_ordinal + 1
		  AND publication_controller.fences_digest = publication.fence_hash
		  AND publication_fence.line_id = publication.line_id
		  AND publication_fence.ordinal = publication.fence_ordinal
		  AND publication_fence.line_version = publication.line_version
		  AND publication_fence.mutation_epoch = publication.mutation_epoch
		  AND publication_fence.park_intent_id = publication.park_intent_id
		  AND publication_fence.base_commit = publication.base_commit
		  AND publication_fence.tip_commit = publication.tip_commit
		  AND publication_fence.tree = publication.tree
		  AND publication_fence.no_changes = publication.no_changes
		  AND publication_fence.fence_hash = publication.fence_hash
		  AND publication_fence.reviewed_at IS NOT NULL
		  AND publication_orchestration.session_id = publication_owner_session.id
		  AND publication_orchestration.case_id = publication_membership.case_id
		  AND publication_orchestration.thread_id = publication_membership.thread_id
		  AND publication_orchestration.controller_id = publication_controller.id
		  AND publication_orchestration.phase = 'completed'
		  AND publication_orchestration.workspace_id = publication.workspace_id
		  AND publication_orchestration.clone_url = publication.source_clone_url
		  AND publication_orchestration.head_ref = publication.source_ref
		  AND publication_orchestration.head_sha = publication.source_commit
		  AND publication_orchestration.source_tree = publication.source_tree
		  AND publication_orchestration.ledger_entry_id = publication.attempt_ledger_entry_id
		  AND publication_orchestration.ci_status = 'passed'
		  AND publication_orchestration.ci_effective_plan_digest = publication.ci_plan_digest
		  AND publication_orchestration.ci_execution_digest = publication.ci_result_digest
		  AND publication_orchestration.validation_line_id = publication.line_id
		  AND publication_orchestration.validation_mutation_epoch = publication.mutation_epoch
		  AND publication_orchestration.candidate_tree = publication.tree
		  AND publication_orchestration.no_changes = publication.no_changes
	)`

// prDevelopmentCaseAttentionRequiredColumn is the one coarse mutable list
// projection. The browser sees only the union boolean while every local or
// publication trigger identity, policy, workflow run, and bearer stays private.
const prDevelopmentCaseAttentionRequiredColumn = `
	CASE WHEN ` + prDevelopmentCaseLocalAttentionRequiredPredicate + ` OR ` +
	prDevelopmentCasePublicationAttentionRequiredPredicate + ` THEN 1 ELSE 0 END`

type storedPRDevelopmentCase struct {
	Case        PRDevelopmentCase
	CaptureHash string
}

// LookupPRDevelopmentCapture checks exact durable provenance and its complete
// integrity-checked provider-thread binding before a caller repeats a provider
// read. A missing case is not an error; malformed, missing, corrupt, or
// mismatched binding state is. An isolated migration-created legacy binding is
// accepted so an already committed pre-identity capture remains retryable.
func (s *Store) LookupPRDevelopmentCapture(
	ctx context.Context,
	identity PRDevelopmentCaptureIdentity,
	expectedThread *PRDevelopmentThreadIdentity,
) (PRDevelopmentCase, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentCase{}, false, err
	}
	normalized, err := normalizePRDevelopmentCaptureIdentity(identity)
	if err != nil {
		return PRDevelopmentCase{}, false, err
	}
	var normalizedThread *PRDevelopmentThreadIdentity
	if expectedThread != nil {
		normalized, normalizeErr := normalizePRDevelopmentThreadIdentity(
			*expectedThread,
			expectedThread.PullNumber,
			"",
		)
		if normalizeErr != nil {
			return PRDevelopmentCase{}, false, normalizeErr
		}
		normalizedThread = &normalized
	}

	var (
		developmentCase PRDevelopmentCase
		found           bool
	)
	err = s.withPRDevelopmentConversationReadSnapshot(
		ctx,
		func(queryer rowsQueryer) error {
			if verifyErr := verifyPRDevelopmentDispatch(
				ctx,
				queryer,
				normalized,
			); verifyErr != nil {
				return verifyErr
			}
			stored, exists, findErr := findPRDevelopmentCaptureByProvenance(
				ctx,
				queryer,
				normalized,
			)
			if findErr != nil {
				return findErr
			}
			if !exists {
				return nil
			}
			if prDevelopmentCaseIdentity(stored.Case) != normalized {
				return fmt.Errorf(
					"%w: dispatch or run provenance differs",
					ErrPRDevelopmentConflict,
				)
			}
			if normalizedThread != nil {
				caseThread, normalizeErr := normalizePRDevelopmentThreadIdentity(
					*normalizedThread,
					stored.Case.PullNumber,
					stored.Case.PullURL,
				)
				if normalizeErr != nil || caseThread != *normalizedThread {
					return fmt.Errorf(
						"%w: capture does not match the expected provider thread",
						ErrPRDevelopmentConflict,
					)
				}
			}
			binding, bindingErr := loadPRDevelopmentThreadBindingForCase(
				ctx,
				queryer,
				stored.Case.ID,
			)
			if bindingErr != nil {
				return bindingErr
			}
			switch binding.Kind {
			case PRDevelopmentThreadProvider:
				if normalizedThread == nil || binding.Identity != *normalizedThread {
					return fmt.Errorf(
						"%w: capture is bound to a different provider thread",
						ErrPRDevelopmentConflict,
					)
				}
			case PRDevelopmentThreadLegacy:
				if normalizedThread != nil {
					return fmt.Errorf(
						"%w: legacy capture cannot claim a provider thread",
						ErrPRDevelopmentConflict,
					)
				}
				// Schema-v9 migration deliberately isolated every pre-v9 case in
				// one legacy thread. Its binding loader proves ordinal-zero
				// singleton membership, allowing an in-flight old event to
				// reconcile without inventing unavailable provider IDs.
			default:
				return fmt.Errorf(
					"%w: capture has an unsupported thread binding",
					ErrPRDevelopmentConflict,
				)
			}
			developmentCase = stored.Case
			found = true
			return nil
		},
	)
	if err != nil {
		return PRDevelopmentCase{}, false, fmt.Errorf(
			"lookup pull request development capture: %w",
			s.dbError(err),
		)
	}
	return developmentCase, found, nil
}

// CapturePRDevelopmentCase atomically persists one provider-verified review
// occurrence. Exact dispatch/run retries return the committed row only when
// the complete normalized provenance and provider content remain identical.
func (s *Store) CapturePRDevelopmentCase(
	ctx context.Context,
	input PRDevelopmentCaptureRequest,
) (PRDevelopmentCase, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentCase{}, false, err
	}
	normalized, err := normalizePRDevelopmentCapture(input.Case)
	if err != nil {
		return PRDevelopmentCase{}, false, err
	}
	threadIdentity, err := normalizePRDevelopmentThreadIdentity(
		input.Thread,
		normalized.PullNumber,
		normalized.PullURL,
	)
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
			thread, threadErr := loadPRDevelopmentThreadBindingForCase(
				ctx,
				conn,
				existing.Case.ID,
			)
			if threadErr != nil {
				return threadErr
			}
			if thread.Kind != PRDevelopmentThreadProvider ||
				thread.Identity != threadIdentity {
				return fmt.Errorf(
					"%w: dispatch or run is bound to a different thread",
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
		threadID, link, threadErr := preparePRDevelopmentProviderThreadLink(
			ctx,
			conn,
			threadIdentity,
			caseID,
			captureHash,
			now,
		)
		if threadErr != nil {
			return threadErr
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
		if _, execErr := conn.ExecContext(ctx, `
			INSERT INTO pr_development_thread_cases (
				case_id, thread_id, ordinal, linked_at,
				previous_hash, link_hash
			) VALUES (?, ?, ?, ?, ?, ?)`,
			caseID,
			threadID,
			link.Ordinal,
			toDBTime(link.LinkedAt),
			link.PreviousHash,
			link.LinkHash,
		); execErr != nil {
			return execErr
		}
		if _, execErr := conn.ExecContext(ctx, `
			INSERT INTO pr_development_conversations (
				case_id, version, content_bytes, transcript_digest
			) VALUES (?, 0, 0, ?)`,
			caseID,
			emptyPRDevelopmentTranscriptDigest(),
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
		func(scanner rowScanner) (PRDevelopmentCaseListItem, error) {
			return scanPRDevelopmentCaseListItem(scanner)
		},
		func(item PRDevelopmentCaseListItem) PRDevelopmentCaseCursor {
			return PRDevelopmentCaseCursor{
				UpdatedAt: item.UpdatedAt,
				ID:        item.ID,
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
		prDevelopmentCaseColumns+", "+prDevelopmentCaseAttentionRequiredColumn,
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
	return scanPRDevelopmentCaseWithTrailing(scanner)
}

func scanPRDevelopmentCaseListItem(
	scanner rowScanner,
) (PRDevelopmentCaseListItem, error) {
	var attentionRequired int64
	stored, err := scanPRDevelopmentCaseWithTrailing(scanner, &attentionRequired)
	if err != nil {
		return PRDevelopmentCaseListItem{}, err
	}
	if attentionRequired != 0 && attentionRequired != 1 {
		return PRDevelopmentCaseListItem{}, errors.New(
			"stored development attention-required projection is invalid",
		)
	}
	return PRDevelopmentCaseListItem{
		PRDevelopmentCase: stored.Case,
		AttentionRequired: attentionRequired == 1,
	}, nil
}

func scanPRDevelopmentCaseWithTrailing(
	scanner rowScanner,
	trailing ...any,
) (storedPRDevelopmentCase, error) {
	var (
		stored                storedPRDevelopmentCase
		pullNumber            int64
		pullDraft, pullMerged int64
		reviewSubmittedAt     int64
		createdAt, updatedAt  int64
	)
	destinations := make([]any, 0, 33+len(trailing))
	destinations = append(destinations,
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
	destinations = append(destinations, trailing...)
	err := scanner.Scan(destinations...)
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
