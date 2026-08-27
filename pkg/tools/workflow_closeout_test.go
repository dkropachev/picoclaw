package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestCloseoutWorkflowDispatchAndOperatorActions(t *testing.T) {
	if result := (*WorkflowTool)(nil).Execute(context.Background(), nil); result == nil || !result.IsError {
		t.Fatalf("nil workflow tool result = %#v", result)
	}
	if result := (&WorkflowTool{}).Execute(context.Background(), nil); result == nil || !result.IsError {
		t.Fatalf("unconfigured workflow result = %#v", result)
	}

	workspace := t.TempDir()
	writeWorkflowToolFile(t, workspace, "closeout.yml", `
name: Closeout
on:
  manual: {}
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	runtimeCompatibility := workflows.RuntimeCompatibility{
		PicoclawVersion: "v1.0.0",
		GitCommit:       "closeout",
	}
	store := workflows.NewFileRunStore(workspace)
	tool := newNoopWorkflowTool(t, workspace, runtimeCompatibility, store)
	if tool.Name() != WorkflowToolName || tool.Description() == "" || tool.Parameters()["type"] != "object" {
		t.Fatalf("workflow descriptor = %q %q %#v", tool.Name(), tool.Description(), tool.Parameters())
	}

	for _, action := range []string{"", "list", "compatibility", "reload"} {
		args := map[string]any{"action": action}
		result := tool.Execute(context.Background(), args)
		if result == nil || result.IsError {
			t.Errorf("workflow action %q = %#v", action, result)
		}
	}
	if result := tool.Execute(context.Background(), map[string]any{
		"action": "unsupported",
	}); result == nil || !result.IsError || !strings.Contains(result.ForLLM, "unsupported") {
		t.Fatalf("unsupported action = %#v", result)
	}
	if result := tool.Execute(context.Background(), map[string]any{
		"action": "validate",
	}); result == nil || !result.IsError {
		t.Fatalf("validate without ref = %#v", result)
	}
	if result := tool.Execute(context.Background(), map[string]any{
		"action": "validate",
		"ref":    "workflows/closeout.yml",
	}); result == nil || result.IsError || !strings.Contains(result.ForLLM, `"valid": true`) {
		t.Fatalf("validate workflow = %#v", result)
	}

	now := time.Now().UTC()
	run := &workflows.Run{
		ID:          "wr_closeout_operator",
		WorkflowRef: "workflows/closeout.yml",
		Status:      workflows.RunStatusRunning,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(context.Background(), workflows.RunEvent{
		RunID:   run.ID,
		Kind:    "workflow.closeout",
		Message: "safe event",
	}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"status", "events", "graph"} {
		result := tool.Execute(context.Background(), map[string]any{
			"action": action,
			"run_id": run.ID,
		})
		if result == nil || result.IsError {
			t.Errorf("workflow %s = %#v", action, result)
		}
	}
	canceled := tool.Execute(context.Background(), map[string]any{
		"action": "cancel",
		"run_id": run.ID,
		"reason": "operator closeout",
	})
	if canceled == nil || canceled.IsError || !strings.Contains(canceled.ForLLM, workflows.RunStatusCanceled) {
		t.Fatalf("cancel workflow = %#v", canceled)
	}

	for _, action := range []string{"status", "events", "graph", "cancel", "retry"} {
		result := tool.Execute(context.Background(), map[string]any{"action": action})
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "run_id is required") {
			t.Errorf("workflow %s without run id = %#v", action, result)
		}
	}
	for _, action := range []string{"run", "retry"} {
		args := map[string]any{
			"action":  action,
			"ref":     "workflows/closeout.yml",
			"run_id":  run.ID,
			"secrets": []string{"invalid"},
		}
		result := tool.Execute(context.Background(), args)
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "secrets must be an object") {
			t.Errorf("workflow %s invalid secrets = %#v", action, result)
		}
	}
	if result := tool.Execute(context.Background(), map[string]any{
		"action": "run",
	}); result == nil || !result.IsError || !strings.Contains(result.ForLLM, "ref is required") {
		t.Fatalf("run without ref = %#v", result)
	}
}

func TestCloseoutWorkflowStoreErrorBranches(t *testing.T) {
	injected := errors.New("injected workflow store failure")
	run := &workflows.Run{
		ID:          "wr_closeout_error",
		WorkflowRef: "workflows/missing.yml",
		Status:      workflows.RunStatusRunning,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}

	getFailure := &closeoutWorkflowStore{getErr: injected}
	tool := NewWorkflowTool(&workflows.Executor{Store: getFailure}, t.TempDir())
	for _, action := range []string{"status", "events", "graph", "retry"} {
		result := tool.Execute(context.Background(), map[string]any{
			"action": action,
			"run_id": run.ID,
		})
		if result == nil || !result.IsError {
			t.Errorf("%s get failure = %#v", action, result)
		}
	}

	eventsFailure := &closeoutWorkflowStore{run: run, eventsErr: injected}
	tool = NewWorkflowTool(&workflows.Executor{Store: eventsFailure}, t.TempDir())
	if result := tool.Execute(context.Background(), map[string]any{
		"action": "events",
		"run_id": run.ID,
	}); result == nil || !result.IsError {
		t.Fatalf("events failure = %#v", result)
	}

	cancelFailure := &closeoutWorkflowStore{run: run, cancelErr: injected}
	tool = NewWorkflowTool(&workflows.Executor{Store: cancelFailure}, t.TempDir())
	if result := tool.Execute(context.Background(), map[string]any{
		"action": "cancel",
		"run_id": run.ID,
	}); result == nil || !result.IsError {
		t.Fatalf("cancel failure = %#v", result)
	}

	privateRetry := &workflows.Run{
		ID:                "wr_closeout_private_retry",
		WorkflowRef:       "inline/private-closeout",
		ContextVisibility: workflows.WorkflowContextVisibilityPrivate,
		Status:            workflows.RunStatusSucceeded,
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
	}
	privateStore := &closeoutWorkflowStore{run: privateRetry}
	tool = NewWorkflowTool(&workflows.Executor{Store: privateStore}, t.TempDir())
	if result := tool.Execute(context.Background(), map[string]any{
		"action": "retry",
		"run_id": privateRetry.ID,
	}); result == nil || !result.IsError {
		t.Fatalf("private retry without captured context = %#v", result)
	}
}

func TestCloseoutWorkflowFilesystemAndExecutionErrors(t *testing.T) {
	invalidWorkspace := filepath.Join(t.TempDir(), "workspace-file")
	if err := os.WriteFile(invalidWorkspace, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewWorkflowTool(&workflows.Executor{}, invalidWorkspace)
	for _, action := range []string{
		"list", "compatibility", "revalidate", "reload", "dev_status", "dev_start",
	} {
		args := map[string]any{
			"action":     action,
			"prompt":     "cannot persist",
			"target_ref": "workflows/failure.yml",
		}
		if result := tool.Execute(context.Background(), args); result == nil || !result.IsError {
			t.Errorf("invalid-workspace %s = %#v", action, result)
		}
	}
	for _, ref := range []string{"workflows/missing.yml", "../escape.yml"} {
		if result := tool.Execute(context.Background(), map[string]any{
			"action": "validate",
			"ref":    ref,
		}); result == nil || !result.IsError {
			t.Errorf("validate invalid ref %q = %#v", ref, result)
		}
	}
	tool.ConfigureDevelopmentPublishGate(WorkflowDevelopmentPublishGateConfig{
		WorkflowsEnabled: true,
		DefinitionsDir:   workflows.DefaultDefinitionsDir,
		MaxCallDepth:     4,
		Resolver: workflows.WorkflowDependencyRuntimeResolverFunc(func(
			context.Context,
			workflows.WorkflowDependencyOccurrence,
		) workflows.WorkflowDependencyReadinessCode {
			return workflows.WorkflowDependencyReadinessReady
		}),
	})
	if result := tool.Execute(context.Background(), map[string]any{
		"action": "dev_publish",
	}); result == nil || !result.IsError {
		t.Fatalf("publish with unreadable development state = %#v", result)
	}

	workspace := t.TempDir()
	writeWorkflowToolFile(t, workspace, "semantic-invalid.yml", `
name: Semantic Invalid
on:
  manual: {}
jobs: {}
`)
	semanticTool := NewWorkflowTool(&workflows.Executor{}, workspace)
	if result := semanticTool.Execute(context.Background(), map[string]any{
		"action": "validate",
		"ref":    "workflows/semantic-invalid.yml",
	}); result == nil || !result.IsError {
		t.Fatalf("validate semantic-invalid workflow = %#v", result)
	}

	failingRegistry := workflows.NewFunctionRegistry()
	injected := errors.New("injected workflow function failure")
	if err := failingRegistry.Register(
		"fail",
		func(
			context.Context,
			map[string]any,
			workflows.ExecutionContext,
		) (map[string]any, error) {
			return nil, injected
		},
	); err != nil {
		t.Fatal(err)
	}
	writeWorkflowToolFile(t, workspace, "runtime-failure.yml", `
name: Runtime Failure
on:
  manual: {}
jobs:
  fail:
    runs-on: picoclaw
    steps:
      - uses: function/fail
`)
	runtimeCompatibility := workflows.RuntimeCompatibility{
		PicoclawVersion: "v1.0.0",
		GitCommit:       "closeout-failure",
	}
	if _, err := workflows.RevalidateLocal(
		context.Background(),
		workspace,
		runtimeCompatibility,
	); err != nil {
		t.Fatal(err)
	}
	failureTool := NewWorkflowTool(
		&workflows.Executor{
			WorkspaceDir: workspace,
			Store:        workflows.NewFileRunStore(workspace),
			Functions:    failingRegistry,
		},
		workspace,
		runtimeCompatibility,
	)
	if result := failureTool.Execute(context.Background(), map[string]any{
		"action": "run",
		"ref":    "workflows/runtime-failure.yml",
	}); result == nil || !result.IsError || !strings.Contains(result.ForLLM, injected.Error()) {
		t.Fatalf("runtime workflow failure = %#v", result)
	}
}

func TestCloseoutWorkflowDevelopmentFailureLifecycle(t *testing.T) {
	t.Run("no active session", func(t *testing.T) {
		workspace := t.TempDir()
		tool := newNoopWorkflowTool(
			t,
			workspace,
			workflows.RuntimeCompatibility{},
			workflows.NewFileRunStore(workspace),
		)
		if result := tool.Execute(context.Background(), map[string]any{
			"action": "dev_status",
		}); result == nil || result.IsError {
			t.Fatalf("development status = %#v", result)
		}
		for _, action := range []string{
			"dev_revise", "dev_validate", "dev_test", "dev_publish", "dev_discard",
		} {
			if result := tool.Execute(context.Background(), map[string]any{
				"action": action,
			}); result == nil || !result.IsError {
				t.Errorf("%s without session = %#v", action, result)
			}
		}
		if result := tool.Execute(context.Background(), map[string]any{
			"action": "dev_test",
			"prompt": "revise without a session",
		}); result == nil || !result.IsError {
			t.Fatalf("inline revise without session = %#v", result)
		}
	})

	t.Run("invalid draft", func(t *testing.T) {
		workspace := t.TempDir()
		tool := newNoopWorkflowTool(
			t,
			workspace,
			workflows.RuntimeCompatibility{},
			workflows.NewFileRunStore(workspace),
		)
		started := tool.Execute(context.Background(), map[string]any{
			"action":     "dev_start",
			"prompt":     "create closeout workflow",
			"target_ref": "workflows/closeout-invalid.yml",
		})
		if started == nil || started.IsError {
			t.Fatalf("start invalid draft session = %#v", started)
		}
		if status := tool.Execute(context.Background(), map[string]any{
			"action": "dev_status",
		}); status == nil || status.IsError {
			t.Fatalf("active development status = %#v", status)
		}
		revised := tool.Execute(context.Background(), map[string]any{
			"action": "dev_revise",
			"yaml":   "name: [unterminated",
		})
		if revised == nil || revised.IsError {
			t.Fatalf("record invalid draft = %#v", revised)
		}
		validated := tool.Execute(context.Background(), map[string]any{
			"action": "dev_validate",
		})
		if validated == nil || validated.IsError {
			t.Fatalf("validate invalid draft record = %#v", validated)
		}
		tested := tool.Execute(context.Background(), map[string]any{
			"action": "dev_test",
		})
		if tested == nil || !tested.IsError || !strings.Contains(tested.ForLLM, "not valid") {
			t.Fatalf("test invalid draft = %#v", tested)
		}
		published := tool.Execute(context.Background(), map[string]any{
			"action": "dev_publish",
		})
		if published == nil || !published.IsError {
			t.Fatalf("publish invalid draft = %#v", published)
		}
		if discarded := tool.Execute(context.Background(), map[string]any{
			"action": "dev_discard",
		}); discarded == nil || discarded.IsError {
			t.Fatalf("discard invalid draft = %#v", discarded)
		}
	})

	t.Run("inline valid revision test", func(t *testing.T) {
		workspace := t.TempDir()
		tool := newNoopWorkflowTool(
			t,
			workspace,
			workflows.RuntimeCompatibility{},
			workflows.NewFileRunStore(workspace),
		)
		if result := tool.Execute(context.Background(), map[string]any{
			"action":     "dev_start",
			"prompt":     "create valid closeout workflow",
			"target_ref": "workflows/closeout-valid.yml",
		}); result == nil || result.IsError {
			t.Fatalf("start valid draft = %#v", result)
		}
		invalidSecrets := tool.Execute(context.Background(), map[string]any{
			"action":  "dev_test",
			"prompt":  "use this exact draft",
			"secrets": []string{"invalid"},
			"yaml": `name: Closeout Valid
on:
  manual: {}
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`,
		})
		if invalidSecrets == nil || !invalidSecrets.IsError {
			t.Fatalf("development test invalid secrets = %#v", invalidSecrets)
		}
		result := tool.Execute(context.Background(), map[string]any{
			"action": "dev_test",
		})
		if result == nil || result.IsError {
			t.Fatalf("inline revised development test = %#v", result)
		}
		if discarded := tool.Execute(context.Background(), map[string]any{
			"action": "dev_discard",
		}); discarded == nil || discarded.IsError {
			t.Fatalf("discard tested draft = %#v", discarded)
		}
	})

	t.Run("semantic publish rejection", func(t *testing.T) {
		workspace := t.TempDir()
		tool := newNoopWorkflowTool(
			t,
			workspace,
			workflows.RuntimeCompatibility{},
			workflows.NewFileRunStore(workspace),
		)
		if result := tool.Execute(context.Background(), map[string]any{
			"action":     "dev_start",
			"prompt":     "create semantic invalid workflow",
			"target_ref": "workflows/semantic-invalid.yml",
		}); result == nil || result.IsError {
			t.Fatalf("start semantic invalid draft = %#v", result)
		}
		if result := tool.Execute(context.Background(), map[string]any{
			"action": "dev_revise",
			"yaml": `name: Semantic Invalid
on:
  manual: {}
jobs: {}
`,
		}); result == nil || result.IsError {
			t.Fatalf("revise semantic invalid draft = %#v", result)
		}
		if result := tool.Execute(context.Background(), map[string]any{
			"action": "dev_publish",
		}); result == nil || !result.IsError {
			t.Fatalf("publish semantic invalid draft = %#v", result)
		}
		if result := tool.Execute(context.Background(), map[string]any{
			"action": "dev_discard",
		}); result == nil || result.IsError {
			t.Fatalf("discard semantic invalid draft = %#v", result)
		}
	})

	t.Run("runtime failure and canceled gate", func(t *testing.T) {
		workspace := t.TempDir()
		registry := workflows.NewFunctionRegistry()
		injected := errors.New("development runtime failure")
		if err := registry.Register(
			"fail",
			func(
				context.Context,
				map[string]any,
				workflows.ExecutionContext,
			) (map[string]any, error) {
				return nil, injected
			},
		); err != nil {
			t.Fatal(err)
		}
		tool := NewWorkflowTool(
			&workflows.Executor{
				WorkspaceDir: workspace,
				Store:        workflows.NewFileRunStore(workspace),
				Functions:    registry,
			},
			workspace,
		).ConfigureDevelopmentPublishGate(WorkflowDevelopmentPublishGateConfig{
			WorkflowsEnabled: true,
			DefinitionsDir:   workflows.DefaultDefinitionsDir,
			MaxCallDepth:     4,
			Resolver: workflows.WorkflowDependencyRuntimeResolverFunc(func(
				context.Context,
				workflows.WorkflowDependencyOccurrence,
			) workflows.WorkflowDependencyReadinessCode {
				return workflows.WorkflowDependencyReadinessReady
			}),
		})
		if result := tool.Execute(context.Background(), map[string]any{
			"action":     "dev_start",
			"prompt":     "create failing development workflow",
			"target_ref": "workflows/development-failure.yml",
		}); result == nil || result.IsError {
			t.Fatalf("start failing development = %#v", result)
		}
		const failingYAML = `name: Development Failure
on:
  manual: {}
jobs:
  fail:
    runs-on: picoclaw
    steps:
      - uses: function/fail
`
		if result := tool.Execute(context.Background(), map[string]any{
			"action": "dev_revise",
			"yaml":   failingYAML,
		}); result == nil || result.IsError {
			t.Fatalf("revise failing development = %#v", result)
		}
		if result := tool.Execute(context.Background(), map[string]any{
			"action": "dev_test",
		}); result == nil || !result.IsError || !strings.Contains(result.ForLLM, injected.Error()) {
			t.Fatalf("test failing development = %#v", result)
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if result := tool.Execute(canceled, map[string]any{
			"action": "dev_publish",
		}); result == nil || !result.IsError || !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("publish with canceled gate = %#v", result)
		}
		if result := tool.Execute(context.Background(), map[string]any{
			"action": "dev_discard",
		}); result == nil || result.IsError {
			t.Fatalf("discard failed development = %#v", result)
		}
	})
}

func TestCloseoutWorkflowPureHelperBranches(t *testing.T) {
	executor := &workflows.Executor{DefinitionsDir: "custom-definitions"}
	runtimeCompatibility := workflows.RuntimeCompatibility{PicoclawVersion: "v1"}
	tool := NewWorkflowTool(executor, " workspace ", runtimeCompatibility)
	if tool.workspace != "workspace" || tool.definitionsDir != "custom-definitions" ||
		executor.RuntimeCompatibility.PicoclawVersion != "v1" {
		t.Fatalf("workflow constructor projection = %#v", tool)
	}
	if (*WorkflowTool)(nil).localOptions() != nil {
		t.Fatal("nil workflow tool returned local options")
	}
	if (&WorkflowTool{}).localOptions() != nil || len(tool.localOptions()) != 1 {
		t.Fatal("workflow local options were not normalized")
	}
	if (&WorkflowTool{executor: &workflows.Executor{}, workspace: t.TempDir()}).executorStore() == nil {
		t.Fatal("workflow fallback store is nil")
	}

	ctx := WithToolContext(context.Background(), "channel", "chat")
	ctx = WithToolTopicContext(ctx, "topic")
	ctx = WithToolMessageContext(ctx, "message", "reply")
	delivery := deliveryFromToolContext(ctx)
	if delivery.Channel != "channel" || delivery.ChatID != "chat" ||
		delivery.TopicID != "topic" || delivery.MessageID != "message" ||
		delivery.ReplyToMessageID != "reply" {
		t.Fatalf("workflow delivery = %#v", delivery)
	}

	injected := errors.New("closeout error")
	if result := jsonToolResult(make(chan int)); result == nil || !result.IsError {
		t.Fatalf("jsonToolResult marshal failure = %#v", result)
	}
	if result := jsonErrorToolResult(map[string]any{"status": "partial"}, injected); result == nil ||
		!result.IsError || !strings.Contains(result.ForLLM, "partial") {
		t.Fatalf("jsonErrorToolResult partial = %#v", result)
	}
	for _, value := range []any{nil, make(chan int)} {
		if result := jsonErrorToolResult(value, injected); result == nil || !result.IsError ||
			!strings.Contains(result.ForLLM, injected.Error()) {
			t.Fatalf("jsonErrorToolResult fallback = %#v", result)
		}
	}

	args := map[string]any{
		"text":     " value ",
		"optional": "",
		"truth":    true,
	}
	if workflowStringArg(args, "text") != "value" || !workflowBoolArg(args, "truth") ||
		workflowBoolArg(args, "missing") {
		t.Fatal("workflow scalar argument parsing was inconsistent")
	}
	if workflowOptionalStringArg(args, "missing") != nil ||
		workflowOptionalStringArg(map[string]any{"x": nil}, "x") != nil ||
		workflowOptionalStringArg(map[string]any{"x": 1}, "x") != nil {
		t.Fatal("invalid optional workflow strings were retained")
	}
	optional := workflowOptionalStringArg(args, "optional")
	if optional == nil || *optional != "" {
		t.Fatalf("present empty optional workflow string = %#v", optional)
	}
	if secrets, err := workflowStringMapArg(nil); err != nil || secrets != nil {
		t.Fatalf("nil workflow secrets = %#v, %v", secrets, err)
	}
	if secrets, err := workflowStringMapArg(map[string]any{"token": "safe"}); err != nil ||
		secrets["token"] != "safe" {
		t.Fatalf("workflow secrets = %#v, %v", secrets, err)
	}
	for _, raw := range []any{"bad", map[string]any{"token": 1}} {
		if _, err := workflowStringMapArg(raw); err == nil {
			t.Fatalf("workflowStringMapArg(%#v) succeeded", raw)
		}
	}
}

type closeoutWorkflowStore struct {
	run       *workflows.Run
	events    []workflows.RunEvent
	getErr    error
	eventsErr error
	cancelErr error
}

func (*closeoutWorkflowStore) CreateRun(context.Context, *workflows.Run) error { return nil }

func (*closeoutWorkflowStore) UpdateRun(context.Context, *workflows.Run) error { return nil }

func (store *closeoutWorkflowStore) CancelRun(
	context.Context,
	string,
	string,
) (*workflows.Run, error) {
	if store.cancelErr != nil {
		return nil, store.cancelErr
	}
	return store.run, nil
}

func (store *closeoutWorkflowStore) GetRun(context.Context, string) (*workflows.Run, error) {
	if store.getErr != nil {
		return nil, store.getErr
	}
	return store.run, nil
}

func (*closeoutWorkflowStore) ListRuns(context.Context) ([]workflows.Run, error) { return nil, nil }

func (*closeoutWorkflowStore) AppendEvent(context.Context, workflows.RunEvent) error { return nil }

func (store *closeoutWorkflowStore) Events(context.Context, string) ([]workflows.RunEvent, error) {
	if store.eventsErr != nil {
		return nil, store.eventsErr
	}
	return store.events, nil
}

func (*closeoutWorkflowStore) DeleteRun(context.Context, string) error { return nil }

func (*closeoutWorkflowStore) PruneTerminalRuns(context.Context, time.Time) (int, error) {
	return 0, nil
}

func TestCloseoutWorkflowFileFixturesRemainPrivate(t *testing.T) {
	workspace := t.TempDir()
	tool := NewWorkflowTool(&workflows.Executor{}, workspace)
	tool.definitionsDir = "missing-definitions"
	result := tool.Execute(context.Background(), map[string]any{"action": "list"})
	if result == nil || result.IsError {
		t.Fatalf("list missing definitions = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(workspace, "workflows")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("list created default definitions directory: %v", err)
	}
}
