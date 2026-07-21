package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWorkflowToolParametersIncludeAllOperatorActions(t *testing.T) {
	tool := NewWorkflowTool(&workflows.Executor{}, t.TempDir())
	params := tool.Parameters()
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", params["properties"])
	}
	action, ok := props["action"].(map[string]any)
	if !ok {
		t.Fatalf("action = %#v", props["action"])
	}
	rawEnum, ok := action["enum"].([]string)
	if !ok {
		t.Fatalf("action enum = %#v", action["enum"])
	}
	for _, want := range []string{
		"list",
		"compatibility",
		"revalidate",
		"validate",
		"reload",
		"run",
		"status",
		"events",
		"graph",
		"cancel",
		"retry",
		"dev_status",
		"dev_start",
		"dev_revise",
		"dev_validate",
		"dev_test",
		"dev_publish",
		"dev_discard",
	} {
		if !slices.Contains(rawEnum, want) {
			t.Fatalf("action enum %v missing %q", rawEnum, want)
		}
	}
}

func TestWorkflowToolReloadAction(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowToolFile(t, workspace, "summarize.yml", `
name: Summarize
on:
  manual: {}
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	tool := NewWorkflowTool(&workflows.Executor{}, workspace)

	result := tool.Execute(context.Background(), map[string]any{"action": "reload"})
	if result == nil || result.IsError {
		t.Fatalf("reload result = %#v", result)
	}
	var payload struct {
		Workflows []workflows.Definition `json:"workflows"`
		Errors    []any                  `json:"errors"`
	}
	if err := json.Unmarshal([]byte(result.ContentForLLM()), &payload); err != nil {
		t.Fatalf("unmarshal reload result: %v\n%s", err, result.ContentForLLM())
	}
	if len(payload.Errors) != 0 {
		t.Fatalf("reload errors = %#v", payload.Errors)
	}
	if len(payload.Workflows) != 1 || payload.Workflows[0].Ref != "workflows/summarize.yml" {
		t.Fatalf("workflows = %#v", payload.Workflows)
	}
}

func TestWorkflowToolRevalidateActionReturnsCompatibilitySummary(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	writeWorkflowToolFile(t, workspace, "summarize.yml", `
name: Summarize
on:
  manual: {}
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	runtime := workflows.RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"}
	tool := newNoopWorkflowTool(t, workspace, runtime, workflows.NewFileRunStore(workspace))

	result := tool.Execute(ctx, map[string]any{"action": "revalidate"})
	if result == nil || result.IsError {
		t.Fatalf("revalidate result = %#v", result)
	}
	var summary workflows.WorkflowCompatibilitySummary
	if err := json.Unmarshal([]byte(result.ContentForLLM()), &summary); err != nil {
		t.Fatalf("unmarshal revalidate result: %v\n%s", err, result.ContentForLLM())
	}
	if summary.HasBlocking {
		t.Fatalf("summary = %#v, want no blocking workflows", summary)
	}
	if summary.Counts[workflows.WorkflowValidationStatusValid] != 1 {
		t.Fatalf("summary counts = %#v, want one valid workflow", summary.Counts)
	}
}

func TestWorkflowToolRunRequiresCurrentRevalidation(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	writeWorkflowToolFile(t, workspace, "run.yml", `
name: Run
on:
  manual: {}
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	runtime := workflows.RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"}
	store := workflows.NewFileRunStore(workspace)
	tool := newNoopWorkflowTool(t, workspace, runtime, store)

	result := tool.Execute(ctx, map[string]any{
		"action": "run",
		"ref":    "workflows/run.yml",
	})
	if result == nil || !result.IsError {
		t.Fatalf("run result before revalidation = %#v, want error", result)
	}
	if got := result.ContentForLLM(); !strings.Contains(got, "must be revalidated") {
		t.Fatalf("run error = %q, want revalidation error", got)
	}
	if got := workflowToolRunCount(t, ctx, store); got != 0 {
		t.Fatalf("run count before revalidation = %d, want 0", got)
	}

	if _, err := workflows.RevalidateLocal(ctx, workspace, runtime); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}
	result = tool.Execute(ctx, map[string]any{
		"action": "run",
		"ref":    "workflows/run.yml",
	})
	if result == nil || result.IsError {
		t.Fatalf("run result after revalidation = %#v", result)
	}
	var payload workflows.RunResult
	if err := json.Unmarshal([]byte(result.ContentForLLM()), &payload); err != nil {
		t.Fatalf("unmarshal run result: %v\n%s", err, result.ContentForLLM())
	}
	if payload.Status != workflows.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded", payload.Status)
	}
	if got := workflowToolRunCount(t, ctx, store); got != 1 {
		t.Fatalf("run count after revalidation = %d, want 1", got)
	}
}

func TestWorkflowToolRetryRequiresCurrentRevalidation(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	writeWorkflowToolFile(t, workspace, "retry.yml", `
name: Retry
on:
  manual: {}
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: function/noop
`)
	runtime := workflows.RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"}
	store := workflows.NewFileRunStore(workspace)
	now := time.Now().UTC()
	if err := store.CreateRun(ctx, &workflows.Run{
		ID:          "wr_previous",
		WorkflowRef: "workflows/retry.yml",
		Status:      workflows.RunStatusSucceeded,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}
	tool := newNoopWorkflowTool(t, workspace, runtime, store)

	result := tool.Execute(ctx, map[string]any{
		"action": "retry",
		"run_id": "wr_previous",
	})
	if result == nil || !result.IsError {
		t.Fatalf("retry result before revalidation = %#v, want error", result)
	}
	if got := result.ContentForLLM(); !strings.Contains(got, "must be revalidated") {
		t.Fatalf("retry error = %q, want revalidation error", got)
	}
	if got := workflowToolRunCount(t, ctx, store); got != 1 {
		t.Fatalf("run count before retry revalidation = %d, want 1", got)
	}

	if _, err := workflows.RevalidateLocal(ctx, workspace, runtime); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}
	result = tool.Execute(ctx, map[string]any{
		"action": "retry",
		"run_id": "wr_previous",
	})
	if result == nil || result.IsError {
		t.Fatalf("retry result after revalidation = %#v", result)
	}
	var payload workflows.RunResult
	if err := json.Unmarshal([]byte(result.ContentForLLM()), &payload); err != nil {
		t.Fatalf("unmarshal retry result: %v\n%s", err, result.ContentForLLM())
	}
	if payload.Status != workflows.RunStatusSucceeded {
		t.Fatalf("retry status = %q, want succeeded", payload.Status)
	}
	runs, err := store.ListRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs after retry = %#v, want 2", runs)
	}
	var retryRun workflows.Run
	for _, run := range runs {
		if run.ID != "wr_previous" {
			retryRun = run
		}
	}
	if retryRun.RetryOfRunID != "wr_previous" {
		t.Fatalf("retry_of_run_id = %q, want wr_previous", retryRun.RetryOfRunID)
	}
}

func TestWorkflowToolDevelopmentLifecyclePublishesValidatedWorkflow(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	runtime := workflows.RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"}
	store := workflows.NewFileRunStore(workspace)
	tool := newNoopWorkflowTool(t, workspace, runtime, store)

	result := tool.Execute(ctx, map[string]any{
		"action":     "dev_start",
		"prompt":     "create a native smoke workflow",
		"target_ref": "workflows/native-smoke.yml",
	})
	if result == nil || result.IsError {
		t.Fatalf("dev_start result = %#v", result)
	}
	var started struct {
		Session *workflows.WorkflowDevelopmentSession `json:"session"`
	}
	if err := json.Unmarshal([]byte(result.ContentForLLM()), &started); err != nil {
		t.Fatalf("unmarshal dev_start result: %v\n%s", err, result.ContentForLLM())
	}
	if started.Session == nil || started.Session.TargetWorkflowRef != "workflows/native-smoke.yml" {
		t.Fatalf("started session = %#v", started.Session)
	}

	conflict := tool.Execute(ctx, map[string]any{
		"action": "dev_start",
		"prompt": "second workflow",
	})
	if conflict == nil || !conflict.IsError || !strings.Contains(conflict.ContentForLLM(), "already active") {
		t.Fatalf("second dev_start result = %#v, want active-session conflict", conflict)
	}

	draftYAML := `name: Native Smoke
on:
  manual: {}
jobs:
  test:
    runs-on: picoclaw
    steps:
      - id: ok
        uses: function/noop
`
	result = tool.Execute(ctx, map[string]any{
		"action": "dev_revise",
		"yaml":   draftYAML,
	})
	if result == nil || result.IsError {
		t.Fatalf("dev_revise result = %#v", result)
	}

	result = tool.Execute(ctx, map[string]any{"action": "dev_test"})
	if result == nil || result.IsError {
		t.Fatalf("dev_test result = %#v", result)
	}
	var tested struct {
		Session *workflows.WorkflowDevelopmentSession `json:"session"`
		Result  *workflows.RunResult                  `json:"result"`
	}
	if err := json.Unmarshal([]byte(result.ContentForLLM()), &tested); err != nil {
		t.Fatalf("unmarshal dev_test result: %v\n%s", err, result.ContentForLLM())
	}
	if tested.Session == nil ||
		tested.Session.Status != workflows.WorkflowDevelopmentStatusReadyToPublish ||
		tested.Session.LastTest == nil ||
		tested.Session.LastTest.Status != workflows.RunStatusSucceeded {
		t.Fatalf("tested session = %#v, want ready successful test", tested.Session)
	}
	if tested.Result == nil || tested.Result.Status != workflows.RunStatusSucceeded {
		t.Fatalf("test result = %#v, want succeeded", tested.Result)
	}

	result = tool.Execute(ctx, map[string]any{"action": "dev_publish"})
	if result == nil || result.IsError {
		t.Fatalf("dev_publish result = %#v", result)
	}
	var published workflows.WorkflowDevelopmentPublishResult
	if err := json.Unmarshal([]byte(result.ContentForLLM()), &published); err != nil {
		t.Fatalf("unmarshal dev_publish result: %v\n%s", err, result.ContentForLLM())
	}
	if published.WorkflowRef != "workflows/native-smoke.yml" {
		t.Fatalf("published ref = %q, want native smoke ref", published.WorkflowRef)
	}
	if _, err := os.Stat(filepath.Join(workspace, "workflows", "native-smoke.yml")); err != nil {
		t.Fatalf("published workflow stat error = %v", err)
	}
	if err := workflows.EnsureWorkflowRunnable(ctx, workspace, published.WorkflowRef, runtime); err != nil {
		t.Fatalf("EnsureWorkflowRunnable() after publish error = %v", err)
	}
	active, err := workflows.GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
	}
	if active != nil {
		t.Fatalf("active session after publish = %#v, want nil", active)
	}
}

func TestWorkflowToolGraphAction(t *testing.T) {
	workspace := t.TempDir()
	store := workflows.NewFileRunStore(workspace)
	now := time.Now().UTC()
	parent := &workflows.Run{
		ID:          "parent",
		WorkflowRef: "workflows/parent.yml",
		Status:      workflows.RunStatusSucceeded,
		ChildRunIDs: []string{
			"child",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	child := &workflows.Run{
		ID:          "child",
		WorkflowRef: "workflows/child.yml",
		Status:      workflows.RunStatusSucceeded,
		ParentRunID: "parent",
		CallerJobID: "call",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.CreateRun(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRun(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	tool := NewWorkflowTool(&workflows.Executor{Store: store}, workspace)

	result := tool.Execute(context.Background(), map[string]any{
		"action": "graph",
		"run_id": "parent",
	})
	if result == nil || result.IsError {
		t.Fatalf("graph result = %#v", result)
	}
	var graph workflows.RunGraph
	if err := json.Unmarshal([]byte(result.ContentForLLM()), &graph); err != nil {
		t.Fatalf("unmarshal graph result: %v\n%s", err, result.ContentForLLM())
	}
	if len(graph.Nodes) != 2 {
		t.Fatalf("nodes = %#v", graph.Nodes)
	}
	if len(graph.Edges) != 1 || graph.Edges[0].Kind != "child" || graph.Edges[0].JobID != "call" {
		t.Fatalf("edges = %#v", graph.Edges)
	}
}

func newNoopWorkflowTool(
	t *testing.T,
	workspace string,
	runtime workflows.RuntimeCompatibility,
	store workflows.RunStore,
) *WorkflowTool {
	t.Helper()
	registry := workflows.NewFunctionRegistry()
	if err := registry.Register(
		"noop",
		func(context.Context, map[string]any, workflows.ExecutionContext) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	return NewWorkflowTool(&workflows.Executor{
		WorkspaceDir: workspace,
		Store:        store,
		Functions:    registry,
	}, workspace, runtime)
}

func workflowToolRunCount(t *testing.T, ctx context.Context, store workflows.RunStore) int {
	t.Helper()
	runs, err := store.ListRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return len(runs)
}

func writeWorkflowToolFile(t *testing.T, workspace, name, content string) {
	t.Helper()
	path := filepath.Join(workspace, "workflows", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
