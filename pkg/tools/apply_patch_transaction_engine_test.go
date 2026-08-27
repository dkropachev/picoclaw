package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionPreparationRevalidatesAndMarksPrepared(t *testing.T) {
	workspacePath := t.TempDir()
	workspace, err := snapshotApplyPatchWorkspace(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	plan := &applyPatchPlan{
		workspace: workspace,
		fences:    append([]applyPatchPathFence(nil), workspace.fences...),
		ops: []plannedApplyPatchOp{{
			kind: "add", targetLabel: "nested/result.txt",
			targetPath: filepath.Join(workspacePath, "nested", "result.txt"),
			after:      []byte("candidate\n"), mode: 0o644,
		}},
	}
	state, workspaceState := openApplyPatchTxnTestState(t, workspace)
	transaction, err := beginApplyPatchTransaction(
		context.Background(),
		state,
		workspaceState,
		plan,
	)
	if err != nil {
		t.Fatalf("beginApplyPatchTransaction() error = %v", err)
	}
	if err := transaction.revalidate(context.Background(), plan); err != nil {
		t.Fatalf("transaction.revalidate() error = %v", err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatalf("transaction.markPrepared() error = %v", err)
	}
	if transaction.journal.Phase != applyPatchTransactionPhasePrepared {
		t.Fatalf("transaction phase = %q", transaction.journal.Phase)
	}
	if _, err := os.Lstat(filepath.Join(workspacePath, "nested")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared transaction published target: %v", err)
	}
	if err := transaction.abortPreparing(); err != nil {
		t.Fatalf("prepared-without-effects cleanup error = %v", err)
	}
	if err := workspaceState.withDirectoryAnchor(
		func(root *os.Root) error {
			return requireApplyPatchTxnWorkspaceReadyForNewTransaction(root)
		},
	); err != nil {
		t.Fatalf("workspace state after cleanup = %v", err)
	}
}

func TestApplyPatchTransactionPreparationDetectsPostStageSourceDrift(t *testing.T) {
	workspacePath := t.TempDir()
	sourcePath := filepath.Join(workspacePath, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := snapshotApplyPatchWorkspace(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	plan := &applyPatchPlan{
		workspace: workspace,
		fences:    append([]applyPatchPathFence(nil), workspace.fences...),
		ops: []plannedApplyPatchOp{{
			kind: "update", sourceLabel: "source.txt", targetLabel: "source.txt",
			sourcePath: sourcePath, targetPath: sourcePath,
			source: &applyPatchFileSnapshot{
				path: sourcePath, info: sourceInfo, mode: 0o600,
				data: []byte("before\n"), linkCount: 1,
			},
			before: []byte("before\n"), after: []byte("candidate\n"), mode: 0o600,
		}},
	}
	state, workspaceState := openApplyPatchTxnTestState(t, workspace)
	transaction, err := beginApplyPatchTransaction(
		context.Background(),
		state,
		workspaceState,
		plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	driftWriteErr := os.WriteFile(sourcePath, []byte("alien\n"), 0o600)
	if driftWriteErr != nil {
		t.Fatal(driftWriteErr)
	}
	revalidationErr := transaction.revalidate(context.Background(), plan)
	if revalidationErr == nil {
		t.Fatal("post-stage source drift was accepted")
	}
	abortErr := transaction.abortPreparing()
	if abortErr != nil {
		t.Fatal(abortErr)
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil || string(data) != "alien\n" {
		t.Fatalf("pre-effect cleanup altered raced source: %q, %v", data, err)
	}
}

func openApplyPatchTxnTestState(
	t *testing.T,
	workspace applyPatchWorkspace,
) (*applyPatchTransactionState, *applyPatchTransactionWorkspaceState) {
	t.Helper()
	prepared, err := prepareApplyPatchTransactionStateRoot(
		workspace.canonical,
		filepath.Join(t.TempDir(), "transaction-state"),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	workspaceState, err := state.lockWorkspace(context.Background(), workspace.canonical)
	if err != nil {
		_ = state.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = workspaceState.Close()
		_ = state.Close()
	})
	return state, workspaceState
}
