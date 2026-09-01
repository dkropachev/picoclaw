package repoaudit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	repositoryReviewDatabaseFilename   = "repository-reviews.db"
	repositoryReviewDatabaseComponent  = "repository-reviews"
	repositoryReviewLegacyArchiveLabel = "repository-reviews-v1"
	maxRepositoryReviewLegacySources   = 50_000
)

const repositoryReviewStatesSchema = `CREATE TABLE repository_review_states (
    state_id                  TEXT PRIMARY KEY,
    repository                TEXT NOT NULL UNIQUE,
    schema_version            INTEGER NOT NULL CHECK(schema_version > 0),
    version                   INTEGER NOT NULL CHECK(version > 0),
    review_version            INTEGER NOT NULL CHECK(review_version >= 0 AND review_version <= version),
    last_commit_sha           TEXT NOT NULL,
    finding_count             INTEGER NOT NULL CHECK(finding_count >= 0),
    repository_finding_count  INTEGER NOT NULL CHECK(repository_finding_count >= 0),
    open_finding_count        INTEGER NOT NULL CHECK(open_finding_count >= 0),
    issue_draft_count         INTEGER NOT NULL CHECK(issue_draft_count >= 0),
    unsupported_count         INTEGER NOT NULL CHECK(unsupported_count >= 0),
    reviewed_file_count       INTEGER NOT NULL CHECK(reviewed_file_count >= 0),
    excluded_file_count       INTEGER NOT NULL CHECK(excluded_file_count >= 0),
    updated_at_unix_nano      INTEGER NOT NULL,
    payload_json              BLOB NOT NULL CHECK(length(payload_json) <= 67108864),
    CHECK(length(CAST(state_id AS BLOB)) = 68),
    CHECK(substr(state_id, 1, 4) = 'rrp_'),
    CHECK(length(CAST(repository AS BLOB)) BETWEEN 1 AND 4096),
    CHECK(length(CAST(last_commit_sha AS BLOB)) <= 256)
) STRICT`

const repositoryReviewRecordsSchema = `CREATE TABLE repository_review_records (
    state_id              TEXT NOT NULL,
    record_kind           TEXT NOT NULL CHECK(record_kind IN (
        'run', 'finding', 'raw_finding', 'deduplicated_finding', 'issue_draft', 'repository_finding'
    )),
    position              INTEGER NOT NULL CHECK(position >= 0 AND position < 100000),
    record_id             TEXT NOT NULL CHECK(length(CAST(record_id AS BLOB)) BETWEEN 1 AND 1024),
    status                TEXT NOT NULL,
    version               INTEGER NOT NULL CHECK(version >= 0),
    created_at_unix_nano  INTEGER NOT NULL,
    updated_at_unix_nano  INTEGER NOT NULL,
    PRIMARY KEY(state_id, record_kind, position),
    UNIQUE(state_id, record_kind, record_id),
    FOREIGN KEY(state_id) REFERENCES repository_review_states(state_id) ON DELETE CASCADE
) STRICT`

const repositoryReviewProfilesSchema = `CREATE TABLE repository_review_profiles (
    profile_id                         TEXT PRIMARY KEY,
    schema_version                     INTEGER NOT NULL CHECK(schema_version > 0),
    version                            INTEGER NOT NULL CHECK(version > 0),
    name                               TEXT NOT NULL,
    review_focus                       TEXT NOT NULL,
    scope_free_text                    TEXT NOT NULL,
    reviewer_model                     TEXT NOT NULL,
    deduplication_model                TEXT NOT NULL,
    deduplication_similarity_threshold INTEGER NOT NULL CHECK(deduplication_similarity_threshold BETWEEN 0 AND 100),
    deduplication_candidate_limit      INTEGER NOT NULL CHECK(deduplication_candidate_limit >= 0),
    issue_writer_model                 TEXT NOT NULL,
    issue_prompt                       TEXT NOT NULL,
    account_ref                        TEXT NOT NULL,
    force_enabled                      INTEGER NOT NULL CHECK(force_enabled IN (0, 1)),
    auto_continue                      INTEGER NOT NULL CHECK(auto_continue IN (0, 1)),
    max_files_per_run                  INTEGER NOT NULL CHECK(max_files_per_run > 0),
    max_content_bytes                  INTEGER NOT NULL CHECK(max_content_bytes > 0),
    max_parallel_children              INTEGER NOT NULL CHECK(max_parallel_children > 0),
    assignment_timeout_seconds         INTEGER NOT NULL CHECK(assignment_timeout_seconds > 0),
    guard_expression                   TEXT NOT NULL,
    created_at_unix_nano               INTEGER NOT NULL,
    updated_at_unix_nano               INTEGER NOT NULL,
    CHECK(length(CAST(profile_id AS BLOB)) BETWEEN 6 AND 128),
    CHECK(substr(profile_id, 1, 5) = 'rrpf_'),
    CHECK(updated_at_unix_nano >= created_at_unix_nano)
) STRICT`

const repositoryReviewProfileScopeSchema = `CREATE TABLE repository_review_profile_scope (
    profile_id  TEXT NOT NULL,
    scope_kind  TEXT NOT NULL CHECK(scope_kind IN ('code_type', 'include_folder', 'exclude_folder')),
    position    INTEGER NOT NULL CHECK(position >= 0 AND position < 64),
    scope_value TEXT NOT NULL CHECK(length(CAST(scope_value AS BLOB)) BETWEEN 1 AND 1024),
    PRIMARY KEY(profile_id, scope_kind, position),
    UNIQUE(profile_id, scope_kind, scope_value),
    FOREIGN KEY(profile_id) REFERENCES repository_review_profiles(profile_id) ON DELETE CASCADE
) STRICT`

const repositoryReviewAutomationsSchema = `CREATE TABLE repository_review_automations (
    automation_id            TEXT PRIMARY KEY,
    schema_version           INTEGER NOT NULL CHECK(schema_version > 0),
    version                  INTEGER NOT NULL CHECK(version > 0),
    profile_id               TEXT,
    profile_version          INTEGER,
    name                     TEXT NOT NULL,
    repository               TEXT NOT NULL,
    canonical_repository     TEXT NOT NULL,
    ref                      TEXT NOT NULL,
    status                   TEXT NOT NULL CHECK(status IN ('idle', 'running', 'stopping', 'paused', 'completed', 'failed')),
    pause_reason             TEXT NOT NULL,
    campaign_id              TEXT NOT NULL,
    active_run_id            TEXT NOT NULL,
    resolved_commit_sha      TEXT NOT NULL,
    progress_stage           TEXT NOT NULL,
    completed_batches        INTEGER NOT NULL CHECK(completed_batches >= 0),
    total_batches            INTEGER NOT NULL CHECK(total_batches >= 0),
    reviewed_files           INTEGER NOT NULL CHECK(reviewed_files >= 0),
    remaining_files          INTEGER NOT NULL CHECK(remaining_files >= 0),
    finding_count            INTEGER NOT NULL CHECK(finding_count >= 0),
    created_at_unix_nano     INTEGER NOT NULL,
    updated_at_unix_nano     INTEGER NOT NULL,
    started_at_unix_nano     INTEGER,
    completed_at_unix_nano   INTEGER,
    payload_json             BLOB NOT NULL CHECK(length(payload_json) <= 4194304),
    CHECK(length(CAST(automation_id AS BLOB)) BETWEEN 5 AND 128),
    CHECK(substr(automation_id, 1, 4) = 'rra_'),
    CHECK(length(CAST(repository AS BLOB)) BETWEEN 1 AND 4096),
    CHECK(length(CAST(canonical_repository AS BLOB)) BETWEEN 1 AND 4096),
    CHECK((profile_id IS NULL AND profile_version IS NULL) OR
          (profile_id IS NOT NULL AND profile_version > 0)),
    CHECK(updated_at_unix_nano >= created_at_unix_nano),
    FOREIGN KEY(profile_id) REFERENCES repository_review_profiles(profile_id) ON DELETE RESTRICT
) STRICT`

const repositoryReviewAutomationModelsSchema = `CREATE TABLE repository_review_automation_models (
    automation_id TEXT NOT NULL,
    position      INTEGER NOT NULL CHECK(position >= 0 AND position < 8),
    model_alias   TEXT NOT NULL CHECK(length(CAST(model_alias AS BLOB)) BETWEEN 1 AND 256),
    PRIMARY KEY(automation_id, position),
    UNIQUE(automation_id, model_alias),
    FOREIGN KEY(automation_id) REFERENCES repository_review_automations(automation_id) ON DELETE CASCADE
) STRICT`

const repositoryReviewAutomationRunsSchema = `CREATE TABLE repository_review_automation_runs (
    automation_id TEXT NOT NULL,
    position      INTEGER NOT NULL CHECK(position >= 0 AND position < 10000),
    run_id        TEXT NOT NULL CHECK(length(CAST(run_id AS BLOB)) BETWEEN 1 AND 1024),
    PRIMARY KEY(automation_id, position),
    FOREIGN KEY(automation_id) REFERENCES repository_review_automations(automation_id) ON DELETE CASCADE
) STRICT`

const repositoryReviewStatesUpdatedIndexSchema = `CREATE INDEX repository_review_states_updated_idx
    ON repository_review_states(updated_at_unix_nano DESC, state_id)`

const repositoryReviewRecordsStatusIndexSchema = `CREATE INDEX repository_review_records_status_idx
    ON repository_review_records(record_kind, status, state_id, position)`

const repositoryReviewProfilesUpdatedIndexSchema = `CREATE INDEX repository_review_profiles_updated_idx
    ON repository_review_profiles(updated_at_unix_nano DESC, profile_id)`

const repositoryReviewAutomationsUpdatedIndexSchema = `CREATE INDEX repository_review_automations_updated_idx
    ON repository_review_automations(updated_at_unix_nano DESC, automation_id)`

const repositoryReviewAutomationsStatusIndexSchema = `CREATE INDEX repository_review_automations_status_idx
    ON repository_review_automations(status, updated_at_unix_nano DESC, automation_id)`

const repositoryReviewAutomationsProfileIndexSchema = `CREATE INDEX repository_review_automations_profile_idx
    ON repository_review_automations(profile_id, status, automation_id) WHERE profile_id IS NOT NULL`

const repositoryReviewAutomationsRepositoryIndexSchema = `CREATE INDEX repository_review_automations_repository_idx
    ON repository_review_automations(repository, status, automation_id)`

const repositoryReviewAutomationsCanonicalIndexSchema = `CREATE UNIQUE INDEX repository_review_automations_canonical_idx
    ON repository_review_automations(canonical_repository) WHERE profile_id IS NOT NULL`

func (s Store) openDatabase(ctx context.Context) (*sql.DB, error) {
	if s.openForTest != nil {
		return s.openForTest(ctx)
	}
	databasePath := filepath.Join(s.root, repositoryReviewDatabaseFilename)
	databasePath, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, fmt.Errorf("resolve repository review database: %w", err)
	}
	root := filepath.Dir(databasePath)
	return sqlitestore.Open(ctx, databasePath, repositoryReviewStoreOptions(root))
}

func repositoryReviewStoreOptions(root string) sqlitestore.Options {
	return sqlitestore.Options{
		Component: repositoryReviewDatabaseComponent,
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{
				repositoryReviewStatesSchema,
				repositoryReviewRecordsSchema,
				repositoryReviewProfilesSchema,
				repositoryReviewProfileScopeSchema,
				repositoryReviewAutomationsSchema,
				repositoryReviewAutomationModelsSchema,
				repositoryReviewAutomationRunsSchema,
				repositoryReviewStatesUpdatedIndexSchema,
				repositoryReviewRecordsStatusIndexSchema,
				repositoryReviewProfilesUpdatedIndexSchema,
				repositoryReviewAutomationsUpdatedIndexSchema,
				repositoryReviewAutomationsStatusIndexSchema,
				repositoryReviewAutomationsProfileIndexSchema,
				repositoryReviewAutomationsRepositoryIndexSchema,
				repositoryReviewAutomationsCanonicalIndexSchema,
			},
		}},
		Validate: validateRepositoryReviewDatabaseSchema,
		Legacy: &sqlitestore.LegacyOptions{
			SourceRoot:    root,
			ArchiveRoot:   filepath.Join(root, "legacy-json", repositoryReviewLegacyArchiveLabel),
			Sources:       func() ([]sqlitestore.LegacySource, error) { return legacyRepositoryReviewSources(root) },
			Import:        importLegacyRepositoryReviewSource,
			MaxBytes:      maxStateFileBytes,
			MaxSources:    maxRepositoryReviewLegacySources,
			MaxTotalBytes: 1 << 40,
		},
	}
}

func validateRepositoryReviewDatabaseSchema(ctx context.Context, conn *sql.Conn) error {
	objects := []struct{ kind, name, schema string }{
		{"table", "repository_review_states", repositoryReviewStatesSchema},
		{"table", "repository_review_records", repositoryReviewRecordsSchema},
		{"table", "repository_review_profiles", repositoryReviewProfilesSchema},
		{"table", "repository_review_profile_scope", repositoryReviewProfileScopeSchema},
		{"table", "repository_review_automations", repositoryReviewAutomationsSchema},
		{"table", "repository_review_automation_models", repositoryReviewAutomationModelsSchema},
		{"table", "repository_review_automation_runs", repositoryReviewAutomationRunsSchema},
		{"index", "repository_review_states_updated_idx", repositoryReviewStatesUpdatedIndexSchema},
		{"index", "repository_review_records_status_idx", repositoryReviewRecordsStatusIndexSchema},
		{"index", "repository_review_profiles_updated_idx", repositoryReviewProfilesUpdatedIndexSchema},
		{"index", "repository_review_automations_updated_idx", repositoryReviewAutomationsUpdatedIndexSchema},
		{"index", "repository_review_automations_status_idx", repositoryReviewAutomationsStatusIndexSchema},
		{"index", "repository_review_automations_profile_idx", repositoryReviewAutomationsProfileIndexSchema},
		{"index", "repository_review_automations_repository_idx", repositoryReviewAutomationsRepositoryIndexSchema},
		{"index", "repository_review_automations_canonical_idx", repositoryReviewAutomationsCanonicalIndexSchema},
	}
	for _, object := range objects {
		if err := sqlitestore.ValidateSchemaObject(ctx, conn, object.kind, object.name, object.schema); err != nil {
			return err
		}
	}
	for _, table := range []string{
		"repository_review_states", "repository_review_records", "repository_review_profiles",
		"repository_review_profile_scope", "repository_review_automations",
		"repository_review_automation_models", "repository_review_automation_runs",
	} {
		expected := []string(nil)
		if table == "repository_review_automations" {
			expected = []string{"repository_review_automations_canonical_idx"}
		}
		if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, table, expected...); err != nil {
			return err
		}
	}
	return validateRepositoryReviewAggregateRows(ctx, conn)
}

func validateRepositoryReviewAggregateRows(ctx context.Context, conn *sql.Conn) error {
	var invalid int
	if err := conn.QueryRowContext(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM repository_review_states WHERE json_valid(CAST(payload_json AS TEXT)) <> 1) +
		  (SELECT COUNT(*) FROM repository_review_automations WHERE json_valid(CAST(payload_json AS TEXT)) <> 1) +
		  (SELECT COUNT(*) FROM (
		     SELECT state_id, record_kind
		       FROM repository_review_records
		      GROUP BY state_id, record_kind
		     HAVING MIN(position) <> 0 OR MAX(position) <> COUNT(*) - 1
		   )) +
		  (SELECT COUNT(*) FROM repository_review_profiles AS profile
		    WHERE (SELECT COUNT(*) FROM repository_review_profile_scope AS scope
		            WHERE scope.profile_id = profile.profile_id AND scope.scope_kind = 'code_type')
		          NOT BETWEEN 1 AND 4) +
		  (SELECT COUNT(*) FROM (
		     SELECT profile_id, scope_kind
		       FROM repository_review_profile_scope
		      GROUP BY profile_id, scope_kind
		     HAVING MIN(position) <> 0 OR MAX(position) <> COUNT(*) - 1
		   )) +
		  (SELECT COUNT(*) FROM repository_review_automations AS automation
		    WHERE (SELECT COUNT(*) FROM repository_review_automation_models AS model
		            WHERE model.automation_id = automation.automation_id) NOT BETWEEN 1 AND 8
		       OR (SELECT COALESCE(MIN(position), 0) FROM repository_review_automation_models AS model
		            WHERE model.automation_id = automation.automation_id) <> 0
		       OR (SELECT COALESCE(MAX(position), -1) FROM repository_review_automation_models AS model
		            WHERE model.automation_id = automation.automation_id) <>
		          (SELECT COUNT(*) - 1 FROM repository_review_automation_models AS model
		            WHERE model.automation_id = automation.automation_id)
		       OR (SELECT COALESCE(MIN(position), 0) FROM repository_review_automation_runs AS run
		            WHERE run.automation_id = automation.automation_id) <> 0
		       OR (SELECT COALESCE(MAX(position), -1) FROM repository_review_automation_runs AS run
		            WHERE run.automation_id = automation.automation_id) <>
		          (SELECT COUNT(*) - 1 FROM repository_review_automation_runs AS run
		            WHERE run.automation_id = automation.automation_id))`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return errors.New("repository review rows violate aggregate invariants")
	}
	return nil
}

func legacyRepositoryReviewSources(root string) ([]sqlitestore.LegacySource, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sources := make([]sqlitestore.LegacySource, 0)
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("repository review legacy entry %q is a symlink", entry.Name())
		}
		if !legacyRepositoryReviewFilename(entry.Name()) {
			continue
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil, fmt.Errorf("repository review legacy entry %q is not a regular file", entry.Name())
		}
		relative := filepath.ToSlash(entry.Name())
		digest := sha256.Sum256([]byte(relative))
		maximum := maxStateFileBytes
		order := 1
		if strings.HasPrefix(entry.Name(), "profile_") {
			maximum = maxProfileFileBytes
			order = 0
		} else if strings.HasPrefix(entry.Name(), "automation_") {
			maximum = maxAutomationFileBytes
			order = 2
		} else if strings.HasSuffix(entry.Name(), ".summary.json") {
			order = 3
		}
		sources = append(sources, sqlitestore.LegacySource{
			ID: "review-" + hex.EncodeToString(digest[:12]), Relative: relative,
			MaxBytes: maximum, Order: order,
		})
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Relative < sources[j].Relative })
	return sources, nil
}

func legacyRepositoryReviewFilename(name string) bool {
	return strings.HasSuffix(name, ".json") &&
		(strings.HasPrefix(name, "repo_") || strings.HasPrefix(name, "profile_") ||
			strings.HasPrefix(name, "automation_"))
}

func importLegacyRepositoryReviewSource(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	skipped := func(code string) sqlitestore.ImportResult {
		return sqlitestore.ImportResult{Skipped: 1, Issues: []sqlitestore.ImportIssue{{
			Code: code, RecordDigest: input.Digest,
		}}}
	}
	name := filepath.Base(filepath.FromSlash(input.Relative))
	switch {
	case strings.HasSuffix(name, ".summary.json"):
		var summary RepositorySummary
		if err := json.Unmarshal(input.Data, &summary); err != nil ||
			summary.ID != RepositoryID(summary.Repository) || summary.SchemaVersion < 1 ||
			summary.SchemaVersion > SchemaVersion {
			//nolint:nilerr // Invalid legacy records are intentionally audited and skipped.
			return skipped("invalid_summary"), nil
		}
		return sqlitestore.ImportResult{}, nil
	case strings.HasPrefix(name, "repo_"):
		var state RepositoryState
		if err := json.Unmarshal(input.Data, &state); err != nil {
			//nolint:nilerr // Invalid legacy records are intentionally audited and skipped.
			return skipped("malformed_json"), nil
		}
		if _, err := migrateRepositoryState(&state); err != nil {
			//nolint:nilerr // Invalid legacy records are intentionally audited and skipped.
			return skipped("invalid_record"), nil
		}
		backfillCanonicalIssueAssociations(&state)
		expectedID := "rrp_" + strings.TrimSuffix(strings.TrimPrefix(name, "repo_"), ".json")
		if state.ID != expectedID || state.ID != RepositoryID(state.Repository) ||
			validateState(state) != nil {
			//nolint:nilerr // Invalid legacy records are intentionally audited and skipped.
			return skipped("invalid_identity"), nil
		}
		inserted, err := insertRepositoryStateConn(ctx, conn, state, true, 0)
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		if !inserted {
			return skipped("duplicate_identity"), nil
		}
		return sqlitestore.ImportResult{Imported: 1}, nil
	case strings.HasPrefix(name, "profile_"):
		id := strings.TrimSuffix(strings.TrimPrefix(name, "profile_"), ".json")
		profile, err := decodeLegacyRepositoryReviewProfile(id, input.Data)
		if err != nil {
			//nolint:nilerr // Invalid legacy records are intentionally audited and skipped.
			return skipped("invalid_profile"), nil
		}
		inserted, err := insertRepositoryReviewProfileConn(ctx, conn, profile, true, 0)
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		if !inserted {
			return skipped("duplicate_identity"), nil
		}
		return sqlitestore.ImportResult{Imported: 1}, nil
	case strings.HasPrefix(name, "automation_"):
		id := strings.TrimSuffix(strings.TrimPrefix(name, "automation_"), ".json")
		automation, err := decodeLegacyRepositoryReviewAutomation(id, input.Data)
		if err != nil {
			//nolint:nilerr // Invalid legacy records are intentionally audited and skipped.
			return skipped("invalid_automation"), nil
		}
		if automation.ProfileID != "" {
			var found int
			err = conn.QueryRowContext(ctx,
				`SELECT 1 FROM repository_review_profiles WHERE profile_id = ?`, automation.ProfileID,
			).Scan(&found)
			if errors.Is(err, sql.ErrNoRows) {
				return skipped("broken_profile_reference"), nil
			}
			if err != nil {
				return sqlitestore.ImportResult{}, err
			}
		}
		inserted, err := insertRepositoryReviewAutomationConn(ctx, conn, automation, true, 0)
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		if !inserted {
			return skipped("duplicate_identity"), nil
		}
		return sqlitestore.ImportResult{Imported: 1}, nil
	default:
		return skipped("unknown_source"), nil
	}
}

func decodeLegacyRepositoryReviewProfile(id string, data []byte) (RepositoryReviewProfile, error) {
	var profile RepositoryReviewProfile
	_, err := unmarshalRepositoryReviewGuardState(data, &profile)
	if err != nil {
		return RepositoryReviewProfile{}, err
	}
	var legacy map[string]json.RawMessage
	_ = json.Unmarshal(data, &legacy)
	if profile.ID != id {
		return RepositoryReviewProfile{}, errors.New("repository review profile identity mismatch")
	}
	if profile.SchemaVersion >= 1 && profile.SchemaVersion < RepositoryReviewProfileSchemaVersion {
		profile.SchemaVersion = RepositoryReviewProfileSchemaVersion
		if strings.TrimSpace(profile.IssuePrompt) == "" {
			profile.IssuePrompt = DefaultRepositoryReviewIssuePrompt
		}
	}
	if _, exists := legacy["deduplication_similarity_threshold"]; !exists {
		profile.DeduplicationSimilarityThreshold = DeduplicationDefaultThreshold
	}
	if _, exists := legacy["deduplication_candidate_limit"]; !exists {
		profile.DeduplicationCandidateLimit = DeduplicationDefaultCandidateLimit
	}
	if err := normalizeProfile(&profile); err != nil {
		return RepositoryReviewProfile{}, err
	}
	return profile, nil
}

func decodeLegacyRepositoryReviewAutomation(id string, data []byte) (RepositoryReviewAutomation, error) {
	var automation RepositoryReviewAutomation
	if _, err := unmarshalRepositoryReviewGuardState(data, &automation); err != nil {
		return RepositoryReviewAutomation{}, err
	}
	if automation.ID != id {
		return RepositoryReviewAutomation{}, errors.New("repository review automation identity mismatch")
	}
	var persisted map[string]json.RawMessage
	_ = json.Unmarshal(data, &persisted)
	if _, exists := persisted["deduplication_similarity_threshold"]; !exists {
		automation.DeduplicationSimilarityThreshold = DeduplicationDefaultThreshold
	}
	if _, exists := persisted["deduplication_candidate_limit"]; !exists {
		automation.DeduplicationCandidateLimit = DeduplicationDefaultCandidateLimit
	}
	var progress map[string]json.RawMessage
	_ = json.Unmarshal(persisted["progress"], &progress)
	if _, exists := progress["deduplicated_findings"]; !exists {
		automation.Progress.DeduplicatedFindings = automation.Progress.Findings
	}
	if automation.SchemaVersion == 1 {
		automation.SchemaVersion = RepositoryReviewAutomationSchemaVersion
	}
	if err := normalizeAutomation(&automation); err != nil {
		return RepositoryReviewAutomation{}, err
	}
	return automation, nil
}

type repositoryReviewRecord struct {
	kind, id, status string
	position         int
	version          int64
	created, updated time.Time
}

func repositoryReviewStateRecords(state RepositoryState) []repositoryReviewRecord {
	records := make([]repositoryReviewRecord, 0,
		len(state.Runs)+len(state.Findings)+len(state.RawFindings)+len(state.DeduplicatedFindings)+
			len(state.IssueDrafts)+len(state.RepositoryFindings))
	for position, run := range state.Runs {
		records = append(records, repositoryReviewRecord{
			kind: "run", id: run.ID, position: position, created: run.CompletedAt, updated: run.CompletedAt,
		})
	}
	for position, finding := range state.Findings {
		records = append(records, repositoryReviewRecord{
			kind: "finding", id: finding.ID, status: string(finding.Status), position: position,
			version: finding.Version, created: finding.CreatedAt, updated: finding.UpdatedAt,
		})
	}
	for position, finding := range state.RawFindings {
		records = append(records, repositoryReviewRecord{
			kind: "raw_finding", id: finding.ID, status: string(finding.State), position: position,
			version: finding.Version, created: finding.CreatedAt, updated: finding.UpdatedAt,
		})
	}
	for position, finding := range state.DeduplicatedFindings {
		records = append(records, repositoryReviewRecord{
			kind: "deduplicated_finding", id: finding.ID, status: string(finding.Status), position: position,
			version: finding.Version, created: finding.CreatedAt, updated: finding.UpdatedAt,
		})
	}
	for position, draft := range state.IssueDrafts {
		records = append(records, repositoryReviewRecord{
			kind: "issue_draft", id: draft.ID, status: string(draft.State), position: position,
			version: draft.Version, created: draft.CreatedAt, updated: draft.UpdatedAt,
		})
	}
	for position, finding := range state.RepositoryFindings {
		records = append(records, repositoryReviewRecord{
			kind: "repository_finding", id: finding.ID, status: string(finding.Lifecycle), position: position,
			version: finding.Version, created: finding.CreatedAt, updated: finding.UpdatedAt,
		})
	}
	return records
}

func insertRepositoryStateConn(
	ctx context.Context,
	conn *sql.Conn,
	state RepositoryState,
	inserting bool,
	expectedVersion int64,
) (bool, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return false, err
	}
	if int64(len(payload)) > maxStateFileBytes {
		return false, errors.New("repository review state exceeds its size limit")
	}
	summary := Summarize(state)
	arguments := []any{
		state.ID, state.Repository, state.SchemaVersion, state.Version, state.ReviewVersion,
		state.LastCommitSHA, summary.FindingCount, summary.RepositoryFindingCount,
		summary.OpenFindingCount, summary.IssueDraftCount, summary.UnsupportedCount,
		summary.ReviewedFileCount, summary.ExcludedFileCount, state.UpdatedAt.UnixNano(), payload,
	}
	var result sql.Result
	if inserting {
		result, err = conn.ExecContext(ctx, `
			INSERT OR IGNORE INTO repository_review_states (
				state_id, repository, schema_version, version, review_version, last_commit_sha,
				finding_count, repository_finding_count, open_finding_count, issue_draft_count,
				unsupported_count, reviewed_file_count, excluded_file_count, updated_at_unix_nano,
				payload_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, arguments...)
	} else {
		arguments = append(arguments, state.ID, expectedVersion)
		result, err = conn.ExecContext(ctx, `
			UPDATE repository_review_states SET
				repository = ?, schema_version = ?, version = ?, review_version = ?, last_commit_sha = ?,
				finding_count = ?, repository_finding_count = ?, open_finding_count = ?, issue_draft_count = ?,
				unsupported_count = ?, reviewed_file_count = ?, excluded_file_count = ?,
				updated_at_unix_nano = ?, payload_json = ?
			 WHERE state_id = ? AND version = ?`, arguments[1:]...)
	}
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return false, err
	}
	if _, err := conn.ExecContext(ctx,
		`DELETE FROM repository_review_records WHERE state_id = ?`, state.ID,
	); err != nil {
		return false, err
	}
	for _, record := range repositoryReviewStateRecords(state) {
		created := record.created.UTC().UnixNano()
		updated := record.updated.UTC().UnixNano()
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO repository_review_records (
				state_id, record_kind, position, record_id, status, version,
				created_at_unix_nano, updated_at_unix_nano
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, state.ID, record.kind, record.position,
			record.id, record.status, record.version, created, updated); err != nil {
			return false, err
		}
	}
	return true, nil
}

func saveRepositoryStateDatabase(ctx context.Context, database *sql.DB, state *RepositoryState) error {
	if state == nil {
		return errors.New("repository review state is required")
	}
	return sqlitestore.Immediate(ctx, database, func(conn *sql.Conn) error {
		var current int64
		err := conn.QueryRowContext(ctx,
			`SELECT version FROM repository_review_states WHERE state_id = ?`, state.ID,
		).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			if state.Version == 0 {
				state.Version = 1
			}
			inserted, insertErr := insertRepositoryStateConn(ctx, conn, *state, true, 0)
			if insertErr != nil {
				return insertErr
			}
			if !inserted {
				return ErrConflict
			}
			return nil
		}
		if err != nil {
			return err
		}
		expectedVersion := state.Version - 1
		if current == state.Version {
			expectedVersion = current
		} else if current != expectedVersion {
			return ErrConflict
		}
		inserted, err := insertRepositoryStateConn(ctx, conn, *state, false, expectedVersion)
		if err != nil {
			return err
		}
		if !inserted {
			return ErrConflict
		}
		return nil
	})
}

func loadRepositoryStateRow(ctx context.Context, database *sql.DB, id string) (RepositoryState, error) {
	var payload []byte
	var row struct {
		id, repository, lastCommit string
		schema                     int
		version, reviewVersion     int64
		finding, repositoryFinding int
		open, drafts, unsupported  int
		reviewed, excluded         int
		updated                    int64
	}
	err := database.QueryRowContext(ctx, `
		SELECT state_id, repository, schema_version, version, review_version, last_commit_sha,
		       finding_count, repository_finding_count, open_finding_count, issue_draft_count,
		       unsupported_count, reviewed_file_count, excluded_file_count, updated_at_unix_nano,
		       payload_json
		  FROM repository_review_states WHERE state_id = ?`, id).Scan(
		&row.id, &row.repository, &row.schema, &row.version, &row.reviewVersion, &row.lastCommit,
		&row.finding, &row.repositoryFinding, &row.open, &row.drafts, &row.unsupported,
		&row.reviewed, &row.excluded, &row.updated, &payload,
	)
	if err != nil {
		return RepositoryState{}, err
	}
	var state RepositoryState
	if err := json.Unmarshal(payload, &state); err != nil {
		return RepositoryState{}, errors.New("repository review payload is invalid")
	}
	if _, err := migrateRepositoryState(&state); err != nil {
		return RepositoryState{}, err
	}
	backfillCanonicalIssueAssociations(&state)
	summary := Summarize(state)
	if state.ID != row.id || state.Repository != row.repository || state.SchemaVersion != row.schema ||
		state.Version != row.version || state.ReviewVersion != row.reviewVersion ||
		state.LastCommitSHA != row.lastCommit || state.UpdatedAt.UnixNano() != row.updated ||
		summary.FindingCount != row.finding || summary.RepositoryFindingCount != row.repositoryFinding ||
		summary.OpenFindingCount != row.open || summary.IssueDraftCount != row.drafts ||
		summary.UnsupportedCount != row.unsupported || summary.ReviewedFileCount != row.reviewed ||
		summary.ExcludedFileCount != row.excluded {
		return RepositoryState{}, errors.New("repository review typed projection mismatch")
	}
	if err := validateState(state); err != nil {
		return RepositoryState{}, err
	}
	if err := validateRepositoryReviewStateRecords(ctx, database, state); err != nil {
		return RepositoryState{}, err
	}
	return state, nil
}

func validateRepositoryReviewStateRecords(
	ctx context.Context,
	database *sql.DB,
	state RepositoryState,
) error {
	expected := repositoryReviewStateRecords(state)
	sort.Slice(expected, func(left, right int) bool {
		if expected[left].kind != expected[right].kind {
			return expected[left].kind < expected[right].kind
		}
		return expected[left].position < expected[right].position
	})
	rows, err := database.QueryContext(ctx, `
		SELECT record_kind, position, record_id, status, version,
		       created_at_unix_nano, updated_at_unix_nano
		  FROM repository_review_records
		 WHERE state_id = ?
		 ORDER BY record_kind, position`, state.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		if index >= len(expected) {
			return errors.New("repository review relationships contain an unexpected record")
		}
		var kind, id, status string
		var position int
		var version, created, updated int64
		if err := rows.Scan(&kind, &position, &id, &status, &version, &created, &updated); err != nil {
			return err
		}
		want := expected[index]
		if kind != want.kind || position != want.position || id != want.id ||
			status != want.status || version != want.version ||
			created != want.created.UTC().UnixNano() || updated != want.updated.UTC().UnixNano() {
			return errors.New("repository review relationship projection mismatch")
		}
		index++
	}
	if index != len(expected) {
		return errors.New("repository review relationships are incomplete")
	}
	return rows.Err()
}

func insertRepositoryReviewProfileConn(
	ctx context.Context,
	conn *sql.Conn,
	profile RepositoryReviewProfile,
	inserting bool,
	expectedVersion int64,
) (bool, error) {
	arguments := []any{
		profile.ID, profile.SchemaVersion, profile.Version, profile.Name, profile.ReviewFocus,
		profile.ScopePolicy.FreeText, profile.ReviewerModel, profile.DeduplicationModel,
		profile.DeduplicationSimilarityThreshold, profile.DeduplicationCandidateLimit,
		profile.IssueWriterModel, profile.IssuePrompt, profile.AccountRef,
		reviewBoolInteger(profile.Force), reviewBoolInteger(profile.AutoContinue), profile.MaxFilesPerRun,
		profile.MaxContentBytes, profile.MaxParallelChildren, profile.AssignmentTimeoutSeconds,
		profile.BudgetPolicy.GuardExpression, profile.CreatedAt.UnixNano(), profile.UpdatedAt.UnixNano(),
	}
	var result sql.Result
	var err error
	if inserting {
		result, err = conn.ExecContext(ctx, `
			INSERT OR IGNORE INTO repository_review_profiles (
				profile_id, schema_version, version, name, review_focus, scope_free_text,
				reviewer_model, deduplication_model, deduplication_similarity_threshold,
				deduplication_candidate_limit, issue_writer_model, issue_prompt, account_ref,
				force_enabled, auto_continue, max_files_per_run, max_content_bytes,
				max_parallel_children, assignment_timeout_seconds, guard_expression,
				created_at_unix_nano, updated_at_unix_nano
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, arguments...)
	} else {
		arguments = append(arguments, profile.ID, expectedVersion)
		result, err = conn.ExecContext(ctx, `
			UPDATE repository_review_profiles SET
				schema_version = ?, version = ?, name = ?, review_focus = ?, scope_free_text = ?,
				reviewer_model = ?, deduplication_model = ?, deduplication_similarity_threshold = ?,
				deduplication_candidate_limit = ?, issue_writer_model = ?, issue_prompt = ?, account_ref = ?,
				force_enabled = ?, auto_continue = ?, max_files_per_run = ?, max_content_bytes = ?,
				max_parallel_children = ?, assignment_timeout_seconds = ?, guard_expression = ?,
				created_at_unix_nano = ?, updated_at_unix_nano = ?
			 WHERE profile_id = ? AND version = ?`, arguments[1:]...)
	}
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return false, err
	}
	if _, err := conn.ExecContext(ctx,
		`DELETE FROM repository_review_profile_scope WHERE profile_id = ?`, profile.ID,
	); err != nil {
		return false, err
	}
	values := []struct {
		kind   string
		values []string
	}{
		{"code_type", repositoryReviewCodeTypeStrings(profile.ScopePolicy.CodeTypes)},
		{"include_folder", profile.ScopePolicy.IncludeFolders},
		{"exclude_folder", profile.ScopePolicy.ExcludeFolders},
	}
	for _, group := range values {
		for position, value := range group.values {
			if _, err := conn.ExecContext(ctx, `
				INSERT INTO repository_review_profile_scope (profile_id, scope_kind, position, scope_value)
				VALUES (?, ?, ?, ?)`, profile.ID, group.kind, position, value); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

func saveRepositoryReviewProfileDatabase(ctx context.Context, database *sql.DB, profile RepositoryReviewProfile) error {
	return sqlitestore.Immediate(ctx, database, func(conn *sql.Conn) error {
		var current int64
		err := conn.QueryRowContext(ctx,
			`SELECT version FROM repository_review_profiles WHERE profile_id = ?`, profile.ID,
		).Scan(&current)
		importing := errors.Is(err, sql.ErrNoRows)
		if err != nil && !importing {
			return err
		}
		expectedVersion := profile.Version - 1
		if !importing {
			if current == profile.Version {
				expectedVersion = current
			} else if current != expectedVersion {
				return ErrConflict
			}
		}
		if importing {
			expectedVersion = 0
		}
		inserted, err := insertRepositoryReviewProfileConn(
			ctx, conn, profile, importing, expectedVersion,
		)
		if err != nil {
			return err
		}
		if !inserted {
			return ErrConflict
		}
		return nil
	})
}

func loadRepositoryReviewProfileRow(
	ctx context.Context,
	database *sql.DB,
	id string,
) (RepositoryReviewProfile, error) {
	var profile RepositoryReviewProfile
	var force, auto int
	var created, updated int64
	err := database.QueryRowContext(ctx, `
		SELECT profile_id, schema_version, version, name, review_focus, scope_free_text,
		       reviewer_model, deduplication_model, deduplication_similarity_threshold,
		       deduplication_candidate_limit, issue_writer_model, issue_prompt, account_ref,
		       force_enabled, auto_continue, max_files_per_run, max_content_bytes,
		       max_parallel_children, assignment_timeout_seconds, guard_expression,
		       created_at_unix_nano, updated_at_unix_nano
		  FROM repository_review_profiles WHERE profile_id = ?`, id).Scan(
		&profile.ID, &profile.SchemaVersion, &profile.Version, &profile.Name, &profile.ReviewFocus,
		&profile.ScopePolicy.FreeText, &profile.ReviewerModel, &profile.DeduplicationModel,
		&profile.DeduplicationSimilarityThreshold, &profile.DeduplicationCandidateLimit,
		&profile.IssueWriterModel, &profile.IssuePrompt, &profile.AccountRef, &force, &auto,
		&profile.MaxFilesPerRun, &profile.MaxContentBytes, &profile.MaxParallelChildren,
		&profile.AssignmentTimeoutSeconds, &profile.BudgetPolicy.GuardExpression, &created, &updated,
	)
	if err != nil {
		return RepositoryReviewProfile{}, err
	}
	profile.Force = force == 1
	profile.AutoContinue = auto == 1
	profile.CreatedAt = time.Unix(0, created).UTC()
	profile.UpdatedAt = time.Unix(0, updated).UTC()
	rows, err := database.QueryContext(ctx, `
		SELECT scope_kind, scope_value FROM repository_review_profile_scope
		 WHERE profile_id = ? ORDER BY scope_kind, position`, id)
	if err != nil {
		return RepositoryReviewProfile{}, err
	}
	for rows.Next() {
		var kind, value string
		if scanErr := rows.Scan(&kind, &value); scanErr != nil {
			//nolint:sqlclosecheck // Close immediately before returning the scan failure.
			rows.Close()
			return RepositoryReviewProfile{}, scanErr
		}
		switch kind {
		case "code_type":
			profile.ScopePolicy.CodeTypes = append(profile.ScopePolicy.CodeTypes, RepositoryReviewCodeType(value))
		case "include_folder":
			profile.ScopePolicy.IncludeFolders = append(profile.ScopePolicy.IncludeFolders, value)
		case "exclude_folder":
			profile.ScopePolicy.ExcludeFolders = append(profile.ScopePolicy.ExcludeFolders, value)
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err == nil {
		if normalizeErr := normalizeProfile(&profile); normalizeErr != nil {
			return RepositoryReviewProfile{}, normalizeErr
		}
	}
	return profile, err
}

func insertRepositoryReviewAutomationConn(
	ctx context.Context,
	conn *sql.Conn,
	automation RepositoryReviewAutomation,
	inserting bool,
	expectedVersion int64,
) (bool, error) {
	payload, err := json.Marshal(automation)
	if err != nil {
		return false, err
	}
	if validationErr := validateEncodedAutomationSize(payload); validationErr != nil {
		return false, validationErr
	}
	profileID, profileVersion := nullableReviewProfile(automation.ProfileID, automation.ProfileVersion)
	arguments := []any{
		automation.ID, automation.SchemaVersion, automation.Version, profileID, profileVersion,
		automation.Name, automation.Repository, canonicalAutomationRepository(automation.Repository),
		automation.Ref, string(automation.Status),
		string(automation.PauseReason), automation.CampaignID, automation.ActiveRunID,
		automation.ResolvedCommitSHA, automation.Progress.Stage, automation.Progress.CompletedBatches,
		automation.Progress.TotalBatches, automation.Progress.ReviewedFiles,
		automation.Progress.RemainingFiles, automation.Progress.DeduplicatedFindings,
		automation.CreatedAt.UnixNano(), automation.UpdatedAt.UnixNano(),
		nullableReviewTime(automation.StartedAt), nullableReviewTime(automation.CompletedAt), payload,
	}
	var result sql.Result
	if inserting {
		result, err = conn.ExecContext(ctx, `
			INSERT OR IGNORE INTO repository_review_automations (
				automation_id, schema_version, version, profile_id, profile_version, name,
				repository, canonical_repository, ref, status, pause_reason, campaign_id, active_run_id,
				resolved_commit_sha, progress_stage, completed_batches, total_batches,
				reviewed_files, remaining_files, finding_count, created_at_unix_nano,
				updated_at_unix_nano, started_at_unix_nano, completed_at_unix_nano, payload_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, arguments...)
	} else {
		arguments = append(arguments, automation.ID, expectedVersion)
		result, err = conn.ExecContext(ctx, `
			UPDATE repository_review_automations SET
				schema_version = ?, version = ?, profile_id = ?, profile_version = ?, name = ?,
				repository = ?, canonical_repository = ?, ref = ?, status = ?, pause_reason = ?, campaign_id = ?, active_run_id = ?,
				resolved_commit_sha = ?, progress_stage = ?, completed_batches = ?, total_batches = ?,
				reviewed_files = ?, remaining_files = ?, finding_count = ?, created_at_unix_nano = ?,
				updated_at_unix_nano = ?, started_at_unix_nano = ?, completed_at_unix_nano = ?, payload_json = ?
			 WHERE automation_id = ? AND version = ?`, arguments[1:]...)
	}
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return false, err
	}
	if _, err := conn.ExecContext(ctx,
		`DELETE FROM repository_review_automation_models WHERE automation_id = ?`, automation.ID,
	); err != nil {
		return false, err
	}
	for position, alias := range automation.ReviewerModels {
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO repository_review_automation_models (automation_id, position, model_alias)
			VALUES (?, ?, ?)`, automation.ID, position, alias); err != nil {
			return false, err
		}
	}
	if _, err := conn.ExecContext(ctx,
		`DELETE FROM repository_review_automation_runs WHERE automation_id = ?`, automation.ID,
	); err != nil {
		return false, err
	}
	for position, runID := range automation.RunIDs {
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO repository_review_automation_runs (automation_id, position, run_id)
			VALUES (?, ?, ?)`, automation.ID, position, runID); err != nil {
			return false, err
		}
	}
	return true, nil
}

func saveRepositoryReviewAutomationDatabase(
	ctx context.Context,
	database *sql.DB,
	automation RepositoryReviewAutomation,
) error {
	return sqlitestore.Immediate(ctx, database, func(conn *sql.Conn) error {
		var current int64
		err := conn.QueryRowContext(ctx,
			`SELECT version FROM repository_review_automations WHERE automation_id = ?`, automation.ID,
		).Scan(&current)
		importing := errors.Is(err, sql.ErrNoRows)
		if err != nil && !importing {
			return err
		}
		expectedVersion := automation.Version - 1
		if !importing {
			if current == automation.Version {
				expectedVersion = current
			} else if current != expectedVersion {
				return ErrConflict
			}
		}
		if importing {
			expectedVersion = 0
		}
		inserted, err := insertRepositoryReviewAutomationConn(
			ctx, conn, automation, importing, expectedVersion,
		)
		if err != nil {
			return err
		}
		if !inserted {
			return ErrConflict
		}
		return nil
	})
}

func loadRepositoryReviewAutomationRow(
	ctx context.Context,
	database *sql.DB,
	id string,
) (RepositoryReviewAutomation, error) {
	var automation RepositoryReviewAutomation
	var payload []byte
	var profileID sql.NullString
	var profileVersion sql.NullInt64
	var status, pauseReason, progressStage, canonicalRepository string
	var created, updated int64
	var started, completed sql.NullInt64
	var completedBatches, totalBatches, reviewed, remaining, findingCount int
	err := database.QueryRowContext(ctx, `
		SELECT automation_id, schema_version, version, profile_id, profile_version, name,
		       repository, canonical_repository, ref, status, pause_reason, campaign_id, active_run_id,
		       resolved_commit_sha, progress_stage, completed_batches, total_batches,
		       reviewed_files, remaining_files, finding_count, created_at_unix_nano,
		       updated_at_unix_nano, started_at_unix_nano, completed_at_unix_nano, payload_json
		  FROM repository_review_automations WHERE automation_id = ?`, id).Scan(
		&automation.ID, &automation.SchemaVersion, &automation.Version, &profileID, &profileVersion,
		&automation.Name, &automation.Repository, &canonicalRepository, &automation.Ref, &status, &pauseReason,
		&automation.CampaignID, &automation.ActiveRunID, &automation.ResolvedCommitSHA, &progressStage,
		&completedBatches, &totalBatches, &reviewed, &remaining, &findingCount, &created, &updated,
		&started, &completed, &payload,
	)
	if err != nil {
		return RepositoryReviewAutomation{}, err
	}
	var decoded RepositoryReviewAutomation
	if decodeErr := json.Unmarshal(payload, &decoded); decodeErr != nil {
		return RepositoryReviewAutomation{}, errors.New("repository review automation payload is invalid")
	}
	if decoded.ID != automation.ID || decoded.SchemaVersion != automation.SchemaVersion ||
		decoded.Version != automation.Version || decoded.Name != automation.Name ||
		decoded.Repository != automation.Repository || decoded.Ref != automation.Ref ||
		string(decoded.Status) != status || string(decoded.PauseReason) != pauseReason ||
		decoded.CampaignID != automation.CampaignID || decoded.ActiveRunID != automation.ActiveRunID ||
		decoded.ResolvedCommitSHA != automation.ResolvedCommitSHA || decoded.Progress.Stage != progressStage ||
		decoded.Progress.CompletedBatches != completedBatches || decoded.Progress.TotalBatches != totalBatches ||
		decoded.Progress.ReviewedFiles != reviewed || decoded.Progress.RemainingFiles != remaining ||
		decoded.Progress.DeduplicatedFindings != findingCount || decoded.CreatedAt.UnixNano() != created ||
		decoded.UpdatedAt.UnixNano() != updated || decoded.ProfileID != profileID.String ||
		decoded.ProfileVersion != profileVersion.Int64 ||
		canonicalAutomationRepository(decoded.Repository) != canonicalRepository ||
		!reviewNullTimeMatches(decoded.StartedAt, started) ||
		!reviewNullTimeMatches(decoded.CompletedAt, completed) {
		return RepositoryReviewAutomation{}, errors.New("repository review automation typed projection mismatch")
	}
	automation = decoded
	// The relational columns make presence explicit even when both configured
	// values are zero. Preserve the non-persisted normalization hint used by
	// the domain model so explicit zero does not become the legacy default.
	automation.DeduplicationSettingsSpecified = true
	models, err := loadReviewOrderedStrings(ctx, database,
		`SELECT model_alias FROM repository_review_automation_models WHERE automation_id = ? ORDER BY position`, id)
	if err != nil {
		return RepositoryReviewAutomation{}, err
	}
	runs, err := loadReviewOrderedStrings(ctx, database,
		`SELECT run_id FROM repository_review_automation_runs WHERE automation_id = ? ORDER BY position`, id)
	if err != nil {
		return RepositoryReviewAutomation{}, err
	}
	if !equalReviewStrings(models, automation.ReviewerModels) || !equalReviewStrings(runs, automation.RunIDs) {
		return RepositoryReviewAutomation{}, errors.New("repository review automation relationships mismatch")
	}
	if err := normalizeAutomation(&automation); err != nil {
		return RepositoryReviewAutomation{}, err
	}
	return automation, nil
}

func loadReviewOrderedStrings(
	ctx context.Context,
	database *sql.DB,
	query string,
	id string,
) ([]string, error) {
	rows, err := database.QueryContext(ctx, query, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func repositoryReviewCodeTypeStrings(values []RepositoryReviewCodeType) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func reviewBoolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableReviewProfile(id string, version int64) (any, any) {
	if id == "" {
		return nil, nil
	}
	return id, version
}

func nullableReviewTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().UnixNano()
}

func reviewNullTimeMatches(value time.Time, encoded sql.NullInt64) bool {
	if value.IsZero() {
		return !encoded.Valid
	}
	return encoded.Valid && value.UTC().UnixNano() == encoded.Int64
}

func equalReviewStrings(left, right []string) bool {
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

// RewriteStateForMigration replaces one already-persisted repository ledger
// without changing its public version. It is intentionally narrow: callers
// must supply the exact current version, and the rewrite is one transactional
// compare-and-swap used by trusted compatibility/backfill code.
func (s Store) RewriteStateForMigration(
	ctx context.Context,
	state RepositoryState,
) (RepositoryState, error) {
	reconcileFindingsProcessingCounters(&state)
	if err := prepareRepositoryStateForMigrationRewrite(&state); err != nil {
		return RepositoryState{}, err
	}
	err := s.rewriteMigrationRow(
		ctx,
		state.Repository,
		`SELECT version FROM repository_review_states WHERE state_id = ?`,
		state.ID,
		state.Version,
		func(ctx context.Context, conn *sql.Conn, current int64) (bool, error) {
			return insertRepositoryStateConn(ctx, conn, state, false, current)
		},
	)
	if err != nil {
		return RepositoryState{}, err
	}
	return state, nil
}

func prepareRepositoryStateForMigrationRewrite(state *RepositoryState) error {
	if state == nil {
		return errors.New("repository review state is required")
	}
	if state.FileAttributions == nil {
		state.FileAttributions = []RepositoryReviewFileAttribution{}
	}
	backfillCanonicalIssueAssociations(state)
	summary := Summarize(*state)
	state.FindingCount = summary.FindingCount
	state.RepositoryFindingCount = summary.RepositoryFindingCount
	state.OpenFindingCount = summary.OpenFindingCount
	state.IssueDraftCount = summary.IssueDraftCount
	state.UnsupportedCount = summary.UnsupportedCount
	state.ReviewedFileCount = summary.ReviewedFileCount
	return validateState(*state)
}

// RewriteProfileForMigration performs a same-version CAS rewrite for trusted
// legacy normalization. Ordinary profile mutations must use UpdateProfile.
func (s Store) RewriteProfileForMigration(
	ctx context.Context,
	profile RepositoryReviewProfile,
) (RepositoryReviewProfile, error) {
	if err := normalizeProfile(&profile); err != nil {
		return RepositoryReviewProfile{}, err
	}
	err := s.rewriteMigrationRow(
		ctx,
		"profile:"+profile.ID,
		`SELECT version FROM repository_review_profiles WHERE profile_id = ?`,
		profile.ID,
		profile.Version,
		func(ctx context.Context, conn *sql.Conn, current int64) (bool, error) {
			return insertRepositoryReviewProfileConn(ctx, conn, profile, false, current)
		},
	)
	if err != nil {
		return RepositoryReviewProfile{}, err
	}
	return profile, nil
}

// RewriteAutomationForMigration performs a same-version CAS rewrite for
// trusted recovery/backfill code. Ordinary mutations use UpdateAutomation.
func (s Store) RewriteAutomationForMigration(
	ctx context.Context,
	automation RepositoryReviewAutomation,
) (RepositoryReviewAutomation, error) {
	if err := normalizeAutomation(&automation); err != nil {
		return RepositoryReviewAutomation{}, err
	}
	err := s.rewriteMigrationRow(
		ctx,
		"automation:"+automation.ID,
		`SELECT version FROM repository_review_automations WHERE automation_id = ?`,
		automation.ID,
		automation.Version,
		func(ctx context.Context, conn *sql.Conn, current int64) (bool, error) {
			return insertRepositoryReviewAutomationConn(ctx, conn, automation, false, current)
		},
	)
	if err != nil {
		return RepositoryReviewAutomation{}, err
	}
	return automation, nil
}

func (s Store) rewriteMigrationRow(
	ctx context.Context,
	lockKey string,
	versionQuery string,
	id string,
	version int64,
	write func(context.Context, *sql.Conn, int64) (bool, error),
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if write == nil || strings.TrimSpace(versionQuery) == "" {
		return errors.New("repository review migration rewrite is invalid")
	}
	unlock, err := s.lock(lockKey)
	if err != nil {
		return err
	}
	defer unlock()
	database, err := s.openDatabase(ctx)
	if err != nil {
		return err
	}
	defer database.Close()
	return sqlitestore.Immediate(ctx, database, func(conn *sql.Conn) error {
		var current int64
		if queryErr := conn.QueryRowContext(ctx, versionQuery, id).Scan(&current); queryErr != nil {
			if errors.Is(queryErr, sql.ErrNoRows) {
				return os.ErrNotExist
			}
			return queryErr
		}
		if version != current && version != current+1 {
			return ErrConflict
		}
		inserted, rewriteErr := write(ctx, conn, current)
		if rewriteErr != nil {
			return rewriteErr
		}
		if !inserted {
			return ErrConflict
		}
		return nil
	})
}
