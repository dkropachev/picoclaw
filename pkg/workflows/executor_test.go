package workflows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
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

type fakeRuntimeEventPublisher struct {
	events []runtimeevents.Event
}

func (p *fakeRuntimeEventPublisher) PublishNonBlocking(evt runtimeevents.Event) runtimeevents.PublishResult {
	p.events = append(p.events, evt)
	return runtimeevents.PublishResult{Matched: 1, Delivered: 1}
}

type cancelOnSucceededUpdateStore struct {
	RunStore
	canceled bool
}

func (s *cancelOnSucceededUpdateStore) UpdateRun(ctx context.Context, run *Run) error {
	if run != nil && run.Status == RunStatusSucceeded && !s.canceled {
		s.canceled = true
		now := time.Now().UTC()
		canceled := cloneRun(run)
		canceled.Status = RunStatusCanceled
		canceled.CancelReason = "late cancel"
		canceled.CancelRequestedAt = &now
		canceled.CompletedAt = &now
		canceled.UpdatedAt = now
		if err := s.RunStore.UpdateRun(ctx, canceled); err != nil {
			return err
		}
		*run = *cloneRun(canceled)
		return nil
	}
	return s.RunStore.UpdateRun(ctx, run)
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

func TestExecutorPublishesWorkflowLifecycleEvents(t *testing.T) {
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
name: Events
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: echo
        uses: function/echo
        with:
          text: hello
`)
	workspace := t.TempDir()
	publisher := &fakeRuntimeEventPublisher{}
	result, err := (&Executor{
		WorkspaceDir:  workspace,
		Store:         NewFileRunStore(workspace),
		Functions:     registry,
		RuntimeEvents: publisher,
	}).Run(context.Background(), RunRequest{
		Workflow:    workflow,
		WorkflowRef: "workflows/events.yml",
		Session:     "workflow:test",
		Delivery:    Delivery{Channel: "slack", ChatID: "C123", TopicID: "T1", MessageID: "m1"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
	if len(publisher.events) == 0 {
		t.Fatal("runtime events len = 0, want lifecycle events")
	}
	var sawStart, sawEnd bool
	for _, evt := range publisher.events {
		if evt.Source.Component != "workflow" || evt.Source.Name != "workflows/events.yml" {
			t.Fatalf("runtime event source = %#v, want workflow ref", evt.Source)
		}
		if evt.Scope.SessionKey != "workflow:test" ||
			evt.Scope.Channel != "slack" ||
			evt.Scope.ChatID != "C123" {
			t.Fatalf("runtime event scope = %#v, want run session and delivery", evt.Scope)
		}
		switch evt.Kind {
		case runtimeevents.KindWorkflowRunStart:
			sawStart = true
		case runtimeevents.KindWorkflowRunEnd:
			sawEnd = true
		}
	}
	if !sawStart || !sawEnd {
		t.Fatalf("runtime event kinds = %#v, want run start and end", publisher.events)
	}
}

func TestExecutorCancelRunPublishesRuntimeEvent(t *testing.T) {
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	now := time.Now().UTC()
	run := &Run{
		ID:          "wr_cancel",
		WorkflowRef: "workflows/cancel.yml",
		Status:      RunStatusRunning,
		Session:     "workflow:cancel",
		Delivery:    Delivery{Channel: "slack", ChatID: "C123"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	publisher := &fakeRuntimeEventPublisher{}
	executor := &Executor{WorkspaceDir: workspace, Store: store, RuntimeEvents: publisher}
	canceled, err := executor.CancelRun(context.Background(), run.ID, "operator cancel")
	if err != nil {
		t.Fatalf("CancelRun() error = %v", err)
	}
	if canceled.Status != RunStatusCanceled {
		t.Fatalf("status = %q, want canceled", canceled.Status)
	}
	var sawCanceled bool
	for _, evt := range publisher.events {
		if evt.Kind == runtimeevents.KindWorkflowRunCanceled &&
			evt.Source.Name == "workflows/cancel.yml" &&
			evt.Scope.SessionKey == "workflow:cancel" {
			sawCanceled = true
		}
	}
	if !sawCanceled {
		t.Fatalf("runtime events = %#v, want canceled lifecycle event", publisher.events)
	}
}

func TestExecutorPublishesCanceledRuntimeEventWhenFinalUpdatePreservesCancel(t *testing.T) {
	registry := NewFunctionRegistry()
	if err := registry.Register(
		"noop",
		func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, `
name: Late Cancel
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	workspace := t.TempDir()
	store := &cancelOnSucceededUpdateStore{RunStore: NewFileRunStore(workspace)}
	publisher := &fakeRuntimeEventPublisher{}
	result, err := (&Executor{
		WorkspaceDir:  workspace,
		Store:         store,
		Functions:     registry,
		RuntimeEvents: publisher,
	}).Run(context.Background(), RunRequest{
		Workflow:    workflow,
		WorkflowRef: "workflows/late-cancel.yml",
	})
	if !errors.Is(err, ErrRunCanceled) || result == nil {
		t.Fatalf("Run() error = %v result=%#v, want canceled result", err, result)
	}
	if result.Status != RunStatusCanceled {
		t.Fatalf("status = %q, want canceled", result.Status)
	}
	var sawCanceled bool
	for _, evt := range publisher.events {
		if evt.Kind == runtimeevents.KindWorkflowRunCanceled &&
			evt.Source.Name == "workflows/late-cancel.yml" {
			sawCanceled = true
		}
	}
	if !sawCanceled {
		t.Fatalf("runtime events = %#v, want canceled lifecycle event", publisher.events)
	}
}

func TestExecutorPassesAgentOutputContractManagedAndScope(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Agent Output
on:
  workflow_call:
    outputs:
      result:
        value: ${{ jobs.main.outputs.result }}
jobs:
  main:
    runs-on: picoclaw
    outputs:
      result: ${{ steps.review.outputs.text }}
    steps:
      - id: review
        uses: agent/reviewer
        with:
          managed: auto
          prompt: Review the scope.
          scope:
            - id: a
              type: file
          output:
            format: json
            schema:
              type: object
              required: [summary]
              properties:
                summary:
                  type: string
`)
	agents := &fakeAgentRunner{outputs: map[string]any{"text": `{"summary":"ok"}`}}
	result, err := (&Executor{WorkspaceDir: t.TempDir(), Agents: agents}).Run(context.Background(), RunRequest{
		Workflow:    workflow,
		WorkflowRef: "inline",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
	if len(agents.requests) != 1 {
		t.Fatalf("agent requests = %d, want 1", len(agents.requests))
	}
	req := agents.requests[0]
	if req.Output == nil || req.Output.Format != "json" {
		t.Fatalf("output contract = %#v, want json contract", req.Output)
	}
	if req.Managed != "auto" {
		t.Fatalf("managed = %#v, want auto", req.Managed)
	}
	scope, ok := req.Scope.([]any)
	if !ok || len(scope) != 1 {
		t.Fatalf("scope = %#v, want one scope item", req.Scope)
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

func TestExecutorReusableWorkflowRequiresCurrentValidationStamp(t *testing.T) {
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
      result:
        value: ${{ jobs.child.outputs.result }}
jobs:
  child:
    runs-on: picoclaw
    outputs:
      result: ${{ steps.make.outputs.text }}
    steps:
      - id: make
        uses: function/prefix
        with:
          text: ${{ inputs.text }}
`)
	writeWorkflowFile(t, workspace, "parent.yml", `
name: Parent
on:
  workflow_call:
    outputs:
      result:
        value: ${{ jobs.call.outputs.result }}
jobs:
  call:
    uses: workflows/child.yml
    with:
      text: from-parent
`)
	runtime := RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"}
	if _, err := RevalidateLocal(context.Background(), workspace, runtime); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}
	writeWorkflowFile(t, workspace, "child.yml", `
name: Child
on:
  workflow_call:
    inputs:
      text:
        type: string
        required: true
    outputs:
      result:
        value: ${{ jobs.child.outputs.result }}
jobs:
  child:
    runs-on: picoclaw
    outputs:
      result: ${{ steps.make.outputs.text }}
    steps:
      - id: make
        uses: function/prefix
        with:
          text: changed-${{ inputs.text }}
	`)
	registry := NewFunctionRegistry()
	_ = registry.Register(
		"prefix",
		func(_ context.Context, args map[string]any, _ ExecutionContext) (map[string]any, error) {
			return map[string]any{"text": args["text"]}, nil
		},
	)
	store := NewFileRunStore(workspace)
	result, err := (&Executor{
		WorkspaceDir:         workspace,
		Store:                store,
		Functions:            registry,
		RuntimeCompatibility: runtime,
	}).Run(context.Background(), RunRequest{Ref: "workflows/parent.yml"})
	if err == nil {
		t.Fatal("Run() error = nil, want stale child validation error")
	}
	if !strings.Contains(err.Error(), "workflows/child.yml must be revalidated") {
		t.Fatalf("Run() error = %v, want child revalidation error", err)
	}
	if result == nil || result.Status != RunStatusFailed {
		t.Fatalf("result = %#v, want failed result", result)
	}
	parentRun, getErr := store.GetRun(context.Background(), result.RunID)
	if getErr != nil {
		t.Fatalf("GetRun() error = %v", getErr)
	}
	if len(parentRun.ChildRunIDs) != 0 {
		t.Fatalf("child run ids = %#v, want none for stale child", parentRun.ChildRunIDs)
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

func TestExecutorRejectsMissingMappedReusableWorkflowSecret(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "child-secret.yml", `
name: Child
on:
  workflow_call:
    secrets:
      child_token:
        required: true
jobs:
  child:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	parent := parseWorkflow(t, `
name: Parent
on:
  manual: {}
jobs:
  call:
    uses: workflows/child-secret.yml
    secrets:
      child_token: ${{ secrets.parent_token_typo }}
`)

	result, err := (&Executor{WorkspaceDir: workspace, Functions: NewFunctionRegistry()}).Run(
		context.Background(),
		RunRequest{Workflow: parent, WorkflowRef: "workflows/parent.yml"},
	)
	if err == nil {
		t.Fatal("Run succeeded, want missing mapped secret error")
	}
	if result == nil || result.Status != RunStatusFailed {
		t.Fatalf("result = %#v, want failed result", result)
	}
	if !strings.Contains(err.Error(), `mapped workflow secret "child_token" is missing`) {
		t.Fatalf("Run error = %v, want missing mapped secret error", err)
	}
}

func TestExecutorRejectsMissingMappedReusableWorkflowSecretInMap(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "child-secret.yml", `
name: Child
on:
  workflow_call:
    secrets:
      child_token:
        required: true
jobs:
  child:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	parent := parseWorkflow(t, `
name: Parent
on:
  manual: {}
jobs:
  call:
    uses: workflows/child-secret.yml
    secrets:
      child_token:
        part: ${{ secrets.parent_token_typo }}
`)

	result, err := (&Executor{WorkspaceDir: workspace, Functions: NewFunctionRegistry()}).Run(
		context.Background(),
		RunRequest{Workflow: parent, WorkflowRef: "workflows/parent.yml"},
	)
	if err == nil {
		t.Fatal("Run succeeded, want missing mapped secret error")
	}
	if result == nil || result.Status != RunStatusFailed {
		t.Fatalf("result = %#v, want failed result", result)
	}
	if !strings.Contains(err.Error(), `mapped workflow secret "child_token" is missing`) {
		t.Fatalf("Run error = %v, want missing mapped secret error", err)
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

func TestExecutorStepJobContinueOnError(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Job Continue
on:
  workflow_call:
    outputs:
      ok:
        value: ${{ jobs.after.outputs.ok }}
jobs:
  main:
    continue-on-error: true
    runs-on: picoclaw
    steps:
      - uses: function/fail
  after:
    needs: main
    runs-on: picoclaw
    outputs:
      ok: ${{ steps.ok.outputs.text }}
    steps:
      - id: ok
        uses: function/ok
`)
	registry := NewFunctionRegistry()
	_ = registry.Register("fail", func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		return nil, errors.New("step failed")
	})
	_ = registry.Register("ok", func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		return map[string]any{"text": "after ran"}, nil
	})
	store := NewFileRunStore(t.TempDir())
	result, err := (&Executor{WorkspaceDir: t.TempDir(), Store: store, Functions: registry}).Run(
		context.Background(),
		RunRequest{Workflow: workflow, WorkflowRef: "inline"},
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
	if got := result.Outputs["ok"]; got != "after ran" {
		t.Fatalf("output ok = %#v, want after ran", got)
	}
	run, err := store.GetRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if run.Jobs["main"].Status != RunStatusSucceeded || run.Jobs["main"].Error == "" {
		t.Fatalf("main job = %#v, want succeeded with preserved error", run.Jobs["main"])
	}
}

func TestExecutorStepContinueOnErrorPreservesOutputs(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Step Continue
on:
  workflow_call:
    outputs:
      partial:
        value: ${{ jobs.main.outputs.partial }}
jobs:
  main:
    runs-on: picoclaw
    outputs:
      partial: ${{ steps.fail.outputs.partial }}
    steps:
      - id: fail
        uses: function/partial
        continue-on-error: true
`)
	registry := NewFunctionRegistry()
	_ = registry.Register("partial", func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		return map[string]any{"partial": "kept"}, errors.New("partial failure")
	})
	store := NewFileRunStore(t.TempDir())
	result, err := (&Executor{WorkspaceDir: t.TempDir(), Store: store, Functions: registry}).Run(
		context.Background(),
		RunRequest{Workflow: workflow, WorkflowRef: "inline"},
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := result.Outputs["partial"]; got != "kept" {
		t.Fatalf("partial output = %#v, want kept", got)
	}
	run, err := store.GetRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	step := run.Steps["main/fail"]
	if step.Status != RunStatusSucceeded || step.Error == "" || step.Outputs["partial"] != "kept" {
		t.Fatalf("continued step = %#v, want succeeded with error and outputs", step)
	}
}

func TestExecutorFailedStepPreservesOutputs(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Step Failure
on:
  workflow_call:
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: fail
        uses: function/partial
`)
	registry := NewFunctionRegistry()
	_ = registry.Register("partial", func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		return map[string]any{"structured_error": "invalid payload"}, errors.New("partial failure")
	})
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	result, err := (&Executor{WorkspaceDir: workspace, Store: store, Functions: registry}).Run(
		context.Background(),
		RunRequest{Workflow: workflow, WorkflowRef: "inline"},
	)
	if err == nil {
		t.Fatal("Run() error = nil, want step failure")
	}
	if result == nil || result.Status != RunStatusFailed {
		t.Fatalf("result = %#v, want failed result", result)
	}
	run, getErr := store.GetRun(context.Background(), result.RunID)
	if getErr != nil {
		t.Fatalf("GetRun() error = %v", getErr)
	}
	step := run.Steps["main/fail"]
	if step.Status != RunStatusFailed || step.Error == "" || step.Outputs["structured_error"] != "invalid payload" {
		t.Fatalf("failed step = %#v, want failed with error and outputs", step)
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

func TestExecutorCancelParentStopsChildBeforeNextStep(t *testing.T) {
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	writeWorkflowFile(t, workspace, "child-cancel.yml", `
name: Child
on:
  workflow_call: {}
jobs:
  child:
    runs-on: picoclaw
    steps:
      - id: cancel
        uses: function/cancel-parent
      - id: after
        uses: function/after
`)
	parent := parseWorkflow(t, `
name: Parent
on:
  manual: {}
jobs:
  call:
    uses: workflows/child-cancel.yml
`)
	registry := NewFunctionRegistry()
	if err := registry.Register(
		"cancel-parent",
		func(ctx context.Context, _ map[string]any, exec ExecutionContext) (map[string]any, error) {
			child, err := store.GetRun(ctx, exec.RunID)
			if err != nil {
				return nil, err
			}
			if child.ParentRunID == "" {
				return nil, errors.New("child parent run id is empty")
			}
			_, err = store.CancelRun(ctx, child.ParentRunID, "operator cancel")
			return map[string]any{"ok": true}, err
		},
	); err != nil {
		t.Fatal(err)
	}
	afterCalled := false
	if err := registry.Register(
		"after",
		func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
			afterCalled = true
			return map[string]any{"ok": true}, nil
		},
	); err != nil {
		t.Fatal(err)
	}

	result, err := (&Executor{
		WorkspaceDir: workspace,
		Store:        store,
		Functions:    registry,
	}).Run(context.Background(), RunRequest{Workflow: parent, WorkflowRef: "workflows/parent.yml"})
	if err == nil || !errors.Is(err, ErrRunCanceled) {
		t.Fatalf("Run error = %v, want cancellation", err)
	}
	if result == nil || result.Status != RunStatusCanceled {
		t.Fatalf("result = %#v, want canceled result", result)
	}
	if afterCalled {
		t.Fatal("child step after parent cancel ran")
	}
	runs, err := store.ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	childCanceled := false
	for _, run := range runs {
		if run.ParentRunID == result.RunID {
			childCanceled = run.Status == RunStatusCanceled
		}
	}
	if !childCanceled {
		t.Fatalf("runs = %#v, want canceled child run", runs)
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
