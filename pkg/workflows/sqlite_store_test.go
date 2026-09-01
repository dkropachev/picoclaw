package workflows

import (
	"context"
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
)

func privateWorkflowTestWorkspace(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	return workspace
}

//nolint:govet // Test assertions intentionally scope independent errors.
func TestSQLiteRunStoreSchemaDurabilityAndVersionFence(t *testing.T) {
	workspace := privateWorkflowTestWorkspace(t)
	store, err := NewSQLiteRunStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	path, err := workflowDatabasePath(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, checked := range []struct {
		path string
		mode os.FileMode
	}{{filepath.Dir(path), 0o700}, {path, 0o600}} {
		info, err := os.Stat(checked.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != checked.mode {
			t.Fatalf("%s mode = %o, want %o", checked.path, info.Mode().Perm(), checked.mode)
		}
	}
	db, err := openWorkflowDatabase(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pragmas := []struct {
		query string
		want  string
	}{
		{"PRAGMA journal_mode", "wal"},
		{"PRAGMA foreign_keys", "1"},
		{"PRAGMA synchronous", "2"},
		{"PRAGMA busy_timeout", "5000"},
		{"PRAGMA user_version", "1"},
	}
	for _, check := range pragmas {
		var got string
		if err := db.QueryRowContext(t.Context(), check.query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != check.want {
			t.Fatalf("%s = %q, want %q", check.query, got, check.want)
		}
	}
	var strictTables int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pragma_table_list
		WHERE name LIKE 'workflow_%' AND strict=1`).Scan(&strictTables); err != nil {
		t.Fatal(err)
	}
	if strictTables != 13 {
		t.Fatalf("strict workflow tables = %d, want 13", strictTables)
	}

	now := time.Date(0, time.January, 2, 3, 4, 5, 987654321, time.UTC)
	run := &Run{
		ID:          "wr_sqlite",
		WorkflowRef: "workflows/test.yml",
		Status:      RunStatusRunning,
		CreatedAt:   now,
		UpdatedAt:   now,
		Inputs:      map[string]any{"exact": json.Number("9007199254740993")},
	}
	if err := store.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	first, err := store.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale := cloneRun(first)
	first.Outputs = map[string]any{"winner": true}
	if err := store.UpdateRun(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	stale.Outputs = map[string]any{"winner": false}
	if err := store.UpdateRun(t.Context(), stale); !errors.Is(err, ErrRunVersionConflict) {
		t.Fatalf("stale UpdateRun() error = %v, want version conflict", err)
	}
	got, err := store.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CreatedAt.Year() != 0 || got.CreatedAt.Nanosecond() != now.Nanosecond() || got.Outputs["winner"] != true {
		t.Fatalf("stored run = %#v", got)
	}
	empty := &Run{
		ID:          "wr_empty_collections",
		WorkflowRef: "workflows/test.yml",
		Status:      RunStatusSucceeded,
		ChildRunIDs: []string{},
		Jobs:        map[string]JobExecution{},
		Steps:       map[string]StepExecution{},
		CreatedAt:   now,
		UpdatedAt:   now,
		humanTasks:  map[string]WorkflowHumanTask{},
	}
	if err := store.CreateRun(t.Context(), empty); err != nil {
		t.Fatal(err)
	}
	preserved, err := store.GetRun(t.Context(), empty.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preserved.ChildRunIDs == nil || preserved.Jobs == nil || preserved.Steps == nil ||
		preserved.humanTasks == nil {
		t.Fatalf("non-nil empty collections were not preserved: %#v", preserved)
	}
}

func TestSQLiteRunStoreConcurrentEventsAndAtomicCancellation(t *testing.T) {
	workspace := privateWorkflowTestWorkspace(t)
	store := NewFileRunStore(workspace)
	now := time.Now().UTC()
	run := &Run{
		ID:          "wr_events",
		WorkflowRef: "workflows/test.yml",
		Status:      RunStatusRunning,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	const writers = 24
	var group sync.WaitGroup
	errorsByWriter := make(chan error, writers)
	for index := 0; index < writers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			errorsByWriter <- store.AppendEvent(context.Background(), RunEvent{
				RunID: run.ID, Kind: "test.concurrent", Message: fmt.Sprint(index),
			})
		}(index)
	}
	group.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.Events(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != writers {
		t.Fatalf("events = %d, want %d", len(events), writers)
	}
	canceled, err := store.CancelRun(t.Context(), run.ID, " operator ")
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Status != RunStatusCanceled || canceled.CancelReason != "operator" {
		t.Fatalf("canceled run = %#v", canceled)
	}
	events, err = store.Events(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != writers+1 || events[len(events)-1].Kind != "workflow.run.canceled" {
		t.Fatalf("cancellation events = %#v", events)
	}
}

//nolint:govet // Test assertions intentionally scope independent errors.
func TestWorkflowSQLiteLegacyMigrationArchivesAndReopensIdempotently(t *testing.T) {
	workspace := privateWorkflowTestWorkspace(t)
	legacyRun := &Run{
		ID: "wr_legacy", WorkflowRef: "workflows/legacy.yml", Status: RunStatusSucceeded,
		CreatedAt: time.Date(2025, 4, 5, 6, 7, 8, 9, time.UTC),
		UpdatedAt: time.Date(2025, 4, 5, 7, 8, 9, 10, time.UTC),
		Event:     map[string]any{"large": json.Number("9007199254740993")},
	}
	runData, err := marshalPersistedRun(legacyRun)
	if err != nil {
		t.Fatal(err)
	}
	runRelative := filepath.Join("workflow_runs", legacyRun.ID, "run.json")
	writeWorkflowLegacyFixture(t, workspace, runRelative, runData)
	event := RunEvent{
		Time:    legacyRun.CreatedAt,
		Kind:    "legacy.event",
		RunID:   legacyRun.ID,
		Payload: map[string]any{"huge": json.Number("1e400")},
	}
	eventData, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	eventData = append(eventData, '\n')
	eventData = append(eventData, []byte("not-json\n")...)
	eventsRelative := filepath.Join("workflow_runs", legacyRun.ID, "events.jsonl")
	writeWorkflowLegacyFixture(t, workspace, eventsRelative, eventData)

	namespace, key := "legacy-space", "legacy-key"
	nativeRelative := filepath.Join(workflowStateDir, safeStorageSegment(namespace), safeStorageSegment(key)+".json")
	nativeData, _ := json.Marshal(nativeStateEnvelope{
		Key:       key,
		Value:     map[string]any{"n": json.Number("42")},
		UpdatedAt: legacyRun.UpdatedAt,
	})
	writeWorkflowLegacyFixture(t, workspace, nativeRelative, nativeData)

	stamp := WorkflowValidationStamp{
		WorkflowRef: legacyRun.WorkflowRef, WorkflowHash: "sha256:test",
		PicoclawVersion: "v1", WorkflowEngine: WorkflowEngineVersion,
		WorkflowSchema: WorkflowSchemaVersion, ValidatorFingerprint: ValidatorFingerprint,
		Status: WorkflowValidationStatusValid, ValidatedAt: legacyRun.UpdatedAt,
	}
	manifest := &WorkflowCompatibilityManifest{
		PicoclawVersion: "v1", WorkflowEngine: WorkflowEngineVersion,
		WorkflowSchema: WorkflowSchemaVersion, ValidatorFingerprint: ValidatorFingerprint,
		UpdatedAt: legacyRun.UpdatedAt, Workflows: map[string]WorkflowValidationStamp{legacyRun.WorkflowRef: stamp},
	}
	manifestData, _ := json.Marshal(manifest)
	manifestRelative := filepath.Join(compatibilityManifestDir, compatibilityManifest)
	writeWorkflowLegacyFixture(t, workspace, manifestRelative, manifestData)

	development := &WorkflowDevelopmentSession{
		ID: "dev_legacy", SessionRevision: "session",
		DraftRevision: "draft", BaseTargetRevision: WorkflowTargetRevisionMissing,
		Reason: WorkflowDevelopmentReasonNew, Status: WorkflowDevelopmentStatusEditing,
		TargetWorkflowRef: "workflows/draft.yml", YAML: "name: Draft\non:\n  manual: {}\njobs: {}\n",
		CreatedAt: legacyRun.CreatedAt, UpdatedAt: legacyRun.UpdatedAt,
	}
	developmentData, _ := json.Marshal(development)
	developmentRelative := filepath.Join(workflowDevelopmentDir, workflowDevelopmentActive)
	writeWorkflowLegacyFixture(t, workspace, developmentRelative, developmentData)

	store, err := NewSQLiteRunStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetRun(t.Context(), legacyRun.ID)
	if err != nil || got.Status != legacyRun.Status {
		t.Fatalf("legacy run = %#v, %v", got, err)
	}
	events, err := store.Events(t.Context(), legacyRun.ID)
	if err != nil || len(events) != 1 {
		t.Fatalf("legacy events = %#v, %v", events, err)
	}
	exec := ExecutionContext{WorkspaceDir: workspace}
	value, exists, err := readNativeStateValue(exec, namespace, key)
	if err != nil || !exists || value.(map[string]any)["n"].(json.Number).String() != "42" {
		t.Fatalf("legacy native state = %#v, %v, %v", value, exists, err)
	}
	active, err := GetWorkflowDevelopmentSession(workspace)
	if err != nil || active == nil || active.ID != development.ID {
		t.Fatalf("legacy development = %#v, %v", active, err)
	}
	for _, relative := range []string{runRelative, eventsRelative, nativeRelative, manifestRelative, developmentRelative} {
		if _, err := os.Lstat(filepath.Join(workspace, relative)); !os.IsNotExist(err) {
			t.Fatalf("legacy source %s remains: %v", relative, err)
		}
		archived := filepath.Join(workspace, "legacy-json", workflowLegacyArchiveLabel, relative)
		if info, err := os.Stat(archived); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("archive %s = %v, %v", archived, info, err)
		}
	}
	db, err := openWorkflowDatabase(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var imported, skipped, issues int
	if err := db.QueryRowContext(t.Context(), `SELECT COALESCE(SUM(imported_count),0),
		COALESCE(SUM(skipped_count),0) FROM storage_imports WHERE component=?`,
		workflowDatabaseComponent).Scan(&imported, &skipped); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM storage_import_issues
		WHERE component=?`, workflowDatabaseComponent).Scan(&issues); err != nil {
		t.Fatal(err)
	}
	if imported < 5 || skipped != 1 || issues != 1 {
		t.Fatalf("migration counts = imported %d skipped %d issues %d", imported, skipped, issues)
	}
	if _, err := NewSQLiteRunStore(workspace); err != nil {
		t.Fatalf("idempotent reopen: %v", err)
	}
	var runCount int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM workflow_runs`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 {
		t.Fatalf("run count after reopen = %d", runCount)
	}
}

func writeWorkflowLegacyFixture(t *testing.T, workspace, relative string, data []byte) {
	t.Helper()
	path := filepath.Join(workspace, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

//nolint:govet // Test assertions intentionally scope independent errors.
func TestWorkflowDatabaseRejectsTooNewVersionAndNoncanonicalPayload(t *testing.T) {
	workspace := privateWorkflowTestWorkspace(t)
	store := NewFileRunStore(workspace)
	now := time.Now().UTC()
	run := &Run{
		ID: "wr_corrupt", WorkflowRef: "workflows/test.yml", Status: RunStatusRunning,
		CreatedAt: now, UpdatedAt: now, Inputs: map[string]any{"ok": true},
	}
	if err := store.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	db, err := openWorkflowDatabase(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE workflow_run_payloads SET inputs_json=? WHERE run_id=?`,
		[]byte(`{ "ok" : true }`), run.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSQLiteRunStore(workspace); err == nil || !strings.Contains(err.Error(), "noncanonical JSON") {
		t.Fatalf("noncanonical reopen error = %v", err)
	}

	workspace = privateWorkflowTestWorkspace(t)
	if _, err := NewSQLiteRunStore(workspace); err != nil {
		t.Fatal(err)
	}
	path, _ := workflowDatabasePath(workspace)
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA user_version=999`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if _, err := NewSQLiteRunStore(workspace); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("too-new reopen error = %v", err)
	}
}

func TestWorkflowLegacyPrivateMarkerMismatchAbortsWithoutArchive(t *testing.T) {
	workspace := privateWorkflowTestWorkspace(t)
	now := time.Now().UTC()
	run := &Run{
		ID: "wr_public", WorkflowRef: "workflows/test.yml", Status: RunStatusRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	data, err := marshalPersistedRun(run)
	if err != nil {
		t.Fatal(err)
	}
	runRelative := filepath.Join("workflow_runs", run.ID, "run.json")
	markerRelative := filepath.Join("workflow_runs", run.ID, privateRunMarkerFilename)
	writeWorkflowLegacyFixture(t, workspace, runRelative, data)
	writeWorkflowLegacyFixture(t, workspace, markerRelative, []byte(privateRunMarkerContents))
	if _, err := NewSQLiteRunStore(workspace); err == nil || strings.Contains(err.Error(), string(data)) {
		t.Fatalf("private-marker migration error = %v", err)
	}
	for _, relative := range []string{runRelative, markerRelative} {
		if _, err := os.Stat(filepath.Join(workspace, relative)); err != nil {
			t.Fatalf("source %s changed after rollback: %v", relative, err)
		}
		archive := filepath.Join(workspace, "legacy-json", workflowLegacyArchiveLabel, relative)
		if _, err := os.Stat(archive); !os.IsNotExist(err) {
			t.Fatalf("source %s archived after rollback: %v", relative, err)
		}
	}
}

func TestWorkflowDatabaseReopenRejectsNormalizedShapeTampering(t *testing.T) {
	tests := []struct {
		name     string
		mutation string
	}{
		{"missing payload row", `DELETE FROM workflow_run_payloads WHERE run_id='wr_shape'`},
		{"marker mismatch", `INSERT INTO workflow_private_run_markers(run_id) VALUES('wr_shape')`},
		{"private flag mismatch", `UPDATE workflow_runs SET is_private=1 WHERE run_id='wr_shape'`},
		{"nil jobs own rows", `UPDATE workflow_runs SET jobs_is_null=1 WHERE run_id='wr_shape'`},
		{"nil children own rows", `UPDATE workflow_runs SET child_ids_is_null=1 WHERE run_id='wr_shape'`},
		{"child position gap", `UPDATE workflow_run_children SET position=3 WHERE run_id='wr_shape' AND position=1`},
		{"event sequence gap", `UPDATE workflow_run_events SET sequence=3 WHERE run_id='wr_shape' AND sequence=1`},
		{"issue position gap", `UPDATE workflow_validation_issues SET position=3
			WHERE workflow_ref='workflows/shape.yml' AND issue_kind='error' AND position=1`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := privateWorkflowTestWorkspace(t)
			store := NewFileRunStore(workspace)
			now := time.Now().UTC()
			run := &Run{
				ID:          "wr_shape",
				WorkflowRef: "workflows/shape.yml",
				Status:      RunStatusRunning,
				ChildRunIDs: []string{"wr_child_a", "wr_child_b"},
				Jobs: map[string]JobExecution{
					"a": {ID: "a", Status: RunStatusSucceeded},
					"b": {ID: "b", Status: RunStatusSucceeded},
				},
				Steps: map[string]StepExecution{
					"a/one": {ID: "one", Status: RunStatusSucceeded},
					"b/two": {ID: "two", Status: RunStatusSucceeded},
				},
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := store.CreateRun(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			for index := range 2 {
				if err := store.AppendEvent(t.Context(), RunEvent{
					RunID: run.ID,
					Kind:  fmt.Sprintf("shape.%d", index),
				}); err != nil {
					t.Fatal(err)
				}
			}
			manifest := &WorkflowCompatibilityManifest{
				PicoclawVersion:      "test",
				WorkflowEngine:       WorkflowEngineVersion,
				WorkflowSchema:       WorkflowSchemaVersion,
				ValidatorFingerprint: ValidatorFingerprint,
				UpdatedAt:            now,
				Workflows: map[string]WorkflowValidationStamp{
					run.WorkflowRef: {
						WorkflowRef:          run.WorkflowRef,
						PicoclawVersion:      "test",
						WorkflowEngine:       WorkflowEngineVersion,
						WorkflowSchema:       WorkflowSchemaVersion,
						ValidatorFingerprint: ValidatorFingerprint,
						Status:               WorkflowValidationStatusInvalid,
						ValidatedAt:          now,
						Errors: []WorkflowValidationIssue{
							{Path: "first", Message: "first"},
							{Path: "second", Message: "second"},
						},
					},
				},
			}
			if err := writeCompatibilityManifest(workspace, manifest); err != nil {
				t.Fatal(err)
			}
			path, err := workflowDatabasePath(workspace)
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(test.mutation); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := NewSQLiteRunStore(workspace); !errors.Is(err, ErrWorkflowStorageUnavailable) {
				t.Fatalf("tampered reopen error = %v, want storage unavailable", err)
			}
		})
	}
}
