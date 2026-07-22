package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
	for _, want := range []string{"list", "validate", "reload", "run", "status", "events", "graph", "cancel", "retry"} {
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
