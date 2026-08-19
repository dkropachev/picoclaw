package api

import (
	"context"
	"strings"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

// workflowRunPrivacySnapshot is one fail-closed view of the run store. Each
// private PR-lifecycle run ID is removed from generic projections, and every
// run in their malformed parent/child/retry component is denied generic
// mutation. Ordinary runs in those components remain readable after their
// relationship fields have been scrubbed.
type workflowRunPrivacySnapshot struct {
	runsByID       map[string]workflows.Run
	hiddenIDs      map[string]struct{}
	mutationDenied map[string]struct{}
}

func loadWorkflowRunPrivacySnapshot(
	ctx context.Context,
	store workflows.RunStore,
) (*workflowRunPrivacySnapshot, error) {
	runs, err := store.ListRuns(ctx)
	if err != nil {
		return nil, err
	}
	return newWorkflowRunPrivacySnapshot(runs), nil
}

func newWorkflowRunPrivacySnapshot(runs []workflows.Run) *workflowRunPrivacySnapshot {
	snapshot := &workflowRunPrivacySnapshot{
		runsByID:       make(map[string]workflows.Run, len(runs)),
		hiddenIDs:      make(map[string]struct{}),
		mutationDenied: make(map[string]struct{}),
	}
	adjacent := make(map[string]map[string]struct{}, len(runs))
	connect := func(left, right string) {
		left = strings.TrimSpace(left)
		right = strings.TrimSpace(right)
		if left == "" || right == "" {
			return
		}
		if adjacent[left] == nil {
			adjacent[left] = make(map[string]struct{})
		}
		if adjacent[right] == nil {
			adjacent[right] = make(map[string]struct{})
		}
		adjacent[left][right] = struct{}{}
		adjacent[right][left] = struct{}{}
	}
	for index := range runs {
		run := runs[index]
		runID := strings.TrimSpace(run.ID)
		if runID == "" {
			continue
		}
		snapshot.runsByID[runID] = run
		if isPrivateInternalWorkflowRun(&run) {
			snapshot.hiddenIDs[runID] = struct{}{}
		}
		connect(runID, run.ParentRunID)
		connect(runID, run.RetryOfRunID)
		for _, childID := range run.ChildRunIDs {
			connect(runID, childID)
		}
	}
	queue := make([]string, 0, len(snapshot.hiddenIDs))
	for runID := range snapshot.hiddenIDs {
		snapshot.mutationDenied[runID] = struct{}{}
		queue = append(queue, runID)
	}
	for len(queue) != 0 {
		runID := queue[0]
		queue = queue[1:]
		for relatedID := range adjacent[runID] {
			if _, seen := snapshot.mutationDenied[relatedID]; seen {
				continue
			}
			snapshot.mutationDenied[relatedID] = struct{}{}
			queue = append(queue, relatedID)
		}
	}
	return snapshot
}

func (snapshot *workflowRunPrivacySnapshot) runMutationAllowed(runID string) bool {
	if snapshot == nil {
		return false
	}
	run, exists := snapshot.runsByID[strings.TrimSpace(runID)]
	if !exists || isPrivateInternalWorkflowRun(&run) {
		return false
	}
	canonicalRunID := strings.TrimSpace(run.ID)
	if _, hidden := snapshot.hiddenIDs[canonicalRunID]; hidden {
		return false
	}
	_, denied := snapshot.mutationDenied[canonicalRunID]
	return !denied
}

func (snapshot *workflowRunPrivacySnapshot) sanitizeRun(
	run *workflows.Run,
) *workflows.Run {
	if snapshot == nil || run == nil || isPrivateInternalWorkflowRun(run) ||
		snapshot.hiddenReference(run.ID) {
		return nil
	}
	projected := *run
	if snapshot.hiddenReference(projected.ParentRunID) {
		projected.ParentRunID = ""
		projected.CallerJobID = ""
	}
	if snapshot.hiddenReference(projected.RetryOfRunID) {
		projected.RetryOfRunID = ""
	}
	children := make([]string, 0, len(projected.ChildRunIDs))
	for _, childID := range projected.ChildRunIDs {
		if !snapshot.hiddenReference(childID) {
			children = append(children, childID)
		}
	}
	if len(children) == 0 {
		projected.ChildRunIDs = nil
	} else {
		projected.ChildRunIDs = children
	}
	if projected.Origin != nil && snapshot.hiddenReference(projected.Origin.RootRunID) {
		projected.Origin = nil
	}
	return &projected
}

func (snapshot *workflowRunPrivacySnapshot) hiddenReference(runID string) bool {
	if snapshot == nil {
		return false
	}
	_, hidden := snapshot.hiddenIDs[strings.TrimSpace(runID)]
	return hidden
}

func isPrivateInternalWorkflowRun(run *workflows.Run) bool {
	return run != nil && strings.HasPrefix(
		strings.TrimSpace(run.WorkflowRef),
		"inline/pr-lifecycle/",
	)
}

func (snapshot *workflowRunPrivacySnapshot) projectRunForBrowser(
	ctx context.Context,
	store workflows.RunStore,
	run *workflows.Run,
) *workflows.Run {
	safe := snapshot.sanitizeRun(run)
	if safe == nil {
		return nil
	}
	projected := workflows.ProjectWorkflowRunForBrowserWithStore(
		ctx,
		store,
		safe,
		workflows.IsEventBackedDraftRunFamily(ctx, store, safe),
	)
	return snapshot.sanitizeRun(projected)
}

func (snapshot *workflowRunPrivacySnapshot) projectRunsForBrowser(
	ctx context.Context,
	store workflows.RunStore,
	runs []workflows.Run,
) []workflows.Run {
	visible := make([]workflows.Run, 0, len(runs))
	for index := range runs {
		if safe := snapshot.sanitizeRun(&runs[index]); safe != nil {
			visible = append(visible, *safe)
		}
	}
	projected := workflows.ProjectEventBackedDraftRunsForBrowserWithStore(
		ctx,
		store,
		visible,
	)
	for index := range projected {
		if safe := snapshot.sanitizeRun(&projected[index]); safe != nil {
			projected[index] = *safe
		}
	}
	return projected
}

func workflowRunMutationSnapshot(
	ctx context.Context,
	store workflows.RunStore,
	runID string,
) (*workflowRunPrivacySnapshot, bool) {
	if store == nil {
		return nil, false
	}
	snapshot, err := loadWorkflowRunPrivacySnapshot(ctx, store)
	if err != nil || !snapshot.runMutationAllowed(runID) {
		return nil, false
	}
	return snapshot, true
}
