package tools

import (
	"context"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionEngineExactCloseoutBeginFailures(t *testing.T) {
	t.Run("authentication state closed", func(t *testing.T) {
		workspace := t.TempDir()
		plan := buildApplyPatchTxnTestPlan(
			t,
			workspace,
			"*** Begin Patch\n*** Add File: result.txt\n+result\n*** End Patch",
		)
		state, workspaceState := openApplyPatchTxnTestState(t, plan.workspace)
		if err := workspaceState.Close(); err != nil {
			t.Fatal(err)
		}
		if err := state.Close(); err != nil {
			t.Fatal(err)
		}
		if tx, err := beginApplyPatchTransaction(
			context.Background(), state, workspaceState, plan,
		); tx != nil || err == nil {
			t.Fatalf("closed authentication state = %#v, %v", tx, err)
		}
	})
	t.Run("invalid workspace binding", func(t *testing.T) {
		workspace := t.TempDir()
		validSnapshot, err := snapshotApplyPatchWorkspace(workspace)
		if err != nil {
			t.Fatal(err)
		}
		state, workspaceState := openApplyPatchTxnTestState(t, validSnapshot)
		plan := &applyPatchPlan{
			workspace: applyPatchWorkspace{},
			ops: []plannedApplyPatchOp{{
				kind: "add", targetLabel: "result.txt",
				targetPath: filepath.Join(workspace, "result.txt"),
				after:      []byte("result\n"), mode: 0o644,
			}},
		}
		if tx, err := beginApplyPatchTransaction(
			context.Background(), state, workspaceState, plan,
		); tx != nil || err == nil {
			t.Fatalf("invalid workspace binding = %#v, %v", tx, err)
		}
	})
	t.Run("closed workspace state", func(t *testing.T) {
		workspace := t.TempDir()
		plan := buildApplyPatchTxnTestPlan(
			t,
			workspace,
			"*** Begin Patch\n*** Add File: result.txt\n+result\n*** End Patch",
		)
		state, workspaceState := openApplyPatchTxnTestState(t, plan.workspace)
		if err := workspaceState.Close(); err != nil {
			t.Fatal(err)
		}
		if tx, err := beginApplyPatchTransaction(
			context.Background(), state, workspaceState, plan,
		); tx != nil || err == nil {
			t.Fatalf("closed workspace state = %#v, %v", tx, err)
		}
	})
	t.Run("protected transaction endpoint", func(t *testing.T) {
		workspace := t.TempDir()
		snapshot, err := snapshotApplyPatchWorkspace(workspace)
		if err != nil {
			t.Fatal(err)
		}
		state, workspaceState := openApplyPatchTxnTestState(t, snapshot)
		rootPath, err := state.rootPath()
		if err != nil {
			t.Fatal(err)
		}
		plan := &applyPatchPlan{
			workspace: snapshot,
			ops: []plannedApplyPatchOp{{
				kind: "add", targetLabel: "protected",
				targetPath: filepath.Join(rootPath, "protected"),
				after:      []byte("protected\n"), mode: 0o644,
			}},
		}
		if tx, err := beginApplyPatchTransaction(
			context.Background(), state, workspaceState, plan,
		); tx != nil || err == nil {
			t.Fatalf("protected transaction endpoint = %#v, %v", tx, err)
		}
	})
	t.Run("active transaction already exists", func(t *testing.T) {
		workspace := t.TempDir()
		plan := buildApplyPatchTxnTestPlan(
			t,
			workspace,
			"*** Begin Patch\n*** Add File: result.txt\n+result\n*** End Patch",
		)
		state, workspaceState := openApplyPatchTxnTestState(t, plan.workspace)
		first, err := beginApplyPatchTransaction(
			context.Background(), state, workspaceState, plan,
		)
		if err != nil {
			t.Fatal(err)
		}
		defer first.abortPreparing()
		if second, err := beginApplyPatchTransaction(
			context.Background(), state, workspaceState, plan,
		); second != nil || err == nil {
			t.Fatalf("second active transaction = %#v, %v", second, err)
		}
	})
}
