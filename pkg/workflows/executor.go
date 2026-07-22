package workflows

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
)

const defaultMaxCallDepth = 4

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
	Session      string
	Delivery     Delivery
	ParentRunID  string
	CallerJobID  string
	Workflow     *Workflow
	WorkflowRef  string
	RetryOfRunID string
	CallDepth    int
	OnRunCreated func(*Run)
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
	workflow, workflowRef, loadErr := e.loadWorkflow(req)
	if loadErr != nil {
		return nil, loadErr
	}
	if validateErr := Validate(workflow); validateErr != nil {
		return nil, validateErr
	}
	store := e.Store
	if store == nil {
		store = NewFileRunStore(e.WorkspaceDir)
	}
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		runID = NewRunID()
	}
	now := time.Now().UTC()
	run := &Run{
		ID:           runID,
		WorkflowRef:  workflowRef,
		Status:       RunStatusRunning,
		ParentRunID:  req.ParentRunID,
		CallerJobID:  req.CallerJobID,
		RetryOfRunID: req.RetryOfRunID,
		Session:      strings.TrimSpace(req.Session),
		Delivery:     req.Delivery,
		Event:        cloneMap(req.Event),
		Inputs:       cloneMap(req.Inputs),
		Outputs:      make(map[string]any),
		Jobs:         make(map[string]JobExecution),
		Steps:        make(map[string]StepExecution),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if createErr := e.createRun(ctx, store, run, req.ParentRunID == ""); createErr != nil {
		return nil, createErr
	}
	e.appendEvent(
		ctx,
		store,
		RunEvent{Kind: "workflow.run.start", RunID: run.ID, Payload: map[string]any{"workflow_ref": workflowRef}},
	)
	if req.OnRunCreated != nil {
		req.OnRunCreated(cloneRun(run))
	}

	outputs, runErr := e.executeWorkflow(ctx, store, run, workflow, req)
	if runErr != nil {
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
			return &RunResult{
				RunID:   run.ID,
				Status:  run.Status,
				Outputs: run.Outputs,
				Error:   run.Error,
			}, nil
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
		return &RunResult{RunID: run.ID, Status: run.Status, Outputs: outputs, Error: run.Error}, runErr
	}
	if cancelErr := checkRunCanceled(ctx, store, run); cancelErr != nil {
		completedAt := time.Now().UTC()
		run.Status = RunStatusCanceled
		run.Error = cancelErr.Error()
		run.CompletedAt = &completedAt
		_ = store.UpdateRun(context.Background(), run)
		e.appendEvent(
			context.Background(),
			store,
			RunEvent{Kind: "workflow.run.canceled", RunID: run.ID, Message: cancelErr.Error()},
		)
		return &RunResult{RunID: run.ID, Status: run.Status, Outputs: outputs, Error: run.Error}, cancelErr
	}
	run.Status = RunStatusSucceeded
	run.Outputs = outputs
	now = time.Now().UTC()
	run.CompletedAt = &now
	if updateErr := store.UpdateRun(ctx, run); updateErr != nil {
		return nil, updateErr
	}
	if run.Status == RunStatusCanceled {
		reason := strings.TrimSpace(run.CancelReason)
		if reason == "" {
			reason = "cancel requested"
		}
		cancelErr := fmt.Errorf("%w: %s", ErrRunCanceled, reason)
		e.publishCanceledRuntimeEvent(context.Background(), store, run, cancelErr.Error())
		return &RunResult{
			RunID:   run.ID,
			Status:  run.Status,
			Outputs: run.Outputs,
			Error:   cancelErr.Error(),
		}, cancelErr
	}
	if run.Status != RunStatusSucceeded {
		return &RunResult{
			RunID:   run.ID,
			Status:  run.Status,
			Outputs: run.Outputs,
			Error:   run.Error,
		}, nil
	}
	e.appendEvent(
		ctx,
		store,
		RunEvent{Kind: "workflow.run.end", RunID: run.ID, Payload: map[string]any{"outputs": outputs}},
	)
	return &RunResult{RunID: run.ID, Status: run.Status, Outputs: outputs}, nil
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
	return e.Run(ctx, RunRequest{
		Ref:          run.WorkflowRef,
		Inputs:       cloneMap(run.Inputs),
		Secrets:      cloneStringMap(secrets),
		Event:        cloneMap(run.Event),
		Session:      run.Session,
		Delivery:     run.Delivery,
		ParentRunID:  run.ParentRunID,
		CallerJobID:  run.CallerJobID,
		RetryOfRunID: run.ID,
	})
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
		return nil, err
	}
	if run != nil && run.Status == RunStatusCanceled && (previous == nil || !isTerminalRunStatus(previous.Status)) {
		e.publishCanceledRuntimeEvent(ctx, store, run, run.CancelReason)
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
		return fmt.Errorf("workflow concurrency limit reached: %d running, max %d", running, e.MaxConcurrentRuns)
	}
	return nil
}

func checkRunCanceled(ctx context.Context, store RunStore, run *Run) error {
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

func (e *Executor) loadWorkflow(req RunRequest) (*Workflow, string, error) {
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

func (e *Executor) executeWorkflow(
	ctx context.Context,
	store RunStore,
	run *Run,
	workflow *Workflow,
	req RunRequest,
) (map[string]any, error) {
	inputs, err := applyWorkflowCallContract(workflow.On.WorkflowCall, req.Inputs, req.Secrets)
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
	order, err := topoJobs(workflow.Jobs)
	if err != nil {
		return nil, err
	}
	jobs := make(map[string]JobExecution, len(workflow.Jobs))
	for _, jobID := range order {
		if err := checkRunCanceled(ctx, store, run); err != nil {
			return nil, err
		}
		job := workflow.Jobs[jobID]
		jobExec, err := e.executeJob(ctx, store, run, jobID, job, req, execCtx, jobs)
		jobs[jobID] = jobExec
		run.Jobs[jobID] = jobExec
		if updateErr := store.UpdateRun(ctx, run); updateErr != nil {
			return nil, updateErr
		}
		if err != nil {
			outputs, outputErr := renderWorkflowOutputs(workflow, inputs, req, execCtx, jobs)
			if outputErr != nil {
				return outputs, outputErr
			}
			return outputs, err
		}
	}
	return renderWorkflowOutputs(workflow, inputs, req, execCtx, jobs)
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
) (JobExecution, error) {
	jobExec := JobExecution{ID: jobID, Status: RunStatusRunning, Outputs: make(map[string]any)}
	e.appendEvent(ctx, store, RunEvent{Kind: "workflow.job.start", RunID: run.ID, JobID: jobID})
	if err := checkRunCanceled(ctx, store, run); err != nil {
		jobExec.Status = RunStatusCanceled
		jobExec.Error = err.Error()
		return jobExec, err
	}
	for _, dep := range job.Needs {
		depExec := jobs[dep]
		execCtx.Needs[dep] = depExec
		if depExec.Status != RunStatusSucceeded {
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
	if ok, err := evalIf(job.If, expressionCtxFrom(execCtx, jobs)); err != nil {
		jobExec.Status = RunStatusFailed
		jobExec.Error = err.Error()
		return jobExec, err
	} else if !ok {
		jobExec.Status = RunStatusSkipped
		e.appendEvent(ctx, store, RunEvent{Kind: "workflow.job.end", RunID: run.ID, JobID: jobID, Message: "skipped"})
		return jobExec, nil
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
	for index, step := range job.Steps {
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
		if updateErr := store.UpdateRun(ctx, run); updateErr != nil {
			return jobExec, updateErr
		}
		if err != nil {
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
	with, err := renderMap(job.With, expressionCtxFrom(execCtx, jobs))
	if err != nil {
		return nil, "", err
	}
	childReq := RunRequest{
		Ref:         job.Uses,
		Inputs:      with,
		Event:       execCtx.Event,
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
	with, err := renderMap(step.With, expressionCtxFrom(execCtx, jobs))
	if err != nil {
		stepExec.Status = RunStatusFailed
		stepExec.Error = err.Error()
		return stepExec, err
	}
	stepTargetCtx := execCtx
	stepTargetCtx.JobID = jobID
	stepTargetCtx.StepID = stepID
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
	if e == nil || !runtimeCompatibilityConfigured(e.RuntimeCompatibility) {
		return nil
	}
	return EnsureWorkflowRunnable(ctx, e.WorkspaceDir, ref, e.RuntimeCompatibility, e.localOptions()...)
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
		return e.Tools.RunTool(ctx, ToolRequest{
			Name:     "mcp_" + strings.ReplaceAll(strings.TrimPrefix(uses, "mcp/"), "/", "_"),
			Args:     with,
			Session:  stepSession(step.Context, with, execCtx),
			Delivery: stepDelivery(step.Context, execCtx),
		})
	case strings.HasPrefix(uses, "agent/"):
		if e.Agents == nil {
			return nil, fmt.Errorf("agent runner not configured")
		}
		output, err := ParseAgentOutputContract(with["output"])
		if err != nil {
			return nil, err
		}
		return e.Agents.RunAgent(ctx, AgentRequest{
			AgentID:  strings.TrimPrefix(uses, "agent/"),
			Message:  stringFromMap(with, "message"),
			Prompt:   stringFromMap(with, "prompt"),
			Context:  stringFromMap(with, "context"),
			Session:  stepSession(step.Context, with, execCtx),
			History:  stringFromMap(with, "history"),
			Cache:    stringFromMap(with, "cache"),
			Delivery: stepDelivery(step.Context, execCtx),
			Inputs:   with,
			Output:   output,
			Managed:  with["managed"],
			Scope:    with["scope"],
		})
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

func applyWorkflowCallContract(
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
			out[name] = input.Default
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
		Inputs:   inputs,
		Secrets:  req.Secrets,
		Event:    req.Event,
		Session:  req.Session,
		Delivery: req.Delivery,
		Steps:    execCtx.Steps,
		Needs:    execCtx.Needs,
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
		out[key] = value
	}
	return out
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

func cloneRun(run *Run) *Run {
	if run == nil {
		return nil
	}
	out := *run
	out.ChildRunIDs = append([]string(nil), run.ChildRunIDs...)
	out.Delivery = run.Delivery
	out.Delivery.ReplyHandles = cloneStringMap(run.Delivery.ReplyHandles)
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
	_ = store.AppendEvent(ctx, event)
	e.publishRuntimeEvent(ctx, store, event)
}

func (e *Executor) publishRuntimeEvent(ctx context.Context, store RunStore, event RunEvent) {
	if e == nil || e.RuntimeEvents == nil || strings.TrimSpace(event.Kind) == "" {
		return
	}
	evt := runtimeevents.Event{
		Kind:     runtimeevents.Kind(event.Kind),
		Time:     event.Time,
		Source:   runtimeevents.Source{Component: "workflow", Name: event.RunID},
		Severity: workflowRuntimeSeverity(event.Kind),
		Payload:  workflowRuntimePayload(event),
	}
	if store != nil && strings.TrimSpace(event.RunID) != "" {
		if run, err := store.GetRun(ctx, event.RunID); err == nil && run != nil {
			if strings.TrimSpace(run.WorkflowRef) != "" {
				evt.Source.Name = run.WorkflowRef
			}
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
