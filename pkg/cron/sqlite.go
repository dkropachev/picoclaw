package cron

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	cronDatabaseFilename   = "jobs.db"
	cronLegacyFilename     = "jobs.json"
	cronDatabaseComponent  = "cron-jobs"
	cronLegacySourceID     = "cron-jobs-json-v1"
	cronLegacyArchiveLabel = "cron-jobs-v1"
	cronLegacyMaxBytes     = int64(64 << 20)

	maximumCronJobs           = 100_000
	maximumCronIDBytes        = 256
	maximumCronNameBytes      = 64 << 10
	maximumCronKindBytes      = 256
	maximumCronExprBytes      = 64 << 10
	maximumCronTimezoneBytes  = 4 << 10
	maximumCronPayloadBytes   = 1 << 20
	maximumCronRouteBytes     = 64 << 10
	maximumCronStatusBytes    = 256
	maximumCronErrorBytes     = 1 << 20
	maximumCronAggregateBytes = int64(1 << 30)
)

const cronStoreSchema = `CREATE TABLE cron_store (
    id          INTEGER PRIMARY KEY CHECK(id = 1),
    generation  INTEGER NOT NULL DEFAULT 0 CHECK(generation >= 0),
    version     INTEGER NOT NULL DEFAULT 1 CHECK(version > 0)
) STRICT`

const cronStoreSeedSchema = `INSERT INTO cron_store(id, generation, version) VALUES (1, 0, 1)`

const cronJobsSchema = `CREATE TABLE cron_jobs (
    job_id              TEXT PRIMARY KEY,
    position            INTEGER NOT NULL CHECK(position >= 0),
    name                TEXT NOT NULL,
    enabled             INTEGER NOT NULL CHECK(enabled IN (0, 1)),
    schedule_kind       TEXT NOT NULL,
    schedule_at_ms      INTEGER,
    schedule_every_ms   INTEGER,
    schedule_expr       TEXT NOT NULL,
    schedule_timezone   TEXT NOT NULL,
    payload_kind        TEXT NOT NULL,
    payload_message     TEXT NOT NULL,
    payload_command     TEXT NOT NULL,
    payload_channel     TEXT NOT NULL,
    payload_to          TEXT NOT NULL,
    next_run_at_ms      INTEGER,
    last_run_at_ms      INTEGER,
    last_status         TEXT NOT NULL,
    last_error          TEXT NOT NULL,
    created_at_ms       INTEGER NOT NULL,
    updated_at_ms       INTEGER NOT NULL,
    delete_after_run    INTEGER NOT NULL CHECK(delete_after_run IN (0, 1)),
    touched_generation  INTEGER NOT NULL CHECK(touched_generation >= 0),
    version             INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    CHECK(length(CAST(job_id AS BLOB)) BETWEEN 1 AND 256),
    CHECK(length(CAST(name AS BLOB)) <= 65536),
    CHECK(length(CAST(schedule_kind AS BLOB)) <= 256),
    CHECK(length(CAST(schedule_expr AS BLOB)) <= 65536),
    CHECK(length(CAST(schedule_timezone AS BLOB)) <= 4096),
    CHECK(length(CAST(payload_kind AS BLOB)) <= 256),
    CHECK(length(CAST(payload_message AS BLOB)) <= 1048576),
    CHECK(length(CAST(payload_command AS BLOB)) <= 1048576),
    CHECK(length(CAST(payload_channel AS BLOB)) <= 65536),
    CHECK(length(CAST(payload_to AS BLOB)) <= 65536),
    CHECK(length(CAST(last_status AS BLOB)) <= 256),
    CHECK(length(CAST(last_error AS BLOB)) <= 1048576),
    CHECK(instr(job_id, char(0)) = 0),
    CHECK(instr(name, char(0)) = 0),
    CHECK(instr(schedule_kind, char(0)) = 0),
    CHECK(instr(schedule_expr, char(0)) = 0),
    CHECK(instr(schedule_timezone, char(0)) = 0),
    CHECK(instr(payload_kind, char(0)) = 0),
    CHECK(instr(payload_message, char(0)) = 0),
    CHECK(instr(payload_command, char(0)) = 0),
    CHECK(instr(payload_channel, char(0)) = 0),
    CHECK(instr(payload_to, char(0)) = 0),
    CHECK(instr(last_status, char(0)) = 0),
    CHECK(instr(last_error, char(0)) = 0)
) STRICT`

const cronJobsDueIndexSchema = `CREATE INDEX cron_jobs_due_idx
    ON cron_jobs(enabled, next_run_at_ms, position, job_id)`

const cronJobsPositionIndexSchema = `CREATE INDEX cron_jobs_position_idx
    ON cron_jobs(position, job_id)`

type cronSQLiteStorage struct {
	databasePath string
	sourceRoot   string
	archiveRoot  string
	dbMu         sync.Mutex
	db           *sql.DB
}

type legacyCronStore struct {
	Version int               `json:"version"`
	Jobs    []json.RawMessage `json:"jobs"`
}

func newCronSQLiteStorage(locator string) (*cronSQLiteStorage, error) {
	if strings.TrimSpace(locator) == "" || strings.ContainsRune(locator, '\x00') {
		return nil, errors.New("cron store path is required")
	}
	resolved, err := filepath.Abs(filepath.Clean(locator))
	if err != nil {
		return nil, err
	}
	extension := strings.ToLower(filepath.Ext(resolved))
	databasePath := resolved
	legacyPath := resolved
	switch extension {
	case ".json":
		databasePath = strings.TrimSuffix(resolved, filepath.Ext(resolved)) + ".db"
	case ".db":
		legacyPath = strings.TrimSuffix(resolved, filepath.Ext(resolved)) + ".json"
	default:
		databasePath = filepath.Join(resolved, cronDatabaseFilename)
		legacyPath = filepath.Join(resolved, cronLegacyFilename)
	}
	sourceRoot := filepath.Dir(legacyPath)
	return &cronSQLiteStorage{
		databasePath: databasePath,
		sourceRoot:   sourceRoot,
		archiveRoot: filepath.Join(
			sourceRoot,
			"legacy-json",
			cronLegacyArchiveLabel,
		),
	}, nil
}

func (s *cronSQLiteStorage) open(ctx context.Context) (*sql.DB, error) {
	return sqlitestore.Open(ctx, s.databasePath, sqlitestore.Options{
		Component: cronDatabaseComponent,
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{
				cronStoreSchema,
				cronStoreSeedSchema,
				cronJobsSchema,
				cronJobsDueIndexSchema,
				cronJobsPositionIndexSchema,
			},
		}},
		Validate: validateCronSchema,
		Legacy: &sqlitestore.LegacyOptions{
			SourceRoot:  s.sourceRoot,
			ArchiveRoot: s.archiveRoot,
			Sources: func() ([]sqlitestore.LegacySource, error) {
				return []sqlitestore.LegacySource{{
					ID:       cronLegacySourceID,
					Relative: cronLegacyFilename,
					MaxBytes: cronLegacyMaxBytes,
				}}, nil
			},
			Import:        importLegacyCronStore,
			MaxBytes:      cronLegacyMaxBytes,
			MaxSources:    1,
			MaxTotalBytes: cronLegacyMaxBytes,
		},
	})
}

func validateCronSchema(ctx context.Context, conn *sql.Conn) error {
	for _, object := range []struct {
		objectType string
		name       string
		schema     string
	}{
		{objectType: "table", name: "cron_store", schema: cronStoreSchema},
		{objectType: "table", name: "cron_jobs", schema: cronJobsSchema},
		{objectType: "index", name: "cron_jobs_due_idx", schema: cronJobsDueIndexSchema},
		{objectType: "index", name: "cron_jobs_position_idx", schema: cronJobsPositionIndexSchema},
	} {
		if err := sqlitestore.ValidateSchemaObject(
			ctx,
			conn,
			object.objectType,
			object.name,
			object.schema,
		); err != nil {
			return err
		}
	}
	for _, table := range []string{"cron_store", "cron_jobs"} {
		if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, table); err != nil {
			return err
		}
	}
	var unexpected int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
        WHERE name NOT LIKE 'sqlite_%'
          AND name NOT IN (
              'cron_store', 'cron_jobs', 'cron_jobs_due_idx', 'cron_jobs_position_idx',
              'storage_imports', 'storage_import_issues', 'storage_imports_archive_status_idx'
          )`).Scan(&unexpected); err != nil {
		return err
	}
	if unexpected != 0 {
		return errors.New("cron database has unexpected schema objects")
	}
	var singletonCount, jobCount, noncontiguous int
	var aggregateBytes int64
	if err := conn.QueryRowContext(ctx, `SELECT
        (SELECT COUNT(*) FROM cron_store WHERE id = 1),
        (SELECT COUNT(*) FROM cron_jobs),
        (SELECT COUNT(*) FROM (
            SELECT position, row_number() OVER (ORDER BY position, job_id) - 1 AS expected
              FROM cron_jobs
        ) WHERE position <> expected),
        (SELECT COALESCE(SUM(
            length(CAST(job_id AS BLOB)) + length(CAST(name AS BLOB)) +
            length(CAST(schedule_kind AS BLOB)) + length(CAST(schedule_expr AS BLOB)) +
            length(CAST(schedule_timezone AS BLOB)) + length(CAST(payload_kind AS BLOB)) +
            length(CAST(payload_message AS BLOB)) + length(CAST(payload_command AS BLOB)) +
            length(CAST(payload_channel AS BLOB)) + length(CAST(payload_to AS BLOB)) +
            length(CAST(last_status AS BLOB)) + length(CAST(last_error AS BLOB))
        ), 0) FROM cron_jobs)`).Scan(
		&singletonCount,
		&jobCount,
		&noncontiguous,
		&aggregateBytes,
	); err != nil {
		return err
	}
	if singletonCount != 1 || jobCount > maximumCronJobs || noncontiguous != 0 ||
		aggregateBytes > maximumCronAggregateBytes {
		return errors.New("cron database violates its data bounds")
	}
	return nil
}

func (s *cronSQLiteStorage) load(ctx context.Context) (*CronStore, error) {
	db, err := s.database(ctx)
	if err != nil {
		return nil, err
	}
	return loadCronStore(ctx, db)
}

func (s *cronSQLiteStorage) database(ctx context.Context) (*sql.DB, error) {
	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	if s.db != nil {
		return s.db, nil
	}
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	s.db = db
	return db, nil
}

func (s *cronSQLiteStorage) close() error {
	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	if s.db == nil {
		return nil
	}
	db := s.db
	s.db = nil
	return db.Close()
}

type cronQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func loadCronStore(ctx context.Context, queryer cronQueryer) (*CronStore, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT
        job_id, name, enabled, schedule_kind, schedule_at_ms, schedule_every_ms,
        schedule_expr, schedule_timezone, payload_kind, payload_message,
        payload_command, payload_channel, payload_to, next_run_at_ms,
        last_run_at_ms, last_status, last_error, created_at_ms, updated_at_ms,
        delete_after_run
      FROM cron_jobs ORDER BY position, job_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	store := &CronStore{Version: 1, Jobs: make([]CronJob, 0)}
	for rows.Next() {
		var job CronJob
		var enabled, deleteAfterRun int
		var atMS, everyMS, nextRunAtMS, lastRunAtMS sql.NullInt64
		if err := rows.Scan(
			&job.ID,
			&job.Name,
			&enabled,
			&job.Schedule.Kind,
			&atMS,
			&everyMS,
			&job.Schedule.Expr,
			&job.Schedule.TZ,
			&job.Payload.Kind,
			&job.Payload.Message,
			&job.Payload.Command,
			&job.Payload.Channel,
			&job.Payload.To,
			&nextRunAtMS,
			&lastRunAtMS,
			&job.State.LastStatus,
			&job.State.LastError,
			&job.CreatedAtMS,
			&job.UpdatedAtMS,
			&deleteAfterRun,
		); err != nil {
			return nil, err
		}
		job.Enabled = enabled == 1
		job.DeleteAfterRun = deleteAfterRun == 1
		job.Schedule.AtMS = nullableCronInt64(atMS)
		job.Schedule.EveryMS = nullableCronInt64(everyMS)
		job.State.NextRunAtMS = nullableCronInt64(nextRunAtMS)
		job.State.LastRunAtMS = nullableCronInt64(lastRunAtMS)
		store.Jobs = append(store.Jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return store, nil
}

func nullableCronInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func (s *cronSQLiteStorage) mutate(
	ctx context.Context,
	mutation func(*CronStore) error,
) (*CronStore, error) {
	db, err := s.database(ctx)
	if err != nil {
		return nil, err
	}
	var committed *CronStore
	err = sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		store, loadErr := loadCronStore(ctx, conn)
		if loadErr != nil {
			return loadErr
		}
		if mutation != nil {
			if mutationErr := mutation(store); mutationErr != nil {
				return mutationErr
			}
		}
		if writeErr := writeCronStore(ctx, conn, store); writeErr != nil {
			return writeErr
		}
		committed = cloneCronStore(store)
		return nil
	})
	return committed, err
}

func writeCronStore(ctx context.Context, conn *sql.Conn, store *CronStore) error {
	if err := validateCronStore(store); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE cron_store
        SET generation = generation + 1, version = version + 1 WHERE id = 1`); err != nil {
		return err
	}
	var generation int64
	if err := conn.QueryRowContext(ctx, `SELECT generation FROM cron_store WHERE id = 1`).Scan(
		&generation,
	); err != nil {
		return err
	}
	for position := range store.Jobs {
		job := store.Jobs[position]
		if _, err := conn.ExecContext(ctx, `INSERT INTO cron_jobs (
            job_id, position, name, enabled, schedule_kind, schedule_at_ms,
            schedule_every_ms, schedule_expr, schedule_timezone, payload_kind,
            payload_message, payload_command, payload_channel, payload_to,
            next_run_at_ms, last_run_at_ms, last_status, last_error,
            created_at_ms, updated_at_ms, delete_after_run, touched_generation, version
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
        ON CONFLICT(job_id) DO UPDATE SET
            position = excluded.position,
            name = excluded.name,
            enabled = excluded.enabled,
            schedule_kind = excluded.schedule_kind,
            schedule_at_ms = excluded.schedule_at_ms,
            schedule_every_ms = excluded.schedule_every_ms,
            schedule_expr = excluded.schedule_expr,
            schedule_timezone = excluded.schedule_timezone,
            payload_kind = excluded.payload_kind,
            payload_message = excluded.payload_message,
            payload_command = excluded.payload_command,
            payload_channel = excluded.payload_channel,
            payload_to = excluded.payload_to,
            next_run_at_ms = excluded.next_run_at_ms,
            last_run_at_ms = excluded.last_run_at_ms,
            last_status = excluded.last_status,
            last_error = excluded.last_error,
            created_at_ms = excluded.created_at_ms,
            updated_at_ms = excluded.updated_at_ms,
            delete_after_run = excluded.delete_after_run,
            touched_generation = excluded.touched_generation,
            version = cron_jobs.version + 1`,
			job.ID,
			position,
			job.Name,
			cronBoolInt(job.Enabled),
			job.Schedule.Kind,
			job.Schedule.AtMS,
			job.Schedule.EveryMS,
			job.Schedule.Expr,
			job.Schedule.TZ,
			job.Payload.Kind,
			job.Payload.Message,
			job.Payload.Command,
			job.Payload.Channel,
			job.Payload.To,
			job.State.NextRunAtMS,
			job.State.LastRunAtMS,
			job.State.LastStatus,
			job.State.LastError,
			job.CreatedAtMS,
			job.UpdatedAtMS,
			cronBoolInt(job.DeleteAfterRun),
			generation,
		); err != nil {
			return err
		}
	}
	_, err := conn.ExecContext(
		ctx,
		`DELETE FROM cron_jobs WHERE touched_generation <> ?`,
		generation,
	)
	return err
}

func validateCronStore(store *CronStore) error {
	if store == nil || len(store.Jobs) > maximumCronJobs {
		return errors.New("cron store exceeds its job limit")
	}
	seen := make(map[string]struct{}, len(store.Jobs))
	var aggregate int64
	for index := range store.Jobs {
		job := &store.Jobs[index]
		if err := validateCronJob(job); err != nil {
			return err
		}
		if _, exists := seen[job.ID]; exists {
			return errors.New("cron store has duplicate job identities")
		}
		seen[job.ID] = struct{}{}
		aggregate += int64(
			len(job.ID) + len(job.Name) + len(job.Schedule.Kind) + len(job.Schedule.Expr) +
				len(job.Schedule.TZ) + len(job.Payload.Kind) + len(job.Payload.Message) +
				len(job.Payload.Command) + len(job.Payload.Channel) + len(job.Payload.To) +
				len(job.State.LastStatus) + len(job.State.LastError),
		)
		if aggregate > maximumCronAggregateBytes {
			return errors.New("cron store exceeds its aggregate text limit")
		}
	}
	return nil
}

func validateCronJob(job *CronJob) error {
	if job == nil {
		return errors.New("cron job is required")
	}
	for _, field := range []struct {
		name     string
		value    string
		maximum  int
		required bool
	}{
		{name: "id", value: job.ID, maximum: maximumCronIDBytes, required: true},
		{name: "name", value: job.Name, maximum: maximumCronNameBytes},
		{name: "schedule kind", value: job.Schedule.Kind, maximum: maximumCronKindBytes},
		{name: "schedule expression", value: job.Schedule.Expr, maximum: maximumCronExprBytes},
		{name: "schedule timezone", value: job.Schedule.TZ, maximum: maximumCronTimezoneBytes},
		{name: "payload kind", value: job.Payload.Kind, maximum: maximumCronKindBytes},
		{name: "payload message", value: job.Payload.Message, maximum: maximumCronPayloadBytes},
		{name: "payload command", value: job.Payload.Command, maximum: maximumCronPayloadBytes},
		{name: "payload channel", value: job.Payload.Channel, maximum: maximumCronRouteBytes},
		{name: "payload target", value: job.Payload.To, maximum: maximumCronRouteBytes},
		{name: "last status", value: job.State.LastStatus, maximum: maximumCronStatusBytes},
		{name: "last error", value: job.State.LastError, maximum: maximumCronErrorBytes},
	} {
		if (field.required && field.value == "") || !utf8.ValidString(field.value) ||
			strings.ContainsRune(field.value, '\x00') || len(field.value) > field.maximum {
			return fmt.Errorf("cron job %s is invalid", field.name)
		}
	}
	return nil
}

func cloneCronStore(store *CronStore) *CronStore {
	if store == nil {
		return nil
	}
	clone := &CronStore{Version: store.Version, Jobs: make([]CronJob, len(store.Jobs))}
	for index := range store.Jobs {
		clone.Jobs[index] = cloneCronJob(store.Jobs[index])
	}
	return clone
}

func cronBoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func importLegacyCronStore(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(input.Data))
	decoder.UseNumber()
	var legacy legacyCronStore
	if err := decoder.Decode(&legacy); err != nil {
		//nolint:nilerr // A malformed aggregate is a selected legacy skip.
		return skippedCronImport("malformed-json", input.Digest), nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return skippedCronImport("malformed-json", input.Digest), nil
	}
	if legacy.Version != 0 && legacy.Version != 1 {
		return skippedCronImport("unsupported-version", input.Digest), nil
	}
	if len(legacy.Jobs) > maximumCronJobs {
		return sqlitestore.ImportResult{}, errors.New("legacy cron job count exceeds its limit")
	}
	var generation int64
	position := 0
	if err := conn.QueryRowContext(ctx, `SELECT
        (SELECT generation FROM cron_store WHERE id = 1),
        COALESCE((SELECT MAX(position) + 1 FROM cron_jobs), 0)`).Scan(
		&generation,
		&position,
	); err != nil {
		return sqlitestore.ImportResult{}, err
	}
	result := sqlitestore.ImportResult{}
	seen := make(map[string]struct{}, len(legacy.Jobs))
	for _, raw := range legacy.Jobs {
		digest := sha256.Sum256(raw)
		var job CronJob
		if err := json.Unmarshal(raw, &job); err != nil || validateCronJob(&job) != nil {
			appendCronImportIssue(&result, "invalid-job", digest)
			continue
		}
		if _, duplicate := seen[job.ID]; duplicate {
			appendCronImportIssue(&result, "identity-conflict", digest)
			continue
		}
		seen[job.ID] = struct{}{}
		execution, err := conn.ExecContext(ctx, `INSERT INTO cron_jobs (
            job_id, position, name, enabled, schedule_kind, schedule_at_ms,
            schedule_every_ms, schedule_expr, schedule_timezone, payload_kind,
            payload_message, payload_command, payload_channel, payload_to,
            next_run_at_ms, last_run_at_ms, last_status, last_error,
            created_at_ms, updated_at_ms, delete_after_run, touched_generation, version
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
        ON CONFLICT(job_id) DO NOTHING`,
			job.ID,
			position,
			job.Name,
			cronBoolInt(job.Enabled),
			job.Schedule.Kind,
			job.Schedule.AtMS,
			job.Schedule.EveryMS,
			job.Schedule.Expr,
			job.Schedule.TZ,
			job.Payload.Kind,
			job.Payload.Message,
			job.Payload.Command,
			job.Payload.Channel,
			job.Payload.To,
			job.State.NextRunAtMS,
			job.State.LastRunAtMS,
			job.State.LastStatus,
			job.State.LastError,
			job.CreatedAtMS,
			job.UpdatedAtMS,
			cronBoolInt(job.DeleteAfterRun),
			generation,
		)
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		inserted, err := execution.RowsAffected()
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		if inserted == 1 {
			result.Imported++
			position++
		} else {
			appendCronImportIssue(&result, "sqlite-authoritative", digest)
		}
	}
	return result, nil
}

func skippedCronImport(code string, digest [sha256.Size]byte) sqlitestore.ImportResult {
	result := sqlitestore.ImportResult{}
	appendCronImportIssue(&result, code, digest)
	return result
}

func appendCronImportIssue(
	result *sqlitestore.ImportResult,
	code string,
	digest [sha256.Size]byte,
) {
	result.Skipped++
	if len(result.Issues) < 512 {
		result.Issues = append(result.Issues, sqlitestore.ImportIssue{
			Code:         code,
			RecordDigest: digest,
		})
	}
}
