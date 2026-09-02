package workflows

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/sqlitestore"
)

// SQLiteRunStore is the durable workflow store. FileRunStore remains an alias
// for one compatibility cycle so callers compiled against the old constructor
// move to SQLite without a second persistence path.
type SQLiteRunStore = FileRunStore

var allowUnfencedWorkflowProviderForTests atomic.Bool

// NewSQLiteRunStore validates and opens the workspace database eagerly.
func NewSQLiteRunStore(workspace string) (*SQLiteRunStore, error) {
	store := NewFileRunStore(workspace)
	if store.usesWorkflowBroker() {
		if _, err := store.workflowBrokerClient(); err != nil {
			return nil, err
		}
		return store, nil
	}
	ctx := context.Background()
	db, err := store.borrowDatabase(ctx)
	if err != nil {
		return nil, err
	}
	defer store.releaseDatabase()
	if err := validateBorrowedWorkflowDatabase(ctx, db); err != nil {
		return nil, workflowDatabaseError("validate", err)
	}
	return store, nil
}

func validateBorrowedWorkflowDatabase(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	version, err := sqliteprovider.SchemaVersion(ctx, conn)
	if err != nil {
		return err
	}
	if version > 1 {
		return fmt.Errorf("%w: workflow database version %d", sqlitestore.ErrTooNew, version)
	}
	if version < 1 {
		return fmt.Errorf("%w: workflow database version %d", sqlitestore.ErrInvalidSchema, version)
	}
	if err := validateWorkflowSchema(ctx, conn); err != nil {
		return err
	}
	if err := sqliteprovider.CheckIntegrity(ctx, conn); err != nil {
		return fmt.Errorf("workflow database integrity check failed: %w", err)
	}
	return nil
}

func (s *FileRunStore) workspaceDir() string {
	if strings.TrimSpace(s.workspace) != "" {
		return s.workspace
	}
	root := strings.TrimSpace(s.root)
	if root == "" {
		return "."
	}
	return strings.TrimSuffix(root, string(os.PathSeparator)+"workflow_runs")
}

type workflowDatabasePool struct {
	mu         sync.Mutex
	workspace  string
	db         *sql.DB
	users      int
	persistent bool
}

var workflowDatabasePools sync.Map

func workflowDatabasePoolFor(workspace string) *workflowDatabasePool {
	key, err := workflowDatabasePath(workspace)
	if err != nil {
		key = filepath.Clean(workspace)
	}
	actual, _ := workflowDatabasePools.LoadOrStore(key, &workflowDatabasePool{workspace: workspace})
	return actual.(*workflowDatabasePool)
}

func borrowWorkflowDatabase(
	ctx context.Context,
	workspace string,
) (*sql.DB, func(), error) {
	pool := workflowDatabasePoolFor(workspace)
	db, err := pool.borrow(ctx)
	if err != nil {
		return nil, nil, err
	}
	return db, pool.release, nil
}

func (pool *workflowDatabasePool) borrow(ctx context.Context) (*sql.DB, error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.db == nil {
		db, err := openWorkflowDatabase(ctx, pool.workspace)
		if err != nil {
			return nil, workflowDatabaseError("open", err)
		}
		pool.db = db
	}
	pool.users++
	return pool.db, nil
}

func (pool *workflowDatabasePool) release() {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.users > 0 {
		pool.users--
	}
	// Broker-owned runtime pools remain stable for the runtime generation.
	// Standalone embedders and tests have no broker shutdown callback, so close
	// synchronously at the operation boundary to avoid retaining mappings after
	// their temporary storage root is removed.
	if pool.users == 0 && pool.db != nil && !pool.persistent {
		_ = pool.db.Close()
		pool.db = nil
	}
}

func (pool *workflowDatabasePool) retainUntilClose() {
	pool.mu.Lock()
	pool.persistent = true
	pool.mu.Unlock()
}

func (pool *workflowDatabasePool) close() error {
	if pool == nil {
		return nil
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.users != 0 {
		return fmt.Errorf("workflow database still has %d active users", pool.users)
	}
	pool.persistent = false
	if pool.db == nil {
		return nil
	}
	err := pool.db.Close()
	pool.db = nil
	return err
}

func (s *FileRunStore) borrowDatabase(ctx context.Context) (*sql.DB, error) {
	s.poolOnce.Do(func() {
		if s.database == nil {
			s.database = workflowDatabasePoolFor(s.workspaceDir())
		}
	})
	return s.database.borrow(ctx)
}

func (s *FileRunStore) releaseDatabase() {
	if s != nil && s.database != nil {
		s.database.release()
	}
}

// Close releases this logical store reference. The database broker owns the
// physical pool and retains it until broker shutdown, so callers never close a
// live SQLite generation as an operation becomes idle.
func (s *FileRunStore) Close() error {
	return nil
}

func withWorkflowDB[T any](
	ctx context.Context,
	store *FileRunStore,
	operation string,
	fn func(*sql.DB) (T, error),
) (T, error) {
	var zero T
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	db, err := store.borrowDatabase(ctx)
	if err != nil {
		return zero, err
	}
	defer store.releaseDatabase()
	value, err := fn(db)
	if err != nil {
		return zero, workflowDatabaseError(operation, err)
	}
	return value, nil
}

func workflowImmediate[T any](
	ctx context.Context,
	db *sql.DB,
	fn func(*sql.Conn) (T, error),
) (T, error) {
	var value T
	err := sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		var err error
		value, err = fn(conn)
		return err
	})
	return value, err
}

func encodeWorkflowJSON(value any, maximum int64) ([]byte, error) {
	if value == nil || (reflect.ValueOf(value).Kind() == reflect.Pointer && reflect.ValueOf(value).IsNil()) {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	data, err = canonicalWorkflowJSON(data)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("workflow JSON payload exceeds its limit")
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("workflow JSON payload is not valid UTF-8")
	}
	return data, nil
}

func decodeWorkflowJSON(data []byte, target any) error {
	if len(data) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("workflow JSON contains multiple values")
		}
		return err
	}
	return nil
}

func decodeOrdinaryWorkflowMap(data []byte, target *map[string]any) error {
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, target); err == nil {
		return nil
	} else {
		var fallback map[string]any
		if fallbackErr := decodeWorkflowJSON(data, &fallback); fallbackErr != nil {
			return err
		}
		retained, fallbackErr := normalizeOverflowJSONMap(fallback)
		if fallbackErr != nil || !retained {
			return err
		}
		*target = fallback
		return nil
	}
}

func decodeWorkflowInputs(data []byte, target *map[string]any) error {
	return decodeOrdinaryWorkflowMap(data, target)
}

func canonicalWorkflowJSON(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var value any
	if err := decodeWorkflowJSON(data, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func workflowText(value, name string, minimum, maximum int) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') ||
		len(value) < minimum || len(value) > maximum {
		return fmt.Errorf("%s is invalid", name)
	}
	return nil
}

func prepareWorkflowRun(run *Run) error {
	if run == nil {
		return fmt.Errorf("run is required")
	}
	if run.ID != strings.TrimSpace(run.ID) {
		return fmt.Errorf("run id is invalid")
	}
	if err := workflowText(run.ID, "run id", 1, maximumWorkflowIdentityBytes); err != nil {
		return err
	}
	if err := workflowText(run.WorkflowRef, "workflow reference", 0, maximumWorkflowReferenceBytes); err != nil {
		return err
	}
	if err := workflowText(run.Status, "run status", 0, 256); err != nil {
		return err
	}
	if err := validateRunPrivateContext(run); err != nil {
		return err
	}
	if len(run.ChildRunIDs) > maximumWorkflowChildrenPerRun ||
		len(run.Jobs) > maximumWorkflowJobsPerRun ||
		len(run.Steps) > maximumWorkflowStepsPerRun ||
		len(run.humanTasks) > maximumWorkflowHumanTasksPerRun {
		return errors.New("workflow run child collection exceeds its limit")
	}
	if _, _, err := workflowTimestamp(run.CreatedAt); err != nil {
		return fmt.Errorf("created timestamp: %w", err)
	}
	if _, _, err := workflowTimestamp(run.UpdatedAt); err != nil {
		return fmt.Errorf("updated timestamp: %w", err)
	}
	if _, _, err := nullableWorkflowTimestamp(run.CompletedAt); err != nil {
		return fmt.Errorf("completed timestamp: %w", err)
	}
	if _, _, err := nullableWorkflowTimestamp(run.CancelRequestedAt); err != nil {
		return fmt.Errorf("cancel timestamp: %w", err)
	}
	return nil
}

type workflowRunRecord struct {
	run                  *Run
	eventJSON            []byte
	inputsJSON           []byte
	outputsJSON          []byte
	deliveryHandlesJSON  []byte
	executionJSON        []byte
	privateContextJSON   []byte
	childIDsNil          int
	jobsNil              int
	stepsNil             int
	humanTasksNil        int
	private              int
	completedSeconds     any
	completedNanoseconds any
	cancelSeconds        any
	cancelNanoseconds    any
}

func encodeWorkflowRunRecord(run *Run) (*workflowRunRecord, error) {
	if err := prepareWorkflowRun(run); err != nil {
		return nil, err
	}
	record := &workflowRunRecord{run: run}
	var err error
	if record.eventJSON, err = encodeWorkflowJSON(run.Event, maximumWorkflowRunPayloadBytes); err != nil {
		return nil, err
	}
	if record.inputsJSON, err = encodeWorkflowJSON(run.Inputs, maximumWorkflowRunPayloadBytes); err != nil {
		return nil, err
	}
	if record.outputsJSON, err = encodeWorkflowJSON(run.Outputs, maximumWorkflowRunPayloadBytes); err != nil {
		return nil, err
	}
	if record.deliveryHandlesJSON, err = encodeWorkflowJSON(
		run.Delivery.ReplyHandles,
		maximumWorkflowRunPayloadBytes,
	); err != nil {
		return nil, err
	}
	if run.execution != nil {
		execution := *run.execution
		execution.Checkpoint = checkpointWorkflowRun(run)
		if record.executionJSON, err = encodeWorkflowJSON(&execution, maximumWorkflowRunPayloadBytes); err != nil {
			return nil, err
		}
	}
	if record.privateContextJSON, err = encodeWorkflowJSON(
		run.privateRoot,
		maximumWorkflowRunPayloadBytes,
	); err != nil {
		return nil, err
	}
	if run.ChildRunIDs == nil {
		record.childIDsNil = 1
	}
	if run.Jobs == nil {
		record.jobsNil = 1
	}
	if run.Steps == nil {
		record.stepsNil = 1
	}
	if run.humanTasks == nil {
		record.humanTasksNil = 1
	}
	if IsPrivateWorkflowRun(run) {
		record.private = 1
	}
	if record.completedSeconds, record.completedNanoseconds, err = nullableWorkflowTimestamp(
		run.CompletedAt,
	); err != nil {
		return nil, err
	}
	if record.cancelSeconds, record.cancelNanoseconds, err = nullableWorkflowTimestamp(
		run.CancelRequestedAt,
	); err != nil {
		return nil, err
	}
	return record, nil
}

func originColumns(origin *RunOrigin) (any, any, any, any) {
	if origin == nil {
		return nil, nil, nil, nil
	}
	return origin.Kind, origin.EventID, origin.DispatchID, origin.RootRunID
}

const workflowRunInsertSQL = `INSERT INTO workflow_runs (
 run_id, workflow_ref, status, context_visibility, parent_run_id, caller_job_id,
 retry_of_run_id, session_key, delivery_channel, delivery_chat_id, delivery_topic_id,
 delivery_thread_ts, delivery_message_id, delivery_reply_message_id,
 origin_kind, origin_event_id, origin_dispatch_id, origin_root_run_id,
 error_text, cancel_reason, created_at_seconds, created_at_nanosecond,
 updated_at_seconds, updated_at_nanosecond, completed_at_seconds,
 completed_at_nanosecond, cancel_at_seconds, cancel_at_nanosecond,
 child_ids_is_null, jobs_is_null, steps_is_null, human_tasks_is_null, is_private, version
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1)`

const workflowRunUpdateSQL = `UPDATE workflow_runs SET
 workflow_ref=?, status=?, context_visibility=?, parent_run_id=?, caller_job_id=?,
 retry_of_run_id=?, session_key=?, delivery_channel=?, delivery_chat_id=?, delivery_topic_id=?,
 delivery_thread_ts=?, delivery_message_id=?, delivery_reply_message_id=?,
 origin_kind=?, origin_event_id=?, origin_dispatch_id=?, origin_root_run_id=?,
 error_text=?, cancel_reason=?, created_at_seconds=?, created_at_nanosecond=?,
 updated_at_seconds=?, updated_at_nanosecond=?, completed_at_seconds=?,
 completed_at_nanosecond=?, cancel_at_seconds=?, cancel_at_nanosecond=?,
 child_ids_is_null=?, jobs_is_null=?, steps_is_null=?, human_tasks_is_null=?, is_private=?,
 version=version+1 WHERE run_id=? AND version=?`

func workflowRunColumnArgs(record *workflowRunRecord, includeID bool) ([]any, error) {
	run := record.run
	createdSeconds, createdNanos, err := workflowTimestamp(run.CreatedAt)
	if err != nil {
		return nil, err
	}
	updatedSeconds, updatedNanos, err := workflowTimestamp(run.UpdatedAt)
	if err != nil {
		return nil, err
	}
	originKind, originEvent, originDispatch, originRoot := originColumns(run.Origin)
	parent := any(nil)
	if run.ParentRunID != "" {
		parent = run.ParentRunID
	}
	retry := any(nil)
	if run.RetryOfRunID != "" {
		retry = run.RetryOfRunID
	}
	args := []any{
		run.WorkflowRef, run.Status, run.ContextVisibility, parent, run.CallerJobID,
		retry, run.Session, run.Delivery.Channel, run.Delivery.ChatID, run.Delivery.TopicID,
		run.Delivery.ThreadTS, run.Delivery.MessageID, run.Delivery.ReplyToMessageID,
		originKind, originEvent, originDispatch, originRoot, run.Error, run.CancelReason,
		createdSeconds, createdNanos, updatedSeconds, updatedNanos,
		record.completedSeconds, record.completedNanoseconds,
		record.cancelSeconds, record.cancelNanoseconds, record.childIDsNil,
		record.jobsNil, record.stepsNil, record.humanTasksNil, record.private,
	}
	if includeID {
		args = append([]any{run.ID}, args...)
	}
	return args, nil
}

//nolint:govet // Transaction-local errors stay scoped to their exact statement.
func insertWorkflowRunConn(ctx context.Context, conn *sql.Conn, run *Run) error {
	record, err := encodeWorkflowRunRecord(run)
	if err != nil {
		return err
	}
	var runCount int
	var payloadBytes int64
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_runs`).Scan(&runCount); err != nil {
		return err
	}
	if runCount >= maximumWorkflowRuns {
		return fmt.Errorf("workflow run count exceeds its limit")
	}
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(SUM(
		COALESCE(length(event_json),0)+COALESCE(length(inputs_json),0)+
		COALESCE(length(outputs_json),0)+COALESCE(length(delivery_handles_json),0)+
		COALESCE(length(execution_json),0)+COALESCE(length(private_context_json),0)),0)
		FROM workflow_run_payloads`).Scan(&payloadBytes); err != nil {
		return err
	}
	newPayloadBytes := int64(len(record.eventJSON) + len(record.inputsJSON) + len(record.outputsJSON) +
		len(record.deliveryHandlesJSON) + len(record.executionJSON) + len(record.privateContextJSON))
	if newPayloadBytes > maximumWorkflowRunTotalBytes-payloadBytes {
		return fmt.Errorf("workflow run payload total exceeds its limit")
	}
	args, err := workflowRunColumnArgs(record, true)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, workflowRunInsertSQL, args...); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return fmt.Errorf("%w: %s", ErrRunAlreadyExists, run.ID)
		}
		return err
	}
	if err := replaceWorkflowRunChildren(ctx, conn, record); err != nil {
		return err
	}
	run.storeVersion = 1
	return nil
}

//nolint:govet // Transaction-local errors stay scoped to their exact statement.
func updateWorkflowRunConn(ctx context.Context, conn *sql.Conn, run *Run, version int64) error {
	record, err := encodeWorkflowRunRecord(run)
	if err != nil {
		return err
	}
	var totalBytes, previousBytes int64
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT COALESCE(SUM(COALESCE(length(event_json),0)+COALESCE(length(inputs_json),0)+
		 COALESCE(length(outputs_json),0)+COALESCE(length(delivery_handles_json),0)+
		 COALESCE(length(execution_json),0)+COALESCE(length(private_context_json),0)),0)
		 FROM workflow_run_payloads),
		COALESCE(length(event_json),0)+COALESCE(length(inputs_json),0)+
		COALESCE(length(outputs_json),0)+COALESCE(length(delivery_handles_json),0)+
		COALESCE(length(execution_json),0)+COALESCE(length(private_context_json),0)
		FROM workflow_run_payloads WHERE run_id=?`, run.ID).Scan(&totalBytes, &previousBytes); err != nil {
		return err
	}
	newBytes := int64(len(record.eventJSON) + len(record.inputsJSON) + len(record.outputsJSON) +
		len(record.deliveryHandlesJSON) + len(record.executionJSON) + len(record.privateContextJSON))
	if newBytes > maximumWorkflowRunTotalBytes-(totalBytes-previousBytes) {
		return fmt.Errorf("workflow run payload total exceeds its limit")
	}
	args, err := workflowRunColumnArgs(record, false)
	if err != nil {
		return err
	}
	args = append(args, run.ID, version)
	result, err := conn.ExecContext(ctx, workflowRunUpdateSQL, args...)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrRunVersionConflict
	}
	for _, table := range []string{
		"workflow_run_payloads", "workflow_run_children", "workflow_run_jobs",
		"workflow_run_steps", "workflow_human_tasks", "workflow_private_run_markers",
	} {
		if _, err := conn.ExecContext(ctx, "DELETE FROM "+table+" WHERE run_id=?", run.ID); err != nil {
			return err
		}
	}
	if err := replaceWorkflowRunChildren(ctx, conn, record); err != nil {
		return err
	}
	run.storeVersion = version + 1
	return nil
}

func replaceWorkflowRunChildren(ctx context.Context, conn *sql.Conn, record *workflowRunRecord) error {
	run := record.run
	if _, err := conn.ExecContext(ctx, `INSERT INTO workflow_run_payloads
		(run_id,event_json,inputs_json,outputs_json,delivery_handles_json,execution_json,private_context_json)
		VALUES(?,?,?,?,?,?,?)`, run.ID, record.eventJSON, record.inputsJSON, record.outputsJSON,
		record.deliveryHandlesJSON, record.executionJSON, record.privateContextJSON); err != nil {
		return err
	}
	seenChildren := make(map[string]struct{}, len(run.ChildRunIDs))
	for position, childID := range run.ChildRunIDs {
		if err := workflowText(childID, "child run id", 1, maximumWorkflowIdentityBytes); err != nil {
			return err
		}
		if _, duplicate := seenChildren[childID]; duplicate {
			return fmt.Errorf("duplicate child run id")
		}
		seenChildren[childID] = struct{}{}
		if _, err := conn.ExecContext(ctx, `INSERT INTO workflow_run_children
			(run_id,position,child_run_id) VALUES(?,?,?)`, run.ID, position, childID); err != nil {
			return err
		}
	}
	jobKeys := sortedStringKeys(run.Jobs)
	for _, key := range jobKeys {
		job := run.Jobs[key]
		outputs, err := encodeWorkflowJSON(job.Outputs, maximumWorkflowRunPayloadBytes)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO workflow_run_jobs
			(run_id,job_key,job_id,status,error_text,outputs_json) VALUES(?,?,?,?,?,?)`,
			run.ID, key, job.ID, job.Status, job.Error, outputs); err != nil {
			return err
		}
	}
	stepKeys := sortedStringKeys(run.Steps)
	for _, key := range stepKeys {
		step := run.Steps[key]
		outputs, err := encodeWorkflowJSON(step.Outputs, maximumWorkflowRunPayloadBytes)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO workflow_run_steps
			(run_id,step_key,step_id,status,error_text,outputs_json) VALUES(?,?,?,?,?,?)`,
			run.ID, key, step.ID, step.Status, step.Error, outputs); err != nil {
			return err
		}
	}
	taskKeys := sortedStringKeys(run.humanTasks)
	for _, key := range taskKeys {
		if err := insertWorkflowHumanTask(ctx, conn, run.ID, key, run.humanTasks[key]); err != nil {
			return err
		}
	}
	if record.private != 0 {
		if _, err := conn.ExecContext(
			ctx,
			`INSERT INTO workflow_private_run_markers(run_id) VALUES(?)`,
			run.ID,
		); err != nil {
			return err
		}
	}
	return validateWorkflowChildAggregateLimitsConn(ctx, conn)
}

func validateWorkflowChildAggregateLimitsConn(ctx context.Context, conn *sql.Conn) error {
	var childCount, jobCount, stepCount, humanTaskCount, validationIssueCount int
	var payloadBytes int64
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM workflow_run_children),
		(SELECT COUNT(*) FROM workflow_run_jobs),
		(SELECT COUNT(*) FROM workflow_run_steps),
		(SELECT COUNT(*) FROM workflow_human_tasks),
		(SELECT COUNT(*) FROM workflow_validation_issues),
		(SELECT COALESCE(SUM(
			COALESCE(length(event_json),0)+COALESCE(length(inputs_json),0)+
			COALESCE(length(outputs_json),0)+COALESCE(length(delivery_handles_json),0)+
			COALESCE(length(execution_json),0)+COALESCE(length(private_context_json),0)
		),0) FROM workflow_run_payloads) +
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
		&payloadBytes,
	); err != nil {
		return err
	}
	if childCount > maximumWorkflowRunChildren ||
		jobCount > maximumWorkflowRunJobs ||
		stepCount > maximumWorkflowRunSteps ||
		humanTaskCount > maximumWorkflowHumanTasks ||
		validationIssueCount > maximumWorkflowValidationIssues ||
		payloadBytes > maximumWorkflowRunTotalBytes {
		return errors.New("workflow database child storage exceeds its aggregate limits")
	}
	return nil
}

func sortedStringKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func insertWorkflowHumanTask(ctx context.Context, conn *sql.Conn, runID, key string, task WorkflowHumanTask) error {
	questions, err := encodeWorkflowJSON(task.Questions, 8<<20)
	if err != nil {
		return err
	}
	if questions == nil {
		questions = []byte("null")
	}
	responseSchema, err := encodeWorkflowJSON(task.ResponseSchema, 8<<20)
	if err != nil {
		return err
	}
	gateForm, err := encodeWorkflowJSON(task.GateForm, 8<<20)
	if err != nil {
		return err
	}
	gateWorkflow, err := encodeWorkflowJSON(task.GateWorkflow, 8<<20)
	if err != nil {
		return err
	}
	response, err := encodeWorkflowJSON(task.Response, 8<<20)
	if err != nil {
		return err
	}
	createdSec, createdNano, err := workflowTimestamp(task.CreatedAt)
	if err != nil {
		return err
	}
	updatedSec, updatedNano, err := workflowTimestamp(task.UpdatedAt)
	if err != nil {
		return err
	}
	answeredSec, answeredNano, err := nullableWorkflowTimestamp(task.AnsweredAt)
	if err != nil {
		return err
	}
	canceledSec, canceledNano, err := nullableWorkflowTimestamp(task.CanceledAt)
	if err != nil {
		return err
	}
	retrySec, retryNano, err := nullableWorkflowTimestamp(task.RetryAt)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO workflow_human_tasks (
		run_id,task_key,task_id,workflow_ref,job_id,step_id,status,revision,input_hash,title,
		actor_kind,execution_id,action_revision,response_id,created_at_seconds,
		created_at_nanosecond,updated_at_seconds,updated_at_nanosecond,answered_at_seconds,
		answered_at_nanosecond,canceled_at_seconds,canceled_at_nanosecond,retry_at_seconds,
		retry_at_nanosecond,questions_json,response_schema_json,gate_form_json,
		gate_workflow_json,response_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		runID, key, task.ID, task.WorkflowRef, task.JobID, task.StepID, task.Status, task.Revision,
		task.InputHash, task.Title, task.ActorKind, task.ExecutionID, task.ActionRevision,
		task.ResponseID, createdSec, createdNano, updatedSec, updatedNano, answeredSec,
		answeredNano, canceledSec, canceledNano, retrySec, retryNano, questions,
		responseSchema, gateForm, gateWorkflow, response)
	return err
}

const workflowRunSelectSQL = `SELECT
 r.workflow_ref,r.status,r.context_visibility,r.parent_run_id,r.caller_job_id,r.retry_of_run_id,
 r.session_key,r.delivery_channel,r.delivery_chat_id,r.delivery_topic_id,r.delivery_thread_ts,
 r.delivery_message_id,r.delivery_reply_message_id,r.origin_kind,r.origin_event_id,
 r.origin_dispatch_id,r.origin_root_run_id,r.error_text,r.cancel_reason,r.created_at_seconds,
 r.created_at_nanosecond,r.updated_at_seconds,r.updated_at_nanosecond,r.completed_at_seconds,
 r.completed_at_nanosecond,r.cancel_at_seconds,r.cancel_at_nanosecond,r.child_ids_is_null,
 r.jobs_is_null,r.steps_is_null,r.human_tasks_is_null,r.is_private,r.version,
 p.event_json,p.inputs_json,p.outputs_json,p.delivery_handles_json,p.execution_json,p.private_context_json,
 EXISTS(SELECT 1 FROM workflow_private_run_markers m WHERE m.run_id=r.run_id)
 FROM workflow_runs r JOIN workflow_run_payloads p ON p.run_id=r.run_id WHERE r.run_id=?`

func rejectWorkflowRunKeyAliasConn(ctx context.Context, conn *sql.Conn, runID string) error {
	canonical := safeID(runID)
	if canonical == runID {
		return nil
	}
	var private int
	err := conn.QueryRowContext(ctx, `SELECT is_private FROM workflow_runs WHERE run_id=?`, canonical).Scan(&private)
	if errors.Is(err, sql.ErrNoRows) {
		return os.ErrNotExist
	}
	if err != nil {
		return err
	}
	if private != 0 {
		return ErrPrivateWorkflowContext
	}
	return os.ErrNotExist
}

//nolint:govet // Decode errors stay scoped to the exact payload or relation.
func getWorkflowRunConn(ctx context.Context, conn *sql.Conn, runID string) (*Run, int64, error) {
	if runID == "" {
		return nil, 0, os.ErrNotExist
	}
	if err := rejectWorkflowRunKeyAliasConn(ctx, conn, runID); err != nil {
		return nil, 0, err
	}
	run := &Run{ID: runID}
	var parent, retry sql.NullString
	var originKind, originEvent, originDispatch, originRoot sql.NullString
	var createdSec, createdNano, updatedSec, updatedNano int64
	var completedSec, completedNano, cancelSec, cancelNano sql.NullInt64
	var childNil, jobsNil, stepsNil, tasksNil, private, marker int
	var version int64
	var eventJSON, inputsJSON, outputsJSON, handlesJSON, executionJSON, privateJSON []byte
	err := conn.QueryRowContext(ctx, workflowRunSelectSQL, runID).Scan(
		&run.WorkflowRef, &run.Status, &run.ContextVisibility, &parent, &run.CallerJobID, &retry,
		&run.Session, &run.Delivery.Channel, &run.Delivery.ChatID, &run.Delivery.TopicID,
		&run.Delivery.ThreadTS, &run.Delivery.MessageID, &run.Delivery.ReplyToMessageID,
		&originKind, &originEvent, &originDispatch, &originRoot, &run.Error, &run.CancelReason,
		&createdSec, &createdNano, &updatedSec, &updatedNano, &completedSec, &completedNano,
		&cancelSec, &cancelNano, &childNil, &jobsNil, &stepsNil, &tasksNil, &private, &version,
		&eventJSON, &inputsJSON, &outputsJSON, &handlesJSON, &executionJSON, &privateJSON, &marker,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, os.ErrNotExist
	}
	if err != nil {
		return nil, 0, err
	}
	if private != marker {
		return nil, 0, ErrPrivateWorkflowContext
	}
	run.storeVersion = version
	run.ParentRunID = parent.String
	run.RetryOfRunID = retry.String
	if originKind.Valid {
		if !originEvent.Valid || !originRoot.Valid {
			return nil, 0, ErrInvalidRunOrigin
		}
		run.Origin = &RunOrigin{
			Kind:       originKind.String,
			EventID:    originEvent.String,
			DispatchID: originDispatch.String,
			RootRunID:  originRoot.String,
		}
	}
	run.CreatedAt = workflowTime(createdSec, createdNano)
	run.UpdatedAt = workflowTime(updatedSec, updatedNano)
	if completedSec.Valid != completedNano.Valid || cancelSec.Valid != cancelNano.Valid {
		return nil, 0, errors.New("workflow timestamp columns are inconsistent")
	}
	if completedSec.Valid {
		value := workflowTime(completedSec.Int64, completedNano.Int64)
		run.CompletedAt = &value
	}
	if cancelSec.Valid {
		value := workflowTime(cancelSec.Int64, cancelNano.Int64)
		run.CancelRequestedAt = &value
	}
	if err := decodeOrdinaryWorkflowMap(eventJSON, &run.Event); err != nil {
		return nil, 0, err
	}
	if err := decodeWorkflowInputs(inputsJSON, &run.Inputs); err != nil {
		return nil, 0, err
	}
	if err := decodeOrdinaryWorkflowMap(outputsJSON, &run.Outputs); err != nil {
		return nil, 0, err
	}
	if err := decodeWorkflowJSON(handlesJSON, &run.Delivery.ReplyHandles); err != nil {
		return nil, 0, err
	}
	if len(executionJSON) != 0 {
		var execution workflowExecutionState
		if err := decodeWorkflowJSON(executionJSON, &execution); err != nil {
			return nil, 0, err
		}
		run.execution = &execution
	}
	if len(privateJSON) != 0 {
		var root frozenWorkflowRootContext
		if err := decodeWorkflowJSON(privateJSON, &root); err != nil {
			return nil, 0, ErrPrivateWorkflowContext
		}
		run.privateRoot = &root
	}
	if childNil == 0 {
		run.ChildRunIDs = []string{}
	}
	if jobsNil == 0 {
		run.Jobs = map[string]JobExecution{}
	}
	if stepsNil == 0 {
		run.Steps = map[string]StepExecution{}
	}
	if tasksNil == 0 {
		run.humanTasks = map[string]WorkflowHumanTask{}
	}
	if err := loadWorkflowRunRelations(ctx, conn, run); err != nil {
		return nil, 0, err
	}
	restoreWorkflowRunCheckpoint(run)
	trustedExact, err := workflowRunUsesExactEventNumbers(ctx, conn, run)
	if err != nil {
		return nil, 0, err
	}
	if trustedExact {
		if err := decodeWorkflowJSON(eventJSON, &run.Event); err != nil {
			return nil, 0, err
		}
		if err := promoteWorkflowInputEvent(inputsJSON, &run.Inputs); err != nil {
			return nil, 0, err
		}
	}
	if err := validateRunPrivateContext(run); err != nil {
		return nil, 0, err
	}
	return run, version, nil
}

func promoteWorkflowInputEvent(data []byte, inputs *map[string]any) error {
	if len(data) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	raw, exists := fields["event"]
	if !exists {
		return nil
	}
	var event any
	if err := decodeWorkflowJSON(raw, &event); err != nil {
		return err
	}
	if *inputs == nil {
		*inputs = make(map[string]any)
	}
	(*inputs)["event"] = event
	return nil
}

func workflowRunUsesExactEventNumbers(ctx context.Context, conn *sql.Conn, run *Run) (bool, error) {
	if run == nil {
		return false, nil
	}
	if _, trusted := trustedRunOrigin(run); trusted {
		return true, nil
	}
	eventID, ok := run.Event["id"].(string)
	if !ok || !isExternalEventContext(run.Event) {
		return false, nil
	}
	current := run
	seen := map[string]struct{}{run.ID: {}}
	for depth := 0; depth < eventBackedDraftAncestryMaximumDepth; depth++ {
		if isExternalEventRun(current) || isEventBackedDraftTopLevelRun(current) {
			return true, nil
		}
		parentID := strings.TrimSpace(current.ParentRunID)
		if parentID == "" {
			return false, nil
		}
		if _, duplicate := seen[parentID]; duplicate {
			return false, nil
		}
		seen[parentID] = struct{}{}
		parent := &Run{ID: parentID}
		var parentRef, parentLink, sessionKey string
		var eventData, inputData []byte
		err := conn.QueryRowContext(ctx, `SELECT workflow_ref,COALESCE(parent_run_id,''),session_key,
			p.event_json,p.inputs_json FROM workflow_runs r JOIN workflow_run_payloads p ON p.run_id=r.run_id
			WHERE r.run_id=?`, parentID).Scan(&parentRef, &parentLink, &sessionKey, &eventData, &inputData)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		parent.WorkflowRef = parentRef
		parent.ParentRunID = parentLink
		parent.Session = sessionKey
		if err := decodeOrdinaryWorkflowMap(eventData, &parent.Event); err != nil {
			return false, err
		}
		if err := decodeOrdinaryWorkflowMap(inputData, &parent.Inputs); err != nil {
			return false, err
		}
		parentEventID, ok := parent.Event["id"].(string)
		if !ok || parentEventID != eventID || !isExternalEventContext(parent.Event) {
			return false, nil
		}
		current = parent
	}
	return false, nil
}

func loadWorkflowRunRelations(ctx context.Context, conn *sql.Conn, run *Run) error {
	if err := func() error {
		children, queryErr := conn.QueryContext(ctx, `SELECT child_run_id FROM workflow_run_children
			WHERE run_id=? ORDER BY position`, run.ID)
		if queryErr != nil {
			return queryErr
		}
		defer children.Close()
		for children.Next() {
			var child string
			if scanErr := children.Scan(&child); scanErr != nil {
				return scanErr
			}
			run.ChildRunIDs = append(run.ChildRunIDs, child)
		}
		return children.Err()
	}(); err != nil {
		return err
	}
	if err := loadWorkflowExecutions(ctx, conn, run); err != nil {
		return err
	}
	return loadWorkflowHumanTasks(ctx, conn, run)
}

func loadWorkflowExecutions(ctx context.Context, conn *sql.Conn, run *Run) error {
	if err := func() error {
		jobs, queryErr := conn.QueryContext(ctx, `SELECT job_key,job_id,status,error_text,outputs_json
			FROM workflow_run_jobs WHERE run_id=? ORDER BY job_key`, run.ID)
		if queryErr != nil {
			return queryErr
		}
		defer jobs.Close()
		for jobs.Next() {
			var key string
			var job JobExecution
			var outputs []byte
			if scanErr := jobs.Scan(&key, &job.ID, &job.Status, &job.Error, &outputs); scanErr != nil {
				return scanErr
			}
			if decodeErr := decodeOrdinaryWorkflowMap(outputs, &job.Outputs); decodeErr != nil {
				return decodeErr
			}
			if run.Jobs == nil {
				run.Jobs = make(map[string]JobExecution)
			}
			run.Jobs[key] = job
		}
		return jobs.Err()
	}(); err != nil {
		return err
	}
	steps, err := conn.QueryContext(ctx, `SELECT step_key,step_id,status,error_text,outputs_json
		FROM workflow_run_steps WHERE run_id=? ORDER BY step_key`, run.ID)
	if err != nil {
		return err
	}
	defer steps.Close()
	for steps.Next() {
		var key string
		var step StepExecution
		var outputs []byte
		if scanErr := steps.Scan(&key, &step.ID, &step.Status, &step.Error, &outputs); scanErr != nil {
			return scanErr
		}
		if decodeErr := decodeOrdinaryWorkflowMap(outputs, &step.Outputs); decodeErr != nil {
			return decodeErr
		}
		if run.Steps == nil {
			run.Steps = make(map[string]StepExecution)
		}
		run.Steps[key] = step
	}
	return steps.Err()
}

func scanNullableWorkflowTime(seconds, nanos sql.NullInt64) (*time.Time, error) {
	if seconds.Valid != nanos.Valid {
		return nil, errors.New("workflow timestamp columns are inconsistent")
	}
	if !seconds.Valid {
		return nil, nil
	}
	value := workflowTime(seconds.Int64, nanos.Int64)
	return &value, nil
}

//nolint:govet // Row errors stay scoped to the exact human-task record.
func loadWorkflowHumanTasks(ctx context.Context, conn *sql.Conn, run *Run) error {
	rows, err := conn.QueryContext(ctx, `SELECT task_key,task_id,workflow_ref,job_id,step_id,status,
		revision,input_hash,title,actor_kind,execution_id,action_revision,response_id,
		created_at_seconds,created_at_nanosecond,updated_at_seconds,updated_at_nanosecond,
		answered_at_seconds,answered_at_nanosecond,canceled_at_seconds,canceled_at_nanosecond,
		retry_at_seconds,retry_at_nanosecond,questions_json,response_schema_json,gate_form_json,
		gate_workflow_json,response_json FROM workflow_human_tasks WHERE run_id=? ORDER BY task_key`, run.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var task WorkflowHumanTask
		var createdSec, createdNano, updatedSec, updatedNano int64
		var answeredSec, answeredNano, canceledSec, canceledNano, retrySec, retryNano sql.NullInt64
		var questions, responseSchema, gateForm, gateWorkflow, response []byte
		if err := rows.Scan(&key, &task.ID, &task.WorkflowRef, &task.JobID, &task.StepID, &task.Status,
			&task.Revision, &task.InputHash, &task.Title, &task.ActorKind, &task.ExecutionID,
			&task.ActionRevision, &task.ResponseID, &createdSec, &createdNano, &updatedSec,
			&updatedNano, &answeredSec, &answeredNano, &canceledSec, &canceledNano, &retrySec,
			&retryNano, &questions, &responseSchema, &gateForm, &gateWorkflow, &response); err != nil {
			return err
		}
		task.RunID = run.ID
		task.CreatedAt = workflowTime(createdSec, createdNano)
		task.UpdatedAt = workflowTime(updatedSec, updatedNano)
		if task.AnsweredAt, err = scanNullableWorkflowTime(answeredSec, answeredNano); err != nil {
			return err
		}
		if task.CanceledAt, err = scanNullableWorkflowTime(canceledSec, canceledNano); err != nil {
			return err
		}
		if task.RetryAt, err = scanNullableWorkflowTime(retrySec, retryNano); err != nil {
			return err
		}
		if err := decodeWorkflowJSON(questions, &task.Questions); err != nil {
			return err
		}
		if err := decodeWorkflowJSON(responseSchema, &task.ResponseSchema); err != nil {
			return err
		}
		if len(gateForm) != 0 {
			task.GateForm = &GateForm{}
			if err := decodeWorkflowJSON(gateForm, task.GateForm); err != nil {
				return err
			}
		}
		if len(gateWorkflow) != 0 {
			task.GateWorkflow = &gateActionWorkflowContinuation{}
			if err := decodeWorkflowJSON(gateWorkflow, task.GateWorkflow); err != nil {
				return err
			}
		}
		if err := decodeWorkflowJSON(response, &task.Response); err != nil {
			return err
		}
		if run.humanTasks == nil {
			run.humanTasks = make(map[string]WorkflowHumanTask)
		}
		run.humanTasks[key] = task
	}
	return rows.Err()
}

//nolint:govet // Transaction-local errors stay scoped to their exact statement.
func appendWorkflowEventConn(ctx context.Context, conn *sql.Conn, event RunEvent) error {
	event.RunID = strings.TrimSpace(event.RunID)
	if event.RunID == "" {
		return fmt.Errorf("event run id is required")
	}
	run, _, err := getWorkflowRunConn(ctx, conn, event.RunID)
	if err != nil {
		return ErrPrivateWorkflowContext
	}
	if IsPrivateWorkflowRun(run) {
		event = sanitizePrivateWorkflowEvent(event)
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	seconds, nanos, err := workflowTimestamp(event.Time)
	if err != nil {
		return err
	}
	payload, err := encodeWorkflowJSON(event.Payload, maximumWorkflowEventPayloadBytes)
	if err != nil {
		return err
	}
	var eventCount, runEventCount int
	var payloadBytes int64
	if err := conn.QueryRowContext(ctx, `SELECT
		COUNT(*),COALESCE(SUM(length(payload_json)),0),
		COALESCE(SUM(CASE WHEN run_id=? THEN 1 ELSE 0 END),0)
		FROM workflow_run_events`, event.RunID).Scan(&eventCount, &payloadBytes, &runEventCount); err != nil {
		return err
	}
	if eventCount >= maximumWorkflowEvents || runEventCount >= maximumWorkflowEventsPerRun ||
		int64(len(payload)) > maximumWorkflowEventTotalBytes-payloadBytes {
		return fmt.Errorf("workflow event storage exceeds its limit")
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO workflow_run_events
		(run_id,sequence,occurred_at_seconds,occurred_nanosecond,kind,job_id,step_id,message,payload_json)
		SELECT ?,COALESCE(MAX(sequence)+1,0),?,?,?,?,?,?,? FROM workflow_run_events WHERE run_id=?`,
		event.RunID, seconds, nanos, event.Kind, event.JobID, event.StepID, event.Message,
		payload, event.RunID)
	return err
}

func listWorkflowEventsConn(ctx context.Context, conn *sql.Conn, runID string) ([]RunEvent, error) {
	_, _, err := getWorkflowRunConn(ctx, conn, runID)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := conn.QueryContext(ctx, `SELECT occurred_at_seconds,occurred_nanosecond,kind,
		job_id,step_id,message,payload_json FROM workflow_run_events WHERE run_id=? ORDER BY sequence`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]RunEvent, 0)
	for rows.Next() {
		var seconds, nanos int64
		var payload []byte
		event := RunEvent{RunID: runID}
		if err := rows.Scan(&seconds, &nanos, &event.Kind, &event.JobID, &event.StepID,
			&event.Message, &payload); err != nil {
			return nil, err
		}
		event.Time = workflowTime(seconds, nanos)
		if err := decodeOrdinaryWorkflowMap(payload, &event.Payload); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func checkWorkflowResumeConcurrencyConn(
	ctx context.Context,
	conn *sql.Conn,
	run *Run,
	maximum int,
) error {
	if maximum <= 0 || run.ParentRunID != "" {
		return nil
	}
	var running int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_runs
		WHERE run_id<>? AND status=? AND parent_run_id IS NULL`, run.ID, RunStatusRunning).Scan(&running); err != nil {
		return err
	}
	if running >= maximum {
		return fmt.Errorf("%w: %d running, max %d", ErrRunConcurrencyLimit, running, maximum)
	}
	return nil
}

func claimWorkflowHumanTaskConn(
	ctx context.Context,
	conn *sql.Conn,
	runID, taskID string,
	req HumanTaskResumeRequest,
) (*Run, WorkflowHumanTask, bool, error) {
	run, version, err := getWorkflowRunConn(ctx, conn, runID)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, WorkflowHumanTask{}, false, fmt.Errorf("%w: %s", ErrHumanTaskNotFound, runID)
		}
		return nil, WorkflowHumanTask{}, false, err
	}
	if IsPrivateWorkflowRun(run) && len(req.Secrets) != 0 {
		return nil, WorkflowHumanTask{}, false, ErrPrivateWorkflowContext
	}
	task, exists := run.humanTasks[taskID]
	if !exists || task.ID != taskID || task.RunID != run.ID {
		return nil, WorkflowHumanTask{}, false, ErrHumanTaskNotFound
	}
	if task.Status == HumanTaskStatusAnswered {
		if strings.TrimSpace(req.ResponseID) != task.ResponseID || req.InputHash != task.InputHash ||
			canonicalJSON(req.Response) != canonicalJSON(task.Response) {
			return nil, WorkflowHumanTask{}, false, ErrHumanTaskConflict
		}
		now := time.Now().UTC()
		if run.Status != RunStatusRunning || run.execution == nil || run.execution.Resume == nil ||
			run.execution.Resume.TaskID != task.ID || now.Before(run.execution.Resume.ExpiresAt) {
			return cloneRun(run), cloneWorkflowHumanTask(task), true, nil
		}
		if err := validateAnsweredHumanTaskCheckpoint(run, task); err != nil {
			return nil, WorkflowHumanTask{}, false, err
		}
		if err := checkWorkflowResumeConcurrencyConn(ctx, conn, run, req.maxConcurrent); err != nil {
			return nil, WorkflowHumanTask{}, false, err
		}
		lease := req.resumeLease
		if lease <= 0 {
			lease = humanTaskResumeLease(0)
		}
		run.execution.Resume = &workflowResumeClaim{
			TaskID:     task.ID,
			ResponseID: task.ResponseID,
			Token:      NewRunID(),
			ClaimedAt:  now,
			ExpiresAt:  now.Add(lease),
			Lease:      lease,
		}
		run.UpdatedAt = now
		if err := updateWorkflowRunConn(ctx, conn, run, version); err != nil {
			return nil, WorkflowHumanTask{}, false, err
		}
		return cloneRun(run), cloneWorkflowHumanTask(task), false, nil
	}
	if task.Status != HumanTaskStatusWaiting || run.Status != RunStatusWaiting {
		return nil, WorkflowHumanTask{}, false, ErrHumanTaskConflict
	}
	if err := validateWaitingHumanTaskCheckpoint(run, task); err != nil {
		return nil, WorkflowHumanTask{}, false, err
	}
	if err := checkWorkflowResumeConcurrencyConn(ctx, conn, run, req.maxConcurrent); err != nil {
		return nil, WorkflowHumanTask{}, false, err
	}
	if err := validateHumanTaskResume(task, req); err != nil {
		return nil, WorkflowHumanTask{}, false, err
	}
	stepKey := task.JobID + "/" + task.StepID
	step := run.Steps[stepKey]
	now := time.Now().UTC()
	task.Status = HumanTaskStatusAnswered
	task.Revision++
	task.ResponseID = strings.TrimSpace(req.ResponseID)
	task.Response = cloneJSONValue(req.Response)
	task.UpdatedAt = now
	task.AnsweredAt = &now
	run.humanTasks[task.ID] = task
	step.Status = RunStatusSucceeded
	step.Outputs = humanTaskStepOutputs(task)
	step.Error = ""
	run.Steps[stepKey] = step
	run.execution.Cursor.StepIndex++
	lease := req.resumeLease
	if lease <= 0 {
		lease = humanTaskResumeLease(0)
	}
	run.execution.Resume = &workflowResumeClaim{
		TaskID:     task.ID,
		ResponseID: task.ResponseID,
		Token:      NewRunID(),
		ClaimedAt:  now,
		ExpiresAt:  now.Add(lease),
		Lease:      lease,
	}
	run.Status = RunStatusRunning
	run.Error = ""
	run.UpdatedAt = now
	if err := updateWorkflowRunConn(ctx, conn, run, version); err != nil {
		return nil, WorkflowHumanTask{}, false, err
	}
	return cloneRun(run), cloneWorkflowHumanTask(task), false, nil
}

func renewWorkflowHumanTaskConn(
	ctx context.Context,
	conn *sql.Conn,
	runID, taskID, token string,
	lease time.Duration,
) error {
	run, version, err := getWorkflowRunConn(ctx, conn, runID)
	if err != nil {
		return err
	}
	task, exists := run.humanTasks[taskID]
	if !exists || task.Status != HumanTaskStatusAnswered || run.Status != RunStatusRunning ||
		run.execution == nil || run.execution.Resume == nil ||
		run.execution.Resume.TaskID != taskID || run.execution.Resume.Token != token {
		return ErrHumanTaskConflict
	}
	now := time.Now().UTC()
	if !now.Before(run.execution.Resume.ExpiresAt) {
		return ErrHumanTaskConflict
	}
	nextExpiry := now.Add(lease)
	if !nextExpiry.After(run.execution.Resume.ExpiresAt) {
		return nil
	}
	run.execution.Resume.ExpiresAt = nextExpiry
	run.UpdatedAt = now
	return updateWorkflowRunConn(ctx, conn, run, version)
}

func cancelWorkflowHumanTaskConn(
	ctx context.Context,
	conn *sql.Conn,
	runID, taskID, reason string,
) (*Run, error) {
	run, version, err := getWorkflowRunConn(ctx, conn, runID)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrHumanTaskNotFound, runID)
		}
		return nil, err
	}
	task, exists := run.humanTasks[taskID]
	if !exists || task.ID != taskID || task.RunID != run.ID {
		return nil, ErrHumanTaskNotFound
	}
	if task.Status != HumanTaskStatusWaiting || run.Status != RunStatusWaiting {
		return nil, ErrHumanTaskConflict
	}
	now := time.Now().UTC()
	run.Status = RunStatusCanceled
	run.CancelReason = reason
	run.CancelRequestedAt = &now
	run.CompletedAt = &now
	run.UpdatedAt = now
	task.Status = HumanTaskStatusCanceled
	task.Revision++
	task.UpdatedAt = now
	task.CanceledAt = &now
	run.humanTasks[task.ID] = task
	if err := updateWorkflowRunConn(ctx, conn, run, version); err != nil {
		return nil, err
	}
	if err := appendWorkflowEventConn(ctx, conn, RunEvent{
		Kind: "workflow.run.canceled", RunID: run.ID, Message: run.CancelReason,
	}); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *FileRunStore) ClaimHumanTask(
	ctx context.Context,
	runID string,
	taskID string,
	req HumanTaskResumeRequest,
) (*Run, WorkflowHumanTask, bool, error) {
	if s.usesWorkflowBroker() {
		return s.brokerClaimHumanTask(ctx, runID, taskID, req)
	}
	runID = strings.TrimSpace(runID)
	taskID = strings.TrimSpace(taskID)
	if runID == "" || taskID == "" {
		return nil, WorkflowHumanTask{}, false, ErrHumanTaskNotFound
	}
	type result struct {
		run       *Run
		task      WorkflowHumanTask
		duplicate bool
	}
	value, err := withWorkflowDB(ctx, s, "claim human task", func(db *sql.DB) (result, error) {
		return workflowImmediate(ctx, db, func(conn *sql.Conn) (result, error) {
			run, task, duplicate, err := claimWorkflowHumanTaskConn(ctx, conn, runID, taskID, req)
			return result{run: run, task: task, duplicate: duplicate}, err
		})
	})
	return value.run, value.task, value.duplicate, err
}

func (s *FileRunStore) RenewHumanTaskClaim(
	ctx context.Context,
	runID string,
	taskID string,
	token string,
	lease time.Duration,
) error {
	if s.usesWorkflowBroker() {
		return s.brokerRenewHumanTaskClaim(ctx, runID, taskID, token, lease)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runID = strings.TrimSpace(runID)
	taskID = strings.TrimSpace(taskID)
	token = strings.TrimSpace(token)
	if runID == "" || taskID == "" || token == "" || lease <= 0 {
		return ErrHumanTaskConflict
	}
	_, err := withWorkflowDB(ctx, s, "renew human task claim", func(db *sql.DB) (struct{}, error) {
		return workflowImmediate(ctx, db, func(conn *sql.Conn) (struct{}, error) {
			return struct{}{}, renewWorkflowHumanTaskConn(ctx, conn, runID, taskID, token, lease)
		})
	})
	return err
}

func (s *FileRunStore) CancelHumanTask(
	ctx context.Context,
	runID string,
	taskID string,
	reason string,
) (*Run, error) {
	if s.usesWorkflowBroker() {
		return s.brokerCancelHumanTask(ctx, runID, taskID, reason)
	}
	reason, err := NormalizeWorkflowCancelReason(reason)
	if err != nil {
		return nil, err
	}
	runID = strings.TrimSpace(runID)
	taskID = strings.TrimSpace(taskID)
	if runID == "" || taskID == "" {
		return nil, ErrHumanTaskNotFound
	}
	run, err := withWorkflowDB(ctx, s, "cancel human task", func(db *sql.DB) (*Run, error) {
		return workflowImmediate(ctx, db, func(conn *sql.Conn) (*Run, error) {
			return cancelWorkflowHumanTaskConn(ctx, conn, runID, taskID, reason)
		})
	})
	if err != nil {
		return nil, err
	}
	s.cancelChildRuns(ctx, run.ID, run.CancelReason)
	return run, nil
}

func (s *FileRunStore) AppendEvent(ctx context.Context, event RunEvent) error {
	if s.usesWorkflowBroker() {
		return s.brokerAppendEvent(ctx, event)
	}
	_, err := withWorkflowDB(ctx, s, "append event", func(db *sql.DB) (struct{}, error) {
		return workflowImmediate(ctx, db, func(conn *sql.Conn) (struct{}, error) {
			return struct{}{}, appendWorkflowEventConn(ctx, conn, event)
		})
	})
	return err
}

func (s *FileRunStore) Events(ctx context.Context, runID string) ([]RunEvent, error) {
	if s.usesWorkflowBroker() {
		return s.brokerEvents(ctx, runID)
	}
	runID = strings.TrimSpace(runID)
	return withWorkflowDB(ctx, s, "list events", func(db *sql.DB) ([]RunEvent, error) {
		conn, err := db.Conn(ctx)
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		return listWorkflowEventsConn(ctx, conn, runID)
	})
}

func (s *FileRunStore) DeleteRun(ctx context.Context, runID string) error {
	if s.usesWorkflowBroker() {
		return s.brokerDeleteRun(ctx, runID)
	}
	runID = strings.TrimSpace(runID)
	if runID == "" || safeID(runID) == "unknown" {
		return fmt.Errorf("run id is required")
	}
	_, err := withWorkflowDB(ctx, s, "delete run", func(db *sql.DB) (struct{}, error) {
		return workflowImmediate(ctx, db, func(conn *sql.Conn) (struct{}, error) {
			if err := rejectWorkflowRunKeyAliasConn(ctx, conn, runID); err != nil {
				return struct{}{}, err
			}
			result, err := conn.ExecContext(ctx, `DELETE FROM workflow_runs WHERE run_id=?`, runID)
			if err != nil {
				return struct{}{}, err
			}
			_, err = result.RowsAffected()
			return struct{}{}, err
		})
	})
	return err
}

func (s *FileRunStore) PruneTerminalRuns(ctx context.Context, olderThan time.Time) (int, error) {
	if s.usesWorkflowBroker() {
		return s.brokerPruneTerminalRuns(ctx, olderThan)
	}
	return withWorkflowDB(ctx, s, "prune runs", func(db *sql.DB) (int, error) {
		return workflowImmediate(ctx, db, func(conn *sql.Conn) (int, error) {
			seconds, nanos, err := workflowTimestamp(olderThan)
			if err != nil {
				return 0, err
			}
			result, err := conn.ExecContext(ctx, `DELETE FROM workflow_runs
				WHERE status IN (?,?,?,?) AND
				(COALESCE(completed_at_seconds,updated_at_seconds) < ? OR
				 (COALESCE(completed_at_seconds,updated_at_seconds)=? AND
				  COALESCE(completed_at_nanosecond,updated_at_nanosecond) < ?))`,
				RunStatusSucceeded, RunStatusFailed, RunStatusCanceled, RunStatusSkipped,
				seconds, seconds, nanos)
			if err != nil {
				return 0, err
			}
			count, err := result.RowsAffected()
			return int(count), err
		})
	})
}
