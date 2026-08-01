package workflows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	HumanTaskStatusWaiting          = "waiting"
	HumanTaskStatusAnswered         = "answered"
	HumanTaskStatusCanceled         = "canceled"
	HumanTaskStatusContinuing       = "continuing"
	HumanTaskStatusRecoveryRequired = "recovery_required"

	MaxHumanTaskTitleBytes      = 4 << 10
	MaxHumanTaskPayloadBytes    = 256 << 10
	MaxHumanTaskInputHashBytes  = 512
	MaxHumanTaskResponseIDBytes = 512
)

var (
	ErrHumanTaskNotFound        = errors.New("workflow human task not found")
	ErrHumanTaskConflict        = errors.New("workflow human task conflict")
	ErrHumanTaskStale           = errors.New("workflow human task is stale")
	ErrHumanTaskResponseInvalid = errors.New("workflow human task response is invalid")
	ErrHumanTaskUnsupported     = errors.New("workflow human task is unsupported")
)

// WorkflowHumanTask is the durable, browser-safe contract for one suspended
// human/task step. Workflow definitions and execution cursors are persisted
// separately and are never part of this projection.
type WorkflowHumanTask struct {
	ID             string         `json:"id"`
	RunID          string         `json:"run_id"`
	WorkflowRef    string         `json:"workflow_ref"`
	JobID          string         `json:"job_id"`
	StepID         string         `json:"step_id"`
	Status         string         `json:"status"`
	Revision       uint64         `json:"revision"`
	InputHash      string         `json:"input_hash"`
	Title          string         `json:"title"`
	Questions      any            `json:"questions"`
	ResponseSchema map[string]any `json:"response_schema,omitempty"`
	ResponseID     string         `json:"response_id,omitempty"`
	Response       any            `json:"response,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	AnsweredAt     *time.Time     `json:"answered_at,omitempty"`
	CanceledAt     *time.Time     `json:"canceled_at,omitempty"`
	RetryAt        *time.Time     `json:"retry_at,omitempty"`
}

type HumanTaskResumeRequest struct {
	ExpectedRevision uint64            `json:"expected_revision"`
	InputHash        string            `json:"input_hash"`
	ResponseID       string            `json:"response_id"`
	Response         any               `json:"response"`
	Secrets          map[string]string `json:"secrets,omitempty"`
	resumeLease      time.Duration
	maxConcurrent    int
}

// WorkflowExecutionCursor identifies the next step after a suspended task.
// It is deliberately not projected as part of a Run.
type WorkflowExecutionCursor struct {
	JobID     string `json:"job_id"`
	StepIndex int    `json:"step_index"`
}

type workflowExecutionState struct {
	Workflow         *Workflow                `json:"workflow"`
	WorkflowRevision string                   `json:"workflow_revision"`
	Cursor           *WorkflowExecutionCursor `json:"cursor,omitempty"`
	Resume           *workflowResumeClaim     `json:"resume,omitempty"`
	Checkpoint       *workflowRunCheckpoint   `json:"checkpoint,omitempty"`
}

type workflowRunCheckpoint struct {
	Inputs  map[string]any           `json:"inputs,omitempty"`
	Event   map[string]any           `json:"event,omitempty"`
	Outputs map[string]any           `json:"outputs,omitempty"`
	Jobs    map[string]JobExecution  `json:"jobs,omitempty"`
	Steps   map[string]StepExecution `json:"steps,omitempty"`
}

type workflowResumeClaim struct {
	TaskID     string        `json:"task_id"`
	ResponseID string        `json:"response_id"`
	Token      string        `json:"token"`
	ClaimedAt  time.Time     `json:"claimed_at"`
	ExpiresAt  time.Time     `json:"expires_at"`
	Lease      time.Duration `json:"-"`
}

type humanTaskStore interface {
	RunStore
	ListHumanTasks(ctx context.Context, runID string) ([]WorkflowHumanTask, error)
	ClaimHumanTask(
		ctx context.Context,
		runID string,
		taskID string,
		req HumanTaskResumeRequest,
	) (*Run, WorkflowHumanTask, bool, error)
	RenewHumanTaskClaim(
		ctx context.Context,
		runID string,
		taskID string,
		token string,
		lease time.Duration,
	) error
	CancelHumanTask(ctx context.Context, runID, taskID, reason string) (*Run, error)
}

func humanTaskResumeLease(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return 10 * time.Minute
	}
	return timeout + time.Minute
}

func humanTaskHeartbeatInterval(lease time.Duration) time.Duration {
	interval := lease / 3
	if interval < 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	if interval > 30*time.Second {
		return 30 * time.Second
	}
	return interval
}

type workflowWaitingError struct{}

func (workflowWaitingError) Error() string { return "workflow is waiting for human input" }

func newWorkflowExecutionState(workflow *Workflow) (*workflowExecutionState, error) {
	data, err := json.Marshal(workflow)
	if err != nil {
		return nil, fmt.Errorf("snapshot workflow: %w", err)
	}
	var snapshot Workflow
	if err := decodeJSONWithNumbers(data, &snapshot); err != nil {
		return nil, fmt.Errorf("decode workflow snapshot: %w", err)
	}
	digest := sha256.Sum256(data)
	return &workflowExecutionState{
		Workflow:         &snapshot,
		WorkflowRevision: "sha256:" + hex.EncodeToString(digest[:]),
	}, nil
}

func validatePersistedWorkflowSnapshot(execution *workflowExecutionState) error {
	if execution == nil || execution.Workflow == nil || execution.Cursor == nil {
		return ErrHumanTaskConflict
	}
	if err := Validate(execution.Workflow); err != nil {
		return ErrHumanTaskConflict
	}
	data, err := json.Marshal(execution.Workflow)
	if err != nil {
		return ErrHumanTaskConflict
	}
	digest := sha256.Sum256(data)
	if execution.WorkflowRevision != "sha256:"+hex.EncodeToString(digest[:]) {
		return ErrHumanTaskConflict
	}
	return nil
}

func validateWaitingHumanTaskCheckpoint(run *Run, task WorkflowHumanTask) error {
	if run == nil || run.Status != RunStatusWaiting ||
		validatePersistedWorkflowSnapshot(run.execution) != nil {
		return ErrHumanTaskConflict
	}
	cursor := run.execution.Cursor
	if cursor.JobID != task.JobID {
		return ErrHumanTaskConflict
	}
	job, exists := run.execution.Workflow.Jobs[cursor.JobID]
	if !exists || cursor.StepIndex < 0 || cursor.StepIndex >= len(job.Steps) {
		return ErrHumanTaskConflict
	}
	step := job.Steps[cursor.StepIndex]
	stepID := strings.TrimSpace(step.ID)
	if stepID == "" {
		stepID = fmt.Sprintf("step_%d", cursor.StepIndex+1)
	}
	if stepID != task.StepID || strings.TrimSpace(step.Uses) != "human/task" ||
		humanTaskID(run.ID, task.JobID, task.StepID) != task.ID {
		return ErrHumanTaskConflict
	}
	stepExecution, exists := run.Steps[task.JobID+"/"+task.StepID]
	if !exists || stepExecution.Status != RunStatusWaiting {
		return ErrHumanTaskConflict
	}
	return nil
}

func validateAnsweredHumanTaskCheckpoint(run *Run, task WorkflowHumanTask) error {
	if run == nil || validatePersistedWorkflowSnapshot(run.execution) != nil ||
		run.execution.Resume == nil || run.execution.Resume.TaskID != task.ID {
		return ErrHumanTaskConflict
	}
	job, exists := run.execution.Workflow.Jobs[task.JobID]
	if !exists {
		return ErrHumanTaskConflict
	}
	stepIndex := -1
	for index, step := range job.Steps {
		stepID := strings.TrimSpace(step.ID)
		if stepID == "" {
			stepID = fmt.Sprintf("step_%d", index+1)
		}
		if stepID == task.StepID && strings.TrimSpace(step.Uses) == "human/task" {
			stepIndex = index
			break
		}
	}
	if stepIndex < 0 || run.execution.Cursor.JobID != task.JobID ||
		run.execution.Cursor.StepIndex != stepIndex+1 {
		return ErrHumanTaskConflict
	}
	stepExecution, exists := run.Steps[task.JobID+"/"+task.StepID]
	if !exists || stepExecution.Status != RunStatusSucceeded {
		return ErrHumanTaskConflict
	}
	return nil
}

func (e *Executor) ListHumanTasks(ctx context.Context, runID string) ([]WorkflowHumanTask, error) {
	if e == nil {
		return nil, fmt.Errorf("workflow executor is nil")
	}
	store := e.Store
	if store == nil {
		store = NewFileRunStore(e.WorkspaceDir)
	}
	tasks, ok := store.(humanTaskStore)
	if !ok {
		return nil, ErrHumanTaskUnsupported
	}
	return tasks.ListHumanTasks(ctx, runID)
}

func humanTaskID(runID, jobID, stepID string) string {
	digest := sha256.Sum256([]byte(runID + "\x00" + jobID + "\x00" + stepID))
	return "ht_" + hex.EncodeToString(digest[:16])
}

func newWorkflowHumanTask(run *Run, jobID, stepID string, with map[string]any) (WorkflowHumanTask, error) {
	if run == nil {
		return WorkflowHumanTask{}, fmt.Errorf("run is required")
	}
	title, ok := with["title"].(string)
	title = strings.TrimSpace(title)
	if !ok || title == "" || !utf8.ValidString(title) || len(title) > MaxHumanTaskTitleBytes {
		return WorkflowHumanTask{}, fmt.Errorf(
			"human/task title is required and must be at most %d bytes",
			MaxHumanTaskTitleBytes,
		)
	}
	questions, exists := with["questions"]
	if !exists || questions == nil {
		return WorkflowHumanTask{}, fmt.Errorf("human/task questions are required")
	}
	schema, err := normalizeSchemaMap(with["response_schema"])
	if err != nil {
		return WorkflowHumanTask{}, fmt.Errorf("invalid human/task response_schema: %w", err)
	}
	payload := map[string]any{
		"title":           title,
		"questions":       questions,
		"response_schema": schema,
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > MaxHumanTaskPayloadBytes {
		return WorkflowHumanTask{}, fmt.Errorf(
			"human/task payload is invalid or exceeds %d bytes",
			MaxHumanTaskPayloadBytes,
		)
	}
	inputHash := strings.TrimSpace(stringFromMap(with, "input_hash"))
	if inputHash == "" {
		digest := sha256.Sum256(encoded)
		inputHash = "sha256:" + hex.EncodeToString(digest[:])
	}
	if !utf8.ValidString(inputHash) || len(inputHash) > MaxHumanTaskInputHashBytes {
		return WorkflowHumanTask{}, fmt.Errorf("human/task input_hash exceeds %d bytes", MaxHumanTaskInputHashBytes)
	}
	now := time.Now().UTC()
	return WorkflowHumanTask{
		ID:             humanTaskID(run.ID, jobID, stepID),
		RunID:          run.ID,
		WorkflowRef:    run.WorkflowRef,
		JobID:          jobID,
		StepID:         stepID,
		Status:         HumanTaskStatusWaiting,
		Revision:       1,
		InputHash:      inputHash,
		Title:          title,
		Questions:      cloneJSONValue(questions),
		ResponseSchema: cloneMap(schema),
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

func humanTaskStepOutputs(task WorkflowHumanTask) map[string]any {
	return map[string]any{
		"task_id":     task.ID,
		"input_hash":  task.InputHash,
		"response_id": task.ResponseID,
		"response":    cloneJSONValue(task.Response),
	}
}

func validateHumanTaskResume(task WorkflowHumanTask, req HumanTaskResumeRequest) error {
	if req.ExpectedRevision == 0 || req.ExpectedRevision != task.Revision {
		return ErrHumanTaskStale
	}
	if strings.TrimSpace(req.InputHash) == "" || req.InputHash != task.InputHash {
		return ErrHumanTaskStale
	}
	responseID := strings.TrimSpace(req.ResponseID)
	if responseID == "" || !utf8.ValidString(responseID) || len(responseID) > MaxHumanTaskResponseIDBytes {
		return fmt.Errorf(
			"%w: response_id is required and must be at most %d bytes",
			ErrHumanTaskResponseInvalid,
			MaxHumanTaskResponseIDBytes,
		)
	}
	encoded, err := json.Marshal(req.Response)
	if err != nil || len(encoded) > MaxHumanTaskPayloadBytes {
		return fmt.Errorf("%w: response exceeds %d bytes", ErrHumanTaskResponseInvalid, MaxHumanTaskPayloadBytes)
	}
	if len(task.ResponseSchema) > 0 {
		if err := validateJSONSchemaValue(req.Response, task.ResponseSchema, "$"); err != nil {
			return fmt.Errorf("%w: %v", ErrHumanTaskResponseInvalid, err)
		}
	}
	return nil
}

func cloneWorkflowHumanTask(task WorkflowHumanTask) WorkflowHumanTask {
	task.Questions = cloneJSONValue(task.Questions)
	task.ResponseSchema = cloneMap(task.ResponseSchema)
	task.Response = cloneJSONValue(task.Response)
	if task.AnsweredAt != nil {
		value := *task.AnsweredAt
		task.AnsweredAt = &value
	}
	if task.CanceledAt != nil {
		value := *task.CanceledAt
		task.CanceledAt = &value
	}
	if task.RetryAt != nil {
		value := *task.RetryAt
		task.RetryAt = &value
	}
	return task
}

func sortedWorkflowHumanTasks(values map[string]WorkflowHumanTask) []WorkflowHumanTask {
	out := make([]WorkflowHumanTask, 0, len(values))
	for _, task := range values {
		out = append(out, cloneWorkflowHumanTask(task))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}
