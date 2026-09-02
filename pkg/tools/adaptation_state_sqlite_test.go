package tools

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestToolAdaptationSQLiteSchemaPragmasPermissionsAndReopen(t *testing.T) {
	home, store := useToolAdaptationSQLiteHome(t)
	profile := ToolAdaptationProfile{Provider: " CoPiLoT ", Model: " GPT-5.4 "}
	observation, ok := store.observe(
		profile,
		" codex ",
		[]providers.ToolDefinition{{
			Function: providers.ToolFunctionDefinition{
				Name:       "exec_command",
				Parameters: map[string]any{"type": "object"},
			},
		}},
		&providers.UsageInfo{PromptTokens: 1_000, CachedTokens: 250},
	)
	if !ok {
		t.Fatal("observe() ok = false")
	}
	outcome, ok := store.observeToolOutcome(
		profile,
		" codex ",
		" exec_command ",
		false,
		" bad args ",
		1250*time.Millisecond,
	)
	if !ok {
		t.Fatal("observeToolOutcome() ok = false")
	}
	if observation.Profile.Provider != "github-copilot" || observation.Profile.Model != "gpt-5.4" ||
		observation.VisibleToolSurface != "codex" || observation.CacheHitRatio != 0.25 ||
		!observation.CacheSensitive || !observation.Sniffed {
		t.Fatalf("observation = %#v", observation)
	}
	if outcome.Profile.Provider != "github-copilot" || outcome.ToolName != "exec_command" ||
		outcome.Failures != 1 || outcome.Successes != 0 || outcome.LastError != "bad args" ||
		outcome.LastDurationMS != 1250 {
		t.Fatalf("outcome = %#v", outcome)
	}
	if _, err := os.Stat(filepath.Join(home, legacyToolAdaptationFilename)); !os.IsNotExist(err) {
		t.Fatalf("legacy JSON unexpectedly exists: %v", err)
	}

	store.mu.Lock()
	db, err := store.openLocked(t.Context())
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("openLocked() error = %v", err)
	}
	defer db.Close()
	var version, foreignKeys, busyTimeout, synchronous int
	var journal string
	for query, destination := range map[string]any{
		"PRAGMA user_version": &version,
		"PRAGMA foreign_keys": &foreignKeys,
		"PRAGMA busy_timeout": &busyTimeout,
		"PRAGMA synchronous":  &synchronous,
		"PRAGMA journal_mode": &journal,
	} {
		if scanErr := db.QueryRowContext(t.Context(), query).Scan(destination); scanErr != nil {
			t.Fatalf("%s error = %v", query, scanErr)
		}
	}
	if version != 1 || foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 ||
		!strings.EqualFold(journal, "wal") {
		t.Fatalf("PRAGMAs = version:%d fk:%d busy:%d sync:%d journal:%q",
			version, foreignKeys, busyTimeout, synchronous, journal)
	}
	for name, expected := range map[string]string{
		"tool_adaptation_observations":          toolAdaptationObservationsSchema,
		"tool_adaptation_outcomes":              toolAdaptationOutcomesSchema,
		"tool_adaptation_observations_time_idx": toolAdaptationObservationsTimeIndexSchema,
		"tool_adaptation_outcomes_time_idx":     toolAdaptationOutcomesTimeIndexSchema,
	} {
		var actual string
		if scanErr := db.QueryRowContext(
			t.Context(),
			`SELECT sql FROM sqlite_schema WHERE name = ?`,
			name,
		).Scan(&actual); scanErr != nil {
			t.Fatalf("read schema %s: %v", name, scanErr)
		}
		if compactAdaptationSQL(actual) != compactAdaptationSQL(expected) {
			t.Fatalf("schema %s = %q, want %q", name, actual, expected)
		}
	}
	var provider, model, surface, schemaHash string
	var promptTokens, cachedTokens, cacheSensitive, sniffed, storedVersion int
	var ratio float64
	var observedAt int64
	if scanErr := db.QueryRowContext(t.Context(), `SELECT
        provider, model, visible_tool_surface, tool_schema_hash, prompt_tokens,
        cached_tokens, cache_hit_ratio, cache_sensitive, sniffed,
        observed_at_unix_nano, version FROM tool_adaptation_observations`).Scan(
		&provider,
		&model,
		&surface,
		&schemaHash,
		&promptTokens,
		&cachedTokens,
		&ratio,
		&cacheSensitive,
		&sniffed,
		&observedAt,
		&storedVersion,
	); scanErr != nil {
		t.Fatalf("read typed observation: %v", scanErr)
	}
	if provider != "github-copilot" || model != "gpt-5.4" || surface != "codex" ||
		len(schemaHash) != sha256.Size*2 || promptTokens != 1000 || cachedTokens != 250 ||
		ratio != 0.25 || cacheSensitive != 1 || sniffed != 1 || observedAt <= 0 || storedVersion != 1 {
		t.Fatalf("typed observation row is invalid")
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE tool_adaptation_observations
        SET version = version`); err != nil {
		t.Fatalf("create WAL evidence: %v", err)
	}
	for _, path := range []string{
		toolAdaptationStatePath(),
		toolAdaptationStatePath() + "-wal",
		toolAdaptationStatePath() + "-shm",
	} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", filepath.Base(path), statErr)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode %s = %o, want 600", filepath.Base(path), info.Mode().Perm())
		}
	}
	if info, statErr := os.Stat(home); statErr != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("home mode = %v, %v; want 700", info, statErr)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}

	reopened := &toolAdaptationStateStore{pathOverride: toolAdaptationStatePath()}
	latest, found := reopened.latest(profile)
	if !found || latest.PromptTokens != 1000 || latest.CachedTokens != 250 ||
		!latest.ObservedAt.Equal(observation.ObservedAt) {
		t.Fatalf("reopened observation = %#v, %v", latest, found)
	}
	outcomes := reopened.latestToolOutcomes(profile)
	if len(outcomes) != 1 || outcomes[0].Failures != 1 || outcomes[0].LastError != "bad args" {
		t.Fatalf("reopened outcomes = %#v", outcomes)
	}
}

func TestToolAdaptationSQLiteObservationUpsertLatestAndVersion(t *testing.T) {
	_, store := useToolAdaptationSQLiteHome(t)
	profile := ToolAdaptationProfile{Provider: "openai", Model: "gpt-5"}
	earlier := time.Date(2026, time.January, 2, 3, 4, 5, 6, time.UTC)
	later := earlier.Add(time.Minute)
	old := ToolAdaptationObservation{
		Profile:            profile,
		VisibleToolSurface: "simple",
		ToolSchemaHash:     "old",
		PromptTokens:       500,
		CachedTokens:       10,
		CacheHitRatio:      0.02,
		ObservedAt:         earlier,
	}
	newer := old
	newer.VisibleToolSurface = "codex"
	newer.ToolSchemaHash = "new"
	newer.PromptTokens = 1000
	newer.CachedTokens = 500
	newer.CacheHitRatio = 0.5
	newer.CacheSensitive = true
	newer.Sniffed = true
	newer.ObservedAt = later
	store.mu.Lock()
	if err := store.writeObservationLocked(t.Context(), newer); err != nil {
		store.mu.Unlock()
		t.Fatalf("write newer observation: %v", err)
	}
	if err := store.writeObservationLocked(t.Context(), old); err != nil {
		store.mu.Unlock()
		t.Fatalf("write stale observation: %v", err)
	}
	store.mu.Unlock()
	latest, found := store.latest(profile)
	if !found || latest.ToolSchemaHash != "new" || latest.VisibleToolSurface != "codex" ||
		latest.PromptTokens != 1000 || !latest.ObservedAt.Equal(later) {
		t.Fatalf("latest observation = %#v, %v", latest, found)
	}
	store.mu.Lock()
	db, err := store.openLocked(t.Context())
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("openLocked() error = %v", err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRowContext(t.Context(), `SELECT version
        FROM tool_adaptation_observations WHERE provider = 'openai' AND model = 'gpt-5'`).Scan(&version); err != nil {
		t.Fatalf("read observation version: %v", err)
	}
	if version != 1 {
		t.Fatalf("version after stale write = %d, want 1", version)
	}
	newest := newer
	newest.ToolSchemaHash = "newest"
	newest.ObservedAt = later.Add(time.Minute)
	store.mu.Lock()
	writeErr := store.writeObservationLocked(t.Context(), newest)
	store.mu.Unlock()
	if writeErr != nil {
		t.Fatalf("write newest observation: %v", writeErr)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT version
        FROM tool_adaptation_observations WHERE provider = 'openai' AND model = 'gpt-5'`).Scan(&version); err != nil {
		t.Fatalf("read updated observation version: %v", err)
	}
	if version != 2 {
		t.Fatalf("version after newer write = %d, want 2", version)
	}
}

func TestToolAdaptationSQLiteOutcomeCountersConcurrencyAndOrder(t *testing.T) {
	_, seedStore := useToolAdaptationSQLiteHome(t)
	databasePath := seedStore.pathOverride
	profile := ToolAdaptationProfile{Provider: "openai", Model: "gpt-5"}
	const writers = 32
	start := make(chan struct{})
	var wait sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			store := &toolAdaptationStateStore{pathOverride: databasePath}
			success := writer%2 == 0
			if _, ok := store.observeToolOutcome(
				profile,
				"codex",
				"exec_command",
				success,
				fmt.Sprintf("error-%02d", writer),
				time.Duration(writer)*time.Millisecond,
			); !ok {
				t.Errorf("observeToolOutcome(%d) ok = false", writer)
			}
		}()
	}
	close(start)
	wait.Wait()
	outcomes := seedStore.latestToolOutcomes(profile)
	if len(outcomes) != 1 || outcomes[0].Successes != writers/2 || outcomes[0].Failures != writers/2 {
		t.Fatalf("concurrent counter = %#v", outcomes)
	}

	for _, item := range []struct {
		surface string
		tool    string
	}{
		{surface: "simple", tool: "write_file"},
		{surface: "codex", tool: "apply_patch"},
	} {
		if _, ok := seedStore.observeToolOutcome(profile, item.surface, item.tool, true, "", 0); !ok {
			t.Fatalf("observeToolOutcome(%s/%s) ok = false", item.surface, item.tool)
		}
	}
	outcomes = seedStore.latestToolOutcomes(profile)
	want := []string{"codex/apply_patch", "codex/exec_command", "simple/write_file"}
	if len(outcomes) != len(want) {
		t.Fatalf("outcomes = %#v, want %d", outcomes, len(want))
	}
	for index := range outcomes {
		got := outcomes[index].VisibleToolSurface + "/" + outcomes[index].ToolName
		if got != want[index] {
			t.Fatalf("outcome order = %#v, want %#v", outcomes, want)
		}
	}
	seedStore.mu.Lock()
	db, err := seedStore.openLocked(t.Context())
	seedStore.mu.Unlock()
	if err != nil {
		t.Fatalf("openLocked() error = %v", err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRowContext(t.Context(), `SELECT version FROM tool_adaptation_outcomes
        WHERE provider = 'openai' AND model = 'gpt-5'
          AND visible_tool_surface = 'codex' AND tool_name = 'exec_command'`).Scan(&version); err != nil {
		t.Fatalf("read outcome version: %v", err)
	}
	if version != writers {
		t.Fatalf("outcome version = %d, want %d", version, writers)
	}
}

func TestToolAdaptationSQLiteLegacyImportAuditArchiveAndIdempotence(t *testing.T) {
	home, store := useToolAdaptationSQLiteHome(t)
	earlier := "2026-01-02T03:04:05.000000006Z"
	later := "2026-01-02T04:04:05.000000006Z"
	legacy := `{
  "version": 1,
  "observations": {
    "a": {"profile":{"provider":"copilot","model":" GPT-5.4 "},"visible_tool_surface":"simple","tool_schema_hash":"old","prompt_tokens":500,"cached_tokens":10,"cache_hit_ratio":0.02,"observed_at":"` + earlier + `"},
    "b": {"profile":{"provider":"github-copilot","model":"gpt-5.4"},"visible_tool_surface":"codex","tool_schema_hash":"new","prompt_tokens":1000,"cached_tokens":500,"cache_hit_ratio":0.5,"cache_sensitive":true,"sniffed":true,"observed_at":"` + later + `"},
    "invalid": {"profile":{},"observed_at":"` + later + `"}
  },
  "outcomes": {
    "a": {"profile":{"provider":"copilot","model":" GPT-5.4 "},"visible_tool_surface":" codex ","tool_name":" exec_command ","successes":2,"failures":1,"last_error":"old error","last_duration_ms":10,"updated_at":"` + earlier + `"},
    "b": {"profile":{"provider":"github-copilot","model":"gpt-5.4"},"visible_tool_surface":"codex","tool_name":"exec_command","successes":3,"failures":4,"last_error":"new error","last_duration_ms":20,"updated_at":"` + later + `"},
    "invalid": {"profile":{"provider":"openai","model":"gpt"},"tool_name":"","updated_at":"` + later + `"}
  }
}`
	legacyPath := filepath.Join(home, legacyToolAdaptationFilename)
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o640); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}
	if err := os.Chmod(legacyPath, 0o640); err != nil {
		t.Fatalf("chmod legacy state: %v", err)
	}
	profile := ToolAdaptationProfile{Provider: "copilot", Model: "gpt-5.4"}
	observation, found := store.latest(profile)
	if !found || observation.Profile.Provider != "github-copilot" ||
		observation.ToolSchemaHash != "new" || observation.PromptTokens != 1000 ||
		observation.CachedTokens != 500 || observation.ObservedAt.Format(time.RFC3339Nano) != later {
		t.Fatalf("imported observation = %#v, %v", observation, found)
	}
	outcomes := store.latestToolOutcomes(profile)
	if len(outcomes) != 1 || outcomes[0].Successes != 5 || outcomes[0].Failures != 5 ||
		outcomes[0].LastError != "new error" || outcomes[0].LastDurationMS != 20 ||
		outcomes[0].UpdatedAt.Format(time.RFC3339Nano) != later {
		t.Fatalf("imported outcomes = %#v", outcomes)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy source still exists: %v", err)
	}
	archivePath := filepath.Join(
		home,
		"legacy-json",
		legacyToolAdaptationArchiveLabel,
		legacyToolAdaptationFilename,
	)
	archived, archiveErr := os.ReadFile(archivePath)
	if archiveErr != nil || string(archived) != legacy {
		t.Fatalf("archive = %q, %v", archived, archiveErr)
	}
	if info, statErr := os.Stat(archivePath); statErr != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("archive mode = %v, %v; want 640", info, statErr)
	}
	store.mu.Lock()
	db, err := store.openLocked(t.Context())
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("openLocked() error = %v", err)
	}
	defer db.Close()
	var imported, skipped int
	var status string
	if scanErr := db.QueryRowContext(t.Context(), `SELECT imported_count, skipped_count, archive_status
        FROM storage_imports WHERE component = ?`, toolAdaptationDatabaseComponent).Scan(
		&imported,
		&skipped,
		&status,
	); scanErr != nil {
		t.Fatalf("read import record: %v", scanErr)
	}
	if imported != 2 || skipped != 2 || status != "complete" {
		t.Fatalf("import audit = imported:%d skipped:%d status:%q", imported, skipped, status)
	}
	var issueCount int
	if scanErr := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM storage_import_issues
        WHERE component = ? AND issue_code IN ('invalid-observation', 'invalid-outcome')`,
		toolAdaptationDatabaseComponent,
	).Scan(&issueCount); scanErr != nil {
		t.Fatalf("read issues: %v", scanErr)
	}
	if issueCount != 2 {
		t.Fatalf("safe issue count = %d, want 2", issueCount)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}
	reopened := &toolAdaptationStateStore{pathOverride: store.pathOverride}
	if observation, found := reopened.latest(profile); !found || observation.ToolSchemaHash != "new" {
		t.Fatalf("idempotent reopen observation = %#v, %v", observation, found)
	}
	reopened.mu.Lock()
	db, err = reopened.openLocked(t.Context())
	reopened.mu.Unlock()
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer db.Close()
	var importCount, observationCount, outcomeCount int
	for query, destination := range map[string]*int{
		`SELECT COUNT(*) FROM storage_imports WHERE component = 'tool-adaptation'`: &importCount,
		`SELECT COUNT(*) FROM tool_adaptation_observations`:                        &observationCount,
		`SELECT COUNT(*) FROM tool_adaptation_outcomes`:                            &outcomeCount,
	} {
		if err := db.QueryRowContext(t.Context(), query).Scan(destination); err != nil {
			t.Fatalf("%s error = %v", query, err)
		}
	}
	if importCount != 1 || observationCount != 1 || outcomeCount != 1 {
		t.Fatalf("idempotent counts = imports:%d observations:%d outcomes:%d",
			importCount, observationCount, outcomeCount)
	}
}

func TestToolAdaptationSQLiteMalformedLegacyIsAuditedAndArchived(t *testing.T) {
	home, store := useToolAdaptationSQLiteHome(t)
	legacy := []byte(`{"version":1,"observations":{"secret-token":`)
	legacyPath := filepath.Join(home, legacyToolAdaptationFilename)
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatalf("write malformed state: %v", err)
	}
	if err := os.Chmod(legacyPath, 0o600); err != nil {
		t.Fatalf("chmod malformed state: %v", err)
	}
	if _, found := store.latest(ToolAdaptationProfile{Provider: "openai", Model: "gpt"}); found {
		t.Fatal("malformed legacy state produced an observation")
	}
	archivePath := filepath.Join(
		home,
		"legacy-json",
		legacyToolAdaptationArchiveLabel,
		legacyToolAdaptationFilename,
	)
	archived, err := os.ReadFile(archivePath)
	if err != nil || string(archived) != string(legacy) {
		t.Fatalf("malformed archive = %q, %v", archived, err)
	}
	store.mu.Lock()
	db, err := store.openLocked(t.Context())
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("openLocked() error = %v", err)
	}
	defer db.Close()
	var imported, skipped int
	var issueCode string
	if err := db.QueryRowContext(t.Context(), `SELECT imported_count, skipped_count
        FROM storage_imports WHERE component = ?`, toolAdaptationDatabaseComponent).Scan(
		&imported,
		&skipped,
	); err != nil {
		t.Fatalf("read malformed import: %v", err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT issue_code FROM storage_import_issues
        WHERE component = ?`, toolAdaptationDatabaseComponent).Scan(&issueCode); err != nil {
		t.Fatalf("read malformed issue: %v", err)
	}
	if imported != 0 || skipped != 1 || issueCode != "malformed-json" ||
		strings.Contains(issueCode, "secret") {
		t.Fatalf("malformed audit = imported:%d skipped:%d code:%q", imported, skipped, issueCode)
	}
}

func TestToolAdaptationSQLiteRejectsTooNewCorruptAndUnsafeState(t *testing.T) {
	t.Run("too new", func(t *testing.T) {
		_, store := useToolAdaptationSQLiteHome(t)
		store.mu.Lock()
		db, openErr := store.openLocked(t.Context())
		store.mu.Unlock()
		if openErr != nil {
			t.Fatalf("openLocked() error = %v", openErr)
		}
		if _, execErr := db.ExecContext(t.Context(), `PRAGMA user_version = 2`); execErr != nil {
			db.Close()
			t.Fatalf("set too-new version: %v", execErr)
		}
		if closeErr := db.Close(); closeErr != nil {
			t.Fatalf("Close() error = %v", closeErr)
		}
		store.mu.Lock()
		_, openErr = store.openLocked(t.Context())
		store.mu.Unlock()
		if !errors.Is(openErr, sqlitestore.ErrTooNew) {
			t.Fatalf("openLocked() error = %v, want ErrTooNew", openErr)
		}
		if _, found := store.latest(ToolAdaptationProfile{Provider: "openai", Model: "gpt"}); found {
			t.Fatal("latest() returned data from too-new database")
		}
	})

	t.Run("corrupt", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), ".picoclaw")
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatalf("MkdirAll(): %v", err)
		}
		databasePath := filepath.Join(home, toolAdaptationDatabaseFilename)
		if err := os.WriteFile(databasePath, []byte("not a SQLite database"), 0o600); err != nil {
			t.Fatalf("write corrupt database: %v", err)
		}
		if err := os.Chmod(databasePath, 0o600); err != nil {
			t.Fatalf("chmod corrupt database: %v", err)
		}
		store := &toolAdaptationStateStore{pathOverride: databasePath}
		store.mu.Lock()
		_, err := store.openLocked(t.Context())
		store.mu.Unlock()
		if err == nil {
			t.Fatal("openLocked() accepted corrupt database")
		}
	})

	t.Run("unsafe legacy mode", func(t *testing.T) {
		home, store := useToolAdaptationSQLiteHome(t)
		legacyPath := filepath.Join(home, legacyToolAdaptationFilename)
		if err := os.WriteFile(legacyPath, []byte(`{"version":1}`), 0o600); err != nil {
			t.Fatalf("write legacy state: %v", err)
		}
		if err := os.Chmod(legacyPath, 0o666); err != nil {
			t.Fatalf("chmod unsafe legacy state: %v", err)
		}
		store.mu.Lock()
		_, err := store.openLocked(t.Context())
		store.mu.Unlock()
		if err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("openLocked() error = %v, want unsafe-mode error", err)
		}
	})

	t.Run("legacy symlink", func(t *testing.T) {
		home, store := useToolAdaptationSQLiteHome(t)
		target := filepath.Join(t.TempDir(), "target.json")
		if err := os.WriteFile(target, []byte(`{"version":1}`), 0o600); err != nil {
			t.Fatalf("write symlink target: %v", err)
		}
		if err := os.Symlink(target, filepath.Join(home, legacyToolAdaptationFilename)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		store.mu.Lock()
		_, err := store.openLocked(t.Context())
		store.mu.Unlock()
		if err == nil || (!strings.Contains(err.Error(), "symlink") &&
			!strings.Contains(err.Error(), "unsafe type")) {
			t.Fatalf("openLocked() error = %v, want unsafe-link error", err)
		}
	})

	t.Run("database symlink", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), ".picoclaw")
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatalf("MkdirAll(): %v", err)
		}
		target := filepath.Join(t.TempDir(), "target.db")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatalf("write database target: %v", err)
		}
		databasePath := filepath.Join(home, toolAdaptationDatabaseFilename)
		if err := os.Symlink(target, databasePath); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		store := &toolAdaptationStateStore{pathOverride: databasePath}
		store.mu.Lock()
		_, err := store.openLocked(t.Context())
		store.mu.Unlock()
		if err == nil ||
			(!strings.Contains(err.Error(), "regular file") && !strings.Contains(err.Error(), "unsafe member")) {
			t.Fatalf("openLocked() error = %v, want regular-file error", err)
		}
	})
}

func TestToolAdaptationSQLiteRejectsUnrelatedSchemaObjects(t *testing.T) {
	for name, statement := range map[string]string{
		"table": `CREATE TABLE rogue_adaptation_state(id INTEGER PRIMARY KEY) STRICT`,
		"view": `CREATE VIEW rogue_adaptation_view AS
			SELECT provider, model FROM tool_adaptation_observations`,
		"index": `CREATE INDEX rogue_adaptation_import_idx ON storage_imports(source_id)`,
	} {
		t.Run(name, func(t *testing.T) {
			_, store := useToolAdaptationSQLiteHome(t)
			store.mu.Lock()
			database, err := store.openLocked(t.Context())
			store.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if _, execErr := database.ExecContext(t.Context(), statement); execErr != nil {
				_ = database.Close()
				t.Fatal(execErr)
			}
			if closeErr := database.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			store.mu.Lock()
			reopened, err := store.openLocked(t.Context())
			store.mu.Unlock()
			if reopened != nil {
				_ = reopened.Close()
			}
			if !errors.Is(err, sqlitestore.ErrInvalidSchema) {
				t.Fatalf("openLocked() error = %v, want ErrInvalidSchema", err)
			}
		})
	}
}

func TestToolAdaptationSQLiteConstraintsAndBestEffortAPIs(t *testing.T) {
	_, store := useToolAdaptationSQLiteHome(t)
	store.mu.Lock()
	db, err := store.openLocked(t.Context())
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("openLocked() error = %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), `INSERT INTO tool_adaptation_observations (
        provider, model, visible_tool_surface, tool_schema_hash, prompt_tokens,
        cached_tokens, cache_hit_ratio, cache_sensitive, sniffed,
        observed_at_unix_nano
    ) VALUES ('OpenAI', 'gpt', '', '', 1, 0, 0, 0, 0, 1)`); err == nil {
		t.Fatal("provider normalization constraint accepted mixed case")
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO tool_adaptation_observations (
        provider, model, visible_tool_surface, tool_schema_hash, prompt_tokens,
        cached_tokens, cache_hit_ratio, cache_sensitive, sniffed,
        observed_at_unix_nano
    ) VALUES ('openai', 'gpt', '', '', -1, 0, 0, 0, 0, 1)`); err == nil {
		t.Fatal("token constraint accepted a negative count")
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO tool_adaptation_outcomes (
        provider, model, visible_tool_surface, tool_name, successes, failures,
        last_error, last_duration_ms, updated_at_unix_nano
    ) VALUES ('openai', 'gpt', '', '', 0, 0, '', 0, 1)`); err == nil {
		t.Fatal("tool identity constraint accepted empty tool")
	}

	failing := &toolAdaptationStateStore{pathOverride: t.TempDir()}
	profile := ToolAdaptationProfile{Provider: "openai", Model: "gpt"}
	if _, ok := failing.observe(
		profile,
		"codex",
		nil,
		&providers.UsageInfo{PromptTokens: minCacheSniffPromptTokens, CachedTokens: 1},
	); !ok {
		t.Fatal("observe() dropped best-effort event after persistence failure")
	}
	if _, ok := failing.observeToolOutcome(profile, "codex", "tool", true, "", 0); !ok {
		t.Fatal("observeToolOutcome() dropped best-effort event after persistence failure")
	}
	if _, ok := failing.latest(profile); ok {
		t.Fatal("latest() returned data after persistence failure")
	}
	if outcomes := failing.latestToolOutcomes(profile); outcomes != nil {
		t.Fatalf("latestToolOutcomes() = %#v, want nil after failure", outcomes)
	}
}

func TestImportLegacyToolAdaptationStateRejectsUnboundedCount(t *testing.T) {
	_, store := useToolAdaptationSQLiteHome(t)
	store.mu.Lock()
	db, err := store.openLocked(t.Context())
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("openLocked() error = %v", err)
	}
	defer db.Close()
	observations := make(map[string]json.RawMessage, maximumAdaptationObservations+1)
	for index := 0; index <= maximumAdaptationObservations; index++ {
		observations[fmt.Sprintf("observation-%06d", index)] = json.RawMessage("null")
	}
	data, err := json.Marshal(map[string]any{
		"version":      1,
		"observations": observations,
	})
	if err != nil {
		t.Fatalf("marshal oversized fixture: %v", err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("db.Conn() error = %v", err)
	}
	defer conn.Close()
	_, err = importLegacyToolAdaptationState(t.Context(), conn, sqlitestore.LegacyInput{
		ID:     legacyToolAdaptationSourceID,
		Data:   data,
		Digest: sha256.Sum256(data),
	})
	if err == nil || !strings.Contains(err.Error(), "count exceeds") {
		t.Fatalf("importLegacyToolAdaptationState() error = %v, want bounded-count error", err)
	}
}

func useToolAdaptationSQLiteHome(t *testing.T) (string, *toolAdaptationStateStore) {
	t.Helper()
	home := filepath.Join(t.TempDir(), ".picoclaw")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", home, err)
	}
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatalf("chmod home: %v", err)
	}
	t.Setenv(config.EnvHome, home)
	return home, &toolAdaptationStateStore{
		pathOverride: filepath.Join(home, toolAdaptationDatabaseFilename),
	}
}

func compactAdaptationSQL(value string) string {
	return strings.Join(strings.Fields(strings.TrimSuffix(strings.TrimSpace(value), ";")), " ")
}

var _ adaptationQueryer = (*sql.DB)(nil)
