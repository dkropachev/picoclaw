package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionRecoveryPreparingCleansPrivateState(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	transaction := fixture.begin(t)
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(),
		state,
		workspaceState,
		fixture.workspace,
	); err != nil {
		t.Fatalf("preparing recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	if _, err := os.Lstat(filepath.Join(fixture.workspacePath, "result.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preparing recovery published target: %v", err)
	}
	assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
}

func TestApplyPatchTransactionRecoveryResumesAuthenticatedRemovalQuarantine(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	transaction := fixture.begin(t)
	witness, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		0,
		applyPatchTransactionArtifactPostimageWitness,
	)
	if err != nil || witness.Rooted.Identity == nil {
		t.Fatal(errors.Join(err, errors.New("postimage witness unavailable")))
	}
	prepareCleanupErr := transaction.store.preparePrivateCleanup(
		transaction.key[:],
		transaction.journal,
	)
	if prepareCleanupErr != nil {
		t.Fatal(prepareCleanupErr)
	}
	witness.Rooted.RemovalAttempted = true
	checkpointErr := transaction.checkpoint()
	if checkpointErr != nil {
		t.Fatal(checkpointErr)
	}
	injected := errors.New("interrupted authenticated removal")
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
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	); err != nil {
		t.Fatalf("removal-quarantine recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
}

func TestApplyPatchTransactionRecoveryResumesCommittedRemovalQuarantine(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
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
		t.Fatal(errors.Join(err, errors.New("committed witness unavailable")))
	}
	witness.Rooted.RemovalAttempted = true
	checkpointErr := transaction.checkpoint()
	if checkpointErr != nil {
		t.Fatal(checkpointErr)
	}
	injected := errors.New("interrupted committed removal")
	err = applyPatchTxnRemoveExact(
		transaction.intent.operations[0].targetAnchor,
		witness.Rooted.Basename,
		witness.Rooted.RemovalBasename,
		*witness.Rooted.Identity,
		false,
		func() error { return injected },
	)
	if !errors.Is(err, injected) {
		t.Fatalf("interrupted committed removal error = %v", err)
	}
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	); err != nil {
		t.Fatalf("committed removal recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	assertApplyPatchTxnTestFileModeNarrowed(
		t,
		filepath.Join(fixture.workspacePath, "result.txt"),
		"candidate\n",
		0o644,
	)
}

func TestApplyPatchTransactionRecoveryReconcilesJournalStageRemovalProgress(t *testing.T) {
	for _, testCase := range []struct {
		name              string
		currentAttempted  bool
		stagedAttempted   bool
		removeBeforeCrash bool
	}{
		{
			name:            "flag set stage before removal",
			stagedAttempted: true,
		},
		{
			name:              "flag clear stage after removal",
			currentAttempted:  true,
			removeBeforeCrash: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
			transaction := fixture.begin(t)
			if len(transaction.intent.forests) != 1 {
				t.Fatalf("forest intents = %d, want 1", len(transaction.intent.forests))
			}
			forestIntent := transaction.intent.forests[0]
			journalForest, err := requireApplyPatchTxnJournalForest(
				transaction.journal,
				forestIntent.id,
			)
			if err != nil || journalForest.SentinelWitness.Identity == nil {
				t.Fatal(errors.Join(err, errors.New("forest sentinel witness unavailable")))
			}
			sentinelEntry := applyPatchTxnRecoveryTestSentinelEntry(t, journalForest)
			journalForest.SentinelWitness.RemovalAttempted = testCase.currentAttempted
			sentinelEntry.RemovalAttempted = testCase.currentAttempted
			if testCase.currentAttempted {
				if checkpointErr := transaction.checkpoint(); checkpointErr != nil {
					t.Fatal(checkpointErr)
				}
			}

			if testCase.removeBeforeCrash {
				if removeErr := applyPatchTxnRemoveExact(
					forestIntent.anchor,
					journalForest.SentinelWitness.Basename,
					journalForest.SentinelWitness.RemovalBasename,
					*journalForest.SentinelWitness.Identity,
					false,
				); removeErr != nil {
					t.Fatal(removeErr)
				}
				if syncErr := applyPatchTxnSyncDirectory(forestIntent.anchor); syncErr != nil {
					t.Fatal(syncErr)
				}
				applyPatchTxnRecoveryTestRemoveForestEntry(
					t,
					forestIntent,
					sentinelEntry,
				)
			}

			stagedJournal := cloneApplyPatchTxnRecoveryJournal(
				t,
				transaction.key[:],
				transaction.journal,
			)
			stagedForest, err := requireApplyPatchTxnJournalForest(
				stagedJournal,
				forestIntent.id,
			)
			if err != nil {
				t.Fatal(err)
			}
			stagedForest.SentinelWitness.RemovalAttempted = testCase.stagedAttempted
			applyPatchTxnRecoveryTestSentinelEntry(
				t,
				stagedForest,
			).RemovalAttempted = testCase.stagedAttempted
			if !sameApplyPatchTxnJournalTopology(transaction.journal, stagedJournal) {
				t.Fatal("removal progress changed authenticated journal topology")
			}
			writeApplyPatchTxnRecoveryJournalStage(t, transaction, stagedJournal)
			fixture.simulateCrash(t, transaction)

			state, workspaceState := fixture.reopen(t)
			tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
			if err := tool.recoverApplyPatchTransaction(
				context.Background(), state, workspaceState, fixture.workspace,
			); err != nil {
				t.Fatalf("journal-stage recovery error = %v", err)
			}
			fixture.closeReopened(t, state, workspaceState)
			if _, err := os.Lstat(filepath.Join(
				fixture.workspacePath,
				"nested/deeper/result.txt",
			)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("preparing recovery published target: %v", err)
			}
			assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
			assertApplyPatchTxnRecoveryJournalStageAbsent(t, fixture.stateRoot)
		})
	}
}

func TestApplyPatchTransactionRecoveryResumesCommittedSourceWitnessOnlyCleanup(t *testing.T) {
	workspacePath := t.TempDir()
	sourcePath := filepath.Join(workspacePath, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("before\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	fixture := newApplyPatchTxnRecoveryFixtureForPatch(
		t,
		workspacePath,
		"*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+after\n*** End Patch",
	)
	transaction := commitApplyPatchTxnRecoveryFixtureToDecision(t, fixture)
	if err := transaction.store.prepareCommittedCleanup(
		transaction.key[:], transaction.journal,
	); err != nil {
		t.Fatal(err)
	}
	quarantine, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		0,
		applyPatchTransactionArtifactSourceQuarantine,
	)
	if err != nil || quarantine.Rooted.Identity == nil {
		t.Fatal(errors.Join(err, errors.New("committed source quarantine unavailable")))
	}
	if err := transaction.removeRooted(
		transaction.intent.operations[0].source.anchor,
		quarantine.Rooted,
		false,
	); err != nil {
		t.Fatal(err)
	}
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	); err != nil {
		t.Fatalf("committed source cleanup recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	assertApplyPatchTxnTestFile(t, sourcePath, "after\n", 0o640)
}

func TestApplyPatchTransactionRecoveryPreservesUnattemptedRemovalCanary(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "target.txt")
	transaction := fixture.begin(t)
	if err := transaction.revalidate(context.Background(), fixture.plan); err != nil {
		t.Fatal(err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	transaction.effects = applyPatchTxnEffects{
		sourceQuarantined:         make(map[int]bool),
		sourceRestoreRequired:     make(map[int]bool),
		targetPublished:           make(map[int]bool),
		targetRollbackQuarantined: make(map[int]bool),
		forestPublished:           make(map[string]bool),
		forestRollbackQuarantined: make(map[string]bool),
	}
	if err := transaction.publishTargets(); err != nil {
		t.Fatal(err)
	}
	stage, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		0,
		applyPatchTransactionArtifactPostimageStage,
	)
	if err != nil || stage.Rooted.RemovalAttempted {
		t.Fatal(errors.Join(err, errors.New("unexpected stage removal state")))
	}
	publicTarget := filepath.Join(fixture.workspacePath, "target.txt")
	canary := filepath.Join(fixture.workspacePath, stage.Rooted.RemovalBasename)
	if err := os.Link(publicTarget, canary); err != nil {
		t.Fatal(err)
	}
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	recoveryErr := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	)
	if recoveryErr == nil {
		t.Fatal("unattempted removal canary was accepted")
	}
	fixture.closeReopened(t, state, workspaceState)
	assertApplyPatchTxnTestFileModeNarrowed(t, publicTarget, "candidate\n", 0o644)
	assertApplyPatchTxnTestFileModeNarrowed(t, canary, "candidate\n", 0o644)
}

func TestApplyPatchTransactionRecoveryPreparedPublishedTargetRollsBack(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	transaction := fixture.begin(t)
	if err := transaction.revalidate(context.Background(), fixture.plan); err != nil {
		t.Fatal(err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	transaction.effects = applyPatchTxnEffects{
		sourceQuarantined: make(map[int]bool),
		targetPublished:   make(map[int]bool),
		forestPublished:   make(map[string]bool),
	}
	if err := transaction.publishTargets(); err != nil {
		t.Fatal(err)
	}
	assertApplyPatchTxnTestFileModeNarrowed(
		t,
		filepath.Join(fixture.workspacePath, "result.txt"),
		"candidate\n",
		0o644,
	)
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(),
		state,
		workspaceState,
		fixture.workspace,
	); err != nil {
		t.Fatalf("prepared recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	if _, err := os.Lstat(filepath.Join(fixture.workspacePath, "result.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared recovery retained target: %v", err)
	}
	assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
}

func TestApplyPatchTransactionRecoveryResumesTargetWitnessOnlyCleanup(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	transaction := fixture.begin(t)
	if err := transaction.revalidate(context.Background(), fixture.plan); err != nil {
		t.Fatal(err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	transaction.effects = applyPatchTxnEffects{
		sourceQuarantined:         make(map[int]bool),
		sourceRestoreRequired:     make(map[int]bool),
		targetPublished:           make(map[int]bool),
		targetRollbackQuarantined: make(map[int]bool),
		forestPublished:           make(map[string]bool),
		forestRollbackQuarantined: make(map[string]bool),
	}
	if err := transaction.publishTargets(); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("interrupted target witness cleanup")
	transaction.fault = func(boundary string) error {
		if boundary == "rollback_target_quarantine_removed:0" {
			return injected
		}
		return nil
	}
	rollbackErr := transaction.rollback(injected)
	if !errors.Is(rollbackErr, errApplyPatchRollbackIncomplete) {
		t.Fatalf("rollback error = %v, want incomplete", rollbackErr)
	}
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	); err != nil {
		t.Fatalf("target witness cleanup recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	if _, err := os.Lstat(filepath.Join(fixture.workspacePath, "result.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target remained after resumed cleanup: %v", err)
	}
}

func TestApplyPatchTransactionRecoveryPreparedPublishedForestRollsBack(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
	transaction := fixture.begin(t)
	if err := transaction.revalidate(context.Background(), fixture.plan); err != nil {
		t.Fatal(err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	transaction.effects = applyPatchTxnEffects{
		sourceQuarantined:         make(map[int]bool),
		targetPublished:           make(map[int]bool),
		targetRollbackQuarantined: make(map[int]bool),
		forestPublished:           make(map[string]bool),
		forestRollbackQuarantined: make(map[string]bool),
	}
	if err := transaction.publishTargets(); err != nil {
		t.Fatal(err)
	}
	assertApplyPatchTxnTestFileModeNarrowed(
		t,
		filepath.Join(fixture.workspacePath, "nested", "deeper", "result.txt"),
		"candidate\n",
		0o644,
	)
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	); err != nil {
		t.Fatalf("published-forest recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	if _, err := os.Lstat(filepath.Join(fixture.workspacePath, "nested")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared forest recovery retained tree: %v", err)
	}
	assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
}

func TestApplyPatchTransactionRecoveryCommittedKeepsPostimage(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	transaction := commitApplyPatchTxnRecoveryFixtureToDecision(t, fixture)
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(),
		state,
		workspaceState,
		fixture.workspace,
	); err != nil {
		t.Fatalf("committed recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	assertApplyPatchTxnTestFileModeNarrowed(
		t,
		filepath.Join(fixture.workspacePath, "result.txt"),
		"candidate\n",
		0o644,
	)
	assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
}

func TestApplyPatchTransactionRecoveryCommittedQuarantinedDirectory(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	transaction := commitApplyPatchTxnRecoveryFixtureToDecision(t, fixture)
	if err := transaction.store.prepareCommittedCleanup(
		transaction.key[:],
		transaction.journal,
	); err != nil {
		t.Fatal(err)
	}
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	); err != nil {
		t.Fatalf("committed-directory recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	assertApplyPatchTxnTestFileModeNarrowed(
		t,
		filepath.Join(fixture.workspacePath, "result.txt"),
		"candidate\n",
		0o644,
	)
}

func TestApplyPatchTransactionRecoveryAfterCommittedJournalDeletion(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	transaction := commitApplyPatchTxnRecoveryFixtureToDecision(t, fixture)
	if err := transaction.store.prepareCommittedCleanup(
		transaction.key[:], transaction.journal,
	); err != nil {
		t.Fatal(err)
	}
	if err := transaction.cleanupCommittedPublicArtifacts(); err != nil {
		t.Fatal(err)
	}
	removeAllApplyPatchTxnRecoveryOwnedState(t, transaction.store)
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	); err != nil {
		t.Fatalf("journal-deleted recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	assertApplyPatchTxnTestFileModeNarrowed(
		t,
		filepath.Join(fixture.workspacePath, "result.txt"),
		"candidate\n",
		0o644,
	)
}

func TestApplyPatchTransactionRecoveryAfterCommittedWitnessCleanup(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	transaction := commitApplyPatchTxnRecoveryFixtureToDecision(t, fixture)
	if err := transaction.store.prepareCommittedCleanup(
		transaction.key[:], transaction.journal,
	); err != nil {
		t.Fatal(err)
	}
	if err := transaction.cleanupCommittedPublicArtifacts(); err != nil {
		t.Fatal(err)
	}
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	); err != nil {
		t.Fatalf("witness-cleanup recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	assertApplyPatchTxnTestFileModeNarrowed(
		t,
		filepath.Join(fixture.workspacePath, "result.txt"),
		"candidate\n",
		0o644,
	)
}

func TestApplyPatchTransactionRecoveryAfterCommittedForestWitnessCleanup(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "nested/result.txt")
	transaction := commitApplyPatchTxnRecoveryFixtureToDecision(t, fixture)
	if err := transaction.store.prepareCommittedCleanup(
		transaction.key[:], transaction.journal,
	); err != nil {
		t.Fatal(err)
	}
	if err := transaction.cleanupCommittedPublicArtifacts(); err != nil {
		t.Fatal(err)
	}
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	); err != nil {
		t.Fatalf("forest-witness-cleanup recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	assertApplyPatchTxnTestFileModeNarrowed(
		t,
		filepath.Join(fixture.workspacePath, "nested", "result.txt"),
		"candidate\n",
		0o644,
	)
}

func TestApplyPatchTransactionRecoveryAfterCommittedDirectoryDeletion(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	transaction := commitApplyPatchTxnRecoveryFixtureToDecision(t, fixture)
	if err := transaction.store.prepareCommittedCleanup(
		transaction.key[:], transaction.journal,
	); err != nil {
		t.Fatal(err)
	}
	if err := transaction.cleanupCommittedPublicArtifacts(); err != nil {
		t.Fatal(err)
	}
	removeAllApplyPatchTxnRecoveryOwnedState(t, transaction.store)
	removeApplyPatchTxnRecoveryCommittedDirectoryOnly(t, transaction.store)
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	); err != nil {
		t.Fatalf("directory-deleted recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	assertApplyPatchTxnTestFileModeNarrowed(
		t,
		filepath.Join(fixture.workspacePath, "result.txt"),
		"candidate\n",
		0o644,
	)
}

func TestApplyPatchTransactionRecoveryPreparedUpdateAfterSourceQuarantine(t *testing.T) {
	workspacePath := t.TempDir()
	sourcePath := filepath.Join(workspacePath, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("before\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sourcePath, 0o640); err != nil {
		t.Fatal(err)
	}
	fixture := newApplyPatchTxnRecoveryFixtureForPatch(
		t,
		workspacePath,
		"*** Begin Patch\n*** Update File: source.txt\n"+
			"@@\n-before\n+after\n*** End Patch",
	)
	transaction := fixture.begin(t)
	if err := transaction.revalidate(context.Background(), fixture.plan); err != nil {
		t.Fatal(err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	transaction.effects = applyPatchTxnEffects{
		sourceQuarantined: make(map[int]bool),
		targetPublished:   make(map[int]bool),
		forestPublished:   make(map[string]bool),
	}
	if err := transaction.quarantineSources(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source was not quarantined: %v", err)
	}
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	); err != nil {
		t.Fatalf("source-quarantine recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	assertApplyPatchTxnTestFile(t, sourcePath, "before\n", 0o640)
	assertNoApplyPatchTxnWorkspaceResidue(t, workspacePath)
}

func TestApplyPatchTransactionRecoveryRestoresMissingQuarantineFromBackup(t *testing.T) {
	workspacePath := t.TempDir()
	sourcePath := filepath.Join(workspacePath, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("before backup fallback\n"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sourcePath, 0o751); err != nil {
		t.Fatal(err)
	}
	fixture := newApplyPatchTxnRecoveryFixtureForPatch(
		t,
		workspacePath,
		"*** Begin Patch\n*** Update File: source.txt\n*** Move to: moved.txt\n*** End Patch",
	)
	transaction := fixture.begin(t)
	if err := transaction.revalidate(context.Background(), fixture.plan); err != nil {
		t.Fatal(err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	transaction.effects = applyPatchTxnEffects{
		sourceQuarantined:         make(map[int]bool),
		sourceRestoreRequired:     make(map[int]bool),
		targetPublished:           make(map[int]bool),
		targetRollbackQuarantined: make(map[int]bool),
		forestPublished:           make(map[string]bool),
		forestRollbackQuarantined: make(map[string]bool),
	}
	if err := transaction.quarantineSources(); err != nil {
		t.Fatal(err)
	}
	operation := transaction.intent.operations[0]
	quarantine, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		0,
		applyPatchTransactionArtifactSourceQuarantine,
	)
	if err != nil || quarantine.Rooted.Identity == nil {
		t.Fatal(errors.Join(err, errors.New("source quarantine unavailable")))
	}
	removeErr := applyPatchTxnRemoveExact(
		operation.source.anchor,
		quarantine.Rooted.Basename,
		quarantine.Rooted.RemovalBasename,
		*quarantine.Rooted.Identity,
		false,
	)
	if removeErr != nil {
		t.Fatal(removeErr)
	}
	if err := applyPatchTxnSyncDirectory(operation.source.anchor); err != nil {
		t.Fatal(err)
	}
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	); err != nil {
		t.Fatalf("backup fallback recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	assertApplyPatchTxnTestFile(
		t,
		sourcePath,
		"before backup fallback\n",
		0o751,
	)
	if _, err := os.Lstat(filepath.Join(workspacePath, "moved.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup fallback published move target: %v", err)
	}
	assertNoApplyPatchTxnWorkspaceResidue(t, workspacePath)
}

func TestApplyPatchTransactionRecoveryRestoresPublishedUpdateWithMissingQuarantine(t *testing.T) {
	workspacePath := t.TempDir()
	sourcePath := filepath.Join(workspacePath, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("before update fallback\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	fixture := newApplyPatchTxnRecoveryFixtureForPatch(
		t,
		workspacePath,
		"*** Begin Patch\n*** Update File: source.txt\n"+
			"@@\n-before update fallback\n+after update fallback\n*** End Patch",
	)
	transaction := fixture.begin(t)
	if err := transaction.revalidate(context.Background(), fixture.plan); err != nil {
		t.Fatal(err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	transaction.effects = applyPatchTxnEffects{
		sourceQuarantined:         make(map[int]bool),
		sourceRestoreRequired:     make(map[int]bool),
		targetPublished:           make(map[int]bool),
		targetRollbackQuarantined: make(map[int]bool),
		forestPublished:           make(map[string]bool),
		forestRollbackQuarantined: make(map[string]bool),
	}
	if err := transaction.quarantineSources(); err != nil {
		t.Fatal(err)
	}
	if err := transaction.publishTargets(); err != nil {
		t.Fatal(err)
	}
	operation := transaction.intent.operations[0]
	quarantine, err := requireApplyPatchTxnArtifact(
		transaction.journal, operation.index,
		applyPatchTransactionArtifactSourceQuarantine,
	)
	if err != nil || quarantine.Rooted.Identity == nil {
		t.Fatal(errors.Join(err, errors.New("update quarantine unavailable")))
	}
	if err := applyPatchTxnRemoveExact(
		operation.source.anchor,
		quarantine.Rooted.Basename,
		quarantine.Rooted.RemovalBasename,
		*quarantine.Rooted.Identity,
		false,
	); err != nil {
		t.Fatal(err)
	}
	if err := applyPatchTxnSyncDirectory(operation.source.anchor); err != nil {
		t.Fatal(err)
	}
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	); err != nil {
		t.Fatalf("published update fallback recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	assertApplyPatchTxnTestFile(t, sourcePath, "before update fallback\n", 0o640)
}

func TestApplyPatchTransactionRecoveryResumesCheckpointedBackupRestoreStage(t *testing.T) {
	fixture, transaction, operation, restore := prepareApplyPatchTxnBackupFallbackStage(t)
	backup, err := transaction.store.readBackup(
		transaction.key[:], transaction.journal, operation.index,
	)
	if err != nil {
		t.Fatal(err)
	}
	file, identity, err := applyPatchTxnCreateRegular(
		operation.source.anchor,
		restore.Rooted.Basename,
		0o600,
	)
	if err != nil {
		t.Fatal(err)
	}
	restore.Rooted.Identity = copyApplyPatchTxnIdentity(identity)
	restore.Rooted.Links = 1
	if err := applyPatchTxnSyncDirectory(operation.source.anchor); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := transaction.checkpoint(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	partial := backup[:len(backup)/2]
	if _, err := file.Write(partial); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.simulateCrash(t, transaction)

	recoverApplyPatchTxnBackupFallbackFixture(t, fixture)
	assertApplyPatchTxnTestFile(
		t,
		filepath.Join(fixture.workspacePath, "source.txt"),
		"before backup fallback\n",
		0o751,
	)
}

func TestApplyPatchTransactionRecoveryResumesPublishedBackupRestoreWitness(t *testing.T) {
	fixture, transaction, operation, restore := prepareApplyPatchTxnBackupFallbackStage(t)
	backup, err := transaction.store.readBackup(
		transaction.key[:], transaction.journal, operation.index,
	)
	if err != nil {
		t.Fatal(err)
	}
	file, identity, err := applyPatchTxnCreateRegular(
		operation.source.anchor, restore.Rooted.Basename, 0o600,
	)
	if err != nil {
		t.Fatal(err)
	}
	restore.Rooted.Identity = copyApplyPatchTxnIdentity(identity)
	restore.Rooted.Links = 1
	if err := applyPatchTxnSyncDirectory(operation.source.anchor); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := transaction.checkpoint(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := applyPatchTxnWriteRegularContext(
		context.Background(), file, backup, 0o751, true,
	); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := applyPatchTxnLinkWitness(
		operation.source.anchor,
		restore.Rooted.Basename,
		identity,
		2,
		operation.source.anchor,
		operation.source.basename,
		restore.Rooted.RemovalBasename,
	); err != nil {
		t.Fatal(err)
	}
	restore.Rooted.Links = 2
	if err := applyPatchTxnSyncDirectory(operation.source.anchor); err != nil {
		t.Fatal(err)
	}
	if err := transaction.checkpoint(); err != nil {
		t.Fatal(err)
	}
	fixture.simulateCrash(t, transaction)

	recoverApplyPatchTxnBackupFallbackFixture(t, fixture)
	assertApplyPatchTxnTestFile(
		t,
		filepath.Join(fixture.workspacePath, "source.txt"),
		"before backup fallback\n",
		0o751,
	)
}

func TestApplyPatchTransactionRecoveryCheckpointsUnjournaledBackupRestoreLink(t *testing.T) {
	fixture, transaction, operation, restore := prepareApplyPatchTxnBackupFallbackStage(t)
	transaction.effects.sourceQuarantined[operation.index] = false
	transaction.effects.sourceRestoreRequired[operation.index] = true
	publicationFault := errors.New("crash after backup restore hard-link publication")
	transaction.fault = func(boundary string) error {
		if boundary == "restore_link_published_before_checkpoint:0" {
			return publicationFault
		}
		return nil
	}
	rollbackErr := transaction.rollback(publicationFault)
	if !errors.Is(rollbackErr, errApplyPatchRollbackIncomplete) ||
		!errors.Is(rollbackErr, publicationFault) {
		t.Fatalf("publication-window rollback error = %v", rollbackErr)
	}
	if restore.Rooted.Links != 1 {
		t.Fatalf("in-memory restore links = %d, want uncheckpointed 1", restore.Rooted.Links)
	}
	assertApplyPatchTxnBackupRestoreAliases(t, fixture.workspacePath, restore.Rooted.Basename)
	fixture.simulateCrash(t, transaction)

	recoveryFault := errors.New("crash after reconciled link checkpoint")
	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	tool.transactionFault = func(boundary string) error {
		if boundary == "restore_old_witness_removed:0" {
			return recoveryFault
		}
		return nil
	}
	recoveryErr := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	)
	fixture.closeReopened(t, state, workspaceState)
	if !errors.Is(recoveryErr, errApplyPatchRollbackIncomplete) ||
		!errors.Is(recoveryErr, recoveryFault) {
		t.Fatalf("checkpoint-window recovery error = %v", recoveryErr)
	}
	if links := readApplyPatchTxnRecoveryRestoreLinks(t, fixture, operation.index); links != 2 {
		t.Fatalf("persisted restore links = %d, want 2", links)
	}

	recoverApplyPatchTxnBackupFallbackFixture(t, fixture)
	assertApplyPatchTxnTestFile(
		t,
		filepath.Join(fixture.workspacePath, "source.txt"),
		"before backup fallback\n",
		0o751,
	)
	assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
}

func assertApplyPatchTxnBackupRestoreAliases(
	t *testing.T,
	workspace string,
	restoreBasename string,
) {
	t.Helper()
	publicInfo, err := os.Lstat(filepath.Join(workspace, "source.txt"))
	if err != nil {
		t.Fatal(err)
	}
	restoreInfo, err := os.Lstat(filepath.Join(workspace, restoreBasename))
	if err != nil {
		t.Fatal(err)
	}
	publicFile, err := os.Open(filepath.Join(workspace, "source.txt"))
	if err != nil {
		t.Fatal(err)
	}
	links, linksErr := applyPatchLinkCount(publicFile, publicInfo)
	closeErr := publicFile.Close()
	if linksErr != nil || closeErr != nil {
		t.Fatal(errors.Join(linksErr, closeErr))
	}
	if !os.SameFile(publicInfo, restoreInfo) || links != 2 {
		t.Fatalf(
			"backup restore aliases = same:%t links:%d",
			os.SameFile(publicInfo, restoreInfo),
			links,
		)
	}
}

func readApplyPatchTxnRecoveryRestoreLinks(
	t *testing.T,
	fixture *applyPatchTxnRecoveryFixture,
	operationIndex int,
) uint64 {
	t.Helper()
	state, workspaceState := fixture.reopen(t)
	key, err := state.authenticationKey()
	if err != nil {
		t.Fatal(err)
	}
	store, journal, err := openApplyPatchTxnRecoveryStore(workspaceState, key[:])
	clear(key[:])
	if err != nil {
		fixture.closeReopened(t, state, workspaceState)
		t.Fatal(err)
	}
	restore, err := requireApplyPatchTxnArtifact(
		journal,
		operationIndex,
		applyPatchTransactionArtifactSourceRestoreStage,
	)
	if err != nil || restore.Rooted == nil {
		_ = store.Close()
		fixture.closeReopened(t, state, workspaceState)
		t.Fatal(errors.Join(err, errors.New("persisted restore artifact unavailable")))
	}
	links := restore.Rooted.Links
	if err := store.Close(); err != nil {
		fixture.closeReopened(t, state, workspaceState)
		t.Fatal(err)
	}
	fixture.closeReopened(t, state, workspaceState)
	return links
}

func TestApplyPatchTransactionRecoveryResumesBackupWitnessCleanupFaults(t *testing.T) {
	for _, boundary := range []string{
		"restore_old_witness_removed:0",
		"restore_private_witness_removed:0",
	} {
		t.Run(boundary, func(t *testing.T) {
			fixture, transaction, operation, _ := prepareApplyPatchTxnBackupFallbackStage(t)
			transaction.effects.sourceQuarantined[operation.index] = false
			transaction.effects.sourceRestoreRequired[operation.index] = true
			injected := errors.New("injected backup witness cleanup interruption")
			transaction.fault = func(got string) error {
				if got == boundary {
					return injected
				}
				return nil
			}
			rollbackErr := transaction.rollback(injected)
			if !errors.Is(rollbackErr, errApplyPatchRollbackIncomplete) {
				t.Fatalf("rollback error = %v, want incomplete", rollbackErr)
			}
			fixture.simulateCrash(t, transaction)
			recoverApplyPatchTxnBackupFallbackFixture(t, fixture)
			assertApplyPatchTxnTestFile(
				t,
				filepath.Join(fixture.workspacePath, "source.txt"),
				"before backup fallback\n",
				0o751,
			)
		})
	}
}

func TestApplyPatchTransactionRecoveryBackupRestoreFaultBoundaries(t *testing.T) {
	for _, testCase := range []struct {
		boundary     string
		wantConflict bool
	}{
		{boundary: "restore_create_before_identity:0", wantConflict: true},
		{boundary: "restore_identity_checkpoint:0"},
		{boundary: "restore_stage_synced:0"},
		{boundary: "restore_published:0"},
	} {
		t.Run(testCase.boundary, func(t *testing.T) {
			fixture, transaction, operation, _ := prepareApplyPatchTxnBackupFallbackStage(t)
			transaction.effects.sourceQuarantined[operation.index] = false
			transaction.effects.sourceRestoreRequired[operation.index] = true
			injected := errors.New("injected backup restore interruption")
			transaction.fault = func(got string) error {
				if got == testCase.boundary {
					return injected
				}
				return nil
			}
			rollbackErr := transaction.rollback(injected)
			if !errors.Is(rollbackErr, errApplyPatchRollbackIncomplete) {
				t.Fatalf("rollback error = %v, want incomplete", rollbackErr)
			}
			fixture.simulateCrash(t, transaction)
			state, workspaceState := fixture.reopen(t)
			tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
			recoveryErr := tool.recoverApplyPatchTransaction(
				context.Background(), state, workspaceState, fixture.workspace,
			)
			fixture.closeReopened(t, state, workspaceState)
			if testCase.wantConflict {
				if recoveryErr == nil {
					t.Fatal("uncheckpointed restore artifact was cleaned automatically")
				}
				return
			}
			if recoveryErr != nil {
				t.Fatalf("backup restore recovery error = %v", recoveryErr)
			}
			assertApplyPatchTxnTestFile(
				t,
				filepath.Join(fixture.workspacePath, "source.txt"),
				"before backup fallback\n",
				0o751,
			)
		})
	}
}

func prepareApplyPatchTxnBackupFallbackStage(
	t *testing.T,
) (*applyPatchTxnRecoveryFixture, *applyPatchPreparedTransaction, *applyPatchTxnIntent, *applyPatchTransactionJournalArtifact) {
	t.Helper()
	workspacePath := t.TempDir()
	sourcePath := filepath.Join(workspacePath, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("before backup fallback\n"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sourcePath, 0o751); err != nil {
		t.Fatal(err)
	}
	fixture := newApplyPatchTxnRecoveryFixtureForPatch(
		t,
		workspacePath,
		"*** Begin Patch\n*** Update File: source.txt\n*** Move to: moved.txt\n*** End Patch",
	)
	transaction := fixture.begin(t)
	if err := transaction.revalidate(context.Background(), fixture.plan); err != nil {
		t.Fatal(err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	transaction.effects = applyPatchTxnEffects{
		sourceQuarantined:         make(map[int]bool),
		sourceRestoreRequired:     make(map[int]bool),
		targetPublished:           make(map[int]bool),
		targetRollbackQuarantined: make(map[int]bool),
		forestPublished:           make(map[string]bool),
		forestRollbackQuarantined: make(map[string]bool),
	}
	if err := transaction.quarantineSources(); err != nil {
		t.Fatal(err)
	}
	operation := transaction.intent.operations[0]
	quarantine, err := requireApplyPatchTxnArtifact(
		transaction.journal, operation.index,
		applyPatchTransactionArtifactSourceQuarantine,
	)
	if err != nil || quarantine.Rooted.Identity == nil {
		t.Fatal(errors.Join(err, errors.New("source quarantine unavailable")))
	}
	removeErr := applyPatchTxnRemoveExact(
		operation.source.anchor,
		quarantine.Rooted.Basename,
		quarantine.Rooted.RemovalBasename,
		*quarantine.Rooted.Identity,
		false,
	)
	if removeErr != nil {
		t.Fatal(removeErr)
	}
	syncErr := applyPatchTxnSyncDirectory(operation.source.anchor)
	if syncErr != nil {
		t.Fatal(syncErr)
	}
	transaction.journal.Phase = applyPatchTransactionPhaseRollingBack
	checkpointErr := transaction.checkpoint()
	if checkpointErr != nil {
		t.Fatal(checkpointErr)
	}
	restore, err := requireApplyPatchTxnArtifact(
		transaction.journal, operation.index,
		applyPatchTransactionArtifactSourceRestoreStage,
	)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, transaction, operation, restore
}

func recoverApplyPatchTxnBackupFallbackFixture(
	t *testing.T,
	fixture *applyPatchTxnRecoveryFixture,
) {
	t.Helper()
	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	); err != nil {
		t.Fatalf("backup fallback recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
}

func TestApplyPatchTransactionRecoveryPreparedPublishedUpdateRollsBack(t *testing.T) {
	workspacePath := t.TempDir()
	sourcePath := filepath.Join(workspacePath, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := newApplyPatchTxnRecoveryFixtureForPatch(
		t,
		workspacePath,
		"*** Begin Patch\n*** Update File: source.txt\n"+
			"@@\n-before\n+after\n*** End Patch",
	)
	transaction := fixture.begin(t)
	if err := transaction.revalidate(context.Background(), fixture.plan); err != nil {
		t.Fatal(err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	transaction.effects = applyPatchTxnEffects{
		sourceQuarantined: make(map[int]bool),
		targetPublished:   make(map[int]bool),
		forestPublished:   make(map[string]bool),
	}
	if err := transaction.quarantineSources(); err != nil {
		t.Fatal(err)
	}
	if err := transaction.publishTargets(); err != nil {
		t.Fatal(err)
	}
	assertApplyPatchTxnTestFile(t, sourcePath, "after\n", 0o600)
	fixture.simulateCrash(t, transaction)

	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(), state, workspaceState, fixture.workspace,
	); err != nil {
		t.Fatalf("published-update recovery error = %v", err)
	}
	fixture.closeReopened(t, state, workspaceState)
	assertApplyPatchTxnTestFile(t, sourcePath, "before\n", 0o600)
	assertNoApplyPatchTxnWorkspaceResidue(t, workspacePath)
}

func applyPatchTxnRecoveryTestSentinelEntry(
	t *testing.T,
	forest *applyPatchTransactionJournalForest,
) *applyPatchTransactionJournalForestEntry {
	t.Helper()
	if forest == nil {
		t.Fatal("journal forest is unavailable")
	}
	for index := range forest.Entries {
		entry := &forest.Entries[index]
		if entry.RelativePath == forest.SentinelRelativePath {
			if entry.Identity == nil {
				t.Fatal("journal forest sentinel identity is unavailable")
			}
			return entry
		}
	}
	t.Fatal("journal forest sentinel entry is unavailable")
	return nil
}

func applyPatchTxnRecoveryTestRemoveForestEntry(
	t *testing.T,
	intent *applyPatchTxnForestIntent,
	entry *applyPatchTransactionJournalForestEntry,
) {
	t.Helper()
	if intent == nil || entry == nil || entry.Identity == nil {
		t.Fatal("forest removal fixture is unavailable")
	}
	stageRootPath := filepath.Join(intent.anchorPath, intent.stageRoot)
	parentPath := stageRootPath
	parentRelative := filepath.Dir(filepath.FromSlash(entry.RelativePath))
	if parentRelative != "." {
		parentPath = filepath.Join(stageRootPath, parentRelative)
	}
	parent, err := openApplyPatchTxnAnchor(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	removeErr := applyPatchTxnRemoveExact(
		parent,
		filepath.Base(filepath.FromSlash(entry.RelativePath)),
		entry.RemovalBasename,
		*entry.Identity,
		entry.Kind == "directory",
	)
	if removeErr == nil {
		removeErr = applyPatchTxnSyncDirectory(parent)
	}
	closeErr := parent.Close()
	if removeErr != nil || closeErr != nil {
		t.Fatal(errors.Join(removeErr, closeErr))
	}
}

func writeApplyPatchTxnRecoveryJournalStage(
	t *testing.T,
	transaction *applyPatchPreparedTransaction,
	staged *applyPatchTransactionJournal,
) {
	t.Helper()
	if transaction == nil || transaction.store == nil {
		t.Fatal("transaction store is unavailable")
	}
	encoded, err := encodeApplyPatchTransactionJournal(transaction.key[:], staged)
	if err != nil {
		t.Fatal(err)
	}
	store := transaction.store
	store.mu.Lock()
	defer store.mu.Unlock()
	if validationErr := store.revalidateLocked(); validationErr != nil {
		t.Fatal(validationErr)
	}
	if journalErr := store.revalidateCurrentJournalLocked(transaction.key[:]); journalErr != nil {
		t.Fatal(journalErr)
	}
	file, err := store.activeRoot.OpenFile(
		applyPatchTransactionJournalStageFile,
		os.O_CREATE|os.O_EXCL|os.O_RDWR,
		0o600,
	)
	if err != nil {
		t.Fatal(err)
	}
	writeErr := writeApplyPatchTransactionSyncedFile(file, encoded)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal(errors.Join(writeErr, closeErr))
	}
	if err := syncApplyPatchTxnRootDirectory(store.activeRoot); err != nil {
		t.Fatal(err)
	}
}

func cloneApplyPatchTxnRecoveryJournal(
	t *testing.T,
	key []byte,
	journal *applyPatchTransactionJournal,
) *applyPatchTransactionJournal {
	t.Helper()
	encoded, err := encodeApplyPatchTransactionJournal(key, journal)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := decodeApplyPatchTransactionJournal(key, encoded)
	if err != nil {
		t.Fatal(err)
	}
	return clone
}

func assertApplyPatchTxnRecoveryJournalStageAbsent(t *testing.T, stateRoot string) {
	t.Helper()
	err := filepath.WalkDir(stateRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() == applyPatchTransactionJournalStageFile {
			t.Errorf("journal replacement stage remained at %q", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type applyPatchTxnRecoveryFixture struct {
	workspacePath  string
	stateRoot      string
	workspace      applyPatchWorkspace
	plan           *applyPatchPlan
	state          *applyPatchTransactionState
	workspaceState *applyPatchTransactionWorkspaceState
}

func newApplyPatchTxnRecoveryFixture(
	t *testing.T,
	target string,
) *applyPatchTxnRecoveryFixture {
	t.Helper()
	workspacePath := t.TempDir()
	return newApplyPatchTxnRecoveryFixtureForPatch(
		t,
		workspacePath,
		"*** Begin Patch\n*** Add File: "+target+"\n+candidate\n*** End Patch",
	)
}

func newApplyPatchTxnRecoveryFixtureForPatch(
	t *testing.T,
	workspacePath string,
	patch string,
) *applyPatchTxnRecoveryFixture {
	t.Helper()
	workspace, err := snapshotApplyPatchWorkspace(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	plan := buildApplyPatchTxnTestPlan(
		t,
		workspacePath,
		patch,
	)
	stateRoot := filepath.Join(t.TempDir(), "transaction-state")
	prepared, err := prepareApplyPatchTransactionStateRoot(
		workspacePath,
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
	workspaceState, err := state.lockWorkspace(context.Background(), workspace.canonical)
	if err != nil {
		_ = state.Close()
		t.Fatal(err)
	}
	return &applyPatchTxnRecoveryFixture{
		workspacePath:  workspacePath,
		stateRoot:      stateRoot,
		workspace:      workspace,
		plan:           plan,
		state:          state,
		workspaceState: workspaceState,
	}
}

func (fixture *applyPatchTxnRecoveryFixture) begin(
	t *testing.T,
) *applyPatchPreparedTransaction {
	t.Helper()
	transaction, err := beginApplyPatchTransaction(
		context.Background(),
		fixture.state,
		fixture.workspaceState,
		fixture.plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	return transaction
}

func (fixture *applyPatchTxnRecoveryFixture) simulateCrash(
	t *testing.T,
	transaction *applyPatchPreparedTransaction,
) {
	t.Helper()
	if err := transaction.closeHandles(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.workspaceState.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.state.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.workspaceState = nil
	fixture.state = nil
}

func (fixture *applyPatchTxnRecoveryFixture) reopen(
	t *testing.T,
) (*applyPatchTransactionState, *applyPatchTransactionWorkspaceState) {
	t.Helper()
	prepared, err := prepareApplyPatchTransactionStateRoot(
		fixture.workspacePath,
		fixture.stateRoot,
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
		fixture.workspace.canonical,
	)
	if err != nil {
		_ = state.Close()
		t.Fatal(err)
	}
	return state, workspaceState
}

func (fixture *applyPatchTxnRecoveryFixture) closeReopened(
	t *testing.T,
	state *applyPatchTransactionState,
	workspaceState *applyPatchTransactionWorkspaceState,
) {
	t.Helper()
	if err := workspaceState.Close(); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
}

func newApplyPatchTxnRecoveryTool(
	t *testing.T,
	workspace string,
	stateRoot string,
) *ApplyPatchTool {
	t.Helper()
	tool, err := NewApplyPatchToolWithPermissionsAndPolicy(
		workspace,
		true,
		true,
		true,
		ApplyPatchPreflightPolicy{TransactionStateRoot: stateRoot},
	)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func commitApplyPatchTxnRecoveryFixtureToDecision(
	t *testing.T,
	fixture *applyPatchTxnRecoveryFixture,
) *applyPatchPreparedTransaction {
	t.Helper()
	transaction := fixture.begin(t)
	if err := transaction.revalidate(context.Background(), fixture.plan); err != nil {
		t.Fatal(err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	transaction.effects = applyPatchTxnEffects{
		sourceQuarantined:         make(map[int]bool),
		targetPublished:           make(map[int]bool),
		targetRollbackQuarantined: make(map[int]bool),
		forestPublished:           make(map[string]bool),
		forestRollbackQuarantined: make(map[string]bool),
	}
	if err := transaction.quarantineSources(); err != nil {
		t.Fatal(err)
	}
	if err := transaction.publishTargets(); err != nil {
		t.Fatal(err)
	}
	if err := verifyApplyPatchTxnCommittedPublicState(
		transaction.intent, transaction.journal, transaction.effects,
	); err != nil {
		t.Fatal(err)
	}
	transaction.journal.DecisionAttempted = true
	if err := transaction.persistForwardDecisionState(); err != nil {
		t.Fatal(err)
	}
	transaction.journal.Phase = applyPatchTransactionPhaseCommitted
	if err := transaction.persistCommittedDecision(); err != nil {
		t.Fatal(err)
	}
	return transaction
}

func removeAllApplyPatchTxnRecoveryOwnedState(
	t *testing.T,
	store *applyPatchTxnStore,
) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	for name, identity := range store.owned {
		if err := removeApplyPatchTxnRootIdentity(
			store.activeRoot, name, identity,
		); err != nil {
			t.Fatal(err)
		}
		delete(store.owned, name)
	}
	if err := syncApplyPatchTxnRootDirectory(store.activeRoot); err != nil {
		t.Fatal(err)
	}
}

func removeApplyPatchTxnRecoveryCommittedDirectoryOnly(
	t *testing.T,
	store *applyPatchTxnStore,
) {
	t.Helper()
	store.mu.Lock()
	root := store.activeRoot
	store.activeRoot = nil
	store.closed = true
	name := store.activeName
	info := store.activeInfo
	workspace := store.workspace
	store.mu.Unlock()
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := workspace.withDirectoryAnchor(func(workspaceRoot *os.Root) error {
		current, err := workspaceRoot.Lstat(name)
		if err != nil || !os.SameFile(current, info) {
			return errors.Join(err, errors.New("committed directory identity changed"))
		}
		if err := workspaceRoot.Remove(name); err != nil {
			return err
		}
		return syncApplyPatchTxnRootDirectory(workspaceRoot)
	}); err != nil {
		t.Fatal(err)
	}
}
