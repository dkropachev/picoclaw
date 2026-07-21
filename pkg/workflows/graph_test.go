package workflows

import (
	"context"
	"testing"
	"time"
)

func TestBuildRunGraphIncludesRetryEndpoints(t *testing.T) {
	for _, rootID := range []string{"wr_original", "wr_retry"} {
		t.Run(rootID, func(t *testing.T) {
			ctx := context.Background()
			store := NewFileRunStore(t.TempDir())
			createRetryGraphRuns(t, ctx, store)

			graph, err := BuildRunGraph(ctx, store, rootID)
			if err != nil {
				t.Fatalf("BuildRunGraph() error = %v", err)
			}
			if !runGraphHasNode(graph, "wr_original") || !runGraphHasNode(graph, "wr_retry") {
				t.Fatalf("graph nodes = %#v, want original and retry", graph.Nodes)
			}
			if !runGraphHasEdge(graph, "wr_original", "wr_retry", "retry") {
				t.Fatalf("graph edges = %#v, want retry edge", graph.Edges)
			}
		})
	}
}

func TestBuildRunGraphIncludesParentLinkedChildren(t *testing.T) {
	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	now := time.Now().UTC()
	parent := &Run{
		ID:          "wr_parent",
		WorkflowRef: "workflows/parent.yml",
		Status:      RunStatusRunning,
		CreatedAt:   now,
	}
	child := &Run{
		ID:          "wr_child",
		WorkflowRef: "workflows/child.yml",
		Status:      RunStatusRunning,
		ParentRunID: "wr_parent",
		CallerJobID: "call",
		CreatedAt:   now.Add(time.Second),
	}
	if err := store.CreateRun(ctx, parent); err != nil {
		t.Fatalf("CreateRun(parent) error = %v", err)
	}
	if err := store.CreateRun(ctx, child); err != nil {
		t.Fatalf("CreateRun(child) error = %v", err)
	}

	for _, rootID := range []string{"wr_parent", "wr_child"} {
		t.Run(rootID, func(t *testing.T) {
			graph, err := BuildRunGraph(ctx, store, rootID)
			if err != nil {
				t.Fatalf("BuildRunGraph() error = %v", err)
			}
			if !runGraphHasNode(graph, "wr_parent") || !runGraphHasNode(graph, "wr_child") {
				t.Fatalf("graph nodes = %#v, want parent and child", graph.Nodes)
			}
			if !runGraphHasEdge(graph, "wr_parent", "wr_child", "child") {
				t.Fatalf("graph edges = %#v, want child edge", graph.Edges)
			}
		})
	}
}

func createRetryGraphRuns(t *testing.T, ctx context.Context, store *FileRunStore) {
	t.Helper()
	now := time.Now().UTC()
	for _, run := range []*Run{
		{
			ID:          "wr_original",
			WorkflowRef: "workflows/test.yml",
			Status:      RunStatusFailed,
			CreatedAt:   now,
		},
		{
			ID:           "wr_retry",
			WorkflowRef:  "workflows/test.yml",
			Status:       RunStatusSucceeded,
			RetryOfRunID: "wr_original",
			CreatedAt:    now.Add(time.Second),
		},
	} {
		if err := store.CreateRun(ctx, run); err != nil {
			t.Fatalf("CreateRun(%s) error = %v", run.ID, err)
		}
	}
}

func runGraphHasNode(graph *RunGraph, id string) bool {
	for _, node := range graph.Nodes {
		if node.ID == id {
			return true
		}
	}
	return false
}

func runGraphHasEdge(graph *RunGraph, from, to, kind string) bool {
	for _, edge := range graph.Edges {
		if edge.From == from && edge.To == to && edge.Kind == kind {
			return true
		}
	}
	return false
}
