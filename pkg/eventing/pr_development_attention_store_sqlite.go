//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var (
	_ PRDevelopmentAttentionSnapshotReader   = (*Store)(nil)
	_ PRDevelopmentAttentionDecisionRunStore = (*Store)(nil)
)

const prDevelopmentAttentionDecisionRunColumns = `
	case_id, review_entry_id, review_entry_hash, conversation_version,
	subject_revision, decision_point, policy_revision, run_id,
	selected_ordinal, transcript_digest, thread_id, thread_case_count,
	thread_cases_digest, ledger_entry_count, ledger_entries_digest,
	ledger_checkpoint_count, ledger_checkpoints_digest, review_entry_ordinal,
	attempt_id, attempt_ordinal, fence_ordinal, fence_hash, controller_id,
	controller_revision, controller_line_version, controller_fence_count,
	controller_fences_digest, owner_session_id, owner_session_version,
	owner_attempt_count, created_at`

// GetPRDevelopmentAttentionSnapshot returns the current attention-required
// review subject and every admission high-water from one read transaction.
func (s *Store) GetPRDevelopmentAttentionSnapshot(
	ctx context.Context,
	caseID string,
) (PRDevelopmentAttentionSnapshot, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentAttentionSnapshot{}, err
	}
	caseID = strings.TrimSpace(caseID)
	if !validPrefixedHexID(caseID, prDevelopmentCaseIDPrefix) {
		return PRDevelopmentAttentionSnapshot{}, fmt.Errorf(
			"%w: invalid development case ID",
			ErrInvalidPRDevelopmentAttention,
		)
	}

	var snapshot PRDevelopmentAttentionSnapshot
	err := s.withPRDevelopmentConversationReadSnapshot(
		ctx,
		func(queryer rowsQueryer) error {
			loaded, loadErr := loadCurrentPRDevelopmentAttentionSnapshot(
				ctx,
				queryer,
				caseID,
			)
			snapshot = loaded
			return loadErr
		},
	)
	if err != nil {
		return PRDevelopmentAttentionSnapshot{}, fmt.Errorf(
			"get pull request development attention snapshot: %w",
			s.dbError(err),
		)
	}
	return snapshot, nil
}

// GetPRDevelopmentAttentionDecisionRun returns the durable workflow run bound
// to one exact semantic attention decision.
func (s *Store) GetPRDevelopmentAttentionDecisionRun(
	ctx context.Context,
	key PRDevelopmentAttentionDecisionKey,
) (PRDevelopmentAttentionDecisionRunLink, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentAttentionDecisionRunLink{}, err
	}
	normalized, err := normalizePRDevelopmentAttentionDecisionKey(key)
	if err != nil {
		return PRDevelopmentAttentionDecisionRunLink{}, err
	}
	var link PRDevelopmentAttentionDecisionRunLink
	err = s.withPRDevelopmentConversationReadSnapshot(
		ctx,
		func(queryer rowsQueryer) error {
			loaded, loadErr := getPRDevelopmentAttentionDecisionRun(
				ctx,
				queryer,
				normalized,
			)
			if loadErr != nil {
				return loadErr
			}
			if validateErr := validateHistoricalPRDevelopmentAttentionDecisionRun(
				ctx,
				queryer,
				loaded,
			); validateErr != nil {
				return validateErr
			}
			link = loaded
			return nil
		},
	)
	if err != nil {
		return PRDevelopmentAttentionDecisionRunLink{}, s.dbError(err)
	}
	return link, nil
}

// AdmitPRDevelopmentAttentionDecisionRun invokes create at most once for one
// exact semantic key. Historical exact replay is resolved before consulting
// mutable PR-development state. A new link requires the supplied snapshot to
// remain the current, terminal attention-required tail under BEGIN IMMEDIATE.
func (s *Store) AdmitPRDevelopmentAttentionDecisionRun(
	ctx context.Context,
	admission PRDevelopmentAttentionDecisionRunAdmission,
	create func(context.Context) error,
) (PRDevelopmentAttentionDecisionRunLink, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentAttentionDecisionRunLink{}, false, err
	}
	key, runID, err := normalizePRDevelopmentAttentionDecisionRunIdentity(
		admission.Key,
		admission.RunID,
	)
	if err != nil {
		return PRDevelopmentAttentionDecisionRunLink{}, false, err
	}
	if create == nil {
		return PRDevelopmentAttentionDecisionRunLink{}, false, fmt.Errorf(
			"%w: workflow run create callback is required",
			ErrInvalidPRDevelopmentAttention,
		)
	}

	var (
		link              PRDevelopmentAttentionDecisionRunLink
		existed           bool
		callbackSucceeded bool
	)
	transactionErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		existing, findErr := getPRDevelopmentAttentionDecisionRun(ctx, conn, key)
		switch {
		case findErr == nil:
			if existing.RunID != runID {
				return fmt.Errorf(
					"%w: attention decision is already bound to another workflow run",
					ErrPRDevelopmentAttentionConflict,
				)
			}
			if validateErr := validateHistoricalPRDevelopmentAttentionDecisionRun(
				ctx,
				conn,
				existing,
			); validateErr != nil {
				return validateErr
			}
			link = existing
			existed = true
			return nil
		case !errors.Is(findErr, sql.ErrNoRows):
			return findErr
		}

		conflicting, findErr := getPRDevelopmentAttentionDecisionRunByRunID(
			ctx,
			conn,
			runID,
		)
		switch {
		case findErr == nil:
			if conflicting.Key != key {
				return fmt.Errorf(
					"%w: workflow run is already bound to another attention decision",
					ErrPRDevelopmentAttentionConflict,
				)
			}
			return fmt.Errorf(
				"%w: inconsistent attention decision workflow-run binding",
				ErrPRDevelopmentAttentionConflict,
			)
		case !errors.Is(findErr, sql.ErrNoRows):
			return findErr
		}

		expected, normalizeErr := normalizePRDevelopmentAttentionHighWater(
			admission.Snapshot,
			key,
		)
		if normalizeErr != nil {
			return normalizeErr
		}
		current, loadErr := loadCurrentPRDevelopmentAttentionSnapshot(
			ctx,
			conn,
			key.CaseID,
		)
		if loadErr != nil {
			return loadErr
		}
		if current.HighWater != expected {
			return fmt.Errorf(
				"%w: attention subject changed after snapshot",
				ErrPRDevelopmentAttentionConflict,
			)
		}

		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		link = PRDevelopmentAttentionDecisionRunLink{
			Key:       key,
			Snapshot:  expected,
			RunID:     runID,
			CreatedAt: now,
		}
		if insertErr := insertPRDevelopmentAttentionDecisionRun(
			ctx,
			conn,
			link,
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
			return PRDevelopmentAttentionDecisionRunLink{}, false, fmt.Errorf(
				"%w: %w",
				ErrPRDevelopmentAttentionAdmissionUncertain,
				s.dbError(transactionErr),
			)
		}
		return PRDevelopmentAttentionDecisionRunLink{}, false, fmt.Errorf(
			"admit pull request development attention decision workflow run: %w",
			s.dbError(transactionErr),
		)
	}
	return link, existed, nil
}

// validateHistoricalPRDevelopmentAttentionDecisionRun validates every
// immutable source prefix captured by an existing link. Later conversation,
// thread, ledger, checkpoint, repair, fence, and controller advancement is
// allowed; deletion or mutation inside the admitted prefix is not.
func validateHistoricalPRDevelopmentAttentionDecisionRun(
	ctx context.Context,
	queryer rowsQueryer,
	link PRDevelopmentAttentionDecisionRunLink,
) error {
	highWater := link.Snapshot
	storedCase, err := getPRDevelopmentCaseRecord(ctx, queryer, link.Key.CaseID)
	if err != nil {
		return err
	}
	if storedCase.Case.ID != link.Key.CaseID {
		return errors.New("historical attention decision case binding is invalid")
	}

	conversation, err := loadPRDevelopmentConversation(ctx, queryer, link.Key.CaseID)
	if err != nil {
		return err
	}
	if conversation.Conversation.Version < highWater.ConversationVersion ||
		len(conversation.Conversation.Messages) < int(highWater.ConversationVersion) {
		return errors.New("historical attention conversation prefix is unavailable")
	}
	transcriptDigest := emptyPRDevelopmentTranscriptDigest()
	for _, message := range conversation.Conversation.Messages[:highWater.ConversationVersion] {
		transcriptDigest, err = extendPRDevelopmentTranscriptDigest(
			transcriptDigest,
			message,
		)
		if err != nil {
			return err
		}
	}
	if transcriptDigest != highWater.TranscriptDigest {
		return errors.New("historical attention conversation prefix digest changed")
	}

	thread, err := loadPRDevelopmentThreadForCase(ctx, queryer, link.Key.CaseID)
	if err != nil {
		return err
	}
	if thread.ID != highWater.ThreadID ||
		thread.Kind != PRDevelopmentThreadProvider ||
		thread.CaseCount < highWater.ThreadCaseCount ||
		len(thread.Cases) < highWater.ThreadCaseCount ||
		highWater.SelectedOrdinal >= highWater.ThreadCaseCount ||
		thread.Cases[highWater.SelectedOrdinal].CaseID != link.Key.CaseID ||
		thread.Cases[highWater.ThreadCaseCount-1].LinkHash != highWater.ThreadCasesDigest {
		return errors.New("historical attention thread prefix changed")
	}

	controller, found, err := loadPRDevelopmentControllerAggregateByID(
		ctx,
		queryer,
		highWater.ControllerID,
	)
	if err != nil {
		return err
	}
	if !found || controller.ThreadID != highWater.ThreadID ||
		controller.OwnerSessionID != highWater.OwnerSessionID ||
		controller.Revision < highWater.ControllerRevision ||
		controller.LineVersion < highWater.ControllerLineVersion ||
		controller.FenceCount < highWater.ControllerFenceCount {
		return errors.New("historical attention controller high-water regressed")
	}
	owner, err := loadPRDevelopmentRepairSessionByID(
		ctx,
		queryer,
		highWater.OwnerSessionID,
	)
	if err != nil {
		return err
	}
	if owner.CaseID != link.Key.CaseID ||
		owner.Version < highWater.OwnerSessionVersion ||
		len(owner.Attempts) < highWater.OwnerAttemptCount ||
		highWater.AttemptOrdinal >= highWater.OwnerAttemptCount {
		return errors.New("historical attention owner-session prefix changed")
	}
	targetAttempt := owner.Attempts[highWater.AttemptOrdinal]
	if targetAttempt.ID != highWater.AttemptID ||
		targetAttempt.Ordinal != highWater.AttemptOrdinal ||
		targetAttempt.Status != PRDevelopmentRepairCompleted {
		return errors.New("historical attention target attempt changed")
	}
	fences, err := loadPRDevelopmentReviewFences(ctx, queryer, controller.ID)
	if err != nil {
		return err
	}
	if len(fences) < highWater.ControllerFenceCount ||
		highWater.FenceOrdinal >= highWater.ControllerFenceCount {
		return errors.New("historical attention controller fence prefix is unavailable")
	}
	fence := fences[highWater.FenceOrdinal]
	if fence.AttemptID != highWater.AttemptID ||
		fence.Ordinal != highWater.FenceOrdinal ||
		fence.LineVersion != highWater.ControllerLineVersion ||
		fence.FenceHash != highWater.FenceHash ||
		fences[highWater.ControllerFenceCount-1].FenceHash !=
			highWater.ControllerFencesDigest ||
		fence.ReviewedAt == nil {
		return errors.New("historical attention target fence changed")
	}

	ledger, err := loadPRDevelopmentLedgerAggregate(ctx, queryer, thread)
	if err != nil {
		return err
	}
	if ledger.ThreadID != highWater.ThreadID ||
		len(ledger.Entries) < highWater.LedgerEntryCount ||
		ledger.Entries[highWater.LedgerEntryCount-1].EntryHash !=
			highWater.LedgerEntriesDigest ||
		len(ledger.Checkpoints) < highWater.LedgerCheckpointCount {
		return errors.New("historical attention ledger prefix changed")
	}
	if highWater.LedgerCheckpointCount == 0 {
		if highWater.LedgerCheckpointsDigest != emptyPRDevelopmentLedgerCheckpointsDigest() {
			return errors.New("historical attention empty checkpoint prefix changed")
		}
	} else if ledger.Checkpoints[highWater.LedgerCheckpointCount-1].CheckpointHash !=
		highWater.LedgerCheckpointsDigest {
		return errors.New("historical attention checkpoint prefix changed")
	}
	reviewEntry := ledger.Entries[highWater.LedgerEntryCount-1]
	if reviewEntry.ID != link.Key.ReviewEntryID ||
		reviewEntry.EntryHash != link.Key.ReviewEntryHash ||
		reviewEntry.Ordinal != highWater.ReviewEntryOrdinal ||
		reviewEntry.Kind != PRDevelopmentLedgerReview ||
		reviewEntry.ReviewOutcome != PRDevelopmentLedgerReviewAttentionRequired ||
		reviewEntry.AttemptID != highWater.AttemptID ||
		reviewEntry.FenceOrdinal != highWater.FenceOrdinal ||
		reviewEntry.FenceHash != highWater.FenceHash {
		return errors.New("historical attention review entry changed")
	}
	return nil
}

func loadCurrentPRDevelopmentAttentionSnapshot(
	ctx context.Context,
	queryer rowsQueryer,
	caseID string,
) (PRDevelopmentAttentionSnapshot, error) {
	storedCase, err := getPRDevelopmentCaseRecord(ctx, queryer, caseID)
	if err != nil {
		return PRDevelopmentAttentionSnapshot{}, err
	}
	conversation, err := loadPRDevelopmentConversation(ctx, queryer, caseID)
	if err != nil {
		return PRDevelopmentAttentionSnapshot{}, err
	}
	thread, err := loadPRDevelopmentThreadForCase(ctx, queryer, caseID)
	if err != nil {
		return PRDevelopmentAttentionSnapshot{}, err
	}
	if thread.Kind != PRDevelopmentThreadProvider {
		return PRDevelopmentAttentionSnapshot{}, fmt.Errorf(
			"%w: legacy development threads cannot launch attention workflows",
			ErrPRDevelopmentAttentionConflict,
		)
	}
	selectedOrdinal := -1
	for _, membership := range thread.Cases {
		if membership.CaseID == caseID {
			selectedOrdinal = membership.Ordinal
			break
		}
	}
	if selectedOrdinal < 0 {
		return PRDevelopmentAttentionSnapshot{}, errors.New(
			"selected development case disappeared from its thread",
		)
	}

	controller, found, err := loadPRDevelopmentControllerAggregate(
		ctx,
		queryer,
		thread.ID,
	)
	if err != nil {
		return PRDevelopmentAttentionSnapshot{}, err
	}
	if !found {
		return PRDevelopmentAttentionSnapshot{}, fmt.Errorf(
			"%w: development thread has no controller",
			ErrPRDevelopmentAttentionConflict,
		)
	}
	owner, err := loadPRDevelopmentRepairSessionByID(
		ctx,
		queryer,
		controller.OwnerSessionID,
	)
	if err != nil {
		return PRDevelopmentAttentionSnapshot{}, err
	}
	if owner.CaseID != caseID || len(owner.Attempts) == 0 {
		return PRDevelopmentAttentionSnapshot{}, fmt.Errorf(
			"%w: selected case does not own the completed controller attempt",
			ErrPRDevelopmentAttentionConflict,
		)
	}
	targetAttempt := owner.Attempts[len(owner.Attempts)-1]
	if targetAttempt.Status != PRDevelopmentRepairCompleted ||
		targetAttempt.ID != controller.CurrentAttemptID ||
		targetAttempt.LeaseOwner != "" || targetAttempt.LeaseToken != "" ||
		targetAttempt.LeaseUntil != nil {
		return PRDevelopmentAttentionSnapshot{}, fmt.Errorf(
			"%w: controller attempt is not the unleased completed owner-session tail",
			ErrPRDevelopmentAttentionConflict,
		)
	}
	if controller.Phase != PRDevelopmentControllerReady ||
		controller.LeaseKind != "" || controller.LeaseOwner != "" ||
		controller.LeaseToken != "" || controller.LeaseUntil != nil ||
		controller.MutationReservationKey != "" {
		return PRDevelopmentAttentionSnapshot{}, fmt.Errorf(
			"%w: controller is not ready and reservation-free",
			ErrPRDevelopmentAttentionConflict,
		)
	}

	ledger, err := loadPRDevelopmentLedgerAggregate(ctx, queryer, thread)
	if err != nil {
		return PRDevelopmentAttentionSnapshot{}, err
	}
	if len(ledger.Entries) == 0 {
		return PRDevelopmentAttentionSnapshot{}, fmt.Errorf(
			"%w: development ledger has no review tail",
			ErrPRDevelopmentAttentionConflict,
		)
	}
	reviewEntry := ledger.Entries[len(ledger.Entries)-1]
	if reviewEntry.Kind != PRDevelopmentLedgerReview ||
		reviewEntry.ReviewOutcome != PRDevelopmentLedgerReviewAttentionRequired ||
		reviewEntry.AttemptID != targetAttempt.ID || reviewEntry.CaseID != caseID ||
		reviewEntry.CaseOrdinal != selectedOrdinal ||
		ledger.EntriesDigest != reviewEntry.EntryHash {
		return PRDevelopmentAttentionSnapshot{}, fmt.Errorf(
			"%w: current ledger tail is not the target attention-required review",
			ErrPRDevelopmentAttentionConflict,
		)
	}
	fence, found, err := loadPRDevelopmentReviewFenceByAttempt(
		ctx,
		queryer,
		targetAttempt.ID,
	)
	if err != nil {
		return PRDevelopmentAttentionSnapshot{}, err
	}
	if !found || fence.ReviewedAt == nil || fence.ControllerID != controller.ID ||
		fence.ThreadID != thread.ID || fence.AttemptID != targetAttempt.ID ||
		fence.Ordinal != controller.FenceCount-1 ||
		fence.Ordinal != reviewEntry.FenceOrdinal ||
		fence.FenceHash != reviewEntry.FenceHash ||
		controller.FencesDigest != fence.FenceHash {
		return PRDevelopmentAttentionSnapshot{}, fmt.Errorf(
			"%w: attention review is not the controller fence tail",
			ErrPRDevelopmentAttentionConflict,
		)
	}

	highWater := PRDevelopmentAttentionHighWater{
		CaseID:                  caseID,
		SelectedOrdinal:         selectedOrdinal,
		ConversationVersion:     conversation.Conversation.Version,
		TranscriptDigest:        conversation.TranscriptDigest,
		ThreadID:                thread.ID,
		ThreadCaseCount:         thread.CaseCount,
		ThreadCasesDigest:       thread.CasesDigest,
		LedgerEntryCount:        len(ledger.Entries),
		LedgerEntriesDigest:     ledger.EntriesDigest,
		LedgerCheckpointCount:   len(ledger.Checkpoints),
		LedgerCheckpointsDigest: ledger.CheckpointsDigest,
		ReviewEntryID:           reviewEntry.ID,
		ReviewEntryOrdinal:      reviewEntry.Ordinal,
		ReviewEntryHash:         reviewEntry.EntryHash,
		AttemptID:               targetAttempt.ID,
		AttemptOrdinal:          targetAttempt.Ordinal,
		FenceOrdinal:            fence.Ordinal,
		FenceHash:               fence.FenceHash,
		ControllerID:            controller.ID,
		ControllerRevision:      controller.Revision,
		ControllerLineVersion:   controller.LineVersion,
		ControllerFenceCount:    controller.FenceCount,
		ControllerFencesDigest:  controller.FencesDigest,
		OwnerSessionID:          owner.ID,
		OwnerSessionVersion:     owner.Version,
		OwnerAttemptCount:       len(owner.Attempts),
	}
	return PRDevelopmentAttentionSnapshot{
		Case:         storedCase.Case,
		Thread:       thread,
		Conversation: conversation.Conversation,
		OwnerSession: owner,
		Controller:   controller,
		Fence:        fence,
		Ledger:       ledger,
		ReviewEntry:  reviewEntry,
		HighWater:    highWater,
	}, nil
}

func normalizePRDevelopmentAttentionDecisionRunIdentity(
	key PRDevelopmentAttentionDecisionKey,
	runID string,
) (PRDevelopmentAttentionDecisionKey, string, error) {
	normalized, err := normalizePRDevelopmentAttentionDecisionKey(key)
	if err != nil {
		return PRDevelopmentAttentionDecisionKey{}, "", err
	}
	if !validPrefixedHexID(runID, "wr_") {
		return PRDevelopmentAttentionDecisionKey{}, "", fmt.Errorf(
			"%w: invalid workflow run ID",
			ErrInvalidPRDevelopmentAttention,
		)
	}
	return normalized, runID, nil
}

func normalizePRDevelopmentAttentionDecisionKey(
	key PRDevelopmentAttentionDecisionKey,
) (PRDevelopmentAttentionDecisionKey, error) {
	if !validPrefixedHexID(key.CaseID, prDevelopmentCaseIDPrefix) ||
		!validPrefixedHexID(key.ReviewEntryID, prDevelopmentLedgerEntryIDPrefix) ||
		!validPRDevelopmentHex(key.ReviewEntryHash, 64) ||
		key.ConversationVersion < 0 ||
		key.ConversationVersion > MaxPRDevelopmentMessagesPerCase {
		return PRDevelopmentAttentionDecisionKey{}, fmt.Errorf(
			"%w: invalid case, review entry, or conversation version",
			ErrInvalidPRDevelopmentAttention,
		)
	}
	if !validReviewPolicyRevision(key.SubjectRevision) ||
		!validReviewPolicyRevision(key.PolicyRevision) {
		return PRDevelopmentAttentionDecisionKey{}, fmt.Errorf(
			"%w: subject and policy revisions must be lowercase SHA-256 revisions",
			ErrInvalidPRDevelopmentAttention,
		)
	}
	if !validReviewDecisionPoint(key.DecisionPoint) {
		return PRDevelopmentAttentionDecisionKey{}, fmt.Errorf(
			"%w: decision point must match [a-z][a-z0-9._-]{0,127}",
			ErrInvalidPRDevelopmentAttention,
		)
	}
	return key, nil
}

func normalizePRDevelopmentAttentionHighWater(
	snapshot PRDevelopmentAttentionHighWater,
	key PRDevelopmentAttentionDecisionKey,
) (PRDevelopmentAttentionHighWater, error) {
	if snapshot.CaseID != key.CaseID ||
		snapshot.ReviewEntryID != key.ReviewEntryID ||
		snapshot.ReviewEntryHash != key.ReviewEntryHash ||
		snapshot.ConversationVersion != key.ConversationVersion ||
		snapshot.SelectedOrdinal < 0 ||
		snapshot.SelectedOrdinal >= snapshot.ThreadCaseCount ||
		snapshot.ThreadCaseCount < 1 ||
		snapshot.ThreadCaseCount > MaxPRDevelopmentThreadCases ||
		snapshot.LedgerEntryCount < 2 ||
		snapshot.LedgerEntryCount > MaxPRDevelopmentLedgerEntries ||
		snapshot.LedgerCheckpointCount < 0 ||
		snapshot.LedgerCheckpointCount > MaxPRDevelopmentControllerFences ||
		snapshot.ReviewEntryOrdinal < 1 ||
		snapshot.ReviewEntryOrdinal >= MaxPRDevelopmentLedgerEntries ||
		snapshot.ReviewEntryOrdinal%2 != 1 ||
		snapshot.FenceOrdinal < 0 ||
		snapshot.FenceOrdinal >= MaxPRDevelopmentControllerFences ||
		snapshot.ReviewEntryOrdinal != snapshot.FenceOrdinal*2+1 ||
		snapshot.AttemptOrdinal < 0 ||
		snapshot.AttemptOrdinal >= MaxPRDevelopmentRepairAttempts ||
		snapshot.OwnerAttemptCount != snapshot.AttemptOrdinal+1 ||
		snapshot.ControllerFenceCount != snapshot.FenceOrdinal+1 ||
		snapshot.ControllerLineVersion < 1 ||
		snapshot.ControllerLineVersion > MaxPRDevelopmentControllerFences ||
		snapshot.ControllerRevision < 1 ||
		snapshot.ControllerRevision > MaxPRDevelopmentControllerRevision ||
		snapshot.OwnerSessionVersion < 1 ||
		snapshot.OwnerSessionVersion > MaxPRDevelopmentRepairVersion ||
		!validPrefixedHexID(snapshot.ThreadID, prDevelopmentThreadIDPrefix) ||
		!validPrefixedHexID(snapshot.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(snapshot.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(snapshot.OwnerSessionID, prDevelopmentRepairSessionIDPrefix) ||
		!validPRDevelopmentHex(snapshot.TranscriptDigest, 64) ||
		!validPRDevelopmentHex(snapshot.ThreadCasesDigest, 64) ||
		!validPRDevelopmentHex(snapshot.LedgerEntriesDigest, 64) ||
		!validPRDevelopmentHex(snapshot.LedgerCheckpointsDigest, 64) ||
		!validPRDevelopmentHex(snapshot.FenceHash, 64) ||
		!validPRDevelopmentHex(snapshot.ControllerFencesDigest, 64) {
		return PRDevelopmentAttentionHighWater{}, fmt.Errorf(
			"%w: invalid or mismatched attention snapshot high-water",
			ErrInvalidPRDevelopmentAttention,
		)
	}
	return snapshot, nil
}

func insertPRDevelopmentAttentionDecisionRun(
	ctx context.Context,
	conn *sql.Conn,
	link PRDevelopmentAttentionDecisionRunLink,
) error {
	snapshot := link.Snapshot
	_, err := conn.ExecContext(ctx, `
		INSERT INTO pr_development_attention_decision_runs (
			case_id, review_entry_id, review_entry_kind, review_entry_hash,
			conversation_version, subject_revision, decision_point, policy_revision,
			run_id, selected_ordinal, transcript_digest, thread_id,
			thread_case_count, thread_cases_digest, ledger_entry_count,
			ledger_entries_digest, ledger_checkpoint_count,
			ledger_checkpoints_digest, review_entry_ordinal, attempt_id,
			attempt_ordinal, fence_ordinal, fence_hash, controller_id,
			controller_revision, controller_line_version, controller_fence_count,
			controller_fences_digest, owner_session_id, owner_session_version,
			owner_attempt_count, created_at
		) VALUES (?, ?, 'review', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		link.Key.CaseID,
		link.Key.ReviewEntryID,
		link.Key.ReviewEntryHash,
		link.Key.ConversationVersion,
		link.Key.SubjectRevision,
		link.Key.DecisionPoint,
		link.Key.PolicyRevision,
		link.RunID,
		snapshot.SelectedOrdinal,
		snapshot.TranscriptDigest,
		snapshot.ThreadID,
		snapshot.ThreadCaseCount,
		snapshot.ThreadCasesDigest,
		snapshot.LedgerEntryCount,
		snapshot.LedgerEntriesDigest,
		snapshot.LedgerCheckpointCount,
		snapshot.LedgerCheckpointsDigest,
		snapshot.ReviewEntryOrdinal,
		snapshot.AttemptID,
		snapshot.AttemptOrdinal,
		snapshot.FenceOrdinal,
		snapshot.FenceHash,
		snapshot.ControllerID,
		snapshot.ControllerRevision,
		snapshot.ControllerLineVersion,
		snapshot.ControllerFenceCount,
		snapshot.ControllerFencesDigest,
		snapshot.OwnerSessionID,
		snapshot.OwnerSessionVersion,
		snapshot.OwnerAttemptCount,
		toDBTime(link.CreatedAt),
	)
	return err
}

func getPRDevelopmentAttentionDecisionRun(
	ctx context.Context,
	queryer rowQueryer,
	key PRDevelopmentAttentionDecisionKey,
) (PRDevelopmentAttentionDecisionRunLink, error) {
	return scanPRDevelopmentAttentionDecisionRun(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentAttentionDecisionRunColumns+`
		FROM pr_development_attention_decision_runs
		WHERE case_id = ? AND review_entry_id = ? AND review_entry_hash = ?
			AND conversation_version = ? AND subject_revision = ?
			AND decision_point = ? AND policy_revision = ?`,
		key.CaseID,
		key.ReviewEntryID,
		key.ReviewEntryHash,
		key.ConversationVersion,
		key.SubjectRevision,
		key.DecisionPoint,
		key.PolicyRevision,
	))
}

func getPRDevelopmentAttentionDecisionRunByRunID(
	ctx context.Context,
	queryer rowQueryer,
	runID string,
) (PRDevelopmentAttentionDecisionRunLink, error) {
	return scanPRDevelopmentAttentionDecisionRun(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentAttentionDecisionRunColumns+`
		FROM pr_development_attention_decision_runs
		WHERE run_id = ?`,
		runID,
	))
}

func scanPRDevelopmentAttentionDecisionRun(
	scanner rowScanner,
) (PRDevelopmentAttentionDecisionRunLink, error) {
	var (
		link                                               PRDevelopmentAttentionDecisionRunLink
		createdAt                                          int64
		selectedOrdinal, threadCaseCount, ledgerEntryCount int64
		ledgerCheckpointCount, reviewEntryOrdinal          int64
		attemptOrdinal, fenceOrdinal, controllerFenceCount int64
		ownerAttemptCount                                  int64
	)
	if err := scanner.Scan(
		&link.Key.CaseID,
		&link.Key.ReviewEntryID,
		&link.Key.ReviewEntryHash,
		&link.Key.ConversationVersion,
		&link.Key.SubjectRevision,
		&link.Key.DecisionPoint,
		&link.Key.PolicyRevision,
		&link.RunID,
		&selectedOrdinal,
		&link.Snapshot.TranscriptDigest,
		&link.Snapshot.ThreadID,
		&threadCaseCount,
		&link.Snapshot.ThreadCasesDigest,
		&ledgerEntryCount,
		&link.Snapshot.LedgerEntriesDigest,
		&ledgerCheckpointCount,
		&link.Snapshot.LedgerCheckpointsDigest,
		&reviewEntryOrdinal,
		&link.Snapshot.AttemptID,
		&attemptOrdinal,
		&fenceOrdinal,
		&link.Snapshot.FenceHash,
		&link.Snapshot.ControllerID,
		&link.Snapshot.ControllerRevision,
		&link.Snapshot.ControllerLineVersion,
		&controllerFenceCount,
		&link.Snapshot.ControllerFencesDigest,
		&link.Snapshot.OwnerSessionID,
		&link.Snapshot.OwnerSessionVersion,
		&ownerAttemptCount,
		&createdAt,
	); err != nil {
		return PRDevelopmentAttentionDecisionRunLink{}, err
	}
	link.Snapshot.CaseID = link.Key.CaseID
	link.Snapshot.ReviewEntryID = link.Key.ReviewEntryID
	link.Snapshot.ReviewEntryHash = link.Key.ReviewEntryHash
	link.Snapshot.ConversationVersion = link.Key.ConversationVersion
	link.Snapshot.SelectedOrdinal = int(selectedOrdinal)
	link.Snapshot.ThreadCaseCount = int(threadCaseCount)
	link.Snapshot.LedgerEntryCount = int(ledgerEntryCount)
	link.Snapshot.LedgerCheckpointCount = int(ledgerCheckpointCount)
	link.Snapshot.ReviewEntryOrdinal = int(reviewEntryOrdinal)
	link.Snapshot.AttemptOrdinal = int(attemptOrdinal)
	link.Snapshot.FenceOrdinal = int(fenceOrdinal)
	link.Snapshot.ControllerFenceCount = int(controllerFenceCount)
	link.Snapshot.OwnerAttemptCount = int(ownerAttemptCount)
	if int64(link.Snapshot.SelectedOrdinal) != selectedOrdinal ||
		int64(link.Snapshot.ThreadCaseCount) != threadCaseCount ||
		int64(link.Snapshot.LedgerEntryCount) != ledgerEntryCount ||
		int64(link.Snapshot.LedgerCheckpointCount) != ledgerCheckpointCount ||
		int64(link.Snapshot.ReviewEntryOrdinal) != reviewEntryOrdinal ||
		int64(link.Snapshot.AttemptOrdinal) != attemptOrdinal ||
		int64(link.Snapshot.FenceOrdinal) != fenceOrdinal ||
		int64(link.Snapshot.ControllerFenceCount) != controllerFenceCount ||
		int64(link.Snapshot.OwnerAttemptCount) != ownerAttemptCount {
		return PRDevelopmentAttentionDecisionRunLink{}, errors.New(
			"stored attention decision integer overflows",
		)
	}
	link.CreatedAt = fromDBTime(createdAt)
	if _, err := normalizePRDevelopmentAttentionDecisionKey(link.Key); err != nil {
		return PRDevelopmentAttentionDecisionRunLink{}, errors.New(
			"stored attention decision key is invalid",
		)
	}
	if _, err := normalizePRDevelopmentAttentionHighWater(link.Snapshot, link.Key); err != nil {
		return PRDevelopmentAttentionDecisionRunLink{}, errors.New(
			"stored attention decision snapshot is invalid",
		)
	}
	if !validPrefixedHexID(link.RunID, "wr_") ||
		validateDBTimestamp("attention decision creation time", link.CreatedAt) != nil {
		return PRDevelopmentAttentionDecisionRunLink{}, errors.New(
			"stored attention decision link is invalid",
		)
	}
	return link, nil
}
