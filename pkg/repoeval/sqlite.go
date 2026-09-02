package repoeval

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
	"sync"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
)

type retainedEvaluationDatabase struct {
	mu     sync.RWMutex
	db     *sql.DB
	closed bool
}

const (
	evaluationDatabaseFilename   = "evaluations.db"
	evaluationDatabaseComponent  = "repository-evaluations"
	evaluationLegacyArchiveLabel = "repository-evaluations-v1"
)

const evaluationTableSchema = `CREATE TABLE repository_evaluations (
    evaluation_id             TEXT PRIMARY KEY,
    schema_version            INTEGER NOT NULL CHECK(schema_version = 1),
    version                   INTEGER NOT NULL CHECK(version > 0),
    status                    TEXT NOT NULL CHECK(status IN (
        'draft', 'preflighting', 'ready', 'running', 'judging', 'analyzing',
        'completed', 'canceling', 'canceled', 'failed'
    )),
    one_shot                  INTEGER NOT NULL CHECK(one_shot IN (0, 1)),
    repository                TEXT NOT NULL,
    ref                       TEXT NOT NULL,
    selector_model_alias      TEXT NOT NULL,
    judge_model_alias         TEXT NOT NULL,
    profile_id                TEXT,
    profile_version           INTEGER,
    progress_stage            TEXT NOT NULL CHECK(progress_stage IN (
        'idle', 'resolving', 'inventorying', 'classifying', 'selecting',
        'validating', 'candidate-execution', 'judging', 'analyzing',
        'completed', 'canceling', 'canceled', 'failed'
    )),
    progress_percent          REAL NOT NULL CHECK(progress_percent >= 0 AND progress_percent <= 100),
    progress_updated_at_nano  INTEGER NOT NULL,
    failure                   TEXT NOT NULL,
    created_at_unix_nano      INTEGER NOT NULL,
    updated_at_unix_nano      INTEGER NOT NULL,
    started_at_unix_nano      INTEGER,
    finished_at_unix_nano     INTEGER,
    payload_json              BLOB NOT NULL CHECK(length(payload_json) <= 33554432),
    CHECK(length(CAST(evaluation_id AS BLOB)) = 36),
    CHECK(substr(evaluation_id, 1, 4) = 'rme_'),
    CHECK(length(CAST(repository AS BLOB)) BETWEEN 1 AND 4096),
    CHECK(length(CAST(ref AS BLOB)) BETWEEN 1 AND 4096),
    CHECK(length(CAST(selector_model_alias AS BLOB)) BETWEEN 1 AND 256),
    CHECK(length(CAST(judge_model_alias AS BLOB)) BETWEEN 1 AND 256),
    CHECK((profile_id IS NULL AND profile_version IS NULL) OR
          (profile_id IS NOT NULL AND profile_version > 0)),
    CHECK(updated_at_unix_nano >= created_at_unix_nano)
) STRICT`

const evaluationModelsSchema = `CREATE TABLE repository_evaluation_models (
    evaluation_id TEXT NOT NULL,
    position      INTEGER NOT NULL CHECK(position >= 0 AND position < 8),
    model_alias   TEXT NOT NULL CHECK(length(CAST(model_alias AS BLOB)) BETWEEN 1 AND 256),
    PRIMARY KEY(evaluation_id, position),
    UNIQUE(evaluation_id, model_alias),
    FOREIGN KEY(evaluation_id) REFERENCES repository_evaluations(evaluation_id) ON DELETE CASCADE
) STRICT`

const evaluationRunsSchema = `CREATE TABLE repository_evaluation_runs (
    evaluation_id TEXT NOT NULL,
    position      INTEGER NOT NULL CHECK(position >= 0 AND position < 10000),
    run_id        TEXT NOT NULL CHECK(length(CAST(run_id AS BLOB)) BETWEEN 1 AND 1024),
    PRIMARY KEY(evaluation_id, position),
    FOREIGN KEY(evaluation_id) REFERENCES repository_evaluations(evaluation_id) ON DELETE CASCADE
) STRICT`

const evaluationUpdatedIndexSchema = `CREATE INDEX repository_evaluations_updated_idx
    ON repository_evaluations(updated_at_unix_nano DESC, evaluation_id)`

const evaluationStatusIndexSchema = `CREATE INDEX repository_evaluations_status_idx
    ON repository_evaluations(status, updated_at_unix_nano DESC, evaluation_id)`

const evaluationRepositoryIndexSchema = `CREATE INDEX repository_evaluations_repository_idx
    ON repository_evaluations(repository, updated_at_unix_nano DESC, evaluation_id)`

const evaluationProfileIndexSchema = `CREATE INDEX repository_evaluations_profile_idx
    ON repository_evaluations(profile_id, updated_at_unix_nano DESC, evaluation_id)
    WHERE profile_id IS NOT NULL`

type evaluationPayload struct {
	Focus                    Focus                                `json:"focus"`
	Profile                  *ProfileSnapshot                     `json:"profile,omitempty"`
	DefaultFilesPerLanguage  int                                  `json:"default_files_per_language"`
	FilesPerLanguage         map[string]int                       `json:"files_per_language"`
	WorkSizingPlan           []WorkSizingPoint                    `json:"work_sizing_plan,omitempty"`
	WorkSizingUsage          map[string]map[string]Usage          `json:"work_sizing_usage,omitempty"`
	WorkSizingConcreteModels map[string]map[string]map[string]int `json:"work_sizing_concrete_models,omitempty"`
	WorkSizingResults        []WorkSizingModelResult              `json:"work_sizing_results,omitempty"`
	Corpus                   *CorpusManifest                      `json:"corpus,omitempty"`
	Progress                 Progress                             `json:"progress"`
	Usage                    Usage                                `json:"usage"`
	ModelStats               map[string]ModelStats                `json:"model_stats"`
	Checkpoint               Checkpoint                           `json:"checkpoint,omitempty"`
	Comparisons              []ModelComparison                    `json:"comparisons"`
	Warnings                 []string                             `json:"warnings"`
}

func (s Store) open(ctx context.Context) (*sql.DB, error) {
	if err := s.localProviderError(); err != nil {
		return nil, err
	}
	if s.broker != nil {
		return nil, database.NewError(database.CodeUnsupported, "repository evaluation operation is not broker-routed")
	}
	if s.openForTest != nil {
		return s.openForTest(ctx)
	}
	databasePath := filepath.Join(s.root, evaluationDatabaseFilename)
	databasePath, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, fmt.Errorf("resolve repository evaluation database: %w", err)
	}
	return sqlitestore.Open(ctx, databasePath, evaluationStoreOptions(filepath.Dir(databasePath)))
}

func (s Store) acquire(ctx context.Context) (*sql.DB, func(), error) {
	if err := s.localProviderError(); err != nil {
		return nil, nil, err
	}
	if s.retained != nil {
		s.retained.mu.RLock()
		if s.retained.closed || s.retained.db == nil {
			s.retained.mu.RUnlock()
			return nil, nil, errors.New("repository evaluation database is closed")
		}
		return s.retained.db, s.retained.mu.RUnlock, nil
	}
	db, err := s.open(ctx)
	if err != nil {
		return nil, nil, err
	}
	return db, func() { _ = db.Close() }, nil
}

func newRetainedEvaluationStore(workspace string) (Store, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() &&
		!allowUnfencedEvaluationProviderForTests.Load() {
		return Store{}, database.NewError(
			database.CodeUnauthorized,
			"repository evaluation retained store requires online database fencing",
		)
	}
	store := newSQLiteStoreLocal(workspace)
	store.brokerOwned = true
	db, err := store.open(context.Background())
	if err != nil {
		return Store{}, err
	}
	store.retained = &retainedEvaluationDatabase{db: db}
	return store, nil
}

func (s Store) Close() error {
	if s.retained == nil {
		return nil
	}
	s.retained.mu.Lock()
	defer s.retained.mu.Unlock()
	if s.retained.closed {
		return nil
	}
	s.retained.closed = true
	if s.retained.db == nil {
		return nil
	}
	err := s.retained.db.Close()
	s.retained.db = nil
	return err
}

func evaluationStoreOptions(root string) sqlitestore.Options {
	return sqlitestore.Options{
		Component: evaluationDatabaseComponent,
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{
				evaluationTableSchema,
				evaluationModelsSchema,
				evaluationRunsSchema,
				evaluationUpdatedIndexSchema,
				evaluationStatusIndexSchema,
				evaluationRepositoryIndexSchema,
				evaluationProfileIndexSchema,
			},
		}},
		Validate: validateEvaluationDatabaseSchema,
		Legacy: &sqlitestore.LegacyOptions{
			SourceRoot:    root,
			ArchiveRoot:   filepath.Join(root, "legacy-json", evaluationLegacyArchiveLabel),
			Sources:       func() ([]sqlitestore.LegacySource, error) { return legacyEvaluationSources(root) },
			Import:        importLegacyEvaluation,
			MaxBytes:      maxStateFileBytes,
			MaxSources:    maxEvaluations,
			MaxTotalBytes: int64(maxEvaluations) * maxStateFileBytes,
		},
	}
}

func validateEvaluationDatabaseSchema(ctx context.Context, conn *sql.Conn) error {
	objects := []struct {
		kind, name, schema string
	}{
		{"table", "repository_evaluations", evaluationTableSchema},
		{"table", "repository_evaluation_models", evaluationModelsSchema},
		{"table", "repository_evaluation_runs", evaluationRunsSchema},
		{"index", "repository_evaluations_updated_idx", evaluationUpdatedIndexSchema},
		{"index", "repository_evaluations_status_idx", evaluationStatusIndexSchema},
		{"index", "repository_evaluations_repository_idx", evaluationRepositoryIndexSchema},
		{"index", "repository_evaluations_profile_idx", evaluationProfileIndexSchema},
	}
	for _, object := range objects {
		if err := sqlitestore.ValidateSchemaObject(ctx, conn, object.kind, object.name, object.schema); err != nil {
			return err
		}
	}
	for _, table := range []string{
		"repository_evaluations", "repository_evaluation_models", "repository_evaluation_runs",
	} {
		if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, table); err != nil {
			return err
		}
	}
	if err := validateEvaluationSchemaObjectSet(ctx, conn); err != nil {
		return err
	}
	return validateEvaluationAggregateRows(ctx, conn)
}

func validateEvaluationSchemaObjectSet(ctx context.Context, conn *sql.Conn) error {
	var unexpected int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type IN ('table', 'index', 'view', 'trigger')
		  AND name NOT LIKE 'sqlite_%'
		  AND name NOT IN (
		    'repository_evaluations', 'repository_evaluation_models',
		    'repository_evaluation_runs', 'repository_evaluations_updated_idx',
		    'repository_evaluations_status_idx', 'repository_evaluations_repository_idx',
		    'repository_evaluations_profile_idx',
		    'storage_imports', 'storage_import_issues', 'storage_import_horizons',
		    'storage_imports_archive_status_idx'
		  )`).Scan(&unexpected); err != nil {
		return err
	}
	if unexpected != 0 {
		return errors.New("repository evaluation database has unexpected schema objects")
	}
	return nil
}

func validateEvaluationAggregateRows(ctx context.Context, conn *sql.Conn) error {
	var invalid int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM repository_evaluations AS evaluation
		 WHERE (SELECT COUNT(*) FROM repository_evaluation_models AS model
		         WHERE model.evaluation_id = evaluation.evaluation_id) NOT BETWEEN 2 AND 8
		    OR (SELECT COALESCE(MIN(position), 0) FROM repository_evaluation_models AS model
		         WHERE model.evaluation_id = evaluation.evaluation_id) <> 0
		    OR (SELECT COALESCE(MAX(position), -1) FROM repository_evaluation_models AS model
		         WHERE model.evaluation_id = evaluation.evaluation_id) <>
		       (SELECT COUNT(*) - 1 FROM repository_evaluation_models AS model
		         WHERE model.evaluation_id = evaluation.evaluation_id)
		    OR (SELECT COALESCE(MIN(position), 0) FROM repository_evaluation_runs AS run
		         WHERE run.evaluation_id = evaluation.evaluation_id) <> 0
		    OR (SELECT COALESCE(MAX(position), -1) FROM repository_evaluation_runs AS run
		         WHERE run.evaluation_id = evaluation.evaluation_id) <>
		       (SELECT COUNT(*) - 1 FROM repository_evaluation_runs AS run
		         WHERE run.evaluation_id = evaluation.evaluation_id)
		    OR json_valid(CAST(payload_json AS TEXT)) <> 1`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return errors.New("repository evaluation rows violate aggregate invariants")
	}
	return nil
}

func legacyEvaluationSources(root string) ([]sqlitestore.LegacySource, error) {
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
			return nil, fmt.Errorf("repository evaluation legacy entry %q is a symlink", entry.Name())
		}
		if !evaluationStateFilename(entry.Name()) {
			continue
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil, fmt.Errorf("repository evaluation legacy entry %q is not a regular file", entry.Name())
		}
		relative := filepath.ToSlash(entry.Name())
		digest := sha256.Sum256([]byte(relative))
		sources = append(sources, sqlitestore.LegacySource{
			ID:       "evaluation-" + hex.EncodeToString(digest[:12]),
			Relative: relative,
			MaxBytes: maxStateFileBytes,
		})
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Relative < sources[j].Relative })
	return sources, nil
}

func importLegacyEvaluation(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	malformed := func(code string) sqlitestore.ImportResult {
		return sqlitestore.ImportResult{
			Skipped: 1,
			Issues:  []sqlitestore.ImportIssue{{Code: code, RecordDigest: input.Digest}},
		}
	}
	var evaluation Evaluation
	if err := json.Unmarshal(input.Data, &evaluation); err != nil {
		//nolint:nilerr // Invalid legacy records are intentionally audited and skipped.
		return malformed("malformed_json"), nil
	}
	filename := filepath.Base(filepath.FromSlash(input.Relative))
	id := strings.TrimSuffix(strings.TrimPrefix(filename, stateNamePrefix), stateFileSuffix)
	if !validEvaluationID(id) || evaluation.ID != id {
		return malformed("invalid_identity"), nil
	}
	if err := validateEvaluation(evaluation); err != nil {
		//nolint:nilerr // Invalid legacy records are intentionally audited and skipped.
		return malformed("invalid_record"), nil
	}
	inserted, err := insertEvaluationConn(ctx, conn, evaluation, true)
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	if !inserted {
		return malformed("duplicate_identity"), nil
	}
	return sqlitestore.ImportResult{Imported: 1}, nil
}

func (s Store) exists(ctx context.Context, id string) (bool, error) {
	database, release, err := s.acquire(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	var one int
	err = database.QueryRowContext(ctx,
		`SELECT 1 FROM repository_evaluations WHERE evaluation_id = ?`, id,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func saveEvaluation(ctx context.Context, database *sql.DB, evaluation Evaluation, exclusive bool) error {
	return sqlitestore.Immediate(ctx, database, func(conn *sql.Conn) error {
		inserted, err := insertEvaluationConn(ctx, conn, evaluation, exclusive)
		if err != nil {
			return err
		}
		if exclusive && !inserted {
			return os.ErrExist
		}
		if !exclusive && !inserted {
			return classifyEvaluationUpdateMiss(ctx, conn, evaluation.ID)
		}
		return nil
	})
}

func classifyEvaluationUpdateMiss(ctx context.Context, conn *sql.Conn, id string) error {
	var present int
	err := conn.QueryRowContext(ctx,
		`SELECT 1 FROM repository_evaluations WHERE evaluation_id = ?`, id,
	).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return os.ErrNotExist
	}
	if err != nil {
		return err
	}
	return ErrConflict
}

func insertEvaluationConn(
	ctx context.Context,
	conn *sql.Conn,
	evaluation Evaluation,
	exclusive bool,
) (bool, error) {
	payload, err := encodeEvaluationPayload(evaluation)
	if err != nil {
		return false, err
	}
	profileID, profileVersion := nullableProfile(evaluation.Profile)
	startedAt := nullableTimeNano(evaluation.StartedAt)
	finishedAt := nullableTimeNano(evaluation.FinishedAt)
	arguments := []any{
		evaluation.ID, evaluation.SchemaVersion, evaluation.Version, string(evaluation.Status),
		boolInteger(evaluation.OneShot), evaluation.Repository, evaluation.Ref,
		evaluation.SelectorModelAlias, evaluation.JudgeModelAlias, profileID, profileVersion,
		string(evaluation.Progress.Stage), evaluation.Progress.Percent,
		evaluation.Progress.UpdatedAt.UnixNano(), evaluation.Failure,
		evaluation.CreatedAt.UnixNano(), evaluation.UpdatedAt.UnixNano(), startedAt, finishedAt, payload,
	}
	var result sql.Result
	if exclusive {
		result, err = conn.ExecContext(ctx, `
			INSERT OR IGNORE INTO repository_evaluations (
				evaluation_id, schema_version, version, status, one_shot, repository, ref,
				selector_model_alias, judge_model_alias, profile_id, profile_version,
				progress_stage, progress_percent, progress_updated_at_nano, failure,
				created_at_unix_nano, updated_at_unix_nano, started_at_unix_nano,
				finished_at_unix_nano, payload_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, arguments...)
	} else {
		arguments = append(arguments, evaluation.ID, evaluation.Version-1)
		result, err = conn.ExecContext(ctx, `
			UPDATE repository_evaluations SET
				schema_version = ?, version = ?, status = ?, one_shot = ?, repository = ?, ref = ?,
				selector_model_alias = ?, judge_model_alias = ?, profile_id = ?, profile_version = ?,
				progress_stage = ?, progress_percent = ?, progress_updated_at_nano = ?, failure = ?,
				created_at_unix_nano = ?, updated_at_unix_nano = ?, started_at_unix_nano = ?,
				finished_at_unix_nano = ?, payload_json = ?
			 WHERE evaluation_id = ? AND version = ?`, arguments[1:]...)
	}
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return false, err
	}
	if _, err := conn.ExecContext(ctx,
		`DELETE FROM repository_evaluation_models WHERE evaluation_id = ?`, evaluation.ID,
	); err != nil {
		return false, err
	}
	for position, alias := range evaluation.CandidateModels {
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO repository_evaluation_models (evaluation_id, position, model_alias)
			VALUES (?, ?, ?)`, evaluation.ID, position, alias); err != nil {
			return false, err
		}
	}
	if _, err := conn.ExecContext(ctx,
		`DELETE FROM repository_evaluation_runs WHERE evaluation_id = ?`, evaluation.ID,
	); err != nil {
		return false, err
	}
	for position, runID := range evaluation.RunIDs {
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO repository_evaluation_runs (evaluation_id, position, run_id)
			VALUES (?, ?, ?)`, evaluation.ID, position, runID); err != nil {
			return false, err
		}
	}
	return true, nil
}

func loadEvaluation(ctx context.Context, queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, id string,
) (Evaluation, error) {
	var evaluation Evaluation
	var payload []byte
	var oneShot int
	var profileID sql.NullString
	var profileVersion sql.NullInt64
	var startedAt sql.NullInt64
	var finishedAt sql.NullInt64
	var status, progressStage string
	var createdAt, updatedAt, progressUpdatedAt int64
	if err := queryer.QueryRowContext(ctx, `
		SELECT evaluation_id, schema_version, version, status, one_shot, repository, ref,
		       selector_model_alias, judge_model_alias, profile_id, profile_version,
		       progress_stage, progress_percent, progress_updated_at_nano, failure,
		       created_at_unix_nano, updated_at_unix_nano, started_at_unix_nano,
		       finished_at_unix_nano, payload_json
		  FROM repository_evaluations
		 WHERE evaluation_id = ?`, id).Scan(
		&evaluation.ID, &evaluation.SchemaVersion, &evaluation.Version, &status, &oneShot,
		&evaluation.Repository, &evaluation.Ref, &evaluation.SelectorModelAlias,
		&evaluation.JudgeModelAlias, &profileID, &profileVersion, &progressStage,
		&evaluation.Progress.Percent, &progressUpdatedAt, &evaluation.Failure,
		&createdAt, &updatedAt, &startedAt, &finishedAt, &payload,
	); err != nil {
		return Evaluation{}, err
	}
	var decoded evaluationPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return Evaluation{}, errors.New("repository evaluation payload is invalid")
	}
	if string(decoded.Progress.Stage) != progressStage ||
		decoded.Progress.Percent != evaluation.Progress.Percent ||
		decoded.Progress.UpdatedAt.UTC().UnixNano() != progressUpdatedAt {
		return Evaluation{}, errors.New("repository evaluation progress projection mismatch")
	}
	evaluation.Status = Status(status)
	evaluation.OneShot = oneShot == 1
	evaluation.Focus = decoded.Focus
	evaluation.Profile = decoded.Profile
	evaluation.DefaultFilesPerLanguage = decoded.DefaultFilesPerLanguage
	evaluation.FilesPerLanguage = decoded.FilesPerLanguage
	evaluation.WorkSizingPlan = decoded.WorkSizingPlan
	evaluation.WorkSizingUsage = decoded.WorkSizingUsage
	evaluation.WorkSizingConcreteModels = decoded.WorkSizingConcreteModels
	evaluation.WorkSizingResults = decoded.WorkSizingResults
	evaluation.Corpus = decoded.Corpus
	evaluation.Progress = decoded.Progress
	evaluation.Progress.Stage = ProgressStage(progressStage)
	evaluation.Progress.UpdatedAt = time.Unix(0, progressUpdatedAt).UTC()
	evaluation.Usage = decoded.Usage
	evaluation.ModelStats = decoded.ModelStats
	evaluation.Checkpoint = decoded.Checkpoint
	evaluation.Comparisons = decoded.Comparisons
	evaluation.Warnings = decoded.Warnings
	evaluation.CreatedAt = time.Unix(0, createdAt).UTC()
	evaluation.UpdatedAt = time.Unix(0, updatedAt).UTC()
	evaluation.StartedAt = timeFromNullNano(startedAt)
	evaluation.FinishedAt = timeFromNullNano(finishedAt)
	//nolint:rowserrcheck // ScanStrings checks rows.Err and closes the result set.
	modelRows, err := queryer.QueryContext(ctx, `
		SELECT model_alias FROM repository_evaluation_models
		 WHERE evaluation_id = ? ORDER BY position`, id)
	if err != nil {
		return Evaluation{}, err
	}
	evaluation.CandidateModels, err = sqlitestore.ScanStrings(modelRows)
	if err != nil {
		return Evaluation{}, err
	}
	//nolint:rowserrcheck // ScanStrings checks rows.Err and closes the result set.
	runRows, err := queryer.QueryContext(ctx, `
		SELECT run_id FROM repository_evaluation_runs
		 WHERE evaluation_id = ? ORDER BY position`, id)
	if err != nil {
		return Evaluation{}, err
	}
	evaluation.RunIDs, err = sqlitestore.ScanStrings(runRows)
	if err != nil {
		return Evaluation{}, err
	}
	if profileID.Valid != (evaluation.Profile != nil) ||
		profileID.Valid && (evaluation.Profile.ID != profileID.String ||
			evaluation.Profile.Version != profileVersion.Int64) {
		return Evaluation{}, errors.New("repository evaluation profile identity mismatch")
	}
	if err := validateEvaluation(evaluation); err != nil {
		return Evaluation{}, err
	}
	return evaluation, nil
}

func encodeEvaluationPayload(evaluation Evaluation) ([]byte, error) {
	payload, err := json.Marshal(evaluationPayload{
		Focus: evaluation.Focus, Profile: evaluation.Profile,
		DefaultFilesPerLanguage: evaluation.DefaultFilesPerLanguage,
		FilesPerLanguage:        evaluation.FilesPerLanguage, WorkSizingPlan: evaluation.WorkSizingPlan,
		WorkSizingUsage:          evaluation.WorkSizingUsage,
		WorkSizingConcreteModels: evaluation.WorkSizingConcreteModels,
		WorkSizingResults:        evaluation.WorkSizingResults, Corpus: evaluation.Corpus,
		Progress: evaluation.Progress, Usage: evaluation.Usage, ModelStats: evaluation.ModelStats,
		Checkpoint: evaluation.Checkpoint, Comparisons: evaluation.Comparisons, Warnings: evaluation.Warnings,
	})
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > maxStateFileBytes {
		return nil, errors.New("repository evaluation state exceeds its size limit")
	}
	return payload, nil
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableProfile(profile *ProfileSnapshot) (any, any) {
	if profile == nil {
		return nil, nil
	}
	return profile.ID, profile.Version
}

func nullableTimeNano(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().UnixNano()
}

func timeFromNullNano(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	parsed := time.Unix(0, value.Int64).UTC()
	return &parsed
}
