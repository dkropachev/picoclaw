package reviews

import (
	"context"
	"time"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

// PruneTerminalWorkflowRunsExceptAttention applies ordinary workflow
// retention without deleting the reserved review-attention records. Review
// cases own the lifetime of those private runs because projection and exact
// response replay depend on them after process restarts.
func PruneTerminalWorkflowRunsExceptAttention(
	ctx context.Context,
	store workflows.RunStore,
	olderThan time.Time,
) (int, error) {
	runs, err := store.ListRuns(ctx)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for index := range runs {
		run := &runs[index]
		if IsAttentionWorkflowRun(run) || !terminalWorkflowRun(run.Status) {
			continue
		}
		completedAt := run.UpdatedAt
		if run.CompletedAt != nil && !run.CompletedAt.IsZero() {
			completedAt = *run.CompletedAt
		}
		if !completedAt.Before(olderThan) {
			continue
		}
		if err := store.DeleteRun(ctx, run.ID); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func terminalWorkflowRun(status string) bool {
	switch status {
	case workflows.RunStatusSucceeded,
		workflows.RunStatusFailed,
		workflows.RunStatusCanceled,
		workflows.RunStatusSkipped:
		return true
	default:
		return false
	}
}
