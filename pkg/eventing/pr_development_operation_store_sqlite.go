//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const prDevelopmentControllerOperationColumns = `
	id, controller_id, attempt_id, ordinal, kind, status,
	prepared_controller_revision, agent_id, workspace_id, line_id,
	source_clone_url, source_ref, source_commit, source_tree, line_version,
	mutation_epoch, tip_commit, tree, mutation_reservation_digest,
	mutation_lease_epoch, mutation_lease_token_digest, effect_intent_id,
	request_json, request_hash, previous_hash, intent_hash, recovery_id,
	replacement_reservation_key, replacement_reservation_digest,
	recovery_revision, expired_controller_revision, expired_lease_epoch,
	expired_lease_token_digest, recovery_lease_until, recovery_staged_at,
	recovery_hash, claim_id, claim_owner, claim_token, claim_until,
	claim_epoch, claims, claimed_at, rotation_result_hash,
	recovery_claim_token_digest, new_mutation_lease_epoch,
	new_mutation_lease_token_digest, new_mutation_lease_until, result_json,
	result_hash, stage_authorization_digest, final_controller_revision,
	final_controller_phase, final_fence_hash, final_hash, created_at,
	finalized_at, updated_at`

const (
	prDevelopmentOperationCommitMessageBytes = 512
	prDevelopmentOperationChangedFilesMax    = 10_000
)

func loadPRDevelopmentControllerOperationByID(
	ctx context.Context,
	queryer rowsQueryer,
	operationID string,
) (PRDevelopmentControllerOperation, bool, error) {
	operation, err := scanPRDevelopmentControllerOperation(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentControllerOperationColumns+`
		FROM pr_development_controller_operation_intents
		WHERE id = ?`, operationID))
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentControllerOperation{}, false, nil
	}
	if err != nil {
		return PRDevelopmentControllerOperation{}, false, err
	}
	return operation, true, nil
}

func loadPRDevelopmentControllerOperationByRecoveryID(
	ctx context.Context,
	queryer rowsQueryer,
	recoveryID string,
) (PRDevelopmentControllerOperation, bool, error) {
	operation, err := scanPRDevelopmentControllerOperation(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentControllerOperationColumns+`
		FROM pr_development_controller_operation_intents
		WHERE recovery_id = ?`, recoveryID))
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentControllerOperation{}, false, nil
	}
	if err != nil {
		return PRDevelopmentControllerOperation{}, false, err
	}
	return operation, true, nil
}

func loadActivePRDevelopmentControllerOperation(
	ctx context.Context,
	queryer rowsQueryer,
	controllerID string,
) (PRDevelopmentControllerOperation, bool, error) {
	operation, err := scanPRDevelopmentControllerOperation(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentControllerOperationColumns+`
		FROM pr_development_controller_operation_intents
		WHERE controller_id = ? AND status <> 'finalized'`, controllerID))
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentControllerOperation{}, false, nil
	}
	if err != nil {
		return PRDevelopmentControllerOperation{}, false, err
	}
	return operation, true, nil
}

func loadPRDevelopmentControllerOperations(
	ctx context.Context,
	queryer rowsQueryer,
	controllerID string,
) ([]PRDevelopmentControllerOperation, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT `+prDevelopmentControllerOperationColumns+`
		FROM pr_development_controller_operation_intents
		WHERE controller_id = ?
		ORDER BY ordinal`, controllerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanPRDevelopmentControllerChainRows(
		rows,
		prDevelopmentControllerChainSpec[PRDevelopmentControllerOperation]{
			initialHash: emptyPRDevelopmentOperationDigest(),
			maximum:     MaxPRDevelopmentControllerOperations,
			scan:        scanPRDevelopmentControllerOperation,
			link: func(operation PRDevelopmentControllerOperation) prDevelopmentControllerChainLink {
				return prDevelopmentControllerChainLink{
					ordinal:      operation.Ordinal,
					previousHash: operation.PreviousHash,
					finalHash:    operation.FinalHash,
					resolved:     operation.Status == PRDevelopmentControllerOperationFinalized,
				}
			},
			discontinuousText: "stored controller operation chain is not contiguous",
			unresolvedText:    "stored controller operation has an unresolved predecessor",
			capacityText:      "stored controller has too many operations",
		},
	)
}

func scanPRDevelopmentControllerOperation(
	scanner rowScanner,
) (PRDevelopmentControllerOperation, error) {
	var (
		operation                            PRDevelopmentControllerOperation
		ordinal, claims                      int64
		recoveryLeaseUntil, recoveryStagedAt sql.NullInt64
		claimUntil, claimedAt                sql.NullInt64
		newMutationLeaseUntil, finalizedAt   sql.NullInt64
		createdAt, updatedAt                 int64
	)
	if err := scanner.Scan(
		&operation.ID,
		&operation.ControllerID,
		&operation.AttemptID,
		&ordinal,
		&operation.Kind,
		&operation.Status,
		&operation.PreparedControllerRevision,
		&operation.AgentID,
		&operation.WorkspaceID,
		&operation.LineID,
		&operation.SourceCloneURL,
		&operation.SourceRef,
		&operation.SourceCommit,
		&operation.SourceTree,
		&operation.LineVersion,
		&operation.MutationEpoch,
		&operation.TipCommit,
		&operation.Tree,
		&operation.MutationReservationDigest,
		&operation.MutationLeaseEpoch,
		&operation.MutationLeaseTokenDigest,
		&operation.EffectIntentID,
		&operation.RequestJSON,
		&operation.RequestHash,
		&operation.PreviousHash,
		&operation.IntentHash,
		&operation.RecoveryID,
		&operation.ReplacementReservationKey,
		&operation.ReplacementReservationDigest,
		&operation.RecoveryRevision,
		&operation.ExpiredControllerRevision,
		&operation.ExpiredLeaseEpoch,
		&operation.ExpiredLeaseTokenDigest,
		&recoveryLeaseUntil,
		&recoveryStagedAt,
		&operation.RecoveryHash,
		&operation.ClaimID,
		&operation.ClaimOwner,
		&operation.ClaimToken,
		&claimUntil,
		&operation.ClaimEpoch,
		&claims,
		&claimedAt,
		&operation.RotationResultHash,
		&operation.RecoveryClaimTokenDigest,
		&operation.NewMutationLeaseEpoch,
		&operation.NewMutationLeaseTokenDigest,
		&newMutationLeaseUntil,
		&operation.ResultJSON,
		&operation.ResultHash,
		&operation.StageAuthorizationDigest,
		&operation.FinalControllerRevision,
		&operation.FinalControllerPhase,
		&operation.FinalFenceHash,
		&operation.FinalHash,
		&createdAt,
		&finalizedAt,
		&updatedAt,
	); err != nil {
		return PRDevelopmentControllerOperation{}, err
	}
	operation.Ordinal = int(ordinal)
	operation.Claims = int(claims)
	if int64(operation.Ordinal) != ordinal || int64(operation.Claims) != claims {
		return PRDevelopmentControllerOperation{}, errors.New(
			"stored controller operation integer overflows",
		)
	}
	operation.RecoveryLeaseUntil = fromNullableTime(recoveryLeaseUntil)
	operation.RecoveryStagedAt = fromNullableTime(recoveryStagedAt)
	operation.ClaimUntil = fromNullableTime(claimUntil)
	operation.ClaimedAt = fromNullableTime(claimedAt)
	operation.NewMutationLeaseUntil = fromNullableTime(newMutationLeaseUntil)
	operation.FinalizedAt = fromNullableTime(finalizedAt)
	operation.CreatedAt = fromDBTime(createdAt)
	operation.UpdatedAt = fromDBTime(updatedAt)

	request, err := decodePRDevelopmentOperationRequest(operation.RequestJSON)
	if err != nil {
		return PRDevelopmentControllerOperation{}, fmt.Errorf(
			"stored controller operation request is invalid: %w", err,
		)
	}
	operation.Request = request
	if operation.RequestHash != prDevelopmentOperationPayloadHash(
		"picoclaw-pr-development-operation-request-v1\x00",
		operation.RequestJSON,
	) {
		return PRDevelopmentControllerOperation{}, errors.New(
			"stored controller operation request hash is invalid",
		)
	}
	if len(operation.ResultJSON) != 0 {
		result, decodeErr := decodePRDevelopmentOperationResult(operation.ResultJSON)
		if decodeErr != nil {
			return PRDevelopmentControllerOperation{}, fmt.Errorf(
				"stored controller operation result is invalid: %w", decodeErr,
			)
		}
		operation.Result = result
		if operation.ResultHash != prDevelopmentOperationPayloadHash(
			"picoclaw-pr-development-operation-result-v1\x00",
			operation.ResultJSON,
		) {
			return PRDevelopmentControllerOperation{}, errors.New(
				"stored controller operation result hash is invalid",
			)
		}
	}
	if err := validateStoredPRDevelopmentControllerOperation(operation); err != nil {
		return PRDevelopmentControllerOperation{}, err
	}
	operation.RequestJSON = bytes.Clone(operation.RequestJSON)
	operation.ResultJSON = bytes.Clone(operation.ResultJSON)
	return operation, nil
}

func validateStoredPRDevelopmentControllerOperation(
	operation PRDevelopmentControllerOperation,
) error {
	if !validPrefixedHexID(operation.ID, prDevelopmentOperationIDPrefix) ||
		!validPrefixedHexID(operation.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(operation.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		operation.Ordinal < 0 || operation.Ordinal >= MaxPRDevelopmentControllerOperations ||
		operation.PreparedControllerRevision < 1 ||
		operation.PreparedControllerRevision > MaxPRDevelopmentControllerRevision ||
		!validPRDevelopmentRepairAgentID(operation.AgentID) ||
		!validPRDevelopmentRepairIdentity(
			operation.WorkspaceID, MaxPRDevelopmentControllerIdentityBytes,
		) || !validPrefixedHexID(operation.LineID, prDevelopmentLineIDPrefix) ||
		!validPRDevelopmentRepairCloneURL(operation.SourceCloneURL) ||
		!validPRDevelopmentGitRef(operation.SourceRef) ||
		!validPRDevelopmentHex(operation.SourceCommit, 40, 64) ||
		operation.LineVersion < 0 ||
		operation.LineVersion > MaxPRDevelopmentControllerFences ||
		operation.MutationEpoch < 0 ||
		operation.MutationEpoch > MaxPRDevelopmentControllerFences+1 ||
		!validPRDevelopmentHex(operation.MutationReservationDigest, sha256.Size*2) ||
		operation.MutationLeaseEpoch < 1 ||
		!validPRDevelopmentHex(operation.MutationLeaseTokenDigest, sha256.Size*2) ||
		operation.StageAuthorizationDigest != operation.MutationLeaseTokenDigest ||
		!validPRDevelopmentHex(operation.RequestHash, sha256.Size*2) ||
		!validPRDevelopmentHex(operation.PreviousHash, sha256.Size*2) ||
		operation.IntentHash != hashPRDevelopmentOperationIntent(operation) ||
		validateDBTimestamp("operation creation time", operation.CreatedAt) != nil ||
		validateDBTimestamp("operation update time", operation.UpdatedAt) != nil ||
		operation.UpdatedAt.Before(operation.CreatedAt) {
		return errors.New("stored controller operation header is invalid")
	}
	if operation.SourceTree != "" && !validSameWidthPRDevelopmentOIDs(
		operation.SourceCommit, operation.SourceTree,
	) {
		return errors.New("stored controller operation source tree is invalid")
	}
	bound := operation.SourceTree != ""
	if bound != (operation.TipCommit != "") || bound != (operation.Tree != "") {
		return errors.New("stored controller operation line fence is partial")
	}
	if bound && (!validSameWidthPRDevelopmentOIDs(
		operation.SourceCommit, operation.SourceTree, operation.TipCommit, operation.Tree,
	) || operation.MutationEpoch < operation.LineVersion ||
		operation.MutationEpoch > operation.LineVersion+1) {
		return errors.New("stored controller operation line fence is invalid")
	}
	if operation.Kind != PRDevelopmentControllerOperationAdopt &&
		operation.Kind != PRDevelopmentControllerOperationResume &&
		operation.Kind != PRDevelopmentControllerOperationCommit &&
		operation.Kind != PRDevelopmentControllerOperationPark {
		return errors.New("stored controller operation kind is invalid")
	}
	if operation.Kind == PRDevelopmentControllerOperationCommit {
		if !validPrefixedHexID(operation.EffectIntentID, prDevelopmentCommitIntentIDPrefix) {
			return errors.New("stored commit operation intent is invalid")
		}
	} else if operation.Kind == PRDevelopmentControllerOperationPark {
		if !validPrefixedHexID(operation.EffectIntentID, prDevelopmentParkIntentIDPrefix) {
			return errors.New("stored park operation intent is invalid")
		}
	} else if operation.EffectIntentID != "" {
		return errors.New("stored line transition has an unexpected effect intent")
	}

	if operation.RecoveryStagedAt == nil {
		if operation.RecoveryHash != "" {
			return errors.New("stored controller operation has an unstaged recovery hash")
		}
	} else if operation.RecoveryLeaseUntil == nil ||
		operation.RecoveryHash != hashPRDevelopmentOperationRecovery(operation) ||
		validateDBTimestamp("operation recovery lease deadline", *operation.RecoveryLeaseUntil) != nil ||
		validateDBTimestamp("operation recovery stage time", *operation.RecoveryStagedAt) != nil {
		return errors.New("stored controller operation recovery evidence is invalid")
	}
	for name, value := range map[string]*time.Time{
		"claim deadline":              operation.ClaimUntil,
		"claim time":                  operation.ClaimedAt,
		"new mutation lease deadline": operation.NewMutationLeaseUntil,
		"finalization time":           operation.FinalizedAt,
	} {
		if value != nil && validateDBTimestamp("operation "+name, *value) != nil {
			return errors.New("stored controller operation timestamp is invalid")
		}
	}
	if err := validateStoredPRDevelopmentControllerOperationLifecycle(operation); err != nil {
		return err
	}
	if operation.Status == PRDevelopmentControllerOperationFinalized {
		normalized, err := normalizePRDevelopmentControllerOperationResultForRecovery(
			operation.Kind,
			operation,
			operation.Result,
			operation.RecoveryStagedAt != nil,
		)
		if err != nil {
			return fmt.Errorf("stored controller operation result shape is invalid: %w", err)
		}
		canonical, resultHash, err := encodePRDevelopmentOperationResult(normalized)
		if err != nil || !bytes.Equal(canonical, operation.ResultJSON) ||
			resultHash != operation.ResultHash || operation.FinalizedAt == nil ||
			operation.FinalHash != hashPRDevelopmentOperationFinal(operation) {
			return errors.New("stored controller operation final evidence is invalid")
		}
	} else if len(operation.ResultJSON) != 0 || operation.ResultHash != "" ||
		operation.FinalizedAt != nil || operation.FinalHash != "" {
		return errors.New("stored unfinished controller operation has final evidence")
	}
	return nil
}

func validateStoredPRDevelopmentControllerOperationLifecycle(
	operation PRDevelopmentControllerOperation,
) error {
	staged := operation.RecoveryStagedAt != nil
	if !staged {
		if operation.RecoveryID != "" || operation.ReplacementReservationKey != "" ||
			operation.ReplacementReservationDigest != "" || operation.RecoveryRevision != 0 ||
			operation.ExpiredControllerRevision != 0 || operation.ExpiredLeaseEpoch != 0 ||
			operation.ExpiredLeaseTokenDigest != "" || operation.RecoveryLeaseUntil != nil ||
			operation.RecoveryHash != "" {
			return errors.New("stored unstaged operation has recovery evidence")
		}
	} else {
		if operation.RecoveryRevision != operation.PreparedControllerRevision+1 ||
			operation.ExpiredControllerRevision != operation.PreparedControllerRevision ||
			operation.ExpiredLeaseEpoch != operation.MutationLeaseEpoch ||
			operation.ExpiredLeaseTokenDigest != operation.MutationLeaseTokenDigest ||
			operation.RecoveryLeaseUntil == nil || operation.RecoveryStagedAt == nil ||
			operation.RecoveryLeaseUntil.After(*operation.RecoveryStagedAt) ||
			operation.RecoveryStagedAt.Before(operation.CreatedAt) {
			return errors.New("stored operation recovery stage is unreachable")
		}
		if operation.Kind == PRDevelopmentControllerOperationPark {
			if operation.RecoveryID != "" || operation.ReplacementReservationKey != "" ||
				operation.ReplacementReservationDigest != "" {
				return errors.New("stored Park recovery has replacement authority")
			}
		} else if !validPrefixedHexID(
			operation.RecoveryID,
			prDevelopmentRecoveryIntentIDPrefix,
		) || !validPRDevelopmentHex(
			operation.ReplacementReservationDigest,
			sha256.Size*2,
		) || operation.ReplacementReservationDigest == operation.MutationReservationDigest {
			return errors.New("stored operation recovery replacement is invalid")
		}
		if operation.Kind != PRDevelopmentControllerOperationPark {
			if operation.Status == PRDevelopmentControllerOperationFinalized {
				if operation.ReplacementReservationKey != "" {
					return errors.New("stored finalized recovery retained its replacement bearer")
				}
			} else if !validPrefixedHexID(
				operation.ReplacementReservationKey,
				prDevelopmentControllerKeyPrefix,
			) || prDevelopmentMutationReservationDigest(
				operation.ReplacementReservationKey,
			) != operation.ReplacementReservationDigest {
				return errors.New("stored unresolved recovery replacement bearer is invalid")
			}
		}
	}

	claimed := operation.Status == PRDevelopmentControllerOperationRecoveryClaimed
	finalRecovered := operation.Status == PRDevelopmentControllerOperationFinalized && staged
	if claimed || finalRecovered {
		if !validPRDevelopmentRepairIdentity(
			operation.ClaimID,
			MaxPRDevelopmentControllerIdentityBytes,
		) || !validPRDevelopmentRepairIdentity(
			operation.ClaimOwner,
			MaxPRDevelopmentControllerIdentityBytes,
		) || operation.ClaimEpoch < 1 || int64(operation.Claims) != operation.ClaimEpoch ||
			operation.ClaimedAt == nil || operation.ClaimedAt.Before(*operation.RecoveryStagedAt) {
			return errors.New("stored operation recovery claim evidence is invalid")
		}
	} else if operation.ClaimID != "" || operation.ClaimOwner != "" ||
		operation.ClaimEpoch != 0 || operation.Claims != 0 || operation.ClaimedAt != nil {
		return errors.New("stored unclaimed operation has claim evidence")
	}
	if claimed {
		if !validPRDevelopmentRepairIdentity(
			operation.ClaimToken,
			prDevelopmentControllerLeaseTokenBytes,
		) || operation.ClaimUntil == nil ||
			!operation.ClaimUntil.After(operation.UpdatedAt) ||
			operation.RecoveryClaimTokenDigest != "" {
			return errors.New("stored live operation recovery claim is invalid")
		}
	} else if operation.ClaimToken != "" || operation.ClaimUntil != nil {
		return errors.New("stored operation retains an inactive recovery claim token")
	}

	switch operation.Status {
	case PRDevelopmentControllerOperationPending:
		if staged {
			return errors.New("stored pending operation already staged recovery")
		}
	case PRDevelopmentControllerOperationRecoveryPending:
		if !staged {
			return errors.New("stored recovery-pending operation has no recovery stage")
		}
	case PRDevelopmentControllerOperationRecoveryClaimed:
		if !staged {
			return errors.New("stored recovery-claimed operation has no recovery stage")
		}
	case PRDevelopmentControllerOperationFinalized:
		if operation.FinalizedAt == nil || operation.FinalizedAt.Before(operation.CreatedAt) ||
			(operation.ClaimedAt != nil && operation.FinalizedAt.Before(*operation.ClaimedAt)) {
			return errors.New("stored finalized operation time is invalid")
		}
		if staged {
			if operation.ReplacementReservationKey != "" ||
				!validPRDevelopmentHex(
					operation.RecoveryClaimTokenDigest,
					sha256.Size*2,
				) {
				return errors.New("stored recovered operation retained raw authority")
			}
			expectedRevision := operation.RecoveryRevision + 1
			if operation.Kind == PRDevelopmentControllerOperationAdopt ||
				operation.Kind == PRDevelopmentControllerOperationResume {
				expectedRevision++
			} else if operation.Kind == PRDevelopmentControllerOperationPark {
				expectedRevision = operation.RecoveryRevision
			}
			if operation.FinalControllerRevision != expectedRevision {
				return errors.New("stored recovered operation revision is unreachable")
			}
			if operation.Kind == PRDevelopmentControllerOperationPark {
				if operation.RotationResultHash != "" ||
					operation.NewMutationLeaseEpoch != 0 ||
					operation.NewMutationLeaseTokenDigest != "" ||
					operation.NewMutationLeaseUntil != nil {
					return errors.New("stored recovered Park issued mutation authority")
				}
			} else if !validPRDevelopmentHex(
				operation.RotationResultHash,
				sha256.Size*2,
			) || operation.NewMutationLeaseEpoch != operation.ExpiredLeaseEpoch+1 ||
				!validPRDevelopmentHex(
					operation.NewMutationLeaseTokenDigest,
					sha256.Size*2,
				) || operation.NewMutationLeaseUntil == nil ||
				!operation.NewMutationLeaseUntil.After(*operation.FinalizedAt) {
				return errors.New("stored recovered mutation authority is invalid")
			}
		} else {
			expectedRevision := operation.PreparedControllerRevision
			if operation.Kind != PRDevelopmentControllerOperationCommit {
				expectedRevision++
			}
			if operation.FinalControllerRevision != expectedRevision ||
				operation.RotationResultHash != "" ||
				operation.RecoveryClaimTokenDigest != "" ||
				operation.NewMutationLeaseEpoch != 0 ||
				operation.NewMutationLeaseTokenDigest != "" ||
				operation.NewMutationLeaseUntil != nil {
				return errors.New("stored normal operation final state is invalid")
			}
		}
	default:
		return errors.New("stored controller operation status is invalid")
	}
	if operation.Status != PRDevelopmentControllerOperationFinalized {
		if operation.RotationResultHash != "" || operation.NewMutationLeaseEpoch != 0 ||
			operation.NewMutationLeaseTokenDigest != "" ||
			operation.NewMutationLeaseUntil != nil ||
			operation.RecoveryClaimTokenDigest != "" ||
			operation.FinalControllerRevision != 0 ||
			operation.FinalControllerPhase != "" || operation.FinalFenceHash != "" {
			return errors.New("stored unfinished operation has final authority proof")
		}
		return nil
	}
	if operation.Kind == PRDevelopmentControllerOperationPark {
		if operation.FinalControllerPhase != PRDevelopmentControllerReviewPending ||
			!validPRDevelopmentHex(operation.FinalFenceHash, sha256.Size*2) {
			return errors.New("stored finalized Park review state is invalid")
		}
	} else if operation.FinalControllerPhase != PRDevelopmentControllerMutation ||
		operation.FinalFenceHash != "" {
		return errors.New("stored finalized mutation operation phase is invalid")
	}
	return nil
}

// PreparePRDevelopmentControllerOperation durably binds one exact local Git
// request to the current mutation lease before the caller performs that
// request. The store itself performs no filesystem or Git work.
func (s *Store) PreparePRDevelopmentControllerOperation(
	ctx context.Context,
	input PRDevelopmentControllerOperationPrepare,
) (PRDevelopmentControllerOperation, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentControllerOperation{}, false, err
	}
	normalized, err := normalizePRDevelopmentControllerOperationPrepare(input)
	if err != nil {
		return PRDevelopmentControllerOperation{}, false, err
	}
	var (
		operation        PRDevelopmentControllerOperation
		changed          bool
		recoveryRequired bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		session, loadErr := loadPRDevelopmentRepairSessionByAttempt(
			ctx, conn, normalized.AttemptID,
		)
		if loadErr != nil {
			return loadErr
		}
		relation, relationErr := loadPRDevelopmentControllerAttemptRelation(
			ctx, conn, session.CaseID, normalized.AttemptID,
		)
		if relationErr != nil {
			return relationErr
		}
		controller, found, controllerErr := loadPRDevelopmentControllerAggregateByID(
			ctx, conn, normalized.ControllerID,
		)
		if controllerErr != nil {
			return controllerErr
		}
		if !found {
			return sql.ErrNoRows
		}
		if controller.ThreadID != relation.Thread.ID ||
			controller.OwnerSessionID != relation.Session.ID ||
			controller.CurrentAttemptID != normalized.AttemptID ||
			controller.AgentID != relation.Session.AgentID {
			return fmt.Errorf(
				"%w: operation attempt is not the controller owner",
				ErrPRDevelopmentControllerConflict,
			)
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if timeErr := requireNonRegressingPRDevelopmentControllerTime(
			now,
			maxPRDevelopmentControllerTime(
				controller.UpdatedAt, relation.Session.UpdatedAt, relation.Attempt.UpdatedAt,
			),
		); timeErr != nil {
			return timeErr
		}
		if controller.Phase == PRDevelopmentControllerMutation &&
			controller.LeaseUntil != nil && !controller.LeaseUntil.After(now) {
			if controller.LeaseKind != PRDevelopmentControllerMutationLease ||
				controller.LeaseToken != normalized.LeaseToken ||
				controller.LeaseEpoch != normalized.LeaseEpoch ||
				controller.Revision != normalized.ExpectedRevision {
				return fmt.Errorf(
					"%w: expired operation does not hold the exact mutation lease",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if expireErr := expirePRDevelopmentMutationLease(
				ctx, conn, controller, now,
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

		operations, operationsErr := loadPRDevelopmentControllerOperations(
			ctx, conn, controller.ID,
		)
		if operationsErr != nil {
			return operationsErr
		}
		expectedRequest, requestErr := normalizePRDevelopmentControllerOperationRequest(
			normalized.Kind,
			controller,
			relation.Session,
			operations,
			normalized.Request,
		)
		if requestErr != nil {
			return requestErr
		}
		requestJSON, requestHash, encodeErr := encodePRDevelopmentOperationRequest(
			expectedRequest,
		)
		if encodeErr != nil {
			return encodeErr
		}
		providedJSON, _, encodeErr := encodePRDevelopmentOperationRequest(
			normalized.Request,
		)
		if encodeErr != nil {
			return encodeErr
		}
		if !bytes.Equal(providedJSON, requestJSON) {
			return fmt.Errorf(
				"%w: operation request differs from the canonical controller fence",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if orchestrationErr := preflightPRDevelopmentRepairOrchestrationOperation(
			ctx,
			conn,
			controller,
			relation,
			operations,
			normalized.Kind,
			expectedRequest,
		); orchestrationErr != nil {
			return orchestrationErr
		}

		if existing, exists, existingErr := loadPRDevelopmentControllerOperationByID(
			ctx, conn, normalized.OperationID,
		); existingErr != nil {
			return existingErr
		} else if exists {
			finalizedCommitReplay := existing.Kind == PRDevelopmentControllerOperationCommit &&
				existing.Status == PRDevelopmentControllerOperationFinalized
			if existing.ControllerID != controller.ID ||
				existing.AttemptID != normalized.AttemptID ||
				existing.Kind != normalized.Kind ||
				(existing.Status != PRDevelopmentControllerOperationPending &&
					!finalizedCommitReplay) ||
				existing.PreparedControllerRevision != normalized.ExpectedRevision ||
				existing.MutationLeaseEpoch != normalized.LeaseEpoch ||
				existing.MutationLeaseTokenDigest != prDevelopmentLeaseTokenDigest(
					PRDevelopmentControllerMutationLease, normalized.LeaseToken,
				) || !bytes.Equal(existing.RequestJSON, requestJSON) ||
				len(operations) == 0 || operations[len(operations)-1].ID != existing.ID {
				return fmt.Errorf(
					"%w: operation ID is bound to different or non-current evidence",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if finalizedCommitReplay {
				_, expired, replayErr := requireImmediatePRDevelopmentOperationReplay(
					ctx, conn, controller, existing, normalized.LeaseToken, now,
				)
				if replayErr != nil {
					return replayErr
				}
				if expired {
					recoveryRequired = true
					return nil
				}
			}
			operation = existing
			return nil
		}
		if len(operations) >= MaxPRDevelopmentControllerOperations {
			return fmt.Errorf(
				"%w: controller operation capacity exhausted",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if active, activeFound, activeErr := loadActivePRDevelopmentControllerOperation(
			ctx, conn, controller.ID,
		); activeErr != nil {
			return activeErr
		} else if activeFound {
			_ = active
			return ErrPRDevelopmentControllerActive
		}
		if orderErr := requirePRDevelopmentControllerOperationOrder(
			controller, operations, normalized.AttemptID, normalized.Kind,
		); orderErr != nil {
			return orderErr
		}
		if normalized.Kind != PRDevelopmentControllerOperationPark {
			var recoveryAuditCount int
			if countErr := conn.QueryRowContext(ctx, `
				SELECT COUNT(*)
				FROM pr_development_controller_recovery_intents
				WHERE controller_id = ?`,
				controller.ID,
			).Scan(&recoveryAuditCount); countErr != nil {
				return countErr
			}
			if recoveryAuditCount >= MaxPRDevelopmentControllerRecoveries {
				return fmt.Errorf(
					"%w: controller recovery audit capacity is unavailable",
					ErrPRDevelopmentControllerConflict,
				)
			}
		}
		if headroomErr := requirePRDevelopmentControllerOperationHeadroom(
			controller, normalized.Kind,
		); headroomErr != nil {
			return headroomErr
		}

		previousHash := emptyPRDevelopmentOperationDigest()
		if len(operations) > 0 {
			previousHash = operations[len(operations)-1].FinalHash
		}
		operation = PRDevelopmentControllerOperation{
			ID:                         normalized.OperationID,
			ControllerID:               controller.ID,
			AttemptID:                  normalized.AttemptID,
			Ordinal:                    len(operations),
			Kind:                       normalized.Kind,
			Status:                     PRDevelopmentControllerOperationPending,
			PreparedControllerRevision: controller.Revision,
			AgentID:                    controller.AgentID,
			WorkspaceID:                relation.Session.WorkspaceID,
			LineID:                     controller.LineID,
			SourceCloneURL:             relation.Session.CloneURL,
			SourceRef:                  relation.Session.HeadRef,
			SourceCommit:               relation.Session.HeadSHA,
			SourceTree:                 controller.SourceTree,
			LineVersion:                controller.LineVersion,
			MutationEpoch:              controller.MutationEpoch,
			TipCommit:                  controller.TipCommit,
			Tree:                       controller.Tree,
			MutationReservationDigest: prDevelopmentMutationReservationDigest(
				controller.MutationReservationKey,
			),
			MutationLeaseEpoch: controller.LeaseEpoch,
			MutationLeaseTokenDigest: prDevelopmentLeaseTokenDigest(
				PRDevelopmentControllerMutationLease, controller.LeaseToken,
			),
			EffectIntentID: expectedRequest.EffectIntentID,
			Request:        expectedRequest,
			RequestJSON:    requestJSON,
			RequestHash:    requestHash,
			PreviousHash:   previousHash,
			StageAuthorizationDigest: prDevelopmentLeaseTokenDigest(
				PRDevelopmentControllerMutationLease, controller.LeaseToken,
			),
			CreatedAt: now,
			UpdatedAt: now,
		}
		if normalized.Kind == PRDevelopmentControllerOperationAdopt {
			operation.SourceTree = expectedRequest.ExpectedTree
			operation.TipCommit = operation.SourceCommit
			operation.Tree = operation.SourceTree
		}
		operation.IntentHash = hashPRDevelopmentOperationIntent(operation)
		_, insertErr := conn.ExecContext(ctx, `
			INSERT INTO pr_development_controller_operation_intents (
				id, controller_id, attempt_id, ordinal, kind, status,
				prepared_controller_revision, agent_id, workspace_id, line_id,
				source_clone_url, source_ref, source_commit, source_tree,
				line_version, mutation_epoch, tip_commit, tree,
				mutation_reservation_digest, mutation_lease_epoch,
				mutation_lease_token_digest, effect_intent_id, request_json,
				request_hash, previous_hash, intent_hash,
				stage_authorization_digest, created_at, updated_at
			) VALUES (
				?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
				?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			)`,
			operation.ID,
			operation.ControllerID,
			operation.AttemptID,
			operation.Ordinal,
			operation.Kind,
			operation.PreparedControllerRevision,
			operation.AgentID,
			operation.WorkspaceID,
			operation.LineID,
			operation.SourceCloneURL,
			operation.SourceRef,
			operation.SourceCommit,
			operation.SourceTree,
			operation.LineVersion,
			operation.MutationEpoch,
			operation.TipCommit,
			operation.Tree,
			operation.MutationReservationDigest,
			operation.MutationLeaseEpoch,
			operation.MutationLeaseTokenDigest,
			operation.EffectIntentID,
			operation.RequestJSON,
			operation.RequestHash,
			operation.PreviousHash,
			operation.IntentHash,
			operation.StageAuthorizationDigest,
			toDBTime(now),
			toDBTime(now),
		)
		if insertErr != nil {
			return insertErr
		}
		loaded, loadedFound, reloadErr := loadPRDevelopmentControllerOperationByID(
			ctx, conn, operation.ID,
		)
		if reloadErr != nil {
			return reloadErr
		}
		if !loadedFound {
			return errors.New("prepared controller operation disappeared")
		}
		operation = loaded
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentControllerOperation{}, false, fmt.Errorf(
			"prepare pull request development controller operation: %w",
			s.dbError(err),
		)
	}
	if recoveryRequired {
		return PRDevelopmentControllerOperation{}, false, fmt.Errorf(
			"prepare pull request development controller operation: %w",
			ErrPRDevelopmentControllerRecoveryRequired,
		)
	}
	return operation, changed, nil
}

func normalizePRDevelopmentControllerOperationPrepare(
	input PRDevelopmentControllerOperationPrepare,
) (PRDevelopmentControllerOperationPrepare, error) {
	input.OperationID = strings.TrimSpace(input.OperationID)
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	leaseToken, err := normalizePRDevelopmentControllerIdentity(
		"lease token", input.LeaseToken, prDevelopmentControllerLeaseTokenBytes, true,
	)
	if err != nil {
		return PRDevelopmentControllerOperationPrepare{}, err
	}
	input.LeaseToken = leaseToken
	if !validPrefixedHexID(input.OperationID, prDevelopmentOperationIDPrefix) ||
		!validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		input.ExpectedRevision < 1 ||
		input.ExpectedRevision > MaxPRDevelopmentControllerRevision ||
		input.LeaseEpoch < 1 ||
		(input.Kind != PRDevelopmentControllerOperationAdopt &&
			input.Kind != PRDevelopmentControllerOperationResume &&
			input.Kind != PRDevelopmentControllerOperationCommit &&
			input.Kind != PRDevelopmentControllerOperationPark) {
		return PRDevelopmentControllerOperationPrepare{}, fmt.Errorf(
			"%w: valid operation, controller, attempt, revision, lease, and kind are required",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func normalizePRDevelopmentControllerOperationRequest(
	kind PRDevelopmentControllerOperationKind,
	controller PRDevelopmentController,
	session PRDevelopmentRepairSession,
	operations []PRDevelopmentControllerOperation,
	provided PRDevelopmentControllerOperationRequest,
) (PRDevelopmentControllerOperationRequest, error) {
	if session.WorkspaceID == "" || session.HeadRepository == "" ||
		session.CloneURL == "" || session.HeadRef == "" || session.HeadSHA == "" ||
		session.AgentID != controller.AgentID {
		return PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
			"%w: controller owner pin is incomplete",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if controller.WorkspaceID != "" &&
		(controller.WorkspaceID != session.WorkspaceID ||
			controller.SourceCloneURL != session.CloneURL ||
			controller.SourceRef != session.HeadRef ||
			controller.SourceCommit != session.HeadSHA) {
		return PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
			"%w: controller source differs from its owner pin",
			ErrPRDevelopmentControllerConflict,
		)
	}
	base := PRDevelopmentControllerOperationRequest{
		Repository:   session.HeadRepository,
		SourceRef:    session.HeadRef,
		SourceCommit: session.HeadSHA,
		AgentID:      controller.AgentID,
		WorkspaceID:  session.WorkspaceID,
		LineID:       controller.LineID,
	}
	switch kind {
	case PRDevelopmentControllerOperationAdopt:
		provided.ExpectedTree = strings.TrimSpace(provided.ExpectedTree)
		if controller.WorkspaceID != "" || controller.LineVersion != 0 ||
			controller.MutationEpoch != 0 ||
			!validSameWidthPRDevelopmentOIDs(session.HeadSHA, provided.ExpectedTree) {
			return PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
				"%w: Adopt requires the unbound source-version fence and exact source tree",
				ErrPRDevelopmentControllerConflict,
			)
		}
		base.ExpectedTree = provided.ExpectedTree
	case PRDevelopmentControllerOperationResume:
		if controller.WorkspaceID == "" ||
			controller.MutationEpoch != controller.LineVersion {
			return PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
				"%w: Resume requires the exact parked line fence",
				ErrPRDevelopmentControllerConflict,
			)
		}
		base.ExpectedVersion = controller.LineVersion
		base.ExpectedEpoch = controller.MutationEpoch
		base.ExpectedTip = controller.TipCommit
		base.ExpectedTree = controller.Tree
	case PRDevelopmentControllerOperationCommit:
		if controller.WorkspaceID == "" ||
			controller.MutationEpoch != controller.LineVersion+1 {
			return PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
				"%w: Commit requires an active bound mutation epoch",
				ErrPRDevelopmentControllerConflict,
			)
		}
		provided.ExpectedTree = strings.TrimSpace(provided.ExpectedTree)
		provided.CandidateDigest = strings.TrimSpace(provided.CandidateDigest)
		provided.CommitMessage = strings.TrimSpace(provided.CommitMessage)
		provided.EffectIntentID = strings.TrimSpace(provided.EffectIntentID)
		_, authoredOffset := provided.AuthoredAt.Zone()
		if !validSameWidthPRDevelopmentOIDs(controller.TipCommit, provided.ExpectedTree) ||
			controller.Tree == provided.ExpectedTree ||
			!validPRDevelopmentHex(provided.CandidateDigest, sha256.Size*2) ||
			!validPrefixedHexID(
				provided.EffectIntentID, prDevelopmentCommitIntentIDPrefix,
			) || !validPRDevelopmentRepairIdentity(
			provided.CommitMessage, prDevelopmentOperationCommitMessageBytes,
		) || provided.AuthoredAt.IsZero() || authoredOffset != 0 ||
			provided.AuthoredAt.Nanosecond() != 0 ||
			validateDBTimestamp("operation authored time", provided.AuthoredAt) != nil {
			return PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
				"%w: Commit candidate, intent, message, or authored time is invalid",
				ErrInvalidPRDevelopmentController,
			)
		}
		base.EffectIntentID = provided.EffectIntentID
		base.ExpectedParent = controller.TipCommit
		base.ExpectedTree = provided.ExpectedTree
		base.CandidateDigest = provided.CandidateDigest
		base.CommitMessage = provided.CommitMessage
		base.AuthoredAt = provided.AuthoredAt.UTC()
	case PRDevelopmentControllerOperationPark:
		if controller.WorkspaceID == "" ||
			controller.MutationEpoch != controller.LineVersion+1 {
			return PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
				"%w: Park requires an active bound mutation epoch",
				ErrPRDevelopmentControllerConflict,
			)
		}
		provided.EffectIntentID = strings.TrimSpace(provided.EffectIntentID)
		provided.CompletionSummary = strings.TrimSpace(provided.CompletionSummary)
		if !validPrefixedHexID(provided.EffectIntentID, prDevelopmentParkIntentIDPrefix) ||
			!validStoredPRDevelopmentRepairText(
				provided.CompletionSummary, MaxPRDevelopmentRepairSummaryBytes,
			) || provided.CompletionIterations < 1 ||
			provided.CompletionIterations > MaxPRDevelopmentRepairIterations {
			return PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
				"%w: Park intent and completion are invalid",
				ErrInvalidPRDevelopmentController,
			)
		}
		if len(session.Attempts) == 0 ||
			session.Attempts[len(session.Attempts)-1].ID != controller.CurrentAttemptID {
			return PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
				"%w: Park attempt is not owner-session latest",
				ErrPRDevelopmentControllerConflict,
			)
		}
		attempt := session.Attempts[len(session.Attempts)-1]
		if attempt.Status != PRDevelopmentRepairQueued &&
			attempt.Status != PRDevelopmentRepairCompleted {
			return PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
				"%w: Park requires a queued or completed owner attempt",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if attempt.Status == PRDevelopmentRepairCompleted &&
			(attempt.Summary != provided.CompletionSummary ||
				attempt.Iterations != provided.CompletionIterations) {
			return PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
				"%w: Park completion differs from the existing completed attempt",
				ErrPRDevelopmentControllerConflict,
			)
		}
		latestAttempt := session.Attempts[len(session.Attempts)-1]
		if latestAttempt.ID != controller.CurrentAttemptID {
			return PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
				"%w: Park attempt is not owner-session latest",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if latestAttempt.Status == PRDevelopmentRepairCompleted {
			if provided.CompletionSummary != latestAttempt.Summary ||
				provided.CompletionIterations != latestAttempt.Iterations {
				return PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
					"%w: Park completion differs from the existing terminal attempt",
					ErrPRDevelopmentControllerConflict,
				)
			}
		} else if latestAttempt.Status != PRDevelopmentRepairQueued {
			return PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
				"%w: Park requires the queued or completed latest attempt",
				ErrPRDevelopmentControllerConflict,
			)
		}
		base.EffectIntentID = provided.EffectIntentID
		base.ExpectedVersion = controller.LineVersion
		base.MutationEpoch = controller.MutationEpoch
		base.PreviousTip = controller.TipCommit
		base.CompletionSummary = provided.CompletionSummary
		base.CompletionIterations = provided.CompletionIterations
		commit, committed := finalizedPRDevelopmentCommitOperation(
			operations, controller.CurrentAttemptID,
		)
		if committed {
			base.Tip = commit.Result.Commit
			base.Tree = commit.Result.Tree
			base.NoChanges = false
		} else {
			base.Tip = controller.TipCommit
			base.Tree = controller.Tree
			base.NoChanges = true
		}
	default:
		return PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
			"%w: unknown operation kind", ErrInvalidPRDevelopmentController,
		)
	}
	return base, nil
}

func finalizedPRDevelopmentCommitOperation(
	operations []PRDevelopmentControllerOperation,
	attemptID string,
) (PRDevelopmentControllerOperation, bool) {
	for index := len(operations) - 1; index >= 0; index-- {
		operation := operations[index]
		if operation.AttemptID == attemptID &&
			operation.Kind == PRDevelopmentControllerOperationCommit &&
			operation.Status == PRDevelopmentControllerOperationFinalized {
			return operation, true
		}
	}
	return PRDevelopmentControllerOperation{}, false
}

func requirePRDevelopmentControllerOperationOrder(
	controller PRDevelopmentController,
	operations []PRDevelopmentControllerOperation,
	attemptID string,
	kind PRDevelopmentControllerOperationKind,
) error {
	current := make([]PRDevelopmentControllerOperation, 0, 3)
	for _, operation := range operations {
		if operation.AttemptID == attemptID {
			current = append(current, operation)
		}
	}
	valid := false
	switch len(current) {
	case 0:
		legacyBoundHighWater := len(operations) == 0 && controller.WorkspaceID != "" &&
			controller.MutationEpoch == controller.LineVersion+1
		valid = (controller.WorkspaceID == "" && kind == PRDevelopmentControllerOperationAdopt) ||
			(controller.WorkspaceID != "" &&
				controller.MutationEpoch == controller.LineVersion &&
				kind == PRDevelopmentControllerOperationResume) ||
			(legacyBoundHighWater &&
				(kind == PRDevelopmentControllerOperationCommit ||
					kind == PRDevelopmentControllerOperationPark))
	case 1:
		valid = current[0].Status == PRDevelopmentControllerOperationFinalized &&
			((current[0].Kind == PRDevelopmentControllerOperationAdopt ||
				current[0].Kind == PRDevelopmentControllerOperationResume) &&
				(kind == PRDevelopmentControllerOperationCommit ||
					kind == PRDevelopmentControllerOperationPark) ||
				current[0].Ordinal == 0 &&
					current[0].Kind == PRDevelopmentControllerOperationCommit &&
					kind == PRDevelopmentControllerOperationPark)
	case 2:
		valid = current[0].Status == PRDevelopmentControllerOperationFinalized &&
			current[1].Status == PRDevelopmentControllerOperationFinalized &&
			(current[0].Kind == PRDevelopmentControllerOperationAdopt ||
				current[0].Kind == PRDevelopmentControllerOperationResume) &&
			current[1].Kind == PRDevelopmentControllerOperationCommit &&
			kind == PRDevelopmentControllerOperationPark
	}
	if !valid {
		return fmt.Errorf(
			"%w: operation kind is not the next causal step for this attempt",
			ErrPRDevelopmentControllerConflict,
		)
	}
	return nil
}

func requirePRDevelopmentControllerOperationHeadroom(
	controller PRDevelopmentController,
	kind PRDevelopmentControllerOperationKind,
) error {
	leaseEpochHeadroom := int64(1) // later reservation-free review claim
	if kind != PRDevelopmentControllerOperationPark {
		leaseEpochHeadroom++ // worst-case operation recovery rotation
	}
	if controller.LeaseEpoch > int64(^uint64(0)>>1)-leaseEpochHeadroom {
		return fmt.Errorf(
			"%w: operation lacks recovery lease-epoch headroom",
			ErrPRDevelopmentControllerConflict,
		)
	}
	required := int64(2) // Park plus later review finish.
	switch kind {
	case PRDevelopmentControllerOperationAdopt,
		PRDevelopmentControllerOperationResume:
		required = 5 // expiry, recovery, binding, Park, review
	case PRDevelopmentControllerOperationCommit:
		required = 4 // expiry, recovery, Park, review
	}
	if controller.Revision > MaxPRDevelopmentControllerRevision-required {
		return fmt.Errorf(
			"%w: operation lacks controller revision headroom",
			ErrPRDevelopmentControllerConflict,
		)
	}
	return nil
}

// FinalizePRDevelopmentControllerOperation records only a proven successful
// result. Adopt/Resume bind the line, Commit retains exact local evidence, and
// Park commits the complete attempt-to-review handoff atomically.
func (s *Store) FinalizePRDevelopmentControllerOperation(
	ctx context.Context,
	input PRDevelopmentControllerOperationFinalize,
) (PRDevelopmentControllerOperationTransition, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentControllerOperationTransition{}, false, err
	}
	normalized, err := normalizePRDevelopmentControllerOperationFinalize(input)
	if err != nil {
		return PRDevelopmentControllerOperationTransition{}, false, err
	}
	var (
		transition       PRDevelopmentControllerOperationTransition
		changed          bool
		recoveryRequired bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		operation, found, loadErr := loadPRDevelopmentControllerOperationByID(
			ctx, conn, normalized.OperationID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return sql.ErrNoRows
		}
		if operation.ControllerID != normalized.ControllerID ||
			operation.AttemptID != normalized.AttemptID ||
			operation.PreparedControllerRevision != normalized.ExpectedRevision ||
			operation.MutationLeaseEpoch != normalized.LeaseEpoch {
			return fmt.Errorf(
				"%w: operation finalization differs from its prepared fence",
				ErrPRDevelopmentControllerConflict,
			)
		}
		stageDigest := prDevelopmentLeaseTokenDigest(
			PRDevelopmentControllerMutationLease, normalized.LeaseToken,
		)
		if stageDigest != operation.StageAuthorizationDigest {
			return fmt.Errorf(
				"%w: operation finalization has foreign mutation authority",
				ErrPRDevelopmentControllerConflict,
			)
		}
		result, resultErr := normalizePRDevelopmentControllerOperationResult(
			operation.Kind, operation, normalized.Result,
		)
		if resultErr != nil {
			return resultErr
		}
		resultJSON, resultHash, encodeErr := encodePRDevelopmentOperationResult(result)
		if encodeErr != nil {
			return encodeErr
		}
		providedJSON, _, encodeErr := encodePRDevelopmentOperationResult(normalized.Result)
		if encodeErr != nil {
			return encodeErr
		}
		if !bytes.Equal(resultJSON, providedJSON) {
			return fmt.Errorf(
				"%w: operation result differs from its exact effect fence",
				ErrPRDevelopmentControllerConflict,
			)
		}

		controller, controllerFound, controllerErr := loadPRDevelopmentControllerAggregateByID(
			ctx, conn, normalized.ControllerID,
		)
		if controllerErr != nil {
			return controllerErr
		}
		if !controllerFound {
			return sql.ErrNoRows
		}
		operations, operationsErr := loadPRDevelopmentControllerOperations(
			ctx, conn, controller.ID,
		)
		if operationsErr != nil {
			return operationsErr
		}
		if len(operations) == 0 || operations[len(operations)-1].ID != operation.ID {
			return fmt.Errorf(
				"%w: operation finalization is no longer the chain tail",
				ErrPRDevelopmentControllerConflict,
			)
		}
		session, sessionErr := loadPRDevelopmentRepairSessionByAttempt(
			ctx, conn, normalized.AttemptID,
		)
		if sessionErr != nil {
			return sessionErr
		}
		if session.ID != controller.OwnerSessionID || len(session.Attempts) == 0 ||
			session.Attempts[len(session.Attempts)-1].ID != normalized.AttemptID ||
			controller.CurrentAttemptID != normalized.AttemptID {
			return fmt.Errorf(
				"%w: operation attempt is no longer controller-current",
				ErrPRDevelopmentControllerConflict,
			)
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if timeErr := requireNonRegressingPRDevelopmentControllerTime(
			now,
			maxPRDevelopmentControllerTime(
				controller.UpdatedAt, session.UpdatedAt,
				session.Attempts[len(session.Attempts)-1].UpdatedAt,
				operation.UpdatedAt,
			),
		); timeErr != nil {
			return timeErr
		}

		if operation.Status == PRDevelopmentControllerOperationFinalized {
			if operation.ResultHash != resultHash ||
				!bytes.Equal(operation.ResultJSON, resultJSON) {
				return fmt.Errorf(
					"%w: finalized operation is bound to another result",
					ErrPRDevelopmentControllerConflict,
				)
			}
			fence, expired, replayErr := requireImmediatePRDevelopmentOperationReplay(
				ctx, conn, controller, operation, normalized.LeaseToken, now,
			)
			if replayErr != nil {
				return replayErr
			}
			if expired {
				recoveryRequired = true
				return nil
			}
			transition = PRDevelopmentControllerOperationTransition{
				Controller: controller,
				Operation:  operation,
				Fence:      fence,
			}
			return nil
		}
		if operation.Status != PRDevelopmentControllerOperationPending ||
			operation.RecoveryStagedAt != nil {
			return fmt.Errorf(
				"%w: operation is not pending under its original mutation lease",
				ErrPRDevelopmentControllerConflict,
			)
		}
		if controller.Phase == PRDevelopmentControllerMutation &&
			controller.LeaseUntil != nil && !controller.LeaseUntil.After(now) {
			if controller.Revision != operation.PreparedControllerRevision ||
				controller.CurrentAttemptID != operation.AttemptID ||
				controller.LeaseEpoch != operation.MutationLeaseEpoch ||
				controller.LeaseToken != normalized.LeaseToken {
				return fmt.Errorf(
					"%w: expired finalization does not own the prepared lease",
					ErrPRDevelopmentControllerConflict,
				)
			}
			if expireErr := expirePRDevelopmentMutationLease(
				ctx, conn, controller, now,
			); expireErr != nil {
				return expireErr
			}
			recoveryRequired = true
			return nil
		}
		if leaseErr := requireLivePRDevelopmentControllerLease(
			controller,
			operation.AttemptID,
			normalized.LeaseToken,
			operation.MutationLeaseEpoch,
			PRDevelopmentControllerMutation,
			now,
		); leaseErr != nil {
			return leaseErr
		}
		if controller.Revision != operation.PreparedControllerRevision ||
			controller.MutationReservationKey == "" ||
			prDevelopmentMutationReservationDigest(
				controller.MutationReservationKey,
			) != operation.MutationReservationDigest {
			return fmt.Errorf(
				"%w: controller changed after operation prepare",
				ErrPRDevelopmentControllerConflict,
			)
		}

		var fence *PRDevelopmentAttemptReviewFence
		switch operation.Kind {
		case PRDevelopmentControllerOperationAdopt,
			PRDevelopmentControllerOperationResume:
			if bindErr := finalizePRDevelopmentLineOperation(
				ctx, conn, controller, operation, result, now,
			); bindErr != nil {
				return bindErr
			}
		case PRDevelopmentControllerOperationCommit:
			// Commit materializes local content only. Its exact result is retained
			// on the operation; the controller's parked high-water moves at Park.
			// Touch the controller under the same exact authority fence so its
			// aggregate timestamp also covers this finalized operation.
			controllerResult, touchErr := conn.ExecContext(ctx, `
				UPDATE pr_development_thread_controllers
				SET updated_at = ?
				WHERE id = ? AND revision = ? AND phase = 'mutation' AND
					current_attempt_id = ? AND lease_kind = 'mutation' AND
					lease_token = ? AND lease_epoch = ? AND
					mutation_reservation_key = ?`,
				toDBTime(now),
				controller.ID,
				controller.Revision,
				controller.CurrentAttemptID,
				normalized.LeaseToken,
				controller.LeaseEpoch,
				controller.MutationReservationKey,
			)
			if touchErr != nil {
				return touchErr
			}
			if rowErr := requireOnePRDevelopmentControllerRow(controllerResult); rowErr != nil {
				return rowErr
			}
		case PRDevelopmentControllerOperationPark:
			parked, parkErr := finalizePRDevelopmentParkOperation(
				ctx,
				conn,
				controller,
				operation,
				result,
				now,
				operation.PreparedControllerRevision,
			)
			if parkErr != nil {
				return parkErr
			}
			fence = &parked
		default:
			return errors.New("prepared controller operation has an unknown kind")
		}

		finalRevision := controller.Revision
		finalPhase := PRDevelopmentControllerMutation
		finalFenceHash := ""
		if operation.Kind == PRDevelopmentControllerOperationAdopt ||
			operation.Kind == PRDevelopmentControllerOperationResume {
			finalRevision++
		}
		if operation.Kind == PRDevelopmentControllerOperationPark {
			finalRevision++
			finalPhase = PRDevelopmentControllerReviewPending
			finalFenceHash = fence.FenceHash
		}
		operation.Status = PRDevelopmentControllerOperationFinalized
		operation.Result = result
		operation.ResultJSON = resultJSON
		operation.ResultHash = resultHash
		operation.FinalControllerRevision = finalRevision
		operation.FinalControllerPhase = finalPhase
		operation.FinalFenceHash = finalFenceHash
		operation.FinalizedAt = &now
		operation.UpdatedAt = now
		operation.FinalHash = hashPRDevelopmentOperationFinal(operation)
		operationResult, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_controller_operation_intents
			SET status = 'finalized', result_json = ?, result_hash = ?,
				final_controller_revision = ?, final_controller_phase = ?,
				final_fence_hash = ?, final_hash = ?, finalized_at = ?, updated_at = ?
			WHERE id = ? AND controller_id = ? AND attempt_id = ? AND
				status = 'pending' AND prepared_controller_revision = ? AND
				mutation_lease_epoch = ? AND stage_authorization_digest = ?`,
			operation.ResultJSON,
			operation.ResultHash,
			operation.FinalControllerRevision,
			operation.FinalControllerPhase,
			operation.FinalFenceHash,
			operation.FinalHash,
			toDBTime(now),
			toDBTime(now),
			operation.ID,
			operation.ControllerID,
			operation.AttemptID,
			operation.PreparedControllerRevision,
			operation.MutationLeaseEpoch,
			operation.StageAuthorizationDigest,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(operationResult); rowErr != nil {
			return rowErr
		}
		loadedOperation, loadedFound, reloadErr := loadPRDevelopmentControllerOperationByID(
			ctx, conn, operation.ID,
		)
		if reloadErr != nil {
			return reloadErr
		}
		if !loadedFound {
			return errors.New("finalized controller operation disappeared")
		}
		loadedController, loadedControllerFound, reloadErr := loadPRDevelopmentControllerAggregateByID(
			ctx,
			conn,
			controller.ID,
		)
		if reloadErr != nil {
			return reloadErr
		}
		if !loadedControllerFound {
			return errors.New("operation controller disappeared after finalization")
		}
		transition = PRDevelopmentControllerOperationTransition{
			Controller: loadedController,
			Operation:  loadedOperation,
			Fence:      fence,
		}
		changed = true
		return nil
	})
	if err != nil {
		return PRDevelopmentControllerOperationTransition{}, false, fmt.Errorf(
			"finalize pull request development controller operation: %w",
			s.dbError(err),
		)
	}
	if recoveryRequired {
		return PRDevelopmentControllerOperationTransition{}, false, fmt.Errorf(
			"finalize pull request development controller operation: %w",
			ErrPRDevelopmentControllerRecoveryRequired,
		)
	}
	return transition, changed, nil
}

func normalizePRDevelopmentControllerOperationFinalize(
	input PRDevelopmentControllerOperationFinalize,
) (PRDevelopmentControllerOperationFinalize, error) {
	input.ControllerID = strings.TrimSpace(input.ControllerID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	leaseToken, err := normalizePRDevelopmentControllerIdentity(
		"lease token", input.LeaseToken, prDevelopmentControllerLeaseTokenBytes, true,
	)
	if err != nil {
		return PRDevelopmentControllerOperationFinalize{}, err
	}
	input.LeaseToken = leaseToken
	if !validPrefixedHexID(input.ControllerID, prDevelopmentControllerIDPrefix) ||
		!validPrefixedHexID(input.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		!validPrefixedHexID(input.OperationID, prDevelopmentOperationIDPrefix) ||
		input.ExpectedRevision < 1 ||
		input.ExpectedRevision > MaxPRDevelopmentControllerRevision ||
		input.LeaseEpoch < 1 {
		return PRDevelopmentControllerOperationFinalize{}, fmt.Errorf(
			"%w: valid controller, attempt, operation, revision, and lease are required",
			ErrInvalidPRDevelopmentController,
		)
	}
	return input, nil
}

func normalizePRDevelopmentControllerOperationResult(
	kind PRDevelopmentControllerOperationKind,
	operation PRDevelopmentControllerOperation,
	provided PRDevelopmentControllerOperationResult,
) (PRDevelopmentControllerOperationResult, error) {
	return normalizePRDevelopmentControllerOperationResultForRecovery(
		kind,
		operation,
		provided,
		false,
	)
}

func normalizePRDevelopmentControllerOperationResultForRecovery(
	kind PRDevelopmentControllerOperationKind,
	operation PRDevelopmentControllerOperation,
	provided PRDevelopmentControllerOperationResult,
	allowCommitWorkspaceDrift bool,
) (PRDevelopmentControllerOperationResult, error) {
	// Replay markers are deliberately operational and do not enter durable
	// equality or hashes.
	provided.AlreadyOwned = false
	provided.AlreadyApplied = false
	provided.AlreadyParked = false
	var expected PRDevelopmentControllerOperationResult
	switch kind {
	case PRDevelopmentControllerOperationAdopt:
		expected = PRDevelopmentControllerOperationResult{
			WorkspaceID:   operation.WorkspaceID,
			Version:       0,
			MutationEpoch: 1,
			Tip:           operation.SourceCommit,
			Tree:          operation.SourceTree,
		}
	case PRDevelopmentControllerOperationResume:
		expected = PRDevelopmentControllerOperationResult{
			WorkspaceID:   operation.WorkspaceID,
			Version:       operation.LineVersion,
			MutationEpoch: operation.MutationEpoch + 1,
			Tip:           operation.TipCommit,
			Tree:          operation.Tree,
		}
	case PRDevelopmentControllerOperationCommit:
		provided.WorkspaceID = strings.TrimSpace(provided.WorkspaceID)
		provided.IntentID = strings.TrimSpace(provided.IntentID)
		provided.ParentCommit = strings.TrimSpace(provided.ParentCommit)
		provided.CandidateDigest = strings.TrimSpace(provided.CandidateDigest)
		provided.Commit = strings.TrimSpace(provided.Commit)
		provided.Tree = strings.TrimSpace(provided.Tree)
		if provided.WorkspaceID != operation.WorkspaceID ||
			provided.IntentID != operation.Request.EffectIntentID ||
			provided.ParentCommit != operation.Request.ExpectedParent ||
			provided.Tree != operation.Request.ExpectedTree ||
			provided.CandidateDigest != operation.Request.CandidateDigest ||
			!validSameWidthPRDevelopmentOIDs(
				provided.ParentCommit, provided.Tree, provided.Commit,
			) || provided.Commit == provided.ParentCommit ||
			provided.ChangedFiles < 1 ||
			provided.ChangedFiles > prDevelopmentOperationChangedFilesMax ||
			(!allowCommitWorkspaceDrift && !provided.WorkspaceClean) {
			return PRDevelopmentControllerOperationResult{}, fmt.Errorf(
				"%w: Commit result does not prove the exact clean candidate",
				ErrPRDevelopmentControllerConflict,
			)
		}
		expected = PRDevelopmentControllerOperationResult{
			WorkspaceID:     operation.WorkspaceID,
			Tree:            operation.Request.ExpectedTree,
			WorkspaceClean:  provided.WorkspaceClean,
			IntentID:        operation.Request.EffectIntentID,
			ParentCommit:    operation.Request.ExpectedParent,
			CandidateDigest: operation.Request.CandidateDigest,
			Commit:          provided.Commit,
			ChangedFiles:    provided.ChangedFiles,
		}
	case PRDevelopmentControllerOperationPark:
		provided.ReviewDigest = strings.TrimSpace(provided.ReviewDigest)
		if !validPRDevelopmentHex(provided.ReviewDigest, sha256.Size*2) {
			return PRDevelopmentControllerOperationResult{}, fmt.Errorf(
				"%w: Park result has an invalid review digest",
				ErrInvalidPRDevelopmentController,
			)
		}
		expected = PRDevelopmentControllerOperationResult{
			WorkspaceID:         operation.WorkspaceID,
			Version:             operation.Request.ExpectedVersion + 1,
			MutationEpoch:       operation.Request.MutationEpoch,
			PreviousTip:         operation.Request.PreviousTip,
			Tip:                 operation.Request.Tip,
			Tree:                operation.Request.Tree,
			NoChanges:           operation.Request.NoChanges,
			WorkspaceClean:      true,
			ReviewVersion:       operation.Request.ExpectedVersion + 1,
			ReviewMutationEpoch: operation.Request.MutationEpoch,
			ReviewParkIntentID:  operation.Request.EffectIntentID,
			ReviewBaseCommit:    operation.Request.PreviousTip,
			ReviewCommit:        operation.Request.Tip,
			ReviewTree:          operation.Request.Tree,
			ReviewDigest:        provided.ReviewDigest,
		}
	default:
		return PRDevelopmentControllerOperationResult{}, fmt.Errorf(
			"%w: unknown operation kind", ErrInvalidPRDevelopmentController,
		)
	}
	providedJSON, _, err := encodePRDevelopmentOperationResult(provided)
	if err != nil {
		return PRDevelopmentControllerOperationResult{}, err
	}
	expectedJSON, _, err := encodePRDevelopmentOperationResult(expected)
	if err != nil {
		return PRDevelopmentControllerOperationResult{}, err
	}
	if !bytes.Equal(providedJSON, expectedJSON) {
		return PRDevelopmentControllerOperationResult{}, fmt.Errorf(
			"%w: operation result contains changed or kind-inapplicable evidence",
			ErrPRDevelopmentControllerConflict,
		)
	}
	return expected, nil
}

func finalizePRDevelopmentLineOperation(
	ctx context.Context,
	conn *sql.Conn,
	controller PRDevelopmentController,
	operation PRDevelopmentControllerOperation,
	result PRDevelopmentControllerOperationResult,
	now time.Time,
) error {
	if controller.Revision != operation.PreparedControllerRevision ||
		controller.Phase != PRDevelopmentControllerMutation ||
		controller.CurrentAttemptID != operation.AttemptID ||
		controller.AgentID != operation.AgentID ||
		controller.LineID != operation.LineID ||
		controller.MutationReservationKey == "" ||
		prDevelopmentMutationReservationDigest(
			controller.MutationReservationKey,
		) != operation.MutationReservationDigest {
		return fmt.Errorf(
			"%w: line operation controller fence changed",
			ErrPRDevelopmentControllerConflict,
		)
	}
	var (
		update sql.Result
		err    error
	)
	switch operation.Kind {
	case PRDevelopmentControllerOperationAdopt:
		if controller.WorkspaceID != "" || controller.LineVersion != 0 ||
			controller.MutationEpoch != 0 || result.Version != 0 ||
			result.MutationEpoch != 1 {
			return fmt.Errorf(
				"%w: Adopt no longer targets an unbound controller",
				ErrPRDevelopmentControllerConflict,
			)
		}
		update, err = conn.ExecContext(ctx, `
			UPDATE pr_development_thread_controllers
			SET workspace_id = ?, source_clone_url = ?, source_ref = ?,
				source_commit = ?, source_tree = ?, line_version = ?,
				mutation_epoch = ?, tip_commit = ?, tree = ?,
				revision = revision + 1, updated_at = ?
			WHERE id = ? AND revision = ? AND phase = 'mutation' AND
				current_attempt_id = ? AND lease_epoch = ? AND
				mutation_reservation_key <> '' AND workspace_id = ''`,
			operation.WorkspaceID,
			operation.SourceCloneURL,
			operation.SourceRef,
			operation.SourceCommit,
			operation.SourceTree,
			result.Version,
			result.MutationEpoch,
			result.Tip,
			result.Tree,
			toDBTime(now),
			controller.ID,
			controller.Revision,
			controller.CurrentAttemptID,
			controller.LeaseEpoch,
		)
	case PRDevelopmentControllerOperationResume:
		if controller.WorkspaceID != operation.WorkspaceID ||
			controller.SourceCloneURL != operation.SourceCloneURL ||
			controller.SourceRef != operation.SourceRef ||
			controller.SourceCommit != operation.SourceCommit ||
			controller.SourceTree != operation.SourceTree ||
			controller.LineVersion != operation.LineVersion ||
			controller.MutationEpoch != operation.MutationEpoch ||
			controller.TipCommit != operation.TipCommit ||
			controller.Tree != operation.Tree ||
			result.MutationEpoch != controller.LineVersion+1 {
			return fmt.Errorf(
				"%w: Resume no longer targets its parked controller fence",
				ErrPRDevelopmentControllerConflict,
			)
		}
		update, err = conn.ExecContext(ctx, `
			UPDATE pr_development_thread_controllers
			SET mutation_epoch = ?, revision = revision + 1, updated_at = ?
			WHERE id = ? AND revision = ? AND phase = 'mutation' AND
				current_attempt_id = ? AND lease_epoch = ? AND workspace_id = ? AND
				line_version = ? AND mutation_epoch = ? AND tip_commit = ? AND tree = ?`,
			result.MutationEpoch,
			toDBTime(now),
			controller.ID,
			controller.Revision,
			controller.CurrentAttemptID,
			controller.LeaseEpoch,
			controller.WorkspaceID,
			controller.LineVersion,
			controller.MutationEpoch,
			controller.TipCommit,
			controller.Tree,
		)
	default:
		return errors.New("non-line operation reached line finalization")
	}
	if err != nil {
		return err
	}
	return requireOnePRDevelopmentControllerRow(update)
}

// finalizePRDevelopmentParkOperation owns the complete SQLite terminal tuple
// but intentionally does not update the operation row. This lets both the
// normal and recovery callers join their distinct authorization proof to the
// same attempt/session/fence/controller transition.
func finalizePRDevelopmentParkOperation(
	ctx context.Context,
	conn *sql.Conn,
	controller PRDevelopmentController,
	operation PRDevelopmentControllerOperation,
	result PRDevelopmentControllerOperationResult,
	now time.Time,
	mutationRevision int64,
) (PRDevelopmentAttemptReviewFence, error) {
	if operation.Kind != PRDevelopmentControllerOperationPark ||
		operation.ControllerID != controller.ID ||
		operation.AttemptID != controller.CurrentAttemptID ||
		operation.PreparedControllerRevision != mutationRevision ||
		controller.AgentID != operation.AgentID ||
		controller.WorkspaceID != operation.WorkspaceID ||
		controller.LineID != operation.LineID ||
		controller.SourceCloneURL != operation.SourceCloneURL ||
		controller.SourceRef != operation.SourceRef ||
		controller.SourceCommit != operation.SourceCommit ||
		controller.SourceTree != operation.SourceTree ||
		controller.LineVersion != operation.LineVersion ||
		controller.MutationEpoch != operation.MutationEpoch ||
		controller.TipCommit != operation.TipCommit ||
		controller.Tree != operation.Tree ||
		controller.MutationReservationKey == "" ||
		prDevelopmentMutationReservationDigest(
			controller.MutationReservationKey,
		) != operation.MutationReservationDigest ||
		result.Version != controller.LineVersion+1 ||
		result.MutationEpoch != result.Version ||
		result.ReviewVersion != result.Version ||
		result.ReviewMutationEpoch != result.MutationEpoch ||
		result.ReviewParkIntentID != operation.EffectIntentID ||
		result.ReviewBaseCommit != controller.TipCommit ||
		result.ReviewCommit != result.Tip || result.ReviewTree != result.Tree ||
		result.ReviewDigest == "" || !result.WorkspaceClean {
		return PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
			"%w: Park terminal tuple differs from its prepared fence",
			ErrPRDevelopmentControllerConflict,
		)
	}
	normalTransition := controller.Phase == PRDevelopmentControllerMutation &&
		controller.Revision == mutationRevision &&
		controller.LeaseKind == PRDevelopmentControllerMutationLease &&
		controller.LeaseEpoch == operation.MutationLeaseEpoch &&
		prDevelopmentLeaseTokenDigest(
			PRDevelopmentControllerMutationLease, controller.LeaseToken,
		) == operation.MutationLeaseTokenDigest
	recoveryTransition := controller.Phase == PRDevelopmentControllerRecoveryRequired &&
		controller.Revision == mutationRevision+1 && controller.LeaseKind == "" &&
		controller.LeaseToken == "" && controller.LeaseUntil == nil &&
		controller.LeaseEpoch == operation.MutationLeaseEpoch
	if !normalTransition && !recoveryTransition {
		return PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
			"%w: Park controller is neither its live nor expired exact owner",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if controller.FenceCount >= MaxPRDevelopmentControllerFences ||
		controller.Revision > MaxPRDevelopmentControllerRevision-1 {
		return PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
			"%w: Park controller capacity exhausted",
			ErrPRDevelopmentControllerConflict,
		)
	}

	session, err := loadPRDevelopmentRepairSessionByAttempt(
		ctx, conn, operation.AttemptID,
	)
	if err != nil {
		return PRDevelopmentAttemptReviewFence{}, err
	}
	if session.ID != controller.OwnerSessionID ||
		session.WorkspaceID != operation.WorkspaceID || len(session.Attempts) == 0 {
		return PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
			"%w: Park attempt does not belong to the retained workspace owner",
			ErrPRDevelopmentControllerConflict,
		)
	}
	attempt := session.Attempts[len(session.Attempts)-1]
	if attempt.ID != operation.AttemptID {
		return PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
			"%w: Park attempt is not owner-session latest",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if now.Before(session.UpdatedAt) || now.Before(attempt.UpdatedAt) {
		return PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
			"%w: store clock regressed behind Park attempt",
			ErrInvalidPRDevelopmentController,
		)
	}
	switch attempt.Status {
	case PRDevelopmentRepairQueued:
		if attempt.Claims != 0 || session.Version >= MaxPRDevelopmentRepairVersion {
			return PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
				"%w: queued Park attempt cannot reach its terminal version",
				ErrPRDevelopmentRepairConflict,
			)
		}
		attemptResult, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_attempts
			SET status = 'completed', lease_owner = '', lease_token = '',
				lease_until = NULL, claims = 1, summary = ?, error_code = '',
				internal_error = '', iterations = ?, updated_at = ?
			WHERE id = ? AND session_id = ? AND status = 'queued' AND claims = 0`,
			operation.Request.CompletionSummary,
			operation.Request.CompletionIterations,
			toDBTime(now),
			attempt.ID,
			session.ID,
		)
		if updateErr != nil {
			return PRDevelopmentAttemptReviewFence{}, updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(attemptResult); rowErr != nil {
			return PRDevelopmentAttemptReviewFence{}, rowErr
		}
		sessionResult, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_repair_sessions
			SET workspace_id = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND version = ? AND workspace_id = ? AND version < ?`,
			operation.WorkspaceID,
			toDBTime(now),
			session.ID,
			session.Version,
			operation.WorkspaceID,
			MaxPRDevelopmentRepairVersion,
		)
		if updateErr != nil {
			return PRDevelopmentAttemptReviewFence{}, updateErr
		}
		if rowErr := requireOnePRDevelopmentControllerRow(sessionResult); rowErr != nil {
			return PRDevelopmentAttemptReviewFence{}, rowErr
		}
	case PRDevelopmentRepairCompleted:
		if attempt.Summary != operation.Request.CompletionSummary ||
			attempt.Iterations != operation.Request.CompletionIterations ||
			attempt.Claims < 1 || attempt.ErrorCode != "" || attempt.InternalError != "" {
			return PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
				"%w: completed Park attempt differs from its prepared terminal account",
				ErrPRDevelopmentControllerConflict,
			)
		}
	default:
		return PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
			"%w: only queued or already-completed attempts may Park",
			ErrPRDevelopmentControllerConflict,
		)
	}

	var duplicate int
	if duplicateErr := conn.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM pr_development_attempt_review_fences
		WHERE attempt_id = ? OR mutation_reservation_digest = ? OR
			(controller_id = ? AND park_intent_id = ?)`,
		operation.AttemptID,
		operation.MutationReservationDigest,
		controller.ID,
		operation.EffectIntentID,
	).Scan(&duplicate); duplicateErr != nil {
		return PRDevelopmentAttemptReviewFence{}, duplicateErr
	}
	if duplicate != 0 {
		return PRDevelopmentAttemptReviewFence{}, fmt.Errorf(
			"%w: Park attempt, intent, or reservation was already retired",
			ErrPRDevelopmentControllerConflict,
		)
	}
	fence := PRDevelopmentAttemptReviewFence{
		AttemptID:                  operation.AttemptID,
		ControllerID:               controller.ID,
		ThreadID:                   controller.ThreadID,
		LineID:                     controller.LineID,
		Ordinal:                    controller.FenceCount,
		LineVersion:                result.Version,
		MutationEpoch:              result.MutationEpoch,
		ParkIntentID:               result.ReviewParkIntentID,
		BaseCommit:                 result.ReviewBaseCommit,
		TipCommit:                  result.ReviewCommit,
		Tree:                       result.ReviewTree,
		NoChanges:                  result.NoChanges,
		LineReviewDigest:           result.ReviewDigest,
		MutationReservationDigest:  operation.MutationReservationDigest,
		MutationLeaseEpoch:         operation.MutationLeaseEpoch,
		MutationLeaseTokenDigest:   operation.MutationLeaseTokenDigest,
		MutationControllerRevision: mutationRevision,
		PreviousHash:               controller.FencesDigest,
		CreatedAt:                  now,
	}
	fence.FenceHash = hashPRDevelopmentReviewFence(fence)
	_, err = conn.ExecContext(ctx, `
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
		toDBTime(now),
	)
	if err != nil {
		return PRDevelopmentAttemptReviewFence{}, err
	}
	finalRevision := controller.Revision
	if normalTransition {
		finalRevision++
	}
	controllerResult, err := conn.ExecContext(ctx, `
		UPDATE pr_development_thread_controllers
		SET revision = ?, phase = 'review_pending', line_version = ?,
			mutation_epoch = ?, tip_commit = ?, tree = ?, lease_kind = '',
			lease_owner = '', lease_token = '', lease_until = NULL,
			mutation_reservation_key = '', fence_count = fence_count + 1,
			fences_digest = ?, updated_at = ?
		WHERE id = ? AND revision = ? AND current_attempt_id = ? AND
			mutation_reservation_key <> '' AND phase = ?`,
		finalRevision,
		fence.LineVersion,
		fence.MutationEpoch,
		fence.TipCommit,
		fence.Tree,
		fence.FenceHash,
		toDBTime(now),
		controller.ID,
		controller.Revision,
		controller.CurrentAttemptID,
		controller.Phase,
	)
	if err != nil {
		return PRDevelopmentAttemptReviewFence{}, err
	}
	if rowErr := requireOnePRDevelopmentControllerRow(controllerResult); rowErr != nil {
		return PRDevelopmentAttemptReviewFence{}, rowErr
	}
	if orchestrationErr := finalizePRDevelopmentRepairOrchestrationPark(
		ctx,
		conn,
		controller,
		operation,
		result,
		fence,
		now,
	); orchestrationErr != nil {
		return PRDevelopmentAttemptReviewFence{}, orchestrationErr
	}
	return fence, nil
}

func requireImmediatePRDevelopmentOperationReplay(
	ctx context.Context,
	conn *sql.Conn,
	controller PRDevelopmentController,
	operation PRDevelopmentControllerOperation,
	leaseToken string,
	now time.Time,
) (*PRDevelopmentAttemptReviewFence, bool, error) {
	heartbeatTolerantCommit := operation.Kind == PRDevelopmentControllerOperationCommit &&
		operation.RecoveryStagedAt == nil
	if operation.FinalizedAt == nil ||
		controller.Revision != operation.FinalControllerRevision ||
		controller.Phase != operation.FinalControllerPhase ||
		controller.CurrentAttemptID != operation.AttemptID ||
		controller.UpdatedAt.Before(*operation.FinalizedAt) ||
		(!heartbeatTolerantCommit && !controller.UpdatedAt.Equal(*operation.FinalizedAt)) {
		return nil, false, fmt.Errorf(
			"%w: finalized operation replay is no longer controller-current",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if operation.Kind == PRDevelopmentControllerOperationPark {
		fence, found, err := loadPRDevelopmentReviewFenceByAttempt(
			ctx, conn, operation.AttemptID,
		)
		if err != nil {
			return nil, false, err
		}
		if !found || fence.FenceHash != operation.FinalFenceHash ||
			controller.FencesDigest != fence.FenceHash ||
			fence.LineVersion != operation.Result.Version ||
			fence.TipCommit != operation.Result.Tip ||
			fence.Tree != operation.Result.Tree ||
			fence.LineReviewDigest != operation.Result.ReviewDigest {
			return nil, false, fmt.Errorf(
				"%w: finalized Park replay fence changed",
				ErrPRDevelopmentControllerConflict,
			)
		}
		return &fence, false, nil
	}
	if controller.Phase != PRDevelopmentControllerMutation ||
		controller.LeaseKind != PRDevelopmentControllerMutationLease ||
		controller.LeaseToken != leaseToken ||
		controller.LeaseEpoch != operation.MutationLeaseEpoch ||
		prDevelopmentMutationReservationDigest(
			controller.MutationReservationKey,
		) != operation.MutationReservationDigest {
		return nil, false, fmt.Errorf(
			"%w: finalized operation replay lost its mutation fence",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if operation.Kind == PRDevelopmentControllerOperationCommit &&
		(controller.AgentID != operation.AgentID ||
			controller.WorkspaceID != operation.WorkspaceID ||
			controller.LineID != operation.LineID ||
			controller.SourceCloneURL != operation.SourceCloneURL ||
			controller.SourceRef != operation.SourceRef ||
			controller.SourceCommit != operation.SourceCommit ||
			controller.SourceTree != operation.SourceTree ||
			controller.LineVersion != operation.LineVersion ||
			controller.MutationEpoch != operation.MutationEpoch ||
			controller.TipCommit != operation.TipCommit ||
			controller.Tree != operation.Tree ||
			operation.Result.WorkspaceID != controller.WorkspaceID ||
			operation.Result.ParentCommit != controller.TipCommit ||
			operation.Result.Tree != operation.Request.ExpectedTree ||
			operation.Result.CandidateDigest != operation.Request.CandidateDigest ||
			operation.Result.IntentID != operation.Request.EffectIntentID) {
		return nil, false, fmt.Errorf(
			"%w: finalized Commit replay changed its line or result evidence",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if operation.Kind == PRDevelopmentControllerOperationAdopt ||
		operation.Kind == PRDevelopmentControllerOperationResume {
		if controller.WorkspaceID != operation.Result.WorkspaceID ||
			controller.MutationEpoch != operation.Result.MutationEpoch ||
			controller.TipCommit != operation.Result.Tip ||
			controller.Tree != operation.Result.Tree {
			return nil, false, fmt.Errorf(
				"%w: finalized line transition replay changed",
				ErrPRDevelopmentControllerConflict,
			)
		}
	}
	if controller.LeaseUntil == nil {
		return nil, false, fmt.Errorf(
			"%w: finalized operation replay lost its lease deadline",
			ErrPRDevelopmentControllerConflict,
		)
	}
	if !controller.LeaseUntil.After(now) {
		if err := expirePRDevelopmentMutationLease(ctx, conn, controller, now); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	return nil, false, nil
}
