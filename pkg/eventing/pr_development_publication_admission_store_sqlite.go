//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// enqueuePRDevelopmentPublication is the sole occurrence-admission seam. Its
// caller has already appended the passed review under the same BEGIN IMMEDIATE
// transaction; this function independently cross-binds every immutable local
// proof before inserting one pending, effect-free journal record.
func enqueuePRDevelopmentPublication(
	ctx context.Context,
	conn *sql.Conn,
	controller PRDevelopmentController,
	fence PRDevelopmentAttemptReviewFence,
	attemptEntry PRDevelopmentLedgerEntry,
	reviewEntry PRDevelopmentLedgerEntry,
	orchestration PRDevelopmentRepairOrchestration,
	now time.Time,
) (PRDevelopmentPublication, error) {
	if err := requirePRDevelopmentPublicationAdmissionEvidence(
		ctx,
		conn,
		controller,
		fence,
		attemptEntry,
		reviewEntry,
		orchestration,
		now,
	); err != nil {
		return PRDevelopmentPublication{}, err
	}
	id, err := newPrefixedID(prDevelopmentPublicationIDPrefix)
	if err != nil {
		return PRDevelopmentPublication{}, err
	}
	receipt := *orchestration.Validation
	publication := PRDevelopmentPublication{
		ID:                       id,
		CaseID:                   reviewEntry.CaseID,
		ThreadID:                 controller.ThreadID,
		ControllerID:             controller.ID,
		ControllerRevision:       controller.Revision,
		OwnerSessionID:           controller.OwnerSessionID,
		AttemptID:                fence.AttemptID,
		FenceOrdinal:             fence.Ordinal,
		FenceHash:                fence.FenceHash,
		AttemptLedgerEntryID:     attemptEntry.ID,
		AttemptLedgerEntryKind:   attemptEntry.Kind,
		AttemptLedgerEntryHash:   attemptEntry.EntryHash,
		ReviewLedgerEntryID:      reviewEntry.ID,
		ReviewLedgerEntryKind:    reviewEntry.Kind,
		ReviewLedgerEntryHash:    reviewEntry.EntryHash,
		ReviewOutcome:            reviewEntry.ReviewOutcome,
		OrchestrationPhase:       orchestration.Phase,
		OrchestrationReceiptHash: receipt.ReceiptHash,
		CIStatus:                 receipt.CIStatus,
		CIPlanDigest:             receipt.CIEffectivePlanDigest,
		CIResultDigest:           receipt.CIExecutionDigest,
		WorkspaceID:              controller.WorkspaceID,
		LineID:                   controller.LineID,
		SourceCloneURL:           controller.SourceCloneURL,
		SourceRef:                controller.SourceRef,
		SourceCommit:             controller.SourceCommit,
		SourceTree:               controller.SourceTree,
		LineVersion:              fence.LineVersion,
		MutationEpoch:            fence.MutationEpoch,
		ParkIntentID:             fence.ParkIntentID,
		BaseCommit:               fence.BaseCommit,
		TipCommit:                fence.TipCommit,
		Tree:                     fence.Tree,
		NoChanges:                fence.NoChanges,
		Status:                   PRDevelopmentPublicationPending,
		AvailableAt:              now,
		CreatedAt:                now,
		UpdatedAt:                now,
	}
	if validationErr := validateStoredPRDevelopmentPublication(publication); validationErr != nil {
		return PRDevelopmentPublication{}, fmt.Errorf(
			"%w: initial publication is invalid: %v",
			ErrPRDevelopmentPublicationConflict,
			validationErr,
		)
	}
	_, err = conn.ExecContext(ctx, `
		INSERT INTO pr_development_publications (
			id, case_id, thread_id, controller_id, controller_revision,
			owner_session_id, attempt_id, fence_ordinal, fence_hash,
			attempt_ledger_entry_id, attempt_ledger_entry_kind,
			attempt_ledger_entry_hash, review_ledger_entry_id,
			review_ledger_entry_kind, review_ledger_entry_hash, review_outcome,
			orchestration_phase, orchestration_receipt_hash, ci_status,
			ci_plan_digest, ci_result_digest, workspace_id, line_id,
			source_clone_url, source_ref, source_commit, source_tree,
			line_version, mutation_epoch, park_intent_id, base_commit,
			tip_commit, tree, no_changes, status, available_at, created_at, updated_at
		) VALUES (
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?
		)`,
		publication.ID,
		publication.CaseID,
		publication.ThreadID,
		publication.ControllerID,
		publication.ControllerRevision,
		publication.OwnerSessionID,
		publication.AttemptID,
		publication.FenceOrdinal,
		publication.FenceHash,
		publication.AttemptLedgerEntryID,
		publication.AttemptLedgerEntryKind,
		publication.AttemptLedgerEntryHash,
		publication.ReviewLedgerEntryID,
		publication.ReviewLedgerEntryKind,
		publication.ReviewLedgerEntryHash,
		publication.ReviewOutcome,
		publication.OrchestrationPhase,
		publication.OrchestrationReceiptHash,
		publication.CIStatus,
		publication.CIPlanDigest,
		publication.CIResultDigest,
		publication.WorkspaceID,
		publication.LineID,
		publication.SourceCloneURL,
		publication.SourceRef,
		publication.SourceCommit,
		publication.SourceTree,
		publication.LineVersion,
		publication.MutationEpoch,
		publication.ParkIntentID,
		publication.BaseCommit,
		publication.TipCommit,
		publication.Tree,
		boolDBValue(publication.NoChanges),
		toDBTime(publication.AvailableAt),
		toDBTime(publication.CreatedAt),
		toDBTime(publication.UpdatedAt),
	)
	if err != nil {
		return PRDevelopmentPublication{}, err
	}
	stored, err := getPRDevelopmentPublicationByReview(
		ctx,
		conn,
		publication.ReviewLedgerEntryID,
	)
	if err != nil {
		return PRDevelopmentPublication{}, err
	}
	if !equalInitialPRDevelopmentPublication(stored, publication) {
		return PRDevelopmentPublication{}, fmt.Errorf(
			"%w: inserted publication changed before readback",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	return stored, nil
}

func requirePRDevelopmentPublicationAdmissionEvidence(
	ctx context.Context,
	queryer rowsQueryer,
	controller PRDevelopmentController,
	fence PRDevelopmentAttemptReviewFence,
	attemptEntry PRDevelopmentLedgerEntry,
	reviewEntry PRDevelopmentLedgerEntry,
	orchestration PRDevelopmentRepairOrchestration,
	now time.Time,
) error {
	if controller.Phase != PRDevelopmentControllerReady ||
		controller.LeaseKind != "" || controller.LeaseOwner != "" ||
		controller.LeaseToken != "" || controller.LeaseUntil != nil ||
		controller.MutationReservationKey != "" ||
		controller.CurrentAttemptID != fence.AttemptID ||
		controller.FenceCount != fence.Ordinal+1 ||
		controller.FencesDigest != fence.FenceHash ||
		controller.LineID != fence.LineID || controller.LineVersion != fence.LineVersion ||
		controller.MutationEpoch != fence.MutationEpoch ||
		controller.TipCommit != fence.TipCommit || controller.Tree != fence.Tree ||
		controller.Revision != fence.ReviewControllerRevision+1 ||
		!controller.UpdatedAt.Equal(now) ||
		fence.ControllerID != controller.ID || fence.ThreadID != controller.ThreadID ||
		fence.ReviewedAt == nil || !fence.ReviewedAt.Equal(now) ||
		fence.FenceHash != hashPRDevelopmentReviewFence(fence) {
		return fmt.Errorf(
			"%w: passed review has no exact ready controller fence",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	if attemptEntry.Kind != PRDevelopmentLedgerAttempt ||
		reviewEntry.Kind != PRDevelopmentLedgerReview ||
		attemptEntry.ThreadID != controller.ThreadID ||
		reviewEntry.ThreadID != controller.ThreadID ||
		attemptEntry.AttemptID != fence.AttemptID ||
		reviewEntry.AttemptID != fence.AttemptID ||
		attemptEntry.FenceOrdinal != fence.Ordinal ||
		reviewEntry.FenceOrdinal != fence.Ordinal ||
		reviewEntry.Ordinal != attemptEntry.Ordinal+1 ||
		attemptEntry.Ordinal != fence.Ordinal*2 ||
		reviewEntry.PreviousHash != attemptEntry.EntryHash ||
		attemptEntry.FenceHash != mutationStagePRDevelopmentReviewFenceHash(fence) ||
		reviewEntry.FenceHash != fence.FenceHash ||
		reviewEntry.ReviewOutcome != PRDevelopmentLedgerReviewPassed ||
		len(reviewEntry.Findings) != 0 ||
		!reviewEntry.CreatedAt.Equal(now) || attemptEntry.CreatedAt.After(now) ||
		attemptEntry.CaseID == "" || reviewEntry.CaseID != attemptEntry.CaseID ||
		attemptEntry.CaseOrdinal != reviewEntry.CaseOrdinal ||
		attemptEntry.EntryHash != hashPRDevelopmentLedgerEntry(attemptEntry) ||
		reviewEntry.EntryHash != hashPRDevelopmentLedgerEntry(reviewEntry) {
		return fmt.Errorf(
			"%w: passed review has no exact adjacent ledger proof",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	if orchestration.Phase != PRDevelopmentRepairOrchestrationCompleted ||
		orchestration.Validation == nil || orchestration.ControllerID != controller.ID ||
		orchestration.SessionID != controller.OwnerSessionID ||
		orchestration.CaseID != reviewEntry.CaseID ||
		orchestration.ThreadID != controller.ThreadID ||
		orchestration.AgentID != controller.AgentID ||
		orchestration.AttemptID != fence.AttemptID ||
		orchestration.LedgerEntryID != attemptEntry.ID ||
		orchestration.WorkspaceID != controller.WorkspaceID ||
		orchestration.CloneURL != controller.SourceCloneURL ||
		orchestration.HeadRef != controller.SourceRef ||
		orchestration.HeadSHA != controller.SourceCommit ||
		orchestration.SourceTree != controller.SourceTree ||
		orchestration.FenceHash != mutationStagePRDevelopmentReviewFenceHash(fence) {
		return fmt.Errorf(
			"%w: passed review has no exact completed orchestration",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	receipt := *orchestration.Validation
	if receipt.CIStatus != PRDevelopmentCIPassed ||
		receipt.ReceiptHash != hashPRDevelopmentRepairValidationReceipt(receipt) ||
		receipt.CIEffectivePlanDigest != attemptEntry.CIPlanDigest ||
		receipt.CIExecutionDigest != attemptEntry.CIResultDigest ||
		attemptEntry.CIStatus != PRDevelopmentCIPassed || !attemptEntry.ciStatusBound ||
		attemptEntry.Commit != fence.TipCommit || attemptEntry.Tree != fence.Tree ||
		attemptEntry.NoChanges != fence.NoChanges || receipt.CandidateTree != fence.Tree ||
		receipt.NoChanges != fence.NoChanges {
		return fmt.Errorf(
			"%w: passed review has no exact green local-CI receipt",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	session, err := loadPRDevelopmentRepairSessionByID(
		ctx,
		queryer,
		controller.OwnerSessionID,
	)
	if err != nil {
		return err
	}
	if session.CaseID != reviewEntry.CaseID || session.AgentID != controller.AgentID ||
		session.HeadRef != controller.SourceRef || session.CloneURL != controller.SourceCloneURL ||
		session.HeadSHA != controller.SourceCommit || session.WorkspaceID != controller.WorkspaceID ||
		len(session.Attempts) == 0 {
		return fmt.Errorf(
			"%w: publication owner session changed",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	latest := session.Attempts[len(session.Attempts)-1]
	if latest.ID != fence.AttemptID || latest.Status != PRDevelopmentRepairCompleted ||
		latest.SessionID != session.ID {
		return fmt.Errorf(
			"%w: publication attempt is no longer the completed tail",
			ErrPRDevelopmentPublicationSuperseded,
		)
	}
	return nil
}

func equalInitialPRDevelopmentPublication(
	left, right PRDevelopmentPublication,
) bool {
	return equalInitialPRDevelopmentPublicationIdentity(left, right) &&
		equalInitialPRDevelopmentPublicationLedger(left, right) &&
		equalInitialPRDevelopmentPublicationEvidence(left, right) &&
		equalInitialPRDevelopmentPublicationSource(left, right) &&
		equalInitialPRDevelopmentPublicationPark(left, right) &&
		equalInitialPRDevelopmentPublicationState(left, right)
}

func equalInitialPRDevelopmentPublicationIdentity(
	left, right PRDevelopmentPublication,
) bool {
	return left.ID == right.ID && left.CaseID == right.CaseID &&
		left.ThreadID == right.ThreadID && left.ControllerID == right.ControllerID &&
		left.ControllerRevision == right.ControllerRevision &&
		left.OwnerSessionID == right.OwnerSessionID && left.AttemptID == right.AttemptID &&
		left.FenceOrdinal == right.FenceOrdinal && left.FenceHash == right.FenceHash
}

func equalInitialPRDevelopmentPublicationLedger(
	left, right PRDevelopmentPublication,
) bool {
	return left.AttemptLedgerEntryID == right.AttemptLedgerEntryID &&
		left.AttemptLedgerEntryKind == right.AttemptLedgerEntryKind &&
		left.AttemptLedgerEntryHash == right.AttemptLedgerEntryHash &&
		left.ReviewLedgerEntryID == right.ReviewLedgerEntryID &&
		left.ReviewLedgerEntryKind == right.ReviewLedgerEntryKind &&
		left.ReviewLedgerEntryHash == right.ReviewLedgerEntryHash &&
		left.ReviewOutcome == right.ReviewOutcome
}

func equalInitialPRDevelopmentPublicationEvidence(
	left, right PRDevelopmentPublication,
) bool {
	return left.OrchestrationPhase == right.OrchestrationPhase &&
		left.OrchestrationReceiptHash == right.OrchestrationReceiptHash &&
		left.CIStatus == right.CIStatus && left.CIPlanDigest == right.CIPlanDigest &&
		left.CIResultDigest == right.CIResultDigest
}

func equalInitialPRDevelopmentPublicationSource(
	left, right PRDevelopmentPublication,
) bool {
	return left.WorkspaceID == right.WorkspaceID &&
		left.LineID == right.LineID && left.SourceCloneURL == right.SourceCloneURL &&
		left.SourceRef == right.SourceRef && left.SourceCommit == right.SourceCommit &&
		left.SourceTree == right.SourceTree
}

func equalInitialPRDevelopmentPublicationPark(
	left, right PRDevelopmentPublication,
) bool {
	return left.LineVersion == right.LineVersion &&
		left.MutationEpoch == right.MutationEpoch && left.ParkIntentID == right.ParkIntentID &&
		left.BaseCommit == right.BaseCommit && left.TipCommit == right.TipCommit &&
		left.Tree == right.Tree && left.NoChanges == right.NoChanges
}

func equalInitialPRDevelopmentPublicationState(
	left, right PRDevelopmentPublication,
) bool {
	return left.Status == PRDevelopmentPublicationPending && right.Status ==
		PRDevelopmentPublicationPending && left.AvailableAt.Equal(right.AvailableAt) &&
		left.CreatedAt.Equal(right.CreatedAt) && left.UpdatedAt.Equal(right.UpdatedAt) &&
		left.ClaimFrom == "" && left.ClaimOwner == "" && left.ClaimToken == "" &&
		left.ClaimUntil == nil && left.ClaimEpoch == 0 && left.Claims == 0 &&
		left.ClaimedAt == nil && left.Attempts == 0 && left.PolicyRevision == "" &&
		left.SubjectRevision == "" && left.ProviderObservedAt == nil &&
		left.DecisionRunID == "" && left.PushRequestHash == "" &&
		left.PushResultHash == "" && left.LastErrorCode == "" &&
		left.LastErrorDetail == "" && left.EffectStartedAt == nil &&
		left.CompletedAt == nil
}

func loadOptionalPRDevelopmentPublicationForReview(
	ctx context.Context,
	queryer rowQueryer,
	reviewLedgerEntryID string,
) (*PRDevelopmentPublication, error) {
	publication, err := getPRDevelopmentPublicationByReview(
		ctx,
		queryer,
		reviewLedgerEntryID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	publication = redactPRDevelopmentPublicationAuthority(publication)
	return &publication, nil
}
