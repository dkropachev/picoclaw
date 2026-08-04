package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/fileutil"
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
	ErrRunCanceled         = errors.New("workflow run canceled")
	ErrRunAlreadyExists    = errors.New("workflow run already exists")
	ErrRunConcurrencyLimit = errors.New("workflow concurrency limit reached")
	ErrInvalidCancelReason = errors.New("invalid workflow cancellation reason")
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

	execution   *workflowExecutionState
	humanTasks  map[string]WorkflowHumanTask
	privateRoot *frozenWorkflowRootContext
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

const (
	privateRunMarkerFilename = ".private-context"
	privateRunMarkerContents = "picoclaw-private-workflow-context-v1\n"
)

var fileRunStoreLocks sync.Map

func NewFileRunStore(workspace string) *FileRunStore {
	return &FileRunStore{root: filepath.Join(workspace, "workflow_runs")}
}

func (s *FileRunStore) CreateRun(ctx context.Context, run *Run) error {
	_ = ctx
	unlock, err := s.lockRoot()
	if err != nil {
		return err
	}
	defer unlock()
	return s.createRunLocked(run)
}

func (s *FileRunStore) CreateRunIfUnderLimit(ctx context.Context, run *Run, maxConcurrent int) error {
	_ = ctx
	unlock, err := s.lockRoot()
	if err != nil {
		return err
	}
	defer unlock()
	if maxConcurrent > 0 {
		runs, err := s.listRunsLocked(ctx)
		if err != nil {
			return err
		}
		running := 0
		for _, run := range runs {
			if run.Status == RunStatusRunning && run.ParentRunID == "" {
				running++
			}
		}
		if running >= maxConcurrent {
			return fmt.Errorf(
				"%w: %d running, max %d",
				ErrRunConcurrencyLimit,
				running,
				maxConcurrent,
			)
		}
	}
	return s.createRunLocked(run)
}

func (s *FileRunStore) createRunLocked(run *Run) error {
	if run == nil {
		return fmt.Errorf("run is required")
	}
	if strings.TrimSpace(run.ID) == "" {
		return fmt.Errorf("run id is required")
	}
	if err := validateRunPrivateContext(run); err != nil {
		return err
	}
	dir := filepath.Join(s.root, safeID(run.ID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	markerPath := filepath.Join(dir, privateRunMarkerFilename)
	if markerPrivate, markerErr := readPrivateRunMarker(markerPath); markerErr != nil || markerPrivate {
		return ErrPrivateWorkflowContext
	}
	run.UpdatedAt = time.Now().UTC()
	data, err := marshalPersistedRun(run)
	if err != nil {
		return err
	}
	runPath := filepath.Join(dir, "run.json")
	if err := writeNewRunFile(runPath, data); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%w: %s", ErrRunAlreadyExists, run.ID)
		}
		// Some platforms report an existing directory or another unusual
		// filesystem entry with an error other than fs.ErrExist. Preserve the
		// store's existing duplicate semantics without using this check as the
		// creation boundary.
		if _, statErr := os.Lstat(runPath); statErr == nil {
			return fmt.Errorf("%w: %s", ErrRunAlreadyExists, run.ID)
		}
		return err
	}
	if IsPrivateWorkflowRun(run) {
		if markerErr := writeNewRunFile(markerPath, []byte(privateRunMarkerContents)); markerErr != nil {
			// The JSON markers classify the short publication window safely.
			// Creation succeeds only after the independent marker is durable.
			if removeErr := os.Remove(runPath); removeErr != nil && !os.IsNotExist(removeErr) {
				_ = syncWorkflowRunDirectory(dir)
				return ErrPrivateWorkflowContext
			}
			_ = syncWorkflowRunDirectory(dir)
			return ErrPrivateWorkflowContext
		}
	}
	return nil
}

// writeNewRunFile publishes a run with a filesystem-enforced create-only
// boundary. O_EXCL is required even while the store lock is held: the advisory
// lock is intentionally a no-op on some platforms and cannot coordinate
// separate processes there.
func writeNewRunFile(path string, data []byte) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			if closeErr := file.Close(); err == nil && closeErr != nil {
				err = closeErr
			}
		}
		if err == nil {
			return
		}
		// Close first so cleanup also works on Windows. A failed create must
		// not leave an empty or partial run that makes every retry a duplicate.
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, fmt.Errorf("remove incomplete workflow run: %w", removeErr))
		}
		if syncErr := syncWorkflowRunDirectory(filepath.Dir(path)); syncErr != nil {
			err = errors.Join(err, fmt.Errorf("sync workflow run cleanup: %w", syncErr))
		}
	}()
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	closed = true

	// The dispatcher persists its linked run ID immediately after CreateRun
	// succeeds. Make every directory entry needed to find that run durable
	// before returning: run.json, the run directory, and a newly created store
	// root beneath the workspace.
	runDir := filepath.Dir(path)
	for _, dir := range []string{runDir, filepath.Dir(runDir), filepath.Dir(filepath.Dir(runDir))} {
		if err = syncWorkflowRunDirectory(dir); err != nil {
			return fmt.Errorf("sync workflow run directory %s: %w", dir, err)
		}
	}
	return nil
}

func (s *FileRunStore) UpdateRun(ctx context.Context, run *Run) error {
	_ = ctx
	if run == nil {
		return fmt.Errorf("run is required")
	}
	if strings.TrimSpace(run.ID) == "" {
		return fmt.Errorf("run id is required")
	}
	unlock, err := s.lockRoot()
	if err != nil {
		return err
	}
	defer unlock()
	dir := filepath.Join(s.root, safeID(run.ID))
	if mkdirErr := os.MkdirAll(dir, 0o755); mkdirErr != nil {
		return mkdirErr
	}
	now := time.Now().UTC()
	existing, readErr := readRunFile(filepath.Join(dir, "run.json"))
	if readErr != nil {
		if run.privateRoot != nil || !os.IsNotExist(readErr) {
			// An unclassifiable existing record may be private. Never replace it
			// with caller-supplied state, recreate a missing private capability,
			// or expose parser/path diagnostics.
			return ErrPrivateWorkflowContext
		}
	}
	if readErr == nil {
		if privateErr := preserveFrozenRunPrivateContext(existing, run); privateErr != nil {
			return privateErr
		}
		if existing.execution != nil && existing.execution.Resume != nil &&
			existing.execution.Resume.Token != "" {
			incomingToken := ""
			if run.execution != nil && run.execution.Resume != nil {
				incomingToken = run.execution.Resume.Token
			}
			if incomingToken != existing.execution.Resume.Token {
				return ErrHumanTaskConflict
			}
		}
		if isTerminalRunStatus(existing.Status) {
			*run = *cloneRun(existing)
			return nil
		}
		if existing.execution != nil && existing.execution.Resume != nil &&
			existing.execution.Resume.Token != "" {
			if !now.Before(existing.execution.Resume.ExpiresAt) {
				return ErrHumanTaskConflict
			}
			if run.execution != nil && run.execution.Resume != nil &&
				run.execution.Resume.ExpiresAt.Before(existing.execution.Resume.ExpiresAt) {
				run.execution.Resume.ExpiresAt = existing.execution.Resume.ExpiresAt
			}
		}
	}
	run.UpdatedAt = now
	data, err := marshalPersistedRun(run)
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(filepath.Join(dir, "run.json"), data, 0o600)
}

func persistedRunDataMayBePrivate(data []byte) bool {
	return bytes.Contains(data, []byte(`"private_context"`)) ||
		bytes.Contains(data, []byte(`"context_visibility": "private"`)) ||
		bytes.Contains(data, []byte(`"context_visibility":"private"`))
}

func readRunFile(path string) (*Run, error) {
	markerPrivate, markerErr := readPrivateRunMarker(
		filepath.Join(filepath.Dir(path), privateRunMarkerFilename),
	)
	if markerErr != nil {
		return nil, ErrPrivateWorkflowContext
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if markerPrivate {
			return nil, ErrPrivateWorkflowContext
		}
		return nil, err
	}
	run, raw, err := decodeRunWithExactEventFields(data)
	if err != nil {
		if markerPrivate || persistedRunDataMayBePrivate(data) {
			return nil, ErrPrivateWorkflowContext
		}
		return nil, err
	}
	if markerPrivate != IsPrivateWorkflowRun(run) {
		return nil, ErrPrivateWorkflowContext
	}
	if safeID(run.ID) != filepath.Base(filepath.Dir(path)) {
		if !markerPrivate && !IsPrivateWorkflowRun(run) {
			return nil, os.ErrNotExist
		}
		return nil, ErrPrivateWorkflowContext
	}
	trustedEventID, trusted := trustedExternalEventRunFamily(path, run)
	if !trusted {
		if run.execution != nil && run.execution.Checkpoint != nil {
			if err := validateRunPrivateContext(run); err != nil {
				return nil, err
			}
			return run, nil
		}
		if err := restoreOrdinaryRunEventFields(run, raw); err != nil {
			return nil, err
		}
		if err := validateRunPrivateContext(run); err != nil {
			return nil, err
		}
		return run, nil
	}
	inputEvent, ok := run.Inputs["event"].(map[string]any)
	if raw.hasInputEvent &&
		(!ok ||
			!isExternalEventContext(inputEvent) ||
			inputEvent["id"] != trustedEventID) {
		if err := restoreOrdinaryRunInputEvent(run, raw); err != nil {
			return nil, err
		}
	}
	if err := validateRunPrivateContext(run); err != nil {
		return nil, err
	}
	return run, nil
}

func readPrivateRunMarker(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if string(data) != privateRunMarkerContents {
		return false, ErrPrivateWorkflowContext
	}
	return true, nil
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

func restoreOrdinaryRunEventFields(
	run *Run,
	raw runEventJSONFields,
) error {
	if run == nil {
		return nil
	}
	if raw.hasEvent {
		var event map[string]any
		if err := json.Unmarshal(raw.event, &event); err != nil {
			return err
		}
		run.Event = event
	}
	return restoreOrdinaryRunInputEvent(run, raw)
}

func restoreOrdinaryRunInputEvent(
	run *Run,
	raw runEventJSONFields,
) error {
	if run == nil || !raw.hasInputEvent {
		return nil
	}
	var inputEvent any
	if err := json.Unmarshal(raw.inputEvent, &inputEvent); err != nil {
		return err
	}
	if run.Inputs == nil {
		run.Inputs = make(map[string]any)
	}
	run.Inputs["event"] = inputEvent
	return nil
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

func trustedExternalEventRunFamily(path string, run *Run) (string, bool) {
	if run == nil {
		return "", false
	}
	if origin, trusted := trustedRunOrigin(run); trusted {
		return origin.EventID, true
	}
	eventID, eventIDOK := run.Event["id"].(string)
	if !eventIDOK || !isExternalEventContext(run.Event) {
		return "", false
	}
	if isExternalEventRun(run) || isEventBackedDraftTopLevelRun(run) {
		return eventID, true
	}
	if strings.TrimSpace(run.ParentRunID) == "" ||
		!isExternalEventContext(run.Event) {
		return "", false
	}
	storeRoot := filepath.Dir(filepath.Dir(path))
	parentID := strings.TrimSpace(run.ParentRunID)
	seen := map[string]struct{}{run.ID: {}}
	for depth := 0; depth < eventBackedDraftAncestryMaximumDepth; depth++ {
		if _, exists := seen[parentID]; exists {
			return "", false
		}
		seen[parentID] = struct{}{}
		parentData, err := os.ReadFile(filepath.Join(
			storeRoot,
			safeID(parentID),
			"run.json",
		))
		if err != nil {
			return "", false
		}
		parent, _, decodeErr := decodeRunWithExactEventFields(parentData)
		if decodeErr != nil ||
			parent == nil ||
			parent.ID != parentID {
			return "", false
		}
		parentEventID, parentEventOK := parent.Event["id"].(string)
		if !parentEventOK ||
			parentEventID != eventID ||
			!isExternalEventContext(parent.Event) {
			return "", false
		}
		if isExternalEventRun(parent) || isEventBackedDraftTopLevelRun(parent) {
			return eventID, true
		}
		parentID = strings.TrimSpace(parent.ParentRunID)
		if parentID == "" {
			return "", false
		}
	}
	return "", false
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

func (s *FileRunStore) CancelRun(ctx context.Context, runID, reason string) (*Run, error) {
	runID = strings.TrimSpace(runID)
	reason, err := NormalizeWorkflowCancelReason(reason)
	if err != nil {
		return nil, err
	}
	runPath := filepath.Join(s.root, safeID(runID), "run.json")
	unlock, err := s.lockRoot()
	if err != nil {
		return nil, err
	}
	run, err := readRunFile(runPath)
	if err != nil {
		unlock()
		return nil, err
	}
	if identityErr := exactStoredRunID(run, runID); identityErr != nil {
		unlock()
		return nil, identityErr
	}
	if isTerminalRunStatus(run.Status) {
		unlock()
		return run, nil
	}
	now := time.Now().UTC()
	run.Status = RunStatusCanceled
	run.CancelReason = strings.TrimSpace(reason)
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
	data, err := marshalPersistedRun(run)
	if err != nil {
		unlock()
		return nil, err
	}
	if err := fileutil.WriteFileAtomic(runPath, data, 0o600); err != nil {
		unlock()
		return nil, err
	}
	unlock()
	event := RunEvent{
		Kind:    "workflow.run.canceled",
		RunID:   run.ID,
		Message: run.CancelReason,
	}
	if IsPrivateWorkflowRun(run) {
		event = sanitizePrivateWorkflowEvent(event)
	}
	_ = s.AppendEvent(ctx, event)
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
	_ = ctx
	runID = strings.TrimSpace(runID)
	unlock, err := s.lockRoot()
	if err != nil {
		return nil, err
	}
	defer unlock()
	run, err := readRunFile(filepath.Join(s.root, safeID(runID), "run.json"))
	if err != nil {
		return nil, err
	}
	if run == nil || run.ID != runID {
		if run != nil && !IsPrivateWorkflowRun(run) {
			return nil, os.ErrNotExist
		}
		return nil, ErrPrivateWorkflowContext
	}
	return run, nil
}

func (s *FileRunStore) ListRuns(ctx context.Context) ([]Run, error) {
	_ = ctx
	unlock, err := s.lockRoot()
	if err != nil {
		return nil, err
	}
	defer unlock()
	return s.listRunsLocked(ctx)
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
func (s *FileRunStore) ClaimHumanTask(
	ctx context.Context,
	runID string,
	taskID string,
	req HumanTaskResumeRequest,
) (*Run, WorkflowHumanTask, bool, error) {
	_ = ctx
	runID = strings.TrimSpace(runID)
	taskID = strings.TrimSpace(taskID)
	if runID == "" || taskID == "" {
		return nil, WorkflowHumanTask{}, false, ErrHumanTaskNotFound
	}
	unlock, lockErr := s.lockRoot()
	if lockErr != nil {
		return nil, WorkflowHumanTask{}, false, lockErr
	}
	defer unlock()
	runPath := filepath.Join(s.root, safeID(runID), "run.json")
	run, readErr := readRunFile(runPath)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, WorkflowHumanTask{}, false, fmt.Errorf("%w: %s", ErrHumanTaskNotFound, runID)
		}
		return nil, WorkflowHumanTask{}, false, readErr
	}
	if run.ID != runID {
		return nil, WorkflowHumanTask{}, false, ErrHumanTaskNotFound
	}
	if IsPrivateWorkflowRun(run) && len(req.Secrets) != 0 {
		return nil, WorkflowHumanTask{}, false, ErrPrivateWorkflowContext
	}
	task, exists := run.humanTasks[taskID]
	if !exists || task.ID != taskID || task.RunID != run.ID {
		return nil, WorkflowHumanTask{}, false, ErrHumanTaskNotFound
	}
	if task.Status == HumanTaskStatusAnswered {
		if strings.TrimSpace(req.ResponseID) == task.ResponseID &&
			req.InputHash == task.InputHash &&
			canonicalJSON(req.Response) == canonicalJSON(task.Response) {
			now := time.Now().UTC()
			if run.Status != RunStatusRunning || run.execution == nil ||
				run.execution.Resume == nil || run.execution.Resume.TaskID != task.ID ||
				now.Before(run.execution.Resume.ExpiresAt) {
				return cloneRun(run), cloneWorkflowHumanTask(task), true, nil
			}
			if validationErr := validateAnsweredHumanTaskCheckpoint(run, task); validationErr != nil {
				return nil, WorkflowHumanTask{}, false, validationErr
			}
			if concurrencyErr := s.checkHumanTaskResumeConcurrencyLocked(
				ctx,
				run,
				req.maxConcurrent,
			); concurrencyErr != nil {
				return nil, WorkflowHumanTask{}, false, concurrencyErr
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
			data, marshalErr := marshalPersistedRun(run)
			if marshalErr != nil {
				return nil, WorkflowHumanTask{}, false, marshalErr
			}
			if writeErr := fileutil.WriteFileAtomic(runPath, data, 0o600); writeErr != nil {
				return nil, WorkflowHumanTask{}, false, writeErr
			}
			return cloneRun(run), cloneWorkflowHumanTask(task), false, nil
		}
		return nil, WorkflowHumanTask{}, false, ErrHumanTaskConflict
	}
	if task.Status != HumanTaskStatusWaiting || run.Status != RunStatusWaiting {
		return nil, WorkflowHumanTask{}, false, ErrHumanTaskConflict
	}
	if validationErr := validateWaitingHumanTaskCheckpoint(run, task); validationErr != nil {
		return nil, WorkflowHumanTask{}, false, validationErr
	}
	if concurrencyErr := s.checkHumanTaskResumeConcurrencyLocked(
		ctx,
		run,
		req.maxConcurrent,
	); concurrencyErr != nil {
		return nil, WorkflowHumanTask{}, false, concurrencyErr
	}
	if validationErr := validateHumanTaskResume(task, req); validationErr != nil {
		return nil, WorkflowHumanTask{}, false, validationErr
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
	data, marshalErr := marshalPersistedRun(run)
	if marshalErr != nil {
		return nil, WorkflowHumanTask{}, false, marshalErr
	}
	if writeErr := fileutil.WriteFileAtomic(runPath, data, 0o600); writeErr != nil {
		return nil, WorkflowHumanTask{}, false, writeErr
	}
	return cloneRun(run), cloneWorkflowHumanTask(task), false, nil
}

func (s *FileRunStore) checkHumanTaskResumeConcurrencyLocked(
	ctx context.Context,
	run *Run,
	maxConcurrent int,
) error {
	if maxConcurrent <= 0 || run.ParentRunID != "" {
		return nil
	}
	runs, err := s.listRunsLocked(ctx)
	if err != nil {
		return err
	}
	running := 0
	for _, candidate := range runs {
		if candidate.ID != run.ID && candidate.ParentRunID == "" &&
			candidate.Status == RunStatusRunning {
			running++
		}
	}
	if running >= maxConcurrent {
		return fmt.Errorf(
			"%w: %d running, max %d",
			ErrRunConcurrencyLimit,
			running,
			maxConcurrent,
		)
	}
	return nil
}

func (s *FileRunStore) RenewHumanTaskClaim(
	ctx context.Context,
	runID string,
	taskID string,
	token string,
	lease time.Duration,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	runID = strings.TrimSpace(runID)
	taskID = strings.TrimSpace(taskID)
	token = strings.TrimSpace(token)
	if runID == "" || taskID == "" || token == "" || lease <= 0 {
		return ErrHumanTaskConflict
	}
	unlock, err := s.lockRoot()
	if err != nil {
		return err
	}
	defer unlock()
	runPath := filepath.Join(s.root, safeID(runID), "run.json")
	run, err := readRunFile(runPath)
	if err != nil {
		return err
	}
	if run.ID != runID {
		return ErrHumanTaskNotFound
	}
	task, exists := run.humanTasks[taskID]
	if !exists || task.Status != HumanTaskStatusAnswered ||
		run.Status != RunStatusRunning || run.execution == nil ||
		run.execution.Resume == nil || run.execution.Resume.TaskID != taskID ||
		run.execution.Resume.Token != token {
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
	data, err := marshalPersistedRun(run)
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(runPath, data, 0o600)
}

func (s *FileRunStore) CancelHumanTask(
	ctx context.Context,
	runID string,
	taskID string,
	reason string,
) (*Run, error) {
	reason, err := NormalizeWorkflowCancelReason(reason)
	if err != nil {
		return nil, err
	}
	runID = strings.TrimSpace(runID)
	taskID = strings.TrimSpace(taskID)
	if runID == "" || taskID == "" {
		return nil, ErrHumanTaskNotFound
	}
	unlock, err := s.lockRoot()
	if err != nil {
		return nil, err
	}
	runPath := filepath.Join(s.root, safeID(runID), "run.json")
	run, err := readRunFile(runPath)
	if err != nil {
		unlock()
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrHumanTaskNotFound, runID)
		}
		return nil, err
	}
	if run.ID != runID {
		unlock()
		return nil, ErrHumanTaskNotFound
	}
	task, exists := run.humanTasks[taskID]
	if !exists || task.ID != taskID || task.RunID != run.ID {
		unlock()
		return nil, ErrHumanTaskNotFound
	}
	if task.Status != HumanTaskStatusWaiting || run.Status != RunStatusWaiting {
		unlock()
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
	data, err := marshalPersistedRun(run)
	if err != nil {
		unlock()
		return nil, err
	}
	if err := fileutil.WriteFileAtomic(runPath, data, 0o600); err != nil {
		unlock()
		return nil, err
	}
	unlock()
	event := RunEvent{
		Kind:    "workflow.run.canceled",
		RunID:   run.ID,
		Message: run.CancelReason,
	}
	if IsPrivateWorkflowRun(run) {
		event = sanitizePrivateWorkflowEvent(event)
	}
	_ = s.AppendEvent(ctx, event)
	s.cancelChildRuns(ctx, run.ID, run.CancelReason)
	return run, nil
}

func (s *FileRunStore) listRunsLocked(ctx context.Context) ([]Run, error) {
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
		run, err := readRunFile(filepath.Join(s.root, entry.Name(), "run.json"))
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
	event.RunID = strings.TrimSpace(event.RunID)
	if event.RunID == "" {
		return fmt.Errorf("event run id is required")
	}
	unlock, err := s.lockRoot()
	if err != nil {
		return err
	}
	defer unlock()
	dir := filepath.Join(s.root, safeID(event.RunID))
	run, readErr := readRunFile(filepath.Join(dir, "run.json"))
	if readErr != nil {
		// Events are subordinate to an existing run record. Missing and
		// unclassifiable records may be deleted/corrupt private runs, so there
		// is no safe public fallback classification.
		return ErrPrivateWorkflowContext
	}
	if identityErr := exactStoredRunID(run, event.RunID); identityErr != nil {
		return identityErr
	}
	private := IsPrivateWorkflowRun(run)
	if private {
		event = sanitizePrivateWorkflowEvent(event)
	}
	if mkdirErr := os.MkdirAll(dir, 0o755); mkdirErr != nil {
		if private {
			return ErrPrivateWorkflowContext
		}
		return mkdirErr
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	data, err := json.Marshal(event)
	if err != nil {
		if private {
			return ErrPrivateWorkflowContext
		}
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		if private {
			return ErrPrivateWorkflowContext
		}
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		if private {
			return ErrPrivateWorkflowContext
		}
		return err
	}
	if err := f.Sync(); err != nil && private {
		return ErrPrivateWorkflowContext
	} else {
		return err
	}
}

func (s *FileRunStore) Events(ctx context.Context, runID string) ([]RunEvent, error) {
	_ = ctx
	runID = strings.TrimSpace(runID)
	unlock, err := s.lockRoot()
	if err != nil {
		return nil, err
	}
	defer unlock()
	dir := filepath.Join(s.root, safeID(runID))
	run, readErr := readRunFile(filepath.Join(dir, "run.json"))
	if readErr != nil {
		if os.IsNotExist(readErr) {
			if _, eventErr := os.Stat(filepath.Join(dir, "events.jsonl")); os.IsNotExist(eventErr) {
				return nil, nil
			}
		}
		return nil, ErrPrivateWorkflowContext
	}
	if identityErr := exactStoredRunID(run, runID); identityErr != nil {
		return nil, identityErr
	}
	private := IsPrivateWorkflowRun(run)
	data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		if private {
			return nil, ErrPrivateWorkflowContext
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
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			var fallback RunEvent
			if fallbackErr := decodeJSONWithNumbers(
				[]byte(line),
				&fallback,
			); fallbackErr != nil {
				continue
			}
			retainedOverflow, fallbackErr := normalizeOverflowJSONMap(
				fallback.Payload,
			)
			if fallbackErr != nil || !retainedOverflow {
				continue
			}
			event = fallback
		}
		if private {
			event = sanitizePrivateWorkflowEvent(event)
		}
		events = append(events, event)
	}
	return events, nil
}

func (s *FileRunStore) DeleteRun(ctx context.Context, runID string) error {
	_ = ctx
	runID = strings.TrimSpace(runID)
	key := safeID(runID)
	if runID == "" || key == "unknown" {
		return fmt.Errorf("run id is required")
	}
	unlock, err := s.lockRoot()
	if err != nil {
		return err
	}
	defer unlock()
	dir := filepath.Join(s.root, key)
	run, readErr := readRunFile(filepath.Join(dir, "run.json"))
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil
		}
		return readErr
	}
	if identityErr := exactStoredRunID(run, runID); identityErr != nil {
		return identityErr
	}
	return os.RemoveAll(dir)
}

func exactStoredRunID(run *Run, requested string) error {
	if run != nil && run.ID == strings.TrimSpace(requested) {
		return nil
	}
	if IsPrivateWorkflowRun(run) {
		return ErrPrivateWorkflowContext
	}
	return os.ErrNotExist
}

func (s *FileRunStore) lockRoot() (func(), error) {
	root := filepath.Clean(s.root)
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	actual, _ := fileRunStoreLocks.LoadOrStore(root, &sync.Mutex{})
	rootMu := actual.(*sync.Mutex)
	rootMu.Lock()
	unlockFile, err := lockWorkflowRunStore(root)
	if err != nil {
		rootMu.Unlock()
		return nil, err
	}
	s.mu.Lock()
	return func() {
		s.mu.Unlock()
		unlockFile()
		rootMu.Unlock()
	}, nil
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
