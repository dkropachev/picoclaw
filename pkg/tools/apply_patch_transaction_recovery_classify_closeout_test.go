package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionRecoveryClassifyCloseoutUncheckpointedRollback(t *testing.T) {
	for _, state := range []string{"rolling rollback", "outside rollback", "wrong identity"} {
		t.Run(state, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
			tx := prepareApplyPatchTxnCrashTransaction(t, fixture)
			defer tx.closeHandles()
			initializeApplyPatchTxnCrashEffects(tx)
			operation := tx.intent.operations[0]
			stage, _ := requireApplyPatchTxnArtifact(
				tx.journal, 0, applyPatchTransactionArtifactPostimageStage,
			)
			rollback, _ := requireApplyPatchTxnArtifact(
				tx.journal, 0, applyPatchTransactionArtifactTargetRollbackQuarantine,
			)
			stagePath := filepath.Join(operation.targetAnchor.canonical, stage.Rooted.Basename)
			rollbackPath := filepath.Join(operation.targetAnchor.canonical, rollback.Rooted.Basename)
			if state == "wrong identity" {
				if err := os.WriteFile(rollbackPath, []byte("alien\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Rename(stagePath, rollbackPath); err != nil {
				t.Fatal(err)
			}
			if state == "outside rollback" {
				tx.journal.Phase = applyPatchTransactionPhasePrepared
			} else {
				tx.journal.Phase = applyPatchTransactionPhaseRollingBack
			}
			err := classifyApplyPatchTxnRecoveryTarget(
				tx,
				operation,
				stage,
				applyPatchTxnRecoveryAbsent,
			)
			if state == "rolling rollback" {
				if err != nil || rollback.Rooted.Identity == nil ||
					!tx.effects.targetRollbackQuarantined[0] {
					t.Fatalf("uncheckpointed rollback = %+v, %v", rollback, err)
				}
			} else if err == nil {
				t.Fatal("invalid uncheckpointed rollback was accepted")
			}
		})
	}
}

func TestApplyPatchTransactionRecoveryClassifyCloseoutTargetPhaseMatrix(t *testing.T) {
	tests := []struct {
		name   string
		phase  applyPatchTransactionPhase
		mutate func(*testing.T, *applyPatchPreparedTransaction, *applyPatchTxnIntent)
		valid  bool
	}{
		{"preparing staged", applyPatchTransactionPhasePreparing, nil, true},
		{"prepared staged", applyPatchTransactionPhasePrepared, nil, true},
		{"committed staged invalid", applyPatchTransactionPhaseCommitted, nil, false},
		{
			"prepared duplicate public stage",
			applyPatchTransactionPhasePrepared,
			func(t *testing.T, tx *applyPatchPreparedTransaction, operation *applyPatchTxnIntent) {
				stage, _ := requireApplyPatchTxnArtifact(
					tx.journal, 0, applyPatchTransactionArtifactPostimageStage,
				)
				if err := os.Link(
					filepath.Join(operation.targetAnchor.canonical, stage.Rooted.Basename),
					operation.planned.targetPath,
				); err != nil {
					t.Fatal(err)
				}
			},
			false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
			tx := fixture.begin(t)
			defer tx.abortPreparing()
			initializeApplyPatchTxnCrashEffects(tx)
			operation := tx.intent.operations[0]
			stage, _ := requireApplyPatchTxnArtifact(
				tx.journal, 0, applyPatchTransactionArtifactPostimageStage,
			)
			tx.journal.Phase = test.phase
			if test.mutate != nil {
				test.mutate(t, tx, operation)
			}
			err := classifyApplyPatchTxnRecoveryTarget(
				tx, operation, stage, applyPatchTxnRecoveryAbsent,
			)
			if test.valid && err != nil {
				t.Fatalf("valid target phase = %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("invalid target phase was accepted")
			}
		})
	}
}

func TestApplyPatchTransactionRecoveryClassifyCloseoutUncheckpointedQuarantine(t *testing.T) {
	fixture := newApplyPatchTxnCrashMoveFixture(t)
	tx := prepareApplyPatchTxnCrashTransaction(t, fixture)
	defer tx.closeHandles()
	initializeApplyPatchTxnCrashEffects(tx)
	quarantineApplyPatchTxnCrashSourceBeforeCheckpoint(t, tx)
	operation := tx.intent.operations[0]
	quarantine, _ := requireApplyPatchTxnArtifact(
		tx.journal, 0, applyPatchTransactionArtifactSourceQuarantine,
	)
	state, err := inspectApplyPatchTxnUncheckpointedQuarantine(
		operation, quarantine, tx.journal,
	)
	if err != nil || state != applyPatchTxnRecoveryOriginal || quarantine.Rooted.Identity == nil {
		t.Fatalf("uncheckpointed source quarantine = %q, %+v, %v", state, quarantine, err)
	}
	if _, err := inspectApplyPatchTxnUncheckpointedQuarantine(
		operation,
		&applyPatchTransactionJournalArtifact{
			Rooted: &applyPatchTransactionJournalRootedLocation{
				Basename: "missing",
			},
		},
		tx.journal,
	); err != nil {
		t.Fatalf("absent uncheckpointed quarantine = %v", err)
	}
}

func TestApplyPatchTransactionRecoveryClassifyCloseoutIdentityMismatch(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "object")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchor, err := openApplyPatchTxnAnchor(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer anchor.Close()
	identity, _, err := applyPatchTxnIdentityAt(anchor, "object")
	if err != nil {
		t.Fatal(err)
	}
	wrong := identity
	wrong.File++
	if present, err := applyPatchTxnRecoveryIdentityPresent(anchor, "object", wrong); present || err == nil {
		t.Fatalf("wrong identity present = %v, %v", present, err)
	}
	if _, err := inspectApplyPatchTxnRecoveryObject(
		anchor,
		"object",
		map[applyPatchTxnRecoveryObjectState]applyPatchTxnIdentity{
			applyPatchTxnRecoveryOriginal: wrong,
		},
	); err == nil {
		t.Fatal("wrong recovery object identity classified")
	}
}
