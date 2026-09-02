package memory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
)

const (
	sessionsDatabaseFilename = "sessions.db"
	sessionsComponent        = "sessions"

	sqlCreateSessions = `CREATE TABLE sessions (
    session_key      TEXT PRIMARY KEY CHECK(length(CAST(session_key AS BLOB)) BETWEEN 1 AND 16384),
    summary          TEXT NOT NULL DEFAULT '' CHECK(length(CAST(summary AS BLOB)) <= 16777216),
    created_seconds  INTEGER,
    created_nanos    INTEGER,
    updated_seconds  INTEGER,
    updated_nanos    INTEGER,
    version          INTEGER NOT NULL DEFAULT 1 CHECK(version >= 1),
    CHECK ((created_seconds IS NULL) = (created_nanos IS NULL)),
    CHECK (created_seconds IS NULL OR created_seconds BETWEEN -62167219200 AND 253402300799),
    CHECK (created_nanos IS NULL OR created_nanos BETWEEN 0 AND 999999999),
    CHECK ((updated_seconds IS NULL) = (updated_nanos IS NULL)),
    CHECK (updated_seconds IS NULL OR updated_seconds BETWEEN -62167219200 AND 253402300799),
    CHECK (updated_nanos IS NULL OR updated_nanos BETWEEN 0 AND 999999999)
) STRICT`

	sqlCreateSessionScopes = `CREATE TABLE session_scopes (
    session_key   TEXT PRIMARY KEY REFERENCES sessions(session_key) ON DELETE CASCADE,
    scope_version INTEGER NOT NULL CHECK(scope_version >= 0),
    agent_id      TEXT NOT NULL CHECK(length(CAST(agent_id AS BLOB)) <= 16384),
    channel       TEXT NOT NULL CHECK(length(CAST(channel AS BLOB)) <= 16384),
    account       TEXT NOT NULL CHECK(length(CAST(account AS BLOB)) <= 16384)
) STRICT`

	sqlCreateSessionScopeDimensions = `CREATE TABLE session_scope_dimensions (
    session_key TEXT NOT NULL REFERENCES session_scopes(session_key) ON DELETE CASCADE,
    sequence    INTEGER NOT NULL CHECK(sequence >= 0),
    dimension   TEXT NOT NULL CHECK(length(CAST(dimension AS BLOB)) BETWEEN 1 AND 256),
    value       TEXT NOT NULL CHECK(length(CAST(value AS BLOB)) BETWEEN 1 AND 16384),
    is_dimension INTEGER NOT NULL CHECK(is_dimension IN (0, 1)),
    PRIMARY KEY(session_key, sequence),
    UNIQUE(session_key, dimension)
) STRICT`

	sqlCreateSessionAliases = `CREATE TABLE session_aliases (
    session_key TEXT NOT NULL REFERENCES sessions(session_key) ON DELETE CASCADE,
    sequence    INTEGER NOT NULL CHECK(sequence >= 0),
    alias       TEXT NOT NULL CHECK(length(CAST(alias AS BLOB)) BETWEEN 1 AND 16384),
    PRIMARY KEY(session_key, sequence),
    UNIQUE(session_key, alias),
    CHECK(alias <> session_key)
) STRICT`

	sqlCreateSessionMessages = `CREATE TABLE session_messages (
    session_key       TEXT NOT NULL REFERENCES sessions(session_key) ON DELETE CASCADE,
    sequence          INTEGER NOT NULL CHECK(sequence >= 0),
    role              TEXT NOT NULL CHECK(length(CAST(role AS BLOB)) <= 1024),
    content           TEXT NOT NULL CHECK(length(CAST(content AS BLOB)) <= 10485759),
    model_name        TEXT NOT NULL DEFAULT '' CHECK(length(CAST(model_name AS BLOB)) <= 4096),
    created_seconds   INTEGER,
    created_nanos     INTEGER,
    reasoning_content TEXT NOT NULL DEFAULT '' CHECK(length(CAST(reasoning_content AS BLOB)) <= 10485759),
    tool_call_id      TEXT NOT NULL DEFAULT '' CHECK(length(CAST(tool_call_id AS BLOB)) <= 4096),
    nested_payload    BLOB,
    PRIMARY KEY(session_key, sequence),
    CHECK ((created_seconds IS NULL) = (created_nanos IS NULL)),
    CHECK (created_seconds IS NULL OR created_seconds BETWEEN -62167219200 AND 253402300799),
    CHECK (created_nanos IS NULL OR created_nanos BETWEEN 0 AND 999999999),
    CHECK (nested_payload IS NULL OR (
        length(nested_payload) <= 10485759 AND json_valid(nested_payload)
    ))
) STRICT`

	sqlCreateThreads = `CREATE TABLE threads (
    thread_id           TEXT PRIMARY KEY CHECK(length(CAST(thread_id AS BLOB)) BETWEEN 1 AND 16384),
    ui_session_id       TEXT NOT NULL CHECK(length(CAST(ui_session_id AS BLOB)) <= 16384),
    primary_session_key TEXT NOT NULL REFERENCES sessions(session_key) ON DELETE CASCADE,
    agent_id            TEXT NOT NULL CHECK(length(CAST(agent_id AS BLOB)) <= 16384),
    owner_identity      TEXT NOT NULL CHECK(length(CAST(owner_identity AS BLOB)) <= 16384),
    title               TEXT NOT NULL CHECK(length(CAST(title AS BLOB)) <= 16384),
    thread_type         TEXT NOT NULL CHECK(thread_type IN (
        'general', 'coding', 'reviewing', 'investigating'
    )),
    source_query        TEXT NOT NULL DEFAULT '' CHECK(length(CAST(source_query AS BLOB)) <= 1048576),
    registration        TEXT NOT NULL CHECK(registration IN (
        'auto', 'tool', 'manual', 'migrated'
    )),
    dropped_seconds     INTEGER,
    dropped_nanos       INTEGER,
    created_seconds     INTEGER NOT NULL CHECK(created_seconds BETWEEN -62167219200 AND 253402300799),
    created_nanos       INTEGER NOT NULL CHECK(created_nanos BETWEEN 0 AND 999999999),
    updated_seconds     INTEGER NOT NULL CHECK(updated_seconds BETWEEN -62167219200 AND 253402300799),
    updated_nanos       INTEGER NOT NULL CHECK(updated_nanos BETWEEN 0 AND 999999999),
    version             INTEGER NOT NULL DEFAULT 1 CHECK(version >= 1),
    CHECK ((dropped_seconds IS NULL) = (dropped_nanos IS NULL)),
    CHECK (dropped_seconds IS NULL OR dropped_seconds BETWEEN -62167219200 AND 253402300799),
    CHECK (dropped_nanos IS NULL OR dropped_nanos BETWEEN 0 AND 999999999),
    FOREIGN KEY(thread_id, primary_session_key)
        REFERENCES thread_sessions(thread_id, session_key)
        DEFERRABLE INITIALLY DEFERRED
) STRICT`

	sqlCreateThreadContext = `CREATE TABLE thread_context (
    thread_id TEXT NOT NULL REFERENCES threads(thread_id) ON DELETE CASCADE,
    key       TEXT NOT NULL CHECK(length(CAST(key AS BLOB)) BETWEEN 1 AND 256),
    value     TEXT NOT NULL CHECK(length(CAST(value AS BLOB)) <= 16384),
    PRIMARY KEY(thread_id, key)
) STRICT`

	sqlCreateThreadAliases = `CREATE TABLE thread_aliases (
    thread_id TEXT NOT NULL REFERENCES threads(thread_id) ON DELETE CASCADE,
    sequence  INTEGER NOT NULL CHECK(sequence >= 0),
    alias     TEXT NOT NULL CHECK(length(CAST(alias AS BLOB)) BETWEEN 1 AND 16384),
    PRIMARY KEY(thread_id, sequence),
    UNIQUE(thread_id, alias)
) STRICT`

	sqlCreateThreadSessions = `CREATE TABLE thread_sessions (
    thread_id   TEXT NOT NULL REFERENCES threads(thread_id) ON DELETE CASCADE,
    sequence    INTEGER NOT NULL CHECK(sequence >= 0),
    session_key TEXT NOT NULL REFERENCES sessions(session_key) ON DELETE CASCADE,
    is_primary  INTEGER NOT NULL CHECK(is_primary IN (0, 1)),
    PRIMARY KEY(thread_id, sequence),
    UNIQUE(thread_id, session_key)
) STRICT`

	sqlCreateSessionThreadLinks = `CREATE TABLE session_thread_links (
    session_key      TEXT PRIMARY KEY REFERENCES sessions(session_key) ON DELETE CASCADE,
    thread_id        TEXT NOT NULL REFERENCES threads(thread_id) ON DELETE CASCADE,
    attached_seconds INTEGER NOT NULL CHECK(attached_seconds BETWEEN -62167219200 AND 253402300799),
    attached_nanos   INTEGER NOT NULL CHECK(attached_nanos BETWEEN 0 AND 999999999),
    FOREIGN KEY(thread_id, session_key)
        REFERENCES thread_sessions(thread_id, session_key)
        ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED
) STRICT`

	sqlCreateThreadHandoffs = `CREATE TABLE thread_handoffs (
    handoff_id         TEXT PRIMARY KEY CHECK(length(CAST(handoff_id AS BLOB)) BETWEEN 1 AND 16384),
    origin_session_key TEXT NOT NULL REFERENCES sessions(session_key) ON DELETE CASCADE,
    origin_session_id  TEXT NOT NULL DEFAULT '' CHECK(length(CAST(origin_session_id AS BLOB)) <= 16384),
    target_thread_id   TEXT NOT NULL REFERENCES threads(thread_id) ON DELETE CASCADE,
    target_session_id  TEXT NOT NULL CHECK(length(CAST(target_session_id AS BLOB)) <= 16384),
    agent_id           TEXT NOT NULL CHECK(length(CAST(agent_id AS BLOB)) <= 16384),
    summary            TEXT NOT NULL DEFAULT '' CHECK(length(CAST(summary AS BLOB)) <= 1048576),
    created_seconds    INTEGER NOT NULL CHECK(created_seconds BETWEEN -62167219200 AND 253402300799),
    created_nanos      INTEGER NOT NULL CHECK(created_nanos BETWEEN 0 AND 999999999),
    version            INTEGER NOT NULL DEFAULT 1 CHECK(version >= 1)
) STRICT`

	sqlCreateSessionAliasIndex = `CREATE INDEX session_alias_lookup_idx
    ON session_aliases(alias, session_key)`
	sqlCreateSessionUpdatedIndex = `CREATE INDEX sessions_updated_idx
    ON sessions(updated_seconds DESC, updated_nanos DESC, session_key)`
	sqlCreateThreadsUpdatedIndex = `CREATE INDEX threads_updated_idx
    ON threads(updated_seconds DESC, updated_nanos DESC, thread_id)`
	sqlCreateThreadSessionsSessionIndex = `CREATE INDEX thread_sessions_session_idx
    ON thread_sessions(session_key, thread_id)`
	sqlCreateThreadSessionsPrimaryIndex = `CREATE UNIQUE INDEX thread_sessions_one_primary_idx
    ON thread_sessions(thread_id) WHERE is_primary = 1`
	sqlCreateSessionThreadLinksThreadIndex = `CREATE INDEX session_thread_links_thread_idx
    ON session_thread_links(thread_id, session_key)`
	sqlCreateThreadHandoffsOriginIndex = `CREATE INDEX thread_handoffs_origin_idx
    ON thread_handoffs(origin_session_key, created_seconds DESC, created_nanos DESC)`
	sqlCreateThreadHandoffsTargetIndex = `CREATE INDEX thread_handoffs_target_idx
    ON thread_handoffs(target_thread_id, created_seconds DESC, created_nanos DESC)`
)

var sessionsSchemaObjects = []struct {
	typeName string
	name     string
	schema   string
}{
	{"table", "sessions", sqlCreateSessions},
	{"table", "session_scopes", sqlCreateSessionScopes},
	{"table", "session_scope_dimensions", sqlCreateSessionScopeDimensions},
	{"table", "session_aliases", sqlCreateSessionAliases},
	{"table", "session_messages", sqlCreateSessionMessages},
	{"table", "threads", sqlCreateThreads},
	{"table", "thread_context", sqlCreateThreadContext},
	{"table", "thread_aliases", sqlCreateThreadAliases},
	{"table", "thread_sessions", sqlCreateThreadSessions},
	{"table", "session_thread_links", sqlCreateSessionThreadLinks},
	{"table", "thread_handoffs", sqlCreateThreadHandoffs},
	{"index", "session_alias_lookup_idx", sqlCreateSessionAliasIndex},
	{"index", "sessions_updated_idx", sqlCreateSessionUpdatedIndex},
	{"index", "threads_updated_idx", sqlCreateThreadsUpdatedIndex},
	{"index", "thread_sessions_session_idx", sqlCreateThreadSessionsSessionIndex},
	{"index", "thread_sessions_one_primary_idx", sqlCreateThreadSessionsPrimaryIndex},
	{"index", "session_thread_links_thread_idx", sqlCreateSessionThreadLinksThreadIndex},
	{"index", "thread_handoffs_origin_idx", sqlCreateThreadHandoffsOriginIndex},
	{"index", "thread_handoffs_target_idx", sqlCreateThreadHandoffsTargetIndex},
}

func sessionsMigrations() []sqlitestore.Migration {
	statements := make([]string, 0, len(sessionsSchemaObjects))
	for _, object := range sessionsSchemaObjects {
		statements = append(statements, object.schema)
	}
	return []sqlitestore.Migration{{Version: 1, Statements: statements}}
}

func validateSessionsSchema(ctx context.Context, conn *sql.Conn) error {
	for _, object := range sessionsSchemaObjects {
		if err := sqlitestore.ValidateSchemaObject(
			ctx, conn, object.typeName, object.name, object.schema,
		); err != nil {
			return err
		}
	}
	for _, table := range []string{
		"sessions", "session_scopes", "session_scope_dimensions", "session_aliases",
		"session_messages", "threads", "thread_context", "thread_aliases",
		"thread_sessions", "session_thread_links", "thread_handoffs",
	} {
		expected := []string(nil)
		if table == "thread_sessions" {
			expected = []string{"thread_sessions_one_primary_idx"}
		}
		if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, table, expected...); err != nil {
			return err
		}
	}
	if err := validateSessionsObjectSet(ctx, conn); err != nil {
		return err
	}
	if err := validateSessionsRelationships(ctx, conn); err != nil {
		return err
	}
	return validateSessionsDataBounds(ctx, conn)
}

func validateSessionsRelationships(ctx context.Context, conn *sql.Conn) error {
	for _, ordered := range []struct{ table, parent string }{
		{"session_scope_dimensions", "session_key"},
		{"session_aliases", "session_key"},
		{"session_messages", "session_key"},
		{"thread_aliases", "thread_id"},
		{"thread_sessions", "thread_id"},
	} {
		query := `SELECT 1 FROM ` + ordered.table + ` GROUP BY ` + ordered.parent + `
            HAVING MIN(sequence) <> 0 OR MAX(sequence) <> COUNT(*) - 1 LIMIT 1`
		var invalid int
		err := conn.QueryRowContext(ctx, query).Scan(&invalid)
		if err == nil {
			return fmt.Errorf("table %s has a non-contiguous sequence", ordered.table)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	var invalid int
	err := conn.QueryRowContext(ctx, `SELECT 1 FROM threads t
        LEFT JOIN thread_sessions ts ON ts.thread_id = t.thread_id
        GROUP BY t.thread_id
        HAVING COALESCE(SUM(CASE WHEN ts.is_primary = 1 THEN 1 ELSE 0 END), 0) <> 1
            OR COALESCE(SUM(CASE WHEN ts.is_primary = 1
                AND ts.session_key = t.primary_session_key THEN 1 ELSE 0 END), 0) <> 1
        LIMIT 1`).Scan(&invalid)
	if err == nil {
		return errors.New("thread primary-session relationships are inconsistent")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	err = conn.QueryRowContext(ctx, `SELECT 1 FROM session_thread_links l
        LEFT JOIN thread_sessions ts ON ts.thread_id = l.thread_id
            AND ts.session_key = l.session_key
        WHERE ts.thread_id IS NULL LIMIT 1`).Scan(&invalid)
	if err == nil {
		return errors.New("session-thread link has no reciprocal thread membership")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func validateSessionsObjectSet(ctx context.Context, conn *sql.Conn) error {
	allowed := map[string]struct{}{
		"storage_imports": {}, "storage_import_issues": {}, "storage_import_horizons": {},
		"storage_imports_archive_status_idx": {},
	}
	for _, object := range sessionsSchemaObjects {
		allowed[object.name] = struct{}{}
	}
	rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_schema
        WHERE type IN ('table', 'index', 'trigger', 'view')
          AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := make(map[string]struct{}, len(allowed))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("unexpected SQLite schema object %s", name)
		}
		seen[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(seen) != len(allowed) {
		return errors.New("SQLite schema object set is incomplete")
	}
	return nil
}

func validateSessionsDataBounds(ctx context.Context, conn *sql.Conn) error {
	for table, maximum := range map[string]int64{
		"sessions": 1_000_000, "session_scopes": 1_000_000,
		"session_scope_dimensions": 16_000_000, "session_aliases": 16_000_000,
		"session_messages": 100_000_000, "threads": 1_000_000,
		"thread_context": 16_000_000, "thread_aliases": 16_000_000,
		"thread_sessions": 16_000_000, "session_thread_links": 1_000_000,
		"thread_handoffs": 16_000_000,
	} {
		var count int64
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			return err
		}
		if count > maximum {
			return fmt.Errorf("table %s exceeds its row limit", table)
		}
	}
	var aggregate int64
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(SUM(
        length(CAST(role AS BLOB)) + length(CAST(content AS BLOB)) +
        length(CAST(model_name AS BLOB)) + length(CAST(reasoning_content AS BLOB)) +
        length(CAST(tool_call_id AS BLOB)) + COALESCE(length(nested_payload), 0)
    ), 0) FROM session_messages`).Scan(&aggregate); err != nil {
		return err
	}
	if aggregate > 1<<40 {
		return errors.New("session message aggregate exceeds its byte limit")
	}
	for table, expression := range map[string]string{
		"sessions": `length(CAST(session_key AS BLOB)) + length(CAST(summary AS BLOB))`,
		"session_scopes": `length(CAST(session_key AS BLOB)) + length(CAST(agent_id AS BLOB)) +
            length(CAST(channel AS BLOB)) + length(CAST(account AS BLOB))`,
		"session_scope_dimensions": `length(CAST(session_key AS BLOB)) +
            length(CAST(dimension AS BLOB)) + length(CAST(value AS BLOB))`,
		"session_aliases": `length(CAST(session_key AS BLOB)) + length(CAST(alias AS BLOB))`,
		"threads": `length(CAST(thread_id AS BLOB)) + length(CAST(ui_session_id AS BLOB)) +
            length(CAST(primary_session_key AS BLOB)) + length(CAST(agent_id AS BLOB)) +
            length(CAST(owner_identity AS BLOB)) + length(CAST(title AS BLOB)) +
            length(CAST(thread_type AS BLOB)) + length(CAST(source_query AS BLOB)) +
            length(CAST(registration AS BLOB))`,
		"thread_context": `length(CAST(thread_id AS BLOB)) + length(CAST(key AS BLOB)) +
            length(CAST(value AS BLOB))`,
		"thread_aliases":       `length(CAST(thread_id AS BLOB)) + length(CAST(alias AS BLOB))`,
		"thread_sessions":      `length(CAST(thread_id AS BLOB)) + length(CAST(session_key AS BLOB))`,
		"session_thread_links": `length(CAST(session_key AS BLOB)) + length(CAST(thread_id AS BLOB))`,
		"thread_handoffs": `length(CAST(handoff_id AS BLOB)) +
            length(CAST(origin_session_key AS BLOB)) + length(CAST(origin_session_id AS BLOB)) +
            length(CAST(target_thread_id AS BLOB)) + length(CAST(target_session_id AS BLOB)) +
            length(CAST(agent_id AS BLOB)) + length(CAST(summary AS BLOB))`,
	} {
		var total int64
		if err := conn.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(`+expression+`), 0) FROM `+table,
		).Scan(&total); err != nil {
			return err
		}
		if total > 1<<40 {
			return fmt.Errorf("table %s exceeds its aggregate byte limit", table)
		}
	}
	rows, err := conn.QueryContext(ctx, `SELECT nested_payload FROM session_messages
        WHERE nested_payload IS NOT NULL ORDER BY session_key, sequence`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return errors.New("nested session message JSON is invalid")
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return errors.New("nested session message JSON has trailing data")
		}
		canonical, err := json.Marshal(decoded)
		if err != nil || !bytes.Equal(canonical, raw) {
			return errors.New("nested session message JSON is not canonical")
		}
	}
	return rows.Err()
}
