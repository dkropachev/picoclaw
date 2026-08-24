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
	"strings"
	"time"

	developmentnotifications "github.com/sipeed/picoclaw/pkg/developmentnotifications"
)

func reconcileDevelopmentNotificationsTx(
	ctx context.Context, conn *sql.Conn, aggregate PRWorkspaceAggregate, now time.Time,
) error {
	drafts := eventingNotificationDrafts(aggregate)
	active := make(map[string]struct{}, len(drafts))
	for _, draft := range drafts {
		active[draft.SourceKey] = struct{}{}
		if _, err := upsertDevelopmentNotificationTx(ctx, conn, draft, now); err != nil {
			return err
		}
	}
	rows, err := conn.QueryContext(ctx, `SELECT payload_json FROM development_notifications
		WHERE workspace_id = ? AND status = 'open'`, aggregate.Workspace.ID)
	if err != nil {
		return err
	}
	defer func() {
		_ = rows.Close()
	}()
	var existing []developmentnotifications.Notification
	for rows.Next() {
		var payload []byte
		if scanErr := rows.Scan(&payload); scanErr != nil {
			return scanErr
		}
		var notification developmentnotifications.Notification
		if decodeErr := json.Unmarshal(payload, &notification); decodeErr != nil {
			return decodeErr
		}
		existing = append(existing, notification)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return rowsErr
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, notification := range existing {
		if _, ok := active[notification.SourceKey]; ok {
			continue
		}
		resolved, changed, resolveErr := developmentnotifications.Resolve(notification, now)
		if resolveErr != nil {
			return resolveErr
		}
		if changed {
			if err := writeDevelopmentNotificationTx(ctx, conn, resolved); err != nil {
				return err
			}
		}
	}
	return nil
}

func eventingNotificationDrafts(aggregate PRWorkspaceAggregate) []developmentnotifications.Draft {
	workspace := aggregate.Workspace
	base := func(
		reason developmentnotifications.Reason, entity, title, summary, panel string, generation uint64,
	) developmentnotifications.Draft {
		if generation == 0 {
			generation = 1
		}
		sourceKey := strings.Join([]string{workspace.ID, string(reason), entity}, ":")
		return developmentnotifications.Draft{
			ID: developmentNotificationID(sourceKey), SourceKey: sourceKey, Generation: generation,
			WorkspaceID: workspace.ID, Repository: workspace.Repository,
			Intent:     developmentnotifications.Intent(workspace.Intent),
			SourceKind: developmentnotifications.SourceKind(workspace.SourceKind),
			Phase:      string(workspace.Phase), Reason: reason, Title: title, Summary: summary,
			Target: developmentnotifications.Target{Panel: panel, EntityID: entity},
		}
	}
	var drafts []developmentnotifications.Draft
	var latestCharter *PRCharterRevision
	for index := range aggregate.Charters {
		charter := &aggregate.Charters[index]
		if latestCharter == nil || charter.Ordinal > latestCharter.Ordinal {
			latestCharter = charter
		}
	}
	if latestCharter != nil && latestCharter.Status == PRRecordDraft && latestCharter.ClarificationNeeded {
		drafts = append(drafts, base(
			developmentnotifications.ReasonCharterAmbiguity, latestCharter.ID,
			"Feature clarification needed", latestCharter.ClarificationQuestion, "charter",
			uint64(latestCharter.Revision),
		))
	}
	for _, gate := range aggregate.GateRuns {
		if gate.State != PRExecutionWaitingUser {
			continue
		}
		reason, title, panel := developmentnotifications.ReasonScopeException, "Scope decision needed", "scope"
		switch {
		case strings.Contains(gate.DecisionPoint, "charter"):
			reason, title, panel = developmentnotifications.ReasonCharterAmbiguity, "Charter decision needed", "charter"
		case strings.Contains(gate.DecisionPoint, "publish"):
			reason, title, panel = developmentnotifications.ReasonPublicationApproval, "Publication approval needed", "publication"
		case strings.Contains(gate.DecisionPoint, "reconcile"):
			reason, title, panel = developmentnotifications.ReasonProviderOutcomeUnknown, "Provider outcome needs reconciliation", "publication"
		}
		drafts = append(drafts, base(reason, gate.ID, title, gate.DecisionPoint, panel, uint64(len(gate.Turns)+1)))
	}
	for _, publication := range aggregate.Publications {
		if publication.ExecutionState == PRExecutionUnknown {
			drafts = append(drafts, base(
				developmentnotifications.ReasonProviderOutcomeUnknown, publication.ID,
				"Provider outcome is unknown", "Check provider state before retrying.", "publication",
				uint64(publication.Attempts),
			))
		}
	}
	if workspace.ExecutionState == PRExecutionFailed || workspace.ExecutionState == PRExecutionBlocked {
		generation := uint64(1)
		if len(aggregate.StageRuns) > 0 && aggregate.StageRuns[len(aggregate.StageRuns)-1].Attempt > 0 {
			generation = uint64(aggregate.StageRuns[len(aggregate.StageRuns)-1].Attempt)
		}
		drafts = append(drafts, base(
			developmentnotifications.ReasonImplementationBlocked,
			workspace.ID,
			"Implementation is blocked",
			"Review the latest failed stage and validation evidence.",
			"overview",
			generation,
		))
	}
	for index, message := range aggregate.Messages {
		if !strings.HasPrefix(message.Kind, "development_chat:steer:needs_clarification") ||
			(message.CharterID != "" && message.CharterID != workspace.ActiveCharterID) {
			continue
		}
		superseded := false
		for _, later := range aggregate.Messages[index+1:] {
			if strings.HasPrefix(later.Kind, "development_chat:steer:") {
				superseded = true
				break
			}
		}
		if !superseded {
			drafts = append(drafts, base(
				developmentnotifications.ReasonSteeringScopeChange, message.ID,
				"Steering would change scope", "Send narrower steering or start a new feature with expanded scope.",
				"chat", 1,
			))
		}
	}
	return drafts
}

func developmentNotificationID(sourceKey string) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("picoclaw-pr-workspace-id-v1\x00dnt_\x00"))
	_, _ = digest.Write([]byte(sourceKey))
	_, _ = digest.Write([]byte{0})
	return "dnt_" + hex.EncodeToString(digest.Sum(nil)[:16])
}

func (s *Store) UpsertDevelopmentNotification(
	ctx context.Context,
	draft developmentnotifications.Draft,
) (developmentnotifications.UpsertResult, error) {
	if err := s.ready(ctx); err != nil {
		return developmentnotifications.UpsertResult{}, err
	}
	now, err := s.currentTime()
	if err != nil {
		return developmentnotifications.UpsertResult{}, err
	}
	var result developmentnotifications.UpsertResult
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		var upsertErr error
		result, upsertErr = upsertDevelopmentNotificationTx(ctx, conn, draft, now)
		return upsertErr
	})
	if err != nil {
		return developmentnotifications.UpsertResult{}, s.dbError(err)
	}
	return result, nil
}

func upsertDevelopmentNotificationTx(
	ctx context.Context, conn *sql.Conn, draft developmentnotifications.Draft, now time.Time,
) (developmentnotifications.UpsertResult, error) {
	var current *developmentnotifications.Notification
	var payload []byte
	scanErr := conn.QueryRowContext(ctx, `SELECT payload_json FROM development_notifications
		WHERE source_key = ?`, draft.SourceKey).Scan(&payload)
	if scanErr == nil {
		var value developmentnotifications.Notification
		if decodeErr := json.Unmarshal(payload, &value); decodeErr != nil {
			return developmentnotifications.UpsertResult{}, decodeErr
		}
		current = &value
	} else if !errors.Is(scanErr, sql.ErrNoRows) {
		return developmentnotifications.UpsertResult{}, scanErr
	}
	result, err := developmentnotifications.Upsert(current, draft, now)
	if err != nil || !result.Changed {
		return result, err
	}
	if err := writeDevelopmentNotificationTx(ctx, conn, result.Notification); err != nil {
		return developmentnotifications.UpsertResult{}, err
	}
	return result, nil
}

func writeDevelopmentNotificationTx(
	ctx context.Context, conn *sql.Conn, notification developmentnotifications.Notification,
) error {
	encoded, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO development_notifications (
		id, source_key, workspace_id, generation, status, priority, version,
		payload_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(source_key) DO UPDATE SET generation=excluded.generation,
		status=excluded.status, priority=excluded.priority, version=excluded.version,
		payload_json=excluded.payload_json, updated_at=excluded.updated_at`,
		notification.ID, notification.SourceKey, notification.WorkspaceID,
		notification.Generation, notification.Status, notification.Priority,
		notification.Version, encoded, toDBTime(notification.CreatedAt), toDBTime(notification.UpdatedAt))
	return err
}

func (s *Store) ListDevelopmentNotifications(ctx context.Context) ([]developmentnotifications.Notification, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload_json FROM development_notifications
		ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, s.dbError(err)
	}
	defer rows.Close()
	var result []developmentnotifications.Notification
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, s.dbError(err)
		}
		var notification developmentnotifications.Notification
		if err := json.Unmarshal(payload, &notification); err != nil || notification.Validate() != nil {
			return nil, fmt.Errorf("development notification payload is invalid")
		}
		result = append(result, notification)
	}
	return result, rows.Err()
}

func (s *Store) ListRecentDevelopmentPushNotifications(
	ctx context.Context, limit int,
) ([]developmentnotifications.Notification, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 2048 {
		return nil, ErrInvalidPRWorkspace
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload_json FROM development_notifications
		WHERE status = 'open' AND priority IN ('critical','high')
		ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, s.dbError(err)
	}
	defer rows.Close()
	result := make([]developmentnotifications.Notification, 0, limit)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, s.dbError(err)
		}
		var notification developmentnotifications.Notification
		if err := json.Unmarshal(payload, &notification); err != nil || notification.Validate() != nil {
			return nil, fmt.Errorf("development notification payload is invalid")
		}
		result = append(result, notification)
	}
	if err := rows.Err(); err != nil {
		return nil, s.dbError(err)
	}
	return result, nil
}

func (s *Store) GetDevelopmentNotification(
	ctx context.Context, id string,
) (developmentnotifications.Notification, error) {
	if err := s.ready(ctx); err != nil {
		return developmentnotifications.Notification{}, err
	}
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload_json FROM development_notifications WHERE id = ?`, id).
		Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return developmentnotifications.Notification{}, ErrNotFound
	}
	if err != nil {
		return developmentnotifications.Notification{}, s.dbError(err)
	}
	var value developmentnotifications.Notification
	if err := json.Unmarshal(payload, &value); err != nil || value.Validate() != nil {
		return developmentnotifications.Notification{}, fmt.Errorf("development notification payload is invalid")
	}
	return value, nil
}

func (s *Store) MutateDevelopmentNotification(
	ctx context.Context,
	id string,
	expectedVersion uint64,
	action string,
	snoozedUntil *time.Time,
) (developmentnotifications.Notification, error) {
	if err := s.ready(ctx); err != nil {
		return developmentnotifications.Notification{}, err
	}
	now, err := s.currentTime()
	if err != nil {
		return developmentnotifications.Notification{}, err
	}
	var result developmentnotifications.Notification
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		var mutateErr error
		result, mutateErr = mutateDevelopmentNotificationTx(
			ctx, conn, id, expectedVersion, action, snoozedUntil, now,
		)
		return mutateErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return developmentnotifications.Notification{}, ErrNotFound
	}
	if err != nil {
		return developmentnotifications.Notification{}, s.dbError(err)
	}
	return result, nil
}

func (s *Store) MutateDevelopmentNotifications(
	ctx context.Context,
	input DevelopmentNotificationBulkMutation,
) (DevelopmentNotificationBulkMutationResult, error) {
	if err := s.ready(ctx); err != nil {
		return DevelopmentNotificationBulkMutationResult{}, err
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.Action = strings.TrimSpace(input.Action)
	if err := validatePRWorkspaceRequestID(input.RequestID); err != nil {
		return DevelopmentNotificationBulkMutationResult{}, err
	}
	if len(input.Items) == 0 || len(input.Items) > 100 || !validDevelopmentNotificationAction(input.Action) {
		return DevelopmentNotificationBulkMutationResult{}, ErrInvalidPRWorkspace
	}
	if input.Action == "snooze" {
		if input.SnoozedUntil == nil {
			return DevelopmentNotificationBulkMutationResult{}, developmentnotifications.ErrInvalidTransition
		}
		value := input.SnoozedUntil.UTC()
		input.SnoozedUntil = &value
	} else if input.SnoozedUntil != nil {
		return DevelopmentNotificationBulkMutationResult{}, ErrInvalidPRWorkspace
	}
	seen := make(map[string]struct{}, len(input.Items))
	for _, item := range input.Items {
		if !validPrefixedID(item.ID, "dnt_") || item.ExpectedVersion == 0 {
			return DevelopmentNotificationBulkMutationResult{}, ErrInvalidPRWorkspace
		}
		if _, exists := seen[item.ID]; exists {
			return DevelopmentNotificationBulkMutationResult{}, ErrInvalidPRWorkspace
		}
		seen[item.ID] = struct{}{}
	}
	canonical, err := json.Marshal(input)
	if err != nil {
		return DevelopmentNotificationBulkMutationResult{}, err
	}
	requestHash := hashPRWorkspaceRequest("development-notification-bulk", canonical)
	now, err := s.currentTime()
	if err != nil {
		return DevelopmentNotificationBulkMutationResult{}, err
	}
	result := DevelopmentNotificationBulkMutationResult{}
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		var storedHash string
		var stored []byte
		scanErr := conn.QueryRowContext(ctx, `SELECT request_hash, result_json
			FROM development_notification_requests WHERE request_id = ?`, input.RequestID).
			Scan(&storedHash, &stored)
		if scanErr == nil {
			if storedHash != requestHash {
				return fmt.Errorf("%w: request ID reused with different content", ErrPRWorkspaceConflict)
			}
			if decodeErr := json.Unmarshal(stored, &result); decodeErr != nil {
				return fmt.Errorf("decode development notification replay: %w", decodeErr)
			}
			result.Replayed = true
			return nil
		}
		if !errors.Is(scanErr, sql.ErrNoRows) {
			return scanErr
		}
		result.Notifications = make([]developmentnotifications.Notification, 0, len(input.Items))
		for _, item := range input.Items {
			notification, mutateErr := mutateDevelopmentNotificationTx(
				ctx, conn, item.ID, item.ExpectedVersion, input.Action, input.SnoozedUntil, now,
			)
			if mutateErr != nil {
				return mutateErr
			}
			result.Notifications = append(result.Notifications, notification)
		}
		encoded, encodeErr := json.Marshal(result)
		if encodeErr != nil {
			return encodeErr
		}
		if len(encoded) < 2 || len(encoded) > MaxPRWorkspaceRecordBytes {
			return fmt.Errorf("%w: notification bulk result exceeds durable bound", ErrInvalidPRWorkspace)
		}
		_, insertErr := conn.ExecContext(ctx, `INSERT INTO development_notification_requests (
			request_id, request_hash, result_json, created_at
		) VALUES (?, ?, ?, ?)`, input.RequestID, requestHash, encoded, toDBTime(now))
		return insertErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return DevelopmentNotificationBulkMutationResult{}, ErrNotFound
	}
	if err != nil {
		return DevelopmentNotificationBulkMutationResult{}, s.dbError(err)
	}
	return result, nil
}

func mutateDevelopmentNotificationTx(
	ctx context.Context,
	conn *sql.Conn,
	id string,
	expectedVersion uint64,
	action string,
	snoozedUntil *time.Time,
	now time.Time,
) (developmentnotifications.Notification, error) {
	var payload []byte
	if err := conn.QueryRowContext(ctx, `SELECT payload_json FROM development_notifications WHERE id = ?`, id).
		Scan(&payload); err != nil {
		return developmentnotifications.Notification{}, err
	}
	var current developmentnotifications.Notification
	if err := json.Unmarshal(payload, &current); err != nil || current.Validate() != nil {
		return developmentnotifications.Notification{}, fmt.Errorf("development notification payload is invalid")
	}
	if current.Version != expectedVersion {
		return developmentnotifications.Notification{}, ErrPRWorkspaceConflict
	}
	var result developmentnotifications.Notification
	var changed bool
	var err error
	switch action {
	case "mark_read":
		result, changed, err = developmentnotifications.MarkRead(current, true, now)
	case "mark_unread":
		result, changed, err = developmentnotifications.MarkRead(current, false, now)
	case "snooze":
		if snoozedUntil == nil {
			return developmentnotifications.Notification{}, developmentnotifications.ErrInvalidTransition
		}
		result, changed, err = developmentnotifications.Snooze(current, *snoozedUntil, now)
	case "clear_snooze":
		result, changed, err = developmentnotifications.ClearSnooze(current, now)
	case "archive":
		result, changed, err = developmentnotifications.Archive(current, now)
	case "resolve":
		result, changed, err = developmentnotifications.Resolve(current, now)
	default:
		return developmentnotifications.Notification{}, developmentnotifications.ErrInvalidTransition
	}
	if err != nil || !changed {
		return result, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return developmentnotifications.Notification{}, err
	}
	updated, err := conn.ExecContext(ctx, `UPDATE development_notifications SET
		status=?, priority=?, version=?, payload_json=?, updated_at=? WHERE id=? AND version=?`,
		result.Status, result.Priority, result.Version, encoded, toDBTime(result.UpdatedAt), id, expectedVersion)
	if err != nil {
		return developmentnotifications.Notification{}, err
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return developmentnotifications.Notification{}, err
	}
	if count != 1 {
		return developmentnotifications.Notification{}, ErrPRWorkspaceConflict
	}
	return result, nil
}

func validDevelopmentNotificationAction(action string) bool {
	switch action {
	case "mark_read", "mark_unread", "snooze", "clear_snooze", "archive", "resolve":
		return true
	default:
		return false
	}
}

func (s *Store) GetDevelopmentNotificationViews(ctx context.Context) (DevelopmentNotificationViewsDocument, error) {
	if err := s.ready(ctx); err != nil {
		return DevelopmentNotificationViewsDocument{}, err
	}
	var version uint64
	var payload []byte
	scanErr := s.db.QueryRowContext(ctx, `SELECT version, payload_json FROM development_notification_views WHERE id='singleton'`).
		Scan(&version, &payload)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return DevelopmentNotificationViewsDocument{Views: []developmentnotifications.SavedView{}, Version: 1}, nil
	}
	if scanErr != nil {
		return DevelopmentNotificationViewsDocument{}, s.dbError(scanErr)
	}
	var views []developmentnotifications.SavedView
	if decodeErr := json.Unmarshal(payload, &views); decodeErr != nil {
		return DevelopmentNotificationViewsDocument{}, decodeErr
	}
	views, validateErr := developmentnotifications.ValidateSavedViews(views)
	return DevelopmentNotificationViewsDocument{Views: views, Version: version}, validateErr
}

func (s *Store) PutDevelopmentNotificationViews(
	ctx context.Context,
	views []developmentnotifications.SavedView,
	expectedVersion uint64,
) (DevelopmentNotificationViewsDocument, error) {
	views, err := developmentnotifications.ValidateSavedViews(views)
	if err != nil || expectedVersion == 0 {
		return DevelopmentNotificationViewsDocument{}, developmentnotifications.ErrInvalidSavedView
	}
	encoded, err := json.Marshal(views)
	if err != nil {
		return DevelopmentNotificationViewsDocument{}, err
	}
	now, err := s.currentTime()
	if err != nil {
		return DevelopmentNotificationViewsDocument{}, err
	}
	nextVersion := expectedVersion + 1
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		var current uint64
		scanErr := conn.QueryRowContext(ctx, `SELECT version FROM development_notification_views WHERE id='singleton'`).
			Scan(&current)
		if errors.Is(scanErr, sql.ErrNoRows) {
			current = 1
		} else if scanErr != nil {
			return scanErr
		}
		if current != expectedVersion {
			return developmentnotifications.ErrStaleViewVersion
		}
		_, execErr := conn.ExecContext(
			ctx,
			`INSERT INTO development_notification_views(id,version,payload_json,updated_at)
			VALUES('singleton',?,?,?) ON CONFLICT(id) DO UPDATE SET version=excluded.version,
			payload_json=excluded.payload_json,updated_at=excluded.updated_at`,
			nextVersion,
			encoded,
			toDBTime(now),
		)
		return execErr
	})
	if err != nil {
		return DevelopmentNotificationViewsDocument{}, s.dbError(err)
	}
	return DevelopmentNotificationViewsDocument{Views: views, Version: nextVersion}, nil
}

func (s *Store) GetDevelopmentPushState(ctx context.Context) (DevelopmentPushStateDocument, error) {
	if err := s.ready(ctx); err != nil {
		return DevelopmentPushStateDocument{}, err
	}
	var version uint64
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT version,payload_json FROM development_push_state
		WHERE id='singleton'`).Scan(&version, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return DevelopmentPushStateDocument{Version: 1, State: json.RawMessage(`{}`)}, nil
	}
	if err != nil {
		return DevelopmentPushStateDocument{}, s.dbError(err)
	}
	if !json.Valid(payload) {
		return DevelopmentPushStateDocument{}, fmt.Errorf("development push state is invalid")
	}
	return DevelopmentPushStateDocument{Version: version, State: append(json.RawMessage(nil), payload...)}, nil
}

func (s *Store) PutDevelopmentPushState(
	ctx context.Context, state json.RawMessage, expectedVersion uint64,
) (DevelopmentPushStateDocument, error) {
	if expectedVersion == 0 || len(state) < 2 || len(state) > MaxPRWorkspaceRecordBytes || !json.Valid(state) {
		return DevelopmentPushStateDocument{}, ErrInvalidPRWorkspace
	}
	now, err := s.currentTime()
	if err != nil {
		return DevelopmentPushStateDocument{}, err
	}
	next := expectedVersion + 1
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		var current uint64
		scanErr := conn.QueryRowContext(ctx, `SELECT version FROM development_push_state WHERE id='singleton'`).
			Scan(&current)
		if errors.Is(scanErr, sql.ErrNoRows) {
			current = 1
		} else if scanErr != nil {
			return scanErr
		}
		if current != expectedVersion {
			return ErrPRWorkspaceConflict
		}
		_, execErr := conn.ExecContext(ctx, `INSERT INTO development_push_state(id,version,payload_json,updated_at)
			VALUES('singleton',?,?,?) ON CONFLICT(id) DO UPDATE SET version=excluded.version,
			payload_json=excluded.payload_json,updated_at=excluded.updated_at`, next, []byte(state), toDBTime(now))
		return execErr
	})
	if err != nil {
		return DevelopmentPushStateDocument{}, s.dbError(err)
	}
	return DevelopmentPushStateDocument{Version: next, State: append(json.RawMessage(nil), state...)}, nil
}

func (s *Store) PruneDevelopmentNotifications(
	ctx context.Context, before time.Time, limit int,
) (int64, error) {
	if err := s.ready(ctx); err != nil {
		return 0, err
	}
	if before.IsZero() || limit < 1 {
		return 0, ErrInvalidPRWorkspace
	}
	if limit > 500 {
		limit = 500
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM development_notifications WHERE id IN (
		SELECT id FROM development_notifications
		WHERE status IN ('resolved','archived') AND updated_at < ?
		ORDER BY updated_at ASC,id ASC LIMIT ?
	)`, toDBTime(before.UTC()), limit)
	if err != nil {
		return 0, s.dbError(err)
	}
	return result.RowsAffected()
}
