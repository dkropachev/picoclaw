package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionRecoveryTopCloseoutBindingFailure(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	tx := fixture.begin(t)
	if err := tx.closeHandles(); err != nil {
		t.Fatal(err)
	}
	otherPath := t.TempDir()
	otherWorkspace, err := snapshotApplyPatchWorkspace(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(),
		fixture.state,
		fixture.workspaceState,
		otherWorkspace,
	); err == nil {
		t.Fatal("mismatched recovery workspace binding succeeded")
	}
}

func TestApplyPatchTransactionRecoveryTopCloseoutAuthorizationFailure(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	tx := fixture.begin(t)
	if err := tx.closeHandles(); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("recovery denied")
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	tool.pathGuard = func(string) error { return denied }
	if err := tool.recoverApplyPatchTransaction(
		context.Background(),
		fixture.state,
		fixture.workspaceState,
		fixture.workspace,
	); err == nil {
		t.Fatal("denied recovery authorization succeeded")
	}
}

func TestApplyPatchTransactionRecoveryTopCloseoutReconstructionFailure(t *testing.T) {
	workspaceParent := t.TempDir()
	workspacePath := filepath.Join(workspaceParent, "workspace")
	if err := os.Mkdir(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := newApplyPatchTxnRecoveryFixtureForPatch(
		t,
		workspacePath,
		"*** Begin Patch\n*** Add File: result.txt\n+result\n*** End Patch",
	)
	tx := fixture.begin(t)
	if err := tx.closeHandles(); err != nil {
		t.Fatal(err)
	}
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := os.Rename(workspacePath, filepath.Join(workspaceParent, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := tool.recoverApplyPatchTransaction(
		context.Background(),
		fixture.state,
		fixture.workspaceState,
		fixture.workspace,
	); err == nil {
		t.Fatal("missing recovery anchor reconstructed")
	}
}
