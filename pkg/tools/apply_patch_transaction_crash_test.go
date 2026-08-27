package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchTransactionRecoveryPreparedMoveIntermediates(t *testing.T) {
	states := []string{
		"prepared",
		"source quarantine checkpointed",
		"source quarantine before checkpoint",
		"target publish before checkpoint",
		"rolling back source restored",
		"rolling back target quarantine before checkpoint",
		"rolling back target quarantined",
		"rolling back target removed",
	}
	for _, stateName := range states {
		t.Run(stateName, func(t *testing.T) {
			fixture := newApplyPatchTxnCrashMoveFixture(t)
			transaction := prepareApplyPatchTxnCrashTransaction(t, fixture)
			initializeApplyPatchTxnCrashEffects(transaction)

			switch stateName {
			case "prepared":
			case "source quarantine checkpointed":
				if err := transaction.quarantineSources(); err != nil {
					t.Fatal(err)
				}
			case "source quarantine before checkpoint":
				quarantineApplyPatchTxnCrashSourceBeforeCheckpoint(t, transaction)
			case "target publish before checkpoint":
				if err := transaction.quarantineSources(); err != nil {
					t.Fatal(err)
				}
				publishApplyPatchTxnCrashTargetBeforeCheckpoint(t, transaction)
			case "rolling back source restored":
				publishApplyPatchTxnCrashMove(t, transaction)
				markApplyPatchTxnCrashRollingBack(t, transaction)
				if err := transaction.restoreMoveSources(); err != nil {
					t.Fatal(err)
				}
			case "rolling back target quarantined":
				publishApplyPatchTxnCrashMove(t, transaction)
				markApplyPatchTxnCrashRollingBack(t, transaction)
				if err := transaction.restoreMoveSources(); err != nil {
					t.Fatal(err)
				}
				quarantineApplyPatchTxnCrashTarget(t, transaction)
			case "rolling back target quarantine before checkpoint":
				publishApplyPatchTxnCrashMove(t, transaction)
				markApplyPatchTxnCrashRollingBack(t, transaction)
				if err := transaction.restoreMoveSources(); err != nil {
					t.Fatal(err)
				}
				quarantineApplyPatchTxnCrashTargetBeforeCheckpoint(t, transaction)
			case "rolling back target removed":
				publishApplyPatchTxnCrashMove(t, transaction)
				markApplyPatchTxnCrashRollingBack(t, transaction)
				if err := transaction.restoreMoveSources(); err != nil {
					t.Fatal(err)
				}
				if err := transaction.rollbackPublishedTarget(
					transaction.intent.operations[0],
				); err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatalf("unknown crash state %q", stateName)
			}

			fixture.simulateCrash(t, transaction)
			recoverApplyPatchTxnCrashFixture(t, fixture)
			assertApplyPatchTxnTestFile(
				t,
				filepath.Join(fixture.workspacePath, "source.txt"),
				"before move\n",
				0o751,
			)
			if _, err := os.Lstat(filepath.Join(fixture.workspacePath, "target.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("move target remains after recovery: %v", err)
			}
			assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
			assertApplyPatchTxnFaultStateReady(
				t,
				fixture.workspacePath,
				fixture.stateRoot,
			)
		})
	}
}

func TestApplyPatchTransactionRecoveryCommittedMoveCleanupStates(t *testing.T) {
	states := []string{
		"committed directory",
		"journal removed",
		"pointer only",
	}
	for _, stateName := range states {
		t.Run(stateName, func(t *testing.T) {
			fixture := newApplyPatchTxnCrashMoveFixture(t)
			transaction := commitApplyPatchTxnRecoveryFixtureToDecision(t, fixture)
			if err := transaction.store.prepareCommittedCleanup(
				transaction.key[:],
				transaction.journal,
			); err != nil {
				t.Fatal(err)
			}
			if stateName != "committed directory" {
				if err := transaction.cleanupCommittedPublicArtifacts(); err != nil {
					t.Fatal(err)
				}
				removeAllApplyPatchTxnRecoveryOwnedState(t, transaction.store)
			}
			if stateName == "pointer only" {
				removeApplyPatchTxnRecoveryCommittedDirectoryOnly(t, transaction.store)
			}
			fixture.simulateCrash(t, transaction)
			recoverApplyPatchTxnCrashFixture(t, fixture)

			if _, err := os.Lstat(filepath.Join(fixture.workspacePath, "source.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("committed move source returned: %v", err)
			}
			assertApplyPatchTxnTestFile(
				t,
				filepath.Join(fixture.workspacePath, "target.txt"),
				"before move\n",
				0o751,
			)
			assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
			assertApplyPatchTxnFaultStateReady(
				t,
				fixture.workspacePath,
				fixture.stateRoot,
			)
		})
	}
}

func TestApplyPatchTransactionRecoveryRejectsAlienReplacementWithoutClobber(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*testing.T) *applyPatchTxnRecoveryFixture
		mutate func(*testing.T, *applyPatchPreparedTransaction, *applyPatchTxnRecoveryFixture)
		path   string
	}{
		{
			name: "published target inode replacement",
			setup: func(t *testing.T) *applyPatchTxnRecoveryFixture {
				return newApplyPatchTxnRecoveryFixture(t, "target.txt")
			},
			mutate: func(
				t *testing.T,
				transaction *applyPatchPreparedTransaction,
				fixture *applyPatchTxnRecoveryFixture,
			) {
				if err := transaction.publishTargets(); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(fixture.workspacePath, "target.txt")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				writeApplyPatchFixture(t, fixture.workspacePath, "target.txt", "alien\n", 0o600)
			},
			path: "target.txt",
		},
		{
			name:  "quarantined source identical-content ABA",
			setup: newApplyPatchTxnCrashMoveFixture,
			mutate: func(
				t *testing.T,
				transaction *applyPatchPreparedTransaction,
				fixture *applyPatchTxnRecoveryFixture,
			) {
				if err := transaction.quarantineSources(); err != nil {
					t.Fatal(err)
				}
				writeApplyPatchFixture(
					t,
					fixture.workspacePath,
					"source.txt",
					"before move\n",
					0o751,
				)
			},
			path: "source.txt",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := test.setup(t)
			transaction := prepareApplyPatchTxnCrashTransaction(t, fixture)
			initializeApplyPatchTxnCrashEffects(transaction)
			test.mutate(t, transaction, fixture)
			fixture.simulateCrash(t, transaction)

			tool := newApplyPatchTxnRecoveryTool(
				t,
				fixture.workspacePath,
				fixture.stateRoot,
			)
			result := executeApplyPatch(
				t,
				tool,
				context.Background(),
				"*** Begin Patch\n*** Add File: must-not-run.txt\n+blocked\n*** End Patch",
			)
			if result == nil || !result.IsError || result.ForUser != "" {
				t.Fatalf("alien recovery result = %#v", result)
			}
			if !strings.Contains(strings.ToLower(result.ForLLM), "conflict") {
				t.Fatalf("alien recovery error = %q", result.ForLLM)
			}
			assertApplyPatchTxnTestFile(
				t,
				filepath.Join(fixture.workspacePath, test.path),
				map[string]string{
					"target.txt": "alien\n",
					"source.txt": "before move\n",
				}[test.path],
				map[string]os.FileMode{
					"target.txt": 0o600,
					"source.txt": 0o751,
				}[test.path],
			)
			blockedPath := filepath.Join(fixture.workspacePath, "must-not-run.txt")
			if _, err := os.Lstat(blockedPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("new patch ran after recovery conflict: %v", err)
			}
		})
	}
}

func TestApplyPatchTransactionRecoveryRejectsSameIdentityPostimageDrift(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "target.txt")
	transaction := prepareApplyPatchTxnCrashTransaction(t, fixture)
	initializeApplyPatchTxnCrashEffects(transaction)
	if err := transaction.publishTargets(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fixture.workspacePath, "target.txt")
	if err := os.WriteFile(target, []byte("alien in-place bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.simulateCrash(t, transaction)

	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	result := executeApplyPatch(
		t,
		tool,
		context.Background(),
		"*** Begin Patch\n*** Add File: must-not-run.txt\n+blocked\n*** End Patch",
	)
	if result == nil || !result.IsError || result.ForUser != "" {
		t.Fatalf("same-identity drift recovery result = %#v", result)
	}
	assertApplyPatchTxnTestFile(t, target, "alien in-place bytes\n", 0o644)
	if _, err := os.Lstat(filepath.Join(fixture.workspacePath, "must-not-run.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new patch ran after same-identity drift: %v", err)
	}
}

func TestApplyPatchTransactionRecoveryRequiresLivePostimageWitness(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "target.txt")
	transaction := prepareApplyPatchTxnCrashTransaction(t, fixture)
	initializeApplyPatchTxnCrashEffects(transaction)
	if err := transaction.publishTargets(); err != nil {
		t.Fatal(err)
	}
	operation := transaction.intent.operations[0]
	witness, artifactErr := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactPostimageWitness,
	)
	if artifactErr != nil || witness.Rooted.Identity == nil {
		t.Fatal(errors.Join(artifactErr, errors.New("postimage witness unavailable")))
	}
	if err := applyPatchTxnRemoveExact(
		operation.targetAnchor,
		witness.Rooted.Basename,
		witness.Rooted.RemovalBasename,
		*witness.Rooted.Identity,
		false,
	); err != nil {
		t.Fatal(err)
	}
	if err := applyPatchTxnSyncDirectory(operation.targetAnchor); err != nil {
		t.Fatal(err)
	}
	fixture.simulateCrash(t, transaction)

	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	result := executeApplyPatch(
		t,
		tool,
		context.Background(),
		"*** Begin Patch\n*** Add File: must-not-run.txt\n+blocked\n*** End Patch",
	)
	if result == nil || !result.IsError || result.ForUser != "" {
		t.Fatalf("missing-witness recovery result = %#v", result)
	}
	assertApplyPatchTxnTestFileModeNarrowed(
		t,
		filepath.Join(fixture.workspacePath, "target.txt"),
		"candidate\n",
		0o644,
	)
	if _, err := os.Lstat(filepath.Join(fixture.workspacePath, "must-not-run.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new patch ran after witness loss: %v", err)
	}
}

func TestApplyPatchTransactionRecoveryRejectsUncheckpointedSourceWitness(t *testing.T) {
	fixture := newApplyPatchTxnCrashMoveFixture(t)
	transaction := prepareApplyPatchTxnCrashTransaction(t, fixture)
	initializeApplyPatchTxnCrashEffects(transaction)
	operation := transaction.intent.operations[0]
	witness, artifactErr := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactSourceWitness,
	)
	if artifactErr != nil {
		t.Fatal(artifactErr)
	}
	if witness.Rooted.Identity != nil {
		t.Fatal("source witness unexpectedly checkpointed before creation")
	}
	expected := *transaction.journal.Operations[operation.index].Source.PreflightIdentity
	if err := applyPatchTxnLinkWitness(
		operation.source.anchor,
		operation.source.basename,
		expected,
		2,
		operation.source.anchor,
		witness.Rooted.Basename,
		witness.Rooted.RemovalBasename,
	); err != nil {
		t.Fatal(err)
	}
	if err := applyPatchTxnSyncDirectory(operation.source.anchor); err != nil {
		t.Fatal(err)
	}
	witnessPath := filepath.Join(fixture.workspacePath, witness.Rooted.Basename)
	fixture.simulateCrash(t, transaction)

	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	result := executeApplyPatch(
		t,
		tool,
		context.Background(),
		"*** Begin Patch\n*** Add File: must-not-run.txt\n+blocked\n*** End Patch",
	)
	if result == nil || !result.IsError || result.ForUser != "" ||
		!strings.Contains(result.ForLLM, "recovery conflict") {
		t.Fatalf("uncheckpointed-witness recovery result = %#v", result)
	}
	assertApplyPatchTxnTestFile(
		t,
		filepath.Join(fixture.workspacePath, "source.txt"),
		"before move\n",
		0o751,
	)
	assertApplyPatchTxnTestFile(t, witnessPath, "before move\n", 0o751)
	if _, err := os.Lstat(filepath.Join(fixture.workspacePath, "must-not-run.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new patch ran after uncheckpointed witness: %v", err)
	}
}

func TestApplyPatchTransactionRecoveryRejectsSameIdentityOriginalDrift(t *testing.T) {
	fixture := newApplyPatchTxnCrashMoveFixture(t)
	transaction := prepareApplyPatchTxnCrashTransaction(t, fixture)
	initializeApplyPatchTxnCrashEffects(transaction)
	if err := transaction.quarantineSources(); err != nil {
		t.Fatal(err)
	}
	operation := transaction.intent.operations[0]
	quarantine, artifactErr := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactSourceQuarantine,
	)
	if artifactErr != nil || quarantine.Rooted.Identity == nil {
		t.Fatal(errors.Join(artifactErr, errors.New("source quarantine unavailable")))
	}
	quarantinePath := filepath.Join(fixture.workspacePath, quarantine.Rooted.Basename)
	if err := os.WriteFile(quarantinePath, []byte("alien original bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.simulateCrash(t, transaction)

	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	result := executeApplyPatch(
		t,
		tool,
		context.Background(),
		"*** Begin Patch\n*** Add File: must-not-run.txt\n+blocked\n*** End Patch",
	)
	if result == nil || !result.IsError || result.ForUser != "" {
		t.Fatalf("same-identity original drift recovery result = %#v", result)
	}
	if _, err := os.Lstat(filepath.Join(fixture.workspacePath, "source.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tampered original was restored to the public source: %v", err)
	}
	assertApplyPatchTxnTestFile(t, quarantinePath, "alien original bytes\n", 0o751)
	if _, err := os.Lstat(filepath.Join(fixture.workspacePath, "must-not-run.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new patch ran after original drift: %v", err)
	}
}

func TestApplyPatchTransactionRecoveryForestRollbackBeforeCheckpoint(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
	transaction := prepareApplyPatchTxnCrashTransaction(t, fixture)
	initializeApplyPatchTxnCrashEffects(transaction)
	if err := transaction.publishTargets(); err != nil {
		t.Fatal(err)
	}
	markApplyPatchTxnCrashRollingBack(t, transaction)
	forestIntent := transaction.intent.forests[0]
	forest, err := requireApplyPatchTxnJournalForest(
		transaction.journal,
		forestIntent.id,
	)
	if err != nil || forest.StageRoot.Identity == nil {
		t.Fatal(errors.Join(err, errors.New("forest identity unavailable")))
	}
	if err := applyPatchTxnQuarantineExact(
		forestIntent.anchor,
		forestIntent.publicRoot,
		forestIntent.rollbackRoot,
		*forest.StageRoot.Identity,
	); err != nil {
		t.Fatal(err)
	}
	if err := applyPatchTxnSyncDirectory(forestIntent.anchor); err != nil {
		t.Fatal(err)
	}
	fixture.simulateCrash(t, transaction)

	recoverApplyPatchTxnCrashFixture(t, fixture)
	if _, err := os.Lstat(filepath.Join(fixture.workspacePath, "nested")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forest remained after rollback recovery: %v", err)
	}
	assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
	assertApplyPatchTxnFaultStateReady(t, fixture.workspacePath, fixture.stateRoot)
}

func TestApplyPatchTransactionRecoveryRejectsPublishedForestMemberDrift(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
	transaction := prepareApplyPatchTxnCrashTransaction(t, fixture)
	initializeApplyPatchTxnCrashEffects(transaction)
	if err := transaction.publishTargets(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fixture.workspacePath, "nested", "deeper", "result.txt")
	if err := os.WriteFile(target, []byte("alien forest bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.simulateCrash(t, transaction)

	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	result := executeApplyPatch(
		t,
		tool,
		context.Background(),
		"*** Begin Patch\n*** Add File: must-not-run.txt\n+blocked\n*** End Patch",
	)
	if result == nil || !result.IsError || result.ForUser != "" {
		t.Fatalf("forest drift recovery result = %#v", result)
	}
	assertApplyPatchTxnTestFileModeNarrowed(
		t,
		target,
		"alien forest bytes\n",
		0o644,
	)
	if _, err := os.Lstat(filepath.Join(fixture.workspacePath, "must-not-run.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new patch ran after forest drift: %v", err)
	}
}

func newApplyPatchTxnCrashMoveFixture(t *testing.T) *applyPatchTxnRecoveryFixture {
	t.Helper()
	workspace := t.TempDir()
	writeApplyPatchFixture(t, workspace, "source.txt", "before move\n", 0o751)
	return newApplyPatchTxnRecoveryFixtureForPatch(
		t,
		workspace,
		"*** Begin Patch\n"+
			"*** Update File: source.txt\n*** Move to: target.txt\n"+
			"*** End Patch",
	)
}

func prepareApplyPatchTxnCrashTransaction(
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
	return transaction
}

func initializeApplyPatchTxnCrashEffects(transaction *applyPatchPreparedTransaction) {
	transaction.effects = applyPatchTxnEffects{
		sourceQuarantined:         make(map[int]bool),
		targetPublished:           make(map[int]bool),
		targetRollbackQuarantined: make(map[int]bool),
		forestPublished:           make(map[string]bool),
		forestRollbackQuarantined: make(map[string]bool),
	}
}

func publishApplyPatchTxnCrashMove(
	t *testing.T,
	transaction *applyPatchPreparedTransaction,
) {
	t.Helper()
	if err := transaction.quarantineSources(); err != nil {
		t.Fatal(err)
	}
	if err := transaction.publishTargets(); err != nil {
		t.Fatal(err)
	}
}

func markApplyPatchTxnCrashRollingBack(
	t *testing.T,
	transaction *applyPatchPreparedTransaction,
) {
	t.Helper()
	transaction.journal.Phase = applyPatchTransactionPhaseRollingBack
	if err := transaction.checkpoint(); err != nil {
		t.Fatal(err)
	}
}

func quarantineApplyPatchTxnCrashSourceBeforeCheckpoint(
	t *testing.T,
	transaction *applyPatchPreparedTransaction,
) {
	t.Helper()
	operation := transaction.intent.operations[0]
	journalOperation := &transaction.journal.Operations[operation.index]
	expected := *journalOperation.Source.PreflightIdentity
	witness, artifactErr := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactSourceWitness,
	)
	if artifactErr != nil {
		t.Fatal(artifactErr)
	}
	if err := applyPatchTxnLinkWitness(
		operation.source.anchor,
		operation.source.basename,
		expected,
		2,
		operation.source.anchor,
		witness.Rooted.Basename,
		witness.Rooted.RemovalBasename,
	); err != nil {
		t.Fatal(err)
	}
	witness.Rooted.Identity = copyApplyPatchTxnIdentity(expected)
	witness.Rooted.Links = 2
	if err := applyPatchTxnSyncDirectory(operation.source.anchor); err != nil {
		t.Fatal(err)
	}
	if err := transaction.checkpoint(); err != nil {
		t.Fatal(err)
	}
	quarantine, artifactErr := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactSourceQuarantine,
	)
	if artifactErr != nil {
		t.Fatal(artifactErr)
	}
	if err := applyPatchTxnQuarantineExact(
		operation.source.anchor,
		operation.source.basename,
		quarantine.Rooted.Basename,
		expected,
	); err != nil {
		t.Fatal(err)
	}
	if err := applyPatchTxnSyncDirectory(operation.source.anchor); err != nil {
		t.Fatal(err)
	}
}

func publishApplyPatchTxnCrashTargetBeforeCheckpoint(
	t *testing.T,
	transaction *applyPatchPreparedTransaction,
) {
	t.Helper()
	operation := transaction.intent.operations[0]
	if err := applyPatchTxnRenameNoReplace(
		operation.targetAnchor,
		operation.stageName,
		operation.targetAnchor,
		operation.targetLayout.components[0],
	); err != nil {
		t.Fatal(err)
	}
	if err := applyPatchTxnSyncDirectory(operation.targetAnchor); err != nil {
		t.Fatal(err)
	}
}

func quarantineApplyPatchTxnCrashTarget(
	t *testing.T,
	transaction *applyPatchPreparedTransaction,
) {
	t.Helper()
	operation := transaction.intent.operations[0]
	stage, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactPostimageStage,
	)
	if err != nil || stage.Rooted.Identity == nil {
		t.Fatal(errors.Join(err, errors.New("postimage identity unavailable")))
	}
	rollback, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactTargetRollbackQuarantine,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyPatchTxnQuarantineExact(
		operation.targetAnchor,
		operation.targetLayout.components[0],
		rollback.Rooted.Basename,
		*stage.Rooted.Identity,
	); err != nil {
		t.Fatal(err)
	}
	rollback.Rooted.Identity = copyApplyPatchTxnIdentity(*stage.Rooted.Identity)
	rollback.Rooted.Links = 2
	transaction.effects.targetPublished[operation.index] = false
	transaction.effects.targetRollbackQuarantined[operation.index] = true
	if err := applyPatchTxnSyncDirectory(operation.targetAnchor); err != nil {
		t.Fatal(err)
	}
	if err := transaction.checkpoint(); err != nil {
		t.Fatal(err)
	}
}

func quarantineApplyPatchTxnCrashTargetBeforeCheckpoint(
	t *testing.T,
	transaction *applyPatchPreparedTransaction,
) {
	t.Helper()
	operation := transaction.intent.operations[0]
	stage, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactPostimageStage,
	)
	if err != nil || stage.Rooted.Identity == nil {
		t.Fatal(errors.Join(err, errors.New("postimage identity unavailable")))
	}
	rollback, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		operation.index,
		applyPatchTransactionArtifactTargetRollbackQuarantine,
	)
	if err != nil {
		t.Fatal(err)
	}
	if rollback.Rooted.Identity != nil {
		t.Fatal("target rollback unexpectedly checkpointed")
	}
	if err := applyPatchTxnQuarantineExact(
		operation.targetAnchor,
		operation.targetLayout.components[0],
		rollback.Rooted.Basename,
		*stage.Rooted.Identity,
	); err != nil {
		t.Fatal(err)
	}
	if err := applyPatchTxnSyncDirectory(operation.targetAnchor); err != nil {
		t.Fatal(err)
	}
}

func recoverApplyPatchTxnCrashFixture(
	t *testing.T,
	fixture *applyPatchTxnRecoveryFixture,
) {
	t.Helper()
	state, workspaceState := fixture.reopen(t)
	tool := newApplyPatchTxnRecoveryTool(
		t,
		fixture.workspacePath,
		fixture.stateRoot,
	)
	if err := tool.recoverApplyPatchTransaction(
		context.Background(),
		state,
		workspaceState,
		fixture.workspace,
	); err != nil {
		t.Fatal(err)
	}
	fixture.closeReopened(t, state, workspaceState)
}
