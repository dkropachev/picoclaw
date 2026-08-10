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
	_ PRDevelopmentControllerReader         = (*Store)(nil)
	_ PRDevelopmentControllerStore          = (*Store)(nil)
	_ PRDevelopmentControllerOperationStore = (*Store)(nil)

	errInvalidStoredPRDevelopmentController = errors.New(
		"invalid stored pull request development controller",
	)
)

const (
	prDevelopmentControllerColumns = `
		id, thread_id, owner_session_id, agent_id, revision, phase, line_id,
		workspace_id, source_clone_url, source_ref, source_commit, source_tree,
		line_version, mutation_epoch, tip_commit, tree, current_attempt_id,
		lease_kind, lease_owner, lease_token, lease_until, lease_epoch, claims,
		mutation_reservation_key, fence_count, fences_digest, created_at, updated_at`
	prDevelopmentReviewFenceColumns = `
		attempt_id, controller_id, thread_id, line_id, ordinal, line_version,
		mutation_epoch, park_intent_id, base_commit, tip_commit, tree, no_changes,
		line_review_digest, mutation_reservation_digest, mutation_lease_epoch,
		mutation_lease_token_digest, mutation_controller_revision,
		review_lease_epoch, review_lease_token_digest, review_controller_revision,
		previous_hash, fence_hash, created_at, reviewed_at`

	prDevelopmentControllerLeaseTokenBytes               = 128
	prDevelopmentControllerCloneURLBytes                 = 4096
	prDevelopmentControllerRefBytes                      = 1024
	prDevelopmentControllerMutationRevisionReserve int64 = 6
)

// GetPRDevelopmentControllerForCase resolves the selected case's provider
// thread and validates the complete private controller and fence chain from one
// SQLite snapshot. It redacts both live lease tokens and the raw filesystem
// reservation before returning, so this read grants no mutation or review
// authority.
func (s *Store) GetPRDevelopmentControllerForCase(
	ctx context.Context,
	caseID string,
) (PRDevelopmentController, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentController{}, err
	}
	caseID = strings.TrimSpace(caseID)
	if !validPrefixedHexID(caseID, prDevelopmentCaseIDPrefix) {
		return PRDevelopmentController{}, fmt.Errorf(
			"%w: invalid development case ID",
			ErrInvalidPRDevelopmentController,
		)
	}

	var controller PRDevelopmentController
	err := s.withPRDevelopmentConversationReadSnapshot(
		ctx,
		func(queryer rowsQueryer) error {
			binding, loadErr := loadPRDevelopmentThreadBindingForCase(ctx, queryer, caseID)
			if loadErr != nil {
				return loadErr
			}
			if binding.Kind != PRDevelopmentThreadProvider {
				return fmt.Errorf(
					"%w: legacy threads cannot own development controllers",
					ErrPRDevelopmentControllerConflict,
				)
			}
			loaded, found, loadErr := loadPRDevelopmentControllerAggregate(
				ctx,
				queryer,
				binding.ID,
			)
			if loadErr != nil {
				return loadErr
			}
			if !found {
				return sql.ErrNoRows
			}
			controller = loaded
			return nil
		},
	)
	if err != nil {
		return PRDevelopmentController{}, fmt.Errorf(
			"get pull request development controller: %w",
			s.dbError(err),
		)
	}
	controller.LeaseToken = ""
	controller.MutationReservationKey = ""
	return controller, nil
}

// AcquirePRDevelopmentControllerLease acquires either the exclusive raw
// workspace mutation bearer or a reservation-free immutable-review lease.
// Eventing-recoverable expired mutation authority is fenced into
// recovery_required and is never automatically transferred to another worker.
func (s *Store) AcquirePRDevelopmentControllerLease(
	ctx context.Context,
	input PRDevelopmentControllerAcquire,
) (PRDevelopmentControllerLease, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentControllerLease{}, false, err
	}
	normalized, err := normalizePRDevelopmentControllerAcquire(input)
	if err != nil {
		return PRDevelopmentControllerLease{}, false, err
	}

	var (
		lease            PRDevelopmentControllerLease
		recoveryRequired bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		relation, relationErr := loadPRDevelopmentControllerAttemptRelation(
			ctx,
			conn,
			normalized.CaseID,
			normalized.AttemptID,
		)
		if relationErr != nil {
			return relationErr
		}
		controller, found, loadErr := loadPRDevelopmentControllerAggregate(
			ctx,
			conn,
			relation.Thread.ID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			if normalized.Kind != PRDevelopmentControllerMutationLease {
				return fmt.Errorf(
					"%w: a mutation lease must create the controller before review",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if normalized.ExpectedRevision != 0 {
				return fmt.Errorf(
					"%w: expected revision %d, current revision 0",
					ErrPRDevelopmentControllerConflict,
					normalized.ExpectedRevision,
				)
			}
			if siblingErr := requireNoSiblingPRDevelopmentRepairSessions(
				ctx,
				conn,
				relation.Thread.ID,
				relation.Session.ID,
			); siblingErr != nil {
				return siblingErr
			}
			now, clockErr := s.currentTime()
			if clockErr != nil {
				return clockErr
			}
			if timeErr := requireNonRegressingPRDevelopmentControllerTime(
				now,
				maxPRDevelopmentControllerTime(
					relation.Session.UpdatedAt,
					relation.Attempt.UpdatedAt,
				),
			); timeErr != nil {
				return timeErr
			}
			deadline, deadlineErr := prDevelopmentControllerDeadline(now, normalized.Lease)
			if deadlineErr != nil {
				return deadlineErr
			}
			created, createErr := insertLeasedPRDevelopmentController(
				ctx,
				conn,
				relation,
				normalized,
				now,
				deadline,
				false,
			)
			if createErr != nil {
				return createErr
			}
			lease.Controller = created
			lease.Created = true
			return nil
		}
		if controller.OwnerSessionID != relation.Session.ID ||
			controller.AgentID != relation.Session.AgentID {
			return fmt.Errorf(
				"%w: attempt does not belong to the controller owner",
				ErrPRDevelopmentControllerConflict,
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
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if timeErr := requireNonRegressingPRDevelopmentControllerTime(
			now,
			maxPRDevelopmentControllerTime(
				controller.UpdatedAt,
				relation.Session.UpdatedAt,
				relation.Attempt.UpdatedAt,
			),
		); timeErr != nil {
			return timeErr
		}
		deadline, deadlineErr := prDevelopmentControllerDeadline(now, normalized.Lease)
		if deadlineErr != nil {
			return deadlineErr
		}
		if controller.Phase == PRDevelopmentControllerMutation &&
			controller.LeaseUntil != nil && !controller.LeaseUntil.After(now) {
			if expireErr := expirePRDevelopmentMutationLease(
				ctx,
				conn,
				controller,
				now,
			); expireErr != nil {
				return expireErr
			}
			recoveryRequired = true
			return nil
		}

		switch normalized.Kind {
		case PRDevelopmentControllerMutationLease:
			updated, transitionErr := acquirePRDevelopmentMutationLease(
				ctx,
				conn,
				controller,
				normalized,
				now,
				deadline,
			)
			if errors.Is(transitionErr, ErrPRDevelopmentControllerRecoveryRequired) {
				recoveryRequired = true
				return nil
			}
			if transitionErr != nil {
				return transitionErr
			}
			lease.Controller = updated
		case PRDevelopmentControllerReviewLease:
			updated, fence, reclaimed, transitionErr := acquirePRDevelopmentReviewLease(
				ctx,
				conn,
				controller,
				normalized,
				now,
				deadline,
			)
			if transitionErr != nil {
				return transitionErr
			}
			lease.Controller = updated
			lease.ReviewFence = &fence
			lease.Reclaimed = reclaimed
		default:
			return fmt.Errorf(
				"%w: unknown lease kind",
				ErrInvalidPRDevelopmentController,
			)
		}
		return nil
	})
	if err != nil {
		return PRDevelopmentControllerLease{}, false, fmt.Errorf(
			"acquire pull request development controller lease: %w",
			s.dbError(err),
		)
	}
	if recoveryRequired {
		return PRDevelopmentControllerLease{}, false, fmt.Errorf(
			"acquire pull request development controller lease: %w",
			ErrPRDevelopmentControllerRecoveryRequired,
		)
	}
	return lease, true, nil
}

// RenewPRDevelopmentControllerLease extends only the exact still-live lease.
// An eventing-recoverable expired mutation lease is durably converted to
// recovery_required while an expired review lease must be safely reclaimed
// through Acquire.
func (s *Store) RenewPRDevelopmentControllerLease(
	ctx context.Context,
	input PRDevelopmentControllerRenew,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	normalized, err := normalizePRDevelopmentControllerRenew(input)
	if err != nil {
		return err
	}
	recoveryRequired := false
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		controller, found, loadErr := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			normalized.ControllerID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		attemptHighWater, highWaterErr := loadPRDevelopmentControllerAttemptHighWater(
			ctx,
			conn,
			controller,
		)
		if highWaterErr != nil {
			return highWaterErr
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if timeErr := requireNonRegressingPRDevelopmentControllerTime(
			now,
			maxPRDevelopmentControllerTime(controller.UpdatedAt, attemptHighWater),
		); timeErr != nil {
			return timeErr
		}
		deadline, deadlineErr := prDevelopmentControllerDeadline(now, normalized.Lease)
		if deadlineErr != nil {
			return deadlineErr
		}
		if controller.Phase == PRDevelopmentControllerMutation &&
			controller.LeaseUntil != nil && !controller.LeaseUntil.After(now) {
			if controller.LeaseKind != PRDevelopmentControllerMutationLease ||
				controller.CurrentAttemptID != normalized.AttemptID ||
				controller.LeaseToken != normalized.LeaseToken ||
				controller.LeaseEpoch != normalized.LeaseEpoch {
				return fmt.Errorf(
					"%w: expired renewal does not hold the exact current mutation lease",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if expireErr := expirePRDevelopmentMutationLease(
				ctx,
				conn,
				controller,
				now,
			); expireErr != nil {
				return expireErr
			}
			recoveryRequired = true
			return nil
		}
		if controller.CurrentAttemptID != normalized.AttemptID ||
			controller.LeaseToken != normalized.LeaseToken ||
			controller.LeaseEpoch != normalized.LeaseEpoch ||
			controller.LeaseUntil == nil || !controller.LeaseUntil.After(now) ||
			(controller.Phase != PRDevelopmentControllerMutation &&
				controller.Phase != PRDevelopmentControllerReview) {
			return fmt.Errorf(
				"%w: controller lease is not the exact live owner",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if controller.LeaseUntil.After(deadline) {
			deadline = *controller.LeaseUntil
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_thread_controllers
			SET lease_until = ?, updated_at = ?
			WHERE id = ? AND current_attempt_id = ? AND phase = ? AND
				lease_token = ? AND lease_epoch = ? AND lease_until > ?`,
			toDBTime(deadline),
			toDBTime(now),
			controller.ID,
			controller.CurrentAttemptID,
			controller.Phase,
			controller.LeaseToken,
			controller.LeaseEpoch,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		return requireOnePRDevelopmentControllerRow(result)
	})
	if err != nil {
		return fmt.Errorf(
			"renew pull request development controller lease: %w",
			s.dbError(err),
		)
	}
	if recoveryRequired {
		return fmt.Errorf(
			"renew pull request development controller lease: %w",
			ErrPRDevelopmentControllerRecoveryRequired,
		)
	}
	return nil
}

// BindPRDevelopmentControllerLine stores the immutable source and retained
// line identity reported by the exact live mutation owner. First adoption and
// each newly acquired Resume durably advance the binding epoch and revision;
// retrying that exact committed assertion consumes no further revision.
func (s *Store) BindPRDevelopmentControllerLine(
	ctx context.Context,
	input PRDevelopmentControllerLineBind,
) (PRDevelopmentController, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentController{}, false, err
	}
	normalized, err := normalizePRDevelopmentControllerLineBind(input)
	if err != nil {
		return PRDevelopmentController{}, false, err
	}

	var (
		controller       PRDevelopmentController
		changed          bool
		recoveryRequired bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		current, found, loadErr := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			normalized.ControllerID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		if activeErr := requireNoActivePRDevelopmentControllerOperation(
			ctx,
			conn,
			current.ID,
		); activeErr != nil {
			return activeErr
		}
		var operationCount int
		if countErr := conn.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM pr_development_controller_operation_intents
			WHERE controller_id = ?`,
			current.ID,
		).Scan(&operationCount); countErr != nil {
			return countErr
		}
		if operationCount != 0 {
			return fmt.Errorf(
				"%w: controller operation history owns all later line transitions",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if pinErr := requirePRDevelopmentControllerOwnerPin(
			ctx,
			conn,
			current,
			normalized.WorkspaceID,
			normalized.SourceCloneURL,
			normalized.SourceRef,
			normalized.SourceCommit,
		); pinErr != nil {
			return pinErr
		}
		attemptHighWater, highWaterErr := loadPRDevelopmentControllerAttemptHighWater(
			ctx,
			conn,
			current,
		)
		if highWaterErr != nil {
			return highWaterErr
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if timeErr := requireNonRegressingPRDevelopmentControllerTime(
			now,
			maxPRDevelopmentControllerTime(current.UpdatedAt, attemptHighWater),
		); timeErr != nil {
			return timeErr
		}
		exactReplay := current.WorkspaceID != "" &&
			equalPRDevelopmentControllerLineBindingExceptEpoch(current, normalized) &&
			current.MutationEpoch == normalized.MutationEpoch &&
			normalized.MutationEpoch == current.LineVersion+1 &&
			current.Phase == PRDevelopmentControllerMutation &&
			current.LeaseKind == PRDevelopmentControllerMutationLease &&
			current.LeaseToken == normalized.LeaseToken &&
			current.LeaseEpoch == normalized.LeaseEpoch &&
			current.Revision >= normalized.ExpectedRevision &&
			current.Revision <= normalized.ExpectedRevision+1
		if current.Phase == PRDevelopmentControllerMutation &&
			current.LeaseUntil != nil && !current.LeaseUntil.After(now) {
			exactCurrentCaller := current.LeaseKind == PRDevelopmentControllerMutationLease &&
				current.CurrentAttemptID == normalized.AttemptID &&
				current.LeaseToken == normalized.LeaseToken &&
				current.LeaseEpoch == normalized.LeaseEpoch &&
				current.Revision == normalized.ExpectedRevision
			if !exactCurrentCaller && !exactReplay {
				return fmt.Errorf(
					"%w: expired mutation operation does not hold the exact current lease and revision",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if expireErr := expirePRDevelopmentMutationLease(
				ctx,
				conn,
				current,
				now,
			); expireErr != nil {
				return expireErr
			}
			recoveryRequired = true
			return nil
		}
		if exactReplay && current.LeaseUntil != nil && current.LeaseUntil.After(now) {
			controller = current
			return nil
		}
		if leaseErr := requireLivePRDevelopmentControllerLease(
			current,
			normalized.AttemptID,
			normalized.LeaseToken,
			normalized.LeaseEpoch,
			PRDevelopmentControllerMutation,
			now,
		); leaseErr != nil {
			return leaseErr
		}
		if current.WorkspaceID != "" {
			if !equalPRDevelopmentControllerLineBindingExceptEpoch(current, normalized) ||
				normalized.MutationEpoch != current.LineVersion+1 {
				return fmt.Errorf(
					"%w: retained line binding is immutable",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if current.MutationEpoch != current.LineVersion ||
				current.Revision != normalized.ExpectedRevision {
				return fmt.Errorf(
					"%w: resume binding expected revision or mutation epoch is stale",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if current.Revision > MaxPRDevelopmentControllerRevision-3 {
				return fmt.Errorf(
					"%w: controller revision capacity exhausted",
					ErrPRDevelopmentControllerConflict,
				)
			}
			result, updateErr := conn.ExecContext(ctx, `
				UPDATE pr_development_thread_controllers
				SET mutation_epoch = ?, revision = revision + 1, updated_at = ?
				WHERE id = ? AND revision = ? AND phase = 'mutation' AND
					current_attempt_id = ? AND lease_token = ? AND lease_epoch = ? AND
					workspace_id = ? AND line_version = ? AND mutation_epoch = ?`,
				normalized.MutationEpoch,
				toDBTime(now),
				current.ID,
				current.Revision,
				current.CurrentAttemptID,
				current.LeaseToken,
				current.LeaseEpoch,
				current.WorkspaceID,
				current.LineVersion,
				current.MutationEpoch,
			)
			if updateErr != nil {
				return updateErr
			}
			if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
				return rowErr
			}
			loaded, loadedFound, reloadErr := loadPRDevelopmentControllerAggregateByID(
				ctx,
				conn,
				current.ID,
			)
			if reloadErr != nil {
				return reloadErr
			}
			if !loadedFound {
				return errors.New("resumed pull request development controller disappeared")
			}
			controller = loaded
			changed = true
			return nil
		}
		if current.Revision != normalized.ExpectedRevision {
			return fmt.Errorf(
				"%w: expected revision %d, current revision %d",
				ErrPRDevelopmentControllerConflict,
				normalized.ExpectedRevision,
				current.Revision,
			)
		}
		if normalized.LineVersion != 0 || normalized.MutationEpoch != 1 ||
			normalized.TipCommit != normalized.SourceCommit ||
			normalized.Tree != normalized.SourceTree {
			return fmt.Errorf(
				"%w: first line binding must be source version zero at mutation epoch one",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if current.Revision > MaxPRDevelopmentControllerRevision-3 {
			return fmt.Errorf(
				"%w: controller revision capacity exhausted",
				ErrPRDevelopmentControllerConflict,
			)
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_thread_controllers
			SET workspace_id = ?, source_clone_url = ?, source_ref = ?,
				source_commit = ?, source_tree = ?, line_version = ?,
				mutation_epoch = ?, tip_commit = ?, tree = ?,
				revision = revision + 1, updated_at = ?
			WHERE id = ? AND revision = ? AND phase = 'mutation' AND
				current_attempt_id = ? AND lease_token = ? AND lease_epoch = ? AND
				workspace_id = ''`,
			normalized.WorkspaceID,
			normalized.SourceCloneURL,
			normalized.SourceRef,
			normalized.SourceCommit,
			normalized.SourceTree,
			normalized.LineVersion,
			normalized.MutationEpoch,
			normalized.TipCommit,
			normalized.Tree,
			toDBTime(now),
			current.ID,
			current.Revision,
			current.CurrentAttemptID,
			current.LeaseToken,
			current.LeaseEpoch,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
			return rowErr
		}
		loaded, found, loadErr := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			current.ID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return errors.New("bound pull request development controller disappeared")
		}
		controller = loaded
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentController{}, false, fmt.Errorf(
			"bind pull request development controller line: %w",
			s.dbError(err),
		)
	}
	if recoveryRequired {
		return PRDevelopmentController{}, false, fmt.Errorf(
			"bind pull request development controller line: %w",
			ErrPRDevelopmentControllerRecoveryRequired,
		)
	}
	return controller, changed, nil
}

// RecordPRDevelopmentAttemptReviewFence atomically appends the exact parked
// line snapshot, hash-chains its non-authorizing reservation fingerprint, and
// retires both the raw mutation bearer and mutation lease before review.
func (s *Store) RecordPRDevelopmentAttemptReviewFence(
	ctx context.Context,
	input PRDevelopmentAttemptReviewFenceRecord,
) (PRDevelopmentAttemptReviewFence, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentAttemptReviewFence{}, false, err
	}
	normalized, err := normalizePRDevelopmentAttemptReviewFenceRecord(input)
	if err != nil {
		return PRDevelopmentAttemptReviewFence{}, false, err
	}

	var (
		fence            PRDevelopmentAttemptReviewFence
		changed          bool
		recoveryRequired bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		existing, found, loadErr := loadPRDevelopmentReviewFenceByAttempt(
			ctx,
			conn,
			normalized.AttemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		if found {
			if !equalPRDevelopmentReviewFenceRecord(existing, normalized) {
				return fmt.Errorf(
					"%w: attempt fence is bound to different parked evidence",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if _, aggregateFound, aggregateErr := loadPRDevelopmentControllerAggregateByID(
				ctx,
				conn,
				existing.ControllerID,
			); aggregateErr != nil {
				return aggregateErr
			} else if !aggregateFound {
				return errors.New("replayed review fence has no controller")
			}
			fence = existing
			return nil
		}

		controller, found, loadErr := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			normalized.ControllerID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		if activeErr := requireNoActivePRDevelopmentControllerOperation(
			ctx,
			conn,
			controller.ID,
		); activeErr != nil {
			return activeErr
		}
		var operationCount int
		if countErr := conn.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM pr_development_controller_operation_intents
			WHERE controller_id = ? AND attempt_id = ?`,
			controller.ID,
			normalized.AttemptID,
		).Scan(&operationCount); countErr != nil {
			return countErr
		}
		if operationCount != 0 {
			return fmt.Errorf(
				"%w: an operation-owned attempt must be fenced by its Park operation",
				ErrPRDevelopmentControllerConflict,
			)
		}
		completionHighWater, completionErr := requireCompletedPRDevelopmentControllerAttempt(
			ctx,
			conn,
			controller,
			normalized.AttemptID,
		)
		if completionErr != nil {
			return completionErr
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if timeErr := requireNonRegressingPRDevelopmentControllerTime(
			now,
			maxPRDevelopmentControllerTime(controller.UpdatedAt, completionHighWater),
		); timeErr != nil {
			return timeErr
		}
		if controller.Phase == PRDevelopmentControllerMutation &&
			controller.LeaseUntil != nil && !controller.LeaseUntil.After(now) {
			if controller.LeaseKind != PRDevelopmentControllerMutationLease ||
				controller.CurrentAttemptID != normalized.AttemptID ||
				controller.LeaseToken != normalized.LeaseToken ||
				controller.LeaseEpoch != normalized.LeaseEpoch ||
				controller.Revision != normalized.ExpectedRevision {
				return fmt.Errorf(
					"%w: expired fence operation does not hold the exact current lease and revision",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if expireErr := expirePRDevelopmentMutationLease(
				ctx,
				conn,
				controller,
				now,
			); expireErr != nil {
				return expireErr
			}
			recoveryRequired = true
			return nil
		}
		if leaseErr := requireLivePRDevelopmentControllerLease(
			controller,
			normalized.AttemptID,
			normalized.LeaseToken,
			normalized.LeaseEpoch,
			PRDevelopmentControllerMutation,
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
		if controller.WorkspaceID == "" {
			return fmt.Errorf(
				"%w: retained line must be bound before it can be parked",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if controller.FenceCount >= MaxPRDevelopmentControllerFences ||
			controller.Revision > MaxPRDevelopmentControllerRevision-2 {
			return fmt.Errorf(
				"%w: controller fence or revision capacity exhausted",
				ErrPRDevelopmentControllerConflict,
			)
		}
		expectedVersion := controller.LineVersion + 1
		if normalized.LineVersion != expectedVersion ||
			normalized.MutationEpoch != expectedVersion ||
			controller.MutationEpoch != expectedVersion ||
			normalized.BaseCommit != controller.TipCommit {
			return fmt.Errorf(
				"%w: parked line version, mutation epoch, or base is not contiguous",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if controller.FenceCount == 0 &&
			normalized.BaseCommit != controller.SourceCommit {
			return fmt.Errorf(
				"%w: first fence base does not match the immutable source",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if normalized.NoChanges && normalized.Tree != controller.Tree {
			return fmt.Errorf(
				"%w: no-change fence must preserve the prior tree",
				ErrPRDevelopmentControllerConflict,
			)
		}
		reservationDigest := prDevelopmentMutationReservationDigest(
			controller.MutationReservationKey,
		)
		var duplicate int
		if queryErr := conn.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM pr_development_attempt_review_fences
			WHERE mutation_reservation_digest = ?`,
			reservationDigest,
		).Scan(&duplicate); queryErr != nil {
			return queryErr
		}
		if duplicate != 0 {
			return fmt.Errorf(
				"%w: mutation reservation was already retired",
				ErrPRDevelopmentControllerConflict,
			)
		}
		fence = PRDevelopmentAttemptReviewFence{
			AttemptID:                 normalized.AttemptID,
			ControllerID:              controller.ID,
			ThreadID:                  controller.ThreadID,
			LineID:                    controller.LineID,
			Ordinal:                   controller.FenceCount,
			LineVersion:               normalized.LineVersion,
			MutationEpoch:             normalized.MutationEpoch,
			ParkIntentID:              normalized.ParkIntentID,
			BaseCommit:                normalized.BaseCommit,
			TipCommit:                 normalized.TipCommit,
			Tree:                      normalized.Tree,
			NoChanges:                 normalized.NoChanges,
			LineReviewDigest:          normalized.LineReviewDigest,
			MutationReservationDigest: reservationDigest,
			MutationLeaseEpoch:        controller.LeaseEpoch,
			MutationLeaseTokenDigest: prDevelopmentLeaseTokenDigest(
				PRDevelopmentControllerMutationLease,
				controller.LeaseToken,
			),
			MutationControllerRevision: controller.Revision,
			PreviousHash:               controller.FencesDigest,
			CreatedAt:                  now,
		}
		fence.FenceHash = hashPRDevelopmentReviewFence(fence)
		_, insertErr := conn.ExecContext(ctx, `
			INSERT INTO pr_development_attempt_review_fences (
				attempt_id, controller_id, thread_id, line_id, ordinal, line_version,
				mutation_epoch, park_intent_id, base_commit, tip_commit, tree,
				no_changes, line_review_digest, mutation_reservation_digest,
				mutation_lease_epoch, mutation_lease_token_digest,
				mutation_controller_revision, review_lease_epoch,
				review_lease_token_digest, review_controller_revision,
				previous_hash, fence_hash, created_at, reviewed_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, '', 0, ?, ?, ?, NULL)`,
			fence.AttemptID,
			fence.ControllerID,
			fence.ThreadID,
			fence.LineID,
			fence.Ordinal,
			fence.LineVersion,
			fence.MutationEpoch,
			fence.ParkIntentID,
			fence.BaseCommit,
			fence.TipCommit,
			fence.Tree,
			boolDBValue(fence.NoChanges),
			fence.LineReviewDigest,
			fence.MutationReservationDigest,
			fence.MutationLeaseEpoch,
			fence.MutationLeaseTokenDigest,
			fence.MutationControllerRevision,
			fence.PreviousHash,
			fence.FenceHash,
			toDBTime(fence.CreatedAt),
		)
		if insertErr != nil {
			return insertErr
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_thread_controllers
			SET revision = revision + 1, phase = 'review_pending',
				line_version = ?, mutation_epoch = ?, tip_commit = ?, tree = ?,
				lease_kind = '', lease_owner = '', lease_token = '', lease_until = NULL,
				mutation_reservation_key = '', fence_count = fence_count + 1,
				fences_digest = ?, updated_at = ?
			WHERE id = ? AND revision = ? AND phase = 'mutation' AND
				current_attempt_id = ? AND lease_token = ? AND lease_epoch = ? AND
				mutation_reservation_key = ?`,
			fence.LineVersion,
			fence.MutationEpoch,
			fence.TipCommit,
			fence.Tree,
			fence.FenceHash,
			toDBTime(now),
			controller.ID,
			controller.Revision,
			controller.CurrentAttemptID,
			controller.LeaseToken,
			controller.LeaseEpoch,
			controller.MutationReservationKey,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
			return rowErr
		}
		if _, found, loadErr = loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			controller.ID,
		); loadErr != nil {
			return loadErr
		} else if !found {
			return errors.New("parked pull request development controller disappeared")
		}
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentAttemptReviewFence{}, false, fmt.Errorf(
			"record pull request development attempt review fence: %w",
			s.dbError(err),
		)
	}
	if recoveryRequired {
		return PRDevelopmentAttemptReviewFence{}, false, fmt.Errorf(
			"record pull request development attempt review fence: %w",
			ErrPRDevelopmentControllerRecoveryRequired,
		)
	}
	return fence, changed, nil
}

// FinishPRDevelopmentControllerReview marks the exact pending fence reviewed
// and releases its reservation-free review lease into ready.
func (s *Store) FinishPRDevelopmentControllerReview(
	ctx context.Context,
	input PRDevelopmentControllerReviewTransition,
) (PRDevelopmentController, bool, error) {
	return s.transitionPRDevelopmentControllerReview(ctx, input, true)
}

// ReleasePRDevelopmentControllerReview releases the exact live review lease
// back to review_pending without marking its immutable fence reviewed.
func (s *Store) ReleasePRDevelopmentControllerReview(
	ctx context.Context,
	input PRDevelopmentControllerReviewTransition,
) (PRDevelopmentController, error) {
	controller, _, err := s.transitionPRDevelopmentControllerReview(ctx, input, false)
	return controller, err
}

func (s *Store) transitionPRDevelopmentControllerReview(
	ctx context.Context,
	input PRDevelopmentControllerReviewTransition,
	finish bool,
) (PRDevelopmentController, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentController{}, false, err
	}
	normalized, err := normalizePRDevelopmentControllerReviewTransition(input)
	if err != nil {
		return PRDevelopmentController{}, false, err
	}
	var (
		controller PRDevelopmentController
		changed    bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		current, found, loadErr := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			normalized.ControllerID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		latest, found, loadErr := loadLatestPRDevelopmentReviewFence(
			ctx,
			conn,
			current.ID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found || latest.AttemptID != normalized.AttemptID {
			return fmt.Errorf(
				"%w: review transition does not identify the latest fence",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if finish && current.Phase == PRDevelopmentControllerReady &&
			latest.ReviewedAt != nil &&
			equalPRDevelopmentFinishedReviewReplay(latest, normalized) &&
			current.Revision == normalized.ExpectedRevision+1 {
			controller = current
			return nil
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if timeErr := requireNonRegressingPRDevelopmentControllerTime(
			now,
			current.UpdatedAt,
		); timeErr != nil {
			return timeErr
		}
		if leaseErr := requireLivePRDevelopmentControllerLease(
			current,
			normalized.AttemptID,
			normalized.LeaseToken,
			normalized.LeaseEpoch,
			PRDevelopmentControllerReview,
			now,
		); leaseErr != nil {
			return leaseErr
		}
		if current.Revision != normalized.ExpectedRevision {
			return fmt.Errorf(
				"%w: expected revision %d, current revision %d",
				ErrPRDevelopmentControllerConflict,
				normalized.ExpectedRevision,
				current.Revision,
			)
		}
		revisionHeadroom := int64(2)
		if finish {
			revisionHeadroom = 1
		}
		if latest.ReviewedAt != nil ||
			current.Revision > MaxPRDevelopmentControllerRevision-revisionHeadroom {
			return fmt.Errorf(
				"%w: review fence is already reviewed or revision capacity is exhausted",
				ErrPRDevelopmentControllerConflict,
			)
		}
		nextPhase := PRDevelopmentControllerReviewPending
		nextFencesDigest := current.FencesDigest
		if finish {
			nextPhase = PRDevelopmentControllerReady
			reviewedAt := now
			latest.ReviewedAt = &reviewedAt
			latest.ReviewLeaseEpoch = current.LeaseEpoch
			latest.ReviewLeaseTokenDigest = prDevelopmentLeaseTokenDigest(
				PRDevelopmentControllerReviewLease,
				current.LeaseToken,
			)
			latest.ReviewControllerRevision = current.Revision
			latest.FenceHash = hashPRDevelopmentReviewFence(latest)
			nextFencesDigest = latest.FenceHash
			result, reviewErr := conn.ExecContext(ctx, `
				UPDATE pr_development_attempt_review_fences
				SET reviewed_at = ?, review_lease_epoch = ?,
					review_lease_token_digest = ?, review_controller_revision = ?,
					fence_hash = ?
				WHERE attempt_id = ? AND controller_id = ? AND reviewed_at IS NULL`,
				toDBTime(now),
				latest.ReviewLeaseEpoch,
				latest.ReviewLeaseTokenDigest,
				latest.ReviewControllerRevision,
				latest.FenceHash,
				latest.AttemptID,
				current.ID,
			)
			if reviewErr != nil {
				return reviewErr
			}
			if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
				return rowErr
			}
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_thread_controllers
			SET revision = revision + 1, phase = ?, lease_kind = '',
				lease_owner = '', lease_token = '', lease_until = NULL,
				fences_digest = ?, updated_at = ?
			WHERE id = ? AND revision = ? AND phase = 'review' AND
				current_attempt_id = ? AND lease_token = ? AND lease_epoch = ?`,
			nextPhase,
			nextFencesDigest,
			toDBTime(now),
			current.ID,
			current.Revision,
			current.CurrentAttemptID,
			current.LeaseToken,
			current.LeaseEpoch,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
			return rowErr
		}
		loaded, found, loadErr := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			current.ID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return errors.New("reviewed pull request development controller disappeared")
		}
		controller = loaded
		changed = true
		return nil
	})
	if err != nil {
		action := "release"
		if finish {
			action = "finish"
		}
		return PRDevelopmentController{}, false, fmt.Errorf(
			"%s pull request development controller review: %w",
			action,
			s.dbError(err),
		)
	}
	return controller, changed, nil
}

func normalizePRDevelopmentControllerAcquire(
	input PRDevelopmentControllerAcquire,
) (PRDevelopmentControllerAcquire, error) {
	input.CaseID = strings.TrimSpace(input.CaseID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	worker, err := normalizePRDevelopmentControllerIdentity(
		"worker label",
		input.WorkerLabel,
		MaxPRDevelopmentControllerIdentityBytes,
		true,
	)
	if err != nil {
		return PRDevelopmentControllerAcquire{}, err
	}
	input.WorkerLabel = worker
	if !validPrefixedHexID(input.CaseID, prDevelopmentCaseIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		input.ExpectedRevision < 0 ||
		input.ExpectedRevision > MaxPRDevelopmentControllerRevision ||
		(input.Kind != PRDevelopmentControllerMutationLease &&
			input.Kind != PRDevelopmentControllerReviewLease) ||
		input.Lease <= 0 {
		return PRDevelopmentControllerAcquire{}, fmt.Errorf(
			"%w: valid case, attempt, revision, lease kind, worker, and positive lease are required",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func normalizePRDevelopmentControllerRenew(
	input PRDevelopmentControllerRenew,
) (PRDevelopmentControllerRenew, error) {
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	token, err := normalizePRDevelopmentControllerIdentity(
		"lease token",
		input.LeaseToken,
		prDevelopmentControllerLeaseTokenBytes,
		true,
	)
	if err != nil {
		return PRDevelopmentControllerRenew{}, err
	}
	input.LeaseToken = token
	if !validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		input.LeaseEpoch < 1 || input.Lease <= 0 {
		return PRDevelopmentControllerRenew{}, fmt.Errorf(
			"%w: valid controller, attempt, token, epoch, and positive lease are required",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func normalizePRDevelopmentControllerLineBind(
	input PRDevelopmentControllerLineBind,
) (PRDevelopmentControllerLineBind, error) {
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	var err error
	for _, field := range []struct {
		name    string
		value   *string
		maximum int
	}{
		{"lease token", &input.LeaseToken, prDevelopmentControllerLeaseTokenBytes},
		{"workspace ID", &input.WorkspaceID, MaxPRDevelopmentControllerIdentityBytes},
	} {
		*field.value, err = normalizePRDevelopmentControllerIdentity(
			field.name,
			*field.value,
			field.maximum,
			true,
		)
		if err != nil {
			return PRDevelopmentControllerLineBind{}, err
		}
	}
	input.SourceCloneURL = strings.TrimSpace(input.SourceCloneURL)
	input.SourceRef = strings.TrimSpace(input.SourceRef)
	input.SourceCommit = strings.TrimSpace(input.SourceCommit)
	input.SourceTree = strings.TrimSpace(input.SourceTree)
	input.TipCommit = strings.TrimSpace(input.TipCommit)
	input.Tree = strings.TrimSpace(input.Tree)
	if !validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		input.ExpectedRevision < 1 ||
		input.ExpectedRevision > MaxPRDevelopmentControllerRevision ||
		input.LeaseEpoch < 1 ||
		!validPRDevelopmentRepairCloneURL(input.SourceCloneURL) ||
		!validPRDevelopmentGitRef(input.SourceRef) ||
		!validSameWidthPRDevelopmentOIDs(
			input.SourceCommit,
			input.SourceTree,
			input.TipCommit,
			input.Tree,
		) ||
		input.LineVersion < 0 ||
		input.LineVersion > MaxPRDevelopmentControllerFences ||
		input.MutationEpoch < 1 ||
		input.MutationEpoch > MaxPRDevelopmentControllerFences+1 {
		return PRDevelopmentControllerLineBind{}, fmt.Errorf(
			"%w: invalid retained-line binding",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func normalizePRDevelopmentAttemptReviewFenceRecord(
	input PRDevelopmentAttemptReviewFenceRecord,
) (PRDevelopmentAttemptReviewFenceRecord, error) {
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	var err error
	for _, field := range []struct {
		name    string
		value   *string
		maximum int
	}{
		{"lease token", &input.LeaseToken, prDevelopmentControllerLeaseTokenBytes},
		{"park intent ID", &input.ParkIntentID, MaxPRDevelopmentControllerIdentityBytes},
	} {
		*field.value, err = normalizePRDevelopmentControllerIdentity(
			field.name,
			*field.value,
			field.maximum,
			true,
		)
		if err != nil {
			return PRDevelopmentAttemptReviewFenceRecord{}, err
		}
	}
	input.BaseCommit = strings.TrimSpace(input.BaseCommit)
	input.TipCommit = strings.TrimSpace(input.TipCommit)
	input.Tree = strings.TrimSpace(input.Tree)
	input.LineReviewDigest = strings.TrimSpace(input.LineReviewDigest)
	if !validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		input.ExpectedRevision < 1 ||
		input.ExpectedRevision > MaxPRDevelopmentControllerRevision ||
		input.LeaseEpoch < 1 ||
		input.LineVersion < 1 ||
		input.LineVersion > MaxPRDevelopmentControllerFences ||
		input.MutationEpoch != input.LineVersion ||
		!validSameWidthPRDevelopmentOIDs(
			input.BaseCommit,
			input.TipCommit,
			input.Tree,
		) ||
		(input.NoChanges != (input.BaseCommit == input.TipCommit)) ||
		!validPRDevelopmentHex(input.LineReviewDigest, sha256.Size*2) {
		return PRDevelopmentAttemptReviewFenceRecord{}, fmt.Errorf(
			"%w: invalid parked review fence",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func normalizePRDevelopmentControllerReviewTransition(
	input PRDevelopmentControllerReviewTransition,
) (PRDevelopmentControllerReviewTransition, error) {
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	token, err := normalizePRDevelopmentControllerIdentity(
		"lease token",
		input.LeaseToken,
		prDevelopmentControllerLeaseTokenBytes,
		true,
	)
	if err != nil {
		return PRDevelopmentControllerReviewTransition{}, err
	}
	input.LeaseToken = token
	if !validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		input.ExpectedRevision < 1 ||
		input.ExpectedRevision > MaxPRDevelopmentControllerRevision ||
		input.LeaseEpoch < 1 {
		return PRDevelopmentControllerReviewTransition{}, fmt.Errorf(
			"%w: valid controller, attempt, revision, token, and epoch are required",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func normalizePRDevelopmentControllerIdentity(
	field, value string,
	maximum int,
	required bool,
) (string, error) {
	value = strings.TrimSpace(value)
	if (!required && value == "") || validPRDevelopmentRepairIdentity(value, maximum) {
		return value, nil
	}
	return "", fmt.Errorf(
		"%w: %s must be bounded exact UTF-8 without control characters",
		ErrInvalidPRDevelopmentController,
		field,
	)
}

func validSameWidthPRDevelopmentOIDs(values ...string) bool {
	if len(values) == 0 || !validPRDevelopmentHex(values[0], 40, 64) {
		return false
	}
	width := len(values[0])
	for _, value := range values[1:] {
		if len(value) != width || !validPRDevelopmentHex(value, width) {
			return false
		}
	}
	return true
}

func prDevelopmentControllerDeadline(
	now time.Time,
	lease time.Duration,
) (time.Time, error) {
	deadline := now.Add(lease)
	if lease <= 0 || !deadline.After(now) ||
		validateDBTimestamp("controller lease deadline", deadline) != nil {
		return time.Time{}, fmt.Errorf(
			"%w: lease deadline is outside the durable timestamp range",
			ErrInvalidPRDevelopmentController,
		)
	}
	return deadline, nil
}

func requireNonRegressingPRDevelopmentControllerTime(
	now, highWater time.Time,
) error {
	if now.Before(highWater) {
		return fmt.Errorf(
			"%w: store clock regressed behind controller high-water time",
			ErrInvalidPRDevelopmentController,
		)
	}
	return nil
}

func maxPRDevelopmentControllerTime(values ...time.Time) time.Time {
	var maximum time.Time
	for _, value := range values {
		if value.After(maximum) {
			maximum = value
		}
	}
	return maximum
}

type prDevelopmentControllerAttemptRelation struct {
	Thread  PRDevelopmentThreadBinding
	Session PRDevelopmentRepairSession
	Attempt PRDevelopmentRepairAttempt
}

func loadPRDevelopmentControllerAttemptRelation(
	ctx context.Context,
	queryer rowsQueryer,
	caseID, attemptID string,
) (prDevelopmentControllerAttemptRelation, error) {
	thread, err := loadPRDevelopmentThreadBindingForCase(ctx, queryer, caseID)
	if err != nil {
		return prDevelopmentControllerAttemptRelation{}, err
	}
	if thread.Kind != PRDevelopmentThreadProvider {
		return prDevelopmentControllerAttemptRelation{}, fmt.Errorf(
			"%w: legacy threads cannot own development controllers",
			ErrPRDevelopmentControllerConflict,
		)
	}
	session, err := loadPRDevelopmentRepairSessionByAttempt(ctx, queryer, attemptID)
	if err != nil {
		return prDevelopmentControllerAttemptRelation{}, err
	}
	if session.CaseID != caseID || len(session.Attempts) == 0 {
		return prDevelopmentControllerAttemptRelation{}, fmt.Errorf(
			"%w: attempt is not owned by the selected case",
			ErrPRDevelopmentControllerConflict,
		)
	}
	attempt := session.Attempts[len(session.Attempts)-1]
	if attempt.ID != attemptID {
		return prDevelopmentControllerAttemptRelation{}, fmt.Errorf(
			"%w: controller operations require the owner's latest attempt",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if attempt.Status == PRDevelopmentRepairPreparing ||
		attempt.Status == PRDevelopmentRepairRunning {
		return prDevelopmentControllerAttemptRelation{}, fmt.Errorf(
			"%w: attempt already has live legacy repair ownership",
			ErrPRDevelopmentControllerActive,
		)
	}
	if attempt.Status != PRDevelopmentRepairQueued &&
		attempt.Status != PRDevelopmentRepairCompleted {
		if attempt.Status == PRDevelopmentRepairRecoveryRequired {
			return prDevelopmentControllerAttemptRelation{},
				ErrPRDevelopmentControllerRecoveryRequired
		}
		return prDevelopmentControllerAttemptRelation{}, fmt.Errorf(
			"%w: only queued or completed attempts may enter controller ownership",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if session.HeadRepository == "" || session.WorkspaceID == "" {
		return prDevelopmentControllerAttemptRelation{}, fmt.Errorf(
			"%w: controller ownership requires a pinned retained-workspace baseline",
			ErrPRDevelopmentControllerConflict,
		)
	}
	return prDevelopmentControllerAttemptRelation{
		Thread:  thread,
		Session: session,
		Attempt: attempt,
	}, nil
}

func requireNoSiblingPRDevelopmentRepairSessions(
	ctx context.Context,
	queryer rowQueryer,
	threadID, ownerSessionID string,
) error {
	var siblings int
	if err := queryer.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM pr_development_repair_sessions AS sessions
		JOIN pr_development_thread_cases AS cases
			ON cases.case_id = sessions.case_id
		WHERE cases.thread_id = ? AND sessions.id <> ?`,
		threadID,
		ownerSessionID,
	).Scan(&siblings); err != nil {
		return err
	}
	if siblings != 0 {
		return fmt.Errorf(
			"%w: provider thread already has a sibling repair session",
			ErrPRDevelopmentControllerConflict,
		)
	}
	return nil
}

func requireCompletedPRDevelopmentControllerAttempt(
	ctx context.Context,
	queryer rowsQueryer,
	controller PRDevelopmentController,
	attemptID string,
) (time.Time, error) {
	session, err := loadPRDevelopmentRepairSessionByAttempt(ctx, queryer, attemptID)
	if err != nil {
		return time.Time{}, err
	}
	if session.ID != controller.OwnerSessionID || len(session.Attempts) == 0 ||
		session.Attempts[len(session.Attempts)-1].ID != attemptID ||
		session.Attempts[len(session.Attempts)-1].Status != PRDevelopmentRepairCompleted {
		return time.Time{}, fmt.Errorf(
			"%w: only the completed latest owner attempt may publish a review fence",
			ErrPRDevelopmentControllerConflict,
		)
	}
	attempt := session.Attempts[len(session.Attempts)-1]
	return maxPRDevelopmentControllerTime(session.UpdatedAt, attempt.UpdatedAt), nil
}

func loadPRDevelopmentControllerAttemptHighWater(
	ctx context.Context,
	queryer rowsQueryer,
	controller PRDevelopmentController,
) (time.Time, error) {
	if controller.CurrentAttemptID == "" {
		return time.Time{}, errors.New("controller has no current attempt high-water")
	}
	session, err := loadPRDevelopmentRepairSessionByAttempt(
		ctx,
		queryer,
		controller.CurrentAttemptID,
	)
	if err != nil {
		return time.Time{}, err
	}
	if session.ID != controller.OwnerSessionID || len(session.Attempts) == 0 {
		return time.Time{}, errors.New("controller current attempt high-water owner is invalid")
	}
	attempt := session.Attempts[len(session.Attempts)-1]
	if attempt.ID != controller.CurrentAttemptID {
		return time.Time{}, errors.New("controller current attempt high-water is not latest")
	}
	return maxPRDevelopmentControllerTime(session.UpdatedAt, attempt.UpdatedAt), nil
}

func requirePRDevelopmentControllerOwnerPin(
	ctx context.Context,
	queryer rowsQueryer,
	controller PRDevelopmentController,
	workspaceID, cloneURL, sourceRef, sourceCommit string,
) error {
	session, err := loadPRDevelopmentRepairSessionByID(
		ctx,
		queryer,
		controller.OwnerSessionID,
	)
	if err != nil {
		return err
	}
	if session.WorkspaceID != workspaceID || session.CloneURL != cloneURL ||
		session.HeadRef != sourceRef || session.HeadSHA != sourceCommit {
		return fmt.Errorf(
			"%w: retained line source differs from the immutable owner pin",
			ErrPRDevelopmentControllerConflict,
		)
	}
	return nil
}

func insertLeasedPRDevelopmentController(
	ctx context.Context,
	conn *sql.Conn,
	relation prDevelopmentControllerAttemptRelation,
	input PRDevelopmentControllerAcquire,
	now, deadline time.Time,
	alreadySuppressed bool,
) (PRDevelopmentController, error) {
	reservation := relation.Session.ReservationKey
	if err := requireFreshPRDevelopmentMutationReservation(
		ctx,
		conn,
		reservation,
	); err != nil {
		return PRDevelopmentController{}, err
	}
	if alreadySuppressed {
		var suppressed int
		if err := conn.QueryRowContext(ctx, `
			SELECT claim_suppressed
			FROM pr_development_repair_sessions
			WHERE id = ?`, relation.Session.ID).Scan(&suppressed); err != nil {
			return PRDevelopmentController{}, err
		}
		if suppressed != 1 {
			return PRDevelopmentController{}, fmt.Errorf(
				"%w: orchestration owner did not suppress the legacy queue",
				ErrPRDevelopmentControllerConflict,
			)
		}
	} else {
		suppressed, err := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_sessions
			SET claim_suppressed = 1
			WHERE id = ? AND claim_suppressed = 0`,
			relation.Session.ID,
		)
		if err != nil {
			return PRDevelopmentController{}, err
		}
		if rowErr := requireOnePRDevelopmentControllerRow(suppressed); rowErr != nil {
			return PRDevelopmentController{}, fmt.Errorf(
				"%w: owner session cannot transfer legacy queue ownership: %v",
				ErrPRDevelopmentControllerConflict,
				rowErr,
			)
		}
	}
	controllerID, err := newPrefixedID(prDevelopmentControllerIDPrefix)
	if err != nil {
		return PRDevelopmentController{}, err
	}
	lineID, err := newPrefixedID(prDevelopmentLineIDPrefix)
	if err != nil {
		return PRDevelopmentController{}, err
	}
	token, err := newLeaseToken(input.WorkerLabel)
	if err != nil {
		return PRDevelopmentController{}, err
	}
	_, err = conn.ExecContext(ctx, `
		INSERT INTO pr_development_thread_controllers (
			id, thread_id, owner_session_id, agent_id, revision, phase, line_id,
			workspace_id, source_clone_url, source_ref, source_commit, source_tree,
			line_version, mutation_epoch, tip_commit, tree, current_attempt_id,
			lease_kind, lease_owner, lease_token, lease_until, lease_epoch, claims,
			mutation_reservation_key, fence_count, fences_digest, created_at, updated_at
		) VALUES (
			?, ?, ?, ?, 1, 'mutation', ?, '', '', '', '', '', 0, 0, '', '', ?,
			'mutation', ?, ?, ?, 1, 1, ?, 0, ?, ?, ?
		)`,
		controllerID,
		relation.Thread.ID,
		relation.Session.ID,
		relation.Session.AgentID,
		lineID,
		relation.Attempt.ID,
		input.WorkerLabel,
		token,
		toDBTime(deadline),
		reservation,
		emptyPRDevelopmentReviewFencesDigest(),
		toDBTime(now),
		toDBTime(now),
	)
	if err != nil {
		return PRDevelopmentController{}, err
	}
	controller, found, err := loadPRDevelopmentControllerAggregateByID(
		ctx,
		conn,
		controllerID,
	)
	if err != nil {
		return PRDevelopmentController{}, err
	}
	if !found {
		return PRDevelopmentController{}, errors.New(
			"created pull request development controller disappeared",
		)
	}
	return controller, nil
}

func acquirePRDevelopmentMutationLease(
	ctx context.Context,
	conn *sql.Conn,
	controller PRDevelopmentController,
	input PRDevelopmentControllerAcquire,
	now, deadline time.Time,
) (PRDevelopmentController, error) {
	if controller.Phase == PRDevelopmentControllerMutation {
		if controller.LeaseUntil != nil && !controller.LeaseUntil.After(now) {
			if err := expirePRDevelopmentMutationLease(
				ctx,
				conn,
				controller,
				now,
			); err != nil {
				return PRDevelopmentController{}, err
			}
			return PRDevelopmentController{}, ErrPRDevelopmentControllerRecoveryRequired
		}
		return PRDevelopmentController{}, ErrPRDevelopmentControllerActive
	}
	if controller.Phase == PRDevelopmentControllerRecoveryRequired {
		return PRDevelopmentController{}, ErrPRDevelopmentControllerRecoveryRequired
	}
	if controller.Phase != PRDevelopmentControllerIdle &&
		controller.Phase != PRDevelopmentControllerReady {
		return PRDevelopmentController{}, ErrPRDevelopmentControllerActive
	}
	if controller.LineVersion >= MaxPRDevelopmentControllerFences ||
		controller.Revision > MaxPRDevelopmentControllerRevision-
			prDevelopmentControllerMutationRevisionReserve ||
		controller.LeaseEpoch == int64(^uint64(0)>>1) {
		return PRDevelopmentController{}, fmt.Errorf(
			"%w: controller mutation capacity exhausted",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if controller.CurrentAttemptID == input.AttemptID {
		return PRDevelopmentController{}, fmt.Errorf(
			"%w: attempt already owns the controller high-water state",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if _, found, err := loadPRDevelopmentReviewFenceByAttempt(
		ctx,
		conn,
		input.AttemptID,
	); err != nil {
		return PRDevelopmentController{}, err
	} else if found {
		return PRDevelopmentController{}, fmt.Errorf(
			"%w: attempt already has a review fence",
			ErrPRDevelopmentControllerConflict,
		)
	}
	token, err := newLeaseToken(input.WorkerLabel)
	if err != nil {
		return PRDevelopmentController{}, err
	}
	reservation, err := newUniquePRDevelopmentMutationReservation(ctx, conn)
	if err != nil {
		return PRDevelopmentController{}, err
	}
	mutationEpoch := controller.MutationEpoch
	result, err := conn.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET revision = revision + 1, phase = 'mutation', current_attempt_id = ?,
			mutation_epoch = ?, lease_kind = 'mutation', lease_owner = ?,
			lease_token = ?, lease_until = ?, lease_epoch = lease_epoch + 1,
			claims = claims + 1, mutation_reservation_key = ?, updated_at = ?
		WHERE id = ? AND revision = ? AND phase = ? AND lease_kind = '' AND
			mutation_reservation_key = ''`,
		input.AttemptID,
		mutationEpoch,
		input.WorkerLabel,
		token,
		toDBTime(deadline),
		reservation,
		toDBTime(now),
		controller.ID,
		controller.Revision,
		controller.Phase,
	)
	if err != nil {
		return PRDevelopmentController{}, err
	}
	if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
		return PRDevelopmentController{}, rowErr
	}
	updated, found, err := loadPRDevelopmentControllerAggregateByID(
		ctx,
		conn,
		controller.ID,
	)
	if err != nil {
		return PRDevelopmentController{}, err
	}
	if !found {
		return PRDevelopmentController{}, errors.New(
			"leased pull request development controller disappeared",
		)
	}
	return updated, nil
}

func acquirePRDevelopmentReviewLease(
	ctx context.Context,
	conn *sql.Conn,
	controller PRDevelopmentController,
	input PRDevelopmentControllerAcquire,
	now, deadline time.Time,
) (PRDevelopmentController, PRDevelopmentAttemptReviewFence, bool, error) {
	if controller.Phase == PRDevelopmentControllerRecoveryRequired {
		return PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{}, false,
			ErrPRDevelopmentControllerRecoveryRequired
	}
	if controller.Phase == PRDevelopmentControllerMutation {
		return PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{}, false,
			ErrPRDevelopmentControllerActive
	}
	if controller.Phase != PRDevelopmentControllerReviewPending &&
		controller.Phase != PRDevelopmentControllerReview {
		return PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{}, false,
			fmt.Errorf(
				"%w: controller has no pending review",
				ErrPRDevelopmentControllerConflict,
			)
	}
	if controller.CurrentAttemptID != input.AttemptID {
		return PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{}, false,
			fmt.Errorf(
				"%w: attempt is not the pending review owner",
				ErrPRDevelopmentControllerConflict,
			)
	}
	reclaimed := false
	if controller.Phase == PRDevelopmentControllerReview {
		if controller.LeaseUntil != nil && controller.LeaseUntil.After(now) {
			return PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{}, false,
				ErrPRDevelopmentControllerActive
		}
		reclaimed = true
	}
	if controller.LeaseEpoch == int64(^uint64(0)>>1) {
		return PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{}, false,
			fmt.Errorf(
				"%w: controller lease epoch capacity exhausted",
				ErrPRDevelopmentControllerConflict,
			)
	}
	token, err := newLeaseToken(input.WorkerLabel)
	if err != nil {
		return PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{}, false, err
	}
	result, err := conn.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET phase = 'review', lease_kind = 'review', lease_owner = ?,
			lease_token = ?, lease_until = ?, lease_epoch = lease_epoch + 1,
			claims = claims + 1, updated_at = ?
		WHERE id = ? AND revision = ? AND current_attempt_id = ? AND
			mutation_reservation_key = '' AND
			(phase = 'review_pending' OR (phase = 'review' AND lease_until <= ?))`,
		input.WorkerLabel,
		token,
		toDBTime(deadline),
		toDBTime(now),
		controller.ID,
		controller.Revision,
		controller.CurrentAttemptID,
		toDBTime(now),
	)
	if err != nil {
		return PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{}, false, err
	}
	if rowErr := requireOnePRDevelopmentControllerRow(result); rowErr != nil {
		return PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{}, false, rowErr
	}
	updated, found, err := loadPRDevelopmentControllerAggregateByID(
		ctx,
		conn,
		controller.ID,
	)
	if err != nil {
		return PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{}, false, err
	}
	if !found {
		return PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{}, false,
			errors.New("review-leased pull request development controller disappeared")
	}
	fence, found, err := loadLatestPRDevelopmentReviewFence(ctx, conn, controller.ID)
	if err != nil {
		return PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{}, false, err
	}
	if !found || fence.AttemptID != input.AttemptID || fence.ReviewedAt != nil {
		return PRDevelopmentController{}, PRDevelopmentAttemptReviewFence{}, false,
			errors.New("pending pull request development review fence is invalid")
	}
	return updated, fence, reclaimed, nil
}

func expirePRDevelopmentMutationLease(
	ctx context.Context,
	conn *sql.Conn,
	controller PRDevelopmentController,
	now time.Time,
) error {
	operation, hasOperation, err := loadActivePRDevelopmentControllerOperation(
		ctx,
		conn,
		controller.ID,
	)
	if err != nil {
		return err
	}
	requiredHeadroom := int64(4) // expire, finalize, Park, review finish
	if hasOperation {
		switch operation.Kind {
		case PRDevelopmentControllerOperationAdopt,
			PRDevelopmentControllerOperationResume:
			requiredHeadroom = 5 // expire, recover, bind, Park, review finish
		case PRDevelopmentControllerOperationPark:
			requiredHeadroom = 2 // expiry/recovered Park, review finish
		case PRDevelopmentControllerOperationCommit:
			// The default exactly covers expire, recover, Park, and review finish.
		default:
			return errors.New("active controller operation has an unknown kind")
		}
	} else if controller.WorkspaceID == "" {
		requiredHeadroom++ // first retained-line bind
	}
	if controller.Revision > MaxPRDevelopmentControllerRevision-requiredHeadroom {
		return fmt.Errorf(
			"%w: expired mutation has no recovery/finalization revision headroom",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if hasOperation {
		if stageErr := stagePRDevelopmentControllerOperationRecoveryForExpiry(
			ctx,
			conn,
			controller,
			operation,
			now,
		); stageErr != nil {
			return stageErr
		}
	} else {
		if _, stageErr := insertPRDevelopmentRecoveryIntentForExpiry(
			ctx,
			conn,
			controller,
			now,
		); stageErr != nil {
			return stageErr
		}
	}
	result, err := conn.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET revision = revision + 1, phase = 'recovery_required',
			lease_kind = '', lease_owner = '', lease_token = '', lease_until = NULL,
			updated_at = ?
		WHERE id = ? AND revision = ? AND phase = 'mutation' AND
			lease_until <= ? AND mutation_reservation_key = ?`,
		toDBTime(now),
		controller.ID,
		controller.Revision,
		toDBTime(now),
		controller.MutationReservationKey,
	)
	if err != nil {
		return err
	}
	return requireOnePRDevelopmentControllerRow(result)
}

func newUniquePRDevelopmentMutationReservation(
	ctx context.Context,
	queryer rowsQueryer,
) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		reservation, err := newPrefixedID(prDevelopmentControllerKeyPrefix)
		if err != nil {
			return "", err
		}
		digest := prDevelopmentMutationReservationDigest(reservation)
		var collisions int
		if queryErr := queryer.QueryRowContext(ctx, `
			SELECT
				(SELECT COUNT(*) FROM pr_development_thread_controllers
				 WHERE mutation_reservation_key = ?) +
				(SELECT COUNT(*) FROM pr_development_attempt_review_fences
				 WHERE mutation_reservation_digest = ?) +
				(SELECT COUNT(*) FROM pr_development_controller_recovery_intents
				 WHERE previous_reservation_digest = ? OR replacement_reservation_digest = ?) +
				(SELECT COUNT(*) FROM pr_development_controller_operation_intents
				 WHERE mutation_reservation_digest = ? OR replacement_reservation_digest = ?) +
				(SELECT COUNT(*) FROM pr_development_controller_suspensions
				 WHERE suspension_reservation_digest = ? OR resume_reservation_digest = ?)`,
			reservation,
			digest,
			digest,
			digest,
			digest,
			digest,
			digest,
			digest,
		).Scan(&collisions); queryErr != nil {
			return "", queryErr
		}
		if collisions == 0 {
			return reservation, nil
		}
	}
	return "", errors.New("generate unique pull request development mutation reservation")
}

func requireFreshPRDevelopmentMutationReservation(
	ctx context.Context,
	queryer rowsQueryer,
	reservation string,
) error {
	owners, err := countPRDevelopmentRepairReservationOwners(ctx, queryer, reservation)
	if err != nil {
		return err
	}
	if owners != 1 {
		return fmt.Errorf(
			"%w: mutation reservation does not have one repair-session owner",
			ErrPRDevelopmentControllerConflict,
		)
	}
	digest := prDevelopmentMutationReservationDigest(reservation)
	var collisions int
	if err := queryer.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM pr_development_thread_controllers
			 WHERE mutation_reservation_key = ?) +
			(SELECT COUNT(*) FROM pr_development_attempt_review_fences
			 WHERE mutation_reservation_digest = ?) +
			(SELECT COUNT(*) FROM pr_development_controller_recovery_intents
			 WHERE previous_reservation_digest = ? OR replacement_reservation_digest = ?) +
			(SELECT COUNT(*) FROM pr_development_controller_operation_intents
			 WHERE mutation_reservation_digest = ? OR replacement_reservation_digest = ?) +
			(SELECT COUNT(*) FROM pr_development_controller_suspensions
			 WHERE suspension_reservation_digest = ? OR resume_reservation_digest = ?)`,
		reservation,
		digest,
		digest,
		digest,
		digest,
		digest,
		digest,
		digest,
	).Scan(&collisions); err != nil {
		return err
	}
	if collisions != 0 {
		return fmt.Errorf(
			"%w: mutation reservation was already active or retired",
			ErrPRDevelopmentControllerConflict,
		)
	}
	return nil
}

func requireOnePRDevelopmentControllerRow(result sql.Result) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf(
			"%w: controller transition lost its optimistic fence",
			ErrPRDevelopmentControllerConflict,
		)
	}
	return nil
}

func loadPRDevelopmentControllerAggregate(
	ctx context.Context,
	queryer rowsQueryer,
	threadID string,
) (PRDevelopmentController, bool, error) {
	controller, err := scanPRDevelopmentController(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentControllerColumns+`
		FROM pr_development_thread_controllers
		WHERE thread_id = ?`,
		threadID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentController{}, false, nil
	}
	if err != nil {
		return PRDevelopmentController{}, false, err
	}
	if err := validatePRDevelopmentControllerAggregate(ctx, queryer, controller); err != nil {
		return PRDevelopmentController{}, false, wrapInvalidStoredPRDevelopmentController(err)
	}
	return controller, true, nil
}

func loadPRDevelopmentControllerAggregateByID(
	ctx context.Context,
	queryer rowsQueryer,
	controllerID string,
) (PRDevelopmentController, bool, error) {
	controller, err := scanPRDevelopmentController(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentControllerColumns+`
		FROM pr_development_thread_controllers
		WHERE id = ?`,
		controllerID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentController{}, false, nil
	}
	if err != nil {
		return PRDevelopmentController{}, false, err
	}
	if err := validatePRDevelopmentControllerAggregate(ctx, queryer, controller); err != nil {
		return PRDevelopmentController{}, false, wrapInvalidStoredPRDevelopmentController(err)
	}
	return controller, true, nil
}

func scanPRDevelopmentController(
	scanner rowScanner,
) (PRDevelopmentController, error) {
	var (
		controller           PRDevelopmentController
		currentAttempt       sql.NullString
		leaseUntil           sql.NullInt64
		claims, fenceCount   int64
		createdAt, updatedAt int64
	)
	if err := scanner.Scan(
		&controller.ID,
		&controller.ThreadID,
		&controller.OwnerSessionID,
		&controller.AgentID,
		&controller.Revision,
		&controller.Phase,
		&controller.LineID,
		&controller.WorkspaceID,
		&controller.SourceCloneURL,
		&controller.SourceRef,
		&controller.SourceCommit,
		&controller.SourceTree,
		&controller.LineVersion,
		&controller.MutationEpoch,
		&controller.TipCommit,
		&controller.Tree,
		&currentAttempt,
		&controller.LeaseKind,
		&controller.LeaseOwner,
		&controller.LeaseToken,
		&leaseUntil,
		&controller.LeaseEpoch,
		&claims,
		&controller.MutationReservationKey,
		&fenceCount,
		&controller.FencesDigest,
		&createdAt,
		&updatedAt,
	); err != nil {
		return PRDevelopmentController{}, err
	}
	controller.Claims = int(claims)
	controller.FenceCount = int(fenceCount)
	if int64(controller.Claims) != claims || int64(controller.FenceCount) != fenceCount {
		return PRDevelopmentController{}, wrapInvalidStoredPRDevelopmentController(
			errors.New("stored controller integer overflows"),
		)
	}
	if currentAttempt.Valid {
		controller.CurrentAttemptID = currentAttempt.String
	}
	controller.LeaseUntil = fromNullableTime(leaseUntil)
	controller.CreatedAt = fromDBTime(createdAt)
	controller.UpdatedAt = fromDBTime(updatedAt)
	if err := validateStoredPRDevelopmentController(controller); err != nil {
		return PRDevelopmentController{}, wrapInvalidStoredPRDevelopmentController(err)
	}
	return controller, nil
}

func validateStoredPRDevelopmentController(
	controller PRDevelopmentController,
) error {
	if !validPrefixedHexID(controller.ID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(controller.ThreadID, prDevelopmentThreadIDPrefix) ||
		!validPrefixedHexID(controller.OwnerSessionID, prDevelopmentRepairSessionIDPrefix) ||
		!validPRDevelopmentRepairAgentID(controller.AgentID) ||
		controller.Revision < 1 ||
		controller.Revision > MaxPRDevelopmentControllerRevision ||
		!validPrefixedHexID(controller.LineID, prDevelopmentLineIDPrefix) ||
		controller.LineVersion < 0 ||
		controller.LineVersion > MaxPRDevelopmentControllerFences ||
		controller.MutationEpoch < 0 ||
		controller.MutationEpoch > MaxPRDevelopmentControllerFences+1 ||
		controller.LeaseEpoch < 0 || controller.Claims < 0 ||
		controller.FenceCount < 0 ||
		controller.FenceCount > MaxPRDevelopmentControllerFences ||
		!validPRDevelopmentHex(controller.FencesDigest, sha256.Size*2) ||
		validateDBTimestamp("controller creation time", controller.CreatedAt) != nil ||
		validateDBTimestamp("controller update time", controller.UpdatedAt) != nil ||
		controller.UpdatedAt.Before(controller.CreatedAt) {
		return errors.New("stored controller header is invalid")
	}
	bound := controller.WorkspaceID != ""
	if bound != (controller.SourceCloneURL != "") ||
		bound != (controller.SourceRef != "") ||
		bound != (controller.SourceCommit != "") ||
		bound != (controller.SourceTree != "") ||
		bound != (controller.TipCommit != "") ||
		bound != (controller.Tree != "") {
		return errors.New("stored controller line binding is partial")
	}
	if !bound {
		if controller.LineVersion != 0 || controller.MutationEpoch != 0 ||
			controller.FenceCount != 0 ||
			controller.FencesDigest != emptyPRDevelopmentReviewFencesDigest() {
			return errors.New("stored unbound controller has line high-water state")
		}
	} else if !validPRDevelopmentRepairIdentity(
		controller.WorkspaceID,
		MaxPRDevelopmentControllerIdentityBytes,
	) || !validPRDevelopmentRepairCloneURL(controller.SourceCloneURL) ||
		!validPRDevelopmentGitRef(controller.SourceRef) ||
		!validSameWidthPRDevelopmentOIDs(
			controller.SourceCommit,
			controller.SourceTree,
			controller.TipCommit,
			controller.Tree,
		) || controller.FenceCount != int(controller.LineVersion) ||
		controller.MutationEpoch < controller.LineVersion ||
		controller.MutationEpoch > controller.LineVersion+1 {
		return errors.New("stored controller line binding is invalid")
	}
	if controller.Claims != int(controller.LeaseEpoch) {
		return errors.New("stored controller lease claim high-water state is invalid")
	}
	if controller.Phase != PRDevelopmentControllerIdle && controller.Claims < 1 {
		return errors.New("stored non-idle controller has no lease claim history")
	}
	leased := controller.Phase == PRDevelopmentControllerMutation ||
		controller.Phase == PRDevelopmentControllerReview
	if leased != (controller.LeaseKind != "") ||
		leased != (controller.LeaseOwner != "") ||
		leased != (controller.LeaseToken != "") ||
		leased != (controller.LeaseUntil != nil) {
		return errors.New("stored controller lease is partial")
	}
	if leased && (!validPRDevelopmentRepairIdentity(
		controller.LeaseOwner,
		MaxPRDevelopmentControllerIdentityBytes,
	) || !validPRDevelopmentRepairIdentity(
		controller.LeaseToken,
		prDevelopmentControllerLeaseTokenBytes,
	) || validateDBTimestamp("controller lease deadline", *controller.LeaseUntil) != nil) {
		return errors.New("stored controller lease is invalid")
	}
	if controller.CurrentAttemptID != "" &&
		!validPrefixedHexID(controller.CurrentAttemptID, prDevelopmentRepairAttemptIDPrefix) {
		return errors.New("stored controller current attempt is invalid")
	}
	hasReservation := controller.MutationReservationKey != ""
	if hasReservation &&
		!validPrefixedHexID(controller.MutationReservationKey, prDevelopmentControllerKeyPrefix) &&
		!validPrefixedHexID(controller.MutationReservationKey, prDevelopmentRepairReservationPrefix) {
		return errors.New("stored controller mutation reservation is invalid")
	}
	switch controller.Phase {
	case PRDevelopmentControllerIdle:
		if bound || controller.CurrentAttemptID != "" || leased || hasReservation ||
			controller.Claims != 0 || controller.LeaseEpoch != 0 {
			return errors.New("stored idle controller is invalid")
		}
	case PRDevelopmentControllerMutation:
		if controller.CurrentAttemptID == "" ||
			controller.LeaseKind != PRDevelopmentControllerMutationLease ||
			!hasReservation || controller.LineVersion >= MaxPRDevelopmentControllerFences {
			return errors.New("stored mutation controller is invalid")
		}
		if bound && controller.MutationEpoch != controller.LineVersion &&
			controller.MutationEpoch != controller.LineVersion+1 {
			return errors.New("stored mutation controller epoch is invalid")
		}
	case PRDevelopmentControllerReviewPending:
		if !bound || controller.CurrentAttemptID == "" || leased || hasReservation ||
			controller.FenceCount == 0 ||
			controller.MutationEpoch != controller.LineVersion {
			return errors.New("stored review-pending controller is invalid")
		}
	case PRDevelopmentControllerReview:
		if !bound || controller.CurrentAttemptID == "" ||
			controller.LeaseKind != PRDevelopmentControllerReviewLease || hasReservation ||
			controller.FenceCount == 0 ||
			controller.MutationEpoch != controller.LineVersion {
			return errors.New("stored review controller is invalid")
		}
	case PRDevelopmentControllerReady:
		if !bound || controller.CurrentAttemptID == "" || leased || hasReservation ||
			controller.FenceCount == 0 ||
			controller.MutationEpoch != controller.LineVersion {
			return errors.New("stored ready controller is invalid")
		}
	case PRDevelopmentControllerRecoveryRequired:
		if controller.CurrentAttemptID == "" || leased || !hasReservation {
			return errors.New("stored recovery-required controller is invalid")
		}
	case PRDevelopmentControllerSuspensionPending,
		PRDevelopmentControllerSuspended:
		if !bound || controller.CurrentAttemptID == "" || leased || hasReservation {
			return errors.New("stored suspended controller is invalid")
		}
	default:
		return errors.New("stored controller phase is invalid")
	}
	return nil
}

func validatePRDevelopmentControllerAggregate(
	ctx context.Context,
	queryer rowsQueryer,
	controller PRDevelopmentController,
) error {
	session, err := loadPRDevelopmentRepairSessionByID(
		ctx,
		queryer,
		controller.OwnerSessionID,
	)
	if err != nil {
		return err
	}
	if session.AgentID != controller.AgentID {
		return errors.New("stored controller agent differs from owner session")
	}
	reservationOwners, err := countPRDevelopmentRepairReservationOwners(
		ctx,
		queryer,
		session.ReservationKey,
	)
	if err != nil {
		return err
	}
	if reservationOwners != 1 {
		return errors.New("stored controller owner reservation is not unique")
	}
	if controller.MutationReservationKey != "" {
		if controller.FenceCount == 0 {
			if controller.LineVersion != 0 ||
				(controller.Phase != PRDevelopmentControllerMutation &&
					controller.Phase != PRDevelopmentControllerRecoveryRequired) {
				return errors.New("stored initial controller reservation is invalid")
			}
			if controller.MutationReservationKey != session.ReservationKey {
				if !validPrefixedHexID(
					controller.MutationReservationKey,
					prDevelopmentControllerKeyPrefix,
				) {
					return errors.New("stored initial controller reservation is invalid")
				}
				var recoveryMatches int
				var resumedMatches int
				digest := prDevelopmentMutationReservationDigest(
					controller.MutationReservationKey,
				)
				if queryErr := queryer.QueryRowContext(ctx, `
					SELECT COUNT(*)
					FROM pr_development_controller_recovery_intents
					WHERE controller_id = ? AND
						((status <> 'finalized' AND previous_reservation_key = ?) OR
						 (status = 'finalized' AND replacement_reservation_digest = ?))`,
					controller.ID,
					controller.MutationReservationKey,
					digest,
				).Scan(&recoveryMatches); queryErr != nil {
					return queryErr
				}
				if queryErr := queryer.QueryRowContext(ctx, `
					SELECT COUNT(*)
					FROM pr_development_controller_suspensions
					WHERE controller_id = ? AND status = 'resumed' AND
						resume_reservation_digest = ? AND
						final_resume_revision = ?`,
					controller.ID,
					digest,
					controller.Revision,
				).Scan(&resumedMatches); queryErr != nil {
					return queryErr
				}
				validRecovery := recoveryMatches >= 1 && recoveryMatches <= 2
				validResume := resumedMatches == 1
				if validRecovery == validResume {
					return errors.New("stored initial controller reservation has no recovery intent")
				}
			}
		} else if !validPrefixedHexID(
			controller.MutationReservationKey,
			prDevelopmentControllerKeyPrefix,
		) {
			return errors.New("stored resumed controller reservation is invalid")
		}
		var retired int
		if queryErr := queryer.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM pr_development_attempt_review_fences
			WHERE mutation_reservation_digest = ?`,
			prDevelopmentMutationReservationDigest(
				controller.MutationReservationKey,
			),
		).Scan(&retired); queryErr != nil {
			return queryErr
		}
		if retired != 0 {
			return errors.New("stored active controller reservation was already retired")
		}
	}
	if session.HeadRepository == "" || session.WorkspaceID == "" {
		return errors.New("stored controller owner has no pinned retained-workspace baseline")
	}
	if controller.WorkspaceID != "" &&
		(controller.WorkspaceID != session.WorkspaceID ||
			controller.SourceCloneURL != session.CloneURL ||
			controller.SourceRef != session.HeadRef ||
			controller.SourceCommit != session.HeadSHA) {
		return errors.New("stored controller source differs from its immutable owner pin")
	}
	var claimSuppressed int
	if queryErr := queryer.QueryRowContext(ctx, `
		SELECT claim_suppressed
		FROM pr_development_repair_sessions
		WHERE id = ?`,
		controller.OwnerSessionID,
	).Scan(&claimSuppressed); queryErr != nil {
		return queryErr
	}
	if claimSuppressed != 1 {
		return errors.New("stored controller owner remains in the legacy repair queue")
	}
	binding, err := loadPRDevelopmentThreadBindingForCase(ctx, queryer, session.CaseID)
	if err != nil {
		return err
	}
	if binding.Kind != PRDevelopmentThreadProvider || binding.ID != controller.ThreadID {
		return errors.New("stored controller owner is not in its provider thread")
	}
	if siblingErr := requireNoSiblingPRDevelopmentRepairSessions(
		ctx,
		queryer,
		controller.ThreadID,
		controller.OwnerSessionID,
	); siblingErr != nil {
		return siblingErr
	}
	attemptOrdinals := make(map[string]int, len(session.Attempts))
	attemptStatuses := make(map[string]PRDevelopmentRepairStatus, len(session.Attempts))
	attemptCreatedAt := make(map[string]time.Time, len(session.Attempts))
	attemptUpdatedAt := make(map[string]time.Time, len(session.Attempts))
	for _, attempt := range session.Attempts {
		attemptOrdinals[attempt.ID] = attempt.Ordinal
		attemptStatuses[attempt.ID] = attempt.Status
		attemptCreatedAt[attempt.ID] = attempt.CreatedAt
		attemptUpdatedAt[attempt.ID] = attempt.UpdatedAt
	}
	if controller.CurrentAttemptID != "" {
		if _, exists := attemptOrdinals[controller.CurrentAttemptID]; !exists {
			return errors.New("stored controller current attempt belongs to another session")
		}
		if controller.Phase != PRDevelopmentControllerReady &&
			controller.Phase != PRDevelopmentControllerSuspended &&
			controller.CurrentAttemptID != session.Attempts[len(session.Attempts)-1].ID {
			return errors.New("stored active controller attempt is not owner-session latest")
		}
		if controller.UpdatedAt.Before(attemptCreatedAt[controller.CurrentAttemptID]) {
			return errors.New("stored controller predates its current attempt")
		}
	}
	fences, err := loadPRDevelopmentReviewFences(ctx, queryer, controller.ID)
	if err != nil {
		return err
	}
	if len(fences) != controller.FenceCount {
		return errors.New("stored controller fence count differs from its rows")
	}
	if operationErr := validatePRDevelopmentControllerOperationChain(
		ctx,
		queryer,
		controller,
		session,
		fences,
		attemptOrdinals,
		attemptCreatedAt,
	); operationErr != nil {
		return operationErr
	}
	recoveryStats, err := validatePRDevelopmentControllerRecoveryChain(
		ctx,
		queryer,
		controller,
		session,
		fences,
		attemptOrdinals,
	)
	if err != nil {
		return err
	}
	suspensionStats, err := validatePRDevelopmentControllerSuspensionChain(
		ctx,
		queryer,
		controller,
		session,
		attemptOrdinals,
	)
	if err != nil {
		return err
	}
	previousHash := emptyPRDevelopmentReviewFencesDigest()
	previousTip := controller.SourceCommit
	previousTree := controller.SourceTree
	previousAttemptOrdinal := -1
	previousControllerRevision := int64(0)
	previousLeaseEpoch := int64(0)
	var (
		previousCreatedAt  time.Time
		previousReviewedAt time.Time
	)
	for ordinal, fence := range fences {
		attemptOrdinal, owned := attemptOrdinals[fence.AttemptID]
		if !owned || attemptStatuses[fence.AttemptID] != PRDevelopmentRepairCompleted ||
			attemptOrdinal <= previousAttemptOrdinal {
			return errors.New("stored controller fence attempt ownership/order is invalid")
		}
		if fence.ControllerID != controller.ID ||
			fence.ThreadID != controller.ThreadID || fence.LineID != controller.LineID ||
			fence.Ordinal != ordinal || fence.LineVersion != int64(ordinal+1) ||
			fence.MutationEpoch != fence.LineVersion ||
			fence.BaseCommit != previousTip || fence.PreviousHash != previousHash ||
			(fence.NoChanges && fence.Tree != previousTree) ||
			fence.FenceHash != hashPRDevelopmentReviewFence(fence) ||
			fence.CreatedAt.Before(attemptUpdatedAt[fence.AttemptID]) ||
			fence.CreatedAt.Before(controller.CreatedAt) ||
			fence.CreatedAt.After(controller.UpdatedAt) ||
			(fence.ReviewedAt != nil && fence.ReviewedAt.After(controller.UpdatedAt)) ||
			fence.MutationControllerRevision <= previousControllerRevision ||
			fence.MutationControllerRevision > MaxPRDevelopmentControllerRevision-2 ||
			fence.MutationLeaseEpoch <= previousLeaseEpoch ||
			(!previousCreatedAt.IsZero() && fence.CreatedAt.Before(previousCreatedAt)) ||
			(!previousReviewedAt.IsZero() && fence.CreatedAt.Before(previousReviewedAt)) {
			return errors.New("stored controller fence chain is invalid")
		}
		attemptRecoveries := recoveryStats.finalizedByAttempt[fence.AttemptID]
		expectedMutationRevision := int64(2 + 2*attemptRecoveries)
		expectedMutationLeaseEpoch := int64(1 + attemptRecoveries)
		if ordinal > 0 {
			expectedMutationRevision = previousControllerRevision + 3 +
				int64(2*attemptRecoveries)
			expectedMutationLeaseEpoch = previousLeaseEpoch + 1 +
				int64(attemptRecoveries)
		}
		if fence.MutationControllerRevision != expectedMutationRevision ||
			fence.MutationLeaseEpoch != expectedMutationLeaseEpoch {
			return errors.New("stored controller fence mutation proof is unreachable")
		}
		if ordinal == 0 && fence.MutationReservationDigest !=
			prDevelopmentMutationReservationDigest(session.ReservationKey) {
			var recoveryMatches int
			if queryErr := queryer.QueryRowContext(ctx, `
				SELECT COUNT(*)
				FROM pr_development_controller_recovery_intents
				WHERE controller_id = ? AND attempt_id = ? AND status = 'finalized' AND
					replacement_reservation_digest = ?`,
				controller.ID,
				fence.AttemptID,
				fence.MutationReservationDigest,
			).Scan(&recoveryMatches); queryErr != nil {
				return queryErr
			}
			if recoveryMatches != 1 {
				return errors.New(
					"stored first controller fence did not retire the owner reservation or its recovered successor",
				)
			}
		}
		if ordinal < len(fences)-1 && fence.ReviewedAt == nil {
			return errors.New("stored non-tail controller fence is not reviewed")
		}
		previousHash = fence.FenceHash
		previousTip = fence.TipCommit
		previousTree = fence.Tree
		previousAttemptOrdinal = attemptOrdinal
		previousControllerRevision = fence.MutationControllerRevision
		previousLeaseEpoch = fence.MutationLeaseEpoch
		if fence.ReviewedAt != nil {
			if fence.ReviewControllerRevision-fence.MutationControllerRevision >
				fence.ReviewLeaseEpoch-fence.MutationLeaseEpoch {
				return errors.New("stored controller fence review proof is unreachable")
			}
			previousControllerRevision = fence.ReviewControllerRevision
			if fence.ReviewLeaseEpoch <= previousLeaseEpoch {
				return errors.New("stored controller review lease epoch is not monotonic")
			}
			previousLeaseEpoch = fence.ReviewLeaseEpoch
			previousReviewedAt = *fence.ReviewedAt
		}
		previousCreatedAt = fence.CreatedAt
	}
	if suspensionStats.active != nil {
		if err := validatePRDevelopmentControllerSuspensionFenceHighWater(
			controller,
			fences,
			previousControllerRevision,
			previousLeaseEpoch,
		); err != nil {
			return err
		}
		return nil
	}
	var latestFence *PRDevelopmentAttemptReviewFence
	if len(fences) != 0 {
		copy := fences[len(fences)-1]
		latestFence = &copy
	}
	if resumed, resumeErr := validatePRDevelopmentControllerResumedAuthority(
		controller,
		latestFence,
		suspensionStats.latestResumed,
	); resumeErr != nil {
		return resumeErr
	} else if resumed {
		if err := validatePRDevelopmentControllerSuspensionFenceHighWater(
			controller,
			fences,
			previousControllerRevision,
			previousLeaseEpoch,
		); err != nil {
			return err
		}
		return nil
	}
	if len(fences) == 0 {
		if controller.FencesDigest != emptyPRDevelopmentReviewFencesDigest() {
			return errors.New("stored empty controller fence digest is invalid")
		}
		if controller.WorkspaceID != "" && controller.LineVersion != 0 {
			return errors.New("stored controller without fences has a nonzero version")
		}
		bound := controller.WorkspaceID != ""
		finalizedRecoveries := recoveryStats.finalizedByAttempt[controller.CurrentAttemptID]
		expectedLeaseEpoch := int64(1 + finalizedRecoveries)
		expectedMutationRevision := int64(1 + 2*finalizedRecoveries)
		if bound {
			expectedMutationRevision++
		}
		switch controller.Phase {
		case PRDevelopmentControllerMutation:
			if controller.LeaseEpoch != expectedLeaseEpoch ||
				controller.Revision != expectedMutationRevision ||
				(!bound && controller.MutationEpoch != 0) ||
				(bound && controller.MutationEpoch != 1) {
				return errors.New("stored initial mutation controller high-water state is invalid")
			}
		case PRDevelopmentControllerRecoveryRequired:
			if controller.LeaseEpoch != expectedLeaseEpoch ||
				controller.Revision != expectedMutationRevision+1 ||
				(!bound && controller.MutationEpoch != 0) ||
				(bound && controller.MutationEpoch != 1) {
				return errors.New("stored initial recovery controller high-water state is invalid")
			}
		default:
			return errors.New("stored controller phase is unreachable without fences")
		}
		return nil
	}
	latest := fences[len(fences)-1]
	if controller.FencesDigest != latest.FenceHash ||
		controller.LineVersion != latest.LineVersion ||
		controller.TipCommit != latest.TipCommit || controller.Tree != latest.Tree ||
		controller.Revision <= previousControllerRevision ||
		controller.LeaseEpoch < previousLeaseEpoch {
		return errors.New("stored controller fence high-water state is invalid")
	}
	if latest.ReviewedAt == nil {
		revisionDelta := controller.Revision - latest.MutationControllerRevision
		leaseDelta := controller.LeaseEpoch - latest.MutationLeaseEpoch
		if controller.CurrentAttemptID != latest.AttemptID || revisionDelta < 1 ||
			controller.Revision > MaxPRDevelopmentControllerRevision-1 {
			return errors.New("stored pending controller does not own its tail fence")
		}
		switch controller.Phase {
		case PRDevelopmentControllerReviewPending:
			if leaseDelta < revisionDelta-1 {
				return errors.New("stored pending controller review claims cannot explain its revision")
			}
		case PRDevelopmentControllerReview:
			if leaseDelta < revisionDelta {
				return errors.New("stored live review claims cannot explain its revision")
			}
		default:
			return errors.New("stored controller advanced beyond an unreviewed fence")
		}
		return nil
	}
	if controller.Phase == PRDevelopmentControllerReady {
		if controller.CurrentAttemptID != latest.AttemptID ||
			controller.LeaseEpoch != latest.ReviewLeaseEpoch ||
			controller.Revision != latest.ReviewControllerRevision+1 {
			return errors.New("stored ready controller high-water state differs from its fence")
		}
		return nil
	}
	if controller.CurrentAttemptID == latest.AttemptID {
		return errors.New("stored post-review controller attempt or lease epoch is invalid")
	}
	if latest.ReviewControllerRevision > MaxPRDevelopmentControllerRevision-5 {
		return errors.New(
			"stored post-review controller exceeds the legacy revision ceiling",
		)
	}
	finalizedRecoveries := recoveryStats.finalizedByAttempt[controller.CurrentAttemptID]
	expectedLeaseEpoch := latest.ReviewLeaseEpoch + 1 + int64(finalizedRecoveries)
	if controller.LeaseEpoch != expectedLeaseEpoch {
		return errors.New("stored post-review controller recovery lease epoch is invalid")
	}
	switch controller.Phase {
	case PRDevelopmentControllerMutation:
		expectedRevision := latest.ReviewControllerRevision + 2 +
			int64(2*finalizedRecoveries)
		if controller.MutationEpoch == controller.LineVersion+1 {
			expectedRevision++
		}
		if controller.Revision != expectedRevision {
			return errors.New("stored resumed mutation controller revision is invalid")
		}
	case PRDevelopmentControllerRecoveryRequired:
		expectedRevision := latest.ReviewControllerRevision + 3 +
			int64(2*finalizedRecoveries)
		if controller.MutationEpoch == controller.LineVersion+1 {
			expectedRevision++
		}
		if controller.Revision != expectedRevision {
			return errors.New("stored resumed recovery controller revision is invalid")
		}
	default:
		return errors.New("stored reviewed controller phase is invalid")
	}
	return nil
}

func wrapInvalidStoredPRDevelopmentController(err error) error {
	return fmt.Errorf("%w: %v", errInvalidStoredPRDevelopmentController, err)
}

func loadPRDevelopmentReviewFences(
	ctx context.Context,
	queryer rowsQueryer,
	controllerID string,
) ([]PRDevelopmentAttemptReviewFence, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT `+prDevelopmentReviewFenceColumns+`
		FROM pr_development_attempt_review_fences
		WHERE controller_id = ?
		ORDER BY ordinal`,
		controllerID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	fences := make([]PRDevelopmentAttemptReviewFence, 0)
	for rows.Next() {
		fence, scanErr := scanPRDevelopmentReviewFence(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		fences = append(fences, fence)
		if len(fences) > MaxPRDevelopmentControllerFences {
			return nil, wrapInvalidStoredPRDevelopmentController(
				errors.New("stored controller has too many review fences"),
			)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return fences, nil
}

func loadPRDevelopmentReviewFenceByAttempt(
	ctx context.Context,
	queryer rowQueryer,
	attemptID string,
) (PRDevelopmentAttemptReviewFence, bool, error) {
	fence, err := scanPRDevelopmentReviewFence(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentReviewFenceColumns+`
		FROM pr_development_attempt_review_fences
		WHERE attempt_id = ?`,
		attemptID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentAttemptReviewFence{}, false, nil
	}
	if err != nil {
		return PRDevelopmentAttemptReviewFence{}, false, err
	}
	return fence, true, nil
}

func loadLatestPRDevelopmentReviewFence(
	ctx context.Context,
	queryer rowQueryer,
	controllerID string,
) (PRDevelopmentAttemptReviewFence, bool, error) {
	fence, err := scanPRDevelopmentReviewFence(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentReviewFenceColumns+`
		FROM pr_development_attempt_review_fences
		WHERE controller_id = ?
		ORDER BY ordinal DESC
		LIMIT 1`,
		controllerID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentAttemptReviewFence{}, false, nil
	}
	if err != nil {
		return PRDevelopmentAttemptReviewFence{}, false, err
	}
	return fence, true, nil
}

func scanPRDevelopmentReviewFence(
	scanner rowScanner,
) (PRDevelopmentAttemptReviewFence, error) {
	var (
		fence              PRDevelopmentAttemptReviewFence
		ordinal, noChanges int64
		createdAt          int64
		reviewedAt         sql.NullInt64
	)
	if err := scanner.Scan(
		&fence.AttemptID,
		&fence.ControllerID,
		&fence.ThreadID,
		&fence.LineID,
		&ordinal,
		&fence.LineVersion,
		&fence.MutationEpoch,
		&fence.ParkIntentID,
		&fence.BaseCommit,
		&fence.TipCommit,
		&fence.Tree,
		&noChanges,
		&fence.LineReviewDigest,
		&fence.MutationReservationDigest,
		&fence.MutationLeaseEpoch,
		&fence.MutationLeaseTokenDigest,
		&fence.MutationControllerRevision,
		&fence.ReviewLeaseEpoch,
		&fence.ReviewLeaseTokenDigest,
		&fence.ReviewControllerRevision,
		&fence.PreviousHash,
		&fence.FenceHash,
		&createdAt,
		&reviewedAt,
	); err != nil {
		return PRDevelopmentAttemptReviewFence{}, err
	}
	fence.Ordinal = int(ordinal)
	if int64(fence.Ordinal) != ordinal || (noChanges != 0 && noChanges != 1) {
		return PRDevelopmentAttemptReviewFence{}, wrapInvalidStoredPRDevelopmentController(
			errors.New("stored review fence integer is invalid"),
		)
	}
	fence.NoChanges = noChanges == 1
	fence.CreatedAt = fromDBTime(createdAt)
	fence.ReviewedAt = fromNullableTime(reviewedAt)
	if err := validateStoredPRDevelopmentReviewFence(fence); err != nil {
		return PRDevelopmentAttemptReviewFence{}, wrapInvalidStoredPRDevelopmentController(err)
	}
	return fence, nil
}

func validateStoredPRDevelopmentReviewFence(
	fence PRDevelopmentAttemptReviewFence,
) error {
	if !validPrefixedHexID(fence.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(fence.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(fence.ThreadID, prDevelopmentThreadIDPrefix) ||
		!validPrefixedHexID(fence.LineID, prDevelopmentLineIDPrefix) ||
		fence.Ordinal < 0 || fence.Ordinal >= MaxPRDevelopmentControllerFences ||
		fence.LineVersion < 1 ||
		fence.LineVersion > MaxPRDevelopmentControllerFences ||
		fence.MutationEpoch != fence.LineVersion ||
		!validPRDevelopmentRepairIdentity(
			fence.ParkIntentID,
			MaxPRDevelopmentControllerIdentityBytes,
		) || !validSameWidthPRDevelopmentOIDs(
		fence.BaseCommit,
		fence.TipCommit,
		fence.Tree,
	) || fence.NoChanges != (fence.BaseCommit == fence.TipCommit) ||
		!validPRDevelopmentHex(fence.LineReviewDigest, sha256.Size*2) ||
		!validPRDevelopmentHex(fence.MutationReservationDigest, sha256.Size*2) ||
		fence.MutationLeaseEpoch < 1 ||
		!validPRDevelopmentHex(fence.MutationLeaseTokenDigest, sha256.Size*2) ||
		fence.MutationControllerRevision < 1 ||
		fence.MutationControllerRevision > MaxPRDevelopmentControllerRevision ||
		!validPRDevelopmentHex(fence.PreviousHash, sha256.Size*2) ||
		!validPRDevelopmentHex(fence.FenceHash, sha256.Size*2) ||
		validateDBTimestamp("review fence creation time", fence.CreatedAt) != nil ||
		fence.FenceHash != hashPRDevelopmentReviewFence(fence) {
		return errors.New("stored review fence is invalid")
	}
	if fence.ReviewedAt == nil {
		if fence.ReviewLeaseEpoch != 0 || fence.ReviewLeaseTokenDigest != "" ||
			fence.ReviewControllerRevision != 0 {
			return errors.New("stored unreviewed fence has review completion proof")
		}
	} else if validateDBTimestamp("review fence review time", *fence.ReviewedAt) != nil ||
		fence.ReviewedAt.Before(fence.CreatedAt) || fence.ReviewLeaseEpoch < 1 ||
		!validPRDevelopmentHex(fence.ReviewLeaseTokenDigest, sha256.Size*2) ||
		fence.ReviewControllerRevision <= fence.MutationControllerRevision ||
		fence.ReviewControllerRevision > MaxPRDevelopmentControllerRevision {
		return errors.New("stored reviewed fence completion proof is invalid")
	}
	return nil
}

func emptyPRDevelopmentReviewFencesDigest() string {
	digest := sha256.Sum256([]byte("picoclaw-pr-development-review-fences-v1\x00empty"))
	return hex.EncodeToString(digest[:])
}

func prDevelopmentMutationReservationDigest(reservation string) string {
	digest := sha256.Sum256([]byte(
		"picoclaw-pr-development-mutation-reservation-v1\x00" + reservation,
	))
	return hex.EncodeToString(digest[:])
}

func prDevelopmentLeaseTokenDigest(
	kind PRDevelopmentControllerLeaseKind,
	token string,
) string {
	digest := sha256.Sum256([]byte(
		"picoclaw-pr-development-controller-lease-v1\x00" + string(kind) + "\x00" + token,
	))
	return hex.EncodeToString(digest[:])
}

func hashPRDevelopmentReviewFence(
	fence PRDevelopmentAttemptReviewFence,
) string {
	digest := sha256.New()
	writePRDevelopmentControllerHashField(
		digest,
		"picoclaw-pr-development-review-fence-v2",
	)
	reviewedAt := ""
	if fence.ReviewedAt != nil {
		reviewedAt = fmt.Sprintf("%d", toDBTime(*fence.ReviewedAt))
	}
	for _, value := range []string{
		fence.AttemptID,
		fence.ControllerID,
		fence.ThreadID,
		fence.LineID,
		fmt.Sprintf("%d", fence.Ordinal),
		fmt.Sprintf("%d", fence.LineVersion),
		fmt.Sprintf("%d", fence.MutationEpoch),
		fence.ParkIntentID,
		fence.BaseCommit,
		fence.TipCommit,
		fence.Tree,
		fmt.Sprintf("%t", fence.NoChanges),
		fence.LineReviewDigest,
		fence.MutationReservationDigest,
		fmt.Sprintf("%d", fence.MutationLeaseEpoch),
		fence.MutationLeaseTokenDigest,
		fmt.Sprintf("%d", fence.MutationControllerRevision),
		fmt.Sprintf("%d", fence.ReviewLeaseEpoch),
		fence.ReviewLeaseTokenDigest,
		fmt.Sprintf("%d", fence.ReviewControllerRevision),
		reviewedAt,
		fence.PreviousHash,
		fmt.Sprintf("%d", toDBTime(fence.CreatedAt)),
	} {
		writePRDevelopmentControllerHashField(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writePRDevelopmentControllerHashField(digest hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(value))
}

func equalPRDevelopmentControllerLineBindingExceptEpoch(
	controller PRDevelopmentController,
	input PRDevelopmentControllerLineBind,
) bool {
	return controller.ID == input.ControllerID &&
		controller.CurrentAttemptID == input.AttemptID &&
		controller.WorkspaceID == input.WorkspaceID &&
		controller.SourceCloneURL == input.SourceCloneURL &&
		controller.SourceRef == input.SourceRef &&
		controller.SourceCommit == input.SourceCommit &&
		controller.SourceTree == input.SourceTree &&
		controller.LineVersion == input.LineVersion &&
		controller.TipCommit == input.TipCommit && controller.Tree == input.Tree
}

func equalPRDevelopmentReviewFenceRecord(
	fence PRDevelopmentAttemptReviewFence,
	input PRDevelopmentAttemptReviewFenceRecord,
) bool {
	return fence.ControllerID == input.ControllerID &&
		fence.AttemptID == input.AttemptID &&
		fence.MutationControllerRevision == input.ExpectedRevision &&
		fence.MutationLeaseEpoch == input.LeaseEpoch &&
		fence.MutationLeaseTokenDigest == prDevelopmentLeaseTokenDigest(
			PRDevelopmentControllerMutationLease,
			input.LeaseToken,
		) &&
		fence.LineVersion == input.LineVersion &&
		fence.MutationEpoch == input.MutationEpoch &&
		fence.ParkIntentID == input.ParkIntentID &&
		fence.BaseCommit == input.BaseCommit &&
		fence.TipCommit == input.TipCommit && fence.Tree == input.Tree &&
		fence.NoChanges == input.NoChanges &&
		fence.LineReviewDigest == input.LineReviewDigest
}

func equalPRDevelopmentFinishedReviewReplay(
	fence PRDevelopmentAttemptReviewFence,
	input PRDevelopmentControllerReviewTransition,
) bool {
	return fence.ControllerID == input.ControllerID &&
		fence.AttemptID == input.AttemptID &&
		fence.ReviewControllerRevision == input.ExpectedRevision &&
		fence.ReviewLeaseEpoch == input.LeaseEpoch &&
		fence.ReviewLeaseTokenDigest == prDevelopmentLeaseTokenDigest(
			PRDevelopmentControllerReviewLease,
			input.LeaseToken,
		)
}

func requireLivePRDevelopmentControllerLease(
	controller PRDevelopmentController,
	attemptID, leaseToken string,
	leaseEpoch int64,
	phase PRDevelopmentControllerPhase,
	now time.Time,
) error {
	kind := PRDevelopmentControllerReviewLease
	if phase == PRDevelopmentControllerMutation {
		kind = PRDevelopmentControllerMutationLease
	}
	if controller.Phase != phase || controller.LeaseKind != kind ||
		controller.CurrentAttemptID != attemptID || controller.LeaseToken != leaseToken ||
		controller.LeaseEpoch != leaseEpoch || controller.LeaseUntil == nil {
		return fmt.Errorf(
			"%w: operation does not hold the exact controller lease",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if !controller.LeaseUntil.After(now) {
		if phase == PRDevelopmentControllerMutation {
			return ErrPRDevelopmentControllerRecoveryRequired
		}
		return fmt.Errorf(
			"%w: review lease expired and must be reacquired",
			ErrPRDevelopmentControllerConflict,
		)
	}
	return nil
}
