package tools

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

func TestAdaptationStateCoverageCloseoutBoundaries(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", filepath.Join(t.TempDir(), ".picoclaw"))
	store := &toolAdaptationStateStore{pathOverride: filepath.Join(t.TempDir(), "state.db")}
	profile := ToolAdaptationProfile{Provider: "openai", Model: "gpt"}

	invalidObservation := ToolAdaptationObservation{Profile: profile, ObservedAt: time.Time{}}
	if err := store.writeObservationLocked(t.Context(), invalidObservation); err == nil {
		t.Fatal("writeObservationLocked() accepted a zero timestamp")
	}
	invalidOutcome := ToolAdaptationToolOutcome{
		Profile: profile, VisibleToolSurface: "codex", ToolName: "tool", UpdatedAt: time.Time{},
	}
	if _, err := store.incrementOutcomeLocked(t.Context(), invalidOutcome); err == nil {
		t.Fatal("incrementOutcomeLocked() accepted a zero timestamp")
	}
	for _, observation := range []ToolAdaptationObservation{
		{Profile: profile, ObservedAt: time.Now(), CacheHitRatio: math.NaN()},
		{Profile: profile, ObservedAt: time.Now(), PromptTokens: -1},
	} {
		if _, _, err := normalizeStoredObservation(observation); err == nil {
			t.Fatal("normalizeStoredObservation() accepted invalid counters")
		}
	}
	for _, outcome := range []ToolAdaptationToolOutcome{
		{Profile: profile, ToolName: "tool", Successes: -1, UpdatedAt: time.Now()},
		{Profile: profile, ToolName: "tool", LastError: "bad\nerror", UpdatedAt: time.Now()},
	} {
		if _, _, err := normalizeStoredOutcome(outcome); err == nil {
			t.Fatal("normalizeStoredOutcome() accepted invalid state")
		}
	}
	if validAdaptationText("bad\ntext", 100, false) {
		t.Fatal("validAdaptationText() accepted control text")
	}
	for _, timestamp := range []time.Time{
		{},
		time.Date(1969, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		if _, err := adaptationTimestamp(timestamp); err == nil {
			t.Fatalf("adaptationTimestamp(%v) returned nil error", timestamp)
		}
	}

	now := time.Now().UTC()
	first := ToolAdaptationObservation{Profile: profile, ObservedAt: now}
	if err := store.writeObservationLockedWithLimit(t.Context(), first, 1); err != nil {
		t.Fatalf("write first bounded observation: %v", err)
	}
	second := first
	second.Profile.Model = "other"
	if err := store.writeObservationLockedWithLimit(t.Context(), second, 1); err == nil {
		t.Fatal("observation capacity limit was not enforced")
	}
	firstOutcome := ToolAdaptationToolOutcome{
		Profile: profile, VisibleToolSurface: "codex", ToolName: "first", UpdatedAt: now,
	}
	if _, err := store.incrementOutcomeLockedWithLimit(t.Context(), firstOutcome, 1); err != nil {
		t.Fatalf("write first bounded outcome: %v", err)
	}
	secondOutcome := firstOutcome
	secondOutcome.ToolName = "second"
	if _, err := store.incrementOutcomeLockedWithLimit(t.Context(), secondOutcome, 1); err == nil {
		t.Fatal("outcome capacity limit was not enforced")
	}

	invalidMaps := &toolAdaptationStateStore{
		pathOverride: filepath.Join(t.TempDir(), "compat.db"),
		observations: map[string]ToolAdaptationObservation{
			"invalid": {Profile: ToolAdaptationProfile{}, ObservedAt: now},
		},
		outcomes: map[string]ToolAdaptationToolOutcome{
			"invalid": {Profile: profile, ToolName: "", UpdatedAt: now},
		},
	}
	if got := canonicalObservationValues(invalidMaps.observations); len(got) != 0 {
		t.Fatalf("canonicalObservationValues(invalid) = %#v", got)
	}
	if got := canonicalOutcomeValues(invalidMaps.outcomes); len(got) != 0 {
		t.Fatalf("canonicalOutcomeValues(invalid) = %#v", got)
	}
	invalidMaps.observations["valid"] = first
	if err := invalidMaps.saveLockedWithLimits(0, 1); err == nil {
		t.Fatal("saveLockedWithLimits() ignored observation limit")
	}

	previousAbs := adaptationAbsPath
	adaptationAbsPath = func(string) (string, error) { return "", errors.New("injected abs failure") }
	t.Cleanup(func() { adaptationAbsPath = previousAbs })
	if _, _, _, err := store.storagePathsLocked(); err == nil {
		t.Fatal("storagePathsLocked() ignored absolute-path failure")
	}
	if got := store.statePathLocked(); got != ToolAdaptationStatePath() {
		t.Fatalf("statePathLocked() fallback = %q", got)
	}
}

func TestAdaptationStateCoverageCloseoutQueryAndLoadFailures(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", filepath.Join(t.TempDir(), ".picoclaw"))
	closed, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	profile := ToolAdaptationProfile{Provider: "openai", Model: "gpt"}
	if _, err := queryObservation(t.Context(), closed, profile); err == nil {
		t.Fatal("queryObservation(closed) returned nil error")
	}
	if _, err := queryOutcome(t.Context(), closed, profile, "codex", "tool"); err == nil {
		t.Fatal("queryOutcome(closed) returned nil error")
	}
	if _, err := queryOutcomes(t.Context(), closed, profile); err == nil {
		t.Fatal("queryOutcomes(closed) returned nil error")
	}
	if err := loadAdaptationObservations(closed, map[string]ToolAdaptationObservation{}); err == nil {
		t.Fatal("loadAdaptationObservations(closed) returned nil error")
	}
	if err := loadAdaptationOutcomes(closed, map[string]ToolAdaptationToolOutcome{}); err == nil {
		t.Fatal("loadAdaptationOutcomes(closed) returned nil error")
	}

	canary := errors.New("injected load failure")
	previousObservations := adaptationLoadObservations
	previousOutcomes := adaptationLoadOutcomes
	t.Cleanup(func() {
		adaptationLoadObservations = previousObservations
		adaptationLoadOutcomes = previousOutcomes
	})
	store := &toolAdaptationStateStore{pathOverride: filepath.Join(t.TempDir(), "state.db")}
	adaptationLoadObservations = func(*sql.DB, map[string]ToolAdaptationObservation) error { return canary }
	store.loadLocked()
	if !store.loaded {
		t.Fatal("loadLocked() did not retain failed load state")
	}
	adaptationLoadObservations = func(*sql.DB, map[string]ToolAdaptationObservation) error { return nil }
	adaptationLoadOutcomes = func(*sql.DB, map[string]ToolAdaptationToolOutcome) error { return canary }
	store.loaded = false
	store.loadLocked()

	previousOpen := adaptationOpenSQLite
	t.Cleanup(func() { adaptationOpenSQLite = previousOpen })
	adaptationOpenSQLite = func(context.Context, string, sqlitestore.Options) (*sql.DB, error) {
		db, openErr := sql.Open("sqlite", ":memory:")
		if openErr != nil {
			return nil, openErr
		}
		_ = db.Close()
		return db, nil
	}
	if _, ok := store.latest(profile); ok {
		t.Fatal("latest() accepted a closed database")
	}
	if outcomes := store.latestToolOutcomes(profile); outcomes != nil {
		t.Fatalf("latestToolOutcomes() = %#v", outcomes)
	}
}

func TestAdaptationStateCoverageCloseoutRowScanFailures(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE tool_adaptation_observations (
			provider, model, visible_tool_surface, tool_schema_hash,
			prompt_tokens, cached_tokens, cache_hit_ratio, cache_sensitive,
			sniffed, observed_at_unix_nano
		)`,
		`INSERT INTO tool_adaptation_observations VALUES (
			'openai', 'gpt', 'codex', 'hash', 'not-an-integer', 0, 0.0, 0, 0, 1
		)`,
		`CREATE TABLE tool_adaptation_outcomes (
			provider, model, visible_tool_surface, tool_name, successes,
			failures, last_error, last_duration_ms, updated_at_unix_nano
		)`,
		`INSERT INTO tool_adaptation_outcomes VALUES (
			'openai', 'gpt', 'codex', 'tool', 'not-an-integer', 0, '', 0, 1
		)`,
	} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := loadAdaptationObservations(
		db,
		map[string]ToolAdaptationObservation{},
	); err == nil {
		t.Fatal("loadAdaptationObservations accepted an invalid row type")
	}
	if err := loadAdaptationOutcomes(
		db,
		map[string]ToolAdaptationToolOutcome{},
	); err == nil {
		t.Fatal("loadAdaptationOutcomes accepted an invalid row type")
	}
	if _, err := queryOutcomes(t.Context(), db, profileForCoverage()); err == nil {
		t.Fatal("queryOutcomes accepted an invalid row type")
	}
}

//nolint:govet // Independent fault assertions intentionally reuse short declarations.
func TestAdaptationStateCoverageCloseoutSchemaAndTransactionFailures(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", filepath.Join(t.TempDir(), ".picoclaw"))
	store, db := openAdaptationCoverageStore(t)
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := validateToolAdaptationSchemaWithLimits(t.Context(), conn, 0, 0); err != nil {
		t.Fatalf("empty schema with zero limits: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	observation := ToolAdaptationObservation{
		Profile: ToolAdaptationProfile{Provider: "openai", Model: "gpt"}, ObservedAt: now,
	}
	if err := store.writeObservationLocked(t.Context(), observation); err != nil {
		t.Fatal(err)
	}
	conn, err = db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := validateToolAdaptationSchemaWithLimits(t.Context(), conn, 0, 1); err == nil {
		t.Fatal("schema count limit was not enforced")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	previousImmediate := adaptationImmediate
	previousExec := adaptationExecContext
	t.Cleanup(func() {
		adaptationImmediate = previousImmediate
		adaptationExecContext = previousExec
	})
	adaptationImmediate = func(
		ctx context.Context,
		database *sql.DB,
		fn func(*sql.Conn) error,
	) error {
		if _, err := database.ExecContext(ctx, `DROP TABLE tool_adaptation_observations`); err != nil {
			return err
		}
		return sqlitestore.Immediate(ctx, database, fn)
	}
	other := observation
	other.Profile.Model = "other"
	if err := store.writeObservationLocked(t.Context(), other); err == nil {
		t.Fatal("writeObservationLocked() ignored count-query failure")
	}

	store, db = openAdaptationCoverageStore(t)
	defer db.Close()
	adaptationImmediate = previousImmediate
	adaptationExecContext = func(
		context.Context,
		*sql.Conn,
		string,
		...any,
	) (sql.Result, error) {
		return nil, errors.New("injected exec failure")
	}
	validOutcome := ToolAdaptationToolOutcome{
		Profile: profileForCoverage(), VisibleToolSurface: "codex", ToolName: "tool", UpdatedAt: time.Now(),
	}
	if _, err := store.incrementOutcomeLocked(t.Context(), validOutcome); err == nil {
		t.Fatal("incrementOutcomeLocked() ignored exec failure")
	}
	store.observations = map[string]ToolAdaptationObservation{
		"one": {Profile: profileForCoverage(), ObservedAt: time.Now()},
	}
	store.outcomes = map[string]ToolAdaptationToolOutcome{
		"one": validOutcome,
	}
	if err := store.saveLocked(); err == nil {
		t.Fatal("saveLocked() ignored delete failure")
	}
}

//nolint:govet // Independent fault assertions intentionally reuse short declarations.
func TestAdaptationStateCoverageCloseoutSchemaInjectedFailures(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", filepath.Join(t.TempDir(), ".picoclaw"))
	_, db := openAdaptationCoverageStore(t)
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	canary := errors.New("injected schema failure")
	previousSchema := adaptationValidateSchemaObject
	previousUnique := adaptationValidateUniqueIndexes
	t.Cleanup(func() {
		adaptationValidateSchemaObject = previousSchema
		adaptationValidateUniqueIndexes = previousUnique
	})
	adaptationValidateSchemaObject = func(
		context.Context,
		*sql.Conn,
		string,
		string,
		string,
	) error {
		return canary
	}
	if err := validateToolAdaptationSchema(t.Context(), conn); !errors.Is(err, canary) {
		t.Fatalf("schema object error = %v", err)
	}
	adaptationValidateSchemaObject = previousSchema
	adaptationValidateUniqueIndexes = func(
		context.Context,
		*sql.Conn,
		string,
		...string,
	) error {
		return canary
	}
	if err := validateToolAdaptationSchema(t.Context(), conn); !errors.Is(err, canary) {
		t.Fatalf("unique index error = %v", err)
	}
	adaptationValidateSchemaObject = func(context.Context, *sql.Conn, string, string, string) error {
		return nil
	}
	adaptationValidateUniqueIndexes = func(context.Context, *sql.Conn, string, ...string) error {
		return nil
	}
	closedConn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := closedConn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateToolAdaptationSchema(t.Context(), closedConn); err == nil {
		t.Fatal("schema validation accepted a closed connection")
	}
}

//nolint:govet // Independent fault assertions intentionally reuse short declarations.
func TestAdaptationStateCoverageCloseoutRemainingFaultSeams(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", filepath.Join(t.TempDir(), ".picoclaw"))
	store, db := openAdaptationCoverageStore(t)
	defer db.Close()
	profile := profileForCoverage()
	now := time.Now().UTC()

	previousImmediate := adaptationImmediate
	previousAbs := adaptationAbsPath
	previousOpen := adaptationOpenSQLite
	previousSchema := adaptationValidateSchemaObject
	previousUnique := adaptationValidateUniqueIndexes
	previousExec := adaptationExecContext
	t.Cleanup(func() {
		adaptationImmediate = previousImmediate
		adaptationAbsPath = previousAbs
		adaptationOpenSQLite = previousOpen
		adaptationValidateSchemaObject = previousSchema
		adaptationValidateUniqueIndexes = previousUnique
		adaptationExecContext = previousExec
	})

	adaptationImmediate = func(
		ctx context.Context,
		database *sql.DB,
		fn func(*sql.Conn) error,
	) error {
		if _, err := database.ExecContext(ctx, `DROP TABLE tool_adaptation_outcomes`); err != nil {
			return err
		}
		return sqlitestore.Immediate(ctx, database, fn)
	}
	if _, err := store.incrementOutcomeLocked(t.Context(), ToolAdaptationToolOutcome{
		Profile: profile, VisibleToolSurface: "codex", ToolName: "tool", UpdatedAt: now,
	}); err == nil {
		t.Fatal("incrementOutcomeLocked() ignored count-query failure")
	}
	adaptationImmediate = previousImmediate

	adaptationAbsPath = func(string) (string, error) { return "", errors.New("abs failure") }
	if _, err := store.openLocked(t.Context()); err == nil {
		t.Fatal("openLocked() ignored storage path failure")
	}
	store.pathOverride = ""
	if _, _, _, err := store.storagePathsLocked(); err == nil {
		t.Fatal("storagePathsLocked(default) ignored abs failure")
	}
	adaptationAbsPath = previousAbs

	canary := errors.New("open failure")
	adaptationOpenSQLite = func(context.Context, string, sqlitestore.Options) (*sql.DB, error) {
		return nil, canary
	}
	store.pathOverride = filepath.Join(t.TempDir(), "state.db")
	if err := store.saveLocked(); !errors.Is(err, canary) {
		t.Fatalf("saveLocked() open error = %v", err)
	}
	adaptationOpenSQLite = previousOpen

	_, schemaDB := openAdaptationCoverageStore(t)
	defer schemaDB.Close()
	conn, err := schemaDB.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	adaptationValidateSchemaObject = func(context.Context, *sql.Conn, string, string, string) error {
		return nil
	}
	adaptationValidateUniqueIndexes = func(context.Context, *sql.Conn, string, ...string) error {
		return nil
	}
	if _, err := conn.ExecContext(t.Context(), `CREATE INDEX rogue_adaptation_idx
		ON tool_adaptation_outcomes(last_error)`); err != nil {
		t.Fatal(err)
	}
	if err := validateToolAdaptationSchema(t.Context(), conn); err == nil ||
		!strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("rogue schema error = %v", err)
	}
	if _, err := conn.ExecContext(t.Context(), `DROP TABLE tool_adaptation_observations`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `DROP INDEX rogue_adaptation_idx`); err != nil {
		t.Fatal(err)
	}
	if err := validateToolAdaptationSchema(t.Context(), conn); err == nil {
		t.Fatal("schema count query accepted a missing table")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	adaptationValidateSchemaObject = previousSchema
	adaptationValidateUniqueIndexes = previousUnique

	_, importDB := openAdaptationCoverageStore(t)
	defer importDB.Close()
	importConn, err := importDB.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer importConn.Close()
	adaptationExecContext = func(context.Context, *sql.Conn, string, ...any) (sql.Result, error) {
		return nil, errors.New("import exec failure")
	}
	when := now.Format(time.RFC3339Nano)
	for _, raw := range []string{
		`{"version":1,"observations":{"one":{"profile":{"provider":"openai","model":"one"},"observed_at":"` + when + `"}}}`,
		`{"version":1,"outcomes":{"one":{"profile":{"provider":"openai","model":"one"},"visible_tool_surface":"codex","tool_name":"tool","updated_at":"` + when + `"}}}`,
	} {
		data := []byte(raw)
		if _, err := importLegacyToolAdaptationState(t.Context(), importConn, sqlitestore.LegacyInput{
			ID: legacyToolAdaptationSourceID, Data: data, Digest: sha256.Sum256(data),
		}); err == nil {
			t.Fatal("legacy import ignored exec failure")
		}
	}
	adaptationExecContext = previousExec

	decoder := json.NewDecoder(strings.NewReader(`1 2`))
	var first int
	if err := decoder.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if err := requireAdaptationJSONEOF(decoder); err == nil {
		t.Fatal("requireAdaptationJSONEOF() accepted trailing value")
	}
}

func TestAdaptationStateCoverageCloseoutSaveWriteStages(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", filepath.Join(t.TempDir(), ".picoclaw"))
	now := time.Now().UTC()
	profile := profileForCoverage()
	store := &toolAdaptationStateStore{
		pathOverride: filepath.Join(t.TempDir(), "state.db"),
		observations: map[string]ToolAdaptationObservation{
			"one": {Profile: profile, ObservedAt: now},
		},
		outcomes: map[string]ToolAdaptationToolOutcome{
			"one": {
				Profile: profile, VisibleToolSurface: "codex", ToolName: "tool", UpdatedAt: now,
			},
		},
	}
	previousExec := adaptationExecContext
	t.Cleanup(func() { adaptationExecContext = previousExec })
	for _, failAt := range []int{2, 3, 4} {
		calls := 0
		adaptationExecContext = func(
			ctx context.Context,
			conn *sql.Conn,
			query string,
			args ...any,
		) (sql.Result, error) {
			calls++
			if calls == failAt {
				return nil, errors.New("injected staged write failure")
			}
			return conn.ExecContext(ctx, query, args...)
		}
		if err := store.saveLocked(); err == nil {
			t.Fatalf("saveLocked() stage %d returned nil error", failAt)
		}
	}
}

//nolint:govet // Independent fault assertions intentionally reuse short declarations.
func TestAdaptationStateCoverageCloseoutLegacyBranches(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", filepath.Join(t.TempDir(), ".picoclaw"))
	_, db := openAdaptationCoverageStore(t)
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for name, data := range map[string]string{
		"unsupported":     `{"version":2}`,
		"invalid records": `{"version":1,"observations":{"bad":null},"outcomes":{"bad":null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			input := []byte(data)
			result, err := importLegacyToolAdaptationState(t.Context(), conn, sqlitestore.LegacyInput{
				ID: legacyToolAdaptationSourceID, Data: input, Digest: sha256.Sum256(input),
			})
			if err != nil || result.Skipped == 0 {
				t.Fatalf("import result = %#v, %v", result, err)
			}
		})
	}
	decoder := json.NewDecoder(strings.NewReader(`1 {`))
	var first int
	if err := decoder.Decode(&first); err != nil {
		t.Fatalf("decode first JSON value: %v", err)
	}
	if err := requireAdaptationJSONEOF(decoder); err == nil {
		t.Fatal("requireAdaptationJSONEOF() accepted malformed trailing JSON")
	}

	authoritative := `{"version":1,"observations":{"one":{"profile":{"provider":"openai","model":"gpt"},"observed_at":"` + now + `"}},"outcomes":{"one":{"profile":{"provider":"openai","model":"gpt"},"visible_tool_surface":"codex","tool_name":"tool","updated_at":"` + now + `"}}}`
	input := []byte(authoritative)
	if _, err := importLegacyToolAdaptationState(t.Context(), conn, sqlitestore.LegacyInput{
		ID: legacyToolAdaptationSourceID, Data: input, Digest: sha256.Sum256(input),
	}); err != nil {
		t.Fatal(err)
	}
	result, err := importLegacyToolAdaptationState(t.Context(), conn, sqlitestore.LegacyInput{
		ID: legacyToolAdaptationSourceID, Data: input, Digest: sha256.Sum256(input),
	})
	if err != nil || result.Skipped != 2 {
		t.Fatalf("authoritative replay = %#v, %v", result, err)
	}

	previousRowsAffected := adaptationRowsAffected
	t.Cleanup(func() { adaptationRowsAffected = previousRowsAffected })
	adaptationRowsAffected = func(sql.Result) (int64, error) {
		return 0, errors.New("injected RowsAffected failure")
	}
	other := []byte(
		`{"version":1,"observations":{"two":{"profile":{"provider":"openai","model":"other"},"observed_at":"` +
			now + `"}}}`,
	)
	if _, err := importLegacyToolAdaptationState(t.Context(), conn, sqlitestore.LegacyInput{
		ID: legacyToolAdaptationSourceID, Data: other, Digest: sha256.Sum256(other),
	}); err == nil {
		t.Fatal("legacy observation ignored RowsAffected error")
	}
	outcome := []byte(
		`{"version":1,"outcomes":{"two":{"profile":{"provider":"openai","model":"other"},"visible_tool_surface":"codex","tool_name":"tool","updated_at":"` +
			now + `"}}}`,
	)
	if _, err := importLegacyToolAdaptationState(t.Context(), conn, sqlitestore.LegacyInput{
		ID: legacyToolAdaptationSourceID, Data: outcome, Digest: sha256.Sum256(outcome),
	}); err == nil {
		t.Fatal("legacy outcome ignored RowsAffected error")
	}
}

func openAdaptationCoverageStore(t *testing.T) (*toolAdaptationStateStore, *sql.DB) {
	t.Helper()
	store := &toolAdaptationStateStore{pathOverride: filepath.Join(t.TempDir(), "state.db")}
	db, err := store.openLocked(t.Context())
	if err != nil {
		t.Fatalf("openLocked() error = %v", err)
	}
	return store, db
}

func profileForCoverage() ToolAdaptationProfile {
	return ToolAdaptationProfile{Provider: "openai", Model: "gpt"}
}
