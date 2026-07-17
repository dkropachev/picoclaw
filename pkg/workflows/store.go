package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	RunStatusRunning   = "running"
	RunStatusSucceeded = "succeeded"
	RunStatusFailed    = "failed"
	RunStatusCanceled  = "canceled"
	RunStatusSkipped   = "skipped"
)

var ErrRunCanceled = fmt.Errorf("workflow run canceled")

type Run struct {
	ID                string                   `json:"id"`
	WorkflowRef       string                   `json:"workflow_ref"`
	Status            string                   `json:"status"`
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
	root string
	mu   sync.Mutex
}

func NewFileRunStore(workspace string) *FileRunStore {
	return &FileRunStore{root: filepath.Join(workspace, "workflow_runs")}
}

func (s *FileRunStore) CreateRun(ctx context.Context, run *Run) error {
	return s.UpdateRun(ctx, run)
}

func (s *FileRunStore) UpdateRun(ctx context.Context, run *Run) error {
	_ = ctx
	if run == nil {
		return fmt.Errorf("run is required")
	}
	if strings.TrimSpace(run.ID) == "" {
		return fmt.Errorf("run id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.root, safeID(run.ID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if existing, err := readRunFile(filepath.Join(dir, "run.json")); err == nil &&
		existing.Status == RunStatusCanceled && run.Status == RunStatusRunning {
		run.Status = RunStatusCanceled
		run.CancelReason = existing.CancelReason
		run.CancelRequestedAt = existing.CancelRequestedAt
		run.CompletedAt = existing.CompletedAt
	}
	run.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "run.json"), data, 0o600)
}

func readRunFile(path string) (*Run, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *FileRunStore) CancelRun(ctx context.Context, runID, reason string) (*Run, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	run.Status = RunStatusCanceled
	run.CancelReason = strings.TrimSpace(reason)
	run.CancelRequestedAt = &now
	if run.CompletedAt == nil {
		run.CompletedAt = &now
	}
	if err := s.UpdateRun(ctx, run); err != nil {
		return nil, err
	}
	_ = s.AppendEvent(ctx, RunEvent{
		Kind:    "workflow.run.canceled",
		RunID:   run.ID,
		Message: run.CancelReason,
	})
	return run, nil
}

func (s *FileRunStore) GetRun(ctx context.Context, runID string) (*Run, error) {
	_ = ctx
	data, err := os.ReadFile(filepath.Join(s.root, safeID(runID), "run.json"))
	if err != nil {
		return nil, err
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *FileRunStore) ListRuns(ctx context.Context) ([]Run, error) {
	_ = ctx
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	runs := make([]Run, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		run, err := s.GetRun(ctx, entry.Name())
		if err == nil {
			runs = append(runs, *run)
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].CreatedAt.After(runs[j].CreatedAt)
	})
	return runs, nil
}

func (s *FileRunStore) AppendEvent(ctx context.Context, event RunEvent) error {
	_ = ctx
	if strings.TrimSpace(event.RunID) == "" {
		return fmt.Errorf("event run id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.root, safeID(event.RunID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func (s *FileRunStore) Events(ctx context.Context, runID string) ([]RunEvent, error) {
	_ = ctx
	data, err := os.ReadFile(filepath.Join(s.root, safeID(runID), "events.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var events []RunEvent
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event RunEvent
		if err := json.Unmarshal([]byte(line), &event); err == nil {
			events = append(events, event)
		}
	}
	return events, nil
}

func (s *FileRunStore) DeleteRun(ctx context.Context, runID string) error {
	_ = ctx
	runID = safeID(runID)
	if runID == "" || runID == "unknown" {
		return fmt.Errorf("run id is required")
	}
	return os.RemoveAll(filepath.Join(s.root, runID))
}

func (s *FileRunStore) PruneTerminalRuns(ctx context.Context, olderThan time.Time) (int, error) {
	runs, err := s.ListRuns(ctx)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, run := range runs {
		if !isTerminalRunStatus(run.Status) {
			continue
		}
		completeAt := run.UpdatedAt
		if run.CompletedAt != nil && !run.CompletedAt.IsZero() {
			completeAt = *run.CompletedAt
		}
		if !completeAt.Before(olderThan) {
			continue
		}
		if err := s.DeleteRun(ctx, run.ID); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

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
