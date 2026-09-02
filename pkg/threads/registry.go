//nolint:govet,sqlclosecheck // Transactional SQL uses narrow error scopes and explicit closes before dependent queries.
package threads

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

const (
	RegistrationAuto     = "auto"
	RegistrationTool     = "tool"
	RegistrationManual   = "manual"
	RegistrationMigrated = "migrated"
)

func (s Store) withDefaults() Store {
	if strings.TrimSpace(s.Workspace) == "" {
		if strings.TrimSpace(s.Dir) != "" {
			s.Workspace = filepath.Dir(s.Dir)
		} else {
			s.Workspace = ResolveWorkspace("")
		}
	}
	if strings.TrimSpace(s.Dir) == "" {
		s.Dir = filepath.Join(s.Workspace, "sessions")
	}
	if strings.TrimSpace(s.ThreadsDir) == "" {
		s.ThreadsDir = filepath.Join(s.Workspace, "threads")
	}
	if strings.TrimSpace(s.HandoffsDir) == "" {
		s.HandoffsDir = filepath.Join(s.ThreadsDir, "handoffs")
	}
	return s
}

// Deprecated legacy path helpers remain for source compatibility and import
// fixture construction. Runtime thread state is stored only in sessions.db.
func (s Store) threadPath(id string) string {
	s = s.withDefaults()
	return filepath.Join(s.ThreadsDir, sanitizeThreadID(id)+".json")
}

func threadTimeParts(value time.Time) (int64, int) {
	return value.Unix(), value.Nanosecond()
}

func nullableThreadTimeParts(value *time.Time) (any, any) {
	if value == nil || value.IsZero() {
		return nil, nil
	}
	return value.Unix(), value.Nanosecond()
}

func scanThreadTime(seconds, nanos int64) time.Time {
	return time.Unix(seconds, nanos).UTC()
}

func resolveSessionKeySQL(
	ctx context.Context,
	queryer interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
		QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	},
	requested string,
) (string, bool, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", false, nil
	}
	var key string
	err := queryer.QueryRowContext(ctx,
		`SELECT session_key FROM sessions WHERE session_key = ?`, requested).Scan(&key)
	if err == nil {
		return key, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	rows, err := queryer.QueryContext(ctx, `SELECT session_key FROM session_aliases
        WHERE alias = ? ORDER BY session_key LIMIT 2`, requested)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	owners := make([]string, 0, 2)
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			return "", false, err
		}
		owners = append(owners, owner)
	}
	if len(owners) > 1 {
		return "", false, fmt.Errorf("threads: session alias %q is ambiguous", requested)
	}
	if len(owners) == 0 {
		return "", false, rows.Err()
	}
	return owners[0], true, rows.Err()
}

func rejectReviewSessionSQL(ctx context.Context, conn *sql.Conn, key string) error {
	var channel string
	err := conn.QueryRowContext(ctx,
		`SELECT channel FROM session_scopes WHERE session_key = ?`, key).Scan(&channel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(channel), "review") {
		return errReviewScope
	}
	return nil
}

func ensureThreadSessionSQL(ctx context.Context, conn *sql.Conn, key string, now time.Time) error {
	seconds, nanos := threadTimeParts(now)
	_, err := conn.ExecContext(ctx, `INSERT INTO sessions (
        session_key, created_seconds, created_nanos, updated_seconds, updated_nanos, version
    ) VALUES (?, ?, ?, ?, ?, 1) ON CONFLICT(session_key) DO NOTHING`,
		key, seconds, nanos, seconds, nanos)
	return err
}

func readThreadMetaSQL(
	ctx context.Context,
	queryer interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
		QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	},
	id string,
) (ThreadMeta, bool, int64, error) {
	var meta ThreadMeta
	var droppedSeconds, droppedNanos sql.NullInt64
	var createdSeconds, createdNanos, updatedSeconds, updatedNanos, version int64
	err := queryer.QueryRowContext(ctx, `SELECT thread_id, ui_session_id,
        primary_session_key, agent_id, owner_identity, title, thread_type, source_query,
        registration, dropped_seconds, dropped_nanos, created_seconds, created_nanos,
        updated_seconds, updated_nanos, version FROM threads WHERE thread_id = ?`, id).Scan(
		&meta.ID, &meta.UISessionID, &meta.PrimarySessionKey, &meta.AgentID,
		&meta.OwnerIdentity, &meta.Title, &meta.Type, &meta.SourceQuery, &meta.Registration,
		&droppedSeconds, &droppedNanos, &createdSeconds, &createdNanos,
		&updatedSeconds, &updatedNanos, &version,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ThreadMeta{}, false, 0, nil
	}
	if err != nil {
		return ThreadMeta{}, false, 0, err
	}
	meta.CreatedAt = scanThreadTime(createdSeconds, createdNanos)
	meta.UpdatedAt = scanThreadTime(updatedSeconds, updatedNanos)
	if droppedSeconds.Valid && droppedNanos.Valid {
		dropped := scanThreadTime(droppedSeconds.Int64, droppedNanos.Int64)
		meta.DroppedAt = &dropped
	}
	contextRows, err := queryer.QueryContext(ctx,
		`SELECT key, value FROM thread_context WHERE thread_id = ? ORDER BY key`, id)
	if err != nil {
		return ThreadMeta{}, false, 0, err
	}
	for contextRows.Next() {
		var key, value string
		if err := contextRows.Scan(&key, &value); err != nil {
			_ = contextRows.Close()
			return ThreadMeta{}, false, 0, err
		}
		if meta.Context == nil {
			meta.Context = make(map[string]string)
		}
		meta.Context[key] = value
	}
	if rowsErr := contextRows.Err(); rowsErr != nil {
		_ = contextRows.Close()
		return ThreadMeta{}, false, 0, rowsErr
	}
	if err := contextRows.Close(); err != nil {
		return ThreadMeta{}, false, 0, err
	}
	aliasRows, err := queryer.QueryContext(ctx,
		`SELECT alias FROM thread_aliases WHERE thread_id = ? ORDER BY sequence`, id)
	if err != nil {
		return ThreadMeta{}, false, 0, err
	}
	for aliasRows.Next() {
		var alias string
		if err := aliasRows.Scan(&alias); err != nil {
			_ = aliasRows.Close()
			return ThreadMeta{}, false, 0, err
		}
		meta.Aliases = append(meta.Aliases, alias)
	}
	if rowsErr := aliasRows.Err(); rowsErr != nil {
		_ = aliasRows.Close()
		return ThreadMeta{}, false, 0, rowsErr
	}
	if err := aliasRows.Close(); err != nil {
		return ThreadMeta{}, false, 0, err
	}
	sessionRows, err := queryer.QueryContext(ctx, `SELECT session_key FROM thread_sessions
        WHERE thread_id = ? ORDER BY sequence`, id)
	if err != nil {
		return ThreadMeta{}, false, 0, err
	}
	for sessionRows.Next() {
		var key string
		if err := sessionRows.Scan(&key); err != nil {
			_ = sessionRows.Close()
			return ThreadMeta{}, false, 0, err
		}
		meta.SessionKeys = append(meta.SessionKeys, key)
	}
	if rowsErr := sessionRows.Err(); rowsErr != nil {
		_ = sessionRows.Close()
		return ThreadMeta{}, false, 0, rowsErr
	}
	if err := sessionRows.Close(); err != nil {
		return ThreadMeta{}, false, 0, err
	}
	return normalizeThreadMeta(meta), true, version, nil
}

func (s Store) readThreadMeta(id string) (ThreadMeta, error) {
	store, release, err := s.borrowSessionStore()
	if err != nil {
		return ThreadMeta{}, err
	}
	defer release()
	meta, found, _, err := readThreadMetaSQL(context.Background(), threadSessionDatabase(store), id)
	if err != nil {
		return ThreadMeta{}, err
	}
	if !found {
		return ThreadMeta{}, os.ErrNotExist
	}
	return meta, nil
}

func (s Store) GetMeta(id string) (ThreadMeta, bool, error) {
	if s.brokerClient != nil {
		id = strings.TrimSpace(id)
		if id == "" {
			return ThreadMeta{}, false, nil
		}
		return s.brokerGetMeta(context.Background(), id)
	}
	meta, err := s.readThreadMeta(strings.TrimSpace(id))
	if errors.Is(err, os.ErrNotExist) {
		return ThreadMeta{}, false, nil
	}
	return meta, err == nil, err
}

func (s Store) writeThreadMeta(meta ThreadMeta) error {
	store, release, err := s.borrowSessionStore()
	if err != nil {
		return err
	}
	defer release()
	meta = normalizeThreadMeta(meta)
	return threadSessionAdapter(store).Immediate(context.Background(), func(ctx context.Context, conn *sql.Conn) error {
		resolvedPrimary, found, err := resolveSessionKeySQL(ctx, conn, meta.PrimarySessionKey)
		if err != nil {
			return err
		}
		if found {
			meta.PrimarySessionKey = resolvedPrimary
			meta.SessionKeys = uniqueStrings(append([]string{resolvedPrimary}, meta.SessionKeys...))
		} else if err := ensureThreadSessionSQL(
			ctx,
			conn,
			meta.PrimarySessionKey,
			time.Now().UTC(),
		); err != nil {
			return err
		}
		_, found, version, err := readThreadMetaSQL(ctx, conn, meta.ID)
		if err != nil {
			return err
		}
		if !found {
			return insertThreadSQL(ctx, conn, meta)
		}
		updatedSeconds, updatedNanos := threadTimeParts(meta.UpdatedAt)
		droppedSeconds, droppedNanos := nullableThreadTimeParts(meta.DroppedAt)
		result, err := conn.ExecContext(ctx, `UPDATE threads SET ui_session_id = ?,
            primary_session_key = ?, agent_id = ?, owner_identity = ?, title = ?,
            thread_type = ?, source_query = ?, registration = ?, dropped_seconds = ?,
            dropped_nanos = ?, updated_seconds = ?, updated_nanos = ?, version = version + 1
            WHERE thread_id = ? AND version = ?`, meta.UISessionID, meta.PrimarySessionKey,
			meta.AgentID, meta.OwnerIdentity, meta.Title, meta.Type, meta.SourceQuery,
			meta.Registration, droppedSeconds, droppedNanos, updatedSeconds, updatedNanos,
			meta.ID, version)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errors.New("threads: thread changed concurrently")
		}
		return writeThreadChildrenSQL(ctx, conn, meta)
	})
}

func (s Store) setSessionThreadLink(
	ctx context.Context,
	sessionKey,
	threadID string,
	attachedAt time.Time,
) error {
	store, release, err := s.borrowSessionStore()
	if err != nil {
		return err
	}
	defer release()
	return threadSessionAdapter(
		store,
	).Immediate(contextOrBackground(ctx), func(ctx context.Context, conn *sql.Conn) error {
		key, found, err := resolveSessionKeySQL(ctx, conn, sessionKey)
		if err != nil || !found {
			return firstThreadError(err, errSessionMissing)
		}
		if _, threadFound, _, err := readThreadMetaSQL(ctx, conn, threadID); err != nil {
			return err
		} else if !threadFound {
			now := attachedAt.UTC()
			placeholder := ThreadMeta{
				ID: threadID, UISessionID: threadID, PrimarySessionKey: key,
				AgentID: "main", OwnerIdentity: "unknown", Title: "New thread",
				Type: TypeGeneral, Registration: RegistrationMigrated,
				SessionKeys: []string{key}, CreatedAt: now, UpdatedAt: now,
			}
			if err := insertThreadSQL(ctx, conn, placeholder); err != nil {
				return err
			}
		}
		seconds, nanos := threadTimeParts(attachedAt)
		_, err = conn.ExecContext(ctx, `INSERT INTO session_thread_links (
            session_key, thread_id, attached_seconds, attached_nanos
        ) VALUES (?, ?, ?, ?) ON CONFLICT(session_key) DO UPDATE SET
            thread_id = excluded.thread_id, attached_seconds = excluded.attached_seconds,
            attached_nanos = excluded.attached_nanos`, key, threadID, seconds, nanos)
		return err
	})
}

func (s Store) listThreadMetas() ([]ThreadMeta, error) {
	store, release, err := s.borrowSessionStore()
	if err != nil {
		return nil, err
	}
	defer release()
	if err := threadSessionAdapter(
		store,
	).Immediate(context.Background(), func(ctx context.Context, conn *sql.Conn) error {
		return migrateUnregisteredPicoSessionsSQL(ctx, conn)
	}); err != nil {
		return nil, err
	}
	rows, err := threadSessionDatabase(store).Query(`SELECT thread_id FROM threads
        ORDER BY updated_seconds DESC, updated_nanos DESC, thread_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := make([]ThreadMeta, 0, len(ids))
	for _, id := range ids {
		meta, found, _, err := readThreadMetaSQL(context.Background(), threadSessionDatabase(store), id)
		if err != nil {
			return nil, err
		}
		if found {
			items = append(items, meta)
		}
	}
	return items, nil
}

type picoSessionMigrationCandidate struct {
	key                          string
	summary                      string
	createdSeconds, createdNanos sql.NullInt64
	updatedSeconds, updatedNanos sql.NullInt64
	agentID, channel, account    string
}

// migrateUnregisteredPicoSessionsSQL preserves the launcher-era behavior
// where a Pico conversation becomes discoverable as a thread even when no
// explicit registration call was made. The projection is now created once in
// the same database transaction instead of by writing registry JSON.
func migrateUnregisteredPicoSessionsSQL(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `SELECT s.session_key, s.summary,
        s.created_seconds, s.created_nanos, s.updated_seconds, s.updated_nanos,
        sc.agent_id, sc.channel, sc.account
        FROM sessions s JOIN session_scopes sc ON sc.session_key = s.session_key
        WHERE NOT EXISTS (
            SELECT 1 FROM thread_sessions ts WHERE ts.session_key = s.session_key
        ) AND NOT EXISTS (
            SELECT 1 FROM session_thread_links sl WHERE sl.session_key = s.session_key
        ) ORDER BY s.session_key`)
	if err != nil {
		return err
	}
	candidates := make([]picoSessionMigrationCandidate, 0)
	for rows.Next() {
		var candidate picoSessionMigrationCandidate
		if err := rows.Scan(
			&candidate.key, &candidate.summary,
			&candidate.createdSeconds, &candidate.createdNanos,
			&candidate.updatedSeconds, &candidate.updatedNanos,
			&candidate.agentID, &candidate.channel, &candidate.account,
		); err != nil {
			_ = rows.Close()
			return err
		}
		candidates = append(candidates, candidate)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		_ = rows.Close()
		return rowsErr
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, candidate := range candidates {
		if !strings.EqualFold(strings.TrimSpace(candidate.channel), "pico") {
			continue
		}
		scope := session.SessionScope{
			Version: 1, AgentID: candidate.agentID, Channel: candidate.channel,
			Account: candidate.account, Values: make(map[string]string),
		}
		dimensionRows, err := conn.QueryContext(ctx, `SELECT dimension, value, is_dimension
            FROM session_scope_dimensions WHERE session_key = ? ORDER BY sequence`, candidate.key)
		if err != nil {
			return err
		}
		for dimensionRows.Next() {
			var dimension, value string
			var isDimension int
			if err := dimensionRows.Scan(&dimension, &value, &isDimension); err != nil {
				_ = dimensionRows.Close()
				return err
			}
			if isDimension == 1 {
				scope.Dimensions = append(scope.Dimensions, dimension)
			}
			scope.Values[dimension] = value
		}
		if rowsErr := dimensionRows.Err(); rowsErr != nil {
			_ = dimensionRows.Close()
			return rowsErr
		}
		if err := dimensionRows.Close(); err != nil {
			return err
		}
		picoID, ok := picoSessionIDFromScope(scope)
		if !ok {
			continue
		}
		var existing int
		if err := conn.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM threads WHERE thread_id = ?`, picoID,
		).Scan(&existing); err != nil {
			return err
		}
		if existing != 0 {
			continue
		}

		aliases := make([]string, 0)
		aliasRows, err := conn.QueryContext(ctx, `SELECT alias FROM session_aliases
            WHERE session_key = ? ORDER BY sequence`, candidate.key)
		if err != nil {
			return err
		}
		for aliasRows.Next() {
			var alias string
			if err := aliasRows.Scan(&alias); err != nil {
				_ = aliasRows.Close()
				return err
			}
			aliases = append(aliases, alias)
		}
		if rowsErr := aliasRows.Err(); rowsErr != nil {
			_ = aliasRows.Close()
			return rowsErr
		}
		if err := aliasRows.Close(); err != nil {
			return err
		}

		preview := ""
		if err := conn.QueryRowContext(ctx, `SELECT content FROM session_messages
            WHERE session_key = ? AND role = 'user' AND trim(content) <> ''
            ORDER BY sequence LIMIT 1`, candidate.key).Scan(&preview); err != nil &&
			!errors.Is(err, sql.ErrNoRows) {
			return err
		}
		title := truncateRunes(firstNonEmpty(preview, candidate.summary, "New thread"), 80)
		created := time.Unix(0, 0).UTC()
		if candidate.createdSeconds.Valid && candidate.createdNanos.Valid {
			created = scanThreadTime(candidate.createdSeconds.Int64, candidate.createdNanos.Int64)
		}
		updated := created
		if candidate.updatedSeconds.Valid && candidate.updatedNanos.Valid {
			updated = scanThreadTime(candidate.updatedSeconds.Int64, candidate.updatedNanos.Int64)
		}
		meta := ThreadMeta{
			ID: picoID, UISessionID: picoID, PrimarySessionKey: candidate.key,
			AgentID:       firstNonEmpty(scope.AgentID, "main"),
			OwnerIdentity: ownerIdentityFromScope(&scope), Title: title,
			Type:        NormalizeType(InferType(title + " " + candidate.summary)),
			Context:     scopeContext(mustMarshalThreadScope(scope)),
			SessionKeys: []string{candidate.key}, Aliases: aliases,
			Registration: RegistrationMigrated, CreatedAt: created, UpdatedAt: updated,
		}
		if err := insertThreadSQL(ctx, conn, meta); err != nil {
			return err
		}
		seconds, nanos := threadTimeParts(updated)
		if _, err := conn.ExecContext(ctx, `INSERT INTO session_thread_links (
            session_key, thread_id, attached_seconds, attached_nanos
        ) VALUES (?, ?, ?, ?)`, candidate.key, picoID, seconds, nanos); err != nil {
			return err
		}
	}
	return nil
}

func mustMarshalThreadScope(scope session.SessionScope) json.RawMessage {
	data, _ := json.Marshal(scope)
	return data
}

func (s Store) threadFromRegistryMeta(meta ThreadMeta) (Thread, bool) {
	state, err := s.readOrdinarySessionState(context.Background(), meta.PrimarySessionKey, true)
	if err != nil {
		return Thread{}, false
	}
	return threadFromOrdinarySessionState(meta, state)
}

func threadFromOrdinarySessionState(meta ThreadMeta, state ordinarySessionState) (Thread, bool) {
	meta = normalizeThreadMeta(meta)
	if !state.found || state.key == "" {
		return Thread{}, false
	}
	if state.key != meta.PrimarySessionKey {
		meta.SessionKeys = uniqueStrings(append([]string{state.key}, meta.SessionKeys...))
		meta.PrimarySessionKey = state.key
	}
	visible := visibleMessages(state.history)
	preview := ""
	for _, message := range visible {
		if message.Role == "user" {
			preview = messagePreview(message)
			break
		}
	}
	preview = firstNonEmpty(preview, state.meta.Summary, meta.SourceQuery, meta.Title, "(empty)")
	title := firstNonEmpty(meta.Title, preview, "New thread")
	updated := meta.UpdatedAt
	if state.meta.UpdatedAt.After(updated) {
		updated = state.meta.UpdatedAt
	}
	updated = firstThreadTime(updated, meta.CreatedAt, state.historyModifiedAt)
	created := firstThreadTime(meta.CreatedAt, state.meta.CreatedAt, updated)
	if created.IsZero() && updated.IsZero() {
		return Thread{}, false
	}
	return Thread{
		ID: meta.ID, UISessionID: meta.UISessionID, SessionKey: meta.PrimarySessionKey,
		PrimarySessionKey: meta.PrimarySessionKey, AgentID: meta.AgentID,
		OwnerIdentity: meta.OwnerIdentity, Title: truncateRunes(title, 80),
		Preview: truncateRunes(preview, 120), Type: NormalizeType(meta.Type),
		Context:      MergeContext(scopeContext(state.meta.Scope), meta.Context),
		MessageCount: len(visible), Created: created, Updated: updated,
		SourceQuery: strings.TrimSpace(meta.SourceQuery), Discoverable: meta.DroppedAt == nil,
		DroppedAt: meta.DroppedAt,
	}, true
}

func firstThreadTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func writeThreadChildrenSQL(ctx context.Context, conn *sql.Conn, meta ThreadMeta) error {
	type linkRow struct {
		sessionKey      string
		attachedSeconds int64
		attachedNanos   int64
	}
	linkRows, err := conn.QueryContext(ctx, `SELECT session_key, attached_seconds,
        attached_nanos FROM session_thread_links WHERE thread_id = ? ORDER BY session_key`, meta.ID)
	if err != nil {
		return err
	}
	links := make([]linkRow, 0)
	for linkRows.Next() {
		var link linkRow
		if err := linkRows.Scan(
			&link.sessionKey, &link.attachedSeconds, &link.attachedNanos,
		); err != nil {
			_ = linkRows.Close()
			return err
		}
		links = append(links, link)
	}
	if rowsErr := linkRows.Err(); rowsErr != nil {
		_ = linkRows.Close()
		return rowsErr
	}
	if err := linkRows.Close(); err != nil {
		return err
	}
	for _, table := range []string{"thread_context", "thread_aliases", "thread_sessions"} {
		if _, err := conn.ExecContext(ctx, "DELETE FROM "+table+" WHERE thread_id = ?", meta.ID); err != nil {
			return err
		}
	}
	contextKeys := make([]string, 0, len(meta.Context))
	for key := range meta.Context {
		contextKeys = append(contextKeys, key)
	}
	sort.Strings(contextKeys)
	for _, key := range contextKeys {
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO thread_context(thread_id, key, value) VALUES (?, ?, ?)`,
			meta.ID, key, meta.Context[key]); err != nil {
			return err
		}
	}
	for index, alias := range uniqueStrings(meta.Aliases) {
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO thread_aliases(thread_id, sequence, alias) VALUES (?, ?, ?)`,
			meta.ID, index, alias); err != nil {
			return err
		}
	}
	position := 0
	retainedSessions := make(map[string]struct{})
	for _, key := range uniqueStrings(append([]string{meta.PrimarySessionKey}, meta.SessionKeys...)) {
		var exists int
		if err := conn.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sessions WHERE session_key = ?`, key).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			continue
		}
		retainedSessions[key] = struct{}{}
		primary := 0
		if key == meta.PrimarySessionKey {
			primary = 1
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO thread_sessions (
            thread_id, sequence, session_key, is_primary
        ) VALUES (?, ?, ?, ?)`, meta.ID, position, key, primary); err != nil {
			return err
		}
		position++
	}
	for _, link := range links {
		if _, retained := retainedSessions[link.sessionKey]; !retained {
			continue
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO session_thread_links (
            session_key, thread_id, attached_seconds, attached_nanos
        ) VALUES (?, ?, ?, ?)`, link.sessionKey, meta.ID,
			link.attachedSeconds, link.attachedNanos); err != nil {
			return err
		}
	}
	return nil
}

func insertThreadSQL(ctx context.Context, conn *sql.Conn, meta ThreadMeta) error {
	meta = normalizeThreadMeta(meta)
	if meta.ID == "" || meta.PrimarySessionKey == "" {
		return errors.New("threads: thread identity is invalid")
	}
	createdSeconds, createdNanos := threadTimeParts(meta.CreatedAt)
	updatedSeconds, updatedNanos := threadTimeParts(meta.UpdatedAt)
	droppedSeconds, droppedNanos := nullableThreadTimeParts(meta.DroppedAt)
	if _, err := conn.ExecContext(ctx, `INSERT INTO threads (
        thread_id, ui_session_id, primary_session_key, agent_id, owner_identity, title,
        thread_type, source_query, registration, dropped_seconds, dropped_nanos,
        created_seconds, created_nanos, updated_seconds, updated_nanos, version
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		meta.ID, meta.UISessionID, meta.PrimarySessionKey, meta.AgentID, meta.OwnerIdentity,
		meta.Title, meta.Type, meta.SourceQuery, meta.Registration, droppedSeconds, droppedNanos,
		createdSeconds, createdNanos, updatedSeconds, updatedNanos); err != nil {
		return err
	}
	return writeThreadChildrenSQL(ctx, conn, meta)
}

func (s Store) CreateThread(ctx context.Context, request CreateRequest) (Thread, error) {
	if s.brokerClient != nil {
		storeID, err := s.resolvedBrokerStoreID(ctx)
		if err != nil {
			return Thread{}, err
		}
		thread, found, err := s.brokerThreadMutation(
			ctx, threadOperationCreate,
			threadCreateRequest{StoreID: storeID, Request: cloneCreateRequest(request)},
		)
		if err != nil {
			return Thread{}, err
		}
		if !found {
			return Thread{}, database.NewError(database.CodeIntegrity, "thread broker response is invalid")
		}
		return thread, nil
	}
	ctx = contextOrBackground(ctx)
	store, release, err := s.borrowSessionStore()
	if err != nil {
		return Thread{}, err
	}
	defer release()
	now := time.Now().UTC()
	id := strings.TrimSpace(request.ID)
	if id == "" {
		id = GenerateSessionID()
	}
	primary := strings.TrimSpace(request.PrimarySessionKey)
	if primary == "" {
		return Thread{}, errors.New("threads: primary session key is empty")
	}
	if resolved, found, err := resolveSessionKeySQL(ctx, threadSessionDatabase(store), primary); err != nil {
		return Thread{}, err
	} else if found {
		primary = resolved
		if err := rejectReviewSessionDB(ctx, threadSessionDatabase(store), primary); err != nil {
			return Thread{}, err
		}
	}
	if s.testHooks != nil && s.testHooks.afterCreatePreflight != nil {
		s.testHooks.afterCreatePreflight()
	}
	err = threadSessionAdapter(store).Immediate(ctx, func(ctx context.Context, conn *sql.Conn) error {
		resolved, found, err := resolveSessionKeySQL(ctx, conn, primary)
		if err != nil {
			return err
		}
		if found {
			primary = resolved
		} else if err := ensureThreadSessionSQL(ctx, conn, primary, now); err != nil {
			return err
		}
		if err := rejectReviewSessionSQL(ctx, conn, primary); err != nil {
			return err
		}
		meta := ThreadMeta{
			ID:                id,
			UISessionID:       firstNonEmpty(request.UISessionID, id),
			PrimarySessionKey: primary,
			AgentID:           firstNonEmpty(request.AgentID, routingAgentFromSessionKey(primary), "main"),
			OwnerIdentity:     firstNonEmpty(request.OwnerIdentity, "unknown"),
			Title:             truncateRunes(firstNonEmpty(request.Title, request.SourceQuery, "New thread"), 80),
			Type: NormalizeType(
				firstNonEmpty(request.Type, InferType(request.Title+" "+request.SourceQuery)),
			),
			Context:      MergeContext(ExtractContext(request.SourceQuery+" "+request.Title), request.Context),
			SourceQuery:  strings.TrimSpace(firstNonEmpty(request.SourceQuery, request.Title, "New thread")),
			SessionKeys:  uniqueStrings(append([]string{primary}, request.SessionKeys...)),
			Registration: firstNonEmpty(normalizeRegistration(request.Registration), RegistrationManual),
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if s.testHooks != nil && s.testHooks.writeThreadMeta != nil {
			if err := s.testHooks.writeThreadMeta(meta); err != nil {
				return err
			}
		}
		if err := insertThreadSQL(ctx, conn, meta); err != nil {
			return err
		}
		seconds, nanos := threadTimeParts(now)
		_, err = conn.ExecContext(ctx, `INSERT INTO session_thread_links (
            session_key, thread_id, attached_seconds, attached_nanos
        ) VALUES (?, ?, ?, ?) ON CONFLICT(session_key) DO UPDATE SET
            thread_id = excluded.thread_id, attached_seconds = excluded.attached_seconds,
            attached_nanos = excluded.attached_nanos`, primary, id, seconds, nanos)
		return err
	})
	if err != nil {
		return Thread{}, err
	}
	thread, found, err := s.Get(id)
	if err != nil {
		return Thread{}, err
	}
	if !found {
		return Thread{}, errors.New("threads: created thread could not be loaded")
	}
	return thread, nil
}

func (s Store) UpdateThread(id string, request UpdateRequest) (Thread, bool, error) {
	if s.brokerClient != nil {
		storeID, err := s.resolvedBrokerStoreID(context.Background())
		if err != nil {
			return Thread{}, false, err
		}
		return s.brokerThreadMutation(
			context.Background(), threadOperationUpdate,
			threadUpdateRequest{
				StoreID: storeID, ID: id, Request: cloneUpdateRequest(request),
			},
		)
	}
	store, release, err := s.borrowSessionStore()
	if err != nil {
		return Thread{}, false, err
	}
	defer release()
	found := false
	err = threadSessionAdapter(store).Immediate(context.Background(), func(ctx context.Context, conn *sql.Conn) error {
		meta, exists, version, err := readThreadMetaSQL(ctx, conn, strings.TrimSpace(id))
		if err != nil || !exists {
			return err
		}
		primary, primaryFound, err := resolveSessionKeySQL(ctx, conn, meta.PrimarySessionKey)
		if err != nil {
			return err
		}
		if !primaryFound {
			return errSessionMissing
		}
		if err := rejectReviewSessionSQL(ctx, conn, primary); err != nil {
			return err
		}
		meta.PrimarySessionKey = primary
		meta.SessionKeys = uniqueStrings(append([]string{primary}, meta.SessionKeys...))
		found = true
		if strings.TrimSpace(request.Title) != "" {
			meta.Title = truncateRunes(request.Title, 80)
		}
		if strings.TrimSpace(request.Type) != "" {
			meta.Type = NormalizeType(request.Type)
		}
		if request.Context != nil {
			meta.Context = cleanContext(request.Context)
		}
		if strings.TrimSpace(request.SourceQuery) != "" {
			meta.SourceQuery = strings.TrimSpace(request.SourceQuery)
		}
		if request.Discoverable != nil {
			if *request.Discoverable {
				meta.DroppedAt = nil
			} else if meta.DroppedAt == nil {
				dropped := time.Now().UTC()
				meta.DroppedAt = &dropped
			}
		}
		meta.UpdatedAt = time.Now().UTC()
		if s.testHooks != nil && s.testHooks.writeThreadMeta != nil {
			if err := s.testHooks.writeThreadMeta(meta); err != nil {
				return err
			}
		}
		updatedSeconds, updatedNanos := threadTimeParts(meta.UpdatedAt)
		droppedSeconds, droppedNanos := nullableThreadTimeParts(meta.DroppedAt)
		result, err := conn.ExecContext(ctx, `UPDATE threads SET title = ?, thread_type = ?,
            source_query = ?, dropped_seconds = ?, dropped_nanos = ?, updated_seconds = ?,
            updated_nanos = ?, version = version + 1 WHERE thread_id = ? AND version = ?`,
			meta.Title, meta.Type, meta.SourceQuery, droppedSeconds, droppedNanos,
			updatedSeconds, updatedNanos, meta.ID, version)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errors.New("threads: thread changed concurrently")
		}
		return writeThreadChildrenSQL(ctx, conn, meta)
	})
	if err != nil || !found {
		return Thread{}, found, err
	}
	thread, found, err := s.Get(id)
	return thread, found, err
}

func (s Store) DropThread(id string) (Thread, bool, error) {
	discoverable := false
	return s.UpdateThread(id, UpdateRequest{Discoverable: &discoverable})
}

func (s Store) RegisterCurrent(
	ctx context.Context,
	request CreateRequest,
	scope *session.SessionScope,
) (Thread, error) {
	if err := rejectReviewThreadSessionScope(scope); err != nil {
		return Thread{}, err
	}
	if strings.TrimSpace(request.PrimarySessionKey) == "" {
		return Thread{}, errors.New("threads: current session key is empty")
	}
	uiSessionID := strings.TrimSpace(request.UISessionID)
	if uiSessionID == "" && scope != nil {
		if picoID, ok := picoSessionIDFromScope(*scope); ok {
			uiSessionID = picoID
		}
	}
	request.UISessionID = firstNonEmpty(uiSessionID, request.ID, request.PrimarySessionKey)
	request.Registration = firstNonEmpty(request.Registration, RegistrationTool)
	request.OwnerIdentity = firstNonEmpty(request.OwnerIdentity, ownerIdentityFromScope(scope))
	return s.CreateThread(ctx, request)
}

func appendSummarySQL(
	ctx context.Context,
	conn *sql.Conn,
	key string,
	message providers.Message,
) error {
	payload, err := json.Marshal(struct {
		Media       []string                 `json:"media,omitempty"`
		Attachments []providers.Attachment   `json:"attachments,omitempty"`
		Parts       []providers.PromptPart   `json:"parts,omitempty"`
		SystemParts []providers.ContentBlock `json:"system_parts,omitempty"`
		ToolCalls   []providers.ToolCall     `json:"tool_calls,omitempty"`
	}{message.Media, message.Attachments, message.Parts, message.SystemParts, message.ToolCalls})
	if err != nil {
		return err
	}
	if string(payload) == "{}" {
		payload = nil
	} else {
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return err
		}
		payload, err = json.Marshal(decoded)
		if err != nil {
			return err
		}
	}
	var sequence int
	if err := conn.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence) + 1, 0) FROM session_messages WHERE session_key = ?`,
		key).Scan(&sequence); err != nil {
		return err
	}
	seconds, nanos := threadTimeParts(*message.CreatedAt)
	_, err = conn.ExecContext(ctx, `INSERT INTO session_messages (
        session_key, sequence, role, content, model_name, created_seconds, created_nanos,
        reasoning_content, tool_call_id, nested_payload
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, key, sequence, message.Role,
		message.Content, message.ModelName, seconds, nanos, message.ReasoningContent,
		message.ToolCallID, payload)
	if err != nil {
		return err
	}
	result, err := conn.ExecContext(ctx, `UPDATE sessions SET updated_seconds = ?,
        updated_nanos = ?, version = version + 1 WHERE session_key = ?`,
		seconds, nanos, key)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("threads: continuation session disappeared")
	}
	return nil
}

func (s Store) AttachCurrent(
	ctx context.Context,
	request AttachRequest,
) (Thread, ThreadHandoff, error) {
	if s.brokerClient != nil {
		storeID, err := s.resolvedBrokerStoreID(ctx)
		if err != nil {
			return Thread{}, ThreadHandoff{}, err
		}
		var response threadBrokerResponse
		err = s.callBroker(
			ctx, threadOperationAttach,
			threadAttachRequest{
				StoreID: storeID, Request: cloneAttachRequest(request),
			},
			&response, true,
		)
		if err != nil {
			return Thread{}, ThreadHandoff{}, err
		}
		if !response.Found || response.Thread == nil || response.Handoff == nil {
			return Thread{}, ThreadHandoff{}, database.NewError(
				database.CodeIntegrity, "thread broker response is invalid",
			)
		}
		return cloneThread(*response.Thread), *response.Handoff, nil
	}
	ctx = contextOrBackground(ctx)
	if err := rejectReviewThreadSessionScope(request.Scope); err != nil {
		return Thread{}, ThreadHandoff{}, err
	}
	if strings.TrimSpace(request.ThreadID) == "" || strings.TrimSpace(request.SessionKey) == "" {
		return Thread{}, ThreadHandoff{}, errors.New("threads: attach identity is invalid")
	}
	store, release, err := s.borrowSessionStore()
	if err != nil {
		return Thread{}, ThreadHandoff{}, err
	}
	defer release()
	preflightMeta, found, _, err := readThreadMetaSQL(ctx, threadSessionDatabase(store), request.ThreadID)
	if err != nil {
		return Thread{}, ThreadHandoff{}, err
	}
	if !found {
		return Thread{}, ThreadHandoff{}, os.ErrNotExist
	}
	if origin, originFound, err := resolveSessionKeySQL(
		ctx,
		threadSessionDatabase(store),
		request.SessionKey,
	); err != nil {
		return Thread{}, ThreadHandoff{}, err
	} else if originFound {
		if err := rejectReviewSessionDB(ctx, threadSessionDatabase(store), origin); err != nil {
			return Thread{}, ThreadHandoff{}, err
		}
	}
	if target, targetFound, err := resolveSessionKeySQL(
		ctx,
		threadSessionDatabase(store),
		preflightMeta.PrimarySessionKey,
	); err != nil {
		return Thread{}, ThreadHandoff{}, err
	} else if targetFound {
		if err := rejectReviewSessionDB(ctx, threadSessionDatabase(store), target); err != nil {
			return Thread{}, ThreadHandoff{}, err
		}
	}
	if s.testHooks != nil && s.testHooks.afterAttachPreflight != nil {
		s.testHooks.afterAttachPreflight()
	}
	var handoff ThreadHandoff
	err = threadSessionAdapter(store).Immediate(ctx, func(ctx context.Context, conn *sql.Conn) error {
		meta, found, version, err := readThreadMetaSQL(ctx, conn, request.ThreadID)
		if err != nil {
			return err
		}
		if !found {
			return os.ErrNotExist
		}
		origin, originFound, err := resolveSessionKeySQL(ctx, conn, request.SessionKey)
		if err != nil || !originFound {
			return firstThreadError(err, errSessionMissing)
		}
		target, targetFound, err := resolveSessionKeySQL(ctx, conn, meta.PrimarySessionKey)
		if err != nil || !targetFound {
			return firstThreadError(err, errSessionMissing)
		}
		if err := rejectReviewSessionSQL(ctx, conn, origin); err != nil {
			return err
		}
		if err := rejectReviewSessionSQL(ctx, conn, target); err != nil {
			return err
		}
		now := time.Now().UTC()
		meta.PrimarySessionKey = target
		meta.SessionKeys = uniqueStrings(append(meta.SessionKeys, origin, target))
		meta.OwnerIdentity = firstNonEmpty(meta.OwnerIdentity, request.OwnerIdentity)
		meta.AgentID = firstNonEmpty(meta.AgentID, request.AgentID)
		meta.UpdatedAt = now
		handoff = ThreadHandoff{
			ID: GenerateHandoffID(), OriginSessionKey: origin,
			OriginSessionID: strings.TrimSpace(request.OriginSessionID),
			TargetThreadID:  meta.ID, TargetSessionID: meta.UISessionID,
			AgentID: firstNonEmpty(request.AgentID, meta.AgentID),
			Summary: strings.TrimSpace(request.Summary), CreatedAt: now,
		}
		if s.testHooks != nil && s.testHooks.writeThreadMeta != nil {
			if err := s.testHooks.writeThreadMeta(meta); err != nil {
				return err
			}
		}
		if s.testHooks != nil && s.testHooks.writeHandoff != nil {
			if err := s.testHooks.writeHandoff(handoff); err != nil {
				return err
			}
		}
		seconds, nanos := threadTimeParts(now)
		result, err := conn.ExecContext(ctx, `UPDATE threads SET primary_session_key = ?,
            agent_id = ?, owner_identity = ?, updated_seconds = ?, updated_nanos = ?,
            version = version + 1 WHERE thread_id = ? AND version = ?`, target,
			meta.AgentID, meta.OwnerIdentity, seconds, nanos, meta.ID, version)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errors.New("threads: thread changed concurrently")
		}
		if err := writeThreadChildrenSQL(ctx, conn, meta); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO session_thread_links (
            session_key, thread_id, attached_seconds, attached_nanos
        ) VALUES (?, ?, ?, ?) ON CONFLICT(session_key) DO UPDATE SET
            thread_id = excluded.thread_id, attached_seconds = excluded.attached_seconds,
            attached_nanos = excluded.attached_nanos`, origin, meta.ID, seconds, nanos); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO thread_handoffs (
            handoff_id, origin_session_key, origin_session_id, target_thread_id,
            target_session_id, agent_id, summary, created_seconds, created_nanos, version
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`, handoff.ID, origin,
			handoff.OriginSessionID, meta.ID, meta.UISessionID, handoff.AgentID,
			handoff.Summary, seconds, nanos); err != nil {
			return err
		}
		if handoff.Summary != "" && target != origin {
			message := providers.Message{
				Role: "user", Content: "Continued from another session.\n\n" + handoff.Summary,
				CreatedAt: &now,
			}
			if s.testHooks != nil && s.testHooks.appendSummary != nil {
				if err := s.testHooks.appendSummary(ctx, target, message); err != nil {
					log.Printf("threads: attach %s summary projection failed: %v", handoff.ID, err)
					return nil
				}
			}
			if err := appendSummarySQL(ctx, conn, target, message); err != nil {
				log.Printf("threads: attach %s summary projection failed: %v", handoff.ID, err)
			}
		}
		return nil
	})
	if err != nil {
		return Thread{}, ThreadHandoff{}, err
	}
	thread, found, err := s.Get(request.ThreadID)
	if err != nil || !found {
		return Thread{}, ThreadHandoff{}, firstThreadError(
			err,
			errors.New("threads: attached thread could not be loaded"),
		)
	}
	return thread, handoff, nil
}

func firstThreadError(actual, fallback error) error {
	if actual != nil {
		return actual
	}
	return fallback
}

func (s Store) DetachCurrent(sessionKey string) error {
	if s.brokerClient != nil {
		storeID, err := s.resolvedBrokerStoreID(context.Background())
		if err != nil {
			return err
		}
		var response threadBrokerResponse
		err = s.callBroker(
			context.Background(), threadOperationDetach,
			threadDetachRequest{StoreID: storeID, SessionKey: sessionKey},
			&response, true,
		)
		if err != nil {
			return err
		}
		if !response.OK {
			return database.NewError(database.CodeIntegrity, "thread broker response is invalid")
		}
		return nil
	}
	store, release, err := s.borrowSessionStore()
	if err != nil {
		return err
	}
	defer release()
	return threadSessionAdapter(store).Immediate(context.Background(), func(ctx context.Context, conn *sql.Conn) error {
		key, found, err := resolveSessionKeySQL(ctx, conn, sessionKey)
		if err != nil || !found {
			return err
		}
		if err := rejectReviewSessionSQL(ctx, conn, key); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `DELETE FROM session_thread_links WHERE session_key = ?`, key)
		return err
	})
}

func readHandoffSQL(
	ctx context.Context,
	queryer interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	},
	id string,
) (ThreadHandoff, bool, error) {
	var handoff ThreadHandoff
	var seconds, nanos int64
	err := queryer.QueryRowContext(ctx, `SELECT handoff_id, origin_session_key,
        origin_session_id, target_thread_id, target_session_id, agent_id, summary,
        created_seconds, created_nanos FROM thread_handoffs WHERE handoff_id = ?`, id).Scan(
		&handoff.ID, &handoff.OriginSessionKey, &handoff.OriginSessionID,
		&handoff.TargetThreadID, &handoff.TargetSessionID, &handoff.AgentID,
		&handoff.Summary, &seconds, &nanos)
	if errors.Is(err, sql.ErrNoRows) {
		return ThreadHandoff{}, false, nil
	}
	if err != nil {
		return ThreadHandoff{}, false, err
	}
	handoff.CreatedAt = scanThreadTime(seconds, nanos)
	return handoff, true, nil
}

func (s Store) ReturnToOrigin(handoffID string) (ThreadHandoff, bool, error) {
	if s.brokerClient != nil {
		storeID, err := s.resolvedBrokerStoreID(context.Background())
		if err != nil {
			return ThreadHandoff{}, false, err
		}
		var response threadBrokerResponse
		err = s.callBroker(
			context.Background(), threadOperationReturnOrigin,
			threadIDRequest{StoreID: storeID, ID: handoffID},
			&response, false,
		)
		if err != nil || !response.Found {
			return ThreadHandoff{}, response.Found, err
		}
		if response.Handoff == nil {
			return ThreadHandoff{}, false, database.NewError(
				database.CodeIntegrity, "thread broker response is invalid",
			)
		}
		return *response.Handoff, true, nil
	}
	store, release, err := s.borrowSessionStore()
	if err != nil {
		return ThreadHandoff{}, false, err
	}
	defer release()
	handoff, found, err := readHandoffSQL(context.Background(), threadSessionDatabase(store), handoffID)
	if err != nil || !found {
		return handoff, found, err
	}
	if _, found, err := resolveSessionKeySQL(
		context.Background(), threadSessionDatabase(store), handoff.OriginSessionKey,
	); err != nil || !found {
		return ThreadHandoff{}, false, err
	}
	if origin, _, err := resolveSessionKeySQL(
		context.Background(), threadSessionDatabase(store), handoff.OriginSessionKey,
	); err != nil {
		return ThreadHandoff{}, false, err
	} else if err := rejectReviewSessionDB(context.Background(), threadSessionDatabase(store), origin); err != nil {
		return ThreadHandoff{}, false, err
	}
	targetMeta, threadFound, _, err := readThreadMetaSQL(
		context.Background(), threadSessionDatabase(store), handoff.TargetThreadID,
	)
	if err != nil || !threadFound {
		return ThreadHandoff{}, false, err
	}
	if target, found, err := resolveSessionKeySQL(
		context.Background(), threadSessionDatabase(store), targetMeta.PrimarySessionKey,
	); err != nil || !found {
		return ThreadHandoff{}, false, err
	} else if err := rejectReviewSessionDB(context.Background(), threadSessionDatabase(store), target); err != nil {
		return ThreadHandoff{}, false, err
	}
	return handoff, true, nil
}

func rejectReviewSessionDB(ctx context.Context, db *sql.DB, key string) error {
	var channel string
	err := db.QueryRowContext(ctx,
		`SELECT channel FROM session_scopes WHERE session_key = ?`, key).Scan(&channel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(channel), "review") {
		return errReviewScope
	}
	return nil
}

func normalizeThreadMeta(meta ThreadMeta) ThreadMeta {
	meta.ID = strings.TrimSpace(meta.ID)
	meta.UISessionID = strings.TrimSpace(firstNonEmpty(meta.UISessionID, meta.ID))
	meta.PrimarySessionKey = strings.TrimSpace(meta.PrimarySessionKey)
	meta.AgentID = strings.TrimSpace(firstNonEmpty(meta.AgentID, "main"))
	meta.OwnerIdentity = strings.TrimSpace(firstNonEmpty(meta.OwnerIdentity, "unknown"))
	meta.Title = truncateRunes(firstNonEmpty(meta.Title, "New thread"), 80)
	meta.Type = NormalizeType(meta.Type)
	meta.Context = cleanContext(meta.Context)
	meta.SourceQuery = strings.TrimSpace(meta.SourceQuery)
	meta.SessionKeys = uniqueStrings(append([]string{meta.PrimarySessionKey}, meta.SessionKeys...))
	meta.Aliases = uniqueStrings(meta.Aliases)
	meta.Registration = firstNonEmpty(normalizeRegistration(meta.Registration), RegistrationManual)
	if meta.DroppedAt != nil && meta.DroppedAt.IsZero() {
		meta.DroppedAt = nil
	}
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = time.Now().UTC()
	}
	if meta.UpdatedAt.IsZero() {
		meta.UpdatedAt = meta.CreatedAt
	}
	return meta
}

func normalizeRegistration(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case RegistrationAuto:
		return RegistrationAuto
	case RegistrationTool:
		return RegistrationTool
	case RegistrationManual:
		return RegistrationManual
	case RegistrationMigrated:
		return RegistrationMigrated
	default:
		return ""
	}
}

func GenerateHandoffID() string { return "handoff-" + GenerateSessionID() }

func sanitizeThreadID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.NewReplacer(":", "_", "/", "_", "\\", "_").Replace(id)
	return id
}

func ownerIdentityFromScope(scope *session.SessionScope) string {
	if scope == nil {
		return "unknown"
	}
	for _, dimension := range []string{"sender", "chat", "space"} {
		if value := strings.TrimSpace(scope.Values[dimension]); value != "" {
			return strings.ToLower(value)
		}
	}
	if scope.Account != "" {
		return strings.ToLower(strings.TrimSpace(scope.Account))
	}
	if scope.AgentID != "" {
		return "agent:" + strings.ToLower(strings.TrimSpace(scope.AgentID))
	}
	return "unknown"
}

func routingAgentFromSessionKey(sessionKey string) string {
	if parsed := session.ParseLegacyAgentSessionKey(sessionKey); parsed != nil {
		return parsed.AgentID
	}
	return ""
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
