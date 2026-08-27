package tools

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionEffectZeroCloseoutRetainedSourceFailures(t *testing.T) {
	for _, corruption := range []string{
		"missing witness metadata",
		"changed quarantine",
		"replaced witness",
		"public collision",
		"checkpoint interruption",
	} {
		t.Run(corruption, func(t *testing.T) {
			transaction := newApplyPatchTxnEffectCloseoutUpdate(t)
			operation := transaction.intent.operations[0]
			if err := transaction.quarantineSources(); err != nil {
				t.Fatal(err)
			}
			quarantine, err := requireApplyPatchTxnArtifact(
				transaction.journal, 0, applyPatchTransactionArtifactSourceQuarantine,
			)
			if err != nil {
				t.Fatal(err)
			}
			witness, err := requireApplyPatchTxnArtifact(
				transaction.journal, 0, applyPatchTransactionArtifactSourceWitness,
			)
			if err != nil {
				t.Fatal(err)
			}
			switch corruption {
			case "missing witness metadata":
				removeApplyPatchTxnCloseoutArtifact(
					t, transaction.journal, 0,
					applyPatchTransactionArtifactSourceWitness,
				)
			case "changed quarantine":
				if err := os.WriteFile(
					filepath.Join(operation.source.anchor.canonical, quarantine.Rooted.Basename),
					[]byte("changed\n"), 0o600,
				); err != nil {
					t.Fatal(err)
				}
			case "replaced witness":
				path := filepath.Join(operation.source.anchor.canonical, witness.Rooted.Basename)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("alien\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "public collision":
				if err := os.WriteFile(operation.planned.sourcePath, []byte("alien\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "checkpoint interruption":
				transaction.fault = func(boundary string) error {
					if boundary == "journal_replace_before_rename" {
						return errors.New("restore checkpoint interrupted")
					}
					return nil
				}
			}
			if err := transaction.restoreSource(operation); err == nil {
				t.Fatal("corrupt retained source restore succeeded")
			}
		})
	}
}

func TestApplyPatchTransactionEffectZeroCloseoutWitnessCleanupMetadata(t *testing.T) {
	for _, role := range []applyPatchTransactionArtifactRole{
		applyPatchTransactionArtifactSourceWitness,
		applyPatchTransactionArtifactSourceRestoreStage,
	} {
		t.Run("missing "+string(role), func(t *testing.T) {
			transaction := newApplyPatchTxnEffectCloseoutUpdate(t)
			removeApplyPatchTxnCloseoutArtifact(t, transaction.journal, 0, role)
			if err := transaction.cleanupRollbackSourceWitnesses(); err == nil {
				t.Fatal("missing rollback witness metadata cleanup succeeded")
			}
		})
	}

	t.Run("changed restore identity", func(t *testing.T) {
		_, transaction, operation, restore := prepareApplyPatchTxnBackupFallbackStage(t)
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
		if err := applyPatchTxnWriteRegular(file, backup, 0o600, true); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		identity.File++
		restore.Rooted.Identity = &identity
		if err := transaction.cleanupRollbackSourceWitnesses(); err == nil {
			t.Fatal("changed restore identity cleanup succeeded")
		}
	})
}

func TestApplyPatchTransactionEffectZeroCloseoutUnpublishedTargetFailures(t *testing.T) {
	for _, corruption := range []string{
		"missing witness",
		"missing stage",
		"changed witness",
		"changed stage",
	} {
		t.Run(corruption, func(t *testing.T) {
			transaction := newApplyPatchTxnEffectCloseoutTransaction(
				t,
				"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
				nil,
			)
			operation := transaction.intent.operations[0]
			role := applyPatchTransactionArtifactPostimageWitness
			if corruption == "missing stage" || corruption == "changed stage" {
				role = applyPatchTransactionArtifactPostimageStage
			}
			if corruption == "missing witness" || corruption == "missing stage" {
				removeApplyPatchTxnCloseoutArtifact(t, transaction.journal, 0, role)
			} else {
				artifact, err := requireApplyPatchTxnArtifact(transaction.journal, 0, role)
				if err != nil || artifact.Rooted.Identity == nil {
					t.Fatal(errors.Join(err, errors.New("target artifact unavailable")))
				}
				artifact.Rooted.Identity.File++
			}
			if err := cleanupApplyPatchTxnUnpublishedTarget(
				operation, transaction.journal, transaction.checkpoint,
			); err == nil {
				t.Fatal("corrupt unpublished target cleanup succeeded")
			}
		})
	}
}

func TestApplyPatchTransactionEffectZeroCloseoutForestRemovalFailures(t *testing.T) {
	for _, corruption := range []string{
		"public root absent",
		"changed sentinel",
		"uncheckpointed entry",
		"missing root identity",
	} {
		t.Run(corruption, func(t *testing.T) {
			transaction := newApplyPatchTxnEffectCloseoutTransaction(
				t,
				"*** Begin Patch\n*** Add File: nested/result.txt\n+candidate\n*** End Patch",
				nil,
			)
			intent := transaction.intent.forests[0]
			forest, err := requireApplyPatchTxnJournalForest(transaction.journal, intent.id)
			if err != nil {
				t.Fatal(err)
			}
			switch corruption {
			case "public root absent":
				if err := transaction.rollbackPublishedForest(intent, forest); err == nil {
					t.Fatal("absent public forest rollback succeeded")
				}
				return
			case "changed sentinel":
				forest.SentinelWitness.Identity.File++
			case "uncheckpointed entry":
				forest.SentinelWitness.Identity = nil
				forest.Entries[len(forest.Entries)-1].Identity = nil
			case "missing root identity":
				forest.SentinelWitness.Identity = nil
				forest.StageRoot.Identity = nil
			}
			if err := removeApplyPatchTxnForestTree(
				intent, forest, intent.stageRoot, transaction.checkpoint,
			); err == nil {
				t.Fatal("corrupt forest removal succeeded")
			}
		})
	}
}

func TestApplyPatchTransactionEffectZeroCloseoutBackupFinishMetadata(t *testing.T) {
	_, transaction, operation, _ := prepareApplyPatchTxnBackupFallbackStage(t)
	quarantine, err := requireApplyPatchTxnArtifact(
		transaction.journal, operation.index,
		applyPatchTransactionArtifactSourceQuarantine,
	)
	if err != nil {
		t.Fatal(err)
	}
	quarantine.Rooted.Identity = nil
	if err := transaction.finishBackupRestoredSourceCleanup(operation, nil); err == nil {
		t.Fatal("missing restore witness finish succeeded")
	}
}
