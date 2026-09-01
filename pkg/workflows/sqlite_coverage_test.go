package workflows

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const workflowSQLiteFaultDriverName = "picoclaw-workflow-sqlite-fault"

var registerWorkflowSQLiteFaultDriver sync.Once

type workflowSQLiteFaultDriver struct{}

type workflowSQLiteFaultConn struct{}

func (workflowSQLiteFaultDriver) Open(string) (driver.Conn, error) {
	return workflowSQLiteFaultConn{}, nil
}

func (workflowSQLiteFaultConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("injected workflow SQLite prepare failure")
}

func (workflowSQLiteFaultConn) Close() error { return nil }

func (workflowSQLiteFaultConn) Begin() (driver.Tx, error) {
	return nil, errors.New("injected workflow SQLite transaction failure")
}

func (workflowSQLiteFaultConn) QueryContext(
	context.Context,
	string,
	[]driver.NamedValue,
) (driver.Rows, error) {
	return nil, errors.New("injected workflow SQLite query failure")
}

func (workflowSQLiteFaultConn) ExecContext(
	context.Context,
	string,
	[]driver.NamedValue,
) (driver.Result, error) {
	return nil, errors.New("injected workflow SQLite exec failure")
}

var (
	_ driver.QueryerContext = workflowSQLiteFaultConn{}
	_ driver.ExecerContext  = workflowSQLiteFaultConn{}
)

//nolint:govet // Independent storage assertions intentionally use narrow error scopes.
func TestWorkflowSQLiteRichRelationalRoundTrip(t *testing.T) {
	workspace := privateWorkflowTestWorkspace(t)
	store, err := NewSQLiteRunStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 31, 12, 34, 56, 789, time.UTC)
	later := now.Add(time.Minute)
	run := &Run{
		ID:                "wr_rich_relational",
		WorkflowRef:       "workflows/rich.yml",
		Status:            RunStatusRunning,
		ContextVisibility: "public",
		ParentRunID:       "wr_parent",
		ChildRunIDs:       []string{"wr_child_b", "wr_child_a"},
		CallerJobID:       "caller",
		RetryOfRunID:      "wr_retry_source",
		Session:           "agent:main:web:rich",
		Delivery: Delivery{
			Channel: "web", ChatID: "chat", TopicID: "topic", ThreadTS: "thread",
			MessageID: "message", ReplyToMessageID: "reply",
			ReplyHandles: map[string]string{"primary": "handle"},
		},
		Event:   map[string]any{"decimal": json.Number("9007199254740993")},
		Inputs:  map[string]any{"nested": map[string]any{"enabled": true}},
		Outputs: map[string]any{"result": []any{"ok", json.Number("1e400")}},
		Jobs: map[string]JobExecution{
			"job/z": {ID: "z", Status: RunStatusFailed, Error: "job error", Outputs: map[string]any{"z": 2}},
			"job/a": {ID: "a", Status: RunStatusSucceeded, Outputs: map[string]any{"a": 1}},
		},
		Steps: map[string]StepExecution{
			"job/z/step": {ID: "step", Status: RunStatusFailed, Error: "step error", Outputs: map[string]any{"n": 3}},
		},
		Error:             "run error",
		CancelReason:      "requested",
		CreatedAt:         now,
		UpdatedAt:         now,
		CompletedAt:       &later,
		CancelRequestedAt: &now,
		execution: &workflowExecutionState{
			Cursor: &WorkflowExecutionCursor{JobID: "job/z", StepIndex: 2},
		},
		humanTasks: map[string]WorkflowHumanTask{
			"job/z/review": {
				ID: "task-rich", WorkflowRef: "workflows/rich.yml", JobID: "job/z", StepID: "review",
				Status: HumanTaskStatusAnswered, Revision: 7, InputHash: "sha256:input", Title: "Review",
				Questions:      []any{map[string]any{"id": "approve"}},
				ResponseSchema: map[string]any{"type": "boolean"},
				GateForm:       &GateForm{GateRef: "gates.review", Prompt: "Approve?"},
				ActorKind:      "human", ExecutionID: "execution", ActionRevision: "revision",
				GateWorkflow: &gateActionWorkflowContinuation{
					ChildRunID: "wr_gate_child", ChildTaskID: "task-child", GateRef: "gates.review",
					ExecutionID: "gate-execution", ActionRevision: "gate-revision", InputHash: "gate-input",
				},
				ResponseID: "response", Response: map[string]any{"approve": true},
				CreatedAt: now, UpdatedAt: later, AnsweredAt: &later, CanceledAt: &now, RetryAt: &later,
			},
		},
	}
	if err := store.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentRunID != run.ParentRunID || got.RetryOfRunID != run.RetryOfRunID ||
		len(got.ChildRunIDs) != 2 || len(got.Jobs) != 2 || len(got.Steps) != 1 ||
		len(got.humanTasks) != 1 || got.CompletedAt == nil || got.CancelRequestedAt == nil ||
		got.execution == nil || got.execution.Cursor == nil {
		t.Fatalf("rich round trip lost normalized relations: %#v", got)
	}
	task := got.humanTasks["job/z/review"]
	if task.GateForm == nil || task.GateWorkflow == nil || task.AnsweredAt == nil ||
		task.CanceledAt == nil || task.RetryAt == nil || task.ResponseID != "response" {
		t.Fatalf("rich human task lost fields: %#v", task)
	}
	got.ChildRunIDs = []string{"wr_child_c"}
	got.Jobs = map[string]JobExecution{"job/new": {ID: "new", Status: RunStatusRunning}}
	got.Steps = map[string]StepExecution{}
	got.humanTasks = map[string]WorkflowHumanTask{}
	if err := store.UpdateRun(t.Context(), got); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetRun(t.Context(), run.ID)
	if err != nil || len(updated.ChildRunIDs) != 1 || len(updated.Jobs) != 1 ||
		len(updated.Steps) != 0 || len(updated.humanTasks) != 0 {
		t.Fatalf("updated normalized relations = %#v, %v", updated, err)
	}
	if err := store.AppendEvent(t.Context(), RunEvent{
		RunID: run.ID, Kind: "rich.explicit", Time: later,
		Payload: map[string]any{"exact": json.Number("9007199254740993")},
	}); err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(t.Context(), run.ID)
	if err != nil || len(events) != 1 || events[0].Time.Nanosecond() != later.Nanosecond() {
		t.Fatalf("rich events = %#v, %v", events, err)
	}
}

func TestWorkflowSQLiteCodecAndValidationBoundaries(t *testing.T) {
	var nilRun *Run
	if encoded, err := encodeWorkflowJSON(nilRun, 1); err != nil || encoded != nil {
		t.Fatalf("typed nil encoding = %q, %v", encoded, err)
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if _, err := encodeWorkflowJSON(cycle, 1024); err == nil {
		t.Fatal("cyclic JSON encoded")
	}
	if _, err := encodeWorkflowJSON(map[string]any{"value": "too large"}, 1); err == nil {
		t.Fatal("oversized JSON encoded")
	}
	for _, data := range [][]byte{[]byte(`{"unterminated":`), []byte(`{} {}`)} {
		var value any
		if err := decodeWorkflowJSON(data, &value); err == nil {
			t.Fatalf("decodeWorkflowJSON(%q) succeeded", data)
		}
		if _, err := canonicalWorkflowJSON(data); err == nil {
			t.Fatalf("canonicalWorkflowJSON(%q) succeeded", data)
		}
	}
	if err := decodeWorkflowJSON(nil, &map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if encoded, err := canonicalWorkflowJSON(nil); err != nil || encoded != nil {
		t.Fatalf("empty canonical JSON = %q, %v", encoded, err)
	}
	for _, value := range []string{"", "bad\x00value", string([]byte{0xff})} {
		if err := workflowText(value, "test", 1, 8); err == nil {
			t.Fatalf("workflowText(%q) succeeded", value)
		}
	}
	base := Run{ID: "valid", WorkflowRef: "workflows/valid.yml", Status: RunStatusRunning}
	invalidRuns := []*Run{
		nil,
		func() *Run { value := base; value.ID = " padded "; return &value }(),
		func() *Run { value := base; value.ID = ""; return &value }(),
		func() *Run {
			value := base
			value.WorkflowRef = strings.Repeat("r", maximumWorkflowReferenceBytes+1)
			return &value
		}(),
		func() *Run { value := base; value.Status = "bad\x00status"; return &value }(),
		func() *Run { value := base; value.ContextVisibility = WorkflowContextVisibilityPrivate; return &value }(),
		func() *Run {
			value := base
			value.ChildRunIDs = make([]string, maximumWorkflowChildrenPerRun+1)
			return &value
		}(),
		func() *Run { value := base; value.CreatedAt = time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC); return &value }(),
		func() *Run {
			value := base
			value.UpdatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			return &value
		}(),
		func() *Run {
			value := base
			outside := time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)
			value.CompletedAt = &outside
			return &value
		}(),
		func() *Run {
			value := base
			outside := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			value.CancelRequestedAt = &outside
			return &value
		}(),
	}
	for index, run := range invalidRuns {
		if err := prepareWorkflowRun(run); err == nil {
			t.Fatalf("invalid run %d passed validation", index)
		}
	}
	if _, _, err := nullableWorkflowTimestamp(nil); err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []error{
		ErrPrivateWorkflowContext, ErrHumanTaskNotFound, ErrHumanTaskConflict, ErrRunCanceled,
		ErrRunAlreadyExists, ErrHumanTaskStale, ErrHumanTaskResponseInvalid,
		ErrRunConcurrencyLimit, ErrRunVersionConflict, ErrInvalidCancelReason,
		os.ErrNotExist, context.Canceled, context.DeadlineExceeded,
	} {
		if got := workflowDatabaseError("test", sentinel); !errors.Is(got, sentinel) {
			t.Fatalf("workflowDatabaseError(%v) = %v", sentinel, got)
		}
	}
	if workflowDatabaseError("test", nil) != nil {
		t.Fatal("nil database error was wrapped")
	}
	if got := workflowDatabaseError(
		"test",
		errors.New("driver failed"),
	); !errors.Is(
		got,
		ErrWorkflowStorageUnavailable,
	) {
		t.Fatalf("driver error = %v", got)
	}
}

func sqliteCoverageRun(id string) *Run {
	now := time.Date(2026, time.August, 31, 1, 2, 3, 4, time.UTC)
	return &Run{
		ID: id, WorkflowRef: "workflows/coverage.yml", Status: RunStatusRunning,
		CreatedAt: now, UpdatedAt: now,
		Event: map[string]any{"value": true}, Inputs: map[string]any{"value": true},
		Outputs:     map[string]any{"value": true},
		Delivery:    Delivery{ReplyHandles: map[string]string{"reply": "handle"}},
		ChildRunIDs: []string{"wr_child"},
		Jobs: map[string]JobExecution{
			"job": {ID: "job", Status: RunStatusRunning, Outputs: map[string]any{"value": true}},
		},
		Steps: map[string]StepExecution{
			"job/step": {ID: "step", Status: RunStatusRunning, Outputs: map[string]any{"value": true}},
		},
		execution: &workflowExecutionState{Cursor: &WorkflowExecutionCursor{JobID: "job"}},
		humanTasks: map[string]WorkflowHumanTask{
			"job/task": {
				ID: "task", WorkflowRef: "workflows/coverage.yml", JobID: "job", StepID: "task",
				Status: HumanTaskStatusWaiting, Revision: 1, InputHash: "hash", Title: "Title",
				Questions: []any{"question"}, ResponseSchema: map[string]any{"type": "string"},
				GateForm:     &GateForm{GateRef: "gates.coverage"},
				GateWorkflow: &gateActionWorkflowContinuation{ChildRunID: "wr_child"},
				Response:     map[string]any{"value": true}, CreatedAt: now, UpdatedAt: now,
			},
		},
	}
}

func TestWorkflowSQLiteCorruptRelationalColumnsFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		queries []string
	}{
		{"privacy marker mismatch", []string{`INSERT INTO workflow_private_run_markers(run_id) VALUES('wr_corrupt')`}},
		{"origin columns", []string{`UPDATE workflow_runs SET origin_kind='external_event' WHERE run_id='wr_corrupt'`}},
		{
			"completed timestamp",
			[]string{
				`PRAGMA ignore_check_constraints=ON`,
				`UPDATE workflow_runs SET completed_at_seconds=1,completed_at_nanosecond=NULL WHERE run_id='wr_corrupt'`,
			},
		},
		{
			"cancel timestamp",
			[]string{
				`PRAGMA ignore_check_constraints=ON`,
				`UPDATE workflow_runs SET cancel_at_seconds=1,cancel_at_nanosecond=NULL WHERE run_id='wr_corrupt'`,
			},
		},
		{"event payload", []string{`UPDATE workflow_run_payloads SET event_json=x'7b' WHERE run_id='wr_corrupt'`}},
		{"inputs payload", []string{`UPDATE workflow_run_payloads SET inputs_json=x'7b' WHERE run_id='wr_corrupt'`}},
		{"outputs payload", []string{`UPDATE workflow_run_payloads SET outputs_json=x'7b' WHERE run_id='wr_corrupt'`}},
		{
			"delivery handles",
			[]string{`UPDATE workflow_run_payloads SET delivery_handles_json=x'7b' WHERE run_id='wr_corrupt'`},
		},
		{
			"execution payload",
			[]string{`UPDATE workflow_run_payloads SET execution_json=x'7b' WHERE run_id='wr_corrupt'`},
		},
		{"private payload", []string{
			`UPDATE workflow_runs SET is_private=1,context_visibility='private' WHERE run_id='wr_corrupt'`,
			`INSERT INTO workflow_private_run_markers(run_id) VALUES('wr_corrupt')`,
			`UPDATE workflow_run_payloads SET private_context_json=x'7b' WHERE run_id='wr_corrupt'`,
		}},
		{"job outputs", []string{`UPDATE workflow_run_jobs SET outputs_json=x'7b' WHERE run_id='wr_corrupt'`}},
		{"step outputs", []string{`UPDATE workflow_run_steps SET outputs_json=x'7b' WHERE run_id='wr_corrupt'`}},
		{
			"task answered timestamp",
			[]string{
				`PRAGMA ignore_check_constraints=ON`,
				`UPDATE workflow_human_tasks SET answered_at_seconds=1,answered_at_nanosecond=NULL WHERE run_id='wr_corrupt'`,
			},
		},
		{
			"task canceled timestamp",
			[]string{
				`PRAGMA ignore_check_constraints=ON`,
				`UPDATE workflow_human_tasks SET canceled_at_seconds=1,canceled_at_nanosecond=NULL WHERE run_id='wr_corrupt'`,
			},
		},
		{
			"task retry timestamp",
			[]string{
				`PRAGMA ignore_check_constraints=ON`,
				`UPDATE workflow_human_tasks SET retry_at_seconds=1,retry_at_nanosecond=NULL WHERE run_id='wr_corrupt'`,
			},
		},
		{"task questions", []string{`UPDATE workflow_human_tasks SET questions_json=x'7b' WHERE run_id='wr_corrupt'`}},
		{
			"task response schema",
			[]string{`UPDATE workflow_human_tasks SET response_schema_json=x'7b' WHERE run_id='wr_corrupt'`},
		},
		{"task gate form", []string{`UPDATE workflow_human_tasks SET gate_form_json=x'7b' WHERE run_id='wr_corrupt'`}},
		{
			"task gate workflow",
			[]string{`UPDATE workflow_human_tasks SET gate_workflow_json=x'7b' WHERE run_id='wr_corrupt'`},
		},
		{"task response", []string{`UPDATE workflow_human_tasks SET response_json=x'7b' WHERE run_id='wr_corrupt'`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := privateWorkflowTestWorkspace(t)
			store := NewFileRunStore(workspace)
			if err := store.CreateRun(t.Context(), sqliteCoverageRun("wr_corrupt")); err != nil {
				t.Fatal(err)
			}
			db, err := openWorkflowDatabase(t.Context(), workspace)
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			for _, query := range test.queries {
				if _, err := db.ExecContext(t.Context(), query); err != nil {
					db.Close()
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := store.GetRun(t.Context(), "wr_corrupt"); err == nil {
				t.Fatal("corrupt relational record was returned")
			}
		})
	}
}

func TestWorkflowSQLiteRejectsInvalidNormalizedChildren(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	tests := []struct {
		name   string
		mutate func(*Run)
	}{
		{"event JSON", func(run *Run) { run.Event = cycle }},
		{"inputs JSON", func(run *Run) { run.Inputs = cycle }},
		{"outputs JSON", func(run *Run) { run.Outputs = cycle }},
		{"empty child", func(run *Run) { run.ChildRunIDs = []string{""} }},
		{"duplicate child", func(run *Run) { run.ChildRunIDs = []string{"same", "same"} }},
		{"job outputs", func(run *Run) { job := run.Jobs["job"]; job.Outputs = cycle; run.Jobs["job"] = job }},
		{
			"step outputs",
			func(run *Run) { step := run.Steps["job/step"]; step.Outputs = cycle; run.Steps["job/step"] = step },
		},
		{"task questions", func(run *Run) {
			task := run.humanTasks["job/task"]
			task.Questions = cycle
			run.humanTasks["job/task"] = task
		}},
		{"task response schema", func(run *Run) {
			task := run.humanTasks["job/task"]
			task.ResponseSchema = cycle
			run.humanTasks["job/task"] = task
		}},
		{"task response", func(run *Run) {
			task := run.humanTasks["job/task"]
			task.Response = cycle
			run.humanTasks["job/task"] = task
		}},
		{"task created timestamp", func(run *Run) {
			task := run.humanTasks["job/task"]
			task.CreatedAt = time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)
			run.humanTasks["job/task"] = task
		}},
		{"task updated timestamp", func(run *Run) {
			task := run.humanTasks["job/task"]
			task.UpdatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			run.humanTasks["job/task"] = task
		}},
		{"task answered timestamp", func(run *Run) {
			task := run.humanTasks["job/task"]
			value := time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)
			task.AnsweredAt = &value
			run.humanTasks["job/task"] = task
		}},
		{"task canceled timestamp", func(run *Run) {
			task := run.humanTasks["job/task"]
			value := time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)
			task.CanceledAt = &value
			run.humanTasks["job/task"] = task
		}},
		{"task retry timestamp", func(run *Run) {
			task := run.humanTasks["job/task"]
			value := time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)
			task.RetryAt = &value
			run.humanTasks["job/task"] = task
		}},
	}
	workspace := privateWorkflowTestWorkspace(t)
	store := NewFileRunStore(workspace)
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := sqliteCoverageRun("wr_invalid_" + strings.ReplaceAll(test.name, " ", "_"))
			run.execution = nil
			test.mutate(run)
			if err := store.CreateRun(t.Context(), run); err == nil {
				t.Fatalf("invalid normalized run %d was created", index)
			}
		})
	}
}

//nolint:govet // Independent storage assertions intentionally use narrow error scopes.
func TestWorkflowSQLiteLegacyImportSkipMatrix(t *testing.T) {
	workspace := privateWorkflowTestWorkspace(t)
	db, err := openWorkflowDatabase(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	input := func(relative string, data []byte) sqlitestore.LegacyInput {
		return sqlitestore.LegacyInput{Relative: relative, Data: data, Limit: maximumWorkflowLegacySourceBytes}
	}
	assertSkipped := func(t *testing.T, result sqlitestore.ImportResult, err error, code string) {
		t.Helper()
		if err != nil || result.Skipped != 1 || len(result.Issues) != 1 || result.Issues[0].Code != code {
			t.Fatalf("skip result = %#v, %v, want %s", result, err, code)
		}
	}
	result, err := importWorkflowLegacySource(t.Context(), conn, input("unknown.json", []byte(`{}`)))
	if err == nil || result.Imported != 0 {
		t.Fatalf("unknown source = %#v, %v", result, err)
	}
	result, err = importWorkflowLegacyRun(t.Context(), conn, input("workflow_runs/bad/run.json", []byte(`{`)))
	assertSkipped(t, result, err, "invalid_run_json")
	validRun := sqliteCoverageRun("wr_legacy_matrix")
	validRun.ChildRunIDs, validRun.Jobs, validRun.Steps, validRun.humanTasks, validRun.execution = nil, nil, nil, nil, nil
	data, err := marshalPersistedRun(validRun)
	if err != nil {
		t.Fatal(err)
	}
	result, err = importWorkflowLegacyRun(t.Context(), conn, input("workflow_runs/wrong/run.json", data))
	assertSkipped(t, result, err, "invalid_run_identity")
	invalidRecord := *validRun
	invalidRecord.ID = " padded "
	invalidData, _ := json.Marshal(&invalidRecord)
	result, err = importWorkflowLegacyRun(t.Context(), conn, input("workflow_runs/padded/run.json", invalidData))
	assertSkipped(t, result, err, "invalid_run_record")
	result, err = importWorkflowLegacyRun(t.Context(), conn, input("workflow_runs/wr_legacy_matrix/run.json", data))
	if err != nil || result.Imported != 1 {
		t.Fatalf("valid run import = %#v, %v", result, err)
	}
	result, err = importWorkflowLegacyRun(t.Context(), conn, input("workflow_runs/wr_legacy_matrix/run.json", data))
	assertSkipped(t, result, err, "duplicate_run_identity")
	result, err = importWorkflowLegacyMarker(
		t.Context(),
		conn,
		input("workflow_runs/wr_legacy_matrix/.private-context", []byte("bad")),
	)
	assertSkipped(t, result, err, "invalid_private_marker")
	result, err = importWorkflowLegacyMarker(t.Context(), conn, input("bad-marker", []byte(privateRunMarkerContents)))
	assertSkipped(t, result, err, "invalid_private_marker_identity")
	if _, err := importWorkflowLegacyMarker(
		t.Context(),
		conn,
		input("workflow_runs/missing/.private-context", []byte(privateRunMarkerContents)),
	); err == nil {
		t.Fatal("orphan private marker imported")
	}
	result, err = importWorkflowLegacyEvents(t.Context(), conn, input("bad-events", []byte(`{}`)))
	assertSkipped(t, result, err, "invalid_event_source")
	eventData := []byte("\n{\"run_id\":\"wrong\"}\n{\"run_id\":\"wr_missing\",\"kind\":\"orphan\"}\n")
	result, err = importWorkflowLegacyEvents(
		t.Context(),
		conn,
		input("workflow_runs/wr_missing/events.jsonl", eventData),
	)
	if err != nil || result.Skipped != 2 || len(result.Issues) != 2 {
		t.Fatalf("invalid event import = %#v, %v", result, err)
	}
	result, err = importWorkflowLegacyNativeState(t.Context(), conn, input("workflow_state/ns/key.json", []byte(`{`)))
	assertSkipped(t, result, err, "invalid_native_state")
	native := nativeStateEnvelope{Key: "key", Value: map[string]any{"ok": true}, UpdatedAt: time.Now().UTC()}
	nativeData, _ := json.Marshal(native)
	nativeRelative := filepath.ToSlash(filepath.Join("workflow_state", "ns", safeStorageSegment(native.Key)+".json"))
	result, err = importWorkflowLegacyNativeState(t.Context(), conn, input("bad-native", nativeData))
	assertSkipped(t, result, err, "invalid_native_identity")
	result, err = importWorkflowLegacyNativeState(t.Context(), conn, input("workflow_state/ns/wrong.json", nativeData))
	assertSkipped(t, result, err, "invalid_native_identity")
	result, err = importWorkflowLegacyNativeState(t.Context(), conn, input(nativeRelative, nativeData))
	if err != nil || result.Imported != 1 {
		t.Fatalf("native import = %#v, %v", result, err)
	}
	result, err = importWorkflowLegacyNativeState(t.Context(), conn, input(nativeRelative, nativeData))
	assertSkipped(t, result, err, "duplicate_native_identity")
	nilNative := nativeStateEnvelope{Key: "nil-key", UpdatedAt: time.Now().UTC()}
	nilNativeData, _ := json.Marshal(nilNative)
	nilNativeRelative := filepath.ToSlash(
		filepath.Join("workflow_state", "ns", safeStorageSegment(nilNative.Key)+".json"),
	)
	result, err = importWorkflowLegacyNativeState(t.Context(), conn, input(nilNativeRelative, nilNativeData))
	if err != nil || result.Imported != 1 {
		t.Fatalf("nil native import = %#v, %v", result, err)
	}
	result, err = importWorkflowLegacyManifest(
		t.Context(),
		conn,
		input("workflow_validations/manifest.json", []byte(`{`)),
	)
	assertSkipped(t, result, err, "invalid_validation_manifest")
	tooManyIssues := make([]WorkflowValidationIssue, maximumWorkflowIssuesPerStamp+1)
	manifest := WorkflowCompatibilityManifest{
		UpdatedAt: time.Now().UTC(),
		Workflows: map[string]WorkflowValidationStamp{
			"workflows/identity.yml": {WorkflowRef: "workflows/wrong.yml", ValidatedAt: time.Now().UTC()},
			"workflows/invalid.yml": {
				WorkflowRef: "workflows/invalid.yml",
				ValidatedAt: time.Now().UTC(),
				Errors:      tooManyIssues,
			},
			"workflows/defaulted.yml": {
				ValidatedAt: time.Now().UTC(), Errors: []WorkflowValidationIssue{{Path: "jobs", Message: "error"}},
				Warnings: []WorkflowValidationIssue{{Path: "on", Message: "warning"}},
			},
		},
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	result, err = importWorkflowLegacyManifest(
		t.Context(),
		conn,
		input("workflow_validations/manifest.json", manifestData),
	)
	if err != nil || result.Imported != 2 || result.Skipped != 2 || len(result.Issues) != 2 {
		t.Fatalf("manifest skip matrix = %#v, %v", result, err)
	}
	result, err = importWorkflowLegacyDevelopment(t.Context(), conn, input("workflow_dev/active.json", []byte(`{`)))
	assertSkipped(t, result, err, "invalid_development_session")
	development := WorkflowDevelopmentSession{
		ID:        "dev_matrix",
		Status:    "unknown",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	developmentData, _ := json.Marshal(development)
	result, err = importWorkflowLegacyDevelopment(
		t.Context(),
		conn,
		input("workflow_dev/archive/dev.json", developmentData),
	)
	assertSkipped(t, result, err, "invalid_development_lifecycle")
	development.Status = WorkflowDevelopmentStatusEditing
	developmentData, _ = json.Marshal(development)
	result, err = importWorkflowLegacyDevelopment(t.Context(), conn, input("workflow_dev/active.json", developmentData))
	if err != nil || result.Imported != 1 {
		t.Fatalf("development import = %#v, %v", result, err)
	}
	result, err = importWorkflowLegacyDevelopment(t.Context(), conn, input("workflow_dev/active.json", developmentData))
	assertSkipped(t, result, err, "duplicate_development_identity")
	development.ID = "dev_archived_matrix"
	development.Status = " published "
	developmentData, _ = json.Marshal(development)
	result, err = importWorkflowLegacyDevelopment(
		t.Context(),
		conn,
		input("workflow_dev/archive/dev.json", developmentData),
	)
	if err != nil || result.Imported != 1 {
		t.Fatalf("archived development import = %#v, %v", result, err)
	}
	privateWorkspace := privateWorkflowTestWorkspace(t)
	privateStore := NewFileRunStore(privateWorkspace)
	compilation, err := CompileGateWorkflow("Private marker", []GateSpec{{
		ID: "policy", Kind: GateDeterministic, When: "false", Title: "Policy", Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "marker"})
	if err != nil {
		t.Fatal(err)
	}
	privateResult, err := (&Executor{WorkspaceDir: privateWorkspace, Store: privateStore}).Run(t.Context(), RunRequest{
		Workflow: compilation.Workflow, WorkflowRef: "inline/private-marker", PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	privateDB, err := openWorkflowDatabase(t.Context(), privateWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	privateConn, err := privateDB.Conn(t.Context())
	if err != nil {
		privateDB.Close()
		t.Fatal(err)
	}
	result, err = importWorkflowLegacyMarker(t.Context(), privateConn, input(
		"workflow_runs/"+privateResult.RunID+"/"+privateRunMarkerFilename,
		[]byte(privateRunMarkerContents),
	))
	privateConn.Close()
	privateDB.Close()
	if err != nil || result.Imported != 1 {
		t.Fatalf("matching private marker import = %#v, %v", result, err)
	}
}

func TestWorkflowSQLiteLegacyEnumerationRejectsUnsafeTrees(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{"run root file", func(t *testing.T, workspace string) {
			if err := os.WriteFile(filepath.Join(workspace, "workflow_runs"), []byte("bad"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"run root unexpected file", func(t *testing.T, workspace string) {
			path := filepath.Join(workspace, "workflow_runs", "not-a-directory")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("bad"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"run unexpected child", func(t *testing.T, workspace string) {
			path := filepath.Join(workspace, "workflow_runs", "run", "unexpected.txt")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("bad"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"run child directory", func(t *testing.T, workspace string) {
			path := filepath.Join(workspace, "workflow_runs", "run", "nested")
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"unreadable run directory", func(t *testing.T, workspace string) {
			path := filepath.Join(workspace, "workflow_runs", "run")
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
		}},
		{"writable native directory", func(t *testing.T, workspace string) {
			path := filepath.Join(workspace, workflowStateDir)
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o722); err != nil {
				t.Fatal(err)
			}
		}},
		{"native symlink", func(t *testing.T, workspace string) {
			target := filepath.Join(workspace, "target.json")
			if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(workspace, workflowStateDir)
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(root, "link.json")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := privateWorkflowTestWorkspace(t)
			test.setup(t, workspace)
			if _, err := enumerateWorkflowLegacySources(workspace); err == nil {
				t.Fatal("unsafe legacy tree was enumerated")
			}
		})
	}
}

func TestWorkflowSQLiteLegacyEnumerationOrdersAndBoundsSources(t *testing.T) {
	workspace := privateWorkflowTestWorkspace(t)
	for _, relative := range []string{
		filepath.Join(workflowStateDir, "namespace", "first.json"),
		filepath.Join(workflowStateDir, "namespace", "second.JSON"),
		filepath.Join(workflowStateDir, "namespace", "ignored.txt"),
		filepath.Join(workflowStateDir, "too", "deep", "ignored.json"),
		filepath.Join(workflowDevelopmentDir, "archive", "published.json"),
	} {
		writeWorkflowLegacyFixture(t, workspace, relative, []byte(`{}`))
	}
	writeWorkflowLegacyFixture(t, workspace,
		filepath.Join(workflowStateDir, workflowDevelopmentPublishJournalFile), []byte(`{}`))
	writeWorkflowLegacyFixture(t, workspace,
		filepath.Join(workflowStateDir, workflowTemplateInstallJournalFile), []byte(`{}`))
	sources, err := enumerateWorkflowLegacySources(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 {
		t.Fatalf("bounded legacy sources = %#v", sources)
	}
	for index := 1; index < len(sources); index++ {
		if sources[index-1].Order > sources[index].Order ||
			sources[index-1].Order == sources[index].Order && sources[index-1].Relative > sources[index].Relative {
			t.Fatalf("legacy source order = %#v", sources)
		}
	}
}

func TestWorkflowSQLiteDirectImporterDatabaseErrors(t *testing.T) {
	workspace := privateWorkflowTestWorkspace(t)
	db, err := openWorkflowDatabase(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	input := sqlitestore.LegacyInput{Relative: "workflow_runs/wr/run.json", Data: []byte(`{}`)}
	if _, err := importWorkflowLegacyRun(t.Context(), conn, input); err != nil {
		// Invalid JSON is handled before the closed connection; exercise a query path below.
	}
	valid := sqliteCoverageRun("wr")
	valid.ChildRunIDs, valid.Jobs, valid.Steps, valid.humanTasks, valid.execution = nil, nil, nil, nil, nil
	input.Data, _ = marshalPersistedRun(valid)
	if _, err := importWorkflowLegacyRun(t.Context(), conn, input); err == nil {
		t.Fatal("closed legacy run connection succeeded")
	}
	if _, err := importWorkflowLegacyMarker(t.Context(), conn, sqlitestore.LegacyInput{
		Relative: "workflow_runs/wr/" + privateRunMarkerFilename, Data: []byte(privateRunMarkerContents),
	}); err == nil {
		t.Fatal("closed private marker connection succeeded")
	}
	native := nativeStateEnvelope{Key: "key", Value: true, UpdatedAt: time.Now().UTC()}
	nativeData, _ := json.Marshal(native)
	if _, err := importWorkflowLegacyNativeState(t.Context(), conn, sqlitestore.LegacyInput{
		Relative: filepath.ToSlash(filepath.Join(workflowStateDir, "ns", safeStorageSegment("key")+".json")),
		Data:     nativeData,
	}); err == nil {
		t.Fatal("closed native-state connection succeeded")
	}
	manifestData, _ := json.Marshal(WorkflowCompatibilityManifest{
		UpdatedAt: time.Now().UTC(), Workflows: map[string]WorkflowValidationStamp{},
	})
	if _, err := importWorkflowLegacyManifest(
		t.Context(),
		conn,
		sqlitestore.LegacyInput{Data: manifestData},
	); err == nil {
		t.Fatal("closed manifest connection succeeded")
	}
	if err := insertWorkflowValidationStamp(t.Context(), conn, WorkflowValidationStamp{
		WorkflowRef: "workflows/closed.yml", ValidatedAt: time.Now().UTC(),
	}); err == nil {
		t.Fatal("closed validation stamp connection succeeded")
	}
	development := WorkflowDevelopmentSession{
		ID:        "dev_closed",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := insertWorkflowDevelopmentSession(t.Context(), conn, &development, "active", false); err == nil {
		t.Fatal("closed development connection succeeded")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

//nolint:govet // Independent storage assertions intentionally use narrow error scopes.
func TestWorkflowSQLiteDevelopmentPersistenceBoundaries(t *testing.T) {
	fixture := newWorkflowDevelopmentPublishFixture(t, "")
	active, err := GetWorkflowDevelopmentSession(fixture.workspace)
	if err != nil || active == nil {
		t.Fatalf("active development = %#v, %v", active, err)
	}
	if err := writeNewActiveDevelopment(fixture.workspace, nil); !errors.Is(err, ErrNoActiveDevelopment) {
		t.Fatalf("write nil new active = %v", err)
	}
	duplicate := *active
	duplicate.ID = "dev_second_active"
	if err := writeNewActiveDevelopment(fixture.workspace, &duplicate); !errors.Is(err, ErrActiveDevelopmentExists) {
		t.Fatalf("write second active = %v", err)
	}
	if err := writeActiveDevelopment(fixture.workspace, nil); !errors.Is(err, ErrNoActiveDevelopment) {
		t.Fatalf("write nil active = %v", err)
	}
	missing := *active
	missing.ID = "dev_missing"
	if err := writeActiveDevelopment(fixture.workspace, &missing); !errors.Is(err, ErrNoActiveDevelopment) {
		t.Fatalf("write missing active = %v", err)
	}
	if err := archiveDevelopmentSession(fixture.workspace, nil, "published"); !errors.Is(err, ErrNoActiveDevelopment) {
		t.Fatalf("archive nil active = %v", err)
	}
	if err := archiveDevelopmentSession(fixture.workspace, active, "unknown"); err == nil {
		t.Fatal("invalid archive lifecycle succeeded")
	}
	if err := archiveDevelopmentSession(
		fixture.workspace,
		&missing,
		"published",
	); !errors.Is(
		err,
		ErrNoActiveDevelopment,
	) {
		t.Fatalf("archive missing active = %v", err)
	}

	db, err := openWorkflowDatabase(t.Context(), fixture.workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, column := range []string{"validation_json", "last_test_json"} {
		if _, err := db.ExecContext(
			t.Context(),
			"UPDATE workflow_development_sessions SET "+column+"=x'7b' WHERE lifecycle='active'",
		); err != nil {
			t.Fatal(err)
		}
		if _, err := loadWorkflowDevelopmentSession(t.Context(), db, "active"); err == nil {
			t.Fatalf("invalid %s was decoded", column)
		}
		if _, err := db.ExecContext(
			t.Context(),
			"UPDATE workflow_development_sessions SET "+column+"=NULL WHERE lifecycle='active'",
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE workflow_development_sessions SET
		base_target_revision='',draft_revision='',session_revision='' WHERE lifecycle='active'`); err != nil {
		t.Fatal(err)
	}
	refreshed, err := loadWorkflowDevelopmentSession(t.Context(), db, "active")
	if err != nil || refreshed.BaseTargetRevision != WorkflowTargetRevisionUnknown ||
		refreshed.DraftRevision == "" || refreshed.SessionRevision == "" {
		t.Fatalf("refreshed development revisions = %#v, %v", refreshed, err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	invalidNested := *refreshed
	invalidNested.Validation = &WorkflowDevelopmentValidation{
		ValidatedAt: time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := updateWorkflowDevelopmentSession(t.Context(), conn, &invalidNested, "active", 1); err == nil {
		t.Fatal("invalid development validation encoded")
	}
	invalidNested = *refreshed
	invalidNested.LastTest = &WorkflowDevelopmentTest{
		TestedAt: time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := updateWorkflowDevelopmentSession(t.Context(), conn, &invalidNested, "active", 1); err == nil {
		t.Fatal("invalid development test encoded")
	}
	invalidCreated := *refreshed
	invalidCreated.CreatedAt = time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := updateWorkflowDevelopmentSession(t.Context(), conn, &invalidCreated, "active", 1); err == nil {
		t.Fatal("invalid development created timestamp accepted")
	}
	invalidUpdated := *refreshed
	invalidUpdated.UpdatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := updateWorkflowDevelopmentSession(t.Context(), conn, &invalidUpdated, "active", 1); err == nil {
		t.Fatal("invalid development updated timestamp accepted")
	}
	if err := updateWorkflowDevelopmentSession(
		t.Context(),
		conn,
		refreshed,
		"active",
		-1,
	); !errors.Is(
		err,
		ErrWorkflowSessionRevisionMismatch,
	) {
		t.Fatalf("stale direct development update = %v", err)
	}
	archived := *refreshed
	archived.ID = "dev_replace_path"
	if err := insertWorkflowDevelopmentSession(t.Context(), conn, &archived, "discarded", true); err != nil {
		t.Fatalf("replace development insert = %v", err)
	}
	invalidCreated.ID = "dev_invalid_created"
	if err := insertWorkflowDevelopmentSession(t.Context(), conn, &invalidCreated, "discarded", false); err == nil {
		t.Fatal("invalid development insert timestamp accepted")
	}
	invalidUpdated.ID = "dev_invalid_updated"
	if err := insertWorkflowDevelopmentSession(t.Context(), conn, &invalidUpdated, "discarded", false); err == nil {
		t.Fatal("invalid development update timestamp inserted")
	}
}

//nolint:govet // Independent storage assertions intentionally use narrow error scopes.
func TestWorkflowSQLiteClosedConnectionFailsRelationalBoundaries(t *testing.T) {
	raw, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateBorrowedWorkflowDatabase(t.Context(), raw); err == nil {
		t.Fatal("version-zero database validated")
	}
	if _, err := raw.ExecContext(t.Context(), `PRAGMA user_version=2`); err != nil {
		t.Fatal(err)
	}
	if err := validateBorrowedWorkflowDatabase(t.Context(), raw); !errors.Is(err, sqlitestore.ErrTooNew) {
		t.Fatalf("too-new direct validation = %v", err)
	}
	if _, err := raw.ExecContext(t.Context(), `PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	if err := validateBorrowedWorkflowDatabase(t.Context(), raw); err == nil {
		t.Fatal("schema-less version-one database validated")
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateBorrowedWorkflowDatabase(t.Context(), raw); err == nil {
		t.Fatal("closed database validated")
	}

	workspace := privateWorkflowTestWorkspace(t)
	db, err := openWorkflowDatabase(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	run := sqliteCoverageRun("wr_closed")
	run.ChildRunIDs, run.Jobs, run.Steps, run.humanTasks, run.execution = nil, nil, nil, nil, nil
	record, err := encodeWorkflowRunRecord(run)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	now := time.Now().UTC()
	task := WorkflowHumanTask{
		ID: "task", Status: HumanTaskStatusWaiting, Questions: []any{}, CreatedAt: now, UpdatedAt: now,
	}
	checks := []struct {
		name string
		call func() error
	}{
		{"insert run", func() error { return insertWorkflowRunConn(t.Context(), conn, run) }},
		{"update run", func() error { return updateWorkflowRunConn(t.Context(), conn, run, 1) }},
		{"replace children", func() error { return replaceWorkflowRunChildren(t.Context(), conn, record) }},
		{"aggregate limits", func() error { return validateWorkflowChildAggregateLimitsConn(t.Context(), conn) }},
		{"insert task", func() error { return insertWorkflowHumanTask(t.Context(), conn, run.ID, "task", task) }},
		{"load relations", func() error { return loadWorkflowRunRelations(t.Context(), conn, run) }},
		{"load executions", func() error { return loadWorkflowExecutions(t.Context(), conn, run) }},
		{"load tasks", func() error { return loadWorkflowHumanTasks(t.Context(), conn, run) }},
		{"resume concurrency", func() error { return checkWorkflowResumeConcurrencyConn(t.Context(), conn, run, 1) }},
		{"renew task", func() error {
			return renewWorkflowHumanTaskConn(t.Context(), conn, run.ID, "task", "token", time.Second)
		}},
	}
	for _, check := range checks {
		if err := check.call(); err == nil {
			t.Fatalf("%s succeeded on closed connection", check.name)
		}
	}
	if _, _, err := getWorkflowRunConn(t.Context(), conn, run.ID); err == nil {
		t.Fatal("get run succeeded on closed connection")
	}
	if _, err := listWorkflowEventsConn(t.Context(), conn, run.ID); err == nil {
		t.Fatal("list events succeeded on closed connection")
	}
	if _, _, _, err := claimWorkflowHumanTaskConn(
		t.Context(),
		conn,
		run.ID,
		"task",
		HumanTaskResumeRequest{},
	); err == nil {
		t.Fatal("claim task succeeded on closed connection")
	}
	if _, err := cancelWorkflowHumanTaskConn(t.Context(), conn, run.ID, "task", "reason"); err == nil {
		t.Fatal("cancel task succeeded on closed connection")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowSQLitePublicOperationErrorBoundaries(t *testing.T) {
	workspace := privateWorkflowTestWorkspace(t)
	store := NewFileRunStore(workspace)
	run := sqliteCoverageRun("wr_boundaries")
	run.ChildRunIDs, run.Jobs, run.Steps, run.humanTasks, run.execution = nil, nil, nil, nil, nil
	if err := store.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRun(t.Context(), run); !errors.Is(err, ErrRunAlreadyExists) {
		t.Fatalf("duplicate run error = %v", err)
	}
	if _, err := store.GetRun(t.Context(), ""); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty get error = %v", err)
	}
	if _, err := store.GetRun(t.Context(), "wr_missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing get error = %v", err)
	}
	if result, err := (&Executor{Store: store}).Retry(
		t.Context(),
		"wr_missing",
		nil,
	); result != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing retry = %#v, %v", result, err)
	}
	if err := store.AppendEvent(t.Context(), RunEvent{}); err == nil {
		t.Fatal("empty event appended")
	}
	outside := time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.AppendEvent(t.Context(), RunEvent{RunID: run.ID, Kind: "bad-time", Time: outside}); err == nil {
		t.Fatal("event with invalid timestamp appended")
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if err := store.AppendEvent(t.Context(), RunEvent{RunID: run.ID, Kind: "bad-payload", Payload: cycle}); err == nil {
		t.Fatal("event with cyclic payload appended")
	}
	if events, err := store.Events(t.Context(), "wr_missing"); err != nil || len(events) != 0 {
		t.Fatalf("missing events = %#v, %v", events, err)
	}
	if err := store.DeleteRun(t.Context(), " "); err == nil {
		t.Fatal("empty run deleted")
	}
	if _, err := store.PruneTerminalRuns(t.Context(), outside); err == nil {
		t.Fatal("prune accepted invalid timestamp")
	}
	if _, _, _, err := store.ClaimHumanTask(
		t.Context(),
		"",
		"",
		HumanTaskResumeRequest{},
	); !errors.Is(
		err,
		ErrHumanTaskNotFound,
	) {
		t.Fatalf("empty claim error = %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.RenewHumanTaskClaim(
		canceled,
		run.ID,
		"task",
		"token",
		time.Second,
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled renew error = %v", err)
	}
	if err := store.RenewHumanTaskClaim(t.Context(), "", "", "", 0); !errors.Is(err, ErrHumanTaskConflict) {
		t.Fatalf("invalid renew error = %v", err)
	}
	if _, err := store.CancelHumanTask(t.Context(), "", "", "reason"); !errors.Is(err, ErrHumanTaskNotFound) {
		t.Fatalf("empty task cancel error = %v", err)
	}
	if _, err := store.CancelHumanTask(
		t.Context(),
		run.ID,
		"task",
		strings.Repeat("x", MaxWorkflowCancelReasonBytes+1),
	); !errors.Is(
		err,
		ErrInvalidCancelReason,
	) {
		t.Fatalf("invalid task cancel reason = %v", err)
	}
	if store.Close() != nil {
		t.Fatal("compatibility Close failed")
	}
	zero := &FileRunStore{}
	if zero.workspaceDir() != "." {
		t.Fatalf("zero workspace = %q", zero.workspaceDir())
	}
	rooted := &FileRunStore{root: filepath.Join("relative", "workflow_runs")}
	if rooted.workspaceDir() != "relative" {
		t.Fatalf("root-derived workspace = %q", rooted.workspaceDir())
	}
}

func TestWorkflowSQLiteEventPromotionBoundaries(t *testing.T) {
	var inputs map[string]any
	if err := promoteWorkflowInputEvent(nil, &inputs); err != nil {
		t.Fatal(err)
	}
	if err := promoteWorkflowInputEvent([]byte(`{"other":true}`), &inputs); err != nil {
		t.Fatal(err)
	}
	if err := promoteWorkflowInputEvent([]byte(`{`), &inputs); err == nil {
		t.Fatal("malformed input envelope promoted")
	}
	if err := promoteWorkflowInputEvent([]byte(`{"event":`), &inputs); err == nil {
		t.Fatal("malformed input event promoted")
	}
	if err := promoteWorkflowInputEvent([]byte(`{"event":{"exact":9007199254740993}}`), &inputs); err != nil {
		t.Fatal(err)
	}
	if inputs["event"].(map[string]any)["exact"].(json.Number).String() != "9007199254740993" {
		t.Fatalf("promoted exact event = %#v", inputs)
	}
}

//nolint:govet // Independent storage assertions intentionally use narrow error scopes.
func TestWorkflowSQLitePrivateRunRejectsPublicContextInjection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Run)
	}{
		{"inputs", func(run *Run) { run.Inputs = map[string]any{"injected": true} }},
		{"event", func(run *Run) { run.Event = map[string]any{"injected": true} }},
		{"origin", func(run *Run) {
			run.Origin = &RunOrigin{Kind: RunOriginExternalEvent, EventID: "event", RootRunID: run.ID}
		}},
		{"session", func(run *Run) { run.Session = "injected-session" }},
		{"delivery", func(run *Run) { run.Delivery = Delivery{Channel: "injected-channel"} }},
		{"parent", func(run *Run) { run.ParentRunID = "wr_injected_parent" }},
		{"caller", func(run *Run) { run.CallerJobID = "injected-job" }},
		{"children", func(run *Run) { run.ChildRunIDs = []string{"wr_injected_child"} }},
		{"retry whitespace", func(run *Run) { run.RetryOfRunID = " " }},
		{"retry provenance", func(run *Run) { run.RetryOfRunID = "wr_injected_source" }},
		{"workflow ref", func(run *Run) { run.WorkflowRef = "inline/injected" }},
		{"run binding", func(run *Run) { run.privateRoot.RunBinding = "tampered-binding" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := privateWorkflowTestWorkspace(t)
			store := NewFileRunStore(workspace)
			compilation, err := CompileGateWorkflow("Private injection", []GateSpec{{
				ID: "approval", Kind: GateDeterministic, When: "true", Title: "Approval",
				Questions: []any{"Proceed?"},
			}}, map[string]any{"private": "injection-canary"})
			if err != nil {
				t.Fatal(err)
			}
			waiting, err := (&Executor{WorkspaceDir: workspace, Store: store}).Run(t.Context(), RunRequest{
				Workflow: compilation.Workflow, WorkflowRef: "inline/private-injection",
				PrivateRoot: compilation.PrivateRoot,
			})
			if err != nil || waiting == nil || waiting.Status != RunStatusWaiting {
				t.Fatalf("private waiting run = %#v, %v", waiting, err)
			}
			before := persistedWorkflowPrivatePayloadForTest(t, workspace, waiting.RunID)
			candidate, err := store.GetRun(t.Context(), waiting.RunID)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(candidate)
			if err := store.UpdateRun(t.Context(), candidate); !errors.Is(err, ErrPrivateWorkflowContext) {
				t.Fatalf("injected private update error = %v", err)
			}
			after := persistedWorkflowPrivatePayloadForTest(t, workspace, waiting.RunID)
			if !bytes.Equal(after, before) {
				t.Fatal("rejected private update changed relational payloads")
			}
			reloaded, err := store.GetRun(t.Context(), waiting.RunID)
			if err != nil || reloaded.Status != RunStatusWaiting ||
				validatePrivateRunInvocationEnvelope(reloaded) != nil {
				t.Fatalf("private run after rejection = %#v, %v", reloaded, err)
			}
		})
	}
}

func TestWorkflowSQLitePrivateRunRejectsStoreKeyAliases(t *testing.T) {
	workspace := privateWorkflowTestWorkspace(t)
	store := NewFileRunStore(workspace)
	compilation, err := CompileGateWorkflow("Private alias", []GateSpec{{
		ID: "policy", Kind: GateDeterministic, When: "false", Title: "Policy",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "alias-canary"})
	if err != nil {
		t.Fatal(err)
	}
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	result, err := executor.Run(t.Context(), RunRequest{
		Workflow: compilation.Workflow, WorkflowRef: "inline/private-alias",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || result == nil || result.Status != RunStatusSucceeded {
		t.Fatalf("private result = %#v, %v", result, err)
	}
	alias := strings.Replace(result.RunID, "_", "/", 1)
	if alias == result.RunID || safeID(alias) != result.RunID {
		t.Fatalf("alias %q does not map to %q", alias, result.RunID)
	}
	if canceled, err := executor.CancelRun(
		t.Context(),
		alias,
		"alias cancel",
	); canceled != nil ||
		!errors.Is(err, ErrPrivateWorkflowContext) {
		t.Fatalf("alias cancel = %#v, %v", canceled, err)
	}
	if err := store.AppendEvent(
		t.Context(),
		RunEvent{RunID: alias, Kind: "alias"},
	); !errors.Is(
		err,
		ErrPrivateWorkflowContext,
	) {
		t.Fatalf("alias append = %v", err)
	}
	if _, err := store.Events(t.Context(), alias); !errors.Is(err, ErrPrivateWorkflowContext) {
		t.Fatalf("alias events = %v", err)
	}
	if err := store.DeleteRun(t.Context(), alias); !errors.Is(err, ErrPrivateWorkflowContext) {
		t.Fatalf("alias delete = %v", err)
	}
	if persisted, err := store.GetRun(t.Context(), result.RunID); err != nil || persisted.Status != RunStatusSucceeded {
		t.Fatalf("private victim after aliases = %#v, %v", persisted, err)
	}
}

//nolint:govet // Independent storage assertions intentionally use narrow error scopes.
func TestWorkflowSQLitePrivateRunDoesNotRecreateMissingRecord(t *testing.T) {
	workspace := privateWorkflowTestWorkspace(t)
	store := NewFileRunStore(workspace)
	compilation, err := CompileGateWorkflow("Private missing", []GateSpec{{
		ID: "approval", Kind: GateDeterministic, When: "true", Title: "Approval",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "missing-canary"})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := (&Executor{WorkspaceDir: workspace, Store: store}).Run(t.Context(), RunRequest{
		Workflow: compilation.Workflow, WorkflowRef: "inline/private-missing",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || waiting == nil {
		t.Fatalf("private waiting run = %#v, %v", waiting, err)
	}
	persisted, err := store.GetRun(t.Context(), waiting.RunID)
	if err != nil {
		t.Fatal(err)
	}
	forged := cloneRun(persisted)
	forged.ID = "wr_private_forged_copy"
	if err := store.UpdateRun(t.Context(), forged); !errors.Is(err, ErrPrivateWorkflowContext) {
		t.Fatalf("forged private update = %v", err)
	}
	db, err := openWorkflowDatabase(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM workflow_runs WHERE run_id=?`, waiting.RunID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRun(t.Context(), persisted); !errors.Is(err, ErrPrivateWorkflowContext) {
		t.Fatalf("missing private update = %v", err)
	}
	if _, err := store.GetRun(t.Context(), waiting.RunID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing private run was recreated: %v", err)
	}
}

func TestWorkflowSQLitePublishSnapshotsAndDatabaseRecovery(t *testing.T) {
	if snapshot, err := workflowDatabaseSnapshot(
		map[string]any{"ignored": true},
		false,
	); err != nil ||
		snapshot.Exists {
		t.Fatalf("missing database snapshot = %#v, %v", snapshot, err)
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if _, err := workflowDatabaseSnapshot(cycle, true); err == nil {
		t.Fatal("cyclic publish snapshot encoded")
	}
	if _, err := workflowDatabaseSnapshot(strings.Repeat("x", int(maximumWorkflowManifestBytes)+1), true); err == nil {
		t.Fatal("oversized publish snapshot encoded")
	}
	if snapshot, err := workflowDatabaseSnapshot(
		map[string]any{"ok": true},
		true,
	); err != nil || !snapshot.Exists ||
		snapshot.Mode != 0o600 {
		t.Fatalf("valid database snapshot = %#v, %v", snapshot, err)
	}

	t.Run("apply revision mismatch", func(t *testing.T) {
		fixture := newWorkflowDevelopmentPublishFixture(t, "")
		wrong := *fixture.session
		wrong.SessionRevision = "sha256:wrong"
		manifest, _, err := readCompatibilityManifest(fixture.workspace)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyWorkflowDevelopmentPublishDatabase(
			t.Context(),
			fixture.workspace,
			&wrong,
			manifest,
		); !errors.Is(
			err,
			ErrWorkflowSessionRevisionMismatch,
		) {
			t.Fatalf("apply stale development = %v", err)
		}
	})

	t.Run("canceled apply", func(t *testing.T) {
		fixture := newWorkflowDevelopmentPublishFixture(t, "")
		manifest, _, err := readCompatibilityManifest(fixture.workspace)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := applyWorkflowDevelopmentPublishDatabase(
			ctx,
			fixture.workspace,
			fixture.session,
			manifest,
		); !errors.Is(
			err,
			context.Canceled,
		) {
			t.Fatalf("canceled publish database apply = %v", err)
		}
	})

	t.Run("invalid preimages", func(t *testing.T) {
		fixture := newWorkflowDevelopmentPublishFixture(t, "")
		base := workflowDevelopmentPublishJournal{SessionID: fixture.session.ID}
		pre := base
		pre.Manifest.Preimage = workflowDevelopmentPublishFileSnapshot{Exists: true, Data: []byte(`{`)}
		if err := recoverWorkflowPublishDatabasePreimage(fixture.workspace, &pre); err == nil {
			t.Fatal("invalid manifest preimage recovered")
		}
		post := base
		post.Manifest.Postimage = workflowDevelopmentPublishFileSnapshot{Exists: true, Data: []byte(`{`)}
		if err := recoverWorkflowPublishDatabasePreimage(fixture.workspace, &post); err == nil {
			t.Fatal("invalid manifest postimage recovered")
		}
		if err := recoverWorkflowPublishDatabasePreimage(fixture.workspace, &base); err == nil {
			t.Fatal("missing active preimage recovered")
		}
		invalidActive := base
		invalidActive.Active.Preimage = workflowDevelopmentPublishFileSnapshot{Exists: true, Data: []byte(`{`)}
		if err := recoverWorkflowPublishDatabasePreimage(fixture.workspace, &invalidActive); err == nil {
			t.Fatal("invalid active preimage recovered")
		}
	})

	t.Run("changed lifecycle", func(t *testing.T) {
		fixture := newWorkflowDevelopmentPublishFixture(t, "")
		active, err := workflowDatabaseSnapshot(fixture.session, true)
		if err != nil {
			t.Fatal(err)
		}
		db, err := openWorkflowDatabase(t.Context(), fixture.workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), `UPDATE workflow_development_sessions SET lifecycle='discarded'
			WHERE session_id=?`, fixture.session.ID); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		journal := workflowDevelopmentPublishJournal{
			SessionID: fixture.session.ID,
			Active:    workflowDevelopmentPublishFileTransition{Preimage: active},
		}
		if err := recoverWorkflowPublishDatabasePreimage(fixture.workspace, &journal); err == nil {
			t.Fatal("changed development lifecycle recovered")
		}
	})

	t.Run("missing row restored without manifest", func(t *testing.T) {
		fixture := newWorkflowDevelopmentPublishFixture(t, "")
		active, err := workflowDatabaseSnapshot(fixture.session, true)
		if err != nil {
			t.Fatal(err)
		}
		db, err := openWorkflowDatabase(t.Context(), fixture.workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(
			t.Context(),
			`DELETE FROM workflow_development_sessions WHERE session_id=?`,
			fixture.session.ID,
		); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		journal := workflowDevelopmentPublishJournal{
			SessionID: fixture.session.ID,
			Active:    workflowDevelopmentPublishFileTransition{Preimage: active},
		}
		if err := recoverWorkflowPublishDatabasePreimage(fixture.workspace, &journal); err != nil {
			t.Fatalf("restore missing active row = %v", err)
		}
		if restored, err := GetWorkflowDevelopmentSession(
			fixture.workspace,
		); err != nil || restored == nil ||
			restored.ID != fixture.session.ID {
			t.Fatalf("restored active row = %#v, %v", restored, err)
		}
	})

	t.Run("published row and manifest restored", func(t *testing.T) {
		fixture := newWorkflowDevelopmentPublishFixture(t, "")
		active, err := workflowDatabaseSnapshot(fixture.session, true)
		if err != nil {
			t.Fatal(err)
		}
		manifest, _, err := readCompatibilityManifest(fixture.workspace)
		if err != nil {
			t.Fatal(err)
		}
		manifestPre, err := workflowDatabaseSnapshot(manifest, true)
		if err != nil {
			t.Fatal(err)
		}
		db, err := openWorkflowDatabase(t.Context(), fixture.workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), `UPDATE workflow_development_sessions SET lifecycle='published'
			WHERE session_id=?`, fixture.session.ID); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		journal := workflowDevelopmentPublishJournal{
			SessionID: fixture.session.ID,
			Active:    workflowDevelopmentPublishFileTransition{Preimage: active},
			Manifest:  workflowDevelopmentPublishFileTransition{Preimage: manifestPre},
		}
		if err := recoverWorkflowPublishDatabasePreimage(fixture.workspace, &journal); err != nil {
			t.Fatalf("restore published database state = %v", err)
		}
	})
}

func TestWorkflowSQLitePublishTransactionEarlyFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := publishWorkflowDevelopmentTransaction(
		ctx,
		t.TempDir(),
		nil,
		RuntimeCompatibility{},
		nil,
		nil,
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled publish transaction = %v", err)
	}
	workspace := privateWorkflowTestWorkspace(t)
	if _, err := publishWorkflowDevelopmentTransaction(
		t.Context(),
		workspace,
		nil,
		RuntimeCompatibility{},
		nil,
		nil,
	); !errors.Is(
		err,
		ErrNoActiveDevelopment,
	) {
		t.Fatalf("publish without active development = %v", err)
	}
	fixture := newWorkflowDevelopmentPublishFixture(t, "")
	active, err := GetWorkflowDevelopmentSession(fixture.workspace)
	if err != nil {
		t.Fatal(err)
	}
	active.YAML = "name: [unterminated\n"
	if err := writeActiveDevelopment(fixture.workspace, active); err != nil {
		t.Fatal(err)
	}
	request := WorkflowDevelopmentPublishRequest{
		SessionID: active.ID, ExpectedSessionRevision: active.SessionRevision,
		ExpectedDraftRevision: active.DraftRevision, ExpectedBaseTargetRevision: active.BaseTargetRevision,
	}
	if _, err := publishWorkflowDevelopmentTransaction(
		t.Context(), fixture.workspace, &request, fixture.runtime, nil, nil, fixture.localOptions...,
	); !errors.Is(err, ErrWorkflowDevelopmentDraftNotReady) {
		t.Fatalf("invalid draft publish = %v", err)
	}
}

func writeWorkflowSQLiteRecoveryFixture(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowSQLiteRecoversLegacyPublishJournal(t *testing.T) {
	workspace := privateWorkflowTestWorkspace(t)
	const sessionID = "dev_legacy_publish_recovery"
	resolved, err := (Resolver{WorkspaceDir: workspace, DefinitionsDir: DefaultDefinitionsDir}).ResolveLocal(
		"workflows/legacy-publish.yml",
	)
	if err != nil {
		t.Fatal(err)
	}
	paths := struct {
		target, manifest, active, archive string
	}{
		target:   resolved.Path,
		manifest: filepath.Join(workspace, compatibilityManifestDir, compatibilityManifest),
		active:   filepath.Join(workspace, workflowDevelopmentDir, workflowDevelopmentActive),
		archive:  filepath.Join(workspace, workflowDevelopmentDir, "archive", sessionID+".json"),
	}
	for _, item := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{paths.target, []byte("new target"), 0o640},
		{paths.manifest, []byte("new manifest"), 0o600},
		{paths.active, []byte("new active"), 0o600},
		{paths.archive, []byte("new archive"), 0o600},
	} {
		writeWorkflowSQLiteRecoveryFixture(t, item.path, item.data, item.mode)
	}
	transition := func(old, current string, mode uint32) workflowDevelopmentPublishFileTransition {
		return workflowDevelopmentPublishFileTransition{
			Preimage:  workflowDevelopmentPublishFileSnapshot{Exists: true, Data: []byte(old), Mode: mode},
			Postimage: workflowDevelopmentPublishFileSnapshot{Exists: true, Data: []byte(current), Mode: mode},
		}
	}
	journal := &workflowDevelopmentPublishJournal{
		Version:        workflowDevelopmentPublishLegacyJournalVersion,
		Phase:          workflowDevelopmentPublishPhasePrepared,
		Stage:          workflowDevelopmentPublishStageActiveRemoveStarted,
		DefinitionsDir: filepath.ToSlash(DefaultDefinitionsDir),
		TargetRef:      "workflows/legacy-publish.yml", SessionID: sessionID,
		Target:   transition("old target", "new target", 0o640),
		Manifest: transition("old manifest", "new manifest", 0o600),
		Active:   transition("old active", "new active", 0o600),
		Archive:  transition("old archive", "new archive", 0o600),
	}
	if err := writeWorkflowDevelopmentPublishJournal(workspace, journal); err != nil {
		t.Fatal(err)
	}
	if err := recoverWorkflowDevelopmentPublishTransaction(workspace); err != nil {
		t.Fatalf("recover legacy publish journal = %v", err)
	}
	for _, item := range []struct {
		path string
		want string
	}{
		{paths.target, "old target"},
		{paths.manifest, "old manifest"},
		{paths.active, "old active"},
		{paths.archive, "old archive"},
	} {
		data, err := os.ReadFile(item.path)
		if err != nil || string(data) != item.want {
			t.Fatalf("recovered %s = %q, %v", item.path, data, err)
		}
	}
	if _, err := os.Stat(workflowDevelopmentPublishJournalPath(workspace)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy publish journal remains: %v", err)
	}
}

func TestWorkflowSQLiteRecoversLegacyTemplateJournal(t *testing.T) {
	workspace := privateWorkflowTestWorkspace(t)
	template := builtInWorkflowTemplateRegistry[0]
	resolved, err := (Resolver{WorkspaceDir: workspace, DefinitionsDir: DefaultDefinitionsDir}).ResolveLocal(
		template.ref,
	)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(workspace, compatibilityManifestDir, compatibilityManifest)
	writeWorkflowSQLiteRecoveryFixture(t, resolved.Path, []byte("new template"), 0o640)
	writeWorkflowSQLiteRecoveryFixture(t, manifestPath, []byte("new manifest"), 0o600)
	transition := func(old, current string, mode uint32) workflowTemplateInstallFileTransition {
		return workflowTemplateInstallFileTransition{
			Preimage:  workflowTemplateInstallFileSnapshot{Exists: true, Data: []byte(old), Mode: mode},
			Postimage: workflowTemplateInstallFileSnapshot{Exists: true, Data: []byte(current), Mode: mode},
		}
	}
	journal := &workflowTemplateInstallJournal{
		Version:        workflowTemplateInstallLegacyJournalVersion,
		Phase:          workflowTemplateInstallPhasePrepared,
		Stage:          workflowTemplateInstallStageManifestWriteStarted,
		DefinitionsDir: filepath.ToSlash(DefaultDefinitionsDir),
		TemplateName:   template.name, TargetRef: template.ref,
		Target:   transition("old template", "new template", 0o640),
		Manifest: transition("old manifest", "new manifest", 0o600),
	}
	if err := writeWorkflowTemplateInstallJournal(workspace, journal); err != nil {
		t.Fatal(err)
	}
	if err := recoverWorkflowTemplateInstallTransaction(workspace); err != nil {
		t.Fatalf("recover legacy template journal = %v", err)
	}
	for _, item := range []struct {
		path string
		want string
	}{{resolved.Path, "old template"}, {manifestPath, "old manifest"}} {
		data, err := os.ReadFile(item.path)
		if err != nil || string(data) != item.want {
			t.Fatalf("recovered %s = %q, %v", item.path, data, err)
		}
	}
	if _, err := os.Stat(workflowTemplateInstallJournalPath(workspace)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy template journal remains: %v", err)
	}
}

type workflowSQLitePrivateOrderingStore struct {
	RunStore
	order  *[]string
	agents *privateGateSecurityAgentRunner
}

func (s *workflowSQLitePrivateOrderingStore) CreateRun(ctx context.Context, run *Run) error {
	if s.agents == nil || s.agents.captureCount != 1 {
		return errors.New("durable create occurred before the frozen session capture")
	}
	*s.order = append(*s.order, "create")
	return s.RunStore.CreateRun(ctx, run)
}

func assertWorkflowSQLitePrivateAgentRequests(
	t *testing.T,
	requests []AgentRequest,
	wantKey, wantSummary string,
) {
	t.Helper()
	if len(requests) == 0 {
		t.Fatal("private agent requests are empty")
	}
	for index, request := range requests {
		if !request.PrivateContext || request.Session != "" || request.FrozenReadOnlySession == nil {
			t.Fatalf("private agent request %d = %#v", index, request)
		}
		snapshot := request.FrozenReadOnlySession.Snapshot
		if snapshot.Key != wantKey || snapshot.Summary != wantSummary {
			t.Fatalf("private agent request %d snapshot = %#v", index, snapshot)
		}
	}
}

//nolint:govet // Independent storage assertions intentionally use narrow error scopes.
func TestWorkflowSQLitePrivateWorkingGateFreezesBeforeCreateAndRetry(t *testing.T) {
	workspace := privateWorkflowTestWorkspace(t)
	const (
		subjectCanary       = "sqlite-private-working-subject"
		liveReferenceCanary = "sqlite-private-live-session"
		canonicalCanary     = "agent:main:web:sqlite-private-canonical"
		newLiveCanary       = "sqlite-private-changed-live-session"
		summaryCanary       = "sqlite-private-frozen-summary"
		outputCanary        = "sqlite-private-agent-output"
	)
	order := []string{}
	agents := &privateGateSecurityAgentRunner{
		order: &order, captureKey: canonicalCanary, captureSummary: summaryCanary,
		historyRevision: "history-revision-sqlite-frozen-1",
		outputs: map[string]any{
			"structured":   map[string]any{"ask_user": false, "reason": "", "questions": []any{}},
			"private_echo": outputCanary, "cache_key": liveReferenceCanary,
		},
	}
	baseStore := NewFileRunStore(workspace)
	store := &workflowSQLitePrivateOrderingStore{RunStore: baseStore, order: &order, agents: agents}
	compilation, err := CompileGateWorkflow("SQLite private working gate", []GateSpec{{
		ID: "discussion", Kind: GateAIWorkingContext, AgentID: "main",
		Criteria: "Ask only for an unresolved product choice.", Title: "PR discussion",
	}}, map[string]any{"finding": subjectCanary})
	if err != nil {
		t.Fatal(err)
	}
	compilation.PrivateRoot.ReadOnlySession = &ReadOnlySessionRef{
		AgentID: "main", Session: liveReferenceCanary,
	}
	executor := &Executor{WorkspaceDir: workspace, Store: store, Agents: agents}
	first, err := executor.Run(t.Context(), RunRequest{
		Workflow: compilation.Workflow, WorkflowRef: "inline/sqlite-private-working",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || first == nil || first.Status != RunStatusSucceeded {
		t.Fatalf("initial private working gate = %#v, %v", first, err)
	}
	if strings.Join(order, ",") != "capture,create,agent" || agents.captureCount != 1 ||
		len(agents.captureRefs) != 1 || agents.captureRefs[0].Session != liveReferenceCanary {
		t.Fatalf(
			"initial private operation order=%#v captures=%d refs=%#v",
			order,
			agents.captureCount,
			agents.captureRefs,
		)
	}
	assertPrivateGateTestOmits(t, "initial SQLite private result", first,
		subjectCanary, liveReferenceCanary, canonicalCanary, summaryCanary, outputCanary)
	assertWorkflowSQLitePrivateAgentRequests(t, agents.requests, canonicalCanary, summaryCanary)
	privateBytes := persistedWorkflowPrivatePayloadForTest(t, workspace, first.RunID)
	if bytes.Contains(privateBytes, []byte(liveReferenceCanary)) ||
		!bytes.Contains(privateBytes, []byte(canonicalCanary)) ||
		!bytes.Contains(privateBytes, []byte(summaryCanary)) {
		t.Fatalf("frozen SQLite continuation = %s", privateBytes)
	}

	agents.captureKey = newLiveCanary
	agents.captureSummary = "changed-live-summary-must-not-be-read"
	retry, err := executor.Retry(t.Context(), first.RunID, nil)
	if err != nil || retry == nil || retry.Status != RunStatusSucceeded {
		t.Fatalf("private SQLite retry = %#v, %v", retry, err)
	}
	if agents.captureCount != 1 || strings.Join(order, ",") != "capture,create,agent,create,agent" {
		t.Fatalf("retry operation order=%#v captures=%d", order, agents.captureCount)
	}
	assertWorkflowSQLitePrivateAgentRequests(t, agents.requests, canonicalCanary, summaryCanary)
	assertPrivateGateTestOmits(t, "retry SQLite private result", retry,
		subjectCanary, liveReferenceCanary, canonicalCanary, newLiveCanary, summaryCanary, outputCanary)
	for _, runID := range []string{first.RunID, retry.RunID} {
		persisted, err := store.GetRun(t.Context(), runID)
		if err != nil || persisted.privateRoot == nil || persisted.privateRoot.ReadOnlySession == nil {
			t.Fatalf("persisted private run %q = %#v, %v", runID, persisted, err)
		}
		frozen := persisted.privateRoot.ReadOnlySession
		if frozen.Snapshot.Key != canonicalCanary || frozen.Snapshot.Summary != summaryCanary {
			t.Fatalf("persisted private snapshot %q = %#v", runID, frozen.Snapshot)
		}
		decision := persisted.Steps["gates/gate_discussion_decision"]
		if _, exists := decision.Outputs["cache_key"]; exists ||
			decision.Outputs["session"] != AgentSessionPrivate ||
			decision.Outputs["session_mode"] != AgentSessionPrivate {
			t.Fatalf("private decision output %q = %#v", runID, decision.Outputs)
		}
		assertPrivateGateTestOmits(t, "private browser projection "+runID,
			ProjectWorkflowRunForBrowser(persisted, false),
			subjectCanary, liveReferenceCanary, canonicalCanary, newLiveCanary, summaryCanary, outputCanary)
		events, err := store.Events(t.Context(), runID)
		if err != nil {
			t.Fatal(err)
		}
		assertPrivateGateTestEventsRedacted(t, events)
		assertPrivateGateTestOmits(t, "private events "+runID, events,
			subjectCanary, liveReferenceCanary, canonicalCanary, newLiveCanary, summaryCanary, outputCanary)
	}

	db, releaseDatabase, err := borrowWorkflowDatabase(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseDatabase()
	var privateJSON []byte
	if err := db.QueryRowContext(t.Context(), `SELECT private_context_json FROM workflow_run_payloads
		WHERE run_id=?`, first.RunID).Scan(&privateJSON); err != nil {
		t.Fatal(err)
	}
	needle := []byte(`"read_only_session":{`)
	if !bytes.Contains(privateJSON, needle) {
		t.Fatalf("private continuation has no frozen session: %s", privateJSON)
	}
	malformed := bytes.Replace(privateJSON, needle,
		[]byte(`"read_only_session":{"unknown_sqlite_private_field":true,`), 1)
	if _, err := db.ExecContext(t.Context(), `UPDATE workflow_run_payloads SET private_context_json=?
		WHERE run_id=?`, malformed, first.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetRun(t.Context(), first.RunID); !errors.Is(err, ErrPrivateWorkflowContext) {
		t.Fatalf("malformed private SQLite continuation = %v", err)
	}
}

//nolint:govet // Independent storage assertions intentionally use narrow error scopes.
func TestWorkflowSQLitePropagatesDriverFailuresAtRelationalBoundaries(t *testing.T) {
	registerWorkflowSQLiteFaultDriver.Do(func() {
		sql.Register(workflowSQLiteFaultDriverName, workflowSQLiteFaultDriver{})
	})
	db, err := sql.Open(workflowSQLiteFaultDriverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := validateBorrowedWorkflowDatabase(t.Context(), db); err == nil {
		t.Fatal("faulting database validated")
	}
	run := sqliteCoverageRun("wr_fault_driver")
	run.execution = nil
	record, err := encodeWorkflowRunRecord(run)
	if err != nil {
		t.Fatal(err)
	}
	session := &WorkflowDevelopmentSession{
		ID: "dev_fault_driver", Status: WorkflowDevelopmentStatusEditing,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	task := run.humanTasks["job/task"]
	externalEvent := map[string]any{
		"id": "ev_" + strings.Repeat("a", 32), "source": "test", "connector": "test", "type": "test",
	}
	ancestryRun := &Run{ID: "wr_ancestry", ParentRunID: "wr_parent", Event: externalEvent}
	checks := []struct {
		name string
		call func() error
	}{
		{
			"load development",
			func() error { _, err := loadWorkflowDevelopmentSession(t.Context(), conn, "active"); return err },
		},
		{
			"update development",
			func() error { return updateWorkflowDevelopmentSession(t.Context(), conn, session, "active", 1) },
		},
		{
			"insert development",
			func() error { return insertWorkflowDevelopmentSession(t.Context(), conn, session, "active", false) },
		},
		{"insert run", func() error { return insertWorkflowRunConn(t.Context(), conn, run) }},
		{"update run", func() error { return updateWorkflowRunConn(t.Context(), conn, run, 1) }},
		{"replace run relations", func() error { return replaceWorkflowRunChildren(t.Context(), conn, record) }},
		{
			"validate relation limits",
			func() error { return validateWorkflowChildAggregateLimitsConn(t.Context(), conn) },
		},
		{"insert human task", func() error { return insertWorkflowHumanTask(t.Context(), conn, run.ID, "task", task) }},
		{"get run", func() error { _, _, err := getWorkflowRunConn(t.Context(), conn, run.ID); return err }},
		{
			"exact event ancestry",
			func() error { _, err := workflowRunUsesExactEventNumbers(t.Context(), conn, ancestryRun); return err },
		},
		{"load relations", func() error { return loadWorkflowRunRelations(t.Context(), conn, run) }},
		{"load executions", func() error { return loadWorkflowExecutions(t.Context(), conn, run) }},
		{"load human tasks", func() error { return loadWorkflowHumanTasks(t.Context(), conn, run) }},
		{"append event", func() error { return appendWorkflowEventConn(t.Context(), conn, RunEvent{RunID: run.ID}) }},
		{"list events", func() error { _, err := listWorkflowEventsConn(t.Context(), conn, run.ID); return err }},
		{"resume concurrency", func() error { return checkWorkflowResumeConcurrencyConn(t.Context(), conn, run, 1) }},
		{"claim human task", func() error {
			_, _, _, err := claimWorkflowHumanTaskConn(t.Context(), conn, run.ID, task.ID, HumanTaskResumeRequest{})
			return err
		}},
		{"renew human task", func() error {
			return renewWorkflowHumanTaskConn(t.Context(), conn, run.ID, task.ID, "token", time.Second)
		}},
		{"cancel human task", func() error {
			_, err := cancelWorkflowHumanTaskConn(t.Context(), conn, run.ID, task.ID, "reason")
			return err
		}},
	}
	for _, check := range checks {
		if err := check.call(); err == nil {
			t.Fatalf("%s swallowed injected driver failure", check.name)
		}
	}
}

func TestWorkflowSQLiteMissingTablesFailClosedAcrossStores(t *testing.T) {
	tables := []struct {
		name       string
		operations func(*testing.T, string, *FileRunStore, *Run)
	}{
		{"workflow_runs", func(t *testing.T, _ string, store *FileRunStore, run *Run) {
			candidate := sqliteCoverageRun("wr_after_runs_drop")
			candidate.execution = nil
			if err := store.CreateRun(t.Context(), candidate); !errors.Is(err, ErrWorkflowStorageUnavailable) {
				t.Fatalf("create after runs drop = %v", err)
			}
			if _, err := store.GetRun(t.Context(), run.ID); !errors.Is(err, ErrWorkflowStorageUnavailable) {
				t.Fatalf("get after runs drop = %v", err)
			}
		}},
		{"workflow_run_payloads", func(t *testing.T, _ string, store *FileRunStore, run *Run) {
			if _, err := store.GetRun(t.Context(), run.ID); !errors.Is(err, ErrWorkflowStorageUnavailable) {
				t.Fatalf("get after payload drop = %v", err)
			}
			if err := store.UpdateRun(t.Context(), run); !errors.Is(err, ErrPrivateWorkflowContext) {
				t.Fatalf("update after payload drop = %v", err)
			}
		}},
		{"workflow_run_children", func(t *testing.T, _ string, store *FileRunStore, run *Run) {
			if _, err := store.GetRun(t.Context(), run.ID); !errors.Is(err, ErrWorkflowStorageUnavailable) {
				t.Fatalf("get after children drop = %v", err)
			}
		}},
		{"workflow_run_jobs", func(t *testing.T, _ string, store *FileRunStore, run *Run) {
			if _, err := store.GetRun(t.Context(), run.ID); !errors.Is(err, ErrWorkflowStorageUnavailable) {
				t.Fatalf("get after jobs drop = %v", err)
			}
		}},
		{"workflow_run_steps", func(t *testing.T, _ string, store *FileRunStore, run *Run) {
			if _, err := store.GetRun(t.Context(), run.ID); !errors.Is(err, ErrWorkflowStorageUnavailable) {
				t.Fatalf("get after steps drop = %v", err)
			}
		}},
		{"workflow_human_tasks", func(t *testing.T, _ string, store *FileRunStore, run *Run) {
			if _, err := store.GetRun(t.Context(), run.ID); !errors.Is(err, ErrWorkflowStorageUnavailable) {
				t.Fatalf("get after tasks drop = %v", err)
			}
		}},
		{"workflow_private_run_markers", func(t *testing.T, _ string, store *FileRunStore, run *Run) {
			if _, err := store.GetRun(t.Context(), run.ID); !errors.Is(err, ErrWorkflowStorageUnavailable) {
				t.Fatalf("get after markers drop = %v", err)
			}
		}},
		{"workflow_run_events", func(t *testing.T, _ string, store *FileRunStore, run *Run) {
			if err := store.AppendEvent(
				t.Context(),
				RunEvent{RunID: run.ID, Kind: "after-drop"},
			); !errors.Is(
				err,
				ErrWorkflowStorageUnavailable,
			) {
				t.Fatalf("append after events drop = %v", err)
			}
			if _, err := store.Events(t.Context(), run.ID); !errors.Is(err, ErrWorkflowStorageUnavailable) {
				t.Fatalf("list after events drop = %v", err)
			}
		}},
		{"workflow_development_sessions", func(t *testing.T, workspace string, _ *FileRunStore, _ *Run) {
			if _, err := GetWorkflowDevelopmentSession(workspace); err == nil {
				t.Fatal("development read succeeded after table drop")
			}
			session := &WorkflowDevelopmentSession{
				ID: "dev_after_drop", Status: WorkflowDevelopmentStatusEditing,
				CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}
			if err := writeNewActiveDevelopment(workspace, session); err == nil {
				t.Fatal("development create succeeded after table drop")
			}
			if err := writeActiveDevelopment(workspace, session); err == nil {
				t.Fatal("development update succeeded after table drop")
			}
			if err := archiveDevelopmentSession(workspace, session, "discarded"); err == nil {
				t.Fatal("development archive succeeded after table drop")
			}
		}},
	}
	for _, test := range tables {
		t.Run(test.name, func(t *testing.T) {
			workspace := privateWorkflowTestWorkspace(t)
			store := NewFileRunStore(workspace)
			run := sqliteCoverageRun("wr_table_drop")
			run.execution = nil
			if err := store.CreateRun(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			db, err := store.borrowDatabase(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer store.releaseDatabase()
			conn, err := db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.ExecContext(t.Context(), `PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ExecContext(t.Context(), "DROP TABLE "+test.name); err != nil {
				t.Fatal(err)
			}
			test.operations(t, workspace, store, run)
		})
	}
}

func TestWorkflowSQLiteExactEventAncestryFailureModes(t *testing.T) {
	workspace := privateWorkflowTestWorkspace(t)
	store := NewFileRunStore(workspace)
	eventID := "ev_" + strings.Repeat("b", 32)
	event := map[string]any{"id": eventID, "source": "test", "connector": "test", "type": "created"}
	now := time.Now().UTC()
	parent := &Run{
		ID: "wr_event_parent", WorkflowRef: "workflows/event.yml", Status: RunStatusRunning,
		Event: event, Inputs: map[string]any{}, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateRun(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	child := &Run{
		ID: "wr_event_child", WorkflowRef: "workflows/event.yml", Status: RunStatusRunning,
		ParentRunID: parent.ID, Event: event, Inputs: map[string]any{}, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateRun(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	db, err := openWorkflowDatabase(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if exact, err := workflowRunUsesExactEventNumbers(t.Context(), conn, nil); err != nil || exact {
		t.Fatalf("nil exact-event run = %v, %v", exact, err)
	}
	missingParent := cloneRun(child)
	missingParent.ParentRunID = "wr_missing_parent"
	if exact, err := workflowRunUsesExactEventNumbers(t.Context(), conn, missingParent); err != nil || exact {
		t.Fatalf("missing parent exact-event run = %v, %v", exact, err)
	}
	cycle := cloneRun(child)
	cycle.ParentRunID = cycle.ID
	if exact, err := workflowRunUsesExactEventNumbers(t.Context(), conn, cycle); err != nil || exact {
		t.Fatalf("cyclic exact-event run = %v, %v", exact, err)
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE workflow_run_payloads SET event_json=x'7b'
		WHERE run_id=?`, parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := workflowRunUsesExactEventNumbers(t.Context(), conn, child); err == nil {
		t.Fatal("malformed parent event was trusted")
	}
	canonicalEvent, _ := encodeWorkflowJSON(event, maximumWorkflowRunPayloadBytes)
	if _, err := conn.ExecContext(t.Context(), `UPDATE workflow_run_payloads SET event_json=?,inputs_json=x'7b'
		WHERE run_id=?`, canonicalEvent, parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := workflowRunUsesExactEventNumbers(t.Context(), conn, child); err == nil {
		t.Fatal("malformed parent inputs were trusted")
	}
}

func TestWorkflowSQLiteNumberCodecsAndEditorOperationMarkers(t *testing.T) {
	operations := []WorkflowJobsOperation{
		WorkflowJobInsertOperation{},
		WorkflowJobDeleteOperation{},
		WorkflowJobPatchOperation{},
		WorkflowStepInsertOperation{},
		WorkflowStepDeleteOperation{},
		WorkflowStepMoveOperation{},
		WorkflowStepPatchOperation{},
	}
	for _, operation := range operations {
		operation.workflowJobsOperation()
	}
	var strict struct {
		Known json.Number `json:"known"`
	}
	if err := decodeStrictJSONWithNumbers([]byte(`{"known":9007199254740993}`), &strict); err != nil ||
		strict.Known.String() != "9007199254740993" {
		t.Fatalf("strict number decode = %#v, %v", strict, err)
	}
	if err := decodeStrictJSONWithNumbers([]byte(`{"known":1,"unknown":true}`), &strict); err == nil {
		t.Fatal("strict number decoder accepted unknown field")
	}
	if err := decodeStrictJSONWithNumbers([]byte(`{"known":1} {}`), &strict); err == nil {
		t.Fatal("strict number decoder accepted trailing JSON")
	}
	if retained, err := normalizeRunOverflowNumbers(nil); err != nil || retained {
		t.Fatalf("nil overflow normalization = %v, %v", retained, err)
	}
	run := &Run{
		Event:   map[string]any{"huge": json.Number("1e400")},
		Inputs:  map[string]any{"ordinary": json.Number("1.5")},
		Outputs: map[string]any{"integer": json.Number("2")},
		Jobs:    map[string]JobExecution{"job": {Outputs: map[string]any{"huge": json.Number("1e400")}}},
		Steps:   map[string]StepExecution{"step": {Outputs: map[string]any{"huge": json.Number("1e400")}}},
	}
	if retained, err := normalizeRunOverflowNumbers(run); err != nil || !retained {
		t.Fatalf("overflow normalization = %#v, %v, %v", run, retained, err)
	}
	if got := fmt.Sprint(strict.Known); got == "" {
		t.Fatal("strict number string is empty")
	}
}

func TestWorkflowSQLiteContinuationRejectsInvalidJobAndStepStates(t *testing.T) {
	newRun := func() *Run {
		return &Run{
			ID: "wr_direct_continuation", WorkflowRef: "workflows/direct.yml", Status: RunStatusRunning,
			Inputs: map[string]any{}, Event: map[string]any{}, Jobs: map[string]JobExecution{},
			Steps: map[string]StepExecution{}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
	}
	execCtx := func() ExecutionContext {
		return ExecutionContext{
			Inputs: map[string]any{}, Event: map[string]any{}, Steps: map[string]StepExecution{},
			Needs: map[string]JobExecution{},
		}
	}
	executor := &Executor{}
	t.Run("step cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		step, err := executor.executeStep(ctx, nil, newRun(), "job", 0, Step{}, execCtx(), nil)
		if !errors.Is(err, ErrRunCanceled) || step.Status != RunStatusCanceled {
			t.Fatalf("canceled step = %#v, %v", step, err)
		}
	})
	t.Run("step invalid if", func(t *testing.T) {
		step, err := executor.executeStep(t.Context(), nil, newRun(), "job", 0,
			Step{If: "${{ nope.value }}"}, execCtx(), nil)
		if err == nil || step.Status != RunStatusFailed {
			t.Fatalf("invalid-if step = %#v, %v", step, err)
		}
	})
	t.Run("step false if", func(t *testing.T) {
		step, err := executor.executeStep(t.Context(), nil, newRun(), "job", 0,
			Step{If: "${{ false }}"}, execCtx(), nil)
		if err != nil || step.Status != RunStatusSkipped {
			t.Fatalf("false-if step = %#v, %v", step, err)
		}
	})
	t.Run("human step secret reference", func(t *testing.T) {
		step, err := executor.executeStep(t.Context(), nil, newRun(), "job", 0,
			Step{Uses: "human/task", With: map[string]any{"title": "${{ secrets.token }}"}}, execCtx(), nil)
		if err == nil || step.Status != RunStatusFailed {
			t.Fatalf("secret human step = %#v, %v", step, err)
		}
	})
	t.Run("step render failure", func(t *testing.T) {
		step, err := executor.executeStep(t.Context(), nil, newRun(), "job", 0,
			Step{With: map[string]any{"value": "${{ nope.value }}"}}, execCtx(), nil)
		if err == nil || step.Status != RunStatusFailed {
			t.Fatalf("render-failure step = %#v, %v", step, err)
		}
	})
	humanStep := Step{ID: "review", Uses: "human/task", With: map[string]any{
		"title": "Review", "questions": []any{map[string]any{"id": "approve"}},
	}}
	t.Run("human step lacks continuation", func(t *testing.T) {
		step, err := executor.executeStep(t.Context(), nil, newRun(), "job", 0, humanStep, execCtx(), nil)
		if !errors.Is(err, ErrHumanTaskUnsupported) || step.Status != RunStatusFailed {
			t.Fatalf("unsupported human step = %#v, %v", step, err)
		}
	})
	t.Run("duplicate human task", func(t *testing.T) {
		run := newRun()
		run.execution = &workflowExecutionState{}
		first, err := executor.executeStep(t.Context(), nil, run, "job", 0, humanStep, execCtx(), nil)
		if _, waiting := err.(workflowWaitingError); !waiting || first.Status != RunStatusWaiting {
			t.Fatalf("first human task = %#v, %v", first, err)
		}
		duplicate, err := executor.executeStep(t.Context(), nil, run, "job", 0, humanStep, execCtx(), nil)
		if !errors.Is(err, ErrHumanTaskConflict) || duplicate.Status != RunStatusFailed {
			t.Fatalf("duplicate human task = %#v, %v", duplicate, err)
		}
	})
	t.Run("step continuation conflict cause", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		cancel(ErrHumanTaskConflict)
		step, err := executor.executeStep(ctx, nil, newRun(), "job", 0, Step{}, execCtx(), nil)
		if !errors.Is(err, ErrHumanTaskConflict) || step.Status != RunStatusCanceled {
			t.Fatalf("conflicted step = %#v, %v", step, err)
		}
	})
	t.Run("step observer errors", func(t *testing.T) {
		injected := errors.New("injected step observer failure")
		observed := &Executor{StepActivityObserver: func(StepActivityEvent) error { return injected }}
		for _, continueOnError := range []bool{false, true} {
			step, err := observed.executeStep(t.Context(), nil, newRun(), "job", 0,
				Step{ContinueOnError: continueOnError}, execCtx(), nil)
			if !errors.Is(err, injected) {
				t.Fatalf("observer error continue=%v = %#v, %v", continueOnError, step, err)
			}
			if continueOnError && step.Status != RunStatusSucceeded ||
				!continueOnError && step.Status != RunStatusFailed {
				t.Fatalf("observer status continue=%v = %#v", continueOnError, step)
			}
		}
	})

	t.Run("resumed job missing checkpoint", func(t *testing.T) {
		job, err := executor.executeJob(t.Context(), nil, newRun(), "job", Job{}, RunRequest{},
			execCtx(), map[string]JobExecution{}, 0, true)
		if err == nil || job.Status != RunStatusFailed {
			t.Fatalf("missing resumed job = %#v, %v", job, err)
		}
	})
	t.Run("job cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		job, err := executor.executeJob(ctx, nil, newRun(), "job", Job{}, RunRequest{},
			execCtx(), map[string]JobExecution{}, 0, false)
		if !errors.Is(err, ErrRunCanceled) || job.Status != RunStatusCanceled {
			t.Fatalf("canceled job = %#v, %v", job, err)
		}
	})
	t.Run("job dependency failure", func(t *testing.T) {
		job, err := executor.executeJob(t.Context(), nil, newRun(), "job",
			Job{Needs: StringList{"dep"}}, RunRequest{}, execCtx(),
			map[string]JobExecution{"dep": {ID: "dep", Status: RunStatusFailed}}, 0, false)
		if err == nil || job.Status != RunStatusSkipped {
			t.Fatalf("dependency-skipped job = %#v, %v", job, err)
		}
	})
	t.Run("resumed job dependency failure", func(t *testing.T) {
		run := newRun()
		run.Jobs["job"] = JobExecution{ID: "job", Status: RunStatusWaiting}
		job, err := executor.executeJob(t.Context(), nil, run, "job",
			Job{Needs: StringList{"dep"}}, RunRequest{}, execCtx(),
			map[string]JobExecution{"dep": {ID: "dep", Status: RunStatusFailed}}, 0, true)
		if err == nil || job.Status != RunStatusFailed {
			t.Fatalf("resumed dependency failure = %#v, %v", job, err)
		}
	})
	t.Run("job invalid and false if", func(t *testing.T) {
		invalid, err := executor.executeJob(t.Context(), nil, newRun(), "job",
			Job{If: "${{ nope.value }}"}, RunRequest{}, execCtx(), map[string]JobExecution{}, 0, false)
		if err == nil || invalid.Status != RunStatusFailed {
			t.Fatalf("invalid-if job = %#v, %v", invalid, err)
		}
		skipped, err := executor.executeJob(t.Context(), nil, newRun(), "job",
			Job{If: "${{ false }}"}, RunRequest{}, execCtx(), map[string]JobExecution{}, 0, false)
		if err != nil || skipped.Status != RunStatusSkipped {
			t.Fatalf("false-if job = %#v, %v", skipped, err)
		}
	})
	t.Run("job output render failure", func(t *testing.T) {
		job, err := executor.executeJob(t.Context(), nil, newRun(), "job",
			Job{Outputs: map[string]string{"value": "${{ nope.value }}"}}, RunRequest{},
			execCtx(), map[string]JobExecution{}, 0, false)
		if err == nil || job.Status != RunStatusFailed {
			t.Fatalf("output-render job = %#v, %v", job, err)
		}
	})
}

func TestWorkflowSQLiteCancellationChecksPersistedParentState(t *testing.T) {
	if err := checkRunCanceled(t.Context(), nil, nil); err != nil {
		t.Fatal(err)
	}
	conflictCtx, conflictCancel := context.WithCancelCause(t.Context())
	conflictCancel(ErrHumanTaskConflict)
	if err := checkRunCanceled(conflictCtx, nil, &Run{}); !errors.Is(err, ErrHumanTaskConflict) {
		t.Fatalf("conflict cancellation check = %v", err)
	}
	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := checkRunCanceled(canceledCtx, nil, &Run{}); !errors.Is(err, ErrRunCanceled) {
		t.Fatalf("context cancellation check = %v", err)
	}
	deadlineCtx, deadlineCancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer deadlineCancel()
	if err := checkRunCanceled(deadlineCtx, nil, &Run{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline cancellation check = %v", err)
	}

	workspace := privateWorkflowTestWorkspace(t)
	store := NewFileRunStore(workspace)
	now := time.Now().UTC()
	parent := &Run{
		ID: "wr_canceled_parent", WorkflowRef: "workflows/cancel.yml", Status: RunStatusCanceled,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateRun(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	child := &Run{
		ID: "wr_parent_canceled_child", WorkflowRef: "workflows/cancel.yml", Status: RunStatusRunning,
		ParentRunID: parent.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateRun(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	projection := cloneRun(child)
	if err := checkRunCanceled(t.Context(), store, projection); !errors.Is(err, ErrRunCanceled) ||
		projection.Status != RunStatusCanceled || projection.CancelReason != "parent run canceled" {
		t.Fatalf("parent cancellation projection = %#v, %v", projection, err)
	}
	direct := &Run{
		ID: "wr_direct_canceled", WorkflowRef: "workflows/cancel.yml", Status: RunStatusCanceled,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateRun(t.Context(), direct); err != nil {
		t.Fatal(err)
	}
	directProjection := cloneRun(direct)
	if err := checkRunCanceled(t.Context(), store, directProjection); !errors.Is(err, ErrRunCanceled) ||
		directProjection.CancelReason != "cancel requested" {
		t.Fatalf("direct cancellation projection = %#v, %v", directProjection, err)
	}
	if err := checkRunCanceled(t.Context(), store, &Run{ID: "wr_missing"}); err != nil {
		t.Fatalf("missing persisted run cancellation = %v", err)
	}
}
