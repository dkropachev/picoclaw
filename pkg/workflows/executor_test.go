package workflows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeToolRunner struct {
	requests []ToolRequest
	outputs  map[string]any
	err      error
}

func (r *fakeToolRunner) RunTool(_ context.Context, req ToolRequest) (map[string]any, error) {
	r.requests = append(r.requests, req)
	if r.err != nil {
		return nil, r.err
	}
	if r.outputs != nil {
		return cloneMap(r.outputs), nil
	}
	return map[string]any{"text": "tool:" + req.Name}, nil
}

type fakeAgentRunner struct {
	requests []AgentRequest
	outputs  map[string]any
	err      error
}

func (r *fakeAgentRunner) RunAgent(_ context.Context, req AgentRequest) (map[string]any, error) {
	r.requests = append(r.requests, req)
	if r.err != nil {
		return nil, r.err
	}
	if r.outputs != nil {
		return cloneMap(r.outputs), nil
	}
	return map[string]any{"text": req.Message}, nil
}

func TestExecutorRunsFunctionWorkflowWithIfAndOutputs(t *testing.T) {
	registry := NewFunctionRegistry()
	if err := registry.Register(
		"echo",
		func(_ context.Context, args map[string]any, _ ExecutionContext) (map[string]any, error) {
			return map[string]any{"text": args["text"]}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, `
name: Function
on:
  workflow_call:
    inputs:
      text:
        type: string
        required: true
    outputs:
      result:
        value: ${{ jobs.main.outputs.result }}
jobs:
  main:
    runs-on: picoclaw
    outputs:
      result: ${{ steps.echo.outputs.text }}
    steps:
      - id: skip
        if: ${{ inputs.text == 'nope' }}
        uses: function/echo
        with:
          text: should-not-run
      - id: echo
        uses: function/echo
        with:
          text: ${{ inputs.text }}
`)

	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	executor := &Executor{
		WorkspaceDir: workspace,
		Store:        store,
		Functions:    registry,
	}
	result, err := executor.Run(context.Background(), RunRequest{
		Workflow:    workflow,
		WorkflowRef: "inline",
		Inputs:      map[string]any{"text": "hello"},
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.Status != RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
	if got := result.Outputs["result"]; got != "hello" {
		t.Fatalf("output result = %#v, want hello", got)
	}

	run, err := store.GetRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if got := run.Steps["main/skip"].Status; got != RunStatusSkipped {
		t.Fatalf("skipped step status = %q, want skipped", got)
	}
}

func TestExecutorRunsReusableWorkflowJob(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "child.yml", `
name: Child
on:
  workflow_call:
    inputs:
      text:
        type: string
        required: true
    outputs:
      upper:
        value: ${{ jobs.child.outputs.upper }}
jobs:
  child:
    runs-on: picoclaw
    outputs:
      upper: ${{ steps.make.outputs.text }}
    steps:
      - id: make
        uses: function/prefix
        with:
          text: ${{ inputs.text }}
`)
	parent := parseWorkflow(t, `
name: Parent
on:
  workflow_call:
    outputs:
      result:
        value: ${{ jobs.call.outputs.upper }}
jobs:
  call:
    uses: workflows/child.yml
    with:
      text: from-parent
`)
	registry := NewFunctionRegistry()
	if err := registry.Register(
		"prefix",
		func(_ context.Context, args map[string]any, _ ExecutionContext) (map[string]any, error) {
			return map[string]any{"text": "child:" + args["text"].(string)}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	store := NewFileRunStore(workspace)
	executor := &Executor{
		WorkspaceDir: workspace,
		Store:        store,
		Functions:    registry,
	}

	result, err := executor.Run(context.Background(), RunRequest{
		Workflow:    parent,
		WorkflowRef: "workflows/parent.yml",
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if got := result.Outputs["result"]; got != "child:from-parent" {
		t.Fatalf("output result = %#v, want child:from-parent", got)
	}
	parentRun, err := store.GetRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if len(parentRun.ChildRunIDs) != 1 {
		t.Fatalf("child run ids = %#v, want one", parentRun.ChildRunIDs)
	}
}

func TestExecutorPropagatesDeliveryAndSessionToToolAndAgentSteps(t *testing.T) {
	toolRunner := &fakeToolRunner{}
	agentRunner := &fakeAgentRunner{}
	workflow := parseWorkflow(t, `
name: Chat
on:
  manual: {}
jobs:
  chat:
    runs-on: picoclaw
    steps:
      - id: search
        uses: tool/web_search
        with:
          query: ${{ event.message.text }}
      - id: answer
        uses: agent/main
        with:
          message: ${{ steps.search.outputs.text }}
          history: read_write
          cache: session
`)
	delivery := Delivery{
		Channel:          "telegram",
		ChatID:           "-1001",
		TopicID:          "42",
		MessageID:        "100",
		ReplyToMessageID: "99",
	}
	_, err := (&Executor{
		WorkspaceDir: t.TempDir(),
		Tools:        toolRunner,
		Agents:       agentRunner,
	}).Run(context.Background(), RunRequest{
		Workflow:    workflow,
		WorkflowRef: "inline",
		Event:       map[string]any{"message": map[string]any{"text": "hello"}},
		Session:     "workflow:discussion",
		Delivery:    delivery,
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if len(toolRunner.requests) != 1 {
		t.Fatalf("tool requests = %d, want 1", len(toolRunner.requests))
	}
	if got := toolRunner.requests[0].Delivery.TopicID; got != "42" {
		t.Fatalf("tool delivery topic = %q, want 42", got)
	}
	if got := toolRunner.requests[0].Session; got != "workflow:discussion" {
		t.Fatalf("tool session = %q, want workflow:discussion", got)
	}
	if len(agentRunner.requests) != 1 {
		t.Fatalf("agent requests = %d, want 1", len(agentRunner.requests))
	}
	if got := agentRunner.requests[0].Delivery.ReplyToMessageID; got != "99" {
		t.Fatalf("agent delivery reply = %q, want 99", got)
	}
	if got := agentRunner.requests[0].Session; got != "workflow:discussion" {
		t.Fatalf("agent session = %q, want workflow:discussion", got)
	}
}

func TestExecutorRejectsMissingWorkflowCallInputAndSecret(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Contract
on:
  workflow_call:
    inputs:
      text:
        type: string
        required: true
    secrets:
      token:
        required: true
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	executor := &Executor{WorkspaceDir: t.TempDir(), Functions: NewFunctionRegistry()}
	if _, err := executor.Run(context.Background(), RunRequest{Workflow: workflow}); err == nil {
		t.Fatal("Run succeeded, want missing input error")
	}
	if _, err := executor.Run(context.Background(), RunRequest{
		Workflow: workflow,
		Inputs:   map[string]any{"text": "ok"},
	}); err == nil {
		t.Fatal("Run succeeded, want missing secret error")
	}
}

func TestExecutorMapsReusableWorkflowSecrets(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "child-secret.yml", `
name: Child
on:
  workflow_call:
    secrets:
      child_token:
        required: true
    outputs:
      token:
        value: ${{ jobs.child.outputs.token }}
jobs:
  child:
    runs-on: picoclaw
    outputs:
      token: ${{ steps.echo.outputs.text }}
    steps:
      - id: echo
        uses: function/echo-secret
`)
	parent := parseWorkflow(t, `
name: Parent
on:
  workflow_call:
    outputs:
      token:
        value: ${{ jobs.call.outputs.token }}
jobs:
  call:
    uses: workflows/child-secret.yml
    secrets:
      child_token: ${{ secrets.parent_token }}
`)
	registry := NewFunctionRegistry()
	if err := registry.Register(
		"echo-secret",
		func(_ context.Context, _ map[string]any, exec ExecutionContext) (map[string]any, error) {
			return map[string]any{"text": exec.Secrets["child_token"]}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	result, err := (&Executor{WorkspaceDir: workspace, Functions: registry}).Run(context.Background(), RunRequest{
		Workflow:    parent,
		WorkflowRef: "workflows/parent.yml",
		Secrets:     map[string]string{"parent_token": "mapped-secret"},
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if got := result.Outputs["token"]; got != "mapped-secret" {
		t.Fatalf("token output = %#v, want mapped-secret", got)
	}
}

func TestExecutorReusableWorkflowContinueOnError(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "child-fail.yml", `
name: Child
on:
  workflow_call: {}
jobs:
  fail:
    runs-on: picoclaw
    steps:
      - uses: function/fail
`)
	parent := parseWorkflow(t, `
name: Parent
on:
  manual: {}
jobs:
  call:
    uses: workflows/child-fail.yml
    continue-on-error: true
  after:
    needs: call
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	registry := NewFunctionRegistry()
	_ = registry.Register("fail", func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		return nil, errors.New("child failed")
	})
	_ = registry.Register("noop", func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		return map[string]any{"ok": true}, nil
	})
	result, err := (&Executor{WorkspaceDir: workspace, Functions: registry}).Run(context.Background(), RunRequest{
		Workflow:    parent,
		WorkflowRef: "workflows/parent.yml",
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.Status != RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
}

func TestExecutorCancelRunBeforeNextStep(t *testing.T) {
	registry := NewFunctionRegistry()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	_ = registry.Register(
		"cancel",
		func(ctx context.Context, _ map[string]any, exec ExecutionContext) (map[string]any, error) {
			runs, err := store.ListRuns(ctx)
			if err != nil {
				return nil, err
			}
			for _, run := range runs {
				if run.Status != RunStatusRunning {
					continue
				}
				if _, err := store.CancelRun(ctx, run.ID, "test cancel"); err != nil {
					return nil, err
				}
				return map[string]any{"ok": true}, nil
			}
			return nil, errors.New("running run not found")
		},
	)
	_ = registry.Register("after", func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		t.Fatal("after step should not run after cancellation")
		return nil, nil
	})
	workflow := parseWorkflow(t, `
name: Cancel
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: function/cancel
      - uses: function/after
`)
	executor := &Executor{WorkspaceDir: workspace, Store: store, Functions: registry}
	result, err := executor.Run(context.Background(), RunRequest{
		Workflow:    workflow,
		WorkflowRef: "inline",
	})
	if !errors.Is(err, ErrRunCanceled) || result == nil {
		t.Fatalf("Run error = %v result=%#v, want cancel error with result", err, result)
	}
	if result.Status != RunStatusCanceled {
		t.Fatalf("status = %q, want canceled", result.Status)
	}
	run, err := store.GetRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.CancelReason != "test cancel" {
		t.Fatalf("cancel reason = %q, want test cancel", run.CancelReason)
	}
}

func TestExecutorRetryUsesPreviousRunInputsAndEvent(t *testing.T) {
	workspace := t.TempDir()
	registry := NewFunctionRegistry()
	_ = registry.Register(
		"echo",
		func(_ context.Context, args map[string]any, exec ExecutionContext) (map[string]any, error) {
			return map[string]any{"text": args["text"], "event": exec.Event["kind"]}, nil
		},
	)
	writeWorkflowFile(t, workspace, "retry.yml", `
name: Retry
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    outputs:
      text: ${{ steps.echo.outputs.text }}
    steps:
      - id: echo
        uses: function/echo
        with:
          text: ${{ inputs.text }}
`)
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store, Functions: registry}
	first, err := executor.Run(context.Background(), RunRequest{
		Ref:    "workflows/retry.yml",
		Inputs: map[string]any{"text": "again"},
		Event:  map[string]any{"kind": "manual"},
	})
	if err != nil {
		t.Fatalf("first Run failed: %v", err)
	}
	retry, err := executor.Retry(context.Background(), first.RunID, nil)
	if err != nil {
		t.Fatalf("Retry failed: %v", err)
	}
	retryRun, err := store.GetRun(context.Background(), retry.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if retryRun.RetryOfRunID != first.RunID {
		t.Fatalf("RetryOfRunID = %q, want %q", retryRun.RetryOfRunID, first.RunID)
	}
	if got := retryRun.Inputs["text"]; got != "again" {
		t.Fatalf("retry inputs = %#v", retryRun.Inputs)
	}
	if got := retryRun.Event["kind"]; got != "manual" {
		t.Fatalf("retry event = %#v", retryRun.Event)
	}
}

func TestExecutorEnforcesConcurrency(t *testing.T) {
	store := NewFileRunStore(t.TempDir())
	now := time.Now().UTC()
	if err := store.CreateRun(context.Background(), &Run{
		ID:          "running",
		WorkflowRef: "workflows/a.yml",
		Status:      RunStatusRunning,
		CreatedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, `
name: Limit
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	executor := &Executor{
		WorkspaceDir:      t.TempDir(),
		Store:             store,
		Functions:         NewFunctionRegistry(),
		MaxConcurrentRuns: 1,
	}
	_, err := executor.Run(context.Background(), RunRequest{Workflow: workflow, WorkflowRef: "inline"})
	if err == nil || !strings.Contains(err.Error(), "concurrency limit") {
		t.Fatalf("Run error = %v, want concurrency limit", err)
	}
}

func TestExecutorAppliesDefaultTimeout(t *testing.T) {
	registry := NewFunctionRegistry()
	_ = registry.Register(
		"wait",
		func(ctx context.Context, _ map[string]any, _ ExecutionContext) (map[string]any, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
				return map[string]any{"ok": true}, nil
			}
		},
	)
	workflow := parseWorkflow(t, `
name: Timeout
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: function/wait
`)
	result, err := (&Executor{
		WorkspaceDir:   t.TempDir(),
		Functions:      registry,
		DefaultTimeout: time.Millisecond,
	}).Run(context.Background(), RunRequest{Workflow: workflow, WorkflowRef: "inline"})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want deadline exceeded", err)
	}
	if result == nil || result.Status != RunStatusFailed {
		t.Fatalf("result = %#v, want failed timeout result", result)
	}
}

func writeWorkflowFile(t *testing.T, workspace, name, content string) {
	t.Helper()
	dir := filepath.Join(workspace, "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
