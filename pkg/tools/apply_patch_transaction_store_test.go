package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionStorePersistsJournalAndAuthenticatedBackups(t *testing.T) {
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
		ops: []plannedApplyPatchOp{{
			kind: "update", sourceLabel: "source.txt", targetLabel: "source.txt",
			sourcePath: sourcePath, targetPath: sourcePath,
			source: &applyPatchFileSnapshot{
				path: sourcePath, info: sourceInfo, mode: 0o600,
				data: []byte("before\n"), linkCount: 1,
			},
			before: []byte("before\n"), after: []byte("after\n"), mode: 0o600,
		}},
	}
	intent, err := buildApplyPatchTxnIntent(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	defer intent.Close()

	configuredRoot := filepath.Join(t.TempDir(), "transaction-state")
	prepared, err := prepareApplyPatchTransactionStateRoot(
		workspacePath,
		configuredRoot,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	workspaceState, err := state.lockWorkspace(context.Background(), workspace.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer workspaceState.Close()

	keyArray, err := state.authenticationKey()
	if err != nil {
		t.Fatal(err)
	}
	key := keyArray[:]
	workspaceBinding, err := newApplyPatchTxnWorkspaceBinding(workspace)
	if err != nil {
		t.Fatal(err)
	}
	rootPath, err := state.rootPath()
	if err != nil {
		t.Fatal(err)
	}
	rootIdentity, err := state.rootIdentity()
	if err != nil {
		t.Fatal(err)
	}
	workspaceRelative, err := workspaceState.directoryRelative()
	if err != nil {
		t.Fatal(err)
	}
	stateBinding, err := newApplyPatchTxnStateBinding(
		rootPath,
		rootIdentity,
		key,
		workspaceRelative,
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
		t.Fatal(err)
	}
	store, err := createApplyPatchTxnStore(workspaceState, intent)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	initialWriteErr := store.writeJournal(key, journal)
	if initialWriteErr != nil {
		t.Fatal(initialWriteErr)
	}
	checkpoints := 0
	checkpoint := func(current *applyPatchTransactionJournal) error {
		checkpoints++
		return store.writeJournal(key, current)
	}
	backupWriteErr := store.writeBackups(
		context.Background(),
		key,
		intent,
		journal,
		checkpoint,
	)
	if backupWriteErr != nil {
		t.Fatalf("writeBackups() error = %v", backupWriteErr)
	}
	stageErr := stageApplyPatchTxnPostimages(
		context.Background(),
		intent,
		journal,
		checkpoint,
	)
	if stageErr != nil {
		t.Fatalf("stageApplyPatchTxnPostimages() error = %v", stageErr)
	}
	if checkpoints != 5 {
		t.Fatalf("preparation checkpoints = %d, want 5", checkpoints)
	}
	persisted, encoded, err := store.readJournal(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) == 0 || persisted.TransactionID != intent.id {
		t.Fatalf("persisted journal = %+v, bytes=%d", persisted, len(encoded))
	}
	backup, err := requireApplyPatchTxnArtifact(
		persisted,
		0,
		applyPatchTransactionArtifactBackupBlob,
	)
	if err != nil || backup.StateIdentity == nil {
		t.Fatalf("persisted backup artifact = %+v, %v", backup, err)
	}
	store.mu.Lock()
	backupData, _, readErr := readApplyPatchTransactionPrivateRegularBounded(
		store.activeRoot,
		backup.StateName,
		applyPatchTransactionMaxBackupBytes,
	)
	store.mu.Unlock()
	if readErr != nil || string(backupData) != "before\n" {
		t.Fatalf("backup data = %q, %v", backupData, readErr)
	}
	backupVerifyErr := verifyApplyPatchTransactionBackup(
		key,
		persisted.TransactionID,
		backup.StateName,
		*backup.Backup,
		backupData,
	)
	if backupVerifyErr != nil {
		t.Fatal(backupVerifyErr)
	}
	second, secondStoreErr := createApplyPatchTxnStore(workspaceState, intent)
	if secondStoreErr == nil {
		_ = second.Close()
		t.Fatal("second active transaction store was created")
	}
	cleanupErr := cleanupApplyPatchTxnPrePONR(intent, journal, store, key)
	if cleanupErr != nil {
		t.Fatalf("cleanupApplyPatchTxnPrePONR() error = %v", cleanupErr)
	}
	content, err := os.ReadFile(sourcePath)
	if err != nil || string(content) != "before\n" {
		t.Fatalf("source after cleanup = %q, %v", content, err)
	}
	if err := workspaceState.withDirectoryAnchor(
		func(root *os.Root) error {
			return requireApplyPatchTxnWorkspaceReadyForNewTransaction(root)
		},
	); err != nil {
		t.Fatalf("workspace state after cleanup = %v", err)
	}
}
