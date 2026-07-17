package workflows

import "context"

type RunGraph struct {
	RunID string         `json:"run_id"`
	Nodes []RunGraphNode `json:"nodes"`
	Edges []RunGraphEdge `json:"edges"`
}

type RunGraphNode struct {
	ID           string `json:"id"`
	WorkflowRef  string `json:"workflow_ref"`
	Status       string `json:"status"`
	ParentRunID  string `json:"parent_run_id,omitempty"`
	CallerJobID  string `json:"caller_job_id,omitempty"`
	RetryOfRunID string `json:"retry_of_run_id,omitempty"`
}

type RunGraphEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	JobID string `json:"job_id,omitempty"`
	Kind  string `json:"kind"`
}

func BuildRunGraph(ctx context.Context, store RunStore, runID string) (*RunGraph, error) {
	root, err := store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	graph := &RunGraph{RunID: root.ID}
	seen := make(map[string]bool)
	var visit func(*Run) error
	visit = func(run *Run) error {
		if run == nil || seen[run.ID] {
			return nil
		}
		seen[run.ID] = true
		graph.Nodes = append(graph.Nodes, RunGraphNode{
			ID:           run.ID,
			WorkflowRef:  run.WorkflowRef,
			Status:       run.Status,
			ParentRunID:  run.ParentRunID,
			CallerJobID:  run.CallerJobID,
			RetryOfRunID: run.RetryOfRunID,
		})
		if run.ParentRunID != "" {
			graph.Edges = append(graph.Edges, RunGraphEdge{
				From:  run.ParentRunID,
				To:    run.ID,
				JobID: run.CallerJobID,
				Kind:  "child",
			})
		}
		if run.RetryOfRunID != "" {
			graph.Edges = append(graph.Edges, RunGraphEdge{
				From: run.RetryOfRunID,
				To:   run.ID,
				Kind: "retry",
			})
		}
		for _, childID := range run.ChildRunIDs {
			child, err := store.GetRun(ctx, childID)
			if err != nil {
				continue
			}
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	return graph, nil
}
