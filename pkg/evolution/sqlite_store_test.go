package evolution_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/evolution"
	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

func TestEvolutionSQLiteFreshSchemaConfigurationAndReopen(t *testing.T) {
	paths := evolution.NewPaths(t.TempDir(), "")
	store := evolution.NewSQLiteStore(paths)
	if records, err := store.LoadTaskRecords(); err != nil || len(records) != 0 {
		t.Fatalf("fresh LoadTaskRecords = %#v, %v", records, err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(paths.Database); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("database mode = %v, %v", info, err)
		}
		if info, err := os.Stat(paths.RootDir); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("database directory mode = %v, %v", info, err)
		}
	}
	db := openEvolutionTestDatabase(t, paths.Database)
	defer db.Close()
	var version, foreignKeys, busyTimeout, synchronous int
	var journal string
	if err := db.QueryRow(`SELECT
        (SELECT user_version FROM pragma_user_version),
        (SELECT foreign_keys FROM pragma_foreign_keys),
        (SELECT timeout FROM pragma_busy_timeout),
        (SELECT synchronous FROM pragma_synchronous),
        (SELECT journal_mode FROM pragma_journal_mode)`).
		Scan(&version, &foreignKeys, &busyTimeout, &synchronous, &journal); err != nil {
		t.Fatal(err)
	}
	if version != 1 || foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 ||
		!strings.EqualFold(journal, "wal") {
		t.Fatalf("SQLite config = version:%d fk:%d busy:%d sync:%d journal:%q",
			version, foreignKeys, busyTimeout, synchronous, journal)
	}
	var tables, indexes int
	if err := db.QueryRow(`SELECT
        (SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name LIKE 'evolution_%'),
        (SELECT COUNT(*) FROM sqlite_schema WHERE type = 'index' AND name LIKE 'evolution_%')`).
		Scan(&tables, &indexes); err != nil {
		t.Fatal(err)
	}
	if tables != 13 || indexes < 4 {
		t.Fatalf("schema objects = tables:%d indexes:%d", tables, indexes)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := evolution.NewSQLiteStore(paths).LoadProfiles(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
}

func TestEvolutionSQLiteRejectsTooNewAndChangedSchema(t *testing.T) {
	for _, mutate := range []func(*testing.T, *sql.DB){
		func(t *testing.T, db *sql.DB) {
			t.Helper()
			if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
				t.Fatal(err)
			}
		},
		func(t *testing.T, db *sql.DB) {
			t.Helper()
			if _, err := db.Exec(`CREATE INDEX evolution_rogue_idx ON evolution_records(summary)`); err != nil {
				t.Fatal(err)
			}
		},
	} {
		paths := evolution.NewPaths(t.TempDir(), "")
		if _, err := evolution.NewSQLiteStore(paths).LoadTaskRecords(); err != nil {
			t.Fatal(err)
		}
		db := openEvolutionTestDatabase(t, paths.Database)
		mutate(t, db)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		_, err := evolution.NewSQLiteStore(paths).LoadTaskRecords()
		if err == nil {
			t.Fatal("mutated evolution database reopened")
		}
	}
}

func TestEvolutionSQLiteRejectsBrokenRelationshipOrdering(t *testing.T) {
	paths := evolution.NewPaths(t.TempDir(), "")
	store := evolution.NewSQLiteStore(paths)
	record := evolution.LearningRecord{
		ID: "ordered", Kind: evolution.RecordKindTask, WorkspaceID: paths.Workspace,
		CreatedAt: time.Unix(1700000000, 0).UTC(), Status: "new",
		ToolKinds: []string{"first", "second"},
	}
	if err := store.AppendTaskRecord(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	db := openEvolutionTestDatabase(t, paths.Database)
	if _, err := db.Exec(`UPDATE evolution_record_strings SET position = 3
        WHERE record_class = 'task' AND workspace_id = ? AND record_id = ?
          AND field_name = 'tool_kinds' AND position = 1`, paths.Workspace, record.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadTaskRecords(); err == nil || !errors.Is(err, sqlitestore.ErrInvalidSchema) {
		t.Fatalf("broken ordering reopen error = %v", err)
	}
}

func TestEvolutionSQLiteStructuredRoundTrip(t *testing.T) {
	paths := evolution.NewPaths(t.TempDir(), "")
	store := evolution.NewSQLiteStore(paths)
	updated := time.Unix(1700000001, 2).UTC()
	success := true
	record := evolution.LearningRecord{
		ID: "task-structured", Kind: evolution.RecordKindTask, WorkspaceID: paths.Workspace,
		CreatedAt: time.Unix(1700000000, 1).UTC(), UpdatedAt: &updated,
		SessionKey: "session", TaskHash: "hash", Summary: "summary", UserGoal: "goal",
		FinalOutput: "output", Source: map[string]any{"nested": map[string]any{"number": float64(7)}},
		Status: "new", Success: &success, ToolKinds: []string{"read", "exec"},
		ToolExecutions: []evolution.ToolExecutionRecord{{
			Name: "read_file", Success: true, SkillNames: []string{"one", "two"},
		}},
		InitialSkillNames: []string{"one"}, AddedSkillNames: []string{"two"},
		UsedSkillNames: []string{"one", "two"}, AllLoadedSkillNames: []string{"zero", "one", "two"},
		ActiveSkillNames: []string{"two"}, AttemptTrail: &evolution.AttemptTrail{
			AttemptedSkills: []string{"zero", "one"}, FinalSuccessfulPath: []string{"one", "two"},
			SkillContextSnapshots: []evolution.SkillContextSnapshot{{
				Sequence: 3, Trigger: "retry", SkillNames: []string{"one", "two"},
			}},
		},
		Signals: []string{"signal"}, SourceRecordIDs: []string{"source"},
		TaskRecordIDs: []string{"task"}, Label: "label", ClusterReason: "reason",
		EventCount: 4, SuccessRate: 0.75, MaturityScore: 0.9,
		WinningPath: []string{"one", "two"}, LateAddedSkills: []string{"two"},
		FinalSnapshotTrigger: "final", MatchedSkillNames: []string{"one"},
	}
	if err := store.AppendTaskRecord(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadTaskRecords()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("LoadTaskRecords = %#v, %v", loaded, err)
	}
	if !reflect.DeepEqual(loaded[0], record) {
		t.Fatalf("round trip = %#v, want %#v", loaded[0], record)
	}
	if _, err := os.Stat(paths.TaskRecords); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mutable JSONL was created: %v", err)
	}
}

func TestEvolutionSQLiteMigratesArchivesAndAuditsLegacySources(t *testing.T) {
	workspace := t.TempDir()
	paths := evolution.NewPaths(workspace, "")
	if err := os.MkdirAll(paths.ProfilesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	baseTask := evolution.LearningRecord{
		ID: "task-1", Kind: evolution.RecordKindTask, WorkspaceID: workspace,
		CreatedAt: time.Unix(1700000000, 0).UTC(), Summary: "legacy summary", Status: "new",
	}
	pattern := evolution.LearningRecord{
		ID: "pattern-1", Kind: evolution.RecordKindPattern, WorkspaceID: workspace,
		CreatedAt: time.Unix(1700000001, 0).UTC(), Summary: "pattern", Status: "ready",
	}
	writeEvolutionJSONL(t, paths.LearningRecords, baseTask, pattern)
	overrideTask := baseTask
	overrideTask.Summary = "split summary wins"
	writeEvolutionJSONLWithMalformed(t, paths.TaskRecords, overrideTask, overrideTask)
	draft := evolution.SkillDraft{
		ID: "draft-1", WorkspaceID: workspace, CreatedAt: time.Unix(1700000002, 0).UTC(),
		SourceRecordID: "pattern-1", TargetSkillName: "weather", DraftType: evolution.DraftTypeShortcut,
		ChangeKind: evolution.ChangeKindAppend, HumanSummary: "draft", BodyOrPatch: "patch",
		Status: evolution.DraftStatusCandidate,
	}
	draftData, err := json.Marshal([]evolution.SkillDraft{draft, {ID: "", WorkspaceID: workspace}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.SkillDrafts, draftData, 0o600); err != nil {
		t.Fatal(err)
	}
	profile := evolution.SkillProfile{
		SkillName: "weather", WorkspaceID: workspace, Status: evolution.SkillStatusActive,
		Origin: "evolved", HumanSummary: "profile", LastUsedAt: time.Unix(1700000003, 0).UTC(),
	}
	profileData, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(paths.ProfilesDir, "weather.json")
	if err := os.WriteFile(profilePath, profileData, 0o600); err != nil {
		t.Fatal(err)
	}

	store := evolution.NewSQLiteStore(paths)
	tasks, err := store.LoadTaskRecords()
	if err != nil {
		t.Fatal(err)
	}
	patterns, err := store.LoadPatternRecords()
	if err != nil {
		t.Fatal(err)
	}
	drafts, err := store.LoadDrafts()
	if err != nil {
		t.Fatal(err)
	}
	loadedProfile, err := store.LoadProfile("weather")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Summary != "split summary wins" ||
		len(patterns) != 1 || len(drafts) != 1 || loadedProfile.HumanSummary != "profile" {
		t.Fatalf("migration result tasks=%#v patterns=%#v drafts=%#v profile=%#v",
			tasks, patterns, drafts, loadedProfile)
	}
	for _, relative := range []string{
		"learning-records.jsonl", "task-records.jsonl", "skill-drafts.json", "profiles/weather.json",
	} {
		if _, err := os.Stat(filepath.Join(paths.LegacyArchive, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("archive %s: %v", relative, err)
		}
		if _, err := os.Stat(filepath.Join(paths.RootDir, filepath.FromSlash(relative))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy source %s remains: %v", relative, err)
		}
	}
	db := openEvolutionTestDatabase(t, paths.Database)
	defer db.Close()
	var imported, skipped, issues int
	if err := db.QueryRow(`SELECT
        COALESCE(SUM(imported_count), 0), COALESCE(SUM(skipped_count), 0),
        (SELECT COUNT(*) FROM storage_import_issues WHERE component = 'evolution')
      FROM storage_imports WHERE component = 'evolution'`).Scan(&imported, &skipped, &issues); err != nil {
		t.Fatal(err)
	}
	if imported != 5 || skipped < 3 || issues < 3 {
		t.Fatalf("migration accounting imported=%d skipped=%d issues=%d", imported, skipped, issues)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := store.LoadLearningRecords()
	if err != nil || len(second) != 2 {
		t.Fatalf("idempotent reopen = %#v, %v", second, err)
	}
}

func TestEvolutionSQLiteUnsafeLegacySourceAbortsWithoutImport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions differ on Windows")
	}
	paths := evolution.NewPaths(t.TempDir(), "")
	if err := os.MkdirAll(paths.RootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, paths.TaskRecords); err != nil {
		t.Fatal(err)
	}
	if _, err := evolution.NewSQLiteStore(paths).LoadTaskRecords(); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "unsafe") {
		t.Fatalf("unsafe migration error = %v", err)
	}
	raw, err := sql.Open("sqlite", paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var userVersion int
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if userVersion != 0 {
		t.Fatalf("failed migration committed schema version %d", userVersion)
	}
}

func openEvolutionTestDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+
		"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func writeEvolutionJSONL(t *testing.T, path string, records ...evolution.LearningRecord) {
	t.Helper()
	var content strings.Builder
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		content.Write(encoded)
		content.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeEvolutionJSONLWithMalformed(
	t *testing.T,
	path string,
	records ...evolution.LearningRecord,
) {
	t.Helper()
	writeEvolutionJSONL(t, path, records...)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{malformed\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
