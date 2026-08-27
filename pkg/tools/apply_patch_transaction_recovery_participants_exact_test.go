package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionRecoveryParticipantsExactSourceFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *applyPatchPreparedTransaction, *applyPatchTxnIntent)
	}{
		{
			"missing witness artifact",
			func(_ *testing.T, tx *applyPatchPreparedTransaction, _ *applyPatchTxnIntent) {
				removeApplyPatchTxnExactArtifact(tx.journal, applyPatchTransactionArtifactSourceWitness)
			},
		},
		{
			"quarantine without witness",
			func(_ *testing.T, tx *applyPatchPreparedTransaction, operation *applyPatchTxnIntent) {
				tx.effects.sourceQuarantined[operation.index] = true
			},
		},
		{
			"missing restore artifact",
			func(t *testing.T, tx *applyPatchPreparedTransaction, operation *applyPatchTxnIntent) {
				if err := os.Remove(operation.planned.sourcePath); err != nil {
					t.Fatal(err)
				}
				removeApplyPatchTxnExactArtifact(
					tx.journal, applyPatchTransactionArtifactSourceRestoreStage,
				)
			},
		},
		{
			"missing backup fallback witness",
			func(t *testing.T, tx *applyPatchPreparedTransaction, operation *applyPatchTxnIntent) {
				if err := os.Remove(operation.planned.sourcePath); err != nil {
					t.Fatal(err)
				}
				tx.effects.sourceRestoreRequired[operation.index] = true
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApplyPatchTxnCrashMoveFixture(t)
			tx := prepareApplyPatchTxnCrashTransaction(t, fixture)
			defer tx.closeHandles()
			initializeApplyPatchTxnCrashEffects(tx)
			tx.effects.sourceRestoreRequired = make(map[int]bool)
			operation := tx.intent.operations[0]
			test.mutate(t, tx, operation)
			if err := validateApplyPatchTxnRecoverySourceParticipants(tx, operation, false); err == nil {
				t.Fatal("invalid source participants were accepted")
			}
		})
	}
}

func TestApplyPatchTransactionRecoveryParticipantsExactSourceDrift(t *testing.T) {
	for _, participant := range []string{"witness", "quarantine"} {
		t.Run(participant, func(t *testing.T) {
			fixture := newApplyPatchTxnCrashMoveFixture(t)
			tx := prepareApplyPatchTxnCrashTransaction(t, fixture)
			defer tx.closeHandles()
			initializeApplyPatchTxnCrashEffects(tx)
			tx.effects.sourceRestoreRequired = make(map[int]bool)
			if err := tx.quarantineSources(); err != nil {
				t.Fatal(err)
			}
			role := applyPatchTransactionArtifactSourceWitness
			if participant == "quarantine" {
				role = applyPatchTransactionArtifactSourceQuarantine
			}
			artifact, _ := requireApplyPatchTxnArtifact(tx.journal, 0, role)
			path := filepath.Join(artifact.Rooted.AnchorCanonicalPath, artifact.Rooted.Basename)
			if err := os.WriteFile(path, []byte("drift\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := validateApplyPatchTxnRecoverySourceParticipants(
				tx, tx.intent.operations[0], false,
			); err == nil {
				t.Fatal("drifted source participant was accepted")
			}
		})
	}
}

func TestApplyPatchTransactionRecoveryParticipantsExactTargetFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *applyPatchPreparedTransaction, *applyPatchTxnIntent)
	}{
		{
			"uncheckpointed stage",
			func(_ *testing.T, tx *applyPatchPreparedTransaction, _ *applyPatchTxnIntent) {
				stage, _ := requireApplyPatchTxnArtifact(
					tx.journal, 0, applyPatchTransactionArtifactPostimageStage,
				)
				stage.Rooted.Identity = nil
			},
		},
		{
			"missing witness artifact",
			func(_ *testing.T, tx *applyPatchPreparedTransaction, _ *applyPatchTxnIntent) {
				removeApplyPatchTxnExactArtifact(tx.journal, applyPatchTransactionArtifactPostimageWitness)
			},
		},
		{
			"witness identity conflict",
			func(_ *testing.T, tx *applyPatchPreparedTransaction, _ *applyPatchTxnIntent) {
				witness, _ := requireApplyPatchTxnArtifact(
					tx.journal, 0, applyPatchTransactionArtifactPostimageWitness,
				)
				witness.Rooted.Identity.File++
			},
		},
		{
			"missing rollback artifact",
			func(_ *testing.T, tx *applyPatchPreparedTransaction, operation *applyPatchTxnIntent) {
				tx.effects.targetRollbackQuarantined[operation.index] = true
				removeApplyPatchTxnExactArtifact(
					tx.journal, applyPatchTransactionArtifactTargetRollbackQuarantine,
				)
			},
		},
		{
			"missing postimage participant",
			func(t *testing.T, tx *applyPatchPreparedTransaction, operation *applyPatchTxnIntent) {
				stage, _ := requireApplyPatchTxnArtifact(
					tx.journal, 0, applyPatchTransactionArtifactPostimageStage,
				)
				if err := os.Remove(
					filepath.Join(operation.targetAnchor.canonical, stage.Rooted.Basename),
				); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
			tx := prepareApplyPatchTxnCrashTransaction(t, fixture)
			defer tx.closeHandles()
			initializeApplyPatchTxnCrashEffects(tx)
			operation := tx.intent.operations[0]
			test.mutate(t, tx, operation)
			if err := validateApplyPatchTxnRecoveryTargetParticipants(tx, operation, false); err == nil {
				t.Fatal("invalid target participants were accepted")
			}
		})
	}
}

func TestApplyPatchTransactionRecoveryParticipantsExactForestFailures(t *testing.T) {
	for _, state := range []string{"uncheckpointed", "missing staged", "published absent", "rollback absent"} {
		t.Run(state, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
			tx := prepareApplyPatchTxnCrashTransaction(t, fixture)
			defer tx.closeHandles()
			initializeApplyPatchTxnCrashEffects(tx)
			intent := tx.intent.forests[0]
			forest := &tx.journal.Forests[0]
			switch state {
			case "uncheckpointed":
				forest.StageRoot.Identity = nil
			case "missing staged":
				if err := os.Rename(
					filepath.Join(intent.anchorPath, intent.stageRoot),
					filepath.Join(intent.anchorPath, "detached"),
				); err != nil {
					t.Fatal(err)
				}
			case "published absent":
				tx.effects.forestPublished[intent.id] = true
			case "rollback absent":
				tx.effects.forestRollbackQuarantined[intent.id] = true
			}
			if err := validateApplyPatchTxnRecoveryParticipants(tx); err == nil {
				t.Fatal("invalid forest participants were accepted")
			}
		})
	}
}

func removeApplyPatchTxnExactArtifact(
	journal *applyPatchTransactionJournal,
	role applyPatchTransactionArtifactRole,
) {
	for index := range journal.Artifacts {
		if journal.Artifacts[index].Role == role {
			journal.Artifacts = append(journal.Artifacts[:index], journal.Artifacts[index+1:]...)
			return
		}
	}
}
