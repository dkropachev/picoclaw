package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	runtimeDatabaseFilename    = "runtime.db"
	runtimeDatabaseComponent   = "runtime-state"
	runtimeLegacyArchiveLabel  = "runtime-state-v1"
	runtimeLegacyMaxBytes      = int64(1 << 20)
	runtimeRootSourceID        = "workspace-root-state-v0"
	runtimeDirectorySourceID   = "state-directory-v1"
	runtimeRootSourcePriority  = 1
	runtimeStateSourcePriority = 2
	runtimeSQLitePriority      = 3
)

var errRuntimeStateVersionChanged = errors.New("runtime-state version changed during update")

var absoluteRuntimePath = filepath.Abs

const runtimeStateSchema = `CREATE TABLE runtime_state (
    id                       INTEGER PRIMARY KEY CHECK(id = 1),
    last_channel             TEXT NOT NULL DEFAULT '',
    last_chat_id             TEXT NOT NULL DEFAULT '',
    updated_at_unix_seconds  INTEGER,
    updated_at_nanosecond    INTEGER,
    version                  INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    origin_priority          INTEGER NOT NULL DEFAULT 0 CHECK(origin_priority BETWEEN 0 AND 3),
    CHECK(length(CAST(last_channel AS BLOB)) <= 65536),
    CHECK(length(CAST(last_chat_id AS BLOB)) <= 65536),
    CHECK(instr(last_channel, char(0)) = 0),
    CHECK(instr(last_chat_id, char(0)) = 0),
    CHECK(
        (updated_at_unix_seconds IS NULL AND updated_at_nanosecond IS NULL)
        OR (
            updated_at_unix_seconds BETWEEN -62167219200 AND 253402300799
            AND updated_at_nanosecond IS NOT NULL
            AND updated_at_nanosecond BETWEEN 0 AND 999999999
        )
    )
) STRICT`

const insertDefaultRuntimeStateSQL = `INSERT INTO runtime_state(id) VALUES (1)`

const selectRuntimeStateSQL = `SELECT
    last_channel,
    last_chat_id,
    updated_at_unix_seconds,
    updated_at_nanosecond,
    version,
    origin_priority
FROM runtime_state
WHERE id = 1`

const updateLastChannelSQL = `UPDATE runtime_state SET
    last_channel = ?,
    updated_at_unix_seconds = ?,
    updated_at_nanosecond = ?,
    version = version + 1,
    origin_priority = 3
WHERE id = 1 AND version = ?`

const updateLastChatIDSQL = `UPDATE runtime_state SET
    last_chat_id = ?,
    updated_at_unix_seconds = ?,
    updated_at_nanosecond = ?,
    version = version + 1,
    origin_priority = 3
WHERE id = 1 AND version = ?`

const importRuntimeStateSQL = `UPDATE runtime_state SET
    last_channel = ?,
    last_chat_id = ?,
    updated_at_unix_seconds = ?,
    updated_at_nanosecond = ?,
    version = version + 1,
    origin_priority = ?
WHERE id = 1 AND version = ?`

func resolveRuntimeDatabasePath(workspace string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		return "", errors.New("workspace path is required")
	}
	workspacePath, err := absoluteRuntimePath(filepath.Clean(workspace))
	if err != nil {
		return "", err
	}
	return filepath.Join(workspacePath, "state", runtimeDatabaseFilename), nil
}

func runtimeStoreOptions(workspace string) (sqlitestore.Options, error) {
	options := sqlitestore.Options{
		Component: runtimeDatabaseComponent,
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{
				runtimeStateSchema,
				insertDefaultRuntimeStateSQL,
			},
		}},
		Validate: validateRuntimeSchema,
	}
	hasLegacy, err := runtimeLegacyStatePresent(workspace)
	if err != nil {
		return sqlitestore.Options{}, err
	}
	if hasLegacy {
		options.Legacy = &sqlitestore.LegacyOptions{
			SourceRoot: workspace,
			ArchiveRoot: filepath.Join(
				workspace,
				"state",
				"legacy-json",
				runtimeLegacyArchiveLabel,
			),
			Sources: func() ([]sqlitestore.LegacySource, error) {
				return []sqlitestore.LegacySource{
					{
						ID:       runtimeRootSourceID,
						Relative: "state.json",
						MaxBytes: runtimeLegacyMaxBytes,
					},
					{
						ID:       runtimeDirectorySourceID,
						Relative: "state/state.json",
						MaxBytes: runtimeLegacyMaxBytes,
					},
				}, nil
			},
			Import:        importLegacyRuntimeState,
			MaxBytes:      runtimeLegacyMaxBytes,
			MaxSources:    2,
			MaxTotalBytes: runtimeLegacyMaxBytes * 2,
		}
	}
	return options, nil
}

func runtimeLegacyStatePresent(workspace string) (bool, error) {
	for _, relative := range []string{
		"state.json",
		"state/state.json",
		"state/legacy-json/runtime-state-v1/state.json",
		"state/legacy-json/runtime-state-v1/state/state.json",
	} {
		_, err := os.Lstat(filepath.Join(workspace, filepath.FromSlash(relative)))
		if err == nil {
			return true, nil
		}
		if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
}

func validateRuntimeSchema(ctx context.Context, conn *sql.Conn) error {
	if err := sqlitestore.ValidateSchemaObject(
		ctx,
		conn,
		"table",
		"runtime_state",
		runtimeStateSchema,
	); err != nil {
		return err
	}
	if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, "runtime_state"); err != nil {
		return err
	}
	var unexpected int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
        WHERE name NOT LIKE 'sqlite_%'
          AND name NOT IN (
              'runtime_state',
              'storage_imports',
              'storage_import_issues',
              'storage_imports_archive_status_idx'
          )`).Scan(&unexpected); err != nil {
		return err
	}
	if unexpected != 0 {
		return errors.New("runtime-state schema has unexpected objects")
	}
	var rows int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_state WHERE id = 1`).Scan(&rows); err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("runtime-state singleton row is missing")
	}
	return nil
}

func importLegacyRuntimeState(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	priority, ok := runtimeSourcePriority(input.ID)
	if !ok {
		return sqlitestore.ImportResult{}, errors.New("legacy runtime-state source identity is invalid")
	}
	candidate, valid := decodeLegacyRuntimeState(input.Data)
	if !valid {
		return skippedRuntimeImport("malformed-json", input.Digest), nil
	}
	if !validLegacyRuntimeState(candidate) {
		return skippedRuntimeImport("invalid-state", input.Digest), nil
	}
	seconds, nanoseconds, valid := legacyRuntimeTimestampValues(candidate.Timestamp)
	if !valid {
		return skippedRuntimeImport("invalid-state", input.Digest), nil
	}
	current, version, currentPriority, err := scanRuntimeState(
		conn.QueryRowContext(ctx, selectRuntimeStateSQL),
	)
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	if currentPriority == runtimeSQLitePriority {
		return skippedRuntimeImport("sqlite-authoritative", input.Digest), nil
	}
	if currentPriority >= priority {
		return skippedRuntimeImport("lower-priority", input.Digest), nil
	}
	result := sqlitestore.ImportResult{Imported: 1}
	if currentPriority > 0 && !sameRuntimeState(current, candidate) {
		result.Skipped = 1
		result.Issues = []sqlitestore.ImportIssue{{
			Code:         "source-conflict",
			RecordDigest: input.Digest,
		}}
	}
	executionResult, err := conn.ExecContext(
		ctx,
		importRuntimeStateSQL,
		candidate.LastChannel,
		candidate.LastChatID,
		seconds,
		nanoseconds,
		priority,
		version,
	)
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	changed, err := executionResult.RowsAffected()
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	if changed != 1 {
		return sqlitestore.ImportResult{}, errRuntimeStateVersionChanged
	}
	return result, nil
}

func decodeLegacyRuntimeState(data []byte) (State, bool) {
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, false
	}
	return state, true
}

func validLegacyRuntimeState(state State) bool {
	return validateRuntimeStateValue(state.LastChannel) == nil &&
		validateRuntimeStateValue(state.LastChatID) == nil
}

func legacyRuntimeTimestampValues(timestamp time.Time) (any, any, bool) {
	seconds, nanoseconds, err := runtimeTimestampValues(timestamp)
	return seconds, nanoseconds, err == nil
}

func runtimeSourcePriority(sourceID string) (int, bool) {
	switch sourceID {
	case runtimeRootSourceID:
		return runtimeRootSourcePriority, true
	case runtimeDirectorySourceID:
		return runtimeStateSourcePriority, true
	default:
		return 0, false
	}
}

func skippedRuntimeImport(code string, digest [sha256.Size]byte) sqlitestore.ImportResult {
	return sqlitestore.ImportResult{
		Skipped: 1,
		Issues: []sqlitestore.ImportIssue{{
			Code:         code,
			RecordDigest: digest,
		}},
	}
}

type runtimeStateRowScanner interface {
	Scan(dest ...any) error
}

func scanRuntimeState(scanner runtimeStateRowScanner) (State, int64, int, error) {
	var (
		state       State
		seconds     sql.NullInt64
		nanoseconds sql.NullInt64
		version     int64
		priority    int
	)
	if err := scanner.Scan(
		&state.LastChannel,
		&state.LastChatID,
		&seconds,
		&nanoseconds,
		&version,
		&priority,
	); err != nil {
		return State{}, 0, 0, err
	}
	if seconds.Valid != nanoseconds.Valid {
		return State{}, 0, 0, errors.New("runtime-state timestamp columns are inconsistent")
	}
	if seconds.Valid {
		state.Timestamp = time.Unix(seconds.Int64, nanoseconds.Int64).UTC()
	}
	return state, version, priority, nil
}

func runtimeTimestampValues(timestamp time.Time) (any, any, error) {
	if timestamp.IsZero() {
		return nil, nil, nil
	}
	if timestamp.Year() < 0 || timestamp.Year() > 9999 {
		return nil, nil, errors.New("runtime-state timestamp is outside the supported range")
	}
	return timestamp.Unix(), int64(timestamp.Nanosecond()), nil
}

func sameRuntimeState(left, right State) bool {
	return left.LastChannel == right.LastChannel &&
		left.LastChatID == right.LastChatID &&
		left.Timestamp.Equal(right.Timestamp)
}

func runtimeDatabaseLockDirectory(databasePath string) string {
	return databasePath + ".locks"
}

func runtimeDatabaseLockPath(databasePath string) string {
	return filepath.Join(runtimeDatabaseLockDirectory(databasePath), "store")
}

func lockRuntimeDatabase(databasePath string) (func(), error) {
	if strings.TrimSpace(databasePath) == "" {
		return nil, errors.New("runtime-state database path is required")
	}
	if err := sqlitestore.EnsurePrivateDir(filepath.Dir(databasePath)); err != nil {
		return nil, err
	}
	lockDirectory := runtimeDatabaseLockDirectory(databasePath)
	if err := sqlitestore.EnsurePrivateDir(lockDirectory); err != nil {
		return nil, err
	}
	return lockRuntimeStateFile(runtimeDatabaseLockPath(databasePath))
}
