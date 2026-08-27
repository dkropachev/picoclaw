package tools

import (
	"context"
	"errors"
	"testing"
)

func TestApplyPatchTransactionEngineCancellationCloseoutBoundaries(t *testing.T) {
	for remaining := 1; remaining <= 80; remaining++ {
		workspace := t.TempDir()
		writeApplyPatchFixture(t, workspace, "source.txt", "before\n", 0o640)
		plan := buildApplyPatchTxnTestPlan(
			t,
			workspace,
			"*** Begin Patch\n"+
				"*** Update File: source.txt\n@@\n-before\n+after\n"+
				"*** Add File: nested/deeper/result.txt\n+result\n"+
				"*** End Patch",
		)
		state, workspaceState := openApplyPatchTxnTestState(t, plan.workspace)
		ctx := &applyPatchCancelAfterChecksContext{
			Context:   context.Background(),
			remaining: remaining,
		}
		tx, err := beginApplyPatchTransaction(ctx, state, workspaceState, plan)
		if tx != nil {
			_ = tx.abortPreparing()
			continue
		}
		if err == nil {
			t.Fatalf("cancellation boundary %d returned no transaction or error", remaining)
		}
		if !errors.Is(err, context.Canceled) {
			// Cleanup errors can join the injected cancellation. Every outcome must
			// still be a failure and leave no public mutation.
			assertApplyPatchTxnTestFile(
				t,
				workspace+"/source.txt",
				"before\n",
				0o640,
			)
		}
	}
}
