//nolint:govet,sqlclosecheck // Transactional SQL uses narrow error scopes and closes rows before dependent queries.
package memory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/messageutil"
	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

// SQLiteStore is the authoritative session, thread, and handoff database.
// JSONLStore remains a deprecated type alias for one compatibility cycle.
type SQLiteStore struct {
	db   *sql.DB
	dir  string
	path string
}

// JSONLStore is retained for source compatibility. It is SQLite-backed.
// Deprecated: use SQLiteStore and NewSQLiteStore.
type JSONLStore = SQLiteStore

// NewSQLiteStore opens <dir>/sessions.db and imports legacy session/thread
// files on the first authoritative open.
func NewSQLiteStore(dir string) (*SQLiteStore, error) {
	return openSQLiteStore(context.Background(), dir)
}

// NewJSONLStore is a source-compatible facade backed by SQLite.
// Deprecated: use NewSQLiteStore.
func NewJSONLStore(dir string) (*JSONLStore, error) { return NewSQLiteStore(dir) }

func openSQLiteStore(ctx context.Context, dir string) (*SQLiteStore, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("memory: sessions directory is required")
	}
	absDir, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return nil, fmt.Errorf("memory: resolve sessions directory: %w", err)
	}
	path := filepath.Join(absDir, SessionsDatabaseFilename)
	legacy, err := newSessionsLegacyOptions(absDir)
	if err != nil {
		return nil, err
	}
	db, err := sqlitestore.Open(ctx, path, sqlitestore.Options{
		Component:    sessionsComponent,
		Migrations:   sessionsMigrations(),
		Validate:     validateSessionsSchema,
		Legacy:       legacy,
		MaxOpenConns: 8,
	})
	if err != nil {
		return nil, err
	}
	return &SQLiteStore{db: db, dir: absDir, path: path}, nil
}

// DBPath returns the canonical SQLite database path.
func (s *SQLiteStore) DBPath() string {
	if s == nil {
		return ""
	}
	return s.path
}

// SQLDB returns the live database handle for the adjacent thread subsystem.
// Callers must not close or retain it beyond the owning SQLiteStore lifetime.
func (s *SQLiteStore) SQLDB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Immediate exposes one transaction boundary to the adjacent thread store.
// The callback must not retain conn or re-enter this store.
func (s *SQLiteStore) Immediate(
	ctx context.Context,
	callback func(context.Context, *sql.Conn) error,
) error {
	if s == nil || s.db == nil {
		return errors.New("memory: SQLite session store is closed")
	}
	if callback == nil {
		return errors.New("memory: SQLite session transaction callback is required")
	}
	ctx = contextOrBackground(ctx)
	return sqlitestore.Immediate(ctx, s.db, func(conn *sql.Conn) error {
		return callback(ctx, conn)
	})
}

func (s *SQLiteStore) beginRead(ctx context.Context) (*sql.Tx, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("memory: SQLite session store is closed")
	}
	return s.db.BeginTx(contextOrBackground(ctx), &sql.TxOptions{ReadOnly: true})
}

type storedMessageNested struct {
	Media       []string                 `json:"media,omitempty"`
	Attachments []providers.Attachment   `json:"attachments,omitempty"`
	Parts       []providers.PromptPart   `json:"parts,omitempty"`
	SystemParts []providers.ContentBlock `json:"system_parts,omitempty"`
	ToolCalls   []providers.ToolCall     `json:"tool_calls,omitempty"`
}

type sqliteSessionScope struct {
	Version    int               `json:"version"`
	AgentID    string            `json:"agent_id"`
	Channel    string            `json:"channel"`
	Account    string            `json:"account"`
	Dimensions []string          `json:"dimensions"`
	Values     map[string]string `json:"values"`
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func sqliteTimeParts(value time.Time) (seconds any, nanos any) {
	if value.IsZero() {
		return nil, nil
	}
	return value.Unix(), value.Nanosecond()
}

func sqliteRequiredTimeParts(value time.Time) (int64, int) {
	return value.Unix(), value.Nanosecond()
}

func timeFromSQLite(seconds, nanos sql.NullInt64) time.Time {
	if !seconds.Valid || !nanos.Valid {
		return time.Time{}
	}
	return time.Unix(seconds.Int64, nanos.Int64).UTC()
}

func encodeStoredMessage(message providers.Message) ([]byte, error) {
	nested := storedMessageNested{
		Media:       append([]string(nil), message.Media...),
		Attachments: append([]providers.Attachment(nil), message.Attachments...),
		Parts:       append([]providers.PromptPart(nil), message.Parts...),
		SystemParts: append([]providers.ContentBlock(nil), message.SystemParts...),
		ToolCalls:   append([]providers.ToolCall(nil), message.ToolCalls...),
	}
	if len(nested.Media) == 0 && len(nested.Attachments) == 0 && len(nested.Parts) == 0 &&
		len(nested.SystemParts) == 0 && len(nested.ToolCalls) == 0 {
		return nil, nil
	}
	payload, err := json.Marshal(nested)
	if err != nil {
		return nil, fmt.Errorf("memory: encode nested message payload: %w", err)
	}
	payload, err = canonicalJSONBlob(payload)
	if err != nil {
		return nil, fmt.Errorf("memory: canonicalize nested message payload: %w", err)
	}
	if len(payload) >= maxLineSize {
		return nil, fmt.Errorf("memory: encoded message exceeds maximum size of %d bytes", maxLineSize-1)
	}
	return payload, nil
}

func canonicalJSONBlob(payload []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("JSON payload has trailing data")
		}
		return nil, err
	}
	return json.Marshal(decoded)
}

func decodeStoredMessageNested(payload []byte, message *providers.Message) error {
	if len(payload) == 0 {
		return nil
	}
	var nested storedMessageNested
	if err := json.Unmarshal(payload, &nested); err != nil {
		return fmt.Errorf("memory: decode nested message payload: %w", err)
	}
	message.Media = nested.Media
	message.Attachments = nested.Attachments
	message.Parts = nested.Parts
	message.SystemParts = nested.SystemParts
	message.ToolCalls = nested.ToolCalls
	return nil
}

func resolveSessionKeyConn(
	ctx context.Context,
	conn interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
		QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	},
	requested string,
	strict bool,
) (string, bool, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", false, nil
	}
	var direct string
	err := conn.QueryRowContext(
		ctx,
		`SELECT session_key FROM sessions WHERE session_key = ?`,
		requested,
	).Scan(&direct)
	directFound := err == nil
	if directFound && !strict {
		return direct, true, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	rows, err := conn.QueryContext(
		ctx,
		`SELECT session_key FROM session_aliases WHERE alias = ? ORDER BY session_key`,
		requested,
	)
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
		if strict && len(owners) > 1 {
			return "", false, fmt.Errorf("memory: session alias %q has multiple owners", requested)
		}
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	if len(owners) == 0 {
		if directFound {
			return direct, true, nil
		}
		return "", false, nil
	}
	return owners[0], true, nil
}

func ensureSessionConn(ctx context.Context, conn *sql.Conn, key string, now time.Time) error {
	seconds, nanos := sqliteRequiredTimeParts(now)
	_, err := conn.ExecContext(ctx, `INSERT INTO sessions (
        session_key, created_seconds, created_nanos, updated_seconds, updated_nanos, version
    ) VALUES (?, ?, ?, ?, ?, 1) ON CONFLICT(session_key) DO NOTHING`,
		key, seconds, nanos, seconds, nanos,
	)
	return err
}

func bumpSessionVersionConn(
	ctx context.Context,
	conn *sql.Conn,
	key string,
	version int64,
	now time.Time,
) error {
	seconds, nanos := sqliteRequiredTimeParts(now)
	result, err := conn.ExecContext(ctx, `UPDATE sessions
        SET updated_seconds = ?, updated_nanos = ?, version = version + 1
        WHERE session_key = ? AND version = ?`, seconds, nanos, key, version)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrSnapshotConflict
	}
	return nil
}

func sessionVersionConn(ctx context.Context, conn *sql.Conn, key string) (int64, error) {
	var version int64
	err := conn.QueryRowContext(ctx, `SELECT version FROM sessions WHERE session_key = ?`, key).Scan(&version)
	return version, err
}

func insertMessageConn(
	ctx context.Context,
	conn *sql.Conn,
	key string,
	sequence int,
	message providers.Message,
) error {
	payload, err := encodeStoredMessage(message)
	if err != nil {
		return err
	}
	var createdSeconds, createdNanos any
	if message.CreatedAt != nil && !message.CreatedAt.IsZero() {
		createdSeconds, createdNanos = sqliteTimeParts(*message.CreatedAt)
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO session_messages (
        session_key, sequence, role, content, model_name, created_seconds, created_nanos,
        reasoning_content, tool_call_id, nested_payload
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key, sequence, message.Role, message.Content, message.ModelName,
		createdSeconds, createdNanos, message.ReasoningContent, message.ToolCallID, payload,
	)
	return err
}

func readMessagesConn(
	ctx context.Context,
	queryer interface {
		QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	},
	key string,
) ([]providers.Message, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT role, content, model_name,
        created_seconds, created_nanos, reasoning_content, tool_call_id, nested_payload
        FROM session_messages WHERE session_key = ? ORDER BY sequence`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]providers.Message, 0)
	for rows.Next() {
		var message providers.Message
		var seconds, nanos sql.NullInt64
		var payload []byte
		if err := rows.Scan(
			&message.Role, &message.Content, &message.ModelName, &seconds, &nanos,
			&message.ReasoningContent, &message.ToolCallID, &payload,
		); err != nil {
			return nil, err
		}
		if seconds.Valid {
			created := timeFromSQLite(seconds, nanos)
			message.CreatedAt = &created
		}
		if err := decodeStoredMessageNested(payload, &message); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *SQLiteStore) AddMessage(
	ctx context.Context,
	sessionKey, role, content string,
) error {
	return s.AddFullMessage(ctx, sessionKey, providers.Message{Role: role, Content: content})
}

func (s *SQLiteStore) AddFullMessage(
	ctx context.Context,
	sessionKey string,
	message providers.Message,
) error {
	if messageutil.IsTransientAssistantThoughtMessage(message) {
		return nil
	}
	ctx = contextOrBackground(ctx)
	return s.Immediate(ctx, func(ctx context.Context, conn *sql.Conn) error {
		key, found, err := resolveSessionKeyConn(ctx, conn, sessionKey, false)
		if err != nil {
			return err
		}
		if !found {
			key = strings.TrimSpace(sessionKey)
		}
		now := time.Now().UTC()
		if message.CreatedAt == nil || message.CreatedAt.IsZero() {
			created := now
			message.CreatedAt = &created
		}
		if err := ensureSessionConn(ctx, conn, key, now); err != nil {
			return err
		}
		version, err := sessionVersionConn(ctx, conn, key)
		if err != nil {
			return err
		}
		var sequence int
		if err := conn.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(sequence) + 1, 0) FROM session_messages WHERE session_key = ?`,
			key,
		).Scan(&sequence); err != nil {
			return err
		}
		if err := insertMessageConn(ctx, conn, key, sequence, message); err != nil {
			return err
		}
		return bumpSessionVersionConn(ctx, conn, key, version, now)
	})
}

func (s *SQLiteStore) GetHistory(ctx context.Context, sessionKey string) ([]providers.Message, error) {
	ctx = contextOrBackground(ctx)
	tx, err := s.beginRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	key, found, err := resolveSessionKeyConn(ctx, tx, sessionKey, false)
	if err != nil {
		return nil, err
	}
	if !found {
		return []providers.Message{}, nil
	}
	history, err := readMessagesConn(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	return history, tx.Commit()
}

func (s *SQLiteStore) GetSummary(ctx context.Context, sessionKey string) (string, error) {
	ctx = contextOrBackground(ctx)
	tx, err := s.beginRead(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	key, found, err := resolveSessionKeyConn(ctx, tx, sessionKey, false)
	if err != nil || !found {
		return "", err
	}
	var summary string
	err = tx.QueryRowContext(ctx, `SELECT summary FROM sessions WHERE session_key = ?`, key).Scan(&summary)
	if err != nil {
		return "", err
	}
	return summary, tx.Commit()
}

func (s *SQLiteStore) SetSummary(ctx context.Context, sessionKey, summary string) error {
	ctx = contextOrBackground(ctx)
	return s.Immediate(ctx, func(ctx context.Context, conn *sql.Conn) error {
		key, found, err := resolveSessionKeyConn(ctx, conn, sessionKey, false)
		if err != nil {
			return err
		}
		if !found {
			key = strings.TrimSpace(sessionKey)
		}
		now := time.Now().UTC()
		if err := ensureSessionConn(ctx, conn, key, now); err != nil {
			return err
		}
		version, err := sessionVersionConn(ctx, conn, key)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(
			ctx,
			`UPDATE sessions SET summary = ? WHERE session_key = ?`,
			summary,
			key,
		); err != nil {
			return err
		}
		return bumpSessionVersionConn(ctx, conn, key, version, now)
	})
}

func (s *SQLiteStore) SetHistory(
	ctx context.Context,
	sessionKey string,
	history []providers.Message,
) error {
	ctx = contextOrBackground(ctx)
	history = messageutil.FilterInvalidHistoryMessages(history)
	return s.Immediate(ctx, func(ctx context.Context, conn *sql.Conn) error {
		key, found, err := resolveSessionKeyConn(ctx, conn, sessionKey, false)
		if err != nil {
			return err
		}
		if !found {
			key = strings.TrimSpace(sessionKey)
		}
		now := time.Now().UTC()
		if err := ensureSessionConn(ctx, conn, key, now); err != nil {
			return err
		}
		version, err := sessionVersionConn(ctx, conn, key)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM session_messages WHERE session_key = ?`, key); err != nil {
			return err
		}
		for index := range history {
			if history[index].CreatedAt == nil || history[index].CreatedAt.IsZero() {
				created := now
				history[index].CreatedAt = &created
			}
			if err := insertMessageConn(ctx, conn, key, index, history[index]); err != nil {
				return err
			}
		}
		return bumpSessionVersionConn(ctx, conn, key, version, now)
	})
}

func (s *SQLiteStore) TruncateHistory(ctx context.Context, sessionKey string, keepLast int) error {
	ctx = contextOrBackground(ctx)
	return s.Immediate(ctx, func(ctx context.Context, conn *sql.Conn) error {
		key, found, err := resolveSessionKeyConn(ctx, conn, sessionKey, false)
		if err != nil || !found {
			return err
		}
		version, err := sessionVersionConn(ctx, conn, key)
		if err != nil {
			return err
		}
		if keepLast <= 0 {
			_, err = conn.ExecContext(ctx, `DELETE FROM session_messages WHERE session_key = ?`, key)
		} else {
			_, err = conn.ExecContext(ctx, `DELETE FROM session_messages
                WHERE session_key = ? AND sequence < (
                    SELECT COALESCE(MAX(sequence) - ? + 1, 0)
                    FROM session_messages WHERE session_key = ?
                )`, key, keepLast, key)
		}
		if err != nil {
			return err
		}
		rows, err := conn.QueryContext(ctx,
			`SELECT sequence FROM session_messages WHERE session_key = ? ORDER BY sequence`, key)
		if err != nil {
			return err
		}
		sequences := make([]int, 0)
		for rows.Next() {
			var sequence int
			if err := rows.Scan(&sequence); err != nil {
				_ = rows.Close()
				return err
			}
			sequences = append(sequences, sequence)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for newSequence, oldSequence := range sequences {
			if _, err := conn.ExecContext(ctx, `UPDATE session_messages SET sequence = ?
                WHERE session_key = ? AND sequence = ?`, 1_000_000_000+newSequence, key, oldSequence); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, `UPDATE session_messages SET sequence = sequence - 1000000000
			WHERE session_key = ? AND sequence >= 1000000000`, key); err != nil {
			return err
		}
		return bumpSessionVersionConn(ctx, conn, key, version, time.Now().UTC())
	})
}

// Compact is a no-op because SQLite has no logically skipped message prefix.
func (s *SQLiteStore) Compact(ctx context.Context, _ string) error {
	return contextOrBackground(ctx).Err()
}

func (s *SQLiteStore) ListSessions() []string {
	if s == nil || s.db == nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT session_key FROM sessions ORDER BY session_key`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return keys
}

func (s *SQLiteStore) ResolveSessionKey(
	ctx context.Context,
	sessionKey string,
) (string, bool, error) {
	ctx = contextOrBackground(ctx)
	tx, err := s.beginRead(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	key, found, err := resolveSessionKeyConn(ctx, tx, sessionKey, false)
	if err != nil {
		return "", false, err
	}
	return key, found, tx.Commit()
}

func readScopeConn(
	ctx context.Context,
	queryer interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
		QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	},
	key string,
) (json.RawMessage, error) {
	var scope sqliteSessionScope
	err := queryer.QueryRowContext(ctx, `SELECT scope_version, agent_id, channel, account
        FROM session_scopes WHERE session_key = ?`, key).Scan(
		&scope.Version, &scope.AgentID, &scope.Channel, &scope.Account,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := queryer.QueryContext(ctx, `SELECT dimension, value, is_dimension
        FROM session_scope_dimensions WHERE session_key = ? ORDER BY sequence`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var dimension, value string
		var isDimension int
		if err := rows.Scan(&dimension, &value, &isDimension); err != nil {
			return nil, err
		}
		if isDimension == 1 {
			scope.Dimensions = append(scope.Dimensions, dimension)
		}
		if scope.Values == nil {
			scope.Values = make(map[string]string)
		}
		scope.Values[dimension] = value
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(scope)
	return data, err
}

func readAliasesConn(
	ctx context.Context,
	queryer interface {
		QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	},
	key string,
) ([]string, error) {
	rows, err := queryer.QueryContext(ctx,
		`SELECT alias FROM session_aliases WHERE session_key = ? ORDER BY sequence`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	aliases := make([]string, 0)
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return nil, err
		}
		aliases = append(aliases, alias)
	}
	return aliases, rows.Err()
}

func readSessionMetaConn(
	ctx context.Context,
	queryer interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
		QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	},
	key string,
) (SessionMeta, bool, error) {
	var meta SessionMeta
	var createdSeconds, createdNanos, updatedSeconds, updatedNanos sql.NullInt64
	var version int64
	err := queryer.QueryRowContext(ctx, `SELECT session_key, summary,
        created_seconds, created_nanos, updated_seconds, updated_nanos, version
        FROM sessions WHERE session_key = ?`, key).Scan(
		&meta.Key, &meta.Summary, &createdSeconds, &createdNanos,
		&updatedSeconds, &updatedNanos, &version,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionMeta{Key: key}, false, nil
	}
	if err != nil {
		return SessionMeta{}, false, err
	}
	meta.CreatedAt = timeFromSQLite(createdSeconds, createdNanos)
	meta.UpdatedAt = timeFromSQLite(updatedSeconds, updatedNanos)
	meta.Scope, err = readScopeConn(ctx, queryer, key)
	if err != nil {
		return SessionMeta{}, false, err
	}
	meta.Aliases, err = readAliasesConn(ctx, queryer, key)
	if err != nil {
		return SessionMeta{}, false, err
	}
	if err := queryer.QueryRowContext(ctx, `SELECT l.thread_id, l.attached_seconds,
        l.attached_nanos, t.thread_type, t.title, t.source_query
        FROM session_thread_links l JOIN threads t ON t.thread_id = l.thread_id
        WHERE l.session_key = ?`, key).Scan(
		&meta.ThreadID, &createdSeconds, &createdNanos,
		&meta.ThreadType, &meta.ThreadTitle, &meta.ThreadSourceQuery,
	); err == nil {
		meta.ThreadAttachedAt = timeFromSQLite(createdSeconds, createdNanos)
		rows, queryErr := queryer.QueryContext(ctx,
			`SELECT key, value FROM thread_context WHERE thread_id = ? ORDER BY key`, meta.ThreadID)
		if queryErr != nil {
			return SessionMeta{}, false, queryErr
		}
		for rows.Next() {
			var contextKey, value string
			if scanErr := rows.Scan(&contextKey, &value); scanErr != nil {
				_ = rows.Close()
				return SessionMeta{}, false, scanErr
			}
			if meta.ThreadContext == nil {
				meta.ThreadContext = make(map[string]string)
			}
			meta.ThreadContext[contextKey] = value
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			_ = rows.Close()
			return SessionMeta{}, false, rowsErr
		}
		if closeErr := rows.Close(); closeErr != nil {
			return SessionMeta{}, false, closeErr
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return SessionMeta{}, false, err
	}
	var count int
	if err := queryer.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_messages WHERE session_key = ?`, key).Scan(&count); err != nil {
		return SessionMeta{}, false, err
	}
	meta.Count = count
	return meta, true, nil
}

func (s *SQLiteStore) GetSessionMeta(ctx context.Context, sessionKey string) (SessionMeta, error) {
	ctx = contextOrBackground(ctx)
	tx, err := s.beginRead(ctx)
	if err != nil {
		return SessionMeta{}, err
	}
	defer tx.Rollback()
	key, found, err := resolveSessionKeyConn(ctx, tx, sessionKey, false)
	if err != nil {
		return SessionMeta{}, err
	}
	if !found {
		return SessionMeta{Key: strings.TrimSpace(sessionKey)}, nil
	}
	meta, _, err := readSessionMetaConn(ctx, tx, key)
	if err != nil {
		return SessionMeta{}, err
	}
	return meta, tx.Commit()
}

func writeScopeConn(ctx context.Context, conn *sql.Conn, key string, raw json.RawMessage) error {
	if _, err := conn.ExecContext(ctx, `DELETE FROM session_scopes WHERE session_key = ?`, key); err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	canonical, err := canonicalSessionScopeJSON(raw)
	if err != nil {
		return err
	}
	var scope sqliteSessionScope
	if err := json.Unmarshal(canonical, &scope); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO session_scopes (
        session_key, scope_version, agent_id, channel, account
    ) VALUES (?, ?, ?, ?, ?)`, key, scope.Version, scope.AgentID, scope.Channel, scope.Account); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(scope.Values))
	sequence := 0
	for index, dimension := range scope.Dimensions {
		value, ok := scope.Values[dimension]
		if !ok || strings.TrimSpace(dimension) == "" || strings.TrimSpace(value) == "" {
			return errors.New("memory: session scope dimension is invalid")
		}
		if _, duplicate := seen[dimension]; duplicate {
			return errors.New("memory: session scope dimension is duplicated")
		}
		seen[dimension] = struct{}{}
		if _, err := conn.ExecContext(ctx, `INSERT INTO session_scope_dimensions (
            session_key, sequence, dimension, value, is_dimension
        ) VALUES (?, ?, ?, ?, 1)`, key, index, dimension, value); err != nil {
			return err
		}
		sequence++
	}
	extra := make([]string, 0, len(scope.Values)-len(scope.Dimensions))
	for dimension, value := range scope.Values {
		if _, exists := seen[dimension]; exists {
			continue
		}
		if strings.TrimSpace(dimension) == "" || strings.TrimSpace(value) == "" {
			return errors.New("memory: session scope value is invalid")
		}
		extra = append(extra, dimension)
	}
	sort.Strings(extra)
	for _, dimension := range extra {
		if _, err := conn.ExecContext(ctx, `INSERT INTO session_scope_dimensions (
            session_key, sequence, dimension, value, is_dimension
        ) VALUES (?, ?, ?, ?, 0)`, key, sequence, dimension, scope.Values[dimension]); err != nil {
			return err
		}
		sequence++
	}
	return nil
}

func validateAliasWriteConn(
	ctx context.Context,
	conn *sql.Conn,
	key string,
	aliases []string,
	exclusive bool,
) error {
	for _, alias := range aliases {
		var direct int
		if err := conn.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sessions WHERE session_key = ? AND session_key <> ?`, alias, key,
		).Scan(&direct); err != nil {
			return err
		}
		if direct > 0 && exclusive {
			return fmt.Errorf("memory: session alias %q already has session data", alias)
		}
		var owners int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(DISTINCT session_key)
            FROM session_aliases WHERE alias = ? AND session_key <> ?`, alias, key).Scan(&owners); err != nil {
			return err
		}
		if owners > 0 && exclusive {
			return fmt.Errorf("memory: session alias %q already belongs to another session", alias)
		}
	}
	return nil
}

func writeAliasesConn(
	ctx context.Context,
	conn *sql.Conn,
	key string,
	aliases []string,
	exclusive bool,
) error {
	normalized := normalizeAliases(key, aliases)
	if len(normalized) != len(aliases) || !slices.Equal(normalized, aliases) {
		return errors.New("memory: session aliases are not canonical")
	}
	if err := validateAliasWriteConn(ctx, conn, key, normalized, exclusive); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM session_aliases WHERE session_key = ?`, key); err != nil {
		return err
	}
	for index, alias := range normalized {
		if _, err := conn.ExecContext(ctx, `INSERT INTO session_aliases (
            session_key, sequence, alias
        ) VALUES (?, ?, ?)`, key, index, alias); err != nil {
			return err
		}
	}
	return nil
}

func writeSessionMetaConn(
	ctx context.Context,
	conn *sql.Conn,
	key string,
	meta SessionMeta,
	exclusiveAliases bool,
) error {
	if meta.Key != "" && meta.Key != key {
		return fmt.Errorf("memory: session metadata key %q does not match canonical key %q", meta.Key, key)
	}
	createdSeconds, createdNanos := sqliteTimeParts(meta.CreatedAt)
	updatedSeconds, updatedNanos := sqliteTimeParts(meta.UpdatedAt)
	if _, err := conn.ExecContext(ctx, `UPDATE sessions SET summary = ?,
        created_seconds = COALESCE(?, created_seconds), created_nanos = COALESCE(?, created_nanos),
        updated_seconds = ?, updated_nanos = ? WHERE session_key = ?`,
		meta.Summary, createdSeconds, createdNanos, updatedSeconds, updatedNanos, key,
	); err != nil {
		return err
	}
	if err := writeScopeConn(ctx, conn, key, meta.Scope); err != nil {
		return err
	}
	return writeAliasesConn(ctx, conn, key, meta.Aliases, exclusiveAliases)
}

func (s *SQLiteStore) UpsertSessionMeta(
	ctx context.Context,
	sessionKey string,
	scope json.RawMessage,
	aliases []string,
) error {
	ctx = contextOrBackground(ctx)
	aliases = normalizeAliases(sessionKey, aliases)
	return s.Immediate(ctx, func(ctx context.Context, conn *sql.Conn) error {
		now := time.Now().UTC()
		if err := ensureSessionConn(ctx, conn, sessionKey, now); err != nil {
			return err
		}
		version, err := sessionVersionConn(ctx, conn, sessionKey)
		if err != nil {
			return err
		}
		meta, _, err := readSessionMetaConn(ctx, conn, sessionKey)
		if err != nil {
			return err
		}
		meta.Scope = append(json.RawMessage(nil), scope...)
		meta.Aliases = append([]string(nil), aliases...)
		meta.UpdatedAt = now
		if err := writeSessionMetaConn(ctx, conn, sessionKey, meta, false); err != nil {
			return err
		}
		return bumpSessionVersionConn(ctx, conn, sessionKey, version, now)
	})
}

func (s *SQLiteStore) ReadSessionState(
	ctx context.Context,
	sessionKey string,
) ([]providers.Message, SessionMeta, time.Time, error) {
	ctx = contextOrBackground(ctx)
	tx, err := s.beginRead(ctx)
	if err != nil {
		return nil, SessionMeta{}, time.Time{}, err
	}
	defer tx.Rollback()
	key, found, err := resolveSessionKeyConn(ctx, tx, sessionKey, false)
	if err != nil {
		return nil, SessionMeta{}, time.Time{}, err
	}
	if !found {
		return []providers.Message{}, SessionMeta{Key: strings.TrimSpace(sessionKey)}, time.Time{}, nil
	}
	meta, _, err := readSessionMetaConn(ctx, tx, key)
	if err != nil {
		return nil, SessionMeta{}, time.Time{}, err
	}
	history, err := readMessagesConn(ctx, tx, key)
	if err != nil {
		return nil, SessionMeta{}, time.Time{}, err
	}
	return history, meta, meta.UpdatedAt, tx.Commit()
}

func (s *SQLiteStore) ReadSessionStateStrict(
	ctx context.Context,
	sessionKey string,
) (string, []providers.Message, SessionMeta, time.Time, bool, error) {
	ctx = contextOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return "", nil, SessionMeta{}, time.Time{}, false, err
	}
	tx, err := s.beginRead(ctx)
	if err != nil {
		return "", nil, SessionMeta{}, time.Time{}, false, err
	}
	defer tx.Rollback()
	key, found, err := resolveSessionKeyConn(ctx, tx, sessionKey, true)
	if err != nil || !found {
		return key, nil, SessionMeta{}, time.Time{}, found, err
	}
	meta, _, err := readSessionMetaConn(ctx, tx, key)
	if err != nil {
		return "", nil, SessionMeta{}, time.Time{}, false, err
	}
	history, err := readMessagesConn(ctx, tx, key)
	if err != nil {
		return "", nil, SessionMeta{}, time.Time{}, false, err
	}
	meta.Revision, err = snapshotRevision(key, history, meta)
	if err != nil {
		return "", nil, SessionMeta{}, time.Time{}, false, err
	}
	return key, history, meta, meta.UpdatedAt, true, tx.Commit()
}

func (s *SQLiteStore) ReadSessionSnapshot(
	ctx context.Context,
	sessionKey string,
) (string, []providers.Message, SessionMeta, bool, error) {
	key, history, meta, _, found, err := s.ReadSessionStateStrict(ctx, sessionKey)
	return key, history, meta, found, err
}

func (s *SQLiteStore) ReplaceSessionSnapshot(
	ctx context.Context,
	replacement SessionSnapshotReplacement,
) error {
	ctx = contextOrBackground(ctx)
	if err := validateSnapshotReplacement(replacement); err != nil {
		return err
	}
	replacement.History = messageutil.FilterInvalidHistoryMessages(replacement.History)
	return s.Immediate(ctx, func(ctx context.Context, conn *sql.Conn) error {
		meta, exists, err := readSessionMetaConn(ctx, conn, replacement.Key)
		if err != nil {
			return err
		}
		var version int64
		if exists {
			if replacement.ExpectedRevision == "" {
				return ErrSnapshotConflict
			}
			history, err := readMessagesConn(ctx, conn, replacement.Key)
			if err != nil {
				return err
			}
			currentRevision, err := snapshotRevision(replacement.Key, history, meta)
			if err != nil {
				return err
			}
			if currentRevision != replacement.ExpectedRevision {
				return ErrSnapshotConflict
			}
			version, err = sessionVersionConn(ctx, conn, replacement.Key)
			if err != nil {
				return err
			}
		} else {
			if replacement.ExpectedRevision != "" {
				return ErrSnapshotConflict
			}
			now := time.Now().UTC()
			if err := ensureSessionConn(ctx, conn, replacement.Key, now); err != nil {
				return err
			}
			version, err = sessionVersionConn(ctx, conn, replacement.Key)
			if err != nil {
				return err
			}
			meta = SessionMeta{Key: replacement.Key, CreatedAt: now}
		}
		now := time.Now().UTC()
		meta.Summary = replacement.Summary
		meta.Scope = append(json.RawMessage(nil), replacement.Scope...)
		meta.Aliases = append([]string(nil), replacement.Aliases...)
		meta.UpdatedAt = now
		if err := writeSessionMetaConn(ctx, conn, replacement.Key, meta, true); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx,
			`DELETE FROM session_messages WHERE session_key = ?`, replacement.Key); err != nil {
			return err
		}
		for index := range replacement.History {
			if err := insertMessageConn(ctx, conn, replacement.Key, index, replacement.History[index]); err != nil {
				return err
			}
		}
		return bumpSessionVersionConn(ctx, conn, replacement.Key, version, now)
	})
}

func (s *SQLiteStore) AdmitSessionMeta(
	ctx context.Context,
	sessionKey string,
	admit SessionMetaAdmission,
) (bool, error) {
	ctx = contextOrBackground(ctx)
	if strings.TrimSpace(sessionKey) == "" || sessionKey != strings.TrimSpace(sessionKey) || admit == nil {
		return false, errors.New("memory: session metadata admission is invalid")
	}
	updated := false
	err := s.Immediate(ctx, func(ctx context.Context, conn *sql.Conn) error {
		key, found, err := resolveSessionKeyConn(ctx, conn, sessionKey, true)
		if err != nil {
			return err
		}
		if !found {
			key = sessionKey
		}
		meta, exists, err := readSessionMetaConn(ctx, conn, key)
		if err != nil {
			return err
		}
		decision, err := admit(cloneSessionMeta(meta), exists)
		if err != nil || !decision.Update {
			return err
		}
		if _, err := canonicalSessionScopeJSON(decision.Scope); err != nil {
			return err
		}
		aliases := append([]string(nil), decision.Aliases...)
		if decision.PreserveRequestedAlias && sessionKey != key && !slices.Contains(aliases, sessionKey) {
			aliases = append(aliases, sessionKey)
		}
		aliases = normalizeAliases(key, aliases)
		now := time.Now().UTC()
		if !exists {
			if err := ensureSessionConn(ctx, conn, key, now); err != nil {
				return err
			}
			meta = SessionMeta{Key: key, CreatedAt: now}
		}
		version, err := sessionVersionConn(ctx, conn, key)
		if err != nil {
			return err
		}
		if decision.PromoteAliasHistory {
			for _, alias := range aliases {
				if isMainSessionAlias(alias) {
					continue
				}
				if promoted, err := promoteAliasConn(ctx, conn, key, alias); err != nil {
					return err
				} else if promoted {
					meta, _, err = readSessionMetaConn(ctx, conn, key)
					if err != nil {
						return err
					}
					break
				}
			}
		}
		meta.Scope = append(json.RawMessage(nil), decision.Scope...)
		meta.Aliases = aliases
		meta.UpdatedAt = now
		if err := writeSessionMetaConn(ctx, conn, key, meta, decision.ExclusiveAliases); err != nil {
			return err
		}
		if err := bumpSessionVersionConn(ctx, conn, key, version, now); err != nil {
			return err
		}
		updated = true
		return nil
	})
	return updated, err
}

func promoteAliasConn(ctx context.Context, conn *sql.Conn, key, alias string) (bool, error) {
	var canonicalCount int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_messages WHERE session_key = ?`, key).Scan(&canonicalCount); err != nil {
		return false, err
	}
	var canonicalSummary string
	if err := conn.QueryRowContext(ctx,
		`SELECT summary FROM sessions WHERE session_key = ?`, key).Scan(&canonicalSummary); err != nil {
		return false, err
	}
	if canonicalCount > 0 || strings.TrimSpace(canonicalSummary) != "" {
		return false, nil
	}
	var aliasSummary string
	err := conn.QueryRowContext(ctx, `SELECT summary FROM sessions WHERE session_key = ?`, alias).Scan(&aliasSummary)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	history, err := readMessagesConn(ctx, conn, alias)
	if err != nil || len(history) == 0 && strings.TrimSpace(aliasSummary) == "" {
		return false, err
	}
	if _, err := conn.ExecContext(
		ctx,
		`UPDATE sessions SET summary = ? WHERE session_key = ?`,
		aliasSummary,
		key,
	); err != nil {
		return false, err
	}
	for index := range history {
		if err := insertMessageConn(ctx, conn, key, index, history[index]); err != nil {
			return false, err
		}
	}
	if err := rebindPromotedSessionRelationshipsConn(ctx, conn, alias, key); err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM sessions WHERE session_key = ?`, alias); err != nil {
		return false, err
	}
	return true, nil
}

type promotedSessionThreadLink struct {
	sessionKey      string
	attachedSeconds int64
	attachedNanos   int64
}

func rebindPromotedSessionRelationshipsConn(
	ctx context.Context,
	conn *sql.Conn,
	from,
	to string,
) error {
	rows, err := conn.QueryContext(ctx, `SELECT thread_id FROM thread_sessions
        WHERE session_key = ? UNION SELECT thread_id FROM threads
        WHERE primary_session_key = ? ORDER BY thread_id`, from, from)
	if err != nil {
		return err
	}
	threadIDs := make([]string, 0)
	for rows.Next() {
		var threadID string
		if err := rows.Scan(&threadID); err != nil {
			_ = rows.Close()
			return err
		}
		threadIDs = append(threadIDs, threadID)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		_ = rows.Close()
		return rowsErr
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := time.Now().UTC()
	seconds, nanos := sqliteRequiredTimeParts(now)
	for _, threadID := range threadIDs {
		var primary string
		var version int64
		if err := conn.QueryRowContext(ctx, `SELECT primary_session_key, version
            FROM threads WHERE thread_id = ?`, threadID).Scan(&primary, &version); err != nil {
			return err
		}
		membershipRows, err := conn.QueryContext(ctx, `SELECT session_key
            FROM thread_sessions WHERE thread_id = ? ORDER BY sequence`, threadID)
		if err != nil {
			return err
		}
		members := make([]string, 0)
		seen := make(map[string]struct{})
		for membershipRows.Next() {
			var member string
			if err := membershipRows.Scan(&member); err != nil {
				_ = membershipRows.Close()
				return err
			}
			if member == from {
				member = to
			}
			if _, duplicate := seen[member]; duplicate {
				continue
			}
			seen[member] = struct{}{}
			members = append(members, member)
		}
		if rowsErr := membershipRows.Err(); rowsErr != nil {
			_ = membershipRows.Close()
			return rowsErr
		}
		if err := membershipRows.Close(); err != nil {
			return err
		}
		linkRows, err := conn.QueryContext(ctx, `SELECT session_key,
            attached_seconds, attached_nanos FROM session_thread_links
            WHERE thread_id = ? ORDER BY session_key`, threadID)
		if err != nil {
			return err
		}
		links := make([]promotedSessionThreadLink, 0)
		for linkRows.Next() {
			var link promotedSessionThreadLink
			if err := linkRows.Scan(
				&link.sessionKey, &link.attachedSeconds, &link.attachedNanos,
			); err != nil {
				_ = linkRows.Close()
				return err
			}
			if link.sessionKey == from {
				link.sessionKey = to
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
		if primary == from {
			primary = to
			result, err := conn.ExecContext(ctx, `UPDATE threads SET
                primary_session_key = ?, updated_seconds = ?, updated_nanos = ?,
                version = version + 1 WHERE thread_id = ? AND version = ?`,
				primary, seconds, nanos, threadID, version)
			if err != nil {
				return err
			}
			if changed, err := result.RowsAffected(); err != nil || changed != 1 {
				if err != nil {
					return err
				}
				return ErrSnapshotConflict
			}
		}
		if _, exists := seen[primary]; !exists {
			return errors.New("memory: promoted thread primary membership is missing")
		}
		if _, err := conn.ExecContext(ctx,
			`DELETE FROM thread_sessions WHERE thread_id = ?`, threadID); err != nil {
			return err
		}
		for sequence, member := range members {
			isPrimary := 0
			if member == primary {
				isPrimary = 1
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO thread_sessions (
                thread_id, sequence, session_key, is_primary
            ) VALUES (?, ?, ?, ?)`, threadID, sequence, member, isPrimary); err != nil {
				return err
			}
		}
		for _, link := range links {
			if _, err := conn.ExecContext(ctx, `INSERT INTO session_thread_links (
                session_key, thread_id, attached_seconds, attached_nanos
            ) VALUES (?, ?, ?, ?) ON CONFLICT(session_key) DO NOTHING`,
				link.sessionKey, threadID, link.attachedSeconds, link.attachedNanos); err != nil {
				return err
			}
		}
	}
	_, err = conn.ExecContext(ctx, `UPDATE thread_handoffs
        SET origin_session_key = ?, version = version + 1
        WHERE origin_session_key = ?`, to, from)
	return err
}

func (s *SQLiteStore) PromoteAliasHistory(
	ctx context.Context,
	sessionKey string,
	scope json.RawMessage,
	aliases []string,
) (bool, error) {
	ctx = contextOrBackground(ctx)
	promoted := false
	err := s.Immediate(ctx, func(ctx context.Context, conn *sql.Conn) error {
		now := time.Now().UTC()
		if err := ensureSessionConn(ctx, conn, sessionKey, now); err != nil {
			return err
		}
		version, err := sessionVersionConn(ctx, conn, sessionKey)
		if err != nil {
			return err
		}
		for _, alias := range normalizeAliases(sessionKey, aliases) {
			if isMainSessionAlias(alias) {
				continue
			}
			moved, err := promoteAliasConn(ctx, conn, sessionKey, alias)
			if err != nil {
				return err
			}
			if moved {
				promoted = true
				break
			}
		}
		if !promoted {
			return nil
		}
		meta, _, err := readSessionMetaConn(ctx, conn, sessionKey)
		if err != nil {
			return err
		}
		meta.Scope = append(json.RawMessage(nil), scope...)
		meta.Aliases = normalizeAliases(sessionKey, aliases)
		meta.UpdatedAt = now
		if err := writeSessionMetaConn(ctx, conn, sessionKey, meta, false); err != nil {
			return err
		}
		return bumpSessionVersionConn(ctx, conn, sessionKey, version, now)
	})
	return promoted, err
}

func (s *SQLiteStore) UpdateSessionMeta(
	ctx context.Context,
	sessionKey string,
	update func(*SessionMeta) error,
) error {
	_, _, err := s.UpdateSessionMetaStrict(ctx, sessionKey,
		func(meta *SessionMeta, _ SessionMetaMutationState) error { return update(meta) })
	return err
}

func (s *SQLiteStore) UpdateSessionMetaStrict(
	ctx context.Context,
	sessionKey string,
	update func(*SessionMeta, SessionMetaMutationState) error,
) (canonicalKey string, existed bool, err error) {
	ctx = contextOrBackground(ctx)
	if strings.TrimSpace(sessionKey) == "" || sessionKey != strings.TrimSpace(sessionKey) || update == nil {
		return "", false, errors.New("memory: strict session metadata update is invalid")
	}
	err = s.Immediate(ctx, func(ctx context.Context, conn *sql.Conn) error {
		key, found, err := resolveSessionKeyConn(ctx, conn, sessionKey, true)
		if err != nil {
			return err
		}
		if !found {
			key = sessionKey
		}
		canonicalKey = key
		meta, found, err := readSessionMetaConn(ctx, conn, key)
		if err != nil {
			return err
		}
		existed = found
		before := cloneSessionMeta(meta)
		if err := update(&meta, SessionMetaMutationState{
			SessionExists: found, MetadataExists: found,
		}); err != nil {
			return err
		}
		if before.Key != "" && meta.Key != before.Key {
			return errors.New("memory: strict metadata update changed the canonical key")
		}
		if meta.Skip != before.Skip || meta.Count != before.Count || meta.HistorySlot != before.HistorySlot {
			return errors.New("memory: strict metadata update changed history-owned fields")
		}
		now := time.Now().UTC()
		if !found {
			if err := ensureSessionConn(ctx, conn, key, now); err != nil {
				return err
			}
			meta.Key = key
			meta.CreatedAt = now
		}
		version, err := sessionVersionConn(ctx, conn, key)
		if err != nil {
			return err
		}
		if err := writeSessionMetaConn(ctx, conn, key, meta, false); err != nil {
			return err
		}
		return bumpSessionVersionConn(ctx, conn, key, version, now)
	})
	return canonicalKey, existed, err
}

func (s *SQLiteStore) CompareAndSwapSessionMetaStrict(
	ctx context.Context,
	sessionKey string,
	expected SessionMeta,
	replacement *SessionMeta,
) (bool, error) {
	ctx = contextOrBackground(ctx)
	changed := false
	err := s.Immediate(ctx, func(ctx context.Context, conn *sql.Conn) error {
		key, found, err := resolveSessionKeyConn(ctx, conn, sessionKey, true)
		if err != nil || !found {
			return err
		}
		current, _, err := readSessionMetaConn(ctx, conn, key)
		if err != nil {
			return err
		}
		equal, err := persistedSessionMetaEqual(current, expected)
		if err != nil || !equal {
			return err
		}
		if replacement == nil {
			var count int
			if err := conn.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM session_messages WHERE session_key = ?`, key).Scan(&count); err != nil {
				return err
			}
			if count != 0 || current.Summary != "" || len(current.Scope) != 0 || len(current.Aliases) != 0 {
				return errors.New("memory: cannot remove nonempty session metadata")
			}
			_, err = conn.ExecContext(ctx, `DELETE FROM sessions WHERE session_key = ?`, key)
		} else {
			restored := cloneSessionMeta(*replacement)
			if restored.Key == "" {
				restored.Key = key
			}
			err = writeSessionMetaConn(ctx, conn, key, restored, false)
		}
		changed = err == nil
		return err
	})
	return changed, err
}

func (s *SQLiteStore) CompareAndDeleteEmptySessionStrict(
	ctx context.Context,
	sessionKey string,
	expected SessionMeta,
) (bool, error) {
	ctx = contextOrBackground(ctx)
	deleted := false
	err := s.Immediate(ctx, func(ctx context.Context, conn *sql.Conn) error {
		key, found, err := resolveSessionKeyConn(ctx, conn, sessionKey, true)
		if err != nil || !found {
			return err
		}
		current, _, err := readSessionMetaConn(ctx, conn, key)
		if err != nil {
			return err
		}
		equal, err := persistedSessionMetaEqual(current, expected)
		if err != nil || !equal {
			return err
		}
		if current.Count != 0 || current.Summary != "" {
			return errors.New("memory: compared session history is not empty")
		}
		result, err := conn.ExecContext(ctx, `DELETE FROM sessions WHERE session_key = ?`, key)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		deleted = count == 1
		return err
	})
	return deleted, err
}

func (s *SQLiteStore) EnsureSessionHistory(ctx context.Context, sessionKey string) error {
	ctx = contextOrBackground(ctx)
	return s.Immediate(ctx, func(ctx context.Context, conn *sql.Conn) error {
		return ensureSessionConn(ctx, conn, strings.TrimSpace(sessionKey), time.Now().UTC())
	})
}

func (s *SQLiteStore) DeleteSession(ctx context.Context, sessionKey string) (bool, error) {
	return s.DeleteSessions(ctx, []string{sessionKey})
}

func normalizeDeleteKeys(keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, errors.New("memory: session deletion keys are empty")
	}
	result := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, errors.New("memory: session key is empty")
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	sort.Strings(result)
	return result, nil
}

func (s *SQLiteStore) DeleteSessions(ctx context.Context, sessionKeys []string) (bool, error) {
	ctx = contextOrBackground(ctx)
	keys, err := normalizeDeleteKeys(sessionKeys)
	if err != nil {
		return false, err
	}
	deleted := false
	err = s.Immediate(ctx, func(ctx context.Context, conn *sql.Conn) error {
		canonical := make([]string, 0, len(keys))
		seen := make(map[string]struct{})
		for _, key := range keys {
			resolved, found, err := resolveSessionKeyConn(ctx, conn, key, true)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			if _, ok := seen[resolved]; ok {
				continue
			}
			seen[resolved] = struct{}{}
			canonical = append(canonical, resolved)
		}
		for _, key := range canonical {
			result, err := conn.ExecContext(ctx, `DELETE FROM sessions WHERE session_key = ?`, key)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			deleted = deleted || count == 1
		}
		return nil
	})
	return deleted, err
}

func (s *SQLiteStore) DeleteSessionsWithAliasesMatching(
	ctx context.Context,
	sessionKeys []string,
	matchSession func(SessionMeta, bool) bool,
	matchAlias func(SessionMeta, string) bool,
) (bool, error) {
	ctx = contextOrBackground(ctx)
	keys, err := normalizeDeleteKeys(sessionKeys)
	if err != nil {
		return false, err
	}
	deleted := false
	err = s.Immediate(ctx, func(ctx context.Context, conn *sql.Conn) error {
		deleteSet := make(map[string]struct{})
		for _, requested := range keys {
			key, found, err := resolveSessionKeyConn(ctx, conn, requested, true)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			meta, _, err := readSessionMetaConn(ctx, conn, key)
			if err != nil {
				return err
			}
			if matchSession == nil || matchSession(cloneSessionMeta(meta), true) {
				deleteSet[key] = struct{}{}
			}
			if matchAlias != nil {
				for _, alias := range meta.Aliases {
					if matchAlias(cloneSessionMeta(meta), alias) {
						if shadow, shadowFound, err := resolveSessionKeyConn(ctx, conn, alias, true); err != nil {
							return err
						} else if shadowFound && shadow == alias {
							deleteSet[shadow] = struct{}{}
						}
					}
				}
			}
		}
		for key := range deleteSet {
			result, err := conn.ExecContext(ctx, `DELETE FROM sessions WHERE session_key = ?`, key)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			deleted = deleted || count == 1
		}
		return nil
	})
	return deleted, err
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}
