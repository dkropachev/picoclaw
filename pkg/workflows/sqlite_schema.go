package workflows

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	workflowDatabaseComponent  = "workflows"
	workflowDatabaseFilename   = "workflows.db"
	workflowDatabaseStateDir   = "state"
	workflowLegacyArchiveLabel = "workflows-v1"

	maximumWorkflowRuns               = 1_000_000
	maximumWorkflowEvents             = 10_000_000
	maximumWorkflowRunChildren        = 10_000_000
	maximumWorkflowRunJobs            = 10_000_000
	maximumWorkflowRunSteps           = 100_000_000
	maximumWorkflowHumanTasks         = 10_000_000
	maximumWorkflowValidationIssues   = 10_000_000
	maximumWorkflowChildrenPerRun     = 100_000
	maximumWorkflowJobsPerRun         = 100_000
	maximumWorkflowStepsPerRun        = 1_000_000
	maximumWorkflowHumanTasksPerRun   = 100_000
	maximumWorkflowEventsPerRun       = 1_000_000
	maximumWorkflowIssuesPerStamp     = 100_000
	maximumWorkflowRunPayloadBytes    = int64(64 << 20)
	maximumWorkflowRunTotalBytes      = int64(4 << 30)
	maximumWorkflowEventPayloadBytes  = int64(1 << 20)
	maximumWorkflowEventTotalBytes    = int64(4 << 30)
	maximumWorkflowNativeValueBytes   = int64(16 << 20)
	maximumWorkflowNativeTotalBytes   = int64(64 << 20)
	maximumWorkflowManifestBytes      = int64(32 << 20)
	maximumWorkflowDevelopmentBytes   = int64(16 << 20)
	maximumWorkflowLegacySourceBytes  = int64(64 << 20)
	maximumWorkflowLegacyTotalBytes   = int64(4 << 30)
	maximumWorkflowLegacySources      = 1_000_000
	maximumWorkflowIdentityBytes      = 4096
	maximumWorkflowReferenceBytes     = 16 << 10
	maximumWorkflowScalarTextBytes    = 64 << 10
	maximumWorkflowDevelopmentRecords = 100_000
)

const workflowRunsSchema = `CREATE TABLE workflow_runs (
    run_id                    TEXT PRIMARY KEY,
    workflow_ref              TEXT NOT NULL,
    status                    TEXT NOT NULL,
    context_visibility        TEXT NOT NULL,
    parent_run_id             TEXT,
    caller_job_id             TEXT NOT NULL,
    retry_of_run_id           TEXT,
    session_key               TEXT NOT NULL,
    delivery_channel          TEXT NOT NULL,
    delivery_chat_id          TEXT NOT NULL,
    delivery_topic_id         TEXT NOT NULL,
    delivery_thread_ts        TEXT NOT NULL,
    delivery_message_id       TEXT NOT NULL,
    delivery_reply_message_id TEXT NOT NULL,
    origin_kind               TEXT,
    origin_event_id           TEXT,
    origin_dispatch_id        TEXT,
    origin_root_run_id        TEXT,
    error_text                TEXT NOT NULL,
    cancel_reason             TEXT NOT NULL,
    created_at_seconds        INTEGER NOT NULL CHECK(created_at_seconds BETWEEN -62167219200 AND 253402300799),
    created_at_nanosecond     INTEGER NOT NULL CHECK(created_at_nanosecond BETWEEN 0 AND 999999999),
    updated_at_seconds        INTEGER NOT NULL CHECK(updated_at_seconds BETWEEN -62167219200 AND 253402300799),
    updated_at_nanosecond     INTEGER NOT NULL CHECK(updated_at_nanosecond BETWEEN 0 AND 999999999),
    completed_at_seconds      INTEGER,
    completed_at_nanosecond   INTEGER,
    cancel_at_seconds         INTEGER,
    cancel_at_nanosecond      INTEGER,
    child_ids_is_null         INTEGER NOT NULL CHECK(child_ids_is_null IN (0, 1)),
    jobs_is_null              INTEGER NOT NULL CHECK(jobs_is_null IN (0, 1)),
    steps_is_null             INTEGER NOT NULL CHECK(steps_is_null IN (0, 1)),
    human_tasks_is_null       INTEGER NOT NULL CHECK(human_tasks_is_null IN (0, 1)),
    is_private                INTEGER NOT NULL CHECK(is_private IN (0, 1)),
    version                   INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    CHECK(length(CAST(run_id AS BLOB)) BETWEEN 1 AND 4096),
    CHECK(run_id = trim(run_id)),
    CHECK(instr(run_id, char(0)) = 0),
    CHECK(length(CAST(workflow_ref AS BLOB)) BETWEEN 0 AND 16384),
    CHECK(instr(workflow_ref, char(0)) = 0),
    CHECK(length(CAST(status AS BLOB)) BETWEEN 0 AND 256),
    CHECK(length(CAST(context_visibility AS BLOB)) <= 256),
    CHECK(parent_run_id IS NULL OR length(CAST(parent_run_id AS BLOB)) BETWEEN 1 AND 4096),
    CHECK(retry_of_run_id IS NULL OR length(CAST(retry_of_run_id AS BLOB)) BETWEEN 1 AND 4096),
    CHECK(length(CAST(caller_job_id AS BLOB)) <= 4096),
    CHECK(length(CAST(session_key AS BLOB)) <= 65536),
    CHECK(length(CAST(error_text AS BLOB)) <= 65536),
    CHECK(length(CAST(cancel_reason AS BLOB)) <= 1024),
    CHECK((completed_at_seconds IS NULL) = (completed_at_nanosecond IS NULL)),
    CHECK(completed_at_seconds IS NULL OR completed_at_seconds BETWEEN -62167219200 AND 253402300799),
    CHECK(completed_at_nanosecond IS NULL OR completed_at_nanosecond BETWEEN 0 AND 999999999),
    CHECK((cancel_at_seconds IS NULL) = (cancel_at_nanosecond IS NULL)),
    CHECK(cancel_at_seconds IS NULL OR cancel_at_seconds BETWEEN -62167219200 AND 253402300799),
    CHECK(cancel_at_nanosecond IS NULL OR cancel_at_nanosecond BETWEEN 0 AND 999999999),
    -- Ancestors may be pruned independently while retained descendants keep
    -- their immutable lineage identifiers.
    CHECK(parent_run_id IS NULL OR parent_run_id <> run_id),
    CHECK(retry_of_run_id IS NULL OR retry_of_run_id <> run_id)
) STRICT`

const workflowRunPayloadsSchema = `CREATE TABLE workflow_run_payloads (
    run_id                TEXT PRIMARY KEY,
    event_json            BLOB,
    inputs_json           BLOB,
    outputs_json          BLOB,
    delivery_handles_json BLOB,
    execution_json        BLOB,
    private_context_json  BLOB,
    CHECK(event_json IS NULL OR (typeof(event_json) = 'blob' AND length(event_json) <= 67108864)),
    CHECK(inputs_json IS NULL OR (typeof(inputs_json) = 'blob' AND length(inputs_json) <= 67108864)),
    CHECK(outputs_json IS NULL OR (typeof(outputs_json) = 'blob' AND length(outputs_json) <= 67108864)),
    CHECK(delivery_handles_json IS NULL OR (typeof(delivery_handles_json) = 'blob' AND length(delivery_handles_json) <= 67108864)),
    CHECK(execution_json IS NULL OR (typeof(execution_json) = 'blob' AND length(execution_json) <= 67108864)),
    CHECK(private_context_json IS NULL OR (typeof(private_context_json) = 'blob' AND length(private_context_json) <= 67108864)),
    FOREIGN KEY(run_id) REFERENCES workflow_runs(run_id) ON DELETE CASCADE
) STRICT`

const workflowRunChildrenSchema = `CREATE TABLE workflow_run_children (
    run_id       TEXT NOT NULL,
    position     INTEGER NOT NULL CHECK(position >= 0),
    child_run_id TEXT NOT NULL,
    PRIMARY KEY(run_id, position),
    UNIQUE(run_id, child_run_id),
    FOREIGN KEY(run_id) REFERENCES workflow_runs(run_id) ON DELETE CASCADE,
    CHECK(length(CAST(child_run_id AS BLOB)) BETWEEN 1 AND 4096)
) STRICT`

const workflowRunJobsSchema = `CREATE TABLE workflow_run_jobs (
    run_id       TEXT NOT NULL,
    job_key      TEXT NOT NULL,
    job_id       TEXT NOT NULL,
    status       TEXT NOT NULL,
    error_text   TEXT NOT NULL,
    outputs_json BLOB,
    PRIMARY KEY(run_id, job_key),
    FOREIGN KEY(run_id) REFERENCES workflow_runs(run_id) ON DELETE CASCADE,
    CHECK(length(CAST(job_key AS BLOB)) BETWEEN 1 AND 4096),
    CHECK(length(CAST(job_id AS BLOB)) <= 4096),
    CHECK(length(CAST(status AS BLOB)) <= 256),
    CHECK(length(CAST(error_text AS BLOB)) <= 65536),
    CHECK(outputs_json IS NULL OR (typeof(outputs_json) = 'blob' AND length(outputs_json) <= 67108864))
) STRICT`

const workflowRunStepsSchema = `CREATE TABLE workflow_run_steps (
    run_id       TEXT NOT NULL,
    step_key     TEXT NOT NULL,
    step_id      TEXT NOT NULL,
    status       TEXT NOT NULL,
    error_text   TEXT NOT NULL,
    outputs_json BLOB,
    PRIMARY KEY(run_id, step_key),
    FOREIGN KEY(run_id) REFERENCES workflow_runs(run_id) ON DELETE CASCADE,
    CHECK(length(CAST(step_key AS BLOB)) BETWEEN 1 AND 8192),
    CHECK(length(CAST(step_id AS BLOB)) <= 4096),
    CHECK(length(CAST(status AS BLOB)) <= 256),
    CHECK(length(CAST(error_text AS BLOB)) <= 65536),
    CHECK(outputs_json IS NULL OR (typeof(outputs_json) = 'blob' AND length(outputs_json) <= 67108864))
) STRICT`

const workflowHumanTasksSchema = `CREATE TABLE workflow_human_tasks (
    run_id                  TEXT NOT NULL,
    task_key                TEXT NOT NULL,
    task_id                 TEXT NOT NULL,
    workflow_ref            TEXT NOT NULL,
    job_id                  TEXT NOT NULL,
    step_id                 TEXT NOT NULL,
    status                  TEXT NOT NULL,
    revision                INTEGER NOT NULL CHECK(revision >= 0),
    input_hash              TEXT NOT NULL,
    title                   TEXT NOT NULL,
    actor_kind              TEXT NOT NULL,
    execution_id            TEXT NOT NULL,
    action_revision         TEXT NOT NULL,
    response_id             TEXT NOT NULL,
    created_at_seconds      INTEGER NOT NULL CHECK(created_at_seconds BETWEEN -62167219200 AND 253402300799),
    created_at_nanosecond   INTEGER NOT NULL CHECK(created_at_nanosecond BETWEEN 0 AND 999999999),
    updated_at_seconds      INTEGER NOT NULL CHECK(updated_at_seconds BETWEEN -62167219200 AND 253402300799),
    updated_at_nanosecond   INTEGER NOT NULL CHECK(updated_at_nanosecond BETWEEN 0 AND 999999999),
    answered_at_seconds     INTEGER,
    answered_at_nanosecond  INTEGER,
    canceled_at_seconds     INTEGER,
    canceled_at_nanosecond  INTEGER,
    retry_at_seconds        INTEGER,
    retry_at_nanosecond     INTEGER,
    questions_json          BLOB NOT NULL,
    response_schema_json    BLOB,
    gate_form_json          BLOB,
    gate_workflow_json      BLOB,
    response_json           BLOB,
    PRIMARY KEY(run_id, task_key),
    UNIQUE(run_id, task_id),
    FOREIGN KEY(run_id) REFERENCES workflow_runs(run_id) ON DELETE CASCADE,
    CHECK(length(CAST(task_key AS BLOB)) BETWEEN 1 AND 4096),
    CHECK(length(CAST(task_id AS BLOB)) BETWEEN 1 AND 4096),
    CHECK(typeof(questions_json) = 'blob' AND length(questions_json) <= 8388608),
    CHECK((answered_at_seconds IS NULL) = (answered_at_nanosecond IS NULL)),
    CHECK(answered_at_nanosecond IS NULL OR answered_at_nanosecond BETWEEN 0 AND 999999999),
    CHECK(answered_at_seconds IS NULL OR answered_at_seconds BETWEEN -62167219200 AND 253402300799),
    CHECK((canceled_at_seconds IS NULL) = (canceled_at_nanosecond IS NULL)),
    CHECK(canceled_at_nanosecond IS NULL OR canceled_at_nanosecond BETWEEN 0 AND 999999999),
    CHECK(canceled_at_seconds IS NULL OR canceled_at_seconds BETWEEN -62167219200 AND 253402300799),
    CHECK((retry_at_seconds IS NULL) = (retry_at_nanosecond IS NULL)),
    CHECK(retry_at_nanosecond IS NULL OR retry_at_nanosecond BETWEEN 0 AND 999999999),
    CHECK(retry_at_seconds IS NULL OR retry_at_seconds BETWEEN -62167219200 AND 253402300799),
    CHECK(response_schema_json IS NULL OR (typeof(response_schema_json) = 'blob' AND length(response_schema_json) <= 8388608)),
    CHECK(gate_form_json IS NULL OR (typeof(gate_form_json) = 'blob' AND length(gate_form_json) <= 8388608)),
    CHECK(gate_workflow_json IS NULL OR (typeof(gate_workflow_json) = 'blob' AND length(gate_workflow_json) <= 8388608)),
    CHECK(response_json IS NULL OR (typeof(response_json) = 'blob' AND length(response_json) <= 8388608))
) STRICT`

const workflowPrivateMarkersSchema = `CREATE TABLE workflow_private_run_markers (
    run_id TEXT PRIMARY KEY,
    FOREIGN KEY(run_id) REFERENCES workflow_runs(run_id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED
) STRICT`

const workflowRunEventsSchema = `CREATE TABLE workflow_run_events (
    run_id              TEXT NOT NULL,
    sequence            INTEGER NOT NULL CHECK(sequence >= 0),
    occurred_at_seconds INTEGER NOT NULL CHECK(occurred_at_seconds BETWEEN -62167219200 AND 253402300799),
    occurred_nanosecond INTEGER NOT NULL CHECK(occurred_nanosecond BETWEEN 0 AND 999999999),
    kind                TEXT NOT NULL,
    job_id              TEXT NOT NULL,
    step_id             TEXT NOT NULL,
    message             TEXT NOT NULL,
    payload_json        BLOB,
    PRIMARY KEY(run_id, sequence),
    FOREIGN KEY(run_id) REFERENCES workflow_runs(run_id) ON DELETE CASCADE,
    CHECK(length(CAST(kind AS BLOB)) BETWEEN 1 AND 4096),
    CHECK(length(CAST(message AS BLOB)) <= 65536),
    CHECK(payload_json IS NULL OR (typeof(payload_json) = 'blob' AND length(payload_json) <= 1048576))
) STRICT`

const workflowNativeStateSchema = `CREATE TABLE workflow_native_state (
    namespace_id         TEXT NOT NULL,
    key_id               TEXT NOT NULL,
    key_text             TEXT NOT NULL,
    value_json           BLOB NOT NULL,
    updated_at_seconds   INTEGER NOT NULL CHECK(updated_at_seconds BETWEEN -62167219200 AND 253402300799),
    updated_at_nanosecond INTEGER NOT NULL CHECK(updated_at_nanosecond BETWEEN 0 AND 999999999),
    version              INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    PRIMARY KEY(namespace_id, key_id),
    CHECK(length(CAST(namespace_id AS BLOB)) BETWEEN 1 AND 4096),
    CHECK(length(CAST(key_id AS BLOB)) BETWEEN 1 AND 4096),
    CHECK(length(CAST(key_text AS BLOB)) BETWEEN 1 AND 65536),
    CHECK(typeof(value_json) = 'blob' AND length(value_json) <= 16777216)
) STRICT`

const workflowCompatibilityRuntimeSchema = `CREATE TABLE workflow_compatibility_runtime (
    singleton             INTEGER PRIMARY KEY CHECK(singleton = 1),
    picoclaw_version      TEXT NOT NULL,
    git_commit            TEXT NOT NULL,
    workflow_engine       TEXT NOT NULL,
    workflow_schema       TEXT NOT NULL,
    validator_fingerprint TEXT NOT NULL,
    updated_at_seconds    INTEGER NOT NULL CHECK(updated_at_seconds BETWEEN -62167219200 AND 253402300799),
    updated_at_nanosecond INTEGER NOT NULL CHECK(updated_at_nanosecond BETWEEN 0 AND 999999999),
    version               INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    CHECK(length(CAST(picoclaw_version AS BLOB)) <= 4096),
    CHECK(length(CAST(git_commit AS BLOB)) <= 4096),
    CHECK(length(CAST(workflow_engine AS BLOB)) <= 4096),
    CHECK(length(CAST(workflow_schema AS BLOB)) <= 4096),
    CHECK(length(CAST(validator_fingerprint AS BLOB)) <= 4096)
) STRICT`

const workflowValidationStampsSchema = `CREATE TABLE workflow_validation_stamps (
    workflow_ref          TEXT PRIMARY KEY,
    workflow_hash         TEXT NOT NULL,
    picoclaw_version      TEXT NOT NULL,
    git_commit            TEXT NOT NULL,
    workflow_engine       TEXT NOT NULL,
    workflow_schema       TEXT NOT NULL,
    validator_fingerprint TEXT NOT NULL,
    status                TEXT NOT NULL,
    validated_at_seconds  INTEGER NOT NULL CHECK(validated_at_seconds BETWEEN -62167219200 AND 253402300799),
    validated_at_nanosecond INTEGER NOT NULL CHECK(validated_at_nanosecond BETWEEN 0 AND 999999999),
    CHECK(length(CAST(workflow_ref AS BLOB)) BETWEEN 1 AND 16384),
    CHECK(length(CAST(workflow_hash AS BLOB)) <= 4096),
    CHECK(length(CAST(picoclaw_version AS BLOB)) <= 4096),
    CHECK(length(CAST(git_commit AS BLOB)) <= 4096),
    CHECK(length(CAST(workflow_engine AS BLOB)) <= 4096),
    CHECK(length(CAST(workflow_schema AS BLOB)) <= 4096),
    CHECK(length(CAST(validator_fingerprint AS BLOB)) <= 4096),
    CHECK(length(CAST(status AS BLOB)) <= 256)
) STRICT`

const workflowValidationIssuesSchema = `CREATE TABLE workflow_validation_issues (
    workflow_ref TEXT NOT NULL,
    issue_kind   TEXT NOT NULL CHECK(issue_kind IN ('error', 'warning')),
    position     INTEGER NOT NULL CHECK(position >= 0),
    path_text    TEXT NOT NULL,
    message      TEXT NOT NULL,
    PRIMARY KEY(workflow_ref, issue_kind, position),
    FOREIGN KEY(workflow_ref) REFERENCES workflow_validation_stamps(workflow_ref) ON DELETE CASCADE,
    CHECK(length(CAST(path_text AS BLOB)) <= 16384),
    CHECK(length(CAST(message AS BLOB)) <= 65536)
) STRICT`

const workflowDevelopmentSessionsSchema = `CREATE TABLE workflow_development_sessions (
    session_id              TEXT PRIMARY KEY,
    lifecycle               TEXT NOT NULL CHECK(lifecycle IN ('active', 'published', 'discarded')),
    session_revision        TEXT NOT NULL,
    draft_revision          TEXT NOT NULL,
    base_target_revision    TEXT NOT NULL,
    reason                  TEXT NOT NULL,
    status                  TEXT NOT NULL,
    prompt_text             TEXT NOT NULL,
    source_workflow_ref     TEXT NOT NULL,
    target_workflow_ref     TEXT NOT NULL,
    target_picoclaw_version TEXT NOT NULL,
    target_git_commit       TEXT NOT NULL,
    yaml_text               TEXT NOT NULL,
    validation_json         BLOB,
    last_test_json          BLOB,
    created_at_seconds      INTEGER NOT NULL CHECK(created_at_seconds BETWEEN -62167219200 AND 253402300799),
    created_at_nanosecond   INTEGER NOT NULL CHECK(created_at_nanosecond BETWEEN 0 AND 999999999),
    updated_at_seconds      INTEGER NOT NULL CHECK(updated_at_seconds BETWEEN -62167219200 AND 253402300799),
    updated_at_nanosecond   INTEGER NOT NULL CHECK(updated_at_nanosecond BETWEEN 0 AND 999999999),
    version                 INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    CHECK(length(CAST(session_id AS BLOB)) BETWEEN 1 AND 4096),
    CHECK(length(CAST(yaml_text AS BLOB)) <= 16777216),
    CHECK(validation_json IS NULL OR (typeof(validation_json) = 'blob' AND length(validation_json) <= 16777216)),
    CHECK(last_test_json IS NULL OR (typeof(last_test_json) = 'blob' AND length(last_test_json) <= 16777216))
) STRICT`

const workflowDevelopmentActiveIndexSchema = `CREATE UNIQUE INDEX workflow_development_single_active_idx
    ON workflow_development_sessions(lifecycle) WHERE lifecycle = 'active'`

const workflowRunsCreatedIndexSchema = `CREATE INDEX workflow_runs_created_idx
    ON workflow_runs(created_at_seconds DESC, created_at_nanosecond DESC, run_id)`

const workflowRunsStatusIndexSchema = `CREATE INDEX workflow_runs_status_idx
    ON workflow_runs(status, parent_run_id, created_at_seconds, run_id)`

const workflowRunEventsTimeIndexSchema = `CREATE INDEX workflow_run_events_time_idx
    ON workflow_run_events(occurred_at_seconds, occurred_nanosecond, run_id, sequence)`

const workflowNativeNamespaceIndexSchema = `CREATE INDEX workflow_native_state_namespace_idx
    ON workflow_native_state(namespace_id, key_text, key_id)`

func workflowDatabasePath(workspace string) (string, error) {
	canonical, err := canonicalWorkflowWorkspace(workspace)
	if err != nil {
		return "", err
	}
	return filepath.Join(canonical, workflowDatabaseStateDir, workflowDatabaseFilename), nil
}

func workflowLegacyArchiveRoot(workspace string) (string, error) {
	canonical, err := canonicalWorkflowWorkspace(workspace)
	if err != nil {
		return "", err
	}
	return filepath.Join(canonical, "legacy-json", workflowLegacyArchiveLabel), nil
}

func workflowStoreOptions(workspace string) (sqlitestore.Options, error) {
	canonical, err := canonicalWorkflowWorkspace(workspace)
	if err != nil {
		return sqlitestore.Options{}, err
	}
	archiveRoot, err := workflowLegacyArchiveRoot(canonical)
	if err != nil {
		return sqlitestore.Options{}, err
	}
	legacySources, err := enumerateWorkflowLegacySources(canonical)
	if err != nil {
		return sqlitestore.Options{}, err
	}
	legacy := &sqlitestore.LegacyOptions{
		SourceRoot:  canonical,
		ArchiveRoot: archiveRoot,
		Sources: func() ([]sqlitestore.LegacySource, error) {
			return append([]sqlitestore.LegacySource(nil), legacySources...), nil
		},
		Import:        importWorkflowLegacySource,
		MaxBytes:      maximumWorkflowLegacySourceBytes,
		MaxSources:    maximumWorkflowLegacySources,
		MaxTotalBytes: maximumWorkflowLegacyTotalBytes,
	}
	return sqlitestore.Options{
		Component: workflowDatabaseComponent,
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{
				workflowRunsSchema,
				workflowRunPayloadsSchema,
				workflowRunChildrenSchema,
				workflowRunJobsSchema,
				workflowRunStepsSchema,
				workflowHumanTasksSchema,
				workflowPrivateMarkersSchema,
				workflowRunEventsSchema,
				workflowNativeStateSchema,
				workflowCompatibilityRuntimeSchema,
				workflowValidationStampsSchema,
				workflowValidationIssuesSchema,
				workflowDevelopmentSessionsSchema,
				workflowDevelopmentActiveIndexSchema,
				workflowRunsCreatedIndexSchema,
				workflowRunsStatusIndexSchema,
				workflowRunEventsTimeIndexSchema,
				workflowNativeNamespaceIndexSchema,
			},
		}},
		Validate: validateWorkflowSchema,
		Legacy:   legacy,
	}, nil
}

func openWorkflowDatabase(ctx context.Context, workspace string) (*sql.DB, error) {
	path, err := workflowDatabasePath(workspace)
	if err != nil {
		return nil, err
	}
	options, err := workflowStoreOptions(workspace)
	if err != nil {
		return nil, err
	}
	return sqlitestore.Open(ctx, path, options)
}

func validateWorkflowSchema(ctx context.Context, conn *sql.Conn) error {
	objects := []struct{ kind, name, schema string }{
		{"table", "workflow_runs", workflowRunsSchema},
		{"table", "workflow_run_payloads", workflowRunPayloadsSchema},
		{"table", "workflow_run_children", workflowRunChildrenSchema},
		{"table", "workflow_run_jobs", workflowRunJobsSchema},
		{"table", "workflow_run_steps", workflowRunStepsSchema},
		{"table", "workflow_human_tasks", workflowHumanTasksSchema},
		{"table", "workflow_private_run_markers", workflowPrivateMarkersSchema},
		{"table", "workflow_run_events", workflowRunEventsSchema},
		{"table", "workflow_native_state", workflowNativeStateSchema},
		{"table", "workflow_compatibility_runtime", workflowCompatibilityRuntimeSchema},
		{"table", "workflow_validation_stamps", workflowValidationStampsSchema},
		{"table", "workflow_validation_issues", workflowValidationIssuesSchema},
		{"table", "workflow_development_sessions", workflowDevelopmentSessionsSchema},
		{"index", "workflow_development_single_active_idx", workflowDevelopmentActiveIndexSchema},
		{"index", "workflow_runs_created_idx", workflowRunsCreatedIndexSchema},
		{"index", "workflow_runs_status_idx", workflowRunsStatusIndexSchema},
		{"index", "workflow_run_events_time_idx", workflowRunEventsTimeIndexSchema},
		{"index", "workflow_native_state_namespace_idx", workflowNativeNamespaceIndexSchema},
	}
	for _, object := range objects {
		if err := sqlitestore.ValidateSchemaObject(ctx, conn, object.kind, object.name, object.schema); err != nil {
			return err
		}
	}
	for _, table := range []string{
		"workflow_runs", "workflow_run_payloads", "workflow_run_children", "workflow_run_jobs",
		"workflow_run_steps", "workflow_human_tasks", "workflow_private_run_markers",
		"workflow_run_events", "workflow_native_state", "workflow_compatibility_runtime",
		"workflow_validation_stamps", "workflow_validation_issues", "workflow_development_sessions",
	} {
		expected := []string(nil)
		if table == "workflow_development_sessions" {
			expected = []string{"workflow_development_single_active_idx"}
		}
		if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, table, expected...); err != nil {
			return err
		}
	}
	allowed := make([]string, 0, len(objects)+4)
	for _, object := range objects {
		allowed = append(allowed, object.name)
	}
	allowed = append(allowed, "storage_imports", "storage_import_issues", "storage_import_horizons",
		"storage_imports_archive_status_idx")
	placeholders := strings.TrimRight(strings.Repeat("?,", len(allowed)), ",")
	arguments := make([]any, len(allowed))
	for index := range allowed {
		arguments[index] = allowed[index]
	}
	var unexpected int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE name NOT LIKE 'sqlite_%' AND name NOT IN (`+placeholders+`)`, arguments...).Scan(&unexpected); err != nil {
		return err
	}
	if unexpected != 0 {
		return errors.New("workflow schema contains unexpected objects")
	}
	var runCount, eventCount, developmentCount, validationCount int
	var runPayloadBytes, eventPayloadBytes, nativeBytes, developmentBytes, validationBytes int64
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM workflow_runs),
		(SELECT COUNT(*) FROM workflow_run_events),
		(SELECT COUNT(*) FROM workflow_development_sessions),
		(SELECT COUNT(*) FROM workflow_validation_stamps),
		(SELECT COALESCE(SUM(
			COALESCE(length(event_json),0)+COALESCE(length(inputs_json),0)+
			COALESCE(length(outputs_json),0)+COALESCE(length(delivery_handles_json),0)+
			COALESCE(length(execution_json),0)+COALESCE(length(private_context_json),0)
		),0) FROM workflow_run_payloads),
		(SELECT COALESCE(SUM(length(payload_json)),0) FROM workflow_run_events),
		(SELECT COALESCE(SUM(length(value_json)),0) FROM workflow_native_state),
		(SELECT COALESCE(SUM(
			length(CAST(prompt_text AS BLOB))+length(CAST(yaml_text AS BLOB))+
			COALESCE(length(validation_json),0)+COALESCE(length(last_test_json),0)
		),0) FROM workflow_development_sessions),
		(SELECT COALESCE(SUM(
			length(CAST(workflow_ref AS BLOB))+length(CAST(workflow_hash AS BLOB))+
			length(CAST(picoclaw_version AS BLOB))+length(CAST(git_commit AS BLOB))+
			length(CAST(workflow_engine AS BLOB))+length(CAST(workflow_schema AS BLOB))+
			length(CAST(validator_fingerprint AS BLOB))+length(CAST(status AS BLOB))
		),0) FROM workflow_validation_stamps) + (SELECT COALESCE(SUM(length(CAST(path_text AS BLOB))+
			length(CAST(message AS BLOB))),0) FROM workflow_validation_issues)`).Scan(
		&runCount, &eventCount, &developmentCount, &validationCount, &runPayloadBytes,
		&eventPayloadBytes, &nativeBytes, &developmentBytes, &validationBytes,
	); err != nil {
		return err
	}
	if runCount > maximumWorkflowRuns || eventCount > maximumWorkflowEvents ||
		developmentCount > maximumWorkflowDevelopmentRecords ||
		validationCount > maximumWorkflowDevelopmentRecords ||
		runPayloadBytes > maximumWorkflowRunTotalBytes ||
		eventPayloadBytes > maximumWorkflowEventTotalBytes ||
		nativeBytes > maximumWorkflowNativeTotalBytes ||
		developmentBytes > int64(maximumWorkflowDevelopmentRecords)*maximumWorkflowDevelopmentBytes ||
		validationBytes > maximumWorkflowManifestBytes {
		return errors.New("workflow database exceeds its aggregate limits")
	}
	var childCount, jobCount, stepCount, humanTaskCount, validationIssueCount int
	var childPayloadBytes int64
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM workflow_run_children),
		(SELECT COUNT(*) FROM workflow_run_jobs),
		(SELECT COUNT(*) FROM workflow_run_steps),
		(SELECT COUNT(*) FROM workflow_human_tasks),
		(SELECT COUNT(*) FROM workflow_validation_issues),
		(SELECT COALESCE(SUM(length(outputs_json)),0) FROM workflow_run_jobs) +
		(SELECT COALESCE(SUM(length(outputs_json)),0) FROM workflow_run_steps) +
		(SELECT COALESCE(SUM(
			length(questions_json)+COALESCE(length(response_schema_json),0)+
			COALESCE(length(gate_form_json),0)+COALESCE(length(gate_workflow_json),0)+
			COALESCE(length(response_json),0)
		),0) FROM workflow_human_tasks)`).Scan(
		&childCount,
		&jobCount,
		&stepCount,
		&humanTaskCount,
		&validationIssueCount,
		&childPayloadBytes,
	); err != nil {
		return err
	}
	if childCount > maximumWorkflowRunChildren ||
		jobCount > maximumWorkflowRunJobs ||
		stepCount > maximumWorkflowRunSteps ||
		humanTaskCount > maximumWorkflowHumanTasks ||
		validationIssueCount > maximumWorkflowValidationIssues ||
		childPayloadBytes > maximumWorkflowRunTotalBytes-runPayloadBytes {
		return errors.New("workflow database child storage exceeds its aggregate limits")
	}
	var shapeViolations int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM workflow_runs r
		 LEFT JOIN workflow_run_payloads p ON p.run_id=r.run_id WHERE p.run_id IS NULL) +
		(SELECT COUNT(*) FROM workflow_runs r WHERE r.is_private <>
		 EXISTS(SELECT 1 FROM workflow_private_run_markers m WHERE m.run_id=r.run_id)) +
		(SELECT COUNT(*) FROM workflow_runs r JOIN workflow_run_payloads p ON p.run_id=r.run_id
		 WHERE r.is_private<>(p.private_context_json IS NOT NULL) OR
		 r.is_private<>(r.context_visibility='private')) +
		(SELECT COUNT(*) FROM workflow_runs r WHERE
		 (r.child_ids_is_null=1 AND EXISTS(SELECT 1 FROM workflow_run_children c WHERE c.run_id=r.run_id)) OR
		 (r.jobs_is_null=1 AND EXISTS(SELECT 1 FROM workflow_run_jobs j WHERE j.run_id=r.run_id)) OR
		 (r.steps_is_null=1 AND EXISTS(SELECT 1 FROM workflow_run_steps s WHERE s.run_id=r.run_id)) OR
		 (r.human_tasks_is_null=1 AND EXISTS(SELECT 1 FROM workflow_human_tasks h WHERE h.run_id=r.run_id))) +
		(SELECT COUNT(*) FROM (SELECT run_id FROM workflow_run_children GROUP BY run_id
		 HAVING MIN(position)<>0 OR MAX(position)<>COUNT(*)-1)) +
		(SELECT COUNT(*) FROM (SELECT run_id FROM workflow_run_events GROUP BY run_id
		 HAVING MIN(sequence)<>0 OR MAX(sequence)<>COUNT(*)-1)) +
		(SELECT COUNT(*) FROM (SELECT workflow_ref,issue_kind FROM workflow_validation_issues
		 GROUP BY workflow_ref,issue_kind HAVING MIN(position)<>0 OR MAX(position)<>COUNT(*)-1))`).Scan(
		&shapeViolations,
	); err != nil {
		return err
	}
	if shapeViolations != 0 {
		return errors.New("workflow database normalized child shape is invalid")
	}
	var perParentViolations int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM (SELECT run_id FROM workflow_run_children GROUP BY run_id HAVING COUNT(*)>?)) +
		(SELECT COUNT(*) FROM (SELECT run_id FROM workflow_run_jobs GROUP BY run_id HAVING COUNT(*)>?)) +
		(SELECT COUNT(*) FROM (SELECT run_id FROM workflow_run_steps GROUP BY run_id HAVING COUNT(*)>?)) +
		(SELECT COUNT(*) FROM (SELECT run_id FROM workflow_human_tasks GROUP BY run_id HAVING COUNT(*)>?)) +
		(SELECT COUNT(*) FROM (SELECT run_id FROM workflow_run_events GROUP BY run_id HAVING COUNT(*)>?)) +
		(SELECT COUNT(*) FROM (SELECT workflow_ref FROM workflow_validation_issues
		 GROUP BY workflow_ref HAVING COUNT(*)>?))`,
		maximumWorkflowChildrenPerRun,
		maximumWorkflowJobsPerRun,
		maximumWorkflowStepsPerRun,
		maximumWorkflowHumanTasksPerRun,
		maximumWorkflowEventsPerRun,
		maximumWorkflowIssuesPerStamp,
	).Scan(&perParentViolations); err != nil {
		return err
	}
	if perParentViolations != 0 {
		return errors.New("workflow database per-parent child limit is invalid")
	}
	return validateWorkflowCanonicalJSON(ctx, conn)
}

func validateWorkflowCanonicalJSON(ctx context.Context, conn *sql.Conn) error {
	columns := []struct{ table, column string }{
		{"workflow_run_payloads", "event_json"},
		{"workflow_run_payloads", "inputs_json"},
		{"workflow_run_payloads", "outputs_json"},
		{"workflow_run_payloads", "delivery_handles_json"},
		{"workflow_run_payloads", "execution_json"},
		{"workflow_run_payloads", "private_context_json"},
		{"workflow_run_jobs", "outputs_json"},
		{"workflow_run_steps", "outputs_json"},
		{"workflow_human_tasks", "questions_json"},
		{"workflow_human_tasks", "response_schema_json"},
		{"workflow_human_tasks", "gate_form_json"},
		{"workflow_human_tasks", "gate_workflow_json"},
		{"workflow_human_tasks", "response_json"},
		{"workflow_run_events", "payload_json"},
		{"workflow_native_state", "value_json"},
		{"workflow_development_sessions", "validation_json"},
		{"workflow_development_sessions", "last_test_json"},
	}
	for _, item := range columns {
		if err := func() error {
			rows, queryErr := conn.QueryContext(ctx, "SELECT "+item.column+" FROM "+item.table+
				" WHERE "+item.column+" IS NOT NULL")
			if queryErr != nil {
				return queryErr
			}
			defer rows.Close()
			for rows.Next() {
				var data []byte
				if scanErr := rows.Scan(&data); scanErr != nil {
					return scanErr
				}
				canonical, canonicalErr := canonicalWorkflowJSON(data)
				if canonicalErr != nil || !bytes.Equal(canonical, data) {
					return fmt.Errorf("workflow database contains noncanonical JSON in %s.%s", item.table, item.column)
				}
			}
			return rows.Err()
		}(); err != nil {
			return err
		}
	}
	return nil
}

func workflowTimestamp(value time.Time) (int64, int64, error) {
	if value.Year() < 0 || value.Year() > 9999 {
		return 0, 0, fmt.Errorf("workflow timestamp is outside the supported range")
	}
	return value.Unix(), int64(value.Nanosecond()), nil
}

func nullableWorkflowTimestamp(value *time.Time) (any, any, error) {
	if value == nil {
		return nil, nil, nil
	}
	seconds, nanoseconds, err := workflowTimestamp(*value)
	return seconds, nanoseconds, err
}

func workflowTime(seconds, nanoseconds int64) time.Time {
	return time.Unix(seconds, nanoseconds).UTC()
}

func workflowDatabaseError(operation string, err error) error {
	if err == nil {
		return nil
	}
	for _, sentinel := range []error{
		ErrPrivateWorkflowContext,
		ErrHumanTaskNotFound,
		ErrHumanTaskConflict,
		ErrRunCanceled,
	} {
		if errors.Is(err, sentinel) {
			return sentinel
		}
	}
	for _, domainErr := range []error{
		ErrRunAlreadyExists,
		ErrHumanTaskStale,
		ErrHumanTaskResponseInvalid,
		ErrRunConcurrencyLimit,
		ErrRunVersionConflict,
		ErrInvalidCancelReason,
	} {
		if errors.Is(err, domainErr) {
			return err
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return os.ErrNotExist
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return fmt.Errorf("%w: %s workflow database: %v", ErrWorkflowStorageUnavailable, operation, err)
}
