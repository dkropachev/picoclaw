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
	allRuns, err := store.ListRuns(ctx)
	if err != nil {
		return nil, err
	}
	runsByID := make(map[string]Run, len(allRuns)+1)
	childrenByParentID := make(map[string][]Run, len(allRuns))
	retriesByRunID := make(map[string][]Run, len(allRuns))
	for _, run := range allRuns {
		runsByID[run.ID] = run
		if run.ParentRunID != "" {
			childrenByParentID[run.ParentRunID] = append(childrenByParentID[run.ParentRunID], run)
		}
		if run.RetryOfRunID != "" {
			retriesByRunID[run.RetryOfRunID] = append(retriesByRunID[run.RetryOfRunID], run)
		}
	}
	runsByID[root.ID] = *root
	graph := &RunGraph{RunID: root.ID}
	seen := make(map[string]bool)
	seenEdges := make(map[RunGraphEdge]bool)
	addEdge := func(edge RunGraphEdge) {
		if seenEdges[edge] {
			return
		}
		seenEdges[edge] = true
		graph.Edges = append(graph.Edges, edge)
	}
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
			addEdge(RunGraphEdge{
				From:  run.ParentRunID,
				To:    run.ID,
				JobID: run.CallerJobID,
				Kind:  "child",
			})
			if parent, ok := runsByID[run.ParentRunID]; ok {
				if err := visit(&parent); err != nil {
					return err
				}
			} else if parent, err := store.GetRun(ctx, run.ParentRunID); err == nil {
				if err := visit(parent); err != nil {
					return err
				}
			}
		}
		if run.RetryOfRunID != "" {
			addEdge(RunGraphEdge{
				From: run.RetryOfRunID,
				To:   run.ID,
				Kind: "retry",
			})
			if retryOf, ok := runsByID[run.RetryOfRunID]; ok {
				if err := visit(&retryOf); err != nil {
					return err
				}
			} else if retryOf, err := store.GetRun(ctx, run.RetryOfRunID); err == nil {
				if err := visit(retryOf); err != nil {
					return err
				}
			}
		}
		for _, childID := range run.ChildRunIDs {
			if child, ok := runsByID[childID]; ok {
				if err := visit(&child); err != nil {
					return err
				}
				continue
			}
			child, err := store.GetRun(ctx, childID)
			if err != nil {
				continue
			}
			if err := visit(child); err != nil {
				return err
			}
		}
		for _, child := range childrenByParentID[run.ID] {
			if err := visit(&child); err != nil {
				return err
			}
		}
		for _, retry := range retriesByRunID[run.ID] {
			if err := visit(&retry); err != nil {
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
