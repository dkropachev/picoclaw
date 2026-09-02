package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	minCacheSniffPromptTokens = 256

	toolAdaptationDatabaseFilename   = "tool-adaptation.db"
	legacyToolAdaptationFilename     = "tool_adaptation_state.json"
	toolAdaptationDatabaseComponent  = "tool-adaptation"
	legacyToolAdaptationSourceID     = "tool-adaptation-json-v1"
	legacyToolAdaptationArchiveLabel = "tool-adaptation-v1"
	legacyToolAdaptationMaxBytes     = int64(64 << 20)

	maximumAdaptationObservations = 100_000
	maximumAdaptationOutcomes     = 1_000_000
	maximumAdaptationProvider     = 256
	maximumAdaptationModel        = 8 << 10
	maximumAdaptationSurface      = 1 << 10
	maximumAdaptationToolName     = 1 << 10
	maximumAdaptationSchemaHash   = 128
	maximumAdaptationError        = 16 << 10
	maximumAdaptationAuditIssues  = 512
)

const toolAdaptationObservationsSchema = `CREATE TABLE tool_adaptation_observations (
    provider                 TEXT NOT NULL,
    model                    TEXT NOT NULL,
    visible_tool_surface     TEXT NOT NULL,
    tool_schema_hash         TEXT NOT NULL,
    prompt_tokens            INTEGER NOT NULL CHECK(prompt_tokens >= 0),
    cached_tokens            INTEGER NOT NULL CHECK(cached_tokens >= 0),
    cache_hit_ratio          REAL NOT NULL,
    cache_sensitive          INTEGER NOT NULL CHECK(cache_sensitive IN (0, 1)),
    sniffed                  INTEGER NOT NULL CHECK(sniffed IN (0, 1)),
    observed_at_unix_nano    INTEGER NOT NULL CHECK(observed_at_unix_nano >= 0),
    version                  INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    PRIMARY KEY(provider, model),
    CHECK(provider <> '' OR model <> ''),
    CHECK(length(CAST(provider AS BLOB)) <= 256),
    CHECK(provider = lower(trim(provider))),
    CHECK(instr(provider, char(0)) = 0),
    CHECK(length(CAST(model AS BLOB)) <= 8192),
    CHECK(model = lower(trim(model))),
    CHECK(instr(model, char(0)) = 0),
    CHECK(length(CAST(visible_tool_surface AS BLOB)) <= 1024),
    CHECK(instr(visible_tool_surface, char(0)) = 0),
    CHECK(length(CAST(tool_schema_hash AS BLOB)) <= 128),
    CHECK(instr(tool_schema_hash, char(0)) = 0)
) STRICT`

const toolAdaptationOutcomesSchema = `CREATE TABLE tool_adaptation_outcomes (
    provider                 TEXT NOT NULL,
    model                    TEXT NOT NULL,
    visible_tool_surface     TEXT NOT NULL,
    tool_name                TEXT NOT NULL,
    successes               INTEGER NOT NULL CHECK(successes >= 0),
    failures                INTEGER NOT NULL CHECK(failures >= 0),
    last_error              TEXT NOT NULL,
    last_duration_ms        INTEGER NOT NULL,
    updated_at_unix_nano    INTEGER NOT NULL CHECK(updated_at_unix_nano >= 0),
    version                 INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    PRIMARY KEY(provider, model, visible_tool_surface, tool_name),
    CHECK(provider <> '' OR model <> ''),
    CHECK(length(CAST(provider AS BLOB)) <= 256),
    CHECK(provider = lower(trim(provider))),
    CHECK(instr(provider, char(0)) = 0),
    CHECK(length(CAST(model AS BLOB)) <= 8192),
    CHECK(model = lower(trim(model))),
    CHECK(instr(model, char(0)) = 0),
    CHECK(length(CAST(visible_tool_surface AS BLOB)) <= 1024),
    CHECK(visible_tool_surface = trim(visible_tool_surface)),
    CHECK(instr(visible_tool_surface, char(0)) = 0),
    CHECK(length(CAST(tool_name AS BLOB)) BETWEEN 1 AND 1024),
    CHECK(tool_name = trim(tool_name)),
    CHECK(instr(tool_name, char(0)) = 0),
    CHECK(length(CAST(last_error AS BLOB)) <= 16384),
    CHECK(instr(last_error, char(0)) = 0)
) STRICT`

const toolAdaptationObservationsTimeIndexSchema = `CREATE INDEX tool_adaptation_observations_time_idx
    ON tool_adaptation_observations(observed_at_unix_nano DESC, provider, model)`

const toolAdaptationOutcomesTimeIndexSchema = `CREATE INDEX tool_adaptation_outcomes_time_idx
    ON tool_adaptation_outcomes(updated_at_unix_nano DESC, provider, model, visible_tool_surface, tool_name)`

// ToolAdaptationProfile identifies the model/API pair whose tool behavior is
// being learned. Provider/model aliases are normalized so UI and runtime calls
// land on the same profile where possible.
type ToolAdaptationProfile struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ToolAdaptationObservation records the latest cache evidence for a pinned
// visible tool surface and tool schema fingerprint.
type ToolAdaptationObservation struct {
	Profile            ToolAdaptationProfile `json:"profile"`
	VisibleToolSurface string                `json:"visible_tool_surface"`
	ToolSchemaHash     string                `json:"tool_schema_hash"`
	PromptTokens       int                   `json:"prompt_tokens"`
	CachedTokens       int                   `json:"cached_tokens"`
	CacheHitRatio      float64               `json:"cache_hit_ratio"`
	CacheSensitive     bool                  `json:"cache_sensitive"`
	Sniffed            bool                  `json:"sniffed"`
	ObservedAt         time.Time             `json:"observed_at"`
}

type ToolAdaptationToolOutcome struct {
	Profile            ToolAdaptationProfile `json:"profile"`
	VisibleToolSurface string                `json:"visible_tool_surface"`
	ToolName           string                `json:"tool_name"`
	Successes          int                   `json:"successes"`
	Failures           int                   `json:"failures"`
	LastError          string                `json:"last_error,omitempty"`
	LastDurationMS     int64                 `json:"last_duration_ms"`
	UpdatedAt          time.Time             `json:"updated_at"`
}

type toolAdaptationStateFile struct {
	Version      int                        `json:"version"`
	Observations map[string]json.RawMessage `json:"observations"`
	Outcomes     map[string]json.RawMessage `json:"outcomes"`
}

// The maps and loaded flag are retained as a deprecated in-package facade for
// one compatibility cycle. Runtime reads and writes always use SQLite.
type toolAdaptationStateStore struct {
	mu           sync.Mutex
	observations map[string]ToolAdaptationObservation
	outcomes     map[string]ToolAdaptationToolOutcome
	loaded       bool
	pathOverride string
}

var globalToolAdaptationState = &toolAdaptationStateStore{}

var (
	adaptationOpenSQLite   = sqlitestore.Open
	adaptationImmediate    = sqlitestore.Immediate
	adaptationAbsPath      = filepath.Abs
	adaptationRowsAffected = func(result sql.Result) (int64, error) {
		return result.RowsAffected()
	}
	adaptationLoadObservations      = loadAdaptationObservations
	adaptationLoadOutcomes          = loadAdaptationOutcomes
	adaptationValidateSchemaObject  = sqlitestore.ValidateSchemaObject
	adaptationValidateUniqueIndexes = sqlitestore.ValidateUniqueIndexSet
	adaptationExecContext           = func(
		ctx context.Context,
		conn *sql.Conn,
		query string,
		args ...any,
	) (sql.Result, error) {
		return conn.ExecContext(ctx, query, args...)
	}
)

func ObserveToolAdaptationCache(
	profile ToolAdaptationProfile,
	visibleToolSurface string,
	toolDefs []providers.ToolDefinition,
	usage *providers.UsageInfo,
) (ToolAdaptationObservation, bool) {
	return globalToolAdaptationState.observe(profile, visibleToolSurface, toolDefs, usage)
}

func LatestToolAdaptationObservation(profile ToolAdaptationProfile) (ToolAdaptationObservation, bool) {
	return globalToolAdaptationState.latest(profile)
}

func ObserveToolAdaptationToolOutcome(
	profile ToolAdaptationProfile,
	visibleToolSurface string,
	toolName string,
	success bool,
	errorSummary string,
	duration time.Duration,
) (ToolAdaptationToolOutcome, bool) {
	return globalToolAdaptationState.observeToolOutcome(
		profile,
		visibleToolSurface,
		toolName,
		success,
		errorSummary,
		duration,
	)
}

func LatestToolAdaptationToolOutcomes(profile ToolAdaptationProfile) []ToolAdaptationToolOutcome {
	return globalToolAdaptationState.latestToolOutcomes(profile)
}

func ToolAdaptationStatePath() string {
	return filepath.Join(config.GetHome(), toolAdaptationDatabaseFilename)
}

func ToolSchemaHash(toolDefs []providers.ToolDefinition) string {
	if len(toolDefs) == 0 {
		return ""
	}

	items := make([]map[string]any, 0, len(toolDefs))
	for _, def := range toolDefs {
		items = append(items, map[string]any{
			"name":       def.Function.Name,
			"parameters": def.Function.Parameters,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		left, _ := items[i]["name"].(string)
		right, _ := items[j]["name"].(string)
		return left < right
	})

	payload, err := json.Marshal(items)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (s *toolAdaptationStateStore) observe(
	profile ToolAdaptationProfile,
	visibleToolSurface string,
	toolDefs []providers.ToolDefinition,
	usage *providers.UsageInfo,
) (ToolAdaptationObservation, bool) {
	profile = normalizeToolAdaptationProfile(profile)
	if profile.Provider == "" && profile.Model == "" {
		return ToolAdaptationObservation{}, false
	}
	if usage == nil || usage.PromptTokens < minCacheSniffPromptTokens {
		return ToolAdaptationObservation{}, false
	}

	ratio := 0.0
	if usage.PromptTokens > 0 {
		ratio = float64(usage.CachedTokens) / float64(usage.PromptTokens)
	}
	observation := ToolAdaptationObservation{
		Profile:            profile,
		VisibleToolSurface: strings.TrimSpace(visibleToolSurface),
		ToolSchemaHash:     ToolSchemaHash(toolDefs),
		PromptTokens:       usage.PromptTokens,
		CachedTokens:       usage.CachedTokens,
		CacheHitRatio:      ratio,
		CacheSensitive:     usage.CachedTokens > 0,
		Sniffed:            true,
		ObservedAt:         time.Now().UTC(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeObservationLocked(context.Background(), observation); err != nil {
		s.warnLocked("Failed to persist tool adaptation state", err)
	}
	return observation, true
}

func (s *toolAdaptationStateStore) latest(profile ToolAdaptationProfile) (ToolAdaptationObservation, bool) {
	profile = normalizeToolAdaptationProfile(profile)
	if profile.Provider == "" && profile.Model == "" {
		return ToolAdaptationObservation{}, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.openLocked(context.Background())
	if err != nil {
		s.warnLocked("Failed to read tool adaptation state", err)
		return ToolAdaptationObservation{}, false
	}
	defer db.Close()
	observation, err := queryObservation(context.Background(), db, profile)
	if errors.Is(err, sql.ErrNoRows) {
		return ToolAdaptationObservation{}, false
	}
	if err != nil {
		s.warnLocked("Failed to read tool adaptation state", err)
		return ToolAdaptationObservation{}, false
	}
	return observation, true
}

func (s *toolAdaptationStateStore) observeToolOutcome(
	profile ToolAdaptationProfile,
	visibleToolSurface string,
	toolName string,
	success bool,
	errorSummary string,
	duration time.Duration,
) (ToolAdaptationToolOutcome, bool) {
	profile = normalizeToolAdaptationProfile(profile)
	toolName = strings.TrimSpace(toolName)
	if (profile.Provider == "" && profile.Model == "") || toolName == "" {
		return ToolAdaptationToolOutcome{}, false
	}

	outcome := ToolAdaptationToolOutcome{
		Profile:            profile,
		VisibleToolSurface: strings.TrimSpace(visibleToolSurface),
		ToolName:           toolName,
		LastDurationMS:     duration.Milliseconds(),
		UpdatedAt:          time.Now().UTC(),
	}
	if success {
		outcome.Successes = 1
	} else {
		outcome.Failures = 1
		outcome.LastError = strings.TrimSpace(errorSummary)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	persisted, err := s.incrementOutcomeLocked(context.Background(), outcome)
	if err != nil {
		s.warnLocked("Failed to persist tool adaptation state", err)
		return outcome, true
	}
	return persisted, true
}

func (s *toolAdaptationStateStore) latestToolOutcomes(profile ToolAdaptationProfile) []ToolAdaptationToolOutcome {
	profile = normalizeToolAdaptationProfile(profile)
	if profile.Provider == "" && profile.Model == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.openLocked(context.Background())
	if err != nil {
		s.warnLocked("Failed to read tool adaptation state", err)
		return nil
	}
	defer db.Close()
	outcomes, err := queryOutcomes(context.Background(), db, profile)
	if err != nil {
		s.warnLocked("Failed to read tool adaptation state", err)
		return nil
	}
	return outcomes
}

func (s *toolAdaptationStateStore) writeObservationLocked(
	ctx context.Context,
	observation ToolAdaptationObservation,
) error {
	return s.writeObservationLockedWithLimit(
		ctx,
		observation,
		maximumAdaptationObservations,
	)
}

func (s *toolAdaptationStateStore) writeObservationLockedWithLimit(
	ctx context.Context,
	observation ToolAdaptationObservation,
	maximum int,
) error {
	normalized, timestamp, err := normalizeStoredObservation(observation)
	if err != nil {
		return err
	}
	db, err := s.openLocked(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	return adaptationImmediate(ctx, db, func(conn *sql.Conn) error {
		var exists, count int
		if err := conn.QueryRowContext(ctx, `SELECT
            EXISTS(SELECT 1 FROM tool_adaptation_observations WHERE provider = ? AND model = ?),
            (SELECT COUNT(*) FROM tool_adaptation_observations)`,
			normalized.Profile.Provider,
			normalized.Profile.Model,
		).Scan(&exists, &count); err != nil {
			return err
		}
		if maximum < 1 || (exists == 0 && count >= maximum) {
			return errors.New("tool adaptation observation limit reached")
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO tool_adaptation_observations (
            provider, model, visible_tool_surface, tool_schema_hash,
            prompt_tokens, cached_tokens, cache_hit_ratio, cache_sensitive,
            sniffed, observed_at_unix_nano, version
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
        ON CONFLICT(provider, model) DO UPDATE SET
            visible_tool_surface = excluded.visible_tool_surface,
            tool_schema_hash = excluded.tool_schema_hash,
            prompt_tokens = excluded.prompt_tokens,
            cached_tokens = excluded.cached_tokens,
            cache_hit_ratio = excluded.cache_hit_ratio,
            cache_sensitive = excluded.cache_sensitive,
            sniffed = excluded.sniffed,
            observed_at_unix_nano = excluded.observed_at_unix_nano,
            version = tool_adaptation_observations.version + 1
        WHERE excluded.observed_at_unix_nano >= tool_adaptation_observations.observed_at_unix_nano`,
			normalized.Profile.Provider,
			normalized.Profile.Model,
			normalized.VisibleToolSurface,
			normalized.ToolSchemaHash,
			normalized.PromptTokens,
			normalized.CachedTokens,
			normalized.CacheHitRatio,
			boolInt(normalized.CacheSensitive),
			boolInt(normalized.Sniffed),
			timestamp,
		)
		return err
	})
}

func (s *toolAdaptationStateStore) incrementOutcomeLocked(
	ctx context.Context,
	outcome ToolAdaptationToolOutcome,
) (ToolAdaptationToolOutcome, error) {
	return s.incrementOutcomeLockedWithLimit(
		ctx,
		outcome,
		maximumAdaptationOutcomes,
	)
}

func (s *toolAdaptationStateStore) incrementOutcomeLockedWithLimit(
	ctx context.Context,
	outcome ToolAdaptationToolOutcome,
	maximum int,
) (ToolAdaptationToolOutcome, error) {
	normalized, timestamp, normalizeErr := normalizeStoredOutcome(outcome)
	if normalizeErr != nil {
		return ToolAdaptationToolOutcome{}, normalizeErr
	}
	db, openErr := s.openLocked(ctx)
	if openErr != nil {
		return ToolAdaptationToolOutcome{}, openErr
	}
	defer db.Close()
	var result ToolAdaptationToolOutcome
	transactionErr := adaptationImmediate(ctx, db, func(conn *sql.Conn) error {
		var exists, count int
		if scanErr := conn.QueryRowContext(ctx, `SELECT
            EXISTS(SELECT 1 FROM tool_adaptation_outcomes
                WHERE provider = ? AND model = ? AND visible_tool_surface = ? AND tool_name = ?),
            (SELECT COUNT(*) FROM tool_adaptation_outcomes)`,
			normalized.Profile.Provider,
			normalized.Profile.Model,
			normalized.VisibleToolSurface,
			normalized.ToolName,
		).Scan(&exists, &count); scanErr != nil {
			return scanErr
		}
		if maximum < 1 || (exists == 0 && count >= maximum) {
			return errors.New("tool adaptation outcome limit reached")
		}
		if _, execErr := adaptationExecContext(ctx, conn, `INSERT INTO tool_adaptation_outcomes (
            provider, model, visible_tool_surface, tool_name, successes, failures,
            last_error, last_duration_ms, updated_at_unix_nano, version
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
        ON CONFLICT(provider, model, visible_tool_surface, tool_name) DO UPDATE SET
            successes = tool_adaptation_outcomes.successes + excluded.successes,
            failures = tool_adaptation_outcomes.failures + excluded.failures,
            last_error = CASE
                WHEN excluded.updated_at_unix_nano >= tool_adaptation_outcomes.updated_at_unix_nano
                THEN excluded.last_error ELSE tool_adaptation_outcomes.last_error END,
            last_duration_ms = CASE
                WHEN excluded.updated_at_unix_nano >= tool_adaptation_outcomes.updated_at_unix_nano
                THEN excluded.last_duration_ms ELSE tool_adaptation_outcomes.last_duration_ms END,
            updated_at_unix_nano = max(
                tool_adaptation_outcomes.updated_at_unix_nano,
                excluded.updated_at_unix_nano
            ),
            version = tool_adaptation_outcomes.version + 1`,
			normalized.Profile.Provider,
			normalized.Profile.Model,
			normalized.VisibleToolSurface,
			normalized.ToolName,
			normalized.Successes,
			normalized.Failures,
			normalized.LastError,
			normalized.LastDurationMS,
			timestamp,
		); execErr != nil {
			return execErr
		}
		var queryErr error
		result, queryErr = queryOutcome(
			ctx,
			conn,
			normalized.Profile,
			normalized.VisibleToolSurface,
			normalized.ToolName,
		)
		return queryErr
	})
	return result, transactionErr
}

type adaptationQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func queryObservation(
	ctx context.Context,
	queryer adaptationQueryer,
	profile ToolAdaptationProfile,
) (ToolAdaptationObservation, error) {
	var observation ToolAdaptationObservation
	var cacheSensitive, sniffed int
	var observedAt int64
	err := queryer.QueryRowContext(ctx, `SELECT
        visible_tool_surface, tool_schema_hash, prompt_tokens, cached_tokens,
        cache_hit_ratio, cache_sensitive, sniffed, observed_at_unix_nano
        FROM tool_adaptation_observations WHERE provider = ? AND model = ?`,
		profile.Provider,
		profile.Model,
	).Scan(
		&observation.VisibleToolSurface,
		&observation.ToolSchemaHash,
		&observation.PromptTokens,
		&observation.CachedTokens,
		&observation.CacheHitRatio,
		&cacheSensitive,
		&sniffed,
		&observedAt,
	)
	if err != nil {
		return ToolAdaptationObservation{}, err
	}
	observation.Profile = profile
	observation.CacheSensitive = cacheSensitive == 1
	observation.Sniffed = sniffed == 1
	observation.ObservedAt = time.Unix(0, observedAt).UTC()
	return observation, nil
}

func queryOutcome(
	ctx context.Context,
	queryer adaptationQueryer,
	profile ToolAdaptationProfile,
	visibleToolSurface,
	toolName string,
) (ToolAdaptationToolOutcome, error) {
	var outcome ToolAdaptationToolOutcome
	var updatedAt int64
	err := queryer.QueryRowContext(ctx, `SELECT
        successes, failures, last_error, last_duration_ms, updated_at_unix_nano
        FROM tool_adaptation_outcomes
        WHERE provider = ? AND model = ? AND visible_tool_surface = ? AND tool_name = ?`,
		profile.Provider,
		profile.Model,
		visibleToolSurface,
		toolName,
	).Scan(
		&outcome.Successes,
		&outcome.Failures,
		&outcome.LastError,
		&outcome.LastDurationMS,
		&updatedAt,
	)
	if err != nil {
		return ToolAdaptationToolOutcome{}, err
	}
	outcome.Profile = profile
	outcome.VisibleToolSurface = visibleToolSurface
	outcome.ToolName = toolName
	outcome.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return outcome, nil
}

func queryOutcomes(
	ctx context.Context,
	queryer adaptationQueryer,
	profile ToolAdaptationProfile,
) ([]ToolAdaptationToolOutcome, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT
        visible_tool_surface, tool_name, successes, failures, last_error,
        last_duration_ms, updated_at_unix_nano
        FROM tool_adaptation_outcomes
        WHERE provider = ? AND model = ?
        ORDER BY visible_tool_surface, tool_name`, profile.Provider, profile.Model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	outcomes := make([]ToolAdaptationToolOutcome, 0)
	for rows.Next() {
		var outcome ToolAdaptationToolOutcome
		var updatedAt int64
		if err := rows.Scan(
			&outcome.VisibleToolSurface,
			&outcome.ToolName,
			&outcome.Successes,
			&outcome.Failures,
			&outcome.LastError,
			&outcome.LastDurationMS,
			&updatedAt,
		); err != nil {
			return nil, err
		}
		outcome.Profile = profile
		outcome.UpdatedAt = time.Unix(0, updatedAt).UTC()
		outcomes = append(outcomes, outcome)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return outcomes, nil
}

func (s *toolAdaptationStateStore) openLocked(ctx context.Context) (*sql.DB, error) {
	databasePath, sourceRoot, legacyRelative, err := s.storagePathsLocked()
	if err != nil {
		return nil, err
	}
	return adaptationOpenSQLite(ctx, databasePath, toolAdaptationStoreOptions(sourceRoot, legacyRelative))
}

func (s *toolAdaptationStateStore) storagePathsLocked() (string, string, string, error) {
	if override := strings.TrimSpace(s.pathOverride); override != "" {
		absolute, err := adaptationAbsPath(override)
		if err != nil {
			return "", "", "", err
		}
		root := filepath.Dir(absolute)
		if strings.EqualFold(filepath.Ext(absolute), ".json") {
			return strings.TrimSuffix(absolute, filepath.Ext(absolute)) + ".db",
				root,
				filepath.Base(absolute),
				nil
		}
		return absolute, root, legacyToolAdaptationFilename, nil
	}
	databasePath, err := adaptationAbsPath(ToolAdaptationStatePath())
	if err != nil {
		return "", "", "", err
	}
	return databasePath, filepath.Dir(databasePath), legacyToolAdaptationFilename, nil
}

func toolAdaptationStoreOptions(root, legacyRelative string) sqlitestore.Options {
	return sqlitestore.Options{
		Component: toolAdaptationDatabaseComponent,
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{
				toolAdaptationObservationsSchema,
				toolAdaptationOutcomesSchema,
				toolAdaptationObservationsTimeIndexSchema,
				toolAdaptationOutcomesTimeIndexSchema,
			},
		}},
		Validate: validateToolAdaptationSchema,
		Legacy: &sqlitestore.LegacyOptions{
			SourceRoot:  root,
			ArchiveRoot: filepath.Join(root, "legacy-json", legacyToolAdaptationArchiveLabel),
			Sources: func() ([]sqlitestore.LegacySource, error) {
				return []sqlitestore.LegacySource{{
					ID:       legacyToolAdaptationSourceID,
					Relative: legacyRelative,
					MaxBytes: legacyToolAdaptationMaxBytes,
				}}, nil
			},
			Import:        importLegacyToolAdaptationState,
			MaxBytes:      legacyToolAdaptationMaxBytes,
			MaxSources:    1,
			MaxTotalBytes: legacyToolAdaptationMaxBytes,
		},
	}
}

func validateToolAdaptationSchema(ctx context.Context, conn *sql.Conn) error {
	return validateToolAdaptationSchemaWithLimits(
		ctx,
		conn,
		maximumAdaptationObservations,
		maximumAdaptationOutcomes,
	)
}

func validateToolAdaptationSchemaWithLimits(
	ctx context.Context,
	conn *sql.Conn,
	maximumObservations,
	maximumOutcomes int,
) error {
	for _, object := range []struct {
		objectType string
		name       string
		schema     string
	}{
		{objectType: "table", name: "tool_adaptation_observations", schema: toolAdaptationObservationsSchema},
		{objectType: "table", name: "tool_adaptation_outcomes", schema: toolAdaptationOutcomesSchema},
		{objectType: "index", name: "tool_adaptation_observations_time_idx", schema: toolAdaptationObservationsTimeIndexSchema},
		{objectType: "index", name: "tool_adaptation_outcomes_time_idx", schema: toolAdaptationOutcomesTimeIndexSchema},
	} {
		if err := adaptationValidateSchemaObject(
			ctx,
			conn,
			object.objectType,
			object.name,
			object.schema,
		); err != nil {
			return err
		}
	}
	for _, table := range []string{"tool_adaptation_observations", "tool_adaptation_outcomes"} {
		if err := adaptationValidateUniqueIndexes(ctx, conn, table); err != nil {
			return err
		}
	}
	var unexpected int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
	        WHERE name NOT LIKE 'sqlite_%'
	          AND name NOT IN (
	              'tool_adaptation_observations',
	              'tool_adaptation_outcomes',
	              'tool_adaptation_observations_time_idx',
	              'tool_adaptation_outcomes_time_idx',
	              'storage_imports',
	              'storage_import_issues',
	              'storage_import_horizons',
	              'storage_imports_archive_status_idx'
	          )`).Scan(&unexpected); err != nil {
		return err
	}
	if unexpected != 0 {
		return errors.New("tool adaptation schema has unexpected objects")
	}
	var observations, outcomes int
	if err := conn.QueryRowContext(ctx, `SELECT
        (SELECT COUNT(*) FROM tool_adaptation_observations),
        (SELECT COUNT(*) FROM tool_adaptation_outcomes)`).Scan(&observations, &outcomes); err != nil {
		return err
	}
	if maximumObservations < 0 || maximumOutcomes < 0 ||
		observations > maximumObservations || outcomes > maximumOutcomes {
		return errors.New("tool adaptation database exceeds its record limit")
	}
	return nil
}

func normalizeStoredObservation(
	observation ToolAdaptationObservation,
) (ToolAdaptationObservation, int64, error) {
	observation.Profile = normalizeToolAdaptationProfile(observation.Profile)
	if !validAdaptationProfile(observation.Profile) ||
		!validAdaptationText(observation.VisibleToolSurface, maximumAdaptationSurface, false) ||
		!validAdaptationText(observation.ToolSchemaHash, maximumAdaptationSchemaHash, false) ||
		observation.PromptTokens < 0 || observation.CachedTokens < 0 ||
		math.IsNaN(observation.CacheHitRatio) || math.IsInf(observation.CacheHitRatio, 0) {
		return ToolAdaptationObservation{}, 0, errors.New("tool adaptation observation is invalid")
	}
	timestamp, err := adaptationTimestamp(observation.ObservedAt)
	if err != nil {
		return ToolAdaptationObservation{}, 0, err
	}
	return observation, timestamp, nil
}

func normalizeStoredOutcome(
	outcome ToolAdaptationToolOutcome,
) (ToolAdaptationToolOutcome, int64, error) {
	outcome.Profile = normalizeToolAdaptationProfile(outcome.Profile)
	outcome.VisibleToolSurface = strings.TrimSpace(outcome.VisibleToolSurface)
	outcome.ToolName = strings.TrimSpace(outcome.ToolName)
	if !validAdaptationProfile(outcome.Profile) ||
		!validAdaptationText(outcome.VisibleToolSurface, maximumAdaptationSurface, false) ||
		!validAdaptationText(outcome.ToolName, maximumAdaptationToolName, true) ||
		!validAdaptationText(outcome.LastError, maximumAdaptationError, false) ||
		outcome.Successes < 0 || outcome.Failures < 0 {
		return ToolAdaptationToolOutcome{}, 0, errors.New("tool adaptation outcome is invalid")
	}
	timestamp, err := adaptationTimestamp(outcome.UpdatedAt)
	if err != nil {
		return ToolAdaptationToolOutcome{}, 0, err
	}
	return outcome, timestamp, nil
}

func validAdaptationProfile(profile ToolAdaptationProfile) bool {
	return (profile.Provider != "" || profile.Model != "") &&
		validAdaptationText(profile.Provider, maximumAdaptationProvider, false) &&
		validAdaptationText(profile.Model, maximumAdaptationModel, false)
}

func validAdaptationText(value string, maximum int, requireNonempty bool) bool {
	if !utf8.ValidString(value) || len(value) > maximum || strings.ContainsRune(value, '\x00') ||
		(requireNonempty && value == "") {
		return false
	}
	for _, character := range value {
		if character < ' ' || character == '\u007f' {
			return false
		}
	}
	return true
}

func adaptationTimestamp(value time.Time) (int64, error) {
	if value.IsZero() {
		return 0, errors.New("tool adaptation timestamp is invalid")
	}
	nanoseconds := value.UnixNano()
	if nanoseconds < 0 || !time.Unix(0, nanoseconds).Equal(value) {
		return 0, errors.New("tool adaptation timestamp is invalid")
	}
	return nanoseconds, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

type legacyObservationCandidate struct {
	observation ToolAdaptationObservation
	digest      [sha256.Size]byte
	sourceKey   string
}

type legacyOutcomeCandidate struct {
	outcome   ToolAdaptationToolOutcome
	digest    [sha256.Size]byte
	sourceKey string
}

func importLegacyToolAdaptationState(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	var state toolAdaptationStateFile
	decoder := json.NewDecoder(bytes.NewReader(input.Data))
	if err := decoder.Decode(&state); err != nil || requireAdaptationJSONEOF(decoder) != nil {
		// A malformed aggregate is one safely skipped legacy record. The
		// sqlitestore import record retains only its digest and this fixed code.
		//nolint:nilerr
		return malformedAdaptationImport(input.Digest, "malformed-json"), nil
	}
	if state.Version != 0 && state.Version != 1 {
		return malformedAdaptationImport(input.Digest, "unsupported-version"), nil
	}
	if len(state.Observations) > maximumAdaptationObservations ||
		len(state.Outcomes) > maximumAdaptationOutcomes {
		return sqlitestore.ImportResult{}, errors.New("legacy tool adaptation record count exceeds its limit")
	}

	result := sqlitestore.ImportResult{}
	addIssue := func(code string, digest [sha256.Size]byte) {
		result.Skipped++
		if len(result.Issues) < maximumAdaptationAuditIssues {
			result.Issues = append(result.Issues, sqlitestore.ImportIssue{
				Code:         code,
				RecordDigest: digest,
			})
		}
	}

	observationWinners := make(map[string]legacyObservationCandidate, len(state.Observations))
	for _, sourceKey := range sortedRawMessageKeys(state.Observations) {
		raw := state.Observations[sourceKey]
		digest := sha256.Sum256(raw)
		var observation ToolAdaptationObservation
		if err := json.Unmarshal(raw, &observation); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			addIssue("invalid-observation", digest)
			continue
		}
		normalized, _, err := normalizeStoredObservation(observation)
		if err != nil {
			addIssue("invalid-observation", digest)
			continue
		}
		key := normalized.Profile.key()
		current, exists := observationWinners[key]
		if !exists || normalized.ObservedAt.After(current.observation.ObservedAt) {
			observationWinners[key] = legacyObservationCandidate{
				observation: normalized,
				digest:      digest,
				sourceKey:   sourceKey,
			}
		}
	}

	outcomeWinners := make(map[string]legacyOutcomeCandidate, len(state.Outcomes))
	for _, sourceKey := range sortedRawMessageKeys(state.Outcomes) {
		raw := state.Outcomes[sourceKey]
		digest := sha256.Sum256(raw)
		var outcome ToolAdaptationToolOutcome
		if err := json.Unmarshal(raw, &outcome); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			addIssue("invalid-outcome", digest)
			continue
		}
		normalized, _, err := normalizeStoredOutcome(outcome)
		if err != nil {
			addIssue("invalid-outcome", digest)
			continue
		}
		key := toolOutcomeKey(normalized.Profile, normalized.VisibleToolSurface, normalized.ToolName)
		current, exists := outcomeWinners[key]
		if !exists {
			outcomeWinners[key] = legacyOutcomeCandidate{
				outcome:   normalized,
				digest:    digest,
				sourceKey: sourceKey,
			}
			continue
		}
		successes := current.outcome.Successes + normalized.Successes
		failures := current.outcome.Failures + normalized.Failures
		if normalized.UpdatedAt.After(current.outcome.UpdatedAt) {
			current = legacyOutcomeCandidate{
				outcome:   normalized,
				digest:    digest,
				sourceKey: sourceKey,
			}
		}
		current.outcome.Successes = successes
		current.outcome.Failures = failures
		outcomeWinners[key] = current
	}

	for _, key := range sortedObservationCandidateKeys(observationWinners) {
		candidate := observationWinners[key]
		timestamp, _ := adaptationTimestamp(candidate.observation.ObservedAt)
		execution, err := adaptationExecContext(ctx, conn, `INSERT INTO tool_adaptation_observations (
            provider, model, visible_tool_surface, tool_schema_hash,
            prompt_tokens, cached_tokens, cache_hit_ratio, cache_sensitive,
            sniffed, observed_at_unix_nano, version
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
        ON CONFLICT(provider, model) DO NOTHING`,
			candidate.observation.Profile.Provider,
			candidate.observation.Profile.Model,
			candidate.observation.VisibleToolSurface,
			candidate.observation.ToolSchemaHash,
			candidate.observation.PromptTokens,
			candidate.observation.CachedTokens,
			candidate.observation.CacheHitRatio,
			boolInt(candidate.observation.CacheSensitive),
			boolInt(candidate.observation.Sniffed),
			timestamp,
		)
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		inserted, err := adaptationRowsAffected(execution)
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		if inserted == 1 {
			result.Imported++
		} else {
			addIssue("sqlite-authoritative", candidate.digest)
		}
	}
	for _, key := range sortedOutcomeCandidateKeys(outcomeWinners) {
		candidate := outcomeWinners[key]
		timestamp, _ := adaptationTimestamp(candidate.outcome.UpdatedAt)
		execution, err := adaptationExecContext(ctx, conn, `INSERT INTO tool_adaptation_outcomes (
            provider, model, visible_tool_surface, tool_name, successes, failures,
            last_error, last_duration_ms, updated_at_unix_nano, version
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
        ON CONFLICT(provider, model, visible_tool_surface, tool_name) DO NOTHING`,
			candidate.outcome.Profile.Provider,
			candidate.outcome.Profile.Model,
			candidate.outcome.VisibleToolSurface,
			candidate.outcome.ToolName,
			candidate.outcome.Successes,
			candidate.outcome.Failures,
			candidate.outcome.LastError,
			candidate.outcome.LastDurationMS,
			timestamp,
		)
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		inserted, err := adaptationRowsAffected(execution)
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		if inserted == 1 {
			result.Imported++
		} else {
			addIssue("sqlite-authoritative", candidate.digest)
		}
	}
	return result, nil
}

func malformedAdaptationImport(digest [sha256.Size]byte, code string) sqlitestore.ImportResult {
	return sqlitestore.ImportResult{
		Skipped: 1,
		Issues: []sqlitestore.ImportIssue{{
			Code:         code,
			RecordDigest: digest,
		}},
	}
}

func requireAdaptationJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if err == nil {
		return errors.New("unexpected trailing JSON")
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func sortedRawMessageKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedObservationCandidateKeys(values map[string]legacyObservationCandidate) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedOutcomeCandidateKeys(values map[string]legacyOutcomeCandidate) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// loadLocked refreshes the deprecated compatibility maps from SQLite.
func (s *toolAdaptationStateStore) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.observations = make(map[string]ToolAdaptationObservation)
	s.outcomes = make(map[string]ToolAdaptationToolOutcome)
	db, err := s.openLocked(context.Background())
	if err != nil {
		s.warnLocked("Failed to read tool adaptation state", err)
		return
	}
	defer db.Close()
	loadObservationsErr := adaptationLoadObservations(db, s.observations)
	if loadObservationsErr != nil {
		s.warnLocked("Failed to read tool adaptation state", loadObservationsErr)
		return
	}
	loadOutcomesErr := adaptationLoadOutcomes(db, s.outcomes)
	if loadOutcomesErr != nil {
		s.warnLocked("Failed to read tool adaptation state", loadOutcomesErr)
		return
	}
}

func loadAdaptationObservations(
	db *sql.DB,
	destination map[string]ToolAdaptationObservation,
) error {
	observationRows, queryErr := db.Query(`SELECT provider, model, visible_tool_surface,
        tool_schema_hash, prompt_tokens, cached_tokens, cache_hit_ratio,
        cache_sensitive, sniffed, observed_at_unix_nano
        FROM tool_adaptation_observations ORDER BY provider, model`)
	if queryErr != nil {
		return queryErr
	}
	defer observationRows.Close()
	for observationRows.Next() {
		var observation ToolAdaptationObservation
		var cacheSensitive, sniffed int
		var observedAt int64
		if scanErr := observationRows.Scan(
			&observation.Profile.Provider,
			&observation.Profile.Model,
			&observation.VisibleToolSurface,
			&observation.ToolSchemaHash,
			&observation.PromptTokens,
			&observation.CachedTokens,
			&observation.CacheHitRatio,
			&cacheSensitive,
			&sniffed,
			&observedAt,
		); scanErr != nil {
			return scanErr
		}
		observation.CacheSensitive = cacheSensitive == 1
		observation.Sniffed = sniffed == 1
		observation.ObservedAt = time.Unix(0, observedAt).UTC()
		destination[observation.Profile.key()] = observation
	}
	return observationRows.Err()
}

func loadAdaptationOutcomes(
	db *sql.DB,
	destination map[string]ToolAdaptationToolOutcome,
) error {
	outcomeRows, queryErr := db.Query(`SELECT provider, model, visible_tool_surface,
        tool_name, successes, failures, last_error, last_duration_ms,
        updated_at_unix_nano FROM tool_adaptation_outcomes
        ORDER BY provider, model, visible_tool_surface, tool_name`)
	if queryErr != nil {
		return queryErr
	}
	defer outcomeRows.Close()
	for outcomeRows.Next() {
		var outcome ToolAdaptationToolOutcome
		var updatedAt int64
		if scanErr := outcomeRows.Scan(
			&outcome.Profile.Provider,
			&outcome.Profile.Model,
			&outcome.VisibleToolSurface,
			&outcome.ToolName,
			&outcome.Successes,
			&outcome.Failures,
			&outcome.LastError,
			&outcome.LastDurationMS,
			&updatedAt,
		); scanErr != nil {
			return scanErr
		}
		outcome.UpdatedAt = time.Unix(0, updatedAt).UTC()
		destination[toolOutcomeKey(
			outcome.Profile,
			outcome.VisibleToolSurface,
			outcome.ToolName,
		)] = outcome
	}
	return outcomeRows.Err()
}

// saveLocked replaces SQLite state from the deprecated compatibility maps in
// one transaction. Runtime mutation paths never use this facade.
func (s *toolAdaptationStateStore) saveLocked() error {
	return s.saveLockedWithLimits(
		maximumAdaptationObservations,
		maximumAdaptationOutcomes,
	)
}

func (s *toolAdaptationStateStore) saveLockedWithLimits(
	maximumObservations,
	maximumOutcomes int,
) error {
	db, openErr := s.openLocked(context.Background())
	if openErr != nil {
		return openErr
	}
	defer db.Close()
	observations := canonicalObservationValues(s.observations)
	outcomes := canonicalOutcomeValues(s.outcomes)
	if maximumObservations < 0 || maximumOutcomes < 0 ||
		len(observations) > maximumObservations || len(outcomes) > maximumOutcomes {
		return errors.New("tool adaptation compatibility state exceeds its record limit")
	}
	transactionErr := adaptationImmediate(context.Background(), db, func(conn *sql.Conn) error {
		if _, execErr := adaptationExecContext(
			context.Background(),
			conn,
			`DELETE FROM tool_adaptation_observations`,
		); execErr != nil {
			return execErr
		}
		if _, execErr := adaptationExecContext(
			context.Background(),
			conn,
			`DELETE FROM tool_adaptation_outcomes`,
		); execErr != nil {
			return execErr
		}
		for _, observation := range observations {
			timestamp, _ := adaptationTimestamp(observation.ObservedAt)
			if _, execErr := adaptationExecContext(
				context.Background(),
				conn,
				`INSERT INTO tool_adaptation_observations (
                provider, model, visible_tool_surface, tool_schema_hash,
                prompt_tokens, cached_tokens, cache_hit_ratio, cache_sensitive,
                sniffed, observed_at_unix_nano, version
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
				observation.Profile.Provider,
				observation.Profile.Model,
				observation.VisibleToolSurface,
				observation.ToolSchemaHash,
				observation.PromptTokens,
				observation.CachedTokens,
				observation.CacheHitRatio,
				boolInt(observation.CacheSensitive),
				boolInt(observation.Sniffed),
				timestamp,
			); execErr != nil {
				return execErr
			}
		}
		for _, outcome := range outcomes {
			timestamp, _ := adaptationTimestamp(outcome.UpdatedAt)
			if _, execErr := adaptationExecContext(context.Background(), conn, `INSERT INTO tool_adaptation_outcomes (
                provider, model, visible_tool_surface, tool_name, successes,
                failures, last_error, last_duration_ms, updated_at_unix_nano, version
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
				outcome.Profile.Provider,
				outcome.Profile.Model,
				outcome.VisibleToolSurface,
				outcome.ToolName,
				outcome.Successes,
				outcome.Failures,
				outcome.LastError,
				outcome.LastDurationMS,
				timestamp,
			); execErr != nil {
				return execErr
			}
		}
		return nil
	})
	if transactionErr == nil {
		s.loaded = false
	}
	return transactionErr
}

func canonicalObservationValues(
	values map[string]ToolAdaptationObservation,
) []ToolAdaptationObservation {
	winners := make(map[string]ToolAdaptationObservation)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		normalized, _, err := normalizeStoredObservation(values[key])
		if err != nil {
			continue
		}
		profileKey := normalized.Profile.key()
		current, exists := winners[profileKey]
		if !exists || normalized.ObservedAt.After(current.ObservedAt) {
			winners[profileKey] = normalized
		}
	}
	keys = keys[:0]
	for key := range winners {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]ToolAdaptationObservation, 0, len(keys))
	for _, key := range keys {
		result = append(result, winners[key])
	}
	return result
}

func canonicalOutcomeValues(values map[string]ToolAdaptationToolOutcome) []ToolAdaptationToolOutcome {
	winners := make(map[string]ToolAdaptationToolOutcome)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		normalized, _, err := normalizeStoredOutcome(values[key])
		if err != nil {
			continue
		}
		outcomeKey := toolOutcomeKey(normalized.Profile, normalized.VisibleToolSurface, normalized.ToolName)
		current, exists := winners[outcomeKey]
		if !exists {
			winners[outcomeKey] = normalized
			continue
		}
		successes := current.Successes + normalized.Successes
		failures := current.Failures + normalized.Failures
		if normalized.UpdatedAt.After(current.UpdatedAt) {
			current = normalized
		}
		current.Successes = successes
		current.Failures = failures
		winners[outcomeKey] = current
	}
	keys = keys[:0]
	for key := range winners {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]ToolAdaptationToolOutcome, 0, len(keys))
	for _, key := range keys {
		result = append(result, winners[key])
	}
	return result
}

func (s *toolAdaptationStateStore) statePathLocked() string {
	databasePath, _, _, err := s.storagePathsLocked()
	if err != nil {
		return ToolAdaptationStatePath()
	}
	return databasePath
}

func (s *toolAdaptationStateStore) warnLocked(message string, err error) {
	logger.WarnCF("tools", message, map[string]any{
		"path":  s.statePathLocked(),
		"error": err.Error(),
	})
}

func normalizeToolAdaptationProfile(profile ToolAdaptationProfile) ToolAdaptationProfile {
	return ToolAdaptationProfile{
		Provider: providers.NormalizeProvider(profile.Provider),
		Model:    strings.ToLower(strings.TrimSpace(profile.Model)),
	}
}

func (p ToolAdaptationProfile) key() string {
	p = normalizeToolAdaptationProfile(p)
	return fmt.Sprintf("%s/%s", p.Provider, p.Model)
}

func toolOutcomeKey(profile ToolAdaptationProfile, visibleToolSurface string, toolName string) string {
	return fmt.Sprintf(
		"%s/%s/%s",
		profile.key(),
		strings.TrimSpace(visibleToolSurface),
		strings.TrimSpace(toolName),
	)
}
