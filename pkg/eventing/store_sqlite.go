//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const (
	defaultListLimit = 50
	maxListLimit     = 500
)

// Store is a single-node durable external-event inbox.
type Store struct {
	db              *sql.DB
	now             func() time.Time
	redactor        *Redactor
	maxPayloadBytes int

	closed   atomic.Bool
	close    sync.Once
	closeErr error
}

var _ Inbox = (*Store)(nil)

// Open creates or opens a durable eventing store at path.
func Open(ctx context.Context, path string, options ...Option) (*Store, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("eventing database path is required")
	}
	if len(path) >= len("file:") && strings.EqualFold(path[:len("file:")], "file:") {
		return nil, fmt.Errorf("eventing database path must be a filesystem path, not a SQLite URI")
	}
	if strings.ContainsRune(path, '\x00') {
		return nil, fmt.Errorf("eventing database path contains a NUL byte")
	}

	resolved := optionsFrom(options)
	fileBacked := path != ":memory:"
	if fileBacked {
		parent := filepath.Dir(path)
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return nil, fmt.Errorf("create eventing database directory: %w", err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, fmt.Errorf("securely create eventing database: %w", err)
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("secure eventing database permissions: %w", err)
		}
		if err := file.Close(); err != nil {
			return nil, fmt.Errorf("close prepared eventing database: %w", err)
		}
	}

	dsn, err := sqliteDSN(path, resolved)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open eventing database: %w", err)
	}
	// Keeping one connection makes connection-local PRAGMAs authoritative and
	// serializes claims within the single-node deployment contract.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{
		db:              db,
		now:             resolved.now,
		redactor:        NewRedactor(resolved.additionalKeys, resolved.secretValues),
		maxPayloadBytes: resolved.maxPayloadBytes,
	}
	if err := store.configure(ctx, resolved.busyTimeout, !fileBacked); err != nil {
		_ = db.Close()
		return nil, err
	}
	if fileBacked {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("secure eventing database permissions: %w", err)
		}
	}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if fileBacked {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("secure eventing database permissions: %w", err)
		}
	}
	return store, nil
}

// OpenStore is an explicit alias for Open.
func OpenStore(ctx context.Context, path string, options ...Option) (*Store, error) {
	return Open(ctx, path, options...)
}

func sqliteDSN(path string, options storeOptions) (string, error) {
	if path == ":memory:" {
		// A replacement connection would also lose the in-memory schema, so the
		// file-backed reconnect guarantee does not apply to this test-only mode.
		return path, nil
	}

	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve eventing database path: %w", err)
	}
	query := url.Values{}
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout("+strconv.FormatInt(options.busyTimeout.Milliseconds(), 10)+")")
	query.Add("_pragma", "synchronous(NORMAL)")
	databaseURL, err := sqliteFileURL(
		filepath.ToSlash(absolutePath),
		filepath.ToSlash(filepath.VolumeName(absolutePath)),
	)
	if err != nil {
		return "", err
	}
	databaseURL.RawQuery = query.Encode()
	return databaseURL.String(), nil
}

func sqliteFileURL(slashPath, slashVolume string) (*url.URL, error) {
	if strings.HasPrefix(slashVolume, "//") {
		authorityAndShare := strings.TrimPrefix(slashVolume, "//")
		server, share, ok := strings.Cut(authorityAndShare, "/")
		if !ok || server == "" || share == "" || !strings.HasPrefix(slashPath, slashVolume) {
			return nil, fmt.Errorf("resolve eventing UNC database path %q", slashPath)
		}
		// Keep the URI authority empty. SQLite rejects remote authorities unless
		// compiled with SQLITE_ALLOW_URI_AUTHORITY; file:////server/share keeps
		// the UNC server and share in the path for the Windows VFS.
		return &url.URL{Scheme: "file", Path: slashPath}, nil
	}
	if slashVolume != "" && !strings.HasPrefix(slashPath, "/") {
		// A Windows drive path must be emitted as file:///C:/..., not with the
		// drive parsed as a URI authority.
		slashPath = "/" + slashPath
	}
	return &url.URL{Scheme: "file", Path: slashPath}, nil
}

func (s *Store) configure(ctx context.Context, busyTimeout time.Duration, memory bool) error {
	var journalMode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return fmt.Errorf("enable eventing WAL: %w", err)
	}
	if !memory && !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("enable eventing WAL: SQLite selected %q", journalMode)
	}

	var foreignKeys, configuredBusyTimeout, synchronous int
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("verify eventing foreign keys: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&configuredBusyTimeout); err != nil {
		return fmt.Errorf("verify eventing busy timeout: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return fmt.Errorf("verify eventing synchronous mode: %w", err)
	}
	if memory {
		// The non-URI :memory: path cannot carry per-connection options.
		if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
			return fmt.Errorf("configure in-memory eventing foreign keys: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, "PRAGMA busy_timeout = "+
			strconv.FormatInt(busyTimeout.Milliseconds(), 10)); err != nil {
			return fmt.Errorf("configure in-memory eventing busy timeout: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, "PRAGMA synchronous = NORMAL"); err != nil {
			return fmt.Errorf("configure in-memory eventing synchronous mode: %w", err)
		}
		return nil
	}
	if foreignKeys != 1 {
		return fmt.Errorf("verify eventing foreign keys: got %d, want 1", foreignKeys)
	}
	if configuredBusyTimeout != int(busyTimeout.Milliseconds()) {
		return fmt.Errorf("verify eventing busy timeout: got %d, want %d",
			configuredBusyTimeout, busyTimeout.Milliseconds())
	}
	// SQLite represents NORMAL as 1.
	if synchronous != 1 {
		return fmt.Errorf("verify eventing synchronous mode: got %d, want 1", synchronous)
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire eventing migration connection: %w", err)
	}
	defer conn.Close()

	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin eventing migration: %w", err)
	}
	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var version int
	if err = conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read eventing schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("%w: database=%d supported=%d", ErrSchemaTooNew, version, schemaVersion)
	}
	if version < 1 {
		if _, err = conn.ExecContext(ctx, schemaV1); err != nil {
			return fmt.Errorf("create eventing schema v1: %w", err)
		}
		if _, err = conn.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
			return fmt.Errorf("record eventing schema v1: %w", err)
		}
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit eventing migration: %w", err)
	}
	return nil
}

const schemaV1 = `
CREATE TABLE IF NOT EXISTS event_inbox (
	id TEXT PRIMARY KEY,
	source TEXT NOT NULL,
	connector TEXT NOT NULL,
	event_type TEXT NOT NULL,
	dedupe_key TEXT NOT NULL,
	actor_json BLOB,
	subject_json BLOB,
	occurred_at INTEGER,
	received_at INTEGER NOT NULL,
	payload_json BLOB NOT NULL,
	attributes_json BLOB NOT NULL,
	replay_of TEXT REFERENCES event_inbox(id) ON DELETE RESTRICT,
	routing_status TEXT NOT NULL CHECK (routing_status IN ('pending', 'claimed', 'succeeded', 'dead')),
	routing_owner TEXT NOT NULL DEFAULT '',
	routing_lease_until INTEGER,
	routing_available_at INTEGER NOT NULL,
	routing_attempts INTEGER NOT NULL DEFAULT 0 CHECK (routing_attempts >= 0),
	routing_last_error TEXT NOT NULL DEFAULT '',
	routing_updated_at INTEGER NOT NULL,
	CHECK (replay_of IS NULL OR replay_of <> id)
);
CREATE UNIQUE INDEX IF NOT EXISTS event_inbox_dedupe
	ON event_inbox(source, connector, dedupe_key)
	WHERE dedupe_key <> '';
CREATE INDEX IF NOT EXISTS event_inbox_routing_claim
	ON event_inbox(routing_status, routing_available_at, routing_lease_until, received_at, id);
CREATE INDEX IF NOT EXISTS event_inbox_list
	ON event_inbox(received_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS event_dispatches (
	id TEXT PRIMARY KEY,
	event_id TEXT NOT NULL REFERENCES event_inbox(id) ON DELETE CASCADE,
	workflow_ref TEXT NOT NULL,
	run_id TEXT NOT NULL UNIQUE,
	status TEXT NOT NULL CHECK (status IN ('pending', 'claimed', 'running', 'succeeded', 'failed', 'dead')),
	owner TEXT NOT NULL DEFAULT '',
	lease_until INTEGER,
	available_at INTEGER NOT NULL,
	attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
	last_error TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	linked_at INTEGER,
	finished_at INTEGER,
	UNIQUE(event_id, workflow_ref)
);
CREATE INDEX IF NOT EXISTS event_dispatches_claim
	ON event_dispatches(status, available_at, lease_until, created_at, id);
CREATE INDEX IF NOT EXISTS event_dispatches_list
	ON event_dispatches(created_at DESC, id DESC);
`

// Close releases the database. It is safe and idempotent.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.close.Do(func() {
		s.closed.Store(true)
		if s.db == nil {
			s.closeErr = ErrClosed
			return
		}
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

func (s *Store) ready(ctx context.Context) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if s == nil || s.db == nil || s.closed.Load() {
		return ErrClosed
	}
	return nil
}

func (s *Store) currentTime() (time.Time, error) {
	now := s.now().UTC()
	if err := validateDBTimestamp("store clock", now); err != nil {
		return time.Time{}, err
	}
	return now, nil
}

func (s *Store) dbError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if s == nil || s.closed.Load() || strings.Contains(err.Error(), "database is closed") {
		return ErrClosed
	}
	return err
}

// Insert validates, redacts, and atomically deduplicates an envelope.
func (s *Store) Insert(ctx context.Context, input Envelope) (InsertResult, error) {
	if err := s.ready(ctx); err != nil {
		return InsertResult{}, err
	}
	if len(input.Payload) > s.maxPayloadBytes {
		return InsertResult{}, fmt.Errorf("%w: got %d bytes, maximum %d",
			ErrPayloadTooLarge, len(input.Payload), s.maxPayloadBytes)
	}
	now, err := s.currentTime()
	if err != nil {
		return InsertResult{}, err
	}
	event, err := NormalizeEnvelope(input, now)
	if err != nil {
		return InsertResult{}, err
	}
	event, err = s.redactor.RedactEnvelope(event)
	if err != nil {
		return InsertResult{}, fmt.Errorf("%w: redact payload: %v", ErrInvalidEnvelope, err)
	}
	if len(event.Payload) > s.maxPayloadBytes {
		return InsertResult{}, fmt.Errorf("%w after redaction: got %d bytes, maximum %d",
			ErrPayloadTooLarge, len(event.Payload), s.maxPayloadBytes)
	}
	if err := event.Validate(); err != nil {
		return InsertResult{}, err
	}

	actorJSON, subjectJSON, attributesJSON, err := marshalEnvelopeParts(event)
	if err != nil {
		return InsertResult{}, err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO event_inbox (
			id, source, connector, event_type, dedupe_key, actor_json,
			subject_json, occurred_at, received_at, payload_json,
			attributes_json, replay_of, routing_status, routing_available_at,
			routing_updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source, connector, dedupe_key) WHERE dedupe_key <> '' DO NOTHING`,
		event.ID, event.Source, event.Connector, event.Type, event.DedupeKey,
		actorJSON, subjectJSON, nullableTime(event.OccurredAt), toDBTime(event.ReceivedAt),
		[]byte(event.Payload), attributesJSON, nullableString(event.ReplayOf),
		RoutingPending, toDBTime(now), toDBTime(now),
	)
	if err != nil {
		return InsertResult{}, fmt.Errorf("insert event: %w", s.dbError(err))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return InsertResult{}, fmt.Errorf("read insert result: %w", err)
	}
	if affected == 0 {
		stored, err := s.getByDedupe(ctx, event.Source, event.Connector, event.DedupeKey)
		if err != nil {
			return InsertResult{}, err
		}
		return InsertResult{Event: stored, Inserted: false}, nil
	}
	stored, err := s.Get(ctx, event.ID)
	if err != nil {
		return InsertResult{}, err
	}
	return InsertResult{Event: stored, Inserted: true}, nil
}

// Get retrieves an event by ID.
func (s *Store) Get(ctx context.Context, id string) (StoredEvent, error) {
	if err := s.ready(ctx); err != nil {
		return StoredEvent{}, err
	}
	return s.getWith(ctx, s.db, `
		SELECT `+eventColumns+` FROM event_inbox WHERE id = ?`, strings.TrimSpace(id))
}

func (s *Store) getByDedupe(ctx context.Context, source, connector, dedupeKey string) (StoredEvent, error) {
	return s.getWith(ctx, s.db, `
		SELECT `+eventColumns+` FROM event_inbox
		WHERE source = ? AND connector = ? AND dedupe_key = ?`,
		source, connector, dedupeKey,
	)
}

const eventColumns = `
	id, source, connector, event_type, dedupe_key, actor_json, subject_json,
	occurred_at, received_at, payload_json, attributes_json, replay_of,
	routing_status, routing_owner, routing_lease_until, routing_attempts,
	routing_available_at, routing_last_error, routing_updated_at`

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) getWith(ctx context.Context, queryer rowQueryer, query string, args ...any) (StoredEvent, error) {
	event, err := scanStoredEvent(queryer.QueryRowContext(ctx, query, args...))
	if err != nil {
		return StoredEvent{}, s.dbError(err)
	}
	return event, nil
}

// List returns a newest-first keyset page.
func (s *Store) List(ctx context.Context, filter EventFilter) (EventPage, error) {
	if err := s.ready(ctx); err != nil {
		return EventPage{}, err
	}
	if filter.RoutingStatus != "" && !validRoutingStatus(filter.RoutingStatus) {
		return EventPage{}, fmt.Errorf("%w: unknown routing status %q", ErrInvalidTransition, filter.RoutingStatus)
	}
	if filter.After != nil {
		if err := validateDBTimestamp("event cursor received_at", filter.After.ReceivedAt); err != nil {
			return EventPage{}, err
		}
	}
	limit := normalizedLimit(filter.Limit)
	query := `SELECT ` + eventColumns + ` FROM event_inbox WHERE 1 = 1`
	args := make([]any, 0, 10)
	if filter.Source != "" {
		query += ` AND source = ?`
		args = append(args, filter.Source)
	}
	if filter.Connector != "" {
		query += ` AND connector = ?`
		args = append(args, filter.Connector)
	}
	if filter.Type != "" {
		query += ` AND event_type = ?`
		args = append(args, filter.Type)
	}
	if filter.RoutingStatus != "" {
		query += ` AND routing_status = ?`
		args = append(args, filter.RoutingStatus)
	}
	if filter.After != nil {
		query += ` AND (received_at < ? OR (received_at = ? AND id < ?))`
		position := toDBTime(filter.After.ReceivedAt)
		args = append(args, position, position, filter.After.ID)
	}
	query += ` ORDER BY received_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return EventPage{}, fmt.Errorf("list events: %w", s.dbError(err))
	}
	defer rows.Close()

	events := make([]StoredEvent, 0, limit+1)
	for rows.Next() {
		event, scanErr := scanStoredEvent(rows)
		if scanErr != nil {
			return EventPage{}, fmt.Errorf("scan event list: %w", scanErr)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return EventPage{}, fmt.Errorf("iterate event list: %w", s.dbError(err))
	}
	page := EventPage{Events: events}
	if len(events) > limit {
		page.Events = events[:limit]
		last := page.Events[len(page.Events)-1].Envelope
		page.Next = &EventCursor{ReceivedAt: last.ReceivedAt, ID: last.ID}
	}
	return page, nil
}

// ListEvents is an explicit alias for List.
func (s *Store) ListEvents(ctx context.Context, filter EventFilter) (EventPage, error) {
	return s.List(ctx, filter)
}

// ClaimRouting claims the oldest available or expired inbox work. workerLabel
// is diagnostic only. Each result has a fresh opaque LeaseToken which must be
// supplied to transitions, fencing previous claims by the same worker.
func (s *Store) ClaimRouting(
	ctx context.Context,
	workerLabel string,
	limit int,
	lease time.Duration,
) ([]StoredEvent, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	workerLabel = strings.TrimSpace(workerLabel)
	if workerLabel == "" || limit <= 0 || lease <= 0 {
		return nil, fmt.Errorf("worker label, positive limit, and positive lease are required")
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	now, err := s.currentTime()
	if err != nil {
		return nil, err
	}
	leaseUntil := now.Add(lease)
	if err := validateDBTimestamp("routing lease deadline", leaseUntil); err != nil {
		return nil, err
	}
	claimed := make([]StoredEvent, 0, limit)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, `
			SELECT id FROM event_inbox
			WHERE (routing_status = ? AND routing_available_at <= ?)
			   OR (routing_status = ? AND routing_lease_until <= ?)
			ORDER BY received_at ASC, id ASC
			LIMIT ?`,
			RoutingPending, toDBTime(now), RoutingClaimed, toDBTime(now), limit,
		)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range ids {
			leaseToken, err := newLeaseToken(workerLabel)
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `
				UPDATE event_inbox
				SET routing_status = ?, routing_owner = ?, routing_lease_until = ?,
				    routing_attempts = routing_attempts + 1, routing_updated_at = ?
				WHERE id = ?`,
				RoutingClaimed, leaseToken, toDBTime(leaseUntil), toDBTime(now), id,
			); err != nil {
				return err
			}
			event, err := s.getWith(ctx, conn,
				`SELECT `+eventColumns+` FROM event_inbox WHERE id = ?`, id)
			if err != nil {
				return err
			}
			claimed = append(claimed, event)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("claim routing work: %w", s.dbError(err))
	}
	return claimed, nil
}

// AckRouting marks owned, live routing work successful.
func (s *Store) AckRouting(ctx context.Context, id, leaseToken string) error {
	return s.finishRouting(ctx, id, leaseToken, RoutingSucceeded, time.Time{}, "")
}

// NackRouting returns owned, live routing work to the pending queue at
// availableAt. Zero or past values retry immediately.
func (s *Store) NackRouting(
	ctx context.Context,
	id, leaseToken string,
	availableAt time.Time,
	detail string,
) error {
	return s.finishRouting(ctx, id, leaseToken, RoutingPending, availableAt, detail)
}

// DeadRouting marks owned, live routing work permanently dead.
func (s *Store) DeadRouting(ctx context.Context, id, leaseToken, detail string) error {
	return s.finishRouting(ctx, id, leaseToken, RoutingDead, time.Time{}, detail)
}

func (s *Store) finishRouting(
	ctx context.Context,
	id, leaseToken string,
	status RoutingStatus,
	availableAt time.Time,
	detail string,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	now, err := s.currentTime()
	if err != nil {
		return err
	}
	if availableAt.IsZero() || availableAt.Before(now) {
		availableAt = now
	}
	availableAt = availableAt.UTC()
	if err := validateDBTimestamp("routing availability", availableAt); err != nil {
		return err
	}
	detail = s.sanitizeDetail(detail)
	result, err := s.db.ExecContext(ctx, `
		UPDATE event_inbox
		SET routing_status = ?, routing_owner = '', routing_lease_until = NULL,
		    routing_available_at = ?, routing_last_error = ?, routing_updated_at = ?
		WHERE id = ? AND routing_status = ? AND routing_owner = ?
		  AND routing_lease_until > ?`,
		status, toDBTime(availableAt), detail, toDBTime(now), id,
		RoutingClaimed, leaseToken, toDBTime(now),
	)
	if err != nil {
		return fmt.Errorf("finish routing work: %w", s.dbError(err))
	}
	return s.requireLeaseUpdate(ctx, result, "event_inbox", id)
}

// CreateDispatch idempotently creates one workflow delivery for an event.
// Both the dispatch ID and workflow run ID are deterministic for the
// (event, workflow) pair.
func (s *Store) CreateDispatch(
	ctx context.Context,
	eventID, workflowRef string,
) (Dispatch, bool, error) {
	if err := s.ready(ctx); err != nil {
		return Dispatch{}, false, err
	}
	eventID = strings.TrimSpace(eventID)
	workflowRef = strings.TrimSpace(workflowRef)
	if eventID == "" || workflowRef == "" {
		return Dispatch{}, false, fmt.Errorf("event ID and workflow reference are required")
	}
	if !validEventID(eventID) {
		return Dispatch{}, false, fmt.Errorf("event ID is invalid")
	}
	if err := validateBoundedString("workflow reference", workflowRef, maxWorkflowRefLength); err != nil {
		return Dispatch{}, false, err
	}
	dispatchID, runID := deterministicDispatchIDs(eventID, workflowRef)
	now, err := s.currentTime()
	if err != nil {
		return Dispatch{}, false, err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO event_dispatches (
			id, event_id, workflow_ref, run_id, status, available_at, created_at, updated_at
		)
		SELECT ?, id, ?, ?, ?, ?, ?, ? FROM event_inbox WHERE id = ?
		ON CONFLICT(event_id, workflow_ref) DO NOTHING`,
		dispatchID, workflowRef, runID, DispatchPending, toDBTime(now),
		toDBTime(now), toDBTime(now), eventID,
	)
	if err != nil {
		return Dispatch{}, false, fmt.Errorf("create event dispatch: %w", s.dbError(err))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Dispatch{}, false, err
	}
	dispatch, getErr := s.getDispatchByPair(ctx, eventID, workflowRef)
	if getErr != nil {
		if affected == 0 && errors.Is(getErr, ErrNotFound) {
			return Dispatch{}, false, fmt.Errorf("%w: event %q", ErrNotFound, eventID)
		}
		return Dispatch{}, false, getErr
	}
	return dispatch, affected == 1, nil
}

// GetDispatch retrieves one dispatch by ID.
func (s *Store) GetDispatch(ctx context.Context, id string) (Dispatch, error) {
	if err := s.ready(ctx); err != nil {
		return Dispatch{}, err
	}
	dispatch, err := scanDispatch(s.db.QueryRowContext(ctx,
		`SELECT `+dispatchColumns+` FROM event_dispatches WHERE id = ?`, id))
	if err != nil {
		return Dispatch{}, s.dbError(err)
	}
	return dispatch, nil
}

func (s *Store) getDispatchByPair(ctx context.Context, eventID, workflowRef string) (Dispatch, error) {
	dispatch, err := scanDispatch(s.db.QueryRowContext(ctx, `
		SELECT `+dispatchColumns+` FROM event_dispatches
		WHERE event_id = ? AND workflow_ref = ?`, eventID, workflowRef))
	if err != nil {
		return Dispatch{}, s.dbError(err)
	}
	return dispatch, nil
}

const dispatchColumns = `
	id, event_id, workflow_ref, run_id, status, owner, lease_until,
	available_at, attempts, last_error, created_at, updated_at, linked_at, finished_at`

// ClaimDispatches claims the oldest available or expired workflow deliveries.
// workerLabel is diagnostic only; transitions require each returned fresh
// LeaseToken.
func (s *Store) ClaimDispatches(
	ctx context.Context,
	workerLabel string,
	limit int,
	lease time.Duration,
) ([]Dispatch, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	workerLabel = strings.TrimSpace(workerLabel)
	if workerLabel == "" || limit <= 0 || lease <= 0 {
		return nil, fmt.Errorf("worker label, positive limit, and positive lease are required")
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	now, err := s.currentTime()
	if err != nil {
		return nil, err
	}
	leaseUntil := now.Add(lease)
	if err := validateDBTimestamp("dispatch lease deadline", leaseUntil); err != nil {
		return nil, err
	}
	claimed := make([]Dispatch, 0, limit)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, `
			SELECT id FROM event_dispatches
			WHERE (status = ? AND available_at <= ?)
			   OR (status IN (?, ?) AND lease_until <= ?)
			ORDER BY created_at ASC, id ASC
			LIMIT ?`,
			DispatchPending, toDBTime(now), DispatchClaimed, DispatchRunning,
			toDBTime(now), limit,
		)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range ids {
			leaseToken, err := newLeaseToken(workerLabel)
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `
				UPDATE event_dispatches
				SET status = ?, owner = ?, lease_until = ?,
				    attempts = attempts + 1, updated_at = ?
				WHERE id = ?`,
				DispatchClaimed, leaseToken, toDBTime(leaseUntil), toDBTime(now), id,
			); err != nil {
				return err
			}
			dispatch, err := scanDispatch(conn.QueryRowContext(ctx,
				`SELECT `+dispatchColumns+` FROM event_dispatches WHERE id = ?`, id))
			if err != nil {
				return err
			}
			claimed = append(claimed, dispatch)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("claim dispatches: %w", s.dbError(err))
	}
	return claimed, nil
}

// LinkDispatchRun records that the deterministic workflow run has started.
func (s *Store) LinkDispatchRun(ctx context.Context, id, leaseToken, runID string) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	dispatch, err := s.GetDispatch(ctx, id)
	if err != nil {
		return err
	}
	if dispatch.RunID != runID {
		return fmt.Errorf("%w: expected %q", ErrRunIDMismatch, dispatch.RunID)
	}
	now, err := s.currentTime()
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE event_dispatches
		SET status = ?, linked_at = COALESCE(linked_at, ?), updated_at = ?
		WHERE id = ? AND status = ? AND owner = ? AND lease_until > ?`,
		DispatchRunning, toDBTime(now), toDBTime(now), id,
		DispatchClaimed, leaseToken, toDBTime(now),
	)
	if err != nil {
		return fmt.Errorf("link event dispatch: %w", s.dbError(err))
	}
	return s.requireLeaseUpdate(ctx, result, "event_dispatches", id)
}

// FinishDispatch completes owned, live claimed or running work.
func (s *Store) FinishDispatch(
	ctx context.Context,
	id, leaseToken string,
	status DispatchStatus,
	detail string,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	if !terminalDispatchStatus(status) {
		return fmt.Errorf("%w: dispatch finish status %q is not terminal", ErrInvalidTransition, status)
	}
	now, err := s.currentTime()
	if err != nil {
		return err
	}
	detail = s.sanitizeDetail(detail)
	result, err := s.db.ExecContext(ctx, `
		UPDATE event_dispatches
		SET status = ?, owner = '', lease_until = NULL, last_error = ?,
		    updated_at = ?, finished_at = ?
		WHERE id = ? AND status IN (?, ?) AND owner = ? AND lease_until > ?`,
		status, detail, toDBTime(now), toDBTime(now), id,
		DispatchClaimed, DispatchRunning, leaseToken, toDBTime(now),
	)
	if err != nil {
		return fmt.Errorf("finish event dispatch: %w", s.dbError(err))
	}
	return s.requireLeaseUpdate(ctx, result, "event_dispatches", id)
}

// NackDispatch returns owned, live claimed or running work to the pending
// queue at availableAt. Zero or past values retry immediately.
func (s *Store) NackDispatch(
	ctx context.Context,
	id, leaseToken string,
	availableAt time.Time,
	detail string,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	now, err := s.currentTime()
	if err != nil {
		return err
	}
	if availableAt.IsZero() || availableAt.Before(now) {
		availableAt = now
	}
	availableAt = availableAt.UTC()
	if err := validateDBTimestamp("dispatch availability", availableAt); err != nil {
		return err
	}
	detail = s.sanitizeDetail(detail)
	result, err := s.db.ExecContext(ctx, `
		UPDATE event_dispatches
		SET status = ?, owner = '', lease_until = NULL, available_at = ?,
		    last_error = ?, updated_at = ?
		WHERE id = ? AND status IN (?, ?) AND owner = ? AND lease_until > ?`,
		DispatchPending, toDBTime(availableAt), detail, toDBTime(now), id,
		DispatchClaimed, DispatchRunning, leaseToken, toDBTime(now),
	)
	if err != nil {
		return fmt.Errorf("nack event dispatch: %w", s.dbError(err))
	}
	return s.requireLeaseUpdate(ctx, result, "event_dispatches", id)
}

// ListDispatches returns a newest-first keyset page.
func (s *Store) ListDispatches(ctx context.Context, filter DispatchFilter) (DispatchPage, error) {
	if err := s.ready(ctx); err != nil {
		return DispatchPage{}, err
	}
	if filter.Status != "" && !validDispatchStatus(filter.Status) {
		return DispatchPage{}, fmt.Errorf("%w: unknown dispatch status %q", ErrInvalidTransition, filter.Status)
	}
	if filter.After != nil {
		if err := validateDBTimestamp("dispatch cursor created_at", filter.After.CreatedAt); err != nil {
			return DispatchPage{}, err
		}
	}
	limit := normalizedLimit(filter.Limit)
	query := `SELECT ` + dispatchColumns + ` FROM event_dispatches WHERE 1 = 1`
	args := make([]any, 0, 8)
	if filter.EventID != "" {
		query += ` AND event_id = ?`
		args = append(args, filter.EventID)
	}
	if filter.WorkflowRef != "" {
		query += ` AND workflow_ref = ?`
		args = append(args, filter.WorkflowRef)
	}
	if filter.Status != "" {
		query += ` AND status = ?`
		args = append(args, filter.Status)
	}
	if filter.After != nil {
		query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		position := toDBTime(filter.After.CreatedAt)
		args = append(args, position, position, filter.After.ID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return DispatchPage{}, fmt.Errorf("list dispatches: %w", s.dbError(err))
	}
	defer rows.Close()
	dispatches := make([]Dispatch, 0, limit+1)
	for rows.Next() {
		dispatch, scanErr := scanDispatch(rows)
		if scanErr != nil {
			return DispatchPage{}, fmt.Errorf("scan dispatch list: %w", scanErr)
		}
		dispatches = append(dispatches, dispatch)
	}
	if err := rows.Err(); err != nil {
		return DispatchPage{}, fmt.Errorf("iterate dispatch list: %w", s.dbError(err))
	}
	page := DispatchPage{Dispatches: dispatches}
	if len(dispatches) > limit {
		page.Dispatches = dispatches[:limit]
		last := page.Dispatches[len(page.Dispatches)-1]
		page.Next = &DispatchCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

// Replay creates a fresh inbox item linked to an existing event. The replay
// gets a new identity and dedupe key, leaving the original immutable.
func (s *Store) Replay(ctx context.Context, id string) (InsertResult, error) {
	if err := s.ready(ctx); err != nil {
		return InsertResult{}, err
	}
	original, err := s.Get(ctx, id)
	if err != nil {
		return InsertResult{}, err
	}
	replay := original.Envelope.Clone()
	replay.ID = ""
	replay.ReceivedAt = time.Time{}
	replay.ReplayOf = original.Envelope.ID
	replayID, err := newPrefixedID(eventIDPrefix)
	if err != nil {
		return InsertResult{}, err
	}
	replay.ID = replayID
	replay.DedupeKey = "replay/" + replayID
	return s.Insert(ctx, replay)
}

// Prune removes terminal events older than before. Pending/claimed routing and
// events with any non-terminal dispatch are retained.
func (s *Store) Prune(ctx context.Context, before time.Time, limit int) (int64, error) {
	if err := s.ready(ctx); err != nil {
		return 0, err
	}
	if before.IsZero() || limit <= 0 {
		return 0, fmt.Errorf("non-zero cutoff and positive limit are required")
	}
	before = before.UTC()
	if err := validateDBTimestamp("retention cutoff", before); err != nil {
		return 0, err
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM event_inbox
		WHERE id IN (
			SELECT e.id
			FROM event_inbox e
			WHERE e.received_at < ?
			  AND e.routing_status IN (?, ?)
			  AND NOT EXISTS (
				SELECT 1 FROM event_dispatches d
				WHERE d.event_id = e.id
				  AND d.status IN (?, ?, ?)
			  )
			  AND NOT EXISTS (
				SELECT 1 FROM event_inbox replay
				WHERE replay.replay_of = e.id
			  )
			ORDER BY e.received_at ASC, e.id ASC
			LIMIT ?
		)`,
		toDBTime(before), RoutingSucceeded, RoutingDead,
		DispatchPending, DispatchClaimed, DispatchRunning, limit,
	)
	if err != nil {
		return 0, fmt.Errorf("prune event inbox: %w", s.dbError(err))
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) requireLeaseUpdate(
	ctx context.Context,
	result sql.Result,
	table, id string,
) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	var exists int
	err = s.db.QueryRowContext(ctx,
		`SELECT 1 FROM `+table+` WHERE id = ?`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return s.dbError(err)
	}
	return ErrStaleLease
}

func (s *Store) sanitizeDetail(detail string) string {
	detail = strings.ToValidUTF8(detail, "\uFFFD")
	detail = s.redactor.RedactText(detail)
	if len(detail) <= maxErrorDetailBytes {
		return detail
	}
	detail = detail[:maxErrorDetailBytes]
	for !utf8.ValidString(detail) {
		detail = detail[:len(detail)-1]
	}
	return detail
}

func (s *Store) withImmediate(ctx context.Context, operation func(*sql.Conn) error) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err = operation(conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func marshalEnvelopeParts(event Envelope) ([]byte, []byte, []byte, error) {
	actorJSON, err := json.Marshal(event.Actor)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal event actor: %w", err)
	}
	subjectJSON, err := json.Marshal(event.Subject)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal event subject: %w", err)
	}
	attributesJSON, err := json.Marshal(event.Attributes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal event attributes: %w", err)
	}
	return actorJSON, subjectJSON, attributesJSON, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanStoredEvent(scanner rowScanner) (StoredEvent, error) {
	var (
		event                       StoredEvent
		actorJSON, subjectJSON      []byte
		payloadJSON, attributesJSON []byte
		occurredAt, leaseUntil      sql.NullInt64
		replayOf                    sql.NullString
		receivedAt, availableAt     int64
		updatedAt                   int64
	)
	err := scanner.Scan(
		&event.Envelope.ID,
		&event.Envelope.Source,
		&event.Envelope.Connector,
		&event.Envelope.Type,
		&event.Envelope.DedupeKey,
		&actorJSON,
		&subjectJSON,
		&occurredAt,
		&receivedAt,
		&payloadJSON,
		&attributesJSON,
		&replayOf,
		&event.Routing.Status,
		&event.Routing.LeaseToken,
		&leaseUntil,
		&event.Routing.Attempts,
		&availableAt,
		&event.Routing.LastError,
		&updatedAt,
	)
	if err != nil {
		return StoredEvent{}, err
	}
	if string(actorJSON) != "null" {
		if err := json.Unmarshal(actorJSON, &event.Envelope.Actor); err != nil {
			return StoredEvent{}, err
		}
	}
	if string(subjectJSON) != "null" {
		if err := json.Unmarshal(subjectJSON, &event.Envelope.Subject); err != nil {
			return StoredEvent{}, err
		}
	}
	if string(attributesJSON) != "null" {
		if err := json.Unmarshal(attributesJSON, &event.Envelope.Attributes); err != nil {
			return StoredEvent{}, err
		}
	}
	event.Envelope.Payload = cloneBytes(payloadJSON)
	event.Envelope.OccurredAt = fromNullableTime(occurredAt)
	event.Envelope.ReceivedAt = fromDBTime(receivedAt)
	if replayOf.Valid {
		event.Envelope.ReplayOf = replayOf.String
	}
	event.Routing.LeaseUntil = fromNullableTime(leaseUntil)
	event.Routing.AvailableAt = fromDBTime(availableAt)
	event.Routing.UpdatedAt = fromDBTime(updatedAt)
	return event, nil
}

func scanDispatch(scanner rowScanner) (Dispatch, error) {
	var (
		dispatch                          Dispatch
		leaseUntil, linkedAt, finishedAt  sql.NullInt64
		availableAt, createdAt, updatedAt int64
	)
	err := scanner.Scan(
		&dispatch.ID,
		&dispatch.EventID,
		&dispatch.WorkflowRef,
		&dispatch.RunID,
		&dispatch.Status,
		&dispatch.LeaseToken,
		&leaseUntil,
		&availableAt,
		&dispatch.Attempts,
		&dispatch.LastError,
		&createdAt,
		&updatedAt,
		&linkedAt,
		&finishedAt,
	)
	if err != nil {
		return Dispatch{}, err
	}
	dispatch.LeaseUntil = fromNullableTime(leaseUntil)
	dispatch.AvailableAt = fromDBTime(availableAt)
	dispatch.CreatedAt = fromDBTime(createdAt)
	dispatch.UpdatedAt = fromDBTime(updatedAt)
	dispatch.LinkedAt = fromNullableTime(linkedAt)
	dispatch.FinishedAt = fromNullableTime(finishedAt)
	return dispatch, nil
}

func deterministicDispatchIDs(eventID, workflowRef string) (string, string) {
	dispatchDigest := sha256.Sum256([]byte("dispatch\x00" + eventID + "\x00" + workflowRef))
	runDigest := sha256.Sum256([]byte("run\x00" + eventID + "\x00" + workflowRef))
	return "dsp_" + hex.EncodeToString(dispatchDigest[:16]),
		"wr_" + hex.EncodeToString(runDigest[:16])
}

func newLeaseToken(workerLabel string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate eventing lease token: %w", err)
	}
	workerDigest := sha256.Sum256([]byte(workerLabel))
	return "lease_" + hex.EncodeToString(workerDigest[:4]) + "_" +
		hex.EncodeToString(random[:]), nil
}

func normalizedLimit(limit int) int {
	if limit <= 0 {
		return defaultListLimit
	}
	if limit > maxListLimit {
		return maxListLimit
	}
	return limit
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return toDBTime(*value)
}

func toDBTime(value time.Time) int64 {
	return value.UTC().UnixNano()
}

func fromDBTime(value int64) time.Time {
	return time.Unix(0, value).UTC()
}

func fromNullableTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	parsed := fromDBTime(value.Int64)
	return &parsed
}
