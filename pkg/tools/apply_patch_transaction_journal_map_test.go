package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionIntentMapsToAuthenticatedPreparingJournal(t *testing.T) {
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	stateRoot := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(workspacePath, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("before\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	workspaceInfo, err := os.Lstat(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	plan := &applyPatchPlan{
		workspace: applyPatchWorkspace{
			canonical: workspacePath,
			info:      workspaceInfo,
		},
		ops: []plannedApplyPatchOp{
			{
				kind: "update", sourceLabel: "source.txt", targetLabel: "source.txt",
				sourcePath: sourcePath, targetPath: sourcePath,
				source: &applyPatchFileSnapshot{
					path: sourcePath, info: sourceInfo, mode: 0o640,
					data: []byte("before\n"), linkCount: 1,
				},
				before: []byte("before\n"), after: []byte("after\n"), mode: 0o640,
			},
			{
				kind: "add", targetLabel: "nested/new.txt",
				targetPath: filepath.Join(workspacePath, "nested", "new.txt"),
				after:      []byte("new\n"), mode: 0o644,
			},
		},
	}
	intent, err := buildApplyPatchTxnIntent(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	defer intent.Close()
	workspaceBinding, err := newApplyPatchTxnWorkspaceBinding(plan.workspace)
	if err != nil {
		t.Fatal(err)
	}
	stateInfo, err := os.Lstat(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	stateIdentity, err := applyPatchTxnIdentityFromFileInfo(stateInfo, "directory")
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, applyPatchTransactionKeyBytes)
	for index := range key {
		key[index] = byte(index + 1)
	}
	stateBinding, err := newApplyPatchTxnStateBinding(
		stateRoot,
		stateIdentity,
		key,
		applyPatchTxnTestWorkspaceDirectory(t, workspaceBinding.Identity),
		intent,
	)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := newApplyPatchTxnPreparingJournal(
		key,
		workspaceBinding,
		stateBinding,
		intent,
	)
	if err != nil {
		t.Fatalf("newApplyPatchTxnPreparingJournal() error = %v", err)
	}
	encoded, err := encodeApplyPatchTransactionJournal(key, journal)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeApplyPatchTransactionJournal(key, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.TransactionID != intent.id || decoded.OperationCount != 2 ||
		len(decoded.Artifacts) != 8 || len(decoded.Forests) != 1 {
		t.Fatalf("decoded journal = %+v", decoded)
	}
	if decoded.Operations[0].After.Mode != 0o640 || decoded.Operations[1].After.Mode != 0 {
		t.Fatalf("preparing modes = %#o/%#o", decoded.Operations[0].After.Mode, decoded.Operations[1].After.Mode)
	}
	backupDigest := sha256.Sum256([]byte("before\n"))
	if decoded.Artifacts[0].Backup == nil ||
		decoded.Artifacts[0].Backup.SHA256 != hex.EncodeToString(backupDigest[:]) {
		t.Fatalf("backup artifact = %+v", decoded.Artifacts[0])
	}
}
