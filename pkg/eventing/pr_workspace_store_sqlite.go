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
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxPRWorkspaceListItems      = 100
	maxPRWorkspaceRequestIDBytes = 256
	maxPRWorkspaceIdentityBytes  = 1024
	maxPRWorkspaceTextBytes      = 64 << 10
	maxPRWorkspaceBodyBytes      = 512 << 10
	maxPRWorkspaceListEntries    = 512
)

var (
	_ PRWorkspaceStore        = (*Store)(nil)
	_ PRWorkspaceCutoverStore = (*Store)(nil)
	_ PRWorkspaceWorkerStore  = (*Store)(nil)
)

const prWorkspaceColumns = `
	id, provider, provider_origin, repository_id, repository,
	pull_request_id, pull_number, provider_head_sha, owned, head_writable, phase,
	execution_state, current_provider_ordinal, active_charter_id, version,
	created_at, updated_at`

type prWorkspaceCreateResult struct {
	WorkspaceID      string `json:"workspace_id"`
	WorkspaceVersion int64  `json:"workspace_version"`
	Created          bool   `json:"created"`
}

type prWorkspacePatchReceipt struct {
	WorkspaceID      string `json:"workspace_id"`
	WorkspaceVersion int64  `json:"workspace_version"`
}

type prWorkspaceMutationRecord struct {
	table     string
	prefix    string
	immutable bool
	mode      string
	value     any
	meta      *PRWorkspaceRecord
	status    string
}

// SetPRWorkspaceIngressCutover advances the process-wide inbox cutover for one
// source/connector pair. The cursor is independent of any PR workspace so a
// migration can establish it before the first workspace exists.
func (s *Store) SetPRWorkspaceIngressCutover(ctx context.Context, watermark PRIngressCutoverWatermark) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	watermark.Source = strings.TrimSpace(watermark.Source)
	watermark.Connector = strings.TrimSpace(watermark.Connector)
	watermark.InboxEventID = strings.TrimSpace(watermark.InboxEventID)
	if err := validatePRIngressCutoverWatermark(watermark); err != nil {
		return err
	}
	now, err := s.currentTime()
	if err != nil {
		return err
	}
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		var receivedAt int64
		var eventID string
		err := conn.QueryRowContext(ctx, `SELECT inbox_received_at, inbox_event_id
			FROM pr_ingress_cutover_watermarks WHERE source = ? AND connector = ?`,
			watermark.Source, watermark.Connector).Scan(&receivedAt, &eventID)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = conn.ExecContext(ctx, `INSERT INTO pr_ingress_cutover_watermarks (
				source, connector, inbox_received_at, inbox_event_id, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?)`, watermark.Source, watermark.Connector,
				toDBTime(watermark.InboxReceivedAt), watermark.InboxEventID, toDBTime(now), toDBTime(now))
			return err
		}
		if err != nil {
			return err
		}
		storedPosition := fromDBTime(receivedAt)
		if watermark.InboxReceivedAt.Before(storedPosition) ||
			(watermark.InboxReceivedAt.Equal(storedPosition) && watermark.InboxEventID < eventID) {
			return fmt.Errorf("%w: ingress cutover cannot move backwards", ErrPRWorkspaceConflict)
		}
		if watermark.InboxReceivedAt.Equal(storedPosition) && watermark.InboxEventID == eventID {
			return nil
		}
		_, err = conn.ExecContext(ctx, `UPDATE pr_ingress_cutover_watermarks
			SET inbox_received_at = ?, inbox_event_id = ?, updated_at = ?
			WHERE source = ? AND connector = ?`, toDBTime(watermark.InboxReceivedAt),
			watermark.InboxEventID, toDBTime(now), watermark.Source, watermark.Connector)
		return err
	})
	if err != nil {
		return fmt.Errorf("set pull request ingress cutover: %w", s.dbError(err))
	}
	return nil
}

// GetPRWorkspaceIngressCutover loads the process-wide cutover cursor.
func (s *Store) GetPRWorkspaceIngressCutover(ctx context.Context, source, connector string) (PRIngressCutoverWatermark, error) {
	if err := s.ready(ctx); err != nil {
		return PRIngressCutoverWatermark{}, err
	}
	source = strings.TrimSpace(source)
	connector = strings.TrimSpace(connector)
	if err := validatePRWorkspaceString("watermark source", source, 256, true); err != nil {
		return PRIngressCutoverWatermark{}, err
	}
	if err := validatePRWorkspaceString("watermark connector", connector, 256, true); err != nil {
		return PRIngressCutoverWatermark{}, err
	}
	var result PRIngressCutoverWatermark
	var receivedAt, createdAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT source, connector, inbox_received_at,
		inbox_event_id, created_at, updated_at FROM pr_ingress_cutover_watermarks
		WHERE source = ? AND connector = ?`, source, connector).Scan(&result.Source,
		&result.Connector, &receivedAt, &result.InboxEventID, &createdAt, &updatedAt)
	if err != nil {
		return PRIngressCutoverWatermark{}, fmt.Errorf("get pull request ingress cutover: %w", s.dbError(err))
	}
	result.InboxReceivedAt = fromDBTime(receivedAt)
	result.CreatedAt = fromDBTime(createdAt)
	result.UpdatedAt = fromDBTime(updatedAt)
	return result, nil
}

func validatePRIngressCutoverWatermark(value PRIngressCutoverWatermark) error {
	if err := validatePRWorkspaceString("watermark source", value.Source, 256, true); err != nil {
		return err
	}
	if err := validatePRWorkspaceString("watermark connector", value.Connector, 256, true); err != nil {
		return err
	}
	if err := validatePRWorkspaceString("watermark event ID", value.InboxEventID, 256, true); err != nil {
		return err
	}
	if value.InboxReceivedAt.IsZero() {
		return fmt.Errorf("%w: watermark received_at is required", ErrInvalidPRWorkspace)
	}
	if err := validateDBTimestamp("watermark received_at", value.InboxReceivedAt); err != nil {
		return fmt.Errorf("%w: watermark received_at is invalid", ErrInvalidPRWorkspace)
	}
	return nil
}

// CreatePRWorkspace verifies strict provider identity at the storage boundary,
// deduplicates that immutable identity, and records the initial provider
// snapshot in the same transaction.
func (s *Store) CreatePRWorkspace(
	ctx context.Context,
	input PRWorkspaceCreate,
) (PRWorkspaceAggregate, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRWorkspaceAggregate{}, false, err
	}
	now, err := s.currentTime()
	if err != nil {
		return PRWorkspaceAggregate{}, false, err
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	if err := validatePRWorkspaceRequestID(input.RequestID); err != nil {
		return PRWorkspaceAggregate{}, false, err
	}
	input.WorkspaceID = strings.TrimSpace(input.WorkspaceID)
	if input.WorkspaceID != "" && !validPrefixedID(input.WorkspaceID, prWorkspaceIDPrefix) {
		return PRWorkspaceAggregate{}, false, fmt.Errorf("%w: invalid workspace ID", ErrInvalidPRWorkspace)
	}
	if input.Phase == "" {
		input.Phase = PRWorkspaceIntake
	}
	if input.ExecutionState == "" {
		input.ExecutionState = PRExecutionQueued
	}
	if err := validatePRWorkspacePhase(input.Phase); err != nil {
		return PRWorkspaceAggregate{}, false, err
	}
	if err := validatePRExecutionState(input.ExecutionState); err != nil {
		return PRWorkspaceAggregate{}, false, err
	}
	normalizePRProviderSnapshot(&input.Provider)
	if err := validatePRProviderSnapshot(input.Provider); err != nil {
		return PRWorkspaceAggregate{}, false, err
	}
	canonical, err := json.Marshal(struct {
		WorkspaceID    string             `json:"workspace_id,omitempty"`
		Provider       PRProviderSnapshot `json:"provider"`
		Phase          PRWorkspacePhase   `json:"phase"`
		ExecutionState PRExecutionState   `json:"execution_state"`
	}{input.WorkspaceID, input.Provider, input.Phase, input.ExecutionState})
	if err != nil {
		return PRWorkspaceAggregate{}, false, fmt.Errorf("%w: encode create request: %v", ErrInvalidPRWorkspace, err)
	}
	requestHash := hashPRWorkspaceRequest("create", canonical)

	var result prWorkspaceCreateResult
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		if replay, ok, replayErr := loadPRWorkspaceCreateReplay(ctx, conn, input.RequestID, requestHash); replayErr != nil {
			return replayErr
		} else if ok {
			result = replay
			return nil
		}

		workspace, findErr := findPRWorkspaceByIdentity(ctx, conn, input.Provider)
		if findErr != nil && !errors.Is(findErr, sql.ErrNoRows) {
			return findErr
		}
		if findErr == nil {
			if input.WorkspaceID != "" && input.WorkspaceID != workspace.ID {
				return fmt.Errorf("%w: provider identity already has workspace %s", ErrPRWorkspaceConflict, workspace.ID)
			}
			result = prWorkspaceCreateResult{WorkspaceID: workspace.ID, WorkspaceVersion: workspace.Version, Created: false}
			return insertPRWorkspaceRequest(ctx, conn, input.RequestID, workspace.ID, "create", requestHash, result, now)
		}

		workspaceID := input.WorkspaceID
		if workspaceID == "" {
			var idErr error
			workspaceID, idErr = newPrefixedID(prWorkspaceIDPrefix)
			if idErr != nil {
				return idErr
			}
		} else {
			var found int
			err := conn.QueryRowContext(ctx, `SELECT 1 FROM pr_workspaces WHERE id = ?`, workspaceID).Scan(&found)
			if err == nil {
				return fmt.Errorf("%w: workspace ID belongs to another provider identity", ErrPRWorkspaceConflict)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		snapshotID, idErr := newPrefixedID(prProviderSnapshotIDPrefix)
		if idErr != nil {
			return idErr
		}
		input.Provider.PRWorkspaceRecord = PRWorkspaceRecord{
			ID: snapshotID, WorkspaceID: workspaceID, Ordinal: 1,
			CreatedAt: now, UpdatedAt: now,
		}
		if input.Provider.ObservedAt.IsZero() {
			input.Provider.ObservedAt = now
		}
		providerJSON, marshalErr := json.Marshal(input.Provider)
		if marshalErr != nil {
			return marshalErr
		}
		if len(providerJSON) > MaxPRWorkspaceRecordBytes {
			return fmt.Errorf("%w: provider snapshot exceeds %d bytes", ErrInvalidPRWorkspace, MaxPRWorkspaceRecordBytes)
		}
		if _, execErr := conn.ExecContext(ctx, `
			INSERT INTO pr_workspaces (
				id, provider, provider_origin, repository_id, repository,
				pull_request_id, pull_number, provider_head_sha, owned,
				head_writable, phase, execution_state, current_provider_ordinal, active_charter_id,
				version, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, '', 1, ?, ?)`,
			workspaceID, input.Provider.Provider, input.Provider.ProviderOrigin,
			input.Provider.RepositoryID, input.Provider.Repository,
			input.Provider.PullRequestID, input.Provider.PullNumber, input.Provider.HeadSHA,
			input.Provider.Owned,
			input.Provider.HeadWritable, input.Phase, input.ExecutionState,
			toDBTime(now), toDBTime(now),
		); execErr != nil {
			return execErr
		}
		if _, execErr := conn.ExecContext(ctx, `
			INSERT INTO pr_provider_snapshots (
				id, workspace_id, ordinal, status, payload_json, created_at, updated_at
			) VALUES (?, ?, 1, 'observed', ?, ?, ?)`,
			snapshotID, workspaceID, providerJSON, toDBTime(now), toDBTime(now),
		); execErr != nil {
			return execErr
		}
		initialRecord := &prWorkspaceMutationRecord{table: "pr_provider_snapshots", prefix: prProviderSnapshotIDPrefix, immutable: true, mode: "append", value: &input.Provider, meta: &input.Provider.PRWorkspaceRecord, status: "observed"}
		if historyErr := appendPRWorkspaceHistory(ctx, conn, workspaceID, 1, []*prWorkspaceMutationRecord{initialRecord}, now); historyErr != nil {
			return historyErr
		}
		result = prWorkspaceCreateResult{WorkspaceID: workspaceID, WorkspaceVersion: 1, Created: true}
		return insertPRWorkspaceRequest(ctx, conn, input.RequestID, workspaceID, "create", requestHash, result, now)
	})
	if err != nil {
		return PRWorkspaceAggregate{}, false, fmt.Errorf("create pull request workspace: %w", s.dbError(err))
	}
	aggregate, err := s.getPRWorkspaceAtVersion(ctx, result.WorkspaceID, result.WorkspaceVersion)
	if err != nil {
		return PRWorkspaceAggregate{}, false, err
	}
	return aggregate, result.Created, nil
}

// GetPRWorkspace returns a transactionally consistent aggregate projection.
func (s *Store) GetPRWorkspace(ctx context.Context, workspaceID string) (PRWorkspaceAggregate, error) {
	if err := s.ready(ctx); err != nil {
		return PRWorkspaceAggregate{}, err
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if !validPrefixedID(workspaceID, prWorkspaceIDPrefix) {
		return PRWorkspaceAggregate{}, fmt.Errorf("%w: invalid workspace ID", ErrInvalidPRWorkspace)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return PRWorkspaceAggregate{}, s.dbError(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN"); err != nil {
		return PRWorkspaceAggregate{}, s.dbError(err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	workspace, err := getPRWorkspaceRecord(ctx, conn, workspaceID)
	if err != nil {
		return PRWorkspaceAggregate{}, s.dbError(err)
	}
	aggregate, err := loadPRWorkspaceAggregateAtVersion(ctx, conn, workspaceID, workspace.Version)
	if err != nil {
		return PRWorkspaceAggregate{}, s.dbError(err)
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return PRWorkspaceAggregate{}, s.dbError(err)
	}
	committed = true
	return aggregate, nil
}

// ListPRWorkspaces returns current workspace projections in stable newest-first
// keyset order.
func (s *Store) ListPRWorkspaces(ctx context.Context, filter PRWorkspaceFilter) (PRWorkspacePage, error) {
	if err := s.ready(ctx); err != nil {
		return PRWorkspacePage{}, err
	}
	if filter.Phase != "" {
		if err := validatePRWorkspacePhase(filter.Phase); err != nil {
			return PRWorkspacePage{}, err
		}
	}
	if filter.ExecutionState != "" {
		if err := validatePRExecutionState(filter.ExecutionState); err != nil {
			return PRWorkspacePage{}, err
		}
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxPRWorkspaceListItems {
		limit = maxPRWorkspaceListItems
	}
	clauses := []string{"1 = 1"}
	args := make([]any, 0, 10)
	if value := strings.TrimSpace(filter.ProviderOrigin); value != "" {
		clauses = append(clauses, "provider_origin = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(filter.RepositoryID); value != "" {
		clauses = append(clauses, "repository_id = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(filter.Repository); value != "" {
		clauses = append(clauses, "repository = ? COLLATE NOCASE")
		args = append(args, value)
	}
	if filter.Phase != "" {
		clauses = append(clauses, "phase = ?")
		args = append(args, filter.Phase)
	}
	if filter.ExecutionState != "" {
		clauses = append(clauses, "execution_state = ?")
		args = append(args, filter.ExecutionState)
	}
	if filter.OwnedOnly != nil {
		clauses = append(clauses, "owned = ?")
		args = append(args, *filter.OwnedOnly)
	}
	if filter.HeadWritable != nil {
		clauses = append(clauses, "head_writable = ?")
		args = append(args, *filter.HeadWritable)
	}
	if filter.NeedsAction != nil {
		operator := "NOT IN"
		if *filter.NeedsAction {
			operator = "IN"
		}
		clauses = append(clauses, "execution_state "+operator+" ('waiting_gate', 'waiting_user', 'failed', 'blocked', 'unknown')")
	}
	if filter.After != nil {
		if filter.After.UpdatedAt.IsZero() || !validPrefixedID(strings.TrimSpace(filter.After.ID), prWorkspaceIDPrefix) {
			return PRWorkspacePage{}, fmt.Errorf("%w: invalid workspace cursor", ErrInvalidPRWorkspace)
		}
		clauses = append(clauses, "(updated_at < ? OR (updated_at = ? AND id < ?))")
		encoded := toDBTime(filter.After.UpdatedAt)
		args = append(args, encoded, encoded, strings.TrimSpace(filter.After.ID))
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+prWorkspaceColumns+`
		FROM pr_workspaces
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY updated_at DESC, id DESC
		LIMIT ?`, args...)
	if err != nil {
		return PRWorkspacePage{}, fmt.Errorf("list pull request workspaces: %w", s.dbError(err))
	}
	defer rows.Close()
	workspaces := make([]PRWorkspace, 0, limit+1)
	for rows.Next() {
		workspace, scanErr := scanPRWorkspace(rows)
		if scanErr != nil {
			return PRWorkspacePage{}, fmt.Errorf("scan pull request workspace list: %w", scanErr)
		}
		workspaces = append(workspaces, workspace)
	}
	if err := rows.Err(); err != nil {
		return PRWorkspacePage{}, fmt.Errorf("iterate pull request workspace list: %w", err)
	}
	page := PRWorkspacePage{Workspaces: workspaces}
	if len(page.Workspaces) > limit {
		page.Workspaces = page.Workspaces[:limit]
		last := page.Workspaces[len(page.Workspaces)-1]
		page.Next = &PRWorkspaceCursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	return page, nil
}

// ApplyPRWorkspaceMutation applies one typed child append/update under an
// aggregate-version CAS. A request replay returns the exact first result even
// after later aggregate versions exist.
func (s *Store) ApplyPRWorkspaceMutation(
	ctx context.Context,
	input PRWorkspaceMutation,
) (PRWorkspaceMutationResult, error) {
	if err := s.ready(ctx); err != nil {
		return PRWorkspaceMutationResult{}, err
	}
	input.WorkspaceID = strings.TrimSpace(input.WorkspaceID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if !validPrefixedID(input.WorkspaceID, prWorkspaceIDPrefix) {
		return PRWorkspaceMutationResult{}, fmt.Errorf("%w: invalid workspace ID", ErrInvalidPRWorkspace)
	}
	if input.ExpectedVersion <= 0 {
		return PRWorkspaceMutationResult{}, fmt.Errorf("%w: expected version must be positive", ErrInvalidPRWorkspace)
	}
	if err := validatePRWorkspaceRequestID(input.RequestID); err != nil {
		return PRWorkspaceMutationResult{}, err
	}
	decoded, canonical, err := decodePRWorkspaceMutation(input.Kind, input.Payload)
	if err != nil {
		return PRWorkspaceMutationResult{}, err
	}
	requestHash := hashPRWorkspaceRequest(string(input.Kind), canonical)
	var result PRWorkspaceMutationResult
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		if replay, ok, replayErr := loadPRWorkspaceMutationReplay(ctx, conn, input, requestHash); replayErr != nil {
			return replayErr
		} else if ok {
			result = replay
			return nil
		}
		workspace, getErr := getPRWorkspaceRecord(ctx, conn, input.WorkspaceID)
		if getErr != nil {
			return getErr
		}
		if workspace.Version != input.ExpectedVersion {
			return fmt.Errorf("%w: workspace version got %d want %d", ErrPRWorkspaceConflict, workspace.Version, input.ExpectedVersion)
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		entityID := workspace.ID
		created := false
		var changedRecords []*prWorkspaceMutationRecord
		if input.Kind == PRMutationWorkspaceState {
			change := decoded.(*PRWorkspaceStateChange)
			if _, execErr := conn.ExecContext(ctx, `
				UPDATE pr_workspaces SET phase = ?, execution_state = ?
				WHERE id = ?`, change.Phase, change.ExecutionState, workspace.ID); execErr != nil {
				return execErr
			}
		} else {
			record := decoded.(*prWorkspaceMutationRecord)
			changedRecords = []*prWorkspaceMutationRecord{record}
			var applyErr error
			entityID, created, applyErr = applyPRWorkspaceRecord(ctx, conn, workspace, record, now)
			if applyErr != nil {
				return applyErr
			}
		}
		if err := validatePRWorkspaceAggregateReferences(ctx, conn, workspace.ID); err != nil {
			return err
		}
		newVersion := workspace.Version + 1
		updated, execErr := conn.ExecContext(ctx, `
			UPDATE pr_workspaces SET version = ?, updated_at = ?
			WHERE id = ? AND version = ?`,
			newVersion, toDBTime(now), workspace.ID, workspace.Version,
		)
		if execErr != nil {
			return execErr
		}
		affected, rowsErr := updated.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		if affected != 1 {
			return fmt.Errorf("%w: workspace changed during mutation", ErrPRWorkspaceConflict)
		}
		if historyErr := appendPRWorkspaceHistory(ctx, conn, workspace.ID, newVersion, changedRecords, now); historyErr != nil {
			return historyErr
		}
		result = PRWorkspaceMutationResult{
			WorkspaceID: workspace.ID, WorkspaceVersion: newVersion,
			RequestID: input.RequestID, Kind: input.Kind, EntityID: entityID,
			Created: created, AppliedAt: now,
		}
		return insertPRWorkspaceRequest(ctx, conn, input.RequestID, workspace.ID, string(input.Kind), requestHash, result, now)
	})
	if err != nil {
		return PRWorkspaceMutationResult{}, fmt.Errorf("apply pull request workspace mutation: %w", s.dbError(err))
	}
	return result, nil
}

// ApplyPRWorkspacePatch persists one complete domain transition atomically.
// This is the preferred adapter boundary for pkg/prworkspace: no observer can
// see half a review/repair/gate transition and one CAS increment covers every
// record in the patch.
func (s *Store) ApplyPRWorkspacePatch(
	ctx context.Context,
	input PRWorkspacePatchMutation,
) (PRWorkspacePatchResult, error) {
	if err := s.ready(ctx); err != nil {
		return PRWorkspacePatchResult{}, err
	}
	input.WorkspaceID = strings.TrimSpace(input.WorkspaceID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if !validPrefixedID(input.WorkspaceID, prWorkspaceIDPrefix) {
		return PRWorkspacePatchResult{}, fmt.Errorf("%w: invalid workspace ID", ErrInvalidPRWorkspace)
	}
	if input.ExpectedVersion <= 0 {
		return PRWorkspacePatchResult{}, fmt.Errorf("%w: expected version must be positive", ErrInvalidPRWorkspace)
	}
	if err := validatePRWorkspaceRequestID(input.RequestID); err != nil {
		return PRWorkspacePatchResult{}, err
	}
	rawPatch, err := json.Marshal(input.Patch)
	if err != nil {
		return PRWorkspacePatchResult{}, fmt.Errorf("%w: encode patch: %v", ErrInvalidPRWorkspace, err)
	}
	var normalizedPatch PRWorkspacePatch
	if err := decodeStrictPRWorkspaceJSON(rawPatch, &normalizedPatch); err != nil {
		return PRWorkspacePatchResult{}, err
	}
	if err := validatePRWorkspacePatch(&normalizedPatch); err != nil {
		return PRWorkspacePatchResult{}, err
	}
	input.Patch = normalizedPatch
	canonical, err := json.Marshal(input.Patch)
	if err != nil {
		return PRWorkspacePatchResult{}, fmt.Errorf("%w: encode normalized patch: %v", ErrInvalidPRWorkspace, err)
	}
	if len(canonical) > 16<<20 {
		return PRWorkspacePatchResult{}, fmt.Errorf("%w: patch exceeds 16 MiB", ErrInvalidPRWorkspace)
	}
	requestHash := hashPRWorkspaceRequest("patch", canonical)
	var result PRWorkspacePatchResult
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		if replay, ok, replayErr := loadPRWorkspacePatchReplay(ctx, conn, input, requestHash); replayErr != nil {
			return replayErr
		} else if ok {
			result = replay
			result.Replayed = true
			return nil
		}
		workspace, getErr := getPRWorkspaceRecord(ctx, conn, input.WorkspaceID)
		if getErr != nil {
			return getErr
		}
		if workspace.Version != input.ExpectedVersion {
			return fmt.Errorf("%w: workspace version got %d want %d", ErrPRWorkspaceConflict, workspace.Version, input.ExpectedVersion)
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		records := prWorkspacePatchRecords(&input.Patch)
		for _, record := range records {
			if _, _, applyErr := applyPRWorkspaceRecord(ctx, conn, workspace, record, now); applyErr != nil {
				return applyErr
			}
		}
		if err := validatePRWorkspaceAggregateReferences(ctx, conn, workspace.ID); err != nil {
			return err
		}
		if input.Patch.ActiveCharterID != nil {
			charterID := strings.TrimSpace(*input.Patch.ActiveCharterID)
			if charterID != "" {
				var found int
				if err := conn.QueryRowContext(ctx, `SELECT 1 FROM pr_charter_revisions
					WHERE id = ? AND workspace_id = ? AND status = 'confirmed'`, charterID, workspace.ID).Scan(&found); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						return fmt.Errorf("%w: active charter is not confirmed in workspace", ErrPRWorkspaceConflict)
					}
					return err
				}
			}
			if _, err := conn.ExecContext(ctx, `UPDATE pr_workspaces SET active_charter_id = ? WHERE id = ?`, charterID, workspace.ID); err != nil {
				return err
			}
		}
		if input.Patch.Phase != nil {
			if _, err := conn.ExecContext(ctx, `UPDATE pr_workspaces SET phase = ? WHERE id = ?`, *input.Patch.Phase, workspace.ID); err != nil {
				return err
			}
		}
		if input.Patch.ExecutionState != nil {
			if _, err := conn.ExecContext(ctx, `UPDATE pr_workspaces SET execution_state = ? WHERE id = ?`, *input.Patch.ExecutionState, workspace.ID); err != nil {
				return err
			}
		}
		newVersion := workspace.Version + 1
		updated, err := conn.ExecContext(ctx, `UPDATE pr_workspaces SET version = ?, updated_at = ?
			WHERE id = ? AND version = ?`, newVersion, toDBTime(now), workspace.ID, workspace.Version)
		if err != nil {
			return err
		}
		affected, err := updated.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("%w: workspace changed during patch", ErrPRWorkspaceConflict)
		}
		if historyErr := appendPRWorkspaceHistory(ctx, conn, workspace.ID, newVersion, records, now); historyErr != nil {
			return historyErr
		}
		aggregate, err := loadPRWorkspaceAggregateAtVersion(ctx, conn, workspace.ID, newVersion)
		if err != nil {
			return err
		}
		result = PRWorkspacePatchResult{Aggregate: aggregate}
		receipt := prWorkspacePatchReceipt{WorkspaceID: workspace.ID, WorkspaceVersion: newVersion}
		return insertPRWorkspaceRequest(ctx, conn, input.RequestID, workspace.ID, "patch", requestHash, receipt, now)
	})
	if err != nil {
		return PRWorkspacePatchResult{}, fmt.Errorf("apply pull request workspace patch: %w", s.dbError(err))
	}
	return result, nil
}

func validatePRWorkspacePatch(patch *PRWorkspacePatch) error {
	if patch == nil {
		return fmt.Errorf("%w: patch is required", ErrInvalidPRWorkspace)
	}
	if patch.Phase != nil {
		if err := validatePRWorkspacePhase(*patch.Phase); err != nil {
			return err
		}
	}
	if patch.ExecutionState != nil {
		if err := validatePRExecutionState(*patch.ExecutionState); err != nil {
			return err
		}
	}
	if patch.ActiveCharterID != nil {
		id := strings.TrimSpace(*patch.ActiveCharterID)
		if id != "" && !validPrefixedID(id, prCharterRevisionIDPrefix) {
			return fmt.Errorf("%w: invalid current charter ID", ErrInvalidPRWorkspace)
		}
	}
	count := 0
	if patch.ProviderSnapshot != nil {
		normalizePRProviderSnapshot(patch.ProviderSnapshot)
		if patch.ProviderSnapshot.ID != "" {
			return fmt.Errorf("%w: provider snapshot append must omit ID", ErrInvalidPRWorkspace)
		}
		if err := validatePRWorkspaceRecord(patch.ProviderSnapshot); err != nil {
			return err
		}
		count++
	}
	for _, records := range [][]int{{len(patch.AppendCharters), len(patch.ReplaceCharters)}, {len(patch.AppendStageRuns), len(patch.ReplaceStageRuns)}, {len(patch.UpsertFindings)}, {len(patch.AppendFindingEvents)}, {len(patch.AppendConversations), len(patch.ReplaceConversations)}, {len(patch.AppendMessages)}, {len(patch.AppendCorrections), len(patch.ReplaceCorrections)}, {len(patch.AppendLessons), len(patch.ReplaceLessons)}, {len(patch.AppendNudgeRounds), len(patch.ReplaceNudgeRounds)}, {len(patch.AppendNudgeRewards)}, {len(patch.UpsertDeferredGroups)}, {len(patch.UpsertDeferredItems)}, {len(patch.AppendRepairAttempts), len(patch.ReplaceRepairAttempts)}, {len(patch.AppendValidationRuns), len(patch.ReplaceValidationRuns)}, {len(patch.AppendGateRuns), len(patch.ReplaceGateRuns)}, {len(patch.AppendGateDecisions)}, {len(patch.AppendPublications), len(patch.ReplacePublications)}, {len(patch.AppendOperationIntents), len(patch.ReplaceOperationIntents)}, {len(patch.UpsertIngressWatermarks)}, {len(patch.AppendActivity)}} {
		for _, size := range records {
			count += size
		}
	}
	if count > 2048 {
		return fmt.Errorf("%w: patch exceeds 2048 records", ErrInvalidPRWorkspace)
	}
	if count == 0 && patch.Phase == nil && patch.ExecutionState == nil && patch.ActiveCharterID == nil {
		return fmt.Errorf("%w: empty patch", ErrInvalidPRWorkspace)
	}
	for _, record := range prWorkspacePatchRecords(patch) {
		if record.table != "pr_provider_snapshots" {
			if record.meta.ID == "" {
				return fmt.Errorf("%w: %s patch record requires a stable ID", ErrInvalidPRWorkspace, record.table)
			}
			if !validPrefixedID(record.meta.ID, record.prefix) {
				return fmt.Errorf("%w: invalid %s record ID", ErrInvalidPRWorkspace, record.table)
			}
		}
		if err := validatePRWorkspaceRecord(record.value); err != nil {
			return err
		}
	}
	return nil
}

func prWorkspacePatchRecords(patch *PRWorkspacePatch) []*prWorkspaceMutationRecord {
	if patch == nil {
		return nil
	}
	result := make([]*prWorkspaceMutationRecord, 0, 64)
	add := func(table, prefix string, immutable bool, value any, meta *PRWorkspaceRecord, status string, appendOnly bool) {
		mode := "upsert"
		if appendOnly {
			mode = "append"
		}
		result = append(result, &prWorkspaceMutationRecord{table: table, prefix: prefix, immutable: immutable, mode: mode, value: value, meta: meta, status: status})
	}
	if patch.ProviderSnapshot != nil {
		add("pr_provider_snapshots", prProviderSnapshotIDPrefix, true, patch.ProviderSnapshot, &patch.ProviderSnapshot.PRWorkspaceRecord, "observed", true)
	}
	for i := range patch.AppendCharters {
		v := &patch.AppendCharters[i]
		add("pr_charter_revisions", prCharterRevisionIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.Status), true)
	}
	for i := range patch.ReplaceCharters {
		v := &patch.ReplaceCharters[i]
		add("pr_charter_revisions", prCharterRevisionIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.Status), false)
	}
	for i := range patch.AppendStageRuns {
		v := &patch.AppendStageRuns[i]
		add("pr_stage_runs", prStageRunIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.State), true)
	}
	for i := range patch.ReplaceStageRuns {
		v := &patch.ReplaceStageRuns[i]
		add("pr_stage_runs", prStageRunIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.State), false)
	}
	// Nudge rounds precede findings so a domain-preassigned finding can point
	// at the round that discovered it in the same atomic patch.
	for i := range patch.AppendNudgeRounds {
		v := &patch.AppendNudgeRounds[i]
		add("pr_nudge_rounds", prNudgeRoundIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.State), true)
	}
	for i := range patch.ReplaceNudgeRounds {
		v := &patch.ReplaceNudgeRounds[i]
		add("pr_nudge_rounds", prNudgeRoundIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.State), false)
	}
	for i := range patch.UpsertFindings {
		v := &patch.UpsertFindings[i]
		add("pr_findings", prFindingIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.Disposition), false)
	}
	for i := range patch.AppendFindingEvents {
		v := &patch.AppendFindingEvents[i]
		add("pr_finding_events", prFindingEventIDPrefix, true, v, &v.PRWorkspaceRecord, v.Kind, true)
	}
	for i := range patch.AppendConversations {
		v := &patch.AppendConversations[i]
		add("pr_conversations", prConversationIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.Status), true)
	}
	for i := range patch.ReplaceConversations {
		v := &patch.ReplaceConversations[i]
		add("pr_conversations", prConversationIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.Status), false)
	}
	// Corrections precede messages so a message can link a correction created in the same patch.
	for i := range patch.AppendCorrections {
		v := &patch.AppendCorrections[i]
		add("pr_corrections", prCorrectionIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.Status), true)
	}
	for i := range patch.ReplaceCorrections {
		v := &patch.ReplaceCorrections[i]
		add("pr_corrections", prCorrectionIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.Status), false)
	}
	for i := range patch.AppendLessons {
		v := &patch.AppendLessons[i]
		add("pr_repository_lessons", prRepositoryLessonIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.Status), true)
	}
	for i := range patch.ReplaceLessons {
		v := &patch.ReplaceLessons[i]
		add("pr_repository_lessons", prRepositoryLessonIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.Status), false)
	}
	for i := range patch.AppendMessages {
		v := &patch.AppendMessages[i]
		add("pr_messages", prMessageIDPrefix, true, v, &v.PRWorkspaceRecord, v.Role, true)
	}
	for i := range patch.AppendNudgeRewards {
		v := &patch.AppendNudgeRewards[i]
		add("pr_nudge_rewards", prNudgeRewardIDPrefix, true, v, &v.PRWorkspaceRecord, "resolved", true)
	}
	for i := range patch.UpsertDeferredGroups {
		v := &patch.UpsertDeferredGroups[i]
		v.Items = nil
		add("pr_deferred_groups", prDeferredGroupIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.Status), false)
	}
	for i := range patch.UpsertDeferredItems {
		v := &patch.UpsertDeferredItems[i]
		status := "active"
		if v.Removed {
			status = "removed"
		}
		add("pr_deferred_group_items", prDeferredGroupItemIDPrefix, false, v, &v.PRWorkspaceRecord, status, false)
	}
	for i := range patch.AppendRepairAttempts {
		v := &patch.AppendRepairAttempts[i]
		add("pr_repair_attempts", prRepairAttemptIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.State), true)
	}
	for i := range patch.ReplaceRepairAttempts {
		v := &patch.ReplaceRepairAttempts[i]
		add("pr_repair_attempts", prRepairAttemptIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.State), false)
	}
	for i := range patch.AppendValidationRuns {
		v := &patch.AppendValidationRuns[i]
		add("pr_validation_runs", prValidationRunIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.State), true)
	}
	for i := range patch.ReplaceValidationRuns {
		v := &patch.ReplaceValidationRuns[i]
		add("pr_validation_runs", prValidationRunIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.State), false)
	}
	for i := range patch.AppendGateRuns {
		v := &patch.AppendGateRuns[i]
		add("pr_gate_runs", prGateRunIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.State), true)
	}
	for i := range patch.ReplaceGateRuns {
		v := &patch.ReplaceGateRuns[i]
		add("pr_gate_runs", prGateRunIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.State), false)
	}
	for i := range patch.AppendGateDecisions {
		v := &patch.AppendGateDecisions[i]
		add("pr_gate_decisions", prGateDecisionIDPrefix, true, v, &v.PRWorkspaceRecord, string(v.Outcome), true)
	}
	for i := range patch.AppendPublications {
		v := &patch.AppendPublications[i]
		add("pr_publications", prPublicationIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.Status), true)
	}
	for i := range patch.ReplacePublications {
		v := &patch.ReplacePublications[i]
		add("pr_publications", prPublicationIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.Status), false)
	}
	for i := range patch.AppendOperationIntents {
		v := &patch.AppendOperationIntents[i]
		add("pr_operation_intents", prOperationIntentIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.State), true)
	}
	for i := range patch.ReplaceOperationIntents {
		v := &patch.ReplaceOperationIntents[i]
		add("pr_operation_intents", prOperationIntentIDPrefix, false, v, &v.PRWorkspaceRecord, string(v.State), false)
	}
	for i := range patch.UpsertIngressWatermarks {
		v := &patch.UpsertIngressWatermarks[i]
		add("pr_ingress_watermarks", prIngressWatermarkIDPrefix, false, v, &v.PRWorkspaceRecord, "observed", false)
	}
	for i := range patch.AppendActivity {
		v := &patch.AppendActivity[i]
		add("pr_activity", prActivityIDPrefix, true, v, &v.PRWorkspaceRecord, v.Kind, true)
	}
	return result
}

func loadPRWorkspacePatchReplay(ctx context.Context, conn *sql.Conn, input PRWorkspacePatchMutation, requestHash string) (PRWorkspacePatchResult, bool, error) {
	var workspaceID, kind, storedHash string
	var payload []byte
	err := conn.QueryRowContext(ctx, `SELECT workspace_id, kind, request_hash, result_json
		FROM pr_workspace_requests WHERE request_id = ?`, input.RequestID).Scan(&workspaceID, &kind, &storedHash, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return PRWorkspacePatchResult{}, false, nil
	}
	if err != nil {
		return PRWorkspacePatchResult{}, false, err
	}
	if workspaceID != input.WorkspaceID || kind != "patch" || storedHash != requestHash {
		return PRWorkspacePatchResult{}, false, fmt.Errorf("%w: request ID reused with different content", ErrPRWorkspaceConflict)
	}
	var receipt prWorkspacePatchReceipt
	if err := json.Unmarshal(payload, &receipt); err != nil {
		return PRWorkspacePatchResult{}, false, fmt.Errorf("decode workspace patch replay: %w", err)
	}
	aggregate, err := loadPRWorkspaceAggregateAtVersion(ctx, conn, receipt.WorkspaceID, receipt.WorkspaceVersion)
	if err != nil {
		return PRWorkspacePatchResult{}, false, err
	}
	return PRWorkspacePatchResult{Aggregate: aggregate}, true, nil
}

func applyPRWorkspaceRecord(
	ctx context.Context,
	conn *sql.Conn,
	workspace PRWorkspace,
	record *prWorkspaceMutationRecord,
	now time.Time,
) (string, bool, error) {
	if record == nil || record.meta == nil {
		return "", false, fmt.Errorf("%w: missing mutation record", ErrInvalidPRWorkspace)
	}
	if record.meta.WorkspaceID != "" && record.meta.WorkspaceID != workspace.ID {
		return "", false, fmt.Errorf("%w: record belongs to another workspace", ErrPRWorkspaceConflict)
	}
	if record.mode != "append" && record.mode != "replace" && record.mode != "upsert" {
		return "", false, fmt.Errorf("%w: invalid %s mutation mode", ErrInvalidPRWorkspace, record.table)
	}
	if record.meta.ID != "" && !validPrefixedID(record.meta.ID, record.prefix) {
		return "", false, fmt.Errorf("%w: invalid %s record ID", ErrInvalidPRWorkspace, record.table)
	}
	created := record.meta.ID == ""
	var existingPayload []byte
	if record.meta.ID != "" {
		var existingWorkspace string
		var existingOrdinal, existingCreatedAt int64
		err := conn.QueryRowContext(ctx,
			"SELECT workspace_id, ordinal, created_at, payload_json FROM "+record.table+" WHERE id = ?",
			record.meta.ID,
		).Scan(&existingWorkspace, &existingOrdinal, &existingCreatedAt, &existingPayload)
		if errors.Is(err, sql.ErrNoRows) {
			if record.mode == "replace" {
				return "", false, fmt.Errorf("%w: replace target does not exist", ErrPRWorkspaceConflict)
			}
			created = true
		} else if err != nil {
			return "", false, err
		} else {
			if record.mode == "append" {
				return "", false, fmt.Errorf("%w: append target already exists", ErrPRWorkspaceConflict)
			}
			if existingWorkspace != workspace.ID {
				return "", false, fmt.Errorf("%w: record belongs to another workspace", ErrPRWorkspaceConflict)
			}
			if record.immutable {
				return "", false, fmt.Errorf("%w: %s records are immutable", ErrPRWorkspaceConflict, record.table)
			}
			if record.meta.Ordinal != 0 && record.meta.Ordinal != existingOrdinal {
				return "", false, fmt.Errorf("%w: record ordinal changed", ErrPRWorkspaceConflict)
			}
			record.meta.Ordinal = existingOrdinal
			record.meta.CreatedAt = fromDBTime(existingCreatedAt)
			created = false
		}
	} else if record.mode == "replace" {
		return "", false, fmt.Errorf("%w: replace record ID is required", ErrInvalidPRWorkspace)
	}
	if created {
		if record.meta.ID == "" {
			id, err := newPrefixedID(record.prefix)
			if err != nil {
				return "", false, err
			}
			record.meta.ID = id
		}
		var nextOrdinal int64
		if err := conn.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(ordinal), 0) + 1 FROM "+record.table+" WHERE workspace_id = ?",
			workspace.ID,
		).Scan(&nextOrdinal); err != nil {
			return "", false, err
		}
		record.meta.Ordinal = nextOrdinal
		record.meta.CreatedAt = now
		switch value := record.value.(type) {
		case *PRPublication:
			waitingForGate := value.Status == PRPublicationUnknown &&
				(value.ExecutionState == PRExecutionWaitingGate || value.ExecutionState == PRExecutionWaitingUser)
			if (value.Status != PRPublicationPending && !waitingForGate) || value.Attempts != 0 {
				return "", false, fmt.Errorf("%w: publication must start pending or gate-waiting with zero attempts", ErrInvalidPRWorkspace)
			}
		case *PRWorkspaceOperationIntent:
			if value.State != PRExecutionQueued || value.Attempts != 0 {
				return "", false, fmt.Errorf("%w: operation must start queued with zero attempts", ErrInvalidPRWorkspace)
			}
		}
	}
	record.meta.WorkspaceID = workspace.ID
	record.meta.UpdatedAt = now
	if provider, ok := record.value.(*PRProviderSnapshot); ok && provider.ObservedAt.IsZero() {
		provider.ObservedAt = now
	}
	if err := validatePRWorkspaceRecord(record.value); err != nil {
		return "", false, err
	}
	if !created {
		if err := validatePRWorkspaceRecordTransition(existingPayload, record.value); err != nil {
			return "", false, err
		}
	}
	if err := validatePRWorkspaceReferences(ctx, conn, workspace.ID, record.value); err != nil {
		return "", false, err
	}
	payload, err := json.Marshal(record.value)
	if err != nil {
		return "", false, fmt.Errorf("%w: encode mutation record: %v", ErrInvalidPRWorkspace, err)
	}
	if len(payload) < 2 || len(payload) > MaxPRWorkspaceRecordBytes {
		return "", false, fmt.Errorf("%w: record payload has %d bytes, maximum %d", ErrInvalidPRWorkspace, len(payload), MaxPRWorkspaceRecordBytes)
	}
	if created {
		if err := insertPRWorkspaceRecord(ctx, conn, workspace.ID, record, payload); err != nil {
			return "", false, err
		}
	} else {
		updated, err := updatePRWorkspaceRecord(ctx, conn, workspace.ID, record, payload)
		if err != nil {
			return "", false, err
		}
		affected, err := updated.RowsAffected()
		if err != nil {
			return "", false, err
		}
		if affected != 1 {
			return "", false, fmt.Errorf("%w: record changed during update", ErrPRWorkspaceConflict)
		}
	}

	switch value := record.value.(type) {
	case *PRProviderSnapshot:
		if value.Provider != workspace.Provider || value.ProviderOrigin != workspace.ProviderOrigin ||
			value.RepositoryID != workspace.RepositoryID || value.PullRequestID != workspace.PullRequestID {
			return "", false, fmt.Errorf("%w: provider snapshot changes immutable PR identity", ErrPRWorkspaceConflict)
		}
		if _, err := conn.ExecContext(ctx, `
			UPDATE pr_workspaces SET
				repository = ?, pull_number = ?, provider_head_sha = ?, owned = ?,
				head_writable = ?, current_provider_ordinal = ?
			WHERE id = ?`,
			value.Repository, value.PullNumber, value.HeadSHA, value.Owned,
			value.HeadWritable, value.Ordinal, workspace.ID,
		); err != nil {
			return "", false, err
		}
	case *PRCharterRevision:
		if value.Status == PRRecordConfirmed {
			if err := supersedeConfirmedPRCharters(ctx, conn, workspace.ID, value.ID, now); err != nil {
				return "", false, err
			}
			if _, err := conn.ExecContext(ctx, `UPDATE pr_workspaces
				SET active_charter_id = ?, phase = 'review', execution_state = 'queued'
				WHERE id = ?`, value.ID, workspace.ID); err != nil {
				return "", false, err
			}
		}
	}
	return record.meta.ID, created, nil
}

func insertPRWorkspaceRecord(ctx context.Context, conn *sql.Conn, workspaceID string, record *prWorkspaceMutationRecord, payload []byte) error {
	var err error
	switch value := record.value.(type) {
	case *PRDeferredGroupItem:
		_, err = conn.ExecContext(ctx, `INSERT INTO pr_deferred_group_items (
			id, workspace_id, ordinal, status, group_id, finding_id, ordinal_in_group,
			payload_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.meta.ID, workspaceID,
			record.meta.Ordinal, record.status, value.GroupID, value.FindingID, value.OrdinalInGroup,
			payload, toDBTime(record.meta.CreatedAt), toDBTime(record.meta.UpdatedAt))
	case *PRPublication:
		_, err = conn.ExecContext(ctx, `INSERT INTO pr_publications (
			id, workspace_id, ordinal, status, available_at, lease_owner, lease_token,
			lease_until, attempts, payload_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.meta.ID, workspaceID,
			record.meta.Ordinal, record.status, toDBTime(value.AvailableAt), value.LeaseOwner,
			value.LeaseToken, nullablePRWorkspaceTime(value.LeaseUntil), value.Attempts, payload,
			toDBTime(record.meta.CreatedAt), toDBTime(record.meta.UpdatedAt))
	case *PRWorkspaceOperationIntent:
		_, err = conn.ExecContext(ctx, `INSERT INTO pr_operation_intents (
			id, workspace_id, ordinal, status, available_at, lease_owner, lease_token,
			lease_until, attempts, payload_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.meta.ID, workspaceID,
			record.meta.Ordinal, record.status, toDBTime(value.AvailableAt), value.LeaseOwner,
			value.LeaseToken, nullablePRWorkspaceTime(value.LeaseUntil), value.Attempts, payload,
			toDBTime(record.meta.CreatedAt), toDBTime(record.meta.UpdatedAt))
	default:
		_, err = conn.ExecContext(ctx, "INSERT INTO "+record.table+` (
			id, workspace_id, ordinal, status, payload_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`, record.meta.ID, workspaceID, record.meta.Ordinal,
			record.status, payload, toDBTime(record.meta.CreatedAt), toDBTime(record.meta.UpdatedAt))
	}
	return err
}

func updatePRWorkspaceRecord(ctx context.Context, conn *sql.Conn, workspaceID string, record *prWorkspaceMutationRecord, payload []byte) (sql.Result, error) {
	switch value := record.value.(type) {
	case *PRDeferredGroupItem:
		return conn.ExecContext(ctx, `UPDATE pr_deferred_group_items SET status = ?,
			group_id = ?, finding_id = ?, ordinal_in_group = ?, payload_json = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ?`, record.status, value.GroupID, value.FindingID,
			value.OrdinalInGroup, payload, toDBTime(record.meta.UpdatedAt), record.meta.ID, workspaceID)
	case *PRPublication:
		return conn.ExecContext(ctx, `UPDATE pr_publications SET status = ?, available_at = ?,
			lease_owner = ?, lease_token = ?, lease_until = ?, attempts = ?, payload_json = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ?`, record.status, toDBTime(value.AvailableAt), value.LeaseOwner,
			value.LeaseToken, nullablePRWorkspaceTime(value.LeaseUntil), value.Attempts, payload,
			toDBTime(record.meta.UpdatedAt), record.meta.ID, workspaceID)
	case *PRWorkspaceOperationIntent:
		return conn.ExecContext(ctx, `UPDATE pr_operation_intents SET status = ?, available_at = ?,
			lease_owner = ?, lease_token = ?, lease_until = ?, attempts = ?, payload_json = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ?`, record.status, toDBTime(value.AvailableAt), value.LeaseOwner,
			value.LeaseToken, nullablePRWorkspaceTime(value.LeaseUntil), value.Attempts, payload,
			toDBTime(record.meta.UpdatedAt), record.meta.ID, workspaceID)
	default:
		return conn.ExecContext(ctx, "UPDATE "+record.table+`
			SET status = ?, payload_json = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ?`, record.status, payload,
			toDBTime(record.meta.UpdatedAt), record.meta.ID, workspaceID)
	}
}

func nullablePRWorkspaceTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return toDBTime(*value)
}

func supersedeConfirmedPRCharters(
	ctx context.Context,
	conn *sql.Conn,
	workspaceID, exceptID string,
	now time.Time,
) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT id, payload_json FROM pr_charter_revisions
		WHERE workspace_id = ? AND status = 'confirmed' AND id <> ?
		ORDER BY ordinal`, workspaceID, exceptID)
	if err != nil {
		return err
	}
	type charterRow struct {
		id      string
		charter PRCharterRevision
	}
	var updates []charterRow
	for rows.Next() {
		var id string
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			_ = rows.Close()
			return err
		}
		var charter PRCharterRevision
		if err := json.Unmarshal(payload, &charter); err != nil {
			_ = rows.Close()
			return fmt.Errorf("decode confirmed charter %s: %w", id, err)
		}
		charter.Status = PRRecordSuperseded
		charter.UpdatedAt = now
		updates = append(updates, charterRow{id: id, charter: charter})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, update := range updates {
		payload, err := json.Marshal(update.charter)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE pr_charter_revisions
			SET status = 'superseded', payload_json = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ?`,
			payload, toDBTime(now), update.id, workspaceID,
		); err != nil {
			return err
		}
	}
	return nil
}

func validatePRWorkspaceReferences(
	ctx context.Context,
	conn *sql.Conn,
	workspaceID string,
	value any,
) error {
	require := func(table, id, field string) error {
		if strings.TrimSpace(id) == "" {
			return nil
		}
		var found int
		err := conn.QueryRowContext(ctx,
			"SELECT 1 FROM "+table+" WHERE id = ? AND workspace_id = ?",
			id, workspaceID,
		).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s does not belong to workspace", ErrPRWorkspaceConflict, field)
		}
		return err
	}
	switch record := value.(type) {
	case *PRCharterRevision:
		return require("pr_charter_revisions", record.SupersedesID, "supersedes_id")
	case *PRStageRun:
		return require("pr_charter_revisions", record.CharterID, "charter_id")
	case *PRFinding:
		if err := require("pr_stage_runs", record.StageRunID, "stage_run_id"); err != nil {
			return err
		}
		if record.DeferredGroupID != "" {
			return fmt.Errorf("%w: deferred membership is owned by group items", ErrInvalidPRWorkspace)
		}
		return nil
	case *PRFindingEvent:
		return require("pr_findings", record.FindingID, "finding_id")
	case *PRMessage:
		for _, ref := range []struct{ table, id, field string }{
			{"pr_conversations", record.ConversationID, "conversation_id"},
			{"pr_stage_runs", record.StageRunID, "stage_run_id"},
			{"pr_findings", record.FindingID, "finding_id"},
			{"pr_corrections", record.CorrectionID, "correction_id"},
			{"pr_charter_revisions", record.CharterID, "charter_id"},
		} {
			if err := require(ref.table, ref.id, ref.field); err != nil {
				return err
			}
		}
	case *PRRepositoryLesson:
		var repositoryID string
		if err := conn.QueryRowContext(ctx, `SELECT repository_id FROM pr_workspaces WHERE id = ?`, workspaceID).Scan(&repositoryID); err != nil {
			return err
		}
		if record.RepositoryID != repositoryID {
			return fmt.Errorf("%w: repository lesson must match source workspace repository", ErrPRWorkspaceConflict)
		}
		return require("pr_corrections", record.SourceCorrectionID, "source_correction_id")
	case *PRCorrection:
		for _, ref := range []struct{ table, id, field string }{
			{"pr_stage_runs", record.StageRunID, "stage_run_id"},
			{"pr_charter_revisions", record.CharterID, "charter_id"},
			{"pr_corrections", record.SupersedesID, "supersedes_id"},
			{"pr_repository_lessons", record.RepositoryLessonID, "repository_lesson_id"},
		} {
			if err := require(ref.table, ref.id, ref.field); err != nil {
				return err
			}
		}
		targetTable := map[string]string{
			"finding": "pr_findings", "stage_run": "pr_stage_runs", "charter": "pr_charter_revisions",
			"message": "pr_messages", "gate": "pr_gate_runs", "publication": "pr_publications",
		}[record.TargetKind]
		if record.TargetID != "" {
			if record.TargetKind == "workspace" {
				if record.TargetID != workspaceID {
					return fmt.Errorf("%w: correction target workspace does not match", ErrPRWorkspaceConflict)
				}
				return nil
			}
			if targetTable != "" {
				return require(targetTable, record.TargetID, "target_id")
			}
		}
	case *PRNudgeRound:
		return require("pr_stage_runs", record.StageRunID, "stage_run_id")
	case *PRNudgeReward:
		if err := require("pr_nudge_rounds", record.NudgeRoundID, "nudge_round_id"); err != nil {
			return err
		}
		return require("pr_findings", record.FindingID, "finding_id")
	case *PRDeferredGroupItem:
		if err := require("pr_findings", record.FindingID, "finding_id"); err != nil {
			return err
		}
		if record.Removed {
			return nil
		}
		if err := require("pr_deferred_groups", record.GroupID, "group_id"); err != nil {
			return err
		}
		return validatePRDeferredGroupItem(ctx, conn, workspaceID, record)
	case *PRRepairAttempt:
		if err := require("pr_stage_runs", record.StageRunID, "stage_run_id"); err != nil {
			return err
		}
		for _, findingID := range record.FindingIDs {
			if err := require("pr_findings", findingID, "repair finding_id"); err != nil {
				return err
			}
		}
	case *PRValidationRun:
		if err := require("pr_stage_runs", record.StageRunID, "stage_run_id"); err != nil {
			return err
		}
		return require("pr_repair_attempts", record.RepairAttemptID, "repair_attempt_id")
	case *PRGateDecision:
		return require("pr_gate_runs", record.GateRunID, "gate_run_id")
	case *PRPublication:
		if err := require("pr_gate_runs", record.GateRunID, "gate_run_id"); err != nil {
			return err
		}
		if err := require("pr_deferred_groups", record.DeferredGroupID, "deferred_group_id"); err != nil {
			return err
		}
		for _, findingID := range record.FindingIDs {
			if err := require("pr_findings", findingID, "publication finding_id"); err != nil {
				return err
			}
		}
	case *PRWorkspaceOperationIntent:
		if err := require("pr_stage_runs", record.StageRunID, "stage_run_id"); err != nil {
			return err
		}
		return require("pr_charter_revisions", record.InputCharterID, "input_charter_id")
	}
	return nil
}

func validatePRWorkspaceAggregateReferences(ctx context.Context, conn *sql.Conn, workspaceID string) error {
	require := func(table, id, field string) error {
		if id == "" {
			return nil
		}
		var found int
		err := conn.QueryRowContext(ctx, "SELECT 1 FROM "+table+" WHERE id = ? AND workspace_id = ?", id, workspaceID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s does not belong to workspace", ErrPRWorkspaceConflict, field)
		}
		return err
	}
	findings, err := loadPRWorkspaceRecords[PRFinding](ctx, conn, "pr_findings", workspaceID)
	if err != nil {
		return err
	}
	for _, finding := range findings {
		if err := require("pr_nudge_rounds", finding.NudgeRoundID, "finding nudge_round_id"); err != nil {
			return err
		}
	}
	findingDisposition := make(map[string]PRFindingDisposition, len(findings))
	for _, finding := range findings {
		findingDisposition[finding.ID] = finding.Disposition
	}
	items, err := loadPRWorkspaceRecords[PRDeferredGroupItem](ctx, conn, "pr_deferred_group_items", workspaceID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if !item.Removed && findingDisposition[item.FindingID] != PRFindingDeferred {
			return fmt.Errorf("%w: non-deferred finding remains in deferred group", ErrPRWorkspaceConflict)
		}
	}
	rounds, err := loadPRWorkspaceRecords[PRNudgeRound](ctx, conn, "pr_nudge_rounds", workspaceID)
	if err != nil {
		return err
	}
	for _, round := range rounds {
		for _, findingID := range round.FindingIDs {
			if err := require("pr_findings", findingID, "nudge finding_id"); err != nil {
				return err
			}
		}
	}
	groups, err := loadPRWorkspaceRecords[PRDeferredGroup](ctx, conn, "pr_deferred_groups", workspaceID)
	if err != nil {
		return err
	}
	for _, group := range groups {
		if err := require("pr_publications", group.PublicationID, "deferred publication_id"); err != nil {
			return err
		}
	}
	return nil
}

func validatePRWorkspaceRecordTransition(existing []byte, next any) error {
	conflict := func(label string) error {
		return fmt.Errorf("%w: immutable %s changed", ErrPRWorkspaceConflict, label)
	}
	equal := func(label string, before, after any) error {
		left, err := json.Marshal(before)
		if err != nil {
			return err
		}
		right, err := json.Marshal(after)
		if err != nil {
			return err
		}
		if !bytes.Equal(left, right) {
			return conflict(label)
		}
		return nil
	}
	switch value := next.(type) {
	case *PRCharterRevision:
		var old PRCharterRevision
		if err := json.Unmarshal(existing, &old); err != nil {
			return err
		}
		if old.Status == PRRecordConfirmed || old.Status == PRRecordSuperseded {
			return conflict("confirmed charter")
		}
		if old.Revision != value.Revision {
			return conflict("charter revision")
		}
	case *PRStageRun:
		var old PRStageRun
		if err := json.Unmarshal(existing, &old); err != nil {
			return err
		}
		immutable := func(v PRStageRun) any {
			return struct {
				Phase                                          PRWorkspacePhase
				Kind, CharterID                                string
				WorkspaceVersion                               int64
				Attempt                                        int
				BaseSHA, HeadSHA, AgentID, Model, PromptDigest string
			}{v.Phase, v.Kind, v.CharterID, v.WorkspaceVersion, v.Attempt, v.BaseSHA, v.HeadSHA, v.AgentID, v.Model, v.PromptDigest}
		}
		if err := equal("stage inputs", immutable(old), immutable(*value)); err != nil {
			return err
		}
		if len(old.Evidence) > 0 {
			equalEvidence, err := equalPRWorkspaceRawJSON(old.Evidence, value.Evidence)
			if err != nil {
				return err
			}
			if !equalEvidence {
				return conflict("stage evidence")
			}
		}
		return nil
	case *PRFinding:
		var old PRFinding
		if err := json.Unmarshal(existing, &old); err != nil {
			return err
		}
		return equal("finding provenance", []string{old.Origin, old.StageRunID, old.NudgeRoundID, old.ExternalID, old.Fingerprint}, []string{value.Origin, value.StageRunID, value.NudgeRoundID, value.ExternalID, value.Fingerprint})
	case *PRConversation:
		var old PRConversation
		if err := json.Unmarshal(existing, &old); err != nil {
			return err
		}
		return equal("conversation identity", []string{old.Channel, string(old.Phase)}, []string{value.Channel, string(value.Phase)})
	case *PRCorrection:
		var old PRCorrection
		if err := json.Unmarshal(existing, &old); err != nil {
			return err
		}
		immutable := func(v PRCorrection) any {
			return struct {
				Kind, TargetKind, TargetID, StageRunID, OriginalClaim, Correction, Reason, Evidence string
				Review, Implementation                                                              bool
				CharterID, HeadSHA, SupersedesID                                                    string
			}{v.Kind, v.TargetKind, v.TargetID, v.StageRunID, v.OriginalClaim, v.Correction, v.Reason, v.Evidence, v.AppliesToReview, v.AppliesToImplement, v.CharterID, v.HeadSHA, v.SupersedesID}
		}
		return equal("correction provenance", immutable(old), immutable(*value))
	case *PRRepositoryLesson:
		var old PRRepositoryLesson
		if err := json.Unmarshal(existing, &old); err != nil {
			return err
		}
		immutable := func(v PRRepositoryLesson) any {
			return struct {
				RepositoryID, Kind, Content, SourceCorrectionID string
				Types                                           []PRType
				Phases                                          []PRWorkspacePhase
				ConfirmedBy                                     string
			}{v.RepositoryID, v.Kind, v.Content, v.SourceCorrectionID, v.ApplicableTypes, v.ApplicablePhases, v.ConfirmedBy}
		}
		return equal("repository lesson", immutable(old), immutable(*value))
	case *PRNudgeRound:
		var old PRNudgeRound
		if err := json.Unmarshal(existing, &old); err != nil {
			return err
		}
		immutable := func(v PRNudgeRound) any {
			return struct {
				StageRunID, Stage                                     string
				Phase                                                 PRWorkspacePhase
				Round, Minimum, Cap                                   int
				Strategy, DomainStrategy, Coverage, Challenge, Digest string
				Variant, Prompt, Agent, Model                         string
			}{v.StageRunID, v.Stage, v.Phase, v.Round, v.MinimumRounds, v.HardCap, v.StrategyFamily, v.Strategy, v.CoverageTarget, v.Challenge, v.ChallengeDigest, v.VariantDigest, v.PromptDigest, v.AgentID, v.Model}
		}
		return equal("nudge inputs", immutable(old), immutable(*value))
	case *PRDeferredGroupItem:
		var old PRDeferredGroupItem
		if err := json.Unmarshal(existing, &old); err != nil {
			return err
		}
		if old.FindingID != value.FindingID {
			return conflict("deferred finding membership identity")
		}
	case *PRRepairAttempt:
		var old PRRepairAttempt
		if err := json.Unmarshal(existing, &old); err != nil {
			return err
		}
		immutable := func(v PRRepairAttempt) any {
			return struct {
				StageRunID, Instruction, RepairWorkspaceID    string
				Attempt                                       int
				Goal, Base, Agent, Model, Prompt, ScopePrompt string
				PublicationFence                              *PRImplementationPublicationFence
			}{v.StageRunID, v.Instruction, v.RepairWorkspaceID, v.Attempt, v.GoalDigest, v.BaseCommit, v.AgentID, v.Model, v.PromptDigest, v.ScopePromptDigest, v.PublicationFence}
		}
		return equal("repair inputs", immutable(old), immutable(*value))
	case *PRValidationRun:
		var old PRValidationRun
		if err := json.Unmarshal(existing, &old); err != nil {
			return err
		}
		return equal("validation inputs", []string{old.StageRunID, old.RepairAttemptID, old.CandidateSHA, old.Kind, old.Command}, []string{value.StageRunID, value.RepairAttemptID, value.CandidateSHA, value.Kind, value.Command})
	case *PRGateRun:
		var old PRGateRun
		if err := json.Unmarshal(existing, &old); err != nil {
			return err
		}
		immutable := func(v PRGateRun) any {
			return struct {
				Decision, Target, Purpose, Profile, ProfileRevision string
				Policy                                              json.RawMessage
				PolicyHash, SubjectRevision                         string
				Subject                                             json.RawMessage
				SubjectHash                                         string
				Evidence                                            json.RawMessage
			}{v.DecisionPoint, v.TargetID, v.Purpose, v.ProfileID, v.ProfileRevision, v.PinnedPolicy, v.PinnedPolicyHash, v.SubjectRevision, v.PinnedSubject, v.PinnedSubjectHash, v.Evidence}
		}
		return equal("gate pinned inputs", immutable(old), immutable(*value))
	case *PRPublication:
		var old PRPublication
		if err := json.Unmarshal(existing, &old); err != nil {
			return err
		}
		immutable := func(v PRPublication) any {
			return struct {
				Kind                              PRPublicationKind
				Target, Gate, Group, Head, Marker string
				FindingIDs                        []string
				Request                           json.RawMessage
				RequestDigest, PayloadDigest      string
			}{v.Kind, v.TargetID, v.GateRunID, v.DeferredGroupID, v.ExpectedHeadSHA, v.Marker, v.FindingIDs, v.Request, v.RequestDigest, v.PayloadDigest}
		}
		if err := equal("publication intent", immutable(old), immutable(*value)); err != nil {
			return err
		}
		if old.Status == PRPublicationClaimed || value.Status == PRPublicationClaimed {
			return fmt.Errorf("%w: claimed publication requires worker lease API", ErrPRWorkspaceConflict)
		}
		if old.LeaseOwner != "" || old.LeaseToken != "" || old.LeaseUntil != nil ||
			value.LeaseOwner != "" || value.LeaseToken != "" || value.LeaseUntil != nil {
			return fmt.Errorf("%w: publication lease requires worker API", ErrPRWorkspaceConflict)
		}
		if value.Attempts < old.Attempts {
			return fmt.Errorf("%w: publication attempts cannot decrease", ErrPRWorkspaceConflict)
		}
		return nil
	case *PRWorkspaceOperationIntent:
		var old PRWorkspaceOperationIntent
		if err := json.Unmarshal(existing, &old); err != nil {
			return err
		}
		immutable := func(v PRWorkspaceOperationIntent) any {
			return struct {
				Kind, Stage           string
				Version               int64
				Charter, Head, Digest string
				Input                 json.RawMessage
			}{v.Kind, v.StageRunID, v.InputWorkspaceVersion, v.InputCharterID, v.InputHeadSHA, v.InputDigest, v.Input}
		}
		if err := equal("operation intent", immutable(old), immutable(*value)); err != nil {
			return err
		}
		if old.State == PRExecutionRunning || value.State == PRExecutionRunning {
			return fmt.Errorf("%w: running operation requires worker lease API", ErrPRWorkspaceConflict)
		}
		return equal("operation worker lease", struct {
			Owner, Token string
			Until        *time.Time
			Attempts     int
		}{old.LeaseOwner, old.LeaseToken, old.LeaseUntil, old.Attempts}, struct {
			Owner, Token string
			Until        *time.Time
			Attempts     int
		}{value.LeaseOwner, value.LeaseToken, value.LeaseUntil, value.Attempts})
	case *PRIngressWatermark:
		var old PRIngressWatermark
		if err := json.Unmarshal(existing, &old); err != nil {
			return err
		}
		return equal("ingress watermark identity", []string{old.Source, old.Connector}, []string{value.Source, value.Connector})
	}
	return nil
}

// equalPRWorkspaceRawJSON compares the meaning of two bounded JSON values,
// not their representation. This matters for immutable interface-valued
// evidence: concrete structs retain field order on the initial marshal, while
// the same value is restored as maps whose keys are marshaled canonically.
// Strict duplicate detection runs before decoding, and UseNumber retains exact
// number tokens instead of rounding them through float64.
func equalPRWorkspaceRawJSON(left, right json.RawMessage) (bool, error) {
	canonical := func(value json.RawMessage) ([]byte, error) {
		if len(value) == 0 {
			return nil, nil
		}
		if err := validatePRWorkspaceRaw("immutable JSON", value); err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, err
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			if err == nil {
				return nil, errors.New("immutable JSON contains more than one value")
			}
			return nil, err
		}
		return json.Marshal(decoded)
	}
	canonicalLeft, err := canonical(left)
	if err != nil {
		return false, err
	}
	canonicalRight, err := canonical(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(canonicalLeft, canonicalRight), nil
}

func validatePRDeferredGroupItem(ctx context.Context, conn *sql.Conn, workspaceID string, item *PRDeferredGroupItem) error {
	var findingPayload []byte
	if err := conn.QueryRowContext(ctx, `SELECT payload_json FROM pr_findings WHERE id = ? AND workspace_id = ?`, item.FindingID, workspaceID).Scan(&findingPayload); err != nil {
		return err
	}
	var finding PRFinding
	if err := json.Unmarshal(findingPayload, &finding); err != nil {
		return err
	}
	if finding.Disposition != PRFindingDeferred {
		return fmt.Errorf("%w: only deferred findings may join deferred groups", ErrPRWorkspaceConflict)
	}
	var existingID string
	err := conn.QueryRowContext(ctx, `SELECT id FROM pr_deferred_group_items
		WHERE workspace_id = ? AND finding_id = ? AND id <> ?`, workspaceID, item.FindingID, item.ID).Scan(&existingID)
	if err == nil {
		return fmt.Errorf("%w: finding already belongs to deferred group", ErrPRWorkspaceConflict)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	err = conn.QueryRowContext(ctx, `SELECT id FROM pr_deferred_group_items
		WHERE workspace_id = ? AND group_id = ? AND ordinal_in_group = ? AND id <> ?`,
		workspaceID, item.GroupID, item.OrdinalInGroup, item.ID).Scan(&existingID)
	if err == nil {
		return fmt.Errorf("%w: deferred group ordinal already occupied", ErrPRWorkspaceConflict)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func decodePRWorkspaceMutation(
	kind PRWorkspaceMutationKind,
	payload json.RawMessage,
) (any, []byte, error) {
	if len(payload) < 2 || len(payload) > MaxPRWorkspaceRecordBytes {
		return nil, nil, fmt.Errorf("%w: mutation payload has %d bytes, maximum %d", ErrInvalidPRWorkspace, len(payload), MaxPRWorkspaceRecordBytes)
	}
	if kind == PRMutationWorkspaceState {
		var value PRWorkspaceStateChange
		if err := decodeStrictPRWorkspaceJSON(payload, &value); err != nil {
			return nil, nil, err
		}
		if err := validatePRWorkspacePhase(value.Phase); err != nil {
			return nil, nil, err
		}
		if err := validatePRExecutionState(value.ExecutionState); err != nil {
			return nil, nil, err
		}
		canonical, _ := json.Marshal(value)
		return &value, canonical, nil
	}

	var record prWorkspaceMutationRecord
	switch kind {
	case PRMutationProviderSnapshot:
		value := new(PRProviderSnapshot)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		normalizePRProviderSnapshot(value)
		record = prWorkspaceMutationRecord{"pr_provider_snapshots", prProviderSnapshotIDPrefix, true, "upsert", value, &value.PRWorkspaceRecord, "observed"}
	case PRMutationCharter:
		value := new(PRCharterRevision)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_charter_revisions", prCharterRevisionIDPrefix, false, "upsert", value, &value.PRWorkspaceRecord, string(value.Status)}
	case PRMutationStageRun:
		value := new(PRStageRun)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_stage_runs", prStageRunIDPrefix, false, "upsert", value, &value.PRWorkspaceRecord, string(value.State)}
	case PRMutationFinding:
		value := new(PRFinding)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_findings", prFindingIDPrefix, false, "upsert", value, &value.PRWorkspaceRecord, string(value.Disposition)}
	case PRMutationFindingEvent:
		value := new(PRFindingEvent)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_finding_events", prFindingEventIDPrefix, true, "upsert", value, &value.PRWorkspaceRecord, strings.TrimSpace(value.Kind)}
	case PRMutationConversation:
		value := new(PRConversation)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_conversations", prConversationIDPrefix, false, "upsert", value, &value.PRWorkspaceRecord, string(value.Status)}
	case PRMutationMessage:
		value := new(PRMessage)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_messages", prMessageIDPrefix, true, "upsert", value, &value.PRWorkspaceRecord, strings.TrimSpace(value.Role)}
	case PRMutationCorrection:
		value := new(PRCorrection)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_corrections", prCorrectionIDPrefix, false, "upsert", value, &value.PRWorkspaceRecord, string(value.Status)}
	case PRMutationRepositoryLesson:
		value := new(PRRepositoryLesson)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_repository_lessons", prRepositoryLessonIDPrefix, false, "upsert", value, &value.PRWorkspaceRecord, string(value.Status)}
	case PRMutationNudgeRound:
		value := new(PRNudgeRound)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_nudge_rounds", prNudgeRoundIDPrefix, false, "upsert", value, &value.PRWorkspaceRecord, string(value.State)}
	case PRMutationNudgeReward:
		value := new(PRNudgeReward)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_nudge_rewards", prNudgeRewardIDPrefix, true, "upsert", value, &value.PRWorkspaceRecord, "resolved"}
	case PRMutationDeferredGroup:
		value := new(PRDeferredGroup)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		if len(value.Items) != 0 {
			return nil, nil, fmt.Errorf("%w: deferred group items require separate mutations", ErrInvalidPRWorkspace)
		}
		record = prWorkspaceMutationRecord{"pr_deferred_groups", prDeferredGroupIDPrefix, false, "upsert", value, &value.PRWorkspaceRecord, string(value.Status)}
	case PRMutationDeferredGroupItem:
		value := new(PRDeferredGroupItem)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		status := "active"
		if value.Removed {
			status = "removed"
		}
		record = prWorkspaceMutationRecord{"pr_deferred_group_items", prDeferredGroupItemIDPrefix, false, "upsert", value, &value.PRWorkspaceRecord, status}
	case PRMutationRepairAttempt:
		value := new(PRRepairAttempt)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_repair_attempts", prRepairAttemptIDPrefix, false, "upsert", value, &value.PRWorkspaceRecord, string(value.State)}
	case PRMutationValidationRun:
		value := new(PRValidationRun)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_validation_runs", prValidationRunIDPrefix, false, "upsert", value, &value.PRWorkspaceRecord, string(value.State)}
	case PRMutationGateRun:
		value := new(PRGateRun)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_gate_runs", prGateRunIDPrefix, false, "upsert", value, &value.PRWorkspaceRecord, string(value.State)}
	case PRMutationGateDecision:
		value := new(PRGateDecision)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_gate_decisions", prGateDecisionIDPrefix, true, "upsert", value, &value.PRWorkspaceRecord, string(value.Outcome)}
	case PRMutationPublication:
		value := new(PRPublication)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_publications", prPublicationIDPrefix, false, "upsert", value, &value.PRWorkspaceRecord, string(value.Status)}
	case PRMutationOperationIntent:
		value := new(PRWorkspaceOperationIntent)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_operation_intents", prOperationIntentIDPrefix, false, "upsert", value, &value.PRWorkspaceRecord, string(value.State)}
	case PRMutationIngressWatermark:
		value := new(PRIngressWatermark)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_ingress_watermarks", prIngressWatermarkIDPrefix, false, "upsert", value, &value.PRWorkspaceRecord, "observed"}
	case PRMutationActivity:
		value := new(PRActivity)
		if err := decodeStrictPRWorkspaceJSON(payload, value); err != nil {
			return nil, nil, err
		}
		record = prWorkspaceMutationRecord{"pr_activity", prActivityIDPrefix, true, "upsert", value, &value.PRWorkspaceRecord, value.Kind}
	default:
		return nil, nil, fmt.Errorf("%w: unsupported mutation kind %q", ErrInvalidPRWorkspace, kind)
	}
	if err := validatePRWorkspaceRecord(record.value); err != nil {
		return nil, nil, err
	}
	canonical, err := json.Marshal(record.value)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: encode mutation payload: %v", ErrInvalidPRWorkspace, err)
	}
	return &record, canonical, nil
}

func decodeStrictPRWorkspaceJSON(payload []byte, target any) error {
	if err := validateUniquePRWorkspaceJSON(payload); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPRWorkspace, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: decode mutation payload: %v", ErrInvalidPRWorkspace, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: mutation payload contains more than one value", ErrInvalidPRWorkspace)
		}
		return fmt.Errorf("%w: decode mutation payload trailer: %v", ErrInvalidPRWorkspace, err)
	}
	return nil
}

func validateUniquePRWorkspaceJSON(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	tokens := 0
	if err := consumeUniquePRWorkspaceJSON(decoder, 0, &tokens); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains trailing value")
		}
		return err
	}
	return nil
}

func consumeUniquePRWorkspaceJSON(decoder *json.Decoder, depth int, tokens *int) error {
	if depth > 32 {
		return errors.New("JSON nesting exceeds 32 levels")
	}
	*tokens++
	if *tokens > 1<<20 {
		return errors.New("JSON token count exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			folded := strings.ToLower(key)
			if _, exists := seen[folded]; exists {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[folded] = struct{}{}
			if err := consumeUniquePRWorkspaceJSON(decoder, depth+1, tokens); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := consumeUniquePRWorkspaceJSON(decoder, depth+1, tokens); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func validatePRWorkspaceRecord(value any) error {
	var meta *PRWorkspaceRecord
	switch record := value.(type) {
	case *PRProviderSnapshot:
		meta = &record.PRWorkspaceRecord
		if err := validatePRProviderSnapshot(*record); err != nil {
			return err
		}
	case *PRCharterRevision:
		meta = &record.PRWorkspaceRecord
		if record.Revision <= 0 {
			return fmt.Errorf("%w: charter revision must be positive", ErrInvalidPRWorkspace)
		}
		if record.Status != PRRecordDraft && record.Status != PRRecordConfirmed && record.Status != PRRecordSuperseded {
			return invalidPRWorkspaceField("charter status", string(record.Status), 32, true)
		}
		if !validPRType(record.Type) {
			return invalidPRWorkspaceField("PR type", string(record.Type), 32, true)
		}
		if err := validatePRWorkspaceString("charter goal", record.Goal, maxPRWorkspaceTextBytes, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceStringList("acceptance criteria", record.AcceptanceCriteria, true); err != nil {
			return err
		}
		for field, values := range map[string][]string{"included areas": record.IncludedAreas, "exclusions": record.Exclusions, "non-goals": record.NonGoals} {
			if err := validatePRWorkspaceStringList(field, values, false); err != nil {
				return err
			}
		}
		for field, item := range map[string]string{"base SHA": record.BaseSHA, "head SHA": record.HeadSHA, "created by": record.CreatedBy} {
			if err := validatePRWorkspaceString(field, item, maxPRWorkspaceIdentityBytes, true); err != nil {
				return err
			}
		}
		if record.Status == PRRecordConfirmed && record.ConfirmedAt == nil {
			return fmt.Errorf("%w: confirmed charter requires confirmed_at", ErrInvalidPRWorkspace)
		}
	case *PRStageRun:
		meta = &record.PRWorkspaceRecord
		if record.Attempt <= 0 {
			return fmt.Errorf("%w: stage attempt must be positive", ErrInvalidPRWorkspace)
		}
		if err := validatePRWorkspacePhase(record.Phase); err != nil {
			return err
		}
		if err := validatePRExecutionState(record.State); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("stage kind", record.Kind, 128, true); err != nil {
			return err
		}
		if record.WorkspaceVersion <= 0 {
			return fmt.Errorf("%w: stage workspace_version must be positive", ErrInvalidPRWorkspace)
		}
		if err := validatePRWorkspaceRaw("stage evidence", record.Evidence); err != nil {
			return err
		}
	case *PRFinding:
		meta = &record.PRWorkspaceRecord
		if !validPRFindingDisposition(record.Disposition) {
			return invalidPRWorkspaceField("finding disposition", string(record.Disposition), 32, true)
		}
		if !validPRScopeDistance(record.ScopeDistance) {
			return invalidPRWorkspaceField("scope distance", string(record.ScopeDistance), 64, true)
		}
		if !validPRChangeSize(record.ChangeSize) {
			return invalidPRWorkspaceField("change size", string(record.ChangeSize), 8, true)
		}
		if err := validatePRScopeProjection(record.ScopePresence, record.ScopeChangeEvidence); err != nil {
			return err
		}
		for field, item := range map[string]string{"finding origin": record.Origin, "fingerprint": record.Fingerprint, "severity": record.Severity, "title": record.Title, "message": record.Message} {
			if err := validatePRWorkspaceString(field, item, maxPRWorkspaceTextBytes, true); err != nil {
				return err
			}
		}
		if record.ClassificationConf < 0 || record.ClassificationConf > 1 {
			return fmt.Errorf("%w: classification confidence must be between 0 and 1", ErrInvalidPRWorkspace)
		}
		if record.Version <= 0 {
			return fmt.Errorf("%w: finding version must be positive", ErrInvalidPRWorkspace)
		}
		if record.NudgeReward != nil && (*record.NudgeReward < 0 || *record.NudgeReward > 1) {
			return fmt.Errorf("%w: finding nudge reward must be between 0 and 1", ErrInvalidPRWorkspace)
		}
		if record.NudgeReward != nil && strings.TrimSpace(record.RewardSource) == "" {
			return fmt.Errorf("%w: rewarded finding requires reward source", ErrInvalidPRWorkspace)
		}
		if err := validatePRChangeMetrics(record.EstimatedMetrics); err != nil {
			return err
		}
		if record.ActualMetrics != nil {
			if err := validatePRChangeMetrics(*record.ActualMetrics); err != nil {
				return err
			}
		}
	case *PRFindingEvent:
		meta = &record.PRWorkspaceRecord
		if err := validatePRWorkspaceString("finding event finding_id", record.FindingID, 128, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("finding event kind", record.Kind, 128, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("finding event actor", record.Actor, 256, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceRaw("finding event before", record.Before); err != nil {
			return err
		}
		if err := validatePRWorkspaceRaw("finding event after", record.After); err != nil {
			return err
		}
	case *PRConversation:
		meta = &record.PRWorkspaceRecord
		if err := validatePRWorkspaceString("conversation channel", record.Channel, 128, true); err != nil {
			return err
		}
		if err := validatePRWorkspacePhase(record.Phase); err != nil {
			return err
		}
		if record.Status != PRRecordActive && record.Status != PRRecordResolved && record.Status != PRRecordSuperseded {
			return invalidPRWorkspaceField("conversation status", string(record.Status), 32, true)
		}
	case *PRMessage:
		meta = &record.PRWorkspaceRecord
		if record.Role != "user" && record.Role != "assistant" && record.Role != "system" {
			return invalidPRWorkspaceField("message role", record.Role, 32, true)
		}
		if err := validatePRWorkspacePhase(record.Phase); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("message kind", record.Kind, 128, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("message content", record.Content, MaxPRWorkspaceMessageBytes, true); err != nil {
			return err
		}
		for field, item := range map[string]string{"message charter ID": record.CharterID, "message head SHA": record.HeadSHA} {
			if err := validatePRWorkspaceString(field, item, maxPRWorkspaceIdentityBytes, false); err != nil {
				return err
			}
		}
	case *PRCorrection:
		meta = &record.PRWorkspaceRecord
		if record.Status != PRRecordActive && record.Status != PRRecordSuperseded && record.Status != PRRecordRevoked {
			return invalidPRWorkspaceField("correction status", string(record.Status), 32, true)
		}
		if !record.AppliesToReview && !record.AppliesToImplement {
			return fmt.Errorf("%w: correction must apply to review, implementation, or both", ErrInvalidPRWorkspace)
		}
		for field, item := range map[string]string{"correction kind": record.Kind, "target kind": record.TargetKind, "original claim": record.OriginalClaim, "correction": record.Correction} {
			if err := validatePRWorkspaceString(field, item, maxPRWorkspaceTextBytes, true); err != nil {
				return err
			}
		}
	case *PRRepositoryLesson:
		meta = &record.PRWorkspaceRecord
		if record.Status != PRRecordActive && record.Status != PRRecordRevoked {
			return invalidPRWorkspaceField("lesson status", string(record.Status), 32, true)
		}
		for field, item := range map[string]string{"repository ID": record.RepositoryID, "lesson kind": record.Kind, "lesson content": record.Content, "source correction ID": record.SourceCorrectionID, "confirmed by": record.ConfirmedBy} {
			if err := validatePRWorkspaceString(field, item, maxPRWorkspaceTextBytes, true); err != nil {
				return err
			}
		}
		for _, item := range record.ApplicableTypes {
			if !validPRType(item) {
				return invalidPRWorkspaceField("applicable PR type", string(item), 32, true)
			}
		}
		for _, item := range record.ApplicablePhases {
			if err := validatePRWorkspacePhase(item); err != nil {
				return err
			}
		}
	case *PRNudgeRound:
		meta = &record.PRWorkspaceRecord
		if err := validatePRExecutionState(record.State); err != nil {
			return err
		}
		if record.Phase != PRWorkspaceReview && record.Phase != PRWorkspaceCompletionAudit {
			return fmt.Errorf("%w: nudge phase must be review or completion_audit", ErrInvalidPRWorkspace)
		}
		if record.Round <= 0 || record.MinimumRounds < 0 || record.HardCap < record.MinimumRounds || record.HardCap > 10 || record.Round > record.HardCap {
			return fmt.Errorf("%w: invalid nudge round budget", ErrInvalidPRWorkspace)
		}
		for field, item := range map[string]string{"stage run ID": record.StageRunID, "strategy family": record.StrategyFamily, "coverage target": record.CoverageTarget, "challenge digest": record.ChallengeDigest, "prompt digest": record.PromptDigest} {
			if err := validatePRWorkspaceString(field, item, maxPRWorkspaceTextBytes, true); err != nil {
				return err
			}
		}
		if record.CandidateCount < 0 || record.NovelCount < 0 || record.DuplicateCount < 0 || record.NovelCount+record.DuplicateCount > record.CandidateCount {
			return fmt.Errorf("%w: invalid nudge finding counts", ErrInvalidPRWorkspace)
		}
		if record.ResolvedFindings < 0 || record.ResolvedFindings > len(record.FindingIDs) {
			return fmt.Errorf("%w: invalid resolved nudge finding count", ErrInvalidPRWorkspace)
		}
		if record.Reward != nil && (*record.Reward < 0 || *record.Reward > 1) {
			return fmt.Errorf("%w: nudge reward must be between 0 and 1", ErrInvalidPRWorkspace)
		}
	case *PRNudgeReward:
		meta = &record.PRWorkspaceRecord
		if err := validatePRWorkspaceString("nudge round ID", record.NudgeRoundID, 128, true); err != nil {
			return err
		}
		if record.Reward < 0 || record.Reward > 1 {
			return fmt.Errorf("%w: nudge reward must be between 0 and 1", ErrInvalidPRWorkspace)
		}
		if err := validatePRWorkspaceString("nudge outcome", record.Outcome, 128, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("nudge provenance", record.Provenance, maxPRWorkspaceTextBytes, true); err != nil {
			return err
		}
	case *PRDeferredGroup:
		meta = &record.PRWorkspaceRecord
		if record.Status != PRRecordDraft && record.Status != PRRecordActive && record.Status != PRRecordResolved && record.Status != PRRecordDismissed {
			return invalidPRWorkspaceField("deferred group status", string(record.Status), 32, true)
		}
		if !validPRScopeDistance(record.ScopeDistance) || !validPRChangeSize(record.ChangeSize) {
			return fmt.Errorf("%w: invalid deferred group scope grade", ErrInvalidPRWorkspace)
		}
		if record.DraftRevision <= 0 {
			return fmt.Errorf("%w: deferred group draft_revision must be positive", ErrInvalidPRWorkspace)
		}
		if record.Version <= 0 {
			return fmt.Errorf("%w: deferred group version must be positive", ErrInvalidPRWorkspace)
		}
		if record.ScopeFiles < 0 || record.ScopeSemanticLines < 0 || record.ScopeModules < 0 || record.ScopeConfidence < 0 || record.ScopeConfidence > 1 {
			return fmt.Errorf("%w: invalid deferred group scope assessment", ErrInvalidPRWorkspace)
		}
		if err := validatePRScopeProjection(record.ScopePresence, record.ScopeChangeEvidence); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("deferred group title", record.Title, maxPRWorkspaceTextBytes, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("deferred group body", record.Body, maxPRWorkspaceBodyBytes, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceStringList("deferred labels", record.Labels, false); err != nil {
			return err
		}
		if record.PublicationSuppressed {
			if err := validatePRWorkspaceString("deferred suppression reason", record.SuppressionReason, 256, true); err != nil {
				return err
			}
		} else if record.SuppressionReason != "" {
			return fmt.Errorf("%w: unsuppressed deferred group has a suppression reason", ErrInvalidPRWorkspace)
		}
	case *PRDeferredGroupItem:
		meta = &record.PRWorkspaceRecord
		if err := validatePRWorkspaceString("deferred finding ID", record.FindingID, 128, true); err != nil {
			return err
		}
		if record.Removed {
			if record.GroupID != "" || record.OrdinalInGroup != -1 {
				return fmt.Errorf("%w: removed deferred item must clear its group position", ErrInvalidPRWorkspace)
			}
			break
		}
		if err := validatePRWorkspaceString("deferred group ID", record.GroupID, 128, true); err != nil {
			return err
		}
		if record.OrdinalInGroup < 0 {
			return fmt.Errorf("%w: deferred item ordinal must not be negative", ErrInvalidPRWorkspace)
		}
	case *PRRepairAttempt:
		meta = &record.PRWorkspaceRecord
		if err := validatePRExecutionState(record.State); err != nil {
			return err
		}
		if record.Attempt <= 0 || record.Attempt > 10 {
			return fmt.Errorf("%w: repair attempt must be between 1 and 10", ErrInvalidPRWorkspace)
		}
		for field, item := range map[string]string{"repair stage run ID": record.StageRunID, "goal digest": record.GoalDigest, "base commit": record.BaseCommit} {
			if err := validatePRWorkspaceString(field, item, maxPRWorkspaceIdentityBytes, true); err != nil {
				return err
			}
		}
		if err := validatePRChangeMetrics(record.Metrics); err != nil {
			return err
		}
		if record.ScopeDistance != "" && !validPRScopeDistance(record.ScopeDistance) {
			return fmt.Errorf("%w: invalid repair scope distance", ErrInvalidPRWorkspace)
		}
		if record.ScopeChangeSize != "" && !validPRChangeSize(record.ScopeChangeSize) {
			return fmt.Errorf("%w: invalid repair scope size", ErrInvalidPRWorkspace)
		}
		if record.ScopeConfidence < 0 || record.ScopeConfidence > 1 {
			return fmt.Errorf("%w: invalid repair scope confidence", ErrInvalidPRWorkspace)
		}
		if err := validatePRScopeProjection(record.ScopePresence, record.ScopeChangeEvidence); err != nil {
			return err
		}
		if err := validatePRWorkspaceStringList("changed files", record.ChangedFiles, false); err != nil {
			return err
		}
		if len(record.FindingIDs) > maxPRWorkspaceListEntries {
			return fmt.Errorf("%w: repair finding IDs exceed bound", ErrInvalidPRWorkspace)
		}
		if record.PublicationFence != nil {
			fence := record.PublicationFence
			if fence.LineVersion <= 0 || fence.MutationEpoch <= 0 {
				return fmt.Errorf("%w: repair publication fence versions must be positive", ErrInvalidPRWorkspace)
			}
			for field, item := range map[string]string{
				"fence git workspace ID": fence.GitWorkspaceID,
				"fence line ID":          fence.LineID,
				"fence park intent ID":   fence.ParkIntentID,
				"fence base commit":      fence.BaseCommit,
				"fence tip":              fence.Tip,
				"fence tree":             fence.Tree,
			} {
				if err := validatePRWorkspaceString(field, item, maxPRWorkspaceIdentityBytes, true); err != nil {
					return err
				}
			}
		}
	case *PRValidationRun:
		meta = &record.PRWorkspaceRecord
		if err := validatePRExecutionState(record.State); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("validation stage run ID", record.StageRunID, 128, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("validation kind", record.Kind, 128, true); err != nil {
			return err
		}
		if len(record.Checks) > maxPRWorkspaceListEntries {
			return fmt.Errorf("%w: validation checks exceed bound", ErrInvalidPRWorkspace)
		}
		for _, check := range record.Checks {
			if err := validatePRWorkspaceString("validation check ID", check.ID, 256, true); err != nil {
				return err
			}
			if err := validatePRWorkspaceString("validation check name", check.Name, maxPRWorkspaceTextBytes, true); err != nil {
				return err
			}
			if err := validatePRWorkspaceString("validation check status", check.Status, 128, true); err != nil {
				return err
			}
			if check.DurationMS < 0 {
				return fmt.Errorf("%w: validation check duration must not be negative", ErrInvalidPRWorkspace)
			}
		}
	case *PRGateRun:
		meta = &record.PRWorkspaceRecord
		if err := validatePRExecutionState(record.State); err != nil {
			return err
		}
		for field, item := range map[string]string{"decision point": record.DecisionPoint, "gate purpose": record.Purpose, "profile ID": record.ProfileID, "profile revision": record.ProfileRevision, "pinned policy hash": record.PinnedPolicyHash, "subject revision": record.SubjectRevision, "pinned subject hash": record.PinnedSubjectHash} {
			if err := validatePRWorkspaceString(field, item, maxPRWorkspaceIdentityBytes, true); err != nil {
				return err
			}
		}
		if err := validatePRWorkspaceRaw("pinned policy", record.PinnedPolicy); err != nil {
			return err
		}
		if err := validatePRWorkspaceRaw("pinned subject", record.PinnedSubject); err != nil {
			return err
		}
		if err := validatePRWorkspaceRaw("gate evidence", record.Evidence); err != nil {
			return err
		}
		if record.Outcome != "" && !validPRGateOutcome(record.Outcome) {
			return fmt.Errorf("%w: invalid gate outcome", ErrInvalidPRWorkspace)
		}
		if len(record.Turns) > maxPRWorkspaceListEntries {
			return fmt.Errorf("%w: gate turns exceed bound", ErrInvalidPRWorkspace)
		}
	case *PRGateDecision:
		meta = &record.PRWorkspaceRecord
		if !validPRGateOutcome(record.Outcome) {
			return invalidPRWorkspaceField("gate outcome", string(record.Outcome), 32, true)
		}
		for field, item := range map[string]string{"gate run ID": record.GateRunID, "gate stage ID": record.StageID, "gate kind": record.Kind, "gate actor": record.Actor} {
			if err := validatePRWorkspaceString(field, item, maxPRWorkspaceIdentityBytes, true); err != nil {
				return err
			}
		}
		if err := validatePRWorkspaceRaw("gate answers", record.Answers); err != nil {
			return err
		}
	case *PRPublication:
		meta = &record.PRWorkspaceRecord
		if !validPRPublicationKind(record.Kind) {
			return invalidPRWorkspaceField("publication kind", string(record.Kind), 32, true)
		}
		if !validPRPublicationStatus(record.Status) {
			return invalidPRWorkspaceField("publication status", string(record.Status), 32, true)
		}
		if record.ExecutionState != "" {
			if err := validatePRExecutionState(record.ExecutionState); err != nil {
				return err
			}
		}
		if len(record.FindingIDs) > maxPRWorkspaceListEntries {
			return fmt.Errorf("%w: publication finding IDs exceed bound", ErrInvalidPRWorkspace)
		}
		if err := validatePRWorkspaceString("publication request digest", record.RequestDigest, maxPRWorkspaceIdentityBytes, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceRaw("publication request", record.Request); err != nil {
			return err
		}
		requestHash := sha256.Sum256(record.Request)
		expectedDigest := "sha256:" + hex.EncodeToString(requestHash[:])
		if record.RequestDigest != expectedDigest || record.PayloadDigest != expectedDigest {
			return fmt.Errorf("%w: publication request digest mismatch", ErrInvalidPRWorkspace)
		}
		if record.Attempts < 0 {
			return fmt.Errorf("%w: publication attempts must not be negative", ErrInvalidPRWorkspace)
		}
		if record.AvailableAt.IsZero() {
			return fmt.Errorf("%w: publication available_at is required", ErrInvalidPRWorkspace)
		}
		if err := validateDBTimestamp("publication available_at", record.AvailableAt); err != nil {
			return fmt.Errorf("%w: publication available_at is invalid", ErrInvalidPRWorkspace)
		}
		if err := validatePRWorkspaceLease(record.LeaseOwner, record.LeaseToken, record.LeaseUntil, record.Status == PRPublicationClaimed); err != nil {
			return err
		}
	case *PRWorkspaceOperationIntent:
		meta = &record.PRWorkspaceRecord
		if err := validatePRExecutionState(record.State); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("operation kind", record.Kind, 128, true); err != nil {
			return err
		}
		if record.InputWorkspaceVersion <= 0 {
			return fmt.Errorf("%w: operation input workspace version must be positive", ErrInvalidPRWorkspace)
		}
		if err := validatePRWorkspaceString("operation input digest", record.InputDigest, maxPRWorkspaceIdentityBytes, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceRaw("operation input", record.Input); err != nil {
			return err
		}
		if err := validatePRWorkspaceRaw("operation result", record.Result); err != nil {
			return err
		}
		if record.Attempts < 0 {
			return fmt.Errorf("%w: operation attempts must not be negative", ErrInvalidPRWorkspace)
		}
		if record.AvailableAt.IsZero() {
			return fmt.Errorf("%w: operation available_at is required", ErrInvalidPRWorkspace)
		}
		if err := validateDBTimestamp("operation available_at", record.AvailableAt); err != nil {
			return fmt.Errorf("%w: operation available_at is invalid", ErrInvalidPRWorkspace)
		}
		if err := validatePRWorkspaceLease(record.LeaseOwner, record.LeaseToken, record.LeaseUntil, record.State == PRExecutionRunning); err != nil {
			return err
		}
	case *PRIngressWatermark:
		meta = &record.PRWorkspaceRecord
		if err := validatePRWorkspaceString("watermark source", record.Source, 256, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("watermark connector", record.Connector, 256, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("watermark event ID", record.InboxEventID, 256, true); err != nil {
			return err
		}
		if record.InboxReceivedAt.IsZero() {
			return fmt.Errorf("%w: watermark received_at is required", ErrInvalidPRWorkspace)
		}
	case *PRActivity:
		meta = &record.PRWorkspaceRecord
		if err := validatePRWorkspaceString("activity kind", record.Kind, 128, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("activity actor", record.Actor, 256, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("activity summary", record.Summary, maxPRWorkspaceTextBytes, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("activity entity ID", record.EntityID, 256, false); err != nil {
			return err
		}
		if len(record.Metadata) > 128 {
			return fmt.Errorf("%w: activity metadata exceeds 128 entries", ErrInvalidPRWorkspace)
		}
		encoded, err := json.Marshal(record.Metadata)
		if err != nil || len(encoded) > MaxPRWorkspaceRecordBytes {
			return fmt.Errorf("%w: invalid activity metadata", ErrInvalidPRWorkspace)
		}
	default:
		return fmt.Errorf("%w: unsupported mutation record %T", ErrInvalidPRWorkspace, value)
	}
	if meta == nil || meta.Ordinal < 0 {
		return fmt.Errorf("%w: invalid record metadata", ErrInvalidPRWorkspace)
	}
	if meta.ID != "" && len(meta.ID) > 128 {
		return fmt.Errorf("%w: record ID exceeds 128 bytes", ErrInvalidPRWorkspace)
	}
	if meta.WorkspaceID != "" && !validPrefixedID(meta.WorkspaceID, prWorkspaceIDPrefix) {
		return fmt.Errorf("%w: invalid record workspace ID", ErrInvalidPRWorkspace)
	}
	return nil
}

func normalizePRProviderSnapshot(value *PRProviderSnapshot) {
	if value == nil {
		return
	}
	value.Provider = strings.ToLower(strings.TrimSpace(value.Provider))
	value.ProviderOrigin = strings.TrimSpace(value.ProviderOrigin)
	value.RepositoryID = strings.TrimSpace(value.RepositoryID)
	value.Repository = strings.TrimSpace(value.Repository)
	value.PullRequestID = strings.TrimSpace(value.PullRequestID)
	value.Title = strings.TrimSpace(value.Title)
	value.AuthorID = strings.TrimSpace(value.AuthorID)
	value.AuthorLogin = strings.TrimSpace(value.AuthorLogin)
	value.AuthenticatedUserID = strings.TrimSpace(value.AuthenticatedUserID)
	value.BaseRef = strings.TrimSpace(value.BaseRef)
	value.BaseSHA = strings.TrimSpace(value.BaseSHA)
	value.HeadRepositoryID = strings.TrimSpace(value.HeadRepositoryID)
	value.HeadRepository = strings.TrimSpace(value.HeadRepository)
	value.HeadRef = strings.TrimSpace(value.HeadRef)
	value.HeadSHA = strings.TrimSpace(value.HeadSHA)
	value.State = strings.TrimSpace(value.State)
	value.ProviderRevision = strings.TrimSpace(value.ProviderRevision)
}

func validatePRWorkspaceLease(owner, token string, until *time.Time, claimed bool) error {
	if claimed {
		if err := validatePRWorkspaceString("lease owner", owner, 256, true); err != nil {
			return err
		}
		if !validPrefixedID(token, prLeaseTokenIDPrefix) {
			return fmt.Errorf("%w: invalid lease token", ErrInvalidPRWorkspace)
		}
		if until == nil {
			return fmt.Errorf("%w: claimed record requires lease_until", ErrInvalidPRWorkspace)
		}
		if err := validateDBTimestamp("lease_until", *until); err != nil {
			return fmt.Errorf("%w: lease_until is invalid", ErrInvalidPRWorkspace)
		}
		return nil
	}
	if owner != "" || token != "" || until != nil {
		return fmt.Errorf("%w: unclaimed record cannot carry a lease", ErrInvalidPRWorkspace)
	}
	return nil
}

func validatePRProviderSnapshot(value PRProviderSnapshot) error {
	if value.Provider != "github" {
		return fmt.Errorf("%w: provider must be github", ErrInvalidPRWorkspace)
	}
	for field, item := range map[string]string{
		"provider origin": value.ProviderOrigin, "repository ID": value.RepositoryID,
		"repository": value.Repository, "pull request ID": value.PullRequestID,
		"base SHA": value.BaseSHA, "head SHA": value.HeadSHA,
		"head repository ID": value.HeadRepositoryID,
		"head repository":    value.HeadRepository,
	} {
		if err := validatePRWorkspaceString(field, item, maxPRWorkspaceIdentityBytes, true); err != nil {
			return err
		}
	}
	for field, item := range map[string]string{
		"title": value.Title, "body": value.Body, "author ID": value.AuthorID,
		"author login": value.AuthorLogin, "authenticated user ID": value.AuthenticatedUserID,
		"base ref": value.BaseRef,
		"head ref": value.HeadRef, "state": value.State,
		"provider revision": value.ProviderRevision,
	} {
		maximum := maxPRWorkspaceBodyBytes
		if field != "body" && field != "title" {
			maximum = 4096
		}
		if err := validatePRWorkspaceString(field, item, maximum, false); err != nil {
			return err
		}
	}
	if value.PullNumber <= 0 || value.PullNumber > 1<<31-1 {
		return fmt.Errorf("%w: pull number is outside supported range", ErrInvalidPRWorkspace)
	}
	if !value.ObservedAt.IsZero() {
		if err := validateDBTimestamp("provider observed_at", value.ObservedAt); err != nil {
			return fmt.Errorf("%w: provider observed_at is invalid", ErrInvalidPRWorkspace)
		}
	}
	return nil
}

func validatePRWorkspaceRequestID(value string) error {
	if err := validatePRWorkspaceString("request ID", value, maxPRWorkspaceRequestIDBytes, true); err != nil {
		return err
	}
	if strings.ContainsAny(value, "\r\n\t ") {
		return fmt.Errorf("%w: request ID contains whitespace", ErrInvalidPRWorkspace)
	}
	return nil
}

func validatePRWorkspaceString(field, value string, maximum int, required bool) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%w: %s is not valid text", ErrInvalidPRWorkspace, field)
	}
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidPRWorkspace, field)
	}
	if len(value) > maximum {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalidPRWorkspace, field, maximum)
	}
	return nil
}

func invalidPRWorkspaceField(field, value string, maximum int, required bool) error {
	if err := validatePRWorkspaceString(field, value, maximum, required); err != nil {
		return err
	}
	return fmt.Errorf("%w: unsupported %s", ErrInvalidPRWorkspace, field)
}

func validatePRWorkspaceStringList(field string, values []string, required bool) error {
	if required && len(values) == 0 {
		return fmt.Errorf("%w: %s requires at least one item", ErrInvalidPRWorkspace, field)
	}
	if len(values) > maxPRWorkspaceListEntries {
		return fmt.Errorf("%w: %s exceeds %d entries", ErrInvalidPRWorkspace, field, maxPRWorkspaceListEntries)
	}
	for _, value := range values {
		if err := validatePRWorkspaceString(field+" item", value, maxPRWorkspaceTextBytes, true); err != nil {
			return err
		}
	}
	return nil
}

func validatePRWorkspaceRaw(field string, value json.RawMessage) error {
	if len(value) == 0 {
		return nil
	}
	if len(value) > MaxPRWorkspaceRecordBytes || !utf8.Valid(value) || !json.Valid(value) {
		return fmt.Errorf("%w: %s is not bounded valid JSON", ErrInvalidPRWorkspace, field)
	}
	if err := validateUniquePRWorkspaceJSON(value); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidPRWorkspace, field, err)
	}
	return nil
}

func validatePRChangeMetrics(value PRChangeMetrics) error {
	if value.Files < 0 || value.SemanticLines < 0 || value.Modules < 0 || value.RawLines < 0 {
		return fmt.Errorf("%w: change metrics must not be negative", ErrInvalidPRWorkspace)
	}
	return nil
}

func validatePRWorkspacePhase(value PRWorkspacePhase) error {
	switch value {
	case PRWorkspaceIntake, PRWorkspaceCharter, PRWorkspaceReview, PRWorkspaceTriage,
		PRWorkspaceImplementation, PRWorkspaceValidation, PRWorkspaceCompletionAudit,
		PRWorkspacePublication, PRWorkspaceComplete:
		return nil
	default:
		return invalidPRWorkspaceField("workspace phase", string(value), 64, true)
	}
}

func validatePRExecutionState(value PRExecutionState) error {
	switch value {
	case PRExecutionQueued, PRExecutionRunning, PRExecutionWaitingGate,
		PRExecutionWaitingUser, PRExecutionSucceeded, PRExecutionFailed,
		PRExecutionBlocked, PRExecutionCanceled, PRExecutionStale, PRExecutionUnknown:
		return nil
	default:
		return invalidPRWorkspaceField("execution state", string(value), 64, true)
	}
}

func validPRType(value PRType) bool {
	switch value {
	case PRTypeFix, PRTypeRefactor, PRTypeFeature, PRTypeDocumentation, PRTypeTest:
		return true
	}
	return false
}

func validPRFindingDisposition(value PRFindingDisposition) bool {
	switch value {
	case PRFindingOpen, PRFindingInScope, PRFindingFixed, PRFindingDeferred, PRFindingDismissed:
		return true
	}
	return false
}

func validPRScopeDistance(value PRScopeDistance) bool {
	switch value {
	case PRScopeExact, PRScopeNecessaryAdjacent, PRScopeRelatedFollowup, PRScopeUnrelated:
		return true
	}
	return false
}

func validPRChangeSize(value PRChangeSize) bool {
	switch value {
	case PRChangeSizeXS, PRChangeSizeS, PRChangeSizeM, PRChangeSizeL:
		return true
	}
	return false
}

func validPRWorkPresence(value PRWorkPresence) bool {
	return value == PRWorkCandidatePresent || value == PRWorkFollowUp
}

func validatePRScopeProjection(presence PRWorkPresence, changes []PRScopeChange) error {
	if presence != "" && !validPRWorkPresence(presence) {
		return invalidPRWorkspaceField("scope presence", string(presence), 32, true)
	}
	if len(changes) > maxPRWorkspaceListEntries {
		return fmt.Errorf("%w: scope change evidence exceeds bound", ErrInvalidPRWorkspace)
	}
	for _, change := range changes {
		if err := validatePRWorkspaceString("scope change path", change.Path, maxPRWorkspaceTextBytes, true); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("scope change hunk", change.Hunk, maxPRWorkspaceTextBytes, false); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("scope change module", change.Module, maxPRWorkspaceTextBytes, false); err != nil {
			return err
		}
		if change.SemanticLines < 0 || change.Confidence < 0 || change.Confidence > 1 {
			return fmt.Errorf("%w: invalid scope change metrics", ErrInvalidPRWorkspace)
		}
		if !validPRWorkPresence(change.Presence) || !validPRScopeDistance(change.ScopeDistance) || !validPRChangeSize(change.ChangeSize) {
			return fmt.Errorf("%w: invalid scope change classification", ErrInvalidPRWorkspace)
		}
		if err := validatePRWorkspaceStringList("scope change charter clauses", change.CharterClauses, false); err != nil {
			return err
		}
		if err := validatePRWorkspaceString("scope change explanation", change.Explanation, maxPRWorkspaceTextBytes, false); err != nil {
			return err
		}
	}
	return nil
}

func validPRGateOutcome(value PRGateOutcome) bool {
	switch value {
	case PRGatePass, PRGateRevise, PRGateDefer, PRGateBlock:
		return true
	}
	return false
}

func validPRPublicationKind(value PRPublicationKind) bool {
	switch value {
	case PRPublicationGitHubReview, PRPublicationBranchPush, PRPublicationGitHubIssue:
		return true
	}
	return false
}

func validPRPublicationStatus(value PRPublicationStatus) bool {
	switch value {
	case PRPublicationPending, PRPublicationClaimed, PRPublicationPublished, PRPublicationUnknown, PRPublicationFailed:
		return true
	}
	return false
}

func validPrefixedID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+32 {
		return false
	}
	_, err := hex.DecodeString(value[len(prefix):])
	return err == nil
}

func hashPRWorkspaceRequest(kind string, payload []byte) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte(kind))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(payload)
	return hex.EncodeToString(digest.Sum(nil))
}

func loadPRWorkspaceCreateReplay(
	ctx context.Context, conn *sql.Conn, requestID, requestHash string,
) (prWorkspaceCreateResult, bool, error) {
	var kind, storedHash string
	var payload []byte
	err := conn.QueryRowContext(ctx, `SELECT kind, request_hash, result_json
		FROM pr_workspace_requests WHERE request_id = ?`, requestID).Scan(&kind, &storedHash, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return prWorkspaceCreateResult{}, false, nil
	}
	if err != nil {
		return prWorkspaceCreateResult{}, false, err
	}
	if kind != "create" || storedHash != requestHash {
		return prWorkspaceCreateResult{}, false, fmt.Errorf("%w: request ID reused with different content", ErrPRWorkspaceConflict)
	}
	var result prWorkspaceCreateResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return result, false, fmt.Errorf("decode workspace create replay: %w", err)
	}
	return result, true, nil
}

func loadPRWorkspaceMutationReplay(
	ctx context.Context, conn *sql.Conn, input PRWorkspaceMutation, requestHash string,
) (PRWorkspaceMutationResult, bool, error) {
	var workspaceID, kind, storedHash string
	var payload []byte
	err := conn.QueryRowContext(ctx, `SELECT workspace_id, kind, request_hash, result_json
		FROM pr_workspace_requests WHERE request_id = ?`, input.RequestID).
		Scan(&workspaceID, &kind, &storedHash, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return PRWorkspaceMutationResult{}, false, nil
	}
	if err != nil {
		return PRWorkspaceMutationResult{}, false, err
	}
	if workspaceID != input.WorkspaceID || kind != string(input.Kind) || storedHash != requestHash {
		return PRWorkspaceMutationResult{}, false, fmt.Errorf("%w: request ID reused with different content", ErrPRWorkspaceConflict)
	}
	var result PRWorkspaceMutationResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return result, false, fmt.Errorf("decode workspace mutation replay: %w", err)
	}
	return result, true, nil
}

func insertPRWorkspaceRequest(
	ctx context.Context, conn *sql.Conn, requestID, workspaceID, kind, requestHash string,
	result any, now time.Time,
) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode workspace request result: %w", err)
	}
	if len(payload) < 2 || len(payload) > MaxPRWorkspaceMessageBytes {
		return fmt.Errorf("%w: request result exceeds durable bound", ErrInvalidPRWorkspace)
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO pr_workspace_requests (
		request_id, workspace_id, kind, request_hash, result_json, created_at
	) VALUES (?, ?, ?, ?, ?, ?)`, requestID, workspaceID, kind, requestHash, payload, toDBTime(now))
	return err
}

func findPRWorkspaceByIdentity(
	ctx context.Context, queryer rowQueryer, provider PRProviderSnapshot,
) (PRWorkspace, error) {
	return scanPRWorkspace(queryer.QueryRowContext(ctx, `SELECT `+prWorkspaceColumns+`
		FROM pr_workspaces WHERE provider = ? AND provider_origin = ?
		AND repository_id = ? AND pull_request_id = ?`,
		provider.Provider, provider.ProviderOrigin, provider.RepositoryID, provider.PullRequestID))
}

func getPRWorkspaceRecord(ctx context.Context, queryer rowQueryer, workspaceID string) (PRWorkspace, error) {
	workspace, err := scanPRWorkspace(queryer.QueryRowContext(ctx, `SELECT `+prWorkspaceColumns+`
		FROM pr_workspaces WHERE id = ?`, workspaceID))
	if err != nil {
		return PRWorkspace{}, err
	}
	return workspace, nil
}

func scanPRWorkspace(scanner rowScanner) (PRWorkspace, error) {
	var value PRWorkspace
	var owned, writable int
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&value.ID, &value.Provider, &value.ProviderOrigin, &value.RepositoryID,
		&value.Repository, &value.PullRequestID, &value.PullNumber,
		&value.ProviderHeadSHA, &owned, &writable, &value.Phase,
		&value.ExecutionState, &value.CurrentProviderOrdinal,
		&value.ActiveCharterID, &value.Version, &createdAt, &updatedAt,
	); err != nil {
		return PRWorkspace{}, err
	}
	_ = owned
	_ = writable
	value.CreatedAt = fromDBTime(createdAt)
	value.UpdatedAt = fromDBTime(updatedAt)
	return value, nil
}

type prWorkspaceJSONRow struct {
	id      string
	payload []byte
}

func loadPRWorkspaceRecords[T any](ctx context.Context, conn *sql.Conn, table, workspaceID string) ([]T, error) {
	rows, err := conn.QueryContext(ctx, "SELECT payload_json FROM "+table+" WHERE workspace_id = ? ORDER BY ordinal", workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []T
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var value T
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, fmt.Errorf("decode %s record: %w", table, err)
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func loadPRWorkspaceAggregate(ctx context.Context, conn *sql.Conn, workspaceID string) (PRWorkspaceAggregate, error) {
	workspace, err := getPRWorkspaceRecord(ctx, conn, workspaceID)
	if err != nil {
		return PRWorkspaceAggregate{}, err
	}
	result := PRWorkspaceAggregate{Workspace: workspace}
	if result.ProviderSnapshots, err = loadPRWorkspaceRecords[PRProviderSnapshot](ctx, conn, "pr_provider_snapshots", workspaceID); err != nil {
		return result, err
	}
	for _, snapshot := range result.ProviderSnapshots {
		if snapshot.Ordinal == workspace.CurrentProviderOrdinal {
			result.ProviderSnapshot = snapshot
			break
		}
	}
	if result.ProviderSnapshot.ID == "" {
		return result, fmt.Errorf("%w: current provider snapshot is missing", ErrSchemaInvalid)
	}
	if result.Charters, err = loadPRWorkspaceRecords[PRCharterRevision](ctx, conn, "pr_charter_revisions", workspaceID); err != nil {
		return result, err
	}
	if result.StageRuns, err = loadPRWorkspaceRecords[PRStageRun](ctx, conn, "pr_stage_runs", workspaceID); err != nil {
		return result, err
	}
	if result.Findings, err = loadPRWorkspaceRecords[PRFinding](ctx, conn, "pr_findings", workspaceID); err != nil {
		return result, err
	}
	if result.FindingEvents, err = loadPRWorkspaceRecords[PRFindingEvent](ctx, conn, "pr_finding_events", workspaceID); err != nil {
		return result, err
	}
	if result.Conversations, err = loadPRWorkspaceRecords[PRConversation](ctx, conn, "pr_conversations", workspaceID); err != nil {
		return result, err
	}
	if result.Messages, err = loadPRWorkspaceRecords[PRMessage](ctx, conn, "pr_messages", workspaceID); err != nil {
		return result, err
	}
	if result.Corrections, err = loadPRWorkspaceRecords[PRCorrection](ctx, conn, "pr_corrections", workspaceID); err != nil {
		return result, err
	}
	if result.RepositoryLessons, err = loadPRRepositoryLessonsForWorkspace(ctx, conn, workspace); err != nil {
		return result, err
	}
	if result.NudgeRounds, err = loadPRWorkspaceRecords[PRNudgeRound](ctx, conn, "pr_nudge_rounds", workspaceID); err != nil {
		return result, err
	}
	if result.NudgeRewards, err = loadPRWorkspaceRecords[PRNudgeReward](ctx, conn, "pr_nudge_rewards", workspaceID); err != nil {
		return result, err
	}
	if result.DeferredGroups, err = loadPRWorkspaceRecords[PRDeferredGroup](ctx, conn, "pr_deferred_groups", workspaceID); err != nil {
		return result, err
	}
	itemsByGroup := make(map[string][]PRDeferredGroupItem)
	items, err := loadPRWorkspaceRecords[PRDeferredGroupItem](ctx, conn, "pr_deferred_group_items", workspaceID)
	if err != nil {
		return result, err
	}
	for _, item := range items {
		if item.Removed {
			continue
		}
		itemsByGroup[item.GroupID] = append(itemsByGroup[item.GroupID], item)
	}
	for index := range result.DeferredGroups {
		result.DeferredGroups[index].Items = itemsByGroup[result.DeferredGroups[index].ID]
		sort.Slice(result.DeferredGroups[index].Items, func(left, right int) bool {
			return result.DeferredGroups[index].Items[left].OrdinalInGroup < result.DeferredGroups[index].Items[right].OrdinalInGroup
		})
		for _, item := range result.DeferredGroups[index].Items {
			for findingIndex := range result.Findings {
				if result.Findings[findingIndex].ID == item.FindingID {
					result.Findings[findingIndex].DeferredGroupID = item.GroupID
				}
			}
		}
	}
	if result.RepairAttempts, err = loadPRWorkspaceRecords[PRRepairAttempt](ctx, conn, "pr_repair_attempts", workspaceID); err != nil {
		return result, err
	}
	if result.ValidationRuns, err = loadPRWorkspaceRecords[PRValidationRun](ctx, conn, "pr_validation_runs", workspaceID); err != nil {
		return result, err
	}
	if result.GateRuns, err = loadPRWorkspaceRecords[PRGateRun](ctx, conn, "pr_gate_runs", workspaceID); err != nil {
		return result, err
	}
	if result.GateDecisions, err = loadPRWorkspaceRecords[PRGateDecision](ctx, conn, "pr_gate_decisions", workspaceID); err != nil {
		return result, err
	}
	if result.Publications, err = loadPRWorkspaceRecords[PRPublication](ctx, conn, "pr_publications", workspaceID); err != nil {
		return result, err
	}
	if result.OperationIntents, err = loadPRWorkspaceRecords[PRWorkspaceOperationIntent](ctx, conn, "pr_operation_intents", workspaceID); err != nil {
		return result, err
	}
	if result.IngressWatermarks, err = loadPRWorkspaceRecords[PRIngressWatermark](ctx, conn, "pr_ingress_watermarks", workspaceID); err != nil {
		return result, err
	}
	if result.Activity, err = loadPRWorkspaceRecords[PRActivity](ctx, conn, "pr_activity", workspaceID); err != nil {
		return result, err
	}
	return result, nil
}

func loadPRRepositoryLessonsForWorkspace(ctx context.Context, conn *sql.Conn, workspace PRWorkspace) ([]PRRepositoryLesson, error) {
	rows, err := conn.QueryContext(ctx, `SELECT lesson.payload_json
		FROM pr_repository_lessons AS lesson
		JOIN pr_workspaces AS source ON source.id = lesson.workspace_id
		WHERE source.provider_origin = ? AND source.repository_id = ? AND lesson.status = 'active'
		ORDER BY lesson.created_at, lesson.id`, workspace.ProviderOrigin, workspace.RepositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PRRepositoryLesson
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var lesson PRRepositoryLesson
		if err := json.Unmarshal(payload, &lesson); err != nil {
			return nil, fmt.Errorf("decode repository lesson: %w", err)
		}
		result = append(result, lesson)
	}
	return result, rows.Err()
}

type prWorkspaceHistoryValue struct {
	table   string
	id      string
	payload []byte
}

func appendPRWorkspaceHistory(ctx context.Context, conn *sql.Conn, workspaceID string, version int64, records []*prWorkspaceMutationRecord, now time.Time) error {
	workspace, err := getPRWorkspaceRecord(ctx, conn, workspaceID)
	if err != nil {
		return err
	}
	if workspace.Version != version {
		return fmt.Errorf("%w: history version does not match workspace", ErrPRWorkspaceConflict)
	}
	workspaceJSON, err := json.Marshal(workspace)
	if err != nil {
		return err
	}
	values := []prWorkspaceHistoryValue{{table: "pr_workspaces", id: workspace.ID, payload: workspaceJSON}}
	touched := make(map[string]map[string]struct{})
	charterTouched := false
	repositoryLessonTouched := version == 1
	for _, record := range records {
		if record == nil || record.meta == nil {
			continue
		}
		if record.table == "pr_charter_revisions" {
			charterTouched = true
		}
		if record.table == "pr_repository_lessons" {
			repositoryLessonTouched = true
		}
		ids := touched[record.table]
		if ids == nil {
			ids = make(map[string]struct{})
			touched[record.table] = ids
		}
		ids[record.meta.ID] = struct{}{}
	}
	if charterTouched {
		rows, err := conn.QueryContext(ctx, `SELECT id FROM pr_charter_revisions WHERE workspace_id = ?`, workspaceID)
		if err != nil {
			return err
		}
		ids := touched["pr_charter_revisions"]
		if ids == nil {
			ids = make(map[string]struct{})
			touched["pr_charter_revisions"] = ids
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids[id] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
	}
	var keys []struct{ table, id string }
	for table, ids := range touched {
		for id := range ids {
			keys = append(keys, struct{ table, id string }{table, id})
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].table == keys[j].table {
			return keys[i].id < keys[j].id
		}
		return keys[i].table < keys[j].table
	})
	for _, key := range keys {
		var payload []byte
		if err := conn.QueryRowContext(ctx, "SELECT payload_json FROM "+key.table+" WHERE id = ? AND workspace_id = ?", key.id, workspaceID).Scan(&payload); err != nil {
			return err
		}
		values = append(values, prWorkspaceHistoryValue{table: key.table, id: key.id, payload: payload})
	}
	if repositoryLessonTouched {
		marker, _ := json.Marshal(map[string]int64{"workspace_version": version})
		values = append(values, prWorkspaceHistoryValue{table: "pr_repository_lesson_view_marker", id: "view", payload: marker})
		lessons, err := loadPRRepositoryLessonsForWorkspace(ctx, conn, workspace)
		if err != nil {
			return err
		}
		for _, lesson := range lessons {
			payload, err := json.Marshal(lesson)
			if err != nil {
				return err
			}
			values = append(values, prWorkspaceHistoryValue{table: "pr_repository_lesson_view", id: lesson.ID, payload: payload})
		}
	}
	for sequence, value := range values {
		if len(value.payload) < 2 || len(value.payload) > MaxPRWorkspaceRecordBytes {
			return fmt.Errorf("%w: history payload exceeds bound", ErrInvalidPRWorkspace)
		}
		id, err := newPrefixedID(prHistoryIDPrefix)
		if err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, `INSERT INTO pr_workspace_history (
			id, workspace_id, version, sequence, record_table, record_id, payload_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, workspaceID, version, sequence, value.table, value.id, value.payload, toDBTime(now)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) getPRWorkspaceAtVersion(ctx context.Context, workspaceID string, version int64) (PRWorkspaceAggregate, error) {
	if err := s.ready(ctx); err != nil {
		return PRWorkspaceAggregate{}, err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return PRWorkspaceAggregate{}, s.dbError(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN"); err != nil {
		return PRWorkspaceAggregate{}, s.dbError(err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	result, err := loadPRWorkspaceAggregateAtVersion(ctx, conn, workspaceID, version)
	if err != nil {
		return PRWorkspaceAggregate{}, s.dbError(err)
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return PRWorkspaceAggregate{}, s.dbError(err)
	}
	committed = true
	return result, nil
}

func loadPRWorkspaceAggregateAtVersion(ctx context.Context, conn *sql.Conn, workspaceID string, version int64) (PRWorkspaceAggregate, error) {
	if version <= 0 {
		return PRWorkspaceAggregate{}, fmt.Errorf("%w: history version must be positive", ErrInvalidPRWorkspace)
	}
	var current int64
	if err := conn.QueryRowContext(ctx, `SELECT version FROM pr_workspaces WHERE id = ?`, workspaceID).Scan(&current); err != nil {
		return PRWorkspaceAggregate{}, err
	}
	if version > current {
		return PRWorkspaceAggregate{}, fmt.Errorf("%w: history version is newer than workspace", ErrPRWorkspaceConflict)
	}
	rows, err := conn.QueryContext(ctx, `SELECT version, record_table, record_id, payload_json
		FROM pr_workspace_history WHERE workspace_id = ? AND version <= ? ORDER BY version, sequence`, workspaceID, version)
	if err != nil {
		return PRWorkspaceAggregate{}, err
	}
	defer rows.Close()
	records := make(map[string]map[string][]byte)
	lessonViewVersion := int64(0)
	for rows.Next() {
		var recordVersion int64
		var table, id string
		var payload []byte
		if err := rows.Scan(&recordVersion, &table, &id, &payload); err != nil {
			return PRWorkspaceAggregate{}, err
		}
		if table == "pr_repository_lesson_view_marker" {
			records["pr_repository_lesson_view"] = make(map[string][]byte)
			lessonViewVersion = recordVersion
			continue
		}
		if table == "pr_repository_lesson_view" && recordVersion != lessonViewVersion {
			continue
		}
		bucket := records[table]
		if bucket == nil {
			bucket = make(map[string][]byte)
			records[table] = bucket
		}
		bucket[id] = append([]byte(nil), payload...)
	}
	if err := rows.Err(); err != nil {
		return PRWorkspaceAggregate{}, err
	}
	workspacePayload := records["pr_workspaces"][workspaceID]
	if len(workspacePayload) == 0 {
		return PRWorkspaceAggregate{}, fmt.Errorf("%w: workspace history is incomplete", ErrSchemaInvalid)
	}
	var result PRWorkspaceAggregate
	if err := json.Unmarshal(workspacePayload, &result.Workspace); err != nil {
		return result, err
	}
	decode := func(table string, target any) error { return decodePRWorkspaceHistoryRecords(records[table], target) }
	if err := decode("pr_provider_snapshots", &result.ProviderSnapshots); err != nil {
		return result, err
	}
	for _, snapshot := range result.ProviderSnapshots {
		if snapshot.Ordinal == result.Workspace.CurrentProviderOrdinal {
			result.ProviderSnapshot = snapshot
			break
		}
	}
	if result.ProviderSnapshot.ID == "" {
		return result, fmt.Errorf("%w: provider history is incomplete", ErrSchemaInvalid)
	}
	for table, target := range map[string]any{
		"pr_charter_revisions": &result.Charters, "pr_stage_runs": &result.StageRuns, "pr_findings": &result.Findings,
		"pr_finding_events": &result.FindingEvents, "pr_conversations": &result.Conversations, "pr_messages": &result.Messages,
		"pr_corrections": &result.Corrections, "pr_repository_lesson_view": &result.RepositoryLessons, "pr_nudge_rounds": &result.NudgeRounds,
		"pr_nudge_rewards": &result.NudgeRewards, "pr_deferred_groups": &result.DeferredGroups, "pr_repair_attempts": &result.RepairAttempts,
		"pr_validation_runs": &result.ValidationRuns, "pr_gate_runs": &result.GateRuns, "pr_gate_decisions": &result.GateDecisions,
		"pr_publications": &result.Publications, "pr_operation_intents": &result.OperationIntents, "pr_ingress_watermarks": &result.IngressWatermarks,
		"pr_activity": &result.Activity,
	} {
		if err := decode(table, target); err != nil {
			return result, err
		}
	}
	var items []PRDeferredGroupItem
	if err := decode("pr_deferred_group_items", &items); err != nil {
		return result, err
	}
	attachPRDeferredItems(&result, items)
	return result, nil
}

func decodePRWorkspaceHistoryRecords(records map[string][]byte, target any) error {
	if len(records) == 0 {
		return nil
	}
	values := make([]json.RawMessage, 0, len(records))
	for _, payload := range records {
		values = append(values, append([]byte(nil), payload...))
	}
	sort.Slice(values, func(i, j int) bool {
		var left, right struct {
			Ordinal int64 `json:"ordinal"`
		}
		_ = json.Unmarshal(values[i], &left)
		_ = json.Unmarshal(values[j], &right)
		return left.Ordinal < right.Ordinal
	})
	encoded, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

func attachPRDeferredItems(result *PRWorkspaceAggregate, items []PRDeferredGroupItem) {
	itemsByGroup := make(map[string][]PRDeferredGroupItem)
	for _, item := range items {
		if item.Removed {
			continue
		}
		itemsByGroup[item.GroupID] = append(itemsByGroup[item.GroupID], item)
	}
	for index := range result.DeferredGroups {
		result.DeferredGroups[index].Items = itemsByGroup[result.DeferredGroups[index].ID]
		sort.Slice(result.DeferredGroups[index].Items, func(i, j int) bool {
			return result.DeferredGroups[index].Items[i].OrdinalInGroup < result.DeferredGroups[index].Items[j].OrdinalInGroup
		})
		for _, item := range result.DeferredGroups[index].Items {
			for findingIndex := range result.Findings {
				if result.Findings[findingIndex].ID == item.FindingID {
					result.Findings[findingIndex].DeferredGroupID = item.GroupID
				}
			}
		}
	}
}
