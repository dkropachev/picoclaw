package gitworkspace

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"time"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	inventoryDatabaseComponent = "git-workspace-inventory"
	inventoryDatabaseFilename  = "inventory.db"
	inventoryLegacyFilename    = "inventory.json"
	inventoryLegacySourceID    = "inventory-json-v1"
	inventoryLegacyArchive     = "git-workspaces-v1"
	inventoryLegacyMaxBytes    = int64(64 << 20)
	inventoryMaximumRows       = 1_000_000
)

const inventoryMetaSchema = `CREATE TABLE inventory_meta (
    singleton  INTEGER PRIMARY KEY CHECK(singleton = 1),
    generation INTEGER NOT NULL CHECK(generation >= 0)
) STRICT`

const inventoryRepositoriesSchema = `CREATE TABLE inventory_repositories (
    repository_id            TEXT PRIMARY KEY CHECK(length(CAST(repository_id AS BLOB)) BETWEEN 1 AND 256),
    remote_url               TEXT NOT NULL CHECK(length(CAST(remote_url AS BLOB)) BETWEEN 1 AND 4096),
    first_seen_unix_seconds  INTEGER,
    first_seen_nanosecond    INTEGER,
    last_seen_unix_seconds   INTEGER,
    last_seen_nanosecond     INTEGER,
    last_work_unix_seconds   INTEGER,
    last_work_nanosecond     INTEGER,
    CHECK((first_seen_unix_seconds IS NULL AND first_seen_nanosecond IS NULL) OR
          (first_seen_unix_seconds IS NOT NULL AND first_seen_nanosecond IS NOT NULL AND
           first_seen_unix_seconds BETWEEN -62167219200 AND 253402300799 AND first_seen_nanosecond BETWEEN 0 AND 999999999)),
    CHECK((last_seen_unix_seconds IS NULL AND last_seen_nanosecond IS NULL) OR
          (last_seen_unix_seconds IS NOT NULL AND last_seen_nanosecond IS NOT NULL AND
           last_seen_unix_seconds BETWEEN -62167219200 AND 253402300799 AND last_seen_nanosecond BETWEEN 0 AND 999999999)),
    CHECK((last_work_unix_seconds IS NULL AND last_work_nanosecond IS NULL) OR
          (last_work_unix_seconds IS NOT NULL AND last_work_nanosecond IS NOT NULL AND
           last_work_unix_seconds BETWEEN -62167219200 AND 253402300799 AND last_work_nanosecond BETWEEN 0 AND 999999999))
) STRICT`

const inventoryWorkspacesSchema = `CREATE TABLE inventory_workspaces (
    workspace_id                    TEXT PRIMARY KEY CHECK(length(CAST(workspace_id AS BLOB)) BETWEEN 1 AND 256),
    repository_id                   TEXT NOT NULL,
    remote_url                      TEXT NOT NULL CHECK(length(CAST(remote_url AS BLOB)) BETWEEN 1 AND 4096),
    upstream_url                    TEXT NOT NULL CHECK(length(CAST(upstream_url AS BLOB)) <= 4096),
    fresh_snapshot                  INTEGER NOT NULL CHECK(fresh_snapshot IN (0, 1)),
    source_ref                      TEXT NOT NULL CHECK(length(CAST(source_ref AS BLOB)) <= 4096),
    pinned_source_ref               TEXT NOT NULL CHECK(length(CAST(pinned_source_ref AS BLOB)) <= 4096),
    pinned_commit                   TEXT NOT NULL CHECK(length(CAST(pinned_commit AS BLOB)) <= 256),
    checkout_path                   TEXT NOT NULL CHECK(length(CAST(checkout_path AS BLOB)) BETWEEN 1 AND 16384),
    created_unix_seconds            INTEGER,
    created_nanosecond              INTEGER,
    updated_unix_seconds            INTEGER,
    updated_nanosecond              INTEGER,
    last_work_unix_seconds          INTEGER,
    last_work_nanosecond            INTEGER,
    last_cleaned_unix_seconds       INTEGER,
    last_cleaned_nanosecond         INTEGER,
    preserved_branch                TEXT NOT NULL CHECK(length(CAST(preserved_branch AS BLOB)) <= 4096),
    development_line_id             TEXT,
    reservation_rotation_count      INTEGER NOT NULL CHECK(reservation_rotation_count BETWEEN 0 AND 8192),
    reservation_rotation_tail_hash  TEXT NOT NULL CHECK(length(CAST(reservation_rotation_tail_hash AS BLOB)) <= 64),
    dropped_unix_seconds            INTEGER,
    dropped_nanosecond              INTEGER,
    FOREIGN KEY(repository_id) REFERENCES inventory_repositories(repository_id) ON DELETE CASCADE,
    FOREIGN KEY(development_line_id) REFERENCES inventory_development_lines(line_id) DEFERRABLE INITIALLY DEFERRED,
    CHECK((created_unix_seconds IS NULL AND created_nanosecond IS NULL) OR
          (created_unix_seconds IS NOT NULL AND created_nanosecond IS NOT NULL AND
           created_unix_seconds BETWEEN -62167219200 AND 253402300799 AND created_nanosecond BETWEEN 0 AND 999999999)),
    CHECK((updated_unix_seconds IS NULL AND updated_nanosecond IS NULL) OR
          (updated_unix_seconds IS NOT NULL AND updated_nanosecond IS NOT NULL AND
           updated_unix_seconds BETWEEN -62167219200 AND 253402300799 AND updated_nanosecond BETWEEN 0 AND 999999999)),
    CHECK((last_work_unix_seconds IS NULL AND last_work_nanosecond IS NULL) OR
          (last_work_unix_seconds IS NOT NULL AND last_work_nanosecond IS NOT NULL AND
           last_work_unix_seconds BETWEEN -62167219200 AND 253402300799 AND last_work_nanosecond BETWEEN 0 AND 999999999)),
    CHECK((last_cleaned_unix_seconds IS NULL AND last_cleaned_nanosecond IS NULL) OR
          (last_cleaned_unix_seconds IS NOT NULL AND last_cleaned_nanosecond IS NOT NULL AND
           last_cleaned_unix_seconds BETWEEN -62167219200 AND 253402300799 AND last_cleaned_nanosecond BETWEEN 0 AND 999999999)),
    CHECK((dropped_unix_seconds IS NULL AND dropped_nanosecond IS NULL) OR
          (dropped_unix_seconds IS NOT NULL AND dropped_nanosecond IS NOT NULL AND
           dropped_unix_seconds BETWEEN -62167219200 AND 253402300799 AND dropped_nanosecond BETWEEN 0 AND 999999999))
) STRICT`

const inventoryRepositoryWorkspaceOrderSchema = `CREATE TABLE inventory_repository_workspace_order (
    repository_id TEXT NOT NULL,
    ordinal       INTEGER NOT NULL CHECK(ordinal >= 0),
    workspace_id  TEXT NOT NULL,
    PRIMARY KEY(repository_id, ordinal),
    UNIQUE(repository_id, workspace_id),
    FOREIGN KEY(repository_id) REFERENCES inventory_repositories(repository_id) ON DELETE CASCADE,
    FOREIGN KEY(workspace_id) REFERENCES inventory_workspaces(workspace_id) ON DELETE CASCADE
) STRICT`

const inventoryWorkspaceLocksSchema = `CREATE TABLE inventory_workspace_locks (
    workspace_id              TEXT PRIMARY KEY,
    session_key               TEXT NOT NULL CHECK(length(CAST(session_key AS BLOB)) BETWEEN 1 AND 4096),
    agent_id                  TEXT NOT NULL CHECK(length(CAST(agent_id AS BLOB)) <= 256),
    locked_unix_seconds       INTEGER NOT NULL CHECK(locked_unix_seconds BETWEEN -62167219200 AND 253402300799),
    locked_nanosecond         INTEGER NOT NULL CHECK(locked_nanosecond BETWEEN 0 AND 999999999),
    heartbeat_unix_seconds    INTEGER NOT NULL CHECK(heartbeat_unix_seconds BETWEEN -62167219200 AND 253402300799),
    heartbeat_nanosecond      INTEGER NOT NULL CHECK(heartbeat_nanosecond BETWEEN 0 AND 999999999),
    FOREIGN KEY(workspace_id) REFERENCES inventory_workspaces(workspace_id) ON DELETE CASCADE
) STRICT`

const inventoryDevelopmentLinesSchema = `CREATE TABLE inventory_development_lines (
    line_id                         TEXT PRIMARY KEY CHECK(length(CAST(line_id AS BLOB)) BETWEEN 1 AND 256),
    workspace_id                    TEXT NOT NULL UNIQUE,
    repository_id                   TEXT NOT NULL,
    source_ref                      TEXT NOT NULL CHECK(length(CAST(source_ref AS BLOB)) BETWEEN 1 AND 4096),
    source_commit                   TEXT NOT NULL CHECK(length(CAST(source_commit AS BLOB)) BETWEEN 1 AND 256),
    branch                          TEXT NOT NULL CHECK(length(CAST(branch AS BLOB)) BETWEEN 1 AND 4096),
    tip                             TEXT NOT NULL CHECK(length(CAST(tip AS BLOB)) BETWEEN 1 AND 256),
    tree                            TEXT NOT NULL CHECK(length(CAST(tree AS BLOB)) BETWEEN 1 AND 256),
    version                         INTEGER NOT NULL CHECK(version BETWEEN 0 AND 8192),
    mutation_epoch                  INTEGER NOT NULL CHECK(mutation_epoch BETWEEN 1 AND 8193),
    state                           TEXT NOT NULL CHECK(state IN ('parked', 'mutating', 'suspended')),
    mutation_reservation_hash       TEXT NOT NULL CHECK(length(CAST(mutation_reservation_hash AS BLOB)) <= 64),
    mutation_agent_id               TEXT NOT NULL CHECK(length(CAST(mutation_agent_id AS BLOB)) <= 256),
    suspension_count                INTEGER NOT NULL CHECK(suspension_count BETWEEN 0 AND 8192),
    suspension_tail_hash            TEXT NOT NULL CHECK(length(CAST(suspension_tail_hash AS BLOB)) <= 64),
    pending_park_set                INTEGER NOT NULL CHECK(pending_park_set IN (0, 1)),
    pending_park_intent_id          TEXT NOT NULL CHECK(length(CAST(pending_park_intent_id AS BLOB)) <= 256),
    pending_park_reservation_hash   TEXT NOT NULL CHECK(length(CAST(pending_park_reservation_hash AS BLOB)) <= 64),
    pending_park_agent_id           TEXT NOT NULL CHECK(length(CAST(pending_park_agent_id AS BLOB)) <= 256),
    pending_park_epoch              INTEGER NOT NULL CHECK(pending_park_epoch BETWEEN 0 AND 8193),
    pending_park_expected_version   INTEGER NOT NULL CHECK(pending_park_expected_version BETWEEN 0 AND 8192),
    pending_park_previous_tip       TEXT NOT NULL CHECK(length(CAST(pending_park_previous_tip AS BLOB)) <= 256),
    pending_park_tip                TEXT NOT NULL CHECK(length(CAST(pending_park_tip AS BLOB)) <= 256),
    pending_park_tree               TEXT NOT NULL CHECK(length(CAST(pending_park_tree AS BLOB)) <= 256),
    pending_park_no_changes         INTEGER NOT NULL CHECK(pending_park_no_changes IN (0, 1)),
    last_park_intent_id             TEXT NOT NULL CHECK(length(CAST(last_park_intent_id AS BLOB)) <= 256),
    last_park_reservation_hash      TEXT NOT NULL CHECK(length(CAST(last_park_reservation_hash AS BLOB)) <= 64),
    last_park_agent_id              TEXT NOT NULL CHECK(length(CAST(last_park_agent_id AS BLOB)) <= 256),
    last_park_epoch                 INTEGER NOT NULL CHECK(last_park_epoch BETWEEN 0 AND 8192),
    last_park_expected_version      INTEGER NOT NULL CHECK(last_park_expected_version BETWEEN 0 AND 8192),
    last_park_previous_tip          TEXT NOT NULL CHECK(length(CAST(last_park_previous_tip AS BLOB)) <= 256),
    last_park_tip                   TEXT NOT NULL CHECK(length(CAST(last_park_tip AS BLOB)) <= 256),
    last_park_tree                  TEXT NOT NULL CHECK(length(CAST(last_park_tree AS BLOB)) <= 256),
    created_unix_seconds            INTEGER NOT NULL CHECK(created_unix_seconds BETWEEN -62167219200 AND 253402300799),
    created_nanosecond              INTEGER NOT NULL CHECK(created_nanosecond BETWEEN 0 AND 999999999),
    updated_unix_seconds            INTEGER NOT NULL CHECK(updated_unix_seconds BETWEEN -62167219200 AND 253402300799),
    updated_nanosecond              INTEGER NOT NULL CHECK(updated_nanosecond BETWEEN 0 AND 999999999),
    FOREIGN KEY(workspace_id) REFERENCES inventory_workspaces(workspace_id) ON DELETE CASCADE,
    FOREIGN KEY(repository_id) REFERENCES inventory_repositories(repository_id) ON DELETE CASCADE
) STRICT`

const inventoryRetiredReservationsSchema = `CREATE TABLE inventory_development_line_retired_reservations (
    line_id           TEXT NOT NULL,
    ordinal           INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 8191),
    reservation_hash  TEXT NOT NULL CHECK(length(CAST(reservation_hash AS BLOB)) = 64),
    PRIMARY KEY(line_id, ordinal),
    UNIQUE(line_id, reservation_hash),
    FOREIGN KEY(line_id) REFERENCES inventory_development_lines(line_id) ON DELETE CASCADE
) STRICT`

const inventorySuspensionsSchema = `CREATE TABLE inventory_development_line_suspensions (
    line_id                    TEXT NOT NULL,
    ordinal                    INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 8191),
    mode                       TEXT NOT NULL CHECK(mode IN ('candidate', 'commit_recovery')),
    intent_id                  TEXT NOT NULL CHECK(length(CAST(intent_id AS BLOB)) BETWEEN 1 AND 256),
    request_hash               TEXT NOT NULL CHECK(length(CAST(request_hash AS BLOB)) = 64),
    workspace_id               TEXT NOT NULL,
    repository_id              TEXT NOT NULL,
    source_ref                 TEXT NOT NULL CHECK(length(CAST(source_ref AS BLOB)) BETWEEN 1 AND 4096),
    source_commit              TEXT NOT NULL CHECK(length(CAST(source_commit AS BLOB)) BETWEEN 1 AND 256),
    version                    INTEGER NOT NULL CHECK(version BETWEEN 0 AND 8191),
    mutation_epoch             INTEGER NOT NULL CHECK(mutation_epoch BETWEEN 1 AND 8192),
    tip                        TEXT NOT NULL CHECK(length(CAST(tip AS BLOB)) BETWEEN 1 AND 256),
    tree                       TEXT NOT NULL CHECK(length(CAST(tree AS BLOB)) BETWEEN 1 AND 256),
    retired_reservation_hash   TEXT NOT NULL CHECK(length(CAST(retired_reservation_hash AS BLOB)) = 64),
    agent_id                   TEXT NOT NULL CHECK(length(CAST(agent_id AS BLOB)) BETWEEN 1 AND 256),
    candidate_tree             TEXT NOT NULL CHECK(length(CAST(candidate_tree AS BLOB)) BETWEEN 1 AND 256),
    candidate_digest           TEXT NOT NULL CHECK(length(CAST(candidate_digest AS BLOB)) = 64),
    changed_file_count         INTEGER NOT NULL CHECK(changed_file_count BETWEEN 0 AND 1000000),
    prepared_commit            TEXT NOT NULL CHECK(length(CAST(prepared_commit AS BLOB)) <= 256),
    prepared_tree              TEXT NOT NULL CHECK(length(CAST(prepared_tree AS BLOB)) <= 256),
    prepared_commit_applied    INTEGER NOT NULL CHECK(prepared_commit_applied IN (0, 1)),
    previous_record_hash       TEXT NOT NULL CHECK(length(CAST(previous_record_hash AS BLOB)) = 64),
    record_hash                TEXT NOT NULL CHECK(length(CAST(record_hash AS BLOB)) = 64),
    suspended_unix_seconds     INTEGER NOT NULL CHECK(suspended_unix_seconds BETWEEN -62167219200 AND 253402300799),
    suspended_nanosecond       INTEGER NOT NULL CHECK(suspended_nanosecond BETWEEN 0 AND 999999999),
    PRIMARY KEY(line_id, ordinal),
    UNIQUE(intent_id),
    UNIQUE(request_hash),
    UNIQUE(record_hash),
    FOREIGN KEY(line_id) REFERENCES inventory_development_lines(line_id) ON DELETE CASCADE,
    FOREIGN KEY(workspace_id) REFERENCES inventory_workspaces(workspace_id) ON DELETE CASCADE,
    FOREIGN KEY(repository_id) REFERENCES inventory_repositories(repository_id) ON DELETE CASCADE
) STRICT`

const inventoryRotationsSchema = `CREATE TABLE inventory_reservation_rotations (
    workspace_id                 TEXT NOT NULL,
    ordinal                      INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 8191),
    intent_id                    TEXT NOT NULL CHECK(length(CAST(intent_id AS BLOB)) BETWEEN 1 AND 256),
    line_id                      TEXT,
    repository_id                TEXT NOT NULL,
    source_ref                   TEXT NOT NULL CHECK(length(CAST(source_ref AS BLOB)) BETWEEN 1 AND 4096),
    source_commit                TEXT NOT NULL CHECK(length(CAST(source_commit AS BLOB)) BETWEEN 1 AND 256),
    version                      INTEGER NOT NULL CHECK(version BETWEEN 0 AND 8191),
    mutation_epoch               INTEGER NOT NULL CHECK(mutation_epoch BETWEEN 0 AND 8192),
    tip                          TEXT NOT NULL CHECK(length(CAST(tip AS BLOB)) <= 256),
    tree                         TEXT NOT NULL CHECK(length(CAST(tree AS BLOB)) <= 256),
    suspension_hash              TEXT NOT NULL CHECK(length(CAST(suspension_hash AS BLOB)) <= 64),
    previous_reservation_hash    TEXT NOT NULL CHECK(length(CAST(previous_reservation_hash AS BLOB)) = 64),
    replacement_reservation_hash TEXT NOT NULL CHECK(length(CAST(replacement_reservation_hash AS BLOB)) = 64),
    agent_id                     TEXT NOT NULL CHECK(length(CAST(agent_id AS BLOB)) BETWEEN 1 AND 256),
    previous_record_hash         TEXT NOT NULL CHECK(length(CAST(previous_record_hash AS BLOB)) = 64),
    record_hash                  TEXT NOT NULL CHECK(length(CAST(record_hash AS BLOB)) = 64),
    rotated_unix_seconds         INTEGER NOT NULL CHECK(rotated_unix_seconds BETWEEN -62167219200 AND 253402300799),
    rotated_nanosecond           INTEGER NOT NULL CHECK(rotated_nanosecond BETWEEN 0 AND 999999999),
    PRIMARY KEY(workspace_id, ordinal),
    UNIQUE(intent_id),
    UNIQUE(record_hash),
    FOREIGN KEY(workspace_id) REFERENCES inventory_workspaces(workspace_id) ON DELETE CASCADE,
    FOREIGN KEY(line_id) REFERENCES inventory_development_lines(line_id) ON DELETE CASCADE,
    FOREIGN KEY(repository_id) REFERENCES inventory_repositories(repository_id) ON DELETE CASCADE
) STRICT`

const inventoryHistorySchema = `CREATE TABLE inventory_history (
    stream            TEXT NOT NULL CHECK(stream IN ('public', 'development')),
    ordinal           INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 999),
    history_id        TEXT NOT NULL CHECK(length(CAST(history_id AS BLOB)) BETWEEN 1 AND 256),
    time_unix_seconds INTEGER NOT NULL CHECK(time_unix_seconds BETWEEN -62167219200 AND 253402300799),
    time_nanosecond   INTEGER NOT NULL CHECK(time_nanosecond BETWEEN 0 AND 999999999),
    action            TEXT NOT NULL CHECK(length(CAST(action AS BLOB)) BETWEEN 1 AND 256),
    repository_id     TEXT NOT NULL CHECK(length(CAST(repository_id AS BLOB)) <= 256),
    workspace_id      TEXT NOT NULL CHECK(length(CAST(workspace_id AS BLOB)) <= 256),
    session_key       TEXT NOT NULL CHECK(length(CAST(session_key AS BLOB)) <= 4096),
    agent_id          TEXT NOT NULL CHECK(length(CAST(agent_id AS BLOB)) <= 256),
    detail            TEXT NOT NULL CHECK(length(CAST(detail AS BLOB)) <= 16384),
    PRIMARY KEY(stream, ordinal)
) STRICT`

const inventoryWorkspacesRepositoryIndexSchema = `CREATE INDEX inventory_workspaces_repository_idx
    ON inventory_workspaces(repository_id, dropped_unix_seconds, workspace_id)`

const inventoryDevelopmentLinesStateIndexSchema = `CREATE INDEX inventory_development_lines_state_idx
    ON inventory_development_lines(state, updated_unix_seconds, line_id)`

const inventoryHistoryActionIndexSchema = `CREATE INDEX inventory_history_action_idx
    ON inventory_history(stream, action, time_unix_seconds, time_nanosecond, ordinal)`

var inventorySchemaObjects = []struct {
	typ, name, ddl string
}{
	{"table", "inventory_meta", inventoryMetaSchema},
	{"table", "inventory_repositories", inventoryRepositoriesSchema},
	{"table", "inventory_workspaces", inventoryWorkspacesSchema},
	{"table", "inventory_repository_workspace_order", inventoryRepositoryWorkspaceOrderSchema},
	{"table", "inventory_workspace_locks", inventoryWorkspaceLocksSchema},
	{"table", "inventory_development_lines", inventoryDevelopmentLinesSchema},
	{"table", "inventory_development_line_retired_reservations", inventoryRetiredReservationsSchema},
	{"table", "inventory_development_line_suspensions", inventorySuspensionsSchema},
	{"table", "inventory_reservation_rotations", inventoryRotationsSchema},
	{"table", "inventory_history", inventoryHistorySchema},
	{"index", "inventory_workspaces_repository_idx", inventoryWorkspacesRepositoryIndexSchema},
	{"index", "inventory_development_lines_state_idx", inventoryDevelopmentLinesStateIndexSchema},
	{"index", "inventory_history_action_idx", inventoryHistoryActionIndexSchema},
}

func (m *Manager) openInventoryDatabase(ctx context.Context) (*sql.DB, error) {
	if m == nil {
		return nil, errors.New("git workspace manager is not configured")
	}
	return sqlitestore.Open(ctx, m.databasePath(), sqlitestore.Options{
		Component: inventoryDatabaseComponent,
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{
				inventoryMetaSchema,
				`INSERT INTO inventory_meta(singleton, generation) VALUES (1, 0)`,
				inventoryRepositoriesSchema,
				inventoryWorkspacesSchema,
				inventoryRepositoryWorkspaceOrderSchema,
				inventoryWorkspaceLocksSchema,
				inventoryDevelopmentLinesSchema,
				inventoryRetiredReservationsSchema,
				inventorySuspensionsSchema,
				inventoryRotationsSchema,
				inventoryHistorySchema,
				inventoryWorkspacesRepositoryIndexSchema,
				inventoryDevelopmentLinesStateIndexSchema,
				inventoryHistoryActionIndexSchema,
			},
		}},
		Validate: validateInventorySchema,
		Legacy: &sqlitestore.LegacyOptions{
			SourceRoot:  m.rootDir,
			ArchiveRoot: filepath.Join(m.rootDir, "legacy-json", inventoryLegacyArchive),
			Sources: func() ([]sqlitestore.LegacySource, error) {
				return []sqlitestore.LegacySource{{
					ID: inventoryLegacySourceID, Relative: inventoryLegacyFilename,
					MaxBytes: inventoryLegacyMaxBytes,
				}}, nil
			},
			Import:   m.importLegacyInventory,
			MaxBytes: inventoryLegacyMaxBytes,
		},
	})
}

func validateInventorySchema(ctx context.Context, conn *sql.Conn) error {
	for _, object := range inventorySchemaObjects {
		if err := sqlitestore.ValidateSchemaObject(ctx, conn, object.typ, object.name, object.ddl); err != nil {
			return err
		}
	}
	for _, table := range []string{
		"inventory_meta", "inventory_repositories", "inventory_workspaces",
		"inventory_repository_workspace_order", "inventory_workspace_locks",
		"inventory_development_lines", "inventory_development_line_retired_reservations",
		"inventory_development_line_suspensions", "inventory_reservation_rotations",
		"inventory_history",
	} {
		if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, table); err != nil {
			return err
		}
	}
	allowed := make([]string, 0, len(inventorySchemaObjects)+3)
	for _, object := range inventorySchemaObjects {
		allowed = append(allowed, object.name)
	}
	allowed = append(allowed, "storage_imports", "storage_import_issues", "storage_imports_archive_status_idx")
	placeholders := "?"
	arguments := make([]any, 0, len(allowed))
	for index, name := range allowed {
		if index > 0 {
			placeholders += ",?"
		}
		arguments = append(arguments, name)
	}
	var unexpected int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
        WHERE name NOT LIKE 'sqlite_%' AND name NOT IN (`+placeholders+`)`, arguments...).Scan(&unexpected); err != nil {
		return err
	}
	if unexpected != 0 {
		return errors.New("git workspace inventory schema has unexpected objects")
	}
	for _, table := range []string{
		"inventory_repositories", "inventory_workspaces", "inventory_repository_workspace_order",
		"inventory_workspace_locks", "inventory_development_lines",
		"inventory_development_line_retired_reservations", "inventory_development_line_suspensions",
		"inventory_reservation_rotations", "inventory_history",
	} {
		var count int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			return err
		}
		if count > inventoryMaximumRows {
			return errors.New("git workspace inventory row limit exceeded")
		}
	}
	state, err := loadInventoryStateFrom(ctx, conn)
	if err != nil {
		return err
	}
	return validateDevelopmentLineInventory(state)
}

func (m *Manager) importLegacyInventory(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	var generation int64
	if err := conn.QueryRowContext(ctx, `SELECT generation FROM inventory_meta WHERE singleton = 1`).
		Scan(&generation); err != nil {
		return sqlitestore.ImportResult{}, err
	}
	if generation != 0 {
		return sqlitestore.ImportResult{
			Skipped: 1,
			Issues:  []sqlitestore.ImportIssue{{Code: "sqlite-authoritative", RecordDigest: input.Digest}},
		}, nil
	}
	state, err := m.decodeLegacyInventory(input.Data)
	if err != nil {
		return sqlitestore.ImportResult{}, errors.New("legacy git workspace inventory is malformed")
	}
	if err := rewriteInventoryState(ctx, conn, state, 0); err != nil {
		return sqlitestore.ImportResult{}, err
	}
	return sqlitestore.ImportResult{Imported: inventoryStateRecordCount(state)}, nil
}

func (m *Manager) decodeLegacyInventory(data []byte) (*storeState, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var state storeState
	if err := decoder.Decode(&state); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("legacy inventory contains trailing JSON")
		}
		return nil, err
	}
	if state.Repositories == nil {
		state.Repositories = map[string]*RepositoryRecord{}
	}
	if state.Workspaces == nil {
		state.Workspaces = map[string]*WorkspaceRecord{}
	}
	if state.DevelopmentLines == nil {
		state.DevelopmentLines = map[string]*developmentLineRecord{}
	}
	if state.PinnedReservationRotations == nil {
		state.PinnedReservationRotations = map[string][]pinnedReservationRotationRecord{}
	}
	if err := validateGitWorkspaceInventoryVersion(state.Version, stateVersion); err != nil {
		return nil, err
	}
	if state.Version < 3 &&
		(len(state.PinnedReservationRotations) != 0 || hasPinnedReservationRotationAnchors(&state)) {
		return nil, errors.New("pre-version-3 inventory contains rollback-fenced reservation rotations")
	}
	if state.Version < 4 && hasDevelopmentLineSuspensionEvidence(&state) {
		return nil, errors.New("pre-version-4 inventory contains rollback-fenced development line suspension evidence")
	}
	if state.Version < 2 {
		if len(state.DevelopmentLines) != 0 || len(state.DevelopmentLineHistory) != 0 {
			return nil, errors.New("legacy numeric inventory contains rollback-fenced controller state")
		}
		if err := m.migrateLegacyPinnedWorkspaces(&state); err != nil {
			return nil, err
		}
	}
	if state.Version < 3 {
		initializePinnedReservationRotationAnchors(&state)
	}
	if state.Version < 4 {
		initializeDevelopmentLineSuspensionAnchors(&state)
	}
	state.Version = stateVersion
	partitionDevelopmentLineHistory(&state)
	if err := validateInventoryRelationalState(&state); err != nil {
		return nil, err
	}
	if err := validateDevelopmentLineInventory(&state); err != nil {
		return nil, err
	}
	return &state, nil
}

func inventoryStateRecordCount(state *storeState) int {
	if state == nil {
		return 0
	}
	count := len(state.Repositories) + len(state.Workspaces) + len(state.DevelopmentLines) +
		len(state.History) + len(state.DevelopmentLineHistory)
	for _, repository := range state.Repositories {
		if repository != nil {
			count += len(repository.WorkspaceIDs)
		}
	}
	for _, line := range state.DevelopmentLines {
		if line != nil {
			count += len(line.RetiredReservationHashes) + len(line.Suspensions)
		}
	}
	for _, rotations := range state.PinnedReservationRotations {
		count += len(rotations)
	}
	return count
}

type inventoryQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func loadInventoryState(ctx context.Context, database *sql.DB) (*storeState, error) {
	if database == nil {
		return nil, errors.New("git workspace inventory database is unavailable")
	}
	return loadInventoryStateFrom(ctx, database)
}

func loadInventoryStateFrom(ctx context.Context, queryer inventoryQueryer) (*storeState, error) {
	state := &storeState{
		Version: stateVersion, Repositories: map[string]*RepositoryRecord{},
		Workspaces: map[string]*WorkspaceRecord{}, DevelopmentLines: map[string]*developmentLineRecord{},
		PinnedReservationRotations: map[string][]pinnedReservationRotationRecord{},
	}
	if err := queryer.QueryRowContext(ctx, `SELECT generation FROM inventory_meta WHERE singleton = 1`).
		Scan(&state.generation); err != nil {
		return nil, fmt.Errorf("read git workspace inventory generation: %w", err)
	}
	if err := loadInventoryRepositories(ctx, queryer, state); err != nil {
		return nil, err
	}
	if err := loadInventoryWorkspaces(ctx, queryer, state); err != nil {
		return nil, err
	}
	if err := loadInventoryDevelopmentLines(ctx, queryer, state); err != nil {
		return nil, err
	}
	if err := loadInventoryRotations(ctx, queryer, state); err != nil {
		return nil, err
	}
	if err := loadInventoryHistory(ctx, queryer, state); err != nil {
		return nil, err
	}
	if err := validateInventoryRelationalState(state); err != nil {
		return nil, err
	}
	if err := validateDevelopmentLineInventory(state); err != nil {
		return nil, err
	}
	return state, nil
}

func loadInventoryRepositories(ctx context.Context, queryer inventoryQueryer, state *storeState) error {
	rows, err := queryer.QueryContext(ctx, `SELECT repository_id, remote_url,
        first_seen_unix_seconds, first_seen_nanosecond, last_seen_unix_seconds,
        last_seen_nanosecond, last_work_unix_seconds, last_work_nanosecond
        FROM inventory_repositories ORDER BY repository_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var record RepositoryRecord
		var firstS, firstN, lastS, lastN, workS, workN sql.NullInt64
		if scanErr := rows.Scan(
			&record.ID,
			&record.RemoteURL,
			&firstS,
			&firstN,
			&lastS,
			&lastN,
			&workS,
			&workN,
		); scanErr != nil {
			return scanErr
		}
		record.FirstSeenAt = inventoryDecodeTime(firstS, firstN)
		record.LastSeenAt = inventoryDecodeTime(lastS, lastN)
		record.LastWorkAt = inventoryDecodeTime(workS, workN)
		state.Repositories[record.ID] = &record
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return rowsErr
	}
	orderRows, err := queryer.QueryContext(ctx, `SELECT repository_id, ordinal, workspace_id
        FROM inventory_repository_workspace_order ORDER BY repository_id, ordinal`)
	if err != nil {
		return err
	}
	defer orderRows.Close()
	for orderRows.Next() {
		var repositoryID, workspaceID string
		var ordinal int
		if scanErr := orderRows.Scan(&repositoryID, &ordinal, &workspaceID); scanErr != nil {
			return scanErr
		}
		repository := state.Repositories[repositoryID]
		if repository == nil || ordinal != len(repository.WorkspaceIDs) {
			return errors.New("git workspace repository ordering is invalid")
		}
		repository.WorkspaceIDs = append(repository.WorkspaceIDs, workspaceID)
	}
	return orderRows.Err()
}

func loadInventoryWorkspaces(ctx context.Context, queryer inventoryQueryer, state *storeState) error {
	rows, err := queryer.QueryContext(ctx, `SELECT workspace_id, repository_id, remote_url,
        upstream_url, fresh_snapshot, source_ref, pinned_source_ref, pinned_commit, checkout_path,
        created_unix_seconds, created_nanosecond, updated_unix_seconds, updated_nanosecond,
        last_work_unix_seconds, last_work_nanosecond, last_cleaned_unix_seconds,
        last_cleaned_nanosecond, preserved_branch, development_line_id,
        reservation_rotation_count, reservation_rotation_tail_hash,
        dropped_unix_seconds, dropped_nanosecond
        FROM inventory_workspaces ORDER BY workspace_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var record WorkspaceRecord
		var fresh int
		var developmentLine sql.NullString
		var createdS, createdN, updatedS, updatedN, workS, workN, cleanedS, cleanedN, droppedS, droppedN sql.NullInt64
		if scanErr := rows.Scan(
			&record.ID, &record.RepoID, &record.RemoteURL, &record.UpstreamURL, &fresh,
			&record.Ref, &record.PinnedSourceRef, &record.PinnedCommit, &record.Path,
			&createdS, &createdN, &updatedS, &updatedN, &workS, &workN, &cleanedS, &cleanedN,
			&record.PreservedBranch, &developmentLine, &record.PinnedReservationRotationCount,
			&record.PinnedReservationRotationTailHash, &droppedS, &droppedN,
		); scanErr != nil {
			return scanErr
		}
		record.FreshSnapshot = fresh == 1
		record.DevelopmentLineID = developmentLine.String
		record.CreatedAt = inventoryDecodeTime(createdS, createdN)
		record.UpdatedAt = inventoryDecodeTime(updatedS, updatedN)
		record.LastWorkAt = inventoryDecodeTime(workS, workN)
		record.LastCleanedAt = inventoryDecodeTime(cleanedS, cleanedN)
		if droppedS.Valid {
			value := inventoryDecodeTime(droppedS, droppedN)
			record.DroppedAt = &value
		}
		state.Workspaces[record.ID] = &record
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return rowsErr
	}
	lockRows, err := queryer.QueryContext(ctx, `SELECT workspace_id, session_key, agent_id,
        locked_unix_seconds, locked_nanosecond, heartbeat_unix_seconds, heartbeat_nanosecond
        FROM inventory_workspace_locks ORDER BY workspace_id`)
	if err != nil {
		return err
	}
	defer lockRows.Close()
	for lockRows.Next() {
		var workspaceID string
		var lock LockInfo
		var lockedS, lockedN, heartbeatS, heartbeatN int64
		if scanErr := lockRows.Scan(
			&workspaceID,
			&lock.SessionKey,
			&lock.AgentID,
			&lockedS,
			&lockedN,
			&heartbeatS,
			&heartbeatN,
		); scanErr != nil {
			return scanErr
		}
		workspace := state.Workspaces[workspaceID]
		if workspace == nil || workspace.LockedBy != nil {
			return errors.New("git workspace lock owner is invalid")
		}
		lock.LockedAt = time.Unix(lockedS, lockedN).UTC()
		lock.HeartbeatAt = time.Unix(heartbeatS, heartbeatN).UTC()
		workspace.LockedBy = &lock
	}
	return lockRows.Err()
}

func loadInventoryDevelopmentLines(ctx context.Context, queryer inventoryQueryer, state *storeState) error {
	rows, err := queryer.QueryContext(ctx, `SELECT line_id, workspace_id, repository_id, source_ref,
        source_commit, branch, tip, tree, version, mutation_epoch, state,
        mutation_reservation_hash, mutation_agent_id, suspension_count, suspension_tail_hash,
        pending_park_set, pending_park_intent_id, pending_park_reservation_hash,
        pending_park_agent_id, pending_park_epoch, pending_park_expected_version,
        pending_park_previous_tip, pending_park_tip, pending_park_tree, pending_park_no_changes,
        last_park_intent_id, last_park_reservation_hash, last_park_agent_id, last_park_epoch,
        last_park_expected_version, last_park_previous_tip, last_park_tip, last_park_tree,
        created_unix_seconds, created_nanosecond, updated_unix_seconds, updated_nanosecond
        FROM inventory_development_lines ORDER BY line_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var line developmentLineRecord
		var pending, noChanges int
		var createdS, createdN, updatedS, updatedN int64
		if scanErr := rows.Scan(
			&line.ID, &line.WorkspaceID, &line.RepoID, &line.SourceRef, &line.SourceCommit,
			&line.Branch, &line.Tip, &line.Tree, &line.Version, &line.MutationEpoch, &line.State,
			&line.MutationReservationHash, &line.MutationAgentID, &line.SuspensionCount,
			&line.SuspensionTailHash, &pending, &line.PendingParkIntentID,
			&line.PendingParkReservationHash, &line.PendingParkAgentID, &line.PendingParkEpoch,
			&line.PendingParkExpectedVersion, &line.PendingParkPreviousTip, &line.PendingParkTip,
			&line.PendingParkTree, &noChanges, &line.LastParkIntentID, &line.LastParkReservationHash,
			&line.LastParkAgentID, &line.LastParkEpoch, &line.LastParkExpectedVersion,
			&line.LastParkPreviousTip, &line.LastParkTip, &line.LastParkTree,
			&createdS, &createdN, &updatedS, &updatedN,
		); scanErr != nil {
			return scanErr
		}
		line.PendingParkSet = pending == 1
		line.PendingParkNoChanges = noChanges == 1
		line.CreatedAt = time.Unix(createdS, createdN).UTC()
		line.UpdatedAt = time.Unix(updatedS, updatedN).UTC()
		state.DevelopmentLines[line.ID] = &line
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return rowsErr
	}
	retiredRows, err := queryer.QueryContext(ctx, `SELECT line_id, ordinal, reservation_hash
        FROM inventory_development_line_retired_reservations ORDER BY line_id, ordinal`)
	if err != nil {
		return err
	}
	defer retiredRows.Close()
	for retiredRows.Next() {
		var lineID, hash string
		var ordinal int
		if scanErr := retiredRows.Scan(&lineID, &ordinal, &hash); scanErr != nil {
			return scanErr
		}
		line := state.DevelopmentLines[lineID]
		if line == nil || ordinal != len(line.RetiredReservationHashes) {
			return errors.New("git workspace retired reservation ordering is invalid")
		}
		line.RetiredReservationHashes = append(line.RetiredReservationHashes, hash)
	}
	if rowsErr := retiredRows.Err(); rowsErr != nil {
		return rowsErr
	}
	suspensionRows, err := queryer.QueryContext(ctx, `SELECT line_id, ordinal, mode, intent_id,
        request_hash, workspace_id, repository_id, source_ref, source_commit, version,
        mutation_epoch, tip, tree, retired_reservation_hash, agent_id, candidate_tree,
        candidate_digest, changed_file_count, prepared_commit, prepared_tree,
        prepared_commit_applied, previous_record_hash, record_hash,
        suspended_unix_seconds, suspended_nanosecond
        FROM inventory_development_line_suspensions ORDER BY line_id, ordinal`)
	if err != nil {
		return err
	}
	defer suspensionRows.Close()
	for suspensionRows.Next() {
		var lineID string
		var ordinal, applied int
		var record developmentLineSuspensionRecord
		var seconds, nanos int64
		if scanErr := suspensionRows.Scan(
			&lineID, &ordinal, &record.Mode, &record.IntentID, &record.RequestHash,
			&record.WorkspaceID, &record.RepoID, &record.SourceRef, &record.SourceCommit,
			&record.Version, &record.MutationEpoch, &record.Tip, &record.Tree,
			&record.RetiredReservationHash, &record.AgentID, &record.CandidateTree,
			&record.CandidateDigest, &record.ChangedFileCount, &record.PreparedCommit,
			&record.PreparedTree, &applied, &record.PreviousRecordHash, &record.RecordHash,
			&seconds, &nanos,
		); scanErr != nil {
			return scanErr
		}
		line := state.DevelopmentLines[lineID]
		if line == nil || ordinal != len(line.Suspensions) {
			return errors.New("git workspace suspension ordering is invalid")
		}
		record.LineID = lineID
		record.PreparedCommitApplied = applied == 1
		record.SuspendedAt = time.Unix(seconds, nanos).UTC()
		line.Suspensions = append(line.Suspensions, record)
	}
	return suspensionRows.Err()
}

func loadInventoryRotations(ctx context.Context, queryer inventoryQueryer, state *storeState) error {
	rows, err := queryer.QueryContext(ctx, `SELECT workspace_id, ordinal, intent_id, line_id,
        repository_id, source_ref, source_commit, version, mutation_epoch, tip, tree,
        suspension_hash, previous_reservation_hash, replacement_reservation_hash, agent_id,
        previous_record_hash, record_hash, rotated_unix_seconds, rotated_nanosecond
        FROM inventory_reservation_rotations ORDER BY workspace_id, ordinal`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var record pinnedReservationRotationRecord
		var lineID sql.NullString
		var ordinal int
		var seconds, nanos int64
		if scanErr := rows.Scan(
			&record.WorkspaceID, &ordinal, &record.IntentID, &lineID, &record.RepoID,
			&record.SourceRef, &record.SourceCommit, &record.Version, &record.MutationEpoch,
			&record.Tip, &record.Tree, &record.SuspensionHash, &record.PreviousReservationHash,
			&record.ReplacementReservationHash, &record.AgentID, &record.PreviousRecordHash,
			&record.RecordHash, &seconds, &nanos,
		); scanErr != nil {
			return scanErr
		}
		record.LineID = lineID.String
		record.RotatedAt = time.Unix(seconds, nanos).UTC()
		current := state.PinnedReservationRotations[record.WorkspaceID]
		if ordinal != len(current) {
			return errors.New("git workspace reservation rotation ordering is invalid")
		}
		state.PinnedReservationRotations[record.WorkspaceID] = append(current, record)
	}
	return rows.Err()
}

func loadInventoryHistory(ctx context.Context, queryer inventoryQueryer, state *storeState) error {
	rows, err := queryer.QueryContext(ctx, `SELECT stream, ordinal, history_id,
        time_unix_seconds, time_nanosecond, action, repository_id, workspace_id,
        session_key, agent_id, detail FROM inventory_history ORDER BY stream, ordinal`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var stream string
		var ordinal int
		var seconds, nanos int64
		var entry HistoryEntry
		if scanErr := rows.Scan(&stream, &ordinal, &entry.ID, &seconds, &nanos, &entry.Action,
			&entry.RepoID, &entry.WorkspaceID, &entry.SessionKey, &entry.AgentID, &entry.Detail); scanErr != nil {
			return scanErr
		}
		entry.Time = time.Unix(seconds, nanos).UTC()
		switch stream {
		case "public":
			if ordinal != len(state.History) {
				return errors.New("git workspace public history ordering is invalid")
			}
			state.History = append(state.History, entry)
		case "development":
			if ordinal != len(state.DevelopmentLineHistory) {
				return errors.New("git workspace development history ordering is invalid")
			}
			state.DevelopmentLineHistory = append(state.DevelopmentLineHistory, entry)
		default:
			return errors.New("git workspace history stream is invalid")
		}
	}
	return rows.Err()
}

func saveInventoryState(ctx context.Context, database *sql.DB, state *storeState) error {
	if database == nil || state == nil {
		return errors.New("git workspace inventory save is invalid")
	}
	if err := validateInventoryRelationalState(state); err != nil {
		return err
	}
	expected := state.generation
	if err := sqlitestore.Immediate(ctx, database, func(conn *sql.Conn) error {
		return rewriteInventoryState(ctx, conn, state, expected)
	}); err != nil {
		return fmt.Errorf("save git workspace inventory: %w", err)
	}
	state.generation = expected + 1
	return nil
}

func rewriteInventoryState(ctx context.Context, conn *sql.Conn, state *storeState, expected int64) error {
	if state == nil || state.Version != stateVersion || state.generation != expected {
		return errors.New("git workspace inventory generation is invalid")
	}
	var current int64
	if err := conn.QueryRowContext(ctx, `SELECT generation FROM inventory_meta WHERE singleton = 1`).
		Scan(&current); err != nil {
		return err
	}
	if current != expected {
		return errors.New("git workspace inventory generation conflict")
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM inventory_history`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM inventory_repositories`); err != nil {
		return err
	}
	if err := insertInventoryRepositories(ctx, conn, state); err != nil {
		return err
	}
	if err := insertInventoryWorkspaces(ctx, conn, state); err != nil {
		return err
	}
	if err := insertInventoryDevelopmentLines(ctx, conn, state); err != nil {
		return err
	}
	if err := insertInventoryRepositoryOrder(ctx, conn, state); err != nil {
		return err
	}
	if err := insertInventoryRotations(ctx, conn, state); err != nil {
		return err
	}
	if err := insertInventoryHistory(ctx, conn, state); err != nil {
		return err
	}
	result, err := conn.ExecContext(ctx, `UPDATE inventory_meta SET generation = generation + 1
        WHERE singleton = 1 AND generation = ?`, expected)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return errors.New("git workspace inventory generation conflict")
	}
	return nil
}

func insertInventoryRepositories(ctx context.Context, conn *sql.Conn, state *storeState) error {
	for _, id := range inventorySortedKeys(state.Repositories) {
		record := state.Repositories[id]
		if record == nil {
			return errors.New("git workspace repository record is nil")
		}
		firstS, firstN := inventoryEncodeTime(record.FirstSeenAt)
		lastS, lastN := inventoryEncodeTime(record.LastSeenAt)
		workS, workN := inventoryEncodeTime(record.LastWorkAt)
		if _, err := conn.ExecContext(ctx, `INSERT INTO inventory_repositories VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			record.ID, record.RemoteURL, firstS, firstN, lastS, lastN, workS, workN); err != nil {
			return err
		}
	}
	return nil
}

func insertInventoryWorkspaces(ctx context.Context, conn *sql.Conn, state *storeState) error {
	for _, id := range inventorySortedKeys(state.Workspaces) {
		record := state.Workspaces[id]
		if record == nil {
			return errors.New("git workspace record is nil")
		}
		createdS, createdN := inventoryEncodeTime(record.CreatedAt)
		updatedS, updatedN := inventoryEncodeTime(record.UpdatedAt)
		workS, workN := inventoryEncodeTime(record.LastWorkAt)
		cleanedS, cleanedN := inventoryEncodeTime(record.LastCleanedAt)
		var dropped time.Time
		if record.DroppedAt != nil {
			dropped = *record.DroppedAt
		}
		droppedS, droppedN := inventoryEncodeTime(dropped)
		var developmentLine any
		if record.DevelopmentLineID != "" {
			developmentLine = record.DevelopmentLineID
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO inventory_workspaces VALUES (
            ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.ID, record.RepoID, record.RemoteURL, record.UpstreamURL, inventoryBool(record.FreshSnapshot),
			record.Ref, record.PinnedSourceRef, record.PinnedCommit, record.Path,
			createdS, createdN, updatedS, updatedN, workS, workN, cleanedS, cleanedN,
			record.PreservedBranch, developmentLine, record.PinnedReservationRotationCount,
			record.PinnedReservationRotationTailHash, droppedS, droppedN,
		); err != nil {
			return err
		}
		if record.LockedBy != nil {
			lockedS, lockedN := inventoryRequiredTime(record.LockedBy.LockedAt)
			heartbeatS, heartbeatN := inventoryRequiredTime(record.LockedBy.HeartbeatAt)
			if _, err := conn.ExecContext(ctx, `INSERT INTO inventory_workspace_locks VALUES (?, ?, ?, ?, ?, ?, ?)`,
				record.ID, record.LockedBy.SessionKey, record.LockedBy.AgentID,
				lockedS, lockedN, heartbeatS, heartbeatN); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertInventoryRepositoryOrder(ctx context.Context, conn *sql.Conn, state *storeState) error {
	for _, id := range inventorySortedKeys(state.Repositories) {
		repository := state.Repositories[id]
		for ordinal, workspaceID := range repository.WorkspaceIDs {
			if _, err := conn.ExecContext(ctx, `INSERT INTO inventory_repository_workspace_order VALUES (?, ?, ?)`,
				repository.ID, ordinal, workspaceID); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertInventoryDevelopmentLines(ctx context.Context, conn *sql.Conn, state *storeState) error {
	for _, id := range inventorySortedKeys(state.DevelopmentLines) {
		line := state.DevelopmentLines[id]
		if line == nil {
			return errors.New("git workspace development line is nil")
		}
		createdS, createdN := inventoryRequiredTime(line.CreatedAt)
		updatedS, updatedN := inventoryRequiredTime(line.UpdatedAt)
		if _, err := conn.ExecContext(ctx, `INSERT INTO inventory_development_lines VALUES (
            ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
            ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			line.ID, line.WorkspaceID, line.RepoID, line.SourceRef, line.SourceCommit,
			line.Branch, line.Tip, line.Tree, line.Version, line.MutationEpoch, line.State,
			line.MutationReservationHash, line.MutationAgentID, line.SuspensionCount,
			line.SuspensionTailHash, inventoryBool(line.PendingParkSet), line.PendingParkIntentID,
			line.PendingParkReservationHash, line.PendingParkAgentID, line.PendingParkEpoch,
			line.PendingParkExpectedVersion, line.PendingParkPreviousTip, line.PendingParkTip,
			line.PendingParkTree, inventoryBool(line.PendingParkNoChanges), line.LastParkIntentID,
			line.LastParkReservationHash, line.LastParkAgentID, line.LastParkEpoch,
			line.LastParkExpectedVersion, line.LastParkPreviousTip, line.LastParkTip,
			line.LastParkTree, createdS, createdN, updatedS, updatedN,
		); err != nil {
			return err
		}
		for ordinal, hash := range line.RetiredReservationHashes {
			if _, err := conn.ExecContext(
				ctx,
				`INSERT INTO inventory_development_line_retired_reservations VALUES (?, ?, ?)`,
				line.ID,
				ordinal,
				hash,
			); err != nil {
				return err
			}
		}
		for ordinal, record := range line.Suspensions {
			seconds, nanos := inventoryRequiredTime(record.SuspendedAt)
			if _, err := conn.ExecContext(ctx, `INSERT INTO inventory_development_line_suspensions VALUES (
                ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				line.ID, ordinal, record.Mode, record.IntentID, record.RequestHash,
				record.WorkspaceID, record.RepoID, record.SourceRef, record.SourceCommit,
				record.Version, record.MutationEpoch, record.Tip, record.Tree,
				record.RetiredReservationHash, record.AgentID, record.CandidateTree,
				record.CandidateDigest, record.ChangedFileCount, record.PreparedCommit,
				record.PreparedTree, inventoryBool(record.PreparedCommitApplied),
				record.PreviousRecordHash, record.RecordHash, seconds, nanos,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertInventoryRotations(ctx context.Context, conn *sql.Conn, state *storeState) error {
	for _, workspaceID := range inventorySortedKeys(state.PinnedReservationRotations) {
		for ordinal, record := range state.PinnedReservationRotations[workspaceID] {
			seconds, nanos := inventoryRequiredTime(record.RotatedAt)
			var lineID any
			if record.LineID != "" {
				lineID = record.LineID
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO inventory_reservation_rotations VALUES (
                ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				record.WorkspaceID, ordinal, record.IntentID, lineID, record.RepoID,
				record.SourceRef, record.SourceCommit, record.Version, record.MutationEpoch,
				record.Tip, record.Tree, record.SuspensionHash, record.PreviousReservationHash,
				record.ReplacementReservationHash, record.AgentID, record.PreviousRecordHash,
				record.RecordHash, seconds, nanos,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertInventoryHistory(ctx context.Context, conn *sql.Conn, state *storeState) error {
	for _, stream := range []struct {
		name    string
		entries []HistoryEntry
	}{{"public", state.History}, {"development", state.DevelopmentLineHistory}} {
		for ordinal, entry := range stream.entries {
			seconds, nanos := inventoryRequiredTime(entry.Time)
			if _, err := conn.ExecContext(ctx, `INSERT INTO inventory_history VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				stream.name, ordinal, entry.ID, seconds, nanos, entry.Action, entry.RepoID,
				entry.WorkspaceID, entry.SessionKey, entry.AgentID, entry.Detail); err != nil {
				return err
			}
		}
	}
	return nil
}

func inventorySortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func inventoryEncodeTime(value time.Time) (any, any) {
	if value.IsZero() {
		return nil, nil
	}
	seconds, nanos := inventoryRequiredTime(value)
	return seconds, nanos
}

func inventoryRequiredTime(value time.Time) (int64, int64) {
	value = value.UTC()
	return value.Unix(), int64(value.Nanosecond())
}

func inventoryDecodeTime(seconds, nanos sql.NullInt64) time.Time {
	if !seconds.Valid || !nanos.Valid {
		return time.Time{}
	}
	return time.Unix(seconds.Int64, nanos.Int64).UTC()
}

func inventoryBool(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validateInventoryRelationalState(state *storeState) error {
	if state == nil || state.Repositories == nil || state.Workspaces == nil ||
		state.DevelopmentLines == nil || state.PinnedReservationRotations == nil {
		return errors.New("git workspace relational inventory is incomplete")
	}
	total := len(state.Repositories) + len(state.Workspaces) + len(state.DevelopmentLines) +
		len(state.History) + len(state.DevelopmentLineHistory)
	if len(state.History) > historyLimit || len(state.DevelopmentLineHistory) > historyLimit {
		return errors.New("git workspace history limit exceeded")
	}
	owners := make(map[string]string, len(state.Workspaces))
	for repositoryID, repository := range state.Repositories {
		if repository == nil || len(repository.WorkspaceIDs) > inventoryMaximumRows {
			return errors.New("git workspace repository relationship is invalid")
		}
		total += len(repository.WorkspaceIDs)
		for _, workspaceID := range repository.WorkspaceIDs {
			workspace := state.Workspaces[workspaceID]
			if workspace == nil || workspace.RepoID != repositoryID {
				return errors.New("git workspace repository relationship is invalid")
			}
			if _, duplicate := owners[workspaceID]; duplicate {
				return errors.New("git workspace repository relationship is duplicated")
			}
			owners[workspaceID] = repositoryID
		}
	}
	for workspaceID := range state.Workspaces {
		if _, owned := owners[workspaceID]; !owned {
			return errors.New("git workspace repository relationship is missing")
		}
	}
	for _, line := range state.DevelopmentLines {
		if line != nil {
			total += len(line.RetiredReservationHashes) + len(line.Suspensions)
		}
	}
	for _, rotations := range state.PinnedReservationRotations {
		total += len(rotations)
	}
	if total > inventoryMaximumRows {
		return errors.New("git workspace inventory aggregate row limit exceeded")
	}
	return nil
}
