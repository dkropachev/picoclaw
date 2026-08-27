package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchTransactionFaultExecuteRollsBackExactTree(t *testing.T) {
	type fixture struct {
		path    string
		content string
		mode    os.FileMode
	}
	tests := []struct {
		name       string
		fixtures   []fixture
		patch      string
		faultMatch func(string) bool
	}{
		{
			name:     "update after source witness",
			fixtures: []fixture{{"update.txt", "before update\n", 0o600}},
			patch: "*** Begin Patch\n" +
				"*** Update File: update.txt\n@@\n-before update\n+after update\n" +
				"*** End Patch",
			faultMatch: applyPatchTxnFaultBoundaryIs("source_witness:0"),
		},
		{
			name:     "delete after source quarantine",
			fixtures: []fixture{{"delete.txt", "delete me\n", 0o640}},
			patch: "*** Begin Patch\n" +
				"*** Delete File: delete.txt\n" +
				"*** End Patch",
			faultMatch: applyPatchTxnFaultBoundaryIs("source_quarantine:0"),
		},
		{
			name:     "move after source witness",
			fixtures: []fixture{{"move.txt", "move me\n", 0o751}},
			patch: "*** Begin Patch\n" +
				"*** Update File: move.txt\n*** Move to: moved.txt\n" +
				"*** End Patch",
			faultMatch: applyPatchTxnFaultBoundaryIs("source_witness:0"),
		},
		{
			name:     "move after source quarantine",
			fixtures: []fixture{{"move.txt", "move me\n", 0o751}},
			patch: "*** Begin Patch\n" +
				"*** Update File: move.txt\n*** Move to: moved.txt\n" +
				"*** End Patch",
			faultMatch: applyPatchTxnFaultBoundaryIs("source_quarantine:0"),
		},
		{
			name:       "add after target publish",
			patch:      "*** Begin Patch\n*** Add File: added.txt\n+candidate\n*** End Patch",
			faultMatch: applyPatchTxnFaultBoundaryIs("target_publish:0"),
		},
		{
			name:     "move after target publish",
			fixtures: []fixture{{"move.txt", "move me\n", 0o751}},
			patch: "*** Begin Patch\n" +
				"*** Update File: move.txt\n*** Move to: moved.txt\n" +
				"*** End Patch",
			faultMatch: applyPatchTxnFaultBoundaryIs("target_publish:0"),
		},
		{
			name:  "nested add after forest publish",
			patch: "*** Begin Patch\n*** Add File: nested/deep/added.txt\n+candidate\n*** End Patch",
			faultMatch: func(boundary string) bool {
				return strings.HasPrefix(boundary, "forest_publish:")
			},
		},
		{
			name: "mixed before decision",
			fixtures: []fixture{
				{"update.txt", "before update\n", 0o600},
				{"delete.txt", "delete me\n", 0o640},
				{"move.txt", "move me\n", 0o751},
			},
			patch: "*** Begin Patch\n" +
				"*** Update File: update.txt\n@@\n-before update\n+after update\n" +
				"*** Delete File: delete.txt\n" +
				"*** Update File: move.txt\n*** Move to: moved.txt\n" +
				"*** Add File: added.txt\n+root candidate\n" +
				"*** Add File: nested/deep/added.txt\n+nested candidate\n" +
				"*** End Patch",
			faultMatch: applyPatchTxnFaultBoundaryIs("before_decision"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			for _, item := range test.fixtures {
				writeApplyPatchFixture(t, workspace, item.path, item.content, item.mode)
			}
			before := applyPatchSnapshotTree(t, workspace)
			stateRoot := filepath.Join(t.TempDir(), "transaction-state")
			tool := newApplyPatchPreflightTestTool(
				t,
				workspace,
				true,
				true,
				ApplyPatchPreflightPolicy{TransactionStateRoot: stateRoot},
			)
			injected := errors.New("injected apply-patch transaction fault")
			faultHit := false
			tool.transactionFault = func(boundary string) error {
				if !faultHit && test.faultMatch(boundary) {
					faultHit = true
					return injected
				}
				return nil
			}

			result := executeApplyPatch(
				t,
				tool,
				context.Background(),
				test.patch,
			)
			if !faultHit {
				t.Fatal("requested transaction fault boundary was not reached")
			}
			if result == nil || !result.IsError ||
				!strings.Contains(result.ForLLM, "transaction failed") {
				t.Fatalf("fault result = %#v", result)
			}
			if result.ForUser != "" || result.ResponseHandled {
				t.Fatalf("failed transaction exposed public success: %#v", result)
			}
			assertApplyPatchTreeEqual(t, workspace, before)
			assertNoApplyPatchTxnWorkspaceResidue(t, workspace)
			assertApplyPatchTxnFaultStateReady(t, workspace, stateRoot)
		})
	}
}

func applyPatchTxnFaultBoundaryIs(want string) func(string) bool {
	return func(boundary string) bool { return boundary == want }
}

func assertApplyPatchTxnFaultStateReady(
	t *testing.T,
	workspacePath string,
	stateRoot string,
) {
	t.Helper()
	workspace, err := snapshotApplyPatchWorkspace(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareApplyPatchTransactionStateRoot(
		workspace.canonical,
		stateRoot,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	workspaceState, err := state.lockWorkspace(
		context.Background(),
		workspace.canonical,
	)
	if err != nil {
		_ = state.Close()
		t.Fatal(err)
	}
	readyErr := workspaceState.withDirectoryAnchor(
		func(root *os.Root) error {
			return requireApplyPatchTxnWorkspaceReadyForNewTransaction(root)
		},
	)
	closeErr := errors.Join(workspaceState.Close(), state.Close())
	if readyErr != nil || closeErr != nil {
		t.Fatalf("transaction state residue: %v", errors.Join(readyErr, closeErr))
	}
}
