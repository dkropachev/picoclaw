package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchTransactionRecoveryRemovesExactEmptyInitialShell(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	intent, err := buildApplyPatchTxnIntent(context.Background(), fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	store, err := createApplyPatchTxnStore(fixture.workspaceState, intent)
	if err != nil {
		_ = intent.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := intent.Close(); err != nil {
		t.Fatal(err)
	}
	closeApplyPatchTxnInitialRecoveryFixture(t, fixture)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	); err != nil {
		t.Fatalf("empty-shell recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
}

func TestApplyPatchTransactionRecoveryPublishesSoleInitialJournalStage(t *testing.T) {
	for _, journalLinked := range []bool{false, true} {
		name := "stage only"
		if journalLinked {
			name = "journal link published"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
			transaction := prepareApplyPatchTxnInitialJournalStage(t, fixture)
			writeApplyPatchTxnRecoveryJournalStage(t, transaction, transaction.journal)
			if journalLinked {
				if err := transaction.store.activeRoot.Link(
					applyPatchTransactionJournalStageFile,
					applyPatchTransactionJournalFile,
				); err != nil {
					t.Fatal(err)
				}
				if err := syncApplyPatchTxnRootDirectory(transaction.store.activeRoot); err != nil {
					t.Fatal(err)
				}
			}
			fixture.simulateCrash(t, transaction)

			state, workspaceState := fixture.reopen(t)
			tool := newApplyPatchTxnRecoveryTool(
				t,
				fixture.workspacePath,
				fixture.stateRoot,
			)
			if err := tool.recoverApplyPatchTransaction(
				context.Background(), state, workspaceState, fixture.workspace,
			); err != nil {
				t.Fatalf("initial-journal-stage recovery error = %v", err)
			}
			fixture.closeReopened(t, state, workspaceState)
			assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
		})
	}
}

func TestApplyPatchTransactionRecoveryRejectsAlienJournalLessInitialShell(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	intent, err := buildApplyPatchTxnIntent(context.Background(), fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	store, err := createApplyPatchTxnStore(fixture.workspaceState, intent)
	if err != nil {
		_ = intent.Close()
		t.Fatal(err)
	}
	file, err := store.activeRoot.OpenFile("alien", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	closeErr := file.Close()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	syncErr := syncApplyPatchTxnRootDirectory(store.activeRoot)
	if syncErr != nil {
		t.Fatal(syncErr)
	}
	storeCloseErr := store.Close()
	if storeCloseErr != nil {
		t.Fatal(storeCloseErr)
	}
	intentCloseErr := intent.Close()
	if intentCloseErr != nil {
		t.Fatal(intentCloseErr)
	}
	closeApplyPatchTxnInitialRecoveryFixture(t, fixture)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	err = tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	)
	if err == nil || !strings.Contains(err.Error(), "alien entry") {
		t.Fatalf("alien journal-less recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
}

func TestApplyPatchTransactionRecoveryResyncsVisibleCommittedDecisionBeforeCleanup(
	t *testing.T,
) {
	for _, boundary := range []string{
		"committed_recovery_visible_before_sync",
		"committed_recovery_journal_sync",
	} {
		t.Run(boundary, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
			transaction := commitApplyPatchTxnRecoveryFixtureToDecision(t, fixture)
			fixture.simulateCrash(t, transaction)

			state, workspaceState := fixture.reopen(t)
			tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
			injected := errors.New("interrupted committed marker resync")
			tool.transactionFault = func(observed string) error {
				if observed == boundary {
					return injected
				}
				return nil
			}
			err := tool.recoverApplyPatchTransaction(
				context.Background(), state, workspaceState, fixture.workspace,
			)
			if !errors.Is(err, errApplyPatchCommitUncertain) || !errors.Is(err, injected) {
				t.Fatalf("committed resync error = %v", err)
			}
			fixture.closeReopened(t, state, workspaceState)
			assertApplyPatchTxnTestFileModeNarrowed(
				t,
				filepath.Join(fixture.workspacePath, "result.txt"),
				"candidate\n",
				0o644,
			)

			state, workspaceState = fixture.reopen(t)
			tool = newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
			if err := tool.recoverApplyPatchTransaction(
				context.Background(), state, workspaceState, fixture.workspace,
			); err != nil {
				t.Fatalf("committed resync retry error = %v", err)
			}
			fixture.closeReopened(t, state, workspaceState)
			assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
		})
	}
}

func TestApplyPatchTransactionRecoveryRevalidatesAfterNilResyncHook(t *testing.T) {
	for _, mutate := range []string{"journal", "active directory"} {
		t.Run(mutate, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
			transaction := commitApplyPatchTxnRecoveryFixtureToDecision(t, fixture)
			workspaceStatePath, err := transaction.workspaceState.directoryPath()
			if err != nil {
				t.Fatal(err)
			}
			activePath := filepath.Join(workspaceStatePath, transaction.store.activeName)
			fixture.simulateCrash(t, transaction)

			state, workspaceState := fixture.reopen(t)
			tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
			tool.transactionFault = func(boundary string) error {
				if boundary != "committed_recovery_journal_sync" {
					return nil
				}
				var mutationErr error
				switch mutate {
				case "journal":
					mutationErr = os.Rename(
						filepath.Join(activePath, applyPatchTransactionJournalFile),
						filepath.Join(activePath, "alien-journal"),
					)
				case "active directory":
					mutationErr = os.Rename(activePath, activePath+"-alien")
				}
				if mutationErr != nil {
					t.Fatalf("mutate committed recovery state: %v", mutationErr)
				}
				return nil
			}
			err = tool.recoverApplyPatchTransaction(
				context.Background(), state, workspaceState, fixture.workspace,
			)
			if !errors.Is(err, errApplyPatchCommitUncertain) {
				t.Fatalf("post-hook mutation error = %v", err)
			}
			fixture.closeReopened(t, state, workspaceState)
		})
	}
}

func TestApplyPatchTransactionRecoveryConflictMakesZeroRemovalWrites(t *testing.T) {
	workspacePath := t.TempDir()
	fixture := newApplyPatchTxnRecoveryFixtureForPatch(
		t,
		workspacePath,
		"*** Begin Patch\n"+
			"*** Add File: one.txt\n+one\n"+
			"*** Add File: two.txt\n+two\n"+
			"*** End Patch",
	)
	transaction := commitApplyPatchTxnRecoveryFixtureToDecision(t, fixture)
	if err := transaction.store.prepareCommittedCleanup(
		transaction.key[:],
		transaction.journal,
	); err != nil {
		t.Fatal(err)
	}
	witness, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		0,
		applyPatchTransactionArtifactPostimageWitness,
	)
	if err != nil || witness.Rooted.Identity == nil {
		t.Fatal(errors.Join(err, errors.New("first postimage witness unavailable")))
	}
	witness.Rooted.RemovalAttempted = true
	checkpointErr := transaction.checkpoint()
	if checkpointErr != nil {
		t.Fatal(checkpointErr)
	}
	injected := errors.New("stop after exact removal quarantine")
	err = applyPatchTxnRemoveExact(
		transaction.intent.operations[0].targetAnchor,
		witness.Rooted.Basename,
		witness.Rooted.RemovalBasename,
		*witness.Rooted.Identity,
		false,
		func() error { return injected },
	)
	if !errors.Is(err, injected) {
		t.Fatalf("interrupted removal error = %v", err)
	}
	removalPath := filepath.Join(
		witness.Rooted.AnchorCanonicalPath,
		witness.Rooted.RemovalBasename,
	)
	removalInfo, err := os.Lstat(removalPath)
	if err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(workspacePath, "two.txt")
	removeErr := os.Remove(secondPath)
	if removeErr != nil {
		t.Fatal(removeErr)
	}
	writeErr := os.WriteFile(secondPath, []byte("alien\n"), 0o600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	err = tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	)
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("alien recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	currentRemoval, err := os.Lstat(removalPath)
	if err != nil || !os.SameFile(removalInfo, currentRemoval) {
		t.Fatalf("pending removal changed before whole conflict detection: %v", err)
	}
}

func TestApplyPatchTransactionRecoveryCleansIdentityCheckpointedPartialPostimageStage(
	t *testing.T,
) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	transaction := fixture.begin(t)
	stage, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		0,
		applyPatchTransactionArtifactPostimageStage,
	)
	if err != nil || stage.Rooted.Identity == nil {
		t.Fatal(errors.Join(err, errors.New("postimage stage unavailable")))
	}
	witness, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		0,
		applyPatchTransactionArtifactPostimageWitness,
	)
	if err != nil || witness.Rooted.Identity == nil {
		t.Fatal(errors.Join(err, errors.New("postimage witness unavailable")))
	}
	anchor := transaction.intent.operations[0].targetAnchor
	removeErr := applyPatchTxnRemoveExact(
		anchor,
		witness.Rooted.Basename,
		witness.Rooted.RemovalBasename,
		*witness.Rooted.Identity,
		false,
	)
	if removeErr != nil {
		t.Fatal(removeErr)
	}
	resetApplyPatchTxnSourceProbeArtifact(witness.Rooted)
	stage.Rooted.Links = 1
	checkpointErr := transaction.checkpoint()
	if checkpointErr != nil {
		t.Fatal(checkpointErr)
	}
	stagePath := filepath.Join(stage.Rooted.AnchorCanonicalPath, stage.Rooted.Basename)
	truncateErr := os.Truncate(stagePath, 3)
	if truncateErr != nil {
		t.Fatal(truncateErr)
	}
	file, err := os.Open(stagePath)
	if err != nil {
		t.Fatal(err)
	}
	syncErr := file.Sync()
	if syncErr != nil {
		_ = file.Close()
		t.Fatal(syncErr)
	}
	closeErr := file.Close()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	stageInfo, err := os.Lstat(stagePath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	err = tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	)
	if err != nil {
		t.Fatalf("partial-stage recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	_, stageErr := os.Lstat(stagePath)
	if !errors.Is(stageErr, os.ErrNotExist) {
		t.Fatalf(
			"identity-checkpointed partial stage remains: %v (was %v)",
			stageErr,
			stageInfo,
		)
	}
	assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
}

func TestApplyPatchTransactionRecoveryCleansIdentityCheckpointedPartialBackup(
	t *testing.T,
) {
	workspacePath := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(workspacePath, "source.txt"),
		[]byte("before\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	fixture := newApplyPatchTxnRecoveryFixtureForPatch(
		t,
		workspacePath,
		"*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+after\n*** End Patch",
	)
	transaction := fixture.begin(t)
	backup, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		0,
		applyPatchTransactionArtifactBackupBlob,
	)
	if err != nil || backup.StateIdentity == nil {
		t.Fatal(errors.Join(err, errors.New("backup unavailable")))
	}
	workspaceStatePath, err := transaction.workspaceState.directoryPath()
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(
		workspaceStatePath,
		transaction.store.activeName,
		backup.StateName,
	)
	truncateErr := os.Truncate(backupPath, 3)
	if truncateErr != nil {
		t.Fatal(truncateErr)
	}
	file, err := os.Open(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	syncErr := file.Sync()
	if syncErr != nil {
		_ = file.Close()
		t.Fatal(syncErr)
	}
	closeErr := file.Close()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	recoveryErr := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	)
	if recoveryErr != nil {
		t.Fatalf("partial-backup recovery error = %v", recoveryErr)
	}
	fixture.closeReopened(t, state, workspaceState)
	assertApplyPatchTxnTestFile(
		t,
		filepath.Join(workspacePath, "source.txt"),
		"before\n",
		0o600,
	)
	_, backupErr := os.Lstat(backupPath)
	if !errors.Is(backupErr, os.ErrNotExist) {
		t.Fatalf("identity-checkpointed partial backup remains: %v", backupErr)
	}
	assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
}

func prepareApplyPatchTxnInitialJournalStage(
	t *testing.T,
	fixture *applyPatchTxnRecoveryFixture,
) *applyPatchPreparedTransaction {
	t.Helper()
	key, err := fixture.state.authenticationKey()
	if err != nil {
		t.Fatal(err)
	}
	intent, err := buildApplyPatchTxnIntent(context.Background(), fixture.plan)
	if err != nil {
		clear(key[:])
		t.Fatal(err)
	}
	workspaceBinding, err := newApplyPatchTxnWorkspaceBinding(fixture.plan.workspace)
	if err != nil {
		_ = intent.Close()
		clear(key[:])
		t.Fatal(err)
	}
	rootPath, err := fixture.state.rootPath()
	if err != nil {
		_ = intent.Close()
		clear(key[:])
		t.Fatal(err)
	}
	rootIdentity, err := fixture.state.rootIdentity()
	if err != nil {
		_ = intent.Close()
		clear(key[:])
		t.Fatal(err)
	}
	workspaceRelative, err := fixture.workspaceState.directoryRelative()
	if err != nil {
		_ = intent.Close()
		clear(key[:])
		t.Fatal(err)
	}
	stateBinding, err := newApplyPatchTxnStateBinding(
		rootPath, rootIdentity, key[:], workspaceRelative, intent,
	)
	if err != nil {
		_ = intent.Close()
		clear(key[:])
		t.Fatal(err)
	}
	journal, err := newApplyPatchTxnPreparingJournal(
		key[:], workspaceBinding, stateBinding, intent,
	)
	if err != nil {
		_ = intent.Close()
		clear(key[:])
		t.Fatal(err)
	}
	store, err := createApplyPatchTxnStore(fixture.workspaceState, intent)
	if err != nil {
		_ = intent.Close()
		clear(key[:])
		t.Fatal(err)
	}
	return &applyPatchPreparedTransaction{
		state: fixture.state, workspaceState: fixture.workspaceState,
		intent: intent, journal: journal, store: store, key: key,
	}
}

func closeApplyPatchTxnInitialRecoveryFixture(
	t *testing.T,
	fixture *applyPatchTxnRecoveryFixture,
) {
	t.Helper()
	if err := fixture.workspaceState.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.state.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.workspaceState = nil
	fixture.state = nil
}
