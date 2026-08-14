package api

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestPRLifecycleWorkflowRunsStayOutsideGenericWorkflowSurface(t *testing.T) {
	runs := []workflows.Run{
		{
			ID:          "lifecycle-gate",
			WorkflowRef: "inline/pr-lifecycle/pr.review.complete/digest",
			Status:      workflows.RunStatusWaiting,
		},
		{
			ID:          "related-run",
			WorkflowRef: "workflows/user.yml",
			Status:      workflows.RunStatusRunning,
			ChildRunIDs: []string{"lifecycle-gate"},
		},
		{
			ID:                "user-private-run",
			WorkflowRef:       "inline/user-private",
			Status:            workflows.RunStatusWaiting,
			ContextVisibility: workflows.WorkflowContextVisibilityPrivate,
		},
	}
	snapshot := newWorkflowRunPrivacySnapshot(runs)

	if got := snapshot.sanitizeRun(&runs[0]); got != nil {
		t.Fatalf("PR lifecycle gate was exposed: %#v", got)
	}
	if snapshot.runMutationAllowed("related-run") {
		t.Fatal("run related to private lifecycle gate allowed generic mutation")
	}
	related := snapshot.sanitizeRun(&runs[1])
	if related == nil || len(related.ChildRunIDs) != 0 {
		t.Fatalf("related run projection = %#v", related)
	}
	if got := snapshot.sanitizeRun(&runs[2]); got == nil {
		t.Fatal("unrelated user-authored private workflow was hidden")
	}
}
