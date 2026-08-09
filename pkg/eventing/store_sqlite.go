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

var (
	_ Inbox                          = (*Store)(nil)
	_ EventOperatorReader            = (*Store)(nil)
	_ DispatchOperatorReader         = (*Store)(nil)
	_ DispatchOperatorGetter         = (*Store)(nil)
	_ RoutingDispatchCreator         = (*Store)(nil)
	_ RevisionRoutingDispatchCreator = (*Store)(nil)
	_ RoutingLeaseRenewer            = (*Store)(nil)
	_ DispatchLeaseRenewer           = (*Store)(nil)
)

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
	if err = validateSchemaV1(ctx, conn); err != nil {
		return fmt.Errorf("validate eventing schema v1: %w", err)
	}
	if version < 2 {
		if _, err = conn.ExecContext(ctx, schemaV2); err != nil {
			return fmt.Errorf("create eventing schema v2: %w", err)
		}
		if _, err = conn.ExecContext(ctx, "PRAGMA user_version = 2"); err != nil {
			return fmt.Errorf("record eventing schema v2: %w", err)
		}
	}
	if err = validateSchemaV2(ctx, conn); err != nil {
		return fmt.Errorf("validate eventing schema v2: %w", err)
	}
	if version < 3 {
		if _, err = conn.ExecContext(ctx, schemaV3); err != nil {
			return fmt.Errorf("create eventing schema v3: %w", err)
		}
		if _, err = conn.ExecContext(ctx, "PRAGMA user_version = 3"); err != nil {
			return fmt.Errorf("record eventing schema v3: %w", err)
		}
	}
	if err = validateSchemaV3(ctx, conn); err != nil {
		return fmt.Errorf("validate eventing schema v3: %w", err)
	}
	if version < 4 {
		if _, err = conn.ExecContext(ctx, schemaV4); err != nil {
			return fmt.Errorf("create eventing schema v4: %w", err)
		}
		if _, err = conn.ExecContext(ctx, "PRAGMA user_version = 4"); err != nil {
			return fmt.Errorf("record eventing schema v4: %w", err)
		}
	}
	if err = validateSchemaV4(ctx, conn); err != nil {
		return fmt.Errorf("validate eventing schema v4: %w", err)
	}
	if version < 5 {
		if _, err = conn.ExecContext(ctx, schemaV5); err != nil {
			return fmt.Errorf("create eventing schema v5: %w", err)
		}
		if _, err = conn.ExecContext(ctx, "PRAGMA user_version = 5"); err != nil {
			return fmt.Errorf("record eventing schema v5: %w", err)
		}
	}
	if err = validateSchemaV5(ctx, conn); err != nil {
		return fmt.Errorf("validate eventing schema v5: %w", err)
	}
	if version < 6 {
		if _, err = conn.ExecContext(ctx, schemaV6); err != nil {
			return fmt.Errorf("create eventing schema v6: %w", err)
		}
		if _, err = conn.ExecContext(ctx, "PRAGMA user_version = 6"); err != nil {
			return fmt.Errorf("record eventing schema v6: %w", err)
		}
	}
	if err = validateSchemaV6(ctx, conn); err != nil {
		return fmt.Errorf("validate eventing schema v6: %w", err)
	}
	if version < 7 {
		if _, err = conn.ExecContext(ctx, schemaV7); err != nil {
			return fmt.Errorf("create eventing schema v7: %w", err)
		}
		if err = validateSchemaV7(ctx, conn); err != nil {
			return fmt.Errorf("validate eventing schema v7: %w", err)
		}
		if err = backfillPRDevelopmentConversations(ctx, conn); err != nil {
			return fmt.Errorf(
				"backfill eventing schema v7 conversations: %w",
				err,
			)
		}
		if _, err = conn.ExecContext(ctx, "PRAGMA user_version = 7"); err != nil {
			return fmt.Errorf("record eventing schema v7: %w", err)
		}
	} else if err = validateSchemaV7(ctx, conn); err != nil {
		return fmt.Errorf("validate eventing schema v7: %w", err)
	}
	if version < 8 {
		if _, err = conn.ExecContext(ctx, schemaV8); err != nil {
			return fmt.Errorf("create eventing schema v8: %w", err)
		}
		if err = validateSchemaV8(ctx, conn); err != nil {
			return fmt.Errorf("validate eventing schema v8: %w", err)
		}
		if _, err = conn.ExecContext(ctx, "PRAGMA user_version = 8"); err != nil {
			return fmt.Errorf("record eventing schema v8: %w", err)
		}
	} else if err = validateSchemaV8(ctx, conn); err != nil {
		return fmt.Errorf("validate eventing schema v8: %w", err)
	}
	if version < 9 {
		if _, err = conn.ExecContext(ctx, schemaV9); err != nil {
			return fmt.Errorf("create eventing schema v9: %w", err)
		}
		if err = validateSchemaV9(ctx, conn); err != nil {
			return fmt.Errorf("validate eventing schema v9: %w", err)
		}
		if err = backfillPRDevelopmentThreads(ctx, conn); err != nil {
			return fmt.Errorf("backfill eventing schema v9 threads: %w", err)
		}
		if _, err = conn.ExecContext(ctx, "PRAGMA user_version = 9"); err != nil {
			return fmt.Errorf("record eventing schema v9: %w", err)
		}
	} else if err = validateSchemaV9(ctx, conn); err != nil {
		return fmt.Errorf("validate eventing schema v9: %w", err)
	}
	if version < 10 {
		if _, err = conn.ExecContext(ctx, schemaV10); err != nil {
			return fmt.Errorf("create eventing schema v10: %w", err)
		}
		if err = validateSchemaV10(ctx, conn); err != nil {
			return fmt.Errorf("validate eventing schema v10: %w", err)
		}
		if _, err = conn.ExecContext(ctx, "PRAGMA user_version = 10"); err != nil {
			return fmt.Errorf("record eventing schema v10: %w", err)
		}
	} else if err = validateSchemaV10(ctx, conn); err != nil {
		return fmt.Errorf("validate eventing schema v10: %w", err)
	}
	if version < 11 {
		if _, err = conn.ExecContext(ctx, schemaV11); err != nil {
			return fmt.Errorf("create eventing schema v11: %w", err)
		}
		if err = validateSchemaV11(ctx, conn); err != nil {
			return fmt.Errorf("validate eventing schema v11: %w", err)
		}
		if _, err = conn.ExecContext(ctx, "PRAGMA user_version = 11"); err != nil {
			return fmt.Errorf("record eventing schema v11: %w", err)
		}
	} else if err = validateSchemaV11(ctx, conn); err != nil {
		return fmt.Errorf("validate eventing schema v11: %w", err)
	}
	if version < 12 {
		if _, err = conn.ExecContext(ctx, schemaV12); err != nil {
			return fmt.Errorf("create eventing schema v12: %w", err)
		}
		if err = validateSchemaV12(ctx, conn); err != nil {
			return fmt.Errorf("validate eventing schema v12: %w", err)
		}
		if _, err = conn.ExecContext(ctx, "PRAGMA user_version = 12"); err != nil {
			return fmt.Errorf("record eventing schema v12: %w", err)
		}
	} else if err = validateSchemaV12(ctx, conn); err != nil {
		return fmt.Errorf("validate eventing schema v12: %w", err)
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit eventing migration: %w", err)
	}
	return nil
}

const (
	schemaV1EventInboxTable = `CREATE TABLE IF NOT EXISTS event_inbox (
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
);`
	schemaV1EventInboxDedupeIndex = `CREATE UNIQUE INDEX IF NOT EXISTS event_inbox_dedupe
	ON event_inbox(source, connector, dedupe_key)
	WHERE dedupe_key <> '';`
	schemaV1EventInboxRoutingClaimIndex = `CREATE INDEX IF NOT EXISTS event_inbox_routing_claim
	ON event_inbox(routing_status, routing_available_at, routing_lease_until, received_at, id);`
	schemaV1EventInboxListIndex = `CREATE INDEX IF NOT EXISTS event_inbox_list
	ON event_inbox(received_at DESC, id DESC);`
	schemaV1EventDispatchesTable = `CREATE TABLE IF NOT EXISTS event_dispatches (
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
);`
	schemaV1EventDispatchesClaimIndex = `CREATE INDEX IF NOT EXISTS event_dispatches_claim
	ON event_dispatches(status, available_at, lease_until, created_at, id);`
	schemaV1EventDispatchesListIndex = `CREATE INDEX IF NOT EXISTS event_dispatches_list
	ON event_dispatches(created_at DESC, id DESC);`
	schemaV1 = schemaV1EventInboxTable + "\n" +
		schemaV1EventInboxDedupeIndex + "\n" +
		schemaV1EventInboxRoutingClaimIndex + "\n" +
		schemaV1EventInboxListIndex + "\n" +
		schemaV1EventDispatchesTable + "\n" +
		schemaV1EventDispatchesClaimIndex + "\n" +
		schemaV1EventDispatchesListIndex
	schemaV2DispatchWorkflowRevisionsTable = `CREATE TABLE IF NOT EXISTS event_dispatch_workflow_revisions (
	dispatch_id TEXT PRIMARY KEY REFERENCES event_dispatches(id) ON DELETE CASCADE,
	workflow_revision TEXT NOT NULL CHECK (workflow_revision <> '')
);`
	schemaV2 = schemaV2DispatchWorkflowRevisionsTable
)

type schemaValidationError struct {
	object  string
	problem string
}

func (e *schemaValidationError) Error() string {
	return fmt.Sprintf("eventing schema object %q is invalid: %s", e.object, e.problem)
}

func (e *schemaValidationError) Unwrap() error {
	return ErrSchemaInvalid
}

type schemaTableSpec struct {
	name          string
	createSQL     string
	uniqueIndexes []schemaUniqueIndexSpec
}

type schemaIndexColumn struct {
	name      string
	desc      bool
	collation string
}

type schemaUniqueIndexSpec struct {
	name    string
	origin  string
	partial bool
	columns []schemaIndexColumn
}

type schemaIndexSpec struct {
	name      string
	createSQL string
}

type schemaIndexMetadata struct {
	name    string
	unique  bool
	partial bool
	origin  string
}

func validateSchemaV1(ctx context.Context, conn *sql.Conn) error {
	for _, table := range schemaV1TableSpecs() {
		if err := validateSchemaTable(ctx, conn, table); err != nil {
			return err
		}
	}
	for _, index := range schemaV1IndexSpecs() {
		if err := validateSchemaIndex(ctx, conn, index); err != nil {
			return err
		}
	}
	return nil
}

func validateSchemaV2(ctx context.Context, conn *sql.Conn) error {
	return validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "event_dispatch_workflow_revisions",
		createSQL: schemaV2DispatchWorkflowRevisionsTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{
				origin:  "pk",
				columns: []schemaIndexColumn{{name: "dispatch_id", collation: "BINARY"}},
			},
		},
	})
}

func validateSchemaTable(ctx context.Context, conn *sql.Conn, spec schemaTableSpec) error {
	if err := validateSchemaTableColumns(ctx, conn, spec.name); err != nil {
		return err
	}

	tableSQL, err := readSchemaSQL(ctx, conn, "table", spec.name)
	if err != nil {
		return err
	}
	if err = validateSchemaDefinition(spec.name, tableSQL, spec.createSQL); err != nil {
		return err
	}

	indexes, err := readSchemaIndexes(ctx, conn, spec.name)
	if err != nil {
		return err
	}
	if err = validateSchemaUniqueIndexes(
		ctx,
		conn,
		spec.name,
		indexes,
		spec.uniqueIndexes,
	); err != nil {
		return err
	}
	return nil
}

func validateSchemaTableColumns(
	ctx context.Context,
	conn *sql.Conn,
	table string,
) error {
	rows, err := conn.QueryContext(
		ctx,
		"PRAGMA table_xinfo("+quoteSQLiteStringLiteral(table)+")",
	)
	if err != nil {
		return fmt.Errorf("inspect eventing table %s columns: %w", table, err)
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var (
			position     int
			name         string
			dataType     string
			notNull      int
			defaultValue sql.NullString
			primaryKey   int
			hidden       int
		)
		if err := rows.Scan(
			&position,
			&name,
			&dataType,
			&notNull,
			&defaultValue,
			&primaryKey,
			&hidden,
		); err != nil {
			return fmt.Errorf("scan eventing table %s columns: %w", table, err)
		}
		found = true
		if hidden != 0 {
			return schemaErrorf(
				table,
				"column %q is hidden or generated (table_xinfo hidden=%d)",
				name,
				hidden,
			)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate eventing table %s columns: %w", table, err)
	}
	if !found {
		return schemaErrorf(table, "required table is missing")
	}
	return nil
}

func validateSchemaIndex(ctx context.Context, conn *sql.Conn, spec schemaIndexSpec) error {
	indexSQL, err := readSchemaSQL(ctx, conn, "index", spec.name)
	if err != nil {
		return err
	}
	if err = validateSchemaDefinition(spec.name, indexSQL, spec.createSQL); err != nil {
		return err
	}
	return nil
}

func validateSchemaUniqueIndexes(
	ctx context.Context,
	conn *sql.Conn,
	table string,
	indexes []schemaIndexMetadata,
	expected []schemaUniqueIndexSpec,
) error {
	matched := make([]bool, len(expected))
	for _, index := range indexes {
		if !index.unique {
			continue
		}
		columns, err := readSchemaIndexColumns(ctx, conn, index.name)
		if err != nil {
			return err
		}
		match := -1
		for expectedIndex, candidate := range expected {
			if matched[expectedIndex] ||
				(candidate.name != "" && candidate.name != index.name) ||
				candidate.origin != index.origin ||
				candidate.partial != index.partial ||
				!equalSchemaIndexColumns(candidate.columns, columns) {
				continue
			}
			match = expectedIndex
			break
		}
		if match < 0 {
			return schemaErrorf(
				index.name,
				"unexpected unique index on table %q with origin=%q partial=%t columns=%#v",
				table,
				index.origin,
				index.partial,
				columns,
			)
		}
		matched[match] = true
	}
	for expectedIndex, candidate := range expected {
		if matched[expectedIndex] {
			continue
		}
		object := candidate.name
		if object == "" {
			object = table
		}
		return schemaErrorf(
			object,
			"missing required unique index on table %q with origin=%q partial=%t columns=%#v",
			table,
			candidate.origin,
			candidate.partial,
			candidate.columns,
		)
	}
	return nil
}

func readSchemaIndexes(
	ctx context.Context,
	conn *sql.Conn,
	table string,
) ([]schemaIndexMetadata, error) {
	rows, err := conn.QueryContext(
		ctx,
		"PRAGMA index_list("+quoteSQLiteStringLiteral(table)+")",
	)
	if err != nil {
		return nil, fmt.Errorf("inspect eventing table %s indexes: %w", table, err)
	}
	defer rows.Close()

	var indexes []schemaIndexMetadata
	for rows.Next() {
		var (
			sequence int
			unique   int
			partial  int
			index    schemaIndexMetadata
		)
		if err := rows.Scan(&sequence, &index.name, &unique, &index.origin, &partial); err != nil {
			return nil, fmt.Errorf("scan eventing table %s indexes: %w", table, err)
		}
		index.unique = unique == 1
		index.partial = partial == 1
		indexes = append(indexes, index)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate eventing table %s indexes: %w", table, err)
	}
	return indexes, nil
}

func readSchemaIndexColumns(
	ctx context.Context,
	conn *sql.Conn,
	index string,
) ([]schemaIndexColumn, error) {
	rows, err := conn.QueryContext(
		ctx,
		"PRAGMA index_xinfo("+quoteSQLiteStringLiteral(index)+")",
	)
	if err != nil {
		return nil, fmt.Errorf("inspect eventing index %s columns: %w", index, err)
	}
	defer rows.Close()

	var columns []schemaIndexColumn
	for rows.Next() {
		var (
			sequence   int
			columnID   int
			descending int
			keyColumn  int
			name       sql.NullString
			collation  sql.NullString
		)
		if err := rows.Scan(
			&sequence,
			&columnID,
			&name,
			&descending,
			&collation,
			&keyColumn,
		); err != nil {
			return nil, fmt.Errorf("scan eventing index %s columns: %w", index, err)
		}
		if keyColumn == 0 {
			continue
		}
		if !name.Valid || columnID < 0 {
			return nil, schemaErrorf(index, "contains an unsupported expression column")
		}
		if sequence != len(columns) {
			return nil, schemaErrorf(
				index,
				"column %q has position %d, want %d",
				name.String,
				sequence,
				len(columns),
			)
		}
		columns = append(columns, schemaIndexColumn{
			name:      name.String,
			desc:      descending == 1,
			collation: collation.String,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate eventing index %s columns: %w", index, err)
	}
	return columns, nil
}

func readSchemaSQL(ctx context.Context, conn *sql.Conn, objectType, name string) (string, error) {
	var statement sql.NullString
	err := conn.QueryRowContext(ctx, `
		SELECT sql FROM sqlite_schema WHERE type = ? AND name = ?`,
		objectType,
		name,
	).Scan(&statement)
	if errors.Is(err, sql.ErrNoRows) {
		return "", schemaErrorf(name, "required %s is missing", objectType)
	}
	if err != nil {
		return "", fmt.Errorf("inspect eventing schema object %s: %w", name, err)
	}
	if !statement.Valid || strings.TrimSpace(statement.String) == "" {
		return "", schemaErrorf(name, "has no defining SQL")
	}
	return statement.String, nil
}

func equalSchemaIndexColumns(left, right []schemaIndexColumn) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func quoteSQLiteStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func validateSchemaDefinition(object, actual, expected string) error {
	canonicalActual, err := canonicalSchemaSQL(actual)
	if err != nil {
		return schemaErrorf(object, "cannot canonicalize defining SQL: %v", err)
	}
	canonicalExpected, err := canonicalSchemaSQL(expected)
	if err != nil {
		return fmt.Errorf("canonicalize required eventing schema object %s: %w", object, err)
	}
	if canonicalActual != canonicalExpected {
		return schemaErrorf(object, "definition differs from the required schema")
	}
	return nil
}

func canonicalSchemaSQL(statement string) (string, error) {
	if !utf8.ValidString(statement) {
		return "", fmt.Errorf("SQL is not valid UTF-8")
	}

	tokens := make([]string, 0, len(statement)/4)
	for offset := 0; offset < len(statement); {
		char, size := utf8.DecodeRuneInString(statement[offset:])
		if isSchemaSQLWhitespace(char) {
			offset += size
			continue
		}
		if strings.HasPrefix(statement[offset:], "--") {
			newline := strings.IndexByte(statement[offset+2:], '\n')
			if newline < 0 {
				offset = len(statement)
			} else {
				offset += 2 + newline
			}
			continue
		}
		if strings.HasPrefix(statement[offset:], "/*") {
			end := strings.Index(statement[offset+2:], "*/")
			if end < 0 {
				return "", fmt.Errorf("unterminated block comment")
			}
			offset += 2 + end + 2
			continue
		}

		if strings.ContainsRune("'\"`[", char) {
			token, next, err := readSchemaSQLQuotedToken(statement, offset)
			if err != nil {
				return "", err
			}
			tokens = append(tokens, token)
			offset = next
			continue
		}
		if isSchemaSQLWordRune(char) {
			var token strings.Builder
			for offset < len(statement) {
				char, size = utf8.DecodeRuneInString(statement[offset:])
				if !isSchemaSQLWordRune(char) {
					break
				}
				token.WriteRune(lowerSchemaSQLRune(char))
				offset += size
			}
			tokens = append(tokens, token.String())
			continue
		}

		operator := ""
		for _, candidate := range []string{
			"->>",
			"||", "->", "<=", ">=", "!=", "==", "<>", "<<", ">>",
		} {
			if strings.HasPrefix(statement[offset:], candidate) {
				operator = candidate
				break
			}
		}
		if operator == "" {
			operator = statement[offset : offset+size]
		}
		tokens = append(tokens, operator)
		offset += len(operator)
	}

	for len(tokens) > 0 && tokens[len(tokens)-1] == ";" {
		tokens = tokens[:len(tokens)-1]
	}
	if len(tokens) >= 5 &&
		tokens[0] == "create" &&
		(tokens[1] == "table" || tokens[1] == "index") &&
		tokens[2] == "if" &&
		tokens[3] == "not" &&
		tokens[4] == "exists" {
		tokens = append(tokens[:2], tokens[5:]...)
	} else if len(tokens) >= 6 &&
		tokens[0] == "create" &&
		tokens[1] == "unique" &&
		tokens[2] == "index" &&
		tokens[3] == "if" &&
		tokens[4] == "not" &&
		tokens[5] == "exists" {
		tokens = append(tokens[:3], tokens[6:]...)
	}

	var canonical strings.Builder
	for _, token := range tokens {
		canonical.WriteString(strconv.Itoa(len(token)))
		canonical.WriteByte(':')
		canonical.WriteString(token)
	}
	return canonical.String(), nil
}

func readSchemaSQLQuotedToken(statement string, offset int) (string, int, error) {
	start := offset
	delimiter := statement[offset]
	if delimiter == '[' {
		delimiter = ']'
	}
	offset++
	for offset < len(statement) {
		if statement[offset] != delimiter {
			offset++
			continue
		}
		offset++
		if offset < len(statement) && statement[offset] == delimiter {
			offset++
			continue
		}
		return statement[start:offset], offset, nil
	}
	return "", 0, fmt.Errorf("unterminated %q quoted token", string(statement[start]))
}

func isSchemaSQLWordRune(char rune) bool {
	return char == '_' ||
		char == '$' ||
		char >= utf8.RuneSelf ||
		char >= 'a' && char <= 'z' ||
		char >= 'A' && char <= 'Z' ||
		char >= '0' && char <= '9'
}

func isSchemaSQLWhitespace(char rune) bool {
	return char == ' ' ||
		char == '\t' ||
		char == '\n' ||
		char == '\f' ||
		char == '\r'
}

func lowerSchemaSQLRune(char rune) rune {
	if char >= 'A' && char <= 'Z' {
		return char + ('a' - 'A')
	}
	return char
}

func schemaErrorf(object, format string, args ...any) *schemaValidationError {
	return &schemaValidationError{
		object:  object,
		problem: fmt.Sprintf(format, args...),
	}
}

func schemaV1TableSpecs() []schemaTableSpec {
	return []schemaTableSpec{
		{
			name:      "event_inbox",
			createSQL: schemaV1EventInboxTable,
			uniqueIndexes: []schemaUniqueIndexSpec{
				{
					origin:  "pk",
					columns: []schemaIndexColumn{{name: "id", collation: "BINARY"}},
				},
				{
					name:    "event_inbox_dedupe",
					origin:  "c",
					partial: true,
					columns: []schemaIndexColumn{
						{name: "source", collation: "BINARY"},
						{name: "connector", collation: "BINARY"},
						{name: "dedupe_key", collation: "BINARY"},
					},
				},
			},
		},
		{
			name:      "event_dispatches",
			createSQL: schemaV1EventDispatchesTable,
			uniqueIndexes: []schemaUniqueIndexSpec{
				{
					origin:  "pk",
					columns: []schemaIndexColumn{{name: "id", collation: "BINARY"}},
				},
				{
					origin:  "u",
					columns: []schemaIndexColumn{{name: "run_id", collation: "BINARY"}},
				},
				{
					origin: "u",
					columns: []schemaIndexColumn{
						{name: "event_id", collation: "BINARY"},
						{name: "workflow_ref", collation: "BINARY"},
					},
				},
			},
		},
	}
}

func schemaV1IndexSpecs() []schemaIndexSpec {
	return []schemaIndexSpec{
		{
			name:      "event_inbox_dedupe",
			createSQL: schemaV1EventInboxDedupeIndex,
		},
		{
			name:      "event_inbox_routing_claim",
			createSQL: schemaV1EventInboxRoutingClaimIndex,
		},
		{
			name:      "event_inbox_list",
			createSQL: schemaV1EventInboxListIndex,
		},
		{
			name:      "event_dispatches_claim",
			createSQL: schemaV1EventDispatchesClaimIndex,
		},
		{
			name:      "event_dispatches_list",
			createSQL: schemaV1EventDispatchesListIndex,
		},
	}
}

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
	now, clockErr := s.currentTime()
	if clockErr != nil {
		return InsertResult{}, clockErr
	}
	event, normalizeErr := NormalizeEnvelope(input, now)
	if normalizeErr != nil {
		return InsertResult{}, normalizeErr
	}
	event, redactErr := s.redactor.RedactEnvelope(event)
	if redactErr != nil {
		return InsertResult{}, fmt.Errorf(
			"%w: redact payload: %v",
			ErrInvalidEnvelope,
			redactErr,
		)
	}
	if len(event.Payload) > s.maxPayloadBytes {
		return InsertResult{}, fmt.Errorf("%w after redaction: got %d bytes, maximum %d",
			ErrPayloadTooLarge, len(event.Payload), s.maxPayloadBytes)
	}
	if validationErr := event.Validate(); validationErr != nil {
		return InsertResult{}, validationErr
	}

	actorJSON, subjectJSON, attributesJSON, marshalErr := marshalEnvelopeParts(event)
	if marshalErr != nil {
		return InsertResult{}, marshalErr
	}
	result, execErr := s.db.ExecContext(ctx, `
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
	if execErr != nil {
		return InsertResult{}, fmt.Errorf("insert event: %w", s.dbError(execErr))
	}
	affected, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return InsertResult{}, fmt.Errorf("read insert result: %w", rowsErr)
	}
	if affected == 0 {
		stored, getErr := s.getByDedupe(ctx, event.Source, event.Connector, event.DedupeKey)
		if getErr != nil {
			return InsertResult{}, getErr
		}
		return InsertResult{Event: stored, Inserted: false}, nil
	}
	stored, getErr := s.Get(ctx, event.ID)
	if getErr != nil {
		return InsertResult{}, getErr
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

const eventMetadataColumns = `
	id, source, connector, event_type, actor_json, subject_json,
	occurred_at, received_at, length(payload_json), attributes_json, replay_of,
	routing_status, routing_lease_until, routing_attempts,
	routing_available_at, routing_last_error, routing_updated_at`

type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type rowsQueryer interface {
	rowQueryer
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
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
	plan, err := buildEventListPlan(eventColumns, filter)
	if err != nil {
		return EventPage{}, err
	}
	events, next, err := collectListPage(
		ctx,
		s,
		plan,
		scanStoredEvent,
		func(event StoredEvent) EventCursor {
			return EventCursor{
				ReceivedAt: event.Envelope.ReceivedAt,
				ID:         event.Envelope.ID,
			}
		},
		listErrorContext{
			query:   "list events",
			scan:    "scan event list",
			iterate: "iterate event list",
		},
	)
	if err != nil {
		return EventPage{}, err
	}
	return EventPage{Events: events, Next: next}, nil
}

// ListEvents is an explicit alias for List.
func (s *Store) ListEvents(ctx context.Context, filter EventFilter) (EventPage, error) {
	return s.List(ctx, filter)
}

// GetEventMetadata retrieves one event without selecting its payload,
// deduplication key, routing owner, or routing lease token.
func (s *Store) GetEventMetadata(
	ctx context.Context,
	id string,
) (StoredEventMetadata, error) {
	if err := s.ready(ctx); err != nil {
		return StoredEventMetadata{}, err
	}
	event, err := scanStoredEventMetadata(s.db.QueryRowContext(ctx, `
		SELECT `+eventMetadataColumns+` FROM event_inbox WHERE id = ?`,
		strings.TrimSpace(id),
	))
	if err != nil {
		return StoredEventMetadata{}, s.dbError(err)
	}
	return event, nil
}

// GetEventPayload retrieves an independent copy of the exact stored,
// already-redacted JSON payload.
func (s *Store) GetEventPayload(ctx context.Context, id string) ([]byte, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	var payload []byte
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT payload_json FROM event_inbox WHERE id = ?`,
		strings.TrimSpace(id),
	).Scan(&payload); err != nil {
		return nil, s.dbError(err)
	}
	return cloneBytes(payload), nil
}

// ListEventMetadata returns a newest-first page without selecting payload
// blobs or worker fencing credentials.
func (s *Store) ListEventMetadata(
	ctx context.Context,
	filter EventFilter,
) (EventMetadataPage, error) {
	if err := s.ready(ctx); err != nil {
		return EventMetadataPage{}, err
	}
	plan, err := buildEventListPlan(eventMetadataColumns, filter)
	if err != nil {
		return EventMetadataPage{}, err
	}
	events, next, err := collectListPage(
		ctx,
		s,
		plan,
		scanStoredEventMetadata,
		func(event StoredEventMetadata) EventCursor {
			return EventCursor{
				ReceivedAt: event.Envelope.ReceivedAt,
				ID:         event.Envelope.ID,
			}
		},
		listErrorContext{
			query:   "list event metadata",
			scan:    "scan event metadata list",
			iterate: "iterate event metadata list",
		},
	)
	if err != nil {
		return EventMetadataPage{}, err
	}
	return EventMetadataPage{Events: events, Next: next}, nil
}

type listPlan struct {
	query string
	args  []any
	limit int
}

type listFilter struct {
	column  string
	value   any
	enabled bool
}

type listPosition struct {
	at time.Time
	id string
}

func buildEventListPlan(columns string, filter EventFilter) (listPlan, error) {
	if filter.RoutingStatus != "" && !validRoutingStatus(filter.RoutingStatus) {
		return listPlan{}, fmt.Errorf(
			"%w: unknown routing status %q",
			ErrInvalidTransition,
			filter.RoutingStatus,
		)
	}
	if filter.After != nil {
		if err := validateDBTimestamp(
			"event cursor received_at",
			filter.After.ReceivedAt,
		); err != nil {
			return listPlan{}, err
		}
	}
	var after *listPosition
	if filter.After != nil {
		after = &listPosition{at: filter.After.ReceivedAt, id: filter.After.ID}
	}
	return buildListPlan(
		columns,
		"event_inbox",
		"received_at",
		[]listFilter{
			{column: "source", value: filter.Source, enabled: filter.Source != ""},
			{column: "connector", value: filter.Connector, enabled: filter.Connector != ""},
			{column: "event_type", value: filter.Type, enabled: filter.Type != ""},
			{
				column:  "routing_status",
				value:   filter.RoutingStatus,
				enabled: filter.RoutingStatus != "",
			},
		},
		after,
		filter.Limit,
	), nil
}

func buildListPlan(
	columns, table, positionColumn string,
	filters []listFilter,
	after *listPosition,
	requestedLimit int,
) listPlan {
	limit := normalizedLimit(requestedLimit)
	query := `SELECT ` + columns + ` FROM ` + table + ` WHERE 1 = 1`
	args := make([]any, 0, len(filters)+4)
	for _, filter := range filters {
		if !filter.enabled {
			continue
		}
		query += ` AND ` + filter.column + ` = ?`
		args = append(args, filter.value)
	}
	if after != nil {
		query += ` AND (` + positionColumn +
			` < ? OR (` + positionColumn + ` = ? AND id < ?))`
		position := toDBTime(after.at)
		args = append(args, position, position, after.id)
	}
	query += ` ORDER BY ` + positionColumn + ` DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	return listPlan{query: query, args: args, limit: limit}
}

type listErrorContext struct {
	query   string
	scan    string
	iterate string
}

func collectListPage[T, C any](
	ctx context.Context,
	store *Store,
	plan listPlan,
	scan func(rowScanner) (T, error),
	cursor func(T) C,
	errContext listErrorContext,
) ([]T, *C, error) {
	rows, err := store.db.QueryContext(ctx, plan.query, plan.args...)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", errContext.query, store.dbError(err))
	}
	defer rows.Close()

	events := make([]T, 0, plan.limit+1)
	for rows.Next() {
		event, scanErr := scan(rows)
		if scanErr != nil {
			return nil, nil, fmt.Errorf("%s: %w", errContext.scan, scanErr)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", errContext.iterate, store.dbError(err))
	}
	if len(events) > plan.limit {
		next := cursor(events[plan.limit-1])
		return events[:plan.limit], &next, nil
	}
	return events, nil, nil
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
	now, clockErr := s.currentTime()
	if clockErr != nil {
		return nil, clockErr
	}
	leaseUntil := now.Add(lease)
	if timestampErr := validateDBTimestamp(
		"routing lease deadline",
		leaseUntil,
	); timestampErr != nil {
		return nil, timestampErr
	}
	claimed := make([]StoredEvent, 0, limit)
	claimErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		ids, queryErr := queryIDs(ctx, conn, `
			SELECT id FROM event_inbox
			WHERE (routing_status = ? AND routing_available_at <= ?)
			   OR (routing_status = ? AND routing_lease_until <= ?)
			ORDER BY received_at ASC, id ASC
			LIMIT ?`,
			RoutingPending, toDBTime(now), RoutingClaimed, toDBTime(now), limit,
		)
		if queryErr != nil {
			return queryErr
		}
		for _, id := range ids {
			leaseToken, leaseErr := newLeaseToken(workerLabel)
			if leaseErr != nil {
				return leaseErr
			}
			if _, updateErr := conn.ExecContext(ctx, `
				UPDATE event_inbox
				SET routing_status = ?, routing_owner = ?, routing_lease_until = ?,
				    routing_attempts = routing_attempts + 1, routing_updated_at = ?
				WHERE id = ?`,
				RoutingClaimed, leaseToken, toDBTime(leaseUntil), toDBTime(now), id,
			); updateErr != nil {
				return updateErr
			}
			event, getErr := s.getWith(ctx, conn,
				`SELECT `+eventColumns+` FROM event_inbox WHERE id = ?`, id)
			if getErr != nil {
				return getErr
			}
			claimed = append(claimed, event)
		}
		return nil
	})
	if claimErr != nil {
		return nil, fmt.Errorf("claim routing work: %w", s.dbError(claimErr))
	}
	return claimed, nil
}

// RenewRoutingLease extends an owned, live routing claim from current store
// time. Ownership and liveness are checked atomically with the update.
func (s *Store) RenewRoutingLease(
	ctx context.Context,
	id, leaseToken string,
	lease time.Duration,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" ||
		strings.TrimSpace(leaseToken) == "" ||
		lease <= 0 {
		return fmt.Errorf("event ID, lease token, and positive lease are required")
	}
	now, err := s.currentTime()
	if err != nil {
		return err
	}
	leaseUntil := now.Add(lease)
	if timestampErr := validateDBTimestamp(
		"routing lease deadline",
		leaseUntil,
	); timestampErr != nil {
		return timestampErr
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE event_inbox
		SET routing_lease_until = ?, routing_updated_at = ?
		WHERE id = ? AND routing_status = ? AND routing_owner = ?
		  AND routing_lease_until > ?`,
		toDBTime(leaseUntil), toDBTime(now), id,
		RoutingClaimed, leaseToken, toDBTime(now),
	)
	if err != nil {
		return fmt.Errorf("renew event routing lease: %w", s.dbError(err))
	}
	return s.requireLeaseUpdate(ctx, result, "event_inbox", id)
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
	if timestampErr := validateDBTimestamp(
		"routing availability",
		availableAt,
	); timestampErr != nil {
		return timestampErr
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

// CreateDispatchForRoutingClaim idempotently creates one workflow delivery
// only while leaseToken still owns a live routing claim for the event.
func (s *Store) CreateDispatchForRoutingClaim(
	ctx context.Context,
	eventID, leaseToken, workflowRef string,
) (Dispatch, bool, error) {
	return s.createDispatchForRoutingClaim(
		ctx,
		eventID,
		leaseToken,
		workflowRef,
		"",
	)
}

// CreateRevisionedDispatchForRoutingClaim idempotently creates one workflow
// delivery and atomically persists the exact workflow content revision that
// matched under the live routing claim.
func (s *Store) CreateRevisionedDispatchForRoutingClaim(
	ctx context.Context,
	eventID, leaseToken, workflowRef, workflowRevision string,
) (Dispatch, bool, error) {
	workflowRevision = strings.TrimSpace(workflowRevision)
	if workflowRevision == "" {
		return Dispatch{}, false, fmt.Errorf("workflow revision is required")
	}
	if err := validateBoundedString(
		"workflow revision",
		workflowRevision,
		maxWorkflowRevisionLength,
	); err != nil {
		return Dispatch{}, false, err
	}
	return s.createDispatchForRoutingClaim(
		ctx,
		eventID,
		leaseToken,
		workflowRef,
		workflowRevision,
	)
}

func (s *Store) createDispatchForRoutingClaim(
	ctx context.Context,
	eventID, leaseToken, workflowRef, workflowRevision string,
) (Dispatch, bool, error) {
	if err := s.ready(ctx); err != nil {
		return Dispatch{}, false, err
	}
	eventID = strings.TrimSpace(eventID)
	leaseToken = strings.TrimSpace(leaseToken)
	workflowRef = strings.TrimSpace(workflowRef)
	if eventID == "" || leaseToken == "" || workflowRef == "" {
		return Dispatch{}, false, fmt.Errorf(
			"event ID, routing lease token, and workflow reference are required",
		)
	}
	if !validEventID(eventID) {
		return Dispatch{}, false, fmt.Errorf("event ID is invalid")
	}
	if err := validateBoundedString("workflow reference", workflowRef, maxWorkflowRefLength); err != nil {
		return Dispatch{}, false, err
	}
	dispatchID, runID := deterministicDispatchIDs(eventID, workflowRef)
	var (
		dispatch Dispatch
		created  bool
	)
	createErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, err := s.currentTime()
		if err != nil {
			return err
		}
		var live int
		err = conn.QueryRowContext(ctx, `
			SELECT 1 FROM event_inbox
			WHERE id = ? AND routing_status = ? AND routing_owner = ?
			  AND routing_lease_until > ?`,
			eventID, RoutingClaimed, leaseToken, toDBTime(now),
		).Scan(&live)
		if errors.Is(err, sql.ErrNoRows) {
			var exists int
			existsErr := conn.QueryRowContext(
				ctx,
				`SELECT 1 FROM event_inbox WHERE id = ?`,
				eventID,
			).Scan(&exists)
			if errors.Is(existsErr, sql.ErrNoRows) {
				return ErrNotFound
			}
			if existsErr != nil {
				return existsErr
			}
			return ErrStaleLease
		}
		if err != nil {
			return err
		}
		result, err := conn.ExecContext(ctx, `
			INSERT INTO event_dispatches (
				id, event_id, workflow_ref, run_id, status, available_at, created_at, updated_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(event_id, workflow_ref) DO NOTHING`,
			dispatchID, eventID, workflowRef, runID, DispatchPending,
			toDBTime(now), toDBTime(now), toDBTime(now),
		)
		if err != nil {
			return err
		}
		if workflowRevision != "" {
			if _, revisionErr := conn.ExecContext(ctx, `
				INSERT INTO event_dispatch_workflow_revisions (
					dispatch_id, workflow_revision
				)
				VALUES (?, ?)
				ON CONFLICT(dispatch_id) DO NOTHING`,
				dispatchID,
				workflowRevision,
			); revisionErr != nil {
				return revisionErr
			}
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		dispatch, err = scanDispatch(conn.QueryRowContext(ctx, `
			SELECT `+dispatchColumns+` FROM event_dispatches
			WHERE event_id = ? AND workflow_ref = ?`,
			eventID,
			workflowRef,
		))
		if err != nil {
			return err
		}
		created = affected == 1
		return nil
	})
	if createErr != nil {
		return Dispatch{}, false, fmt.Errorf(
			"create event dispatch for routing claim: %w",
			s.dbError(createErr),
		)
	}
	return dispatch, created, nil
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

// GetDispatchMetadata retrieves one dispatch without selecting worker
// owner/lease-token credentials.
func (s *Store) GetDispatchMetadata(
	ctx context.Context,
	id string,
) (DispatchMetadata, error) {
	if err := s.ready(ctx); err != nil {
		return DispatchMetadata{}, err
	}
	dispatch, err := scanDispatchMetadata(s.db.QueryRowContext(
		ctx,
		`SELECT `+dispatchMetadataColumns+` FROM event_dispatches WHERE id = ?`,
		id,
	))
	if err != nil {
		return DispatchMetadata{}, s.dbError(err)
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
	id, event_id, workflow_ref,
	COALESCE((SELECT workflow_revision FROM event_dispatch_workflow_revisions
		WHERE dispatch_id = event_dispatches.id), ''),
	run_id, status, owner, lease_until,
	available_at, attempts, last_error, created_at, updated_at, linked_at, finished_at`

const dispatchMetadataColumns = `
	id, event_id, workflow_ref,
	COALESCE((SELECT workflow_revision FROM event_dispatch_workflow_revisions
		WHERE dispatch_id = event_dispatches.id), ''),
	run_id, status, lease_until,
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
	now, clockErr := s.currentTime()
	if clockErr != nil {
		return nil, clockErr
	}
	leaseUntil := now.Add(lease)
	if timestampErr := validateDBTimestamp(
		"dispatch lease deadline",
		leaseUntil,
	); timestampErr != nil {
		return nil, timestampErr
	}
	claimed := make([]Dispatch, 0, limit)
	claimErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		ids, queryErr := queryIDs(ctx, conn, `
			SELECT id FROM event_dispatches
			WHERE (status = ? AND available_at <= ?)
			   OR (status IN (?, ?) AND lease_until <= ?)
			ORDER BY created_at ASC, id ASC
			LIMIT ?`,
			DispatchPending, toDBTime(now), DispatchClaimed, DispatchRunning,
			toDBTime(now), limit,
		)
		if queryErr != nil {
			return queryErr
		}
		for _, id := range ids {
			leaseToken, leaseErr := newLeaseToken(workerLabel)
			if leaseErr != nil {
				return leaseErr
			}
			if _, updateErr := conn.ExecContext(ctx, `
				UPDATE event_dispatches
				SET status = ?, owner = ?, lease_until = ?,
				    attempts = attempts + 1, updated_at = ?
				WHERE id = ?`,
				DispatchClaimed, leaseToken, toDBTime(leaseUntil), toDBTime(now), id,
			); updateErr != nil {
				return updateErr
			}
			dispatch, scanErr := scanDispatch(conn.QueryRowContext(ctx,
				`SELECT `+dispatchColumns+` FROM event_dispatches WHERE id = ?`, id))
			if scanErr != nil {
				return scanErr
			}
			claimed = append(claimed, dispatch)
		}
		return nil
	})
	if claimErr != nil {
		return nil, fmt.Errorf("claim dispatches: %w", s.dbError(claimErr))
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

// RenewDispatchLease extends an owned, live claimed or running dispatch lease
// from the current store time. Ownership and liveness are checked atomically
// with the update.
func (s *Store) RenewDispatchLease(
	ctx context.Context,
	id, leaseToken string,
	lease time.Duration,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" ||
		strings.TrimSpace(leaseToken) == "" ||
		lease <= 0 {
		return fmt.Errorf("dispatch ID, lease token, and positive lease are required")
	}
	now, err := s.currentTime()
	if err != nil {
		return err
	}
	leaseUntil := now.Add(lease)
	if timestampErr := validateDBTimestamp(
		"dispatch lease deadline",
		leaseUntil,
	); timestampErr != nil {
		return timestampErr
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE event_dispatches
		SET lease_until = ?, updated_at = ?
		WHERE id = ? AND status IN (?, ?) AND owner = ? AND lease_until > ?`,
		toDBTime(leaseUntil), toDBTime(now), id,
		DispatchClaimed, DispatchRunning, leaseToken, toDBTime(now),
	)
	if err != nil {
		return fmt.Errorf("renew event dispatch lease: %w", s.dbError(err))
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
	if timestampErr := validateDBTimestamp(
		"dispatch availability",
		availableAt,
	); timestampErr != nil {
		return timestampErr
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
	plan, err := buildDispatchListPlan(dispatchColumns, filter)
	if err != nil {
		return DispatchPage{}, err
	}
	dispatches, next, err := collectListPage(
		ctx,
		s,
		plan,
		scanDispatch,
		func(dispatch Dispatch) DispatchCursor {
			return DispatchCursor{CreatedAt: dispatch.CreatedAt, ID: dispatch.ID}
		},
		listErrorContext{
			query:   "list dispatches",
			scan:    "scan dispatch list",
			iterate: "iterate dispatch list",
		},
	)
	if err != nil {
		return DispatchPage{}, err
	}
	return DispatchPage{Dispatches: dispatches, Next: next}, nil
}

// ListDispatchMetadata returns a newest-first page without selecting worker
// owner/lease-token credentials.
func (s *Store) ListDispatchMetadata(
	ctx context.Context,
	filter DispatchFilter,
) (DispatchMetadataPage, error) {
	if err := s.ready(ctx); err != nil {
		return DispatchMetadataPage{}, err
	}
	plan, err := buildDispatchListPlan(dispatchMetadataColumns, filter)
	if err != nil {
		return DispatchMetadataPage{}, err
	}
	dispatches, next, err := collectListPage(
		ctx,
		s,
		plan,
		scanDispatchMetadata,
		func(dispatch DispatchMetadata) DispatchCursor {
			return DispatchCursor{CreatedAt: dispatch.CreatedAt, ID: dispatch.ID}
		},
		listErrorContext{
			query:   "list dispatches",
			scan:    "scan dispatch list",
			iterate: "iterate dispatch list",
		},
	)
	if err != nil {
		return DispatchMetadataPage{}, err
	}
	return DispatchMetadataPage{Dispatches: dispatches, Next: next}, nil
}

func buildDispatchListPlan(columns string, filter DispatchFilter) (listPlan, error) {
	if filter.Status != "" && !validDispatchStatus(filter.Status) {
		return listPlan{}, fmt.Errorf(
			"%w: unknown dispatch status %q",
			ErrInvalidTransition,
			filter.Status,
		)
	}
	if filter.After != nil {
		if err := validateDBTimestamp(
			"dispatch cursor created_at",
			filter.After.CreatedAt,
		); err != nil {
			return listPlan{}, err
		}
	}
	var after *listPosition
	if filter.After != nil {
		after = &listPosition{at: filter.After.CreatedAt, id: filter.After.ID}
	}
	return buildListPlan(
		columns,
		"event_dispatches",
		"created_at",
		[]listFilter{
			{column: "event_id", value: filter.EventID, enabled: filter.EventID != ""},
			{
				column:  "workflow_ref",
				value:   filter.WorkflowRef,
				enabled: filter.WorkflowRef != "",
			},
			{column: "status", value: filter.Status, enabled: filter.Status != ""},
		},
		after,
		filter.Limit,
	), nil
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
			  AND NOT EXISTS (
				SELECT 1 FROM pr_review_cases review_case
				WHERE review_case.event_id = e.id
			  )
			  AND NOT EXISTS (
				SELECT 1 FROM pr_development_cases development_case
				WHERE development_case.event_id = e.id
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

func queryIDs(
	ctx context.Context,
	conn *sql.Conn,
	query string,
	args ...any,
) ([]string, error) {
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, scanErr
		}
		ids = append(ids, id)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	return ids, nil
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
	Scan(dest ...any) error
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

func scanStoredEventMetadata(scanner rowScanner) (StoredEventMetadata, error) {
	var (
		event                   StoredEventMetadata
		actorJSON, subjectJSON  []byte
		attributesJSON          []byte
		occurredAt, leaseUntil  sql.NullInt64
		replayOf                sql.NullString
		payloadBytes            int64
		receivedAt, availableAt int64
		updatedAt               int64
	)
	err := scanner.Scan(
		&event.Envelope.ID,
		&event.Envelope.Source,
		&event.Envelope.Connector,
		&event.Envelope.Type,
		&actorJSON,
		&subjectJSON,
		&occurredAt,
		&receivedAt,
		&payloadBytes,
		&attributesJSON,
		&replayOf,
		&event.Routing.Status,
		&leaseUntil,
		&event.Routing.Attempts,
		&availableAt,
		&event.Routing.LastError,
		&updatedAt,
	)
	if err != nil {
		return StoredEventMetadata{}, err
	}
	if payloadBytes < 0 || payloadBytes > int64(^uint(0)>>1) {
		return StoredEventMetadata{}, fmt.Errorf(
			"stored event payload length is invalid",
		)
	}
	if string(actorJSON) != "null" {
		if err := json.Unmarshal(actorJSON, &event.Envelope.Actor); err != nil {
			return StoredEventMetadata{}, err
		}
	}
	if string(subjectJSON) != "null" {
		if err := json.Unmarshal(subjectJSON, &event.Envelope.Subject); err != nil {
			return StoredEventMetadata{}, err
		}
	}
	if string(attributesJSON) != "null" {
		if err := json.Unmarshal(attributesJSON, &event.Envelope.Attributes); err != nil {
			return StoredEventMetadata{}, err
		}
	}
	event.PayloadBytes = int(payloadBytes)
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
		&dispatch.WorkflowRevision,
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

func scanDispatchMetadata(scanner rowScanner) (DispatchMetadata, error) {
	var (
		dispatch                          DispatchMetadata
		leaseUntil, linkedAt, finishedAt  sql.NullInt64
		availableAt, createdAt, updatedAt int64
	)
	err := scanner.Scan(
		&dispatch.ID,
		&dispatch.EventID,
		&dispatch.WorkflowRef,
		&dispatch.WorkflowRevision,
		&dispatch.RunID,
		&dispatch.Status,
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
		return DispatchMetadata{}, err
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
