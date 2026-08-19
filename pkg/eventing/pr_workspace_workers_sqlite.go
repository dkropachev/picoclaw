//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	maxPRWorkspaceClaimItems = 100
	maxPRWorkspaceLease      = 24 * time.Hour
)

type prWorkspaceClaimCandidate struct {
	id          string
	workspaceID string
	payload     []byte
}

// ClaimPRWorkspaceOperations atomically fences a bounded, globally ordered
// batch. It also recovers running intents whose previous lease expired.
func (s *Store) ClaimPRWorkspaceOperations(
	ctx context.Context,
	input PRWorkspaceClaimRequest,
) ([]PRClaimedOperationIntent, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	if err := validatePRWorkspaceClaimRequest(&input); err != nil {
		return nil, err
	}
	now, clockErr := s.currentTime()
	if clockErr != nil {
		return nil, clockErr
	}
	leaseUntil := now.Add(input.LeaseDuration)
	var claimed []PRClaimedOperationIntent
	transactionErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		candidates, candidateErr := loadPRWorkspaceClaimCandidates(ctx, conn, `SELECT id, workspace_id, payload_json
			FROM pr_operation_intents
			WHERE (status = 'queued' AND available_at <= ?)
				OR (status = 'running' AND lease_until IS NOT NULL AND lease_until <= ?)
			ORDER BY available_at ASC, created_at ASC, id ASC LIMIT ?`, now, input.Limit)
		if candidateErr != nil {
			return candidateErr
		}
		claimed = make([]PRClaimedOperationIntent, 0, len(candidates))
		changes := make(map[string][]*prWorkspaceMutationRecord)
		for _, candidate := range candidates {
			var intent PRWorkspaceOperationIntent
			if decodeErr := json.Unmarshal(candidate.payload, &intent); decodeErr != nil {
				return fmt.Errorf("decode claimed operation %s: %w", candidate.id, decodeErr)
			}
			token, tokenErr := newPrefixedID(prLeaseTokenIDPrefix)
			if tokenErr != nil {
				return tokenErr
			}
			intent.State = PRExecutionRunning
			intent.LeaseOwner = input.WorkerID
			intent.LeaseToken = token
			intent.LeaseUntil = &leaseUntil
			intent.Attempts++
			intent.UpdatedAt = now
			if validationErr := validatePRWorkspaceRecord(&intent); validationErr != nil {
				return validationErr
			}
			payload, marshalErr := json.Marshal(intent)
			if marshalErr != nil {
				return marshalErr
			}
			updated, updateErr := conn.ExecContext(ctx, `UPDATE pr_operation_intents
				SET status = 'running', lease_owner = ?, lease_token = ?, lease_until = ?,
					attempts = ?, payload_json = ?, updated_at = ?
				WHERE id = ? AND workspace_id = ?`, intent.LeaseOwner, intent.LeaseToken,
				toDBTime(leaseUntil), intent.Attempts, payload, toDBTime(now), candidate.id, candidate.workspaceID)
			if updateErr != nil {
				return updateErr
			}
			if affectedErr := requirePRWorkspaceRowsAffected(updated, "operation claim"); affectedErr != nil {
				return affectedErr
			}
			claimed = append(claimed, PRClaimedOperationIntent{Intent: intent})
			changes[candidate.workspaceID] = append(changes[candidate.workspaceID], &prWorkspaceMutationRecord{
				table: "pr_operation_intents", prefix: prOperationIntentIDPrefix, value: &intent,
				meta: &intent.PRWorkspaceRecord, status: string(intent.State),
			})
		}
		versions, historyErr := advancePRWorkspaceWorkerHistory(ctx, conn, changes, now)
		if historyErr != nil {
			return historyErr
		}
		for index := range claimed {
			claimed[index].WorkspaceVersion = versions[claimed[index].Intent.WorkspaceID]
		}
		return nil
	})
	if transactionErr != nil {
		return nil, fmt.Errorf("claim pull request operations: %w", s.dbError(transactionErr))
	}
	return claimed, nil
}

// FinishPRWorkspaceOperation accepts a result only from the live lease token.
func (s *Store) FinishPRWorkspaceOperation(
	ctx context.Context,
	input PRWorkspaceOperationFinish,
) (PRClaimedOperationIntent, error) {
	if err := s.ready(ctx); err != nil {
		return PRClaimedOperationIntent{}, err
	}
	input.IntentID = strings.TrimSpace(input.IntentID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	if !validPrefixedID(input.IntentID, prOperationIntentIDPrefix) ||
		!validPrefixedID(input.LeaseToken, prLeaseTokenIDPrefix) {
		return PRClaimedOperationIntent{}, fmt.Errorf("%w: invalid operation finish identity", ErrInvalidPRWorkspace)
	}
	if !validPRWorkspaceOperationFinishState(input.State) {
		return PRClaimedOperationIntent{}, fmt.Errorf(
			"%w: operation finish state must be terminal",
			ErrInvalidPRWorkspace,
		)
	}
	if err := validatePRWorkspaceRaw("operation result", input.Result); err != nil {
		return PRClaimedOperationIntent{}, err
	}
	now, clockErr := s.currentTime()
	if clockErr != nil {
		return PRClaimedOperationIntent{}, clockErr
	}
	var result PRClaimedOperationIntent
	transactionErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		var payload []byte
		if queryErr := conn.QueryRowContext(ctx, `SELECT payload_json FROM pr_operation_intents WHERE id = ?`, input.IntentID).
			Scan(&payload); queryErr != nil {
			return queryErr
		}
		var intent PRWorkspaceOperationIntent
		if decodeErr := json.Unmarshal(payload, &intent); decodeErr != nil {
			return decodeErr
		}
		if intent.State != PRExecutionRunning || intent.LeaseToken != input.LeaseToken || intent.LeaseUntil == nil ||
			!intent.LeaseUntil.After(now) {
			return fmt.Errorf("%w: operation lease is absent, expired, or superseded", ErrPRWorkspaceConflict)
		}
		intent.State = input.State
		intent.Result = append(json.RawMessage(nil), input.Result...)
		intent.ResultDigest = strings.TrimSpace(input.ResultDigest)
		intent.PublicErrorCode = strings.TrimSpace(input.PublicErrorCode)
		intent.InternalError = strings.TrimSpace(input.InternalError)
		intent.LeaseOwner = ""
		intent.LeaseToken = ""
		intent.LeaseUntil = nil
		intent.UpdatedAt = now
		if validationErr := validatePRWorkspaceRecord(&intent); validationErr != nil {
			return validationErr
		}
		encoded, marshalErr := json.Marshal(intent)
		if marshalErr != nil {
			return marshalErr
		}
		updated, updateErr := conn.ExecContext(ctx, `UPDATE pr_operation_intents SET status = ?,
			lease_owner = '', lease_token = '', lease_until = NULL, payload_json = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND status = 'running' AND lease_token = ?`,
			intent.State, encoded, toDBTime(now), intent.ID, intent.WorkspaceID, input.LeaseToken)
		if updateErr != nil {
			return updateErr
		}
		if affectedErr := requirePRWorkspaceRowsAffected(updated, "operation finish"); affectedErr != nil {
			return affectedErr
		}
		record := &prWorkspaceMutationRecord{
			table: "pr_operation_intents", prefix: prOperationIntentIDPrefix,
			value: &intent, meta: &intent.PRWorkspaceRecord, status: string(intent.State),
		}
		versions, historyErr := advancePRWorkspaceWorkerHistory(ctx, conn, map[string][]*prWorkspaceMutationRecord{
			intent.WorkspaceID: {record},
		}, now)
		if historyErr != nil {
			return historyErr
		}
		result = PRClaimedOperationIntent{Intent: intent, WorkspaceVersion: versions[intent.WorkspaceID]}
		return nil
	})
	if transactionErr != nil {
		return PRClaimedOperationIntent{}, fmt.Errorf(
			"finish pull request operation: %w",
			s.dbError(transactionErr),
		)
	}
	return result, nil
}

// ClaimPRWorkspacePublications atomically fences pending work and recovers
// claimed publications whose lease expired.
func (s *Store) ClaimPRWorkspacePublications(
	ctx context.Context,
	input PRWorkspaceClaimRequest,
) ([]PRClaimedPublication, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	if err := validatePRWorkspaceClaimRequest(&input); err != nil {
		return nil, err
	}
	now, clockErr := s.currentTime()
	if clockErr != nil {
		return nil, clockErr
	}
	leaseUntil := now.Add(input.LeaseDuration)
	var claimed []PRClaimedPublication
	transactionErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		candidates, candidateErr := loadPRWorkspaceClaimCandidates(ctx, conn, `SELECT id, workspace_id, payload_json
			FROM pr_publications
			WHERE (status = 'pending' AND available_at <= ?)
				OR (status = 'claimed' AND lease_until IS NOT NULL AND lease_until <= ?)
			ORDER BY available_at ASC, created_at ASC, id ASC LIMIT ?`, now, input.Limit)
		if candidateErr != nil {
			return candidateErr
		}
		claimed = make([]PRClaimedPublication, 0, len(candidates))
		changes := make(map[string][]*prWorkspaceMutationRecord)
		for _, candidate := range candidates {
			var publication PRPublication
			if decodeErr := json.Unmarshal(candidate.payload, &publication); decodeErr != nil {
				return fmt.Errorf("decode claimed publication %s: %w", candidate.id, decodeErr)
			}
			token, tokenErr := newPrefixedID(prLeaseTokenIDPrefix)
			if tokenErr != nil {
				return tokenErr
			}
			publication.Status = PRPublicationClaimed
			publication.ExecutionState = PRExecutionRunning
			publication.LeaseOwner = input.WorkerID
			publication.LeaseToken = token
			publication.LeaseUntil = &leaseUntil
			publication.Attempts++
			publication.UpdatedAt = now
			if validationErr := validatePRWorkspaceRecord(&publication); validationErr != nil {
				return validationErr
			}
			payload, marshalErr := json.Marshal(publication)
			if marshalErr != nil {
				return marshalErr
			}
			updated, updateErr := conn.ExecContext(ctx, `UPDATE pr_publications
				SET status = 'claimed', lease_owner = ?, lease_token = ?, lease_until = ?,
					attempts = ?, payload_json = ?, updated_at = ?
				WHERE id = ? AND workspace_id = ?`, publication.LeaseOwner, publication.LeaseToken,
				toDBTime(leaseUntil), publication.Attempts, payload, toDBTime(now), candidate.id, candidate.workspaceID)
			if updateErr != nil {
				return updateErr
			}
			if affectedErr := requirePRWorkspaceRowsAffected(updated, "publication claim"); affectedErr != nil {
				return affectedErr
			}
			claimed = append(claimed, PRClaimedPublication{Publication: publication})
			changes[candidate.workspaceID] = append(changes[candidate.workspaceID], &prWorkspaceMutationRecord{
				table: "pr_publications", prefix: prPublicationIDPrefix, value: &publication,
				meta: &publication.PRWorkspaceRecord, status: string(publication.Status),
			})
		}
		versions, historyErr := advancePRWorkspaceWorkerHistory(ctx, conn, changes, now)
		if historyErr != nil {
			return historyErr
		}
		for index := range claimed {
			claimed[index].WorkspaceVersion = versions[claimed[index].Publication.WorkspaceID]
		}
		return nil
	})
	if transactionErr != nil {
		return nil, fmt.Errorf("claim pull request publications: %w", s.dbError(transactionErr))
	}
	return claimed, nil
}

// FinishPRWorkspacePublication records a fenced published, failed, or unknown
// outcome. Unknown is reserved for ambiguous provider responses.
func (s *Store) FinishPRWorkspacePublication(
	ctx context.Context,
	input PRWorkspacePublicationFinish,
) (PRClaimedPublication, error) {
	if err := s.ready(ctx); err != nil {
		return PRClaimedPublication{}, err
	}
	input.PublicationID = strings.TrimSpace(input.PublicationID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	if !validPrefixedID(input.PublicationID, prPublicationIDPrefix) ||
		!validPrefixedID(input.LeaseToken, prLeaseTokenIDPrefix) {
		return PRClaimedPublication{}, fmt.Errorf("%w: invalid publication finish identity", ErrInvalidPRWorkspace)
	}
	if input.Status != PRPublicationPublished && input.Status != PRPublicationFailed &&
		input.Status != PRPublicationUnknown {
		return PRClaimedPublication{}, fmt.Errorf(
			"%w: publication finish status must be terminal",
			ErrInvalidPRWorkspace,
		)
	}
	now, clockErr := s.currentTime()
	if clockErr != nil {
		return PRClaimedPublication{}, clockErr
	}
	var result PRClaimedPublication
	transactionErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		var payload []byte
		if queryErr := conn.QueryRowContext(ctx, `SELECT payload_json FROM pr_publications WHERE id = ?`, input.PublicationID).
			Scan(&payload); queryErr != nil {
			return queryErr
		}
		var publication PRPublication
		if decodeErr := json.Unmarshal(payload, &publication); decodeErr != nil {
			return decodeErr
		}
		if publication.Status != PRPublicationClaimed || publication.LeaseToken != input.LeaseToken ||
			publication.LeaseUntil == nil ||
			!publication.LeaseUntil.After(now) {
			return fmt.Errorf("%w: publication lease is absent, expired, or superseded", ErrPRWorkspaceConflict)
		}
		publication.Status = input.Status
		switch input.Status {
		case PRPublicationPublished:
			publication.ExecutionState = PRExecutionSucceeded
		case PRPublicationFailed:
			publication.ExecutionState = PRExecutionFailed
		case PRPublicationUnknown:
			publication.ExecutionState = PRExecutionUnknown
		}
		publication.ExternalID = strings.TrimSpace(input.ExternalID)
		publication.ExternalURL = strings.TrimSpace(input.ExternalURL)
		publication.PublicErrorCode = strings.TrimSpace(input.PublicErrorCode)
		publication.InternalError = strings.TrimSpace(input.InternalError)
		if input.Status == PRPublicationPublished {
			if input.PublishedAt == nil {
				publishedAt := now
				publication.PublishedAt = &publishedAt
			} else {
				publishedAt := *input.PublishedAt
				publication.PublishedAt = &publishedAt
			}
		} else {
			publication.PublishedAt = nil
		}
		publication.LeaseOwner = ""
		publication.LeaseToken = ""
		publication.LeaseUntil = nil
		publication.UpdatedAt = now
		if validationErr := validatePRWorkspaceRecord(&publication); validationErr != nil {
			return validationErr
		}
		encoded, marshalErr := json.Marshal(publication)
		if marshalErr != nil {
			return marshalErr
		}
		updated, updateErr := conn.ExecContext(ctx, `UPDATE pr_publications SET status = ?,
			lease_owner = '', lease_token = '', lease_until = NULL, payload_json = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND status = 'claimed' AND lease_token = ?`,
			publication.Status, encoded, toDBTime(now), publication.ID, publication.WorkspaceID, input.LeaseToken)
		if updateErr != nil {
			return updateErr
		}
		if affectedErr := requirePRWorkspaceRowsAffected(updated, "publication finish"); affectedErr != nil {
			return affectedErr
		}
		record := &prWorkspaceMutationRecord{
			table: "pr_publications", prefix: prPublicationIDPrefix,
			value: &publication, meta: &publication.PRWorkspaceRecord, status: string(publication.Status),
		}
		versions, historyErr := advancePRWorkspaceWorkerHistory(ctx, conn, map[string][]*prWorkspaceMutationRecord{
			publication.WorkspaceID: {record},
		}, now)
		if historyErr != nil {
			return historyErr
		}
		result = PRClaimedPublication{Publication: publication, WorkspaceVersion: versions[publication.WorkspaceID]}
		return nil
	})
	if transactionErr != nil {
		return PRClaimedPublication{}, fmt.Errorf(
			"finish pull request publication: %w",
			s.dbError(transactionErr),
		)
	}
	return result, nil
}

func validatePRWorkspaceClaimRequest(input *PRWorkspaceClaimRequest) error {
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	if err := validatePRWorkspaceString("worker ID", input.WorkerID, 256, true); err != nil {
		return err
	}
	if input.Limit <= 0 || input.Limit > maxPRWorkspaceClaimItems {
		return fmt.Errorf("%w: claim limit must be between 1 and %d", ErrInvalidPRWorkspace, maxPRWorkspaceClaimItems)
	}
	if input.LeaseDuration <= 0 || input.LeaseDuration > maxPRWorkspaceLease {
		return fmt.Errorf(
			"%w: lease duration must be positive and at most %s",
			ErrInvalidPRWorkspace,
			maxPRWorkspaceLease,
		)
	}
	return nil
}

func loadPRWorkspaceClaimCandidates(
	ctx context.Context,
	conn *sql.Conn,
	query string,
	now time.Time,
	limit int,
) ([]prWorkspaceClaimCandidate, error) {
	rows, err := conn.QueryContext(ctx, query, toDBTime(now), toDBTime(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []prWorkspaceClaimCandidate
	for rows.Next() {
		var candidate prWorkspaceClaimCandidate
		if err := rows.Scan(&candidate.id, &candidate.workspaceID, &candidate.payload); err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	return result, rows.Err()
}

func requirePRWorkspaceRowsAffected(result sql.Result, action string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s lost its lease race", ErrPRWorkspaceConflict, action)
	}
	return nil
}

func advancePRWorkspaceWorkerHistory(
	ctx context.Context,
	conn *sql.Conn,
	changes map[string][]*prWorkspaceMutationRecord,
	now time.Time,
) (map[string]int64, error) {
	workspaceIDs := make([]string, 0, len(changes))
	for workspaceID := range changes {
		workspaceIDs = append(workspaceIDs, workspaceID)
	}
	sort.Strings(workspaceIDs)
	versions := make(map[string]int64, len(workspaceIDs))
	for _, workspaceID := range workspaceIDs {
		workspace, err := getPRWorkspaceRecord(ctx, conn, workspaceID)
		if err != nil {
			return nil, err
		}
		version := workspace.Version + 1
		updated, err := conn.ExecContext(ctx, `UPDATE pr_workspaces SET version = ?, updated_at = ?
			WHERE id = ? AND version = ?`, version, toDBTime(now), workspaceID, workspace.Version)
		if err != nil {
			return nil, err
		}
		if err := requirePRWorkspaceRowsAffected(updated, "workspace worker version"); err != nil {
			return nil, err
		}
		if err := appendPRWorkspaceHistory(ctx, conn, workspaceID, version, changes[workspaceID], now); err != nil {
			return nil, err
		}
		versions[workspaceID] = version
	}
	return versions, nil
}

func validPRWorkspaceOperationFinishState(value PRExecutionState) bool {
	switch value {
	case PRExecutionSucceeded, PRExecutionFailed, PRExecutionBlocked, PRExecutionCanceled,
		PRExecutionStale, PRExecutionUnknown:
		return true
	default:
		return false
	}
}
