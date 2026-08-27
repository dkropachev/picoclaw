package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionEffectRestoreCloseoutMetadataFailures(t *testing.T) {
	t.Run("missing quarantine", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutUpdate(t)
		operation := transaction.intent.operations[0]
		removeApplyPatchTxnCloseoutArtifact(
			t, transaction.journal, operation.index,
			applyPatchTransactionArtifactSourceQuarantine,
		)
		if err := transaction.restoreSource(operation); err == nil {
			t.Fatal("missing quarantine restore succeeded")
		}
	})

	t.Run("missing backup witness", func(t *testing.T) {
		fixture, transaction, operation, _ := prepareApplyPatchTxnBackupFallbackStage(t)
		_ = fixture
		removeApplyPatchTxnCloseoutArtifact(
			t, transaction.journal, operation.index,
			applyPatchTransactionArtifactSourceWitness,
		)
		if err := verifyApplyPatchTxnBackupFallbackWitness(operation, transaction.journal); err == nil {
			t.Fatal("missing backup witness verified")
		}
		if err := transaction.restoreSourceFromBackupV2(operation); err == nil {
			t.Fatal("missing backup witness restore succeeded")
		}
	})

	t.Run("changed backup witness", func(t *testing.T) {
		_, transaction, operation, _ := prepareApplyPatchTxnBackupFallbackStage(t)
		witness, err := requireApplyPatchTxnArtifact(
			transaction.journal, operation.index,
			applyPatchTransactionArtifactSourceWitness,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(operation.source.anchor.canonical, witness.Rooted.Basename),
			[]byte("changed witness\n"), 0o600,
		); err != nil {
			t.Fatal(err)
		}
		if err := verifyApplyPatchTxnBackupFallbackWitness(operation, transaction.journal); err == nil {
			t.Fatal("changed backup witness verified")
		}
	})

	t.Run("missing restore artifact", func(t *testing.T) {
		_, transaction, operation, _ := prepareApplyPatchTxnBackupFallbackStage(t)
		removeApplyPatchTxnCloseoutArtifact(
			t, transaction.journal, operation.index,
			applyPatchTransactionArtifactSourceRestoreStage,
		)
		if err := transaction.restoreSourceFromBackupV2(operation); err == nil {
			t.Fatal("missing restore artifact succeeded")
		}
	})

	t.Run("backup authentication changed", func(t *testing.T) {
		_, transaction, operation, _ := prepareApplyPatchTxnBackupFallbackStage(t)
		backup, err := requireApplyPatchTxnArtifact(
			transaction.journal, operation.index,
			applyPatchTransactionArtifactBackupBlob,
		)
		if err != nil {
			t.Fatal(err)
		}
		backup.Backup.HMACSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
		if err := transaction.restoreSourceFromBackupV2(operation); err == nil {
			t.Fatal("changed backup authentication restore succeeded")
		}
	})

	t.Run("alien public source", func(t *testing.T) {
		_, transaction, operation, _ := prepareApplyPatchTxnBackupFallbackStage(t)
		if err := os.WriteFile(operation.planned.sourcePath, []byte("alien\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := transaction.restoreSourceFromBackupV2(operation); err == nil {
			t.Fatal("alien public source restore succeeded")
		}
	})
}

func TestApplyPatchTransactionEffectRestoreCloseoutRollbackFailures(t *testing.T) {
	t.Run("rolling checkpoint interruption", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
			nil,
		)
		injected := errors.New("rolling checkpoint interrupted")
		transaction.fault = func(boundary string) error {
			if boundary == "journal_replace_before_rename" {
				return injected
			}
			return nil
		}
		rollbackErr := transaction.rollback(injected)
		if !errors.Is(rollbackErr, errApplyPatchRollbackIncomplete) ||
			!errors.Is(rollbackErr, injected) {
			t.Fatalf("rolling checkpoint rollback = %v", rollbackErr)
		}
	})

	t.Run("missing unpublished target artifact", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
			nil,
		)
		removeApplyPatchTxnCloseoutArtifact(
			t, transaction.journal, 0,
			applyPatchTransactionArtifactPostimageStage,
		)
		rollbackErr := transaction.rollback(errors.New("rollback"))
		if !errors.Is(rollbackErr, errApplyPatchRollbackIncomplete) {
			t.Fatalf("missing target artifact rollback = %v", rollbackErr)
		}
	})

	t.Run("missing forest journal", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: nested/result.txt\n+candidate\n*** End Patch",
			nil,
		)
		transaction.journal.Forests = nil
		rollbackErr := transaction.rollback(errors.New("rollback"))
		if !errors.Is(rollbackErr, errApplyPatchRollbackIncomplete) {
			t.Fatalf("missing forest rollback = %v", rollbackErr)
		}
	})

	t.Run("published target missing stage metadata", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
			nil,
		)
		transaction.effects.targetPublished[0] = true
		stage, err := requireApplyPatchTxnArtifact(
			transaction.journal, 0, applyPatchTransactionArtifactPostimageStage,
		)
		if err != nil {
			t.Fatal(err)
		}
		stage.Rooted.Identity = nil
		if err := transaction.rollbackPublishedTarget(transaction.intent.operations[0]); err == nil {
			t.Fatal("missing stage rollback succeeded")
		}
	})

	t.Run("rollback quarantine missing witness", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
			nil,
		)
		operation := transaction.intent.operations[0]
		if err := transaction.publishTargets(); err != nil {
			t.Fatal(err)
		}
		if err := transaction.rollbackPublishedTarget(operation); err != nil {
			t.Fatal(err)
		}
		removeApplyPatchTxnCloseoutArtifact(
			t, transaction.journal, 0,
			applyPatchTransactionArtifactPostimageWitness,
		)
		if err := transaction.finishRollbackQuarantinedTarget(operation); err == nil {
			t.Fatal("missing rollback witness cleanup succeeded")
		}
	})
}

func TestApplyPatchTransactionEffectRestoreCloseoutRollingForestCorruption(t *testing.T) {
	for _, corruption := range []string{
		"missing identity",
		"wrong root identity",
		"uncheckpointed entry",
		"changed file",
		"alien entry",
	} {
		t.Run(corruption, func(t *testing.T) {
			transaction := newApplyPatchTxnEffectCloseoutTransaction(
				t,
				"*** Begin Patch\n*** Add File: nested/deeper/result.txt\n+candidate\n*** End Patch",
				nil,
			)
			intent := transaction.intent.forests[0]
			forest, err := requireApplyPatchTxnJournalForest(transaction.journal, intent.id)
			if err != nil {
				t.Fatal(err)
			}
			switch corruption {
			case "missing identity":
				forest.StageRoot.Identity = nil
			case "wrong root identity":
				forest.StageRoot.Identity.File++
			case "uncheckpointed entry":
				forest.Entries[len(forest.Entries)-1].Identity = nil
			case "changed file":
				if err := os.WriteFile(
					filepath.Join(intent.anchorPath, intent.stageRoot, "deeper", "result.txt"),
					[]byte("changed\n"), 0o600,
				); err != nil {
					t.Fatal(err)
				}
			case "alien entry":
				if err := os.WriteFile(
					filepath.Join(intent.anchorPath, intent.stageRoot, "alien"),
					[]byte("alien"), 0o600,
				); err != nil {
					t.Fatal(err)
				}
			}
			if err := verifyApplyPatchTxnRollingForestTreeAt(
				intent, forest, intent.stageRoot,
			); err == nil {
				t.Fatal("corrupt rolling forest verified")
			}
		})
	}
}

func TestApplyPatchTransactionEffectRestoreCloseoutPostimageWitnessLinks(t *testing.T) {
	transaction := newApplyPatchTxnEffectCloseoutTransaction(
		t,
		"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
		nil,
	)
	operation := transaction.intent.operations[0]
	witness, artifactErr := requireApplyPatchTxnArtifact(
		transaction.journal, 0, applyPatchTransactionArtifactPostimageWitness,
	)
	if artifactErr != nil || witness.Rooted.Identity == nil {
		t.Fatal(errors.Join(artifactErr, errors.New("postimage witness unavailable")))
	}
	if err := os.Remove(filepath.Join(operation.targetAnchor.canonical, witness.Rooted.Basename)); err != nil {
		t.Fatal(err)
	}
	if _, err := committedApplyPatchTxnPostimageLinks(operation, transaction.journal, false); err == nil {
		t.Fatal("missing committed witness succeeded")
	}
	links, err := committedApplyPatchTxnPostimageLinks(operation, transaction.journal, true)
	if err != nil || links != 1 {
		t.Fatalf("authenticated missing witness links = %d, %v", links, err)
	}
}

func TestApplyPatchTransactionEffectRestoreCloseoutCancellation(t *testing.T) {
	_, transaction, operation, _ := prepareApplyPatchTxnBackupFallbackStage(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	backup, err := transaction.store.readBackup(
		transaction.key[:], transaction.journal, operation.index,
	)
	if err != nil {
		t.Fatal(err)
	}
	restore, err := requireApplyPatchTxnArtifact(
		transaction.journal, operation.index,
		applyPatchTransactionArtifactSourceRestoreStage,
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
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	restore.Rooted.Identity = copyApplyPatchTxnIdentity(identity)
	restore.Rooted.Links = 1
	if err := applyPatchTxnResumeRegularContext(
		canceled,
		operation.source.anchor,
		restore.Rooted.Basename,
		identity,
		backup,
		os.FileMode(transaction.journal.Operations[operation.index].Before.Mode),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled restore resume = %v", err)
	}
}
