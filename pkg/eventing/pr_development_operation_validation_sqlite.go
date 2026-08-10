//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"
)

func validatePRDevelopmentControllerOperationChain(
	ctx context.Context,
	queryer rowsQueryer,
	controller PRDevelopmentController,
	session PRDevelopmentRepairSession,
	fences []PRDevelopmentAttemptReviewFence,
	attemptOrdinals map[string]int,
	attemptCreatedAt map[string]time.Time,
) error {
	operations, err := loadPRDevelopmentControllerOperations(ctx, queryer, controller.ID)
	if err != nil {
		return err
	}
	if len(operations) == 0 {
		return nil
	}
	var (
		previousAttemptOrdinal = -1
		currentAttemptID       string
		currentKinds           []PRDevelopmentControllerOperationKind
		previousTime           time.Time
		active                 *PRDevelopmentControllerOperation
	)
	finalizedByAttempt := make(map[string][]PRDevelopmentControllerOperation)
	for index := range operations {
		operation := operations[index]
		attemptOrdinal, owned := attemptOrdinals[operation.AttemptID]
		if !owned || operation.ControllerID != controller.ID ||
			operation.AgentID != controller.AgentID ||
			operation.WorkspaceID != session.WorkspaceID ||
			operation.LineID != controller.LineID ||
			operation.SourceCloneURL != session.CloneURL ||
			operation.SourceRef != session.HeadRef ||
			operation.SourceCommit != session.HeadSHA ||
			operation.CreatedAt.Before(controller.CreatedAt) ||
			operation.CreatedAt.Before(attemptCreatedAt[operation.AttemptID]) ||
			(!previousTime.IsZero() && operation.CreatedAt.Before(previousTime)) {
			return errors.New("stored controller operation ownership or time is invalid")
		}
		if controller.SourceTree != "" && operation.SourceTree != controller.SourceTree {
			return errors.New("stored controller operation source tree changed")
		}
		if err := validateStoredPRDevelopmentControllerOperationRequest(
			operation,
			session,
			finalizedByAttempt[operation.AttemptID],
		); err != nil {
			return err
		}
		if operation.AttemptID != currentAttemptID {
			if currentAttemptID != "" && (len(currentKinds) == 0 ||
				currentKinds[len(currentKinds)-1] != PRDevelopmentControllerOperationPark) {
				return errors.New("stored controller operation attempt changed before Park")
			}
			if attemptOrdinal <= previousAttemptOrdinal {
				return errors.New("stored controller operation attempt order regressed")
			}
			expectedLineKind := PRDevelopmentControllerOperationResume
			priorFence := false
			for _, fence := range fences {
				if attemptOrdinals[fence.AttemptID] < attemptOrdinal {
					priorFence = true
					break
				}
			}
			if !priorFence {
				expectedLineKind = PRDevelopmentControllerOperationAdopt
			}
			legacyBoundHighWater := index == 0 &&
				(operation.Kind == PRDevelopmentControllerOperationCommit ||
					operation.Kind == PRDevelopmentControllerOperationPark)
			if operation.Kind != expectedLineKind && !legacyBoundHighWater {
				return errors.New(
					"stored controller operation starts with the wrong line transition",
				)
			}
			currentAttemptID = operation.AttemptID
			currentKinds = currentKinds[:0]
			previousAttemptOrdinal = attemptOrdinal
		}
		validOrder := false
		switch len(currentKinds) {
		case 0:
			validOrder = operation.Kind == PRDevelopmentControllerOperationAdopt ||
				operation.Kind == PRDevelopmentControllerOperationResume ||
				index == 0 && (operation.Kind == PRDevelopmentControllerOperationCommit ||
					operation.Kind == PRDevelopmentControllerOperationPark)
		case 1:
			validOrder = (currentKinds[0] == PRDevelopmentControllerOperationAdopt ||
				currentKinds[0] == PRDevelopmentControllerOperationResume) &&
				(operation.Kind == PRDevelopmentControllerOperationCommit ||
					operation.Kind == PRDevelopmentControllerOperationPark) ||
				currentKinds[0] == PRDevelopmentControllerOperationCommit &&
					operation.Kind == PRDevelopmentControllerOperationPark
		case 2:
			validOrder = currentKinds[1] == PRDevelopmentControllerOperationCommit &&
				operation.Kind == PRDevelopmentControllerOperationPark
		}
		if !validOrder {
			return errors.New("stored controller operation kind order is invalid")
		}
		currentKinds = append(currentKinds, operation.Kind)
		if operation.Status == PRDevelopmentControllerOperationFinalized {
			if operation.Kind == PRDevelopmentControllerOperationCommit &&
				!operation.Result.WorkspaceClean {
				suspension, found, loadErr :=
					loadPRDevelopmentControllerSuspensionBySource(
						ctx,
						queryer,
						PRDevelopmentControllerSuspensionSourceOperationRecovery,
						operation.RecoveryID,
					)
				if loadErr != nil {
					return loadErr
				}
				if !found ||
					suspension.SourceOperationID != operation.ID ||
					suspension.SourceFinalRevision != operation.FinalControllerRevision ||
					suspension.SourceFinalHash != operation.FinalHash ||
					suspension.Mode != PRDevelopmentControllerSuspensionCommitRecovery {
					return errors.New(
						"stored dirty recovered Commit has no exact suspension handoff",
					)
				}
			}
			finalizedByAttempt[operation.AttemptID] = append(
				finalizedByAttempt[operation.AttemptID],
				operation,
			)
			previousTime = *operation.FinalizedAt
			if operation.FinalizedAt.After(controller.UpdatedAt) ||
				operation.FinalControllerRevision > controller.Revision {
				return errors.New("stored finalized operation exceeds controller high-water")
			}
			if operation.Kind == PRDevelopmentControllerOperationPark {
				if err := validateFinalizedPRDevelopmentParkOperation(
					ctx,
					queryer,
					operation,
				); err != nil {
					return err
				}
			}
		} else {
			if index != len(operations)-1 || active != nil {
				return errors.New("stored unresolved controller operation is not the tail")
			}
			operationCopy := operation
			active = &operationCopy
			previousTime = operation.UpdatedAt
		}
	}
	for _, fence := range fences {
		attemptOperations := finalizedByAttempt[fence.AttemptID]
		if len(attemptOperations) != 0 &&
			attemptOperations[len(attemptOperations)-1].Kind !=
				PRDevelopmentControllerOperationPark {
			return errors.New(
				"stored fenced operation attempt did not terminate with Park",
			)
		}
	}
	if active == nil {
		return nil
	}
	if active.AttemptID != controller.CurrentAttemptID ||
		active.MutationReservationDigest != prDevelopmentMutationReservationDigest(
			controller.MutationReservationKey,
		) || active.MutationLeaseEpoch != controller.LeaseEpoch {
		return errors.New("stored active operation differs from controller authority")
	}
	if (active.Status == PRDevelopmentControllerOperationRecoveryPending ||
		active.Status == PRDevelopmentControllerOperationRecoveryClaimed) &&
		active.Kind != PRDevelopmentControllerOperationPark {
		if err := validateActivePRDevelopmentOperationReplacementFresh(
			ctx,
			queryer,
			*active,
		); err != nil {
			return err
		}
	}
	if _, found, err := loadActivePRDevelopmentRecoveryIntent(
		ctx,
		queryer,
		controller.ID,
	); err != nil {
		return err
	} else if found {
		return errors.New("stored active operation has a competing recovery intent")
	}
	switch active.Status {
	case PRDevelopmentControllerOperationPending:
		if controller.Phase != PRDevelopmentControllerMutation ||
			controller.Revision != active.PreparedControllerRevision ||
			controller.LeaseKind != PRDevelopmentControllerMutationLease ||
			prDevelopmentLeaseTokenDigest(
				PRDevelopmentControllerMutationLease,
				controller.LeaseToken,
			) != active.MutationLeaseTokenDigest {
			return errors.New("stored pending operation lost its mutation lease")
		}
	case PRDevelopmentControllerOperationRecoveryPending,
		PRDevelopmentControllerOperationRecoveryClaimed:
		if controller.Phase != PRDevelopmentControllerRecoveryRequired ||
			controller.Revision != active.RecoveryRevision || controller.LeaseKind != "" ||
			controller.LeaseToken != "" || controller.LeaseUntil != nil ||
			active.RecoveryStagedAt == nil ||
			!active.RecoveryStagedAt.Equal(controller.UpdatedAt) {
			return errors.New("stored recovering operation differs from controller quarantine")
		}
	default:
		return errors.New("stored active operation status is invalid")
	}
	return nil
}

func validateActivePRDevelopmentOperationReplacementFresh(
	ctx context.Context,
	queryer rowsQueryer,
	operation PRDevelopmentControllerOperation,
) error {
	var collisions int
	if err := queryer.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM pr_development_repair_sessions
			 WHERE reservation_key = ?) +
			(SELECT COUNT(*) FROM pr_development_thread_controllers
			 WHERE mutation_reservation_key = ?) +
			(SELECT COUNT(*) FROM pr_development_attempt_review_fences
			 WHERE mutation_reservation_digest = ?) +
			(SELECT COUNT(*) FROM pr_development_controller_recovery_intents
			 WHERE previous_reservation_digest = ? OR replacement_reservation_digest = ?) +
			(SELECT COUNT(*) FROM pr_development_controller_operation_intents
			 WHERE id <> ? AND (mutation_reservation_digest = ? OR
				replacement_reservation_digest = ? OR replacement_reservation_key = ?)) +
			(SELECT COUNT(*) FROM pr_development_controller_suspensions
			 WHERE suspension_reservation_digest = ? OR resume_reservation_digest = ?)`,
		operation.ReplacementReservationKey,
		operation.ReplacementReservationKey,
		operation.ReplacementReservationDigest,
		operation.ReplacementReservationDigest,
		operation.ReplacementReservationDigest,
		operation.ID,
		operation.ReplacementReservationDigest,
		operation.ReplacementReservationDigest,
		operation.ReplacementReservationKey,
		operation.ReplacementReservationDigest,
		operation.ReplacementReservationDigest,
	).Scan(&collisions); err != nil {
		return err
	}
	if collisions != 0 {
		return errors.New(
			"stored active operation recovery replacement reuses foreign authority",
		)
	}
	return nil
}

func validateFinalizedPRDevelopmentParkOperation(
	ctx context.Context,
	queryer rowsQueryer,
	operation PRDevelopmentControllerOperation,
) error {
	fence, found, err := loadPRDevelopmentReviewFenceByAttempt(
		ctx,
		queryer,
		operation.AttemptID,
	)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("stored finalized Park operation lost its review fence")
	}
	parkFenceHash := fence.FenceHash
	if fence.ReviewedAt != nil {
		parked := fence
		parked.ReviewLeaseEpoch = 0
		parked.ReviewLeaseTokenDigest = ""
		parked.ReviewControllerRevision = 0
		parked.ReviewedAt = nil
		parkFenceHash = hashPRDevelopmentReviewFence(parked)
	}
	if parkFenceHash != operation.FinalFenceHash ||
		fence.MutationControllerRevision != operation.PreparedControllerRevision ||
		fence.MutationReservationDigest != operation.MutationReservationDigest ||
		fence.MutationLeaseEpoch != operation.MutationLeaseEpoch ||
		fence.MutationLeaseTokenDigest != operation.MutationLeaseTokenDigest ||
		fence.LineVersion != operation.Result.Version ||
		fence.MutationEpoch != operation.Result.MutationEpoch ||
		fence.ParkIntentID != operation.Result.ReviewParkIntentID ||
		fence.BaseCommit != operation.Result.ReviewBaseCommit ||
		fence.TipCommit != operation.Result.ReviewCommit ||
		fence.Tree != operation.Result.ReviewTree ||
		fence.LineReviewDigest != operation.Result.ReviewDigest {
		return errors.New("stored finalized Park operation differs from its review fence")
	}
	session, err := loadPRDevelopmentRepairSessionByAttempt(
		ctx,
		queryer,
		operation.AttemptID,
	)
	if err != nil {
		return err
	}
	var attempt *PRDevelopmentRepairAttempt
	for index := range session.Attempts {
		if session.Attempts[index].ID == operation.AttemptID {
			attempt = &session.Attempts[index]
			break
		}
	}
	if attempt == nil || attempt.Status != PRDevelopmentRepairCompleted ||
		attempt.Summary != operation.Request.CompletionSummary ||
		attempt.Iterations != operation.Request.CompletionIterations ||
		attempt.Claims < 1 || attempt.ErrorCode != "" || attempt.InternalError != "" {
		return errors.New(
			"stored finalized Park operation differs from its completed repair account",
		)
	}
	return nil
}

func validateStoredPRDevelopmentControllerOperationRequest(
	operation PRDevelopmentControllerOperation,
	session PRDevelopmentRepairSession,
	prior []PRDevelopmentControllerOperation,
) error {
	expected := PRDevelopmentControllerOperationRequest{
		Repository:   session.HeadRepository,
		SourceRef:    session.HeadRef,
		SourceCommit: session.HeadSHA,
		AgentID:      operation.AgentID,
		WorkspaceID:  operation.WorkspaceID,
		LineID:       operation.LineID,
	}
	switch operation.Kind {
	case PRDevelopmentControllerOperationAdopt:
		if operation.LineVersion != 0 || operation.MutationEpoch != 0 ||
			operation.TipCommit != operation.SourceCommit ||
			operation.Tree != operation.SourceTree ||
			!validSameWidthPRDevelopmentOIDs(operation.SourceCommit, operation.SourceTree) {
			return errors.New("stored Adopt operation source fence is invalid")
		}
		expected.ExpectedTree = operation.SourceTree
	case PRDevelopmentControllerOperationResume:
		if operation.MutationEpoch != operation.LineVersion ||
			!validSameWidthPRDevelopmentOIDs(
				operation.SourceCommit,
				operation.SourceTree,
				operation.TipCommit,
				operation.Tree,
			) {
			return errors.New("stored Resume operation line fence is invalid")
		}
		expected.ExpectedVersion = operation.LineVersion
		expected.ExpectedEpoch = operation.MutationEpoch
		expected.ExpectedTip = operation.TipCommit
		expected.ExpectedTree = operation.Tree
	case PRDevelopmentControllerOperationCommit:
		request := operation.Request
		if operation.MutationEpoch != operation.LineVersion+1 ||
			!validSameWidthPRDevelopmentOIDs(
				operation.TipCommit,
				operation.Tree,
				request.ExpectedTree,
			) || request.ExpectedTree == operation.Tree ||
			request.ExpectedParent != operation.TipCommit ||
			request.EffectIntentID != operation.EffectIntentID ||
			!validPRDevelopmentHex(request.CandidateDigest, sha256.Size*2) ||
			!validPRDevelopmentRepairIdentity(
				request.CommitMessage,
				prDevelopmentOperationCommitMessageBytes,
			) || request.AuthoredAt.IsZero() || request.AuthoredAt.Location() != time.UTC ||
			request.AuthoredAt.Nanosecond() != 0 {
			return errors.New("stored Commit operation request is invalid")
		}
		expected.EffectIntentID = request.EffectIntentID
		expected.ExpectedParent = request.ExpectedParent
		expected.ExpectedTree = request.ExpectedTree
		expected.CandidateDigest = request.CandidateDigest
		expected.CommitMessage = request.CommitMessage
		expected.AuthoredAt = request.AuthoredAt
	case PRDevelopmentControllerOperationPark:
		request := operation.Request
		if operation.MutationEpoch != operation.LineVersion+1 ||
			request.EffectIntentID != operation.EffectIntentID ||
			request.ExpectedVersion != operation.LineVersion ||
			request.MutationEpoch != operation.MutationEpoch ||
			request.PreviousTip != operation.TipCommit ||
			!validStoredPRDevelopmentRepairText(
				request.CompletionSummary,
				MaxPRDevelopmentRepairSummaryBytes,
			) || request.CompletionIterations < 1 ||
			request.CompletionIterations > MaxPRDevelopmentRepairIterations {
			return errors.New("stored Park operation request is invalid")
		}
		expected.EffectIntentID = request.EffectIntentID
		expected.ExpectedVersion = operation.LineVersion
		expected.MutationEpoch = operation.MutationEpoch
		expected.PreviousTip = operation.TipCommit
		expected.CompletionSummary = request.CompletionSummary
		expected.CompletionIterations = request.CompletionIterations
		var commit *PRDevelopmentControllerOperation
		for index := range prior {
			candidate := prior[index]
			if candidate.Kind == PRDevelopmentControllerOperationCommit {
				candidateCopy := candidate
				commit = &candidateCopy
			}
		}
		if commit == nil {
			expected.Tip = operation.TipCommit
			expected.Tree = operation.Tree
			expected.NoChanges = true
		} else {
			expected.Tip = commit.Result.Commit
			expected.Tree = commit.Result.Tree
		}
	default:
		return errors.New("stored operation request has an unknown kind")
	}
	expectedJSON, _, err := encodePRDevelopmentOperationRequest(expected)
	if err != nil {
		return err
	}
	if !bytesEqualPRDevelopmentOperation(expectedJSON, operation.RequestJSON) {
		return errors.New("stored operation request differs from its controller fence")
	}
	return nil
}
