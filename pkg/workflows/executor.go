package workflows

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
)

const defaultMaxCallDepth = 4

var (
	// ErrRunAdmissionConflict is the fixed, private-safe boundary returned when
	// a durable-create admission fence no longer matches captured state.
	ErrRunAdmissionConflict = errors.New("workflow run admission conflict")
	// ErrRunAdmissionUnavailable is the fixed, private-safe boundary returned
	// when durable-create admission cannot verify its captured state.
	ErrRunAdmissionUnavailable = errors.New("workflow run admission unavailable")
)

type Executor struct {
	WorkspaceDir         string
	DefinitionsDir       string
	Store                RunStore
	Tools                ToolRunner
	Agents               AgentRunner
	Functions            FunctionRunner
	RuntimeEvents        RuntimeEventPublisher
	RuntimeCompatibility RuntimeCompatibility
	MaxCallDepth         int
	MaxConcurrentRuns    int
	DefaultTimeout       time.Duration
	// WorkflowSnapshots is an immutable, pre-admitted reusable closure. When
	// present, the executor uses these exact parsed bytes instead of reloading
	// definitions after admission.
	WorkflowSnapshots map[string]*LocalWorkflowSnapshot
	// verifyCapturedSnapshots marks snapshots captured internally by the
	// executor's universal closure admission. Unlike externally admitted
	// snapshots, their compatibility stamps have not already been fenced at
	// durable create, so reusable calls verify each captured content revision.
	verifyCapturedSnapshots bool
	// capturedReusableErrors freezes a child-load/compatibility failure found
	// during universal closure admission. Keeping the exact failure prevents a
	// mutable definition from becoming executable after parent effects while
	// preserving the established child-call failure boundary.
	capturedReusableErrors   map[string]error
	humanTaskClosureAdmitted bool
	// AdmittedRunCreate wraps the durable create for a top-level run or an
	// explicit retry (including a reusable child retry). The wrapper can hold
	// admission locks and revalidate immutable snapshots through the create
	// itself, closing the final check/create TOCTOU window.
	AdmittedRunCreate func(
		context.Context,
		*Run,
		func() error,
	) error
	// AdmittedHumanTaskClaim wraps the atomic response claim so callers can
	// fence mutable runtime policy through the durable waiting-to-running
	// transition without retaining admission locks during continuation.
	AdmittedHumanTaskClaim func(
		context.Context,
		string,
		string,
		func() (*Run, WorkflowHumanTask, bool, error),
	) (*Run, WorkflowHumanTask, bool, error)
}

type RuntimeEventPublisher interface {
	PublishNonBlocking(evt runtimeevents.Event) runtimeevents.PublishResult
}

type limitedRunCreator interface {
	CreateRunIfUnderLimit(ctx context.Context, run *Run, maxConcurrent int) error
}

type RunRequest struct {
	RunID        string
	Ref          string
	Inputs       map[string]any
	Secrets      map[string]string
	Event        map[string]any
	Origin       *RunOrigin
	Session      string
	Delivery     Delivery
	ParentRunID  string
	CallerJobID  string
	Workflow     *Workflow
	WorkflowRef  string
	RetryOfRunID string
	CallDepth    int
	// PrivateRoot is accepted only for the trusted in-memory workflow emitted
	// by CompileGateWorkflow. It is frozen before durable creation and omitted
	// from all ordinary run observations.
	PrivateRoot *PrivateRootRequest
	// frozenPrivateRoot is used only by retry. External callers cannot bypass
	// capture or inject a previously frozen capability.
	frozenPrivateRoot *frozenWorkflowRootContext
	// OnRunPersisted runs after the durable run record is created and before
	// lifecycle publication, user callbacks, or workflow side effects. An
	// error fails the new run without executing the workflow.
	OnRunPersisted func(*Run) error
	OnRunCreated   func(*Run)
}

type RunResult struct {
	RunID   string         `json:"run_id"`
	Status  string         `json:"status"`
	Outputs map[string]any `json:"outputs,omitempty"`
	Error   string         `json:"error,omitempty"`
}

func (e *Executor) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	if e == nil {
		return nil, fmt.Errorf("workflow executor is nil")
	}
	// Reject a mixed private invocation before recursively cloning caller-owned
	// public maps. A cyclic public graph must not reach generic cloning when the
	// private contract rejects that graph without inspecting it.
	if privateErr := validatePrivateWorkflowInvocationEnvelope(req); privateErr != nil {
		return nil, privateErr
	}
	// Freeze caller-owned request graphs before admission hooks or background
	// execution can observe a later mutation.
	var cloneErr error
	req, cloneErr = cloneRunRequestForExecution(req)
	if cloneErr != nil {
		return nil, cloneErr
	}
	if req.PrivateRoot != nil {
		workflowSnapshot, snapshotErr := captureInitialPrivateWorkflow(req.Workflow)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		req.Workflow = workflowSnapshot
	}
	maxDepth := e.MaxCallDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxCallDepth
	}
	if req.CallDepth > maxDepth {
		return nil, fmt.Errorf("workflow call depth exceeded")
	}
	if e.DefaultTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.DefaultTimeout)
		defer cancel()
	}
	workflow, workflowRef, loadErr := e.loadWorkflow(ctx, req)
	if loadErr != nil {
		return nil, loadErr
	}
	if privateErr := validatePrivateWorkflowAdmission(workflow, req); privateErr != nil {
		return nil, privateErr
	}
	if validateErr := Validate(workflow); validateErr != nil {
		if req.PrivateRoot != nil || req.frozenPrivateRoot != nil {
			return nil, ErrPrivateWorkflowContext
		}
		return nil, validateErr
	}
	admittedExecutor, admissionErr := e.admitHumanTaskClosure(
		ctx,
		workflowRef,
		workflow,
		maxDepth,
	)
	if admissionErr != nil {
		return nil, admissionErr
	}
	e = admittedExecutor
	var privateRoot *frozenWorkflowRootContext
	if req.PrivateRoot != nil {
		privateRoot, admissionErr = freezeWorkflowPrivateRoot(ctx, e.Agents, req.PrivateRoot)
		if admissionErr != nil {
			return nil, admissionErr
		}
	} else if req.frozenPrivateRoot != nil {
		privateRoot = cloneFrozenWorkflowRootContext(req.frozenPrivateRoot)
	}
	if privateRoot != nil {
		if privateErr := validatePrivateRootForWorkflow(workflow, privateRoot); privateErr != nil {
			return nil, privateErr
		}
		// The unresolved local session reference has served its only purpose.
		req.PrivateRoot = nil
		req.frozenPrivateRoot = nil
	}
	if strings.TrimSpace(req.ParentRunID) != "" && workflowContainsHumanTask(workflow) {
		return nil, fmt.Errorf("%w: human/task cannot run inside a reusable workflow call", ErrHumanTaskUnsupported)
	}
	var execution *workflowExecutionState
	if workflowContainsHumanTask(workflow) {
		var executionErr error
		execution, executionErr = newWorkflowExecutionState(workflow)
		if executionErr != nil {
			return nil, executionErr
		}
		if normalizedValidationErr := Validate(execution.Workflow); normalizedValidationErr != nil {
			if privateRoot != nil {
				return nil, ErrPrivateWorkflowContext
			}
			return nil, normalizedValidationErr
		}
		// Execute the same immutable JSON-normalized definition that is
		// persisted for continuation. Programmatic callers therefore cannot
		// mutate or supply typed nested schema shapes that differ between the
		// initial segment and a resumed segment.
		workflow = execution.Workflow
	}
	store := e.Store
	if store == nil {
		store = NewFileRunStore(e.WorkspaceDir)
	}
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		runID = NewRunID()
	}
	origin, originErr := normalizeRunOrigin(
		req.Origin,
		runID,
		req.ParentRunID,
		req.RetryOfRunID,
		req.Event,
		req.Inputs,
	)
	if originErr != nil {
		return nil, originErr
	}
	req.Origin = origin
	now := time.Now().UTC()
	run := &Run{
		ID:           runID,
		WorkflowRef:  workflowRef,
		Status:       RunStatusRunning,
		Origin:       cloneRunOrigin(origin),
		ParentRunID:  req.ParentRunID,
		CallerJobID:  req.CallerJobID,
		RetryOfRunID: req.RetryOfRunID,
		Session:      strings.TrimSpace(req.Session),
		Delivery:     cloneDelivery(req.Delivery),
		Event:        cloneMap(req.Event),
		Inputs:       cloneMap(req.Inputs),
		Outputs:      make(map[string]any),
		Jobs:         make(map[string]JobExecution),
		Steps:        make(map[string]StepExecution),
		CreatedAt:    now,
		UpdatedAt:    now,
		execution:    execution,
		humanTasks:   make(map[string]WorkflowHumanTask),
		privateRoot:  cloneFrozenWorkflowRootContext(privateRoot),
	}
	if privateRoot != nil {
		run.ContextVisibility = WorkflowContextVisibilityPrivate
		if bindErr := bindPrivateWorkflowRun(run); bindErr != nil {
			return nil, ErrPrivateWorkflowContext
		}
	}
	topLevel := req.ParentRunID == ""
	admittedRun := topLevel || strings.TrimSpace(req.RetryOfRunID) != ""
	create := func() error {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return e.createRun(ctx, store, run, topLevel)
	}
	var createErr error
	if admittedRun && e.AdmittedRunCreate != nil {
		createErr = e.AdmittedRunCreate(ctx, cloneRun(run), create)
	} else {
		createErr = create()
	}
	if createErr != nil {
		return sanitizePrivateRunOutcome(run, nil, createErr)
	}
	if req.OnRunPersisted != nil {
		if persistedErr := req.OnRunPersisted(cloneRun(run)); persistedErr != nil {
			completedAt := time.Now().UTC()
			run.Status = RunStatusFailed
			run.Error = fmt.Sprintf("run persistence callback failed: %v", persistedErr)
			run.CompletedAt = &completedAt
			run.UpdatedAt = completedAt
			_ = store.UpdateRun(context.Background(), run)
			e.appendEvent(
				context.Background(),
				store,
				RunEvent{Kind: "workflow.run.failed", RunID: run.ID, Message: run.Error},
			)
			return sanitizePrivateRunOutcome(run, &RunResult{
				RunID:  run.ID,
				Status: RunStatusFailed,
				Error:  run.Error,
			}, fmt.Errorf("run persistence callback: %w", persistedErr))
		}
	}
	e.appendEvent(
		ctx,
		store,
		RunEvent{Kind: "workflow.run.start", RunID: run.ID, Payload: map[string]any{"workflow_ref": workflowRef}},
	)
	if req.OnRunCreated != nil {
		req.OnRunCreated(cloneRun(run))
	}

	outputs, runErr := e.executeWorkflow(ctx, store, run, workflow, req, nil)
	if runErr != nil {
		if errors.Is(runErr, ErrHumanTaskConflict) ||
			errors.Is(context.Cause(ctx), ErrHumanTaskConflict) {
			return nil, ErrHumanTaskConflict
		}
		var waiting workflowWaitingError
		if errors.As(runErr, &waiting) {
			return e.persistWorkflowWait(store, run, outputs)
		}
		completedAt := time.Now().UTC()
		if errors.Is(runErr, ErrRunCanceled) {
			run.Status = RunStatusCanceled
			if run.CancelRequestedAt == nil {
				run.CancelRequestedAt = &completedAt
			}
			if run.CancelReason == "" {
				run.CancelReason = runErr.Error()
			}
		} else {
			run.Status = RunStatusFailed
		}
		run.Error = runErr.Error()
		run.Outputs = outputs
		run.CompletedAt = &completedAt
		_ = store.UpdateRun(ctx, run)
		if run.Status != RunStatusFailed && run.Status != RunStatusCanceled {
			return sanitizePrivateRunOutcome(run, &RunResult{
				RunID:   run.ID,
				Status:  run.Status,
				Outputs: run.Outputs,
				Error:   run.Error,
			}, nil)
		}
		if run.Status == RunStatusCanceled {
			e.appendEvent(
				context.Background(),
				store,
				RunEvent{Kind: "workflow.run.canceled", RunID: run.ID, Message: runErr.Error()},
			)
		} else {
			e.appendEvent(
				context.Background(),
				store,
				RunEvent{Kind: "workflow.run.failed", RunID: run.ID, Message: runErr.Error()},
			)
		}
		return sanitizePrivateRunOutcome(
			run,
			&RunResult{RunID: run.ID, Status: run.Status, Outputs: outputs, Error: run.Error},
			runErr,
		)
	}
	if cancelErr := checkRunCanceled(ctx, store, run); cancelErr != nil {
		completedAt := time.Now().UTC()
		run.Status = RunStatusCanceled
		run.Error = cancelErr.Error()
		run.CompletedAt = &completedAt
		if updateErr := store.UpdateRun(context.Background(), run); updateErr != nil {
			if errors.Is(updateErr, ErrHumanTaskConflict) {
				return nil, ErrHumanTaskConflict
			}
			return sanitizePrivateRunOutcome(run, nil, updateErr)
		}
		e.appendEvent(
			context.Background(),
			store,
			RunEvent{Kind: "workflow.run.canceled", RunID: run.ID, Message: cancelErr.Error()},
		)
		return sanitizePrivateRunOutcome(
			run,
			&RunResult{RunID: run.ID, Status: run.Status, Outputs: outputs, Error: run.Error},
			cancelErr,
		)
	}
	run.Status = RunStatusSucceeded
	run.Outputs = outputs
	now = time.Now().UTC()
	run.CompletedAt = &now
	if updateErr := store.UpdateRun(ctx, run); updateErr != nil {
		return sanitizePrivateRunOutcome(run, nil, updateErr)
	}
	if run.Status == RunStatusCanceled {
		reason := strings.TrimSpace(run.CancelReason)
		if reason == "" {
			reason = "cancel requested"
		}
		cancelErr := fmt.Errorf("%w: %s", ErrRunCanceled, reason)
		e.publishCanceledRuntimeEvent(context.Background(), store, run, cancelErr.Error())
		return sanitizePrivateRunOutcome(run, &RunResult{
			RunID:   run.ID,
			Status:  run.Status,
			Outputs: run.Outputs,
			Error:   cancelErr.Error(),
		}, cancelErr)
	}
	if run.Status != RunStatusSucceeded {
		return sanitizePrivateRunOutcome(run, &RunResult{
			RunID:   run.ID,
			Status:  run.Status,
			Outputs: run.Outputs,
			Error:   run.Error,
		}, nil)
	}
	e.appendEvent(
		ctx,
		store,
		RunEvent{Kind: "workflow.run.end", RunID: run.ID, Payload: map[string]any{"outputs": outputs}},
	)
	return sanitizePrivateRunOutcome(
		run,
		&RunResult{RunID: run.ID, Status: run.Status, Outputs: outputs},
		nil,
	)
}

func (e *Executor) createRun(ctx context.Context, store RunStore, run *Run, topLevel bool) error {
	if topLevel && e.MaxConcurrentRuns > 0 {
		if limited, ok := store.(limitedRunCreator); ok {
			return limited.CreateRunIfUnderLimit(ctx, run, e.MaxConcurrentRuns)
		}
		if limitErr := e.enforceConcurrency(ctx, store); limitErr != nil {
			return limitErr
		}
	}
	return store.CreateRun(ctx, run)
}

func (e *Executor) Retry(ctx context.Context, runID string, secrets map[string]string) (*RunResult, error) {
	if e == nil {
		return nil, fmt.Errorf("workflow executor is nil")
	}
	store := e.Store
	if store == nil {
		store = NewFileRunStore(e.WorkspaceDir)
	}
	run, err := store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	return e.RetryCaptured(ctx, run, secrets)
}

// RetryCaptured retries one already-loaded immutable source run. Admission
// callers use this method so the authoritative workflow ref and invocation
// context cannot be replaced by a second store read after readiness checks.
func (e *Executor) RetryCaptured(
	ctx context.Context,
	source *Run,
	secrets map[string]string,
) (*RunResult, error) {
	if e == nil {
		return nil, fmt.Errorf("workflow executor is nil")
	}
	if source == nil {
		return nil, fmt.Errorf("workflow retry source is required")
	}
	secrets = cloneStringMap(secrets)
	store := e.Store
	if store == nil {
		store = NewFileRunStore(e.WorkspaceDir)
	}
	if IsPrivateWorkflowRun(source) {
		if len(secrets) != 0 {
			return nil, ErrPrivateWorkflowContext
		}
		// Validate the private root and reject prohibited public context before
		// generic Run cloning traverses any caller-owned map.
		if err := validateRunPrivateContext(source); err != nil {
			return nil, ErrPrivateWorkflowContext
		}
		if source.execution == nil || source.execution.Workflow == nil ||
			validatePersistedWorkflowDefinition(source.execution) != nil {
			return nil, ErrPrivateWorkflowContext
		}
		return e.Run(ctx, RunRequest{
			Workflow:          source.execution.Workflow,
			WorkflowRef:       source.WorkflowRef,
			RetryOfRunID:      source.ID,
			frozenPrivateRoot: cloneFrozenWorkflowRootContext(source.privateRoot),
		})
	}
	run := cloneRun(source)
	origin, _ := trustedRunOriginWithStore(ctx, store, run)
	return e.Run(ctx, RunRequest{
		Ref:          run.WorkflowRef,
		Inputs:       cloneMap(run.Inputs),
		Secrets:      cloneStringMap(secrets),
		Event:        cloneMap(run.Event),
		Origin:       origin,
		Session:      run.Session,
		Delivery:     run.Delivery,
		ParentRunID:  run.ParentRunID,
		CallerJobID:  run.CallerJobID,
		RetryOfRunID: run.ID,
	})
}

// ResumeHumanTask atomically records one schema-valid human response and
// continues the exact workflow snapshot persisted with the suspended run.
func (e *Executor) ResumeHumanTask(
	ctx context.Context,
	runID string,
	taskID string,
	response HumanTaskResumeRequest,
) (*RunResult, error) {
	if e == nil {
		return nil, fmt.Errorf("workflow executor is nil")
	}
	response.Secrets = cloneStringMap(response.Secrets)
	store := e.Store
	if store == nil {
		store = NewFileRunStore(e.WorkspaceDir)
	}
	taskStore, ok := store.(humanTaskStore)
	if !ok {
		return nil, ErrHumanTaskUnsupported
	}
	if len(response.Secrets) != 0 {
		candidate, readErr := store.GetRun(ctx, runID)
		if readErr != nil {
			return nil, readErr
		}
		if IsPrivateWorkflowRun(candidate) {
			return nil, ErrPrivateWorkflowContext
		}
	}
	response.resumeLease = humanTaskResumeLease(e.DefaultTimeout)
	response.maxConcurrent = e.MaxConcurrentRuns
	claim := func() (*Run, WorkflowHumanTask, bool, error) {
		return taskStore.ClaimHumanTask(ctx, runID, taskID, response)
	}
	var run *Run
	var task WorkflowHumanTask
	var duplicate bool
	var err error
	if e.AdmittedHumanTaskClaim != nil {
		run, task, duplicate, err = e.AdmittedHumanTaskClaim(
			ctx,
			runID,
			taskID,
			claim,
		)
	} else {
		run, task, duplicate, err = claim()
	}
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, ErrHumanTaskConflict
	}
	if IsPrivateWorkflowRun(run) {
		if privateErr := validateRunPrivateContext(run); privateErr != nil {
			return nil, ErrPrivateWorkflowContext
		}
		if len(response.Secrets) != 0 {
			return nil, ErrPrivateWorkflowContext
		}
	}
	if duplicate {
		result := &RunResult{RunID: run.ID, Status: run.Status}
		if !IsPrivateWorkflowRun(run) {
			result.Outputs = cloneMap(run.Outputs)
			result.Error = run.Error
		}
		return sanitizePrivateRunOutcome(run, result, nil)
	}
	if run.execution == nil || run.execution.Workflow == nil || run.execution.Cursor == nil {
		return nil, ErrHumanTaskConflict
	}
	// The response is durable once ClaimHumanTask succeeds. A browser or API
	// client disconnect must not turn that accepted answer into a canceled run;
	// explicit workflow cancellation remains observable through the run store.
	ctx = context.WithoutCancel(ctx)
	workflow := run.execution.Workflow
	if err := Validate(workflow); err != nil {
		return nil, fmt.Errorf("%w: persisted workflow snapshot is invalid", ErrHumanTaskConflict)
	}
	if e.DefaultTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.DefaultTimeout)
		defer cancel()
	}
	ctx, cancelClaimContinuation := context.WithCancelCause(ctx)
	defer cancelClaimContinuation(nil)
	stopClaimHeartbeat := startHumanTaskClaimHeartbeat(
		taskStore,
		run.ID,
		task.ID,
		run.execution.Resume.Token,
		response.resumeLease,
		run.execution.Resume.ExpiresAt,
		cancelClaimContinuation,
	)
	defer stopClaimHeartbeat()
	e.appendEvent(
		ctx,
		store,
		RunEvent{
			Kind:   "workflow.human_task.answered",
			RunID:  run.ID,
			JobID:  task.JobID,
			StepID: task.StepID,
			Payload: map[string]any{
				"task_id":     task.ID,
				"input_hash":  task.InputHash,
				"response_id": task.ResponseID,
			},
		},
	)
	cursor := *run.execution.Cursor
	runReq := RunRequest{
		Inputs:   cloneMap(run.Inputs),
		Secrets:  cloneStringMap(response.Secrets),
		Event:    cloneMap(run.Event),
		Session:  run.Session,
		Delivery: run.Delivery,
	}
	outputs, runErr := e.executeWorkflow(ctx, store, run, workflow, runReq, &cursor)
	if errors.Is(context.Cause(ctx), ErrHumanTaskConflict) {
		return nil, ErrHumanTaskConflict
	}
	if runErr != nil {
		if errors.Is(runErr, ErrHumanTaskConflict) ||
			errors.Is(context.Cause(ctx), ErrHumanTaskConflict) {
			return nil, ErrHumanTaskConflict
		}
		var waiting workflowWaitingError
		if errors.As(runErr, &waiting) {
			return e.persistWorkflowWait(store, run, outputs)
		}
		completedAt := time.Now().UTC()
		if errors.Is(runErr, ErrRunCanceled) {
			run.Status = RunStatusCanceled
			if run.CancelRequestedAt == nil {
				run.CancelRequestedAt = &completedAt
			}
			if run.CancelReason == "" {
				run.CancelReason = runErr.Error()
			}
		} else {
			run.Status = RunStatusFailed
		}
		run.Error = runErr.Error()
		run.Outputs = outputs
		run.CompletedAt = &completedAt
		if updateErr := store.UpdateRun(context.Background(), run); updateErr != nil {
			if errors.Is(updateErr, ErrHumanTaskConflict) {
				return nil, ErrHumanTaskConflict
			}
			return sanitizePrivateRunOutcome(run, nil, updateErr)
		}
		kind := "workflow.run.failed"
		if run.Status == RunStatusCanceled {
			kind = "workflow.run.canceled"
		}
		e.appendEvent(context.Background(), store, RunEvent{Kind: kind, RunID: run.ID, Message: runErr.Error()})
		return sanitizePrivateRunOutcome(
			run,
			&RunResult{RunID: run.ID, Status: run.Status, Outputs: outputs, Error: run.Error},
			runErr,
		)
	}
	if cancelErr := checkRunCanceled(ctx, store, run); cancelErr != nil {
		if errors.Is(cancelErr, ErrHumanTaskConflict) {
			return nil, ErrHumanTaskConflict
		}
		completedAt := time.Now().UTC()
		run.Status = RunStatusCanceled
		run.Error = cancelErr.Error()
		run.CompletedAt = &completedAt
		if updateErr := store.UpdateRun(context.Background(), run); updateErr != nil {
			if errors.Is(updateErr, ErrHumanTaskConflict) {
				return nil, ErrHumanTaskConflict
			}
			return sanitizePrivateRunOutcome(run, nil, updateErr)
		}
		return sanitizePrivateRunOutcome(
			run,
			&RunResult{RunID: run.ID, Status: run.Status, Outputs: outputs, Error: run.Error},
			cancelErr,
		)
	}
	run.Status = RunStatusSucceeded
	run.Outputs = outputs
	completedAt := time.Now().UTC()
	run.CompletedAt = &completedAt
	if err := store.UpdateRun(ctx, run); err != nil {
		return sanitizePrivateRunOutcome(run, nil, err)
	}
	if run.Status != RunStatusSucceeded {
		return sanitizePrivateRunOutcome(run, &RunResult{
			RunID: run.ID, Status: run.Status, Outputs: run.Outputs, Error: run.Error,
		}, nil)
	}
	e.appendEvent(ctx, store, RunEvent{
		Kind: "workflow.run.end", RunID: run.ID, Payload: map[string]any{"outputs": outputs},
	})
	return sanitizePrivateRunOutcome(
		run,
		&RunResult{RunID: run.ID, Status: run.Status, Outputs: outputs},
		nil,
	)
}

func startHumanTaskClaimHeartbeat(
	store humanTaskStore,
	runID string,
	taskID string,
	token string,
	lease time.Duration,
	expiresAt time.Time,
	loseClaim context.CancelCauseFunc,
) func() {
	heartbeatCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if expiresAt.IsZero() {
			expiresAt = time.Now().UTC().Add(lease)
		}
		ticker := time.NewTicker(humanTaskHeartbeatInterval(lease))
		defer ticker.Stop()
		timer := time.NewTimer(time.Until(expiresAt))
		defer timer.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-timer.C:
				if loseClaim != nil {
					loseClaim(ErrHumanTaskConflict)
				}
				return
			case <-ticker.C:
				renewedAt := time.Now().UTC()
				if err := store.RenewHumanTaskClaim(
					heartbeatCtx,
					runID,
					taskID,
					token,
					lease,
				); err != nil {
					if errors.Is(err, ErrHumanTaskConflict) ||
						errors.Is(err, ErrHumanTaskNotFound) ||
						heartbeatCtx.Err() != nil {
						if heartbeatCtx.Err() == nil && loseClaim != nil {
							loseClaim(ErrHumanTaskConflict)
						}
						return
					}
					continue
				}
				expiresAt = renewedAt.Add(lease)
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(time.Until(expiresAt))
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// CancelHumanTask cancels the containing run when the named task is still
// waiting. Answered or stale tasks are rejected rather than rewriting history.
func (e *Executor) CancelHumanTask(
	ctx context.Context,
	runID string,
	taskID string,
	reason string,
) (*Run, error) {
	if e == nil {
		return nil, fmt.Errorf("workflow executor is nil")
	}
	store := e.Store
	if store == nil {
		store = NewFileRunStore(e.WorkspaceDir)
	}
	taskStore, ok := store.(humanTaskStore)
	if !ok {
		return nil, ErrHumanTaskUnsupported
	}
	run, err := taskStore.CancelHumanTask(ctx, runID, taskID, reason)
	if err != nil {
		if IsPrivateWorkflowRun(run) {
			_, err = sanitizePrivateRunOutcome(run, nil, err)
			return nil, err
		}
		return run, err
	}
	if run != nil && run.Status == RunStatusCanceled {
		e.publishCanceledRuntimeEvent(ctx, store, run, run.CancelReason)
	}
	if IsPrivateWorkflowRun(run) {
		return projectPrivateWorkflowRunForBrowser(run), nil
	}
	return run, nil
}

func (e *Executor) persistWorkflowWait(
	store RunStore,
	run *Run,
	outputs map[string]any,
) (*RunResult, error) {
	if run == nil || run.execution == nil || run.execution.Workflow == nil ||
		run.execution.Cursor == nil {
		return nil, ErrHumanTaskConflict
	}
	cursor := run.execution.Cursor
	job, exists := run.execution.Workflow.Jobs[cursor.JobID]
	if !exists || cursor.StepIndex < 0 || cursor.StepIndex >= len(job.Steps) {
		return nil, ErrHumanTaskConflict
	}
	stepID := strings.TrimSpace(job.Steps[cursor.StepIndex].ID)
	if stepID == "" {
		stepID = fmt.Sprintf("step_%d", cursor.StepIndex+1)
	}
	taskID := humanTaskID(run.ID, cursor.JobID, stepID)
	task, exists := run.humanTasks[taskID]
	if !exists || task.Status != HumanTaskStatusWaiting {
		return nil, ErrHumanTaskConflict
	}

	// Publish the task, cursor, waiting step/job, and claimable run status in
	// one run.json replacement. Until this write completes, a concurrent task
	// API call can observe only the preceding running checkpoint.
	run.Status = RunStatusWaiting
	run.Error = ""
	run.Outputs = outputs
	run.CompletedAt = nil
	if err := store.UpdateRun(context.Background(), run); err != nil {
		return sanitizePrivateRunOutcome(run, nil, err)
	}
	if run.Status != RunStatusWaiting {
		result := &RunResult{
			RunID:   run.ID,
			Status:  run.Status,
			Outputs: cloneMap(run.Outputs),
			Error:   run.Error,
		}
		if run.Status == RunStatusCanceled {
			reason := strings.TrimSpace(run.CancelReason)
			if reason == "" {
				reason = "cancel requested"
			}
			return sanitizePrivateRunOutcome(
				run,
				result,
				fmt.Errorf("%w: %s", ErrRunCanceled, reason),
			)
		}
		return sanitizePrivateRunOutcome(run, result, ErrHumanTaskConflict)
	}
	e.appendEvent(
		context.Background(),
		store,
		RunEvent{
			Kind:   "workflow.human_task.waiting",
			RunID:  run.ID,
			JobID:  task.JobID,
			StepID: task.StepID,
			Payload: map[string]any{
				"task_id":    task.ID,
				"input_hash": task.InputHash,
			},
		},
	)
	e.appendEvent(
		context.Background(),
		store,
		RunEvent{Kind: "workflow.run.waiting", RunID: run.ID},
	)
	return sanitizePrivateRunOutcome(run, &RunResult{
		RunID:   run.ID,
		Status:  RunStatusWaiting,
		Outputs: cloneMap(outputs),
	}, nil)
}

func (e *Executor) CancelRun(ctx context.Context, runID, reason string) (*Run, error) {
	if e == nil {
		return nil, fmt.Errorf("workflow executor is nil")
	}
	store := e.Store
	if store == nil {
		store = NewFileRunStore(e.WorkspaceDir)
	}
	previous, _ := store.GetRun(ctx, runID)
	run, err := store.CancelRun(ctx, runID, reason)
	if err != nil {
		privateRun := run
		if privateRun == nil {
			privateRun = previous
		}
		if IsPrivateWorkflowRun(privateRun) {
			_, err = sanitizePrivateRunOutcome(privateRun, nil, err)
		}
		return nil, err
	}
	if run != nil && run.Status == RunStatusCanceled && (previous == nil || !isTerminalRunStatus(previous.Status)) {
		e.publishCanceledRuntimeEvent(ctx, store, run, run.CancelReason)
	}
	if IsPrivateWorkflowRun(run) {
		return projectPrivateWorkflowRunForBrowser(run), nil
	}
	return run, nil
}

func (e *Executor) enforceConcurrency(ctx context.Context, store RunStore) error {
	if e.MaxConcurrentRuns <= 0 || store == nil {
		return nil
	}
	runs, err := store.ListRuns(ctx)
	if err != nil {
		return err
	}
	running := 0
	for _, run := range runs {
		if run.Status == RunStatusRunning && run.ParentRunID == "" {
			running++
		}
	}
	if running >= e.MaxConcurrentRuns {
		return fmt.Errorf(
			"%w: %d running, max %d",
			ErrRunConcurrencyLimit,
			running,
			e.MaxConcurrentRuns,
		)
	}
	return nil
}

func checkRunCanceled(ctx context.Context, store RunStore, run *Run) error {
	if errors.Is(context.Cause(ctx), ErrHumanTaskConflict) {
		return ErrHumanTaskConflict
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return fmt.Errorf("%w: context canceled", ErrRunCanceled)
		}
		return err
	}
	if store == nil || run == nil || strings.TrimSpace(run.ID) == "" {
		return nil
	}
	latest, _ := store.GetRun(ctx, run.ID)
	if latest == nil {
		return nil
	}
	if latest.Status == RunStatusCanceled {
		reason := strings.TrimSpace(latest.CancelReason)
		if reason == "" {
			reason = "cancel requested"
		}
		run.Status = RunStatusCanceled
		run.CancelReason = reason
		run.CancelRequestedAt = latest.CancelRequestedAt
		return fmt.Errorf("%w: %s", ErrRunCanceled, reason)
	}
	if strings.TrimSpace(latest.ParentRunID) != "" {
		parent, _ := store.GetRun(ctx, latest.ParentRunID)
		if parent != nil && parent.Status == RunStatusCanceled {
			reason := strings.TrimSpace(parent.CancelReason)
			if reason == "" {
				reason = "parent run canceled"
			}
			run.Status = RunStatusCanceled
			run.CancelReason = reason
			run.CancelRequestedAt = parent.CancelRequestedAt
			return fmt.Errorf("%w: parent run %s canceled: %s", ErrRunCanceled, parent.ID, reason)
		}
	}
	return nil
}

func (e *Executor) loadWorkflow(
	ctx context.Context,
	req RunRequest,
) (*Workflow, string, error) {
	if req.Workflow != nil {
		ref := strings.TrimSpace(req.WorkflowRef)
		if ref == "" {
			ref = strings.TrimSpace(req.Ref)
		}
		if ref == "" {
			ref = "inline"
		}
		return req.Workflow, ref, nil
	}
	if canonical, err := CanonicalLocalRef(req.Ref); err == nil {
		if capturedErr := e.capturedReusableErrors[canonical]; capturedErr != nil {
			return nil, "", capturedErr
		}
	}
	if len(e.WorkflowSnapshots) != 0 {
		if snapshot, canonical, ok := e.workflowSnapshot(req.Ref); ok {
			return snapshot.Workflow, canonical, nil
		}
		return nil, "", fmt.Errorf(
			"workflow %q is outside the admitted snapshot closure",
			req.Ref,
		)
	}
	if runtimeCompatibilityConfigured(e.RuntimeCompatibility) {
		workflow, err := LoadRunnableLocalSnapshot(
			ctx,
			e.WorkspaceDir,
			req.Ref,
			e.RuntimeCompatibility,
			e.localOptions()...,
		)
		if err != nil {
			return nil, "", err
		}
		canonical, err := CanonicalLocalRef(req.Ref)
		if err != nil {
			return nil, "", err
		}
		return workflow, canonical, nil
	}
	resolved, err := (Resolver{WorkspaceDir: e.WorkspaceDir, DefinitionsDir: e.DefinitionsDir}).ResolveLocal(req.Ref)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(resolved.Path)
	if err != nil {
		return nil, "", err
	}
	workflow, err := Parse(data)
	if err != nil {
		return nil, "", err
	}
	return workflow, resolved.Canonical, nil
}

type executorWorkflowSnapshotLoader struct {
	executor       *Executor
	snapshots      map[string]*LocalWorkflowSnapshot
	capturedErrors map[string]error
	capture        bool
	bytesRead      int64
	firstError     error
}

func (l *executorWorkflowSnapshotLoader) LoadReusableWorkflow(
	ctx context.Context,
	ref string,
) (*Workflow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	canonical, err := CanonicalLocalRef(ref)
	if err != nil {
		l.rememberError(err)
		return nil, err
	}
	if snapshot, ok := l.snapshots[canonical]; ok && snapshot != nil &&
		snapshot.Workflow != nil && snapshot.Ref == canonical {
		return snapshot.Workflow, nil
	}
	if !l.capture {
		err = fmt.Errorf(
			"workflow %q is outside the admitted snapshot closure",
			canonical,
		)
		l.rememberError(err)
		return nil, err
	}
	resolved, err := (Resolver{
		WorkspaceDir:   l.executor.WorkspaceDir,
		DefinitionsDir: l.executor.DefinitionsDir,
	}).ResolveLocal(canonical)
	if err != nil {
		safeErr := fmt.Errorf("workflow dependency %q is unavailable", canonical)
		l.rememberLoadError(canonical, safeErr)
		return nil, safeErr
	}
	remaining := MaxWorkflowDependencyTotalBytes - l.bytesRead
	data, err := readExecutorWorkflowDependency(resolved.Path, remaining)
	if err != nil {
		if errors.Is(err, ErrWorkflowDependencyAnalysisLimitExceeded) {
			l.rememberError(ErrWorkflowDependencyAnalysisLimitExceeded)
			return nil, ErrWorkflowDependencyAnalysisLimitExceeded
		}
		safeErr := fmt.Errorf("workflow dependency %q is unavailable", canonical)
		l.rememberLoadError(canonical, safeErr)
		return nil, safeErr
	}
	l.bytesRead += int64(len(data))
	revision := workflowHashBytes(data)
	if runtimeCompatibilityConfigured(l.executor.RuntimeCompatibility) {
		if compatibilityErr := ensureWorkflowHashRunnable(
			l.executor.WorkspaceDir,
			canonical,
			NormalizeRuntimeCompatibility(l.executor.RuntimeCompatibility),
			revision,
		); compatibilityErr != nil {
			l.rememberLoadError(canonical, compatibilityErr)
		}
	}
	workflow, err := Parse(data)
	if err != nil {
		if _, compatibilityFailed := l.capturedErrors[canonical]; !compatibilityFailed {
			l.rememberLoadError(canonical, err)
		}
		return nil, err
	}
	l.snapshots[canonical] = &LocalWorkflowSnapshot{
		Ref:      canonical,
		Revision: revision,
		Workflow: workflow,
	}
	return workflow, nil
}

func (l *executorWorkflowSnapshotLoader) rememberError(err error) {
	if l != nil && l.firstError == nil {
		l.firstError = err
	}
}

func (l *executorWorkflowSnapshotLoader) rememberLoadError(ref string, err error) {
	if l == nil {
		return
	}
	if !l.capture {
		l.rememberError(err)
		return
	}
	if _, exists := l.capturedErrors[ref]; !exists {
		l.capturedErrors[ref] = err
	}
}

func readExecutorWorkflowDependency(path string, remaining int64) ([]byte, error) {
	if remaining <= 0 {
		return nil, ErrWorkflowDependencyAnalysisLimitExceeded
	}
	limit := MaxWorkflowDependencyDefinitionBytes
	if remaining < limit {
		limit = remaining
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrWorkflowDependencyAnalysisLimitExceeded
	}
	return data, nil
}

func (e *Executor) admitHumanTaskClosure(
	ctx context.Context,
	workflowRef string,
	workflow *Workflow,
	maxCallDepth int,
) (*Executor, error) {
	if e == nil || e.humanTaskClosureAdmitted || workflow == nil ||
		!workflowHasReusableDependency(workflow) {
		return e, nil
	}
	rootRef, err := CanonicalLocalRef(workflowRef)
	if err != nil {
		execution, snapshotErr := newWorkflowExecutionState(workflow)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		digest := strings.TrimPrefix(execution.WorkflowRevision, "sha256:")
		rootRef = "workflows/__inline_" + digest + ".yml"
	}
	snapshots := make(map[string]*LocalWorkflowSnapshot, len(e.WorkflowSnapshots)+1)
	for ref, snapshot := range e.WorkflowSnapshots {
		snapshots[ref] = snapshot
	}
	rootExecution, err := newWorkflowExecutionState(workflow)
	if err != nil {
		return nil, err
	}
	snapshots[rootRef] = &LocalWorkflowSnapshot{
		Ref:      rootRef,
		Revision: rootExecution.WorkflowRevision,
		Workflow: rootExecution.Workflow,
	}
	loader := &executorWorkflowSnapshotLoader{
		executor:       e,
		snapshots:      snapshots,
		capturedErrors: make(map[string]error),
		capture:        len(e.WorkflowSnapshots) == 0,
	}
	closure, err := CheckWorkflowDependencyClosure(ctx, WorkflowDependencyCheckRequest{
		RootRef:      rootRef,
		RootWorkflow: rootExecution.Workflow,
		Loader:       loader,
		MaxCallDepth: maxCallDepth,
	})
	if err != nil {
		return nil, err
	}
	for _, issue := range closure.Issues {
		if issue.Code == WorkflowDependencyIssueHumanTaskReusableUnsupported {
			return nil, fmt.Errorf(
				"%w: human/task cannot share an admitted reusable workflow closure",
				ErrHumanTaskUnsupported,
			)
		}
	}
	if loader.firstError != nil {
		return nil, loader.firstError
	}
	for _, issue := range closure.Issues {
		if issue.Code == WorkflowDependencyIssueAnalysisLimitExceeded {
			return nil, ErrWorkflowDependencyAnalysisLimitExceeded
		}
	}
	admitted := *e
	admitted.WorkflowSnapshots = snapshots
	admitted.humanTaskClosureAdmitted = true
	if len(e.WorkflowSnapshots) == 0 {
		admitted.verifyCapturedSnapshots = runtimeCompatibilityConfigured(e.RuntimeCompatibility)
		admitted.capturedReusableErrors = loader.capturedErrors
	}
	return &admitted, nil
}

func workflowHasReusableDependency(workflow *Workflow) bool {
	if workflow == nil {
		return false
	}
	for _, job := range workflow.Jobs {
		if strings.TrimSpace(job.Uses) != "" {
			return true
		}
	}
	return false
}

func (e *Executor) executeWorkflow(
	ctx context.Context,
	store RunStore,
	run *Run,
	workflow *Workflow,
	req RunRequest,
	resume *WorkflowExecutionCursor,
) (map[string]any, error) {
	inputs, err := ResolveWorkflowCallInvocation(workflow.On.WorkflowCall, req.Inputs, req.Secrets)
	if err != nil {
		return nil, err
	}
	execCtx := ExecutionContext{
		Inputs:       inputs,
		Secrets:      cloneStringMap(req.Secrets),
		Event:        cloneMap(req.Event),
		Session:      strings.TrimSpace(req.Session),
		Delivery:     req.Delivery,
		Steps:        make(map[string]StepExecution),
		Needs:        make(map[string]JobExecution),
		WorkspaceDir: e.WorkspaceDir,
		WorkflowRef:  run.WorkflowRef,
		RunID:        run.ID,
	}
	if run.privateRoot != nil {
		execCtx.privateValues = cloneMap(run.privateRoot.Values)
		execCtx.frozenReadOnlySession = cloneFrozenReadOnlySession(
			run.privateRoot.ReadOnlySession,
		)
	}
	order, err := topoJobs(workflow.Jobs)
	if err != nil {
		return nil, err
	}
	jobs := make(map[string]JobExecution, len(workflow.Jobs))
	recovering := resume != nil
	cursorReached := false
	for _, jobID := range order {
		if err := checkRunCanceled(ctx, store, run); err != nil {
			return nil, err
		}
		job := workflow.Jobs[jobID]
		startStep := 0
		resumeJob := false
		if recovering && jobID == resume.JobID {
			if persisted, exists := run.Jobs[jobID]; exists &&
				(persisted.Status == RunStatusSucceeded || persisted.Status == RunStatusSkipped) {
				// A prior claimant may have completed the cursor job (and even
				// later jobs) before crashing ahead of the final run update. Its
				// durable job checkpoint is authoritative; advancing from the
				// original human step would otherwise reject or repeat it.
				jobs[jobID] = persisted
				cursorReached = true
				continue
			}
			startStep = resume.StepIndex
			resumeJob = true
			cursorReached = true
		} else if recovering {
			persisted, exists := run.Jobs[jobID]
			if exists && (persisted.Status == RunStatusSucceeded || persisted.Status == RunStatusSkipped) {
				jobs[jobID] = persisted
				continue
			}
			if !cursorReached {
				return nil, fmt.Errorf("invalid persisted workflow cursor")
			}
			if exists && persisted.Status == RunStatusRunning {
				var prefixErr error
				startStep, prefixErr = persistedWorkflowStepPrefix(run, jobID, job.Steps)
				if prefixErr != nil {
					return nil, prefixErr
				}
				resumeJob = true
			}
		}
		jobExec, err := e.executeJob(
			ctx, store, run, jobID, job, req, execCtx, jobs, startStep, resumeJob,
		)
		jobs[jobID] = jobExec
		run.Jobs[jobID] = jobExec
		if err != nil {
			var waiting workflowWaitingError
			if errors.As(err, &waiting) {
				return cloneMap(run.Outputs), err
			}
			if updateErr := store.UpdateRun(ctx, run); updateErr != nil {
				return nil, updateErr
			}
			outputs, outputErr := renderWorkflowOutputs(workflow, inputs, req, execCtx, jobs)
			if outputErr != nil {
				return outputs, outputErr
			}
			return outputs, err
		}
		if updateErr := store.UpdateRun(ctx, run); updateErr != nil {
			return nil, updateErr
		}
	}
	if run.execution != nil {
		run.execution.Cursor = nil
	}
	return renderWorkflowOutputs(workflow, inputs, req, execCtx, jobs)
}

func persistedWorkflowStepPrefix(
	run *Run,
	jobID string,
	steps []Step,
) (int, error) {
	prefix := 0
	gap := false
	for index, step := range steps {
		stepID := strings.TrimSpace(step.ID)
		if stepID == "" {
			stepID = fmt.Sprintf("step_%d", index+1)
		}
		persisted, exists := run.Steps[jobID+"/"+stepID]
		if !exists {
			gap = true
			continue
		}
		if persisted.Status == RunStatusSucceeded || persisted.Status == RunStatusSkipped {
			if gap {
				return 0, fmt.Errorf("invalid persisted workflow cursor")
			}
			prefix++
			continue
		}
		gap = true
	}
	return prefix, nil
}

func (e *Executor) executeJob(
	ctx context.Context,
	store RunStore,
	run *Run,
	jobID string,
	job Job,
	req RunRequest,
	execCtx ExecutionContext,
	jobs map[string]JobExecution,
	startStep int,
	resumeJob bool,
) (JobExecution, error) {
	resumedJob := resumeJob
	jobExec := JobExecution{ID: jobID, Status: RunStatusRunning, Outputs: make(map[string]any)}
	if resumedJob {
		persisted, exists := run.Jobs[jobID]
		if !exists || (persisted.Status != RunStatusWaiting && persisted.Status != RunStatusRunning) {
			return JobExecution{ID: jobID, Status: RunStatusFailed}, fmt.Errorf("invalid persisted workflow cursor")
		}
		jobExec = persisted
		jobExec.Status = RunStatusRunning
		jobExec.Error = ""
	} else {
		e.appendEvent(ctx, store, RunEvent{Kind: "workflow.job.start", RunID: run.ID, JobID: jobID})
	}
	if err := checkRunCanceled(ctx, store, run); err != nil {
		jobExec.Status = RunStatusCanceled
		jobExec.Error = err.Error()
		return jobExec, err
	}
	for _, dep := range job.Needs {
		depExec := jobs[dep]
		execCtx.Needs[dep] = depExec
		if depExec.Status != RunStatusSucceeded {
			if resumedJob {
				return JobExecution{ID: jobID, Status: RunStatusFailed}, fmt.Errorf("invalid persisted workflow cursor")
			}
			jobExec.Status = RunStatusSkipped
			jobExec.Error = fmt.Sprintf("dependency %s did not succeed", dep)
			e.appendEvent(
				ctx,
				store,
				RunEvent{Kind: "workflow.job.failed", RunID: run.ID, JobID: jobID, Message: jobExec.Error},
			)
			return jobExec, fmt.Errorf("%s", jobExec.Error)
		}
	}
	if !resumedJob {
		if ok, err := evalIf(job.If, expressionCtxFrom(execCtx, jobs)); err != nil {
			jobExec.Status = RunStatusFailed
			jobExec.Error = err.Error()
			return jobExec, err
		} else if !ok {
			jobExec.Status = RunStatusSkipped
			e.appendEvent(ctx, store, RunEvent{
				Kind: "workflow.job.end", RunID: run.ID, JobID: jobID, Message: "skipped",
			})
			return jobExec, nil
		}
	}
	if strings.TrimSpace(job.Uses) != "" {
		childOutputs, childRunID, err := e.executeReusableJob(ctx, job, req, execCtx, jobs, jobID, run.ID)
		if childRunID != "" {
			run.ChildRunIDs = append(run.ChildRunIDs, childRunID)
		}
		if err != nil {
			if job.ContinueOnError {
				jobExec.Status = RunStatusSucceeded
				jobExec.Error = err.Error()
				jobExec.Outputs = childOutputs
				e.appendEvent(
					ctx,
					store,
					RunEvent{
						Kind:    "workflow.job.end",
						RunID:   run.ID,
						JobID:   jobID,
						Message: "continued after error",
						Payload: map[string]any{"outputs": childOutputs, "error": err.Error()},
					},
				)
				return jobExec, nil
			}
			jobExec.Status = RunStatusFailed
			jobExec.Error = err.Error()
			e.appendEvent(
				ctx,
				store,
				RunEvent{Kind: "workflow.job.failed", RunID: run.ID, JobID: jobID, Message: err.Error()},
			)
			return jobExec, err
		}
		jobExec.Outputs = childOutputs
		jobExec.Status = RunStatusSucceeded
		e.appendEvent(
			ctx,
			store,
			RunEvent{
				Kind:    "workflow.job.end",
				RunID:   run.ID,
				JobID:   jobID,
				Payload: map[string]any{"outputs": childOutputs},
			},
		)
		return jobExec, nil
	}
	stepCtx := execCtx
	stepCtx.Needs = map[string]JobExecution{}
	for _, dep := range job.Needs {
		stepCtx.Needs[dep] = jobs[dep]
	}
	stepCtx.Steps = make(map[string]StepExecution)
	if startStep > len(job.Steps) {
		return JobExecution{ID: jobID, Status: RunStatusFailed}, fmt.Errorf("invalid persisted workflow cursor")
	}
	for index := 0; index < startStep; index++ {
		stepID := strings.TrimSpace(job.Steps[index].ID)
		if stepID == "" {
			stepID = fmt.Sprintf("step_%d", index+1)
		}
		persisted, ok := run.Steps[jobID+"/"+stepID]
		if !ok || (persisted.Status != RunStatusSucceeded && persisted.Status != RunStatusSkipped) {
			return JobExecution{ID: jobID, Status: RunStatusFailed}, fmt.Errorf("invalid persisted workflow cursor")
		}
		stepCtx.Steps[stepID] = persisted
	}
	for index := startStep; index < len(job.Steps); index++ {
		step := job.Steps[index]
		stepID := strings.TrimSpace(step.ID)
		if stepID == "" {
			stepID = fmt.Sprintf("step_%d", index+1)
		}
		if persisted, ok := run.Steps[jobID+"/"+stepID]; ok &&
			(persisted.Status == RunStatusSucceeded || persisted.Status == RunStatusSkipped) {
			// A resume claimant may have crashed after durably recording this
			// step but before completing the run. Rehydrate it instead of
			// repeating side effects when the response lease is reclaimed.
			stepCtx.Steps[stepID] = persisted
			continue
		}
		if persisted, ok := run.Steps[jobID+"/"+stepID]; ok &&
			(persisted.Status == RunStatusFailed || persisted.Status == RunStatusCanceled) {
			stepCtx.Steps[stepID] = persisted
			if job.ContinueOnError {
				jobExec.Status = RunStatusSucceeded
				jobExec.Error = persisted.Error
				if jobExec.Error == "" {
					jobExec.Error = "persisted continuation step did not succeed"
				}
				outputs, outputErr := renderJobOutputs(job.Outputs, stepCtx, jobs)
				if outputErr != nil {
					outputs = map[string]any{}
				}
				jobExec.Outputs = outputs
				e.appendEvent(
					ctx,
					store,
					RunEvent{
						Kind:    "workflow.job.end",
						RunID:   run.ID,
						JobID:   jobID,
						Message: "continued after persisted error",
						Payload: map[string]any{
							"outputs": outputs,
							"error":   jobExec.Error,
						},
					},
				)
				return jobExec, nil
			}
			jobExec.Status = persisted.Status
			jobExec.Error = persisted.Error
			if jobExec.Error == "" {
				jobExec.Error = "persisted continuation step did not succeed"
			}
			return jobExec, errors.New(jobExec.Error)
		}
		if err := checkRunCanceled(ctx, store, run); err != nil {
			jobExec.Status = RunStatusCanceled
			jobExec.Error = err.Error()
			return jobExec, err
		}
		stepExec, err := e.executeStep(ctx, store, run, jobID, index, step, stepCtx, jobs)
		if stepExec.ID != "" {
			stepCtx.Steps[stepExec.ID] = stepExec
			run.Steps[jobID+"/"+stepExec.ID] = stepExec
		}
		if err != nil {
			var waiting workflowWaitingError
			if errors.As(err, &waiting) {
				jobExec.Status = RunStatusWaiting
				return jobExec, err
			}
			run.Jobs[jobID] = jobExec
			if updateErr := store.UpdateRun(ctx, run); updateErr != nil {
				return jobExec, updateErr
			}
			if step.ContinueOnError {
				continue
			}
			if job.ContinueOnError {
				jobExec.Status = RunStatusSucceeded
				jobExec.Error = err.Error()
				outputs, outputErr := renderJobOutputs(job.Outputs, stepCtx, jobs)
				if outputErr != nil {
					outputs = map[string]any{}
				}
				jobExec.Outputs = outputs
				e.appendEvent(
					ctx,
					store,
					RunEvent{
						Kind:    "workflow.job.end",
						RunID:   run.ID,
						JobID:   jobID,
						Message: "continued after error",
						Payload: map[string]any{"outputs": outputs, "error": err.Error()},
					},
				)
				return jobExec, nil
			}
			jobExec.Status = RunStatusFailed
			jobExec.Error = err.Error()
			e.appendEvent(
				ctx,
				store,
				RunEvent{Kind: "workflow.job.failed", RunID: run.ID, JobID: jobID, Message: err.Error()},
			)
			return jobExec, err
		}
		run.Jobs[jobID] = jobExec
		if updateErr := store.UpdateRun(ctx, run); updateErr != nil {
			return jobExec, updateErr
		}
	}
	outputs, err := renderJobOutputs(job.Outputs, stepCtx, jobs)
	if err != nil {
		jobExec.Status = RunStatusFailed
		jobExec.Error = err.Error()
		return jobExec, err
	}
	jobExec.Outputs = outputs
	jobExec.Status = RunStatusSucceeded
	e.appendEvent(
		ctx,
		store,
		RunEvent{Kind: "workflow.job.end", RunID: run.ID, JobID: jobID, Payload: map[string]any{"outputs": outputs}},
	)
	return jobExec, nil
}

func (e *Executor) executeReusableJob(
	ctx context.Context,
	job Job,
	req RunRequest,
	execCtx ExecutionContext,
	jobs map[string]JobExecution,
	jobID string,
	parentRunID string,
) (map[string]any, string, error) {
	if execCtx.privateValues != nil || execCtx.frozenReadOnlySession != nil {
		return nil, "", fmt.Errorf(
			"%w: private root cannot cross a reusable workflow edge",
			ErrPrivateWorkflowContext,
		)
	}
	with, err := renderMap(job.With, expressionCtxFrom(execCtx, jobs))
	if err != nil {
		return nil, "", err
	}
	childReq := RunRequest{
		Ref:         job.Uses,
		Inputs:      with,
		Event:       execCtx.Event,
		Origin:      cloneRunOrigin(req.Origin),
		Session:     inheritedContextValue(job.Context.Session, execCtx.Session),
		Delivery:    inheritedDelivery(job.Context.Delivery, execCtx.Delivery),
		ParentRunID: parentRunID,
		CallerJobID: jobID,
		CallDepth:   req.CallDepth + 1,
	}
	childSecrets, err := renderJobSecrets(job.Secrets, execCtx, jobs)
	if err != nil {
		return nil, "", err
	}
	childReq.Secrets = childSecrets
	if runnableErr := e.ensureReusableWorkflowRunnable(ctx, childReq.Ref); runnableErr != nil {
		return nil, "", runnableErr
	}
	result, err := e.Run(ctx, childReq)
	if result == nil {
		return nil, "", err
	}
	return result.Outputs, result.RunID, err
}

func (e *Executor) executeStep(
	ctx context.Context,
	store RunStore,
	run *Run,
	jobID string,
	index int,
	step Step,
	execCtx ExecutionContext,
	jobs map[string]JobExecution,
) (StepExecution, error) {
	stepID := strings.TrimSpace(step.ID)
	if stepID == "" {
		stepID = fmt.Sprintf("step_%d", index+1)
	}
	stepExec := StepExecution{ID: stepID, Status: RunStatusRunning, Outputs: make(map[string]any)}
	e.appendEvent(ctx, store, RunEvent{Kind: "workflow.step.start", RunID: run.ID, JobID: jobID, StepID: stepID})
	if err := checkRunCanceled(ctx, store, run); err != nil {
		stepExec.Status = RunStatusCanceled
		stepExec.Error = err.Error()
		return stepExec, err
	}
	if ok, err := evalIf(step.If, expressionCtxFrom(execCtx, jobs)); err != nil {
		stepExec.Status = RunStatusFailed
		stepExec.Error = err.Error()
		return stepExec, err
	} else if !ok {
		stepExec.Status = RunStatusSkipped
		e.appendEvent(
			ctx,
			store,
			RunEvent{Kind: "workflow.step.end", RunID: run.ID, JobID: jobID, StepID: stepID, Message: "skipped"},
		)
		return stepExec, nil
	}
	if strings.TrimSpace(step.Uses) == "human/task" && workflowValueReferencesSecrets(step.With) {
		stepExec.Status = RunStatusFailed
		stepExec.Error = "human/task values cannot reference secrets"
		return stepExec, fmt.Errorf("%s", stepExec.Error)
	}
	with, err := renderMap(step.With, expressionCtxFrom(execCtx, jobs))
	if err != nil {
		stepExec.Status = RunStatusFailed
		stepExec.Error = err.Error()
		return stepExec, err
	}
	if strings.TrimSpace(step.Uses) == "human/task" {
		task, taskErr := newWorkflowHumanTask(run, jobID, stepID, with)
		if taskErr != nil {
			stepExec.Status = RunStatusFailed
			stepExec.Error = taskErr.Error()
			return stepExec, taskErr
		}
		if run.execution == nil {
			stepExec.Status = RunStatusFailed
			stepExec.Error = ErrHumanTaskUnsupported.Error()
			return stepExec, ErrHumanTaskUnsupported
		}
		if run.humanTasks == nil {
			run.humanTasks = make(map[string]WorkflowHumanTask)
		}
		if _, exists := run.humanTasks[task.ID]; exists {
			stepExec.Status = RunStatusFailed
			stepExec.Error = ErrHumanTaskConflict.Error()
			return stepExec, ErrHumanTaskConflict
		}
		run.humanTasks[task.ID] = task
		run.execution.Cursor = &WorkflowExecutionCursor{JobID: jobID, StepIndex: index}
		stepExec.Status = RunStatusWaiting
		stepExec.Outputs = map[string]any{
			"task_id":    task.ID,
			"input_hash": task.InputHash,
		}
		return stepExec, workflowWaitingError{}
	}
	stepTargetCtx := execCtx
	stepTargetCtx.JobID = jobID
	stepTargetCtx.StepID = stepID
	if errors.Is(context.Cause(ctx), ErrHumanTaskConflict) {
		stepExec.Status = RunStatusFailed
		stepExec.Error = ErrHumanTaskConflict.Error()
		return stepExec, ErrHumanTaskConflict
	}
	outputs, err := e.runStepTarget(ctx, step, with, stepTargetCtx)
	if err != nil {
		if step.ContinueOnError {
			if outputs == nil {
				outputs = map[string]any{}
			}
			stepExec.Status = RunStatusSucceeded
			stepExec.Error = err.Error()
			stepExec.Outputs = outputs
			e.appendEvent(
				ctx,
				store,
				RunEvent{
					Kind:    "workflow.step.end",
					RunID:   run.ID,
					JobID:   jobID,
					StepID:  stepID,
					Message: "continued after error",
					Payload: map[string]any{"outputs": outputs, "error": err.Error()},
				},
			)
			return stepExec, err
		}
		stepExec.Status = RunStatusFailed
		stepExec.Error = err.Error()
		if outputs == nil {
			outputs = map[string]any{}
		}
		stepExec.Outputs = outputs
		e.appendEvent(
			ctx,
			store,
			RunEvent{
				Kind:    "workflow.step.failed",
				RunID:   run.ID,
				JobID:   jobID,
				StepID:  stepID,
				Message: err.Error(),
				Payload: map[string]any{"outputs": outputs},
			},
		)
		return stepExec, err
	}
	stepExec.Outputs = outputs
	stepExec.Status = RunStatusSucceeded
	e.appendEvent(
		ctx,
		store,
		RunEvent{
			Kind:    "workflow.step.end",
			RunID:   run.ID,
			JobID:   jobID,
			StepID:  stepID,
			Payload: map[string]any{"outputs": outputs},
		},
	)
	return stepExec, nil
}

func (e *Executor) ensureReusableWorkflowRunnable(ctx context.Context, ref string) error {
	if e == nil {
		return nil
	}
	if canonical, err := CanonicalLocalRef(ref); err == nil {
		if capturedErr := e.capturedReusableErrors[canonical]; capturedErr != nil {
			return capturedErr
		}
	}
	if len(e.WorkflowSnapshots) != 0 {
		if snapshot, canonical, ok := e.workflowSnapshot(ref); ok {
			if e.verifyCapturedSnapshots {
				if err := ensureWorkflowHashRunnable(
					e.WorkspaceDir,
					canonical,
					NormalizeRuntimeCompatibility(e.RuntimeCompatibility),
					snapshot.Revision,
				); err != nil {
					return err
				}
			}
			return nil
		}
		return fmt.Errorf(
			"workflow %q is outside the admitted snapshot closure",
			ref,
		)
	}
	if !runtimeCompatibilityConfigured(e.RuntimeCompatibility) {
		return nil
	}
	return EnsureWorkflowRunnable(ctx, e.WorkspaceDir, ref, e.RuntimeCompatibility, e.localOptions()...)
}

func (e *Executor) workflowSnapshot(ref string) (*LocalWorkflowSnapshot, string, bool) {
	if e == nil || len(e.WorkflowSnapshots) == 0 {
		return nil, "", false
	}
	canonical, err := CanonicalLocalRef(ref)
	if err != nil {
		return nil, "", false
	}
	snapshot, ok := e.WorkflowSnapshots[canonical]
	if !ok || snapshot == nil || snapshot.Workflow == nil || snapshot.Ref != canonical {
		return nil, "", false
	}
	return snapshot, canonical, true
}

func (e *Executor) localOptions() []LocalOption {
	if e == nil || strings.TrimSpace(e.DefinitionsDir) == "" {
		return nil
	}
	return []LocalOption{WithDefinitionsDir(e.DefinitionsDir)}
}

func runtimeCompatibilityConfigured(runtime RuntimeCompatibility) bool {
	return strings.TrimSpace(runtime.PicoclawVersion) != "" ||
		strings.TrimSpace(runtime.GitCommit) != "" ||
		strings.TrimSpace(runtime.WorkflowEngine) != "" ||
		strings.TrimSpace(runtime.WorkflowSchema) != "" ||
		strings.TrimSpace(runtime.ValidatorFingerprint) != ""
}

func renderJobSecrets(raw any, execCtx ExecutionContext, jobs map[string]JobExecution) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}
	if text, ok := raw.(string); ok {
		if strings.EqualFold(strings.TrimSpace(text), "inherit") {
			return cloneStringMap(execCtx.Secrets), nil
		}
		return nil, fmt.Errorf("unsupported secrets mode %q", text)
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("secrets must be inherit or a map")
	}
	exprCtx := expressionCtxFrom(execCtx, jobs)
	out := make(map[string]string, len(values))
	for key, value := range values {
		rendered, err := renderSecretValue(key, value, exprCtx)
		if err != nil {
			return nil, err
		}
		out[key] = rendered
	}
	return out, nil
}

func renderSecretValue(name string, value any, ctx expressionContext) (string, error) {
	if text, ok := value.(string); ok {
		rendered, err := renderSecretString(name, text, ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprint(rendered), nil
	}
	rendered, err := renderValue(value, ctx)
	if err != nil {
		return "", err
	}
	if secretValueMissing(rendered) {
		return "", fmt.Errorf("mapped workflow secret %q is missing", name)
	}
	return fmt.Sprint(rendered), nil
}

func renderSecretString(name, input string, ctx expressionContext) (any, error) {
	matches := expressionPattern.FindAllStringSubmatch(input, -1)
	if len(matches) == 0 {
		return input, nil
	}
	if len(matches) == 1 && strings.TrimSpace(matches[0][0]) == strings.TrimSpace(input) {
		value, err := evalExpression(matches[0][1], ctx)
		if err != nil {
			return nil, err
		}
		if value == nil {
			return nil, fmt.Errorf("mapped workflow secret %q is missing", name)
		}
		return value, nil
	}
	var firstErr error
	out := expressionPattern.ReplaceAllStringFunc(input, func(match string) string {
		if firstErr != nil {
			return ""
		}
		sub := expressionPattern.FindStringSubmatch(match)
		value, err := evalExpression(sub[1], ctx)
		if err != nil {
			firstErr = err
			return ""
		}
		if value == nil {
			firstErr = fmt.Errorf("mapped workflow secret %q is missing", name)
			return ""
		}
		return fmt.Sprint(value)
	})
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func secretValueMissing(value any) bool {
	if value == nil {
		return true
	}
	switch v := value.(type) {
	case map[string]any:
		for _, item := range v {
			if secretValueMissing(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if secretValueMissing(item) {
				return true
			}
		}
	}
	return false
}

func (e *Executor) runStepTarget(
	ctx context.Context,
	step Step,
	with map[string]any,
	execCtx ExecutionContext,
) (map[string]any, error) {
	uses := strings.TrimSpace(step.Uses)
	switch {
	case strings.HasPrefix(uses, "tool/"):
		if e.Tools == nil {
			return nil, fmt.Errorf("tool runner not configured")
		}
		return e.Tools.RunTool(ctx, ToolRequest{
			Name:     strings.TrimPrefix(uses, "tool/"),
			Args:     with,
			Session:  stepSession(step.Context, with, execCtx),
			Delivery: stepDelivery(step.Context, execCtx),
		})
	case strings.HasPrefix(uses, "mcp/"):
		if e.Tools == nil {
			return nil, fmt.Errorf("tool runner not configured")
		}
		target := strings.TrimPrefix(uses, "mcp/")
		serverName, toolName, ok := strings.Cut(target, "/")
		if !ok || strings.TrimSpace(serverName) == "" || strings.TrimSpace(toolName) == "" {
			return nil, fmt.Errorf("invalid MCP uses target %q: expected mcp/<server>/<tool>", uses)
		}
		return e.Tools.RunTool(ctx, ToolRequest{
			Name:      picomcp.CanonicalToolName(serverName, toolName),
			Args:      with,
			Session:   stepSession(step.Context, with, execCtx),
			Delivery:  stepDelivery(step.Context, execCtx),
			MCP:       true,
			MCPServer: serverName,
			MCPTool:   toolName,
		})
	case strings.HasPrefix(uses, "agent/"):
		if e.Agents == nil {
			return nil, fmt.Errorf("agent runner not configured")
		}
		output, err := ParseAgentOutputContract(with["output"])
		if err != nil {
			return nil, err
		}
		sessionMode := stringFromMap(with, "session")
		sessionKey := stepSession(step.Context, with, execCtx)
		if sessionMode == AgentSessionEphemeral {
			sessionKey = ""
		}
		agentID := strings.TrimPrefix(uses, "agent/")
		var frozenSession *FrozenReadOnlySession
		if execCtx.frozenReadOnlySession != nil &&
			strings.TrimSpace(stringFromMap(with, "history")) == "read_only" {
			if execCtx.frozenReadOnlySession.AgentID != agentID {
				return nil, fmt.Errorf(
					"%w: read-only agent does not match captured session",
					ErrPrivateWorkflowContext,
				)
			}
			frozenSession = cloneFrozenReadOnlySession(execCtx.frozenReadOnlySession)
			sessionKey = ""
		}
		outputs, runErr := e.Agents.RunAgent(ctx, AgentRequest{
			AgentID:               agentID,
			Message:               stringFromMap(with, "message"),
			Prompt:                stringFromMap(with, "prompt"),
			Context:               stringFromMap(with, "context"),
			Session:               sessionKey,
			EphemeralSession:      sessionMode == AgentSessionEphemeral,
			History:               stringFromMap(with, "history"),
			Cache:                 stringFromMap(with, "cache"),
			Tools:                 agentToolsMode(with),
			Delivery:              stepDelivery(step.Context, execCtx),
			Inputs:                with,
			Output:                output,
			Managed:               with["managed"],
			Scope:                 with["scope"],
			PrivateContext:        execCtx.privateValues != nil || frozenSession != nil,
			FrozenReadOnlySession: frozenSession,
		})
		if frozenSession != nil && outputs != nil {
			outputs["session"] = AgentSessionPrivate
			outputs["session_mode"] = AgentSessionPrivate
			outputs["history_revision"] = frozenSession.HistoryRevision
			delete(outputs, "cache_key")
		}
		return outputs, runErr
	case strings.HasPrefix(uses, "function/"):
		name := strings.TrimPrefix(uses, "function/")
		if outputs, handled, err := RunNativeFunction(ctx, name, with, execCtx); handled {
			return outputs, err
		}
		if e.Functions == nil {
			return nil, fmt.Errorf("function runner not configured")
		}
		return e.Functions.RunFunction(ctx, name, with, execCtx)
	default:
		return nil, fmt.Errorf("unsupported uses target %q", uses)
	}
}

func agentToolsMode(with map[string]any) string {
	if strings.TrimSpace(stringFromMap(with, "tools")) == AgentToolsNone {
		return AgentToolsNone
	}
	return AgentToolsInherit
}

// ResolveWorkflowCallInvocation applies workflow_call defaults and validates
// provided input types and required secrets. It is pure and shared by normal
// execution and trigger simulation.
func ResolveWorkflowCallInvocation(
	call *WorkflowCall,
	provided map[string]any,
	secrets map[string]string,
) (map[string]any, error) {
	out := cloneMap(provided)
	if call == nil {
		return out, nil
	}
	for name, input := range call.Inputs {
		value, ok := out[name]
		if ok {
			if err := validateWorkflowInputValue(name, input.Type, value); err != nil {
				return nil, err
			}
			continue
		}
		if input.Default != nil {
			if err := validateWorkflowInputValue(name, input.Type, input.Default); err != nil {
				return nil, err
			}
			out[name] = cloneJSONValue(input.Default)
			continue
		}
		if input.Required {
			return nil, fmt.Errorf("required workflow input %q is missing", name)
		}
	}
	for name, secret := range call.Secrets {
		if !secret.Required {
			continue
		}
		if strings.TrimSpace(secrets[name]) == "" {
			return nil, fmt.Errorf("required workflow secret %q is missing", name)
		}
	}
	return out, nil
}

func renderJobOutputs(
	outputs map[string]string,
	execCtx ExecutionContext,
	jobs map[string]JobExecution,
) (map[string]any, error) {
	out := make(map[string]any, len(outputs))
	for name, expr := range outputs {
		value, err := renderString(expr, expressionCtxFrom(execCtx, jobs))
		if err != nil {
			return nil, err
		}
		out[name] = value
	}
	return out, nil
}

func renderWorkflowOutputs(
	workflow *Workflow,
	inputs map[string]any,
	req RunRequest,
	execCtx ExecutionContext,
	jobs map[string]JobExecution,
) (map[string]any, error) {
	if workflow.On.WorkflowCall == nil || len(workflow.On.WorkflowCall.Outputs) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(workflow.On.WorkflowCall.Outputs))
	ctx := expressionCtxFrom(ExecutionContext{
		Inputs:        inputs,
		Secrets:       req.Secrets,
		Event:         req.Event,
		Session:       req.Session,
		Delivery:      req.Delivery,
		Steps:         execCtx.Steps,
		Needs:         execCtx.Needs,
		privateValues: execCtx.privateValues,
	}, jobs)
	for name, output := range workflow.On.WorkflowCall.Outputs {
		value, err := renderString(output.Value, ctx)
		if err != nil {
			return out, fmt.Errorf("render workflow output %q: %w", name, err)
		}
		out[name] = value
	}
	return out, nil
}

func validateWorkflowInputValue(name, typ string, value any) error {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "", "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("workflow input %q must be a string", name)
		}
	case "number":
		if _, ok := asFloat(value); !ok {
			return fmt.Errorf("workflow input %q must be a number", name)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("workflow input %q must be a boolean", name)
		}
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("workflow input %q must be an object", name)
		}
	case "array":
		if _, ok := value.([]any); !ok {
			return fmt.Errorf("workflow input %q must be an array", name)
		}
	}
	return nil
}

func expressionCtxFrom(execCtx ExecutionContext, jobs map[string]JobExecution) expressionContext {
	return expressionContext{
		Inputs:   execCtx.Inputs,
		Private:  execCtx.privateValues,
		Secrets:  execCtx.Secrets,
		Event:    execCtx.Event,
		Steps:    execCtx.Steps,
		Needs:    execCtx.Needs,
		Jobs:     jobs,
		Delivery: execCtx.Delivery,
		Session:  execCtx.Session,
	}
}

func topoJobs(jobs map[string]Job) ([]string, error) {
	state := make(map[string]int, len(jobs))
	var order []string
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("job dependency cycle at %s", id)
		case 2:
			return nil
		}
		state[id] = 1
		for _, dep := range jobs[id].Needs {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[id] = 2
		order = append(order, id)
		return nil
	}
	ids := make([]string, 0, len(jobs))
	for id := range jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if state[id] == 0 {
			if err := visit(id); err != nil {
				return nil, err
			}
		}
	}
	return order, nil
}

func inheritedContextValue(mode, current string) string {
	switch strings.TrimSpace(mode) {
	case "", "inherit":
		return current
	case "none":
		return ""
	default:
		if strings.HasPrefix(mode, "key:") {
			return strings.TrimSpace(strings.TrimPrefix(mode, "key:"))
		}
		return current
	}
}

func inheritedDelivery(mode string, current Delivery) Delivery {
	switch strings.TrimSpace(mode) {
	case "", "inherit":
		return current
	case "none":
		return Delivery{}
	default:
		return current
	}
}

func stepSession(ctx RunContext, with map[string]any, execCtx ExecutionContext) string {
	if session, ok := stringOption(with, "session"); ok {
		return inheritedContextValue(session, execCtx.Session)
	}
	return inheritedContextValue(ctx.Session, execCtx.Session)
}

func stepDelivery(ctx RunContext, execCtx ExecutionContext) Delivery {
	return inheritedDelivery(ctx.Delivery, execCtx.Delivery)
}

func stringFromMap(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func cloneMap(values map[string]any) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = cloneJSONValue(value)
	}
	return out
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = cloneJSONValue(item)
		}
		return out
	default:
		// JSON scalar values, including json.Number, are immutable.
		return value
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func checkpointWorkflowRun(run *Run) *workflowRunCheckpoint {
	if run == nil {
		return nil
	}
	checkpoint := &workflowRunCheckpoint{
		Inputs:  cloneMap(run.Inputs),
		Event:   cloneMap(run.Event),
		Outputs: cloneMap(run.Outputs),
		Jobs:    make(map[string]JobExecution, len(run.Jobs)),
		Steps:   make(map[string]StepExecution, len(run.Steps)),
	}
	for key, job := range run.Jobs {
		job.Outputs = cloneMap(job.Outputs)
		checkpoint.Jobs[key] = job
	}
	for key, step := range run.Steps {
		step.Outputs = cloneMap(step.Outputs)
		checkpoint.Steps[key] = step
	}
	return checkpoint
}

func cloneWorkflowRunCheckpoint(checkpoint *workflowRunCheckpoint) *workflowRunCheckpoint {
	if checkpoint == nil {
		return nil
	}
	run := &Run{
		Inputs:  checkpoint.Inputs,
		Event:   checkpoint.Event,
		Outputs: checkpoint.Outputs,
		Jobs:    checkpoint.Jobs,
		Steps:   checkpoint.Steps,
	}
	return checkpointWorkflowRun(run)
}

func restoreWorkflowRunCheckpoint(run *Run) {
	if run == nil || run.execution == nil || run.execution.Checkpoint == nil {
		return
	}
	checkpoint := cloneWorkflowRunCheckpoint(run.execution.Checkpoint)
	run.Inputs = checkpoint.Inputs
	run.Event = checkpoint.Event
	run.Outputs = checkpoint.Outputs
	run.Jobs = checkpoint.Jobs
	run.Steps = checkpoint.Steps
}

func cloneRun(run *Run) *Run {
	if run == nil {
		return nil
	}
	out := *run
	out.Origin = cloneRunOrigin(run.Origin)
	out.ChildRunIDs = append([]string(nil), run.ChildRunIDs...)
	out.Delivery = cloneDelivery(run.Delivery)
	out.Event = cloneMap(run.Event)
	out.Inputs = cloneMap(run.Inputs)
	out.Outputs = cloneMap(run.Outputs)
	out.Jobs = make(map[string]JobExecution, len(run.Jobs))
	for key, job := range run.Jobs {
		job.Outputs = cloneMap(job.Outputs)
		out.Jobs[key] = job
	}
	out.Steps = make(map[string]StepExecution, len(run.Steps))
	for key, step := range run.Steps {
		step.Outputs = cloneMap(step.Outputs)
		out.Steps[key] = step
	}
	if run.execution != nil {
		execution := *run.execution
		if run.execution.Cursor != nil {
			cursor := *run.execution.Cursor
			execution.Cursor = &cursor
		}
		if run.execution.Resume != nil {
			resume := *run.execution.Resume
			execution.Resume = &resume
		}
		// Workflow snapshots are immutable after run creation.
		execution.Workflow = run.execution.Workflow
		execution.Checkpoint = cloneWorkflowRunCheckpoint(run.execution.Checkpoint)
		out.execution = &execution
	}
	out.privateRoot = cloneFrozenWorkflowRootContext(run.privateRoot)
	out.humanTasks = make(map[string]WorkflowHumanTask, len(run.humanTasks))
	for key, task := range run.humanTasks {
		out.humanTasks[key] = cloneWorkflowHumanTask(task)
	}
	if run.CompletedAt != nil {
		completedAt := *run.CompletedAt
		out.CompletedAt = &completedAt
	}
	if run.CancelRequestedAt != nil {
		cancelRequestedAt := *run.CancelRequestedAt
		out.CancelRequestedAt = &cancelRequestedAt
	}
	return &out
}

func (e *Executor) appendEvent(ctx context.Context, store RunStore, event RunEvent) {
	if store == nil {
		return
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	if strings.TrimSpace(event.RunID) != "" {
		run, err := store.GetRun(ctx, event.RunID)
		if err != nil {
			return
		}
		if IsPrivateWorkflowRun(run) {
			event = sanitizePrivateWorkflowEvent(event)
		}
	}
	_ = store.AppendEvent(ctx, event)
	e.publishRuntimeEvent(ctx, store, event)
}

func (e *Executor) publishRuntimeEvent(ctx context.Context, store RunStore, event RunEvent) {
	if e == nil || e.RuntimeEvents == nil || strings.TrimSpace(event.Kind) == "" {
		return
	}
	var run *Run
	if store != nil && strings.TrimSpace(event.RunID) != "" {
		var err error
		run, err = store.GetRun(ctx, event.RunID)
		if err != nil || run == nil {
			return
		}
		if IsPrivateWorkflowRun(run) {
			event = sanitizePrivateWorkflowEvent(event)
		}
	}
	evt := runtimeevents.Event{
		Kind:     runtimeevents.Kind(event.Kind),
		Time:     event.Time,
		Source:   runtimeevents.Source{Component: "workflow", Name: event.RunID},
		Severity: workflowRuntimeSeverity(event.Kind),
		Payload:  workflowRuntimePayload(event),
	}
	if run != nil {
		if strings.TrimSpace(run.WorkflowRef) != "" {
			evt.Source.Name = run.WorkflowRef
		}
		if !IsPrivateWorkflowRun(run) {
			evt.Scope = runtimeevents.Scope{
				SessionKey: run.Session,
				Channel:    run.Delivery.Channel,
				ChatID:     run.Delivery.ChatID,
				TopicID:    run.Delivery.TopicID,
				MessageID:  run.Delivery.MessageID,
			}
		}
	}
	if evt.Source.Name == "" {
		if workflowRef, _ := event.Payload["workflow_ref"].(string); strings.TrimSpace(workflowRef) != "" {
			evt.Source.Name = strings.TrimSpace(workflowRef)
		}
	}
	e.RuntimeEvents.PublishNonBlocking(evt)
}

func (e *Executor) publishCanceledRuntimeEvent(ctx context.Context, store RunStore, run *Run, message string) {
	if run == nil || run.Status != RunStatusCanceled {
		return
	}
	event := RunEvent{
		Kind:    runtimeevents.KindWorkflowRunCanceled.String(),
		RunID:   run.ID,
		Message: strings.TrimSpace(message),
	}
	if event.Message == "" {
		event.Message = strings.TrimSpace(run.CancelReason)
	}
	if run.CancelRequestedAt != nil {
		event.Time = *run.CancelRequestedAt
	}
	e.publishRuntimeEvent(ctx, store, event)
}

func workflowRuntimePayload(event RunEvent) map[string]any {
	payload := cloneMap(event.Payload)
	payload["run_id"] = event.RunID
	if event.JobID != "" {
		payload["job_id"] = event.JobID
	}
	if event.StepID != "" {
		payload["step_id"] = event.StepID
	}
	if event.Message != "" {
		payload["message"] = event.Message
	}
	return payload
}

func workflowRuntimeSeverity(kind string) runtimeevents.Severity {
	switch kind {
	case runtimeevents.KindWorkflowRunFailed.String(), "workflow.run.canceled",
		runtimeevents.KindWorkflowJobFailed.String(), runtimeevents.KindWorkflowStepFailed.String():
		return runtimeevents.SeverityWarn
	default:
		return runtimeevents.SeverityInfo
	}
}

func NewRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "wr_" + hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("wr_%d", time.Now().UnixNano())
}
