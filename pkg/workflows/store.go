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
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	RunStatusRunning   = "running"
	RunStatusWaiting   = "waiting"
	RunStatusSucceeded = "succeeded"
	RunStatusFailed    = "failed"
	RunStatusCanceled  = "canceled"
	RunStatusSkipped   = "skipped"

	// MaxWorkflowCancelReasonBytes bounds a persisted nonempty cancellation
	// reason after surrounding whitespace is removed.
	MaxWorkflowCancelReasonBytes = 1024
)

var (
	ErrRunCanceled                = errors.New("workflow run canceled")
	ErrRunAlreadyExists           = errors.New("workflow run already exists")
	ErrRunConcurrencyLimit        = errors.New("workflow concurrency limit reached")
	ErrRunVersionConflict         = errors.New("workflow run version conflict")
	ErrWorkflowStorageUnavailable = errors.New("workflow storage unavailable")
	ErrInvalidCancelReason        = errors.New("invalid workflow cancellation reason")
)

type Run struct {
	ID                string                   `json:"id"`
	WorkflowRef       string                   `json:"workflow_ref"`
	Status            string                   `json:"status"`
	ContextVisibility string                   `json:"context_visibility,omitempty"`
	Origin            *RunOrigin               `json:"origin,omitempty"`
	ParentRunID       string                   `json:"parent_run_id,omitempty"`
	ChildRunIDs       []string                 `json:"child_run_ids,omitempty"`
	CallerJobID       string                   `json:"caller_job_id,omitempty"`
	RetryOfRunID      string                   `json:"retry_of_run_id,omitempty"`
	Session           string                   `json:"session,omitempty"`
	Delivery          Delivery                 `json:"delivery,omitempty"`
	Event             map[string]any           `json:"event,omitempty"`
	Inputs            map[string]any           `json:"inputs,omitempty"`
	Outputs           map[string]any           `json:"outputs,omitempty"`
	Jobs              map[string]JobExecution  `json:"jobs,omitempty"`
	Steps             map[string]StepExecution `json:"steps,omitempty"`
	Error             string                   `json:"error,omitempty"`
	CancelReason      string                   `json:"cancel_reason,omitempty"`
	CreatedAt         time.Time                `json:"created_at"`
	UpdatedAt         time.Time                `json:"updated_at"`
	CompletedAt       *time.Time               `json:"completed_at,omitempty"`
	CancelRequestedAt *time.Time               `json:"cancel_requested_at,omitempty"`

	execution    *workflowExecutionState
	humanTasks   map[string]WorkflowHumanTask
	privateRoot  *frozenWorkflowRootContext
	storeVersion int64
}

type RunEvent struct {
	Time    time.Time      `json:"time"`
	Kind    string         `json:"kind"`
	RunID   string         `json:"run_id"`
	JobID   string         `json:"job_id,omitempty"`
	StepID  string         `json:"step_id,omitempty"`
	Message string         `json:"message,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

type RunStore interface {
	CreateRun(ctx context.Context, run *Run) error
	UpdateRun(ctx context.Context, run *Run) error
	CancelRun(ctx context.Context, runID string, reason string) (*Run, error)
	GetRun(ctx context.Context, runID string) (*Run, error)
	ListRuns(ctx context.Context) ([]Run, error)
	AppendEvent(ctx context.Context, event RunEvent) error
	Events(ctx context.Context, runID string) ([]RunEvent, error)
	DeleteRun(ctx context.Context, runID string) error
	PruneTerminalRuns(ctx context.Context, olderThan time.Time) (int, error)
}

type FileRunStore struct {
	// root remains only so source-compatible tests and legacy discovery can
	// locate the former workflow_runs directory. New writes never use it.
	root      string
	workspace string
	database  *workflowDatabasePool
	poolOnce  sync.Once
}

const (
	privateRunMarkerFilename = ".private-context"
	privateRunMarkerContents = "picoclaw-private-workflow-context-v1\n"
)

func NewFileRunStore(workspace string) *FileRunStore {
	return &FileRunStore{
		root:      filepath.Join(workspace, "workflow_runs"),
		workspace: workspace,
		database:  workflowDatabasePoolFor(workspace),
	}
}

func (s *FileRunStore) CreateRun(ctx context.Context, run *Run) error {
	persistenceCtx := context.Background()
	_, err := withWorkflowDB(persistenceCtx, s, "create run", func(db *sql.DB) (struct{}, error) {
		_, err := workflowImmediate(persistenceCtx, db, func(conn *sql.Conn) (struct{}, error) {
			if run == nil {
				return struct{}{}, fmt.Errorf("run is required")
			}
			run.UpdatedAt = time.Now().UTC()
			return struct{}{}, insertWorkflowRunConn(persistenceCtx, conn, run)
		})
		return struct{}{}, err
	})
	return err
}

func (s *FileRunStore) CreateRunIfUnderLimit(ctx context.Context, run *Run, maxConcurrent int) error {
	persistenceCtx := context.Background()
	_, err := withWorkflowDB(persistenceCtx, s, "create run under limit", func(db *sql.DB) (struct{}, error) {
		return workflowImmediate(persistenceCtx, db, func(conn *sql.Conn) (struct{}, error) {
			if run == nil {
				return struct{}{}, fmt.Errorf("run is required")
			}
			if maxConcurrent > 0 {
				var running int
				if err := conn.QueryRowContext(persistenceCtx, `SELECT COUNT(*) FROM workflow_runs
					WHERE status=? AND parent_run_id IS NULL`, RunStatusRunning).Scan(&running); err != nil {
					return struct{}{}, err
				}
				if running >= maxConcurrent {
					return struct{}{}, fmt.Errorf("%w: %d running, max %d",
						ErrRunConcurrencyLimit, running, maxConcurrent)
				}
			}
			run.UpdatedAt = time.Now().UTC()
			return struct{}{}, insertWorkflowRunConn(persistenceCtx, conn, run)
		})
	})
	return err
}

// createRunLocked is retained only for one-package compatibility tests that
// exercised the old file helper directly. SQLite still supplies the sole
// create-only boundary.
func (s *FileRunStore) createRunLocked(run *Run) error {
	return s.CreateRun(context.Background(), run)
}

func (s *FileRunStore) UpdateRun(ctx context.Context, run *Run) error {
	if run == nil || strings.TrimSpace(run.ID) == "" {
		return fmt.Errorf("run is required")
	}
	persistenceCtx := context.Background()
	_, err := withWorkflowDB(persistenceCtx, s, "update run", func(db *sql.DB) (struct{}, error) {
		return workflowImmediate(persistenceCtx, db, func(conn *sql.Conn) (struct{}, error) {
			existing, version, err := getWorkflowRunConn(persistenceCtx, conn, strings.TrimSpace(run.ID))
			if err != nil {
				if run.privateRoot != nil || !os.IsNotExist(err) {
					return struct{}{}, ErrPrivateWorkflowContext
				}
				return struct{}{}, err
			}
			if err := preserveFrozenRunPrivateContext(existing, run); err != nil {
				return struct{}{}, err
			}
			if isTerminalRunStatus(existing.Status) {
				*run = *cloneRun(existing)
				return struct{}{}, nil
			}
			now := time.Now().UTC()
			resumeOwnsCurrentVersion := false
			if existing.execution != nil && existing.execution.Resume != nil &&
				existing.execution.Resume.Token != "" {
				incomingToken := ""
				if run.execution != nil && run.execution.Resume != nil {
					incomingToken = run.execution.Resume.Token
				}
				if incomingToken != existing.execution.Resume.Token ||
					!now.Before(existing.execution.Resume.ExpiresAt) {
					return struct{}{}, ErrHumanTaskConflict
				}
				resumeOwnsCurrentVersion = true
				if run.execution != nil && run.execution.Resume != nil &&
					run.execution.Resume.ExpiresAt.Before(existing.execution.Resume.ExpiresAt) {
					run.execution.Resume.ExpiresAt = existing.execution.Resume.ExpiresAt
				}
			}
			if run.storeVersion != 0 && run.storeVersion != version && !resumeOwnsCurrentVersion {
				return struct{}{}, ErrRunVersionConflict
			}
			run.UpdatedAt = now
			return struct{}{}, updateWorkflowRunConn(persistenceCtx, conn, run, version)
		})
	})
	return err
}

func marshalPersistedRun(run *Run) ([]byte, error) {
	if run == nil {
		return nil, fmt.Errorf("run is required")
	}
	if err := validateRunPrivateContext(run); err != nil {
		return nil, err
	}
	base, err := json.Marshal((*persistedRunJSON)(run))
	if err != nil {
		return nil, err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(base, &document); err != nil {
		return nil, err
	}
	if run.execution != nil {
		execution := *run.execution
		execution.Checkpoint = checkpointWorkflowRun(run)
		encoded, err := json.Marshal(&execution)
		if err != nil {
			return nil, err
		}
		document["execution"] = encoded
	}
	if len(run.humanTasks) != 0 {
		encoded, err := json.Marshal(run.humanTasks)
		if err != nil {
			return nil, err
		}
		document["human_tasks"] = encoded
	}
	if run.privateRoot != nil {
		encoded, err := json.Marshal(run.privateRoot)
		if err != nil {
			return nil, err
		}
		document["private_context"] = encoded
	}
	return json.MarshalIndent(document, "", "  ")
}

type runEventJSONFields struct {
	event         json.RawMessage
	inputEvent    json.RawMessage
	hasEvent      bool
	hasInputEvent bool
}

func decodeRunWithExactEventFields(
	data []byte,
) (*Run, runEventJSONFields, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, runEventJSONFields{}, err
	}
	raw := runEventJSONFields{}
	executionJSON := document["execution"]
	humanTasksJSON := document["human_tasks"]
	privateContextJSON := document["private_context"]
	delete(document, "execution")
	delete(document, "human_tasks")
	delete(document, "private_context")
	raw.event, raw.hasEvent = document["event"]
	if raw.hasEvent {
		document["event"] = json.RawMessage(`{}`)
	}
	if encodedInputs, exists := document["inputs"]; exists {
		var inputs map[string]json.RawMessage
		if err := json.Unmarshal(encodedInputs, &inputs); err != nil {
			return nil, runEventJSONFields{}, err
		}
		if inputs != nil {
			raw.inputEvent, raw.hasInputEvent = inputs["event"]
			if raw.hasInputEvent {
				inputs["event"] = json.RawMessage(`{}`)
				maskedInputs, err := json.Marshal(inputs)
				if err != nil {
					return nil, runEventJSONFields{}, err
				}
				document["inputs"] = maskedInputs
			}
		}
	}
	masked, err := json.Marshal(document)
	if err != nil {
		return nil, runEventJSONFields{}, err
	}
	var run Run
	if err := json.Unmarshal(masked, &run); err != nil {
		var fallback Run
		if fallbackErr := decodeJSONWithNumbers(masked, &fallback); fallbackErr != nil {
			return nil, runEventJSONFields{}, err
		}
		retainedOverflow, fallbackErr := normalizeRunOverflowNumbers(&fallback)
		if fallbackErr != nil || !retainedOverflow {
			return nil, runEventJSONFields{}, err
		}
		run = fallback
	}
	if raw.hasEvent {
		var event map[string]any
		if err := decodeJSONWithNumbers(raw.event, &event); err != nil {
			return nil, runEventJSONFields{}, err
		}
		run.Event = event
	}
	if raw.hasInputEvent {
		var inputEvent any
		if err := decodeJSONWithNumbers(raw.inputEvent, &inputEvent); err != nil {
			return nil, runEventJSONFields{}, err
		}
		if run.Inputs == nil {
			run.Inputs = make(map[string]any)
		}
		run.Inputs["event"] = inputEvent
	}
	if len(executionJSON) != 0 && string(executionJSON) != "null" {
		var execution workflowExecutionState
		if err := decodeJSONWithNumbers(executionJSON, &execution); err != nil {
			return nil, runEventJSONFields{}, err
		}
		run.execution = &execution
	}
	if len(humanTasksJSON) != 0 && string(humanTasksJSON) != "null" {
		var tasks map[string]WorkflowHumanTask
		if err := decodeJSONWithNumbers(humanTasksJSON, &tasks); err != nil {
			return nil, runEventJSONFields{}, err
		}
		run.humanTasks = tasks
	}
	if len(privateContextJSON) != 0 && string(privateContextJSON) != "null" {
		var privateRoot frozenWorkflowRootContext
		if err := decodeStrictJSONWithNumbers(privateContextJSON, &privateRoot); err != nil {
			return nil, runEventJSONFields{}, err
		}
		run.privateRoot = &privateRoot
	}
	restoreWorkflowRunCheckpoint(&run)
	if err := validateRunPrivateContext(&run); err != nil {
		return nil, runEventJSONFields{}, err
	}
	return &run, raw, nil
}

func decodeJSONWithNumbers(data []byte, value any) error {
	return decodeJSONWithNumbersMode(data, value, false)
}

func decodeStrictJSONWithNumbers(data []byte, value any) error {
	return decodeJSONWithNumbersMode(data, value, true)
}

func decodeJSONWithNumbersMode(data []byte, value any, strict bool) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains multiple values")
		}
		return err
	}
	return nil
}

// normalizeRunOverflowNumbers restores json.Unmarshal's legacy float64 shape
// after a UseNumber fallback, retaining json.Number only for valid tokens that
// cannot be represented by float64. Event and inputs.event are replaced from
// their separately decoded raw fields after this step.
func normalizeRunOverflowNumbers(run *Run) (bool, error) {
	if run == nil {
		return false, nil
	}
	retainedOverflow := false
	for _, values := range []map[string]any{
		run.Event,
		run.Inputs,
		run.Outputs,
	} {
		retained, err := normalizeOverflowJSONMap(values)
		if err != nil {
			return false, err
		}
		retainedOverflow = retainedOverflow || retained
	}
	for key, job := range run.Jobs {
		retained, err := normalizeOverflowJSONMap(job.Outputs)
		if err != nil {
			return false, err
		}
		retainedOverflow = retainedOverflow || retained
		run.Jobs[key] = job
	}
	for key, step := range run.Steps {
		retained, err := normalizeOverflowJSONMap(step.Outputs)
		if err != nil {
			return false, err
		}
		retainedOverflow = retainedOverflow || retained
		run.Steps[key] = step
	}
	return retainedOverflow, nil
}

func normalizeOverflowJSONMap(values map[string]any) (bool, error) {
	retainedOverflow := false
	for key, value := range values {
		normalized, retained, err := normalizeOverflowJSONValue(value)
		if err != nil {
			return false, err
		}
		values[key] = normalized
		retainedOverflow = retainedOverflow || retained
	}
	return retainedOverflow, nil
}

func normalizeOverflowJSONValue(value any) (any, bool, error) {
	switch typed := value.(type) {
	case json.Number:
		number, err := strconv.ParseFloat(typed.String(), 64)
		if err == nil {
			return number, false, nil
		}
		var numericError *strconv.NumError
		if errors.As(err, &numericError) &&
			errors.Is(numericError.Err, strconv.ErrRange) {
			return typed, true, nil
		}
		return nil, false, err
	case map[string]any:
		retained, err := normalizeOverflowJSONMap(typed)
		if err != nil {
			return nil, false, err
		}
		return typed, retained, nil
	case []any:
		retainedOverflow := false
		for index, item := range typed {
			normalized, retained, err := normalizeOverflowJSONValue(item)
			if err != nil {
				return nil, false, err
			}
			typed[index] = normalized
			retainedOverflow = retainedOverflow || retained
		}
		return typed, retainedOverflow, nil
	default:
		return value, false, nil
	}
}

func isExternalEventRun(run *Run) bool {
	if run == nil || !isExternalEventContext(run.Event) {
		return false
	}
	eventID, _ := run.Event["id"].(string)
	inputEventID, eventIDOK := run.Inputs["event_id"].(string)
	dispatchID, dispatchIDOK := run.Inputs["dispatch_id"].(string)
	return eventIDOK &&
		inputEventID == eventID &&
		dispatchIDOK &&
		isExternalDispatchID(dispatchID) &&
		run.Session == EventWorkflowSession(run.WorkflowRef, eventID)
}

func isEventBackedDraftTopLevelRun(run *Run) bool {
	if run == nil ||
		strings.TrimSpace(run.ParentRunID) != "" ||
		!isExternalEventContext(run.Event) {
		return false
	}
	workflowRef := strings.TrimSpace(run.WorkflowRef)
	targetRef, draft := strings.CutPrefix(workflowRef, "draft:")
	if !draft || strings.TrimSpace(targetRef) == "" {
		return false
	}
	eventID, _ := run.Event["id"].(string)
	inputEventID, eventIDOK := run.Inputs["event_id"].(string)
	inputEvent, inputEventOK := run.Inputs["event"].(map[string]any)
	_, dispatchPresent := run.Inputs["dispatch_id"]
	return eventIDOK &&
		inputEventID == eventID &&
		inputEventOK &&
		isExternalEventContext(inputEvent) &&
		inputEvent["id"] == eventID &&
		!dispatchPresent &&
		run.Session == EventWorkflowSession(targetRef, eventID)
}

func isExternalEventContext(event map[string]any) bool {
	if len(event) == 0 {
		return false
	}
	id, idOK := event["id"].(string)
	source, sourceOK := event["source"].(string)
	connector, connectorOK := event["connector"].(string)
	eventType, typeOK := event["type"].(string)
	return idOK && isExternalEventID(id) &&
		sourceOK && strings.TrimSpace(source) != "" &&
		connectorOK && strings.TrimSpace(connector) != "" &&
		typeOK && strings.TrimSpace(eventType) != ""
}

func isExternalEventID(id string) bool {
	if len(id) != len("ev_")+32 || !strings.HasPrefix(id, "ev_") {
		return false
	}
	for _, char := range id[len("ev_"):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func isExternalDispatchID(id string) bool {
	if len(id) != len("dsp_")+32 || !strings.HasPrefix(id, "dsp_") {
		return false
	}
	for _, char := range id[len("dsp_"):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

//nolint:govet // Transaction callback errors stay scoped to the exact mutation.
func (s *FileRunStore) CancelRun(ctx context.Context, runID, reason string) (*Run, error) {
	runID = strings.TrimSpace(runID)
	reason, err := NormalizeWorkflowCancelReason(reason)
	if err != nil {
		return nil, err
	}
	run, err := withWorkflowDB(ctx, s, "cancel run", func(db *sql.DB) (*Run, error) {
		return workflowImmediate(ctx, db, func(conn *sql.Conn) (*Run, error) {
			run, version, err := getWorkflowRunConn(ctx, conn, runID)
			if err != nil {
				return nil, err
			}
			if isTerminalRunStatus(run.Status) {
				return run, nil
			}
			now := time.Now().UTC()
			run.Status = RunStatusCanceled
			run.CancelReason = reason
			run.CancelRequestedAt = &now
			if run.CompletedAt == nil {
				run.CompletedAt = &now
			}
			run.UpdatedAt = now
			for id, task := range run.humanTasks {
				if task.Status != HumanTaskStatusWaiting {
					continue
				}
				task.Status = HumanTaskStatusCanceled
				task.Revision++
				task.UpdatedAt = now
				task.CanceledAt = &now
				run.humanTasks[id] = task
			}
			if err := updateWorkflowRunConn(ctx, conn, run, version); err != nil {
				return nil, err
			}
			event := RunEvent{Kind: "workflow.run.canceled", RunID: run.ID, Message: run.CancelReason}
			if err := appendWorkflowEventConn(ctx, conn, event); err != nil {
				return nil, err
			}
			return run, nil
		})
	})
	if err != nil {
		return nil, err
	}
	s.cancelChildRuns(ctx, run.ID, run.CancelReason)
	return run, nil
}

// NormalizeWorkflowCancelReason trims and bounds a cancellation reason for
// durable storage. Empty reasons remain valid for compatibility with existing
// non-HTTP callers; operator HTTP requests apply their stricter policy before
// calling the store.
func NormalizeWorkflowCancelReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", nil
	}
	if !utf8.ValidString(reason) || len(reason) > MaxWorkflowCancelReasonBytes {
		return "", ErrInvalidCancelReason
	}
	return reason, nil
}

func (s *FileRunStore) cancelChildRuns(ctx context.Context, parentRunID, reason string) {
	parentRunID = strings.TrimSpace(parentRunID)
	if parentRunID == "" {
		return
	}
	runs, err := s.ListRuns(ctx)
	if err != nil {
		return
	}
	for _, child := range runs {
		if child.ParentRunID != parentRunID || isTerminalRunStatus(child.Status) {
			continue
		}
		_, _ = s.CancelRun(ctx, child.ID, reason)
	}
}

func (s *FileRunStore) GetRun(ctx context.Context, runID string) (*Run, error) {
	runID = strings.TrimSpace(runID)
	run, err := withWorkflowDB(ctx, s, "get run", func(db *sql.DB) (*Run, error) {
		conn, err := db.Conn(ctx)
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		run, _, err := getWorkflowRunConn(ctx, conn, runID)
		return run, err
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	return run, err
}

// GetRunBounded rejects an oversized persisted run before decoding it. It is
// intended for bounded background recovery scans, not ordinary run display.
func (s *FileRunStore) GetRunBounded(
	ctx context.Context,
	runID string,
	maximumBytes int64,
) (*Run, error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	runID = strings.TrimSpace(runID)
	if runID == "" || maximumBytes < 1 {
		return nil, os.ErrInvalid
	}
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	data, err := marshalPersistedRun(run)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximumBytes {
		return nil, fmt.Errorf("workflow run exceeds its recovery read limit")
	}
	return run, nil
}

func (s *FileRunStore) ListRuns(ctx context.Context) ([]Run, error) {
	return withWorkflowDB(ctx, s, "list runs", func(db *sql.DB) ([]Run, error) {
		conn, err := db.Conn(ctx)
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		var ids []string
		if err := func() error {
			rows, queryErr := conn.QueryContext(ctx, `SELECT run_id FROM workflow_runs
				ORDER BY created_at_seconds DESC,created_at_nanosecond DESC,run_id`)
			if queryErr != nil {
				return queryErr
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if scanErr := rows.Scan(&id); scanErr != nil {
					return scanErr
				}
				ids = append(ids, id)
			}
			return rows.Err()
		}(); err != nil {
			return nil, err
		}
		runs := make([]Run, 0, len(ids))
		for _, id := range ids {
			run, _, err := getWorkflowRunConn(ctx, conn, id)
			if err != nil {
				return nil, err
			}
			runs = append(runs, *run)
		}
		return runs, nil
	})
}

func (s *FileRunStore) ListHumanTasks(
	ctx context.Context,
	runID string,
) ([]WorkflowHumanTask, error) {
	runID = strings.TrimSpace(runID)
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrHumanTaskNotFound, strings.TrimSpace(runID))
		}
		return nil, err
	}
	if run.ID != runID {
		return nil, fmt.Errorf("%w: %s", ErrHumanTaskNotFound, runID)
	}
	tasks := sortedWorkflowHumanTasks(run.humanTasks)
	if run.Status == RunStatusRunning && run.execution != nil && run.execution.Resume != nil {
		claim := run.execution.Resume
		for index := range tasks {
			if tasks[index].ID != claim.TaskID || tasks[index].Status != HumanTaskStatusAnswered {
				continue
			}
			retryAt := claim.ExpiresAt
			tasks[index].RetryAt = &retryAt
			if time.Now().UTC().Before(retryAt) {
				tasks[index].Status = HumanTaskStatusContinuing
			} else {
				tasks[index].Status = HumanTaskStatusRecoveryRequired
			}
		}
	}
	return tasks, nil
}

// ClaimHumanTask atomically validates and records one human response. The
// returned duplicate flag is true only for an exact idempotent replay.
func isTerminalRunStatus(status string) bool {
	switch status {
	case RunStatusSucceeded, RunStatusFailed, RunStatusCanceled, RunStatusSkipped:
		return true
	default:
		return false
	}
}

func safeID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.ReplaceAll(id, "/", "_")
	id = strings.ReplaceAll(id, "\\", "_")
	id = strings.ReplaceAll(id, "..", "_")
	if id == "" {
		return "unknown"
	}
	return id
}
