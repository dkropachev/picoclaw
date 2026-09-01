package evolution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	evolutionDatabaseComponent = "evolution"
	evolutionSchemaVersion     = 1

	maximumEvolutionRecords       = 100_000
	maximumEvolutionDrafts        = 100_000
	maximumEvolutionProfiles      = 100_000
	maximumEvolutionChildren      = 10_000
	maximumEvolutionTextBytes     = 4 << 20
	maximumEvolutionSourceBytes   = 4 << 20
	maximumEvolutionLegacyBytes   = 64 << 20
	maximumEvolutionLegacyTotal   = 256 << 20
	maximumEvolutionLegacySources = 100_005
	maximumEvolutionAuditIssues   = 512
)

const evolutionRecordsSchema = `CREATE TABLE IF NOT EXISTS evolution_records (
    record_class          TEXT NOT NULL CHECK(record_class IN ('task', 'pattern')),
    workspace_id          TEXT NOT NULL CHECK(length(workspace_id) BETWEEN 1 AND 4096 AND instr(workspace_id, char(0)) = 0),
    record_id             TEXT NOT NULL CHECK(length(record_id) BETWEEN 1 AND 1024 AND instr(record_id, char(0)) = 0),
    position              INTEGER NOT NULL CHECK(position >= 0 AND position < 100000),
    kind                  TEXT NOT NULL CHECK(kind IN ('task', 'pattern', 'case', 'rule')),
    created_at_unix_nano  INTEGER NOT NULL CHECK(created_at_unix_nano > 0),
    updated_at_unix_nano  INTEGER CHECK(updated_at_unix_nano IS NULL OR updated_at_unix_nano > 0),
    session_key           TEXT NOT NULL CHECK(length(session_key) <= 4096 AND instr(session_key, char(0)) = 0),
    task_hash             TEXT NOT NULL CHECK(length(task_hash) <= 1024 AND instr(task_hash, char(0)) = 0),
    summary               TEXT NOT NULL CHECK(length(summary) <= 4194304 AND instr(summary, char(0)) = 0),
    user_goal             TEXT NOT NULL CHECK(length(user_goal) <= 4194304 AND instr(user_goal, char(0)) = 0),
    final_output          TEXT NOT NULL CHECK(length(final_output) <= 4194304 AND instr(final_output, char(0)) = 0),
    source_json           BLOB CHECK(source_json IS NULL OR (typeof(source_json) = 'blob' AND length(source_json) <= 4194304 AND json_valid(CAST(source_json AS TEXT)))),
    status                TEXT NOT NULL CHECK(status IN ('', 'new', 'clustered', 'ready')),
    success               INTEGER CHECK(success IS NULL OR success IN (0, 1)),
    label                 TEXT NOT NULL CHECK(length(label) <= 4194304 AND instr(label, char(0)) = 0),
    cluster_reason        TEXT NOT NULL CHECK(length(cluster_reason) <= 4194304 AND instr(cluster_reason, char(0)) = 0),
    event_count           INTEGER NOT NULL CHECK(event_count >= 0),
    success_rate          REAL NOT NULL,
    maturity_score        REAL NOT NULL,
    final_snapshot_trigger TEXT NOT NULL CHECK(length(final_snapshot_trigger) <= 4194304 AND instr(final_snapshot_trigger, char(0)) = 0),
    import_source         TEXT CHECK(import_source IS NULL OR import_source IN ('learning-records', 'task-records', 'pattern-records')),
    version               INTEGER NOT NULL CHECK(version > 0),
    PRIMARY KEY(record_class, workspace_id, record_id),
    UNIQUE(record_class, position)
) STRICT;`

const evolutionRecordStringsSchema = `CREATE TABLE IF NOT EXISTS evolution_record_strings (
    record_class TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    record_id    TEXT NOT NULL,
    field_name   TEXT NOT NULL CHECK(field_name IN (
        'tool_kinds', 'initial_skill_names', 'added_skill_names', 'used_skill_names',
        'all_loaded_skill_names', 'active_skill_names', 'signals', 'source_record_ids',
        'task_record_ids', 'winning_path', 'late_added_skills', 'matched_skill_names'
    )),
    position     INTEGER NOT NULL CHECK(position >= 0 AND position < 10000),
    value        TEXT NOT NULL CHECK(length(value) <= 4194304 AND instr(value, char(0)) = 0),
    PRIMARY KEY(record_class, workspace_id, record_id, field_name, position),
    FOREIGN KEY(record_class, workspace_id, record_id)
        REFERENCES evolution_records(record_class, workspace_id, record_id) ON DELETE CASCADE
) STRICT;`

const evolutionToolExecutionsSchema = `CREATE TABLE IF NOT EXISTS evolution_record_tool_executions (
    record_class  TEXT NOT NULL,
    workspace_id  TEXT NOT NULL,
    record_id     TEXT NOT NULL,
    position      INTEGER NOT NULL CHECK(position >= 0 AND position < 10000),
    name          TEXT NOT NULL CHECK(length(name) <= 4096 AND instr(name, char(0)) = 0),
    success       INTEGER NOT NULL CHECK(success IN (0, 1)),
    error_summary TEXT NOT NULL CHECK(length(error_summary) <= 4194304 AND instr(error_summary, char(0)) = 0),
    PRIMARY KEY(record_class, workspace_id, record_id, position),
    FOREIGN KEY(record_class, workspace_id, record_id)
        REFERENCES evolution_records(record_class, workspace_id, record_id) ON DELETE CASCADE
) STRICT;`

const evolutionToolExecutionSkillsSchema = `CREATE TABLE IF NOT EXISTS evolution_record_tool_execution_skills (
    record_class       TEXT NOT NULL,
    workspace_id       TEXT NOT NULL,
    record_id          TEXT NOT NULL,
    execution_position INTEGER NOT NULL,
    position           INTEGER NOT NULL CHECK(position >= 0 AND position < 10000),
    skill_name         TEXT NOT NULL CHECK(length(skill_name) <= 4096 AND instr(skill_name, char(0)) = 0),
    PRIMARY KEY(record_class, workspace_id, record_id, execution_position, position),
    FOREIGN KEY(record_class, workspace_id, record_id, execution_position)
        REFERENCES evolution_record_tool_executions(record_class, workspace_id, record_id, position) ON DELETE CASCADE
) STRICT;`

const evolutionAttemptTrailsSchema = `CREATE TABLE IF NOT EXISTS evolution_record_attempt_trails (
    record_class TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    record_id    TEXT NOT NULL,
    PRIMARY KEY(record_class, workspace_id, record_id),
    FOREIGN KEY(record_class, workspace_id, record_id)
        REFERENCES evolution_records(record_class, workspace_id, record_id) ON DELETE CASCADE
) STRICT;`

const evolutionAttemptStringsSchema = `CREATE TABLE IF NOT EXISTS evolution_record_attempt_strings (
    record_class TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    record_id    TEXT NOT NULL,
    field_name   TEXT NOT NULL CHECK(field_name IN ('attempted_skills', 'final_successful_path')),
    position     INTEGER NOT NULL CHECK(position >= 0 AND position < 10000),
    value        TEXT NOT NULL CHECK(length(value) <= 4194304 AND instr(value, char(0)) = 0),
    PRIMARY KEY(record_class, workspace_id, record_id, field_name, position),
    FOREIGN KEY(record_class, workspace_id, record_id)
        REFERENCES evolution_record_attempt_trails(record_class, workspace_id, record_id) ON DELETE CASCADE
) STRICT;`

const evolutionAttemptSnapshotsSchema = `CREATE TABLE IF NOT EXISTS evolution_record_attempt_snapshots (
    record_class TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    record_id    TEXT NOT NULL,
    position     INTEGER NOT NULL CHECK(position >= 0 AND position < 10000),
    sequence     INTEGER NOT NULL,
    trigger_name TEXT NOT NULL CHECK(length(trigger_name) <= 4194304 AND instr(trigger_name, char(0)) = 0),
    PRIMARY KEY(record_class, workspace_id, record_id, position),
    FOREIGN KEY(record_class, workspace_id, record_id)
        REFERENCES evolution_record_attempt_trails(record_class, workspace_id, record_id) ON DELETE CASCADE
) STRICT;`

const evolutionAttemptSnapshotSkillsSchema = `CREATE TABLE IF NOT EXISTS evolution_record_attempt_snapshot_skills (
    record_class      TEXT NOT NULL,
    workspace_id      TEXT NOT NULL,
    record_id         TEXT NOT NULL,
    snapshot_position INTEGER NOT NULL,
    position          INTEGER NOT NULL CHECK(position >= 0 AND position < 10000),
    skill_name        TEXT NOT NULL CHECK(length(skill_name) <= 4096 AND instr(skill_name, char(0)) = 0),
    PRIMARY KEY(record_class, workspace_id, record_id, snapshot_position, position),
    FOREIGN KEY(record_class, workspace_id, record_id, snapshot_position)
        REFERENCES evolution_record_attempt_snapshots(record_class, workspace_id, record_id, position) ON DELETE CASCADE
) STRICT;`

const evolutionDraftsSchema = `CREATE TABLE IF NOT EXISTS evolution_skill_drafts (
    workspace_id         TEXT NOT NULL CHECK(length(workspace_id) BETWEEN 1 AND 4096 AND instr(workspace_id, char(0)) = 0),
    draft_id             TEXT NOT NULL CHECK(length(draft_id) BETWEEN 1 AND 1024 AND instr(draft_id, char(0)) = 0),
    position             INTEGER NOT NULL UNIQUE CHECK(position >= 0 AND position < 100000),
    created_at_unix_nano INTEGER NOT NULL CHECK(created_at_unix_nano >= 0),
    updated_at_unix_nano INTEGER CHECK(updated_at_unix_nano IS NULL OR updated_at_unix_nano >= 0),
    source_record_id     TEXT NOT NULL CHECK(length(source_record_id) <= 1024 AND instr(source_record_id, char(0)) = 0),
    target_skill_name    TEXT NOT NULL CHECK(length(target_skill_name) <= 128 AND instr(target_skill_name, char(0)) = 0),
    draft_type           TEXT NOT NULL CHECK(length(draft_type) <= 64 AND instr(draft_type, char(0)) = 0),
    change_kind          TEXT NOT NULL CHECK(length(change_kind) <= 64 AND instr(change_kind, char(0)) = 0),
    human_summary        TEXT NOT NULL CHECK(length(human_summary) <= 4194304 AND instr(human_summary, char(0)) = 0),
    body_or_patch        TEXT NOT NULL CHECK(length(body_or_patch) <= 4194304 AND instr(body_or_patch, char(0)) = 0),
    status               TEXT NOT NULL CHECK(status IN ('candidate', 'quarantined', 'accepted')),
    version              INTEGER NOT NULL CHECK(version > 0),
    PRIMARY KEY(workspace_id, draft_id)
) STRICT;`

const evolutionDraftStringsSchema = `CREATE TABLE IF NOT EXISTS evolution_skill_draft_strings (
    workspace_id TEXT NOT NULL,
    draft_id     TEXT NOT NULL,
    field_name   TEXT NOT NULL CHECK(field_name IN (
        'matched_skill_refs', 'intended_use_cases', 'preferred_entry_path',
        'avoid_patterns', 'review_notes', 'scan_findings'
    )),
    position     INTEGER NOT NULL CHECK(position >= 0 AND position < 10000),
    value        TEXT NOT NULL CHECK(length(value) <= 4194304 AND instr(value, char(0)) = 0),
    PRIMARY KEY(workspace_id, draft_id, field_name, position),
    FOREIGN KEY(workspace_id, draft_id)
        REFERENCES evolution_skill_drafts(workspace_id, draft_id) ON DELETE CASCADE
) STRICT;`

const evolutionProfilesSchema = `CREATE TABLE IF NOT EXISTS evolution_skill_profiles (
    workspace_id          TEXT NOT NULL CHECK(length(workspace_id) <= 4096 AND instr(workspace_id, char(0)) = 0),
    skill_name            TEXT NOT NULL CHECK(length(skill_name) BETWEEN 1 AND 128 AND instr(skill_name, char(0)) = 0),
    current_version       TEXT NOT NULL CHECK(length(current_version) <= 1024 AND instr(current_version, char(0)) = 0),
    status                TEXT NOT NULL CHECK(status IN ('', 'active', 'cold', 'archived', 'deleted')),
    origin                TEXT NOT NULL CHECK(length(origin) <= 1024 AND instr(origin, char(0)) = 0),
    human_summary         TEXT NOT NULL CHECK(length(human_summary) <= 4194304 AND instr(human_summary, char(0)) = 0),
    change_reason         TEXT NOT NULL CHECK(length(change_reason) <= 4194304 AND instr(change_reason, char(0)) = 0),
    last_used_at_unix_nano INTEGER NOT NULL,
    use_count             INTEGER NOT NULL CHECK(use_count >= 0),
    retention_score       REAL NOT NULL,
    version               INTEGER NOT NULL CHECK(version > 0),
    PRIMARY KEY(workspace_id, skill_name)
) STRICT;`

const evolutionProfileStringsSchema = `CREATE TABLE IF NOT EXISTS evolution_skill_profile_strings (
    workspace_id TEXT NOT NULL,
    skill_name   TEXT NOT NULL,
    field_name   TEXT NOT NULL CHECK(field_name IN ('intended_use_cases', 'preferred_entry_path', 'avoid_patterns')),
    position     INTEGER NOT NULL CHECK(position >= 0 AND position < 10000),
    value        TEXT NOT NULL CHECK(length(value) <= 4194304 AND instr(value, char(0)) = 0),
    PRIMARY KEY(workspace_id, skill_name, field_name, position),
    FOREIGN KEY(workspace_id, skill_name)
        REFERENCES evolution_skill_profiles(workspace_id, skill_name) ON DELETE CASCADE
) STRICT;`

const evolutionProfileVersionsSchema = `CREATE TABLE IF NOT EXISTS evolution_skill_profile_versions (
    workspace_id          TEXT NOT NULL,
    skill_name            TEXT NOT NULL,
    position              INTEGER NOT NULL CHECK(position >= 0 AND position < 10000),
    version_name          TEXT NOT NULL CHECK(length(version_name) <= 1024 AND instr(version_name, char(0)) = 0),
    action_name           TEXT NOT NULL CHECK(length(action_name) <= 1024 AND instr(action_name, char(0)) = 0),
    timestamp_unix_nano   INTEGER NOT NULL,
    draft_id              TEXT NOT NULL CHECK(length(draft_id) <= 1024 AND instr(draft_id, char(0)) = 0),
    summary               TEXT NOT NULL CHECK(length(summary) <= 4194304 AND instr(summary, char(0)) = 0),
    rollback              INTEGER NOT NULL CHECK(rollback IN (0, 1)),
    rollback_reason       TEXT NOT NULL CHECK(length(rollback_reason) <= 4194304 AND instr(rollback_reason, char(0)) = 0),
    PRIMARY KEY(workspace_id, skill_name, position),
    FOREIGN KEY(workspace_id, skill_name)
        REFERENCES evolution_skill_profiles(workspace_id, skill_name) ON DELETE CASCADE
) STRICT;`

const evolutionRecordsWorkspaceIndexSchema = `CREATE INDEX IF NOT EXISTS evolution_records_workspace_idx
    ON evolution_records(workspace_id, record_class, position);`
const evolutionRecordsStatusIndexSchema = `CREATE INDEX IF NOT EXISTS evolution_records_status_idx
    ON evolution_records(record_class, status, position);`
const evolutionDraftsWorkspaceIndexSchema = `CREATE INDEX IF NOT EXISTS evolution_skill_drafts_workspace_idx
    ON evolution_skill_drafts(workspace_id, position);`
const evolutionProfilesStatusIndexSchema = `CREATE INDEX IF NOT EXISTS evolution_skill_profiles_status_idx
    ON evolution_skill_profiles(workspace_id, status, skill_name);`

func evolutionStoreOptions(paths Paths) sqlitestore.Options {
	return sqlitestore.Options{
		Component: evolutionDatabaseComponent,
		Migrations: []sqlitestore.Migration{{
			Version: evolutionSchemaVersion,
			Statements: []string{
				evolutionRecordsSchema,
				evolutionRecordStringsSchema,
				evolutionToolExecutionsSchema,
				evolutionToolExecutionSkillsSchema,
				evolutionAttemptTrailsSchema,
				evolutionAttemptStringsSchema,
				evolutionAttemptSnapshotsSchema,
				evolutionAttemptSnapshotSkillsSchema,
				evolutionDraftsSchema,
				evolutionDraftStringsSchema,
				evolutionProfilesSchema,
				evolutionProfileStringsSchema,
				evolutionProfileVersionsSchema,
				evolutionRecordsWorkspaceIndexSchema,
				evolutionRecordsStatusIndexSchema,
				evolutionDraftsWorkspaceIndexSchema,
				evolutionProfilesStatusIndexSchema,
			},
		}},
		Validate: validateEvolutionSchema,
		Legacy: &sqlitestore.LegacyOptions{
			SourceRoot:    paths.RootDir,
			ArchiveRoot:   paths.LegacyArchive,
			Sources:       func() ([]sqlitestore.LegacySource, error) { return evolutionLegacySources(paths) },
			Import:        importEvolutionLegacySource,
			MaxBytes:      maximumEvolutionLegacyBytes,
			MaxSources:    maximumEvolutionLegacySources,
			MaxTotalBytes: maximumEvolutionLegacyTotal,
		},
	}
}

func validateEvolutionSchema(ctx context.Context, conn *sql.Conn) error {
	objects := []struct{ objectType, name, schema string }{
		{"table", "evolution_records", evolutionRecordsSchema},
		{"table", "evolution_record_strings", evolutionRecordStringsSchema},
		{"table", "evolution_record_tool_executions", evolutionToolExecutionsSchema},
		{"table", "evolution_record_tool_execution_skills", evolutionToolExecutionSkillsSchema},
		{"table", "evolution_record_attempt_trails", evolutionAttemptTrailsSchema},
		{"table", "evolution_record_attempt_strings", evolutionAttemptStringsSchema},
		{"table", "evolution_record_attempt_snapshots", evolutionAttemptSnapshotsSchema},
		{"table", "evolution_record_attempt_snapshot_skills", evolutionAttemptSnapshotSkillsSchema},
		{"table", "evolution_skill_drafts", evolutionDraftsSchema},
		{"table", "evolution_skill_draft_strings", evolutionDraftStringsSchema},
		{"table", "evolution_skill_profiles", evolutionProfilesSchema},
		{"table", "evolution_skill_profile_strings", evolutionProfileStringsSchema},
		{"table", "evolution_skill_profile_versions", evolutionProfileVersionsSchema},
		{"index", "evolution_records_workspace_idx", evolutionRecordsWorkspaceIndexSchema},
		{"index", "evolution_records_status_idx", evolutionRecordsStatusIndexSchema},
		{"index", "evolution_skill_drafts_workspace_idx", evolutionDraftsWorkspaceIndexSchema},
		{"index", "evolution_skill_profiles_status_idx", evolutionProfilesStatusIndexSchema},
	}
	for _, object := range objects {
		if err := sqlitestore.ValidateSchemaObject(ctx, conn, object.objectType, object.name, object.schema); err != nil {
			return err
		}
	}
	for _, table := range []string{
		"evolution_records", "evolution_record_strings", "evolution_record_tool_executions",
		"evolution_record_tool_execution_skills", "evolution_record_attempt_trails",
		"evolution_record_attempt_strings", "evolution_record_attempt_snapshots",
		"evolution_record_attempt_snapshot_skills", "evolution_skill_drafts",
		"evolution_skill_draft_strings", "evolution_skill_profiles",
		"evolution_skill_profile_strings", "evolution_skill_profile_versions",
	} {
		if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, table); err != nil {
			return err
		}
	}
	var records, drafts, profiles int
	if err := conn.QueryRowContext(ctx, `SELECT
        (SELECT COUNT(*) FROM evolution_records),
        (SELECT COUNT(*) FROM evolution_skill_drafts),
        (SELECT COUNT(*) FROM evolution_skill_profiles)`).Scan(&records, &drafts, &profiles); err != nil {
		return err
	}
	if records > 2*maximumEvolutionRecords || drafts > maximumEvolutionDrafts || profiles > maximumEvolutionProfiles {
		return errors.New("evolution database exceeds its aggregate limits")
	}
	if err := validateEvolutionPositions(ctx, conn); err != nil {
		return err
	}
	var unexpected int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		  AND name NOT IN (
              'evolution_records', 'evolution_record_strings',
              'evolution_record_tool_executions', 'evolution_record_tool_execution_skills',
              'evolution_record_attempt_trails', 'evolution_record_attempt_strings',
              'evolution_record_attempt_snapshots', 'evolution_record_attempt_snapshot_skills',
              'evolution_skill_drafts', 'evolution_skill_draft_strings',
			  'evolution_skill_profiles', 'evolution_skill_profile_strings',
			  'evolution_skill_profile_versions', 'storage_imports', 'storage_import_issues'
          )`).Scan(&unexpected); err != nil {
		return err
	}
	if unexpected != 0 {
		return errors.New("evolution schema has unexpected tables")
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type IN ('index', 'trigger')
		  AND name NOT LIKE 'sqlite_autoindex_%'
		  AND name NOT IN (
			  'evolution_records_workspace_idx', 'evolution_records_status_idx',
			  'evolution_skill_drafts_workspace_idx', 'evolution_skill_profiles_status_idx',
			  'storage_imports_archive_status_idx'
          )`).Scan(&unexpected); err != nil {
		return err
	}
	if unexpected != 0 {
		return errors.New("evolution schema has unexpected indexes or triggers")
	}
	return nil
}

func validateEvolutionPositions(ctx context.Context, conn *sql.Conn) error {
	queries := []string{
		`SELECT COUNT(*) FROM (
            SELECT record_class FROM evolution_records GROUP BY record_class
            HAVING MIN(position) <> 0 OR MAX(position) + 1 <> COUNT(*)
        )`,
		`SELECT CASE
            WHEN COUNT(*) = 0 OR (MIN(position) = 0 AND MAX(position) + 1 = COUNT(*)) THEN 0
            ELSE 1
         END FROM evolution_skill_drafts`,
		`SELECT COUNT(*) FROM (
            SELECT record_class, workspace_id, record_id, field_name
              FROM evolution_record_strings GROUP BY record_class, workspace_id, record_id, field_name
            HAVING MIN(position) <> 0 OR MAX(position) + 1 <> COUNT(*)
        )`,
		`SELECT COUNT(*) FROM (
            SELECT record_class, workspace_id, record_id
              FROM evolution_record_tool_executions GROUP BY record_class, workspace_id, record_id
            HAVING MIN(position) <> 0 OR MAX(position) + 1 <> COUNT(*)
        )`,
		`SELECT COUNT(*) FROM (
            SELECT record_class, workspace_id, record_id, execution_position
              FROM evolution_record_tool_execution_skills
             GROUP BY record_class, workspace_id, record_id, execution_position
            HAVING MIN(position) <> 0 OR MAX(position) + 1 <> COUNT(*)
        )`,
		`SELECT COUNT(*) FROM (
            SELECT record_class, workspace_id, record_id, field_name
              FROM evolution_record_attempt_strings
             GROUP BY record_class, workspace_id, record_id, field_name
            HAVING MIN(position) <> 0 OR MAX(position) + 1 <> COUNT(*)
        )`,
		`SELECT COUNT(*) FROM (
            SELECT record_class, workspace_id, record_id
              FROM evolution_record_attempt_snapshots
             GROUP BY record_class, workspace_id, record_id
            HAVING MIN(position) <> 0 OR MAX(position) + 1 <> COUNT(*)
        )`,
		`SELECT COUNT(*) FROM (
            SELECT record_class, workspace_id, record_id, snapshot_position
              FROM evolution_record_attempt_snapshot_skills
             GROUP BY record_class, workspace_id, record_id, snapshot_position
            HAVING MIN(position) <> 0 OR MAX(position) + 1 <> COUNT(*)
        )`,
		`SELECT COUNT(*) FROM (
            SELECT workspace_id, draft_id, field_name
              FROM evolution_skill_draft_strings GROUP BY workspace_id, draft_id, field_name
            HAVING MIN(position) <> 0 OR MAX(position) + 1 <> COUNT(*)
        )`,
		`SELECT COUNT(*) FROM (
            SELECT workspace_id, skill_name, field_name
              FROM evolution_skill_profile_strings GROUP BY workspace_id, skill_name, field_name
            HAVING MIN(position) <> 0 OR MAX(position) + 1 <> COUNT(*)
        )`,
		`SELECT COUNT(*) FROM (
            SELECT workspace_id, skill_name
              FROM evolution_skill_profile_versions GROUP BY workspace_id, skill_name
            HAVING MIN(position) <> 0 OR MAX(position) + 1 <> COUNT(*)
        )`,
	}
	for _, query := range queries {
		var invalid int
		if err := conn.QueryRowContext(ctx, query).Scan(&invalid); err != nil {
			return fmt.Errorf("validate evolution ordering: %w", err)
		}
		if invalid != 0 {
			return errors.New("evolution database has non-contiguous ordered relationships")
		}
	}
	return nil
}

func normalizedEvolutionPaths(paths Paths) Paths {
	if paths.RootDir == "" {
		paths = NewPaths(paths.Workspace, "")
	}
	if paths.Database == "" {
		paths.Database = filepath.Join(paths.RootDir, "evolution.db")
	}
	if paths.LegacyArchive == "" {
		paths.LegacyArchive = filepath.Join(paths.RootDir, "legacy-json", "evolution-v1")
	}
	defaults := NewPaths(paths.Workspace, paths.RootDir)
	if paths.LearningRecords == "" {
		paths.LearningRecords = defaults.LearningRecords
	}
	if paths.TaskRecords == "" {
		paths.TaskRecords = defaults.TaskRecords
	}
	if paths.PatternRecords == "" {
		paths.PatternRecords = defaults.PatternRecords
	}
	if paths.SkillDrafts == "" {
		paths.SkillDrafts = defaults.SkillDrafts
	}
	if paths.ProfilesDir == "" {
		paths.ProfilesDir = defaults.ProfilesDir
	}
	if paths.BackupsDir == "" {
		paths.BackupsDir = defaults.BackupsDir
	}
	return paths
}
