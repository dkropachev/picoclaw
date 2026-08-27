package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchTransactionMixedCommitPreservesExactPostimage(t *testing.T) {
	workspace := t.TempDir()
	fixtures := []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{"update.txt", "before update\n", 0o600},
		{"delete.txt", "delete me\n", 0o640},
		{"move.txt", "move me\n", 0o751},
	}
	for _, fixture := range fixtures {
		path := filepath.Join(workspace, fixture.name)
		if err := os.WriteFile(path, []byte(fixture.content), fixture.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, fixture.mode); err != nil {
			t.Fatal(err)
		}
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: update.txt\n@@\n-before update\n+after update\n" +
		"*** Delete File: delete.txt\n" +
		"*** Update File: move.txt\n*** Move to: nested/moved.txt\n" +
		"*** Add File: root-add.txt\n+root add\n" +
		"*** Add File: tree/deep.txt\n+deep add\n" +
		"*** End Patch"
	plan := buildApplyPatchTxnTestPlan(t, workspace, patch)
	state, workspaceState := openApplyPatchTxnTestState(t, plan.workspace)
	transaction, err := beginApplyPatchTransaction(
		context.Background(),
		state,
		workspaceState,
		plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.revalidate(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := transaction.commit(); err != nil {
		t.Fatalf("transaction.commit() error = %v", err)
	}

	assertApplyPatchTxnTestFile(t, filepath.Join(workspace, "update.txt"), "after update\n", 0o600)
	assertApplyPatchTxnTestFile(t, filepath.Join(workspace, "nested", "moved.txt"), "move me\n", 0o751)
	assertApplyPatchTxnTestFileModeNarrowed(
		t,
		filepath.Join(workspace, "root-add.txt"),
		"root add\n",
		0o644,
	)
	assertApplyPatchTxnTestFileModeNarrowed(
		t,
		filepath.Join(workspace, "tree", "deep.txt"),
		"deep add\n",
		0o644,
	)
	for _, absent := range []string{"delete.txt", "move.txt"} {
		_, statErr := os.Lstat(filepath.Join(workspace, absent))
		if !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%s remained after commit: %v", absent, statErr)
		}
	}
	assertNoApplyPatchTxnWorkspaceResidue(t, workspace)
	if err := workspaceState.withDirectoryAnchor(
		func(root *os.Root) error {
			return requireApplyPatchTxnWorkspaceReadyForNewTransaction(root)
		},
	); err != nil {
		t.Fatalf("transaction state residue = %v", err)
	}
}

func TestApplyPatchTransactionInjectedPostQuarantineFailureRollsBackExactly(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	plan := buildApplyPatchTxnTestPlan(t, workspace,
		"*** Begin Patch\n*** Update File: source.txt\n"+
			"@@\n-before\n+after\n*** End Patch")
	state, workspaceState := openApplyPatchTxnTestState(t, plan.workspace)
	transaction, err := beginApplyPatchTransaction(
		context.Background(), state, workspaceState, plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.revalidate(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected after source quarantine")
	transaction.fault = func(boundary string) error {
		if boundary == "source_quarantine:0" {
			return injected
		}
		return nil
	}
	commitErr := transaction.commit()
	if !errors.Is(commitErr, injected) || errors.Is(commitErr, errApplyPatchRollbackIncomplete) {
		t.Fatalf("transaction.commit() error = %v", commitErr)
	}
	assertApplyPatchTxnTestFile(t, path, "before\n", 0o640)
	assertNoApplyPatchTxnWorkspaceResidue(t, workspace)
	if err := workspaceState.withDirectoryAnchor(
		func(root *os.Root) error {
			return requireApplyPatchTxnWorkspaceReadyForNewTransaction(root)
		},
	); err != nil {
		t.Fatalf("rollback state residue = %v", err)
	}
}

func TestApplyPatchTransactionNoReplaceConflictPreservesAlienTarget(t *testing.T) {
	workspace := t.TempDir()
	plan := buildApplyPatchTxnTestPlan(t, workspace,
		"*** Begin Patch\n*** Add File: target.txt\n+candidate\n*** End Patch")
	state, workspaceState := openApplyPatchTxnTestState(t, plan.workspace)
	transaction, err := beginApplyPatchTransaction(
		context.Background(), state, workspaceState, plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.revalidate(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "target.txt"), []byte("alien\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitErr := transaction.commit()
	if !errors.Is(commitErr, errApplyPatchRollbackIncomplete) {
		t.Fatalf("no-replace conflict error = %v", commitErr)
	}
	assertApplyPatchTxnTestFile(t, filepath.Join(workspace, "target.txt"), "alien\n", 0o600)
	assertNoApplyPatchTxnWorkspaceResidue(t, workspace)
}

func buildApplyPatchTxnTestPlan(t *testing.T, workspace string, patch string) *applyPatchPlan {
	t.Helper()
	isolateApplyPatchDefaultTransactionState(t)
	operations, err := parseCodexPatchContext(context.Background(), patch)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotApplyPatchWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	tool := NewApplyPatchTool(workspace, true)
	plan, err := tool.planPatch(context.Background(), snapshot, operations)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestBuildApplyPatchTxnTestPlanIsolatesDefaultTransactionState(t *testing.T) {
	isolateApplyPatchDefaultTransactionState(t)
	sharedDefaultStateRoot := defaultApplyPatchTransactionStateRoot()
	workspace := t.TempDir()
	plan := buildApplyPatchTxnTestPlan(
		t,
		workspace,
		"*** Begin Patch\n*** Add File: target.txt\n+candidate\n*** End Patch",
	)
	if err := os.MkdirAll(sharedDefaultStateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := revalidateApplyPatchPlan(context.Background(), plan); err != nil {
		t.Fatalf("default transaction state changed isolated plan: %v", err)
	}
}

func assertApplyPatchTxnTestFile(
	t *testing.T,
	path string,
	wantContent string,
	wantMode os.FileMode,
) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != wantContent {
		t.Fatalf("file %q = %q, %v; want %q", path, data, err, wantContent)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != wantMode.Perm() {
		t.Fatalf("file %q mode = %#o, %v; want %#o", path, info.Mode().Perm(), err, wantMode.Perm())
	}
}

func assertApplyPatchTxnTestFileModeNarrowed(
	t *testing.T,
	path string,
	wantContent string,
	requested os.FileMode,
) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != wantContent {
		t.Fatalf("file %q = %q, %v; want %q", path, data, err, wantContent)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm()&^requested.Perm() != 0 {
		t.Fatalf("file %q mode = %#o, %v; exceeds %#o", path, info.Mode().Perm(), err, requested.Perm())
	}
}

func assertNoApplyPatchTxnWorkspaceResidue(t *testing.T, workspace string) {
	t.Helper()
	err := filepath.WalkDir(workspace, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if strings.HasPrefix(entry.Name(), ".picoclaw-apply-patch-") {
			t.Errorf("transaction residue remained at %q", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
