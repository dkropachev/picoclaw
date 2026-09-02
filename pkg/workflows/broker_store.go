package workflows

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	dbcatalog "github.com/sipeed/picoclaw/pkg/database/catalog"
)

const (
	// BrokerDomain and BrokerVersion identify the typed workflow RPC surface.
	BrokerDomain  = "workflows"
	BrokerVersion = 1

	workflowRPCDomain  = BrokerDomain
	workflowRPCVersion = BrokerVersion

	workflowRPCOperationCreateRun           = "create-run"
	workflowRPCOperationCreateRunUnderLimit = "create-run-under-limit"
	workflowRPCOperationUpdateRun           = "update-run"
	workflowRPCOperationCancelRun           = "cancel-run"
	workflowRPCOperationGetRun              = "get-run"
	workflowRPCOperationGetRunBounded       = "get-run-bounded"
	workflowRPCOperationListRuns            = "list-runs"
	workflowRPCOperationListHumanTasks      = "list-human-tasks"
	workflowRPCOperationClaimHumanTask      = "claim-human-task"
	workflowRPCOperationRenewHumanTaskClaim = "renew-human-task-claim"
	workflowRPCOperationCancelHumanTask     = "cancel-human-task"
	workflowRPCOperationAppendEvent         = "append-event"
	workflowRPCOperationEvents              = "events"
	workflowRPCOperationDeleteRun           = "delete-run"
	workflowRPCOperationPruneTerminalRuns   = "prune-terminal-runs"
	workflowRPCOperationPreflight           = "preflight"
	workflowRPCOperationResolveStore        = "resolve-store"

	workflowDefaultStoreName = "workspace.workflows"
	workflowRPCPageItems     = 128
	workflowRPCPageBytes     = 8 << 20
)

const (
	workflowErrorRunNotFound         = "workflow_run_not_found"
	workflowErrorRunCanceled         = "workflow_run_canceled"
	workflowErrorRunAlreadyExists    = "workflow_run_already_exists"
	workflowErrorConcurrencyLimit    = "workflow_concurrency_limit"
	workflowErrorVersionConflict     = "workflow_run_version_conflict"
	workflowErrorInvalidCancelReason = "workflow_invalid_cancel_reason"
	workflowErrorPrivateContext      = "workflow_private_context"
	workflowErrorHumanNotFound       = "workflow_human_task_not_found"
	workflowErrorHumanConflict       = "workflow_human_task_conflict"
	workflowErrorHumanStale          = "workflow_human_task_stale"
	workflowErrorHumanInvalid        = "workflow_human_task_response_invalid"
	workflowErrorHumanUnsupported    = "workflow_human_task_unsupported"
	workflowErrorInvalidRequest      = "workflow_invalid_request"
)

type workflowRunWire struct {
	Document     json.RawMessage `json:"document"`
	StoreVersion int64           `json:"store_version"`
}

type workflowTargetRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Cursor  int              `json:"cursor"`
}

type workflowResolveRequest struct {
	WorkspaceSelector string `json:"workspace_selector"`
}

type workflowResolveResponse struct {
	StoreID database.StoreID `json:"store_id"`
}

type workflowRunRequest struct {
	StoreID       database.StoreID `json:"store_id"`
	Run           workflowRunWire  `json:"run"`
	MaxConcurrent int              `json:"max_concurrent,omitempty"`
}

type workflowRunIDRequest struct {
	StoreID      database.StoreID `json:"store_id"`
	RunID        string           `json:"run_id"`
	MaximumBytes int64            `json:"maximum_bytes,omitempty"`
	Cursor       int              `json:"cursor,omitempty"`
}

type workflowCancelRunRequest struct {
	StoreID database.StoreID `json:"store_id"`
	RunID   string           `json:"run_id"`
	Reason  string           `json:"reason"`
}

type workflowClaimHumanTaskRequest struct {
	StoreID       database.StoreID       `json:"store_id"`
	RunID         string                 `json:"run_id"`
	TaskID        string                 `json:"task_id"`
	Resume        HumanTaskResumeRequest `json:"resume"`
	ResumeLease   time.Duration          `json:"resume_lease"`
	MaxConcurrent int                    `json:"max_concurrent"`
}

type workflowRenewHumanTaskRequest struct {
	StoreID database.StoreID `json:"store_id"`
	RunID   string           `json:"run_id"`
	TaskID  string           `json:"task_id"`
	Token   string           `json:"token"`
	Lease   time.Duration    `json:"lease"`
}

type workflowCancelHumanTaskRequest struct {
	StoreID database.StoreID `json:"store_id"`
	RunID   string           `json:"run_id"`
	TaskID  string           `json:"task_id"`
	Reason  string           `json:"reason"`
}

type workflowEventRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Event   RunEvent         `json:"event"`
}

type workflowPruneRequest struct {
	StoreID   database.StoreID `json:"store_id"`
	OlderThan time.Time        `json:"older_than"`
}

type workflowMutationResponse struct {
	Updated bool `json:"updated"`
}

type workflowRunResponse struct {
	Run workflowRunWire `json:"run"`
}

type workflowRunsResponse struct {
	Runs       []workflowRunWire `json:"runs"`
	NextCursor int               `json:"next_cursor"`
	Done       bool              `json:"done"`
}

type workflowTasksResponse struct {
	Tasks      []WorkflowHumanTask `json:"tasks"`
	NextCursor int                 `json:"next_cursor"`
	Done       bool                `json:"done"`
}

type workflowClaimResponse struct {
	Run       workflowRunWire   `json:"run"`
	Task      WorkflowHumanTask `json:"task"`
	Duplicate bool              `json:"duplicate"`
}

type workflowEventsResponse struct {
	Events     []RunEvent `json:"events"`
	NextCursor int        `json:"next_cursor"`
	Done       bool       `json:"done"`
}

type workflowPruneResponse struct {
	Deleted int `json:"deleted"`
}

type workflowPreflightResponse struct {
	Ready bool `json:"ready"`
}

// Preflight synchronously proves that the workflow store is current and
// usable before a caller persists a related automation transition.
func (s *FileRunStore) Preflight(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.usesWorkflowBroker() {
		var response workflowPreflightResponse
		if err := s.brokerCall(
			ctx,
			workflowRPCOperationPreflight,
			workflowTargetRequest{StoreID: s.storeID},
			&response,
			false,
		); err != nil {
			return err
		}
		if !response.Ready {
			return database.NewError(database.CodeIntegrity, "workflow preflight response is invalid")
		}
		return nil
	}
	db, release, err := borrowWorkflowDatabase(ctx, s.workspaceDir())
	if err != nil {
		return err
	}
	defer release()
	return db.PingContext(ctx)
}

func resolveWorkflowBrokerStoreID(
	ctx context.Context,
	client *database.Client,
	workspace string,
) (database.StoreID, error) {
	if client == nil {
		return "", database.NewError(database.CodeUnavailable, "workflow broker client is unavailable")
	}
	selector, err := workflowWorkspaceSelector(workspace)
	if err != nil {
		return "", err
	}
	var response workflowResolveResponse
	err = client.Call(
		ctx, workflowRPCDomain, workflowRPCVersion, workflowRPCOperationResolveStore,
		workflowResolveRequest{WorkspaceSelector: selector}, &response,
	)
	if err != nil {
		return "", err
	}
	if !response.StoreID.Valid() {
		return "", database.NewError(database.CodeIntegrity, "workflow broker StoreID is invalid")
	}
	return response.StoreID, nil
}

func (s *FileRunStore) usesWorkflowBroker() bool {
	return s != nil && (s.broker != nil || s.brokerErr != nil)
}

func (s *FileRunStore) workflowBrokerClient() (*database.Client, error) {
	if s == nil || s.broker == nil {
		if s != nil && s.brokerErr != nil {
			return nil, s.brokerErr
		}
		return nil, database.NewError(database.CodeUnavailable, "workflow broker client is unavailable")
	}
	if !s.storeID.Valid() {
		return nil, database.NewError(database.CodeInvalid, "workflow broker store ID is invalid")
	}
	return s.broker, nil
}

func encodeWorkflowRunWire(run *Run) (workflowRunWire, error) {
	document, err := marshalPersistedRun(run)
	if err != nil {
		return workflowRunWire{}, err
	}
	return workflowRunWire{Document: document, StoreVersion: run.storeVersion}, nil
}

func decodeWorkflowRunWire(wire workflowRunWire) (*Run, error) {
	if len(wire.Document) == 0 {
		return nil, errors.New("workflow broker run is missing")
	}
	run, _, err := decodeRunWithExactEventFields(wire.Document)
	if err != nil {
		return nil, err
	}
	run.storeVersion = wire.StoreVersion
	return run, nil
}

func workflowRPCError(err error) error {
	if err == nil {
		return nil
	}
	var structured *database.Error
	if errors.As(err, &structured) && structured != nil {
		return database.NewError(structured.Code, structured.Message)
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return database.NewError(database.CodeDeadline, "workflow operation deadline was exceeded")
	case errors.Is(err, os.ErrInvalid):
		return database.NewError(database.CodeInvalid, workflowErrorInvalidRequest)
	case errors.Is(err, ErrRunAlreadyExists):
		return database.NewError(database.CodeAlreadyExists, workflowErrorRunAlreadyExists)
	case errors.Is(err, ErrRunConcurrencyLimit):
		return database.NewError(database.CodeConflict, workflowErrorConcurrencyLimit)
	case errors.Is(err, ErrRunVersionConflict):
		return database.NewError(database.CodeConflict, workflowErrorVersionConflict)
	case errors.Is(err, ErrRunCanceled):
		return database.NewError(database.CodeConflict, workflowErrorRunCanceled)
	case errors.Is(err, ErrInvalidCancelReason):
		return database.NewError(database.CodeInvalid, workflowErrorInvalidCancelReason)
	case errors.Is(err, ErrPrivateWorkflowContext):
		return database.NewError(database.CodeUnauthorized, workflowErrorPrivateContext)
	case errors.Is(err, ErrHumanTaskNotFound):
		return database.NewError(database.CodeNotFound, workflowErrorHumanNotFound)
	case errors.Is(err, ErrHumanTaskConflict):
		return database.NewError(database.CodeConflict, workflowErrorHumanConflict)
	case errors.Is(err, ErrHumanTaskStale):
		return database.NewError(database.CodeConflict, workflowErrorHumanStale)
	case errors.Is(err, ErrHumanTaskResponseInvalid):
		return database.NewError(database.CodeInvalid, workflowErrorHumanInvalid)
	case errors.Is(err, ErrHumanTaskUnsupported):
		return database.NewError(database.CodeUnsupported, workflowErrorHumanUnsupported)
	case errors.Is(err, os.ErrNotExist):
		return database.NewError(database.CodeNotFound, workflowErrorRunNotFound)
	case errors.Is(err, sqlitestore.ErrTooNew):
		return database.NewError(database.CodeUnsupported, "workflow schema is newer than supported")
	case errors.Is(err, sqlitestore.ErrInvalidSchema), errors.Is(err, sqlitestore.ErrIntegrity):
		return database.NewError(database.CodeIntegrity, "workflow integrity validation failed")
	case errors.Is(err, ErrWorkflowStorageUnavailable):
		return database.NewError(database.CodeUnavailable, "workflow storage is unavailable")
	default:
		return database.NewError(database.CodeInternal, "workflow broker operation failed")
	}
}

func decodeWorkflowRPCError(err error) error {
	if err == nil {
		return nil
	}
	if database.CodeOf(err) == database.CodeOutcomeUnknown {
		return err
	}
	var brokerErr *database.Error
	if !errors.As(err, &brokerErr) || brokerErr == nil {
		return err
	}
	switch brokerErr.Message {
	case workflowErrorRunNotFound:
		return os.ErrNotExist
	case workflowErrorRunCanceled:
		return ErrRunCanceled
	case workflowErrorRunAlreadyExists:
		return ErrRunAlreadyExists
	case workflowErrorConcurrencyLimit:
		return ErrRunConcurrencyLimit
	case workflowErrorVersionConflict:
		return ErrRunVersionConflict
	case workflowErrorInvalidCancelReason:
		return ErrInvalidCancelReason
	case workflowErrorPrivateContext:
		return ErrPrivateWorkflowContext
	case workflowErrorHumanNotFound:
		return ErrHumanTaskNotFound
	case workflowErrorHumanConflict:
		return ErrHumanTaskConflict
	case workflowErrorHumanStale:
		return ErrHumanTaskStale
	case workflowErrorHumanInvalid:
		return ErrHumanTaskResponseInvalid
	case workflowErrorHumanUnsupported:
		return ErrHumanTaskUnsupported
	case workflowErrorInvalidRequest:
		return os.ErrInvalid
	}
	if brokerErr.Code == database.CodeDeadline {
		return context.DeadlineExceeded
	}
	if brokerErr.Code == database.CodeUnavailable || brokerErr.Code == database.CodeInternal {
		return fmt.Errorf("%w: %w", ErrWorkflowStorageUnavailable, brokerErr)
	}
	return err
}

func (s *FileRunStore) brokerCall(
	ctx context.Context,
	operation string,
	request any,
	response any,
	mutation bool,
) error {
	client, err := s.workflowBrokerClient()
	if err != nil {
		return decodeWorkflowRPCError(err)
	}
	if mutation {
		err = client.CallWithOptions(
			ctx, workflowRPCDomain, workflowRPCVersion, operation, request, response,
			database.CallOptions{Mutation: true},
		)
	} else {
		err = client.Call(ctx, workflowRPCDomain, workflowRPCVersion, operation, request, response)
	}
	return decodeWorkflowRPCError(err)
}

func (s *FileRunStore) brokerCreateRun(ctx context.Context, operation string, run *Run, limit int) error {
	wire, err := encodeWorkflowRunWire(run)
	if err != nil {
		return err
	}
	var response workflowRunResponse
	err = s.brokerCall(ctx, operation, workflowRunRequest{
		StoreID: s.storeID, Run: wire, MaxConcurrent: limit,
	}, &response, true)
	if err != nil {
		return err
	}
	updated, err := decodeWorkflowRunWire(response.Run)
	if err != nil {
		return fmt.Errorf("%w: invalid create response", ErrWorkflowStorageUnavailable)
	}
	*run = *updated
	return nil
}

func (s *FileRunStore) brokerUpdateRun(ctx context.Context, run *Run) error {
	wire, err := encodeWorkflowRunWire(run)
	if err != nil {
		return err
	}
	var response workflowRunResponse
	err = s.brokerCall(ctx, workflowRPCOperationUpdateRun, workflowRunRequest{
		StoreID: s.storeID, Run: wire,
	}, &response, true)
	if err != nil {
		return err
	}
	updated, err := decodeWorkflowRunWire(response.Run)
	if err != nil {
		return fmt.Errorf("%w: invalid update response", ErrWorkflowStorageUnavailable)
	}
	*run = *updated
	return nil
}

func (s *FileRunStore) brokerCancelRun(ctx context.Context, runID, reason string) (*Run, error) {
	var response workflowRunResponse
	err := s.brokerCall(ctx, workflowRPCOperationCancelRun, workflowCancelRunRequest{
		StoreID: s.storeID, RunID: runID, Reason: reason,
	}, &response, true)
	if err != nil {
		return nil, err
	}
	return decodeWorkflowRunWire(response.Run)
}

func (s *FileRunStore) brokerGetRun(ctx context.Context, operation, runID string, maximum int64) (*Run, error) {
	var response workflowRunResponse
	err := s.brokerCall(ctx, operation, workflowRunIDRequest{
		StoreID: s.storeID, RunID: runID, MaximumBytes: maximum,
	}, &response, false)
	if err != nil {
		return nil, err
	}
	return decodeWorkflowRunWire(response.Run)
}

func (s *FileRunStore) brokerListRuns(ctx context.Context) ([]Run, error) {
	runs := make([]Run, 0)
	cursor := 0
	for {
		var response workflowRunsResponse
		err := s.brokerCall(
			ctx,
			workflowRPCOperationListRuns,
			workflowTargetRequest{StoreID: s.storeID, Cursor: cursor},
			&response,
			false,
		)
		if err != nil {
			return nil, err
		}
		if len(response.Runs) > workflowRPCPageItems ||
			response.NextCursor != cursor+len(response.Runs) ||
			(!response.Done && response.NextCursor <= cursor) ||
			response.NextCursor > maximumWorkflowRuns || len(runs) > maximumWorkflowRuns-len(response.Runs) {
			return nil, fmt.Errorf("%w: invalid list pagination", ErrWorkflowStorageUnavailable)
		}
		for _, wire := range response.Runs {
			run, decodeErr := decodeWorkflowRunWire(wire)
			if decodeErr != nil {
				return nil, fmt.Errorf("%w: invalid list response", ErrWorkflowStorageUnavailable)
			}
			runs = append(runs, *run)
		}
		if response.Done {
			return runs, nil
		}
		cursor = response.NextCursor
	}
}

//nolint:dupl // Task and event pagination retain separate bounded wire element types.
func (s *FileRunStore) brokerListHumanTasks(ctx context.Context, runID string) ([]WorkflowHumanTask, error) {
	tasks := make([]WorkflowHumanTask, 0)
	for cursor := 0; ; {
		var response workflowTasksResponse
		err := s.brokerCall(ctx, workflowRPCOperationListHumanTasks, workflowRunIDRequest{
			StoreID: s.storeID, RunID: runID, Cursor: cursor,
		}, &response, false)
		if err != nil {
			return nil, err
		}
		if len(response.Tasks) > workflowRPCPageItems ||
			response.NextCursor != cursor+len(response.Tasks) ||
			(!response.Done && response.NextCursor <= cursor) ||
			response.NextCursor > maximumWorkflowHumanTasksPerRun ||
			len(tasks) > maximumWorkflowHumanTasksPerRun-len(response.Tasks) {
			return nil, database.NewError(database.CodeIntegrity, "workflow task pagination is invalid")
		}
		tasks = append(tasks, response.Tasks...)
		if response.Done {
			return tasks, nil
		}
		cursor = response.NextCursor
	}
}

func (s *FileRunStore) brokerClaimHumanTask(
	ctx context.Context, runID, taskID string, req HumanTaskResumeRequest,
) (*Run, WorkflowHumanTask, bool, error) {
	var response workflowClaimResponse
	err := s.brokerCall(ctx, workflowRPCOperationClaimHumanTask, workflowClaimHumanTaskRequest{
		StoreID: s.storeID, RunID: runID, TaskID: taskID, Resume: req,
		ResumeLease: req.resumeLease, MaxConcurrent: req.maxConcurrent,
	}, &response, true)
	if err != nil {
		return nil, WorkflowHumanTask{}, false, err
	}
	run, err := decodeWorkflowRunWire(response.Run)
	return run, response.Task, response.Duplicate, err
}

func (s *FileRunStore) brokerRenewHumanTaskClaim(
	ctx context.Context, runID, taskID, token string, lease time.Duration,
) error {
	var response workflowMutationResponse
	return s.brokerCall(ctx, workflowRPCOperationRenewHumanTaskClaim, workflowRenewHumanTaskRequest{
		StoreID: s.storeID, RunID: runID, TaskID: taskID, Token: token, Lease: lease,
	}, &response, true)
}

func (s *FileRunStore) brokerCancelHumanTask(
	ctx context.Context, runID, taskID, reason string,
) (*Run, error) {
	var response workflowRunResponse
	err := s.brokerCall(ctx, workflowRPCOperationCancelHumanTask, workflowCancelHumanTaskRequest{
		StoreID: s.storeID, RunID: runID, TaskID: taskID, Reason: reason,
	}, &response, true)
	if err != nil {
		return nil, err
	}
	return decodeWorkflowRunWire(response.Run)
}

func (s *FileRunStore) brokerAppendEvent(ctx context.Context, event RunEvent) error {
	var response workflowMutationResponse
	return s.brokerCall(ctx, workflowRPCOperationAppendEvent, workflowEventRequest{
		StoreID: s.storeID, Event: event,
	}, &response, true)
}

//nolint:dupl // Task and event pagination retain separate bounded wire element types.
func (s *FileRunStore) brokerEvents(ctx context.Context, runID string) ([]RunEvent, error) {
	events := make([]RunEvent, 0)
	for cursor := 0; ; {
		var response workflowEventsResponse
		err := s.brokerCall(ctx, workflowRPCOperationEvents, workflowRunIDRequest{
			StoreID: s.storeID, RunID: runID, Cursor: cursor,
		}, &response, false)
		if err != nil {
			return nil, err
		}
		if len(response.Events) > workflowRPCPageItems ||
			response.NextCursor != cursor+len(response.Events) ||
			(!response.Done && response.NextCursor <= cursor) ||
			response.NextCursor > maximumWorkflowEventsPerRun ||
			len(events) > maximumWorkflowEventsPerRun-len(response.Events) {
			return nil, database.NewError(database.CodeIntegrity, "workflow event pagination is invalid")
		}
		events = append(events, response.Events...)
		if response.Done {
			return events, nil
		}
		cursor = response.NextCursor
	}
}

func (s *FileRunStore) brokerDeleteRun(ctx context.Context, runID string) error {
	var response workflowMutationResponse
	return s.brokerCall(ctx, workflowRPCOperationDeleteRun, workflowRunIDRequest{
		StoreID: s.storeID, RunID: runID,
	}, &response, true)
}

func (s *FileRunStore) brokerPruneTerminalRuns(ctx context.Context, olderThan time.Time) (int, error) {
	var response workflowPruneResponse
	err := s.brokerCall(ctx, workflowRPCOperationPruneTerminalRuns, workflowPruneRequest{
		StoreID: s.storeID, OlderThan: olderThan,
	}, &response, true)
	return response.Deleted, err
}

type workflowBrokerWorkspace struct {
	selector  string
	store     *FileRunStore
	openOnce  sync.Once
	openError error
}

// open initializes exactly this cataloged workspace on its first domain
// operation. Store resolution remains metadata-only, and a failed sibling is
// remembered locally without preventing another StoreID from opening.
func (workspace *workflowBrokerWorkspace) open(
	ctx context.Context,
) (*FileRunStore, error) {
	if workspace == nil || workspace.store == nil {
		return nil, database.NewError(database.CodeUnavailable, "workflow store is unavailable")
	}
	workspace.openOnce.Do(func() {
		store := workspace.store
		store.database = workflowDatabasePoolFor(store.workspaceDir())
		store.database.retainUntilClose()
		if err := store.Preflight(ctx); err != nil {
			workspace.openError = errors.Join(err, store.database.close())
		}
	})
	if workspace.openError != nil {
		return nil, workspace.openError
	}
	return workspace.store, nil
}

// BrokerHandler owns one stable workflow pool for the primary and every
// distinct configured-agent workspace. Only opaque selectors and cataloged
// StoreIDs cross the RPC boundary.
type BrokerHandler struct {
	mu sync.RWMutex

	workspaces map[database.StoreID]*workflowBrokerWorkspace
	selectors  map[string]database.StoreID

	// Keep the primary aliases for one-package compatibility tests and callers
	// that only need to inspect the primary retained pool.
	storeID database.StoreID
	store   *FileRunStore
	closed  bool

	closeOnce sync.Once
	closeErr  error
}

func NewBrokerHandler(home string, cfg *config.Config) (*BrokerHandler, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() &&
		!allowUnfencedWorkflowProviderForTests.Load() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"workflow broker handler requires authenticated broker authority",
		)
	}
	if cfg == nil {
		return nil, database.NewError(database.CodeInvalid, "workflow broker configuration is invalid")
	}
	catalog, err := dbcatalog.New(home, cfg)
	if err != nil {
		return nil, err
	}
	configured, err := configuredWorkflowWorkspaces(home, cfg, catalog)
	if err != nil {
		return nil, err
	}
	handler := &BrokerHandler{
		workspaces: make(map[database.StoreID]*workflowBrokerWorkspace, len(configured)),
		selectors:  make(map[string]database.StoreID, len(configured)),
	}
	for index, item := range configured {
		// Construct only the logical target here. The provider pool is opened and
		// validated by workflowBrokerWorkspace.open on the first operation for
		// this exact StoreID.
		store := &FileRunStore{
			root: filepath.Join(item.workspace, "workflow_runs"), workspace: item.workspace,
		}
		handler.workspaces[item.storeID] = &workflowBrokerWorkspace{
			selector: item.selector,
			store:    store,
		}
		handler.selectors[item.selector] = item.storeID
		if index == 0 {
			handler.storeID = item.storeID
			handler.store = store
		}
	}
	return handler, nil
}

func (handler *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != workflowRPCDomain || request.Version != workflowRPCVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, database.NewError(database.CodeDeadline, "workflow operation deadline was exceeded")
	}
	handler.mu.RLock()
	defer handler.mu.RUnlock()
	if handler.closed {
		return nil, database.NewError(database.CodeUnavailable, "workflow broker is unavailable")
	}
	if request.Operation == workflowRPCOperationResolveStore {
		var input workflowResolveRequest
		if request.DecodePayload(&input) != nil || !validWorkflowWorkspaceSelector(input.WorkspaceSelector) {
			return nil, database.NewError(database.CodeInvalid, "workflow broker request is invalid")
		}
		storeID, ok := handler.selectors[input.WorkspaceSelector]
		if !ok {
			return nil, database.NewError(database.CodeUnauthorized, "workflow workspace is not cataloged")
		}
		return workflowResolveResponse{StoreID: storeID}, nil
	}
	storeID, err := workflowRequestStoreID(request)
	if err != nil {
		return nil, err
	}
	workspace, ok := handler.workspaces[storeID]
	if !ok || workspace == nil || workspace.store == nil {
		return nil, database.NewError(database.CodeUnauthorized, "workflow store is not cataloged")
	}
	store, err := workspace.open(ctx)
	if err != nil {
		return nil, workflowRPCError(err)
	}
	return handler.handle(ctx, request, store)
}

func (handler *BrokerHandler) validateStoreID(id database.StoreID) error {
	if !id.Valid() {
		return database.NewError(database.CodeInvalid, "workflow broker request is invalid")
	}
	if _, ok := handler.workspaces[id]; !ok {
		return database.NewError(database.CodeUnauthorized, "workflow store is not cataloged")
	}
	return nil
}

func (handler *BrokerHandler) decodeRequest(
	request database.Request,
	destination any,
	storeID *database.StoreID,
) error {
	if err := request.DecodePayload(destination); err != nil {
		return database.NewError(database.CodeInvalid, "workflow broker request is invalid")
	}
	return handler.validateStoreID(*storeID)
}

func (handler *BrokerHandler) handle(
	ctx context.Context,
	request database.Request,
	store *FileRunStore,
) (any, error) {
	switch request.Operation {
	case workflowRPCOperationPreflight:
		var input workflowTargetRequest
		if err := handler.decodeRequest(request, &input, &input.StoreID); err != nil {
			return nil, err
		}
		if err := store.Preflight(ctx); err != nil {
			return nil, workflowRPCError(err)
		}
		return workflowPreflightResponse{Ready: true}, nil
	case workflowRPCOperationCreateRun, workflowRPCOperationCreateRunUnderLimit, workflowRPCOperationUpdateRun:
		var input workflowRunRequest
		if err := handler.decodeRequest(request, &input, &input.StoreID); err != nil {
			return nil, err
		}
		run, err := decodeWorkflowRunWire(input.Run)
		if err != nil {
			return nil, database.NewError(database.CodeInvalid, "workflow run is invalid")
		}
		switch request.Operation {
		case workflowRPCOperationCreateRun:
			err = store.CreateRun(ctx, run)
		case workflowRPCOperationCreateRunUnderLimit:
			err = store.CreateRunIfUnderLimit(ctx, run, input.MaxConcurrent)
		case workflowRPCOperationUpdateRun:
			err = store.UpdateRun(ctx, run)
		}
		if err != nil {
			return nil, workflowRPCError(err)
		}
		wire, err := encodeWorkflowRunWire(run)
		return workflowRunResponse{Run: wire}, workflowRPCError(err)
	case workflowRPCOperationCancelRun:
		var input workflowCancelRunRequest
		if err := handler.decodeRequest(request, &input, &input.StoreID); err != nil {
			return nil, err
		}
		run, err := store.CancelRun(ctx, input.RunID, input.Reason)
		if err != nil {
			return nil, workflowRPCError(err)
		}
		wire, err := encodeWorkflowRunWire(run)
		return workflowRunResponse{Run: wire}, workflowRPCError(err)
	case workflowRPCOperationGetRun, workflowRPCOperationGetRunBounded:
		var input workflowRunIDRequest
		if err := handler.decodeRequest(request, &input, &input.StoreID); err != nil {
			return nil, err
		}
		var run *Run
		var err error
		if request.Operation == workflowRPCOperationGetRunBounded {
			run, err = store.GetRunBounded(ctx, input.RunID, input.MaximumBytes)
		} else {
			run, err = store.GetRun(ctx, input.RunID)
		}
		if err != nil {
			return nil, workflowRPCError(err)
		}
		wire, err := encodeWorkflowRunWire(run)
		return workflowRunResponse{Run: wire}, workflowRPCError(err)
	case workflowRPCOperationListRuns:
		var input workflowTargetRequest
		if err := handler.decodeRequest(request, &input, &input.StoreID); err != nil {
			return nil, err
		}
		if input.Cursor < 0 || input.Cursor > maximumWorkflowRuns {
			return nil, database.NewError(database.CodeInvalid, "workflow list cursor is invalid")
		}
		runs, err := store.ListRuns(ctx)
		if err != nil {
			return nil, workflowRPCError(err)
		}
		if input.Cursor > len(runs) {
			return nil, database.NewError(database.CodeInvalid, "workflow list cursor is invalid")
		}
		response := workflowRunsResponse{
			Runs: make([]workflowRunWire, 0, min(workflowRPCPageItems, len(runs)-input.Cursor)),
		}
		pageBytes := 0
		index := input.Cursor
		for ; index < len(runs) && len(response.Runs) < workflowRPCPageItems; index++ {
			wire, encodeErr := encodeWorkflowRunWire(&runs[index])
			if encodeErr != nil {
				return nil, workflowRPCError(encodeErr)
			}
			itemBytes := len(wire.Document) + 64
			if itemBytes > workflowRPCPageBytes {
				return nil, database.NewError(database.CodeIntegrity, "workflow run exceeds list page limit")
			}
			if len(response.Runs) > 0 && pageBytes+itemBytes > workflowRPCPageBytes {
				break
			}
			response.Runs = append(response.Runs, wire)
			pageBytes += itemBytes
		}
		response.NextCursor = index
		response.Done = index == len(runs)
		return response, nil
	//nolint:dupl // Task and event pages use distinct bounded response element types.
	case workflowRPCOperationListHumanTasks:
		var input workflowRunIDRequest
		if err := handler.decodeRequest(request, &input, &input.StoreID); err != nil {
			return nil, err
		}
		if input.Cursor < 0 || input.Cursor > maximumWorkflowHumanTasksPerRun {
			return nil, database.NewError(database.CodeInvalid, "workflow task cursor is invalid")
		}
		tasks, err := store.ListHumanTasks(ctx, input.RunID)
		if err != nil {
			return nil, workflowRPCError(err)
		}
		if input.Cursor > len(tasks) {
			return nil, database.NewError(database.CodeInvalid, "workflow task cursor is invalid")
		}
		response := workflowTasksResponse{
			Tasks: make([]WorkflowHumanTask, 0, min(workflowRPCPageItems, len(tasks)-input.Cursor)),
		}
		pageBytes := 0
		index := input.Cursor
		for ; index < len(tasks) && len(response.Tasks) < workflowRPCPageItems; index++ {
			raw, encodeErr := database.MarshalCanonical(tasks[index])
			if encodeErr != nil || len(raw) > workflowRPCPageBytes {
				return nil, database.NewError(database.CodeIntegrity, "workflow task exceeds list page limit")
			}
			if len(response.Tasks) > 0 && pageBytes+len(raw) > workflowRPCPageBytes {
				break
			}
			response.Tasks = append(response.Tasks, tasks[index])
			pageBytes += len(raw)
		}
		response.NextCursor = index
		response.Done = index == len(tasks)
		return response, nil
	case workflowRPCOperationClaimHumanTask:
		var input workflowClaimHumanTaskRequest
		if err := handler.decodeRequest(request, &input, &input.StoreID); err != nil {
			return nil, err
		}
		input.Resume.resumeLease = input.ResumeLease
		input.Resume.maxConcurrent = input.MaxConcurrent
		run, task, duplicate, err := store.ClaimHumanTask(ctx, input.RunID, input.TaskID, input.Resume)
		if err != nil {
			return nil, workflowRPCError(err)
		}
		wire, err := encodeWorkflowRunWire(run)
		return workflowClaimResponse{Run: wire, Task: task, Duplicate: duplicate}, workflowRPCError(err)
	case workflowRPCOperationRenewHumanTaskClaim:
		var input workflowRenewHumanTaskRequest
		if err := handler.decodeRequest(request, &input, &input.StoreID); err != nil {
			return nil, err
		}
		err := store.RenewHumanTaskClaim(ctx, input.RunID, input.TaskID, input.Token, input.Lease)
		return workflowMutationResponse{Updated: err == nil}, workflowRPCError(err)
	case workflowRPCOperationCancelHumanTask:
		var input workflowCancelHumanTaskRequest
		if err := handler.decodeRequest(request, &input, &input.StoreID); err != nil {
			return nil, err
		}
		run, err := store.CancelHumanTask(ctx, input.RunID, input.TaskID, input.Reason)
		if err != nil {
			return nil, workflowRPCError(err)
		}
		wire, err := encodeWorkflowRunWire(run)
		return workflowRunResponse{Run: wire}, workflowRPCError(err)
	case workflowRPCOperationAppendEvent:
		var input workflowEventRequest
		if err := handler.decodeRequest(request, &input, &input.StoreID); err != nil {
			return nil, err
		}
		err := store.AppendEvent(ctx, input.Event)
		return workflowMutationResponse{Updated: err == nil}, workflowRPCError(err)
	//nolint:dupl // Task and event pages use distinct bounded response element types.
	case workflowRPCOperationEvents:
		var input workflowRunIDRequest
		if err := handler.decodeRequest(request, &input, &input.StoreID); err != nil {
			return nil, err
		}
		if input.Cursor < 0 || input.Cursor > maximumWorkflowEventsPerRun {
			return nil, database.NewError(database.CodeInvalid, "workflow event cursor is invalid")
		}
		events, err := store.Events(ctx, input.RunID)
		if err != nil {
			return nil, workflowRPCError(err)
		}
		if input.Cursor > len(events) {
			return nil, database.NewError(database.CodeInvalid, "workflow event cursor is invalid")
		}
		response := workflowEventsResponse{
			Events: make([]RunEvent, 0, min(workflowRPCPageItems, len(events)-input.Cursor)),
		}
		pageBytes := 0
		index := input.Cursor
		for ; index < len(events) && len(response.Events) < workflowRPCPageItems; index++ {
			raw, encodeErr := database.MarshalCanonical(events[index])
			if encodeErr != nil || len(raw) > workflowRPCPageBytes {
				return nil, database.NewError(database.CodeIntegrity, "workflow event exceeds list page limit")
			}
			if len(response.Events) > 0 && pageBytes+len(raw) > workflowRPCPageBytes {
				break
			}
			response.Events = append(response.Events, events[index])
			pageBytes += len(raw)
		}
		response.NextCursor = index
		response.Done = index == len(events)
		return response, nil
	case workflowRPCOperationDeleteRun:
		var input workflowRunIDRequest
		if err := handler.decodeRequest(request, &input, &input.StoreID); err != nil {
			return nil, err
		}
		err := store.DeleteRun(ctx, input.RunID)
		return workflowMutationResponse{Updated: err == nil}, workflowRPCError(err)
	case workflowRPCOperationPruneTerminalRuns:
		var input workflowPruneRequest
		if err := handler.decodeRequest(request, &input, &input.StoreID); err != nil {
			return nil, err
		}
		deleted, err := store.PruneTerminalRuns(ctx, input.OlderThan)
		return workflowPruneResponse{Deleted: deleted}, workflowRPCError(err)
	default:
		return nil, database.NewError(database.CodeUnsupported, "workflow broker operation is unsupported")
	}
}

// Close closes every broker-owned stable workflow pool. It must run before the
// server releases its online storage fence.
func (handler *BrokerHandler) Close() error {
	if handler == nil {
		return nil
	}
	handler.closeOnce.Do(func() {
		handler.mu.Lock()
		defer handler.mu.Unlock()
		handler.closed = true
		for _, workspace := range handler.workspaces {
			if workspace != nil && workspace.store != nil && workspace.store.database != nil {
				handler.closeErr = errors.Join(handler.closeErr, workspace.store.database.close())
			}
		}
	})
	return handler.closeErr
}

type configuredWorkflowWorkspace struct {
	workspace string
	selector  string
	storeID   database.StoreID
}

func configuredWorkflowWorkspaces(
	home string,
	cfg *config.Config,
	catalog *dbcatalog.Catalog,
) ([]configuredWorkflowWorkspace, error) {
	canonicalHome, err := database.CanonicalHome(home)
	if err != nil {
		return nil, err
	}
	primary, err := resolveConfiguredWorkflowWorkspace(canonicalHome, cfg.Agents.Defaults.Workspace)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		workspace string
		primary   bool
	}
	candidates := []candidate{{workspace: primary, primary: true}}
	for _, agent := range cfg.Agents.List {
		if strings.TrimSpace(agent.Workspace) == "" {
			continue
		}
		workspace, resolveErr := resolveConfiguredWorkflowWorkspace(canonicalHome, agent.Workspace)
		if resolveErr != nil {
			return nil, resolveErr
		}
		candidates = append(candidates, candidate{workspace: workspace})
	}
	seenWorkspaces := make(map[string]struct{}, len(candidates))
	seenSelectors := make(map[string]string, len(candidates))
	result := make([]configuredWorkflowWorkspace, 0, len(candidates))
	for _, item := range candidates {
		if _, duplicate := seenWorkspaces[item.workspace]; duplicate {
			continue
		}
		seenWorkspaces[item.workspace] = struct{}{}
		selector, selectorErr := workflowWorkspaceSelector(item.workspace)
		if selectorErr != nil {
			return nil, selectorErr
		}
		if previous, collision := seenSelectors[selector]; collision && previous != item.workspace {
			return nil, database.NewError(database.CodeIntegrity, "workflow workspace selector collides")
		}
		seenSelectors[selector] = item.workspace
		logicalName := workflowDefaultStoreName
		if !item.primary {
			logicalName = "workspace." + selector + ".workflows"
		}
		storeID, lookupErr := catalog.Lookup(logicalName)
		if lookupErr != nil {
			return nil, database.NewError(database.CodeIntegrity, "workflow store is missing from the catalog")
		}
		result = append(result, configuredWorkflowWorkspace{
			workspace: item.workspace,
			selector:  selector,
			storeID:   storeID,
		})
	}
	return result, nil
}

func resolveConfiguredWorkflowWorkspace(home, configured string) (string, error) {
	value := strings.TrimSpace(configured)
	if value == "" {
		value = filepath.Join(home, "workspace")
	} else if value == "~" || strings.HasPrefix(value, "~/") {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if value == "~" {
			value = userHome
		} else {
			value = filepath.Join(userHome, value[2:])
		}
	} else if !filepath.IsAbs(value) {
		value = filepath.Join(home, value)
	}
	resolved, err := canonicalWorkflowWorkspace(value)
	if err != nil {
		return "", database.NewError(database.CodeInvalid, "workflow workspace is invalid")
	}
	return resolved, nil
}

func workflowWorkspaceSelector(workspace string) (string, error) {
	value := strings.TrimSpace(workspace)
	if value == "" || strings.ContainsRune(value, 0) {
		return "", database.NewError(database.CodeInvalid, "workflow workspace is invalid")
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", database.NewError(database.CodeInvalid, "workflow workspace is invalid")
		}
		if value == "~" {
			value = userHome
		} else {
			value = filepath.Join(userHome, value[2:])
		}
	}
	if !filepath.IsAbs(value) {
		home, err := database.CanonicalHome(config.GetHome())
		if err != nil {
			return "", database.NewError(database.CodeInvalid, "workflow workspace is invalid")
		}
		value = filepath.Join(home, value)
	}
	canonical, err := canonicalWorkflowWorkspace(value)
	if err != nil {
		return "", database.NewError(database.CodeInvalid, "workflow workspace is invalid")
	}
	digest := sha256.Sum256([]byte(filepath.Clean(canonical)))
	return fmt.Sprintf("%x", digest[:8]), nil
}

func validWorkflowWorkspaceSelector(selector string) bool {
	if len(selector) != 16 {
		return false
	}
	for _, value := range selector {
		if (value < '0' || value > '9') && (value < 'a' || value > 'f') {
			return false
		}
	}
	return true
}

func workflowRequestStoreID(request database.Request) (database.StoreID, error) {
	var header struct {
		StoreID database.StoreID `json:"store_id"`
	}
	if json.Unmarshal(request.Payload, &header) != nil || !header.StoreID.Valid() {
		return "", database.NewError(database.CodeInvalid, "workflow broker request is invalid")
	}
	return header.StoreID, nil
}

var _ database.Handler = (*BrokerHandler)(nil)
